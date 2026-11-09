package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
)

// A Manager read is deliberately below the runtime tool-result cap. This
// leaves space for the envelope and follow-up arguments. Tasks shrink by
// complete rows, never by clipping task text.
const managerReadMaxJSONBytes = 256 << 10

const managerTaskPageMaxJSONBytes = managerReadMaxJSONBytes - 4096

func (o *sessionOperations) discover(ctx context.Context, params appwire.ThreadListParams) (appwire.SessionDiscovery, error) {
	builtAtTime := time.Now()
	builtAt := builtAtTime.UnixMilli()
	ctx = appsource.WithThreadListBuiltAt(ctx, builtAtTime)
	threads, sourceStates, err := hubThreadListWithSourceStates(ctx, o.cfg, o.sources, params)
	if err != nil {
		return appwire.SessionDiscovery{}, err
	}
	for _, sourceID := range params.SourceIDs {
		if sourceStates[sourceID] == "" {
			return appwire.SessionDiscovery{}, appwire.SessionUnavailable("source unavailable: " + sourceID)
		}
	}
	o.rosterBroker().enrichDiscovery(threads.Data)
	discovery := appwire.SessionDiscovery{Threads: threads, Sources: []appwire.SessionSource{{ID: "local", State: sourceStates["local"]}}, BuiltAt: builtAt}
	seen := map[string]bool{"local": true}
	hosts := o.cfg.RemoteHostRegistry
	if hosts == nil {
		hosts = hostRegistryFromConfig(o.cfg)
	}
	catalog := newHubHostManager(o.sources, nil, o.cfg, "", hosts, nil)
	listed, err := catalog.List(ctx, appwire.EmptyParams{})
	if err != nil {
		return discovery, err
	}
	// Only public source identity leaves this call. HostRow includes private SSH
	// paths and administration state, which are not Manager discovery data.
	for _, host := range listed.Hosts {
		if host.Removed || seen[host.Name] {
			continue
		}
		state := sourceStates[host.Name]
		if state == "" {
			if sourceAllowedForList(host.Name, params) {
				state = appwire.SessionSourceSkippedUnattached
			} else {
				state = appwire.SessionSourceNotSelected
			}
		}
		discovery.Sources = append(discovery.Sources, appwire.SessionSource{ID: host.Name, State: state})
		seen[host.Name] = true
	}
	for _, source := range o.sources.All() {
		if !seen[source.ID()] {
			discovery.Sources = append(discovery.Sources, appwire.SessionSource{ID: source.ID(), State: sourceStates[source.ID()]})
			seen[source.ID()] = true
		}
	}
	return discovery, nil
}

func (o *sessionOperations) turns(ctx context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if p.Cursor == "" {
		read, err := o.read(ctx, appwire.ThreadReadParams{
			Ref: p.Ref, IncludeTurns: true, ItemLimit: p.ItemLimit,
			ThreadWindowParams: p.ThreadWindowParams,
		}, false)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		return appwire.ThreadTurnsListResponse{Data: read.Thread.Turns, NextCursor: read.OlderCursor, BootGeneration: read.BootGeneration, Epoch: read.Epoch, Snapshot: read.Snapshot, Authoritative: read.Authoritative, Window: read.Window}, nil
	}
	if err := appwire.ValidateThreadTurnsListParams(p); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	source, err := sourceForThreadWithDeletionFence(ctx, o.cfg, o.sources, p.Ref, "")
	if err == nil {
		if p.TurnWindow != nil {
			page, continuationErr := continueLiveTranscriptWindowOwned(ctx, source, p, o)
			if continuationErr == nil {
				return page, nil
			}
			err = continuationErr
		} else {
			// The initial Manager read mints a hub-owned cursor through the
			// candidate machinery; the continuation must resolve it the same
			// way. A raw ListTurns forwards that cursor to the daemon, which
			// rejects the foreign identity as stale. The handled flag needs no
			// inspection here: without an error it only distinguishes a packed
			// page from a legacy source's page, and both are success.
			page, _, listErr := listItemTurns(ctx, source, p, nil)
			if listErr == nil {
				return page, nil
			}
			err = listErr
		}
	}
	if !isDeadSessionError(err) {
		return appwire.ThreadTurnsListResponse{}, err
	}
	page, ok, pastErr := pastThreadTurnsListOwned(ctx, o.cfg, p, func(options *transcriptWindowPackOptions) {
		o.retainCanonicalWindowOptions(options)
	})
	if pastErr != nil {
		return appwire.ThreadTurnsListResponse{}, pastErr
	}
	if ok {
		return page, nil
	}
	return appwire.ThreadTurnsListResponse{}, err
}

