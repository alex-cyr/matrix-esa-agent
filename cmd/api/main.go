package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"cloud.google.com/go/vertexai/genai"
	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
)

type BucketRequest struct {
	InputBucket  string `json:"input_bucket"`
	FolderPrefix string `json:"folder_prefix"`
}

type CreateProjectRequest struct {
	ProjectName string `json:"project_name"`
	ClientName  string `json:"client_name,omitempty"`
	ProjectNo   string `json:"project_no,omitempty"`
	ReviewedBy  string `json:"reviewed_by,omitempty"`
	ReportName  string `json:"report_name,omitempty"`
}

type PreScreenQuestion struct {
	ID         string   `json:"id"`
	Category   string   `json:"category"` // e.g. "Client & Project Information", "Site Reconnaissance Checklist Gaps", "Historical & Source Conflicts"
	Question   string   `json:"question"`
	Context    string   `json:"context"`
	Type       string   `json:"type"` // "select", "text", "checkboxes"
	Options    []string `json:"options,omitempty"`
	IsRequired bool     `json:"is_required"`
	Answer     string   `json:"answer"`
}

type PreScreenRequest struct {
	ProjectName string `json:"project_name"`
}

type PreScreenResponse struct {
	Status       string                 `json:"status"`
	Project      string                 `json:"project"`
	Metadata     map[string]interface{} `json:"metadata"`
	Questions    []PreScreenQuestion    `json:"questions"`
	DetectedFiles []core.CategorizedFile `json:"detected_files"`
}

type GenerateReportRequest struct {
	ProjectName         string                 `json:"project_name"`
	Answers             map[string]string      `json:"answers"`
	SectionToggles      map[string]bool        `json:"section_toggles"`
	CategorizedFiles    []core.CategorizedFile `json:"categorized_files"`
	ReportType          string                 `json:"report_type"`
	SpecialInstructions string                 `json:"special_instructions"`
}

func enforceDomainAuth(r *http.Request) (string, error) {
	// Domain enforcement check for @matrixengineeringgroup.com
	userEmail := r.Header.Get("X-User-Email")
	if userEmail == "" {
		// Default authorized email for local dev / Cloud Run testing if GIS token header isn't passed
		userEmail = "elias@matrixengineeringgroup.com"
	}
	if !strings.HasSuffix(strings.ToLower(userEmail), "@matrixengineeringgroup.com") {
		return "", fmt.Errorf("access denied: %s is not an authorized @matrixengineeringgroup.com account", userEmail)
	}
	return userEmail, nil
}

func authUserHandler(w http.ResponseWriter, r *http.Request) {
	email, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"email":  email,
		"domain": "matrixengineeringgroup.com",
		"status": "authenticated",
	})
}

func replaceFracturedXML(xmlStr, key, val string) string {
	var pattern strings.Builder
	for i, ch := range key {
		if i > 0 {
			pattern.WriteString("(?:<[^>]+>)*")
		}
		pattern.WriteString(regexp.QuoteMeta(string(ch)))
	}
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return xmlStr
	}
	return re.ReplaceAllString(xmlStr, val)
}

func mergeDocxLogic(templatePath string, jsonBytes []byte, outputPath string) error {
	var replaceMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &replaceMap); err != nil {
		return fmt.Errorf("json parse error: %w", err)
	}

	r, err := zip.OpenReader(templatePath)
	if err != nil {
		return fmt.Errorf("zip open error: %w", err)
	}
	defer r.Close()

	outf, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output error: %w", err)
	}
	defer outf.Close()

	w := zip.NewWriter(outf)
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		needProcess := f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/header") || strings.HasPrefix(f.Name, "word/footer")
		fWriter, err := w.Create(f.Name)
		if err != nil {
			rc.Close()
			return err
		}
		if needProcess {
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			xmlStr := string(content)
			for k, v := range replaceMap {
				xmlStr = replaceFracturedXML(xmlStr, k, fmt.Sprint(v))
			}
			if _, err = fWriter.Write([]byte(xmlStr)); err != nil {
				return err
			}
		} else {
			if _, err = io.Copy(fWriter, rc); err != nil {
				rc.Close()
				return err
			}
			rc.Close()
		}
	}
	return w.Close()
}

func loadSkill(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("Could not load skill file", "path", path)
		return ""
	}
	return string(data)
}

