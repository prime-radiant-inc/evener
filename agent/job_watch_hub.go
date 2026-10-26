package agent

import (
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

const (
	// hubWatchRetryDelay bounds how long a suspended watch waits before trying
	// its binding again. The cost of an unnecessary retry is one state read
	// against a healthy connection; the cost of waiting only for a rebind is an
	// observation that never resumes.
	hubWatchRetryDelay = 2 * time.Second
	// hubWatchStateFailureLimit ends the watch durably after consecutive failed
	// state reads, so a genuinely broken connection produces a notice instead of
	// an indefinite retry loop.
	hubWatchStateFailureLimit = 5
)

func hubObservationSelector(event string) bool {
	switch event {
	case "status", "question", "approval", "communicate":
		return true
	}
	return false
}

// Hub watches use the same journal, pending-send rail and watch ID as ordinary
// watches. Their source is a canonical ref, not a local job or event stream.
// caller names the Manager whose binding admitted the watch; the durable
// identity keys on the target triple plus that caller, so two Managers
// watching the same target keep distinct watches.
func (jm *jobManager) configureHubWatch(ref, instance, generation, caller string, selectors []string, completion bool) (watchResult, error) {
	if ref == "" || instance == "" || generation == "" || caller == "" {
		return watchResult{}, errors.New("source_not_watchable: authoritative hub target generation and Manager caller are required")
	}
	if len(selectors) == 0 && !completion {
		return watchResult{}, errors.New("invalid_request: watch needs events or completion")
	}
	for _, event := range selectors {
		if !hubObservationSelector(event) {
			return watchResult{}, fmt.Errorf("invalid_request: unsupported hub observation %q", event)
		}
	}
	a := watchArgs{Target: ref, Source: ref, Send: &watchSendArgs{To: runtimeMessageAliasCaller}, ReceiverSendInternal: true}
	cfg, err := newWatchConfig(a, jm.now(), "")
	if err != nil {
		return watchResult{}, err
	}
	cfg.events = append([]string(nil), selectors...)
	cfg.hubRef, cfg.hubInstance, cfg.hubGeneration, cfg.hubCaller, cfg.hubCompletion = ref, instance, generation, caller, completion
	cfg.configHash = fmt.Sprintf("%s:%s:%s:%s:%s:%v:%v", cfg.configHash, ref, instance, generation, caller, selectors, completion)
	key := watchKey{VisibleSessionID: jm.sessionID, Target: ref, SendTo: runtimeMessageAliasCaller}
	jm.mu.Lock()
	existing := jm.watches[key]
	if existing == nil && len(jm.watches) >= maxLiveWatches {
		jm.mu.Unlock()
		return watchResult{}, errors.New("invalid_request: too many live watches")
	}
	equal, detachedCfgs, dropped, err := jm.persistWatchReplacementLocked(key, cfg)
	if err != nil {
		jm.mu.Unlock()
		return watchResult{}, err
	}
	if equal {
		cfg = existing
	} else {
		if existing != nil {
			jm.recordWatchEndedLocked(key, existing, "replaced")
			closeWatchConfig(existing)
		}
		jm.watches[key] = cfg
	}
	result := watchResultFromConfig(cfg, existing != nil && !equal)
	jm.mu.Unlock()
	jm.removeWatchSendTerminalSnapshots(dropped)
	jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
	return result, nil
}

func hubObservationIdentity(m appwire.SessionObservationMetadata) string {
	if m.EntryID == "" {
		return ""
	}
	return m.InstanceID + "/" + m.GenerationID + "/" + m.EntryID
}

// onHubObservation accepts evidence from the bound serving generation. Turn
// identity, not the serving boot, selects recorded completion evidence. The
// terminal send is persisted after the watch's clear and before its wake token.
func (jm *jobManager) onHubObservation(watchID string, update appwire.SessionWatchUpdate) error {
	return jm.onHubObservationEvidence(watchID, update, false)
}

// recorded means the evidence came from an already validated snapshot. Its
// execution provenance need not name the current serving boot, so do not fill
// missing provenance with the subscription's identity.
func (jm *jobManager) onHubObservationEvidence(watchID string, update appwire.SessionWatchUpdate, recorded bool) error {
	return jm.onHubObservationLookup(watchID, update, recorded, nil)
}

type hubAnswerLookup func(ref, instance, generation, ancestor, candidate string) (bool, error)

func (jm *jobManager) onHubObservationLookup(watchID string, update appwire.SessionWatchUpdate, recorded bool, lookup hubAnswerLookup) error {
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok || cfg.hubRef == "" {
		jm.mu.Unlock()
		return nil
	}
	if update.Notification != nil && update.Notification.Method == appwire.NotifyEvenerThreadResync {
		matches := update.Metadata.Ref == cfg.hubRef && update.Metadata.InstanceID == cfg.hubInstance && update.Metadata.GenerationID == cfg.hubGeneration
		jm.mu.Unlock()
		if matches {
			return jm.suspendHubWatch(watchID)
		}
		return nil
	}
	if update.Snapshot != nil {
		read := update.Snapshot
		if read.Thread.Evener.Ref != cfg.hubRef || read.Thread.Evener.InstanceID != cfg.hubInstance || read.BootGeneration != cfg.hubGeneration {
			jm.mu.Unlock()
			return nil
		}
		if !cfg.hubTurnBound {
			cfg.hubTurnID = read.Thread.Evener.ActiveTurnID
			if cfg.hubTurnID == "" {
				// A fresh idle watch catches up only with the latest work, never
				// an older done hidden behind later unfinished work.
				for _, turn := range read.Thread.Turns {
					if turn.ID != appwire.SystemPreludeTurnID {
						cfg.hubTurnID = turn.ID
					}
				}
			}
			cfg.hubTurnBound = cfg.hubTurnID != ""
		}
		cfg.hubSuspended = false
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
				if meta.EntryID == "" {
					meta.EntryID = item.TranscriptKey
					if meta.EntryID == "" {
						meta.EntryID = item.ID
					}
				}
				if meta.TurnID == "" {
					meta.TurnID = turn.ID
				}
				u := appwire.SessionWatchUpdate{Metadata: meta, Event: "communicate"}
				if err := jm.onHubObservationLookup(watchID, u, true, lookup); err != nil {
					return err
				}
			}
		}
		return nil
	}
	m := update.Metadata
	if len(m.AnswerToTurnIDs) != 0 {
		jm.mu.Unlock()
		return nil // Answer input is history evidence, not a communicate event.
	}
	if !recorded && (m.Ref != cfg.hubRef || m.InstanceID != cfg.hubInstance || m.GenerationID != cfg.hubGeneration) {
		jm.mu.Unlock()
		return nil
	}
	switch update.Event {
	case "", "status", "question", "approval", "communicate":
	default:
		jm.mu.Unlock()
		return fmt.Errorf("unsupported hub observation %q; re-register", update.Event)
	}
	// Delivery identity belongs to this subscription, not the historical boot.
	identityMetadata := m
	identityMetadata.InstanceID, identityMetadata.GenerationID = cfg.hubInstance, cfg.hubGeneration
	entryIdentity := hubObservationIdentity(identityMetadata)
	if entryIdentity == "" {
		jm.mu.Unlock()
		return nil
	}
	if !cfg.hubTurnBound && m.TurnID != "" {
		cfg.hubTurnID, cfg.hubTurnBound = m.TurnID, true
	}
	matchingTurn := !cfg.hubTurnBound || m.TurnID == cfg.hubTurnID
	terminal := cfg.hubCompletion && m.CausalKnown && m.TerminalAccepted != nil && *m.TerminalAccepted && m.EndTurn && m.EndReason == "done"
	if terminal && !matchingTurn && m.TurnID != "" && lookup != nil {
		bound := cfg.hubTurnID
		ref, instance, generation := cfg.hubRef, cfg.hubInstance, cfg.hubGeneration
		jm.mu.Unlock()
		matches, err := lookup(ref, instance, generation, bound, m.TurnID)
		jm.mu.Lock()
		_, current, present := jm.watchConfigByIDLocked(watchID)
		if !present || current != cfg || cfg.rejectingDelivery || cfg.hubTurnID != bound || cfg.hubRef != ref || cfg.hubInstance != instance || cfg.hubGeneration != generation {
			jm.mu.Unlock()
			return nil
		}
		if err != nil {
			jm.mu.Unlock()
			return err
		}
		matchingTurn = matches
	}
	complete := terminal && matchingTurn
	identity := entryIdentity + ":" + update.Event
	if complete {
		// One recorded entry can first arrive without an acceptance verdict.
		// Its later terminal verdict must not inherit the earlier event's dedup.
		identity += ":completed"
	}
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
	selected := false
	for _, selector := range cfg.events {
		if selector == event {
			selected = true
			break
		}
	}
	if !m.CausalKnown {
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
	decoded, err := decodeManagerArguments[appwire.SessionWatchRequest](encoded)
	if err != nil {
		return nil, err
	}
	request = *decoded
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
		if !hubObservationSelector(selector) {
			return nil, appwire.InvalidParams("unsupported observation selector " + selector)
		}
	}
	state, err := client.State(ctx)
	if err != nil {
		return nil, err
	}
	if !state.Manager {
		return nil, appwire.InvalidRequest("session is not a Manager")
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
	result, err := jm.configureHubWatch(request.Ref, first.Metadata.InstanceID, first.Metadata.GenerationID, state.Ref, request.Events, request.Completion)
	if err != nil {
		cancel()
		return nil, err
	}
	if s.installHubWatchSubscription(jm, result.WatchID, cancel) {
		go s.consumeHubWatch(watchCtx, result.WatchID, request, state.Ref, state.InstanceID, updates, first)
	}
	return marshalWatchResult(result, jobToolResultDefaultMaxChar)
}

