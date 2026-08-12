// probelocation makes the smallest possible Vertex call against a given
// location, to establish whether a model is reachable there before any real
// work is sent.
//
// Written for one question: the regional input-tokens-per-minute quota in
// us-central1 is capped at 1,000,000 and the project is not eligible for a
// self-serve increase, while the project's GLOBAL input-tokens-per-minute row
// is unlimited. If gemini-2.5-pro answers on location "global", the regional
// wall stops applying.
//
// The SDK supports the location: cloud.google.com/go/vertexai/genai
// special-cases it, using aiplatform.googleapis.com instead of
// <region>-aiplatform.googleapis.com. Whether the MODEL serves traffic there is
// a separate question, and only a live call answers it.
//
//	go run ./tools/probelocation                       # global, gemini-2.5-pro
//	go run ./tools/probelocation -location us-central1 # control
//
// Deliberately tiny: a six-word prompt and a 16-token cap, so the probe costs
// essentially nothing against a quota that is already strained.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"cloud.google.com/go/vertexai/genai"
)

func main() {
	location := flag.String("location", "global", "Vertex location to probe")
	model := flag.String("model", "gemini-2.5-pro", "model ID to probe")
	flag.Parse()

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "matrix-esa-production"
	}

	endpoint := *location + "-aiplatform.googleapis.com"
	if *location == "global" {
		endpoint = "aiplatform.googleapis.com"
	}
	fmt.Printf("probing project=%s location=%s model=%s\n  endpoint %s\n",
		projectID, *location, *model, endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, projectID, *location)
	if err != nil {
		fmt.Printf("  RESULT: client construction failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	m := client.GenerativeModel(*model)
	m.SetMaxOutputTokens(16)
	var temp float32
	m.Temperature = &temp

	start := time.Now()
	resp, err := m.GenerateContent(ctx, genai.Text("Reply with the single word: OK"))
	elapsed := time.Since(start).Round(time.Millisecond)

	if err != nil {
		fmt.Printf("  RESULT: FAILED after %s\n  %v\n", elapsed, err)
		os.Exit(1)
	}

	var prompt, out, total int32
	if resp.UsageMetadata != nil {
		prompt, out, total = resp.UsageMetadata.PromptTokenCount,
			resp.UsageMetadata.CandidatesTokenCount, resp.UsageMetadata.TotalTokenCount
	}
	fmt.Printf("  RESULT: OK in %s — prompt=%d output=%d total=%d tokens\n",
		elapsed, prompt, out, total)
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, p := range resp.Candidates[0].Content.Parts {
			if t, ok := p.(genai.Text); ok {
				fmt.Printf("  model said: %q\n", string(t))
			}
		}
	}
}
