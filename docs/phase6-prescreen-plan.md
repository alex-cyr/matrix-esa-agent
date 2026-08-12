# Phase 6 — Dynamic Prescreen

**Plan for review. Nothing built. No code written.**

Author: Claude Code · Repo state: `b3641b3` · Findings are read from the code,
not from CLAUDE.md's memory of it.

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

## 5. What Phase 6 must also fix (found, not scoped in the brief)

- **Client-side placeholder defaults** (§1.4). `client_spelling` defaulting to a
  named client is live and unguarded.
- **`Beavers_Road_Property` fallbacks** in four places across Go and the UI.
- **The free-text scraping heuristic** (`index.html:860-869`) that guesses parcel
  IDs and acreages out of arbitrary answers. With a structured schema it becomes
  unnecessary; while it exists it can silently overwrite a correct value.

---

## 6. Questions for the reviewer

1. **Fork.** Accept (c) built (b)→(c) — static core plus *document-absence*
   supplement, deferring content-level gap analysis until we see whether it is
   needed? Or commit to the full two-step flow now?
2. **Authorization composition.** The standing ruling says compose
   `User_Authorization` in Go. Confirm the sentence shape, given it is a
   fragment continuing *"This work was performed in accordance with "* — e.g.
   `our proposal dated <date> and approved on <date>` / `Purchase Order <no>,
   emailed to <name> on <date>`.
3. **Client block.** Should `client.*` become Go-supplied into
   `{{Proposal_To1-4}}` / `{{Proposal_Letter1-5}}`? It would make the recipient
   block deterministic — it shipped **empty** once — but those tags carry a
   60-character audit and a layout constraint, and Go would then own line
   assignment. Bigger than it looks; I would want it as its own item.
4. **Schema versioning.** `schema_version` as an integer with additive-only
   changes, or full semver? Report Studio and checklist v2 will both bind to
   this, so the answer determines how breaking changes get made later.
5. **Site-visit questions.** Confirm the trigger is *"no `Site Recon Checklist`
   in `detected_files`"* — filename-based, which is what exists today. That is
   load-bearing and imperfect: a checklist named oddly reads as absent, and the
   EP gets asked questions the document could answer.
6. **Multi-parcel UI.** `parcel_ids` is an array. Does the form need repeatable
   inputs now, or is comma-separated text acceptable for v1 with the array as
   the wire format?

---

**STOP.** No code. Awaiting rulings on §6.
