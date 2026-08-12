package main

import (
	"fmt"
	"strings"
	"time"
)

// The intake contract.
//
// This schema IS the API, not a form payload. The web form is its first client;
// Report Studio will POST the same JSON, and site-recon checklist v2 feeds the
// same shape. Design decisions here outlive the form.
//
// Versioning: integer, additive-only. Within a version, fields are never
// removed or repurposed — only added. A breaking change is a new integer. The
// server accepts known versions ONLY and rejects unknown ones loudly; it never
// best-effort parses something it does not understand, because a silently
// half-understood intake is how a field goes missing without anyone noticing.

const intakeSchemaVersion = 1

// supportedIntakeVersions is the allowlist. Add to it deliberately.
var supportedIntakeVersions = map[int]bool{1: true}

type Intake struct {
	SchemaVersion int `json:"schema_version"`

	Project struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	} `json:"project"`

	Authorization Authorization `json:"authorization"`
	Client        ClientBlock   `json:"client"`
	Site          SiteBlock     `json:"site"`
	UserKnowledge UserKnowledge `json:"user_knowledge"`
	SiteVisit     SiteVisit     `json:"site_visit"`

	SpecialInstructions string `json:"special_instructions"`
}

// Authorization replaces model inference about how the work was authorized.
// The field exists precisely to stop the model reading it out of the uploaded
// proposal, so an unanswered authorization is a data gap, never an inference.
type Authorization struct {
	Basis            string `json:"basis"` // signed_proposal | purchase_order | other
	ProposalDate     string `json:"proposal_date"`
	ApprovalDate     string `json:"approval_date"`
	PONumber         string `json:"po_number"`
	PORecipient      string `json:"po_recipient"`
	PODate           string `json:"po_date"`
	OtherDescription string `json:"other_description"`
}

type ClientBlock struct {
	EntityName   string   `json:"entity_name"`
	ContactName  string   `json:"contact_name"`
	AddressLines []string `json:"address_lines"`
}

type SiteBlock struct {
	ParcelIDs  []string `json:"parcel_ids"`
	Acreage    string   `json:"acreage"`
	County     string   `json:"county"`
	Descriptor string   `json:"descriptor"`
}

// Knowledge is one 40 CFR 312 user-obligation item.
//
// Known is a POINTER on purpose — three states, not two:
//
//	nil    the user was not asked, or did not answer
//	false  the user answered: none known
//	true   the user answered: yes
//
// Collapsing nil into false would render an unanswered question as "the user
// reports none known" — a disclosure the user never made, in a signed report,
// about a legal obligation. That is the placeholder lesson in boolean form.
type Knowledge struct {
	Known  *bool  `json:"known"`
	Detail string `json:"detail"`
}

func (k Knowledge) answered() bool { return k.Known != nil }
func (k Knowledge) isYes() bool    { return k.Known != nil && *k.Known }

type UserKnowledge struct {
	EnvironmentalLiens   Knowledge `json:"environmental_liens"`
	AULKnown             Knowledge `json:"aul_known"`
	SpecializedKnowledge Knowledge `json:"specialized_knowledge"`
	Other                string    `json:"other"`
}

// SiteVisit is asked only when no site recon checklist was uploaded.
type SiteVisit struct {
	AccessFrom     string `json:"access_from"`
	AccessVia      string `json:"access_via"`
	CurrentUse     string `json:"current_use"`
	ASTUSTObserved string `json:"ast_ust_observed"`
}

// userKnowledge is nil-safe, but note that "no intake at all" and "an intake
// that left the obligations blank" are DIFFERENT and must not be conflated:
// see userKnowledgeBlock, which is where that distinction is drawn.
func (in *Intake) userKnowledge() UserKnowledge {
	if in == nil {
		return UserKnowledge{}
	}
	return in.UserKnowledge
}

// authorization is nil-safe.
func (in *Intake) authorization() Authorization {
	if in == nil {
		return Authorization{}
	}
	return in.Authorization
}

// Validate rejects an intake the server does not understand.
func (in *Intake) Validate() error {
	if in == nil {
		return nil // absent intake is legal; the legacy answers map still works
	}
	if in.SchemaVersion == 0 {
		return fmt.Errorf("intake.schema_version is required (this server speaks version %d)", intakeSchemaVersion)
	}
	if !supportedIntakeVersions[in.SchemaVersion] {
		return fmt.Errorf("intake.schema_version %d is not supported by this server (supported: %s) — "+
			"refusing rather than parsing a contract this build does not understand",
			in.SchemaVersion, supportedVersionList())
	}
	return nil
}

