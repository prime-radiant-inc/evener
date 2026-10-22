package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/task"
)

func writeTaskStoreFixture(t *testing.T, stateDir, owner string, tasks []task.Task) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, "tasks"), 0o755); err != nil {
		t.Fatalf("create task store dir: %v", err)
	}
	raw, err := json.Marshal(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "tasks", owner+".json"), raw, 0o600); err != nil {
		t.Fatalf("write task store: %v", err)
	}
}

func TestReportSummaryCountsAndCurrent(t *testing.T) {
	dir := t.TempDir()
	writeTaskStoreFixture(t, dir, "s1", []task.Task{
		{ID: 5, Description: "working later", Status: task.TaskInProgress},
		{ID: 1, Description: "finished", Status: task.TaskDone},
		{ID: 2, Description: "dropped", Status: task.TaskCancelled},
		{ID: 3, Description: "working first", Status: task.TaskInProgress},
		{ID: 4, Description: "waiting", Status: task.TaskOpen},
	})
	summary := buildReportSummary(&Session{id: "s1", stateDir: dir}, "done")
	if !summary.Tasks.Present {
		t.Fatal("task summary is not present")
	}
	if summary.Tasks.Total != 5 || summary.Tasks.Done != 1 || summary.Tasks.Cancelled != 1 || summary.Tasks.Remaining != 3 {
		t.Fatalf("task counts = %+v, want 5/1/1/3", summary.Tasks)
	}
	if summary.Tasks.Current == nil || summary.Tasks.Current.ID != 5 {
		t.Fatalf("current task = %+v, want task 5", summary.Tasks.Current)
	}
	if len(summary.Tasks.Incomplete) != 3 || summary.Tasks.Incomplete[0].ID != 3 {
		t.Fatalf("incomplete tasks = %+v, want tasks 3, 4 and 5 with 3 first", summary.Tasks.Incomplete)
	}
}

func TestReportSummaryBoundsIncompleteList(t *testing.T) {
	dir := t.TempDir()
	tasks := make([]task.Task, 0, 12)
	for id := 1; id <= 12; id++ {
		status := task.TaskOpen
		if id == 2 || id == 5 {
			status = task.TaskInProgress
		}
		tasks = append(tasks, task.Task{ID: id, Description: strings.Repeat("x", 300), Status: status})
	}
	writeTaskStoreFixture(t, dir, "s1", tasks)
	summary := buildReportSummary(&Session{id: "s1", stateDir: dir}, "done")
	if len(summary.Tasks.Incomplete) != 10 {
		t.Fatalf("incomplete tasks = %d, want 10", len(summary.Tasks.Incomplete))
	}
	if !summary.Tasks.Truncated {
		t.Fatal("incomplete tasks are not marked truncated")
	}
	if summary.Tasks.Incomplete[0].ID != 2 || summary.Tasks.Incomplete[1].ID != 5 {
		t.Fatalf("incomplete order = %+v, want in-progress tasks first", summary.Tasks.Incomplete[:2])
	}
	if len(summary.Tasks.Incomplete[0].Description) > 200 {
		t.Fatalf("description length = %d, want a byte bound", len(summary.Tasks.Incomplete[0].Description))
	}
}

func TestReportSummaryAbsentStoreIsNotComplete(t *testing.T) {
	summary := buildReportSummary(&Session{id: "s1", stateDir: t.TempDir()}, "done")
	if summary.Tasks.Present {
		t.Fatal("absent task store is reported as present")
	}
	if summary.Tasks.Total != 0 || summary.Tasks.Done != 0 || summary.Tasks.Remaining != 0 {
		t.Fatalf("absent task counts = %+v, want zero values", summary.Tasks)
	}
}

func TestReportSummarySharedOwnerOnce(t *testing.T) {
	dir := t.TempDir()
	writeTaskStoreFixture(t, dir, "shared-owner", []task.Task{
		{ID: 1, Description: "shared work", Status: task.TaskDone},
	})
	s := &Session{id: "s1", stateDir: dir}
	s.cfg.spawn.sharedTaskStoreOwnerSessionID = "shared-owner"
	summary := buildReportSummary(s, "done")
	if summary.TaskOwnerID != "shared-owner" {
		t.Fatalf("task owner = %q, want shared-owner", summary.TaskOwnerID)
	}
	if summary.Tasks.Total != 1 || summary.Tasks.Done != 1 {
		t.Fatalf("shared task counts = %+v, want one done", summary.Tasks)
	}
}

func TestDelegateNotificationRendersSummaryOnce(t *testing.T) {
	summary := ReportSummary{
		Version:    1,
		ReportKind: "blocked",
		Tasks: TaskSummary{
			Present:   true,
			Total:     1,
			Remaining: 1,
			Incomplete: []TaskSummaryItem{
				{ID: 7, Status: string(task.TaskOpen), Description: "summary-only-description"},
			},
		},
		Inventory: InventorySummary{Categories: []InventoryCategory{{Category: "job", IDs: []string{"job-1"}}}},
	}
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{Report: &summary})
	content, err := delegateNotificationContent(delegateDeliveryPlan{
		delegateID: "dlg_target",
		packet:     delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: []byte(`"finished"`), Metadata: metadata},
	})
	if err != nil {
		t.Fatalf("delegateNotificationContent: %v", err)
	}
	if count := strings.Count(content, "summary-only-description"); count != 1 {
		t.Fatalf("summary description appears %d times, want once: %s", count, content)
	}
	if strings.Contains(content, `"report":`) {
		t.Fatalf("raw report metadata is repeated beside the rendered summary: %s", content)
	}
	if !strings.Contains(content, "job:job-1") {
		t.Fatalf("notification does not carry the inventory: %s", content)
	}
}

func TestInlineAndEarlierResultsCarrySummary(t *testing.T) {
	summary := ReportSummary{
		Version:    1,
		ReportKind: "done",
		Tasks:      TaskSummary{Present: true, Total: 1, Done: 1},
	}
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{Report: &summary})
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: []byte(`"finished"`), Metadata: metadata}

	current := sendMessageResult{DelegateID: "dlg_target", Type: "delegate"}
	populateStableDelegateSendResult(&current, packet)
	if current.ReportSummary == "" {
		t.Fatal("current result has no rendered report summary")
	}
	withEarlier := sendMessageResult{DelegateID: "dlg_target", Type: "delegate", Earlier: []delegatestore.TerminalPacket{packet}}
	out := delegateSendResultFrom(withEarlier)
	if len(out.EarlierResults) != 1 || out.EarlierResults[0].ReportSummary == "" {
		t.Fatalf("earlier result = %+v, want a rendered report summary", out.EarlierResults)
	}
}

func TestLaterSupervisionAttentionCanStartNewGeneration(t *testing.T) {
	first := ReportSummary{Version: 1, ReportKind: "done", Tasks: TaskSummary{Present: true, Total: 1, Done: 1}}
	second := ReportSummary{Version: 1, ReportKind: "blocked", Tasks: TaskSummary{Present: true, Total: 1, Remaining: 1}}
	firstNotice := renderReportNotice(first)
	secondNotice := renderReportNotice(second)
	if firstNotice == secondNotice {
		t.Fatal("two generations rendered the same notice")
	}
	if !strings.Contains(firstNotice, "done") || !strings.Contains(secondNotice, "blocked") {
		t.Fatalf("notices do not name their generation's disposition: %q / %q", firstNotice, secondNotice)
	}
}
