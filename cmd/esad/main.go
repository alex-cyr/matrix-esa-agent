/*
 * ////////////////////////////////////////////////////////
 * // MATRIX ENGINEERING | 1337 ESA AGENT FRAMEWORK //
 * ////////////////////////////////////////////////////////
 * // Architecture:    Multi-Agentic System (A2A Network)
 * // Target:          EDR PDF Packages -> ASTM Rationale
 * // Security LeveL:  CLASSIFIED [M&A Portfolio Ready]
 * ////////////////////////////////////////////////////////
 * // MATRIX TEAMMATES: To hook into this framework, DO NOT
 * // bypass the Human-in-the-Loop (HITL) yield state.
 * // Artifacts MUST emit SIG_YIELD for engineering review.
 * ////////////////////////////////////////////////////////
 */
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joho/godotenv"
	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

// extractDocxText reads a docx archive and extracts raw text from document.xml
func extractDocxText(path string) (string, error) {
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
		return "", fmt.Errorf("word/document.xml not found")
	}

	rc, err := docXML.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	var text bytes.Buffer
	for {
		t, _ := decoder.Token()
		if t == nil {
			break
		}
		switch se := t.(type) {
		case xml.CharData:
			if len(se) > 0 {
				text.Write(se)
			}
		}
	}
	return text.String(), nil
}

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

func mergeDocxLogic(templatePath, jsonPath, outputPath string) error {
	jsonData, err := os.ReadFile(jsonPath)
	if err != nil { return fmt.Errorf("read json: %v", err) }
	jsonStr := string(jsonData)
	startIdx := strings.Index(jsonStr, "{")
	endIdx := strings.LastIndex(jsonStr, "}")
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		jsonStr = jsonStr[startIdx : endIdx+1]
	}
	var replaceMap map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &replaceMap); err != nil { return fmt.Errorf("json parse: %v", err) }

	r, err := zip.OpenReader(templatePath)
	if err != nil { return fmt.Errorf("zip open: %v", err) }
	defer r.Close()

	outf, err := os.Create(outputPath)
	if err != nil { return fmt.Errorf("create output: %v", err) }
	defer outf.Close()

	w := zip.NewWriter(outf)
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil { return err }
		needProcess := f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/header") || strings.HasPrefix(f.Name, "word/footer")
		fWriter, err := w.Create(f.Name)
		if err != nil { rc.Close(); return err }
		if needProcess {
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil { return err }
			xmlStr := string(content)
			for k, v := range replaceMap {
				xmlStr = replaceFracturedXML(xmlStr, k, fmt.Sprint(v))
			}
			if _, err = fWriter.Write([]byte(xmlStr)); err != nil { return err }
		} else {
			if _, err = io.Copy(fWriter, rc); err != nil { rc.Close(); return err }
			rc.Close()
		}
	}
	return w.Close()
}

