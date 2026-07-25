package main

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestPastThreadReadFailsWholeSessionOnOneUnknownTurnField pins kata wf7e's
// established mechanism: transcript.DecodeEntry runs DisallowUnknownFields on
// every record in a session's transcript before any of it is projected. So
// one record this binary doesn't fully understand — the shape an older
// serf-hub sees once a newer serf CLI has added a schema.Turn field — takes
// the whole session down, not just the turn carrying the new field. The other
// 199 turns in this fixture decode perfectly and are still not visible.
//
// This is deliberate, not a bug: see the design note beside
// decoder.DisallowUnknownFields() in agent/transcript/transcript.go, and the
// matching, independently-arrived-at posture in agent/transcript_read.go's
// readSemanticTranscript ("corrupt complete lines... reject the whole
// file") and agent/doctor/transcript.go's loadTranscriptWithMaxLineBytes. The
// test exists so a future change to that posture is a decision, not an
// accident.
func TestPastThreadReadFailsWholeSessionOnOneUnknownTurnField(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	appendUnknownTurnField(t, path)

	resp, found, err := pastThreadReadResponse(cfg, params)
	if !found || err == nil {
		t.Fatalf("past thread/read = (%+v, %v, %v), want found=true with a decode error", resp, found, err)
	}
	if resp.Thread.Turns != nil {
		t.Fatalf("past thread/read returned %d turns despite the decode error; want none — a partial result here would mean the all-or-nothing failure this test pins had silently stopped being all-or-nothing", len(resp.Thread.Turns))
	}

	page, found, err := pastThreadTurnsList(cfg, appwire.ThreadTurnsListParams{Ref: params.Ref, Limit: 1})
	if !found || err == nil || page.Data != nil {
		t.Fatalf("past thread/turns/list = (%+v, %v, %v), want found=true with a decode error and no data", page, found, err)
	}
}

// appendUnknownTurnField appends one more, otherwise well-formed, transcript
// entry whose nested turn object carries a field no schema.Turn in this
// build declares — the exact shape a transcript written by a newer serf
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
