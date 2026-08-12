# Phase 4 — Template Tag Validator

**Status: BUILT AND ACCEPTED.** External review returned 2026-08-11; all six
open questions answered, two additions required. Amendments are folded in below
and marked **[AMENDED]**. Acceptance run 2026-08-12 — see §12.

Author: Claude Code · Repo state at design: `1428cb9` · Template measured:
`knowledge/ESA_PHASE_I_Template.docx`

---

## Invariants

Three properties hold across the whole validator, not just the section that
introduced them. Everything below is subordinate to these.

1. **Detection never blocks delivery. [AMENDED]** The validator and the splice
   detector are **detection-only**: they annotate, log, and bracket. They never
   repair prose, and they never fail a merge. A validator that can block a
   report is a validator that gets disabled on a deadline. (Post-merge XML
   validation in `core.ValidateDocxXML` is separate and *does* fail the request —
   that guards file corruption, not content quality.)
2. **The canonical tag list is the union of body ∪ headers ∪ footers. [AMENDED]**
   300 tags today. Body-only counting re-prompts for tags the headers need.
3. **The retry mechanism must never become a fabrication mechanism. [AMENDED]**
   Any re-prompt that demands specific keys creates pressure to invent values to
   comply. Every such prompt must explicitly authorise emptiness.

---

## 0. The problem, and the evidence it is real

Bug 3: the Template Compiler emits ~300 `{{Key}}` values with **no validation**. A
key it forgets becomes a blank region in a signed, sealed report, and nothing in
the system notices.

This is not theoretical. Direct inspection of the delivered Providence Road
report (`Phase_I_ESA_Report_..._20260805_022316.docx`) shows:

| Section | Delivered state |
|---|---|
| 8.13 Radon | **Completely empty.** The "Radon" heading sits directly against "Asbestos Containing Materials". No zone, no pCi/L figure, nothing. |
| 8.15 Lead Based Paint | Static boilerplate only. The site-specific value never rendered. |
| 9.0 Findings | Compiler skipped the enumeration (previously recorded). |
| Header parts | 13 unreplaced tags + 2 fractured survivors (previously recorded). |

The cause of the 8.13 and 8.15 blanks turned out to be a *second* bug class:
compiler rules that name tags which **do not exist in the template**. Rule 6
specified the radon wording for `{{Radon_Summary}}`; the template's real slot is
`{{Sec8_13_Radon}}`. The value was emitted, matched nothing, and was discarded.
Fixed in `1428cb9`, but the class needs a systemic guard — §2.

**[AMENDED] Why this class survived two shipped reports and a fix pass:** the
dead key sat in the JSON schema block *beside its live twin*, so the model was
free to fill either one. That makes the discard **nondeterministic** — the same
bug produces a blank on one run and content on the next. Intermittent is the
worst kind of silent: it defeats spot-checking, and it makes the defect look
like a one-off rather than a structural fault.

**Correction to repo history. [AMENDED]** CLAUDE.md's SiteRecon investigation
note records `Sec8_13` as "filled with real site-visit content". Direct
inspection shows it empty. This lands as an **attributed correction** — the
original conclusion stays visible with a dated note recording what re-inspection
found and why the first reading was wrong. It is not a silent edit: the earlier
conclusion was reasonable on the evidence then available, and quietly rewriting
it destroys the reasoning trail that makes the note useful.

---

## 1. Canonical tag inventory (4a)

**Source of truth is the template, parsed at boot.** Not `docx_tags.txt` (stale
and itself fractured), not the skill markdown (drifted — §2).

### Algorithm

1. Open `knowledge/ESA_PHASE_I_Template.docx` at startup.
2. For `word/document.xml` and every `word/header*.xml` / `word/footer*.xml`:
   run the existing `unfractureDocxXML`, then extract `<w:t>` content
   **concatenated with no separator**, then match `\{\{([A-Za-z0-9_]+)\}\}`.
3. Union into a sorted set. Classify each tag:
   - **slot** — matches `^(Up|Down)\d+_`
   - **substantive** — everything else
