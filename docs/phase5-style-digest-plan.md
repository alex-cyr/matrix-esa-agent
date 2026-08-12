# Phase 5 — Style Digest

**Status: APPROVED WITH RULINGS** (2026-08-12). All five questions answered and
one addition required; see §7. Nothing built yet beyond Stage A.

Author: Claude Code · Repo state: `2e2dc11`

---

## 1. Measured baseline — the CLAUDE.md figure is 20% low

Every number below is measured from the warm cache, not estimated.

| | bytes | ≈ tokens |
|---|---|---|
| All 19 cached transcripts | 1,316,877 | **329k** |
| **Attached to two agents — per run** | | **≈ 658k** |
| The 9 Tier-1 reports | 631,850 | **157k** |

CLAUDE.md records ~275k / ~550k. The real figure is **329k / 658k**. The
correction matters because it moves the reduction target.

**This is now operational, not cosmetic.** The 2026-08-12 confirmation attempt
died on `ResourceExhausted` at the Template Compiler — the node carrying the
largest accumulated payload — after three generations in one evening. The
corpus is the dominant term in that payload.

---

## 2. The budget arithmetic, and a question it forces

The brief says **60–80k per run**. The digest is attached to **two** agents, so:

> **A 60–80k *per-run* budget means a 30–40k *file*, not a 60–80k file.**

CLAUDE.md's Phase 5 text says "target 60–80k tokens total", which reads as file
size and would land at 120–160k per run. Both are large improvements on 658k;
they are not the same target. **This plan assumes per-run**, i.e. a 30–40k file,
and flags it as question 1.

That budget is tight against the exemplars:

| Component | ≈ tokens |
|---|---|
| Old Field Road (primary exemplar) | 15k |
| 1080 Moreland FINAL (REC-present exemplar) | 20k |
| Homestead (optional third) | 17k |
| **Two exemplars alone** | **35k** |
| **Three exemplars alone** | **52k** |

Two exemplars consume the entire 30–40k budget and leave nothing for the
distilled tiers — which are the actual point of the digest. Three overshoot it
outright.

### Proposal: split the digest by role rather than shrink it further

The two consumers do not need the same thing:

- **ASTM Synthesizer** writes rationales, REC/HREC/CREC reasoning, regulatory
  lingo. It needs Section 9/10 formulas, the data-gap definition, outcome
  phrasing — and one *reasoning-shaped* exemplar.
- **Template Compiler** emits per-tag values. It needs canonical blocks,
  fragment shapes, section-opener wording — and one *formatting-shaped*
  exemplar.

Splitting produces **two files of ~20–25k each, ~45k per run total**, with each
agent getting more of what it actually uses than a shared 30–40k block would
give it. It also fits the exemplar picks naturally: 1080 Moreland (REC-present)
to the Synthesizer, Old Field Road (clean, vacant — closest match to Providence)
to the Compiler.

This is a change to the reviewed Phase 5 shape and needs an explicit ruling —
question 2.

---

## 3. Extraction method for the 9 Tier-1 reports

**Deterministic first, model last.** The corpus is 157k tokens of Tier-1
material; asking a model to "find the common passages" across it is expensive,
unverifiable, and exactly the kind of step that quietly invents a canonical
block nobody wrote. Text that appears verbatim in nine reports can be found by
diffing.

### Stage A — deterministic candidate mining (Go, zero model calls)

1. **Normalize** each of the 9 transcripts: collapse whitespace, split into
   sentences, strip page furniture (running headers, page numbers, figure
   captions) by pattern.
2. **Tier 1 candidates — exact intersection.** Hash each sentence; keep those
   appearing in **≥ 7 of 9** reports. Rank by (length × frequency). This is the
   "canonical blocks, stated once each" set, and it is *found*, not asserted —
   directly answering CLAUDE.md's instruction to **verify identity by diffing,
   not assume it**.
3. **Tier 2 candidates — skeleton clustering.** Re-run the same pass over
   sentences with values masked: numbers, dates, proper nouns, county names,
   quadrangle names, and addresses replaced by typed placeholders. Sentences
   whose *skeletons* match but whose values differ are the "formulas with
   variant families". Cluster them and record each variant.
4. **Emit a review table**, not prose: candidate text, how many of the 9 carry
   it, which ones, and token cost. A human can scan that.

The tool is one-shot and belongs in `tools/`, alongside `fixheader` and
`draftnote`, for the same reason those do: the digest is generated from
committed inputs, and the generator is the only reviewable record of how.

### Stage B — assembly

Compose `knowledge/style_baseline.md` from the Stage A tables — Tier 1 blocks
stated once, Tier 2 formulas with their variant families, then the exemplars
appended whole. Assembly is editorial and small; whether that pass is done by
hand or by one model call is question 3. Either way the input is the mined
table, never the raw corpus.

### Stage C — verification before anything is wired

- **Token count reported** against the budget, per file and per run.
- **Provenance check**: every Tier 1 block traceable to the reports it came
  from. A block that cannot be traced to ≥7 of 9 does not belong in Tier 1.
- **Contamination check**: no client names, site addresses, parcel IDs, owner
  names or project numbers in the digest. This is the highest-risk defect in
  the whole phase — a digest is attached to *every* report, so one leaked
  client detail becomes a cross-client data leak in a signed deliverable, not
  just a wrong word. Automated scan plus human review.
- **ASTM version guardrail**: add to both consuming skills — *"Baseline reports
  may cite older ASTM versions; always cite E 1527-21 regardless of baseline
  phrasing."* The Tier-1 nine are all E1527-21, but the exemplars are appended
  whole and may quote surrounding material.

**Then STOP for human review before wiring the agents to it.** The digest is a
reviewable style asset, not just a prompt.

---

## 4. Sourcing — settled, and verified rather than trusted

