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

// These tests pin the dirty-controller rule's one exemption: only this
// controller's own executable — Options.OwnExecutable, the hub's default deploy
// source — is installable from a dirty build. A source checkout cannot be
// reproduced, and an operator-named artifact cannot be told apart from a foreign
// dirty build ("<sha>-dirty" is every dirty tree at that commit), so both stay
// refused terminally. Only the dirty controller is restricted: a clean one
// deploys a named artifact exactly as it always did.

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

// TestDirtyControllerRefusesANamedBuildBinary pins the guard on the other side of
// Options.OwnExecutable: a BuildBinary that is not declared to be the
// controller's own executable — an operator's -deploy-binary, or an embedder's
// own compile — is exactly the build a dirty version cannot prove, so the
// refusal stays, and no build runs.
func TestDirtyControllerRefusesANamedBuildBinary(t *testing.T) {
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

// TestCleanControllerDeploysANamedBuildBinary pins the scope of the narrowed
// rule from the other side: only a DIRTY controller has to refuse a named
// artifact. With a reproducible version, a named artifact deploys exactly as it
// did before — the pre-push checks judge its evener identity and host target,
// and the post-deploy read judges the build the host reports (errDeployUnstamped)
// — so the rule reads "dirty AND not this controller's own executable", not
// "artifacts are second-class".
func TestCleanControllerDeploysANamedBuildBinary(t *testing.T) {
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
		controllerVersionOverride: "abc1234",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("named-artifact-bytes"), 0o755)
		},
	})

	target, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("a clean controller refused a named binary artifact: %v", err)
	}
	if target != "/opt/evener/bin/evener" || builds != 1 || string(pushed) != "named-artifact-bytes" {
		t.Fatalf("deploy: target=%q builds=%d pushed=%q, want the named artifact pushed to /opt/evener/bin/evener", target, builds, pushed)
	}
}

// TestDirtyControllerDeploysItsOwnExecutable pins the exemption: the
// controller's own executable carries the bytes themselves (the hub holds the
// file to the bytes it adopted), so a dirty controller installs it instead of
// refusing. The deploy runs the whole push path (resolve, build seam, atomic
// pipe) and the staged bytes are what reaches the host.
func TestDirtyControllerDeploysItsOwnExecutable(t *testing.T) {
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
		OwnExecutable:             true,
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("own-build-bytes"), 0o755)
		},
	})

	target, err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("deploy of the controller's own executable from a dirty controller: %v", err)
	}
	if target != "/opt/evener/bin/evener" {
		t.Fatalf("resolved target = %q, want /opt/evener/bin/evener", target)
	}
	if builds != 1 {
		t.Fatalf("artifact staging calls = %d, want 1", builds)
	}
	if string(pushed) != "own-build-bytes" {
		t.Fatalf("pushed bytes = %q, want the own executable's bytes", pushed)
	}
}

// TestDirtyControllerOwnExecutableDeployConvergesThroughTheOperation pins the
// deploy pipeline's own entry point (DeployForOperation, which the hub's deploy
// worker runs): a dirty controller deploying its own executable installs it and
// passes the post-deploy identity read — the artifact reports the controller's
// own dirty version, so the terminal unstamped-build refusal does not fire.
func TestDirtyControllerOwnExecutableDeployConvergesThroughTheOperation(t *testing.T) {
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
		OwnExecutable:             true,
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

// TestDirtyControllerOwnExecutableMismatchRefusesUnstamped pins the guard that
// must survive the dirty relaxation: the own executable is deployable from a
// dirty controller because it IS the controller's build — and when the build the
// host ends up with is a different one after all (nothing in sshconn can prove
// the bytes, so the check is the host's own report), the post-deploy identity
// read still refuses terminally (errDeployUnstamped) instead of attaching the
// host to that build.
func TestDirtyControllerOwnExecutableMismatchRefusesUnstamped(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				// The on-disk binary before the deploy: this controller's own
				// dirty build.
				return []byte(`{"protocol":"evener-appwire-v5","version":"` + dirtyControllerVersion + `","launch_flags":["api-log"]}`), nil
			}
			// The artifact the deploy wrote: right platform, foreign identity.
			return []byte(`{"protocol":"evener-appwire-v5","version":"othersha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirtyControllerVersion,
		OwnExecutable:             true,
		BuildBinary:               writeStageBinary,
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, errDeployUnstamped) {
		t.Fatalf("Ensure err = %v, want errDeployUnstamped", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("Ensure err = %v still wraps ErrDeploy, so the supervisor would re-push the same foreign artifact forever", err)
	}
	if !isTerminal(err) {
		t.Fatalf("Ensure err = %v, want a terminal refusal", err)
	}
}
