package agent

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"testing"
)

func validPNGFixture(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatalf("encode PNG fixture: %v", err)
	}
	return data.Bytes()
}

func validWebPFixture(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("UklGRjAAAABXRUJQVlA4ICQAAABwAQCdASoBAAEAAgA0JYwCdAGIQAD++ZNsGW2xURhNJHYAAAA=")
	if err != nil {
		t.Fatalf("decode WebP fixture: %v", err)
	}
	return data
}

// validPDFFixture builds the smallest PDF a parser will actually open: a
// catalog, a page tree, and one page, followed by a cross-reference table whose
// byte offsets are RECORDED AS THE OBJECTS ARE WRITTEN rather than typed in.
//
// That last part is the whole point. A hand-pasted PDF blob whose xref offsets
// are wrong still contains every marker a grep looks for -- %PDF, xref,
// trailer, startxref, %%EOF -- and a previous attempt at this fixture shipped
// exactly that (three of its four offsets pointed at nothing). Offsets computed
// from buf.Len() are correct by construction, and
// TestValidPDFFixtureCrossReferencesResolve re-derives them by parsing the
// bytes back, so "valid" is a checked claim and not a comment.
func validPDFFixture(t *testing.T) []byte {
	t.Helper()
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 72 72] /Resources << >> >>",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefOffset := buf.Len()
	// Every xref entry is exactly 20 bytes: 10-digit offset, space, 5-digit
	// generation, space, type, space, newline. Object 0 is the head of the
	// free list at generation 65535, as the format requires.
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d %05d n \n", offset, 0)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return buf.Bytes()
}

// resolvePDFCrossReferences parses a PDF the way a reader does: it follows
// startxref to the cross-reference table, walks the 20-byte entries, and
// requires each in-use offset to land on that object's own "N 0 obj" header.
// It returns the resolved offsets keyed by object number.
//
// This is the validation the fixture's claim rests on. Checking that the
// markers are PRESENT proves nothing -- the offsets between them are what a
// parser follows, and they are what a hand-written fixture gets wrong.
func resolvePDFCrossReferences(data []byte) (map[int]int, error) {
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, errors.New("no %PDF- header")
	}
	if !bytes.HasSuffix(bytes.TrimRight(data, "\r\n"), []byte("%%EOF")) {
		return nil, errors.New("no %%EOF trailer")
	}
	marker := bytes.LastIndex(data, []byte("startxref"))
	if marker < 0 {
		return nil, errors.New("no startxref")
	}
	fields := strings.Fields(string(data[marker+len("startxref"):]))
	if len(fields) == 0 {
		return nil, errors.New("startxref has no operand")
	}
	xrefOffset, err := strconv.Atoi(fields[0])
	if err != nil {
		return nil, fmt.Errorf("startxref operand %q: %w", fields[0], err)
	}
	if xrefOffset < 0 || xrefOffset >= len(data) {
		return nil, fmt.Errorf("startxref %d is outside the %d-byte file", xrefOffset, len(data))
	}
	table := data[xrefOffset:]
	if !bytes.HasPrefix(table, []byte("xref\n")) {
		return nil, fmt.Errorf("startxref %d does not point at a cross-reference table: %q", xrefOffset, head(table))
	}
	subsection, entries, ok := bytes.Cut(table[len("xref\n"):], []byte("\n"))
	if !ok {
		return nil, errors.New("cross-reference subsection header is unterminated")
	}
	header := strings.Fields(string(subsection))
	if len(header) != 2 {
		return nil, fmt.Errorf("cross-reference subsection header = %q, want \"first count\"", subsection)
	}
	first, err := strconv.Atoi(header[0])
	if err != nil {
		return nil, fmt.Errorf("subsection first object %q: %w", header[0], err)
	}
	count, err := strconv.Atoi(header[1])
	if err != nil {
		return nil, fmt.Errorf("subsection count %q: %w", header[1], err)
	}
	if len(entries) < count*20 {
		return nil, fmt.Errorf("cross-reference table holds %d bytes, need %d for %d entries", len(entries), count*20, count)
	}
	resolved := make(map[int]int, count)
	for i := range count {
		entry := string(entries[i*20 : (i+1)*20])
		number := first + i
		if len(entry) != 20 || entry[10] != ' ' || entry[16] != ' ' || entry[18] != ' ' {
			return nil, fmt.Errorf("entry %d = %q is not a 20-byte cross-reference entry", number, entry)
		}
		offset, err := strconv.Atoi(entry[:10])
		if err != nil {
			return nil, fmt.Errorf("entry %d offset %q: %w", number, entry[:10], err)
		}
		switch entry[17] {
		case 'f':
			continue
		case 'n':
		default:
			return nil, fmt.Errorf("entry %d type = %q, want 'n' or 'f'", number, entry[17])
		}
		if offset < 0 || offset >= len(data) {
			return nil, fmt.Errorf("object %d offset %d is outside the %d-byte file", number, offset, len(data))
		}
		want := fmt.Sprintf("%d 0 obj", number)
		if !bytes.HasPrefix(data[offset:], []byte(want)) {
			return nil, fmt.Errorf("object %d offset %d points at %q, want %q", number, offset, head(data[offset:]), want)
		}
		resolved[number] = offset
	}
	trailer := bytes.LastIndex(data, []byte("trailer"))
	if trailer < 0 {
		return nil, errors.New("no trailer")
	}
	size, err := pdfTrailerSize(data[trailer:])
	if err != nil {
		return nil, err
	}
	if size != count {
		return nil, fmt.Errorf("trailer /Size %d disagrees with the %d cross-reference entries", size, count)
	}
	root, err := pdfTrailerRoot(data[trailer:])
	if err != nil {
		return nil, err
	}
	if _, ok := resolved[root]; !ok {
		return nil, fmt.Errorf("trailer /Root names object %d, which the cross-reference table does not resolve", root)
	}
	return resolved, nil
}

