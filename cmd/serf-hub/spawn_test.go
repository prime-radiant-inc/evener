package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/rendezvous"
)

func TestBuildSpawnArgs(t *testing.T) {
	req := SpawnRequest{
		Model:           "openai/gpt-5.2",
		Agent:           "default",
		WorkingDir:      "/Users/jesse/git/foo",
		StateDir:        "/Users/jesse/.local/state/serf/projects/foo",
		RunDir:          "/Users/jesse/.cache/serf/run",
		ReasoningEffort: "medium",
		SSERingSize:     4096,
	}
	args := buildSpawnArgs(req)
	want := map[string]string{
		"--model":            "openai/gpt-5.2",
		"--agent":            "default",
		"--reasoning-effort": "medium",
		"--sse-ring-size":    "4096",
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
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := SpawnDaemon(context.Background(), bin, filepath.Join(dir, "run"), SpawnRequest{Model: "openai/gpt-5.2"}, 10*time.Second)
	if err == nil {
		t.Fatal("expected spawn error")
	}
	if time.Since(start) > 8*time.Second {
		t.Fatalf("spawn waited for timeout instead of process exit: %v", time.Since(start))
	}
	if !strings.Contains(err.Error(), "exited before rendezvous") {
		t.Fatalf("error=%v", err)
	}
}

func TestBuildSerfChildEnvUsesExplicitConfigBeforeInheritedEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "parent-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-parent-secret")
	cfg := DefaultConfig()
	cfg.SerfLaunch.Env = map[string]string{
		"OPENROUTER_API_KEY": "configured-secret",
	}

	env := buildSerfChildEnv(cfg, "/tmp/run", "/tmp/state", "generated-token")
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

func TestBuildSerfChildEnvGeneratedHubTokenOverridesConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SerfLaunch.Env = map[string]string{
		"SERF_HUB_TOKEN": "configured-token",
	}

	got := envMap(buildSerfChildEnv(cfg, "/tmp/run", "/tmp/state", "generated-token"))

	if got["SERF_HUB_TOKEN"] != "generated-token" {
		t.Fatalf("SERF_HUB_TOKEN=%q, want generated-token", got["SERF_HUB_TOKEN"])
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
		Model:      "ollama/test",
		WorkingDir: dir,
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

func TestProviderCredentialPreflightRequiresOpenRouterKey(t *testing.T) {
	err := validateProviderCredentials("openrouter/free", envFromMap(map[string]string{}), "")
	assertHubLaunchError(t, err)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked secret-like value: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsConfiguredOpenRouterKey(t *testing.T) {
	err := validateProviderCredentials("openrouter/free", envFromMap(map[string]string{
		"OPENROUTER_API_KEY": "configured-secret",
	}), "")
	if err != nil {
		t.Fatalf("validateProviderCredentials: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsInheritedGoogleAlias(t *testing.T) {
	err := validateProviderCredentials("google/gemini-3-flash-preview", envFromMap(map[string]string{
		"GOOGLE_API_KEY": "google-secret",
	}), "")
	if err != nil {
		t.Fatalf("validateProviderCredentials: %v", err)
	}
}

func TestProviderCredentialPreflightAcceptsStoredOpenAIAuth(t *testing.T) {
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "stored-access-token",
		RefreshToken: "stored-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	err := validateProviderCredentials("openai/gpt-5", envFromMap(map[string]string{}), stateDir)
	if err != nil {
		t.Fatalf("validateProviderCredentials: %v", err)
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
