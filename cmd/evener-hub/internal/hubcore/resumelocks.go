package hubcore

import (
	"context"
	"crypto/rand"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
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
	locks         map[string]*ResumeMutex
	active        map[string]map[*ActiveResume]struct{}
	recovery      map[string]SessionRecoveryState
	sequence      uint64
}

// NewResumeLocks returns an empty registry ready for use.
func NewResumeLocks() *ResumeLocks {
	return &ResumeLocks{locks: map[string]*ResumeMutex{}}
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
func (r *ResumeLocks) For(sessionID string) *ResumeMutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.locks[sessionID]
	if !ok {
		m = &ResumeMutex{token: make(chan struct{}, 1)}
		m.token <- struct{}{}
		r.locks[sessionID] = m
	}
	return m
}

// ResumeMutex keeps one ownership token shared with ordinary Lock callers.
// Context cancellation never needs a goroutine waiting to acquire and release
// a mutex after its caller has already gone away.
type ResumeMutex struct {
	token chan struct{}
}

func (m *ResumeMutex) Lock() { <-m.token }

func (m *ResumeMutex) Unlock() {
	select {
	case m.token <- struct{}{}:
	default:
		panic("unlock of unlocked resume mutex")
	}
}

func (m *ResumeMutex) TryLock() bool {
	select {
	case <-m.token:
		return true
	default:
		return false
	}
}

func (m *ResumeMutex) LockContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}

// UnlockAliases releases aliases in reverse acquisition order. It is the
// shared release path behind LockAliases and TryLockAliases.
func (r *ResumeLocks) UnlockAliases(aliases []string) {
	for _, alias := range slices.Backward(aliases) {
		r.For(alias).Unlock()
	}
}

// LockAliases acquires every alias in order through ctx and returns the
// group's release function. If a later alias blocks past cancellation it
// releases the prefix already held instead of hanging and retaining the
// earlier aliases.
func (r *ResumeLocks) LockAliases(ctx context.Context, aliases []string) (release func(), err error) {
	acquired := 0
	defer func() {
		if err != nil {
			r.UnlockAliases(aliases[:acquired])
		}
	}()
	for _, alias := range aliases {
		if err = r.For(alias).LockContext(ctx); err != nil {
			return nil, err
		}
		acquired++
	}
	return func() { r.UnlockAliases(aliases) }, nil
}

// TryLockAliases takes every alias without blocking. It reports true only when
// all of them are held; on any failure it releases the prefix it took.
func (r *ResumeLocks) TryLockAliases(aliases []string) (release func(), ok bool) {
	acquired := 0
	for _, alias := range aliases {
		if !r.For(alias).TryLock() {
			r.UnlockAliases(aliases[:acquired])
			return nil, false
		}
		acquired++
	}
	return func() { r.UnlockAliases(aliases) }, true
}

var ErrResumeInvalidated = errors.New("session recovery changed before resume ownership was acquired")

// ActiveResume is only the lifetime of an in-flight hub Resume. It has no
// durable identity and is removed when cleanup and ownership release finish.
type ActiveResume struct {
	owner         *ResumeLocks
	aliases       []string
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	cleanupErr    error // guarded by owner.mu, including after done closes
	handlerDone   bool
	childPrepared bool
	childReaped   bool
	launchFailed  bool
	cleanupDone   chan struct{}
}

func (a *ActiveResume) Context() context.Context { return a.ctx }

// CleanupDone closes once the handler has completed and any failed launch's
// child has been confirmed reaped, or after a successful handoff. It is not the
// handler completion edge.
func (a *ActiveResume) CleanupDone() <-chan struct{} { return a.cleanupDone }

// BeforeLaunch runs immediately before Start, while the caller owns all aliases.
// It refuses to launch while another in-flight operation on the same aliases has
// unconfirmed child cleanup.
func (a *ActiveResume) BeforeLaunch() error {
	r := a.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return err
	}
	for _, alias := range a.aliases {
		for other := range r.active[alias] {
			if other != a {
				if err := other.cleanupErr; err != nil {
					return err
				}
			}
		}
	}
	a.childPrepared = true
	return nil
}

// ChildReaped is called by the original (and only) child Wait reader. Start
// failure also calls it: no child was created, so no live ownership can remain.
func (a *ActiveResume) ChildReaped() {
	r := a.owner
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	a.childReaped = true
	a.settleLocked()
}

