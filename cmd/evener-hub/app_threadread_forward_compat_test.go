package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestPastThreadReadQuarantinesOneUnknownTurnField pins what a daemonless
// read does with a record this binary does not fully understand — the shape
// an older evener-hub sees once a newer evener CLI has added a schema.Turn
// field. transcript.DecodeEntry still rejects the record
// (DisallowUnknownFields), but the transcript index quarantines it: it
// projects as one visible "unreadable entry" item naming why, and the rest of
// the session's history stays readable. A single entry that deterministically
// fails to project is quarantined; the failed-history state is only for
// rebuild and infrastructure failures (the transcript read model's final
// contract decision 6).
func TestPastThreadReadQuarantinesOneUnknownTurnField(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	appendUnknownTurnField(t, path)

	resp, found, err := pastThreadReadResponse(context.Background(), cfg, params)
	if !found || err != nil {
		t.Fatalf("past thread/read = (%v, %v), want the session readable", found, err)
	}
	items := flattenTestItems(resp.Thread.Turns)
	if len(items) < 2 {
		t.Fatalf("past thread/read items = %d, want the saved history before the unreadable entry", len(items))
	}
	last := items[len(items)-1]
	if last.Description != "Unreadable transcript entry" || !strings.Contains(last.Text, "future_field_an_old_binary_lacks") {
		t.Fatalf("last item = %+v, want the unreadable entry naming the unknown field", last)
	}
	if items[len(items)-2].CallID != "call_img" {
		t.Fatalf("item before the unreadable entry = %+v, want the saved screenshot call", items[len(items)-2])
	}

	page, found, err := pastThreadTurnsList(context.Background(), cfg, appwire.ThreadTurnsListParams{Ref: params.Ref, ItemLimit: 1})
	if !found || err != nil {
		t.Fatalf("past thread/turns/list = (%v, %v), want the session readable", found, err)
	}
	if pageItems := flattenTestItems(page.Data); len(pageItems) != 1 || pageItems[0].Description != "Unreadable transcript entry" {
		t.Fatalf("past thread/turns/list items = %+v, want the unreadable entry", pageItems)
	}
}

// appendUnknownTurnField appends one more, otherwise well-formed, transcript
// entry whose nested turn object carries a field no schema.Turn in this
// build declares — the exact shape a transcript written by a newer evener
// binary presents to an older one.
func appendUnknownTurnField(t *testing.T, path string) {
	t.Helper()
	line := `{"kind":"entry","seq":200,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi"}]},"future_field_an_old_binary_lacks":"x"}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test cleanup; write error already caught below
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}
