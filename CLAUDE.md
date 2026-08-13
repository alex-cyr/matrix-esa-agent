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
go run ./tools/fixheader             # one-shot: repair the running-header parts
go run ./tools/draftnote             # one-shot: add {{DraftNote}} to the cover
docker build -t matrix-esa-agent .
```

CLI run (reads `<payload>/edr_source/*.pdf`, writes `<payload>/output/`):

```powershell
go run ./cmd/esad -payload . -project $env:GCP_PROJECT -skip-hitl
```

```powershell
go test ./internal/...               # cache/corpus tests; no credentials needed
go run ./cmd/esad -payload . -project matrix-esa-production -warm-historical
$env:LOG_LEVEL="debug"; go run ./cmd/api   # per-node artifact previews
```

`LOG_LEVEL=debug` enables `NODE ARTIFACT PREVIEW` — the first ~200 characters of each pipeline node's actual output. Reach for it before theorizing about why a node's output looks wrong; direct inspection beats ranking hypotheses, as the SiteRecon 520-token investigation demonstrated.

### Tests

Coverage is deliberate and narrow — every test guards behavior that previously failed silently, most of it in a delivered report. Nothing else is tested.

- `internal/core/historical_test.go` — corpus caching: content-hash keying, invalidation on content change, reuse on rename/duplicate, local formats bypassing the model, and per-file failures being recorded without aborting.
- `internal/core/baseline_test.go` — baseline coverage and the draft note: cache-coverage counting, coverage never calling the model, the nil extractor recording a failure, failures being retried while successes are reused, and atomic cache writes.
- `internal/core/retry_test.go` — error classification and both backoff schedules, including the 1000-page `InvalidArgument` classifying permanent through two layers of `fmt.Errorf` wrapping.
- `internal/core/reportdate_test.go` — the house date format and the UTC-evening rollover, in daylight and standard time.
- `cmd/api/merge_test.go` — tag replacement, post-model field injection, the deterministic header, and the cover draft note.
- `cmd/api/download_test.go` — `/download` auth and path-traversal rejection.

**Do not "clean up" `TestInjectFieldDefaultsCarriesNoClientHardcodes`.** It is a deliberate regression tripwire: it fails the build if the strings `Arkan`, `Morningpark`, `Hashem`, `Gwinnett`, or `Roswell` reappear in `injectFieldDefaults` output. Those hardcodes silently pinned every generated report to one client's recipient block and site address regardless of the actual project, and the shape of that function invites their return. `TestReplaceTagOrderIndependent` is likewise load-bearing — it proves colliding keys (`ParcelID` vs `SiteParcelID`) converge on the same document regardless of Go's randomized map order.

**`go build ./...` and `go vet ./...` fail** — `scratch/` holds ~40 standalone `package main` throwaway scripts in one directory, so `main` is redeclared. Always scope commands to `./cmd/... ./internal/... ./tools/...`. Don't try to "fix" `scratch/`; it's a junk drawer of one-off template/PDF inspection programs, useful as reference for how to poke at the `.docx` internals.

`tools/` is different from `scratch/` and is **not** gitignored: it holds one-shot maintenance programs that modify committed binaries. Because the template is a `.docx`, a change to it is unreviewable in a diff — the tool is the diff, which is why it has to be checked in. Each is idempotent and validates the rewritten file before replacing the original.

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

1. `unfractureDocxXML` runs two passes. **(a)** Rejoins brace pairs Word split across runs — in `header9`/`header10` the *opening* `{{` of `{{ReportDate}}` is two separate `<w:t>` runs, which the complete-tag regex cannot see, so that tag survived into a delivered report. **(b)** Strips the run-splitting markup from *inside* now-complete `{{...}}`. Pass (a) is bounded to 12 intervening tags so two unrelated braces cannot be welded into a placeholder.
2. Each value is passed through `core.SanitizeDocxValue`, then substituted for its **exact** `{{Key}}` only.
3. Any `{{Tag}}` still present is logged at error level (`UNREPLACED TEMPLATE TAGS`), **and** orphaned `{{` with no close is logged separately (`FRACTURED TAG SURVIVORS`). The second count exists because the first is blind to fractured survivors — the Providence Road run reported 13 unfilled tags when 15 were actually unfilled. Both feed the Phase 4 validator.
4. Leftover `{{` / `}}` are stripped globally.
5. **Post-merge validation** (`core.ValidateDocxXML`) re-opens the written file and parses every rewritten part. A malformed artifact fails the request; it is never uploaded to GCS or offered for download.

**`core.SanitizeDocxValue` is not optional.** Values are injected as raw text into XML, so it must run on every one, in this order: strip XML-1.0-illegal control characters → escape `&`, `<`, `>` → expand newlines into `</w:t><w:br/><w:t>`. The order matters — escaping after the break expansion would mangle the break markup, and escaping before the control-char strip would leave characters no parser accepts. A single unescaped `&` (e.g. an owner named "Diane & Brian J. Pete", or the address "12735 & 12725 Providence Road") makes Word refuse the whole document with "experienced an error trying to open the file."

Two merge implementations still exist — the API's brace-based `replaceTag` and the CLI's `replaceFracturedXML` (a per-character regex tolerating interleaved tags). **They now share sanitization and validation via `internal/core/docxsafe.go`**; only the matching strategy differs. The CLI additionally uses `ReplaceAllLiteralString`, because `$` in a value would otherwise expand as a capture-group reference.

`injectFieldDefaults` runs after the LLM and applies exactly three things: `MEG-` prefix normalization on `ProjectNo`, `parcel_id`/`site_acreage` from EP pre-screen answers, and blanking `Proposal_Letter1-5` (a layout hack — populating those text boxes overlaps the page-2 logo). It also normalizes keys/values that arrive wrapped in their own braces, which matters because `replaceTag` matches exactly. It deliberately contains **no client-specific data**; see the tripwire test.

Template resolution order: `knowledge/ESA_PHASE_I_Template.docx`, falling back to `ESA_PHASE_I_BLANK_TEMPLATE.docx` at repo root.

### Appendices

`appendix.go` classifies each uploaded file into ASTM Appendix A–H purely by **filename and category substring matching** (`MapCategoryToAppendix`, `FormatIntuitiveTitle`, `DeriveDefaultSource`) — filenames are load-bearing. `NewAppendixPackage` produces a text manifest that is appended to the LLM payload.

`appendix_builder.go` (`BuildCompleteAppendixPDF`) assembles the real deliverable PDF with `gofpdf` + `gofpdi`: cover pages, per-figure Matrix title-block frames, and imported source PDF pages. **It is not wired into the API or CLI yet** — only `scratch/` calls it. Page-count detection works by `recover()`-ing the panic `gofpdi.ImportPage` throws past the last page.

### Historical style baselines (`internal/core/historical.go`)

`historical/` holds completed human-authored reports whose prose the Template Compiler mirrors. They are PDFs, so `core.LoadHistoricalCorpus` transcribes each one via the `historical-extractor` agent and caches the text at `historical/.cache/<sha256-of-file>.txt`.

- The cache key is a **hash of file contents**, not the name: editing or replacing a report re-extracts it, while renaming or duplicating one reuses the existing text. Hashes are memoized on name+size+modtime, so a warm cache costs a stat rather than re-reading 300 MB of PDFs.
- **Extraction never happens on the request path.** `buildAgents` passes a nil extractor; a cache miss is a recorded failure. Extraction is exclusively `esad -warm-historical`.
- **Boot refuses to start on a cold or partial cache** (`preflightHistoricalCache` → `core.HistoricalCoverage`, which reads the cache without calling the model). The corpus is **19 files, fully cached — coverage is 19/19 and strict mode is the production posture.** `ESA_ALLOW_PARTIAL_BASELINE=1` downgrades the abort to a warning; it is a **dev-only escape hatch and is never set on Cloud Run.** If a deploy needs it, the cache is wrong — fix the cache, don't set the variable.
- Cache writes are atomic (temp file + `Sync` + rename). They used to go straight to the final path while the read side only checked for non-empty content, so an interrupted write left a truncated transcript that was trusted forever.
- The in-process memo caches **successes only**. A fingerprint hit with failures present reuses the extracted text and retries just the failures, so a transient quota blip no longer drops a baseline for the whole process lifetime.
- `/generate` returns `baseline: {used, total, missing}`. When incomplete, `{{DraftNote}}` on the cover page renders `[DRAFT NOTE — INTERNAL: generated against N of M historical baselines — remove before issuance]`. It is a **drafting artifact, not an ASTM data gap** — a thin style baseline says nothing about information required by E1527-21, and recording it as a data gap would put a false regulatory finding in a signed report. The placeholder lives in an existing empty cover paragraph (added by [tools/draftnote](tools/draftnote/main.go)), so a complete baseline costs no layout at all.
- `.txt`, `.md`, and `.docx` are decoded in-process (`core.ExtractDocxText`) and never cost an API call. Only PDFs and images go to the model.
- An in-process memo keyed on a directory fingerprint (names, sizes, modtimes) stops a long-running server re-reading the cache each request, while still noticing new files.
- Warm the cache offline with `go run ./cmd/esad -payload . -warm-historical`; the Dockerfile's existing `COPY historical/` then carries it into the image, so containers never pay extraction cost on a customer request. **`.dockerignore` deliberately does NOT exclude `historical/`** — it excludes `.env`, `tmp/`, `edr_source/`, `output/`, `historical_excluded/`, `scratch/` and `.git`, so secrets and unused client material stay out of the build context while the cache still bakes in.
- The real extraction limit is **page count, not bytes**: Vertex rejects documents over **1000 pages** (`InvalidArgument`). Byte size has not proven to be a practical constraint — baselines of 48.8 MB and 20.1 MB both extracted fine. Extraction failures land in `corpus.Failed` rather than aborting, whatever the cause.
- **`historical_excluded/` holds stamped reports held out of the corpus.** Currently one: `5 Hidden Hills Parcels ESA-Phase I Oct 2022.pdf`, at **1400 pages against Vertex's 1000-page limit**, which no amount of cache warming can fix — with it present, coverage was pinned at 19/20 and strict preflight could never pass. It is **moved, never deleted**: it is a stamped house report and **returns to `historical/` once the page-split lands** (Phase 6 backlog — only ~20 of its 1400 pages are the report proper; the rest is EDR appendix printouts the extractor would discard anyway). Same client-material rules as `historical/`: gitignored, never committed, never pasted into external services.

`corpus.PromptBlock()` is a plain string appended to a system prompt, so more than one agent can consume it.

### HTTP layer & storage

`cmd/api/main.go` registers all routes on `http.DefaultServeMux`; `web/index.html` is a single-file vanilla-JS wizard (upload → pre-screen → generate) served at `/web/`.

- `/api/v1/user`, `/projects`, `/projects/create`, `/upload`, `/prescreen`, `/generate`, `/analyze/bucket`, `/download`
- Every handler calls `enforceDomainAuth`, which reads the IAP header `X-Goog-Authenticated-User-Email` and requires `@matrixengineeringgroup.com`. **With no header present it returns `elias@matrixengineeringgroup.com` and allows the request** — auth relies entirely on Cloud Run/IAP being in front. Locally, everything is open.
- `/download` serves from exactly two sources: the per-request `matrix-generate-*` temp directory, and `esa_outputs/<project>/<file>` in the bucket (Cloud Run instances are ephemeral, so the local copy is often gone by the time the download arrives). `safeDownloadName` and `safeProjectName` **reject** rather than normalize — a traversal attempt shows up in the logs as `DOWNLOAD REJECTED` instead of quietly succeeding against a neighbouring file. Downloads are limited to `.docx` and `.pdf`.
- Storage is dual-path: files are written to both `tmp/esa_inputs/<project>/` and `gs://$ESA_INPUT_BUCKET/esa_inputs/<project>/`. Reads prefer local and fall back to GCS. Reports go to `esa_outputs/<project>/` under both a timestamped name and the fixed `Matrix_Cloud_Final_Report.docx`.
- `/prescreen` currently returns a **hardcoded 4-question list with pre-filled answers**; only the `detected_files` categorization is computed. `/analyze/bucket` likewise hardcodes its answers and re-dispatches into `generateReportHandler`.

Env: `GOOGLE_CLOUD_PROJECT` (default `matrix-esa-production`), `VERTEX_LOCATION` (`us-central1`), `ESA_INPUT_BUCKET` (`matrix-esa-production-vault`), `PORT`. The CLI reads `.env` via godotenv and `GCP_PROJECT`. Auth is Application Default Credentials.

### Deploys are image-based, never source-based

**Service:** `matrix-esa-agent-git` · **region:** `europe-west1` · **project:**
`matrix-esa-production`. Env vars on the service: `VERTEX_LOCATION=global`,
`VERTEX_TOKENS_PER_MINUTE=5000000`.

**The Cloud Build trigger is DISABLED, deliberately** (`fb05d786-…`, fired on
push to `vertex-api-migration`). It source-deployed from GitHub, and
`historical/` is gitignored client material — so every triggered build produced
a container with **no baselines**, which the strict preflight correctly refused
to start: `STARTUP ABORTED … baseline="0/0"`. Revisions 00019–00028 are that
failure repeating on every push. Re-enabling the trigger re-breaks the service.

Deploy procedure:

```powershell
$env:CLOUDSDK_PYTHON="$env:LOCALAPPDATA\Google\Cloud SDK\google-cloud-sdk\platform\bundledpython\python.exe"
gcloud builds submit --project matrix-esa-production --region europe-west1 `
  --tag europe-west1-docker.pkg.dev/matrix-esa-production/cloud-run-source-deploy/matrix-esa-agent-baked:<sha> .
gcloud run deploy matrix-esa-agent-git --project matrix-esa-production --region europe-west1 `
  --image <same tag> --set-env-vars "VERTEX_LOCATION=global,VERTEX_TOKENS_PER_MINUTE=5000000"
```

`CLOUDSDK_PYTHON` is required on this machine — the Windows Store Python alias
shadows the interpreter gcloud needs.

**Two ignore files, both load-bearing, for different reasons.** `.dockerignore`
governs the Docker build; **`.gcloudignore` governs what is uploaded at all**,
and without it `gcloud builds submit` falls back to `.gitignore` — which
excludes `historical/*`. That produced a clean build of an image with an empty
`historical/`, and a revision that aborted at boot exactly like the trigger's.
Both files must let `historical/` through.

Rollback is routing traffic back to the previous revision; old revisions are
left in place.

### Vertex quota — the binding limit is per-minute, per-region

**Region is `us-central1`.** `VERTEX_LOCATION` is unset in `.env`, the Dockerfile and every yaml, so the hardcoded default applies. A quota increase is filed against that region's row. *(If the deployed Cloud Run service sets `VERTEX_LOCATION` in its own service config, confirm there too — the repo cannot see it.)*

The ceiling that fails runs is **1,000,000 input tokens per minute, regional**. The daily budget is 1B and barely touched, so a `ResourceExhausted` here is almost never "out of quota for the day" — it is a burst.

**The parser loop is what bursts.** It walks every uploaded file with the bytes attached as a `Blob`; a Providence generate is 13 files and ~72 MB. On 2026-08-12 it pushed ~40 MB into the single minute 12:39:47–12:40:47 — four large PDFs plus a 19 MB PNG — and the region rejected the rest of the run. The pipeline nodes are **not** the pressure: after Phase 5 their prompts are ~16k and ~19k tokens.

`core.Pacer` holds a rolling one-minute total under a budget (`VERTEX_TOKENS_PER_MINUTE`, default 800k for headroom), charging each parser call an estimate of `bytes / 25` (`VERTEX_BYTES_PER_TOKEN`). The estimate deliberately runs **high**: over-estimating costs a short wait, under-estimating costs the whole run. For Providence that projects ~3.0M tokens and a parser stage of ~3.8 minutes.

A single file bigger than the whole budget cannot be paced under it — the 19 MB PNG alone estimates ~760k tokens. The pacer logs that case and proceeds rather than stalling forever, because no wait can help it.

## Runtime file dependencies

The binary reads `.agents/`, `knowledge/`, and `historical/` **relative to the working directory** at request time (hence their explicit `COPY` lines in the Dockerfile). `core.LoadSkill` errors on a missing or empty skill file, and `cmd/api` preflights all six at boot and exits 1 — so a bad container layout fails at deploy time instead of yielding prompt-less agents that still return plausible text.

## Data sensitivity

`edr_source/`, `output/`, and `.env` are gitignored as client M&A material. `historical/`, `historical_excluded/`, and `tmp/` hold real client reports and generated drafts — do not commit their contents or paste them into external services.





## EP-caught errors

Failure modes found by Environmental Professional review of delivered drafts.
These are permanent institutional memory: each one shipped, looked correct, and
was caught by a human rather than by any check in this repo. **Do not weaken or
"simplify" the guards listed here.**

### 1. Fabricated REC from a misidentified field artifact (Providence Road)

The pipeline asserted a REC for a potential former septic component. The feature
was actually a water meter / well cap. **The reasoning chain was internally
valid; the premise was false** — and a sound argument from a false premise
produces a fabricated finding in a signed, sealed report. This is the most
dangerous failure mode in the system precisely because the output reads as
rigorous.

Fixed across three skills, defence in depth at each stage where the error could
enter:
- **parser** — *Verbatim Label Protocol.* Photo tags and survey callouts carry
  the original wording ("paved over junction box"), never a normalized
  interpretation. Normalization smuggles in a conclusion the inspector never
  made, and downstream agents cannot tell it from an observation.
- **site-recon-synthesizer** — *Ambiguous Feature Rule.* Describe, never
  diagnose. Neutral physical description plus
  `[UNIDENTIFIED UTILITY FEATURE — EP TO VERIFY: <verbatim label>]`.
- **astm-synthesizer** — *Field-Observation Confidence Gate.* A
  field-derived REC requires EXPLICIT identification in the checklist or a photo
  tag. Caps, boxes, cleanouts, meters, vaults and paved-over fittings are never
  classified as septic/UST/AST/waste infrastructure.

The gate is deliberately scoped to **field observations only**. Regulatory
listings and documentary evidence are evaluated normally — the rule removes
invented findings, not real ones.

### 2. Splice duplication

Template lead-in text plus a value that restated the lead-in produced doubled
sentences in the delivered report (`User_Authorization`, `Sec4_4`, topographic
summary, Section 10 Opinions opener). Values must *continue* the template's
surrounding sentence, never restate it, and must not add a period the template
already supplies. Fixed in template-compiler SKILL.md.

### 3. Emptied letter recipient block, and the overlap it was hiding

The unconditional `Proposal_Letter1-5` blanking in `injectFieldDefaults` wiped
Providence Road's letter recipient block, and contradicted template-compiler
rule 3, which instructs the model to populate those very lines.

**The overlap it suppressed was never caused by address lines.** A historical
screenshot identified the real defect: an earlier pipeline routed body
paragraph prose and the "Re:" subject block into the `Proposal_Letter` tags.
Those render in **floating text boxes anchored near the page-2 header** — they
expanded over the Matrix logo and pushed the signature block onto page 3.
Blanking suppressed the symptom while destroying correct data.

Retired, and replaced with two guards on the actual defect:
- `auditPayloadValues` logs any `Proposal_To*` / `Proposal_Letter*` value over
  60 characters as a probable misrouted value.
- template-compiler rule 3 now states these are **short address lines only —
  never sentences, never the "Re:" subject, never body prose.**

If the overlap ever recurs, the cause is an over-long value, not the presence of
values. Fix the routing; do not reinstate the blanking.

### 4. Client address printed as the subject property

The same historical screenshot showed the cover page reading "At 12690
Morningpark Cir, Roswell, GA 30075" — the **client's mailing address** — while
the actual subject property was Beavers Road Tract. This is precisely what
template-compiler's **CRITICAL ADDRESS SEPARATION RULE** (rule 14) exists to
prevent: `SiteStreetAddress` / `SiteCityStateZip` / `SiteFullAddress` come from
the EDR target property, while `Proposal_To*` / `Proposal_Letter*` carry the
client's corporate mailing address.

**Rule 14 and rule 3 are both load-bearing. Neither is redundant boilerplate.**
Between them they prevent a report that names the wrong property — the single
most consequential error this system can make — and one that ships with no
recipient at all.

### 5. Splice duplication, seen in the wild

The same artifact contains the delivered text: *"This work was performed in
accordance with Matrix Engineering Group was authorized under signed proposal
dated July 06, 2026.."* — the template lead-in, the restated lead-in, and a
doubled period. Concrete evidence for the splice rule and trailing-punctuation
rule in template-compiler SKILL.md.

### 6. Regional gradient reported where the parcel-scale gradient is opposite

Caught in PE/EP review of the Providence Road draft. The pipeline reported
groundwater flow as **westerly** — the regional/quadrangle-scale direction —
when flow at the **parcel** is **easterly**. The site sits below a ridge running
along Providence Road, and water drains east toward the nearer tributary. A
regional gradient is not wrong in the abstract; it is wrong as an answer to a
question that is always asked about the subject property. The report reads
authoritatively either way, so nothing downstream catches it.

**SITE-SCALE GRADIENT RULE — all spatial analysis is subject-property-centric.**
Determine flow direction at **parcel scale**, by comparing contour elevations at
the property's own boundaries — not by reading the dominant slope of the
quadrangle.

Source precedence, in order:
1. **EDR GeoCheck's computed topographic gradient — primary.** The parser must
   extract it explicitly rather than leaving it for a downstream agent to
   infer.
2. **Contour interpretation at the parcel boundaries — secondary**, used when
   GeoCheck supplies nothing.
3. **If the sources disagree, or confidence is low, emit
   `[EP VERIFY: groundwater flow direction]`.** Never a confident guess. An
   uncertain flow direction flagged for the EP costs a minute of review; a
   confident wrong one propagates into the migration-pathway discussion and the
   Section 10 opinion.

### 7. Model-composed running header

Also from the Providence review. The running header is **entirely
deterministic** — template formatting plus Go-supplied values. **The model never
composes header content.** Today's output had a model-guessed `ReportDate`,
doubled spacing in the date cell (residue from the `header8` fracture repair),
and a stray underline on the project-number run.

Format contract, taken from the stamped house reports:

| | left | right |
|---|---|---|
| line 1 | `Environmental Site Assessment - Phase I` | `[Month D, YYYY]` (generation date) |
| line 2 | `[Site Address] - [Project Descriptor]` | `MEG  Project No. [number]` |

`ReportDate` is generated in Go (`core.ReportDateNow`, `"January 2, 2006"`, in
the office time zone). The model key is still accepted and always overwritten —
a date the model invents is a date nobody chose.

## House conventions

Firm formatting rulings from Elias. They are project truth, not style
preferences: the historical baselines shipped to the model contain the *older*
conventions, so anything not pinned deterministically in Go will drift back.

- **Project number prints verbatim, as the user entered it.** The old `.XX`
  suffix convention is **retired**. Older stamped reports in `historical/` still
  carry it and are actively present in the prompt context, so the model will
  imitate it unless the value is pinned. Never synthesize or append a suffix.
- **The header line-2 project descriptor is sourced, never invented.** It comes
  from the proposal when one is extracted from the uploads, else from the
  intake-form value the user typed at generation. If neither supplies one, the
  line is **address-only**. *Not yet plumbed — see below.*
- **`MEG  Project No.` keeps its double space.** Intentional house formatting in
  the stamped template. Do not normalize it. The template carries `<w:noProof/>`
  on that run so Word's grammar checker cannot draw a squiggle under it — that
  squiggle, not any `<w:u>` run, was the "stray underline" seen in review.

- **"Go-supplied" means Go-guaranteed — which is why an override is a third,
  separate category.** A key in `goSuppliedKeys` is written on *every* run: the
  EP's answer when there is one, a visible bracket when there is not. That makes
  the corresponding intake question effectively mandatory, so the category is
  only correct for facts that genuinely live in the user's head.

  `SiteCounty` is deliberately **not** in that set, and the omission is a
  decision rather than an oversight. Go writes it only when intake supplies it;
  otherwise the model's value stands and the fragment normalizer still strips a
  duplicated "County". Making it guaranteed would bracket the county whenever
  the field was left blank — demanding a typed answer for a fact the EDR package
  states plainly, which is the laundering failure mode the prescreen design
  principle forbids, pointed at the EP instead of at the parser. A blank field
  plus a model failure still lands in the two-tier validation path, so no silent
  blank is possible either way.

  The three categories, kept distinct: **Go-guaranteed** (always written,
  brackets when absent), **override** (Go wins when the human speaks, model plus
  normalizer when they don't), and **model context** (no deterministic path at
  all).

  **`ProjectNo` is Go-guaranteed, and that was a correction.** It sat in
  `goSuppliedKeys` while quietly falling back to the model's value, which the
  Phase 6 empty-intake acceptance run exposed: it printed `303315`, read out of
  the uploaded proposal. Right by luck — and the same class of defect as the
  `'MEG-' + Math.random()` generator the kill commit removed, a plausible
  identifier nobody assigned printed on every page of a sealed report. The
  number is EP-assigned law and required at project creation, so absent now
  yields `[MEG DATAGAP: project number]`. **Being listed in `goSuppliedKeys` is
  not the same as behaving that way; check the code, not the map.**

- **Verifiers must be built from independent assumptions.** A check that shares
  its assumptions with the thing it checks **confirms that thing instead of
  testing it**, and reports success while the defect sits in the output. Build
  the verifier from a different direction, and prefer it to be *broader* than
  what it verifies.

  Three times this has already paid, each in a different shape:
  - **The contamination scan.** It reported *clean* while `Old Field Road NW`
    and `Adairsville, GA` sat in the digest — masker and scan both assumed a
    street number and a fixed city list. Rebuilt deliberately wider (street
    names with no number, any `City, ST`, any `X County`), it immediately
    caught an occupant address in Knoxville, TN that the Georgia-only masker
    had missed.
  - **`goSuppliedKeys` vs `injectFieldDefaults`.** The divergence test checks
    them against *the template*, not against each other — which is why it found
    four dead key spellings, including the acreage the EP typed by hand.
  - **The corpus miner.** Trusting its first output would have shipped a table
    missing every canonical block. Testing it against passages predicted
    *independently* — CLAUDE.md's own Tier-1 list — exposed three bugs at once.

  The general form: **do not let the thing that produces an answer also decide
  whether the answer is right.**

- **Absence of an answer is never an answer of absence.** A question the user
  never answered and a question they answered "none" are different facts, and
  collapsing them attributes a disclosure to someone who never made one. This
  bites hardest on the 40 CFR 312 user obligations, where the fabricated version
  is a legal statement in a signed report.

  The distinction is now carried at four layers, and it took a defect at each to
  learn that three is not enough: `Knowledge.Known` is a **pointer** in the
  intake schema (nil / false / true); `userKnowledgeBlock` emits nothing for a
  request with no intake but `NOT ANSWERED` for a supplied intake left blank;
  the web form's selects lead with `— not answered —` so an untouched control
  submits null rather than its first option; and template-compiler rule 2 bans
  the negative phrasings outright.

  That last one is the reason the rule is written here. The Phase 6 acceptance
  run rendered *"no environmental liens, activity and use limitations (AULs), or
  specialized knowledge ... were reported by the User"* against a user who had
  answered nothing — **and the skill had prescribed that sentence as its
  pending-questionnaire default.** The type system, the request gate and the
  form control all held; the defect entered at the only layer Go does not own.
  The rule governs USER DISCLOSURES only: a completed EDR Environmental Lien /
  AUL search still reports its actual result.

- **Skill exemplars are fabrication vectors.** Template-compiler rule 7 once
  gave `{{USGS_TopoSummary}}` the example wording *"gently slopes to the south
  with surface water runoff directed toward municipal drainage features"*. That
  string was copied **verbatim** into a delivered report for a parcel that
  drains **east** — the example became the finding. To a model filling a slot,
  an example is indistinguishable from a default, and the result reads exactly
  like a real observation.

  So: every exemplar in `.agents/skills/` must either be **marked
  illustrative-only** or use **obviously-placeholder values** (`[Month D,
  YYYY]`, `[Quadrangle]`, `[Year]`). And any **directional or factual site
  claim** — flow direction, slope, distance, date, owner — routes from agent
  output, carrying `[EP VERIFY: ...]` or `[MEG DATAGAP: ...]` where applicable.
  Never from example text.

The header repair itself is in [tools/fixheader](tools/fixheader/main.go), not
`scratch/`: the template is a committed binary, so without a checked-in tool the
change to it is unreviewable. It is idempotent and validates the rewritten file
before replacing the original. Header line 1 and line 2 now position their
right-hand values with a single right-aligned tab stop at 9360 twips (page width
12240 less two 1440 margins) instead of seven tab runs plus ten literal spaces.

## Reading delivered vs generated reports

Two distinctions that have already caused wrong conclusions during
investigation:

- **`tmp/esa_outputs/` holds machine drafts, not delivered copies.** The reports
  that went to clients were hand-finalized afterwards, and those finals are not
  on the dev machine. A defect visible in a draft may have been corrected by
  hand before issuance, and a draft that looks clean proves nothing about what
  shipped. Say which one you inspected, every time.
- **Purchaser ≠ owner.** On Providence Road, **Arkan Homes LLC is the
  purchaser** (and the client — the recipient block is Mr. Ihssan Hashem, Arkan
  Homes LLC, Milton GA), while **the Petes are the property owners**. A report
  naming Arkan as the client and the Petes as owners is correct, not a leak of
  the retired hardcodes. This matters because `Arkan`, `Morningpark` and
  `Hashem` are on the `injectFieldDefaults` tripwire list: the tripwire bans
  them from **Go-side defaults**, not from the report, where they are legitimate
  model-sourced client data.

## Known bugs to fix (in order)
1. ~~main.go feeds raw .docx bytes (zip binary) from historical/ into the
   system prompt as "historical context" — must extract real text first.~~
   DONE. It was raw *PDF* bytes (no extension filter), while cmd/esad
   filtered to .txt/.md/.docx and so ingested nothing at all. Both now call
   `core.LoadHistoricalCorpus`.
2. ~~Historical context only reaches the Template Compiler agent; the ASTM
   Synthesizer (which writes the actual lingo/rationales) never sees it.~~
   DONE. `buildAgents` now loads the corpus before constructing either agent
   and appends the same `corpus.PromptBlock()` to both. Note this attaches
   the baseline twice per generation — see the token cost below.
3. ~~Template Compiler must emit ~280 exact `{{Key}}` JSON keys with no
   validation — missing keys silently become blank fields in the report.~~
   **DONE — Phase 4.** The canonical list is parsed from the template at boot
   (**300 tags**: 299 in the body plus header-only `ReportDate`; 170 slots,
   130 substantive), and `validateAndRepair` fills slot gaps, re-prompts once
   for absent substantive keys, and brackets the rest. Proven on the
   2026-08-12 Providence acceptance run, where the compiler emitted **seven
   invented Section 9.0 key names** and the validator recovered all 12 missing
   keys — without it that run would have shipped a blank Section 9.0.

## Approved, not yet built

Owner-approved work, in phase order. Do not start a phase without an explicit
go-ahead. Show diffs before applying anything touching `mergeDocxLogic`,
`pipeline.go`, or `agent.go`. Work the queue in order, one commit per numbered
item, and stop and report between items.

**PHASE 2 — verified run (gate). PASSED.** Elias confirmed the pipeline
verified end-to-end on the Providence Road run of 2026-08-10; both the
Providence and Beavers reports shipped, hand-finalized. That run is also the
source of the EP-caught errors 6 and 7 recorded above. The gate is cleared —
subsequent phases no longer wait on it.

**PHASE 3 — robustness. DONE** — all five items shipped as specified; see the
historical baselines notes above for the resulting behavior.

The one deviation has since been resolved rather than kept. An
`ESA_ALLOW_PARTIAL_BASELINE` escape hatch was added because Hidden Hills cannot
be extracted at any cache warmth, which made strict mode permanently
unstartable. Hidden Hills has now been moved to `historical_excluded/`, so
coverage is **19/19 and strict mode runs unconditionally in production**. The
variable remains in the code as a dev-only convenience and is **never set on
Cloud Run**. Original spec retained below.

1. Boot preflight for cache coverage — **strict: refuse to start** on a cold or
   partial cache.
2. Never extract inline on the request path — nil extractor in `buildAgents`;
   extraction becomes exclusively a `-warm-historical` operation. Today a cold
   cache means ~18 sequential Vertex transcriptions *inside* one HTTP request,
   which will blow the Cloud Run timeout and present as a generic timeout.
3. Don't memoize failures — the in-process memo currently stores `Failed` too,
   so a transient quota blip drops those baselines for the whole process
   lifetime. Cache successes only; retry failures next call.
4. Atomic cache writes — temp file + rename. Current writes go straight to the
   final path and the read side only checks non-empty, so an interrupted write
   leaves a truncated transcript that is trusted forever.
5. `baseline: {used, total, missing}` in the `/generate` JSON response, plus a
   loud bracketed internal note in the docx when incomplete:
   `[DRAFT NOTE — INTERNAL: generated against N of M historical baselines —
   remove before issuance]`. Explicitly **not** an ASTM data-gap line — it is a
   drafting artifact for the EP to clear, not a regulatory finding.

**PHASE 4 — bug 3, the validator. DONE.** Design in
[docs/phase4-validator-design.md](docs/phase4-validator-design.md) (approved
with amendments after external review). Acceptance run **2026-08-12**,
Providence Road, `Phase_I_ESA_Report_..._20260812_004029.docx`:

- **Zero unreplaced tags, zero fractured survivors.** Baseline 19/19.
- 8.13 Radon, 8.15 LBP and Section 9.0 all carry real content — all three were
  blank or boilerplate-only in the August 5 draft.
- `slots_auto_filled=0, missing_first_pass=12, reprompt_ran=true,
  recovered=12, data_gapped=0, deliberate_blanks=1`.
- **The run is the proof the validator was needed.** The compiler emitted seven
  invented Section 9.0 key names (`Sec9_Item1_Intro`, `Sec9_Item3_Topography`,
  `Sec9_Item9_DeMinimis`, …), none of which exist. Without the re-prompt,
  Section 9.0 ships blank — exactly the delivered defect.
- The splice detector found two live defects the same run: the
  `User_Authorization` restatement (EP-caught error 5, still shipping) and a
  previously unknown *"Fulton County County Tax Assessor"* duplication. Both
  fixed at source afterwards.

Boot now **fails** on either binding drift tier — a rule-heading target or a
JSON-schema key naming a tag the template lacks. Original spec retained below.

- Parse the canonical tag list from `knowledge/ESA_PHASE_I_Template.docx` at
  boot (after unfracturing), **not** from the skill markdown. Log drift vs the
  skill's documented list once at startup.
- Two-tier validation of the Template Compiler JSON before merge:
  (a) missing `UpN_*`/`DownN_*` slot keys auto-fill to `""` — they are table
  slots; (b) missing substantive keys trigger exactly **one** re-prompt listing
  the absent keys; anything still missing becomes `[MEG DATAGAP: <key>]` so it
  is visible in the docx, never silently blank.

**Confirmed target list (from the Providence Road run):** 13 unreplaced tags
plus 2 fractured survivors in header parts. **`Sec9_Item1`–`Sec9_Item7` must be
classed substantive-mandatory** — the compiler skipped the entire Section 9.0
findings enumeration, which is the core of the report. A validator that let
Sec9 keys fall through to `""` would reproduce exactly the delivered defect.
*(Shipped as `Sec9_Item1`–`Sec9_Item8`, the five `SV_*` strings and
`DataGaps_Text` — 14 mandatory keys.)*
- ~~Update template-compiler SKILL.md: correct "160" to ~280, and require
  unused `UpN`/`DownN` slots be emitted as `""`.~~ DONE — the skill now states
  300 / 170 / 130 and names the Go-supplied keys.

**PHASE 5 PREP — corpus inventory.** Read-only, zero model calls. May run any
time after the nil-extractor verification, in parallel with Phase 2. One row per
cached transcript: source filename | report year | ASTM version cited
(`E 1527-13` vs `E 1527-21`) | county | property type (residential / commercial /
institutional / vacant-undeveloped / large tract) | outcome (clean = "revealed no
evidence" vs REC-present = "revealed the following") | approx tokens.

Flag specifically: any **vacant/undeveloped-land** report (highest-relevance
exemplar for Providence Road, and template-compiler SKILL.md references Section
4.2 undeveloped verbiage), any **Fulton County** report, and **every E1527-13
citation**.

Elias picks keeps from the table and deletes the rest from `historical/` by hand
— the Hard rules bar Claude from touching that folder. Deletions only shift the
directory fingerprint; orphaned cache entries are harmless and can be pruned in
the same commit window if asked.

**Branch decision — RESOLVED, no substitute needed, inventory complete.** The
premise was wrong: Homestead (20.1 MB) extracted successfully, as did Cross Keys
(48.8 MB). Nothing in Phase 6 gates Phase 5. The inventory has since run and its
conclusions are folded into the sourcing rules below; Homestead was in fact
demoted from primary exemplar to optional third, on relevance grounds rather
than availability.

Homestead's transcript was spot-checked because it ran near the assumed ceiling:
69.6 KB / 1002 lines, cover page verbatim, 100 numbered section headings,
Sections 9.0/10.0/11.0 all present (these sit near the end of the body and would
be the first casualties of truncation), natural ending at the EP qualifications
list, zero markdown/JSON contamination.

**PHASE 5 — style digest replaces the raw corpus. DONE 2026-08-12.** Both
agents load a role-split digest instead of `corpus.PromptBlock()`:
`knowledge/style_baseline_astm.md` (~13k) and
`knowledge/style_baseline_compiler.md` (~9k), regenerated by
[tools/builddigest](tools/builddigest/main.go) from the mined tables in
[docs/phase5-mined-candidates.md](docs/phase5-mined-candidates.md).

**Per-run pipeline input: 712,959 → 76,955 est. tokens, an 89% cut**, with
validator outcome parity (`data_gapped=2`, same two keys across three runs),
zero unreplaced tags, and 13/13 parser files. Sections 9 and 10 now name all
three Providence parcels where the pre-digest baseline named one.

**Two cautions carried forward.** Exemplar directions and spot elevations are
masked to `⟨DIRECTION⟩`/`⟨ELEV⟩` because the unmasked digest was *teaching* the
gradient defect — a run asserted a flow direction matching the exemplars' rather
than the site's. And output varies run to run on identical inputs
(`deliberate_blanks` 1→4→3, owner named vs bracketed), so no single run proves
anything about prose quality. Full record in the plan doc §11.

Plan and rulings in
[docs/phase5-style-digest-plan.md](docs/phase5-style-digest-plan.md).

**Measured 2026-08-12** (the earlier "~1.2 MB ≈ 275k / ~550k" was an estimate,
and ~20% low): the warmed corpus is **1,316,877 bytes ≈ 329k tokens**, attached
to *two* agents = **≈ 658k tokens/report**. This is no longer a cost question
only — it is the dominant term in the payload of the node that hit
`ResourceExhausted` on 2026-08-12.

**Ruled: a role-split digest, not one shared file.** The two consumers need
different things, so each gets its own ~20–25k file — **≈ 45k per run, a 93%
cut** — with one exemplar each: 1080 Moreland (REC-present, reasoning-shaped)
to the ASTM Synthesizer, Old Field Road (clean/vacant, formatting-shaped) to
the Template Compiler. Homestead is dropped; a third exemplar fits no budget
without eating the distilled tiers, which are the point. Both agents load their
digest instead of `corpus.PromptBlock()`; the raw corpus stops shipping in
prompts entirely.

**Exemplars are pseudonymized before assembly** — client and owner names, site
addresses, parcel IDs and project numbers replaced with typed placeholders.
They are full client reports appended to *every* future prompt, so they are the
largest leak vector in the phase; placeholders keep the voice, kill the vector,
let the contamination scan run over the whole digest with no exemptions, and
remove the mimicry temptation that `gently slopes to the south` already
demonstrated once.

**Sourcing rules — settled by the corpus inventory, not to be re-derived:**

- **Tier 1 sources from 9 independent E1527-21 reports, not 19.** Eight of the
  19 transcripts are excluded from digest sourcing entirely: the seven citing
  superseded **E1527-13** (304 Creighton, 318-328 3rd Ave, Cross Keys, Hilton
  Garden Inn, Regalwoods, 2333 Defoor Hills, Colham Ferry) plus **HIES 2014**,
  which cites neither version and uses pre-convention phrasing ("revealed the
  following *information*").
- **Versioned pairs — keep the later half only.** ELC/ELC-R1 share 77% of
  distinct lines and Rockdale Jan/Feb share 59%; they are draft/revision pairs
  of one project, not independent reports. Keep **Rockdale Feb 2025** and
  **ELC R1**; exclude the earlier halves from Tier 1 diffing so draft/revision
  overlap cannot masquerade as house style.

- **Tier 1 — canonical blocks, stated once each.** Every passage
  verbatim-identical across the kept corpus. Known so far: Section 9.0 opener,
  transmittal closing ("...looks forward to our continued association..."),
  §3.2.21 data-gap definition, running header/footer formats,
  questionnaire-pending default, radon county formula. **Verify identity by
  diffing across transcripts — do not assume.**
- **Tier 2 — formulas with variant families.** Section 10 Opinions opener plus
  its clean-closing variants and the enumerated REC-present format; Section 9
  numbered-finding ordering (1 = location/parcel/owner, 2 = topography, then
  history, then regulatory); Section 5.1 aerial grouping style (year-ranges
  sharing one description).
- **Plus 2–3 full exemplar transcripts appended whole.** Picks upgraded for
  relevance to Providence Road (an undeveloped parcel), replacing Homestead as
  the primary pair:
  1. **Old Field Road** — clean outcome, **vacant/undeveloped** subject
     property, E1527-21, Bartow County. The only transcript that is all three,
     and the closest match to Providence Road. Template-compiler SKILL.md
     rule 2 points at Section 4.2 undeveloped-land verbiage, which this
     supplies.
  2. **1080 Moreland FINAL** — **REC-present**, vacant/heavily wooded,
     E1527-21.
  3. Optional third for a developed-parcel voice: **Homestead** (residential
     small-parcel) or **Rockdale Feb 2025** (institutional, REC-present).
- Exclusions are enumerated in the sourcing rules above (7 × E1527-13 + HIES
  2014 + the 2 earlier halves of the versioned pairs).
- Add one guardrail line to both consuming skills: *"Baseline reports may cite
  older ASTM versions; always cite E 1527-21 regardless of baseline phrasing."*
- ~~Target **60–80k tokens** total.~~ Superseded: **~20–25k per file, ~45k per
  run** under the role-split ruling. The 60–80k figure was never load-bearing
  precision — the operational goal is per-run tokens far below quota pressure.
  Report the actual token count and what went into each tier, then **STOP for
  human review before wiring the agents** — the digest is a reviewable style
  asset, not just a prompt. That stop is a hard gate.
- **Mining is deterministic, not model-driven** ([tools/minecorpus](tools/minecorpus/main.go)).
  Text appearing verbatim in nine reports is *found by diffing*, with
  provenance; a model asked to "find the common passages" would be expensive,
  unverifiable, and free to invent a canonical block nobody wrote. Assembly
  composes from the mined tables, never from the raw corpus.

**PHASE 6 — backlog (each needs its own go-ahead).**
- **Compiler prose cleanup — splices that are still shipping.** Found on both
  Phase 6 acceptance runs and unchanged by that work, so they are compiler
  wording, not plumbing. The Section 10 opener renders *"...and the limitations
  discussed herein, This assessment has revealed evidence of..."*; four literal
  doubled periods appear in the body; and `TP_Databases` renders *"the target
  property was Not applicable. in The subject property was not identified ...
  searched.. Not applicable."* The splice detector flags all of them and is
  detection-only by design — **every signal was verified against the rendered
  text, not trusted from the log**, and all were real.
- **Bracket provenance — the validator cannot tell its own brackets from the
  model's.** Acceptance run 1 rendered `[MEG DATAGAP: INSERT OWNER NAME]`: a
  fill-in-the-blank instruction to a typist, wearing data-gap vocabulary the
  model has learned to imitate. Visible, so not dangerous today, but it means a
  bracket count no longer distinguishes "Go recorded a gap" from "the model
  wrote something bracket-shaped". Tag validator-emitted brackets distinctly, or
  reject model-composed ones.
- **Owner-name robustness — one more evidence entry.** The same run produced
  that `INSERT OWNER NAME` bracket where earlier runs on identical inputs
  produced `Diane Pete & Brian J Pete`. The owner is in the material; the
  extraction depends on luck. See the existing backlog item below.
- **Report title/descriptor in header line 2.** The stamped format is
  `[Site Address] - [Project Descriptor]`, but the template has no descriptor
  tag and nothing sources one, so the line is address-only today — which is the
  correct fallback under the house convention, not a defect. Build it as one
  unit when Phase 6 lands: an **optional intake field**, prefilled from proposal
  extraction where available (needs a new parser key for the proposal title),
  printed after the address with a `" - "` separator and **omitted entirely when
  blank**. Do not add the template tag before the plumbing — a tag with no
  source is a mechanism that can only ever resolve to the fallback, which looks
  like a feature and behaves like nothing.
- **Dynamic prescreen. DONE 2026-08-13** — accepted on two runs; full record in
  [docs/phase6-prescreen-plan.md](docs/phase6-prescreen-plan.md) §7.

  What shipped: the **intake contract** (`cmd/api/intake.go`, integer
  additive-only `schema_version`, unknown versions refused loudly), a static
  core of questions no document can answer, **absence-triggered** site-visit
  questions with an EP category override, `User_Authorization` composed in Go,
  and the labelled 40 CFR 312 user-knowledge block. Per-run evidence:
  authorization rendered as a composed sentence with a filled intake and as
  `[MEG DATAGAP: authorization]` with an empty one **while the proposal sat
  parsed in the uploads** — non-inference, demonstrated rather than asserted.

  Content-level gap analysis was deliberately **deferred, behind the same
  schema**, because a second model surface that can hallucinate a gap list into
  confidently wrong questions is a new fabrication surface. The deferral is
  evidence-gated on two logged signals, and the case file is plan §8. **The
  first signal has already been observed:** run 1 bracketed `SV_AccessFrom` and
  `SV_AccessVia` while the recon checklist was present, parsed and correctly
  suppressing those very questions — the one shape absence-detection cannot
  catch.

  Original spec retained below.

  Replace the hardcoded 4 questions with real data-gap
  questions derived from parsing uploads at prescreen time; cache those parse
  results and reuse in `/generate` so parsing isn't paid twice. Remove the
  pre-filled fake answers — parcel `10-123-456` must never be a default. Add a
  free-text "Other / additional information" question wired into the answers
  flow, labeled in the payload as user-provided **actual knowledge** so the ASTM
  skill's Actual Knowledge Override rule picks it up.

  **Design principle — what may be asked.** A question is legitimate only if it
  asks for either:
  (a) a fact that exists solely in the user's head — authorization, client
  entity spelling, parcel IDs; or
  (b) a document-derived fact whose **source document is absent** from the
  uploads — e.g. surface `SV_*` site-visit questions ONLY when no recon
  checklist was detected.

  **Never ask for data that is sitting in an uploaded document.** Doing so
  **launders an extraction failure into a green run**: the EP fills the field,
  the report looks complete, and the parser bug survives to the next project
  where nobody happens to notice. The question hides exactly the defect it
  appears to solve.

  **Every dynamic question carries a source-of-truth note in its `Context`
  field**, so the EP knows why it is being asked — "No site recon checklist
  found in uploads" vs "Value conflicts between the proposal and the EDR
  package". Without it the EP cannot judge whether answering is appropriate or
  whether something upstream is broken.

  **Permanent structured question — authorization.** "How was this work
  authorized?" with options [Signed Proposal / Purchase Order / Other] plus the
  relevant date field(s). The `User_Authorization` sentence is then **composed
  in Go** per template-compiler rule 6 and injected via `injectFieldDefaults` —
  deterministic, never model-dependent. This kills both observed defects on that
  field at once: the blank value, and the splice duplication ("This work was
  performed in accordance with Matrix Engineering Group was authorized under
  signed proposal dated July 06, 2026..").

- **`SV_*` blanking is NOT a prescreen problem** — it belongs to the Phase 4
  validator plus the SiteRecon handoff. Do not paper over it with questions.

  **SiteRecon's 520 tokens on the Providence Road run are probably normal.**
  Docx evidence closed this, and it is a good example of why artifact
  inspection beats reasoning from token counts:
  - `Sec8.1`, `8.2`, `8.4`–`8.10`, `8.13` were all **filled with real
    site-visit content** — so the compiler consumed most of SiteRecon's output.
    A full handoff-drop is ruled out.

    > **CORRECTION, 2026-08-12 (Claude Code).** `Sec8_13` was **not** filled.
    > Re-inspection of the same artifact
    > (`Phase_I_ESA_Report_..._20260805_022316.docx`) shows Section 8.13 Radon
    > completely empty — the "Radon" heading sits directly against "Asbestos
    > Containing Materials", with no zone and no pCi/L figure anywhere in the
    > document. `Sec8_15` likewise carried only static boilerplate.
    >
    > The cause was found later: template-compiler rule 6 specified the radon
    > wording for `{{Radon_Summary}}`, a tag the template does not contain, and
    > the dead key sat in the JSON schema beside its live twin
    > `{{Sec8_13_Radon}}` — so the model could fill either, and the discard was
    > **nondeterministic**. Fixed in `1428cb9` and `ca08ead`.
    >
    > The original conclusion above is left standing because it was reasonable
    > on the evidence then to hand, and the reasoning it records is still the
    > right method. What it got wrong is the fill status of one section, not
    > the argument. The wider point survives intact: a full handoff-drop *is*
    > ruled out, since 8.1–8.10 really were populated.
  - The two `Sec8` blanks were **fractured-tag survivors**, addressed by the
    un-fracture fix in `45c2446`.
  - The `SV_*` blanks are **Section 3.1 parser-extracted strings, not SiteRecon
    products** — a compiler key-skip, i.e. bug 3's shape, already covered by the
    Phase 4 validator.
  - `Site_Recon_Checklist_Providence Road Properties.pdf` *was* in the uploads,
    so no missing-input explanation either.

  520 tokens appears near-normal for this node's narrow contract. Still worth
  reading its `NODE ENGAGED` / `NODE YIELD` pair on the Beavers run to confirm.

  One genuine latent issue remains: SiteRecon's skill claims it "receives the
  raw JSON checklist data from the Parser Agent", but `Pipeline.Run` hands it
  the entire accumulated payload — every parser extract plus the Geospatial
  output. The skill's stated contract and the pipeline's real behavior do not
  match, independent of the token question.
- ~~`/download`: add `enforceDomainAuth` + sanitize the `file` param — it
  currently serves arbitrary paths with no auth.~~ DONE. See the HTTP layer
  notes above.
- Gate or delete `analyzeBucketHandler`'s hardcoded-answers path.
- **GeoCheck gradient as a structured parser field, plus a stated-direction
  match check.** The gradient guard proves a GeoCheck gradient *was extracted*;
  it cannot prove the model's stated direction *matches* it, because the
  gradient arrives as prose. Making it a structured field would let the
  validator compare the two and bracket a mismatch — strictly better, strictly
  harder, and a parser-contract change that deserves its own review rather than
  being folded into the guard.
- **Owner extraction robustness.** On the 2026-08-12 comparison runs the same
  13 source files produced `Diane Pete & Brian J Pete` on one run and
  `[MEG DATAGAP: Current owner not identified in provided documents]` on
  another. The bracket is honest and visible — the system working as designed —
  but the owner *is* in the material. Give the parser explicit emphasis on the
  tax-record / title owner so the extraction stops depending on luck. Not a
  digest problem: it varied across runs with identical prompts.
- **`"a easterly"` — template-owned article before a vowel-initial value.**
  Section 7.3 reads *"inferred to flow in a `{{GWFlowDir}}` direction"*, so any
  value starting with a vowel ("easterly", "eastern") reads ungrammatically.
  Not fixable at merge without touching the template — the article belongs to
  the template, and rewriting values to dodge it would corrupt the data. Fix in
  the next deliberate template revision (`a` → `a(n)`, or reword the lead-in).
  Cosmetic but real, and it appears in a signed deliverable.
- **Hidden Hills: split it, or extract only its report body — then move it back
  into `historical/`.** `5 Hidden Hills Parcels ESA-Phase I Oct 2022.pdf` was
  the sole extraction failure: `InvalidArgument: The document contains 1400
  pages which exceeds the supported page limit of 1000`. The cap is on **page
  count, not bytes** — Cross Keys extracted fine at 48.8 MB. A
  `genai.FileData`/GCS URI would therefore **not** fix it; the page limit
  applies however the document is passed. Only ~20 of the 1400 pages are the
  report proper; the rest is EDR appendix printouts, which the extractor skill
  would discard anyway. It now sits in `historical_excluded/` so strict
  preflight can pass; **this backlog item is its return condition.**
- ~~Retry loop: classify errors by `googleapi` status, retry only retryables,
  then restore `maxRetries` to a sane value.~~ DONE — `internal/core/retry.go`
  classifies gRPC codes, HTTP statuses and transport errors into
  permanent / throttled (429, 30-60-90s) / transient (5-10-20s); `maxRetries`
  is back to 3. Unrecognized errors retry as transient and are logged
  `unclassified` so recurring shapes can be promoted.
- Consolidate the two docx merge implementations (api vs esad). **Partially
  done:** value sanitization and post-merge validation are now shared via
  `internal/core/docxsafe.go`. What still differs is the matching strategy —
  the API's exact `{{Key}}` replacement after un-fracturing, versus the CLI's
  per-character `replaceFracturedXML` regex. Pick one and delete the other.
- Fix the `a.Cfg.Name[:4]` TODO properly.