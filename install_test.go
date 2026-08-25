package evener_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	agentplugin "primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/rendezvous"
)

func TestWebPreflightBootstrapsMissingFrontendDependencies(t *testing.T) {
	t.Parallel()

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	fixtureRoot := t.TempDir()
	frontendDir := filepath.Join(fixtureRoot, "cmd", "evener-hub", "frontend")
	if err := os.MkdirAll(frontendDir, 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}

	copyMakefileSources(t, repoRoot, fixtureRoot)
	copyRepositoryFile(t, repoRoot, fixtureRoot, "scripts/web/web-preflight.sh", 0o755)
	if err := os.WriteFile(filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}

	env := installTestEnv(t, t.TempDir(), nil)
	runCommand(t, fixtureRoot, npmShimEnv(t, env), "make", "web-preflight")

	tscPath := filepath.Join(frontendDir, "node_modules", ".bin", "tsc")
	tscInfo, err := os.Stat(tscPath)
	if err != nil {
		t.Fatalf("preflight did not install local tsc: %v", err)
	}
	if tscInfo.Mode().Perm()&0o111 == 0 {
		t.Fatalf("local tsc is not executable: mode %s", tscInfo.Mode())
	}
}

func TestNpmShimRejectsUnsupportedCommand(t *testing.T) {
	t.Parallel()

	env := npmShimEnv(t, installTestEnv(t, t.TempDir(), nil))
	var npmPath string
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if !ok || name != "PATH" {
			continue
		}
		parts := strings.Split(value, string(os.PathListSeparator))
		if len(parts) == 0 || parts[0] == "" {
			t.Fatal("npm shim PATH is empty")
		}
		npmPath = filepath.Join(parts[0], "npm")
		break
	}
	if npmPath == "" {
		t.Fatal("npm shim PATH was not configured")
	}

	_, err := combinedOutputRetryingETXTBSY("", env, npmPath, "install")
	if err == nil {
		t.Fatal("npm shim accepted unsupported command")
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("npm shim did not execute as a process: %v", err)
		}
		if got := exitErr.ExitCode(); got != 2 {
			t.Fatalf("npm shim exit code = %d, want 2", got)
		}
	}
}

