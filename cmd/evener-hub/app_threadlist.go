package hub

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// threadListSourceTimeout prevents one unavailable or busy source from
// holding the Hub's roster RPC open forever. Sources are queried concurrently,
// so the total wait is bounded by this duration rather than by the number of
// configured sources.
var threadListSourceTimeout = 3 * time.Second

func hubThreadList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	var threads []appwire.Thread
	liveIDs := map[string]struct{}{}
	allSources := sources.All()
	type sourceResult struct {
		index int
		resp  appwire.ThreadListResponse
		err   error
	}
	results := make(chan sourceResult, len(allSources))
	for index, source := range allSources {
		if !sourceAllowedForList(source.ID(), params) {
			continue
		}
		go func(index int, source appsource.Source) {
			sourceCtx, cancel := context.WithTimeout(ctx, threadListSourceTimeout)
			defer cancel()
			done := make(chan sourceResult, 1)
			go func() {
				resp, err := source.ListThreads(sourceCtx, params)
				done <- sourceResult{index: index, resp: resp, err: err}
			}()
			select {
			case result := <-done:
				results <- result
			case <-sourceCtx.Done():
				results <- sourceResult{index: index, err: sourceCtx.Err()}
			}
		}(index, source)
	}
	listed := make([]sourceResult, 0, len(allSources))
	for _, source := range allSources {
		if !sourceAllowedForList(source.ID(), params) {
			continue
		}
		select {
		case result := <-results:
			listed = append(listed, result)
		case <-ctx.Done():
			return appwire.ThreadListResponse{}, ctx.Err()
		}
	}
	slices.SortFunc(listed, func(a, b sourceResult) int { return a.index - b.index })
	for _, result := range listed {
		source := allSources[result.index]
		resp, err := result.resp, result.err
		if err != nil {
			if sourceExplicitlyRequestedForList(source.ID(), params) {
				return appwire.ThreadListResponse{}, err
			}
			continue
		}
		for _, thread := range resp.Data {
			sourceID := threadListSourceID(source.ID(), thread)
			for _, id := range threadListIDs(sourceID, thread) {
				if key := threadListSourceKey(sourceID, id); key != "" {
					liveIDs[key] = struct{}{}
				}
			}
			var err error
			thread, err = mergePastMetadataForList(ctx, cfg, source.ID(), thread)
			if err != nil {
				return appwire.ThreadListResponse{}, err
			}
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
			thread := pastEntryThreadForList(cfg, entry)
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

func threadListSourceID(defaultSourceID string, thread appwire.Thread) string {
	if thread.Source != "" {
		return thread.Source
	}
	if ref, err := appwire.ParseRef(thread.Evener.Ref); err == nil && ref.SourceID != "" {
		return ref.SourceID
	}
	return defaultSourceID
}

func threadListIDs(sourceID string, thread appwire.Thread) []string {
	ids := []string{thread.ID, thread.SessionID}
	if ref, err := appwire.ParseRef(thread.Evener.Ref); err == nil && ref.SourceID == sourceID {
		ids = append(ids, ref.ThreadID)
	}
	return ids
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

// mergePastMetadataForList enriches live with its past-persisted metadata.
// The returned error is non-nil ONLY for ctx cancellation/deadline: every
// OTHER failure reading past data (no matching entry, a corrupt journal, …)
// still degrades to the unenriched live thread — only cancellation must
// stop the caller's sweep instead of being treated the same way.
func mergePastMetadataForList(ctx context.Context, cfg hubcore.WebConfig, sourceID string, live appwire.Thread) (appwire.Thread, error) {
	if cfg.Past == nil {
		return live, nil
	}
	if threadListSourceID(sourceID, live) != "local" {
		return live, nil
	}
	var entry hubcore.PastEntry
	var ok bool
	for _, id := range threadListIDs("local", live) {
		if id == "" {
			continue
		}
		entry, ok = cfg.Past.Find(id)
		if ok {
			break
		}
	}
	if !ok {
		return live, nil
	}
	if err := ctx.Err(); err != nil {
		return appwire.Thread{}, err
	}
	past := pastEntryThreadForList(cfg, entry)
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
	if live.Evener.Ref == "" {
		live.Evener.Ref = past.Evener.Ref
	}
	if live.Evener.Profile == "" {
		live.Evener.Profile = past.Evener.Profile
	}
	return live, nil
}

// pastEntryThreadForList is deliberately metadata-only. pastEntryThread also
// loads delegate history and persisted task state, which is appropriate for a
// single thread/read but makes a roster request scale with every transcript's
// event log. The list response only needs fields that can enrich a live row.
func pastEntryThreadForList(cfg hubcore.WebConfig, entry hubcore.PastEntry) appwire.Thread {
	title := schema.SessionDisplayName(entry.Meta)
	if title == "" {
		title = entry.Meta.ID
	}
	name := title
	if name == "" || name == strings.TrimSpace(entry.Meta.ID) {
		name = hubcore.ShortID(strings.TrimSpace(entry.Meta.ID))
	}
	cwd := entry.Meta.EnvInfo.WorkingDir
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
	parentRef := ""
	if entry.Meta.ParentSessionID != "" {
		parentRef = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ParentSessionID}.String()
	}
	kind := "session"
	if entry.Meta.IsSubagent {
		kind = "subagent"
	} else if entry.Meta.ParentSessionID != "" {
		kind = "fork"
	}
	status := appwire.ThreadStatusNotLoaded
	if cfg.Roster != nil {
		if subState, live := cfg.Roster.SubagentState(entry.Meta.ID); live {
			subState = strings.TrimSpace(subState)
			if subState == "" {
				subState = appwire.ThreadStatusActive
			}
			status = subState
		}
	}
	return appwire.Thread{
		ID:            entry.Meta.ID,
		SessionID:     entry.Meta.ID,
		Preview:       title,
		Name:          name,
		ModelProvider: entry.Meta.Model,
		CreatedAt:     hubcore.UnixSeconds(hubcore.OrderCreatedAt(entry.Meta.CreatedAt, entry.Meta.UpdatedAt)),
		UpdatedAt:     hubcore.UnixSeconds(hubcore.OrderUpdatedAt(entry.Meta.UpdatedAt, entry.Meta.CreatedAt)),
		Status:        appwire.ThreadStatus{Type: status},
		Path:          filepath.Base(cwd),
		CWD:           cwd,
		Source:        "local",
		Evener: appwire.EvenerThread{
			Ref:       ref,
			ParentRef: parentRef,
			Kind:      kind,
			Profile:   entry.Meta.ProfileID,
		},
	}
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
		thread.Evener.Profile,
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
