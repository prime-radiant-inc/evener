package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// Hub watches use the same journal, pending-send rail and watch ID as ordinary
// watches. Their source is a canonical ref, not a local job or event stream.
func (jm *jobManager) configureHubWatch(ref, instance, generation, hubID string, revision uint64, selectors []string, completion bool) (watchResult, error) {
	if ref == "" || instance == "" || generation == "" || hubID == "" || revision == 0 {
		return watchResult{}, errors.New("source_not_watchable: authoritative hub target generation and setting are required")
	}
	if len(selectors) == 0 && !completion {
		return watchResult{}, errors.New("invalid_request: watch needs events or completion")
	}
	for _, event := range selectors {
		switch event {
		case "status", "question", "approval", "communicate":
		default:
			return watchResult{}, fmt.Errorf("invalid_request: unsupported hub observation %q", event)
		}
	}
	a := watchArgs{Target: ref, Source: ref, Send: &watchSendArgs{To: runtimeMessageAliasCaller}, ReceiverSendInternal: true}
	cfg, err := newWatchConfig(a, jm.now(), "")
	if err != nil {
		return watchResult{}, err
	}
	cfg.events = append([]string(nil), selectors...)
	cfg.hubRef, cfg.hubInstance, cfg.hubGeneration, cfg.hubID, cfg.hubRevision, cfg.hubCompletion = ref, instance, generation, hubID, revision, completion
	cfg.configHash = fmt.Sprintf("%s:%s:%s:%s:%d:%v:%v", cfg.configHash, ref, instance, generation, revision, selectors, completion)
	key := watchKey{VisibleSessionID: jm.sessionID, Target: ref, SendTo: runtimeMessageAliasCaller}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) >= maxLiveWatches {
		return watchResult{}, errors.New("invalid_request: too many live watches")
	}
	if existing := jm.watches[key]; existing != nil {
		return watchResultFromConfig(existing, false), nil
	}
	if err := jm.appendWatchRegisteredEvent(cfg); err != nil {
		return watchResult{}, err
	}
	jm.watches[key] = cfg
	return watchResultFromConfig(cfg, false), nil
}

func hubObservationIdentity(m appwire.SessionObservationMetadata) string {
	if m.EntryID == "" {
		return ""
	}
	return m.InstanceID + "/" + m.GenerationID + "/" + m.EntryID
}

