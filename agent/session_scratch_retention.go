package agent

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

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
	// The reset below reports whether it performed a carry, and only that
	// verdict decides what a fresh install may adopt. The pre-read it
	// replaces sampled the manifest's Released state through an unlocked
	// load, so a terminal release landing between that read and the reset's
	// own locked read made the stale flag say not-released while the reset
	// still carried the durable rows: the fresh mint then replaced the
	// carried consumer and orphaned the carried binding's lease-owning slot
	// — the exact graph the reader fails closed on, with no later reset left
	// to repair it (round 22).
	if hook := s.cfg.testOnly.scratchInstallBeforeReset; hook != nil {
		hook()
	}
	manifest, resetPerformed, err := sandbox.ResetScratchRetentionIfReleased(owner)
	if err != nil {
		return err
	}
	// An environment that already carries an installed binding keeps it: a
	// shared parent environment's binding is owned by whoever published it, and
	// must not be renamed to this consumer or republished under another root. If
	// it belongs to this owner's manifest, register only the consumer's role;
	// an identity of this owner that a manifest reset orphaned re-registers
	// the same way (round 15).
	if existing, err := env.ScratchRetentionBinding(); err == nil && existing.BindingID != "" {
		if _, ok := findScratchBinding(manifest, existing.BindingID); !ok {
			installedOwner, installed := env.ScratchRetentionOwner()
			if !installed || installedOwner != owner {
				// A binding installed under another root's manifest is not
				// this consumer's to rename or republish under this root.
				return nil
			}
			// This binding is ours and the fresh manifest does not name it:
			// the reset above reinitialized a manifest whose terminal release
			// orphaned the identity, so the registration below re-publishes
			// it instead of leaving later publications without a row.
		}
		installRefusals := 0
		for range 5 {
			if hook := s.cfg.testOnly.scratchUpsertAttempt; hook != nil {
				hook()
			}
			// Recompute both rows from the manifest as it stands NOW: a
			// concurrent writer that committed while a refused attempt
			// waited on the lock — a role update, a slot move on the same
			// binding — must not be overwritten by this pass's stale
			// snapshot when its retry replays the upsert.
			fresh, err := sandbox.LoadScratchRetention(owner)
			if err != nil {
				return err
			}
			if hook := s.cfg.testOnly.scratchUpsertAfterLoad; hook != nil {
				hook()
			}
			freshBinding, ok := findScratchBinding(fresh, existing.BindingID)
			if !ok {
				// The reset orphaned the old rows. An identity whose
				// environment still owns live handles must not be
				// re-published slotless: the reset's carry never reached
				// those allocations (their pair died with the old manifest),
				// so this registration is their one durable home — re-pin
				// them, then re-derive from the manifest the pin just moved.
				// An inherited identity owns nothing to pin, so the pin is a
				// no-op there and the slotless republish below is exactly
				// what arrives (round 18).
				if err := env.PinOwnedScratch(); err != nil {
					return err
				}
				if fresh, err = sandbox.LoadScratchRetention(owner); err != nil {
					return err
				}
				if freshBinding, ok = findScratchBinding(fresh, existing.BindingID); !ok {
					republished := existing
					republished.Slots = nil
					freshBinding = republished
				}
			}
			// The revision check refuses a manifest that moved since these
			// rows were derived — the consumer merge replaces rows
			// wholesale, so a row from a superseded snapshot would clobber
			// whatever committed in between (round 14).
			err = sandbox.UpsertScratchBindingAtRevision(owner, freshBinding, scratchConsumerPreservingRoles(fresh, sessionID, freshBinding.BindingID), fresh.Revision)
			// A stale revision re-derives immediately; only a fail-fast lock
			// refusal — transient by construction, held by a mortal
			// in-process writer — waits out the hold with the shared backoff.
			if errors.Is(err, sandbox.ErrScratchRetentionStaleRevision) ||
				errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
				if errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
					s.sleepScratchLockBackoff(installRefusals)
					installRefusals++
				}
				continue
			}
			// A non-retryable write error may still be a committed write:
			// the manifest rename can commit before writeScratchRetention
			// reports a post-rename failure. When the durable manifest
			// already carries exactly the rows this pass derived, the
			// install is done — report success instead of aborting over
			// rows that are already present (the round 39/43/45
			// committed-write pattern).
			if err != nil && scratchUpsertCommitted(owner, freshBinding, scratchConsumerPreservingRoles(fresh, sessionID, freshBinding.BindingID)) {
				return nil
			}
			return err
		}
		return fmt.Errorf("scratch retention: install rows for %q stayed stale", sessionID)
	}
	binding := sandbox.ScratchBinding{
		OwnerSessionID: sessionID,
		WorkingDir:     env.WorkingDirectory(),
	}
	if resetPerformed {
		// A consumer row the released manifest's reset carried may already
		// name this session with a binding that survived the reset — the
		// contended pair, whose lease-owning slot names a directory a later
		// restore can still re-probe and resume in. Minting a fresh binding
		// would replace that row (the upsert below rewrites this session's
		// consumer) and orphan the carried binding's slot: the graph reader
		// fails closed on a slot no consumer role names, with Released false
		// again and no later reset left to repair it. Adopt the carried
		// binding instead, keeping the durable identity the manifest already
		// records for this consumer.
		if carried, ok := findCarriedConsumerBinding(manifest, sessionID); ok {
			binding = carried
		}
	}
	if binding.BindingID == "" {
		bindingID, err := identifier.NewSessionID()
		if err != nil {
			return err
		}
		binding.BindingID = bindingID
	}
	if err := env.SetScratchRetentionBinding(owner, binding); err != nil {
		return err
	}
	// The environment's freshly minted allocations are fallbacks for any
	// adopted slot whose directory they are not: mark those kinds pending so
	// the mint pins as a bare protected reference and the carried slot keeps
	// naming the retained directory for a later restore to re-probe (the
	// round-10 displacement contract).
	for kind, slot := range binding.Slots {
		if !slot.OwnsLease {
			continue
		}
		if owned := envScratchRefDir(env, kind); owned == "" || canonicalScratchDir(owned) != canonicalScratchDir(slot.Dir) {
			env.MarkRetainedSlotPending(kind)
		}
	}
	if err := env.PinOwnedScratch(); err != nil {
		return err
	}
	installRefusals := 0
	for range 5 {
		if hook := s.cfg.testOnly.scratchUpsertAttempt; hook != nil {
			hook()
		}
		// Recompute both rows from the manifest as it stands NOW: a role
		// update that committed while a refused attempt waited on the lock
		// must not be overwritten by this pass's stale snapshot when its
		// retry replays the upsert — the same recompute the existing-binding
		// and role-registration closures run (round 12).
		published, err := env.ScratchRetentionBinding()
		if err != nil {
			// SetScratchRetentionBinding just recorded the binding, so this
			// read-back failing is unexpected — and failing loudly is the
			// contract: returning nil here reported a successful install
			// while silently skipping the consumer row a later refresh
			// re-probes (round 49).
			return err
		}
		fresh, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			return err
		}
		if hook := s.cfg.testOnly.scratchUpsertAfterLoad; hook != nil {
			hook()
		}
		// The revision check refuses a manifest that moved since these rows
		// were derived, so a row from a superseded snapshot cannot clobber a
		// concurrent commit (round 14).
		err = sandbox.UpsertScratchBindingAtRevision(owner, published, scratchConsumerPreservingRoles(fresh, sessionID, published.BindingID), fresh.Revision)
		if errors.Is(err, sandbox.ErrScratchRetentionStaleRevision) ||
			errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
			if errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
				s.sleepScratchLockBackoff(installRefusals)
				installRefusals++
			}
			continue
		}
		// A non-retryable write error may still be a committed write: the
		// rename can commit before writeScratchRetention reports a
		// post-rename failure. When the durable manifest already carries
		// exactly the rows this pass derived, the install is done (the
		// round 39/43/45 committed-write pattern).
		if err != nil && scratchUpsertCommitted(owner, published, scratchConsumerPreservingRoles(fresh, sessionID, published.BindingID)) {
			return nil
		}
		return err
	}
	return fmt.Errorf("scratch retention: install rows for %q stayed stale", sessionID)
}

