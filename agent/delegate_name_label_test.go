package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// Non-worktree delegate names are display-only labels: the create result, the
// durable descriptor, job_list, and job_status all surface them, while every
// addressing surface stays keyed to the delegate id.

func TestDelegateName_LabelCarriesToCreateResultAndListings(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)

	createOut, err := stableDelegateCreateTool(context.Background(), root, map[string]any{
		"prompt": "labeled unit of work",
		"name":   "research-label",
	}, 1<<20)
	if err != nil {
		t.Fatalf("stableDelegateCreateTool: %v", err)
	}
	var create stableDelegateCreateResult
	if err := json.Unmarshal([]byte(createOut), &create); err != nil {
		t.Fatalf("create result JSON: %v\n%s", err, createOut)
	}
	if create.DelegateID == "" {
		t.Fatalf("create result has no delegate id: %s", createOut)
	}
	if create.Name != "research-label" {
		t.Fatalf("create result name = %q, want research-label", create.Name)
	}

	// The durable descriptor carries the label, and the label cut no branch.
	root.delegateController.mu.Lock()
	descriptor := root.delegateController.durable[create.DelegateID].Descriptor
	root.delegateController.mu.Unlock()
	if descriptor.Name != "research-label" {
		t.Fatalf("durable descriptor name = %q, want research-label", descriptor.Name)
	}
	if descriptor.WorktreeBranch != "" {
		t.Fatalf("non-worktree descriptor branch = %q, want empty", descriptor.WorktreeBranch)
	}

	// job_list rows surface the label, in state and in the rendered line.
	listed := jobListState(t, root, nil)
	var row *jobListEntry
	for i := range listed.Items {
		if listed.Items[i].ID == create.DelegateID {
			row = &listed.Items[i]
		}
	}
	if row == nil {
		t.Fatalf("job_list omitted the named delegate: %#v", listed.Items)
	}
	if row.Name != "research-label" {
		t.Fatalf("job_list row name = %q, want research-label", row.Name)
	}
	if rendered := formatJobList(listed); !strings.Contains(rendered, "research-label") {
		t.Fatalf("job_list render omits the label:\n%s", rendered)
	}

	// job_status for the delegate surfaces the label too.
	statusValue, err := stableDelegateStatusTool(root, create.DelegateID, 1<<20)
	if err != nil {
		t.Fatalf("stableDelegateStatusTool: %v", err)
	}
	statusResult, ok := statusValue.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("job_status result = %T, want StateResult", statusValue)
	}
	status, ok := statusResult.State.(stableDelegateStatusResult)
	if !ok {
		t.Fatalf("job_status state = %T, want stableDelegateStatusResult", statusResult.State)
	}
	if status.Name != "research-label" {
		t.Fatalf("job_status name = %q, want research-label", status.Name)
	}
}

func TestDelegateName_AbsentLabelRendersAsAbsent(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)

	createOut, err := stableDelegateCreateTool(context.Background(), root, map[string]any{
		"prompt": "unnamed unit of work",
	}, 1<<20)
	if err != nil {
		t.Fatalf("stableDelegateCreateTool: %v", err)
	}
	if strings.Contains(createOut, `"name"`) {
		t.Fatalf("unnamed create result carries a name field: %s", createOut)
	}
	listed := jobListState(t, root, nil)
	if len(listed.Items) != 1 || listed.Items[0].Name != "" {
		t.Fatalf("unnamed job_list rows = %#v, want one row with an empty name", listed.Items)
	}
}

