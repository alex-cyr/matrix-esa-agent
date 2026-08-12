package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"cloud.google.com/go/vertexai/genai"
	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

// Two-tier validation of the Template Compiler's JSON, between the pipeline and
// the merge.
//
// Invariant: this never fails a request. Every outcome is a filled value, a
// logged deliberate blank, or a loud bracket in the document. A validator that
// can block a report is a validator that gets disabled on a deadline.

// goSuppliedKeys are filled by injectFieldDefaults after the model runs, so the
// validator must never re-prompt or DATAGAP them.
//
// ONE definition, consumed by both sides on purpose. Two lists that must agree
// eventually will not, and the failure modes are exactly the bugs this phase
// exists to kill: a spurious re-prompt, or a silent blank. TestGoSuppliedKeys...
// in validate_test.go asserts they cannot drift.
// Every member must be a real template tag; TestGoSuppliedKeysExistInTemplate
// enforces that, and caught four dead spellings the first time it ran.
var goSuppliedKeys = map[string]bool{
	"ReportDate": true, // core.ReportDateNow, always overwritten
	"DraftNote":  true, // baseline coverage note, "" when complete
	"ProjectNo":  true, // MEG- prefix normalization
	"ParcelID":   true, // EP pre-screen answer
	"SiteAcres":  true, // EP pre-screen answer
	// User_Authorization joins this set when Phase 6 composes it in Go.
}

// substantiveMandatory are keys where an empty string is NOT an acceptable
// answer. They get a [MEG DATAGAP] bracket even when the model deliberately
// returns "".
//
// Section 9.0 is the core of the report and the compiler skipped it entirely on
// a delivered run. The SV_* strings are the site visit. DataGaps_Text is an
// ASTM requirement.
var substantiveMandatory = map[string]bool{
	"Sec9_Item1_SiteInfo":   true,
	"Sec9_Item2_Topo":       true,
	"Sec9_Item3_Wetlands":   true,
	"Sec9_Item4_Flood":      true,
	"Sec9_Item5_VEC":        true,
	"Sec9_Item6_OnsiteReg":  true,
	"Sec9_Item7_OffsiteReg": true,
	"Sec9_Item8_DataGaps":   true,
	"SV_AccessFrom":         true,
	"SV_AccessVia":          true,
	"SV_CurrentUse":         true,
	"SV_ConditionSummary":   true,
	"SV_ObservedFeatures":   true,
	"DataGaps_Text":         true,
}

// RepromptFunc asks the compiler for a specific set of absent keys. Injected so
// the validator is testable without credentials.
type RepromptFunc func(ctx context.Context, missing []string) (map[string]string, error)

// ValidationResult is the accounting for one generate. Every substantive tag
// ends up in exactly one bucket.
type ValidationResult struct {
	SlotsAutoFilled  int      `json:"slots_auto_filled"`
	MissingFirstPass []string `json:"missing_first_pass"`
	RepromptRan      bool     `json:"reprompt_ran"`
	RepromptErr      string   `json:"reprompt_error,omitempty"`
	Recovered        []string `json:"recovered"`
	DataGapped       []string `json:"data_gapped"`
	DeliberateBlanks []string `json:"deliberate_blanks"`
	UnknownKeys      []string `json:"unknown_keys"`
}

// DeliberateBlankCount travels in the /generate response: a deliberate blank is
// still a blank, so it has to be chosen AND seen.
func (v ValidationResult) DeliberateBlankCount() int { return len(v.DeliberateBlanks) }

// normalizeTagKey strips braces the model sometimes wraps around its own keys.
// Shared with injectFieldDefaults so both agree on what a key is called.
func normalizeTagKey(k string) string {
	k = strings.TrimSpace(k)
	k = strings.TrimPrefix(strings.TrimSuffix(k, "}}"), "{{")
	k = strings.TrimPrefix(strings.TrimSuffix(k, "}"), "{")
	return strings.TrimSpace(k)
}

