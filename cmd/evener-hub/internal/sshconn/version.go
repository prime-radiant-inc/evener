package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Bounds for the restart stop/health waits. The stop wait is short: with
// nothing actively streaming the lock frees within milliseconds, and SIGTERM's
// graceful drain is capped at ~5s.
const (
	restartStopAttempts   = 50
	restartStopInterval   = 100 * time.Millisecond
	restartHealthAttempts = 30
	restartHealthInterval = 200 * time.Millisecond
)

const (
	systemctlListUnits     = "systemctl list-units --type=service --all --no-legend --plain"
	systemctlListUnitsUser = "systemctl --user list-units --type=service --all --no-legend --plain"
)

// explicitHostAddr is the listen address the host's registration or the manager
// options name, or "" when neither does. It does not select the attach
// destination.
func explicitHostAddr(o Options, host hostreg.Host) string {
	if a := strings.TrimSpace(host.Addr); a != "" {
		return a
	}
	return strings.TrimSpace(o.HubListenAddr)
}

// supervisorKind classifies how a host hub is managed.
type supervisorKind string

const (
	supervisorNone        supervisorKind = ""
	supervisorLaunchd     supervisorKind = "launchd"
	supervisorSystemd     supervisorKind = "systemd"
	supervisorSystemdUser supervisorKind = "systemd-user"
)

// supervisor is a detected hub manager: a launchd label or a systemd unit.
type supervisor struct {
	kind  supervisorKind
	label string
}

// restartRemote is the remote command that restarts the supervised hub. ok is
// false when no safe supervised command can be built — launchd with no numeric
// uid, or a label outside the bare-safe set — and the caller then refuses
// rather than naming the label in a command.
//
// The darwin domain argument is `gui/<uid>/<label>` with the numeric uid
// resolved in preflight and passed as one bare-safe word: a `$(id -u)`
// substitution would be single-quoted into a literal by the quoting rule and
// never expand, and the label is host-derived so emitting it raw would be the
// injection the quoted path avoids (spec 04, §"Stop/restart mechanics").
func (s supervisor) restartRemote(uid string) (string, bool) {
	switch s.kind {
	case supervisorLaunchd:
		if !isNumericUID(uid) || !isBareSafeLabel(s.label) {
			return "", false
		}
		return "launchctl kickstart -k gui/" + uid + "/" + s.label, true
	case supervisorSystemd:
		return "systemctl restart " + shellquote.RemoteWord(s.label), true
	case supervisorSystemdUser:
		return "systemctl --user restart " + shellquote.RemoteWord(s.label), true
	default:
		return "", false
	}
}

// startRemote is the supervised hub START, used by the first-attach bootstrap:
// the supervisor that owns the hub is asked to bring it up (systemd `start`,
// launchd `kickstart` without the killing -k flag). ok is false under the same conditions as
// restartRemote, so an unbuildable command falls through to the ad hoc path.
func (s supervisor) startRemote(uid string) (string, bool) {
	switch s.kind {
	case supervisorLaunchd:
		if !isNumericUID(uid) || !isBareSafeLabel(s.label) {
			return "", false
		}
		return "launchctl kickstart gui/" + uid + "/" + s.label, true
	case supervisorSystemd:
		return "systemctl start " + shellquote.RemoteWord(s.label), true
	case supervisorSystemdUser:
		return "systemctl --user start " + shellquote.RemoteWord(s.label), true
	default:
		return "", false
	}
}

// isNumericUID reports whether s is a non-empty run of ASCII digits.
func isNumericUID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// numericPID validates a host-derived pid before it is interpolated into a
// remote command. A pid comes from the host's listener probe, so it is
// host-controlled data: every downstream use — `kill`, `lsof -p`, `ps -p`, and
// the `/proc/<pid>/...` path — must only ever see a bare run of digits. A line
// like `-9` would be read as an option and a `/` or `;` as a path fragment or a
// command separator, which is exactly the injection the quoting rule cannot
// catch (a quoted `-9` is still an option, and a quoted path is still a path).
// This is the structural guard at each seam that renders one, so a caller cannot
// reintroduce the hole by skipping validation.
func numericPID(pid string) (string, error) {
	if !isNumericUID(pid) {
		return "", fmt.Errorf("%w: host-derived pid %q is not a numeric pid; refusing to build a remote command around it", ErrRestart, pid)
	}
	return pid, nil
}

