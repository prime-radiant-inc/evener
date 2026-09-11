package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// validateRetainedScratchPresent verifies every referenced allocation still
// exists at its original path and every binding slot resolves to a pinned
// reference. It neither acquires a lease nor mutates durable state, so it is
// safe during preparation.
func (s *Session) validateRetainedScratchPresent() error {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
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
	}
	for _, ref := range manifest.References {
		handle, err := sandbox.OpenRetainedSessionScratch(owner, ref)
		if err != nil {
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
	}
	return nil
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
