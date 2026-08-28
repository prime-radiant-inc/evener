package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// seedPastSessionWithUsage writes a real transcript whose assistant entries
// carry the given per-turn usage, plus a meta with NO CumulativeUsage — the
// shape agent/fork.go's writeForkChild actually produces. divergenceTurn > 0
// makes it a fork child, and the entries before that ordinal are the inherited
// parent prefix. Returns a config whose past index sees only this session, plus
// the thread ref for it.
//
// The id is minted rather than passed in: PastIndex.Rebuild drops any session
// whose id fails identifier.ValidateSessionID (a 22-char base62 UUIDv7 payload)
// and says nothing about it, so a readable placeholder makes the seeded session
// invisible to every reader.
func seedPastSessionWithUsage(t testing.TB, divergenceTurn int, usages []llm.Usage) (hubcore.WebConfig, hubcore.PastEntry) {
	t.Helper()
	sessionID := identifier.MustNewSessionID()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-usage-0000000000")
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	meta := schema.SessionMeta{
		ID: sessionID, ProfileID: "anthropic", Model: "claude-opus-4-5",
		CreatedAt: now, UpdatedAt: now, TurnCount: len(usages),
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
	// one fsync per Append (same reason seedBoundedPastThread does).
	w.SyncInterval = time.Hour
	for _, u := range usages {
		if err := w.Append(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("saved turn"), Usage: u}); err != nil {
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
	return hubcore.WebConfig{Past: idx, Registry: pricingRegistry(t)}, entry
}

func readSeededThread(t testing.TB, cfg hubcore.WebConfig, entry hubcore.PastEntry) appwire.Thread {
	t.Helper()
	thread, ok := requirePastThreadForRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + entry.Meta.ID})
	if !ok {
		t.Fatalf("past thread %s not found", entry.Meta.ID)
	}
	return thread
}

// THE KATA. A fork child's meta carries no CumulativeUsage (writeForkChild does
// not stamp it), so evener.usage and evener.cost arrived empty and the client could
// only sum the turns it happened to hold. The read path now sums the session's
// own span of the full transcript.
func TestPastThreadRead_DerivesSessionUsageFromFullTranscriptWhenMetaHasNone(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
		{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200},
		{InputTokens: 3000, OutputTokens: 300, TotalTokens: 3300},
	})

	thread := readSeededThread(t, cfg, entry)

	if thread.Evener.Usage == nil {
		t.Fatalf("thread.Evener.Usage = nil; want the full-transcript sum, not an empty total")
	}
	want := appwire.EvenerUsage{InputTokens: 6000, OutputTokens: 600, TotalTokens: 6600}
	if got := *thread.Evener.Usage; got != want {
		t.Fatalf("thread.Evener.Usage = %+v, want %+v", got, want)
	}
	if !strings.HasPrefix(thread.Evener.Cost, "~$") {
		t.Fatalf("thread.Evener.Cost = %q, want a ~$ estimate derived from the recovered total", thread.Evener.Cost)
	}
}

// A fork's tokens are not the parent's. The child transcript OPENS with a
// verbatim copy of the parent's prefix, so summing the whole file would charge
// the child for spend it never made. Only the post-divergence span counts.
func TestPastThreadRead_ForkUsageExcludesTheInheritedParentPrefix(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 3, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100}, // parent's, entry 1
		{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200}, // parent's, entry 2
		{InputTokens: 4000, OutputTokens: 400, TotalTokens: 4400}, // the child's own, entry 3
		{InputTokens: 5000, OutputTokens: 500, TotalTokens: 5500}, // the child's own, entry 4
	})

	thread := readSeededThread(t, cfg, entry)

	if thread.Evener.Usage == nil {
		t.Fatalf("thread.Evener.Usage = nil, want the child's own post-divergence total")
	}
	want := appwire.EvenerUsage{InputTokens: 9000, OutputTokens: 900, TotalTokens: 9900}
	if got := *thread.Evener.Usage; got != want {
		t.Fatalf("thread.Evener.Usage = %+v, want %+v (the parent's 3000/300 prefix must not be counted)", got, want)
	}
}

// A meta that DOES carry CumulativeUsage is authoritative: it is the daemon's
// own running total, and re-deriving would risk a second, disagreeing figure.
// Proven with a transcript whose per-turn sum deliberately differs.
func TestPastThreadRead_PersistedCumulativeUsageWinsOverTheDerivedSum(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	entry.Meta.CumulativeUsage = schema.CumulativeUsage{InputTokens: 7777, OutputTokens: 777, TotalTokens: 8554}
	if err := schema.SaveSessionMeta(entry.StateDir, entry.Meta); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	thread := readSeededThread(t, cfg, entry)

	if thread.Evener.Usage == nil || thread.Evener.Usage.InputTokens != 7777 {
		t.Fatalf("thread.Evener.Usage = %+v, want the persisted 7777 total, not the derived 1000", thread.Evener.Usage)
	}
}

