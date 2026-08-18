package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/doctor"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// This file replays the incident the provider-failure-feedback spec was written
// from, end to end against a scripted provider: a user input whose round grinds
// through failing attempts, the early stop, what settlement leaves behind, what
// an operator reading the transcript afterwards sees, and what the model is
// shown when the user types "continue". The unit-level tests around it each pin
// one component; these pin that the components add up to the spec's walkthrough.

// steppedContentClock returns a clock that advances by step on every read, so a
// scripted stream presents a content-event window measured in minutes while the
// test runs in milliseconds. An attempt with n content events reports a window
// of (n-1)*step, which is what the cap early-stop rule (≥60s) keys on. Guarded
// so the sequence stays monotonic no matter which goroutine reads it.
func steppedContentClock(step time.Duration) func() time.Time {
	var mu sync.Mutex
	at := time.Unix(0, 0).UTC()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(step)
		return at
	}
}

// recoveringProvider scripts a provider that keeps failing a given way until
// the test declares it healed, after which it answers through the result tool.
// Recovery is keyed on the turn boundary rather than an attempt count on
// purpose: an early-stop rule that failed to trip then shows up as a group
// grinding through its whole retry budget, instead of being masked by an
// attempt that happens to succeed.
type recoveringProvider struct {
	// fail scripts the failing attempt; attempt is 1-based within the session.
	fail   func(attempt int) func(*llm.ChanStream)
	answer string

	mu       sync.Mutex
	attempts int
	healed   bool
}

func (p *recoveringProvider) stream(st *llm.ChanStream) {
	p.mu.Lock()
	healed := p.healed
	p.attempts++
	attempt := p.attempts
	p.mu.Unlock()
	if healed {
		streamCommunicate(p.answer)(st)
		return
	}
	p.fail(attempt)(st)
}

func (p *recoveringProvider) heal() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healed = true
}

// streamCommunicate scripts a healthy round: the model answers through the
// result tool, which is what ends a turn cleanly.
func streamCommunicate(message string) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		call := communicateCall("c1", message)
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: "function"}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}
}

// streamLongThenFail scripts a cap-shaped attempt: several content events, then
// a mid-stream death. Each delta advances the injected content clock, so the
// attempt reports the long content window the cap rule looks for.
func streamLongThenFail(chunk string, deltas int, err error) func(*llm.ChanStream) {
	return func(st *llm.ChanStream) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
		for range deltas {
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: chunk})
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: err})
	}
}

func midStreamDeath() error {
	return llm.NewStreamError("openai", "stream ended without completion", nil)
}

// transcriptOutline renders the session's transcript the way
// `serf-doctor transcript --format outline` does — through the doctor package
// itself, so what these tests assert is what an operator actually reads.
func transcriptOutline(t *testing.T, s *Session) string {
	t.Helper()
	res, err := doctor.Transcript(s.stateDir, s.ID(), doctor.TranscriptOpts{Format: "outline"})
	if err != nil {
		t.Fatalf("doctor.Transcript: %v", err)
	}
	return doctor.RenderTranscript(res, "outline")
}

// modelVisibleText is every message the request carried, joined — what the
// model was actually shown for this turn.
func modelVisibleText(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text())
		b.WriteString("\n")
	}
	return b.String()
}

// lastRequest is the request the most recent stream attempt sent.
func lastRequest(t *testing.T, a *scriptedStreamAdapter) llm.Request {
	t.Helper()
	reqs := a.Requests()
	if len(reqs) == 0 {
		t.Fatal("adapter recorded no requests")
	}
	return reqs[len(reqs)-1]
}

func unhealthyVerdict(t *testing.T, err error) *llm.ProviderUnhealthyError {
	t.Helper()
	var unhealthy *llm.ProviderUnhealthyError
	if !errors.As(err, &unhealthy) {
		t.Fatalf("ProcessInput error = %v (%T), want *llm.ProviderUnhealthyError", err, err)
	}
	return unhealthy
}

// assertOrdered checks that each fragment appears in text after the previous
// one, so an outline assertion pins order and not just presence.
func assertOrdered(t *testing.T, label, text string, fragments ...string) {
	t.Helper()
	at := 0
	for _, fragment := range fragments {
		i := strings.Index(text[at:], fragment)
		if i < 0 {
			t.Fatalf("%s = \n%s\nwant %q after position %d", label, text, fragment, at)
		}
		at += i + len(fragment)
	}
}