4. Log once: totals, and the §2 drift report.
5. Fail startup only if the template is unreadable or yields zero tags —
   a template that parses to nothing means a broken container layout.

### Measured today

| Metric | Count |
|---|---|
| Distinct tags in `document.xml` | 299 |
| Header/footer tags | 3 (`ProjectNo`, `ReportDate`, `SiteStreetAddress`) |
| **Canonical union — the list the validator uses** | **300** |
| Slot tags (`UpN_*` / `DownN_*`) | **170** |
| **Substantive tags** | **130** |

**[AMENDED] The union is confirmed as an invariant, not an observation.** 299 is
the body count; the union is 300 because `ReportDate` appears only in `header9` /
`header10`. The validator uses the union. Anything that counts the body alone
will re-prompt the model for a header tag it was never asked to supply.

**Self-maintaining by design:** no count is stored in Go. Adding a tag to the
template changes the inventory at next boot. The numbers above are observations
recorded for review; they must not become constants in code.

### The extraction subtlety, recorded so it is not rediscovered

Joining `<w:t>` runs with a **space** produces 17 phantom "corrupted" tags
(`{{Sec8_ 10 _Spills}}`, `{{USGS_ TopoSource}}`, …). They do not exist. Word
splits tags across runs and the separator becomes part of the name. Join with
**no separator**. I made this mistake during investigation and caught it only by
re-inspecting; §8 carries a regression test so the shipped parser cannot regain
the habit.

---

## 2. Boot-time skill/template cross-check (new)

Every tag named in a compiler rule must exist in the canonical list. This turns
the 8.13-class bug from archaeology into a startup error.

### Measured today (`template-compiler/SKILL.md`)

294 distinct tags named. **Seven do not exist in the template:**

| Tag named in skill | Reality | Disposition |
|---|---|---|
| `ACM_Summary` | §8.14 — real slot is `Sec8_14_Asbestos` | **Dead-rules commit** (Addition A) |
| `LBP_Summary` | §8.15 — real slot is `Sec8_15_LBP` (**delivered blank**) | **Dead-rules commit** (Addition A) |
| `Proposal_To5` | Template has `Proposal_To1..4` only | **Dead-rules commit** (Addition A) |
| `ExecutiveSummary_Text` | Rule 3, Section 9.0 findings — real slots are `Sec9_Item1..8` | Rides with Phase 4 |
| `RadonZone` | Composition placeholder, not a tag | Prose — ignore marker |
| `Radon_Summary` | Fixed in `1428cb9`; now only a historical note | Prose — ignore marker |
| `Topo_QuadNames` | Composition placeholder, not a tag | Prose — ignore marker |

`ACM_Summary` and `LBP_Summary` are the same defect as radon and very likely
explain the §8.14/§8.15 blanks. `ExecutiveSummary_Text` is rule 3 — the Section
9.0 rule — pointing at nothing, a strong candidate for the skipped Section 9.0
enumeration; its rework is larger and rides with Phase 4, where `Sec9_Item1..8`
become substantive-mandatory anyway.

### Scope: headings **and** the JSON schema block [AMENDED]

The original design scanned rule headings only (option a). **That misses the
schema-rivalry class entirely**, because schema keys are not rule headings — and
schema rivalry is the variant that actually shipped, twice, nondeterministically.

The scan therefore covers **both kinds of machine-consumed target text**:

| Reference kind | Binding? | Why |
|---|---|---|
| Rule heading — `**… (`{{Tag}}`)**` | **Yes** | A rule target pointing at nothing is a rule written against nothing. |
| JSON schema block key | **Yes** | The model fills it directly. A dead key beside its live twin lets the model pick either. |
| Prose, examples, historical notes | No — warn | Explanatory text. Marked deliberate references are silenced. |

### Phased strictness [AMENDED]

Review chose a staged rollout rather than warn-only forever:

- **Schema-block mismatches FAIL BOOT — now.** `ca08ead` cleaned this tier, so
  it is already clean and can be enforced immediately. The class that had to be
  found by hand becomes a deploy-time error from here on. **Shipped as
  implemented.**