// isBareSafeLabel reports whether a supervisor label can be passed as one
// bare-safe word ([A-Za-z0-9_.-], no `/`). The launchd domain argument
// `gui/<uid>/<label>` has to be a single unquoted word, so a label outside this
// set refuses the supervised path instead of being interpolated into the remote
// shell.
func isBareSafeLabel(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

// restartStatusIsAdvisory reports whether a nonzero restart-command status is
// only diagnostic. Repo docs note `launchctl kickstart` can report failure even
// when the restart succeeded (docs/evener-hub-remote-operations.md:290-293), so
// its status must not short-circuit the health probe. systemd's `restart` status
// stays authoritative: it fails for a real reason (a missing unit, no polkit
// authorization) that a still-healthy old process would otherwise mask.
func (s supervisor) restartStatusIsAdvisory() bool {
	return s.kind == supervisorLaunchd
}

// parseLaunchdHubs classifies launchd jobs whose labels name an evener hub.
// `launchctl list` prints "PID Status Label" per line; a loaded-but-not-running
// job shows "-" in the PID column. A numeric PID is a RUNNING hub (live); "-" is
// a job that exists but is not serving (dormant). The two are kept apart because
// treating a dormant job as live would make restartHub start a second hub while
// an ad hoc hub kept serving the old version, whereas a dormant job with the hub
// port free is exactly the case where restarting the job is the correct repair.
// The doc's precedent is `launchctl list | grep evener-hub`.
func parseLaunchdHubs(out []byte) (live, dormant []string) {
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "PID" {
			continue
		}
		label := fields[len(fields)-1]
		if !isEvenerHubName(label) {
			continue
		}
		if fields[0] == "-" {
			dormant = append(dormant, label)
			continue
		}
		live = append(live, label)
	}
	return live, dormant
}

// parseSystemdHubs classifies evener hub units in `systemctl list-units` output.
// Each line starts with the unit name followed by LOAD, ACTIVE, and SUB, e.g.
// "evener-hub.service loaded active running". A leading status glyph (●) is
// stripped. `loaded active running` is the only live shape: `loaded active
// exited` is a unit that is loaded but has no process serving, and
// `loaded inactive dead` is a stopped one. Both are dormant — reloading them is
// the repair for a host left without a listener, but they must never be read as
// live (restarting one then would start a second hub while an ad hoc hub still
// holds hub.lock). A unit that is not `loaded` (not-found) is ignored entirely:
// starting it would fail.
func parseSystemdHubs(out []byte) (live, dormant []string) {
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "●"))
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		if !strings.HasSuffix(unit, ".service") {
			continue
		}
		if fields[1] != "loaded" || !isEvenerHubName(strings.TrimSuffix(unit, ".service")) {
			continue
		}
		if fields[2] == "active" && fields[3] == "running" {
			live = append(live, unit)
			continue
		}
		dormant = append(dormant, unit)
	}
	return live, dormant
}

// isEvenerHubName reports whether a label or unit base name names an evener hub.
func isEvenerHubName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "evener") && strings.Contains(lower, "hub")
}

// supervisorListingAbsent reports whether a failed supervisor listing means the
// listing tool is genuinely unavailable on the host — the executable is missing,
// or systemd is not the running init system — so the hub really is unmanaged and
// the bare-process path is the correct fallback. Every other failure (a
// permission refusal, a broken bus, an ssh transport drop) is surfaced instead
// of silently falling back, because falling back would nohup-replace a hub the
// service manager still owns.
func supervisorListingAbsent(out []byte) bool {
	text := string(out)
	for _, marker := range []string{
		"not found",
		"No such file",
		"not been booted with systemd",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// systemdUserBusAbsent reports whether a failed `systemctl --user list-units`
// proves there is no user manager to list, rather than a permission or transport
// failure. A headless host reached over non-interactive ssh exports no
// XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS, so the user listing exits nonzero
// with one of systemd's "Failed to connect to bus" messages; none of
// supervisorListingAbsent's markers match the newer
// "$DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined" wording. On a
// host whose system listing named no evener unit — a fresh install, or one
// stopped by an operator — that failure used to be fatal (round eleven), even
// though a host with no reachable user bus cannot be running a user unit, so the
// bare-process path is the only restart such a host can have.
//
// A bus that answers with a denial is NOT absent: permission and authentication
// failures keep the probe fatal, because a user unit may exist and be running
// where the ssh user simply cannot see it.
func systemdUserBusAbsent(out []byte) bool {
	text := string(out)
	if !strings.Contains(text, "Failed to connect to bus:") &&
		!strings.Contains(text, "$DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined") {
		return false
	}
	for _, denial := range []string{
		"Permission denied",
		"Access denied",
		"Operation not permitted",
		"Authentication required",
	} {
		if strings.Contains(text, denial) {
			return false
		}
	}
	return true
}

// restartHub restarts the host's hub: after a deploy, to replace a stale process,
// or to settle a restart or start an earlier attempt recorded. It resolves the
// owner of the host's selected endpoint and records the restart intent before
// any mutation. A supervised hub (launchd/systemd) is restarted by unit or
// label; a hub with no supervisor is killed and relaunched from the selected
// launch. A supervisor whose command cannot be built safely (no numeric uid, or
// a label outside the bare-safe set, which is never interpolated into the
// remote shell) is refused.
// replaced is the identity of the hub process this restart expects to replace, so
// a non-unique version cannot make the old process look like a successful
// replacement.
func (m *Manager) restartHub(ctx context.Context, host hostreg.Host, facts Preflight, expectVersion, expectPin string, replaced hubIdentity) error {
	started, err := m.restartHubWithStartProof(ctx, host, facts, expectVersion, expectPin, replaced, m.pendingRestart(host.Name).binding)
	if started && err == nil {
		m.clearPendingRestart(host.Name)
	}
	return err
}

// restartHubWithStartProof reports only the actual free-port startup branch.
// Unknown predecessor identity alone never authorizes startup verification.
// That branch retains its pending intent for the caller to verify and settle.
func (m *Manager) restartHubWithStartProof(ctx context.Context, host hostreg.Host, facts Preflight, expectVersion, expectPin string, replaced hubIdentity, binding *effectBinding) (bool, error) {
	pending := m.pendingRestart(host.Name)
	if pending.selected != nil {
		return pending.start, m.recoverSelectedRestart(ctx, host, facts, pending)
	}
	previous := pending
	if err := m.checkEffectBinding(binding); err != nil {
		return false, err
	}
	owner, err := m.resolveOwnedEndpoint(ctx, host, facts)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrRestart, err)
	}
	if replaced.startedAt.IsZero() {
		replaced, _ = identityFromReport(owner.report)
	}
	pending, err = m.prepareAndStoreRestart(ctx, host, facts, owner, expectVersion, expectPin, replaced, binding)
	if err != nil {
		return false, err
	}
	return pending.start, m.restartPreparedSelectedPending(ctx, host, facts, &pending, owner.pid != "0", previous)
}

