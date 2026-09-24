package execenv

import (
	"errors"
	"fmt"
	"maps"
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
	// The PREVIOUS identity decides the reset: reading the field only
	// before it is overwritten below, or the comparison would match the new
	// binding against itself and never fire.
	prevID := e.retentionBinding.BindingID
	e.retentionOwner = owner
	e.retentionBinding = cloneScratchBinding(binding)
	e.retentionSet = true
	// A genuinely new binding identity re-derives contention for every kind
	// this cycle, so a pending marker must not outlive it. The SAME identity
	// re-installed is a transfer or a re-adoption of one logical environment
	// (a re-rooted clone inheriting its binding, an adoption cycle) and its
	// pending kinds travel with it: clearing them here would let the first
	// publication on the receiving environment claim a contended retained
	// slot and end the continuity retry (round 12).
	if prevID != binding.BindingID {
		e.retentionPending = nil
	}
	return nil
}

// MarkRetainedSlotPending records that kind's retained owning slot was skipped
// by adoption because its lease is held elsewhere in this process — typically
// the idle-release teardown racing the restore. While the kind is pending,
// PinOwnedScratch pins any fresh fallback mint as a bare protected reference
// instead of claiming the binding's slot for it, so the manifest row keeps
// naming the retained directory and a later refresh re-probes it once the
// contention settles.
func (e *LocalExecutionEnvironment) MarkRetainedSlotPending(kind string) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	if e.retentionPending == nil {
		e.retentionPending = make(map[string]struct{})
	}
	e.retentionPending[kind] = struct{}{}
}

// RetentionPendingKinds returns the kinds whose retained owning slot this
// environment recorded as contended-pending, for carrying the marker across a
// binding transfer to another environment object.
func (e *LocalExecutionEnvironment) RetentionPendingKinds() []string {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	kinds := make([]string, 0, len(e.retentionPending))
	for kind := range e.retentionPending {
		kinds = append(kinds, kind)
	}
	return kinds
}

// ScratchRetentionOwner returns the manifest owner this environment's binding
// was installed under, reporting whether any binding is installed at all. A
// caller uses it to tell an identity this owner's manifest lost to a reset
// from one installed under a different root's manifest.
func (e *LocalExecutionEnvironment) ScratchRetentionOwner() (sandbox.ScratchOwner, bool) {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	return e.retentionOwner, e.retentionSet
}

// PinOwnedScratch publishes this environment's installed binding and pins every
// allocation it currently owns into the owner's manifest, under the live leases
// it holds, in ONE manifest transaction: the references and the binding that
// owns them become durable together or not at all. It is idempotent and safe to
// call after any allocation is minted. It never writes a consumer record: an
// environment may serve several consumers (a root and the children sharing its
// object), and only the agent layer knows which of them owns which binding role,
// so a mint must not re-point them. A failure is recorded sticky for the
// preparation readiness check and returned to the caller; it never silently
// succeeds.
func (e *LocalExecutionEnvironment) PinOwnedScratch() error {
	e.scratchMu.Lock()
	if !e.retentionSet {
		e.scratchMu.Unlock()
		return nil
	}
	owner := e.retentionOwner
	binding := cloneScratchBinding(e.retentionBinding)
	pending := maps.Clone(e.retentionPending)
	handles := make(map[string]*sandbox.SessionScratch)
	if e.ownedSessionTmp != nil {
		handles[sandbox.ScratchKindSandbox] = e.ownedSessionTmp
	}
	if e.unsandboxedScratch != nil {
		handles[sandbox.ScratchKindUnsandboxed] = e.unsandboxedScratch
	}
	e.scratchMu.Unlock()

	if binding.Slots == nil {
		binding.Slots = make(map[string]sandbox.ScratchSlot)
	}
	owned := make(map[string]*sandbox.SessionScratch, len(handles))
	for kind, handle := range handles {
		if !handle.HasLease() {
			// Already retained/handed off; it can no longer be pinned, and a
			// stale slot must not be republished.
			continue
		}
		owned[kind] = handle
	}
	// Nothing owned live: there is nothing to pin, and the installed binding's
	// stale slots — naming allocations a moved allocation or a manifest reset
	// took away — must not be submitted: their validation failure would
	// reject the slotless republish the reinstall performs next. The
	// inherited-identity case was always meant to no-op here (round 18).
	// Ownership has moved on, so a prior recoverable race — a lock holder that
	// has since let go, a released manifest a reset went on to repair — must
	// not keep failing preparation either: every later pin no-ops, so nothing
	// else could ever clear the record. The recovery clear preserves genuine
	// durability verdicts (round 59).
	if len(owned) == 0 {
		e.clearRecoverableRetentionPinError()
		return nil
	}
	// Pinning one handle at a time and publishing the binding afterwards left the
	// earlier pins' durable references owned by no binding whenever a later pin or
	// the publication failed, which restore cannot attribute and nothing can
	// collect (round 19). One transaction publishes them together.
	// The manifest's update lock is fail-fast, so a concurrent in-process
	// writer — a delegate restore's refresh install, another environment's
	// mint — can refuse this pin with lock-held for as long as its fsync-scale
	// hold lasts. That refusal is transient by construction, never a
	// durability verdict, so the pin retries it with backoff before recording
	// anything sticky: a lock race must not permanently poison a live
	// environment's retention state.
	pin := 0
	err := sandbox.RetryScratchLockContention(func() error {
		pin++
		if e.scratchPinProbe != nil {
			e.scratchPinProbe(pin)
		}
		return sandbox.PinScratchBinding(owner, binding, owned, pending)
	})
	if err != nil {
		e.recordRetentionPinError(err)
		return err
	}
	// A pin that succeeded under the manifest that exists now proves any
	// earlier released- or lock-held race healed — the reset reinitialized the
	// tombstone this very pin raced, and the lock holder that exhausted the
	// retry bound has since let go — so keeping either failure sticky would
	// fail preparation forever on a state that no longer holds. Other pin
	// failures are durability verdicts that a later success does not
	// retroactively explain away (round 9's sticky contract, round 27's and
	// round 58's recoverable carve-outs).
	e.clearRecoverableRetentionPinError()
	return nil
}

