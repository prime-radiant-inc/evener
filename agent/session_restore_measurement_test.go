package agent

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestSavedInputCountSurvivesARealRestore drives the whole path the unit tests
// only split up: a scripted reply's input count is saved by Meta, the session
// closes, and RestoreSessionFromMetaWithConfig must install that count so
// ContextStatus prices the restored history from it plus the delta for the reply
// appended after the measured boundary. It fails if the restore stops seeding
// the count, which the split tests cannot see.
func TestSavedInputCountSurvivesARealRestore(t *testing.T) {
	const saved = 600
	stateDir := t.TempDir()
	sess := newSession(t, withStateDir(stateDir), withSteps(func(llm.Request) llm.Response {
		resp := agenttest.FinalResponse("done")
		resp.Usage.InputTokens = saved
		return resp
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	meta := sess.Meta()
	if meta.LastInputTokens != saved {
		t.Fatalf("seeded meta LastInputTokens = %d, want %d", meta.LastInputTokens, saved)
	}
	sess.Close()

	client := llm.NewClient()
	client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("again") }))
	restored, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(stateDir), meta, RestoreSessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	metrics, _ := restored.ContextStatus()
	if metrics.Used < saved {
		t.Fatalf("ContextStatus after restore = %d, want at least the saved %d: the resume did not install the persisted count", metrics.Used, saved)
	}
	if metrics.Used == saved {
		t.Fatalf("ContextStatus after restore = %d, want more than the saved %d: the reply appended after the measured boundary is missing", metrics.Used, saved)
	}
	if metrics.Used > saved+64 {
		t.Fatalf("ContextStatus after restore = %d, want no more than %d: the whole history was re-estimated on top of the saved count", metrics.Used, saved+64)
	}
}

// placeSessionRoundTrip writes turns the way a live session does: Record with
// PlaceSession mints a transcript TurnID and stamps it on the persisted copy,
// unlike Append, which leaves the turn verbatim. It returns the history a
// resume rebuilds from those entries.
func placeSessionRoundTrip(t *testing.T, turns []schema.Turn) []schema.Turn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "round-trip.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "anchor-round-trip"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for _, turn := range turns {
		if _, err := writer.Record(turn, transcript.RecordOptions{Door: transcript.DoorDeferred, Place: transcript.PlaceSession}); err != nil {
			t.Fatalf("record turn: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	_, entries, _, err := readTranscript(context.Background(), path, "")
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return ResumeHistory(entries)
}

// TestPrefixAnchorSurvivesATranscriptRoundTrip pins the anchor against the two
// turn shapes a session actually holds. Live history turns carry only what their
// producer stamped (a user input's conversation id, an assistant response's id,
// a tool result's call id, or just the timestamp NewTurn sets); a PlaceSession
// write adds a TurnID to its persisted copy, and restore rebuilds history from
// that stamped entry. Every prefix must anchor to the same value before and
// after the round trip, or a resume would discard a count that still covers the
// history it restored.
func TestPrefixAnchorSurvivesATranscriptRoundTrip(t *testing.T) {
	live := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")),
		// A single-result turn carries its call id on the message.
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_1", "read_file", "body", false)),
		// A parallel-results turn carries its call ids only on the parts, so the
		// timestamp NewTurn set is the identity the anchor falls back to.
		schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind:       llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: "call_2", Content: "body"},
			}},
		}),
		schema.NewTurn(schema.TurnUserInput, llm.User("second")),
	}
	// The producer-stamped identities a client mutation and a provider response
	// leave behind must be the anchor on both sides too, not just a fallback.
	live[0].StableTurnID = "stable_1"
	live[1].ResponseID = "resp_1"
	restored := placeSessionRoundTrip(t, live)
	if len(restored) != len(live) {
		t.Fatalf("restored %d turns, want %d", len(restored), len(live))
	}
	for i := range live {
		if restored[i].TurnID == "" {
			t.Fatalf("restored turn %d has no TurnID: the fixture did not exercise PlaceSession stamping", i)
		}
		liveAnchor := contextmgr.PrefixAnchor(live, i+1)
		if liveAnchor == "" {
			t.Fatalf("live anchor at %d is empty: nothing names the measured prefix", i+1)
		}
		if got := contextmgr.PrefixAnchor(restored, i+1); got != liveAnchor {
			t.Fatalf("anchor at %d after restore = %q, want %q: the transcript's TurnID must not change the measured prefix's identity", i+1, got, liveAnchor)
		}
	}
}

