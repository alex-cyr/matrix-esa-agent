package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"cloud.google.com/go/vertexai/genai"
)

// ErrHITLYield signals the pipeline stopped for Human-in-the-Loop review.
// This is a normal outcome and must stay distinguishable from a node failure.
var ErrHITLYield = errors.New("SIG_YIELD: entity validation required by Matrix Engineer")

// Pipeline enforces the SequentialAgent pattern.
type Pipeline struct {
	Agents    []*Agent
	Memory    []*Artifact
	ProjectID string
	Location  string
	SkipHITL  bool
}

func NewPipeline(project, location string, skipHITL bool, agents ...*Agent) (*Pipeline, error) {
	if len(agents) == 0 {
		return nil, errors.New("pipeline requires at least one agent")
	}
	// A nil agent means construction failed upstream. Dropping it silently
	// shortened the chain and produced a report missing a whole analysis stage.
	for i, a := range agents {
		if a == nil {
			return nil, fmt.Errorf("pipeline agent %d is nil: agent construction failed upstream", i+1)
		}
	}
	return &Pipeline{
		Agents:    agents,
		Memory:    make([]*Artifact, 0),
		ProjectID: project,
		Location:  location,
		SkipHITL:  skipHITL,
	}, nil
}

// Run executes the chain sequentially, pausing on unapproved artifacts.
func (p *Pipeline) Run(ctx context.Context, initialPayload string) (string, error) {
	slog.Info("MATRIX EXECUTABLE LOADED: Initializing SequentialAgent Pipeline", "nodes", len(p.Agents))

	currentPayload := initialPayload
	var cumulativeEstTokens int
	for i, agent := range p.Agents {
		validPayload := strings.ToValidUTF8(currentPayload, "")
		// Logged before the call: a context-limit rejection returns an error
		// with no UsageMetadata, so this is the only record of what we sent.
		// chars/4 is a crude undercount for table-dense text -- exact counts
		// come from UsageMetadata in the NODE YIELD line when a node succeeds.
		sysChars, payChars := len(agent.Cfg.SystemPrompt), len(validPayload)
		cumulativeEstTokens += (sysChars + payChars) / 4
		slog.Info("/// NODE ENGAGED ///", "name", agent.Cfg.Name, "sequence_step", i+1,
			"system_prompt_chars", sysChars, "payload_chars", payChars,
			"est_input_tokens", (sysChars+payChars)/4,
			"cumulative_est_tokens", cumulativeEstTokens)
		artifact, err := agent.Execute(ctx, genai.Text(validPayload))
		if err != nil {
			slog.Error("/// NODE FAILED /// aborting pipeline", "agent", agent.Cfg.Name, "sequence_step", i+1, "err", err)
			return "", fmt.Errorf("pipeline node %d (%s) failed: %w", i+1, agent.Cfg.Name, err)
		}

		p.Memory = append(p.Memory, artifact)

		if !artifact.Approved {
			if p.SkipHITL {
				artifact.Approved = true
			} else {
				return "", fmt.Errorf("%w [artifact %s]", ErrHITLYield, artifact.ID)
			}
		}

		currentPayload = currentPayload + "\n\n=== [MATRIX NODE: " + agent.Cfg.Name + "] ===\n" + artifact.Content
	}

	slog.Info("/// MATRIX EXECUTION COMPLETE /// Final Payload Ready for Extraction.")
	return p.Memory[len(p.Memory)-1].Content, nil
}
