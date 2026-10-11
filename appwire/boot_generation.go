package appwire

import (
	"strconv"
	"strings"
)

// DaemonlessBootGeneration is the boot generation the hub stamps on a read it
// serves from the transcript with no daemon running for the session.
const DaemonlessBootGeneration = "daemonless"

// BootGeneration spells a daemon's boot counter as the wire carries it for
// the daemon's root thread.
func BootGeneration(counter uint64) string {
	return strconv.FormatUint(counter, 10)
}

// DescendantBootGeneration is the boot generation of a descendant served by
// its root's daemon: "<n>@<rootSessionID>", the root's generation qualified
// by the root. A descendant has no counter of its own, so its generation is
// comparable only with others from the same root's daemon; a daemon serving
// the descendant as its own root carries a plain counter, which replaces.
func DescendantBootGeneration(rootGeneration, rootSessionID string) string {
	return rootGeneration + "@" + rootSessionID
}

// BootGenerationAction is what a client does with a read response or
// history update, given the boot generation it holds for the thread.
type BootGenerationAction int

const (
	// BootGenerationApply: the same token as held; apply normally.
	BootGenerationApply BootGenerationAction = iota
	// BootGenerationIgnore: a lower generation of the same counter (the same
	// root, qualified or not), from a daemon boot the client already moved
	// past.
	BootGenerationIgnore
	// BootGenerationReplace: any other token (a higher number, another
	// counter, or a switch between numeric and daemonless). The client marks
	// the thread invalid, drops updates, and replaces the whole history with
	// a fresh subscribing latest-window read.
	BootGenerationReplace
)

// CompareBootGeneration is the spec's one state machine for boot generations
// (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
// "Recorded length, entry ordinals and Seq"): it decides what a client
// holding held does with a message carrying incoming. Two tokens compare
// numerically only when both are counters of the same kind: both plain, or
// both qualified by the same root (DescendantBootGeneration). Anything else
// that differs replaces.
func CompareBootGeneration(held, incoming string) BootGenerationAction {
	if held == incoming {
		return BootGenerationApply
	}
	heldCounter, heldOwner, heldOK := parseBootGeneration(held)
	incomingCounter, incomingOwner, incomingOK := parseBootGeneration(incoming)
	if !heldOK || !incomingOK || heldOwner != incomingOwner {
		return BootGenerationReplace
	}
	switch {
	case incomingCounter < heldCounter:
		return BootGenerationIgnore
	case incomingCounter == heldCounter:
		return BootGenerationApply
	default:
		return BootGenerationReplace
	}
}

// parseBootGeneration reads a counter token: "<n>" (owner "") or
// "<n>@<root>". daemonless and malformed tokens report false.
func parseBootGeneration(token string) (counter uint64, owner string, ok bool) {
	number, owner, qualified := strings.Cut(token, "@")
	if qualified && owner == "" {
		return 0, "", false
	}
	counter, err := strconv.ParseUint(number, 10, 64)
	return counter, owner, err == nil
}
