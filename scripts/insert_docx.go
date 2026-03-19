package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

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

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: go run insert_docx.go <template.docx> <data.json> <output.docx>")
		os.Exit(1)
	}

	templatePath := os.Args[1]
	jsonPath := os.Args[2]
	outputPath := os.Args[3]

	// 1. Read JSON Data
	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		fmt.Printf("Failed to read JSON file: %v\n", err)
		os.Exit(1)
	}

	// 1.5 Clean Markdown Formatting (Extract only the JSON object)
	jsonStr := string(jsonData)
	startIdx := strings.Index(jsonStr, "{")
	endIdx := strings.LastIndex(jsonStr, "}")
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		jsonStr = jsonStr[startIdx : endIdx+1]
	} else {
		fmt.Printf("Warning: Could not auto-detect JSON braces. Parsing raw output.\n")
	}

	var replaceMap map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &replaceMap); err != nil {
		fmt.Printf("Failed to parse JSON data: %v\n", err)
		os.Exit(1)
	}

	// 2. Open Template with pure archive/zip
	r, err := zip.OpenReader(templatePath)
	if err != nil {
		fmt.Printf("Failed to open DOCX template: %v\n", err)
		os.Exit(1)
	}
	defer r.Close()

	// 3. Create Output Zip
	outf, err := os.Create(outputPath)
	if err != nil {
		fmt.Printf("Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer outf.Close()

	w := zip.NewWriter(outf)

	// 4. Iterate over files inside the DOCX
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			fmt.Printf("Failed to open file in zip: %v\n", err)
			os.Exit(1)
		}

		// check if this file needs processing
		needProcess := false
		if f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/header") || strings.HasPrefix(f.Name, "word/footer") {
			needProcess = true
		}

		fWriter, err := w.Create(f.Name)
		if err != nil {
			rc.Close()
			fmt.Printf("Failed to create file in new docx: %v\n", err)
			os.Exit(1)
		}

		if needProcess {
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				fmt.Printf("Failed to read xml file: %v\n", err)
				os.Exit(1)
			}

			xmlStr := string(content)
			for k, v := range replaceMap {
				valStr := fmt.Sprint(v)
				xmlStr = replaceFracturedXML(xmlStr, k, valStr)
			}
			
			_, err = fWriter.Write([]byte(xmlStr))
			if err != nil {
				fmt.Printf("Failed to write to new docx: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Copy as is
			_, err = io.Copy(fWriter, rc)
			rc.Close()
			if err != nil {
				fmt.Printf("Failed to copy to new docx: %v\n", err)
				os.Exit(1)
			}
		}
	}

	// 5. Close Writer
	if err := w.Close(); err != nil {
		fmt.Printf("Failed to close docx writer: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully generated %s\n", outputPath)
}
