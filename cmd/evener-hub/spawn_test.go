package hub

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

// writeFakeEvener writes an executable fake-evener stub that the test then runs
// through the hub spawner / launch-check.
//
// A freshly written executable can fail execve with ETXTBSY ("text file busy"):
// os.WriteFile holds the file open for writing, and if any sibling parallel test
// forks for its own os/exec during that window, the forked child inherits the
// still-open write fd. Until that child execs, the kernel refuses to execute
// this file. syscall.ForkLock is the standard guard: fork/exec takes it for
// writing (forkExec -> acquireForkLock), so holding it for reading across the
// whole write excludes any concurrent fork; once the fd is closed no child can
// inherit it. This keeps these tests parallel while removing the race at its
// source. See Go issue #22315.
func writeFakeEvener(t *testing.T, path, script string) {
	t.Helper()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake-evener: %v", err)
	}
}

func TestBuildSpawnArgs(t *testing.T) {
	ssering := 4096
	req := hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model:           "openai/gpt-5.2",
			FastCheapModel:  "openai/gpt-5-mini",
			Agent:           "default",
			ReasoningEffort: "medium",
			AppReplaySize:   &ssering,
		}},
		WorkingDir: "/Users/jesse/git/foo",
		StateDir:   "/Users/jesse/.local/state/evener/projects/foo",
		RunDir:     "/Users/jesse/.cache/evener/run",
	}
	args := buildSpawnArgs(req)
	want := map[string]string{
		"--model":            "openai/gpt-5.2",
		"--fast-cheap-model": "openai/gpt-5-mini",
		"--agent":            "default",
		"--reasoning-effort": "medium",
		"--app-replay-size":  "4096",
		"--dir":              "/Users/jesse/git/foo",
		"--state-dir":        "/Users/jesse/.local/state/evener/projects/foo",
		"--run-dir":          "/Users/jesse/.cache/evener/run",
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
	req := hubcore.SpawnRequest{Resolved: r, WorkingDir: "/wd", StateDir: "/st", RunDir: "/rn"}
	got := buildSpawnArgs(req)
	m := pairsToMap(got)
	wantPairs := map[string]string{
		"--addr":       "127.0.0.1:0",
		"--dir":        "/wd",
		"--state-dir":  "/st",
		"--run-dir":    "/rn",
		"--model":      "openai/gpt-5",
		"--agent":      "default",
		"--skills-dir": "/sk",
	}
	for k, v := range wantPairs {
		if m[k] != v {
			t.Errorf("arg %s: got %q, want %q", k, m[k], v)
		}
	}
}

func TestBuildResumeArgsOmitAmbientModelKnobs(t *testing.T) {
	maxRounds := 50
	req := hubcore.ResumeRequest{
		SessionID:  "01JRESUME",
		WorkingDir: "/wd",
		StateDir:   "/st",
		RunDir:     "/rn",
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model:           "openai/gpt-env",
			FastCheapModel:  "openai/gpt-4.1-nano",
			ModelFallbacks:  &[]string{"openai/gpt-fallback"},
			Agent:           "default",
			ReasoningEffort: "medium",
			MaxRounds:       &maxRounds,
		}},
	}
	args := buildResumeArgs(req)
	for _, forbidden := range []string{"--model", "--fast-cheap-model", "--model-fallback"} {
		if hasArg(args, forbidden) {
			t.Fatalf("resume args must not include ambient %s: %v", forbidden, args)
		}
	}
	for _, required := range []string{"serve", "--resume", "01JRESUME", "--agent", "default", "--reasoning-effort", "medium", "--max-rounds", "50"} {
		if !hasArg(args, required) {
			t.Fatalf("resume args missing %q: %v", required, args)
		}
	}
}

func TestHubSpawnerResumeLaunchCheckOmitsAmbientModel(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	argsOut := filepath.Join(dir, "launch-check-args.txt")
	t.Setenv("ARGS_OUT", argsOut)
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '%s\n' "$@" > "$ARGS_OUT"
  printf '{"protocol":"evener-appwire-v3"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	cfg := DefaultConfig()
	cfg.SpawnTimeout = 2 * time.Second
	spawner := HubSpawner{Cfg: cfg, EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}
	_, err := spawner.Resume(context.Background(), hubcore.ResumeRequest{
		SessionID: "01JRESUME",
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model:          "openrouter/stale-model",
			FastCheapModel: "openrouter/stale-cheap",
		}},
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	data, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("read launch-check args: %v", err)
	}
	args := strings.Fields(string(data))
	if hasArg(args, "--model") {
		t.Fatalf("resume launch-check must not pass ambient --model: %v", args)
	}
}

