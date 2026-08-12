package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

// --- schema version ----------------------------------------------------------

// The contract is integer-versioned and additive-only. A version this build
// does not know must be REJECTED, never best-effort parsed: Report Studio and
// checklist v2 both bind to this, and a silently half-understood intake is how
// a disclosed field goes missing with nobody noticing.
func TestIntakeRejectsUnknownSchemaVersion(t *testing.T) {
	in := &Intake{SchemaVersion: 99}
	err := in.Validate()
	if err == nil {
		t.Fatal("schema_version 99 was accepted; unknown versions must be refused")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("error does not name the offending version: %v", err)
	}
}

func TestIntakeRequiresSchemaVersion(t *testing.T) {
	if err := (&Intake{}).Validate(); err == nil {
		t.Fatal("a missing schema_version was accepted")
	}
}

func TestIntakeCurrentVersionIsAccepted(t *testing.T) {
	if err := (&Intake{SchemaVersion: intakeSchemaVersion}).Validate(); err != nil {
		t.Fatalf("this build's own version was rejected: %v", err)
	}
}

// An absent intake is legal — the legacy flat map still works during the
// transition — and must not panic anywhere on the nil path.
func TestNilIntakeIsLegalAndNilSafe(t *testing.T) {
	var in *Intake
	if err := in.Validate(); err != nil {
		t.Fatalf("absent intake rejected: %v", err)
	}
	if got := composeAuthorization(in.authorization()); got != authorizationDataGap {
		t.Errorf("nil intake authorization = %q, want the data-gap bracket", got)
	}
	if got := userKnowledgeBlock(in); got != "" {
		t.Errorf("nil intake rendered a knowledge block: %q", got)
	}
	if got := intakeToAnswers(in); len(got) != 0 {
		t.Errorf("nil intake produced answers: %v", got)
	}
}

// The other half of that distinction, and the more important half. An intake
// that WAS supplied but left the required obligations blank must still emit the
// block, saying NOT ANSWERED — suppressing it is how a required disclosure
// silently vanishes, which is the worst failure available in this phase.
func TestSuppliedIntakeWithBlankObligationsStillEmitsTheBlock(t *testing.T) {
	got := userKnowledgeBlock(&Intake{SchemaVersion: 1})
	if got == "" {
		t.Fatal("a supplied intake with blank obligations emitted nothing")
	}
	if strings.Count(got, "NOT ANSWERED") != 3 {
		t.Errorf("expected all three obligations flagged unanswered:\n%s", got)
	}
}

// --- house date --------------------------------------------------------------

func TestFormatHouseDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-07-06", "July 6, 2026"},
		{"2026-07-06T00:00:00Z", "July 6, 2026"},
		{"2026/07/06", "July 6, 2026"},
		{"07/06/2026", "July 6, 2026"},
		{"", ""},
		{"   ", ""},
		// Unparseable input becomes empty, so it surfaces as a data gap
		// upstream rather than a wrong date in a signed report.
		{"sometime last July", ""},
		{"6 July 2026", ""},
	}
	for _, tc := range cases {
		if got := formatHouseDate(tc.in); got != tc.want {
			t.Errorf("formatHouseDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- authorization composition ------------------------------------------------

// FRAGMENT CONTRACT. The template prints, in two places:
//
//	"This work was performed in accordance with {{User_Authorization}}."
//
// so the value continues that clause: it must not restate the lead-in and must
// not supply a trailing period. Both defects shipped once, together, as
// "in accordance with Matrix Engineering Group was authorized under signed
// proposal dated July 06, 2026.." in a delivered report.
func TestComposeAuthorizationRespectsTheFragmentContract(t *testing.T) {
	values := []string{
		composeAuthorization(Authorization{Basis: "signed_proposal", ProposalDate: "2026-07-06"}),
		composeAuthorization(Authorization{Basis: "purchase_order", PONumber: "4501", PORecipient: "Jane Doe", PODate: "2026-07-06"}),
		composeAuthorization(Authorization{Basis: "other", OtherDescription: "a verbal authorization confirmed by email"}),
	}
	for _, v := range values {
		if strings.HasSuffix(v, ".") {
			t.Errorf("%q ends with a period; the template supplies it", v)
		}
		if strings.Contains(strings.ToLower(v), "in accordance with") {
			t.Errorf("%q restates the template lead-in", v)
		}
		if strings.Contains(strings.ToLower(v), "this work was performed") {
			t.Errorf("%q restates the template lead-in", v)
		}
	}
}

func TestComposeAuthorizationShapes(t *testing.T) {
	cases := []struct {
		name string
		in   Authorization
		want string
	}{
		{
			"signed proposal, one date",
			Authorization{Basis: "signed_proposal", ProposalDate: "2026-07-06"},
			"our proposal dated July 6, 2026",
		},
		{
			"signed proposal, separate approval",
			Authorization{Basis: "signed_proposal", ProposalDate: "2026-07-06", ApprovalDate: "2026-07-09"},
			"our proposal dated July 6, 2026 and approved on July 9, 2026",
		},
		{
			"purchase order, full",
			Authorization{Basis: "purchase_order", PONumber: "PO-4501", PORecipient: "Jane Doe", PODate: "2026-07-06"},
			"Purchase Order PO-4501, emailed to Jane Doe on July 6, 2026",
		},
		{
			"purchase order, number only",
			Authorization{Basis: "purchase_order", PONumber: "PO-4501"},
			"Purchase Order PO-4501",
		},
		{
			// The EP owns this wording; it is used verbatim.
			"other, verbatim",
			Authorization{Basis: "other", OtherDescription: "a task order issued under our master services agreement"},
			"a task order issued under our master services agreement",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeAuthorization(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An unanswered authorization is a data gap. It is NEVER inferred from the
// uploaded proposal — this field exists precisely to replace that inference, so
// falling back to it would defeat the field.
func TestUnansweredAuthorizationIsADataGapNeverAnInference(t *testing.T) {
	gaps := []Authorization{
		{},
		{Basis: "signed_proposal"},                    // no date
		{Basis: "signed_proposal", ProposalDate: "??"}, // unparseable date
		{Basis: "purchase_order"},                     // no number
		{Basis: "other"},                              // no description
		{Basis: "carrier pigeon"},                     // unknown basis
	}
	for _, a := range gaps {
		if got := composeAuthorization(a); got != authorizationDataGap {
			t.Errorf("composeAuthorization(%+v) = %q, want %q", a, got, authorizationDataGap)
		}
	}
}

// The tag is Go-guaranteed: written on every run, as a visible bracket when
// unanswered, so it can never ship blank and can never carry a model splice.
func TestUserAuthorizationIsAlwaysWritten(t *testing.T) {
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(injectFieldDefaults(`{}`, nil, "")), &got); err != nil {
		t.Fatal(err)
	}
	if got["User_Authorization"] != authorizationDataGap {
		t.Errorf("User_Authorization = %v, want %q", got["User_Authorization"], authorizationDataGap)
	}
}

// A model-supplied value must not survive: the model reading authorization out
// of the uploaded proposal is the exact behaviour this field replaces.
func TestModelAuthorizationValueIsOverwritten(t *testing.T) {
	var got map[string]interface{}
	payload := `{"User_Authorization":"Matrix Engineering Group was authorized under signed proposal dated July 06, 2026."}`
	out := injectFieldDefaults(payload, map[string]string{
		"_composed_authorization": "our proposal dated July 6, 2026",
	}, "")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["User_Authorization"] != "our proposal dated July 6, 2026" {
		t.Errorf("User_Authorization = %v; the composed value must win", got["User_Authorization"])
	}
}

// --- site county --------------------------------------------------------------

// Intake outranks the model when supplied, and the fragment contract still
// applies to the EP's own typing.
func TestIntakeCountyOverridesTheModelAndStripsTheSuffix(t *testing.T) {
	var got map[string]interface{}
	out := injectFieldDefaults(`{"SiteCounty":"Cobb"}`, map[string]string{"site_county": "Fulton County"}, "")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["SiteCounty"] != "Fulton" {
		t.Errorf("SiteCounty = %v, want \"Fulton\"", got["SiteCounty"])
	}
}

// SiteCounty is deliberately NOT Go-guaranteed. With no intake answer the
// model's value stands — bracketing it would demand the EP type a fact the EDR
// package states plainly, which the prescreen design principle forbids.
func TestAbsentCountyAnswerLeavesTheModelValueAlone(t *testing.T) {
	var got map[string]interface{}
	out := injectFieldDefaults(`{"SiteCounty":"Fulton County"}`, nil, "")
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got["SiteCounty"] != "Fulton" {
		t.Errorf("SiteCounty = %v; the model value should stand, normalized", got["SiteCounty"])
	}
	if strings.Contains(out, "[MEG DATAGAP: county") {
		t.Error("an unanswered county was bracketed; it is not Go-guaranteed")
	}
}

// --- user actual knowledge -----------------------------------------------------

// The tri-state is the whole point. An unanswered 40 CFR 312 obligation must
// never render as "the user reports none known" — that is a legal disclosure
// the user never made, in a signed report.
func TestUnansweredObligationNeverReadsAsNoneKnown(t *testing.T) {
	// Other alone is enough to produce a block; the three obligations are
	// untouched, i.e. never asked.
	got := RenderUserKnowledge(UserKnowledge{Other: "The rear shed was a small engine repair shop."})
	if got == "" {
		t.Fatal("volunteered information produced no block")
	}
	if strings.Contains(got, "none known") {
		t.Errorf("an unanswered obligation rendered as \"none known\":\n%s", got)
	}
	for _, want := range []string{"NOT ANSWERED", "Environmental liens", "Activity and Use Limitations", "Specialized knowledge"} {
		if !strings.Contains(got, want) {
			t.Errorf("block is missing %q:\n%s", want, got)
		}
	}
}

func TestAnsweredNoRendersAsNoneKnown(t *testing.T) {
	got := RenderUserKnowledge(UserKnowledge{
		EnvironmentalLiens:   Knowledge{Known: boolp(false)},
		AULKnown:             Knowledge{Known: boolp(false)},
		SpecializedKnowledge: Knowledge{Known: boolp(false)},
	})
	if strings.Contains(got, "NOT ANSWERED") {
		t.Errorf("an answered obligation rendered as unanswered:\n%s", got)
	}
	if strings.Count(got, "none known") != 3 {
		t.Errorf("expected three \"none known\" lines:\n%s", got)
	}
}

// A disclosed condition that silently vanishes is the worst failure this phase
// could introduce: it is user-supplied evidence of a condition.
func TestDisclosedConditionReachesTheBlockWithItsDetail(t *testing.T) {
	got := RenderUserKnowledge(UserKnowledge{
		AULKnown: Knowledge{Known: boolp(true), Detail: "A deed restriction bars groundwater extraction."},
	})
	if !strings.Contains(got, "deed restriction bars groundwater extraction") {
		t.Errorf("the disclosed detail vanished:\n%s", got)
	}
	if !strings.Contains(got, "YES") {
		t.Errorf("the disclosure is not marked YES:\n%s", got)
	}
}

// Disclosed with no detail must still not vanish — it becomes a data gap the EP
// has to close, not silence.
func TestDisclosureWithoutDetailBecomesABracketNotSilence(t *testing.T) {
	got := RenderUserKnowledge(UserKnowledge{
		EnvironmentalLiens: Knowledge{Known: boolp(true)},
	})
	if !strings.Contains(got, "MEG DATAGAP") {
		t.Errorf("a detail-less disclosure produced no bracket instruction:\n%s", got)
	}
}

// Attribution is mandatory: a lien the EP disclosed and a lien Matrix
// discovered are different claims in a signed report.
func TestKnowledgeBlockCarriesAttributionAndItsLabel(t *testing.T) {
	got := RenderUserKnowledge(UserKnowledge{EnvironmentalLiens: Knowledge{Known: boolp(false)}})
	if !strings.Contains(got, userKnowledgeBlockHeader) {
		t.Errorf("block is unlabelled, so the skill cannot be pointed at it:\n%s", got)
	}
	if !strings.Contains(got, "not a Matrix finding") {
		t.Errorf("block lacks the attribution instruction:\n%s", got)
	}
}

// --- intake flattening ----------------------------------------------------------

// Parcel IDs are printed verbatim per the house rule — spaces, dots and dashes
// preserved, no format enforcement — and multi-parcel is the common case.
func TestParcelIDsJoinVerbatim(t *testing.T) {
	in := &Intake{SchemaVersion: 1}
	in.Site.ParcelIDs = []string{"22-4640-1106-064-9", "22 4640 1106 065 6", "12.345.67"}
	got := intakeToAnswers(in)["parcel_id"]
	want := "22-4640-1106-064-9, 22 4640 1106 065 6, 12.345.67"
	if got != want {
		t.Errorf("parcel_id = %q, want %q", got, want)
	}
}

// INDEPENDENT VERIFIER. The form and the structured contract must converge on
// one key per fact. This checks intakeToAnswers against the QUESTION LIST, not
// against itself — two spellings of the same fact is exactly the divergence
// that left the EP's typed acreage stranded on a dead key.
func TestIntakeKeysMatchTheQuestionIDs(t *testing.T) {
	// Keys that legitimately have no question: the UI supplies them separately
	// or Go composes them.
	noQuestion := map[string]bool{
		"project_number": true, // supplied by the project header, not the form
		"client_contact": true, // client block is its own later item
		"client_address": true, // ditto
	}

	// No checklist detected, so the site-visit questions are in play. That is
	// the case where every sv_* key has a matching question; when a checklist
	// IS present the questions vanish and the keys simply go unfilled.
	ids := map[string]bool{}
	for _, q := range append(staticCoreQuestions(), siteVisitQuestions(nil)...) {
		ids[q.ID] = true
	}

	full := &Intake{SchemaVersion: 1}
	full.Project.Number = "303315"
	full.Client.EntityName = "Example Holdings, LLC"
	full.Client.ContactName = "Pat Example"
	full.Client.AddressLines = []string{"1 Example Way", "Atlanta, GA 30303"}
	full.Site.ParcelIDs = []string{"22-4640-1106-064-9"}
	full.Site.Acreage = "3.84"
	full.Site.County = "Fulton"
	full.Site.Descriptor = "Residential Redevelopment"
	full.SiteVisit = SiteVisit{
		AccessFrom:       "Providence Road",
		AccessVia:        "a gravel drive",
		CurrentUse:       "vacant wooded land",
		ObservedFeatures: "a paved-over junction box near the north boundary",
		ASTUSTObserved:   "No ASTs or USTs observed",
	}

	for k := range intakeToAnswers(full) {
		if !ids[k] && !noQuestion[k] {
			t.Errorf("intakeToAnswers emits %q, which is neither a question ID nor a declared "+
				"question-less key — the form and the contract have diverged on this fact", k)
		}
	}

	// And the reverse direction: a question whose answer no Go path reads is a
	// question that quietly does nothing.
	readsAnswer := map[string]bool{
		"authorization_basis":             true, // assembled into intake.authorization by the client
		"authorization_proposal_date":     true,
		"authorization_approval_date":     true,
		"authorization_po_number":         true,
		"authorization_po_recipient":      true,
		"authorization_po_date":           true,
		"authorization_other_description": true,
		"user_liens":                      true, // assembled into intake.user_knowledge
		"user_auls":                       true,
		"user_specialized_knowledge":      true,
		"user_other":                      true,
	}
	emitted := intakeToAnswers(full)
	for id := range ids {
		if _, ok := emitted[id]; !ok && !readsAnswer[id] {
			t.Errorf("question %q has no consumer: nothing in intakeToAnswers emits it "+
				"and it is not declared as client-assembled", id)
		}
	}
}
