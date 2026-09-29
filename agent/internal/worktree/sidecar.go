package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var sidecarWrite = func(f *os.File, raw []byte) (int, error) { return f.Write(raw) }

// Sidecar is the on-disk shape of a managed worktree's metadata JSON file
// (spec §6 "Metadata sidecar" — field names and json tags are normative).
// It is the sole source of a managed worktree's provenance: who created it,
// from what base, and (once removed) whether its branch was kept.
type Sidecar struct {
	Name            string `json:"name"`
	Branch          string `json:"branch"`
	BaseSHA         string `json:"base_sha"`
	MergeTarget     string `json:"merge_target,omitempty"`
	OriginalRoot    string `json:"original_root"`
	CreatorSession  string `json:"creator_session"`
	DelegateID      string `json:"delegate_id,omitempty"`
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	TipSHAAtRemoval string `json:"tip_sha_at_removal,omitempty"`
	CreatedAt       string `json:"created_at"`
}

// BranchOrName is the single resolution rule for a lane's git branch: the
// recorded Branch when set, the Name otherwise. Every branch-acting consumer
// (dispose, close-time disposal, remove, prune) resolves a lane's branch
// through this method and never through the name, so a lane whose branch
// differs from its directory name (a delegate lane created with an explicit
// branch) is judged and deleted by the branch git actually holds. The fallback
// covers a legacy sidecar that records no branch.
func (sc Sidecar) BranchOrName() string {
	if sc.Branch != "" {
		return sc.Branch
	}
	return sc.Name
}

// ReconcileGrace is the minimum sidecar file age (spec §5 sweep 2, judged by
// the file's mtime on the shared filesystem, never the recorded CreatedAt
// wall-clock string) before prune's reconciliation sweep will act on a
// sidecar with no matching registered worktree. It exists because a
// concurrent create writes its sidecar moments before `git worktree add`
// registers the worktree; without the grace, reconciliation could eat a
// fresh sidecar out from under a create that is still in flight.
const ReconcileGrace = 15 * time.Minute

// sidecarPath returns the on-disk path for name's sidecar under metaDir.
func sidecarPath(metaDir, name string) string {
	return filepath.Join(metaDir, EncodeSidecarName(name)+".json")
}

// WriteSidecarExcl creates name's sidecar under metaDir with O_CREATE|
// O_EXCL|O_WRONLY and writes sc as JSON. metaDir must already exist (spec §3
// step 5 assigns that MkdirAll to the caller, as a step distinct from the
// write). O_EXCL is load-bearing: two concurrent same-name creates both pass
// the branch-exists check upstream, and a plain write would let the loser
// clobber the winner's provenance (creator_session/base_sha inversion — spec
// §3 step 5). On a losing race the returned error satisfies os.IsExist.
//
// If the write or close fails after O_EXCL has created the file, that file is
// this call's own half-written reservation and is removed before returning:
// left in place it would be invisible to ListSidecars (undecodable JSON) yet
// block every same-name retry with EEXIST, making the name persistently
// uncreatable until a human deletes it. O_EXCL proves the file is ours, so the
// removal never reclaims another creator's reservation.
func WriteSidecarExcl(metaDir, name string, sc Sidecar) error {
	path := sidecarPath(metaDir, name)
	raw, _ := json.MarshalIndent(sc, "", "  ")
	raw = append(raw, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	created, statErr := f.Stat()
	if _, err := sidecarWrite(f, raw); err != nil {
		_ = f.Close()
		return removeOwnedResidue(path, created, statErr, err)
	}
	if err := f.Close(); err != nil {
		return removeOwnedResidue(path, created, statErr, err)
	}
	return nil
}

// removeOwnedResidue deletes the sidecar at path — the residue of a create
// whose O_EXCL open succeeded but whose write or close failed — and returns
// cause. It removes nothing unless it can confirm the pathname still names the
// file this call created (created, captured from the open handle): if the fresh
// handle had no usable identity, or a concurrent actor unlinked this
// reservation and recreated or replaced the path before cleanup ran, the path
// either cannot be trusted or names someone else's sidecar, and removing it
// would destroy a file this call does not own. Unremoved residue is not lost:
// it is an undecodable reservation that prune sweep 2 surfaces for repair, and
// its own name stays reserved. If the removal itself fails the reservation
// really does persist, so cause is wrapped with that failure rather than
// dropped; the wrap preserves cause for errors.Is/As.
//
// A concurrent actor could still swap the pathname in the two syscalls between
// that confirmation and os.Remove; POSIX offers no unlink-if-inode-matches to
// close it. No evener path performs that swap on a failed create's residue —
// create's O_EXCL fails while the file exists and never deletes it, and every
// collection or update path only removes a sidecar it has already decoded — so
// the swap would take an external actor deleting the file by hand.
func removeOwnedResidue(path string, created os.FileInfo, statErr error, cause error) error {
	if statErr != nil {
		return cause // no identity for the file we created: never remove by path
	}
	current, curErr := os.Stat(path)
	if curErr != nil || !os.SameFile(created, current) {
		return cause // gone, unreadable, or replaced: not our residue to delete
	}
	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("%w (removing partial sidecar %s: %w)", cause, path, rmErr)
	}
	return cause
}

