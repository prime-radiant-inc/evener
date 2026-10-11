package hub

import (
	"context"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	items := append([]appwire.ThreadItem{}, all[start:]...)
	return appwire.EvenerSubagentPreviewResponse{
		Ref:       ref,
		Items:     items,
		Truncated: start > 0,
	}
}

// pastSubagentPreview previews a session no daemon serves from its transcript
// index's latest window: the preview needs only the newest few items, and
// whether older ones exist.
func pastSubagentPreview(ctx context.Context, cfg hubcore.WebConfig, ref string, limit int) (appwire.EvenerSubagentPreviewResponse, bool, error) {
	limit = clampSubagentPreviewLimit(limit)
	read, ok, err := pastThreadItemReadResponse(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemLimit: limit})
	if !ok || err != nil {
		return appwire.EvenerSubagentPreviewResponse{}, ok, err
	}
	preview := subagentPreviewFromThread(read.Thread, ref, limit)
	preview.Truncated = preview.Truncated || read.OlderCursor != ""
	return preview, true, nil
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
