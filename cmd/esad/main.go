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
	"encoding/xml"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

	// 3. Assemble SequentialAgent Pipeline (1337 A2A Network Matrix)
	activeAgents := []*core.Agent{gAgent} // Note: pAgent logic runs separately to extract initial flow.
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

			// Resolve the absolute path of the executable/script directory to find insert_docx.go consistently
			execDir, err := os.Getwd()
			if err != nil {
				slog.Error("SYSTEM_FAULT: Could not determine working directory", "err", err)
			}
			scriptPath := filepath.Join(execDir, "scripts", "insert_docx.go")

			// We map this out via standard exec command using the internal script
			cmd := exec.Command("go", "run", scriptPath, templatePath, jsonPath, finalDocxPath)

			// Capture output
			output, err := cmd.CombinedOutput()
			if err != nil {
				slog.Error("SYSTEM_FAULT: DOCX merge failed", "err", err, "output", string(output))
			} else {
				slog.Info("/// DOCX MERGE SUCCESSFUL ///", "output", finalDocxPath)
			}
		}

	} else {
		slog.Info("/// MATRIX EXECUTION COMPLETE /// Handled by Parser Only.")
	}
}
