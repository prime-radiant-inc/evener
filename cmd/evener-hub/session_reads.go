package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A Manager read is deliberately below the runtime tool-result cap. This
// leaves space for the envelope and follow-up arguments. Tasks shrink by
// complete rows, never by clipping task text.
const managerReadMaxJSONBytes = 256 << 10

const managerTaskPageMaxJSONBytes = managerReadMaxJSONBytes - 4096

func (o *sessionOperations) discover(ctx context.Context, params appwire.ThreadListParams) (appwire.SessionDiscovery, error) {
	threads, err := hubThreadList(ctx, o.cfg, o.sources, params)
	if err != nil {
		return appwire.SessionDiscovery{}, err
	}
	discovery := appwire.SessionDiscovery{Threads: threads, Sources: []appwire.SessionSource{{ID: "local"}}}
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
		discovery.Sources = append(discovery.Sources, appwire.SessionSource{ID: host.Name})
		seen[host.Name] = true
	}
	for _, source := range o.sources.All() {
		if !seen[source.ID()] {
			discovery.Sources = append(discovery.Sources, appwire.SessionSource{ID: source.ID()})
			seen[source.ID()] = true
		}
	}
	return discovery, nil
}

func (o *sessionOperations) turns(ctx context.Context, p appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if p.Cursor == "" {
		read, err := o.read(ctx, appwire.ThreadReadParams{Ref: p.Ref, IncludeTurns: true, ItemLimit: p.ItemLimit}, false)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		return appwire.ThreadTurnsListResponse{Data: read.Thread.Turns, NextCursor: read.OlderCursor, BootGeneration: read.BootGeneration, Epoch: read.Epoch, Snapshot: read.Snapshot, Authoritative: read.Authoritative}, nil
	}
	if err := appwire.ValidateThreadTurnsListParams(p); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	source, err := sourceForThreadWithDeletionFence(ctx, o.cfg, o.sources, p.Ref, "")
	if err == nil {
		page, readErr := source.ListTurns(ctx, p)
		if readErr == nil {
			return page, nil
		}
		err = readErr
	}
	if !isDeadSessionError(err) {
		return appwire.ThreadTurnsListResponse{}, err
	}
	page, ok, pastErr := pastThreadTurnsList(ctx, o.cfg, p)
	if pastErr != nil {
		return appwire.ThreadTurnsListResponse{}, pastErr
	}
	if ok {
		return page, nil
	}
	return appwire.ThreadTurnsListResponse{}, err
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
