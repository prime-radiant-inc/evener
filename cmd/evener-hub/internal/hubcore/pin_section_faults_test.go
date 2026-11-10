package hubcore

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// A PinSectionStore write runs in one transaction, retried on a fresh
// connection while SQLite reports a lock conflict. The tables below inject a
// fault at each step of each write and pin what the user gets: a transient
// lock is retried until the write lands, a lock that never clears gives up
// after a bounded number of attempts, and any other failure is returned at
// once with the transaction rolled back.

// pinWriteOp is one store write, with how to tell that its effect landed.
type pinWriteOp struct {
	name   string
	run    func(store *PinSectionStore, research PinSection) error
	landed func(state pinStoreState, research PinSection) bool
}

// pinStoreState is everything a client can read back from a store.
type pinStoreState struct {
	sections []PinSection
	pins     map[ArchiveKey]SessionPin
}

func readPinStoreState(t *testing.T, dbPath string) pinStoreState {
	t.Helper()
	plain := NewPinSectionStore(dbPath)
	sections, err := plain.Sections()
	if err != nil {
		t.Fatal(err)
	}
	pins, err := plain.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	return pinStoreState{sections: sections, pins: pins}
}

func (s pinStoreState) section(id string) (PinSection, bool) {
	for _, section := range s.sections {
		if section.ID == id {
			return section, true
		}
	}
	return PinSection{}, false
}

func (s pinStoreState) sectionNamed(name string) (PinSection, bool) {
	for _, section := range s.sections {
		if section.Name == name {
			return section, true
		}
	}
	return PinSection{}, false
}

var pinWriteOps = []pinWriteOp{
	{
		name: "assign",
		run: func(store *PinSectionStore, research PinSection) error {
			_, _, err := store.Assign(research.ID, "", "session-x", time.Unix(2, 0))
			return err
		},
		landed: func(state pinStoreState, research PinSection) bool {
			return state.pins[SessionPinKey("", "session-x")].SectionID == research.ID
		},
	},
	{
		name: "create or reuse and assign",
		run: func(store *PinSectionStore, _ PinSection) error {
			_, _, err := store.CreateOrReuseAndAssign("Fresh", "", "session-x", time.Unix(2, 0))
			return err
		},
		landed: func(state pinStoreState, _ PinSection) bool {
			fresh, ok := state.sectionNamed("Fresh")
			return ok && state.pins[SessionPinKey("", "session-x")].SectionID == fresh.ID
		},
	},
	{
		name: "rename",
		run: func(store *PinSectionStore, research PinSection) error {
			_, _, err := store.Rename(research.ID, "Renamed", time.Unix(2, 0))
			return err
		},
		landed: func(state pinStoreState, research PinSection) bool {
			renamed, ok := state.section(research.ID)
			return ok && renamed.Name == "Renamed"
		},
	},
	{
		name: "delete section",
		run: func(store *PinSectionStore, research PinSection) error {
			_, _, err := store.DeleteSection(research.ID)
			return err
		},
		landed: func(state pinStoreState, research PinSection) bool {
			_, kept := state.section(research.ID)
			_, pinned := state.pins[SessionPinKey("", "seed-a")]
			return !kept && !pinned
		},
	},
	{
		name: "delete session",
		run: func(store *PinSectionStore, _ PinSection) error {
			_, err := store.DeleteSession("", "seed-a")
			return err
		},
		landed: func(state pinStoreState, _ PinSection) bool {
			_, pinned := state.pins[SessionPinKey("", "seed-a")]
			return !pinned
		},
	},
}

func pinWriteOpNamed(t *testing.T, name string) pinWriteOp {
	t.Helper()
	for _, op := range pinWriteOps {
		if op.name == name {
			return op
		}
	}
	t.Fatalf("no pin write op %q", name)
	return pinWriteOp{}
}

