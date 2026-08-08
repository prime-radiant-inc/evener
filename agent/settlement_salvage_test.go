package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// This file pins settlement's observable end state, per the spec's
// "Component 3: partial-preserving settlement": which turns a terminally failed
// round persists, in what order, and which live events watching clients get so
// their screen matches the transcript a reloading client would read.

// settlementSession builds a transcript-backed session against a scripted
// adapter, with a retry budget large enough that the early-stop rules — not the
// budget — decide when a group gives up.
func settlementSession(t *testing.T, a *scriptedStreamAdapter, fallbacks ...string) *Session {
	t.Helper()
	dir := t.TempDir()
	policy := llm.RetryPolicy{MaxRetries: 10, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	return newSession(t,
		withAdapter(a),
		withProfile(NewOpenAIProfile("primary")),
		withDir(dir),
		withConfig(SessionConfig{
			StateDir:         dir,
			MaxSubagentDepth: 1,
			LLMRetryPolicy:   &policy,
			LLMSleep:         func(context.Context, time.Duration) error { return nil },
			ModelFallbacks:   fallbacks,
			testOnly:         testConfig{metaFS: afero.NewMemMapFs()},
		}),
	)
}

// settledKinds returns the kinds of every turn the round settled — the tail
// after the user input that opened the turn.
func settledKinds(turns []schema.Turn) []schema.TurnKind {
	start := 0
	for i, turn := range turns {
		if turn.Kind == schema.TurnUserInput {
			start = i + 1
		}
	}
	var kinds []schema.TurnKind
	for _, turn := range turns[start:] {
		kinds = append(kinds, turn.Kind)
	}
	return kinds
}

func sessionHistory(s *Session) []schema.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.Turn(nil), s.history...)
}

func transcriptTurns(t *testing.T, path string) []schema.Turn {
	t.Helper()
	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var out []schema.Turn
	for _, entry := range data.Entries {
		out = append(out, entry.Turn)
	}
	return out
}

func assertKinds(t *testing.T, label string, got, want []schema.TurnKind) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s settled %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s settled %v, want %v", label, got, want)
		}
	}
}

// eventKindsOfInterest filters a captured event stream down to the settlement
// sequence, so unrelated lifecycle chatter cannot make an ordering assertion
// pass or fail by accident.
func eventKindsOfInterest(evs []events.SessionEvent, want ...events.EventKind) []events.EventKind {
	keep := map[events.EventKind]bool{}
	for _, k := range want {
		keep[k] = true
	}
	var out []events.EventKind
	for _, ev := range evs {
		if keep[ev.Kind] {
			out = append(out, ev.Kind)
		}
	}
	return out
}

func snapshotEvents(evs *[]events.SessionEvent, mu *sync.Mutex) []events.SessionEvent {
	mu.Lock()
	defer mu.Unlock()
	return append([]events.SessionEvent(nil), *evs...)
}

// streamTextThenFail scripts one attempt that streams text and then dies
// mid-stream — the consume-phase shape settlement salvages from.
func streamTextThenFail(text string, err error) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: text})
		st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: err})
	}
}

// streamReasoningThenFail scripts a consume-phase death that leaves nothing
// salvageable: reasoning moves the content window but is never salvaged.
func streamReasoningThenFail(err error) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "weighing options"})
		st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: err})
	}
}

// TestSettlement_StallStreakPersistsSteeringOnly: a round whose every attempt
// died mid-stream with nothing salvageable settles the steering turn alone, and
// the presentational failure marker still follows it.
func TestSettlement_StallStreakPersistsSteeringOnly(t *testing.T) {
	t.Parallel()
	a := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"primary": streamReasoningThenFail(llm.NewStreamError("openai", "stream ended without completion", nil)),
		},
	}
	sess := settlementSession(t, a)
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "write the plan", nil); err == nil {
		t.Fatal("expected a provider error from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	hist := sessionHistory(sess)
	sess.Close()

	want := []schema.TurnKind{schema.TurnSteering, schema.TurnFailure}
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, tpath)), want)

	steering := hist[len(hist)-2]
	if !strings.Contains(steering.Message.Text(), "The provider stopped responding mid-stream") {
		t.Errorf("steering text = %q, want the stall template", steering.Message.Text())
	}
	if strings.Contains(steering.Message.Text(), "draft") {
		t.Errorf("steering text = %q must not reference a draft when nothing was salvaged", steering.Message.Text())
	}
}

