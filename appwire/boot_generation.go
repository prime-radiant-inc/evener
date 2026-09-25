package appwire

import "strconv"

// DaemonlessBootGeneration is the boot generation the hub stamps on a read it
// serves from the transcript with no daemon running for the session.
const DaemonlessBootGeneration = "daemonless"

// BootGeneration spells a daemon's boot counter as the wire carries it.
func BootGeneration(counter uint64) string {
	return strconv.FormatUint(counter, 10)
}

// BootGenerationAction is what a client does with a read response or
// history update, given the boot generation it holds for the thread.
type BootGenerationAction int

const (
	// BootGenerationApply: the same token as held; apply normally.
	BootGenerationApply BootGenerationAction = iota
	// BootGenerationIgnore: a lower numeric generation, from a daemon boot
	// the client already moved past.
	BootGenerationIgnore
	// BootGenerationReplace: any other token (a higher number, or a switch
	// between numeric and daemonless). The client marks the thread invalid,
	// drops updates, and replaces the whole history with a fresh subscribing
	// latest-window read.
	BootGenerationReplace
)

// CompareBootGeneration is the spec's one state machine for boot generations
// (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
// "Recorded length, entry ordinals and Seq"): it decides what a client
// holding held does with a message carrying incoming.
func CompareBootGeneration(held, incoming string) BootGenerationAction {
	if held == incoming {
		return BootGenerationApply
	}
	heldCounter, heldErr := strconv.ParseUint(held, 10, 64)
	incomingCounter, incomingErr := strconv.ParseUint(incoming, 10, 64)
	if heldErr == nil && incomingErr == nil && incomingCounter < heldCounter {
		return BootGenerationIgnore
	}
	return BootGenerationReplace
}
