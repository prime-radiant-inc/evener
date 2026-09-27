package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const (
	proseRootID  = "02wMz5Txv1C3Hut0M8GCeB"
	proseChildID = "02wMz5Txv2enqVTitaig6F"
)

func writeFluencyMeta(t *testing.T, stateDir, id, parent string, created time.Time) {
	t.Helper()
	meta := schema.SessionMeta{ID: id, IsSubagent: parent != "", ParentSessionID: parent, CreatedAt: created}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "sessions", id+".meta.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assistantTurn(parts ...llm.ContentPart) schema.Turn {
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: parts})
}

func textPart(s string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: s}
}

// writeProseRun writes a root session that says rootMessage through its
// result tool and a delegate that reports "Child report.".
func writeProseRun(t *testing.T, stateDir, rootMessage string) {
	t.Helper()
	now := time.Now()
	writeFluencyMeta(t, stateDir, proseRootID, "", now)
	writeFluencyMeta(t, stateDir, proseChildID, proseRootID, now.Add(time.Second))
	message, _ := json.Marshal(map[string]any{"message": rootMessage, "end_turn": true})
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(textPart("Looking at the tests."), fluencyToolCall("communicate", string(message))),
		// Arguments that were not valid JSON: the transcript keeps the raw text.
		assistantTurn(llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "call_bad", Name: "communicate", Arguments: json.RawMessage(`{}`), RawArguments: "{not json",
		}}),
	})
	writeFluencyTranscript(t, stateDir, proseChildID, []schema.Turn{
		assistantTurn(fluencyToolCall("communicate", `{"message":"Child report.","end_turn":true}`)),
	})
}

// TestExtractRunProseSplitsRootAndDelegateProse: the user sees only the
// root's result-tool messages; the whole run's prose adds visible assistant
// text and delegate reports; a call with unparseable arguments is skipped.
func TestExtractRunProseSplitsRootAndDelegateProse(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	writeProseRun(t, stateDir, "Fixed — see #12.")
	p, err := extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	if want := []string{"Fixed — see #12."}; !slices.Equal(p.ToUser, want) {
		t.Errorf("ToUser = %q, want %q", p.ToUser, want)
	}
	for _, want := range []string{"Looking at the tests.", "Fixed — see #12.", "Child report."} {
		if !slices.Contains(p.All, want) {
			t.Errorf("All = %q, missing %q", p.All, want)
		}
	}
	if len(p.All) != 3 {
		t.Errorf("All = %q, want exactly 3 pieces", p.All)
	}
}

func TestResultMessagesReadsBothFieldsOnce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args string
		want []string
	}{
		{`{"message":"a"}`, []string{"a"}},
		{`{"message":"a","output":{"message":"b"}}`, []string{"a", "b"}},
		{`{"message":"a","output":{"message":"a"}}`, []string{"a"}},
		{`{"message":"a","output":"plain"}`, []string{"a"}},
		{`{not json`, nil},
	} {
		if got := resultMessages(c.args); !slices.Equal(got, c.want) {
			t.Errorf("resultMessages(%s) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestSummarizeProseGroupsByLabelAndModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	writeProseRun(t, stateDir, "Done — see #12.")
	for rep, status := range map[int]string{1: "passed", 2: "failed"} {
		res := probeResult{Probe: "prose.bugfix-tally", Model: "m", Repetition: rep, Status: status, StateDir: stateDir}
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "m", "prose.bugfix-tally", fmt.Sprintf("rep-%02d", rep), "result.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := summarizeProse([]labeledDir{{Label: "baseline", Dir: dir}})
	if err != nil {
		t.Fatalf("summarizeProse: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want one row", stats)
	}
	s := stats[0]
	if s.Label != "baseline" || s.Model != "m" || s.Runs != 2 || s.Passed != 1 || s.Tasks != 1 || s.TasksAllPassed != 0 {
		t.Errorf("row = %+v, want baseline/m with 2 runs, 1 passed, 1 task, 0 all-passed", s)
	}
	if s.Messages != 2 || s.MedianMessageWords != 3 || s.ToUser.EmDashes != 2 || s.ToUser.OpaqueIDs != 2 {
		t.Errorf("row = %+v, want 2 messages of 3 words with 2 em dashes and 2 ids", s)
	}
	if s.All.Words <= s.ToUser.Words {
		t.Errorf("All.Words = %d, want more than ToUser.Words = %d", s.All.Words, s.ToUser.Words)
	}
}

func TestParseLabeledNeedsBothParts(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "label", "=dir", "label="} {
		if _, _, err := parseLabeled(bad); err == nil {
			t.Errorf("parseLabeled(%q) succeeded, want an error", bad)
		}
	}
	if label, value, err := parseLabeled("v1-A=/tmp/x"); err != nil || label != "v1-A" || value != "/tmp/x" {
		t.Errorf("parseLabeled = %q, %q, %v", label, value, err)
	}
}