// TestSettlement_SalvagedDraftPersistsBeforeSteering: a round that streamed a
// substantial partial before the provider gave up persists that partial as a
// model-visible assistant turn, then the steering, then the failure marker —
// and the salvaged turn carries no Responses-continuation metadata, which would
// point the next round at a response the provider never finalized.
func TestSettlement_SalvagedDraftPersistsBeforeSteering(t *testing.T) {
	t.Parallel()
	draft := strings.Repeat("plan step. ", 1000) // ~11KB
	var attempts int
	a := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"primary": func(st *llm.ChanStream) {
				// The first attempt streams the large draft; the retries trickle.
				// Settlement must keep the LARGEST partial, not the latest.
				text := "..."
				if attempts == 0 {
					text = draft
				}
				attempts++
				streamTextThenFail(text, llm.NewStreamError("openai", "stream ended without completion", nil))(st)
			},
		},
	}
	sess := settlementSession(t, a)
	evs, mu, _ := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "write the plan", nil); err == nil {
		t.Fatal("expected a provider error from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	hist := sessionHistory(sess)
	captured := snapshotEvents(evs, mu)
	sess.Close()

	want := []schema.TurnKind{schema.TurnAssistant, schema.TurnSteering, schema.TurnFailure}
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, tpath)), want)

	salvaged := hist[len(hist)-3]
	if salvaged.Message.Text() != draft {
		t.Errorf("salvaged turn text length = %d, want the %d-byte draft verbatim", len(salvaged.Message.Text()), len(draft))
	}
	if salvaged.ResponseModel != "primary" || salvaged.ResponseProvider == "" {
		t.Errorf("salvaged turn provenance = model %q provider %q, want the failing group's model/provider",
			salvaged.ResponseModel, salvaged.ResponseProvider)
	}
	for name, got := range map[string]string{
		"ResponseID":                      salvaged.ResponseID,
		"ResponseIDHash":                  salvaged.ResponseIDHash,
		"ResponseEndpoint":                salvaged.ResponseEndpoint,
		"ResponseEndpointFamily":          salvaged.ResponseEndpointFamily,
		"ResponseStorageScopeFingerprint": salvaged.ResponseStorageScopeFingerprint,
		"ResponseRequestFingerprint":      salvaged.ResponseRequestFingerprint,
		"ResponseContextMarker":           salvaged.ResponseContextMarker,
		"AttemptGroupID":                  salvaged.AttemptGroupID,
	} {
		if got != "" {
			t.Errorf("salvaged turn %s = %q, want empty: a partial stream has no response to continue from", name, got)
		}
	}
	if turnHasResponsesContinuationMetadata(salvaged) {
		t.Error("salvaged turn reads as a continuation anchor; the next round would try to resume a dead stream")
	}

	// The whole point of the salvaged turn: the model sees its own draft next round.
	msgs := expandHistory(hist, replayScope{})
	seen := false
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant && m.Text() == draft {
			seen = true
		}
	}
	if !seen {
		t.Error("salvaged draft is not model-visible in the rebuilt history")
	}

	steering := hist[len(hist)-2]
	if !strings.Contains(steering.Message.Text(), "Do not start over.") {
		t.Errorf("steering text = %q, want the draft-reuse wording for a substantial partial", steering.Message.Text())
	}

	// Live clients must end up rendering what the transcript stores: the screen
	// holds the LAST attempt's trickle, so settlement resets it and re-emits the
	// salvaged text as a completed assistant item before the steering and the
	// failure event.
	gotEvents := eventKindsOfInterest(captured,
		events.EventAssistantTextReset,
		events.EventAssistantTextStart,
		events.EventAssistantTextDelta,
		events.EventAssistantTextEnd,
		events.EventSteeringInjected,
		events.EventError,
	)
	tail := []events.EventKind{
		events.EventAssistantTextReset,
		events.EventAssistantTextStart,
		events.EventAssistantTextDelta,
		events.EventAssistantTextEnd,
		events.EventSteeringInjected,
		events.EventError,
	}
	if len(gotEvents) < len(tail) {
		t.Fatalf("settlement events = %v, want a tail of %v", gotEvents, tail)
	}
	for i, want := range tail {
		if got := gotEvents[len(gotEvents)-len(tail)+i]; got != want {
			t.Fatalf("settlement events = %v, want a tail of %v", gotEvents, tail)
		}
	}
	var endText, deltaText string
	for _, ev := range captured {
		switch d := ev.Data.(type) {
		case events.AssistantTextDeltaData:
			deltaText = d.Delta
		case events.AssistantTextEndData:
			endText = d.Text
		}
	}
	if deltaText != draft {
		t.Errorf("last assistant delta carried %d bytes, want the %d-byte salvaged draft", len(deltaText), len(draft))
	}
	if endText != draft {
		t.Errorf("assistant text end carried %d bytes, want the %d-byte salvaged draft", len(endText), len(draft))
	}
}

// TestSettlement_CancelledRoundPersistsNothing: a cancellation is not a provider
// failure. Even holding a large partial, the round persists neither the salvaged
// turn nor steering.
func TestSettlement_CancelledRoundPersistsNothing(t *testing.T) {
	t.Parallel()
	draft := strings.Repeat("plan step. ", 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"primary": func(st *llm.ChanStream) {
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: draft})
				cancel()
				st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: context.Canceled})
			},
		},
	}
	sess := settlementSession(t, a)
	drainSessionEvents(sess)

	if _, err := sess.ProcessInput(ctx, "write the plan", nil); err == nil {
		t.Fatal("expected a cancellation error from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	hist := sessionHistory(sess)
	sess.Close()

	// The interrupt marker is the round loop's own pre-existing cancellation
	// notice, not settlement's; settlement itself must add nothing.
	want := []schema.TurnKind{schema.TurnSteering}
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, tpath)), want)
	for _, turn := range hist {
		if turn.Kind == schema.TurnAssistant {
			t.Fatal("cancelled round persisted a salvaged assistant turn")
		}
		if turn.SteeringKind != events.SteeringKindInterrupted && turn.Kind == schema.TurnSteering {
			t.Fatalf("cancelled round persisted steering %q", turn.Message.Text())
		}
	}
}

