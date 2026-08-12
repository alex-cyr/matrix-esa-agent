package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The demo strings the form used to pre-fill for the two tag-writing fields.
// The delivered Providence draft carries "Parcel ID 10-123-456" as a result.
// These now write to real tags deterministically, so a placeholder reaching a
// tag is a fabricated fact with a guaranteed path into a signed report.
var shippedPlaceholders = []string{"10-123-456", "1.7 Acres"}

func TestPrescreenPlaceholdersNeverReachATag(t *testing.T) {
	for _, ph := range shippedPlaceholders {
		t.Run(ph, func(t *testing.T) {
			out := injectFieldDefaults(`{}`, map[string]string{
				"parcel_id":    ph,
				"site_acreage": ph,
			}, "")
			if strings.Contains(out, ph) {
				t.Errorf("placeholder %q reached the payload: %s", ph, out)
			}
			if !strings.Contains(out, "[MEG DATAGAP:") {
				t.Errorf("placeholder %q was dropped without a visible bracket: %s", ph, out)
			}
		})
	}
}

// Guard the form itself, not just the injection path: a pre-filled answer is a
// value the EP never typed that the report treats as authoritative.
func TestPrescreenFormShipsNoPrefilledAnswers(t *testing.T) {
	for _, q := range prescreenQuestions() {
		if strings.TrimSpace(q.Answer) != "" {
			t.Errorf("question %q ships a pre-filled answer %q", q.ID, q.Answer)
		}
		for _, opt := range q.Options {
			for _, banned := range clientSpecificOptions {
				if strings.Contains(strings.ToLower(opt), banned) {
					t.Errorf("question %q offers client-specific option %q; "+
						"named entities pin the form to one project", q.ID, opt)
				}
			}
		}
	}
}

// A genuine EP selection must survive. Blanking the default fixed the
// pre-answered field observation; blacklisting the string would have broken the
// answer itself.
func TestLegitimateFieldObservationSelectionSurvives(t *testing.T) {
	if isPlaceholderAnswer("No ASTs or USTs observed") {
		t.Error("a real EP selection is being rejected as a placeholder")
	}
}

func TestAbsentPrescreenAnswerBecomesBracket(t *testing.T) {
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(injectFieldDefaults(`{}`, nil, "")), &got); err != nil {
		t.Fatal(err)
	}
	if got["ParcelID"] != "[MEG DATAGAP: parcel ID]" {
		t.Errorf("ParcelID = %v, want a data-gap bracket", got["ParcelID"])
	}
	if got["SiteAcres"] != "[MEG DATAGAP: site acreage]" {
		t.Errorf("SiteAcres = %v, want a data-gap bracket", got["SiteAcres"])
	}
}

func TestRealPrescreenAnswersSurvive(t *testing.T) {
	var got map[string]interface{}
	out := injectFieldDefaults(`{}`, map[string]string{
		"parcel_id":    "22-4930-0271-021-4",
		"site_acreage": "3.84",
	}, "")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["ParcelID"] != "22-4930-0271-021-4" {
		t.Errorf("ParcelID = %v", got["ParcelID"])
	}
	if got["SiteAcres"] != "3.84" {
		t.Errorf("SiteAcres = %v", got["SiteAcres"])
	}
}

// Fragment contract: the template already supplies the unit and the label.
func TestPrescreenFragmentContracts(t *testing.T) {
	acreage := []struct{ in, want string }{
		{"3.84", "3.84"},
		{"3.84 acres", "3.84"},
		{"3.84 Acres", "3.84"},
		{"3.84 ACRES", "3.84"},
		{"3.84 acre", "3.84"},
		{"3.84 ac.", "3.84"},
		{"3.84 acres.", "3.84"},
		{"12.5, acres", "12.5"},
	}
	for _, tc := range acreage {
		if got := stripAcreUnit(tc.in); got != tc.want {
			t.Errorf("stripAcreUnit(%q) = %q, want %q — the template supplies \"acres\"", tc.in, got, tc.want)
		}
	}

	parcels := []struct{ in, want string }{
		{"22-4930-0271-021-4", "22-4930-0271-021-4"},
		{"Parcel ID 22-4930-0271-021-4", "22-4930-0271-021-4"},
		{"parcel id: 22-4930-0271-021-4", "22-4930-0271-021-4"},
		{"Parcel No. 22-4930", "22-4930"},
		{"Parcel #22-4930", "22-4930"},
	}
	for _, tc := range parcels {
		if got := stripParcelLabel(tc.in); got != tc.want {
			t.Errorf("stripParcelLabel(%q) = %q, want %q — the template supplies \"Parcel ID\"", tc.in, got, tc.want)
		}
	}
}

// The live acceptance run delivered "the Fulton County County Tax Assessor's
// website". The skill now states the contract, but a skill rule is advisory and
// the model already broke this one, so Go enforces it too.
func TestSiteCountyFragmentContract(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Fulton", "Fulton"},
		{"Fulton County", "Fulton"},
		{"Fulton county", "Fulton"},
		{"FULTON COUNTY", "FULTON"},
		{"Fulton County.", "Fulton"},
		{"DeKalb County", "DeKalb"},
	}
	for _, tc := range cases {
		if got := stripCountySuffix(tc.in); got != tc.want {
			t.Errorf("stripCountySuffix(%q) = %q, want %q — the template supplies \"County\"", tc.in, got, tc.want)
		}
	}
}

func TestModelValueNormalizersApplyThroughInjection(t *testing.T) {
	var got map[string]interface{}
	out := injectFieldDefaults(`{"SiteCounty":"Fulton County","OwnerName":"Acme County Holdings"}`, nil, "")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["SiteCounty"] != "Fulton" {
		t.Errorf("SiteCounty = %v, want \"Fulton\"", got["SiteCounty"])
	}
	// Only tags with a declared contract are touched. "County" inside an
	// unrelated value is none of our business.
	if got["OwnerName"] != "Acme County Holdings" {
		t.Errorf("OwnerName was rewritten: %v", got["OwnerName"])
	}
}

// End to end: the doubled unit seen in the delivered draft cannot recur through
// the deterministic path.
func TestAcreageDoesNotDoubleTheUnit(t *testing.T) {
	body := `<w:document><w:body><w:p><w:r>` +
		`<w:t>occupies approximately {{SiteAcres}} acres in size (Parcel ID {{ParcelID}})</w:t>` +
		`</w:r></w:p></w:body></w:document>`

	payload := injectFieldDefaults(`{}`, map[string]string{
		"parcel_id":    "Parcel ID 22-4930",
		"site_acreage": "3.84 Acres",
	}, "")

	var m map[string]string
	var raw map[string]interface{}
	json.Unmarshal([]byte(payload), &raw)
	m = map[string]string{}
	for k, v := range raw {
		m[k] = v.(string)
	}

	got, err := mergeToString(t, body, m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Acres acres") || strings.Contains(got, "acres acres") {
		t.Errorf("unit doubled: %s", got)
	}
	if strings.Contains(got, "Parcel ID Parcel ID") {
		t.Errorf("label doubled: %s", got)
	}
	if !strings.Contains(got, "approximately 3.84 acres in size") {
		t.Errorf("acreage did not render cleanly: %s", got)
	}
	if !strings.Contains(got, "(Parcel ID 22-4930)") {
		t.Errorf("parcel ID did not render cleanly: %s", got)
	}
}