func (s *Session) installHubWatchSubscription(jm *jobManager, id string, cancel context.CancelFunc) bool {
	s.mu.Lock()
	if s.hubWatchCancels == nil {
		s.hubWatchCancels = map[string]context.CancelFunc{}
	}
	// A concurrent create can replace this registration before its transport
	// is installed. Never resurrect that transport or cancel the winner's.
	if !managerHubWatchPresent(jm, id) || s.hubWatchCancels[id] != nil {
		s.mu.Unlock()
		cancel()
		return false
	}
	var retired []context.CancelFunc
	for id, stop := range s.hubWatchCancels {
		if !managerHubWatchPresent(jm, id) {
			delete(s.hubWatchCancels, id)
			retired = append(retired, stop)
		}
	}
	s.hubWatchCancels[id] = cancel
	s.mu.Unlock()
	for _, stop := range retired {
		stop()
	}
	return true
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

// hubWatchReadBudget bounds one observation-side operation, not the watch's
// lifetime. State checks and continuation queries share this existing budget.
const hubWatchReadBudget = 2 * time.Second

// consumeHubWatch owns just a transport subscription. The job manager owns
// durable watch state, delivery identities, provenance and restart teardown.
// callerRef and callerInstance name the Manager whose binding admitted the
// watch: a recheck ends the watch if the binding no longer serves that same
// Manager session.
func (s *Session) consumeHubWatch(ctx context.Context, id string, request appwire.SessionWatchRequest, callerRef, callerInstance string, updates <-chan appwire.SessionWatchUpdate, initial appwire.SessionWatchUpdate) {
	defer s.cancelHubWatch(id)
	jm := s.jobManager
	if jm == nil {
		return
	}
	s.mu.Lock()
	boundSignal := s.hubWatchSignal
	s.mu.Unlock()
	stateFailures := 0
	var held *appwire.SnapshotIdentity
	var epoch uint64
	observe := func(update appwire.SessionWatchUpdate) error {
		if read := update.Snapshot; read != nil && read.Snapshot != nil {
			if held != nil && (held.Incarnation != read.Snapshot.Incarnation || read.Snapshot.Length < held.Length || epoch != read.Epoch) {
				return errors.New("answer continuation history identity changed")
			}
			cut := *read.Snapshot
			held, epoch = &cut, read.Epoch
		}
		return jm.onHubObservationLookup(id, update, false, func(ref, instance, generation, ancestor, candidate string) (bool, error) {
			if held == nil {
				return false, errors.New("answer continuation has no history identity")
			}
			s.mu.Lock()
			client := s.hubClient
			s.mu.Unlock()
			if client == nil {
				return false, errors.New("answer continuation transport unavailable")
			}
			query := &appwire.AnswerContinuationQuery{AncestorTurnID: ancestor, CandidateTurnID: candidate}
			readCtx, cancel := context.WithTimeout(ctx, hubWatchReadBudget)
			defer cancel()
			result, err := client.Invoke(readCtx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: ref, HeldSnapshot: held, AnswerContinuation: query}})
			if err != nil {
				return false, err
			}
			if err := readCtx.Err(); err != nil {
				return false, err
			}
			read := result.Read
			if read == nil {
				return false, errors.New("answer continuation response missing")
			}
			if read.Thread.Evener.Ref != ref || read.Thread.Evener.InstanceID != instance || read.BootGeneration != generation || read.Epoch != epoch || read.Snapshot == nil || read.Snapshot.Incarnation != held.Incarnation || read.Snapshot.Length < held.Length {
				return false, fmt.Errorf("answer continuation response identity mismatch: ref=%t instance=%t boot=%t epoch=%d/%d snapshot=%+v held=%+v", read.Thread.Evener.Ref == ref, read.Thread.Evener.InstanceID == instance, read.BootGeneration == generation, read.Epoch, epoch, read.Snapshot, held)
			}
			verdict := read.AnswerContinuation
			if verdict == nil || verdict.AncestorTurnID != ancestor || verdict.CandidateTurnID != candidate {
				return false, errors.New("answer continuation response omitted requested verdict")
			}
			return verdict.Matches, nil
		})
	}
	for {
		if err := observe(initial); err != nil {
			_ = jm.endHubWatch(id, "observation snapshot failed: "+err.Error(), nil)
			return
		}
		if !managerHubWatchPresent(jm, id) {
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
				if err := observe(update); err != nil {
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
			// A failed state read leaves it undetermined whether the stream died
			// with the connection or ended on a healthy one. Resolve that with a
			// bounded retry: a healthy connection then reaches the same
			// "ended unexpectedly" arm a successful read would have reached,
			// instead of parking on a signal that only fires on a rebind.
			var stateErr error
			stateCtx, cancel := context.WithTimeout(ctx, hubWatchReadBudget)
			state, err := client.State(stateCtx)
			cancel()
			if err == nil {
				stateFailures = 0
				if !state.Manager || state.Ref != callerRef || state.InstanceID != callerInstance {
					_ = jm.endHubWatch(id, "Manager binding no longer serves this session; re-register", nil)
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
			} else {
				stateErr = err
				stateFailures++
			}
			// A rebind resolves the ambiguity immediately; otherwise retry after a
			// bounded delay. Repeated failures end the watch durably rather than
			// retrying a broken read forever: the Manager gets a notice it can act
			// on instead of an observation that silently stopped.
			select {
			case <-ctx.Done():
				return
			case <-signal:
			case <-time.After(hubWatchRetryDelay):
			}
			if stateErr != nil && stateFailures >= hubWatchStateFailureLimit {
				_ = jm.endHubWatch(id, "observation unreachable: "+stateErr.Error()+"; re-register", nil)
				return
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