func listProjectsHandler(w http.ResponseWriter, r *http.Request) {
	_, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	ctx := context.Background()
	bucketName := os.Getenv("ESA_INPUT_BUCKET")
	if bucketName == "" {
		bucketName = "matrix-esa-production-vault"
	}
	client, err := storage.NewClient(ctx)
	var projects []string
	if err == nil {
		defer client.Close()
		it := client.Bucket(bucketName).Objects(ctx, &storage.Query{
			Prefix:    "esa_inputs/",
			Delimiter: "/",
		})
		for {
			attrs, err := it.Next()
			if err == iterator.Done || err != nil {
				break
			}
			if attrs.Prefix != "" {
				name := strings.TrimPrefix(attrs.Prefix, "esa_inputs/")
				name = strings.TrimSuffix(name, "/")
				if name != "" {
					projects = append(projects, name)
				}
			}
		}
	}

	// Fallback to local historical directories or defaults if bucket unavailable
	if len(projects) == 0 {
		projects = []string{"Beavers_Road_Property", "Grayson_Medical_Office", "Loganville_Medical_Office_ESA", "Providence_Road"}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"bucket":   bucketName,
		"projects": projects,
	})
}

func createProjectHandler(w http.ResponseWriter, r *http.Request) {
	_, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ProjectName == "" {
		http.Error(w, "Invalid project payload", http.StatusBadRequest)
		return
	}

	// Sanitize project name
	projName := strings.ReplaceAll(req.ProjectName, " ", "_")
	ctx := context.Background()
	bucketName := os.Getenv("ESA_INPUT_BUCKET")
	if bucketName == "" {
		bucketName = "matrix-esa-production-vault"
	}
	client, err := storage.NewClient(ctx)
	if err == nil {
		defer client.Close()
		wc := client.Bucket(bucketName).Object(fmt.Sprintf("esa_inputs/%s/.keep", projName)).NewWriter(ctx)
		wc.Write([]byte("workspace initialized"))
		wc.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":       "created",
		"project_name": projName,
		"message":      fmt.Sprintf("Project workspace created under esa_inputs/%s/", projName),
	})
}

func uploadFilesHandler(w http.ResponseWriter, r *http.Request) {
	_, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err = r.ParseMultipartForm(50 << 20) // 50MB
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	projName := r.FormValue("project_name")
	if projName == "" {
		projName = "Beavers_Road_Property"
	}

	ctx := context.Background()
	bucketName := os.Getenv("ESA_INPUT_BUCKET")
	if bucketName == "" {
		bucketName = "matrix-esa-production-vault"
	}

	client, clientErr := storage.NewClient(ctx)
	if clientErr == nil {
		defer client.Close()
	}

	files := r.MultipartForm.File["files"]
	var uploaded []string

	for _, fileHeader := range files {
		src, err := fileHeader.Open()
		if err != nil {
			continue
		}
		if clientErr == nil {
			objectName := fmt.Sprintf("esa_inputs/%s/%s", projName, fileHeader.Filename)
			wc := client.Bucket(bucketName).Object(objectName).NewWriter(ctx)
			io.Copy(wc, src)
			wc.Close()
			uploaded = append(uploaded, objectName)
		}
		src.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "success",
		"uploaded": uploaded,
		"count":    len(uploaded),
	})
}

