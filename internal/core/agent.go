package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	// MaxOutputTokens is set explicitly. The SDK declares a
	// defaultMaxOutputTokens = 2048 constant but never references it, so the
	// real cap is a server-side default that cannot be read from here.
	MaxOutputTokens int32 `yaml:"max_output_tokens"`
	// ResponseMIMEType forces structured output. Set "application/json" only
	// on agents whose entire yield is a single JSON object.
	ResponseMIMEType string `yaml:"response_mime_type"`
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
	if a.Cfg.MaxOutputTokens > 0 {
		model.SetMaxOutputTokens(a.Cfg.MaxOutputTokens)
	}
	if a.Cfg.ResponseMIMEType != "" {
		model.ResponseMIMEType = a.Cfg.ResponseMIMEType
	}

	// A2A state conditioning via system instructions
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(a.Cfg.SystemPrompt)},
	}

	var resp *genai.GenerateContentResponse
	var err error

	// Retries are classified (see retry.go): permanent errors surface on the
	// first attempt, quota errors get 30/60/90s, transport blips 5/10/20s.
	for attempt := 1; ; attempt++ {
		resp, err = model.GenerateContent(ctx, parts...)
		if err == nil {
			break
		}

		class, reason := classifyRetry(err)

		if class == retryNever {
			slog.Error("/// NODE CALL FAILED — NOT RETRYABLE ///",
				"agent", a.Cfg.Name, "attempt", attempt, "reason", reason, "err", err)
			return nil, fmt.Errorf("generation failed for %s (%s, not retryable): %w",
				a.Cfg.Name, reason, err)
		}

		if attempt > maxRetries {
			slog.Error("/// NODE CALL EXHAUSTED RETRIES ///", "agent", a.Cfg.Name,
				"attempts", attempt, "class", class.String(), "reason", reason, "err", err)
			return nil, fmt.Errorf("generation failed for %s after %d attempts (%s, %s): %w",
				a.Cfg.Name, attempt, class, reason, err)
		}

		delay := retryDelay(class, attempt)
		slog.Warn("/// NODE CALL FAILED /// retrying", "agent", a.Cfg.Name,
			"attempt", attempt, "of", maxRetries+1, "class", class.String(),
			"reason", reason, "backoff", delay.String(), "err", err)

		// A 90s sleep must not outlive a canceled request.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("generation for %s abandoned during %s backoff: %w",
				a.Cfg.Name, delay, ctx.Err())
		case <-time.After(delay):
		}
	}

	if len(resp.Candidates) == 0 {
		return nil, fmt.Errorf("no candidates returned for %s", a.Cfg.Name)
	}
	cand := resp.Candidates[0]

	var promptTok, outTok, totalTok int32
	if resp.UsageMetadata != nil {
		promptTok = resp.UsageMetadata.PromptTokenCount
		outTok = resp.UsageMetadata.CandidatesTokenCount
		totalTok = resp.UsageMetadata.TotalTokenCount
	}

	// A truncated yield is the failure this exists to catch: a ~280-key JSON
	// cut short parses as fewer keys, and every absent key becomes a blank
	// field in a document nobody flagged as incomplete.
	switch cand.FinishReason {
	case genai.FinishReasonStop:
		// Normal completion.
	case genai.FinishReasonUnspecified:
		// Tolerated so an SDK/backend quirk cannot block a run, but recorded
		// so a pattern of it is visible.
		slog.Warn("/// NODE FINISH REASON UNSPECIFIED /// treating as success",
			"agent", a.Cfg.Name, "output_tokens", outTok)
	case genai.FinishReasonMaxTokens:
		return nil, fmt.Errorf("%s: output truncated at the token cap (cap=%d, produced=%d output tokens, prompt=%d, total=%d)",
			a.Cfg.Name, a.Cfg.MaxOutputTokens, outTok, promptTok, totalTok)
	default:
		return nil, fmt.Errorf("%s: generation did not complete: finish_reason=%v (prompt=%d, output=%d)",
			a.Cfg.Name, cand.FinishReason, promptTok, outTok)
	}

	slog.Info("/// NODE YIELD ///", "agent", a.Cfg.Name,
		"prompt_tokens", promptTok, "output_tokens", outTok, "total_tokens", totalTok)

	if cand.Content == nil || len(cand.Content.Parts) == 0 {
		return nil, fmt.Errorf("empty yield from model for %s", a.Cfg.Name)
	}

	// Join every part. Reading only Parts[0] silently discarded the remainder
	// of any multi-part response.
	var sb strings.Builder
	for _, p := range cand.Content.Parts {
		if t, ok := p.(genai.Text); ok {
			sb.WriteString(string(t))
			continue
		}
		sb.WriteString(fmt.Sprintf("%v", p))
	}
	output := sb.String()
	if strings.TrimSpace(output) == "" {
		return nil, fmt.Errorf("%s: model returned only empty parts", a.Cfg.Name)
	}

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
