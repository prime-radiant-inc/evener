package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/serf/llm"
)

// settlementKind is what a failed round persists, per the spec's
// "Component 3: partial-preserving settlement".
type settlementKind int

const (
	// settleNone is the excluded class: no turns persisted.
	settleNone settlementKind = iota
	// settleSteeringOnly persists the steering turn alone — the round broke
	// mid-stream but left nothing salvageable.
	settleSteeringOnly
	// settleSalvageAndSteering persists the salvaged assistant turn followed by
	// the steering turn.
	settleSalvageAndSteering
)

// substantialSalvageBytes is where the steering switches from fragment wording
// to draft-reuse wording. It scales WORDING only: there is no salvage floor,
// and any nonzero salvage is persisted either way.
const substantialSalvageBytes = 512

// steeringResultToolName is the result tool the draft wording points the model
// at. The composer takes no session handle, so a session that renamed its
// result tool (Config.ResultToolName) still reads the canonical name here.
const steeringResultToolName = "communicate"

// capContentWindow is the content-event window above which a dead stream is
// cap-shaped rather than stall-shaped. It matches llm's cap-detection bound so
// the steering names the same shape the early stop acted on.
const capContentWindow = 60 * time.Second

// The model-visible template sentences, copied verbatim from the spec's
// component 3. Do not paraphrase them: this text is product surface.
const (
	stallSteering       = "The provider stopped responding mid-stream"
	silentStallSteering = "The provider accepted requests but streamed nothing."
	capShapeSteering    = "The transport cut off your response after ~%ds of streaming%s."
	inBandSteering      = "The provider reported: "
	draftSteering       = "Before the connection failed, you produced the response above. " +
		"Any tool calls in progress did not execute, and nothing was delivered or saved. " +
		"Treat it as your draft — re-send user-facing content through %s and re-issue " +
		"file writes in smaller pieces, reusing the draft rather than regenerating it. " +
		"Do not start over."
	fragmentSteering  = "A small fragment (%d bytes) was produced and not delivered."
	capAdviceSteering = "The transport cannot sustain responses that long. Keep each response " +
		"well under that size and continue your work across multiple rounds."
	fallbackAlsoFailedSteering = "The configured fallback model also failed"
	// The spec words the filter case only as "steering-only, with
	// filter-appropriate wording": it states the outcome without pushing a
	// retry and never mentions a draft, since filter rounds persist no salvage.
	contentFilterSteering = "The provider blocked this response under its content policy. " +
		"Nothing was delivered or saved."
)

// failureShape is which of the spec's four consume-phase templates describes a
// group's failure.
type failureShape int

const (
	shapeStall failureShape = iota
	shapeSilentStall
	shapeCap
	shapeInBand
)

// classifySettlement decides what a failed round persists. Settlement fires
// when the terminal error is a ProviderUnhealthyError or the recorder holds a
// consume-phase failure; it keys on the class of the salvage-producing (or
// consume-phase) group, never on whichever group failed last, so a chain walk
// ending on an open-phase or context-length fallback rejection still settles
// the primary group's partial.
//
// Excluded: cancellations, and rounds whose only failures are open-phase
// request rejections (auth, 4xx, quota, context length — all rejected before
// the stream opened, so "the provider connection failed" would be false).
// Content-filter rounds settle steering-only, and never with salvage: a
// compaction-atomic turn carrying the filtered content would pin it in history
// and defeat the ForceCompact recovery that exists to remove it.
func classifySettlement(rec *roundRecorder, terminalErr error) settlementKind {
	if roundWasCancelled(terminalErr) {
		return settleNone
	}
	consumePhase := rec.HasConsumePhaseFailure()
	if llm.Kind(terminalErr) == llm.KindContentFilter {
		if consumePhase {
			return settleSteeringOnly
		}
		return settleNone
	}
	var unhealthy *llm.ProviderUnhealthyError
	if !consumePhase && !errors.As(terminalErr, &unhealthy) {
		return settleNone
	}
	if partial, _ := rec.BestSalvage(); salvageText(partial) != "" {
		return settleSalvageAndSteering
	}
	return settleSteeringOnly
}

