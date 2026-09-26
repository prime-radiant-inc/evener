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
	UID         string
	Version     string
	Protocol    string
	LaunchFlags []string
	// LaunchCheckKnown is true when the on-disk binary answered
	// `launch-check --json` with a contract. It is false when the binary refused
	// the controller's appwire protocol outright, in which case Protocol,
	// Version, and LaunchFlags carry no information and must be read as unknown.
	LaunchCheckKnown bool
	// ExecutableMissing records the verified result of the dedicated executable
	// probe (`test -x <run_path>`, or `command -v <run_path>` for a bare name):
	// there is no evener executable at the host's resolved run target. It is
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
const envProbeScript = `printf '%s\n' "HOME=${HOME-}" "XDG_STATE_HOME=${XDG_STATE_HOME-}" "XDG_CONFIG_HOME=${XDG_CONFIG_HOME-}"`

// parseEnvProbe turns the probe's KEY=value lines into a map. HOME must be
// present (it may be empty, which resolves to the same fallback evener uses).
func parseEnvProbe(out []byte) (map[string]string, error) {
	env := map[string]string{}
	text := strings.TrimRight(string(out), "\n")
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
// are treated as unset, matching evener's own env lookups.
func resolveRoots(goos string, env map[string]string) (configRoot, stateRoot string) {
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok && value != ""
	}
	stateRoot = cmdutil.StateRootFromLookup(goos, lookup)
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
	Protocol    string   `json:"protocol"`
	Version     string   `json:"version"`
	LaunchFlags []string `json:"launch_flags"`
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

// preflight probes host non-interactively and returns its facts. Every failure
// is a named error class: an unreachable host surfaces the retryable
// ErrSSHStart, while a contract violation surfaces a terminal class.
func (m *Manager) preflight(ctx context.Context, host hostreg.Host) (Preflight, error) {
	pf := Preflight{Host: host.Name}

	unameS, err := m.runRemote(ctx, host, "uname -s")
	if err != nil {
		return pf, err
	}
	pf.UnameOS = strings.TrimSpace(string(unameS))
	goos, ok := mapOS(pf.UnameOS)
	if !ok {
		return pf, fmt.Errorf("%w: host %q uname -s %q", ErrUnsupportedHost, host.Name, pf.UnameOS)
	}
	pf.OS = goos

	unameM, err := m.runRemote(ctx, host, "uname -m")
	if err != nil {
		return pf, err
	}
	pf.UnameMachine = strings.TrimSpace(string(unameM))
	goarch, ok := mapArch(pf.UnameMachine)
	if !ok {
		return pf, fmt.Errorf("%w: host %q uname -m %q", ErrUnsupportedHost, host.Name, pf.UnameMachine)
	}
	pf.Arch = goarch

	if !targetSupported(goos, goarch) {
		return pf, fmt.Errorf("%w: host %q has no build for %s/%s", ErrUnsupportedHost, host.Name, goos, goarch)
	}

	envOut, err := m.runRemote(ctx, host, envProbeScript)
	if err != nil {
		return pf, err
	}
	env, err := parseEnvProbe(envOut)
	if err != nil {
		return pf, err
	}
	pf.Home = env["HOME"]
	pf.ConfigRoot, pf.StateRoot = resolveRoots(goos, env)

	uidOut, err := m.runRemote(ctx, host, "id -u")
	if err != nil {
		return pf, err
	}
	pf.UID = parseEffectiveUID(uidOut)

	// Address the executable this Manager already resolved for the host (a deploy
	// target, or a discovered install) when the registry has no evener_path, so
	// version auto-match probes the binary the host actually runs rather than a
	// bare `evener` the non-interactive PATH may not carry.
	probeHost := host
	if strings.TrimSpace(probeHost.EvenerPath) == "" {
		probeHost.EvenerPath = m.resolvedTarget(host.Name)
	}
	lc, err := m.probeLaunchCheck(ctx, probeHost)
	if err != nil {
		missing := errors.Is(err, errExecutableMissing)
		if missing {
			// The executable is absent from the non-interactive PATH, but the
			// binary may still exist at the installer's default location
			// deployTarget falls back to. Probe it before recording the contract
			// as unknown, so a host that already runs the controller's build at
			// ~/.local/bin/evener is recognized instead of being re-deployed on
			// every reconnect. This runs whether or not a deploy is configured:
			// discovering an already-installed binary is the same probe either
			// way, and gating it on canDeploy made a controller with no build
			// source (or an unpublishable installer ref) fail to attach to a
			// perfectly good host whose binary is simply off the non-interactive
			// PATH. Only the deploy decision below is gated on canDeploy.
			if p, ok := m.probeInstallerDefaultExecutable(ctx, host); ok {
				probeHost.EvenerPath = p
				if lc2, err2 := m.probeLaunchCheck(ctx, probeHost); err2 == nil {
					m.setResolvedTarget(host.Name, p)
					pf.LaunchCheckKnown = true
					pf.Protocol = lc2.Protocol
					pf.Version = lc2.Version
					pf.LaunchFlags = append([]string(nil), lc2.LaunchFlags...)
					return pf, nil
				}
			}
		}
		if missing {
			// The dedicated probe verified that the resolved run target is not
			// executable there, and the installer default did not hold a binary
			// either: record the verified fact.
			pf.ExecutableMissing = true
		}
		if m.canDeploy() && (errors.Is(err, ErrProtocolIncompatible) || errors.Is(err, ErrPreflightDecode) || missing) {
			// The on-disk binary either refused the controller's appwire protocol
			// outright, returned a contract that cannot be read, or is not there at
			// all. Each is a fact about the binary, not a fatal preflight: ensureOnce
			// must be able to deploy a matching build over ssh (which uses neither
			// appwire nor the contract) before any refusal is made terminal. The
			// absent-executable case is why this matters for the installer fallback,
			// whose default target ~/.local/bin/evener is not on the non-interactive
			// PATH: without it, a host that simply has no evener installed could
			// never be deployed to. Record the contract as unknown and let the
			// decision ladder choose the deploy path. With no deploy configured
			// there is nothing to install, so the failure stays terminal rather than
			// being deferred to a deploy that can never run.
			return pf, nil
		}
		if missing {
			// The host has no evener at the resolved path and no deploy path is
			// configured (the branch above defers the same error when one is), so
			// nothing can install the binary. Call it terminally with the remedy this
			// refusal carries: the hub's flags when it supplied Options.DeployHelp,
			// else the library's own field.
			return pf, fmt.Errorf("%w: %s", err, m.deployHelp())
		}
		return pf, err
	}
	pf.LaunchCheckKnown = true
	pf.Protocol = lc.Protocol
	pf.Version = lc.Version
	pf.LaunchFlags = append([]string(nil), lc.LaunchFlags...)
	// Neither the protocol nor the launch-flag contract is refused here. Both are
	// judged by ensureOnce *after* the deploy path has had its chance: a 04a-era
	// host binary missing a required flag, or speaking an older appwire protocol,
	// must be upgradable by the version-match deploy before it is refused.
	// Otherwise it is permanently rejected and no deploy can ever fix it.
	return pf, nil
}

// probeLaunchCheck runs `evener launch-check` on the host and returns its parsed
// contract. It is the one place the invocation and its protocol-refusal and
// decode classification live, so preflight and the post-deploy re-probe in
// ensureOnce cannot drift apart.
func (m *Manager) probeLaunchCheck(ctx context.Context, host hostreg.Host) (launchCheck, error) {
	out, err := m.runner.Run(ctx, evenerCommandArgv(m.opts, host, "launch-check", "--protocol", appwire.ProtocolVersion, "--json"), nil)
	if err != nil {
		if isProtocolMismatchOutput(out) {
			return launchCheck{}, fmt.Errorf("%w: host %q refused protocol %s: %s", ErrProtocolIncompatible, host.Name, appwire.ProtocolVersion, tail(out))
		}
		if m.verifiedExecutableMissing(ctx, host) {
			return launchCheck{}, fmt.Errorf("%w: host %q launch-check: %s", errExecutableMissing, host.Name, tail(out))
		}
		return launchCheck{}, sshRunFailure(host.Name, "launch-check", err, tail(out))
	}
	return parseLaunchCheck(out)
}

// errExecutableMissing marks the verified absence of the evener executable at the
// host's resolved run target: the dedicated executable probe proved it (its own
// exit 1), rather than the failed launch-check's status or its not-found text
// being parsed. It is a fact about the executable's absence, not a transport or
// permission refusal, so a controller with a deploy configured records the launch
// contract as unknown and lets the installer fallback install a binary at a known
// location, instead of refusing the host outright.
var errExecutableMissing = errors.New("sshconn: evener executable not found on the host")

// ErrExecutableMissing is the exported alias for the terminal
// missing-executable refusal (errExecutableMissing, above): the host has no
// evener at the resolved path and, when no deploy path exists either, there is
// nothing the controller can install. It is terminal like ErrVersionMismatch
// (isTerminal), and it is exported so a caller — the hub's attach handler — can
// match it with errors.Is and surface it as a typed deploy/launch failure
// (appwire.HubLaunchError) rather than a generic internal error.
var ErrExecutableMissing = errExecutableMissing

// verifiedExecutableMissing reports whether the dedicated executable probe
// proves there is no evener executable at the command launch-check just tried.
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
func (m *Manager) verifiedExecutableMissing(ctx context.Context, host hostreg.Host) bool {
	_, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, executableProbeRemote(evenerCommand(host.EvenerPath))), nil)
	if err == nil {
		return false
	}
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