func TestInstallHomeGeneratedHome(t *testing.T) {
	if testing.Short() {
		t.Skip("install integration test")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	fixtureRoot := copyTrackedWorkingTree(t, repoRoot)
	sourceNodeModulesPath := filepath.Join(repoRoot, "cmd", "evener-hub", "frontend", "node_modules")
	sourceNodeModulesBefore := fingerprintInstallTree(t, sourceNodeModulesPath)
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	stateHome := filepath.Join(home, ".local", "state")
	cacheHome := filepath.Join(home, ".cache")
	env := installTestEnv(t, home, map[string]string{
		"XDG_CONFIG_HOME": configHome,
		"XDG_STATE_HOME":  stateHome,
		"XDG_CACHE_HOME":  cacheHome,
	})

	runCommand(t, fixtureRoot, npmShimEnv(t, env), "make", "install")

	binDir := filepath.Join(home, ".local", "bin")
	shareBinDir := filepath.Join(home, ".local", "share", "evener", "bin")
	for _, bin := range []string{"evener", "evener-dev"} {
		installed := filepath.Join(shareBinDir, bin)
		info, err := os.Stat(installed)
		if err != nil {
			t.Fatalf("installed binary %s: %v", installed, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed binary %s is not executable: mode %s", installed, info.Mode())
		}

		link := filepath.Join(binDir, bin)
		linkInfo, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("installed link %s: %v", link, err)
		}
		if linkInfo.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("installed entry %s is not a symlink: mode %s", link, linkInfo.Mode())
		}
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink %s: %v", link, err)
		}
		if target != installed {
			t.Fatalf("symlink %s -> %s, want %s", link, target, installed)
		}
	}

	if exists(filepath.Join(configHome, "evener")) {
		t.Fatalf("install created %s; Evener should create config dirs on first run", filepath.Join(configHome, "evener"))
	}
	if exists(filepath.Join(home, ".evener")) {
		t.Fatalf("install created %s; Evener should create runtime dirs on first run", filepath.Join(home, ".evener"))
	}

	evenerBin := filepath.Join(binDir, "evener")
	runCommand(t, fixtureRoot, env, evenerBin, "--version")
	runCommand(t, fixtureRoot, env, evenerBin, "hub", "--help")
	runCommand(t, fixtureRoot, env, evenerBin, "tui", "--help")
	runCommand(t, fixtureRoot, env, evenerBin, "doctor", "help")
	runCommand(t, fixtureRoot, env, evenerBin, "migrate", "--dry-run")
	runCommand(t, fixtureRoot, env, evenerBin, "--list-sessions")

	for _, dir := range []string{
		filepath.Join(configHome, "evener", "skills"),
		filepath.Join(configHome, "evener", "plugins"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("first Evener run did not create %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}
	}
	if exists(filepath.Join(home, ".evener")) {
		t.Fatalf("evener --list-sessions created %s; no hub runtime state should be needed", filepath.Join(home, ".evener"))
	}

	expectedAgents := expectedBundledAgents(t, repoRoot)
	expectedSkills := expectedBundledSkills(t, repoRoot)
	status := installedServeStatus(t, fixtureRoot, env, evenerBin)

	if status.Detailed == nil {
		t.Fatal("installed evener serve /status omitted detailed status")
	}
	installedSkillNames := status.Detailed.SkillNames()
	assertContainsAll(t, "bundled agents", status.Detailed.Agents, expectedAgents)
	assertSameSet(t, "bundled skills", installedSkillNames, expectedSkills)

	sourceNodeModulesAfter := fingerprintInstallTree(t, sourceNodeModulesPath)
	if sourceNodeModulesBefore != sourceNodeModulesAfter {
		t.Fatalf("install mutated source checkout node_modules: before=%+v after=%+v", sourceNodeModulesBefore, sourceNodeModulesAfter)
	}
}

// The node_modules guard above must not fire on vite's own cache churn:
// vite/vitest rewrite node_modules/.vite and .vite-temp on every run, and
// with several worktrees symlinking one real install, a concurrent vitest
// anywhere flips those bytes between the guard's two fingerprints. The guard
// exists to catch the INSTALL mutating the checkout; vite's scratch space
// cannot witness that, so it stays outside the digest.
func TestFingerprintInstallTreeIgnoresViteCacheChurn(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	pkgDir := filepath.Join(tree, "somepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTreeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTreeFile(filepath.Join(pkgDir, "index.js"), "module.exports = 1\n")
	writeTreeFile(filepath.Join(tree, ".vite", "deps", "chunk.js"), "cache v1")

	before := fingerprintInstallTree(t, tree)

	writeTreeFile(filepath.Join(tree, ".vite", "deps", "chunk.js"), "cache v2")
	writeTreeFile(filepath.Join(tree, ".vite-temp", "scratch.js"), "in flight")
	if after := fingerprintInstallTree(t, tree); before != after {
		t.Fatalf("vite cache churn changed the node_modules fingerprint: before=%+v after=%+v", before, after)
	}

	writeTreeFile(filepath.Join(pkgDir, "index.js"), "module.exports = 2\n")
	if after := fingerprintInstallTree(t, tree); before == after {
		t.Fatal("a real package mutation no longer changes the node_modules fingerprint")
	}
}

func TestInstallScriptInstallsReleaseArchive(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	for _, tc := range []struct {
		name     string
		osName   string
		archName string
		asset    string
	}{
		{
			name:     "linux amd64",
			osName:   "Linux",
			archName: "x86_64",
			asset:    "evener_linux_amd64.tar.gz",
		},
		{
			name:     "darwin arm64",
			osName:   "Darwin",
			archName: "arm64",
			asset:    "evener_darwin_arm64.tar.gz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			fixtures := t.TempDir()
			archive := filepath.Join(fixtures, tc.asset)
			writeInstallReleaseArchive(t, archive, strings.TrimSuffix(tc.asset, ".tar.gz"))

			fakeBin, urlFile := writeInstallScriptFakeBin(t, fixtures, tc.osName, tc.archName)

			env := installTestEnv(t, home, map[string]string{
				"PATH":                      fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"EVENER_INSTALL_VERSION":    "v1.2.3",
				"EVENER_FAKE_CURL_ARCHIVE":  archive,
				"EVENER_FAKE_CURL_URL_FILE": urlFile,
			})
			runCommand(t, repoRoot, env, "sh", script)

			expectedURL := "https://github.com/prime-radiant-inc/evener/releases/download/v1.2.3/" + tc.asset
			data, err := os.ReadFile(urlFile)
			if err != nil {
				t.Fatalf("read fake curl URL: %v", err)
			}
			urls := strings.Split(strings.TrimSpace(string(data)), "\n")
			wantURLs := []string{expectedURL, "https://github.com/prime-radiant-inc/evener/releases/download/v1.2.3/checksums.txt"}
			if len(urls) != 2 || urls[0] != wantURLs[0] || urls[1] != wantURLs[1] {
				t.Fatalf("download URLs = %q, want %q", urls, wantURLs)
			}

			binDir := filepath.Join(home, ".local", "bin")
			shareBinDir := filepath.Join(home, ".local", "share", "evener", "bin")
			for _, bin := range []string{"evener", "evener-dev"} {
				installed := filepath.Join(shareBinDir, bin)
				info, err := os.Stat(installed)
				if err != nil {
					t.Fatalf("installed binary %s: %v", installed, err)
				}
				if info.Mode().Perm()&0o111 == 0 {
					t.Fatalf("installed binary %s is not executable: mode %s", installed, info.Mode())
				}

				link := filepath.Join(binDir, bin)
				target, err := os.Readlink(link)
				if err != nil {
					t.Fatalf("readlink %s: %v", link, err)
				}
				if target != installed {
					t.Fatalf("symlink %s -> %s, want %s", link, target, installed)
				}
			}
		})
	}
}

func TestInstallScriptPreservesDownloadFailuresAndClassifies404(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	for _, tc := range []struct {
		name       string
		version    string
		httpStatus string
		curlExit   string
		wantExit   int
		wantAdvice bool
	}{
		{name: "latest 404", version: "latest", httpStatus: "404", curlExit: "22", wantExit: 22, wantAdvice: true},
		{name: "latest 401", version: "latest", httpStatus: "401", curlExit: "22", wantExit: 22},
		{name: "latest 403", version: "latest", httpStatus: "403", curlExit: "22", wantExit: 22},
		{name: "latest 429", version: "latest", httpStatus: "429", curlExit: "22", wantExit: 22},
		{name: "latest 500", version: "latest", httpStatus: "500", curlExit: "22", wantExit: 22},
		{name: "latest DNS failure", version: "latest", httpStatus: "000", curlExit: "6", wantExit: 6},
		{name: "latest TLS failure", version: "latest", httpStatus: "000", curlExit: "35", wantExit: 35},
		{name: "latest redirect failure", version: "latest", httpStatus: "000", curlExit: "47", wantExit: 47},
		{name: "latest receive failure", version: "latest", httpStatus: "000", curlExit: "56", wantExit: 56},
		{name: "pinned 404", version: "v1.2.3", httpStatus: "404", curlExit: "22", wantExit: 22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, out, runErr := runInstallScript(t, script, "Darwin", "arm64", map[string]string{
				"EVENER_INSTALL_VERSION":     tc.version,
				"EVENER_FAKE_CURL_HTTP_CODE": tc.httpStatus,
				"EVENER_FAKE_CURL_EXIT":      tc.curlExit,
			})
			if runErr == nil {
				t.Fatalf("install succeeded; output = %s", out)
			}
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run error is not an exit error: %v", runErr)
			}
			if got := exitErr.ExitCode(); got != tc.wantExit {
				t.Fatalf("exit status = %d, want %d; output = %s", got, tc.wantExit, out)
			}
			gotAdvice := strings.Contains(out, "EVENER_INSTALL_VERSION=snapshot")
			if gotAdvice != tc.wantAdvice {
				t.Fatalf("snapshot advice = %v, want %v; output = %s", gotAdvice, tc.wantAdvice, out)
			}
			assertNothingInstalled(t, home, out)
		})
	}
}

