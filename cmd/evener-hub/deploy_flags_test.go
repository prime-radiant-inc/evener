package hub

import (
	"bytes"
	stdbuildinfo "debug/buildinfo"
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
	// The fixture must build as its own module, not in whatever workspace or
	// toolchain configuration the developer's environment names: an exported
	// GOWORK (or a `go env -w` GOENV setting, or a GOFLAGS build flag) would
	// otherwise reach the child `go build` and fail it for a reason unrelated to
	// the code under test. Same isolation cmd/evener-dev's fixture builds use.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
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
// wired as though a path were set. With the own-executable default in place, "no
// path at all" means the flag behaves exactly as if it were never given, so the
// default is adopted — the trimmed value is the one value validation, logging,
// and the wiring all read.
func TestParseHubOptionsNormalizesDeployFlags(t *testing.T) {
	exe := evenerArtifact(t)
	stubHubExecutable(t, exe, nil)
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("canonicalize the artifact: %v", err)
	}
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
			if opts.deployBinary != want || opts.buildSource != "" || !opts.deployDefault {
				t.Fatalf("a whitespace-only %s left deployBinary=%q buildSource=%q deployDefault=%v, want the unset-flag default %q", tc.name, opts.deployBinary, opts.buildSource, opts.deployDefault, want)
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

// TestDeployWiringWithNeitherSetsHelpOnly pins the raw no-fields case: a
// hubOptions with neither flag set carries no build seam and still the help text
// naming the remedy. Production never leaves a parsed hubOptions in this shape —
// validateDeployFlags resolves the own-executable default there — so this pins
// the unwired state's wiring itself, which is what a non-evener executable (and
// an -no-deploy hub) reaches.
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
//
// Both states are checked because the text is state-dependent: this test binary
// is not an evener build, so a flagless hub is the unwired state and must get the
// text that leads with the flags it can act on — while -no-deploy is the state
// whose way back is exactly the shared text's leading clause.
func TestRunMainPassesDeployHelpToTheSSHManager(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unwired (this test binary is not an evener build)", want: hubDeployHelpUnwired},
		{name: "disabled with -no-deploy", args: []string{"-no-deploy"}, want: hubDeployHelp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, deps := newTraceMainTestDeps(t)
			var got sshconn.Options
			deps.newSSHManager = func(reg *hostreg.Registry, opts sshconn.Options) *sshconn.Manager {
				got = opts
				return sshconn.New(reg, opts)
			}
			var stderr bytes.Buffer
			args := append([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, tc.args...)
			if err := runMain(args, &stderr, deps); err != nil {
				t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
			}
			if got.DeployHelp != tc.want {
				t.Fatalf("sshconn.Options.DeployHelp = %q, want the hub's text for the %s state: %q", got.DeployHelp, tc.name, tc.want)
			}
			for _, want := range []string{"-deploy-binary", "-build-source"} {
				if !strings.Contains(got.DeployHelp, want) {
					t.Fatalf("the wired deploy help %q does not name %s", got.DeployHelp, want)
				}
			}
		})
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
	// The hub logs the canonical path it stores — validateDeployFlags runs each
	// flag through filepath.EvalSymlinks — so the expected strings are canonical
	// too. On macOS t.TempDir lives under a symlinked /var, where a raw spelling
	// would otherwise differ from what is logged for a reason unrelated to the
	// behaviour under test; the sibling canonical-path test builds its want the
	// same way.
	exe, err := filepath.EvalSymlinks(evenerArtifact(t))
	if err != nil {
		t.Fatalf("canonicalize the built artifact: %v", err)
	}
	// The no-flag and whitespace-only cases below pin the unwired default: this
	// test binary is not an evener build, so the own-executable default is
	// skipped and there is no deploy leg to log. Stub the seam explicitly so that
	// property is the test's own rather than an accident of the binary running it.
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	stubHubExecutable(t, testBinary, nil)
	// The checkout only has to pass verifyBuildSource here. This test binary
	// carries no build stamp, so the revision check is skipped; pinning GitSHA to
	// "" keeps that true if the package is ever built with ldflags.
	origSHA := buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.GitSHA = origSHA })
	buildinfo.GitSHA = ""
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize the checkout: %v", err)
	}
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

