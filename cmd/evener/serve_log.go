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

// turnFailureSummary renders a failed turn for the daemon log as the failure's
// kind and HTTP status, never the provider's own error body: a provider body
// can carry a credential fragment or the user's request text, and this line
// lands in run/logs/daemon-*.log. It mirrors the pause line's
// providerFailureSummary (agent/session_attention.go, #3411). "HTTP 401
// (authentication)", or "sign-in required"; the failure's kind alone when the
// error carries no HTTP status (#3418).
func turnFailureSummary(err error) string {
	if errors.Is(err, llm.ErrSignInRequired) {
		return "sign-in required"
	}
	kind := llm.Kind(err).String()
	var llmErr llm.Error
	if errors.As(err, &llmErr) && llmErr.StatusCode() != 0 {
		return fmt.Sprintf("HTTP %d (%s)", llmErr.StatusCode(), kind)
	}
	return kind
}
