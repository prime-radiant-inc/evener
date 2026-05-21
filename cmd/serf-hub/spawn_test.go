package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/internal/launchconfig"
	"primeradiant.com/serf/rendezvous"
)

func TestBuildSpawnArgs(t *testing.T) {
	ssering := 4096
	req := SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model:           "openai/gpt-5.2",
			FastCheapModel:  "openai/gpt-5-mini",
			Agent:           "default",
			ReasoningEffort: "medium",
			AppReplaySize:   &ssering,
		}},
		WorkingDir: "/Users/jesse/git/foo",
		StateDir:   "/Users/jesse/.local/state/serf/projects/foo",
		RunDir:     "/Users/jesse/.cache/serf/run",
	}
	args := buildSpawnArgs(req)
	want := map[string]string{
		"--model":            "openai/gpt-5.2",
		"--fast-cheap-model": "openai/gpt-5-mini",
		"--agent":            "default",
		"--reasoning-effort": "medium",
		"--app-replay-size":  "4096",
		"--dir":              "/Users/jesse/git/foo",
		"--state-dir":        "/Users/jesse/.local/state/serf/projects/foo",
		"--run-dir":          "/Users/jesse/.cache/serf/run",
		"--addr":             "127.0.0.1:0",
	}
	got := pairsToMap(args)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("arg %s: got %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["--provider"]; ok {
		t.Fatal("spawn args must not pass --provider; provider belongs in --model provider/model")
	}
}

// pairsToMap collapses ["--k", "v", ...] to {"--k": "v"} for assertions.
func pairsToMap(args []string) map[string]string {
	out := make(map[string]string)
	for i := 0; i+1 < len(args); i += 2 {
		out[args[i]] = args[i+1]
	}
	return out
}

func TestBuildSpawnArgs_FromResolved(t *testing.T) {
	r := launchconfig.Resolved{Effective: launchconfig.Layer{
		Model:      "openai/gpt-5",
		Agent:      "default",
		SkillsDirs: []string{"/sk"},
	}}
	req := SpawnRequest{Resolved: r, WorkingDir: "/wd", StateDir: "/st", RunDir: "/rn"}
	got := buildSpawnArgs(req)
	wantHas := []string{"--addr", "127.0.0.1:0", "--dir", "/wd", "--state-dir", "/st", "--run-dir", "/rn", "--model", "openai/gpt-5", "--agent", "default", "--skills-dir", "/sk"}
	for _, w := range wantHas {
		found := false
		for _, a := range got {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("buildSpawnArgs missing %q in %v", w, got)
		}
	}
}

func TestPrepareResolvedForSpawnMaterializesInlinePrompts(t *testing.T) {
	stateDir := t.TempDir()
	const systemPrompt = "base inline prompt body"
	const appendPrompt = "append inline prompt body"
	resolved := launchconfig.Resolved{Effective: launchconfig.Layer{
		SystemPromptMode:       "inline",
		SystemPromptText:       systemPrompt,
		SystemPromptAppendMode: "inline",
		SystemPromptAppendText: appendPrompt,
	}}

	got, cleanup, err := prepareResolvedForSpawn(stateDir, resolved)
	if err != nil {
		t.Fatalf("prepareResolvedForSpawn: %v", err)
	}

	if got.Effective.SystemPromptMode != "file" {
		t.Fatalf("SystemPromptMode=%q, want file", got.Effective.SystemPromptMode)
	}
	if got.Effective.SystemPromptAppendMode != "file" {
		t.Fatalf("SystemPromptAppendMode=%q, want file", got.Effective.SystemPromptAppendMode)
	}
	if got.Effective.SystemPromptText != "" {
		t.Fatalf("SystemPromptText=%q, want cleared", got.Effective.SystemPromptText)
	}
	if got.Effective.SystemPromptAppendText != "" {
		t.Fatalf("SystemPromptAppendText=%q, want cleared", got.Effective.SystemPromptAppendText)
	}
	assertPromptFile(t, got.Effective.SystemPromptFile, stateDir, systemPrompt)
	assertPromptFile(t, got.Effective.SystemPromptAppendFile, stateDir, appendPrompt)
	cleanup()
	assertPromptFile(t, got.Effective.SystemPromptFile, stateDir, systemPrompt)
	assertPromptFile(t, got.Effective.SystemPromptAppendFile, stateDir, appendPrompt)
}

