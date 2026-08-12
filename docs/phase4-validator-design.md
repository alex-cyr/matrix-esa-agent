# Phase 4 — Template Tag Validator

**Status: APPROVED WITH AMENDMENTS.** External review returned 2026-08-11; all
six open questions answered, two additions required. Amendments are folded in
below and marked **[AMENDED]** where they changed the original design.

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

### Design — phased strictness [AMENDED]

Review chose a two-stage rollout rather than warn-only forever:

- **Now: warn-only.** Three of the seven are legitimate prose. Failing boot today
  would make the check something people route around.
- **After the seven are cleaned: rule-heading mismatches FAIL BOOT.** A rule
  *target* — the tag named in a bolded rule heading — pointing at nothing is
  never legitimate. Skills deploy with the container, so deploy time is the cheap
  place to fail; the expensive place is a delivered report with a blank section.
- **Prose references stay warn-only**, with an inline ignore marker for
  stragglers (option (b) from the original design), so historical notes like the
  `{{Radon_Summary}}` reference in rule 6 can be kept deliberately.

Scanning targets rule headings (the bolded `**… (`{{Tag}}`)**` form). Log format:
one `SKILL/TEMPLATE TAG DRIFT` line per skill file, once at boot, listing unknown
tags and whether each is a heading target or prose.

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

Current members: `ReportDate`, `DraftNote`, `ProjectNo`, `ParcelID`,
`SiteParcelID`, `parcel_id`, `SiteAcreage`, `site_acreage` — plus
`User_Authorization` once Phase 6 composes it.

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
3. **Signal A** — flag if the value's first *k = 3* words equal the preceding
   text's last 3 words (and symmetrically for trailing overlap).
4. **Signal B** — flag if the value's first word is **capitalised** and the
   template lead-in does **not** end in sentence-terminating punctuation: the
   value starts a new sentence in the middle of an existing one.
5. Flag **doubled terminal punctuation**: value ends `.` and the next template
   character is `.`.
6. Log at error level: tag name, signal type, preceding template words, value
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
| drift check | a rule-heading tag that does not exist produces the failure; a prose reference with an ignore marker does not |

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
