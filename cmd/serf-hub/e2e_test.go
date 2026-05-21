package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/launchconfig"
)

// TestE2E_HubAndDaemon brings up a real serf daemon plus the hub and
// verifies the landing page lists the daemon. Skip-by-default; runs only
// when SERF_TEST_PROVIDER, SERF_TEST_MODEL, and an API key are set.
func TestE2E_HubAndDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	provider := os.Getenv("SERF_TEST_PROVIDER")
	model := os.Getenv("SERF_TEST_MODEL")
	if provider == "" || model == "" {
		t.Skip("SERF_TEST_PROVIDER and SERF_TEST_MODEL required")
	}
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("no LLM API key in env")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// Build serf and serf-hub.
	for _, target := range []string{"./cmd/serf", "./cmd/serf-hub"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(tmpHome, filepath.Base(target)), target)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", target, err, out)
		}
	}
	serfBin := filepath.Join(tmpHome, "serf")
	hubBin := filepath.Join(tmpHome, "serf-hub")

	// Launch a serf serve daemon.
	dCmd := exec.Command(serfBin, "serve",
		"--model", provider+"/"+model,
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
	)
	dCmd.Env = append(os.Environ(), "HOME="+tmpHome)
	dCmd.Stderr = os.Stderr
	if err := dCmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer func() {
		_ = dCmd.Process.Kill()
	}()

	// Launch the hub on a fixed port.
	hubAddr := "127.0.0.1:9181"
	hCmd := exec.Command(hubBin, "--addr", hubAddr, "--serf", serfBin)
	hCmd.Env = append(os.Environ(), "HOME="+tmpHome)
	hCmd.Stderr = os.Stderr
	if err := hCmd.Start(); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer func() {
		_ = hCmd.Process.Kill()
	}()

	// Wait for hub to be reachable.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get("http://" + hubAddr + "/"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Wait up to 5s for the roster to find the daemon.
	deadline = time.Now().Add(5 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + hubAddr + "/live")
		if err == nil {
			b := make([]byte, 16384)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
			resp.Body.Close()
			if strings.Contains(body, "no live daemons") {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if strings.Contains(body, "row") {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("hub roster did not pick up the daemon. last body: %q", body)
}

func TestE2E_LayeredLaunchConfig(t *testing.T) {
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
	paths := launchconfig.PathsFor(stateRoot, cwd)
	pid := launchconfig.ProjectID(cwd)
	if err := launchconfig.SaveLayer(paths.Project, launchconfig.Layer{
		PluginDirs: []string{"/proj/plugins"},
	}); err != nil {
		t.Fatal(err)
	}

	// Trusted in-repo layer.
	repoTOML := []byte(`skills_dirs = ["sub"]
context_strategy = "ooda"
`)
	repoPath := filepath.Join(cwd, ".serf", "launch.toml")
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
