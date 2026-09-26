package hub

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// TestParseHubOptionsRejectsMissingDeployBinary pins that a bad -deploy-binary
// value fails at startup naming the flag, not later at the first attach where
// the operator would have to reverse-engineer which flag caused the refusal.
func TestParseHubOptionsRejectsMissingDeployBinary(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseHubOptions([]string{"-deploy-binary", filepath.Join(t.TempDir(), "absent")}, &stderr)
	if err == nil {
		t.Fatal("parseHubOptions accepted a -deploy-binary that does not exist")
	}
	if !strings.Contains(err.Error(), "-deploy-binary") {
		t.Fatalf("startup error does not name the flag: %v", err)
	}
}

// TestParseHubOptionsRejectsNonGoDeployBinary covers the other artifact failure
// the seam must never turn into a panic or a push: a file that is not a Go
// executable (a script, a stripped binary, an ELF with no buildinfo) is refused
// where the flag is read.
func TestParseHubOptionsRejectsNonGoDeployBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-binary")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho not go\n"), 0o755); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	_, err := parseHubOptions([]string{"-deploy-binary", path}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseHubOptions accepted a non-Go -deploy-binary")
	}
	if !strings.Contains(err.Error(), "-deploy-binary") {
		t.Fatalf("startup error does not name the flag: %v", err)
	}
}

// TestValidateDeployBinaryRejectsNonEvenerGoBinary is the artifact-identity pin:
// a -deploy-binary that is a real Go executable for the host's platform but not
// evener must be refused where the flag is read, naming the flag. Without the
// check the artifact validates (stat-able, executable, readable buildinfo), so it
// is pushed and replaces the host's evener — caught only after the fact by the
// post-push version comparison, with the host already holding a file that cannot
// serve a hub.
//
// Both cases are real Go binaries the test does not fake: a program from an
// unrelated module (the operator's stray artifact) and this package's own test
// binary (a sibling command in the evener module, so even an evener-tree binary
// that is not the runtime is refused).
func TestValidateDeployBinaryRejectsNonEvenerGoBinary(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "foreign module", path: nonEvenerGoBinary(t)},
		{name: "this package's test binary", path: testBinary},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateDeployBinary(tc.path)
			if err == nil {
				t.Fatalf("validateDeployBinary accepted a non-evener Go binary %q", tc.path)
			}
			if !strings.Contains(err.Error(), deployBinaryFlag) {
				t.Fatalf("refusal does not name the flag: %v", err)
			}
			if !strings.Contains(err.Error(), "not evener") {
				t.Fatalf("refusal does not say the artifact is not evener: %v", err)
			}
		})
	}
}

// TestValidateDeployBinaryAcceptsAGenuineEvenerArtifact is the other half, proven
// rather than assumed: the repository's own ./cmd/evener build validates, so the
// identity check cannot refuse the artifact an operator actually ships. It pins
// buildinfo.Path — the import path of a Go executable's main package — as the
// field the check reads: that is the field which names
// primeradiant.com/evener/cmd/evener exactly, where Main.Path (the module) is
// shared with this package's test binary and every other command in the module.
func TestValidateDeployBinaryAcceptsAGenuineEvenerArtifact(t *testing.T) {
	artifact := evenerArtifact(t)
	got, err := validateDeployBinary(artifact)
	if err != nil {
		t.Fatalf("validateDeployBinary refused the repository's own ./cmd/evener build: %v", err)
	}
	want, err := filepath.EvalSymlinks(artifact)
	if err != nil {
		t.Fatalf("canonicalize %q: %v", artifact, err)
	}
	if got != want {
		t.Fatalf("validateDeployBinary returned %q, want the canonical %q", got, want)
	}
}