// checkHubOwner proves the identified listener runs as the host's effective SSH
// user before it is signaled (spec 04, §"Stop/restart mechanics" check 3): a
// process owned by another user is not provably this host's hub, so a mismatch —
// or an owner or effective user the probe cannot read — refuses with ErrRestart,
// with no kill and no relaunch (§"Unverifiable restart target"). The effective
// user is the entry's User when set, else the user part of a user@host SSH
// destination, else the login user the host reports (`id -un`), exactly the chain
// the spec defines; the owner is read with `ps -o user= -p <pid>`. pid must
// already have been validated by numericPID.
func (m *Manager) checkHubOwner(ctx context.Context, host hostreg.Host, pid string) error {
	user, ok := effectiveUserName(host)
	if !ok {
		// The entry names no user — a host written as a bare hostname, or one
		// resolved through an ambient ssh_config — so ask the host which user the
		// non-interactive session runs as. Comparing the owner against the empty
		// string instead would refuse every legitimate hub on such a host and
		// block automated restarts.
		out, err := m.runRemote(ctx, host, "id -un")
		if err != nil {
			return fmt.Errorf("%w: host %q could not read the login user to check the restart target's owner: %w", ErrRestart, host.Name, err)
		}
		user = firstLine(string(out))
		if user == "" {
			return fmt.Errorf("%w: host %q reported no login user to check the restart target's owner against; refusing to restart it", ErrRestart, host.Name)
		}
	}
	out, err := m.runRemote(ctx, host, pidOwnerRemote(pid))
	if err != nil {
		return fmt.Errorf("%w: host %q could not read the owner of pid %s: %w", ErrRestart, host.Name, pid, err)
	}
	owner := firstLine(string(out))
	if owner == "" {
		return fmt.Errorf("%w: host %q pid %s reported no owning user; refusing to restart a process whose owner cannot be read", ErrRestart, host.Name, pid)
	}
	matches, err := m.sameUser(ctx, host, owner, user)
	if err != nil {
		return fmt.Errorf("%w: host %q could not resolve owner identity: %w", ErrRestart, host.Name, err)
	}
	if !matches {
		return fmt.Errorf("%w: host %q pid %s is owned by user %q, not the host's effective SSH user %q; refusing to restart it",
			ErrRestart, host.Name, pid, owner, user)
	}
	return nil
}

// effectiveUserName resolves the effective user of the host's SSH session from
// the registry entry alone: the user part of the destination ssh will dial,
// derived from composeDest so the destination's one definition also decides the
// user. ok is false when the destination names no user — a bare hostname, or a
// host resolved through an ambient ssh_config — and the caller must then probe
// the login user the host reports.
func effectiveUserName(host hostreg.Host) (string, bool) {
	if user, _, found := strings.Cut(composeDest(host), "@"); found && user != "" {
		return user, true
	}
	return "", false
}

// pidOwnerRemote builds the remote command that prints the user pid runs as.
// `ps -o user=` reports the process's effective user, the field the identity
// read compares against the host's effective SSH user; ps is the package's
// process-inspection tool for a host without a readable /proc. pid must already
// have been validated by numericPID.
func pidOwnerRemote(pid string) string {
	return "ps -o user= -p " + pid
}

// pidAliveMarker and pidGoneMarker are the process-exit probe's answers. Like
// the port probe's markers they make "the process is gone" (a zero exit carrying
// pidGoneMarker) distinguishable from a probe that could not run at all (a
// nonzero exit), which must never be read as an exited process.
const (
	pidAliveMarker = "__sshconn_pid_alive__"
	pidGoneMarker  = "__sshconn_pid_gone__"
)

// processAliveRemote builds the remote command that reports whether pid still
// exists. `kill -s 0` sends no signal: it only performs the existence and
// permission check. The `--` is the second, structural guard on the argument —
// pid must already be validated by numericPID, but the command makes it
// impossible for even a future caller's bad value to be read as an option.
func processAliveRemote(pid string) string {
	return "if kill -s 0 -- " + pid + " 2>/dev/null; then echo " + pidAliveMarker +
		"; else echo " + pidGoneMarker + "; fi"
}