// ScratchRetentionError returns the first sticky retention-pin failure recorded
// on this environment, or nil. Preparation surfaces it as a persistence error.
func (e *LocalExecutionEnvironment) ScratchRetentionError() error {
	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()
	return e.retentionPinErr
}

func (e *LocalExecutionEnvironment) recordRetentionPinError(err error) {
	if err == nil {
		return
	}
	e.scratchMu.Lock()
	if e.retentionPinErr == nil {
		e.retentionPinErr = err
	}
	e.scratchMu.Unlock()
}

// clearRecoverableRetentionPinError drops a sticky pin failure that a later
// successful pin proves was a race, not a durability verdict: the released
// and lock-held sentinels both describe contention against transient
// manifest state — the tombstone a reset went on to reinitialize, the
// fail-fast update lock a concurrent writer went on to release — so a pin
// that has since succeeded clears them (round 27's carve-out, round 58's
// extension to the exhausted lock race).
func (e *LocalExecutionEnvironment) clearRecoverableRetentionPinError() {
	e.scratchMu.Lock()
	if e.retentionPinErr != nil &&
		(errors.Is(e.retentionPinErr, sandbox.ErrScratchRetentionReleased) ||
			errors.Is(e.retentionPinErr, sandbox.ErrScratchRetentionLockHeld)) {
		e.retentionPinErr = nil
	}
	e.scratchMu.Unlock()
}

// pinOwnedScratchAfterMint runs after a fresh allocation is installed, outside
// scratchMu, so a live session actually pins what it just created.
func (e *LocalExecutionEnvironment) pinOwnedScratchAfterMint() {
	if err := e.PinOwnedScratch(); err != nil {
		e.recordRetentionPinError(err)
	}
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
	// A pending kind's durable truth is the installed slot — the retained
	// directory the manifest row still names — not the live fallback mint.
	_, pendingSandbox := e.retentionPending[sandbox.ScratchKindSandbox]
	_, pendingUnsandboxed := e.retentionPending[sandbox.ScratchKindUnsandboxed]
	if e.ownedSessionTmp != nil && e.ownedSessionTmp.HasLease() && !pendingSandbox {
		binding.Slots[sandbox.ScratchKindSandbox] = sandbox.ScratchSlot{Dir: e.ownedSessionTmp.Dir, OwnsLease: true}
	}
	if e.unsandboxedScratch != nil && e.unsandboxedScratch.HasLease() && !pendingUnsandboxed {
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
	// The restored directory may be spelled differently from the binding
	// row's slot (a relative spelling canonicalizes to the handle's
	// absolute path): compare canonically, the same normalization every
	// pool key uses, or the same directory named two ways is refused as a
	// mismatch (round 43).
	refDir, refErr := filepath.Abs(ref.Dir)
	scratchDir, scratchErr := filepath.Abs(scratch.Dir)
	if refErr != nil || scratchErr != nil || filepath.Clean(refDir) != filepath.Clean(scratchDir) {
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
	// The retained slot for this kind was adopted: the environment owns the
	// retained directory again, so later mints publish normally.
	delete(e.retentionPending, ref.Kind)
	e.scratchMu.Unlock()
	e.invalidateSandboxFS()
	return nil
}

func cloneScratchBinding(binding sandbox.ScratchBinding) sandbox.ScratchBinding {
	clone := binding
	if binding.Slots != nil {
		clone.Slots = make(map[string]sandbox.ScratchSlot, len(binding.Slots))
		maps.Copy(clone.Slots, binding.Slots)
	}
	return clone
}
