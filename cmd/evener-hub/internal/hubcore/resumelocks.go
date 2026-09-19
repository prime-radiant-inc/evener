package hubcore

import (
	"context"
	"crypto/rand"
	"errors"
	"maps"
	"os"
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
	fences        []*ForceStopFence // held admission fences, newest last; guarded by mu
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
	// childCleanup is the kill handle a launch left behind after it failed to
	// kill its own child; guarded by owner.mu. Retained until the child is
	// confirmed reaped so the stop draining this Resume can retry it.
	childCleanup func() error
	// heldAliases records which of the registered aliases this Resume's
	// handler currently holds the per-alias ownership reservation for, from
	// AcquireOwnership until ReleaseOwnership, so a concurrent force stop can
	// attribute a held reservation to a launch the drain will cancel rather
	// than to an unrelated action. Guarded by owner.mu.
	heldAliases map[string]bool
}

func (a *ActiveResume) Context() context.Context { return a.ctx }

// CleanupDone closes once the handler has completed and any failed launch's
// child has been confirmed reaped, or after a successful handoff. It is not the
// handler completion edge.
func (a *ActiveResume) CleanupDone() <-chan struct{} { return a.cleanupDone }

// AcquireOwnership takes the per-alias ownership reservations of the
// registered aliases, in the sorted registration order force stop's own
// reservations use, and records the hold so a concurrent force stop can
// attribute a held reservation to this launch rather than to an unrelated
// action. On failure it releases the prefix it took.
func (a *ActiveResume) AcquireOwnership(ctx context.Context) error {
	acquired := 0
	for _, alias := range a.aliases {
		if err := a.owner.For(alias).LockContext(ctx); err != nil {
			a.unmarkHeld(a.aliases[:acquired])
			for _, held := range slices.Backward(a.aliases[:acquired]) {
				a.owner.For(held).Unlock()
			}
			return err
		}
		a.markHeld(alias)
		acquired++
	}
	return nil
}

func (a *ActiveResume) markHeld(alias string) {
	r := a.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.heldAliases == nil {
		a.heldAliases = make(map[string]bool)
	}
	a.heldAliases[alias] = true
}

func (a *ActiveResume) unmarkHeld(aliases []string) {
	if len(aliases) == 0 {
		return
	}
	r := a.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		delete(a.heldAliases, alias)
	}
}

// ReleaseOwnership gives back the per-alias ownership reservations
// AcquireOwnership took. It is idempotent and safe to call whether
// acquisition succeeded, failed partway, or never ran.
func (a *ActiveResume) ReleaseOwnership() {
	r := a.owner
	r.mu.Lock()
	var held []string
	for _, alias := range slices.Backward(a.aliases) {
		if a.heldAliases[alias] {
			held = append(held, alias)
			delete(a.heldAliases, alias)
		}
	}
	r.mu.Unlock()
	for _, alias := range held {
		r.For(alias).Unlock()
	}
}

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

// RetainChildCleanup keeps a launch's child kill handle for the active-resume
// lifetime after the launcher failed to kill the child itself. The launcher is
// about to return; without the retained handle its failure would strand a live
// child no later action can address, while the unconfirmed cleanup keeps this
// Resume retained and its aliases blocked. The stop that drains the Resume
// retries the handle until the child is confirmed reaped.
func (a *ActiveResume) RetainChildCleanup(kill func() error) {
	r := a.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	a.childCleanup = kill
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
	// A settled Resume holds nothing, whatever its handler left behind.
	clear(a.heldAliases)
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
	return r.resumeCleanupErrorLocked(aliases, false)
}

