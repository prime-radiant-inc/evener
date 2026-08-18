package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

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
// deciding how its turns are named, AND a row whose label is not what the kind
// does. Each live label names a distinct, observable behaviour, so
// TestEveryTurnOpeningLabelMatchesTheKindsBehaviour runs one real turn per kind
// and reads the answer off the event stream instead of trusting the row (kata
// vxz3). Writing unservedSoUnaddressable next to a kind that opens a named turn
// on a served session now fails.
//
// WHAT IT STILL DOES NOT: check the one label with no behaviour to observe.
// noProductionProducer says nothing dispatches the kind, so there is no turn to
// run and a kind wearing it is skipped. Claiming it therefore takes TWO
// deliberate edits -- the row, and kindsNothingDispatches -- so a probe that
// goes red cannot be silenced by quietly relabelling its kind. What no test can
// do is confirm the claim itself: that a producer really is absent stays a grep
// and a reviewer's judgement.
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
	// it, most plausibly unservedSoUnaddressable.
	//
	// The behaviour probes below check every OTHER label against what the kind
	// does, so a false live label is caught rather than invisible. This label is
	// the one they cannot check -- there is no turn to observe -- so wearing it
	// costs a second, deliberate edit instead: kindsNothingDispatches has to
	// name the kind too. Without the label at all, a producerless kind would
	// have no honest option left: no live label any probe could confirm, and a
	// suite red until someone invented either a producer or a lie.
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

// probeExemptTurnOpenings records the labels no behaviour probe can check, and
// why. A label belongs here only when there is nothing to observe -- never
// because observing it is awkward.
var probeExemptTurnOpenings = map[turnOpening]string{
	// Nothing outside tests dispatches a kind wearing this, so there is no turn
	// to run and no event stream to read: the exemption IS the label's content.
	//
	// Kata z5fm already ruled on the alternative. Its first draft deleted the
	// label rather than exempting it, and that was rejected -- see
	// noProductionProducer's own comment above for the reasoning, which is not
	// re-argued here.
	noProductionProducer: "nothing dispatches this kind, so no turn of it exists to observe",
}

// kindsNothingDispatches names every EntryKind excused from the behaviour
// probe. Empty today, and it has to stay in exact agreement with the rows
// wearing a probeExemptTurnOpenings label --
// TestTheProbeExemptionNamesTheSameKindsAsTheTable is what holds the two
// together.
//
// It exists because an exemption has two halves and guarding only the label
// side leaves the cheaper lie open. Widening probeExemptTurnOpenings is an edit
// TestOnlyTheUnobservableLabelIsExemptFromTheProbe refuses; MOVING a live
// kind's row to noProductionProducer was, until this set, one edit that skipped
// that kind's probe in silence. Naming the kind here as well is what turns
// excusing a kind into a decision somebody has to make on purpose.
var kindsNothingDispatches = map[EntryKind]bool{}