func continueLiveTranscriptWindow(ctx context.Context, source appsource.Source, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return continueLiveTranscriptWindowOwned(ctx, source, params, nil)
}

func continueLiveTranscriptWindowOwned(
	ctx context.Context,
	source appsource.Source,
	params appwire.ThreadTurnsListParams,
	owner *sessionOperations,
) (appwire.ThreadTurnsListResponse, error) {
	advertised := sourceAdvertisesWindowRead(ctx, source, params.Ref, params.ThreadID)
	// A remote hub owns its saved window whether or not its thread row
	// advertises the live daemon's capability, so the controller forwards every
	// continuation form and passes the hub's typed failure through.
	if remote, ok := source.(*appsource.RemoteHubSource); ok {
		page, err := remote.ListTurns(ctx, params)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		} else if page.Window != nil {
			thread := enrichSourcedThreadImages(source, appwire.Thread{Turns: page.Data})
			page.Data = thread.Turns
			if owner != nil {
				if err := owner.retainRemoteWindowItems(ctx, remote, params.Ref, thread); err != nil {
					return appwire.ThreadTurnsListResponse{}, err
				}
			}
			return page, nil
		} else if params.TurnWindow.Latest <= 0 {
			return appwire.ThreadTurnsListResponse{}, appwire.WindowUnsupportedOnDaemon()
		}
	}
	itemLimit, err := normalizeTranscriptWindowItemLimit(params.TurnWindow, params.ItemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	hubCompatibility := !advertised
	var candidates transcriptItemCandidateResult
	var read appwire.ThreadReadResponse
	if hubCompatibility {
		readParams := appwire.ThreadReadParams{
			Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: true, ItemLimit: itemLimit,
			ThreadWindowParams: params.ThreadWindowParams,
		}
		sourceParams := readParams
		sourceParams.ThreadWindowParams = appwire.ThreadWindowParams{}
		sourceParams.ItemLimit = appwire.TranscriptItemPageLimit
		read, err = source.ReadThread(ctx, sourceParams)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		loader, loadErr := newCompatibilityTranscriptWindowLoader(ctx, source, readParams, read)
		if loadErr != nil {
			return appwire.ThreadTurnsListResponse{}, loadErr
		}
		loaded, _, resolveErr := resolveTranscriptWindowContinuation(ctx, params, itemLimit, loader.load)
		if resolveErr != nil {
			return appwire.ThreadTurnsListResponse{}, resolveErr
		}
		candidates = loaded.Result
		read.Thread = loaded.Thread
	} else {
		loaded, _, resolveErr := resolveTranscriptWindowContinuation(ctx, params, itemLimit, func(ctx context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
			windowParams := params.ThreadWindowParams
			windowParams.TurnWindow = selector
			readParams := appwire.ThreadReadParams{
				Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: true, ItemLimit: itemLimit,
				ThreadWindowParams: windowParams,
			}
			loadedRead, loadErr := source.ReadThread(ctx, readParams)
			if loadErr != nil {
				return transcriptWindowLoadedRange{}, loadErr
			}
			result, loadErr := sourceItemCandidateResultForRead(ctx, source, readParams, loadedRead)
			if loadErr != nil {
				return transcriptWindowLoadedRange{}, loadErr
			}
			return transcriptWindowLoadedRange{Result: result, Thread: loadedRead.Thread}, nil
		})
		if resolveErr != nil {
			return appwire.ThreadTurnsListResponse{}, resolveErr
		}
		candidates = loaded.Result
		read.Thread = loaded.Thread
	}
	fetchOrigin := appitempaging.FetchCursorOriginServingDaemon
	appliedBy := transcriptWindowAppliedByServingDaemon
	if hubCompatibility {
		appliedBy = transcriptWindowAppliedByHubCompat
		fetchOrigin = appitempaging.FetchCursorOriginHubCompat
	} else if _, remote := source.(*appsource.RemoteHubSource); remote {
		fetchOrigin = appitempaging.FetchCursorOriginRemoteSource
	}
	options := transcriptWindowOptionsFor(candidates, params.ThreadWindowParams, itemLimit, appliedBy, fetchOrigin)
	if owner != nil {
		options = owner.canonicalWindowOptions(source, read.Thread, candidates, params.ThreadWindowParams, itemLimit, appliedBy, fetchOrigin)
	} else {
		options.CanonicalizeCandidates = func(candidates []appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, error) {
			return canonicalizeTranscriptItemCandidates(source, read.Thread, candidates)
		}
	}
	configureTranscriptWindowResponseFit(&options, params)
	packed, err := packThreadTurnsWindowItemCandidates(candidates, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		return response, nil
	}, options)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	return candidates.History.StampPage(packed), nil
}