// stubHubExecutable points the hub's own-executable seam at path (or err) for
// one test. The deploy default is this hub's own executable, so asserting it
// needs the seam to name a controlled artifact instead of whatever binary is
// running this test.
func stubHubExecutable(t *testing.T, path string, err error) {
	t.Helper()
	orig := hubExecutable
	hubExecutable = func() (string, error) { return path, err }
	t.Cleanup(func() { hubExecutable = orig })
}

// TestParseHubOptionsDefaultsDeployBinaryToOwnExecutable pins the directive's
// default: with neither -deploy-binary nor -build-source set, the hub wires its
// own running executable as the deploy artifact, so a host that needs the
// controller's build is offered one with no flags. The wired seam is invoked,
// not merely inspected, so "carries the own-executable source" means the bytes
// it stages are the byte-identical running executable.
func TestParseHubOptionsDefaultsDeployBinaryToOwnExecutable(t *testing.T) {
	exe := evenerArtifact(t)
	stubHubExecutable(t, exe, nil)

	opts, err := parseHubOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseHubOptions with no deploy flags: %v", err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("canonicalize the artifact: %v", err)
	}
	if opts.deployBinary != want {
		t.Fatalf("deployBinary = %q, want this hub's own executable %q", opts.deployBinary, want)
	}
	dw := opts.deployWiring()
	if dw.buildBinary == nil {
		t.Fatalf("deployWiring did not wire the own-executable source: %+v", dw)
	}
	if dw.buildSource != "" {
		t.Fatalf("deployWiring wired a build source %q alongside the own executable", dw.buildSource)
	}
	out := filepath.Join(t.TempDir(), "evener")
	if err := dw.buildBinary(t.Context(), runtime.GOOS, runtime.GOARCH, out); err != nil {
		t.Fatalf("the wired own-executable seam refused this hub's own build: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read the staged artifact: %v", err)
	}
	wantBytes, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read the hub's own executable: %v", err)
	}
	if !bytes.Equal(got, wantBytes) {
		t.Fatalf("the staged default artifact (%d bytes) is not the byte-identical hub executable (%d bytes)", len(got), len(wantBytes))
	}
}

// TestDefaultDeploySourceHonestyWithThisTestBinarysIdentity pins the state the
// default leaves for a hub that is not an evener build, from the one identity a
// unit test can reach without stubbing anything: the seam at its default —
// hubExecutable is os.Executable, so the executable under test is the binary
// running this test. That identity is the hub package's own test binary, whose
// main package is not the evener runtime (observed and pinned below), so the
// default must not resolve, the deploy stays unwired, and a host that needs a
// build is refused with the unwired state's remedy. The production side of the
// same seam — a hub run from ./cmd/evener, the only binary that carries the hub
// code — is TestDefaultDeploySourceIsTheHubExecutableE2E, which boots the real
// binary and reads the deploy line it logs; together the two say the default
// fires for the evener runtime and for nothing else.
func TestDefaultDeploySourceHonestyWithThisTestBinarysIdentity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	info, err := stdbuildinfo.ReadFile(exe)
	if err != nil {
		t.Fatalf("read buildinfo of this test binary %q: %v", exe, err)
	}
	t.Logf("this test binary: %s; main package %q", exe, info.Path)
	const testBinaryMainPackage = "primeradiant.com/evener/cmd/evener-hub.test"
	if info.Path != testBinaryMainPackage {
		t.Fatalf("this test binary's main package = %q, want %q (the non-evener identity the default must refuse)", info.Path, testBinaryMainPackage)
	}
	opts, err := parseHubOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseHubOptions: %v", err)
	}
	if opts.deployBinary != "" || opts.deployDefault {
		t.Fatalf("a non-evener executable resolved a deploy source: deployBinary=%q deployDefault=%v", opts.deployBinary, opts.deployDefault)
	}
	dw := opts.deployWiring()
	if dw.buildBinary != nil || dw.buildSource != "" || dw.ownExecutable {
		t.Fatalf("the unwired state still wired a deploy source: %+v", dw)
	}
	if dw.help != hubDeployHelpUnwired {
		t.Fatalf("the unwired state's remedy = %q, want %q (the text whose first clause names an action this hub can take)", dw.help, hubDeployHelpUnwired)
	}
}

