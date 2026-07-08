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
// half-enforced non-off mode must never be reachable through the user flag, so
// any non-off --sandbox value FAILS SESSION START here — a single explicit gate
// at the flag boundary, distinct from the fail-closed-floor *sandbox.RefusalError
// that sandbox.Resolve returns when a host cannot satisfy a mode (that path is
// exercised directly in the sandbox package's tests, never through this flag).
//
// off (the default) leaves the carrier fields zero, so a default session's env
// and persisted config are byte-identical to today's.
//
// REMOVE THIS GATE IN M5, when --sandbox goes live on a validated backend.
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
		return nil // today's behavior; carrier stays zero (byte-identical no-op)
	}
	// Carry the request inert (M4 resume re-applies it once enforcement exists).
	cfg.Sandbox = mode.String()
	cfg.SandboxNet = &net
	return fmt.Errorf("sandbox support is in development and not yet enabled (--sandbox %s); only --sandbox off is currently available", mode)
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
