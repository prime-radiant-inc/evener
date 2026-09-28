package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
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

// crossTarget returns a GOOS/GOARCH pair that differs from this process's, so a
// test can ask for a host the defaulted own executable cannot serve. Only the
// arch changes, so the pair is valid on every host.
func crossTarget() (string, string) {
	if runtime.GOARCH == "arm64" {
		return runtime.GOOS, "amd64"
	}
	return runtime.GOOS, "arm64"
}

// installRunner records every remote command and fails the installer invocation,
// so a test can prove the installer fallback was the path taken — and which ref
// it was pinned to — without scripting a whole install. Commands other than the
// installer fail as unexpected.
func installRunner(t *testing.T) *fakeRunner {
	t.Helper()
	return &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "EVENER_INSTALL_VERSION=") {
			return nil, errors.New("installer refused by the test's runner")
		}
		t.Errorf("an unexpected remote command ran before the installer: %v", argv)
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
}

// TestDefaultSourceCrossTargetFallsBackToTheInstaller pins the dispatch the
// default must not shadow: the own executable is this process's build, so a host
// on another target cannot be served by pushing it — and a controller whose
// channel has a published artifact provisions that host through the installer
// fallback, exactly as a flagless controller did before the default existed. The
// push seam never runs, and no artifact-mismatch refusal is produced.
func TestDefaultSourceCrossTargetFallsBackToTheInstaller(t *testing.T) {
	origChannel, origDirty, origTag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = origChannel, origDirty, origTag })
	// A snapshot controller on a clean tree: the installer fallback has an
	// artifact to pin, so the fallback is the path that works — the Medium this
	// pins. GitDirty is cleared with the rest of the stamp: a dirty controller's
	// installer fallback has no published artifact to pin (installerRefFor
	// refuses), which would make this test assert the terminal cross-target
	// refusal instead of the fallback.
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = "snapshot", "", ""

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	goos, goarch := crossTarget()
	builds := 0
	fr := installRunner(t)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OwnExecutable: true,
		BuildBinary: func(context.Context, string, string, string) error {
			builds++
			return nil
		},
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: goos, Arch: goarch, Home: "/home/dev"})
	if err == nil {
		t.Fatal("the cross-target deploy reported success")
	}
	if errors.Is(err, ErrDeployArtifactUnusable) {
		t.Fatalf("the cross-target default produced the push's artifact-mismatch refusal instead of reaching the installer fallback: %v", err)
	}
	if !errors.Is(err, ErrDeploy) {
		t.Fatalf("installer-path err = %v, want the installer's own refusal (the retryable ErrDeploy class)", err)
	}
	if builds != 0 {
		t.Fatalf("the push seam ran for a host the default cannot serve cross-target (builds = %d)", builds)
	}
	var pinned bool
	for _, run := range fr.recordedRuns() {
		if strings.Contains(strings.Join(run, " "), "EVENER_INSTALL_VERSION=snapshot") {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the installer fallback never ran with the controller's pinned ref (runs: %v)", fr.recordedRuns())
	}
}

// TestDefaultSourceCrossTargetUnpinnableRefusesTerminally pins the consequence
// the fallback cannot avoid: with no published artifact to pin (a dev, dirty, or
// tag-less release controller), a controller whose only source is its own
// executable can never provision a host on another target. The refusal is
// terminal and names both targets, so the reconnect loop stops instead of
// retrying the installer's retryable refusal forever (round thirteen), and the
// remedy arrives through DeployHelp.
func TestDefaultSourceCrossTargetUnpinnableRefusesTerminally(t *testing.T) {
	origChannel, origDirty, origTag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = origChannel, origDirty, origTag })
	// A dev controller: no channel, so no published artifact to pin.
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = "", "", ""

	const help = "set Options.BuildSource to the evener checkout"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	goos, goarch := crossTarget()
	builds := 0
	fr := refusingRunner(t)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OwnExecutable: true,
		DeployHelp:    help,
		BuildBinary: func(context.Context, string, string, string) error {
			builds++
			return nil
		},
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: goos, Arch: goarch, Home: "/home/dev"})
	if !errors.Is(err, errOwnExecutableCannotServe) {
		t.Fatalf("err = %v, want errOwnExecutableCannotServe", err)
	}
	if !isTerminal(err) {
		t.Fatalf("err = %v, want a terminal refusal (the same host, target, and build re-refuse identically)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("err = %v still wraps the retryable ErrDeploy, so the reconnect loop would retry it forever", err)
	}
	for _, want := range []string{runtime.GOOS + "/" + runtime.GOARCH, goos + "/" + goarch, help} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not name %q: %v", want, err)
		}
	}
	if builds != 0 {
		t.Fatalf("the push seam ran for an unservable host (builds = %d)", builds)
	}
	if runs := fr.recordedRuns(); len(runs) != 0 {
		t.Fatalf("an unpinnable fallback reached the host: %v", runs)
	}
}