func main() {
	// Load .env file if it exists
	_ = godotenv.Load()
	var (
		payloadPath = flag.String("payload", "", "Path to raw EDR PDF suite or initialized project folder")
		projectID   = flag.String("project", os.Getenv("GCP_PROJECT"), "GCP Project ID for Vertex AI")
		location    = flag.String("location", "us-central1", "GCP Location for Vertex AI")
		skipHITL    = flag.Bool("skip-hitl", false, "1337 TOGGLE: Bypass HITL validation for fully automated runs")
		skipASTM    = flag.Bool("skip-astm", false, "1337 TOGGLE: Omit the ASTM Synthesizer Agent from the A2A network")
	)
	flag.Parse()

	if *payloadPath == "" {
		slog.Error("Payload target required. Initialize with -payload flag.")
		os.Exit(1)
	}

	ctx := context.Background()

	// 1. Initialize Matrix Agent Skill Configs (loaded from .agents/skills/...)
	loadSkill := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("Could not load skill file", "path", path)
			return ""
		}
		return string(data)
	}

	parserCfg := core.AgentConfig{
		Name:         "ParserAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/parser/SKILL.md"),
		Temperature:  0.0,
	}

	geoCfg := core.AgentConfig{
		Name:         "GeospatialEvaluatorAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/geospatial-evaluator/SKILL.md"),
		Temperature:  0.1,
	}

	astmCfg := core.AgentConfig{
		Name:         "ASTMSynthesizerAgent",
		Model:        "gemini-2.5-flash", // We use flash to avoid model/quota issues
		SystemPrompt: loadSkill(".agents/skills/astm-synthesizer/SKILL.md"),
		Temperature:  0.2,
	}

	templateCfg := core.AgentConfig{
		Name:         "TemplateCompilerAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: loadSkill(".agents/skills/template-compiler/SKILL.md"),
		Temperature:  0.2,
	}
	siteReconCfg := core.AgentConfig{
		Name:         "SiteReconSynthesizerAgent",
		Model:        "gemini-2.5-pro",
		SystemPrompt: loadSkill(".agents/skills/site-recon-synthesizer/SKILL.md"),
		Temperature:  0.2,
	}
	// 1.5 Inject Historical Reports into Template Compiler Context
	historicalDir := filepath.Join(*payloadPath, "historical")
	if hFiles, err := os.ReadDir(historicalDir); err == nil {
		for _, hFile := range hFiles {
			ext := strings.ToLower(filepath.Ext(hFile.Name()))
			if !hFile.IsDir() && (ext == ".txt" || ext == ".md" || ext == ".docx") {
				var contentStr string
				var fileErr error

				filePath := filepath.Join(historicalDir, hFile.Name())
				if ext == ".docx" {
					contentStr, fileErr = extractDocxText(filePath)
				} else {
					contentBytes, err := os.ReadFile(filePath)
					contentStr = string(contentBytes)
					fileErr = err
				}

				if fileErr == nil {
					templateCfg.SystemPrompt += "\n\n=== HISTORICAL REPORT BASELINE CONTEXT [" + hFile.Name() + "] ===\n" + contentStr
					slog.Info("/// INGESTING HISTORICAL REPORT (CONTEXT) ///", "file", hFile.Name())
				} else {
					slog.Warn("Failed to read historical report", "file", hFile.Name(), "err", fileErr)
				}
			}
		}
	}

	// 2. Instantiate Agents
	pAgent, err := core.NewAgent(ctx, *projectID, *location, parserCfg)
	if err != nil {
		slog.Error("SYSTEM_FAULT: Parser Init Failed", "err", err)
		os.Exit(1)
	}

	gAgent, err := core.NewAgent(ctx, *projectID, *location, geoCfg)
	if err != nil {
		slog.Error("SYSTEM_FAULT: Geo Init Failed", "err", err)
		os.Exit(1)
	}

	aAgent, err := core.NewAgent(ctx, *projectID, *location, astmCfg)
	if err != nil {
		slog.Error("SYSTEM_FAULT: ASTM Init Failed", "err", err)
		os.Exit(1)
	}

	tAgent, err := core.NewAgent(ctx, *projectID, *location, templateCfg)
	if err != nil {
		slog.Error("SYSTEM_FAULT: Template Init Failed", "err", err)
		os.Exit(1)
	}
	srAgent, err := core.NewAgent(ctx, *projectID, *location, siteReconCfg)
	if err != nil {
		slog.Error("SYSTEM_FAULT: Site Recon Init Failed", "err", err)
		os.Exit(1)
	}
	// 3. Assemble SequentialAgent Pipeline (1337 A2A Network Matrix)
	activeAgents := []*core.Agent{gAgent} // Note: pAgent logic runs separately to extract initial flow.
	activeAgents = append(activeAgents, srAgent)
	if !*skipASTM {
		activeAgents = append(activeAgents, aAgent)
	}
	activeAgents = append(activeAgents, tAgent)

	pipeline := core.NewPipeline(*projectID, *location, *skipHITL, activeAgents...)

	// 4. Extract Initial Payload using real pAgent execution from edr_source/
	pdfSourceDir := *payloadPath + "\\edr_source"
	initialDataFlow, err := ExtractEDRSuite(ctx, pAgent, pdfSourceDir)
	if err != nil {
		slog.Error("SYSTEM_FAULT: Parser Execution Failed", "err", err)
		os.Exit(1)
	}

	if len(pipeline.Agents) > 0 {
		finalPayload, err := pipeline.Run(ctx, initialDataFlow)
		if err != nil {
			slog.Error("EXECUTION_SUSPENDED: State Yielded to Matrix Engineering", "cause", err)
			// Yield state here requires manual Antigravity IDE UI review loop (HITL)
			// NOTE: Teammates do not alter this exit constraint without modifying HW approval logic.
			os.Exit(0)
		}

		// Save the final payload to a file inside output/
		jsonPath := *payloadPath + "\\output\\MATRIX_ESA_REPORT.json"
		err = os.WriteFile(jsonPath, []byte(finalPayload), 0644)
		if err != nil {
			slog.Error("SYSTEM_FAULT: Failed to write output report to disk", "err", err)
		} else {
			slog.Info("/// ARTIFACT WRITTEN TO DISK ///", "path", jsonPath)

			// Automatically find the new Blank Template in the knowledge/ folder
			knowledgeDir := *payloadPath + "\\knowledge"
			templatePath := knowledgeDir + "\\ESA_PHASE_I_Template.docx" // Target the new template
			finalDocxPath := *payloadPath + "\\output\\FINAL_DRAFT_REPORT.docx"

			slog.Info("/// INITIATING DOCX MERGE ///", "template", templatePath)

			// Execute natively to bypass any child-process Windows Security policies
			err = mergeDocxLogic(templatePath, jsonPath, finalDocxPath)
			if err != nil {
				slog.Error("SYSTEM_FAULT: DOCX merge failed", "err", err)
			} else {
				slog.Info("/// DOCX MERGE SUCCESSFUL ///", "output", finalDocxPath)
			}
		}

	} else {
		slog.Info("/// MATRIX EXECUTION COMPLETE /// Handled by Parser Only.")
	}
}