// TestProviderFailureE2E_StallStreakSteersTheResumedTurn is the incident's first
// failing turn: every attempt opens a stream, reasons briefly, and dies with
// nothing salvageable. The streak rule stops the group at four attempts rather
// than burning the eleven-attempt budget, the round settles steering only, and
// the turn the user's "continue" runs carries that steering into the model's
// history.
func TestProviderFailureE2E_StallStreakSteersTheResumedTurn(t *testing.T) {
	t.Parallel()
	p := &recoveringProvider{
		answer: "here is the plan, in pieces",
		fail: func(int) func(*llm.ChanStream) {
			return streamReasoningThenFail(midStreamDeath())
		},
	}
	a := &scriptedStreamAdapter{
		provider: "openai",
		script:   map[string]func(*llm.ChanStream){"primary": p.stream},
	}
	sess := settlementSession(t, a)
	drainSessionEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	_, err := sess.ProcessInput(ctx, "write the plan", nil)

	verdict := unhealthyVerdict(t, err)
	if verdict.Shape != "stall" || verdict.Attempts != modelRetryFailFastAfter {
		t.Errorf("verdict = shape %q after %d attempts, want shape \"stall\" after %d",
			verdict.Shape, verdict.Attempts, modelRetryFailFastAfter)
	}
	// The budget allows eleven attempts; the streak rule is what stopped this.
	if got := a.Models(); len(got) != modelRetryFailFastAfter {
		t.Errorf("streamed %d attempts (%v), want %d: the streak rule must stop the group early",
			len(got), got, modelRetryFailFastAfter)
	}

	want := []schema.TurnKind{schema.TurnSteering, schema.TurnFailure}
	hist := sessionHistory(sess)
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, sess.TranscriptPath())), want)

	steeringText := hist[len(hist)-2].Message.Text()
	if !strings.Contains(steeringText, stallSteering) {
		t.Errorf("steering = %q, want the stall template", steeringText)
	}

	// What an operator reading the incident sees: a steering turn explaining the
	// failure and the failure marker, with no assistant turn — this group
	// produced nothing to salvage, so claiming otherwise would be a lie.
	outline := transcriptOutline(t, sess)
	assertOrdered(t, "outline", outline, "USER_INPUT", "STEERING", stallSteering, "TURN_FAILURE")
	if strings.Contains(outline, "] ASSISTANT") {
		t.Errorf("outline = \n%s\nwant no ASSISTANT turn: the stall group salvaged nothing", outline)
	}

	// The user types "continue". The steering must reach the model, or the round
	// that failed is invisible to it and it starts over blind.
	p.heal()
	out, err := sess.ProcessInput(ctx, "continue", nil)
	if err != nil {
		t.Fatalf("resumed turn: %v, want it to succeed", err)
	}
	if out != "here is the plan, in pieces" {
		t.Errorf("resumed turn output = %q, want the provider's answer", out)
	}
	if got := len(a.Models()); got != modelRetryFailFastAfter+1 {
		t.Errorf("streamed %d attempts total, want %d: the resumed turn must take one healthy attempt",
			got, modelRetryFailFastAfter+1)
	}
	if sent := modelVisibleText(lastRequest(t, a)); !strings.Contains(sent, steeringText) {
		t.Errorf("resumed turn sent %d bytes of history without the failure steering; the model cannot see why its last turn produced nothing", len(sent))
	}
}

