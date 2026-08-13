# Phase 6 — Dynamic Prescreen

**DONE — accepted 2026-08-13 on two acceptance runs. See §7.**

**Reviewed. All six §6 questions ruled — see the `[RULED]` annotations.**
Rulings are authoritative; where the analysis above them differs, the ruling
wins.

Author: Claude Code · Plan written at `b3641b3`, rulings folded in at `7a06e22`
· Findings are read from the code, not from CLAUDE.md's memory of it.

**Build order (ruled):** kill commit → schema + static core + Go-supplied wiring
(county, authorization composition) + labelled user-knowledge block →
absence-triggered `site_visit` questions + categorization override → acceptance
on **two** runs: a filled intake (`User_Authorization` composed, `SiteCounty`
supplied, §4 content present *with attribution*) and an empty intake (brackets
everywhere, zero defaults, zero inference).

---

## 1. Current state — what IS

### 1.1 The form

Four questions, defined in `prescreenQuestions()` (`cmd/api/main.go:839`), a
fixed list returned by `/api/v1/prescreen` regardless of what was uploaded.

| id | question | type | feeds |
|---|---|---|---|
| `parcel_id` | Tax Parcel ID | text | **`{{ParcelID}}`** via `injectFieldDefaults` |
| `site_acreage` | Total site acreage | text | **`{{SiteAcres}}`** via `injectFieldDefaults` |
| `client_spelling` | Client entity name for the recipient block | text | model context only |
| `site_recon_ast_ust` | AST/UST observed during the site visit | select | model context only |

All four now ship `Answer: ""` — the Phase 4 purge. Options on the AST/UST
question survive, which is correct: the option list is a legitimate answer
vocabulary; only the pre-*selection* was the defect.

### 1.2 What the handler actually computes

`prescreenHandler` (`:722`) downloads the project's files (local `tmp/` first,
GCS fallback) and categorises them **by filename substring** — `proposal`,
`edr|aerial|topo|sanborn|radius`, `filio|photo`, `recon|checklist`, `wetland`,
`firm|flood`, `vec`, `questionnaire`. That categorisation is returned as
`detected_files` and is **the only thing about the request that is dynamic
today.** The questions are not derived from it.

**No parsing happens at prescreen time.** No model call, no content inspection —
filenames only.

### 1.3 How answers travel — two distinct paths

```
POST /api/v1/generate { answers: {...} }
        │
        ├─► fullExtractedData += "=== [EP PRE-SCREENING ANSWERS & CORRECTIONS] ===" + JSON
        │       (main.go:1135)  → every answer, verbatim, as MODEL CONTEXT
        │
        └─► injectFieldDefaults(payload, answers, draftNote)
                (main.go:940-941, :918) → only THREE keys are Go-supplied:
                  project_number → {{ProjectNo}}
                  parcel_id      → {{ParcelID}}   (via prescreenValue)
                  site_acreage   → {{SiteAcres}}  (via prescreenValue)
```

So: **every answer reaches the model; only three reach a tag deterministically.**
`client_spelling` and `site_recon_ast_ust` are context the model may or may not
use — nothing guarantees they land anywhere.

`project_number` is consumed by the backend (`{{ProjectNo}}`, and the appendix
package at `:1143`) but **is not one of the four questions** — the UI supplies it
separately. It is an intake field in everything but name.

`special_instructions` is a separate top-level field, appended as
`=== [SPECIAL EP DRAFT INSTRUCTIONS] ===`. Free text, model context only.

### 1.4 What survived the Phase 4 purge — the purge was backend-only

**The web form still seeds the placeholders client-side** (`web/index.html:847`):

```js
const ansObj = {
    project_number: activeProjectNo,
    client_spelling: activeClient,     // defaults to "Arkan Homes, LLC"  (:648)
    parcel_id: '10-123-456',
    site_acreage: '1.7 Acres'
};
```

