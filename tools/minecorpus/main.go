// minecorpus mines the Tier-1 historical transcripts for the passages the
// Phase 5 style digest is built from, and writes a review table.
//
// DETERMINISTIC. Zero model calls. Text that appears verbatim in nine reports
// can be found by diffing; a model asked to "find the common passages" across
// 157k tokens would be expensive, unverifiable, and free to invent a canonical
// block nobody wrote. Every candidate here carries provenance — which reports
// it came from — so a human can check it rather than take it on trust.
//
// Two passes:
//
//	Tier 1  sentences appearing verbatim in >= 7 of the 9 reports. The
//	        "canonical blocks, stated once each" set, FOUND rather than
//	        asserted.
//	Tier 2  sentences whose SKELETON recurs — the same sentence with different
//	        values. Numbers, dates and proper nouns are masked, then skeletons
//	        are clustered and their variants recorded. These are the "formulas
//	        with variant families".
//
// Run from the repo root:
//
//	go run ./tools/minecorpus
//
// Writes docs/phase5-mined-candidates.md. Reads only; touches nothing else.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	historicalDir = "historical"
	cacheDir      = "historical/.cache"
	outPath       = "docs/phase5-mined-candidates.md"

	// tier1Quorum is how many of the 9 must carry a sentence verbatim for it to
	// count as canonical. Not 9-of-9: one transcription hiccup in one report
	// should not disqualify a block that is plainly house-standard.
	tier1Quorum = 7

	// minWords filters out fragments and page furniture that survive cleaning.
	minWords = 8

	// maxVariantsShown caps how many variants of a Tier-2 skeleton are printed.
	maxVariantsShown = 4
)

// tier1Sources is the settled sourcing set: 9 independent E1527-21 reports.
// Excluded per CLAUDE.md — seven citing superseded E1527-13, HIES 2014 (cites
// neither and uses pre-convention phrasing), and the earlier half of each
// versioned pair (Rockdale Jan, ELC non-R1), which are drafts of a kept report
// rather than independent evidence of house style.
var tier1Sources = []string{
	"4962 Rockbridge ESA phase 1.pdf",
	"Clayton County Schools - 6630 Camp Street Ph.1 ESA April 2023.pdf",
	"ESA-Phase I 1078 Moreland Ave May 2023.pdf",
	"Final Phase I_Report Rockdale Judicial Admin Complex Feb 2025.pdf",
	"Final Phase_ I_Report_4127_Plunkett_Nov_2025.pdf",
	"Phase 1 Homestead Properties 9 10 23.pdf",
	"Phase I ESA Old Field Road October 2023.pdf",
	"Phase_ I_Report_1080_Moreland_October_2025_FINAL.pdf",
	"Updated Phase I_Report  Early Learning Center March 13 2026R1.pdf",
}

// shortName trims a source filename to something that fits a table cell.
func shortName(f string) string {
	n := strings.TrimSuffix(f, ".pdf")
	for _, cut := range []string{"Final Phase I_Report ", "Final Phase_ I_Report_", "Phase I_Report ",
		"Updated Phase I_Report  ", "Phase I ESA ", "ESA-Phase I ", "Phase 1 ", "Phase_ I_Report_"} {
		n = strings.TrimPrefix(n, cut)
	}
	if len(n) > 34 {
		n = n[:34]
	}
	return strings.TrimSpace(n)
}

