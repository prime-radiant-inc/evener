package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/execsupport/shellquote"
)

// requiredLaunchFlag is the serve flag every host binary must advertise before
// the controller will use it, mirroring the local gate's requiredLaunchFlag
// (cmd/evener-hub/spawn.go:770) and launchcheck.supportedLaunchFlags
// (cmd/evener/internal/launchcheck/launchcheck.go:40).
const requiredLaunchFlag = "api-log"

// Preflight is what one non-interactive probe learns about a host. It records
// the resolved roots verbatim from the host environment (never inventing XDG
// values the host does not have).
//
// ConfigRoot and StateRoot are the host's *default* evener roots: the same
// HOME/XDG chain the host binary uses when it has no config override. A host
// hub.toml that sets hub_state_root (or a config root) moves the running hub to
// a different directory, and `evener hub attach` — which reads its own config —
// follows it there, so these fields are not where that hub's token and lock
// necessarily live.
type Preflight struct {
	Host         string
	OS           string // GOOS
	Arch         string // GOARCH
	UnameOS      string // raw `uname -s`
	UnameMachine string // raw `uname -m`
	Home         string
	// ConfigRoot is the default evener config root (~/.config/evener or
	// XDG_CONFIG_HOME/evener), not a hub.toml override.
	ConfigRoot string
	// StateRoot is the default evener state root (~/.local/state/evener or
	// XDG_STATE_HOME/evener), not a hub.toml hub_state_root override.
	StateRoot string
	// UID is the host's numeric effective uid (`id -u`), or empty when the host
	// did not report a numeric one. The supervised darwin restart interpolates it
	// into `gui/<uid>/<label>`; it is expired on the controller, never left for
	// the remote shell to expand as `$(id -u)` (spec 04, §"Preflight + version
	// contract" and §"Stop/restart mechanics").
	UID               string
	Version           string
	Protocol          string
	LaunchFlags       []string
	HubEndpointSchema int
	// LaunchCheckKnown is true when the on-disk binary answered
	// `launch-check --json` with a decoded contract. A protocol refusal alone
	// leaves it false: Protocol, Version, and LaunchFlags remain unknown until
	// the executable successfully describes its own contract.
	LaunchCheckKnown bool
	// ExecutableMissing records the verified result of the dedicated executable
	// probe (file presence for a path, or `command -v` for a bare name):
	// there is no evener file at the host's resolved run target. It is
	// surfaced as the ErrExecutableMissing sentinel where an error is needed;
	// when a deploy path is configured, `Ensure` routes it into the
	// deploy/install ladder, which creates the missing run target.
	ExecutableMissing bool
}

// osArchTargetsSupported reports whether a build ships for this target. Only
// linux/amd64 and darwin/arm64 do (.goreleaser.yml; install.sh:39-45).
func targetSupported(goos, goarch string) bool {
	switch goos + "/" + goarch {
	case "linux/amd64", "darwin/arm64":
		return true
	default:
		return false
	}
}

// mapOS maps `uname -s` to GOOS exactly as install.sh:21-30 does.
func mapOS(unameS string) (string, bool) {
	switch strings.TrimSpace(unameS) {
	case "Linux":
		return "linux", true
	case "Darwin":
		return "darwin", true
	default:
		return "", false
	}
}

// mapArch maps `uname -m` to GOARCH exactly as install.sh:32-38 does.
func mapArch(unameM string) (string, bool) {
	switch strings.TrimSpace(unameM) {
	case "x86_64", "amd64":
		return "amd64", true
	case "arm64", "aarch64":
		return "arm64", true
	default:
		return "", false
	}
}

// envProbeScript prints the host's HOME and XDG bases. Non-interactive ssh runs
// a shell but exports no XDG_* (spike finding), so the `-` default leaves both
// empty and the roots fall back to ~/.config/evener and ~/.local/state/evener.
// It is one ssh argument so the remote shell performs the parameter expansion.
const envProbeScript = `case "${HOME-}${XDG_STATE_HOME-}${XDG_CONFIG_HOME-}" in *'
'*) exit 1 ;; esac; printf '%s\n' "HOME=${HOME-}" "XDG_STATE_HOME=${XDG_STATE_HOME-}" "XDG_CONFIG_HOME=${XDG_CONFIG_HOME-}"`

