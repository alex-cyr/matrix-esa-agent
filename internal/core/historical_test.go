package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// countingExtractor stands in for the Vertex-backed extractor so cache
// behavior can be verified without credentials or API cost.
type countingExtractor struct {
	calls int
	text  string
}

func (c *countingExtractor) ExtractText(ctx context.Context, name string, data []byte, mimeType string) (string, error) {
	c.calls++
	if c.text != "" {
		return c.text, nil
	}
	return "extracted:" + name + ":" + string(data), nil
}

func writeHistorical(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestHistoricalCorpusCachesByContentHash(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "report_a.pdf", "ALPHA")
	ex := &countingExtractor{}

	corpus, err := LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if len(corpus.Docs) != 1 || len(corpus.Failed) != 0 {
		t.Fatalf("expected 1 doc 0 failed, got %d/%d", len(corpus.Docs), len(corpus.Failed))
	}
	if ex.calls != 1 {
		t.Fatalf("expected 1 extraction, got %d", ex.calls)
	}

	// A second file with identical content must hit the cache: the key is the
	// content hash, not the filename.
	writeHistorical(t, dir, "report_a_copy.pdf", "ALPHA")
	corpus, err = LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(corpus.Docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(corpus.Docs))
	}
	if ex.calls != 1 {
		t.Fatalf("duplicate content should reuse cache; extractions went %d -> want 1", ex.calls)
	}
}

func TestHistoricalCorpusInvalidatesOnContentChange(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "report.pdf", "ORIGINAL")
	ex := &countingExtractor{}

	corpus, err := LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if got := corpus.Docs[0].Text; got != "extracted:report.pdf:ORIGINAL" {
		t.Fatalf("unexpected text %q", got)
	}

	// Replacing a report must re-extract. This is the failure the cache design
	// exists to avoid: adding a new baseline and silently getting the old text.
	writeHistorical(t, dir, "report.pdf", "REVISED")
	corpus, err = LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if ex.calls != 2 {
		t.Fatalf("changed content must re-extract; got %d calls, want 2", ex.calls)
	}
	if got := corpus.Docs[0].Text; got != "extracted:report.pdf:REVISED" {
		t.Fatalf("stale cached text served after content change: %q", got)
	}
}

func TestHistoricalCorpusLocalFormatsSkipModel(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "notes.txt", "plain text baseline")
	writeHistorical(t, dir, "notes.md", "# markdown baseline")
	ex := &countingExtractor{}

	corpus, err := LoadHistoricalCorpus(context.Background(), dir, ex)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(corpus.Docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(corpus.Docs))
	}
	if ex.calls != 0 {
		t.Fatalf("locally decodable formats must not call the model; got %d calls", ex.calls)
	}
}

func TestHistoricalCorpusRecordsFailuresWithoutAborting(t *testing.T) {
	dir := t.TempDir()
	writeHistorical(t, dir, "good.txt", "fine")
	writeHistorical(t, dir, "bad.pdf", "unreadable")

	// No extractor configured: the PDF cannot be resolved, the txt still can.
	corpus, err := LoadHistoricalCorpus(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("load should degrade, not fail: %v", err)
	}
	if len(corpus.Docs) != 1 || corpus.Docs[0].Name != "good.txt" {
		t.Fatalf("expected the txt to survive, got %+v", corpus.Docs)
	}
	if len(corpus.Failed) != 1 || corpus.Failed[0] != "bad.pdf" {
		t.Fatalf("expected bad.pdf recorded as failed, got %v", corpus.Failed)
	}
	if corpus.PromptBlock() == "" {
		t.Fatal("surviving docs should still render a prompt block")
	}
}

func TestHistoricalCorpusMissingDirIsAnError(t *testing.T) {
	if _, err := LoadHistoricalCorpus(context.Background(), filepath.Join(t.TempDir(), "nope"), nil); err == nil {
		t.Fatal("missing historical dir must return an error, not an empty corpus")
	}
}
