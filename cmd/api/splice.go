package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"
)

// Splice detection and empty numbered-paragraph removal: the two merge-path
// changes from the Phase 4 design (§5 and §6).
//
// INVARIANT: detection only. Nothing here repairs prose and nothing here fails
// a merge. That is what licenses the accepted proper-noun false positives; if
// this ever gains repair or blocking behaviour, the tolerance has to be
// revisited first.

// boundaryWords is how many words of context appear in the log line.
const boundaryWords = 6

// contextWindow bounds the overlap comparison. This is a boundary check, not a
// document scan, but the window has to be wide enough to hold a whole restated
// lead-in: "Matrix was authorized to perform this work under" is eight words,
// and the value repeated all of it.
const contextWindow = 20

// spliceOverlapWords is signal A's threshold.
const spliceOverlapWords = 3

// bracketPrefixes mark our own deliberate insertions. [EP VERIFY: ...] and
// [MEG DATAGAP: ...] are DESIGNED to land mid-sentence and start with a capital
// -- the {{GWFlowDir}} slot guarantees it. Without this exemption signal B
// would fire on every correctly flagged report, and the detector would be
// switched off within a week.
var bracketPrefixes = []string{"[MEG", "[EP"}

// SpliceFinding is one suspected duplication at a tag boundary.
type SpliceFinding struct {
	Part   string
	Tag    string
	Signal string // "overlap", "sentence-start", "duplicate-word", "doubled-period"
	Before string // template words immediately preceding the tag
	Value  string // leading words of the inserted value
}

var wordSplit = regexp.MustCompile(`\s+`)

// normalizeWords lowercases and strips punctuation for comparison.
func normalizeWords(s string) []string {
	var out []string
	for _, w := range wordSplit.Split(strings.TrimSpace(s), -1) {
		w = strings.TrimFunc(w, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if w != "" {
			out = append(out, strings.ToLower(w))
		}
	}
	return out
}

func lastN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[len(xs)-n:]
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}

