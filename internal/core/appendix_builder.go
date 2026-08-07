package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jung-kurt/gofpdf"
	"github.com/jung-kurt/gofpdf/contrib/gofpdi"
)

type AppendixGroup struct {
	Letter      string
	Title       string
	Description string
	Files       []CategorizedFile
}

// DrawExactMatrixFigureFrame draws the exact Matrix Appendix Figure Box layout matching user's templates.
func DrawExactMatrixFigureFrame(pdf *gofpdf.Fpdf, mainTitle string, subTitle string, sourceStr string, projName string, projNum string) {
	// Outer Border Rect
	pdf.SetDrawColor(40, 40, 40)
	pdf.SetLineWidth(0.5)
	pdf.Rect(15, 15, 185, 217, "D")

	// Divider line above Title Row
	pdf.Line(15, 193, 200, 193)

	// Title Row Box Text
	pdf.SetY(194)
	pdf.SetFont("Arial", "B", 13)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(0, 6, mainTitle, "", 1, "C", false, 0, "")
	if subTitle != "" {
		pdf.SetY(200)
		pdf.SetFont("Arial", "B", 12)
		pdf.CellFormat(0, 5, subTitle, "", 1, "C", false, 0, "")
	}

	// Divider line above Metadata Row
	pdf.Line(15, 205, 200, 205)

	// Middle Vertical Divider Line for Metadata Row
	pdf.Line(107.5, 205, 107.5, 232)

	// Left Metadata Box: Source / Project Number
	pdf.SetY(207)
	pdf.SetX(18)
	pdf.SetFont("Arial", "", 9)
	pdf.SetTextColor(50, 50, 50)
	pdf.CellFormat(85, 5, fmt.Sprintf("Source: %s", sourceStr), "", 1, "L", false, 0, "")
	if sourceStr == "EDR Report" || strings.Contains(sourceStr, "EDR") {
		pdf.SetX(18)
		pdf.CellFormat(85, 5, fmt.Sprintf("Project Number: %s", projNum), "", 1, "L", false, 0, "")
	}

	// Right Metadata Box: Project Name & Project Number
	pdf.SetY(207)
	pdf.SetX(110)
	pdf.CellFormat(88, 5, fmt.Sprintf("Project Name: %s", projName), "", 1, "L", false, 0, "")
	pdf.SetX(110)
	pdf.CellFormat(88, 5, fmt.Sprintf("Project Number: %s", projNum), "", 1, "L", false, 0, "")

	// Bottom Centered Matrix Logo Emblem & Brand Text
	pdf.SetY(237)
	pdf.SetFont("Arial", "B", 14)
	pdf.SetTextColor(112, 36, 25) // Matrix Deep Maroon Logo Color (#702419)
	pdf.CellFormat(0, 6, "M", "", 1, "C", false, 0, "")

	pdf.SetY(243)
	pdf.SetFont("Arial", "B", 10)
	pdf.SetTextColor(30, 30, 30)
	pdf.CellFormat(0, 5, "Matrix Engineering Group, Inc.", "", 1, "C", false, 0, "")
}

