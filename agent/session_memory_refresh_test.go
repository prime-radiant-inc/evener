package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The memory refresh delivers a scope's full index only when the session has
// no baseline for it (start, resume, compaction) and never echoes the
// session's own index writes. These tests drive the real turn loop with a
// scripted provider and play other sessions by writing files under the
// memory root between model calls.

// memoryContextMessages counts the memory-context messages a request carries.
func memoryContextMessages(req llm.Request) int {
	n := 0
	for _, msg := range req.Messages {
		if strings.HasPrefix(msg.Name, "memory_") {
			n++
		}
	}
	return n
}

func writeMemoryIndex(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Another session's index write during a turn reaches no later model call of
// that turn.
func TestMemoryRefreshSkipsLaterRoundsOfATurn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-round-1\n")
	var first int
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(req llm.Request) llm.Response {
			first = memoryContextMessages(req)
			writeMemoryIndex(t, path, "opaque-round-1\nopaque-round-2\n")
			return memoryCallResponse("memory_search", map[string]any{"scope": "personal", "pattern": "opaque-absent"})
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != first {
				t.Fatalf("second round carries %d memory contexts, first carried %d", got, first)
			}
			return finalResponse("done")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if first != 1 || memoryContextCount(s) != 1 {
		t.Fatalf("first round contexts=%d, history contexts=%d, want 1 and 1", first, memoryContextCount(s))
	}
}

// The session's own memory_edit of MEMORY.md becomes its baseline: neither the
// rest of the turn nor the next turn carries a refresh of it.
func TestMemoryRefreshIgnoresOwnIndexEdit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-own-1\n")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root}), withSteps(
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"})
		},
		func(llm.Request) llm.Response {
			return memoryCallResponse("memory_edit", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "old_string": "opaque-own-1", "new_string": "opaque-own-2"})
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("round after the edit carries %d memory contexts, want 1", got)
			}
			return finalResponse("edited")
		},
		func(req llm.Request) llm.Response {
			if got := memoryContextMessages(req); got != 1 {
				t.Fatalf("next turn carries %d memory contexts, want 1", got)
			}
			return finalResponse("next")
		},
	))
	if _, err := s.ProcessInput(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "opaque-own-2\n" {
		t.Fatalf("index=%q err=%v, want the edit applied", got, err)
	}
	if _, err := s.ProcessInput(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
	if got := memoryContextCount(s); got != 1 {
		t.Fatalf("history contexts=%d, want 1", got)
	}
}

// Compaction and resume each deliver the current full index once, and the
// unchanged turn after each delivers nothing.
func TestMemoryRefreshFullIndexAfterCompactionAndResume(t *testing.T) {
	t.Parallel()
	root, history, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	path := memorySeed(t, root, "personal", "opaque-full-1\n")
	s := newScriptedSummaryCompactSession(t, "memory-refresh-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("opaque-refresh-fold")}
	}, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root}))
	wantFull := func(body string) func(llm.Request) llm.Response {
		return func(req llm.Request) llm.Response {
			state, got, _ := memoryRequestIndex(t, req, "personal")
			if state != "current" || got != body {
				t.Fatalf("personal index state=%s body=%q, want current %q", state, got, body)
			}
			return finalResponse("observed")
		}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("opaque-full-1\n"), wantFull("opaque-full-2\n"), wantFull("opaque-full-2\n"),
	}})
	turn := func(sess *Session, wantContexts int) {
		t.Helper()
		if _, err := sess.ProcessInput(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		if got := memoryContextCount(sess); got != wantContexts {
			t.Fatalf("history contexts=%d, want %d", got, wantContexts)
		}
	}
	turn(s, 1)
	writeMemoryIndex(t, path, "opaque-full-2\n")
	for range 12 {
		s.appendTurnWithTranscriptMessage(schema.TurnUserInput, llm.User("opaque-old"), llm.User("opaque-old"))
	}
	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := memoryContextCount(s); got != 0 {
		t.Fatalf("compaction kept %d memory contexts", got)
	}
	turn(s, 1)
	turn(s, 1)
	s.Close()

	meta, err := schema.LoadSessionMeta(history, s.id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: history, MemoryStateRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		wantFull("opaque-full-2\n"), wantFull("opaque-full-2\n"),
	}})
	restored := memoryContextCount(r)
	turn(r, restored+1)
	turn(r, restored+1)
}