// resumeCleanupErrorLocked reports the first retained child-cleanup failure
// among the Resumes active on aliases. includeInFlight also inspects a Resume
// whose handler has not yet completed; without it only handlerDone Resumes
// report, so a fresh connection is never refused by a Resume that is still
// completing normally. Callers hold r.mu.
func (r *ResumeLocks) resumeCleanupErrorLocked(aliases []string, includeInFlight bool) error {
	for _, alias := range aliases {
		for active := range r.active[alias] {
			if !includeInFlight && !active.handlerDone {
				continue
			}
			if err := active.cleanupErr; err != nil {
				return err
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
	return r.resumeCleanupErrorLocked(aliases, true)
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
	acquired := 0
	defer func() {
		for _, alias := range slices.Backward(aliases[:acquired]) {
			r.For(alias).Unlock()
		}
	}()
	for _, alias := range aliases {
		if err := r.For(alias).LockContext(ctx); err != nil {
			return nil, err
		}
		acquired++
	}
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
			// A launch whose child kill failed left the kill handle on the
			// active lifetime: retry it here, where the stop that canceled the
			// launch drains it, and wait for the confirmed reap. Once cleanup
			// is confirmed the retained cleanup error no longer describes the
			// world — the child is dead and the aliases settled — so only an
			// unconfirmed failure reaches the caller.
			active.owner.mu.Lock()
			cleanup := active.childCleanup
			confirmed := active.cleanupDone
			active.owner.mu.Unlock()
			if cleanup != nil {
				retryErr := cleanup()
				if retryErr == nil || errors.Is(retryErr, os.ErrProcessDone) {
					select {
					case <-confirmed:
						continue
					case <-ctx.Done():
						return errors.Join(cleanupErr, ctx.Err())
					}
				}
				cleanupErr = errors.Join(cleanupErr, retryErr)
			}
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

// HeldByActiveResumes reports whether every alias's per-alias reservation is
// currently held by an active Resume. A force stop that could not take a
// drain group's reservations uses it to tell a launch that provably holds its
// own aliases — and so blocks deletion publication across the drain — from an
// unrelated action whose reservation may release inside the check-to-cancel
// window.
func (r *ResumeLocks) HeldByActiveResumes(aliases []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		held := false
		for active := range r.active[alias] {
			if active.heldAliases[alias] {
				held = true
				break
			}
		}
		if !held {
			return false
		}
	}
	return true
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
	fence := r.beginForceStopLocked(aliases)
	stop.release = fence.Finish
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
// The returned fence is the caller's own rollback token — Finish releases it
// and Reject rolls back its admissions — so concurrent force stops over the
// same aliases never reject or roll back each other's fences.
func (r *ResumeLocks) BeginForceStop(aliases []string) *ForceStopFence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.beginForceStopLocked(aliases)
}

func (r *ResumeLocks) beginForceStopLocked(aliases []string) *ForceStopFence {
	if r.recovery == nil {
		r.recovery = make(map[string]SessionRecoveryState)
	}
	r.sequence++
	fence := &ForceStopFence{owner: r, aliases: slices.Clone(aliases), previous: make(map[string]uint64, len(aliases)), previousEpoch: make(map[string]uint64, len(aliases)), epochWritten: make(map[string]uint64, len(aliases)), sequence: r.sequence}
	for _, id := range aliases {
		state := r.recovery[id]
		fence.previous[id] = state.LastRecoverySequence
		fence.previousEpoch[id] = state.Epoch
		state.Epoch++
		fence.epochWritten[id] = state.Epoch
		state.LastRecoverySequence = r.sequence
		state.Stopping++
		r.recovery[id] = state
	}
	r.fences = append(r.fences, fence)
	return fence
}

// ForceStopFence is the rollback token of one held admission fence, created by
// beginForceStopLocked and dropped by Finish when the fence ends. It carries
// the connection-level sequence metadata and admission epochs the fence
// advanced so a refusal that canceled nothing can give them back. Only the
// caller that began the force stop finishes or rejects it.
type ForceStopFence struct {
	owner    *ResumeLocks
	aliases  []string
	previous map[string]uint64
	// previousEpoch is each fenced alias's epoch restore target: the epoch
	// the alias held before this fence advanced it. An older fence's rejection
	// can rewrite it to skip an advance that older refusal already gave back.
	previousEpoch map[string]uint64
	// epochWritten is the epoch value this fence wrote on each fenced alias,
	// fixed at begin, so the rollback below still recognizes its own write
	// after an older fence's rejection rewrote previousEpoch.
	epochWritten map[string]uint64
	sequence     uint64
	rejected     bool
	finished     bool
}

// Finish releases the fence: it gives back the admission Stopping each fenced
// alias took and, unless the fence was rejected, writes the fenced aliases'
// connection-level recovery sequence again, so the world a completed stop
// leaves behind stales connections admitted before it. A rejected fence's
// release keeps the sequence rolled back: the refusal canceled nothing, so
// connections from before the fence must not be staled by its release either.
func (f *ForceStopFence) Finish(stopped bool) {
	r := f.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.finished {
		return
	}
	f.finished = true
	r.fences = slices.DeleteFunc(r.fences, func(held *ForceStopFence) bool { return held == f })
	r.sequence++
	for _, id := range f.aliases {
		state := r.recovery[id]
		state.Stopping--
		if !f.rejected {
			state.LastRecoverySequence = r.sequence
		}
		state.ResumeRequired = state.ResumeRequired || stopped
		r.recovery[id] = state
	}
}

// Reject makes a refused force-stop fence admission-neutral: it gives back the
// admission epoch the fence advanced — but only while that epoch is still the
// value this fence wrote — and rolls back the connection-level sequence
// metadata the fence wrote, so a connection established before the fence is
// not refused as stale by the refusal — the force stop was refused and
// canceled nothing. The in-flight Resume a refusal deliberately preserved
// was admitted under the pre-fence epoch and completes through
// ExplicitResumeCompleted, which ignores a stale epoch — the durable resume
// requirement would stay set even though the Resume succeeded. The epoch
// give-back is conditional because an advance another actor published since
// this fence began must survive the refusal: a newer held fence's, or a
// confirmed-stopped no-op's InvalidateResumeAdmission, which advances epochs
// without installing a fence. Decrementing unconditionally would erase such a
// publication and admit a registration on the pre-decision snapshot after the
// no-op already reported the session stopped. Call it only on refusals that
// have canceled nothing, while the fence is still held and before Finish:
// with Stopping above zero no admission can bind the advanced epoch before
// the Finish(false) release restores eligibility, and a rejected fence's
// release keeps the sequence rolled back. A fence rejects exactly once, by
// its own caller's handle and never another request's alias set, and never
// once Finish has ended it. An alias a newer actor has since advanced keeps
// the newer epoch and sequence value, so a refused fence cannot un-stale a
// connection the newer fence must keep refusing; but the values this fence
// wrote are retired wherever they are still saved: a newer held fence that
// recorded them as an alias's previous state skips to this fence's own
// previous values, so the newer fence's later rollback cannot restore a
// value an already-rolled-back fence wrote.
func (f *ForceStopFence) Reject() {
	r := f.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.rejected || f.finished {
		return
	}
	f.rejected = true
	for _, id := range f.aliases {
		state := r.recovery[id]
		// Give back the epoch only while the value this fence wrote is still
		// the alias's current one: an advance another actor published since
		// this fence began — a newer held fence's, or a confirmed-stopped
		// no-op's InvalidateResumeAdmission, which installs no fence — must
		// survive a refusal that canceled nothing, so the rollback cannot
		// decrement the alias back past it and un-publish a decision already
		// reported.
		if state.Epoch == f.epochWritten[id] {
			state.Epoch = f.previousEpoch[id]
		}
		// Roll back only what this fence wrote and nothing since has
		// overwritten: an alias a newer fence has advanced keeps that
		// fence's value until the newer fence releases it.
		if state.LastRecoverySequence == f.sequence {
			state.LastRecoverySequence = f.previous[id]
		}
		r.recovery[id] = state
	}
	// Only fences newer than this one can have saved a value this fence
	// wrote as an alias's previous state: every older fence recorded smaller
	// sequence values and epochs. Retire the written values wherever a newer
	// held fence still saves them, so the newer fence's own rollback skips
	// them: the sequence this fence wrote, and the epoch an older refusal
	// already gave back.
	for _, held := range r.fences {
		if held == f {
			continue
		}
		for _, id := range f.aliases {
			if held.previous[id] == f.sequence {
				held.previous[id] = f.previous[id]
			}
			if held.previousEpoch[id] == f.epochWritten[id] {
				held.previousEpoch[id] = f.previousEpoch[id]
			}
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
