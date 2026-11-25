package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestRestoreFailureEnvironmentIsSeededWithoutLiveReplay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	start, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "restore-environment",
		Input:            []appwire.InputItem{{Type: "text", Text: "restore-input"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := sess.claimClientMutationStart(); err != nil || !claimed {
		t.Fatalf("claim start: claimed=%v err=%v", claimed, err)
	}
	// Persist the failure intent, then stop before recovery has appended any
	// model context or input. Resume owns the remaining durable transaction.
	if err := sess.beginClientMutationFailure("restore-environment", errors.New("fixture failure")); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	onDisk, err := readTranscriptFull(context.Background(), transcriptPath(restored.stateDir, restored.id), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	entries := onDisk.Entries
	environments, inputs := 0, 0
	for _, entry := range entries {
		switch entry.Turn.Kind {
		case schema.TurnEnvironment:
			environments++
			if entry.Turn.StableTurnID == "" || entry.Turn.StableTurnID == start.Turn.ID {
				t.Fatalf("environment identity = %q, runnable identity = %q", entry.Turn.StableTurnID, start.Turn.ID)
			}
		case schema.TurnUserInput:
			if entry.Turn.StableTurnID == start.Turn.ID {
				inputs++
			}
		}
	}
	if environments != 1 || inputs != 1 {
		t.Fatalf("seeded recovery entries: environment=%d input=%d", environments, inputs)
	}
	var observed []events.EventKind
	drained := make(chan struct{})
	restored.ConsumeEventsLossless(func(event events.SessionEvent) {
		observed = append(observed, event.Kind)
	}, func() { close(drained) })
	restored.Close()
	<-drained
	if len(observed) == 0 || observed[0] != events.EventSessionStart {
		t.Fatalf("first restored event must introduce the session; observed %v", observed)
	}
	for _, kind := range observed {
		if kind == events.EventEnvironment {
			t.Fatal("restore replayed environment already present in the seed transcript")
		}
	}
}

func TestColdRestoreReusesSavedSystemPromptVerbatim(t *testing.T) {
	t.Parallel()
	stateDir, promptPath := t.TempDir(), filepath.Join(t.TempDir(), "launch-prompt.md")
	// The prompt opens with the render-failure diagnostic's words, so the
	// restore path proves it keeps a custom prompt that merely starts like the
	// diagnostic instead of discarding it as prose.
	writeTestFile(t, promptPath, "Template rendering failed: a custom launch prompt. LAUNCH_PROMPT_SENTINEL_4f2a")
	fresh := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{SystemPromptFile: promptPath, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}))
	launch := fresh.cachedSystemPrompt
	fresh.Close()
	meta := loadMetaForTest(t, fresh)
	reuse := func() { // the recorded prompt answers regardless of the old path
		r := restorePromptState(t, stateDir, meta, t.TempDir())
		if !r.systemPromptFrozen || r.cachedSystemPrompt != launch || !strings.Contains(launch, "LAUNCH_PROMPT_SENTINEL_4f2a") ||
			len(r.promptSourceLog) != 1 || r.promptSourceLog[0].Label != restoredSystemPromptLabel {
			t.Fatalf("restore (frozen=%t) sources=%+v did not reuse the recorded prompt:\n%s", r.systemPromptFrozen, r.promptSourceLog, r.cachedSystemPrompt)
		}
	}
	_ = os.Remove(promptPath)
	reuse()
	if _, err := NewSession(w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{SystemPromptFile: promptPath}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh session with a damaged prompt path err = %v, want a read error", err)
	}
	_ = os.Mkdir(promptPath, 0o755)
	reuse()
}

func TestLaunchRenderFailureRecordsNoPromptHeader(t *testing.T) {
	stateDir, promptPath := t.TempDir(), filepath.Join(t.TempDir(), "launch-prompt.md")
	writeTestFile(t, promptPath, "OVERRIDE_AFTER_FAILURE_SENTINEL_2d64")
	tpl := executeSystemPromptTemplate
	forceSystemPromptRenderFailure(t)
	sess := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{MaxSubagentDepth: 1,
		SystemPromptFile: promptPath, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}))
	id := sess.ID()
	if hdr := sessionHeader(t, stateDir, id); hdr.SystemPrompt != "" {
		t.Fatalf("transcript header recorded %q after a failed render, want no prompt", hdr.SystemPrompt)
	}
	executeSystemPromptTemplate = tpl
	if warning, failed := sess.refreshSystemPromptCache(sess.env); failed || warning != "" {
		t.Fatalf("recovery render: failed=%v warning=%q", failed, warning)
	}
	if !sess.systemPromptFrozen || !strings.Contains(sess.cachedSystemPrompt, "OVERRIDE_AFTER_FAILURE_SENTINEL_2d64") {
		t.Fatalf("recovery render did not freeze the override:\n%s", sess.cachedSystemPrompt)
	}
	sess.Close()
	meta := loadMetaForTest(t, sess) // pre-freeze shape: prose in meta only
	meta.LaunchSystemPrompt = "Template rendering failed: forced. Please report this bug."
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	legacyDir := t.TempDir()
	if r := restorePromptState(t, stateDir, meta, legacyDir); !r.systemPromptFrozen ||
		!strings.Contains(r.cachedSystemPrompt, "OVERRIDE_AFTER_FAILURE_SENTINEL_2d64") || !strings.Contains(r.cachedSystemPrompt, legacyDir) {
		t.Fatalf("recorded failure prose swallowed the override:\n%s", r.cachedSystemPrompt)
	}
}

func restorePromptState(t *testing.T, stateDir string, meta schema.SessionMeta, restoreDir string) *Session {
	t.Helper()
	restored, err := RestoreSessionFromMetaWithConfig(w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(restoreDir), meta, RestoreSessionConfig{StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(restored.Close)
	return restored
}