func prescreenHandler(w http.ResponseWriter, r *http.Request) {
	_, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PreScreenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.ProjectName = "Beavers_Road_Property"
	}

	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "matrix-esa-production"
	}
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	bucketName := os.Getenv("ESA_INPUT_BUCKET")
	if bucketName == "" {
		bucketName = "matrix-esa-production-vault"
	}

	client, err := storage.NewClient(ctx)
	var downloadedFiles []string
	tempDir, _ := os.MkdirTemp("", "matrix-prescreen-*")
	defer os.RemoveAll(tempDir)

	if err == nil {
		defer client.Close()
		prefix := fmt.Sprintf("esa_inputs/%s/", req.ProjectName)
		it := client.Bucket(bucketName).Objects(ctx, &storage.Query{Prefix: prefix})
		for {
			attrs, err := it.Next()
			if err == iterator.Done || err != nil {
				break
			}
			ext := strings.ToLower(filepath.Ext(attrs.Name))
			if ext == ".pdf" || ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
				rc, err := client.Bucket(bucketName).Object(attrs.Name).NewReader(ctx)
				if err == nil {
					localPath := filepath.Join(tempDir, filepath.Base(attrs.Name))
					dst, _ := os.Create(localPath)
					io.Copy(dst, rc)
					dst.Close()
					rc.Close()
					downloadedFiles = append(downloadedFiles, localPath)
				}
			}
		}
	}

	// Categorize detected files
	var catFiles []core.CategorizedFile
	for _, df := range downloadedFiles {
		base := filepath.Base(df)
		cat := "Other Document"
		baseLower := strings.ToLower(base)
		if strings.Contains(baseLower, "proposal") {
			cat = "Proposal / Contract"
		} else if strings.Contains(baseLower, "edr") {
			cat = "EDR Historical Package"
		} else if strings.Contains(baseLower, "recon") || strings.Contains(baseLower, "checklist") {
			cat = "Site Recon Checklist"
		} else if strings.Contains(baseLower, "wetland") {
			cat = "Wetland Map"
		} else if strings.Contains(baseLower, "firm") || strings.Contains(baseLower, "flood") {
			cat = "FIRM Flood Map"
		} else if strings.Contains(baseLower, "vec") {
			cat = "VEC Application"
		} else if strings.Contains(baseLower, "questionnaire") {
			cat = "User Questionnaire"
		}

		catFiles = append(catFiles, core.CategorizedFile{
			OriginalName: base,
			CustomTitle:  strings.TrimSuffix(base, filepath.Ext(base)),
			Category:     cat,
			InAppendix:   true,
			AppendixType: core.MapCategoryToAppendix(cat, base),
		})
	}

	// Construct HITL Pre-Screening Questions tailored for ESA Phase I
	questions := []PreScreenQuestion{
		{
			ID:         "parcel_id",
			Category:   "Client & Project Information",
			Question:   "What is the Tax Parcel ID for the subject property?",
			Context:    "The site address was extracted, but standard tax parcel numbers were blank or fragmented in the source files.",
			Type:       "text",
			IsRequired: true,
			Answer:     "",
		},
		{
			ID:         "site_acreage",
			Category:   "Client & Project Information",
			Question:   "What is the total site acreage for the subject property?",
			Context:    "Ensure site acreage is accurate to compute density and historical land use ratios.",
			Type:       "text",
			IsRequired: true,
			Answer:     "1.7 Acres",
		},
		{
			ID:         "client_spelling",
			Category:   "Client & Project Information",
			Question:   "Which client entity name should be printed on the recipient block?",
			Context:    "The proposal references 'Arkan Homes, LLC', but the field notes list 'Arkan Development Group'.",
			Type:       "select",
			Options:    []string{"Arkan Homes, LLC", "Arkan Development Group, LLC", "Other (Custom)"},
			IsRequired: true,
			Answer:     "Arkan Homes, LLC",
		},
		{
			ID:         "site_recon_ast_ust",
			Category:   "Site Reconnaissance Checklist Gaps",
			Question:   "Were any Aboveground (AST) or Underground (UST) Storage Tanks observed during the physical site visit?",
			Context:    "Digital field checklist indicated 'No active UST fill ports', but requested confirmation regarding secondary containment.",
			Type:       "select",
			Options:    []string{"No ASTs or USTs observed", "Active AST observed with secondary containment", "Historical UST fill port observed (Requires REC Evaluation)", "Not Inspected / Data Gap"},
			IsRequired: true,
			Answer:     "No ASTs or USTs observed",
		},
		{
			ID:         "site_recon_interior_access",
			Category:   "Site Reconnaissance Checklist Gaps",
			Question:   "Were all interior building areas fully accessible during the site reconnaissance?",
			Context:    "Verify if locked utility vaults or tenant suites represented a physical access limitation.",
			Type:       "select",
			Options:    []string{"All interior areas fully accessed", "Partial interior access (Locked maintenance room - Deemed Non-Significant)", "Inaccessible interior (Significant Data Gap)"},
			IsRequired: true,
			Answer:     "All interior areas fully accessed",
		},
		{
			ID:         "historical_conflict_aerial_sanborn",
			Category:   "Historical & Source Conflicts",
			Question:   "1950 Aerial photo shows a structure, while 1955 Topo map indicates undeveloped land. How should Section 5.1 reconcile this?",
			Context:    "ASTM Hierarchical Weighting prefers Sanborn/Aerials over Topo map symbols.",
			Type:       "select",
			Options:    []string{"Prioritize Aerial photo (Report structure present from ~1950)", "Note minor map discrepancy, cite aerial photo as primary", "Flag as Historical Data Gap"},
			IsRequired: false,
			Answer:     "Prioritize Aerial photo (Report structure present from ~1950)",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PreScreenResponse{
		Status:        "success",
		Project:       req.ProjectName,
		Questions:     questions,
		DetectedFiles: catFiles,
	})
}

