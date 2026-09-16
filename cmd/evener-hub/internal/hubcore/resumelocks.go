package hubcore

import (
	"context"
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
		r.recovery[alias] = SessionRecoveryState{ResumeRequired: true, ExitConfirmed: authority.ExitConfirmed, LaunchPending: authority.LaunchPending, group: group, durableGroup: id, ResumeSessionID: authority.SessionID}
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
			state.LaunchPending = false
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

var ErrResumeInvalidated = errors.New("session recovery changed before resume ownership was acquired")

// ActiveResume is only the lifetime of an in-flight hub Resume. It has no
// durable identity and is removed when cleanup and ownership release finish.
type ActiveResume struct {
	owner          *ResumeLocks
	target         string
	aliases        []string
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	cleanupErr     error // guarded by owner.mu, including after done closes
	handlerDone    bool
	childPrepared  bool
	childReaped    bool
	launchFinished bool
	launchFailed   bool
	proofSettled   bool
	proofErr       error
	proof          map[string]SessionRecoveryState
	cleanupDone    chan struct{}
}

func (a *ActiveResume) Context() context.Context { return a.ctx }

// CleanupDone closes after failed child cleanup and its recovery proof have
// settled, or after a successful handoff. It is not the handler completion edge.
func (a *ActiveResume) CleanupDone() <-chan struct{} { return a.cleanupDone }

// BeforeLaunch runs immediately before Start, while the caller owns all aliases.
// A previous process's exit proof must never describe this new child, including
// when this hub dies before the child publishes a rendezvous entry.
func (a *ActiveResume) BeforeLaunch() error {
	r := a.owner
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return err
	}
	for _, alias := range a.aliases {
		for other := range r.active[alias] {
			if other != a {
				if err := errors.Join(other.cleanupErr, other.proofErr); err != nil {
					return err
				}
			}
		}
	}
	proof, err := r.beginLaunchProofLocked(a.target, a.aliases)
	if err != nil {
		if len(proof) != 0 {
			// Start will not run. Retain an uncertain invalidation until the
			// old, still-valid exit proof can be durably restored.
			a.proof = proof
			a.childPrepared, a.childReaped = true, true
			a.launchFinished, a.launchFailed = true, true
			a.proofErr = err
		}
		return err
	}
	a.proof = proof
	a.childPrepared = true
	return nil
}

// beginLaunchProofLocked is the durable half of BeforeLaunch, shared with every
// other launch path. It records a launch in progress and invalidates the prior
// owner's exit proof for every verified alias in the group, which is what stops
// a previous process's proof from describing the child about to start. Both
// registry locks are held. The returned proof is non-empty exactly when the
// invalidation may already be durable, so a caller whose commit failed can
// still restore the proof it replaced.
func (r *ResumeLocks) beginLaunchProofLocked(target string, aliases []string) (map[string]SessionRecoveryState, error) {
	proof := make(map[string]SessionRecoveryState)
	for _, alias := range aliases {
		state := r.recovery[alias]
		if !state.ResumeRequired {
			continue
		}
		if !state.ExitConfirmed {
			return nil, errors.New("resume owner exit is unconfirmed; verify the existing process before launching a replacement")
		}
		if state.group == nil || state.ResumeSessionID != target || state.group != r.recovery[target].group {
			return nil, errors.New("resume recovery authority changed before launch")
		}
		for _, member := range state.group.aliases {
			current := r.recovery[member]
			if !slices.Contains(aliases, member) || current.group != state.group || current.durableGroup != state.durableGroup || current.ResumeSessionID != target || !current.ResumeRequired || !current.ExitConfirmed {
				return nil, errors.New("resume recovery aliases changed before launch")
			}
			proof[member] = current
		}
	}
	if len(proof) == 0 {
		return nil, nil
	}
	if r.store != nil {
		next := maps.Clone(r.store.state)
		for alias, state := range proof {
			authority := next[alias]
			if authority.Group != state.durableGroup || authority.SessionID != target || !authority.ExitConfirmed {
				return nil, errors.New("durable resume recovery authority changed before launch")
			}
			authority.ExitConfirmed = false
			authority.LaunchPending = true
			next[alias] = authority
		}
		committed, err := r.store.commit(next)
		if err != nil {
			if committed {
				for alias := range proof {
					state := r.recovery[alias]
					state.ExitConfirmed = false
					state.LaunchPending = true
					r.recovery[alias] = state
				}
				return proof, err
			}
			return nil, err
		}
	}
	for alias := range proof {
		state := r.recovery[alias]
		state.ExitConfirmed = false
		state.LaunchPending = true
		r.recovery[alias] = state
	}
	return proof, nil
}

