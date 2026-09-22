package agent

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/identifier"
)

// installScratchRetention registers this session's retention consumer and a
// stable binding for env, minting the manifest and pinning any allocation the
// environment already owns. It is idempotent and returns a persistence error
// rather than a warning, so a live session actually publishes its retention
// before it can be retired. A session without durable state or a non-local
// environment is a no-op.
func (s *Session) installScratchRetention(env *execenv.LocalExecutionEnvironment) error {
	return s.installScratchRetentionFor(env, s.id)
}

// installScratchRetentionFor registers sessionID's consumer and a binding for
// env. An environment that already carries an installed binding keeps it — a
// shared parent environment's binding is owned by whoever published it and must
// not be renamed to this consumer or republished under another root. Otherwise
// env is a distinct constructed environment and gets its own new opaque id
// (plan 646); identity is never inferred from the owning session's latest
// environment, so two environments owned by one root stay two bindings.
func (s *Session) installScratchRetentionFor(env *execenv.LocalExecutionEnvironment, sessionID string) error {
	if env == nil {
		return nil
	}
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return err
	}
	// An environment that already carries an installed binding keeps it: a
	// shared parent environment's binding is owned by whoever published it, and
	// must not be renamed to this consumer or republished under another root. If
	// it belongs to this owner's manifest, register only the consumer's role.
	if existing, err := env.ScratchRetentionBinding(); err == nil && existing.BindingID != "" {
		stored, ok := findScratchBinding(manifest, existing.BindingID)
		if !ok {
			return nil
		}
		consumer := scratchConsumerPreservingRoles(manifest, sessionID, stored.BindingID)
		return sandbox.UpsertScratchBinding(owner, stored, consumer)
	}
	bindingID, err := identifier.NewSessionID()
	if err != nil {
		return err
	}
	binding := sandbox.ScratchBinding{
		BindingID:      bindingID,
		OwnerSessionID: sessionID,
		WorkingDir:     env.WorkingDirectory(),
	}
	if err := env.SetScratchRetentionBinding(owner, binding); err != nil {
		return err
	}
	if err := env.PinOwnedScratch(); err != nil {
		return err
	}
	published, err := env.ScratchRetentionBinding()
	if err != nil {
		// SetScratchRetentionBinding just recorded the binding, so an unreadable
		// read-back is an unreachable defensive branch, not an install failure.
		return nil //nolint:nilerr // binding already installed; nothing to register
	}
	consumer := scratchConsumerPreservingRoles(manifest, sessionID, published.BindingID)
	return sandbox.UpsertScratchBinding(owner, published, consumer)
}

func findScratchBinding(manifest sandbox.ScratchManifest, bindingID string) (sandbox.ScratchBinding, bool) {
	for _, binding := range manifest.Bindings {
		if binding.BindingID == bindingID {
			return binding, true
		}
	}
	return sandbox.ScratchBinding{}, false
}

// scratchConsumerPreservingRoles builds the consumer record for a transition
// that changes only sessionID's current binding. The session's already-recorded
// role fields (parent-shared, worktree-restore, abandoned) are carried forward
// rather than wiped, so an interruption before the full role registration that
// follows cannot lose them (plan 650).
func scratchConsumerPreservingRoles(manifest sandbox.ScratchManifest, sessionID, currentBindingID string) sandbox.ScratchConsumerBinding {
	consumer := sandbox.ScratchConsumerBinding{SessionID: sessionID, CurrentBindingID: currentBindingID}
	for _, existing := range manifest.Consumers {
		if existing.SessionID != sessionID {
			continue
		}
		consumer.ParentSharedBindingID = existing.ParentSharedBindingID
		consumer.WorktreeRestoreBindingID = existing.WorktreeRestoreBindingID
		consumer.AbandonedBindingIDs = existing.AbandonedBindingIDs
		break
	}
	return consumer
}

// stageScratchSwapBinding persists the allocation-ownership transition a moving
// environment swap is about to perform, before AdoptSessionScratch takes the
// handles (and, when the target already owns a kind, releases the incoming
// lease). Each distinct owned environment keeps its own opaque binding id: the
// target takes exactly the owning slots it is about to receive and the source
// drops exactly those, while the source's id and every other slot (an
// allocation a shared child minted concurrently) are preserved. A stale
// revision is rebased onto the fresh manifest and retried, never overwritten.
func (s *Session) stageScratchSwapBinding(target, source *execenv.LocalExecutionEnvironment, sessionID string) error {
	if target == nil || source == nil {
		return nil
	}
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	sourceBinding, err := source.ScratchRetentionBinding()
	if err != nil || sourceBinding.BindingID == "" {
		// A source with no durable identity has nothing to hand over.
		return nil //nolint:nilerr // no durable source identity is a no-op, not a failure
	}
	targetID, err := s.ensureScratchBindingID(target, owner, sessionID)
	if err != nil {
		return err
	}
	// The kinds the target already owns physically are kept by
	// AdoptSessionScratch; an incoming allocation of such a kind is retained,
	// not adopted, so it must not be moved onto the target's binding.
	targetOwned, err := target.ScratchRetentionBinding()
	if err != nil {
		return err
	}
	keptKinds := make(map[string]struct{}, len(targetOwned.Slots))
	for kind, slot := range targetOwned.Slots {
		if slot.OwnsLease {
			keptKinds[kind] = struct{}{}
		}
	}
	// Snapshot the source's owning slots: the update loop below deletes from
	// sourceRecord.Slots, and when the source binding is missing from the loaded
	// manifest sourceRecord aliases sourceBinding. Ranging over a clone keeps the
	// kinds to move intact across a stale-revision retry instead of letting the
	// delete shrink the set the eventual write copies.
	moved := maps.Clone(sourceBinding.Slots)
	for range 5 {
		manifest, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			return err
		}
		consumer := scratchConsumerPreservingRoles(manifest, sessionID, targetID)
		targetRecord, ok := findScratchBinding(manifest, targetID)
		if !ok {
			targetRecord = sandbox.ScratchBinding{
				BindingID:      targetID,
				OwnerSessionID: sessionID,
				WorkingDir:     target.WorkingDirectory(),
			}
		}
		if targetRecord.Slots == nil {
			targetRecord.Slots = map[string]sandbox.ScratchSlot{}
		}
		sourceRecord, ok := findScratchBinding(manifest, sourceBinding.BindingID)
		if !ok {
			sourceRecord = sourceBinding
			// Clone rather than alias sourceBinding.Slots: the loop deletes from
			// sourceRecord.Slots, and aliasing would mutate the caller's binding
			// (and, before the moved clone above, the set being moved).
			sourceRecord.Slots = maps.Clone(sourceBinding.Slots)
		}
		if sourceRecord.Slots == nil {
			sourceRecord.Slots = map[string]sandbox.ScratchSlot{}
		}
		for kind, slot := range moved {
			if current, ok := sourceRecord.Slots[kind]; ok && filepath.Clean(current.Dir) == filepath.Clean(slot.Dir) {
				delete(sourceRecord.Slots, kind)
			}
			if _, kept := keptKinds[kind]; kept {
				continue
			}
			targetRecord.Slots[kind] = sandbox.ScratchSlot{Dir: slot.Dir, OwnsLease: true}
		}
		if hook := s.cfg.testOnly.scratchSwapBeforeUpdate; hook != nil {
			hook()
		}
		err = sandbox.UpdateScratchBindings(owner, manifest.Revision,
			[]sandbox.ScratchBinding{targetRecord, sourceRecord},
			[]sandbox.ScratchConsumerBinding{consumer})
		// A stale revision rebases onto the fresh manifest and retries; a
		// fail-fast lock refusal is the same transient class — a concurrent
		// in-process writer (a delegate restore's refresh install, another
		// mint) holding the manifest lock for microseconds — so the swap
		// retries it too rather than failing a live worktree move on a lock
		// race (round 8).
		if errors.Is(err, sandbox.ErrScratchRetentionStaleRevision) ||
			errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
			continue
		}
		return err
	}
	return fmt.Errorf("scratch retention: swap binding for %q stayed stale", targetID)
}

