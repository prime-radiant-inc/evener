package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The resume corpus pins the outcome of OpenWriterForSession on the transcript
// shapes a resume actually meets: a clean file, a crash tail, a compaction
// anchor mid-file, and corruption on either side of that anchor. Every
// expected value below was produced by running this package's resume path, so
// a change that alters any of them is a resume that reads a transcript
// differently than the build before it — which for a durability-adjacent path
// means a silently different session.
//
// Two properties are the reason this table exists:
//
//   - A torn tail is truncated and the resume succeeds, while a corrupt
//     COMPLETE line fails the whole resume and leaves every byte in place.
//     The order matters: the scan validates before it truncates, so a file
//     with both a corrupt line and a torn tail is left untouched.
//   - Every entry before the last compaction anchor is returned, not only the
//     ones ResumeHistory retains. Resume's callers consume that prefix: see
//     TestResumeReturnsEntriesBeforeTheCompactionAnchor.

const corpusHeader = `{"kind":"header","format_version":2,"session_id":"CORPUS","created_at":"2026-10-25T00:16:04.227716886Z","profile_id":"p","model":"m"}` + "\n"

// corpusCase is one transcript body and the resume outcome pinned for it.
type corpusCase struct {
	name string
	body string

	// wantFailure says the resume must fail, and wantErr lists the sentinels
	// its error must match. A failing case with no sentinel wants a failure
	// this package does not name with one (a strict-decode error, say).
	wantFailure bool
	wantErr     []error

	// wantEntries is the "seq:KIND" sequence the scan must return.
	wantEntries []string
	// wantNextSeq is the sequence the resumed writer gave its next append.
	wantNextSeq int
	// wantTruncated reports whether the resume cut the file's tail.
	wantTruncated bool
}

func corpusTurn(kind schema.TurnKind, text string) schema.Turn {
	return schema.NewTurn(kind, llm.User(text))
}

func corpusLine(t *testing.T, seq int, turn schema.Turn) string {
	t.Helper()
	encoded, err := json.Marshal(Entry{Kind: "entry", MachineryFlagged: true, Seq: seq, Turn: turn})
	if err != nil {
		t.Fatalf("marshal corpus entry: %v", err)
	}
	return string(encoded) + "\n"
}

// corpusLinePreFlag writes an entry the way a build before the machinery flag
// did, so the resume path's pre-flag inference is exercised too.
func corpusLinePreFlag(t *testing.T, seq int, turn schema.Turn) string {
	t.Helper()
	encoded, err := json.Marshal(Entry{Kind: "entry", Seq: seq, Turn: turn})
	if err != nil {
		t.Fatalf("marshal pre-flag corpus entry: %v", err)
	}
	return string(encoded) + "\n"
}

