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

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/hubapi"
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

// restartRemote is the remote command that restarts the supervised hub.
func (s supervisor) restartRemote() string {
	switch s.kind {
	case supervisorLaunchd:
		return "launchctl kickstart -k gui/$(id -u)/" + shellQuote(s.label)
	case supervisorSystemd:
		return "systemctl restart " + shellQuote(s.label)
	case supervisorSystemdUser:
		return "systemctl --user restart " + shellQuote(s.label)
	default:
		return ""
	}
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

// detectSupervisorFrom classifies a host from raw supervisor listings. It is
// pure so the table test needs no ssh: launchd for darwin, systemd (system,
// then --user) for linux, otherwise a bare process.
//
// More than one matching label or unit is an error rather than a silent pick of
// the first: restarting the wrong evener service would leave the hub the
// controller meant to replace untouched while reporting success.
func detectSupervisorFrom(goos string, launchctlOut, systemdOut, systemdUserOut []byte) (supervisor, error) {
	switch goos {
	case "darwin":
		return pickSupervisor(supervisorLaunchd, "launchd", parseLaunchdHubs(launchctlOut))
	case "linux":
		if sup, err := pickSupervisor(supervisorSystemd, "systemd", parseSystemdHubs(systemdOut)); err != nil || sup.kind != supervisorNone {
			return sup, err
		}
		return pickSupervisor(supervisorSystemdUser, "systemd --user", parseSystemdHubs(systemdUserOut))
	default:
		return supervisor{}, nil
	}
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

// parseLaunchdHubs finds launchd jobs whose labels name an evener hub and that
// are actually running. `launchctl list` prints "PID Status Label" per line; a
// loaded-but-not-running job shows "-" in the PID column. Treating such a job as
// a live hub would make restartHub start a second hub (which then fails on
// hub.lock) while an ad hoc hub kept serving the old version, so it must fall
// through to the bare-process path instead. The doc's precedent is
// `launchctl list | grep evener-hub`.
func parseLaunchdHubs(out []byte) []string {
	var labels []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "PID" {
			continue
		}
		if fields[0] == "-" {
			continue
		}
		if label := fields[len(fields)-1]; isEvenerHubName(label) {
			labels = append(labels, label)
		}
	}
	return labels
}

// parseSystemdHubs finds evener hub units in `systemctl list-units` output.
// Each line starts with the unit name followed by LOAD, ACTIVE, and SUB, e.g.
// "evener-hub.service loaded active running". A leading status glyph (●) is
// stripped. `--all` also lists loaded-but-inactive units, which must not be read
// as a live hub: restarting one would start a second hub while an ad hoc one
// still holds hub.lock. SUB must be `running` too: `loaded active exited` is a
// unit that is loaded and enabled but has no process serving, so treating it as
// a live supervised hub would restart a unit instead of the ad hoc hub holding
// the port.
func parseSystemdHubs(out []byte) []string {
	var units []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "●"))
		if len(fields) < 4 {
			continue
		}
		unit := fields[0]
		if !strings.HasSuffix(unit, ".service") {
			continue
		}
		if fields[1] != "loaded" || fields[2] != "active" || fields[3] != "running" {
			continue
		}
		if isEvenerHubName(strings.TrimSuffix(unit, ".service")) {
			units = append(units, unit)
		}
	}
	return units
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

// detectSupervisor runs the host's supervisor listings. Only a listing that
// proves the supervisor is absent falls through to the bare-process path; any
// other failure is surfaced. An ambiguous listing is fatal, not a fallback.
func (m *Manager) detectSupervisor(ctx context.Context, host hostreg.Host, facts Preflight) (supervisor, error) {
	switch facts.OS {
	case "darwin":
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "launchctl list"), nil)
		if err != nil && !supervisorListingAbsent(out) {
			return supervisor{}, fmt.Errorf("%w: host %q launchctl list: %w: %s", ErrRestart, host.Name, err, tail(out))
		}
		return detectSupervisorFrom("darwin", out, nil, nil)
	case "linux":
		sysOut, sysErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, systemctlListUnits), nil)
		if sysErr != nil {
			if !supervisorListingAbsent(sysOut) {
				return supervisor{}, fmt.Errorf("%w: host %q systemctl list-units: %w: %s", ErrRestart, host.Name, sysErr, tail(sysOut))
			}
			sysOut = nil
		}
		userOut, userErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, systemctlListUnitsUser), nil)
		if userErr != nil {
			if !supervisorListingAbsent(userOut) {
				return supervisor{}, fmt.Errorf("%w: host %q systemctl --user list-units: %w: %s", ErrRestart, host.Name, userErr, tail(userOut))
			}
			userOut = nil
		}
		return detectSupervisorFrom("linux", nil, sysOut, userOut)
	default:
		return supervisor{}, nil
	}
}

