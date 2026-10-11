package hub

// Shared helpers for the live, disposable-host-side SSH checks (the credential
// push check, app_host_push_credentials_e2e_test.go, and the settings-UI
// acceptance check, app_host_settings_ui_e2e_test.go). Each check owns its own
// run directory, config root, hub port, and cleanup; only the mechanics of
// staging a build onto the host, launching that build's hub detached, and
// waiting for its health endpoint are shared, so the two checks cannot drift
// apart in how they treat a real host.
//
// These were extracted verbatim from the push check (component 07c) when the
// settings-UI check (component 07b) needed the same mechanism; the push check
// now calls them here rather than carrying its own copies. The non-mutation
// guard (its path selection and its before/after comparison) lives here too, so
// the two live checks cannot drift apart in what they prove was left untouched.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/shellquote"
)

// hostTargetBuild caches, per target, the staged host binary once per test
// binary: two live checks staging the same target share one `go build`. The
// build runs OUTSIDE the lock (a per-key single-flight), so building one target
// never serialises another, and only the staged binary's PATH is kept — the
// bytes stay on disk rather than resident in the test binary.
var hostTargetBuild struct {
	mu      sync.Mutex
	entries map[string]*hostTargetBuildEntry
}

// hostTargetBuildEntry is one target's single-flight build: once runs the
// `go build` a single time, and path/err carry its result to every caller.
type hostTargetBuildEntry struct {
	once sync.Once
	path string
	err  error
}

// stageHostTargetBinary cross-compiles this checkout's ./cmd/evener, unstamped,
// for the host's target and returns its bytes. It is deliberately unstamped so
// its reported version matches the controller's own unstamped test build
// ("dev"): the version ladder compares the strings, so a stamped artifact would
// be refused rather than bridged to. The build is cached once per target per
// test binary, by PATH: a caller for a target already built re-reads the staged
// file instead of rebuilding, and the binary is never held resident between
// calls.
func stageHostTargetBinary(t *testing.T, goos, goarch string) []byte {
	t.Helper()
	path := hostTargetBinaryPath(t, goos, goarch)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged binary %s: %v", path, err)
	}
	return data
}

// hostTargetBinaryPath returns the path of the staged host binary for the
// target, building it once (per key) on first use. The single-flight lives per
// key, so two callers for the SAME target share one build while callers for
// different targets never block one another.
func hostTargetBinaryPath(t *testing.T, goos, goarch string) string {
	t.Helper()
	key := goos + "-" + goarch
	hostTargetBuild.mu.Lock()
	if hostTargetBuild.entries == nil {
		hostTargetBuild.entries = map[string]*hostTargetBuildEntry{}
	}
	entry, ok := hostTargetBuild.entries[key]
	if !ok {
		entry = &hostTargetBuildEntry{}
		hostTargetBuild.entries[key] = entry
	}
	hostTargetBuild.mu.Unlock()
	entry.once.Do(func() {
		entry.path, entry.err = buildHostTargetBinary(goos, goarch, key)
	})
	if entry.err != nil {
		t.Fatalf("stage the disposable-host binary: %v", entry.err)
	}
	return entry.path
}

// buildHostTargetBinary compiles ./cmd/evener for the target into a per-target
// directory under the test root and returns the staged path. It takes no
// testing.T: it runs inside a single-flight once that another test's caller may
// be waiting on, so it reports failure through the returned error rather than
// failing a test out from under whichever goroutine owns it.
func buildHostTargetBinary(goos, goarch, key string) (string, error) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		return "", fmt.Errorf("abs repo root: %w", err)
	}
	dir := filepath.Join(testEnv.Root, "host-target-bin", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	out := filepath.Join(dir, "evener")
	build := exec.Command("go", "build", "-o", out, "./cmd/evener/")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if combined, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build ./cmd/evener/ for %s: %w\n%s", key, err, combined)
	}
	info, err := os.Stat(out)
	if err != nil {
		return "", fmt.Errorf("stat staged binary %s: %w", out, err)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("staged binary %s is empty", out)
	}
	return out, nil
}

// hostHubLaunchScript is the detached remote launch of the host's own hub with
// a disposable config root. Each word is quoted individually and the line is
// never handed to `sh -c`. XDG_CONFIG_HOME relocates the hub's whole config
// root, and EVENER_CREDENTIALS_CONFIG / EVENER_PROVIDERS_CONFIG are explicitly
// UNSET with `env -u`: both outrank XDG_CONFIG_HOME when the credential/config
// paths are resolved (cmdutil.CredentialsPath), so a host whose ambient
// environment exports either would otherwise keep the "disposable" hub reading
// and writing the host's real provider and credential files. nohup + </dev/null
// + redirected fds is the same detachment sshconn's relaunchCommand uses (macOS
// has no setsid), so the hub outlives the ssh command.
func hostHubLaunchScript(evenerAbs, configPath, hostDir, addr string) string {
	cmd := strings.Join([]string{
		shellquote.RemoteWord(evenerAbs),
		"hub",
		"--config", shellquote.RemoteWord(configPath),
		"--addr", shellquote.RemoteWord(addr),
	}, " ")
	return "nohup env -u EVENER_CREDENTIALS_CONFIG -u EVENER_PROVIDERS_CONFIG " +
		"XDG_CONFIG_HOME=" + shellquote.RemoteWord(hostDir) + " " + cmd +
		" </dev/null >>" + shellquote.RemoteWord(hostDir+"/hub.log") + " 2>&1 &"
}

