package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	toolpkg "primeradiant.com/serf/agent/internal/tool"
)

func TestStableDelegateTools_CreateSendStopStatusUseDelegateID(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	for _, tc := range []struct {
		name      string
		parameter string
	}{
		{name: "delegate_send", parameter: "to"},
		{name: "job_stop", parameter: "target"},
		{name: "job_status", parameter: "target"},
	} {
		registered := s.reg.Get(tc.name)
		if registered == nil {
			t.Fatalf("registered %s tool is absent", tc.name)
		}
		properties, ok := registered.Tool.Definition.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("registered %s properties = %T", tc.name, registered.Tool.Definition.Parameters["properties"])
		}
		if _, ok := properties[tc.parameter]; !ok {
			t.Errorf("registered %s omits stable identifier parameter %q: %#v", tc.name, tc.parameter, properties)
		}
		if tc.parameter == "target" {
			if _, legacy := properties["job_id"]; legacy {
				t.Errorf("registered %s still exposes activation-only job_id", tc.name)
			}
		}
	}
	create := s.reg.Get("delegate")
	properties := create.Tool.Definition.Parameters["properties"].(map[string]any)
	if _, exists := properties["max_wait_ms"]; exists {
		t.Fatal("delegate creation exposes max_wait_ms")
	}
	wire, err := marshalStableDelegateCreateResult(stableDelegateCreateResult{
		DelegateID: "dlg_stable", ChildSessionID: "ses_child", Type: "delegate", Status: "running", TranscriptRef: "local:ses_child",
	}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(wire), &result); err != nil {
		t.Fatal(err)
	}
	if result["delegate_id"] != "dlg_stable" {
		t.Fatalf("delegate result identity = %#v", result)
	}
	for _, legacy := range []string{"job_id", "started_job_id", "latest_job_id", "activation_job_id"} {
		if _, exists := result[legacy]; exists {
			t.Fatalf("delegate result retains activation alias %q: %#v", legacy, result)
		}
	}
}

func TestStableDelegateTools_StatusReadsMetadataWithoutPacketOrAck(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	id := "dlg_status_metadata"
	seedStableToolDelegate(t, s, id, "", time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC())
	before, err := s.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeAggregate := delegateAggregateSnapshot(t, s.delegateController, id)
	if len(beforeAggregate.PendingDeliveries) != 1 {
		t.Fatalf("pending deliveries before status = %d, want 1", len(beforeAggregate.PendingDeliveries))
	}

	value, err := jobStatusTool(s, map[string]any{"target": id}, 1<<20)
	if err != nil {
		t.Fatalf("job_status stable delegate: %v", err)
	}
	state := stableToolStateMap(t, value)
	if state["id"] != id || state["type"] != "delegate" || state["status"] != "idle" {
		t.Fatalf("stable status metadata = %#v", state)
	}
	encoded := string(handlerJSON(t, value))
	if strings.Contains(encoded, "packet-only-secret") || strings.Contains(encoded, "structured_result") {
		t.Fatalf("metadata-only status leaked terminal packet: %s", encoded)
	}
	afterAggregate := delegateAggregateSnapshot(t, s.delegateController, id)
	if len(afterAggregate.PendingDeliveries) != 1 || afterAggregate.PendingDeliveries[0].DeliveryID != beforeAggregate.PendingDeliveries[0].DeliveryID {
		t.Fatalf("status acknowledged terminal delivery: before=%#v after=%#v", beforeAggregate.PendingDeliveries, afterAggregate.PendingDeliveries)
	}
	after, err := s.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("metadata-only status mutated the delegate journal")
	}
}

func TestStableDelegateTools_StatusRejectsActivationAlias(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	started := time.Unix(10, 0).UTC()
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, JobID: "job_legacy_activation", Type: jobstore.JobType(delegateResourceType),
		OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobStatusTool(s, map[string]any{"target": "job_legacy_activation"}, 1<<20); err == nil || !strings.Contains(err.Error(), "legacy_delegate_activation") {
		t.Fatalf("job_status legacy activation error = %v, want typed fail-closed rejection", err)
	}
}

