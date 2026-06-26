package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

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

func (s *WebServer) handleSubagentPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		http.Error(w, "ref required", http.StatusBadRequest)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	source, err := sourceForThreadWithManagedLaunch(ctx, s.cfg, s.sources, ref, "")
	if err != nil {
		if thread, ok := pastThreadForRead(s.cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"}); ok {
			writeJSON(w, subagentPreviewFromThread(thread, ref, limit))
			return
		}
		http.Error(w, "preview unavailable", http.StatusNotFound)
		return
	}
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		if thread, ok := pastThreadForRead(s.cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"}); ok {
			writeJSON(w, subagentPreviewFromThread(thread, ref, limit))
			return
		}
		http.Error(w, "preview unavailable", http.StatusBadGateway)
		return
	}
	writeJSON(w, subagentPreviewFromThread(resp.Thread, ref, limit))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
