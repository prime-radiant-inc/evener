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
// env. It reuses an already-installed binding (a shared environment) or an
// existing binding that already owns one of env's allocations, so a directory
// never gains a second lease-owning binding. Otherwise it mints a new opaque id.
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
		consumer := sandbox.ScratchConsumerBinding{SessionID: sessionID, CurrentBindingID: stored.BindingID}
		return sandbox.UpsertScratchBinding(owner, stored, consumer)
	}
	// No installed binding: reuse a stored binding that already owns one of
	// env's allocations (a wrapper borrow), else mint a fresh one.
	binding, ok, err := reuseScratchBindingForEnv(owner, env)
	if err != nil {
		return err
	}
	if !ok {
		minted, err := s.scratchRetentionBindingID(owner, sessionID)
		if err != nil {
			return err
		}
		binding = sandbox.ScratchBinding{
			BindingID:      minted,
			OwnerSessionID: sessionID,
			WorkingDir:     env.WorkingDirectory(),
		}
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
	consumer := sandbox.ScratchConsumerBinding{SessionID: sessionID, CurrentBindingID: published.BindingID}
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

// reuseScratchBindingForEnv returns the binding id that already owns one of
// env's current lease-owning allocations, or ok=false when none does.
func reuseScratchBindingForEnv(owner sandbox.ScratchOwner, env *execenv.LocalExecutionEnvironment) (sandbox.ScratchBinding, bool, error) {
	refs, err := env.ScratchRetentionReferences()
	if err != nil || len(refs) == 0 {
		return sandbox.ScratchBinding{}, false, err
	}
	dirs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if dir, err := filepath.Abs(ref.Dir); err == nil {
			dirs[filepath.Clean(dir)] = struct{}{}
		}
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return sandbox.ScratchBinding{}, false, err
	}
	for _, binding := range manifest.Bindings {
		for _, slot := range binding.Slots {
			if !slot.OwnsLease {
				continue
			}
			if dir, err := filepath.Abs(slot.Dir); err == nil {
				if _, ok := dirs[filepath.Clean(dir)]; ok {
					return binding, true, nil
				}
			}
		}
	}
	return sandbox.ScratchBinding{}, false, nil
}

// scratchRetentionBindingID returns this session's persisted current binding id,
// or mints a fresh opaque one on first publication.
func (s *Session) scratchRetentionBindingID(owner sandbox.ScratchOwner, sessionID string) (string, error) {
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return "", err
	}
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID == sessionID && consumer.CurrentBindingID != "" {
			return consumer.CurrentBindingID, nil
		}
	}
	return identifier.NewSessionID()
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
	if sameEnvironment(shared, env) {
		consumer.ParentSharedBindingID = binding.BindingID
	}
	if restore == env {
		consumer.WorktreeRestoreBindingID = binding.BindingID
	}
	for _, candidate := range abandoned {
		if candidate == env {
			consumer.AbandonedBindingIDs = append(consumer.AbandonedBindingIDs, binding.BindingID)
			break
		}
	}
	return sandbox.UpsertScratchBinding(owner, binding, consumer)
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
	adopted   map[string]struct{}
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
		adopted:   make(map[string]struct{}),
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
	for kind, slot := range binding.Slots {
		key := filepath.Clean(slot.Dir)
		handle := pool.handles[key]
		if handle == nil {
			if _, already := pool.adopted[key]; already {
				return fmt.Errorf("retained scratch: binding %q slot %q was already transferred", bindingID, kind)
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
		pool.adopted[key] = struct{}{}
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
	if err := s.adoptRetainedScratch(env, consumer.CurrentBindingID); err != nil {
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
