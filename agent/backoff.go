package agent

import "time"

// backoffDelay is attempt's (zero-based) retry delay: initial, doubling each
// attempt, capped at limit.
func backoffDelay(attempt int, initial, limit time.Duration) time.Duration {
	delay := initial
	for range attempt {
		delay *= 2
		if delay >= limit {
			return limit
		}
	}
	return delay
}

// backoffTimer is the state of one generation-guarded retry rail: a one-shot
// clock timer that re-arms with a doubling delay while its work stays pending.
// The timer cannot be cancelled once armed, so every cancel bumps generation
// and a firing proceeds only if the generation it captured at arm time is
// still current.
//
// It is not self-locking. Each rail keeps it under the mutex that guards the
// work it retries, and the firing callback takes that mutex before calling
// fire.
type backoffTimer struct {
	active     bool
	delay      time.Duration
	generation int
}

// arm marks the timer armed and returns the delay to schedule and the
// generation the firing must still own.
func (b *backoffTimer) arm() (time.Duration, int) {
	delay := b.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	b.active = true
	b.generation++
	return delay, b.generation
}

// superseded reports whether a cancel or a newer arm has invalidated the
// firing that captured generation.
func (b *backoffTimer) superseded(generation int) bool {
	return b.generation != generation
}

// fire reports whether the firing that captured generation still owns the
// timer, and if so marks the timer no longer armed.
func (b *backoffTimer) fire(generation int) bool {
	if b.superseded(generation) {
		return false
	}
	b.active = false
	return true
}

// disarm strands an armed firing and leaves the backoff delay where it is.
func (b *backoffTimer) disarm() {
	b.generation++
	b.active = false
}

// disarmIfArmed is disarm for a timer that may not be armed: an idle timer
// keeps its generation.
func (b *backoffTimer) disarmIfArmed() {
	if b.active {
		b.disarm()
	}
}

// reset ends the backoff episode: it strands an armed firing and returns the
// delay to the initial one.
func (b *backoffTimer) reset() {
	b.disarm()
	b.delay = jobNotificationRetryInitialDelay
}

// settle sets the delay of the next arm after a firing. Work still pending
// doubles from, up to ceiling; a rail whose work is done starts over. Most
// rails double the delay they were armed with. The stable-delegate rail passes
// the current b.delay instead, so a delay reset that landed while the timer was
// armed holds rather than being doubled back up.
func (b *backoffTimer) settle(pending bool, from, ceiling time.Duration) {
	if pending {
		b.delay = min(from*2, ceiling)
		return
	}
	b.delay = jobNotificationRetryInitialDelay
}