// An aside fork holds only the inherited prefix until someone opens and runs it.
// It has spent nothing, and the honest report for "nothing measured" is an
// ABSENT total — never a zero that renders as a real "↑0 ↓0" or a "~$0.00".
func TestPastThreadRead_UnopenedForkReportsAbsentUsageNotZero(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 3, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
		{InputTokens: 2000, OutputTokens: 200, TotalTokens: 2200},
	})

	thread := readSeededThread(t, cfg, entry)

	if thread.Evener.Usage != nil {
		t.Fatalf("thread.Evener.Usage = %+v, want nil (an unopened fork has spent nothing)", thread.Evener.Usage)
	}
	if thread.Evener.Cost != "" {
		t.Fatalf("thread.Evener.Cost = %q, want \"\" (no total means no cost, never ~$0.00)", thread.Evener.Cost)
	}
}

// A legacy format_version 1 transcript must leave the total ABSENT and still
// project the rest of the thread. Reporting "unknown" is honest; guessing is
// not, and a missing token total is no reason to fail the whole read. This is
// the bulk of a real state dir: 1580 of 1592 transcripts on the author's
// machine are still v1.
func TestPastThreadRead_LegacyTranscriptLeavesUsageAbsentWithoutFailing(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	thread := readSeededThread(t, cfg, entry)

	if thread.Evener.Usage != nil || thread.Evener.Cost != "" {
		t.Fatalf("legacy-transcript thread usage=%+v cost=%q, want both absent", thread.Evener.Usage, thread.Evener.Cost)
	}
	if thread.ID != entry.Meta.ID {
		t.Fatalf("thread.ID = %q, want the rest of the projection intact", thread.ID)
	}
}

// A session whose transcript is gone (the meta survives, the file does not)
// reports absent rather than erroring the read.
func TestPastThreadRead_MissingTranscriptLeavesUsageAbsentWithoutFailing(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})
	if err := os.Remove(filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")); err != nil {
		t.Fatal(err)
	}

	thread := readSeededThread(t, cfg, entry)

	if thread.Evener.Usage != nil || thread.Evener.Cost != "" {
		t.Fatalf("no-transcript thread usage=%+v cost=%q, want both absent", thread.Evener.Usage, thread.Evener.Cost)
	}
}

// THE POINT of doing this server-side. The derived total covers the WHOLE
// transcript, so it must not change with the window thread/read returns. This is
// exactly the difference between the server's honest full-session figure and the
// client's "tokens (loaded turns)" fallback.
func TestPastThreadRead_DerivedUsageIsIndependentOfTheTurnWindow(t *testing.T) {
	usages := make([]llm.Usage, 60)
	for i := range usages {
		usages[i] = llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	}
	cfg, entry := seedPastSessionWithUsage(t, 0, usages)

	windowed, ok := requirePastThreadReadResponse(t, cfg, appwire.ThreadReadParams{
		Ref: "local:" + entry.Meta.ID, IncludeTurns: true, TurnLimit: 10,
	})
	if !ok {
		t.Fatal("past thread not found")
	}
	if windowed.OlderCursor == "" {
		t.Fatalf("expected a truncated window (OlderCursor set) over 60 turns; got %d turns uncut", len(windowed.Thread.Turns))
	}
	if windowed.Thread.Evener.Usage == nil {
		t.Fatalf("windowed read thread.Evener.Usage = nil, want the full-transcript total")
	}
	want := appwire.EvenerUsage{InputTokens: 6000, OutputTokens: 600, TotalTokens: 6600}
	if got := *windowed.Thread.Evener.Usage; got != want {
		t.Fatalf("windowed read usage = %+v, want the full %+v (a window must not shrink the session total)", got, want)
	}
}

// The per-entry list sweep must NOT pay a transcript scan per session: on a real
// state dir that is a read of every transcript there (~1s measured over 1592).
// pastEntryThread is the sweep's projector, so it stays on the persisted field
// alone, and an absent total there is the honest cost of that bound.
func TestPastEntryThread_DoesNotDeriveUsageOnTheListSweepPath(t *testing.T) {
	cfg, entry := seedPastSessionWithUsage(t, 0, []llm.Usage{
		{InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100},
	})

	thread := requirePastEntryThread(t, cfg, entry, false)

	if thread.Evener.Usage != nil {
		t.Fatalf("pastEntryThread usage = %+v, want nil; the sweep projector must not scan transcripts", thread.Evener.Usage)
	}
}
