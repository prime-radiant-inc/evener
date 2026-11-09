package agent

import (
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
)

// quietYield is one request made by the yield tool (issue #832). It ends the
// current activation without another model round or user-visible output; the
// session then waits for its normal inbound events. A non-zero Deadline arms
// exactly one fallback wake, after which the session resumes once.
type quietYield struct {
	ID       string
	Intent   string
	Deadline time.Time
}

// armedQuietYield is the fallback timer a yield with a timeout left behind.
// Generation is compared on firing so a timer that outlived an ordinary wake
// cannot resume a session the wake already resumed.
type armedQuietYield struct {
	yield      quietYield
	generation uint64
}

// requestQuietYield records the tool call's yield and, with a timeout, arms one
// fallback wake on the session clock. It is called from the tool's Exec, before
// the tool result is persisted: the activation ends only after the round's
// results are durable, and consumeQuietYieldRequest is what marks that end.
func (s *Session) requestQuietYield(intent string, timeout time.Duration) quietYield {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	s.yieldSeq++
	y := quietYield{
		ID:     fmt.Sprintf("y%d", s.yieldSeq),
		Intent: strings.TrimSpace(intent),
	}
	if timeout > 0 {
		y.Deadline = s.sclock().Now().Add(timeout)
		armed := &armedQuietYield{yield: y, generation: s.yieldSeq}
		s.yieldArmed = armed
		generation := armed.generation
		s.sclock().AfterFunc(timeout, func() { s.fireQuietYieldTimeout(generation) })
	}
	s.yieldRequest = &y
	return y
}

// fireQuietYieldTimeout turns an armed fallback into a due record and wakes the
// daemon to run its notification turn. The generation check drops a timer an
// ordinary turn already canceled; a closed session is left for its teardown.
func (s *Session) fireQuietYieldTimeout(generation uint64) {
	release, err := s.beginRetirementMutation("autonomous")
	if err != nil {
		// Retirement is claiming. Keep the timer armed for a paced retry
		// rather than firing into a runtime that is going away.
		s.sclock().AfterFunc(jobNotificationRetryInitialDelay, func() { s.fireQuietYieldTimeout(generation) })
		return
	}
	defer release()
	s.yieldMu.Lock()
	armed := s.yieldArmed
	if armed == nil || armed.generation != generation {
		s.yieldMu.Unlock()
		return
	}
	y := armed.yield
	s.yieldArmed = nil
	s.yieldDue = &y
	s.yieldMu.Unlock()
	if s.isClosingOrClosed() {
		return
	}
	s.notify()
}

// markQuietYield records that a yield ended the activation. The flag keeps
// autonomous continuations from resuming the session before an ordinary event
// does, and it survives until the next real turn starts (or the session is
// stopped).
func (s *Session) markQuietYield() {
	s.yieldMu.Lock()
	s.yieldQuiet = true
	s.yieldMu.Unlock()
}

// lastTurnYielded reports whether the newest turn ended on a quiet yield.
func (s *Session) lastTurnYielded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTurnYieldedFlag
}

// quietYieldRequested reports whether this round's tool call asked to yield.
// It is read before the turn ends, so the round's steering is left for a
// carrier rather than drained into an activation that has no further request.
func (s *Session) quietYieldRequested() bool {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	return s.yieldRequest != nil
}

// quietYieldActive reports that the session is resting on a yield: an armed
// fallback, a due fallback, or the quiet flag a successful yield set.
func (s *Session) quietYieldActive() bool {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	return s.yieldQuiet || s.yieldArmed != nil || s.yieldDue != nil
}

// hasDueQuietYield reports whether a fired fallback still needs its
// notification turn. The paced notification retry reads it as pending work.
func (s *Session) hasDueQuietYield() bool {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	return s.yieldDue != nil
}

// takeDueQuietYield consumes the fallback a fired timer left for the next
// notification turn. It returns nil when nothing is due.
func (s *Session) takeDueQuietYield() *quietYield {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	due := s.yieldDue
	s.yieldDue = nil
	return due
}

// restoreDueQuietYield puts a due record back after the notification turn could
// not persist its reminder, so a later wake retries rather than losing it.
func (s *Session) restoreDueQuietYield(due quietYield) {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	if s.yieldDue == nil {
		s.yieldDue = &due
	}
}

// consumeQuietYieldRequest reports whether this turn's round asked to yield
// and clears the request. The fallback timer, if one was armed, deliberately
// survives: it is the wake that resumes the yielded activation.
func (s *Session) consumeQuietYieldRequest() bool {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	requested := s.yieldRequest != nil
	s.yieldRequest = nil
	return requested
}

// cancelQuietYieldWait drops the request and the fallback timer but leaves a
// due record for the notification turn that consumes it. processOneInput uses
// this for an EntryNotification turn, whose accept must be able to take a due
// fallback; every other turn ends the wait outright.
func (s *Session) cancelQuietYieldWait() {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	if s.yieldRequest == nil && s.yieldArmed == nil && !s.yieldQuiet {
		return
	}
	s.yieldSeq++
	s.yieldRequest = nil
	s.yieldArmed = nil
	s.yieldQuiet = false
}

// cancelPendingQuietYield drops the request, the fallback timer, and any due
// record. A turn that ends another way (a communicate boundary, a failure, or
// a Stop) does this because the quiet wait it was asked for is over. The
// generation bump is what makes an already-armed timer firing a no-op.
func (s *Session) cancelPendingQuietYield() {
	s.yieldMu.Lock()
	defer s.yieldMu.Unlock()
	if s.yieldRequest == nil && s.yieldArmed == nil && s.yieldDue == nil && !s.yieldQuiet {
		return
	}
	s.yieldSeq++
	s.yieldRequest = nil
	s.yieldArmed = nil
	s.yieldQuiet = false
	// A due record is the timer that already fired. An ordinary turn or a Stop
	// that lands before its notification turn consumes it must cancel the wake
	// too; the notification turn itself takes the record first, so this does
	// not drop the wake it is running.
	s.yieldDue = nil
}

// formatQuietYieldTimeoutReminder renders the bounded context a fallback wake
// carries. It names the yield, the trigger, and the deadline so the model can
// tell a timer wake from an ordinary event without a second round.
func formatQuietYieldTimeoutReminder(due quietYield) string {
	var b strings.Builder
	b.WriteString(systemNotification("quiet yield reached its fallback timeout. Resume the work it was waiting on."))
	b.WriteString("\n\nyield_id: ")
	b.WriteString(due.ID)
	b.WriteString("\ntrigger: timeout\ndeadline: ")
	b.WriteString(due.Deadline.UTC().Format(time.RFC3339))
	if due.Intent != "" {
		b.WriteString("\nintent: ")
		b.WriteString(due.Intent)
	}
	return b.String()
}

// hasKnownRunningWork reports whether a no-timeout yield has a source that can
// wake the session: a running shell job or a live delegate. Without one the
// session would wait forever with nothing scheduled to resume it, so the tool
// refuses that shape and the model must pass a timeout or start the work.
func (s *Session) hasKnownRunningWork() bool {
	if len(sessionRunningJobIDs(s)) > 0 {
		return true
	}
	return s.subagents != nil && s.subagents.hasRunningChildren()
}

// quietYieldTimeoutMaxSeconds is the enforced value of the tool definition's
// advertised bound.
const quietYieldTimeoutMaxSeconds = tool.YieldTimeoutMaxSeconds
