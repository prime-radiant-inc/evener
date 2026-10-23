package hub

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/identifier"
)

func validSessionWatch(request appwire.SessionWatchRequest) error {
	if !request.Completion && len(request.Events) == 0 {
		return appwire.InvalidParams("events or completion is required")
	}
	for _, event := range request.Events {
		switch event {
		case "status", "question", "approval", "communicate":
		default:
			return appwire.InvalidParams(fmt.Sprintf("unsupported observation selector %q", event))
		}
	}
	return nil
}

func sessionWatchSelected(request appwire.SessionWatchRequest, event string) bool {
	for _, selected := range request.Events {
		if selected == event {
			return true
		}
	}
	return false
}

func observationMetadata(item appwire.ThreadItem, ref, instance, generation string) appwire.SessionObservationMetadata {
	m := *item.Observation
	m.Ref, m.InstanceID, m.GenerationID = ref, instance, generation
	if m.EntryID == "" {
		m.EntryID = item.TranscriptKey
		if m.EntryID == "" {
			m.EntryID = item.ID
		}
	}
	return m
}

// recordedObservationUpdates consumes the recorded projection, including the
// acceptance verdict already attached by Task 4a. Status cannot substitute for
// the terminal winner; missing acceptance is not completion.
func recordedObservationUpdates(items []appwire.ThreadItem, request appwire.SessionWatchRequest, ref, instance, generation string) []appwire.SessionWatchUpdate {
	out := []appwire.SessionWatchUpdate{}
	for _, item := range items {
		if item.Observation == nil {
			continue
		}
		m := observationMetadata(item, ref, instance, generation)
		if sessionWatchSelected(request, "communicate") || request.Completion || !m.CausalKnown {
			out = append(out, appwire.SessionWatchUpdate{Event: "communicate", Metadata: m})
		}
	}
	return out
}

