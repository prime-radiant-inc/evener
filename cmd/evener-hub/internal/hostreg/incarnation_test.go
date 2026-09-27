package hostreg

import (
	"testing"

	"github.com/google/uuid"
)

// TestIncarnationIDMintedOnAddStableAcrossUpdateAndRotatedOnReAdd pins spec 08
// §1's minting and rotation rules: "The opaque server-generated string minted
// beside the generation on every add/re-add, never derived from it and never
// reused: at most 128 bytes, and the generator pins its output to 36 bytes
// (canonical UUID text)". §4 adds the rotation's exact span: "the incarnation
// id is minted fresh on every add/re-add in the same atomic hub.toml write that
// mints the generation". An update advances the generation but is not an
// add/re-add, so it keeps the incarnation it replaces.
func TestIncarnationIDMintedOnAddStableAcrossUpdateAndRotatedOnReAdd(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if err := r.Add(Host{Name: "a", SSH: "a.example"}); err != nil {
		t.Fatalf("Add(a): %v", err)
	}
	first, ok := r.Get("a")
	if !ok {
		t.Fatal("Add(a) left the host unregistered")
	}
	if len(first.IncarnationID) != 36 {
		t.Fatalf("incarnation id %q is %d bytes, want the generator's pinned 36", first.IncarnationID, len(first.IncarnationID))
	}
	if _, err := uuid.Parse(first.IncarnationID); err != nil {
		t.Fatalf("incarnation id %q is not canonical UUID text: %v", first.IncarnationID, err)
	}

	// An update advances the generation and keeps the incarnation: nothing was
	// re-added, so nothing may rotate the never-reused id.
	if err := r.Update(Host{Name: "a", SSH: "a2.example"}); err != nil {
		t.Fatalf("Update(a): %v", err)
	}
	updated, _ := r.Get("a")
	if updated.IncarnationID != first.IncarnationID {
		t.Fatalf("update rotated the incarnation id %q -> %q; only add/re-add mints", first.IncarnationID, updated.IncarnationID)
	}
	if updated.Generation <= first.Generation {
		t.Fatalf("update generation = %d, want greater than %d", updated.Generation, first.Generation)
	}

	// A re-add mints a fresh incarnation id: the name's new entry shares the
	// name with the removed one but must never share its identity.
	if err := r.Remove("a"); err != nil {
		t.Fatalf("Remove(a): %v", err)
	}
	if err := r.Add(Host{Name: "a", SSH: "a.example"}); err != nil {
		t.Fatalf("re-Add(a): %v", err)
	}
	readded, _ := r.Get("a")
	if readded.IncarnationID == first.IncarnationID {
		t.Fatalf("re-add reused the removed incarnation id %q", first.IncarnationID)
	}
	if err := r.Add(Host{Name: "b", SSH: "b.example"}); err != nil {
		t.Fatalf("Add(b): %v", err)
	}
	other, _ := r.Get("b")
	if other.IncarnationID == first.IncarnationID || other.IncarnationID == readded.IncarnationID {
		t.Fatalf("distinct names share incarnation ids: %q, %q, %q", first.IncarnationID, readded.IncarnationID, other.IncarnationID)
	}
}

// TestPresenceEpochAdvancesExactlyOncePerPresenceEvent pins spec 08 §1: "The
// per-host monotonic removal/presence counter the file advances on every add,
// remove, re-add, and expiry purge." One add, one remove, one re-add — each
// advances the name's counter by exactly one; an update is not a presence
// event and leaves it alone.
func TestPresenceEpochAdvancesExactlyOncePerPresenceEvent(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if err := r.Add(Host{Name: "a", SSH: "a.example"}); err != nil {
		t.Fatalf("Add(a): %v", err)
	}
	first, _ := r.Get("a")
	if first.PresenceEpoch != 1 {
		t.Fatalf("a fresh add's presence epoch = %d, want 1", first.PresenceEpoch)
	}
	if err := r.Update(Host{Name: "a", SSH: "a2.example"}); err != nil {
		t.Fatalf("Update(a): %v", err)
	}
	updated, _ := r.Get("a")
	if updated.PresenceEpoch != first.PresenceEpoch {
		t.Fatalf("update advanced the presence epoch %d -> %d; only add/remove/re-add/purge advance it",
			first.PresenceEpoch, updated.PresenceEpoch)
	}
	// The durable-first removal records the advance before the registry's own
	// Remove runs, with the one arithmetic NextPresenceEpoch holds.
	advanced := r.NextPresenceEpoch("a")
	if advanced != first.PresenceEpoch+1 {
		t.Fatalf("NextPresenceEpoch(a) = %d, want the removed event's %d", advanced, first.PresenceEpoch+1)
	}
	if err := r.Remove("a"); err != nil {
		t.Fatalf("Remove(a): %v", err)
	}
	if next := r.NextPresenceEpoch("a"); next != advanced+1 {
		t.Fatalf("the event after the remove advances to %d, want %d", next, advanced+1)
	}
	if err := r.Add(Host{Name: "a", SSH: "a.example"}); err != nil {
		t.Fatalf("re-Add(a): %v", err)
	}
	readded, _ := r.Get("a")
	if readded.PresenceEpoch != advanced+1 {
		t.Fatalf("a re-add advanced the presence epoch to %d, want %d", readded.PresenceEpoch, advanced+1)
	}
}

