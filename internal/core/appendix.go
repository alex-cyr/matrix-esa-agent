package core

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// AppendixSection represents a standard ASTM E1527-21 Appendix group.
type AppendixSection string

const (
	AppendixA AppendixSection = "Appendix A - Site Plan, FIRM, NWI Maps & Historical Aerial Photographs"
	AppendixB AppendixSection = "Appendix B - Photographs"
	AppendixC AppendixSection = "Appendix C - EDR Radius Map Report"
	AppendixD AppendixSection = "Appendix D - Phase I Property Owner / User Interview Questionnaire"
	AppendixE AppendixSection = "Appendix E - Sanborn Map Report"
	AppendixF AppendixSection = "Appendix F - Vapor Encroachment Screen Report"
	AppendixG AppendixSection = "Appendix G - Environmental Lien and AUL Search Report"
	AppendixH AppendixSection = "Appendix H - Qualifications"
)

// TitleBlock Metadata for framed figure template pages in Appendix A / B.
type FigureTitleBlock struct {
	FigureTitle   string `json:"figure_title"`
	Source        string `json:"source"`
	ProjectName   string `json:"project_name"`
	ProjectNumber string `json:"project_number"`
}

// CategorizedFile holds mapping info for uploaded files.
type CategorizedFile struct {
	OriginalName string           `json:"original_name"`
	CustomTitle  string           `json:"custom_title"`
	Category     string           `json:"category"`
	InAppendix   bool             `json:"in_appendix"`
	AppendixType AppendixSection  `json:"appendix_type"`
	TitleBlock   FigureTitleBlock `json:"title_block"`
}

// FormatIntuitiveTitle generates professional section and figure titles based on filename conventions.
func FormatIntuitiveTitle(filename string) string {
	l := strings.ToLower(filename)
	if strings.Contains(l, "aerial") {
		re := regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)
		if match := re.FindString(filename); match != "" {
			return fmt.Sprintf("Historical Aerial Photograph - %s", match)
		}
		return "Historical Aerial Photograph"
	}
	if strings.Contains(l, "topo") {
		return "USGS Historical Topographic Maps"
	}
	if strings.Contains(l, "sanborn") {
		return "Certified Sanborn Map Report"
	}
	if strings.Contains(l, "radius") {
		return "EDR Radius Map Report"
	}
	if strings.Contains(l, "firm") || strings.Contains(l, "flood") {
		return "Flood Insurance Rate Map"
	}
	if strings.Contains(l, "wetland") || strings.Contains(l, "nwi") {
		return "National Wetland Inventory Map"
	}
	if strings.Contains(l, "filio") || strings.Contains(l, "alyateem") || strings.Contains(l, "photo") {
		return "Filio Site Inspection Photographs"
	}
	if strings.Contains(l, "recon") || strings.Contains(l, "checklist") {
		return "Site Reconnaissance Field Checklist"
	}
	if strings.Contains(l, "questionnaire") || strings.Contains(l, "owner") {
		return "User Questionnaire & Owner Information"
	}
	if strings.Contains(l, "proposal") || strings.Contains(l, "contract") {
		return "Signed Project Proposal & Scope"
	}
	if strings.Contains(l, "vec") || strings.Contains(l, "vapor") {
		return "Vapor Encroachment Screen Report"
	}
	if strings.Contains(l, "qualification") || strings.Contains(l, "resume") {
		return "Qualifications of Environmental Professional"
	}
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

// MapCategoryToAppendix determines default Appendix section strictly matching Matrix's static report standard (A through H).
func MapCategoryToAppendix(category string, filename string) AppendixSection {
	l := strings.ToLower(filename)
	c := strings.ToLower(category)

	// Appendix A: Site Plan, FIRM, NWI Maps, Aerials, Topo Maps
	if strings.Contains(l, "aerial") || strings.Contains(l, "topo") || strings.Contains(l, "firm") || strings.Contains(l, "flood") || strings.Contains(l, "wetland") || strings.Contains(l, "nwi") || strings.Contains(l, "site_plan") || strings.Contains(c, "aerial") || strings.Contains(c, "topo") || strings.Contains(c, "firm") || strings.Contains(c, "wetland") {
		return AppendixA
	}
	// Appendix B: Photographs (Filio)
	if strings.Contains(l, "filio") || strings.Contains(l, "photo") || strings.Contains(c, "filio") || strings.Contains(c, "photo") {
		return AppendixB
	}
	// Appendix C: EDR Radius Map Report
	if strings.Contains(l, "radius") || strings.Contains(c, "radius") || strings.Contains(c, "edr") {
		return AppendixC
	}
	// Appendix D: Phase I Property Owner / User Interview Questionnaire
	if strings.Contains(l, "questionnaire") || strings.Contains(l, "user") || strings.Contains(l, "interview") || strings.Contains(l, "owner") || strings.Contains(c, "questionnaire") || strings.Contains(c, "user") {
		return AppendixD
	}
	// Appendix E: Sanborn Map Report
	if strings.Contains(l, "sanborn") || strings.Contains(c, "sanborn") {
		return AppendixE
	}
	// Appendix F: Vapor Encroachment Screen Report (VEC)
	if strings.Contains(l, "vec") || strings.Contains(l, "vapor") || strings.Contains(c, "vec") || strings.Contains(c, "vapor") {
		return AppendixF
	}
	// Appendix G: Environmental Lien and AUL Search Report
	if strings.Contains(l, "lien") || strings.Contains(l, "aul") || strings.Contains(c, "lien") || strings.Contains(c, "aul") {
		return AppendixG
	}
	// Appendix H: Qualifications
	if strings.Contains(l, "qualification") || strings.Contains(l, "resume") || strings.Contains(c, "qualification") {
		return AppendixH
	}

	return AppendixA
}