// restartHub restarts the host's hub after a deploy. It prefers a detected
// supervisor (launchd/systemd) and otherwise restarts the bare process by
// recovering its pid, argv, and log. It never starts a second hub while the old
// one holds hub.lock: the bare path waits for the port to clear before
// relaunching. replaced is the identity of the hub process this restart expects
// to replace, so a non-unique version cannot make the old process look like a
// successful replacement.
func (m *Manager) restartHub(ctx context.Context, host hostreg.Host, facts Preflight, replaced hubIdentity) error {
	expected := m.opts.controllerVersion()
	sup, err := m.detectSupervisor(ctx, host, facts)
	if err != nil {
		return err
	}
	if sup.kind != supervisorNone {
		remote := sup.restartRemote()
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
		if err := m.waitHealthy(ctx, host, expected, replaced); err != nil {
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

	if err := m.restartBare(ctx, host, replaced); err != nil {
		return err
	}
	if err := m.waitHealthy(ctx, host, expected, replaced); err != nil {
		return err
	}
	// A healthy replacement is serving, so any relaunch this Manager recorded for
	// this host is settled.
	m.clearPendingRestart(host.Name)
	return nil
}

// recoverRestart retries the restart command a previous attempt recorded before
// it ran: a bare relaunch or a supervisor restart. It is the recovery half of the
// ladder: a restart that killed the old hub but left no listener must be
// completed by the next Ensure, which is what ErrRestart promises. There is no
// hub to kill here, so it runs the recorded command directly and waits for the
// expected build to answer.
func (m *Manager) recoverRestart(ctx context.Context, host hostreg.Host, pending pendingRestartState) error {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, pending.command), nil)
	if err != nil {
		return fmt.Errorf("%w: host %q restart recovery: %w: %s", ErrRestart, host.Name, err, tail(out))
	}
	if err := m.waitHealthy(ctx, host, m.opts.controllerVersion(), pending.replaced); err != nil {
		return err
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
	// port collision could make an unrelated service. Only a recovered
	// `evener hub` invocation whose --addr agrees with the probed port is
	// restarted; anything else is refused rather than guessed at.
	argv, err := m.recoverHubArgv(ctx, host, pid)
	if err != nil {
		return err
	}
	if a, ok := hubAddrFlag(argv); ok {
		ap, ok := argvHubPort(a)
		if !ok {
			// An unparsable recovered address must not be defaulted onto :9180:
			// that would agree with a default-port host and let this kill a
			// listener the recovered invocation did not own.
			return fmt.Errorf("%w: host %q pid %s was launched with unparsable --addr %q; refusing to restart it",
				ErrRestart, host.Name, pid, a)
		}
		if ap != port {
			return fmt.Errorf("%w: host %q pid %s listens on :%s but was launched with --addr %q; refusing to restart it",
				ErrRestart, host.Name, pid, port, a)
		}
	}

	logPath := ""
	if lout, lerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+shellQuote(pid)+" -a -d 1,2"), nil); lerr == nil {
		logPath, _ = parseLogPath(lout)
	}

	relaunch := relaunchCommand(argv, logPath)
	// Stop the old hub and wait for its port to clear before starting a new
	// one; relaunching into the gap races hub.lock (docs/evener-hub-remote-
	// operations.md:363-377).
	kout, kerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "kill "+shellQuote(pid)), nil)
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

	rout, rerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, relaunch), nil)
	if rerr != nil {
		return fmt.Errorf("%w: host %q relaunch: %w: %s", ErrRestart, host.Name, rerr, tail(rout))
	}
	return nil
}

