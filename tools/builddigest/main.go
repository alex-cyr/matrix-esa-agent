// builddigest assembles the two Phase 5 style digests from the mined tables
// and the two exemplar transcripts.
//
// DETERMINISTIC. Zero model calls. The Tier 1 keeps and Tier 2 families are
// listed here explicitly, each with the tag it feeds, because the editorial
// decision of what belongs in a digest is a human one and this file is the
// record of it. What the tool automates is the part that must not be done by
// hand: pseudonymizing the exemplars and scanning the result.
//
// Run from the repo root:
//
//	go run ./tools/builddigest
//
// Writes knowledge/style_baseline_astm.md and knowledge/style_baseline_compiler.md.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	historicalDir = "historical"
	cacheDir      = "historical/.cache"

	astmOut     = "knowledge/style_baseline_astm.md"
	compilerOut = "knowledge/style_baseline_compiler.md"

	// One exemplar each, per the ruling. Homestead dropped.
	astmExemplar     = "Phase_ I_Report_1080_Moreland_October_2025_FINAL.pdf" // REC-present
	compilerExemplar = "Phase I ESA Old Field Road October 2023.pdf"          // clean, vacant
)

// tier1Keep is the Tier-1 material that survived both filters AND names a tag
// it feeds. The tag is recorded so the keep can be re-checked against the
// canonical list rather than taken on trust.
// BYTE-IDENTICAL to the mined table. The header of every digest promises these
// were found by diffing nine reports; a line edited during assembly breaks that
// promise silently. The first build shortened three of these and changed
// "dispensers" to "stained soils" -- a different observation about tanks,
// presented as corpus-verified house wording. verifyCanonical() below now makes
// that impossible to repeat.
var tier1Keep = []struct{ Tag, Text string }{
	{"DataGaps_Text / Sec9_Item8_DataGaps",
		"However, the significance of this gap is considered low and not likely to alter the report's conclusions due to the limited information obtained during interviews, as well as during our search of standard historical sources of information such as aerial photographs, and historic topographic maps"},
	{"VEC_Summary / Sec9_Item5_VEC",
		"The report was designed to assist parties seeking to meet the search requirements of the ASTM Standard Practice for Assessment of Vapor Encroachment into Structures on Property Involved in Real Estate Transactions (E 2600-10)"},
	{"Sec8_13_Radon",
		"If radon levels within a structure are greater than the 4 pCi/L level, the EPA recommends that construction or renovation processes be in compliance with the EPA's Radon Prevention in the Design and Construction Guidelines"},
	{"Sec8_12_Tanks",
		"No evidence of USTs was identified on the subject property and no common indicators of USTs, such as vent pipes, fill ports, manways, pavement cuts, fuel gauges or dispensers, were observed"},
	{"Sec8_12_Tanks",
		"The subject site was not identified on the Georgia list of registered UST facilities"},
	{"Sec8_12_Tanks",
		"ASTs are not required to be registered in the state of Georgia"},
}

const minedTable = "docs/phase5-mined-candidates.md"

// verifyCanonical fails the build if any canonical line is not present verbatim
// in the mined table. Mechanical, because "I checked" is exactly the assurance
// that failed here.
func verifyCanonical() {
	mined, err := os.ReadFile(minedTable)
	if err != nil {
		log.Fatalf("read %s: %v", minedTable, err)
	}
	body := string(mined)
	for _, k := range tier1Keep {
		if !strings.Contains(body, k.Text) {
			log.Fatalf("canonical line for %s is not verbatim in %s.\nAssembly may not edit mined text.\n  wanted: %s", k.Tag, minedTable, k.Text)
		}
	}
}

// tier2Family is a house formula. Values are shown as placeholders; the shape
// is the point.
type tier2Family struct {
	Name     string
	Quorum   string
	Formula  string
	Variants []string
}