func equalWords(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// echoLookback is how far back the lexical-echo test looks.
const echoLookback = 12

// echoWords is how many of the value's opening words must appear in the
// preceding text for a capitalised restart to count as a restatement.
const echoWords = 2

// echoesPrecedingText reports whether the value's opening words are already
// present in the template text just before it.
//
// This is what separates a restatement from a proper noun. "The topography
// suggests" echoes a lead-in ending "the topography of the site"; "Alpharetta,
// GA 30009" echoes nothing, because a place name is new information rather
// than a repetition.
func echoesPrecedingText(beforeWords, valueWords []string) bool {
	window := lastN(beforeWords, echoLookback)
	if len(window) == 0 {
		return false
	}
	present := make(map[string]bool, len(window))
	for _, w := range window {
		present[w] = true
	}

	need := echoWords
	if len(valueWords) < need {
		need = len(valueWords)
	}
	for i := 0; i < need; i++ {
		if !present[valueWords[i]] {
			return false
		}
	}
	return need > 0
}

// suffixPrefixOverlap returns the length of the longest suffix of a that is
// also a prefix of b.
func suffixPrefixOverlap(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for k := n; k > 0; k-- {
		if equalWords(a[len(a)-k:], b[:k]) {
			return k
		}
	}
	return 0
}

func hasBracketPrefix(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, p := range bracketPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// detectSplice compares one insertion against the text that surrounds it.
//
// before/after are the plain-text template neighbours of the tag; value is what
// is about to be inserted. All three are raw (not yet normalized).
func detectSplice(part, tag, before, value, after string) []SpliceFinding {
	var findings []SpliceFinding
	if strings.TrimSpace(value) == "" {
		return nil
	}

	beforeWords := normalizeWords(before)
	valueWords := normalizeWords(value)
	afterWords := normalizeWords(after)
	if len(valueWords) == 0 {
		return nil
	}

	preview := strings.Join(firstN(valueWords, boundaryWords), " ")
	context := strings.Join(lastN(beforeWords, boundaryWords), " ")

	add := func(signal string) {
		findings = append(findings, SpliceFinding{
			Part: part, Tag: tag, Signal: signal, Before: context, Value: preview,
		})
	}

	// Signal A -- the value restates words the template has already said.
	//
	// Longest-suffix-of-template that is a prefix-of-value, rather than a fixed
	// tail-versus-head comparison. The restatement can begin anywhere in the
	// lead-in: the User_Authorization splice repeated the clause from its FIRST
	// word ("Matrix was authorized to perform this work under ..."), which a
	// last-3-versus-first-3 check does not see at all.
	if suffixPrefixOverlap(lastN(beforeWords, contextWindow), valueWords) >= spliceOverlapWords {
		add("overlap")
	}
	// Trailing -- the value pre-states what the template says next.
	if suffixPrefixOverlap(valueWords, firstN(afterWords, contextWindow)) >= spliceOverlapWords {
		add("overlap-trailing")
	}

	// Signal C -- a single duplicated word across the boundary. Cheap, and it
	// catches the unit duplication seen in a delivered report: the template
	// reads "occupies approximately {{SiteAcres}} acres in size" and the value
	// was "1.7 Acres", delivering "1.7 Acres acres".
	//
	// PROPOSED ADDITION, not in the reviewed §5. Flagged for the reviewer: it is
	// evidence-driven but it is the loudest of the four signals, since one
	// repeated common word is a weaker indication than three.
	if len(afterWords) > 0 && valueWords[len(valueWords)-1] == afterWords[0] {
		add("duplicate-word")
	}
	if len(beforeWords) > 0 && valueWords[0] == beforeWords[len(beforeWords)-1] {
		add("duplicate-word")
	}

	// Signal B -- the value opens a new sentence in the middle of one the
	// template already began. This is the rule that catches the topography
	// splice, whose literal overlap is only two words.
	//
	// REFINED with data from the first acceptance run, where the unrefined form
	// produced 73 findings across 67 tags -- almost all of them proper nouns
	// that legitimately start a value: "Alpharetta, GA 30009", "Mr. Ihssan
	// Hashem", "August 2026", "Fulton County". At that volume the detector is
	// noise and gets switched off.
	//
	// The added condition is a LEXICAL ECHO: a capitalised restart only counts
	// as a splice when the value's opening words also appear in the template
	// text just before it. A restatement necessarily echoes what it restates;
	// a proper noun does not. Checked against all 73 findings from that run:
	// every proper-noun case is silenced, and "The topography suggests" -- the
	// one catch that mattered -- still fires, because both "the" and
	// "topography" appear in the lead-in.
	if !hasBracketPrefix(value) && startsNewSentence(value) && !endsSentence(before) &&
		echoesPrecedingText(beforeWords, valueWords) {
		add("sentence-start")
	}

	// Doubled terminal punctuation: value ends "." and the template supplies
	// another one immediately.
	if strings.HasSuffix(strings.TrimSpace(value), ".") && strings.HasPrefix(strings.TrimSpace(after), ".") {
		add("doubled-period")
	}

	return findings
}

// startsNewSentence reports whether a value begins with a capitalised word.
func startsNewSentence(value string) bool {
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsUpper(r) {
			return true
		}
		return false // first rune is not upper-case
	}
	return false
}

// endsSentence reports whether the preceding template text already closed a
// sentence, in which case a capitalised value is correct rather than spliced.
func endsSentence(before string) bool {
	t := strings.TrimRight(before, " \t\n")
	if t == "" {
		return true // start of a block: a capital is expected
	}
	switch t[len(t)-1] {
	case '.', '!', '?', ':', ';':
		return true
	}
	return false
}

// scanPartForSplices checks every tag insertion in one document part.
//
// It runs on the plain text of the un-fractured part BEFORE substitution, so
// the tag positions are still visible and the surrounding text is the
// template's own words.
func scanPartForSplices(part, xmlStr string, replaceMap map[string]interface{}) []SpliceFinding {
	text := docxPartText(xmlStr)
	if text == "" {
		return nil
	}

	// Normalize the payload keys once; the model sometimes braces its own.
	values := make(map[string]string, len(replaceMap))
	for k, v := range replaceMap {
		values[normalizeTagKey(k)] = cleanBracketsAndPunctuation(fmt.Sprint(v))
	}

	var findings []SpliceFinding
	for _, loc := range tagPattern.FindAllStringSubmatchIndex(text, -1) {
		whole, tag := text[loc[0]:loc[1]], text[loc[2]:loc[3]]
		value, ok := values[tag]
		if !ok {
			continue // nothing will be inserted here
		}
		before := text[:loc[0]]
		after := text[loc[0]+len(whole):]
		findings = append(findings, detectSplice(part, tag, before, value, after)...)
	}
	return findings
}

func logSpliceFindings(findings []SpliceFinding) {
	for _, f := range findings {
		slog.Error("SPLICE SUSPECTED AT TAG BOUNDARY",
			"part", f.Part, "tag", f.Tag, "signal", f.Signal,
			"template_before", f.Before, "value_starts", f.Value,
			"note", "detection only; nothing was changed")
	}
}

// --- empty numbered-paragraph removal (§6) ------------------------------------

// emptyNumberedParaPattern matches a whole <w:p> that carries list numbering.
// Non-greedy so it cannot span two paragraphs.
var emptyNumberedParaPattern = regexp.MustCompile(`(?s)<w:p[ >].*?</w:p>`)

// removeEmptyNumberedParagraphs drops auto-numbered paragraphs left with no
// text after substitution.
//
// Sec9_Item1..8 each sit in their own paragraph under numId=8 and DataGaps_Text
// under numId=9. An empty value leaves Word rendering the list number against
// blank space -- there is no literal "2)" string to strip, which is why this
// has to happen structurally.
//
// Guards, both required: the paragraph must carry <w:numPr>, and it must have
// no remaining text content. Together they keep the blast radius inside
// numbered-list entries, where a blank entry is never wanted. A paragraph that
// still holds any text, or any drawing/picture, is left alone.
func removeEmptyNumberedParagraphs(xmlStr string) (string, int) {
	removed := 0
	out := emptyNumberedParaPattern.ReplaceAllStringFunc(xmlStr, func(para string) string {
		if !strings.Contains(para, "<w:numPr>") {
			return para
		}
		if strings.TrimSpace(docxPartText(para)) != "" {
			return para
		}
		// Never drop a paragraph carrying non-text content.
		for _, keep := range []string{"<w:drawing", "<w:pict", "<w:object", "<w:tbl", "<w:br"} {
			if strings.Contains(para, keep) {
				return para
			}
		}
		removed++
		return ""
	})
	return out, removed
}
