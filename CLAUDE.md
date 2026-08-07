# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Hard rules
- 100% Go. Never introduce Python, Node, or shell-script dependencies.
- Prefer stdlib. Docx handling = archive/zip + encoding/xml. No new
  third-party deps without asking first.
- Never modify .env, service account keys, or anything in edr_source/
  or historical/ — those are data, not code.

## What this is

A Go service that drafts **Phase I Environmental Site Assessment (ESA) reports** under ASTM E1527-21. It ingests EDR PDF packages, field checklists, and site photos, runs them through a sequential chain of Vertex AI (Gemini) agents, and injects the resulting JSON into a Word template (`.docx`) via raw OOXML string replacement. There is no ORM, no framework — stdlib `net/http`, `archive/zip`, and the Vertex AI / GCS SDKs.

## Commands

```powershell
go build -o api.exe ./cmd/api        # HTTP server (Cloud Run entrypoint)
go run ./cmd/api                     # serve on :8080, UI at http://localhost:8080/web/index.html
go build -o esad.exe ./cmd/esad      # local CLI variant
go vet ./cmd/... ./internal/...      # NOTE: scope explicitly — see below
docker build -t matrix-esa-agent .
```

CLI run (reads `<payload>/edr_source/*.pdf`, writes `<payload>/output/`):

```powershell
go run ./cmd/esad -payload . -project $env:GCP_PROJECT -skip-hitl
```

There are no tests in this repo.

**`go build ./...` and `go vet ./...` fail** — `scratch/` holds ~40 standalone `package main` throwaway scripts in one directory, so `main` is redeclared. Always scope commands to `./cmd/...` and `./internal/...`. Don't try to "fix" `scratch/`; it's a junk drawer of one-off template/PDF inspection programs, useful as reference for how to poke at the `.docx` internals.

## Architecture

### The pipeline (`internal/core/`)

`agent.go` → `pipeline.go` implement a minimal SequentialAgent chain. Each `Agent` wraps a Vertex `genai.Client`; its system prompt is the **raw markdown of a `.agents/skills/<name>/SKILL.md` file**, read from disk at request time. The skill files ARE the prompts — most behavior changes belong there, not in Go.

Order (see `generateReportHandler` in [cmd/api/main.go](cmd/api/main.go)):

1. **Parser** — runs *outside* the pipeline, once per uploaded file, with the file bytes as a `genai.Blob`. Output is concatenated into one big text payload.
2. **GeospatialEvaluator** → 3. **SiteReconSynthesizer** → 4. **ASTMSynthesizer** → 5. **TemplateCompiler** — chained by `Pipeline.Run`, which *appends* each agent's output to the running payload (nothing is replaced), so later agents see everything upstream. The final agent's output is the return value and must be a JSON object of template tag → value.

Key behaviors to know before changing anything here:

- **The pipeline never hard-fails on LLM errors.** A failed node substitutes `mockFallbackReportPayload()` — a hardcoded "Arkan Homes / 12690 Morningpark Cir" JSON blob in [pipeline.go](internal/core/pipeline.go). If a generated report comes out with that address, the LLM call failed silently; check the logs, don't chase the template.
- **HITL is structurally present but bypassed.** `Agent.Execute` returns `Approved: false` on every artifact; the API constructs the pipeline with `skipHITL = true`, so approval is auto-granted. The CLI exposes `-skip-hitl`.
- `Agent.Execute` retries any error 10× with a 5s sleep — a genuine (non-quota) error costs ~50s before surfacing.
- Model IDs are **hardcoded to `gemini-2.5-pro` in Go**, and disagree with the `model:` frontmatter inside the SKILL.md files (which say `gemini-2.5-flash`). The Go value wins; the frontmatter is decorative.
- Every `historical/*.pdf` is read and appended verbatim to the TemplateCompiler's system prompt as style-mirroring context. Adding files there directly inflates every generation's token cost.

### DOCX templating

The template is a plain Word file whose text contains `{{TagName}}` placeholders (the full inventory is in `docx_tags.txt`). `mergeDocxLogic` unzips it, and for `word/document.xml` + every `word/header*` / `word/footer*`:

