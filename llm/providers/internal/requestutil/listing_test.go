package requestutil

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestNormalizeReasoningEfforts pins the ladder normalization once at its
// source: trim, drop empties and duplicates, drop `ultra` (a Codex client
// delegation preset, not a wire effort), keep served order. Both /models
// readers route through it, so their own tests pin only their decode shapes.
func TestNormalizeReasoningEfforts(t *testing.T) {
	t.Parallel()
	got := NormalizeReasoningEfforts([]string{" high ", "", "ultra", "Ultra", "high", "none"})
	if want := []string{"high", "none"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if NormalizeReasoningEfforts(nil) != nil {
		t.Fatal("no levels must stay nil")
	}
	if got := NormalizeDefaultReasoningEffort(" Ultra "); got != "" {
		t.Fatalf("ultra default = %q, want dropped", got)
	}
	if got := NormalizeDefaultReasoningEffort(" high "); got != "high" {
		t.Fatalf("default = %q, want high", got)
	}
	if got := NormalizeDefaultReasoningEffort("   "); got != "" {
		t.Fatalf("blank default = %q, want dropped", got)
	}
}

// TestDecodeListingRows pins the decode helper both /models readers share:
// decodable rows survive an alien sibling, and the first failure surfaces
// for the caller's nothing-survived guard.
func TestDecodeListingRows(t *testing.T) {
	t.Parallel()
	type row struct {
		ID int `json:"id"`
	}
	rows, firstErr := DecodeListingRows[row]([]json.RawMessage{
		json.RawMessage(`{"id":1}`),
		json.RawMessage(`{"id":"alien"}`),
		json.RawMessage(`{"id":3}`),
	})
	if len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 3 {
		t.Fatalf("rows = %+v, want the two decodable rows", rows)
	}
	if firstErr == nil {
		t.Fatal("the alien row's decode error must surface")
	}
	none, err := DecodeListingRows[row]([]json.RawMessage{json.RawMessage(`"x"`)})
	if none != nil || err == nil {
		t.Fatalf("rows = %+v err = %v, want nothing decodable with the error kept", none, err)
	}
}
