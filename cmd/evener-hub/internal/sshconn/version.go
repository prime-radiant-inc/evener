package sshconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/shellquote"
)

// defaultHubAddr is the host hub's default loopback listen address
// (docs/evener-hub.md:96-104); the controller may override via Options.HubAddr.
const defaultHubAddr = "127.0.0.1:9180"

// Bounds for the restart stop/health waits. The stop wait is short: with
// nothing actively streaming the lock frees within milliseconds, and SIGTERM's
// graceful drain is capped at ~5s.
const (
	restartStopAttempts   = 50
	restartStopInterval   = 100 * time.Millisecond
	restartHealthAttempts = 30
	restartHealthInterval = 200 * time.Millisecond
)

// Bounds and hardening for the single-request health probe. --connect-timeout
// and --max-time bound it: without them a listener that accepts the connection
// but never answers would hold the whole health loop until the much longer
// deploy budget expires. -q and --noproxy '*' stop a host-side .curlrc or proxy
// environment from answering in place of the loopback hub, where a forged body
// could report the expected version and pass restart verification.
const (
	healthCurlConnectTimeout = "5"
	healthCurlMaxTime        = "10"
)

const (
	systemctlListUnits     = "systemctl list-units --type=service --all --no-legend --plain"
	systemctlListUnitsUser = "systemctl --user list-units --type=service --all --no-legend --plain"
)

// explicitHostAddr is the address the operator configured for host: the per-host
// registry Addr when set, else the manager-wide Options.HubAddr. Empty means the
// controller was told nothing, and the host must resolve its own address (from
// hub.toml, else the hub default), so the bridge is left to do exactly that
// rather than being handed a default that would override the config.
func explicitHostAddr(o Options, host hostreg.Host) string {
	if a := strings.TrimSpace(host.Addr); a != "" {
		return a
	}
	return strings.TrimSpace(o.HubAddr)
}

// hostAddrFor resolves the listen address of host's hub for the restart and
// health probes: the configured address when there is one, else the hub default.
// channelArgv passes the same configured address as the bridge's --addr, so the
// bridge and the probes address the same port whenever one is configured. A host
// that points at its own hub.toml (ConfigPath) but gives no address is refused by
// checkHostAddr rather than defaulted here: the bridge would resolve the file's
// address on the host, and the controller cannot read that file, so probing the
// default could address a different hub than the bridge attaches to.
func hostAddrFor(o Options, host hostreg.Host) string {
	if a := explicitHostAddr(o, host); a != "" {
		return a
	}
	return defaultHubAddr
}

func (m *Manager) hostAddr(host hostreg.Host) string {
	return hostAddrFor(m.opts, host)
}

// checkHostAddr validates the address the restart and health probes will use. A
// non-loopback or malformed address is refused before any ssh command runs:
// hubPort's parse-failure default would otherwise let a malformed address make
// the bare-process path stop whatever happens to listen on :9180, and the health
// probe would curl an arbitrary endpoint. A host that names its own hub.toml
// (ConfigPath) without an address is also refused: the file may select any port,
// the bridge resolves it on the host, and the controller has no way to read it,
// so silently probing the default would diverge from the bridge.
func (m *Manager) checkHostAddr(host hostreg.Host) error {
	if a := explicitHostAddr(m.opts, host); a != "" {
		return validateHubAddr(host.Name, a)
	}
	if p := strings.TrimSpace(host.ConfigPath); p != "" {
		return fmt.Errorf("%w: host %q sets config_path %q but no address; set Addr (or Options.HubAddr) to the hub's listen address so the restart and health probes address the same hub the bridge attaches to",
			ErrHostAddr, host.Name, p)
	}
	// Nothing configured: the host resolves the hub's documented default, which
	// is what hostAddrFor returns and what the bridge (with no --addr) dials.
	return nil
}

// validateHubAddr accepts the addresses a hub can actually listen on: a
// loopback host, localhost, or a wildcard bind (which loopbackAddr normalizes to
// loopback for probing). Anything else — including a missing port — is refused
// rather than probed.
func validateHubAddr(name, addr string) error {
	hostPart, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: host %q address %q is not host:port: %w", ErrHostAddr, name, addr, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%w: host %q address %q has no usable port", ErrHostAddr, name, addr)
	}
	switch hostPart {
	case "", "localhost", "0.0.0.0", "::":
		return nil
	}
	ip := net.ParseIP(hostPart)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%w: host %q address %q is not a loopback or wildcard bind", ErrHostAddr, name, addr)
	}
	return nil
}

// hubPort extracts the TCP port from a configured host:port hub address,
// defaulting to the known hub port when addr is unparsable. The default is safe
// only because every configured address is validated by checkHostAddr before it
// reaches here; a recovered argv's --addr is not validated that way and must go
// through argvHubPort, which refuses an unparsable address instead of defaulting
// it onto the port a default-port host listens on.
func hubPort(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p
	}
	return "9180"
}

// argvHubPort extracts the port a hub's recovered --addr names, reporting false
// when the address cannot be parsed. Unlike hubPort it has no default: mapping a
// recovered "garbage" onto :9180 would agree with a default-port host and let
// restartBare kill a listener the old hub was never launched with.
func argvHubPort(addr string) (string, bool) {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p, true
	}
	return "", false
}

// loopbackAddr rewrites a wildcard hub bind address to loopback, mirroring the
// attach path's normalization (cmd/evener-hub/attach.go loopbackAddr) so the
// restart and health probes address the host's hub exactly as the bridge dials
// it. The IPv6 wildcard maps to ::1 rather than 127.0.0.1: a hub bound
// IPv6-only is not listening on IPv4. Non-wildcard addresses pass through
// unchanged, so a valid loopback such as 127.0.0.2 or [::1], or a non-default
// port, is probed as configured instead of being flattened to "localhost".
// "localhost" names are rewritten to the literal 127.0.0.1 for the same reason
// the attach path does: the probe must not depend on the host's resolver.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "localhost":
		return net.JoinHostPort("127.0.0.1", port)
	case "::":
		return net.JoinHostPort("::1", port)
	}
	return addr
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

// supervisorSet is everything one host's supervisor listings yield: at most one
// live supervisor (a RUNNING hub unit) and at most one dormant supervisor (a
// loaded but inactive hub unit). Both are kept because an inactive unit is still
// the right way to bring the hub back on a host that is not currently serving —
// but only while nothing else holds the hub port, since starting a second hub
// races hub.lock.
type supervisorSet struct {
	live    supervisor
	dormant supervisor
}