and only overwrites them from inputs the EP actually filled (`if (!val) return`).
Lines 864 and 869 then compare typed values *against those literals* in a
heuristic that scrapes parcel IDs and acreages out of free-text answers.

Consequences, precisely:

- `parcel_id` / `site_acreage`: a blank field sends the demo string. **Go
  catches it** — `placeholderPrescreenAnswers` rejects both literals and writes
  `[MEG DATAGAP: …]`. The defence holds, but it is the *second* line, and it is
  doing work the form should never have created.
- `client_spelling`: defaults to **`"Arkan Homes, LLC"`** and is **not
  guarded**, because placeholder-by-value rejection was deliberately narrowed to
  the tag-writing fields (blacklisting a real client name would refuse a
  legitimate answer). It reaches the model as context on **every project**. This
  is the pinning defect CLAUDE.md's tripwire exists to prevent, alive in the
  frontend.
- Further hardcoded client defaults: `activeProject = "Beavers_Road_Property"`
  (`:646`), the same as a fallback in `listProjectsHandler` (`:598`),
  `prescreenHandler` (`:735`) and `analyzeBucketHandler` (`:1434`).

**Finding: the placeholder lesson was applied to Go and not to the client.** Any
Phase 6 work must close this, and the schema below is designed so that a client
which sends nothing produces brackets rather than defaults.

---

## 2. The architectural fork

"Gap-driven" implies knowing the gaps, which implies analysis before questions.
Three shapes, with costs.

### (a) Two-step flow — parse, analyse gaps, then ask

`upload → cheap parse/gap pass → questions from real gaps → answers → generate`

| | |
|---|---|
| **Cost** | A second pass over the documents. The parser stage is ~3.0M est. tokens for Providence's 13 files — the dominant cost in a run. A *cheap* pass means a smaller model or a subset of files, not the full parser. |
| **Latency** | Prescreen becomes a minutes-long operation. Today it is sub-second (filenames only). The EP waits at upload instead of at generate. |
| **UX** | Best possible: every question is one the system genuinely could not answer. No busywork. |
| **Failure modes** | The gap pass is a *second* model surface that can hallucinate a gap that isn't one, or miss one that is. A wrong gap list produces confidently wrong questions. Cache invalidation: if the EP uploads another file after answering, the gap analysis is stale. |
| **Reuse** | CLAUDE.md's standing note — cache the parse and reuse it in `/generate` so parsing isn't paid twice — is what makes this affordable. That is a real engineering commitment: a parse cache keyed on file content, with the same invalidation discipline as `historical/.cache`. |

### (b) Static but honest — a fixed set redesigned around what documents can't supply

| | |
|---|---|
| **Cost** | Near zero. No second pass. |
| **Latency** | Unchanged, sub-second. |
| **UX** | Some questions will be unnecessary on some projects (asking for a parcel ID the EDR package happens to contain). Mild busywork, no wrong answers. |
| **Failure modes** | The *design principle already in CLAUDE.md* bounds this: never ask for data sitting in an uploaded document, because answering it "launders an extraction failure into a green run" — the EP fills the field, the report looks complete, and the parser bug survives. A static set violates that principle **the moment a document does contain the answer**. |
| **Honest limit** | It cannot be gap-*driven*. It can only be gap-*shaped*. |

### (c) Hybrid — static core + gap-driven supplement

| | |
|---|---|
| **Cost** | The supplement's analysis only, and it can be scoped to cheap signals. |
| **Latency** | Static core renders instantly; supplement can stream in or arrive on a second screen. |
| **UX** | Good. The EP always sees the questions only they can answer, plus anything the documents failed to supply. |
| **Failure modes** | Two code paths and two question sources to keep coherent; the answer schema must accommodate questions that did not exist when the form was designed. |

### Recommendation: **(c), built in the order (b) → (c)**

