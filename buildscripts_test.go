package evener_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runWebPreflight runs the real scripts/web/web-preflight.sh against frontend,
// a throwaway frontend directory (EVENER_WEB_FRONTEND_DIR), never the shared
// install. Every case here is refused before the script would reach npm.
func runWebPreflight(t *testing.T, frontend string) (string, error) {
	t.Helper()
	command := exec.Command("scripts/web/web-preflight.sh")
	command.Env = append(os.Environ(), "EVENER_WEB_FRONTEND_DIR="+frontend)
	output, err := command.CombinedOutput()
	return string(output), err
}

// A symlinked node_modules is another worktree's shared install, and npm ci
// deletes an existing node_modules first: through the symlink, that deletes
// the shared install for every worktree. Preflight refuses whenever the shared
// install's lockfile differs, however the timestamps fall: an older shared
// install would otherwise trip the -nt freshness check into npm ci, and a
// newer one used to short-circuit the lockfile comparison entirely.
func TestWebPreflightRefusesNpmCiThroughASymlink(t *testing.T) {
	for name, age := range map[string]time.Duration{"older": -time.Hour, "newer": time.Hour} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			frontend := filepath.Join(root, "frontend")
			writeTestFile(t, filepath.Join(frontend, "package-lock.json"), []byte("{}\n"), 0o644)
			shared := filepath.Join(root, "shared-node-modules")
			writeTestFile(t, filepath.Join(shared, "package-lock.json"), []byte("{\"different\":true}\n"), 0o644)
			if err := os.Symlink(shared, filepath.Join(frontend, "node_modules")); err != nil {
				t.Fatalf("symlink node_modules: %v", err)
			}
			stamp := time.Now().Add(age)
			if err := os.Chtimes(shared, stamp, stamp); err != nil {
				t.Fatalf("date the shared install: %v", err)
			}

			output, err := runWebPreflight(t, frontend)
			if err == nil {
				t.Fatalf("preflight accepted a mismatched symlinked node_modules; output = %s", output)
			}
			if !strings.Contains(output, "symlink") {
				t.Fatalf("refusal does not explain the symlink, so the reader cannot act on it; output = %s", output)
			}
			if _, err := os.Stat(filepath.Join(shared, "package-lock.json")); err != nil {
				t.Fatalf("the shared install was touched despite the refusal: %v", err)
			}
		})
	}
}

// An empty node_modules newer than the lockfile skips npm ci on the -nt check,
// so without the health check the build would run against a toolchain that is
// not there.
func TestWebPreflightRefusesAnInstallWithNoRealTsc(t *testing.T) {
	frontend := filepath.Join(t.TempDir(), "frontend")
	writeTestFile(t, filepath.Join(frontend, "package-lock.json"), []byte("{}\n"), 0o644)
	if err := os.MkdirAll(filepath.Join(frontend, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	backdated := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(frontend, "package-lock.json"), backdated, backdated); err != nil {
		t.Fatal(err)
	}

	output, err := runWebPreflight(t, frontend)
	if err == nil {
		t.Fatalf("preflight accepted an empty node_modules; output = %s", output)
	}
	if !strings.Contains(output, "tsc") {
		t.Fatalf("refusal does not name the toolchain check that failed; output = %s", output)
	}
}

// make install must build the web frontend before the evener binary that embeds
// it, or the installed hub serves a stale SPA. make -n only prints the plan (no
// recipe on this path re-enters make, which -n would run).
func TestMakeInstallBuildsTheWebBeforeTheHub(t *testing.T) {
	output, err := exec.Command("make", "-n", "install").CombinedOutput()
	if err != nil {
		t.Fatalf("make -n install: %v\n%s", err, output)
	}
	lines := strings.Split(string(output), "\n")
	webBuild, hubBuild := -1, -1
	for i, line := range lines {
		if webBuild == -1 && strings.Contains(line, "npm run build") {
			webBuild = i
		}
		if hubBuild == -1 && strings.Contains(line, "./cmd/evener/") {
			hubBuild = i
		}
	}
	if webBuild == -1 || hubBuild == -1 || webBuild > hubBuild {
		t.Fatalf("make -n install does not build the web (line %d) before the evener binary (line %d); output = %s", webBuild, hubBuild, output)
	}
}

// Releases build through goreleaser, whose before hook must build the web
// frontend first for the same reason.
func TestReleaseBuildsTheWebFirst(t *testing.T) {
	output, err := exec.Command("make", "-n", "dist").CombinedOutput()
	if err != nil {
		t.Fatalf("make -n dist: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "goreleaser release --snapshot --clean") {
		t.Fatalf("make -n dist does not defer to a goreleaser snapshot build; output = %s", output)
	}
	cfg, err := os.ReadFile(".goreleaser.yml")
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}
	hook := bytes.Index(cfg, []byte("before:"))
	buildWeb := bytes.Index(cfg, []byte("make build-web"))
	if hook == -1 || buildWeb == -1 || buildWeb < hook {
		t.Fatalf(".goreleaser.yml does not run make build-web as a before hook; the hub binary would embed a stale SPA:\n%s", cfg)
	}
}