// restartRemote is the remote command that restarts (or starts) the supervised
// hub. ok is false when no safe supervised command can be built, in which case
// the caller falls through to the ad hoc path rather than passing host-derived
// data into the remote shell.
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
// launchd `kickstart -k`). ok is false under the same conditions as
// restartRemote, so an unbuildable command falls through to the ad hoc path.
func (s supervisor) startRemote(uid string) (string, bool) {
	switch s.kind {
	case supervisorLaunchd:
		if !isNumericUID(uid) || !isBareSafeLabel(s.label) {
			return "", false
		}
		return "launchctl kickstart -k gui/" + uid + "/" + s.label, true
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
// probeListeners refuses a non-numeric line at the parse boundary; this is the
// structural guard at each seam that renders one, so a future caller cannot
// reintroduce the hole by bypassing the probe.
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

// detectSupervisorsFrom classifies a host from raw supervisor listings. It is
// pure so the table tests need no ssh: launchd for darwin, systemd (system,
// then --user) for linux, otherwise a bare process. A running unit anywhere wins
// over an inactive one; among inactive units a system unit wins over a user one,
// because a loaded system evener-hub unit is the documented deployment for this
// component and is authoritative over a user-scoped one.
//
// More than one matching label or unit in the same listing is an error rather
// than a silent pick of the first: restarting the wrong evener service would
// leave the hub the controller meant to replace untouched while reporting
// success.
func detectSupervisorsFrom(goos string, launchctlOut, systemdOut, systemdUserOut []byte) (supervisorSet, error) {
	switch goos {
	case "darwin":
		live, dormant := parseLaunchdHubs(launchctlOut)
		sup, err := pickSupervisor(supervisorLaunchd, "launchd", live)
		if err != nil || sup.kind != supervisorNone {
			return supervisorSet{live: sup}, err
		}
		dorm, err := pickSupervisor(supervisorLaunchd, "launchd", dormant)
		return supervisorSet{dormant: dorm}, err
	case "linux":
		sysLive, sysDorm := parseSystemdHubs(systemdOut)
		if sup, err := pickSupervisor(supervisorSystemd, "systemd", sysLive); err != nil || sup.kind != supervisorNone {
			return supervisorSet{live: sup}, err
		}
		userLive, userDorm := parseSystemdHubs(systemdUserOut)
		if sup, err := pickSupervisor(supervisorSystemdUser, "systemd --user", userLive); err != nil || sup.kind != supervisorNone {
			return supervisorSet{live: sup}, err
		}
		if dorm, err := pickSupervisor(supervisorSystemd, "systemd", sysDorm); err != nil || dorm.kind != supervisorNone {
			return supervisorSet{dormant: dorm}, err
		}
		dorm, err := pickSupervisor(supervisorSystemdUser, "systemd --user", userDorm)
		return supervisorSet{dormant: dorm}, err
	default:
		return supervisorSet{}, nil
	}
}

// detectSupervisorFrom is the live-only view of detectSupervisorsFrom, for the
// callers and tests that judge only whether a RUNNING supervised hub exists.
func detectSupervisorFrom(goos string, launchctlOut, systemdOut, systemdUserOut []byte) (supervisor, error) {
	set, err := detectSupervisorsFrom(goos, launchctlOut, systemdOut, systemdUserOut)
	return set.live, err
}

// pickSupervisor turns the matches from one listing into a supervisor: none is
// the bare-process path, exactly one is the target, and several are an error.
func pickSupervisor(kind supervisorKind, what string, matches []string) (supervisor, error) {
	switch len(matches) {
	case 0:
		return supervisor{}, nil
	case 1:
		return supervisor{kind: kind, label: matches[0]}, nil
	default:
		return supervisor{}, fmt.Errorf("%w: %d %s evener hub units match (%s); configure the intended one instead of relying on an ambiguous match",
			ErrRestart, len(matches), what, strings.Join(matches, ", "))
	}
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

// detectSupervisor runs the host's supervisor listings. Only a listing that
// proves the supervisor is absent falls through to the bare-process path; any
// other failure is surfaced — except an unreachable systemd user bus, which
// proves no user unit can be running and falls through as well
// (systemdUserBusAbsent). An ambiguous listing is fatal, not a fallback.
//
// The user listing is consulted whenever the system listing named no LIVE evener
// hub unit — no unit at all, or only a loaded-but-inactive one. A headless host
// reached over non-interactive ssh exports no
// XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS, so `systemctl --user list-units`
// exits nonzero with "Failed to connect to bus: ...", which matches none of
// supervisorListingAbsent's markers; running it unconditionally aborted the
// restart with ErrRestart even though the system listing had already found the
// hub's unit, so version auto-match could never repair such a host. A LIVE
// system unit therefore settles the host and the query is skipped for it. An
// inactive system unit does not: a RUNNING user unit is the hub serving the
// host, and skipping the query whenever any system unit existed misidentified
// it as the inactive system unit and restarted the wrong one.
// detectSupervisorsFrom gives the live user unit precedence over the dormant
// system one, so precedence stays live-before-dormant either way. An absent user
// bus proves no user unit can be running and falls through to the system answer
// (systemdUserBusAbsent), which is what keeps a headless host with no live
// system unit repairable instead of refusing it forever.
func (m *Manager) detectSupervisor(ctx context.Context, host hostreg.Host, facts Preflight) (supervisorSet, error) {
	switch facts.OS {
	case "darwin":
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "launchctl list"), nil)
		if err != nil && !supervisorListingAbsent(out) {
			return supervisorSet{}, fmt.Errorf("%w: host %q launchctl list: %w: %s", ErrRestart, host.Name, err, tail(out))
		}
		return detectSupervisorsFrom("darwin", out, nil, nil)
	case "linux":
		sysOut, sysErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, systemctlListUnits), nil)
		if sysErr != nil {
			if !supervisorListingAbsent(sysOut) {
				return supervisorSet{}, fmt.Errorf("%w: host %q systemctl list-units: %w: %s", ErrRestart, host.Name, sysErr, tail(sysOut))
			}
			sysOut = nil
		}
		var userOut []byte
		if live, _ := parseSystemdHubs(sysOut); len(live) == 0 {
			out, userErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, systemctlListUnitsUser), nil)
			if userErr != nil {
				if !supervisorListingAbsent(out) && !systemdUserBusAbsent(out) {
					return supervisorSet{}, fmt.Errorf("%w: host %q systemctl --user list-units: %w: %s", ErrRestart, host.Name, userErr, tail(out))
				}
			} else {
				userOut = out
			}
		}
		return detectSupervisorsFrom("linux", nil, sysOut, userOut)
	default:
		return supervisorSet{}, nil
	}
}

