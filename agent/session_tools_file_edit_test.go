package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

type minimalNoBatchEnv struct {
	execenv.ExecutionEnvironment
	editCalls int
	lastPath  string
	lastOld   string
	lastNew   string
	lastAll   bool
}

func (e *minimalNoBatchEnv) EditFile(path, oldString, newString string, replaceAll bool) (string, error) {
	e.editCalls++
	e.lastPath = path
	e.lastOld = oldString
	e.lastNew = newString
	e.lastAll = replaceAll
	return "single-edit-ok", nil
}

type minimalCtxNoBatchEnv struct {
	execenv.ExecutionEnvironment
	ctxCalls int
}

func (e *minimalCtxNoBatchEnv) ReadFileCtx(context.Context, string, *int, *int) (string, error) {
	return "", nil
}

func (e *minimalCtxNoBatchEnv) EditFileCtx(_ context.Context, path, oldString, newString string, replaceAll bool) (string, error) {
	e.ctxCalls++
	return "ctx-single-edit-ok:" + path + ":" + oldString + "->" + newString, nil
}

func (e *minimalCtxNoBatchEnv) EditFile(string, string, string, bool) (string, error) {
	panic("EditFile must not be called when EditFileCtx is available")
}

func TestEditFile_MultiEditSessionEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "sample.txt")
	initial := "alpha\nbeta\ngamma\ndelta\n"
	if err := os.WriteFile(target, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("disjoint edits apply atomically via Session", func(t *testing.T) {
		t.Parallel()
		subDir := t.TempDir()
		p := filepath.Join(subDir, "file.txt")
		if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
		adapter := newFakeAdapter("anthropic",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "c1",
					Name:      "edit_file",
					Type:      "function",
					Arguments: json.RawMessage(`{"file_path":"file.txt","edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"delta","new_string":"DELTA"}]}`),
				})
			},
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					for _, part := range m.Content {
						if part.ToolResult != nil && part.ToolResult.ToolCallID == "c1" && part.ToolResult.IsError {
							t.Fatalf("unexpected edit_file error: %v", part.ToolResult.Content)
						}
					}
				}
				return agenttest.FinalResponse("done")
			},
		)
		sess := newSession(t, withDir(subDir), withProfile(newAnthropicProfile("claude-test")), withAdapter(adapter))
		if _, err := sess.ProcessInput(context.Background(), "edit both ends", nil); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		want := "ALPHA\nbeta\ngamma\nDELTA\n"
		if string(got) != want {
			t.Fatalf("file = %q, want %q", got, want)
		}
	})

	t.Run("atomicity on failure leaves file unchanged", func(t *testing.T) {
		t.Parallel()
		subDir := t.TempDir()
		p := filepath.Join(subDir, "file.txt")
		if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
		var sawError bool
		adapter := newFakeAdapter("anthropic",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "c1",
					Name:      "edit_file",
					Type:      "function",
					Arguments: json.RawMessage(`{"file_path":"file.txt","edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"missing","new_string":"NOPE"}]}`),
				})
			},
			func(req llm.Request) llm.Response {
				for _, m := range req.Messages {
					for _, part := range m.Content {
						if part.ToolResult != nil && part.ToolResult.ToolCallID == "c1" {
							sawError = part.ToolResult.IsError
						}
					}
				}
				return agenttest.FinalResponse("done")
			},
		)
		sess := newSession(t, withDir(subDir), withProfile(newAnthropicProfile("claude-test")), withAdapter(adapter))
		if _, err := sess.ProcessInput(context.Background(), "fail atomically", nil); err != nil {
			t.Fatal(err)
		}
		if !sawError {
			t.Fatal("expected edit_file failure when second edit misses")
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != initial {
			t.Fatalf("file mutated on failed batch: %q, want %q", got, initial)
		}
	})

	t.Run("read-before-write warning emitted once per call", func(t *testing.T) {
		t.Parallel()
		warnCalls := 0
		deps := &toolDeps{
			readGuard: readGuard{
				trackRead: func(string) {},
				readBeforeWriteWarning: func(string) string {
					warnCalls++
					return "[RBW_SENTINEL]\n"
				},
			},
		}
		reg := tool.NewRegistry()
		if err := registerFileTools(reg, deps); err != nil {
			t.Fatal(err)
		}
		env := execenv.NewLocalExecutionEnvironment(dir)
		res := reg.ExecuteCall(context.Background(), env, llm.ToolCallData{
			ID:        "c1",
			Name:      "edit_file",
			Arguments: json.RawMessage(`{"file_path":"sample.txt","edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"gamma","new_string":"GAMMA"}]}`),
		})
		if res.IsError {
			t.Fatalf("unexpected error: %s", res.Output)
		}
		if warnCalls != 1 || strings.Count(res.Output, "[RBW_SENTINEL]") != 1 {
			t.Fatalf("warnCalls=%d occurrences=%d output=%q", warnCalls, strings.Count(res.Output, "[RBW_SENTINEL]"), res.Output)
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "ALPHA\nbeta\nGAMMA\ndelta\n" {
			t.Fatalf("file = %q", got)
		}
	})
}