- **Rule-heading mismatches: error-logged, not yet fatal.** One known dead target
  remains — rule 3's `{{ExecutiveSummary_Text}}`, whose Section 9.0 rework rides
  with Phase 4. Promote this tier to fatal alongside the schema tier once that
  lands; the code carries a comment saying so at the promotion point.
- **Prose references stay warn-only**, with the `<!-- tag-check: ignore -->`
  marker for deliberate ones. Applied to the two references that exist precisely
  to stop mistakes recurring: the negative reference to `{{Proposal_To5}}` and
  the historical note naming `{{Radon_Summary}}`.

Skills deploy with the container, so deploy time is the cheap place to fail. The
expensive place is a delivered report with a blank section.

Log format: one `SKILL/TEMPLATE TAG DRIFT` line per skill file per tier, once at
boot, saying which tier each unknown tag came from.

> **Tradeoff accepted:** the ignore marker lives in the skill file, which *is*
> the system prompt, so it costs a few tokens of noise the model sees. The
> alternative — an allowlist in Go — would be a second list that has to agree
> with the first, which is the failure mode Addition B exists to eliminate.
> Keeping the marker next to the reference keeps one source of truth.

---

## 3. Where validation sits in the request flow

```
generateReportHandler
  pipeline.Run()                → finalPayload (TemplateCompiler JSON)
  ┌─────────────────────────────────────────────┐
  │ NEW: validateAndRepair(finalPayload)        │
  │   tier A: slot keys      → ""               │
  │   tier B: substantive    → ONE re-prompt    │
  │           still missing  → [MEG DATAGAP: k] │
  │   deliberate blanks      → logged + counted │
  └─────────────────────────────────────────────┘
  injectFieldDefaults()         → Go-supplied keys
  mergeDocxLogic()              → splice detector (§5) runs here
```

**Why before `injectFieldDefaults`:** so the re-prompt asks the model only for
things the model owns.

### The Go-supplied exclusion set — ONE definition [AMENDED]

The validator must not flag keys that Go is about to supply. The original design
described this as a set the validator and `injectFieldDefaults` must agree on.
**Review rejected agreement-by-convention.** It becomes a single shared
definition that both consume:

```go
// goSuppliedKeys are filled by injectFieldDefaults after the model runs. The
// validator must never re-prompt or DATAGAP them. Single definition on purpose:
// two lists that must agree will eventually not.
var goSuppliedKeys = map[string]bool{ ... }
```

Current members: `ReportDate`, `DraftNote`, `ProjectNo`, `ParcelID`, `SiteAcres`
— plus `User_Authorization` once Phase 6 composes it.

**[AMENDED] The set shrank from eight to five, because four of the original
eight were not template tags at all.** `injectFieldDefaults` was writing
`parcel_id`, `SiteParcelID`, `site_acreage` and `SiteAcreage`; the template
contains only `{{ParcelID}}` and `{{SiteAcres}}`. For the parcel ID one of the
three spellings happened to be right. **For acreage, both spellings were wrong,
so the EP's typed site acreage never reached the document on any run** — it was
written into the void while `{{SiteAcres}}` stayed unfilled. Found by
`TestGoSuppliedKeysExistInTemplate` the first time it ran, and fixed in the same
commit as the validator.

**Enforced by test, not by discipline.** §8 carries a divergence test: every key
`injectFieldDefaults` writes must appear in `goSuppliedKeys`, and vice versa. The
failure modes if they drift are precisely the two bugs this phase exists to kill
— a spurious re-prompt, or a silent blank.

---

## 4. Two-tier validation (4b)

### Tier A — table slots

Missing `UpN_*` / `DownN_*` → auto-fill `""`. Never re-prompted, never
DATAGAP'd. Log the count only.

### Tier B — substantive keys

Missing → collect → **exactly one** re-prompt → anything still missing becomes
`[MEG DATAGAP: <key>]`.

**Substantive-mandatory** (empty string is *not* acceptable; these are DATAGAP'd
even when the model deliberately returns `""`):

- `Sec9_Item1_SiteInfo` … `Sec9_Item8_DataGaps` (8)
- `SV_AccessFrom`, `SV_AccessVia`, `SV_CurrentUse`, `SV_ConditionSummary`,
  `SV_ObservedFeatures` (5)