// ensureScratchBindingID returns env's installed binding id, minting and
// installing a fresh opaque one for a distinct owned environment that has none.
// The id lives on the environment, so a backswap onto the same object reuses it.
func (s *Session) ensureScratchBindingID(env *execenv.LocalExecutionEnvironment, owner sandbox.ScratchOwner, sessionID string) (string, error) {
	if installed, err := env.ScratchRetentionBinding(); err == nil && installed.BindingID != "" {
		return installed.BindingID, nil
	}
	bindingID, err := identifier.NewSessionID()
	if err != nil {
		return "", err
	}
	binding := sandbox.ScratchBinding{
		BindingID:      bindingID,
		OwnerSessionID: sessionID,
		WorkingDir:     env.WorkingDirectory(),
	}
	if err := env.SetScratchRetentionBinding(owner, binding); err != nil {
		return "", err
	}
	return bindingID, nil
}

// inheritScratchRetentionBinding gives target the installed binding identity of
// source, so a clone that adopted source's scratch represents the same logical
// environment (plan 646's "retain that ID on ... reuse"). A source with no
// durable identity is a no-op. Slots are cleared: they are derived from the
// environment's owned handles, never carried as stale ownership.
func (s *Session) inheritScratchRetentionBinding(target, source *execenv.LocalExecutionEnvironment) error {
	if target == nil || source == nil {
		return nil
	}
	binding, err := source.ScratchRetentionBinding()
	if err != nil || binding.BindingID == "" {
		return nil //nolint:nilerr // a source with no durable identity is a no-op
	}
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	binding.Slots = nil
	return target.SetScratchRetentionBinding(owner, binding)
}

// assignRetainedScratchBinding installs the persisted binding record for
// bindingID onto env, so an environment reconstructed by a cold resume keeps
// its own opaque identity rather than being left unbound (plan 654). An unknown
// or empty id is a no-op; slots are derived from owned handles.
func (s *Session) assignRetainedScratchBinding(env *execenv.LocalExecutionEnvironment, bindingID string) error {
	if env == nil || bindingID == "" {
		return nil
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	binding, ok := pool.bindings[bindingID]
	pool.mu.Unlock()
	if !ok {
		return nil
	}
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	binding.Slots = nil
	return env.SetScratchRetentionBinding(owner, binding)
}

// installChildScratchRetention registers a delegate child's own durable binding
// and consumer under the parent root's retention manifest, in the same
// transaction as its first publication. ownerSessionID is the child session;
// the manifest owner is the parent root. A child without an owned local
// environment (a shared parent environment) is a no-op.
func (s *Session) installChildScratchRetention(env *execenv.LocalExecutionEnvironment, childSessionID string) error {
	if childSessionID == "" {
		return nil
	}
	return s.installScratchRetentionFor(env, childSessionID)
}

// registerScratchConsumerRoles publishes this session's current, parent-shared,
// worktree-restore and abandoned environment roles in the root's retention
// manifest, before the swapped-in environment is used. It installs the binding
// if it is not yet present. It takes no Session.mu across its I/O.
func (s *Session) registerScratchConsumerRoles(env *execenv.LocalExecutionEnvironment) error {
	if env == nil {
		return nil
	}
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	if fault := s.sessionInitFault("swap_scratch_consumer_roles"); fault != nil {
		return fault
	}
	if err := s.installScratchRetention(env); err != nil {
		return err
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		// installScratchRetention already installed the binding above; the
		// unreachable read-back failure leaves no consumer row to add.
		return nil //nolint:nilerr // binding already installed; nothing to register
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return err
	}
	binding, ok := findScratchBinding(manifest, installed.BindingID)
	if !ok {
		return nil
	}
	consumer := sandbox.ScratchConsumerBinding{SessionID: s.id, CurrentBindingID: binding.BindingID}
	s.mu.Lock()
	shared := s.parentSharedEnv
	restore := s.worktreeRestoreEnv
	abandoned := append([]*execenv.LocalExecutionEnvironment(nil), s.abandonedEnvs...)
	s.mu.Unlock()
	// Each role resolves its OWN environment's binding, which may differ from
	// the published environment's binding (a shared or parked environment), so
	// a Task 6 consumer can join every role to its exact binding.
	if sharedEnv, ok := shared.(*execenv.LocalExecutionEnvironment); ok {
		if id, ok := s.roleScratchBindingID(manifest, sharedEnv); ok {
			consumer.ParentSharedBindingID = id
		}
	}
	if id, ok := s.roleScratchBindingID(manifest, restore); ok {
		consumer.WorktreeRestoreBindingID = id
	}
	for _, candidate := range abandoned {
		if id, ok := s.roleScratchBindingID(manifest, candidate); ok {
			consumer.AbandonedBindingIDs = append(consumer.AbandonedBindingIDs, id)
		}
	}
	return sandbox.UpsertScratchBinding(owner, binding, consumer)
}

// roleScratchBindingID resolves one role environment's own binding id from the
// manifest: its installed binding if that binding is this owner's, else a
// stored binding that owns one of the role environment's allocations.
func (s *Session) roleScratchBindingID(manifest sandbox.ScratchManifest, roleEnv *execenv.LocalExecutionEnvironment) (string, bool) {
	if roleEnv == nil {
		return "", false
	}
	if installed, err := roleEnv.ScratchRetentionBinding(); err == nil && installed.BindingID != "" {
		if _, ok := findScratchBinding(manifest, installed.BindingID); ok {
			return installed.BindingID, true
		}
	}
	refs, err := roleEnv.ScratchRetentionReferences()
	if err != nil || len(refs) == 0 {
		return "", false
	}
	dirs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if dir, err := filepath.Abs(ref.Dir); err == nil {
			dirs[filepath.Clean(dir)] = struct{}{}
		}
	}
	for _, stored := range manifest.Bindings {
		for _, slot := range stored.Slots {
			if !slot.OwnsLease {
				continue
			}
			if dir, err := filepath.Abs(slot.Dir); err == nil {
				if _, ok := dirs[filepath.Clean(dir)]; ok {
					return stored.BindingID, true
				}
			}
		}
	}
	return "", false
}