func TestBuildSpawnArgsNonInteractiveOnlyWhenRequested(t *testing.T) {
	interactive := buildSpawnArgs(hubcore.SpawnRequest{Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5"}}})
	if hasArg(interactive, "--non-interactive") {
		t.Fatalf("interactive spawn args unexpectedly included --non-interactive: %v", interactive)
	}
	enabled := true
	nonInteractive := buildSpawnArgs(hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5", NonInteractive: &enabled}},
	})
	if !hasArg(nonInteractive, "--non-interactive") {
		t.Fatalf("non-interactive spawn args missing --non-interactive: %v", nonInteractive)
	}
	disabled := false
	explicitInteractive := buildSpawnArgs(hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5", NonInteractive: &disabled}},
	})
	if hasArg(explicitInteractive, "--non-interactive") {
		t.Fatalf("explicit interactive spawn args unexpectedly included --non-interactive: %v", explicitInteractive)
	}
}

func hasArg(args []string, want string) bool {
	return slices.Contains(args, want)
}

func TestHubSpawnerHasNoAmbientLaunchDefaults(t *testing.T) {
	if _, ok := reflect.TypeFor[HubSpawner]().FieldByName("LaunchDefaults"); ok {
		t.Fatal("HubSpawner must not keep ambient launch defaults; plugin dirs should come only from launch config")
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

	args := buildSpawnArgs(hubcore.SpawnRequest{Resolved: resolved, StateDir: stateDir})
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

// waitForRendezvous drives the launch wait with no child process to watch: a
// nil exited channel never fires in the loop's select, so the rendezvous file
// and the context are all that decide the outcome.
//
// That is precisely what the deleted exported WaitForRendezvous was — the same
// loop minus the exited arm — and it had already drifted from the live one
// twice: 0c3g had to patch the identical <-ctx.Done() arm in both copies just
// to keep the sentinel meaning one thing, and the copy still polled
// rendezvous.List while the live wait polled the listRendezvousForWait seam.
// Nothing outside these tests could ever have called it; it lived in package
// main, so no other package could import it (kata waf1).
//
// The tests below are the only place the wait's matching rules — PID, the
// startedAfter staleness filter, and which sentinel a done context yields — are
// asserted at all. Pointing them at the live loop puts that coverage on the
// code that ships instead of on a copy of it.
func waitForRendezvous(ctx context.Context, runDir string, pid int, opts ...WaitOption) (rendezvous.Entry, error) {
	return waitForRendezvousOrExit(ctx, runDir, pid, nil, opts...)
}

func TestWaitForRendezvous_AppearsInTime(t *testing.T) {
	dir := t.TempDir()

	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = rendezvous.Write(dir, rendezvous.Entry{
			PID:     12345,
			Address: "127.0.0.1:50000",
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := waitForRendezvous(ctx, dir, 12345)
	if err != nil {
		t.Fatalf("waitForRendezvous: %v", err)
	}
	if got.Address != "127.0.0.1:50000" {
		t.Errorf("Address: %q", got.Address)
	}
}

func TestWaitForRendezvous_TimesOut(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := waitForRendezvous(ctx, dir, 99999)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// A caller that gave up is not the wait running out of time, and a caller
// classifying by sentinel must not be told otherwise (kata 0c3g). This is the
// only place that distinction is asserted at the sentinel rather than through a
// launch failure's rendered message.
func TestWaitForRendezvous_AbandonedCallerIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForRendezvous(ctx, t.TempDir(), 99999)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, errRendezvousTimeout) {
		t.Fatalf("abandoned wait reported as a timeout: %v", err)
	}
	if !errors.Is(err, errRendezvousCanceled) {
		t.Fatalf("err = %v, want errRendezvousCanceled", err)
	}
}

func TestWaitForRendezvous_WrongPID(t *testing.T) {
	dir := t.TempDir()
	_, _ = rendezvous.Write(dir, rendezvous.Entry{PID: 11111, Address: "x"})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := waitForRendezvous(ctx, dir, 22222); err == nil {
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
		time.Sleep(10 * time.Millisecond)
		_, _ = rendezvous.Write(dir, rendezvous.Entry{
			PID:       55555,
			Address:   "127.0.0.1:22222",
			StartedAt: time.Now(),
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := waitForRendezvous(ctx, dir, 55555, WithStartedAfter(startedAfter))
	if err != nil {
		t.Fatalf("waitForRendezvous: %v", err)
	}
	if got.Address != "127.0.0.1:22222" {
		t.Errorf("matched stale entry: address=%q", got.Address)
	}
}

func TestSpawnDaemonReturnsWhenProcessExitsBeforeRendezvous(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
echo 'evener serve: session creation: plugin initialization: resolving plugin dir "/Users/jesse/git/superpowers/superpowers": lstat /Users: no such file or directory' >&2
exit 42
`
	writeFakeEvener(t, bin, script)
	_, err := SpawnDaemon(context.Background(), bin, filepath.Join(dir, "run"), hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5.2"}},
	}, 60*time.Second)
	if err == nil {
		t.Fatal("expected spawn error")
	}
	if !strings.Contains(err.Error(), "exited before rendezvous") {
		t.Fatalf("error=%v", err)
	}
	if !strings.Contains(err.Error(), "plugin initialization: resolving plugin dir") {
		t.Fatalf("spawn error did not include daemon stderr: %v", err)
	}
}

// A hub launch fails for three different reasons and the message must name
// which: the child died first, the caller walked away, or the wait genuinely
// ran out of time. A daemon that fails validation and exits in milliseconds is
// not a timeout, and neither is a browser that navigates away mid-launch —
// saying either is a timeout sends an operator triaging it after a slow
// machine, a hung provider, or a too-short SpawnTimeout, none of which are
// involved. This is the first line surfaced for every hub launch failure,
// spawn and resume alike (katas 42ck, 0c3g).
//
// All three outcomes must stay inside diagnostic.HubFailureKeywords, the
// vocabulary the hub and its web client each classify these messages against
// to offer "Reconnect & retry" rather than a log to go read. The label is what
// changes; which family of failure this is does not.
func TestDaemonLaunchFailureNamesWhatActuallyHappened(t *testing.T) {
	t.Parallel()
	const exitsImmediately = `#!/bin/sh
echo 'evener serve: session sess_old is already running; send work to the live session or fork it' >&2
exit 1
`
	const neverRegisters = `#!/bin/sh
sleep 30
`
	launches := []struct {
		name string
		// The action every one of its failures must open by naming, so an
		// operator reading a bare message knows which launch it came from.
		action string
		call   func(ctx context.Context, evenerBinary, runDir string, timeout time.Duration) error
	}{
		{
			name:   "spawn",
			action: "daemon spawn",
			call: func(ctx context.Context, evenerBinary, runDir string, timeout time.Duration) error {
				_, err := SpawnDaemon(ctx, evenerBinary, runDir, hubcore.SpawnRequest{}, timeout)
				return err
			},
		},
		{
			name:   "resume",
			action: "resume",
			call: func(ctx context.Context, evenerBinary, runDir string, timeout time.Duration) error {
				_, err := ResumeDaemon(ctx, evenerBinary, runDir, hubcore.ResumeRequest{SessionID: "sess_old"}, timeout)
				return err
			},
		},
	}
	outcomes := []struct {
		name   string
		script string
		// The word that follows the action, naming what stopped this launch.
		label   string
		timeout time.Duration
		// The context the hub hands the launch. Nil is an ordinary caller that
		// stays for the answer.
		callerCtx   func() context.Context
		wantContain []string
		wantAbsent  []string
	}{
		{
			// The wait returns as soon as the child exits, so the generous
			// timeout below is never reached — reaching it would itself be the
			// bug this case is about.
			name:    "child exits before rendezvous",
			script:  exitsImmediately,
			label:   "failed",
			timeout: 60 * time.Second,
			wantContain: []string{
				"process exited before rendezvous",
				"exit status 1",
				// the child's own account of why, from its stderr
				"is already running",
			},
			wantAbsent: []string{"timed out", "timeout"},
		},
		{
			// The caller's context is a live request context on both hub paths
			// — r.Context() on the REST resume, the websocket connection's ctx
			// on the RPC one — so a client that drops mid-launch cancels the
			// rendezvous wait. That is the caller walking away, not the machine
			// being slow, and the generous timeout below is never reached.
			name:    "caller abandons the request",
			script:  neverRegisters,
			label:   "canceled",
			timeout: 60 * time.Second,
			callerCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantContain: []string{"request canceled before rendezvous"},
			wantAbsent:  []string{"timed out", "timeout"},
		},
		{
			// A child that starts and never registers is the real timeout, and
			// it must keep saying so.
			name:        "child never registers",
			script:      neverRegisters,
			label:       "timed out",
			timeout:     20 * time.Millisecond,
			wantContain: []string{"timeout waiting for rendezvous"},
		},
	}

	for _, launch := range launches {
		for _, tc := range outcomes {
			t.Run(launch.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				evenerBinary := filepath.Join(dir, "fake-evener")
				writeFakeEvener(t, evenerBinary, tc.script)

				ctx := context.Background()
				if tc.callerCtx != nil {
					ctx = tc.callerCtx()
				}
				err := launch.call(ctx, evenerBinary, filepath.Join(dir, "run"), tc.timeout)
				if err == nil {
					t.Fatal("launch succeeded, want a failure")
				}
				if wantPrefix := launch.action + " " + tc.label + ": "; !strings.HasPrefix(err.Error(), wantPrefix) {
					t.Fatalf("failure does not open with %q:\n%v", wantPrefix, err)
				}
				for _, want := range tc.wantContain {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("failure is missing %q:\n%v", want, err)
					}
				}
				for _, absent := range tc.wantAbsent {
					if strings.Contains(err.Error(), absent) {
						t.Fatalf("failure should not contain %q:\n%v", absent, err)
					}
				}
				if got := diagnostic.Classify(err.Error()).Source; got != diagnostic.SourceHub {
					t.Fatalf("diagnostic source=%q, want %q:\n%v", got, diagnostic.SourceHub, err)
				}
			})
		}
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

	if got["EVENER_HUB_SPAWNED"] != "1" {
		t.Fatalf("EVENER_HUB_SPAWNED=%q, want 1", got["EVENER_HUB_SPAWNED"])
	}
	if got["EVENER_RUN_DIR"] != "/tmp/run" {
		t.Fatalf("EVENER_RUN_DIR=%q, want /tmp/run", got["EVENER_RUN_DIR"])
	}
	if got["EVENER_STATE_DIR"] != "/tmp/state" {
		t.Fatalf("EVENER_STATE_DIR=%q, want /tmp/state", got["EVENER_STATE_DIR"])
	}
	if got["EVENER_HUB_TOKEN"] != "generated-token" {
		t.Fatalf("EVENER_HUB_TOKEN=%q, want generated-token", got["EVENER_HUB_TOKEN"])
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
	// over the HubToken. In practice, Spawn never puts EVENER_HUB_TOKEN in
	// Effective.Env, so the generated token always reaches the child.
	env := launchconfig.ToEnv(launchconfig.EnvInputs{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Env: map[string]string{"EVENER_HUB_TOKEN": "per-launch-override"},
		}},
		RunDir:    "/tmp/run",
		StateDir:  "/tmp/state",
		HubToken:  "generated-token",
		ParentEnv: os.Environ(),
	})
	got := envMap(env)

	if got["EVENER_HUB_TOKEN"] != "per-launch-override" {
		t.Fatalf("EVENER_HUB_TOKEN=%q, want per-launch-override (per-launch env wins)", got["EVENER_HUB_TOKEN"])
	}
}

func TestHubSpawnerSpawnPassesHubTokenToDaemon(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	tokenOut := filepath.Join(dir, "token.txt")
	t.Setenv("TOKEN_OUT", tokenOut)
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"evener-appwire-v3"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s' "$EVENER_HUB_TOKEN" > "$TOKEN_OUT"
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","hub_token":"$EVENER_HUB_TOKEN","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	cfg := DefaultConfig()
	cfg.SpawnTimeout = 2 * time.Second
	spawner := HubSpawner{Cfg: cfg, EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}

	entry, err := spawner.Spawn(context.Background(), hubcore.SpawnRequest{
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
		t.Fatalf("child EVENER_HUB_TOKEN=%q, want generated-token", data)
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
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"evener-appwire-v3"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  printf '%s\n' "$@" > "$ARGS_OUT"
  env > "$ENV_OUT"
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<EOF
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
EOF
  sleep 1
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	cfg := DefaultConfig()
	cfg.SpawnTimeout = 2 * time.Second
	cfg.StateGlob = filepath.Join(stateHome, "evener", "projects", "*")
	spawner := HubSpawner{Cfg: cfg, EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}

	if _, err := spawner.Spawn(context.Background(), hubcore.SpawnRequest{
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
	wantPrefix := filepath.Join(stateHome, "evener", "projects") + string(os.PathSeparator)
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
	if env["EVENER_STATE_DIR"] != stateDir {
		t.Fatalf("child EVENER_STATE_DIR=%q, want %q", env["EVENER_STATE_DIR"], stateDir)
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

func TestHubSpawnerListsModelsFromEvenerLaunchContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"evener-appwire-v3","models":[{"provider":"openai","model":"gpt-5.5"}]}\n'
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	spawner := HubSpawner{Cfg: DefaultConfig(), EvenerBinary: bin, RunDir: runDir, HubToken: "generated-token"}

	models, err := spawner.ListLaunchModels(context.Background())
	if err != nil {
		t.Fatalf("ListLaunchModels: %v", err)
	}
	if len(models) != 1 || models[0].Provider != "openai" || models[0].Model != "gpt-5.5" {
		t.Fatalf("models=%+v", models)
	}
}

func TestValidateEvenerLaunchContractRejectsIncompatibleProtocolBinary(t *testing.T) {
	evenerBinary := filepath.Join(t.TempDir(), "old-evener")
	writeFakeEvener(t, evenerBinary, `#!/bin/sh
case " $* " in
  *" --protocol evener-appwire-v3 "*)
    printf '{"protocol":"evener-appwire-v2"}\n'
    exit 0
    ;;
esac
echo 'unsupported appwire protocol' >&2
exit 2
`)

	err := validateEvenerLaunchContract(context.Background(), evenerBinary, "", nil)
	if err == nil {
		t.Fatal("validateEvenerLaunchContract accepted an evener-appwire-v2 child binary")
	}
	assertHubLaunchError(t, err)
}

// A launch-check that never produced a verdict was stopped for one of two
// unrelated reasons — the evenerLaunchCheckTimeout budget ran out, or the caller
// that asked for the answer went away — and checkCtx.Err() is non-nil for both.
// Calling the second one a timeout sends an operator triaging it after a slow
// machine or a hung `evener launch-check`, when nothing was slow and nobody is
// waiting for the answer any more (kata zg02).
//
// This is the FIRST place a mid-launch cancellation lands. The launch-check runs
// ahead of the rendezvous wait 0c3g covers and carries its own budget, so it is
// the message an operator actually sees when a client drops mid-launch.
//
// Every outcome stays an appwire.HubLaunchError. That is the discriminator each
// surface keys off — the web client's isHubLaunchError (protocol/errors.ts) and
// the TUI notice panel both read evenerErrorInfo "hubLaunch" to headline the
// failure "Couldn't start this session" — so the label is what changes and the
// family of failure is not. Unlike the daemon launch failures of 42ck and 0c3g,
// these strings are never keyword-classified: no surface runs
// diagnostic.Classify over them, because the structured error carries the
// attribution instead.
func TestEvenerLaunchCheckFailureNamesWhatActuallyHappened(t *testing.T) {
	t.Parallel()
	const neverAnswers = `#!/bin/sh
sleep 30
`
	const rejectsTheLaunch = `#!/bin/sh
echo 'unknown provider: openrouter' >&2
exit 2
`
	checks := []struct {
		name string
		call func(ctx context.Context, evenerBinary string) error
	}{
		{
			name: "validate",
			call: func(ctx context.Context, evenerBinary string) error {
				return validateEvenerLaunchContract(ctx, evenerBinary, "openrouter/free", nil)
			},
		},
		{
			name: "models",
			call: func(ctx context.Context, evenerBinary string) error {
				_, err := listEvenerLaunchModelContract(ctx, evenerBinary, nil)
				return err
			},
		},
	}
	outcomes := []struct {
		name   string
		script string
		// The context the hub hands the check. Every hub path that reaches one
		// passes a live request context: r.Context() on the REST resume, the
		// websocket connection's ctx on the RPC one.
		callerCtx   func(t *testing.T) context.Context
		wantContain string
		wantAbsent  []string
	}{
		{
			// A browser that navigates away, a dropped connection, or a
			// keepalive that gives up cancels the check without any budget
			// having elapsed. Nobody was slow; the requester simply left.
			name:   "caller abandons the request",
			script: neverAnswers,
			callerCtx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				t.Cleanup(cancel)
				return ctx
			},
			wantContain: "evener launch-check canceled",
			wantAbsent:  []string{"timed out", "timeout"},
		},
		{
			// A check that starts and never answers is the real timeout, and it
			// must keep saying so. A deadline the caller brought with it is the
			// same thing as the hub's own budget — time genuinely ran out —
			// which is why this drives the branch through the caller's context
			// rather than waiting out evenerLaunchCheckTimeout.
			name:   "the check never answers",
			script: neverAnswers,
			callerCtx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			wantContain: "evener launch-check timed out",
			wantAbsent:  []string{"canceled"},
		},
		{
			// The control: a check that ran, answered, and refused the launch is
			// neither of the above, and none of the three may borrow another's
			// label.
			name:        "the check refuses the launch",
			script:      rejectsTheLaunch,
			wantContain: "evener launch-check failed",
			wantAbsent:  []string{"timed out", "timeout", "canceled"},
		},
	}

	for _, check := range checks {
		for _, tc := range outcomes {
			t.Run(check.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				evenerBinary := filepath.Join(t.TempDir(), "fake-evener")
				writeFakeEvener(t, evenerBinary, tc.script)

				ctx := context.Background()
				if tc.callerCtx != nil {
					ctx = tc.callerCtx(t)
				}
				err := check.call(ctx, evenerBinary)
				assertHubLaunchError(t, err)
				if !strings.Contains(err.Error(), tc.wantContain) {
					t.Fatalf("failure is missing %q:\n%v", tc.wantContain, err)
				}
				for _, absent := range tc.wantAbsent {
					if strings.Contains(err.Error(), absent) {
						t.Fatalf("failure should not contain %q:\n%v", absent, err)
					}
				}
			})
		}
	}
}

func TestValidateEvenerLaunchContractRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-evener")
	writeFakeEvener(t, bin, "#!/bin/sh\necho 'unknown provider: openrouter' >&2\nexit 2\n")
	err := validateEvenerLaunchContract(context.Background(), bin, "openrouter/free", envFromMap(map[string]string{}))
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "evener launch-check") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateEvenerLaunchContractMissingBinaryReturnsStructuredDiagnostic(t *testing.T) {
	err := validateEvenerLaunchContract(context.Background(), filepath.Join(t.TempDir(), "missing-evener"), "openai/gpt-5", envFromMap(map[string]string{}))
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "evener launch-check failed") {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateEvenerLaunchContractRedactsSecretsFromDiagnostics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-evener")
	writeFakeEvener(t, bin, "#!/bin/sh\necho \"$OPENROUTER_API_KEY\" >&2\nexit 2\n")
	err := validateEvenerLaunchContract(context.Background(), bin, "openrouter/free", envFromMap(map[string]string{
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
	got := redactEnvSecrets("evener launch-check exited with code 1", envFromMap(map[string]string{
		"EVENER_HUB_TOKEN": "1",
	}))
	if got != "evener launch-check exited with code 1" {
		t.Fatalf("diagnostic=%q", got)
	}
}

func TestResolveEvenerStateDirMatchesServeDefaultForWorkingDir(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveEvenerStateDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(os.Getenv("XDG_STATE_HOME"), "evener", "projects")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("state dir=%q, want prefix %q", got, wantPrefix)
	}
}

// runGitForSpawn runs a git command in dir with a fixed identity, failing the
// test on error. Mirrors cmdutil/statedir_test.go's runGit; a package-main
// test file cannot import an unexported test helper from another package.
func runGitForSpawn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newLinkedWorktreeForSpawn builds an origin-less main repo with one commit
// and a linked worktree, returning their absolute paths. Mirrors
// cmdutil/statedir_test.go's newLinkedWorktree fixture.
func newLinkedWorktreeForSpawn(t *testing.T) (main, wt string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	runGitForSpawn(t, base, "init", "-q", "main")
	runGitForSpawn(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt = filepath.Join(base, "wt")
	runGitForSpawn(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	return main, wt
}

// TestResolveEvenerStateDirLinkedWorktreeSameAsMain proves the fix for the bug
// described in
// docs/superpowers/specs/2026-07-02-native-worktree-tools-design.md §1
// ("Runtime state keying at launch"): resolveEvenerStateDirWithStateHome (the
// path that computes req.StateDir for every hub-spawned evener session) used
// to key off the raw workDir, so for an origin-less repo, spawning from a
// linked worktree computed a different session state dir than spawning from
// the main checkout — the same class of bug Task 3 already fixed in
// cmdutil.DefaultProjectStateDir for `evener run`/`evener serve`.
func TestResolveEvenerStateDirLinkedWorktreeSameAsMain(t *testing.T) {
	main, wt := newLinkedWorktreeForSpawn(t)

	mainDir, err := resolveEvenerStateDir(main, "")
	if err != nil {
		t.Fatal(err)
	}
	wtDir, err := resolveEvenerStateDir(wt, "")
	if err != nil {
		t.Fatal(err)
	}

	if mainDir != wtDir {
		t.Errorf("state dir differs between main root and linked worktree:\n  main = %q\n  wt   = %q", mainDir, wtDir)
	}
}

// TestResolveEvenerStateDirNotInRepoFallsBackToWorkDir covers the
// not-in-a-repo case: ResolveMainRepoRootLocal returns "" and the state dir
// must key off workDir unchanged, matching pre-existing (pre-fix) behavior
// for non-git directories. The path must land under the state home it was
// given, named by the project id workDir resolves to, and two distinct
// non-repo dirs must not share one.
func TestResolveEvenerStateDirNotInRepoFallsBackToWorkDir(t *testing.T) {
	workDir := t.TempDir()
	other := t.TempDir()
	stateHome := t.TempDir()

	project, got, err := resolveEvenerStateDirWithProject(workDir, "", stateHome)
	if err != nil {
		t.Fatal(err)
	}
	if project.ID == "" {
		t.Fatalf("resolveEvenerStateDir(%q) resolved no project id for a non-repo dir", workDir)
	}
	if want := filepath.Join(stateHome, "evener", "projects", project.ID); got != want {
		t.Errorf("state dir = %q, want %q", got, want)
	}
	otherDir, err := resolveEvenerStateDirWithStateHome(other, "", stateHome)
	if err != nil {
		t.Fatal(err)
	}
	if got == otherDir {
		t.Errorf("resolveEvenerStateDir collided for distinct non-repo workDirs %q and %q: %q", workDir, other, got)
	}
}

// newSpawnGateRegistry builds a hermetic registry holding exactly the named
// instances, resolved against an env and a state root the test owns, and
// wraps it in the holder the spawn gate reads.
func newSpawnGateRegistry(t *testing.T, stateRoot string, env map[string]string, instances map[string]registry.Provider) *hubcore.ProviderRegistry {
	t.Helper()
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		opts := []registry.Option{
			registry.WithOffline(true),
			registry.WithoutCache(),
			registry.WithNoUserLayer(),
			registry.WithStateRoot(stateRoot),
			registry.WithEnv(func(name string) (string, bool) {
				v, ok := env[name]
				return v, ok
			}),
			registry.WithInstances(instances),
		}
		r, err := registry.Load(append(opts, extra...)...)
		return r, nil, err
	})
	if err := holder.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	return holder
}

// writeCodexOAuthRecord writes a usable Codex OAuth record for instanceName
// where the registry looks for one: <stateRoot>/auth/<instance>.json.
func writeCodexOAuthRecord(t *testing.T, stateRoot, instanceName string) {
	t.Helper()
	if err := authopenai.SaveAuth(stateRoot, instanceName, authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Minute),
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		AccessToken:  "oauth-access-token",
		RefreshToken: "oauth-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
		Email:        "work@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
}

// TestValidateProviderCredentials_CodexInstanceOAuth puts the spawn gate on
// the Codex transport: the record at auth/<instance>.json is the credential,
// and without it the refusal names the login command the registry's warning
// carries (spec §9.5, §11.3).
func TestValidateProviderCredentials_CodexInstanceOAuth(t *testing.T) {
	for _, tt := range []struct {
		name      string
		hasRecord bool
	}{
		{name: "with an OAuth record", hasRecord: true},
		{name: "without an OAuth record"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stateRoot := t.TempDir()
			if tt.hasRecord {
				writeCodexOAuthRecord(t, stateRoot, "work")
			}
			reg := newSpawnGateRegistry(t, stateRoot, nil, map[string]registry.Provider{
				"work": {Base: "openai-codex"},
			})
			err := validateProviderCredentials("work", reg)
			if tt.hasRecord {
				if err != nil {
					t.Fatalf("validateProviderCredentials(work) with auth/work.json: %v", err)
				}
				return
			}
			assertHubLaunchError(t, err)
			if !strings.Contains(err.Error(), "evener openai login --instance work") {
				t.Fatalf("the refusal names the login command: %v", err)
			}
		})
	}
}

// TestValidateProviderCredentials_AuthSchemesNeedingNothing covers the two
// schemes that have no credential to look for: auth = none and
// optional-bearer both launch with nothing configured (spec §11.3).
func TestValidateProviderCredentials_AuthSchemesNeedingNothing(t *testing.T) {
	for _, auth := range []string{registry.AuthNone, registry.AuthOptionalBearer} {
		t.Run(auth, func(t *testing.T) {
			reg := newSpawnGateRegistry(t, t.TempDir(), nil, map[string]registry.Provider{
				"local": {
					Base:      "openai-compatible",
					Transport: registry.Transport{BaseURL: "http://127.0.0.1:11434/v1", Auth: auth},
				},
			})
			if err := validateProviderCredentials("local", reg); err != nil {
				t.Fatalf("validateProviderCredentials(local) with auth = %q: %v", auth, err)
			}
		})
	}
}

// TestValidateProviderCredentials_ResolvedKeyPasses walks the credential
// sources a bearer instance can launch with, and the empty environment that
// refuses it.
func TestValidateProviderCredentials_ResolvedKeyPasses(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider registry.Provider
		env      map[string]string
		wantErr  bool
	}{
		{
			name:     "inline api_key",
			provider: registry.Provider{Base: "anthropic", APIKey: "sk-inline-key"},
		},
		{
			name:     "credential header",
			provider: registry.Provider{Base: "anthropic", CredentialHeaders: map[string]string{"Authorization": "Bearer $GATEWAY_KEY"}},
			env:      map[string]string{"GATEWAY_KEY": "gk"},
		},
		{
			name:     "named api_key_env",
			provider: registry.Provider{Base: "anthropic", APIKeyEnv: []string{"WORK_KEY"}},
			env:      map[string]string{"WORK_KEY": "wk"},
		},
		{
			name:     "no credential anywhere",
			provider: registry.Provider{Base: "anthropic", APIKeyEnv: []string{"WORK_KEY"}},
			wantErr:  true,
		},
		{
			name:     "credential header whose variable is unset",
			provider: registry.Provider{Base: "anthropic", CredentialHeaders: map[string]string{"Authorization": "Bearer $GATEWAY_KEY"}},
			wantErr:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reg := newSpawnGateRegistry(t, t.TempDir(), tt.env, map[string]registry.Provider{"work": tt.provider})
			err := validateProviderCredentials("work", reg)
			if tt.wantErr {
				assertHubLaunchError(t, err)
				if !strings.Contains(err.Error(), "work") {
					t.Fatalf("the refusal names the instance: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateProviderCredentials(work): %v", err)
			}
		})
	}
}

// TestValidateProviderCredentials_EndpointStop is spec §10's rule at the
// spawn gate: an instance with its own base_url never inherits the vendor
// key, so a launch that would 401 is refused before the daemon starts.
func TestValidateProviderCredentials_EndpointStop(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "vendor-key"}
	reg := newSpawnGateRegistry(t, t.TempDir(), env, map[string]registry.Provider{
		"gateway": {Base: "anthropic", Transport: registry.Transport{BaseURL: "https://gw.example.test/v1"}},
	})
	err := validateProviderCredentials("gateway", reg)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("the refusal names the gateway instance: %v", err)
	}
}

// TestValidateProviderCredentials_CuratedImplicitWithoutCredential covers the
// name that is a curated implicit provider but has no credential in this
// environment, so it never became an instance: the refusal names the
// variables that would make it one.
func TestValidateProviderCredentials_CuratedImplicitWithoutCredential(t *testing.T) {
	reg := newSpawnGateRegistry(t, t.TempDir(), nil, nil)
	err := validateProviderCredentials("groq", reg)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Fatalf("the refusal names the variable that would configure groq: %v", err)
	}
}

// TestValidateProviderCredentials_UnknownInstance covers a name the registry
// has never heard of: the refusal says how to declare it.
func TestValidateProviderCredentials_UnknownInstance(t *testing.T) {
	reg := newSpawnGateRegistry(t, t.TempDir(), nil, nil)
	err := validateProviderCredentials("nowhere", reg)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "[providers.nowhere]") {
		t.Fatalf("the refusal says how to declare the instance: %v", err)
	}
}

// TestValidateProviderCredentials_NoRegistryOrProviderSkips keeps the gate
// open where there is nothing to check: no launched instance, or a hub whose
// registry never loaded.
func TestValidateProviderCredentials_NoRegistryOrProviderSkips(t *testing.T) {
	reg := newSpawnGateRegistry(t, t.TempDir(), nil, nil)
	if err := validateProviderCredentials("", reg); err != nil {
		t.Fatalf("no provider, no gate: %v", err)
	}
	if err := validateProviderCredentials("anything", nil); err != nil {
		t.Fatalf("no registry, no gate: %v", err)
	}
	if err := validateProviderCredentials("anything", hubcore.NewProviderRegistry(nil)); err != nil {
		t.Fatalf("a registry that never loaded, no gate: %v", err)
	}
}

// TestHubSpawnerResumeAcceptsCredentiallessOllamaConfig resumes against a
// registry whose only instance authenticates with nothing — the shape a
// machine with no provider keys ends up with. Resume must still reach the
// daemon.
func TestHubSpawnerResumeAcceptsCredentiallessOllamaConfig(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "fake-evener")
	script := `#!/bin/sh
if [ "$1" = "launch-check" ]; then
  printf '{"protocol":"evener-appwire-v3"}\n'
  exit 0
fi
if [ "$1" = "serve" ]; then
  mkdir -p "$EVENER_RUN_DIR"
  cat > "$EVENER_RUN_DIR/$$.json" <<RENDEZVOUS
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
RENDEZVOUS
  sleep 1
  exit 0
fi
exit 2
`
	writeFakeEvener(t, bin, script)

	reg := newSpawnGateRegistry(t, t.TempDir(), nil, map[string]registry.Provider{
		"ollama": {Base: "ollama"},
	})
	spawner := HubSpawner{
		Cfg:                 DefaultConfig(),
		EvenerBinary:        bin,
		RunDir:              runDir,
		HubToken:            "generated-token",
		Registry:            reg,
		ProvidersConfigPath: filepath.Join(dir, "providers.toml"),
		CredentialsPath:     filepath.Join(dir, "credentials.toml"),
	}
	if _, err := spawner.Resume(context.Background(), hubcore.ResumeRequest{
		SessionID:  "01JRESUME",
		Provider:   "ollama",
		Resolved:   launchconfig.Resolved{Effective: launchconfig.Layer{Model: "ollama/llama3"}},
		WorkingDir: dir,
	}); err != nil {
		t.Fatalf("Resume with a credential-less ollama instance: %v", err)
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

// TestHubSpawnerListLaunchModelContract_NonexistentWorkingDir verifies the
// model picker keeps working before the spawn form's working directory has
// been created. The spawn flow lets a user type a not-yet-existing directory
// (preflight offers to create it on submit), and the model selector loads its
// launchable set scoped by that cwd. Resolving the project state dir for a
// non-existent path fails at EvalSymlinks; the lister must fall back to the
// unscoped (default) model contract instead of returning an error that empties
// the picker. See ModelField.tsx: a rejected model/list surfaces
// "Couldn't load models" in the catalog.
func TestHubSpawnerListLaunchModelContract_NonexistentWorkingDir(t *testing.T) {
	dir := t.TempDir()
	// A working directory that does not (yet) exist on disk.
	nonexistent := filepath.Join(dir, "never", "created")

	var capturedStateDir string
	oldFn := listEvenerLaunchModelContractFn
	listEvenerLaunchModelContractFn = func(_ context.Context, _ string, env []string) (appwire.ModelListResponse, error) {
		capturedStateDir = envMap(env)["EVENER_STATE_DIR"]
		return appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}},
		}, nil
	}
	t.Cleanup(func() { listEvenerLaunchModelContractFn = oldFn })

	h := &HubSpawner{}
	resp, err := h.ListLaunchModelContractForWorkingDir(context.Background(), nonexistent)
	if err != nil {
		t.Fatalf("ListLaunchModelContractForWorkingDir(nonexistent) = err %v, want nil (fall back to unscoped contract)", err)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("resp.Data empty; want the fallback model contract")
	}
	// The fallback must use the unscoped state dir (resolved from an empty
	// workDir, i.e. the hub's own cwd), NOT a path derived from the
	// non-existent directory.
	if capturedStateDir == "" {
		t.Fatalf("EVENER_STATE_DIR not set in spawned env")
	}
	if strings.Contains(capturedStateDir, nonexistent) {
		t.Fatalf("EVENER_STATE_DIR = %q, must not be derived from the non-existent working dir", capturedStateDir)
	}
}
