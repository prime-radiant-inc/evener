package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
)

// A real saved session resumes the old revision after that revision's legacy
// Git metadata was migrated out, and its plugin hook keeps running. The policy
// resolved for the store then names only the managed roots as walk-exempt.
func TestRestoredSessionKeepsMigratedPluginRevisionAndCleanPolicy(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	workspace := filepath.Join(root, "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	store := plugins.NewManager(filepath.Join(root, "plugin-store"))
	pluginRepo := filepath.Join(root, "source")
	revisionFile(t, pluginRepo, ".claude-plugin/plugin.json", []byte(`{"name":"widget","version":"1.0.0"}`), 0o644)
	revisionFile(t, pluginRepo, "skills/probe/SKILL.md", []byte("---\nname: probe\ndescription: fixture\n---\nA-SKILL\n"), 0o644)
	revisionFile(t, pluginRepo, "resources/companion.bin", []byte("A-COMPANION"), 0o644)
	observed := filepath.Join(root, "hook-output")
	script := "#!/bin/sh\ncat \"$CLAUDE_PLUGIN_ROOT/resources/companion.bin\" > \"" + observed + "\"\n"
	revisionFile(t, pluginRepo, "scripts/probe.sh", []byte(script), 0o755)
	revisionFile(t, pluginRepo, "hooks/hooks.json", []byte(`{"hooks":{"PreToolUse":[{"matcher":"use_skill","hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/scripts/probe.sh"}]}]}}`), 0o644)
	revisionGit(t, pluginRepo, "init", "-q")
	revisionGit(t, pluginRepo, "add", ".")
	revisionGit(t, pluginRepo, "commit", "-qm", "A")
	marketRepo := filepath.Join(root, "market")
	revisionFile(t, marketRepo, ".claude-plugin/marketplace.json", []byte(`{"name":"acme","owner":{"name":"fixture"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"}}]}`), 0o644)
	revisionGit(t, marketRepo, "init", "-q")
	revisionGit(t, marketRepo, "add", ".")
	revisionGit(t, marketRepo, "commit", "-qm", "catalog")

	ctx := context.Background()
	if _, err := store.AddMarketplace(ctx, "", plugins.Source{Kind: plugins.SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := NewOpenAIProfile("gpt-5.2")
	original, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir: stateDir, PluginDirs: []string{first.InstallPath}, testOnly: testConfig{skipGitSnapshot: true, metaFS: afero.NewOsFs()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := original.saveMeta(); err != nil {
		t.Fatal(err)
	}
	sessionID := original.ID()
	original.Close()

	// Leave the store in the legacy shape a pre-separation binary wrote, then
	// let the ordinary GC mutation perform the one-time migration.
	if err := os.Remove(filepath.Join(store.Root, "store-format.json")); err != nil {
		t.Fatal(err)
	}
	// Real Git administration, which migration can prove and move out.
	revisionGit(t, first.InstallPath, "init", "-q")
	if _, err := store.Gc(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(first.InstallPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("migration left metadata on the saved revision: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(first.InstallPath, "resources", "companion.bin")); err != nil || string(body) != "A-COMPANION" {
		t.Fatalf("migration changed saved revision bytes: %q, %v", body, err)
	}
	marker, err := os.ReadFile(filepath.Join(store.Root, "store-format.json"))
	if err != nil {
		t.Fatal(err)
	}

	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMeta(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if !slices.Contains(restored.cfg.PluginDirs, first.InstallPath) {
		t.Fatalf("resume changed the saved revision: %v", restored.cfg.PluginDirs)
	}
	for range 2 {
		result := restored.execTool(ctx, useSkillCall("revision-probe", "widget:probe"), "")
		if result.IsError {
			t.Fatalf("restored plugin hook failed: %+v", result)
		}
		if body, err := os.ReadFile(observed); err != nil || string(body) != "A-COMPANION" {
			t.Fatalf("restored hook bytes = %q, %v", body, err)
		}
	}

	skills := filepath.Join(root, "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(store.Root, "cache")
	bundled := filepath.Join(store.Root, "bundled")
	clean := bytes.Contains(marker, []byte(`"managed_content":"clean"`))
	facts := sandbox.HostFacts{
		OS: "linux", Home: root, BwrapCapable: true,
		EvenerContentRoots:        []string{cache, bundled, skills},
		EvenerManagedContentRoots: []string{cache, bundled},
		EvenerManagedContentClean: clean,
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, facts, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !rp.ManagedWalkExempt(cache) || !rp.ManagedWalkExempt(bundled) {
		t.Fatal("clean managed roots are not walk-exempt")
	}
	if rp.ManagedWalkExempt(skills) {
		t.Fatal("user-skills root was exempted")
	}
}
