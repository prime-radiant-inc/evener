package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/llm"
)

var errRecorderStreamDied = errors.New("stream died")

// salvagedGroup builds a group the way callModel fills one: a consume-phase
// failure per entry in salvaged, each carrying a partial of that many text
// bytes (a zero-byte entry is a reasoning-only partial, which is never salvage).
func salvagedGroup(model string, salvaged ...int) groupRecord {
	g := groupRecord{Model: model, Provider: "openai"}
	for _, n := range salvaged {
		var partial *llm.Response
		if n > 0 {
			partial = &llm.Response{Message: llm.Assistant(strings.Repeat("x", n))}
		}
		g.observe(attemptRecord{Phase: llm.PhaseConsume, Err: errRecorderStreamDied, SalvagedBytes: n}, partial)
	}
	return g
}

// rejectedGroup builds a group that never got past stream open.
func rejectedGroup(model string) groupRecord {
	g := groupRecord{Model: model, Provider: "openai"}
	g.observe(attemptRecord{Phase: llm.PhaseOpen, Err: errRecorderStreamDied}, nil)
	return g
}

// TestRoundRecorder_BestSalvageSpansAllGroups: selection is round-wide, so a
// fallback group that failed with a trickle never shadows the primary group's
// far larger partial.
func TestRoundRecorder_BestSalvageSpansAllGroups(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{
		salvagedGroup("primary", 10_000),
		salvagedGroup("fallback-b", 12),
	}}

	partial, from := rec.BestSalvage()
	if from == nil {
		t.Fatal("BestSalvage returned no group, want the primary group")
	}
	if from.Model != "primary" {
		t.Fatalf("salvage group = %q, want primary (the fallback's trickle must not shadow it)", from.Model)
	}
	if partial == nil || len(partial.Text()) != 10_000 {
		t.Fatalf("salvage partial = %+v, want the 10000-byte primary snapshot", partial)
	}
	if got := from.BestBytes; got != 10_000 {
		t.Fatalf("BestBytes = %d, want 10000", got)
	}
}

// TestRoundRecorder_BestSalvageIsLargestNotLatest: within a group, the retry
// that trickled must not replace the attempt that streamed most of the answer.
func TestRoundRecorder_BestSalvageIsLargestNotLatest(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{salvagedGroup("primary", 100, 4)}}

	partial, from := rec.BestSalvage()
	if from == nil || partial == nil {
		t.Fatal("BestSalvage returned nothing, want the 100-byte attempt")
	}
	if len(partial.Text()) != 100 {
		t.Fatalf("salvage partial length = %d, want 100 (largest, not latest)", len(partial.Text()))
	}
}

// TestRoundRecorder_BestSalvageCountsAnyNonzeroBytes: there is no minimum —
// a single salvaged byte is salvage, and a reasoning-only partial (zero
// salvaged bytes) is not.
func TestRoundRecorder_BestSalvageCountsAnyNonzeroBytes(t *testing.T) {
	oneByte := &roundRecorder{Groups: []groupRecord{salvagedGroup("primary", 1)}}
	if partial, from := oneByte.BestSalvage(); partial == nil || from == nil {
		t.Fatalf("BestSalvage with 1 salvaged byte = (%+v, %+v), want the snapshot", partial, from)
	}
	reasoningOnly := &roundRecorder{Groups: []groupRecord{salvagedGroup("primary", 0)}}
	if partial, from := reasoningOnly.BestSalvage(); partial != nil || from != nil {
		t.Fatalf("BestSalvage with 0 salvaged bytes = (%+v, %+v), want none (reasoning is never salvaged)", partial, from)
	}
}

// TestRoundRecorder_SteeringGroupPrefersSalvageProducer: the steering describes
// the group whose output was salvaged, even when a LATER group also failed in
// the consume phase.
func TestRoundRecorder_SteeringGroupPrefersSalvageProducer(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{
		salvagedGroup("primary", 900),
		salvagedGroup("fallback-b", 0),
	}}

	g := rec.SteeringGroup()
	if g == nil {
		t.Fatal("SteeringGroup = nil, want the salvage-producing group")
	}
	if g.Model != "primary" {
		t.Fatalf("SteeringGroup = %q, want primary (the salvage producer, not the last consume-phase group)", g.Model)
	}
}

// TestRoundRecorder_SteeringGroupFallsBackToLastConsumeGroup: with no salvage
// anywhere, a chain walk that ends on an open-phase fallback rejection still
// describes the group that actually broke mid-stream.
func TestRoundRecorder_SteeringGroupFallsBackToLastConsumeGroup(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{
		salvagedGroup("primary", 0),
		rejectedGroup("fallback-b"),
	}}

	g := rec.SteeringGroup()
	if g == nil {
		t.Fatal("SteeringGroup = nil, want the consume-phase group")
	}
	if g.Model != "primary" {
		t.Fatalf("SteeringGroup = %q, want primary (last consume-phase group, not last group)", g.Model)
	}
}