func TestBuildSpawnArgsPreparedInlinePromptsUseFilesWithoutLeakingText(t *testing.T) {
	stateDir := t.TempDir()
	const systemPrompt = "do not leak base prompt"
	const appendPrompt = "do not leak append prompt"
	resolved, cleanup, err := prepareResolvedForSpawn(stateDir, launchconfig.Resolved{Effective: launchconfig.Layer{
		Model:                  "openai/gpt-5",
		SystemPromptMode:       "inline",
		SystemPromptText:       systemPrompt,
		SystemPromptAppendMode: "inline",
		SystemPromptAppendText: appendPrompt,
	}})
	if err != nil {
		t.Fatalf("prepareResolvedForSpawn: %v", err)
	}
	cleanup()

	args := buildSpawnArgs(SpawnRequest{Resolved: resolved, StateDir: stateDir})
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, systemPrompt) || strings.Contains(joined, appendPrompt) {
		t.Fatalf("buildSpawnArgs leaked inline prompt body text: %#v", args)
	}
	got := pairsToMap(args)
	if got["--system-prompt"] == "" {
		t.Fatalf("missing --system-prompt in %#v", args)
	}
	if got["--system-prompt-append"] == "" {
		t.Fatalf("missing --system-prompt-append in %#v", args)
	}
	assertPromptFile(t, got["--system-prompt"], stateDir, systemPrompt)
	assertPromptFile(t, got["--system-prompt-append"], stateDir, appendPrompt)
}