// entryKindTurnOpening maps every EntryKind to how its turns acquire identity.
var entryKindTurnOpening = map[EntryKind]turnOpening{
	EntryUserInput:         opensOnItsContentEvent,
	EntryContinuation:      opensOnItsContentEvent,
	EntryNotification:      opensOnTurnStarted,
	EntryDelegateAttention: unservedSoUnaddressable,
	EntrySteeringCarrier:   opensOnTurnStarted,
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

// --- the oracle: what each row CLAIMS, checked against what the kind DOES ---
//
// Each live label names a different observable fact about a turn of that kind:
//
//	opensOnItsContentEvent  the turn's opening content event (EventUserInput,
//	                        EventGoalContinuation) carries a StableTurnID
//	opensOnTurnStarted      the turn announces itself with EventTurnStarted
//	unservedSoUnaddressable the turn opens under no id at all, and naming one on
//	                        the session shape the kind is dispatched on refuses
//	                        with turnNameUnserved
//
// The probes below run ONE real turn per kind and read those facts off the
// event stream. Nothing on the probe path reads entryKindTurnOpening: a probe
// that took its expectation from the table it audits would reproduce exactly
// the defect this audit exists to close.

// turnOpeningObservation is what one turn of a kind actually did with its
// identity. Both fields empty means the turn opened under nothing a client
// could address.
type turnOpeningObservation struct {
	// contentEventTurnID is the StableTurnID the turn's opening content event
	// carried, or "" when it carried none (or when there was no content event).
	contentEventTurnID string
	// announcedTurnID is the TurnID EventTurnStarted carried, or "" when the
	// turn announced no boundary.
	announcedTurnID string
}

// openingTurnID is the id the turn actually opened under: its content event's
// where it had one, otherwise the boundary it announced. "" means the turn
// opened under nothing a client could address.
func (o turnOpeningObservation) openingTurnID() string {
	if o.contentEventTurnID != "" {
		return o.contentEventTurnID
	}
	return o.announcedTurnID
}

// turnOpeningRecorder collects the identity-bearing opening events a session
// emits. Registering the drain through ConsumeEventsLossless is also what marks
// the session served (session.go makes that the only writer), which is the
// precondition for a turn to be nameable at all -- so this is both the
// collector and the thing that makes the observation possible.
type turnOpeningRecorder struct {
	session *Session
	drained chan struct{}

	mu  sync.Mutex
	obs turnOpeningObservation
}

func recordTurnOpenings(s *Session) *turnOpeningRecorder {
	rec := &turnOpeningRecorder{session: s, drained: make(chan struct{})}
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		switch data := ev.Data.(type) {
		case events.UserInputData:
			rec.obs.contentEventTurnID = data.StableTurnID
		case events.GoalContinuationData:
			rec.obs.contentEventTurnID = data.StableTurnID
		case events.TurnStartedData:
			rec.obs.announcedTurnID = data.TurnID
		}
	}, func() { close(rec.drained) })
	return rec
}

// snapshot closes the session and waits for the consumer goroutine before
// reading, so the observation covers the whole stream.
//
// ProcessInputKind returning does NOT mean its events have been consumed: the
// drain runs on its own goroutine. Reading straight after the call would flake
// on a positive observation and -- worse -- pass a negative one ("this kind
// announced nothing") merely by reading first. Closing ends the drain and fires
// onDrained; that is the awaitable completion, not a sleep.
func (r *turnOpeningRecorder) snapshot() turnOpeningObservation {
	r.session.Close()
	<-r.drained
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.obs
}

