package llm

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
	apilog "primeradiant.com/evener/llm/apilog"
)

// TestBuildAPIAttemptRecordPreservesDisjointCacheWriteTiers pins issue #946:
// llm.Usage carries two disjoint cache-write tiers (5-minute CacheWriteTokens
// and 1-hour CacheWrite1hTokens); both must survive canonical normalization
// into apilog.Usage. They must remain separate fields -- neither folded into
// the other, into cache reads, nor into uncached input.
func TestBuildAPIAttemptRecordPreservesDisjointCacheWriteTiers(t *testing.T) {
	fiveMinute, oneHour := 44, 17
	startedAt := time.Unix(30, 0).UTC()
	record := buildAPIAttemptRecord("ag_cache_write_tiers", identifier.MustNewAPIAttemptID(), 1, APIAttemptMeta{
		ProviderInstance: "anthropic-primary",
		RequestModel:     "claude-test",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.test/v1/messages",
		RequestBody:      []byte("{}"),
		StartedAt:        startedAt,
	}, APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte("{}"),
		Response: &Response{
			Model:  "claude-test",
			Finish: FinishReason{Reason: FinishReasonStop},
			Usage: Usage{
				InputTokens:        100,
				OutputTokens:       20,
				TotalTokens:        134,
				CacheReadTokens:    new(30),
				CacheWriteTokens:   &fiveMinute,
				CacheWrite1hTokens: &oneHour,
			},
		},
		Outcome:    apilog.AttemptSuccess,
		FinishedAt: startedAt.Add(time.Millisecond),
	})

	encoded, err := apilog.MarshalRecord(record)
	if err != nil {
		t.Fatalf("MarshalRecord(): %v", err)
	}
	var doc struct {
		Response struct {
			Usage map[string]json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatalf("unmarshal canonical record: %v", err)
	}
	for field, want := range map[string]int{
		"cache_read_tokens":     30,
		"input_tokens":          100,
		"cache_write_tokens":    fiveMinute,
		"cache_write_1h_tokens": oneHour,
	} {
		raw, ok := doc.Response.Usage[field]
		if !ok {
			t.Errorf("canonical usage omitted %q: %s", field, encoded)
			continue
		}
		var got int
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("canonical usage field %q: %v", field, err)
			continue
		}
		if got != want {
			t.Errorf("canonical usage %q = %d, want %d", field, got, want)
		}
	}
}
