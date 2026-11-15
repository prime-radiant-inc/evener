package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/identifier"
)

func validSessionWatch(request appwire.SessionWatchRequest) error {
	if request.Scope != "" {
		if request.Scope != "top_level" || request.Ref != "" || request.Completion || len(request.Events) != 0 {
			return appwire.InvalidParams("invalid scope watch request")
		}
		return nil
	}
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
	return slices.Contains(request.Events, event)
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

// observationDisposition classifies a fresh cut of history for the Watch
// stream: a replaced target ends the stream, a same-target history
// discontinuity re-anchors it on the fresh cut, and a continuing thread
// forwards with gap paging.
type observationDisposition int

const (
	observationReplaced observationDisposition = iota
	observationReanchors
	observationContinues
)

func observationSnapshotDisposition(initial appwire.SessionObservationMetadata, held, fresh appwire.ThreadReadResponse) observationDisposition {
	replacement := fresh.Thread.Evener.Ref != initial.Ref || fresh.Thread.Evener.InstanceID != initial.InstanceID || fresh.BootGeneration != initial.GenerationID
	switch {
	case replacement:
		return observationReplaced
	case observationHistoryContinues(held, fresh):
		return observationContinues
	default:
		return observationReanchors
	}
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
	return pageObservationHistory(ctx, client, ref, held, fresh, emit)
}

// reanchorObservationHistory pages a same-target rebuilt history from its
// latest window back to the pre-rebuild held window's oldest position. A
// rebuild leaves no continuable gap, but a terminal that landed during the
// relay gap is still older than the truncated latest window, so the fresh
// history must be paged instead of forwarding only its latest page.
func reanchorObservationHistory(ctx context.Context, client *appwire.Client, ref string, held, fresh appwire.ThreadReadResponse, emit func([]appwire.ThreadItem) bool) error {
	return pageObservationHistory(ctx, client, ref, held, fresh, emit)
}

func pageObservationHistory(ctx context.Context, client *appwire.Client, ref string, held, fresh appwire.ThreadReadResponse, emit func([]appwire.ThreadItem) bool) error {
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

// observationCursorStale reports the typed answer for a cursor that no longer
// addresses the served history.
func observationCursorStale(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	return data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
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
	if request.Scope != "" {
		return watchRoster(ctx, b.owner.rosterBroker(), view)
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
	if request.Completion || sessionWatchSelected(request, "communicate") {
		if ref.SourceID == appwire.LocalSourceID {
			servingRef, parseErr := appwire.ParseRef(read.Thread.Evener.Ref)
			local, ok := b.owner.sources.Source(servingRef.SourceID)
			if parseErr != nil {
				cleanup()
				return nil, appwire.InvalidRequest("target cannot report recorded-observation support")
			}
			if ok {
				if localSource, localDaemon := local.(*appsource.LocalDaemonSource); localDaemon {
					supported, known := localSource.SessionToolsSupported(servingRef)
					if !known || !supported {
						cleanup()
						return nil, appwire.InvalidRequest("target cannot report recorded-observation support")
					}
				}
			}
		} else if _, ok := b.owner.sources.Source(ref.SourceID); ok {
			if read.TargetSessionToolsSupported == nil || !*read.TargetSessionToolsSupported {
				cleanup()
				return nil, appwire.InvalidRequest("target cannot report recorded-observation support")
			}
		} else {
			cleanup()
			return nil, appwire.InvalidRequest("target cannot report recorded-observation support")
		}
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
		// The initial snapshot is the window's historical catch-up: its turns
		// already carry the recorded observations to the consumer. Replaying
		// each one as an individual frame here would only duplicate them, and
		// a window with more observations than the reverse transport's
		// 32-update queue overflows it while the consumer is still taking the
		// snapshot — the overflow path cancels the whole binding. Items that
		// arrive after the snapshot flow through the notification and refresh
		// paths below instead.
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
		staleRefreshed := false
		refresh := func(freshCut bool) {
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
				if freshCut {
					// A stale cut cannot answer against this history and
					// re-reading with it would only repeat that answer. Ask
					// for the latest authoritative cut instead.
					recoveryParams.HeldSnapshot = nil
				} else {
					recoveryParams.HeldSnapshot = previous.Snapshot
				}
				recoveryParams.ItemLimit = appwire.TranscriptItemPageLimit
				response, err := client.ThreadRead(watchCtx, recoveryParams)
				if err == nil && publish(readResult{response: &response}) && response.Thread.Evener.Ref == initial.Metadata.Ref && response.Thread.Evener.InstanceID == initial.Metadata.InstanceID && response.BootGeneration == initial.Metadata.GenerationID && params.IncludeTurns {
					// A same-target discontinuity (an index rebuild behind a
					// resync) has no continuable gap, but its truncated latest
					// window can still sit above evidence the watch has not
					// replayed. Page the fresh history back to the held window
					// so that evidence delivers instead of stranding the watch.
					switch observationSnapshotDisposition(initial.Metadata, previous, response) {
					case observationContinues:
						err = recoverObservationHistory(watchCtx, client, ref.String(), previous, response, func(items []appwire.ThreadItem) bool {
							return publish(readResult{items: items})
						})
					case observationReanchors:
						err = reanchorObservationHistory(watchCtx, client, ref.String(), previous, response, func(items []appwire.ThreadItem) bool {
							return publish(readResult{items: items})
						})
					}
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
					if !staleRefreshed && observationCursorStale(result.err) {
						// The history rotated while this read was paging its
						// gap. Ask for the latest cut once; the next read
						// takes the re-anchor branch instead of ending the
						// subscription.
						staleRefreshed = true
						reading = false
						refresh(true)
						continue
					}
					return
				}
				if fresh := result.response; fresh != nil {
					staleRefreshed = false
					meta := appwire.SessionObservationMetadata{Ref: fresh.Thread.Evener.Ref, InstanceID: fresh.Thread.Evener.InstanceID, GenerationID: fresh.BootGeneration}
					disposition := observationSnapshotDisposition(initial.Metadata, held, *fresh)
					if !send(appwire.SessionWatchUpdate{Snapshot: fresh, Metadata: meta}) {
						return
					}
					switch disposition {
					case observationReplaced:
						// The runtime ends the old watch from this replacement cut,
						// so the stream ends with it.
						return
					case observationReanchors:
						// A same-target history discontinuity re-anchors the
						// subscription on the fresh authoritative cut instead of
						// ending it: the watcher replays the window under its
						// own dedup and the drain continues, so a transient
						// rebuild recovers without re-registration.
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
						refresh(false)
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
					refresh(false)
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
						refresh(false)
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

func watchRoster(ctx context.Context, broker *attentionBroker, view appwire.ManagerView) (<-chan appwire.SessionWatchUpdate, error) {
	subscription, err := broker.Subscribe(view)
	if err != nil {
		return nil, err
	}
	updates := make(chan appwire.SessionWatchUpdate)
	go func() {
		defer close(updates)
		defer subscription.Cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case roster, ok := <-subscription.Updates:
				if !ok {
					return
				}
				select {
				case updates <- appwire.SessionWatchUpdate{Roster: &roster}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return updates, nil
}
