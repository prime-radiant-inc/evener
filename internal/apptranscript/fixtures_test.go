package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// testMaxLineBytes is the maxLineBytes ceiling used throughout this package's
// tests; it is generous enough that no fixture ever trips it by accident.
const testMaxLineBytes = 1 << 20

// sequentialTestProjector adapts boundedTestProjector into an EntryProjector,
// threading one tool-name resolver across a whole file read.
func sequentialTestProjector() EntryProjector {
	toolNames := map[string]string{}
	return func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return boundedTestProjector(turn, turnID, turnIndex, toolNames)
	}
}

func boundedTestProjector(turn schema.Turn, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
	return ProjectTurn(turnID, turnIndex, turn, toolNames, nil, nil)
}

// requireItemTurnsFromFile is the one full-projection entry point every
// still-live grouping/identity test in this package reads through.
func requireItemTurnsFromFile(t testing.TB, path string, maxLineBytes int, project EntryProjector) []appwire.Turn {
	t.Helper()
	turns, err := ItemTurnsFromFile(path, maxLineBytes, project)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	return turns
}

func writeEntries(t testing.TB, entries ...transcript.Entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "entries.transcript.jsonl")
	data := transcriptHeaderLine(t)
	for _, entry := range entries {
		data = append(data, marshalEntryLine(t, entry)...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func userEntry(seq int, text string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(text)}}
}

func assistantTextEntry(seq int, text string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(text)}}
}

func reservedUserEntry(seq int, text, stableTurnID string) transcript.Entry {
	entry := userEntry(seq, text)
	entry.Turn.StableTurnID = stableTurnID
	return entry
}

func assistantToolCallEntry(seq int, id, name, arguments string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(arguments)}}}}}}
}

func toolResultEntry(seq int, id, name, content string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Name: name, Content: content}}}}}}
}

func turnIDs(turns []appwire.Turn) []string {
	ids := make([]string, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
	}
	return ids
}

func transcriptHeaderLine(t testing.TB) []byte {
	t.Helper()
	line, err := json.Marshal(transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion})
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func marshalEntryLine(t testing.TB, entry transcript.Entry) []byte {
	t.Helper()
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}
