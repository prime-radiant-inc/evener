package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// scratchRetentionDirName is the subdirectory, under a root's state dir,
	// holding one manifest and one lock per root session.
	scratchRetentionDirName = "scratch-retention"
	// scratchPinName is the per-directory identity pin. Its fields are
	// immutable: an allocation moving between environments changes only manifest
	// mappings, never this file.
	scratchPinName = ".evener-retained-session.json"
	// scratchRetentionVersion is the only manifest/pin layout this build reads.
	scratchRetentionVersion = 1
)

// Scratch allocation kinds. A binding has at most one current allocation per
// (BindingID, Kind).
const (
	ScratchKindSandbox     = "sandbox"
	ScratchKindUnsandboxed = "unsandboxed"
)

// ErrScratchRetentionStaleRevision is returned when an update's expected
// revision no longer matches the manifest. Callers that race a concurrent
// writer may reload and retry.
var ErrScratchRetentionStaleRevision = errors.New("sandbox: scratch retention revision is stale")

// ErrScratchRetentionLeaseHeld means a retained allocation's lease is already
// held (typically the live owner in this process). A caller restoring after a
// real crash releases the lease first; a held lease is left with its owner.
var ErrScratchRetentionLeaseHeld = errors.New("sandbox: retained scratch lease is already held")

// ErrScratchRetentionLockHeld is returned when the manifest's durable update
// lock is contended: the caller lost a race with a concurrent writer (or a
// reader's install hold) and must retry, exactly as two racing writers
// already do. The lock is deliberately fail-fast — callers never block on it.
var ErrScratchRetentionLockHeld = errors.New("sandbox: scratch retention manifest is locked by another writer")

// ErrScratchRetentionReleased is returned when a binding mutation — a pin, an
// upsert, a revision-checked update — targets a manifest whose terminal
// tombstone has already committed. A released manifest is closed for writes:
// a losing writer retrying through a lock-contention window must not be able
// to resurrect bindings or add references after the terminal release, which
// would leave live allocations pinned against collection on an authority that
// already authorized their collection. Unlike the lock sentinel this error is
// terminal — it must not be retried. ReleaseScratchRetention itself writes
// the tombstone directly and remains the only writer after the fact.
var ErrScratchRetentionReleased = errors.New("sandbox: scratch retention manifest is released")

// ScratchOwner identifies the root that owns a retention manifest. It is the
// only retention authority: every pin, reference and binding belongs to exactly
// one owner.
type ScratchOwner struct {
	StateDir      string `json:"state_dir"`
	RootSessionID string `json:"root_session_id"`
}