// observeTurnOpeningServed drives exactly one turn of kind on a session a
// daemon drains, by the entry path production uses for that kind, and reports
// what its opening events carried.
//
// A served session is the shape that discriminates all three labels: naming is
// available there, so a kind that STILL opens nothing is one that cannot be
// named rather than one that merely was not.
func observeTurnOpeningServed(t *testing.T, kind EntryKind) turnOpeningObservation {
	t.Helper()
	s := newTestSessionForEnvctx(t)
	rec := recordTurnOpenings(s)

	// promisedTurnID is the identity the client was handed before the turn ran,
	// where the entry path hands one out at all.
	var promisedTurnID string
	switch kind {
	case EntryUserInput:
		// The daemon's own path for a user turn (cmd/serf/serve.go): turn/start
		// reserves the identity durably and the drain loop claims it.
		//
		// This is the shape that carries an identity, not the only shape of the
		// kind: ProcessPendingUserInput runs a claimed QUEUE entry as
		// EntryUserInput too, under that entry's own reserved StableTurnID.
		// Both shapes are named, so either would do here; turn/start is the one
		// a client drives directly.
		accepted, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
			ClientMutationID: "entrykind-audit-probe",
			Input:            []appwire.InputItem{{Type: "text", Text: "probe"}},
		})
		if err != nil {
			t.Fatalf("AcceptClientMutationStart: %v", err)
		}
		if accepted.Turn.ID == "" {
			t.Fatal("turn/start handed the client no turn id; the comparison below would hold vacuously")
		}
		promisedTurnID = accepted.Turn.ID
		if _, claimed, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil {
			t.Fatalf("ProcessClientMutationStart: %v", err)
		} else if !claimed {
			t.Fatal("ProcessClientMutationStart claimed no durable start; the probe drove no turn")
		}
	case EntrySteeringCarrier:
		// The carrier exists only to deliver steering the client has already had
		// accepted, so with nothing pending the wake stands down and opens no
		// turn at all: accepting a steer first is what gives it something to
		// carry.
		//
		// The id it runs under is that steer's OWN reserved id --
		// claimSteeringCarrierTurn reuses it rather than minting a fresh one --
		// so the receipt the client is already holding is the promise the
		// announcement below has to keep.
		accepted, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "entrykind-audit-probe-steer",
			Input:            []appwire.InputItem{{Type: "text", Text: "probe"}},
		})
		if err != nil {
			t.Fatalf("AcceptClientMutationSteer: %v", err)
		}
		if accepted.Receipt.TurnID == "" {
			t.Fatal("turn/steer handed the client no turn id; the comparison below would hold vacuously")
		}
		promisedTurnID = accepted.Receipt.TurnID
		if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil {
			t.Fatalf("ProcessPendingUserInput: %v", err)
		} else if !ran {
			t.Fatal("ProcessPendingUserInput ran no carrier turn; the probe drove no turn")
		}
	default:
		if kind == EntryNotification {
			// A wake with nothing to deliver stands down as a no-op and opens no
			// turn at all, so give this one a single thing to deliver.
			s.SteerKind("probe", events.SteeringKindNotification)
		}
		if _, err := s.ProcessInputKind(context.Background(), "probe", nil, kind); err != nil {
			t.Fatalf("ProcessInputKind(EntryKind %d): %v", kind, err)
		}
	}

	obs := rec.snapshot()

	// A probe that silently drove no turn observes no identity, which the
	// classifier would read as "this kind cannot be named" -- a pass for the
	// wrong reason on exactly the label hardest to check. The round counter is
	// the proof that a turn really ran.
	s.mu.Lock()
	rounds := s.totalRounds
	s.mu.Unlock()
	if rounds == 0 {
		t.Fatalf("the probe for EntryKind %d ran no model round, so it observed nothing "+
			"rather than observing that nothing opens the turn", kind)
	}
	// Where an identity was promised, the one on the wire has to BE it. An id
	// the session minted for itself would be just as well-formed and just as
	// unaddressable, so non-emptiness alone is not the property claimed.
	// Read through openingTurnID rather than one named field: which event opens
	// the turn is exactly what this audit classifies, so pinning the promise to
	// the content event would exempt every kind that announces instead.
	if promisedTurnID != "" && obs.openingTurnID() != promisedTurnID {
		t.Fatalf("EntryKind %d opened its turn under %q; the client was promised %q",
			kind, obs.openingTurnID(), promisedTurnID)
	}
	return obs
}