func assertPromptFile(t *testing.T, path, stateDir, want string) {
	t.Helper()
	if path == "" {
		t.Fatal("prompt path is empty")
	}
	rel, err := filepath.Rel(stateDir, path)
	if err != nil {
		t.Fatalf("prompt path %q is not relative to state dir %q: %v", path, stateDir, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		t.Fatalf("prompt path %q is not under state dir %q", path, stateDir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prompt file %q: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("prompt file %q = %q, want %q", path, data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat prompt file %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("prompt file mode=%#o, want 0600", got)
	}
}

func TestWaitForRendezvous_AppearsInTime(t *testing.T) {
	dir := t.TempDir()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = rendezvous.Write(dir, rendezvous.Entry{
			PID:     12345,
			Address: "127.0.0.1:50000",
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := WaitForRendezvous(ctx, dir, 12345)
	if err != nil {
		t.Fatalf("WaitForRendezvous: %v", err)
	}
	if got.Address != "127.0.0.1:50000" {
		t.Errorf("Address: %q", got.Address)
	}
}

func TestWaitForRendezvous_TimesOut(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := WaitForRendezvous(ctx, dir, 99999)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForRendezvous_WrongPID(t *testing.T) {
	dir := t.TempDir()
	_, _ = rendezvous.Write(dir, rendezvous.Entry{PID: 11111, Address: "x"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := WaitForRendezvous(ctx, dir, 22222); err == nil {
		t.Fatal("expected timeout for wrong PID")
	}

}

// A stale rendezvous file from a dead process whose PID was reused must not
// match our newly-spawned daemon. WaitForRendezvous filters by startedAfter.
func TestWaitForRendezvous_IgnoresStaleEntryFromBeforeStart(t *testing.T) {
	dir := t.TempDir()

	// Stale entry: same PID, but written before our spawn time.
	_, _ = rendezvous.Write(dir, rendezvous.Entry{
		PID:       55555,
		Address:   "127.0.0.1:11111",
		StartedAt: time.Now().Add(-1 * time.Hour),
	})

	startedAfter := time.Now()

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = rendezvous.Write(dir, rendezvous.Entry{
			PID:       55555,
			Address:   "127.0.0.1:22222",
			StartedAt: time.Now(),
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := WaitForRendezvous(ctx, dir, 55555, WithStartedAfter(startedAfter))
	if err != nil {
		t.Fatalf("WaitForRendezvous: %v", err)
	}
	if got.Address != "127.0.0.1:22222" {
		t.Errorf("matched stale entry: address=%q", got.Address)
	}
}

func TestSpawnDaemonReturnsWhenProcessExitsBeforeRendezvous(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-serf")
	script := `#!/bin/sh
echo 'serf serve: session creation: plugin initialization: resolving plugin dir "/Users/jesse/git/superpowers/superpowers": lstat /Users: no such file or directory' >&2
exit 42
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := SpawnDaemon(context.Background(), bin, filepath.Join(dir, "run"), SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5.2"}},
	}, 10*time.Second)
	if err == nil {
		t.Fatal("expected spawn error")
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("spawn waited for timeout instead of process exit: %v", time.Since(start))
	}
	if !strings.Contains(err.Error(), "exited before rendezvous") {
		t.Fatalf("error=%v", err)
	}
	if !strings.Contains(err.Error(), "plugin initialization: resolving plugin dir") {
		t.Fatalf("spawn error did not include daemon stderr: %v", err)
	}
}

func TestToEnvUsesExplicitConfigBeforeInheritedEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "parent-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-parent-secret")

	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Env: map[string]string{"OPENROUTER_API_KEY": "configured-secret"},
		}},
		RunDir:    "/tmp/run",
		StateDir:  "/tmp/state",
		HubToken:  "generated-token",
		ParentEnv: os.Environ(),
	})
	got := envMap(env)

	if got["SERF_HUB_SPAWNED"] != "1" {
		t.Fatalf("SERF_HUB_SPAWNED=%q, want 1", got["SERF_HUB_SPAWNED"])
	}
	if got["SERF_RUN_DIR"] != "/tmp/run" {
		t.Fatalf("SERF_RUN_DIR=%q, want /tmp/run", got["SERF_RUN_DIR"])
	}
	if got["SERF_STATE_DIR"] != "/tmp/state" {
		t.Fatalf("SERF_STATE_DIR=%q, want /tmp/state", got["SERF_STATE_DIR"])
	}
	if got["SERF_HUB_TOKEN"] != "generated-token" {
		t.Fatalf("SERF_HUB_TOKEN=%q, want generated-token", got["SERF_HUB_TOKEN"])
	}
	if got["OPENROUTER_API_KEY"] != "configured-secret" {
		t.Fatalf("OPENROUTER_API_KEY=%q, want configured-secret", got["OPENROUTER_API_KEY"])
	}
	if got["ANTHROPIC_API_KEY"] != "anthropic-parent-secret" {
		t.Fatalf("ANTHROPIC_API_KEY was not inherited")
	}
}

func TestToEnvPerLaunchEnvWinsOverHubToken(t *testing.T) {
	// Per-launch Env (Effective.Env) is applied last in ToEnv, so it wins
	// over the HubToken. In practice, Spawn never puts SERF_HUB_TOKEN in
	// Effective.Env, so the generated token always reaches the child.
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Env: map[string]string{"SERF_HUB_TOKEN": "per-launch-override"},
		}},
		RunDir:    "/tmp/run",
		StateDir:  "/tmp/state",
		HubToken:  "generated-token",
		ParentEnv: os.Environ(),
	})
	got := envMap(env)

	if got["SERF_HUB_TOKEN"] != "per-launch-override" {
		t.Fatalf("SERF_HUB_TOKEN=%q, want per-launch-override (per-launch env wins)", got["SERF_HUB_TOKEN"])
	}
}

func TestHubSpawnerSpawnPassesHubTokenToDaemon(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	tokenOut := filepath.Join(dir, "token.txt")
	t.Setenv("TOKEN_OUT", tokenOut)
	bin := filepath.Join(dir, "fake-serf")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"serf-appwire-v1"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s' "$SERF_HUB_TOKEN" > "$TOKEN_OUT"
  mkdir -p "$SERF_RUN_DIR"
  cat > "$SERF_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","hub_token":"$SERF_HUB_TOKEN","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.SpawnTimeout = 2 * time.Second
	spawner := HubSpawner{Cfg: cfg, SerfBinary: bin, RunDir: runDir, HubToken: "generated-token"}

	entry, err := spawner.Spawn(context.Background(), SpawnRequest{
		Resolved:   launchconfig.Resolved{Effective: launchconfig.Layer{Model: "ollama/test"}},
		WorkingDir: dir,
		Provider:   "ollama",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if entry.HubToken != "generated-token" {
		t.Fatalf("rendezvous hub token=%q, want generated-token", entry.HubToken)
	}
	data, err := os.ReadFile(tokenOut)
	if err != nil {
		t.Fatalf("read token output: %v", err)
	}
	if string(data) != "generated-token" {
		t.Fatalf("child SERF_HUB_TOKEN=%q, want generated-token", data)
	}
}

func TestHubSpawnerSpawnUsesConfiguredXDGStateHomeForStateDir(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	workDir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateHome := filepath.Join(dir, "xdg-state")
	argsOut := filepath.Join(dir, "args.txt")
	envOut := filepath.Join(dir, "env.txt")
	t.Setenv("ARGS_OUT", argsOut)
	t.Setenv("ENV_OUT", envOut)
	bin := filepath.Join(dir, "fake-serf")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"serf-appwire-v1"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s\n' "$@" > "$ARGS_OUT"
  env > "$ENV_OUT"
  mkdir -p "$SERF_RUN_DIR"
  cat > "$SERF_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.SpawnTimeout = 2 * time.Second
	cfg.StateGlob = filepath.Join(stateHome, "serf", "projects", "*")
	spawner := HubSpawner{Cfg: cfg, SerfBinary: bin, RunDir: runDir, HubToken: "generated-token"}

	if _, err := spawner.Spawn(context.Background(), SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model: "ollama/test",
			Env:   map[string]string{"XDG_STATE_HOME": stateHome},
		}},
		WorkingDir: workDir,
		Provider:   "ollama",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	argsData, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	stateDir := argValue(strings.Fields(string(argsData)), "--state-dir")
	wantPrefix := filepath.Join(stateHome, "serf", "projects") + string(os.PathSeparator)
	if !strings.HasPrefix(stateDir, wantPrefix) {
		t.Fatalf("--state-dir=%q, want under %q\nargs:\n%s", stateDir, wantPrefix, argsData)
	}
	envData, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	env := envMap(strings.Split(strings.TrimSpace(string(envData)), "\n"))
	if env["XDG_STATE_HOME"] != stateHome {
		t.Fatalf("child XDG_STATE_HOME=%q, want %q", env["XDG_STATE_HOME"], stateHome)
	}
	if env["SERF_STATE_DIR"] != stateDir {
		t.Fatalf("child SERF_STATE_DIR=%q, want %q", env["SERF_STATE_DIR"], stateDir)
	}
}

func argValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestHubSpawnerListsModelsFromSerfLaunchContract(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "fake-serf")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"serf-appwire-v1","models":[{"provider":"openai","model":"gpt-5.5"}]}\n'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	spawner := HubSpawner{Cfg: DefaultConfig(), SerfBinary: bin, RunDir: runDir, HubToken: "generated-token"}

	models, err := spawner.ListLaunchModels(context.Background())
	if err != nil {
		t.Fatalf("ListLaunchModels: %v", err)
	}
	if len(models) != 1 || models[0].Provider != "openai" || models[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", models)
	}
}

func TestProviderCredentialPreflightRequiresOpenRouterKey(t *testing.T) {
	// Empty store — no key in file or env.
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	t.Setenv("OPENROUTER_API_KEY", "") // ensure env is empty
	err := validateProviderCredentials("openrouter", store, nil)
	assertHubLaunchError(t, err)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked secret-like value: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsConfiguredOpenRouterKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "configured-secret")
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("openrouter", store, nil)
	if err != nil {
		t.Fatalf("validateProviderCredentials: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsLaunchEnvKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("openrouter", store, []string{"OPENROUTER_API_KEY=launch-secret"})
	if err != nil {
		t.Fatalf("validateProviderCredentials with launch env key: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsOpenAICompatibleBaseURLOnly(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "")
	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", "")
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("openai-compatible", store, []string{"OPENAI_COMPATIBLE_BASE_URL=http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatalf("validateProviderCredentials with base-url-only openai-compatible env: %v", err)
	}
}

func TestProviderCredentialPreflightRejectsLaunchEnvClearedStoreKey(t *testing.T) {
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	if err := store.Set("openrouter", "stored-secret"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	err := validateProviderCredentials("openrouter", store, []string{"OPENROUTER_API_KEY="})
	assertHubLaunchError(t, err)
}

func TestProviderCredentialPreflightAcceptsStoredOpenAIOAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	xdgStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	t.Setenv("OPENAI_API_KEY", "")
	stateDir := authopenai.DefaultStateDirWithStateHome(xdgStateHome)
	if err := authopenai.SaveAuth(stateDir, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-access-token",
		RefreshToken: "oauth-refresh-token",
		Expiry:       time.Now().Add(-time.Minute),
		Email:        "oauth@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("openai", store, []string{"XDG_STATE_HOME=" + xdgStateHome})
	if err != nil {
		t.Fatalf("validateProviderCredentials(openai) with refreshable stored OAuth: %v", err)
	}
}

func TestProviderCredentialPreflightUsesLaunchHomeForOpenAIOAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	home := t.TempDir()
	stateDir := filepath.Join(home, ".local", "state", "serf")
	if err := authopenai.SaveAuth(stateDir, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-access-token",
		RefreshToken: "oauth-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "oauth@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("openai", store, []string{"XDG_STATE_HOME=", "HOME=" + home})
	if err != nil {
		t.Fatalf("validateProviderCredentials(openai) with HOME-scoped OAuth: %v", err)
	}
}

func TestProviderCredentialPreflightDoesNotUseHubEnvWhenLaunchClearsXDG(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	hubStateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", hubStateHome)
	if err := authopenai.SaveAuth(authopenai.DefaultStateDirWithStateHome(hubStateHome), authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "hub-oauth-access-token",
		RefreshToken: "hub-oauth-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "hub-oauth@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("openai", store, []string{"XDG_STATE_HOME=", "HOME=" + t.TempDir()})
	assertHubLaunchError(t, err)
}

func TestProviderCredentialPreflightAcceptsInheritedGoogleAlias(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "google-secret")
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("google", store, nil)
	if err != nil {
		t.Fatalf("validateProviderCredentials: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsOllama(t *testing.T) {
	store, _ := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	err := validateProviderCredentials("ollama", store, nil)
	if err != nil {
		t.Fatalf("validateProviderCredentials for ollama: %v", err)
	}
}

func TestValidateSerfLaunchContractRejectsUnsupportedProvider(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-serf")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'unknown provider: openrouter' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := validateSerfLaunchContract(context.Background(), bin, "openrouter/free", envFromMap(map[string]string{}))
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "serf launch-check") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateSerfLaunchContractMissingBinaryReturnsStructuredDiagnostic(t *testing.T) {
	err := validateSerfLaunchContract(context.Background(), filepath.Join(t.TempDir(), "missing-serf"), "openai/gpt-5", envFromMap(map[string]string{}))
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "serf launch-check failed") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateSerfLaunchContractRedactsSecretsFromDiagnostics(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-serf")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$OPENROUTER_API_KEY\" >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := validateSerfLaunchContract(context.Background(), bin, "openrouter/free", envFromMap(map[string]string{
		"OPENROUTER_API_KEY": "super-secret-key",
	}))
	assertHubLaunchError(t, err)
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("diagnostic leaked secret: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("diagnostic did not redact secret: %v", err)
	}
}

func TestRedactEnvSecretsKeepsShortSensitiveValues(t *testing.T) {
	got := redactEnvSecrets("serf launch-check exited with code 1", envFromMap(map[string]string{
		"SERF_HUB_TOKEN": "1",
	}))
	if got != "serf launch-check exited with code 1" {
		t.Fatalf("diagnostic=%q", got)
	}
}

func TestResolveSerfStateDirMatchesServeDefaultForWorkingDir(t *testing.T) {
	dir := t.TempDir()
	got := resolveSerfStateDir(dir, "")
	if got == "" {
		t.Fatal("state dir is empty")
	}
	if !strings.Contains(got, "serf") {
		t.Fatalf("state dir=%q, want serf runtime path", got)
	}
}

func envFromMap(values map[string]string) []string {
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	return env
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}