// TestDeployBinarySeamRefusesNonEvenerArtifact pins the artifact's second read:
// the seam re-checks identity before staging, so even an artifact swapped after
// startup is refused before the push, out is never written, and the refusal
// carries the terminal sentinel (retrying re-reads the same file).
func TestDeployBinarySeamRefusesNonEvenerArtifact(t *testing.T) {
	path := nonEvenerGoBinary(t)
	out := filepath.Join(t.TempDir(), "evener")
	err := deployBinaryBuild(path)(t.Context(), runtime.GOOS, runtime.GOARCH, out)
	if err == nil {
		t.Fatal("deployBinaryBuild accepted a non-evener artifact")
	}
	if !errors.Is(err, sshconn.ErrDeployArtifactUnusable) {
		t.Fatalf("refusal %v does not carry the terminal ErrDeployArtifactUnusable sentinel", err)
	}
	if !strings.Contains(err.Error(), deployBinaryFlag) {
		t.Fatalf("refusal does not name the flag: %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("out was written for a non-evener artifact (stat err = %v)", statErr)
	}
}

// TestParseHubOptionsRejectsNonEvenerBuildSource pins that -build-source reuses
// sshconn's checkout validation (via the exported entry point) and fails
// startup naming the flag rather than surfacing as a first-attach build error.
func TestParseHubOptionsRejectsNonEvenerBuildSource(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseHubOptions([]string{"-build-source", t.TempDir()}, &stderr)
	if err == nil {
		t.Fatal("parseHubOptions accepted a -build-source that is not an evener checkout")
	}
	if !strings.Contains(err.Error(), "-build-source") {
		t.Fatalf("startup error does not name the flag: %v", err)
	}
	// sshconn's own refusal is preserved in the wrapped message.
	if !strings.Contains(err.Error(), "not the evener checkout") {
		t.Fatalf("startup error dropped sshconn's refusal: %v", err)
	}
}

// TestDeployBinarySeamCopiesMatchingArtifact is the operator-artifact seam's
// happy path: the artifact is not compiled, so a matching GOOS/GOARCH is
// verified from the file itself and the bytes are copied to out unchanged and
// executable.
func TestDeployBinarySeamCopiesMatchingArtifact(t *testing.T) {
	exe := evenerArtifact(t)
	out := filepath.Join(t.TempDir(), "evener")
	if err := deployBinaryBuild(exe)(t.Context(), runtime.GOOS, runtime.GOARCH, out); err != nil {
		t.Fatalf("deployBinaryBuild(matching target): %v", err)
	}
	want, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read source artifact: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read copied artifact: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("copied artifact differs from the source (%d vs %d bytes)", len(got), len(want))
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat copied artifact: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("copied artifact mode = %v, want an executable mode preserved from the source", info.Mode())
	}
}

// TestDeployBinarySeamRefusesWrongTarget pins the load-bearing rule: the target
// is read from the artifact's buildinfo, not trusted from the operator, and a
// mismatch is refused before the push (out is never written). The refusal must
// also carry the terminal sentinel: a wrong-platform artifact is a permanent
// operator mistake — retrying re-reads the same file — so it must not be
// classified as the retryable ErrDeploy the deploy path otherwise uses.
func TestDeployBinarySeamRefusesWrongTarget(t *testing.T) {
	exe := evenerArtifact(t)
	goos, goarch := otherTarget(runtime.GOOS, runtime.GOARCH)
	out := filepath.Join(t.TempDir(), "evener")
	err := deployBinaryBuild(exe)(t.Context(), goos, goarch, out)
	if err == nil {
		t.Fatalf("deployBinaryBuild accepted a %s/%s artifact for %s/%s", runtime.GOOS, runtime.GOARCH, goos, goarch)
	}
	if !errors.Is(err, sshconn.ErrDeployArtifactUnusable) {
		t.Fatalf("refusal %v does not carry the terminal ErrDeployArtifactUnusable sentinel", err)
	}
	if !strings.Contains(err.Error(), deployBinaryFlag) || !strings.Contains(err.Error(), buildSourceFlag) {
		t.Fatalf("refusal does not name both flags: %v", err)
	}
	for _, want := range []string{runtime.GOOS + "/" + runtime.GOARCH, goos + "/" + goarch} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not name %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("out was written despite the target mismatch (stat err = %v)", statErr)
	}
}

// TestDeployBinarySeamRefusesNonGoArtifact pins that a stripped or non-Go file
// is a refusal on the seam path, never a panic and never a written-out file.
func TestDeployBinarySeamRefusesNonGoArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-go")
	if err := os.WriteFile(path, []byte("plain text, no buildinfo"), 0o755); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	out := filepath.Join(t.TempDir(), "evener")
	err := deployBinaryBuild(path)(t.Context(), runtime.GOOS, runtime.GOARCH, out)
	if err == nil {
		t.Fatal("deployBinaryBuild accepted a non-Go artifact")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("out was written for a non-Go artifact (stat err = %v)", statErr)
	}
}

