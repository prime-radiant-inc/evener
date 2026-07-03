package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
func WriteSidecarExcl(metaDir, name string, sc Sidecar) error {
	path := sidecarPath(metaDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sc); err != nil {
		_ = f.Close()
		return fmt.Errorf("worktree: encode sidecar %s: %w", path, err)
	}
	return f.Close()
}

// ReadSidecar reads and decodes name's sidecar from metaDir. A missing file
// returns an error satisfying os.IsNotExist; malformed JSON returns the
// json.Unmarshal error.
func ReadSidecar(metaDir, name string) (Sidecar, error) {
	path := sidecarPath(metaDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Sidecar{}, err
	}
	var sc Sidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		return Sidecar{}, fmt.Errorf("worktree: decode sidecar %s: %w", path, err)
	}
	return sc, nil
}

// UpdateSidecar reads name's sidecar, applies mutate, and writes the result
// back with a plain truncating write (unlike the create path, an update has
// no loser to protect from — the caller already holds exclusive knowledge
// of the entry via git's occupancy lock, so O_EXCL does not apply here).
func UpdateSidecar(metaDir, name string, mutate func(*Sidecar)) error {
	sc, err := ReadSidecar(metaDir, name)
	if err != nil {
		return err
	}
	mutate(&sc)
	raw, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("worktree: encode sidecar for %s: %w", name, err)
	}
	return os.WriteFile(sidecarPath(metaDir, name), raw, 0o644)
}

// DeleteSidecar removes name's sidecar from metaDir. A missing file returns
// an error satisfying os.IsNotExist.
func DeleteSidecar(metaDir, name string) error {
	return os.Remove(sidecarPath(metaDir, name))
}

// ListSidecars reads every sidecar under metaDir. Entries that are not a
// serf-written sidecar — non-".json" files, subdirectories, ".json" files
// whose basename is not valid EncodeSidecarName output, or ".json" files
// with unparseable content — are silently skipped rather than erroring or
// panicking: an unmanaged file in metaDir is exactly the "unmanaged_meta"
// case spec §6 documents at the tool layer, not a codec failure. Only a
// failure to read metaDir itself is returned as an error.
func ListSidecars(metaDir string) ([]Sidecar, error) {
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return nil, err
	}
	var out []Sidecar
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
			continue
		}
		out = append(out, sc)
	}
	return out, nil
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
