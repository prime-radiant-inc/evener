package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
)

// configureSandbox parses the --sandbox / --sandbox-net flag values into the
// session config's carrier fields. Off (the default) leaves the carrier fields
// zero, so a default session's env and persisted config are byte-identical to an
// unsandboxed run today; a non-off mode records the mode + network decision so the
// resolved policy round-trips into the persisted meta and a resume re-applies it.
//
// It does NOT engage enforcement — that is provisionSandbox's job at env
// construction. Splitting them keeps the persisted request (carrier) distinct from
// the live host-resolved policy: the carrier is the immutable-across-restart INPUT,
// re-resolved against freshly-probed host facts on every start.
func configureSandbox(cfg *agent.SessionConfig, modeFlag, netFlag string) error {
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
	net, err := parseSandboxNet(netFlag)
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

// provisionSandbox engages enforcement on a fresh session's execution environment
// from its configured mode: it re-resolves the policy against freshly-probed host
// facts and, on success, builds an ENFORCED env (in-process file-tool layer plus
// the kernel wrapper) via EnableSandbox. It is the single live-flip that makes a
// CLI-set --sandbox mode actually contain a session (M5).
//
// Off (the default) returns immediately WITHOUT probing the host, so an unsandboxed
// run never forks the capability probes and stays byte-identical to today. A mode
// the host cannot enforce surfaces the resolver's *sandbox.RefusalError, which the
// caller returns to fail session start with the fail-closed floor's legible message.
func provisionSandbox(env *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, cwd string) error {
	if sandbox.ModeIsOff(cfg.Sandbox) {
		return nil
	}
	return provisionSandboxWithHost(env, cfg, cwd, sandbox.RealProber{}.Probe())
}

// provisionSandboxWithHost is provisionSandbox with the host facts supplied by the
// caller rather than probed, so a test can drive the exact resolve+enforce path
// with a controlled home (the credential denylist anchors on host.Home) while still
// building a real kernel wrapper from the resolved backend.
func provisionSandboxWithHost(env *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, cwd string, host sandbox.HostFacts) error {
	rp, err := sandbox.ResolveNamed(cfg.Sandbox, cfg.SandboxNet, host, cwd)
	if err != nil {
		return err
	}
	// rp is nil only for an off/empty mode (ResolveNamed); EnableSandbox(nil) is a
	// no-op, so an off env stays byte-identical.
	return env.EnableSandbox(rp)
}

// reconcileClearSandbox makes a cleared serve session's config agree with the
// environment it reuses. serve's /clear starts a fresh session on the SAME env, so
// the cleared session inherits the env's ACTUAL sandbox — which on resume is the
// persisted mode, not the launch flag. Setting the config's mode + network from the
// env's resolved policy inputs keeps what the session persists identical to what its
// env enforces; an off (unsandboxed) env clears the carrier so the cleared session
// is off too. Without this, a session resumed sandboxed but cleared under an off
// flag would persist off while running enforced (or the reverse), and the sandbox
// could silently evaporate on the next resume.
func reconcileClearSandbox(cfg *agent.SessionConfig, env *execenv.LocalExecutionEnvironment) {
	if env == nil || env.Sandbox == nil || !env.Sandbox.Enforced() {
		cfg.Sandbox = ""
		cfg.SandboxNet = nil
		return
	}
	in := env.Sandbox.Inputs()
	cfg.Sandbox = in.Mode.String()
	cfg.SandboxNet = in.Network
}

// sandboxEnforcementLine returns the single startup line describing what a
// sandboxed session enforces on this host (backend + mode + network + cache
// strategy), read from the environment's ACTUAL resolved policy so it can never
// overstate — a mode that degraded (e.g. overlay-unavailable → cold cache) or a
// backend the host could not serve is reflected exactly. Returns "" for an
// unsandboxed environment, so there is nothing to print for the default path.
func sandboxEnforcementLine(env execenv.ExecutionEnvironment) string {
	le, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil {
		return ""
	}
	return sandbox.EnforcementLine(*le.Sandbox)
}

// parseSandboxNet maps the --sandbox-net value to a boolean (on = egress
// allowed). Empty defaults to on (the default when sandboxed).
func parseSandboxNet(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid --sandbox-net %q (want on or off)", v)
	}
}
