package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
		return nil
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
		return nil
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
	moved := sourceBinding.Slots
	for attempt := 0; attempt < 5; attempt++ {
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
		err = sandbox.UpdateScratchBindings(owner, manifest.Revision,
			[]sandbox.ScratchBinding{targetRecord, sourceRecord},
			[]sandbox.ScratchConsumerBinding{consumer})
		if errors.Is(err, sandbox.ErrScratchRetentionStaleRevision) {
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
		return nil
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
	binding, ok := pool.bindings[bindingID]
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
	if err := s.installScratchRetention(env); err != nil {
		return err
	}
	installed, err := env.ScratchRetentionBinding()
	if err != nil {
		return nil
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

// validateRetainedScratchPresent verifies every referenced allocation still
// exists at its original path and every binding slot resolves to a pinned
// reference. It neither acquires a lease nor mutates durable state, so it is
// safe during preparation.
func (s *Session) validateRetainedScratchPresent() error {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	if s.retainedScratch.Load() != nil {
		return nil
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
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
	// directory while a duplicate transfer by the same consumer is refused.
	adopted map[string]string
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

// adoptRetainedScratch transfers the owning slots of exactly bindingID from the
// pool onto env, installs wrapper-only borrows without duplicating lease
// ownership, and verifies the binding identity. It is a no-op when no pool was
// prepared, so a session with no retention manifest keeps its existing path.
func (s *Session) adoptRetainedScratch(env *execenv.LocalExecutionEnvironment, bindingID string) error {
	return s.adoptRetainedScratchFor(env, bindingID, s.id)
}

// adoptRetainedScratchFor is adoptRetainedScratch with an explicit adopter
// identity: a distinct consumer sharing an allocation that another session
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
	binding, ok := pool.bindings[bindingID]
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
		if slot.OwnsLease && existingKinds[kind] {
			continue
		}
		key := filepath.Clean(slot.Dir)
		handle := pool.handles[key]
		if handle == nil {
			if prior, already := pool.adopted[key]; already {
				if prior == adopterID {
					return fmt.Errorf("retained scratch: binding %q slot %q was already transferred", bindingID, kind)
				}
				borrow, err := sandbox.BorrowRetainedSessionScratch(slot.Dir)
				if err != nil {
					return err
				}
				ref := sandbox.ScratchReference{Dir: slot.Dir, Kind: kind}
				if err := env.RestoreSessionScratch(bindingID, ref, borrow); err != nil {
					return err
				}
				continue
			}
			if _, contended := pool.contended[key]; contended {
				// The allocation's lease is still held in this process; it
				// cannot be double-owned, so leave it with its holder.
				continue
			}
			return fmt.Errorf("retained scratch: binding %q slot %q has no reacquired handle", bindingID, kind)
		}
		if !slot.OwnsLease {
			// A wrapper-only borrow points at the same retained directory
			// without taking a second lease.
			continue
		}
		ref := sandbox.ScratchReference{Dir: slot.Dir, Kind: kind}
		if err := env.RestoreSessionScratch(bindingID, ref, handle); err != nil {
			return err
		}
		delete(pool.handles, key)
		pool.adopted[key] = adopterID
	}
	return nil
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
	consumer, ok := pool.consumers[sessionID]
	if !ok || consumer.CurrentBindingID == "" {
		return false, nil
	}
	if err := s.adoptRetainedScratchFor(env, consumer.CurrentBindingID, sessionID); err != nil {
		return false, err
	}
	return true, nil
}

func releaseRetainedScratchPool(pool *retainedScratchPool) {
	if pool == nil {
		return
	}
	for _, handle := range pool.handles {
		_ = handle.Retain()
	}
	pool.handles = map[string]*sandbox.SessionScratch{}
}

// retainedScratchBindingFor resolves the binding ID a session occupies for the
// given role. It is the join point between a cold descriptor's ChildSessionID
// and the manifest's Consumers.SessionID.
func (s *Session) retainedScratchBindingFor(sessionID, role string) (string, bool) {
	pool := s.retainedScratch.Load()
	if pool == nil {
		return "", false
	}
	consumer, ok := pool.consumers[sessionID]
	if !ok {
		return "", false
	}
	switch role {
	case "current":
		return consumer.CurrentBindingID, consumer.CurrentBindingID != ""
	case "parent_shared":
		return consumer.ParentSharedBindingID, consumer.ParentSharedBindingID != ""
	case "worktree_restore":
		return consumer.WorktreeRestoreBindingID, consumer.WorktreeRestoreBindingID != ""
	default:
		return "", false
	}
}