// TestOwnExecutableSeamRefusalNamesTheDefaultNotAFlag pins the defaulted source's
// refusal wording: the operator passed no flag, so a refusal must not present
// -deploy-binary as the source they named. It names the hub's own executable
// instead, and offers the exits a defaulted source has — -no-deploy, or naming
// another artifact — rather than leaving a terminal host refusal that reads as if
// the operator had chosen the file it refuses.
func TestOwnExecutableSeamRefusalNamesTheDefaultNotAFlag(t *testing.T) {
	exe := evenerArtifact(t)
	stubHubExecutable(t, exe, nil)
	opts, err := parseHubOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseHubOptions: %v", err)
	}
	goos, goarch := otherTarget(runtime.GOOS, runtime.GOARCH)
	out := filepath.Join(t.TempDir(), "evener")
	err = opts.deployWiring().buildBinary(t.Context(), goos, goarch, out)
	if err == nil {
		t.Fatalf("the defaulted seam accepted a %s/%s artifact for %s/%s", runtime.GOOS, runtime.GOARCH, goos, goarch)
	}
	msg := err.Error()
	if !strings.Contains(msg, "this hub's own executable") || !strings.Contains(msg, "default deploy source") {
		t.Fatalf("refusal does not name the defaulted source: %v", err)
	}
	if strings.Contains(msg, deployBinaryFlag+" \"") {
		t.Fatalf("refusal presents -deploy-binary as the source the operator named: %v", err)
	}
	if !strings.Contains(msg, noDeployFlag) {
		t.Fatalf("refusal does not name the opt-out for the defaulted source: %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("out was written despite the target mismatch (stat err = %v)", statErr)
	}
}

// TestTheHubBinaryIsTheEvenerRuntime pins the packaging fact the own-executable
// default rests on: cmd/evener-hub is a library package, not an executable, so
// the only binary a hub can be run from is the evener runtime — production runs
// it as `evener hub`, and make build-hub builds the runtime. That is what makes
// the default's artifact valid: a running hub's os.Executable() is the
// ./cmd/evener build, whose main package is exactly the evenerMainPackage the
// artifact check requires. A `package main` landing in cmd/evener-hub would give
// the hub a second, non-evener executable to be run from — where the default
// would silently stop firing — so this fails then, by name, rather than leaving
// that state for production to find.
func TestTheHubBinaryIsTheEvenerRuntime(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	cmd := exec.Command("go", "list", "-f", "{{.Name}}\t{{.ImportPath}}", "./cmd/evener", "./cmd/evener-hub")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./cmd/evener ./cmd/evener-hub: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(got) != 2 {
		t.Fatalf("go list printed %d lines, want one per package:\n%s", len(got), out)
	}
	if want := "main\t" + evenerMainPackage; got[0] != want {
		t.Fatalf("./cmd/evener = %q, want %q: the hub is run from the evener runtime, so the artifact check must accept its main package", got[0], want)
	}
	if want := "hub\tprimeradiant.com/evener/cmd/evener-hub"; got[1] != want {
		t.Fatalf("./cmd/evener-hub = %q, want %q: a hub executable of its own would never be an accepted deploy artifact, and the own-executable default would silently not fire there", got[1], want)
	}
}

