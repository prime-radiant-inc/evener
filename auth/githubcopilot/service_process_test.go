//go:build unix && !aix && !solaris

package githubcopilot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	serviceHelperModeEnv     = "EVENER_TEST_COPILOT_SERVICE_MODE"
	serviceHelperStateEnv    = "EVENER_TEST_COPILOT_SERVICE_STATE"
	serviceHelperInstanceEnv = "EVENER_TEST_COPILOT_SERVICE_INSTANCE"
	serviceHelperBaseEnv     = "EVENER_TEST_COPILOT_SERVICE_BASE"
	serviceHelperContendEnv  = "EVENER_TEST_COPILOT_SERVICE_CONTEND"
	serviceHelperDoneEnv     = "EVENER_TEST_COPILOT_SERVICE_DONE"
)

type serviceHelperProcess struct {
	cmd    *exec.Cmd
	out    bytes.Buffer
	waited bool
}

func startServiceHelper(t *testing.T, mode, stateDir, instance, baseURL, contendingPath, donePath string) *serviceHelperProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestServiceHelperProcess$")
	cmd.Env = append(os.Environ(),
		serviceHelperModeEnv+"="+mode,
		serviceHelperStateEnv+"="+stateDir,
		serviceHelperInstanceEnv+"="+instance,
		serviceHelperBaseEnv+"="+baseURL,
		serviceHelperContendEnv+"="+contendingPath,
		serviceHelperDoneEnv+"="+donePath,
	)
	helper := &serviceHelperProcess{cmd: cmd}
	cmd.Stdout = &helper.out
	cmd.Stderr = &helper.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		if !helper.waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return helper
}

func (h *serviceHelperProcess) wait(t *testing.T) {
	t.Helper()
	h.waited = true
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("helper process: %v\n%s", err, h.out.String())
	}
}

func (h *serviceHelperProcess) output() string { return h.out.String() }

func writeServiceMarker(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatalf("write %s marker: %v", body, err)
	}
}

// waitForLockHeld blocks until another process holds the record lock. The
// caller writes its contention marker only after this returns, so the marker
// means the following WithRecordLock must wait.
func waitForLockHeld(t *testing.T, stateDir, instance string) {
	t.Helper()
	lockPath := AuthFilePath(stateDir, instance) + lockSuffix
	deadline := time.Now().Add(10 * time.Second)
	for !lockFileHeld(t, lockPath) {
		if time.Now().After(deadline) {
			t.Fatalf("record lock %s was never held by the other process", lockPath)
		}
		<-time.After(time.Millisecond)
	}
}

