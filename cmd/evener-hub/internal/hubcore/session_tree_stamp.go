package hubcore

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// SessionTreeStamp summarizes the on-disk state both hub sweeps read, without
// reading any of it: one directory read per project and one lstat per session
// entry.
//
// It exists so a sweep tick can tell "nothing on disk moved since the last
// tick" from "something did" for a small fraction of the cost of re-reading
// every session's metadata and transcript. Session files are what the past
// index's Rebuild scans and what MessageSearch.Refresh stamps, so a digest over
// exactly those files is a proof over exactly the inputs of both: a change the
// digest cannot see is a change neither sweep could observe either.
//
// The digest covers, per project directory the glob matches:
//
//   - the project path itself, so a project appearing or disappearing counts;
//   - every *.meta.json entry's name, size and modification time — the past
//     index's entire input;
//   - every session transcript's name, size and modification time — what
//     MessageSearch.Refresh compares to decide a transcript changed.
//
// Only a session transcript counts (see sessionTranscriptName): the api and job
// logs beside it, the boot generations and the meta locks all move on ordinary
// turns without changing what either index holds, so folding them in would
// defeat the digest rather than sharpen it.
//
// Every writer of a covered file moves at least one of the three folded facts.
// A meta write is a temp file renamed into place (schema.writeSessionMetaLocked),
// which installs a fresh inode with a new modification time; a transcript append
// extends the file; a transcript rewrite or a transcript-index rebuild replaces
// it. Digests of this shape are the same stamp MessageSearch.Refresh already
// trusts per transcript (TranscriptStamp), applied to the whole tree.
//
// An entry that cannot be stat'd is folded in by name with a fixed marker, so a
// file appearing or disappearing still moves the digest and a transient stat
// failure cannot silently read as "unchanged": the marker differs from every
// real (size, mtime) pair.
//
// The transcript-index sidecar directories are deliberately excluded. A sidecar
// rebuild changes what a *first* read of a transcript would see, but Refresh
// stamps the transcript file, so it already declines to re-read a transcript
// whose file did not move; folding the sidecars in would only make the digest
// fire on every append, including the hub's own refresh.
func SessionTreeStamp(projectGlob string) (uint64, error) {
	if projectGlob == "" {
		return 0, nil
	}
	projects, err := filepath.Glob(projectGlob)
	if err != nil {
		return 0, err
	}
	h := fnv.New64a()
	for _, project := range projects {
		if identifier.ValidateProjectID(filepath.Base(project)) != nil {
			// Rebuild refuses a project whose directory name is not a project
			// id, so nothing under it can reach either index.
			continue
		}
		foldStampString(h, project)
		foldSessionDirStamp(h, schema.SessionsDir(project))
	}
	return h.Sum64(), nil
}

// foldSessionDirStamp folds one sessions directory's covered entries into h.
//
// It reads the directory through a raw handle and calls Readdir rather than
// os.ReadDir: os.ReadDir stats every entry and builds a FileInfo for each,
// which on a sessions dir dominated by per-turn artifacts (api logs, job logs,
// sidecars) costs several times what this whole digest should. Readdir hands
// back dirents that already carry the name, and only the handful of entries
// this function keeps are stat'd.
func foldSessionDirStamp(h hash.Hash64, sessionsDir string) {
	dir, err := os.Open(sessionsDir)
	if err != nil {
		// A missing or unreadable sessions dir is itself a state change: fold a
		// marker so a dir that appears or disappears moves the digest.
		foldStampString(h, "\x00unreadable")
		return
	}
	defer func() { _ = dir.Close() }()
	// Readdirnames, not Readdir: a dirent's type is not needed, and this leaves
	// the lstat entirely to the os.Lstat below, for the kept entries only.
	names, err := dir.Readdirnames(-1)
	if err != nil {
		foldStampString(h, "\x00unreadable")
		return
	}
	// The order readdir returns is the filesystem's and is not stable across a
	// directory's mutations, so sort before folding.
	slices.Sort(names)
	for _, name := range names {
		if !stampWatchesSessionEntry(name) {
			continue
		}
		foldStampString(h, name)
		info, err := os.Lstat(filepath.Join(sessionsDir, name))
		if err != nil {
			foldStampString(h, "\x00statfailed")
			continue
		}
		var b [16]byte
		binary.LittleEndian.PutUint64(b[0:8], uint64(info.Size()))
		binary.LittleEndian.PutUint64(b[8:16], uint64(info.ModTime().UnixNano()))
		_, _ = h.Write(b[:])
	}
}

// stampWatchesSessionEntry reports whether a sessions-dir entry feeds one of
// the sweeps the stamp gates.
func stampWatchesSessionEntry(name string) bool {
	if strings.HasSuffix(name, schema.SessionMetaSuffix) {
		// The past index's input. A writer's intermediate "<id>.meta.json.tmp"
		// does not match this suffix, and does not need to: the rename that
		// installs the real file moves its own entry within the same write.
		return true
	}
	return sessionTranscriptName(name)
}

// sessionTranscriptName reports whether name is a session's own transcript
// file. The transcript index's sidecars live beside it under a longer name
// (<transcript>.index-<projection>), which this refuses, as it does every other
// per-session artifact.
func sessionTranscriptName(name string) bool {
	if !strings.HasSuffix(name, schema.SessionTranscriptSuffix) {
		return false
	}
	id := strings.TrimSuffix(name, schema.SessionTranscriptSuffix)
	return identifier.ValidateSessionID(id) == nil
}

func foldStampString(h hash.Hash64, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