type compatibilityTranscriptWindowLoader struct {
	source     appsource.Source
	readParams appwire.ThreadReadParams
	read       appwire.ThreadReadResponse
	identity   appitempaging.CursorIdentity
	history    appsource.HistoryIdentity
	turns      []appwire.Turn
	cursor     string
	seen       map[string]bool
	pageCalls  int
}

func newCompatibilityTranscriptWindowLoader(
	ctx context.Context,
	source appsource.Source,
	readParams appwire.ThreadReadParams,
	read appwire.ThreadReadResponse,
) (*compatibilityTranscriptWindowLoader, error) {
	result, err := compatibilityItemCandidateResultForRead(ctx, source, readParams, read)
	if err != nil {
		return nil, err
	}
	return &compatibilityTranscriptWindowLoader{
		source: source, readParams: readParams, read: read,
		identity: result.Identity, history: result.History,
		turns: append([]appwire.Turn(nil), read.Thread.Turns...), cursor: read.OlderCursor,
		seen: make(map[string]bool),
	}, nil
}

func (l *compatibilityTranscriptWindowLoader) load(ctx context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
	for {
		selected, resolved := resolveCompatibilityTurnWindow(l.turns, selector, l.cursor == "")
		if resolved {
			selectedRead := l.read
			selectedRead.Thread.Turns = selected
			selectedRead.OlderCursor = ""
			result, err := compatibilityItemCandidateResultForRead(ctx, l.source, l.readParams, selectedRead)
			if err != nil {
				return transcriptWindowLoadedRange{}, err
			}
			// The legacy page conversion reports only positioned candidates, so a
			// selected but empty turn disappears without its shell and recovery
			// metadata. Stamp the resolved selection back onto the result before
			// the packer decides what it served.
			result = normalizeTranscriptWindowLoadedResult(result, selectedRead.Thread)
			result.Identity = l.identity
			result.History = l.history
			result.Exhausted = true
			return transcriptWindowLoadedRange{Result: result, Thread: selectedRead.Thread}, nil
		}
		if l.cursor == "" || l.seen[l.cursor] || l.pageCalls >= hubCompatFetchPageLimit {
			return transcriptWindowLoadedRange{}, appwire.TranscriptItemCursorStale()
		}
		l.seen[l.cursor] = true
		page, err := l.source.ListTurns(ctx, appwire.ThreadTurnsListParams{
			Ref: l.readParams.Ref, ThreadID: l.readParams.ThreadID, Cursor: l.cursor,
			ItemsView: l.readParams.ItemsView, ItemLimit: appwire.TranscriptItemPageLimit,
		})
		l.pageCalls++
		if err != nil {
			return transcriptWindowLoadedRange{}, err
		}
		l.turns = append(append([]appwire.Turn(nil), page.Data...), l.turns...)
		l.cursor = page.NextCursor
	}
}

