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
)

// LoadHistoricalCorpus returns extracted text for every document in dir.
//
// Results are cached to dir/.cache keyed by content hash, so the expensive
// model extraction happens once per unique file rather than once per request.
// An in-process memo keyed on a directory fingerprint (names, sizes, modtimes)
// avoids re-reading the cache on every call while still noticing when the
// directory changes underneath a long-running server.
func LoadHistoricalCorpus(ctx context.Context, dir string, ex TextExtractor) (*HistoricalCorpus, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read historical dir %q: %w", dir, err)
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

	fp, err := dirFingerprint(dir, entries, names)
	if err != nil {
		return nil, err
	}

	corpusMu.Lock()
	defer corpusMu.Unlock()
	if corpusKey == fp && corpusValue != nil {
		return corpusValue, nil
	}

	cacheDir := filepath.Join(dir, HistoricalCacheDir)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create historical cache dir: %w", err)
	}

	corpus := &HistoricalCorpus{}
	for _, name := range names {
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

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	cachePath := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".txt")

	if b, err := os.ReadFile(cachePath); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return string(b), nil
	}

	if !needsModelExtraction(name) {
		return "", fmt.Errorf("unsupported historical format %q", filepath.Ext(name))
	}
	if ex == nil {
		return "", fmt.Errorf("cache miss and no extractor configured (run: esad -warm-historical)")
	}

	slog.Info("HISTORICAL CACHE MISS: extracting via model", "file", name, "bytes", len(data))
	text, err := ex.ExtractText(ctx, name, data, MimeTypeFor(name))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(cachePath, []byte(text), 0o644); err != nil {
		// Extraction succeeded; a cache write failure only costs us the work
		// next time, so continue rather than discarding a good result.
		slog.Error("HISTORICAL CACHE WRITE FAILED", "file", name, "cache", cachePath, "err", err)
	}
	return text, nil
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
