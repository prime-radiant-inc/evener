package hubcore

import (
	"crypto/rand"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/spf13/afero"
)

// ResumeLocks hands out one mutex per session id so concurrent resume attempts
// for the same session serialize. Both the REST send path and the RPC
// auto-resume path share a single instance (via WebConfig.ResumeLocks) so a
// resume triggered on one transport blocks a racing resume on the other,
// preventing two daemons from being spawned for one exited session (kata sm1a).
type ResumeLocks struct {
	persistenceMu sync.Mutex
	store         *recoveryStore
	mu            sync.Mutex
	locks         map[string]*sync.Mutex
	recovery      map[string]SessionRecoveryState
	sequence      uint64
}

// NewResumeLocks returns an empty registry ready for use.
func NewResumeLocks() *ResumeLocks {
	return &ResumeLocks{locks: map[string]*sync.Mutex{}}
}

// NewPersistentResumeLocks restores explicit-resume authority before serving.
// Call NewResumeLocks explicitly for an in-memory registry; an empty root is an error.
func NewPersistentResumeLocks(stateRoot string) (*ResumeLocks, error) {
	store, err := openRecoveryStore(afero.NewOsFs(), stateRoot)
	if err != nil {
		return nil, err
	}
	r := NewResumeLocks()
	r.store = store
	r.recovery = make(map[string]SessionRecoveryState)
	groups := make(map[string]*sessionRecoveryGroup)
	for alias, authority := range store.state {
		id := authority.Group
		group := groups[id]
		if group == nil {
			group = &sessionRecoveryGroup{}
			groups[id] = group
		}
		group.aliases = append(group.aliases, alias)
		r.recovery[alias] = SessionRecoveryState{ResumeRequired: true, ExitConfirmed: authority.ExitConfirmed, group: group, durableGroup: id, ResumeSessionID: authority.SessionID}
	}
	return r, nil
}

// PersistForceStop records verified aliases and their current transcript under
// ownership locks before signaling. Committed intent survives signaling failure.
func (r *ResumeLocks) PersistForceStop(aliases []string, sessionID string) error {
	if len(aliases) == 0 {
		return errors.New("recovery alias set is empty")
	}
	aliases = slices.Compact(slices.Sorted(slices.Values(aliases)))
	for _, alias := range aliases {
		if !validRecoveryAlias(alias) {
			return errors.New("invalid recovery alias")
		}
	}
	if !validRecoveryAlias(sessionID) || !slices.Contains(aliases, sessionID) {
		return errors.New("recovery target must be a verified ownership alias")
	}
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	id := rand.Text()
	committed := true
	var err error
	if r.store != nil {
		next := maps.Clone(r.store.state)
		for _, alias := range aliases {
			next[alias] = recoveryAuthority{Group: id, SessionID: sessionID}
		}
		committed, err = r.store.commit(next)
	}
	if committed {
		r.mu.Lock()
		if r.recovery == nil {
			r.recovery = make(map[string]SessionRecoveryState)
		}
		group := &sessionRecoveryGroup{aliases: slices.Clone(aliases)}
		for _, alias := range aliases {
			state := r.recovery[alias]
			state.group = group
			state.ResumeRequired = true
			state.ExitConfirmed = false
			state.durableGroup = id
			state.ResumeSessionID = sessionID
			r.recovery[alias] = state
		}
		r.mu.Unlock()
	}
	return err
}

// ConfirmForceStop records proven process exit without acknowledging explicit
// Resume. Descendants may recover independently once their owner cannot run.
func (r *ResumeLocks) ConfirmForceStop(sessionID string) error {
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	state := r.recovery[sessionID]
	eligible := make(map[string]SessionRecoveryState)
	if state.group != nil {
		for _, alias := range state.group.aliases {
			current := r.recovery[alias]
			if current.group == state.group {
				eligible[alias] = current
			}
		}
	}
	r.mu.Unlock()
	if len(eligible) == 0 {
		return errors.New("session has no committed recovery authority")
	}
	if r.store != nil {
		next := maps.Clone(r.store.state)
		for alias, current := range eligible {
			authority := next[alias]
			if authority.Group != current.durableGroup {
				return errors.New("session recovery authority changed")
			}
			authority.ExitConfirmed = true
			next[alias] = authority
		}
		if _, err := r.store.commit(next); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for alias, previous := range eligible {
		current := r.recovery[alias]
		if current.group == previous.group {
			current.ExitConfirmed = true
			r.recovery[alias] = current
		}
	}
	return nil
}

// HasUnconfirmedRecovery keeps admission checks active on fresh connections and
// after hub recreation while a stopped owner may still be executing delegates.
func (r *ResumeLocks) HasUnconfirmedRecovery() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range r.recovery {
		if state.Stopping > 0 || (state.ResumeRequired && !state.ExitConfirmed) {
			return true
		}
	}
	return false
}