// restartHub restarts the host's hub: after a deploy, to replace a stale process,
// or to settle a restart or start an earlier attempt recorded. It prefers a
// detected supervisor (launchd/systemd) and otherwise restarts the bare process
// by recovering its pid, argv, and log. It never starts a second hub while the
// old one holds hub.lock: the bare path waits for the port to clear before
// relaunching. replaced is the identity of the hub process this restart expects
// to replace, so a non-unique version cannot make the old process look like a
// successful replacement.
func (m *Manager) restartHub(ctx context.Context, host hostreg.Host, facts Preflight, replaced hubIdentity) error {
	// Judge the restart against the build the host will actually serve, which is
	// this controller's own only when a deploy converged the host on it
	// (expectedServedBuild). This is the same rule recoverRestart applies to a
	// recorded start, and it has to be: the two branches differ only in whether a
	// hub happens to answer /api/health, so deriving the expectation per branch made
	// one recorded start succeed or fail on that accident.
	expectVersion, expectPin := expectedServedBuild(facts, m.opts.controllerVersion())
	set, err := m.detectSupervisor(ctx, host, facts)
	if err != nil {
		return err
	}
	sup := set.live
	if sup.kind == supervisorNone && set.dormant.kind != supervisorNone {
		// A loaded-but-inactive evener hub unit owns this host's hub: restarting
		// it starts it, which is the repair for a host left with no listener after
		// a deploy, a crash, or a failed restart. Only do so while the hub port is
		// free — with an ad hoc hub holding it, starting the unit would race
		// hub.lock and leave the ad hoc hub (on the old build) still serving.
		cleared, err := m.portCleared(ctx, host, hubPort(m.hostAddr(host)))
		if err != nil {
			return err
		}
		if cleared {
			sup = set.dormant
		}
	}
	if sup.kind != supervisorNone {
		if remote, ok := sup.restartRemote(facts.UID); ok {
			return m.restartSupervised(ctx, host, sup, remote, expectVersion, expectPin, replaced)
		}
		// A supervisor whose command cannot be built safely — launchd with no
		// numeric uid from preflight, or a label outside the bare-safe set — falls
		// through to the ad hoc path rather than interpolating host-derived data
		// into the remote shell.
	}

	if err := m.restartBare(ctx, host, replaced); err != nil {
		return err
	}
	if err := m.waitHealthy(ctx, host, expectVersion, expectPin, replaced); err != nil {
		return err
	}
	// A healthy replacement is serving, so any relaunch this Manager recorded for
	// this host is settled.
	m.clearPendingRestart(host.Name)
	return nil
}

// restartSupervised runs a detected supervisor's restart command and verifies
// the host hub afterwards. The restart command is recorded before it runs, so a
// restart that leaves no listener is completed by the next Ensure.
func (m *Manager) restartSupervised(ctx context.Context, host hostreg.Host, sup supervisor, remote, expectedVersion, expectedGitSHA string, replaced hubIdentity) error {
	// Record the restart before running it, exactly as restartBare does: a
	// restart that leaves no listener must be completed by the next Ensure.
	// Without this a supervisor restart that failed with the on-disk version
	// already matching left nothing for the decision ladder to retry, so the
	// next Ensure attached (or failed to attach) against a host with no hub.
	m.setPendingRestart(host.Name, remote, replaced)
	out, runErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
	if runErr != nil && !sup.restartStatusIsAdvisory() {
		// Surface the restart command's own failure even when the hub would
		// answer a health probe: a healthy response alone cannot prove the
		// restart took (the old process stays healthy through a failed
		// `systemctl restart` when the user lacks sudo/polkit), so swallowing
		// this cause is what let a failed restart look like success.
		return fmt.Errorf("%w: host %q %s: %w: %s", ErrRestart, host.Name, remote, runErr, tail(out))
	}
	if err := m.waitHealthy(ctx, host, expectedVersion, expectedGitSHA, replaced); err != nil {
		if runErr != nil {
			// launchd: docs note an interrupted `kickstart` can report failure
			// even when the restart succeeded
			// (docs/evener-hub-remote-operations.md:290-293), so its status is
			// diagnostic, not authoritative. The health failure is the real
			// cause; name the command failure alongside it.
			return fmt.Errorf("%w: host %q %s: %w: %s: %w", ErrRestart, host.Name, remote, runErr, tail(out), err)
		}
		return err
	}
	m.clearPendingRestart(host.Name)
	return nil
}

// recoverRestart retries the restart command a previous attempt recorded before
// it ran: a bare relaunch or a supervisor restart. It is the recovery half of the
// ladder: a restart that killed the old hub but left no listener must be
// completed by the next Ensure, which is what ErrRestart promises. There is no
// hub to kill here, so it runs the recorded command directly and waits for the
// expected build to answer.
func (m *Manager) recoverRestart(ctx context.Context, host hostreg.Host, facts Preflight, expected string, pending pendingRestartState) error {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, pending.command), nil)
	if err != nil {
		return fmt.Errorf("%w: host %q restart recovery: %w: %s", ErrRestart, host.Name, err, tail(out))
	}
	// Both branches wait for the build the host will actually serve
	// (expectedServedBuild), so a recorded start is settled by one rule whether or
	// not a hub answers /api/health. A recorded START has no predecessor to exclude
	// (bootstrapHub only starts a hub where nothing was serving), so that build
	// alone decides; a recorded REPLACEMENT must additionally prove the answering
	// process is not the one it replaced, which for an unverifiable version means
	// both start times must be known and different.
	servedVersion, servedPin := expectedServedBuild(facts, expected)
	var waitErr error
	if pending.start {
		waitErr = m.waitStartedHealthy(ctx, host, servedVersion, servedPin)
	} else {
		waitErr = m.waitHealthy(ctx, host, servedVersion, servedPin, pending.replaced)
	}
	if waitErr != nil {
		return waitErr
	}
	m.clearPendingRestart(host.Name)
	return nil
}

