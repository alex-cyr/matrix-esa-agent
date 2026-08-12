package core

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/vertexai/genai"
)

// fakeClock lets the pacer be tested without sleeping.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func testPacer(budget int) (*Pacer, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	p := NewPacer(budget, time.Minute)
	p.now = clk.now
	return p, clk
}

func TestPacerAllowsUpToBudget(t *testing.T) {
	p, _ := testPacer(1000)
	for i := 0; i < 4; i++ {
		if err := p.Reserve(context.Background(), 250, "f"); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	// The window is now exactly full; a further charge must require a wait.
	p.mu.Lock()
	wait := p.waitFor(p.now(), 250)
	p.mu.Unlock()
	if wait <= 0 {
		t.Fatal("a full window reported room; the burst this exists to stop would go straight through")
	}
}

func TestPacerReleasesAsTheWindowSlides(t *testing.T) {
	p, clk := testPacer(1000)
	for i := 0; i < 4; i++ {
		if err := p.Reserve(context.Background(), 250, "f"); err != nil {
			t.Fatal(err)
		}
	}
	// Once the first charges age out, room reappears without any waiting.
	clk.advance(61 * time.Second)
	p.mu.Lock()
	wait := p.waitFor(p.now(), 1000)
	p.mu.Unlock()
	if wait != 0 {
		t.Errorf("window did not slide: still asking to wait %v", wait)
	}
}

// The observed burst: ~40 MB of media inside one minute. With the default
// estimate that is well over a 1M budget, so the pacer must hold.
func TestPacerHoldsTheObservedBurst(t *testing.T) {
	p, _ := testPacer(800_000)
	sizes := []int{9_489_450, 9_527_015, 1_125_327, 19_020_010, 1_623_942} // 12:39:47-12:40:47

	held := false
	for _, b := range sizes {
		est := EstimateMediaTokens(b)
		p.mu.Lock()
		wait := p.waitFor(p.now(), est)
		if wait > 0 {
			held = true
		} else {
			p.charges = append(p.charges, charge{at: p.now(), tokens: est})
		}
		p.mu.Unlock()
	}
	if !held {
		t.Fatal("the exact burst that exhausted the regional quota passed unpaced")
	}
}

// A single file larger than the whole budget cannot be paced under it. The
// pacer must not stall forever pretending otherwise.
func TestPacerDoesNotStallOnOversizedSingleRequest(t *testing.T) {
	p, _ := testPacer(1000)
	done := make(chan error, 1)
	go func() { done <- p.Reserve(context.Background(), 5000, "huge.pdf") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Reserve stalled on a request no wait can accommodate")
	}
}

func TestPacerRespectsContextCancellation(t *testing.T) {
	p, _ := testPacer(100)
	if err := p.Reserve(context.Background(), 100, "first"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Reserve(ctx, 100, "second"); err == nil {
		t.Error("a cancelled request kept waiting; a 60s hold must not outlive its request")
	}
}

func TestEstimateMediaTokensIsConservative(t *testing.T) {
	// The estimate must run HIGH: under-estimating costs the whole run, while
	// over-estimating costs a short wait.
	const oneMB = 1 << 20
	got := EstimateMediaTokens(oneMB)
	if got < 20_000 {
		t.Errorf("EstimateMediaTokens(1MB) = %d, too low to protect a 1M/min ceiling", got)
	}
}

// The bug this fix exists for: pacing the caller's loop charged once per file
// while Agent.Execute could send that file up to maxRetries+1 times. On the
// 2026-08-12 run that was 12 uncharged parser retries, and the region rejected
// a 24k-token pipeline call because the budget had no idea what had been spent.
//
// Charging happens in Agent.reserve, which Execute calls before EVERY attempt.
// This drives that method directly -- a genai client cannot be exercised
// without credentials, so the assertion is on the charging path rather than on
// a live retry.
func TestAgentChargesEveryAttemptIncludingRetries(t *testing.T) {
	p, _ := testPacer(10_000_000) // large enough that nothing blocks
	a := &Agent{Cfg: AgentConfig{Name: "ParserAgent"}, Pacer: p}

	parts := []genai.Part{
		genai.Text("Extract text and tables from this document: big.pdf"),
		genai.Blob{MIMEType: "application/pdf", Data: make([]byte, 9_000_000)},
	}
	perAttempt := estimatePartsTokens(parts)
	if perAttempt < 100_000 {
		t.Fatalf("estimate for a 9 MB blob is implausibly low: %d", perAttempt)
	}

	const attempts = 4 // one initial call plus maxRetries
	for i := 0; i < attempts; i++ {
		if err := a.reserve(context.Background(), parts); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}

	p.mu.Lock()
	charged := p.prune(p.now())
	p.mu.Unlock()

	if want := perAttempt * attempts; charged != want {
		t.Errorf("charged %d tokens for %d attempts, want %d — retries are not being charged, "+
			"which is exactly the under-count that let a burst through", charged, attempts, want)
	}
}

func TestAgentWithoutPacerIsUnpaced(t *testing.T) {
	a := &Agent{Cfg: AgentConfig{Name: "X"}}
	if err := a.reserve(context.Background(), []genai.Part{genai.Text("hi")}); err != nil {
		t.Errorf("a nil pacer must be a no-op, got %v", err)
	}
}
