//go:build unix

package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestUpdateApplyEndToEnd builds this branch's own evener binary as a
// "snapshot" release build, runs it as a real hub, applies an update
// through the RPC, and proves the process replaced itself in place: same
// PID, /api/health back with a different version. Opt-in only: it downloads
// the current public snapshot release from GitHub (AGENTS.md forbids
// network in default tests).
//
// The hub under test is built from this worktree, not downloaded, because
// the publicly published snapshot release only ever reflects main and this
// feature has not merged there yet -- a downloaded "current snapshot" would
// have no evener/update/apply route to call. Building the branch locally
// with -X buildinfo.Channel=snapshot makes it a non-dev build (so self-update
// isn't refused) whose version differs from whatever main last published,
// which is what lets the post-update /api/health read back a different
// version and prove the exec actually swapped binaries.
//
//	EVENER_UPDATE_E2E=1 go test ./cmd/evener-hub/ -run TestUpdateApplyEndToEnd -v
func TestUpdateApplyEndToEnd(t *testing.T) {
	if os.Getenv("EVENER_UPDATE_E2E") == "" {
		t.Skip("set EVENER_UPDATE_E2E=1 to run (downloads from GitHub)")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	sha := gitShortSHA(t, repoRoot)

	binDir := t.TempDir()
	evenerBin := filepath.Join(binDir, "evener")
	build := exec.Command("go", "build",
		"-ldflags", "-X primeradiant.com/evener/buildinfo.Channel=snapshot -X primeradiant.com/evener/buildinfo.GitSHA="+sha,
		"-o", evenerBin, "./cmd/evener/")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build evener (snapshot-channel): %v\n%s", err, out)
	}

	// No model call is made, so the fake provider only needs to exist in
	// config -- base_url is never dialed.
	stack := startHubStackOnProviderWithEvener(t, `
default = "fake"

[providers.fake]
base     = "openai-compatible"
base_url = "http://127.0.0.1:1"
api_key  = "fakellm-not-a-secret"
`, "fake/does-not-matter", evenerBin)

	before := healthVersion(t, stack.addr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := stack.dialRPC(ctx, t)

	resp, err := client.UpdateApply(ctx, appwire.UpdateApplyParams{Channel: "snapshot"})
	if err != nil {
		t.Fatalf("UpdateApply: %v", err)
	}
	if !resp.Restarting {
		t.Fatalf("resp = %+v", resp)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		r, err := http.Get("http://" + stack.addr + "/api/health")
		if err != nil {
			continue
		}
		var body struct {
			Version string `json:"version"`
		}
		decodeErr := json.NewDecoder(r.Body).Decode(&body)
		_ = r.Body.Close()
		if decodeErr != nil || body.Version == "" || body.Version == before {
			// A version equal to "before" can be the old process answering
			// in the window before its self-exec fires; keep polling for a
			// response that can only come from the newly installed binary.
			continue
		}
		if !processAlive(stack.pid) {
			t.Fatalf("hub pid %d died", stack.pid)
		}
		t.Logf("before=%s after=%s pid=%d", before, body.Version, stack.pid)

		installedBin := filepath.Join(stack.home, ".local", "share", "evener", "bin", "evener")
		if _, statErr := os.Stat(installedBin); statErr != nil {
			t.Fatalf("installed snapshot binary missing at %s: %v", installedBin, statErr)
		}
		return
	}
	t.Fatal("hub did not come back with a new version within 30s")
}

func gitShortSHA(t *testing.T, repoRoot string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse --short HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func healthVersion(t *testing.T, addr string) string {
	t.Helper()
	r, err := http.Get("http://" + addr + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer r.Body.Close()
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode /api/health: %v", err)
	}
	return body.Version
}

// processAlive reports whether pid still refers to a live process, using the
// null-signal probe: FindProcess always succeeds on unix, so Signal(0) is
// what actually checks.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
