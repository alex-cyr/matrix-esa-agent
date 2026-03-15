package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/google/generative-ai-go/genai"
	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

// ExtractEDRSuite reads all PDFs in the EDR suite directory and feeds them to the Parser Agent
func ExtractEDRSuite(ctx context.Context, pAgent *core.Agent, payloadDir string) (string, error) {
	log.Printf("MATRIX EXECUTABLE LOADED: Initiating Full Suite PDF Extraction for %s\n", payloadDir)

	// 1. Read all PDF files from the data folder
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		return "", fmt.Errorf("failed to read payload directory: %w", err)
	}

	var parts []genai.Part

	// 2. Define the strict extraction prompt based on our Logic Matrix
	extractionPrompt := `You are the Parser Agent. I have attached the full EDR Suite (multiple PDFs), including site proposals and checklists.
	You must extract TWO main sets of data:
	
	1. PROJECT METADATA: Extract the Subject Property Address (Street, City, State, Zip), the Proposal Recipient Name(s) / Company, the Project Date, and Project Number (if any).
	
	2. MAPPED SITES SUMMARY: Extract the table or equivalent data detailing coordinates and regulatory databases. You must extract the following exact columns for each site found:
	- MAP ID
	- SITE NAME
	- DATABASE ACRONYMS
	- RELATIVE DIST (ft. & mi.)
	- ELEVATION
	
	Return ALL extracted data as a single structured JSON payload.`

	parts = append(parts, genai.Text(extractionPrompt))

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".pdf" {
			continue
		}
		
		pdfPath := filepath.Join(payloadDir, entry.Name())
		log.Printf("/// INGESTING NODE PAYLOAD ///: %s\n", entry.Name())
		
		pdfBytes, err := os.ReadFile(pdfPath)
		if err != nil {
			log.Printf("WARNING: failed to read EDR report %s: %v", entry.Name(), err)
			continue
		}
		parts = append(parts, genai.Blob{MIMEType: "application/pdf", Data: pdfBytes})
	}

	if len(parts) == 1 {
		return "", fmt.Errorf("no pdf files found in payload directory")
	}

	// 3. Feed the prompt and the PDF bytes into the Agent's Execute function as multimodal parts.
	log.Println("/// NODE ENGAGED ///: Parsing Full EDR Suite...")
	
	response, err := pAgent.Execute(ctx, parts...)
	if err != nil {
		return "", fmt.Errorf("parser agent failed to process documents: %w", err)
	}

	log.Println("SIG_YIELD: Full Suite Extraction Successful.")
	return response.Content, nil
}
