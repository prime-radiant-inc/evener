package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestRunAPILogFlagSelectsAttachMode proves the run path's --api-log wiring:
// the default and explicit off attach the ownership-only logger, and on
// attaches the recording logger. The spy delegates to the real attach so the
// reserved file state observed is the real one; the scripted provider makes no
// transport attempts, so the file stays empty in every mode and the attach mode
// itself is the behavior under test.
func TestRunAPILogFlagSelectsAttachMode(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldEnsure := runEnsureUserConfigDirs
	oldAttach := runAttachAPILogger
	oldRestore := runRestoreSession
	t.Cleanup(func() {
		runEnsureUserConfigDirs = oldEnsure
		runAttachAPILogger = oldAttach
		runRestoreSession = oldRestore
	})
	runEnsureUserConfigDirs = func() error { return nil }
	runRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
		return nil, errors.New("stop after attach")
	}

	for _, tc := range []struct {
		name   string
		apiLog string
		want   bool
	}{
		{name: "default is off", apiLog: "", want: false},
		{name: "explicit off", apiLog: "off", want: false},
		{name: "on", apiLog: "on", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
			if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
				ID: sessionID, ProfileID: "openai", Model: "gpt-test",
			}); err != nil {
				t.Fatalf("SaveSessionMeta: %v", err)
			}
			var recorded *bool
			runAttachAPILogger = func(client *llm.Client, dir string, warnings io.Writer, recordAttempts bool) (func(string) error, func() error, error) {
				value := recordAttempts
				recorded = &value
				return oldAttach(client, dir, warnings, recordAttempts)
			}
			cfg := runConfig{
				workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true,
				apiLog: tc.apiLog, resume: sessionID,
				stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
			}
			err := run(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "stop after attach") {
				t.Fatalf("run error = %v, want stop after attach", err)
			}
			if recorded == nil {
				t.Fatal("runAttachAPILogger was never called")
			}
			if *recorded != tc.want {
				t.Fatalf("recordAttempts = %v, want %v", *recorded, tc.want)
			}
			apiPath := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
			info, statErr := os.Stat(apiPath)
			if statErr != nil {
				t.Fatalf("reserved API log not created: %v", statErr)
			}
			if info.Size() != 0 {
				t.Fatalf("API log size = %d, want 0 (scripted provider makes no transport attempts)", info.Size())
			}
		})
	}
}