func supportedVersionList() string {
	var vs []string
	for v := range supportedIntakeVersions {
		vs = append(vs, fmt.Sprint(v))
	}
	return strings.Join(vs, ", ")
}

// --- house date format ---------------------------------------------------------

// formatHouseDate converts an ISO 8601 date to the stamped house format.
// Returns "" when the input is empty or unparseable, so a bad date becomes a
// data gap upstream rather than a wrong date in a signed report.
func formatHouseDate(iso string) string {
	iso = strings.TrimSpace(iso)
	if iso == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006/01/02", "01/02/2006"} {
		if t, err := time.Parse(layout, iso); err == nil {
			return t.Format("January 2, 2006")
		}
	}
	return ""
}

// --- authorization composition ---------------------------------------------------

// authorizationDataGap is what an unanswered authorization produces. Never an
// inference from the uploaded proposal: this field exists to replace that.
const authorizationDataGap = "[MEG DATAGAP: authorization]"

// composeAuthorization builds the {{User_Authorization}} value.
//
// FRAGMENT CONTRACT, read from the template rather than assumed: the template
// prints, in two places,
//
//	"This work was performed in accordance with {{User_Authorization}}."
//
// so the value continues that clause and supplies no trailing period. Composing
// it in Go kills both observed defects at once — the blank value, and the
// splice where the model restated the lead-in and delivered
// "in accordance with This work was performed in accordance with ... .."
func composeAuthorization(a Authorization) string {
	switch strings.ToLower(strings.TrimSpace(a.Basis)) {
	case "signed_proposal":
		d := formatHouseDate(a.ProposalDate)
		if d == "" {
			return authorizationDataGap
		}
		if ap := formatHouseDate(a.ApprovalDate); ap != "" {
			return fmt.Sprintf("our proposal dated %s and approved on %s", d, ap)
		}
		return fmt.Sprintf("our proposal dated %s", d)

	case "purchase_order":
		num := strings.TrimSpace(a.PONumber)
		if num == "" {
			return authorizationDataGap
		}
		out := fmt.Sprintf("Purchase Order %s", num)
		if who := strings.TrimSpace(a.PORecipient); who != "" {
			out += fmt.Sprintf(", emailed to %s", who)
		}
		if d := formatHouseDate(a.PODate); d != "" {
			out += fmt.Sprintf(" on %s", d)
		}
		return out

	case "other":
		// The EP owns this wording; it is used verbatim.
		if d := strings.TrimSpace(a.OtherDescription); d != "" {
			return d
		}
		return authorizationDataGap

	default:
		return authorizationDataGap
	}
}

// --- user actual knowledge -------------------------------------------------------

// userKnowledgeBlockHeader labels the 40 CFR 312 material in the payload so the
// ASTM skill's Actual Knowledge Override rule can be pointed at it by name
// rather than hoping the model notices it inside the general answers blob.
const userKnowledgeBlockHeader = "=== [USER ACTUAL KNOWLEDGE — 40 CFR 312 USER OBLIGATIONS] ==="

// userKnowledgeBlock is the request-level gate, and the ONLY place that decides
// whether the §4 block appears at all.
//
// A request carrying no intake made no statement about these obligations,
// because it was never asked — the contract predates it. Emitting the block
// there would assert three unanswered legal obligations against a caller that
// was never given the chance to answer.
//
// An intake that IS supplied but leaves the obligations blank is a different
// thing entirely: the questions are required, so blank means unanswered, and
// RenderUserKnowledge says so loudly. Suppressing the block in that case is how
// a required disclosure would silently vanish.
func userKnowledgeBlock(in *Intake) string {
	if in == nil {
		return ""
	}
	return RenderUserKnowledge(in.UserKnowledge)
}