// roundWasCancelled reports whether the round ended because its context went away
// rather than because the provider failed.
func roundWasCancelled(err error) bool {
	var abort *llm.AbortError
	return errors.Is(err, context.Canceled) || errors.As(err, &abort)
}

// composeFailureSteering renders the model-visible steering turn for a settled
// round: what happened (one template, chosen by the steering group's shape and
// parameterized by attempt count so singular and plural both read truthfully),
// whether a configured fallback also failed, what became of any salvage, and —
// for the cap shape only — how to avoid the cap next round.
//
// Interrupt wording is deliberately absent: an interrupted round makes no
// provider-failure claim and is steered elsewhere.
func composeFailureSteering(rec *roundRecorder, terminalErr error, salvagedBytes int) string {
	if llm.Kind(terminalErr) == llm.KindContentFilter {
		return contentFilterSteering
	}
	what, shape, ok := describeGroupFailure(rec.SteeringGroup(), terminalErr)
	if !ok {
		return ""
	}
	parts := []string{what}
	if clause := fallbackAlsoFailedClause(rec, terminalErr); clause != "" {
		parts = append(parts, clause)
	}
	switch {
	case salvagedBytes >= substantialSalvageBytes:
		parts = append(parts, fmt.Sprintf(draftSteering, steeringResultToolName))
	case salvagedBytes > 0:
		parts = append(parts, fmt.Sprintf(fragmentSteering, salvagedBytes))
	}
	if shape == shapeCap {
		parts = append(parts, capAdviceSteering)
	}
	return strings.Join(parts, " ")
}

// describeGroupFailure renders the "what happened" sentence for the group the
// steering describes. ok is false when the round holds nothing a consume-phase
// template can honestly describe.
func describeGroupFailure(g *groupRecord, terminalErr error) (what string, shape failureShape, ok bool) {
	counted := consumePhaseAttempts(g)
	if len(counted) == 0 {
		return describeUnhealthyVerdict(terminalErr)
	}
	// The decoded class wins over the cap shape: a meta-provider that names the
	// upstream failure in-band tells the model more than "the transport cut you
	// off" does, even when the stream ran long enough to be cap-shaped.
	if inBand := firstDecodedInBandError(counted); inBand != nil {
		return inBandSteering + terminatedSentence(inBand.Error()), shapeInBand, true
	}
	if capped := capShapedAttempts(counted); len(capped) > 0 {
		return fmt.Sprintf(capShapeSteering,
			wholeSeconds(longestContentWindow(capped)), repetitionClause(len(capped))), shapeCap, true
	}
	if allSilentStalls(counted) {
		return silentStallSteering, shapeSilentStall, true
	}
	return stallSentence(len(counted), totalAttemptDuration(counted)), shapeStall, true
}

// describeUnhealthyVerdict renders from the verdict's own stats when the round
// reached settlement without per-attempt records — the verdict carries the
// group's shape, attempt count, and elapsed time regardless.
func describeUnhealthyVerdict(terminalErr error) (what string, shape failureShape, ok bool) {
	var unhealthy *llm.ProviderUnhealthyError
	if !errors.As(terminalErr, &unhealthy) {
		return "", shapeStall, false
	}
	if unhealthy.Shape == "cap" {
		// Without per-attempt content windows, the verdict's elapsed time spread
		// across its attempts is the closest honest streaming figure — and the
		// template already marks the number approximate.
		perAttempt := unhealthy.Elapsed
		if unhealthy.Attempts > 1 {
			perAttempt /= time.Duration(unhealthy.Attempts)
		}
		return fmt.Sprintf(capShapeSteering, wholeSeconds(perAttempt), repetitionClause(unhealthy.Attempts)), shapeCap, true
	}
	return stallSentence(unhealthy.Attempts, unhealthy.Elapsed), shapeStall, true
}

// stallSentence renders the stall template. One attempt gets no count clause:
// a permanent mid-stream error settles after a single try, and plural wording
// there would be a lie.
func stallSentence(attempts int, elapsed time.Duration) string {
	if attempts <= 1 {
		return stallSteering + "."
	}
	return fmt.Sprintf("%s, %d times over %s.", stallSteering, attempts, humanElapsed(elapsed))
}

