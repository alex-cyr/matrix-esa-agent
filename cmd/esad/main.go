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
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

func main() {
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
	// MATRIX ENGS: For boilerplate portfolio demo, defining in-memory.
	// 1337 Code: Always prioritize clean strict types.
	parserCfg := core.AgentConfig{
		Name:         "ParserAgent",
		Model:        "gemini-2.5-pro",
		SystemPrompt: "Parse raw EDR reports and extract coordinates and tabular data.",
		Temperature:  0.0,
	}

	geoCfg := core.AgentConfig{
		Name:         "GeospatialEvaluatorAgent",
		Model:        "gemini-2.5-flash",
		SystemPrompt: "Evaluate relative risks of extracted off-site regulatory findings.",
		Temperature:  0.1,
	}

	astmCfg := core.AgentConfig{
		Name:         "ASTMSynthesizerAgent",
		Model:        "gemini-2.5-pro",
		SystemPrompt: "Classify findings via ASTM E1527-21 logic as REC, HREC, or De Minimis.",
		Temperature:  0.2,
	}

	templateCfg := core.AgentConfig{
		Name:         "TemplateCompilerAgent",
		Model:        "gemini-2.5-pro",
		SystemPrompt: "Write finalized data into the ESA Phase I Blank Template doc.",
		Temperature:  0.2,
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
	activeAgents := []*core.Agent{pAgent, gAgent}
	if !*skipASTM {
		activeAgents = append(activeAgents, aAgent)
	}
	activeAgents = append(activeAgents, tAgent)

	pipeline := core.NewPipeline(*projectID, *location, *skipHITL, activeAgents...)

	// 4. Execute Flow Pipeline with human-in-the-loop validation
	// (Simulation payload mapping)
	initialDataFlow := "load(" + *payloadPath + ")"

	if err := pipeline.Run(ctx, initialDataFlow); err != nil {
		slog.Error("EXECUTION_SUSPENDED: State Yielded to Matrix Engineering", "cause", err)
		// Yield state here requires manual Antigravity IDE UI review loop (HITL)
		// NOTE: Teammates do not alter this exit constraint without modifying HW approval logic.
		os.Exit(0)
	}
}