- `DataGaps_Text` (1)

For all other substantive keys, `""` is accepted as a deliberate choice — **but
never an invisible one.**

### Deliberate blanks must be visible [AMENDED]

Review accepted the mandatory set as specified, to be widened from real runs,
**with a visibility layer added**. A deliberate `""` is still a blank; it must be
both *chosen* and *seen*:

- **Per-run log summary** — count plus the full key list of substantive keys the
  model intentionally left empty, at info level, once per generate.
- **`/generate` response** — the count travels in the JSON alongside
  `baselineStatus`, so the caller sees it without reading logs.

This is what makes "`""` means intentional" safe: the ambiguity that produced the
current bug was not that blanks existed, it was that nobody could see them.

### Re-prompt construction

- Reuse the **same** `TemplateCompilerAgent` config (same skill, same style
  baseline, `ResponseMIMEType: application/json`), as a fresh single-turn call.
- Content sent: the **upstream ASTM synthesis + the compiler's own first output**,
  plus the absent-key list verbatim.
- **Not** the full accumulated payload. It is very large, the missing values are
  derivable from the synthesis, and resending everything risks a second
  `MaxTokens` truncation — which would be self-defeating, since truncation is a
  likely cause of the missing keys in the first place.
- Merge: take **only** the requested keys from the response. Ignore extras — a
  re-prompt must not be able to overwrite values that were already correct.
- One attempt. No loop.

### Anti-fabrication clause — mandatory [AMENDED]

A prompt that demands specific keys creates pressure to invent values to comply.
The re-prompt must therefore **explicitly authorise emptiness**:

> Values must be drawn only from the synthesis provided. If the synthesis
> contains no basis for a key, return `""` for that key. An empty value is a
> correct answer; an invented one is not.

The validator then converts `""` to `[MEG DATAGAP: <key>]` for mandatory keys and
accepts it for the rest. This is invariant 3 made concrete: **the retry mechanism
must never become a fabrication mechanism.** Without this clause the validator
would trade a visible blank for an invisible fabrication, which is a strictly
worse failure in a signed report.

---

## 5. Splice detector (4d) — boundary comparison, not document scan

Per the ITEM 5 root-cause finding: the duplication is between **template-owned
lead-in text** and a **model-supplied value**, at a tag insertion boundary. A
whole-document n-gram scan is the wrong instrument — expensive, and noisy on a
report that legitimately repeats boilerplate.

Governed by invariant 1: **detection only, no repair, never blocks the merge.**

### Algorithm

At merge time, for each tag replaced, on the *unfractured, tag-stripped plain
text* of the part:

1. Take up to **N words immediately preceding** the tag position, and N words
   immediately following.
2. Normalise both sides: lowercase, strip punctuation.
3. **Signal A** — flag if the longest **suffix of the preceding template text
   that is also a prefix of the value** is *k = 3* words or longer, over a
   20-word window (and symmetrically: longest suffix of the value that is a
   prefix of the following text).

   > **[AMENDED] Deviation from the reviewed §5, approved.** The original
   > formulation compared the template's *last* 3 words against the value's
   > *first* 3. That assumes the duplication aligns at the boundary, and the
   > real `User_Authorization` splice does not: the value restates the lead-in
   > **from its first word** — template `"Matrix was authorized to perform this
   > work under "`, value `"Matrix was authorized to perform this work under a
   > signed proposal…"`. Last-3 is `[this work under]`, first-3 is `[Matrix was
   > authorized]`, so the specified rule scores zero on a splice that repeats
   > eight consecutive words. Caught by the test on its first run. The
   > suffix/prefix generalization catches both shapes at the same threshold.
   > The 20-word window exists because the restated clause is 8 words long and
   > a 6-word window would clip it.
4. **Signal B** — flag if the value's first word is **capitalised** and the
   template lead-in does **not** end in sentence-terminating punctuation: the
   value starts a new sentence in the middle of an existing one.
