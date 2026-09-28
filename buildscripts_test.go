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

// sharedInstallFixture lays out a frontend whose node_modules is a symlink to
// another worktree's shared install, the way agent worktrees link it: the
// shared node_modules sits beside the lockfile it was installed from, which is
// where preflight reads it (dirname of the link target). sharedLock is that
// lockfile's content; age dates the shared install against this frontend's.
func sharedInstallFixture(t *testing.T, sharedLock string, age time.Duration) (frontend, shared string) {
	t.Helper()
	root := t.TempDir()
	frontend = filepath.Join(root, "frontend")
	writeTestFile(t, filepath.Join(frontend, "package-lock.json"), []byte("{}\n"), 0o644)
	shared = filepath.Join(root, "shared")
	writeTestFile(t, filepath.Join(shared, "package-lock.json"), []byte(sharedLock), 0o644)
	if err := os.MkdirAll(filepath.Join(shared, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "node_modules"), filepath.Join(frontend, "node_modules")); err != nil {
		t.Fatalf("symlink node_modules: %v", err)
	}
	stamp := time.Now().Add(age)
	if err := os.Chtimes(filepath.Join(shared, "node_modules"), stamp, stamp); err != nil {
		t.Fatalf("date the shared install: %v", err)
	}
	return frontend, shared
}

// A symlinked node_modules is another worktree's shared install, and npm ci
// deletes an existing node_modules first: through the symlink, that deletes
// the shared install for every worktree. Preflight refuses whenever the shared
// install's lockfile differs, however the timestamps fall: an older shared
// install would otherwise trip the -nt freshness check into npm ci, and a
// newer one used to short-circuit the lockfile comparison entirely.
func TestWebPreflightRefusesNpmCiThroughASymlink(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
	}{{"older", -time.Hour}, {"newer", time.Hour}} {
		t.Run(tc.name, func(t *testing.T) {
			frontend, shared := sharedInstallFixture(t, "{\"different\":true}\n", tc.age)
			output, err := runWebPreflight(t, frontend)
			if err == nil {
				t.Fatalf("preflight accepted a mismatched symlinked node_modules; output = %s", output)
			}
			if !strings.Contains(output, "does not match") {
				t.Fatalf("refusal does not say the shared lockfile differs, so the reader cannot act on it; output = %s", output)
			}
			if _, err := os.Stat(filepath.Join(shared, "node_modules")); err != nil {
				t.Fatalf("the shared install was touched despite the refusal: %v", err)
			}
		})
	}
}

// A symlinked shared install built from this worktree's own lockfile is the
// install it wants, so preflight takes it without npm ci: every agent
// worktree builds this way. (The empty shared install then fails the tsc
// health check, which is how the test sees preflight got past the symlink.)
func TestWebPreflightAcceptsASharedInstallWithTheSameLockfile(t *testing.T) {
	frontend, _ := sharedInstallFixture(t, "{}\n", -time.Hour)
	output, err := runWebPreflight(t, frontend)
	if err == nil {
		t.Fatalf("preflight passed an empty shared install; want the tsc health check to fail; output = %s", output)
	}
	if strings.Contains(output, "symlink") {
		t.Fatalf("preflight refused a shared install built from the same lockfile; output = %s", output)
	}
	if !strings.Contains(output, "tsc") {
		t.Fatalf("preflight did not reach the install health check; output = %s", output)
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

// runAPIPackagePreflight runs the real scripts/sdk/api-package-preflight.sh
// against packageDir, a throwaway appwire-client/typescript directory
// (EVENER_API_PACKAGE_DIR), never the real shared install. Every case here is
// decided before the script would reach npm ci.
func runAPIPackagePreflight(t *testing.T, packageDir string) (string, error) {
	t.Helper()
	command := exec.Command("scripts/sdk/api-package-preflight.sh")
	command.Env = append(os.Environ(), "EVENER_API_PACKAGE_DIR="+packageDir)
	output, err := command.CombinedOutput()
	return string(output), err
}

// apiPackageFixture lays out a throwaway appwire-client/typescript directory
// with a lockfile dated into the past, so a node_modules the case adds is newer
// than it and the -nt freshness shortcut skips npm ci.
func apiPackageFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "package-lock.json"), []byte("{}\n"), 0o644)
	backdated := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "package-lock.json"), backdated, backdated); err != nil {
		t.Fatalf("date the lockfile: %v", err)
	}
	return dir
}

// freshNodeModules adds a node_modules to dir dated into the future, so the
// -nt check skips npm ci and the health check is what decides the verdict.
func freshNodeModules(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "node_modules"), future, future); err != nil {
		t.Fatalf("date node_modules: %v", err)
	}
}