// TestParseHubOptionsRejectsNonExecutableDeployBinary pins that an artifact the
// hub cannot exec is refused where the flag is read, naming the flag: a 0644 Go
// binary passes every other check (it is stat-able, readable, and carries
// buildinfo) and would otherwise land on the host unable to run.
func TestParseHubOptionsRejectsNonExecutableDeployBinary(t *testing.T) {
	path := copyExecutableArtifact(t, 0o644)
	_, err := parseHubOptions([]string{"-deploy-binary", path}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseHubOptions accepted a non-executable -deploy-binary")
	}
	if !strings.Contains(err.Error(), "-deploy-binary") {
		t.Fatalf("startup error does not name the flag: %v", err)
	}
	if !strings.Contains(err.Error(), "execut") {
		t.Fatalf("startup error does not say the artifact is not executable: %v", err)
	}
}

// TestDeployBinarySeamStagesAnExecutable pins the other half: the staged copy
// always carries the exec bits, whatever the source artifact's mode, so the file
// the push installs is runnable even when the source's bits would not allow it.
func TestDeployBinarySeamStagesAnExecutable(t *testing.T) {
	src := copyExecutableArtifact(t, 0o600)
	out := filepath.Join(t.TempDir(), "evener")
	if err := deployBinaryBuild(src)(t.Context(), runtime.GOOS, runtime.GOARCH, out); err != nil {
		t.Fatalf("deployBinaryBuild(non-executable source): %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat staged artifact: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("staged artifact mode = %v, want the exec bits set regardless of the source's", info.Mode())
	}
}

// copyExecutableArtifact copies a genuine evener artifact — a real Go executable
// with readable buildinfo — to a temp path with the given mode, so a mode-only
// refusal is not confounded by the artifact's identity.
func copyExecutableArtifact(t *testing.T, mode os.FileMode) string {
	t.Helper()
	exe := evenerArtifact(t)
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read %q: %v", exe, err)
	}
	path := filepath.Join(t.TempDir(), "evener")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	// Chmod explicitly: WriteFile's mode is masked by the process umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %q: %v", path, err)
	}
	return path
}

// evenerArtifact returns a genuine evener runtime binary built from this
// checkout. validateDeployBinary requires the artifact's main package to be
// evener's, so a test that needs an accepted artifact cannot use this package's
// test binary: its main package is cmd/evener-hub.test. The build is
// liveStackBinaries' shared once-per-test-binary ./cmd/evener build, so using it
// adds no second compile.
func evenerArtifact(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	return filepath.Join(liveStackBinaries(t, repoRoot), "evener")
}

// nonEvenerGoBinary builds a real, runnable Go executable that is not evener:
// a tiny program in a module of its own, the "any Go program built for the
// host's platform" an operator could point -deploy-binary at. The go directive
// is deliberately older than this repository's so the fixture builds with the
// toolchain already running the tests; a directive newer than the local one
// makes GOTOOLCHAIN=auto try to download a toolchain instead.
func nonEvenerGoBinary(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/not-evener\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write fixture main.go: %v", err)
	}
	out := filepath.Join(t.TempDir(), "not-evener")
	build := exec.Command("go", "build", "-o", out, ".")
	build.Dir = src
	if combined, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build a non-evener Go binary: %v\n%s", err, combined)
	}
	return out
}

// TestParseHubOptionsNormalizesDeployFlags pins that a whitespace-only flag is
// no deploy path at all: it must not skip validation while still being logged and
// wired as though a path were set. Both flags are stored as the single trimmed
// value validation, logging, and the wiring all read.
func TestParseHubOptionsNormalizesDeployFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "deploy-binary", args: []string{"-deploy-binary", "   "}},
		{name: "build-source", args: []string{"-build-source", " \t\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseHubOptions(tc.args, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseHubOptions(%v): %v", tc.args, err)
			}
			if opts.deployBinary != "" || opts.buildSource != "" {
				t.Fatalf("whitespace-only flag stored as deployBinary=%q buildSource=%q, want both empty", opts.deployBinary, opts.buildSource)
			}
		})
	}
}