func TestServiceHelperProcess(t *testing.T) {
	mode := os.Getenv(serviceHelperModeEnv)
	if mode == "" {
		t.Skip("service helper subprocess only")
	}
	cfg := DefaultConfig()
	cfg.GitHubBaseURL = os.Getenv(serviceHelperBaseEnv)
	cfg.APIBaseURL = cfg.GitHubBaseURL
	cfg.HTTPTimeout = 30 * time.Second
	svc := NewService(cfg, &http.Client{Timeout: 30 * time.Second})
	stateDir := os.Getenv(serviceHelperStateEnv)
	instance := os.Getenv(serviceHelperInstanceEnv)
	if contendingPath := os.Getenv(serviceHelperContendEnv); contendingPath != "" {
		beforeRecordLock = func() {
			waitForLockHeld(t, stateDir, instance)
			writeServiceMarker(t, contendingPath, "contending")
		}
	}
	ctx := context.Background()
	switch mode {
	case "resolve":
		credentials, err := svc.ResolveRuntimeCredentials(ctx, stateDir, instance)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if credentials.BearerToken == "" || credentials.AccountID == "" {
			t.Fatal("resolve returned incomplete credentials")
		}
		fmt.Println("ok")
	case "reject":
		if _, err := svc.ResolveRuntimeCredentials(ctx, stateDir, instance); !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("resolve error = %v, want ErrLoginRequired", err)
		}
		fmt.Println("ok login-required")
	case "logout":
		existed, err := svc.Logout(stateDir, instance)
		if err != nil {
			t.Fatalf("logout: %v", err)
		}
		fmt.Printf("ok %t\n", existed)
	case "login":
		if _, err := svc.LoginWithDevice(ctx, stateDir, instance, nil); err != nil {
			t.Fatalf("login: %v", err)
		}
		fmt.Println("ok")
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	if donePath := os.Getenv(serviceHelperDoneEnv); donePath != "" {
		writeServiceMarker(t, donePath, "done")
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	if !fileAppearsWithin(path, 10*time.Second) {
		t.Fatalf("timed out waiting for %s", path)
	}
}

// assertBlockedBeforeRelease requires the helper's contention marker and the
// absence of its completion marker. The helper writes contending immediately
// after confirming another process holds the lock, so it cannot reach done
// until the parent releases the lock; this is not a timed absence window.
func assertBlockedBeforeRelease(t *testing.T, contendingPath, donePath string) {
	t.Helper()
	waitForPath(t, contendingPath)
	if _, err := os.Stat(donePath); err == nil {
		t.Fatal("helper completed before the first process released the record lock")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat completion marker: %v", err)
	}
}

func requireCompleted(t *testing.T, donePath string) {
	t.Helper()
	if _, err := os.Stat(donePath); err != nil {
		t.Fatalf("helper did not complete after the lock was released: %v", err)
	}
}

func TestTwoProcessRefreshRotatesOnce(t *testing.T) {
	stateDir := t.TempDir()
	record := testRecord(time.Now(), -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	gh, server := newScriptedGitHub(t)
	release := gh.blockRefresh()
	t.Cleanup(release)

	first := startServiceHelper(t, "resolve", stateDir, record.Instance, server.URL, "", "")
	waitForSignal(t, gh.started, "the first refresh request")
	markers := t.TempDir()
	contending := filepath.Join(markers, "contending")
	done := filepath.Join(markers, "done")
	second := startServiceHelper(t, "resolve", stateDir, record.Instance, server.URL, contending, done)
	assertBlockedBeforeRelease(t, contending, done)
	release()

	first.wait(t)
	second.wait(t)
	requireCompleted(t, done)
	if got := gh.refreshCount(); got != 1 {
		t.Fatalf("refresh requests = %d, want exactly 1 after one rotation", got)
	}
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.AccessToken != "access-1" || loaded.RefreshToken != "refresh-1" {
		t.Fatal("stored record did not carry the single rotation's pair")
	}
	if loaded.RefreshRejected {
		t.Fatal("concurrent resolver left a false rejection")
	}
}

func TestTwoProcessLogoutDuringRefreshWins(t *testing.T) {
	stateDir := t.TempDir()
	record := testRecord(time.Now(), -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	gh, server := newScriptedGitHub(t)
	release := gh.blockRefresh()
	t.Cleanup(release)

	refresher := startServiceHelper(t, "resolve", stateDir, record.Instance, server.URL, "", "")
	waitForSignal(t, gh.started, "the refresh request")
	markers := t.TempDir()
	contending := filepath.Join(markers, "contending")
	done := filepath.Join(markers, "done")
	logout := startServiceHelper(t, "logout", stateDir, record.Instance, server.URL, contending, done)
	assertBlockedBeforeRelease(t, contending, done)
	release()

	refresher.wait(t)
	logout.wait(t)
	requireCompleted(t, done)
	if !strings.Contains(logout.output(), "ok true") {
		t.Fatalf("logout output = %q, want it to find and remove the record", logout.output())
	}
	if got := gh.refreshCount(); got != 1 {
		t.Fatalf("refresh requests = %d, want 1", got)
	}
	if _, err := LoadAuth(stateDir, record.Instance); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() after logout-during-refresh error = %v, want ErrAuthNotFound", err)
	}
}

func TestTwoProcessFreshLoginClearsStaleRejection(t *testing.T) {
	stateDir := t.TempDir()
	record := testRecord(time.Now(), -time.Minute, 24*time.Hour)
	if err := SaveAuth(stateDir, record.Instance, record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}
	gh, server := newScriptedGitHub(t)
	gh.queueRefreshCodes("bad_refresh_token")
	release := gh.blockRefresh()
	t.Cleanup(release)

	rejecter := startServiceHelper(t, "reject", stateDir, record.Instance, server.URL, "", "")
	waitForSignal(t, gh.started, "the refused refresh request")
	markers := t.TempDir()
	contending := filepath.Join(markers, "contending")
	done := filepath.Join(markers, "done")
	login := startServiceHelper(t, "login", stateDir, record.Instance, server.URL, contending, done)
	assertBlockedBeforeRelease(t, contending, done)
	release()

	rejecter.wait(t)
	login.wait(t)
	requireCompleted(t, done)
	loaded, err := LoadAuth(stateDir, record.Instance)
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if loaded.RefreshRejected {
		t.Fatal("fresh login left the previous generation's rejection in place")
	}
	if loaded.AccessToken != "login-access" || loaded.RefreshToken != "" {
		t.Fatal("fresh login did not replace the refused generation")
	}
}