func TestStableDelegateTools_StopIgnoresIncludeChildrenAndRemainsRecursive(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	c := s.delegateController
	seedStableToolRunningDelegate(t, s, "dlg_stop_parent", "", time.Unix(10, 0).UTC())
	seedStableToolRunningDelegate(t, s, "dlg_stop_child", "dlg_stop_parent", time.Unix(11, 0).UTC())
	var parentCancelled atomic.Int32
	var childCancelled atomic.Int32
	c.mu.Lock()
	c.live["dlg_stop_parent"].binding.cancel = func() { parentCancelled.Add(1) }
	c.live["dlg_stop_child"].binding.cancel = func() { childCancelled.Add(1) }
	c.mu.Unlock()

	value, err := jobStopTool(context.Background(), s, map[string]any{
		"target":           "dlg_stop_parent",
		"include_children": false,
	}, 1<<20)
	if err != nil {
		t.Fatalf("job_stop stable delegate: %v", err)
	}
	state := stableToolStateMap(t, value)
	if state["id"] != "dlg_stop_parent" || state["type"] != "delegate" {
		t.Fatalf("stable stop result = %#v", state)
	}
	if parentCancelled.Load() != 1 || childCancelled.Load() != 1 {
		t.Fatalf("recursive cancellation = parent:%d child:%d", parentCancelled.Load(), childCancelled.Load())
	}
	c.mu.Lock()
	stop := c.stop
	_, childCovered := stop.members["dlg_stop_child"]
	c.mu.Unlock()
	if !childCovered {
		t.Fatal("include_children=false excluded stable descendant from stop")
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_stop_child", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish stopped child: %v", err)
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_stop_parent", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish stopped parent: %v", err)
	}
	<-stop.done
}

func TestStableDelegateTools_WaitIgnoredReasonIsOwnField(t *testing.T) {
	value, err := marshalDelegateSendResult(sendMessageResult{
		DelegateID: "dlg_live", Type: "delegate", Status: jobstore.StatusRunning, Action: "steered",
		WaitIgnoredReason: "live steer returns on delivery",
	}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	state := stableToolStateMap(t, value)
	if state["wait_ignored_reason"] != "live steer returns on delivery" {
		t.Fatalf("delegate_send state = %#v", state)
	}
	if _, exists := state["warnings"]; exists {
		t.Fatalf("call-scoped wait reason leaked into warnings: %#v", state)
	}
}

func TestStableDelegateTools_ListUnifiesShellAndDelegateCandidates(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	seedStableToolDelegate(t, s, "dlg_list", "", time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC())
	seedStableToolShell(t, s.jobManager, "job_shell", time.Unix(30, 0).UTC(), jobstore.StatusRunning)

	result := stableToolListResult(t, s, map[string]any{})
	if got, want := stableToolItemIDs(result.Items), []string{"job_shell", "dlg_list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unified list IDs = %v, want %v; result=%#v", got, want, result)
	}
	if result.Items[0].Type != "shell" || result.Items[1].Type != "delegate" {
		t.Fatalf("unified list types = %#v", result.Items)
	}
}

