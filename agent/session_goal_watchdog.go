package agent

import (
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/goal"
)

// Quiet-goal watchdog (spec §6): at most two owner notices per park stretch —
// park-start plus one half-deadline reminder anchored to the earliest live
// wait deadline (tie → smallest wait_id) — reset on wake/activity, plus one
// for active-but-quiet past the quiet threshold (default 30m, configurable).
// Per-goal per-24h ceiling: park-start notices fire only for stretches
// exceeding the quiet threshold (a 60s wait costs zero notices); no watchdog
// notice emits before threshold-crossing, and the half-deadline reminder
// fires only if it falls after crossing (a 40m-deadline wait's 20m reminder
// is therefore suppressed when the stretch ends first). At most 4 watchdog
// notices per goal per 24h across stretches; repeat-digest stretches past the
// ceiling emit nothing further. Stage-2 bounded auto-parks never emit
// park-start (coalesced into the stall episode's single notice). Delivery is
// the named EventGoalWatchdog announcement (never a model turn — parked goals
// run zero turns). "Quiet" is defined: no ledger change, no wake, no
// owner-visible output since stretch start; the wake itself resets the
// stretch. A 24h park therefore notifies at most twice (park-start + optional
// single reminder at half-deadline), never ~144 pings.

// goalWatchdogState is the session-local watchdog bookkeeping. The persisted
// goal carries no watchdog clock: stretches anchor on live reads (park entry
// / last activity) under s.mu, evaluated with sclock time so FakeClock tests
// are deterministic.
type goalWatchdogState struct {
	// stretchStart anchors the current quiet stretch (park entry for
	// waiting goals, last activity for active goals). Zero means no
	// stretch is tracked.
	stretchStart time.Time
	// anchored marks an explicitly anchored start (first-track backdate to
	// registration/creation, or an activity reset): later checks in the
	// same stretch keep it. Without this, an activity reset followed by a
	// kind/anchor change would backdate past the reset and un-notify
	// quiet the reset had cleared.
	anchored bool
	// stretchKind is "park" or "active" for the tracked stretch.
	stretchKind string
	// stretchNotices counts watchdog notices emitted in this stretch (cap 2:
	// park-start + one half-deadline reminder).
	stretchNotices int
	// halfReminderDone marks the single half-deadline reminder delivered.
	halfReminderDone bool
	// anchorDeadline is the earliest live wait deadline the half-deadline
	// reminder anchored to (tie → smallest wait_id, the §6 chip rule).
	anchorDeadline time.Time
	anchorWaitID   string
	// sentTimes holds the rolling 24h notice timestamps for the per-goal
	// ceiling (at most 4 per 24h across stretches).
	sentTimes []time.Time
	// goalKey identifies the goal the state belongs to (the objective at
	// stretch start, plus the creation instant): a retarget/clear resets the
	// stretch — including a same-text retarget, which store.Set stamps as a
	// new goal (fresh CreatedAt) with cleared waits/ledger. The reset covers
	// the stretch, the per-stretch notice count, AND the per-goal 24h
	// sentTimes ceiling: the ceiling is per-goal per its field comment, so a
	// new goal restarts it (no sentTimes carry-over).
	goalKey string
	// goalCreated anchors the key above: SetGoal stamps CreatedAt=now on
	// every Set, so (goalKey, goalCreated) distinguishes a same-text new
	// goal from the old one the watchdog reader otherwise cannot tell
	// apart (the store snapshot carries no generation counter).
	goalCreated time.Time
}

// goalWatchdogQuietThreshold aliases the single-source watchdog floor
// (goal.WatchdogQuietThreshold, fix-1/4 m1): no watchdog notice emits before
// the stretch crosses it.
const goalWatchdogQuietThreshold = goal.WatchdogQuietThreshold