// scratchRetentionPersistenceError returns the first sticky scratch-retention
// pin/publish failure recorded on any environment this session owns or shares:
// the current environment, the environment an enter parked (worktreeRestoreEnv),
// every environment a swap abandoned, and the live parent's own shared
// environment. A recorded failure means an allocation was not durably pinned,
// so preparation and release must fail rather than trust a partially pinned
// environment. It takes no Session lock across the read.
func (s *Session) scratchRetentionPersistenceError() error {
	check := func(env *execenv.LocalExecutionEnvironment) error {
		if env == nil {
			return nil
		}
		return env.ScratchRetentionError()
	}
	s.mu.Lock()
	sticky := s.scratchRetentionErr
	parked := s.worktreeRestoreEnv
	abandoned := append([]*execenv.LocalExecutionEnvironment(nil), s.abandonedEnvs...)
	shared, _ := s.parentSharedEnv.(*execenv.LocalExecutionEnvironment)
	s.mu.Unlock()
	// A publication failure recorded on the session itself — a swap whose
	// consumer roles could not be persisted after the environment install — is
	// as fatal as a pin failure: the manifest diverged from the live
	// environment, so preparation must fail closed.
	if sticky != nil {
		return sticky
	}
	if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
		if err := check(local); err != nil {
			return err
		}
	}
	envs := make([]*execenv.LocalExecutionEnvironment, 0, len(abandoned)+2)
	envs = append(envs, parked, shared)
	envs = append(envs, abandoned...)
	for _, env := range envs {
		if err := check(env); err != nil {
			return err
		}
	}
	return nil
}

// validateRetainedScratchPresent verifies every referenced allocation still
// exists at its original path and every binding slot resolves to a pinned
// reference. It neither acquires a lease nor mutates durable state, so it is
// safe during preparation. A non-nil retained pool is not proof that durable
// storage is still valid: the committed manifest and each pin are revalidated
// every time, and a recorded pin/publish failure fails the check.
func (s *Session) validateRetainedScratchPresent() error {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	if err := s.scratchRetentionPersistenceError(); err != nil {
		return fmt.Errorf("retained scratch persistence: %w", err)
	}
	// Verify each reference's immutable ownership/kind identity pin under the
	// manifest lock, not just that the directory and manifest path exist: a
	// missing or foreign pin means the next restore would reject the allocation,
	// so retirement must fail closed rather than proceed to release it.
	manifest, err := sandbox.ValidateRetainedScratchPins(owner)
	if err != nil {
		return err
	}
	if manifest.Released {
		return nil
	}
	refs := make(map[string]struct{}, len(manifest.References))
	for _, ref := range manifest.References {
		dir, err := filepath.Abs(ref.Dir)
		if err != nil {
			return err
		}
		dir = filepath.Clean(dir)
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("retained scratch %q: %w", dir, err)
		}
		refs[dir] = struct{}{}
	}
	for _, binding := range manifest.Bindings {
		for kind, slot := range binding.Slots {
			dir, err := filepath.Abs(slot.Dir)
			if err != nil {
				return err
			}
			if _, ok := refs[filepath.Clean(dir)]; !ok {
				return fmt.Errorf("retained scratch binding %q slot %q references unpinned directory", binding.BindingID, kind)
			}
		}
	}
	return nil
}

// retainedScratchPool is the root-owned set of scratch handles reacquired by
// prepareRetainedScratch. Handles stay here, keyed by canonical path, until a
// binding adopts them; a historical or not-yet-reconstructed handle is simply
// never adopted and remains valid for the life of the process.
type retainedScratchPool struct {
	owner     sandbox.ScratchOwner
	handles   map[string]*sandbox.SessionScratch
	bindings  map[string]sandbox.ScratchBinding
	consumers map[string]sandbox.ScratchConsumerBinding
	// contended holds allocations whose lease was already held in this process,
	// so no handle could be reacquired; adopted holds allocations already
	// transferred to a consumer. Together they distinguish "leave with its
	// holder" from "already transferred, refuse a second owner".
	contended map[string]struct{}
	// adopted maps a transferred allocation's canonical dir to the consumer
	// session that took it, so a distinct sharing consumer can borrow the same
	// directory while a duplicate transfer by the same consumer is refused. A
	// dir is recorded here when an owning transfer is claimed — before the
	// environment restore — and cleared again if that restore fails, so a
	// concurrent adopter never doubles a lease the pool is handing over.
	adopted map[string]string
	// mu guards handles and adopted, which adoption and release mutate from the
	// independent goroutines that can restore distinct children of one root,
	// and the bindings/consumers/contended entries that a post-publish
	// refreshRetainedScratchConsumer folds in. Readers of those maps take mu
	// and copy the row out; a refresh only ever installs NEWER manifest rows,
	// so a reader holding a pre-refresh copy sees exactly the world a restore
	// before it would have, never a torn one.
	mu sync.Mutex
}

// findScratchConsumer returns sessionID's consumer row from the manifest.
func findScratchConsumer(manifest sandbox.ScratchManifest, sessionID string) (sandbox.ScratchConsumerBinding, bool) {
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == sessionID {
			return consumer, true
		}
	}
	return sandbox.ScratchConsumerBinding{}, false
}