func corpusCases(t *testing.T) []corpusCase {
	t.Helper()
	clean := corpusHeader +
		corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) +
		corpusLine(t, 1, corpusTurn(schema.TurnAssistant, "a1")) +
		corpusLine(t, 2, corpusTurn(schema.TurnUserInput, "u2"))
	// A checkpoint mid-file: the shape issue #6418 wants to start decoding
	// from. Exactly one entry (seq 0) precedes it, and it is the state the fix
	// must not skip; the multi-entry prefix lives in
	// TestResumeReturnsEntriesBeforeTheCompactionAnchor below.
	checkpoint := corpusHeader +
		corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) +
		corpusLine(t, 1, corpusTurn(schema.TurnCheckpoint, "ck1")) +
		corpusLine(t, 2, corpusTurn(schema.TurnUserInput, "u2")) +
		corpusLine(t, 3, corpusTurn(schema.TurnAssistant, "a3"))

	return []corpusCase{
		{
			name:        "clean",
			body:        clean,
			wantEntries: []string{"0:USER_INPUT", "1:ASSISTANT", "2:USER_INPUT"},
			wantNextSeq: 3,
		},
		{
			name:          "partial_tail_after_crash",
			body:          clean + `{"kind":"entry","seq":3,`,
			wantEntries:   []string{"0:USER_INPUT", "1:ASSISTANT", "2:USER_INPUT"},
			wantNextSeq:   3,
			wantTruncated: true,
		},
		{
			name:        "checkpoint_midfile",
			body:        checkpoint,
			wantEntries: []string{"0:USER_INPUT", "1:CHECKPOINT", "2:USER_INPUT", "3:ASSISTANT"},
			wantNextSeq: 4,
		},
		{
			name:        "checkpoint_last",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + corpusLine(t, 1, corpusTurn(schema.TurnCheckpoint, "ck1")),
			wantEntries: []string{"0:USER_INPUT", "1:CHECKPOINT"},
			wantNextSeq: 2,
		},
		{
			name:          "partial_tail_after_checkpoint",
			body:          corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + corpusLine(t, 1, corpusTurn(schema.TurnCheckpoint, "ck1")) + corpusLine(t, 2, corpusTurn(schema.TurnUserInput, "u2")) + `{"kind":"entry","seq":3,`,
			wantEntries:   []string{"0:USER_INPUT", "1:CHECKPOINT", "2:USER_INPUT"},
			wantNextSeq:   3,
			wantTruncated: true,
		},
		{
			name:        "malformed_before_checkpoint",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + "{not json}\n" + corpusLine(t, 1, corpusTurn(schema.TurnCheckpoint, "ck1")),
			wantFailure: true,
			wantErr:     []error{ErrInvalidRecordBoundary},
		},
		{
			name:        "malformed_after_checkpoint",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnCheckpoint, "ck1")) + "{not json}\n" + corpusLine(t, 1, corpusTurn(schema.TurnUserInput, "u2")),
			wantFailure: true,
			wantErr:     []error{ErrInvalidRecordBoundary},
		},
		{
			// The corrupt line comes before the torn tail, so the resume fails
			// with the file untouched: the scan truncates only after every
			// complete line has validated.
			name:        "malformed_then_partial_tail",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnCheckpoint, "ck1")) + "{not json}\n" + `{"kind":"entry","seq":2,`,
			wantFailure: true,
			wantErr:     []error{ErrInvalidRecordBoundary},
		},
		{
			name:        "unknown_field_before_checkpoint",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + `{"kind":"entry","seq":1,"nonesuch":true,"turn":{"format":1,"kind":"CHECKPOINT","message":{"role":"user","content":[]}}}` + "\n" + corpusLine(t, 2, corpusTurn(schema.TurnUserInput, "u2")),
			wantFailure: true,
		},
		{
			name:        "unknown_field_after_checkpoint",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnCheckpoint, "ck1")) + `{"kind":"entry","seq":1,"nonesuch":true,"turn":{"format":1,"kind":"USER_INPUT","message":{"role":"user","content":[]}}}` + "\n",
			wantFailure: true,
		},
		{
			// Sequence numbers need not be contiguous: a resumed writer takes
			// max(seq)+1 so it can never reuse one lost to crash recovery.
			name:        "seq_gap",
			body:        corpusHeader + corpusLine(t, 5, corpusTurn(schema.TurnUserInput, "u0")) + corpusLine(t, 9, corpusTurn(schema.TurnCheckpoint, "ck1")) + corpusLine(t, 12, corpusTurn(schema.TurnUserInput, "u2")),
			wantEntries: []string{"5:USER_INPUT", "9:CHECKPOINT", "12:USER_INPUT"},
			wantNextSeq: 13,
		},
		{
			name:        "blank_lines",
			body:        corpusHeader + "\n" + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + "\n\n" + corpusLine(t, 1, corpusTurn(schema.TurnCheckpoint, "ck1")) + "\n" + corpusLine(t, 2, corpusTurn(schema.TurnUserInput, "u2")),
			wantEntries: []string{"0:USER_INPUT", "1:CHECKPOINT", "2:USER_INPUT"},
			wantNextSeq: 3,
		},
		{
			name:          "blank_lines_and_partial_tail",
			body:          corpusHeader + "\n" + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + "\n\n" + `{"kind":"entry","se`,
			wantEntries:   []string{"0:USER_INPUT"},
			wantNextSeq:   1,
			wantTruncated: true,
		},
		{
			name:        "pre_flag_entry",
			body:        corpusHeader + corpusLinePreFlag(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + corpusLine(t, 1, corpusTurn(schema.TurnCheckpoint, "ck1")),
			wantEntries: []string{"0:USER_INPUT", "1:CHECKPOINT"},
			wantNextSeq: 2,
		},
		{
			name:        "no_header",
			body:        corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + corpusLine(t, 1, corpusTurn(schema.TurnAssistant, "a1")),
			wantFailure: true,
			wantErr:     []error{ErrUnsupportedFormat},
		},
		{
			name:        "torn_first_line",
			body:        `{"kind":"header","format_version":2,"sess`,
			wantFailure: true,
		},
		{
			name:        "empty",
			body:        "",
			wantFailure: true,
			// The resume path's missing-header error wraps ErrUnsupportedFormat
			// only; ErrMissingHeader is transcript.Scan's, not resume's.
			wantErr: []error{ErrUnsupportedFormat},
		},
		{
			// A header-only transcript resumes with a non-nil empty entry slice:
			// the delegate-attention fold keys on nilness to decide whether it
			// can fold in memory or must re-read the file.
			name:        "header_only",
			body:        corpusHeader,
			wantEntries: []string{},
			wantNextSeq: 0,
		},
		{
			name:        "v1_header",
			body:        `{"kind":"header","format_version":1,"session_id":"CORPUS","created_at":"2026-10-25T00:16:04Z","profile_id":"p","model":"m"}` + "\n" + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")),
			wantFailure: true,
			wantErr:     []error{ErrUnsupportedFormat},
		},
		{
			name:        "nonentry_kind",
			body:        corpusHeader + corpusLine(t, 0, corpusTurn(schema.TurnUserInput, "u0")) + `{"kind":"summary","seq":1}` + "\n",
			wantFailure: true,
			wantErr:     []error{ErrUnsupportedFormat},
		},
		{
			name:        "nonentry_kind_then_partial_tail",
			body:        corpusHeader + `{"kind":"summary","seq":1}` + "\n" + `{"kind":"entry","se`,
			wantFailure: true,
			wantErr:     []error{ErrUnsupportedFormat},
		},
	}
}

