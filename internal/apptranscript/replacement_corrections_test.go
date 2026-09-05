package apptranscript

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"primeradiant.com/evener/appwire"
)

func refreshReplacementFull(t *testing.T, cache *TurnCache, path, refresh string) {
	t.Helper()
	var err error
	switch refresh {
	case "raw":
		_, err = cache.TurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	case "grouped":
		_, err = cache.ItemTurnsFromFile(path, testMaxLineBytes, sequentialTestProjector())
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestFullRefreshCannotMaskItemIndexReplacement(t *testing.T) {
	for _, refresh := range []string{"none", "raw", "grouped"} {
		t.Run(refresh, func(t *testing.T) {
			path := writeEntries(t, userEntry(1, "first-old"), userEntry(2, "second"))
			cache := NewTurnCache()
			options := ItemWindowOptions{ThreadRef: "local:replacement", Limit: 1}
			window, before, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, boundedTestProjector)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rewritten := bytes.Replace(raw, []byte("first-old"), []byte("first-new"), 1)
			if bytes.Equal(raw, rewritten) || len(raw) != len(rewritten) {
				t.Fatal("fixture must replace bytes at equal length")
			}
			replacement := path + ".replacement"
			if err := os.WriteFile(replacement, rewritten, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
			refreshReplacementFull(t, cache, path, refresh)
			options.Cursor = window.OlderCursor
			_, after, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, options, boundedTestProjector)
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("replacement accepted old cursor or returned untyped error: %v (incarnation preserved=%v)", err, before.Incarnation == after.Incarnation)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
				t.Fatalf("want typed item stale, got %v", err)
			}
			if before.Incarnation == after.Incarnation {
				t.Fatal("replacement did not rotate incarnation")
			}
		})
	}
}

func TestFullRefreshPreservesItemIndexAppend(t *testing.T) {
	for _, refresh := range []string{"raw", "grouped"} {
		t.Run(refresh, func(t *testing.T) {
			path := writeEntries(t, userEntry(1, "first"), userEntry(2, "second"))
			cache := NewTurnCache()
			options := ItemWindowOptions{ThreadRef: "local:append", Limit: 1}
			window, before, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, boundedTestProjector)
			if err != nil {
				t.Fatal(err)
			}
			appendFile(t, path, marshalEntryLine(t, userEntry(3, "third")))
			refreshReplacementFull(t, cache, path, refresh)
			var stats ReadStats
			restore := InstallReadObserverForTesting(func(observed ReadStats) { stats = observed })
			defer restore()
			_, after, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, options, boundedTestProjector)
			if err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("valid append with full refresh rotated incarnation")
			}
			if stats.rebuilt || stats.journalRecords != 1 {
				t.Fatalf("append lost incremental index/journal reuse: %+v", stats)
			}
			options.Cursor = window.OlderCursor
			older, continued, err := cache.PreviousItemWindowFromFile(path, testMaxLineBytes, options, boundedTestProjector)
			if err != nil {
				t.Fatal(err)
			}
			if continued != before || len(older.Candidates) != 1 || older.Candidates[0].Item.Text != "first" {
				t.Fatalf("valid old append cursor returned %+v under %+v", older, continued)
			}
		})
	}
}
