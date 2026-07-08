package sandbox

import "fmt"

// FeatureGate enforces the pre-M5 rule that a non-off sandbox mode is refused
// while the enforcement layers are still in development: no file tool or spawned
// command consults a resolved policy yet (M2 wires the in-process file tools, M3
// the kernel wrapper), so a session claiming a non-off mode would be sandboxed in
// name only. Both session start (the --sandbox flag layer in cmd/serf) and
// session restore (a persisted ConfigSnapshot in
// agent.RestoreSessionFromMetaWithConfig) route through this single gate, so a
// persisted or hand-edited meta.json carrying "sandbox":"restricted" cannot
// resume claiming sandboxing with nothing enforced. ModeOff passes — today's
// behavior.
//
// REMOVE THIS GATE IN M5, when --sandbox goes live on a validated backend.
func FeatureGate(mode Mode) error {
	if mode == ModeOff {
		return nil
	}
	return fmt.Errorf("sandbox support is in development and not yet enabled (--sandbox %s); only --sandbox off is currently available", mode)
}