// waitProcessExit blocks until the old hub process has actually exited, or the
// bound is exhausted. Waiting only for the hub port to clear is not enough: the
// hub closes its listener at the START of a graceful shutdown and holds hub.lock
// for the whole drain, so a replacement launched the moment the port frees can
// immediately fail to acquire the lock and exit, leaving the host with no hub at
// all. The kernel releases the lock when the process exits, so the process's
// exit — not the listener's disappearance — is the condition the relaunch can
// rely on.
//
// The probe's answer is read from the markers, not the exit status: a failed Run
// is a transport failure and is surfaced rather than mistaken for an exit.
func (m *Manager) waitProcessExit(ctx context.Context, host hostreg.Host, pid string) error {
	remote := processAliveRemote(pid)
	for range restartStopAttempts {
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
		if err != nil {
			return fmt.Errorf("%w: host %q could not check whether hub pid %s exited: %w: %s", ErrRestart, host.Name, pid, err, tail(out))
		}
		switch strings.TrimSpace(string(out)) {
		case pidAliveMarker:
			// Still draining; wait and re-check.
		case pidGoneMarker:
			return nil
		default:
			return fmt.Errorf("%w: host %q pid %s process probe answered %q, which is neither %q nor %q; refusing to read an unusable answer as an exited process",
				ErrRestart, host.Name, pid, tail(out), pidAliveMarker, pidGoneMarker)
		}
		if err := m.opts.waitSleep(ctx, restartStopInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: host %q hub pid %s is still running after kill", ErrRestart, host.Name, pid)
}

// probeRunningHub asks the host hub's /api/health once and returns the identity
// it reports: the version, plus the process start time when the body carries one.
// ok is false when nothing answered or the body was not a health response, which
// is "no running hub to judge" rather than an error: ensureOnce must not invent a
// restart from an unanswerable probe.
func (m *Manager) probeRunningHub(ctx context.Context, host hostreg.Host, facts Preflight) (hubIdentity, bool) {
	report, err := m.resolveHostEndpoint(ctx, host, facts)
	if err != nil {
		return hubIdentity{}, false
	}
	return identityFromReport(report)
}

func identityFromReport(report hubendpoint.Report) (hubIdentity, bool) {
	if report.Running == nil {
		return hubIdentity{}, false
	}
	return hubIdentity{version: report.Running.Version, gitSHA: report.Running.BackendGitSHA, startedAt: report.Running.StartedAt}, true
}

// snapshotPinGitSHA is the Git SHA a snapshot deploy's post-restart probe must
// see the host hub report, or "" when this controller carries no such pin. The
// build channel (buildinfo.BuildChannel()) is the source, not a second flag: the
// channel decides whether a version names the build. A snapshot version does not
// — two snapshot builds can share it — so only backend_git_sha, the running
// hub's own buildinfo.GitSHA, tells them apart. Release and dev versions are
// left to the version-equality rule the probe already applies.
func snapshotPinGitSHA() string {
	if buildinfo.BuildChannel() != "snapshot" {
		return ""
	}
	return buildinfo.GitSHA
}

// expectedServedBuild is the build a wait should expect the host to serve: the
// version to wait for, and the snapshot pin that goes with it.
//
// The controller's own build is the answer only when the host actually carries
// it. A configured deploy path converges the host onto this controller's build,
// and the result is judged before any wait runs, so by then the host either
// already carries the controller's build or nothing was ever going to install
// one. Waiting for the controller's version in that second case could never
// converge — a binary cannot report a version it does not carry — and refuses a
// working, protocol-compatible host at the last step, which is the refusal the
// attach path no longer makes (ensureOnce). The snapshot pin is dropped for the
// same reason: it names the controller's commit, which a host that kept its own
// build was never given.
//
// A host that keeps its own build only ever gets this far because it answered
// this controller's launch-check, so the protocol matches; the build label is not
// a compatibility signal.
func expectedServedBuild(facts Preflight, expected string) (version, gitSHA string) {
	if hostBuildDiffers(facts, expected) {
		return facts.Version, ""
	}
	return expected, snapshotPinGitSHA()
}

// hubBuildSHAMatches reports whether a health answer's backend_git_sha satisfies
// the expected build identity. With no snapshot pin (expected == "") there is
// nothing to match and every answer does, which is what keeps a non-snapshot
// deploy on the version-equality rule exactly as before. With a pin, an empty or
// different SHA is not yet healthy: the version cannot distinguish two snapshot
// builds, so a response that does not report the deployed commit is not proof
// the deploy took.
func hubBuildSHAMatches(got, expected string) bool {
	return expected == "" || got == expected
}

// recoverHubArgvRaw recovers and validates a plausible `evener hub` invocation
// without requiring its executable to be the manager's configured target. The
// deploy path uses it to learn where an existing hub is installed so it can
// install the controller's build over that same file.
func (m *Manager) recoverHubArgvRaw(ctx context.Context, host hostreg.Host, pid string) ([]string, error) {
	pid, err := numericPID(pid)
	if err != nil {
		return nil, err
	}
	if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, hubArgvRemote(pid)), nil); err == nil && len(out) > 0 {
		argv, ok := splitNullArgv(out)
		if !ok {
			return nil, fmt.Errorf("%w: host %q pid %s: /proc/%s/cmdline was readable but not a usable argv (an empty argument makes it ambiguous); refusing to recover it through ps",
				ErrRestart, host.Name, pid, pid)
		}
		if err := validateHubArgv(argv); err != nil {
			return nil, fmt.Errorf("%w: host %q pid %s: %w", ErrRestart, host.Name, pid, err)
		}
		return argv, nil
	}

	// `-o command=` (trailing `=`) suppresses the `COMMAND` header line, so the
	// recovered command line is the process's argv, not the header followed by
	// it. Real GNU/BSD ps emit the header without the trailing `=`, which is what
	// made the old invocation relaunch as `nohup COMMAND`.
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "ps -p "+pid+" -ww -o command="), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q ps pid %s: %w: %s", ErrRestart, host.Name, pid, err, tail(out))
	}
	cmdline := stripPSHeader(string(out))
	if cmdline == "" {
		return nil, fmt.Errorf("%w: host %q could not recover argv for pid %s", ErrRestart, host.Name, pid)
	}
	argv, err := hubArgvFromCommandLine(cmdline)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q pid %s: %w (command line %q)", ErrRestart, host.Name, pid, err, cmdline)
	}
	return argv, nil
}

