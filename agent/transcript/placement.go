package transcript

import (
	"fmt"

	"primeradiant.com/evener/agent/schema"
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

// openTurn is a turn entries can still join: its TurnID, "" when there is
// none, and whether its TurnKind is still to be recorded on its next entry.
type openTurn struct {
	id    string
	fresh bool
}

// join stamps turn into t, with kind if it is t's first recorded entry.
func (t *openTurn) join(turn *schema.Turn, kind schema.TurnSpanKind) {
	stamp(turn, t.id, "")
	if t.fresh {
		turn.TurnKind = kind
		t.fresh = false
	}
}

func stamp(turn *schema.Turn, turnID string, kind schema.TurnSpanKind) {
	turn.Format = schema.TurnFormatIdentity
	turn.TurnID = turnID
	turn.TurnKind = kind
}

// turnPlacement is the append tail's turn state for its file.
type turnPlacement struct {
	// running is the running execution; a reopened execution is not fresh.
	running openTurn
	// gap is the open gap turn.
	gap openTurn
	// prelude, while its id is set, is the prelude of a fresh transcript that
	// has not begun its first execution.
	prelude openTurn
}

// place stamps turn for p against state and advances state as if the entry
// were recorded. The caller keeps the advanced state only if it is. skip
// reports a completion with nothing to complete.
func (state *turnPlacement) place(turn *schema.Turn, p Placement) (skip bool, err error) {
	switch p.mode {
	case placeVerbatim:
		return false, nil
	case placeCompletion:
		if state.running.id == "" || state.running.fresh {
			// Nothing ran, or nothing it did was recorded: no turn to end.
			state.running = openTurn{}
			return true, nil
		}
		state.running.join(turn, schema.TurnSpanExecution)
		state.running = openTurn{}
	case placeInTurn:
		stamp(turn, p.turnID, "")
	case placeAsync, placeDelivery:
		if p.mode == placeAsync && state.running.id != "" {
			state.running.join(turn, schema.TurnSpanExecution)
			break
		}
		if err := stampDelivery(turn); err != nil {
			return false, err
		}
	case placeSession:
		switch {
		case state.running.id != "":
			state.running.join(turn, schema.TurnSpanExecution)
		case state.prelude.id != "":
			state.prelude.join(turn, schema.TurnSpanPrelude)
		default:
			if state.gap.id == "" {
				id, err := newTurnID()
				if err != nil {
					return false, err
				}
				state.gap = openTurn{id: id, fresh: true}
			}
			state.gap.join(turn, schema.TurnSpanGap)
			return false, nil // the one entry that keeps the gap open
		}
	}
	// Any entry of another turn closes the open gap.
	state.gap = openTurn{}
	return false, nil
}

// stampDelivery stamps turn as a delivery turn of its own.
func stampDelivery(turn *schema.Turn) error {
	id, err := newTurnID()
	if err != nil {
		return err
	}
	stamp(turn, id, schema.TurnSpanDelivery)
	return nil
}

func newTurnID() (string, error) {
	id, err := identifier.NewTurnID()
	if err != nil {
		return "", fmt.Errorf("mint transcript turn id: %w", err)
	}
	return id, nil
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
	w.tail.turns = turnPlacement{running: openTurn{id: turnID, fresh: !reopen}}
}

// RunningTurnID is the running execution's TurnID for the file, "" when none
// runs. Nil-safe.
func (w *Writer) RunningTurnID() string {
	if w == nil || w.tail == nil {
		return ""
	}
	w.tail.mu.Lock()
	defer w.tail.mu.Unlock()
	return w.tail.turns.running.id
}
