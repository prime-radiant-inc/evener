package execenv

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/sandbox"
)

// SetScratchRetentionBinding installs this environment's persisted logical
// identity for the root's scratch-retention manifest. The binding ID is opaque
// and stable across backswap/reuse; it is recorded, not derived from the working
// directory or owning session.
func (e *LocalExecutionEnvironment) SetScratchRetentionBinding(owner sandbox.ScratchOwner, binding sandbox.ScratchBinding) error {
	if strings.TrimSpace(binding.BindingID) == "" {
		return errors.New("execenv: scratch retention binding has no id")
	}
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	e.retentionOwner = owner
	e.retentionBinding = cloneScratchBinding(binding)
	e.retentionSet = true
	return nil
}

// ScratchRetentionBinding returns this environment's logical binding with its
// current owned allocations reflected as lease-owning slots. It does not mutate
// durable state.
func (e *LocalExecutionEnvironment) ScratchRetentionBinding() (sandbox.ScratchBinding, error) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	if !e.retentionSet {
		return sandbox.ScratchBinding{}, errors.New("execenv: no scratch retention binding")
	}
	binding := cloneScratchBinding(e.retentionBinding)
	if binding.Slots == nil {
		binding.Slots = make(map[string]sandbox.ScratchSlot)
	}
	if e.ownedSessionTmp != nil {
		binding.Slots[sandbox.ScratchKindSandbox] = sandbox.ScratchSlot{Dir: e.ownedSessionTmp.Dir, OwnsLease: true}
	}
	if e.unsandboxedScratch != nil {
		binding.Slots[sandbox.ScratchKindUnsandboxed] = sandbox.ScratchSlot{Dir: e.unsandboxedScratch.Dir, OwnsLease: true}
	}
	return binding, nil
}

// ScratchRetentionReferences returns every retained dependency this environment
// currently owns, one per allocation kind. An environment that owns none
// returns an empty slice.
func (e *LocalExecutionEnvironment) ScratchRetentionReferences() ([]sandbox.ScratchReference, error) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	var refs []sandbox.ScratchReference
	if e.ownedSessionTmp != nil {
		refs = append(refs, sandbox.ScratchReference{Dir: e.ownedSessionTmp.Dir, Kind: sandbox.ScratchKindSandbox})
	}
	if e.unsandboxedScratch != nil {
		refs = append(refs, sandbox.ScratchReference{Dir: e.unsandboxedScratch.Dir, Kind: sandbox.ScratchKindUnsandboxed})
	}
	return refs, nil
}

// RestoreSessionScratch installs only the validated owning slot of the exact
// binding identified by bindingID. It refuses a mismatched binding or
// destination and refuses to replace an already-exposed fresh scratch, so a
// restore can never mint a replacement directory or duplicate lease ownership.
func (e *LocalExecutionEnvironment) RestoreSessionScratch(bindingID string, ref sandbox.ScratchReference, scratch *sandbox.SessionScratch) error {
	if scratch == nil || strings.TrimSpace(scratch.Dir) == "" {
		return errors.New("execenv: restore requires a scratch handle")
	}
	if filepath.Clean(ref.Dir) != filepath.Clean(scratch.Dir) {
		return fmt.Errorf("execenv: restored scratch %q does not match reference %q", scratch.Dir, ref.Dir)
	}
	e.scratchMu.Lock()
	if !e.retentionSet || e.retentionBinding.BindingID != bindingID {
		e.scratchMu.Unlock()
		return fmt.Errorf("execenv: restore binding %q does not match the installed binding", bindingID)
	}
	switch ref.Kind {
	case sandbox.ScratchKindSandbox:
		if e.ownedSessionTmp != nil {
			e.scratchMu.Unlock()
			return errors.New("execenv: refusing to replace an exposed sandbox scratch")
		}
		e.ownedSessionTmp = scratch
	case sandbox.ScratchKindUnsandboxed:
		if e.unsandboxedScratch != nil {
			e.scratchMu.Unlock()
			return errors.New("execenv: refusing to replace an exposed unsandboxed scratch")
		}
		e.unsandboxedScratch = scratch
	default:
		e.scratchMu.Unlock()
		return fmt.Errorf("execenv: unknown scratch kind %q", ref.Kind)
	}
	e.scratchMu.Unlock()
	e.invalidateSandboxFS()
	return nil
}

func cloneScratchBinding(binding sandbox.ScratchBinding) sandbox.ScratchBinding {
	clone := binding
	if binding.Slots != nil {
		clone.Slots = make(map[string]sandbox.ScratchSlot, len(binding.Slots))
		for kind, slot := range binding.Slots {
			clone.Slots[kind] = slot
		}
	}
	return clone
}
