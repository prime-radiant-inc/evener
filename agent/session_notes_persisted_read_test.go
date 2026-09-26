package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

// strictSnapshotJSON renders a mutation-snapshot document the strict
// decodeClientMutationSnapshot accepts, carrying note as the committed human
// note. It mirrors the fixture used by TestHumanNoteRestoreCanonicalFixtures.
func strictSnapshotJSON(sessionID, note string) string {
	return fmt.Sprintf(`{"version":1,"session_id":%q,"human_note":%q,"accepted_turns":0,"journal":{},"input_queue":[],"queue_revision":0,"next_turn_sequence":0,"next_queue_entry_sequence":0,"budget_reservations":{},"pending_executions":{}}`, sessionID, note)
}

func writeMutationSnapshotFile(t testing.TB, stateDir, sessionID, data string) {
	t.Helper()
	dir := filepath.Join(stateDir, "mutations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The light reader is a projection of the same persisted field the strict
// reader returns, so on a snapshot the strict authority accepts both must
// report the same note and present flag — including the saved-empty note,
// where present=true distinguishes an explicit clear from no authority.
func TestReadPersistedHumanNoteMatchesStrictOnWellFormedSnapshots(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"saved-sentinel", ""} {
		t.Run(fmt.Sprintf("note=%q", note), func(t *testing.T) {
			sessionID := identifier.MustNewSessionID()
			stateDir := t.TempDir()
			writeMutationSnapshotFile(t, stateDir, sessionID, strictSnapshotJSON(sessionID, note))

			wantNote, wantPresent, err := ReadCanonicalHumanNote(stateDir, sessionID)
			if err != nil {
				t.Fatalf("ReadCanonicalHumanNote: %v", err)
			}
			if wantNote != note || !wantPresent {
				t.Fatalf("strict fixture read = (%q, %v), want (%q, true)", wantNote, wantPresent, note)
			}
			gotNote, gotPresent, err := ReadPersistedHumanNote(stateDir, sessionID)
			if err != nil {
				t.Fatalf("ReadPersistedHumanNote: %v", err)
			}
			if gotNote != wantNote || gotPresent != wantPresent {
				t.Fatalf("light read = (%q, %v), want the strict read (%q, %v)", gotNote, gotPresent, wantNote, wantPresent)
			}
		})
	}
}

// The roster reader must not be the strict decoder in disguise. For documents
// the strict authority refuses, the light reader still returns the top-level
// note: that contrast is what keeps a past-session listing from decoding and
// validating a journal-sized snapshot once per entry. The malformed-journal
// case additionally pins that the read stops at the value it finds, leaving
// everything after it unread.
func TestReadPersistedHumanNoteReadsWhatStrictAuthorityRejects(t *testing.T) {
	t.Parallel()
	sessionID := identifier.MustNewSessionID()
	valid := func() string { return strictSnapshotJSON(sessionID, "light-note") }
	cases := map[string]string{
		"unknown top-level field": strings.Replace(valid(), `{"version":1`, `{"unknown_notes_state":true,"version":1`, 1),
		"unsupported version":     strings.Replace(valid(), `"version":1`, `"version":99`, 1),
		"session id mismatch":     strings.Replace(valid(), fmt.Sprintf("%q", sessionID), `"01OTHER000000000000000000"`, 1),
		"refused journal shape":   strings.Replace(valid(), `"journal":{}`, `"journal":{"m1":{}}`, 1),
		// Not even valid JSON after the note: the light read never looks.
		"journal that does not parse": strings.Replace(valid(), `"journal":{}`, `"journal":not-json`, 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			writeMutationSnapshotFile(t, stateDir, sessionID, data)

			if _, _, err := ReadCanonicalHumanNote(stateDir, sessionID); err == nil {
				t.Fatal("strict reader accepted the fixture; the contrast needs a document it rejects")
			}
			note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
			if err != nil {
				t.Fatalf("light reader failed on a strict-rejected document: %v", err)
			}
			if !present || note != "light-note" {
				t.Fatalf("light read = (%q, %v), want (\"light-note\", true)", note, present)
			}
		})
	}
}

