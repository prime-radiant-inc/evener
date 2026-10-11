package transcript

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDecodeHeaderRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "unknown field", line: `{"kind":"header","format_version":2,"session_id":"s","unknown":true}`},
		{name: "trailing value", line: `{"kind":"header","format_version":2,"session_id":"s"} {}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeHeader([]byte(tc.line)); err == nil {
				t.Fatalf("DecodeHeader(%q) succeeded", tc.line)
			}
		})
	}
}

func TestDecodeEntryRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "unknown entry field", line: `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"},"unknown":true}`},
		{name: "unknown turn field", line: `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","unknown":true}}`},
		{name: "trailing value", line: `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}} {}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeEntry([]byte(tc.line)); err == nil {
				t.Fatalf("DecodeEntry(%q) succeeded", tc.line)
			}
		})
	}
}

func TestDecodeEntryClassifiesNonEntryKindsAsUnsupported(t *testing.T) {
	_, err := DecodeEntry([]byte(`{"kind":"api_call","unknown":true}`))
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("DecodeEntry error = %v, want ErrUnsupportedFormat", err)
	}
}

// A NOTICE entry's payload must match schema.NoticeInfo.Validate()'s own doc
// contract (exactly one payload, matching Kind), or the read side
// (internal/apptranscript/notice.go) drops it silently instead of the whole
// transcript failing loudly the way an unknown field does.
func TestDecodeEntryRejectsAMismatchedNoticePayload(t *testing.T) {
	tests := []struct {
		name string
		turn string
	}{
		{name: "no notice at all", turn: `{"kind":"NOTICE"}`},
		{name: "zero payloads", turn: `{"kind":"NOTICE","notice":{"kind":"goal_ended"}}`},
		{name: "two payloads", turn: `{"kind":"NOTICE","notice":{"kind":"goal_ended","goal_ended":{"status":"complete"},"turn_limit":{"max_turns":4}}}`},
		{name: "mismatched kind", turn: `{"kind":"NOTICE","notice":{"kind":"goal_ended","turn_limit":{"max_turns":4}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"kind":"entry","seq":0,"turn":` + tc.turn + `}`
			if _, err := DecodeEntry([]byte(line)); err == nil {
				t.Fatalf("DecodeEntry(%q) succeeded, want the invalid notice rejected", line)
			}
		})
	}
	// A valid notice still decodes fine.
	valid := `{"kind":"entry","seq":0,"turn":{"kind":"NOTICE","notice":{"kind":"goal_ended","goal_ended":{"status":"complete"}}}}`
	if _, err := DecodeEntry([]byte(valid)); err != nil {
		t.Fatalf("DecodeEntry(%q) = %v, want nil", valid, err)
	}
}

func TestReadLineBoundExcludesNewlineAndDrainsOversizedRecords(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("1234\n12345\nok\n"))

	line, complete, bytesRead, err := ReadLine(reader, 4)
	if err != nil || !complete || string(line) != "1234" || bytesRead != 5 {
		t.Fatalf("exact-max line = (%q, %t, %d, %v), want (1234, true, 5, nil)", line, complete, bytesRead, err)
	}

	line, complete, bytesRead, err = ReadLine(reader, 4)
	if !errors.Is(err, ErrLineTooLong) || complete || line != nil || bytesRead != 6 {
		t.Fatalf("max+1 line = (%q, %t, %d, %v), want drained ErrLineTooLong", line, complete, bytesRead, err)
	}

	line, complete, bytesRead, err = ReadLine(reader, 4)
	if err != nil || !complete || string(line) != "ok" || bytesRead != 3 {
		t.Fatalf("line after oversized record = (%q, %t, %d, %v), want (ok, true, 3, nil)", line, complete, bytesRead, err)
	}
}

func TestReadLineDiscardsArbitraryUnterminatedTailWithoutRetention(t *testing.T) {
	tail := bytes.Repeat([]byte("x"), 1<<20)
	line, complete, bytesRead, err := ReadLine(bufio.NewReader(bytes.NewReader(tail)), 4)
	if err != nil || complete || line != nil || bytesRead != int64(len(tail)) {
		t.Fatalf("unterminated tail = (len=%d, %t, %d, %v), want (0, false, %d, nil)", len(line), complete, bytesRead, err, len(tail))
	}
}
