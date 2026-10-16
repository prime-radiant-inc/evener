package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/remoteinstall"
)

// TestRound12InstallerPinsDefaultDirsAgainstInheritedEnv pins the round-twelve
// Medium that the default-path install left PREFIX/BINDIR/EVENER_SHARE_BINDIR to
// the remote shell environment, while the manager recorded and probed
// $HOME/.local/bin/evener. A non-interactive ssh session still carries whatever
// the server environment or the ssh user's login setup exports, so an inherited
// PREFIX (say /usr/local) installed the binary somewhere the manager never
// probes, records, or relaunches — while the deploy reported success. The default
// dirs are now passed explicitly; install.sh gives BINDIR and EVENER_SHARE_BINDIR
// precedence over PREFIX (install.sh:7-18), so the layout is pinned to the run
// target the manager records.
func TestRound12InstallerPinsDefaultDirsAgainstInheritedEnv(t *testing.T) {
	// The fix's premise, pinned: install.sh derives its two directories from
	// PREFIX only as a FALLBACK. If that ever changes, this fails and the fix must
	// change with it rather than silently relying on an inherited variable.
	for _, line := range []string{
		"bindir=${BINDIR:-$prefix/bin}",
		"share_bindir=${EVENER_SHARE_BINDIR:-$prefix/share/evener/bin}",
	} {
		if !strings.Contains(string(remoteinstall.Script), line) {
			t.Fatalf("install.sh no longer treats BINDIR/EVENER_SHARE_BINDIR as the override for PREFIX (missing %q); an inherited PREFIX could install outside the recorded run target", line)
		}
	}

	const home = "/home/dev"
	bindir, shareBindir, target, err := installerDirs(hostreg.Host{Name: "alpha"}, Preflight{Home: home})
	if err != nil {
		t.Fatalf("installerDirs(default): %v", err)
	}
	if target != "/home/dev/.local/bin/evener" {
		t.Fatalf("run target = %q, want the installer default ~/.local/bin/evener", target)
	}
	got := remoteinstall.Command("snapshot", "", bindir, shareBindir)
	for _, want := range []string{"BINDIR=" + bindir, "EVENER_SHARE_BINDIR=" + shareBindir} {
		if !strings.Contains(got, want) {
			t.Fatalf("installer command does not pin %s: %q", want, got)
		}
	}
	// The hub passes no PREFIX, so the variable is pinned to empty rather than
	// left out: with BINDIR/EVENER_SHARE_BINDIR already pinned, an inherited
	// remote PREFIX could still move install.sh's fallback layout, and empty is
	// what makes install.sh compute the documented default.
	if !strings.Contains(got, "PREFIX=''") {
		t.Fatalf("installer command does not pin PREFIX to empty, so an inherited remote PREFIX could still move the fallback layout: %q", got)
	}
}

// TestRound12InstallerCommandVerifiesByteCount pins the round-twelve Medium that
// the embedded installer was streamed with `cat > "$tmp" && sh "$tmp"` and no
// byte-count check, unlike pushBinaryRemote. ssh reports a dropped stream as a
// successful EOF, so `cat` exits 0 on a partial transfer and a truncated
// installer executed as a valid partial script, leaving a half-installed
// share/evener/bin. The count is checked — before the script is handed to sh, not
// after — exactly as the push path checks the binary it streams.
func TestRound12InstallerCommandVerifiesByteCount(t *testing.T) {
	got := remoteinstall.Command("v1.2.3", "", "/opt/evener/bin", "/opt/evener/share/evener/bin")
	check := "v=$(wc -c < \"$tmp\" | tr -d '[:space:]') && [ \"$v\" = " + strconv.Itoa(len(remoteinstall.Script)) + " ]"
	if !strings.Contains(got, check) {
		t.Fatalf("the installer command does not check the streamed script's byte count: %q", got)
	}
	// The check must gate the execution: `cat > "$tmp" && <count> && env … sh "$tmp"`.
	// A check that ran after `sh` (or in a pipeline) would come too late.
	writeIdx := strings.Index(got, "cat > \"$tmp\"")
	checkIdx := strings.Index(got, check)
	shIdx := strings.Index(got, "sh \"$tmp\"")
	if writeIdx < 0 || checkIdx < 0 || shIdx < 0 {
		t.Fatalf("the installer command lost a step of the handoff: %q", got)
	}
	if writeIdx >= checkIdx || checkIdx >= shIdx {
		t.Fatalf("byte-count check is not between the write and the execution: %q", got)
	}
	if !strings.Contains(got[writeIdx:shIdx], "&& "+check+" && env ") {
		t.Fatalf("the byte-count check does not gate the execution: %q", got)
	}

	// The count the command checks is bound to the script it streams (the size
	// is not a parameter), so a command that counts any length other than the
	// embedded copy's is not representable; the end-to-end deploy test pins the
	// same number deployInstaller actually sends.
	if !strings.Contains(got, fmt.Sprintf("[ \"$v\" = %d ]", len(remoteinstall.Script))) {
		t.Fatalf("the installer command does not pin the embedded script's length (%d): %q", len(remoteinstall.Script), got)
	}
}

