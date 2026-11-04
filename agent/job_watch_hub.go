package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

var hubObservationEvents = [...]string{"status", "question", "approval", "communicate"}

const hubRosterWatchKey = "hub:roster"

const rosterRecoveryFrame = "roster_resync: true\nrecovery: re-register job_watch with hub={\"scope\":\"top_level\"} for a fresh snapshot\n"

// The pending send rail replaces earlier frames for the same watch key.
// Merge only roster frames there, after it finds the actual undelivered state.
// Resync drops earlier pending entries. A fresh initial after resync replaces
// them; a rebind initial without resync keeps earlier undelivered changes.
func mergeHubRosterFrames(previous, current string) string {
	if strings.Contains(current, "\nroster_resync: true\n") ||
		(strings.Contains(current, "\nroster_initial: true\n") && strings.Contains(previous, "\nroster_resync: true\n")) {
		return current
	}
	oldPayload := rosterFrameField(previous, "roster_events: ")
	newPayload := rosterFrameField(current, "roster_events: ")
	if oldPayload != "" && newPayload != "" {
		var old, next []json.RawMessage
		if json.Unmarshal([]byte(oldPayload), &old) == nil && json.Unmarshal([]byte(newPayload), &next) == nil {
			positions := make(map[string]int, len(old))
			merged := make([]json.RawMessage, 0, len(old)+len(next))
			for _, entry := range append(old, next...) {
				var ref struct {
					Ref string `json:"ref"`
				}
				if json.Unmarshal(entry, &ref) != nil || ref.Ref == "" {
					continue
				}
				if at, found := positions[ref.Ref]; found {
					merged[at] = entry
				} else {
					positions[ref.Ref] = len(merged)
					merged = append(merged, entry)
				}
			}
			if encoded, err := json.Marshal(merged); err == nil {
				current = strings.Replace(current, "roster_events: "+newPayload+"\n", "roster_events: "+string(encoded)+"\n", 1)
			}
		}
	}
	if strings.Contains(previous, "\nroster_resync: true\n") && !strings.Contains(current, "\nroster_resync: true\n") {
		current += rosterRecoveryFrame
	}
	return current
}

func rosterFrameField(frame, field string) string {
	for line := range strings.SplitSeq(frame, "\n") {
		if strings.HasPrefix(line, field) {
			return strings.TrimPrefix(line, field)
		}
	}
	return ""
}

// A roster occupies one ordinary live-watch slot and journal identity. Its
// source is top_level, while the reserved target keys its registration.
func (jm *jobManager) configureHubRosterWatch(ctx context.Context, caller string) (watchResult, error) {
	a := watchArgs{Target: hubRosterWatchKey, Source: hubRosterWatchKey, Send: &watchSendArgs{To: runtimeMessageAliasCaller}, ReceiverSendInternal: true}
	cfg, err := newWatchConfig(a, jm.now(), "")
	if err != nil {
		return watchResult{}, err
	}
	cfg.sourcePublic = "top_level"
	cfg.hubRef, cfg.hubCaller = hubRosterWatchKey, caller
	cfg.configHash += ":top_level:" + caller
	key := watchKey{VisibleSessionID: jm.sessionID, Target: hubRosterWatchKey, SendTo: runtimeMessageAliasCaller}
	jm.mu.Lock()
	if err := ctx.Err(); err != nil {
		jm.mu.Unlock()
		return watchResult{}, err
	}
	existing := jm.watches[key]
	if existing == nil && len(jm.watches) >= maxLiveWatches {
		jm.mu.Unlock()
		return watchResult{}, errors.New("invalid_request: too many live watches")
	}
	equal, detached, dropped, err := jm.persistWatchReplacementLocked(key, cfg)
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
	jm.forgetDetachedWatchSendConfigsIfEmpty(detached)
	return result, nil
}

// A transport rebind restarts hub revisions, but keeps the old baseline until
// its replacement snapshot arrives so unchanged entries do not wake the Manager.
func (jm *jobManager) resetHubRosterRevision(id string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if _, cfg, ok := jm.watchConfigByIDLocked(id); ok && cfg.hubRef == hubRosterWatchKey {
		cfg.rosterBound = false
	}
}