// TestSavedInputCountSurvivesResumeOfPlaceSessionTurns pins the count, not just
// the anchor: a measurement taken over live turns must still price a history
// rebuilt from the PlaceSession-stamped transcript entries. The boundary turn is
// a parallel tool-results turn, which has no producer id, so the timestamp is
// what carries the identity across the round trip.
func TestSavedInputCountSurvivesResumeOfPlaceSessionTurns(t *testing.T) {
	const saved = 600
	profile := NewOpenAIProfile("gpt-5.2")
	live := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")),
		schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind:       llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: "call_2", Content: "body"},
			}},
		}),
	}
	cm := contextmgr.NewManager(profile, nil, nil)
	if !cm.RecordInputTokensFor(profile, saved, len(live), contextmgr.PrefixAnchor(live, len(live))) {
		t.Fatal("the manager's own profile was refused")
	}
	restored := placeSessionRoundTrip(t, live)
	if got := cm.EstimateUsage(restored, 0).Used; got != saved {
		t.Fatalf("EstimateUsage after resume = %d, want %d: the saved count no longer names the restored prefix", got, saved)
	}
}

// TestMetaWithholdsAFallbackModelsMeasurement pins which target a persisted count
// may name. A --model-fallback request answers on a fallback model and records
// its count under that profile; the session's configured model is what a resume
// will use, so the count must not be written into its meta. The live manager
// keeps the measurement for its own pressure estimate.
func TestMetaWithholdsAFallbackModelsMeasurement(t *testing.T) {
	sess := newSession(t)
	fallback := NewOpenAIProfile("gpt-5.2-mini")
	sess.contextMgr.SetProfile(fallback)
	if !sess.contextMgr.RecordInputTokensFor(fallback, 600, 3, "response:resp_1|ASSISTANT") {
		t.Fatal("the fallback profile was refused")
	}
	if got := sess.contextMgr.LastInputTokens(); got != 600 {
		t.Fatalf("live LastInputTokens = %d, want 600: the fallback's count still prices this session", got)
	}

	meta := sess.Meta()
	if meta.LastInputTokens != 0 {
		t.Fatalf("LastInputTokens = %d, want 0: the fallback's count must not be persisted under the configured model", meta.LastInputTokens)
	}
	if meta.LastInputMeasuredLen == nil || *meta.LastInputMeasuredLen != 0 {
		t.Fatalf("LastInputMeasuredLen = %v, want 0", meta.LastInputMeasuredLen)
	}
	if meta.LastInputAnchor != "" {
		t.Fatalf("LastInputAnchor = %q, want empty", meta.LastInputAnchor)
	}
	if got, want := meta.Model, sess.currentProfile().Model(); got != want {
		t.Fatalf("Model = %q, want the configured %q", got, want)
	}
}

// TestMetaPersistsTheMeasuredInputPrefix pins the three values a saved count
// travels with: the count, the history length it was measured at, and the anchor
// identifying the last turn it covers, plus the target's protocol. A restore
// that lacks any of them either over-claims the prefix or charges the count to
// another target.
func TestMetaPersistsTheMeasuredInputPrefix(t *testing.T) {
	sess := newSession(t)
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second")),
	}
	for i := range history {
		history[i].TurnID = "turn_" + strconv.Itoa(i)
	}
	// The count belongs to the first two turns: the third was appended after the
	// provider's measurement, as an accepted input is before the next response.
	sess.mu.Lock()
	sess.history = history
	sess.mu.Unlock()
	sess.contextMgr.RecordInputTokens(600, 2, contextmgr.PrefixAnchor(history, 2))

	meta := sess.Meta()
	if meta.LastInputTokens != 600 {
		t.Fatalf("LastInputTokens = %d, want 600", meta.LastInputTokens)
	}
	if meta.LastInputMeasuredLen == nil {
		t.Fatal("LastInputMeasuredLen is nil, want the measured length 2")
	}
	if got := *meta.LastInputMeasuredLen; got != 2 {
		t.Fatalf("LastInputMeasuredLen = %d, want 2", got)
	}
	if want := contextmgr.PrefixAnchor(history, 2); meta.LastInputAnchor != want {
		t.Fatalf("LastInputAnchor = %q, want %q", meta.LastInputAnchor, want)
	}
	if got, want := meta.Protocol, sess.currentProfile().Protocol(); got != want {
		t.Fatalf("Protocol = %q, want %q", got, want)
	}
}

