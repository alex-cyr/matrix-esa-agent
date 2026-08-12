package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// Pre-screen answers reach the document through the deterministic Go path, so
// they are live ammunition.
//
// Before the validator work these values were only advisory: injectFieldDefaults
// wrote them to key names the template does not contain, and anything that
// reached the report did so because the model happened to copy the answer out
// of the payload. Now they are written straight to {{ParcelID}} and
// {{SiteAcres}}. A pre-filled placeholder is therefore no longer a harmless UI
// convenience -- it is a fabricated fact with a guaranteed path into a signed
// report. The delivered Providence draft proves it: it carries "Parcel ID
// 10-123-456", which is the demo string from the pre-screen form.

// placeholderPrescreenAnswers are the demo strings this service used to ship
// pre-filled for the two fields that write straight to template tags. A value
// matching one is a form default nobody typed, and it is rejected on arrival.
//
// Deliberately narrow. It covers only the tag-writing fields, because rejecting
// by value is only safe where the placeholder is unmistakably synthetic. The
// other two former pre-fills -- a named client entity, and "No ASTs or USTs
// observed" -- are fixed by blanking the form default, NOT by blacklisting the
// string: both are perfectly legitimate answers when an EP actually chooses
// them, and refusing a real selection would be its own silent defect.
//
// Collision risk on the acreage, accepted: a genuine 1.7-acre site whose EP
// types exactly "1.7 Acres" is bracketed rather than accepted. The failure is
// loud, and retyping the bare number "1.7" is what the fragment contract wants
// anyway. A silently fabricated acreage is the worse trade.
var placeholderPrescreenAnswers = map[string]bool{
	"10-123-456": true,
	"1.7 acres":  true,
}

// clientSpecificOptions must never appear as selectable options: hardcoding
// named client entities pinned the question to one project, leaving every other
// EP to choose between the wrong company and "Other".
var clientSpecificOptions = []string{"arkan"}

func isPlaceholderAnswer(s string) bool {
	return placeholderPrescreenAnswers[strings.ToLower(strings.TrimSpace(s))]
}

// prescreenValue turns one pre-screen answer into a document value, or into a
// visible data-gap bracket when there is nothing trustworthy to write.
//
// normalize enforces the tag's fragment contract: the deterministic path owns
// formatting now, so it has to format correctly.
func prescreenValue(raw, label string, normalize func(string) string) string {
	gap := fmt.Sprintf("[MEG DATAGAP: %s]", label)

	t := strings.TrimSpace(raw)
	if t == "" {
		return gap
	}
	if isPlaceholderAnswer(t) {
		slog.Error("PRE-SCREEN PLACEHOLDER REJECTED", "field", label, "value", t,
			"note", "this is a form default, not an answer; writing a data gap instead")
		return gap
	}

	v := strings.TrimSpace(normalize(cleanBracketsAndPunctuation(t)))
	if v == "" {
		return gap
	}
	return v
}

// acreUnitSuffix matches a trailing acreage unit, with or without punctuation.
var acreUnitSuffix = regexp.MustCompile(`(?i)[\s,]*\b(acres?|ac\.?)\s*\.?$`)

// stripAcreUnit enforces the {{SiteAcres}} fragment contract.
//
// The template reads "occupies approximately {{SiteAcres}} acres in size", so
// the value is the bare number. The delivered Providence draft shows what
// happens otherwise: "occupies approximately 1.7 Acres acres in size".
func stripAcreUnit(s string) string {
	return strings.TrimSpace(acreUnitSuffix.ReplaceAllString(strings.TrimSpace(s), ""))
}

// parcelLabelPrefix matches a leading "Parcel ID"-style label.
var parcelLabelPrefix = regexp.MustCompile(`(?i)^\s*parcel\s*(id|no\.?|number|#)?\s*[:#-]?\s*`)

// countySuffix matches a trailing "County" on a county name.
var countySuffix = regexp.MustCompile(`(?i)[\s,]*\bcounty\.?\s*$`)

// modelValueNormalizers enforce fragment contracts on tags the MODEL fills.
//
// The skill states each contract too, but a skill rule is advisory and the
// model has broken this one in a live run. Where the template's surrounding
// words are fixed, the correct formatting is knowable in Go, so Go enforces it.
// Keep this list short: it is for contracts the template itself dictates, not
// for editing the model's prose.
var modelValueNormalizers = map[string]func(string) string{
	// Template: "According to the {{SiteCounty}} County Tax Assessor's
	// website". A value of "Fulton County" delivered "the Fulton County County
	// Tax Assessor's website".
	"SiteCounty": stripCountySuffix,

	// Template: "designates the site as zone {{FloodZone}}." A value of
	// "Zone X" delivered "designates the site as zone Zone X."
	"FloodZone": stripZoneLabel,
}

// zoneLabelPrefix matches a leading "Zone" label on a flood zone designation.
var zoneLabelPrefix = regexp.MustCompile(`(?i)^\s*zone\s+`)

// stripZoneLabel enforces the {{FloodZone}} fragment contract: the bare
// designation, because the template supplies the word "zone" before it.
// "Zone X (unshaded)" becomes "X (unshaded)".
func stripZoneLabel(s string) string {
	return strings.TrimSpace(zoneLabelPrefix.ReplaceAllString(strings.TrimSpace(s), ""))
}

func stripCountySuffix(s string) string {
	return strings.TrimSpace(countySuffix.ReplaceAllString(strings.TrimSpace(s), ""))
}

// applyModelValueNormalizers rewrites model-supplied values whose fragment
// contract the template fixes. Returns the number of values changed.
func applyModelValueNormalizers(m map[string]interface{}) int {
	changed := 0
	for key, normalize := range modelValueNormalizers {
		raw, ok := m[key].(string)
		if !ok {
			continue
		}
		fixed := normalize(raw)
		if fixed == raw || fixed == "" {
			continue
		}
		slog.Warn("FRAGMENT CONTRACT ENFORCED", "key", key, "was", raw, "now", fixed,
			"note", "the template supplies the surrounding words; the model repeated them")
		m[key] = fixed
		changed++
	}
	return changed
}

// stripParcelLabel enforces the {{ParcelID}} fragment contract.
//
// The template reads "(Parcel ID {{ParcelID}})", so a value that repeats the
// label delivers "(Parcel ID Parcel ID 11-0022-33)".
func stripParcelLabel(s string) string {
	return strings.TrimSpace(parcelLabelPrefix.ReplaceAllString(strings.TrimSpace(s), ""))
}
