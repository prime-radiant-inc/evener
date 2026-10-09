package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func fuzzScenarioDeriveAttention_SummaryCountsTierEligibleOnly(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01SUB", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}, IsSubagent: true, ParentSessionID: "01A"},
		{ID: "01ARCH", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01WORK", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01A", Status: appwire.ThreadStatusAwaiting},
		{PID: 2, SessionID: "01SUB", Status: appwire.ThreadStatusAwaiting},
		{PID: 3, SessionID: "01ARCH", Status: appwire.ThreadStatusAwaiting},
		{PID: 4, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
		{PID: 5, SessionID: "01WORK", Status: appwire.ThreadStatusActive},
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

func fuzzScenarioDeriveAttention_StaleUnarchivedNeverDecays(t *testing.T) {
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
		{PID: 1, SessionID: "01STALE", Status: appwire.ThreadStatusAwaiting},
		{PID: 2, SessionID: "01STALEUN", Status: appwire.ThreadStatusAwaiting},
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

func fuzzScenarioAttentionWatcher_DiffEmitsOncePerChangeAndSeedsSilently(t *testing.T) {
	var emitted []appwire.AttentionChangedPayload
	w := NewAttentionWatcher(func(p appwire.AttentionChangedPayload) { emitted = append(emitted, p) })
	first := map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you"}}
	w.Tick(first, appwire.AttentionSummary{NeedsYou: 1})
	if len(emitted) != 0 {
		t.Fatalf("first tick must seed silently, emitted %d", len(emitted))
	}
	w.Tick(first, appwire.AttentionSummary{NeedsYou: 1})
	if len(emitted) != 0 {
		t.Fatal("no change, no emit")
	}
	second := map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	w.Tick(second, appwire.AttentionSummary{Working: 1})
	if len(emitted) != 1 || len(emitted[0].Changed) != 1 ||
		emitted[0].Changed[0].Level != "working" || emitted[0].Changed[0].PrevLevel != "needs_you" {
		t.Fatalf("emitted = %+v", emitted)
	}
	// A session disappearing (daemon gone) transitions to idle-family: emit with prevLevel.
	w.Tick(map[string]appwire.AttentionEntry{}, appwire.AttentionSummary{})
	if len(emitted) != 2 || emitted[1].Changed[0].Level != "idle" {
		t.Fatalf("disappearance emit = %+v", emitted)
	}
}

func fuzzScenarioDeriveAttention_CarriesAskPending(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{SessionID: "01A", Status: "awaiting", PendingAsk: true}}
	entries, _ := DeriveAttention(metas, live, nil)
	if !entries["01A"].AskPending {
		t.Fatalf("expected AttentionEntry.AskPending=true, got %+v", entries["01A"])
	}
}

// fuzzScenarioDeriveAttention_CarriesNeedsResponse: a session that rests
// awaiting without a pending question ended its turn with needs_response, and
// the entry says so, so a client can treat a needs-your-reply like a question.
// A plain reply rests idle, so it never carries the flag.
func fuzzScenarioDeriveAttention_CarriesNeedsResponse(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	for _, tc := range []struct {
		name string
		live LiveEntry
		want bool
	}{
		{"needs_response rest", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusAwaiting}, true},
		{"pending question", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true}, false},
		{"plain reply", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusIdle}, false},
		{"pending approval", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusActive, PendingEscalation: true}, false},
		{"approval over a needs_response rest", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingEscalation: true}, true},
		{"failure", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusSystemError}, false},
		{"warning", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusWarning}, false},
		{"restart required", LiveEntry{SessionID: "01A", Status: appwire.ThreadStatusRestartRequired}, false},
	} {
		entries, _ := DeriveAttention(metas, []LiveEntry{tc.live}, nil)
		if got := entries["01A"].NeedsResponse; got != tc.want {
			t.Fatalf("%s: NeedsResponse = %v, want %v (entry %+v)", tc.name, got, tc.want, entries["01A"])
		}
	}
}

// fuzzScenarioDeriveAttention_CarriesApprovalPending: the attention entry says
// why an escalation-promoted session needs you, beside the promotion, so a
// client can show an approval.
func fuzzScenarioDeriveAttention_CarriesApprovalPending(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{SessionID: "01A", Status: appwire.ThreadStatusActive, PendingEscalation: true}}
	entries, _ := DeriveAttention(metas, live, nil)
	if got := entries["01A"]; !got.ApprovalPending || got.Level != "needs_you" {
		t.Fatalf("entry = %+v, want ApprovalPending at level needs_you", got)
	}
	live = []LiveEntry{{SessionID: "01A", Status: appwire.ThreadStatusActive}}
	entries, _ = DeriveAttention(metas, live, nil)
	if entries["01A"].ApprovalPending {
		t.Fatalf("entry without an escalation carries ApprovalPending: %+v", entries["01A"])
	}
}

