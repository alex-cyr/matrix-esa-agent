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

- **The pipeline aborts on any node error.** `Pipeline.Run` returns a wrapped error naming the failing node; `NewPipeline` rejects nil agents rather than dropping them from the chain. Both used to be silent — a failed node substituted a hardcoded "Arkan Homes / 12690 Morningpark Cir" payload, and nil agents shortened the chain without a word. If you reintroduce a fallback, it must be visibly labelled as such in the output.
- **HITL is structurally present but bypassed.** `Agent.Execute` returns `Approved: false` on every artifact; the API constructs the pipeline with `skipHITL = true`, so approval is auto-granted. The CLI exposes `-skip-hitl`. A genuine yield returns `core.ErrHITLYield`, which `cmd/esad` distinguishes from a real failure (exit 0 vs exit 1) — keep that distinction if you touch the exit paths.
- `Agent.Execute` retries **any** error, currently 2× with a 5s sleep. `maxRetries` is temporarily lowered for testing; restoring it means the loop should first classify errors so non-retryable ones (bad request, auth) surface immediately.
- Model IDs are **hardcoded to `gemini-2.5-pro` in Go** (`modelID` in [cmd/api/main.go](cmd/api/main.go)), and disagree with the `model:` frontmatter inside the SKILL.md files. The Go value wins; the frontmatter is decorative.
- Historical style baselines are transcribed by a dedicated `historical-extractor` agent and cached to `historical/.cache/<sha256>.txt` — see below. Missing baselines degrade the output tone but never fail a request.

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

### Historical style baselines (`internal/core/historical.go`)

`historical/` holds completed human-authored reports whose prose the Template Compiler mirrors. They are PDFs, so `core.LoadHistoricalCorpus` transcribes each one via the `historical-extractor` agent and caches the text at `historical/.cache/<sha256-of-file>.txt`.

- The cache key is a **hash of file contents**, not the name: editing or replacing a report re-extracts it, while renaming or duplicating one reuses the existing text.
- `.txt`, `.md`, and `.docx` are decoded in-process (`core.ExtractDocxText`) and never cost an API call. Only PDFs and images go to the model.
- An in-process memo keyed on a directory fingerprint (names, sizes, modtimes) stops a long-running server re-reading the cache each request, while still noticing new files.
- Warm the cache offline with `go run ./cmd/esad -payload . -warm-historical`; the Dockerfile's existing `COPY historical/` then carries it into the image, so containers never pay extraction cost on a customer request. There is no `.dockerignore`, so the cache is included automatically.
- Files over Vertex's ~20 MB inline blob ceiling cannot be extracted this way and land in `corpus.Failed`. Two current baselines exceed it.

`corpus.PromptBlock()` is a plain string appended to a system prompt, so more than one agent can consume it.

### HTTP layer & storage

`cmd/api/main.go` registers all routes on `http.DefaultServeMux`; `web/index.html` is a single-file vanilla-JS wizard (upload → pre-screen → generate) served at `/web/`.

- `/api/v1/user`, `/projects`, `/projects/create`, `/upload`, `/prescreen`, `/generate`, `/analyze/bucket`, `/download`
- Every handler except `/download` calls `enforceDomainAuth`, which reads the IAP header `X-Goog-Authenticated-User-Email` and requires `@matrixengineeringgroup.com`. **With no header present it returns `elias@matrixengineeringgroup.com` and allows the request** — auth relies entirely on Cloud Run/IAP being in front. Locally, everything is open.
- Storage is dual-path: files are written to both `tmp/esa_inputs/<project>/` and `gs://$ESA_INPUT_BUCKET/esa_inputs/<project>/`. Reads prefer local and fall back to GCS. Reports go to `esa_outputs/<project>/` under both a timestamped name and the fixed `Matrix_Cloud_Final_Report.docx`.
- `/prescreen` currently returns a **hardcoded 4-question list with pre-filled answers**; only the `detected_files` categorization is computed. `/analyze/bucket` likewise hardcodes its answers and re-dispatches into `generateReportHandler`.

Env: `GOOGLE_CLOUD_PROJECT` (default `matrix-esa-production`), `VERTEX_LOCATION` (`us-central1`), `ESA_INPUT_BUCKET` (`matrix-esa-production-vault`), `PORT`. The CLI reads `.env` via godotenv and `GCP_PROJECT`. Auth is Application Default Credentials.

## Runtime file dependencies

The binary reads `.agents/`, `knowledge/`, and `historical/` **relative to the working directory** at request time (hence their explicit `COPY` lines in the Dockerfile). `core.LoadSkill` errors on a missing or empty skill file, and `cmd/api` preflights all six at boot and exits 1 — so a bad container layout fails at deploy time instead of yielding prompt-less agents that still return plausible text.

## Data sensitivity

`edr_source/`, `output/`, and `.env` are gitignored as client M&A material. `historical/` and `tmp/` hold real client reports and generated drafts — do not commit their contents or paste them into external services.





## Known bugs to fix (in order)
1. ~~main.go feeds raw .docx bytes (zip binary) from historical/ into the
   system prompt as "historical context" — must extract real text first.~~
   DONE. It was raw *PDF* bytes (no extension filter), while cmd/esad
   filtered to .txt/.md/.docx and so ingested nothing at all. Both now call
   `core.LoadHistoricalCorpus`.
2. Historical context only reaches the Template Compiler agent; the ASTM
   Synthesizer (which writes the actual lingo/rationales) never sees it.
   `corpus.PromptBlock()` is already shaped for this — append it to the ASTM
   agent config in both entrypoints.
3. Template Compiler must emit ~160 exact {{Key}} JSON keys with no
   validation — missing keys silently become blank fields in the report.
   Need a Go-side validate/diff/re-prompt loop before docx injection.