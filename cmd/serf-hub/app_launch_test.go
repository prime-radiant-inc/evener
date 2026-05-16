package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/launchconfig"
)

func TestLaunchController_Resolve_Empty(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(stateRoot)
	got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.Model != "" {
		t.Errorf("empty resolved should have no model: %v", got.Effective)
	}
	if got.Repo == nil || got.Repo.Trust != string(launchconfig.TrustAbsent) {
		t.Errorf("repo = %v, want absent", got.Repo)
	}
}

func TestLaunchController_SetLayer_GlobalRoundtrip(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(stateRoot)
	model := "openai/gpt-5"
	_, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD: cwd, Layer: "global",
		Config: appwire.LaunchConfigLayer{Model: model},
	})
	if err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	got, err := c.GetLayer(context.Background(), appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "global"})
	if err != nil {
		t.Fatalf("GetLayer: %v", err)
	}
	if got.Model != model {
		t.Errorf("Got = %q, want %q", got.Model, model)
	}
}

func TestLaunchController_TrustRepo_RecordsDecision(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := []byte(`model = "from-repo"`)
	if err := os.WriteFile(repoPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, _ := launchconfig.CanonicalHashTOML(contents)

	c := newHubLaunchController(stateRoot)
	got, err := c.TrustRepo(context.Background(), appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash})
	if err != nil {
		t.Fatalf("TrustRepo: %v", err)
	}
	if got.Repo == nil || got.Repo.Trust != string(launchconfig.TrustTrusted) {
		t.Errorf("trust after TrustRepo = %v", got.Repo)
	}
	if got.Effective.Model != "from-repo" {
		t.Errorf("trusted in-repo did not contribute: %v", got.Effective)
	}
}

func TestLaunchController_TrustRepo_HashMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := t.TempDir()
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoPath, []byte(`model = "x"`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newHubLaunchController(stateRoot)
	if _, err := c.TrustRepo(context.Background(), appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: "sha256:nope"}); err == nil {
		t.Errorf("TrustRepo with wrong hash should error")
	}
}