func TestEditFile_FallbackWithoutBatchFileEditor(t *testing.T) {
	t.Parallel()

	t.Run("1-edit falls back to EditFile", func(t *testing.T) {
		env := &minimalNoBatchEnv{}
		args := map[string]any{
			"file_path": "a.txt",
			"edits":     []any{map[string]any{"old_string": "old", "new_string": "new", "replace_all": true}},
		}
		out, err := execFileEdit(context.Background(), env, args, nopReadGuard())
		if err != nil {
			t.Fatalf("execFileEdit: %v", err)
		}
		if out != "single-edit-ok" || env.editCalls != 1 || env.lastPath != "a.txt" || env.lastOld != "old" || env.lastNew != "new" || !env.lastAll {
			t.Fatalf("unexpected fallback result out=%v env=%+v", out, env)
		}
	})

	t.Run("1-edit falls back to EditFileCtx when ContextualFileReader", func(t *testing.T) {
		env := &minimalCtxNoBatchEnv{}
		args := map[string]any{
			"file_path": "b.txt",
			"edits":     []any{map[string]any{"old_string": "x", "new_string": "y"}},
		}
		out, err := execFileEdit(context.Background(), env, args, nopReadGuard())
		if err != nil {
			t.Fatalf("execFileEdit: %v", err)
		}
		if env.ctxCalls != 1 || out != "ctx-single-edit-ok:b.txt:x->y" {
			t.Fatalf("unexpected ctx fallback out=%v calls=%d", out, env.ctxCalls)
		}
	})

	t.Run("multi-edit errors without calling EditFile", func(t *testing.T) {
		env := &minimalNoBatchEnv{}
		args := map[string]any{
			"file_path": "a.txt",
			"edits": []any{
				map[string]any{"old_string": "a", "new_string": "b"},
				map[string]any{"old_string": "c", "new_string": "d"},
			},
		}
		_, err := execFileEdit(context.Background(), env, args, nopReadGuard())
		if err == nil {
			t.Fatal("expected error for multi-edit without BatchFileEditor")
		}
		if env.editCalls != 0 {
			t.Fatalf("EditFile was called %d times on unsupported multi-edit", env.editCalls)
		}
	})
}