// onHubObservation accepts only recorded, generation-matched evidence. The
// terminal send is persisted after the watch's clear and before its wake token.
func (jm *jobManager) onHubObservation(watchID string, update appwire.SessionWatchUpdate) error {
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok || cfg.hubRef == "" {
		jm.mu.Unlock()
		return nil
	}
	if update.Snapshot != nil {
		jm.mu.Unlock()
		if update.Snapshot.Thread.Evener.AskPending {
			question := update.Metadata
			question.EntryID = "question:" + update.Snapshot.Thread.Evener.ActiveTurnID
			question.CausalKnown = true
			if err := jm.onHubObservation(watchID, appwire.SessionWatchUpdate{Metadata: question, Event: "question"}); err != nil {
				return err
			}
		}
		for _, escalation := range update.Snapshot.Thread.Evener.PendingEscalations {
			approval := update.Metadata
			approval.EntryID = "approval:" + escalation.EscalationID
			approval.CausalKnown = true
			if err := jm.onHubObservation(watchID, appwire.SessionWatchUpdate{Metadata: approval, Event: "approval"}); err != nil {
				return err
			}
		}
		for _, turn := range update.Snapshot.Thread.Turns {
			for _, item := range turn.Items {
				if item.Observation == nil {
					continue
				}
				meta := *item.Observation
				meta.Ref = cfg.hubRef
				meta.InstanceID = update.Snapshot.Thread.Evener.InstanceID
				meta.GenerationID = update.Snapshot.BootGeneration
				u := appwire.SessionWatchUpdate{Metadata: meta, Event: "communicate"}
				if err := jm.onHubObservation(watchID, u); err != nil {
					return err
				}
			}
		}
		return nil
	}
	m := update.Metadata
	if m.Ref != cfg.hubRef || m.InstanceID != cfg.hubInstance || m.GenerationID != cfg.hubGeneration {
		jm.mu.Unlock()
		return nil
	}
	switch update.Event {
	case "", "status", "question", "approval", "communicate":
	default:
		jm.mu.Unlock()
		return fmt.Errorf("unsupported hub observation %q; re-register", update.Event)
	}
	entryIdentity := hubObservationIdentity(m)
	if entryIdentity == "" {
		jm.mu.Unlock()
		return nil
	}
	identity := entryIdentity + ":" + update.Event
	if cfg.hubSeen == nil {
		cfg.hubSeen = map[string]bool{}
	}
	if cfg.hubSeen[identity] {
		jm.mu.Unlock()
		return nil
	}
	event := update.Event
	if event == "" && len(cfg.events) == 1 {
		event = cfg.events[0]
	}
	complete := cfg.hubCompletion && m.TerminalAccepted != nil && *m.TerminalAccepted && m.EndTurn && m.EndReason == "done"
	selected := false
	for _, selector := range cfg.events {
		if selector == event {
			selected = true
			break
		}
	}
	if !m.CausalKnown {
		selected = false
		complete = false
		event = "replay gap: unknown causality"
		selected = true
	}
	if !selected && !complete {
		jm.mu.Unlock()
		return nil
	}
	crossedBudget := false
	if m.CausalKnown && !complete {
		var accepted bool
		accepted, crossedBudget = noteConditionFireLocked(cfg)
		if !accepted {
			jm.mu.Unlock()
			return nil
		}
	}
	cfg.hubSeen[identity] = true
	trigger := event
	if complete {
		trigger = "completed: accepted terminal done"
	}
	root := events.SessionEvent{SessionID: jm.sessionID, Kind: events.EventCommunicate, Provenance: m.Causal}
	delivery := jm.watchSendSnapshot(cfg, identity, trigger, root)
	jm.mu.Unlock()
	if complete {
		return jm.endHubWatch(watchID, "completed", &delivery)
	}
	jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	if crossedBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return nil
}

// suspendHubWatch reports a single transport-loss episode without ending the
// durable watch or discarding a pending delivery.
func (jm *jobManager) suspendHubWatch(watchID string) error {
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok || cfg.hubRef == "" || cfg.hubSuspended {
		jm.mu.Unlock()
		return nil
	}
	cfg.hubSuspended = true
	delivery := jm.watchSendSnapshot(cfg, cfg.hubRef+"#interruption", "observation interrupted: connection lost, waiting for rebind", events.SessionEvent{SessionID: jm.sessionID})
	jm.mu.Unlock()
	jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	return nil
}

func (jm *jobManager) resumeHubWatch(watchID string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if _, cfg, ok := jm.watchConfigByIDLocked(watchID); ok {
		cfg.hubSuspended = false
	}
}

func (jm *jobManager) endHubWatches(reason string) error {
	jm.mu.Lock()
	ids := []string{}
	for _, cfg := range jm.watches {
		if cfg.hubRef != "" {
			ids = append(ids, cfg.watchID)
		}
	}
	jm.mu.Unlock()
	for _, id := range ids {
		if err := jm.endHubWatch(id, reason, nil); err != nil {
			return err
		}
	}
	return nil
}

