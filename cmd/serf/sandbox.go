package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/sandbox"
)

// configureSandbox parses the --sandbox / --sandbox-net flag values into the
// session config's inert carrier fields and applies the M1 feature gate.
//
// The sandbox enforcement layers (in-process file tools in M2, the kernel
// wrapper in M3) are not wired yet, and --sandbox does not go live until M5. A
// half-enforced non-off mode must never be reachable, so any non-off --sandbox
// value FAILS SESSION START via the shared sandbox.FeatureGate — the SAME gate
// the restore path applies to a persisted ConfigSnapshot, distinct from the
// fail-closed-floor *sandbox.RefusalError that sandbox.Resolve returns when a
// host cannot satisfy a mode (that path is exercised directly in the sandbox
// package's tests, never through this flag).
//
// off (the default) leaves the carrier fields zero, so a default session's env
// and persisted config are byte-identical to today's.
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
	if err := sandbox.FeatureGate(mode); err != nil {
		// Carry the request inert (M4 resume re-applies it once enforcement exists)
		// so the failed-start config still round-trips the requested mode.
		cfg.Sandbox = mode.String()
		cfg.SandboxNet = &net
		return err
	}
	return nil // off: today's behavior; carrier stays zero (byte-identical no-op)
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