func (jm *jobManager) onHubRoster(id string, update appwire.SessionWatchUpdate) error {
	if update.Roster == nil {
		return nil
	}
	r := update.Roster
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(id)
	if !ok || cfg.hubRef != hubRosterWatchKey || cfg.rejectingDelivery || (cfg.rosterBound && r.Revision <= cfg.rosterRevision) {
		jm.mu.Unlock()
		return nil
	}
	cfg.rosterRevision, cfg.rosterBound = r.Revision, true
	var changed []appwire.RosterEvent
	switch {
	case r.Resync:
		alreadyResync := cfg.rosterNeedsResync
		cfg.rosterBaseline = nil
		cfg.rosterNeedsResync = true
		if alreadyResync {
			jm.mu.Unlock()
			return nil
		}
	case r.Initial:
		cfg.rosterNeedsResync = false
		next := make(map[string]appwire.AttentionEntry, len(r.Events))
		for _, event := range r.Events {
			if event.Ref == "" {
				continue
			}
			next[event.Ref] = event.AttentionEntry
			if old, known := cfg.rosterBaseline[event.Ref]; !known || old != event.AttentionEntry {
				changed = append(changed, event)
			}
		}
		for ref, old := range cfg.rosterBaseline {
			if _, present := next[ref]; !present {
				changed = append(changed, appwire.RosterEvent{AttentionEntry: old, Kind: "disappeared"})
			}
		}
		cfg.rosterBaseline = next
	default:
		if cfg.rosterBaseline == nil {
			if !cfg.rosterNeedsResync {
				cfg.rosterBaseline = make(map[string]appwire.AttentionEntry)
			}
		}
		for _, event := range r.Events {
			if event.Ref == "" {
				continue
			}
			if !cfg.rosterNeedsResync {
				if event.Kind == "disappeared" {
					delete(cfg.rosterBaseline, event.Ref)
				} else {
					cfg.rosterBaseline[event.Ref] = event.AttentionEntry
				}
			}
			changed = append(changed, event)
		}
	}
	if !r.Resync && len(changed) == 0 {
		jm.mu.Unlock()
		return nil
	}
	trigger := "roster resync: re-register job_watch hub={scope:top_level} for a fresh snapshot"
	var payload string
	if !r.Resync {
		type rosterChange struct {
			Ref             string `json:"ref"`
			Title           string `json:"title"`
			Level           string `json:"level"`
			Kind            string `json:"kind"`
			AskPending      bool   `json:"askPending"`
			ApprovalPending bool   `json:"approvalPending"`
			NeedsResponse   bool   `json:"needsResponse"`
		}
		refs := make([]rosterChange, 0, len(changed))
		for _, event := range changed {
			refs = append(refs, rosterChange{event.Ref, event.Title, event.Level, event.Kind, event.AskPending, event.ApprovalPending, event.NeedsResponse})
		}
		encoded, err := json.Marshal(refs)
		if err != nil {
			jm.mu.Unlock()
			return err
		}
		payload = string(encoded)
		trigger = fmt.Sprintf("roster revision %d, %d changes", r.Revision, len(changed))
	}
	delivery := jm.watchSendSnapshot(cfg, hubRosterWatchKey, trigger, events.SessionEvent{SessionID: jm.sessionID})
	delivery.rosterPayload = payload
	delivery.rosterInitial, delivery.rosterResync = r.Initial, r.Resync
	jm.mu.Unlock()
	jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	return nil
}

func hubObservationSelector(event string) bool {
	return slices.Contains(hubObservationEvents[:], event)
}

func validateHubWatchSelectors(selectors []string, completion bool) error {
	if len(selectors) == 0 && !completion {
		return errors.New("invalid_request: watch needs events or completion")
	}
	for _, event := range selectors {
		if !hubObservationSelector(event) {
			return fmt.Errorf("invalid_request: unsupported hub observation %q", event)
		}
	}
	return nil
}