// ErrCorruptSidecar marks a sidecar whose bytes were read but do not decode as
// a Sidecar. It separates a torn record from the other errors ReadSidecar can
// return — a missing file (os.IsNotExist) or a read failure such as a
// permission or I/O error — which are not evidence of corruption and must not
// be classified as one.
var ErrCorruptSidecar = errors.New("worktree: corrupt sidecar")

// ReadSidecar reads and decodes name's sidecar from metaDir. A missing file
// returns an error satisfying os.IsNotExist; a file whose bytes do not decode
// returns an error wrapping ErrCorruptSidecar.
func ReadSidecar(metaDir, name string) (Sidecar, error) {
	path := sidecarPath(metaDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Sidecar{}, err
	}
	var sc Sidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		return Sidecar{}, fmt.Errorf("%w: decode %s: %w", ErrCorruptSidecar, path, err)
	}
	return sc, nil
}

// UpdateSidecar reads name's sidecar, applies mutate, and atomically replaces
// the file with the result (unlike the create path, an update has no loser to
// protect from — the caller already holds exclusive knowledge of the entry
// via git's occupancy lock, so O_EXCL does not apply here). The replacement
// writes a temporary file in metaDir and renames it over the target, so a
// failure partway through — a full disk, a quota limit — leaves the previous
// record intact instead of truncating the only copy of the lane's provenance.
func UpdateSidecar(metaDir, name string, mutate func(*Sidecar)) error {
	sc, err := ReadSidecar(metaDir, name)
	if err != nil {
		return err
	}
	mutate(&sc)
	raw, _ := json.MarshalIndent(sc, "", "  ")
	return replaceSidecar(sidecarPath(metaDir, name), raw)
}