// hostHubHealthProbeCommand is the curl probe awaitHostHubHealth polls. It is
// bounded on both connect and total time (--connect-timeout 2 --max-time 5) so
// a hung connection fails fast and the poll retries, rather than stalling the
// whole 60s window until the outer attach timeout.
func hostHubHealthProbeCommand(url string) string {
	return "curl -fsS --connect-timeout 2 --max-time 5 " + shellquote.RemoteWord(url)
}

// awaitHostHubHealth waits until the disposable hub answers its /api/health on
// the host, the same probe sshconn's Ensure makes before it decides to bridge
// rather than bootstrap.
func awaitHostHubHealth(t *testing.T, host *hostSSH, addr, logPath string) {
	t.Helper()
	url := "http://" + addr + "/api/health"
	deadline := time.Now().Add(60 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := host.run(hostHubHealthProbeCommand(url))
		if err == nil && strings.Contains(string(out), `"version"`) {
			return
		}
		last = strings.TrimSpace(string(out))
		time.Sleep(300 * time.Millisecond)
	}
	logTail, _ := host.run("tail -n 40 " + shellquote.RemoteWord(logPath))
	t.Fatalf("the disposable host hub never answered %s within 60s (last probe %q); hub log tail:\n%s", url, last, logTail)
}

// hostGuardedCredentialsPath picks the credential store the non-mutation guard
// hashes: the host's REAL store (realPath, resolved from the host's own
// environment by hostRealCredentialsPath) by default, or the test-owned
// disposable store the check itself writes when the falsification hook named by
// hookEnv is "1". The hook is the only thing that moves the guard off the real
// store, so a guard silently pointed at a file the check never touches — or one
// that never moves under the hook — is visible in the host-free tests below.
func hostGuardedCredentialsPath(hookEnv, realPath, disposablePath string) string {
	if os.Getenv(hookEnv) == "1" {
		return disposablePath
	}
	return realPath
}

// hostRealCredentialsPath resolves, from the HOST's own ambient environment, the
// credential store the host's real hub would use — the path a stray write would
// land on, and so the path the guard must hash. It reads the three overrides off
// the host, so the guard follows the host's ACTUAL resolution rather than an
// assumed ~/.config default: an exported EVENER_CREDENTIALS_CONFIG or
// EVENER_PROVIDERS_CONFIG (or a non-default XDG_CONFIG_HOME) would otherwise
// leave the guard hashing a path nothing touches.
func hostRealCredentialsPath(t *testing.T, host *hostSSH, home string) string {
	t.Helper()
	creds := hostRawEnvValue(t, host, "EVENER_CREDENTIALS_CONFIG")
	providers := hostRawEnvValue(t, host, "EVENER_PROVIDERS_CONFIG")
	xdg := hostRawEnvValue(t, host, "XDG_CONFIG_HOME")
	return hostAmbientCredentialsPath(creds, providers, xdg, home)
}

// hostRawEnvValue reads a host environment variable RAW: no trimming, so a
// whitespace-padded override survives to be resolved exactly as the product
// would resolve it. It uses runStdout, not output, because output trims.
func hostRawEnvValue(t *testing.T, host *hostSSH, name string) string {
	t.Helper()
	out, err := host.runStdout(`printf '%s' "${` + name + `-}"`)
	if err != nil {
		t.Fatalf("read %s from host %s: %v: %s", name, host.target, err, out)
	}
	return string(out)
}

// hostAmbientCredentialsPath mirrors cmdutil.CredentialsPath (cmdutil/registry.go)
// for the override values a host reports, reproducing its whitespace handling
// bound for bound:
//
//   - EVENER_CREDENTIALS_CONFIG and EVENER_PROVIDERS_CONFIG are "present but
//     empty" when empty OR whitespace-only (the product trims for that test), so
//     those fall through; a non-empty value is used RAW and untrimmed.
//   - XDG_CONFIG_HOME falls through only when it is the empty string: the
//     product uses any non-empty value raw (userdirs.ConfigRoot), so a
//     whitespace-only value is NOT treated as unset.
//
// Reading a trimmed host value here would hash a different path than the
// product writes — silently disarming the guard, which is exactly what
// TestHostAmbientCredentialsPathMatchesCmdutil pins against.
func hostAmbientCredentialsPath(credsConfig, providersConfig, xdgConfigHome, home string) string {
	switch {
	case strings.TrimSpace(credsConfig) != "":
		return credsConfig
	case strings.TrimSpace(providersConfig) != "":
		return filepath.Join(filepath.Dir(providersConfig), "credentials.toml")
	case xdgConfigHome != "":
		return filepath.Join(xdgConfigHome, "evener", "credentials.toml")
	default:
		return filepath.Join(home, ".config", "evener", "credentials.toml")
	}
}

// assertGuardedFileIntact fails the test when a file the check must never touch
// on the host appeared or changed during the run. before and after are
// sha256IfFile results ("" means the file was absent); actor names the writer
// for the message and remedy states what the check was allowed to do instead.
func assertGuardedFileIntact(t *testing.T, host *hostSSH, actor, path, remedy, before, after string) {
	t.Helper()
	if before == "" {
		if after != "" {
			t.Errorf("%s created %s on host %s (sha256 %s); %s", actor, path, host.target, after, remedy)
		}
		return
	}
	if after != before {
		t.Errorf("%s changed %s on host %s (sha256 %s -> %s); %s", actor, path, host.target, before, after, remedy)
	}
}