// checkGoalWatchdog evaluates the quiet-goal watchdog at now (sclock) and
// emits at most one EventGoalWatchdog notice. Deterministic: callers pass
// the clock instant explicitly; tests advance the FakeClock and call this
// seam directly. Must be called with no session locks held (emits + store
// reads take their own locks).
func (s *Session) checkGoalWatchdog(now time.Time) {
	full, ok := s.getOrCreateGoalStore().GoalSnapshot()
	if !ok {
		s.resetGoalWatchdog("")
		return
	}
	if full.Status != goal.StatusWaiting && full.Status != goal.StatusActive {
		s.resetGoalWatchdog("")
		return
	}
	// Stage-2 bounded auto-parks never emit park-start: the stall episode's
	// single nudge note already covered the episode (spec §6 coalescing).
	if full.Status == goal.StatusWaiting && isAutoParkOnly(full) {
		return
	}
	kind := "active"
	var anchor time.Time
	var anchorID, anchorLabel string
	if full.Status == goal.StatusWaiting {
		kind = "park"
		anchor, anchorID, anchorLabel = earliestLiveWait(full)
	}
	s.mu.Lock()
	ws := s.goalWatchdog
	// New goal (retarget — including same-text — or clear+set) resets the
	// whole state: stretch, per-stretch notices, and the per-goal 24h
	// ceiling. CreatedAt distinguishes the same-text case (Set stamps it
	// fresh on every Set).
	if ws.goalKey != "" && (ws.goalKey != full.Objective || !ws.goalCreated.Equal(full.CreatedAt)) {
		ws = goalWatchdogState{}
	}
	if ws.goalKey == "" {
		ws.goalKey = full.Objective
		ws.goalCreated = full.CreatedAt
	}
	// New stretch on kind change, anchor change (re-park on a new deadline),
	// or first track. The anchor deadline is registration-relative (it
	// predates the stretch), so half-deadline math must anchor on the
	// registration instant, not on the first check: backdate the stretch
	// start to the anchor's registration point (deadline minus timeout) when
	// the anchor is known, clamped to now. First-track only — later checks
	// in the same stretch keep the anchored start.
	// Stretch anchor: the quiet clock starts at park entry / goal creation,
	// not at the first poll. Set parks/activates synchronously and
	// RegisterWait parks synchronously, so a poll 31m later must observe
	// quiet=31m. Anchor sources: the anchor lease's registration for park
	// stretches, the goal's creation for active stretches.
	anchorStart := now
	if kind == "park" && !anchor.IsZero() {
		for _, w := range full.Waits {
			if w.Live() && w.Lease.WaitID == anchorID && !w.Lease.RegisteredAt.IsZero() && w.Lease.RegisteredAt.Before(anchorStart) {
				anchorStart = w.Lease.RegisteredAt
			}
		}
	} else if created := goalCreatedAt(full); !created.IsZero() && created.Before(anchorStart) {
		anchorStart = created
	}
	// An explicit activity reset (anchored with a non-zero start) survives
	// kind changes (park→active or active→park): the quiet clock restarts at
	// the last activity, never backdates past it. A new park anchor (a
	// re-registration with a new deadline after the old stretch ended)
	// starts a FRESH stretch anchored at the new registration: the old
	// stretch ended (cancel/expiry/drive), and its notices must not leak
	// into the new one. Only an unanchored (fresh) stretch backdates to
	// registration/creation.
	if ws.stretchStart.IsZero() || !ws.anchored {
		ws.stretchStart = anchorStart
		ws.stretchKind = kind
		ws.stretchNotices = 0
		ws.halfReminderDone = false
		ws.anchorDeadline = anchor
		ws.anchorWaitID = anchorID
		ws.anchored = true
	} else if ws.stretchKind != kind {
		// Kind change after an explicit anchor: refresh the kind bookkeeping
		// but keep the post-activity start — the half-reminder re-anchors to
		// the new deadline against the surviving start.
		ws.stretchKind = kind
		ws.stretchNotices = 0
		ws.halfReminderDone = false
		ws.anchorDeadline = anchor
		ws.anchorWaitID = anchorID
	} else if kind == "park" && (anchorID != ws.anchorWaitID || !anchor.Equal(ws.anchorDeadline)) {
		// New park anchor (re-registration): the old stretch ended, so start
		// fresh at the new registration. Notices reset — the per-24h
		// sentTimes ceiling (not the per-stretch count) is what silences
		// repeat stretches.
		ws.stretchStart = anchorStart
		ws.stretchKind = kind
		ws.stretchNotices = 0
		ws.halfReminderDone = false
		ws.anchorDeadline = anchor
		ws.anchorWaitID = anchorID
		ws.anchored = true
	}
	// Prune the rolling 24h window. Disclosure (fix-1/4 I3): the 24h
	// ceiling lives in session-local state and is NOT persisted — a restart
	// resets it (bounded cost: at most 4 extra notices per restart, and
	// stretches still cap at 2 each). Persisting notice timestamps would
	// trade a bounded repeat for clock-skew and migration complexity.
	kept := ws.sentTimes[:0]
	for _, t := range ws.sentTimes {
		if now.Sub(t) < goal.WatchdogWindow {
			kept = append(kept, t)
		}
	}
	ws.sentTimes = kept
	emitKind := ""
	quiet := now.Sub(ws.stretchStart)
	switch kind {
	case "park":
		if quiet < goalWatchdogQuietThreshold {
			break
		}
		if ws.stretchNotices >= goal.MaxWatchdogNoticesPerStretch {
			break
		}
		if ws.stretchNotices == 0 {
			if len(ws.sentTimes) >= goal.MaxWatchdogNoticesPer24h {
				break
			}
			emitKind = "park-start"
			break
		}
		if ws.stretchNotices == 1 && !ws.halfReminderDone && !ws.anchorDeadline.IsZero() {
			half := ws.stretchStart.Add(ws.anchorDeadline.Sub(ws.stretchStart) / 2)
			// The reminder fires only if it falls after the threshold
			// crossing (a 40m-deadline wait's 20m reminder is suppressed)
			// and only once the half instant has passed.
			thresholdCross := ws.stretchStart.Add(goalWatchdogQuietThreshold)
			if !half.After(thresholdCross) {
				break
			}
			if now.Before(half) {
				break
			}
			if len(ws.sentTimes) >= goal.MaxWatchdogNoticesPer24h {
				break
			}
			emitKind = "half-deadline"
		}
	case "active":
		if quiet < goalWatchdogQuietThreshold {
			break
		}
		if ws.stretchNotices == 0 {
			if len(ws.sentTimes) >= goal.MaxWatchdogNoticesPer24h {
				break
			}
			emitKind = "active-quiet"
		}
	}
	if emitKind == "" {
		s.goalWatchdog = ws
		s.mu.Unlock()
		return
	}
	ws.stretchNotices++
	if emitKind == "half-deadline" {
		ws.halfReminderDone = true
	}
	ws.sentTimes = append(ws.sentTimes, now)
	s.goalWatchdog = ws
	s.mu.Unlock()

	s.emit(events.EventGoalWatchdog, events.GoalWatchdogData{
		Kind:                     emitKind,
		NearestLabel:             anchorLabel,
		NearestDeadlineUnixMilli: unixMilli(anchor),
	})
}