// Hub watches use the same journal, pending-send rail and watch ID as ordinary
// watches. Their source is a canonical ref, not a local job or event stream.
// caller names the Manager whose binding admitted the watch; the durable
// identity keys on the target triple plus that caller, so two Managers
// watching the same target keep distinct watches.
func (jm *jobManager) configureHubWatchContext(ctx context.Context, ref, instance, generation, caller string, selectors []string, completion bool) (watchResult, error) {
	if ref == "" || instance == "" || generation == "" || caller == "" {
		return watchResult{}, errors.New("source_not_watchable: authoritative hub target generation and Manager caller are required")
	}
	if err := validateHubWatchSelectors(selectors, completion); err != nil {
		return watchResult{}, err
	}
	a := watchArgs{Target: ref, Source: ref, Send: &watchSendArgs{To: runtimeMessageAliasCaller}, ReceiverSendInternal: true}
	cfg, err := newWatchConfig(a, jm.now(), "")
	if err != nil {
		return watchResult{}, err
	}
	cfg.events = canonicalWatchEvents(selectors)
	cfg.hubRef, cfg.hubInstance, cfg.hubGeneration, cfg.hubCaller, cfg.hubCompletion = ref, instance, generation, caller, completion
	cfg.configHash = fmt.Sprintf("%s:%s:%s:%s:%s:%v:%v", cfg.configHash, ref, instance, generation, caller, cfg.events, completion)
	key := watchKey{VisibleSessionID: jm.sessionID, Target: ref, SendTo: runtimeMessageAliasCaller}
	jm.mu.Lock()
	if err := ctx.Err(); err != nil {
		jm.mu.Unlock()
		return watchResult{}, err
	}
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
				// The indexed read classifies execution spans independently of the
				// item window, which may end with non-execution history turns.
				cfg.hubTurnID = read.LatestExecutionTurnID
			}
			cfg.hubTurnBound = cfg.hubTurnID != ""
		}
		cfg.hubSuspended = false
		jm.mu.Unlock()
		if update.Snapshot.Thread.Evener.AskPending {
			question := update.Metadata
			question.EntryID = "question:" + update.Snapshot.Thread.Evener.ActiveTurnID
			if pending := update.Snapshot.Thread.Evener.PendingQuestion; pending != nil {
				identity := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", pending.Question, pending.Count)))
				question.EntryID += ":" + hex.EncodeToString(identity[:])
			}
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
	terminal := cfg.hubCompletion && m.CausalKnown && m.TerminalAccepted != nil && *m.TerminalAccepted && m.EndTurn && (m.EndReason == "done" || m.EndReason == "")
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
	selected := slices.Contains(cfg.events, event)
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
	// The completion's dedup marker commits only after its terminal send
	// persists (in the end retry below): a failed end leaves the watch
	// registered — and reusable by an identical re-registration — where a
	// committed marker would suppress the replay that re-delivers the
	// completion once journal writes return.
	if !complete {
		cfg.hubSeen[identity] = true
	}
	trigger := event
	if complete {
		trigger = "completed: accepted terminal done"
	}
	root := events.SessionEvent{SessionID: jm.sessionID, Kind: events.EventCommunicate, Provenance: m.Causal}
	delivery := jm.watchSendSnapshot(cfg, identity, trigger, root).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, m.Causal))
	jm.mu.Unlock()
	if complete {
		if err := jm.endHubWatchUntilPersisted(watchID, "completed", &delivery); err != nil {
			return err
		}
		jm.mu.Lock()
		if _, current, ok := jm.watchConfigByIDLocked(watchID); ok && current == cfg {
			if cfg.hubSeen == nil {
				cfg.hubSeen = map[string]bool{}
			}
			cfg.hubSeen[identity] = true
		}
		jm.mu.Unlock()
		return nil
	}
	jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	if crossedBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return nil
}

