package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingExtractor fails a fixed number of times, then succeeds. It models the
// transient quota blip that the memo used to turn into a permanent loss.
type failingExtractor struct {
	failures int
	calls    int
}

func (f *failingExtractor) ExtractText(ctx context.Context, name string, data []byte, mimeType string) (string, error) {
	f.calls++
	if f.calls <= f.failures {
		return "", errors.New("synthetic quota error")
	}
	return "extracted:" + name, nil
}

func TestBaselineStatusDraftNote(t *testing.T) {
	complete := BaselineStatus{Used: 19, Total: 19}
	if !complete.Complete() {
		t.Error("19/19 should be complete")
	}
	if note := complete.DraftNote(); note != "" {
		t.Errorf("complete baseline emitted a draft note: %q", note)
	}

	partial := BaselineStatus{Used: 18, Total: 19, Missing: []string{"Hidden Hills.pdf"}}
	if partial.Complete() {
		t.Error("18/19 should not be complete")
	}
	note := partial.DraftNote()
	for _, want := range []string{"DRAFT NOTE", "INTERNAL", "18 of 19", "remove before issuance"} {
		if !strings.Contains(note, want) {
			t.Errorf("draft note missing %q: %q", want, note)
		}
	}
	// It is a drafting artifact, not a regulatory finding. Wording that reads as
	// an ASTM data gap in a signed report is the failure this guards.
	for _, banned := range []string{"data gap", "Data Gap", "DATA GAP"} {
		if strings.Contains(note, banned) {
			t.Errorf("draft note reads as an ASTM data gap (%q): %q", banned, note)
		}
	}
}

// An empty corpus is not "complete" -- zero baselines is the cold-cache case,
// not a clean run.
func TestBaselineStatusEmptyIsNotComplete(t *testing.T) {
	if (BaselineStatus{}).Complete() {
		t.Error("a zero-total baseline must not report complete")
	}
}

func TestHistoricalCoverageCountsCacheEntries(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "cached.pdf", "ALPHA")
	writeHistorical(t, dir, "uncached.pdf", "BETA")
	writeHistorical(t, dir, "local.txt", "plain text needs no extraction")

	// Warm exactly one of the two PDFs.
	ex := &countingExtractor{}
	if _, err := LoadHistoricalCorpus(context.Background(), dir, ex); err != nil {
		t.Fatalf("warm: %v", err)
	}
	cacheDir := filepath.Join(dir, HistoricalCacheDir)
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("atomic write left a temp file behind: %s", e.Name())
		}
	}
	// Remove the cache entry belonging to "BETA" so it reads as uncached.
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if strings.Contains(string(b), "uncached.pdf") {
			os.Remove(filepath.Join(cacheDir, e.Name()))
		}
	}

	status, err := HistoricalCoverage(dir)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if status.Total != 3 {
		t.Errorf("Total = %d, want 3", status.Total)
	}
	if status.Used != 2 {
		t.Errorf("Used = %d, want 2 (the cached pdf plus the local .txt)", status.Used)
	}
	if len(status.Missing) != 1 || status.Missing[0] != "uncached.pdf" {
		t.Errorf("Missing = %v, want [uncached.pdf]", status.Missing)
	}
	if status.Complete() {
		t.Error("a partial cache must not report complete")
	}
}

// HistoricalCoverage is the boot preflight: it must never call the model.
func TestHistoricalCoverageNeverExtracts(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "cold.pdf", "NEVER EXTRACTED")

	status, err := HistoricalCoverage(dir)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if status.Used != 0 || len(status.Missing) != 1 {
		t.Fatalf("cold cache reported %s, want 0 used and 1 missing", status.String())
	}
}

// On the request path the extractor is nil: a cache miss is a recorded failure,
// never ~19 sequential Vertex transcriptions inside one HTTP request.
func TestLoadHistoricalCorpusNilExtractorRecordsFailure(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "cold.pdf", "NOT CACHED")

	corpus, err := LoadHistoricalCorpus(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("nil extractor must not fail the load: %v", err)
	}
	if len(corpus.Docs) != 0 || len(corpus.Failed) != 1 {
		t.Fatalf("got %d docs / %d failed, want 0/1", len(corpus.Docs), len(corpus.Failed))
	}
	if status := corpus.Status(); status.Used != 0 || status.Total != 1 {
		t.Errorf("status = %s, want 0/1", status.String())
	}
}

// The memo used to store Failed alongside Docs, so one transient failure
// dropped that baseline for the entire process lifetime.
func TestHistoricalCorpusRetriesFailuresOnNextCall(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "flaky.pdf", "CONTENT")
	ex := &failingExtractor{failures: 1}

	first, err := LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if len(first.Failed) != 1 {
		t.Fatalf("expected the first load to fail extraction, got %d failures", len(first.Failed))
	}

	// Same directory, same fingerprint. The failure must be retried.
	second, err := LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(second.Docs) != 1 || len(second.Failed) != 0 {
		t.Fatalf("failure was memoized: got %d docs / %d failed, want 1/0", len(second.Docs), len(second.Failed))
	}
	if ex.calls != 2 {
		t.Errorf("extractor called %d times, want 2 (one failure, one retry)", ex.calls)
	}
}

// Successful extractions must still be memoized, and must not be re-extracted
// while retrying a sibling's failure.
func TestHistoricalCorpusReusesSuccessesWhileRetrying(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "good.pdf", "GOOD")
	writeHistorical(t, dir, "bad.pdf", "BAD")

	// Fails the second call only: good.pdf succeeds, bad.pdf fails.
	ex := &sequencedExtractor{failOn: map[string]int{"bad.pdf": 1}}

	if _, err := LoadHistoricalCorpus(context.Background(), dir, ex); err != nil {
		t.Fatalf("first load: %v", err)
	}
	callsAfterFirst := ex.calls

	second, err := LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(second.Docs) != 2 || len(second.Failed) != 0 {
		t.Fatalf("got %d docs / %d failed, want 2/0", len(second.Docs), len(second.Failed))
	}
	if got := ex.calls - callsAfterFirst; got != 1 {
		t.Errorf("second load made %d extraction calls, want 1 — successes must not be re-extracted", got)
	}
}

// sequencedExtractor fails the first N attempts for named files.
type sequencedExtractor struct {
	failOn map[string]int
	calls  int
}

func (s *sequencedExtractor) ExtractText(ctx context.Context, name string, data []byte, mimeType string) (string, error) {
	s.calls++
	if remaining, ok := s.failOn[name]; ok && remaining > 0 {
		s.failOn[name] = remaining - 1
		return "", errors.New("synthetic failure for " + name)
	}
	return "extracted:" + name, nil
}

func TestWriteCacheAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "entry.txt")

	if err := writeCacheAtomic(target, "transcribed text"); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(b) != "transcribed text" {
		t.Errorf("content = %q", string(b))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only the final file, got %v", names)
	}

	// Overwriting an existing entry must also land atomically.
	if err := writeCacheAtomic(target, "replacement"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	b, _ = os.ReadFile(target)
	if string(b) != "replacement" {
		t.Errorf("overwrite content = %q", string(b))
	}
}