// TestInstallScriptRejectsTamperedArchive feeds install.sh a checksums.txt
// whose digest cannot match the served archive: the install must fail closed
// at verification, before anything is installed.
// TestInstallScriptReportsMissingLatestReleaseAsset pins install.sh's
// behavior when the default ("latest") download 404s: the documented
// quickstart one-liner sets no EVENER_INSTALL_VERSION, so a release whose
// "latest" tag has no matching evener_<os>_<arch>.tar.gz asset must fail
// with an actionable message pointing at EVENER_INSTALL_VERSION=snapshot,
// not a bare curl error.
func TestInstallScriptReportsMissingLatestReleaseAsset(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	home, out, runErr := runInstallScript(t, script, "Darwin", "arm64", map[string]string{
		"EVENER_INSTALL_VERSION": "latest",
		"EVENER_FAKE_CURL_404":   "evener_darwin_arm64.tar.gz",
	})
	if runErr == nil {
		t.Fatalf("install succeeded despite a 404 on the release asset; output = %s", out)
	}
	if !strings.Contains(out, "Failed to download") {
		t.Fatalf("failure does not report the download error; output = %s", out)
	}
	if !strings.Contains(out, "EVENER_INSTALL_VERSION=snapshot") {
		t.Fatalf("failure does not point at the snapshot workaround; output = %s", out)
	}
	assertNothingInstalled(t, home, out)
}

// TestInstallScriptReportsMissingPinnedReleaseAsset pins that a 404 against
// an explicitly pinned version (not "latest") gets the download-failure
// message without the "latest" nudge — pointing at snapshot would be wrong
// advice when the user already asked for a specific tag.
func TestInstallScriptReportsMissingPinnedReleaseAsset(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	home, out, runErr := runInstallScript(t, script, "Darwin", "arm64", map[string]string{
		"EVENER_INSTALL_VERSION": "v0.1.0",
		"EVENER_FAKE_CURL_404":   "evener_darwin_arm64.tar.gz",
	})
	if runErr == nil {
		t.Fatalf("install succeeded despite a 404 on the release asset; output = %s", out)
	}
	if !strings.Contains(out, "Failed to download") {
		t.Fatalf("failure does not report the download error; output = %s", out)
	}
	if strings.Contains(out, "EVENER_INSTALL_VERSION=snapshot") {
		t.Fatalf("pinned-version failure should not suggest snapshot; output = %s", out)
	}
	assertNothingInstalled(t, home, out)
}