var tier2Families = []tier2Family{
	{"Transmittal closing", "7/9",
		"Matrix Engineering Group, Inc. appreciates the opportunity to work with [CLIENT] on this project and looks forward to our continued association.",
		nil},
	{"User actual knowledge", "7/9",
		"It is our understanding that [CLIENT] has no knowledge of a recognized environmental condition in connection with the subject site.",
		nil},
	{"Site-visit negative — general", "8/9",
		"During our site visit, there was no evidence of distressed vegetation, pools of liquid or sludge, or unusual staining or corrosion at the subject site.",
		nil},
	{"Site-visit negative — hazardous materials", "7/9",
		"There was no evidence of hazardous material storage at the subject site at the time of our site reconnaissance.",
		nil},
	{"Flood map review", "8/9",
		"The Flood Insurance Rate Map (FIRM) for the area of the subject property was reviewed to determine flood zone areas.",
		nil},
	{"PCB explainer", "8/9",
		"In the past, polychlorinated biphenyls (PCBs) have been used as coolants or insulating fluids in electrical equipment, such as transformers.",
		nil},
	{"ACM explainer", "7/9",
		"Asbestos Containing Materials (ACM) have been widely used in various construction materials, such as adhesives, sealants, flooring materials, sprayed-on fireproofing materials and siding materials.",
		nil},
	{"LBP explainer", "8/9",
		"Lead-Based Paints (LBP) have been used extensively in the past.",
		nil},
	// The ninth family, added per the ruling. This is the outcome fork, and the
	// 1-of-9 finding is what proves it is a variant family rather than a
	// canonical block: clean and REC-present reports necessarily differ here.
	{"Section 9/10 outcome closer — THE OUTCOME FORK", "variant family, not canonical",
		"This assessment has revealed [OUTCOME] in connection with the [PROPERTY].",
		[]string{
			"CLEAN: \"...has revealed no evidence of recognized environmental conditions in connection with the property.\"",
			"REC-PRESENT: \"...has revealed the following recognized environmental condition(s) in connection with the subject property:\" followed by the enumerated findings.",
		}},
}

// --- pseudonymization ---------------------------------------------------------