// parseEnvProbe turns the probe's KEY=value lines into a map. HOME must be
// present (it may be empty, which resolves to the same fallback evener uses).
func parseEnvProbe(out []byte) (map[string]string, error) {
	env := map[string]string{}
	text := strings.TrimSuffix(string(out), "\n")
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%w: unparsable environment line %q", ErrPreflightDecode, line)
		}
		env[key] = value
	}
	if _, ok := env["HOME"]; !ok {
		return nil, fmt.Errorf("%w: environment probe did not report HOME", ErrPreflightDecode)
	}
	return env, nil
}

// parseEffectiveUID reads the host's numeric effective uid from `id -u` output.
// A non-numeric or empty answer is recorded as absent rather than guessed: the
// value is interpolated into launchd's bare-safe `gui/<uid>/<label>` word, so a
// host that does not report a plain number falls through to the ad hoc restart
// path instead of risking a wrong or injected command.
func parseEffectiveUID(out []byte) string {
	s := strings.TrimSpace(string(out))
	if !isNumericUID(s) {
		return ""
	}
	return s
}

// resolveRoots resolves the host's evener config and state roots from the
// probed environment using the same chain the host binary itself uses
// (cmdutil.StateRootFromLookup, envvars/userdirs.ConfigRoot). Empty variables
// are treated as unset, matching evener's own env lookups. A root the host
// cannot resolve (no HOME and no XDG base) is reported as "" rather than a
// guessed path, the way userdirs.ConfigRoot reports an unavailable root.
func resolveRoots(goos string, env map[string]string) (configRoot, stateRoot string) {
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok && value != ""
	}
	stateRoot, _ = cmdutil.StateRootFromLookup(goos, lookup)
	home := env["HOME"]
	configRoot = userdirs.ConfigRoot(env["XDG_CONFIG_HOME"], func() (string, error) {
		if home != "" {
			return home, nil
		}
		return "", errors.New("HOME unset")
	})
	return configRoot, stateRoot
}

// launchCheck is the subset of `evener launch-check --json` this component
// consumes (cmd/evener/internal/launchcheck/launchcheck.go:42-54).
type launchCheck struct {
	Protocol          string   `json:"protocol"`
	Version           string   `json:"version"`
	LaunchFlags       []string `json:"launch_flags"`
	HubEndpointSchema int      `json:"hub_endpoint_schema"`
}

// parseLaunchCheck decodes the host's launch-check contract. Any decode failure
// is ErrPreflightDecode: the host cannot be trusted to launch. A contract that
// names a protocol but no version is refused here too — version auto-match keys
// off it, so accepting an empty version would let the manager attach while the
// match stayed unverifiable.
func parseLaunchCheck(out []byte) (launchCheck, error) {
	var lc launchCheck
	if err := json.Unmarshal(out, &lc); err != nil {
		return launchCheck{}, fmt.Errorf("%w: %w", ErrPreflightDecode, err)
	}
	if strings.TrimSpace(lc.Protocol) == "" {
		return launchCheck{}, fmt.Errorf("%w: launch-check reported no protocol", ErrPreflightDecode)
	}
	if strings.TrimSpace(lc.Version) == "" {
		// The launch contract always reports a build version (buildinfo.Version(),
		// or "dev"). An absent one is a broken contract, not a version that merely
		// differs: reading it as a mismatch would drive a deploy against a host
		// whose identity was never established.
		return launchCheck{}, fmt.Errorf("%w: launch-check reported no version", ErrPreflightDecode)
	}
	return lc, nil
}

// preflight preserves the explicit deployment planner's launch-contract gate.
func (m *Manager) preflight(ctx context.Context, host hostreg.Host) (Preflight, error) {
	result, err := m.collectPreflight(ctx, host)
	if err != nil {
		return result.facts, err
	}
	if result.launchErr != nil && !m.canDeploy() {
		return result.facts, result.launchErr
	}
	if !result.facts.ExecutableMissing {
		if err := endpointCapability(host, result.facts); err != nil {
			return result.facts, err
		}
	}
	return result.facts, nil
}

