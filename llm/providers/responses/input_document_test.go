package responses

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// inputFilesFromItems returns every input_file content item a built input
// array carries.
func inputFilesFromItems(t *testing.T, items []any) []map[string]any {
	t.Helper()
	var files []map[string]any
	for _, i := range items {
		m, _ := i.(map[string]any)
		content, _ := m["content"].([]any)
		for _, c := range content {
			cm, _ := c.(map[string]any)
			if cm["type"] == "input_file" {
				files = append(files, cm)
			}
		}
	}
	return files
}

// TestToResponsesInput_DocumentURLUsesFileURL pins the Responses wire shape for
// a URL-only document: the URL rides the API's file_url field, a sibling of
// file_data, never file_data itself (#5491).
func TestToResponsesInput_DocumentURLUsesFileURL(t *testing.T) {
	_, items, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{
			URL:      "https://example.com/report.pdf",
			FileName: "report.pdf",
		}},
	}}}, "")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	files := inputFilesFromItems(t, items)
	if len(files) != 1 {
		t.Fatalf("input_file items = %v, want one", files)
	}
	file := files[0]
	if file["file_url"] != "https://example.com/report.pdf" {
		t.Fatalf("file_url = %v, want the document URL", file["file_url"])
	}
	if _, ok := file["file_data"]; ok {
		t.Fatalf("a URL-only document carried file_data: %v", file)
	}
	if file["filename"] != "report.pdf" {
		t.Fatalf("filename = %v, want report.pdf", file["filename"])
	}
}

// TestToResponsesInput_DocumentBytesStillUseFileData keeps the bytes arm on
// file_data: inline content is what that field accepts.
func TestToResponsesInput_DocumentBytesStillUseFileData(t *testing.T) {
	_, items, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.7 stub")}},
	}}}, "")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	files := inputFilesFromItems(t, items)
	if len(files) != 1 {
		t.Fatalf("input_file items = %v, want one", files)
	}
	file := files[0]
	data, _ := file["file_data"].(string)
	if !strings.HasPrefix(data, "data:application/pdf;base64,") {
		t.Fatalf("file_data = %q, want the inline pdf data URI", data)
	}
	if _, ok := file["file_url"]; ok {
		t.Fatalf("a bytes document carried file_url: %v", file)
	}
}

// TestToResponsesInput_DocumentFilenameOnlyIsDropped pins the source gate: an
// input_file with only a filename has no file_data or file_url (and no
// file_id on this wire), so the provider rejects the whole request. The part
// is dropped instead.
func TestToResponsesInput_DocumentFilenameOnlyIsDropped(t *testing.T) {
	_, items, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{FileName: "report.pdf"}},
	}}}, "")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	if files := inputFilesFromItems(t, items); len(files) != 0 {
		t.Fatalf("input_file items = %v, want none for a source-less document", files)
	}
	if len(items) != 0 {
		t.Fatalf("items = %v, want no message item for a source-less document", items)
	}
}
