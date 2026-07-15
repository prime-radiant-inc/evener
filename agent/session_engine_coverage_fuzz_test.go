//go:build serffuzz

package agent

import (
	"context"
	"testing"
)

// FuzzSessionEngineCoverage replays deterministic, offline branch tests through
// a native fuzz target so their session-engine statements participate in the
// strict tagged fuzz union. The byte selects ordering only; every seed executes
// the complete bounded program.
func FuzzSessionEngineCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Add(byte(1))
	f.Fuzz(func(t *testing.T, reverse byte) {
		tests := []struct {
			name string
			fn   func(*testing.T)
		}{
			{"accessors", TestS2Cov_SessionAccessors},
			{"closed_mutators", TestS2Cov_MutatorsAfterClose},
			{"model_usage", TestS2Cov_RecordResponseUsage},
			{"context_warning", TestS2Cov_MaybeWarnContextUsage},
			{"prepare_request", TestS2Cov_PrepareModelRequest_RepairsOrphan},
			{"continuation_recovery", TestFallbackChain_ContinuationRejectionRetriesFullHistoryBeforeModelFallback},
			{"continuation_fallback", TestFallbackChain_ContinuationRecoveryFailureThenModelFallback},
			{"non_continuation_error", TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry},
			{"stream_json", TestS2Cov_PartialJSONStringField},
			{"stream_unicode", TestS2Cov_UnquoteJSONUnicodeEscape},
			{"persist_vision", TestS2Cov_PersistToolResults_VisionSteering},
			{"strategy_error", TestS2Cov_NotifyStrategyAfterAction_WarnsOnError},
			{"continuation_input", TestS2Cov_AcceptContinuationInput_AppendsSteeringAndMarker},
			{"queue_preview", TestS2Cov_QueuedEntryPreviewLine},
			{"job_notifications", TestS2Cov_JobNotifications},
			{"compaction_turn", TestS2Cov_HandleCompactionTurn_WritesTranscriptAndEmitsEvent},
			{"restore_guards", TestS2Cov_RestoreSession_NilArgGuards},
			{"namer_guards", TestS2Cov_SessionNamerModelGuards},
			{"name_sanitize", TestS2Cov_SanitizeSessionName},
			{"name_trim", TestS2Cov_TrimForSessionNamer},
			{"name_source", TestS2Cov_SessionNameSourceLabel},
			{"warning_hook", TestS2Cov_WarningHookMessage},
			{"drain_cancel", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := (&Session{}).DrainAsSteer(ctx); err != context.Canceled {
					t.Fatalf("DrainAsSteer cancellation = %v, want context.Canceled", err)
				}
			}},
		}
		for i := range tests {
			j := i
			if reverse&1 != 0 {
				j = len(tests) - 1 - i
			}
			t.Run(tests[j].name, tests[j].fn)
		}
	})
}