// noListenerMarker is printed by the port probe when lsof runs and finds no
// listener. The raw exit status cannot carry that meaning over ssh: a missing
// lsof, a transport failure, and lsof's own "no match" all reach the controller
// as empty stdout plus a nonzero exit. The probe translates only lsof's no-match
// exit into this marker with a zero exit, leaving every other failure to
// surface as a Run error.
const noListenerMarker = "__sshconn_no_listener__"

// listenerProbeRemote builds the remote command that lists the PIDs listening on
// port, printing noListenerMarker and exiting 0 when lsof finds none. lsof exits
// 1 for "no matching files"; any other exit (lsof absent, a signal, ssh itself
// failing) is propagated so the caller cannot read it as a free port.
func listenerProbeRemote(port string) string {
	q := shellQuote(":" + port)
	return "lsof -ti " + q + " -sTCP:LISTEN; s=$?; if [ $s -eq 1 ]; then echo " + noListenerMarker + "; exit 0; fi; exit $s"
}

// hubListenerPIDs returns every PID listening on port. A probe that could not
// run (a transport failure, a missing lsof) is an error, so the caller never
// mistakes it for a free or held port.
func (m *Manager) hubListenerPIDs(ctx context.Context, host hostreg.Host, port string) ([]string, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, listenerProbeRemote(port)), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: host %q could not probe listeners on :%s: %w: %s", ErrRestart, host.Name, port, err, tail(out))
	}
	var pids []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" && line != noListenerMarker {
			pids = append(pids, line)
		}
	}
	return pids, nil
}

// findHubPID returns the single pid listening on the hub's port, using lsof
// because pgrep -x evener also matches `evener serve` daemons. hub.lock already
// enforces one hub per host, so more than one listener is a real finding:
// guessing would risk killing or restarting the wrong process.
func (m *Manager) findHubPID(ctx context.Context, host hostreg.Host, port string) (string, error) {
	pids, err := m.hubListenerPIDs(ctx, host, port)
	if err != nil {
		return "", err
	}
	switch len(pids) {
	case 0:
		return "", fmt.Errorf("%w: host %q no hub listening on :%s to restart", ErrRestart, host.Name, port)
	case 1:
		return pids[0], nil
	default:
		return "", fmt.Errorf("%w: host %q %d processes listen on :%s (%s); refusing to guess which one is the hub",
			ErrRestart, host.Name, len(pids), port, strings.Join(pids, ", "))
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

// portCleared reports whether the host has no listener on the hub port. The
// probe's result distinguishes a genuinely free port from a probe that could
// not run, which the caller must not treat as cleared.
func (m *Manager) portCleared(ctx context.Context, host hostreg.Host, port string) (bool, error) {
	pids, err := m.hubListenerPIDs(ctx, host, port)
	if err != nil {
		return false, err
	}
	return len(pids) == 0, nil
}

// hubHealthRemote builds the remote command that reads the host hub's
// /api/health. The address is the configured host address, normalized for a
// wildcard bind the way the attach path normalizes it, and the whole URL is
// quoted as one word so a host-derived address cannot inject a shell command.
// -q (which must lead the option list) and --noproxy '*' keep a host-side
// .curlrc or proxy environment from answering in place of the loopback hub, and
// the timeouts bound a listener that accepts but never answers.
func hubHealthRemote(addr string) string {
	return "curl -q --noproxy '*' -fsS --connect-timeout " + healthCurlConnectTimeout +
		" --max-time " + healthCurlMaxTime + " " + shellQuote(loopbackAddr(addr)+"/api/health")
}

// probeRunningHub asks the host hub's /api/health once and returns the identity
// it reports: the version, plus the process start time when the body carries one.
// ok is false when nothing answered or the body was not a health response, which
// is "no running hub to judge" rather than an error: ensureOnce must not invent a
// restart from an unanswerable probe.
func (m *Manager) probeRunningHub(ctx context.Context, host hostreg.Host) (hubIdentity, bool) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, hubHealthRemote(m.hostAddr(host))), nil)
	if err != nil {
		return hubIdentity{}, false
	}
	return parseHubHealth(out)
}

