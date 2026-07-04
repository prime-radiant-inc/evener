package main

import (
	"context"
	"sort"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
)

func hubThreadList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	var threads []appwire.Thread
	liveIDs := map[string]struct{}{}
	if err := ensureManagedCodexSources(ctx, cfg, sources, params); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	for _, source := range sources.All() {
		if !sourceAllowedForList(source.ID(), params) {
			continue
		}
		resp, err := source.ListThreads(ctx, params)
		if err != nil {
			if sourceExplicitlyRequestedForList(source.ID(), params) {
				return appwire.ThreadListResponse{}, err
			}
			continue
		}
		for _, thread := range resp.Data {
			sourceID := threadListSourceID(source.ID(), thread)
			for _, id := range []string{thread.ID, thread.SessionID} {
				if key := threadListSourceKey(sourceID, id); key != "" {
					liveIDs[key] = struct{}{}
				}
			}
			thread = mergePastMetadataForList(cfg, source.ID(), thread)
			thread = sanitizeStaleProcessingStatus(cfg, thread)
			if appThreadMatches(thread, params) {
				threads = append(threads, thread)
			}
		}
	}
	if cfg.Past != nil {
		limit := params.Limit
		if limit <= 0 {
			limit = 100
		}
		for _, entry := range cfg.Past.Search(params.SearchTerm, limit, 0) {
			if _, ok := liveIDs[threadListSourceKey("local", entry.ID)]; ok {
				continue
			}
			thread := pastEntryThread(entry, false)
			if appThreadMatches(thread, params) {
				threads = append(threads, thread)
			}
		}
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return hubcore.AppwireThreadLess(threads[i], threads[j])
	})
	if params.Limit > 0 && len(threads) > params.Limit {
		threads = threads[:params.Limit]
	}
	return appwire.ThreadListResponse{Data: threads}, nil
}

func ensureManagedCodexSources(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) error {
	if cfg.CodexLauncher == nil || sources == nil {
		return nil
	}
	for _, launch := range cfg.CodexLaunches {
		sourceID := strings.TrimSpace(launch.ID)
		if sourceID == "" {
			sourceID = "codex"
		}
		if !sourceAllowedForList(sourceID, params) {
			continue
		}
		if _, err := cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources); err != nil && sourceExplicitlyRequestedForList(sourceID, params) {
			return err
		}
	}
	return nil
}

func threadListSourceID(defaultSourceID string, thread appwire.Thread) string {
	if thread.Source != "" {
		return thread.Source
	}
	if ref, err := appwire.ParseRef(thread.Serf.Ref); err == nil && ref.SourceID != "" {
		return ref.SourceID
	}
	return defaultSourceID
}

func threadListSourceKey(sourceID, threadID string) string {
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

func sourceAllowedForList(sourceID string, params appwire.ThreadListParams) bool {
	if len(params.SourceIDs) == 0 {
		return true
	}
	for _, want := range params.SourceIDs {
		if want == sourceID {
			return true
		}
	}
	return false
}

func sourceExplicitlyRequestedForList(sourceID string, params appwire.ThreadListParams) bool {
	for _, requested := range params.SourceIDs {
		if requested == sourceID {
			return true
		}
	}
	return false
}

func mergePastMetadataForList(cfg hubcore.WebConfig, sourceID string, live appwire.Thread) appwire.Thread {
	if cfg.Past == nil {
		return live
	}
	if threadListSourceID(sourceID, live) != "local" {
		return live
	}
	var entry hubcore.PastEntry
	var ok bool
	for _, id := range []string{live.ID, live.SessionID} {
		if id == "" {
			continue
		}
		entry, ok = cfg.Past.Find(id)
		if ok {
			break
		}
	}
	if !ok {
		return live
	}
	past := pastEntryThread(entry, false)
	if live.ID == "" {
		live.ID = past.ID
	}
	if live.SessionID == "" {
		live.SessionID = past.SessionID
	}
	if live.Preview == "" || live.Preview == live.ID || live.Preview == live.SessionID {
		live.Preview = past.Preview
	}
	if live.Name == "" {
		live.Name = past.Name
	}
	if live.ModelProvider == "" {
		live.ModelProvider = past.ModelProvider
	}
	if past.CreatedAt != 0 {
		live.CreatedAt = past.CreatedAt
	}
	if past.UpdatedAt != 0 {
		live.UpdatedAt = past.UpdatedAt
	}
	if live.Path == "" || live.Path == "." {
		live.Path = past.Path
	}
	if live.CWD == "" {
		live.CWD = past.CWD
	}
	if live.Source == "" {
		live.Source = past.Source
	}
	if live.Serf.Ref == "" {
		live.Serf.Ref = past.Serf.Ref
	}
	if live.Serf.Profile == "" {
		live.Serf.Profile = past.Serf.Profile
	}
	return live
}

func appThreadMatches(thread appwire.Thread, params appwire.ThreadListParams) bool {
	if len(params.Statuses) > 0 {
		status := strings.ToLower(thread.Status.Type)
		found := false
		for _, want := range params.Statuses {
			if strings.EqualFold(normalizeThreadListStatusFilter(want), status) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(params.SourceIDs) > 0 {
		found := false
		for _, sourceID := range params.SourceIDs {
			if sourceID == thread.Source {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	q := strings.ToLower(strings.TrimSpace(params.SearchTerm))
	if q == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		thread.ID,
		thread.SessionID,
		thread.Name,
		thread.Preview,
		thread.CWD,
		thread.Path,
		thread.ModelProvider,
		thread.Serf.Profile,
	}, " "))
	return strings.Contains(haystack, q)
}

func normalizeThreadListStatusFilter(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active":
		return appwire.ThreadStatusActive
	case "notloaded":
		return appwire.ThreadStatusNotLoaded
	case "systemerror":
		return appwire.ThreadStatusSystemError
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

// sanitizeStaleProcessingStatus rewrites a thread's status when the live
// source claims the session is active but hubcore.WedgedStatus finds it
// wedged (see that doc for the underlying agent-loop gap this compensates
// for, kata r6y9) — otherwise hub readers conclude "active" and the
// workspace UI disables steer/send forever. Only local-source threads are
// checked: other sources have no past index entry, and therefore no
// transcript to inspect.
func sanitizeStaleProcessingStatus(cfg hubcore.WebConfig, thread appwire.Thread) appwire.Thread {
	if cfg.Past == nil {
		return thread
	}
	if thread.Status.Type != appwire.ThreadStatusActive {
		return thread
	}
	threadID := strutil.FirstNonEmpty(thread.ID, thread.SessionID)
	if threadID == "" {
		return thread
	}
	if thread.Serf.Ref != "" {
		ref, err := appwire.ParseRef(thread.Serf.Ref)
		if err == nil && ref.SourceID != "" && ref.SourceID != "local" {
			return thread
		}
	} else if thread.Source != "" && thread.Source != "local" {
		return thread
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return thread
	}
	if hubcore.WedgedStatus(entry) {
		thread.Status = appwire.ThreadStatus{Type: appwire.ThreadStatusSystemError}
	}
	return thread
}
