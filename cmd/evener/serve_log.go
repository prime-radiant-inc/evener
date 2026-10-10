package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"primeradiant.com/evener/llm"
)

// serveLogTimeLayout stamps a [serve] line as RFC3339 in UTC at millisecond
// precision. UTC cross-references directly against the provider API logs and
// the rendezvous started_at values an operator reads alongside it, and the
// fixed-width sub-second field keeps the column straight where RFC3339Nano's
// trailing-zero trimming would ripple it.
const serveLogTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// serveLogf writes one [serve] line labelled with the session it belongs to
// and the moment it happened.
//
// A daemon's output lands in a sink it does not own, and on a hub that sink
// holds every daemon's lines at once. Only this process authoritatively knows
// which session it is and what time it is, so it says both itself rather than
// leaving a reader to infer attribution from whichever line came before
// (kata vca1).
func serveLogf(w io.Writer, sessionID, format string, args ...any) {
	serveLogAt(w, time.Now(), sessionID, format, args...)
}

// serveLogAt is serveLogf against a caller-supplied instant.
func serveLogAt(w io.Writer, at time.Time, sessionID, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "[serve %s session=%s] %s\n",
		at.UTC().Format(serveLogTimeLayout), sessionID, fmt.Sprintf(format, args...))
}

// turnFailureSummary renders a failed turn for the daemon log. A provider
// failure's error body can carry a credential fragment or the user's request
// text and must never reach run/logs/daemon-*.log, so it is rendered as the
// failure's kind and HTTP status with no provider text
// (llm.ProviderFailureSummary, the same summary the pause warning carries).
// Every other error is Evener's own — a closed session, a refused admission —
// and keeps its message so the log still says what failed. A configuration
// diagnosis keeps its remediation text too, unless it wraps a provider
// failure: llm/providers/google builds the Vertex regional-404 remedy with the
// provider's own words in the message, so one that wraps an llm.Error is
// summarized instead (#3418).
func turnFailureSummary(err error) string {
	if errors.Is(err, llm.ErrSignInRequired) {
		return "sign-in required"
	}
	if ce, ok := errors.AsType[*llm.ConfigurationError](err); ok {
		if _, wrapsProvider := errors.AsType[llm.Error](ce.Cause); wrapsProvider {
			return llm.ProviderFailureSummary(ce)
		}
		return err.Error()
	}
	if _, ok := errors.AsType[llm.Error](err); !ok {
		return err.Error()
	}
	return llm.ProviderFailureSummary(err)
}