// TestProviderFailureE2E_CapShapePersistsDraftForTheResumedTurn is the
// incident's second failing turn: two attempts stream minutes of plan text and
// are cut mid-flight. Cap detection stops the group after the second instead of
// grinding the budget, settlement persists the larger partial as the model's
// own draft plus cap-shape steering, and the resumed turn shows the model both.
//
// The content clock is injected (settlementSessionWithContentClock): the cap
// rule keys on a ≥60s content-event window, which is the one thing about this
// shape a test cannot reproduce in real time. Everything else — the retry loop,
// the early stop, salvage selection, persistence, the resumed history — is the
// production path.
func TestProviderFailureE2E_CapShapePersistsDraftForTheResumedTurn(t *testing.T) {
	t.Parallel()
	const contentStep = 45 * time.Second
	const deltasPerAttempt = 3 // window = 2 * contentStep = 90s, over the cap bound
	draft := strings.Repeat("plan step. ", 400)
	trickle := strings.Repeat("plan step. ", 40)
	p := &recoveringProvider{
		answer: "splitting the plan into smaller writes",
		fail: func(attempt int) func(*llm.ChanStream) {
			// Every failing attempt is cap-shaped; only the first carries the big
			// draft, so keeping the LATEST partial rather than the LARGEST would
			// be visible in what settlement persists.
			chunk := trickle
			if attempt == 1 {
				chunk = draft
			}
			return streamLongThenFail(chunk, deltasPerAttempt, midStreamDeath())
		},
	}
	a := &scriptedStreamAdapter{
		provider: "openai",
		script:   map[string]func(*llm.ChanStream){"primary": p.stream},
	}
	sess := settlementSessionWithContentClock(t, a, steppedContentClock(contentStep))
	drainSessionEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	_, err := sess.ProcessInput(ctx, "write the plan", nil)

	verdict := unhealthyVerdict(t, err)
	if verdict.Shape != "cap" || verdict.Attempts != 2 {
		t.Errorf("verdict = shape %q after %d attempts, want shape \"cap\" after 2",
			verdict.Shape, verdict.Attempts)
	}
	if got := a.Models(); len(got) != 2 {
		t.Errorf("streamed %d attempts (%v), want 2: cap detection must stop the group after the second long cutoff",
			len(got), got)
	}

	want := []schema.TurnKind{schema.TurnAssistant, schema.TurnSteering, schema.TurnFailure}
	hist := sessionHistory(sess)
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, sess.TranscriptPath())), want)

	wantDraft := strings.Repeat(draft, deltasPerAttempt)
	salvaged := hist[len(hist)-3].Message.Text()
	if salvaged != wantDraft {
		t.Errorf("salvaged turn carried %d bytes, want the first attempt's %d-byte draft", len(salvaged), len(wantDraft))
	}
	steeringText := hist[len(hist)-2].Message.Text()
	if !strings.Contains(steeringText, "~90s of streaming, twice") {
		t.Errorf("steering = %q, want the cap template naming both long attempts", steeringText)
	}
	if !strings.Contains(steeringText, capAdviceSteering) {
		t.Errorf("steering = %q, want the cap advice", steeringText)
	}
	if !strings.Contains(steeringText, "Do not start over.") {
		t.Errorf("steering = %q, want the draft-reuse wording for a substantial partial", steeringText)
	}

	// What an operator reading the incident sees: the salvaged draft standing as
	// an assistant turn, its steering, and the failure marker after it — an
	// ASSISTANT turn in a transcript no longer means the user was answered.
	assertOrdered(t, "outline", transcriptOutline(t, sess),
		"USER_INPUT", "ASSISTANT", "plan step.", "STEERING", "TURN_FAILURE")

	// The user types "continue". The model must see its own draft AND the
	// instruction to reuse it in smaller pieces; that pairing is the whole point
	// of settlement.
	p.heal()
	out, err := sess.ProcessInput(ctx, "continue", nil)
	if err != nil {
		t.Fatalf("resumed turn: %v, want it to succeed", err)
	}
	if out != "splitting the plan into smaller writes" {
		t.Errorf("resumed turn output = %q, want the provider's answer", out)
	}
	if got := len(a.Models()); got != 3 {
		t.Errorf("streamed %d attempts total, want 3: the resumed turn must take one healthy attempt", got)
	}
	sent := lastRequest(t, a)
	seenDraft := false
	for _, m := range sent.Messages {
		if m.Role == llm.RoleAssistant && m.Text() == wantDraft {
			seenDraft = true
		}
	}
	if !seenDraft {
		t.Error("resumed turn did not show the model its own salvaged draft")
	}
	if !strings.Contains(modelVisibleText(sent), steeringText) {
		t.Error("resumed turn did not show the model the steering that explains the draft")
	}
}