func (b *boundSessionOperations) Watch(ctx context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	// A remote AppWire subscriber stream is not the local observation lease.
	// Fail this unsupported target before asking for a snapshot or a lease; the
	// local watch's admission and handoff order below remains unchanged.
	if target, err := appwire.ParseRef(request.Ref); err == nil {
		if source, err := b.owner.sources.SourceForRef(target.String()); err == nil {
			if _, remote := source.(*appsource.RemoteHubSource); remote {
				return nil, appwire.Unavailable("remote job_watch is unsupported: the remote observation lease is unavailable; reads, status, input and approval remain available")
			}
		}
	}
	view, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	// Admission is the caller's own start-time Manager property, projected by
	// its daemon; no hub preference decides it.
	if !view.Manager {
		return nil, appwire.InvalidRequest("session is not a Manager")
	}
	if err := validSessionWatch(request); err != nil {
		return nil, err
	}
	ref, err := appwire.ParseRef(request.Ref)
	if err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	source, err := b.owner.sources.SourceForRef(ref.String())
	if err != nil {
		return nil, err
	}
	relaySource, ok := source.(appsource.RelaySessionSource)
	if !ok {
		return nil, appwire.Unavailable("remote job_watch is unsupported: the remote observation lease is unavailable; reads, status, input and approval remain available")
	}
	watchCtx, cancel := context.WithCancel(ctx)
	var lease appsource.RelaySessionLease
	cleanup := func() {
		cancel()
		if lease != nil {
			lease.Close()
		}
	}
	current, err := b.State(watchCtx)
	if err != nil {
		cleanup()
		return nil, err
	}
	if !current.Manager || current.Ref != view.Ref || current.InstanceID != view.InstanceID {
		cleanup()
		return nil, appwire.InvalidRequest("Manager caller changed during watch registration")
	}
	canonical, err := relaySource.ResolveRelaySession(appwire.ThreadReadParams{Ref: ref.String()})
	if err != nil {
		cleanup()
		return nil, err
	}
	lease, err = relaySource.AcquireRelaySession(canonical)
	if err != nil {
		cleanup()
		return nil, err
	}
	var notifications <-chan appsource.RelayDelivery
	params := appwire.ThreadReadParams{Ref: ref.String(), IncludeTurns: request.Completion || sessionWatchSelected(request, "communicate")}
	read, err := b.owner.readWithHandoff(watchCtx, params, false, func() error { var listenErr error; notifications, listenErr = lease.Listen(watchCtx); return listenErr })
	if err != nil || notifications == nil {
		cleanup()
		if err == nil {
			err = appwire.SessionUnavailable("thread has no live snapshot handoff")
		}
		return nil, err
	}
	updates := make(chan appwire.SessionWatchUpdate, 32)
	initial := appwire.SessionWatchUpdate{Snapshot: &read, Metadata: appwire.SessionObservationMetadata{Ref: read.Thread.Evener.Ref, InstanceID: read.Thread.Evener.InstanceID, GenerationID: read.BootGeneration}}
	updates <- initial
	// Live notifications carry no identity of their own, so each one needs a
	// manufactured entry ID. A plain counter is not an identity: the watcher
	// dedups on instance/generation/entry/event and keeps that memory across a
	// reconnect, so the first question after a rebind would be numbered the same
	// as the first one before it and dropped as already seen. A per-subscription
	// prefix keeps every manufactured ID distinct from every other subscription's,
	// so a rebind can only repeat an ID it actually re-sends.
	subscription, err := identifier.NewWatchGeneration()
	if err != nil {
		cleanup()
		return nil, err
	}
	go func() {
		defer close(updates)
		defer cleanup()
		var notificationSeq uint64
		send := func(u appwire.SessionWatchUpdate) bool {
			select {
			case updates <- u:
				return true
			case <-watchCtx.Done():
				return false
			}
		}
		for _, turn := range read.Thread.Turns {
			for _, u := range recordedObservationUpdates(turn.Items, request, ref.String(), read.Thread.Evener.InstanceID, read.BootGeneration) {
				if !send(u) {
					return
				}
			}
		}
		for {
			select {
			case <-watchCtx.Done():
				return
			case delivery, open := <-notifications:
				if !open {
					return
				}
				notification := delivery.Notification
				delivery.Acknowledge()
				current, err := b.State(watchCtx)
				if err != nil || !current.Manager || current.Ref != view.Ref || current.InstanceID != view.InstanceID {
					return
				}
				base := appwire.SessionObservationMetadata{Ref: ref.String(), InstanceID: read.Thread.Evener.InstanceID, GenerationID: read.BootGeneration}
				if notification.Method == appwire.NotifyHistoryUpdated {
					var history appwire.HistoryUpdatedParams
					if json.Unmarshal(notification.Params, &history) != nil {
						continue
					}
					for _, u := range recordedObservationUpdates(history.Items, request, ref.String(), base.InstanceID, base.GenerationID) {
						if !send(u) {
							return
						}
					}
					continue
				}
				event := ""
				switch notification.Method {
				case appwire.NotifyThreadStatusChanged:
					event = "status"
					var status appwire.ThreadStatusChangedParams
					if json.Unmarshal(notification.Params, &status) == nil && status.AskPending != nil && *status.AskPending && sessionWatchSelected(request, "question") {
						notificationSeq++
						question := base
						question.EntryID = fmt.Sprintf("question-%s-%d", subscription, notificationSeq)
						question.CausalKnown = true
						if !send(appwire.SessionWatchUpdate{Event: "question", Notification: &notification, Metadata: question}) {
							return
						}
					}
				case appwire.NotifyEvenerThreadResync, appwire.NotifyThreadClosed:
					event = "status"
				case appwire.NotifyEvenerSandboxEscalationRequested:
					event = "approval"
				}
				if event == "" || !sessionWatchSelected(request, event) {
					continue
				}
				notificationSeq++
				base.EntryID = fmt.Sprintf("%s-%s-%d", notification.Method, subscription, notificationSeq)
				base.CausalKnown = true
				if !send(appwire.SessionWatchUpdate{Event: event, Notification: &notification, Metadata: base}) {
					return
				}
			}
		}
	}()
	return updates, nil
}
