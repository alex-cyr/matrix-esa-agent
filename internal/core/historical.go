package core

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"cloud.google.com/go/vertexai/genai"
)

// HistoricalCacheDir is the subdirectory holding extracted text, keyed by the
// SHA-256 of the source file's contents. Hashing contents rather than names
// means editing or replacing a report invalidates its entry, while merely
// renaming one reuses the existing extraction.
const HistoricalCacheDir = ".cache"

// HistoricalDoc is one extracted style-baseline report.
type HistoricalDoc struct {
	Name string
	Text string
}

// HistoricalCorpus is the extracted text of the historical baseline reports.
//
// It is deliberately a value rather than a pre-rendered prompt string: more
// than one agent needs this context, and each appends its own copy.
type HistoricalCorpus struct {
	Docs []HistoricalDoc
	// Failed names documents that could not be extracted. Extraction failure
	// is not fatal -- the report still generates, just without that baseline --
	// but it must never pass unnoticed.
	Failed []string
}

// PromptBlock renders the corpus for appending to an agent system prompt.
func (c *HistoricalCorpus) PromptBlock() string {
	if c == nil || len(c.Docs) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, d := range c.Docs {
		sb.WriteString(fmt.Sprintf("\n\n=== HISTORICAL REPORT BASELINE CONTEXT [%s] ===\n%s", d.Name, d.Text))
	}
	return sb.String()
}

// TextExtractor turns an opaque binary document into plain text.
type TextExtractor interface {
	ExtractText(ctx context.Context, name string, data []byte, mimeType string) (string, error)
}

// AgentExtractor adapts an Agent to TextExtractor. Pair it with the
// historical-extractor skill, not the parser skill: the parser is instructed to
// yield structured JSON, which is useless as a prose style baseline.
type AgentExtractor struct{ Agent *Agent }

func (e AgentExtractor) ExtractText(ctx context.Context, name string, data []byte, mimeType string) (string, error) {
	if e.Agent == nil {
		return "", fmt.Errorf("historical extractor: nil agent")
	}
	art, err := e.Agent.Execute(ctx,
		genai.Text("Transcribe this completed Phase I ESA report to plain text: "+name),
		genai.Blob{MIMEType: mimeType, Data: data})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(art.Content) == "" {
		return "", fmt.Errorf("extractor yielded empty text")
	}
	return art.Content, nil
}

// MimeTypeFor maps a filename to the MIME type Vertex expects, or "" if the
// extension is not something we hand to the model.
func MimeTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return ""
	}
}

// ExtractDocxText reads a .docx archive and returns the raw text of
// word/document.xml.
func ExtractDocxText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	var docXML *zip.File
	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			docXML = f
			break
		}
	}
	if docXML == nil {
		return "", fmt.Errorf("word/document.xml not found in %s", path)
	}

	rc, err := docXML.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	var text bytes.Buffer
	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", path, err)
		}
		if cd, ok := t.(xml.CharData); ok && len(cd) > 0 {
			text.Write(cd)
		}
	}
	return text.String(), nil
}

// isHistoricalSource reports whether a filename is a document we can extract.
func isHistoricalSource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".docx", ".txt", ".md":
		return true
	}
	return false
}

// needsModelExtraction reports whether a format is opaque enough to require the
// LLM. Local formats are decoded in-process and never cost an API call.
func needsModelExtraction(name string) bool {
	return MimeTypeFor(name) != ""
}

var (
	corpusMu    sync.Mutex
	corpusKey   string
	corpusValue *HistoricalCorpus

	// hashMemo caches content hashes keyed by name+size+modtime. The corpus is
	// ~300 MB of PDFs; without this, every retry of a failed document would
	// re-read and re-hash the whole file just to look up its cache entry.
	hashMemo = map[string]string{}
)

// LoadHistoricalCorpus returns extracted text for every document in dir.
//
// Results are cached to dir/.cache keyed by content hash, so the expensive
// model extraction happens once per unique file rather than once per request.
// An in-process memo keyed on a directory fingerprint (names, sizes, modtimes)
// avoids re-reading the cache on every call while still noticing when the
// directory changes underneath a long-running server.
func LoadHistoricalCorpus(ctx context.Context, dir string, ex TextExtractor) (*HistoricalCorpus, error) {
	entries, names, err := historicalSources(dir)
	if err != nil {
		return nil, err
	}

	fp, err := dirFingerprint(dir, entries, names)
	if err != nil {
		return nil, err
	}

	corpusMu.Lock()
	defer corpusMu.Unlock()

	// Successes are memoized; failures are not. A document that failed on a
	// transient quota blip used to be dropped for the whole process lifetime,
	// because the memo stored Failed alongside Docs. Now a fingerprint hit
	// reuses the extracted text and retries only what failed -- which keeps the
	// retry from re-reading 300 MB of PDFs on every request.
	memoized := map[string]HistoricalDoc{}
	if corpusKey == fp && corpusValue != nil {
		if len(corpusValue.Failed) == 0 {
			return corpusValue, nil
		}
		for _, d := range corpusValue.Docs {
			memoized[d.Name] = d
		}
		slog.Info("HISTORICAL CORPUS: retrying previously failed documents",
			"reusing", len(memoized), "retrying", len(corpusValue.Failed))
	}

	cacheDir := filepath.Join(dir, HistoricalCacheDir)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create historical cache dir: %w", err)
	}

	corpus := &HistoricalCorpus{}
	for _, name := range names {
		if d, ok := memoized[name]; ok {
			corpus.Docs = append(corpus.Docs, d)
			continue
		}
		text, err := loadOne(ctx, dir, cacheDir, name, ex)
		if err != nil {
			slog.Error("HISTORICAL EXTRACTION FAILED", "file", name, "err", err)
			corpus.Failed = append(corpus.Failed, name)
			continue
		}
		corpus.Docs = append(corpus.Docs, HistoricalDoc{Name: name, Text: text})
	}

	slog.Info("/// HISTORICAL CORPUS READY ///", "dir", dir, "extracted", len(corpus.Docs), "failed", len(corpus.Failed))
	corpusKey, corpusValue = fp, corpus
	return corpus, nil
}

