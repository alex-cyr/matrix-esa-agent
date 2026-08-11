package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /download used to fall through to os.Stat + http.ServeFile on the raw `file`
// parameter with no auth, so anything the process could read was downloadable.
// These cases are the shapes that exploited it.

func TestSafeDownloadNameRejectsTraversal(t *testing.T) {
	hostile := []string{
		"../.env",
		"../../.env",
		`..\..\.env`,
		"../../../etc/passwd",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		"knowledge/ESA_PHASE_I_Template.docx",
		`tmp\esa_inputs\other-client\report.docx`,
		"..",
		".",
		".env",
		"",
		"   ",
		"service-account.json",
		"main.go",
		"report.docx.exe",
	}

	for _, raw := range hostile {
		t.Run(raw, func(t *testing.T) {
			if got, err := safeDownloadName(raw); err == nil {
				t.Errorf("safeDownloadName(%q) accepted, returning %q — this parameter reaches the filesystem", raw, got)
			}
		})
	}
}

func TestSafeDownloadNameAcceptsGeneratedReports(t *testing.T) {
	valid := []string{
		"Phase_I_ESA_Report_Properties_at_Providence_Road_20260805_022316.docx",
		"Matrix_Cloud_Final_Report.docx",
		"Appendix_Figures_Beavers_Road_Tract_20260811_001500.docx",
		"Appendix_A.pdf",
	}

	for _, raw := range valid {
		t.Run(raw, func(t *testing.T) {
			got, err := safeDownloadName(raw)
			if err != nil {
				t.Fatalf("safeDownloadName(%q) rejected a legitimate deliverable: %v", raw, err)
			}
			if got != raw {
				t.Errorf("safeDownloadName(%q) = %q; the name must pass through unchanged", raw, got)
			}
		})
	}
}

func TestSafeProjectNameRejectsPaths(t *testing.T) {
	hostile := []string{"../other-client", `..\other-client`, "a/b", "", "   ", "esa_outputs/x"}
	for _, raw := range hostile {
		t.Run(raw, func(t *testing.T) {
			if _, err := safeProjectName(raw); err == nil {
				t.Errorf("safeProjectName(%q) accepted; it becomes part of a GCS object path", raw)
			}
		})
	}
}

// Project names legitimately contain spaces.
func TestSafeProjectNameAcceptsRealProjects(t *testing.T) {
	for _, raw := range []string{"Properties at Providence Road", "Beavers_Road_Tract", "Beavers Road Tract"} {
		if got, err := safeProjectName(raw); err != nil || got != raw {
			t.Errorf("safeProjectName(%q) = %q, %v; want it accepted unchanged", raw, got, err)
		}
	}
}

func TestWithinDir(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		target string
		want   bool
	}{
		{filepath.Join(base, "file.docx"), true},
		{filepath.Join(base, "sub", "file.docx"), true},
		{filepath.Join(base, "..", "escaped.docx"), false},
		{filepath.Dir(base), false},
	}
	for _, tc := range cases {
		if got := withinDir(base, tc.target); got != tc.want {
			t.Errorf("withinDir(%q, %q) = %v, want %v", base, tc.target, got, tc.want)
		}
	}
}

// A traversal attempt must be refused before any storage call, so the handler
// stays hermetic here: no credentials, no bucket, still a 400.
func TestDownloadHandlerRejectsTraversalRequest(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"file traversal", "project=Providence&file=../../.env"},
		{"windows file traversal", `project=Providence&file=..\..\.env`},
		{"absolute file", "project=Providence&file=/etc/passwd"},
		{"project traversal", "project=../other&file=Report.docx"},
		{"disallowed extension", "project=Providence&file=main.go"},
		{"missing file", "project=Providence"},
		{"missing project", "file=Report.docx"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			downloadFileHandler(rec, httptest.NewRequest(http.MethodGet, "/api/v1/download?"+tc.query, nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "PRIVATE KEY") || rec.Body.Len() > 200 {
				t.Errorf("response looks like file content, not an error: %q", rec.Body.String())
			}
		})
	}
}

func TestDownloadHandlerRequiresDomainAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/download?project=Providence&file=Report.docx", nil)
	req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:outsider@gmail.com")

	rec := httptest.NewRecorder()
	downloadFileHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d — /download was the one handler with no auth", rec.Code, http.StatusForbidden)
	}
}

// The legitimate path still works: a file sitting in a generate temp dir is
// served, and only by its exact name.
func TestDownloadHandlerServesFromGenerateTempDir(t *testing.T) {
	dir, err := os.MkdirTemp("", "matrix-generate-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	name := "Phase_I_ESA_Report_Download_Test_20260811_000000.docx"
	want := []byte("PK\x03\x04 pretend docx")
	if err := os.WriteFile(filepath.Join(dir, name), want, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	downloadFileHandler(rec, httptest.NewRequest(http.MethodGet, "/api/v1/download?project=Download+Test&file="+name, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(want) {
		t.Errorf("served %q, want %q", got, string(want))
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, name) {
		t.Errorf("Content-Disposition = %q, want it to name the file", cd)
	}
}