// A named delegate's completion notification frame carries the label next to
// the delegate id, so the parent reads which named unit finished; an unnamed
// delegate's frame carries no name attribute.
func TestDelegateName_DeliveryPlanAndNotificationFrameCarryLabel(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	created := delegateControllerCreatedEvent("dlg_target", "")
	created.Created.Descriptor.Name = "notify-label"
	c.mu.Lock()
	_, err := c.appendLocked(created)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed named delegate: %v", err)
	}
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "first")
	if len(plans.deliveries) != 1 {
		t.Fatalf("finish produced %d delivery plans, want 1", len(plans.deliveries))
	}
	plan := plans.deliveries[0]
	if plan.name != "notify-label" {
		t.Fatalf("delivery plan name = %q, want notify-label", plan.name)
	}
	content, err := delegateNotificationContent(plan)
	if err != nil {
		t.Fatalf("delegateNotificationContent: %v", err)
	}
	if !strings.Contains(content, `name="notify-label"`) || !strings.Contains(content, `delegate_id="dlg_target"`) {
		t.Fatalf("notification frame omits the label or the id:\n%s", content)
	}

	// The retry constructor (retryDeliveryPlanLocked) rebuilds the plan from
	// the same durable aggregate, so a retried frame carries the label too.
	receipt := &delegateDeliveryAdmission{
		token:      delegateDeliveryToken{deliveryID: plan.deliveryID},
		delegateID: "dlg_target",
		claim:      plan.claim,
		retryable:  true,
	}
	c.mu.Lock()
	retryPlan := c.retryDeliveryPlanLocked(receipt)
	c.mu.Unlock()
	if retryPlan == nil {
		t.Fatal("retryDeliveryPlanLocked returned no plan for a retryable named delegate")
	}
	if retryPlan.name != "notify-label" {
		t.Fatalf("retry plan name = %q, want notify-label", retryPlan.name)
	}

	plain, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, plain, "dlg_plain", "")
	plainLease, _ := startDelegateDeliveryGeneration(t, plain, "dlg_plain", false)
	plainPlans := finishDelegateDeliveryGeneration(t, plain, plainLease, "second")
	plainContent, err := delegateNotificationContent(plainPlans.deliveries[0])
	if err != nil {
		t.Fatalf("delegateNotificationContent: %v", err)
	}
	if strings.Contains(plainContent, `name=`) {
		t.Fatalf("unnamed notification frame carries a name attribute:\n%s", plainContent)
	}
}

// The send/wait result family surfaces the delegate label the same way the
// notification frame does: a named delegate's reply carries name, an unnamed
// delegate's reply omits it. The label flows through the terminal metadata
// packet (delegateTerminalMetadataFromRun → stableDelegateFinishFromRun) into
// the send result (populateStableDelegateSendResult → marshalDelegateSendResult).

// marshalDelegateSendResultWire marshals a send result and returns its wire
// JSON, following the file's existing helper convention.
func marshalDelegateSendResultWire(t *testing.T, result sendMessageResult) string {
	t.Helper()
	value, err := marshalDelegateSendResult(result, 0)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult: %v", err)
	}
	sr, ok := value.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("marshalDelegateSendResult returned %T, want StateResult", value)
	}
	wire, err := json.Marshal(sr.State)
	if err != nil {
		t.Fatalf("marshal send result state: %v", err)
	}
	return string(wire)
}

func TestDelegateName_TerminalMetadataCarriesLabel(t *testing.T) {
	t.Parallel()
	// A named delegate's terminal metadata includes the label from the descriptor.
	named := delegateTerminalMetadataFromRun(delegateTerminalRunInputs{
		descriptor: delegatestore.Descriptor{Name: "send-label", Task: "task"},
	})
	if named.Name != "send-label" {
		t.Fatalf("named metadata name = %q, want send-label", named.Name)
	}
	// An unnamed delegate's terminal metadata omits the label (zero value).
	unnamed := delegateTerminalMetadataFromRun(delegateTerminalRunInputs{
		descriptor: delegatestore.Descriptor{Task: "task"},
	})
	if unnamed.Name != "" {
		t.Fatalf("unnamed metadata name = %q, want empty", unnamed.Name)
	}
}

func TestDelegateName_FinishPacketMetadataCarriesLabel(t *testing.T) {
	t.Parallel()
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result:       "completed result",
		communicated: true,
		descriptor:   delegatestore.Descriptor{Name: "finish-label", Task: "task"},
	})
	if finish.packet == nil {
		t.Fatal("finish produced no packet")
	}
	metadata := decodeDelegatePacketMetadata(t, *finish.packet)
	if got := metadata["name"]; got != "finish-label" {
		t.Fatalf("packet metadata name = %v, want finish-label", got)
	}
}

