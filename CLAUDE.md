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

```powershell
go test ./internal/...               # cache/corpus tests; no credentials needed
go run ./cmd/esad -payload . -project matrix-esa-production -warm-historical
```

### Tests

Coverage is deliberate and narrow — two files, both guarding behavior that previously failed silently. Nothing else is tested.

- `internal/core/historical_test.go` — corpus caching: content-hash keying, invalidation on content change, reuse on rename/duplicate, local formats bypassing the model, and per-file failures being recorded without aborting.
- `cmd/api/merge_test.go` — tag replacement and post-model field injection.

**Do not "clean up" `TestInjectFieldDefaultsCarriesNoClientHardcodes`.** It is a deliberate regression tripwire: it fails the build if the strings `Arkan`, `Morningpark`, `Hashem`, `Gwinnett`, or `Roswell` reappear in `injectFieldDefaults` output. Those hardcodes silently pinned every generated report to one client's recipient block and site address regardless of the actual project, and the shape of that function invites their return. `TestReplaceTagOrderIndependent` is likewise load-bearing — it proves colliding keys (`ParcelID` vs `SiteParcelID`) converge on the same document regardless of Go's randomized map order.

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

- The cache key is a **hash of file contents**, not the name: editing or replacing a report re-extracts it, while renaming or duplicating one reuses the existing text.
- `.txt`, `.md`, and `.docx` are decoded in-process (`core.ExtractDocxText`) and never cost an API call. Only PDFs and images go to the model.
- An in-process memo keyed on a directory fingerprint (names, sizes, modtimes) stops a long-running server re-reading the cache each request, while still noticing new files.
- Warm the cache offline with `go run ./cmd/esad -payload . -warm-historical`; the Dockerfile's existing `COPY historical/` then carries it into the image, so containers never pay extraction cost on a customer request. There is no `.dockerignore`, so the cache is included automatically.
- The real extraction limit is **page count, not bytes**: Vertex rejects documents over **1000 pages** (`InvalidArgument`). Byte size has not proven to be a practical constraint — baselines of 48.8 MB and 20.1 MB both extracted fine. Of 20 baselines, 19 extract; only the 1400-page Hidden Hills report fails. Extraction failures land in `corpus.Failed` rather than aborting, whatever the cause.

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
3. Template Compiler must emit **~280** exact `{{Key}}` JSON keys with no
   validation — missing keys silently become blank fields in the report.
   (The long-standing "160" figure was wrong: `docx_tags.txt` holds **272**
   unique tags, of which `Up1-14_*` = 70 and `Down1-20_*` = 100 are table
   slots, leaving ~102 substantive keys.) Need a Go-side
   validate/diff/re-prompt loop before docx injection.

## Approved, not yet built

Owner-approved work, in phase order. Do not start a phase without an explicit
go-ahead. Show diffs before applying anything touching `mergeDocxLogic` or the
pipeline.

**PHASE 2 — verified run (gate).** A real `/generate` for `Providence_Road`
through the web UI. Report baseline used/total, prompt tokens, wall time, the
unreplaced-tag log, and the output docx path. No other code changes until this
passes and the docx is reviewed externally.

**PHASE 3 — robustness (approved as specified).**
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

**PHASE 4 — bug 3, the validator.**
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
- Update template-compiler SKILL.md: correct "160" to ~280, and require unused
  `UpN`/`DownN` slots be emitted as `""` (same trailing-empty convention as the
  Proposal lines).

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

**PHASE 5 — style digest replaces the raw corpus.** The warmed corpus is
~1.2 MB ≈ 275k tokens, attached to *two* agents (~550k tokens/report). Generate
**once** from the kept transcripts into `knowledge/style_baseline.md`. Both
consuming agents (ASTM Synthesizer, Template Compiler) load it instead of
`corpus.PromptBlock()`; the raw corpus stops shipping in prompts entirely.

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
- Target **60–80k tokens** total. Write the file, report its actual token count
  and what went into each tier, then **STOP for human review before wiring the
  agents to it** — the digest is a reviewable style asset, not just a prompt.

**PHASE 6 — backlog (each needs its own go-ahead).**
- Dynamic prescreen: replace the hardcoded 4 questions with real data-gap
  questions derived from parsing uploads at prescreen time; cache those parse
  results and reuse in `/generate` so parsing isn't paid twice. Remove the
  pre-filled fake answers — parcel `10-123-456` must never be a default. Add a
  free-text "Other / additional information" question wired into the answers
  flow, labeled in the payload as user-provided **actual knowledge** so the ASTM
  skill's Actual Knowledge Override rule picks it up.
- `/download`: add `enforceDomainAuth` + sanitize the `file` param
  (`filepath.Base`, restrict to the generate temp dirs) — it currently serves
  arbitrary paths with no auth.
- Gate or delete `analyzeBucketHandler`'s hardcoded-answers path.
- **Hidden Hills: split it, or extract only its report body.**
  `5 Hidden Hills Parcels ESA-Phase I Oct 2022.pdf` is the sole extraction
  failure: `InvalidArgument: The document contains 1400 pages which exceeds the
  supported page limit of 1000`. The cap is on **page count, not bytes** —
  Cross Keys extracted fine at 48.8 MB. A `genai.FileData`/GCS URI would
  therefore **not** fix it; the page limit applies however the document is
  passed. Only ~20 of the 1400 pages are the report proper; the rest is EDR
  appendix printouts, which the extractor skill would discard anyway.
- Retry loop: classify errors by `googleapi` status, retry only retryables, then
  restore `maxRetries` to a sane value.
- Consolidate the two docx merge implementations (api vs esad). **Partially
  done:** value sanitization and post-merge validation are now shared via
  `internal/core/docxsafe.go`. What still differs is the matching strategy —
  the API's exact `{{Key}}` replacement after un-fracturing, versus the CLI's
  per-character `replaceFracturedXML` regex. Pick one and delete the other.
- Fix the `a.Cfg.Name[:4]` TODO properly.