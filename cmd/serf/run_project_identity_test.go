package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestRunPassesCanonicalProjectAndActiveWorkingDirToSession(t *testing.T) {
	t.Setenv("SERF_STATE_DIR", "")
	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "linked-worktree")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}

	oldEnsure, oldSeed, oldAttach, oldNew := runEnsureUserConfigDirs, runSeedMarketplaces, runAttachAPILogger, runNewSession
	t.Cleanup(func() {
		runEnsureUserConfigDirs, runSeedMarketplaces, runAttachAPILogger, runNewSession = oldEnsure, oldSeed, oldAttach, oldNew
	})
	runEnsureUserConfigDirs = func() error { return nil }
	runSeedMarketplaces = func() error { return nil }
	runAttachAPILogger = func(*llm.Client, string, io.Writer) (func(string) error, func() error, error) {
		return func(string) error { return nil }, func() error { return nil }, nil
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{}})
	var gotCfg agent.SessionConfig
	var gotCWD string
	runNewSession = func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		gotCfg = cfg
		gotCWD = env.WorkingDirectory()
		return nil, errors.New("capture session config")
	}

	err := run(context.Background(), runConfig{prompt: "test", model: "openai/test", workDir: alias, stdout: io.Discard, stderr: io.Discard, noDefaultMarketplaces: true})
	wantProject, resolveErr := identifier.ResolveProject(alias)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err == nil || gotCfg.Project != wantProject || gotCWD != alias {
		t.Fatalf("run error=%v project=%+v cwd=%q, want project=%+v active=%q", err, gotCfg.Project, gotCWD, wantProject, alias)
	}
}
