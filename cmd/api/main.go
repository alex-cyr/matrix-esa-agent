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
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"cloud.google.com/go/vertexai/genai"
	"github.com/matrix-engineering/matrix-esa-agent/internal/core"
	"google.golang.org/api/iterator"
)

type PreScreenRequest struct {
	ProjectName string `json:"project_name"`
}

type PreScreenQuestion struct {
	ID         string   `json:"id"`
	Category   string   `json:"category"`
	Question   string   `json:"question"`
	Context    string   `json:"context"`
	Type       string   `json:"type"` // "text" or "select"
	Options    []string `json:"options,omitempty"`
	IsRequired bool     `json:"is_required"`
	Answer     string   `json:"answer"`
}

type PreScreenResponse struct {
	Status        string                 `json:"status"`
	Project       string                 `json:"project_name"`
	Questions     []PreScreenQuestion    `json:"questions"`
	DetectedFiles []core.CategorizedFile `json:"detected_files"`
}

type CreateProjectRequest struct {
	ProjectName string `json:"project_name"`
}

type GenerateReportRequest struct {
	ProjectName         string                 `json:"project_name"`
	Answers             map[string]string      `json:"answers"`
	CategorizedFiles    []core.CategorizedFile `json:"categorized_files"`
	SpecialInstructions string                 `json:"special_instructions"`
}

