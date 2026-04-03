# Matrix ESA Agent: Multi-Agentic ADK Framework for Environmental Due Diligence

## Overview
The Matrix ESA Agent is an autonomous, multi-agent AI system designed to automate Phase I Environmental Site Assessments (ESAs) under the strict ASTM E1527-21 standard. 

Built with Google's Agent Development Kit (ADK) and designed for serverless deployment on Google Cloud Run, this "invisible scaffolding" transforms a massive, unstructured data extraction bottleneck into a deterministic, enterprise-ready workflow.

---

## Enterprise Value & Architecture
In the fast-paced world of Mergers & Acquisitions (M&A) and distressed asset accounting, financial due diligence requires speed, precision, and risk mitigation.

- **Cost & Time Optimization:** A standard Phase I ESA takes weeks to complete. Report writing is the largest internal labor cost. By leveraging Gemini 2.5 models on Google Vertex AI, this system reduces a multi-week human bottleneck into an automated pipeline that completes in minutes.
- **Enterprise Bucket Architecture:** Built to safely process payloads exceeding 500MB (massive historical aerial PDFs, topographical maps). It bypasses standard HTTP payload limits by natively integrating with Google Cloud Storage.
- **B2B SaaS Integration:** Operates as a stateless premium JSON REST API, perfectly positioned as the backend logic engine for external software platforms and portals.

## Agent-to-Agent (A2A) Network
Monolithic Large Language Models struggle with context degradation and hallucination when processing dense regulatory data. To solve this, the framework utilizes a structured `SequentialAgent` pipeline written in Golang natively integrated with Vertex AI models.

1. **Parser Agent:** Ingests raw EDR PDF packages and extracts coordinates, elevations, and regulatory data tables.
2. **Geospatial Evaluator Agent:** Analyzes relative risk of off-site regulatory findings by cross-referencing elevation gradients and migration pathways.
3. **ASTM Synthesizer Agent:** Correlates spatial findings with strict ASTM definitions to draft legal rationales (REC, HREC, CREC).
4. **Site Recon Synthesizer Agent:** Translates raw field checklist data into professional engineering paragraphs, enforcing exclusionary boilerplate logic.
5. **Template Compiler Agent:** Yields a strict JSON dictionary to inject via XML directly into the firm's static "ESA PHASE I - Blank Template" document.

---

## API Documentation for Integration Partners

The API provides an enterprise-grade `POST` endpoint designed for integration with frontend React/Next.js web applications. 

### `POST /api/v1/analyze/bucket`

Triggers the full A2A pipeline on a specified Google Cloud Storage folder.

**Request Payload (JSON):**
```json
{
  "input_bucket": "matrix-esa-inputs",
  "folder_prefix": "Property_123_Main_Street/"
}
```

**Workflow:**
1. The endpoint connects to the specified bucket and lists all `.pdf` blobs matching the `folder_prefix`.
2. Blobs are securely downloaded into the serverless container's isolated memory.
3. The ADK pipeline extracts data, analyzes historical records, and compiles the text.
4. The API generates the physical `Matrix_Cloud_Final_Report.docx`.
5. The document is uploaded directly back to `gs://matrix-esa-inputs/Property_123_Main_Street/`.

**Success Response (200 OK):**
```json
{
  "status": "success",
  "message": "Report generated and uploaded to bucket",
  "file_path": "gs://matrix-esa-inputs/Property_123_Main_Street/Matrix_Cloud_Final_Report.docx"
}
```

---

## Deployment Instructions

### Prerequisites
- Google Cloud SDK (`gcloud` CLI) installed locally.
- A Google Cloud Project with Billing Enabled.
- Enabled APIs: `Cloud Run API`, `Vertex AI API`, `Cloud Build API`, `Cloud Storage API`.

### CI/CD Deployment
This repository is connected to Google Cloud Developer Connect. 
Any push to the `vertex-api-migration` branch will automatically trigger a Cloud Build.

```bash
# Push new agent prompts or code updates
git add .
git commit -m "feat: updated ASTM agent prompt logic"
git push origin vertex-api-migration
```

### Local Development & Debugging
To run the server locally while retaining Vertex AI authentication:

1. Obtain a deep Application Default Credential (ADC) token:
   ```bash
   gcloud auth application-default login
   ```
2. Start the Golang server:
   ```bash
   go run cmd/api/main.go
   ```
3. Test locally via `localhost:8080`.
