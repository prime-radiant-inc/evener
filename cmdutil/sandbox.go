package cmdutil

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// ConfigureSandbox parses the --sandbox / --sandbox-net flag values into the
// session config's carrier fields. Off (the default) leaves the carrier fields
// zero, so a default session's env and persisted config are byte-identical to an
// unsandboxed run today; a non-off mode records the mode + network decision so the
// resolved policy round-trips into the persisted meta and a resume re-applies it.
//
// It does NOT engage enforcement — that is ProvisionSandbox's job at env
// construction. Splitting them keeps the persisted request (carrier) distinct from
// the live host-resolved policy: the carrier is the immutable-across-restart INPUT,
// re-resolved against freshly-probed host facts on every start.
func ConfigureSandbox(cfg *agent.SessionConfig, modeFlag, netFlag string) error {
	// An unset mode means off (the flag default). Internal callers that build a
	// config without the flag layer pass "" and must get today's behavior, not an
	// "unknown mode" error.
	if strings.TrimSpace(modeFlag) == "" {
		modeFlag = sandbox.ModeOff.String()
	}
	mode, err := sandbox.ParseMode(modeFlag)
	if err != nil {
		return err
	}
	net, err := ParseSandboxNet(netFlag)
	if err != nil {
		return err
	}
	if mode == sandbox.ModeOff {
		return nil // off: today's behavior; carrier stays zero (byte-identical no-op)
	}
	cfg.Sandbox = mode.String()
	cfg.SandboxNet = &net
	return nil
}

// ProvisionSandbox engages enforcement on a fresh session's execution environment
// from its configured mode: it re-resolves the policy against freshly-probed host
// facts (from probeHost) and, on success, builds an ENFORCED env (in-process file-tool layer plus
// the kernel wrapper) via EnableSandbox. It is the single live-flip that makes a
// CLI-set --sandbox mode actually contain a session (M5).
//
// Off (the default) returns immediately WITHOUT probing the host, so an unsandboxed
// run never forks the capability probes and stays byte-identical to today. A mode
// the host cannot enforce surfaces the resolver's *sandbox.RefusalError, which the
// caller returns to fail session start with the fail-closed floor's legible message.
func ProvisionSandbox(env *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, cwd string, probeHost func() sandbox.HostFacts) error {
	if sandbox.ModeIsOff(cfg.Sandbox) {
		return agent.PickFreshSessionID(env, cfg)
	}
	return ProvisionSandboxWithHost(env, cfg, cwd, probeHost())
}

// ProvisionSandboxWithHost is ProvisionSandbox with the host facts supplied by the
// caller rather than probed, so a test can drive the exact resolve+enforce path
// with a controlled home (the credential denylist anchors on host.Home) while still
// building a real kernel wrapper from the resolved backend.
func ProvisionSandboxWithHost(env *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, cwd string, host sandbox.HostFacts) error {
	// The sandbox mints the session's scratch, so the session's ID has to
	// exist first for the scratch to land in the session's own tree.
	if err := agent.PickFreshSessionID(env, cfg); err != nil {
		return err
	}
	// The session's hook and MCP-server paths join the policy's read/exec surface
	// (ruled 2026-08-06: they are session infrastructure and must work in every
	// mode). Derived only for a sandboxed session, so an off run does no extra
	// config reads.
	var infra []string
	if !sandbox.ModeIsOff(cfg.Sandbox) {
		infra = agent.SessionInfraRoots(*cfg, env)
	}
	rp, err := sandbox.ResolveNamed(cfg.Sandbox, cfg.SandboxNet, host, cwd, infra)
	if err != nil {
		return err
	}
	// rp is nil only for an off/empty mode (ResolveNamed); EnableSandbox(nil) is a
	// no-op, so an off env stays byte-identical.
	return env.EnableSandbox(rp)
}

// ParseSandboxNet maps the --sandbox-net value to a boolean (on = egress
// allowed). Empty defaults to on (the default when sandboxed).
func ParseSandboxNet(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid --sandbox-net %q (want on or off)", v)
	}
}
