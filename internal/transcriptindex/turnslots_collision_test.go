package transcriptindex

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestTurnSlotTablePlacesCollidingIDsInProbeOrder pins the insertion walk
// against the one bug this table can hide: double-counting the probe offset.
// Two ids that hash to the same bucket must land on adjacent buckets. An
// insertion that skips a bucket leaves a gap that ends the probe sequence
// early, and every later lookup of the id behind the gap silently falls back
// to scanning.
//
// The oracle is turn-record bytes read, not the answer: a lookup that misses
// falls back to scanning and still returns the right slot, so the answer alone
// cannot see the gap. A table hit confirms the one record its bucket names; a
// scan fallback reads the whole turn table. The colliding pair sits at the
// start, so a backwards scan must read every later record to reach it.
func TestTurnSlotTablePlacesCollidingIDsInProbeOrder(t *testing.T) {
	// turn_8 and turn_46 collide at bucket 5 of the smallest table, capacity 64.
	const (
		first  = "turn_8"
		second = "turn_46"
		// filler keeps the table at its minimum capacity (2*n must not exceed
		// 64) while making a full scan far larger than one confirm read.
		filler = 30
	)
	if turnSlotHash(first)%turnSlotsMinCapacity != turnSlotHash(second)%turnSlotsMinCapacity {
		t.Fatalf("%s and %s no longer collide; pick a pair that does", first, second)
	}
	previous := recordBufferLimit
	recordBufferLimit = 1
	t.Cleanup(func() { recordBufferLimit = previous })

	lines := make([]fixtureLine, 0, 2*(filler+2))
	add := func(id string, seq int) {
		lines = append(lines,
			entryLine(opens(id, schema.TurnSpanExecution, user("open "+id))),
			entryLine(completion(id, schema.TurnCompleted, seq, 1)),
		)
	}
	add(first, 1)
	add(second, 2)
	for i := range filler {
		add(fmt.Sprintf("turn_c%d", i), 3+i)
	}
	path := writeFixture(t, fixture{header: transcript.Header{SessionID: "colliding_slots"}, lines: lines})
	x := openIndex(t, path, t.TempDir())
	t.Cleanup(func() { _ = x.Close() })

	if x.turnSlots == nil {
		t.Fatal("the build minted no slot table")
	}
	if x.turnSlots.capacity != turnSlotsMinCapacity {
		t.Fatalf("table capacity = %d, want the minimum %d for this fixture", x.turnSlots.capacity, turnSlotsMinCapacity)
	}
	turnsFile := &countingRecordFile{recordFile: x.turns.file}
	x.turns.file = turnsFile

	// A hit costs one confirm read. A miss scans the whole table, which is
	// filler+2 records and far over this ceiling.
	const confirmCeiling = 4 * turnRecordSize
	for want, id := range map[uint64]string{0: first, 1: second} {
		before := turnsFile.readBytes
		slot, found, err := x.turnSlotOf(id)
		if err != nil {
			t.Fatalf("turnSlotOf(%s): %v", id, err)
		}
		if !found || slot != want {
			t.Fatalf("turnSlotOf(%s) = %d, %v, want slot %d", id, slot, found, want)
		}
		if read := turnsFile.readBytes - before; read > confirmCeiling {
			t.Fatalf("turnSlotOf(%s) read %d bytes of turn records, over %d: the table did not name it, so the lookup scanned", id, read, confirmCeiling)
		}
	}
	// And the windows both name still resolve to their own turns.
	for _, id := range []string{first, second} {
		window, err := x.ResolveTurnWindow(appwire.ThreadTurnWindow{Turn: id})
		if err != nil {
			t.Fatalf("ResolveTurnWindow(%s): %v", id, err)
		}
		if len(window.TurnIDs) != 1 || window.TurnIDs[0] != id {
			t.Fatalf("window ids = %v, want [%s]", window.TurnIDs, id)
		}
	}
}
