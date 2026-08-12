package main

import (
	"archive/zip"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

// The canonical tag inventory is parsed from the template at boot. It is the
// source of truth -- not docx_tags.txt (stale, and itself fractured), and not
// the skill markdown, which has drifted repeatedly.
//
// Nothing here stores a count. Adding a tag to the template changes the
// inventory at the next boot; a hardcoded total would be one more thing to
// forget to update.

var (
	tagPattern  = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)
	slotPattern = regexp.MustCompile(`^(Up|Down)\d+_`)
	// wtPattern matches the text content of a <w:t> element.
	wtPattern = regexp.MustCompile(`<w:t[^>]*>([^<]*)</w:t>`)
)

// TagInventory is the set of {{Tag}} names the template actually contains.
type TagInventory struct {
	All         []string // sorted union of every part
	Slots       []string // UpN_* / DownN_* table slots
	Substantive []string // everything else

	set         map[string]bool
	slotSet     map[string]bool
	substantive map[string]bool
}

func (inv *TagInventory) Has(tag string) bool      { return inv.set[tag] }
func (inv *TagInventory) IsSlot(tag string) bool   { return inv.slotSet[tag] }
func (inv *TagInventory) IsSubstantive(t string) bool { return inv.substantive[t] }

// docxPartText returns the visible text of a document part.
//
// Runs are joined with NO separator. Word splits a placeholder across runs, so
// joining with a space turns {{Sec8_10_Spills}} into "{{Sec8_ 10 _Spills}}" --
// which looks exactly like a corrupted template and is not. That mistake
// produced a list of 17 phantom broken tags during design; the regression test
// in tags_test.go exists to keep it from coming back.
func docxPartText(xmlStr string) string {
	var sb strings.Builder
	for _, m := range wtPattern.FindAllStringSubmatch(xmlStr, -1) {
		sb.WriteString(m[1])
	}
	return sb.String()
}

// ParseCanonicalTags reads every processed part of the template and returns the
// union of the tags found, after un-fracturing.
func ParseCanonicalTags(templatePath string) (*TagInventory, error) {
	r, err := zip.OpenReader(templatePath)
	if err != nil {
		return nil, fmt.Errorf("open template %q: %w", templatePath, err)
	}
	defer r.Close()

	found := map[string]bool{}
	for _, f := range r.File {
		if !core.IsProcessedDocxPart(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		// Un-fracture first: the opening "{{" of {{ReportDate}} is two separate
		// runs in header9/header10, and the raw text would not match.
		text := docxPartText(unfractureDocxXML(string(b)))
		for _, m := range tagPattern.FindAllStringSubmatch(text, -1) {
			found[m[1]] = true
		}
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("template %q yielded no {{tags}}: wrong file or broken container layout", templatePath)
	}

	inv := &TagInventory{
		set:         found,
		slotSet:     map[string]bool{},
		substantive: map[string]bool{},
	}
	for tag := range found {
		inv.All = append(inv.All, tag)
		if slotPattern.MatchString(tag) {
			inv.Slots = append(inv.Slots, tag)
			inv.slotSet[tag] = true
		} else {
			inv.Substantive = append(inv.Substantive, tag)
			inv.substantive[tag] = true
		}
	}
	sort.Strings(inv.All)
	sort.Strings(inv.Slots)
	sort.Strings(inv.Substantive)
	return inv, nil
}

// --- skill/template drift ----------------------------------------------------

// A tag named in a skill that the template does not contain is a rule written
// against nothing. Section 8.13 shipped empty for exactly this reason, and the
// schema-rivalry variant below survived two delivered reports.

// driftIgnoreMarker suppresses the warning for a deliberate prose reference --
// a historical note, or a negative reference that exists to stop a mistake
// recurring.
const driftIgnoreMarker = "<!-- tag-check: ignore -->"

// SkillTagRefs are the tags a skill file names, split by how binding the
// reference is.
type SkillTagRefs struct {
	File string
	// Headings are rule targets: a tag named in a numbered rule heading. A
	// heading pointing at a nonexistent tag is never legitimate.
	Headings []string
	// Schema are keys in the ```json payload block. Also machine-consumed: a
	// dead key sitting beside its live twin lets the model fill either one.
	Schema []string
	// Prose is everything else -- explanation, examples, historical notes.
	Prose []string
}

var (
	ruleHeadingPattern = regexp.MustCompile(`^\s*\d+\.\s+\*\*`)
	jsonFencePattern   = regexp.MustCompile("^\\s*```")
)

// collectSkillTagRefs classifies every {{Tag}} reference in a skill file.
func collectSkillTagRefs(path, content string) SkillTagRefs {
	refs := SkillTagRefs{File: path}
	inJSON := false

	for _, line := range strings.Split(content, "\n") {
		if jsonFencePattern.MatchString(line) {
			inJSON = !inJSON
			continue
		}
		matches := tagPattern.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		var bucket *[]string
		switch {
		case inJSON:
			bucket = &refs.Schema
		case ruleHeadingPattern.MatchString(line):
			bucket = &refs.Headings
		case strings.Contains(line, driftIgnoreMarker):
			continue // deliberate reference, explicitly marked
		default:
			bucket = &refs.Prose
		}
		for _, m := range matches {
			*bucket = append(*bucket, m[1])
		}
	}

	dedupe(&refs.Headings)
	dedupe(&refs.Schema)
	dedupe(&refs.Prose)
	return refs
}

func dedupe(xs *[]string) {
	seen := map[string]bool{}
	out := (*xs)[:0]
	for _, x := range *xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	*xs = out
}

// DriftReport is the per-skill result of checking named tags against the
// template.
type DriftReport struct {
	File string
	// Binding are unknown tags in rule headings or the JSON schema block.
	// These are errors: both are machine-consumed target text.
	BindingHeadings []string
	BindingSchema   []string
	// Prose are unknown tags in explanatory text. Warnings only.
	Prose []string
}

func (d DriftReport) HasBinding() bool {
	return len(d.BindingHeadings) > 0 || len(d.BindingSchema) > 0
}
func (d DriftReport) Empty() bool { return !d.HasBinding() && len(d.Prose) == 0 }

// CheckSkillTagDrift reports tags a skill names that the template lacks.
func CheckSkillTagDrift(inv *TagInventory, path, content string) DriftReport {
	refs := collectSkillTagRefs(path, content)
	report := DriftReport{File: path}

	for _, t := range refs.Headings {
		if !inv.Has(t) {
			report.BindingHeadings = append(report.BindingHeadings, t)
		}
	}
	for _, t := range refs.Schema {
		if !inv.Has(t) {
			report.BindingSchema = append(report.BindingSchema, t)
		}
	}
	for _, t := range refs.Prose {
		if !inv.Has(t) {
			report.Prose = append(report.Prose, t)
		}
	}
	return report
}
