package hub

import (
	"strings"

	"primeradiant.com/evener/appwire"
)

const (
	subagentPreviewDefaultLimit = 3
	subagentPreviewMaxLimit     = 5
)

func clampSubagentPreviewLimit(limit int) int {
	if limit <= 0 {
		return subagentPreviewDefaultLimit
	}
	if limit > subagentPreviewMaxLimit {
		return subagentPreviewMaxLimit
	}
	return limit
}

func subagentPreviewFromThread(thread appwire.Thread, ref string, limit int) appwire.EvenerSubagentPreviewResponse {
	limit = clampSubagentPreviewLimit(limit)
	if strings.TrimSpace(ref) == "" {
		ref = thread.Evener.Ref
	}
	var all []appwire.ThreadItem
	for _, turn := range thread.Turns {
		for _, item := range turn.Items {
			all = append(all, subagentPreviewItem(item))
		}
	}
	start := max(len(all)-limit, 0)
	items := append([]appwire.ThreadItem(nil), all[start:]...)
	return appwire.EvenerSubagentPreviewResponse{
		Ref:       ref,
		Items:     items,
		Truncated: start > 0,
	}
}

func subagentPreviewItem(item appwire.ThreadItem) appwire.ThreadItem {
	return appwire.ThreadItem{
		Type:        item.Type,
		Text:        item.Text,
		Delta:       item.Delta,
		Images:      item.Images,
		ToolName:    item.ToolName,
		CallID:      item.CallID,
		Description: item.Description,
		Output:      item.Output,
		Error:       item.Error,
		Status:      item.Status,
		StartedAt:   item.StartedAt,
		CompletedAt: item.CompletedAt,
	}
}