// TestRound12PreflightProbesInstallerDefaultWithoutADeploy pins the round-twelve
// Medium that the installer-default fallback probe ran only when m.canDeploy()
// was true, so a controller with no build source (or an unpublishable installer
// ref) could not discover a perfectly good installed binary that is simply off
// the non-interactive PATH — preflight reported errExecutableMissing and the
// attach failed for a host that needed no deploy at all. Only the later
// deploy decision is gated on canDeploy.
func TestRound12PreflightProbesInstallerDefaultWithoutADeploy(t *testing.T) {
	const resolved = "/home/dev/.local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, resolved+" launch-check"):
			return []byte(goodLaunchCheck), nil
		case strings.Contains(joined, " evener launch-check"):
			// The non-interactive PATH carries no evener.
			return []byte("sh: 1: evener: not found\n"), exitStatus(t, 127)
		case strings.Contains(joined, executableProbeRemote("evener")):
			// The dedicated executable probe: absent.
			return nil, exitStatus(t, 1)
		case strings.Contains(joined, "[ -f ") && strings.Contains(joined, ".local/bin/evener"):
			return []byte(resolved + "\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if m.canDeploy() {
		t.Fatal("test premise: an unstamped controller with no build source must not be able to deploy")
	}

	facts, err := m.preflight(context.Background(), host)
	if err != nil {
		t.Fatalf("preflight: %v (an installed binary off the non-interactive PATH must be discovered even with no deploy configured)", err)
	}
	if !facts.LaunchCheckKnown || facts.Version != "dev" {
		t.Fatalf("preflight did not read the installer-default binary's contract: %+v", facts)
	}
	if got := m.resolvedTarget(host.Name); got != resolved {
		t.Fatalf("preflight did not record the discovered target: got %q, want %q", got, resolved)
	}
}

// TestRound12DirtyVersionIsNotAVersionIdentity pins the round-twelve Medium that
// only empty and "dev" versions were treated as unverifiable, while a dirty
// build's "<sha>-dirty" is equally not a content identity: two dirty checkouts at
// the same commit carry different code and report the same version, so a host
// running one of them compared equal to the controller's own build and skipped
// the deploy. A dirty controller version therefore deploys its own build once per
// Manager (like "dev"), and a clean SHA still matches literally.
func TestRound12DirtyVersionIsNotAVersionIdentity(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	m := newTestManager(t, testRegistry(t, host), &fakeRunner{}, Options{BuildBinary: writeStageBinary})

	const dirty = "abc1234-dirty"
	facts := Preflight{
		Host:             host.Name,
		LaunchCheckKnown: true,
		Protocol:         appwire.ProtocolVersion,
		Version:          dirty,
		LaunchFlags:      []string{requiredLaunchFlag},
	}
	if !m.deployRequired(host.Name, facts, dirty) {
		t.Fatal("a dirty controller's version matched the host's identical version, so a different dirty checkout would have been trusted without deploying")
	}
	// Once this Manager has installed its own build, the identity question is
	// settled for its lifetime (the dev rule), so reconnects do not rebuild.
	m.markDevDeployed(host.Name)
	if m.deployRequired(host.Name, facts, dirty) {
		t.Fatal("a dirty controller re-deploys on every Ensure instead of trusting the build it installed")
	}

	// A clean version is still an identity: the same SHA on both sides is a match.
	facts.Version = "abc1234"
	if m.deployRequired(host.Name, facts, "abc1234") {
		t.Fatal("a clean stamped version was treated as unverifiable")
	}
	if !m.deployRequired(host.Name, facts, "othersha") {
		t.Fatal("a differing clean version did not require a deploy")
	}
}

// TestRound12PendingRestartSurvivesAnUnknownIdentity pins the round-twelve
// Medium that a pending restart was cleared when sameProcessAs returned false —
// which it also does when EITHER process lacks started_at. A failed restart
// followed by an old hub answering with the expected version and no start
// timestamp therefore cleared the pending marker and the next Ensure attached to
// the process the restart was supposed to replace. Unknown identities are
// unresolved: the marker stays and the restart is retried.
func TestRound12PendingRestartSurvivesAnUnknownIdentity(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	restarts := 0
	healthCalls := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"dev","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			restarts++
			return []byte("Failed to restart evener-hub.service: Interactive authentication required.\n"), errors.New("exit status 1")
		case strings.Contains(joined, "api/health"):
			healthCalls++
			if healthCalls == 1 {
				// The hub the restart is meant to replace: a known identity.
				return []byte(`{"version":"dev","started_at":"2026-01-01T00:00:00Z","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			}
			// Later probes answer with the expected version but no start time: an
			// identity that proves nothing about which process is serving.
			return []byte(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "dev")
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 1 err = %v, want ErrRestart (the restart command failed)", err)
	}
	if restarts != 1 {
		t.Fatalf("systemctl restarts after Ensure 1 = %d, want 1", restarts)
	}

	// The old process still answers with the expected version, but its identity is
	// unknown: the pending restart must NOT be read as settled.
	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 2 err = %v, want ErrRestart (an unknown identity must leave the pending restart unresolved)", err)
	}
	if restarts != 2 {
		t.Fatalf("systemctl restarts after Ensure 2 = %d, want 2 (the pending restart must be retried)", restarts)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("bridge Start calls = %d, want 0 (never attach to a process of unknown identity)", got)
	}
}
