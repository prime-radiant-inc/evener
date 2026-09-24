//go:build unix

package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sandboxpkg "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_ServeStartupSweepStaysInsideTheHostTempBases runs a real `evener
// serve` with EVENER_HOST_TEMP_BASES naming one base and proves the startup
// crashed-scratch sweep in that separate process reclaims an abandoned session
// temp container there while one in another base, standing in for the
// machine's /tmp, survives. No setting inside this test process reaches a
// child evener, so before the variable existed every evener the non-short suite
// started swept the developer's real /tmp and /var/tmp.
func TestE2E_ServeStartupSweepStaysInsideTheHostTempBases(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds the evener binary and runs a daemon")
	}

	named := worldUsableHostTempBase(t)
	stoodInForTmp := worldUsableHostTempBase(t)
	abandoned := abandonedSessionTmpContainer(t, named)
	decoy := abandonedSessionTmpContainer(t, stoodInForTmp)

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(filepath.Join(configDir, "evener"), 0o700); err != nil {
		t.Fatal(err)
	}
	providersTOML := fmt.Sprintf(`
default = "fake"

[providers.fake]
base     = "openai-compatible"
base_url = %q
api_key  = "fakellm-not-a-secret"
`, provider.BaseURL())
	if err := os.WriteFile(filepath.Join(configDir, "evener", "providers.toml"), []byte(providersTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	daemon := exec.Command(filepath.Join(liveStackBinaries(t, repoRoot), "evener"), "serve",
		"--model", "fake/"+fakellm.ModelID,
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
	)
	// Every base the sweep walks is this test's own: the scratch bases through
	// TMPDIR and the user cache dir, the host temp bases through the variable
	// under test. Later entries win, so these replace the inherited values.
	daemon.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+configDir,
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"TMPDIR="+t.TempDir(),
		envvars.EVENERHostTempBases.Assignment(named),
	)
	logPath := filepath.Join(home, "daemon.log")
	daemonLog, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stdout = daemonLog
	daemon.Stderr = daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatalf("start evener serve: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- daemon.Wait() }()
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		<-exited
		_ = daemonLog.Close()
		if t.Failed() {
			if body, readErr := os.ReadFile(logPath); readErr == nil {
				t.Logf("daemon log:\n%s", body)
			}
		}
	})

	// The sweep runs on its own goroutine and reports nothing when it
	// succeeds, so the container's removal is the only completion there is to
	// watch. The deadline is a tripwire for a sweep that never comes.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(abandoned); os.IsNotExist(err) {
			break
		}
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("evener serve exited (%v) with the abandoned container %q in %s still in place", err, abandoned, envvars.EVENERHostTempBases.Name)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("evener serve's startup sweep never reclaimed %q in the base %s names", abandoned, envvars.EVENERHostTempBases.Name)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(decoy); err != nil {
		t.Fatalf("evener serve's sweep reached %q, outside the bases %s names: %v", decoy, envvars.EVENERHostTempBases.Name, err)
	}
}

// worldUsableHostTempBase creates a directory with /tmp's own mode, which a
// session temp container is only ever minted in.
func worldUsableHostTempBase(t *testing.T) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), "host-temp")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	return base
}

// abandonedSessionTmpContainer leaves in base what a session that closed two
// days ago and never came back leaves: a retained container, lease released,
// older than the sweep's reclaim window.
func abandonedSessionTmpContainer(t *testing.T, base string) string {
	t.Helper()
	restore := sandboxpkg.SetWorldTempBasesForTesting([]string{base})
	tmp, err := sandboxpkg.NewSessionTmp()
	restore()
	if err != nil {
		t.Fatalf("NewSessionTmp in %q: %v", base, err)
	}
	if err := tmp.Retain(); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	container := filepath.Dir(tmp.Dir)
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(container, stale, stale); err != nil {
		t.Fatal(err)
	}
	return container
}