// executableProbeRemote builds the dedicated executable-presence probe for the
// command launch-check would run: exit 0 when target exists and is executable
// there, exit 1 when it is not. A path target is probed with `test -x`, the
// spec's form; a bare name is probed with `command -v`, the PATH equivalent,
// because `test -x evener` would test the login shell's cwd rather than the
// PATH the bare word resolves through. Both forms normalize their answer to the
// 0/1 status the caller reads, so the recognition does not depend on the shell
// generation, the host locale, or the wording of "not found".
func executableProbeRemote(target string) string {
	word := shellquote.RemoteWord(target)
	cond := "test -x " + word
	if !strings.Contains(target, "/") {
		cond = "command -v " + word + " >/dev/null 2>&1"
	}
	return "if " + cond + "; then exit 0; else exit 1; fi"
}

// runRemote runs a non-evener remote command, classifying a failure as an auth
// refusal or a transport failure.
func (m *Manager) runRemote(ctx context.Context, host hostreg.Host, remote string) ([]byte, error) {
	out, err := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
	if err != nil {
		return out, sshRunFailure(host.Name, fmt.Sprintf("command %q", remote), err, tail(out))
	}
	return out, nil
}

// isProtocolMismatchOutput recognizes a host binary that refused the requested
// appwire protocol. launch-check validates --protocol against its own compiled
// constant and exits nonzero on disagreement
// (cmd/evener/internal/launchcheck/launchcheck.go:69-71), so a mismatch reaches
// us as a failed Run, not a decodable response.
func isProtocolMismatchOutput(out []byte) bool {
	text := string(out)
	return strings.Contains(text, "unsupported appwire protocol") ||
		strings.Contains(text, "does not match Hub protocol")
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