func resolveCompatibilityTurnWindow(turns []appwire.Turn, window *appwire.ThreadTurnWindow, exhausted bool) ([]appwire.Turn, bool) {
	if window == nil {
		return nil, false
	}
	type turnRun struct {
		id         string
		start, end int
	}
	runs := make([]turnRun, 0, len(turns))
	for i := range turns {
		if turns[i].ID != "" && len(runs) > 0 && runs[len(runs)-1].id == turns[i].ID {
			runs[len(runs)-1].end = i + 1
			continue
		}
		runs = append(runs, turnRun{id: turns[i].ID, start: i, end: i + 1})
	}
	selectRuns := func(start, end int) ([]appwire.Turn, bool) {
		if start < 0 || start > end || end > len(runs) {
			return nil, false
		}
		if start == end {
			return []appwire.Turn{}, true
		}
		first := runs[start].start
		if first == 0 && turns[first].HasEarlierItems && !exhausted {
			return nil, false
		}
		return turns[first:runs[end-1].end], true
	}
	if window.Latest > 0 {
		if len(runs) < window.Latest && !exhausted {
			return nil, false
		}
		return selectRuns(max(0, len(runs)-window.Latest), len(runs))
	}
	boundaryID := window.Turn
	if boundaryID == "" {
		boundaryID = window.Since
	}
	if boundaryID == "" && window.Before != nil {
		boundaryID = window.Before.TurnID
	}
	boundary := -1
	for i := range runs {
		if runs[i].id == boundaryID {
			boundary = i
			break
		}
	}
	if boundary < 0 {
		return nil, false
	}
	if window.Turn != "" {
		return selectRuns(boundary, boundary+1)
	}
	if window.Since != "" {
		return selectRuns(boundary+1, min(len(runs), boundary+1+window.Limit))
	}
	start := max(0, boundary-window.Before.Limit)
	if boundary-start < window.Before.Limit && !exhausted {
		return nil, false
	}
	return selectRuns(start, boundary)
}

func (o *sessionOperations) activity(ctx context.Context, p appwire.SessionActivityRequest) (appwire.SessionActivityResponse, error) {
	var result appwire.SessionActivityResponse
	switch p.Resource {
	case appwire.SessionActivityResourceSummary:
		v, err := readSessionActivity(ctx, o.cfg, o.sources, p.Ref, func(s appsource.Source) (appwire.SessionActivitySummary, error) {
			return s.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: p.Ref, Scope: p.Scope})
		}, func(e hubcore.PastEntry) (appwire.SessionActivitySummary, error) {
			return loadRetainedActivitySummary(ctx, e, p)
		})
		result.Summary = &v
		return result, err
	case appwire.SessionActivityResourceDelegates:
		v, err := readSessionActivity(ctx, o.cfg, o.sources, p.Ref, func(s appsource.Source) (appwire.SessionDelegatesResponse, error) {
			return s.ThreadDelegatesList(ctx, appwire.SessionActivityListParams{Ref: p.Ref, Scope: p.Scope, Cursor: p.Cursor, Limit: p.Limit})
		}, func(e hubcore.PastEntry) (appwire.SessionDelegatesResponse, error) {
			return loadRetainedActivityDelegates(ctx, e, p)
		})
		result.Delegates = &v
		return result, err
	case appwire.SessionActivityResourceJobs:
		v, err := readSessionActivity(ctx, o.cfg, o.sources, p.Ref, func(s appsource.Source) (appwire.SessionJobsResponse, error) {
			return s.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: p.Ref, Scope: p.Scope, Cursor: p.Cursor, Limit: p.Limit})
		}, func(e hubcore.PastEntry) (appwire.SessionJobsResponse, error) {
			return loadRetainedActivityJobs(ctx, e, p)
		})
		result.Jobs = &v
		return result, err
	case appwire.SessionActivityResourceWatches:
		v, err := readSessionActivity(ctx, o.cfg, o.sources, p.Ref, func(s appsource.Source) (appwire.SessionWatchesResponse, error) {
			return s.ThreadWatchesList(ctx, appwire.SessionActivityListParams{Ref: p.Ref, Scope: p.Scope, Cursor: p.Cursor, Limit: p.Limit})
		}, func(e hubcore.PastEntry) (appwire.SessionWatchesResponse, error) {
			return loadRetainedActivityWatches(ctx, e, p)
		})
		result.Watches = &v
		return result, err
	}
	return result, appwire.InvalidParams("unsupported activity resource")
}

