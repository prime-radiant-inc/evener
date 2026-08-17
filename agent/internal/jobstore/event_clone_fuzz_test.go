//go:build serffuzz

package jobstore

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/fuzz/aliascheck"
)

// FuzzCloneEventSharesNoMutableState drives cloneEvent against the bug a
// hand-written deep copy actually has: a forgotten field.
//
// cloneEvent names eight fields explicitly and copies the rest by assignment,
// and cloneCausal, cloneWatchSend and cloneWatchEvent do the same again below
// it. Add a pointer or slice field to any of those structs, forget the
// corresponding line, and everything still compiles and still passes an
// equality test — the clone simply shares the original's memory. Cursor-held
// events are handed to callers that mutate them, so the symptom is one reader's
// edit appearing in another's event, long after the fact and nowhere near this
// file.
//
// See fuzz/aliascheck for why the oracle is structural and the values are
// reflected rather than hand-built.
func FuzzCloneEventSharesNoMutableState(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Add(uint8(7))

	f.Fuzz(func(t *testing.T, variant uint8) {
		var event Event
		aliascheck.Populate(reflect.ValueOf(&event).Elem(), int(variant%4)+2)

		clone := cloneEvent(event)

		if !reflect.DeepEqual(event, clone) {
			t.Fatal("cloneEvent produced a value that is not equal to its input")
		}
		if shared := aliascheck.FindSharedStorage(reflect.ValueOf(event), reflect.ValueOf(clone), "Event"); shared != "" {
			t.Fatal(shared)
		}
	})
}