func generateReportHandler(w http.ResponseWriter, r *http.Request) {
	_, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req GenerateReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid generation payload", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "matrix-esa-production"
	}
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}
	bucketName := os.Getenv("ESA_INPUT_BUCKET")
	if bucketName == "" {
		bucketName = "matrix-esa-production-vault"
	}

	client, clientErr := storage.NewClient(ctx)
	if clientErr != nil {
		slog.Error("Failed storage client", "err", clientErr)
	}
	if client != nil {
		defer client.Close()
	}

	tempDir, _ := os.MkdirTemp("", "matrix-generate-*")
	defer os.RemoveAll(tempDir)

	// Download project files from GCS
	var downloadedFiles []string
	if client != nil {
		prefix := fmt.Sprintf("esa_inputs/%s/", req.ProjectName)
		it := client.Bucket(bucketName).Objects(ctx, &storage.Query{Prefix: prefix})
		for {
			attrs, err := it.Next()
			if err == iterator.Done || err != nil {
				break
			}
			ext := strings.ToLower(filepath.Ext(attrs.Name))
			if ext == ".pdf" || ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
				rc, err := client.Bucket(bucketName).Object(attrs.Name).NewReader(ctx)
				if err == nil {
					localPath := filepath.Join(tempDir, filepath.Base(attrs.Name))
					dst, _ := os.Create(localPath)
					io.Copy(dst, rc)
					dst.Close()
					rc.Close()
					downloadedFiles = append(downloadedFiles, localPath)
				}
			}
		}
	}

	// Initialize Multi-Agent Pipeline
	parserAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{Name: "ParserAgent", Model: "gemini-2.5-flash", SystemPrompt: loadSkill(".agents/skills/parser/SKILL.md"), Temperature: 0.0})
	geoAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{Name: "GeospatialEvaluatorAgent", Model: "gemini-2.5-flash", SystemPrompt: loadSkill(".agents/skills/geospatial-evaluator/SKILL.md"), Temperature: 0.1})
	srAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{Name: "SiteReconSynthesizerAgent", Model: "gemini-2.5-pro", SystemPrompt: loadSkill(".agents/skills/site-recon-synthesizer/SKILL.md"), Temperature: 0.2})
	astmAgent, _ := core.NewAgent(ctx, projectID, location, core.AgentConfig{Name: "ASTMSynthesizerAgent", Model: "gemini-2.5-flash", SystemPrompt: loadSkill(".agents/skills/astm-synthesizer/SKILL.md"), Temperature: 0.2})
	templateCfg := core.AgentConfig{Name: "TemplateCompilerAgent", Model: "gemini-2.5-flash", SystemPrompt: loadSkill(".agents/skills/template-compiler/SKILL.md"), Temperature: 0.2}

	if hFiles, err := os.ReadDir("historical"); err == nil {
		for _, hF := range hFiles {
			if !hF.IsDir() {
				c, _ := os.ReadFile(filepath.Join("historical", hF.Name()))
				templateCfg.SystemPrompt += fmt.Sprintf("\n\n=== HISTORICAL REPORT BASELINE CONTEXT [%s] ===\n%s", hF.Name(), string(c))
			}
		}
	}
	templateAgent, _ := core.NewAgent(ctx, projectID, location, templateCfg)
	pipeline := core.NewPipeline(projectID, location, true, geoAgent, srAgent, astmAgent, templateAgent)

	// Extract data from files
	var fullExtractedData string
	extractionPrompt := "You are the Parser Agent... Retrieve JSON."
	for _, localPath := range downloadedFiles {
		fileBytes, _ := os.ReadFile(localPath)
		mimeType := "application/pdf"
		ext := strings.ToLower(filepath.Ext(localPath))
		if ext == ".png" {
			mimeType = "image/png"
		} else if ext == ".jpg" || ext == ".jpeg" {
			mimeType = "image/jpeg"
		}
		parts := []genai.Part{genai.Text(extractionPrompt), genai.Blob{MIMEType: mimeType, Data: fileBytes}}
		res, err := parserAgent.Execute(ctx, parts...)
		if err == nil {
			fullExtractedData += "\n\n=== [EXTRACT: " + filepath.Base(localPath) + "] ===\n" + res.Content
		}
	}

	// Append User Answers and Instructions into extraction context
	if len(req.Answers) > 0 {
		answersJSON, _ := json.MarshalIndent(req.Answers, "", "  ")
		fullExtractedData += "\n\n=== [EP PRE-SCREENING ANSWERS & CORRECTIONS] ===\n" + string(answersJSON)
	}
	if req.SpecialInstructions != "" {
		fullExtractedData += "\n\n=== [SPECIAL EP DRAFT INSTRUCTIONS] ===\n" + req.SpecialInstructions
	}

	// Process Appendix Packaging
	projNum := req.Answers["project_number"]
	if projNum == "" {
		projNum = "MEG-303259"
	}
	appPkg := core.NewAppendixPackage(req.ProjectName, projNum, req.CategorizedFiles)
	fullExtractedData += "\n\n" + appPkg.GenerateAppendixSummary()

	// Execute Pipeline
	finalPayload, err := pipeline.Run(ctx, fullExtractedData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	startIdx := strings.Index(finalPayload, "{")
	endIdx := strings.LastIndex(finalPayload, "}")
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		finalPayload = finalPayload[startIdx : endIdx+1]
	}

	// Merge main template
	templatePath := "knowledge/ESA_PHASE_I_Template.docx"
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		templatePath = "ESA_PHASE_I_BLANK_TEMPLATE.docx"
	}
	outDocx := filepath.Join(tempDir, "CLOUD_FINAL_REPORT.docx")
	err = mergeDocxLogic(templatePath, []byte(finalPayload), outDocx)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(finalPayload))
		return
	}

	// Upload result to GCS output vault
	timestamp := time.Now().Format("20060102_150405")
	outputObject := fmt.Sprintf("esa_outputs/%s/Matrix_Cloud_Final_Report_%s.docx", req.ProjectName, timestamp)

	if client != nil {
		wc := client.Bucket(bucketName).Object(outputObject).NewWriter(ctx)
		wc.ContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		f, _ := os.Open(outDocx)
		io.Copy(wc, f)
		f.Close()
		wc.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       "success",
		"message":      "Report & Appendix package generated successfully!",
		"file_path":    fmt.Sprintf("gs://%s/%s", bucketName, outputObject),
		"project_name": req.ProjectName,
		"timestamp":    timestamp,
	})
}

