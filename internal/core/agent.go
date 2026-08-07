package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/vertexai/genai"
)

// Artifact represents an intermediate deterministic state to be reviewed by the HW (Human Worker).
// MATRIX ENGS: Do not alter this struct. The UI parser strictly maps these fields.
type Artifact struct {
	ID        string `json:"id"`
	Type      string `json:"type"`    // e.g., "extracted_data", "astm_rationale"
	Content   string `json:"content"` // Structured JSON payload
	Approved  bool   `json:"approved"`
	AgentName string `json:"agent_name"`
}

// AgentConfig represents the skill loaded from `.agents/skills/`.
type AgentConfig struct {
	Name         string  `yaml:"name"`
	Description  string  `yaml:"description"`
	Model        string  `yaml:"model"`
	SystemPrompt string  `yaml:"system_prompt"`
	Temperature  float32 `yaml:"temperature"`
}

// Agent is the standalone processing unit for a given skill.
// 1337 CODE: Standalone nodes in the A2A Matrix. All Nodes must be stateless.
type Agent struct {
	Cfg    AgentConfig
	Client *genai.Client
}

func NewAgent(ctx context.Context, projectID, location string, cfg AgentConfig) (*Agent, error) {
	// 1337 UPDATE: Vertex AI Enterprise Ready. Natively uses Application Default Credentials.
	client, err := genai.NewClient(ctx, projectID, location)
	if err != nil {
		return nil, fmt.Errorf("failed creating Vertex client for %s: %w", cfg.Name, err)
	}
	return &Agent{
		Cfg:    cfg,
		Client: client,
	}, nil
}

// Execute performs A2A logic, returning an Artifact that the orchestrator will buffer for Human-in-the-Loop verification.
// CLASSIFIED ROUTINE: Initiates LLM inference via Vertex AI.
func (a *Agent) Execute(ctx context.Context, parts ...genai.Part) (*Artifact, error) {
	model := a.Client.GenerativeModel(a.Cfg.Model)
	model.Temperature = &a.Cfg.Temperature

	// A2A state conditioning via system instructions
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(a.Cfg.SystemPrompt)},
	}

	var resp *genai.GenerateContentResponse
	var err error
	// TEMPORARY: lowered from 10 while testing the loud-failure changes. A
	// non-retryable error (bad request, auth) burns every attempt before
	// surfacing, so keep this low until the retry loop classifies errors.
	maxRetries := 2

	for i := 0; i <= maxRetries; i++ {
		resp, err = model.GenerateContent(ctx, parts...)
		if err == nil {
			break
		}

		if i < maxRetries {
			slog.Warn("/// NODE CALL FAILED /// retrying", "agent", a.Cfg.Name,
				"attempt", i+1, "of", maxRetries+1, "err", err)
			time.Sleep(5 * time.Second)
			continue
		}
		break
	}

	if err != nil {
		slog.Error("/// NODE CALL EXHAUSTED RETRIES ///", "agent", a.Cfg.Name, "err", err)
		return nil, fmt.Errorf("generation failed for %s: %w", a.Cfg.Name, err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty yield from model for %s", a.Cfg.Name)
	}

	output := fmt.Sprintf("%v", resp.Candidates[0].Content.Parts[0])

	return &Artifact{
		// TODO: a.Cfg.Name[:4] panics on any agent name shorter than 4 chars.
		// Slice defensively or derive the ID from a sanitized full name.
		ID:        "art-" + a.Cfg.Name[:4] + "-v1",
		Type:      "agent_yield",
		Content:   output,
		Approved:  false, // HITL HARD CONSTRAINT: Liability mitigation requires explicit Matrix Engineer approval.
		AgentName: a.Cfg.Name,
	}, nil
}