// refreshRetainedScratchConsumer converges the retained pool onto sessionID's
// CURRENT durable manifest rows before a same-process cold restore adopts.
// The pool is an init-time snapshot — prepareRetainedScratch runs once, before
// root/child initialization launches work — and three shapes drift from it: a
// delegate created after init never entered pool.consumers; a delegate whose
// manifest rows moved after the pool was built (a worktree move, a shared
// binding swap) holds rows the pool still records under their old identity;
// and a delegate an idle release retired holds slots behind adoption claims
// the pool has no reason to drop. For the first two, the consumer's rows are
// installed the way init would have loaded them — free leases reacquired, held
// ones marked contended — and for the third, each claimed slot whose lease went
// free is re-proven: a reacquire can only succeed after the previous adopter
// released the lease, so the handle reinstalls and the claim clears. Either
// way the restore gets the same adoption a restart would, resuming in the
// original scratch directory instead of a fresh mint. Nothing else moves: a
// slot the pool deliberately holds no handle for keeps its engineered absence,
// and a claim whose lease is still held keeps its record, so the existing
// refusal semantics are untouched.
//
// The rows are derived from one manifest snapshot and installed only after
// the reacquires, so the revision is rechecked before anything lands: a
// manifest that moved in between drops this pass's reacquired leases and is
// re-derived from the fresh one, bounded like stageScratchSwapBinding's rebase
// loop, and a pass that fails after reacquiring releases the leases it took so
// they cannot strand. A poolless seed publishes through a compare-and-swap: a
// concurrent seed that won the race is folded into, never displaced and
// released. Lock contention is a retry, never a failure: the reacquire opens
// and the install hold serialize on the manifest's fail-fast lock, and a pass
// that loses the race with another refresh or writer hands its leases back and
// re-derives — a contention that never clears still fails loudly after the
// bound, never silently. A slot the pool recorded contended — its lease was
// held in-process at a previous refresh, typically the idle-release teardown
// racing the restore it enables — is re-probed on every refresh: a reacquire
// that now succeeds re-pools the handle and clears the record; one that still
// finds the lease held proves the contention live and leaves it marked.
func (s *Session) refreshRetainedScratchConsumer(sessionID string) error {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	for range 5 {
		manifest, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			return fmt.Errorf("retained scratch refresh: %w", err)
		}
		if manifest.Released {
			return nil
		}
		consumer, ok := findScratchConsumer(manifest, sessionID)
		if !ok || consumer.CurrentBindingID == "" {
			return nil
		}
		binding, ok := findScratchBinding(manifest, consumer.CurrentBindingID)
		if !ok {
			return nil
		}
		owningSlots := make([]sandbox.ScratchReference, 0, len(binding.Slots))
		for kind, slot := range binding.Slots {
			if slot.OwnsLease {
				owningSlots = append(owningSlots, sandbox.ScratchReference{Dir: slot.Dir, Kind: kind})
			}
		}
		pool := s.retainedScratch.Load()
		installRows := pool == nil
		var staleClaims []sandbox.ScratchReference
		if pool != nil {
			pool.mu.Lock()
			// A consumer row that merely exists is not proof the rows it names
			// are live: the manifest can have moved the consumer or its
			// binding after the pool was built, so the refresh installs
			// whenever the pooled rows differ from the manifest's, not only
			// when the consumer is missing entirely.
			pooledConsumer, hasRow := pool.consumers[sessionID]
			if !hasRow || !scratchConsumerRowsCurrent(pooledConsumer, consumer) {
				installRows = true
			} else if pooledBinding, hasBinding := pool.bindings[consumer.CurrentBindingID]; !hasBinding || !scratchBindingRowsCurrent(pooledBinding, binding) {
				installRows = true
			}
			for _, ref := range owningSlots {
				key := canonicalScratchDir(ref.Dir)
				if _, claimed := pool.adopted[key]; claimed {
					staleClaims = append(staleClaims, ref)
				} else if _, held := pool.contended[key]; held {
					// A contended slot's lease was held by another in-process
					// owner at refresh time — often the idle-release teardown
					// racing this very restore, whose leases land back the
					// moment it settles. It is a claim to re-probe exactly
					// like an adopted one: a reacquire that now succeeds
					// re-pools the handle and clears the contention record,
					// and one that still finds the lease held proves the
					// contention live and leaves the record marked.
					staleClaims = append(staleClaims, ref)
				}
			}
			pool.mu.Unlock()
		}
		if !installRows && len(staleClaims) == 0 {
			return nil
		}
		try := owningSlots
		if !installRows {
			try = staleClaims
		}
		handles := make(map[string]*sandbox.SessionScratch)
		contended := make(map[string]struct{})
		openCalls := 0
		lockContention := false
		for _, ref := range try {
			openCalls++
			var handle *sandbox.SessionScratch
			var err error
			if override := s.cfg.testOnly.scratchRefreshOpenOverride; override != nil {
				err = override(ref, openCalls)
			}
			if err == nil {
				handle, err = sandbox.OpenRetainedSessionScratch(owner, ref)
			}
			if err != nil {
				if errors.Is(err, sandbox.ErrScratchRetentionLeaseHeld) {
					// A lease held elsewhere in this process stays with its
					// holder and is marked contended so adoption borrows or
					// skips instead of erroring. A row install mirrors init's
					// reacquire pass. A stale-claim probe that finds the lease
					// held proves the claim live — and for an adopted claim,
					// whose lease the idle-release teardown still holds across
					// the runtime-pointer window, that proof is the only
					// contention record the replacement guard will ever see:
					// unstamped, the guard passes the replacement through and
					// the adoption fails "already transferred" against the
					// session's own stale claim (round 7).
					contended[canonicalScratchDir(ref.Dir)] = struct{}{}
					continue
				}
				// The leases this pass already reacquired are going nowhere —
				// release them with the error, or they hold their slots for the
				// life of a process that never installed them.
				releaseRefreshHandles(handles)
				if errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
					// The reacquire opens serialize with terminal release on
					// the manifest lock, and a concurrent refresh's install
					// hold (or any manifest writer's update) may own it.
					// Contention is transient by construction — holds last
					// microseconds — so the pass hands its leases back and
					// the next pass re-derives instead of failing a restore
					// that raced another refresh by microseconds.
					lockContention = true
					break
				}
				return fmt.Errorf("retained scratch refresh %q: %w", ref.Dir, err)
			}
			handles[canonicalScratchDir(ref.Dir)] = handle
		}
		if lockContention {
			continue
		}
		if hook := s.cfg.testOnly.scratchRefreshBeforeInstall; hook != nil {
			hook(sessionID)
		}
		// Recheck the revision and install under the manifest's durable update
		// lock, one atomic step against every writer: the rows above came from
		// one snapshot, and a binding or consumer update that committed between
		// a bare recheck and the install would leave the pool adopting rows a
		// superseded revision produced. PinScratchBinding, UpdateScratchBindings
		// and the release path all serialize on this lock to write, so holding
		// it means no update can land in that window.
		installErr := sandbox.WithScratchRetentionLock(owner, func() error {
			fresh, err := sandbox.LoadScratchRetention(owner)
			if err != nil {
				return err
			}
			if fresh.Revision != manifest.Revision {
				return errScratchRefreshStaleRevision
			}
			if hook := s.cfg.testOnly.scratchRefreshAfterRecheck; hook != nil {
				hook(sessionID)
			}
			if pool != nil {
				if !s.installConsumerRefresh(pool, consumer, binding, handles, contended) {
					return errScratchRefreshPoolDetached
				}
				return nil
			}
			// No pool was ever published — the root initialized before the
			// manifest held a single row — so seed one from this consumer's
			// rows the way prepareRetainedScratch would have. The publish is a
			// compare-and-swap against nil: a concurrent restore may publish
			// its own seed between the load above and here, and displacing that
			// pool would release the handles it reacquired mid-restore and drop
			// its rows — fold into it instead.
			seeded := &retainedScratchPool{
				owner:     owner,
				handles:   handles,
				bindings:  map[string]sandbox.ScratchBinding{binding.BindingID: binding},
				consumers: map[string]sandbox.ScratchConsumerBinding{sessionID: consumer},
				contended: contended,
				adopted:   make(map[string]string),
			}
			for range 5 {
				if s.retainedScratch.CompareAndSwap(nil, seeded) {
					return nil
				}
				published := s.retainedScratch.Load()
				if published == nil {
					// The pointer went back to nil — a terminal release or
					// init cleanup swept the published pool — so retry the CAS
					// with this pass's seed.
					continue
				}
				if s.installConsumerRefresh(published, consumer, binding, handles, contended) {
					return nil
				}
				// The published pool was detached between the load and the
				// fold; loop to re-load and try the fold again.
			}
			// The published pool kept dying between the CAS and the fold. The
			// bound keeps the manifest lock's hold finite; the sentinel hands
			// the unconsumed handles back and the pass re-derives against the
			// current pointer.
			return errScratchRefreshPoolDetached
		})
		switch {
		case installErr == nil:
			return nil
		case errors.Is(installErr, errScratchRefreshStaleRevision),
			errors.Is(installErr, errScratchRefreshPoolDetached),
			errors.Is(installErr, sandbox.ErrScratchRetentionLockHeld):
			// This pass's reacquired leases are nobody's now — hand them back
			// before the next pass re-derives its own against the moved
			// manifest or the current pool.
			releaseRefreshHandles(handles)
			continue
		default:
			releaseRefreshHandles(handles)
			return fmt.Errorf("retained scratch refresh install: %w", installErr)
		}
	}
	return fmt.Errorf("retained scratch refresh: rows for %q stayed stale", sessionID)
}

var (
	// errScratchRefreshStaleRevision marks a refresh pass whose manifest
	// revision was superseded before its install hold, so the pass re-derives.
	errScratchRefreshStaleRevision = errors.New("agent: retained scratch refresh revision superseded")
	// errScratchRefreshPoolDetached marks a pass whose target pool was swapped
	// out from the session before the fold landed, so the pass retries.
	errScratchRefreshPoolDetached = errors.New("agent: retained scratch refresh pool detached")
)

