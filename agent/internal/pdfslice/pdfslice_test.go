package pdfslice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// syntheticPDF builds a valid PDF with pageCount empty pages entirely in
// memory, so no external binary fixture is needed. The cross-reference offsets
// are recorded while the objects are written, so they are correct by
// construction.
func syntheticPDF(t *testing.T, pageCount int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, pageCount+3)
	writeObject := func(number int, body string) {
		offsets[number] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", number, body)
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, pageCount)
	for i := range kids {
		kids[i] = fmt.Sprintf("%d 0 R", i+3)
	}
	writeObject(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pageCount))
	for i := 0; i < pageCount; i++ {
		writeObject(i+3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>")
	}
	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", pageCount+3)
	buf.WriteString("0000000000 65535 f \n")
	for number := 1; number <= pageCount+2; number++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[number])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		pageCount+3, xrefOffset)
	return buf.Bytes()
}

// pdfPageCount reads pageCount's PDF back through pdfcpu, so a test that claims
// a trimmed PDF has N pages proves it with a parser rather than with the slice
// function's own return values.
func pdfPageCount(t *testing.T, pageCount []byte) int {
	t.Helper()
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	count, err := api.PageCount(context.Background(), bytes.NewReader(pageCount), conf)
	if err != nil {
		t.Fatalf("PageCount on returned PDF: %v", err)
	}
	return count
}

func TestParsePageRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		spec         string
		wantFrom     int
		wantTo       int
		wantProvided bool
	}{
		{name: "empty is omitted", spec: "", wantProvided: false},
		{name: "whitespace is omitted", spec: "   ", wantProvided: false},
		{name: "single page", spec: "3", wantFrom: 3, wantTo: 3, wantProvided: true},
		{name: "range with spaces", spec: " 1-5 ", wantFrom: 1, wantTo: 5, wantProvided: true},
		{name: "max span", spec: "1-20", wantFrom: 1, wantTo: 20, wantProvided: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			from, to, provided, err := ParsePageRange(tc.spec)
			if err != nil {
				t.Fatalf("ParsePageRange(%q) error: %v", tc.spec, err)
			}
			if provided != tc.wantProvided || from != tc.wantFrom || to != tc.wantTo {
				t.Fatalf("ParsePageRange(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tc.spec, from, to, provided, tc.wantFrom, tc.wantTo, tc.wantProvided)
			}
		})
	}

	for _, spec := range []string{"0", "-1", "5-3", "1,3,5", "1-21", "abc", "1-"} {
		t.Run("invalid "+spec, func(t *testing.T) {
			t.Parallel()
			from, to, provided, err := ParsePageRange(spec)
			if err == nil {
				t.Fatalf("ParsePageRange(%q) = (%d, %d, %v), want an error", spec, from, to, provided)
			}
			if !provided {
				t.Fatalf("ParsePageRange(%q) provided = false, want true for a non-empty spec", spec)
			}
			if from != 0 || to != 0 {
				t.Fatalf("ParsePageRange(%q) = (%d, %d), want zero values on error", spec, from, to)
			}
			if spec == "1-21" && !strings.Contains(err.Error(), strconv.Itoa(MaxPDFPagesPerCall)) {
				t.Fatalf("ParsePageRange(%q) error = %q, want it to name the %d-page rule", spec, err, MaxPDFPagesPerCall)
			}
		})
	}
}

func TestSlicePDFUnpagedWithinAndOverLimit(t *testing.T) {
	ctx := context.Background()

	fivePages := syntheticPDF(t, 5)
	sliced, from, to, totalPages, err := SlicePDF(ctx, fivePages, "")
	if err != nil {
		t.Fatalf("SlicePDF(5-page PDF, no pages) error: %v", err)
	}
	if !bytes.Equal(sliced, fivePages) {
		t.Fatal("SlicePDF(5-page PDF, no pages) changed the bytes; want the raw PDF verbatim")
	}
	if from != 1 || to != 5 || totalPages != 5 {
		t.Fatalf("SlicePDF(5-page PDF, no pages) = from %d, to %d, total %d; want 1, 5, 5", from, to, totalPages)
	}

	fifteenPages := syntheticPDF(t, 15)
	_, _, _, totalPages, err = SlicePDF(ctx, fifteenPages, "")
	if err == nil {
		t.Fatal("SlicePDF(15-page PDF, no pages) succeeded; want the unpaged limit error")
	}
	if totalPages != 15 {
		t.Fatalf("SlicePDF(15-page PDF, no pages) totalPages = %d, want 15", totalPages)
	}
	for _, want := range []string{"15", strconv.Itoa(MaxUnpagedPDFPages)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("SlicePDF(15-page PDF, no pages) error = %q, want it to name %q (total pages and the limit)", err, want)
		}
	}

	t.Run("raw bytes over the model cap", func(t *testing.T) {
		old := MaxModelPDFBytes
		MaxModelPDFBytes = len(fivePages) - 1
		t.Cleanup(func() { MaxModelPDFBytes = old })

		if _, _, _, _, err := SlicePDF(ctx, fivePages, ""); err == nil {
			t.Fatalf("SlicePDF(5-page PDF over the %d-byte model cap, no pages) succeeded; want an error", MaxModelPDFBytes)
		}
	})
}

