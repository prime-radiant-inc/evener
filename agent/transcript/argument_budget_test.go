package transcript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// maxRecordPayload is the guard's ceiling for one record's payload. Main's
// Writer enforces no record budget (the writer-side check is candidate-only in
// #6866), so this is the size a resume must be able to frame, not a limit the
// append path applies.
const maxRecordPayload = 4 << 10

// spellingToolCallTurn builds a tool call whose exact bytes outrun their canonical form.
func spellingToolCallTurn(t *testing.T, exact string) schema.Turn {
	t.Helper()
	call := llm.ToolCallData{ID: "call_budget", Name: "write_file"}
	call.Arguments, call.ArgumentsSpelling = llm.NormalizeToolArguments([]byte(exact))
	if call.ArgumentsSpelling == "" {
		t.Fatalf("fixture arguments %q do not need a second spelling", exact)
	}
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &call},
	}})
}

// TestToolCallSpellingColdReplaysExactly is issue #6897's first acceptance
// point: a record carrying both argument representations cold-replays to the
// caller's exact bytes.
func TestToolCallSpellingColdReplaysExactly(t *testing.T) {
	exact := `{` + strings.Repeat(" ", 2500) + `"content" : "<a&b> \"quoted\"\nline", "path" : "x.png" }`
	turn := spellingToolCallTurn(t, exact)
	wantCanonical := string(turn.Message.Content[0].ToolCall.Arguments)
	w, path := newRecordWriter(t)
	if _, err := w.Record(turn, RecordOptions{Door: DoorBuffered}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lines := readTranscriptLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("transcript holds %d lines, want the header and the tool call", len(lines))
	}
	entryLine := []byte(lines[1])
	for _, field := range []string{`"arguments":`, `"arguments_spelling":`} {
		if !bytes.Contains(entryLine, []byte(field)) {
			t.Fatalf("record does not carry %s alongside its exact spelling: %s", field, entryLine)
		}
	}
	cold, entries, err := OpenWriterForSession(path, sharedFileHeader.SessionID)
	if err != nil {
		t.Fatalf("cold replay: %v", err)
	}
	t.Cleanup(func() { _ = cold.Close() })
	if len(entries) != 1 || len(entries[0].Turn.Message.Content) != 1 {
		t.Fatalf("cold replay returned %d entries, want the one tool call", len(entries))
	}
	if call := entries[0].Turn.Message.Content[0].ToolCall; call == nil || call.WireArguments() != exact || call.SentArguments() != wantCanonical {
		t.Fatalf("cold replay call = %+v, want exact %q and canonical %q", call, exact, wantCanonical)
	}
}

// TestOverLimitToolCallKeepsEarlierAndLaterRecordsRecoverable is issue #6897's
// second acceptance point, pinned to main's live contract. Main's Writer has no
// record budget, so the over-limit turn appends and a whole-file Scan stops at
// it with ErrLineTooLong, while a range reader that drains the bad line still
// decodes both neighbours. The writer-side rejection the issue asks for is
// candidate-only in #6866; a PR that adds it must update this test.
func TestOverLimitToolCallKeepsEarlierAndLaterRecordsRecoverable(t *testing.T) {
	w, path := newRecordWriter(t)
	before, err := w.Record(steeringTurn("before the over-limit call"), RecordOptions{Door: DoorDurable})
	if err != nil || !before.Recorded {
		t.Fatalf("record before = %+v, %v", before, err)
	}
	beforeLen := w.RecordedLength()
	over := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "call_over",
			Name:      "write_file",
			Arguments: json.RawMessage(`{"content":"` + strings.Repeat("x", maxRecordPayload+64) + `"}`),
		},
	}}})
	overRec, overErr := w.Record(over, RecordOptions{Door: DoorDurable})
	if overErr != nil || !overRec.Recorded {
		t.Fatalf("main's writer has no record budget, so the over-limit turn must append: %+v, %v", overRec, overErr)
	}
	if overRec.Length-1 <= maxRecordPayload {
		t.Fatalf("fixture record holds %d payload bytes, want over the %d bound", overRec.Length-1, maxRecordPayload)
	}
	if got := w.RecordedLength(); got != beforeLen+overRec.Length {
		t.Fatalf("accepted over-limit record moved the recorded length to %d, want %d", got, beforeLen+overRec.Length)
	}
	if after, err := w.Record(steeringTurn("after the over-limit call"), RecordOptions{Door: DoorDurable}); err != nil || !after.Recorded {
		t.Fatalf("record after = %+v, %v", after, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if got, want := int64(len(data)), w.RecordedLength(); got != want {
		t.Fatalf("file holds %d bytes, recorded length %d", got, want)
	}
	var lineErr *LineError
	if _, scanErr := Scan(context.Background(), bytes.NewReader(data), maxRecordPayload, nil); !errors.Is(scanErr, ErrLineTooLong) ||
		!errors.As(scanErr, &lineErr) || lineErr.Line != 3 {
		t.Fatalf("scan over the appended over-limit record = %v, want ErrLineTooLong at line 3", scanErr)
	}
	// A range reader keeps its own ReadLine loop and resumes past the drained
	// over-limit record, which is how the good lines stay recoverable.
	reader := bufio.NewReader(bytes.NewReader(data))
	var texts []string
	overLimit := 0
	for i := 0; ; i++ {
		line, complete, _, readErr := ReadLine(reader, maxRecordPayload)
		if errors.Is(readErr, ErrLineTooLong) {
			overLimit++
			continue
		}
		if readErr != nil {
			t.Fatalf("read transcript: %v", readErr)
		}
		if !complete {
			break
		}
		if i == 0 { // header
			continue
		}
		entry, decodeErr := DecodeEntry(line)
		if decodeErr != nil {
			t.Fatalf("decode entry: %v", decodeErr)
		}
		texts = append(texts, entry.Turn.Message.Text())
	}
	if overLimit != 1 {
		t.Fatalf("reader reported %d over-limit lines, want the one the writer appended", overLimit)
	}
	if len(texts) != 2 || texts[0] != "before the over-limit call" || texts[1] != "after the over-limit call" {
		t.Fatalf("recovered texts = %q, want both records around the over-limit line", texts)
	}
}