type preflightResult struct {
	facts     Preflight
	launchErr error
}

// collectPreflight separates host/environment failure from an unusable disk
// launch contract. A running hub's initialize, not its disk executable, decides
// whether an existing connection is usable.
func (m *Manager) collectPreflight(ctx context.Context, host hostreg.Host) (preflightResult, error) {
	pf := Preflight{Host: host.Name}

	unameS, err := m.runRemote(ctx, host, "uname -s")
	if err != nil {
		return preflightResult{facts: pf}, err
	}
	pf.UnameOS = strings.TrimSpace(string(unameS))
	goos, ok := mapOS(pf.UnameOS)
	if !ok {
		return preflightResult{facts: pf}, fmt.Errorf("%w: host %q uname -s %q", ErrUnsupportedHost, host.Name, pf.UnameOS)
	}
	pf.OS = goos

	unameM, err := m.runRemote(ctx, host, "uname -m")
	if err != nil {
		return preflightResult{facts: pf}, err
	}
	pf.UnameMachine = strings.TrimSpace(string(unameM))
	goarch, ok := mapArch(pf.UnameMachine)
	if !ok {
		return preflightResult{facts: pf}, fmt.Errorf("%w: host %q uname -m %q", ErrUnsupportedHost, host.Name, pf.UnameMachine)
	}
	pf.Arch = goarch

	if !targetSupported(goos, goarch) {
		return preflightResult{facts: pf}, fmt.Errorf("%w: host %q has no build for %s/%s", ErrUnsupportedHost, host.Name, goos, goarch)
	}

	envOut, err := m.runRemote(ctx, host, envProbeScript)
	if err != nil {
		return preflightResult{facts: pf}, err
	}
	env, err := parseEnvProbe(envOut)
	if err != nil {
		return preflightResult{facts: pf}, err
	}
	pf.Home = env["HOME"]
	pf.ConfigRoot, pf.StateRoot = resolveRoots(goos, env)

	uidOut, err := m.runRemote(ctx, host, "id -u")
	if err != nil {
		return preflightResult{facts: pf}, err
	}
	pf.UID = parseEffectiveUID(uidOut)

	probeHost := host
	m.applyResolvedTarget(&probeHost)
	lc, err := m.probeLaunchCheck(ctx, probeHost)
	// An unfinished effect owns its selected installation. Its absence must not
	// redirect recovery into a second installation discovered at the default.
	if errors.Is(err, ErrExecutableMissing) && !m.pendingRestart(host.Name).active() {
		target, probeErr := m.installerDefaultForPreflight(ctx, host)
		if probeErr != nil {
			return preflightResult{facts: pf}, probeErr
		}
		if target != "" {
			// Deployment may have claimed a target while discovery was in flight.
			// Probe the selected installation, not an unowned discovery's facts.
			probeHost.EvenerPath = m.publishDiscoveredTarget(host, target)
			lc, err = m.probeLaunchCheck(ctx, probeHost)
		}
	}
	if pending := m.pendingRestart(host.Name); pending.deployment && pending.selected != nil && errors.Is(err, errLaunchCheckFailed) {
		// Only the retained installation can use nonexecutability as recorded
		// repair evidence. An ordinary present-but-unusable binary is not missing.
		if unusable, probeErr := m.recordedTargetUnusable(ctx, probeHost); probeErr == nil && unusable {
			err = fmt.Errorf("%w: retained target is nonexecutable", ErrExecutableMissing)
		}
	}
	if errors.Is(err, ErrProtocolIncompatible) {
		// The CLI rejects an imposed protocol before emitting JSON. Ask the same
		// executable for its own contract: refusal prose alone cannot authorize
		// replacing a stopped installation or reconcile an interrupted write.
		described, out, describeErr := m.describeLaunchContract(ctx, probeHost)
		switch {
		case describeErr == nil:
			lc, err = described, nil
		case errors.Is(describeErr, ErrPreflightDecode):
			// Keep a malformed contract's own classification; it is not an SSH
			// failure and the caller handles it as an unusable launch contract.
			lc, err = described, describeErr
		default:
			// Unreadable disk metadata must not prevent a compatible live attach.
			return preflightResult{facts: pf, launchErr: sshRunFailure(host.Name, "launch-check", describeErr, tail(out))}, nil
		}
	}
	if err != nil {
		pf.ExecutableMissing = errors.Is(err, ErrExecutableMissing)
		if pf.ExecutableMissing {
			err = fmt.Errorf("%w: %s", err, m.deployHelp())
		}
		if pf.ExecutableMissing || errors.Is(err, ErrProtocolIncompatible) || errors.Is(err, ErrPreflightDecode) || errors.Is(err, errLaunchCheckFailed) {
			return preflightResult{facts: pf, launchErr: err}, nil
		}
		return preflightResult{facts: pf}, err
	}
	pf.LaunchCheckKnown = true
	pf.Protocol = lc.Protocol
	pf.Version = lc.Version
	pf.LaunchFlags = append([]string(nil), lc.LaunchFlags...)
	pf.HubEndpointSchema = lc.HubEndpointSchema
	return preflightResult{facts: pf}, nil
}

