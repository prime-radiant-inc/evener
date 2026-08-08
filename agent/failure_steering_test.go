package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// This file pins the settlement gate and the failure-steering composer, per
// docs/superpowers/specs/2026-08-07-provider-failure-feedback-design.md
// "Component 3: partial-preserving settlement". The template sentences below
// are copied from that section verbatim; they are model-visible product text,
// so the tests assert them character-for-character rather than by substring
// where the whole sentence is spec-worded.

const (
	wantDraftSentences = "Before the connection failed, you produced the response above. " +
		"Any tool calls in progress did not execute, and nothing was delivered or saved. " +
		"Treat it as your draft — re-send user-facing content through communicate and " +
		"re-issue file writes in smaller pieces, reusing the draft rather than " +
		"regenerating it. Do not start over."
	wantCapAdvice = "The transport cannot sustain responses that long. Keep each response " +
		"well under that size and continue your work across multiple rounds."
	wantSilentStall = "The provider accepted requests but streamed nothing."
)

// midStreamErr is the shape a stream that opened and then died reports.
func midStreamErr() error {
	return llm.NewStreamError("lunarouter", "openai-compatible stream ended without completion", nil)
}

// inBandErr is the shape the openai-compat adapter decodes an in-band
// {"error": ...} chunk into.
func inBandErr() error {
	return llm.ErrorFromHTTPStatus("lunarouter", 429,
		"chat.completions(stream): upstream quota exhausted", nil, nil)
}

func authErr() error {
	return llm.ErrorFromHTTPStatus("lunarouter", 401, "invalid api key", nil, nil)
}

func consumeAttempt(err error, d, window time.Duration, bytes int) attemptRecord {
	return attemptRecord{Phase: llm.PhaseConsume, Err: err, Duration: d, ContentWindow: window, SalvagedBytes: bytes}
}

func silentStallAttempt(d time.Duration) attemptRecord {
	return attemptRecord{Phase: llm.PhaseSilentStall, Err: llm.ErrSSEReadTimeout, Duration: d}
}

func openAttempt(err error) attemptRecord {
	return attemptRecord{Phase: llm.PhaseOpen, Err: err, Duration: time.Second}
}

func fastRejectAttempt(err error) attemptRecord {
	return attemptRecord{Phase: llm.PhaseFastReject, Err: err, Duration: 200 * time.Millisecond}
}

// stallGroup is the incident's stall shape: n mid-stream deaths at ~32s each.
func stallGroup(n int) groupRecord {
	g := groupRecord{Model: "kimi-k3", Provider: "lunarouter"}
	for range n {
		g.Attempts = append(g.Attempts, consumeAttempt(midStreamErr(), 32*time.Second, 20*time.Second, 40))
	}
	return g
}

// capGroup is the incident's cap shape: n attempts each streaming ~295s
// before the transport cut them.
func capGroup(n int) groupRecord {
	g := groupRecord{Model: "kimi-k3", Provider: "lunarouter"}
	for range n {
		g.Attempts = append(g.Attempts, consumeAttempt(midStreamErr(), 300*time.Second, 295*time.Second, 40000))
	}
	return g
}

func withSalvage(g groupRecord, text string) groupRecord {
	g.BestPartial = responseWith(textPart(text))
	g.BestBytes = len(text)
	return g
}

