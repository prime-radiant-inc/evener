//go:build serffuzz

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzSessionAuxExactProgram closes the deterministic branch matrix for the
// session environment, self-compaction, fork, slash-command, and event helpers.
func FuzzSessionAuxExactProgram(f *testing.F) {
	for op := byte(0); op < 20; op++ {
		f.Add(op)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		switch op % 20 {
		case 0:
			s := &Session{}
			s.maybeElicitNoteBeforeCompaction(context.Background(), nil, 0)
			if s.maybeNudgeSelfCompact(0) {
				t.Fatal("nil manager nudged")
			}
		case 1:
			s := newSession(t)
			s.setPinnedNote("keep")
			s.maybeElicitNoteBeforeCompaction(context.Background(), nil, 0)
			s.contextMgr.WarnThreshold = 100
			if s.maybeNudgeSelfCompact(0) {
				t.Fatal("low pressure nudged")
			}
		case 2:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 1
			h := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("x"))}
			s.maybeElicitNoteBeforeCompaction(context.Background(), h, 0)
		case 3:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 0
			s.contextMgr = contextmgr.NewManager(s.profile, nil)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 0
			s.maybeElicitNoteBeforeCompaction(context.Background(), []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("x"))}, 0)
		case 4:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 0
			s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "  ", nil }
			s.maybeElicitNoteBeforeCompaction(context.Background(), []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("x"))}, 0)
		case 5:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 0
			s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "note", nil }
			s.maybeElicitNoteBeforeCompaction(context.Background(), []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("x"))}, 0)
			if s.PinnedNote() != "note" {
				t.Fatal("note not pinned")
			}
		case 6:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 0
			s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", errors.New("fault") }
			s.maybeElicitNoteBeforeCompaction(context.Background(), []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("x"))}, 0)
		case 7:
			s := newSession(t)
			s.contextMgr.WarnThreshold = 0
			if !s.maybeNudgeSelfCompact(0) || s.maybeNudgeSelfCompact(0) {
				t.Fatal("nudge latch contract")
			}
		case 8:
			s := newSession(t)
			s.pluginCommands = map[string]plugin.Command{"p:greet": {Name: "greet", PluginName: "p", Body: "Hi $ARGUMENTS"}}
			for _, in := range []string{"plain", "/", "/missing x"} {
				s.expandSlashCommand(context.Background(), in)
			}
			if got, ok := s.expandSlashCommand(context.Background(), "/greet world"); !ok || got != "Hi world" {
				t.Fatalf("expand = %q,%v", got, ok)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s.expandSlashCommand(ctx, "/greet world")
		case 9:
			s := newSession(t)
			s.pendingPluginEvents = []events.PluginLoadedData{{Name: "p"}}
			s.pendingHookWarnings = []events.WarningData{{Message: "hook"}}
			s.pendingMCPWarnings = []events.WarningData{{Message: "mcp"}}
			s.emitSessionStartEnvelope(events.SessionStartData{Model: "m"}, []promptSource{{Label: "x", Size: 1}})
			if s.pendingPluginEvents != nil || s.pendingHookWarnings != nil || s.pendingMCPWarnings != nil {
				t.Fatal("pending events retained")
			}
			s.emitDiagnosticWarning(events.WarningData{Message: "ok"})
			var nilSession *Session
			nilSession.emitDiagnosticWarning(events.WarningData{})
		case 10:
			s := newSession(t)
			s.runNotificationHook(context.Background(), "ignored")
		case 11:
			fuzzAuxForkBasic(t)
		case 12:
			fuzzAuxForkCorrupt(t)
		case 13:
			fuzzAuxForkSuccess(t, false)
		case 14:
			fuzzAuxForkSuccess(t, true)
		case 15:
			s := newSession(t, withConfig(SessionConfig{NoProjectPrompts: true}), withoutGitSnapshot())
			base := s.currentEnv().(*execenv.LocalExecutionEnvironment)
			next := base.WithWorkingDirectory(t.TempDir())
			s.swapEnvAndRefresh(next)
		case 16:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 1
			s.maybeElicitNoteBeforeCompaction(context.Background(), nil, 0)
		case 17:
			s := newSession(t)
			s.contextMgr.CheckpointThreshold = 0
			s.contextMgr.PreserveRecentTurns = 0
			s.maybeElicitNoteBeforeCompaction(context.Background(), []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("x"))}, 0)
		case 18:
			s := newSession(t, withConfig(SessionConfig{ReasoningEffort: "low"}))
			runner := hooks.NewRunner(nil, "")
			runner.Add(plugin.HookNotification, plugin.RegisteredHook{Matcher: "*", Type: "command", Timeout: 5, Command: `printf '%s\n' '{"hookSpecificOutput":{"additionalContext":"model","systemMessage":"user"}}'`})
			runner.Add(plugin.HookNotification, plugin.RegisteredHook{Matcher: "*", Type: "command", Timeout: 5, Command: `printf '%s\n' 'user'`})
			s.hookRunner = runner
			s.runNotificationHook(context.Background(), "notice")
			_ = s.Events()
			_ = warningHookMessage((*events.WarningData)(nil))
			_ = warningHookMessage(&events.WarningData{Message: "pointer"})
			_ = warningHookMessage(events.SessionEndData{Reason: "done"})
			var nilSession *Session
			nilSession.emitWithProvenance(events.EventWarning, events.WarningData{}, nil)
		case 19:
			s := newSession(t)
			repo := t.TempDir()
			runGitCmd(t, repo, "init")
			if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitCmd(t, repo, "add", "tracked")
			runGitCmd(t, repo, "commit", "-m", "seed")
			base := s.currentEnv().(*execenv.LocalExecutionEnvironment)
			s.swapEnvAndRefresh(base.WithWorkingDirectory(repo))
		}
	})
}

