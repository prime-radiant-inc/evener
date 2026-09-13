package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestServeAPILogFlagSelectsAttachMode proves the serve path's --api-log
// wiring: the default and explicit off attach the ownership-only logger, and on
// attaches the recording logger. The spy delegates to the real attach so the
// reserved file state observed is the real one.
func TestServeAPILogFlagSelectsAttachMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
		want bool
	}{
		{name: "default is off", flag: "", want: false},
		{name: "explicit off", flag: "off", want: false},
		{name: "on", flag: "on", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
			meta := schema.SessionMeta{
				ID: sessionID, ProfileID: "openai", Model: "gpt-test",
				CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
			}
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatalf("SaveSessionMeta: %v", err)
			}
			var recorded *bool
			deps := defaultServeDeps()
			deps.ensureConfigDirs = func() error { return nil }
			deps.seedMarketplaces = func(context.Context) error { return nil }
			deps.attachAPILogger = func(client *llm.Client, dir string, warnings io.Writer, recordAttempts bool) (func(string) error, func() error, error) {
				value := recordAttempts
				recorded = &value
				return serveAttachSessionAPILogger(client, dir, warnings, recordAttempts)
			}
			deps.restoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
				return nil, errors.New("restore reached")
			}
			args := []string{"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(), "--resume", sessionID}
			if tc.flag != "" {
				args = append(args, "--api-log", tc.flag)
			}
			serveErr := runServeWithDeps(args, deps)
			if serveErr == nil || !strings.Contains(serveErr.Error(), "restore reached") {
				t.Fatalf("serve error = %v, want restore reached", serveErr)
			}
			if recorded == nil {
				t.Fatal("attachAPILogger was never called")
			}
			if *recorded != tc.want {
				t.Fatalf("recordAttempts = %v, want %v", *recorded, tc.want)
			}
			apiPath := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
			if _, err := os.Stat(apiPath); err != nil {
				t.Fatalf("reserved API log not created: %v", err)
			}
		})
	}
}

// TestServeAPILogRejectsInvalidValue pins the fail-loud contract for a typo'd
// value: serve refuses to start rather than silently defaulting.
func TestServeAPILogRejectsInvalidValue(t *testing.T) {
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	stateDir := t.TempDir()
	err := runServeWithDeps([]string{"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(), "--api-log", "maybe"}, deps)
	if err == nil || !strings.Contains(err.Error(), "invalid --api-log") {
		t.Fatalf("serve error = %v, want invalid --api-log", err)
	}
}
