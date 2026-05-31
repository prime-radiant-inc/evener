package llm

import "time"

// RetryPolicy configures how retry attempts are spaced, including the maximum
// number of retries, the exponential backoff delays, optional jitter, and an
// optional callback invoked before each retry.
type RetryPolicy struct {
	// MaxRetries is the number of retry attempts (not counting the initial attempt).
	MaxRetries int

	// BaseDelay is the delay before the first retry attempt.
	BaseDelay time.Duration

	// MaxDelay caps the delay between retries.
	MaxDelay time.Duration

	// BackoffMultiplier controls exponential backoff growth (2.0 = double each retry).
	BackoffMultiplier float64

	// Jitter adds randomness to delays to reduce thundering-herd retries.
	Jitter bool

	// OnRetry is invoked before sleeping for a retry attempt.
	OnRetry func(err error, attempt int, delay time.Duration)
}

// DefaultRetryPolicy returns a RetryPolicy with 4 retries, a 1 second base
// delay, a 60 second maximum delay, a backoff multiplier of 2.0, and jitter
// enabled.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:        4,
		BaseDelay:         1 * time.Second,
		MaxDelay:          60 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            true,
	}
}
