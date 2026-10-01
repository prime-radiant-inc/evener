package agent

import (
	"context"
	"errors"
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
				sourceErr := sessionActivitySourceReadError("session job source unavailable", err)
				if !read.excludeUnavailableSource(walk, owner, sourceErr) {
					return false, sourceErr
				}
				continue
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
		if !walk.UnavailableSources[owner] {
			ready, err := read.advanceJobs(ctx, owner)
			if err != nil && !read.excludeUnavailableSource(walk, owner, err) {
				return false, err
			}
			if err == nil && !ready {
				return false, nil
			}
		}
		walk.SourcePosition++
		read.index.progress++
	}
	walk.SourcePosition = 0
	return true, nil
}

// Independent journals remain readable when a sibling is unavailable. A walk
// keeps that source excluded; a fresh root read can admit it after recovery.
// Context cancellation and source-incarnation changes still invalidate the read.
func (read *sessionActivityRead) excludeUnavailableSource(walk *sessionActivityWalk, owner string, err error) bool {
	var wire appwire.WireError
	if len(walk.Owners) < 2 || !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		return false
	}
	if walk.UnavailableSources == nil {
		walk.UnavailableSources = make(map[string]bool)
	}
	walk.UnavailableSources[owner] = true
	walk.Issues = append(walk.Issues, appwire.SessionActivityIssue{Ref: encodeRef("", owner), Code: "unavailable"})
	if source := read.index.jobs[owner]; source != nil {
		source.Complete = false
	}
	return true
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
		index.Established = true
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
			// Failed scans do not publish cursor usage. Reserve the remaining
			// allowance so another source cannot exceed this request's budget.
			read.bytes = 0
			read.budget = 0
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, sessionActivitySourceReadError("retained job journal unavailable", scanErr)
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
		if err := read.acceptJobPage(path, index, cursor, events, complete); err != nil {
			return false, err
		}
		if !complete && used == 0 && len(events) == 0 {
			return false, sessionActivitySourceUnavailable("retained job journal append incomplete")
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
				creationKey := sessionActivityCreationKey(at, event.JobID)
				creationKey.SourceSessionID = owner
				index.JobKeys = insertSessionActivityKey(index.JobKeys, creationKey)
			}
		}
		if event.Kind == jobstore.EventWatchRegistered && index.Watches[event.WatchID] != nil {
			key := "watch:" + event.WatchID
			if _, exists := index.CreationOffsets[key]; !exists {
				index.CreationOffsets[key] = index.PendingEnds[i]
				index.Created[event.WatchID] = event.TS
				creationKey := sessionActivityCreationKey(event.TS, event.WatchID)
				creationKey.SourceSessionID = owner
				index.WatchKeys = insertSessionActivityKey(index.WatchKeys, creationKey)
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
	index.Established = index.Established || index.Complete
	index.Version = version
	index.Pending = nil
	index.PendingEnds = nil
	index.PendingPosition = 0
	return index.Complete, nil
}

// Summary demand catches up only sources whose complete authority was already
// established. Source replacement retires that eligibility with the index.
func (read *sessionActivityRead) refreshWarmSources(ctx context.Context, owners map[string]bool, pageBudget *sessionActivityPageBudget) (bool, []appwire.SessionActivityIssue, error) {
	var ordered []string
	for owner := range owners {
		source := read.index.jobs[owner]
		if source == nil || !source.Established || source.Complete && source.Version == read.index.revision.Load() {
			continue
		}
		ordered = append(ordered, owner)
	}
	if len(ordered) == 0 {
		return false, nil, nil
	}
	sort.Strings(ordered)
	// Start after the previous admitted source so a growing journal cannot
	// consume every summary budget while another established source waits.
	start := sort.SearchStrings(ordered, read.index.summaryOwner)
	if start < len(ordered) && ordered[start] == read.index.summaryOwner {
		start++
	}
	ordered = append(ordered[start:], ordered[:start]...)
	walk := &sessionActivityWalk{}
	for owner := range owners {
		walk.Owners = append(walk.Owners, owner)
	}
	pending := false
	for _, owner := range ordered {
		source := read.index.jobs[owner]
		if source == nil || !source.Established || source.Complete && source.Version == read.index.revision.Load() {
			continue
		}
		if read.budget == 0 || read.bytes <= 128 {
			pending = true
			break
		}
		// Reserve a possible failure before probing; admit its bytes only if
		// this source is actually unavailable. The cursor stays at the last
		// attempted owner so the next pass starts with the unvisited source.
		candidate := *pageBudget
		issue := appwire.SessionActivityIssue{Ref: encodeRef("", owner), Code: "unavailable"}
		if !candidate.fits(issue, nil) {
			if pageBudget.rows == 0 {
				return false, nil, appwire.Unavailable("session activity summary context and issue exceed response budget")
			}
			pending = true
			break
		}
		read.index.summaryOwner = owner
		complete, err := read.advanceJobs(ctx, owner)
		if err != nil {
			// Source checks can fail before journal scanning spends allowance.
			// Failed scans already consume the remaining request budget.
			read.budget = max(0, read.budget-1)
			var wire appwire.WireError
			if errors.As(err, &wire) && read.context.Epoch != read.index.epoch {
				return false, nil, nil
			}
			if read.excludeUnavailableSource(walk, owner, err) {
				*pageBudget = candidate
				continue
			}
			return false, nil, err
		}
		pending = pending || !complete
	}
	return pending, walk.Issues, nil
}

func (read *sessionActivityRead) acceptJobPage(path string, index *sessionActivityJobIndex, cursor jobstore.PageCursor, events []jobstore.Event, complete bool) error {
	// A failed fingerprint capture must leave the accepted scanner/fold boundary intact.
	if err := read.captureTail(path, &index.Source, cursor.Journal.Offset); err != nil {
		return err
	}
	index.Cursor = cursor
	index.Complete = false
	index.Pending = events
	index.PendingEnds = cursor.EventEnds
	index.PendingComplete = complete
	index.PendingPosition = 0
	return nil
}

// nextKey visits only the next bounded candidates without sorting historical rows.
func (read *sessionActivityRead) nextKey(walk *sessionActivityWalk, after sessionActivityKey, watch bool) (string, sessionActivityKey, bool) {
	var selected sessionActivityKey
	selectedOwner := ""
	found := false
	for _, owner := range walk.Owners {
		source := read.index.jobs[owner]
		if source == nil || walk.UnavailableSources[owner] {
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
	result.Page.Issues = append([]appwire.SessionActivityIssue(nil), walk.Issues...)
	pageBudget := newSessionActivityPageBudget(result)
	if pageBudget.bytes > sessionActivityPageBytes-2048 {
		return result, appwire.Unavailable("session activity context and issues exceed response budget")
	}
	if !complete {
		result.Page.NextCursor = read.index.encode(token)
		return result, nil
	}
	if !walk.Ready {
		_, walk.Highwater, _ = read.nextKey(walk, sessionActivityKey{}, false)
		walk.Ready = true
	}
	for read.budget > 0 && len(result.Jobs) < params.Limit {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		owner, key, found := read.nextKey(walk, token.After, false)
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
		if !pageBudget.fits(row, result) {
			result.Jobs = result.Jobs[:len(result.Jobs)-1]
			if len(result.Jobs) == 0 {
				return result, appwire.Unavailable("session activity row exceeds response budget")
			}
			break
		}
		token.After = key
	}
	if !result.Page.Complete {
		if _, _, found := read.nextKey(walk, token.After, false); !found {
			result.Page.Complete = true
		} else {
			result.Page.NextCursor = read.index.encode(token)
		}
	}
	result.Page.Complete = result.Page.Complete && len(result.Page.Issues) == 0
	return result, nil
}