func TestSlicePDFRangesAndClamping(t *testing.T) {
	ctx := context.Background()
	fifteenPages := syntheticPDF(t, 15)

	sliced, from, to, totalPages, err := SlicePDF(ctx, fifteenPages, "2-4")
	if err != nil {
		t.Fatalf("SlicePDF(pages 2-4) error: %v", err)
	}
	if from != 2 || to != 4 || totalPages != 15 {
		t.Fatalf("SlicePDF(pages 2-4) = from %d, to %d, total %d; want 2, 4, 15", from, to, totalPages)
	}
	if got := pdfPageCount(t, sliced); got != 3 {
		t.Fatalf("SlicePDF(pages 2-4) returned a PDF with %d pages, want 3", got)
	}
	// pdfcpu gives every written PDF a fresh file ID and Info dates, so the
	// same input and range must still come back byte-identical: repeat
	// tracking compares the bytes a re-read returns against the first read.
	repeated, _, _, _, err := SlicePDF(ctx, fifteenPages, "2-4")
	if err != nil {
		t.Fatalf("SlicePDF(pages 2-4) again error: %v", err)
	}
	if !bytes.Equal(repeated, sliced) {
		t.Fatal("SlicePDF(pages 2-4) returned different bytes for the same input and range")
	}

	sliced, from, to, totalPages, err = SlicePDF(ctx, fifteenPages, "11-20")
	if err != nil {
		t.Fatalf("SlicePDF(pages 11-20) error: %v", err)
	}
	if from != 11 || to != 15 || totalPages != 15 {
		t.Fatalf("SlicePDF(pages 11-20) = from %d, to %d, total %d; want 11, 15, 15 (clamped)", from, to, totalPages)
	}
	if got := pdfPageCount(t, sliced); got != 5 {
		t.Fatalf("SlicePDF(pages 11-20) returned a PDF with %d pages, want 5", got)
	}

	_, _, _, totalPages, err = SlicePDF(ctx, fifteenPages, "16-20")
	if err == nil {
		t.Fatal("SlicePDF(pages 16-20 on a 15-page PDF) succeeded; want an out-of-range error")
	}
	if totalPages != 15 {
		t.Fatalf("SlicePDF(pages 16-20) totalPages = %d, want 15", totalPages)
	}
	if !strings.Contains(err.Error(), "15") {
		t.Fatalf("SlicePDF(pages 16-20) error = %q, want it to name the 15-page total", err)
	}

	sliced, from, to, totalPages, err = SlicePDF(ctx, fifteenPages, "1-15")
	if err != nil {
		t.Fatalf("SlicePDF(pages 1-15) error: %v", err)
	}
	if !bytes.Equal(sliced, fifteenPages) {
		t.Fatal("SlicePDF(pages 1-15) changed the bytes; want the raw PDF verbatim for the whole document")
	}
	if from != 1 || to != 15 || totalPages != 15 {
		t.Fatalf("SlicePDF(pages 1-15) = from %d, to %d, total %d; want 1, 15, 15", from, to, totalPages)
	}
}

// TestSlicePDFDoesNotTouchConfigDir proves init's api.DisableConfigDir keeps
// pdfcpu hermetic: with HOME and XDG_CONFIG_HOME pointed at an empty directory,
// a page count plus a trim must create nothing on disk.
func TestSlicePDFDoesNotTouchConfigDir(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	t.Setenv("XDG_CONFIG_HOME", empty)

	raw := syntheticPDF(t, 6)
	if _, _, _, _, err := SlicePDF(context.Background(), raw, "2-3"); err != nil {
		t.Fatalf("SlicePDF: %v", err)
	}

	entries, err := os.ReadDir(empty)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", empty, err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("SlicePDF wrote under the config directory: %v", names)
	}
}