The static core is not a compromise — it is the set CLAUDE.md's design principle
*licenses*: facts that exist only in the user's head. Authorization, client
entity spelling, the report descriptor, and user actual-knowledge obligations
under 40 CFR 312 are **never** derivable from an EDR package, on any project.
They are legitimately static.

The gap-driven supplement then covers exactly one category — *(b) in the standing
definition:* a document-derived fact whose **source document is absent**. That is
detectable **without a model**, from the `detected_files` categorisation that
already exists: no `Site Recon Checklist` category → ask the `SV_*` questions; no
`Proposal / Contract` → ask for authorization details; no `User Questionnaire` →
ask the §4 obligation questions.

**That gets most of the value of (a) for none of its cost**, because the
expensive part of (a) — parsing content to find gaps — is only needed for the
harder question "the document is present but the field is missing from it". I
would defer that until the cheap version is in place and we can see whether it
is actually the common case. My suspicion, from the Providence runs, is that
**absent documents explain most real gaps** (the `SV_AccessFrom`/`SV_AccessVia`
brackets recur precisely because checklist fields were thin), but that is a
suspicion and the two-step flow can be added later behind the same schema.

Every question, static or supplemented, carries the mandated `Context` note
saying **why it is being asked** — "No site recon checklist found in uploads" vs
"Value conflicts between the proposal and the EDR package" — so the EP can judge
whether answering is appropriate or whether something upstream is broken.

---

## 3. Answer schema — the intake contract

Designed as the API contract first; the web form is its first client, Report
Studio its second, the site-recon checklist v2 its third.

```jsonc
{
  "schema_version": 1,
  "project": { "name": "Properties_at_Providence_Road", "number": "303315" },

  "authorization": {                       // §2 — structured, replaces inference
    "basis": "signed_proposal",            // signed_proposal | purchase_order | other
    "proposal_date": "2026-07-06",         // ISO 8601
    "approval_date": "2026-07-06",
    "po_number": null,
    "po_recipient": null,
    "po_date": null,
    "other_description": null
  },

  "client": {
    "entity_name": "Arkan Homes, LLC",     // exact legal spelling for the block
    "contact_name": "Mr. Ihssan Hashem",
    "address_lines": ["12690 Morningpark Cir.", "Milton, GA 30004"]
  },

  "site": {
    "parcel_ids": ["22-4640-1106-064-9"],  // ARRAY — multi-parcel is common
    "acreage": "3.84",                     // bare number; template supplies "acres"
    "county": "Fulton",                    // bare name; template supplies "County"
    "descriptor": "Residential Redevelopment"   // header line 2, optional
  },

  "user_knowledge": {                      // §4 / 40 CFR 312 user obligations
    "environmental_liens": { "known": false, "detail": null },
    "aul_known":           { "known": false, "detail": null },
    "specialized_knowledge": { "known": true,
      "detail": "Prior owner operated a small engine repair shop in the rear shed." },
    "other": "Free text. Anything the EP knows that the documents do not say."
  },

  "site_visit": {                          // ONLY asked when no checklist uploaded
    "access_from": null,
    "access_via": null,
    "current_use": null,
    "ast_ust_observed": null               // vocabulary as today
  },

  "special_instructions": "…"              // unchanged, model context
}
```

### Answer semantics — which are Go-guaranteed

Go-supplied-means-Go-guarantees applies. Absent answers produce **brackets,
never defaults.**