// restoreLaunchProofLocked re-confirms the exit proof a launch invalidated once
// that launch has produced no child, and clears its durable launch intent, so
// the ordinary confirmed-stopped resume and force-stop paths can run again. A
// changed group, target, or membership means a newer authority owns the
// aliases: it is left completely untouched. Both registry locks are held.
func (r *ResumeLocks) restoreLaunchProofLocked(proof map[string]SessionRecoveryState, target string) error {
	matching := true
	for alias, previous := range proof {
		current := r.recovery[alias]
		if current.group != previous.group || current.durableGroup != previous.durableGroup || current.ResumeSessionID != target || !current.ResumeRequired || !slices.Equal(current.group.aliases, previous.group.aliases) {
			matching = false
		}
	}
	if matching && r.store != nil && len(proof) != 0 {
		next := maps.Clone(r.store.state)
		for alias, previous := range proof {
			authority := next[alias]
			if authority.Group != previous.durableGroup || authority.SessionID != target {
				return errors.New("durable resume cleanup authority changed")
			}
			authority.ExitConfirmed = true
			authority.LaunchPending = false
			next[alias] = authority
		}
		if _, err := r.store.commit(next); err != nil {
			return err
		}
	}
	if matching {
		for alias := range proof {
			state := r.recovery[alias]
			state.ExitConfirmed = true
			state.LaunchPending = false
			r.recovery[alias] = state
		}
	}
	return nil
}

// LaunchGuard is the durable pre-launch invalidation for a launch path that
// owns no registered ActiveResume lifetime. BeforeLaunch performs the same work
// for an explicit or automatic resume; a path that holds no resume to own
// guards its launch with this instead, so the invariant documented on
// BeforeLaunch holds for every launch.
type LaunchGuard struct {
	owner  *ResumeLocks
	target string
	proof  map[string]SessionRecoveryState
}

// BeginLaunchGuard durably records a launch in progress and invalidates the
// prior owner's exit proof for every verified alias, while the caller owns all
// of them. Failed must run when the guarded launch produced no child.
func (r *ResumeLocks) BeginLaunchGuard(ctx context.Context, target string, aliases []string) (*LaunchGuard, error) {
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proof, err := r.beginLaunchProofLocked(target, aliases)
	if err != nil {
		if len(proof) != 0 {
			// The invalidation may already be durable and no child will start:
			// put the previous exit proof back rather than retaining a fence.
			err = errors.Join(err, r.restoreLaunchProofLocked(proof, target))
		}
		return nil, err
	}
	return &LaunchGuard{owner: r, target: target, proof: proof}, nil
}

// Failed restores the prior exit proof after a guarded launch produced no
// child. It is a no-op after a successful handoff, and for a session that
// carried no exit proof to invalidate.
func (g *LaunchGuard) Failed() error {
	if g == nil || g.owner == nil || len(g.proof) == 0 {
		return nil
	}
	g.owner.persistenceMu.Lock()
	defer g.owner.persistenceMu.Unlock()
	g.owner.mu.Lock()
	defer g.owner.mu.Unlock()
	return g.owner.restoreLaunchProofLocked(g.proof, g.target)
}

// RecoverInterruptedLaunches settles durable launch intents left by a hub that
// died after a launch durably invalidated a prior exit proof but before the
// child started or published a rendezvous claim. claimed reports whether any
// alias of a pending group is still claimed; a claimed group is left fenced so
// a previous process's exit proof can never describe a new child. Every
// unclaimed group's launch intent is cleared and its prior exit proof restored,
// which is what lets the ordinary resume and force-stop paths run instead of
// rejecting the session indefinitely. It returns the first discovery error
// rather than guessing about a group it could not check.
func (r *ResumeLocks) RecoverInterruptedLaunches(claimed func(aliases []string) (bool, error)) error {
	if r == nil || claimed == nil {
		return nil
	}
	type pendingLaunch struct {
		group   *sessionRecoveryGroup
		target  string
		aliases []string
	}
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	seen := make(map[*sessionRecoveryGroup]bool)
	var pending []pendingLaunch
	for _, alias := range slices.Sorted(maps.Keys(r.recovery)) {
		state := r.recovery[alias]
		if !state.LaunchPending || state.group == nil || !state.ResumeRequired || seen[state.group] {
			continue
		}
		seen[state.group] = true
		pending = append(pending, pendingLaunch{group: state.group, target: state.ResumeSessionID, aliases: slices.Clone(state.group.aliases)})
	}
	r.mu.Unlock()
	for _, launch := range pending {
		exists, err := claimed(launch.aliases)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		r.mu.Lock()
		proof := make(map[string]SessionRecoveryState)
		for _, alias := range launch.aliases {
			state := r.recovery[alias]
			if state.LaunchPending && state.group == launch.group && state.ResumeRequired && state.ResumeSessionID == launch.target {
				proof[alias] = state
			}
		}
		if len(proof) != 0 {
			err = r.restoreLaunchProofLocked(proof, launch.target)
		}
		r.mu.Unlock()
		if err != nil {
			return err
		}
	}
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
	a.launchFinished, a.launchFailed = true, failed
	a.cleanupErr = errors.Join(a.cleanupErr, cleanupErr)
	a.settleLocked()
	return errors.Join(a.cleanupErr, a.proofErr)
}

