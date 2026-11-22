package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"golang.org/x/image/bmp"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// readFileMediaImage returns the two-tone RGBA image read_file media fixtures
// encode, so a downscale has an edge to preserve.
func readFileMediaImage(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			if x < width/2 {
				img.SetRGBA(x, y, color.RGBA{R: 0x20, G: 0xA0, B: 0x40, A: 0xFF})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 0xA0, G: 0x20, B: 0x40, A: 0xFF})
			}
		}
	}
	return img
}

// readFileMediaPNG builds the oversized image payload a read_file image result
// carries: the [image: ...] envelope execenv produces followed by base64 PNG.
func readFileMediaPNG(t *testing.T, width, height int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, readFileMediaImage(width, height)); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return buf.Bytes(), "[image: big.png]\n" + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// read_file downscales a 3000x1500 PNG to 2000x1000 before returning it to the
// model, and its text carries the original and displayed dimensions plus the
// factor that maps displayed coordinates back to the original.
func TestReadFileDownscalesOversizedImageAndAnnotatesCoordinates(t *testing.T) {
	t.Parallel()
	original, payload := readFileMediaPNG(t, 3000, 1500)
	sess := newSession(t, withEnv(&w3sub_readFileEnv{output: payload}))
	args, err := json.Marshal(map[string]any{"file_path": "big.png"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID: "read-big-image", Name: "read_file", Arguments: args, Type: "function",
	}, "")
	if res.IsError {
		t.Fatalf("read_file failed: %q", res.Output)
	}
	if bytes.Equal(res.ImageData, original) {
		t.Fatal("read_file returned the original 3000x1500 bytes; want downscaled bytes")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(res.ImageData))
	if err != nil {
		t.Fatalf("returned image bytes do not decode: %v", err)
	}
	if cfg.Width != 2000 || cfg.Height != 1000 {
		t.Fatalf("returned image decodes to %dx%d, want 2000x1000", cfg.Width, cfg.Height)
	}
	if llm.ImageBlockMediaType(res.ImageMediaType) == "" {
		t.Fatalf("returned media type = %q, not accepted by an image block", res.ImageMediaType)
	}
	for _, want := range []string{"3000x1500", "2000x1000", "1.50"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("result text = %q, want it to carry %q (original, displayed, scale factor)", res.Output, want)
		}
	}
}

// A small BMP is re-encoded to PNG at the same pixel dimensions, so read_file
// must not claim coordinates moved: it emits no "multiply coordinates by 1.00"
// note for an image that was only format-converted.
func TestReadFileSmallBMPReencodeHasNoCoordinateNote(t *testing.T) {
	t.Parallel()
	var bitmap bytes.Buffer
	if err := bmp.Encode(&bitmap, readFileMediaImage(100, 100)); err != nil {
		t.Fatalf("encode BMP fixture: %v", err)
	}
	payload := "[image: small.bmp]\n" + base64.StdEncoding.EncodeToString(bitmap.Bytes())
	tracked := 0
	guard := readGuard{
		trackRead:              func(string) { tracked++ },
		readBeforeWriteWarning: func(string) string { return "" },
	}
	v, err := execFileReadWith(context.Background(), map[string]any{"file_path": "small.bmp"}, guard,
		func(string, *int, *int) (string, execenv.ReadFileMeta, error) {
			return payload, execenv.ReadFileMeta{}, nil
		})
	if err != nil {
		t.Fatalf("read_file(small.bmp): %v", err)
	}
	if tracked != 1 {
		t.Fatalf("read_file tracked %d reads, want 1", tracked)
	}
	img, ok := v.(tool.ImageResult)
	if !ok {
		t.Fatalf("read_file(small.bmp) returned %T, want tool.ImageResult", v)
	}
	if img.MediaType != "image/png" {
		t.Fatalf("prepared media type = %q, want image/png", img.MediaType)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("returned image bytes do not decode: %v", err)
	}
	if cfg.Width != 100 || cfg.Height != 100 {
		t.Fatalf("returned image decodes to %dx%d, want 100x100 (dimensions unchanged)", cfg.Width, cfg.Height)
	}
	if strings.Contains(img.Text, "multiply coordinates") || strings.Contains(img.Text, "1.00") {
		t.Fatalf("small BMP re-encode emitted a coordinate note: %q", img.Text)
	}
}