// DeriveDefaultSource returns the standard source citation for a map/figure matching historical reports.
func DeriveDefaultSource(category string, filename string) string {
	l := strings.ToLower(filename)
	c := strings.ToLower(category)
	if strings.Contains(l, "wetland") || strings.Contains(l, "nwi") || strings.Contains(c, "wetland") {
		return "Source: U.S. Fish and Wildlife Service"
	}
	if strings.Contains(l, "firm") || strings.Contains(l, "flood") || strings.Contains(c, "firm") || strings.Contains(c, "flood") {
		return "Source: Federal Emergency Management Agency"
	}
	if strings.Contains(l, "aerial") || strings.Contains(l, "topo") || strings.Contains(l, "sanborn") || strings.Contains(l, "radius") || strings.Contains(l, "edr") || strings.Contains(c, "edr") {
		return "Source: EDR Report"
	}
	if strings.Contains(l, "filio") || strings.Contains(l, "alyateem") || strings.Contains(l, "photo") || strings.Contains(l, "recon") {
		return "Source: Matrix Engineering Group Field Visit"
	}
	return "Source: Matrix Engineering Group, Inc."
}

// AppendixPackage manages appendix attachments for an ESA Phase I Report.
type AppendixPackage struct {
	ProjectName   string            `json:"project_name"`
	ProjectNumber string            `json:"project_number"`
	Files         []CategorizedFile `json:"files"`
}

// EnsureStandardPlaceholders guarantees that key standard Appendix figures/documents (like Site Plan) have a framed placeholder page if not provided in uploaded files.
func EnsureStandardPlaceholders(projName, projNum string, files []CategorizedFile) []CategorizedFile {
	cleanProjName := strings.ReplaceAll(projName, "_", " ")

	hasSitePlan := false
	for _, f := range files {
		l := strings.ToLower(f.OriginalName + " " + f.CustomTitle + " " + f.Category)
		if strings.Contains(l, "site_plan") || strings.Contains(l, "site plan") {
			hasSitePlan = true
			break
		}
	}

	if !hasSitePlan {
		files = append([]CategorizedFile{
			{
				OriginalName: "Site_Plan_Pending.pdf",
				CustomTitle:  "Site Plan (Pending EP Insertion)",
				Category:     "Site Plan",
				InAppendix:   true,
				AppendixType: AppendixA,
				TitleBlock: FigureTitleBlock{
					FigureTitle:   "Site Plan (Pending EP Insertion)",
					Source:        "[EP to Insert Site Plan Graphic / Figure]",
					ProjectName:   cleanProjName,
					ProjectNumber: projNum,
				},
			},
		}, files...)
	}

	return files
}

// NewAppendixPackage initializes package with title block metadata.
func NewAppendixPackage(projName, projNum string, files []CategorizedFile) *AppendixPackage {
	cleanProjName := strings.ReplaceAll(projName, "_", " ")
	files = EnsureStandardPlaceholders(projName, projNum, files)

	var filtered []CategorizedFile
	for _, f := range files {
		// STRICT CHECKBOX RESPECT: If user unchecked "Appendix" on web page, exclude from Appendix package!
		if !f.InAppendix {
			continue
		}
		if f.AppendixType == "" {
			f.AppendixType = MapCategoryToAppendix(f.Category, f.OriginalName)
		}
		if f.TitleBlock.ProjectName == "" {
			f.TitleBlock.ProjectName = cleanProjName
		}
		if f.TitleBlock.ProjectNumber == "" {
			f.TitleBlock.ProjectNumber = projNum
		}
		if f.TitleBlock.Source == "" {
			f.TitleBlock.Source = DeriveDefaultSource(f.Category, f.OriginalName)
		}
		if f.TitleBlock.FigureTitle == "" {
			if f.CustomTitle != "" && f.CustomTitle != strings.TrimSuffix(f.OriginalName, filepath.Ext(f.OriginalName)) {
				f.TitleBlock.FigureTitle = f.CustomTitle
			} else {
				f.TitleBlock.FigureTitle = FormatIntuitiveTitle(f.OriginalName)
			}
		}
		filtered = append(filtered, f)
	}
	return &AppendixPackage{
		ProjectName:   cleanProjName,
		ProjectNumber: projNum,
		Files:         filtered,
	}
}

// GenerateAppendixSummary constructs a clean structured text log for LLM context.
func (a *AppendixPackage) GenerateAppendixSummary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== APPENDIX MANIFEST FOR PROJECT [%s] (Project No: %s) ===\n", a.ProjectName, a.ProjectNumber))
	for i, f := range a.Files {
		sb.WriteString(fmt.Sprintf("%d. File: %s | Category: %s | Appendix Section: %s | Title: %s | Source: %s\n",
			i+1, f.OriginalName, f.Category, f.AppendixType, f.TitleBlock.FigureTitle, f.TitleBlock.Source))
	}
	return sb.String()
}
