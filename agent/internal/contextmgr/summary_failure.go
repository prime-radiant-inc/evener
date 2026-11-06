package contextmgr

import (
	"errors"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
)

// summaryRejectionError identifies a local validation failure without retaining the
// summary text or the provider's raw finish reason.
type summaryRejectionError string

const (
	errSummaryEmptyReply     summaryRejectionError = "empty_reply"
	errSummaryMissingSection summaryRejectionError = "missing_summary_section"
	errSummaryOutputLimit    summaryRejectionError = "output_limit"
	errSummaryEmptyBatch     summaryRejectionError = "empty_batch"
	errSummaryUncondensable  summaryRejectionError = "uncondensable_partner_messages"
	errSummaryMissingModel   summaryRejectionError = "missing_model"
)

func (reason summaryRejectionError) Error() string {
	switch reason {
	case errSummaryEmptyReply:
		return "summarizer returned an empty reply"
	case errSummaryMissingSection:
		return "summarizer reply has no summary section with content"
	case errSummaryOutputLimit:
		return "the summary was cut off at the model's output limit"
	case errSummaryEmptyBatch:
		return "summarizer returned an empty batch summary"
	case errSummaryUncondensable:
		return "partner messages did not condense to fit the summarizer"
	case errSummaryMissingModel:
		return "summarization model is empty"
	default:
		return "summary output rejected"
	}
}

// summaryFailureError keeps the original error chain for callers. Only detail
// may cross the warning/log boundary; cause can contain conversation text.
type summaryFailureError struct {
	detail diagnostic.SummaryFailure
	cause  error
}

func (e *summaryFailureError) Error() string { return e.cause.Error() }
func (e *summaryFailureError) Unwrap() error { return e.cause }

func summaryAttempt(route summarizationRoute, role string, err error) diagnostic.SummaryAttempt {
	attempt := diagnostic.SummaryAttemptFromError(route.provider, route.model, role, err)
	if rejection, ok := errors.AsType[summaryRejectionError](err); ok {
		attempt.Kind, attempt.Stage, attempt.Reason = "rejected_output", "validation", string(rejection)
		if rejection == errSummaryMissingModel {
			attempt.Kind, attempt.Stage = "configuration", "pre_dispatch"
		}
	}
	return attempt
}

func summaryFailure(err error, route summarizationRoute, role string) *summaryFailureError {
	if err == nil {
		return nil
	}
	failure := &summaryFailureError{cause: err}
	if resolution, ok := errors.AsType[*cheapmodel.ResolutionError](err); ok {
		route = summarizationRoute{provider: resolution.Terminal.Provider, model: resolution.Terminal.Model}
		failure.detail.Terminal = summaryAttempt(route, role, resolution.Terminal.Err)
		for _, previous := range resolution.Previous[:min(len(resolution.Previous), 2)] {
			failure.detail.Previous = append(failure.detail.Previous, summaryAttempt(summarizationRoute{provider: previous.Provider, model: previous.Model}, "cheap", previous.Err))
		}
	} else {
		failure.detail.Terminal = summaryAttempt(route, role, err)
	}
	return failure
}

// summaryFailureSignature identifies a summary failure by its safe diagnostic
// shape: every route's provider, model, kind, reason, HTTP status and attempt
// count, terminal first. Two failures with the same signature differ in
// nothing the warning or its daemon-log record would say, so the second is
// not news.
func summaryFailureSignature(failure *diagnostic.SummaryFailure) string {
	if failure == nil {
		return ""
	}
	var b strings.Builder
	write := func(attempt diagnostic.SummaryAttempt) {
		b.WriteString(attempt.Provider)
		b.WriteByte('/')
		b.WriteString(attempt.Model)
		b.WriteByte('|')
		b.WriteString(attempt.Kind)
		b.WriteByte('|')
		b.WriteString(attempt.Reason)
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(attempt.Status))
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(attempt.Attempts))
		b.WriteByte('|')
	}
	write(failure.Terminal)
	for _, previous := range failure.Previous {
		write(previous)
	}
	return b.String()
}

// shouldPublishSummaryFailure reports whether a summary failure's warning
// should publish. An automatic pass (suppressRepeats) publishes only when the
// failure differs from the most recently published one. An explicit compaction
// always publishes and refreshes the latch, so an identical following
// automatic failure stays quiet: the user just heard this one.
func (cm *Manager) shouldPublishSummaryFailure(failure *diagnostic.SummaryFailure, suppressRepeats bool) bool {
	if !suppressRepeats {
		cm.publishSummaryFailureRepeat(failure)
		return true
	}
	return cm.claimSummaryFailureRepeat(failure)
}

// claimSummaryFailureRepeat reports whether an automatic pass should publish
// this summary failure's warning: true when its signature differs from the
// most recently published one, false when it repeats it. A claimed signature
// is stored, so the next identical failure stays quiet until a different
// failure publishes or clearSummaryFailureRepeat re-arms publication.
func (cm *Manager) claimSummaryFailureRepeat(failure *diagnostic.SummaryFailure) bool {
	signature := summaryFailureSignature(failure)
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if signature != "" && signature == cm.lastSummaryFailureRepeat {
		return false
	}
	cm.lastSummaryFailureRepeat = signature
	return true
}

// publishSummaryFailureRepeat records a failure published unconditionally —
// explicit compaction always reports its outcome — so an identical following
// automatic failure stays quiet: the user just heard this one.
func (cm *Manager) publishSummaryFailureRepeat(failure *diagnostic.SummaryFailure) {
	if signature := summaryFailureSignature(failure); signature != "" {
		cm.mu.Lock()
		cm.lastSummaryFailureRepeat = signature
		cm.mu.Unlock()
	}
}

// clearSummaryFailureRepeat re-arms warning publication after a summarize
// layer ran to completion, installed or not: either way the fold is no longer
// stuck on the failure it was repeating.
func (cm *Manager) clearSummaryFailureRepeat() {
	cm.mu.Lock()
	cm.lastSummaryFailureRepeat = ""
	cm.mu.Unlock()
}

// summarizerFailureWarning never passes a raw error to a prose classifier:
// provider errors can echo the conversation and warnings reach Notification hooks.
func summarizerFailureWarning(err error) events.WarningData {
	var failure *summaryFailureError
	if !errors.As(err, &failure) {
		failure = summaryFailure(err, summarizationRoute{}, "none")
	}
	var detail diagnostic.SummaryFailure
	if failure != nil {
		detail = failure.detail
	} else {
		detail.Terminal = summaryAttempt(summarizationRoute{}, "none", nil)
	}
	message, info := diagnostic.SummaryFailureInfo(detail)
	return events.WarningData{
		Message:        message,
		Source:         string(info.Source),
		Code:           events.WarningCodeSummaryFailure,
		Title:          info.Title,
		Hint:           info.Hint,
		SummaryFailure: &detail,
	}
}