func TestResumeCorpus(t *testing.T) {
	t.Parallel()
	for _, tc := range corpusCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write corpus transcript: %v", err)
			}

			w, entries, resumeErr := OpenWriterForSession(path, "CORPUS")

			// Compare the bytes, not the length: a resume that rewrote bytes in
			// place, or cut at the wrong boundary while reporting a consistent
			// RecordedLength, would pass a size-only check.
			resumedBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read corpus transcript after resume: %v", err)
			}
			truncated := len(resumedBytes) != len(tc.body)
			if truncated != tc.wantTruncated {
				t.Fatalf("tail truncated = %v, want %v (%d bytes before, %d after)", truncated, tc.wantTruncated, len(tc.body), len(resumedBytes))
			}

			if !tc.wantFailure {
				if resumeErr != nil {
					t.Fatalf("resume error = %v, want the case to resume", resumeErr)
				}
			} else {
				if resumeErr == nil {
					t.Fatalf("resume succeeded, want an error matching %v", tc.wantErr)
				}
				if w != nil {
					_ = w.Close()
					t.Fatal("a failed resume returned a writer; the caller would append to a transcript it could not validate")
				}
				for _, want := range tc.wantErr {
					if !errors.Is(resumeErr, want) {
						t.Fatalf("resume error = %v, want errors.Is(%v)", resumeErr, want)
					}
				}
				if entries != nil {
					t.Fatalf("failed resume returned %d entries, want none", len(entries))
				}
				// Fail closed includes leaving the file alone: a resume that
				// cannot validate the file must not truncate it.
				if !bytes.Equal(resumedBytes, []byte(tc.body)) {
					t.Fatalf("failed resume changed the file: got %d bytes %q, want the original %d bytes",
						len(resumedBytes), resumedBytes, len(tc.body))
				}
				return
			}

			defer func() { _ = w.Close() }()

			if want := corpusResumedBody(tc.body); !bytes.Equal(resumedBytes, []byte(want)) {
				t.Fatalf("resumed file = %d bytes %q, want the body through its last newline: %d bytes %q",
					len(resumedBytes), resumedBytes, len(want), want)
			}

			got := make([]string, 0, len(entries))
			for _, e := range entries {
				got = append(got, corpusEntryName(e))
			}
			if len(got) != len(tc.wantEntries) {
				t.Fatalf("resumed entries = %v, want %v", got, tc.wantEntries)
			}
			for i := range got {
				if got[i] != tc.wantEntries[i] {
					t.Fatalf("resumed entry %d = %q, want %q (all: %v)", i, got[i], tc.wantEntries[i], got)
				}
			}
			if entries == nil && tc.wantEntries != nil {
				t.Fatal("resumed entries are nil; a header-only transcript must resume with an empty, non-nil slice")
			}

			// The recorded length is the file's length once any crash tail is
			// cut: the next append lands there and nowhere else.
			if int64(len(resumedBytes)) != w.RecordedLength() {
				t.Fatalf("RecordedLength = %d, want the resumed file's %d bytes", w.RecordedLength(), len(resumedBytes))
			}
			if got := w.Header().SessionID; got != "CORPUS" {
				t.Fatalf("resumed header session = %q, want %q", got, "CORPUS")
			}

			// The resumed writer's first append must take the pinned sequence,
			// so a restart never reuses a number the file already holds.
			if err := w.Append(corpusTurn(schema.TurnAssistant, "after resume")); err != nil {
				t.Fatalf("append after resume: %v", err)
			}
			if got := tailNextSeq(w) - 1; got != tc.wantNextSeq {
				t.Fatalf("first post-resume append took seq %d, want %d", got, tc.wantNextSeq)
			}
			grown, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat after post-resume append: %v", err)
			}
			if grown.Size() <= int64(len(resumedBytes)) {
				t.Fatalf("post-resume append did not grow the file: %d then %d", len(resumedBytes), grown.Size())
			}
		})
	}
}