// installableEvenerBasename reports whether a basename names the binary a host
// hub can be run as. Only `evener` does: `evener-dev` is the development/test
// tooling binary (cmd/evener-dev/bin) — no `hub` subcommand and no
// `launch-check` — so a run target naming it would be probed, relaunched, and
// attached as a hub that can never answer. A deploy cannot preserve a run
// target the host cannot serve.
func installableEvenerBasename(p string) bool {
	return path.Base(p) == "evener"
}

// hubExecutableMatches proves a recovered hub argv[0] is the executable the
// manager deploys and launches. It canonicalizes the recovered path on the host
// with the same portable POSIX-sh resolver deployTarget uses (symlinks resolved,
// plain readlink — no `readlink -f`, which BSD readlink rejects) and compares it
// to the canonical configured target: evener_path when set, else the canonical
// `command -v evener`. A basename is not sufficient on its own, since an
// unrelated binary can share one. A mismatch is ErrRestart with no kill.
//
// This does not judge the run target's basename, and it must not: a host already
// running an executable whose basename is not `evener` (say
// /opt/evener/current/evener-hub) can attach, and restarting that process is not
// where the basename is judged. checkRunTarget (deploy.go) is: it refuses every
// basename other than `evener` before the target is probed, pushed, or installed
// on the deploy path, because install.sh ships the runtime binary as `evener` and
// cannot produce a hub at any other name. So a non-`evener` basename means "cannot
// be deployed to", not "cannot be attached to or restarted".
func (m *Manager) hubExecutableMatches(ctx context.Context, host hostreg.Host, exe string) error {
	resolved, err := m.resolveExecutableName(ctx, host, exe)
	if err != nil {
		return err
	}
	actual, err := m.resolveRemotePath(ctx, host, resolved)
	if err != nil {
		return err
	}
	expected, err := m.expectedHubExecutable(ctx, host)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: %w: host %q hub executable %q (resolves to %q) is not the configured target %q; refusing to restart it",
			ErrRestart, errOwnerSelectionMismatch, host.Name, exe, actual, expected)
	}
	return nil
}

// resolveExecutableName turns a recovered hub argv[0] into a path the on-host
// resolver can canonicalize. A bare name is a PATH lookup — the ad hoc hub is
// commonly launched as `evener hub` — and the resolver's `test -f` cannot perform
// one: it checks the SSH login cwd, so a bare name never resolves and a valid hub
// would be refused. Matching expectedHubExecutable and deployTarget, a name with
// no slash is resolved with `command -v` first; anything containing a slash is
// already a path and is passed through.
func (m *Manager) resolveExecutableName(ctx context.Context, host hostreg.Host, exe string) (string, error) {
	if strings.Contains(exe, "/") {
		return exe, nil
	}
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "command -v "+shellquote.RemoteWord(exe)), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve hub executable %q on PATH: %w: %s", ErrRestart, host.Name, exe, err, tail(out))
	}
	p, err := commandPathOutput(out)
	if err != nil {
		return "", fmt.Errorf("%w: host %q hub executable %q is not on PATH and is not a path; refusing to restart it", ErrRestart, host.Name, exe)
	}
	return p, nil
}

