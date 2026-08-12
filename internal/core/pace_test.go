package core

import (
	"context"
	"testing"
	"time"
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
