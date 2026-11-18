package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
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
	done  chan struct{}
	// writing is set for as long as the worker is inside a write. The one slot
	// is free only when it is clear, so an offer can never land in a slot the
	// worker has already drained.
	writing atomic.Int32
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
		done:  make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *serveDiagnosticSpool) run() {
	defer close(p.done)
	for {
		select {
		case line := <-p.lines:
			// Hold the slot until the write finishes. Reading the line frees
			// the channel, so a worker that read and then blocked would let the
			// next offer land in a slot nothing will read again -- accepted and
			// then dropped, which is the one outcome the caller cannot see.
			p.writing.Store(1)
			serveLogAt(p.out, line.at, line.sessionID, "%s", line.text)
			p.writing.Store(0)
		case <-p.stop:
			// A stop can arrive while a line this spool accepted is still
			// queued. Drain what is already here before exiting: the record
			// was taken from its producer, so dropping it would lose the last
			// line before a shutdown for no reason a writable sink explains.
			for {
				select {
				case line := <-p.lines:
					serveLogAt(p.out, line.at, line.sessionID, "%s", line.text)
				default:
					return
				}
			}
		}
	}
}

// Logf queues one diagnostic line and reports whether the spool accepted it. It
// never blocks the caller: a line that arrives while the worker is still writing
// an earlier line is refused.
//
// The result is not decoration. A caller that has state to update on the
// strength of a report -- the park a wake owes exactly one line -- must update
// it only when this returns true, or a refused line leaves the state saying the
// daemon was told when it was not.
func (p *serveDiagnosticSpool) Logf(sessionID, format string, args ...any) bool {
	return p.logAt(time.Now(), sessionID, format, args...)
}

// logAt is Logf against a caller-supplied instant: the moment the line was
// emitted, which is what the worker stamps rather than the moment the sink
// became free.
func (p *serveDiagnosticSpool) logAt(at time.Time, sessionID, format string, args ...any) bool {
	if p.writing.Load() != 0 {
		return false
	}
	select {
	case p.lines <- serveDiagnosticLine{sessionID: sessionID, text: fmt.Sprintf(format, args...), at: at}:
		return true
	default:
		return false
	}
}

// serveDiagnosticSpoolShutdownBudget bounds how long Close waits for the worker
// to drain. It is short on purpose: it exists to let an accepted record reach a
// sink that is still draining, not to give a stalled sink more time.
const serveDiagnosticSpoolShutdownBudget = 250 * time.Millisecond

// Close stops the spool, lets its worker drain what was already accepted, and
// waits for it within a fixed budget.
//
// The wait is what makes the last record deterministic: a producer that handed
// a line to the spool and then shut down would otherwise return before the
// worker wrote it, and a responsive sink would lose the line that recorded why
// the daemon was stopping. The budget is what keeps that from becoming a new
// dependency: a write stuck on a stalled sink cannot be recalled, so Close
// gives up on it rather than holding shutdown behind the sink it is leaving.
func (p *serveDiagnosticSpool) Close() {
	close(p.stop)
	select {
	case <-p.done:
	case <-time.After(serveDiagnosticSpoolShutdownBudget):
	}
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