// probeLaunchCheck requires the controller's protocol when reading the launch
// contract. Post-deploy probes keep this strict gate; only collectPreflight may
// follow a protocol refusal with a read of the executable's own contract.
func (m *Manager) probeLaunchCheck(ctx context.Context, host hostreg.Host) (launchCheck, error) {
	return m.readLaunchCheck(ctx, host, false)
}

// probeStartupLaunchCheck judges the executable a management action will start.
// Capability discovery uses the unfiltered contract, including older protocols.
func (m *Manager) probeStartupLaunchCheck(ctx context.Context, host hostreg.Host) (launchCheck, error) {
	return m.readLaunchCheck(ctx, host, true)
}

func (m *Manager) readLaunchCheck(ctx context.Context, host hostreg.Host, startup bool) (launchCheck, error) {
	if err := ctx.Err(); err != nil {
		return launchCheck{}, err
	}
	args := []string{"launch-check", "--json"}
	if startup {
		args = []string{"launch-check", "--protocol", appwire.ProtocolVersion, "--json"}
	}
	out, err := m.runner.Run(ctx, evenerCommandArgv(m.opts, host, args...), nil)
	if ctx.Err() != nil {
		return launchCheck{}, ctx.Err()
	}
	if err != nil {
		if startup && isProtocolMismatchOutput(out) {
			return launchCheck{}, fmt.Errorf("%w: host %q refused protocol %s: %s", ErrProtocolIncompatible, host.Name, appwire.ProtocolVersion, tail(out))
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 255 {
			return launchCheck{}, sshRunFailure(host.Name, "launch-check", err, tail(out))
		}
		missing, present := m.probeExecutablePresence(ctx, host)
		if missing {
			return launchCheck{}, fmt.Errorf("%w: host %q launch-check: %s", ErrExecutableMissing, host.Name, tail(out))
		}
		failure := sshRunFailure(host.Name, "launch-check", err, tail(out))
		if present && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
			errors.As(err, &exit) && exit.ExitCode() > 0 && exit.ExitCode() != 255 {
			// SSH reserves 255 for its own failures. A different completed exit,
			// plus a successful presence probe, is an unusable disk contract, not
			// evidence against a live compatible hub or permission to install.
			return launchCheck{}, fmt.Errorf("%w: %w: %w", endpointMigration(host), errLaunchCheckFailed, failure)
		}
		return launchCheck{}, failure
	}
	lc, err := parseLaunchCheck(out)
	if err != nil {
		return launchCheck{}, fmt.Errorf("%w: %w", endpointMigration(host), err)
	}
	if startup && lc.Protocol != appwire.ProtocolVersion {
		return launchCheck{}, fmt.Errorf("%w: host %q protocol %q, want %q", ErrProtocolIncompatible, host.Name, lc.Protocol, appwire.ProtocolVersion)
	}
	return lc, nil
}

// describeLaunchContract asks the executable for its own launch contract
// without imposing this controller's protocol. It is the readable description a
// protocol refusal cannot provide: the strict probe refuses before emitting
// JSON, so a caller that must still identify the installed protocol reads it
// here.
func (m *Manager) describeLaunchContract(ctx context.Context, host hostreg.Host) (launchCheck, []byte, error) {
	out, err := m.runner.Run(ctx, evenerCommandArgv(m.opts, host, "launch-check", "--json"), nil)
	if err != nil {
		return launchCheck{}, out, err
	}
	lc, err := parseLaunchCheck(out)
	return lc, out, err
}

var errLaunchCheckFailed = errors.New("sshconn: executable launch-check command failed")

func isProtocolMismatchOutput(out []byte) bool {
	text := string(out)
	return strings.Contains(text, "unsupported appwire protocol") ||
		strings.Contains(text, "does not match Hub protocol")
}

// ErrExecutableMissing marks the verified absence of the evener executable at the
// host's resolved run target: the dedicated executable probe proved it (its own
// exit 1), rather than the failed launch-check's status or its not-found text
// being parsed. It is a fact about the executable's absence, not a transport or
// permission refusal, so a controller with a deploy configured records the launch
// contract as unknown and lets the installer fallback install a binary at a known
// location, instead of refusing the host outright.
var ErrExecutableMissing = errors.New("sshconn: evener executable not found on the host")

// probeExecutablePresence reports independently verified absence or presence at
// the command launch-check just tried. Neither is true for a failed probe.
//
// It reads the probe's exit status alone and never its output: ssh writes its
// own diagnostics and the remote command's stderr to one merged stream, so the
// text is not separable evidence, and a failed evener invocation's 127 is not
// either (the remote shell's not-found status is exactly what must not be
// parsed). The probe normalizes its answer to 0 (present) or 1 (absent); only
// its own 1 is the verified absent result. Every other failure — ssh's 255, a
// spawn failure, any other status — is not a verification, so the caller keeps
// the failed launch-check's own retryable classification rather than reading a
// transport failure as a missing executable (spec 04, §"Preflight", "Missing
// executable"; §"Error handling", "Arbitrary SSH failures must not trigger
// installation").
func (m *Manager) probeExecutablePresence(ctx context.Context, host hostreg.Host) (missing, present bool) {
	_, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, executableProbeRemote(evenerCommand(host.EvenerPath))), nil)
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1, err == nil
}

