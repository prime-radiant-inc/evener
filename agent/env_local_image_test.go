package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func TestParseImageResult_ExtractsImageData(t *testing.T) {
	t.Parallel()
	// Simulate what ReadFile returns for an image.
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	encoded := base64.StdEncoding.EncodeToString(pngHeader)
	readOutput := fmt.Sprintf("[image: png, %d bytes, base64 data follows]\n%s", len(pngHeader), encoded)

	got := tool.ParseImageResult("photo.png", readOutput)
	if got == nil {
		t.Fatal("expected non-nil tool.ImageResult")
	}
	if !bytes.Equal(got.Data, pngHeader) {
		t.Fatalf("Data mismatch: got %v, want %v", got.Data, pngHeader)
	}
	if got.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", got.MediaType)
	}
	wantText := fmt.Sprintf("[image: png, %d bytes, base64 data follows]", len(pngHeader))
	if got.Text != wantText {
		t.Fatalf("Text = %q, want %q", got.Text, wantText)
	}
}

func TestParseImageResult_ReturnsNilForNonImage(t *testing.T) {
	t.Parallel()
	got := tool.ParseImageResult("code.go", "1 | package main\n2 | func main() {}\n")
	if got != nil {
		t.Fatal("expected nil for non-image content")
	}
}

func TestReadFile_Image_EndToEnd_ToolExecResult(t *testing.T) {
	t.Parallel()
	// End-to-end: read_file on a real PNG → env returns base64 → tool.ParseImageResult
	// extracts bytes → tool.ExecResult has ImageData set. This is the path that
	// sends images to the model for visual inspection.
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)

	// Write a minimal valid PNG (just the magic bytes + enough to detect).
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	if err := os.WriteFile(filepath.Join(dir, "board.png"), pngData, 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: ReadFile returns base64 image output.
	output, err := env.ReadFile("board.png", nil, nil)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(output, "[image:") {
		t.Fatalf("expected [image: prefix, got: %q", output[:min(len(output), 50)])
	}

	// Step 2: tool.ParseImageResult extracts the image data.
	img := tool.ParseImageResult("board.png", output)
	if img == nil {
		t.Fatal("tool.ParseImageResult returned nil — image not detected")
	}
	if !bytes.Equal(img.Data, pngData) {
		t.Fatalf("image data mismatch: got %d bytes, want %d", len(img.Data), len(pngData))
	}
	if img.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", img.MediaType)
	}
}

func TestParseDocumentResult_ExtractsPDFData(t *testing.T) {
	t.Parallel()
	pdfContent := []byte("%PDF-1.4 content\x00\x01")
	encoded := base64.StdEncoding.EncodeToString(pdfContent)
	readOutput := fmt.Sprintf("[document: pdf, %d bytes, base64 data follows]\n%s", len(pdfContent), encoded)

	got := tool.ParseDocumentResult("invoice.pdf", readOutput)
	if got == nil {
		t.Fatal("expected non-nil result for document")
	}
	if !bytes.Equal(got.Data, pdfContent) {
		t.Fatalf("Data mismatch: got %v, want %v", got.Data, pdfContent)
	}
	if got.MediaType != "application/pdf" {
		t.Fatalf("MediaType = %q, want application/pdf", got.MediaType)
	}
	wantText := fmt.Sprintf("[document: pdf, %d bytes, base64 data follows]", len(pdfContent))
	if got.Text != wantText {
		t.Fatalf("Text = %q, want %q", got.Text, wantText)
	}
}

func TestParseDocumentResult_ReturnsNilForNonDocument(t *testing.T) {
	t.Parallel()
	got := tool.ParseDocumentResult("code.go", "1 | package main\n2 | func main() {}\n")
	if got != nil {
		t.Fatal("expected nil for non-document content")
	}
}

