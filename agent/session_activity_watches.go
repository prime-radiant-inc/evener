package agent

import (
	"context"
	"slices"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// ListActivityWatches pages receiver-owned watches, including child-held sources.
func (s *Session) ListActivityWatches(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	_, scope, limit, err := normalizeSessionActivity(params.Ref, params.Scope, params.Limit)
	if err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	params.Scope = scope
	params.Limit = limit
	read, err := s.activityRead(ctx, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
	if err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	defer read.index.release()
	return read.watchesPage(ctx, params)
}

// LoadSessionActivityWatches distinguishes retained registration from proven armed state.
func LoadSessionActivityWatches(ctx context.Context, stateDir, sessionID string, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	_, scope, limit, err := normalizeSessionActivity(params.Ref, params.Scope, params.Limit)
	if err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	params.Scope = scope
	params.Limit = limit
	read, err := retainedActivityRead(ctx, stateDir, sessionID, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
	if err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	defer read.index.release()
	return read.watchesPage(ctx, params)
}
func (read *sessionActivityRead) watchesPage(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	result := appwire.SessionWatchesResponse{Context: read.context, Scope: read.scope}
	token, walk, err := read.index.token(params, appwire.SessionActivityResourceWatches, read.context.SessionID)
	if err != nil {
		return result, err
	}
	complete, err := read.prepareSources(ctx, walk, true)
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
		_, walk.Highwater, _ = read.nextKey(walk, sessionActivityKey{}, true)
		walk.Ready = true
	}
	controller := read.index.controller
	if controller != nil {
		controller.mu.Lock()
	}
	owners := read.owners()
	if controller != nil {
		controller.mu.Unlock()
	}
	for read.budget > 0 && len(result.Watches) < params.Limit {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sourceOwner, key, found := read.nextKey(walk, token.After, true)
		if !found {
			result.Page.Complete = true
			break
		}
		read.budget--
		source := read.index.jobs[sourceOwner]
		record := source.Watches[key.ID]
		if record == nil || (!record.Active && !slices.Contains(source.Ended, key.ID)) || source.CreationOffsets["watch:"+key.ID] > walk.Cutoffs[sourceOwner] || walk.Highwater.before(key) {
			token.After = key
			continue
		}
		receiver := record.ReceiverSessionID
		if receiver == "" {
			receiver = sourceOwner
		}
		if !owners[receiver] {
			token.After = key
			continue
		}
		row := projectRetainedSessionWatch(sourceOwner, receiver, record, source)
		if runtime := read.runtime(sourceOwner); runtime != nil && runtime.jobManager != nil {
			jm := runtime.jobManager
			jm.mu.Lock()
			for _, cfg := range jm.watches {
				if cfg != nil && cfg.id == key.ID {
					row.Watch = watchInfoFromStatus(watchStatusInfoFromConfig(cfg))
					row.State = appwire.SessionWatchStateArmed
					if !row.Watch.Active {
						row.State = appwire.SessionWatchStateEnded
					}
					break
				}
			}
			for _, history := range jm.watchHistory {
				if history.id == key.ID {
					row.Watch.Deliveries = max(row.Watch.Deliveries, history.deliveries)
					row.Watch.EndReason = truncateActivityText(history.endReason, activityMaxDelegateProseRunes)
					row.Watch.Active = false
					row.State = appwire.SessionWatchStateEnded
				}
			}
			jm.mu.Unlock()
		}
		result.Watches = append(result.Watches, row)
		if !pageBudget.fits(row, result) {
			result.Watches = result.Watches[:len(result.Watches)-1]
			if len(result.Watches) == 0 {
				return result, appwire.Unavailable("session activity row exceeds response budget")
			}
			break
		}
		token.After = key
	}
	if !result.Page.Complete {
		if _, _, found := read.nextKey(walk, token.After, true); !found {
			result.Page.Complete = true
		} else {
			result.Page.NextCursor = read.index.encode(token)
		}
	}
	result.Page.Complete = result.Page.Complete && len(result.Page.Issues) == 0
	return result, nil
}
func projectRetainedSessionWatch(owner, receiver string, record *jobstore.WatchRecord, index *sessionActivityJobIndex) appwire.SessionWatch {
	cfg := &watchConfig{id: record.WatchID, target: record.Target, sourcePublic: record.Source, createdAt: index.Created[record.WatchID]}
	if snapshot := index.Configs[record.WatchID]; snapshot != nil {
		cfg.target = snapshot.Target
		cfg.outputMatch = truncateActivityText(snapshot.OutputMatch, activityMaxDelegateProseRunes)
		cfg.progressIntervalMS = snapshot.ProgressIntervalMS
		cfg.note = truncateActivityText(snapshot.Note, activityMaxDelegateProseRunes)
		cfg.events = snapshot.Events
		cfg.triggerEvery = snapshot.Every
		cfg.wildcardEvents = slices.Contains(snapshot.Events, "*")
		if filter := snapshot.EventFilter; filter != nil {
			cfg.eventFilter = &watchEventFilter{ToolName: filter.ToolName, Status: filter.Status}
		}
		if snapshot.AfterSeconds > 0 {
			cfg.timer = true
			cfg.oneShot = true
			cfg.timerSeconds = snapshot.AfterSeconds
		} else if snapshot.RepeatSeconds > 0 {
			cfg.timer = true
			cfg.timerSeconds = snapshot.RepeatSeconds
		}
	}
	info := watchInfoFromStatus(watchStatusInfoFromConfig(cfg))
	info.SendTo = truncateActivityText(record.SendTo, activityMaxLabelRunes)
	info.Active = false
	info.EndReason = truncateActivityText(record.EndReason, activityMaxDelegateProseRunes)
	info.Deliveries = max(record.Deliveries, index.DeliveryCounts[record.WatchID])
	info.DeliveryTimes = append([]string(nil), index.DeliveryTimes[record.WatchID]...)
	for i := range info.Cadence {
		info.Cadence[i].DerivedNextFireAt = ""
	}
	state := appwire.SessionWatchStateUnknown
	if !record.Active {
		state = appwire.SessionWatchStateEnded
	}
	return appwire.SessionWatch{OwnerRef: encodeRef("", receiver), ReceiverRef: encodeRef("", receiver), State: state, Watch: info}
}
func watchInfoFromStatus(status WatchStatusInfo) appwire.EvenerWatchInfo {
	result := appwire.EvenerWatchInfo{ID: status.ID, Source: truncateActivityText(status.Source, activityMaxLabelRunes), Target: truncateActivityText(status.Target, activityMaxLabelRunes), SendTo: truncateActivityText(status.SendTo, activityMaxLabelRunes), Note: truncateActivityText(status.Note, activityMaxDelegateProseRunes), OutputMatch: truncateActivityText(status.OutputMatch, activityMaxDelegateProseRunes), Events: append([]string(nil), status.Events...), WildcardEvents: status.WildcardEvents, Deliveries: status.Deliveries, DeliveryTimes: append([]string(nil), status.DeliveryTimes...), CreatedAt: status.CreatedAt, Active: status.Active, EndReason: truncateActivityText(status.EndReason, activityMaxDelegateProseRunes)}
	for _, cadence := range status.Cadence {
		result.Cadence = append(result.Cadence, appwire.EvenerWatchCadence{Kind: cadence.Kind, Seconds: cadence.Seconds, DerivedNextFireAt: cadence.DerivedNextFireAt, Every: cadence.Every, Filter: truncateActivityText(cadence.Filter, activityMaxLabelRunes)})
	}
	return result
}
