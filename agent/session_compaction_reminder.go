package agent

import (
	"fmt"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// Checkpoint compaction reminder (SoL-Pi auto-research design, mechanism 2):
// at task-list step completion boundaries the harness MAY inject a steering
// reminder that compaction is available and cheap right now. The cost gate
// lives in the reminder: it fires only when the projected input savings —
// the observed request rate between completed steps, times the remaining
// step count, times the typical request input size, priced through
// llm/pricing.go — beat the prompt-cache rewrite cost computed from the
// recent cache-write token accounting at the model's cache-write rate, by
// an escalating margin. The agent elects compaction through its existing
// compact_context tool; this mechanism's only externally visible action is
// the steering message itself. The harness never forces compaction.

// checkpointReminderMargins is the escalating margin schedule: the first
// reminder needs projected savings above 1.0× the rewrite cost, the second
// above 1.5×, the third and every later one above 2.0× (capped, so an agent
// that keeps ignoring reminders is never nagged more easily for it).
var checkpointReminderMargins = []float64{1.0, 1.5, 2.0}

// checkpointReminderMargin returns the savings-to-cost ratio the attempt
// after remindersIssued reminders must beat.
func checkpointReminderMargin(remindersIssued int) float64 {
	if remindersIssued < 0 {
		remindersIssued = 0
	}
	if remindersIssued >= len(checkpointReminderMargins) {
		return checkpointReminderMargins[len(checkpointReminderMargins)-1]
	}
	return checkpointReminderMargins[remindersIssued]
}

// checkpointReminderGateInput is the observed state the cost gate reads.
// Every field comes from per-session counters or recorded request usage —
// nothing is estimated from history size, so packed or masked request views
// are already reflected in the recorded numbers.
type checkpointReminderGateInput struct {
	rate            float64   // observed model requests per completed step, over closed inter-boundary windows
	remaining       int       // task-list steps remaining after this completion
	typicalInput    int       // most recent recorded request's total input tokens
	cacheWrite      int       // most recent recorded request's cache-write tokens
	cacheWriteSeen  bool      // that request reported cache-write accounting at all
	price           llm.Price // the session model's rates, from llm.PriceFromCost
	remindersIssued int       // reminders injected since the last published compaction
}

// gatePass evaluates the cost gate. It returns whether the reminder may
// fire, together with both sides of the comparison it decided on. The gate
// is uncomputable — and therefore never fires — when the recent request
// reported no cache-write accounting (the rewrite cost has no basis), when
// no request has been recorded yet, or when the pricing row carries no
// cache-write rate: a fabricated zero is a fabricated estimate, so the
// mechanism degrades to silence rather than to a guess.
func (in checkpointReminderGateInput) gatePass() (pass bool, savingsUSD, rewriteUSD float64) {
	if in.typicalInput <= 0 || !in.cacheWriteSeen || in.price.CacheCreation5mPerM == nil {
		return false, 0, 0
	}
	rewrite := float64(in.cacheWrite) * *in.price.CacheCreation5mPerM / 1e6
	savingsTokens := in.rate * float64(in.remaining) * float64(in.typicalInput)
	if savingsTokens <= 0 {
		return false, 0, rewrite
	}
	// The savings side prices the projected input tokens at the input rate
	// through llm.EstimateCost, keeping pricing.go the single rate source.
	savings := llm.EstimateCost(int64(savingsTokens), 0, 0, in.price)
	return savings > checkpointReminderMargin(in.remindersIssued)*rewrite, savings, rewrite
}

// checkpointCompactionReminder renders the steering reminder. It is advice,
// never an order: the projection and the rewrite cost are stated so the
// agent can judge the claim, and the option to skip is explicit.
func checkpointCompactionReminder(savingsUSD, rewriteUSD float64, remaining int) string {
	b := systemReminderBlockBuilder()
	fmt.Fprintf(b, "You just completed a step on your task list. Compaction is available and cheap right now: the projected input savings of compacting here (about $%.4f over the %d remaining step(s)) beat the estimated prompt-cache rewrite cost (about $%.4f).\n",
		savingsUSD, remaining, rewriteUSD)
	b.WriteString("If this is a clean stopping point, you may call the `compact_context` tool now to fold older history into a checkpoint; a note_to_self keeps the exact details that must survive. This is optional: skip it and keep working if you are mid-step.\n")
	return finishSystemReminderBlock(b)
}

// onTaskStepCompletion runs the checkpoint compaction reminder gate at one
// task-list step-completion boundary. It closes and reopens the
// requests-between-steps observation window, evaluates the cost gate, and —
// only when projected savings beat the margin-adjusted rewrite cost — queues
// the steering reminder the agent may act on with its existing
// compact_context tool. This method never compacts by itself: its only
// externally visible action is the SteerKind call.
//
// The first observed boundary only opens the rate window: no interval
// between completed steps exists yet, so the observed rate has no basis and
// the gate cannot run honestly.
func (s *Session) onTaskStepCompletion(remaining int) error {
	if !s.cfg.CheckpointReminder || s.contextMgr == nil || !s.canInstructTool("compact_context") {
		return nil
	}
	s.mu.Lock()
	requests := s.modelResponses
	if !s.ckptWindowOpen {
		s.ckptWindowOpen = true
		s.ckptWindowOpenRequests = requests
		s.mu.Unlock()
		return nil
	}
	s.ckptRateRequests += max(requests-s.ckptWindowOpenRequests, 0)
	s.ckptRateWindows++
	s.ckptWindowOpenRequests = requests
	rate := float64(s.ckptRateRequests) / float64(s.ckptRateWindows)
	in := checkpointReminderGateInput{
		rate:            rate,
		remaining:       remaining,
		typicalInput:    s.ckptLastInputTokens,
		cacheWriteSeen:  s.ckptLastCacheWrite != nil,
		remindersIssued: s.ckptRemindersIssued,
	}
	if s.ckptLastCacheWrite != nil {
		in.cacheWrite = *s.ckptLastCacheWrite
	}
	s.mu.Unlock()

	price, priced := llm.PriceFromCost(s.currentProfile().Cost())
	if !priced {
		return nil // no pricing row for the model: the cost gate cannot be computed
	}
	in.price = price
	pass, savings, rewrite := in.gatePass()
	if !pass {
		return nil
	}

	// Latch the escalation before steering (the maybeNudgeSelfCompact
	// pattern): a failed steer rolls the count back so the attempt is not
	// silently lost.
	s.mu.Lock()
	s.ckptRemindersIssued++
	issued := s.ckptRemindersIssued
	s.mu.Unlock()
	if err := s.SteerKind(checkpointCompactionReminder(savings, rewrite, remaining), events.SteeringKindCheckpointReminder); err != nil {
		s.mu.Lock()
		// A published compaction may have re-armed the ladder while the
		// steer was in flight; only undo what is still ours.
		if s.ckptRemindersIssued == issued {
			s.ckptRemindersIssued--
		}
		s.mu.Unlock()
		return err
	}
	return nil
}
