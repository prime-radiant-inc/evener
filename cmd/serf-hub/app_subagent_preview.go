package main

import (
	"strings"

	"primeradiant.com/serf/appwire"
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

func subagentPreviewFromThread(thread appwire.Thread, ref string, limit int) appwire.SerfSubagentPreviewResponse {
	limit = clampSubagentPreviewLimit(limit)
	if strings.TrimSpace(ref) == "" {
		ref = thread.Serf.Ref
	}
	var all []appwire.ThreadItem
	for _, turn := range thread.Turns {
		for _, item := range turn.Items {
			all = append(all, subagentPreviewItem(item))
		}
	}
	start := len(all) - limit
	if start < 0 {
		start = 0
	}
	items := append([]appwire.ThreadItem(nil), all[start:]...)
	return appwire.SerfSubagentPreviewResponse{
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
