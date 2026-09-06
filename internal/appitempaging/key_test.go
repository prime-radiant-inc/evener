package appitempaging

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestTranscriptItemKeyPreservesWireFormat(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 12, Item: 34}
	if got, want := TranscriptItemKey("turn:representative", position), "apptranscript-item-v1:turn:representative:12:34"; got != want {
		t.Fatalf("TranscriptItemKey() = %q, want %q", got, want)
	}
}

func TestHasTranscriptKeysPrefix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		old     []string
		current []string
		want    bool
	}{
		{name: "empty", old: nil, current: nil, want: true},
		{name: "exact", old: []string{"a", "b"}, current: []string{"a", "b"}, want: true},
		{name: "appended", old: []string{"a"}, current: []string{"a", "b"}, want: true},
		{name: "current shorter", old: []string{"a", "b"}, current: []string{"a"}, want: false},
		{name: "reordered", old: []string{"a", "b"}, current: []string{"b", "a"}, want: false},
		{name: "replaced", old: []string{"a"}, current: []string{"b"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasTranscriptKeysPrefix(tc.old, tc.current); got != tc.want {
				t.Fatalf("HasTranscriptKeysPrefix(%v, %v) = %v, want %v", tc.old, tc.current, got, tc.want)
			}
		})
	}
}
