package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
)

// Non-worktree delegate names are display-only labels: the create result, the
// durable descriptor, job_list, and job_status all surface them, while every
// addressing surface stays keyed to the delegate id.

func TestDelegateName_LabelCarriesToCreateResultAndListings(t *testing.T) {
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
