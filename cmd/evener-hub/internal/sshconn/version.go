package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
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

// hostAddr returns the listen address of this host's hub: the per-host registry
// Addr when set, else the manager-wide Options.HubAddr, else the default.
// channelArgv passes host.Addr as the bridge's --addr, so the restart and health
// probes must read the same value to address the host's actual hub rather than
// the controller's default port (a non-default port would otherwise kill the
// wrong process or poll the wrong service).
func (m *Manager) hostAddr(host hostreg.Host) string {
	if a := strings.TrimSpace(host.Addr); a != "" {
		return a
	}
	return m.opts.hubAddr()
}

// hubPort extracts the TCP port from a host:port hub address, defaulting to the
// known hub port when addr is unparsable.
func hubPort(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
		return p
	}
	return "9180"
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
// still holds hub.lock.
func parseSystemdHubs(out []byte) []string {
	var units []string
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "●"))
		if len(fields) < 3 {
			continue
		}
		unit := fields[0]
		if !strings.HasSuffix(unit, ".service") {
			continue
		}
		if fields[1] != "loaded" || fields[2] != "active" {
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
		out, runErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
		if runErr != nil && !sup.restartStatusIsAdvisory() {
			// Surface the restart command's own failure even when the hub would
			// answer a health probe: a healthy response alone cannot prove the
			// restart took (the old process stays healthy through a failed
			// `systemctl restart` when the user lacks sudo/polkit), so swallowing
			// this cause is what let a failed restart look like success.
			return fmt.Errorf("%w: host %q %s: %w: %s", ErrRestart, host.Name, remote, runErr, tail(out))
		}
		if err := m.waitHealthy(ctx, host, expected); err != nil {
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
		return nil
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
	port := hubPort(m.hostAddr(host))
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
	// Validate before killing: the process merely holds the hub's port, which a
	// port collision could make an unrelated service. Only a tokenizable
	// `evener hub` invocation whose --addr agrees with the probed port is
	// restarted; anything else is refused rather than guessed at.
	argv, err := hubArgvFromCommandLine(cmdline)
	if err != nil {
		return fmt.Errorf("%w: host %q pid %s: %w (command line %q)", ErrRestart, host.Name, pid, err, cmdline)
	}
	if a, ok := hubAddrFlag(argv); ok && hubPort(a) != port {
		return fmt.Errorf("%w: host %q pid %s listens on :%s but was launched with --addr %q; refusing to restart it",
			ErrRestart, host.Name, pid, port, a)
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

	relaunch := relaunchCommand(argv, logPath)
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
func hubHealthRemote(addr string) string {
	return "curl -fsS " + shellQuote(loopbackAddr(addr)+"/api/health")
}

// probeHubVersion asks the host hub's /api/health once and returns the version
// it reports. ok is false when nothing answered or the body was not a health
// response, which is "no running hub to judge" rather than an error: ensureOnce
// must not invent a restart from an unanswerable probe.
func (m *Manager) probeHubVersion(ctx context.Context, host hostreg.Host) (string, bool) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, hubHealthRemote(m.hostAddr(host))), nil)
	if err != nil {
		return "", false
	}
	return parseHealthVersion(out)
}

// waitHealthy polls the host hub's /api/health until the response reports
// expectedVersion or the bound is exhausted. The expected version is what makes
// this the authoritative restart check: any hub answer proves a hub is serving,
// but only the expected version proves the *deployed* build is the one running.
// Accepting a bare healthy response would mask a failed restart (the old hub
// still answering) and could attach to the dying old process during its
// shutdown drain, tearing down the fresh channel.
func (m *Manager) waitHealthy(ctx context.Context, host hostreg.Host, expectedVersion string) error {
	port := hubPort(m.hostAddr(host))
	remote := hubHealthRemote(m.hostAddr(host))
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

// hubArgvFromCommandLine recovers the argv of the process whose command line ps
// reported, refusing anything that is not a plain `evener hub` invocation. It is
// what keeps restartBare from killing and relaunching a process merely because it
// holds the hub's port.
func hubArgvFromCommandLine(line string) ([]string, error) {
	argv, err := tokenizeCommandLine(line)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("empty command line")
	}
	if base := path.Base(argv[0]); base != "evener" {
		return nil, fmt.Errorf("executable %q is not evener", argv[0])
	}
	// The hub subcommand must sit at the actual subcommand position. Matching
	// "hub" anywhere in argv accepts an unrelated command that merely names it
	// (e.g. `evener serve --model hub`) and would kill and relaunch that
	// process. The daemon is `evener hub` and takes only flags after the
	// subcommand, so a later positional (`evener hub attach`, the client) is
	// not the daemon either.
	if len(argv) < 2 || argv[1] != "hub" {
		return nil, fmt.Errorf("argv does not run the hub subcommand at position 1: %q", argv)
	}
	if len(argv) > 2 && !strings.HasPrefix(argv[2], "-") {
		return nil, fmt.Errorf("unexpected positional %q after the hub subcommand", argv[2])
	}
	return argv, nil
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