func findScratchBinding(manifest sandbox.ScratchManifest, bindingID string) (sandbox.ScratchBinding, bool) {
	for _, binding := range manifest.Bindings {
		if binding.BindingID == bindingID {
			return binding, true
		}
	}
	return sandbox.ScratchBinding{}, false
}

// findCarriedConsumerBinding resolves the binding a manifest's consumer row
// for sessionID still names as its current identity. A released manifest's
// reset carries contended pairs binding-first, so the row a cold restore
// lands on may already have durable state the reinstall must keep instead of
// orphaning.
func findCarriedConsumerBinding(manifest sandbox.ScratchManifest, sessionID string) (sandbox.ScratchBinding, bool) {
	for _, consumer := range manifest.Consumers {
		if consumer.SessionID != sessionID || consumer.CurrentBindingID == "" {
			continue
		}
		if binding, ok := findScratchBinding(manifest, consumer.CurrentBindingID); ok {
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
// scratchConsumerPreservingRoles builds the consumer row a reinstall or swap
// publishes: the caller's binding becomes the current one, and every role the
// existing row carried survives. The displaced current binding is preserved as
// abandoned when its row still exists — a lease-owning slot no consumer role
// names fails the graph reader closed and blocks every later restore, and the
// abandoned role is the model's own home for a binding the consumer moved off
// of (round 61).
func scratchConsumerPreservingRoles(manifest sandbox.ScratchManifest, sessionID, currentBindingID string) sandbox.ScratchConsumerBinding {
	consumer := sandbox.ScratchConsumerBinding{SessionID: sessionID, CurrentBindingID: currentBindingID}
	for _, existing := range manifest.Consumers {
		if existing.SessionID != sessionID {
			continue
		}
		consumer.ParentSharedBindingID = existing.ParentSharedBindingID
		consumer.WorktreeRestoreBindingID = existing.WorktreeRestoreBindingID
		consumer.AbandonedBindingIDs = existing.AbandonedBindingIDs
		displaced := existing.CurrentBindingID
		if displaced != "" && displaced != currentBindingID &&
			consumer.ParentSharedBindingID != displaced &&
			consumer.WorktreeRestoreBindingID != displaced &&
			!slices.Contains(consumer.AbandonedBindingIDs, displaced) {
			for _, binding := range manifest.Bindings {
				if binding.BindingID == displaced {
					consumer.AbandonedBindingIDs = append(consumer.AbandonedBindingIDs, displaced)
					break
				}
			}
		}
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
	// A contended retained slot's pending marker travels with the allocation
	// it protects: the target's post-move pin (AdoptSessionScratch's
	// PinOwnedScratch) publishes with the target's own marker set, or the
	// moved fallback mint would claim the binding's slot and displace the
	// retained directory the later refresh must re-probe (round 13).
	for _, kind := range source.RetentionPendingKinds() {
		if _, moving := moved[kind]; !moving {
			continue
		}
		if _, kept := keptKinds[kind]; kept {
			continue
		}
		target.MarkRetainedSlotPending(kind)
	}
	swapLockRefusals := 0
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
			// The manifest's record and the environment's binding may spell
			// one directory differently (a relative spelling against the
			// handle's absolute): compare canonically, or the cleanup leaves
			// the source binding owning the moved allocation and the
			// manifest rejects the swap as duplicate lease ownership
			// (round 44).
			if current, ok := sourceRecord.Slots[kind]; ok && canonicalScratchDir(current.Dir) == canonicalScratchDir(slot.Dir) {
				delete(sourceRecord.Slots, kind)
			}
			if _, kept := keptKinds[kind]; kept {
				continue
			}
			// The slot moves verbatim, its OwnsLease flag with it: a
			// wrapper-only slot is a borrowed directory whose lease another
			// binding owns, and promoting it to a lease-owning slot would
			// make the manifest claim the target owns a lease that was never
			// transferred (round 32).
			targetRecord.Slots[kind] = slot
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
			// A lock refusal is transient — the holder is a mortal
			// in-process writer — while a stale revision re-derives
			// immediately. Only the refusal waits out the hold, or one
			// fsync-scale writer consumes the whole shared bound and turns
			// a transient race into a failed worktree move.
			if errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
				s.sleepScratchLockBackoff(swapLockRefusals)
				swapLockRefusals++
			}
			continue
		}
		if err == nil {
			return nil
		}
		// A write whose rename already committed can still report the
		// post-rename failure class (writeScratchRetention's probe fires
		// after atomicWritePrivateFile): the durable manifest then names
		// the target's slots while the handles still live on the source,
		// and returning the error would abort the caller before
		// AdoptSessionScratch leaves the manifest and the environment
		// disagreeing about who owns the allocation. Detect the committed
		// transition by content — every moved kind landed on the target
		// and the consumer names it — and report success so the caller
		// completes the adoption. A write that did not commit leaves the
		// target without the moved slots, and the error stands (round 43;
		// the round 39 commit discriminator applied to the swap).
		if fresh, rerr := sandbox.LoadScratchRetention(owner); rerr == nil {
			if rec, ok := findScratchBinding(fresh, targetID); ok {
				committed := true
				for kind, slot := range moved {
					if _, kept := keptKinds[kind]; kept {
						continue
					}
					got, has := rec.Slots[kind]
					if !has || canonicalScratchDir(got.Dir) != canonicalScratchDir(slot.Dir) {
						committed = false
						break
					}
				}
				if committed {
					for _, consumer := range fresh.Consumers {
						if consumer.SessionID == sessionID && consumer.CurrentBindingID == targetID {
							return nil
						}
					}
				}
			}
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
	if err := target.SetScratchRetentionBinding(owner, binding); err != nil {
		return err
	}
	// A contended retained slot's pending marker is part of the logical
	// environment the target inherits: without it the target's first fresh
	// mint would claim the manifest's retained slot and end the continuity
	// retry the source was still owed (round 12).
	for _, kind := range source.RetentionPendingKinds() {
		target.MarkRetainedSlotPending(kind)
	}
	return nil
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
		// installScratchRetention already installed the binding above; a
		// read-back failure here is unexpected, and swallowing it reported
		// success while skipping the consumer row entirely (round 49).
		return err
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		return err
	}
	if _, ok := findScratchBinding(manifest, installed.BindingID); !ok {
		return nil
	}
	registerRefusals := 0
	for range 5 {
		if hook := s.cfg.testOnly.scratchUpsertAttempt; hook != nil {
			hook()
		}
		// Recompute the rows from the manifest as it stands NOW: a role
		// update that committed while a refused attempt waited on the lock
		// must not be overwritten by this pass's stale snapshot when its
		// retry replays the upsert.
		fresh, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			return err
		}
		if hook := s.cfg.testOnly.scratchUpsertAfterLoad; hook != nil {
			hook()
		}
		freshBinding, ok := findScratchBinding(fresh, installed.BindingID)
		if !ok {
			return nil
		}
		consumer := sandbox.ScratchConsumerBinding{SessionID: s.id, CurrentBindingID: freshBinding.BindingID}
		s.mu.Lock()
		shared := s.parentSharedEnv
		restore := s.worktreeRestoreEnv
		abandoned := append([]*execenv.LocalExecutionEnvironment(nil), s.abandonedEnvs...)
		s.mu.Unlock()
		// Each role resolves its OWN environment's binding, which may differ
		// from the published environment's binding (a shared or parked
		// environment), so a Task 6 consumer can join every role to its
		// exact binding.
		if sharedEnv, ok := shared.(*execenv.LocalExecutionEnvironment); ok {
			if id, ok := s.roleScratchBindingID(fresh, sharedEnv); ok {
				consumer.ParentSharedBindingID = id
			}
		}
		if id, ok := s.roleScratchBindingID(fresh, restore); ok {
			consumer.WorktreeRestoreBindingID = id
		}
		for _, candidate := range abandoned {
			if id, ok := s.roleScratchBindingID(fresh, candidate); ok {
				consumer.AbandonedBindingIDs = append(consumer.AbandonedBindingIDs, id)
			}
		}
		// The revision check refuses a manifest that moved since these rows
		// were derived, so a row from a superseded snapshot cannot clobber a
		// concurrent commit (round 14).
		err = sandbox.UpsertScratchBindingAtRevision(owner, freshBinding, consumer, fresh.Revision)
		if errors.Is(err, sandbox.ErrScratchRetentionStaleRevision) ||
			errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
			if errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
				s.sleepScratchLockBackoff(registerRefusals)
				registerRefusals++
			}
			continue
		}
		// A non-retryable write error may still be a committed write: the
		// rename can commit before writeScratchRetention reports a
		// post-rename failure. When the durable manifest already carries
		// exactly the rows this pass derived, the registration is done
		// (the round 39/43/45 committed-write pattern).
		if err != nil && scratchUpsertCommitted(owner, freshBinding, consumer) {
			return nil
		}
		return err
	}
	return fmt.Errorf("scratch retention: register rows for %q stayed stale", s.id)
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
	// and copy the row out; a refresh only ever installs NEWER manifest rows —
	// and removes, on its declines, rows the live manifest no longer backs —
	// so a reader holding a pre-refresh copy sees exactly the world a restore
	// before it would have, never a torn one.
	mu sync.Mutex
}

