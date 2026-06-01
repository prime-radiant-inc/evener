package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
)

func TestHubLaunchControllerSchema(t *testing.T) {
	c := newHubLaunchController(t.TempDir())
	got, err := c.Schema(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if len(got.Options) == 0 {
		t.Fatal("expected schema options")
	}
	if got.Excluded["state_dir"] == "" {
		t.Fatalf("expected state_dir exclusion, got %#v", got.Excluded)
	}
	if got.Options[0].Field != "agent" {
		t.Fatalf("first schema field = %q, want agent", got.Options[0].Field)
	}
}

func TestLaunchController_Resolve_Empty(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
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
	cwd := canonicalTempDir(t)
	c := newHubLaunchController(stateRoot)
	model := "openai/gpt-5"
	fastCheapModel := "openai/gpt-5-mini"
	_, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD: cwd, Layer: "global",
		Config: appwire.LaunchConfigLayer{Model: model, FastCheapModel: fastCheapModel},
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
	if got.FastCheapModel != fastCheapModel {
		t.Errorf("FastCheapModel = %q, want %q", got.FastCheapModel, fastCheapModel)
	}
}

func TestLaunchController_SetLayer_ProjectWritesLocalFile(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	c := newHubLaunchController(stateRoot)
	_, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:    cwd,
		Layer:  "project",
		Config: appwire.LaunchConfigLayer{Model: "openai/gpt-5"},
	})
	if err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	paths := launchconfig.PathsFor(stateRoot, cwd)
	if _, err := os.Stat(paths.Project); err != nil {
		t.Fatalf("project layer was not written to %s: %v", paths.Project, err)
	}
	if _, err := os.Stat(paths.LegacyProject); !os.IsNotExist(err) {
		t.Fatalf("legacy project layer should not be written, stat err=%v", err)
	}
}

func TestLaunchController_GetLayer_ProjectReadsLegacyFallback(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	c := newHubLaunchController(stateRoot)
	paths := launchconfig.PathsFor(stateRoot, cwd)
	if err := launchconfig.SaveLayer(paths.LegacyProject, launchconfig.Layer{Model: "legacy-project"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetLayer(context.Background(), appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "project"})
	if err != nil {
		t.Fatalf("GetLayer: %v", err)
	}
	if got.Model != "legacy-project" {
		t.Fatalf("Model = %q, want legacy-project", got.Model)
	}
}

func TestLaunchController_TrustRepo_RecordsDecision(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
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

func TestLaunchController_TrustRepo_DoesNotCarryRejectedHashes(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rejectedContents := []byte(`model = "rejected"`)
	rejectedHash, err := launchconfig.CanonicalHashTOML(rejectedContents)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte(`model = "trusted"`)
	if err := os.WriteFile(repoPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := launchconfig.CanonicalHashTOML(contents)
	if err != nil {
		t.Fatal(err)
	}
	paths := launchconfig.PathsFor(stateRoot, cwd)
	if err := launchconfig.SaveMeta(paths.Meta, launchconfig.Meta{
		Schema:    1,
		CWD:       cwd,
		CreatedAt: time.Now(),
		Trust: launchconfig.MetaTrust{
			Hashes:   []string{rejectedHash},
			Decision: "rejected",
		},
	}); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	c := newHubLaunchController(stateRoot)
	if _, err := c.TrustRepo(context.Background(), appwire.LaunchConfigTrustRepoParams{CWD: cwd, Hash: hash}); err != nil {
		t.Fatalf("TrustRepo: %v", err)
	}
	meta, err := launchconfig.LoadMeta(paths.Meta)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if launchconfig.HashInSet(rejectedHash, meta.Trust.Hashes) {
		t.Fatalf("rejected hash was carried into trusted set: %#v", meta.Trust.Hashes)
	}
	if !launchconfig.HashInSet(hash, meta.Trust.Hashes) {
		t.Fatalf("new trusted hash missing from trusted set: %#v", meta.Trust.Hashes)
	}
	if meta.Trust.Decision != "trusted" {
		t.Fatalf("decision = %q, want trusted", meta.Trust.Decision)
	}
}

func TestLaunchController_TrustRepo_HashMismatch(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
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
