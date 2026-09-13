package doctor

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm/apilog"
)

// TestAPILogExposesDisjointCacheWriteTiers pins issue #946 on the doctor
// projection: the canonical record's disjoint 5-minute and 1-hour cache-write
// usage must reach both the typed call rows and the session totals (the
// `evener doctor apilog --json` surface), and must not be folded into cache
// reads or uncached input.
func TestAPILogExposesDisjointCacheWriteTiers(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	attempt := doctorAttempt("ag_cache_write", 1, apilog.AttemptSuccess, 10, 100, 20, 30, 5, 0)
	fiveMinute := 44
	attempt.Response.Usage.CacheWriteTokens = &fiveMinute
	oneHour := 17
	attempt.Response.Usage.CacheWrite1hTokens = &oneHour
	writeRichSession(t, bucket, sidA, nil, []apilog.APILogRecord{attempt, doctorSettlement(attempt, 1)}, schema.SessionMeta{})

	result, err := APILog(base, sidA, APILogOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(result.Calls))
	}
	row := result.Calls[0]
	if !hasIntValue(row.CacheWrite, fiveMinute) {
		t.Errorf("row 5-minute cache write = %v, want %d", row.CacheWrite, fiveMinute)
	}
	if !hasIntValue(row.CacheWrite1h, oneHour) {
		t.Errorf("row 1-hour cache write = %v, want %d", row.CacheWrite1h, oneHour)
	}
	if !hasIntValue(result.Totals.CacheWriteTokens, fiveMinute) {
		t.Errorf("totals 5-minute cache write = %v, want %d", result.Totals.CacheWriteTokens, fiveMinute)
	}
	if !hasIntValue(result.Totals.CacheWrite1hTokens, oneHour) {
		t.Errorf("totals 1-hour cache write = %v, want %d", result.Totals.CacheWrite1hTokens, oneHour)
	}
	// Disjointness: cache writes remain separate from cache reads and from
	// the already-uncached input; neither is subtracted nor double-counted.
	if !hasIntValue(row.CacheRead, 30) {
		t.Errorf("row cache read = %v, want 30", row.CacheRead)
	}
	if !hasIntValue(row.UncachedInput, 100) {
		t.Errorf("row uncached input = %v, want 100", row.UncachedInput)
	}
	if !hasIntValue(result.Totals.InputTokens, 100) {
		t.Errorf("totals input = %v, want 100", result.Totals.InputTokens)
	}
	if !hasIntValue(result.Totals.CacheReadTokens, 30) {
		t.Errorf("totals cache read = %v, want 30", result.Totals.CacheReadTokens)
	}

	// The JSON surface is what downstream accounting consumes; assert the
	// fields are actually present (not just zero-valued) there.
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Calls  []map[string]json.RawMessage `json:"calls"`
		Totals map[string]json.RawMessage   `json:"totals"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field string
		want  int
	}{
		{"cache_write_tokens", fiveMinute},
		{"cache_write_1h_tokens", oneHour},
	} {
		if raw, ok := document.Calls[0][tc.field]; !ok {
			t.Errorf("json call omitted %q: %s", tc.field, encoded)
		} else if got := rawInt(t, raw); got != tc.want {
			t.Errorf("json call %q = %d, want %d", tc.field, got, tc.want)
		}
		if raw, ok := document.Totals[tc.field]; !ok {
			t.Errorf("json totals omitted %q: %s", tc.field, encoded)
		} else if got := rawInt(t, raw); got != tc.want {
			t.Errorf("json totals %q = %d, want %d", tc.field, got, tc.want)
		}
	}
}

func rawInt(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var got int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return got
}
