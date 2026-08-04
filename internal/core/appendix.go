package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AppendixSection represents a standard ASTM E1527-21 Appendix group.
type AppendixSection string

const (
	AppendixA AppendixSection = "Appendix A - Historical Records & Maps"
	AppendixB AppendixSection = "Appendix B - Site Reconnaissance Photographs"
	AppendixC AppendixSection = "Appendix C - Regulatory Documentation & VEC"
	AppendixD AppendixSection = "Appendix D - Proposal & Authorization Documents"
	AppendixE AppendixSection = "Appendix E - User Questionnaire & Site Checklist"
	AppendixOther AppendixSection = "Appendix F - Supporting Data & Other Attachments"
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

// MapCategoryToAppendix determines default Appendix section based on category name.
func MapCategoryToAppendix(category string, filename string) AppendixSection {
	catLower := strings.ToLower(category)
	extLower := strings.ToLower(filepath.Ext(filename))

	if strings.Contains(catLower, "edr") || strings.Contains(catLower, "topo") || strings.Contains(catLower, "aerial") || strings.Contains(catLower, "historical") {
		return AppendixA
	}
	if strings.Contains(catLower, "photo") || strings.Contains(catLower, "folio") || extLower == ".jpg" || extLower == ".jpeg" || extLower == ".png" {
		return AppendixB
	}
	if strings.Contains(catLower, "wetland") || strings.Contains(catLower, "firm") || strings.Contains(catLower, "flood") || strings.Contains(catLower, "vec") || strings.Contains(catLower, "regulatory") {
		return AppendixC
	}
	if strings.Contains(catLower, "proposal") || strings.Contains(catLower, "contract") || strings.Contains(catLower, "po") {
		return AppendixD
	}
	if strings.Contains(catLower, "questionnaire") || strings.Contains(catLower, "recon") || strings.Contains(catLower, "checklist") {
		return AppendixE
	}
	return AppendixOther
}

// DeriveDefaultSource returns the standard source citation for a map/figure.
func DeriveDefaultSource(category string, filename string) string {
	catLower := strings.ToLower(category)
	if strings.Contains(catLower, "edr") || strings.Contains(catLower, "topo") || strings.Contains(catLower, "aerial") {
		return "Source: EDR Historical Report"
	}
	if strings.Contains(catLower, "firm") || strings.Contains(catLower, "flood") {
		return "Source: Federal Emergency Management Agency (FEMA)"
	}
	if strings.Contains(catLower, "wetland") {
		return "Source: U.S. Fish and Wildlife Service NWI"
	}
	if strings.Contains(catLower, "parcel") {
		return "Source: County Tax Assessor / qPublic.net"
	}
	if strings.Contains(catLower, "photo") || strings.Contains(catLower, "recon") {
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

// NewAppendixPackage initializes package with title block metadata.
func NewAppendixPackage(projName, projNum string, files []CategorizedFile) *AppendixPackage {
	for i, f := range files {
		if f.AppendixType == "" {
			files[i].AppendixType = MapCategoryToAppendix(f.Category, f.OriginalName)
		}
		if f.TitleBlock.ProjectName == "" {
			files[i].TitleBlock.ProjectName = projName
		}
		if f.TitleBlock.ProjectNumber == "" {
			files[i].TitleBlock.ProjectNumber = projNum
		}
		if f.TitleBlock.Source == "" {
			files[i].TitleBlock.Source = DeriveDefaultSource(f.Category, f.OriginalName)
		}
		if f.TitleBlock.FigureTitle == "" {
			if f.CustomTitle != "" {
				files[i].TitleBlock.FigureTitle = f.CustomTitle
			} else {
				files[i].TitleBlock.FigureTitle = strings.TrimSuffix(f.OriginalName, filepath.Ext(f.OriginalName))
			}
		}
	}
	return &AppendixPackage{
		ProjectName:   projName,
		ProjectNumber: projNum,
		Files:         files,
	}
}

// GenerateAppendixCoverPages constructs dynamic markdown cover page structures.
func (ap *AppendixPackage) GenerateAppendixCoverPages() map[AppendixSection]string {
	covers := make(map[AppendixSection]string)
	grouped := make(map[AppendixSection][]CategorizedFile)
	for _, f := range ap.Files {
		if f.InAppendix {
			grouped[f.AppendixType] = append(grouped[f.AppendixType], f)
		}
	}

	sections := []AppendixSection{AppendixA, AppendixB, AppendixC, AppendixD, AppendixE, AppendixOther}
	for _, sec := range sections {
		files, exists := grouped[sec]
		if !exists || len(files) == 0 {
			continue
		}
		var sb strings.Builder
		secHeader := strings.Split(string(sec), " - ")[0]
		sb.WriteString(fmt.Sprintf("\n=========================================\n"))
		sb.WriteString(fmt.Sprintf("             %s             \n", strings.ToUpper(secHeader)))
		sb.WriteString(fmt.Sprintf("=========================================\n\n"))
		for _, f := range files {
			title := f.TitleBlock.FigureTitle
			sb.WriteString(fmt.Sprintf("  • %s\n", title))
		}
		covers[sec] = sb.String()
	}
	return covers
}

// GenerateAppendixSummary constructs a manifest table of all included appendices.
func (ap *AppendixPackage) GenerateAppendixSummary() string {
	var sb strings.Builder
	sb.WriteString("=== APPENDIX ATTACHMENT & FIGURE FRAME MANIFEST ===\n")
	sb.WriteString(fmt.Sprintf("Project Name: %s\nProject Number: %s\n", ap.ProjectName, ap.ProjectNumber))
	
	covers := ap.GenerateAppendixCoverPages()
	sections := []AppendixSection{AppendixA, AppendixB, AppendixC, AppendixD, AppendixE, AppendixOther}
	for _, sec := range sections {
		if coverText, ok := covers[sec]; ok {
			sb.WriteString(coverText)
		}
	}
	return sb.String()
}