// TestNamedArtifactCrossTargetKeepsThePushRefusal pins the other half of the
// dispatch: the fallback is keyed on the defaulted source, so an operator's
// -deploy-binary built for another platform still goes to the push, whose seam
// refuses it terminally naming the flag to fix. That distinction is why the
// marker is OwnExecutable and not "any binary artifact".
func TestNamedArtifactCrossTargetKeepsThePushRefusal(t *testing.T) {
	origChannel, origDirty, origTag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = origChannel, origDirty, origTag })
	// The installer fallback IS available for this build — a snapshot controller
	// on a clean tree; GitDirty is cleared with the rest of the stamp so a dirty
	// tree cannot make the premise vacuous — and it must not be taken.
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = "snapshot", "", ""

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	goos, goarch := crossTarget()
	builds := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(context.Context, string, string, string) error {
			builds++
			return fmt.Errorf("%w: -deploy-binary %q targets %s/%s, but the host needs %s/%s",
				ErrDeployArtifactUnusable, "/tmp/evener", runtime.GOOS, runtime.GOARCH, goos, goarch)
		},
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: goos, Arch: goarch, Home: "/home/dev"})
	if !errors.Is(err, ErrDeployArtifactUnusable) {
		t.Fatalf("err = %v, want the terminal artifact-mismatch refusal", err)
	}
	if !isTerminal(err) {
		t.Fatalf("err = %v, want a terminal refusal", err)
	}
	if builds != 1 {
		t.Fatalf("the push seam ran %d times, want exactly 1", builds)
	}
	for _, run := range fr.recordedRuns() {
		if joined := strings.Join(run, " "); strings.Contains(joined, "EVENER_INSTALL_VERSION=") {
			t.Fatalf("an explicit artifact reached the installer fallback: %s", joined)
		}
	}
}

// TestDefaultSourceSameTargetStillPushes pins the dispatch's other side: a host
// on this process's own target is served by pushing the own executable exactly
// as before — the fallback belongs to the cross-target pair alone.
func TestDefaultSourceSameTargetStillPushes(t *testing.T) {
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
		OwnExecutable: true,
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("own-build-bytes"), 0o755)
		},
	})

	target, err := m.deploy(context.Background(), host, Preflight{OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		t.Fatalf("same-target deploy of the own executable: %v", err)
	}
	if target != "/opt/evener/bin/evener" || builds != 1 || string(pushed) != "own-build-bytes" {
		t.Fatalf("deploy: target=%q builds=%d pushed=%q, want the own executable pushed to /opt/evener/bin/evener", target, builds, pushed)
	}
}

// TestDirtyControllerRefusesWhenTheStagingPathIsNotTheOwnExecutable pins the
// staging-path keying: Options.OwnExecutable claims BuildBinary serves this
// controller's own executable, so a Manager that would actually stage through
// BuildSource (BuildBinary nil) is judged by that path. A dirty controller with
// the marker set and a build source still refuses terminally, and nothing
// reaches the host.
func TestDirtyControllerRefusesWhenTheStagingPathIsNotTheOwnExecutable(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := refusingRunner(t)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirtyControllerVersion,
		OwnExecutable:             true,
		BuildSource:               "/some/evener/checkout",
	})

	_, err := m.deploy(context.Background(), host, Preflight{OS: runtime.GOOS, Arch: runtime.GOARCH, Home: "/home/dev"})
	if !errors.Is(err, errControllerDirty) {
		t.Fatalf("err = %v, want errControllerDirty (a build source cannot reproduce a dirty controller)", err)
	}
	if !isTerminal(err) {
		t.Fatalf("err = %v, want a terminal refusal", err)
	}
	if runs := fr.recordedRuns(); len(runs) != 0 {
		t.Fatalf("a refused dirty source reached the host: %v", runs)
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

	target, err := m.deploy(context.Background(), host, Preflight{OS: runtime.GOOS, Arch: runtime.GOARCH})
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
			return []byte(launchCheckJSON(dirtyControllerVersion)), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirtyControllerVersion,
		OwnExecutable:             true,
		BuildBinary:               writeStageBinary,
	})

	target, after, err := m.DeployForOperation(context.Background(), host, Preflight{OS: runtime.GOOS, Arch: runtime.GOARCH})
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
	// The host runs the controller's own target, so the deploy is the push the
	// post-deploy identity read then judges; a hardcoded pair here would make the
	// test depend on the architecture running it.
	fr := deployRunnerFor(t, runtime.GOOS, runtime.GOARCH,
		func(call int) ([]byte, error) {
			if call == 0 {
				// The on-disk binary before the deploy: this controller's own
				// dirty build.
				return []byte(launchCheckJSON(dirtyControllerVersion)), nil
			}
			// The artifact the deploy wrote: right platform, foreign identity.
			return []byte(launchCheckJSON("othersha")), nil
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

// launchCheckJSON renders the launch-check answer a host gives for version: its
// protocol is appwire.ProtocolVersion itself, never a literal, so a protocol
// bump cannot leave this fixture speaking the previous version — which reads as
// an old-protocol host and fails the post-deploy agreement check with "host
// protocol incompatible" (what a hardcoded "evener-appwire-v5" did when the
// protocol moved to v6 on main).
func launchCheckJSON(version string) string {
	return fmt.Sprintf(`{"protocol":%q,"version":%q,"launch_flags":["api-log"]}`, appwire.ProtocolVersion, version)
}
