package appwire

import (
	"strconv"
	"strings"
)

// DaemonlessBootGeneration is the boot generation of a read the hub answers
// from the persisted transcript rather than from a daemon for the session.
//
// On a failed read it is a positive claim of absence: the hub established that
// no daemon serves the thread — the roster answered that nobody owns it, or the
// ref names a source this hub does not serve. A failed read that could not
// establish that carries UnknownOwnerBootGeneration instead, because a read
// that failed is no evidence about who would have served it.
//
// A successful saved read carries this token even when ownership was never
// resolved: the hub fell back to the transcript after a live read failed for
// some reason that says nothing about the owner (a relay outage, a protocol
// refusal, a deadline). Clients must not read this token, on a success, as
// proof that the daemon is gone.
const DaemonlessBootGeneration = "daemonless"

// UnknownOwnerBootGeneration is the boot generation the hub stamps on a failed
// history read whose serving daemon it never established: the saved-history
// fallback that answered the read failed, and the evidence that made the
// fallback necessary did not establish who was serving the thread. That covers
// a live read that failed for a reason naming no absence, and a local route
// miss the roster could not resolve to either an owner's absence or a
// confirmed owner — an incompatible daemon, or an incomplete scan.
//
// It is not a claim that no daemon runs. A failed read must never claim a
// daemon is gone, because the only evidence it has is that it failed. It is no
// counter either: CompareBootGeneration applies it to itself and replaces any
// other token.
const UnknownOwnerBootGeneration = "unknown-owner"

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
// "<n>@<root>". DaemonlessBootGeneration, UnknownOwnerBootGeneration and
// malformed tokens report false.
func parseBootGeneration(token string) (counter uint64, owner string, ok bool) {
	number, owner, qualified := strings.Cut(token, "@")
	if qualified && owner == "" {
		return 0, "", false
	}
	counter, err := strconv.ParseUint(number, 10, 64)
	return counter, owner, err == nil
}