func TestInstallScriptRejectsTamperedArchive(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	home, out, runErr := runInstallScript(t, script, "Darwin", "arm64", map[string]string{
		"EVENER_FAKE_CURL_BAD_SHA": "1",
	})
	if runErr == nil {
		t.Fatalf("install succeeded against a tampered checksum; output = %s", out)
	}
	if !strings.Contains(out, "Checksum verification failed") {
		t.Fatalf("failure does not name checksum verification; output = %s", out)
	}
	assertNothingInstalled(t, home, out)
}

// The verification step used to pipe grep straight into the checksum tool.
// A pipeline reports its last command's status, and macOS's /sbin/sha256sum
// exits 0 on empty input, so a checksums.txt with no line for this archive —
// exactly what the live snapshot channel serves, because it writes
// "dist/<name>" and the grep anchored on a bare name — read as "verified" and
// installed the archive unchecked. Every non-unique match must now be a hard
// failure before the checksum tool sees anything.
func TestInstallScriptRejectsChecksumsItCannotMatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	for _, tc := range []struct {
		name    string
		knobs   map[string]string
		wantMsg string
	}{
		{
			name:    "no entry for this archive",
			knobs:   map[string]string{"EVENER_FAKE_CURL_SUM_NAME": "evener_linux_amd64.tar.gz"},
			wantMsg: "has no entry for evener_darwin_arm64.tar.gz",
		},
		{
			name:    "entry hidden behind an unexpected path prefix",
			knobs:   map[string]string{"EVENER_FAKE_CURL_SUM_PREFIX": "build/artifacts/"},
			wantMsg: "has no entry for evener_darwin_arm64.tar.gz",
		},
		{
			name:    "two entries for the same archive",
			knobs:   map[string]string{"EVENER_FAKE_CURL_SUM_DUPLICATE": "1"},
			wantMsg: "has more than one entry for evener_darwin_arm64.tar.gz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, out, runErr := runInstallScript(t, script, "Darwin", "arm64", tc.knobs)
			if runErr == nil {
				t.Fatalf("install succeeded against an unverifiable checksums.txt; output = %s", out)
			}
			if !strings.Contains(out, tc.wantMsg) {
				t.Fatalf("output does not report %q; output = %s", tc.wantMsg, out)
			}
			assertNothingInstalled(t, home, out)
		})
	}
}

// The snapshot channel published today lists "dist/<name>"; goreleaser lists
// the bare name. install.sh has to verify against both, or the transition
// breaks every install.
func TestInstallScriptVerifiesDistPrefixedChecksums(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	home, out, runErr := runInstallScript(t, script, "Darwin", "arm64", map[string]string{
		"EVENER_FAKE_CURL_SUM_PREFIX": "dist/",
	})
	if runErr != nil {
		t.Fatalf("install failed against dist/-prefixed checksums: %v; output = %s", runErr, out)
	}
	if !strings.Contains(out, "evener_darwin_arm64.tar.gz: OK") {
		t.Fatalf("output does not show the archive being verified; output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "evener", "bin", "evener")); err != nil {
		t.Fatalf("evener was not installed: %v; output = %s", err, out)
	}
}

// runInstallScript drives install.sh against fake uname/curl for the given
// platform, with extra environment knobs layered on, and returns the HOME it
// installed into alongside the combined output and the run error.
func runInstallScript(t *testing.T, script, osName, archName string, knobs map[string]string) (home, out string, runErr error) {
	t.Helper()

	home = t.TempDir()
	fixtures := t.TempDir()
	root := installArchiveRoot(osName, archName)
	archive := filepath.Join(fixtures, root+".tar.gz")
	writeInstallReleaseArchive(t, archive, root)
	fakeBin, urlFile := writeInstallScriptFakeBin(t, fixtures, osName, archName)

	overrides := map[string]string{
		"PATH":                      fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"EVENER_INSTALL_VERSION":    "v1.2.3",
		"EVENER_FAKE_CURL_ARCHIVE":  archive,
		"EVENER_FAKE_CURL_URL_FILE": urlFile,
	}
	maps.Copy(overrides, knobs)

	cmd := exec.Command("sh", script)
	cmd.Env = installTestEnv(t, home, overrides)
	combined, runErr := cmd.CombinedOutput()
	return home, string(combined), runErr
}

// installArch maps a `uname -m` answer to the release archive's arch token.
// installArchiveRoot composes the archive's directory name (and, with
// ".tar.gz", the asset name) the same way install.sh does. Both live here
// rather than in install_fuzz_test.go so the untagged tests can use them too.
func installArch(arch string) string {
	if arch == "x86_64" {
		return "amd64"
	}
	return arch
}

func installArchiveRoot(osName, arch string) string {
	return "evener_" + strings.ToLower(osName) + "_" + installArch(arch)
}

func assertNothingInstalled(t *testing.T, home, out string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "evener", "bin", "evener")); !os.IsNotExist(err) {
		t.Fatalf("a binary was installed despite the failed verification; stat err = %v; output = %s", err, out)
	}
}

