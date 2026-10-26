package hub

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/appwire"
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
	// These fields scope delivery to the serving subscription. They do not
	// establish the original execution's boot. TurnID and the recorded typed
	// acceptance verdict identify the work, and the snapshot stays unchanged.
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
		if len(m.AnswerToTurnIDs) != 0 {
			continue // Recorded input evidence is not a communicate event.
		}
		if sessionWatchSelected(request, "communicate") || request.Completion || !m.CausalKnown {
			out = append(out, appwire.SessionWatchUpdate{Event: "communicate", Metadata: m})
		}
	}
	return out
}

func observationPositionBefore(a, b appwire.ThreadItemPosition) bool {
	return a.Entry < b.Entry || a.Entry == b.Entry && (a.Item < b.Item || a.Item == b.Item && a.Sub < b.Sub)
}

func oldestObservationPosition(turns []appwire.Turn) *appwire.ThreadItemPosition {
	var oldest *appwire.ThreadItemPosition
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Position != nil && (oldest == nil || observationPositionBefore(*item.Position, *oldest)) {
				p := *item.Position
				oldest = &p
			}
		}
	}
	return oldest
}

func observationHistoryContinues(held, fresh appwire.ThreadReadResponse) bool {
	return held.Snapshot == nil || fresh.Snapshot != nil && held.Snapshot.Incarnation == fresh.Snapshot.Incarnation && fresh.Snapshot.Length >= held.Snapshot.Length && held.Epoch == fresh.Epoch
}

// recoverObservationHistory walks only the gap back to the held window. The
// public cursor fences the history incarnation and exclusive page boundary,
// so concurrent appends cannot move the traversal forward. As in apphistory,
// append-only growth is allowed, but a replacement/epoch/boot change is not.
// Pages are consumed one at a time, never accumulated into an unbounded read.
func recoverObservationHistory(ctx context.Context, client *appwire.Client, ref string, held, fresh appwire.ThreadReadResponse, emit func([]appwire.ThreadItem) bool) error {
	if !observationHistoryContinues(held, fresh) {
		return appwire.TranscriptItemCursorStale()
	}
	if fresh.Changes != nil && !emit(fresh.Changes.Items) {
		return ctx.Err()
	}
	floor := oldestObservationPosition(held.Thread.Turns)
	oldest := oldestObservationPosition(fresh.Thread.Turns)
	cursor := fresh.OlderCursor
	for cursor != "" && (floor == nil || oldest == nil || observationPositionBefore(*floor, *oldest)) {
		page, err := client.ThreadTurnsList(ctx, appwire.ThreadTurnsListParams{Ref: ref, Cursor: cursor, ItemLimit: appwire.TranscriptItemPageLimit})
		if err != nil {
			return err
		}
		if fresh.Snapshot == nil || page.Snapshot == nil || fresh.Snapshot.Incarnation != page.Snapshot.Incarnation || page.Snapshot.Length < fresh.Snapshot.Length || page.BootGeneration != fresh.BootGeneration || page.Epoch != fresh.Epoch {
			return appwire.TranscriptItemCursorStale()
		}
		next := oldestObservationPosition(page.Data)
		if next == nil || oldest != nil && !observationPositionBefore(*next, *oldest) {
			return appwire.TranscriptItemCursorStale()
		}
		for _, turn := range page.Data {
			if !emit(turn.Items) {
				return ctx.Err()
			}
		}
		oldest, cursor = next, page.NextCursor
	}
	return nil
}

