package core

import (
	"context"
	"fmt"
	"log/slog"
)

// Pipeline enforces the SequentialAgent pattern.
// 1337 A2A NETWORK MATRIX: Core orchestrator for multi-agentic document processing.
type Pipeline struct {
	Agents    []*Agent
	Memory    []*Artifact
	ProjectID string
	Location  string
}

func NewPipeline(project, location string, agents ...*Agent) *Pipeline {
	return &Pipeline{
		Agents:    agents,
		Memory:    make([]*Artifact, 0),
		ProjectID: project,
		Location:  location,
	}
}

// Run executes the chain sequentially, pausing on unapproved artifacts.
func (p *Pipeline) Run(ctx context.Context, initialPayload string) error {
	slog.Info("MATRIX EXECUTABLE LOADED: Initializing SequentialAgent Pipeline")

	currentPayload := initialPayload
	for i, agent := range p.Agents {
		slog.Info("/// NODE ENGAGED ///", "name", agent.Cfg.Name, "sequence_step", i+1)

		artifact, err := agent.Execute(ctx, currentPayload)
		if err != nil {
			return fmt.Errorf("SYSTEM FAULT: Pipeline shattered at matrix node %s: %w", agent.Cfg.Name, err)
		}

		p.Memory = append(p.Memory, artifact)

		// Hard enforcement of Human-in-the-loop (Liability Mitigation pattern).
		if !artifact.Approved {
			slog.Warn("ENFORCING HITL YIELD: State paused for Validation by Matrix Engineering.",
				"artifact_id", artifact.ID,
				"agent", agent.Cfg.Name)

			// In production integration, the Antigravity IDE consumes this event and prompts the UI.
			// After manual approval (inline doc comments), the webhook calls the pipeline back.
			return fmt.Errorf("SIG_YIELD: Entity Validation Required by HW [Artifact %s]", artifact.ID)
		}

		// A2A transmission: Pass current verified artifact as input payload to the next agent downstream.
		currentPayload = artifact.Content
		slog.Info("/// ARTIFACT VERIFIED /// A2A Network Transmission to next matrix node initiating.", "bytes_transferred", len(currentPayload))
	}

	slog.Info("/// MATRIX EXECUTION COMPLETE /// Final Payload Ready for Extraction.")
	return nil
}
