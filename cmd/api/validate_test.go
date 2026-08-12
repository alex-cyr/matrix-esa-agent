package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"os"
	"strings"
	"testing"
)

// testInventory builds a small inventory without needing the real template.
func testInventory(t *testing.T, tags ...string) *TagInventory {
	t.Helper()
	inv := &TagInventory{
		set:         map[string]bool{},
		slotSet:     map[string]bool{},
		substantive: map[string]bool{},
	}
	for _, tag := range tags {
		inv.set[tag] = true
		inv.All = append(inv.All, tag)
		if slotPattern.MatchString(tag) {
			inv.Slots = append(inv.Slots, tag)
			inv.slotSet[tag] = true
		} else {
			inv.Substantive = append(inv.Substantive, tag)
			inv.substantive[tag] = true
		}
	}
	return inv
}

func decode(t *testing.T, js string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	return m
}

// Tier A: table slots are fixed cells. Absent ones fill silently and are never
// re-prompted or bracketed.
func TestValidatorAutoFillsSlotKeys(t *testing.T) {
	inv := testInventory(t, "Up7_Name", "Down3_Address", "OwnerName")

	reprompted := false
	out, res := validateAndRepair(context.Background(), `{"OwnerName":"Acme LLC"}`, inv,
		func(context.Context, []string) (map[string]string, error) {
			reprompted = true
			return nil, nil
		}, true)

	got := decode(t, out)
	if got["Up7_Name"] != "" || got["Down3_Address"] != "" {
		t.Errorf("slot keys not auto-filled: %v", got)
	}
	if res.SlotsAutoFilled != 2 {
		t.Errorf("SlotsAutoFilled = %d, want 2", res.SlotsAutoFilled)
	}
	if reprompted {
		t.Error("slot keys must never trigger a re-prompt")
	}
	if len(res.DataGapped) != 0 {
		t.Errorf("slot keys must never be bracketed, got %v", res.DataGapped)
	}
}

// Tier B: a missing substantive key is re-prompted once, and bracketed if the
// re-prompt does not recover it.
func TestValidatorRepromptsThenBracketsMandatoryKey(t *testing.T) {
	inv := testInventory(t, "Sec9_Item3_Wetlands", "Sec9_Item4_Flood", "OwnerName")

	var asked []string
	out, res := validateAndRepair(context.Background(),
		`{"OwnerName":"Acme LLC","Sec9_Item4_Flood":"The site lies in Zone X."}`, inv,
		func(_ context.Context, missing []string) (map[string]string, error) {
			asked = missing
			return map[string]string{}, nil // model recovers nothing
		}, true)

	if len(asked) != 1 || asked[0] != "Sec9_Item3_Wetlands" {
		t.Fatalf("re-prompt asked for %v, want [Sec9_Item3_Wetlands]", asked)
	}
	got := decode(t, out)
	if got["Sec9_Item3_Wetlands"] != "[MEG DATAGAP: Sec9_Item3_Wetlands]" {
		t.Errorf("unrecovered key not bracketed: %v", got["Sec9_Item3_Wetlands"])
	}
	if got["Sec9_Item4_Flood"] != "The site lies in Zone X." {
		t.Error("an existing correct value was disturbed")
	}
	if !res.RepromptRan {
		t.Error("RepromptRan should be true")
	}
}

func TestValidatorRecoversFromReprompt(t *testing.T) {
	inv := testInventory(t, "Sec9_Item3_Wetlands")

	out, res := validateAndRepair(context.Background(), `{}`, inv,
		func(_ context.Context, missing []string) (map[string]string, error) {
			return map[string]string{"Sec9_Item3_Wetlands": "No wetlands were mapped."}, nil
		}, true)

	if got := decode(t, out)["Sec9_Item3_Wetlands"]; got != "No wetlands were mapped." {
		t.Errorf("recovered value not applied: %v", got)
	}
	if len(res.Recovered) != 1 || len(res.DataGapped) != 0 {
		t.Errorf("recovered=%v datagapped=%v", res.Recovered, res.DataGapped)
	}
}