// For returns the mutex for sessionID, creating it on first use. Repeated calls
// with the same id return the same mutex, so callers serialize against each
// other regardless of which path (REST or RPC) they came in on.
func (r *ResumeLocks) For(sessionID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.locks[sessionID]
	if !ok {
		m = &sync.Mutex{}
		r.locks[sessionID] = m
	}
	return m
}

// SessionRecoveryState is the action admission state shared by every transport.
// Epoch changes invalidate actions that were waiting for session ownership.
type SessionRecoveryState struct {
	Epoch                uint64
	LastRecoverySequence uint64
	Stopping             int
	ResumeRequired       bool
	ExitConfirmed        bool
	ResumeSessionID      string
	group                *sessionRecoveryGroup
	durableGroup         string
}

type sessionRecoveryGroup struct {
	aliases           []string
	resolvedSessionID string
}

func (r *ResumeLocks) RecoveryState(sessionID string) SessionRecoveryState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recovery[sessionID]
}

// RecoveryAliases returns the verified ownership group, including after explicit
// recovery completes. The aliases reserve one daemon without choosing its target.
func (r *ResumeLocks) RecoveryAliases(sessionID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if group := r.recovery[sessionID].group; group != nil {
		return slices.Clone(group.aliases)
	}
	return []string{sessionID}
}

// HasSeparatePendingRecovery identifies an obligation the requested alias cannot
// acknowledge. Repeated stops of the same transcript still create a new group.
func (r *ResumeLocks) HasSeparatePendingRecovery(sessionID, targetID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	requested, target := r.recovery[sessionID], r.recovery[targetID]
	return (target.ResumeRequired || target.Stopping > 0) && target.group != requested.group
}

// RecordResolvedSession retains alias routing after successful Resume without
// recreating a recovery obligation. Fresh rendezvous evidence takes priority.
func (r *ResumeLocks) RecordResolvedSession(sessionID, target string, epoch uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.recovery[sessionID]
	if target != "" && state.Epoch == epoch && state.Stopping == 0 && !state.ResumeRequired && state.group != nil {
		state.group.resolvedSessionID = target
	}
}

func (r *ResumeLocks) ResolvedSessionID(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if group := r.recovery[sessionID].group; group != nil {
		return group.resolvedSessionID
	}
	return ""
}

// RecoverySequence is captured once when a transport is established. A
// request read later cannot turn unread pre-recovery input into fresh intent.
func (r *ResumeLocks) RecoverySequence() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sequence
}

// BeginForceStop blocks new actions and invalidates existing ownership waiters.
// Temporary fences preserve committed alias routing. PersistForceStop installs
// a new group only once its recovery authority may have reached durable storage.
func (r *ResumeLocks) BeginForceStop(aliases []string) func(bool) {
	r.mu.Lock()
	if r.recovery == nil {
		r.recovery = make(map[string]SessionRecoveryState)
	}
	r.sequence++
	for _, id := range aliases {
		state := r.recovery[id]
		state.Epoch++
		state.LastRecoverySequence = r.sequence
		state.Stopping++
		r.recovery[id] = state
	}
	r.mu.Unlock()
	return func(stopped bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.sequence++
		for _, id := range aliases {
			state := r.recovery[id]
			state.Stopping--
			state.LastRecoverySequence = r.sequence
			state.ResumeRequired = state.ResumeRequired || stopped
			r.recovery[id] = state
		}
	}
}

// ExplicitResumeCompleted clears every alias only if no newer recovery began.
func (r *ResumeLocks) ExplicitResumeCompleted(sessionID string, epoch uint64) error {
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	state := r.recovery[sessionID]
	if state.Epoch != epoch || state.Stopping != 0 || state.group == nil {
		r.mu.Unlock()
		return nil
	}
	eligible := make(map[string]SessionRecoveryState)
	for _, id := range state.group.aliases {
		alias := r.recovery[id]
		if alias.group == state.group && alias.Stopping == 0 {
			eligible[id] = alias
		}
	}
	r.mu.Unlock()
	if r.store != nil {
		next := maps.Clone(r.store.state)
		for id, alias := range eligible {
			if next[id].Group == alias.durableGroup {
				delete(next, id)
			}
		}
		// Retry the write even when a previous removal was renamed before a sync
		// error: only a fully synced replacement establishes clear completion.
		if _, err := r.store.commit(next); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, previous := range eligible {
		alias := r.recovery[id]
		if alias.group != previous.group || alias.Epoch != previous.Epoch || alias.Stopping != 0 {
			continue
		}
		alias.ResumeRequired = false
		alias.durableGroup = ""
		alias.ResumeSessionID = ""
		r.recovery[id] = alias
	}
	return nil
}
