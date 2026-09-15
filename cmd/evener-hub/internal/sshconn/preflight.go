package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars/userdirs"
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
	StateRoot   string
	Version     string
	Protocol    string
	LaunchFlags []string
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
// is ErrPreflightDecode: the host cannot be trusted to launch.
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
// is a named, terminal error class: an unreachable host surfaces ErrSSHStart.
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

	checkOut, err := m.runner.Run(ctx, evenerCommandArgv(m.opts, host, "launch-check", "--protocol", appwire.ProtocolVersion, "--json"), nil)
	if err != nil {
		if isProtocolMismatchOutput(checkOut) {
			return pf, fmt.Errorf("%w: host %q refused protocol %s: %s", ErrProtocolIncompatible, host.Name, appwire.ProtocolVersion, tail(checkOut))
		}
		return pf, sshRunFailure(host.Name, "launch-check", err, tail(checkOut))
	}
	lc, err := parseLaunchCheck(checkOut)
	if err != nil {
		return pf, err
	}
	pf.Protocol = lc.Protocol
	pf.Version = lc.Version
	pf.LaunchFlags = append([]string(nil), lc.LaunchFlags...)
	if lc.Protocol != appwire.ProtocolVersion {
		return pf, fmt.Errorf("%w: host %q protocol %q, want %q", ErrProtocolIncompatible, host.Name, lc.Protocol, appwire.ProtocolVersion)
	}
	if !slices.Contains(lc.LaunchFlags, requiredLaunchFlag) {
		return pf, fmt.Errorf("%w: host %q launch_flags %v missing %q", ErrLaunchContract, host.Name, lc.LaunchFlags, requiredLaunchFlag)
	}
	return pf, nil
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
// authenticate non-interactively. Under BatchMode these are terminal: ssh will
// never prompt, so retrying only spams the host.
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