func (jm *jobManager) endHubWatch(watchID, reason string, completion *watchSendDelivery) error {
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok || cfg.hubRef == "" {
		jm.mu.Unlock()
		return nil
	}
	snapshot := watchConfigTerminalSnapshot{key: key, cfg: cfg, endReason: reason}
	cfg.rejectingDelivery = true
	jm.mu.Unlock()
	if err := jm.appendWatchTeardownBatch(nil, []watchConfigTerminalSnapshot{snapshot}); err != nil {
		jm.rollbackWatchConfigSnapshotsRejecting([]watchConfigTerminalSnapshot{snapshot})
		return err
	}
	jm.mu.Lock()
	if jm.watches[key] != cfg {
		jm.mu.Unlock()
		return nil
	}
	delete(jm.watches, key)
	jm.recordWatchEndedLocked(key, cfg, reason)
	cfg.rejectingDelivery = false
	jm.rememberDetachedPendingLocked(cfg)
	var delivery watchSendDelivery
	if completion != nil {
		delivery = *completion
	} else {
		delivery = jm.watchSendSnapshot(cfg, cfg.hubRef+"#ended", "watch ended: "+strings.TrimSpace(reason)+"; re-register if observation is still needed", events.SessionEvent{SessionID: jm.sessionID})
		delivery.endNotice = true
	}
	delivery.allowAfterTerminalExpiry = true
	jm.mu.Unlock()
	jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	jm.publishWatchActivity()
	return nil
}

// restore uses the existing restart end-notice rail, including generations
// that spoke before the runtime died; no continuing hub watch is restored.
func hubWatchLostAtRestart(w *jobstore.WatchRecord) bool { return w != nil && w.HubRef != "" }

