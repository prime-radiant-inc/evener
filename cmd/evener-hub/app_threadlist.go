package hub

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// threadListSourceTimeout bounds one source's thread/list work. A local
// daemon's list is a loopback call whose failure is that daemon's own problem,
// so three seconds is enough to decide whether to wait for it.
const threadListSourceTimeout = 3 * time.Second

// threadListBudgetSource is a source whose ListThreads needs a deadline of its
// own instead of the local-daemon budget. A remote hub source is the case: its
// list resolves a client through the SSH manager, which attaches the host on
// first use — a spawn, an initialize handshake and a preflight that routinely
// outlive three seconds and can take the restart/deploy ladder.
//
// Budgeting only the local-daemon call is not merely impatient with such a
// source: the timeout that cuts a cold attach off is indistinguishable from a
// genuinely unavailable host, so an unfiltered list drops the whole host and
// still reports success — an entire machine's threads disappear from the
// sidebar with nothing to say anything failed. A source that reports a budget
// is given it, so the attach completes and the host is listed.
type threadListBudgetSource interface {
	// ThreadListBudget reports the deadline one ListThreads call needs. A
	// non-positive value means the source has no budget of its own and the
	// caller's default applies.
	ThreadListBudget() time.Duration
}

// attachErrorClassifier is a source that can classify one attach/connect
// failure into the same typed transport-unavailable error its own call path
// produces. The explicit thread/list fan-out attaches a host-targeted remote
// source before calling it, and routes that attach failure through this so it
// reaches the caller as the typed SessionUnavailable rather than the raw
// transport error. A source that does not implement it leaves the error raw,
// preserving every other source's behavior. *appsource.RemoteHubSource
// implements it.
type attachErrorClassifier interface {
	MapAttachError(err error) error
}

// threadListTimeoutFor returns the deadline one source's ListThreads runs
// under: the budget the source reports, or fallback (the local-daemon budget)
// for every source that reports none. Local daemons never attach, so their
// three-second budget is unchanged.
func threadListTimeoutFor(source appsource.Source, fallback time.Duration) time.Duration {
	if budgeted, ok := source.(threadListBudgetSource); ok {
		if budget := budgeted.ThreadListBudget(); budget > 0 {
			return budget
		}
	}
	return fallback
}

const threadListSourceWorkers = 4

func hubThreadList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return hubThreadListWithSourceTimeout(ctx, cfg, sources, params, threadListSourceTimeout)
}

