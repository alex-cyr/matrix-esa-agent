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

	var fullExtractedData string

	// 2. Define the strict extraction prompt based on our Logic Matrix
	extractionPrompt := `You are the Parser Agent. I have attached an EDR Suite document (such as a site proposal or checklist).
	You must extract TWO main sets of data, if present in this specific document:
	
	1. PROJECT METADATA: Extract the Subject Property Address (Street, City, State, Zip), the Proposal Recipient Name(s) / Company, the Project Date, and Project Number (if any).
	
	2. MAPPED SITES SUMMARY: Extract the table or equivalent data detailing coordinates and regulatory databases. You must extract the following exact columns for each site found:
	- MAP ID
	- SITE NAME
	- DATABASE ACRONYMS
	- RELATIVE DIST (ft. & mi.)
	- ELEVATION
	
	Return ALL extracted data found in this specific document as a structured JSON payload. If the document does not contain this information, return an empty JSON object {}.`

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".pdf" {
			continue
		}
		
		pdfPath := filepath.Join(payloadDir, entry.Name())
		log.Printf("/// INGESTING NODE PAYLOAD (SEQUENTIAL) ///: %s\n", entry.Name())
		
		pdfBytes, err := os.ReadFile(pdfPath)
		if err != nil {
			log.Printf("WARNING: failed to read EDR report %s: %v", entry.Name(), err)
			continue
		}
		
		var parts []genai.Part
		parts = append(parts, genai.Text(extractionPrompt))
		parts = append(parts, genai.Blob{MIMEType: "application/pdf", Data: pdfBytes})

		log.Printf("/// NODE ENGAGED ///: Parsing Document: %s\n", entry.Name())
		
		response, err := pAgent.Execute(ctx, parts...)
		if err != nil {
			log.Printf("WARNING: parser agent failed to process document %s: %v", entry.Name(), err)
			continue
		}
		
		fullExtractedData += "\n\n=== [DOCUMENT EXTRACT: " + entry.Name() + "] ===\n" + response.Content
	}

	if fullExtractedData == "" {
		return "", fmt.Errorf("no data extracted from any pdf files in payload directory")
	}

	log.Println("SIG_YIELD: Sequential Suite Extraction Successful.")
	return fullExtractedData, nil
}