// A fresh checkout has no appwire-client/typescript/node_modules, and the
// qualification runner then fails inside npm pack's build with a missing tsc,
// reading as a broken package rather than a missing install. Preflight owns
// that check now, the way web-preflight does for the frontend; the cases here
// pin what each unready state refuses, all without running npm.
func TestAPIPackagePreflightRefusesUnreadyInstalls(t *testing.T) {
	t.Run("missing package-lock.json", func(t *testing.T) {
		dir := t.TempDir()
		freshNodeModules(t, dir)

		output, err := runAPIPackagePreflight(t, dir)
		if err == nil {
			t.Fatalf("preflight accepted an install with no package-lock.json; output = %s", output)
		}
		if !strings.Contains(output, "package-lock.json") {
			t.Fatalf("refusal does not name the missing lockfile; output = %s", output)
		}
	})

	t.Run("symlinked install with a different lockfile", func(t *testing.T) {
		root := t.TempDir()
		work := filepath.Join(root, "appwire-client")
		shared := filepath.Join(root, "shared")
		writeTestFile(t, filepath.Join(work, "package-lock.json"), []byte("{}\n"), 0o644)
		writeTestFile(t, filepath.Join(shared, "package-lock.json"), []byte("{\"different\":true}\n"), 0o644)
		if err := os.MkdirAll(filepath.Join(shared, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(shared, "node_modules"), filepath.Join(work, "node_modules")); err != nil {
			t.Fatalf("symlink node_modules: %v", err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(filepath.Join(shared, "node_modules"), future, future); err != nil {
			t.Fatal(err)
		}

		output, err := runAPIPackagePreflight(t, work)
		if err == nil {
			t.Fatalf("preflight accepted a mismatched symlinked node_modules; output = %s", output)
		}
		if !strings.Contains(output, "does not match") {
			t.Fatalf("refusal does not say the shared lockfile differs; output = %s", output)
		}
		if !strings.Contains(output, "never npm ci through the symlink") {
			t.Fatalf("refusal does not warn that npm ci through the symlink deletes the shared install; output = %s", output)
		}
		if _, err := os.Stat(filepath.Join(shared, "node_modules")); err != nil {
			t.Fatalf("the shared install was touched despite the refusal: %v", err)
		}
	})

	t.Run("empty install newer than the lockfile", func(t *testing.T) {
		dir := apiPackageFixture(t)
		freshNodeModules(t, dir)

		output, err := runAPIPackagePreflight(t, dir)
		if err == nil {
			t.Fatalf("preflight accepted an empty node_modules; output = %s", output)
		}
		if !strings.Contains(output, "tsc") {
			t.Fatalf("refusal does not name the toolchain check that failed; output = %s", output)
		}
	})

	t.Run("install missing ws", func(t *testing.T) {
		dir := apiPackageFixture(t)
		freshNodeModules(t, dir)
		writeTestFile(t, filepath.Join(dir, "node_modules", ".bin", "tsc"), []byte("#!/bin/sh\necho 'Version 5.0.0'\n"), 0o755)

		output, err := runAPIPackagePreflight(t, dir)
		if err == nil {
			t.Fatalf("preflight accepted an install with no ws; output = %s", output)
		}
		if !strings.Contains(output, "ws") {
			t.Fatalf("refusal does not name the missing ws devDependency; output = %s", output)
		}
	})

	t.Run("healthy install", func(t *testing.T) {
		dir := apiPackageFixture(t)
		freshNodeModules(t, dir)
		writeTestFile(t, filepath.Join(dir, "node_modules", ".bin", "tsc"), []byte("#!/bin/sh\necho 'Version 5.0.0'\n"), 0o755)
		writeTestFile(t, filepath.Join(dir, "node_modules", "ws", "package.json"), []byte("{}\n"), 0o644)

		output, err := runAPIPackagePreflight(t, dir)
		if err != nil {
			t.Fatalf("preflight refused a healthy install: %v\n%s", err, output)
		}
		if strings.Contains(output, "ERROR") {
			t.Fatalf("healthy install produced a refusal; output = %s", output)
		}
	})
}

// make test-api-package must run the preflight before the qualification runner,
// so a fresh checkout gets the install named (or the symlink refused) rather
// than a tsc-not-found error inside npm pack. make -n prints the plan; the
// preflight target has no recipe that re-enters make.
func TestMakeTestAPIPackageRunsThePreflight(t *testing.T) {
	output := makeDryRun(t, "test-api-package")
	preflight := strings.Index(output, "api-package-preflight.sh")
	qualify := strings.Index(output, "npm run qualification")
	if preflight == -1 || qualify == -1 {
		t.Fatalf("make -n test-api-package does not run the preflight (index %d) and the qualification runner (index %d); output = %s", preflight, qualify, output)
	}
	if preflight > qualify {
		t.Fatalf("make -n test-api-package runs the qualification runner before the preflight; output = %s", output)
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