// historicalSources lists the extractable documents in dir, sorted.
func historicalSources(dir string) ([]os.DirEntry, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read historical dir %q: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if isHistoricalSource(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return entries, names, nil
}

// HistoricalCoverage reports which baselines are already extracted, without
// extracting anything or calling the model. The boot preflight uses it to
// refuse to start on a cold or partial cache: extraction on the request path
// would mean ~19 sequential Vertex transcriptions inside one HTTP request,
// which blows the Cloud Run timeout and presents as a generic timeout.
func HistoricalCoverage(dir string) (BaselineStatus, error) {
	_, names, err := historicalSources(dir)
	if err != nil {
		return BaselineStatus{}, err
	}

	cacheDir := filepath.Join(dir, HistoricalCacheDir)
	status := BaselineStatus{Total: len(names)}

	corpusMu.Lock()
	defer corpusMu.Unlock()

	for _, name := range names {
		// Locally decodable formats never need the cache.
		if !needsModelExtraction(name) {
			status.Used++
			continue
		}
		hash, err := contentHashLocked(filepath.Join(dir, name))
		if err != nil {
			status.Missing = append(status.Missing, name)
			continue
		}
		if b, err := os.ReadFile(filepath.Join(cacheDir, hash+".txt")); err == nil && len(bytes.TrimSpace(b)) > 0 {
			status.Used++
			continue
		}
		status.Missing = append(status.Missing, name)
	}
	return status, nil
}

func loadOne(ctx context.Context, dir, cacheDir, name string, ex TextExtractor) (string, error) {
	path := filepath.Join(dir, name)

	// Locally decodable formats are cheap; skip the cache entirely.
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case ".docx":
		return ExtractDocxText(path)
	}

	// Hash first and read the bytes only on a cache miss: the corpus is ~300 MB
	// and a warm cache should cost a stat and a small read, not 300 MB of I/O.
	hash, err := contentHashLocked(path)
	if err != nil {
		return "", err
	}
	cachePath := filepath.Join(cacheDir, hash+".txt")

	if b, err := os.ReadFile(cachePath); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return string(b), nil
	}

	if !needsModelExtraction(name) {
		return "", fmt.Errorf("unsupported historical format %q", filepath.Ext(name))
	}
	if ex == nil {
		return "", fmt.Errorf("cache miss and no extractor configured (run: esad -warm-historical)")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	slog.Info("HISTORICAL CACHE MISS: extracting via model", "file", name, "bytes", len(data))
	text, err := ex.ExtractText(ctx, name, data, MimeTypeFor(name))
	if err != nil {
		return "", err
	}
	if err := writeCacheAtomic(cachePath, text); err != nil {
		// Extraction succeeded; a cache write failure only costs us the work
		// next time, so continue rather than discarding a good result.
		slog.Error("HISTORICAL CACHE WRITE FAILED", "file", name, "cache", cachePath, "err", err)
	}
	return text, nil
}

// writeCacheAtomic writes via a temp file in the same directory and renames it
// into place. Writes used to go straight to the final path while the read side
// only checked for non-empty content, so an interrupted write left a truncated
// transcript that would be trusted forever.
func writeCacheAtomic(cachePath, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), ".tmp-cache-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	// Flush to disk before the rename, so a crash cannot leave the final path
	// pointing at an empty file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, cachePath)
}

// contentHashLocked returns the SHA-256 of a file's contents, memoized on
// name+size+modtime. Callers must hold corpusMu.
func contentHashLocked(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
	if h, ok := hashMemo[key]; ok {
		return h, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	hashMemo[key] = sum
	return sum, nil
}

// dirFingerprint builds a cheap change-detector over the source documents.
func dirFingerprint(dir string, entries []os.DirEntry, names []string) (string, error) {
	byName := make(map[string]os.DirEntry, len(entries))
	for _, e := range entries {
		byName[e.Name()] = e
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", dir)
	for _, n := range names {
		info, err := byName[n].Info()
		if err != nil {
			return "", fmt.Errorf("stat historical source %q: %w", n, err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", n, info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