func TestDelegateName_PopulateSendResultCarriesLabel(t *testing.T) {
	t.Parallel()
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Name: "populate-label",
		Task: "task",
	})
	packet := delegatestore.TerminalPacket{
		Kind:     delegatestore.PacketReported,
		Metadata: metadata,
	}
	var result sendMessageResult
	populateStableDelegateSendResult(&result, packet)
	if result.Name != "populate-label" {
		t.Fatalf("send result name = %q, want populate-label", result.Name)
	}
}

func TestDelegateName_PopulateSendResultOmitsAbsentLabel(t *testing.T) {
	t.Parallel()
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Task: "task",
	})
	packet := delegatestore.TerminalPacket{
		Kind:     delegatestore.PacketReported,
		Metadata: metadata,
	}
	var result sendMessageResult
	populateStableDelegateSendResult(&result, packet)
	if result.Name != "" {
		t.Fatalf("unnamed send result name = %q, want empty", result.Name)
	}
}

func TestDelegateName_MarshalSendResultCarriesLabel(t *testing.T) {
	t.Parallel()
	wire := marshalDelegateSendResultWire(t, sendMessageResult{
		DelegateID: "dlg_named",
		Name:       "marshal-label",
		Action:     "completed",
	})
	if !strings.Contains(wire, `"name":"marshal-label"`) {
		t.Fatalf("send result JSON omits the label:\n%s", wire)
	}
}

func TestDelegateName_MarshalSendResultOmitsAbsentLabel(t *testing.T) {
	t.Parallel()
	wire := marshalDelegateSendResultWire(t, sendMessageResult{
		DelegateID: "dlg_unnamed",
		Action:     "completed",
	})
	if strings.Contains(wire, `"name"`) {
		t.Fatalf("unnamed send result JSON carries a name field:\n%s", wire)
	}
}

// TestDelegateName_FullChainSendReplyCarriesLabel drives the full chain:
// stableDelegateFinishFromRun builds the packet with metadata that carries
// the label, populateStableDelegateSendResult extracts it, and
// marshalDelegateSendResult renders it.
func TestDelegateName_FullChainSendReplyCarriesLabel(t *testing.T) {
	t.Parallel()
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result:       "chain result",
		communicated: true,
		descriptor:   delegatestore.Descriptor{Name: "chain-label", Task: "chain task"},
	})
	var result sendMessageResult
	populateStableDelegateSendResult(&result, *finish.packet)
	wire := marshalDelegateSendResultWire(t, result)
	if !strings.Contains(wire, `"name":"chain-label"`) {
		t.Fatalf("full-chain send result JSON omits the label:\n%s", wire)
	}
}

// TestDelegateName_SteeredReplyCarriesLabel verifies that a delegate_send to an
// already-running named delegate (the steered path, maxWaitMS == 0) includes the
// name in the reply. The steered return at the send path built a sendMessageResult
// without Name, so a delegate_send to a running named delegate omitted the label.
func TestDelegateName_SteeredReplyCarriesLabel(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(d *delegatestore.Descriptor) {
		d.Name = "steer-label"
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return finalResponse("first result")
		},
		func(llm.Request) llm.Response { return finalResponse("continued") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "new steering", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer = %#v", steered.result)
	}
	if steered.result.Name != "steer-label" {
		t.Fatalf("steered reply name = %q, want steer-label", steered.result.Name)
	}
}

// TestDelegateName_PopulateSendResultPreservesDescriptorNameOnNamelessMetadata
// verifies that terminal metadata without a name field does not overwrite the
// descriptor-derived name already on the result. Packets written by older
// versions carry no name in metadata; the descriptor-derived value must survive.
func TestDelegateName_PopulateSendResultPreservesDescriptorNameOnNamelessMetadata(t *testing.T) {
	t.Parallel()
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		Task: "task",
	})
	packet := delegatestore.TerminalPacket{
		Kind:     delegatestore.PacketReported,
		Metadata: metadata,
	}
	var result sendMessageResult
	result.Name = "descriptor-label"
	populateStableDelegateSendResult(&result, packet)
	if result.Name != "descriptor-label" {
		t.Fatalf("name overwritten to %q, want descriptor-label", result.Name)
	}
}
