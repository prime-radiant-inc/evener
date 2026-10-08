package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestMemoryPreservation(t *testing.T) {
	t.Parallel()
	t.Run("interrupted-page-and-uncertain-write", func(t *testing.T) {
		root := t.TempDir()
		index := memorySeedPage(t, root, "personal", "fact.md", "opaque-index-original-201")
		seeded, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var writes atomic.Int32
		s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root, StateDir: t.TempDir(), testOnly: testConfig{memoryBeforeIO: func(scope, op string) error {
			if op == "write" && writes.Add(1) == 2 {
				// Interrupt only at the real second write boundary, after the page
				// has committed. No storage result is supplied by the observer.
				cancel()
				return ctx.Err()
			}
			return nil
		}}}), withSteps(
			func(llm.Request) llm.Response {
				return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "page", "content": "opaque-page-202"})
			},
			func(llm.Request) llm.Response {
				return memoryCallResponse("memory_write", map[string]any{"scope": "personal", "file_path": "fact.md", "content": "opaque-page-replacement-203"})
			},
		))
		_, err = s.ProcessInput(ctx, "write one page then another", nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupt error=%v", err)
		}
		if writes.Load() != 2 {
			t.Fatalf("write boundaries=%d", writes.Load())
		}
		if got, err := os.ReadFile(index); err != nil || !bytes.Equal(got, seeded) {
			t.Fatalf("interrupted page=%q err=%v", got, err)
		}
		if res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "page"}); res.IsError || !strings.Contains(res.Output, "opaque-page-202") {
			t.Fatalf("page read=%+v", res)
		}

		// Drop the returned response after the ordinary single-file executor
		// committed. Reconciliation uses a fresh read, not a replay receipt.
		if _, err := s.reg.Get("memory_write").Exec(context.Background(), s.currentEnv(), map[string]any{"scope": "personal", "file_path": "uncertain", "content": "opaque-committed-204"}); err != nil {
			t.Fatal(err)
		}
		if res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "uncertain"}); res.IsError || strings.Count(res.Output, "opaque-committed-204") != 1 {
			t.Fatalf("reconciled=%+v", res)
		}
		if got, err := os.ReadFile(filepath.Join(filepath.Dir(index), "uncertain")); err != nil || string(got) != "opaque-committed-204" {
			t.Fatalf("committed=%q err=%v", got, err)
		}
	})

	t.Run("shared-missing-denied-error", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		index := memorySeedPage(t, root, "personal", "fact.md", "opaque-preserved-205")
		seeded, err := os.ReadFile(index)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "target")
		if err := os.WriteFile(target, []byte("opaque-denied-206"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(filepath.Dir(index), "denied")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(filepath.Dir(index), "directory"), 0o700); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withConfig(SessionConfig{MemoryStateRoot: root}))
		for _, name := range []string{"memory_read", "memory_edit", "memory_delete"} {
			for _, path := range []string{"absent", "denied", "directory"} {
				res := memoryExec(t, s, name, map[string]any{"scope": "personal", "file_path": path, "old_string": "opaque", "new_string": "bad"})
				if name == "memory_delete" && path == "absent" {
					if res.IsError {
						t.Fatal(res.Output)
					}
					continue
				}
				if !res.IsError {
					t.Fatalf("%s %s unexpectedly succeeded", name, path)
				}
			}
		}
		if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "denied", "content": "bad"}); !res.IsError {
			t.Fatal("denied bytes replaced")
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != "opaque-denied-206" {
			t.Fatalf("denied target=%q err=%v", got, err)
		}
		if got, err := os.ReadFile(index); err != nil || !bytes.Equal(got, seeded) {
			t.Fatalf("page=%q err=%v", got, err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(index), "absent")); !os.IsNotExist(err) {
			t.Fatalf("missing file created err=%v", err)
		}
	})

	t.Run("logical-correction-and-removal", func(t *testing.T) {
		root, history := t.TempDir(), t.TempDir()
		index := memorySeedPage(t, root, "personal", "fact.md", "opaque-wrong-207 opaque-keep-index-208")
		wiki := filepath.Dir(index)
		original := map[string]string{"topic": "opaque-wrong-207\nopaque-keep-topic-209\n", "log.md": "opaque-wrong-207\nopaque-keep-log-210\n", "duplicate": "opaque-wrong-207\n", "unrelated": "opaque-unrelated-211\n"}
		for name, body := range original {
			if err := os.WriteFile(filepath.Join(wiki, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		other := memorySeedPage(t, root, "projects/fixture-project", "fact.md", "opaque-other-212")
		otherSeeded, err := os.ReadFile(other)
		if err != nil {
			t.Fatal(err)
		}
		calls := []struct {
			name string
			args map[string]any
		}{
			{"memory_search", map[string]any{"scope": "personal", "pattern": "opaque-wrong-207"}},
			{"memory_edit", map[string]any{"scope": "personal", "file_path": "fact.md", "old_string": "opaque-wrong-207", "new_string": "opaque-correct-213"}},
			{"memory_edit", map[string]any{"scope": "personal", "file_path": "topic", "old_string": "opaque-wrong-207", "new_string": "opaque-correct-213"}},
			{"memory_edit", map[string]any{"scope": "personal", "file_path": "log.md", "old_string": "opaque-wrong-207\n", "new_string": ""}},
			{"memory_delete", map[string]any{"scope": "personal", "file_path": "duplicate"}},
			{"memory_search", map[string]any{"scope": "personal", "pattern": "opaque-wrong-207"}},
		}
		stage := 0
		step := func(req llm.Request) llm.Response {
			if stage > 0 {
				var result *llm.ToolResultData
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.ToolResult != nil {
							result = part.ToolResult
						}
					}
				}
				if result == nil || result.IsError {
					t.Fatalf("correction result=%+v", result)
				}
				if stage == 1 && strings.Count(fmt.Sprint(result.Content), "opaque-wrong-207") != 4 {
					t.Fatalf("search did not find all four fixture copies: %+v", result)
				}
				if stage == len(calls) && strings.Contains(fmt.Sprint(result.Content), "opaque-wrong-207") {
					t.Fatalf("removed fact still found: %+v", result)
				}
			}
			if stage == len(calls) {
				return finalResponse("correction completed")
			}
			call := calls[stage]
			stage++
			return memoryCallResponse(call.name, call.args)
		}
		steps := make([]func(llm.Request) llm.Response, len(calls)+1)
		for i := range steps {
			steps[i] = step
		}
		s := newSession(t, withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root, MemoryProjectID: "fixture-project"}), withSteps(steps...))
		s.appendTurnWithTranscriptMessage(schema.TurnUserInput, llm.User("opaque-original-transcript-214 opaque-wrong-207"), llm.User("opaque-original-transcript-214 opaque-wrong-207"))
		before, err := os.ReadFile(transcriptPath(history, s.id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ProcessInput(context.Background(), "correct fixture fact", nil); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{"fact.md": "---\ndescription: opaque-correct-213 opaque-keep-index-208\n" + memoryOwnStamps(s) + "---\n", "topic": "opaque-correct-213\nopaque-keep-topic-209\n", "log.md": "---\n" + memoryOwnStamps(s) + "---\nopaque-keep-log-210\n", "unrelated": original["unrelated"]} {
			got, err := os.ReadFile(filepath.Join(wiki, name))
			if err != nil || string(got) != want {
				t.Fatalf("%s=%q want=%q err=%v", name, got, want, err)
			}
		}
		if _, err := os.Stat(filepath.Join(wiki, "duplicate")); !os.IsNotExist(err) {
			t.Fatalf("duplicate remains err=%v", err)
		}
		if got, err := os.ReadFile(other); err != nil || !bytes.Equal(got, otherSeeded) {
			t.Fatalf("other=%q err=%v", got, err)
		}
		s.Close()
		after, err := os.ReadFile(transcriptPath(history, s.id))
		if err != nil || !bytes.HasPrefix(after, before) || !bytes.Contains(after, []byte("opaque-original-transcript-214 opaque-wrong-207")) {
			t.Fatalf("original transcript changed err=%v", err)
		}
	})
}

