package hub

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
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
// mismatch is refused before the push (out is never written).
func TestDeployBinarySeamRefusesWrongTarget(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	goos, goarch := otherTarget(runtime.GOOS, runtime.GOARCH)
	out := filepath.Join(t.TempDir(), "evener")
	err = deployBinaryBuild(exe)(t.Context(), goos, goarch, out)
	if err == nil {
		t.Fatalf("deployBinaryBuild accepted a %s/%s artifact for %s/%s", runtime.GOOS, runtime.GOARCH, goos, goarch)
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

// TestDeployWiringPrefersDeployBinary pins the precedence rule at the Options
// seam: with both flags set the constructed Options carry BuildBinary and no
// BuildSource, so the manager's own `BuildBinary first` dispatch uses the
// operator's artifact.
func TestDeployWiringPrefersDeployBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dw := hubOptions{deployBinary: exe, buildSource: "/some/evener/checkout"}.deployWiring()
	if dw.buildBinary == nil {
		t.Fatal("deployWiring did not configure BuildBinary with both flags set")
	}
	if dw.buildSource != "" {
		t.Fatalf("deployWiring kept BuildSource %q alongside BuildBinary", dw.buildSource)
	}
	// The constructed BuildBinary is the artifact seam, not a cross-compile:
	// invoking it with the test binary's own target copies the artifact.
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

// otherTarget returns a GOOS/GOARCH pair guaranteed to differ from the given
// one. Only the arch changes, so the mismatch is deterministic on every host.
func otherTarget(goos, goarch string) (string, string) {
	if goarch == "amd64" {
		return goos, "arm64"
	}
	return goos, "amd64"
}
