package hostreg

import (
	"errors"
	"slices"
	"testing"
)

// TestRegistryUpdateReplacesInPlaceAndAdvancesGeneration pins the identity
// fence every capture-compare consumer reads: an update replaces the entry in
// place, stamps a strictly greater generation on each successive update, and
// makes SameRegistration refuse a capture taken before it.
func TestRegistryUpdateReplacesInPlaceAndAdvancesGeneration(t *testing.T) {
	r, err := New([]Host{{Name: "m4", SSH: "m4.example", Roots: []string{"/a"}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before, ok := r.Get("m4")
	if !ok {
		t.Fatal("m4 not registered after New")
	}
	if err := r.Update(Host{Name: "m4", SSH: "m4b.example", Roots: []string{"/a", "/b"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	first, ok := r.Get("m4")
	if !ok {
		t.Fatal("m4 gone after Update")
	}
	if first.SSH != "m4b.example" || !slices.Equal(first.Roots, []string{"/a", "/b"}) {
		t.Fatalf("entry = %+v, want the edited values", first)
	}
	if first.Generation <= before.Generation {
		t.Fatalf("generation = %d after updating %d, want a strictly greater one", first.Generation, before.Generation)
	}
	if r.SameRegistration("m4", before) {
		t.Fatal("a capture from before the update still matches; the fence is open")
	}
	if err := r.Update(Host{Name: "m4", SSH: "m4c.example"}); err != nil {
		t.Fatalf("second Update: %v", err)
	}
	second, ok := r.Get("m4")
	if !ok {
		t.Fatal("m4 gone after the second Update")
	}
	if second.Generation <= first.Generation {
		t.Fatalf("second update generation = %d, want > %d", second.Generation, first.Generation)
	}
	if r.SameRegistration("m4", first) {
		t.Fatal("a capture from the first update still matches the second's entry")
	}
	if got := len(r.All()); got != 1 {
		t.Fatalf("All() = %d hosts, want 1: an update replaces, never inserts", got)
	}
}

// TestRegistryUpdateRefusalsLeaveTheRegistryUntouched pins that every refusal
// the add path produces is a refusal here too, with nothing changed.
func TestRegistryUpdateRefusalsLeaveTheRegistryUntouched(t *testing.T) {
	r, err := New([]Host{{Name: "m4", SSH: "m4.example", KeyPath: "/keys/m4"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before, _ := r.Get("m4")
	for _, tc := range []struct {
		name  string
		entry Host
		want  error
	}{
		{"unknown name", Host{Name: "nope", SSH: "n.example"}, ErrUnknownHost},
		{"missing ssh", Host{Name: "m4", SSH: "   "}, ErrMissingSSH},
		{"invalid name", Host{Name: "bad name", SSH: "n.example"}, ErrInvalidName},
		{"reserved name", Host{Name: ReservedName, SSH: "n.example"}, ErrReservedName},
		{"empty root", Host{Name: "m4", SSH: "n.example", Roots: []string{"   "}}, ErrEmptyRoot},
		{"ambiguous user", Host{Name: "m4", SSH: "u@n.example", User: "bob"}, ErrAmbiguousSSHUser},
	} {
		if err := r.Update(tc.entry); !errors.Is(err, tc.want) {
			t.Errorf("Update(%s) = %v, want %v", tc.name, err, tc.want)
		}
	}
	after, ok := r.Get("m4")
	if !ok {
		t.Fatal("a refused update dropped the entry")
	}
	if !after.Equal(before) || after.Generation != before.Generation {
		t.Fatalf("entry after the refusals = %+v, want %+v untouched", after, before)
	}
}

// TestRegistryUpdatePreservesUpstreamEdges pins that an edit keeps what the
// attach handshake learned about the host's position in the graph: the edge a
// cycle would close through survives the update.
func TestRegistryUpdatePreservesUpstreamEdges(t *testing.T) {
	r, err := New([]Host{{Name: "a", SSH: "a.example"}, {Name: "b", SSH: "b.example"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// b is downstream of a, so giving a b as an upstream closes a cycle.
	if err := r.SetUpstreams("b", []string{"a"}); err != nil {
		t.Fatalf("SetUpstreams(b): %v", err)
	}
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("SetUpstreams(a) = %v, want ErrHostCycle", err)
	}
	if err := r.Update(Host{Name: "b", SSH: "b2.example"}); err != nil {
		t.Fatalf("Update(b): %v", err)
	}
	if err := r.SetUpstreams("a", []string{"b"}); !errors.Is(err, ErrHostCycle) {
		t.Fatalf("after the update SetUpstreams(a) = %v, want ErrHostCycle: the update dropped b's upstream edge", err)
	}
}

// TestRegistryUpdateStoresWhatItValidated pins the normalize-then-store
// discipline: a padded entry is stored trimmed, so the value the registry
// validated is the value every consumer reads.
func TestRegistryUpdateStoresWhatItValidated(t *testing.T) {
	r, err := New([]Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := r.Update(Host{
		Name:  "  m4  ",
		SSH:   "  m4b.example  ",
		User:  "  operator  ",
		Roots: []string{"  /a  ", " /b "},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, ok := r.Get("m4")
	if !ok {
		t.Fatal("a padded name did not find its host")
	}
	want := Normalize(Host{Name: "m4", SSH: "m4b.example", User: "operator", Roots: []string{"/a", "/b"}})
	if !got.Equal(want) {
		t.Fatalf("stored entry = %+v, want %+v", got, want)
	}
	if len(got.Roots) > 0 {
		got.Roots[0] = "/mutated"
		again, _ := r.Get("m4")
		if again.Roots[0] != "/a" {
			t.Fatal("Get handed out a slice aliased by registry state")
		}
	}
}
