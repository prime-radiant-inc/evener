package transcript

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	const arguments = `{"provider_specific":{"path":"a"}}`
	entryWithSpelling := func(spelling string) string {
		return `{"kind":"entry","seq":0,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c1","name":"read_file","arguments":` + arguments + `,"arguments_spelling":` + spelling + `}}]}}}`
	}
	tests := []struct {
		name       string
		line       string
		permissive bool
	}{
		{name: "unknown entry field", line: `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"},"unknown":true}`},
		{name: "unknown turn field", line: `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT","unknown":true}}`},
		{name: "unknown tool call field", line: `{"kind":"entry","seq":0,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c1","name":"read_file","argumnts":{"path":"a"}}}]}}}`},
		{name: "unknown spelling field after damaged digest", line: entryWithSpelling(`{"sha256":"!","unknown":true}`), permissive: true},
		{name: "unknown edit field after damaged bytes", line: entryWithSpelling(`{"edits":[{"at":0,"bytes":"!","unknown":true}]}`), permissive: true},
		{name: "trailing value", line: `{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}} {}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeEntry([]byte(tc.line)); err == nil {
				t.Fatalf("DecodeEntry(%q) succeeded", tc.line)
			}
			if tc.permissive {
				var entry Entry
				if err := json.Unmarshal([]byte(tc.line), &entry); err != nil {
					t.Fatalf("permissive reader: %v", err)
				}
			}
		})
	}
	digest := sha256.Sum256([]byte(arguments))
	goodDigest := base64.StdEncoding.EncodeToString(digest[:])
	for _, tc := range []struct{ name, digest, edit, want string }{
		{"valid edits", goodDigest, "CQ==", " \t" + arguments},
		{"damaged digest", "!", "CQ==", arguments},
		{"damaged second edit", goodDigest, "!", arguments},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := entryWithSpelling(`{"sha256":"` + tc.digest + `","edits":[{"at":0,"bytes":"IA=="},{"at":0,"bytes":"` + tc.edit + `"}]}`)
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(scanHeaderLine+"\n"+entry+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			w, entries, err := OpenWriterForSession(path, "S")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })
			if len(entries) != 1 {
				t.Fatalf("recovered %d entries, want 1", len(entries))
			}
			if got := entries[0].Turn.Message.Content[0].ToolCall.SentArguments(); got != tc.want {
				t.Errorf("recovered arguments = %q, want %q", got, tc.want)
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

// lineReadResult is one record read, flattened so a whole sequence of reads
// compares as one value.
type lineReadResult struct {
	line      string
	complete  bool
	bytesRead int64
	tooLong   bool
}

// readLineFunc reads one record through either ReadLine or one LineBuffer's
// method, so a sequence runs against both implementations unchanged.
type readLineFunc func(reader *bufio.Reader, maxLineBytes int) ([]byte, bool, int64, error)

// lineReadSequence is one input driven from start to end, with the tuples
// every reader must return for it.
type lineReadSequence struct {
	name         string
	data         string
	bufioSize    int
	maxLineBytes int
	want         []lineReadResult
}

// lineReadSequences covers the boundary cases ReadLine exists to handle. Each
// sequence ends when a read reports an error or an incomplete record, which is
// also why the last tuple of every sequence is one of those two.
var lineReadSequences = []lineReadSequence{
	{
		name:         "consecutive records with an oversized one drained",
		data:         "1234\n12345\nok\n",
		maxLineBytes: 4,
		want: []lineReadResult{
			{line: "1234", complete: true, bytesRead: 5},
			{tooLong: true, bytesRead: 6},
			{line: "ok", complete: true, bytesRead: 3},
			// The input ends: one more read reports EOF at no cost.
			{},
		},
	},
	{
		// A reader smaller than any line forces ReadSlice's ErrBufferFull path,
		// so each record reassembles from fragments split across buffer
		// boundaries. The limit still applies to the whole line.
		name:         "fragmented records across buffer boundaries",
		data:         strings.Repeat("a", 64) + "\n" + strings.Repeat("b", 65) + "\n" + "tail\n",
		bufioSize:    16,
		maxLineBytes: 64,
		want: []lineReadResult{
			{line: strings.Repeat("a", 64), complete: true, bytesRead: 65},
			{tooLong: true, bytesRead: 66},
			{line: "tail", complete: true, bytesRead: 5},
			{},
		},
	},
	{
		// The sequence a reused buffer is most likely to get wrong: a dropped
		// line between two good ones, then an unterminated tail. A buffer that
		// kept the dropped line's bytes, or lost its capacity, corrupts the
		// record that follows — which no consecutive-complete-lines test sees.
		name:         "complete, dropped, complete, unterminated tail",
		data:         "first\n" + strings.Repeat("z", 17) + "\nafter\n" + "unterminated-tail",
		maxLineBytes: 16,
		want: []lineReadResult{
			{line: "first", complete: true, bytesRead: 6},
			{tooLong: true, bytesRead: 18},
			{line: "after", complete: true, bytesRead: 6},
			{bytesRead: int64(len("unterminated-tail"))},
		},
	},
}

// runLineReadSequence drives one reader through one sequence and collects
// every tuple it returns.
func runLineReadSequence(read readLineFunc, seq lineReadSequence) []lineReadResult {
	var reader *bufio.Reader
	if seq.bufioSize > 0 {
		reader = bufio.NewReaderSize(strings.NewReader(seq.data), seq.bufioSize)
	} else {
		reader = bufio.NewReader(strings.NewReader(seq.data))
	}
	var got []lineReadResult
	for {
		line, complete, bytesRead, err := read(reader, seq.maxLineBytes)
		got = append(got, lineReadResult{
			line:      string(line),
			complete:  complete,
			bytesRead: bytesRead,
			tooLong:   errors.Is(err, ErrLineTooLong),
		})
		// ErrLineTooLong is recoverable: the oversized record has been drained
		// and the next read continues at the following one. Note it also
		// reports complete=false, so it has to be handled before the short
		// record case. Any other error, or an unterminated tail, ends the
		// input. Comparing both readers against the same want table also pins
		// that they agree, record for record.
		switch {
		case errors.Is(err, ErrLineTooLong):
			continue
		case err != nil, !complete:
			return got
		}
	}
}

func TestReadLineBoundExcludesNewlineAndDrainsOversizedRecords(t *testing.T) {
	readers := []struct {
		name string
		open func() readLineFunc
	}{
		// ReadLine's signature is readLineFunc's, so the function value is the
		// direct one.
		{name: "ReadLine", open: func() readLineFunc { return ReadLine }},
		// One LineBuffer across the whole sequence: the buffer is what persists
		// between calls, so a fresh one per record would test nothing.
		{name: "LineBuffer", open: func() readLineFunc {
			var b LineBuffer
			return b.ReadLine
		}},
	}
	for _, seq := range lineReadSequences {
		for _, rd := range readers {
			t.Run(seq.name+"/"+rd.name, func(t *testing.T) {
				got := runLineReadSequence(rd.open(), seq)
				if !reflect.DeepEqual(got, seq.want) {
					t.Fatalf("%s over %q (max %d) = %+v, want %+v",
						rd.name, seq.data, seq.maxLineBytes, got, seq.want)
				}
			})
		}
	}
}

func TestReadLineDiscardsArbitraryUnterminatedTailWithoutRetention(t *testing.T) {
	tail := bytes.Repeat([]byte("x"), 1<<20)
	line, complete, bytesRead, err := ReadLine(bufio.NewReader(bytes.NewReader(tail)), 4)
	if err != nil || complete || line != nil || bytesRead != int64(len(tail)) {
		t.Fatalf("unterminated tail = (len=%d, %t, %d, %v), want (0, false, %d, nil)", len(line), complete, bytesRead, err, len(tail))
	}

	// The same unterminated tail through a reused buffer must be dropped rather
	// than kept: a LineBuffer that retained it would hold a megabyte of
	// capacity for a record it is never going to return. Reading a good line
	// afterwards also shows the drop left the buffer usable.
	var b LineBuffer
	line, complete, bytesRead, err = b.ReadLine(bufio.NewReader(bytes.NewReader(tail)), 4)
	if err != nil || complete || line != nil || bytesRead != int64(len(tail)) {
		t.Fatalf("unterminated tail through LineBuffer = (len=%d, %t, %d, %v), want (0, false, %d, nil)", len(line), complete, bytesRead, err, len(tail))
	}
	reader := bufio.NewReader(strings.NewReader("next\n"))
	line, complete, bytesRead, err = b.ReadLine(reader, 4)
	if err != nil || !complete || string(line) != "next" || bytesRead != 5 {
		t.Fatalf("line after unterminated tail through LineBuffer = (%q, %t, %d, %v), want (next, true, 5, nil)", line, complete, bytesRead, err)
	}
}
