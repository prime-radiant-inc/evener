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
// now calls them here rather than carrying its own copies.

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

// hostTargetBuild caches the cross-compiled host binary once per target per
// test binary: two live checks staging the same target share one `go build`.
var hostTargetBuild struct {
	mu   sync.Mutex
	bins map[string][]byte
	err  error
}

// stageHostTargetBinary cross-compiles this checkout's ./cmd/evener, unstamped,
// for the host's target and returns its bytes. It is deliberately unstamped so
// its reported version matches the controller's own unstamped test build
// ("dev"): the version ladder compares the strings, so a stamped artifact would
// be refused rather than bridged to. The build is cached once per target per
// test binary.
func stageHostTargetBinary(t *testing.T, goos, goarch string) []byte {
	t.Helper()
	key := goos + "-" + goarch
	hostTargetBuild.mu.Lock()
	defer hostTargetBuild.mu.Unlock()
	if hostTargetBuild.err != nil {
		t.Fatalf("stage the disposable-host binary: %v", hostTargetBuild.err)
	}
	if hostTargetBuild.bins == nil {
		hostTargetBuild.bins = map[string][]byte{}
	}
	if path, ok := hostTargetBuild.bins[key]; ok {
		return path
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	dir := filepath.Join(testEnv.Root, "host-target-bin", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	out := filepath.Join(dir, "evener")
	build := exec.Command("go", "build", "-o", out, "./cmd/evener/")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if combined, err := build.CombinedOutput(); err != nil {
		hostTargetBuild.err = fmt.Errorf("build ./cmd/evener/ for %s: %w\n%s", key, err, combined)
		t.Fatalf("stage the disposable-host binary: %v", hostTargetBuild.err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read staged binary %s: %v", out, err)
	}
	if len(data) == 0 {
		t.Fatalf("staged binary %s is empty", out)
	}
	hostTargetBuild.bins[key] = data
	return data
}

// hostHubLaunchScript is the detached remote launch of the host's own hub with
// a disposable config root. Each word is quoted individually and the line is
// never handed to `sh -c`. `env` carries XDG_CONFIG_HOME into the hub process —
// the one variable that relocates its whole config root — and nohup +
// </dev/null + redirected fds is the same detachment sshconn's relaunchCommand
// uses (macOS has no setsid), so the hub outlives the ssh command.
func hostHubLaunchScript(evenerAbs, configPath, hostDir, addr string) string {
	cmd := strings.Join([]string{
		shellquote.RemoteWord(evenerAbs),
		"hub",
		"--config", shellquote.RemoteWord(configPath),
		"--addr", shellquote.RemoteWord(addr),
	}, " ")
	return "nohup env XDG_CONFIG_HOME=" + shellquote.RemoteWord(hostDir) + " " + cmd +
		" </dev/null >>" + shellquote.RemoteWord(hostDir+"/hub.log") + " 2>&1 &"
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
		out, err := host.run("curl -fsS " + shellquote.RemoteWord(url))
		if err == nil && strings.Contains(string(out), `"version"`) {
			return
		}
		last = strings.TrimSpace(string(out))
		time.Sleep(300 * time.Millisecond)
	}
	logTail, _ := host.run("tail -n 40 " + shellquote.RemoteWord(logPath))
	t.Fatalf("the disposable host hub never answered %s within 60s (last probe %q); hub log tail:\n%s", url, last, logTail)
}
