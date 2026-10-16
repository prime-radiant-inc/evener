package contextmgr

import (
	"errors"

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