// TestRoundRecorder_SteeringGroupNilWithoutConsumePhase: nothing streamed,
// nothing to steer about.
func TestRoundRecorder_SteeringGroupNilWithoutConsumePhase(t *testing.T) {
	empty := &roundRecorder{}
	if g := empty.SteeringGroup(); g != nil {
		t.Fatalf("SteeringGroup on an empty recorder = %+v, want nil", g)
	}
	rejected := &roundRecorder{Groups: []groupRecord{rejectedGroup("primary"), rejectedGroup("fallback-b")}}
	if g := rejected.SteeringGroup(); g != nil {
		t.Fatalf("SteeringGroup with open-phase groups only = %+v, want nil", g)
	}
}

// TestRoundRecorder_HasConsumePhaseFailure covers each phase's contribution:
// the two consume-phase shapes count, the two zero-content-fast shapes do not,
// and a successful attempt never counts whatever phase it reports.
func TestRoundRecorder_HasConsumePhaseFailure(t *testing.T) {
	cases := []struct {
		name  string
		phase llm.AttemptPhase
		err   error
		want  bool
	}{
		{"Consume", llm.PhaseConsume, errRecorderStreamDied, true},
		{"SilentStall", llm.PhaseSilentStall, errRecorderStreamDied, true},
		{"Open", llm.PhaseOpen, errRecorderStreamDied, false},
		{"FastReject", llm.PhaseFastReject, errRecorderStreamDied, false},
		{"SuccessfulAttempt", llm.PhaseConsume, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := groupRecord{Model: "primary", Provider: "openai"}
			g.observe(attemptRecord{Phase: tc.phase, Err: tc.err}, nil)
			rec := &roundRecorder{Groups: []groupRecord{g}}
			if got := rec.HasConsumePhaseFailure(); got != tc.want {
				t.Fatalf("HasConsumePhaseFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRoundRecorder_FallbackTrickleNeverShadowsPrimaryPartial drives the real
// chain: the primary streams a long draft before dying mid-stream, the first
// fallback trickles a few bytes before dying the same way, and the second
// fallback is rejected at open. The round's salvage must be the primary's
// draft, and the steering group must be the primary — not whichever group
// happened to fail last.
func TestRoundRecorder_FallbackTrickleNeverShadowsPrimaryPartial(t *testing.T) {
	primaryDraft := strings.Repeat("draft ", 200)
	const trickle = "he"
	permErr := llm.ErrorFromHTTPStatus("openai", 403, "cut off", nil, nil)
	a := &scriptedStreamAdapter{
		provider: "openai",
		openErr: map[string]error{
			"fallback-c": llm.ErrorFromHTTPStatus("openai", 403, "fallback-c denied", nil, nil),
		},
		script: map[string]func(*llm.ChanStream){
			"primary":    streamThenFail(primaryDraft, permErr),
			"fallback-b": streamThenFail(trickle, permErr),
		},
	}
	sess := unhealthyChainSession(t, a)

	_, _, _, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), unhealthyChainRequest(), "", 1)
	if err == nil {
		t.Fatal("callModelWithFallback: got nil error, want the exhausted chain's last error")
	}

	rec := sess.roundSalvageRecorder()
	if rec == nil {
		t.Fatal("currentRoundRecord = nil, want the round's recorder")
	}
	if len(rec.Groups) != 3 {
		t.Fatalf("recorded groups = %d (%+v), want 3 (primary, fallback-b, fallback-c)", len(rec.Groups), rec.Groups)
	}
	partial, from := rec.BestSalvage()
	if from == nil || partial == nil {
		t.Fatalf("BestSalvage = (%+v, %+v), want the primary group's draft", partial, from)
	}
	if from.Model != "primary" {
		t.Fatalf("salvage group = %q, want primary", from.Model)
	}
	if partial.Text() != primaryDraft {
		t.Fatalf("salvage text = %q, want the primary's draft", partial.Text())
	}
	if g := rec.SteeringGroup(); g == nil || g.Model != "primary" {
		t.Fatalf("SteeringGroup = %+v, want the primary group", g)
	}
	if !rec.HasConsumePhaseFailure() {
		t.Fatal("HasConsumePhaseFailure = false, want true (both streaming groups died mid-stream)")
	}
	if last := rec.Groups[2]; last.Model != "fallback-c" || last.BestPartial != nil {
		t.Fatalf("last group = %+v, want fallback-c with no salvage (rejected at open)", last)
	}
}

// TestRoundRecorder_FreshPerRound: the recorder rides on the ROUND, so a
// multi-round turn never settles on a previous round's salvage — after two
// rounds the session holds only the second round's groups.
func TestRoundRecorder_FreshPerRound(t *testing.T) {
	sess := newSession(t, withSteps(
		func(llm.Request) llm.Response { return agenttest.ToolCallResponse(shellExecCall("s1")) },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	))
	drainSessionEvents(sess)

	if _, err := sess.ProcessInput(context.Background(), "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	rec := sess.roundSalvageRecorder()
	if rec == nil {
		t.Fatal("currentRoundRecord = nil, want the last round's recorder")
	}
	if len(rec.Groups) != 1 {
		t.Fatalf("recorded groups = %d, want 1 (a per-turn recorder would have accumulated both rounds)", len(rec.Groups))
	}
}