The Tier-1 nine, confirmed present in the cache today:

| Report | ≈ tok |
|---|---|
| 4962 Rockbridge | 13k |
| Clayton County Schools — 6630 Camp Street | 17k |
| ESA-Phase I 1078 Moreland Ave | 17k |
| Rockdale Judicial Admin Complex **Feb 2025** | 18k |
| 4127 Plunkett | 20k |
| Homestead Properties | 17k |
| **Old Field Road** | 15k |
| **1080 Moreland FINAL** | 20k |
| Early Learning Center **R1** | 17k |

Excluded exactly as CLAUDE.md specifies: seven E1527-13 reports, HIES 2014, and
the earlier halves of both versioned pairs (Rockdale Jan, ELC non-R1).

One check I would run in Stage A rather than assume: CLAUDE.md's inventory
classifies Old Field Road as clean/vacant/E1527-21 and 1080 Moreland as
REC-present/vacant/E1527-21. That inventory was itself produced by reading, and
the acceptance run has made me wary of inherited conclusions. Confirming the
ASTM version string and outcome phrasing in both transcripts costs one grep.

---

## 5. Rollout and rollback

1. Land the digest as a reviewed file. **No agent wired.**
2. Wire the ASTM Synthesizer and Template Compiler to their digests; delete the
   `corpus.PromptBlock()` attachment.
3. One live Providence generate. Compare against the 2026-08-12 confirmation
   run — same inputs, same validator, so **prose quality is the only variable**.
   Report tokens per node and the validator block.
4. `LoadHistoricalCorpus` and the warm cache **stay**. The corpus remains the
   source the digest is regenerated from, and the boot preflight still guards
   it. Rollback is re-attaching `PromptBlock()`.

Acceptance: **per-run prompt tokens down from ~658k to the agreed budget**, with
no regression in the validator block and no loss of house voice in the
Section 9/10 prose on the comparison run.

---

## 6. Questions for review

1. **Budget basis.** 60–80k **per run** (→ 30–40k file) or per file (→ 120–160k
   per run)? This plan assumes per run.
2. **Role-split digest** — two ~20–25k files, one per consuming agent, instead
   of one shared file? It is the only way I see to fit meaningful exemplars
   inside a per-run budget, and each agent gets more of what it uses.
3. **Assembly pass** — hand-authored from the mined tables, or one model call
   over them?
4. **Exemplar count.** Two (Old Field + 1080 Moreland, 35k) fits a role split.
   The optional third (Homestead, 17k) does not fit any per-run budget without
   dropping distilled content. Drop it?
5. **Exemplar trimming.** If the budget stays tight, do we append exemplars
   whole as specified, or trim to their load-bearing sections (3.1, 5.x, 7.x,
   9.0, 10.0)? Whole is safer for voice; trimmed buys ~40%.

---

## 7. Rulings (2026-08-12)

| # | Question | Ruling |
|---|---|---|
| 1+2 | Budget basis and role-split | **Adopt the role-split.** Two files, ~20–25k each, **~45k per run**. The 60–80k figure was never load-bearing precision: the operational goal is per-run tokens far below quota pressure, and 658k → ~45k is a **93% cut** that beats both readings. Each agent getting only what it uses is better engineering than a shared blob regardless of budget. |
| 3 | Assembly | **Claude Code composes** the digests from the mined tables — **never from the raw corpus** — and the assembled files go to Elias before any agent is wired. The Stage C stop is a **hard gate**. |
| 4 | Exemplar count | **Two. Drop Homestead.** One per file: 1080 Moreland (REC-present, reasoning-shaped) → Synthesizer; Old Field Road (clean/vacant, formatting-shaped) → Compiler. A third fits no budget without eating the distilled tiers, which are the point. |
| 5 | Trimming | **Append whole** — voice safety wins. Trim to load-bearing sections only if a file measurably busts ~25k, and report the trim if so. |

### Addition — pseudonymize the exemplars before assembly

The contamination check in §3 Stage C, as originally written, scanned the
distilled tiers but **exempted the exemplars** — which are full client reports
carrying names, addresses, parcel IDs and project numbers, appended to *every
future report's prompt*. The largest leak vector was inside the exemption.

Before assembly, run a **deterministic identifier pass** over both exemplars:
client names, owner names, site addresses, parcel IDs and project numbers
replaced with typed placeholders — `[CLIENT]`, `[SITE ADDRESS]`, `[PARCEL ID]`,
`[OWNER]`, `[PROJECT NO]`.

Three things this buys, in order of importance:

1. **The leak vector dies.** Voice is carried by sentence structure and
   register, not by which county is named; the exemplars keep everything that
   makes them useful.
2. **The contamination scan then runs over the ENTIRE digest with zero
   exemptions** — a far stronger guarantee than "scan everything except the
   risky part."
3. **It removes the mimicry temptation.** An exemplar whose identifiers are
   placeholders *cannot* leak a real address into another client's report even
   if a downstream rule fails. This is the same reasoning as the skill-exemplar
   convention: a concrete value in front of a model is a value it may copy, and
   `gently slopes to the south` already proved that once.

### Endorsed as written

Stage A deterministic mining with provenance (≥7-of-9, found not asserted); the
one-shot tool in `tools/`; the ASTM version guardrail in both consuming skills;
grep-verifying the two exemplars' classifications rather than inheriting them;
and corpus + cache retained as the regenerable source with `PromptBlock()`
re-attachment as rollback.

### Build order

1. **Stage A tool + review table** → **STOP**, table to Elias.
2. Assembly (with the pseudonymization pass) → **STOP**, digest files to Elias.
3. Wire both agents, delete the corpus attachment.
4. Comparison Providence generate vs the 2026-08-12 confirmation run.
5. Report per-node tokens + validator block.
