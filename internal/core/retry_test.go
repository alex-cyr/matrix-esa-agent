package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The classifier exists to stop two specific wastes seen in production: three
// retries burned on a permanent 1000-page InvalidArgument, and a 5s backoff
// against a per-minute quota. Both are asserted below by name.

func TestClassifyRetryGRPCCodes(t *testing.T) {
	cases := []struct {
		name string
		code codes.Code
		want retryClass
	}{
		{"quota exhausted backs off long", codes.ResourceExhausted, retryThrottled},

		{"unavailable is transient", codes.Unavailable, retryTransient},
		{"deadline exceeded is transient", codes.DeadlineExceeded, retryTransient},
		{"aborted is transient", codes.Aborted, retryTransient},
		{"backend internal error is transient", codes.Internal, retryTransient},
		{"unknown is transient", codes.Unknown, retryTransient},

		{"invalid argument is permanent", codes.InvalidArgument, retryNever},
		{"unauthenticated is permanent", codes.Unauthenticated, retryNever},
		{"permission denied is permanent", codes.PermissionDenied, retryNever},
		{"not found is permanent", codes.NotFound, retryNever},
		{"failed precondition is permanent", codes.FailedPrecondition, retryNever},
		{"unimplemented is permanent", codes.Unimplemented, retryNever},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := status.Error(tc.code, "synthetic")
			got, reason := classifyRetry(err)
			if got != tc.want {
				t.Fatalf("classifyRetry(%v) = %v (%q), want %v", tc.code, got, reason, tc.want)
			}
			if reason == "" {
				t.Error("classifier returned an empty reason; the reason is surfaced in the error and the logs")
			}
		})
	}
}

// The real page-limit failure, in the shape it arrives: a gRPC status wrapped
// by the agent's own fmt.Errorf. status.FromError would miss this; errors.As
// walks the chain.
func TestClassifyRetryUnwrapsWrappedStatus(t *testing.T) {
	inner := status.Error(codes.InvalidArgument,
		"The document contains 1400 pages which exceeds the supported page limit of 1000")
	wrapped := fmt.Errorf("generation failed for %s: %w", "historical-extractor", inner)

	got, reason := classifyRetry(wrapped)
	if got != retryNever {
		t.Fatalf("wrapped page-limit error classified %v (%q), want permanent — this is the error that burned three retries twice", got, reason)
	}
}

func TestClassifyRetryUnwrapsDoubleWrappedThrottle(t *testing.T) {
	inner := status.Error(codes.ResourceExhausted, "quota exceeded")
	wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", inner))

	if got, _ := classifyRetry(wrapped); got != retryThrottled {
		t.Fatalf("double-wrapped 429 classified %v, want throttled", got)
	}
}

func TestClassifyRetryHTTPStatuses(t *testing.T) {
	cases := []struct {
		code int
		want retryClass
	}{
		{429, retryThrottled},
		{408, retryTransient},
		{500, retryTransient},
		{503, retryTransient},
		{504, retryTransient},
		{501, retryNever},
		{400, retryNever},
		{401, retryNever},
		{403, retryNever},
		{404, retryNever},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("HTTP %d", tc.code), func(t *testing.T) {
			err := fmt.Errorf("call failed: %w", &googleapi.Error{Code: tc.code, Message: "synthetic"})
			if got, reason := classifyRetry(err); got != tc.want {
				t.Fatalf("HTTP %d classified %v (%q), want %v", tc.code, got, reason, tc.want)
			}
		})
	}
}

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "dial tcp: operation timed out" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestClassifyRetryTransportErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want retryClass
	}{
		{"net timeout", fmt.Errorf("post: %w", net.Error(fakeTimeoutErr{})), retryTransient},
		{"eof", fmt.Errorf("reading response: %w", io.EOF), retryTransient},
		{"unexpected eof", io.ErrUnexpectedEOF, retryTransient},
		{"connection reset", errors.New("read tcp 10.0.0.1:443: connection reset by peer"), retryTransient},
		{"tls handshake", errors.New("net/http: TLS handshake timeout"), retryTransient},
		{"unclassified defaults to transient", errors.New("something nobody has seen before"), retryTransient},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, reason := classifyRetry(tc.err); got != tc.want {
				t.Fatalf("classified %v (%q), want %v", got, reason, tc.want)
			}
		})
	}
}

func TestClassifyRetryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got, _ := classifyRetry(fmt.Errorf("call: %w", ctx.Err())); got != retryNever {
		t.Fatalf("canceled context classified %v, want permanent — the caller has already given up", got)
	}
}

func TestClassifyRetryNil(t *testing.T) {
	if got, _ := classifyRetry(nil); got != retryNever {
		t.Fatalf("nil error classified %v, want permanent (no retry)", got)
	}
}

func TestRetryDelaySchedules(t *testing.T) {
	throttled := []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second}
	for i, want := range throttled {
		attempt := i + 1
		if got := retryDelay(retryThrottled, attempt); got != want {
			t.Errorf("retryDelay(throttled, %d) = %v, want %v", attempt, got, want)
		}
	}

	transient := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	for i, want := range transient {
		attempt := i + 1
		if got := retryDelay(retryTransient, attempt); got != want {
			t.Errorf("retryDelay(transient, %d) = %v, want %v", attempt, got, want)
		}
	}
}

// The transient schedule must stay bounded no matter how the caller counts, so
// a future change to the attempt bookkeeping cannot produce a multi-hour sleep
// via the shift.
func TestRetryDelayTransientIsCapped(t *testing.T) {
	for attempt := 1; attempt <= 64; attempt++ {
		if got := retryDelay(retryTransient, attempt); got > transientBackoffMax {
			t.Fatalf("retryDelay(transient, %d) = %v, exceeds cap %v", attempt, got, transientBackoffMax)
		}
	}
	if got := retryDelay(retryTransient, 0); got != transientBackoffBase {
		t.Errorf("retryDelay(transient, 0) = %v, want the base delay %v", got, transientBackoffBase)
	}
}