// expectedHubExecutable resolves the canonical path of the executable the host
// hub is expected to run: the configured evener_path when set, else a target
// this Manager already resolved for the host, else whatever `command -v evener`
// resolves to. Every path is canonicalized on the host, so a symlinked install
// on either side compares equal. A hub installed at the installer default
// ~/.local/bin/evener but absent from the non-interactive PATH is identified
// through the resolved-target branch: preflight probes that location and
// records it as the host's target (probeInstallerDefaultExecutable →
// setResolvedTarget), so the host is read as running the controller's build
// rather than re-deployed to on every reconnect.
func (m *Manager) expectedHubExecutable(ctx context.Context, host hostreg.Host) (string, error) {
	if p := host.EvenerPath; p != "" {
		return m.resolveRemotePath(ctx, host, p)
	}
	if p := m.resolvedTarget(host.Name); p != "" {
		return m.resolveRemotePath(ctx, host, p)
	}
	// There is no installer-default fallback here: `command -v evener` exits
	// non-zero when the name is missing, which the runner reports as an error,
	// so an absent evener takes the error branch below and never returns an
	// empty answer for a fallback to fill in. The installer default is
	// discovered by preflight instead and arrives through the resolved-target
	// branch above.
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "command -v evener"), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve evener on PATH: %w: %s", ErrRestart, host.Name, err, tail(out))
	}
	p, err := commandPathOutput(out)
	if err != nil {
		return "", fmt.Errorf("%w: host %q has no evener on PATH and no configured evener_path to identify the hub by; set evener_path", ErrRestart, host.Name)
	}
	return m.resolveRemotePath(ctx, host, p)
}

// resolveRemotePath canonicalizes a remote path, following symlinks, with the
// resolver deployTarget uses.
func (m *Manager) resolveRemotePath(ctx context.Context, host hostreg.Host, p string) (string, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, resolveDeployCommand(p)), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q resolve %q: %w: %s", ErrRestart, host.Name, p, err, tail(out))
	}
	resolved, err := ownerPathOutput(out)
	if err != nil {
		return "", fmt.Errorf("%w: host %q path %q does not resolve to a real file", ErrRestart, host.Name, p)
	}
	return resolved, nil
}

// hubArgvRemote builds the remote command that prints pid's argv NUL-separated.
// It prints nothing and exits 0 when the host has no readable
// /proc/<pid>/cmdline, so the caller falls back to ps instead of reading an
// error as an empty argv. pid is validated by the caller (numericPID), which is
// what makes `/proc/<pid>/cmdline` a path to a process rather than a
// host-chosen path to cat.
func hubArgvRemote(pid string) string {
	p := "/proc/" + pid + "/cmdline"
	return "if [ -r " + p + " ]; then cat " + p + "; fi"
}

// splitNullArgv parses a NUL-delimited argv. It reports false for anything but a
// non-empty argv with no empty words: an empty word makes the reconstructed
// command line ambiguous, so the caller falls back rather than guessing. Exactly
// one trailing NUL is stripped (the terminator of the last element): trimming
// every trailing NUL would fold an empty final argument into its predecessor,
// turning `evener hub -config ""` into the valid-looking `evener hub -config`
// and relaunching with the value missing.
func splitNullArgv(out []byte) ([]string, bool) {
	out = bytes.TrimSuffix(out, []byte{0})
	if len(out) == 0 {
		return nil, false
	}
	parts := bytes.Split(out, []byte{0})
	argv := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			return nil, false
		}
		argv = append(argv, string(p))
	}
	return argv, true
}

// hubArgvFromCommandLine recovers the argv of the process whose command line ps
// reported, refusing anything that is not a plain `evener hub` invocation. It is
// what keeps a relaunch from killing and restarting a process that is not a hub.
func hubArgvFromCommandLine(line string) ([]string, error) {
	argv, err := tokenizeCommandLine(line)
	if err != nil {
		return nil, err
	}
	if err := validateHubArgv(argv); err != nil {
		return nil, err
	}
	return argv, nil
}

// validateHubArgv checks that argv is a plausible `evener hub` daemon
// invocation. It is shared by the exact (null-delimited) and ps paths.
func validateHubArgv(argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command line")
	}
	// The executable's identity is checked separately, by canonicalizing the
	// recovered argv[0] on the host and comparing it to the configured target
	// (hubExecutableMatches, below). A hardcoded `path.Base(argv[0]) == "evener"`
	// is neither necessary nor sufficient: evener_path is an arbitrary executable
	// path, so a valid custom target could never pass it. What this function
	// decides is shape only.
	if strings.TrimSpace(argv[0]) == "" {
		return errors.New("empty executable in command line")
	}
	// The hub subcommand must sit at the actual subcommand position. Matching
	// "hub" anywhere in argv accepts an unrelated command that merely names it
	// (e.g. `evener serve --model hub`) and would kill and relaunch that
	// process. The daemon is `evener hub` and takes only flags after the
	// subcommand, so a later positional (`evener hub attach`, the client) is
	// not the daemon either.
	if len(argv) < 2 || argv[1] != "hub" {
		return fmt.Errorf("argv does not run the hub subcommand at position 1: %q", argv)
	}
	if len(argv) > 2 && !strings.HasPrefix(argv[2], "-") {
		return fmt.Errorf("unexpected positional %q after the hub subcommand", argv[2])
	}
	// A value's boundary cannot be recovered from a space-joined `ps` line: two
	// consecutive non-flag words are the signature of a split value (a path with
	// a space), so refuse rather than relaunch a wrong argv. An exact
	// (null-delimited) argv never has this shape for a valid invocation.
	for i := 3; i < len(argv); i++ {
		if !strings.HasPrefix(argv[i], "-") && !strings.HasPrefix(argv[i-1], "-") {
			return fmt.Errorf("ambiguous word boundary between %q and %q: a value may contain a space", argv[i-1], argv[i])
		}
	}
	return nil
}