func (s *Session) configureHubWatchRuntime(ctx context.Context, args map[string]any) (any, error) {
	for key := range args {
		if key != "operation" && key != "hub" && key != "intent" {
			return nil, appwire.InvalidParams("hub watch cannot combine ordinary watch arguments")
		}
	}
	if args["operation"] != "create" {
		return nil, appwire.InvalidParams("hub watches require operation=create")
	}
	encoded, err := json.Marshal(args["hub"])
	if err != nil {
		return nil, err
	}
	var request appwire.SessionWatchRequest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if _, err := appwire.ParseRef(request.Ref); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	s.mu.Lock()
	client, enabled, closing := s.hubClient, s.hubEnabled, s.closing
	s.mu.Unlock()
	if client == nil || !enabled || closing {
		return nil, appwire.InvalidRequest("Manager tool unavailable")
	}
	if len(request.Events) == 0 && !request.Completion {
		return nil, appwire.InvalidParams("events or completion is required")
	}
	for _, selector := range request.Events {
		switch selector {
		case "status", "question", "approval", "communicate":
		default:
			return nil, appwire.InvalidParams("unsupported observation selector " + selector)
		}
	}
	state, err := client.State(ctx)
	if err != nil {
		return nil, err
	}
	if !state.Setting.Enabled {
		return nil, appwire.InvalidRequest("Manager mode is disabled")
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	updates, err := client.Watch(watchCtx, request)
	if err != nil {
		cancel()
		return nil, err
	}
	var first appwire.SessionWatchUpdate
	select {
	case first = <-updates:
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	if first.Snapshot == nil || first.Metadata.Ref != request.Ref || first.Metadata.InstanceID == "" || first.Metadata.GenerationID == "" {
		cancel()
		return nil, appwire.Unavailable("observation has no authoritative target generation")
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		cancel()
		return nil, err
	}
	result, err := jm.configureHubWatch(request.Ref, first.Metadata.InstanceID, first.Metadata.GenerationID, state.Setting.HubID, state.Setting.Revision, request.Events, request.Completion)
	if err != nil {
		cancel()
		return nil, err
	}
	s.mu.Lock()
	if s.hubWatchCancels == nil {
		s.hubWatchCancels = map[string]context.CancelFunc{}
	}
	if s.hubWatchCancels[result.WatchID] != nil {
		s.mu.Unlock()
		cancel()
		return marshalWatchResult(result, jobToolResultDefaultMaxChar)
	}
	s.hubWatchCancels[result.WatchID] = cancel
	s.mu.Unlock()
	go s.consumeHubWatch(watchCtx, result.WatchID, request, state.Setting, updates, first)
	return marshalWatchResult(result, jobToolResultDefaultMaxChar)
}

func (s *Session) cancelHubWatch(id string) {
	s.mu.Lock()
	cancel := s.hubWatchCancels[id]
	delete(s.hubWatchCancels, id)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// consumeHubWatch owns just a transport subscription. The job manager owns
// durable watch state, delivery identities, provenance and restart teardown.
func (s *Session) consumeHubWatch(ctx context.Context, id string, request appwire.SessionWatchRequest, setting appwire.ManagerSetting, updates <-chan appwire.SessionWatchUpdate, initial appwire.SessionWatchUpdate) {
	defer s.cancelHubWatch(id)
	jm := s.jobManager
	if jm == nil {
		return
	}
	s.mu.Lock()
	boundSignal := s.hubWatchSignal
	s.mu.Unlock()
	for {
		if err := jm.onHubObservation(id, initial); err != nil {
			_ = jm.endHubWatch(id, "observation snapshot failed: "+err.Error(), nil)
			return
		}
		jm.resumeHubWatch(id)
	stream:
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok {
					break stream
				}
				if update.Snapshot != nil && (update.Metadata.Ref != initial.Metadata.Ref || update.Metadata.InstanceID != initial.Metadata.InstanceID || update.Metadata.GenerationID != initial.Metadata.GenerationID) {
					_ = jm.endHubWatch(id, "target replaced; re-register", nil)
					return
				}
				if err := jm.onHubObservation(id, update); err != nil {
					_ = jm.endHubWatch(id, "observation failed: "+err.Error(), nil)
					return
				}
				if !managerHubWatchPresent(jm, id) {
					return
				}
			}
		}
		if ctx.Err() != nil || !managerHubWatchPresent(jm, id) {
			return
		}
		if err := jm.suspendHubWatch(id); err != nil {
			_ = jm.endHubWatch(id, "observation interruption failed: "+err.Error(), nil)
			return
		}
		for ctx.Err() == nil && managerHubWatchPresent(jm, id) {
			s.mu.Lock()
			client, signal := s.hubClient, s.hubWatchSignal
			s.mu.Unlock()
			if client == nil {
				return
			}
			stateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			state, err := client.State(stateCtx)
			cancel()
			if err == nil {
				if !state.Setting.Enabled || state.Setting.HubID != setting.HubID || state.Setting.Revision != setting.Revision {
					_ = jm.endHubWatch(id, "Manager setting disabled or changed; re-register", nil)
					return
				}
				if signal == boundSignal {
					_ = jm.endHubWatch(id, "observation subscription ended unexpectedly; re-register", nil)
					return
				}
				rebound, watchErr := client.Watch(ctx, request)
				if watchErr == nil {
					var snapshot appwire.SessionWatchUpdate
					select {
					case snapshot = <-rebound:
					case <-ctx.Done():
						return
					}
					if snapshot.Snapshot == nil || snapshot.Metadata.InstanceID != initial.Metadata.InstanceID || snapshot.Metadata.GenerationID != initial.Metadata.GenerationID || snapshot.Metadata.Ref != initial.Metadata.Ref {
						_ = jm.endHubWatch(id, "target replaced or observation unavailable; re-register", nil)
						return
					}
					updates, initial, boundSignal = rebound, snapshot, signal
					break
				}
				if permanentHubWatchError(watchErr) {
					_ = jm.endHubWatch(id, "observation subscription rejected: "+watchErr.Error(), nil)
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-signal:
			}
		}
		if ctx.Err() != nil || !managerHubWatchPresent(jm, id) {
			return
		}
	}
}

// A server refusing the request or its selector cannot be repaired by another
// transport rebind. Unavailability and transport failures may be transient.
func permanentHubWatchError(err error) bool {
	if appwire.IsTransportFailure(err) {
		return false
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	switch wire.Code {
	case appwire.CodeInvalidParams, appwire.CodeInvalidRequest, appwire.CodeMethodNotFound:
		return true
	default:
		return false
	}
}

func managerHubWatchPresent(jm *jobManager, id string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, cfg, ok := jm.watchConfigByIDLocked(id)
	return ok && cfg.hubRef != ""
}
