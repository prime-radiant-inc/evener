package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/selfupdate"
)

// syncWriter is an io.Writer that closes a channel the first time a write
// contains match, letting a test wait for a specific log line from the
// restart goroutine instead of sleeping.
type syncWriter struct {
	mu    sync.Mutex
	match string
	done  chan struct{}
	fired bool
}

func newSyncWriter(match string) *syncWriter {
	return &syncWriter{match: match, done: make(chan struct{})}
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.fired && strings.Contains(string(p), w.match) {
		w.fired = true
		close(w.done)
	}
	return len(p), nil
}

func setBuild(t *testing.T, sha, channel string) {
	t.Helper()
	prevSHA, prevChannel := buildinfo.GitSHA, buildinfo.Channel
	buildinfo.GitSHA, buildinfo.Channel = sha, channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.Channel = prevSHA, prevChannel })
	// hubUpdateApply deliberately leaves hubUpdateMu locked after a
	// successful apply; reset it so tests stay independent of run order.
	hubUpdateMu = sync.Mutex{}
}

func stubUpdateCheck(t *testing.T, fn func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error)) *int {
	t.Helper()
	calls := 0
	previous := runHubUpdateCheck
	runHubUpdateCheck = func(ctx context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		calls++
		return fn(ctx, opts)
	}
	t.Cleanup(func() { runHubUpdateCheck = previous })
	return &calls
}

// stubUpdateAvailable stubs the pre-install freshness check hubUpdateApply
// runs before locking: tests exercising the upgrade seam must not depend on
// live GitHub (AGENTS.md forbids network in default tests).
func stubUpdateAvailable(t *testing.T) {
	t.Helper()
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{
			Channel:         opts.Channel,
			LatestTag:       opts.Channel,
			LatestCommit:    "ffffffffffffffffffffffffffffffffffffffff",
			UpdateAvailable: true,
		}, nil
	})
}

func stubHubSelfUpgrade(t *testing.T, fn func(context.Context, selfupdate.Options) (selfupdate.Result, error)) *int {
	t.Helper()
	calls := 0
	previous := runHubSelfUpgrade
	runHubSelfUpgrade = func(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		calls++
		return fn(ctx, opts)
	}
	t.Cleanup(func() { runHubSelfUpgrade = previous })
	return &calls
}

// stubInstalledResult builds a stub upgrade Result whose Installed paths
// are real temp binaries, so the apply path's digest pinning (which reads
// the installed bytes) works without special-casing tests. Tests that
// need fictional paths must set InstalledSHA256 explicitly or expect the
// missing-digest refusal.
func stubInstalledResult(t *testing.T, paths ...string) selfupdate.Result {
	t.Helper()
	installed := make([]string, 0, len(paths))
	digests := make(map[string]string, len(paths))
	for _, p := range paths {
		var full string
		if filepath.IsAbs(p) {
			full = p
		} else {
			full = filepath.Join(t.TempDir(), p)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("test binary "+full), 0o755); err != nil {
			t.Fatal(err)
		}
		installed = append(installed, full)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		digests[full] = hex.EncodeToString(sum[:])
	}
	return selfupdate.Result{Release: "snapshot", Channel: "snapshot", Installed: installed, InstalledSHA256: digests, ShareBinDir: filepath.Dir(installed[0])}
}

type restartCall struct {
	binary string
	args   []string
}

func stubScheduleRestart(t *testing.T) *[]restartCall {
	t.Helper()
	var calls []restartCall
	previous := scheduleHubRestart
	scheduleHubRestart = func(_ context.Context, _ restartPin, binary string, args []string) {
		calls = append(calls, restartCall{binary, args})
	}
	t.Cleanup(func() { scheduleHubRestart = previous })
	return &calls
}

func TestHubUpdateCheckDevBuildIsNotApplicable(t *testing.T) {
	setBuild(t, "", "")
	calls := stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		t.Fatal("dev build must not call Check")
		return selfupdate.CheckResult{}, nil
	})
	got, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if got.Applicable || got.UpdateAvailable || got.BuildChannel != "dev" || got.CurrentVersion != "dev" {
		t.Fatalf("got %+v", got)
	}
	if *calls != 0 {
		t.Fatalf("Check called %d times", *calls)
	}
}

func TestHubUpdateCheckDefaultsToBuildChannelAndFillsCurrent(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	var gotOpts selfupdate.CheckOptions
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		gotOpts = opts
		return selfupdate.CheckResult{Channel: opts.Channel, LatestTag: "snapshot", LatestCommit: "be70029abc", UpdateAvailable: true}, nil
	})
	got, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if gotOpts.Channel != "snapshot" || gotOpts.CurrentSHA != "3b1c5f8" {
		t.Fatalf("opts = %+v", gotOpts)
	}
	want := appwire.UpdateCheckResponse{
		Channel: "snapshot", BuildChannel: "snapshot", CurrentVersion: "3b1c5f8", CurrentCommit: "3b1c5f8",
		LatestTag: "snapshot", LatestCommit: "be70029abc", UpdateAvailable: true, Applicable: true,
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestHubUpdateCheckHonoursRequestedChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	var gotChannel string
	stubUpdateCheck(t, func(_ context.Context, opts selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		gotChannel = opts.Channel
		return selfupdate.CheckResult{Channel: opts.Channel, LatestTag: "v0.1.0", LatestCommit: "3b1c5f8ffff"}, nil
	})
	got, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{Channel: "release"})
	if err != nil {
		t.Fatalf("hubUpdateCheck: %v", err)
	}
	if gotChannel != "release" || got.Channel != "release" || got.UpdateAvailable {
		t.Fatalf("channel=%q got=%+v", gotChannel, got)
	}
}