func FuzzForkExactFaultProgram(f *testing.F) {
	for op := byte(0); op < 10; op++ {
		f.Add(op)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		if op%10 < 2 {
			fs, state, id := afero.NewMemMapFs(), "/state", "parent"
			dir := filepath.Join(state, sessionsSubdir)
			if err := fs.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			oversize := bytes.Repeat([]byte("x"), 128)
			body := oversize
			if op%10 == 1 {
				body = append([]byte(`{"session_id":"parent"}`+"\n"), oversize...)
			}
			if err := afero.WriteFile(fs, filepath.Join(dir, id+".transcript.jsonl"), body, 0o644); err != nil {
				t.Fatal(err)
			}
			deps := forkSessionDeps{maxScanToken: 64, newWriter: func(fs afero.Fs, path string, h transcript.Header) (forkTranscriptWriter, error) {
				return transcript.NewWriterWithFS(fs, path, h)
			}, saveMeta: schema.SaveSessionMetaWithFS}
			_, _ = forkSessionWithDeps(fs, state, id, 1, "x", "", deps)
			return
		}
		fs, state, id := fuzzAuxParentFS(t)
		mode := op % 10
		writer := &fuzzAuxWriter{failAppendAt: -1}
		deps := forkSessionDeps{
			newWriter: func(afero.Fs, string, transcript.Header) (forkTranscriptWriter, error) {
				if mode == 4 {
					return nil, errors.New("new writer fault")
				}
				if mode == 5 {
					writer.failAppendAt = 0
				}
				if mode == 6 {
					writer.failAppendAt = 2
				}
				if mode == 7 {
					writer.closeErr = errors.New("close fault")
				}
				return writer, nil
			},
			saveMeta: func(fs afero.Fs, state string, meta schema.SessionMeta) error {
				if mode == 8 {
					return errors.New("save fault")
				}
				if mode == 9 && meta.ID == id {
					return errors.New("parent save fault")
				}
				return schema.SaveSessionMetaWithFS(fs, state, meta)
			},
		}
		if mode == 2 {
			_, _ = forkSessionFS(fuzzAuxOpenFaultFS{Fs: fs}, state, id, 1, "x", "")
			return
		}
		if mode == 3 {
			_ = fs.Remove(filepath.Join(state, sessionsSubdir, id+".meta.json"))
		}
		if mode == 9 {
			_, _ = forkSessionWithDeps(fs, state, id, 4, "edit", "", deps)
		}
		_, _ = forkSessionWithDeps(fs, state, id, 3, "edit", "label", deps)
	})
}

