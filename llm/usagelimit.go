package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// resetTimeLayout renders a quota reset instant in the reader's own timezone.
// The zone abbreviation is part of the layout because the wait can span days,
// and a bare wall-clock time is ambiguous over that distance.
const resetTimeLayout = "Mon Jan 2 15:04 MST"

// usageLimitCodes are the provider error codes that mean "this account has
// spent its allowance", as opposed to "you are sending requests too quickly".
// Both arrive as HTTP 429; only the latter is worth retrying.
//
//   - usage_limit_reached: ChatGPT-backed plans (plan allowance exhausted,
//     resets on a rolling multi-hour or multi-day window).
//   - insufficient_quota: the standard OpenAI API (billing quota spent; clears
//     only when the account is topped up).
var usageLimitCodes = map[string]bool{
	"usage_limit_reached": true,
	"insufficient_quota":  true,
}

// usageLimit is the parsed detail of a usage-limit rejection. resetsAt is the
// zero time when the provider did not say when the allowance returns.
type usageLimit struct {
	code     string
	message  string
	planType string
	resetsAt time.Time
}

// parseUsageLimit reports whether raw is a usage-limit rejection body and, if
// so, extracts its message, plan, and reset instant. now anchors the relative
// resets_in_seconds fallback.
//
// The reset instant is taken from resets_at (absolute Unix seconds) when
// present and sane, else from resets_in_seconds (relative). An unusable value
// yields a zero resetsAt rather than a fabricated timestamp — a wrong reset
// time is worse than none, because the user plans around it.
func parseUsageLimit(raw any, now time.Time) (usageLimit, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return usageLimit{}, false
	}
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		return usageLimit{}, false
	}

	// extractErrorCode reads error.code then error.type; a usage limit may
	// arrive under either key depending on the provider surface.
	code := ""
	for _, key := range []string{"code", "type"} {
		if v, _ := errObj[key].(string); usageLimitCodes[strings.ToLower(strings.TrimSpace(v))] {
			code = strings.ToLower(strings.TrimSpace(v))
			break
		}
	}
	if code == "" {
		return usageLimit{}, false
	}

	limit := usageLimit{code: code}
	limit.message, _ = errObj["message"].(string)
	limit.planType, _ = errObj["plan_type"].(string)

	if secs, ok := jsonInt64(errObj["resets_at"]); ok && secs > 0 {
		limit.resetsAt = time.Unix(secs, 0)
	} else if secs, ok := jsonInt64(errObj["resets_in_seconds"]); ok && secs > 0 {
		limit.resetsAt = now.Add(time.Duration(secs) * time.Second)
	}
	return limit, true
}

// jsonInt64 coerces a decoded JSON number to seconds. Adapters decode with
// UseNumber, so the common case is json.Number; float64 is accepted for bodies
// decoded without it. Values that overflow int64 seconds are rejected so a
// hostile or buggy body cannot wrap a Duration into the past.
func jsonInt64(v any) (int64, bool) {
	const maxSecs = int64(math.MaxInt64) / int64(time.Second)
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			f, ferr := n.Float64()
			if ferr != nil || f > float64(maxSecs) || f < float64(-maxSecs) {
				return 0, false
			}
			return int64(f), true
		}
		if i > maxSecs || i < -maxSecs {
			return 0, false
		}
		return i, true
	case float64:
		if n > float64(maxSecs) || n < float64(-maxSecs) {
			return 0, false
		}
		return int64(n), true
	case int64:
		if n > maxSecs || n < -maxSecs {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// usageLimitMessage renders the one-line failure text: the provider's own
// wording, the plan when known, and the reset window in both relative and
// absolute form. Both time forms are present because they answer different
// questions — "how long am I blocked" and "when exactly do I come back".
func usageLimitMessage(limit usageLimit, now time.Time) string {
	msg := strings.TrimSpace(limit.message)
	if msg == "" {
		msg = "usage limit reached"
	}
	var parts []string
	if plan := strings.TrimSpace(limit.planType); plan != "" {
		parts = append(parts, "plan: "+plan)
	}
	if !limit.resetsAt.IsZero() {
		parts = append(parts, "resets "+formatResetWindow(limit.resetsAt, now))
	}
	if len(parts) == 0 {
		return msg
	}
	return fmt.Sprintf("%s (%s)", strings.TrimRight(msg, "."), strings.Join(parts, ", "))
}

// formatResetWindow renders resetsAt as "in 3d 17h (Tue Jul 28 10:02 PDT)".
// The relative half is coarse on purpose: at a multi-day distance, seconds of
// precision are noise, and a two-unit form reads at a glance.
func formatResetWindow(resetsAt time.Time, now time.Time) string {
	return fmt.Sprintf("%s (%s)", relativeWait(resetsAt.Sub(now)), resetsAt.Local().Format(resetTimeLayout))
}

// relativeWait renders a wait as at most two descending units.
func relativeWait(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		if hours == 0 {
			return fmt.Sprintf("in %dd", days)
		}
		return fmt.Sprintf("in %dd %dh", days, hours)
	case d >= time.Hour:
		hours := int(d / time.Hour)
		mins := int((d % time.Hour) / time.Minute)
		if mins == 0 {
			return fmt.Sprintf("in %dh", hours)
		}
		return fmt.Sprintf("in %dh %dm", hours, mins)
	case d >= time.Minute:
		return fmt.Sprintf("in %dm", int(d/time.Minute))
	default:
		return "in under a minute"
	}
}

// UsageLimitResetAt reports when err's exhausted usage allowance returns.
// It holds only for a usage-limit rejection whose provider named a reset time;
// ok is false for every other error, including a plain rate limit and a usage
// limit that arrived without reset information.
//
// Callers use this to render a wait without re-parsing the provider body — e.g.
// to show a countdown, or to decide whether resuming later is worthwhile.
func UsageLimitResetAt(err error) (time.Time, bool) {
	if err == nil {
		return time.Time{}, false
	}
	var q *quotaExceededError
	if !errors.As(err, &q) || q.usageLimitResetsAt.IsZero() {
		return time.Time{}, false
	}
	return q.usageLimitResetsAt, true
}