func TestEditFile_RejectsMixedFormAndCompetingAliasesBeforeDispatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args string
	}{
		{
			name: "edits array plus top-level old_string and new_string",
			args: `{"file_path":"target.txt","edits":[{"old_string":"one","new_string":"ONE"}],"old_string":"two","new_string":"TWO"}`,
		},
		{
			name: "edits null plus top-level old_string and new_string",
			args: `{"file_path":"target.txt","edits":null,"old_string":"one","new_string":"ONE"}`,
		},
		{
			name: "competing nested aliases with different values",
			args: `{"file_path":"target.txt","edits":[{"old_string":"one","new_str":"ONE","newText":"UNO"}]}`,
		},
		{
			name: "competing top-level aliases with different values",
			args: `{"file_path":"target.txt","old_string":"one","new_str":"ONE","newText":"UNO"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := filepath.Join(dir, "target.txt")
			const orig = "one\ntwo\n"
			if err := os.WriteFile(target, []byte(orig), 0o644); err != nil {
				t.Fatal(err)
			}
			var gotResult *llm.ToolResultData
			adapter := newFakeAdapter("anthropic",
				func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(llm.ToolCallData{
						ID:        "bad1",
						Name:      "edit_file",
						Type:      "function",
						Arguments: json.RawMessage(tc.args),
					})
				},
				func(req llm.Request) llm.Response {
					for _, m := range req.Messages {
						for _, p := range m.Content {
							if p.ToolResult != nil && p.ToolResult.ToolCallID == "bad1" {
								gotResult = p.ToolResult
							}
						}
					}
					return agenttest.FinalResponse("done")
				},
			)
			sess := newSession(t, withDir(dir), withProfile(newAnthropicProfile("claude-test")), withAdapter(adapter))
			if _, err := sess.ProcessInput(context.Background(), "attempt invalid edit", nil); err != nil {
				t.Fatal(err)
			}
			if gotResult == nil || !gotResult.IsError {
				t.Fatalf("expected validation error result, got %+v", gotResult)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != orig {
				t.Fatalf("file modified despite invalid call: %q", got)
			}
		})
	}
}

func TestEditFile_AndApplyPatch_NeverBothAdvertisedInSession(t *testing.T) {
	t.Parallel()
	hasTool := func(defs []llm.ToolDefinition, name string) bool {
		for _, td := range defs {
			if td.Name == name {
				return true
			}
		}
		return false
	}

	t.Run("per-surface initial ToolDefinitions", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name           string
			adapter        string
			profile        *provider.Profile
			wantApplyPatch bool
			wantEditFile   bool
		}{
			{"openai", "openai", NewOpenAIProfile("gpt-5.4"), true, false},
			{"anthropic", "anthropic", newAnthropicProfile("claude-test"), false, true},
			{"google", "google", newGeminiProfile("gemini-2.5-pro"), false, true},
			{"generic-glm", "glm", newOpenAICompatProfile("glm", "glm-5", 0), false, true},
			{"generic-kimi", "kimi", newOpenAICompatProfile("kimi", "kimi-k2.5", 0), false, true},
			{"generic-ollama", "ollama", newOpenAICompatProfile("ollama", "llama3", 0), false, true},
			{"generic-deepseek", "deepseek", newOpenAICompatProfile("deepseek", "deepseek-chat", 0), false, true},
			{"generic-qwen", "qwen", newOpenAICompatProfile("qwen", "qwen3-coder", 0), false, true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				sess := newSession(t,
					withProfile(tc.profile),
					withAdapter(&fakeAdapter{name: tc.adapter}),
					withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
				)
				defs := sess.ToolDefinitions()
				if got := hasTool(defs, "apply_patch"); got != tc.wantApplyPatch {
					t.Fatalf("%s: apply_patch advertised = %v, want %v", tc.name, got, tc.wantApplyPatch)
				}
				if got := hasTool(defs, "edit_file"); got != tc.wantEditFile {
					t.Fatalf("%s: edit_file advertised = %v, want %v", tc.name, got, tc.wantEditFile)
				}
			})
		}
	})

	t.Run("switching model flips apply_patch and edit_file without exposing both", func(t *testing.T) {
		t.Parallel()
		sess := newSession(t,
			withProfile(NewOpenAIProfile("gpt-5.4")),
			withAdapter(&fakeAdapter{name: "openai"}),
			withAdapter(&fakeAdapter{name: "google"}),
			withAdapter(&fakeAdapter{name: "glm"}),
			withConfig(SessionConfig{
				ResolveProfile: testResolver,
				testOnly:       testConfig{skipGitSnapshot: true},
			}),
		)

		assertExclusive := func(wantApplyPatch, wantEditFile bool, phase string) {
			t.Helper()
			defs := sess.ToolDefinitions()
			gotAP := hasTool(defs, "apply_patch")
			gotEF := hasTool(defs, "edit_file")
			if gotAP && gotEF {
				t.Fatalf("%s: both apply_patch and edit_file advertised simultaneously", phase)
			}
			if gotAP != wantApplyPatch || gotEF != wantEditFile {
				t.Fatalf("%s: apply_patch=%v edit_file=%v, want apply_patch=%v edit_file=%v",
					phase, gotAP, gotEF, wantApplyPatch, wantEditFile)
			}
		}

		assertExclusive(true, false, "initial openai")
		if err := sess.SetModel("google/gemini-2.5-pro"); err != nil {
			t.Fatalf("SetModel(google): %v", err)
		}
		assertExclusive(false, true, "after switch to google")
		if err := sess.SetModel("glm/glm-5"); err != nil {
			t.Fatalf("SetModel(glm): %v", err)
		}
		assertExclusive(false, true, "after switch to generic glm")
		if err := sess.SetModel("openai/gpt-5.4"); err != nil {
			t.Fatalf("SetModel(openai): %v", err)
		}
		assertExclusive(true, false, "after switch back to openai")
	})
}

func TestEditFile_RepairedStringifiedEditsExecutesAndRendersLiveAndReopened(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := t.TempDir()
	target := filepath.Join(dir, "code.txt")
	if err := os.WriteFile(target, []byte("foo\nbar\nfoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rawArgs := `{"file_path":"code.txt","edits":"[{\"old_str\":\"foo\",\"newText\":\"FOO\",\"replaceAll\":\"true\",\"notes\":\"rename\"},{\"old_text\":\"bar\",\"new_text\":\"BAR\",\"notes\":\"second\"}]"}`
	adapter := newFakeAdapter("anthropic",
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "c_repaired",
				Name:      "edit_file",
				Type:      "function",
				Arguments: json.RawMessage(rawArgs),
			})
		},
		func(req llm.Request) llm.Response {
			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.ToolResult != nil && p.ToolResult.ToolCallID == "c_repaired" && p.ToolResult.IsError {
						t.Fatalf("repaired edit failed: %v", p.ToolResult.Content)
					}
				}
			}
			return agenttest.FinalResponse("done")
		},
	)
	sess := newSession(t,
		withDir(dir),
		withStateDir(stateDir),
		withProfile(newAnthropicProfile("claude-test")),
		withAdapter(adapter),
	)
	evs, stop := captureEvents(sess)
	if _, err := sess.ProcessInput(context.Background(), "apply stringified repaired edits", nil); err != nil {
		t.Fatal(err)
	}
	stop()

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "FOO\nBAR\nFOO\n" {
		t.Fatalf("edited file = %q, want %q", got, "FOO\nBAR\nFOO\n")
	}

	var sawRepaired, sawStart, sawEnd bool
	var liveArgsJSON string
	for _, ev := range *evs {
		switch d := ev.Data.(type) {
		case events.ToolCallRepairedData:
			if d.CallID == "c_repaired" && len(d.Changes) > 0 {
				sawRepaired = true
			}
		case events.ToolCallStartData:
			if d.CallID == "c_repaired" {
				sawStart = true
				liveArgsJSON = d.ArgumentsJSON
			}
		case events.ToolCallEndData:
			if d.CallID == "c_repaired" && d.Error == "" {
				sawEnd = true
			}
		}
	}
	if !sawRepaired || !sawStart || !sawEnd {
		t.Fatalf("live events missing: repaired=%v start=%v end=%v", sawRepaired, sawStart, sawEnd)
	}
	if summary := toolInputSummary("edit_file", json.RawMessage(liveArgsJSON)); !strings.Contains(summary, "2 edits") {
		t.Fatalf("live toolInputSummary = %q, want 2 edits", summary)
	}

	// Reopen persisted transcript and project through apptranscript.
	w, entries, err := transcript.OpenWriterForSession(sess.TranscriptPath(), sess.ID())
	if err != nil {
		t.Fatalf("reopen transcript: %v", err)
	}
	_ = w.Close()

	reg := apptranscript.NewToolCallRegistry()
	var sawProjectedStart, sawProjectedDone bool
	for i, entry := range entries {
		items := apptranscript.ProjectTurn("turn_"+string(rune('0'+i)), i, entry.Turn, reg, nil, nil)
		for _, item := range items {
			if item.Type != "commandExecution" || item.CallID != "c_repaired" || item.ToolName != "edit_file" {
				continue
			}
			if item.Status == "inProgress" {
				sawProjectedStart = true
				if summary := toolInputSummary("edit_file", json.RawMessage(item.ArgumentsJSON)); !strings.Contains(summary, "2 edits") {
					t.Fatalf("reopened toolInputSummary = %q, want 2 edits", summary)
				}
			} else if item.Status == "completed" {
				sawProjectedDone = true
			}
		}
	}
	if !sawProjectedStart || !sawProjectedDone {
		t.Fatalf("reopened transcript projection missing items: start=%v done=%v", sawProjectedStart, sawProjectedDone)
	}
}

func TestMemoryEdit_MultiEditAndAtomicity(t *testing.T) {
	t.Parallel()

	t.Run("LF and CRLF+BOM multi-edit stamps LF and refreshes index", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			MemoryStateRoot: root,
			MemoryProjectID: "proj-1",
			clock:           agenttest.NewFakeClock(),
		}))
		scopeDir := filepath.Join(root, "projects", "proj-1", "memory")
		if err := os.MkdirAll(scopeDir, 0o700); err != nil {
			t.Fatal(err)
		}

		// Scope confinement check: escaping path fails and writes nothing outside.
		outside := filepath.Join(root, "outside.md")
		if err := os.WriteFile(outside, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if res := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "project",
			"file_path": "../../../outside.md",
			"edits":     []any{map[string]any{"old_string": "keep", "new_string": "clobbered"}},
		}); !res.IsError {
			t.Fatalf("escaping path succeeded: %+v", res)
		}
		if got, _ := os.ReadFile(outside); string(got) != "keep\n" {
			t.Fatalf("outside file mutated: %q", got)
		}

		// LF page multi-edit.
		lfPage := filepath.Join(scopeDir, "lf.md")
		if err := os.WriteFile(lfPage, []byte("---\nwhen: Old LF trigger\n---\n# LF\nfirst\nmiddle\nsecond\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "project",
			"file_path": "lf.md",
			"edits": []any{
				map[string]any{"old_string": "Old LF trigger", "new_string": "Updated LF trigger"},
				map[string]any{"old_string": "first", "new_string": "FIRST"},
				map[string]any{"old_string": "second", "new_string": "SECOND"},
			},
		})
		if res.IsError {
			t.Fatalf("LF multi-edit failed: %s", res.Output)
		}
		gotLF, err := os.ReadFile(lfPage)
		if err != nil {
			t.Fatal(err)
		}
		wantLF := "---\nwhen: Updated LF trigger\n" + memoryOwnStamps(s) + "---\n# LF\nFIRST\nmiddle\nSECOND\n"
		if string(gotLF) != wantLF {
			t.Fatalf("LF page = %q, want %q", gotLF, wantLF)
		}

		// CRLF + BOM page multi-edit -> stamped LF output without BOM.
		crlfPage := filepath.Join(scopeDir, "crlf.md")
		crlfContent := "\xEF\xBB\xBF---\r\nwhen: Old CRLF trigger\r\n---\r\n# CRLF\r\nalpha\r\nbeta\r\n"
		if err := os.WriteFile(crlfPage, []byte(crlfContent), 0o600); err != nil {
			t.Fatal(err)
		}
		res = memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "project",
			"file_path": "crlf.md",
			"edits": []any{
				map[string]any{"old_string": "Old CRLF trigger", "new_string": "Updated CRLF trigger"},
				map[string]any{"old_string": "alpha", "new_string": "ALPHA"},
				map[string]any{"old_string": "beta", "new_string": "BETA"},
			},
		})
		if res.IsError {
			t.Fatalf("CRLF+BOM multi-edit failed: %s", res.Output)
		}
		gotCRLF, err := os.ReadFile(crlfPage)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(string(gotCRLF), "\xEF\xBB\xBF") || strings.Contains(string(gotCRLF), "\r\n") {
			t.Fatalf("stamped memory page retained BOM or CRLF: %q", gotCRLF)
		}
		wantCRLF := "---\nwhen: Updated CRLF trigger\n" + memoryOwnStamps(s) + "---\n# CRLF\nALPHA\nBETA\n"
		if string(gotCRLF) != wantCRLF {
			t.Fatalf("CRLF+BOM page = %q, want %q", gotCRLF, wantCRLF)
		}

		// Refreshed index reflects updated when lines.
		idxRes := memoryExec(t, s, "memory_read", map[string]any{"scope": "project", "file_path": "MEMORY.md"})
		if idxRes.IsError {
			t.Fatalf("MEMORY.md read failed: %s", idxRes.Output)
		}
		if !strings.Contains(idxRes.Output, "Updated LF trigger") || !strings.Contains(idxRes.Output, "Updated CRLF trigger") {
			t.Fatalf("refreshed index missing updated when lines:\n%s", idxRes.Output)
		}
	})

	t.Run("mid-write failure leaves stored page and index unchanged", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			MemoryStateRoot: root,
			clock:           agenttest.NewFakeClock(),
		}))
		scopeDir := filepath.Join(root, "memory")
		subDir := filepath.Join(scopeDir, "ro")
		if err := os.MkdirAll(subDir, 0o700); err != nil {
			t.Fatal(err)
		}
		const orig = "---\nwhen: Original trigger\n---\n# Page\none\ntwo\n"
		pagePath := filepath.Join(subDir, "page.md")
		if err := os.WriteFile(pagePath, []byte(orig), 0o600); err != nil {
			t.Fatal(err)
		}
		// Seed baseline index before making subDir read-only.
		beforeIdx := s.readMemoryIndex("personal")
		if !strings.Contains(beforeIdx.Content, "Original trigger") {
			t.Fatalf("initial index = %q", beforeIdx.Content)
		}

		if err := os.Chmod(subDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(subDir, 0o700) })

		res := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "personal",
			"file_path": "ro/page.md",
			"edits": []any{
				map[string]any{"old_string": "Original trigger", "new_string": "Mutated trigger"},
				map[string]any{"old_string": "one", "new_string": "ONE"},
			},
		})
		if !res.IsError {
			t.Fatalf("expected write failure on read-only parent directory, got %+v", res)
		}
		got, err := os.ReadFile(pagePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("stored page mutated on mid-write failure: %q", got)
		}
		afterIdx := s.readMemoryIndex("personal")
		if afterIdx.Content != beforeIdx.Content {
			t.Fatalf("index changed on failed edit:\nbefore: %s\nafter:  %s", beforeIdx.Content, afterIdx.Content)
		}
	})
}

func TestDefEditFile_ProviderSchemaEmission(t *testing.T) {
	t.Parallel()
	defs := []llm.ToolDefinition{tool.DefEditFile()}

	t.Run("anthropic", func(t *testing.T) {
		emitted := defs
		if sent, changed := (&anthropic.Protocol{}).EmitToolSchemas(defs, registry.Resolved{Protocol: registry.ProtocolAnthropic}); changed {
			emitted = sent
		}
		if len(emitted) != 1 || emitted[0].Name != "edit_file" {
			t.Fatalf("anthropic emitted = %+v", emitted)
		}
		props := emitted[0].Parameters["properties"].(map[string]any)
		if _, ok := props["edits"]; !ok {
			t.Fatalf("anthropic emitted missing edits: %+v", props)
		}
	})

	t.Run("google", func(t *testing.T) {
		emitted := defs
		if sent, changed := (&google.Protocol{}).EmitToolSchemas(defs, registry.Resolved{Protocol: registry.ProtocolGoogle}); changed {
			emitted = sent
		}
		if len(emitted) != 1 || emitted[0].Name != "edit_file" {
			t.Fatalf("google emitted = %+v", emitted)
		}
		props := emitted[0].Parameters["properties"].(map[string]any)
		if _, ok := props["edits"]; !ok {
			t.Fatalf("google emitted missing edits: %+v", props)
		}
	})

	t.Run("openai chatcompletions", func(t *testing.T) {
		for _, strict := range []bool{false, true} {
			row := registry.Resolved{Protocol: registry.ProtocolOpenAIChat}
			row.Caps.StrictTools = new(strict)
			emitted := defs
			if sent, changed := (&chatcompletions.Protocol{}).EmitToolSchemas(defs, row); changed {
				emitted = sent
			}
			if len(emitted) != 1 || emitted[0].Name != "edit_file" {
				t.Fatalf("chatcompletions strict=%v emitted = %+v", strict, emitted)
			}
			props := emitted[0].Parameters["properties"].(map[string]any)
			if _, ok := props["edits"]; !ok {
				t.Fatalf("chatcompletions strict=%v missing edits: %+v", strict, props)
			}
		}
	})

	t.Run("openai responses", func(t *testing.T) {
		for _, strict := range []bool{false, true} {
			row := registry.Resolved{Protocol: registry.ProtocolOpenAIResponses}
			row.Caps.StrictTools = new(strict)
			emitted := defs
			if sent, changed := (&responses.Protocol{}).EmitToolSchemas(defs, row); changed {
				emitted = sent
			}
			if len(emitted) != 1 || emitted[0].Name != "edit_file" {
				t.Fatalf("responses strict=%v emitted = %+v", strict, emitted)
			}
			props := emitted[0].Parameters["properties"].(map[string]any)
			if _, ok := props["edits"]; !ok {
				t.Fatalf("responses strict=%v missing edits: %+v", strict, props)
			}
		}
	})
}
