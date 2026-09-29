package transcript

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// TestReadHeaderSkipsBlankLinesAndStopsAfterHeader pins the contract every
// reader of a leading transcript header relies on: blank lines before the
// header are skipped, the first non-blank line is decoded as the header, and
// the reader is left positioned just past that line so the caller can keep
// scanning records.
func TestReadHeaderSkipsBlankLinesAndStopsAfterHeader(t *testing.T) {
	body := "\n  \n" +
		`{"kind":"header","format_version":2,"session_id":"s1"}` + "\n" +
		`{"kind":"entry","seq":1,"turn":{}}` + "\n"
	reader := bufio.NewReader(strings.NewReader(body))
	header, err := ReadHeader(reader, DefaultMaxLineBytes)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if header.SessionID != "s1" || header.FormatVersion != FormatVersion {
		t.Fatalf("header = %+v, want session s1 at format %d", header, FormatVersion)
	}
	line, complete, _, err := ReadLine(reader, DefaultMaxLineBytes)
	if err != nil || !complete {
		t.Fatalf("ReadLine after header = (%q, %v, %v), want the entry line", line, complete, err)
	}
	if !strings.Contains(string(line), `"entry"`) {
		t.Fatalf("next line = %q, want the entry record", line)
	}
}

// TestReadHeaderMissing pins that a reader with no complete leading header
// line is reported as an unsupported format naming the missing header.
func TestReadHeaderMissing(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "blank lines only", body: "\n  \n"},
		{name: "unterminated tail", body: `{"kind":"header","format_version":2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadHeader(bufio.NewReader(strings.NewReader(tt.body)), DefaultMaxLineBytes)
			if !errors.Is(err, ErrUnsupportedFormat) {
				t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
			}
			if !strings.Contains(err.Error(), "missing transcript header") {
				t.Fatalf("err = %v, want it to name the missing header", err)
			}
		})
	}
}

// TestReadHeaderRejectsNonV2 pins that a complete but non-v2 leading line is
// classified as an unsupported format.
func TestReadHeaderRejectsNonV2(t *testing.T) {
	_, err := ReadHeader(bufio.NewReader(strings.NewReader(`{"kind":"header","format_version":1}`+"\n")), DefaultMaxLineBytes)
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want ErrUnsupportedFormat", err)
	}
}