| Field | Disposition | Rationale |
|---|---|---|
| `project.number` | **goSuppliedKeys** → `{{ProjectNo}}` | already is |
| `site.parcel_ids` | **goSuppliedKeys** → `{{ParcelID}}` | already is; **array** joins per the multi-parcel rule |
| `site.acreage` | **goSuppliedKeys** → `{{SiteAcres}}` | already is; fragment contract strips units |
| `site.county` | **goSuppliedKeys** → `{{SiteCounty}}` | **new.** Today the model supplies it and got "Fulton County County"; a Go-side normalizer already exists |
| `authorization.*` | **goSuppliedKeys** → `{{User_Authorization}}` composed in Go | the standing ruling: deterministic, never model-dependent. Kills both observed defects — the blank value and the splice |
| `site.descriptor` | **goSuppliedKeys** → header line 2 | tag and plumbing land together |
| `client.*` | **model context**, and candidate for `Proposal_To*` | the 60-char audit and rule 3 govern the block; making these Go-supplied is a bigger change than it looks — see question 3 |
| `user_knowledge.*` | **model context, attributed** | must land in §4 *with attribution*, never silently dropped — see §4 below |
| `site_visit.*` | **model context** | `SV_*` are substantive-mandatory in the validator; if unanswered and no checklist, they bracket, which is correct |

Every Go-supplied field gains an entry in `goSuppliedKeys`, and the existing
divergence test enforces that `injectFieldDefaults` writes exactly that set —
the check that already caught four dead key spellings.

---

## 4. User actual knowledge — routing and attribution

The ASTM skill has an **Actual Knowledge Override** rule; the payload must label
this material so the rule fires. Two requirements the design must not fudge:

1. **Attribution.** §4 content derived from `user_knowledge` must be
   attributable to the user, not presented as Matrix's own finding. A lien the
   EP disclosed and a lien Matrix discovered are different claims in a signed
   report.
2. **Never silently dropped.** `known: true` with detail must reach §4 or
   produce a bracket. A disclosed AUL that vanishes is the worst failure this
   phase could introduce — it is user-supplied evidence of a condition.

Proposed: a dedicated `=== [USER ACTUAL KNOWLEDGE — 40 CFR 312 USER OBLIGATIONS] ===`
block in the payload, distinct from the general answers block, so the skill can
be pointed at it by name rather than hoping the model notices.

---

## 5. What Phase 6 must also fix (found, not scoped in the brief) — DONE in `7a06e22`

- **Client-side placeholder defaults** (§1.4). `client_spelling` defaulting to a
  named client is live and unguarded.
- **`Beavers_Road_Property` fallbacks** in four places across Go and the UI.
- **The free-text scraping heuristic** (`index.html:860-869`) that guesses parcel
  IDs and acreages out of arbitrary answers. With a structured schema it becomes
  unnecessary; while it exists it can silently overwrite a correct value.

---

## 6. Questions for the reviewer — all six RULED

1. **Fork.** Accept (c) built (b)→(c) — static core plus *document-absence*
   supplement, deferring content-level gap analysis until we see whether it is
   needed? Or commit to the full two-step flow now?

   **[RULED — accepted: (c), built (b)→(c). Content-level gap analysis
   DEFERRED, but deferred *with instrumentation*.]** The reasoning given for
   deferral aligns with the evidence — the recurring
   `SV_AccessFrom`/`SV_AccessVia` brackets *are* absent-checklist gaps — and
   option (a)'s failure mode is decisive: a second model surface that can
   hallucinate a gap list into confidently wrong questions is **a new
   fabrication surface, opened in the week we spent closing fabrication
   surfaces.**

   The deferral is not indefinite; it is evidence-gated. Two log lines decide
   when the two-step flow gets built, behind this same schema:
   - log whenever an **EP-filled field later conflicts with parsed content**
     (the EP answered something the documents also said, differently), and
   - log whenever a **DATAGAP occurs despite the relevant document being
     present** (the extraction failed on material we hold).

   The first says the static core is asking for things it shouldn't; the second
   says content-level analysis would have caught something absence-detection
   cannot. Neither is a guess.