// dropRetainedScratchConsumerRow removes sessionID's consumer row from the
// pool. The refresh calls it on every decline that means the live manifest no
// longer authorizes this consumer's adoption — the row is gone, or names a
// binding the manifest no longer carries — so the adoption seam reading the
// pool right after the decline can never serve stale handles. Pooled handles
// stay: they hold the leases that keep their contended pins protected until
// the pool teardown releases them.
func (s *Session) dropRetainedScratchConsumerRow(sessionID string) {
	if pool := s.retainedScratch.Load(); pool != nil {
		pool.mu.Lock()
		delete(pool.consumers, sessionID)
		pool.mu.Unlock()
	}
}

// clearRetainedScratchConsumerRows empties the pool's consumer rows after the
// refresh read a released manifest. Every row the pool holds was derived from
// the pre-tombstone manifest and writers refuse a released manifest, so no
// later pass can revalidate them; clearing the rows makes each consumer's next
// adoption decline to fresh scratch instead of serving handles the tombstone
// no longer authorizes. A repaired manifest (a reset reinitializes it) lets
// the next refresh re-seed the rows it revalidates.
func (s *Session) clearRetainedScratchConsumerRows() {
	if pool := s.retainedScratch.Load(); pool != nil {
		pool.mu.Lock()
		pool.consumers = make(map[string]sandbox.ScratchConsumerBinding)
		pool.mu.Unlock()
	}
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

// scratchUpsertCommitted recognizes an upsert whose manifest rename committed
// before the write reported a post-rename failure: the rows this pass derived
// are already durable, binding for binding and role for role. A concurrent
// writer committing byte-identical rows lands in the desired state either way,
// so a content match is success, not a false positive.
func scratchUpsertCommitted(owner sandbox.ScratchOwner, binding sandbox.ScratchBinding, consumer sandbox.ScratchConsumerBinding) bool {
	fresh, err := sandbox.LoadScratchRetention(owner)
	if err != nil || fresh.Released {
		return false
	}
	got, ok := findScratchBinding(fresh, binding.BindingID)
	if !ok || !scratchBindingRowsCurrent(got, binding) {
		return false
	}
	gotConsumer, ok := findScratchConsumer(fresh, consumer.SessionID)
	return ok && scratchConsumerRowsCurrent(gotConsumer, consumer)
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
// A decline is not a leave-alone: when the manifest is released, or no longer
// contains the consumer or the binding its row names, the pool's rows are
// stale and the adoption reading the pool right after the decline would serve
// handles the durable state no longer authorizes. The decline clears them —
// every consumer row for a released manifest, this consumer's row for the
// live-manifest absences — so the restore falls to its fresh scratch.
func (s *Session) refreshRetainedScratchConsumer(sessionID string) error {
	owner, ok := s.scratchRetentionOwner()
	if !ok {
		return nil
	}
	lockRefusals := 0
	for range 5 {
		if s.retainedScratchSealed.Load() {
			// The terminal close is sealing the pool: nothing this pass
			// would converge outlives the session, so decline before
			// reacquiring anything.
			return nil
		}
		manifest, err := sandbox.LoadScratchRetention(owner)
		if err != nil {
			return fmt.Errorf("retained scratch refresh: %w", err)
		}
		if manifest.Released {
			s.clearRetainedScratchConsumerRows()
			return nil
		}
		consumer, ok := findScratchConsumer(manifest, sessionID)
		if !ok || consumer.CurrentBindingID == "" {
			s.dropRetainedScratchConsumerRow(sessionID)
			return nil
		}
		binding, ok := findScratchBinding(manifest, consumer.CurrentBindingID)
		if !ok {
			s.dropRetainedScratchConsumerRow(sessionID)
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
				if errors.Is(err, sandbox.ErrScratchRetentionReleased) {
					// The terminal release sealed the manifest between this
					// pass's snapshot and the open — the install seal path's
					// outcome, reached from the reacquire: the leases this
					// pass already took are handed back above, the refresh
					// declines, and the restore proceeds on fresh scratch a
					// later reset will reinitialize into (round 14). The
					// decline is not a leave-alone: a pool published before
					// this pass keeps serving rows the tombstone no longer
					// authorizes — the adoption seam reading the pool right
					// below would transfer a retained allocation over a
					// released manifest — so the published consumer rows
					// clear exactly like the pass-start decline (round 24,
					// round 28).
					s.clearRetainedScratchConsumerRows()
					return nil
				}
				return fmt.Errorf("retained scratch refresh %q: %w", ref.Dir, err)
			}
			handles[canonicalScratchDir(ref.Dir)] = handle
		}
		if lockContention {
			// A fail-fast lock refusal is transient — the holder is a mortal
			// in-process writer holding the manifest for fsync-scale work —
			// so the pass spaces its retry instead of burning the remaining
			// bound against one hold and failing the delegate's send.
			s.sleepScratchLockBackoff(lockRefusals)
			lockRefusals++
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
				// The fold landed the current rows; the sweep against this
				// same manifest drops the debris an earlier world left —
				// handles, marks, claims, and rows for allocations the
				// manifest no longer references (round 53).
				s.reconcileRetainedScratchPool(pool, fresh)
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
					if hook := s.cfg.testOnly.scratchRefreshAfterSeedCAS; hook != nil {
						hook()
					}
					if s.retainedScratchSealed.Load() {
						// The terminal close sealed the pool between this
						// pass's reacquire and its publish. Undo the publish —
						// compare against this pass's own seed so a concurrent
						// pool is never displaced — and let the sentinel hand
						// the leases back: a pool seeded after the terminal
						// detach would be unreachable (every consumer is
						// already torn down) and its handles would hold their
						// pins against the collector for the daemon's life.
						if !s.retainedScratch.CompareAndSwap(seeded, nil) {
							// The terminal detach already swept this pass's
							// published pool and Retained its handles — the
							// published map aliases this pass's own. Only
							// the sweep's winner owns the leases: releasing
							// them here too would race the detach's Retain
							// on the same unsynchronized objects.
							handles = nil
						}
						return errScratchRefreshSealed
					}
					return nil
				}
				published := s.retainedScratch.Load()
				if published == nil {
					// The pointer went back to nil — an init cleanup or a
					// retirement detach swept the published pool — so retry
					// the CAS with this pass's seed. A terminal sweep never
					// republishes: the seal check on the next successful CAS
					// declines it instead.
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
		case errors.Is(installErr, errScratchRefreshSealed):
			// The terminal close owns the manifest now; the restore proceeds
			// on fresh scratch and nothing durable this pass holds outlives
			// the session. Hand the leases back and decline.
			releaseRefreshHandles(handles)
			return nil
		case errors.Is(installErr, errScratchRefreshStaleRevision),
			errors.Is(installErr, errScratchRefreshPoolDetached),
			errors.Is(installErr, sandbox.ErrScratchRetentionLockHeld):
			// This pass's reacquired leases are nobody's now — hand them back
			// before the next pass re-derives its own against the moved
			// manifest or the current pool.
			releaseRefreshHandles(handles)
			if errors.Is(installErr, sandbox.ErrScratchRetentionLockHeld) {
				s.sleepScratchLockBackoff(lockRefusals)
				lockRefusals++
			}
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
	// errScratchRefreshSealed marks a pass whose session sealed its pool for
	// terminal release before the pass's seed publish, so the pass undid the
	// publish and declines instead of leaving an unreachable pool behind.
	errScratchRefreshSealed = errors.New("agent: retained scratch pool sealed for terminal release")
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

// sleepScratchLockBackoff spaces one lock-contention retry the way
// sandbox.RetryScratchLockContention spaces its attempts — the same growing
// schedule, one source — so the agent layer's re-derive loops wait out a
// fsync-scale manifest hold instead of burning their retry bound against it.
// The testOnly hook replaces the wall-clock sleep so tests sequence
// deterministically against the schedule.
func (s *Session) sleepScratchLockBackoff(attempt int) {
	if hook := s.cfg.testOnly.scratchLockBackoff; hook != nil {
		hook(attempt)
		return
	}
	time.Sleep(sandbox.ScratchLockContentionDelay(attempt))
}

// reconcileRetainedScratchPool sweeps the pool against the manifest the fold
// just landed under, releasing and removing every entry whose allocation the
// live manifest no longer references: the debris a reset or a slot move
// leaves behind — pooled handles still holding their leases, contention
// marks, adoption claims, and binding rows no current row can reach. The
// sweep runs inside the manifest's durable update lock, the same hold the
// fold lands in, so no writer can re-reference a directory between the
// manifest this pass read and the entries it drops. An orphaned handle
// hands its lease back: the pin, not the pool's lease, is what protects a
// directory the collector must skip, so the release never exposes a pinned
// directory — it only unblocks collection of the unpinned garbage. Consumer
// rows stay with the decline machinery that already reconciles them
// per-consumer (round 24).
func (s *Session) reconcileRetainedScratchPool(pool *retainedScratchPool, manifest sandbox.ScratchManifest) {
	referenced := make(map[string]struct{}, len(manifest.References))
	for _, ref := range manifest.References {
		referenced[canonicalScratchDir(ref.Dir)] = struct{}{}
	}
	liveBindings := make(map[string]struct{}, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		liveBindings[binding.BindingID] = struct{}{}
	}
	pool.mu.Lock()
	// The fold verified this pool is still the published one under its own
	// mutex hold; a release can detach it between the two holds, so the
	// sweep re-checks before touching anything — a detached pool's maps are
	// the release's to clear, and a second Retain on handles it already
	// released would race its own (round 15).
	if s.retainedScratch.Load() != pool {
		pool.mu.Unlock()
		return
	}
	var released []*sandbox.SessionScratch
	for key, handle := range pool.handles {
		if _, ok := referenced[key]; ok {
			continue
		}
		released = append(released, handle)
		delete(pool.handles, key)
	}
	for key := range pool.adopted {
		if _, ok := referenced[key]; !ok {
			delete(pool.adopted, key)
		}
	}
	for key := range pool.contended {
		if _, ok := referenced[key]; !ok {
			delete(pool.contended, key)
		}
	}
	for id := range pool.bindings {
		if _, ok := liveBindings[id]; !ok {
			delete(pool.bindings, id)
		}
	}
	pool.mu.Unlock()
	// The leases are handed back outside the mutex: the entries are already
	// gone from the maps, so no pool reader can reach these handles, and the
	// detach — which Retains what the maps still hold — cannot release them
	// a second time.
	for _, handle := range released {
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
			// A stale mark a pre-round-9 refresh could have written beside
			// the handle is inert — the guard requires no handle — but it
			// breaks the maps' disjoint shape, so drop it here.
			delete(pool.contended, key)
			continue
		}
		pool.contended[key] = struct{}{}
	}
	if s.retainedScratch.Load() != pool {
		// Belt beyond the serialized detach (detachRetainedScratch): an
		// unsynchronized swap of the published pointer cannot interleave
		// with this fold's check-then-install anymore, but any swap path
		// that bypasses the serializer still lands here. Roll this pass's
		// handles back out so the caller keeps ownership of exactly what it
		// reacquired, decline the fold, and let the pass retry against the
		// current pointer — never report a successful install into a pool
		// that was already dead.
		for key, handle := range handles {
			if pool.handles[key] == handle {
				delete(pool.handles, key)
			}
		}
		return false
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
		var handle *sandbox.SessionScratch
		openErr := sandbox.RetryScratchLockContention(func() error {
			var err error
			handle, err = sandbox.OpenRetainedSessionScratch(owner, ref)
			return err
		})
		if openErr != nil {
			if errors.Is(openErr, sandbox.ErrScratchRetentionLeaseHeld) {
				// Already owned in this process (a live or crash-abandoned
				// runtime holds the lease). Leave it with its owner instead of
				// contending; a real crash releases the lease before restore.
				if dir, absErr := filepath.Abs(ref.Dir); absErr == nil {
					pool.contended[canonicalScratchDir(dir)] = struct{}{}
				}
				continue
			}
			if errors.Is(openErr, sandbox.ErrScratchRetentionReleased) {
				// A terminal release tombstoned the manifest between this
				// preparation's load and the open's in-lock revalidation. The
				// retained restore is declined, not failed: release every
				// handle acquired so far and publish nothing, exactly the
				// already-released short-circuit at the head of this function
				// — the install path's reset resurrects and carries what the
				// release left re-probeable, and the restore proceeds on
				// fresh scratch for the rest (round 50).
				releaseRetainedScratchPool(pool)
				return nil
			}
			releaseRetainedScratchPool(pool)
			return fmt.Errorf("retained scratch %q: %w", ref.Dir, openErr)
		}
		pool.handles[canonicalScratchDir(ref.Dir)] = handle
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
// duplicating lease ownership, and verifies the binding identity. It reports
// whether a live pool was there to transfer from and whether a retained
// allocation actually transferred: a no-op on a detached pool returns
// installed=false, so a caller whose disposal already ran cannot mistake the
// no-op for a transferred allocation (round 18), and a lease-less borrow — a
// distinct consumer sharing an allocation another session already adopted —
// installs without transferring, so no caller can mistake the shared
// directory for one this session owns (round 22). The same consumer asking
// twice is refused. transferred reports the transfer PER KIND: a binding's
// slots are claimed one at a time, and one kind's contended skip must not be
// masked by another kind's successful transfer (round 26).
func (s *Session) adoptRetainedScratchFor(env *execenv.LocalExecutionEnvironment, bindingID, adopterID string) (bool, map[string]bool, error) {
	if env == nil {
		return false, nil, nil
	}
	if hook := s.cfg.testOnly.scratchAdoptionBeforeTransfer; hook != nil {
		hook()
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		return false, nil, nil
	}
	pool.mu.Lock()
	binding, ok := pool.bindings[bindingID]
	pool.mu.Unlock()
	if !ok {
		return false, nil, fmt.Errorf("retained scratch: binding %q is not in the manifest", bindingID)
	}
	if err := env.SetScratchRetentionBinding(pool.owner, binding); err != nil {
		return false, nil, err
	}
	// A kind this environment already provisions (an eagerly provisioned
	// sandbox scratch) is left exposed; only absent kinds are restored, so a
	// retained wrapper never replaces a live allocation.
	existingKinds := map[string]bool{}
	markedPending := false
	transferred := map[string]bool{}
	if refs, err := env.ScratchRetentionReferences(); err == nil {
		for _, ref := range refs {
			existingKinds[ref.Kind] = true
		}
	}
	for kind, slot := range binding.Slots {
		if existingKinds[kind] {
			// A live allocation this environment already provisions is left
			// exposed — normally the resume-time fresh mint of a cold
			// restore. When the slot it shadows is an owning slot whose lease
			// is contended elsewhere in this process, that mint is a fallback
			// for exactly one cycle: the binding row must keep naming the
			// retained directory or the mint's next publication would
			// displace the slot and no later refresh would ever re-probe the
			// original (round 10). Mark the kind pending so the mint pins as
			// a bare protected reference instead — but only when the live
			// allocation is genuinely a fallback: an environment already
			// running ON the retained directory reads as contended to the
			// re-probe (it holds that lease itself), and marking there would
			// defer every later publication off the allocation it already
			// owns.
			if slot.OwnsLease && pool.scratchSlotContended(canonicalScratchDir(slot.Dir)) &&
				canonicalScratchDir(envScratchRefDir(env, kind)) != canonicalScratchDir(slot.Dir) {
				env.MarkRetainedSlotPending(kind)
				markedPending = true
			}
			continue
		}
		key := canonicalScratchDir(slot.Dir)
		if !slot.OwnsLease {
			// A wrapper-only slot points at a retained directory whose lease
			// another binding owns. It never takes a second lease, so the borrow
			// must happen whether the owning handle was reacquired (present), is
			// held elsewhere in this process (contended), or its owner binding
			// has not been adopted yet. But the binding was snapshotted above
			// and this session's own terminal release can seal, detach, and
			// tombstone the allocation in between — the guarded borrow
			// revalidates and serializes against exactly that (round 30).
			if hook := s.cfg.testOnly.scratchAdoptionBeforeBorrow; hook != nil {
				hook()
			}
			pool.mu.Lock()
			takenBy := pool.adopted[key]
			pool.mu.Unlock()
			if takenBy == adopterID {
				return false, nil, fmt.Errorf("retained scratch: binding %q slot %q was already transferred", bindingID, kind)
			}
			installed, err := s.borrowRetainedScratchIfLive(pool, env, bindingID, kind, slot)
			if err != nil {
				return false, nil, err
			}
			if !installed {
				// The borrow declined. A pool that sealed or detached
				// mid-window is dying — the seal and the detach are separate
				// pool-lock acquisitions in every release path, so the borrow
				// can interleave between them (rounds 18 and 33) — and a
				// directory the revalidation read collectible is gone for
				// this cycle the same way. Every decline reports the whole
				// adoption not-installed so the caller reprovisions fresh
				// scratch instead of proceeding on the pre-seal snapshot with
				// the allocation missing, and the declined kind must carry
				// the pending mark first: the binding row is already installed
				// with its slot naming the retained directory, and the
				// reprovision's first mint publishes through the pending path
				// — a bare protected reference — or its publication would
				// claim the binding's slot and end the re-probe exactly like a
				// contended slot's fallback (rounds 10, 37, and 38).
				env.MarkRetainedSlotPending(kind)
				return false, nil, nil
			}
			continue
		}
		handle, prior, already, contended, dying := pool.claimRetainedScratchSlot(s, key, adopterID)
		if hook := s.cfg.testOnly.scratchClaimResolved; hook != nil {
			hook()
		}
		switch {
		case dying:
			// The release sealed this pool or unpublished it between the
			// snapshot and the claim. The whole adoption reports
			// not-installed so the caller reprovisions fresh scratch
			// instead of proceeding on the pre-seal snapshot (rounds 18
			// and 33), and the handle stays in the releasable map so the
			// release path Retains what the adopter never took. The
			// binding row is already installed with its slot naming the
			// retained directory, so the declined kind must also be marked
			// pending: the reprovision's first mint publishes through the
			// pending path — a bare protected reference — or its
			// publication would claim the binding's slot and end the
			// re-probe exactly like a contended slot's fallback (rounds 10
			// and 37).
			env.MarkRetainedSlotPending(kind)
			return false, nil, nil
		case already && prior == adopterID:
			if contended {
				// The refresh's stale-claim probe proved this claim's lease
				// held elsewhere in this process — the idle-release teardown
				// racing the restore — and left the claim in place, since it
				// is also what lets a distinct consumer borrow the directory.
				// This environment provisions no allocation of the kind, so
				// the restore runs on fresh scratch and the row must keep
				// naming the retained directory for the next refresh to
				// re-probe: mark the kind pending, exactly like the
				// uncontended-claim path. The contention verdict is the
				// claim's own snapshot — a second lookup after the claim
				// would race a concurrent refresh fold flipping the mark
				// between the two holds (round 16).
				env.MarkRetainedSlotPending(kind)
				markedPending = true
				continue
			}
			return false, nil, fmt.Errorf("retained scratch: binding %q slot %q was already transferred", bindingID, kind)
		case already:
			// A distinct consumer sharing the allocation borrows the same
			// directory without doubling the lease its adopter holds — but
			// only while the allocation is still live: the claim's snapshot
			// is one pool-lock hold earlier, and the terminal release can
			// seal, detach, and tombstone in between (round 30).
			installed, err := s.borrowRetainedScratchIfLive(pool, env, bindingID, kind, slot)
			if err != nil {
				return false, nil, err
			}
			if !installed {
				// The distinct consumer's borrow declines for the same reasons
				// a wrapper-only borrow can — the pool sealed or detached
				// mid-window (rounds 32 and 33), or the revalidation read the
				// directory collectible — and takes the same
				// declined-adoption treatment: the pending mark keeps the
				// reprovision's mint from claiming the binding row's slot
				// (rounds 10, 37, and 38), and the not-installed report routes
				// the caller to fresh scratch.
				env.MarkRetainedSlotPending(kind)
				return false, nil, nil
			}
		case handle != nil:
			if hook := s.cfg.testOnly.scratchAdoptionAfterClaim; hook != nil {
				hook()
			}
			ref := sandbox.ScratchReference{Dir: slot.Dir, Kind: kind}
			if err := env.RestoreSessionScratch(bindingID, ref, handle); err != nil {
				// The transfer failed, so the pool must not keep the slot
				// claimed: hand the handle back and leave it pooled for a
				// later, successful adoption. A detached pool takes nothing
				// back — the detach had no claim on it — so its release is
				// the restore's own.
				if !pool.releaseScratchSlotClaim(key, adopterID, handle) {
					_ = handle.Retain()
				}
				return false, nil, err
			}
			pool.finishRetainedScratchSlot(key)
			transferred[kind] = true
		case !contended:
			return false, nil, fmt.Errorf("retained scratch: binding %q slot %q has no reacquired handle", bindingID, kind)
		case contended:
			// The slot's lease is held elsewhere in this process — typically
			// the idle-release teardown racing this restore — so adoption
			// skips the transfer for this cycle. The binding row on the
			// manifest must keep naming this directory or the next fresh
			// mint's pin would replace the slot and no later refresh would
			// ever re-probe the original (round 10): mark the kind pending so
			// the mint pins as a bare protected reference instead.
			env.MarkRetainedSlotPending(kind)
			markedPending = true
		}
	}
	if markedPending {
		// A contended slot's fallback mint is protected the moment its kind is
		// marked pending: the mint hook ran before the binding install, so
		// nothing else pins it until the next publication — which a restored
		// session may never reach before a crash or an idle release, leaving
		// the fallback it works in collectible. Pin the owned handles now; the
		// pending kind pins as a bare protected reference (round 19).
		if err := env.PinOwnedScratch(); err != nil {
			return false, nil, err
		}
	}
	return true, transferred, nil
}

// borrowRetainedScratch installs a lease-less borrow of one retained directory
// on env without taking a second lease. The guarded caller holds the pool lock
// across it; the helper itself takes none.
func borrowRetainedScratch(env *execenv.LocalExecutionEnvironment, bindingID, kind string, slot sandbox.ScratchSlot) error {
	borrow, err := sandbox.BorrowRetainedSessionScratch(slot.Dir)
	if err != nil {
		return err
	}
	ref := sandbox.ScratchReference{Dir: slot.Dir, Kind: kind}
	return env.RestoreSessionScratch(bindingID, ref, borrow)
}

// borrowRetainedScratchIfLive installs a lease-less borrow of one retained
// directory, declining unless the allocation is still live. The binding was
// snapshotted under the pool lock earlier in the adoption, and this session's
// own terminal release can seal, detach, and tombstone it in between — while
// the bare borrow checks nothing but the directory's existence. The disk
// revalidation asks what the collector would see: a Released tombstone or a
// removed pin leaves the directory collectible, and a collectible directory
// is not one a restored environment may run on. The install runs under the
// pool lock, and the terminal release stores its seal under the same lock,
// so the release either sealed first (declined) or starts after this install
// completes (round 30).
func (s *Session) borrowRetainedScratchIfLive(pool *retainedScratchPool, env *execenv.LocalExecutionEnvironment, bindingID, kind string, slot sandbox.ScratchSlot) (bool, error) {
	retained, retainedErr := sandbox.ScratchDirectoryRetained(slot.Dir)
	if retainedErr != nil {
		// Unreadable retention state is the r23 abort class, not a decline:
		// fail the adoption loudly and retryably rather than silently
		// skipping an allocation that may still be live.
		return false, retainedErr
	}
	if !retained {
		return false, nil
	}
	pool.mu.Lock()
	sealed := s.retainedScratchSealed.Load()
	live := s.retainedScratch.Load() == pool
	var borrowErr error
	if !sealed && live {
		borrowErr = borrowRetainedScratch(env, bindingID, kind, slot)
	}
	pool.mu.Unlock()
	// The bool reports whether the borrow INSTALLED, not whether the disk
	// reads retained: a sealed or detached pool declines with the install
	// silently skipped, and reporting that as success left the adoption
	// claiming a shared allocation the environment never received (round
	// 32).
	return !sealed && live, borrowErr
}

// scratchSlotContended reports whether slot key's lease was held elsewhere in
// this process at refresh time, the record a fallback mint's publication must
// respect to keep the binding row naming the retained directory.
func (p *retainedScratchPool) scratchSlotContended(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, contended := p.contended[key]
	return contended
}

// scratchSlotClaimedBy reports whether the pool's claim record names adopterID
// as the consumer that took slot key's lease. finishRetainedScratchSlot keeps
// the adopted entry after the transfer precisely so a later settle can tell
// this consumer's committed transfer from an allocation somebody else holds.
func (p *retainedScratchPool) scratchSlotClaimedBy(key, adopterID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.adopted[key] == adopterID
}

// claimRetainedScratchSlot resolves one owning slot under the pool lock. A
// reacquired handle is returned only after the transfer is claimed for
// adopterID, so no concurrent adopter can take the same lease; already reports
// that the slot was claimed before this call, prior names by whom, and
// contended means its lease is held in this process with no reacquired handle.
// dying reports that the owning session's release already sealed this pool or
// unpublished it: the claim refuses the transfer and leaves the handle in the
// releasable map, where the release path Retains it — a claimed handle is
// invisible to the detach's release loop, and the environment would otherwise
// be left holding a lease on scratch the release is settling (round 34). The
// seal and the detach both run under this same lock, so the verification is
// atomic against the release.
func (p *retainedScratchPool) claimRetainedScratchSlot(s *Session, key, adopterID string) (handle *sandbox.SessionScratch, prior string, already, contended, dying bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.retainedScratchSealed.Load() || s.retainedScratch.Load() != p {
		return nil, "", false, false, true
	}
	if prior, already = p.adopted[key]; already {
		// The contention snapshot rides this hold: a lookup after the claim
		// would race a concurrent refresh fold flipping the mark between the
		// two holds and misclassify the own-claim (round 16).
		_, contended = p.contended[key]
		return nil, prior, true, contended, false
	}
	if handle = p.handles[key]; handle != nil {
		// Claim the transfer before the environment restore so a concurrent
		// adopter borrows instead of doubling the lease, and TAKE the handle
		// out of the releasable map: from the claim until the transfer
		// settles, release responsibility is the adopter's alone — a detach
		// must not Retain a lease out from under a claimed transfer (round
		// 15).
		delete(p.handles, key)
		p.adopted[key] = adopterID
		return handle, adopterID, false, false, false
	}
	_, contended = p.contended[key]
	return nil, "", false, contended, false
}

// finishRetainedScratchSlot commits a claimed transfer: the handle leaves the
// pool and its adopted entry stays as the record of who took the lease.
func (p *retainedScratchPool) finishRetainedScratchSlot(key string) {
	p.mu.Lock()
	delete(p.handles, key)
	p.mu.Unlock()
}

// releaseScratchSlotClaim undoes an uncommitted claim after a failed transfer,
// handing the claimed handle back to the pool's releasable map. It reports
// false when the claim is no longer this adopter's — a concurrent detach swept
// the pool — and then the pool did not take the handle back: the caller owns
// releasing it.
func (p *retainedScratchPool) releaseScratchSlotClaim(key, adopterID string, handle *sandbox.SessionScratch) bool {
	p.mu.Lock()
	ours := p.adopted[key] == adopterID
	if ours {
		delete(p.adopted, key)
		if handle != nil {
			p.handles[key] = handle
		}
	}
	p.mu.Unlock()
	return ours
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
// no-op when no pool exists or the consumer has no current binding. transferred
// reports the transfer per scratch kind (round 26).
func (s *Session) adoptConsumerScratch(env *execenv.LocalExecutionEnvironment, sessionID string) (bool, map[string]bool, error) {
	if env == nil {
		return false, nil, nil
	}
	if hook := s.cfg.testOnly.scratchAdoptionBeforeClaim; hook != nil {
		hook()
	}
	pool := s.retainedScratch.Load()
	if pool == nil {
		return false, nil, nil
	}
	pool.mu.Lock()
	consumer, ok := pool.consumers[sessionID]
	pool.mu.Unlock()
	if !ok || consumer.CurrentBindingID == "" {
		return false, nil, nil
	}
	installed, transferred, err := s.adoptRetainedScratchFor(env, consumer.CurrentBindingID, sessionID)
	if err != nil {
		return false, nil, err
	}
	return installed, transferred, nil
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
// scratch is still intact. A failure after the disposal — or an adoption that
// claims nothing because the pool detached between the slot read and the
// claim — re-provisions the environment's own scratch rather than leaving the
// resumed root running on a directory it owns no lease on.
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
	dir, ok, contended := s.retainedConsumerScratchSlot(sessionID, sandbox.ScratchKindSandbox)
	if !ok || contended || canonicalScratchDir(dir) == canonicalScratchDir(env.SessionScratchDir()) {
		if _, _, err := s.adoptConsumerScratch(env, sessionID); err != nil {
			return err
		}
	} else {
		if err := s.rebuildSandboxWrapper(env, dir); err != nil {
			return err
		}
		env.DisposeSandboxScratch()
		if _, _, err := s.adoptConsumerScratch(env, sessionID); err != nil {
			return reprovisionAfterFailedAdoption(env, err)
		}
		// The heal keys on the transfer the environment actually owns, not on
		// the installed report: the guard's slot read and the claim take
		// separate pool.mu holds, and a refresh fold racing the two can flip
		// the slot to contended in between — the adoption then marks the kind
		// pending and reports installed with no handle transferred, and with
		// the fresh mint already disposed the resumed root would run on the
		// retained directory it holds no lease on. Ownership is the same fact
		// the detached-pool no-op lacks (round 17).
		owned := envScratchRefDir(env, sandbox.ScratchKindSandbox)
		if owned == "" {
			return reprovisionUnclaimedSandboxScratch(env)
		}
		// The claim reads the pool's CURRENT rows, and the same racing fold
		// can move the consumer's binding between the snapshot above and the
		// claim: the resumed root then owns the moved allocation while its
		// wrapper still names the pre-move snapshot. Converge the wrapper on
		// what the environment actually holds (round 51).
		if canonicalScratchDir(owned) != canonicalScratchDir(dir) {
			if err := s.rebuildSandboxWrapper(env, owned); err != nil {
				return err
			}
		}
	}
	if hook := s.cfg.testOnly.scratchBeforeUnsandboxedTail; hook != nil {
		hook()
	}
	unsandboxed, ok, unsandboxedContended := s.retainedConsumerScratchSlot(sessionID, sandbox.ScratchKindUnsandboxed)
	if unsandboxedContended {
		// The contended arm declines the adoption with the binding row still
		// naming the retained allocation in its unsandboxed slot, and the
		// contention can land here without any earlier pass seeing it — the
		// sandbox section's adoption leaves a kind the launcher already
		// provisions unmarked. Without the pending marker the launcher
		// environment's next pin rebases that slot onto its own fresh
		// directory, permanently displacing the retained allocation — the
		// unsandboxed flavor of the continuity loss the pending machinery
		// exists to prevent. The mark makes the publication pin the
		// launcher's scratch as a bare protected reference instead, and the
		// next restore re-probes the original. The pin must happen here: the
		// launcher's mint predates the binding identity's install, so no
		// post-mint pin ever covered it, and leaving it unpinned lets a
		// crash or an idle teardown take the one-cycle fallback the marker
		// exists to protect (round 59).
		env.MarkRetainedSlotPending(sandbox.ScratchKindUnsandboxed)
		if err := env.PinOwnedScratch(); err != nil {
			return err
		}
		return nil
	}
	if !ok || canonicalScratchDir(unsandboxed) == canonicalScratchDir(envScratchRefDir(env, sandbox.ScratchKindUnsandboxed)) {
		return nil
	}
	env.DisposeUnsandboxedScratch()
	installed, _, err := s.adoptConsumerScratch(env, sessionID)
	if err != nil || !installed {
		// The pool can detach — or the consumer row can die — between the slot
		// read and the tail's claim, and the adoption then installs nothing:
		// the launcher's unsandboxed mint is already disposed and the next
		// spawned command lazily mints another. Without a pending marker that
		// mint's publication claims the binding's unsandboxed slot with the
		// new directory, permanently displacing the retained allocation — the
		// unsandboxed flavor of the continuity loss the pending machinery
		// exists to prevent (round 19). The mark makes the next publication pin
		// the replacement as a bare protected reference instead, and the next
		// restore re-probes the original; the failed-adoption exit takes it
		// too, since a surviving launcher environment mints the same way.
		env.MarkRetainedSlotPending(sandbox.ScratchKindUnsandboxed)
	}
	if err != nil {
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

// reprovisionAfterFailedAdoption is the error exit of a dispose-then-adopt
// replacement. A failed adoption refuses the restore, and the failure path
// settles the environment by what the durable manifest names — a mid-failure
// mint would pin fresh durable state the next attempt's refusal semantics do
// not expect — so an environment that already owns a sandbox scratch keeps
// it, and only one that owns none gets a usable scratch re-provisioned. The
// rebuilt wrapper does not count: after the disposal it names the retained
// directory the failed adoption never transferred, and reading its report
// (SessionScratchDir) would skip the reprovision and leave every retry
// running lease-less on that directory. The ownership check reads the
// retained references instead — the same fact the no-op heal trusts, checked
// before anything clears the wrapper — and a wrapper over an unowned
// directory is dropped before the mint, exactly as the no-op heal drops it
// (round 41).
//
// The mint is itself a mid-failure publication: EnableSandbox's allocation
// pins through the environment's post-mint hook against the binding row the
// failed adoption installed, whose sandbox slot still names the retained
// directory. Without a pending marker that publication claims the slot for
// the mint and displaces the retained allocation in the durable manifest
// exactly when the restore is being refused — the next attempt would then
// adopt the mint's directory instead of refusing. Marking the kind pending
// keeps the mint's publication bare and the slot naming the retained
// directory (the round 21 lazy-mint shape, same as the dying-claim mark of
// round 38).
func reprovisionAfterFailedAdoption(env *execenv.LocalExecutionEnvironment, cause error) error {
	if env == nil || env.Sandbox == nil {
		return cause
	}
	if envScratchRefDir(env, sandbox.ScratchKindSandbox) != "" {
		return cause
	}
	// The installed row's sandbox slot names a directory this environment
	// does not own (the guard above passed), so the mint's publication must
	// not claim it.
	if binding, err := env.ScratchRetentionBinding(); err == nil {
		if slot, ok := binding.Slots[sandbox.ScratchKindSandbox]; ok && slot.OwnsLease {
			env.MarkRetainedSlotPending(sandbox.ScratchKindSandbox)
		}
	}
	env.Wrapper = nil
	if err := env.EnableSandbox(env.Sandbox); err != nil {
		return errors.Join(cause, fmt.Errorf("re-provision sandbox scratch after a failed retained-scratch adoption: %w", err))
	}
	return cause
}

// reprovisionUnclaimedSandboxScratch heals the silent no-op adoption after a
// disposal: with no error, the restore PROCEEDS with this environment, so it
// must own the scratch its wrapper names. The stale wrapper built around the
// retained directory is dropped first — it is not ownership, and the
// ownership check below would otherwise see nothing to fix — then the
// environment's own policy scratch is minted, wrapper and all, unless it
// already owns a sandbox scratch a partial adoption installed.
func reprovisionUnclaimedSandboxScratch(env *execenv.LocalExecutionEnvironment) error {
	if env == nil || env.Sandbox == nil {
		return nil
	}
	// A wrapper naming the retained directory is not ownership: when the
	// adoption installed nothing — the pool detached between the slot read
	// and the claim — that wrapper points at a directory this session holds
	// no lease on, and running there would straddle the live in-process
	// holder (round 17).
	// The ownership check runs before anything clears the wrapper: an
	// environment that already holds a sandbox scratch is preserved by the
	// early return below, and clearing its wrapper first would leave a live
	// scratch with no kernel sandbox to run commands through (round 23).
	if refs, refsErr := env.ScratchRetentionReferences(); refsErr == nil {
		for _, ref := range refs {
			if ref.Kind == sandbox.ScratchKindSandbox {
				return nil
			}
		}
	}
	env.Wrapper = nil
	if err := env.EnableSandbox(env.Sandbox); err != nil {
		return fmt.Errorf("re-provision sandbox scratch after an unclaimed retained-scratch adoption: %w", err)
	}
	return nil
}

// retainedConsumerScratchSlot snapshots sessionID's lease-owning slot
// directory for kind together with its contention status under ONE
// pool-mutex hold, so the dispose-then-adopt decisions read a coherent row:
// a refresh republishing rows between a separate directory lookup and a
// separate contention check could otherwise pair the new row with the old
// contention verdict. Contended means the slot's lease is held elsewhere in
// this process — the racing idle-release teardown — with no reacquired handle
// in the pool: a slot the pool holds a handle for is NEVER contended, whatever
// a stale contention mark beside it says, because the handle is the
// transferable reacquire the marker claims is missing. A dispose-then-adopt
// replacement must never run against a genuinely contended slot — the
// adoption cannot take the lease, and with the fresh allocation already
// disposed the session would run on the retained directory unowned beside its
// in-process holder. The caller skips the replacement instead, keeps the
// fresh scratch, and the next refresh re-probes the settled contention and
// resumes in the retained directory with the lease in hand.
func (s *Session) retainedConsumerScratchSlot(sessionID, kind string) (dir string, ok, contended bool) {
	pool := s.retainedScratch.Load()
	if pool == nil {
		return "", false, false
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	consumer, hasConsumer := pool.consumers[sessionID]
	if !hasConsumer || consumer.CurrentBindingID == "" {
		return "", false, false
	}
	binding, hasBinding := pool.bindings[consumer.CurrentBindingID]
	if !hasBinding {
		return "", false, false
	}
	slot, hasSlot := binding.Slots[kind]
	if !hasSlot || !slot.OwnsLease {
		return "", false, false
	}
	key := canonicalScratchDir(slot.Dir)
	if _, held := pool.handles[key]; held {
		return slot.Dir, true, false
	}
	_, marked := pool.contended[key]
	return slot.Dir, true, marked
}

// settleFailedRestoreScratch settles the per-session scratch a failed delegate
// restore left on the environment it restored onto — one the restore created,
// or the live parent's it merely shared — so its teardown never removes a
// directory the root's durable retention manifest still references, and never
// takes a live environment's temp container with it.
// A binding's slots are transferred one at a time and binding.Slots has no
// order, so an adoption can commit an earlier slot and then fail on a later one,
// leaving the environment holding an allocation the pool has already handed
// over; the adoption's own recovery can also re-provision and pin a fresh mint
// before it returns the error. DisposeUnadoptedScratch would then remove a
// referenced directory while its reference and binding slot stay, and
// references are append-only with no unpin API, so the root's retirement
// preparation would refuse forever and a cold resume would fail the same way.
//
// Every referenced allocation the environment holds is therefore kept on disk.
// A slot this restore transferred is handed back and requeued in the pool with
// its claim cleared, so a later adoption reacquires it rather than finding a
// transfer with no handle behind it; any other referenced allocation is
// released (its lease given up, its directory kept, as any handoff does). Both
// handoffs work without a pool — the requeue is skipped and the release stands
// — so a pool detached between the adoption and this settle still keeps the
// transferred allocation alive (round 25). A shared environment is not this
// restore's to strip (round 65): the pool's claim record is the only
// attribution the settle has, so only a slot the pool recorded this restore
// claiming is detached and requeued, and every other referenced holding stays
// attached with its lease for the live parent's close to release. adopterID is
// the consumer the failed adoption ran for. Only what the manifest does not
// reference — the
// environment's own fresh mint — is disposed, and the disposal respects
// ownership: an environment this restore created dies with the failure, dirs
// and world-usable temp container both, while a shared one belongs to the live
// parent and is never this restore's to dispose at all — its container by
// removeUnsandboxedTmpLocked's rule, and its scratch because the caller's
// empty-snapshot record cannot attribute what stands there now: the
// environment holds one scratch per kind, the lazy mint reuses what is
// present, and the first minter in the window — this restore's construction
// or another parent/child's command — leaves nothing on the environment to
// tell them apart. What the shared environment holds it owns: the live parent
// reuses it on its next command, and its close releases the lease for the
// sweeper to collect. createdEnv is the caller's ownsFresh.
func (s *Session) settleFailedRestoreScratch(env execenv.ExecutionEnvironment, adopterID string, createdEnv bool) {
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
	// A pool changes nothing about the classification. A partial adoption can
	// commit an earlier slot and fail on a later one, leaving the environment
	// holding a manifest-referenced allocation the pool already handed over —
	// and the pool can detach before this settle runs, so treating a poolless
	// created environment as pure fresh mint deleted that transferred directory
	// out from under the manifest (round 25). The classification below is
	// poolless by construction (retainedScratchReferenceDirs reads the
	// manifest directly and the requeue guard is skipped with no pool), so every
	// environment flavor keeps exactly what the manifest references: a created
	// environment's transferred allocations survive with their leases released
	// for a later restore to reacquire, its unreferenced mint dies with the
	// failure, and a shared parent keeps its world-usable temp container. The
	// strip below is where the flavors part ways: only a slot the pool recorded
	// this restore claiming may leave a shared parent, because the claim record
	// is the one attribution the settle has (round 65).
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
		// A shared parent is not this restore's to strip (round 65). The
		// pool's claim record is the only attribution the settle has: the
		// adopted entry survives the transfer as the record of who took the
		// lease, so a slot the pool recorded this restore claiming — the
		// committed transfer the failed adoption installed — is this
		// restore's to detach and requeue. Everything else the environment
		// holds was there before the restore or minted in the window by a
		// concurrent actor, as unattributable as the round-51 mint: it stays
		// attached, lease and all, and the live parent's close releases for
		// the sweeper.
		if !createdEnv && (pool == nil || !pool.scratchSlotClaimedBy(key, adopterID)) {
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
	if createdEnv {
		local.DisposeUnadoptedScratch()
		return
	}
	// A shared parent is never this restore's to dispose: the mintedScratch
	// gate that reached here recorded only that the environment held no
	// scratch at adoption time — an empty snapshot, not an attribution, and
	// the first allocation minted in the window may be another parent/child's
	// exactly as easily as this construction's. The dispose entrypoints'
	// own contracts forbid a shared parent for this reason; whatever the
	// environment holds, it owns, its next command reuses, and its close
	// releases for the sweeper to collect.
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
	// The slot dir may be spelled relatively (manifest rows store the
	// supplied spelling): the wrapper must carry the canonical absolute
	// path, or every command forked under it resolves a relative TMPDIR
	// against its own working directory instead of the retained scratch
	// (round 47).
	wrapper, err := sandbox.NewWrapper(*env.Sandbox, env.Sandbox.HostBinaryPath(), canonicalScratchDir(dir))
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

// sealRetainedScratch marks this session's retained-scratch pool sealed,
// storing the seal under the live pool's own lock: a wrapper borrow that
// holds the lock across its install either completes before the seal or
// declines inside its critical section, so a terminal release can no longer
// start mid-borrow and leave a restored environment on a directory the
// release is about to make collectible (round 30).
func (s *Session) sealRetainedScratch() {
	pool := s.retainedScratch.Load()
	if pool == nil {
		s.retainedScratchSealed.Store(true)
		return
	}
	pool.mu.Lock()
	s.retainedScratchSealed.Store(true)
	pool.mu.Unlock()
}

// detachRetainedScratch unpublishes and releases the retained-scratch pool.
// The detach itself — the compare-and-swap that unpublishes the pointer —
// runs under the pool mutex, so a concurrent refresh's fold, which installs
// and revalidates publication under the same mutex, can never land rows in a
// pool that was already dead: the fold either completes entirely inside the
// pool's published lifetime or declines and retries against the current
// pointer. Handles are still released outside the mutex so a concurrent
// adoption never observes a half-cleared map.
func (s *Session) detachRetainedScratch() {
	pool := s.retainedScratch.Load()
	if pool == nil {
		return
	}
	pool.mu.Lock()
	if !s.retainedScratch.CompareAndSwap(pool, nil) {
		pool.mu.Unlock()
		return
	}
	handles := pool.handles
	pool.handles = map[string]*sandbox.SessionScratch{}
	pool.adopted = map[string]string{}
	pool.mu.Unlock()
	for _, handle := range handles {
		if hook := s.cfg.testOnly.scratchDetachRetainHook; hook != nil {
			hook()
		}
		_ = handle.Retain()
	}
}