// The anti-fabrication clause tells the model that "" is a correct answer. A
// mandatory key answered "" must still be bracketed, never left blank.
func TestValidatorBracketsEmptyRepromptAnswerForMandatoryKey(t *testing.T) {
	inv := testInventory(t, "DataGaps_Text")

	out, _ := validateAndRepair(context.Background(), `{}`, inv,
		func(context.Context, []string) (map[string]string, error) {
			return map[string]string{"DataGaps_Text": ""}, nil
		}, true)

	if got := decode(t, out)["DataGaps_Text"]; got != "[MEG DATAGAP: DataGaps_Text]" {
		t.Errorf("empty honest answer must become a bracket, got %v", got)
	}
}

// Empty-string semantics: mandatory keys treat "" as missing; other substantive
// keys accept it as deliberate -- but it must be counted and listed.
func TestValidatorEmptyStringSemantics(t *testing.T) {
	inv := testInventory(t, "DataGaps_Text", "Sanborn_Summary")

	out, res := validateAndRepair(context.Background(),
		`{"DataGaps_Text":"","Sanborn_Summary":""}`, inv,
		func(context.Context, []string) (map[string]string, error) { return nil, nil }, true)

	got := decode(t, out)
	if got["DataGaps_Text"] != "[MEG DATAGAP: DataGaps_Text]" {
		t.Errorf("mandatory blank not bracketed: %v", got["DataGaps_Text"])
	}
	if got["Sanborn_Summary"] != "" {
		t.Errorf("non-mandatory blank should stay empty, got %v", got["Sanborn_Summary"])
	}
	if len(res.DeliberateBlanks) != 1 || res.DeliberateBlanks[0] != "Sanborn_Summary" {
		t.Errorf("deliberate blank not recorded: %v", res.DeliberateBlanks)
	}
	if res.DeliberateBlankCount() != 1 {
		t.Errorf("DeliberateBlankCount = %d, want 1", res.DeliberateBlankCount())
	}
}

// Go fills these after the model runs; asking the model for them would be a
// spurious re-prompt, and bracketing them would put [MEG DATAGAP] where a real
// value is about to land.
func TestValidatorIgnoresGoSuppliedKeys(t *testing.T) {
	inv := testInventory(t, "ReportDate", "DraftNote", "ProjectNo", "OwnerName")

	var asked []string
	out, res := validateAndRepair(context.Background(), `{"OwnerName":"Acme"}`, inv,
		func(_ context.Context, missing []string) (map[string]string, error) {
			asked = missing
			return nil, nil
		}, true)

	if len(asked) != 0 {
		t.Errorf("Go-supplied keys were re-prompted: %v", asked)
	}
	if len(res.DataGapped) != 0 {
		t.Errorf("Go-supplied keys were bracketed: %v", res.DataGapped)
	}
	if _, present := decode(t, out)["ReportDate"]; present {
		t.Error("validator should leave Go-supplied keys alone entirely")
	}
}

// Addition B: one definition, enforced. If someone teaches injectFieldDefaults
// to write a new key without adding it to goSuppliedKeys, the result is a
// spurious re-prompt or a silent blank -- so the two cannot be allowed to drift.
func TestGoSuppliedKeysMatchInjectFieldDefaults(t *testing.T) {
	// Answers that exercise every branch that writes a key.
	answers := map[string]string{
		"project_number": "MEG-302858",
		"parcel_id":      "11-0022-33",
		"site_acreage":   "4.2 Acres",
	}
	written := decode(t, injectFieldDefaults(`{}`, answers, "note"))

	for key := range written {
		if !goSuppliedKeys[key] {
			t.Errorf("injectFieldDefaults writes %q but it is missing from goSuppliedKeys — "+
				"the validator will re-prompt the model for a key Go already fills", key)
		}
	}
	for key := range goSuppliedKeys {
		if _, ok := written[key]; !ok {
			t.Errorf("goSuppliedKeys lists %q but injectFieldDefaults never writes it — "+
				"the validator will skip a key nothing fills, leaving a silent blank", key)
		}
	}
}