2. **Authorization composition.** Confirm the sentence shape, given it is a
   fragment continuing *"This work was performed in accordance with "*.

   **[RULED — composed in Go, and the fragment contract is *derived from the
   template's actual surrounding text*: found, not assumed.]** Dates convert
   ISO → house format (`Month D, YYYY`). `basis: other` uses
   `other_description` **verbatim** — the EP owns that wording. An unanswered
   authorization produces `[MEG DATAGAP: authorization]` and is **never
   inferred from the uploaded proposal**: this field exists precisely to
   replace that inference, so falling back to it would defeat the field.

3. **Client block.** Should `client.*` become Go-supplied into the
   `Proposal_To*` / `Proposal_Letter*` tags?

   **[RULED — instinct ratified: its OWN ITEM, after the Phase 6 core.]** A
   deterministic recipient block is right eventually — it shipped **empty**
   once, and it is header-class content — but the 60-character audit plus Go
   owning line assignment is a small layout engine and gets its own design
   pass. **V1:** `client.*` stays model context, now improved by the exact
   legal spelling arriving from intake.

4. **Schema versioning.** Integer additive-only, or semver?

   **[RULED — integer, additive-only.]** Within a version, fields are never
   removed or repurposed, only added. A breaking change is a new integer. The
   server accepts known versions **only** and **rejects unknown versions
   loudly** — never best-effort parses. Define that behaviour *now*, before
   Report Studio binds to it. Semver is ceremony for a contract with three
   known clients.

5. **Site-visit questions.** Confirm the filename-based trigger.

   **[RULED — filename-based confirmed, with one addition that fixes its
   imperfection without a model call: a human categorization override in the
   UI.]** The `detected_files` list is shown with its categories, and the EP
   can re-tag any file ("this IS the Site Recon Checklist"). The
   oddly-named-checklist case then costs the EP **one click instead of five
   redundant questions**, the trigger logic stays deterministic, and every
   question still carries its `Context` note so the EP can *see* why it is
   being asked and correct upstream instead of answering around the problem.

6. **Multi-parcel UI.** Repeatable inputs now, or comma-separated for v1?

   **[RULED — comma-separated text is fine for v1, with `parcel_ids` as the
   array wire format.]** The client splits on comma and trims. Each parcel is
   **verbatim thereafter** per the house rule — spaces, dots and dashes
   preserved, no format enforcement.

### Ratified without change

- The dedicated labelled §4 block (`USER ACTUAL KNOWLEDGE — 40 CFR 312`) with
  attribution and never-silently-dropped semantics. A `known: true` disclosure
  either reaches §4 or produces a bracket — **a vanished disclosed AUL is
  correctly named the worst failure this phase could introduce.**
- The per-question `Context` notes.
- `site.county` as new Go-supplied.
- `parcel_ids` as an array.

### Kill commit — extended, and shipped

The kill was **extended** at review to include the free-text scraping heuristic
(`index.html:860-869`): anything that can silently overwrite a correct EP-typed
value is placeholder-law-adjacent and dies with the seeds and the fallbacks.
Shipped as `7a06e22`.

---

## 7. Acceptance — 2026-08-13, ACCEPTED

Two runs on `Properties_at_Providence_Road` (13 files, checklist present),
baselines 19/19, Vertex `global`. Run on port 8099 because an `api.exe` built
the previous afternoon held 8080 — **acceptance must never be run against a
binary older than the change under test.**

### Pre-flight — question set, no model calls

| check | result |
|---|---|
| Checklist present → site-visit questions | **0** (16 static-core only) |
| Checklist re-tagged to "Other Document" → questions | **5 appear** (21 total) |
| Every dynamic question states its trigger and the fix | yes |
| Category vocabulary served by the API | 9, all categorizer outputs |

### Run 1 — filled intake · `..._20260813_170112.docx`

`data_gapped=[SV_AccessFrom SV_AccessVia] · deliberate_blanks=4 · recovered=3 ·
unknown_keys=0 · 0 unreplaced tags`

- `User_Authorization` composed: *"This work was performed in accordance with
  our proposal dated July 6, 2026."* — both occurrences, no bracket, no doubled
  period, no restated lead-in. **The splice defect of EP-caught errors 2 and 5
  is closed on this field.**
- `SiteCounty` from intake, no doubled "County". Parcel ID and acreage from
  intake.
- §4 carried the disclosure **with attribution**: *"The user, Arkan Homes, LLC,
  communicated specialized knowledge regarding the property, stating that a
  prior owner operated a small engine repair shop in a rear shed."* The
  attribution survived downstream into Section 10, which framed the resulting
  REC as *"Based on user-provided actual knowledge"*. (That disclosure was
  synthetic test input and did not leak into run 2.)

### Run 2 — empty intake · `..._20260813_171323.docx`

`data_gapped=0 · deliberate_blanks=4 · recovered=5 · 0 unreplaced tags`

- `[MEG DATAGAP: authorization]` ×2 **while the proposal PDF was in the uploads
  and had been parsed** — the bracket is the proof of non-inference, which is
  the entire reason the field exists.
- Parcel ID and acreage bracketed. Zero old placeholders (`10-123-456`,
  `1.7 Acres`, `Beavers_Road`: all absent).
- Client mailing address appeared only in the recipient block; the subject
  property stayed Providence Road. EP-caught error 4 did not recur.

### Findings and their disposition

1. **A §4 sentence still read as a negative disclosure** — *"no environmental
   liens, activity and use limitations (AULs), or specialized knowledge ... were
   reported by the User"* — with all three obligations `NOT ANSWERED`. Root
   cause was not a model slip: **template-compiler rule 2 prescribed that exact
   sentence as the pending-questionnaire default.** The skill's own example
   became a fabricated legal disclosure, the exemplar failure mode already
   recorded in CLAUDE.md. **Fixed in the close-out commit**: absence of an answer
   is never an answer of absence, with the banned phrasings enumerated, the
   user-knowledge block declared authoritative over the default, and an explicit
   carve-out so a real EDR lien-search result is still reportable.
2. **`SV_AccessFrom`/`SV_AccessVia` bracketed in run 1 with the checklist
   present** — see §8.
3. **`ProjectNo` was classified Go-supplied but behaved as an override.** With
   an empty intake it fell through to the model's value and produced `303315`,
   read out of the uploaded proposal — correct by luck, and the same class as
   the random `MEG-` generator the kill commit removed. **Ruled and fixed**:
   strictly Go-supplied, model fallback removed, absent →
   `[MEG DATAGAP: project number]`.

### Out of scope, still live (backlog)

The splice detector fired 5× and every signal was verified against the rendered
text rather than trusted from the log. Four are real doubled periods; Section 10
opens *"...and the limitations discussed herein, This assessment has revealed
evidence of..."*, and `TP_Databases` renders *"the target property was Not
applicable. in The subject property was not identified ... searched.. Not
applicable."* Pre-existing, present in both runs, unchanged by Phase 6.

Run 1 also rendered `[MEG DATAGAP: INSERT OWNER NAME]` — a fill-in-the-blank
instruction wearing a data-gap bracket, composed by the model rather than the
validator.

## 8. Q1 instrumentation — case file for the deferred gap analysis

Q1 deferred content-level gap analysis **with instrumentation**: the deferral
ends when the evidence says absence-detection is insufficient. This is the first
entry.

**Signal type: DATAGAP despite the relevant document being present.**
Acceptance run 1, 2026-08-13. `Site_Recon_Checklist_Providence Road
Properties.pdf` was uploaded, categorised correctly, parsed, and suppressed all
five site-visit questions as designed — and `SV_AccessFrom` and `SV_AccessVia`
were still bracketed as data gaps in the document.

This is exactly the case the absence trigger **cannot** catch: the document is
present, so no question is asked, and the field is missing from it anyway. It is
the shape that argues for the two-step flow, and it appeared on the first
observed run. One data point is not a decision — but this is the file it belongs
in, and the second signal type (an EP-filled field conflicting with parsed
content) has not yet been observed.