func TestClassifySettlement(t *testing.T) {
	unhealthy := &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 130 * time.Second, LastErr: midStreamErr()}

	tests := []struct {
		name     string
		rec      *roundRecorder
		terminal error
		want     settlementKind
	}{
		{
			name:     "consume-phase failure with salvage settles both turns",
			rec:      &roundRecorder{Groups: []groupRecord{withSalvage(capGroup(2), strings.Repeat("plan ", 2000))}},
			terminal: unhealthy,
			want:     settleSalvageAndSteering,
		},
		{
			name:     "consume-phase failure with nothing salvageable is steering only",
			rec:      &roundRecorder{Groups: []groupRecord{stallGroup(4)}},
			terminal: unhealthy,
			want:     settleSteeringOnly,
		},
		{
			name:     "silent stalls settle",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{silentStallAttempt(30 * time.Second)}}}},
			terminal: midStreamErr(),
			want:     settleSteeringOnly,
		},
		{
			name:     "permanent mid-stream error after one attempt settles",
			rec:      &roundRecorder{Groups: []groupRecord{stallGroup(1)}},
			terminal: midStreamErr(),
			want:     settleSteeringOnly,
		},
		{
			name:     "cancellation never settles even with salvage",
			rec:      &roundRecorder{Groups: []groupRecord{withSalvage(capGroup(2), strings.Repeat("plan ", 2000))}},
			terminal: context.Canceled,
			want:     settleNone,
		},
		{
			name:     "abort error never settles",
			rec:      &roundRecorder{Groups: []groupRecord{withSalvage(capGroup(2), strings.Repeat("plan ", 2000))}},
			terminal: llm.NewAbortError("cancelled", context.Canceled),
			want:     settleNone,
		},
		{
			name:     "context-length round with no consume-phase failure never settles",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{openAttempt(llm.ErrorFromHTTPStatus("lunarouter", 413, "context length exceeded", nil, nil))}}}},
			terminal: llm.ErrorFromHTTPStatus("lunarouter", 413, "context length exceeded", nil, nil),
			want:     settleNone,
		},
		{
			name: "mixed round: primary consume-phase salvage survives a fallback context-length terminal",
			rec: &roundRecorder{Groups: []groupRecord{
				withSalvage(capGroup(2), strings.Repeat("plan ", 2000)),
				{Model: "kimi-k3-mini", Attempts: []attemptRecord{openAttempt(llm.ErrorFromHTTPStatus("lunarouter", 413, "context length exceeded", nil, nil))}},
			}},
			terminal: llm.ErrorFromHTTPStatus("lunarouter", 413, "context length exceeded", nil, nil),
			want:     settleSalvageAndSteering,
		},
		{
			name:     "open-phase rejection round never settles",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{openAttempt(authErr()), openAttempt(authErr())}}}},
			terminal: authErr(),
			want:     settleNone,
		},
		{
			name:     "fast zero-content rejections never settle",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{fastRejectAttempt(inBandErr()), fastRejectAttempt(inBandErr())}}}},
			terminal: inBandErr(),
			want:     settleNone,
		},
		{
			name:     "content filter with a consume-phase failure is steering only",
			rec:      &roundRecorder{Groups: []groupRecord{stallGroup(2)}},
			terminal: filterBlockedErr(t),
			want:     settleSteeringOnly,
		},
		{
			name:     "content filter never persists salvage",
			rec:      &roundRecorder{Groups: []groupRecord{withSalvage(capGroup(2), strings.Repeat("plan ", 2000))}},
			terminal: filterBlockedErr(t),
			want:     settleSteeringOnly,
		},
		{
			name:     "content filter without a consume-phase failure never settles",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{openAttempt(filterBlockedErr(t))}}}},
			terminal: filterBlockedErr(t),
			want:     settleNone,
		},
		{
			name:     "reasoning-only partial is not salvage",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{consumeAttempt(midStreamErr(), 30*time.Second, 20*time.Second, 0)}, BestPartial: responseWith(thinkingPart("thinking hard")), BestBytes: 0}}},
			terminal: midStreamErr(),
			want:     settleSteeringOnly,
		},
		{
			name:     "nil recorder with an unrelated error never settles",
			rec:      nil,
			terminal: authErr(),
			want:     settleNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySettlement(tc.rec, tc.terminal); got != tc.want {
				t.Fatalf("classifySettlement = %v, want %v", got, tc.want)
			}
		})
	}
}

// filterBlockedErr builds a genuine KindContentFilter error, failing the test
// if the construction does not actually classify as one — the gate keys on the
// kind, so a miscategorised fixture would silently test nothing.
func filterBlockedErr(t *testing.T) error {
	t.Helper()
	err := llm.ErrorFromHTTPStatus("lunarouter", 400,
		"the response was blocked by the content filter", nil, nil)
	if llm.Kind(err) != llm.KindContentFilter {
		t.Fatalf("fixture kind = %v, want KindContentFilter", llm.Kind(err))
	}
	return err
}