// executableProbeRemote builds the dedicated executable-presence probe for the
// command launch-check would run: exit 0 when target exists and is executable
// there, exit 1 when it is not. A path target is probed with `test -x`, the
// spec's form; a bare name is probed with `command -v`, the PATH equivalent,
// because `test -x evener` would test the login shell's cwd rather than the
// PATH the bare word resolves through. The if/exit wrapper normalizes both forms
// to the spec's 0/1 sentinel: `command -v` answers 127 — not 1 — for a name it
// cannot find (dash and other shells), so without the wrapper the absent answer
// would depend on the shell generation, and a status that double-duties as the
// shell's own not-found code could not be told from the probe's.
func executableProbeRemote(target string) string {
	word := shellquote.RemoteWord(target)
	if !strings.Contains(target, "/") {
		return "if command -v " + word + " >/dev/null 2>&1; then exit 0; else exit 1; fi"
	}
	return "if test -e " + word + " || test -L " + word + "; then exit 0; fi; p=" + word + `; while :; do
case "$p" in */*) p=${p%/*}; [ -n "$p" ] || p=/ ;; *) p=. ;; esac
if test -e "$p" || test -L "$p"; then
  if test -d "$p" && test -x "$p"; then exit 1; else exit 2; fi
fi
done`
}

// runRemote runs a non-evener remote command, classifying a failure as an auth
// refusal or a transport failure.
func (m *Manager) runRemote(ctx context.Context, host hostreg.Host, remote string) ([]byte, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		return out, sshRunFailure(host.Name, fmt.Sprintf("command %q", remote), err, tail(out))
	}
	return out, nil
}

