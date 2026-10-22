package hub

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
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
	view, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	if !view.Setting.Enabled || view.Setting.HubID != b.binding.HubID {
		return nil, appwire.InvalidRequest("Manager mode is not enabled at this hub")
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
		return nil, appwire.Unavailable("source has no canonical observation")
	}
	key, err := managerPreferenceRef(b.owner.cfg, appwire.Thread{Evener: appwire.EvenerThread{Ref: view.Ref, InstanceID: view.InstanceID}})
	if err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	observation := &sessionObservation{preferenceKey: key, cancel: cancel}
	b.owner.mu.Lock()
	if b.owner.watches == nil {
		b.owner.watches = map[*sessionObservation]struct{}{}
	}
	b.owner.watches[observation] = struct{}{}
	b.owner.mu.Unlock()
	var lease appsource.RelaySessionLease
	cleanup := func() {
		cancel()
		if lease != nil {
			lease.Close()
		}
		b.owner.mu.Lock()
		delete(b.owner.watches, observation)
		b.owner.mu.Unlock()
	}
	current, err := b.State(watchCtx)
	if err != nil {
		cleanup()
		return nil, err
	}
	if !current.Setting.Enabled || current.Setting.HubID != view.Setting.HubID || current.Setting.Revision != view.Setting.Revision {
		cleanup()
		return nil, appwire.InvalidRequest("Manager setting changed during watch registration")
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
				if err != nil || !current.Setting.Enabled || current.Setting.HubID != view.Setting.HubID || current.Setting.Revision != view.Setting.Revision {
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
						question.EntryID = fmt.Sprintf("question-%d", notificationSeq)
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
				base.EntryID = fmt.Sprintf("%s-%d", notification.Method, notificationSeq)
				base.CausalKnown = true
				if !send(appwire.SessionWatchUpdate{Event: event, Notification: &notification, Metadata: base}) {
					return
				}
			}
		}
	}()
	return updates, nil
}
