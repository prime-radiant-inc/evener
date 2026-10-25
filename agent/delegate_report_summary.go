package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/task"
)

// ReportSummary is the versioned task and outstanding-work evidence attached to
// every terminal delegate packet. Version 1 is the shape every renderer reads.
type ReportSummary struct {
	Version     int              `json:"version"`
	ReportKind  string           `json:"report_kind"`
	Tasks       TaskSummary      `json:"tasks"`
	Inventory   InventorySummary `json:"inventory"`
	TaskOwnerID string           `json:"task_owner_id,omitempty"`
	Blocker     string           `json:"blocker,omitempty"`
}

// TaskSummary is the durable task list's bounded projection.
type TaskSummary struct {
	Present    bool              `json:"present"`
	Total      int               `json:"total"`
	Done       int               `json:"done"`
	Cancelled  int               `json:"cancelled"`
	Remaining  int               `json:"remaining"`
	Current    *TaskSummaryItem  `json:"current,omitempty"`
	Incomplete []TaskSummaryItem `json:"incomplete,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
}

// TaskSummaryItem is one task named in a summary.
type TaskSummaryItem struct {
	ID          int    `json:"id"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

// InventorySummary groups the outstanding-work IDs by category.
type InventorySummary struct {
	Categories []InventoryCategory `json:"categories,omitempty"`
}

// InventoryCategory is one kind of outstanding work and its IDs.
type InventoryCategory struct {
	Category string   `json:"category"`
	IDs      []string `json:"ids"`
}

const (
	reportSummaryVersion          = 1
	reportSummaryIncompleteLimit  = 10
	reportSummaryDescriptionLimit = 200
)

// buildReportSummary reads the durable task list once and the outstanding-work
// inventory once. A missing or unreadable task store is Present:false, never an
// empty completed list. A shared task store is read through its owner identity.
func buildReportSummary(s *Session, reportKind string) ReportSummary {
	summary := ReportSummary{Version: reportSummaryVersion, ReportKind: reportKind}
	if s == nil {
		return summary
	}
	owner := s.taskStoreOwnerSessionID()
	summary.TaskOwnerID = owner
	summary.Inventory = inventorySummary(s)
	if s.stateDir == "" || owner == "" {
		return summary
	}
	path := filepath.Join(s.stateDir, "tasks", owner+".json")
	if _, err := os.Stat(path); err != nil {
		return summary
	}
	store := task.NewTaskStore(s.stateDir, owner)
	if err := store.Load(); err != nil {
		return summary
	}
	tasks, err := store.ViewWithError()
	if err != nil {
		return summary
	}
	summary.Tasks = taskSummaryFrom(tasks)
	return summary
}

func taskSummaryFrom(tasks []task.Task) TaskSummary {
	listed := task.Summarize(tasks)
	summary := TaskSummary{
		Present:   true,
		Total:     listed.Total,
		Done:      listed.Done,
		Cancelled: listed.Cancelled,
		Remaining: listed.Remaining,
	}
	var incomplete []task.Task
	for _, item := range tasks {
		switch item.Status {
		case task.TaskDone, task.TaskCancelled:
			continue
		}
		incomplete = append(incomplete, item)
	}
	sort.SliceStable(incomplete, func(i, j int) bool {
		iProgress := incomplete[i].Status == task.TaskInProgress
		jProgress := incomplete[j].Status == task.TaskInProgress
		if iProgress != jProgress {
			return iProgress
		}
		return incomplete[i].ID < incomplete[j].ID
	})
	if len(incomplete) > reportSummaryIncompleteLimit {
		incomplete = incomplete[:reportSummaryIncompleteLimit]
		summary.Truncated = true
	}
	for _, item := range incomplete {
		summary.Incomplete = append(summary.Incomplete, TaskSummaryItem{
			ID:          item.ID,
			Status:      string(item.Status),
			Description: runetrim.Cut(item.Description, reportSummaryDescriptionLimit),
		})
	}
	if listed.Current != nil {
		summary.Current = &TaskSummaryItem{
			ID:          listed.Current.ID,
			Status:      string(listed.Current.Status),
			Description: runetrim.Cut(listed.Current.Description, reportSummaryDescriptionLimit),
		}
	}
	return summary
}

func inventorySummary(s *Session) InventorySummary {
	work, _ := s.outstandingDelegateWork()
	byCategory := map[string][]string{}
	for _, id := range work.IDs() {
		category, rest, ok := strings.Cut(id, ":")
		if !ok {
			continue
		}
		byCategory[category] = append(byCategory[category], rest)
	}
	categories := make([]string, 0, len(byCategory))
	for category := range byCategory {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	summary := InventorySummary{}
	for _, category := range categories {
		ids := byCategory[category]
		sort.Strings(ids)
		summary.Categories = append(summary.Categories, InventoryCategory{Category: category, IDs: ids})
	}
	return summary
}

// renderReportNotice is the one human-readable rendering of a summary. It is
// generation-scoped: this generation is settled, and a later message may start
// a new one. The summary is never refreshed from live task or inventory state.
func renderReportNotice(summary ReportSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Generation final (%s).", summary.ReportKind)
	if summary.Tasks.Present {
		fmt.Fprintf(&b, " Tasks: %d done, %d cancelled, %d remaining of %d.", summary.Tasks.Done, summary.Tasks.Cancelled, summary.Tasks.Remaining, summary.Tasks.Total)
		if summary.Tasks.Current != nil {
			fmt.Fprintf(&b, " Current: task %d %s.", summary.Tasks.Current.ID, summary.Tasks.Current.Description)
		}
		if len(summary.Tasks.Incomplete) != 0 {
			parts := make([]string, 0, len(summary.Tasks.Incomplete))
			for _, item := range summary.Tasks.Incomplete {
				parts = append(parts, fmt.Sprintf("task %d %s (%s)", item.ID, item.Description, item.Status))
			}
			fmt.Fprintf(&b, " Incomplete: %s.", strings.Join(parts, "; "))
			if summary.Tasks.Truncated {
				b.WriteString(" The list is truncated.")
			}
		}
	} else {
		b.WriteString(" Task list unavailable.")
	}
	if summary.Blocker != "" {
		fmt.Fprintf(&b, " Blocker: %s.", summary.Blocker)
	}
	if len(summary.Inventory.Categories) != 0 {
		parts := make([]string, 0, len(summary.Inventory.Categories))
		for _, category := range summary.Inventory.Categories {
			for _, id := range category.IDs {
				parts = append(parts, category.Category+":"+id)
			}
		}
		fmt.Fprintf(&b, " Outstanding: %s.", strings.Join(parts, ", "))
	}
	b.WriteString(" Resume this delegate if the work is unfinished; a later message may start a new generation.")
	return b.String()
}

// terminalPacketForDelivery projects the captured typed summary at the wire
// boundary. The durable packet stays untouched, and no live state is reread.
func terminalPacketForDelivery(packet delegatestore.TerminalPacket) delegatestore.TerminalPacket {
	if len(packet.Metadata) == 0 {
		return packet
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil || metadata.Report == nil {
		return packet
	}
	// Keep every unrelated field and its presence, including fields written by
	// newer producers. Only report's representation changes at this boundary.
	var original map[string]json.RawMessage
	if json.Unmarshal(packet.Metadata, &original) != nil {
		return packet
	}
	delete(original, "report")
	original["report_notice"], _ = json.Marshal(renderReportNotice(*metadata.Report))
	packet.Metadata, _ = json.Marshal(original)
	return packet
}