// LaunchFinished distinguishes failed startup from a ready daemon handed off
// to ordinary verified discovery. The waiter may arrive before or after this.
func (a *ActiveResume) LaunchFinished(failed bool, cleanupErr error) error {
	r := a.owner
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	a.launchFailed = failed
	a.cleanupErr = errors.Join(a.cleanupErr, cleanupErr)
	a.settleLocked()
	return a.cleanupErr
}

// settleLocked releases alias ownership once the handler has completed and any
// failed launch's child has been confirmed reaped. Stop's temporary epochs do
// not change process identity.
func (a *ActiveResume) settleLocked() {
	r := a.owner
	if !a.handlerDone || (a.childPrepared && a.launchFailed && !a.childReaped) {
		return
	}
	for _, alias := range a.aliases {
		delete(r.active[alias], a)
		if len(r.active[alias]) == 0 {
			delete(r.active, alias)
		}
	}
	select {
	case <-a.cleanupDone:
	default:
		close(a.cleanupDone)
	}
}

// Complete must run after child cleanup and after releasing every alias lock.
// A normal launch failure is returned to the Resume caller, not cleanupErr;
// only failure to confirm cleanup can prevent a waiting Stop from proceeding.
func (a *ActiveResume) Complete(cleanupErr error) {
	a.owner.persistenceMu.Lock()
	defer a.owner.persistenceMu.Unlock()
	a.owner.mu.Lock()
	defer a.owner.mu.Unlock()
	if a.cleanupErr == nil {
		a.cleanupErr = cleanupErr
	}
	a.handlerDone = true
	a.cancel()
	close(a.done)
	a.settleLocked()
}