var (
	whitespace = regexp.MustCompile(`\s+`)

	// Page furniture: running headers, page numbers, bare section numbering,
	// and figure/appendix captions. These recur in every report and would
	// otherwise dominate the Tier-1 list without being house *prose*.
	furniture = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^page \d+( of \d+)?$`),
		regexp.MustCompile(`(?i)^environmental site assessment\s*[-–]\s*phase i`),
		regexp.MustCompile(`(?i)^matrix engineering group,? inc\.?$`),
		regexp.MustCompile(`(?i)^(figure|table|appendix|plate)\s+[a-z0-9]`),
		regexp.MustCompile(`(?i)^meg\s+project no`),
		regexp.MustCompile(`^[\d\.\s]+$`),
		regexp.MustCompile(`(?i)^tucker,? georgia$`),
	}

	// Value masks for Tier-2 skeletons, applied in order. The point is to make
	// two sentences that differ only in their facts collide.
	valueMasks = []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`(?i)\b\d{1,2} (january|february|march|april|may|june|july|august|september|october|november|december),? \d{4}\b`), "[DATE]"},
		{regexp.MustCompile(`(?i)\b(january|february|march|april|may|june|july|august|september|october|november|december) \d{1,2},? \d{4}\b`), "[DATE]"},
		{regexp.MustCompile(`(?i)\b(january|february|march|april|may|june|july|august|september|october|november|december),? \d{4}\b`), "[DATE]"},
		{regexp.MustCompile(`\b\d{1,3}(,\d{3})+(\.\d+)?\b`), "[NUM]"},
		{regexp.MustCompile(`\b\d+(\.\d+)?\b`), "[NUM]"},
		// A run of capitalised words is a name, a county, a quadrangle or a
		// street. Crude, but it only has to make skeletons collide, and the
		// raw variants are printed alongside for the human to judge.
		{regexp.MustCompile(`\b([A-Z][a-zA-Z’'\-]+)(\s+[A-Z][a-zA-Z’'\-]+){1,5}\b`), "[NAME]"},
	}
)

func main() {
	texts := map[string]string{}
	for _, src := range tier1Sources {
		path := filepath.Join(historicalDir, src)
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("read %s: %v (run from the repo root)", path, err)
		}
		sum := sha256.Sum256(data)
		cachePath := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".txt")
		body, err := os.ReadFile(cachePath)
		if err != nil {
			log.Fatalf("no cached transcript for %s (%s): %v\nrun: go run ./cmd/esad -payload . -warm-historical", src, cachePath, err)
		}
		texts[src] = string(body)
	}

	// -grep <substring> prints how each source renders a passage after
	// normalization, and its skeleton. For diagnosing why an expected
	// canonical block did not reach the table -- which is how the sentence
	// splitter and the name-run collapse were both found to be wrong.
	if len(os.Args) == 3 && os.Args[1] == "-grep" {
		needle := strings.ToLower(os.Args[2])
		for _, src := range tier1Sources {
			for _, s := range sentences(texts[src]) {
				if strings.Contains(strings.ToLower(s), needle) {
					fmt.Printf("%-34s %s\n   skeleton: %s\n", shortName(src), s, skeleton(s))
				}
			}
		}
		return
	}

	// sentence -> set of sources carrying it verbatim.
	exact := map[string]map[string]bool{}
	// skeleton -> source -> one raw variant from that source.
	skeletons := map[string]map[string]string{}

	for src, body := range texts {
		for _, s := range sentences(body) {
			if exact[s] == nil {
				exact[s] = map[string]bool{}
			}
			exact[s][src] = true

			sk := skeleton(s)
			if skeletons[sk] == nil {
				skeletons[sk] = map[string]string{}
			}
			if _, seen := skeletons[sk][src]; !seen {
				skeletons[sk][src] = s
			}
		}
	}

	templateText, err := loadTemplateText()
	if err != nil {
		log.Fatalf("read template %s: %v", templatePath, err)
	}

	tier1 := collectTier1(exact)
	for i := range tier1 {
		tier1[i].TemplateOwned = isTemplateOwned(templateText, tier1[i].Text)
		if !tier1[i].TemplateOwned {
			tier1[i].ClassExcluded = excludedClass(tier1[i].Text)
		}
	}
	tier2 := collectTier2(skeletons, exact)

	if err := writeReport(outPath, texts, tier1, tier2); err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Printf("mined %d transcripts\n  tier 1 (verbatim in >=%d of %d): %d candidates\n  tier 2 (recurring skeletons):     %d families\n  wrote %s\n",
		len(texts), tier1Quorum, len(tier1Sources), len(tier1), len(tier2), outPath)
}

// abbrevPeriod matches a period that ends an abbreviation rather than a
// sentence. Splitting naively on ". " cuts "Matrix Engineering Group, Inc." in
// half -- which silently destroyed the transmittal-closing formula, one of the
// canonical blocks this tool exists to find.
// Deliberately does NOT include a single-letter rule. "[a-z]" under (?i)
// matches any initial, which welded "…provided in Appendices A to G." onto the
// sentence after it and hid the transmittal formula from the miner. Losing a
// split at "J. Smith" is cheap; losing one at "G." merges two sentences and
// destroys a canonical block.
var abbrevPeriod = regexp.MustCompile(`(?i)\b(inc|llc|l\.l\.c|ltd|co|corp|nos?|st|rd|ave|blvd|dr|mr|mrs|ms|jr|sr|ph|approx|fig|sec|vs|etc|e\.g|i\.e|u\.s)\.`)

const periodSentinel = "\x00P\x00"

// sentences splits a transcript into cleaned, de-duplicated sentences.
func sentences(body string) []string {
	body = strings.ReplaceAll(body, "\r", "\n")

	// Unwrap hard line breaks first. The extractor wraps prose mid-sentence, so
	// splitting on "\n" fragments exactly the long canonical passages we are
	// looking for. Join a line to the next unless it closes a sentence or the
	// next line starts a new block.
	body = unwrapLines(body)

	// Protect abbreviation periods, split, then restore.
	body = abbrevPeriod.ReplaceAllStringFunc(body, func(m string) string {
		return strings.TrimSuffix(m, ".") + periodSentinel
	})
	raw := regexp.MustCompile(`[.!?](?:\s|$)`).Split(body, -1)
	for i := range raw {
		raw[i] = strings.ReplaceAll(raw[i], periodSentinel, ".")
	}

	seen := map[string]bool{}
	var out []string
	for _, s := range raw {
		s = strings.TrimSpace(whitespace.ReplaceAllString(s, " "))
		s = strings.Trim(s, "•*_#|-— ")
		if s == "" || len(strings.Fields(s)) < minWords {
			continue
		}
		if isFurniture(s) {
			continue
		}
		if seen[s] {
			continue // repeated within one report: count the report once
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// blockStart marks a line that begins new material rather than continuing the
// previous line: numbered headings, bullets, and table rows.
var blockStart = regexp.MustCompile(`^\s*(\d+(\.\d+)*\s|[-•*|#]|\|)`)

// unwrapLines rejoins prose the extractor wrapped mid-sentence.
func unwrapLines(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	var cur strings.Builder

	flush := func() {
		if strings.TrimSpace(cur.String()) != "" {
			out = append(out, strings.TrimSpace(cur.String()))
		}
		cur.Reset()
	}

	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			flush()
			continue
		}
		if blockStart.MatchString(t) {
			flush()
			out = append(out, t)
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString(" ")
		}
		cur.WriteString(t)

		// A line ending in sentence punctuation closes the paragraph fragment.
		if strings.HasSuffix(t, ".") || strings.HasSuffix(t, "!") || strings.HasSuffix(t, "?") {
			flush()
		}
	}
	flush()
	return strings.Join(out, "\n")
}

func isFurniture(s string) bool {
	for _, re := range furniture {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// nameRun collapses a list of names into one placeholder. Without this,
// "work with Bartow County Schools" and "work with DeKalb County Parks,
// Recreation, and Cultural Affairs" mask to different skeletons and the
// transmittal-closing formula never collides -- which is exactly what happened
// on the first run of this tool.
var (
	// Absorbs single capitalised words too: "DeKalb County Recreation, Parks,
	// and Cultural Affairs" masks to "[NAME], Parks, and [NAME]", because
	// "Parks" alone is one word and the name mask needs two.
	nameRun    = regexp.MustCompile(`\[NAME\](\s*,\s*|\s+and\s+|\s*,\s*and\s+)(\[NAME\]|[A-Z][a-zA-Z’'\-]+)`)
	nameSuffix = regexp.MustCompile(`\[NAME\],?\s+(Inc|LLC|L\.L\.C|Ltd|Co|Corp)\.?`)
)

func skeleton(s string) string {
	for _, m := range valueMasks {
		s = m.re.ReplaceAllString(s, m.with)
	}
	// A corporate suffix is part of the name, not part of the formula.
	s = nameSuffix.ReplaceAllString(s, "[NAME]")
	// Collapse "[NAME], [NAME], and [NAME]" to one placeholder.
	for {
		next := nameRun.ReplaceAllString(s, "[NAME]")
		if next == s {
			break
		}
		s = next
	}
	return s
}

// --- editorial filters --------------------------------------------------------

// FILTER 1, mechanical: text the template already prints must never enter the
// digest. A digest that repeats template-owned prose is restatement fuel --
// rule 10's defect at corpus scale, aimed at every tag at once.
const templatePath = "knowledge/ESA_PHASE_I_Template.docx"

// templatePrefixWords is how much of a candidate must appear in the template
// verbatim to count as template-owned. Long enough not to fire on a shared
// stock phrase, short enough to catch a candidate the template interrupts with
// a {{tag}}.
const templatePrefixWords = 8

// loadTemplateText returns the template's static prose, tags stripped.
func loadTemplateText() (string, error) {
	zr, err := zip.OpenReader(templatePath)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var sb strings.Builder
	wt := regexp.MustCompile(`<w:t[^>]*>([^<]*)</w:t>`)
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "word/document.xml") &&
			!strings.HasPrefix(f.Name, "word/header") && !strings.HasPrefix(f.Name, "word/footer") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
		for _, m := range wt.FindAllStringSubmatch(string(b), -1) {
			sb.WriteString(m[1])
		}
		sb.WriteString("\n")
	}
	// Drop the placeholders themselves; what matters is the prose around them.
	text := regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(sb.String(), " ")
	return normalizeForMatch(text), nil
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9 ]+`)

func normalizeForMatch(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, " ")
	return whitespace.ReplaceAllString(s, " ")
}

// isTemplateOwned slides an 8-word window across the candidate rather than
// testing only its opening. A transcript sentence often carries a heading
// prefix the template stores separately -- "9.0 FINDINGS The following
// summarizes..." -- so a prefix-only test misses prose the template plainly
// owns. Any 8-word run in common is enough.
func isTemplateOwned(templateText, candidate string) bool {
	words := strings.Fields(normalizeForMatch(candidate))
	if len(words) < templatePrefixWords {
		return false
	}
	for i := 0; i+templatePrefixWords <= len(words); i++ {
		if strings.Contains(templateText, strings.Join(words[i:i+templatePrefixWords], " ")) {
			return true
		}
	}
	return false
}

// FILTER 2, class-based: static appendix and exhibit text the model never
// composes into a tag. The test for every candidate is "does the model write
// this into a {{tag}}?" -- if not, it is ballast in every prompt forever.
var excludedClasses = []struct {
	name string
	re   *regexp.Regexp
}{
	// Résumé and project-experience bullets from the personnel appendix. These
	// dominated the first survivor list: nineteen of the first fifty were CV
	// content, which is static exhibit text the model never composes.
	{"qualifications/resume", regexp.MustCompile(`(?i)(\b(b\.?s\.?|m\.?s\.?|ph\.?d|p\.?e\.?|p\.?g\.?|nicet|icc|aws|aci)\b|years of experience|resume|curriculum|qualifications of|registered professional|certified professional|has been employed|environmental professional as defined|\bmr\.\s|\bms\.\s|senior project manager|staff engineer|chief engineer|geotechnical engineer|project include|projects include|services included|responsible for (coordinat|the|managing)|performed and/or managed|represented the|developed the first|served as the|the work consisted|project manager|performed investigation|[“"][^”"]{25,}[”"])`)},
	{"letterhead/contact", regexp.MustCompile(`(?i)(www\.|https?://|@[a-z0-9.-]+\.(com|org|net)|\(\d{3}\)\s*\d{3}|\b\d{3}-\d{3}-\d{4}\b|fax\b|telephone\b)`)},
	// EDR and Sanborn licence, copyright and collection-description text. It
	// recurs in all nine reports because every report embeds the same vendor
	// exhibit -- it is not Matrix's voice, and no tag is composed from it.
	{"vendor boilerplate", regexp.MustCompile(`(?i)(sanborn library|the collection includes|maps in the collection|commercial reproduction|copyright holder|continually enhanced|purple shading|areas shaded|as of the day this report was generated|environmental data resources,? inc\.? \(edr\) is)`)},
	// Owner questionnaire items. Many arrive without their question mark, so
	// match the interrogative openers as well.
	{"questionnaire form", regexp.MustCompile(`(?i)(\?\s*$|^\s*(yes|no)\b.*\b(yes|no)\s*$|please (complete|answer|provide|return)|check the appropriate|if yes,|if no,|to the best of your knowledge|^(are|is|has|have|do|does|was|were|did)\b|^(is or has|was/is|has any|are there any|are you aware|do you have any)\b)`)},
	{"signature/seal block", regexp.MustCompile(`(?i)(respectfully submitted|sincerely,|prepared by:|reviewed by:|signature|seal\b)`)},
}

func excludedClass(s string) string {
	for _, c := range excludedClasses {
		if c.re.MatchString(s) {
			return c.name
		}
	}
	return ""
}

type candidate struct {
	Text    string
	Sources []string
	Score   int

	// Filter outcome.
	TemplateOwned bool
	ClassExcluded string
}

func (c candidate) Survives() bool { return !c.TemplateOwned && c.ClassExcluded == "" }

func collectTier1(exact map[string]map[string]bool) []candidate {
	var out []candidate
	for text, srcs := range exact {
		if len(srcs) < tier1Quorum {
			continue
		}
		out = append(out, candidate{Text: text, Sources: sortedKeys(srcs), Score: len(text) * len(srcs)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Text < out[j].Text
	})
	return out
}

type family struct {
	Skeleton string
	Sources  []string
	Variants []string
	Score    int
}

func collectTier2(skeletons map[string]map[string]string, exact map[string]map[string]bool) []family {
	var out []family
	for sk, bySource := range skeletons {
		if len(bySource) < tier1Quorum {
			continue
		}
		// Distinct raw forms. If there is only one, this is already a Tier-1
		// verbatim block and belongs there, not here.
		variantSet := map[string]bool{}
		for _, v := range bySource {
			variantSet[v] = true
		}
		if len(variantSet) < 2 {
			continue
		}
		variants := sortedKeys(variantSet)
		out = append(out, family{
			Skeleton: sk,
			Sources:  sortedKeys(toSet(bySource)),
			Variants: variants,
			Score:    len(sk) * len(bySource),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Skeleton < out[j].Skeleton
	})
	return out
}

func toSet(m map[string]string) map[string]bool {
	s := map[string]bool{}
	for k := range m {
		s[k] = true
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func provenance(srcs []string) string {
	var short []string
	for _, s := range srcs {
		short = append(short, shortName(s))
	}
	return fmt.Sprintf("%d/%d — %s", len(srcs), len(tier1Sources), strings.Join(short, "; "))
}

func writeReport(path string, texts map[string]string, tier1 []candidate, tier2 []family) error {
	var b strings.Builder

	b.WriteString("# Phase 5 Stage A — mined candidates\n\n")
	b.WriteString("**Generated by [tools/minecorpus](../tools/minecorpus/main.go). Deterministic; zero model calls.**\n\n")
	b.WriteString("Every candidate carries provenance — which of the 9 Tier-1 reports contain it.\n")
	b.WriteString("Nothing here is asserted to be canonical; it is *found* by diffing, and the\n")
	b.WriteString("counts are the evidence. Review decides what reaches the digest.\n\n")

	fmt.Fprintf(&b, "- Quorum for both tiers: **%d of %d** reports\n", tier1Quorum, len(tier1Sources))
	fmt.Fprintf(&b, "- Minimum sentence length: %d words\n", minWords)
	fmt.Fprintf(&b, "- Tier 1 candidates: **%d**\n", len(tier1))
	fmt.Fprintf(&b, "- Tier 2 families: **%d**\n\n", len(tier2))

	b.WriteString("## Source transcripts\n\n| Report | ≈ tokens |\n|---|---|\n")
	var names []string
	for n := range texts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "| %s | %dk |\n", shortName(n), len(texts[n])/4000)
	}

	// Filter accounting.
	var survivors []candidate
	byClass := map[string]int{}
	templateOwned := 0
	for _, c := range tier1 {
		switch {
		case c.TemplateOwned:
			templateOwned++
		case c.ClassExcluded != "":
			byClass[c.ClassExcluded]++
		default:
			survivors = append(survivors, c)
		}
	}

	b.WriteString("\n---\n\n## Tier 1 — after editorial filters\n\n")
	fmt.Fprintf(&b, "| Stage | Count |\n|---|---|\n| Mined (verbatim in ≥%d of %d) | %d |\n", tier1Quorum, len(tier1Sources), len(tier1))
	fmt.Fprintf(&b, "| — excluded, **template already prints it** | %d |\n", templateOwned)
	for _, c := range excludedClasses {
		fmt.Fprintf(&b, "| — excluded, %s | %d |\n", c.name, byClass[c.name])
	}
	fmt.Fprintf(&b, "| **Survivors** | **%d** |\n\n", len(survivors))

	b.WriteString("**Filter 1** is mechanical: any candidate whose opening words the template\n")
	b.WriteString("already prints is dropped. Template-owned text in a digest is restatement\n")
	b.WriteString("fuel — rule 10's defect at corpus scale, aimed at every tag at once.\n\n")
	b.WriteString("**Filter 2** drops static appendix and exhibit text the model never composes\n")
	b.WriteString("into a tag. The test is: *does the model write this into a `{{tag}}`?*\n\n")

	t1tok := 0
	for i, c := range survivors {
		t1tok += len(c.Text) / 4
		fmt.Fprintf(&b, "### T1-%02d · %s\n\n> %s\n\n", i+1, provenance(c.Sources), c.Text)
	}
	if len(survivors) == 0 {
		b.WriteString("_No candidate survived both filters._\n\n")
	}

	b.WriteString("---\n\n## Tier 2 — recurring skeletons with variant families\n\n")
	b.WriteString("Same sentence shape, different values. The **skeleton** is the formula; the\n")
	b.WriteString("**variants** show how it is filled. Values are masked as `[NUM]`, `[DATE]`, `[NAME]`.\n\n")
	for i, f := range tier2 {
		fmt.Fprintf(&b, "### T2-%02d · %s\n\n**Skeleton:** `%s`\n\n", i+1, provenance(f.Sources), f.Skeleton)
		shown := f.Variants
		if len(shown) > maxVariantsShown {
			shown = shown[:maxVariantsShown]
		}
		for _, v := range shown {
			fmt.Fprintf(&b, "- %s\n", v)
		}
		if len(f.Variants) > maxVariantsShown {
			fmt.Fprintf(&b, "- _…and %d more variants_\n", len(f.Variants)-maxVariantsShown)
		}
		b.WriteString("\n")
	}
	if len(tier2) == 0 {
		b.WriteString("_None met quorum._\n\n")
	}

	fmt.Fprintf(&b, "---\n\n_Tier 1 material is ≈%dk tokens as mined, before editorial selection._\n", t1tok/1000)
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
