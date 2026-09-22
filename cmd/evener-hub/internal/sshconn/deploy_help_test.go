package sshconn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// versionMismatchRefusal drives Ensure to the terminal version refusal on a
// Manager with no deploy path configured and returns the error it wrapped.
func versionMismatchRefusal(t *testing.T, opts Options) error {
	t.Helper()
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(map[string][]byte{
			"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`),
		}),
		startFn: goodStartFn(t),
	}
	opts.controllerVersionOverride = "newsha"
	m := newTestManager(t, testRegistry(t, host), fr, opts)

	_, err := m.Ensure(context.Background(), "alpha")
	if err == nil {
		t.Fatal("Ensure attached despite a version mismatch with no deploy path")
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want ErrVersionMismatch", err)
	}
	return err
}

// TestVersionMismatchRefusalKeepsDefaultRemedy pins backward compatibility: an
// embedder with no flags to name (Options.DeployHelp empty) sees exactly the
// sentence the refusal carried before this field existed.
func TestVersionMismatchRefusalKeepsDefaultRemedy(t *testing.T) {
	err := versionMismatchRefusal(t, Options{})
	const want = "and no build source is configured to deploy the controller's build; set Options.BuildSource"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("default refusal text changed, want %q in: %v", want, err)
	}
}

// TestVersionMismatchRefusalNamesSuppliedDeployHelp pins the seam: when the
// embedder supplies help text the refusal carries it and drops the library's
// internal remedy, which an operator cannot act on.
func TestVersionMismatchRefusalNamesSuppliedDeployHelp(t *testing.T) {
	const help = "set -deploy-binary <path> or -build-source <path>"
	err := versionMismatchRefusal(t, Options{DeployHelp: help})
	if !strings.Contains(err.Error(), help) {
		t.Fatalf("refusal does not carry the embedder's help text %q: %v", help, err)
	}
	if strings.Contains(err.Error(), "set Options.BuildSource") {
		t.Fatalf("refusal still names the library's internal remedy: %v", err)
	}
}

// TestValidateBuildSourceKeepsRefusals pins the exported entry point the hub
// calls at startup: it is verifyBuildSource's rules, unchanged.
func TestValidateBuildSourceKeepsRefusals(t *testing.T) {
	if _, err := ValidateBuildSource(""); err == nil || !strings.Contains(err.Error(), "no build source") {
		t.Fatalf("ValidateBuildSource(\"\") err = %v, want a missing-source error", err)
	}
	if _, err := ValidateBuildSource(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not the evener checkout") {
		t.Fatalf("ValidateBuildSource(non-checkout) err = %v, want a wrong-checkout error", err)
	}
}