// scratchConsumerRowsCurrent reports whether the pool's consumer row is the
// live manifest's: any drift — a moved current binding, a changed parked or
// shared role, a revised abandonment history — means the pool's rows must be
// reinstalled, not merely a missing consumer.
func scratchConsumerRowsCurrent(pooled, live sandbox.ScratchConsumerBinding) bool {
	return pooled.SessionID == live.SessionID &&
		pooled.CurrentBindingID == live.CurrentBindingID &&
		pooled.ParentSharedBindingID == live.ParentSharedBindingID &&
		pooled.WorktreeRestoreBindingID == live.WorktreeRestoreBindingID &&
		slices.Equal(pooled.AbandonedBindingIDs, live.AbandonedBindingIDs)
}

// scratchBindingRowsCurrent reports whether the pool's binding row is the live
// manifest's, slot by slot: a binding whose slots moved (a backswap, a shared
// handover) is stale even when its id still matches the consumer's current one.
func scratchBindingRowsCurrent(pooled, live sandbox.ScratchBinding) bool {
	if pooled.BindingID != live.BindingID ||
		pooled.OwnerSessionID != live.OwnerSessionID ||
		pooled.WorkingDir != live.WorkingDir ||
		len(pooled.Slots) != len(live.Slots) {
		return false
	}
	for kind, slot := range pooled.Slots {
		other, ok := live.Slots[kind]
		if !ok || slot != other {
			return false
		}
	}
	return true
}

// releaseRefreshHandles releases the leases one refresh pass reacquired: a
// pass that ends without installing — an open failure, a superseded revision —
// must not leave its handles holding their slots. Retain hands the lease back
// without touching the directory, exactly how a pool teardown releases its
// handles.
func releaseRefreshHandles(handles map[string]*sandbox.SessionScratch) {
	for _, handle := range handles {
		_ = handle.Retain()
	}
}

// installConsumerRefresh folds one consumer's refreshed manifest rows into
// pool and reports whether the fold landed in the published one: the consumer
// and its current binding become adoptable, reacquired handles join the pool
// and drop any adoption record their reacquire proves stale — the previous
// adopter gave the lease back — and contended marks record slots whose leases
// are held elsewhere in this process — never a slot the pool itself holds a
// handle for, which is transferable rather than contended — so an adoption
// that finds no handle borrows or skips instead of erroring.
//
// The fold is guarded by the session's published-pool identity, checked under
// the same mutex hold that guards the maps: a terminal release can swap the
// pointer out and drop the pool at any moment, and a fold that lands in a
// detached pool strands its reacquired leases — the release either already
// cleared the map (rows lost with the closing tree, moot) or never will again
// (the handles leak for the life of the process). Checking the pointer inside
// the hold serializes the fold with the release's clear, so the fold either
// happens while the pool is still the published one or not at all, and the
// caller — which keeps ownership of un-folded handles — retries against the
// now-current pointer.
func (s *Session) installConsumerRefresh(pool *retainedScratchPool, consumer sandbox.ScratchConsumerBinding, binding sandbox.ScratchBinding, handles map[string]*sandbox.SessionScratch, contended map[string]struct{}) bool {
	if pool == nil {
		return false
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if s.retainedScratch.Load() != pool {
		return false
	}
	pool.consumers[consumer.SessionID] = consumer
	pool.bindings[binding.BindingID] = binding
	for key, handle := range handles {
		pool.handles[key] = handle
		delete(pool.adopted, key)
		delete(pool.contended, key)
	}
	for key := range contended {
		// A slot the pool holds a handle for — a reacquire a prior refresh
		// installed that no adoption has taken — is pool-owned, the opposite
		// of contended: the allocation is transferable, and a contention
		// mark beside the pooled handle would wedge every later adoption
		// against the pool's own lease (round 5).
		if _, held := pool.handles[key]; held {
			continue
		}
		pool.contended[key] = struct{}{}
	}
	return true
}

// validateRetainedScratchGraph fails closed on an incomplete or contradictory
// reference→binding→consumer graph. A manifest that pins references without any
// binding is the signature of a crash between reference publication and the
// binding transaction that maps the environment: nothing could reconstruct the
// environment, so restore must refuse rather than let initialization mint a
// replacement and silently lose continuity with the original durable artifacts.
// It also rejects contradictory mappings — a binding without an id, a duplicate
// binding/reference, a slot that names an unpinned directory or a different
// kind, or a consumer role that names a binding absent from the manifest — and
// an owning binding that no consumer role names: installScratchRetentionFor
// publishes a binding and its pinned, lease-owning slots before it publishes the
// consumer that maps a session onto them, so a crash in that window leaves an
// allocation nothing would adopt, and restore would silently mint a replacement
// instead of resuming in the retained directory.
//
// It deliberately does not require every reference to be mapped by a binding:
// a backswap whose target already owns the incoming kind leaves the incoming
// allocation a "historical" pinned reference with no owning slot, and restore
// must preserve it (see TestRetirementSharedChildScratchBindingsRestore). It
// likewise preserves a binding that owns no slot — a backswap empties the source
// binding and moves its roles to the target — because a binding that owns
// nothing cannot lose an allocation.
func validateRetainedScratchGraph(manifest sandbox.ScratchManifest) error {
	refKinds := make(map[string]string, len(manifest.References))
	for _, ref := range manifest.References {
		dir, err := filepath.Abs(ref.Dir)
		if err != nil {
			return err
		}
		dir = filepath.Clean(dir)
		if _, dup := refKinds[dir]; dup {
			return fmt.Errorf("duplicate retention reference %q", dir)
		}
		refKinds[dir] = ref.Kind
	}
	if len(manifest.References) > 0 && len(manifest.Bindings) == 0 {
		return fmt.Errorf("retention manifest holds %d references but no binding", len(manifest.References))
	}
	bindingIDs := make(map[string]struct{}, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		if binding.BindingID == "" {
			return errors.New("retention manifest holds a binding without an id")
		}
		if _, dup := bindingIDs[binding.BindingID]; dup {
			return fmt.Errorf("duplicate retention binding %q", binding.BindingID)
		}
		bindingIDs[binding.BindingID] = struct{}{}
	}
	for _, binding := range manifest.Bindings {
		for kind, slot := range binding.Slots {
			dir, err := filepath.Abs(slot.Dir)
			if err != nil {
				return err
			}
			dir = filepath.Clean(dir)
			pinnedKind, ok := refKinds[dir]
			if !ok {
				return fmt.Errorf("retention binding %q slot %q references unpinned directory %q", binding.BindingID, kind, dir)
			}
			if pinnedKind != kind {
				return fmt.Errorf("retention binding %q slot %q kind does not match pinned kind %q", binding.BindingID, kind, pinnedKind)
			}
		}
	}
	referenced := make(map[string]struct{}, len(manifest.Bindings))
	for _, consumer := range manifest.Consumers {
		roles := []string{consumer.CurrentBindingID, consumer.ParentSharedBindingID, consumer.WorktreeRestoreBindingID}
		roles = append(roles, consumer.AbandonedBindingIDs...)
		for _, id := range roles {
			if id == "" {
				continue
			}
			if _, ok := bindingIDs[id]; !ok {
				return fmt.Errorf("retention consumer %q references unknown binding %q", consumer.SessionID, id)
			}
			referenced[id] = struct{}{}
		}
	}
	// Every binding that owns a lease-owning slot must be named by some consumer
	// role, or its allocation can never be adopted on restore. Bindings that own
	// no slot are historical leftovers and are kept.
	for _, binding := range manifest.Bindings {
		if _, ok := referenced[binding.BindingID]; ok {
			continue
		}
		for kind, slot := range binding.Slots {
			if !slot.OwnsLease {
				continue
			}
			return fmt.Errorf("retention binding %q owns slot %q but no consumer role references it", binding.BindingID, kind)
		}
	}
	return nil
}

// scratchRetentionOwner resolves this session's root retention authority. A
// delegate reports its root's owner; a root reports itself. ok is false for a
// session without durable state, which has nothing to prepare.
func (s *Session) scratchRetentionOwner() (sandbox.ScratchOwner, bool) {
	if s == nil || s.stateDir == "" || s.id == "" {
		return sandbox.ScratchOwner{}, false
	}
	root := s.delegateRootSessionID
	if root == "" {
		root = s.id
	}
	return sandbox.ScratchOwner{StateDir: s.stateDir, RootSessionID: root}, true
}

// prepareRetainedScratch loads and validates the root's retention manifest and
// reacquires every referenced lease before root/child initialization launches
// work. It reconstructs each persisted logical environment once in a registry
// keyed by binding ID, using the binding's original working directory. Missing
// bytes, contradictory mappings or identity/permission failures are errors: no
// replacement directory is ever minted. A root with no manifest is a no-op.
func (s *Session) prepareRetainedScratch() error {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return fmt.Errorf("retained scratch: %w", err)
	}
	if manifest.Released || len(manifest.References) == 0 {
		return nil
	}
	// Fail closed on an incomplete or contradictory reference→binding→consumer
	// graph before reacquiring a single lease. A crash between publishing a
	// pin/reference and publishing the binding or consumer that maps it would
	// otherwise restore with an empty binding map: nothing would adopt the
	// retained directory and initialization would mint a replacement, silently
	// losing continuity with the original durable artifacts.
	if err := validateRetainedScratchGraph(manifest); err != nil {
		return fmt.Errorf("retained scratch: %w", err)
	}
	pool := &retainedScratchPool{
		owner:     owner,
		handles:   make(map[string]*sandbox.SessionScratch, len(manifest.References)),
		bindings:  make(map[string]sandbox.ScratchBinding, len(manifest.Bindings)),
		consumers: make(map[string]sandbox.ScratchConsumerBinding, len(manifest.Consumers)),
		contended: make(map[string]struct{}),
		adopted:   make(map[string]string),
	}
	// A reference this session's own environment already holds live is not a
	// restore target: its lease is owned, so reacquiring would self-contend.
	// Skipping it keeps the live handle with the environment that holds it.
	liveDirs := make(map[string]struct{})
	if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
		if refs, err := local.ScratchRetentionReferences(); err == nil {
			for _, live := range refs {
				if dir, err := filepath.Abs(live.Dir); err == nil {
					liveDirs[filepath.Clean(dir)] = struct{}{}
				}
			}
		}
	}
	for _, ref := range manifest.References {
		if dir, err := filepath.Abs(ref.Dir); err == nil {
			if _, ok := liveDirs[filepath.Clean(dir)]; ok {
				continue
			}
		}
		handle, err := sandbox.OpenRetainedSessionScratch(owner, ref)
		if err != nil {
			if errors.Is(err, sandbox.ErrScratchRetentionLeaseHeld) {
				// Already owned in this process (a live or crash-abandoned
				// runtime holds the lease). Leave it with its owner instead of
				// contending; a real crash releases the lease before restore.
				if dir, absErr := filepath.Abs(ref.Dir); absErr == nil {
					pool.contended[filepath.Clean(dir)] = struct{}{}
				}
				continue
			}
			releaseRetainedScratchPool(pool)
			return fmt.Errorf("retained scratch %q: %w", ref.Dir, err)
		}
		pool.handles[filepath.Clean(ref.Dir)] = handle
	}
	for _, binding := range manifest.Bindings {
		if _, dup := pool.bindings[binding.BindingID]; dup {
			releaseRetainedScratchPool(pool)
			return fmt.Errorf("retained scratch: duplicate binding %q", binding.BindingID)
		}
		pool.bindings[binding.BindingID] = binding
	}
	for _, consumer := range manifest.Consumers {
		pool.consumers[consumer.SessionID] = consumer
	}
	prior := s.retainedScratch.Swap(pool)
	releaseRetainedScratchPool(prior)
	return nil
}