func retainedActivityParams(e hubcore.PastEntry, p appwire.SessionActivityRequest) appwire.SessionActivityListParams {
	return appwire.SessionActivityListParams{Ref: appwire.Ref{SourceID: "local", ThreadID: e.Meta.ID}.String(), Scope: p.Scope, Cursor: p.Cursor, Limit: p.Limit}
}
func loadRetainedActivitySummary(ctx context.Context, e hubcore.PastEntry, p appwire.SessionActivityRequest) (appwire.SessionActivitySummary, error) {
	return agent.LoadSessionActivitySummary(ctx, e.StateDir, e.Meta.ID, appwire.SessionActivityReadParams{Ref: appwire.Ref{SourceID: "local", ThreadID: e.Meta.ID}.String(), Scope: p.Scope})
}
func loadRetainedActivityDelegates(ctx context.Context, e hubcore.PastEntry, p appwire.SessionActivityRequest) (appwire.SessionDelegatesResponse, error) {
	return agent.LoadSessionActivityDelegates(ctx, e.StateDir, e.Meta.ID, retainedActivityParams(e, p))
}
func loadRetainedActivityJobs(ctx context.Context, e hubcore.PastEntry, p appwire.SessionActivityRequest) (appwire.SessionJobsResponse, error) {
	return agent.LoadSessionActivityJobs(ctx, e.StateDir, e.Meta.ID, retainedActivityParams(e, p))
}
func loadRetainedActivityWatches(ctx context.Context, e hubcore.PastEntry, p appwire.SessionActivityRequest) (appwire.SessionWatchesResponse, error) {
	return agent.LoadSessionActivityWatches(ctx, e.StateDir, e.Meta.ID, retainedActivityParams(e, p))
}

func (o *sessionOperations) taskInstance(ctx context.Context, ref string) (string, error) {
	read, err := o.read(ctx, appwire.ThreadReadParams{Ref: ref}, false)
	if err != nil {
		return "", err
	}
	if read.Thread.Evener.Ref != ref {
		return "", appwire.SessionUnavailable("task owner identity unavailable")
	}
	return read.Thread.Evener.InstanceID, nil
}

func (o *sessionOperations) tasks(ctx context.Context, p appwire.SessionTasksRequest) (appwire.SessionTasksResponse, error) {
	var result appwire.SessionTasksResponse
	first, err := o.taskInstance(ctx, p.Ref)
	if err != nil {
		return result, err
	}
	full, err := hubTasksList(ctx, o.cfg, o.sources, appwire.TaskListParams{Ref: p.Ref})
	if err != nil {
		return result, err
	}
	last, err := o.taskInstance(ctx, p.Ref)
	if err != nil {
		return result, err
	}
	if first == "" || last == "" || first != last {
		if p.Offset > 0 || p.Snapshot != "" {
			return result, appwire.SessionTaskSnapshotChanged()
		}
		return result, appwire.SessionUnavailable("task snapshot unavailable; retry from offset 0")
	}
	if full.Data == nil {
		if p.Offset > 0 || p.Snapshot != "" {
			return result, appwire.SessionTaskSnapshotChanged()
		}
		return result, nil
	}
	identity := struct {
		Ref        string
		InstanceID string
		Data       []appwire.Task
	}{p.Ref, first, full.Data}
	raw, err := json.Marshal(identity)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(raw)
	result.Snapshot = hex.EncodeToString(sum[:])
	if p.Snapshot != "" && p.Snapshot != result.Snapshot {
		return appwire.SessionTasksResponse{}, appwire.SessionTaskSnapshotChanged()
	}
	result.Data = []appwire.Task{}
	limit := p.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if p.Offset >= len(full.Data) {
		return result, nil
	}
	// A nonempty data array differs from the empty envelope only by the
	// encoded task values and the commas between them. NextOffset is set
	// after this cutoff, just as in the accumulated-response encoding.
	empty, err := json.Marshal(result)
	if err != nil {
		return appwire.SessionTasksResponse{}, err
	}
	pageBytes := len(empty)
	end := min(len(full.Data), p.Offset+limit)
	for i := p.Offset; i < end; i++ {
		encoded, e := json.Marshal(full.Data[i])
		if e != nil {
			return appwire.SessionTasksResponse{}, e
		}
		nextBytes := pageBytes + len(encoded)
		if len(result.Data) > 0 {
			nextBytes++ // comma between task objects
		}
		if nextBytes > managerTaskPageMaxJSONBytes {
			if len(result.Data) == 0 {
				return appwire.SessionTasksResponse{}, appwire.InvalidRequest("task exceeds bounded result; read the task on its owning session")
			}
			break
		}
		result.Data = append(result.Data, full.Data[i])
		pageBytes = nextBytes
	}
	if next := p.Offset + len(result.Data); next < len(full.Data) {
		result.NextOffset = &next
	}
	return result, nil
}

func managerReadWithinCap(response appwire.SessionOperationResponse) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(raw) > managerReadMaxJSONBytes {
		return appwire.InvalidRequest("Manager read exceeds bounded result; no data returned")
	}
	return nil
}