func TestValidatorNormalizesBracedKeys(t *testing.T) {
	inv := testInventory(t, "SiteAcres")

	out, res := validateAndRepair(context.Background(), `{"{{SiteAcres}}":"1.7 Acres"}`, inv,
		func(context.Context, []string) (map[string]string, error) {
			t.Error("a braced key is present, not missing")
			return nil, nil
		}, true)

	if got := decode(t, out)["SiteAcres"]; got != "1.7 Acres" {
		t.Errorf("braced key not normalized: %v", got)
	}
	if len(res.DataGapped) != 0 {
		t.Errorf("braced key was treated as missing: %v", res.DataGapped)
	}
}

func TestValidatorRecordsUnknownKeys(t *testing.T) {
	inv := testInventory(t, "OwnerName")

	_, res := validateAndRepair(context.Background(),
		`{"OwnerName":"Acme","InventedByModel":"x"}`, inv, nil, true)

	if len(res.UnknownKeys) != 1 || res.UnknownKeys[0] != "InventedByModel" {
		t.Errorf("UnknownKeys = %v, want [InventedByModel]", res.UnknownKeys)
	}
}

// The validator never fails a request. A re-prompt error degrades to brackets.
func TestValidatorSurvivesRepromptFailure(t *testing.T) {
	inv := testInventory(t, "Sec9_Item1_SiteInfo")

	out, res := validateAndRepair(context.Background(), `{}`, inv,
		func(context.Context, []string) (map[string]string, error) {
			return nil, errors.New("429 quota exhausted")
		}, true)

	if res.RepromptErr == "" {
		t.Error("re-prompt error not recorded")
	}
	if got := decode(t, out)["Sec9_Item1_SiteInfo"]; got != "[MEG DATAGAP: Sec9_Item1_SiteInfo]" {
		t.Errorf("failed re-prompt must still bracket, got %v", got)
	}
}

// Unparseable input passes through rather than being destroyed.
func TestValidatorPassesThroughUnparseablePayload(t *testing.T) {
	inv := testInventory(t, "OwnerName")
	out, _ := validateAndRepair(context.Background(), `not json at all`, inv, nil, true)
	if out != `not json at all` {
		t.Errorf("payload was altered: %q", out)
	}
}

// --- re-prompt construction ---------------------------------------------------

func TestRepromptPayloadCarriesAntiFabricationClause(t *testing.T) {
	got := buildRepromptPayload("SYNTHESIS BODY", "FIRST OUTPUT", []string{"Sec9_Item2_Topo"})

	for _, want := range []string{
		"SYNTHESIS BODY",
		"FIRST OUTPUT",
		"Sec9_Item2_Topo",
		`return "" for that key`,
		"An empty value is a correct answer; an invented one is not.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("re-prompt payload missing %q", want)
		}
	}
}

func TestRepromptResponseKeepsOnlyRequestedKeys(t *testing.T) {
	got, err := parseRepromptResponse(
		`{"Sec9_Item2_Topo":"recovered","OwnerName":"OVERWRITE ATTEMPT","{{Sec9_Item3_Wetlands}}":"also recovered"}`,
		[]string{"Sec9_Item2_Topo", "Sec9_Item3_Wetlands"})
	if err != nil {
		t.Fatal(err)
	}
	if got["Sec9_Item2_Topo"] != "recovered" {
		t.Errorf("requested key missing: %v", got)
	}
	if got["Sec9_Item3_Wetlands"] != "also recovered" {
		t.Errorf("braced requested key not normalized: %v", got)
	}
	if _, present := got["OwnerName"]; present {
		t.Error("a re-prompt must not be able to overwrite values that were already correct")
	}
}

func TestRepromptResponseRejectsNonJSON(t *testing.T) {
	if _, err := parseRepromptResponse("I'm sorry, I cannot do that.", []string{"X"}); err == nil {
		t.Error("expected an error for a non-JSON re-prompt reply")
	}
}

// --- against the real template -------------------------------------------------