// settleLocked holds both registry locks in persistenceMu -> mu order. Stop's
// temporary epochs do not change process identity. A changed group, target, or
// membership does: in that case the new authority is left completely untouched.
func (a *ActiveResume) settleLocked() {
	r := a.owner
	if a.childPrepared && a.launchFinished && a.launchFailed && a.childReaped && !a.proofSettled {
		if err := r.restoreLaunchProofLocked(a.proof, a.target); err != nil {
			a.proofErr = err
			return
		}
		a.proofSettled = true
		a.cleanupErr, a.proofErr = nil, nil
	}
	if !a.handlerDone || (a.childPrepared && a.launchFailed && !a.proofSettled) {
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
	if a.cleanupErr == nil && !a.proofSettled {
		a.cleanupErr = cleanupErr
	}
	a.handlerDone = true
	a.cancel()
	close(a.done)
	a.settleLocked()
}

// ResumeCleanupError reports retained child failures to fresh connections. A
// failed durable re-confirmation can be retried here, but never before reaping.
func (r *ResumeLocks) ResumeCleanupError(aliases []string) error {
	r.persistenceMu.Lock()
	defer r.persistenceMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		for active := range r.active[alias] {
			if active.handlerDone {
				active.settleLocked()
				if err := errors.Join(active.cleanupErr, active.proofErr); err != nil {
					return err
				}
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, alias := range aliases {
		state := r.recovery[alias]
		for active := range r.active[alias] {
			if err := errors.Join(active.cleanupErr, active.proofErr); err != nil {
				return nil, err
			}
		}
		if state.Epoch != epochs[alias] || state.Stopping != 0 {
			return nil, ErrResumeInvalidated
		}
	}
	opCtx, cancel := context.WithCancel(ctx)
	a := &ActiveResume{owner: r, target: target, aliases: aliases, ctx: opCtx, cancel: cancel, done: make(chan struct{}), cleanupDone: make(chan struct{})}
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
			_ = active.owner.ResumeCleanupError(active.aliases)
			active.owner.mu.Lock()
			cleanupErr = errors.Join(cleanupErr, active.cleanupErr, active.proofErr)
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

func (r *ResumeLocks) BeginActiveResumeStop(alias string) *ResumeStop {
	r.mu.Lock()
	if len(r.active[alias]) == 0 {
		r.mu.Unlock()
		return nil
	}
	// Follow overlapping reservations as one ownership set. A waiter reached
	// through a stable/current alias must not retain a live context or escape
	// the fence merely because Stop named the other alias.
	seenAliases := make(map[string]bool)
	seenActive := make(map[*ActiveResume]bool)
	queue := []string{alias}
	stop := &ResumeStop{}
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
				stop.active = append(stop.active, active)
				queue = append(queue, active.aliases...)
			}
		}
	}
	stop.release = r.beginForceStopLocked(slices.Sorted(maps.Keys(seenAliases)))
	r.mu.Unlock()
	for _, active := range stop.active {
		active.cancel()
	}
	return stop
}

// SessionRecoveryState is the action admission state shared by every transport.
// Epoch changes invalidate actions that were waiting for session ownership.
type SessionRecoveryState struct {
	Epoch                uint64
	LastRecoverySequence uint64
	Stopping             int
	ResumeRequired       bool
	ExitConfirmed        bool
	// LaunchPending is set while a resume has durably invalidated this alias's
	// exit proof and is starting a replacement. It is cleared once that launch
	// settles, so a restart can recover a launch that produced no child.
	LaunchPending   bool
	ResumeSessionID string
	group           *sessionRecoveryGroup
	durableGroup    string
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
		alias.LaunchPending = false
		alias.durableGroup = ""
		alias.ResumeSessionID = ""
		r.recovery[id] = alias
	}
	return nil
}
