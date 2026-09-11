package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func writeScratchRetention(owner ScratchOwner, manifest ScratchManifest) error {
	manifest.Version = scratchRetentionVersion
	manifest.Owner = owner
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("sandbox: marshal scratch retention manifest: %w", err)
	}
	return atomicWritePrivateFile(scratchManifestPath(owner), raw)
}

func acquireScratchRetentionLock(owner ScratchOwner) (scratchLease, error) {
	if err := os.MkdirAll(scratchRetentionDir(owner), 0o700); err != nil {
		return nil, fmt.Errorf("sandbox: create scratch retention dir: %w", err)
	}
	lease, contended, err := acquireScratchLease(scratchRetentionLockPath(owner))
	if err != nil {
		return nil, fmt.Errorf("sandbox: acquire scratch retention lock: %w", err)
	}
	if contended {
		return nil, errors.New("sandbox: scratch retention manifest is locked by another writer")
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
	return writeScratchRetention(owner, manifest)
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
	merged := mergeScratchBindingSlots(manifest, binding)
	mergedBindings := mergeScratchBindings(manifest.Bindings, []ScratchBinding{merged})
	mergedConsumers := mergeScratchConsumers(manifest.Consumers, []ScratchConsumerBinding{consumer})
	if err := validateScratchBindingUpdate(manifest, mergedBindings, []ScratchBinding{binding}, []ScratchConsumerBinding{consumer}); err != nil {
		return err
	}
	manifest.Bindings = mergedBindings
	manifest.Consumers = mergedConsumers
	manifest.Revision++
	return writeScratchRetention(owner, manifest)
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
	for kind, slot := range merged.Slots {
		slots[kind] = slot
	}
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

func validateScratchBindingUpdate(manifest ScratchManifest, mergedBindings []ScratchBinding, suppliedBindings []ScratchBinding, suppliedConsumers []ScratchConsumerBinding) error {
	refs := make(map[string]struct{}, len(manifest.References))
	for _, ref := range manifest.References {
		dir, err := canonicalScratchPath(ref.Dir)
		if err != nil {
			return err
		}
		refs[dir] = struct{}{}
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
			if _, ok := refs[dir]; !ok {
				return fmt.Errorf("sandbox: scratch binding %q slot %q references unpinned directory %q", binding.BindingID, kind, dir)
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

// OpenRetainedSessionScratch reacquires the lease of a pinned allocation at its
// original path. It refuses a released owner, a missing/conflicting pin, a
// foreign namespace, or a directory whose inode changed while the lease was
// being acquired.
func OpenRetainedSessionScratch(owner ScratchOwner, ref ScratchReference) (*SessionScratch, error) {
	if err := owner.validate(); err != nil {
		return nil, err
	}
	dir, err := canonicalScratchPath(ref.Dir)
	if err != nil {
		return nil, err
	}
	manifest, err := loadScratchRetention(owner)
	if err != nil {
		return nil, err
	}
	if manifest.Released {
		return nil, errors.New("sandbox: scratch retention is released")
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
	pin, err := readScratchDirectoryPin(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: read retention pin: %w", err)
	}
	if pin.Owner != owner || filepath.Clean(pin.Dir) != dir || pin.Kind != ref.Kind {
		return nil, fmt.Errorf("sandbox: retention pin identity does not match %q", dir)
	}
	base := filepath.Dir(dir)
	if !strings.HasPrefix(filepath.Base(dir), sessionScratchPrefix) {
		return nil, fmt.Errorf("sandbox: refuse restore outside session scratch namespace: %q", dir)
	}
	before, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: stat retained scratch: %w", err)
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
	return &SessionScratch{Dir: dir, base: base, lease: lease}, nil
}

// ReleaseScratchRetention writes the terminal tombstone that authorizes
// ordinary age-based collection for this owner's directories. It never removes
// a directory and is never called by retirement itself.
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
	return writeScratchRetention(owner, manifest)
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

// scratchDirectoryRetained decides whether the collector must skip dir. A
// missing pin means "not retained by this subsystem". A malformed pin, a
// missing/incomplete manifest, or a pin whose reference is absent is
// conservatively retained with a diagnostic, never treated as collectible. A
// Released tombstone authorizes ordinary collection.
func scratchDirectoryRetained(dir string) (bool, error) {
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