func TestComposeFailureSteering(t *testing.T) {
	fallbackAuthGroup := groupRecord{Model: "kimi-k3-mini", Provider: "lunarouter", Attempts: []attemptRecord{openAttempt(authErr())}}

	tests := []struct {
		name          string
		rec           *roundRecorder
		terminal      error
		salvagedBytes int
		want          string
	}{
		{
			name:     "stall shape, four attempts",
			rec:      &roundRecorder{Groups: []groupRecord{stallGroup(4)}},
			terminal: &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 130 * time.Second, LastErr: midStreamErr()},
			want:     "The provider stopped responding mid-stream, 4 times over 2 minutes.",
		},
		{
			name:     "stall shape, one attempt reads truthfully in the singular",
			rec:      &roundRecorder{Groups: []groupRecord{stallGroup(1)}},
			terminal: midStreamErr(),
			want:     "The provider stopped responding mid-stream.",
		},
		{
			name:     "silent stall shape",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{silentStallAttempt(30 * time.Second), silentStallAttempt(30 * time.Second)}}}},
			terminal: &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 2, Elapsed: 65 * time.Second, LastErr: llm.ErrSSEReadTimeout},
			want:     wantSilentStall,
		},
		{
			name:          "cap shape, twice, with a substantial draft",
			rec:           &roundRecorder{Groups: []groupRecord{capGroup(2)}},
			terminal:      &llm.ProviderUnhealthyError{Shape: "cap", Attempts: 2, Elapsed: 600 * time.Second, LastErr: midStreamErr()},
			salvagedBytes: 40000,
			want: "The transport cut off your response after ~295s of streaming, twice. " +
				wantDraftSentences + " " + wantCapAdvice,
		},
		{
			name:     "cap shape, single attempt, no count clause",
			rec:      &roundRecorder{Groups: []groupRecord{capGroup(1)}},
			terminal: midStreamErr(),
			want:     "The transport cut off your response after ~295s of streaming. " + wantCapAdvice,
		},
		{
			name:     "decoded in-band error reports what the provider said",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{consumeAttempt(inBandErr(), 40*time.Second, 30*time.Second, 120)}}}},
			terminal: inBandErr(),
			want:     "The provider reported: lunarouter error (status=429): chat.completions(stream): upstream quota exhausted.",
		},
		{
			name: "mixed round describes the stall group and notes the fallback",
			rec: &roundRecorder{Groups: []groupRecord{
				stallGroup(4),
				fallbackAuthGroup,
			}},
			terminal: authErr(),
			want: "The provider stopped responding mid-stream, 4 times over 2 minutes. " +
				"The configured fallback model also failed (authentication error).",
		},
		{
			name:          "small fragment gets fragment wording and no draft instruction",
			rec:           &roundRecorder{Groups: []groupRecord{stallGroup(4)}},
			terminal:      &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 130 * time.Second, LastErr: midStreamErr()},
			salvagedBytes: 128,
			want: "The provider stopped responding mid-stream, 4 times over 2 minutes. " +
				"A small fragment (128 bytes) was produced and not delivered.",
		},
		{
			name:     "content filter gets filter wording with no draft reference",
			rec:      &roundRecorder{Groups: []groupRecord{stallGroup(2)}},
			terminal: filterBlockedErr(t),
			want:     "The provider blocked this response under its content policy. Nothing was delivered or saved.",
		},
		{
			name:     "unhealthy verdict without recorded attempts still renders its shape",
			rec:      &roundRecorder{},
			terminal: &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 130 * time.Second, LastErr: midStreamErr()},
			want:     "The provider stopped responding mid-stream, 4 times over 2 minutes.",
		},
		{
			name:     "unhealthy cap verdict without recorded attempts still renders the cap template",
			rec:      &roundRecorder{},
			terminal: &llm.ProviderUnhealthyError{Shape: "cap", Attempts: 2, Elapsed: 590 * time.Second, LastErr: midStreamErr()},
			want:     "The transport cut off your response after ~295s of streaming, twice. " + wantCapAdvice,
		},
		{
			name:     "nothing to describe yields no steering",
			rec:      &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{openAttempt(authErr())}}}},
			terminal: authErr(),
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := composeFailureSteering(tc.rec, tc.terminal, tc.salvagedBytes)
			if got != tc.want {
				t.Fatalf("composeFailureSteering =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestComposeFailureSteering_OneAttemptIsNeverRepeatedly guards the honesty rule
// the spec calls out by name: a permanent mid-stream error settles after ONE
// attempt, and plural wording there would be a lie.
func TestComposeFailureSteering_OneAttemptIsNeverRepeatedly(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{stallGroup(1)}}

	got := composeFailureSteering(rec, midStreamErr(), 0)

	for _, banned := range []string{"repeatedly", "1 times", "twice"} {
		if strings.Contains(got, banned) {
			t.Fatalf("one-attempt steering %q must not contain %q", got, banned)
		}
	}
}

// TestComposeFailureSteering_OneTemplatePerTerminalClass pins the spec's "every
// terminal-error class that can reach settlement maps to exactly one template"
// requirement: no output may carry two shape sentences.
func TestComposeFailureSteering_OneTemplatePerTerminalClass(t *testing.T) {
	shapeMarkers := []string{
		"The provider stopped responding mid-stream",
		wantSilentStall,
		"The transport cut off your response after",
		"The provider reported:",
		"The provider blocked this response under its content policy.",
	}

	tests := []struct {
		name     string
		rec      *roundRecorder
		terminal error
	}{
		{"stall", &roundRecorder{Groups: []groupRecord{stallGroup(4)}}, &llm.ProviderUnhealthyError{Shape: "stall", Attempts: 4, Elapsed: 130 * time.Second, LastErr: midStreamErr()}},
		{"silent stall", &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{silentStallAttempt(30 * time.Second)}}}}, midStreamErr()},
		{"cap", &roundRecorder{Groups: []groupRecord{capGroup(2)}}, &llm.ProviderUnhealthyError{Shape: "cap", Attempts: 2, Elapsed: 600 * time.Second, LastErr: midStreamErr()}},
		{"decoded in-band", &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{consumeAttempt(inBandErr(), 40*time.Second, 30*time.Second, 120)}}}}, inBandErr()},
		{"content filter", &roundRecorder{Groups: []groupRecord{stallGroup(2)}}, filterBlockedErr(t)},
		{"mixed round ending on an open-phase fallback", &roundRecorder{Groups: []groupRecord{stallGroup(4), {Attempts: []attemptRecord{openAttempt(authErr())}}}}, authErr()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := composeFailureSteering(tc.rec, tc.terminal, 0)
			matched := 0
			for _, m := range shapeMarkers {
				if strings.Contains(got, m) {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("steering %q matched %d shape templates, want exactly 1", got, matched)
			}
		})
	}
}