// restartBare implements the doc's four-step ad hoc-hub restart: find the pid
// by listening port, recover the exact argv and log destination, stop it, and
// relaunch detached with the recovered argv.
func (m *Manager) restartBare(ctx context.Context, host hostreg.Host, replaced hubIdentity) error {
	port := hubPort(m.hostAddr(host))
	pid, err := m.findHubPID(ctx, host, port)
	if err != nil {
		return err
	}

	// Validate before killing: the process merely holds the hub's port, which a
	// port collision — or a second hub on a different loopback address — could
	// make something other than the hub this host is configured for. Compare the
	// FULL normalized bind address, not just the port: a hub on 127.0.0.1:9180
	// does not own a host configured for 127.0.0.2:9180 and must not be killed.
	//
	// An invocation with no --addr bound whatever the host's hub.toml or the hub
	// default selects, and the controller cannot read that config. Only the hub
	// default is therefore provably this endpoint; a non-default configured
	// address is refused rather than assumed.
	argv, err := m.recoverHubArgv(ctx, host, pid)
	if err != nil {
		return err
	}
	expectedAddr := loopbackAddr(m.hostAddr(host))
	// Inspect the listener's actual bound address first: it is authoritative,
	// where the recovered --addr (or its absence) is only a proxy. A probe that
	// cannot run — neither lsof nor ss is available, or the tool answered with
	// something unusable — falls back to the recovered address, so such a host is
	// no worse off than before.
	addrs, aerr := m.hubListenerAddrs(ctx, host, pid, port)
	if aerr == nil && len(addrs) > 0 {
		owned := false
		for _, a := range addrs {
			if listenerOwnsAddr(a, expectedAddr) {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf("%w: host %q pid %s listens on %s but does not own the configured endpoint %s; refusing to restart it",
				ErrRestart, host.Name, pid, strings.Join(addrs, ", "), expectedAddr)
		}
		return m.stopAndRelaunch(ctx, host, port, pid, argv, replaced)
	}
	recoveredAddr, hasAddr := hubAddrFlag(argv)
	if !hasAddr {
		recoveredAddr = defaultHubAddr
	}
	if _, ok := argvHubPort(recoveredAddr); !ok {
		// An unparsable recovered address must not be defaulted onto :9180:
		// that would agree with a default-port host and let this kill a
		// listener the recovered invocation did not own.
		return fmt.Errorf("%w: host %q pid %s was launched with unparsable --addr %q; refusing to restart it",
			ErrRestart, host.Name, pid, recoveredAddr)
	}
	if norm := loopbackAddr(recoveredAddr); norm != expectedAddr {
		if hasAddr {
			return fmt.Errorf("%w: host %q pid %s was launched with --addr %q (listens on %s) but the configured endpoint is %s; refusing to restart it",
				ErrRestart, host.Name, pid, recoveredAddr, norm, expectedAddr)
		}
		return fmt.Errorf("%w: host %q pid %s was launched with no --addr (hub default %s) but the configured endpoint is %s; refusing to restart it",
			ErrRestart, host.Name, pid, norm, expectedAddr)
	}
	return m.stopAndRelaunch(ctx, host, port, pid, argv, replaced)
}

// stopAndRelaunch is the half of restartBare that runs once the listener has been
// proved to be the hub this host is configured for: it recovers the log
// destination, stops the old process, and relaunches the recovered argv detached.
func (m *Manager) stopAndRelaunch(ctx context.Context, host hostreg.Host, port, pid string, argv []string, replaced hubIdentity) error {
	pid, err := numericPID(pid)
	if err != nil {
		return err
	}
	logPath := ""
	if lout, lerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+pid+" -a -d 1,2"), nil); lerr == nil {
		logPath, _ = parseLogPath(lout)
	}

	relaunch := relaunchCommand(argv, logPath)
	// Stop the old hub and wait for its port to clear before starting a new
	// one; relaunching into the gap races hub.lock (docs/evener-hub-remote-
	// operations.md:363-377). The pid is validated, and `kill --` documents at
	// the command itself that the argument can only ever be a pid.
	kout, kerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "kill -- "+pid), nil)
	if kerr != nil {
		return fmt.Errorf("%w: host %q could not stop hub pid %s: %w: %s", ErrRestart, host.Name, pid, kerr, tail(kout))
	}
	// Record the relaunch before waiting for the port to clear: the kill has
	// landed, and waitPortClear can still fail afterwards (a graceful drain past
	// the wait window, a dropped transport, a context timeout). With the relaunch
	// recorded only after it succeeded, that failure left the old hub dead, no
	// listener on the port, and nothing for the next Ensure to recover.
	m.setPendingRestart(host.Name, relaunch, replaced)
	if err := m.waitPortClear(ctx, host, port); err != nil {
		return err
	}
	// The port clearing is not the same as the old hub being gone: the hub closes
	// its listener at the start of its graceful shutdown and keeps hub.lock while
	// it drains, so the replacement could still race the lock. Wait for the
	// process itself to exit, which is what releases the lock.
	if err := m.waitProcessExit(ctx, host, pid); err != nil {
		return err
	}

	rout, rerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, relaunch), nil)
	if rerr != nil {
		return fmt.Errorf("%w: host %q relaunch: %w: %s", ErrRestart, host.Name, rerr, tail(rout))
	}
	return nil
}

// noListenerMarker is printed by the port probe when a probe tool ran and found
// no listener. The raw exit status cannot carry that meaning over ssh: a missing
// tool, a transport failure, and a tool's own "no match" all reach the
// controller as empty stdout plus a nonzero exit. The probe translates only a
// tool's no-match answer into this marker with a zero exit, leaving every other
// failure to surface as a Run error.
const noListenerMarker = "__sshconn_no_listener__"

// listenerPresentMarker is printed by the port probe when a fallback tool proved
// a listener exists but could not name the process that owns it (ss without
// process visibility, or the /proc TCP tables). It is a non-empty answer: a
// caller asking "is the port free?" must read it as held, and findHubPID refuses
// to restart a listener it cannot identify rather than guessing at a PID.
const listenerPresentMarker = "__sshconn_listener_present__"