func TestHubUpdateCheckPropagatesError(t *testing.T) {
	setBuild(t, "3b1c5f8", "release")
	stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		return selfupdate.CheckResult{}, errors.New("GET x: 403 Forbidden: API rate limit exceeded")
	})
	_, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestHubUpdateApplyDevBuildRefused(t *testing.T) {
	setBuild(t, "", "")
	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil || !strings.Contains(err.Error(), "dev build") {
		t.Fatalf("err = %v", err)
	}
	if *upgrades != 0 || len(*restarts) != 0 {
		t.Fatalf("upgrades=%d restarts=%d", *upgrades, len(*restarts))
	}
}

func TestHubUpdateApplyInstallsThenSchedulesRestartWithHubArgs(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previousArgs := hubProcessArgs
	hubProcessArgs = func() []string { return []string{"/old/evener", "hub", "-addr", "0.0.0.0:9180"} }
	t.Cleanup(func() { hubProcessArgs = previousArgs })

	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, "evener", "evener-dev"), nil
	})
	restarts := stubScheduleRestart(t)

	got, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "snapshot"})
	if err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Requested != "snapshot" || gotOpts.CurrentChannel != "snapshot" {
		t.Fatalf("opts = %+v", gotOpts)
	}
	if !got.Restarting || got.Release != "snapshot" || len(got.Installed) != 2 {
		t.Fatalf("got %+v", got)
	}
	if len(*restarts) != 1 {
		t.Fatalf("restarts = %v", *restarts)
	}
	call := (*restarts)[0]
	if filepath.Base(call.binary) != "evener" {
		t.Fatalf("binary = %q, want the installed evener binary", call.binary)
	}
	if strings.Join(call.args, " ") != "hub -addr 0.0.0.0:9180" {
		t.Fatalf("args = %v", call.args)
	}
}

func TestHubUpdateApplyDefaultChannelIsBuildChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "release")
	stubUpdateAvailable(t)
	var gotOpts selfupdate.Options
	stubHubSelfUpgrade(t, func(_ context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		gotOpts = opts
		return stubInstalledResult(t, "evener", "evener-dev"), nil
	})
	stubScheduleRestart(t)
	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if gotOpts.Requested != "release" {
		t.Fatalf("Requested = %q, want release", gotOpts.Requested)
	}
}

func TestHubUpdateApplyUpgradeFailureDoesNotRestart(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, errors.New("download failed")
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("err = %v", err)
	}
	if len(*restarts) != 0 {
		t.Fatalf("restart scheduled after failed upgrade: %v", *restarts)
	}
}

func TestHubUpdateApplyRejectsResultWithoutInstalledBinary(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{Release: "snapshot", Channel: "snapshot"}, nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil {
		t.Fatal("expected error for empty Installed")
	}
	if len(*restarts) != 0 {
		t.Fatalf("restart scheduled: %v", *restarts)
	}
}

func TestHubUpdateApplyPicksEvenerFromInstalled(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previousArgs := hubProcessArgs
	hubProcessArgs = func() []string { return []string{"/old/evener", "hub"} }
	t.Cleanup(func() { hubProcessArgs = previousArgs })
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return stubInstalledResult(t, "evener-dev", "evener"), nil
	})
	restarts := stubScheduleRestart(t)
	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}
	if len(*restarts) != 1 || filepath.Base((*restarts)[0].binary) != "evener" {
		t.Fatalf("restarts = %v", *restarts)
	}
}

func TestHubUpdateApplyErrorsWhenInstalledHasNoEvener(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return stubInstalledResult(t, "evener-dev"), nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil || !strings.Contains(err.Error(), "evener") {
		t.Fatalf("err = %v", err)
	}
	if len(*restarts) != 0 {
		t.Fatalf("restart scheduled: %v", *restarts)
	}
}

func TestHubUpdateApplyRejectsUnknownChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		t.Fatal("unknown channel must not reach the upgrade seam")
		return selfupdate.Result{}, nil
	})
	restarts := stubScheduleRestart(t)
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "v0.0.1"})
	if err == nil || !strings.Contains(err.Error(), `unknown update channel "v0.0.1"`) {
		t.Fatalf("err = %v", err)
	}
	if *upgrades != 0 || len(*restarts) != 0 {
		t.Fatalf("upgrades=%d restarts=%d", *upgrades, len(*restarts))
	}
}

func TestHubUpdateApplyRejectsNightlyChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		t.Fatal("unknown channel must not reach the upgrade seam")
		return selfupdate.Result{}, nil
	})
	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{Channel: "nightly"})
	if err == nil || !strings.Contains(err.Error(), `unknown update channel "nightly"`) {
		t.Fatalf("err = %v", err)
	}
	if *upgrades != 0 {
		t.Fatalf("upgrades=%d", *upgrades)
	}
}

func TestHubUpdateCheckRejectsUnknownChannel(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	calls := stubUpdateCheck(t, func(context.Context, selfupdate.CheckOptions) (selfupdate.CheckResult, error) {
		t.Fatal("unknown channel must not reach the check seam")
		return selfupdate.CheckResult{}, nil
	})
	_, err := hubUpdateCheck(context.Background(), appwire.UpdateCheckParams{Channel: "v0.0.1"})
	if err == nil || !strings.Contains(err.Error(), `unknown update channel "v0.0.1"`) {
		t.Fatalf("err = %v", err)
	}
	if *calls != 0 {
		t.Fatalf("Check called %d times", *calls)
	}
}

func TestHubUpdateApplySerializesConcurrentCalls(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		entered <- struct{}{}
		<-block
		return selfupdate.Result{}, errors.New("boom")
	})
	stubScheduleRestart(t)

	done := make(chan error, 1)
	go func() {
		_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
		done <- err
	}()
	<-entered

	_, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{})
	if err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("second apply err = %v", err)
	}

	close(block)
	if firstErr := <-done; firstErr == nil || !strings.Contains(firstErr.Error(), "boom") {
		t.Fatalf("first apply err = %v", firstErr)
	}

	// After a failed upgrade the lock must be released so a later apply proceeds.
	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return stubInstalledResult(t, "evener"), nil
	})
	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{}); err != nil {
		t.Fatalf("apply after failure: %v", err)
	}
}

func TestHubUpdateApplyReleasesLockWhenRestartExecFails(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	previousExec := execHubBinary
	execHubBinary = func(binary string, args []string) error { return errors.New("exec failed") }
	t.Cleanup(func() { execHubBinary = previousExec })

	stderr := newSyncWriter("restart failed")
	previousStderr := hubUpdateStderr
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return stubInstalledResult(t, "evener"), nil
	})

	// This test exercises the real scheduleHubRestartAfterResponse on a
	// context with no appserver connection -- the fallback that restarts
	// straight away because there is no response frame to wait for.
	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{}); err != nil {
		t.Fatalf("hubUpdateApply: %v", err)
	}

	<-stderr.done // wait for the goroutine to log the failed restart and release the lock

	upgrades := stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return selfupdate.Result{}, errors.New("boom")
	})
	if _, err := hubUpdateApply(context.Background(), appwire.UpdateApplyParams{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("second apply err = %v", err)
	}
	if *upgrades != 1 {
		t.Fatalf("upgrades = %d, want the second apply to reach the upgrade seam", *upgrades)
	}
}

func TestHubUpdateApplyRestartsAfterTheResponseIsWritten(t *testing.T) {
	setBuild(t, "3b1c5f8", "snapshot")
	stubUpdateAvailable(t)
	previousDelay := hubRestartDelay
	hubRestartDelay = 0
	t.Cleanup(func() { hubRestartDelay = previousDelay })

	execs := make(chan string, 1)
	previousExec := execHubBinary
	execHubBinary = func(binary string, _ []string) error {
		execs <- binary
		return errors.New("exec failed") // keep this process alive and release hubUpdateMu
	}
	t.Cleanup(func() { execHubBinary = previousExec })

	previousStderr := hubUpdateStderr
	stderr := newSyncWriter("restart failed")
	hubUpdateStderr = stderr
	t.Cleanup(func() { hubUpdateStderr = previousStderr })

	stubHubSelfUpgrade(t, func(context.Context, selfupdate.Options) (selfupdate.Result, error) {
		return stubInstalledResult(t, "evener"), nil
	})

	// A real appserver round trip: only that gives the handler a context
	// carrying the connection AfterResponseWritten needs.
	appServer := newHubAppServer(hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         hubcore.NewPastIndex(""),
	}, appsource.NewRegistry())
	hub := httptest.NewServer(http.HandlerFunc(appServer.ServeWebSocket))
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	resp, err := client.UpdateApply(t.Context(), appwire.UpdateApplyParams{Channel: "snapshot"})
	if err != nil {
		t.Fatalf("UpdateApply: %v", err)
	}
	if !resp.Restarting {
		t.Fatalf("apply response = %+v, want Restarting", resp)
	}

	select {
	case binary := <-execs:
		if filepath.Base(binary) != "evener" {
			t.Fatalf("exec'd %q, want the installed evener binary", binary)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the restart never ran after the apply response reached the transport")
	}

	// Join the restart goroutine before cleanup restores execHubBinary and
	// the next test's setBuild resets the shared globals: the exec stub
	// always fails, so the "restart failed" log line (written after the
	// Unlock) proves the goroutine is done touching shared state. Without
	// this the test races the goroutine under -race (cleanup's write to
	// execHubBinary vs the goroutine's read; next setBuild vs Unlock).
	select {
	case <-stderr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the restart goroutine never finished after exec failed")
	}
}
