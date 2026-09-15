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
		return "launchctl kickstart -k gui/$(id -u)/" + s.label
	case supervisorSystemd:
		return "systemctl restart " + s.label
	case supervisorSystemdUser:
		return "systemctl --user restart " + s.label
	default:
		return ""
	}
}

// detectSupervisorFrom classifies a host from raw supervisor listings. It is
// pure so the table test needs no ssh: launchd for darwin, systemd (system,
// then --user) for linux, otherwise a bare process.
func detectSupervisorFrom(goos string, launchctlOut, systemdOut, systemdUserOut []byte) supervisor {
	switch goos {
	case "darwin":
		if label, ok := parseLaunchdHub(launchctlOut); ok {
			return supervisor{kind: supervisorLaunchd, label: label}
		}
	case "linux":
		if unit, ok := parseSystemdHub(systemdOut); ok {
			return supervisor{kind: supervisorSystemd, label: unit}
		}
		if unit, ok := parseSystemdHub(systemdUserOut); ok {
			return supervisor{kind: supervisorSystemdUser, label: unit}
		}
	}
	return supervisor{}
}

// parseLaunchdHub finds a launchd job whose label names an evener hub.
// `launchctl list` prints "PID Status Label" per line; a job with no running
// pid shows "-". The doc's precedent is `launchctl list | grep evener-hub`.
func parseLaunchdHub(out []byte) (string, bool) {
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "PID" {
			continue
		}
		label := fields[len(fields)-1]
		if isEvenerHubName(label) {
			return label, true
		}
	}
	return "", false
}

// parseSystemdHub finds an evener hub unit in `systemctl list-units` output.
// Each line starts with the unit name, e.g. "evener-hub.service loaded active".
// A leading status glyph (●) is stripped.
func parseSystemdHub(out []byte) (string, bool) {
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
			return unit, true
		}
	}
	return "", false
}

// isEvenerHubName reports whether a label or unit base name names an evener hub.
func isEvenerHubName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "evener") && strings.Contains(lower, "hub")
}

// detectSupervisor runs the host's supervisor listings. A listing failure is
// not fatal: it only means the listing tool is absent, so the hub falls through
// to the bare-process path.
func (m *Manager) detectSupervisor(ctx context.Context, host hostreg.Host, facts Preflight) supervisor {
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
		return supervisor{}
	}
}

// restartHub restarts the host's hub after a deploy. It prefers a detected
// supervisor (launchd/systemd) and otherwise restarts the bare process by
// recovering its pid, argv, and log. It never starts a second hub while the old
// one holds hub.lock: the bare path waits for the port to clear before
// relaunching.
func (m *Manager) restartHub(ctx context.Context, host hostreg.Host, facts Preflight) error {
	expected := m.opts.controllerVersion()
	sup := m.detectSupervisor(ctx, host, facts)
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

	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "ps -p "+pid+" -ww -o command"), nil)
	if err != nil {
		return fmt.Errorf("%w: host %q ps pid %s: %w: %s", ErrRestart, host.Name, pid, err, tail(out))
	}
	cmdline := strings.TrimSpace(string(out))
	if cmdline == "" {
		return fmt.Errorf("%w: host %q could not recover argv for pid %s", ErrRestart, host.Name, pid)
	}

	logPath := ""
	if lout, lerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -p "+pid+" -a -d 1,2"), nil); lerr == nil {
		logPath, _ = parseLogPath(lout)
	}

	// Stop the old hub and wait for its port to clear before starting a new
	// one; relaunching into the gap races hub.lock (docs/evener-hub-remote-
	// operations.md:363-377).
	kout, kerr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "kill "+pid), nil)
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

// findHubPID returns the pid listening on the hub's port, using lsof because
// pgrep -x evener also matches `evener serve` daemons.
func (m *Manager) findHubPID(ctx context.Context, host hostreg.Host, port string) (string, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -ti :"+port+" -sTCP:LISTEN"), nil)
	if err != nil {
		return "", fmt.Errorf("%w: host %q no hub listening on :%s to restart: %w: %s", ErrRestart, host.Name, port, err, tail(out))
	}
	pid := firstLine(string(out))
	if pid == "" {
		return "", fmt.Errorf("%w: host %q no hub listening on :%s to restart", ErrRestart, host.Name, port)
	}
	return pid, nil
}

// waitPortClear blocks until nothing is listening on the hub port or the bound
// is exhausted. lsof exits nonzero when it finds no listener, which is the
// success condition here.
func (m *Manager) waitPortClear(ctx context.Context, host hostreg.Host, port string) error {
	for range restartStopAttempts {
		if m.portCleared(ctx, host, port) {
			return nil
		}
		if err := m.opts.waitSleep(ctx, restartStopInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: host %q hub port :%s still held after kill", ErrRestart, host.Name, port)
}

// portCleared reports whether the host has no listener on the hub port. lsof
// exits nonzero when it finds none, so its error is this wait's success signal
// rather than a failure to report; a held port is an empty listing with a nil
// error. Keeping that judgement here (a boolean) rather than in a bare
// `if err != nil { return nil }` is what keeps the intent legible.
func (m *Manager) portCleared(ctx context.Context, host hostreg.Host, port string) bool {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, "lsof -ti :"+port+" -sTCP:LISTEN"), nil)
	return err != nil || strings.TrimSpace(string(out)) == ""
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
func relaunchCommand(cmdline, logPath string) string {
	if logPath != "" {
		return "nohup " + cmdline + " >>" + logPath + " 2>&1 </dev/null &"
	}
	return "nohup " + cmdline + " </dev/null >/dev/null 2>&1 &"
}

// parseLogPath extracts the regular-file destination shared by fd 1 and fd 2
// from `lsof -p <pid> -a -d 1,2` output. A tty destination (nothing redirected
// at launch) yields no path.
func parseLogPath(out []byte) (string, bool) {
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
		if strings.HasPrefix(name, "/") {
			return name, true
		}
	}
	return "", false
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
