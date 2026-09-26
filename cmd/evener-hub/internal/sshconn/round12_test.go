package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

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
		case strings.Contains(joined, "command -v evener >/dev/null 2>&1"):
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
			return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`), nil
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
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary:               writeStageBinary,
	})

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

// restartBareScript scripts the standard ad-hoc restart sequence for a hub at pid
// 4242: what the port probe answers, what the process-exit probe answers, and the
// two side effects the test observes.
type restartBareScript struct {
	portProbe   func() []byte
	processExit func() []byte
	onKill      func()
	onRelaunch  func()
}

func restartBareFixture(port string, script restartBareScript) *fakeRunner {
	return &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "id -un"):
			return []byte("dev\n"), nil
		case strings.Contains(joined, "ps -o user= -p "):
			return []byte("dev\n"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			return script.portProbe(), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return script.processExit(), nil
		case strings.Contains(joined, "kill -- 4242"):
			if script.onKill != nil {
				script.onKill()
			}
			return nil, nil
		case strings.Contains(joined, "nohup"):
			if script.onRelaunch != nil {
				script.onRelaunch()
			}
			return nil, nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
}

// TestRound12RelaunchWaitsForTheOldProcessToExit pins the round-twelve Medium
// that the bare restart waited only for the TCP listener to disappear before
// relaunching. The hub closes its listener at the START of its graceful shutdown
// and keeps hub.lock while it drains, so the replacement could fail to acquire
// the lock and exit, leaving the host with no hub. The relaunch must wait for the
// old pid itself to exit.
func TestRound12RelaunchWaitsForTheOldProcessToExit(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	exitCalls := 0
	relaunched := 0
	fr := restartBareFixture(port, restartBareScript{
		portProbe: func() []byte {
			if !killed {
				return []byte("4242\n")
			}
			return []byte(noListenerMarker + "\n") // the listener closes first
		},
		processExit: func() []byte {
			exitCalls++
			if exitCalls <= 2 {
				return []byte(pidAliveMarker + "\n") // still draining, still holding hub.lock
			}
			return []byte(pidGoneMarker + "\n")
		},
		onKill:     func() { killed = true },
		onRelaunch: func() { relaunched++ },
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
		t.Fatalf("restartBare: %v", err)
	}
	if relaunched != 1 {
		t.Fatalf("relaunches = %d, want 1", relaunched)
	}
	if exitCalls < 3 {
		t.Fatalf("process-exit probes = %d, want >= 3 (two alive answers then gone)", exitCalls)
	}
	// Ordering: the relaunch must come after the process was observed to exit, and
	// the kill must be the validated `kill -- <pid>` form.
	runs := fr.recordedRuns()
	lastExitProbe, nohupAt, killAt := -1, -1, -1
	for i, argv := range runs {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "kill -s 0 -- 4242") {
			lastExitProbe = i
		}
		if strings.Contains(joined, "nohup") && nohupAt < 0 {
			nohupAt = i
		}
		if strings.HasSuffix(joined, "kill -- 4242") {
			killAt = i
		}
	}
	if killAt < 0 {
		t.Fatalf("kill is not rendered as the validated `kill -- <pid>` form: %v", runs)
	}
	if nohupAt < 0 || lastExitProbe < 0 || nohupAt < lastExitProbe {
		t.Fatalf("relaunch (run %d) did not wait for the process-exit probe (run %d): %v", nohupAt, lastExitProbe, runs)
	}
}

// TestRound12RelaunchRefusedWhileTheOldProcessStillRuns is the fail-closed half:
// a process that never exits after the kill must not be raced by a relaunch. The
// restart fails with ErrRestart (so the ladder retries, with the relaunch already
// recorded) instead of starting a second hub against a held hub.lock.
func TestRound12RelaunchRefusedWhileTheOldProcessStillRuns(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	relaunched := 0
	fr := restartBareFixture(port, restartBareScript{
		portProbe: func() []byte {
			if !killed {
				return []byte("4242\n")
			}
			return []byte(noListenerMarker + "\n")
		},
		processExit: func() []byte { return []byte(pidAliveMarker + "\n") },
		onKill:      func() { killed = true },
		onRelaunch:  func() { relaunched++ },
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("restartBare err = %v, want ErrRestart (the old process never exited)", err)
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("error does not name the still-running process: %v", err)
	}
	if relaunched != 0 {
		t.Fatalf("relaunches = %d, want 0 (the old hub still holds hub.lock)", relaunched)
	}
	if got := m.pendingRestart(host.Name).command; !strings.Contains(got, "nohup") {
		t.Fatalf("pending restart = %q, want the recorded relaunch", got)
	}
}

// TestRound12ListenerProbeRejectsHostilePIDs pins the round-twelve security
// finding: any non-marker line was accepted as a PID, and that value flowed into
// `kill`, `lsof -p`, `ps -p`, and `/proc/<pid>/cmdline` — where a leading dash is
// an option and a slash or semicolon is a path fragment or a command separator,
// not a digit. A hostile or corrupted probe answer must be refused at the parse
// boundary, so no command is ever built from it.
func TestRound12ListenerProbeRejectsHostilePIDs(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	hostile := []string{"-9", "; rm -rf /", "4242; rm -rf /", "/etc/passwd", "$(id -u)", "4242 4242", "--"}
	// Both shapes: the hostile line alone, and the hostile line beside a real pid
	// (which must not be read as "one unknown listener among them" either).
	for _, out := range []string{"", "4242\n"} {
		for _, line := range hostile {
			t.Run(fmt.Sprintf("%q+%q", out, line), func(t *testing.T) {
				fr := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
					return []byte(out + line + "\n"), nil
				}}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})

				pid, err := m.findHubPID(context.Background(), host, port)
				if !errors.Is(err, ErrRestart) {
					t.Fatalf("findHubPID err = %v, want ErrRestart for the probe answer %q", err, line)
				}
				if pid != "" {
					t.Fatalf("findHubPID returned the non-numeric pid %q", pid)
				}
				if !strings.Contains(err.Error(), "non-numeric") {
					t.Fatalf("error does not name the unusable probe answer: %v", err)
				}
				runs := fr.recordedRuns()
				if len(runs) != 1 {
					t.Fatalf("Run calls = %d, want 1 (the refused pid must not reach another command): %v", len(runs), runs)
				}
				// The only command issued is the probe itself: nothing was built from
				// the refused line. The remote command is the last ssh argument (the
				// earlier ones are ssh's own options).
				remote := runs[0][len(runs[0])-1]
				if remote != listenerProbeRemote(port) {
					t.Fatalf("a command was built after the refusal: %q", remote)
				}
			})
		}
	}
}

// TestRound12PIDCommandsAreStructurallySafe pins the structural half of the same
// finding: every builder that interpolates a host-derived pid into a remote
// command goes through numericPID, the kill is issued as `kill -- <pid>`, and the
// /proc path is built only from a validated pid. A future caller that bypasses
// the probe's parse boundary therefore still cannot smuggle an option or a path
// fragment into a command.
func TestRound12PIDCommandsAreStructurallySafe(t *testing.T) {
	for _, bad := range []string{"", " ", "-9", "--", "4242; rm -rf /", "/etc/passwd", "42 42", "$(id -u)", "0x10", "42\n"} {
		if _, err := numericPID(bad); err == nil || !errors.Is(err, ErrRestart) {
			t.Errorf("numericPID(%q) = %v, want ErrRestart", bad, err)
		}
	}
	for _, good := range []string{"0", "4242", "999999"} {
		if got, err := numericPID(good); err != nil || got != good {
			t.Errorf("numericPID(%q) = (%q,%v), want (%q,nil)", good, got, err, good)
		}
	}

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	// Each seam that builds a command around a pid refuses a non-numeric one
	// without running anything.
	if _, err := m.recoverHubArgvRaw(context.Background(), host, "-9"); !errors.Is(err, ErrRestart) {
		t.Fatalf("recoverHubArgvRaw err = %v, want ErrRestart", err)
	}
	if _, err := m.hubListenerAddrs(context.Background(), host, "/etc/passwd", hubPort(defaultHubAddr)); !errors.Is(err, ErrRestart) {
		t.Fatalf("hubListenerAddrs err = %v, want ErrRestart", err)
	}
	if err := m.stopAndRelaunch(context.Background(), host, hubPort(defaultHubAddr), "-9", []string{"evener", "hub"}, hubIdentity{}); !errors.Is(err, ErrRestart) {
		t.Fatalf("stopAndRelaunch err = %v, want ErrRestart", err)
	}
	if got := len(fr.recordedRuns()); got != 0 {
		t.Fatalf("Run calls = %d, want 0 (a refused pid must not reach any command): %v", got, fr.recordedRuns())
	}

	// The builders read a pid as a bare word in the positions where an option or a
	// path would otherwise be read from it.
	if got := hubArgvRemote("4242"); !strings.Contains(got, "cat /proc/4242/cmdline") {
		t.Fatalf("hubArgvRemote = %q, want the /proc/<pid>/cmdline path", got)
	}
	if got := processAliveRemote("4242"); !strings.HasPrefix(got, "if kill -s 0 -- 4242 ") {
		t.Fatalf("processAliveRemote = %q, want `kill -s 0 -- <pid>`", got)
	}
}

// TestRound12UnrelatedHealthBodyIsNotTheHostsHub pins the round-twelve Medium
// that any JSON object with a non-empty version was accepted as hub health. An
// unrelated service answering on the hub's loopback port with the expected
// version would make version auto-match skip recovery, satisfy restart
// verification, and receive the hub auth token during attach. A body must carry
// the hub REST contract version and report the address this controller
// configured.
func TestRound12UnrelatedHealthBodyIsNotTheHostsHub(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	for _, body := range []string{
		`{"version":"newsha"}`,
		`{"status":"ok","version":"newsha"}`,
		`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.2:9180"}`,
		`{"version":"newsha","mobile_api_version":7,"hub_addr":"127.0.0.1:9180"}`,
	} {
		fr := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
			return []byte(body), nil
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})
		if got, ok := m.probeRunningHub(context.Background(), host); ok {
			t.Fatalf("an unrelated body was read as the host's hub: %s -> %+v", body, got)
		}
	}

	// The host's own hub, reporting the configured endpoint, is recognized.
	fr := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
		return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"0.0.0.0:9180"}`), nil
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha"})
	got, ok := m.probeRunningHub(context.Background(), host)
	if !ok || got.version != "newsha" {
		t.Fatalf("the host's hub was not recognized: (%+v,%v)", got, ok)
	}
}