// TestDeployBinaryFlagIsStoredAndLoggedCanonically pins the other half: a
// relative path that validates at startup is stored (and logged) as the canonical
// absolute path, so the artifact a later deploy reads is the one validation
// approved rather than whatever the same relative spelling resolves to then.
func TestDeployBinaryFlagIsStoredAndLoggedCanonically(t *testing.T) {
	exe := evenerArtifact(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	rel, err := filepath.Rel(cwd, exe)
	if err != nil {
		t.Skipf("cannot spell %q relative to %q: %v", exe, cwd, err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("canonicalize %q: %v", exe, err)
	}

	// A relative spelling validates and is stored canonically.
	opts, err := parseHubOptions([]string{"-deploy-binary", rel}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseHubOptions(relative -deploy-binary): %v", err)
	}
	if opts.deployBinary != want {
		t.Fatalf("stored -deploy-binary = %q, want the canonical %q", opts.deployBinary, want)
	}
	if !filepath.IsAbs(opts.deployBinary) {
		t.Fatalf("stored -deploy-binary %q is not absolute", opts.deployBinary)
	}

	// The startup log carries that same canonical value, not the raw spelling.
	_, cfg, deps := newTraceMainTestDeps(t)
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener", "-deploy-binary", rel}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	logged := deployPathLogLines(stderr.String())
	wantLine := "[hub] deploy path: -deploy-binary " + want
	if len(logged) != 1 || logged[0] != wantLine {
		t.Fatalf("deploy path log = %v, want exactly [%q]", logged, wantLine)
	}
}

// TestDeployWiringPrefersDeployBinary pins the precedence rule at the Options
// seam: with both flags set the constructed Options carry BuildBinary and no
// BuildSource, so the manager's own `BuildBinary first` dispatch uses the
// operator's artifact.
func TestDeployWiringPrefersDeployBinary(t *testing.T) {
	exe := evenerArtifact(t)
	dw := hubOptions{deployBinary: exe, buildSource: "/some/evener/checkout"}.deployWiring()
	if dw.buildBinary == nil {
		t.Fatal("deployWiring did not configure BuildBinary with both flags set")
	}
	if dw.buildSource != "" {
		t.Fatalf("deployWiring kept BuildSource %q alongside BuildBinary", dw.buildSource)
	}
	// The constructed BuildBinary is the artifact seam, not a cross-compile:
	// invoking it with the artifact's own target copies the artifact.
	out := filepath.Join(t.TempDir(), "evener")
	if err := dw.buildBinary(t.Context(), runtime.GOOS, runtime.GOARCH, out); err != nil {
		t.Fatalf("constructed BuildBinary: %v", err)
	}
}

// TestDeployWiringUsesBuildSourceWhenNoBinary pins the fallback leg: with only
// -build-source set, BuildSource carries it and no BuildBinary is installed.
func TestDeployWiringUsesBuildSourceWhenNoBinary(t *testing.T) {
	dw := hubOptions{buildSource: "/some/evener/checkout"}.deployWiring()
	if dw.buildBinary != nil {
		t.Fatal("deployWiring configured BuildBinary without -deploy-binary")
	}
	if dw.buildSource != "/some/evener/checkout" {
		t.Fatalf("deployWiring BuildSource = %q, want the flag value", dw.buildSource)
	}
}

// TestDeployWiringWithNeitherSetsHelpOnly pins the no-deploy-path case: no
// build seam is installed (behavior is today's), but the refusal still carries
// the hub's help text naming both flags.
func TestDeployWiringWithNeitherSetsHelpOnly(t *testing.T) {
	dw := hubOptions{}.deployWiring()
	if dw.buildBinary != nil || dw.buildSource != "" {
		t.Fatalf("deployWiring with neither flag = %+v, want no build seam", dw)
	}
	if dw.help == "" {
		t.Fatal("deployWiring left the refusal help text empty, so the refusal would name no flag")
	}
}

// TestHubDeployHelpNamesBothFlags pins what the operator is told when no deploy
// path is configured: the hub's own flags, not sshconn's internal field name.
func TestHubDeployHelpNamesBothFlags(t *testing.T) {
	help := hubOptions{}.deployWiring().help
	for _, want := range []string{"-deploy-binary", "-build-source"} {
		if !strings.Contains(help, want) {
			t.Fatalf("hub deploy help %q does not name %s", help, want)
		}
	}
}

// TestRunMainWithoutDeployFlagsStarts pins that a local-only controller needs no
// deploy path: a hub started with neither flag comes up, as it does today.
func TestRunMainWithoutDeployFlagsStarts(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain with no deploy flags: %v, stderr=%s", err, stderr.String())
	}
}

// TestRunMainPassesDeployHelpToTheSSHManager pins the one line nothing asserted:
// main.go hands the hub's deploy help text to sshconn.Options. That text is what
// makes the terminal version refusal actionable — without it the operator is told
// to "set Options.BuildSource", a library-internal field they cannot act on — so
// the wiring is the behavior, not an implementation detail. The manager is
// captured through the deps seam and the Options it was handed are asserted, so
// this fails if the hub stops passing the help text even though deployWiring
// still produces it.
func TestRunMainPassesDeployHelpToTheSSHManager(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	var got sshconn.Options
	deps.newSSHManager = func(reg *hostreg.Registry, opts sshconn.Options) *sshconn.Manager {
		got = opts
		return sshconn.New(reg, opts)
	}
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if got.DeployHelp != hubDeployHelp {
		t.Fatalf("sshconn.Options.DeployHelp = %q, want the hub's help text %q", got.DeployHelp, hubDeployHelp)
	}
	for _, want := range []string{"-deploy-binary", "-build-source"} {
		if !strings.Contains(got.DeployHelp, want) {
			t.Fatalf("the wired deploy help %q does not name %s", got.DeployHelp, want)
		}
	}
}

// otherTarget returns a GOOS/GOARCH pair guaranteed to differ from the given
// one. Only the arch changes, so the mismatch is deterministic on every host.
func otherTarget(goos, goarch string) (string, string) {
	if goarch == "amd64" {
		return goos, "arm64"
	}
	return goos, "amd64"
}

// TestRunMainLogsTheDeployPathItWasGiven pins acceptance criterion 10 as landed:
// the hub logs, at startup, which deploy path it was started with — the
// -deploy-binary / -build-source value, and which one wins when both are set.
// That single line is the whole deploy-leg log: nothing logs a host, a target,
// or a file's contents, so the criterion is stated as exactly this and no more.
// The hub's other startup lines (the auth URL among them) are not part of the
// deploy leg and are unchanged by this slice.
func TestRunMainLogsTheDeployPathItWasGiven(t *testing.T) {
	exe := evenerArtifact(t)
	// The checkout only has to pass verifyBuildSource here. This test binary
	// carries no build stamp, so the revision check is skipped; pinning GitSHA to
	// "" keeps that true if the package is ever built with ldflags.
	origSHA := buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.GitSHA = origSHA })
	buildinfo.GitSHA = ""
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "both flags log which one wins",
			args: []string{"-deploy-binary", exe, "-build-source", checkout},
			want: "[hub] deploy path: -deploy-binary " + exe + " takes precedence over -build-source " + checkout,
		},
		{
			name: "deploy-binary alone",
			args: []string{"-deploy-binary", exe},
			want: "[hub] deploy path: -deploy-binary " + exe,
		},
		{
			name: "build-source alone",
			args: []string{"-build-source", checkout},
			want: "[hub] deploy path: -build-source " + checkout,
		},
		{
			name: "no deploy path logs nothing",
		},
		{
			// A whitespace-only flag is not a deploy path: it must not be logged
			// (or wired) as one just because the raw string is non-empty.
			name: "whitespace-only deploy-binary logs nothing",
			args: []string{"-deploy-binary", "   "},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, deps := newTraceMainTestDeps(t)
			var stderr bytes.Buffer
			args := append([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, tc.args...)
			if err := runMain(args, &stderr, deps); err != nil {
				t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
			}
			logged := deployPathLogLines(stderr.String())
			if tc.want == "" {
				if len(logged) != 0 {
					t.Fatalf("a hub with no deploy path logged a deploy leg: %v", logged)
				}
				return
			}
			if len(logged) != 1 || logged[0] != tc.want {
				t.Fatalf("deploy path log = %v, want exactly [%q]", logged, tc.want)
			}
		})
	}
}

// deployPathLogLines returns the hub's deploy-path startup lines, which are the
// whole deploy-leg log.
func deployPathLogLines(stderr string) []string {
	var out []string
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(line, "[hub] deploy path: ") {
			out = append(out, line)
		}
	}
	return out
}