// Absent authority and unreadable documents keep the roster's contract: no
// file and no field are quiet absences; something that cannot yield the field
// is an error.
func TestReadPersistedHumanNoteAbsentAndMalformed(t *testing.T) {
	t.Parallel()
	sessionID := identifier.MustNewSessionID()
	t.Run("absent file", func(t *testing.T) {
		note, present, err := ReadPersistedHumanNote(t.TempDir(), sessionID)
		if err != nil || present || note != "" {
			t.Fatalf("absent file = (%q, %v, %v), want (\"\", false, nil)", note, present, err)
		}
	})
	t.Run("absent field", func(t *testing.T) {
		stateDir := t.TempDir()
		data := strings.Replace(strictSnapshotJSON(sessionID, "unused"), `"human_note":"unused",`, "", 1)
		writeMutationSnapshotFile(t, stateDir, sessionID, data)
		note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
		if err != nil || present || note != "" {
			t.Fatalf("absent field = (%q, %v, %v), want (\"\", false, nil)", note, present, err)
		}
	})
	t.Run("null field", func(t *testing.T) {
		stateDir := t.TempDir()
		data := strings.Replace(strictSnapshotJSON(sessionID, "unused"), `"human_note":"unused"`, `"human_note":null`, 1)
		writeMutationSnapshotFile(t, stateDir, sessionID, data)
		note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
		if err != nil || present || note != "" {
			t.Fatalf("null field = (%q, %v, %v), want (\"\", false, nil)", note, present, err)
		}
	})
	// Absence is decided by the key's bytes, not by the document's validity: a
	// document that never spells the key cannot carry a note, so the roster gets
	// "no canonical note" without the reader walking the journal behind the
	// missing field (roborev's review asked for exactly that, because the roster
	// reads one document per past entry). A document that does carry the key but
	// cannot be parsed at or before the value stays an error, since there the
	// reader cannot tell whether a note was meant to be there.
	t.Run("keyless malformed document", func(t *testing.T) {
		stateDir := t.TempDir()
		writeMutationSnapshotFile(t, stateDir, sessionID, "{ this is not a decodable snapshot")
		note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
		if err != nil || present || note != "" {
			t.Fatalf("keyless malformed document = (%q, %v, %v), want (\"\", false, nil)", note, present, err)
		}
	})
	t.Run("keyless non-object document", func(t *testing.T) {
		stateDir := t.TempDir()
		writeMutationSnapshotFile(t, stateDir, sessionID, `[]`)
		note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
		if err != nil || present || note != "" {
			t.Fatalf("keyless non-object document = (%q, %v, %v), want (\"\", false, nil)", note, present, err)
		}
	})
	t.Run("keyless truncated document", func(t *testing.T) {
		stateDir := t.TempDir()
		writeMutationSnapshotFile(t, stateDir, sessionID, `{"accepted_turns":1`)
		note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
		if err != nil || present || note != "" {
			t.Fatalf("keyless truncated document = (%q, %v, %v), want (\"\", false, nil)", note, present, err)
		}
	})
	t.Run("document broken at the key", func(t *testing.T) {
		stateDir := t.TempDir()
		writeMutationSnapshotFile(t, stateDir, sessionID, `{"human_note"`)
		if _, _, err := ReadPersistedHumanNote(stateDir, sessionID); err == nil {
			t.Fatal("document broken at the key read without error")
		}
	})
	t.Run("non-string note", func(t *testing.T) {
		stateDir := t.TempDir()
		data := strings.Replace(strictSnapshotJSON(sessionID, "unused"), `"human_note":"unused"`, `"human_note":5`, 1)
		writeMutationSnapshotFile(t, stateDir, sessionID, data)
		if _, _, err := ReadPersistedHumanNote(stateDir, sessionID); err == nil {
			t.Fatal("non-string note read without error")
		}
	})
}