// fallbackAlsoFailedClause notes that the round walked on to a configured
// fallback which failed too, and how. It fires only when the group that failed
// last is not the group the steering describes.
func fallbackAlsoFailedClause(rec *roundRecorder, terminalErr error) string {
	if rec == nil || len(rec.Groups) == 0 {
		return ""
	}
	steering := rec.SteeringGroup()
	if steering == nil || steering == &rec.Groups[len(rec.Groups)-1] {
		return ""
	}
	kind := llm.Kind(terminalErr)
	if kind == llm.KindUnknown {
		return fallbackAlsoFailedSteering + "."
	}
	return fmt.Sprintf("%s (%s error).", fallbackAlsoFailedSteering, strings.ReplaceAll(kind.String(), "_", " "))
}

// consumePhaseAttempts returns the group's failures that happened after the
// stream opened — the only ones a consume-phase template may describe.
func consumePhaseAttempts(g *groupRecord) []attemptRecord {
	if g == nil {
		return nil
	}
	var counted []attemptRecord
	for _, a := range g.Attempts {
		if a.Err != nil && (a.Phase == llm.PhaseConsume || a.Phase == llm.PhaseSilentStall) {
			counted = append(counted, a)
		}
	}
	return counted
}

// firstDecodedInBandError returns the typed error a provider reported in-band
// on an otherwise-healthy stream, or nil when every failure was a plain
// transport death.
func firstDecodedInBandError(attempts []attemptRecord) llm.Error {
	for _, a := range attempts {
		if a.Phase != llm.PhaseConsume {
			continue
		}
		if err := decodedInBandError(a.Err); err != nil {
			return err
		}
	}
	return nil
}

// decodedInBandError reports the typed error behind a mid-stream failure when
// the provider named a cause. A stall timeout and the generic incomplete-stream
// error name none — they are the transport dying, not the provider speaking.
func decodedInBandError(err error) llm.Error {
	if err == nil || errors.Is(err, llm.ErrSSEReadTimeout) {
		return nil
	}
	var stream *llm.StreamError
	if errors.As(err, &stream) {
		return nil
	}
	var typed llm.Error
	if errors.As(err, &typed) {
		return typed
	}
	return nil
}

// capShapedAttempts returns the attempts that streamed long enough to indict a
// transport cap rather than a stall.
func capShapedAttempts(attempts []attemptRecord) []attemptRecord {
	var capped []attemptRecord
	for _, a := range attempts {
		if a.Phase == llm.PhaseConsume && a.ContentWindow >= capContentWindow {
			capped = append(capped, a)
		}
	}
	return capped
}

func allSilentStalls(attempts []attemptRecord) bool {
	for _, a := range attempts {
		if a.Phase != llm.PhaseSilentStall {
			return false
		}
	}
	return len(attempts) > 0
}

func longestContentWindow(attempts []attemptRecord) time.Duration {
	var longest time.Duration
	for _, a := range attempts {
		longest = max(longest, a.ContentWindow)
	}
	return longest
}

func totalAttemptDuration(attempts []attemptRecord) time.Duration {
	var total time.Duration
	for _, a := range attempts {
		total += a.Duration
	}
	return total
}

// repetitionClause renders the spec's optional repetition clause. A single
// occurrence gets none.
func repetitionClause(n int) string {
	switch {
	case n == 2:
		return ", twice"
	case n > 2:
		return fmt.Sprintf(", %d times", n)
	default:
		return ""
	}
}

// humanElapsed renders an elapsed span the way the steering reads it: minutes
// once the round has been grinding for one, seconds below that.
func humanElapsed(d time.Duration) string {
	if d >= time.Minute {
		return pluralizedUnit(int(math.Round(d.Minutes())), "minute")
	}
	return pluralizedUnit(wholeSeconds(d), "second")
}

func pluralizedUnit(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func wholeSeconds(d time.Duration) int { return int(math.Round(d.Seconds())) }

// terminatedSentence terminates a provider-supplied message so it reads as a sentence
// without doubling punctuation the provider already wrote.
func terminatedSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if last, _ := utf8.DecodeLastRuneInString(s); strings.ContainsRune(".!?…", last) {
		return s
	}
	return s + "."
}