// listenerProbeRemote builds the remote command that reports the listeners on
// port.
//
// lsof names the owning PIDs, but it is not a dependency any host is documented
// or guaranteed to have: a bare Linux host provisioned by the installer fallback
// may carry only ss (iproute2) or only the kernel's /proc TCP tables. The probe
// used to run lsof unconditionally, so a host without it answered with a fatal
// ErrRestart and could never be provisioned at all (round eleven). The tiers are
// tried in order and the answer of the first tool that runs is authoritative:
//
//  1. lsof, which names the owning PIDs;
//  2. ss, which names them when the ssh user may see them and otherwise reports
//     listenerPresentMarker;
//  3. the /proc/net/tcp{,6} LISTEN rows (Linux), which prove presence without
//     naming a PID;
//  4. a nonzero exit with a diagnostic when no probe can run at all — never
//     noListenerMarker, because collision safety depends on an unprobeable port
//     being an error rather than a free port.
//
// Output contract: one PID per line, listenerPresentMarker, noListenerMarker, or
// a nonzero exit. A tier whose parser cannot run (no awk, an unreadable file)
// fails closed instead of falling through to "free".
func listenerProbeRemote(port string) string {
	colonPort := shellquote.RemoteWord(":" + port)
	return `p=` + shellquote.RemoteWord(port) + `
if command -v lsof >/dev/null 2>&1; then
	lsof -ti ` + colonPort + ` -sTCP:LISTEN; s=$?; if [ $s -eq 1 ]; then echo ` + noListenerMarker + `; exit 0; fi; exit $s
fi
if command -v ss >/dev/null 2>&1; then
	out=$(ss -ltnp 2>/dev/null) && {
		printf '%s\n' "$out" | awk -v port=` + colonPort + ` -v nomatch=` + noListenerMarker + ` -v nopid=` + listenerPresentMarker + ` '
			$1 == "LISTEN" && index($4, port) == length($4) - length(port) + 1 {
				hit = 1
				n = split($0, a, "pid=")
				for (i = 2; i <= n; i++) { q = a[i]; sub(/[^0-9].*/, "", q); if (q != "") { print q; named = 1 } }
			}
			END { if (!hit) print nomatch; else if (!named) print nopid }
		'
		s=$?
		exit $s
	}
fi
if [ -r /proc/net/tcp ]; then
	hex=$(printf '%04X' "$p")
	found=0
	for f in /proc/net/tcp /proc/net/tcp6; do
		[ -r "$f" ] || continue
		v=$(awk -v hp="$hex" 'NR > 1 && toupper($4) == "0A" { n = split($2, a, ":"); if (toupper(a[n]) == hp) f = 1 } END { print f ? "hit" : "miss" }' "$f") || { s=$?; echo 'sshconn: cannot read the host TCP tables (awk failed on '"$f"')' >&2; exit $s; }
		[ "$v" = "hit" ] && found=1
	done
	if [ "$found" = 1 ]; then echo ` + listenerPresentMarker + `; else echo ` + noListenerMarker + `; fi
	exit 0
fi
echo 'sshconn: no listener probe is available on this host (lsof, ss, and /proc/net/tcp are all missing); install lsof or iproute2' >&2
exit 1
`
}

// listenerProbe is one port probe's answer: the PIDs a probe tool could name,
// and whether a listener was proved to exist that no tool could name.
type listenerProbe struct {
	pids    []string
	present bool
}

// probeListeners reports the listeners on port. A probe that could not run (a
// transport failure, no probe tool at all, an unreadable TCP table) is an error,
// so the caller never mistakes it for a free or held port.
//
// Every line that is not one of the markers must be a numeric pid: those pids
// flow into `kill`, `lsof -p`, `ps -p`, and `/proc/<pid>/cmdline`, where a
// leading dash is an option and a slash or semicolon is a path fragment or a
// command separator rather than a digit. A non-numeric line is therefore refused
// here, at the parse boundary, instead of being quoted and used; quoting alone
// cannot make `-9` or `/etc/passwd` safe in those positions.
func (m *Manager) probeListeners(ctx context.Context, host hostreg.Host, port string) (listenerProbe, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, listenerProbeRemote(port)), nil)
	if err != nil {
		return listenerProbe{}, fmt.Errorf("%w: host %q could not probe listeners on :%s: %w: %s", ErrRestart, host.Name, port, err, tail(out))
	}
	var lp listenerProbe
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == noListenerMarker {
			continue
		}
		if line == listenerPresentMarker {
			lp.present = true
			continue
		}
		if !isNumericUID(line) {
			return listenerProbe{}, fmt.Errorf("%w: host %q listener probe on :%s reported a non-numeric pid %q; refusing to use it", ErrRestart, host.Name, port, line)
		}
		lp.pids = append(lp.pids, line)
	}
	return lp, nil
}

// listenerAddrRemote builds the remote command that prints the local addresses
// pid listens on for port, one per `n` field so a NAME containing spaces cannot
// be misread. lsof is preferred; a host without it falls back to ss, whose
// numeric local-address column uses the same host:port spelling lsof -F n prints
// (`127.0.0.1:9180`, `[::1]:9180`, `*:9180`), so the caller's ownership check
// reads either source. Each tier translates its no-match answer into an empty
// success, and a tier that cannot run (no awk, no probe tool) exits nonzero, so
// a raced listener is never a transport failure and a missing probe is never
// read as "no address".
//
// pid is validated by the caller (numericPID) before it is rendered here: `lsof
// -p` and the awk `pid=` filter both take it as a bare word, where a leading
// dash would be an option.
func listenerAddrRemote(pid, port string) string {
	q := shellquote.RemoteWord(":" + port)
	return "if command -v lsof >/dev/null 2>&1; then lsof -nP -p " + pid + " -a -iTCP" + q + " -sTCP:LISTEN -F n; s=$?; if [ $s -eq 1 ]; then exit 0; fi; exit $s; fi; " +
		"if command -v ss >/dev/null 2>&1; then ss -ltnp 2>/dev/null | awk -v port=" + q + " -v pid=" + pid + " '$1 == \"LISTEN\" && index($4, port) == length($4) - length(port) + 1 && $0 ~ (\"pid=\" pid \"[^0-9]\") { print \"n\" $4 }'; exit $?; fi; " +
		"echo 'sshconn: no listener address probe is available on this host (neither lsof nor ss); install lsof' >&2; exit 1"
}

// hubListenerAddrs returns the local addresses pid listens on for port. An empty
// slice means the probe matched no socket (the process may have exited), which
// the caller reads as "not proved to own the endpoint".
func (m *Manager) hubListenerAddrs(ctx context.Context, host hostreg.Host, pid, port string) ([]string, error) {
	pid, err := numericPID(pid)
	if err != nil {
		return nil, err
	}
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, listenerAddrRemote(pid, port)), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q could not inspect listener %s on :%s: %w: %s", ErrRestart, host.Name, pid, port, err, tail(out))
	}
	var addrs []string
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "n") {
			continue
		}
		if a := strings.TrimPrefix(line, "n"); a != "" {
			addrs = append(addrs, a)
		}
	}
	return addrs, nil
}

