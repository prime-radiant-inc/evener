package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

// makeDryRun prints make's plan for target without running it. The parent's
// make control variables are dropped, so a test run under make (its jobserver,
// -s, -j) cannot reorder or silence the plan.
func makeDryRun(t *testing.T, target string) string {
	t.Helper()
	command := exec.Command("make", "-n", target)
	for _, entry := range os.Environ() {
		switch name, _, _ := strings.Cut(entry, "="); name {
		case "MAKEFLAGS", "GNUMAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEOVERRIDES":
		default:
			command.Env = append(command.Env, entry)
		}
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %v\n%s", target, err, output)
	}
	return string(output)
}

// Every target that builds evener must build the web frontend first, or the
// binary embeds a stale SPA (or the tracked placeholder): install, and the
// default build through build-runtime and its build-hub alias. make -n only
// prints the plan; no recipe on these paths re-enters make, which -n would run.
// The hub build shows as the evener go build (install) or as the runtime-pair
// script that performs it (build-runtime).
func TestMakeBuildsTheWebBeforeTheHub(t *testing.T) {
	for _, target := range []string{"install", "build", "build-runtime", "build-hub"} {
		t.Run(target, func(t *testing.T) {
			output := makeDryRun(t, target)
			webBuild, hubBuild := -1, -1
			for i, line := range strings.Split(output, "\n") {
				if webBuild == -1 && strings.Contains(line, "npm run build") {
					webBuild = i
				}
				if hubBuild == -1 && (strings.Contains(line, "./cmd/evener/") || strings.Contains(line, "build-runtime-pair.sh")) {
					hubBuild = i
				}
			}
			if webBuild == -1 || hubBuild == -1 || webBuild > hubBuild {
				t.Fatalf("make -n %s does not build the web (line %d) before the evener binary (line %d); output = %s", target, webBuild, hubBuild, output)
			}
		})
	}
}

// Releases build through goreleaser, whose before hooks must build the web
// frontend, for the same reason: goreleaser runs them before any build. The
// hook is pinned as exactly `make build-web` on purpose: that target is the one
// definition of the web build (preflight included), and any other spelling
// would be a second one.
func TestReleaseBuildsTheWebFirst(t *testing.T) {
	output := makeDryRun(t, "dist")
	if !strings.Contains(output, "goreleaser release --snapshot --clean") {
		t.Fatalf("make -n dist does not defer to a goreleaser snapshot build; output = %s", output)
	}
	data, err := os.ReadFile(".goreleaser.yml")
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}
	var cfg struct {
		Before struct {
			Hooks []string `yaml:"hooks"`
		} `yaml:"before"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse .goreleaser.yml: %v", err)
	}
	if !slices.Contains(cfg.Before.Hooks, "make build-web") {
		t.Fatalf(".goreleaser.yml before.hooks = %q, want it to run make build-web so the hub binary embeds a fresh SPA", cfg.Before.Hooks)
	}
}
