package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// These tests pin the binary-artifact half of the dirty-controller rule: the
// hub's own executable (and an operator-supplied -deploy-binary) is declared
// through Options.BinaryArtifact, and such an artifact IS the build this
// controller runs, so deploying it from a dirty controller is allowed where
// compiling a source tree from one is not.

const dirtyControllerVersion = "abc1234-dirty"

// TestDirtyControllerRefusesABuildSource pins the half of the rule that does not
// change: -build-source cannot reproduce a "<sha>-dirty" controller (the source
// checkout cannot be proven to match the uncommitted tree the running build
// compiled), so the terminal refusal stands.
func TestDirtyControllerRefusesABuildSource(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	m := newTestManager(t, testRegistry(t, host), refusingRunner(t), Options{
		controllerVersionOverride: dirtyControllerVersion,
		BuildSource:               "/some/evener/checkout",
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if !errors.Is(err, errControllerDirty) {
		t.Fatalf("err = %v, want errControllerDirty", err)
	}
}

// TestDirtyControllerRefusesAnUndeclaredBuildBinary pins the guard on the other
// side of Options.BinaryArtifact: a BuildBinary that is not declared a binary
// artifact is an embedder's own compile of the controller's tree — exactly the
// build a dirty version cannot prove — so the refusal stays, and no build runs.
func TestDirtyControllerRefusesAnUndeclaredBuildBinary(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	builds := 0
	m := newTestManager(t, testRegistry(t, host), refusingRunner(t), Options{
		controllerVersionOverride: dirtyControllerVersion,
		BuildBinary: func(context.Context, string, string, string) error {
			builds++
			return nil
		},
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
	if !errors.Is(err, errControllerDirty) {
		t.Fatalf("err = %v, want errControllerDirty", err)
	}
	if builds != 0 {
		t.Fatalf("build attempts = %d, want 0 (an undeclared build source cannot be proven to reproduce a dirty controller)", builds)
	}
}

// TestDirtyControllerDeploysABinaryArtifact pins the new half: a declared binary
// artifact — the hub's own executable, or an operator-named -deploy-binary —
// carries the bytes themselves, so a dirty controller installs it instead of
// refusing. The deploy runs the whole push path (resolve, build seam, atomic
// pipe) and the staged bytes are what reaches the host.
func TestDirtyControllerDeploysABinaryArtifact(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	var pushed []byte
	builds := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				pushed, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirtyControllerVersion,
		BinaryArtifact:            true,
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("own-build-bytes"), 0o755)
		},
	})

	target, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("deploy of a declared binary artifact from a dirty controller: %v", err)
	}
	if target != "/opt/evener/bin/evener" {
		t.Fatalf("resolved target = %q, want /opt/evener/bin/evener", target)
	}
	if builds != 1 {
		t.Fatalf("artifact staging calls = %d, want 1", builds)
	}
	if string(pushed) != "own-build-bytes" {
		t.Fatalf("pushed bytes = %q, want the declared artifact's bytes", pushed)
	}
}

// TestDirtyControllerArtifactDeployConvergesThroughTheOperation pins the deploy
// pipeline's own entry point (DeployForOperation, which the hub's deploy worker
// runs): a dirty controller deploying a declared binary artifact installs it and
// passes the post-deploy identity read — the artifact reports the controller's
// own dirty version, so the terminal unstamped-build refusal does not fire.
func TestDirtyControllerArtifactDeployConvergesThroughTheOperation(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(joined, "launch-check"):
			// The artifact the deploy installed reports the controller's own
			// dirty build, as the hub's own executable does by construction.
			return []byte(`{"protocol":"evener-appwire-v5","version":"` + dirtyControllerVersion + `","launch_flags":["api-log"]}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirtyControllerVersion,
		BinaryArtifact:            true,
		BuildBinary:               writeStageBinary,
	})

	target, after, err := m.DeployForOperation(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("DeployForOperation: %v", err)
	}
	if target != "/opt/evener/bin/evener" {
		t.Fatalf("resolved target = %q, want /opt/evener/bin/evener", target)
	}
	if after.Version != dirtyControllerVersion {
		t.Fatalf("post-deploy launch-check version = %q, want %q", after.Version, dirtyControllerVersion)
	}
}

// TestDeployDisabledRefusesEveryDeployPath pins the opt-out's whole meaning: a
// controller started with deploying turned off installs nothing on a host, so a
// host that needs a build is left on its own even when the installer fallback
// could pin a published artifact (a snapshot controller), and an explicit deploy
// attempt is refused terminally with the remedy named instead of quietly falling
// back to install.sh.
func TestDeployDisabledRefusesEveryDeployPath(t *testing.T) {
	origChannel, origDirty, origTag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = origChannel, origDirty, origTag })
	// A snapshot controller: without the opt-out, canDeploy accepts the installer
	// fallback, so this is the case that would otherwise write to the host.
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = "snapshot", "", ""

	const help = "start the hub without -no-deploy to deploy again"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	facts := Preflight{
		Host:             host.Name,
		LaunchCheckKnown: true,
		Protocol:         appwire.ProtocolVersion,
		Version:          "othersha",
		LaunchFlags:      []string{requiredLaunchFlag},
	}

	fr := refusingRunner(t)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		DeployDisabled: true,
		DeployHelp:     help,
		sleep:          func(context.Context, time.Duration) error { return nil },
	})

	// The decision ladder never picks a deploy: nothing may be installed, so a
	// version difference is left to the attach path, which reports it.
	if m.canDeploy() {
		t.Fatal("DeployDisabled left canDeploy true, so the installer fallback could still write to a host")
	}
	if m.deployRequired(host.Name, facts, "controller-sha") {
		t.Fatal("DeployDisabled still required a deploy this controller has turned off")
	}

	// The explicit operation reaches deploy directly; it must be refused rather
	// than silently converted into the installer fallback.
	_, err := m.deploy(context.Background(), host, facts)
	if !errors.Is(err, errDeployDisabled) {
		t.Fatalf("deploy err = %v, want errDeployDisabled", err)
	}
	if !isTerminal(err) {
		t.Fatalf("deploy err = %v, want a terminal refusal (retrying re-refuses the same setting)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("deploy err = %v still wraps ErrDeploy, so the supervisor would retry a deliberate setting", err)
	}
	if !strings.Contains(err.Error(), help) {
		t.Fatalf("refusal does not name the remedy: %v", err)
	}
	if _, _, err := m.DeployForOperation(context.Background(), host, facts); !errors.Is(err, errDeployDisabled) {
		t.Fatalf("DeployForOperation err = %v, want errDeployDisabled", err)
	}
	if runs := fr.recordedRuns(); len(runs) != 0 {
		t.Fatalf("a disabled deploy reached the host: %v", runs)
	}
}
