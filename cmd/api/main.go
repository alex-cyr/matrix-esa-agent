package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cloud.google.com/go/vertexai/genai"
	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)



func replaceFracturedXML(xmlStr, key, val string) string {
	var pattern strings.Builder
	for i, ch := range key {
		if i > 0 {
			pattern.WriteString("(?:<[^>]+>)*")
		}
		pattern.WriteString(regexp.QuoteMeta(string(ch)))
	}
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return xmlStr
	}
	return re.ReplaceAllString(xmlStr, val)
}

func mergeDocxLogic(templatePath string, jsonBytes []byte, outputPath string) error {
	var replaceMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &replaceMap); err != nil {
		return fmt.Errorf("json parse error: %w", err)
	}

	r, err := zip.OpenReader(templatePath)
	if err != nil {
		return fmt.Errorf("zip open error: %w", err)
	}
	defer r.Close()

	outf, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output error: %w", err)
	}
	defer outf.Close()

	w := zip.NewWriter(outf)
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		needProcess := f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/header") || strings.HasPrefix(f.Name, "word/footer")
		fWriter, err := w.Create(f.Name)
		if err != nil {
			rc.Close()
			return err
		}
		if needProcess {
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			xmlStr := string(content)
			for k, v := range replaceMap {
				xmlStr = replaceFracturedXML(xmlStr, k, fmt.Sprint(v))
			}
			if _, err = fWriter.Write([]byte(xmlStr)); err != nil {
				return err
			}
		} else {
			if _, err = io.Copy(fWriter, rc); err != nil {
				rc.Close()
				return err
			}
			rc.Close()
		}
	}
	return w.Close()
}

func loadSkill(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("Could not load skill file", "path", path)
		return ""
	}
	return string(data)
}

func analyzeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Setup Request and Workspace
	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "matrix-esa-production" // Fallback project ID
	}
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	err := r.ParseMultipartForm(50 << 20) // 50MB
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	tempDir, err := os.MkdirTemp("", "matrix-payload-*")
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tempDir) // Cleanup after request

	files := r.MultipartForm.File["files"]
	for _, fileHeader := range files {
		src, err := fileHeader.Open()
		if err != nil {
			continue
		}
		dst, err := os.Create(filepath.Join(tempDir, fileHeader.Filename))
		if err == nil {
			io.Copy(dst, src)
			dst.Close()
		}
		src.Close()
	}

	// 2. Initialize Agents
	// Note: In Cloud Run, workspace paths will map to the Docker container's working directory.
	parserAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{
		Name:         "ParserAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/parser/SKILL.md"),
		Temperature:  0.0,
	})
	geoAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{
		Name:         "GeospatialEvaluatorAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/geospatial-evaluator/SKILL.md"),
		Temperature:  0.1,
	})
	srAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{
		Name:         "SiteReconSynthesizerAgent",
		Model:        "gemini-2.5-pro",
		SystemPrompt: loadSkill(".agents/skills/site-recon-synthesizer/SKILL.md"),
		Temperature:  0.2,
	})
	astmAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{
		Name:         "ASTMSynthesizerAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/astm-synthesizer/SKILL.md"),
		Temperature:  0.2,
	})
	templateCfg := core.AgentConfig{
		Name:         "TemplateCompilerAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/template-compiler/SKILL.md"),
		Temperature:  0.2,
	}

	// Inject Historical Data
	if hFiles, err := os.ReadDir("historical"); err == nil {
		for _, hF := range hFiles {
			if !hF.IsDir() {
				c, _ := os.ReadFile(filepath.Join("historical", hF.Name()))
				templateCfg.SystemPrompt += fmt.Sprintf("\n\n=== HISTORICAL REPORT BASELINE CONTEXT [%s] ===\n%s", hF.Name(), string(c))
			}
		}
	}
	templateAgent, _ := core.NewAgent(ctx, projectID, location, templateCfg)
	pipeline := core.NewPipeline(projectID, location, true, geoAgent, srAgent, astmAgent, templateAgent)

	// 3. Extract EDR PDFs via Parser Agent
	slog.Info("Running Cloud Run Pipeline", "files", len(files))
	var fullExtractedData string
	extractionPrompt := "You are the Parser Agent... Retrieve JSON."
	for _, fileHeader := range files {
		if filepath.Ext(fileHeader.Filename) == ".pdf" {
			pdfBytes, _ := os.ReadFile(filepath.Join(tempDir, fileHeader.Filename))
			parts := []genai.Part{genai.Text(extractionPrompt), genai.Blob{MIMEType: "application/pdf", Data: pdfBytes}}
			res, err := parserAgent.Execute(ctx, parts...)
			if err == nil {
				fullExtractedData += "\n\n=== [EXTRACT: " + fileHeader.Filename + "] ===\n" + res.Content
			}
		}
	}

	// 4. Run Core Pipeline
	finalPayload, err := pipeline.Run(ctx, fullExtractedData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Sanitize output for JSON bounds
	startIdx := strings.Index(finalPayload, "{")
	endIdx := strings.LastIndex(finalPayload, "}")
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		finalPayload = finalPayload[startIdx : endIdx+1]
	}

	// 5. Build Final DOCX
	templatePath := "knowledge/ESA_PHASE_I_Template.docx"
	outDocx := filepath.Join(tempDir, "CLOUD_FINAL_REPORT.docx")
	err = mergeDocxLogic(templatePath, []byte(finalPayload), outDocx)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(finalPayload)) // Fallback to JSON if template merge fails
		return
	}

	// 6. Return the finished document
	w.Header().Set("Content-Disposition", "attachment; filename=FINAL_DRAFT_REPORT.docx")
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	http.ServeFile(w, r, outDocx)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/api/v1/analyze", analyzeHandler)

	slog.Info("Cloud Run Web Server Started", "port", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("Failed to start API Server", "err", err)
	}
}