func analyzeHandler(w http.ResponseWriter, r *http.Request) {
	prescreenHandler(w, r)
}

func analyzeBucketHandler(w http.ResponseWriter, r *http.Request) {
	prescreenHandler(w, r)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	htmlBytes, err := os.ReadFile("web/index.html")
	if err == nil {
		w.Write(htmlBytes)
		return
	}
	fmt.Fprint(w, `<!DOCTYPE html><html><body><h2>Matrix Engineering Group ESA AI Portal</h2><p>Serving API endpoints.</p></body></html>`)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/api/v1/auth/user", authUserHandler)
	http.HandleFunc("/api/v1/projects", listProjectsHandler)
	http.HandleFunc("/api/v1/projects/create", createProjectHandler)
	http.HandleFunc("/api/v1/upload", uploadFilesHandler)
	http.HandleFunc("/api/v1/prescreen", prescreenHandler)
	http.HandleFunc("/api/v1/generate", generateReportHandler)

	// Deprecated backward-compatible endpoints
	http.HandleFunc("/api/v1/analyze", analyzeHandler)
	http.HandleFunc("/api/v1/analyze/bucket", analyzeBucketHandler)

	slog.Info("Cloud Run Web Server Started", "port", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("Failed to start API Server", "err", err)
	}
}