// readFileMediaPDF builds a valid PDF with pageCount empty pages in memory and
// returns its bytes plus the [document: ...] envelope execenv produces for a
// PDF read, so PDF tests need no external binary fixture.
func readFileMediaPDF(t *testing.T, pageCount int) ([]byte, string) {
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
	for i := range pageCount {
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
	raw := buf.Bytes()
	payload := fmt.Sprintf("[document: pdf, %d bytes, base64 data follows]\n%s",
		len(raw), base64.StdEncoding.EncodeToString(raw))
	return raw, payload
}

// readFilePDFPageCount counts raw's pages through pdfcpu, so a slicing test
// proves the returned PDF's page count with a parser.
func readFilePDFPageCount(t *testing.T, raw []byte) int {
	t.Helper()
	conf, err := api.LoadConfiguration(api.ConfigurationOptions{Mode: api.ConfigurationModeStateless})
	if err != nil {
		t.Fatalf("LoadConfiguration: %v", err)
	}
	conf.ValidationMode = model.ValidationRelaxed
	count, err := api.PageCount(context.Background(), bytes.NewReader(raw), conf)
	if err != nil {
		t.Fatalf("PageCount on returned PDF: %v", err)
	}
	return count
}

// readFilePDFPages drives read_file's real handler over a document read of raw
// with the given pages selector and returns the result shape the registry hands
// to repeat tracking, plus how many times the read guard tracked the path.
func readFilePDFPages(t *testing.T, raw []byte, pages string) (tool.ExecResult, int, error) {
	t.Helper()
	payload := fmt.Sprintf("[document: pdf, %d bytes, base64 data follows]\n%s",
		len(raw), base64.StdEncoding.EncodeToString(raw))
	tracked := 0
	guard := readGuard{
		trackRead:              func(string) { tracked++ },
		readBeforeWriteWarning: func(string) string { return "" },
	}
	v, err := execFileReadWith(context.Background(), map[string]any{
		"file_path":     "manual.pdf",
		"pages":         pages,
		"vision_prompt": "inspect",
	}, guard, func(string, *int, *int) (string, execenv.ReadFileMeta, error) {
		return payload, execenv.ReadFileMeta{}, nil
	})
	if err != nil {
		return tool.ExecResult{}, tracked, err
	}
	img, ok := v.(tool.ImageResult)
	if !ok {
		t.Fatalf("read_file(pages %q) returned %T, want tool.ImageResult", pages, v)
	}
	return tool.ExecResult{
		ToolName:          "read_file",
		CallID:            "read-pdf-" + pages,
		Output:            img.Text,
		FullOutput:        img.Text,
		RecoverableOutput: img.Text,
		ImageData:         img.Data,
		ImageMediaType:    img.MediaType,
		ImagePrompt:       img.Prompt,
		ReadPages:         img.ReadPages,
	}, tracked, nil
}

// TestReadFilePDFPagesAndRepeatTracking covers the pinned PDF slicing behavior:
// an unpaged PDF over the page limit is refused before it can reach the model,
// pages ranges slice the document, a different range on the same file is not a
// repeat, re-reading a range is, and pages on non-PDF files is refused without
// satisfying read-before-write.
func TestReadFilePDFPagesAndRepeatTracking(t *testing.T) {
	raw15, _ := readFileMediaPDF(t, 15)

	t.Run("unpaged over the page limit is refused through execTool", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "big.pdf"), raw15, 0o644); err != nil {
			t.Fatalf("write 15-page PDF: %v", err)
		}
		sess := newSession(t, withDir(dir))
		args, err := json.Marshal(map[string]any{"file_path": "big.pdf"})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		res := sess.execTool(context.Background(), llm.ToolCallData{
			ID: "read-pdf-unpaged", Name: "read_file", Arguments: args, Type: "function",
		}, "")
		if !res.IsError {
			t.Fatalf("unpaged 15-page PDF read succeeded; want the unpaged limit error")
		}
		if len(res.ImageData) != 0 {
			t.Fatalf("refused PDF read returned %d image bytes; want none", len(res.ImageData))
		}
		for _, want := range []string{"15", "10"} {
			if !strings.Contains(res.Output, want) {
				t.Fatalf("unpaged refusal = %q, want it to name %q (total pages and the limit)", res.Output, want)
			}
		}
	})

	t.Run("page ranges and repeat tracking", func(t *testing.T) {
		first, tracked, err := readFilePDFPages(t, raw15, "1-3")
		if err != nil {
			t.Fatalf("read pages 1-3: %v", err)
		}
		if tracked != 1 {
			t.Fatalf("successful PDF read tracked %d times, want 1", tracked)
		}
		if first.ReadPages != "1-3" {
			t.Fatalf("read pages 1-3 ReadPages = %q, want %q", first.ReadPages, "1-3")
		}
		if first.ImageMediaType != "application/pdf" {
			t.Fatalf("read pages 1-3 media type = %q, want application/pdf", first.ImageMediaType)
		}
		if got := readFilePDFPageCount(t, first.ImageData); got != 3 {
			t.Fatalf("read pages 1-3 returned a %d-page PDF, want 3", got)
		}

		sess := newSession(t)
		stamp := readStamp{size: int64(len(raw15)), mtime: time.Unix(1_700_000_000, 0)}
		sess.noteReadFileRepeat("manual.pdf", stamp, &first)
		if first.UnchangedSinceTurn != nil {
			t.Fatal("first read of pages 1-3 was marked unchanged")
		}

		second, _, err := readFilePDFPages(t, raw15, "4-6")
		if err != nil {
			t.Fatalf("read pages 4-6: %v", err)
		}
		if second.ReadPages != "4-6" {
			t.Fatalf("read pages 4-6 ReadPages = %q, want %q", second.ReadPages, "4-6")
		}
		if got := readFilePDFPageCount(t, second.ImageData); got != 3 {
			t.Fatalf("read pages 4-6 returned a %d-page PDF, want 3", got)
		}
		sess.noteReadFileRepeat("manual.pdf", stamp, &second)
		if second.UnchangedSinceTurn != nil {
			t.Fatal("pages 4-6 was marked unchanged after pages 1-3 of the same file; want distinct ranges distinguished")
		}

		again, _, err := readFilePDFPages(t, raw15, "1-3")
		if err != nil {
			t.Fatalf("re-read pages 1-3: %v", err)
		}
		sess.noteReadFileRepeat("manual.pdf", stamp, &again)
		if again.UnchangedSinceTurn == nil {
			t.Fatal("re-reading pages 1-3 was not detected as an unchanged repeat")
		}
		if len(again.ImageData) == 0 {
			t.Fatal("repeat PDF read dropped the PDF bytes; content must always be returned")
		}
	})

	t.Run("pages on non-PDF files is refused before tracking", func(t *testing.T) {
		pngPayload := "[image: pic.png]\n" + base64.StdEncoding.EncodeToString(validPNGFixture(t))
		for _, tc := range []struct{ name, path, payload string }{
			{name: "text", path: "notes.txt", payload: "1\tplain text\n"},
			{name: "image", path: "pic.png", payload: pngPayload},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tracked := 0
				guard := readGuard{
					trackRead:              func(string) { tracked++ },
					readBeforeWriteWarning: func(string) string { return "" },
				}
				read := func(string, *int, *int) (string, execenv.ReadFileMeta, error) {
					return tc.payload, execenv.ReadFileMeta{}, nil
				}
				v, err := execFileReadWith(context.Background(), map[string]any{
					"file_path": tc.path,
					"pages":     "1-2",
				}, guard, read)
				if err == nil {
					t.Fatalf("read_file(pages 1-2 on %s) succeeded with %T; want an error", tc.path, v)
				}
				if tracked != 0 {
					t.Fatalf("rejected pages call on %s tracked the read %d times; want 0 (read-before-write must not be satisfied)", tc.path, tracked)
				}

				// Control: the same payload without pages succeeds and tracks,
				// so the zero above is the rejection and not a dead guard.
				if _, err := execFileReadWith(context.Background(), map[string]any{
					"file_path": tc.path,
				}, guard, read); err != nil {
					t.Fatalf("read_file(%s) without pages: %v", tc.path, err)
				}
				if tracked != 1 {
					t.Fatalf("control read of %s tracked %d times, want 1", tc.path, tracked)
				}
			})
		}
	})
}