// TestProviderFailureE2E_TrickleStallSalvagesFragmentWithoutDraftClaim is the
// incident's third failing shape: unlike the first two tests, every attempt
// streams a short trickle of content deltas — never enough to cross the
// draft-reuse threshold — before dying mid-stream. Per the spec's resolution at
// b411e7f69, "nothing salvageable" means literally zero bytes: a small nonzero
// salvage is still salvage and must be persisted, but worded as a fragment
// rather than promised to the model as a reusable draft.
//
// Reuses streamLongThenFail (deltas, then a mid-stream death) with a small
// chunk count and no injected content clock, so the content window stays near
// zero and the shape reads as a stall rather than a cap.
func TestProviderFailureE2E_TrickleStallSalvagesFragmentWithoutDraftClaim(t *testing.T) {
	t.Parallel()
	const chunk = "plan step. "
	const deltasPerAttempt = 3
	trickle := strings.Repeat(chunk, deltasPerAttempt)
	if len(trickle) == 0 || len(trickle) >= substantialSalvageBytes {
		t.Fatalf("fixture trickle is %d bytes, want nonzero and well under the %d-byte draft threshold",
			len(trickle), substantialSalvageBytes)
	}
	p := &recoveringProvider{
		answer: "here is the plan, in pieces",
		fail: func(int) func(*llm.ChanStream) {
			return streamLongThenFail(chunk, deltasPerAttempt, midStreamDeath())
		},
	}
	a := &scriptedStreamAdapter{
		provider: "openai",
		script:   map[string]func(*llm.ChanStream){"primary": p.stream},
	}
	sess := settlementSession(t, a)
	drainSessionEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	_, err := sess.ProcessInput(ctx, "write the plan", nil)

	verdict := unhealthyVerdict(t, err)
	if verdict.Shape != "stall" || verdict.Attempts != modelRetryFailFastAfter {
		t.Errorf("verdict = shape %q after %d attempts, want shape \"stall\" after %d",
			verdict.Shape, verdict.Attempts, modelRetryFailFastAfter)
	}

	// A salvaged assistant turn IS persisted: nonzero salvage is salvage, never
	// "too small to bother".
	want := []schema.TurnKind{schema.TurnAssistant, schema.TurnSteering, schema.TurnFailure}
	hist := sessionHistory(sess)
	assertKinds(t, "history", settledKinds(hist), want)
	assertKinds(t, "transcript", settledKinds(transcriptTurns(t, sess.TranscriptPath())), want)

	salvaged := hist[len(hist)-3].Message.Text()
	if salvaged != trickle {
		t.Errorf("salvaged turn = %q, want the %d-byte trickle verbatim", salvaged, len(trickle))
	}

	// The steering uses the real production fragment wording — not the
	// draft-reuse wording — and names the actual salvaged byte count.
	steeringText := hist[len(hist)-2].Message.Text()
	wantFragment := fmt.Sprintf(fragmentSteering, pluralizedUnit(len(trickle), "byte"))
	if !strings.Contains(steeringText, wantFragment) {
		t.Errorf("steering = %q, want the fragment wording %q", steeringText, wantFragment)
	}
	// No draft-reuse claim: a fragment this small is not a reusable draft, and
	// the zero-salvage and fragment shapes must not promise the model one.
	if strings.Contains(steeringText, "Treat it as your draft") || strings.Contains(steeringText, "Do not start over.") {
		t.Errorf("steering = %q makes a draft-reuse claim for a %d-byte fragment", steeringText, len(trickle))
	}

	// What an operator reading the incident sees: the salvaged fragment standing
	// as an assistant turn, the fragment steering, then the failure marker. The
	// outline trims trailing whitespace when it renders a turn, so this checks
	// for the trimmed fragment rather than the trickle's trailing space.
	assertOrdered(t, "outline", transcriptOutline(t, sess),
		"USER_INPUT", "ASSISTANT", strings.TrimSpace(trickle), "STEERING", wantFragment, "TURN_FAILURE")

	// The user types "continue". The model must see its own fragment AND the
	// steering that explains it, exactly as the other settled shapes do.
	p.heal()
	out, err := sess.ProcessInput(ctx, "continue", nil)
	if err != nil {
		t.Fatalf("resumed turn: %v, want it to succeed", err)
	}
	if out != "here is the plan, in pieces" {
		t.Errorf("resumed turn output = %q, want the provider's answer", out)
	}
	if got := len(a.Models()); got != modelRetryFailFastAfter+1 {
		t.Errorf("streamed %d attempts total, want %d: the resumed turn must take one healthy attempt",
			got, modelRetryFailFastAfter+1)
	}
	sent := lastRequest(t, a)
	seenFragment := false
	for _, m := range sent.Messages {
		if m.Role == llm.RoleAssistant && m.Text() == trickle {
			seenFragment = true
		}
	}
	if !seenFragment {
		t.Error("resumed turn did not show the model its own salvaged fragment")
	}
	if !strings.Contains(modelVisibleText(sent), steeringText) {
		t.Error("resumed turn did not show the model the steering that explains the fragment")
	}
}
