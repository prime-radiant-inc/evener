package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

// seedPastSessionWithUsage writes a real transcript whose assistant entries
// carry the given per-turn usage, plus a meta with NO CumulativeUsage — the
// shape agent/fork.go's writeForkChild actually produces. divergenceTurn > 0
// makes it a fork child; the entries before it are the inherited parent prefix.
func seedPastSessionWithUsage(t testing.TB, sessionID string, divergenceTurn int, usages []llm.Usage) hubcore.PastEntry {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-usage-0000000000")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	meta := schema.SessionMeta{
		ID: sessionID, ProfileID: "anthropic", Model: "claude-opus-4-5",
		CreatedAt: now, UpdatedAt: now,
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		DivergenceTurn: divergenceTurn,
	}
	if divergenceTurn > 0 {
		meta.ParentSessionID = "02wMz5TxvPARENT000000"
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
	// one fsync per Append (same reason seedBoundedPastThread does).
	w.SyncInterval = time.Hour
	for _, u := range usages {
		turn := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("saved turn"), Usage: u}
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return hubcore.PastEntry{ID: sessionID, Meta: meta, StateDir: stateDir}
}

// THE KATA. A fork child's meta carries no CumulativeUsage (writeForkChild does
// not stamp it), so before this fix serf.usage and serf.cost arrived empty and
// the client could only sum the turns it happened to hold. The hub now sums the
// child's OWN span of the full transcript, so the total is complete regardless
// of thread/read's window.
func TestPastEntryThread_DerivesSessionUsageFromFullTranscriptWhenMetaHasNone(t *testing.T) {
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000001", 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
		{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200},
		{InputTokens: 3000, OutputTokens: 300, TotalTokens: 3300},
	})

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.Usage == nil {
		t.Fatalf("thread.Serf.Usage = nil; want the full-transcript sum, not an empty total")
	}
	got := *thread.Serf.Usage
	want := appwire.SerfUsage{InputTokens: 6000, OutputTokens: 600, TotalTokens: 6600}
	if got != want {
		t.Fatalf("thread.Serf.Usage = %+v, want %+v", got, want)
	}
	if !strings.HasPrefix(thread.Serf.Cost, "~$") {
		t.Fatalf("thread.Serf.Cost = %q, want a ~$ estimate derived from the recovered total", thread.Serf.Cost)
	}
}

// A fork's tokens are not the parent's. The child transcript OPENS with a
// verbatim copy of the parent's prefix, so summing the whole file would charge
// the child for spend it never made. Only the post-divergence span counts.
func TestPastEntryThread_ForkUsageExcludesTheInheritedParentPrefix(t *testing.T) {
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000002", 3, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100}, // parent's, entry 1
		{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200}, // parent's, entry 2
		{InputTokens: 4000, OutputTokens: 400, TotalTokens: 4400}, // the child's own, entry 3
		{InputTokens: 5000, OutputTokens: 500, TotalTokens: 5500}, // the child's own, entry 4
	})

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.Usage == nil {
		t.Fatalf("thread.Serf.Usage = nil, want the child's own post-divergence total")
	}
	got := *thread.Serf.Usage
	want := appwire.SerfUsage{InputTokens: 9000, OutputTokens: 900, TotalTokens: 9900}
	if got != want {
		t.Fatalf("thread.Serf.Usage = %+v, want %+v (the parent's 3000/300 prefix must not be counted)", got, want)
	}
}

// A meta that DOES carry CumulativeUsage is authoritative: it is the daemon's
// own running total, and re-deriving would risk a second, disagreeing figure.
// Proven with a transcript whose per-turn sum deliberately differs.
func TestPastEntryThread_PersistedCumulativeUsageWinsOverTheDerivedSum(t *testing.T) {
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000003", 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	entry.Meta.CumulativeUsage = schema.CumulativeUsage{InputTokens: 7777, OutputTokens: 777, TotalTokens: 8554}

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.Usage == nil || thread.Serf.Usage.InputTokens != 7777 {
		t.Fatalf("thread.Serf.Usage = %+v, want the persisted 7777 total, not the derived 1000", thread.Serf.Usage)
	}
}

// An aside fork holds only the inherited prefix until someone opens and runs
// it. It has spent nothing, and the honest report for "nothing measured" is an
// ABSENT total — never a zero that renders as a real "↑0 ↓0" or a "~$0.00".
func TestPastEntryThread_UnopenedForkReportsAbsentUsageNotZero(t *testing.T) {
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000004", 3, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
		{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200},
	})

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.Usage != nil {
		t.Fatalf("thread.Serf.Usage = %+v, want nil (an unopened fork has spent nothing)", thread.Serf.Usage)
	}
	if thread.Serf.Cost != "" {
		t.Fatalf("thread.Serf.Cost = %q, want \"\" (no total means no cost, never ~$0.00)", thread.Serf.Cost)
	}
}

// An unreadable or legacy-format transcript must leave the total ABSENT and
// still project the rest of the thread. Reporting "unknown" is honest; guessing
// is not, and a missing token total is not a reason to fail the whole read.
func TestPastEntryThread_UnreadableTranscriptLeavesUsageAbsentWithoutFailing(t *testing.T) {
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000005", 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.Usage != nil || thread.Serf.Cost != "" {
		t.Fatalf("legacy-transcript thread usage=%+v cost=%q, want both absent", thread.Serf.Usage, thread.Serf.Cost)
	}
	if thread.ID != entry.Meta.ID {
		t.Fatalf("thread.ID = %q, want the rest of the projection intact", thread.ID)
	}
}

// A session with no transcript at all (the meta exists, the file does not)
// reports absent rather than erroring the read.
func TestPastEntryThread_MissingTranscriptLeavesUsageAbsentWithoutFailing(t *testing.T) {
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000006", 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	if err := os.Remove(filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")); err != nil {
		t.Fatal(err)
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.Usage != nil || thread.Serf.Cost != "" {
		t.Fatalf("no-transcript thread usage=%+v cost=%q, want both absent", thread.Serf.Usage, thread.Serf.Cost)
	}
}

// The derived total covers the WHOLE transcript, so it must not change with the
// window thread/read happens to return. This is the difference between the
// server's honest total and the client's "tokens (loaded turns)" fallback.
func TestPastThreadRead_DerivedUsageIsIndependentOfTheTurnWindow(t *testing.T) {
	usages := make([]llm.Usage, 60)
	for i := range usages {
		usages[i] = llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	}
	entry := seedPastSessionWithUsage(t, "02wMz5Txv6USAGE000007", 0, usages)
	idx := hubcore.NewPastIndex(filepath.Join(entry.StateDir, "..", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: idx}

	windowed, ok := requirePastThreadReadResponse(t, cfg, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, TurnLimit: 10,
	})
	if !ok {
		t.Fatal("past thread not found")
	}
	if windowed.OlderCursor == "" {
		t.Fatalf("expected a truncated window (OlderCursor set) over 60 turns; got %d turns uncut", len(windowed.Thread.Turns))
	}
	if windowed.Thread.Serf.Usage == nil {
		t.Fatalf("windowed read thread.Serf.Usage = nil, want the full-transcript total")
	}
	want := appwire.SerfUsage{InputTokens: 6000, OutputTokens: 600, TotalTokens: 6600}
	if got := *windowed.Thread.Serf.Usage; got != want {
		t.Fatalf("windowed read usage = %+v, want the full %+v (a window must not shrink the session total)", got, want)
	}
}