func (o ScratchOwner) validate() error {
	if strings.TrimSpace(o.StateDir) == "" {
		return errors.New("sandbox: scratch retention owner has no state directory")
	}
	if strings.TrimSpace(o.RootSessionID) == "" {
		return errors.New("sandbox: scratch retention owner has no root session id")
	}
	if strings.ContainsAny(o.RootSessionID, `/\`) || o.RootSessionID == "." || o.RootSessionID == ".." {
		return fmt.Errorf("sandbox: scratch retention owner id %q is not a safe file name", o.RootSessionID)
	}
	return nil
}

// ScratchReference is a canonical, original allocation directory. Kind is
// immutable; the path is never a relocation.
type ScratchReference struct {
	Dir  string `json:"dir"`
	Kind string `json:"kind"`
}

// ScratchSlot references an immutable allocation within one binding.
type ScratchSlot struct {
	Dir       string `json:"dir"`
	OwnsLease bool   `json:"owns_lease"`
}

// ScratchBinding is one persisted opaque logical environment. OwnerSessionID and
// WorkingDir describe this environment, not its owner's latest cwd, and are
// unchanged by sharing, cloning or moving an allocation.
type ScratchBinding struct {
	BindingID      string                 `json:"binding_id"`
	OwnerSessionID string                 `json:"owner_session_id"`
	WorkingDir     string                 `json:"working_dir"`
	Slots          map[string]ScratchSlot `json:"slots"`
}

// ScratchConsumerBinding maps one session to the binding roles it currently
// occupies.
type ScratchConsumerBinding struct {
	SessionID                string   `json:"session_id"`
	CurrentBindingID         string   `json:"current_binding_id"`
	ParentSharedBindingID    string   `json:"parent_shared_binding_id,omitempty"`
	WorktreeRestoreBindingID string   `json:"worktree_restore_binding_id,omitempty"`
	AbandonedBindingIDs      []string `json:"abandoned_binding_ids,omitempty"`
}

// ScratchManifest is the root-owned durable retention record. Revision
// serializes binding updates; it is not a second lifecycle version. Released is
// the terminal-close/deletion tombstone and is never set by retirement.
type ScratchManifest struct {
	Version    int                      `json:"version"`
	Revision   uint64                   `json:"revision"`
	Owner      ScratchOwner             `json:"owner"`
	References []ScratchReference       `json:"references"`
	Bindings   []ScratchBinding         `json:"bindings"`
	Consumers  []ScratchConsumerBinding `json:"consumers"`
	Released   bool                     `json:"released"`
}

type scratchDirectoryPin struct {
	Version int          `json:"version"`
	Owner   ScratchOwner `json:"owner"`
	Dir     string       `json:"dir"`
	Kind    string       `json:"kind"`
}

func scratchRetentionDir(owner ScratchOwner) string {
	return filepath.Join(owner.StateDir, scratchRetentionDirName)
}

func scratchManifestPath(owner ScratchOwner) string {
	return filepath.Join(scratchRetentionDir(owner), owner.RootSessionID+".json")
}

func scratchRetentionLockPath(owner ScratchOwner) string {
	return filepath.Join(scratchRetentionDir(owner), owner.RootSessionID+".lock")
}

// WithScratchRetentionLock runs fn holding the manifest's durable update lock
// — the same one PinScratchBinding, UpdateScratchBindings and the release
// path serialize on — so a reader can load, validate, and act with no
// manifest update committing in between. The retained-scratch refresh needs
// exactly this: its revision recheck and its row install must be one atomic
// step against every manifest writer.
func WithScratchRetentionLock(owner ScratchOwner, fn func() error) error {
	if err := owner.validate(); err != nil {
		return err
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	return fn()
}

// canonicalScratchPath normalizes an allocation or reference path without
// requiring it to exist (a failed/never-exposed allocation still has a name).
func canonicalScratchPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("sandbox: scratch path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve scratch path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// LoadScratchRetention reads a root's manifest. A missing manifest is not an
// error: it is an empty, unreleased record for that owner.
func LoadScratchRetention(owner ScratchOwner) (ScratchManifest, error) {
	if err := owner.validate(); err != nil {
		return ScratchManifest{}, err
	}
	return loadScratchRetention(owner)
}

func loadScratchRetention(owner ScratchOwner) (ScratchManifest, error) {
	raw, err := os.ReadFile(scratchManifestPath(owner))
	if err != nil {
		if os.IsNotExist(err) {
			return ScratchManifest{Version: scratchRetentionVersion, Owner: owner}, nil
		}
		return ScratchManifest{}, fmt.Errorf("sandbox: read scratch retention manifest: %w", err)
	}
	var manifest ScratchManifest
	if err := decodeStrictJSON(raw, &manifest); err != nil {
		return ScratchManifest{}, fmt.Errorf("sandbox: decode scratch retention manifest: %w", err)
	}
	if manifest.Version != scratchRetentionVersion {
		return ScratchManifest{}, fmt.Errorf("sandbox: unsupported scratch retention version %d", manifest.Version)
	}
	if manifest.Owner != owner {
		return ScratchManifest{}, fmt.Errorf("sandbox: scratch retention manifest owner %+v does not match %+v", manifest.Owner, owner)
	}
	return manifest, nil
}

// scratchManifestWriteProbe is a nil-in-production test seam fired after a
// manifest write's rename already committed: its error simulates the
// post-rename fsync failure class, where writeScratchRetention reports a
// failure for a transaction that is already durable and every caller must
// treat as committed.
var scratchManifestWriteProbe func() error

// SetScratchManifestWriteProbeForTesting installs the post-rename manifest
// write probe and returns its restore. Cross-package tests use it to
// simulate the post-rename fsync failure class for a caller that must treat
// the reported failure as committed; the probe is nil in production.
func SetScratchManifestWriteProbeForTesting(hook func() error) (restore func()) {
	old := scratchManifestWriteProbe
	scratchManifestWriteProbe = hook
	return func() { scratchManifestWriteProbe = old }
}

func writeScratchRetention(owner ScratchOwner, manifest ScratchManifest) error {
	manifest.Version = scratchRetentionVersion
	manifest.Owner = owner
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("sandbox: marshal scratch retention manifest: %w", err)
	}
	if err := atomicWritePrivateFile(scratchManifestPath(owner), raw); err != nil {
		return err
	}
	if hook := scratchManifestWriteProbe; hook != nil {
		return hook()
	}
	return nil
}

func acquireScratchRetentionLock(owner ScratchOwner) (scratchLease, error) {
	if err := os.MkdirAll(scratchRetentionDir(owner), 0o700); err != nil {
		return nil, fmt.Errorf("sandbox: create scratch retention dir: %w", err)
	}
	lease, contended, err := acquireScratchLease(scratchRetentionLockPath(owner))
	if err != nil {
		// The unix lease reports contention as a non-nil error alongside the
		// contended flag (flock's EWOULDBLOCK), so both branches must map
		// contention onto the same sentinel or callers cannot distinguish a
		// lost lock race from real corruption.
		if contended {
			return nil, ErrScratchRetentionLockHeld
		}
		return nil, fmt.Errorf("sandbox: acquire scratch retention lock: %w", err)
	}
	if contended {
		if lease != nil {
			_ = lease.Release()
		}
		return nil, ErrScratchRetentionLockHeld
	}
	return lease, nil
}

func readScratchDirectoryPin(dir string) (scratchDirectoryPin, error) {
	raw, err := os.ReadFile(filepath.Join(dir, scratchPinName))
	if err != nil {
		return scratchDirectoryPin{}, err
	}
	var pin scratchDirectoryPin
	if err := decodeStrictJSON(raw, &pin); err != nil {
		return scratchDirectoryPin{}, fmt.Errorf("sandbox: decode retention pin: %w", err)
	}
	if pin.Version != scratchRetentionVersion {
		return scratchDirectoryPin{}, fmt.Errorf("sandbox: unsupported retention pin version %d", pin.Version)
	}
	if err := pin.Owner.validate(); err != nil {
		return scratchDirectoryPin{}, err
	}
	return pin, nil
}

// writeScratchDirectoryPin writes the immutable directory identity pin first.
// An existing pin with a conflicting owner, directory or kind is refused rather
// than overwritten.
func writeScratchDirectoryPin(dir string, owner ScratchOwner, ref ScratchReference) error {
	canonical, err := canonicalScratchPath(dir)
	if err != nil {
		return err
	}
	existing, err := readScratchDirectoryPin(canonical)
	if err == nil {
		if existing.Owner != owner || filepath.Clean(existing.Dir) != canonical || existing.Kind != ref.Kind {
			return fmt.Errorf("sandbox: conflicting retention pin for %q", canonical)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("sandbox: read retention pin: %w", err)
	}
	pin := scratchDirectoryPin{Version: scratchRetentionVersion, Owner: owner, Dir: canonical, Kind: ref.Kind}
	raw, err := json.Marshal(pin)
	if err != nil {
		return fmt.Errorf("sandbox: marshal retention pin: %w", err)
	}
	return atomicWritePrivateFile(filepath.Join(canonical, scratchPinName), raw)
}

// Pin writes and synchronizes the directory pin first, then the root manifest
// reference, while the caller still owns the live scratch lease. It never waits
// for a live scratch lease: ownership is a precondition. A conflict or write
// failure leaves both live ownership and any preexisting pin/reference intact.
func (s *SessionScratch) Pin(owner ScratchOwner, ref ScratchReference) error {
	if s == nil || strings.TrimSpace(s.Dir) == "" {
		return errors.New("sandbox: pin requires a scratch directory")
	}
	if s.lease == nil {
		return errors.New("sandbox: pin requires a currently owned scratch lease")
	}
	if err := owner.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(ref.Kind) == "" {
		return errors.New("sandbox: pin requires a scratch kind")
	}
	dir, err := canonicalScratchPath(ref.Dir)
	if err != nil {
		return err
	}
	live, err := canonicalScratchPath(s.Dir)
	if err != nil {
		return err
	}
	if dir != live {
		return fmt.Errorf("sandbox: pin directory %q is not the owned scratch %q", dir, live)
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return err
	}
	// A terminally released manifest is closed for writers: a pin published
	// onto the tombstone would resurrect protection the collector is already
	// authorized to ignore (round 13).
	if manifest.Released {
		return ErrScratchRetentionReleased
	}
	canonicalRef := ScratchReference{Dir: dir, Kind: ref.Kind}
	for _, existing := range manifest.References {
		existingDir, err := canonicalScratchPath(existing.Dir)
		if err != nil {
			return err
		}
		if existingDir == dir {
			if existing.Kind != ref.Kind {
				return fmt.Errorf("sandbox: conflicting retention reference for %q", dir)
			}
			return writeScratchDirectoryPin(dir, owner, canonicalRef)
		}
	}
	if err := writeScratchDirectoryPin(dir, owner, canonicalRef); err != nil {
		return err
	}
	manifest.References = append(manifest.References, canonicalRef)
	manifest.Revision++
	if err := writeScratchRetention(owner, manifest); err != nil {
		// The pin must not outlive the reference that was supposed to publish
		// it: ReleaseScratchRetention only removes pins listed in
		// manifest.References, so an unreferenced pin would hold the directory
		// against collection forever. Roll it back before reporting the
		// failure. The original error is returned unwrapped when the rollback
		// succeeds; both causes are reported when it does not.
		if rollbackErr := rollbackUnpublishedScratchPin(owner, dir, canonicalRef.Kind); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

// PinScratchBinding pins every allocation in owned and publishes binding in ONE
// manifest transaction: the references that retain the allocations and the
// binding that owns them become durable together, so no reader can observe a
// reference whose owning binding is missing. Pinning each allocation through Pin
// and upserting the binding afterwards cannot make that promise — each Pin is
// its own committed transaction — and a failure between them left the earlier
// pins' references durable, retained, and owned by nobody (round 19).
// pendingKinds names owned kinds whose handle is pinned as a bare protected
// reference WITHOUT claiming the binding's slot for that kind: the slot keeps
// naming whatever it named, so a fallback mint cannot displace a retained
// allocation whose reacquire is still pending.
//
// Every allocation is validated before any durable write, each newly added
// reference's directory pin is made durable before the manifest commit (so a
// collector still cannot collect a directory whose reference is about to become
// durable), and the manifest is written once. A failure before the commit — an
// unusable handle, a conflicting reference, a binding the merge rejects, a pin
// write, the manifest write itself — rolls back the directory pins this call
// published — including a pin it created repairing a reference the manifest
// already listed — and leaves the manifest exactly as the call found it. A
// commit that
// reports an error after its rename landed is kept whole: references and binding
// are both durable, which is the same state a successful call leaves.
func PinScratchBinding(owner ScratchOwner, binding ScratchBinding, owned map[string]*SessionScratch, pendingKinds map[string]struct{}) error {
	if err := owner.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(binding.BindingID) == "" {
		return errors.New("sandbox: scratch binding has no id")
	}
	// Sorted so the durable order of the pins is deterministic rather than
	// dependent on map iteration.
	kinds := slices.Sorted(maps.Keys(owned))
	refs := make([]ScratchReference, 0, len(kinds))
	for _, kind := range kinds {
		handle := owned[kind]
		if handle == nil || strings.TrimSpace(handle.Dir) == "" {
			return errors.New("sandbox: pin requires a scratch directory")
		}
		if handle.lease == nil {
			return errors.New("sandbox: pin requires a currently owned scratch lease")
		}
		if strings.TrimSpace(kind) == "" {
			return errors.New("sandbox: pin requires a scratch kind")
		}
		dir, err := canonicalScratchPath(handle.Dir)
		if err != nil {
			return err
		}
		refs = append(refs, ScratchReference{Dir: dir, Kind: kind})
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return err
	}
	if manifest.Released {
		return ErrScratchRetentionReleased
	}
	// The pre-call revision is the rollback's commit discriminator: the
	// rollback runs under this lock, so a revision that moved proves the
	// manifest write's rename landed — the transaction is durable despite
	// the reported error (round 39).
	preRevision := manifest.Revision
	if binding.Slots == nil {
		binding.Slots = make(map[string]ScratchSlot, len(refs))
	}
	var added []ScratchReference
	var repaired []ScratchReference
	for _, ref := range refs {
		var listed bool
		for _, existing := range manifest.References {
			existingDir, err := canonicalScratchPath(existing.Dir)
			if err != nil {
				return rollbackPinScratchBindingPins(owner, added, repaired, preRevision, err)
			}
			if existingDir != ref.Dir {
				continue
			}
			if existing.Kind != ref.Kind {
				return rollbackPinScratchBindingPins(owner, added, repaired, preRevision, fmt.Errorf("sandbox: conflicting retention reference for %q", ref.Dir))
			}
			listed = true
			break
		}
		// A pin over a reference the manifest already lists is a repair of a
		// lost pin: the write below may create it, and a failed transaction
		// must also remove what it created there — the added list alone tracks
		// only newly published references (round 35). An unreadable pin fails
		// the write below with the same error and creates nothing, so only
		// absence marks a pin this call is about to create.
		_, pinErr := readScratchDirectoryPin(ref.Dir)
		if err := writeScratchDirectoryPin(ref.Dir, owner, ref); err != nil {
			return rollbackPinScratchBindingPins(owner, added, repaired, preRevision, err)
		}
		if !listed {
			added = append(added, ref)
		} else if os.IsNotExist(pinErr) {
			repaired = append(repaired, ref)
		}
		if _, pending := pendingKinds[ref.Kind]; !pending {
			binding.Slots[ref.Kind] = ScratchSlot{Dir: ref.Dir, OwnsLease: true}
		}
	}
	manifest.References = append(manifest.References, added...)
	if err := applyScratchBindingUpdate(&manifest, binding, nil); err != nil {
		return rollbackPinScratchBindingPins(owner, added, repaired, preRevision, err)
	}
	if err := writeScratchRetention(owner, manifest); err != nil {
		// writeScratchRetention can report an error after the rename committed;
		// the rollback re-reads the manifest and then leaves a published
		// reference's pin alone, so a committed transaction stays whole.
		return rollbackPinScratchBindingPins(owner, added, repaired, preRevision, err)
	}
	return nil
}

// rollbackPinScratchBindingPins removes the directory pins a failed
// PinScratchBinding call created, restoring the pre-call state so the
// allocations are collectible again: the newly published references' pins
// through the unpublished-pin rollback, and the repairs of pins over
// references the manifest already listed through the repaired-pin rollback
// (round 35). It is called with the manifest lock held. For a newly published
// reference it never removes a pin the manifest already holds —
// rollbackUnpublishedScratchPin re-reads the manifest first — so a publication
// that committed despite reporting an error keeps its protection. A repaired
// pin is gated on the pre-call revision for the same reason: a revision that
// moved proves the manifest write's rename landed, and a committed
// transaction keeps its repair (round 39). The original error is returned
// unwrapped when every rollback succeeds; every cause is reported when one
// does not.
func rollbackPinScratchBindingPins(owner ScratchOwner, added, repaired []ScratchReference, preRevision uint64, cause error) error {
	var failures []error
	for _, ref := range added {
		if err := rollbackUnpublishedScratchPin(owner, ref.Dir, ref.Kind); err != nil {
			failures = append(failures, err)
		}
	}
	for _, ref := range repaired {
		if err := rollbackRepairedScratchPin(owner, ref.Dir, ref.Kind, preRevision); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		return cause
	}
	return errors.Join(cause, errors.Join(failures...))
}

// rollbackRepairedScratchPin removes a pin this transaction created over a
// reference the manifest already listed — the repair of a lost pin whose
// transaction then failed BEFORE its commit. The durable manifest is re-read
// first: writeScratchRetention can report an error after the rename committed,
// and a committed transaction keeps its repair — the revision moved, the
// listing is part of the durable coherent state, and removing the pin would
// leave the committed reference unpinned and its directory collectible
// (round 39). Only a revision still at the pre-call value proves the
// transaction never committed, and only then does the pre-call state — no pin —
// get restored. A pin that is absent, unreadable, or no longer this owner's
// own pin for the directory and kind is left alone, mirroring
// rollbackUnpublishedScratchPin's doubt-handling.
func rollbackRepairedScratchPin(owner ScratchOwner, dir, kind string, preRevision uint64) error {
	current, err := loadScratchRetention(owner)
	if err != nil {
		return fmt.Errorf("sandbox: confirm rollback of repaired retention pin for %q: %w", dir, err)
	}
	if current.Revision != preRevision {
		// The transaction committed despite the reported error; the repair
		// stays with it.
		return nil
	}
	pin, pinErr := readScratchDirectoryPin(dir)
	switch {
	case pinErr == nil && pin.Owner == owner && filepath.Clean(pin.Dir) == dir && pin.Kind == kind:
		if removeErr := os.Remove(filepath.Join(dir, scratchPinName)); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("sandbox: roll back repaired retention pin for %q: %w", dir, removeErr)
		}
		return nil
	case os.IsNotExist(pinErr):
		// Already absent; nothing to undo.
		return nil
	case pinErr != nil:
		return fmt.Errorf("sandbox: repaired retention pin for %q is unreadable; left in place: %w", dir, pinErr)
	default:
		return fmt.Errorf("sandbox: repaired retention pin for %q does not identify this owner's pin for the directory and kind; left in place", dir)
	}
}

// rollbackUnpublishedScratchPin undoes the directory pin of a Pin call whose
// manifest publication failed, restoring the pre-Pin state so the allocation is
// collectible again. It is called with the manifest lock held.
//
// The durable manifest is re-read first. atomicWritePrivateFile fsyncs the
// containing directory last, so writeScratchRetention can report an error after
// the manifest rename already committed; the pin is then the only protection of
// a directory the committed manifest does reference, and removing it would be
// exactly the unsafe trade — a directory collectible while the manifest still
// claims it — that the pin-before-reference ordering exists to prevent. A
// published reference therefore leaves the pin alone.
//
// A pin is removed only when it is this owner's own pin for dir and kind,
// mirroring ReleaseScratchRetention: a pin that was replaced by another owner is
// left for the collector rather than deleted on doubt. Since references are
// added and removed only under this lock, an unreferenced pin for this owner's
// own directory is always an orphan left by an interrupted publication — either
// this call's or a predecessor's — and removing it can only undo that
// interruption, never unprotect a claimed directory.
func rollbackUnpublishedScratchPin(owner ScratchOwner, dir, kind string) error {
	current, err := loadScratchRetention(owner)
	if err != nil {
		return fmt.Errorf("sandbox: confirm rollback of retention pin for %q: %w", dir, err)
	}
	for _, ref := range current.References {
		refDir, err := canonicalScratchPath(ref.Dir)
		if err != nil {
			return fmt.Errorf("sandbox: confirm rollback of retention pin for %q: %w", dir, err)
		}
		if refDir == dir && ref.Kind == kind {
			// The reference was committed despite the reported error.
			return nil
		}
	}
	pin, pinErr := readScratchDirectoryPin(dir)
	switch {
	case pinErr == nil && pin.Owner == owner && filepath.Clean(pin.Dir) == dir && pin.Kind == kind:
		if removeErr := os.Remove(filepath.Join(dir, scratchPinName)); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("sandbox: roll back retention pin for %q: %w", dir, removeErr)
		}
		return nil
	case os.IsNotExist(pinErr):
		// Already absent; nothing to undo.
		return nil
	case pinErr != nil:
		return fmt.Errorf("sandbox: retention pin for %q is unreadable; left in place: %w", dir, pinErr)
	default:
		return fmt.Errorf("sandbox: retention pin for %q does not identify this owner's pin for the directory and kind; left in place", dir)
	}
}

// UpdateScratchBindings replaces only the supplied binding/consumer records in
// one fsynced manifest transaction, preserving every other record. It compares
// expectedRevision, validates all slot references and lease-ownership
// uniqueness, and increments the revision; a stale revision fails without
// writing.
func UpdateScratchBindings(owner ScratchOwner, expectedRevision uint64, bindings []ScratchBinding, consumers []ScratchConsumerBinding) error {
	if err := owner.validate(); err != nil {
		return err
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return err
	}
	if manifest.Released {
		return ErrScratchRetentionReleased
	}
	if manifest.Revision != expectedRevision {
		return fmt.Errorf("%w: manifest %d does not match expected %d", ErrScratchRetentionStaleRevision, manifest.Revision, expectedRevision)
	}
	// Validate the MERGED result, not old+new: the plan's single-transaction
	// move (E0 keeps B while E1 takes A's owning slot) is only legal once the
	// stale E0 record is replaced.
	mergedBindings := mergeScratchBindings(manifest.Bindings, bindings)
	mergedConsumers := mergeScratchConsumers(manifest.Consumers, consumers)
	if err := validateScratchBindingUpdate(manifest, mergedBindings, bindings, consumers); err != nil {
		return err
	}
	manifest.Bindings = mergedBindings
	manifest.Consumers = mergedConsumers
	manifest.Revision++
	return writeScratchRetention(owner, manifest)
}

// UpsertScratchBinding publishes one binding and its consumer record under the
// manifest lock, rebasing the caller's observed record per slot onto whatever
// the fresh manifest already holds. Slots the caller never named — a
// concurrently minted allocation — are preserved, and a slot whose directory a
// different binding already owns is demoted to a wrapper borrow. It never
// replaces a whole stale record.
func UpsertScratchBinding(owner ScratchOwner, binding ScratchBinding, consumer ScratchConsumerBinding) error {
	return upsertScratchBinding(owner, binding, []ScratchConsumerBinding{consumer}, 0, false)
}

// UpsertScratchBindingAtRevision publishes one binding and its consumer
// record with the same per-slot rebasing as UpsertScratchBinding, but only
// when the manifest still stands at expectedRevision. Rows a caller derived
// from an earlier snapshot must not replace what a concurrent writer committed
// after that snapshot — the consumer merge replaces rows wholesale — so the
// check turns that race into ErrScratchRetentionStaleRevision for the caller
// to retry by re-deriving.
func UpsertScratchBindingAtRevision(owner ScratchOwner, binding ScratchBinding, consumer ScratchConsumerBinding, expectedRevision uint64) error {
	return upsertScratchBinding(owner, binding, []ScratchConsumerBinding{consumer}, expectedRevision, true)
}

// ScratchLockContentionDelay returns the growing spacing the bounded retry
// applies between attempts at a fail-fast manifest-lock refusal: the first
// retry waits 1ms and the spacing doubles to an 8ms cap, so a fsync-scale
// hold is waited out rather than failed against. It is the single source for
// every lock-contention spacing in the process; pass attempt counting from 0.
func ScratchLockContentionDelay(attempt int) time.Duration {
	// The doubling is for waiting out a microsecond-to-millisecond hold, not
	// for growing a sustained refusal's cost: cap it at the documented 8ms.
	if attempt >= 3 {
		return 8 * time.Millisecond
	}
	return time.Duration(1<<attempt) * time.Millisecond
}

// RetryScratchLockContention runs fn and retries while fn fails with the
// manifest's transient lock refusal, spacing attempts with a growing backoff
// so a concurrent writer's fsync-scale hold can clear between them: a
// lock-held refusal is by construction a microsecond-to-millisecond race,
// never a durability verdict, so a same-inputs retry is always safe — the
// writers re-read and rebase onto the fresh manifest under the lock. The
// bound keeps a sustained refusal a real, reported failure: exhaustion
// returns the refusal to the caller, never a silent success.
func RetryScratchLockContention(fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); !errors.Is(err, ErrScratchRetentionLockHeld) {
			return err
		}
		if attempt >= 4 {
			return err
		}
		time.Sleep(ScratchLockContentionDelay(attempt))
	}
}

// RetryScratchLockContentionFor retries fn like RetryScratchLockContention but
// bounds the retry by a wall-clock budget instead of the writer-sized attempt
// count. The fixed five-attempt bound is sized for one concurrent writer's
// fsync-scale hold; a call site that serializes a whole fleet of participants
// on the same lock — the adoption revalidations, one per concurrent restore of
// the same root — turns each refusal into a lottery the fleet plays together,
// and five tickets strand everyone past the fifth loser no matter how short
// each individual hold is. Every refusal stays a microsecond-to-millisecond
// race, so retrying the same inputs remains always safe. The budget keeps a
// sustained refusal a real, reported failure: its expiry returns the refusal
// to the caller, never a silent success.
func RetryScratchLockContentionFor(budget time.Duration, fn func() error) error {
	var err error
	start := time.Now()
	for attempt := 0; ; attempt++ {
		if err = fn(); !errors.Is(err, ErrScratchRetentionLockHeld) {
			return err
		}
		if time.Since(start) >= budget {
			return err
		}
		time.Sleep(ScratchLockContentionDelay(attempt))
	}
}

// UpsertScratchBindingOnly publishes one binding record under the manifest lock
// with the same per-slot rebasing as UpsertScratchBinding, but leaves every
// consumer record untouched. It is the writer for an environment that minted an
// allocation without changing any session's current binding: consumer roles are
// owned by the agent layer, and a shared environment's mint must not re-point
// its owner's consumer (or erase its recorded roles).
func UpsertScratchBindingOnly(owner ScratchOwner, binding ScratchBinding) error {
	return upsertScratchBinding(owner, binding, nil, 0, false)
}

// upsertScratchBinding publishes binding and consumers with an optional
// expected-revision guard: checkRevision false is the unchecked legacy path;
// true refuses a manifest that moved past expectedRevision. Absence is the
// explicit flag, never a sign bit — revisions are uint64, and a narrowing
// conversion would read every revision above MaxInt64 as the unchecked path
// (round 24).
func upsertScratchBinding(owner ScratchOwner, binding ScratchBinding, consumers []ScratchConsumerBinding, expectedRevision uint64, checkRevision bool) error {
	if err := owner.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(binding.BindingID) == "" {
		return errors.New("sandbox: scratch binding has no id")
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return err
	}
	if manifest.Released {
		return ErrScratchRetentionReleased
	}
	// A mismatching revision refuses the upsert so a caller holding a
	// superseded snapshot re-derives instead of clobbering whatever committed
	// in between.
	if checkRevision && manifest.Revision != expectedRevision {
		return fmt.Errorf("%w: manifest %d does not match expected %d", ErrScratchRetentionStaleRevision, manifest.Revision, expectedRevision)
	}
	if err := applyScratchBindingUpdate(&manifest, binding, consumers); err != nil {
		return err
	}
	return writeScratchRetention(owner, manifest)
}

// applyScratchBindingUpdate merges binding and consumers into manifest,
// validates the merged result and advances the revision. The caller holds the
// manifest lock and commits manifest with exactly one writeScratchRetention, so
// a caller that also appends references publishes them in the same transaction.
func applyScratchBindingUpdate(manifest *ScratchManifest, binding ScratchBinding, consumers []ScratchConsumerBinding) error {
	merged := mergeScratchBindingSlots(*manifest, binding)
	mergedBindings := mergeScratchBindings(manifest.Bindings, []ScratchBinding{merged})
	mergedConsumers := mergeScratchConsumers(manifest.Consumers, consumers)
	if err := validateScratchBindingUpdate(*manifest, mergedBindings, []ScratchBinding{binding}, consumers); err != nil {
		return err
	}
	manifest.Bindings = mergedBindings
	manifest.Consumers = mergedConsumers
	manifest.Revision++
	return nil
}

// mergeScratchBindingSlots rebases target onto the fresh manifest per slot:
// every slot target names is applied (and demoted to a borrow when another
// binding already owns its directory), while slots only the manifest holds are
// preserved. Stored owner identity is never renamed by a caller.
func mergeScratchBindingSlots(manifest ScratchManifest, target ScratchBinding) ScratchBinding {
	merged := target
	if stored, ok := scratchBindingByID(manifest.Bindings, target.BindingID); ok {
		merged = stored
	}
	slots := make(map[string]ScratchSlot, len(merged.Slots)+len(target.Slots))
	maps.Copy(slots, merged.Slots)
	merged.Slots = slots
	for kind, slot := range target.Slots {
		if slot.OwnsLease {
			if dir, err := canonicalScratchPath(slot.Dir); err == nil {
				if owner, ok := leaseOwningBinding(manifest, dir); ok && owner != target.BindingID {
					slot.OwnsLease = false
				}
			}
		}
		merged.Slots[kind] = slot
	}
	return merged
}

func scratchBindingByID(bindings []ScratchBinding, bindingID string) (ScratchBinding, bool) {
	for _, binding := range bindings {
		if binding.BindingID == bindingID {
			return binding, true
		}
	}
	return ScratchBinding{}, false
}

func leaseOwningBinding(manifest ScratchManifest, canonicalDir string) (string, bool) {
	for _, binding := range manifest.Bindings {
		for _, slot := range binding.Slots {
			if !slot.OwnsLease {
				continue
			}
			if dir, err := canonicalScratchPath(slot.Dir); err == nil && dir == canonicalDir {
				return binding.BindingID, true
			}
		}
	}
	return "", false
}

// consumersNamingScratchBinding returns every consumer row that names bindingID
// in any of its roles: the rows a carried binding must travel with for the
// graph's reader to accept an owning binding.
func consumersNamingScratchBinding(consumers []ScratchConsumerBinding, bindingID string) []ScratchConsumerBinding {
	var named []ScratchConsumerBinding
	for _, consumer := range consumers {
		if consumer.CurrentBindingID == bindingID ||
			consumer.ParentSharedBindingID == bindingID ||
			consumer.WorktreeRestoreBindingID == bindingID ||
			slices.Contains(consumer.AbandonedBindingIDs, bindingID) {
			named = append(named, consumer)
		}
	}
	return named
}

func validateScratchBindingUpdate(manifest ScratchManifest, mergedBindings []ScratchBinding, suppliedBindings []ScratchBinding, suppliedConsumers []ScratchConsumerBinding) error {
	refs := make(map[string]string, len(manifest.References))
	for _, ref := range manifest.References {
		dir, err := canonicalScratchPath(ref.Dir)
		if err != nil {
			return err
		}
		refs[dir] = ref.Kind
	}
	seenBinding := make(map[string]struct{}, len(suppliedBindings))
	for _, binding := range suppliedBindings {
		if strings.TrimSpace(binding.BindingID) == "" {
			return errors.New("sandbox: scratch binding has no id")
		}
		if _, dup := seenBinding[binding.BindingID]; dup {
			return fmt.Errorf("sandbox: duplicate scratch binding %q", binding.BindingID)
		}
		seenBinding[binding.BindingID] = struct{}{}
	}
	for _, binding := range mergedBindings {
		for kind, slot := range binding.Slots {
			dir, err := canonicalScratchPath(slot.Dir)
			if err != nil {
				return err
			}
			pinnedKind, ok := refs[dir]
			if !ok {
				return fmt.Errorf("sandbox: scratch binding %q slot %q references unpinned directory %q", binding.BindingID, kind, dir)
			}
			if pinnedKind != kind {
				// A slot claiming a pinned directory under another kind has
				// no reference to pair with — the same mismatch the graph
				// reader fails closed on, caught here before it can be
				// written (round 67).
				return fmt.Errorf("sandbox: scratch binding %q slot %q kind does not match pinned kind %q", binding.BindingID, kind, pinnedKind)
			}
		}
	}
	seenConsumer := make(map[string]struct{}, len(suppliedConsumers))
	for _, consumer := range suppliedConsumers {
		if strings.TrimSpace(consumer.SessionID) == "" {
			return errors.New("sandbox: scratch consumer has no session id")
		}
		if _, dup := seenConsumer[consumer.SessionID]; dup {
			return fmt.Errorf("sandbox: duplicate scratch consumer %q", consumer.SessionID)
		}
		seenConsumer[consumer.SessionID] = struct{}{}
	}
	// At most one current allocation per (BindingID, Kind) is guaranteed by the
	// map key; across bindings, the MERGED set may have a directory owned by at
	// most one binding.
	owners := make(map[string]string)
	for _, binding := range mergedBindings {
		for kind, slot := range binding.Slots {
			if !slot.OwnsLease {
				continue
			}
			dir, err := canonicalScratchPath(slot.Dir)
			if err != nil {
				return err
			}
			if owner, ok := owners[dir]; ok && owner != binding.BindingID {
				return fmt.Errorf("sandbox: directory %q has lease-owning slots in bindings %q and %q", dir, owner, binding.BindingID)
			}
			owners[dir] = binding.BindingID
			_ = kind
		}
	}
	return nil
}

func mergeScratchBindings(existing, updates []ScratchBinding) []ScratchBinding {
	merged := make([]ScratchBinding, 0, len(existing)+len(updates))
	replaced := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		replaced[update.BindingID] = struct{}{}
	}
	for _, binding := range existing {
		if _, ok := replaced[binding.BindingID]; !ok {
			merged = append(merged, binding)
		}
	}
	return append(merged, updates...)
}

func mergeScratchConsumers(existing, updates []ScratchConsumerBinding) []ScratchConsumerBinding {
	merged := make([]ScratchConsumerBinding, 0, len(existing)+len(updates))
	replaced := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		replaced[update.SessionID] = struct{}{}
	}
	for _, consumer := range existing {
		if _, ok := replaced[consumer.SessionID]; !ok {
			merged = append(merged, consumer)
		}
	}
	return append(merged, updates...)
}

// scratchRetentionOpenBeforeLease, when non-nil, runs after
// OpenRetainedSessionScratch has validated the manifest and pin and before it
// acquires the directory lease. It exists only as a test seam for the
// release/open interleaving; production leaves it nil.
var scratchRetentionOpenBeforeLease func()

// OpenRetainedSessionScratch reacquires the lease of a pinned allocation at its
// original path. It refuses a released owner, a missing/conflicting pin, a
// foreign namespace, or a directory whose inode changed while the lease was
// being acquired. Opening is serialized with a concurrent terminal release
// under the manifest lock, and the tombstone and pin are revalidated after the
// directory lease is acquired, so a release that committed between validation
// and lease acquisition cannot yield a usable handle for an allocation the
// release already tombstoned.
func OpenRetainedSessionScratch(owner ScratchOwner, ref ScratchReference) (*SessionScratch, error) {
	if err := owner.validate(); err != nil {
		return nil, err
	}
	dir, err := canonicalScratchPath(ref.Dir)
	if err != nil {
		return nil, err
	}
	if probe := scratchOpenProbe; probe != nil {
		if err := probe(); err != nil {
			return nil, err
		}
	}
	// ReleaseScratchRetention writes the tombstone and removes pins while
	// holding this lock, so holding it across the lease acquisition serializes
	// open against release.
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return nil, err
	}
	if manifest.Released {
		return nil, ErrScratchRetentionReleased
	}
	referenced := false
	for _, existing := range manifest.References {
		existingDir, err := canonicalScratchPath(existing.Dir)
		if err != nil {
			return nil, err
		}
		if existingDir == dir && existing.Kind == ref.Kind {
			referenced = true
			break
		}
	}
	if !referenced {
		return nil, fmt.Errorf("sandbox: %q is not a retained reference", dir)
	}
	if err := verifyRetainedScratchPin(owner, dir, ref.Kind); err != nil {
		return nil, err
	}
	base := filepath.Dir(dir)
	if !strings.HasPrefix(filepath.Base(dir), sessionScratchPrefix) {
		return nil, fmt.Errorf("sandbox: refuse restore outside session scratch namespace: %q", dir)
	}
	before, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: stat retained scratch: %w", err)
	}
	if hook := scratchRetentionOpenBeforeLease; hook != nil {
		hook()
	}
	lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
	if contended {
		return nil, ErrScratchRetentionLeaseHeld
	}
	if err != nil {
		return nil, fmt.Errorf("sandbox: acquire retained scratch lease: %w", err)
	}
	after, err := os.Stat(dir)
	if err != nil || !os.SameFile(before, after) {
		_ = lease.Release()
		return nil, fmt.Errorf("sandbox: retained scratch %q changed while acquiring its lease", dir)
	}
	// Revalidate under the held lease: a release that committed between the
	// initial validation and the lease acquisition must not yield a usable
	// handle.
	if err := revalidateRetainedScratchAfterLease(owner, dir, ref.Kind); err != nil {
		_ = lease.Release()
		return nil, err
	}
	return &SessionScratch{Dir: dir, base: base, lease: lease}, nil
}

// scratchOpenProbe, when set, is consulted at the top of
// OpenRetainedSessionScratch before the manifest lock is taken: a non-nil
// error opens nothing and is returned to the caller. Tests use it to model a
// concurrent fail-fast manifest-lock hold (ErrScratchRetentionLockHeld) and a
// terminal release committing inside the preparation-to-open window without
// lock choreography. It is nil in production; tests set it through
// SetScratchOpenProbeForTesting and clear it with their cleanup.
var scratchOpenProbe func() error

// SetScratchOpenProbeForTesting installs the probe consulted at the top of
// OpenRetainedSessionScratch. Passing nil clears it.
func SetScratchOpenProbeForTesting(fn func() error) {
	scratchOpenProbe = fn
}

// verifyRetainedScratchPin reads dir's identity pin and confirms it is exactly
// this owner's pin for dir and kind.
func verifyRetainedScratchPin(owner ScratchOwner, dir, kind string) error {
	pin, err := readScratchDirectoryPin(dir)
	if err != nil {
		return fmt.Errorf("sandbox: read retention pin: %w", err)
	}
	if pin.Owner != owner || filepath.Clean(pin.Dir) != dir || pin.Kind != kind {
		return fmt.Errorf("sandbox: retention pin identity does not match %q", dir)
	}
	return nil
}

// ValidateRetainedScratchPins loads owner's retention manifest under the
// manifest lock and verifies every referenced allocation still carries this
// owner's immutable identity pin for its kind. It neither acquires a directory
// lease nor mutates durable state, so it is safe during retirement preparation;
// holding the manifest lock across the check serializes it against a concurrent
// release that tombstones the manifest and removes pins. A released manifest is
// returned with no verification — retirement has nothing left to protect. It
// returns the loaded manifest so a caller can continue its own binding checks
// against the same snapshot.
func ValidateRetainedScratchPins(owner ScratchOwner) (ScratchManifest, error) {
	if err := owner.validate(); err != nil {
		return ScratchManifest{}, err
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return ScratchManifest{}, err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return ScratchManifest{}, err
	}
	if manifest.Released {
		return manifest, nil
	}
	for _, ref := range manifest.References {
		dir, err := canonicalScratchPath(ref.Dir)
		if err != nil {
			return ScratchManifest{}, err
		}
		if err := verifyRetainedScratchPin(owner, dir, ref.Kind); err != nil {
			return ScratchManifest{}, err
		}
	}
	return manifest, nil
}

// revalidateRetainedScratchAfterLease re-checks the tombstone and the directory
// pin after the directory lease has been acquired, closing the window where a
// concurrent release could tombstone the manifest and remove the pin between
// the initial validation and the lease acquisition.
func revalidateRetainedScratchAfterLease(owner ScratchOwner, dir, kind string) error {
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return err
	}
	if manifest.Released {
		// Typed: the refresh's release-race decline check matches this error
		// with errors.Is, and an untyped sentinel would slip past it and fail
		// the exact race it exists for (round 15).
		return fmt.Errorf("sandbox: scratch retention was released while acquiring the lease: %w", ErrScratchRetentionReleased)
	}
	return verifyRetainedScratchPin(owner, dir, kind)
}

// ReleaseScratchRetention writes the terminal tombstone that authorizes
// ordinary age-based collection for this owner's directories, then removes the
// matching per-directory identity pin under each allocation whose lease it can
// acquire. A live lease leaves its tombstone/pin for the collector to finish.
// The manifest/tombstone is kept until every matching pin is removed, so an
// interruption can never expose a false missing-manifest state. It never
// removes a directory and is never called by retirement itself.
func ReleaseScratchRetention(owner ScratchOwner) error {
	if err := owner.validate(); err != nil {
		return err
	}
	lock, err := acquireScratchRetentionLock(owner)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return err
	}
	manifest.Released = true
	manifest.Revision++
	if err := writeScratchRetention(owner, manifest); err != nil {
		return err
	}
	// The tombstone is durable before any pin is removed, so an interruption
	// between the two can only leave an extra pin, never a pinless directory
	// whose manifest still expects it.
	var failures []error
	for _, ref := range manifest.References {
		dir, err := canonicalScratchPath(ref.Dir)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
		if contended {
			// A live lease owns the directory; leave its pin for the collector,
			// which acquires the lease before deciding.
			continue
		}
		if err != nil {
			// A confirmed contention reads as "still owned"; anything else (open,
			// stat or chmod failure) means the lease could not be inspected, so the
			// release must not look successful.
			failures = append(failures, fmt.Errorf("sandbox: acquire retention lease for %q: %w", dir, err))
			continue
		}
		// Re-verify identity under the held lease: remove the pin only when it is
		// still this owner's own pin for this directory and kind. A pin that was
		// replaced or is malformed is left for the collector, never deleted on
		// doubt.
		pin, pinErr := readScratchDirectoryPin(dir)
		switch {
		case pinErr == nil && pin.Owner == owner && filepath.Clean(pin.Dir) == dir && pin.Kind == ref.Kind:
			removeErr := os.Remove(filepath.Join(dir, scratchPinName))
			if removeErr != nil && !os.IsNotExist(removeErr) {
				failures = append(failures, fmt.Errorf("sandbox: remove retention pin for %q: %w", dir, removeErr))
			}
		case os.IsNotExist(pinErr):
			// Already absent; nothing to remove.
		default:
			if pinErr != nil {
				failures = append(failures, fmt.Errorf("sandbox: retention pin for %q is unreadable; left for the collector: %w", dir, pinErr))
			} else {
				failures = append(failures, fmt.Errorf("sandbox: retention pin for %q is not this owner's; left for the collector", dir))
			}
		}
		if releaseErr := lease.Release(); releaseErr != nil {
			failures = append(failures, fmt.Errorf("sandbox: release retention lease for %q: %w", dir, releaseErr))
		}
	}
	return errors.Join(failures...)
}

// ResetScratchRetentionIfReleased reinitializes a manifest whose terminal
// tombstone has committed and returns the manifest to publish against, plus
// whether the reinitialization ran. A
// terminal close removed every pin it could acquire and authorized ordinary
// collection for the rest, so a restored session treats that durable state as
// already gone: it mints fresh allocations rather than resuming the closed
// session's scratch, and its first publication must be a legal write against a
// manifest that no longer claims to be released — otherwise every write rides
// a tombstone that authorizes collecting the session's live allocations. An
// unreleased manifest is returned untouched with reset=false. The reset
// verdict is computed under the reset's own manifest lock, so a caller gating
// adoption of carried rows on it cannot race a terminal release that
// tombstones the manifest between the caller's earlier read and the reset
// (round 22).
func ResetScratchRetentionIfReleased(owner ScratchOwner) (ScratchManifest, bool, error) {
	if err := owner.validate(); err != nil {
		return ScratchManifest{}, false, err
	}
	// The manifest lock is fail-fast like every scratch writer's, and a
	// refused reset must not fail the restore it is part of — the same
	// bounded growing backoff applies.
	var out ScratchManifest
	var reset bool
	err := RetryScratchLockContention(func() error {
		// The whole reset is serialized against scratch reclamation: the carry
		// pass resurrects Released rows without taking any directory lease, so
		// a sweep could otherwise remove a carried directory between this
		// reset's read and its commit. Taken per attempt inside the retry —
		// the reset never holds it across a backoff, and a manifest-lock
		// refusal releases it before the retry.
		if scratchResetBeforeReclaimLock != nil {
			scratchResetBeforeReclaimLock()
		}
		scratchReclamationMu.Lock()
		defer scratchReclamationMu.Unlock()
		lock, err := acquireScratchRetentionLock(owner)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Release() }()
		manifest, err := loadScratchRetention(owner)
		if err != nil {
			return err
		}
		if !manifest.Released {
			out = manifest
			return nil
		}
		// Keep the revision advancing: a writer holding the pre-reset revision
		// must still read as stale against the reinitialized manifest.
		fresh := ScratchManifest{
			Version:  manifest.Version,
			Revision: manifest.Revision + 1,
			Owner:    manifest.Owner,
		}
		// A carried reference must travel with the rows that make the graph
		// valid for its own reader (validateRetainedScratchGraph): the binding
		// that owns its directory and every consumer role that names it.
		// Carrying a reference alone would commit a manifest whose restore
		// validation fails forever — references with no binding — with no
		// later reset to repair it, Released being false again (round 16). A
		// reference nothing in the tombstoned manifest owns is nothing a
		// restore could re-probe: its pair dies here instead — the reference
		// drops and every pin is left in place: this owner's own pin is
		// re-pinned by the reinstall's republish (same owner, directory, and
		// kind — the pin write is idempotent), and when nothing republishes,
		// its protection ends with the next terminal release's tombstone —
		// which is what authorizes the collector to remove the pin and its
		// directory; a foreign pin belongs to its own manifest (round 25).
		carriedBindings := make(map[string]ScratchBinding)
		carryReference := func(dir, kind string) error {
			ownerID, owned := leaseOwningBinding(manifest, dir)
			_, found := scratchBindingByID(manifest.Bindings, ownerID)
			consumers := consumersNamingScratchBinding(manifest.Consumers, ownerID)
			if !owned || !found || len(consumers) == 0 {
				// Ownerless, or an owning binding no consumer names — the
				// crash-window artifact the graph reader fails closed on:
				// carrying either would wedge every later restore on the
				// fresh manifest. Let the pair die together, and leave every
				// pin untouched: the death's only caller is the contended
				// branch, so this owner's readable pin protects a directory
				// whose lease a live holder still holds — stripping it
				// lease-less left the collector free to sweep the directory
				// out from under the holder (round 25). The reinstall this
				// reset serves re-pins the same identity immediately, and
				// when nothing does, the pin's protection ends with the next
				// terminal release's tombstone, which is what authorizes the
				// collector to remove it; until then the collector
				// conservatively retains it with a diagnostic.
				// Only a pin that cannot be READ aborts the death: committing
				// past an unreadable pin strands an orphan with no diagnostic
				// at all, and the free-lease branch would refuse the same
				// read anyway (round 23).
				return verifyDyingReferencePin(dir)
			}
			fresh.References = append(fresh.References, ScratchReference{Dir: dir, Kind: kind})
			// The reference travels with every binding whose slot names its
			// directory, not only the lease owner: a wrapper-only slot is a
			// distinct consumer's identity for the same retained allocation,
			// and dropping it orphaned that consumer's row in the narrowing
			// pass below — its next restore then minted fresh scratch instead
			// of borrowing the directory it still held (round 24). Each carried
			// binding narrows to the slots whose directories carried: its
			// other slots may name pins the terminal release already removed,
			// and a slot naming an unpinned directory fails the graph reader.
			for _, binding := range manifest.Bindings {
				for slotKind, slot := range binding.Slots {
					if slotDir, err := canonicalScratchPath(slot.Dir); err == nil && slotDir == dir {
						// Only the reference's own kind carries (round 67): a
						// slot claiming the directory under another kind has
						// no reference to pair with in the fresh manifest, and
						// the graph reader fails closed on the mismatch —
						// wedging every later restore of the root. The binding
						// simply does not travel for this reference; if none
						// of its slots match, the narrowing passes below drop
						// its roles with it.
						if slotKind != kind {
							continue
						}
						narrowed, ok := carriedBindings[binding.BindingID]
						if !ok {
							narrowed = binding
							narrowed.Slots = map[string]ScratchSlot{}
							carriedBindings[binding.BindingID] = narrowed
						}
						narrowed.Slots[slotKind] = slot
					}
				}
			}
			return nil
		}
		// The terminal release that tombstoned this manifest removed every
		// pin whose lease it could take; the ones left behind were contended
		// by live owners, and Released:true was what made them collectible. A
		// reinitialized manifest no longer claims to be released, so each
		// surviving pin must be reconciled or the collector would retain its
		// directory forever with a diagnostic on every sweep: finish the
		// release for the pins whose lease is now free (remove the pin; the
		// directory becomes ordinary), and carry a reference for the pins
		// still held by a pin that is exactly this owner's identity for the
		// directory and kind (the pair stays coherent, the holder keeps its
		// protection, and the next terminal release collects them).
		for _, ref := range manifest.References {
			dir, err := canonicalScratchPath(ref.Dir)
			if err != nil {
				return err
			}
			lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
			if contended {
				pin, pinErr := readScratchDirectoryPin(dir)
				switch {
				case os.IsNotExist(pinErr):
					// A pin the crash window removed leaves nothing a carried
					// reference could pair with: the reference dies with the
					// manifest (round 16).
				case pinErr != nil:
					// An unreadable pin: the reset cannot tell whose protection
					// it is. Abort with the tombstone intact rather than commit
					// past an orphan the collector conservatively retains, with
					// a diagnostic, forever; a later reset retries once the
					// read heals (round 19).
					return fmt.Errorf("sandbox: read retention pin for %q: %w", dir, pinErr)
				case pin.Owner == owner && filepath.Clean(pin.Dir) == dir && pin.Kind == ref.Kind:
					// Exactly this owner's pin for the directory and kind: the
					// pair is still real, and the carry keeps it coherent with
					// the holder's protection.
					if err := carryReference(dir, ref.Kind); err != nil {
						return err
					}
				case pin.Owner == owner:
					// Our own pin with a directory or kind this reference
					// cannot verify. The lease is not ours to remove against,
					// but the contention is transient — a live holder releases —
					// so abort rather than commit past it: the tombstone stays,
					// and the next reset reaches this reference through the
					// free-lease branch, which removes the malformed pin and
					// finishes the release (round 19).
					return fmt.Errorf("sandbox: retention pin for %q does not identify this owner's pin for the directory and kind", dir)
				default:
					// A foreign pin under contention: leave the file — it
					// belongs to its own manifest — and drop the reference
					// (round 17).
				}
				continue
			}
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// The directory was already collected out from under the
					// tombstone (Released pins read collectible), so the
					// reference is stale: it dies with the manifest. Failing
					// here would block every later reset on a directory that
					// is never coming back.
					continue
				}
				return fmt.Errorf("sandbox: acquire retention lease for %q: %w", dir, err)
			}
			// finishRelease removes this owner's pin now that the lease is
			// ours, releasing the lease on the way out of a failure.
			finishRelease := func() error {
				if rmErr := os.Remove(filepath.Join(dir, scratchPinName)); rmErr != nil && !os.IsNotExist(rmErr) {
					if releaseErr := lease.Release(); releaseErr != nil {
						return fmt.Errorf("sandbox: remove retention pin for %q: %w", dir, errors.Join(rmErr, releaseErr))
					}
					return fmt.Errorf("sandbox: remove retention pin for %q: %w", dir, rmErr)
				}
				return nil
			}
			switch pin, pinErr := readScratchDirectoryPin(dir); {
			case pinErr == nil && pin.Owner == owner && filepath.Clean(pin.Dir) == dir && pin.Kind == ref.Kind:
				// The real pair with its lease now free: finish the release.
				if err := finishRelease(); err != nil {
					return err
				}
			case os.IsNotExist(pinErr):
				// Already absent; the reference dies with the manifest.
			case pinErr == nil && pin.Owner == owner:
				// Our own pin with a directory or kind this reference cannot
				// verify — garbage this owner wrote, and the lease is ours
				// right now, so finish the release for it exactly like the
				// matched case: remove the malformed pin and let the
				// reference die with the manifest. Leaving it would strand an
				// orphan an unreleased manifest does not reference, which the
				// collector conservatively retains, with a diagnostic, forever
				// (round 19).
				if err := finishRelease(); err != nil {
					return err
				}
			case pinErr == nil:
				// A foreign pin: leave the file — its coherence is its own
				// manifest's business — and drop the reference, which no
				// longer has a pair here (round 17).
			default:
				// An unreadable pin: the reset cannot even tell whose
				// protection it is, and committing past it would strand the
				// same retained-forever orphan. Abort with the tombstone
				// intact; a later reset retries once the read heals (round
				// 19).
				if releaseErr := lease.Release(); releaseErr != nil {
					return fmt.Errorf("sandbox: read retention pin for %q: %w", dir, errors.Join(pinErr, releaseErr))
				}
				return fmt.Errorf("sandbox: read retention pin for %q: %w", dir, pinErr)
			}
			if releaseErr := lease.Release(); releaseErr != nil {
				return fmt.Errorf("sandbox: release retention lease for %q: %w", dir, releaseErr)
			}
		}
		for _, id := range slices.Sorted(maps.Keys(carriedBindings)) {
			fresh.Bindings = append(fresh.Bindings, carriedBindings[id])
		}
		// Consumer rows narrow in one pass after the carries, not once per
		// carried reference: a consumer routinely names several bindings,
		// and narrowing per carry let the second carry overwrite the
		// first's roles, leaving a carried binding no consumer names — the
		// exact graph the reader fails closed on (round 17). A row
		// survives with exactly the roles that name carried bindings.
		fresh.Consumers = make([]ScratchConsumerBinding, 0, len(manifest.Consumers))
		for _, consumer := range manifest.Consumers {
			narrowed := consumer
			kept := false
			if _, ok := carriedBindings[narrowed.CurrentBindingID]; ok {
				kept = true
			} else {
				narrowed.CurrentBindingID = ""
			}
			if _, ok := carriedBindings[narrowed.ParentSharedBindingID]; ok {
				kept = true
			} else {
				narrowed.ParentSharedBindingID = ""
			}
			if _, ok := carriedBindings[narrowed.WorktreeRestoreBindingID]; ok {
				kept = true
			} else {
				narrowed.WorktreeRestoreBindingID = ""
			}
			abandoned := make([]string, 0, len(narrowed.AbandonedBindingIDs))
			for _, id := range narrowed.AbandonedBindingIDs {
				if _, ok := carriedBindings[id]; ok {
					abandoned = append(abandoned, id)
				}
			}
			if len(abandoned) != len(narrowed.AbandonedBindingIDs) {
				narrowed.AbandonedBindingIDs = abandoned
			}
			if kept || len(abandoned) > 0 {
				fresh.Consumers = append(fresh.Consumers, narrowed)
			}
		}
		if err := writeScratchRetention(owner, fresh); err != nil {
			// A write whose rename already committed can still report the
			// post-rename failure class (writeScratchRetention's probe fires
			// after atomicWritePrivateFile): the reset then committed —
			// Released false with the carried rows at the advanced revision —
			// and reporting reset=false aborts the install over a manifest
			// the reset already repaired, leaving carried references pinned
			// with no restored consumer to re-probe them. This closure holds
			// the single-writer retention lock, so an unreleased manifest at
			// exactly fresh's revision is this reset's commit; anything else
			// keeps the error (round 45; the rounds 39/43 commit
			// discriminators applied to the reset).
			if current, rerr := loadScratchRetention(owner); rerr == nil &&
				!current.Released && current.Revision == fresh.Revision {
				out = current
				reset = true
				return nil
			}
			return err
		}
		out = fresh
		reset = true
		return nil
	})
	if err != nil {
		return ScratchManifest{}, false, err
	}
	return out, reset, nil
}

// verifyDyingReferencePin checks the pin state of a reference whose owning
// pair died with the tombstoned manifest. The death's only caller is the
// reset's contended branch, so the pin protects a directory whose lease a
// live holder still holds: a readable pin — this owner's own or a foreign
// one — is LEFT UNTOUCHED. Stripping this owner's pin lease-less would leave
// the holder's directory collectible while the holder still uses it; the
// reinstall the reset serves re-pins the same identity immediately (the pin
// write is idempotent for a matching owner, directory, and kind), and when
// nothing republishes, the pin's protection ends with the next terminal
// release's tombstone — which is what authorizes the collector to remove the
// pin and its directory (round 25). A pin that is already absent has nothing
// to protect. A pin that cannot be READ aborts the death — committing the
// reference away past an unreadable pin strands an orphan with no diagnostic
// at all, with no later reset left to retry (round 23) — the same contract
// the contended and free-lease branches enforce on their own reads
// (round 19).
func verifyDyingReferencePin(dir string) error {
	_, pinErr := readScratchDirectoryPin(dir)
	if os.IsNotExist(pinErr) {
		return nil
	}
	if pinErr != nil {
		return fmt.Errorf("sandbox: read retention pin for %q: %w", dir, pinErr)
	}
	// A readable pin — ours or foreign — is left exactly where it is.
	return nil
}

// BorrowRetainedSessionScratch returns a lease-less handle to an already
// retained directory so a distinct sharing consumer can point at the same
// allocation without duplicating its lease. The directory must exist.
func BorrowRetainedSessionScratch(dir string) (*SessionScratch, error) {
	canonical, err := canonicalScratchPath(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(canonical); err != nil {
		return nil, fmt.Errorf("sandbox: borrow retained scratch: %w", err)
	}
	return &SessionScratch{Dir: canonical, base: filepath.Dir(canonical)}, nil
}

// ScratchDirectoryRetained decides whether the collector must skip dir —
// and whether a wrapper borrow may still install it: a directory the
// collector may take is not one a restored environment may use (round 30).
// A missing pin means "not retained by this subsystem". A malformed pin, a
// missing/incomplete manifest, or a pin whose reference is absent is
// conservatively retained with a diagnostic, never treated as collectible. A
// Released tombstone authorizes ordinary collection.
func ScratchDirectoryRetained(dir string) (bool, error) {
	pin, err := readScratchDirectoryPin(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return true, fmt.Errorf("sandbox: retention pin for %q: %w", dir, err)
	}
	manifest, err := loadScratchRetention(pin.Owner)
	if err != nil {
		return true, fmt.Errorf("sandbox: retention manifest for %q: %w", dir, err)
	}
	if manifest.Released {
		return false, nil
	}
	canonical, err := canonicalScratchPath(dir)
	if err != nil {
		return true, fmt.Errorf("sandbox: retention pin for %q: %w", dir, err)
	}
	for _, ref := range manifest.References {
		refDir, err := canonicalScratchPath(ref.Dir)
		if err != nil {
			return true, fmt.Errorf("sandbox: retention manifest for %q: %w", dir, err)
		}
		if refDir == canonical && ref.Kind == pin.Kind {
			return true, nil
		}
	}
	return true, fmt.Errorf("sandbox: retention manifest for %q has no reference to its pin", dir)
}

// atomicWritePrivateFile writes data to path via a same-directory temp file,
// fsyncs the file, renames it over path, then fsyncs the containing directory.
func atomicWritePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sandbox: create retention dir: %w", err)
	}
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("sandbox: create retention temp: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sandbox: write retention temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sandbox: sync retention temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("sandbox: close retention temp: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("sandbox: commit retention file: %w", err)
	}
	committed = true
	if err := fsyncDir(dir); err != nil {
		return err
	}
	return nil
}

func fsyncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sandbox: open retention dir for sync: %w", err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sandbox: sync retention dir: %w", err)
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