// listenerOwnsAddr reports whether a local listening address owns the configured
// endpoint: the same normalized host:port, or a wildcard bind on the same port.
// Comparing the full address, not the port alone, is what stops a hub on
// 127.0.0.1:9180 being killed for a host configured for 127.0.0.2:9180. The
// wildcard's ADDRESS FAMILY is part of that comparison: an IPv4 wildcard
// (0.0.0.0) serves no IPv6 address, and an IPv6 wildcard (::) serves IPv6
// addresses only — whether it also accepts IPv4-mapped traffic depends on the
// socket's IPV6_V6ONLY setting, which neither lsof nor ss reports, so a listener
// that may be IPv6-only is never read as owning an IPv4 endpoint. lsof spells
// every wildcard bind "*" without naming its family, so that spelling keeps its
// existing meaning (owns the endpoint on the port, whichever family it is).
func listenerOwnsAddr(local, configured string) bool {
	lh, lp, err := net.SplitHostPort(local)
	if err != nil {
		return false
	}
	ch, cp, err := net.SplitHostPort(configured)
	if err != nil || lp != cp {
		return false
	}
	switch lh {
	case "", "*":
		return true
	case "0.0.0.0":
		return hostIsIPv4(ch)
	case "::":
		return hostIsIPv6(ch)
	}
	return loopbackAddr(local) == loopbackAddr(configured)
}

// hostIsIPv4 reports whether host is an IPv4 address, including the
// IPv4-mapped IPv6 spelling the kernel's tables can produce.
func hostIsIPv4(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil
}

// hostIsIPv6 reports whether host is an IPv6 address that is not an IPv4
// address: an IPv4-mapped spelling names an IPv4 endpoint and belongs to the
// other family.
func hostIsIPv6(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() == nil
}

// findHubPID returns the single pid listening on the hub's port. There is no
// pgrep substitute (pgrep -x evener also matches `evener serve` daemons), so a
// listener the host's probes can see but not name — a host without lsof whose ss
// shows another user's socket — is refused rather than guessed at. hub.lock
// already enforces one hub per host, so more than one listener is a real
// finding: guessing would risk killing or restarting the wrong process.
func (m *Manager) findHubPID(ctx context.Context, host hostreg.Host, port string) (string, error) {
	lp, err := m.probeListeners(ctx, host, port)
	if err != nil {
		return "", err
	}
	if lp.present && len(lp.pids) == 0 {
		// Do not fall through to "no hub listening": the port is held, and the
		// caller must not read this as "nothing to restart" any more than it may
		// read a failed probe as a free port.
		return "", fmt.Errorf("%w: host %q has a listener on :%s but no probe on the host can name the process that owns it; install lsof on the host to restart it",
			ErrRestart, host.Name, port)
	}
	switch len(lp.pids) {
	case 0:
		return "", fmt.Errorf("%w: host %q no hub listening on :%s to restart", ErrRestart, host.Name, port)
	case 1:
		return lp.pids[0], nil
	default:
		return "", fmt.Errorf("%w: host %q %d processes listen on :%s (%s); refusing to guess which one is the hub",
			ErrRestart, host.Name, len(lp.pids), port, strings.Join(lp.pids, ", "))
	}
}