// validateAndRepair fills slot gaps, re-prompts once for absent substantive
// keys, and brackets whatever is still missing.
func validateAndRepair(ctx context.Context, payloadJSON string, inv *TagInventory, reprompt RepromptFunc) (string, ValidationResult) {
	var res ValidationResult

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(payloadJSON), &raw); err != nil || raw == nil {
		// A payload that will not parse is the merge's problem, not ours; pass
		// it through untouched rather than destroying whatever it holds.
		slog.Error("VALIDATOR: compiler payload did not parse as JSON; skipping validation", "err", err)
		return payloadJSON, res
	}

	// Normalize keys before comparing: a key the model emitted as
	// "{{SiteAcres}}" is not a missing SiteAcres.
	values := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		values[normalizeTagKey(k)] = v
	}

	// Keys the model invented. Harmless at merge (they match nothing) but they
	// signal prompt drift, so they are recorded.
	for k := range values {
		if !inv.Has(k) && !goSuppliedKeys[k] {
			res.UnknownKeys = append(res.UnknownKeys, k)
		}
	}
	sort.Strings(res.UnknownKeys)

	// Tier A -- table slots. Absent slots become "", silently. They are fixed
	// table cells, never re-prompted and never bracketed.
	for _, tag := range inv.Slots {
		if _, ok := values[tag]; !ok {
			values[tag] = ""
			res.SlotsAutoFilled++
		}
	}

	// Tier B -- substantive keys.
	var missing []string
	for _, tag := range inv.Substantive {
		if goSuppliedKeys[tag] {
			continue
		}
		v, present := values[tag]
		if !present {
			missing = append(missing, tag)
			continue
		}
		if isBlank(v) {
			if substantiveMandatory[tag] {
				missing = append(missing, tag)
				continue
			}
			res.DeliberateBlanks = append(res.DeliberateBlanks, tag)
		}
	}
	sort.Strings(missing)
	sort.Strings(res.DeliberateBlanks)
	res.MissingFirstPass = missing

	// Exactly one re-prompt. No loop: a second attempt on the same context is
	// unlikely to differ and doubles the chance of a truncated response.
	if len(missing) > 0 && reprompt != nil {
		res.RepromptRan = true
		slog.Warn("VALIDATOR: re-prompting compiler for absent keys", "count", len(missing), "keys", missing)

		recovered, err := reprompt(ctx, missing)
		if err != nil {
			// Not fatal. Everything still absent gets bracketed below.
			res.RepromptErr = err.Error()
			slog.Error("VALIDATOR: re-prompt failed; absent keys will be bracketed", "err", err)
		}
		for _, tag := range missing {
			v, ok := recovered[tag]
			if !ok || strings.TrimSpace(v) == "" {
				continue // "" here is the model correctly declining to invent
			}
			values[tag] = v
			res.Recovered = append(res.Recovered, tag)
		}
		sort.Strings(res.Recovered)
	}

	// Anything still absent or mandatory-blank gets a bracket the EP can find.
	for _, tag := range missing {
		v, present := values[tag]
		if present && !isBlank(v) {
			continue
		}
		values[tag] = fmt.Sprintf("[MEG DATAGAP: %s]", tag)
		res.DataGapped = append(res.DataGapped, tag)
	}
	sort.Strings(res.DataGapped)

	logValidation(res)

	out, err := json.Marshal(values)
	if err != nil {
		slog.Error("VALIDATOR: could not re-marshal payload; passing the original through", "err", err)
		return payloadJSON, res
	}
	return string(out), res
}

// isBlank reports whether a value renders as nothing in the document.
func isBlank(v interface{}) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

func logValidation(res ValidationResult) {
	slog.Info("/// VALIDATOR SUMMARY ///",
		"slots_auto_filled", res.SlotsAutoFilled,
		"missing_first_pass", len(res.MissingFirstPass),
		"reprompt_ran", res.RepromptRan,
		"recovered", len(res.Recovered),
		"data_gapped", len(res.DataGapped),
		"deliberate_blanks", len(res.DeliberateBlanks),
		"unknown_keys", len(res.UnknownKeys))

	// A deliberate blank is still a blank. It must be visible, or "intentional"
	// is indistinguishable from "forgotten" -- which is the ambiguity that
	// produced the bug this phase exists to fix.
	if len(res.DeliberateBlanks) > 0 {
		slog.Info("VALIDATOR: substantive keys left intentionally blank",
			"count", len(res.DeliberateBlanks), "keys", res.DeliberateBlanks)
	}
	if len(res.DataGapped) > 0 {
		slog.Error("VALIDATOR: keys bracketed as data gaps in the document",
			"count", len(res.DataGapped), "keys", res.DataGapped)
	}
	if len(res.UnknownKeys) > 0 {
		slog.Warn("VALIDATOR: compiler emitted keys the template does not contain",
			"count", len(res.UnknownKeys), "keys", res.UnknownKeys)
	}
}