// tokenizeCommandLine splits a command line recovered from `ps -o command=` into
// the argv words a shell would have produced, honoring single quotes, double
// quotes, and backslash escapes. It refuses any line carrying an unquoted shell
// metacharacter: such a line is a compound command or a redirection rather than
// the simple exec a hub is, so it cannot be tokenized unambiguously and must not
// be re-run.
func tokenizeCommandLine(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	haveWord := false
	flush := func() {
		if haveWord {
			words = append(words, cur.String())
			cur.Reset()
			haveWord = false
		}
	}
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\r':
			flush()
			i++
		case '\'':
			haveWord = true
			i++
			end := strings.IndexByte(s[i:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i : i+end])
			i += end + 1
		case '"':
			haveWord = true
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					if n := s[i+1]; n == '"' || n == '\\' || n == '$' || n == '`' {
						cur.WriteByte(n)
						i += 2
						continue
					}
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, errors.New("unterminated double quote")
			}
			i++
		case '\\':
			haveWord = true
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			cur.WriteByte(s[i+1])
			i += 2
		case ';', '|', '&', '<', '>', '(', ')', '`', '$':
			return nil, fmt.Errorf("unquoted shell metacharacter %q", string(c))
		default:
			haveWord = true
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return words, nil
}

// hubAddrFlag returns the value of the hub's --addr/-addr argument in argv,
// accepting both the `-addr value` and `-addr=value` spellings. A hub launched
// without the flag used the default address, reported as absent.
func hubAddrFlag(argv []string) (string, bool) {
	for i, a := range argv {
		for _, name := range []string{"-addr", "--addr"} {
			if a == name && i+1 < len(argv) {
				return argv[i+1], true
			}
			if v, ok := strings.CutPrefix(a, name+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

// relaunchCommand builds the detached remote relaunch from the recovered argv.
// Each word is quoted individually and the recovered line is never handed to
// `sh -c`: the line is host-derived, so re-parsing it as a command would execute
// a metacharacter inside a literal argument (a `;` in a path, say). Redirection
// and backgrounding are ours, not the host's. It appends to the recovered log so
// the hub's history is preserved (docs/evener-hub-remote-operations.md:388-394);
// with no recovered log the output is discarded.
func relaunchCommand(argv []string, logPath string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = shellquote.RemoteWord(a)
	}
	cmd := strings.Join(words, " ")
	if logPath != "" {
		return "nohup " + cmd + " >>" + shellquote.RemoteWord(logPath) + " 2>&1 </dev/null &"
	}
	return "nohup " + cmd + " </dev/null >/dev/null 2>&1 &"
}

// stripPSHeader drops a leading `ps` header line so header-bearing output (a ps
// that ignores `-o command=`, or a BSD variant spelling it differently) never
// becomes part of the relaunched argv. The invocation already suppresses the
// header; this is the defensive half of the fix.
func stripPSHeader(out string) string {
	trimmed := strings.TrimSpace(out)
	first, rest, ok := strings.Cut(trimmed, "\n")
	if !ok {
		if trimmed == "COMMAND" {
			return ""
		}
		return trimmed
	}
	if strings.TrimSpace(first) == "COMMAND" {
		return strings.TrimSpace(rest)
	}
	return trimmed
}

// parseLogPath extracts the regular-file destination shared by fd 1 and fd 2
// from `lsof -p <pid> -a -d 1,2` output. Both descriptors must point at the same
// regular file: relaunching with only one of two different destinations would
// preserve stdout and send stderr (or vice versa) somewhere else. A tty
// destination (nothing redirected at launch) yields no path.
func parseLogPath(out []byte) (string, bool) {
	var stdout, stderr string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] == "COMMAND" {
			continue
		}
		fd := strings.TrimRight(fields[3], "wru")
		if fd != "1" && fd != "2" {
			continue
		}
		if fields[4] != "REG" {
			continue
		}
		// Only the first eight columns are whitespace-delimited metadata. Keep
		// every byte of the absolute NAME, including repeated/trailing spaces.
		name := line
		for range 8 {
			name = strings.TrimLeft(name, " \t")
			end := strings.IndexAny(name, " \t")
			if end < 0 {
				name = ""
				break
			}
			name = name[end:]
		}
		name = strings.TrimLeft(name, " \t")
		if !strings.HasPrefix(name, "/") {
			continue
		}
		switch fd {
		case "1":
			stdout = name
		case "2":
			stderr = name
		}
	}
	if stdout == "" || stdout != stderr {
		return "", false
	}
	return stdout, true
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// prepareAndStoreRestart prepares the selected owner's restart and records it as
// the host's pending intent, naming the predecessor it must replace and the entry
// binding that authorizes it.
func (m *Manager) prepareAndStoreRestart(ctx context.Context, host hostreg.Host, facts Preflight, owner selectedOwner, version, pin string, replaced hubIdentity, binding *effectBinding) (pendingRestartState, error) {
	pending, err := m.prepareSelectedRestart(ctx, host, facts, owner, version, pin)
	if err != nil {
		return pendingRestartState{}, err
	}
	pending.replaced, pending.binding = replaced, binding
	pending.start = owner.report.State == "stopped"
	m.storePendingRestart(host.Name, pending)
	return pending, nil
}

func (m *Manager) restartSelectedPending(ctx context.Context, host hostreg.Host, facts Preflight, pending *pendingRestartState, predecessorLive bool) error {
	return m.dispatchSelectedRestart(ctx, host, facts, pending, predecessorLive, nil)
}

// Only a newly prepared restart may roll back a read-only refusal. Once a
// mutation is dispatched, even a failed SSH reply must retain its intent.
func (m *Manager) restartPreparedSelectedPending(ctx context.Context, host hostreg.Host, facts Preflight, pending *pendingRestartState, predecessorLive bool, previous pendingRestartState) error {
	dispatched := false
	err := m.dispatchSelectedRestart(ctx, host, facts, pending, predecessorLive, func() { dispatched = true })
	if err != nil && !dispatched {
		m.storePendingRestart(host.Name, previous)
	}
	return err
}

func (m *Manager) dispatchSelectedRestart(ctx context.Context, host hostreg.Host, facts Preflight, pending *pendingRestartState, predecessorLive bool, onDispatch func()) error {
	if err := m.checkEffectBinding(pending.binding); err != nil {
		return err
	}
	if pending.selected.supervisor.kind == supervisorNone {
		target, err := m.selectedRelaunchTarget(ctx, host, *pending.selected)
		if err != nil {
			return err
		}
		if pending.relaunchTarget != "" && target != pending.relaunchTarget {
			return fmt.Errorf("%w: retained relaunch executable changed", errOwnerEvidence)
		}
		pending.relaunchTarget = target
	}
	nextEnv, err := m.checkSelectedReplacement(ctx, host, facts, *pending.selected, predecessorLive, pending.nextEnv)
	if err != nil {
		return err
	}
	// The gate performs remote reads. Keep entry authorization adjacent to the
	// ad-hoc signal, as it already is for the supervisor command below.
	if pending.selected.supervisor.kind == supervisorNone && predecessorLive {
		if err := m.checkEffectBinding(pending.binding); err != nil {
			return err
		}
	}
	pending.nextEnv = slices.Clone(nextEnv)
	if pending.nextEnv == nil {
		// An explicitly empty validated environment is not an unrun gate.
		pending.nextEnv = []string{}
	}
	// The installation has been validated, and this command now owns only
	// replacement of its selected predecessor, not future deployment repair.
	pending.deployment = false
	if pending.command == "" && pending.selected.supervisor.kind != supervisorNone {
		pending.command, _ = pending.selected.supervisor.restartRemote(facts.UID)
	}
	if pending.command == "" {
		pending.command = selectedRelaunch(*pending.selected)
	}
	m.storePendingRestart(host.Name, *pending)
	owner := *pending.selected
	if owner.supervisor.kind == supervisorLaunchd && facts.UID != owner.domainUID {
		return fmt.Errorf("%w: selected launchd domain changed", errOwnerEvidence)
	}
	if owner.supervisor.kind == supervisorNone && predecessorLive {
		if onDispatch != nil {
			onDispatch()
		}
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "kill -- "+owner.pid), nil)
		if err != nil {
			return fmt.Errorf("%w: selected stop: %w: %s", ErrRestart, err, tail(out))
		}
		// Unlike the old port-based path, an unrelated listener does not delay a
		// selected replacement. Process exit releases its lock after graceful drain.
		if err := m.waitProcessExit(ctx, host, owner.pid); err != nil {
			return err
		}
	}
	var command string
	// Reconstruct from revalidated intent on every replay, never trust stored text.
	if owner.supervisor.kind == supervisorNone {
		// Graceful drain can outlast the live gate. Freeze the executable above
		// and recheck all stopped launch inputs before starting another process.
		target, err := m.selectedRelaunchTarget(ctx, host, owner)
		if err != nil {
			return err
		}
		if target != pending.relaunchTarget {
			return fmt.Errorf("%w: retained relaunch executable changed", errOwnerEvidence)
		}
		if predecessorLive {
			if _, err := m.checkSelectedReplacement(ctx, host, facts, owner, false, pending.nextEnv); err != nil {
				return err
			}
		}
		command = selectedRelaunch(owner)
	} else {
		var ok bool
		if !predecessorLive {
			command, ok = owner.supervisor.startRemote(facts.UID)
		} else {
			command, ok = owner.supervisor.restartRemote(facts.UID)
		}
		if !ok {
			return fmt.Errorf("%w: selected supervisor domain unavailable", errOwnerEvidence)
		}
	}
	if err := m.checkEffectBinding(pending.binding); err != nil {
		return err
	}
	if onDispatch != nil {
		onDispatch()
	}
	out, runErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, command), nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil && !owner.supervisor.restartStatusIsAdvisory() {
		return fmt.Errorf("%w: selected replacement %s: %w: %s", ErrRestart, command, runErr, tail(out))
	}
	if err := m.waitSelectedSuccessor(ctx, host, facts, *pending); err != nil {
		if runErr != nil {
			return fmt.Errorf("%w: advisory restart status %s: %w", err, command, runErr)
		}
		return err
	}
	m.clearPendingRestart(host.Name)
	return nil
}