// replaceSidecar atomically replaces the file at path with raw. It first checks
// that the target is writable by this process, preserving the plain-write
// contract a systematic os.WriteFile has: rename needs only directory write
// permission, so without the check a read-only sidecar could be replaced where
// os.WriteFile would refuse. It then writes a securely created temporary file
// beside the target, fsyncs it, closes it, and renames it over path. os.Rename
// replaces an existing target atomically within a directory, so a reader sees
// either the old bytes or the new ones, never a torn mix, and the file fsync
// makes the new bytes durable before the rename so a crash cannot land the name
// on a zero-length or partial inode. The containing directory is then fsynced
// where the platform supports it, so the rename itself survives power loss
// (mirroring writeFileDurably in agent/schema); a filesystem that cannot sync a
// directory at all is tolerated, since the rename has already committed.
//
// The temp is created with os.CreateTemp (a random, O_EXCL name, mode 0600):
// its name cannot be pre-placed or symlinked, two concurrent updates never share
// an inode, and it is 0600 until it carries the target's mode, so a hardened
// sidecar is never briefly world-readable. When the target exists its exact
// permission bits are restored on the temp before the rename; a missing target
// keeps CreateTemp's 0600. The name has no ".json" suffix, so the sidecar
// listings ignore it. A crash between the create and the rename leaves one such
// inert file behind — it reserves no name and no evener path reads it.
func replaceSidecar(path string, raw []byte) error {
	// Preserve os.WriteFile's write-permission enforcement on the target; the
	// open (without truncation) is the probe. A missing target is not an error.
	if target, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = target.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	info, statErr := os.Stat(path)
	tmp, err := os.CreateTemp(dir, ".sidecar-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if statErr == nil {
		if err := tmp.Chmod(info.Mode().Perm()); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return syncDir(dir)
}

// DeleteSidecar removes name's sidecar from metaDir. A missing file returns
// an error satisfying os.IsNotExist.
func DeleteSidecar(metaDir, name string) error {
	return os.Remove(sidecarPath(metaDir, name))
}

// ListSidecars reads every sidecar under metaDir. Entries that are not a
// evener-written sidecar — non-".json" files, subdirectories, ".json" files
// whose basename is not valid EncodeSidecarName output, or ".json" files
// with unparseable content — are silently skipped rather than erroring or
// panicking: an unmanaged file in metaDir is exactly the "unmanaged_meta"
// case spec §6 documents at the tool layer, not a codec failure. Only a
// failure to read metaDir itself is returned as an error.
func ListSidecars(metaDir string) ([]Sidecar, error) {
	out, _, err := ListSidecarsWithErrors(metaDir)
	return out, err
}

// SidecarLoadError is one *.json file in metaDir whose basename decodes to a
// valid worktree name but whose contents could not be decoded as a Sidecar.
// It carries the name recovered from the filename — the only identity a
// corrupt file offers — so a repair or reconciliation sweep can name the
// reserved-but-unusable worktree instead of dropping it silently.
type SidecarLoadError struct {
	Name  string
	Error error
}

// ListSidecarsWithErrors returns every decodable sidecar under metaDir
// together with the load failures for files whose basename decodes to a valid
// worktree name but whose bytes do not decode as a Sidecar. ListSidecars
// tolerates a corrupt record by dropping it (an unmanaged file in metaDir is
// the "unmanaged_meta" case spec §6 documents at the tool layer); a repair or
// reconciliation sweep that must report a reserved-but-unusable name cannot
// use that tolerant view, so it uses this enumeration instead. Only a decode
// failure is a load error: a missing file (a concurrent delete) or an
// unreadable one (a permission or I/O error) is not evidence of a torn record
// and is skipped, preserving ListSidecars' tolerant contract. Only a failure
// to read metaDir itself is returned as an error.
func ListSidecarsWithErrors(metaDir string) ([]Sidecar, []SidecarLoadError, error) {
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return nil, nil, err
	}
	var (
		out      []Sidecar
		failures []SidecarLoadError
	)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		encoded, ok := strings.CutSuffix(filename, ".json")
		if !ok {
			continue
		}
		name, ok := DecodeSidecarName(encoded)
		if !ok {
			continue
		}
		sc, err := ReadSidecar(metaDir, name)
		if err != nil {
			if errors.Is(err, ErrCorruptSidecar) {
				failures = append(failures, SidecarLoadError{Name: name, Error: err})
			}
			continue
		}
		out = append(out, sc)
	}
	return out, failures, nil
}

// CorruptStaleReservation returns name's sidecar decode error when the file at
// name is an undecodable record older than grace — the residue of a create
// that died between its O_EXCL open and its write — and nil otherwise. Two
// cases that are not corruption yield nil: a file younger than grace may be a
// live concurrent create the winner has opened but not yet written, and a
// missing or unreadable file (a concurrent delete, a permission or I/O error)
// is not a torn record. Callers use it to tell a genuinely repairable torn
// reservation from a live one instead of misdirecting repair at a reserved
// name that is still being written.
func CorruptStaleReservation(metaDir, name string, grace time.Duration) error {
	_, readErr := ReadSidecar(metaDir, name)
	if !errors.Is(readErr, ErrCorruptSidecar) {
		return nil
	}
	age, ageErr := SidecarAge(metaDir, name)
	return corruptReservationError(readErr, age, ageErr, grace)
}

// corruptReservationError decides whether a corrupt sidecar read is a
// repairable stale reservation. readErr is the ErrCorruptSidecar read result;
// age and ageErr are the subsequent SidecarAge probe. A file that vanished
// between the read and the probe (os.IsNotExist) is indeterminate rather than
// torn, and a file younger than grace may be a live create caught mid-write, so
// neither is reported; any other probe failure is surfaced as corruption.
func corruptReservationError(readErr error, age time.Duration, ageErr error, grace time.Duration) error {
	if os.IsNotExist(ageErr) {
		return nil
	}
	if ageErr == nil && age < grace {
		return nil
	}
	return readErr
}

// SidecarAge returns how long ago name's sidecar file was last written,
// judged by the file's mtime on the shared filesystem (spec §5 sweep 2) —
// never the sidecar's recorded CreatedAt field, which is stamped by the
// creator's clock and would defeat the grace under cross-machine skew on a
// shared state directory.
func SidecarAge(metaDir, name string) (time.Duration, error) {
	info, err := os.Stat(sidecarPath(metaDir, name))
	if err != nil {
		return 0, err
	}
	return time.Since(info.ModTime()), nil
}