// TestRestoreInputMeasurement pins which persisted count a resume may seed. The
// count belongs only to the target that produced it, its measured length is the
// one saved beside it rather than the restored history's length, and a meta from
// before the length was persisted keeps the old restored-length seed.
func TestRestoreInputMeasurement(t *testing.T) {
	profile := NewOpenAIProfile("gpt-5.2")
	three := 3
	zero := 0
	base := func() schema.SessionMeta {
		return schema.SessionMeta{
			ProfileID:            profile.ID(),
			Model:                profile.Model(),
			Protocol:             profile.Protocol(),
			LastInputTokens:      600,
			LastInputMeasuredLen: &three,
			LastInputAnchor:      "turn:turn_1|ASSISTANT",
		}
	}
	tests := []struct {
		name       string
		meta       schema.SessionMeta
		profile    *provider.Profile
		historyLen int
		wantOK     bool
		wantTokens int
		wantLen    int
		wantAnchor string
	}{
		{
			name: "persisted length and anchor",
			meta: base(), profile: profile, historyLen: 9,
			wantOK: true, wantTokens: 600, wantLen: 3, wantAnchor: "turn:turn_1|ASSISTANT",
		},
		{
			name: "length zero means measured over no turns",
			meta: func() schema.SessionMeta {
				m := base()
				m.LastInputMeasuredLen = &zero
				return m
			}(), profile: profile, historyLen: 9,
			wantOK: true, wantTokens: 600, wantLen: 0, wantAnchor: "turn:turn_1|ASSISTANT",
		},
		{
			name: "legacy meta without a persisted length",
			meta: func() schema.SessionMeta {
				m := base()
				m.LastInputMeasuredLen = nil
				m.Protocol = ""
				return m
			}(), profile: profile, historyLen: 9,
			wantOK: true, wantTokens: 600, wantLen: 9, wantAnchor: "turn:turn_1|ASSISTANT",
		},
		{
			name: "different model",
			meta: func() schema.SessionMeta {
				m := base()
				m.Model = "gpt-5.2-mini"
				return m
			}(), profile: profile, historyLen: 9,
		},
		{
			name: "different instance",
			meta: func() schema.SessionMeta {
				m := base()
				m.ProfileID = "anthropic"
				return m
			}(), profile: profile, historyLen: 9,
		},
		{
			name: "different protocol",
			meta: func() schema.SessionMeta {
				m := base()
				m.Protocol = registry.ProtocolOpenAIChat
				return m
			}(), profile: profile, historyLen: 9,
		},
		{
			name: "no count",
			meta: func() schema.SessionMeta {
				m := base()
				m.LastInputTokens = 0
				return m
			}(), profile: profile, historyLen: 9,
		},
		{
			name: "nil profile",
			meta: base(), profile: nil, historyLen: 9,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tokens, measuredLen, anchor, ok := restoreInputMeasurement(&tc.meta, tc.profile, tc.historyLen)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if tokens != tc.wantTokens || measuredLen != tc.wantLen || anchor != tc.wantAnchor {
				t.Fatalf("measurement = (%d, %d, %q), want (%d, %d, %q)",
					tokens, measuredLen, anchor, tc.wantTokens, tc.wantLen, tc.wantAnchor)
			}
		})
	}
}