// The roster pays this read once per past entry, so the absent case must not
// scale with the journal behind the missing field. This document is keyless on
// purpose: the benchmark reads it at two journal sizes, and before the fix the
// large case walked every value in it.
func benchmarkPersistedReadDocument(entries int) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"session_id":"01KREINJECTIONNOTESONLY00","accepted_turns":0,"journal":{`)
	for i := range entries {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"m%05d":{"client_mutation_id":"m%05d","method":"urls/add","payload":"%s"}`, i, i, strings.Repeat("x", 200))
	}
	b.WriteString(`},"input_queue":[],"queue_revision":0,"next_turn_sequence":0,"next_queue_entry_sequence":0,"budget_reservations":{},"pending_executions":{}}`)
	return b.String()
}

func BenchmarkReadPersistedHumanNoteAbsent(b *testing.B) {
	for _, entries := range []int{0, 2000} {
		data := benchmarkPersistedReadDocument(entries)
		b.Run(fmt.Sprintf("entries=%d_size=%dKB", entries, len(data)/1024), func(b *testing.B) {
			sessionID := identifier.MustNewSessionID()
			stateDir := b.TempDir()
			writeMutationSnapshotFile(b, stateDir, sessionID, data)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
				if err != nil || present || note != "" {
					b.Fatalf("light read = (%q, %v, %v), want absent", note, present, err)
				}
			}
		})
	}
}

// The absence pre-filter trusts the key's bytes, which is only sound while the
// document carries no escapes: "\u0068uman_note" is a valid JSON spelling of the
// same key, and the strict reader decodes it, so reporting absence there would
// silently hide a note the authority shows (roborev's sixth round).
func TestReadPersistedHumanNoteAcceptsAnEscapedKey(t *testing.T) {
	t.Parallel()
	sessionID := identifier.MustNewSessionID()
	stateDir := t.TempDir()
	data := strings.Replace(strictSnapshotJSON(sessionID, "escaped-key note"), `"human_note"`, `"\u0068uman_note"`, 1)
	writeMutationSnapshotFile(t, stateDir, sessionID, data)
	if _, _, err := ReadCanonicalHumanNote(stateDir, sessionID); err != nil {
		t.Fatalf("strict reader rejected the escaped-key fixture: %v", err)
	}

	note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
	if err != nil || !present || note != "escaped-key note" {
		t.Fatalf("light read = (%q, %v, %v), want the escaped-key note", note, present, err)
	}
}

// An escaped string in the journal is not a reason to walk it: the fast path must
// survive documents whose payloads contain \n, \" or \u003c escapes, which is
// most real journals. A keyless document that is broken after the key position
// reads as absent while the fast path applies, and errors once the walk runs
// (roborev's eleventh round).
func TestReadPersistedHumanNoteKeepsFastPathForEscapedJournals(t *testing.T) {
	t.Parallel()
	sessionID := identifier.MustNewSessionID()
	stateDir := t.TempDir()
	const broken = `{"version":1,"accepted_turns":0,"journal":{"m1":"line1\nline2 \"quoted\" \u003ctag\u003e"`
	writeMutationSnapshotFile(t, stateDir, sessionID, broken)

	note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
	if err != nil {
		t.Fatalf("escaped keyless journal = (%q, %v, %v), want an absence, not a walk error", note, present, err)
	}
	if present || note != "" {
		t.Fatalf("escaped keyless journal = (%q, %v), want absent", note, present)
	}
}

// The same shape as the benchmark above, with escapes in the journal: this is the
// case the fast path has to keep.
func BenchmarkReadPersistedHumanNoteAbsentWithEscapes(b *testing.B) {
	var payload strings.Builder
	payload.WriteString(`{"version":1,"accepted_turns":0,"journal":{`)
	for i := range 2000 {
		if i > 0 {
			payload.WriteString(",")
		}
		fmt.Fprintf(&payload, `"m%05d":"line1\nline2 \"quoted\" \u003ctag\u003e %s"`, i, strings.Repeat("x", 120))
	}
	payload.WriteString(`}}`)
	data := payload.String()
	sessionID := identifier.MustNewSessionID()
	stateDir := b.TempDir()
	writeMutationSnapshotFile(b, stateDir, sessionID, data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		note, present, err := ReadPersistedHumanNote(stateDir, sessionID)
		if err != nil || present || note != "" {
			b.Fatalf("light read = (%q, %v, %v), want absent", note, present, err)
		}
	}
}
