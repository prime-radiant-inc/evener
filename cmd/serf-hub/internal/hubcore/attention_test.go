package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func TestDeriveAttention_SummaryCountsTierEligibleOnly(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01SUB", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}, IsSubagent: true, ParentSessionID: "01A"},
		{ID: "01ARCH", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01WORK", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01SUB", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ARCH", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 4}, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
		{Entry: rendezvous.Entry{PID: 5}, SessionID: "01WORK", Status: appwire.ThreadStatusActive},
	}
	decisions := map[ArchiveKey]bool{{Kind: "session", ID: "01ARCH"}: true}
	m, sum := DeriveAttention(metas, live, decisions)
	if sum.NeedsYou != 1 || sum.Error != 1 || sum.Working != 1 {
		t.Fatalf("summary = %+v, want NeedsYou:1 Error:1 Working:1 (subagent + archived excluded)", sum)
	}
	if m["01A"].Level != "needs_you" || m["01ERR"].Level != "error" || m["01WORK"].Level != "working" {
		t.Fatalf("levels = %v", m)
	}
	if _, ok := m["01SUB"]; ok {
		t.Fatal("subagent must not carry attention")
	}
	if _, ok := m["01ARCH"]; ok {
		t.Fatal("archived must not carry attention")
	}
}

func TestDeriveAttention_StaleUnarchivedNeverDecays(t *testing.T) {
	// A live awaiting session whose meta is older than the sidebar's 14-day
	// age-archive window must STILL carry attention unless the user explicitly
	// archived it: the badge summary is defined as the tier-eligible set, and
	// the NeedsYou tier suppresses only manual archive decisions (tree.go).
	// needs_you never decays (spec v5) — age-based auto-archive must not
	// silently drop a session from the badge while the tier still shows it.
	now := time.Now()
	stale := now.Add(-30 * 24 * time.Hour)
	metas := []schema.SessionMeta{
		{ID: "01STALE", UpdatedAt: stale, CreatedAt: stale, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01STALEUN", UpdatedAt: stale, CreatedAt: stale, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01STALE", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01STALEUN", Status: appwire.ThreadStatusAwaiting},
	}
	// 01STALE has NO decision; 01STALEUN has an explicit un-archive (false)
	// decision. Both are tier-eligible: only decision==true suppresses.
	decisions := map[ArchiveKey]bool{{Kind: "session", ID: "01STALEUN"}: false}
	m, sum := DeriveAttention(metas, live, decisions)
	if sum.NeedsYou != 2 {
		t.Fatalf("summary = %+v, want NeedsYou:2 (stale-but-live sessions never decay out of the badge)", sum)
	}
	if m["01STALE"].Level != "needs_you" {
		t.Fatalf("stale undecided session = %+v, want Level needs_you (no age decay)", m["01STALE"])
	}
	if m["01STALEUN"].Level != "needs_you" {
		t.Fatalf("stale explicitly-unarchived session = %+v, want Level needs_you", m["01STALEUN"])
	}
}

func TestAttentionWatcher_DiffEmitsOncePerChangeAndSeedsSilently(t *testing.T) {
	var emitted []AttentionChangedPayload
	w := NewAttentionWatcher(func(p AttentionChangedPayload) { emitted = append(emitted, p) })
	first := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you"}}
	w.Tick(first, AttentionSummary{NeedsYou: 1})
	if len(emitted) != 0 {
		t.Fatalf("first tick must seed silently, emitted %d", len(emitted))
	}
	w.Tick(first, AttentionSummary{NeedsYou: 1})
	if len(emitted) != 0 {
		t.Fatal("no change, no emit")
	}
	second := map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	w.Tick(second, AttentionSummary{Working: 1})
	if len(emitted) != 1 || len(emitted[0].Changed) != 1 ||
		emitted[0].Changed[0].Level != "working" || emitted[0].Changed[0].PrevLevel != "needs_you" {
		t.Fatalf("emitted = %+v", emitted)
	}
	// A session disappearing (daemon gone) transitions to idle-family: emit with prevLevel.
	w.Tick(map[string]AttentionEntry{}, AttentionSummary{})
	if len(emitted) != 2 || emitted[1].Changed[0].Level != "idle" {
		t.Fatalf("disappearance emit = %+v", emitted)
	}
}

func TestDeriveAttention_CarriesAskPending(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{SessionID: "01A", Status: "awaiting", PendingAsk: true}}
	entries, _ := DeriveAttention(metas, live, nil)
	if !entries["01A"].AskPending {
		t.Fatalf("expected AttentionEntry.AskPending=true, got %+v", entries["01A"])
	}
}

func TestAttentionWatcher_TicksOnAskOnlyFlip(t *testing.T) {
	var got []AttentionChangedPayload
	w := NewAttentionWatcher(func(p AttentionChangedPayload) { got = append(got, p) })
	base := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: false}}
	w.Tick(base, AttentionSummary{}) // seed, silent

	flipped := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true}}
	w.Tick(flipped, AttentionSummary{})
	if len(got) != 1 {
		t.Fatalf("expected one emitted payload for an ask-only flip (Level unchanged), got %d", len(got))
	}
	if !got[0].Changed[0].AskPending {
		t.Fatalf("changed entry must carry the new AskPending=true, got %+v", got[0].Changed[0])
	}
}
