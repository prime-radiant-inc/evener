package apphistory

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// FuzzHistoryContentCursor exercises arbitrary tokens and canonical cursors.
// Successful decodes must round-trip without changing source identity or the
// exclusive boundary, and must remain unusable with another content selection.
// Registry: native:.:./internal/apphistory:FuzzHistoryContentCursor
func FuzzHistoryContentCursor(f *testing.F) {
	f.Add("", uint8(0))
	f.Add(cursorPrefix+"invalid", uint8(2))
	for _, mask := range []uint8{1, 2, 30, 62, 63} {
		selection := appwire.ThreadContentSelection(mask)
		cursor := contentCursor{Selection: selection.Key(), Snapshot: appwire.SnapshotIdentity{Incarnation: "source", Length: 23}, Ref: "local:thread", ThreadID: "thread", BootGeneration: "7", Epoch: 3, Before: appwire.ThreadItemPosition{Entry: 7, Item: 3, Sub: 2}}
		raw, err := json.Marshal(cursor)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(cursorPrefix+base64.RawURLEncoding.EncodeToString(raw), mask)
	}
	f.Fuzz(func(t *testing.T, encoded string, mask uint8) {
		selection := appwire.ThreadContentSelection(mask & 63)
		cursor, err := decodeCursor(encoded, selection)
		if err != nil {
			return
		}
		raw, err := json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		canonical := cursorPrefix + base64.RawURLEncoding.EncodeToString(raw)
		again, err := decodeCursor(canonical, selection)
		if err != nil || again != cursor {
			t.Fatalf("decoded cursor changed on round-trip: %v", err)
		}
		if _, err := decodeCursor(canonical, selection^1); err == nil {
			t.Fatal("cursor accepted under another content selection")
		}
		if _, err := decodeCursor(canonical, 0); err == nil {
			t.Fatal("selected cursor accepted for a full-content page")
		}
	})
}