func fuzzScenarioDeriveAttention_PendingEscalationPromotesToNeedsYou(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	// A sandbox escalation blocks MID-TURN, so the daemon status is still "active"
	// (level "working"). The pending-escalation flag must promote it to needs_you so
	// the owning session lights up cross-session.
	live := []LiveEntry{{SessionID: "01A", Status: "active", PendingEscalation: true}}
	entries, sum := DeriveAttention(metas, live, nil)
	if entries["01A"].Level != "needs_you" {
		t.Fatalf("a pending escalation must promote an active session to needs_you, got %+v", entries["01A"])
	}
	if sum.NeedsYou != 1 || sum.Working != 0 {
		t.Fatalf("summary must count it as needs_you, got %+v", sum)
	}

	// Clearing the escalation returns it to working (no residual attention).
	live = []LiveEntry{{SessionID: "01A", Status: "active", PendingEscalation: false}}
	entries, sum = DeriveAttention(metas, live, nil)
	if entries["01A"].Level != "working" || sum.NeedsYou != 0 {
		t.Fatalf("clearing the escalation must return the session to working, got %+v / %+v", entries["01A"], sum)
	}
}

func fuzzScenarioDeriveAttention_PendingEscalationNeverDowngradesError(t *testing.T) {
	metas := []schema.SessionMeta{{ID: "01A", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{SessionID: "01A", Status: appwire.ThreadStatusSystemError, PendingEscalation: true}}
	entries, _ := DeriveAttention(metas, live, nil)
	if entries["01A"].Level != "error" {
		t.Fatalf("a pending escalation must not downgrade an error state, got %+v", entries["01A"])
	}
}

func fuzzScenarioAttentionWatcher_TicksOnAskOnlyFlip(t *testing.T) {
	var got []appwire.AttentionChangedPayload
	w := NewAttentionWatcher(func(p appwire.AttentionChangedPayload) { got = append(got, p) })
	base := map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: false}}
	w.Tick(base, appwire.AttentionSummary{}) // seed, silent

	flipped := map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true}}
	w.Tick(flipped, appwire.AttentionSummary{})
	if len(got) != 1 {
		t.Fatalf("expected one emitted payload for an ask-only flip (Level unchanged), got %d", len(got))
	}
	if !got[0].Changed[0].AskPending {
		t.Fatalf("changed entry must carry the new AskPending=true, got %+v", got[0].Changed[0])
	}
}

// fuzzScenarioAttentionWatcher_TicksOnApprovalOnlyFlip: level and ask can hold
// still while the approval moves; a client keyed on the approval must hear it,
// and a session that goes away clears it.
func fuzzScenarioAttentionWatcher_TicksOnApprovalOnlyFlip(t *testing.T) {
	var got []appwire.AttentionChangedPayload
	w := NewAttentionWatcher(func(p appwire.AttentionChangedPayload) { got = append(got, p) })
	w.Tick(map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true}}, appwire.AttentionSummary{})
	w.Tick(map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", AskPending: true, ApprovalPending: true}}, appwire.AttentionSummary{})
	if len(got) != 1 || !got[0].Changed[0].ApprovalPending {
		t.Fatalf("payloads = %+v, want one change carrying ApprovalPending", got)
	}
	w.Tick(map[string]appwire.AttentionEntry{}, appwire.AttentionSummary{})
	if len(got) != 2 || got[1].Changed[0].ApprovalPending {
		t.Fatalf("payloads = %+v, want the gone entry with ApprovalPending cleared", got)
	}
}

// fuzzScenarioAttentionWatcher_TicksOnNeedsResponseOnlyFlip: level and ask can
// hold still while needs_response moves (a warning settling into the rest), so
// the hub reports the flip like an ask or approval flip, and a session that
// goes away clears it. The web fires only when a session enters the tier.
func fuzzScenarioAttentionWatcher_TicksOnNeedsResponseOnlyFlip(t *testing.T) {
	var got []appwire.AttentionChangedPayload
	w := NewAttentionWatcher(func(p appwire.AttentionChangedPayload) { got = append(got, p) })
	w.Tick(map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you"}}, appwire.AttentionSummary{})
	w.Tick(map[string]appwire.AttentionEntry{"01A": {ID: "01A", Level: "needs_you", NeedsResponse: true}}, appwire.AttentionSummary{})
	if len(got) != 1 || !got[0].Changed[0].NeedsResponse {
		t.Fatalf("payloads = %+v, want one change carrying NeedsResponse", got)
	}
	w.Tick(map[string]appwire.AttentionEntry{}, appwire.AttentionSummary{})
	if len(got) != 2 || got[1].Changed[0].NeedsResponse {
		t.Fatalf("payloads = %+v, want the gone entry with NeedsResponse cleared", got)
	}
}