// TestParseHubOptionsNoDeployKeepsTheDeployUnwired pins the opt-out: -no-deploy
// wins over the own-executable default (and over the explicit flags), so no
// deploy seam is wired even though a deployable default exists — and the
// refusals still name the remedy, so the state is explicit rather than a
// silent nothing.
func TestParseHubOptionsNoDeployKeepsTheDeployUnwired(t *testing.T) {
	exe := evenerArtifact(t)
	stubHubExecutable(t, exe, nil)
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("canonicalize the artifact: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
		// wantStored is the validated deploy-binary value kept for the startup
		// log's "ignoring ..." clause; an opted-out flag is still validated and
		// reported, and it is deployWiring that refuses to use it.
		wantStored string
	}{
		{name: "alone", args: []string{"-no-deploy"}},
		{name: "with its own executable on the line", args: []string{"-no-deploy", "-deploy-binary", exe}, wantStored: want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseHubOptions(tc.args, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseHubOptions(%v): %v", tc.args, err)
			}
			if opts.deployBinary != tc.wantStored || opts.buildSource != "" {
				t.Fatalf("-no-deploy stored deployBinary=%q buildSource=%q, want deployBinary=%q and no build source", opts.deployBinary, opts.buildSource, tc.wantStored)
			}
			if opts.deployDefault {
				t.Fatal("-no-deploy still adopted the own-executable default")
			}
			dw := opts.deployWiring()
			if dw.buildBinary != nil || dw.buildSource != "" {
				t.Fatalf("-no-deploy still wired a deploy source: %+v", dw)
			}
			if dw.help == "" {
				t.Fatal("-no-deploy left the refusal help empty, so a host that needs a deploy would be refused with no remedy named")
			}
			for _, want := range []string{"-deploy-binary", "-build-source"} {
				if !strings.Contains(dw.help, want) {
					t.Fatalf("the -no-deploy refusal remedy %q does not name %s", dw.help, want)
				}
			}
		})
	}
}

// TestParseHubOptionsDefaultNeverFiresForANonEvenerExecutable pins the other
// half of the default: only an evener runtime build may become the deploy
// artifact, so an embedder or test binary leaves the deploy unwired exactly as
// today — refused with the named remedy, never a silent no-op and never a
// non-evener program pushed to a host.
func TestParseHubOptionsDefaultNeverFiresForANonEvenerExecutable(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	for _, tc := range []struct {
		name string
		path string
		err  error
	}{
		{name: "a foreign Go program", path: nonEvenerGoBinary(t)},
		{name: "this package's test binary", path: testBinary},
		{name: "an unresolvable executable", err: errors.New("no executable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubHubExecutable(t, tc.path, tc.err)
			opts, err := parseHubOptions(nil, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseHubOptions: %v", err)
			}
			if opts.deployBinary != "" || opts.buildSource != "" {
				t.Fatalf("a non-evener executable was adopted as the deploy source: deployBinary=%q buildSource=%q", opts.deployBinary, opts.buildSource)
			}
			dw := opts.deployWiring()
			if dw.buildBinary != nil || dw.buildSource != "" {
				t.Fatalf("deployWiring wired a non-evener executable: %+v", dw)
			}
			if dw.help == "" || !strings.Contains(dw.help, "-deploy-binary") {
				t.Fatalf("the unwired refusal does not name the remedy: %q", dw.help)
			}
		})
	}
}