// writeInstallScriptFakeBin writes fake `uname` and `curl` executables into a
// fixtures/bin directory so install.sh can run without touching the real
// network or the host's actual OS/arch, and returns that directory alongside
// the file curl records the download URL into. Shared by every test that
// drives install.sh directly.
func writeInstallScriptFakeBin(t *testing.T, fixtures, osName, archName string) (fakeBin, urlFile string) {
	t.Helper()

	fakeBin = filepath.Join(fixtures, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "uname"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  -s) echo %q ;;
  -m) echo %q ;;
  *) echo "unsupported uname args: $*" >&2; exit 2 ;;
esac
`, osName, archName))

	urlFile = filepath.Join(fixtures, "curl-url")
	writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
out=
url=
write_format=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) write_format="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
if [ -z "$out" ]; then
  echo "missing -o" >&2
  exit 2
fi
printf '%s\n' "$url" >> "$EVENER_FAKE_CURL_URL_FILE"
http_code=${EVENER_FAKE_CURL_HTTP_CODE:-200}
if [ -n "${EVENER_FAKE_CURL_404:-}" ] && [ "$(basename "$url")" = "$EVENER_FAKE_CURL_404" ]; then
  http_code=404
fi
if [ -n "$write_format" ]; then
  printf '%s' "$http_code"
fi
if [ -n "${EVENER_FAKE_CURL_EXIT:-}" ]; then
  cp "$EVENER_FAKE_CURL_ARCHIVE" "$out"
  exit "$EVENER_FAKE_CURL_EXIT"
fi
if [ -n "${EVENER_FAKE_CURL_404:-}" ] && [ "$(basename "$url")" = "$EVENER_FAKE_CURL_404" ]; then
  exit 22
fi
case "$(basename "$url")" in
  checksums.txt)
    # Serve the archive's real digest so verification passes; the tamper knob
    # serves a digest that cannot match.
    if [ -n "${EVENER_FAKE_CURL_BAD_SHA:-}" ]; then
      sum="0000000000000000000000000000000000000000000000000000000000000000"
    elif command -v sha256sum >/dev/null 2>&1; then
      sum=$(sha256sum "$EVENER_FAKE_CURL_ARCHIVE" | cut -d' ' -f1)
    else
      sum=$(shasum -a 256 "$EVENER_FAKE_CURL_ARCHIVE" | cut -d' ' -f1)
    fi
    # EVENER_FAKE_CURL_SUM_PREFIX reproduces the path prefix a publisher may put
    # on the artifact name ("dist/" — what the hand-rolled channel wrote and the
    # live snapshot still carries); EVENER_FAKE_CURL_SUM_NAME serves an entry for
    # some other artifact, so no line matches at all.
    name=${EVENER_FAKE_CURL_SUM_NAME:-$(basename "$EVENER_FAKE_CURL_ARCHIVE")}
    printf '%s  %s%s\n' "$sum" "${EVENER_FAKE_CURL_SUM_PREFIX:-}" "$name" > "$out"
    if [ -n "${EVENER_FAKE_CURL_SUM_DUPLICATE:-}" ]; then
      printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$name" >> "$out"
    fi
    ;;
  *)
    cp "$EVENER_FAKE_CURL_ARCHIVE" "$out"
    ;;
esac
`)
	return fakeBin, urlFile
}