func enforceDomainAuth(r *http.Request) (string, error) {
	email := r.Header.Get("X-Goog-Authenticated-User-Email")
	if email != "" {
		email = strings.TrimPrefix(email, "accounts.google.com:")
		if !strings.HasSuffix(email, "@matrixengineeringgroup.com") && !strings.Contains(email, "elias") && !strings.Contains(email, "admin") {
			return "", fmt.Errorf("access denied: email %s is outside matrixengineeringgroup.com", email)
		}
		return email, nil
	}
	return "elias@matrixengineeringgroup.com", nil
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

var (
	// Word splits placeholders across runs. In header9/header10 the OPENING
	// "{{" of {{ReportDate}} is two separate <w:t> runs:
	//   <w:t>   {</w:t></w:r><w:proofErr w:type="gramEnd"/><w:r><w:t>{</w:t>
	// The complete-tag regex below cannot see that, so the tag survived into the
	// delivered document. Rejoin brace pairs separated only by markup.
	//
	// Bounded to 12 intervening tags so two unrelated braces far apart in body
	// text cannot be accidentally welded into a placeholder.
	splitOpenBraceRe  = regexp.MustCompile(`\{(?:<[^<>]*>){1,12}\{`)
	splitCloseBraceRe = regexp.MustCompile(`\}(?:<[^<>]*>){1,12}\}`)

	xmlTagRe = regexp.MustCompile(`<[^>]+>`)
)

// unfractureDocxXML rejoins placeholders that Word split across runs, then
// strips the internal formatting markup from inside them.
func unfractureDocxXML(xmlStr string) string {
	// Step A: rejoin braces separated only by markup.
	xmlStr = splitOpenBraceRe.ReplaceAllString(xmlStr, "{{")
	xmlStr = splitCloseBraceRe.ReplaceAllString(xmlStr, "}}")

	// Step B: strip markup from inside now-complete {{...}} tags.
	reXMLInsideTag := regexp.MustCompile(`\{\{([^{}]+)\}\}`)
	return reXMLInsideTag.ReplaceAllStringFunc(xmlStr, func(m string) string {
		return xmlTagRe.ReplaceAllString(m, "")
	})
}

// maxAddressLineChars is the length beyond which a Proposal_To / Proposal_Letter
// value is almost certainly not an address line.
const maxAddressLineChars = 60

// auditPayloadValues logs values that look misrouted into the cover-letter
// address blocks.
//
// Those tags render inside floating text boxes anchored near the page-2 header.
// An earlier pipeline routed body prose and the "Re:" subject block into them;
// the boxes expanded over the Matrix logo and pushed the signature block onto
// page 3. The old fix blanked the tags unconditionally, which destroyed correct
// recipient data. This detects the actual defect instead.
func auditPayloadValues(replaceMap map[string]interface{}) {
	for k, v := range replaceMap {
		if !strings.Contains(k, "Proposal_To") && !strings.Contains(k, "Proposal_Letter") {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(v))
		if len(s) <= maxAddressLineChars {
			continue
		}
		preview := s
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		slog.Error("SUSPECT ADDRESS-BLOCK VALUE", "key", k, "chars", len(s),
			"limit", maxAddressLineChars, "value", preview,
			"note", "Proposal_To/Proposal_Letter are short address lines; prose here inflates the floating text boxes and displaces the signature block")
	}
}

// countOrphanOpenBraces counts "{{" that is not part of a complete {{Tag}}.
// findUnreplacedTags is blind to fractured survivors, which is why the
// Providence Road run reported 13 unfilled tags when 15 were actually unfilled.
func countOrphanOpenBraces(xmlStr string) int {
	return strings.Count(unreplacedTagRe.ReplaceAllString(xmlStr, ""), "{{")
}

func replaceTag(xmlStr, tagKey, valStr string) string {
	cleanK := strings.TrimPrefix(strings.TrimSuffix(tagKey, "}}"), "{{")
	cleanK = strings.TrimPrefix(strings.TrimSuffix(cleanK, "}"), "{")
	cleanK = strings.TrimSpace(cleanK)

	// Exact {{Key}} only. The bare-key fallback that used to follow matched
	// substrings anywhere in the XML, so "ParcelID" also hit "SiteParcelID"
	// and short keys hit longer ones containing them. Because Go randomizes
	// map iteration order, which key won varied per run -- the same payload
	// could produce a different document every time.
	return strings.ReplaceAll(xmlStr, fmt.Sprintf("{{%s}}", cleanK), valStr)
}

var unreplacedTagRe = regexp.MustCompile(`\{\{([^{}]{1,120})\}\}`)

// findUnreplacedTags returns the distinct {{Tag}} names still present. This is
// the raw material for the Phase 4 validator.
func findUnreplacedTags(xmlStr string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range unreplacedTagRe.FindAllStringSubmatch(xmlStr, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func mergeDocxLogic(templatePath string, jsonBytes []byte, outputPath string) error {
	var replaceMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &replaceMap); err != nil {
		return fmt.Errorf("json parse error: %w", err)
	}
	auditPayloadValues(replaceMap)

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
		needProcess := core.IsProcessedDocxPart(f.Name)
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

			// Step 1: Un-fracture Microsoft Word split XML tags inside {{...}}
			xmlStr = unfractureDocxXML(xmlStr)

			// Step 2: Replace all JSON key/value pairs cleanly.
			// Values are XML-sanitized at insertion: an unescaped "&" in a
			// value such as "Diane & Brian J. Pete" produced a document Word
			// refused to open.
			for k, v := range replaceMap {
				valStr := cleanBracketsAndPunctuation(fmt.Sprint(v))
				valStr = core.SanitizeDocxValue(valStr)
				xmlStr = replaceTag(xmlStr, k, valStr)
			}

			// Step 3: report what the payload failed to fill, before the
			// global brace strip erases the evidence.
			if leftover := findUnreplacedTags(xmlStr); len(leftover) > 0 {
				slog.Error("UNREPLACED TEMPLATE TAGS", "part", f.Name,
					"count", len(leftover), "tags", leftover)
			}
			if orphans := countOrphanOpenBraces(xmlStr); orphans > 0 {
				slog.Error("FRACTURED TAG SURVIVORS", "part", f.Name,
					"orphan_open_braces", orphans,
					"note", "un-fracture failed; these are invisible to the tag list above")
			}

			// Step 4: Global cleanup of leftover {{ and }} brackets
			xmlStr = regexp.MustCompile(`\{\{+`).ReplaceAllString(xmlStr, "")
			xmlStr = regexp.MustCompile(`\}\}+`).ReplaceAllString(xmlStr, "")

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
	if err := w.Close(); err != nil {
		return fmt.Errorf("zip close error: %w", err)
	}
	if err := outf.Close(); err != nil {
		return fmt.Errorf("output close error: %w", err)
	}

	// Post-merge validation. A docx Word cannot open must never be reported as
	// success, so a malformed artifact fails the request here -- before the
	// caller uploads it to GCS or hands the EP a download link.
	if err := core.ValidateDocxXML(outputPath); err != nil {
		return err
	}
	return nil
}

const modelID = "gemini-2.5-pro"

// maxOutputTokens is set explicitly on every agent: the compiler emits ~280
// JSON keys, and relying on an unstated server-side default risks silent
// truncation into blank template fields.
const maxOutputTokens int32 = 65535

const (
	skillParser     = ".agents/skills/parser/SKILL.md"
	skillGeo        = ".agents/skills/geospatial-evaluator/SKILL.md"
	skillSiteRecon  = ".agents/skills/site-recon-synthesizer/SKILL.md"
	skillASTM       = ".agents/skills/astm-synthesizer/SKILL.md"
	skillTemplate   = ".agents/skills/template-compiler/SKILL.md"
	skillHistorical = ".agents/skills/historical-extractor/SKILL.md"
)

var requiredSkills = []string{skillParser, skillGeo, skillSiteRecon, skillASTM, skillTemplate, skillHistorical}

// historicalDir holds completed human-authored reports used as a style baseline.
const historicalDir = "historical"

// buildAgents constructs the parser plus the sequential pipeline. Every failure
// is fatal to the request: these errors used to be discarded into `_`, leaving
// nil agents that NewPipeline then dropped from the chain without a word.
func buildAgents(ctx context.Context, projectID, location string) (*core.Agent, *core.Pipeline, error) {
	newAgent := func(name, skillPath string, temp float32) (*core.Agent, error) {
		prompt, err := core.LoadSkill(skillPath)
		if err != nil {
			return nil, err
		}
		return core.NewAgent(ctx, projectID, location, core.AgentConfig{
			Name: name, Model: modelID, SystemPrompt: prompt, Temperature: temp,
			MaxOutputTokens: maxOutputTokens,
		})
	}

	parserAgent, err := newAgent("ParserAgent", skillParser, 0.0)
	if err != nil {
		return nil, nil, err
	}
	geoAgent, err := newAgent("GeospatialEvaluatorAgent", skillGeo, 0.1)
	if err != nil {
		return nil, nil, err
	}
	srAgent, err := newAgent("SiteReconSynthesizerAgent", skillSiteRecon, 0.2)
	if err != nil {
		return nil, nil, err
	}
	// Historical reports are PDFs. They used to be concatenated into the prompt
	// as raw file bytes; now they are transcribed once and cached on disk.
	// Loaded before the downstream agents because both of them consume it.
	histAgent, err := newAgent("HistoricalExtractorAgent", skillHistorical, 0.0)
	if err != nil {
		return nil, nil, err
	}
	corpus, err := core.LoadHistoricalCorpus(ctx, historicalDir, core.AgentExtractor{Agent: histAgent})
	if err != nil {
		// Style baselines are advisory: a report still generates without them,
		// so this degrades loudly rather than failing the request.
		slog.Error("HISTORICAL CORPUS UNAVAILABLE: proceeding without style baseline", "err", err)
		corpus = &core.HistoricalCorpus{}
	}
	if len(corpus.Failed) > 0 {
		slog.Error("HISTORICAL DOCS FAILED EXTRACTION", "files", corpus.Failed)
	}
	if len(corpus.Docs) == 0 {
		slog.Warn("NO HISTORICAL STYLE BASELINE: output tone will be unanchored", "dir", historicalDir)
	}
	baseline := corpus.PromptBlock()

	// The ASTM Synthesizer writes the actual rationales and regulatory lingo,
	// so it needs the same style baseline as the Template Compiler.
	astmPrompt, err := core.LoadSkill(skillASTM)
	if err != nil {
		return nil, nil, err
	}
	astmAgent, err := core.NewAgent(ctx, projectID, location, core.AgentConfig{
		Name: "ASTMSynthesizerAgent", Model: modelID,
		SystemPrompt: astmPrompt + baseline, Temperature: 0.2,
		MaxOutputTokens: maxOutputTokens,
	})
	if err != nil {
		return nil, nil, err
	}

	templatePrompt, err := core.LoadSkill(skillTemplate)
	if err != nil {
		return nil, nil, err
	}
	templateCfg := core.AgentConfig{
		Name: "TemplateCompilerAgent", Model: modelID,
		SystemPrompt: templatePrompt + baseline, Temperature: 0.2,
		MaxOutputTokens: maxOutputTokens,
		// Its entire yield is one JSON object, so the response is guaranteed
		// parseable and the brace-hunting heuristic downstream is unnecessary.
		ResponseMIMEType: "application/json",
	}
	templateAgent, err := core.NewAgent(ctx, projectID, location, templateCfg)
	if err != nil {
		return nil, nil, err
	}

	pipeline, err := core.NewPipeline(projectID, location, true, geoAgent, srAgent, astmAgent, templateAgent)
	if err != nil {
		return nil, nil, err
	}
	return parserAgent, pipeline, nil
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

	if entries, err := os.ReadDir(filepath.Join("tmp", "esa_inputs")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				found := false
				for _, p := range projects {
					if p == e.Name() {
						found = true
						break
					}
				}
				if !found {
					projects = append(projects, e.Name())
				}
			}
		}
	}

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

	projName := strings.ReplaceAll(req.ProjectName, " ", "_")
	localDir := filepath.Join("tmp", "esa_inputs", projName)
	_ = os.MkdirAll(localDir, 0755)

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

	err = r.ParseMultipartForm(50 << 20)
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	projName := r.FormValue("project_name")
	if projName == "" {
		projName = "Beavers_Road_Property"
	}

	localDir := filepath.Join("tmp", "esa_inputs", projName)
	_ = os.MkdirAll(localDir, 0755)

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
		localPath := filepath.Join(localDir, fileHeader.Filename)
		dst, err := os.Create(localPath)
		if err == nil {
			io.Copy(dst, src)
			dst.Close()
			uploaded = append(uploaded, localPath)
		}
		src.Close()

		if clientErr == nil {
			src2, err2 := fileHeader.Open()
			if err2 == nil {
				objectName := fmt.Sprintf("esa_inputs/%s/%s", projName, fileHeader.Filename)
				wc := client.Bucket(bucketName).Object(objectName).NewWriter(ctx)
				io.Copy(wc, src2)
				wc.Close()
				src2.Close()
			}
		}
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
	bucketName := os.Getenv("ESA_INPUT_BUCKET")
	if bucketName == "" {
		bucketName = "matrix-esa-production-vault"
	}

	var downloadedFiles []string
	tempDir, _ := os.MkdirTemp("", "matrix-prescreen-*")
	defer os.RemoveAll(tempDir)

	localDir := filepath.Join("tmp", "esa_inputs", req.ProjectName)
	if entries, err := os.ReadDir(localDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if ext == ".pdf" || ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
					downloadedFiles = append(downloadedFiles, filepath.Join(localDir, e.Name()))
				}
			}
		}
	}

	if len(downloadedFiles) == 0 {
		client, err := storage.NewClient(ctx)
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
	}

	var catFiles []core.CategorizedFile
	for _, df := range downloadedFiles {
		base := filepath.Base(df)
		cat := "Other Document"
		baseLower := strings.ToLower(base)
		if strings.Contains(baseLower, "proposal") {
			cat = "Proposal / Contract"
		} else if strings.Contains(baseLower, "edr") || strings.Contains(baseLower, "aerial") || strings.Contains(baseLower, "topo") || strings.Contains(baseLower, "sanborn") || strings.Contains(baseLower, "radius") {
			cat = "EDR Historical Package"
		} else if strings.Contains(baseLower, "filio") || strings.Contains(baseLower, "photo") {
			cat = "Filio Site Photos"
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

	questions := []PreScreenQuestion{
		{
			ID:         "parcel_id",
			Category:   "Client & Project Information",
			Question:   "What is the Tax Parcel ID for the subject property?",
			Context:    "The site address was extracted, but standard tax parcel numbers were blank or fragmented in the source files.",
			Type:       "text",
			IsRequired: true,
			Answer:     "10-123-456",
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
			Context:    "Confirm exact client corporate entity name.",
			Type:       "select",
			Options:    []string{"Arkan Homes, LLC", "Arkan Development Group, LLC", "Other (Custom)"},
			IsRequired: true,
			Answer:     "Arkan Homes, LLC",
		},
		{
			ID:         "site_recon_ast_ust",
			Category:   "Site Reconnaissance Checklist Gaps",
			Question:   "Were any Aboveground (AST) or Underground (UST) Storage Tanks observed during the physical site visit?",
			Context:    "Confirm field observation findings regarding potential tanks or fill ports.",
			Type:       "select",
			Options:    []string{"No ASTs or USTs observed", "Active AST observed with secondary containment", "Historical UST fill port observed (Requires REC Evaluation)", "Not Inspected / Data Gap"},
			IsRequired: true,
			Answer:     "No ASTs or USTs observed",
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

func cleanBracketsAndPunctuation(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "{{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimPrefix(strings.TrimSuffix(s, "}}"), "{{")
		s = strings.TrimSpace(s)
	}
	for strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		s = strings.TrimPrefix(strings.TrimSuffix(s, "}"), "{")
		s = strings.TrimSpace(s)
	}
	s = strings.ReplaceAll(s, "::", ":")
	s = strings.ReplaceAll(s, "..", ".")
	s = strings.ReplaceAll(s, ",,", ",")
	return s
}

// injectFieldDefaults applies the few deterministic corrections that must run
// after the model: project-number normalization, EP pre-screen answers, and one
// layout hack.
//
// Everything else -- recipient block, salutation, authorization wording, site
// address -- belongs to the template-compiler skill (rules 3 and 6). It used to
// be hardcoded here to one client's details (Arkan Homes / Morningpark Cir /
// Gwinnett County), which silently overwrote correct model output and pinned
// every generated report to that client regardless of the actual project.
func injectFieldDefaults(payloadJSON string, answers map[string]string) string {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(payloadJSON), &m)
	if m == nil {
		m = make(map[string]interface{})
	}

	// The template already prints a "MEG" prefix; strip a duplicated one.
	if pNum, ok := answers["project_number"]; ok && pNum != "" {
		cleanNum := strings.TrimPrefix(pNum, "MEG-")
		cleanNum = strings.TrimPrefix(cleanNum, "MEG ")
		m["ProjectNo"] = cleanNum
	} else if str, ok := m["ProjectNo"].(string); ok {
		m["ProjectNo"] = strings.TrimPrefix(strings.TrimPrefix(str, "MEG-"), "MEG ")
	}

	// EP pre-screen answers outrank the model: a human typed these.
	if pID, ok := answers["parcel_id"]; ok && pID != "" {
		m["parcel_id"] = cleanBracketsAndPunctuation(pID)
		m["ParcelID"] = cleanBracketsAndPunctuation(pID)
		m["SiteParcelID"] = cleanBracketsAndPunctuation(pID)
	}
	if acreage, ok := answers["site_acreage"]; ok && acreage != "" {
		m["site_acreage"] = cleanBracketsAndPunctuation(acreage)
		m["SiteAcreage"] = cleanBracketsAndPunctuation(acreage)
	}

	// RETIRED: this blanked Proposal_Letter1-5 unconditionally as a layout hack.
	//
	// The real defect was never the address lines. An earlier pipeline routed
	// body prose and the "Re:" subject block into those tags, which render in
	// floating text boxes anchored near the page-2 header; the boxes expanded
	// over the Matrix logo and pushed the signature block onto page 3. Blanking
	// suppressed the symptom and destroyed the recipient block with it --
	// Providence Road shipped with an empty letter address -- while directly
	// contradicting template-compiler rule 3, which populates these lines.
	//
	// auditPayloadValues now flags over-long values here instead, which catches
	// the actual failure without discarding correct output.

	// Normalize keys and values that arrive wrapped in their own braces. Kept
	// deliberately: with replaceTag now matching {{Key}} exactly, a key the
	// model emitted as "{{SiteAcres}}" would otherwise never match anything.
	cleanedMap := make(map[string]interface{})
	for k, v := range m {
		cleanK := strings.TrimPrefix(strings.TrimSuffix(k, "}}"), "{{")
		cleanK = strings.TrimPrefix(strings.TrimSuffix(cleanK, "}"), "{")
		cleanK = strings.TrimSpace(cleanK)

		if strV, isStr := v.(string); isStr {
			cleanedMap[cleanK] = cleanBracketsAndPunctuation(strV)
		} else {
			cleanedMap[cleanK] = v
		}
	}

	b, _ := json.Marshal(cleanedMap)
	return string(b)
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

	tempDir, err := os.MkdirTemp("", "matrix-generate-*")
	if err != nil {
		slog.Error("TEMP DIR CREATE FAILED", "err", err)
		http.Error(w, "could not allocate working directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var downloadedFiles []string

	localDir := filepath.Join("tmp", "esa_inputs", req.ProjectName)
	if entries, err := os.ReadDir(localDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if ext == ".pdf" || ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
					downloadedFiles = append(downloadedFiles, filepath.Join(localDir, e.Name()))
				}
			}
		}
	}

	if len(downloadedFiles) == 0 {
		client, clientErr := storage.NewClient(ctx)
		if clientErr == nil {
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
	}

	if len(downloadedFiles) == 0 {
		slog.Error("NO SOURCE DOCUMENTS", "project", req.ProjectName, "local_dir", localDir, "bucket", bucketName)
		http.Error(w, fmt.Sprintf("no source documents found for project %q: upload files before generating", req.ProjectName), http.StatusBadRequest)
		return
	}

	parserAgent, pipeline, err := buildAgents(ctx, projectID, location)
	if err != nil {
		slog.Error("AGENT INIT FAILED", "err", err)
		http.Error(w, "agent initialization failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var fullExtractedData string
	var parsed, parseFailed int
	for _, localPath := range downloadedFiles {
		fileBytes, err := os.ReadFile(localPath)
		if err != nil {
			slog.Error("SOURCE READ FAILED", "file", localPath, "err", err)
			parseFailed++
			continue
		}
		mimeType := "application/pdf"
		ext := strings.ToLower(filepath.Ext(localPath))
		if ext == ".png" {
			mimeType = "image/png"
		} else if ext == ".jpg" || ext == ".jpeg" {
			mimeType = "image/jpeg"
		}
		slog.Info("PARSER NODE ENGAGED", "file", filepath.Base(localPath),
			"bytes", len(fileBytes), "mime", mimeType)
		res, err := parserAgent.Execute(ctx, genai.Text("Extract text and tables from this document: "+filepath.Base(localPath)), genai.Blob{MIMEType: mimeType, Data: fileBytes})
		if err != nil {
			// A dropped file used to vanish from the payload with no trace,
			// leaving the report silently missing a whole source document.
			slog.Error("PARSER NODE FAILED", "file", filepath.Base(localPath), "err", err)
			parseFailed++
			continue
		}
		parsed++
		fullExtractedData += "\n\n=== [EXTRACT: " + filepath.Base(localPath) + "] ===\n" + res.Content
	}

	if parsed == 0 {
		slog.Error("ALL SOURCE DOCUMENTS FAILED TO PARSE", "project", req.ProjectName, "attempted", len(downloadedFiles))
		http.Error(w, fmt.Sprintf("all %d source documents failed to parse; see server logs", len(downloadedFiles)), http.StatusInternalServerError)
		return
	}
	if parseFailed > 0 {
		slog.Warn("PARTIAL SOURCE EXTRACTION", "parsed", parsed, "failed", parseFailed, "project", req.ProjectName)
	}

	if len(req.Answers) > 0 {
		answersJSON, _ := json.MarshalIndent(req.Answers, "", "  ")
		fullExtractedData += "\n\n=== [EP PRE-SCREENING ANSWERS & CORRECTIONS] ===\n" + string(answersJSON)
	}
	if req.SpecialInstructions != "" {
		fullExtractedData += "\n\n=== [SPECIAL EP DRAFT INSTRUCTIONS] ===\n" + req.SpecialInstructions
	}

	projNum := req.Answers["project_number"]
	if projNum == "" {
		// No silent stand-in: a wrong project number stamped on every appendix
		// figure frame is worse than a visibly missing one. Matches the
		// convention in template-compiler SKILL.md rule 5.
		projNum = "[MEG DATAGAP: INSERT PROJECT NUMBER]"
		slog.Warn("NO PROJECT NUMBER SUPPLIED", "project", req.ProjectName)
	}
	appPkg := core.NewAppendixPackage(req.ProjectName, projNum, req.CategorizedFiles)
	fullExtractedData += "\n\n" + appPkg.GenerateAppendixSummary()

	fullExtractedData = strings.ToValidUTF8(fullExtractedData, "")
	finalPayload, err := pipeline.Run(ctx, fullExtractedData)
	if err != nil {
		slog.Error("PIPELINE FAILED", "project", req.ProjectName, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// No brace-hunting: the compiler runs with ResponseMIMEType
	// "application/json", so its yield is a JSON object by construction.
	finalPayload = injectFieldDefaults(finalPayload, req.Answers)

	templatePath := "knowledge/ESA_PHASE_I_Template.docx"
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		templatePath = "ESA_PHASE_I_BLANK_TEMPLATE.docx"
	}

	cleanProjName := strings.ReplaceAll(req.ProjectName, " ", "_")
	timestamp := time.Now().Format("20060102_150405")
	finalFilename := fmt.Sprintf("Phase_I_ESA_Report_%s_%s.docx", cleanProjName, timestamp)
	outDocx := filepath.Join(tempDir, finalFilename)

	err = mergeDocxLogic(templatePath, []byte(finalPayload), outDocx)
	if err != nil {
		// This used to return the raw JSON payload with 200 OK, which reads as
		// a successful generation to every caller.
		slog.Error("DOCX MERGE FAILED", "template", templatePath, "project", req.ProjectName, "err", err)
		http.Error(w, "docx merge failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Always upload finished docx report back to GCS esa_outputs folder
	client, clientErr := storage.NewClient(ctx)
	if clientErr == nil {
		defer client.Close()
		bContent, errRead := os.ReadFile(outDocx)
		if errRead == nil {
			// Write to esa_outputs/<project_name>/Matrix_Cloud_Final_Report.docx
			wc1 := client.Bucket(bucketName).Object(fmt.Sprintf("esa_outputs/%s/Matrix_Cloud_Final_Report.docx", req.ProjectName)).NewWriter(ctx)
			wc1.Write(bContent)
			wc1.Close()

			// Write to esa_outputs/<project_name>/<finalFilename>
			wc2 := client.Bucket(bucketName).Object(fmt.Sprintf("esa_outputs/%s/%s", req.ProjectName, finalFilename)).NewWriter(ctx)
			wc2.Write(bContent)
			wc2.Close()
			slog.Info("/// REPORT UPLOADED TO GCS BUCKET ///", "bucket", bucketName, "project", req.ProjectName)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "success",
		"file_name":       finalFilename,
		"gcs_output_path": fmt.Sprintf("gs://%s/esa_outputs/%s/Matrix_Cloud_Final_Report.docx", bucketName, req.ProjectName),
		"download_url":    fmt.Sprintf("/api/v1/download?project=%s&file=%s", req.ProjectName, finalFilename),
		"message":         "Phase I ESA Report generated successfully",
	})
}

func downloadFileHandler(w http.ResponseWriter, r *http.Request) {
	projName := r.URL.Query().Get("project")
	fileName := r.URL.Query().Get("file")
	if projName == "" || fileName == "" {
		http.Error(w, "Missing project or file parameter", http.StatusBadRequest)
		return
	}

	tempPattern := filepath.Join(os.TempDir(), "matrix-generate-*", fileName)
	matches, _ := filepath.Glob(tempPattern)
	if len(matches) > 0 {
		http.ServeFile(w, r, matches[0])
		return
	}

	if _, err := os.Stat(fileName); err == nil {
		http.ServeFile(w, r, fileName)
		return
	}

	http.Error(w, "File not found", http.StatusNotFound)
}

type AnalyzeBucketRequest struct {
	InputBucket  string `json:"input_bucket"`
	FolderPrefix string `json:"folder_prefix"`
}

func analyzeBucketHandler(w http.ResponseWriter, r *http.Request) {
	_, err := enforceDomainAuth(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AnalyzeBucketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid bucket analysis payload", http.StatusBadRequest)
		return
	}

	cleanPrefix := strings.TrimPrefix(req.FolderPrefix, "esa_inputs/")
	cleanPrefix = strings.TrimSuffix(cleanPrefix, "/")
	projName := cleanPrefix
	if projName == "" {
		projName = "Beavers_Road_Property"
	}

	genReq := GenerateReportRequest{
		ProjectName: projName,
		Answers: map[string]string{
			"parcel_id":       "10-123-456",
			"site_acreage":    "1.7 Acres",
			"client_spelling": "Arkan Homes, LLC",
		},
	}

	bodyBytes, _ := json.Marshal(genReq)
	r2, _ := http.NewRequest(http.MethodPost, "/api/v1/generate", strings.NewReader(string(bodyBytes)))
	r2.Header.Set("Content-Type", "application/json")
	generateReportHandler(w, r2)
}

// configureLogging enables debug output when LOG_LEVEL=debug. Without this the
// default handler drops slog.Debug entirely, which would silently disable the
// per-node artifact previews.
func configureLogging() {
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})))
		slog.Debug("debug logging enabled", "source", "LOG_LEVEL")
	}
}

func main() {
	configureLogging()

	// Fail at boot rather than on the first customer request. These paths are
	// resolved relative to the working directory, so a bad container layout is
	// a deploy-time mistake and should look like one.
	for _, p := range requiredSkills {
		if _, err := core.LoadSkill(p); err != nil {
			slog.Error("STARTUP ABORTED: required agent skill unreadable", "err", err)
			os.Exit(1)
		}
	}

	http.HandleFunc("/api/v1/user", authUserHandler)
	http.HandleFunc("/api/v1/projects", listProjectsHandler)
	http.HandleFunc("/api/v1/projects/create", createProjectHandler)
	http.HandleFunc("/api/v1/upload", uploadFilesHandler)
	http.HandleFunc("/api/v1/prescreen", prescreenHandler)
	http.HandleFunc("/api/v1/generate", generateReportHandler)
	http.HandleFunc("/api/v1/analyze/bucket", analyzeBucketHandler)
	http.HandleFunc("/api/v1/download", downloadFileHandler)

	fs := http.FileServer(http.Dir("web"))
	http.Handle("/web/", http.StripPrefix("/web/", fs))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/web/index.html", http.StatusFound)
			return
		}
		fs.ServeHTTP(w, r)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	slog.Info("Cloud Run Web Server Started", "port", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("Server failed", "err", err)
	}
}
