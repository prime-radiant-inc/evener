package llm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/llm/apilog"
)

// BenchmarkAPILoggerSuccessfulRequest measures one logical request — one attempt
// record and the settlement that closes its group — with the real fsync in
// place, so the barrier cost is visible. Under the batched bound the settlement
// syncs once and the attempt record rides it, replacing one barrier per record.
func BenchmarkAPILoggerSuccessfulRequest(b *testing.B) {
	path := filepath.Join(b.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		b.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		attempt := standaloneCanonicalAttempt("ag_bench", 1)
		if err := logger.AppendAttempt(ctx, attempt); err != nil {
			b.Fatalf("AppendAttempt: %v", err)
		}
		settlement := apilog.APIAttemptGroupSettlement{
			Kind:              "attempt_group_settlement",
			SchemaVersion:     1,
			AttemptGroupID:    attempt.AttemptGroupID,
			FinalAttemptID:    attempt.AttemptID,
			FinalAttemptCount: 1,
			Outcome:           apilog.AttemptSuccess,
			SettledAt:         attempt.Timestamp,
		}
		if err := logger.AppendSettlement(ctx, settlement); err != nil {
			b.Fatalf("AppendSettlement: %v", err)
		}
	}
}

// BenchmarkAPILoggerSuccessfulRequestBarriers reports the fsync count for one
// logical request, which is the number this change moves. A benchmark is the
// honest measure here because the ruling permits losing the tail; what must not
// regress is that one request costs one barrier, not two.
func BenchmarkAPILoggerSuccessfulRequestBarriers(b *testing.B) {
	path := filepath.Join(b.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		b.Fatalf("NewAPILogger: %v", err)
	}
	defer logger.Close() //nolint:errcheck
	oldSync := apiLogFileSync
	b.Cleanup(func() { apiLogFileSync = oldSync })
	syncs := 0
	apiLogFileSync = func(*os.File) error {
		syncs++
		return nil
	}

	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		attempt := standaloneCanonicalAttempt("ag_bench_barriers", 1)
		if err := logger.AppendAttempt(ctx, attempt); err != nil {
			b.Fatalf("AppendAttempt: %v", err)
		}
		settlement := apilog.APIAttemptGroupSettlement{
			Kind:              "attempt_group_settlement",
			SchemaVersion:     1,
			AttemptGroupID:    attempt.AttemptGroupID,
			FinalAttemptID:    attempt.AttemptID,
			FinalAttemptCount: 1,
			Outcome:           apilog.AttemptSuccess,
			SettledAt:         attempt.Timestamp,
		}
		if err := logger.AppendSettlement(ctx, settlement); err != nil {
			b.Fatalf("AppendSettlement: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(syncs)/float64(b.N), "fsync/request")
}