func TestGoSuppliedKeysExistInTemplate(t *testing.T) {
	tpl := filepath.Join("..", "..", "knowledge", "ESA_PHASE_I_Template.docx")
	if _, err := os.Stat(tpl); err != nil {
		t.Skip("firm template not available")
	}
	inv, err := ParseCanonicalTags(tpl)
	if err != nil {
		t.Fatal(err)
	}
	// No exemptions. A Go-supplied key that is not a template tag is a value
	// written into the void -- which is exactly how the EP's typed site acreage
	// went missing from every report until this test first ran.
	for key := range goSuppliedKeys {
		if !inv.Has(key) {
			t.Errorf("goSuppliedKeys names %q, which is not a template tag: "+
				"anything written to it matches nothing at merge", key)
		}
	}
}

func TestSubstantiveMandatoryKeysExistInTemplate(t *testing.T) {
	tpl := filepath.Join("..", "..", "knowledge", "ESA_PHASE_I_Template.docx")
	if _, err := os.Stat(tpl); err != nil {
		t.Skip("firm template not available")
	}
	inv, err := ParseCanonicalTags(tpl)
	if err != nil {
		t.Fatal(err)
	}
	for key := range substantiveMandatory {
		if !inv.Has(key) {
			t.Errorf("substantiveMandatory names %q, which is not a template tag — "+
				"it would be bracketed on every run and never render", key)
		}
	}
}

// --- gradient guard -------------------------------------------------------------
//
// The geospatial rule makes GeoCheck the primary source and requires low
// confidence to be flagged. The model gambled past that rule in both
// directions on live runs: it fabricated "south/southwesterly" for a parcel the
// EP has established drains east, then stated the correct direction on the next
// run — unflagged and unsourced both times. Right-by-luck is what this guard
// removes.

func TestGradientGuardBracketsBareDirectionWithoutGeoCheck(t *testing.T) {
	inv := testInventory(t, "GWFlowDir")

	out, res := validateAndRepair(context.Background(),
		`{"GWFlowDir":"easterly"}`, inv, nil, false)

	got := decode(t, out)["GWFlowDir"]
	if got != gradientNoSourceBracket {
		t.Errorf("bare direction with no GeoCheck source survived: %v", got)
	}
	if !res.GradientGuarded {
		t.Error("GradientGuarded not recorded")
	}
}

func TestGradientGuardAllowsBareDirectionWithGeoCheck(t *testing.T) {
	inv := testInventory(t, "GWFlowDir")

	out, res := validateAndRepair(context.Background(),
		`{"GWFlowDir":"easterly"}`, inv, nil, true)

	if got := decode(t, out)["GWFlowDir"]; got != "easterly" {
		t.Errorf("a GeoCheck-sourced direction was altered: %v", got)
	}
	if res.GradientGuarded {
		t.Error("GradientGuarded set when a source existed")
	}
}

// An existing flag is the rule already working. Never double-bracket it.
func TestGradientGuardPassesExistingBracketThrough(t *testing.T) {
	inv := testInventory(t, "GWFlowDir")
	const flagged = "[EP VERIFY: groundwater flow direction]"

	out, res := validateAndRepair(context.Background(),
		`{"GWFlowDir":"`+flagged+`"}`, inv, nil, false)

	if got := decode(t, out)["GWFlowDir"]; got != flagged {
		t.Errorf("existing bracket was rewritten: %v", got)
	}
	if res.GradientGuarded {
		t.Error("GradientGuarded set for a value that was already flagged")
	}
}

func TestGeoCheckGradientDetection(t *testing.T) {
	present := []string{
		"GeoCheck Topographic Gradient: East",
		"geocheck summary — general topographic slope is to the east",
		"General Topographic Gradient: SW",
	}
	for _, s := range present {
		if !GeoCheckGradientPresent(s) {
			t.Errorf("GeoCheckGradientPresent(%q) = false, want true", s)
		}
	}

	absent := []string{
		"",
		"no gradient information was located in the package",
		// The parser's explicit marker must win over any incidental mention.
		"GeoCheck Topographic Gradient discussion. [MEG DATAGAP: NO GEOCHECK GRADIENT — EP TO VERIFY GROUNDWATER FLOW DIRECTION]",
	}
	for _, s := range absent {
		if GeoCheckGradientPresent(s) {
			t.Errorf("GeoCheckGradientPresent(%q) = true, want false", s)
		}
	}
}
