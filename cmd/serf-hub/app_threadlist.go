package main

import (
	"context"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
)

var ensureManagedCodexSourcesForList = ensureManagedCodexSources

func hubThreadList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	var threads []appwire.Thread
	liveIDs := map[string]struct{}{}
	if err := ensureManagedCodexSourcesForList(ctx, cfg, sources, params); err != nil {
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
			thread, err := pastEntryThread(cfg, entry, false)
			if err != nil {
				return appwire.ThreadListResponse{}, err
			}
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
	annotateThreadProjects(threads)
	return appwire.ThreadListResponse{Data: threads}, nil
}

// annotateThreadProjects carries the hub's canonical project identity across
// the appwire boundary. The TUI must consume these server keys; it must not
// derive an action key from a display name or basename. Resolve each distinct
// source path once because a list commonly contains many sessions per project.
func annotateThreadProjects(threads []appwire.Thread) {
	projects := make(map[string]identifier.Project)
	for i := range threads {
		path := strings.TrimSpace(threads[i].CWD)
		if path == "" {
			continue
		}
		project, ok := projects[path]
		if !ok {
			var err error
			project, err = identifier.ResolveProject(path)
			if err != nil {
				projects[path] = identifier.Project{}
				continue
			}
			projects[path] = project
		}
		if project.ID == "" {
			continue
		}
		threads[i].ProjectID = project.ID
		threads[i].ProjectPath = project.CanonicalPath
	}
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
	return slices.Contains(params.SourceIDs, sourceID)
}

func sourceExplicitlyRequestedForList(sourceID string, params appwire.ThreadListParams) bool {
	return slices.Contains(params.SourceIDs, sourceID)
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
	past, err := pastEntryThread(cfg, entry, false)
	if err != nil {
		return live
	}
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
	if len(params.SourceIDs) > 0 && !slices.Contains(params.SourceIDs, thread.Source) {
		return false
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