// adoptRetainedScratchFor transfers the owning slots of exactly bindingID from
// the pool onto env for adopterID, installs wrapper-only borrows without
// duplicating lease ownership, and verifies the binding identity. It is a no-op
// when no pool was prepared, so a session with no retention manifest keeps its
// existing path. A distinct consumer sharing an allocation that another session
// already adopted receives a lease-less borrow, while the same consumer asking
// twice is refused.
func (s *Session) adoptRetainedScratchFor(env *execenv.LocalExecutionEnvironment, bindingID, adopterID string) error {
	if env == nil {
		return nil
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	binding, ok := pool.bindings[bindingID]
	pool.mu.Unlock()
	if !ok {
		return fmt.Errorf("retained scratch: binding %q is not in the manifest", bindingID)
	}
	if err := env.SetScratchRetentionBinding(pool.owner, binding); err != nil {
		return err
	}
	// A kind this environment already provisions (an eagerly provisioned
	// sandbox scratch) is left exposed; only absent kinds are restored, so a
	// retained wrapper never replaces a live allocation.
	existingKinds := map[string]bool{}
	if refs, err := env.ScratchRetentionReferences(); err == nil {
		for _, ref := range refs {
			existingKinds[ref.Kind] = true
		}
	}
	for kind, slot := range binding.Slots {
		if existingKinds[kind] {
			continue
		}
		key := filepath.Clean(slot.Dir)
		if !slot.OwnsLease {
			// A wrapper-only slot points at a retained directory whose lease
			// another binding owns. It never takes a second lease, so the borrow
			// must happen whether the owning handle was reacquired (present), is
			// held elsewhere in this process (contended), or its owner binding
			// has not been adopted yet.
			if pool.scratchSlotTakenBy(key) == adopterID {
				return fmt.Errorf("retained scratch: binding %q slot %q was already transferred", bindingID, kind)
			}
			if err := borrowRetainedScratch(env, bindingID, kind, slot); err != nil {
				return err
			}
			continue
		}
		handle, prior, already, contended := pool.claimRetainedScratchSlot(key, adopterID)
		switch {
		case already && prior == adopterID:
			return fmt.Errorf("retained scratch: binding %q slot %q was already transferred", bindingID, kind)
		case already:
			// A distinct consumer sharing the allocation borrows the same
			// directory without doubling the lease its adopter holds.
			if err := borrowRetainedScratch(env, bindingID, kind, slot); err != nil {
				return err
			}
		case handle != nil:
			ref := sandbox.ScratchReference{Dir: slot.Dir, Kind: kind}
			if err := env.RestoreSessionScratch(bindingID, ref, handle); err != nil {
				// The transfer failed, so the pool must not keep the slot
				// claimed: drop the claim and leave the handle pooled for a
				// later, successful adoption.
				pool.releaseScratchSlotClaim(key, adopterID)
				return err
			}
			pool.finishRetainedScratchSlot(key)
		case !contended:
			return fmt.Errorf("retained scratch: binding %q slot %q has no reacquired handle", bindingID, kind)
		}
	}
	return nil
}

// borrowRetainedScratch installs a lease-less borrow of one retained directory
// on env without taking a second lease. It holds no pool lock across the borrow
// or the restore.
func borrowRetainedScratch(env *execenv.LocalExecutionEnvironment, bindingID, kind string, slot sandbox.ScratchSlot) error {
	borrow, err := sandbox.BorrowRetainedSessionScratch(slot.Dir)
	if err != nil {
		return err
	}
	ref := sandbox.ScratchReference{Dir: slot.Dir, Kind: kind}
	return env.RestoreSessionScratch(bindingID, ref, borrow)
}

// scratchSlotTakenBy reports the consumer that has claimed or already taken one
// pooled allocation, or "" when no adopter holds it.
func (p *retainedScratchPool) scratchSlotTakenBy(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.adopted[key]
}

// claimRetainedScratchSlot resolves one owning slot under the pool lock. A
// reacquired handle is returned only after the transfer is claimed for
// adopterID, so no concurrent adopter can take the same lease; already reports
// that the slot was claimed before this call, prior names by whom, and
// contended means its lease is held in this process with no reacquired handle.
func (p *retainedScratchPool) claimRetainedScratchSlot(key, adopterID string) (handle *sandbox.SessionScratch, prior string, already, contended bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prior, already = p.adopted[key]; already {
		return nil, prior, true, false
	}
	if handle = p.handles[key]; handle != nil {
		// Claim the transfer before the environment restore so a concurrent
		// adopter borrows instead of doubling the lease.
		p.adopted[key] = adopterID
		return handle, adopterID, false, false
	}
	_, contended = p.contended[key]
	return nil, "", false, contended
}

// finishRetainedScratchSlot commits a claimed transfer: the handle leaves the
// pool and its adopted entry stays as the record of who took the lease.
func (p *retainedScratchPool) finishRetainedScratchSlot(key string) {
	p.mu.Lock()
	delete(p.handles, key)
	p.mu.Unlock()
}

// releaseScratchSlotClaim undoes an uncommitted claim after a failed transfer.
// The handle was never removed, so the pool returns exactly to its
// pre-transfer state.
func (p *retainedScratchPool) releaseScratchSlotClaim(key, adopterID string) {
	p.mu.Lock()
	if p.adopted[key] == adopterID {
		delete(p.adopted, key)
	}
	p.mu.Unlock()
}

// requeueRetainedScratchSlot puts back a handle whose transfer adopterID claimed
// and whose install a caller undid, clearing that claim so a later adoption
// reacquires the allocation instead of erroring on a transfer that no longer
// exists. It reports false when the pool still holds the handle or the claim is
// not this adopter's, and then the caller keeps the handle's directory instead.
func (p *retainedScratchPool) requeueRetainedScratchSlot(key, adopterID string, handle *sandbox.SessionScratch) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.adopted[key] != adopterID || p.handles[key] != nil {
		return false
	}
	delete(p.adopted, key)
	p.handles[key] = handle
	return true
}