func TestStableDelegateTools_ListPreservesTypeStatusAndVisibilityFilters(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	seedStableToolDelegate(t, s, "dlg_direct_idle", "", time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC())
	seedStableToolRunningDelegate(t, s, "dlg_direct_running", "", time.Unix(30, 0).UTC())
	seedStableToolDelegate(t, s, "dlg_descendant", "dlg_direct_idle", time.Unix(40, 0).UTC(), time.Unix(50, 0).UTC())
	seedStableToolShell(t, s.jobManager, "job_visible", time.Unix(60, 0).UTC(), jobstore.StatusRunning)

	filtered := stableToolListResult(t, s, map[string]any{
		"type":   []any{"delegate"},
		"status": []any{"idle"},
	})
	if got := stableToolItemIDs(filtered.Items); !reflect.DeepEqual(got, []string{"dlg_direct_idle"}) {
		t.Fatalf("direct filtered items = %v", got)
	}
	all := stableToolListResult(t, s, map[string]any{
		"type":                []any{"delegate"},
		"include_descendants": true,
	})
	if got, want := stableToolItemIDs(all.Items), []string{"dlg_descendant", "dlg_direct_running", "dlg_direct_idle"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descendant-visible items = %v, want %v", got, want)
	}
	if _, err := s.delegateController.FinishGeneration(delegateLease{delegateID: "dlg_direct_running", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish running filter fixture: %v", err)
	}
}

func TestStableDelegateTools_ListOwnerWinsDedupeAndSortsBeforePaging(t *testing.T) {
	tree := newStableDelegateShellTree(t)
	rec := createStableDelegateShell(t, tree.childJM, "owner copy wins")
	result := stableToolListResult(t, tree.root, map[string]any{
		"type":                []any{"shell"},
		"include_descendants": true,
		"limit":               1,
	})
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("deduped page = %#v", result)
	}
	item := result.Items[0]
	if item.ID != rec.JobID || item.OwnerSessionID != tree.child.ID() || item.Depth != 1 {
		t.Fatalf("owner-authoritative item = %#v, want child-owned depth-1 %s", item, rec.JobID)
	}
}

func TestStableDelegateTools_ListPreservesOffsetLimitCountTotal(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	seedStableToolDelegate(t, s, "dlg_page_1", "", time.Unix(10, 0).UTC(), time.Unix(20, 0).UTC())
	seedStableToolDelegate(t, s, "dlg_page_2", "", time.Unix(30, 0).UTC(), time.Unix(40, 0).UTC())
	seedStableToolShell(t, s.jobManager, "job_page_1", time.Unix(50, 0).UTC(), jobstore.StatusRunning)
	seedStableToolShell(t, s.jobManager, "job_page_2", time.Unix(60, 0).UTC(), jobstore.StatusRunning)

	result := stableToolListResult(t, s, map[string]any{"offset": 1, "limit": 2})
	if result.Offset != 1 || result.Count != 2 || result.Total != 4 {
		t.Fatalf("paging metadata = offset:%d count:%d total:%d", result.Offset, result.Count, result.Total)
	}
	if got, want := stableToolItemIDs(result.Items), []string{"job_page_1", "dlg_page_2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paged IDs = %v, want %v", got, want)
	}
}

func TestStableDelegateTools_ListPreservesTurnSlotsAllowanceAndWatchDiagnostics(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	s.treeCounter = newTreeCounter(7)
	s.driveCounter = newTreeCounter(3)
	if !s.treeCounter.reserve(slotKindJob) || !s.driveCounter.reserve(slotKindDrive) {
		t.Fatal("reserve diagnostic slots")
	}
	t.Cleanup(func() {
		s.treeCounter.releaseKind(slotKindJob)
		s.driveCounter.releaseKind(slotKindDrive)
	})
	s.mu.Lock()
	s.delegationAllowance = 4
	s.mu.Unlock()
	if _, err := jobWatchTool(s, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"assistant.tool"},
	}, 1<<20); err != nil {
		t.Fatalf("install watch: %v", err)
	}

	result := stableToolListResult(t, s, map[string]any{})
	if result.TurnSlots == nil || result.TurnSlots.Jobs != 1 || result.TurnSlots.Drives != 1 || result.TurnSlots.Cap != 7 {
		t.Fatalf("turn slots = %#v", result.TurnSlots)
	}
	if result.DelegationAllowance != 4 || len(result.Watches) != 1 {
		t.Fatalf("allowance/watch diagnostics = allowance:%d watches:%#v", result.DelegationAllowance, result.Watches)
	}
}