5. **Signal C [AMENDED — added, approved]** — flag an **adjacent duplicate word
   across the boundary**: the value's last word equals the template's next
   word, or the value's first word equals the template's previous word.
   Case-insensitive.

   Added from audit evidence, not from theory. The delivered Providence report
   reads *"the subject site occupies approximately **1.7 Acres acres** in
   size"* — the template supplies the unit and the value carried its own. The
   overlap is one word, so signals A and B both miss it. An adjacent duplicated
   word across a tag boundary is almost never legitimate English, and
   detection-only makes the noise cheap. If the acceptance run shows it firing
   spuriously it gets demoted **with data**, not pre-emptively.

6. Flag **doubled terminal punctuation**: value ends `.` and the next template
   character is `.`.
7. Log at error level: tag name, signal type, preceding template words, value
   prefix.

### Threshold decision [AMENDED — resolved]

The brief specified "repeated 5+ word sequence". **That rule would not catch the
defect that motivated it:**

```
template lead-in : "... the topography of the site "
value            : "The topography suggests ..."
literal overlap  : "the topography"  → 2 words
```

Review shipped **both signals (option c)**. Signal B is the high-value rule: a
capitalised word beginning a value spliced mid-sentence is nearly always wrong,
independent of overlap length. Signal A catches longer restatements like
`User_Authorization`.

### Bracket exemption — mandatory [AMENDED]