// noteGoalWatchdogActivity resets the watchdog stretch on wake/activity (the
// wake itself resets the stretch): ledger change, wake delivery, or
// owner-visible output. Call with no locks held.
func (s *Session) noteGoalWatchdogActivity(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goalWatchdog.stretchStart = now
	s.goalWatchdog.anchored = true
	s.goalWatchdog.stretchNotices = 0
	s.goalWatchdog.halfReminderDone = false
}

// noteGoalWatchdogOwnerOutput resets the watchdog stretch on owner-visible
// output (spec §6 activity signal 3): a goal that keeps reporting to its
// owner is not quiet, so the quiet clock restarts. Same reset as any other
// activity; split out so the owner-output call site reads as its own
// signal. Call with no locks held.
func (s *Session) noteGoalWatchdogOwnerOutput(now time.Time) {
	s.noteGoalWatchdogActivity(now)
}

// noteGoalWatchdogFold records ledger-fold activity (spec §6 activity signal
// 1): a committed fold whose entry advanced (novelty, digest delta, or
// waits-predicate evidence) resets the quiet stretch, so arming never
// false-positives active-quiet on advancing goals. A non-advancing fold
// leaves the stretch running — stalled quiet is exactly what the watchdog
// must observe. Advancement keys off the folded snapshot's trailing entry
// (the fold just committed it), not off the pre-fold outcome alone, so the
// signal and the persisted ledger agree. Call with no locks held.
func (s *Session) noteGoalWatchdogFold(full goal.GoalSnapshot, now time.Time) {
	if n := len(full.LedgerSummary.Entries); n > 0 && full.LedgerSummary.Entries[n-1].Advancement {
		s.noteGoalWatchdogActivity(now)
	}
}

