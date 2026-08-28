package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestThreadClearRequestHash pins the property the "client mutation ID reused
// with a different clear request" guard is built on: equal params hash equal
// and different params hash different. The error return is unreachable for
// the current all-strings params struct, so only the nil-error path runs here;
// it exists so an encoding failure surfaces instead of collapsing every
// request onto the hash of nil.
func TestThreadClearRequestHash(t *testing.T) {
	base := appwire.ThreadClearParams{Ref: "local:a", ClientMutationID: "clear-1", ExpectedInstanceID: "old"}
	first, err := threadClearRequestHash(base)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first == "" {
		t.Fatal("hash is empty")
	}
	same, err := threadClearRequestHash(base)
	if err != nil {
		t.Fatalf("hash of equal params: %v", err)
	}
	if same != first {
		t.Fatalf("equal params hashed differently: %q vs %q", first, same)
	}
	changed := base
	changed.ExpectedInstanceID = "new"
	different, err := threadClearRequestHash(changed)
	if err != nil {
		t.Fatalf("hash of different params: %v", err)
	}
	if different == first {
		t.Fatal("different params hashed identically, so the reuse guard cannot fire")
	}
}
