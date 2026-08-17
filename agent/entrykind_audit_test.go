package agent

import "testing"

// turnOpening records how a turn of a given EntryKind acquires the identity a
// client addresses it by.
//
// Why this table exists: a turn whose opening event carries no identity opens
// under the AppWire projection's own turn_<n>, which the daemon's mutation
// preconditions never accept — so Steer, Send and Stop all fail on it, and the
// composer shows nothing at all (katas 7vmd, 2f41). That reached production
// because the naming mechanism was designed against the kinds someone had in
// mind at the time, and two of the five were not among them.
//
// WHAT THIS AUDIT CATCHES: a kind added to the EntryKind block without anyone
// deciding how its turns are named. WHAT IT DOES NOT: whether the label below
// is true. Nothing cross-checks a classification against behaviour, so writing
// unservedSoUnaddressable next to a kind that does run on a served session
// passes. The label is a claim its author has to have earned; the test only
// makes the claim mandatory.
type turnOpening string

const (
	// opensOnItsContentEvent: the event that opens the turn already carries a
	// StableTurnID — EventUserInput, EventGoalContinuation.
	opensOnItsContentEvent turnOpening = "content-event"
	// opensOnTurnStarted: the turn has no content event of its own, so it
	// announces itself with EventTurnStarted.
	opensOnTurnStarted turnOpening = "turn-started"
	// unservedSoUnaddressable: the kind only ever runs on a child session,
	// which has no authoritative consumer and takes no client mutations, so no
	// client can name its turns. Pinned by
	// TestPreparedSubagentGetsNoAuthoritativeConsumer.
	unservedSoUnaddressable turnOpening = "unserved"
)

// There used to be a fourth label, noProductionProducer, and exactly one kind
// wore it: EntryWatchDelivery. Its producer -- subagent.run's runFromWatch
// branch -- was deleted with the dormant delegate job schema (2a94f56d1), and
// the kind outlived it by six unreachable branches that every later reader had
// to reason about. Kata z5fm deleted it, and the label with it: a kind nothing
// dispatches is not a seam waiting to be used, it is residue, and the audit
// should not have a spelling for it. A new kind arrives WITH its producer and
// picks one of the two labels above.

// entryKindTurnOpening maps every EntryKind to how its turns acquire identity.

var entryKindTurnOpening = map[EntryKind]turnOpening{
	EntryUserInput:         opensOnItsContentEvent,
	EntryContinuation:      opensOnItsContentEvent,
	EntryNotification:      opensOnTurnStarted,
	EntryDelegateAttention: unservedSoUnaddressable,
}

func TestEveryEntryKindDeclaresHowItsTurnOpens(t *testing.T) {
	for kind := range entryKindCount {
		if _, ok := entryKindTurnOpening[kind]; !ok {
			t.Fatalf("EntryKind %d declares no turn opening: add it to entryKindTurnOpening. "+
				"A turn nobody named opens under an id no client can address, and Steer, "+
				"Send and Stop then fail on it silently (kata 7vmd)", kind)
		}
	}
	if len(entryKindTurnOpening) != int(entryKindCount) {
		t.Fatalf("entryKindTurnOpening has %d entries for %d kinds; a stale row hides a missing one",
			len(entryKindTurnOpening), entryKindCount)
	}
}