func (b *boundSessionOperations) Watch(ctx context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
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
	watchCtx, cancel := context.WithCancel(ctx)
	transport, err := b.owner.server.NewInProcessTransport(watchCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	cleanup := func() { cancel(); _ = transport.Close() }
	client := appwire.NewClient(transport)
	client.Start(watchCtx)
	if _, err := client.Initialize(watchCtx, appwire.InitializeParams{}); err != nil {
		cleanup()
		return nil, err
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
	params := appwire.ThreadReadParams{Ref: ref.String(), Subscribe: true, IncludeTurns: request.Completion || sessionWatchSelected(request, "communicate")}
	read, err := client.ThreadRead(watchCtx, params)
	if err != nil {
		cleanup()
		return nil, err
	}
	notifications := client.Notifications()
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
		type readResult struct {
			response *appwire.ThreadReadResponse
			items    []appwire.ThreadItem
			finished bool
			err      error
		}
		// One in-flight read, not a second notification consumer. Keeping this
		// loop draining lets the shared client's response/handoff progress even
		// when notifications precede the response on the wire.
		readDone := make(chan readResult)
		reading, readAgain := false, false
		held, pending := read, read
		refresh := func() {
			if reading {
				readAgain = true
				return
			}
			reading = true
			previous := held
			go func() {
				publish := func(result readResult) bool {
					select {
					case readDone <- result:
						return true
					case <-watchCtx.Done():
						return false
					}
				}
				recoveryParams := params
				recoveryParams.HeldSnapshot = previous.Snapshot
				recoveryParams.ItemLimit = appwire.TranscriptItemPageLimit
				response, err := client.ThreadRead(watchCtx, recoveryParams)
				if err == nil && publish(readResult{response: &response}) && response.Thread.Evener.Ref == initial.Metadata.Ref && response.Thread.Evener.InstanceID == initial.Metadata.InstanceID && response.BootGeneration == initial.Metadata.GenerationID && params.IncludeTurns {
					err = recoverObservationHistory(watchCtx, client, ref.String(), previous, response, func(items []appwire.ThreadItem) bool {
						return publish(readResult{items: items})
					})
				}
				publish(readResult{finished: true, err: err})
			}()
		}
		for {
			select {
			case <-watchCtx.Done():
				return
			case result := <-readDone:
				if result.err != nil {
					return
				}
				if fresh := result.response; fresh != nil {
					if !observationHistoryContinues(held, *fresh) {
						return
					}
					meta := appwire.SessionObservationMetadata{Ref: fresh.Thread.Evener.Ref, InstanceID: fresh.Thread.Evener.InstanceID, GenerationID: fresh.BootGeneration}
					if !send(appwire.SessionWatchUpdate{Snapshot: fresh, Metadata: meta}) {
						return
					}
					if meta.Ref != initial.Metadata.Ref || meta.InstanceID != initial.Metadata.InstanceID || meta.GenerationID != initial.Metadata.GenerationID {
						return // The runtime ends the old watch from this replacement cut.
					}
					pending = *fresh
				}
				for _, u := range recordedObservationUpdates(result.items, request, ref.String(), initial.Metadata.InstanceID, initial.Metadata.GenerationID) {
					if !send(u) {
						return
					}
				}
				if result.finished {
					held, reading = pending, false
					if readAgain {
						readAgain = false
						refresh()
					}
				}
			case notification, open := <-notifications:
				if !open {
					return
				}
				current, err := b.State(watchCtx)
				if err != nil || !current.Manager || current.Ref != view.Ref || current.InstanceID != view.InstanceID {
					return
				}
				base := appwire.SessionObservationMetadata{Ref: ref.String(), InstanceID: read.Thread.Evener.InstanceID, GenerationID: read.BootGeneration}
				if notification.Method == appwire.NotifyEvenerThreadResync {
					if !send(appwire.SessionWatchUpdate{Notification: &notification, Metadata: base}) {
						return
					}
					refresh()
					continue
				}
				// The shared human relay deliberately fans malformed and untargeted
				// frames out unchanged. Receipt alone cannot identify watched work.
				// Keep alias routing with that relay, but require target evidence here.
				if _, routing := relayNotificationRoutingKey(notification, ref.SourceID); routing != relayNotificationTargeted {
					continue
				}
				if notification.Method == appwire.NotifyHistoryUpdated {
					var history appwire.HistoryUpdatedParams
					if json.Unmarshal(notification.Params, &history) != nil {
						continue
					}
					if history.BootGeneration != "" && history.BootGeneration != read.BootGeneration {
						refresh()
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
				case appwire.NotifyThreadClosed:
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