// namingRefusalOnASpawnedChild stands up a REAL prepared subagent -- the only
// session shape subagent.run dispatches an unserved kind on -- and asks it to
// name a turn. The refusal it gets back is the third labelled behaviour.
//
// It asserts on a spawned child rather than a hand-built Session because the
// claim is about the spawn path; that the path never marks a child served is
// TestPreparedSubagentGetsNoAuthoritativeConsumer's job, and this reads the
// consequence.
func namingRefusalOnASpawnedChild(t *testing.T) turnNameRefusal {
	t.Helper()
	parent := newTestSession(t)
	prepared, err := parent.prepareSubagentRun(context.Background(), "turn-opening audit probe", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	child := prepared.sub.sess
	defer child.Close()

	_, refusal := child.mintRunningTurnID()
	return refusal
}

// classifyTurnOpening reports the label a kind's OBSERVED behaviour earns.
func classifyTurnOpening(t *testing.T, kind EntryKind) turnOpening {
	t.Helper()
	obs := observeTurnOpeningServed(t, kind)
	// Both id-bearing labels claim the same id SPACE, not merely a non-empty
	// string: the durable turn_m<n> a client addresses. A turn_<n> minted by
	// the AppWire projection is well-formed, non-empty and unaddressable, and
	// that is precisely what reached production (katas 7vmd, 2f41).
	for _, id := range []string{obs.contentEventTurnID, obs.announcedTurnID} {
		if id != "" && !strings.HasPrefix(id, "turn_m") {
			t.Fatalf("EntryKind %d opened its turn under %q; a turn a client can address carries the "+
				"durable turn_m<n> name, not an id the projection minted for itself", kind, id)
		}
	}
	switch {
	case obs.contentEventTurnID != "":
		return opensOnItsContentEvent
	case obs.announcedTurnID != "":
		return opensOnTurnStarted
	}
	// The turn opened under no id on a session that had one to give, so the
	// only label left is the one that says no client can name it -- and that
	// claim has its own observable half.
	if refusal := namingRefusalOnASpawnedChild(t); refusal != turnNameUnserved {
		t.Fatalf("EntryKind %d opened an unnamed turn on a SERVED session, and the child shape it is "+
			"dispatched on refuses naming with %v rather than turnNameUnserved: this kind's turns are "+
			"unaddressable for a reason no label describes", kind, refusal)
	}
	return unservedSoUnaddressable
}

// TestEveryTurnOpeningLabelMatchesTheKindsBehaviour is the oracle the table
// spent its first life without: it runs a turn of each kind and fails when the
// row claims something the turn did not do.
//
// The mandatory-claim audit above catches a kind nobody classified. This one
// catches a kind someone classified WRONG -- which is the failure that reached
// production (katas 7vmd, 2f41), since a turn opening under an id no mutation
// precondition accepts looks identical to a correctly labelled one until a
// client presses Steer, Send or Stop on it.
func TestEveryTurnOpeningLabelMatchesTheKindsBehaviour(t *testing.T) {
	for kind := range entryKindCount {
		claimed, ok := entryKindTurnOpening[kind]
		if !ok {
			continue // TestEveryEntryKindDeclaresHowItsTurnOpens owns this failure.
		}
		// Keyed on the KIND, not on its label: a row moved to a probe-exempt
		// label without the matching entry here still gets probed, and fails.
		if kindsNothingDispatches[kind] {
			continue
		}
		observed := classifyTurnOpening(t, kind)
		if observed != claimed {
			t.Fatalf("EntryKind %d is labelled %q but its turn behaved as %q; correct the label, or the "+
				"kind's naming, so a client can address the turns it opens", kind, claimed, observed)
		}
	}
}

// TestOnlyTheUnobservableLabelIsExemptFromTheProbe pins the exemption list
// itself. Without it, the cheapest way to silence a failing probe is to exempt
// the label rather than fix the kind -- which puts the table straight back to
// claims with no oracle, one label at a time.
func TestOnlyTheUnobservableLabelIsExemptFromTheProbe(t *testing.T) {
	for label := range validTurnOpenings {
		reason, exempt := probeExemptTurnOpenings[label]
		if exempt && label != noProductionProducer {
			t.Fatalf("turn opening %q is exempt from the behaviour probe (%q), but it describes a kind "+
				"something dispatches: probe it instead of exempting it", label, reason)
		}
		if !exempt && label == noProductionProducer {
			t.Fatalf("turn opening %q lost its probe exemption; a kind nothing dispatches runs no turn, "+
				"so the probe has nothing to observe (kata z5fm)", label)
		}
	}
}

// TestTheProbeExemptionNamesTheSameKindsAsTheTable closes the other half of the
// exemption. The audit skips a kind named in kindsNothingDispatches; the table
// says which kinds claim to have no producer. Letting those two drift is what
// makes an excused kind cheap: with the skip keyed on the label alone, moving a
// live kind's row to noProductionProducer silences its probe in one edit and
// says so nowhere.
func TestTheProbeExemptionNamesTheSameKindsAsTheTable(t *testing.T) {
	for kind, opening := range entryKindTurnOpening {
		_, labelClaimsNoProducer := probeExemptTurnOpenings[opening]
		if labelClaimsNoProducer && !kindsNothingDispatches[kind] {
			t.Fatalf("EntryKind %d is labelled %q, which claims nothing dispatches it, but "+
				"kindsNothingDispatches does not name it: excusing a kind from the behaviour probe takes "+
				"both, so that it is a decision rather than a side effect of editing one row", kind, opening)
		}
		if !labelClaimsNoProducer && kindsNothingDispatches[kind] {
			t.Fatalf("EntryKind %d is excused from the behaviour probe but is labelled %q, which claims a "+
				"behaviour the probe can observe: probe it, or say why nothing dispatches it", kind, opening)
		}
	}
	for kind := range kindsNothingDispatches {
		if _, ok := entryKindTurnOpening[kind]; !ok {
			t.Fatalf("kindsNothingDispatches names EntryKind %d, which the table does not: a stale "+
				"exemption outlives the kind it was written for and silently excuses whatever takes "+
				"that number next", kind)
		}
	}
}