**Signal B fires on our own brackets.** `[EP VERIFY: …]` and `[MEG DATAGAP: …]`
are *designed* to land mid-sentence and begin with a capital — the `{{GWFlowDir}}`
slot guarantees it (*"groundwater is inferred to flow in a [EP VERIFY: …]
direction"*). Without an exemption the detector would scream on every correctly
flagged report, and the first thing anyone would do is turn it off.

**Values beginning `[MEG` or `[EP` are exempt from signal B.** This carries its
own test — a bracket value mid-sentence must stay silent (§8).

Proper-noun false positives (USGS, EDR, county names) are accepted **only
because detection never blocks the merge**. That trade is licensed by invariant 1
and by nothing else; if the detector ever gains repair or blocking behaviour,
the false-positive tolerance has to be revisited first.

---

## 6. DataGaps / Section 9 numbered slots (4c)

**Finding: the numbering is not literal text.** The brief anticipated orphaned
`2)` / `3)` strings. In the actual template:

- `{{Sec9_Item1..8}}` each sit in their own paragraph, all carrying
  `<w:numPr><w:numId w:val="8"/>` — an auto-numbered list.
- `{{DataGaps_Text}}` sits in a paragraph with `numId=9`, `ilvl=0`.

So an empty value leaves an **auto-numbered paragraph containing no text**, and
Word renders its number against blank space. There is no literal string to strip.

Interaction with §4: once mandatory keys are DATAGAP'd, *missing* keys never
render empty. Orphans therefore arise only from **deliberately** empty values.

### Approved: merge-time paragraph removal [AMENDED — approved]

If a tag's value is empty **and** its enclosing `<w:p>` carries `numPr` **and**
the paragraph has no other text content, drop the whole paragraph.

Approved as option (a), explicitly consistent with the radon precedent: **the
template is the canonical contract and stays untouched**; the code moves to the
template, never the reverse. Guards as specified — the `numPr` condition and the
no-other-text condition keep the blast radius inside numbered-list paragraphs,
where a blank entry is never desirable.

**Touches `mergeDocxLogic` → diffs to the reviewer before it is applied**, per
the standing rule.

### Parked: DataGaps_Text rendering [AMENDED — do not change in Phase 4]

`SanitizeDocxValue` expands `\n` into `<w:br/>`, so a `DataGaps_Text` holding
three gaps renders as **one** numbered item with two line breaks, not three
numbered items.

**This is a house-format decision for Eric, and it stays parked.** Phase 4 must
**not** change this rendering behaviour. Noted here so the question is not lost
and not answered by accident inside an unrelated change.

---

## 7. Skill count fix (4e)

`template-compiler/SKILL.md` currently states "~280 variables" and "272 distinct
tags: ~102 substantive keys plus 170 table slots". Correct to:

> **300 canonical tags — 299 in the report body plus `ReportDate` in the running
> header. 170 are `UpN_*` / `DownN_*` table slots; 130 are substantive.**

Also add:

- `ReportDate` and `DraftNote` are **Go-supplied and always overwritten** — do
  not spend effort on them.
- Unused `UpN`/`DownN` slots MUST be `""` (already added).

---

## 8. Tests (4f)

| Test | Asserts |
|---|---|
| canonical parse | 170 slots / 130 substantive from the real template; header-only `ReportDate` present in the union |
| whitespace-join regression | the no-separator join rule; a space-joined parse must not be what ships |
| tier A | absent `Up7_Name` auto-fills `""`, is not re-prompted |
| tier B mandatory | absent `Sec9_Item3_Wetlands` → re-prompt list contains it; still absent → `[MEG DATAGAP: Sec9_Item3_Wetlands]` |
| tier B empty-string | `""` for a mandatory key is treated as missing; `""` for a non-mandatory key is accepted |
| **deliberate-blank visibility** | intentionally-empty substantive keys are counted and listed, and the count reaches the `/generate` response |
| re-prompt shape | the constructed request names exactly the absent keys, and carries the anti-fabrication clause |
| re-prompt merge | extra keys in the response are ignored; existing correct values are never overwritten |
| **exclusion divergence** | every key `injectFieldDefaults` writes is in `goSuppliedKeys` and vice versa — the two cannot drift |
| Go-supplied exclusion | `ReportDate` absent from model output is never re-prompted or DATAGAP'd |
| splice signal A | the real Providence lead-in + `"The topography suggests…"` fires |
| splice signal B | a capitalised value spliced mid-sentence fires |
| **splice bracket exemption** | a value beginning `[MEG` or `[EP` mid-sentence stays **silent** |
| splice negative | the correct fragment `"gently slopes to the …"` stays silent |
| splice punctuation | value ending `.` before a template `.` fires |
| numbered-slot cleanup | empty `Sec9_Item8_DataGaps` removes its numbered paragraph; a non-empty one is untouched |
| drift check — classification | heading / schema / prose are bucketed correctly; an ignore-marked prose reference is silent |
| **drift check — schema tier clean** | the real compiler skill has zero schema-block drift, so the tier that fails boot stays passable |
| **go-supplied keys are real tags** | every member of `goSuppliedKeys` exists in the template — the check that found the lost site acreage |
| **mandatory keys are real tags** | every member of `substantiveMandatory` exists, or it would be bracketed on every run and never render |

---

## 9. Acceptance [AMENDED]

A live Providence Road `/generate` where:

- **Zero *unchosen* blanks.** Every blank is one of exactly three things: a table
  slot, a logged deliberate blank, or a bracket. Nothing is blank by accident.
  (The original "zero silent blanks" contradicted §4's acceptance of deliberate
  `""`; this is the reconciled wording.)
- Every canonical tag is filled, logged-deliberate, or wearing a loud bracket —
  `[MEG DATAGAP: …]` or `[EP VERIFY: …]`.
- Specifically **8.13, 8.14, 8.15 and Section 9.0** all carry content or a
  bracket.
- Header clean — date, address, project number, no stray marks.
- The run survives a 429 without burning retries on non-retryables.

---

## 10. Review outcome

| # | Question | Resolution |
|---|---|---|
| 1 | Splice threshold | **Ship both signals (c)**, with a mandatory exemption for values beginning `[MEG` / `[EP`, plus its own test. Detection-only promoted to a validator-wide invariant. |
| 2 | Empty-string semantics | **Mandatory set as specified**, widened from real runs, **plus a visibility layer** — logged summary and a count in the `/generate` response. §9 reworded to "zero unchosen blanks". |
| 3 | Re-prompt context | **Synthesis + first output**, as designed, **plus a mandatory anti-fabrication clause** authorising `""`. Take-only-requested-keys merge rule unchanged. |
| 4 | Drift strictness | **Phased.** Warn-only now; rule-heading mismatches **fail boot** once the seven are cleaned. Prose stays warn-only with an ignore marker. |
| 5 | Paragraph removal | **Approved**, merge-time, guards as specified, diffs-first. Template stays untouched, per the radon precedent. |
| 6 | Count | **Confirmed: 300-tag union**, written in as an invariant. |

**Addition A — dead rules do not wait for the validator.** `ACM_Summary` →
`Sec8_14_Asbestos` and `LBP_Summary` → `Sec8_15_LBP` are the radon fix again, one
line each, and §8.14/§8.15 ship broken on every generate until they land.
Immediate small commit, same shape as `1428cb9`, trimming `Proposal_To5` in the
same pass. Rule 3's `ExecutiveSummary_Text` rework rides with Phase 4.

**Addition B — one definition, not two agreeing lists.** §3's `goSuppliedKeys` is
a single shared variable consumed by both the validator and
`injectFieldDefaults`, with a divergence test. The failure analysis that argued
for it — spurious re-prompt or silent blank — is the reason it cannot be a
convention.

---

## 11. Build order

| Step | Work | Gate |
|---|---|---|
| 0 | This docs commit | — |
| 1 | **Dead-rules commit** (Addition A) | Immediate |
| 2 | **Validator core** — canonical inventory (union, no-separator join + regression test), two-tier validation, re-prompt with anti-fabrication clause, drift check, shared exclusion set. Tests per §8 including bracket-exemption and exclusion-divergence. | — |
| 3 | **Merge-path changes** — splice detector (§5) and numbered-paragraph removal (§6) | **DIFFS FIRST to the reviewer before applying** |
| 4 | **Acceptance** — live Providence `/generate` per amended §9 | Result back to reviewer |

Checkpoints to the reviewer: the amended doc's diff summary (this commit), then
the merge-path diffs before step 3 applies, then the acceptance-run result.

---

## 12. Acceptance run — 2026-08-12 [AMENDED]

Providence Road, `Phase_I_ESA_Report_..._20260812_004029.docx`. HTTP 200 in
9m48s, baseline 19/19.

```
slots_auto_filled=0  missing_first_pass=12  reprompt_ran=true
recovered=12  data_gapped=0  deliberate_blanks=1  unknown_keys=7
```

**§9 criteria: all passed.** Zero unreplaced tags, zero fractured survivors,
header clean, and 8.13 / 8.15 / Section 9.0 all carrying real content where the
August 5 draft had a blank, boilerplate only, and a skipped enumeration.

**The run is its own justification.** The compiler emitted seven *invented*
Section 9.0 key names — `Sec9_Item1_Intro`, `Sec9_Item2_SiteInfo`,
`Sec9_Item3_Topography`, `Sec9_Item4_WetlandsFlood`, `Sec9_Item6_RECs`,
`Sec9_Item7_HRECsCRECs`, `Sec9_Item9_DeMinimis` — none of which exist. Without
the re-prompt this run ships a blank Section 9.0, which is the original
delivered defect. The root cause was rule 3 still naming `ExecutiveSummary_Text`:
with no valid target the model guessed at names. Reworked to name all eight
slots explicitly, so Section 9.0 fills on the first pass and the re-prompt goes
back to being a safety net.

### Splice detector, first live data

| Signal | Findings | Verdict |
|---|---|---|
| sentence-start (B) | 73 across 67 tags | Almost all proper nouns — **refined**, below |
| doubled-period | 7 | Useful |
| overlap (A) | 2 | Precise |
| duplicate-word (C) | 2 | **Both real defects** |

Two live defects found, both fixed at source afterwards:

1. **`User_Authorization`** — *"in accordance with **This work was performed in
   accordance with** our proposal dated July 6, 2026**..**"*. EP-caught error 5,
   still shipping. Caught by three signals at once. Root cause was rule 6
   itself, which *instructed* the restatement — the same disease as rule 10: an
   instruction mandating restatement of template-owned text outranks a SPLICE
   RULE 250 lines away, every time.
2. **`SiteCounty`** — *"the **Fulton County County** Tax Assessor's website"*.
   Previously unknown, found by signal C, the signal that was added on a hunch
   from one piece of audit evidence.

### Signal B refinement [AMENDED — approved]

Unrefined, signal B is 87% of output and unusable. It is also the *only*
defense against the two-word-overlap restatement class, so it was refined
rather than demoted: **fire only when the value's capitalised opening words
also appear in the preceding window** (2 words, 12-word lookback).

A restatement necessarily echoes what it restates; a proper noun does not.
Checked against all 73 findings from this run: every proper-noun case is
silenced (`Alpharetta, GA 30009`, `Mr. Ihssan Hashem`, `August 2026`,
`Fulton County`) and `The topography suggests` still fires. Pinned by tests in
both directions.

### Supply-chain check

Rule 6 now requires the radon zone from upstream data — so the parser was
checked for a supplier and **had none**. Without that, rule 6 would have
data-gapped radon on every run: honest, but avoidably so. Parser extraction
parameter 5 now extracts the EPA Radon Zone, the county it belongs to, and the
activity threshold, and the compiler cross-checks the county before using it.

### Confirmation run — same day, after the close-out fixes

`Phase_I_ESA_Report_..._20260812_014347.docx`. First attempt returned HTTP 500:
`ResourceExhausted` on the Template Compiler after 4 attempts — a genuine Vertex
quota limit from running three ~550k-token generations in one evening, not a
code fault. The classifier named it `throttled`, spent 30/60/90s of backoff and
surfaced the reason instead of a generic timeout, which is what ITEM 1 was for.
The retry after a quota pause returned HTTP 200 in 12m17s.

```
slots_auto_filled=0  missing_first_pass=5  reprompt_ran=true
recovered=3  data_gapped=2  deliberate_blanks=1  unknown_keys=0
```

| Target | Before | After |
|---|---|---|
| `User_Authorization` | *"in accordance with **This work was performed in accordance with** our proposal dated July 6, 2026**..**"* | *"in accordance with our proposal dated July 6, 2026."* |
| `SiteCounty` | *"the **Fulton County County** Tax Assessor's website"* | *"the Fulton County Tax Assessor's website"* |
| Invented Sec9 keys | 7 | **0** — `unknown_keys=0`, and Section 9.0 filled on the FIRST pass |
| Missing on first pass | 12 | **5** |
| Splice findings | 84 | **14** |

`data_gapped=2` is `SV_AccessFrom` and `SV_AccessVia`, rendering as
*"accessed from [MEG DATAGAP: SV_AccessFrom] via [MEG DATAGAP: SV_AccessVia]"* —
the intended outcome for data genuinely absent from the checklist. Zero
unreplaced tags, header clean, 8.13 populated.

### Two findings for the next pass

1. **`{{FloodZone}}` needs a fragment contract**, third of its kind. Template
   reads *"designates the site as zone `{{FloodZone}}`"*; the value was
   `Zone X`, delivering *"as zone Zone X"*. Found by signal C again — the
   signal added on a hunch has now caught three real defects
   (`SiteAcres`, `SiteCounty`, `FloodZone`) and produced no false positives.
2. **Signal B is close but not done.** Of its 9 findings, **one is a true
   positive** — `SV_CurrentUse` delivered *"The site is presently The site is
   currently developed with…"*, a real restatement. The other 8 are all
   single-word `Residential` table cells (`North_AdjUse`, `East_SurrUse`, …)
   that echo the word "residential" in nearby template prose.

   Proposed one-line tightening, **not applied**: require the value to be at
   least 2 words before signal B can fire. Single-word values are table cells
   and proper nouns, never restatements. That drops all 8 remaining false
   positives and keeps both true positives, since `The topography suggests` and
   `The site is presently…` are multi-word.

### Addendum — both next-pass findings ratified and applied

`{{FloodZone}}` now carries the same fragment contract as `{{SiteAcres}}` and
`{{SiteCounty}}`: skill rule 11 plus a Go normalizer, with the shared principle
stated once — *where the template prints a label or unit next to a tag, the
value supplies only the part the template does not.*

Signal B requires a value of **at least two words**. A single-word value is a
table cell or a proper noun, never a restatement. Both true positives are pinned
by test: `The topography suggests…` and `The site is presently The site is
currently developed with…`.

Expected effect on the next run: splice findings 14 → ~5, with the surviving
signal-B findings being real.