// waitPortClear blocks until nothing is listening on the hub port or the bound
// is exhausted. A probe that cannot run is surfaced immediately: treating it as
// a cleared port would relaunch while the old hub still holds hub.lock.
func (m *Manager) waitPortClear(ctx context.Context, host hostreg.Host, port string) error {
	for range restartStopAttempts {
		cleared, err := m.portCleared(ctx, host, port)
		if err != nil {
			return err
		}
		if cleared {
			return nil
		}
		if err := m.opts.waitSleep(ctx, restartStopInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: host %q hub port :%s still held after kill", ErrRestart, host.Name, port)
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

// portCleared reports whether the host has no listener on the hub port. The
// probe's result distinguishes a genuinely free port from a probe that could
// not run, which the caller must not treat as cleared; a listener no tool could
// name holds the port just as a named one does.
func (m *Manager) portCleared(ctx context.Context, host hostreg.Host, port string) (bool, error) {
	lp, err := m.probeListeners(ctx, host, port)
	if err != nil {
		return false, err
	}
	return len(lp.pids) == 0 && !lp.present, nil
}

// hubHealthRemote builds the remote command that reads the host hub's
// /api/health. The address is the configured host address, normalized for a
// wildcard bind the way the attach path normalizes it, and the whole URL is
// quoted as one word so a host-derived address cannot inject a shell command.
// The scheme is explicit: without it curl only guesses from the host part, which
// is unreliable for a bracketed IPv6 literal ("[::1]:9180/api/health") and is
// not what the documented probe (`curl -fsS http://127.0.0.1:9180/api/health`,
// docs/evener-hub.md) uses.
// -q (which must lead the option list) and --noproxy '*' keep a host-side
// .curlrc or proxy environment from answering in place of the loopback hub, and
// the timeouts bound a listener that accepts but never answers.
func hubHealthRemote(addr string) string {
	return "curl -q --noproxy '*' -fsS --connect-timeout " + healthCurlConnectTimeout +
		" --max-time " + healthCurlMaxTime + " " + shellquote.RemoteWord("http://"+loopbackAddr(addr)+"/api/health")
}

// probeRunningHub asks the host hub's /api/health once and returns the identity
// it reports: the version, plus the process start time when the body carries one.
// ok is false when nothing answered or the body was not a health response, which
// is "no running hub to judge" rather than an error: ensureOnce must not invent a
// restart from an unanswerable probe.
func (m *Manager) probeRunningHub(ctx context.Context, host hostreg.Host) (hubIdentity, bool) {
	addr := m.hostAddr(host)
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, hubHealthRemote(addr)), nil)
	if err != nil {
		return hubIdentity{}, false
	}
	return parseHubHealth(out, addr)
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

// waitHealthy polls the host hub's /api/health until the response reports
// expectedVersion or the bound is exhausted. The expected version is what makes
// this the authoritative restart check: any hub answer proves a hub is serving,
// but only the expected version proves the *deployed* build is the one running.
// Accepting a bare healthy response would mask a failed restart (the old hub
// still answering) and could attach to the dying old process during its
// shutdown drain, tearing down the fresh channel.
//
// expectedGitSHA is the snapshot pin described by snapshotPinGitSHA, already
// resolved by the caller; empty means there is none (see hubBuildSHAMatches for
// what the pin adds). It is a separate argument rather than something this
// function reads itself so a caller's choice of pin is visible at the call.
//
// replaced is the identity of the hub process the restart expects to have
// replaced. A stamped version is a content identity, so an answer reporting it
// proves the deployed build is serving and only a provably unchanged process
// (sameProcessAs) is refused. "dev" and a dirty "<sha>-dirty" are NOT identities:
// two builds report them, so equality proves nothing about the code and the
// answer must PROVE it is a different process than the one replaced — both start
// times known and different (differentProcessFrom, the strict rule the
// pending-restart settle path in ensureOnce already uses). A start time missing
// on either side proves nothing, so such an answer is never accepted: the
// restart is left unresolved and retried instead of being read as a replacement.
// waitStartedHealthy is the same wait for a hub this Manager STARTS.
func (m *Manager) waitHealthy(ctx context.Context, host hostreg.Host, expectedVersion, expectedGitSHA string, replaced hubIdentity) error {
	return m.waitForHealthyHub(ctx, host, expectedVersion, expectedGitSHA, replaced, true)
}

// waitStartedHealthy verifies a hub this Manager started where nothing was
// serving: the bootstrap start, and a pending command bootstrapHub recorded. It
// deliberately has no predecessor: bootstrapHub refuses to start anything while
// a listener holds the address, so there is no process whose survival could
// satisfy the wait, and the expected version alone is the proof a start can
// offer. The exception belongs to the start paths only — a restart whose
// predecessor identity is unknown must fail closed (see waitHealthy), or an old
// process whose health body carries no started_at would satisfy it.
//
// expectedGitSHA is the snapshot pin described by snapshotPinGitSHA, resolved by
// the caller exactly as waitHealthy takes it. A start passes one when the host
// carries this controller's build: the binary it launches is then the one a
// deploy installed, and a snapshot version cannot tell two builds apart — so a
// start on a stopped host (a deploy that found no hub present, or a first attach
// to a host already running a foreign snapshot build of the same version) would
// otherwise attach to a commit this controller did not install. The pin is the
// only build evidence a start can offer; an empty pin leaves a non-snapshot
// start on the version-equality rule unchanged.
//
// Neither argument is unconditionally the controller's own build: a host that
// kept its own binary is started and judged against THAT build, and against no
// pin at all. expectedServedBuild decides which, and why.
func (m *Manager) waitStartedHealthy(ctx context.Context, host hostreg.Host, expectedVersion, expectedGitSHA string) error {
	return m.waitForHealthyHub(ctx, host, expectedVersion, expectedGitSHA, hubIdentity{}, false)
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

// waitForHealthyHub is the shared poll behind waitHealthy and
// waitStartedHealthy. replacing says whether the wait verifies a REPLACEMENT (a
// restart) rather than a start where nothing was serving.
func (m *Manager) waitForHealthyHub(ctx context.Context, host hostreg.Host, expectedVersion, expectedGitSHA string, replaced hubIdentity, replacing bool) error {
	port := hubPort(m.hostAddr(host))
	addr := m.hostAddr(host)
	remote := hubHealthRemote(addr)
	var last hubIdentity
	var lastGitSHA string
	for range restartHealthAttempts {
		if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil); err == nil {
			if got, gitSHA, ok := parseHubHealthBuild(out, addr); ok {
				last = got
				lastGitSHA = gitSHA
				if got.version == expectedVersion &&
					hubAnswerProvesReplacement(got, replaced, expectedVersion, replacing) &&
					hubBuildSHAMatches(gitSHA, expectedGitSHA) {
					return nil
				}
			}
		}
		if err := m.opts.waitSleep(ctx, restartHealthInterval); err != nil {
			return err
		}
	}
	if last.version != "" {
		if last.sameProcessAs(replaced) {
			return fmt.Errorf("%w: host %q hub on :%s is still the pre-restart process (started %s, version %q); the restart did not take",
				ErrRestart, host.Name, port, last.startedAt.Format(time.RFC3339Nano), last.version)
		}
		if replacing && last.version == expectedVersion && isUnverifiableVersion(expectedVersion) {
			// The version matched, but the identity did not prove a replacement:
			// a "dev"/dirty version names no code, and either the pre-restart
			// identity or this answer carries no start time, which proves nothing
			// either way.
			return fmt.Errorf("%w: host %q hub on :%s reports the expected version %q but not a provably different process (pre-restart: %s; answer: %s); the restart is unverified",
				ErrRestart, host.Name, port, expectedVersion, describeHubIdentity(replaced), describeHubIdentity(last))
		}
		if expectedGitSHA != "" && last.version == expectedVersion && lastGitSHA != expectedGitSHA {
			// The version matched but the build identity did not: two snapshot
			// builds share a version, so this is exactly the mismatch the
			// version-equality rule cannot see. The hub is not the deployed commit.
			return fmt.Errorf("%w: host %q hub on :%s reports version %q but backend_git_sha %q, want %q; the running hub is not the snapshot build this controller carries",
				ErrRestart, host.Name, port, last.version, lastGitSHA, expectedGitSHA)
		}
		return fmt.Errorf("%w: host %q hub on :%s reports version %q, want %q after restart", ErrRestart, host.Name, port, last.version, expectedVersion)
	}
	return fmt.Errorf("%w: host %q hub not healthy on :%s after restart", ErrRestart, host.Name, port)
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

// hubAnswerProvesReplacement reports whether an answer carrying expectedVersion
// proves the hub serving it is the outcome a wait needs. For a stamped version,
// equality is a content identity and only a provably unchanged process is
// refused. For an unverifiable version ("dev", a dirty "<sha>-dirty") the answer
// must be provably a different process than the one replaced — both start times
// known and different — except for a start, which has no predecessor to exclude.
func hubAnswerProvesReplacement(got, replaced hubIdentity, expectedVersion string, replacing bool) bool {
	if !isUnverifiableVersion(expectedVersion) {
		return !got.sameProcessAs(replaced)
	}
	if !replacing {
		return true
	}
	return got.differentProcessFrom(replaced)
}

// describeHubIdentity renders an identity for a diagnostic. A health body
// without started_at is named as such rather than printed as a zero timestamp.
func describeHubIdentity(h hubIdentity) string {
	if h.startedAt.IsZero() {
		return fmt.Sprintf("version %q, start time unknown", h.version)
	}
	return fmt.Sprintf("version %q, started %s", h.version, h.startedAt.Format(time.RFC3339Nano))
}

// parseHubHealth reads the running hub's identity from a /api/health body. A
// body that is not the hub's HealthResponse JSON is not usable evidence, so it
// is reported as absent rather than as whatever the version field decoded to.
// That includes JSON that decodes but carries no version: `{}` or
// `{"status":"ok"}` from some unrelated listener is not a hub with an empty
// version, and reading it as one would invent a restart against a non-hub
// process. The start time is carried when the body provides one; it is what
// distinguishes a genuinely new process from the one a restart replaced.
//
// A version alone is not enough to call a response authoritative. Any unrelated
// service that answers on the hub's loopback port with the expected version —
// a stale hub from another install, a proxy, a test server — would otherwise
// read as the host's hub: version auto-match would skip recovery, the restart
// verification would accept it, and attach would hand it the hub auth token.
// Two stable hub-specific fields must agree as well:
//
//   - mobile_api_version, the hub's REST contract version (hubapi), which an
//     unrelated JSON responder does not carry at all;
//   - hub_addr, the address the hub actually bound, which must be the endpoint
//     this controller configured (normalized the same way the probe's URL is,
//     so a wildcard bind still matches the loopback address it is reached on).
func parseHubHealth(out []byte, addr string) (hubIdentity, bool) {
	id, _, ok := parseHubHealthBuild(out, addr)
	return id, ok
}

// parseHubHealthBuild is parseHubHealth plus the build's backend_git_sha, the
// field a snapshot pin's post-restart check compares against the controller's
// own commit. Everything that makes a body usable as the hub's (a version, the
// hub's contract version, the bound address) is decided once here, so the SHA is
// only ever read from a body already accepted as this hub.
func parseHubHealthBuild(out []byte, addr string) (hubIdentity, string, bool) {
	var resp hubapi.HealthResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.Version == "" {
		return hubIdentity{}, "", false
	}
	if resp.MobileAPIVersion != hubapi.MobileAPIVersion {
		return hubIdentity{}, "", false
	}
	if loopbackAddr(resp.HubAddr) != loopbackAddr(addr) {
		return hubIdentity{}, "", false
	}
	return hubIdentity{version: resp.Version, startedAt: resp.StartedAt}, resp.BackendGitSha, true
}

// recoverHubArgv recovers the argv of pid. It prefers the host's null-delimited
// /proc/<pid>/cmdline (Linux), the only form that preserves argument boundaries:
// `ps -o command=` joins argv with single spaces, so a path containing a space
// would be split and a wrong argv relaunched. Where the null-delimited source is
// unavailable (nothing printed: no readable /proc, as on macOS), it falls back
// to ps and refuses a command line whose word boundaries are ambiguous rather
// than guessing.
//
// A readable /proc whose bytes cannot be parsed (an empty word, as
// `evener hub -config ""` produces) does NOT fall back: the exact source was
// available and is unusable, and the ps fallback is lossy in exactly that way —
// it would drop the empty value and relaunch as `evener hub -config`. The old
// hub is already stopped by then, so the wrong relaunch would leave the host
// with no hub and recovery would retry the same broken command. Fail closed.
func (m *Manager) recoverHubArgv(ctx context.Context, host hostreg.Host, pid string) ([]string, error) {
	argv, err := m.recoverHubArgvRaw(ctx, host, pid)
	if err != nil {
		return nil, err
	}
	if err := m.hubExecutableMatches(ctx, host, argv[0]); err != nil {
		return nil, err
	}
	return argv, nil
}

// recoverHubArgvRaw is recoverHubArgv without the executable-identity check: it
// recovers and validates a plausible `evener hub` invocation but does not
// require its executable to be the manager's configured target. The deploy path
// uses it to learn where an existing hub is installed so it can install the
// controller's build over that same file; the restart path uses recoverHubArgv,
// which keeps the identity check.
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

// currentHubExecutableName resolves the user-facing path of the executable of the
// hub currently listening on the host's configured endpoint, or "" when no single
// hub listener can be identified. It reports no error: an unidentifiable or
// unprobeable hub is a missing preservation hint, not a reason to fail a deploy.
//
// It does NOT canonicalize symlinks. The installer fallback needs the path the
// operator's installation names (argv[0], or the `command -v` result for a bare
// name) so installerDirs derives BINDIR from the directory holding the
// user-facing symlink; resolving first returned the real binary under
// share/evener/bin and produced a nested, wrong install location. Canonical
// resolution for identity lives in hubExecutableMatches.
func (m *Manager) currentHubExecutableName(ctx context.Context, host hostreg.Host) string {
	lp, err := m.probeListeners(ctx, host, hubPort(m.hostAddr(host)))
	if err != nil || len(lp.pids) != 1 || lp.present {
		// A listener no probe could name, or more than one of them, leaves the
		// preservation hint ambiguous; a deploy must not derive an install layout
		// from a guess.
		return ""
	}
	argv, err := m.recoverHubArgvRaw(ctx, host, lp.pids[0])
	if err != nil {
		return ""
	}
	exe, err := m.resolveExecutableName(ctx, host, argv[0])
	if err != nil {
		return ""
	}
	return exe
}

// installableEvenerBasename reports whether a basename names the binary a host
// hub can be run as. Only `evener` does: release archives carry `evener-dev`
// too, but that is the development/test tooling binary
// (cmd/evener-dev/bin) — no `hub` subcommand and no `launch-check` — so a run
// target naming it would be probed, relaunched, and attached as a hub that can
// never answer. A deploy cannot preserve a run target the host cannot serve.
func installableEvenerBasename(p string) bool {
	return path.Base(strings.TrimSpace(p)) == "evener"
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
		return fmt.Errorf("%w: host %q hub executable %q (resolves to %q) is not the configured target %q; refusing to restart it",
			ErrRestart, host.Name, exe, actual, expected)
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
	p := firstLine(string(out))
	if p == "" {
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
	if p := strings.TrimSpace(host.EvenerPath); p != "" {
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
	p := firstLine(string(out))
	if p == "" {
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
	resolved := firstLine(string(out))
	if resolved == "" {
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
// what keeps restartBare from killing and relaunching a process merely because it
// holds the hub's port.
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
		// The NAME column begins at field 8 and may itself contain spaces (a log
		// path like "/home/dev/my hub.log"), so rejoin the remainder rather than
		// taking the last whitespace-delimited word.
		name := strings.Join(fields[8:], " ")
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
