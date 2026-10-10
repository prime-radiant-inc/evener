package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A resume reopens the session's kept scratch inside the restore. When the
// restore then fails, that scratch still belongs to the session, which can be
// resumed again: the launcher releases it and must not delete it.
func TestFailedResumeKeepsTheSessionsScratch(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	errRestore := errors.New("restore failed after reopening the scratch")
	saveMeta := func(t *testing.T) string {
		t.Helper()
		stateDir := resolvedTempDir(t)
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID: sessionID, ProfileID: "openai", Model: "gpt-test",
			CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
		}); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		return stateDir
	}
	// failingRestore does what a restore does to the environment before it
	// fails: names the scratch after the session and opens it.
	failingRestore := func(t *testing.T, scratch *string) func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
		return func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
			local, ok := env.(*execenv.LocalExecutionEnvironment)
			if !ok {
				t.Errorf("resume environment = %T, want a local environment", env)
				return nil, errRestore
			}
			local.SetScratchIdentity(meta.ID, meta.ID)
			if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
				t.Errorf("open the resumed session's scratch: %v", err)
			}
			*scratch = local.SessionScratchDir()
			return nil, errRestore
		}
	}
	assertKept := func(t *testing.T, err error, scratch string) {
		t.Helper()
		if !errors.Is(err, errRestore) {
			t.Fatalf("launch error = %v, want the restore failure", err)
		}
		if !strings.Contains(scratch, "evener-scratch-"+sessionID) {
			t.Fatalf("resumed scratch = %q, want it in the session's tree", scratch)
		}
		if _, err := os.Stat(scratch); err != nil {
			t.Errorf("failed resume removed the session's kept scratch %s: %v", scratch, err)
		}
	}

	t.Run("run", func(t *testing.T) {
		t.Setenv("TMPDIR", resolvedTempDir(t))
		installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
		oldEnsure, oldRestore := runEnsureUserConfigDirs, runRestoreSession
		t.Cleanup(func() { runEnsureUserConfigDirs, runRestoreSession = oldEnsure, oldRestore })
		runEnsureUserConfigDirs = func() error { return nil }
		var scratch string
		runRestoreSession = failingRestore(t, &scratch)

		stateDir := saveMeta(t)
		err := run(context.Background(), runConfig{
			workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true, resume: sessionID,
			stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
		})
		assertKept(t, err, scratch)
	})

	t.Run("serve", func(t *testing.T) {
		t.Setenv("TMPDIR", resolvedTempDir(t))
		deps := defaultServeDeps()
		deps.ensureConfigDirs = func() error { return nil }
		deps.seedMarketplaces = func(context.Context) error { return nil }
		var scratch string
		deps.restoreSession = failingRestore(t, &scratch)

		stateDir := saveMeta(t)
		err := runServeWithDeps([]string{"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(), "--resume", sessionID}, deps)
		assertKept(t, err, scratch)
	})
}