// suspendHubWatch reports a single observation-loss episode without ending the
// durable watch or discarding a pending delivery.
func (jm *jobManager) suspendHubWatch(watchID string) error {
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok || cfg.hubRef == "" || cfg.hubSuspended {
		jm.mu.Unlock()
		return nil
	}
	cfg.hubSuspended = true
	delivery := jm.watchSendSnapshot(cfg, cfg.hubRef+"#interruption", "observation interrupted: waiting for recovery", events.SessionEvent{SessionID: jm.sessionID})
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

func (jm *jobManager) endHubWatch(watchID, reason string, completion *watchSendDelivery) error {
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok || cfg.hubRef == "" {
		jm.mu.Unlock()
		return nil
	}
	cfg.rejectingDelivery = true
	// The terminal delivery's eligibility must survive settlement of the
	// last prior pending send inside the unlock-to-record window below:
	// terminalDeliveryPending keeps forgetTerminalFlushIfEmptyLocked from
	// dropping the flush entry the terminal send needs to still count as
	// current. Cleared again on every completion and rollback path.
	cfg.terminalDeliveryPending = true
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
	if jm.testOnlyBeforeTerminalWatchSend != nil {
		jm.testOnlyBeforeTerminalWatchSend()
	}
	delivery = jm.snapshotWatchSendFrame(delivery)
	state, _, recorded, err := jm.recordWatchSend(delivery, []jobstore.Event{watchClearedEvent(jm, cfg, reason)})
	committed := state.DeliveryID != ""
	if !committed {
		jm.mu.Lock()
		cfg.rejectingDelivery = false
		cfg.terminalDeliveryPending = false
		delete(jm.terminalFlush, cfg)
		jm.mu.Unlock()
		if err == nil {
			err = errors.New("terminal watch send was not persisted")
		}
		return err
	}
	jm.mu.Lock()
	if jm.watches[key] != cfg {
		cfg.terminalDeliveryPending = false
		jm.forgetTerminalFlushIfEmptyLocked(cfg)
		jm.mu.Unlock()
		return nil
	}
	delete(jm.watches, key)
	jm.recordWatchEndedLocked(key, cfg, reason)
	cfg.rejectingDelivery = false
	jm.rememberDetachedPendingLocked(cfg)
	jm.mu.Unlock()
	if jm.testOnlyAfterHubWatchDetach != nil {
		jm.testOnlyAfterHubWatchDetach()
	}
	if recorded {
		if state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			if !jm.enqueueNotifications([]jobNotification{watchSendTokenNotification("", state)}) {
				jm.kick()
			}
		} else {
			jm.kick()
		}
	}
	jm.mu.Lock()
	cfg.terminalDeliveryPending = false
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
	jm.mu.Unlock()
	jm.publishWatchActivity()
	return err
}

// endHubWatchUntilPersisted keeps ownership of an end the way the completion
// path does: a failed journal write is not an observation failure, and the
// pending-send retention is disabled for terminal sends while the config
// rejects deliveries, so no other path can finish this end. The caller —
// completion or a consumer that is already exiting — retries the same end
// until it persists, the manager closes, or the watch is ended or replaced by
// another path. Abandoning the end after a bounded window would strand the
// registered watch with no consumer and no re-registration notice.
func (jm *jobManager) endHubWatchUntilPersisted(watchID, reason string, completion *watchSendDelivery) error {
	for {
		err := jm.endHubWatch(watchID, reason, completion)
		if err == nil {
			return nil
		}
		if jm.isClosing() || !managerHubWatchPresent(jm, watchID) {
			return err
		}
		delay := hubWatchRetryDelay
		if jm.testOnlyTerminalSendRetryDelay > 0 {
			delay = jm.testOnlyTerminalSendRetryDelay
		}
		time.Sleep(delay)
	}
}

// restore uses the existing restart end-notice rail, including generations
// that spoke before the runtime died; no continuing hub watch is restored.
func hubWatchLostAtRestart(w *jobstore.WatchRecord) bool { return w != nil && w.HubRef != "" }

