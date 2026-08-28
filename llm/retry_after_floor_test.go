package llm

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterUsesBackoffForNonPositiveAndPreservesPositive(t *testing.T) {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	past := now.Add(-time.Second).Format(http.TimeFormat)
	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "zero", header: "0", want: 25 * time.Millisecond},
		{name: "past_date", header: past, want: 25 * time.Millisecond},
		{name: "positive", header: "2", want: 2 * time.Second},
		{name: "positive_over_max_delay", header: "5", want: 5 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/complete", func(t *testing.T) {
			ra := ParseRetryAfter(tc.header, now)
			if ra == nil {
				t.Fatalf("ParseRetryAfter(%q) = nil", tc.header)
			}
			if tc.name == "zero" || tc.name == "past_date" {
				if *ra != 0 {
					t.Fatalf("ParseRetryAfter(%q) = %s, want zero", tc.header, *ra)
				}
			}

			clock := now
			policy := RetryPolicy{
				MaxRetries:          0,
				BaseDelay:           25 * time.Millisecond,
				MaxDelay:            1 * time.Second,
				BackoffMultiplier:   2,
				RateLimitWallBudget: 10 * time.Second,
				Now:                 func() time.Time { return clock },
			}
			err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, ra)
			attempts := 0
			var sleeps []time.Duration
			sleep := func(ctx context.Context, d time.Duration) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				sleeps = append(sleeps, d)
				clock = clock.Add(d)
				return nil
			}

			_, err := Retry(context.Background(), policy, sleep, func() float64 { return 0.5 }, func() (Response, error) {
				attempts++
				if attempts == 1 {
					return Response{}, err429
				}
				return Response{Provider: "openai", Model: "m", Message: Assistant("ok")}, nil
			})
			if err != nil {
				t.Fatalf("Retry: %v", err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
			if len(sleeps) != 1 || sleeps[0] != tc.want {
				t.Fatalf("attempts = %d; sleeps = %v, want attempts=2 and sleeps=[%s]", attempts, sleeps, tc.want)
			}
		})

		t.Run(tc.name+"/stream", func(t *testing.T) {
			ra := ParseRetryAfter(tc.header, now)
			if ra == nil {
				t.Fatalf("ParseRetryAfter(%q) = nil", tc.header)
			}
			clock := now
			policy := RetryPolicy{
				MaxRetries:          0,
				BaseDelay:           25 * time.Millisecond,
				MaxDelay:            1 * time.Second,
				BackoffMultiplier:   2,
				RateLimitWallBudget: 10 * time.Second,
				Now:                 func() time.Time { return clock },
			}
			err429 := ErrorFromHTTPStatus("openai", 429, "rate limited", nil, ra)
			attempts := 0
			var sleeps []time.Duration
			sleep := func(ctx context.Context, d time.Duration) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				sleeps = append(sleeps, d)
				clock = clock.Add(d)
				return nil
			}

			err := RetryStream(context.Background(), RetryStreamOptions{Policy: policy, Sleep: sleep}, func(context.Context) (AttemptReport, error) {
				attempts++
				if attempts == 1 {
					return AttemptReport{Phase: PhaseOpen}, err429
				}
				return AttemptReport{}, nil
			})
			if err != nil {
				t.Fatalf("RetryStream: %v", err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
			if len(sleeps) != 1 || sleeps[0] != tc.want {
				t.Fatalf("attempts = %d; sleeps = %v, want attempts=2 and sleeps=[%s]", attempts, sleeps, tc.want)
			}
		})
	}
}