// TestReadFilePDFPagesViaSessionExecTool proves the advertised read_file schema
// accepts pages and that Session.execTool performs PDF slicing and range-aware
// repeat tracking end to end. Task 4's direct execFileReadWith tests bypassed
// the advertised schema: pages was not a declared property, so argument repair
// dropped it before dispatch and the handler saw an unpaged read.
func TestReadFilePDFPagesViaSessionExecTool(t *testing.T) {
	raw15, _ := readFileMediaPDF(t, 15)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.pdf"), raw15, 0o644); err != nil {
		t.Fatalf("write 15-page PDF: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("1\tplain text\n"), 0o644); err != nil {
		t.Fatalf("write text fixture: %v", err)
	}
	sess := newSession(t, withDir(dir))

	readPDF := func(id, pages string) tool.ExecResult {
		t.Helper()
		args, err := json.Marshal(map[string]any{"file_path": "big.pdf", "pages": pages})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		res := sess.execTool(context.Background(), llm.ToolCallData{
			ID: id, Name: "read_file", Arguments: args, Type: "function",
		}, "")
		if res.IsError {
			t.Fatalf("read_file(pages %q) failed through Session.execTool: %q", pages, res.Output)
		}
		if len(res.ImageData) == 0 {
			t.Fatalf("read_file(pages %q) returned no PDF bytes", pages)
		}
		return res
	}

	first := readPDF("read-pdf-1-3", "1-3")
	if first.ReadPages != "1-3" {
		t.Fatalf("read pages 1-3 ReadPages = %q, want %q", first.ReadPages, "1-3")
	}
	if got := readFilePDFPageCount(t, first.ImageData); got != 3 {
		t.Fatalf("read pages 1-3 returned a %d-page PDF, want 3", got)
	}
	if first.UnchangedSinceTurn != nil {
		t.Fatal("first read of pages 1-3 was marked unchanged")
	}

	second := readPDF("read-pdf-4-6", "4-6")
	if second.ReadPages != "4-6" {
		t.Fatalf("read pages 4-6 ReadPages = %q, want %q", second.ReadPages, "4-6")
	}
	if got := readFilePDFPageCount(t, second.ImageData); got != 3 {
		t.Fatalf("read pages 4-6 returned a %d-page PDF, want 3", got)
	}
	if second.UnchangedSinceTurn != nil {
		t.Fatal("pages 4-6 was marked unchanged after pages 1-3 of the same file; want distinct ranges distinguished")
	}

	again := readPDF("read-pdf-1-3-repeat", "1-3")
	if again.UnchangedSinceTurn == nil {
		t.Fatal("re-reading pages 1-3 through Session.execTool was not detected as an unchanged repeat")
	}
	if len(again.ImageData) == 0 {
		t.Fatal("repeat PDF read dropped the PDF bytes; content must always be returned")
	}

	args, err := json.Marshal(map[string]any{"file_path": "notes.txt", "pages": "1-2"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID: "read-pdf-pages-on-text", Name: "read_file", Arguments: args, Type: "function",
	}, "")
	if !res.IsError {
		t.Fatalf("pages on notes.txt succeeded with %d PDF bytes; want a clean error", len(res.ImageData))
	}
}