// TestSettlement_ContextLengthTerminalKeepsPrimarySalvage pins the spec's
// mixed-round precedence: the exclusions key on the salvage-producing group's
// failure, never the round's last error. A chain walk that ends on a fallback's
// context-length rejection must still persist the primary group's partial.
func TestSettlement_ContextLengthTerminalKeepsPrimarySalvage(t *testing.T) {
	t.Parallel()
	draft := strings.Repeat("plan step. ", 1000)
	contextLenErr := llm.ErrorFromHTTPStatus("openai", 400, "context length exceeded", nil, nil)
	if llm.Kind(contextLenErr) != llm.KindContextLength {
		t.Fatalf("fixture kind = %v, want KindContextLength", llm.Kind(contextLenErr))
	}
	a := &scriptedStreamAdapter{
		provider: "openai",
		openErr:  map[string]error{"fallback-b": contextLenErr},
		script: map[string]func(*llm.ChanStream){
			// Permanent mid-stream death: one attempt, then the chain walks.
			"primary": streamTextThenFail(draft, llm.ErrorFromHTTPStatus("openai", 403, "upstream denied", nil, nil)),
		},
	}
	sess := settlementSession(t, a, "fallback-b")
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "write the plan", nil); err == nil {
		t.Fatal("expected a provider error from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	hist := sessionHistory(sess)
	sess.Close()

	want := []schema.TurnKind{schema.TurnAssistant, schema.TurnSteering, schema.TurnFailure}
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, tpath)), want)
	if got := hist[len(hist)-3].Message.Text(); got != draft {
		t.Errorf("salvaged turn carried %d bytes, want the primary group's %d-byte draft", len(got), len(draft))
	}
}

// TestSettlement_CapRoundPersistsDraftAndCapAdvice covers the incident's cap
// shape. The cap rule keys on a 60s content window, which no end-to-end test can
// reach without burning a minute of wall clock, so this drives settlement
// directly off a recorder holding the cap-shaped round.
func TestSettlement_CapRoundPersistsDraftAndCapAdvice(t *testing.T) {
	t.Parallel()
	draft := strings.Repeat("plan step. ", 1000)
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		testOnly:         testConfig{metaFS: afero.NewMemMapFs()},
	}))
	drainSessionEvents(sess)
	rec := sess.roundSalvageRecorder()
	rec.Groups = append(rec.Groups, withSalvage(capGroup(2), draft))

	sess.settleFailedRound(&llm.ProviderUnhealthyError{
		Shape: "cap", Attempts: 2, Elapsed: 600 * time.Second, LastErr: midStreamErr(),
	})
	hist := sessionHistory(sess)

	assertKinds(t, "history", settledKinds(hist), []schema.TurnKind{schema.TurnAssistant, schema.TurnSteering})
	if got := hist[len(hist)-2].Message.Text(); got != draft {
		t.Errorf("salvaged turn carried %d bytes, want the %d-byte draft", len(got), len(draft))
	}
	steering := hist[len(hist)-1].Message.Text()
	if !strings.Contains(steering, wantCapAdvice) {
		t.Errorf("cap-round steering = %q, want the cap advice", steering)
	}
	if !strings.Contains(steering, "Do not start over.") {
		t.Errorf("cap-round steering = %q, want the draft-reuse wording", steering)
	}
}

// TestSettlement_SteeringNamesTheConfiguredResultTool: the draft wording tells
// the model which tool to re-send user-facing content through. A session that
// renamed its result tool must not be pointed at a tool it does not have.
func TestSettlement_SteeringNamesTheConfiguredResultTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newSession(t, withDir(dir), withConfig(SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
		ResultToolName:   "report_result",
		testOnly:         testConfig{metaFS: afero.NewMemMapFs()},
	}))
	drainSessionEvents(sess)
	rec := sess.roundSalvageRecorder()
	rec.Groups = append(rec.Groups, withSalvage(capGroup(2), strings.Repeat("plan step. ", 1000)))

	sess.settleFailedRound(&llm.ProviderUnhealthyError{
		Shape: "cap", Attempts: 2, Elapsed: 600 * time.Second, LastErr: midStreamErr(),
	})
	hist := sessionHistory(sess)

	assertKinds(t, "history", settledKinds(hist), []schema.TurnKind{schema.TurnAssistant, schema.TurnSteering})
	steering := hist[len(hist)-1].Message.Text()
	if !strings.Contains(steering, "through report_result and") {
		t.Errorf("steering = %q, want it to name the session's configured result tool", steering)
	}
	if strings.Contains(steering, "communicate") {
		t.Errorf("steering = %q names a result tool this session does not expose", steering)
	}
}