// adoptConsumerScratch installs the retained owning slot of sessionID's current
// binding onto env, using the pool prepareRetainedScratch reacquired. It is a
// no-op when no pool exists or the consumer has no current binding.
func (s *Session) adoptConsumerScratch(env *execenv.LocalExecutionEnvironment, sessionID string) (bool, error) {
	if env == nil {
		return false, nil
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		return false, nil
	}
	pool.mu.Lock()
	consumer, ok := pool.consumers[sessionID]
	pool.mu.Unlock()
	if !ok || consumer.CurrentBindingID == "" {
		return false, nil
	}
	if err := s.adoptRetainedScratchFor(env, consumer.CurrentBindingID, sessionID); err != nil {
		return false, err
	}
	return true, nil
}

// retainedScratchSlotContended reports whether the pool holds no reacquired
// handle for dir because its lease is held elsewhere in this process — the
// racing-idle-release window. A slot the pool holds a handle for is never
// contended, whatever a stale contention record beside it says: the handle is
// the transferable reacquire the marker claims is missing, so the guard reads
// the slot as adoptable. A dispose-then-adopt replacement must never run
// against a genuinely contended slot: the adoption cannot take the lease (no
// handle), and with the fresh allocation already disposed the session would end
// up running on the retained directory unowned, beside its in-process holder.
// The replacement is skipped instead, the fresh allocation stays, and the next
// restore re-probes the settled contention (refreshRetainedScratchConsumer)
// and resumes in the retained directory with the lease in hand. An engineered
// absence — no handle, no contention — is NOT skipped here; that refusal
// semantics is pinned elsewhere and stays.
func (s *Session) retainedScratchSlotContended(dir string) bool {
	pool := s.retainedScratch.Load()
	if pool == nil {
		return false
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	key := canonicalScratchDir(dir)
	if _, held := pool.handles[key]; held {
		// A reacquired handle makes the slot pool-owned — transferable —
		// whatever a stale contention record beside it says: the marker and
		// a pooled handle describe mutually exclusive states, and the handle
		// wins. This is also the heal for pools wedged by the pre-round-5
		// refresh: the next restore adopts the pooled handle instead of
		// skipping the slot forever.
		return false
	}
	_, contended := pool.contended[key]
	return contended
}

// adoptResumedRootScratch adopts sessionID's retained allocation onto env on
// the resume path. Resume provisions the sandbox before this runs, and
// EnableSandbox always mints a fresh session scratch; that fresh mint is a
// replacement, not a live allocation, so adoptRetainedScratchFor's same-kind
// guard would skip the persisted sandbox slot and leave the session in a new
// directory with its retained handle orphaned. When the consumer's binding owns
// a sandbox allocation at a different directory, drop the freshly minted
// scratch and rebuild env's kernel wrapper around the retained directory before
// adopting, so the session resumes in the scratch it originally worked in.
//
// Disposal is inherent to that order — RestoreSessionScratch refuses to replace
// an exposed scratch — so the replacement wrapper is built FIRST: a host that
// cannot wrap the retained directory refuses the restore while the minted
// scratch is still intact. A failure after the disposal re-provisions the
// environment's own scratch rather than leaving it with none.
//
// The unsandboxed kind gets the same replacement treatment. The launcher
// environment handed to a restore may already own an unsandboxed scratch its own
// command minted before the restore (session_worktree_resume.go), and that mint
// means adoptRetainedScratchFor's same-kind guard would skip the persisted
// unsandboxed slot. Without replacing it, the resumed root works in the empty
// launch directory while its retained allocation stays unattributed.
func (s *Session) adoptResumedRootScratch(env *execenv.LocalExecutionEnvironment, sessionID string) error {
	if env == nil {
		return nil
	}
	dir, ok := s.retainedConsumerScratchDir(sessionID, sandbox.ScratchKindSandbox)
	if !ok || s.retainedScratchSlotContended(dir) || filepath.Clean(dir) == filepath.Clean(env.SessionScratchDir()) {
		if _, err := s.adoptConsumerScratch(env, sessionID); err != nil {
			return err
		}
	} else {
		if err := s.rebuildSandboxWrapper(env, dir); err != nil {
			return err
		}
		env.DisposeSandboxScratch()
		if _, err := s.adoptConsumerScratch(env, sessionID); err != nil {
			return reprovisionDiscardedSandboxScratch(env, err)
		}
	}
	unsandboxed, ok := s.retainedConsumerScratchDir(sessionID, sandbox.ScratchKindUnsandboxed)
	if !ok || s.retainedScratchSlotContended(unsandboxed) || filepath.Clean(unsandboxed) == filepath.Clean(envScratchRefDir(env, sandbox.ScratchKindUnsandboxed)) {
		return nil
	}
	env.DisposeUnsandboxedScratch()
	if _, err := s.adoptConsumerScratch(env, sessionID); err != nil {
		return err
	}
	return nil
}

// envScratchRefDir returns env's currently owned scratch directory for kind, or
// "" when it owns none. It reads every kind rather than SessionScratchDir, which
// reports only one directory, so the unsandboxed comparison above is not
// confused by a sandbox allocation the environment also owns.
func envScratchRefDir(env *execenv.LocalExecutionEnvironment, kind string) string {
	refs, err := env.ScratchRetentionReferences()
	if err != nil {
		return ""
	}
	for _, ref := range refs {
		if ref.Kind == kind {
			return ref.Dir
		}
	}
	return ""
}

// reprovisionDiscardedSandboxScratch leaves env with a usable sandbox scratch
// after a failed retained-scratch adoption discarded the freshly minted one.
// Adoption has to dispose that mint first (RestoreSessionScratch refuses to
// replace an exposed scratch), so the failure path re-provisions the
// environment's own policy instead of returning one whose only scratch is gone.
// It is a no-op when env already reports a scratch: either the wrapper was
// repointed at the retained directory before the disposal or a partial adoption
// installed it, and both leave a live directory in place.
func reprovisionDiscardedSandboxScratch(env *execenv.LocalExecutionEnvironment, cause error) error {
	if env == nil || env.SessionScratchDir() != "" || env.Sandbox == nil {
		return cause
	}
	if err := env.EnableSandbox(env.Sandbox); err != nil {
		return errors.Join(cause, fmt.Errorf("re-provision sandbox scratch after a failed retained-scratch adoption: %w", err))
	}
	return cause
}

// retainedConsumerScratchDir returns the directory sessionID's current binding
// owns for kind, or ok=false when there is no prepared pool, no consumer, or no
// lease-owning slot of that kind.
func (s *Session) retainedConsumerScratchDir(sessionID, kind string) (string, bool) {
	pool := s.retainedScratch.Load()
	if pool == nil {
		return "", false
	}
	pool.mu.Lock()
	consumer, ok := pool.consumers[sessionID]
	if !ok || consumer.CurrentBindingID == "" {
		pool.mu.Unlock()
		return "", false
	}
	binding, ok := pool.bindings[consumer.CurrentBindingID]
	if !ok {
		pool.mu.Unlock()
		return "", false
	}
	slot, ok := binding.Slots[kind]
	if !ok || !slot.OwnsLease {
		pool.mu.Unlock()
		return "", false
	}
	pool.mu.Unlock()
	return slot.Dir, true
}

// settleFailedRestoreScratch settles the per-session scratch a failed delegate
// restore left on the environment that restore created, so its teardown never
// removes a directory the root's durable retention manifest still references.
// A binding's slots are transferred one at a time and binding.Slots has no
// order, so an adoption can commit an earlier slot and then fail on a later one,
// leaving the environment holding an allocation the pool has already handed
// over; the adoption's own recovery can also re-provision and pin a fresh mint
// before it returns the error. DisposeUnadoptedScratch would then remove a
// referenced directory while its reference and binding slot stay, and
// references are append-only with no unpin API, so the root's retirement
// preparation would refuse forever and a cold resume would fail the same way.
//
// Every referenced allocation the environment holds is therefore kept. A slot
// this restore transferred is handed back and requeued in the pool with its
// claim cleared, so a later adoption reacquires it rather than finding a
// transfer with no handle behind it; any other referenced allocation is
// released (its lease given up, its directory kept, as any handoff does).
// adopterID is the consumer the failed adoption ran for. Only what the manifest
// does not reference — the environment's own fresh mint — is disposed.
func (s *Session) settleFailedRestoreScratch(env execenv.ExecutionEnvironment, adopterID string) {
	local, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok {
		disposeUnadoptedScratch(env)
		return
	}
	refs, err := local.ScratchRetentionReferences()
	if err != nil {
		// What the environment holds cannot be read, so no held directory can
		// be told apart from a fresh mint: keep them all and give up only the
		// leases, never the allocation a reference may name.
		local.RetainSessionScratch()
		return
	}
	referenced, ok := s.retainedScratchReferenceDirs()
	if !ok {
		local.RetainSessionScratch()
		return
	}
	pool := s.retainedScratch.Load()
	for _, ref := range refs {
		key := canonicalScratchDir(ref.Dir)
		if _, retained := referenced[key]; !retained {
			continue
		}
		handle := local.ReleaseSessionScratch(ref.Kind)
		if handle == nil {
			continue
		}
		if pool != nil && pool.requeueRetainedScratchSlot(key, adopterID, handle) {
			continue
		}
		_ = handle.Retain()
	}
	// Whatever is left is the restore's own fresh mint, which no manifest
	// references and no later restore would reacquire.
	local.DisposeUnadoptedScratch()
}

// retainedScratchReferenceDirs returns the canonical directories this session's
// root retention manifest references, keyed the way the pool and the adoption
// compare them. ok is false when the manifest cannot be read, so a caller that
// is deciding what to keep retains everything instead of trusting an empty set;
// a session with no retention authority has no manifest and nothing to keep. A
// RELEASED manifest names no durable allocation — prepareRetainedScratch and
// validateRetainedScratchPresent both return early on one, and the Release
// tombstone is what authorizes the sweeper to collect its directories — so its
// references are historical and keep nothing.
func (s *Session) retainedScratchReferenceDirs() (map[string]struct{}, bool) {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return map[string]struct{}{}, true
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return nil, false
	}
	if manifest.Released {
		return map[string]struct{}{}, true
	}
	dirs := make(map[string]struct{}, len(manifest.References))
	for _, ref := range manifest.References {
		dirs[canonicalScratchDir(ref.Dir)] = struct{}{}
	}
	return dirs, true
}

