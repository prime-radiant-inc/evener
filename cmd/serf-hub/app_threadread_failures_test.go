package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

// shellRound describes one assistant round in a seeded transcript: a shell call
// and the exit code its result carries.
type shellRound struct{ exitCode int64 }

// seedPastSessionWithShellExits writes a real transcript of shell calls with
// the given exit codes, plus a meta. divergenceTurn > 0 makes it a fork child
// whose entries before that ordinal are the inherited parent prefix.
//
// The id is minted rather than passed in: PastIndex.Rebuild silently drops any
// session whose id fails identifier.ValidateSessionID.
func seedPastSessionWithShellExits(t testing.TB, divergenceTurn int, rounds []shellRound) (hubcore.WebConfig, hubcore.PastEntry) {
	t.Helper()
	sessionID := identifier.MustNewSessionID()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-failures-0000000000")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	meta := schema.SessionMeta{
		ID: sessionID, ProfileID: "anthropic", Model: "claude-opus-4-5",
		CreatedAt: now, UpdatedAt: now, TurnCount: len(rounds) * 2,
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		DivergenceTurn: divergenceTurn,
	}
	if divergenceTurn > 0 {
		meta.ParentSessionID = identifier.MustNewSessionID()
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "anthropic", Model: "claude-opus-4-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fixture, not a durability test: batch the writes so seeding does not pay
	// one fsync per Append.
	w.SyncInterval = time.Hour
	for i, round := range rounds {
		id := "call_" + string(rune('a'+i%26))
		state, err := json.Marshal(map[string]any{"exit_code": round.exitCode})
		if err != nil {
			t.Fatal(err)
		}
		announce := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: id, Name: "shell", Arguments: json.RawMessage(`{"command":"go test ./..."}`)},
		}}}
		results := llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: id, Name: "shell", Content: "output", ToolState: state},
		}}}
		if err := w.Append(schema.Turn{Kind: schema.TurnAssistant, Message: announce}); err != nil {
			t.Fatal(err)
		}
		if err := w.Append(schema.Turn{Kind: schema.TurnToolResults, Message: results}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Find(sessionID)
	if !ok {
		t.Fatalf("past index did not find seeded session %s", sessionID)
	}
	return hubcore.WebConfig{Past: idx}, entry
}

// THE KATA (hw2n). A reader could not tell whether a session contained a
// failure without scrolling all of it. The read path now reports the count the
// session's own chrome can state.
func TestPastThreadRead_ReportsTheFailedToolCallCountFromTheFullTranscript(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{0}, {1}, {0}, {127}, {0}})

	thread := readSeededThread(t, cfg, entry)

	if thread.Serf.FailedToolCalls == nil {
		t.Fatal("thread.Serf.FailedToolCalls = nil, want the full-transcript count")
	}
	if got := *thread.Serf.FailedToolCalls; got != 2 {
		t.Fatalf("thread.Serf.FailedToolCalls = %d, want 2", got)
	}
}

// A clean session reports a REAL zero, not an absence. The distinction is the
// whole reason the field is a pointer: the client renders zero as nothing, but
// a future surface that wants to say "nothing failed" must be able to tell
// "measured, none" from "nobody counted".
func TestPastThreadRead_ReportsZeroForASessionWhereNothingFailed(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{0}, {0}, {0}})

	thread := readSeededThread(t, cfg, entry)

	if thread.Serf.FailedToolCalls == nil {
		t.Fatal("thread.Serf.FailedToolCalls = nil, want a measured 0 for a clean session")
	}
	if got := *thread.Serf.FailedToolCalls; got != 0 {
		t.Fatalf("thread.Serf.FailedToolCalls = %d, want 0", got)
	}
}

// A fork child's transcript opens with a verbatim copy of the parent's prefix.
// Those failures were the parent's; attributing them to the child is the same
// bug DivergenceTurn already prevents for tokens.
func TestPastThreadRead_ForkFailureCountExcludesTheInheritedParentPrefix(t *testing.T) {
	// Entries 1-2 and 3-4 are the parent's rounds; the child's own history
	// starts at entry 5.
	cfg, entry := seedPastSessionWithShellExits(t, 5, []shellRound{{1}, {1}, {2}})

	thread := readSeededThread(t, cfg, entry)

	if thread.Serf.FailedToolCalls == nil {
		t.Fatal("thread.Serf.FailedToolCalls = nil, want the child's own count")
	}
	if got := *thread.Serf.FailedToolCalls; got != 1 {
		t.Fatalf("thread.Serf.FailedToolCalls = %d, want 1 (the parent's two failures are not the child's)", got)
	}
}

// A legacy format_version 1 transcript is unreadable, so the count is UNKNOWN
// and stays absent — and the rest of the thread still projects. Reporting a
// comforting 0 for a session nobody can read is the exact misreading this
// count exists to prevent.
func TestPastThreadRead_LegacyTranscriptLeavesTheFailureCountAbsent(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{1}})
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	thread := readSeededThread(t, cfg, entry)

	if thread.Serf.FailedToolCalls != nil {
		t.Fatalf("thread.Serf.FailedToolCalls = %d, want absent for an unreadable transcript", *thread.Serf.FailedToolCalls)
	}
	if thread.ID != entry.Meta.ID {
		t.Fatalf("thread.ID = %q, want the rest of the projection intact", thread.ID)
	}
}

func TestPastThreadRead_MissingTranscriptLeavesTheFailureCountAbsent(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{1}})
	if err := os.Remove(filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")); err != nil {
		t.Fatal(err)
	}

	thread := readSeededThread(t, cfg, entry)

	if thread.Serf.FailedToolCalls != nil {
		t.Fatalf("thread.Serf.FailedToolCalls = %d, want absent when the transcript is gone", *thread.Serf.FailedToolCalls)
	}
}

// THE POINT of doing this server-side. Only ~47% of a long session's document
// hydrates at load (kata hw2n), so a count over the loaded window undercounts.
// The derived count must not move with the window thread/read returns.
func TestPastThreadRead_FailureCountIsIndependentOfTheTurnWindow(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{1}, {0}, {1}, {0}, {1}})

	resp, ok := requirePastThreadReadResponse(t, cfg, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, TurnLimit: 1,
	})
	if !ok {
		t.Fatalf("past thread %s not found", entry.Meta.ID)
	}

	if resp.OlderCursor == "" {
		t.Fatal("OlderCursor is empty; the fixture must actually truncate for this test to mean anything")
	}
	if resp.Thread.Serf.FailedToolCalls == nil {
		t.Fatal("thread.Serf.FailedToolCalls = nil on a windowed read, want the full-transcript count")
	}
	if got := *resp.Thread.Serf.FailedToolCalls; got != 3 {
		t.Fatalf("windowed read reported %d failures, want 3 (the whole transcript's, not the window's)", got)
	}
}

// The thread-list and transcript-target sweeps run this projector once per
// session in the state dir. A transcript scan per entry there costs a read of
// every transcript on disk (measured at 1.09s over 1592 files in kata 5tdg),
// so the sweep path must stay off it and report absent instead.
func TestPastEntryThread_DoesNotDeriveTheFailureCountOnTheListSweepPath(t *testing.T) {
	cfg, entry := seedPastSessionWithShellExits(t, 0, []shellRound{{1}, {1}})

	thread, err := pastEntryThread(cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}

	if thread.Serf.FailedToolCalls != nil {
		t.Fatalf("sweep projector reported %d failures; it must not scan transcripts", *thread.Serf.FailedToolCalls)
	}
}
