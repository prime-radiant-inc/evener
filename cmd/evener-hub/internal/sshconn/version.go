package sshconn

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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

const (
	systemctlListUnits     = "systemctl list-units --type=service --all --no-legend --plain"
	systemctlListUnitsUser = "systemctl --user list-units --type=service --all --no-legend --plain"
)

// hubAddr returns the configured hub listen address or the default.
func (o Options) hubAddr() string {
	if a := strings.TrimSpace(o.HubAddr); a != "" {
		return a
	}
	return defaultHubAddr
}

// hubPort extracts the TCP port from a host:port hub address, defaulting to the
// known hub port when addr is unparsable.
func hubPort(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p
	}
	return "9180"
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

// parseLaunchdHubs finds launchd jobs whose labels name an evener hub.
// `launchctl list` prints "PID Status Label" per line; a job with no running
// pid shows "-". The doc's precedent is `launchctl list | grep evener-hub`.
func parseLaunchdHubs(out []byte) []string {
	var labels []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "PID" {
			continue
		}
		if label := fields[len(fields)-1]; isEvenerHubName(label) {
			labels = append(labels, label)
		}
	}
	return labels
}

// parseSystemdHubs finds evener hub units in `systemctl list-units` output.
// Each line starts with the unit name, e.g. "evener-hub.service loaded active".
// A leading status glyph (●) is stripped.
func parseSystemdHubs(out []byte) []string {
	var units []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "●"))
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if !strings.HasSuffix(unit, ".service") {
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

// detectSupervisor runs the host's supervisor listings. A listing failure is
// not fatal: it only means the listing tool is absent, so the hub falls through
// to the bare-process path. An ambiguous listing is fatal, not a fallback.
func (m *Manager) detectSupervisor(ctx context.Context, host hostreg.Host, facts Preflight) (supervisor, error) {
	switch facts.OS {
	case "darwin":
		out, _ := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "launchctl list"), nil)
		return detectSupervisorFrom("darwin", out, nil, nil)
	case "linux":
		sysOut, sysErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, systemctlListUnits), nil)
		if sysErr != nil {
			sysOut = nil
		}
		userOut, userErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, systemctlListUnitsUser), nil)
		if userErr != nil {
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
// relaunching.
func (m *Manager) restartHub(ctx context.Context, host hostreg.Host, facts Preflight) error {
	expected := m.opts.controllerVersion()
	sup, err := m.detectSupervisor(ctx, host, facts)
	if err != nil {
		return err
	}
	if sup.kind != supervisorNone {
		remote := sup.restartRemote()
		out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
		if err != nil {
			// Surface the restart command's own failure even when the hub would
			// answer a health probe: a healthy response alone cannot prove the
			// restart took (the old process stays healthy through a failed
			// `systemctl restart` when the user lacks sudo/polkit), so swallowing
			// this cause is what let a failed restart look like success.
			return fmt.Errorf("%w: host %q %s: %w: %s", ErrRestart, host.Name, remote, err, tail(out))
		}
		return m.waitHealthy(ctx, host, expected)
	}

	if err := m.restartBare(ctx, host); err != nil {
		return err
	}
	return m.waitHealthy(ctx, host, expected)
}

// restartBare implements the doc's four-step ad hoc-hub restart: find the pid
// by listening port, recover the exact argv and log destination, stop it, and
// relaunch detached with the recovered argv.
func (m *Manager) restartBare(ctx context.Context, host hostreg.Host) error {
	port := hubPort(m.opts.hubAddr())
	pid, err := m.findHubPID(ctx, host, port)
	if err != nil {
		return err
	}

	// `-o command=` (trailing `=`) suppresses the `COMMAND` header line, so the
	// recovered command line is the process's argv, not the header followed by
	// it. Real GNU/BSD ps emit the header without the trailing `=`, which is what
	// made the old invocation relaunch as `nohup COMMAND`.
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "ps -p "+shellQuote(pid)+" -ww -o command="), nil)
	if err != nil {
		return fmt.Errorf("%w: host %q ps pid %s: %w: %s", ErrRestart, host.Name, pid, err, tail(out))
	}
	cmdline := stripPSHeader(string(out))
	if cmdline == "" {
		return fmt.Errorf("%w: host %q could not recover argv for pid %s", ErrRestart, host.Name, pid)
	}

	logPath := ""
	if lout, lerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+shellQuote(pid)+" -a -d 1,2"), nil); lerr == nil {
		logPath, _ = parseLogPath(lout)
	}

	// Stop the old hub and wait for its port to clear before starting a new
	// one; relaunching into the gap races hub.lock (docs/evener-hub-remote-
	// operations.md:363-377).
	kout, kerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "kill "+shellQuote(pid)), nil)
	if kerr != nil {
		return fmt.Errorf("%w: host %q could not stop hub pid %s: %w: %s", ErrRestart, host.Name, pid, kerr, tail(kout))
	}
	if err := m.waitPortClear(ctx, host, port); err != nil {
		return err
	}

	relaunch := relaunchCommand(cmdline, logPath)
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
		clear, err := m.portCleared(ctx, host, port)
		if err != nil {
			return err
		}
		if clear {
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

// waitHealthy polls the host hub's /api/health until the response reports
// expectedVersion or the bound is exhausted. The expected version is what makes
// this the authoritative restart check: any hub answer proves a hub is serving,
// but only the expected version proves the *deployed* build is the one running.
// Accepting a bare healthy response would mask a failed restart (the old hub
// still answering) and could attach to the dying old process during its
// shutdown drain, tearing down the fresh channel.
func (m *Manager) waitHealthy(ctx context.Context, host hostreg.Host, expectedVersion string) error {
	port := hubPort(m.opts.hubAddr())
	remote := "curl -fsS localhost:" + port + "/api/health"
	var lastVersion string
	for range restartHealthAttempts {
		if out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil); err == nil {
			if got, ok := parseHealthVersion(out); ok {
				if got == expectedVersion {
					return nil
				}
				lastVersion = got
			}
		}
		if err := m.opts.waitSleep(ctx, restartHealthInterval); err != nil {
			return err
		}
	}
	if lastVersion != "" {
		return fmt.Errorf("%w: host %q hub on :%s reports version %q, want %q after restart", ErrRestart, host.Name, port, lastVersion, expectedVersion)
	}
	return fmt.Errorf("%w: host %q hub not healthy on :%s after restart", ErrRestart, host.Name, port)
}

// parseHealthVersion reads the running version from a /api/health body. A body
// that is not the hub's HealthResponse JSON is not usable evidence, so it is
// reported as absent rather than as whatever the version field decoded to.
func parseHealthVersion(out []byte) (string, bool) {
	var resp hubapi.HealthResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", false
	}
	return resp.Version, true
}

// relaunchCommand builds the detached remote relaunch. It appends to the
// recovered log so the hub's history is preserved (docs/evener-hub-remote-
// operations.md:388-394); with no recovered log the output is discarded.
//
// The recovered command line is passed to `sh -c` as a single quoted word: the
// text came back from `ps` as a shell command line, so it must be re-parsed as
// one, but it is host-derived and must not be allowed to inject into the outer
// shell that starts the relaunch. The log path is quoted for the same reason.
func relaunchCommand(cmdline, logPath string) string {
	if logPath != "" {
		return "nohup sh -c " + shellQuote(cmdline) + " >>" + shellQuote(logPath) + " 2>&1 </dev/null &"
	}
	return "nohup sh -c " + shellQuote(cmdline) + " </dev/null >/dev/null 2>&1 &"
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
		name := fields[len(fields)-1]
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