// TestUnwiredRemedyNamesTheActualCause pins why the unwired state has two texts:
// "not an evener build" is a claim about the executable, so it is returned only
// when validateDeployFlags actually rejected the executable on its main package.
// A hub whose executable could not be located or read is refused with the
// cause-free text instead — an evener hub whose binary moved must not be told it
// is not evener.
func TestUnwiredRemedyNamesTheActualCause(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		execErr error
		want    string
	}{
		{name: "not an evener build", path: nonEvenerGoBinary(t), want: hubDeployHelpUnwired},
		{name: "no executable to locate", execErr: errors.New("no executable"), want: hubDeployHelpNoSource},
		{name: "an unresolvable path", path: filepath.Join(t.TempDir(), "absent"), want: hubDeployHelpNoSource},
		{name: "an unreadable file", path: unreadableGoArtifact(t), want: hubDeployHelpNoSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubHubExecutable(t, tc.path, tc.execErr)
			opts, err := parseHubOptions(nil, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseHubOptions: %v", err)
			}
			if opts.deployBinary != "" || opts.deployDefault {
				t.Fatalf("a non-adoptable executable resolved a deploy source: deployBinary=%q deployDefault=%v", opts.deployBinary, opts.deployDefault)
			}
			help := opts.deployWiring().help
			if help != tc.want {
				t.Fatalf("unwired remedy = %q, want %q", help, tc.want)
			}
			for _, flag := range []string{deployBinaryFlag, buildSourceFlag} {
				if !strings.Contains(help, flag) {
					t.Fatalf("the %s remedy does not name %s: %q", tc.name, flag, help)
				}
			}
		})
	}
}

// unreadableGoArtifact returns the path of an executable file with no Go
// buildinfo: validateDeployBinary reads it fine at the filesystem level and
// rejects it when the buildinfo read fails — an adoption failure that is not the
// identity one.
func unreadableGoArtifact(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-go")
	if err := os.WriteFile(path, []byte("plain text, no buildinfo"), 0o755); err != nil {
		t.Fatalf("seed %q: %v", path, err)
	}
	return path
}

// TestParseHubOptionsExplicitDeployFlagsOverrideTheOwnExecutableDefault pins the
// precedence: an operator-named source wins over the own-executable default, so
// the default is a fallback and not a second, silently-preferred path. The own
// executable is a different file from the explicit artifact, so the assertions
// say which one was stored, not merely that something was.
func TestParseHubOptionsExplicitDeployFlagsOverrideTheOwnExecutableDefault(t *testing.T) {
	own := evenerArtifact(t)
	stubHubExecutable(t, own, nil)
	explicit := copyExecutableArtifact(t, 0o755)

	t.Run("-deploy-binary", func(t *testing.T) {
		opts, err := parseHubOptions([]string{"-deploy-binary", explicit}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseHubOptions(-deploy-binary): %v", err)
		}
		want, err := filepath.EvalSymlinks(explicit)
		if err != nil {
			t.Fatalf("canonicalize the explicit artifact: %v", err)
		}
		ownCanonical, err := filepath.EvalSymlinks(own)
		if err != nil {
			t.Fatalf("canonicalize the own executable: %v", err)
		}
		if opts.deployBinary != want {
			t.Fatalf("deployBinary = %q, want the explicit artifact %q (not the default %q)", opts.deployBinary, want, ownCanonical)
		}
		if opts.deployDefault {
			t.Fatal("the own-executable default fired alongside an explicit -deploy-binary")
		}
		dw := opts.deployWiring()
		if dw.buildBinary == nil {
			t.Fatalf("explicit -deploy-binary wiring = %+v, want a build seam", dw)
		}
		if dw.ownExecutable {
			t.Fatal("an explicit -deploy-binary was marked as this hub's own executable, so a dirty controller would install an artifact it cannot verify")
		}
	})

	t.Run("-build-source", func(t *testing.T) {
		checkout := evenerCheckoutFixture(t)
		opts, err := parseHubOptions([]string{"-build-source", checkout}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("parseHubOptions(-build-source): %v", err)
		}
		if opts.deployBinary != "" || opts.deployDefault {
			t.Fatalf("the own-executable default fired alongside -build-source: deployBinary=%q deployDefault=%v", opts.deployBinary, opts.deployDefault)
		}
		if opts.buildSource != checkout {
			t.Fatalf("buildSource = %q, want %q", opts.buildSource, checkout)
		}
		dw := opts.deployWiring()
		if dw.buildSource != checkout || dw.buildBinary != nil || dw.ownExecutable {
			t.Fatalf("-build-source wiring = %+v, want the source seam and no binary artifact", dw)
		}
	})
}

