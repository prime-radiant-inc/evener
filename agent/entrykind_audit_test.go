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
	// noProductionProducer: nothing outside tests ever dispatches this kind.
	// Wiring one up means choosing one of the labels above first.
	//
	// This is a DEBT MARKER, not a resting state, and it has a precedent.
	// EntryWatchDelivery wore it after its only producer -- subagent.run's
	// runFromWatch branch -- was deleted with the dormant delegate job schema
	// (2a94f56d1). It then sat here for three months while six unreachable
	// branches accumulated readers, and it took an adversarial branch review to
	// file the removal (kata z5fm). The label was honest and correct; what was
	// missing is that nothing obliged anyone to act on it. Read a kind wearing
	// this as on the clock.
	//
	// z5fm's first draft deleted this label outright, on the theory that a kind
	// nothing dispatches is residue rather than a seam. That was wrong twice
	// over. It disarms the detector on the strength of the detector having
	// worked -- this label is why z5fm was filed at all. And with three labels
	// and no honest option, a kind added before its producer exists (a feature
	// landing in stages) can only go green by claiming a label that is FALSE for
	// it, most plausibly unservedSoUnaddressable. This audit's own header says
	// nothing cross-checks a classification against behaviour, so a false label
	// is invisible where an honest one is greppable. Keeping the escape hatch is
	// what keeps the table truthful.
	noProductionProducer turnOpening = "no-producer"
)

// validTurnOpenings closes the label vocabulary. turnOpening is a string type,
// so the map below accepts any literal at all -- a typo, or a label someone
// invents rather than choosing one of these. Keeping the set here is also what
// keeps an unused escape hatch (noProductionProducer, which no kind wears
// today) from being deleted as dead by the unused linter: the vocabulary is
// deliberately wider than what is currently spoken.
var validTurnOpenings = map[turnOpening]bool{
	opensOnItsContentEvent:  true,
	opensOnTurnStarted:      true,
	unservedSoUnaddressable: true,
	noProductionProducer:    true,
}

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

// TestEveryTurnOpeningLabelIsOneOfTheVocabulary keeps the table's answers
// inside the vocabulary. Without it a kind can satisfy the mandatory-claim
// audit with any string, which is the one way to pass that test while saying
// nothing.
func TestEveryTurnOpeningLabelIsOneOfTheVocabulary(t *testing.T) {
	for kind, opening := range entryKindTurnOpening {
		if !validTurnOpenings[opening] {
			t.Fatalf("EntryKind %d declares turn opening %q, which is not one of the %d labels: "+
				"pick one, or add a new one with the argument for why the existing spellings do not fit",
				kind, opening, len(validTurnOpenings))
		}
	}
}