// --- re-prompt construction --------------------------------------------------

// repromptInstruction is prepended to the absent-key list.
//
// The anti-fabrication clause is mandatory. A prompt that demands specific keys
// creates pressure to invent values to comply, and trading a visible blank for
// an invisible fabrication is strictly worse in a signed report. The validator
// converts a returned "" into [MEG DATAGAP] for mandatory keys and accepts it
// for the rest, so an honest empty answer is always safe to give.
const repromptInstruction = `The previous response omitted the keys listed below.

Return ONLY a single JSON object containing exactly these keys and nothing else.

Values must be drawn only from the synthesis provided above. If the synthesis contains no basis for a key, return "" for that key.
An empty value is a correct answer; an invented one is not.
Do not restate template lead-in text: every value continues a sentence the template has already begun.

Absent keys:
`

// buildRepromptPayload assembles the re-prompt: the upstream synthesis plus the
// compiler's own first output, and the absent keys.
//
// Deliberately NOT the full accumulated pipeline payload. That is very large,
// the missing values are derivable from the synthesis, and resending everything
// risks a second MaxTokens truncation -- which is a likely cause of the missing
// keys in the first place.
func buildRepromptPayload(synthesis, firstOutput string, missing []string) string {
	var sb strings.Builder
	if synthesis != "" {
		sb.WriteString("=== ASTM SYNTHESIS ===\n")
		sb.WriteString(synthesis)
		sb.WriteString("\n\n")
	}
	if firstOutput != "" {
		sb.WriteString("=== YOUR PREVIOUS OUTPUT ===\n")
		sb.WriteString(firstOutput)
		sb.WriteString("\n\n")
	}
	sb.WriteString(repromptInstruction)
	for _, k := range missing {
		sb.WriteString("- ")
		sb.WriteString(k)
		sb.WriteString("\n")
	}
	return sb.String()
}

// makeReprompt builds the one-shot repair call from the pipeline that just ran.
//
// The Template Compiler is the last agent in the chain, and Pipeline.Memory
// holds every node's artifact, so the ASTM synthesis is reachable without
// changing the pipeline.
func makeReprompt(p *core.Pipeline, firstOutput string) RepromptFunc {
	if p == nil || len(p.Agents) == 0 {
		return nil
	}
	compiler := p.Agents[len(p.Agents)-1]
	synthesis := artifactByAgent(p.Memory, "ASTMSynthesizerAgent")

	return func(ctx context.Context, missing []string) (map[string]string, error) {
		payload := buildRepromptPayload(synthesis, firstOutput, missing)
		slog.Info("/// VALIDATOR RE-PROMPT ///", "agent", compiler.Cfg.Name,
			"absent_keys", len(missing), "payload_chars", len(payload))

		art, err := compiler.Execute(ctx, genai.Text(payload))
		if err != nil {
			return nil, err
		}
		return parseRepromptResponse(art.Content, missing)
	}
}

// artifactByAgent returns a named node's output, or "" if it is not present.
func artifactByAgent(memory []*core.Artifact, name string) string {
	for _, a := range memory {
		if a != nil && a.AgentName == name {
			return a.Content
		}
	}
	return ""
}

// parseRepromptResponse reads the re-prompt reply, keeping only requested keys.
// A re-prompt must never be able to overwrite values that were already correct.
func parseRepromptResponse(body string, missing []string) (map[string]string, error) {
	wanted := make(map[string]bool, len(missing))
	for _, k := range missing {
		wanted[k] = true
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("re-prompt response did not parse as JSON: %w", err)
	}

	out := map[string]string{}
	for k, v := range raw {
		key := normalizeTagKey(k)
		if !wanted[key] {
			continue
		}
		if s, ok := v.(string); ok {
			out[key] = s
		} else if v != nil {
			out[key] = fmt.Sprint(v)
		}
	}
	return out, nil
}