func TestReadFile_PDF_EndToEnd_ToolExecResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)

	pdfData := []byte("%PDF-1.4 invoice content\x00\x01\x02")
	if err := os.WriteFile(filepath.Join(dir, "report.pdf"), pdfData, 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: ReadFile returns base64 document output.
	output, err := env.ReadFile("report.pdf", nil, nil)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(output, "[document:") {
		t.Fatalf("expected [document: prefix, got: %q", output[:min(len(output), 50)])
	}

	// Step 2: tool.ParseDocumentResult extracts the document data.
	doc := tool.ParseDocumentResult("report.pdf", output)
	if doc == nil {
		t.Fatal("tool.ParseDocumentResult returned nil")
	}
	if !bytes.Equal(doc.Data, pdfData) {
		t.Fatalf("data mismatch: got %d bytes, want %d", len(doc.Data), len(pdfData))
	}
	if doc.MediaType != "application/pdf" {
		t.Fatalf("MediaType = %q, want application/pdf", doc.MediaType)
	}
}

// TestReadFile_RealPDF_DetectedByItsBytes is the only test that read_file's
// document handling could not pass with a placeholder payload.
//
// Every other PDF case names the file `.pdf`, and the extension alone decides
// (execenv.detectDocumentFormat) -- so `[]byte("pdf")` passes them all, and for
// years the fixture that looked like PDF coverage was three ASCII characters.
// Downstream is worse: document payloads are exempt from raster decoding
// (tool.dispatchedResult), so nothing past the environment examines the bytes
// at all.
//
// The one place the CONTENT decides is the magic-byte branch, reached when the
// extension does not claim a PDF. Reading the same path with the same tool,
// twice, differing only in the bytes on disk, is therefore the whole oracle: a
// real PDF must come back as a document carrying application/pdf, and a
// non-PDF must not. It runs through the production read_file tool -- the
// registered executor, not ReadFile directly -- so the environment, the
// document parse and the exemption are all in the path.
func TestReadFile_RealPDF_DetectedByItsBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tracked := ""
	reg := tool.NewRegistry()
	if err := registerFileTools(reg, &toolDeps{readGuard: readGuard{
		trackRead:              func(path string) { tracked = path },
		readBeforeWriteWarning: func(string) string { return "" },
	}}); err != nil {
		t.Fatalf("registerFileTools: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	readFile := func(name string, content []byte) tool.ExecResult {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		args, err := json.Marshal(map[string]any{"file_path": name, "purpose": "inspect"})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		return reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: "read", Name: "read_file", Arguments: args})
	}

	pdf := validPDFFixture(t)
	res := readFile("mystery.dat", pdf)
	if res.IsError {
		t.Fatalf("read_file rejected a real PDF: %#v", res)
	}
	if !strings.HasPrefix(res.Output, "[document: pdf,") {
		t.Fatalf("read_file did not classify a real PDF as a document: output = %q", res.Output[:min(len(res.Output), 60)])
	}
	if res.ImageMediaType != "application/pdf" {
		t.Fatalf("media type = %q, want application/pdf", res.ImageMediaType)
	}
	if !bytes.Equal(res.ImageData, pdf) {
		t.Fatalf("the model would receive %d bytes, want the file's %d", len(res.ImageData), len(pdf))
	}
	if tracked != "mystery.dat" {
		t.Fatalf("read guard tracked %q", tracked)
	}

	// The control: same tool, same path, bytes that are not a PDF. Without
	// this, "is a document" is satisfied by any file at all and the assertion
	// above measures nothing about the bytes.
	if res := readFile("mystery.dat", []byte("plain text, no header\n")); res.IsError ||
		strings.HasPrefix(res.Output, "[document:") || len(res.ImageData) != 0 {
		t.Fatalf("a non-PDF read through the same path came back as a document: %#v", res)
	}

	// A truncated header is the near miss: it shares a prefix with the real
	// thing and must still be read as text.
	if res := readFile("mystery.dat", []byte("%PDF")); res.IsError ||
		strings.HasPrefix(res.Output, "[document:") || len(res.ImageData) != 0 {
		t.Fatalf("an incomplete PDF signature was accepted as a document: %#v", res)
	}
}