// BuildCompleteAppendixPDF compiles all cover pages and actual attached PDF/Image documents into one master Appendix PDF deliverable.
func BuildCompleteAppendixPDF(projName, projNum, inputFilesDir, outPdfPath string, files []CategorizedFile) error {
	cleanProjName := strings.ReplaceAll(projName, "_", " ")

	pdf := gofpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(false, 0)

	// 1. Master Title Cover Page
	pdf.AddPage()
	pdf.SetY(60)
	pdf.SetFont("Arial", "B", 20)
	pdf.SetTextColor(112, 36, 25) // Matrix Logo Deep Maroon (#702419)
	pdf.CellFormat(0, 12, "MATRIX ENGINEERING GROUP, INC.", "", 1, "C", false, 0, "")

	pdf.SetFont("Arial", "B", 14)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(0, 10, "PHASE I ENVIRONMENTAL SITE ASSESSMENT", "", 1, "C", false, 0, "")
	pdf.Ln(15)

	pdf.SetFont("Arial", "B", 24)
	pdf.SetTextColor(112, 36, 25)
	pdf.CellFormat(0, 14, "APPENDICES PACKAGE", "", 1, "C", false, 0, "")
	pdf.Ln(4)

	pdf.SetFont("Arial", "I", 11)
	pdf.SetTextColor(90, 90, 90)
	pdf.CellFormat(0, 6, "Phase I Environmental Site Assessment Document & Figure Package", "", 1, "C", false, 0, "")

	pdf.SetY(210)
	pdf.SetFont("Arial", "", 11)
	pdf.SetTextColor(50, 50, 50)
	pdf.CellFormat(0, 6, fmt.Sprintf("Subject Property: %s", cleanProjName), "", 1, "C", false, 0, "")
	pdf.CellFormat(0, 6, fmt.Sprintf("Project Number: %s", projNum), "", 1, "C", false, 0, "")

	groups := []AppendixGroup{
		{
			Letter:      "APPENDIX A",
			Title:       "APPENDIX A - SITE PLAN, FIRM, NWI MAPS & HISTORICAL AERIAL PHOTOGRAPHS",
			Description: "Contains Site Plan, Flood Insurance Rate Maps (FIRM), National Wetland Inventory Map (NWI), USGS Topographic Quadrangle Maps, and Historical Aerial Photographs.",
		},
		{
			Letter:      "APPENDIX B",
			Title:       "APPENDIX B - PHOTOGRAPHS",
			Description: "Contains Filio Site Reconnaissance Inspection Photographs and Field Observation Logs.",
		},
		{
			Letter:      "APPENDIX C",
			Title:       "APPENDIX C - EDR RADIUS MAP REPORT",
			Description: "Contains The Environmental Data Resources (EDR) Radius Map Report and Regulatory Database Listings.",
		},
		{
			Letter:      "APPENDIX D",
			Title:       "APPENDIX D - PHASE I PROPERTY OWNER / USER INTERVIEW QUESTIONNAIRE",
			Description: "Contains User Questionnaire and Property Owner Representative Interview Records.",
		},
		{
			Letter:      "APPENDIX E",
			Title:       "APPENDIX E - SANBORN MAP REPORT",
			Description: "Contains Certified Sanborn Fire Insurance Map Report.",
		},
		{
			Letter:      "APPENDIX F",
			Title:       "APPENDIX F - VAPOR ENCROACHMENT SCREEN REPORT",
			Description: "Contains Vapor Encroachment Condition (VEC) Screening Documentation.",
		},
		{
			Letter:      "APPENDIX G",
			Title:       "APPENDIX G - ENVIRONMENTAL LIEN AND AUL SEARCH REPORT",
			Description: "Contains Environmental Lien and Activity & Use Limitation (AUL) Search Documentation.",
		},
		{
			Letter:      "APPENDIX H",
			Title:       "APPENDIX H - QUALIFICATIONS",
			Description: "Contains Qualifications of Environmental Professional (EP).",
		},
	}

	// Map input files into groups
	for i := range groups {
		for _, f := range files {
			if !f.InAppendix {
				continue
			}
			sec := MapCategoryToAppendix(f.Category, f.OriginalName)
			secUpper := strings.ToUpper(string(sec))
			grpUpper := strings.ToUpper(groups[i].Letter)
			if strings.HasPrefix(secUpper, grpUpper) {
				groups[i].Files = append(groups[i].Files, f)
			}
		}
	}

	aerialYears := []string{"1949", "1951", "1955", "1966", "1972", "1978", "1988", "1999", "2007", "2015", "2023"}

	for _, grp := range groups {
		// Section Cover Page
		pdf.AddPage()
		pdf.SetY(80)
		pdf.SetFont("Arial", "B", 20)
		pdf.SetTextColor(112, 36, 25)
		pdf.CellFormat(0, 12, grp.Letter, "", 1, "C", false, 0, "")
		pdf.Ln(5)
		pdf.SetFont("Arial", "B", 14)
		pdf.SetTextColor(40, 40, 40)
		pdf.MultiCell(0, 8, grp.Title, "", "C", false)
		pdf.Ln(8)
		pdf.SetFont("Arial", "I", 11)
		pdf.SetTextColor(90, 90, 90)
		pdf.MultiCell(0, 6, grp.Description, "", "C", false)

		if len(grp.Files) == 0 {
			pdf.SetY(150)
			pdf.SetFont("Arial", "I", 11)
			pdf.SetTextColor(120, 120, 120)
			pdf.CellFormat(0, 8, "[ PENDING EP INSERTION / DOCUMENT NOT PROVIDED ]", "", 1, "C", false, 0, "")
		} else {
			yearIdx := 0
			for _, f := range grp.Files {
				srcPath := filepath.Join(inputFilesDir, f.OriginalName)
				if _, err := os.Stat(srcPath); os.IsNotExist(err) {
					continue
				}

				ext := strings.ToLower(filepath.Ext(srcPath))
				nameLower := strings.ToLower(f.OriginalName)

				if grp.Letter == "APPENDIX A" {
					if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
						pdf.AddPage()
						DrawExactMatrixFigureFrame(pdf, f.CustomTitle, "", "Federal Emergency Management Agency", cleanProjName, projNum)
						var opt gofpdf.ImageOptions
						pdf.ImageOptions(srcPath, 15.5, 15.5, 184, 177, false, opt, 0, "")
					} else if ext == ".pdf" {
						p := 1
						if strings.Contains(nameLower, "aerial") {
							p = 3 // Skip decade cover pages
						}
						for {
							var tpl int
							var importErr error
							func() {
								defer func() {
									if r := recover(); r != nil {
										importErr = fmt.Errorf("end of pdf: %v", r)
									}
								}()
								tpl = gofpdi.ImportPage(pdf, srcPath, p, "/MediaBox")
							}()
							if importErr != nil || tpl == 0 {
								break
							}
							pdf.AddPage()
							subT := ""
							if strings.Contains(nameLower, "aerial") && yearIdx < len(aerialYears) {
								subT = aerialYears[yearIdx]
								yearIdx++
							}
							srcLabel := "EDR Report"
							if strings.Contains(nameLower, "topo") {
								srcLabel = "EDR Report"
							}
							DrawExactMatrixFigureFrame(pdf, f.CustomTitle, subT, srcLabel, cleanProjName, projNum)
							gofpdi.UseImportedTemplate(pdf, tpl, 15.5, 15.5, 184, 177)
							p++
						}
					}
				} else if grp.Letter == "APPENDIX B" {
					if ext == ".pdf" {
						p := 1
						for {
							var tpl int
							var importErr error
							func() {
								defer func() {
									if r := recover(); r != nil {
										importErr = fmt.Errorf("end of pdf: %v", r)
									}
								}()
								tpl = gofpdi.ImportPage(pdf, srcPath, p, "/MediaBox")
							}()
							if importErr != nil || tpl == 0 {
								break
							}
							pdf.AddPage()
							DrawExactMatrixFigureFrame(pdf, "Site Inspection Photograph", fmt.Sprintf("Page %d", p), "Site Reconnaissance", cleanProjName, projNum)
							gofpdi.UseImportedTemplate(pdf, tpl, 15.5, 15.5, 184, 177)
							p++
						}
					}
				} else {
					// Append full raw multi-page report PDFs (EDR Radius Map, Sanborn, VEC, Proposal)
					if ext == ".pdf" {
						p := 1
						for {
							var tpl int
							var importErr error
							func() {
								defer func() {
									if r := recover(); r != nil {
										importErr = fmt.Errorf("end of pdf: %v", r)
									}
								}()
								tpl = gofpdi.ImportPage(pdf, srcPath, p, "/MediaBox")
							}()
							if importErr != nil || tpl == 0 {
								break
							}
							pdf.AddPage()
							gofpdi.UseImportedTemplate(pdf, tpl, 0, 0, 215.9, 279.4)
							p++
						}
					}
				}
			}
		}
	}

	return pdf.OutputFileAndClose(outPdfPath)
}