1. `unfractureDocxXML` strips Word's run-splitting XML from *inside* `{{...}}` — Word routinely shatters a placeholder across `<w:r>` runs, so naive replacement misses it.
2. Each JSON key is substituted, both as `{{Key}}` and as a bare `Key` substring.
3. Leftover `{{` / `}}` are stripped globally.

Two independent implementations exist: the API's (`cmd/api/main.go`, brace-based) and the CLI's (`cmd/esad/main.go`, `replaceFracturedXML`, which builds a per-character regex tolerating interleaved tags). They have diverged — fix bugs in both or consolidate deliberately.

`injectFieldDefaults` runs after the LLM and **overrides** template values with hardcoded client data (recipient block, salutation, authorization date, `Proposal_Letter1-5` blanked to prevent a page-2 logo overlap). This is the current per-client hack layer; expect to touch it when onboarding a different client.

Template resolution order: `knowledge/ESA_PHASE_I_Template.docx`, falling back to `ESA_PHASE_I_BLANK_TEMPLATE.docx` at repo root.

### Appendices

`appendix.go` classifies each uploaded file into ASTM Appendix A–H purely by **filename and category substring matching** (`MapCategoryToAppendix`, `FormatIntuitiveTitle`, `DeriveDefaultSource`) — filenames are load-bearing. `NewAppendixPackage` produces a text manifest that is appended to the LLM payload.

`appendix_builder.go` (`BuildCompleteAppendixPDF`) assembles the real deliverable PDF with `gofpdf` + `gofpdi`: cover pages, per-figure Matrix title-block frames, and imported source PDF pages. **It is not wired into the API or CLI yet** — only `scratch/` calls it. Page-count detection works by `recover()`-ing the panic `gofpdi.ImportPage` throws past the last page.

### HTTP layer & storage

`cmd/api/main.go` registers all routes on `http.DefaultServeMux`; `web/index.html` is a single-file vanilla-JS wizard (upload → pre-screen → generate) served at `/web/`.

- `/api/v1/user`, `/projects`, `/projects/create`, `/upload`, `/prescreen`, `/generate`, `/analyze/bucket`, `/download`
- Every handler except `/download` calls `enforceDomainAuth`, which reads the IAP header `X-Goog-Authenticated-User-Email` and requires `@matrixengineeringgroup.com`. **With no header present it returns `elias@matrixengineeringgroup.com` and allows the request** — auth relies entirely on Cloud Run/IAP being in front. Locally, everything is open.
- Storage is dual-path: files are written to both `tmp/esa_inputs/<project>/` and `gs://$ESA_INPUT_BUCKET/esa_inputs/<project>/`. Reads prefer local and fall back to GCS. Reports go to `esa_outputs/<project>/` under both a timestamped name and the fixed `Matrix_Cloud_Final_Report.docx`.
- `/prescreen` currently returns a **hardcoded 4-question list with pre-filled answers**; only the `detected_files` categorization is computed. `/analyze/bucket` likewise hardcodes its answers and re-dispatches into `generateReportHandler`.

Env: `GOOGLE_CLOUD_PROJECT` (default `matrix-esa-production`), `VERTEX_LOCATION` (`us-central1`), `ESA_INPUT_BUCKET` (`matrix-esa-production-vault`), `PORT`. The CLI reads `.env` via godotenv and `GCP_PROJECT`. Auth is Application Default Credentials.

## Runtime file dependencies

The binary reads `.agents/`, `knowledge/`, and `historical/` **relative to the working directory** at request time (hence their explicit `COPY` lines in the Dockerfile). Moving or renaming those directories silently degrades output rather than erroring — `loadSkill` only logs a warning and returns `""`, leaving an agent with no system prompt.

## Data sensitivity

`edr_source/`, `output/`, and `.env` are gitignored as client M&A material. `historical/` and `tmp/` hold real client reports and generated drafts — do not commit their contents or paste them into external services.





## Known bugs to fix (in order)
1. main.go feeds raw .docx bytes (zip binary) from historical/ into the
   system prompt as "historical context" — must extract real text first.
2. Historical context only reaches the Template Compiler agent; the ASTM
   Synthesizer (which writes the actual lingo/rationales) never sees it.
3. Template Compiler must emit ~160 exact {{Key}} JSON keys with no
   validation — missing keys silently become blank fields in the report.
   Need a Go-side validate/diff/re-prompt loop before docx injection.