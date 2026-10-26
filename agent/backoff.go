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
