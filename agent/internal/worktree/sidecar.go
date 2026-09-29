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
	if _, err := sidecarWrite(f, raw); err != nil {
		_ = f.Close()
		return removePartialCreate(path, err)
	}
	if err := f.Close(); err != nil {
		return removePartialCreate(path, err)
	}
	return nil
}

// removePartialCreate deletes the sidecar at path — the residue of a create
// whose O_EXCL open succeeded but whose write or close failed — and returns
// cause. The caller owns path by construction, so the removal is the opposite
// of reclaiming a competing reservation. If the removal itself fails the
// reservation really does persist, so cause is wrapped with that failure
// rather than dropped; the wrap preserves cause for errors.Is/As.
func removePartialCreate(path string, cause error) error {
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

// replaceSidecar atomically replaces the file at path with raw: it writes a
// temporary file in the destination directory, closes it, and renames it over
// path. os.Rename replaces an existing target atomically within a directory,
// so a reader sees either the old bytes or the new ones, never a torn mix. The
// replacement keeps the target's existing permission bits (a systematic
// os.WriteFile would too); only a missing target falls back to 0o644. The
// temporary name carries a non-".json" suffix so ListSidecars, which only
// considers ".json" files, ignores it while it exists; a crash between the
// create and the rename leaves one such inert file behind — it reserves no
// name and is invisible to every listing, so it needs no in-band cleanup.
func replaceSidecar(path string, raw []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sidecar-tmp-*")
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
	if err := tmp.Chmod(mode); err != nil {
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
	return nil
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
	_, err := ReadSidecar(metaDir, name)
	if err == nil || !errors.Is(err, ErrCorruptSidecar) {
		return nil
	}
	if age, ageErr := SidecarAge(metaDir, name); ageErr == nil && age < grace {
		return nil
	}
	return err
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
