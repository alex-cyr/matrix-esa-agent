package core

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"
)

// Vertex enforces a REGIONAL input-tokens-per-minute quota (us-central1 for
// this service). The daily budget is nowhere near exhausted; the per-minute
// ceiling is what fails a run.
//
// The parser loop is what bursts through it. It walks every uploaded file in
// sequence with the bytes attached as a Blob, and a Providence generate is 13
// files totalling ~72 MB. On the 2026-08-12 comparison run it pushed roughly
// 40 MB of PDFs and images into the single minute 12:39:47-12:40:47 -- four
// large PDFs and a 19 MB PNG -- and the region rejected the rest of the run.
//
// The pipeline nodes are not the problem: after Phase 5 their prompts are ~16k
// and ~19k tokens. Pacing therefore belongs on the parser loop.

const (
	// defaultTokensPerMinute leaves headroom under a 1,000,000/min regional
	// ceiling. The estimate below is rough, so the margin is deliberate.
	defaultTokensPerMinute = 800_000

	// defaultBytesPerToken converts attached media bytes into an input-token
	// estimate. Calibrated from the burst that failed: ~40 MB in one minute
	// exceeded 1M tokens, which puts the true figure at or below ~40 bytes per
	// token. 25 is used so the estimate runs HIGH -- over-estimating costs a
	// short wait, under-estimating costs the whole run.
	defaultBytesPerToken = 25
)

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		slog.Warn("PACER: ignoring unparseable env value", "var", name, "value", v)
	}
	return def
}

// EstimateMediaTokens approximates the input tokens a file will cost when
// attached to a request. Deliberately crude and deliberately high; the exact
// figure depends on page count and image resolution, which are not knowable
// without opening every document.
func EstimateMediaTokens(bytes int) int {
	perToken := envInt("VERTEX_BYTES_PER_TOKEN", defaultBytesPerToken)
	return bytes / perToken
}

type charge struct {
	at     time.Time
	tokens int
}

// Pacer keeps a rolling one-minute total under a budget, blocking callers when
// the next request would exceed it.
type Pacer struct {
	mu      sync.Mutex
	budget  int
	window  time.Duration
	charges []charge
	// now is swappable for tests.
	now func() time.Time
}

func NewPacer(budget int, window time.Duration) *Pacer {
	return &Pacer{budget: budget, window: window, now: time.Now}
}

// NewDefaultPacer builds the pacer the request path uses.
func NewDefaultPacer() *Pacer {
	return NewPacer(envInt("VERTEX_TOKENS_PER_MINUTE", defaultTokensPerMinute), time.Minute)
}

// prune drops charges that have aged out of the window. Caller holds mu.
func (p *Pacer) prune(now time.Time) int {
	cutoff := now.Add(-p.window)
	kept := p.charges[:0]
	total := 0
	for _, c := range p.charges {
		if c.at.After(cutoff) {
			kept = append(kept, c)
			total += c.tokens
		}
	}
	p.charges = kept
	return total
}

// waitFor returns how long until enough of the window frees up for est tokens.
// Caller holds mu.
func (p *Pacer) waitFor(now time.Time, est int) time.Duration {
	inWindow := p.prune(now)
	if inWindow+est <= p.budget {
		return 0
	}
	// Wait until the oldest charges have aged out far enough to fit.
	need := inWindow + est - p.budget
	freed := 0
	for _, c := range p.charges {
		freed += c.tokens
		if freed >= need {
			return c.at.Add(p.window).Sub(now)
		}
	}
	// Even an empty window cannot fit this request: a single oversized file.
	// No amount of waiting helps, so proceed and let the call succeed or fail
	// on its own merits rather than stalling forever.
	return 0
}

// Reserve blocks until the rolling window has room for est tokens, then
// records the charge. It returns early if ctx is cancelled.
func (p *Pacer) Reserve(ctx context.Context, est int, label string) error {
	for {
		p.mu.Lock()
		wait := p.waitFor(p.now(), est)
		if wait <= 0 {
			p.charges = append(p.charges, charge{at: p.now(), tokens: est})
			inWindow := p.prune(p.now())
			p.mu.Unlock()
			if est > p.budget {
				slog.Warn("PACER: single request exceeds the whole per-minute budget; no pacing can prevent a 429 here",
					"file", label, "est_tokens", est, "budget", p.budget)
			}
			slog.Debug("PACER: proceeding", "file", label, "est_tokens", est, "in_window", inWindow)
			return nil
		}
		p.mu.Unlock()

		slog.Info("PACER: holding to stay under the regional per-minute quota",
			"file", label, "est_tokens", est, "wait", wait.Round(time.Second).String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
