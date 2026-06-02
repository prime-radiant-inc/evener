package agent

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

func TestParseImageResult_ExtractsImageData(t *testing.T) {
	// Simulate what ReadFile returns for an image.
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	encoded := base64.StdEncoding.EncodeToString(pngHeader)
	readOutput := fmt.Sprintf("[image: png, %d bytes, base64 data follows]\n%s", len(pngHeader), encoded)

	got := parseImageResult("photo.png", readOutput)
	if got == nil {
		t.Fatal("expected non-nil imageResult")
	}
	if !bytes.Equal(got.Data, pngHeader) {
		t.Fatalf("Data mismatch: got %v, want %v", got.Data, pngHeader)
	}
	if got.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", got.MediaType)
	}
	if !strings.Contains(got.Text, "[image: png") {
		t.Fatalf("Text should contain header, got: %q", got.Text)
	}
}

func TestParseImageResult_ReturnsNilForNonImage(t *testing.T) {
	got := parseImageResult("code.go", "1 | package main\n2 | func main() {}\n")
	if got != nil {
		t.Fatal("expected nil for non-image content")
	}
}

func TestReadFile_Image_EndToEnd_ToolExecResult(t *testing.T) {
	// End-to-end: read_file on a real PNG → env returns base64 → parseImageResult
	// extracts bytes → toolExecResult has ImageData set. This is the path that
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

	// Step 2: parseImageResult extracts the image data.
	img := parseImageResult("board.png", output)
	if img == nil {
		t.Fatal("parseImageResult returned nil — image not detected")
	}
	if !bytes.Equal(img.Data, pngData) {
		t.Fatalf("image data mismatch: got %d bytes, want %d", len(img.Data), len(pngData))
	}
	if img.MediaType != "image/png" {
		t.Fatalf("MediaType = %q, want image/png", img.MediaType)
	}
}

func TestParseDocumentResult_ExtractsPDFData(t *testing.T) {
	pdfContent := []byte("%PDF-1.4 content\x00\x01")
	encoded := base64.StdEncoding.EncodeToString(pdfContent)
	readOutput := fmt.Sprintf("[document: pdf, %d bytes, base64 data follows]\n%s", len(pdfContent), encoded)

	got := parseDocumentResult("invoice.pdf", readOutput)
	if got == nil {
		t.Fatal("expected non-nil result for document")
	}
	if !bytes.Equal(got.Data, pdfContent) {
		t.Fatalf("Data mismatch: got %v, want %v", got.Data, pdfContent)
	}
	if got.MediaType != "application/pdf" {
		t.Fatalf("MediaType = %q, want application/pdf", got.MediaType)
	}
	if !strings.Contains(got.Text, "[document: pdf") {
		t.Fatalf("Text should contain header, got: %q", got.Text)
	}
}

func TestParseDocumentResult_ReturnsNilForNonDocument(t *testing.T) {
	got := parseDocumentResult("code.go", "1 | package main\n2 | func main() {}\n")
	if got != nil {
		t.Fatal("expected nil for non-document content")
	}
}

func TestReadFile_PDF_EndToEnd_ToolExecResult(t *testing.T) {
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

	// Step 2: parseDocumentResult extracts the document data.
	doc := parseDocumentResult("report.pdf", output)
	if doc == nil {
		t.Fatal("parseDocumentResult returned nil")
	}
	if !bytes.Equal(doc.Data, pdfData) {
		t.Fatalf("data mismatch: got %d bytes, want %d", len(doc.Data), len(pdfData))
	}
	if doc.MediaType != "application/pdf" {
		t.Fatalf("MediaType = %q, want application/pdf", doc.MediaType)
	}
}