func pdfTrailerSize(trailer []byte) (int, error) {
	return pdfTrailerInt(trailer, "/Size", 1)
}

func pdfTrailerRoot(trailer []byte) (int, error) {
	return pdfTrailerInt(trailer, "/Root", 1)
}

// pdfTrailerInt reads the nth whitespace-delimited field after key in the
// trailer dictionary. /Size is followed by its count; /Root by the object
// number of an indirect reference ("1 0 R").
func pdfTrailerInt(trailer []byte, key string, field int) (int, error) {
	_, operands, ok := bytes.Cut(trailer, []byte(key))
	if !ok {
		return 0, fmt.Errorf("trailer has no %s", key)
	}
	fields := strings.Fields(string(operands))
	if len(fields) < field {
		return 0, fmt.Errorf("trailer %s has no operand", key)
	}
	value, err := strconv.Atoi(fields[field-1])
	if err != nil {
		return 0, fmt.Errorf("trailer %s operand %q: %w", key, fields[field-1], err)
	}
	return value, nil
}

func head(b []byte) string {
	return string(b[:min(len(b), 24)])
}

// TestValidPDFFixtureCrossReferencesResolve is the fixture's own proof. Without
// it validPDFFixture is one more opaque blob asserting its own validity, which
// is the failure mode this fixture exists to correct.
func TestValidPDFFixtureCrossReferencesResolve(t *testing.T) {
	t.Parallel()
	data := validPDFFixture(t)
	resolved, err := resolvePDFCrossReferences(data)
	if err != nil {
		t.Fatalf("the PDF fixture does not parse: %v", err)
	}
	if len(resolved) != 3 {
		t.Fatalf("resolved %d objects, want the catalog, the page tree and one page", len(resolved))
	}

	// The validator has to be able to REJECT, or resolving proves nothing.
	// Corrupting one offset must be caught, and a marker-presence check would
	// not catch it: every marker survives this edit.
	broken := append([]byte(nil), data...)
	xref := bytes.LastIndex(broken, []byte("xref\n0 4\n"))
	if xref < 0 {
		t.Fatal("fixture layout changed: no cross-reference subsection to corrupt")
	}
	entry := xref + len("xref\n0 4\n") + 20 // object 1's entry, right after the free-list head
	copy(broken[entry:entry+10], "0000000042")
	if _, err := resolvePDFCrossReferences(broken); err == nil {
		t.Fatal("resolvePDFCrossReferences accepted a PDF whose object 1 offset points at nothing; it is checking for markers, not resolving references")
	}
}