// seedResearchSection adds a "Research" section holding seed-a through a
// store with no injected faults.
func seedResearchSection(t *testing.T, dbPath string) PinSection {
	t.Helper()
	research, _, err := NewPinSectionStore(dbPath).CreateOrReuseAndAssign("Research", "", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return research
}

func TestPinSectionStoreWriteRetriesATransientLock(t *testing.T) {
	type lockPoint struct {
		name   string
		inject func()
	}
	begin := lockPoint{"begin", func() { lockedBeginFailures.Store(1) }}
	query := lockPoint{"first query", func() { lockedTxQueryFails.Store(1) }}
	exec := lockPoint{"first exec", func() { lockedTxExecFails.Store(1) }}
	commit := lockPoint{"commit", func() { lockedTxCommitFails.Store(1) }}
	for _, tc := range []struct {
		op     string
		points []lockPoint
	}{
		{"assign", []lockPoint{begin, query, exec, commit}},
		{"create or reuse and assign", []lockPoint{begin, query, exec, commit}},
		{"rename", []lockPoint{begin, query, exec, commit}},
		{"delete section", []lockPoint{begin, query, exec, commit}},
		{"delete session", []lockPoint{begin, exec, commit}},
	} {
		op := pinWriteOpNamed(t, tc.op)
		for _, point := range tc.points {
			t.Run(tc.op+"/"+point.name, func(t *testing.T) {
				store := setupLockedStore(t)
				research := seedResearchSection(t, store.dbPath)
				point.inject()
				if err := op.run(store, research); err != nil {
					t.Fatalf("%s after one lock at %s: %v, want the retry to land the write", tc.op, point.name, err)
				}
				if !op.landed(readPinStoreState(t, store.dbPath), research) {
					t.Fatalf("%s reported success after a retried lock but its effect is not in the store", tc.op)
				}
			})
		}
	}
}

func TestPinSectionStoreWriteGivesUpOnAPersistentLock(t *testing.T) {
	const lockedBegins = 100
	for _, op := range pinWriteOps {
		t.Run(op.name, func(t *testing.T) {
			store := setupLockedStore(t)
			research := seedResearchSection(t, store.dbPath)
			before := readPinStoreState(t, store.dbPath)
			lockedBeginFailures.Store(lockedBegins)
			err := op.run(store, research)
			attempts := lockedBegins - lockedBeginFailures.Load()
			resetLockedCounters()
			if err == nil {
				t.Fatalf("%s succeeded while every transaction begin was locked", op.name)
			}
			if attempts <= 1 || attempts >= lockedBegins {
				t.Fatalf("%s made %d attempts, want a bounded retry", op.name, attempts)
			}
			if after := readPinStoreState(t, store.dbPath); !reflect.DeepEqual(after, before) {
				t.Fatalf("%s changed the store without committing: before %+v, after %+v", op.name, before, after)
			}
		})
	}
}

func TestPinSectionStoreWriteReturnsANonRetryableFailureAndRollsBack(t *testing.T) {
	type faultPoint struct {
		name   string
		inject func()
	}
	query := func(n int64, step string) faultPoint {
		return faultPoint{step, func() { errorQueryTarget.Store(n) }}
	}
	exec := func(n int64, step string) faultPoint {
		return faultPoint{step, func() { errorExecTarget.Store(n) }}
	}
	rowsAffected := faultPoint{"rows affected", func() { errorResultTarget.Store(1) }}
	for _, tc := range []struct {
		op     string
		points []faultPoint
	}{
		{"assign", []faultPoint{
			query(1, "section lookup"), exec(1, "pin upsert"), rowsAffected, query(2, "member count"),
		}},
		{"create or reuse and assign", []faultPoint{
			query(1, "section lookup by name"), exec(2, "pin upsert"), query(2, "member count"),
		}},
		{"rename", []faultPoint{
			query(1, "section lookup"), query(2, "member count"), query(3, "name conflict lookup"),
			exec(1, "update"), query(4, "updated section read"),
		}},
		{"delete section", []faultPoint{
			query(1, "section lookup"), query(2, "member count"), exec(1, "delete"),
		}},
		{"delete session", []faultPoint{
			exec(1, "delete"), rowsAffected,
		}},
	} {
		op := pinWriteOpNamed(t, tc.op)
		for _, point := range tc.points {
			t.Run(tc.op+"/"+point.name, func(t *testing.T) {
				store := setupErrorStore(t)
				research := seedResearchSection(t, store.dbPath)
				before := readPinStoreState(t, store.dbPath)
				resetErrorCounters()
				t.Cleanup(resetErrorCounters)
				point.inject()
				err := op.run(store, research)
				resetErrorCounters()
				if !errors.Is(err, errInjectedFault) {
					t.Fatalf("%s with a failing %s = %v, want the failure returned as is", tc.op, point.name, err)
				}
				if after := readPinStoreState(t, store.dbPath); !reflect.DeepEqual(after, before) {
					t.Fatalf("%s left a partial write after a failing %s: before %+v, after %+v", tc.op, point.name, before, after)
				}
			})
		}
	}
}
