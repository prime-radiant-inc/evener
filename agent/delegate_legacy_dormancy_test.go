package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// Legacy-retirement allowlist:
//   - this file and delegate_resource_bootstrap_test.go may construct literal
//     legacy delegate lifecycle/watch rows only to prove provider-free,
//     read-only fail-closed behavior;
//   - session_outline.go, transcript_render_job_test.go, and
//     delegate_resource_readonly_test.go may decode historical activation IDs
//     only to keep old transcripts readable;
//   - doctor stable-readonly fixtures may retain the same literal rows only to
//     prove explicit legacy diagnostics;
//   - the Task 8 watch journal and stable controller retain typed source and
//     receiver delegate identities. Receivers are derived from the watching
//     session at registration; these fields are not a public receiver input,
//     delegate JobRecord, or alternate delivery authority.
//
// None of those fixtures is a live control alias, reducer branch, migration,
// compatibility path, or writable authority.

func TestDelegateLegacyDormancy_NoDelegateJobRecordCanBeCreated(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return errors.New("stop after stable commit")
		}
		return nil
	}

	call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
		ID:        "legacy-dormancy-create",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"prove stable-only creation"}`),
	})
	if call.IsError {
		t.Fatalf("registered delegate returned transport error: %s", call.Output)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(toolResultJSON(call), &result); err != nil {
		t.Fatalf("decode registered delegate result: %v", err)
	}
	var delegateID string
	if err := json.Unmarshal(result["delegate_id"], &delegateID); err != nil || !strings.HasPrefix(delegateID, "dlg_") {
		t.Fatalf("registered stable identity = %q, err %v", delegateID, err)
	}
	for _, field := range []string{"job_id", "started_job_id", "latest_job_id", "activation_job_id"} {
		if _, exists := result[field]; exists {
			t.Fatalf("registered delegate result exposed %s: %s", field, toolResultJSON(call))
		}
	}
	records, err := root.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load shell job store: %v", err)
	}
	for id, record := range records {
		if record != nil && string(record.Type) == "delegate" {
			t.Fatalf("registered stable create produced delegate JobRecord %q: %+v", id, record)
		}
	}
}

func TestDelegateLegacyDormancy_NoActivationAliasResolvesForLiveControl(t *testing.T) {
	root := newSession(t, withoutGitSnapshot())
	const delegateID = "dlg_live_without_activation"
	seedStableToolRunningDelegate(t, root, delegateID, "", time.Unix(10, 0).UTC())
	var cancelled atomic.Int32
	root.delegateController.mu.Lock()
	root.delegateController.live[delegateID].binding.cancel = func() { cancelled.Add(1) }
	root.delegateController.mu.Unlock()

	for _, toolCall := range []llm.ToolCallData{
		{ID: "legacy-status", Name: "job_status", Arguments: json.RawMessage(`{"target":"job_legacy_activation"}`)},
		{ID: "legacy-stop", Name: "job_stop", Arguments: json.RawMessage(`{"target":"job_legacy_activation"}`)},
	} {
		result := root.reg.ExecuteCall(context.Background(), root.currentEnv(), toolCall)
		if !result.IsError {
			t.Fatalf("registered %s resolved retired activation alias: %s", toolCall.Name, result.Output)
		}
	}
	if cancelled.Load() != 0 {
		t.Fatalf("retired activation alias cancelled live stable delegate %d times", cancelled.Load())
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, delegateID)
	if !aggregate.CurrentRunOpen {
		t.Fatalf("retired activation alias changed stable aggregate: %+v", aggregate)
	}
}

func TestDelegateLegacyDormancy_StableWatchJournalStillDelivers(t *testing.T) {
	TestStableDelegateWatch_ReceiverFsyncPrecedesDeliveredAck(t)
}

func TestDelegateLegacyDormancy_QuietSupervisionStillDelivers(t *testing.T) {
	TestDelegateResourceSupervision_QuietWatchdogUsesTenMinuteThresholdAndThirtySecondChecks(t)
}

func TestDelegateLegacyDormancy_RetentionStillReclaimsOnAdmission(t *testing.T) {
	TestDelegateRuntimeReclaim_CreateAndColdRestoreTriggerReclamation(t)
}

func TestDelegateLegacyDormancy_ShellParentDelegateIDStillRoutes(t *testing.T) {
	TestStableDelegateShell_ParentDelegateIDReplacesSyntheticParentJob(t)
	TestStableDelegateShell_OutputStatusWatchAndStopRemainJobAddressed(t)
}

func TestDelegateLegacyDormancy_HistoricalSendStillRendersReadOnly(t *testing.T) {
	TestStableDelegateReadOnly_HistoricalSendRendersWithoutLiveAlias(t)
}

func TestDelegateLegacyDormancy_LegacyLifecycleAndWatchRowsFailClosed(t *testing.T) {
	TestDelegateResourceBootstrap_LegacyDelegateStateFailsClosed(t)
	TestDelegateResourceBootstrap_LegacyDelegateWatchStateFailsClosed(t)
}
