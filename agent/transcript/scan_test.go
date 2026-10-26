package transcript

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	scanHeaderLine = `{"kind":"header","format_version":2,"session_id":"S"}`
	scanEntryLine  = `{"kind":"entry","seq":1,"turn":{}}`
)

func scanAll(t *testing.T, data string, maxLineBytes int) (ScanResult, []string, error) {
	t.Helper()
	var lines []string
	result, err := Scan(context.Background(), strings.NewReader(data), maxLineBytes, func(line []byte, _ Entry) error {
		lines = append(lines, string(line))
		return nil
	})
	return result, lines, err
}

func TestScanVisitsHeaderThenEveryCompleteEntry(t *testing.T) {
	result, lines, err := scanAll(t, scanHeaderLine+"\n\n"+scanEntryLine+"\n  "+scanEntryLine+"  \n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Header.SessionID != "S" || result.TornTail {
		t.Fatalf("result = %+v", result)
	}
	if len(lines) != 2 || lines[0] != scanEntryLine || lines[1] != scanEntryLine {
		t.Fatalf("visited %q, want two trimmed entry lines", lines)
	}
}

func TestScanSkipsATornTail(t *testing.T) {
	result, lines, err := scanAll(t, scanHeaderLine+"\n"+scanEntryLine+"\n"+`{"kind":"entry","se`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TornTail || len(lines) != 1 {
		t.Fatalf("result = %+v, visited %d", result, len(lines))
	}
}

func TestScanSkipsAnUnterminatedFinalRecordEvenWhenItParses(t *testing.T) {
	result, lines, err := scanAll(t, scanHeaderLine+"\n"+scanEntryLine+"\n"+scanEntryLine, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TornTail || len(lines) != 1 {
		t.Fatalf("result = %+v, visited %d", result, len(lines))
	}
}

func TestScanRefusesAnOverLimitLine(t *testing.T) {
	_, _, err := scanAll(t, scanHeaderLine+"\n"+strings.Repeat("x", 200)+"\n", 100)
	var lineErr *LineError
	if !errors.Is(err, ErrLineTooLong) || !errors.As(err, &lineErr) || lineErr.Line != 2 {
		t.Fatalf("err = %v, want ErrLineTooLong located at line 2", err)
	}
}

func TestScanLocatesACorruptEntryPastBlankLines(t *testing.T) {
	_, _, err := scanAll(t, "\n"+scanHeaderLine+"\n\n{not json}\n", 0)
	var lineErr *LineError
	if !errors.Is(err, ErrInvalidRecordBoundary) || !errors.As(err, &lineErr) || lineErr.Line != 4 {
		t.Fatalf("err = %v, want ErrInvalidRecordBoundary at line 4", err)
	}
}

func TestScanKeepsUnsupportedFormatDistinctFromMissingHeader(t *testing.T) {
	_, _, err := scanAll(t, `{"kind":"header","format_version":1}`+"\n", 0)
	if !errors.Is(err, ErrUnsupportedFormat) || errors.Is(err, ErrMissingHeader) {
		t.Fatalf("v1 header err = %v, want ErrUnsupportedFormat only", err)
	}
	for _, data := range []string{"", "\n\n", scanHeaderLine} {
		_, _, err := scanAll(t, data, 0)
		if !errors.Is(err, ErrUnsupportedFormat) || !errors.Is(err, ErrMissingHeader) {
			t.Fatalf("%q: err = %v, want ErrUnsupportedFormat and ErrMissingHeader", data, err)
		}
	}
}

func TestScanReturnsTheVisitorsErrorUnchanged(t *testing.T) {
	stop := errors.New("stop")
	_, err := Scan(context.Background(), strings.NewReader(scanHeaderLine+"\n"+scanEntryLine+"\n"), 0, func([]byte, Entry) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the visitor's error", err)
	}
}

func TestScanStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Scan(ctx, strings.NewReader(scanHeaderLine+"\n"), 0, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestScanLetsTheVisitorRetainLines(t *testing.T) {
	var kept [][]byte
	_, err := Scan(context.Background(), strings.NewReader(scanHeaderLine+"\n"+scanEntryLine+"\n"+scanEntryLine+"\n"), 0, func(line []byte, _ Entry) error {
		kept = append(kept, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(kept[0]) != scanEntryLine || string(kept[1]) != scanEntryLine {
		t.Fatalf("retained lines were overwritten: %q", kept)
	}
}