// The roster's durable ID is reused across registrations. One setup owns the
// entire join/open/install transaction, including cleanup, so neither an old
// failure nor an uninstalled initial snapshot can affect a later registration.
func (jm *jobManager) lockHubRosterReplacement(ctx context.Context) (func(), error) {
	jm.mu.Lock()
	if jm.hubRosterReplacement == nil {
		jm.hubRosterReplacement = make(chan struct{}, 1)
	}
	gate := jm.hubRosterReplacement
	jm.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

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
	if request.Scope == "top_level" {
		var supplied map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &supplied); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		_, refPresent := supplied["ref"]
		_, completionPresent := supplied["completion"]
		_, eventsPresent := supplied["events"]
		if refPresent || completionPresent || eventsPresent {
			return nil, appwire.InvalidParams("scope cannot combine ref, completion or events")
		}
	} else {
		if request.Scope != "" {
			return nil, appwire.InvalidParams("unsupported watch scope")
		}
		if _, err := appwire.ParseRef(request.Ref); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
	}
	s.mu.Lock()
	client, enabled, closing := s.hubClient, s.hubEnabled, s.closing
	s.mu.Unlock()
	if client == nil || !enabled || closing {
		return nil, appwire.InvalidRequest("Manager tool unavailable")
	}
	if request.Scope == "" {
		if err := validateHubWatchSelectors(request.Events, request.Completion); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
	}
	state, err := client.State(ctx)
	if err != nil {
		return nil, err
	}
	if !state.Manager {
		return nil, appwire.InvalidRequest("session is not a Manager")
	}
	var rosterManager *jobManager
	if request.Scope == "top_level" {
		rosterManager, err = sessionJobManager(s)
		if err != nil {
			return nil, err
		}
		unlock, err := rosterManager.lockHubRosterReplacement(ctx)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	var rosterHandoffID string
	var rosterHandoffDone <-chan struct{}
	defer func() {
		if rosterHandoffID != "" {
			rosterManager.mu.Lock()
			_, cfg, ok := rosterManager.watchConfigByIDLocked(rosterHandoffID)
			owns := ok && cfg.rosterConsumerDone == rosterHandoffDone
			rosterManager.mu.Unlock()
			if owns {
				_ = rosterManager.endHubWatchUntilPersisted(rosterHandoffID, "roster replacement subscription failed", nil)
			}
		}
	}()
	if rosterManager != nil {
		jm := rosterManager
		jm.mu.Lock()
		cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: hubRosterWatchKey, SendTo: runtimeMessageAliasCaller}]
		var id string
		var done <-chan struct{}
		if cfg != nil {
			id, done = cfg.watchID, cfg.rosterConsumerDone
		}
		jm.mu.Unlock()
		if done != nil {
			rosterHandoffID, rosterHandoffDone = id, done
			s.cancelHubWatch(id)
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	stopSetupCancellation := context.AfterFunc(ctx, cancel)
	defer stopSetupCancellation()
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
	if (request.Scope == "top_level" && (first.Roster == nil || !first.Roster.Initial)) || (request.Scope == "" && (first.Snapshot == nil || first.Metadata.Ref != request.Ref || first.Metadata.InstanceID == "" || first.Metadata.GenerationID == "")) {
		cancel()
		return nil, appwire.Unavailable("observation has no authoritative target generation")
	}
	jm, err := sessionJobManager(s)
	if err != nil {
		cancel()
		return nil, err
	}
	var result watchResult
	if request.Scope == "top_level" {
		result, err = jm.configureHubRosterWatch(ctx, state.Ref)
	} else {
		result, err = jm.configureHubWatchContext(ctx, request.Ref, first.Metadata.InstanceID, first.Metadata.GenerationID, state.Ref, request.Events, request.Completion)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	// Stop the setup-context bridge before installing the subscription. This
	// resolves the handoff: cancellation that already won must clear the durable
	// registration, while cancellation after this point no longer owns the
	// established watch.
	stopSetupCancellation()
	if err := ctx.Err(); err != nil {
		cancel()
		if endErr := jm.endHubWatchUntilPersisted(result.WatchID, "setup canceled after registration", nil); endErr != nil {
			return nil, errors.Join(err, endErr)
		}
		return nil, err
	}
	if err := watchCtx.Err(); err != nil {
		if endErr := jm.endHubWatchUntilPersisted(result.WatchID, "setup canceled after registration", nil); endErr != nil {
			return nil, errors.Join(err, endErr)
		}
		return nil, err
	}
	var done chan struct{}
	if request.Scope == "top_level" {
		done = make(chan struct{})
	}
	installed := s.installHubWatchSubscription(jm, result.WatchID, cancel, done)
	// A rejected install means another registration already installed a consumer
	// or the durable watch ended; do not end the winner in the deferred cleanup.
	rosterHandoffID = ""
	if installed {
		if request.Scope == "top_level" {
			// Only the installed transport may reset revisions or apply its initial.
			// The replacement gate keeps another setup out until this is complete.
			rosterHandoffID, rosterHandoffDone = result.WatchID, done
			jm.resetHubRosterRevision(result.WatchID)
			if err := jm.onHubRoster(result.WatchID, first); err != nil {
				s.cancelHubWatch(result.WatchID)
				close(done)
				return nil, err
			}
			rosterHandoffID = ""
		}
		go s.consumeHubWatch(watchCtx, result.WatchID, request, state.Ref, state.InstanceID, updates, first, done)
	}
	return marshalWatchResult(result, jobToolResultDefaultMaxChar)
}

func (s *Session) installHubWatchSubscription(jm *jobManager, id string, cancel context.CancelFunc, done ...chan struct{}) bool {
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
	if len(done) != 0 && done[0] != nil {
		jm.mu.Lock()
		if _, cfg, ok := jm.watchConfigByIDLocked(id); ok {
			cfg.rosterConsumerDone = done[0]
		}
		jm.mu.Unlock()
	}
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
func (s *Session) consumeHubWatch(ctx context.Context, id string, request appwire.SessionWatchRequest, callerRef, callerInstance string, updates <-chan appwire.SessionWatchUpdate, initial appwire.SessionWatchUpdate, done ...chan struct{}) {
	if len(done) != 0 && done[0] != nil {
		defer close(done[0])
	}
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
		if request.Scope == "top_level" {
			return jm.onHubRoster(id, update)
		}
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
			var result appwire.SessionOperationResponse
			for failures := 1; ; failures++ {
				readCtx, cancel := context.WithTimeout(ctx, hubWatchReadBudget)
				var err error
				result, err = client.Invoke(readCtx, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: ref, HeldSnapshot: held, AnswerContinuation: query}})
				if err == nil {
					err = readCtx.Err()
				}
				cancel()
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				if err == nil {
					break
				}
				if !retryableHubContinuationRead(err) || failures >= hubWatchStateFailureLimit {
					return false, err
				}
				if err := jm.suspendHubWatch(id); err != nil {
					return false, err
				}
				// Retain this exact query and held cut, not a newer snapshot that
				// may no longer contain the failed terminal candidate. Only call
				// failures retry, never missing or incompatible evidence below.
				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(hubWatchRetryDelay):
				}
				if !managerHubWatchPresent(jm, id) {
					return false, context.Canceled
				}
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
			jm.resumeHubWatch(id)
			return verdict.Matches, nil
		})
	}
	for {
		if err := observe(initial); err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = jm.endHubWatchUntilPersisted(id, "observation snapshot failed: "+err.Error(), nil)
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
				if request.Scope == "" && update.Snapshot != nil && (update.Metadata.Ref != initial.Metadata.Ref || update.Metadata.InstanceID != initial.Metadata.InstanceID || update.Metadata.GenerationID != initial.Metadata.GenerationID) {
					_ = jm.endHubWatchUntilPersisted(id, "target replaced; re-register", nil)
					return
				}
				if err := observe(update); err != nil {
					if ctx.Err() != nil {
						return
					}
					_ = jm.endHubWatchUntilPersisted(id, "observation failed: "+err.Error(), nil)
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
			_ = jm.endHubWatchUntilPersisted(id, "observation interruption failed: "+err.Error(), nil)
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
					_ = jm.endHubWatchUntilPersisted(id, "Manager binding no longer serves this session; re-register", nil)
					return
				}
				if signal == boundSignal {
					_ = jm.endHubWatchUntilPersisted(id, "observation subscription ended unexpectedly; re-register", nil)
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
					if (request.Scope == "top_level" && (snapshot.Roster == nil || !snapshot.Roster.Initial)) || (request.Scope == "" && (snapshot.Snapshot == nil || snapshot.Metadata.InstanceID != initial.Metadata.InstanceID || snapshot.Metadata.GenerationID != initial.Metadata.GenerationID || snapshot.Metadata.Ref != initial.Metadata.Ref)) {
						_ = jm.endHubWatchUntilPersisted(id, "target replaced or observation unavailable; re-register", nil)
						return
					}
					if request.Scope == "top_level" {
						jm.resetHubRosterRevision(id)
					}
					updates, initial, boundSignal = rebound, snapshot, signal
					break
				}
				if permanentHubWatchError(watchErr) {
					_ = jm.endHubWatchUntilPersisted(id, "observation subscription rejected: "+watchErr.Error(), nil)
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
				_ = jm.endHubWatchUntilPersisted(id, "observation unreachable: "+stateErr.Error()+"; re-register", nil)
				return
			}
		}
		if ctx.Err() != nil || !managerHubWatchPresent(jm, id) {
			return
		}
	}
}

// The daemon maps an untyped index-lock error to InternalError. Retry that
// ambiguous read failure finitely, but do not retry typed permanent history or
// request errors. Response evidence is validated outside this retry boundary.
func retryableHubContinuationRead(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || appwire.IsTransportFailure(err) {
		return true
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	switch data.EvenerErrorInfo {
	case appwire.ErrorTranscriptIndexLocked, appwire.ErrorTranscriptIndexRebuilding:
		return true
	case "", appwire.ErrorInternal, appwire.ErrorSessionUnavailable, appwire.ErrorActionUnavailable:
		return wire.Code == appwire.CodeInternalError || wire.Code == appwire.CodeUnavailable
	default:
		return false
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

func (jm *jobManager) isClosing() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.closing
}