// corpusResumedBody is the body a successful resume leaves: every
// newline-terminated record and nothing of a torn tail. A body that already
// ends in a newline has no tail to cut.
func corpusResumedBody(body string) string {
	if strings.HasSuffix(body, "\n") {
		return body
	}
	cut := strings.LastIndexByte(body, '\n')
	return body[:cut+1]
}

func corpusEntryName(e Entry) string {
	return strconv.Itoa(e.Seq) + ":" + string(e.Turn.Kind)
}

// TestResumeReturnsEntriesBeforeTheCompactionAnchor pins why resume cannot
// start decoding at the last checkpoint, the shape issue #6418 proposes. The
// scan returns EVERY entry, and resume's callers fold the ones before the
// anchor into durable state that a suffix-only read would silently drop:
//
//   - reconcileSkillCompactionReceipts and reconcileSkillDeliveryNotifications
//     replay typed receipts onto a snapshot that may be staler than the
//     transcript (agent/transcript_read.go);
//   - supersededDelegateAttentionIDs folds attention rows, whose openers are
//     usually in the prefix — a resolution whose opener was skipped fails the
//     fold outright as "resolved before it was appended" (agent/session_attention.go);
//   - highestClientMutationTurnSequence floors the session's next reservation
//     above every sequence the file already names (agent/session_execution.go);
//   - TrackFailures learns tool names from every turn, so a name announced
//     before the anchor still resolves a nameless result after it
//     (agent/transcript/failures.go);
//   - restoreConversation and the restoredClientMutation maps adopt identity
//     from the whole file (agent/session_conversation_provenance.go,
//     agent/session_init.go);
//   - SessionStartData.TranscriptEntries is the file's full entry count
//     (agent/session_resume_turn_seed_test.go).
//
// Skipping that decode therefore changes resumed state; it is not a
// pure-performance change. Issue #643's validated-offset sidecar is what
// makes a suffix-only read correct, because it snapshots those facts at
// anchor time.
func TestResumeReturnsEntriesBeforeTheCompactionAnchor(t *testing.T) {
	t.Parallel()
	attention := corpusTurn(schema.TurnSteering, "attention-report")
	attention.AttentionID = "att-1"
	resolution := corpusTurn(schema.TurnAttentionResolution, "")
	resolution.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "att-1", Disposition: "consumed"}
	mutation := corpusTurn(schema.TurnUserInput, "retry-safe")
	mutation.ClientMutationID = "cm-1"
	receipt := corpusTurn(schema.TurnAssistant, "skill")
	receipt.SkillState = &schema.SkillTurnState{
		Compaction: &schema.SkillCompactionReceipt{Revision: 7, SessionID: "CORPUS", Phase: "published"},
	}

	body := corpusHeader +
		corpusLine(t, 0, attention) +
		corpusLine(t, 1, mutation) +
		corpusLine(t, 2, receipt) +
		corpusLine(t, 3, corpusTurn(schema.TurnCheckpoint, "ck3")) +
		corpusLine(t, 4, resolution)
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	w, entries, err := OpenWriterForSession(path, "CORPUS")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer func() { _ = w.Close() }()

	if len(entries) != 5 {
		t.Fatalf("resumed %d entries, want all 5: the three before the anchor carry state resume's callers fold", len(entries))
	}
	if got := entries[0].Turn.AttentionID; got != "att-1" {
		t.Fatalf("entry 0 attention id = %q, want the pre-anchor steering row", got)
	}
	if got := entries[1].Turn.ClientMutationID; got != "cm-1" {
		t.Fatalf("entry 1 client mutation id = %q, want the pre-anchor input's identity", got)
	}
	if state := entries[2].Turn.SkillState; state == nil || state.Compaction == nil || state.Compaction.Revision != 7 {
		t.Fatalf("entry 2 skill state = %+v, want the pre-anchor compaction receipt", state)
	}
	if entries[3].Turn.Kind != schema.TurnCheckpoint {
		t.Fatalf("entry 3 kind = %q, want the anchor", entries[3].Turn.Kind)
	}
	if res := entries[4].Turn.AttentionResolution; res == nil || res.AttentionID != "att-1" {
		t.Fatalf("entry 4 resolution = %+v, want the resolution that closes the pre-anchor attention", res)
	}
}
