//go:build evenerfuzz

package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/fuzz/aliascheck"
)

// FuzzAgentClonesShareNoMutableState holds this package's hand-written deep
// copies to one rule: the copy shares no mutable memory with its original.
//
// These functions exist because the values they copy are handed to callers that
// mutate them — a snapshot taken for persistence while the live session keeps
// running, a record projected for one reader while another advances it. Each
// names its pointers, slices and maps explicitly and copies the rest by
// assignment, so adding a field and forgetting its line leaves the two sharing
// memory. Nothing fails at that moment: the copy is still equal to its original,
// still round-trips, still serialises. The bug surfaces later as one holder's
// write appearing in another's value, with nothing pointing back here.
//
// Grouping them is deliberate. The rule is identical for all of them and the
// oracle is structural (see fuzz/aliascheck), so a clone function added to this
// package can be listed here in four lines and needs no oracle of its own.
//
// NOT listed: cloneActivityRecord. It copies three pointer fields and leaves
// jobstore.JobRecord.StructuredResult — an `any` that holds a decoded JSON
// container — shared with the original. jobstore's own cloneEvent deep-copies
// that same field through cloneJSONValue, so the two disagree about the same
// data. Whether the activity path needs the deeper copy is a product question
// about who mutates a projected record, not something to settle by asserting
// either answer here.
func FuzzAgentClonesShareNoMutableState(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(3))
	f.Add(uint8(6))

	f.Fuzz(func(t *testing.T, variant uint8) {
		depth := int(variant%4) + 2

		for _, tc := range []struct {
			name string
			// run populates a fresh zero value to depth, copies it, and returns
			// both for comparison.
			run func(depth int) (original, copied any)
			// normalizes marks a copy that deliberately does not round-trip:
			// cloneDelegateStartDescriptor routes provenance through
			// provenance.Clone, which nils an empty value by design, so equality
			// is the wrong assertion for it. Aliasing is still checked.
			normalizes bool
		}{
			{name: "client mutation record", run: func(d int) (any, any) {
				var v clientMutationRecord
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, cloneClientMutationRecord(v)
			}},
			{name: "client mutation preconditions", run: func(d int) (any, any) {
				var v clientMutationPreconditions
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, cloneClientMutationPreconditions(v)
			}},
			{name: "delegate terminal packet", run: func(d int) (any, any) {
				var v delegatestore.TerminalPacket
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, cloneDelegateTerminalPacket(v)
			}},
			{name: "watch event filter", run: func(d int) (any, any) {
				var v watchEventFilter
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, *cloneWatchEventFilter(&v)
			}},
			{name: "watch send args", run: func(d int) (any, any) {
				var v watchSendArgs
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, *cloneWatchSendArgs(&v)
			}},
			{name: "client mutation snapshot", normalizes: true, run: func(d int) (any, any) {
				// cloneClientMutationInput rebuilds with make(len), turning a nil
				// input slice into an empty one.
				var v clientMutationSnapshot
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, cloneClientMutationSnapshot(v)
			}},
			{name: "delegate start descriptor", normalizes: true, run: func(d int) (any, any) {
				var v delegatestore.Descriptor
				aliascheck.Populate(reflect.ValueOf(&v).Elem(), d)
				return v, cloneDelegateStartDescriptor(v)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				original, copied := tc.run(depth)
				if !tc.normalizes && !reflect.DeepEqual(original, copied) {
					t.Fatalf("%s: copy is not equal to its original", tc.name)
				}
				if shared := aliascheck.FindSharedStorage(
					reflect.ValueOf(original), reflect.ValueOf(copied), tc.name); shared != "" {
					t.Fatal(shared)
				}
			})
		}
	})
}
