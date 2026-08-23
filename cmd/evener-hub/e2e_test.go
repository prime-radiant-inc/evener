package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/internal/e2ecap"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_HubAndDaemon pins the discovery half of the hub: a daemon the hub did
// NOT spawn, started by hand the way a user starts one, must appear in the
// roster. That is the only coverage of the rendezvous path
// ($XDG_STATE_HOME/evener/run/<pid>.json -> hubcore.Roster.Refresh -> the "local"
// LocalDaemonSource), and every other e2e in this package goes the other way,
// asking the hub to spawn the daemon itself.
//
// It used to poll `GET /live` with no credential and assert on the HTML. Both
// halves had rotted: /live is not a route any more (the roster moved to the
// AppWire thread list when the frontend was rewritten), and the hub's AuthGuard
// answers every unauthenticated request "unauthorized", so the poll read an
// auth failure and the assertion "the roster picked up the daemon" could not
// come true. It was invisible because the test also required live credentials
// to run at all -- for a readiness check that never calls a model.
//
// So it now reads the roster the way the rest of the file does: over the
// authenticated AppWire socket, against a scripted provider. It needs no API
// key and runs in the ordinary suite, which is the point -- a test only this
// package's e2e set can reach is a test nobody runs.
func TestE2E_HubAndDaemon(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	// A daemon of our own, on the hub's HOME/XDG_STATE_HOME so they share
	// $XDG_STATE_HOME/evener/run, started exactly as a user would. The hub
	// knows nothing about it beyond what the daemon writes there.
	daemonDir := t.TempDir()
	daemon := exec.Command(filepath.Join(stack.binDir, "evener"), "serve",
		"--model", stack.model,
		"--addr", "127.0.0.1:0",
		"--dir", daemonDir,
	)
	// XDG_* still point into the package's shared throwaway root, which a
	// hand-started daemon treats as its own config: it syncs plugin
	// marketplaces there, and a clone interrupted by this test's cleanup
	// leaves a .git TestMain's RemoveAll then refuses to delete, which prints
	// a leak line on an otherwise passing run. Point them at the stack's HOME
	// so the daemon writes only inside its own tree.
	daemon.Env = append(os.Environ(),
		"HOME="+stack.home,
		"XDG_CONFIG_HOME="+filepath.Join(stack.home, "config"),
		"XDG_STATE_HOME="+filepath.Join(stack.home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(stack.home, "cache"),
	)
	daemonLog, err := os.Create(filepath.Join(stack.home, "daemon.log"))
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}
	daemon.Stdout = daemonLog
	daemon.Stderr = daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
		_ = daemonLog.Close()
		if t.Failed() {
			if body, readErr := os.ReadFile(filepath.Join(stack.home, "daemon.log")); readErr == nil {
				t.Logf("daemon log:\n%s", body)
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	// The daemon's own working directory is what identifies it: the hub
	// spawned nothing here, so a thread carrying that CWD can only have come
	// from the rendezvous entry the daemon wrote.
	deadline := time.Now().Add(30 * time.Second)
	var listed []appwire.Thread
	for time.Now().Before(deadline) {
		list, err := clientRequest[appwire.ThreadListResponse](ctx, client, appwire.MethodThreadList, appwire.ThreadListParams{})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		listed = list.Data
		for _, thread := range listed {
			if thread.CWD != daemonDir {
				continue
			}
			if thread.Source != "local" {
				t.Fatalf("the daemon was listed under source %q, want the local rendezvous source", thread.Source)
			}
			if thread.Evener.Ref == "" {
				t.Fatalf("the roster listed the daemon with no ref, so nothing can address it: %#v", thread)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the hub roster never listed the hand-started daemon at %s. thread/list last returned %d threads: %#v", daemonDir, len(listed), listed)
}

func TestE2E_LayeredLaunchConfig(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("e2e")
	}
	stateRoot := t.TempDir()
	cwd := t.TempDir()

	// Global layer.
	fifty := 50
	if err := launchconfig.SaveLayer(filepath.Join(stateRoot, "launch.toml"), launchconfig.Layer{
		Model:      "openai/gpt-5-mini-2025-08-07",
		SkillsDirs: []string{"/global/skills"},
		MaxRounds:  &fifty,
	}); err != nil {
		t.Fatal(err)
	}

	// Local per-project layer.
	paths, err := launchconfig.PathsFor(stateRoot, cwd)
	if err != nil {
		t.Fatal(err)
	}
	pid := paths.Project.ID
	if err := launchconfig.SaveLayer(paths.ProjectFile, launchconfig.Layer{
		PluginDirs: []string{"/proj/plugins"},
	}); err != nil {
		t.Fatal(err)
	}

	// Trusted in-repo layer.
	repoTOML := []byte(`skills_dirs = ["sub"]
context_strategy = "ooda"
`)
	repoPath := filepath.Join(cwd, ".evener", "launch.toml")
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repoPath, repoTOML, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := launchconfig.CanonicalHashTOML(repoTOML)
	if err != nil {
		t.Fatal(err)
	}
	if err := launchconfig.SaveMeta(filepath.Join(stateRoot, "projects", pid, "meta.toml"), launchconfig.Meta{
		Schema: 1, CWD: cwd,
		Trust: launchconfig.MetaTrust{Hash: hash, Decision: "trusted"},
	}); err != nil {
		t.Fatal(err)
	}

	// Per-launch overrides.
	overrides := launchconfig.Layer{ReasoningEffort: "low"}

	resolved, err := launchconfig.Resolve(stateRoot, cwd, overrides)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.Effective.Model != "openai/gpt-5-mini-2025-08-07" {
		t.Errorf("Model = %q", resolved.Effective.Model)
	}
	if got := resolved.Effective.SkillsDirs; len(got) != 2 || got[0] != "/global/skills" || got[1] != filepath.Join(cwd, "sub") {
		t.Errorf("SkillsDirs = %v", got)
	}
	if got := resolved.Effective.PluginDirs; len(got) != 1 || got[0] != "/proj/plugins" {
		t.Errorf("PluginDirs = %v", got)
	}
	if resolved.Effective.ContextStrategy != "ooda" {
		t.Errorf("ContextStrategy = %q", resolved.Effective.ContextStrategy)
	}
	if resolved.Effective.ReasoningEffort != "low" {
		t.Errorf("ReasoningEffort = %q", resolved.Effective.ReasoningEffort)
	}
	args := launchconfig.ToArgs(resolved)
	wantHas := []string{"--model", "openai/gpt-5-mini-2025-08-07", "--context-strategy", "ooda", "--reasoning-effort", "low", "--max-rounds", "50"}
	for _, w := range wantHas {
		found := false
		for _, a := range args {
			if a == w {
				found = true
			}
		}
		if !found {
			t.Errorf("args missing %q in %v", w, args)
		}
	}
}
