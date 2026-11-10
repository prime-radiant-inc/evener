package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestRepeatReadOfUnchangedFileIsMarkedUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write("one\ntwo\nthree\n")
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))
	read := func(id string, args map[string]any) tool.ExecResult {
		t.Helper()
		args["file_path"] = path
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		res := s.execTool(t.Context(), llm.ToolCallData{ID: id, Name: "read_file", Arguments: raw}, "")
		if res.IsError {
			t.Fatalf("read_file failed: %s", res.Output)
		}
		return res
	}

	first := read("r1", map[string]any{})
	if first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	s.mu.Lock()
	s.totalRounds += 3
	wantTurn := s.totalRounds
	s.mu.Unlock()

	second := read("r2", map[string]any{})
	if second.UnchangedSinceTurn == nil || *second.UnchangedSinceTurn >= wantTurn {
		t.Fatalf("repeat read: UnchangedSinceTurn=%v, want the earlier read's turn", second.UnchangedSinceTurn)
	}
	if !strings.Contains(second.Output, "three") {
		t.Fatal("the content must still be returned")
	}

	if other := read("r3", map[string]any{"offset": 2}); other.UnchangedSinceTurn != nil {
		t.Fatal("a different range is not a repeat")
	}

	// Rewriting with a new size invalidates even though the mtime is pinned.
	write("one\ntwo\nthree\nfour\n")
	if changed := read("r4", map[string]any{}); changed.UnchangedSinceTurn != nil {
		t.Fatal("a changed file must not be marked unchanged")
	}
	if again := read("r5", map[string]any{}); again.UnchangedSinceTurn == nil {
		t.Fatal("re-reading after the change is a repeat of the new read")
	}
}