// REAL git: prove the registered managed-worktree remove operation actually
// removes a linked checkout, not merely Evener's decision to request removal.
func TestMemoryPreservationWorktreeLifetime(t *testing.T) {
	t.Parallel()
	workspace, project := memoryGitFixture(t)
	root, history := t.TempDir(), t.TempDir()
	paths := []string{memorySeedPage(t, root, "personal", "fact.md", "opaque-lifetime-215"), memorySeedPage(t, root, filepath.Join("projects", project.ID), "fact.md", "opaque-lifetime-215")}
	seeded, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: history, MemoryStateRoot: root, MemoryProjectID: project.ID, Project: project}))
	if res := memoryExec(t, s, "manage_worktree", map[string]any{"operation": "create", "name": "memory-lane"}); res.IsError {
		t.Fatal(res.Output)
	}
	lane := s.currentEnv().WorkingDirectory()
	if lane == workspace {
		t.Fatal("no linked checkout created")
	}
	if res := memoryExec(t, s, "manage_worktree", map[string]any{"operation": "remove", "name": "memory-lane", "delete_branch": true}); res.IsError {
		t.Fatal(res.Output)
	}
	if _, err := os.Stat(lane); !os.IsNotExist(err) {
		t.Fatalf("lane not removed err=%v", err)
	}
	if s.cfg.MemoryProjectID != project.ID {
		t.Fatal("worktree removal rebound project")
	}
	for _, scope := range []string{"personal", "project"} {
		if res := memoryExec(t, s, "memory_read", map[string]any{"scope": scope, "file_path": "fact.md"}); res.IsError || !strings.Contains(res.Output, "opaque-lifetime-215") {
			t.Fatalf("surviving read=%+v", res)
		}
	}
	s.Close()
	retired := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, MemoryProjectID: project.ID, Project: project}))
	retired.maybeAppendMemoryContext(context.Background(), true)
	if err := retired.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, seeded) {
			t.Fatalf("lifetime bytes=%q err=%v", got, err)
		}
	}
}
