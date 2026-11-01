package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm"
)

// badLaunchSettings are shared-flag values that must stop a launch before any
// client or session exists, whichever command was asked to launch.
var badLaunchSettings = []struct {
	name        string
	idleTimeout string
	apiLog      string
	wantAPILog  bool
}{
	{name: "negative provider idle timeout", idleTimeout: "-1s"},
	{name: "unparseable provider idle timeout", idleTimeout: "soon"},
	{name: "unknown api-log value", apiLog: "maybe", wantAPILog: true},
}

func TestRunRejectsBadLaunchSettingsBeforeBuildingAClient(t *testing.T) {
	oldEnsure, oldLoad := runEnsureUserConfigDirs, runLoadClient
	t.Cleanup(func() { runEnsureUserConfigDirs, runLoadClient = oldEnsure, oldLoad })
	runEnsureUserConfigDirs = func() error { return nil }
	for _, tc := range badLaunchSettings {
		t.Run(tc.name, func(t *testing.T) {
			clientBuilt := false
			runLoadClient = func(string) (*llm.Client, error) {
				clientBuilt = true
				return nil, errors.New("client built")
			}
			dir := t.TempDir()
			err := run(context.Background(), runConfig{
				workDir: dir, stateDir: dir, prompt: "hi", noDefaultMarketplaces: true,
				providerIdleTimeout: tc.idleTimeout, apiLog: tc.apiLog,
				stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
			})
			if err == nil {
				t.Fatal("run accepted the bad value")
			}
			if clientBuilt {
				t.Fatal("run built a client before rejecting the bad value")
			}
			if tc.wantAPILog && !errors.Is(err, errInvalidAPILog) {
				t.Fatalf("run error = %v, want errInvalidAPILog", err)
			}
		})
	}
}

func TestServeRejectsBadLaunchSettingsBeforeBuildingAClient(t *testing.T) {
	for _, tc := range badLaunchSettings {
		t.Run(tc.name, func(t *testing.T) {
			clientBuilt := false
			deps := defaultServeDeps()
			deps.ensureConfigDirs = func() error { return nil }
			deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
				clientBuilt = true
				return nil, nil, errors.New("client built")
			}
			dir := t.TempDir()
			args := []string{"--dir", dir, "--state-dir", dir, "--run-dir", t.TempDir()}
			if tc.idleTimeout != "" {
				args = append(args, "--provider-idle-timeout", tc.idleTimeout)
			}
			if tc.apiLog != "" {
				args = append(args, "--api-log", tc.apiLog)
			}
			err := runServeWithDeps(args, deps)
			if err == nil {
				t.Fatal("serve accepted the bad value")
			}
			if clientBuilt {
				t.Fatal("serve built a client before rejecting the bad value")
			}
			if tc.wantAPILog && !errors.Is(err, errInvalidAPILog) {
				t.Fatalf("serve error = %v, want errInvalidAPILog", err)
			}
		})
	}
}

func TestResolveLaunchSettingsCarriesTheFlagValues(t *testing.T) {
	t.Setenv(envvars.EVENEROpenAIResponsesContinuation.Name, "")
	got, err := resolveLaunchSettings("5m", "on", " auto ")
	if err != nil {
		t.Fatal(err)
	}
	if !got.apiLogEnabled || got.openAIResponsesContinuation != "auto" {
		t.Fatalf("settings = %+v", got)
	}
	got, err = resolveLaunchSettings("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.apiLogEnabled || got.openAIResponsesContinuation != "" {
		t.Fatalf("defaults = %+v", got)
	}
}
