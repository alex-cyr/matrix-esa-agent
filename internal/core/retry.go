package core

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Retry classification for Vertex AI call failures.
//
// The loop this replaces retried *every* error twice with a flat 5s sleep.
// That burned three attempts on the 1000-page `InvalidArgument` twice in a row
// — an error no number of retries can fix — while giving a genuine 429 a
// backoff far too short to clear a quota window. Classification separates the
// two: permanent errors surface on the first attempt with their reason, quota
// errors get a backoff long enough to matter, and transport blips get short
// retries.

// retryClass says how the retry loop should treat a failed call.
type retryClass int

const (
	// retryNever marks a permanent error. Another attempt cannot succeed, so
	// the call fails immediately with the classified reason.
	retryNever retryClass = iota
	// retryTransient marks a transport-level or server-side blip. Short
	// retries.
	retryTransient
	// retryThrottled marks quota or rate-limit exhaustion. Long, escalating
	// backoff — a 5s retry against a per-minute quota just fails again.
	retryThrottled
)

func (c retryClass) String() string {
	switch c {
	case retryTransient:
		return "transient"
	case retryThrottled:
		return "throttled"
	default:
		return "permanent"
	}
}

const (
	// maxRetries is the number of retries *after* the initial attempt, so a
	// throttled call makes at most 4 requests over 30+60+90s. Restored to a
	// real value now that permanent errors no longer consume attempts.
	maxRetries = 3

	throttleBackoffStep  = 30 * time.Second
	transientBackoffBase = 5 * time.Second
	transientBackoffMax  = 20 * time.Second
)

// retryDelay returns how long to wait before the next attempt. attempt is
// 1-based and counts the call that just failed: throttled waits run
// 30s / 60s / 90s, transient waits 5s / 10s / 20s.
func retryDelay(c retryClass, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if c == retryThrottled {
		return time.Duration(attempt) * throttleBackoffStep
	}
	if attempt > 4 { // guard the shift below; the cap applies well before this
		return transientBackoffMax
	}
	d := transientBackoffBase << (attempt - 1)
	if d > transientBackoffMax {
		return transientBackoffMax
	}
	return d
}

// classifyRetry decides whether err is worth another attempt, and returns a
// short human-readable reason for the logs and the surfaced error.
func classifyRetry(err error) (retryClass, string) {
	if err == nil {
		return retryNever, "no error"
	}

	// Caller-side cancellation: the work is no longer wanted.
	if errors.Is(err, context.Canceled) {
		return retryNever, "context canceled"
	}

	// gRPC status — the usual shape, including errors wrapped by gax's
	// APIError. errors.As walks the wrap chain, which status.FromError does
	// not.
	var gs interface{ GRPCStatus() *status.Status }
	if errors.As(err, &gs) {
		if st := gs.GRPCStatus(); st != nil {
			return classifyGRPCCode(st.Code())
		}
	}

	// REST transport.
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return classifyHTTPStatus(gerr.Code)
	}

	return classifyTransportErr(err)
}

func classifyGRPCCode(code codes.Code) (retryClass, string) {
	switch code {
	case codes.ResourceExhausted:
		return retryThrottled, "ResourceExhausted (quota or rate limit)"

	// Server-side and transport failures that a later attempt may clear.
	// Internal and Unknown are included because Vertex returns them for
	// backend hiccups that do resolve; they are not statements about the
	// request being wrong.
	case codes.Unavailable, codes.DeadlineExceeded, codes.Aborted,
		codes.Internal, codes.Unknown:
		return retryTransient, code.String()

	// Everything else — InvalidArgument (the 1000-page document),
	// Unauthenticated, PermissionDenied, NotFound, FailedPrecondition,
	// Unimplemented, OutOfRange, DataLoss — is a property of the request or
	// the credentials. Retrying changes nothing.
	default:
		return retryNever, code.String()
	}
}

func classifyHTTPStatus(code int) (retryClass, string) {
	switch {
	case code == 429:
		return retryThrottled, "HTTP 429 (quota or rate limit)"
	case code == 408:
		return retryTransient, "HTTP 408 (request timeout)"
	case code == 501:
		// 5xx, but a permanent statement about the endpoint.
		return retryNever, "HTTP 501"
	case code >= 500:
		return retryTransient, "HTTP " + strconv.Itoa(code)
	default:
		return retryNever, "HTTP " + strconv.Itoa(code)
	}
}

// transientFragments catches connection-level failures that arrive as plain
// errors with no status attached.
var transientFragments = []string{
	"connection reset",
	"connection refused",
	"broken pipe",
	"unexpected eof",
	"tls handshake",
	"no such host",
	"i/o timeout",
	"server closed the stream",
	"goaway",
}

func classifyTransportErr(err error) (retryClass, string) {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return retryTransient, "network timeout"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return retryTransient, "connection closed"
	}

	msg := strings.ToLower(err.Error())
	for _, frag := range transientFragments {
		if strings.Contains(msg, frag) {
			return retryTransient, "transport: " + frag
		}
	}

	// Unclassified. Treated as transient rather than permanent: an unknown
	// error is not a *known* permanent one, and the cost of being wrong here
	// is a few short retries, where the cost of the opposite is aborting a
	// full report run on a blip. The reason string says "unclassified" so a
	// recurring shape is visible in the logs and can be added above.
	return retryTransient, "unclassified"
}
