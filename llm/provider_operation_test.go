package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	apilog "primeradiant.com/serf/llm/apilog"
)

func TestProviderOperationSettlementUsesBegunAttemptState(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_provider_operation_begun")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	operation := &providerOperation{group: group, ownsGroup: true}
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	finalErr := errors.New("provider rejected request")

	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptProviderReject, finalErr))
	operation.settle(ctx, finalErr)

	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 1 || len(settlements) != 1 {
		t.Fatalf("attempts/settlements = %d/%d, want 1/1", len(attempts), len(settlements))
	}
	if settlements[0].FinalAttemptID != attempts[0].AttemptID || settlements[0].FinalAttemptCount != 1 {
		t.Fatalf("settlement = %+v, want begun attempt %+v", settlements[0], attempts[0])
	}
}