// The exemplars are full client reports appended to every future prompt, so
// they are the largest leak vector in the phase. Identifiers are replaced with
// typed placeholders: voice survives, the vector dies, the contamination scan
// then runs over the ENTIRE digest with no exemptions, and an exemplar whose
// identifiers are placeholders cannot leak a real address into another client's
// report even if a downstream rule fails.
var pseudonyms = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)\bMEG[\s-]*(project\s*no\.?\s*)?\d[\d.\-]*`), "[PROJECT NO]"},
	{regexp.MustCompile(`\b\d{2}[\s-]\d{3,4}[\s-]\d{2}[\s-]\d{3}\b`), "[PARCEL ID]"},
	{regexp.MustCompile(`\b\d{2}\s?\d{6,}\b`), "[PARCEL ID]"},
	// Street NAMES, with or without a number. Requiring a leading number let
	// "Old Field Road NW" through the first build while the scan still reported
	// clean -- the address is in the report title, where it never carries a
	// house number.
	{regexp.MustCompile(`\b(\d{1,6}\s+)?[A-Z][A-Za-z'’\-]*(\s+[A-Z][A-Za-z'’\-]*){0,2}\s+(Road|Rd|Street|St|Avenue|Ave|Drive|Dr|Court|Ct|Lane|Ln|Boulevard|Blvd|Way|Circle|Cir|Parkway|Pkwy|Highway|Hwy|Trail|Trl)\b\.?(\s+(NW|NE|SW|SE|N|S|E|W)\b)?`), "[SITE ADDRESS]"},
	// Facility names: schools, churches, centers. Identifying, and the report
	// title often IS one.
	{regexp.MustCompile(`\b[A-Z\[\]][A-Za-z'’\-\]\[]*(\s+[A-Z\[\]][A-Za-z'’\-\]\[]*)*\s+(Elementary|Middle|High)\s+School(\s+#?\s?\d+)?`), "[FACILITY]"},
	{regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+(\s+[A-Z][A-Za-z'’\-]+)*\s+(Church|Academy|Hospital|Library|Stadium|Airport)\b`), "[FACILITY]"},
	{regexp.MustCompile(`(?i)\b(Mr|Mrs|Ms|Dr)\.\s+[A-Z][A-Za-z'’\-]+(\s+[A-Z][A-Za-z'’\-]+)?`), "[CONTACT]"},
	{regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+\s+County\b`), "[COUNTY] County"},
	{regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+(\s+[A-Z][A-Za-z'’\-]+){0,3},?\s+(LLC|L\.L\.C\.|Inc\.|Incorporated|Corporation|Corp\.|LP|L\.P\.)`), "[CLIENT]"},
	{regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+(\s+[A-Z][A-Za-z'’\-]+)*\s+(Schools|School District|Parks|Recreation|Cultural Affairs|Properties|Holdings|Partners|Development)\b`), "[CLIENT]"},
	// Any "City, GA" pair, rather than a fixed list of cities. Enumerating them
	// let "Adairsville, GA" through -- a list of known places cannot cover the
	// place you have not seen yet, which is the wrong shape for a safety check.
	// Same state list as the contamination scan. The first pass masked only
	// Georgia and the broader scan then caught an occupant address in
	// Tennessee -- which is the scan working as intended, and the reason it is
	// deliberately wider than the masker.
	{regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+(\s+[A-Z][A-Za-z'’\-]+)?,\s+(GA|Georgia|AL|TN|SC|NC|FL)\b`), "[PLACE], [STATE]"},
	{regexp.MustCompile(`(?i)\b(Alpharetta|Milton|Roswell|Decatur|Atlanta|Conyers|Tucker|Cartersville|Adairsville|Bartow|DeKalb|Fulton|Gwinnett|Rockdale|Clayton)\b`), "[PLACE]"},
	{regexp.MustCompile(`\b\d{5}(-\d{4})?\b`), "[ZIP]"},
}

// Matrix is the authoring firm, not a client. Its name is house voice and stays.
var restoreMatrix = strings.NewReplacer("[CLIENT]", "[CLIENT]")

func pseudonymize(s string) string {
	// Protect the firm's own name before the corporate-suffix rule reaches it.
	const firm = "\x00FIRM\x00"
	s = regexp.MustCompile(`(?i)Matrix Engineering Group,?\s*(Inc\.?)?`).ReplaceAllString(s, firm)
	for _, p := range pseudonyms {
		s = p.re.ReplaceAllString(s, p.with)
	}
	s = strings.ReplaceAll(s, firm, "Matrix Engineering Group, Inc.")
	return restoreMatrix.Replace(s)
}

// contaminationScan looks for identifiers that survived pseudonymization.
// Reported, never silently accepted: this is the check that decides whether a
// digest is safe to attach to every future report.
// The scan must be BROADER than the masking, not a mirror of it. A scan built
// from the same assumptions as the masker confirms those assumptions instead of
// testing them: the first build reported "clean" while "Old Field Road NW" and
// "Adairsville, GA" sat in the output, because both the masker and the scan
// assumed a street number and a known city list.
var contaminationPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"street name", regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+\s+(Road|Rd|Street|St|Avenue|Ave|Drive|Dr|Court|Ct|Lane|Ln|Boulevard|Blvd|Circle|Cir|Parkway|Pkwy|Highway|Hwy|Trail)\b`)},
	{"corporate entity", regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+\s+(LLC|Inc\.|Corporation|Corp\.)\b`)},
	{"county name", regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+\s+County\b`)},
	{"city, state", regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+,\s+(GA|Georgia|AL|TN|SC|NC|FL)\b`)},
	{"personal name", regexp.MustCompile(`\b(Mr|Mrs|Ms|Dr)\.\s+[A-Z]`)},
	{"facility name", regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+\s+(Elementary|Middle|High)\s+School\b`)},
	{"zip code", regexp.MustCompile(`\b3\d{4}\b`)},

	// Classes added after the first digest review. Every one of these leaked
	// past a scan that reported "clean", and most of them lived in exemplar
	// appendix material. Listed as their own classes rather than folded into
	// the ones above, so a future failure says which shape got through.
	{"business entity", regexp.MustCompile(`\b[A-Z][A-Za-z&'’\-]+(\s+[A-Z][A-Za-z&'’\-]+)*\s+(Co\.|Company|Consulting|Collaborative|Management|Trucking|Automotive|Aggregates|Americas|Discount|Associates|Partners|Industries|Enterprises|Contractors|Supply)\b`)},
	// Second half must be TitleCase: "APAC-Georgia" is an entity, "REC-PRESENT"
	// is this digest's own heading.
	{"hyphenated entity", regexp.MustCompile(`\b[A-Z]{3,}-[A-Z][a-z]+\b`)},
	{"phone number", regexp.MustCompile(`\b\d{3}[.\-]\d{3}[.\-]\d{4}\b`)},
	// A bare pair of capitalised words is ordinary prose. A person is signalled
	// by a label or a phone on the same line, or by being shouted in caps.
	{"person name (labelled)", regexp.MustCompile(`(?i)\b(owner|contact|inspector|preparer|interviewee|representative)['’]?s?\s*name\s*[:\-]\s*\S+`)},
	{"person name (with phone)", regexp.MustCompile(`\b[A-Z][a-z]+\s+([A-Z]\.?\s+)?[A-Z][a-z]+\b[^\n]{0,60}\d{3}[.\-]\d{3}[.\-]\d{4}`)},
	{"shouting name", regexp.MustCompile(`\b[A-Z]{3,}\s+[A-Z]{3,}\b`)},
	{"parcel id (alt formats)", regexp.MustCompile(`\b\d{3,4}-\d{4}-\d{3}\b`)},
	{"FIRM panel", regexp.MustCompile(`\b\d{5}C\d{3,4}[A-Z]\b`)},
	{"named watercourse/landfill", regexp.MustCompile(`\b[A-Z][A-Za-z'’\-]+\s+(Creek|Landfill|Branch|River|Lake|Pond|Quarry|Mine|Reservoir)\b`)},
}

// scanAllowlist are public bodies, standards and generic descriptors that match
// a pattern above but identify nobody. Kept explicit and short: an allowlist is
// how a scan goes quietly blind, so each entry has to earn its place.
var scanAllowlist = []string{
	"Federal Emergency Management", "Environmental Protection", "Georgia Environmental",
	"Solid Waste Landfill", "Waste Landfill", "Sanitary Landfill", "Municipal Landfill",
	"THE OUTCOME", "REC-PRESENT", "SITE ADDRESS", "PROJECT NO", "PARCEL ID",
	"ENVIRONMENTAL SITE", "SITE ASSESSMENT", "PHASE I", "ASTM E", "EPA",
	// Document type and form labels, not entities or people.
	"ESA-Phase", "PROJECT NUMBER", "PROJECT NO", "TABLE OF", "SITE LOCATION",
	"HISTORICAL USE", "REGULATORY REVIEW", "PHYSICAL SITE", "SITE RECONNAISSANCE",
	"USER PROVIDED", "DATA GAPS", "NON-SCOPE", "FINDINGS", "OPINIONS", "CONCLUSIONS",
	"AND SURROUNDING", "PROVIDED INFORMATION", "FIRM PANEL", "SITE NAME",
	"ABBREVIATIONS", "APPENDICES", "REFERENCES",
}

func allowlisted(s string) bool {
	for _, a := range scanAllowlist {
		if strings.Contains(s, a) {
			return true
		}
	}
	return false
}

// exemplarOnly returns the fenced exemplar block, which is the only part of a
// digest that can carry client material. The surrounding prose is generated by
// this tool and safe by construction; scanning it produced 853 "person name"
// hits on headings like "Template Compiler", which is how a scan becomes noise
// nobody reads.
func exemplarOnly(body string) string {
	i := strings.Index(body, "```")
	if i < 0 {
		return body
	}
	rest := body[i+3:]
	if j := strings.LastIndex(rest, "```"); j > 0 {
		return rest[:j]
	}
	return rest
}

func scan(body string) map[string]int {
	hits := map[string]int{}
	for _, p := range contaminationPatterns {
		if n := len(p.re.FindAllString(body, -1)); n > 0 {
			hits[p.name] = n
		}
	}
	return hits
}

type scanHit struct {
	class  string
	count  int
	sample string
}

// scanDetail returns each class with a worked example, so a hit can be judged
// rather than merely counted.
func scanDetail(body string) []scanHit {
	body = exemplarOnly(body)
	var out []scanHit
	for _, p := range contaminationPatterns {
		var kept []string
		for _, m := range p.re.FindAllString(body, -1) {
			if !allowlisted(m) {
				kept = append(kept, m)
			}
		}
		if len(kept) == 0 {
			continue
		}
		sample := strings.TrimSpace(strings.Join(strings.Fields(kept[0]), " "))
		if len(sample) > 44 {
			sample = sample[:44] + "…"
		}
		out = append(out, scanHit{class: p.name, count: len(kept), sample: sample})
	}
	return out
}

// appendixStart marks the first appendix heading proper -- not the table of
// contents entry, which is mixed case.
var appendixStart = regexp.MustCompile(`(?m)^APPENDIX\s+[A-H]\b`)

// bodyOnly keeps the report body: cover letter through Section 11 plus the EP
// declaration, and drops everything from Appendix A onward.
//
// Structural fix, applied before any pattern matching. Every person-name and
// phone-number leak found in review lived in appendix material -- questionnaire
// responses, interview records, résumés, EDR printouts -- which the model never
// composes into a tag. Cutting the class beats masking its members one at a
// time, and it costs no voice: the body is where the voice is.
func bodyOnly(text string) string {
	loc := appendixStart.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return strings.TrimSpace(text[:loc[0]])
}

// bodyLeaks are identifiers that survive in the report BODY, where cutting is
// not an option. Named individually because each was found by human review of
// the assembled digest, and a named list is auditable in a way a clever pattern
// is not.
var bodyLeaks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)\bEntrenchment\s+Creek\b`), "[WATERCOURSE]"},
	{regexp.MustCompile(`(?i)\bDonzi\s+(Old\s+Castle\s+Property|Landfill)\b`), "[LANDFILL]"},
	{regexp.MustCompile(`(?i)\bDonzi\b`), "[LANDFILL]"},
	// Match the bare token, not each rendering. "APAC-Georgia" was masked while
	// "APAC Parcel" and "APAC-GA" survived: an entity appears in more forms
	// than a list of forms can anticipate, so mask the name itself. Word
	// boundaries keep it off "capacitors".
	{regexp.MustCompile(`\bAPAC(-[A-Za-z]+)?\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bCRH\s+(Americas|Southern\s+Atlantic\s+Aggregates)\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bCRH\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bUnited\s+Consulting\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bJonathan\s+Woodner\s+Co\.?\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bKaizen\s+Collaborative\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bMcKenney'?s\s+Management\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bA&B\s+Discount\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bContinental\s+Trucking\b`), "[ENTITY]"},
	{regexp.MustCompile(`(?i)\bGrant\s+Automotive\s+Service\b`), "[ENTITY]"},
	{regexp.MustCompile(`\b1950\s+West\s+Exchange\b`), "[SITE ADDRESS]"},
	// Parcel formats the general masks missed: 040-0195-038, 0040-0195-006.
	{regexp.MustCompile(`\b\d{3,4}-\d{4}-\d{3}\b`), "[PARCEL ID]"},
	// FIRM panel numbers encode the county FIPS code, so they identify the
	// county even when the county name is masked.
	{regexp.MustCompile(`\b\d{5}C\d{3,4}[A-Z]\b`), "[FIRM PANEL]"},
	{regexp.MustCompile(`\b\d{3}[.\-]\d{3}[.\-]\d{4}\b`), "[PHONE]"},
}

func maskBodyLeaks(s string) string {
	for _, l := range bodyLeaks {
		s = l.re.ReplaceAllString(s, l.with)
	}
	return s
}

// restoreHeadings undoes over-masking of structural headings. "Surrounding
// Properties" is a section title, not a client.
var headingFixes = strings.NewReplacer(
	"3.2 [CLIENT]", "3.2 Surrounding Properties",
	"3.2\t[CLIENT]", "3.2\tSurrounding Properties",
	"3.2[CLIENT]", "3.2 Surrounding Properties",
)

func loadExemplar(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(historicalDir, name))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	body, err := os.ReadFile(filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".txt"))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

const guardrail = `## ASTM version

Baseline material below may quote older ASTM versions — one exemplar embeds an
owner questionnaire that cites E1527-13. **Always cite E 1527-21 regardless of
baseline phrasing.**
`

const numberingNote = `## Section numbering comes from the template, never from here

Section numbers shift between reports — the same content sits at 8.13 in one and
8.16 in another, which the corpus mining showed directly. Numbers in this digest
are masked or incidental. Take every section number from the template you are
filling.
`

func main() {
	verifyCanonical()
	// -scan <file>... runs the contamination scan over existing files without
	// rebuilding. The scan has to be demonstrated against known-contaminated
	// input before it can be trusted on clean input: a scan that has only ever
	// reported "clean" has not been tested, it has been assumed.
	if len(os.Args) > 2 && os.Args[1] == "-scan" {
		for _, path := range os.Args[2:] {
			body, err := os.ReadFile(path)
			if err != nil {
				log.Fatalf("read %s: %v", path, err)
			}
			fmt.Printf("%s\n", path)
			hits := scanDetail(string(body))
			if len(hits) == 0 {
				fmt.Printf("  clean\n")
				continue
			}
			for _, h := range hits {
				fmt.Printf("  %-28s %3d  e.g. %s\n", h.class, h.count, h.sample)
			}
		}
		return
	}

	for _, spec := range []struct {
		out, exemplarFile, role, exemplarNote string
	}{
		{astmOut, astmExemplar, "ASTM Synthesizer",
			"REC-present, vacant/heavily wooded, E1527-21. Chosen for reasoning shape: it shows how a finding is argued to a conclusion."},
		{compilerOut, compilerExemplar, "Template Compiler",
			"Clean outcome, vacant/undeveloped, E1527-21. Chosen for formatting shape, and the closest match to an undeveloped subject property."},
	} {
		exemplar, err := loadExemplar(spec.exemplarFile)
		if err != nil {
			log.Fatalf("load exemplar %s: %v", spec.exemplarFile, err)
		}
		// Structural cut first, then pattern masking. Cutting the appendices
		// removes an entire class of leak rather than chasing its members.
		body0 := bodyOnly(exemplar)
		clean := headingFixes.Replace(maskBodyLeaks(pseudonymize(body0)))

		var b strings.Builder
		fmt.Fprintf(&b, "# Matrix house style — %s\n\n", spec.role)
		b.WriteString("Generated by [tools/builddigest](../tools/builddigest/main.go) from the mined\n")
		b.WriteString("corpus tables. Every passage below was **found by diffing** nine independent\n")
		b.WriteString("E1527-21 reports, with the count of reports carrying it recorded. Nothing here\n")
		b.WriteString("is asserted to be house style on anyone's say-so.\n\n")
		b.WriteString("**Identifiers are placeholders.** Client names, addresses, parcel IDs, counties\n")
		b.WriteString("and project numbers have been replaced. Never copy an identifier from this\n")
		b.WriteString("file into a report — there are none to copy, by design.\n\n")
		b.WriteString(guardrail)
		b.WriteString("\n")
		b.WriteString(numberingNote)

		b.WriteString("\n---\n\n## Canonical sentences\n\nVerbatim across the corpus. Each names the tag it feeds.\n\n")
		for _, k := range tier1Keep {
			fmt.Fprintf(&b, "**`%s`**\n\n> %s\n\n", k.Tag, k.Text)
		}

		b.WriteString("---\n\n## House formulas\n\nSame shape every time; the values change.\n\n")
		for i, f := range tier2Families {
			fmt.Fprintf(&b, "### %d. %s (%s)\n\n> %s\n\n", i+1, f.Name, f.Quorum, f.Formula)
			for _, v := range f.Variants {
				fmt.Fprintf(&b, "- %s\n", v)
			}
			if len(f.Variants) > 0 {
				b.WriteString("\n")
			}
		}

		fmt.Fprintf(&b, "---\n\n## Exemplar report\n\n%s\n\nRead it for voice, register and how sections hang together — not for its facts,\nwhich belong to a different property.\n\n```\n%s\n```\n",
			spec.exemplarNote, strings.TrimSpace(clean))

		body := b.String()
		if err := os.WriteFile(spec.out, []byte(body), 0o644); err != nil {
			log.Fatalf("write %s: %v", spec.out, err)
		}

		fmt.Printf("%s\n  %d bytes  ≈%dk tokens\n", spec.out, len(body), len(body)/4000)
		if hits := scanDetail(body); len(hits) > 0 {
			fmt.Printf("  CONTAMINATION SCAN — residual identifiers, review before wiring:\n")
			for _, h := range hits {
				fmt.Printf("    %-28s %3d  e.g. %s\n", h.class, h.count, h.sample)
			}
		} else {
			fmt.Printf("  contamination scan: clean\n")
		}
	}
}