type fuzzAuxOpenFaultFS struct{ afero.Fs }

func (fs fuzzAuxOpenFaultFS) Open(name string) (afero.File, error) {
	return nil, fmt.Errorf("open fault: %w", os.ErrPermission)
}

type fuzzAuxWriter struct {
	appends      int
	failAppendAt int
	closeErr     error
}

func (w *fuzzAuxWriter) Append(schema.Turn) error {
	if w.appends == w.failAppendAt {
		return errors.New("append fault")
	}
	w.appends++
	return nil
}
func (w *fuzzAuxWriter) Close() error { err := w.closeErr; w.closeErr = nil; return err }

func fuzzAuxParentFS(t *testing.T) (afero.Fs, string, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const state, id = "/state", "parent"
	w, err := transcript.NewWriterWithFS(fs, filepath.Join(state, sessionsSubdir, id+".transcript.jsonl"), transcript.Header{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("u")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("a")), schema.NewTurn(schema.TurnUserInput, llm.User("u2"))} {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMetaWithFS(fs, state, schema.SessionMeta{ID: id}); err != nil {
		t.Fatal(err)
	}
	return fs, state, id
}

func fuzzAuxForkBasic(t *testing.T) {
	_, _ = ForkSession(t.TempDir(), "missing", 1, "x", "")
	fs := afero.NewMemMapFs()
	for _, tc := range []struct {
		id   string
		turn int
	}{{"missing", 0}, {"missing", 1}} {
		_, _ = forkSessionFS(fs, "/state", tc.id, tc.turn, "x", "")
	}
	_ = fs.MkdirAll("/state/sessions", 0o755)
	_ = afero.WriteFile(fs, "/state/sessions/empty.transcript.jsonl", nil, 0o644)
	_, _ = forkSessionFS(fs, "/state", "empty", 1, "x", "")
}

func fuzzAuxForkCorrupt(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/state/sessions", 0o755)
	for i, body := range [][]byte{
		[]byte("not-json\n"),
		[]byte("{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"b\"}\n{\"kind\":\"api_call\"}\n"),
	} {
		id := string(rune('a' + i))
		_ = afero.WriteFile(fs, "/state/sessions/"+id+".transcript.jsonl", body, 0o644)
		_, err := forkSessionFS(fs, "/state", id, 1, "x", "")
		if i == 1 && !errors.Is(err, transcript.ErrUnsupportedFormat) {
			t.Fatalf("mixed transcript error = %v, want transcript.ErrUnsupportedFormat", err)
		}
	}
}

func fuzzAuxForkSuccess(t *testing.T, label bool) {
	fs := afero.NewMemMapFs()
	const state, id = "/state", "parent"
	path := filepath.Join(state, sessionsSubdir, id+".transcript.jsonl")
	w, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: id, ProfileID: "p", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("u1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("a1")),
		schema.NewTurn(schema.TurnUserInput, llm.User("u2")),
	} {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMetaWithFS(fs, state, schema.SessionMeta{ID: id, ProfileID: "p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	forkLabel := ""
	if label {
		forkLabel = "branch"
	}
	child, err := forkSessionFS(fs, state, id, 3, "edit", forkLabel)
	if err != nil || child == "" {
		t.Fatalf("fork = %q, %v", child, err)
	}
	data, _ := afero.ReadFile(fs, filepath.Join(state, sessionsSubdir, child+".transcript.jsonl"))
	if !bytes.Contains(data, []byte("edit")) {
		t.Fatal("edited turn absent")
	}
	if label {
		meta, _ := schema.LoadSessionMetaWithFS(fs, state, id)
		if meta.ForkLabel != "branch" {
			t.Fatal("label absent")
		}
	}
}