// RenderUserKnowledge builds the labelled payload block. It is deliberately
// NOISY: an untouched obligation renders as NOT ANSWERED rather than as
// nothing, so the gap is visible instead of absent. See userKnowledgeBlock for
// the one case in which no block is emitted.
//
// Attribution is mandatory: §4 content derived from this is the USER's
// disclosure, not Matrix's finding. A lien the EP disclosed and a lien Matrix
// discovered are different claims in a signed report.
func RenderUserKnowledge(uk UserKnowledge) string {
	var lines []string

	add := func(label string, k Knowledge) {
		switch {
		case !k.answered():
			// NOT asked or not answered. Deliberately rendered as an unanswered
			// obligation, never as "none known" — the user made no statement, so
			// the report must not attribute one to them.
			lines = append(lines, fmt.Sprintf("- %s: NOT ANSWERED by the user. "+
				"Report as [MEG DATAGAP: %s — user obligation unanswered]; do not state that none are known.", label, label))
		case k.isYes() && strings.TrimSpace(k.Detail) != "":
			lines = append(lines, fmt.Sprintf("- %s: YES — user states: %q", label, strings.TrimSpace(k.Detail)))
		case k.isYes():
			// Disclosed with no detail. Must not vanish: a disclosed condition
			// with no description is itself a data gap the EP has to close.
			lines = append(lines, fmt.Sprintf("- %s: YES — user disclosed this but supplied no detail. "+
				"Report as [MEG DATAGAP: %s detail — user disclosed, description missing].", label, label))
		default:
			lines = append(lines, fmt.Sprintf("- %s: user reports none known.", label))
		}
	}

	add("Environmental liens", uk.EnvironmentalLiens)
	add("Activity and Use Limitations (AULs)", uk.AULKnown)
	add("Specialized knowledge or experience", uk.SpecializedKnowledge)

	if other := strings.TrimSpace(uk.Other); other != "" {
		lines = append(lines, fmt.Sprintf("- Additional information volunteered by the user: %q", other))
	}

	if len(lines) == 0 {
		return ""
	}

	return userKnowledgeBlockHeader + "\n" +
		"This is USER-PROVIDED ACTUAL KNOWLEDGE under 40 CFR 312. It is the user's\n" +
		"disclosure, not a Matrix finding: attribute it to the user in Section 4 and\n" +
		"never present it as an independent Matrix observation. Anything marked YES\n" +
		"must appear in the report or carry a bracket — a disclosed condition that\n" +
		"silently vanishes is the worst outcome available here.\n" +
		strings.Join(lines, "\n")
}

// --- intake → answers ------------------------------------------------------------

// intakeToAnswers flattens an Intake into the legacy answers map so the model
// payload and injectFieldDefaults keep one code path during the transition.
// Structured fields that Go composes are handled separately; this carries the
// values the model reads as context.
func intakeToAnswers(in *Intake) map[string]string {
	out := map[string]string{}
	if in == nil {
		return out
	}
	if v := strings.TrimSpace(in.Project.Number); v != "" {
		out["project_number"] = v
	}
	if v := strings.TrimSpace(in.Client.EntityName); v != "" {
		out["client_spelling"] = v
	}
	if v := strings.TrimSpace(in.Client.ContactName); v != "" {
		out["client_contact"] = v
	}
	if len(in.Client.AddressLines) > 0 {
		out["client_address"] = strings.Join(in.Client.AddressLines, "\n")
	}
	if len(in.Site.ParcelIDs) > 0 {
		// Verbatim, joined. House rule: parcel IDs are printed as entered --
		// spaces, dots and dashes preserved, no format enforcement.
		out["parcel_id"] = strings.Join(in.Site.ParcelIDs, ", ")
	}
	if v := strings.TrimSpace(in.Site.Acreage); v != "" {
		out["site_acreage"] = v
	}
	if v := strings.TrimSpace(in.Site.County); v != "" {
		out["site_county"] = v
	}
	if v := strings.TrimSpace(in.Site.Descriptor); v != "" {
		out["report_descriptor"] = v
	}
	if v := strings.TrimSpace(in.SiteVisit.AccessFrom); v != "" {
		out["sv_access_from"] = v
	}
	if v := strings.TrimSpace(in.SiteVisit.AccessVia); v != "" {
		out["sv_access_via"] = v
	}
	if v := strings.TrimSpace(in.SiteVisit.CurrentUse); v != "" {
		out["sv_current_use"] = v
	}
	if v := strings.TrimSpace(in.SiteVisit.ASTUSTObserved); v != "" {
		out["site_recon_ast_ust"] = v
	}
	return out
}