// authFailureMarkers are ssh stderr phrases that mean the host cannot
// authenticate non-interactively. Despite the name, a match is deliberately
// NOT terminal: isTerminal excludes ErrSSHAuth, so the reconnect loop keeps
// retrying. ssh forwards a remote command's stderr and exit status on this same
// stream, so the refusal cannot be attributed to ssh, and retrying is the safe
// side of that ambiguity (see sshRunFailure and ErrSSHAuth).
//
// Every marker is a shape ssh itself emits, because a remote command's stderr
// reaches the controller on that same stream: a bare "Permission denied" is what
// a host prints when it refuses to execute the binary we asked for, and reading
// that as an authentication refusal would end the reconnect loop for good. ssh
// always names the methods it tried in parentheses — "Permission denied
// (publickey,password,keyboard-interactive)." — and that parenthetical is what
// makes the spelling ssh's own.
var authFailureMarkers = []string{
	"Permission denied (",
	"Host key verification failed",
	"no mutual signature algorithm",
	"Too many authentication failures",
}

func isAuthFailure(stderr string) bool {
	for _, marker := range authFailureMarkers {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

// sshRunFailure classifies a failed one-shot ssh invocation. An
// authentication-shaped failure that no remote command could have produced (a
// failed Start) is reported as ErrSSHAuth; everything else is the transport
// class ErrSSHStart. Neither is terminal: ssh forwards the remote command's
// stderr and exit status, so a refusal cannot be proven to be ssh's own, and
// retrying is the safe side of that ambiguity (see ErrSSHAuth). diag is carried
// either way, because it names the cause.
func sshRunFailure(hostName, what string, err error, diag string) error {
	if isSSHAuthFailure(err, diag) {
		return fmt.Errorf("%w: host %q %s: %w: %s", ErrSSHAuth, hostName, what, err, diag)
	}
	return fmt.Errorf("%w: host %q %s: %w: %s", ErrSSHStart, hostName, what, err, diag)
}

// isSSHAuthFailure decides whether a failure can be attributed to ssh's own
// non-interactive authentication refusal. It is the ONE rule the attach path
// shares (manager.attach): an authentication marker is attributable to ssh only
// when no remote command could have produced it.
//
// A failed one-shot Run cannot prove that. ssh forwards the remote command's own
// stderr onto the same stream as its diagnostics, and it forwards the remote
// command's exit status unchanged — including 255, the single status ssh(1) uses
// for its own failures. With both the text and the status reachable by the remote
// command, reading status 255 as an ssh refusal would let a remote program that
// exits 255 with an ssh-shaped error stop the reconnect loop for good. The safe
// side of that trade is to keep the ambiguous failure retryable.
//
// A failed Start is different: ssh never spawned, so no remote command ran and
// the marker on the diagnostic stream is necessarily ssh's own. The caller
// classifies that case from the marker alone — and, because a real spawn failure
// produces no diagnostic at all, an unauthenticable host ordinarily lands in the
// retryable ErrSSHStart class instead. ErrSSHAuth is not terminal either way.
func isSSHAuthFailure(err error, diag string) bool {
	if !isAuthFailure(sshDiagnostic(err, diag)) {
		return false
	}
	// A completed Run carries a status the remote command can forge along with the
	// text, so it stays retryable; only a failure that never ran a remote command
	// (no *RunError) is unambiguous.
	if _, completed := errors.AsType[*RunError](err); completed {
		return false
	}
	return true
}

// sshDiagnostic narrows a failure to what ssh itself reported. The remote
// command's own stdout can contain the same words — "Permission denied" from a
// binary the host will not execute — and reading that as an authentication
// refusal would end the reconnect loop permanently for a problem that is not
// authentication. A runner that reports no separated stream falls back to the
// combined diagnostic.
func sshDiagnostic(err error, combined string) string {
	if rf, ok := errors.AsType[*RunError](err); ok {
		return string(rf.Stderr)
	}
	return combined
}

func (m *Manager) installerDefaultForPreflight(ctx context.Context, host hostreg.Host) (string, error) {
	const remote = `if [ -n "${HOME-}" ] && [ -f "${HOME-}/.local/bin/evener" ]; then printf '%s\n' "${HOME-}/.local/bin/evener"; fi`
	out, err := m.runRemote(ctx, host, remote)
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", nil
	}
	return ownerPathOutput(out)
}