// hubThreadListWithSourceTimeout fans one list out across every allowed source.
// sourceTimeout is the budget for a source that reports none of its own (the
// local-daemon default in production); a threadListBudgetSource is given the
// deadline it reports instead.
func hubThreadListWithSourceTimeout(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadListParams, sourceTimeout time.Duration) (appwire.ThreadListResponse, error) {
	threads := make([]appwire.Thread, 0)
	liveIDs := map[string]struct{}{}
	allSources := sources.All()
	remoteHosts := remoteHostNames(cfg)
	explicit := len(params.SourceIDs) > 0
	type sourceResult struct {
		index int
		resp  appwire.ThreadListResponse
		err   error
	}
	allowed := make([]int, 0, len(allSources))
	for index, source := range allSources {
		if !sourceAllowedForList(source.ID(), params) {
			continue
		}
		// A non-explicit (empty-filter) fan-out must not force attachment: a
		// remote host with no live channel is skipped without a call, so simply
		// opening the hub and listing the fleet cannot dial every configured host
		// (component 05, §"The same gate applies to the primary thread/list
		// fan-out"). A source named explicitly in SourceIDs is a deliberate,
		// host-targeted request and may attach (below).
		if !explicit && !remoteSourceAttached(cfg, remoteHosts, source.ID()) {
			continue
		}
		allowed = append(allowed, index)
	}
	results := make(chan sourceResult, len(allowed))
	jobs := make(chan int)
	workerCount := min(threadListSourceWorkers, len(allowed))
	for range workerCount {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					source := allSources[index]
					// One source's list may have to attach a transport first (a
					// remote host's cold SSH attach); such a source reports the
					// budget that call needs, and every other source keeps the
					// local-daemon deadline the caller passed.
					sourceCtx, cancel := context.WithTimeout(ctx, threadListTimeoutFor(source, sourceTimeout))
					// The explicit host-targeted request is one of the two intended
					// attach triggers: attach before calling the source, whose own
					// resolver is attached-only and so never dials. A non-explicit
					// request never reaches here for an unattached host.
					if _, isRemote := remoteHosts[source.ID()]; isRemote &&
						sourceExplicitlyRequestedForList(source.ID(), params) && cfg.RemoteHostClient != nil {
						// dialRemoteHost applies the shared host-routing origin
						// guard before the Ensure-backed dial: a remote-originated
						// request may not make this hub attach a host (component 07,
						// §"Host-routing origin guard").
						if _, err := dialRemoteHost(sourceCtx, cfg, source.ID()); err != nil {
							// Classify the attach failure through the source exactly as
							// its own call path classifies a connect failure: sshconn's
							// transient attach failures and transport losses become the
							// typed SessionUnavailable the auto-resume/refusal gates
							// match, instead of surfacing here as a raw transport error.
							// The caller's own context ending stays raw, matching
							// RemoteHubSource.call. A guard refusal is already a typed
							// WireError and must not be re-labelled.
							if cerr := ctx.Err(); cerr != nil {
								err = cerr
							} else if _, isWire := errors.AsType[appwire.WireError](err); !isWire {
								if classifier, ok := source.(attachErrorClassifier); ok {
									err = classifier.MapAttachError(err)
								}
							}
							results <- sourceResult{index: index, err: err}
							cancel()
							continue
						}
					}
					resp, err := source.ListThreads(sourceCtx, params)
					cancel()
					results <- sourceResult{index: index, resp: resp, err: err}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, index := range allowed {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	listed := make([]sourceResult, 0, len(allowed))
	for range allowed {
		select {
		case result := <-results:
			listed = append(listed, result)
		case <-ctx.Done():
			return appwire.ThreadListResponse{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return appwire.ThreadListResponse{}, err
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
			thread = applyHubForkCapability(cfg, thread)
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
			thread, err := pastEntryThreadForList(ctx, cfg, entry)
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
		// A remote hub source's threads name another machine's filesystem. Using
		// this hub's ResolveProject on that CWD would stamp a controller-local
		// project identity over the one the remote hub already computed, grouping
		// the thread under the wrong project and aiming project-scoped actions at a
		// controller-local path. Only a local thread's CWD describes a path this
		// hub can resolve; a remote thread keeps the remote's ProjectID/ProjectPath.
		// This mirrors the locality gate on past metadata (mergePastMetadataForList)
		// and file-backed image enrichment (EnrichThreadFileBackedImages).
		if threadListSourceID("local", threads[i]) != "local" {
			continue
		}
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

// remoteHostKnown reports whether name names a remote host this hub knows:
// one the configured entries carry (cfg.RemoteHosts is the configured truth)
// or one the live registry holds — a registry only holds validated entries,
// while the config carries whatever the operator declared. It is the one
// configured∪registry membership rule, shared by
// validateDecisionSource's per-name probe and by remoteHostNames' whole-set
// build below, so a decision source and a fan-out gate can never disagree
// about which names count as remote.
func remoteHostKnown(cfg hubcore.WebConfig, name string) bool {
	if cfg.RemoteHostRegistry != nil {
		if _, ok := cfg.RemoteHostRegistry.Get(name); ok {
			return true
		}
	}
	for _, host := range cfg.RemoteHosts {
		if host.Name == name {
			return true
		}
	}
	return false
}

// remoteHostNames is the set of remote host names — the enumeration half of
// the same configured∪registry rule remoteHostKnown probes per name — so the
// fan-out can tell a remote source (which must gate on attachment) from the
// local one. The live registry is the authority for hosts that exist past
// boot: it carries every host the management surface added at runtime, so a
// UI-added host is classified remote by the same gates a configured one is —
// an explicit thread/list attaches it, and the background refresh gates it on
// attachment instead of treating it as local. The configured entries stay in
// the set alongside it, so the union — not the registry alone — is the set of
// names these gates treat as remote.
func remoteHostNames(cfg hubcore.WebConfig) map[string]struct{} {
	names := make(map[string]struct{}, len(cfg.RemoteHosts))
	for _, host := range cfg.RemoteHosts {
		names[host.Name] = struct{}{}
	}
	if cfg.RemoteHostRegistry != nil {
		// Names() is the name set of All() without the entry copies: this
		// runs per thread/list request, and only membership is needed here.
		for _, name := range cfg.RemoteHostRegistry.Names() {
			names[name] = struct{}{}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// remoteSourceAttached reports whether a source is safe for a non-explicit
// fan-out to call: a local source always is, and a remote source only while the
// attached-only lookup finds a live channel. It is a skip, never a dial: the
// lookup is Manager.ClientIfAttached, so an unattached host is skipped without a
// call and a host that drops between this check and the source's own
// (attached-only) resolution is reported unavailable rather than re-attached.
// With no lookup wired (tests) it reports attached, preserving the pre-gate
// behavior.
func remoteSourceAttached(cfg hubcore.WebConfig, remoteHosts map[string]struct{}, sourceID string) bool {
	if _, isRemote := remoteHosts[sourceID]; !isRemote {
		return true
	}
	if cfg.RemoteHostClientIfAttached == nil {
		return true
	}
	_, ok := cfg.RemoteHostClientIfAttached(sourceID)
	return ok
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
	past, err := pastEntryThreadForList(ctx, cfg, entry)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return appwire.Thread{}, ctxErr
		}
		return live, nil
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
	if live.Evener.Ref == "" {
		live.Evener.Ref = past.Evener.Ref
	}
	if live.Evener.Profile == "" {
		live.Evener.Profile = past.Evener.Profile
	}
	return live, nil
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
