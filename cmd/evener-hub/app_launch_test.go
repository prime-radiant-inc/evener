package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
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
	// Fixed empty env so an ambient EVENER_MODEL can never leak in and
	// flip this assertion on a developer machine.
	c := newHubLaunchControllerWithEnv(stateRoot, func(string) string { return "" })
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

func TestLaunchController_SetLayer_RejectsEnabledPlugins(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	empty := []string{}
	c := newHubLaunchController(stateRoot)
	_, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD: cwd, Layer: "global",
		Config: appwire.LaunchConfigLayer{EnabledPlugins: &empty},
	})
	if err == nil || err.Error() != "enabledPlugins is per-launch only" {
		t.Fatalf("SetLayer error = %v, want persistence rejection", err)
	}
	paths, pathErr := launchconfig.PathsFor(stateRoot, cwd)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(paths.Global); !os.IsNotExist(statErr) {
		t.Fatalf("global layer was persisted, stat error = %v", statErr)
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
	paths, err := launchconfig.PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.ProjectFile); err != nil {
		t.Fatalf("project layer was not written to %s: %v", paths.ProjectFile, err)
	}
	if _, err := os.Stat(paths.LegacyProject); !os.IsNotExist(err) {
		t.Fatalf("legacy project layer should not be written, stat err=%v", err)
	}
	got, err := c.GetLayer(context.Background(), appwire.LaunchConfigGetLayerParams{CWD: cwd, Layer: "project"})
	if err != nil {
		t.Fatalf("GetLayer: %v", err)
	}
	if got.Model != "openai/gpt-5" {
		t.Errorf("GetLayer model = %q, want openai/gpt-5", got.Model)
	}
}

func TestLaunchController_GetLayer_ProjectReadsLegacyFallback(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	c := newHubLaunchController(stateRoot)
	paths, err := launchconfig.PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
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
	repoPath := filepath.Join(cwd, ".evener", "launch.toml")
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
	repoPath := filepath.Join(cwd, ".evener", "launch.toml")
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
	paths, err := launchconfig.PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
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
	repoPath := filepath.Join(cwd, ".evener", "launch.toml")
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

func TestLaunchController_ResolveAppliesRuntimeDefaults(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	env := map[string]string{
		"EVENER_MODEL": "anthropic/claude-sonnet-4",
	}
	c := newHubLaunchControllerWithEnv(stateRoot, func(name string) string { return env[name] })
	got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Env floor: EVENER_MODEL fills the unset model.
	if got.Effective.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("Model = %q, want env model", got.Effective.Model)
	}
	if got.Provenance["model"] != "env" {
		t.Errorf("Provenance[model] = %q, want env", got.Provenance["model"])
	}
	// Builtin floor: the agent's own defaults for unset fields.
	if got.Effective.ContextStrategy != "compact" {
		t.Errorf("ContextStrategy = %q, want builtin compact", got.Effective.ContextStrategy)
	}
	if got.Effective.Sandbox != "off" {
		t.Errorf("Sandbox = %q, want builtin off", got.Effective.Sandbox)
	}
	if got.Effective.SandboxNet == nil || !*got.Effective.SandboxNet {
		t.Errorf("SandboxNet = %v, want builtin on", got.Effective.SandboxNet)
	}
	if got.Effective.OpenAIResponsesContinuation != "off" {
		t.Errorf("OpenAIResponsesContinuation = %q, want builtin off", got.Effective.OpenAIResponsesContinuation)
	}
	if got.Effective.AppReplaySize == nil || *got.Effective.AppReplaySize != 1000 {
		t.Errorf("AppReplaySize = %v, want builtin 1000", got.Effective.AppReplaySize)
	}
	if got.Provenance["context_strategy"] != "builtin" {
		t.Errorf("Provenance[context_strategy] = %q, want builtin", got.Provenance["context_strategy"])
	}
	// The layer view itself stays pure: only the effective view carries
	// the runtime floors.
	if len(got.Layers) != 0 {
		t.Errorf("Layers = %#v, want none on disk", got.Layers)
	}
}

func TestLaunchController_ResolveLayerValueWinsOverRuntimeDefault(t *testing.T) {
	stateRoot := t.TempDir()
	cwd := canonicalTempDir(t)
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "launch.toml"), []byte("context_strategy = \"ooda\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newHubLaunchControllerWithEnv(stateRoot, func(string) string { return "" })
	got, err := c.Resolve(context.Background(), appwire.LaunchConfigResolveParams{CWD: cwd})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Effective.ContextStrategy != "ooda" {
		t.Errorf("ContextStrategy = %q, want layer value to win", got.Effective.ContextStrategy)
	}
	if got.Provenance["context_strategy"] != "global" {
		t.Errorf("Provenance[context_strategy] = %q, want global", got.Provenance["context_strategy"])
	}
	// Unrelated unset fields still get their builtins.
	if got.Effective.Sandbox != "off" {
		t.Errorf("Sandbox = %q, want builtin off", got.Effective.Sandbox)
	}
}
