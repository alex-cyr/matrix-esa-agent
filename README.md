# Matrix ESA Agent: Multi-Agentic Framework for Environmental Due Diligence

**Developed by:** CyR R&D Lab in technical partnership with Matrix Engineering Group, Inc.

## Overview
The Matrix ESA Agent is an autonomous, multi-agent AI system designed to automate Phase I Environmental Site Assessments (ESAs) under the strict ASTM E1527-21 standard. 

Built entirely in **Golang** using Google's Agent Development Kit (ADK) and Antigravity, this "invisible scaffolding" transforms a massive, unstructured data extraction bottleneck into a deterministic, enterprise-ready workflow. It is designed to be 100% "Google Maxed"—running entirely within Google Cloud Run, Vertex AI, and Google Cloud Storage (GCS) without any reliance on fragile local Windows environments, Python frontends, or external dependency bloat.

---

## Enterprise M&A Integration Strategy

This system is built as a highly scalable **B2B SaaS Backend**, explicitly structured for a 3-tier enterprise partnership and acquisition model:

1. **CyR R&D Lab (Core IP):** Engineering the proprietary Golang AI architecture, Agent-to-Agent (A2A) networking, and Vertex data flow limits.
2. **Matrix Engineering (Domain Experts):** Providing the foundational engineering logic, the static ASTM template matrices, and rigorous Human-in-the-Loop (HITL) quality assurance testing.
3. **Vahalo (Enterprise Distributor):** Integrating this stateless, 100% headless JSON API directly into their massive customer-facing web platform. Vahalo provides the UI/UX frontend; Matrix provides the AI engine. 

---

## The "Google Max" Production Workflow
The entire pipeline bypasses standard HTTP 32MB payload limits by executing operations directly against Google Cloud Storage buckets at gigabit speeds. 

**Zero local software is required.** An Environmental Professional can execute the entire pipeline seamlessly from a web browser:

### Step 1: Secure Data Drop
1. Log into the Google Cloud Console.
2. Navigate to the Cloud Storage Bucket (e.g., `matrix-esa-production-vault`).
3. Create a project folder inside `esa_inputs/` (e.g., `esa_inputs/Loganville_Medical_Property/`).
4. **Drag and drop** massive, uncompressed EDR packages (Aerials, Topo Maps, Radius Maps) and Adobe field checklists directly into the browser.

### Step 2: Fire the API (Cloud Shell)
Open the Linux **Cloud Shell** terminal at the bottom of the Google Cloud Dashboard and trigger the Vertex AI engine with a single webhook ping:

```bash
curl -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: bearer $(gcloud auth print-identity-token)" \
  -d '{"input_bucket": "matrix-esa-production-vault", "folder_prefix": "esa_inputs/Loganville_Medical_Property/"}' \
  https://matrix-esa-agent-git-130435836160.europe-west1.run.app/api/v1/analyze/bucket
```

### Step 3: Engine Execution & Output
Upon receiving the command:
1. The **Cloud Run (Golang)** server wakes up.
2. It detects the folder and simultaneously streams all PDFs into its isolated, serverless memory.
3. It passes the data through the highly locked-down Vertex AI `SequentialAgent` pipeline (Parser -> Geospatial -> ASTM -> TemplateCompiler).
4. The internal `go-docx` tool unpacks Matrix's proprietary blank template (embedded directly in the GitHub container), injects the XML findings, and re-compresses the document.
5. The API magically spawns the finished **`Matrix_Cloud_Final_Report.docx`** and drops it into `esa_outputs/Loganville_Medical_Property/`.

---

## Architecture: Agent-to-Agent (A2A) Network
Monolithic Large Language Models struggle with context degradation when processing dense regulatory data. To solve this, the framework utilizes a 5-step microservices architecture:

1. **Parser Agent:** Ingests raw EDR PDF packages and extracts exact coordinates.
2. **Geospatial Evaluator Agent:** Analyzes the relative risk of off-site regulatory findings by cross-referencing elevation gradients.
3. **ASTM Synthesizer Agent:** Correlates spatial findings with strict ASTM E1527-21 definitions to draft legal rationales (REC, HREC, CREC).
4. **Site Recon Synthesizer Agent:** Translates raw field checklist data into professional engineering paragraphs, enforcing exclusionary boilerplate logic.
5. **Template Compiler Agent:** Yields a strict JSON dictionary to inject directly into the static "ESA PHASE I - Blank Template" document.

## Engineering Features
- **Stateless Cloud Native Architecture:** Scales from zero to thousands of containers instantly.
- **Enterprise Data Privacy:** Utilizing Vertex AI ensures that proprietary real-estate data and Client M&A secrets are NEVER used to train Google's public models.
- **Automated Routing:** Eliminates file management overhead by intelligently mapping `esa_inputs` drops directly to `esa_outputs` folders.