type stableToolListWire struct {
	Items []struct {
		ID             string `json:"id"`
		Type           string `json:"type"`
		Status         string `json:"status"`
		OwnerSessionID string `json:"owner_session_id"`
		Depth          int    `json:"depth"`
	} `json:"items"`
	Count               int                `json:"count"`
	Offset              int                `json:"offset"`
	Total               int                `json:"total"`
	TurnSlots           *turnSlotOccupancy `json:"turn_slots"`
	DelegationAllowance int                `json:"delegation_allowance"`
	Watches             []watchListEntry   `json:"watches"`
}

func stableToolListResult(t *testing.T, s *Session, args map[string]any) stableToolListWire {
	t.Helper()
	value, err := jobListTool(s, args, 1<<20)
	if err != nil {
		t.Fatalf("job_list: %v", err)
	}
	var result stableToolListWire
	if err := json.Unmarshal(handlerJSON(t, value), &result); err != nil {
		t.Fatalf("decode job_list state: %v", err)
	}
	return result
}

func stableToolItemIDs(items []struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	OwnerSessionID string `json:"owner_session_id"`
	Depth          int    `json:"depth"`
}) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func stableToolStateMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("tool result = %T, want tool.StateResult", value)
	}
	raw, err := json.Marshal(result.State)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func seedStableToolDelegate(t *testing.T, s *Session, id, parentID string, startedAt, endedAt time.Time) {
	t.Helper()
	descriptor := stableToolDescriptor(s, id, parentID)
	packet := delegatestore.TerminalPacket{
		Kind:             delegatestore.PacketReported,
		Message:          json.RawMessage(`"packet-only-secret"`),
		StructuredResult: json.RawMessage(`null`),
	}
	s.delegateController.mu.Lock()
	_, err := s.delegateController.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, startedAt),
		delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 1}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", endedAt, delegateDeliveryID(id, 1), &packet),
	)
	s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatalf("seed stable delegate %s: %v", id, err)
	}
}

func seedStableToolRunningDelegate(t *testing.T, s *Session, id, parentID string, startedAt time.Time) {
	t.Helper()
	descriptor := stableToolDescriptor(s, id, parentID)
	c := s.delegateController
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, startedAt),
	)
	if err == nil {
		c.live[id] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease: delegateLease{delegateID: id, generation: 1}, cancel: func() {}, ready: true,
		}}
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed running stable delegate %s: %v", id, err)
	}
	t.Cleanup(func() {
		c.mu.Lock()
		aggregate := c.durable[id]
		open := aggregate != nil && aggregate.CurrentRunOpen
		c.mu.Unlock()
		if open {
			_, _ = c.FinishGeneration(delegateLease{delegateID: id, generation: 1}, delegateFinish{})
		}
	})
}

func stableToolDescriptor(s *Session, id, parentID string) delegatestore.Descriptor {
	ownerSessionID := s.ID()
	if parentID != "" {
		ownerSessionID = "child-" + parentID
	}
	return delegatestore.Descriptor{
		ChildSessionID: "child-" + id, TranscriptRef: "local:child-" + id,
		ParentDelegateID: parentID, OwnerSessionID: ownerSessionID, VisibleSessionID: s.ID(),
		Task: "task " + id, Description: "description " + id, AgentType: "general",
		ResolvedModel: "gpt-5.2", ToolNameCeiling: []string{"communicate"}, Resumable: true,
	}
}

func seedStableToolShell(t *testing.T, jm *jobManager, id string, startedAt time.Time, status jobstore.Status) {
	t.Helper()
	if err := jm.appendEvent(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: startedAt, JobID: id, Type: jobstore.JobShell,
		OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, Command: "echo stable", Description: "stable shell", StartedAt: &startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if status != jobstore.StatusRunning {
		endedAt := startedAt.Add(time.Second)
		if err := jm.appendEvent(jobstore.Event{
			Kind: jobstore.EventJobFinished, TS: endedAt, JobID: id, Status: status, EndedAt: &endedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
}