// TestInstallScriptWarnsAboutLegacySerf pins install.sh's completion-message
// nudge toward evener migrate: a machine with an existing ~/.serf or interim
// ~/.evener gets told to migrate before first launch, and a clean machine
// gets no such nudge.
func TestInstallScriptWarnsAboutLegacySerf(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("release archive install integration test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("install.sh requires a Unix shell")
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	script := filepath.Join(repoRoot, "install.sh")

	for _, tc := range []struct {
		name        string
		seedLegacy  bool
		seedInterim bool
		wantMessage bool
	}{
		{name: "legacy serf present", seedLegacy: true, wantMessage: true},
		{name: "interim evener present", seedInterim: true, wantMessage: true},
		{name: "clean machine", wantMessage: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if tc.seedLegacy {
				if err := os.MkdirAll(filepath.Join(home, ".serf"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.seedInterim {
				if err := os.MkdirAll(filepath.Join(home, ".evener"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			fixtures := t.TempDir()
			archive := filepath.Join(fixtures, "evener_darwin_arm64.tar.gz")
			writeInstallReleaseArchive(t, archive, "evener_darwin_arm64")
			fakeBin, urlFile := writeInstallScriptFakeBin(t, fixtures, "Darwin", "arm64")

			env := installTestEnv(t, home, map[string]string{
				"PATH":                      fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"EVENER_INSTALL_VERSION":    "v1.2.3",
				"EVENER_FAKE_CURL_ARCHIVE":  archive,
				"EVENER_FAKE_CURL_URL_FILE": urlFile,
			})

			cmd := exec.Command("sh", script)
			cmd.Dir = repoRoot
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("install.sh failed: %v\n%s", err, out)
			}

			output := string(out)
			gotMessage := strings.Contains(output, "evener migrate")
			if gotMessage != tc.wantMessage {
				t.Fatalf("evener migrate mention in output = %v, want %v\noutput:\n%s", gotMessage, tc.wantMessage, out)
			}
			if tc.wantMessage {
				if !strings.Contains(output, "--dry-run") {
					t.Fatalf("legacy migration guidance omits --dry-run:\n%s", output)
				}
				if strings.Contains(output, "README.md") {
					t.Fatalf("legacy migration guidance points at stale README.md documentation:\n%s", output)
				}
			}
		})
	}
}

func installedServeStatus(t *testing.T, repoRoot string, baseEnv []string, evenerBin string) installedStatus {
	t.Helper()

	runDir := t.TempDir()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	providersPath := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(providersPath, []byte(`
schema = 1
default = "work"

[instances.work]
type = "openai"
api_style = "responses"
api_key = "sk-install-test"
`), 0o600); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}

	env := overlayEnv(baseEnv, map[string]string{
		"EVENER_PROVIDERS_CONFIG": providersPath,
		"EVENER_HUB_TOKEN":        "",
	})

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(evenerBin,
		"serve",
		"--model", "work/gpt-5.2",
		"--addr", "127.0.0.1:0",
		"--dir", workDir,
		"--run-dir", runDir,
		"--state-dir", stateDir,
		"--no-project-prompts",
	)
	cmd.Dir = repoRoot
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start installed evener serve: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var entry rendezvous.Entry
	started := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("installed evener serve exited before rendezvous entry: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		default:
		}

		entries, err := rendezvous.List(runDir)
		if err != nil {
			t.Fatalf("list rendezvous entries: %v", err)
		}
		if len(entries) > 0 && entries[0].Address != "" {
			entry = entries[0]
			started = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("installed evener serve did not start\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
		if err == nil {
			_ = resp.Body.Close()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + entry.Address + "/status")
	if err != nil {
		t.Fatalf("get installed evener serve /status: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", resp.StatusCode)
	}

	var status installedStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode /status: %v", err)
	}
	return status
}

type installedStatus struct {
	Detailed *installedDetailedStatus `json:"detailed"`
}

type installedDetailedStatus struct {
	Agents []string             `json:"agents"`
	Skills []installedSkillInfo `json:"skills"`
}

type installedSkillInfo struct {
	Name string `json:"name"`
}

func (s installedDetailedStatus) SkillNames() []string {
	names := make([]string, 0, len(s.Skills))
	for _, meta := range s.Skills {
		names = append(names, meta.Name)
	}
	return names
}

func expectedBundledAgents(t *testing.T, repoRoot string) []string {
	t.Helper()

	dir := filepath.Join(repoRoot, "internal", "bundled", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundled agents: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read bundled agent %s: %v", entry.Name(), err)
		}
		agent, err := agentplugin.ParseAgent(data, "builtin")
		if err != nil {
			t.Fatalf("parse bundled agent %s: %v", entry.Name(), err)
		}
		names = append(names, agent.Name)
	}
	sort.Strings(names)
	return names
}

func expectedBundledSkills(t *testing.T, repoRoot string) []string {
	t.Helper()

	metas := map[string]skill.SkillMeta{}
	skill.ScanSkillsDir(filepath.Join(repoRoot, "internal", "bundled", "skills"), metas)
	names := make([]string, 0, len(metas))
	for name := range metas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func assertContainsAll(t *testing.T, label string, got, want []string) {
	t.Helper()

	gotSet := make(map[string]bool, len(got))
	for _, name := range got {
		gotSet[name] = true
	}
	var missing []string
	for _, name := range want {
		if !gotSet[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(got)
		t.Fatalf("%s missing %v; got %v", label, missing, got)
	}
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()

	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

type installTreeFingerprint struct {
	exists bool
	mode   fs.FileMode
	digest [sha256.Size]byte
}

func fingerprintInstallTree(t *testing.T, root string) installTreeFingerprint {
	t.Helper()

	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return installTreeFingerprint{}
	}
	if err != nil {
		t.Fatalf("stat install tree %s: %v", root, err)
	}

	fingerprint := installTreeFingerprint{exists: true, mode: info.Mode()}
	if info.IsDir() {
		hash := sha256.New()
		hashInstallTree(t, hash, root, "")
		copy(fingerprint.digest[:], hash.Sum(nil))
		return fingerprint
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(root)
		if err != nil {
			t.Fatalf("read install tree symlink %s: %v", root, err)
		}
		fingerprint.digest = sha256.Sum256([]byte(target))
		return fingerprint
	}
	data, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read install tree %s: %v", root, err)
	}
	fingerprint.digest = sha256.Sum256(data)
	return fingerprint
}

func hashInstallTree(t *testing.T, hash interface{ Write([]byte) (int, error) }, root, relative string) {
	t.Helper()

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read install tree %s: %v", root, err)
	}
	for _, entry := range entries {
		// vite's dep-optimizer cache is rewritten by every vite/vitest run,
		// and worktrees symlink one shared node_modules — so a concurrent run
		// anywhere flips these bytes between two fingerprints. They cannot
		// witness an install mutating the checkout, which is the only thing
		// this digest exists to catch.
		if relative == "" && (entry.Name() == ".vite" || entry.Name() == ".vite-temp") {
			continue
		}
		entryPath := filepath.Join(root, entry.Name())
		entryRelative := filepath.ToSlash(filepath.Join(relative, entry.Name()))
		info, err := os.Lstat(entryPath)
		if err != nil {
			t.Fatalf("stat install tree entry %s: %v", entryPath, err)
		}

		_, _ = hash.Write([]byte("entry\x00" + entryRelative + "\x00" + info.Mode().String() + "\x00"))
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(entryPath)
			if err != nil {
				t.Fatalf("read install tree symlink %s: %v", entryPath, err)
			}
			_, _ = hash.Write([]byte(target))
		case info.IsDir():
			hashInstallTree(t, hash, entryPath, entryRelative)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(entryPath)
			if err != nil {
				t.Fatalf("read install tree entry %s: %v", entryPath, err)
			}
			_, _ = hash.Write(data)
		default:
			t.Fatalf("unsupported install tree entry %s: mode %s", entryPath, info.Mode())
		}
		_, _ = hash.Write([]byte{0})
	}
}

func copyTrackedWorkingTree(t *testing.T, repoRoot string) string {
	t.Helper()

	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("make repository root absolute: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve repository root %s: %v", root, err)
	}

	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("list tracked working-tree files: %v", err)
	}
	parts := bytes.Split(output, []byte{0})
	if len(parts) == 0 || len(parts[len(parts)-1]) != 0 {
		t.Fatalf("git ls-files output is not NUL terminated")
	}
	parts = parts[:len(parts)-1]
	sort.Slice(parts, func(i, j int) bool { return string(parts[i]) < string(parts[j]) })

	fixtureRoot := t.TempDir()
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		relative := string(part)
		clean := path.Clean(relative)
		if relative == "" || path.IsAbs(relative) || clean != relative || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(relative, "\\") {
			t.Fatalf("unsafe tracked path %q", relative)
		}
		if _, ok := seen[relative]; ok {
			t.Fatalf("duplicate tracked path %q", relative)
		}
		seen[relative] = struct{}{}

		sourcePath := filepath.Join(root, filepath.FromSlash(relative))
		sourceInfo, err := os.Lstat(sourcePath)
		if err != nil {
			t.Fatalf("stat tracked working-tree file %s: %v", relative, err)
		}
		if sourceInfo.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("tracked working-tree symlink is not supported: %s", relative)
		}
		if !sourceInfo.Mode().IsRegular() {
			t.Fatalf("tracked working-tree entry is not a regular file: %s mode %s", relative, sourceInfo.Mode())
		}
		resolvedSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			t.Fatalf("resolve tracked working-tree file %s: %v", relative, err)
		}
		withinRoot, err := filepath.Rel(resolvedRoot, resolvedSource)
		if err != nil || filepath.IsAbs(withinRoot) || withinRoot == ".." || strings.HasPrefix(withinRoot, ".."+string(os.PathSeparator)) {
			t.Fatalf("tracked working-tree path escapes repository root: %s", relative)
		}

		destinationPath := filepath.Join(fixtureRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			t.Fatalf("make fixture parent for %s: %v", relative, err)
		}
		if _, err := os.Lstat(destinationPath); err == nil {
			t.Fatalf("fixture destination already exists: %s", relative)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat fixture destination %s: %v", relative, err)
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read tracked working-tree file %s: %v", relative, err)
		}
		if err := os.WriteFile(destinationPath, data, sourceInfo.Mode().Perm()); err != nil {
			t.Fatalf("copy tracked working-tree file %s: %v", relative, err)
		}
		if err := os.Chmod(destinationPath, sourceInfo.Mode().Perm()); err != nil {
			t.Fatalf("preserve mode for tracked working-tree file %s: %v", relative, err)
		}
	}
	return fixtureRoot
}

