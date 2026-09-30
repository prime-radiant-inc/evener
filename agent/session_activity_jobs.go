package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// ListActivityJobs pages shell jobs without reading their output.
func (s *Session) ListActivityJobs(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	_, scope, limit, err := normalizeSessionActivity(params.Ref, params.Scope, params.Limit)
	if err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	params.Scope = scope
	params.Limit = limit
	read, err := s.activityRead(ctx, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
	if err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	defer read.index.release()
	return read.jobsPage(ctx, params)
}

// LoadSessionActivityJobs pages fully folded retained shell state.
func LoadSessionActivityJobs(ctx context.Context, stateDir, sessionID string, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	_, scope, limit, err := normalizeSessionActivity(params.Ref, params.Scope, params.Limit)
	if err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	params.Scope = scope
	params.Limit = limit
	read, err := retainedActivityRead(ctx, stateDir, sessionID, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
	if err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	defer read.index.release()
	return read.jobsPage(ctx, params)
}
func (read *sessionActivityRead) runtime(id string) *Session {
	if read.root == nil {
		return nil
	}
	if id == read.rootID {
		return read.root
	}
	controller := read.index.controller
	if controller == nil {
		return nil
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	delegateID := controller.delegateOwnerOfSessionLocked(id)
	if live := controller.live[delegateID]; live != nil {
		return live.runtime
	}
	return nil
}
func (read *sessionActivityRead) prepareSources(ctx context.Context, walk *sessionActivityWalk, watches bool) (bool, error) {
	if read.index.controller == nil {
		complete, err := read.advanceDelegates(ctx)
		if err != nil || !complete {
			return false, err
		}
	}
	if !read.context.AncestryKnown {
		return false, nil
	}
	if !walk.SourcesReady {
		controller := read.index.controller
		if controller != nil {
			controller.mu.Lock()
		}
		owners := read.owners()
		if watches {
			owners = map[string]bool{read.rootID: true}
			for _, row := range read.state() {
				if row != nil {
					owners[row.Descriptor.ChildSessionID] = true
				}
			}
		}
		for owner := range owners {
			walk.Owners = append(walk.Owners, owner)
		}
		if controller != nil {
			controller.mu.Unlock()
		}
		sort.Strings(walk.Owners)
		for _, owner := range walk.Owners {
			path := filepath.Join(jobsDir(read.stateDir, owner), "jobs.jsonl")
			info, err := os.Stat(path)
			if err != nil && !os.IsNotExist(err) {
				return false, appwire.Unavailable("session job source unavailable")
			}
			if info != nil {
				walk.Cutoffs[owner] = info.Size()
			} else {
				walk.Cutoffs[owner] = 0
			}
		}
		walk.SourcesReady = true
	}
	for walk.SourcePosition < len(walk.Owners) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		owner := walk.Owners[walk.SourcePosition]
		ready, err := read.advanceJobs(ctx, owner)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, nil
		}
		walk.SourcePosition++
		read.index.progress++
	}
	walk.SourcePosition = 0
	return true, nil
}
func (read *sessionActivityRead) advanceJobs(ctx context.Context, owner string) (bool, error) {
	index := read.index.jobs[owner]
	if index == nil {
		index = &sessionActivityJobIndex{Jobs: make(map[string]*jobstore.JobRecord), Watches: make(map[string]*jobstore.WatchRecord), Configs: make(map[string]*jobstore.WatchConfigSnapshot), Created: make(map[string]time.Time), CreationOffsets: make(map[string]int64), Delivered: make(map[string]bool), DeliveryTimes: make(map[string][]string), DeliveryCounts: make(map[string]int)}
		read.index.jobs[owner] = index
	}
	path := filepath.Join(jobsDir(read.stateDir, owner), "jobs.jsonl")
	version := read.index.revision.Load()
	if read.bytes <= 128 {
		return false, nil
	}
	read.bytes -= int64(len(index.Source.Tail))
	info, err := read.index.checkSource(path, &index.Source)
	if err != nil {
		return false, err
	}
	if info == nil {
		index.Complete = true
		index.Version = version
		return true, nil
	}
	if index.Complete && index.Cursor.Journal.Offset == info.Size() {
		index.Version = version
		return true, nil
	}
	if read.budget == 0 || read.bytes <= 128 {
		return false, nil
	}
	reserved := false
	if len(index.Pending) == 0 {
		cursor := index.Cursor
		before := cursor.Journal.Offset
		read.index.scanCalls++
		events, complete, scanErr := jobstore.ReadPage(ctx, path, &cursor, read.bytes-128, read.budget)
		if scanErr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, appwire.Unavailable("retained job journal invalid")
		}
		used := cursor.Journal.Offset - before
		if cursor.Journal.Info != nil && !os.SameFile(index.Source.Info, cursor.Journal.Info) {
			read.index.reset()
			return false, appwire.SessionActivityCursorStale()
		}
		if _, sourceErr := read.index.checkSource(path, &index.Source); sourceErr != nil {
			return false, sourceErr
		}
		read.index.rawBytes += uint64(cursor.Journal.ReadBytes)
		read.bytes -= cursor.Journal.ReadBytes + 128
		read.budget -= max(len(events), cursor.Journal.ReadLines)
		read.index.progress += uint64(used) + uint64(len(events))
		index.Cursor = cursor
		index.Complete = false
		index.Pending = events
		index.PendingEnds = cursor.EventEnds
		index.PendingComplete = complete
		index.PendingPosition = 0
		read.index.rawBytes += uint64(captureSessionActivityTail(path, &index.Source, cursor.Journal.Offset))
		if !complete && used == 0 && len(events) == 0 {
			return false, appwire.Unavailable("retained job journal incomplete")
		}
		reserved = true
	}
	for index.PendingPosition < len(index.Pending) {
		i := index.PendingPosition
		event := index.Pending[i]
		if !reserved {
			if read.budget == 0 {
				return false, nil
			}
			read.budget--
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		jobstore.Apply(index.Jobs, index.Watches, event)
		if event.Kind == jobstore.EventJobStarted {
			key := "job:" + event.JobID
			if _, exists := index.CreationOffsets[key]; !exists {
				index.CreationOffsets[key] = index.PendingEnds[i]
				at := event.TS
				if event.StartedAt != nil {
					at = *event.StartedAt
				}
				index.JobKeys = insertSessionActivityKey(index.JobKeys, sessionActivityCreationKey(at, event.JobID))
			}
		}
		if event.Kind == jobstore.EventWatchRegistered && index.Watches[event.WatchID] != nil {
			key := "watch:" + event.WatchID
			if _, exists := index.CreationOffsets[key]; !exists {
				index.CreationOffsets[key] = index.PendingEnds[i]
				index.Created[event.WatchID] = event.TS
				index.WatchKeys = insertSessionActivityKey(index.WatchKeys, sessionActivityCreationKey(event.TS, event.WatchID))
			}
			index.Configs[event.WatchID] = event.Watch.Config
		}
		if event.Kind == jobstore.EventWatchCleared {
			if record := index.Watches[event.WatchID]; record != nil && !record.Active {
				index.Ended = slices.DeleteFunc(index.Ended, func(id string) bool { return id == event.WatchID })
				index.Ended = append(index.Ended, event.WatchID)
				if len(index.Ended) > watchHistoryCap {
					index.Ended = index.Ended[len(index.Ended)-watchHistoryCap:]
				}
			}
		}
		if send := event.WatchSend; event.Kind == jobstore.EventWatchSendDelivered && send != nil && !send.EndNotice {
			identity := send.DeliveryID
			if identity == "" {
				identity = send.Key.WatchID + ":" + send.CreatedAt.Format(time.RFC3339Nano)
			}
			if !index.Delivered[identity] {
				index.Delivered[identity] = true
				id := send.Key.WatchID
				index.DeliveryCounts[id]++
				ring := index.DeliveryTimes[id]
				ring = append(ring, event.TS.UTC().Format(time.RFC3339Nano))
				if len(ring) > 16 {
					ring = ring[len(ring)-16:]
				}
				index.DeliveryTimes[id] = ring
			}
		}
		index.PendingPosition++
	}

	index.Complete = index.PendingComplete
	index.Version = version
	index.Pending = nil
	index.PendingEnds = nil
	index.PendingPosition = 0
	return index.Complete, nil
}

// mergeSourceKeys visits only the next bounded candidates. It does not allocate
// or sort all historical rows to produce one page.
func (read *sessionActivityRead) nextKey(owners []string, after sessionActivityKey, watch bool) (string, sessionActivityKey, bool) {
	var selected sessionActivityKey
	selectedOwner := ""
	found := false
	for _, owner := range owners {
		source := read.index.jobs[owner]
		if source == nil {
			continue
		}
		keys := source.JobKeys
		if watch {
			keys = source.WatchKeys
		}
		position := len(keys) - 1
		if after.ID != "" {
			position = sort.Search(len(keys), func(i int) bool { return !keys[i].before(after) }) - 1
		}
		if position >= 0 && (!found || selected.before(keys[position])) {
			selected = keys[position]
			selectedOwner = owner
			found = true
		}
	}
	return selectedOwner, selected, found
}
func (read *sessionActivityRead) jobsPage(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	result := appwire.SessionJobsResponse{Context: read.context, Scope: read.scope}
	token, walk, err := read.index.token(params, appwire.SessionActivityResourceJobs, read.context.SessionID)
	if err != nil {
		return result, err
	}
	complete, err := read.prepareSources(ctx, walk, false)
	if err != nil {
		return result, err
	}
	if !complete {
		result.Page.NextCursor = read.index.encode(token)
		return result, nil
	}
	if !walk.Ready {
		_, walk.Highwater, _ = read.nextKey(walk.Owners, sessionActivityKey{}, false)
		walk.Ready = true
	}
	for read.budget > 0 && len(result.Jobs) < params.Limit {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		owner, key, found := read.nextKey(walk.Owners, token.After, false)
		if !found {
			result.Page.Complete = true
			break
		}
		read.budget--
		source := read.index.jobs[owner]
		record := source.Jobs[key.ID]
		if record == nil || record.Type != jobstore.JobShell || source.CreationOffsets["job:"+key.ID] > walk.Cutoffs[owner] || walk.Highwater.before(key) {
			token.After = key
			continue
		}
		row := projectActivityJob(record, encodeRef("", owner))
		if runtime := read.runtime(owner); runtime != nil && runtime.jobManager != nil {
			jm := runtime.jobManager
			jm.mu.Lock()
			if running := jm.running[key.ID]; running != nil && running.durableStarted {
				row = projectActivityJob(running.rec, encodeRef("", owner))
				if running.output != nil {
					row.OutputBytes = running.output.Len()
					row.HasOutput = row.OutputBytes > 0 || row.HasOutput
				}
			}
			jm.mu.Unlock()
		}
		row.OwnerSessionID = owner
		row.Description = truncateActivityText(row.Description, activityMaxDelegateProseRunes)
		row.Command = truncateActivityText(row.Command, activityMaxDelegateProseRunes)
		row.Task = truncateActivityText(row.Task, activityMaxDelegateProseRunes)
		row.Reason = truncateActivityText(row.Reason, activityMaxDelegateProseRunes)
		result.Jobs = append(result.Jobs, row)
		raw, _ := json.Marshal(result)
		if len(raw) > sessionActivityPageBytes-2048 {
			result.Jobs = result.Jobs[:len(result.Jobs)-1]
			if len(result.Jobs) == 0 {
				return result, appwire.Unavailable("session activity row exceeds response budget")
			}
			break
		}
		token.After = key
	}
	if !result.Page.Complete {
		if _, _, found := read.nextKey(walk.Owners, token.After, false); !found {
			result.Page.Complete = true
		} else {
			result.Page.NextCursor = read.index.encode(token)
		}
	}
	return result, nil
}