// waitHealthy polls the host hub's /api/health until the response reports
// expectedVersion or the bound is exhausted. The expected version is what makes
// this the authoritative restart check: any hub answer proves a hub is serving,
// but only the expected version proves the *deployed* build is the one running.
// Accepting a bare healthy response would mask a failed restart (the old hub
// still answering) and could attach to the dying old process during its
// shutdown drain, tearing down the fresh channel. replaced is the pre-restart
// hub identity: an answer that is provably that same process is never accepted,
// because a non-unique version ("dev") would otherwise let a restart that never
// took look like success.
func (m *Manager) waitHealthy(ctx context.Context, host hostreg.Host, expectedVersion string, replaced hubIdentity) error {
	port := hubPort(m.hostAddr(host))
	remote := hubHealthRemote(m.hostAddr(host))
	var last hubIdentity
	for range restartHealthAttempts {
		if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil); err == nil {
			if got, ok := parseHubHealth(out); ok {
				if got.version == expectedVersion && !got.sameProcessAs(replaced) {
					return nil
				}
				last = got
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
		return fmt.Errorf("%w: host %q hub on :%s reports version %q, want %q after restart", ErrRestart, host.Name, port, last.version, expectedVersion)
	}
	return fmt.Errorf("%w: host %q hub not healthy on :%s after restart", ErrRestart, host.Name, port)
}

// parseHubHealth reads the running hub's identity from a /api/health body. A
// body that is not the hub's HealthResponse JSON is not usable evidence, so it
// is reported as absent rather than as whatever the version field decoded to.
// That includes JSON that decodes but carries no version: `{}` or
// `{"status":"ok"}` from some unrelated listener is not a hub with an empty
// version, and reading it as one would invent a restart against a non-hub
// process. The start time is carried when the body provides one; it is what
// distinguishes a genuinely new process from the one a restart replaced.
func parseHubHealth(out []byte) (hubIdentity, bool) {
	var resp hubapi.HealthResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.Version == "" {
		return hubIdentity{}, false
	}
	return hubIdentity{version: resp.Version, startedAt: resp.StartedAt}, true
}

// recoverHubArgv recovers the argv of pid. It prefers the host's null-delimited
// /proc/<pid>/cmdline (Linux), the only form that preserves argument boundaries:
// `ps -o command=` joins argv with single spaces, so a path containing a space
// would be split and a wrong argv relaunched. Where the null-delimited source is
// unavailable (macOS), it falls back to ps and refuses a command line whose word
// boundaries are ambiguous rather than guessing.
func (m *Manager) recoverHubArgv(ctx context.Context, host hostreg.Host, pid string) ([]string, error) {
	if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, hubArgvRemote(pid)), nil); err == nil && len(out) > 0 {
		if argv, ok := splitNullArgv(out); ok {
			if err := validateHubArgv(argv); err != nil {
				return nil, fmt.Errorf("%w: host %q pid %s: %w", ErrRestart, host.Name, pid, err)
			}
			return argv, nil
		}
	}

	// `-o command=` (trailing `=`) suppresses the `COMMAND` header line, so the
	// recovered command line is the process's argv, not the header followed by
	// it. Real GNU/BSD ps emit the header without the trailing `=`, which is what
	// made the old invocation relaunch as `nohup COMMAND`.
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "ps -p "+shellQuote(pid)+" -ww -o command="), nil)
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

// hubArgvRemote builds the remote command that prints pid's argv NUL-separated.
// It prints nothing and exits 0 when the host has no readable
// /proc/<pid>/cmdline, so the caller falls back to ps instead of reading an
// error as an empty argv.
func hubArgvRemote(pid string) string {
	p := shellQuote("/proc/" + pid + "/cmdline")
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
	if base := path.Base(argv[0]); base != "evener" {
		return fmt.Errorf("executable %q is not evener", argv[0])
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
		words[i] = shellQuote(a)
	}
	cmd := strings.Join(words, " ")
	if logPath != "" {
		return "nohup " + cmd + " >>" + shellQuote(logPath) + " 2>&1 </dev/null &"
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
