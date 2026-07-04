package hubcore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
)

func TestStallThresholdIsThreeMinutes(t *testing.T) {
	if StallThreshold != 3*time.Minute {
		t.Fatalf("StallThreshold = %v, want 3m", StallThreshold)
	}
}

// writeWedgeEntry writes a single-line transcript at
// <stateDir>/sessions/<sessionID>.transcript.jsonl whose only (and therefore
// tail) line is tailJSON, and returns the PastEntry WedgedStatus needs to
// find it — the same StateDir/Meta.ID shape cfg.Past.Find produces in
// production.
func writeWedgeEntry(t *testing.T, sessionID, tailJSON string) PastEntry {
	t.Helper()
	stateDir := t.TempDir()
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, sessionID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(tailJSON+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return PastEntry{ID: sessionID, Meta: schema.SessionMeta{ID: sessionID}, StateDir: stateDir}
}

func TestWedgedStatusFlipsOnFailedAPICallTail(t *testing.T) {
	// Port of TestSanitizeStaleProcessingStatusFlipsFailedAPICallToError
	// (app_rpc_test.go): a tail api_call with a non-empty Error means the
	// session is wedged (kata r6y9).
	entry := writeWedgeEntry(t, "01FAILED0000000000000001",
		`{"kind":"api_call","seq":2,"error":"configuration error: unknown provider: openai"}`)
	if !WedgedStatus(entry) {
		t.Fatal("want wedged for tail api_call with Error set")
	}
}

func TestWedgedStatusLeavesCompletedAssistantTailAlone(t *testing.T) {
	// Port of TestSanitizeStaleProcessingStatusLeavesCompletedAssistantTailAlone:
	// mid-flight processing (a completed assistant turn as the tail) must not
	// be reported as wedged.
	entry := writeWedgeEntry(t, "01MIDFLIGHT000000000001",
		`{"kind":"entry","seq":99,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi back"}]}}}`)
	if WedgedStatus(entry) {
		t.Fatal("completed assistant turn tail must not be reported as wedged")
	}
}

func TestWedgedStatusLeavesUserInputTailAlone(t *testing.T) {
	// Port of TestSanitizeStaleProcessingStatusLeavesUserInputTailAlone: a
	// bare USER_INPUT tail with no api_call yet could mean the agent is
	// genuinely preparing its first LLM call — conservatively not wedged.
	entry := writeWedgeEntry(t, "01USERIN00000000000001",
		`{"kind":"entry","seq":3,"turn":{"kind":"USER_INPUT"}}`)
	if WedgedStatus(entry) {
		t.Fatal("USER_INPUT tail must not be reported as wedged")
	}
}

func TestWedgedStatusLeavesSuccessfulAPICallTailAlone(t *testing.T) {
	// An api_call tail with no Error means the daemon may legitimately still
	// be mid-round.
	entry := writeWedgeEntry(t, "01OKCALL00000000000001", `{"kind":"api_call","seq":2}`)
	if WedgedStatus(entry) {
		t.Fatal("successful api_call tail must not be reported as wedged")
	}
}

func TestWedgedStatusMissingTranscriptIsNotWedged(t *testing.T) {
	entry := PastEntry{ID: "01MISSING", Meta: schema.SessionMeta{ID: "01MISSING"}, StateDir: t.TempDir()}
	if WedgedStatus(entry) {
		t.Fatal("missing transcript must not be reported as wedged")
	}
}

func TestStaleActivesNewlyWorkingIsNotStale(t *testing.T) {
	seen := map[string]time.Time{}
	cur := map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	now := time.Now()
	stale := StaleActives(seen, cur, now)
	if len(stale) != 0 {
		t.Fatalf("stale = %v, want none (just started working)", stale)
	}
	if got := seen["01A"]; !got.Equal(now) {
		t.Fatalf("seen[01A] = %v, want %v (first-seen recorded)", got, now)
	}
}

func TestStaleActivesContinuousPastThresholdIsStale(t *testing.T) {
	seen := map[string]time.Time{}
	cur := map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	t0 := time.Now()
	StaleActives(seen, cur, t0) // seeds first-seen
	stale := StaleActives(seen, cur, t0.Add(StallThreshold))
	if len(stale) != 1 || stale[0] != "01A" {
		t.Fatalf("stale = %v, want [01A]", stale)
	}
}

func TestStaleActivesBelowThresholdIsNotYetStale(t *testing.T) {
	seen := map[string]time.Time{}
	cur := map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	t0 := time.Now()
	StaleActives(seen, cur, t0)
	stale := StaleActives(seen, cur, t0.Add(StallThreshold-time.Second))
	if len(stale) != 0 {
		t.Fatalf("stale = %v, want none (still below threshold)", stale)
	}
}

func TestStaleActivesFlippedToNonWorkingDropsTracking(t *testing.T) {
	seen := map[string]time.Time{}
	t0 := time.Now()
	StaleActives(seen, map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}, t0)
	if _, ok := seen["01A"]; !ok {
		t.Fatal("expected 01A tracked after first working tick")
	}
	// Flips to needs_you (no longer working): dropped from tracking.
	StaleActives(seen, map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you"}}, t0.Add(time.Minute))
	if _, ok := seen["01A"]; ok {
		t.Fatal("01A must be dropped from tracking once no longer working")
	}
	// Coming back to working later restarts the clock rather than resuming
	// the earlier window.
	restart := t0.Add(2 * time.Minute)
	StaleActives(seen, map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}, restart)
	stale := StaleActives(seen, map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}, restart.Add(time.Minute))
	if len(stale) != 0 {
		t.Fatalf("stale = %v, want none (clock restarted after the flip)", stale)
	}
}

func TestStaleActivesDisappearedDropsTracking(t *testing.T) {
	seen := map[string]time.Time{}
	t0 := time.Now()
	StaleActives(seen, map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}, t0)
	StaleActives(seen, map[string]AttentionEntry{}, t0.Add(time.Minute))
	if _, ok := seen["01A"]; ok {
		t.Fatal("01A must be dropped from tracking once it disappears from cur")
	}
}

func TestApplyWedgeOverrideMovesWorkingToErrorConsistently(t *testing.T) {
	m := map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	sum := AttentionSummary{Working: 1}
	ApplyWedgeOverride(m, &sum, "01A")
	if m["01A"].Level != "error" {
		t.Fatalf("level = %q, want error", m["01A"].Level)
	}
	if sum.Working != 0 || sum.Error != 1 {
		t.Fatalf("summary = %+v, want Working:0 Error:1", sum)
	}
}

func TestApplyWedgeOverrideNoopIfNotWorking(t *testing.T) {
	m := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you"}}
	sum := AttentionSummary{NeedsYou: 1}
	ApplyWedgeOverride(m, &sum, "01A")
	if m["01A"].Level != "needs_you" || sum.NeedsYou != 1 || sum.Error != 0 {
		t.Fatalf("non-working entry must be left alone: m=%+v sum=%+v", m, sum)
	}
}

func TestApplyWedgeOverrideNoopIfIDMissing(t *testing.T) {
	m := map[string]AttentionEntry{}
	sum := AttentionSummary{}
	ApplyWedgeOverride(m, &sum, "nope")
	if len(m) != 0 || sum != (AttentionSummary{}) {
		t.Fatalf("missing id must be a no-op: m=%+v sum=%+v", m, sum)
	}
}
