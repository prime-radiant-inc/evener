package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/execsupport/componentlog"
	"primeradiant.com/evener/llm"
)

// serveLogTimeLayout stamps a [serve] line as RFC3339 in UTC at millisecond
// precision. UTC cross-references directly against the provider API logs and
// the rendezvous started_at values an operator reads alongside it, and the
// fixed-width sub-second field keeps the column straight where RFC3339Nano's
// trailing-zero trimming would ripple it.
const serveLogTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// serveLogf writes one [serve] line labelled with the session it belongs to
// and the moment it happened. Both travel as structured fields the
// componentlog handler emits, so a sink can read them by name.
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
	componentlog.NewWriter("serve", w).Info(fmt.Sprintf(format, args...),
		"at", at.UTC().Format(serveLogTimeLayout), "session", sessionID)
}

// serveDiagnosticSpool carries diagnostics onto a sink the daemon does not own
// without letting that sink stall the daemon.
//
// A daemon's output is a pipe to whatever started it. When that reader stops
// draining, the pipe fills and every write blocks; a diagnostic written on the
// input loop would then stop the only goroutine that runs turns, and shutdown,
// which joins that loop, would wait behind it. The spool holds one line at a
// time and drops a line that arrives while that slot is taken, so a diagnostic
// line can never cost the daemon progress. Losing a repeat of a stand-down is
// cheaper than stalling on the report of one.
type serveDiagnosticSpool struct {
	out   io.Writer
	lines chan serveDiagnosticLine
	stop  chan struct{}
}

// serveDiagnosticLine is one queued diagnostic, with the session it belongs to.
type serveDiagnosticLine struct {
	sessionID string
	text      string
	// at is the moment the line was emitted, not the moment the worker
	// reached the sink. A line held here while an earlier write blocks would
	// otherwise be stamped after events that happened while it waited, which
	// is the ordering the stamp exists to establish.
	at time.Time
}

// newServeDiagnosticSpool starts the writer goroutine. It is the spool's one
// worker, so lines keep their order relative to each other.
func newServeDiagnosticSpool(out io.Writer) *serveDiagnosticSpool {
	p := &serveDiagnosticSpool{
		out:   out,
		lines: make(chan serveDiagnosticLine, 1),
		stop:  make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *serveDiagnosticSpool) run() {
	for {
		select {
		case <-p.stop:
			return
		case line := <-p.lines:
			serveLogAt(p.out, line.at, line.sessionID, "%s", line.text)
		}
	}
}

// Logf queues one diagnostic line. It never blocks the caller: a line that
// arrives while the worker is still writing an earlier line is dropped.
func (p *serveDiagnosticSpool) Logf(sessionID, format string, args ...any) {
	p.logAt(time.Now(), sessionID, format, args...)
}

// logAt is Logf against a caller-supplied instant: the moment the line was
// emitted, which is what the worker stamps rather than the moment the sink
// became free.
func (p *serveDiagnosticSpool) logAt(at time.Time, sessionID, format string, args ...any) {
	select {
	case p.lines <- serveDiagnosticLine{sessionID: sessionID, text: fmt.Sprintf(format, args...), at: at}:
	default:
	}
}

// Close retires the spool without waiting for a write already in flight. A
// write stuck on a stalled sink cannot be recalled, and shutdown must not wait
// on the sink it is trying to leave behind.
func (p *serveDiagnosticSpool) Close() {
	close(p.stop)
}

// retirementAttemptSummary renders an automatic retirement attempt that did
// not retire the process for the daemon log: the error that failed it and the
// blockers that refused it, each with the session and delegate it belongs to.
func retirementAttemptSummary(attempt agent.RetirementAttempt) string {
	blockers := make([]string, 0, len(attempt.Blockers))
	for _, blocker := range attempt.Blockers {
		owners := []string{}
		if blocker.SessionID != "" {
			owners = append(owners, "session "+blocker.SessionID)
		}
		if blocker.DelegateID != "" {
			owners = append(owners, "delegate "+blocker.DelegateID)
		}
		if len(owners) == 0 {
			blockers = append(blockers, blocker.Category)
			continue
		}
		blockers = append(blockers, fmt.Sprintf("%s (%s)", blocker.Category, strings.Join(owners, ", ")))
	}
	refused := strings.Join(blockers, ", ")
	switch {
	case attempt.Error == "":
		return "automatic retirement refused: " + refused
	case refused == "":
		return "automatic retirement failed: " + attempt.Error
	default:
		return "automatic retirement failed: " + attempt.Error + "; refused: " + refused
	}
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
