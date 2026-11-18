package transcriptindex

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sidecarSuffix starts the name of every sidecar directory beside a transcript.
const sidecarSuffix = ".index"

// DirFor is the sidecar directory the daemon and the hub share for a
// transcript: the location every Open of that transcript's index must agree
// on, so a rebuild by one is visible to the other. The name carries this
// binary's projection, so a daemon still running an older build keeps its own
// sidecar instead of trading rebuilds with the hub over a shared one.
func DirFor(transcriptPath string) string {
	sum := sha256.Sum256([]byte(currentProjection()))
	return transcriptPath + sidecarSuffix + "-" + hex.EncodeToString(sum[:4])
}

// SidecarDirs lists every sidecar directory beside a transcript, whichever
// projection built it, for deleting a transcript's derived data. A transcript
// with none lists none.
func SidecarDirs(transcriptPath string) ([]string, error) {
	parent, prefix := filepath.Dir(transcriptPath), filepath.Base(transcriptPath)+sidecarSuffix
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, entry := range entries {
		if name := entry.Name(); name == prefix || strings.HasPrefix(name, prefix+"-") {
			dirs = append(dirs, filepath.Join(parent, entry.Name()))
		}
	}
	return dirs, nil
}

// supersededGrace is how long another projection's sidecar must have gone
// unused before a rebuild empties it. A sidecar is derived data, so reclaiming
// one a binary still wanted costs that binary one rebuild from the transcript
// — which is why the grace period is generous rather than tight.
//
// A handle refreshes the stamp while it is in use (see noteSidecarUse), so
// this measures how long a sidecar has gone untouched rather than how long
// since it was last extended: a binary that still serves reads from its own
// projection keeps its sidecar, and only one that has stopped talking to the
// transcript entirely lets it age out. A binary built before that stamp cannot
// refresh it at all, which is the whole reason the period is measured in
// weeks: it must outlast any daemon plausibly still running an older build.
const supersededGrace = 30 * 24 * time.Hour

// sidecarUseInterval is how often a handle's operations refresh its sidecar's
// use stamp. Every read of a hot session would otherwise cost a write; the
// interval is far below supersededGrace, so a live handle stays comfortably
// inside it.
const sidecarUseInterval = time.Hour

// noteSidecarUse stamps the sidecar as used, at most once per
// sidecarUseInterval per handle: the lock file's modification time is the
// stamp, because it is the sidecar's one file every handle already opens.
// A failure is ignored — losing a stamp can only make the sidecar look older,
// and the rebuild that reclaims it rebuilds from the transcript.
func (x *Index) noteSidecarUse() {
	if x.readView || x.dir == "" {
		return
	}
	now := time.Now()
	if now.Sub(x.usedAt) < sidecarUseInterval {
		return
	}
	x.usedAt = now
	lock := filepath.Join(x.dir, lockFileName)
	_ = os.Chtimes(lock, now, now)
}

// supersededPruneCandidates lists the sidecars beside this handle's transcript
// that belong to another projection, have gone unused past supersededGrace, and
// are idle right now: the sidecars a rebuild may reclaim.
//
// A rebuild computes this before it commits, because only the candidates known
// before the commit are covered by the durability that commit establishes. A
// sidecar that only becomes a candidate later waits for the next rebuild, which
// is what the grace period is for anyway.
//
// This runs on a rebuild, never on a read: a rebuild already scans the whole
// transcript, so one listing of the transcript's own directory and a walk of
// each candidate's few files is bounded work beside it.
func (x *Index) supersededPruneCandidates() []string {
	dirs, err := SidecarDirs(x.path)
	if err != nil {
		return nil
	}
	current, deadline := DirFor(x.path), time.Now().Add(-supersededGrace)
	var candidates []string
	for _, dir := range dirs {
		// This handle's own sidecar is never a candidate, whichever directory
		// its caller gave it, and neither is the one this binary's projection
		// owns: another live handle of this build may be using it right now.
		if dir == x.dir || dir == current {
			continue
		}
		if !sidecarUnusedSince(dir, deadline) || !sidecarIdle(dir) {
			continue
		}
		candidates = append(candidates, dir)
	}
	return candidates
}

// pruneSupersededSidecars reclaims the sidecars supersededPruneCandidates
// named, once this rebuild's own build and pointer are durable. Until then the
// superseded sidecar may be the only index the install has, so the ordering is
// the safety property: a crash between the commit and the reclaim leaves the
// new build serving and an old sidecar still on disk, and the next rebuild
// reclaims it.
//
// Each sidecar is emptied rather than removed: the directory and its lock file
// stay, so a handle that comes back rebuilds into the same directory and
// serializes on the same lock inode instead of racing a second one into
// existence. Every failure is skipped rather than reported: reclaiming derived
// disk space must not fail a rebuild that has already committed.
func (x *Index) pruneSupersededSidecars(candidates []string) {
	for _, dir := range candidates {
		// The age and idleness checks are repeated here: a rebuild takes long
		// enough that the projection's own binary can wake up and start using
		// the sidecar between the scan and this point.
		if !sidecarUnusedSince(dir, time.Now().Add(-supersededGrace)) || !sidecarIdle(dir) {
			continue
		}
		emptySupersededSidecar(dir)
	}
}

// sidecarUnusedSince reports whether every file in the sidecar was last
// written before deadline. A build commits on every extension — it renames a
// meta.json inside its build directory — so the newest file is the sidecar's
// last use, and a binary still extending it keeps it fresh. An unreadable
// sidecar reads as used, leaving it for a later rebuild.
func sidecarUnusedSince(dir string, deadline time.Time) bool {
	unused := true
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(deadline) {
			unused = false
			return fs.SkipAll
		}
		return nil
	})
	return err == nil && unused
}

// sidecarIdle reports whether no handle, in this process or another, is in an
// operation on the sidecar right now: taking its exclusive lock without waiting
// succeeds only when no reader or writer holds it. A sidecar with no lock file
// is idle by definition. Any other error reads as busy, which keeps the
// sidecar rather than deleting one whose state cannot be established.
func sidecarIdle(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR, 0o600)
	if err != nil {
		return os.IsNotExist(err)
	}
	defer func() { _ = f.Close() }()
	if err := lockFileTry(f, true); err != nil {
		return false
	}
	// Closing releases the lock even when the explicit unlock faults.
	_ = unlockFile(f)
	return true
}

// emptySupersededSidecar deletes everything in a superseded sidecar but its
// lock file. CURRENT goes before the builds, so an interrupted emptying never
// leaves a pointer naming a half-deleted build: with no CURRENT either way,
// the next handle finds no live build and rebuilds from the transcript.
func emptySupersededSidecar(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	if err := os.Remove(filepath.Join(dir, currentFileName)); err != nil && !os.IsNotExist(err) {
		return
	}
	for _, entry := range entries {
		if entry.Name() == lockFileName {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}
