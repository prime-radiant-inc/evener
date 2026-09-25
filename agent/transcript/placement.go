package transcript

import (
	"fmt"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// Placement names the turn an appended entry joins. The append tail resolves
// it under the append lock, against the running execution, the open gap turn
// and the prelude it holds for the file, so the choice and the append are one
// step: a write that loses a race to a completion sees the completion.
//
// Every placement except PlaceVerbatim stamps the entry with the identity
// format marker and its TurnID, and stamps TurnKind on the first recorded
// entry of a turn.
type Placement struct {
	mode   placementMode
	turnID string
}

type placementMode int

const (
	placeVerbatim placementMode = iota
	placeSession
	placeAsync
	placeDelivery
	placeInTurn
	placeCompletion
)

var (
	// PlaceVerbatim writes the turn exactly as given: copies of entries
	// already recorded elsewhere (a fork's prefix, a delegate's inherited
	// context), which keep the identity they were recorded with. It is the
	// zero Placement.
	PlaceVerbatim = Placement{}
	// PlaceSession places a session's own write: the running execution, else
	// the prelude of a fresh transcript before its first execution, else the
	// open gap turn, minting one when none is open.
	PlaceSession = Placement{mode: placeSession}
	// PlaceAsync places a session's asynchronous write — attention delivered
	// from another goroutine: the running execution, else a delivery turn of
	// its own. It never joins a turn whose completion is already recorded.
	PlaceAsync = Placement{mode: placeAsync}
	// PlaceDelivery places a cold writer's entry, which has no session: a
	// delivery turn of its own.
	PlaceDelivery = Placement{mode: placeDelivery}
	// PlaceCompletion places the running execution's completion entry and
	// ends the execution. With no execution running, or one that recorded
	// nothing, there is nothing to complete: the append records nothing.
	PlaceCompletion = Placement{mode: placeCompletion}
)

// PlaceInTurn places an entry in the named turn: resume's interrupted
// completion of an execution a crash left open.
func PlaceInTurn(turnID string) Placement {
	return Placement{mode: placeInTurn, turnID: turnID}
}

// turnPlacement is the append tail's turn state for its file.
type turnPlacement struct {
	// runningID is the running execution's TurnID, "" when none runs.
	// runningFresh reports that none of its entries is recorded yet, so the
	// next one carries its TurnKind; a reopened execution is never fresh.
	runningID    string
	runningFresh bool
	// gapID is the open gap turn, "" when none is open; gapFresh as above.
	gapID    string
	gapFresh bool
	// prelude reports that the file is a fresh transcript that has not begun
	// its first execution; preludeStamped that its TurnKind is recorded.
	prelude        bool
	preludeStamped bool
}

// place stamps turn for p against state and advances state as if the entry
// were recorded. The caller keeps the advanced state only if it is. skip
// reports a completion with nothing to complete.
func (state *turnPlacement) place(turn *schema.Turn, p Placement) (skip bool, err error) {
	if p.mode == placeVerbatim {
		return false, nil
	}
	stamp := func(turnID string, kind schema.TurnSpanKind) {
		turn.Format = schema.TurnFormatIdentity
		turn.TurnID = turnID
		turn.TurnKind = kind
	}
	fresh := func() (string, error) {
		id, err := identifier.NewTurnID()
		if err != nil {
			return "", fmt.Errorf("mint transcript turn id: %w", err)
		}
		return id, nil
	}
	// joinRunning stamps the entry into the running execution. Any entry of
	// another turn closes the open gap.
	joinRunning := func() {
		kind := schema.TurnSpanKind("")
		if state.runningFresh {
			kind = schema.TurnSpanExecution
		}
		stamp(state.runningID, kind)
		state.runningFresh = false
		state.gapID = ""
	}
	delivery := func() error {
		id, err := fresh()
		if err != nil {
			return err
		}
		stamp(id, schema.TurnSpanDelivery)
		state.gapID = ""
		return nil
	}
	switch p.mode {
	case placeCompletion:
		if state.runningID == "" || state.runningFresh {
			// Nothing ran, or nothing it did was recorded: no turn to end.
			state.runningID, state.runningFresh = "", false
			return true, nil
		}
		joinRunning()
		state.runningID = ""
	case placeInTurn:
		stamp(p.turnID, "")
		state.gapID = ""
	case placeDelivery:
		return false, delivery()
	case placeAsync:
		if state.runningID == "" {
			return false, delivery()
		}
		joinRunning()
	case placeSession:
		switch {
		case state.runningID != "":
			joinRunning()
		case state.prelude:
			kind := schema.TurnSpanKind("")
			if !state.preludeStamped {
				kind = schema.TurnSpanPrelude
			}
			stamp(appwire.SystemPreludeTurnID, kind)
			state.preludeStamped = true
		default:
			if state.gapID == "" {
				id, err := fresh()
				if err != nil {
					return false, err
				}
				state.gapID, state.gapFresh = id, true
			}
			kind := schema.TurnSpanKind("")
			if state.gapFresh {
				kind = schema.TurnSpanGap
			}
			stamp(state.gapID, kind)
			state.gapFresh = false
		}
	}
	return false, nil
}

// BeginExecution names the running execution for the file: from here until
// its completion is recorded, the session's writes join turn turnID, and so
// do asynchronous writes. It ends the prelude and closes the open gap. reopen
// marks a turn recovery reclaimed and runs again under its old ID: its
// TurnKind is already recorded, so no entry of the new span restamps it.
// Nil-safe, like the append doors.
func (w *Writer) BeginExecution(turnID string, reopen bool) {
	if w == nil || w.tail == nil {
		return
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	w.tail.turns = turnPlacement{runningID: turnID, runningFresh: !reopen}
}

// RunningTurnID is the running execution's TurnID for the file, "" when none
// runs. Nil-safe.
func (w *Writer) RunningTurnID() string {
	if w == nil || w.tail == nil {
		return ""
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	return w.tail.turns.runningID
}
