package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm/apilog"
)

// writePageBenchAPILog writes a synthetic API log of recordCount attempts with
// the body sizes a real session's records carry, which is what makes a
// whole-file decode expensive: request and response bodies of a few KB each.
func writePageBenchAPILog(b *testing.B, recordCount int) string {
	b.Helper()
	dir := b.TempDir()
	path := filepath.Join(dir, "sessions", "bench.api.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	request := apilog.EncodeBody([]byte(strings.Repeat("request payload byte content. ", 200)))
	response := apilog.EncodeBody([]byte(strings.Repeat("response payload byte content. ", 200)))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		b.Fatal(err)
	}
	for i := range recordCount {
		group := fmt.Sprintf("ag_page_%05d", i)
		attempt := apilog.APIAttemptRecord{
			Kind:             "api_attempt",
			SchemaVersion:    1,
			AttemptID:        identifier.MustNewAPIAttemptID(),
			AttemptGroupID:   group,
			AttemptIndex:     1,
			Timestamp:        time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
			LatencyMS:        int64(1000 + i%500),
			ProviderInstance: "openai-primary",
			RequestModel:     "gpt-5.2-codex",
			Request: apilog.APIAttemptRequest{
				Method:      "POST",
				Endpoint:    "https://provider.test/v1/responses",
				Body:        request,
				Model:       "gpt-5.2-codex",
				HistoryMode: "responses_delta",
			},
			Response: &apilog.APIAttemptResponse{
				StatusCode:    new(200),
				Body:          response,
				Model:         "gpt-5.2-codex",
				FinishReason:  "stop",
				TextLength:    new(40),
				ToolCallCount: new(1),
				Usage: apilog.Usage{
					InputTokens:     new(10000),
					OutputTokens:    new(200),
					TotalTokens:     new(10200),
					CacheReadTokens: new(9000),
				},
			},
			Outcome: apilog.AttemptSuccess,
		}
		settlement := apilog.APIAttemptGroupSettlement{
			Kind:              "attempt_group_settlement",
			SchemaVersion:     1,
			AttemptGroupID:    group,
			FinalAttemptID:    attempt.AttemptID,
			FinalAttemptCount: 1,
			Outcome:           apilog.AttemptSuccess,
			SettledAt:         attempt.Timestamp.Add(time.Second),
		}
		for _, record := range []apilog.APILogRecord{attempt, settlement} {
			line, err := json.Marshal(record)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := f.Write(append(line, '\n')); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkReadAPILogFirstPage measures the paged API-log read the diagnostic
// surfaces use (read_transcript source=api_log): one page rendered out of a
// store whose every record carries multi-KB bodies. The records=200 sub-case
// is the growth check -- a page read must not get more expensive just because
// the store holds more records.
func BenchmarkReadAPILogFirstPage(b *testing.B) {
	for _, recordCount := range []int{200, 2000} {
		path := writePageBenchAPILog(b, recordCount)
		for _, rangeArg := range []string{"start:20", "last:20"} {
			b.Run(fmt.Sprintf("records=%d/range=%s", recordCount, rangeArg), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					envelope, err := readAPILogSummary(context.Background(), path, "", "local:bench", rangeArg)
					if err != nil {
						b.Fatal(err)
					}
					if got := len(envelope.(apiLogReadEnvelope).Records); got != defaultAPILogRecords {
						b.Fatalf("page records = %d, want %d", got, defaultAPILogRecords)
					}
				}
			})
		}
	}
}