// evalGoalWatchdogForTimer is the coalesced wait timer's watchdog piggyback
// (spec §6 Task-8 residual): parked stretches evaluate through the same
// coalesced timer that owns the wait deadlines — never a second timer. The
// timer callback invokes this after the claim path; threshold-crossing
// re-arms (the four-way min in goalWaitNextFire already includes the goal
// deadline, parked-total projection, and poll cadence — the watchdog reads
// the same sclock instant, so a stretch crossing the quiet threshold
// between fires is observed at the next fire, never missed). Pure
// delegation to checkGoalWatchdog; call with no locks held.
func (s *Session) evalGoalWatchdogForTimer(now time.Time) {
	s.checkGoalWatchdog(now)
}

// evalGoalWatchdogAtTurnTail evaluates the watchdog at the turn tail for
// active goals (spec §6 Task-8 residual): active goals arm no coalesced
// timer, so the gate tail is their only evaluation. The gate calls this on
// every committed drive tail with the gate's sclock instant; parked goals
// skip it (their stretch evaluates through the timer piggyback). Call with
// no locks held.
func (s *Session) evalGoalWatchdogAtTurnTail(full goal.GoalSnapshot, now time.Time) {
	if full.Status != goal.StatusActive {
		return
	}
	s.checkGoalWatchdog(now)
}

// resetGoalWatchdog clears the watchdog stretch (no goal, terminal goal, or
// goal identity change handled by the caller key).
func (s *Session) resetGoalWatchdog(_ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goalWatchdog = goalWatchdogState{}
}

// isAutoParkOnly reports whether every live wait is a stage-2 auto-wait
// lease: such stretches never emit park-start (spec §6 coalescing into the
// stall episode's single notice).
func isAutoParkOnly(full goal.GoalSnapshot) bool {
	live := 0
	for _, w := range full.Waits {
		if !w.Live() {
			continue
		}
		live++
		if w.Lease.Label != goal.AutoWaitLabel {
			return false
		}
	}
	return live > 0
}

// earliestLiveWait resolves the §6 chip anchor over live waits: earliest
// deadline wins, ties break on the smallest wait_id.
func earliestLiveWait(full goal.GoalSnapshot) (deadline time.Time, waitID, label string) {
	var have bool
	for _, w := range full.Waits {
		if !w.Live() {
			continue
		}
		if !have || w.Lease.Deadline.Before(deadline) ||
			(w.Lease.Deadline.Equal(deadline) && w.Lease.WaitID < waitID) {
			have = true
			deadline = w.Lease.Deadline
			waitID = w.Lease.WaitID
			label = w.Lease.Label
		}
	}
	return deadline, waitID, label
}

// unixMilli renders t as Unix epoch millis (0 for zero).
// goalCreatedAt reads the goal's creation instant from the full snapshot
// (Set time). First-track stretches anchor here: Set parks/activates
// synchronously, so a poll 31m later observes quiet=31m.
func goalCreatedAt(full goal.GoalSnapshot) time.Time { return full.CreatedAt }

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
