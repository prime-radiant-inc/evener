package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
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

// installerRefusal drives the installer fallback to the refusal a controller
// with the given build channel and dirty stamp produces, and returns that error.
// The refusals fire before the installer is streamed, so the runner is armed to
// fail the test if the host is touched at all: a refusal that still wrote
// something would be a different defect.
func installerRefusal(t *testing.T, host hostreg.Host, help, channel, dirty string) error {
	t.Helper()
	origChannel, origDirty, origTag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = origChannel, origDirty, origTag })
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = channel, dirty, ""

	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		t.Errorf("the refused install reached the host: %v", argv)
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{DeployHelp: help})
	_, err := m.deployInstaller(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if err == nil {
		t.Fatalf("deployInstaller succeeded for channel %q with GitDirty %q", channel, dirty)
	}
	return err
}

// TestInstallerRefusalsNameSuppliedDeployHelp pins the remedy seam for the
// installer-path refusals. They used to end in "use the atomic push path or
// Options.BuildBinary" — an internal field an operator cannot act on — so the
// slice's promise that the refusals name the hub's flags held only for the
// terminal version refusal. With Options.DeployHelp supplied, both installer
// refusals carry the hub's flags instead. The release-with-no-stamped-tag
// refusal is the one that used to end in a hardcoded "use the atomic push path"
// and ignore the seam; it now carries the same clause as its siblings.
func TestInstallerRefusalsNameSuppliedDeployHelp(t *testing.T) {
	const help = "set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path>"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}

	for _, tc := range []struct {
		name    string
		channel string
		dirty   string
	}{
		{"a dirty controller", "", "true"},
		{"a channel with no publishable artifact", "nightly", ""},
		{"an empty channel (buildinfo's dev)", "", ""},
		{"a release build with no stamped tag", "release", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := installerRefusal(t, host, help, tc.channel, tc.dirty)
			if !strings.Contains(err.Error(), help) {
				t.Fatalf("installer refusal does not carry the embedder's help text %q: %v", help, err)
			}
			if strings.Contains(err.Error(), "Options.") {
				t.Fatalf("installer refusal still names a library-internal field: %v", err)
			}
		})
	}
}

// TestInstallerRefusalsKeepTheLibraryRemedy pins the other half of the seam: an
// embedder that names no flags (Options.DeployHelp empty) still sees the
// library's own sentence, so its text is what it was before the field existed.
func TestInstallerRefusalsKeepTheLibraryRemedy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	const want = "use the atomic push path or Options.BuildBinary"
	for _, tc := range []struct{ channel, dirty string }{
		{"", "true"},
		{"nightly", ""},
		{"release", ""},
	} {
		err := installerRefusal(t, host, "", tc.channel, tc.dirty)
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("library default remedy changed for channel %q dirty %q, want %q in: %v", tc.channel, tc.dirty, want, err)
		}
	}
}

// TestInstallerMovedTagRefusalNamesThePushPath pins acceptance criterion 7's
// message clause: the terminal refusal a moved channel tag produces names the
// path that does carry a provable identity, through the same Options.DeployHelp
// seam. The refusal's type and terminality are pinned by
// TestRound8InstallerVersionMismatchIsTerminal; this adds the remedy clause,
// which is the half an operator acts on.
func TestInstallerMovedTagRefusalNamesThePushPath(t *testing.T) {
	origSHA, origDirty, origChannel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = origSHA, origDirty, origChannel })
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = "newsha", "", "snapshot"

	const help = "set -deploy-binary <path> (a pre-built evener for the host's target) or -build-source <path>"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "evener-install.XXXXXX"):
			return nil, nil
		case strings.Contains(joined, "launch-check"):
			// The installer succeeded but fetched a newer snapshot than this
			// controller's commit: a moved channel tag.
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldersha","launch_flags":["api-log"]}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", DeployHelp: help})

	_, err := m.deployInstaller(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("err = %v, want ErrVersionMismatch", err)
	}
	if !strings.Contains(err.Error(), help) {
		t.Fatalf("moved-tag refusal does not name the remedy %q: %v", help, err)
	}
}