// TestSeedHighWaterRaisesTheCountersOnly pins the boot seeding rule: the marks
// a hub.toml file retains for names with no live entry raise the registry's
// counters — the registry-wide generation and the name's presence epoch — so a
// re-add mints strictly above them (spec 08 §1), while a mark below a counter
// the registry already reached changes nothing.
func TestSeedHighWaterRaisesTheCountersOnly(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil): %v", err)
	}
	if err := r.Add(Host{Name: "live", SSH: "live.example"}); err != nil {
		t.Fatalf("Add(live): %v", err)
	}
	r.SeedHighWater(map[string]HighWater{
		"gone": {Generation: 9, PresenceEpoch: 4},
		"live": {Generation: 1, PresenceEpoch: 1},
	})
	if err := r.Add(Host{Name: "gone", SSH: "gone.example"}); err != nil {
		t.Fatalf("re-Add(gone): %v", err)
	}
	reAdded, _ := r.Get("gone")
	if reAdded.Generation <= 9 {
		t.Fatalf("re-added generation = %d, want above the seeded mark 9", reAdded.Generation)
	}
	if reAdded.PresenceEpoch <= 4 {
		t.Fatalf("re-added presence epoch = %d, want above the seeded mark 4", reAdded.PresenceEpoch)
	}
	// A mark at or below the counters leaves them alone: the live name's next
	// presence event still advances one past its own value.
	if next := r.NextPresenceEpoch("live"); next != 2 {
		t.Fatalf("NextPresenceEpoch(live) after a lower seed = %d, want 2", next)
	}
}

// TestNewSeedsPersistedRecordsAndMintsWhatIsAbsent pins the boot load's half of
// the durability story: a hub.toml entry that carries a persisted (generation,
// incarnation id, presence epoch) triple keeps it, the registry counter moves
// above the restored generation so the next mint never reuses it (spec 08 §15:
// "The boot load restores persisted generations before the store serves any
// request"), and an entry with no persisted record gets its initial pair —
// a fresh incarnation id and presence epoch 1 — rather than a zero pair the
// file could never represent.
func TestNewSeedsPersistedRecordsAndMintsWhatIsAbsent(t *testing.T) {
	seeded, err := New([]Host{
		{Name: "h", SSH: "h.example", Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 3},
	})
	if err != nil {
		t.Fatalf("New(seeded): %v", err)
	}
	got, ok := seeded.Get("h")
	if !ok {
		t.Fatal("New dropped the seeded host")
	}
	if got.Generation != 7 || got.IncarnationID != "inc-7" || got.PresenceEpoch != 3 {
		t.Fatalf("seeded entry = %+v, want the persisted (7, inc-7, 3) triple", got)
	}
	if err := seeded.Add(Host{Name: "later", SSH: "later.example"}); err != nil {
		t.Fatalf("Add(later): %v", err)
	}
	later, _ := seeded.Get("later")
	if later.Generation <= got.Generation {
		t.Fatalf("a mint after the restored generation 7 = %d, want above it", later.Generation)
	}

	legacy, err := New([]Host{{Name: "legacy", SSH: "legacy.example"}})
	if err != nil {
		t.Fatalf("New(legacy): %v", err)
	}
	minted, _ := legacy.Get("legacy")
	if minted.Generation != 1 {
		t.Fatalf("a hub.toml host with no persisted mark got generation %d, want the initial 1", minted.Generation)
	}
	if len(minted.IncarnationID) != 36 {
		t.Fatalf("a hub.toml host with no persisted incarnation got %q, want a fresh 36-byte one", minted.IncarnationID)
	}
	if minted.PresenceEpoch != 1 {
		t.Fatalf("a hub.toml host with no persisted presence epoch got %d, want the first value 1", minted.PresenceEpoch)
	}
	// The seeded counter is not lowered by a later removal: the name's epoch
	// advances from the value the file restored.
	if err := legacy.Remove("legacy"); err != nil {
		t.Fatalf("Remove(legacy): %v", err)
	}
	if next := legacy.NextPresenceEpoch("legacy"); next != 3 {
		t.Fatalf("after the first removal of a loaded host the next epoch = %d, want 3", next)
	}
}