// TestComposeFailureSteering_CapAdviceOnlyForCapShape pins the spec's "Cap shape
// adds" scoping: the size advice is meaningless for a stall and must not leak.
func TestComposeFailureSteering_CapAdviceOnlyForCapShape(t *testing.T) {
	tests := []struct {
		name    string
		rec     *roundRecorder
		wantCap bool
	}{
		{"stall", &roundRecorder{Groups: []groupRecord{stallGroup(4)}}, false},
		{"silent stall", &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{silentStallAttempt(30 * time.Second)}}}}, false},
		{"in-band", &roundRecorder{Groups: []groupRecord{{Attempts: []attemptRecord{consumeAttempt(inBandErr(), 40*time.Second, 30*time.Second, 120)}}}}, false},
		{"cap", &roundRecorder{Groups: []groupRecord{capGroup(2)}}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := composeFailureSteering(tc.rec, midStreamErr(), 40000)
			if strings.Contains(got, wantCapAdvice) != tc.wantCap {
				t.Fatalf("steering %q: cap advice present = %v, want %v", got, !tc.wantCap, tc.wantCap)
			}
		})
	}
}

// TestComposeFailureSteering_NoInterruptWording keeps interrupt steering out of
// this composer — the spec gives interrupts their own one-line steering that
// makes no provider-failure claim, composed elsewhere.
func TestComposeFailureSteering_NoInterruptWording(t *testing.T) {
	recs := []*roundRecorder{
		{Groups: []groupRecord{stallGroup(4)}},
		{Groups: []groupRecord{capGroup(2)}},
		{Groups: []groupRecord{{Attempts: []attemptRecord{silentStallAttempt(30 * time.Second)}}}},
	}

	for _, rec := range recs {
		got := composeFailureSteering(rec, midStreamErr(), 4096)
		if strings.Contains(got, "interrupt") {
			t.Fatalf("failure steering %q must not mention interruption", got)
		}
	}
}

// TestComposeFailureSteering_NoSalvageOmitsSalvageWording pins that zero bytes
// produces neither the draft instruction nor the fragment sentence.
func TestComposeFailureSteering_NoSalvageOmitsSalvageWording(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{stallGroup(4)}}

	got := composeFailureSteering(rec, midStreamErr(), 0)

	if strings.Contains(got, "draft") {
		t.Fatalf("steering with no salvage %q must not reference a draft", got)
	}
	if strings.Contains(got, "fragment") {
		t.Fatalf("steering with no salvage %q must not reference a fragment", got)
	}
}

// TestClassifySettlement_SalvageNeedsNoByteFloor pins that a one-byte partial
// settles as salvage: the spec forbids a salvage floor, and wording — not
// persistence — is what scales with size.
func TestClassifySettlement_SalvageNeedsNoByteFloor(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{withSalvage(stallGroup(4), "x")}}

	if got := classifySettlement(rec, midStreamErr()); got != settleSalvageAndSteering {
		t.Fatalf("classifySettlement with a 1-byte partial = %v, want settleSalvageAndSteering", got)
	}
	got := composeFailureSteering(rec, midStreamErr(), 1)
	if !strings.Contains(got, "A small fragment (1 bytes) was produced and not delivered.") {
		t.Fatalf("steering %q, want the fragment sentence", got)
	}
}

// TestComposeFailureSteering_UnwrappedUnhealthyCancellation keeps a cancelled
// round out of settlement even when the cancellation arrives wrapped.
func TestClassifySettlement_WrappedCancellation(t *testing.T) {
	rec := &roundRecorder{Groups: []groupRecord{withSalvage(capGroup(2), strings.Repeat("plan ", 2000))}}
	wrapped := errors.Join(errors.New("round aborted"), context.Canceled)

	if got := classifySettlement(rec, wrapped); got != settleNone {
		t.Fatalf("classifySettlement on a wrapped cancellation = %v, want settleNone", got)
	}
}