// evenerCheckoutFixture writes the smallest tree sshconn's build-source
// validation accepts: an evener go.mod and a cmd/evener package. buildinfo's
// GitSHA is pinned empty so the revision check — which compares a checkout to a
// stamped controller, not the flag precedence under test — is skipped, exactly
// as TestRunMainLogsTheDeployPathItWasGiven does.
func evenerCheckoutFixture(t *testing.T) string {
	t.Helper()
	origSHA := buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.GitSHA = origSHA })
	buildinfo.GitSHA = ""
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize the checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	return checkout
}

// TestRunMainWiresAndLogsTheOwnExecutableDeploy pins the default at the seam the
// hub actually hands sshconn: with no deploy flags the Options carry the
// own-executable BuildBinary marked as the hub's own executable (the fact that
// lets a dirty controller deploy it, and the only source sshconn exempts), no
// BuildSource, and startup logs the defaulted source instead of the old
// no-deploy-path silence.
func TestRunMainWiresAndLogsTheOwnExecutableDeploy(t *testing.T) {
	exe := evenerArtifact(t)
	stubHubExecutable(t, exe, nil)
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("canonicalize the artifact: %v", err)
	}
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
	if got.BuildBinary == nil {
		t.Fatal("sshconn.Options.BuildBinary was not wired to this hub's own executable")
	}
	if got.BuildSource != "" {
		t.Fatalf("sshconn.Options.BuildSource = %q alongside the own-executable default", got.BuildSource)
	}
	if !got.OwnExecutable {
		t.Fatal("the own-executable default was not marked as the hub's own executable, so a dirty controller would still refuse to install it")
	}
	logged := deployPathLogLines(stderr.String())
	wantLine := "[hub] deploy path: -deploy-binary " + want + " (default: this hub's own executable)"
	if len(logged) != 1 || logged[0] != wantLine {
		t.Fatalf("deploy path log = %v, want exactly [%q]", logged, wantLine)
	}
}

// TestRunMainNoDeployWiresAndLogsNone pins -no-deploy end to end at the same
// seam: no build seam reaches sshconn (even though a deployable own executable
// and an explicit artifact are both on offer), and the startup line says the
// effective source is none and which named sources it overrode.
func TestRunMainNoDeployWiresAndLogsNone(t *testing.T) {
	exe := evenerArtifact(t)
	stubHubExecutable(t, exe, nil)
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("canonicalize the artifact: %v", err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "alone",
			args: []string{"-no-deploy"},
			want: "[hub] deploy path: -no-deploy (deploys disabled)",
		},
		{
			name: "wins over an explicit artifact",
			args: []string{"-no-deploy", "-deploy-binary", exe},
			want: "[hub] deploy path: -no-deploy (deploys disabled); ignoring -deploy-binary " + want,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, deps := newTraceMainTestDeps(t)
			var got sshconn.Options
			deps.newSSHManager = func(reg *hostreg.Registry, opts sshconn.Options) *sshconn.Manager {
				got = opts
				return sshconn.New(reg, opts)
			}
			var stderr bytes.Buffer
			args := append([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, tc.args...)
			if err := runMain(args, &stderr, deps); err != nil {
				t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
			}
			if got.BuildBinary != nil || got.BuildSource != "" || got.OwnExecutable {
				t.Fatalf("-no-deploy still wired a deploy source: BuildBinary=%v BuildSource=%q OwnExecutable=%v", got.BuildBinary != nil, got.BuildSource, got.OwnExecutable)
			}
			if !got.DeployDisabled {
				t.Fatal("-no-deploy did not tell sshconn deploying is disabled, so the installer fallback could still install on a host")
			}
			logged := deployPathLogLines(stderr.String())
			if len(logged) != 1 || logged[0] != tc.want {
				t.Fatalf("deploy path log = %v, want exactly [%q]", logged, tc.want)
			}
		})
	}
}