// ResumeCleanupError reports retained child failures to fresh connections.
func (r *ResumeLocks) ResumeCleanupError(aliases []string) error {
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		for active := range r.active[alias] {
			if active.handlerDone {
				if err := active.cleanupErr; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ResumeCleanupErrorStrict reports unconfirmed child cleanup for every active
// Resume, including one whose handler has not yet completed. Ordinary shutdown
// uses it so it cannot fall through to the source shutdown path while a failed
// launch's child cleanup is unconfirmed — the same LaunchFinished-before-Complete
// window RegisterResume already closes. ResumeCleanupError keeps its
// handlerDone gate for fresh connections, which must not refuse an operation
// because of a Resume that is still completing normally.
func (r *ResumeLocks) ResumeCleanupErrorStrict(aliases []string) error {
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		for active := range r.active[alias] {
			if err := active.cleanupErr; err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *ResumeLocks) RegisterResume(ctx context.Context, target string, aliases []string, epochs map[string]uint64) (*ActiveResume, error) {
	aliases = slices.Compact(slices.Sorted(slices.Values(aliases)))
	if !validRecoveryAlias(target) || !slices.Contains(aliases, target) {
		return nil, errors.New("resume target must be an ownership alias")
	}
	for _, alias := range aliases {
		if !validRecoveryAlias(alias) {
			return nil, errors.New("invalid resume ownership alias")
		}
		if _, ok := epochs[alias]; !ok {
			return nil, errors.New("resume ownership alias has no recovery epoch")
		}
	}
	// Admission must take the same per-alias ownership tokens a confirmed-stopped
	// no-op holds across its final HasActiveResume check and success return. That
	// path keeps the aliases reserved until it decides, so registering without
	// the tokens lets a new explicit Resume land inside that window, wait on the
	// held alias lock, and launch after shutdown already reported success.
	release, err := r.LockAliases(ctx, aliases)
	if err != nil {
		return nil, err
	}
	defer release()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, alias := range aliases {
		state := r.recovery[alias]
		for active := range r.active[alias] {
			if err := active.cleanupErr; err != nil {
				// A retained child-cleanup failure is a retryable "cleanup remains
				// unconfirmed" state. It must reach the client as Unavailable, not
				// unwrapped and mapped to InternalError.
				return nil, appwire.Unavailable(err.Error())
			}
		}
		if state.Epoch != epochs[alias] || state.Stopping != 0 {
			return nil, ErrResumeInvalidated
		}
	}
	opCtx, cancel := context.WithCancel(ctx)
	a := &ActiveResume{owner: r, aliases: aliases, ctx: opCtx, cancel: cancel, done: make(chan struct{}), cleanupDone: make(chan struct{})}
	if r.active == nil {
		r.active = make(map[string]map[*ActiveResume]struct{})
	}
	for _, alias := range aliases {
		if r.active[alias] == nil {
			r.active[alias] = make(map[*ActiveResume]struct{})
		}
		r.active[alias][a] = struct{}{}
	}
	return a, nil
}

// ResumeStop owns a temporary admission fence, not stopped-process authority.
// The ordinary Stop path still has to prove which process owns the session.
type ResumeStop struct {
	active  []*ActiveResume
	release func(bool)
}

func (s *ResumeStop) Wait(ctx context.Context) error {
	var cleanupErr error
	for _, active := range s.active {
		select {
		case <-ctx.Done():
			return errors.Join(cleanupErr, ctx.Err())
		case <-active.done:
			active.owner.mu.Lock()
			cleanupErr = errors.Join(cleanupErr, active.cleanupErr)
			active.owner.mu.Unlock()
		}
	}
	return cleanupErr
}

func (s *ResumeStop) Release() { s.release(false) }

// HasActiveResume is a read-only check for shutdown's confirmed-stopped no-op.
// Shutdown must not cancel a restore or manufacture fresh recovery authority.
func (r *ResumeLocks) HasActiveResume(aliases []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		if len(r.active[alias]) != 0 {
			return true
		}
	}
	return false
}

// ActiveResumeStopAliases resolves the ownership group BeginActiveResumeStop
// would fence and drain for alias — the transitive overlap set of the active
// Resumes reachable from it — without canceling anything, and reports nil when
// no Resume is active. Force stop validates every alias's deletion fence
// before it cancels, so a request a sibling-alias fence will refuse does not
// abort the in-flight Resume first.
func (r *ResumeLocks) ActiveResumeStopAliases(alias string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.active[alias]) == 0 {
		return nil
	}
	aliases, _ := r.activeResumeOverlapLocked(alias)
	return aliases
}

func (r *ResumeLocks) BeginActiveResumeStop(alias string) *ResumeStop {
	r.mu.Lock()
	if len(r.active[alias]) == 0 {
		r.mu.Unlock()
		return nil
	}
	aliases, actives := r.activeResumeOverlapLocked(alias)
	stop := &ResumeStop{active: actives}
	stop.release = r.beginForceStopLocked(aliases)
	r.mu.Unlock()
	for _, active := range stop.active {
		active.cancel()
	}
	return stop
}

// activeResumeOverlapLocked follows overlapping reservations as one ownership
// set, returning the sorted alias group and the active Resumes within it. A
// waiter reached through a stable/current alias must not retain a live context
// or escape the fence merely because Stop named the other alias. Callers hold
// r.mu.
func (r *ResumeLocks) activeResumeOverlapLocked(alias string) ([]string, []*ActiveResume) {
	seenAliases := make(map[string]bool)
	seenActive := make(map[*ActiveResume]bool)
	queue := []string{alias}
	var actives []*ActiveResume
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if seenAliases[current] {
			continue
		}
		seenAliases[current] = true
		for active := range r.active[current] {
			if !seenActive[active] {
				seenActive[active] = true
				actives = append(actives, active)
				queue = append(queue, active.aliases...)
			}
		}
	}
	return slices.Sorted(maps.Keys(seenAliases)), actives
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
	defer r.mu.Unlock()
	return r.beginForceStopLocked(aliases)
}

func (r *ResumeLocks) beginForceStopLocked(aliases []string) func(bool) {
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

// InvalidateResumeAdmission advances every alias's admission epoch without
// installing a stop fence: Stopping, ResumeRequired, and the connection-level
// recovery sequence are untouched. A confirmed-stopped no-op calls it while it
// still holds the alias tokens, so a Resume registration already waiting for
// those tokens cannot launch on an admission snapshot taken before the no-op
// reported the session stopped; it is refused and must re-admit afterward.
func (r *ResumeLocks) InvalidateResumeAdmission(aliases []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range aliases {
		state := r.recovery[id]
		state.Epoch++
		r.recovery[id] = state
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