func runCommand(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()

	out, err := combinedOutputRetryingETXTBSY(dir, env, name, args...)
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// combinedOutputRetryingETXTBSY runs the command and returns its combined
// output, retrying while the exec fails with ETXTBSY. Tests in this package
// exec binaries they wrote moments earlier (the npm shim, and scripts the
// shim writes); any forked child of the test binary inherits the whole
// descriptor table, so a sibling forked between our write and close briefly
// holds a writable fd to the freshly written file and the kernel refuses to
// exec it — golang/go#22315. The condition clears as soon as that child
// reaches its own execve, so a short bounded retry (the same treatment
// cmd/go gives test binaries) keeps the exec honest: non-ETXTBSY failures
// return immediately and the caller still sees a real exec failure.
//
// The grandchild case is matched textually: when the freshly written file is
// exec'd by a shell spawned by make, the ETXTBSY surfaces only as the shell's
// "Text file busy" diagnostic in the combined output, never as the errno.
func combinedOutputRetryingETXTBSY(dir string, env []string, name string, args ...string) ([]byte, error) {
	var out []byte
	var err error
	for range 50 {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err = cmd.CombinedOutput()
		if !isETXTBSYExecFailure(err, out) {
			return out, err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return out, err
}

func isETXTBSYExecFailure(err error, out []byte) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ETXTBSY) {
		return true
	}
	return bytes.Contains(bytes.ToLower(out), []byte("text file busy"))
}

// npmShimEnv prepends a fake-bin directory containing a network-free npm to
// env's PATH, for the `make install` invocation above only. install depends on
// build-web (Makefile-coherence rule: a shipped/installed hub must embed a
// fresh SPA, never the tracked PLACEHOLDER), which would otherwise run a real
// npm ci + vite build inside this test — slow, and it would fail entirely in
// environments without node. This test's subject is install's layout/symlinks,
// not web freshness; that is pinned separately by
// TestMakeRuntimeAliasesBuildThePair in runtime_pair_build_test.go. The shim
// models only the local compiler contract that web-preflight checks: npm ci
// creates an executable tsc stub, while npm run build remains a no-op and
// leaves dist exactly as-is. The real go/git must still resolve from the rest
// of PATH, so this only prepends.
func npmShimEnv(t *testing.T, env []string) []string {
	t.Helper()

	fakeBin := t.TempDir()
	const npmShim = `#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = "ci" ]; then
  mkdir -p node_modules/.bin
  printf '#!/bin/sh\necho "Version 6.0.3"\n' > node_modules/.bin/tsc
  chmod +x node_modules/.bin/tsc
elif [ "$#" -eq 2 ] && [ "$1" = "run" ] && [ "$2" = "build" ]; then
  :
else
  echo "unsupported npm args: $*" >&2
  exit 2
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeBin, "npm"), []byte(npmShim), 0o755); err != nil {
		t.Fatalf("write fake npm: %v", err)
	}

	path := os.Getenv("PATH")
	for _, item := range env {
		if name, value, ok := strings.Cut(item, "="); ok && name == "PATH" {
			path = value
			break
		}
	}
	return overlayEnv(env, map[string]string{
		"PATH": fakeBin + string(os.PathListSeparator) + path,
	})
}

func writeInstallReleaseArchive(t *testing.T, path, root string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create release archive: %v", err)
	}
	defer file.Close()

	gz := gzip.NewWriter(file)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	for _, bin := range []string{"evener", "evener-dev"} {
		body := fmt.Sprintf("#!/bin/sh\necho %s\n", bin)
		header := &tar.Header{
			Name: filepath.ToSlash(filepath.Join(root, bin)),
			Mode: 0o755,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()

	// A freshly written executable can fail execve with ETXTBSY: os.WriteFile
	// holds the file open for writing, and a sibling parallel test that forks for
	// its own os/exec in that window leaves the child holding the write fd until it
	// execs. syscall.ForkLock is the standard guard — fork/exec takes it for
	// writing, so holding it for reading across the write excludes any concurrent
	// fork. See Go issue #22315. (These tests are -short-skipped, but the full
	// integration suite runs them in parallel.)
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func installTestEnv(t *testing.T, home string, extra map[string]string) []string {
	t.Helper()

	overrides := map[string]string{
		"HOME":       home,
		"GOMODCACHE": goEnv(t, "GOMODCACHE"),
		"GOCACHE":    goEnv(t, "GOCACHE"),
		"GOPATH":     goEnv(t, "GOPATH"),
		// The Makefile stages binaries through INSTALL_BUILD_DIR, which
		// defaults to .build/install. The install integration test runs from
		// a temporary tracked-working-tree fixture, so this explicit home
		// path keeps its staging output isolated from other test processes.
		"INSTALL_BUILD_DIR": filepath.Join(home, "install-build"),
	}
	maps.Copy(overrides, extra)
	return overlayEnv(os.Environ(), overrides)
}

func goEnv(t *testing.T, key string) string {
	t.Helper()

	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		t.Fatalf("go env %s returned empty value", key)
	}
	return value
}

func overlayEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, override := overrides[key]; override {
			continue
		}
		out = append(out, item)
	}
	for key, value := range overrides {
		out = append(out, fmt.Sprintf("%s=%s", key, value))
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