// canonicalScratchDir resolves a scratch directory to the absolute, cleaned path
// every retention comparison uses.
func canonicalScratchDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(dir)
}

// rebuildSandboxWrapper rebuilds env's kernel wrapper around dir after a restore
// replaced its eagerly provisioned session scratch, so the wrapper grants the
// restored directory as TMPDIR instead of the discarded fresh mint. A wrapperless
// env (an off or write-blocked allocation with no kernel layer) is left alone.
func (s *Session) rebuildSandboxWrapper(env *execenv.LocalExecutionEnvironment, dir string) error {
	if env.Wrapper == nil {
		return nil
	}
	if env.Sandbox == nil {
		return errors.New("scratch retention: sandbox wrapper has no resolved policy")
	}
	wrapper, err := sandbox.NewWrapper(*env.Sandbox, env.Sandbox.HostBinaryPath(), dir)
	if err != nil {
		return err
	}
	env.Wrapper = wrapper
	return nil
}

// recordScratchRetentionError records the first sticky scratch-retention
// publication failure on this session, so a later preparation readiness check
// fails closed instead of trusting a manifest that diverged from the live
// environments. It takes only s.mu and never blocks on I/O.
func (s *Session) recordScratchRetentionError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.scratchRetentionErr == nil {
		s.scratchRetentionErr = err
	}
	s.mu.Unlock()
}

// releaseRetainedScratchPool drops every pooled handle. The handle and adoption
// maps are cleared under the pool lock, then each lease is released outside it,
// so a concurrent adoption never observes a half-cleared map.
func releaseRetainedScratchPool(pool *retainedScratchPool) {
	if pool == nil {
		return
	}
	pool.mu.Lock()
	handles := pool.handles
	pool.handles = map[string]*sandbox.SessionScratch{}
	pool.adopted = map[string]string{}
	pool.mu.Unlock()
	for _, handle := range handles {
		_ = handle.Retain()
	}
}
