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

// SessionTreeStamp summarizes the on-disk state the past index's rebuild reads,
// without reading any of it: one directory read per project and one lstat per
// session entry.
//
// It exists so a sweep tick can tell "nothing on disk moved since the last
// tick" from "something did" for a small fraction of the cost of re-reading
// every session's metadata. The rebuild's whole input is the sessions'
// *.meta.json files (schema.ListSessionMetas, scanAll), so a digest over
// exactly those files is a proof over exactly its input: a change the digest
// cannot see is a change the rebuild could not observe either.
//
// It is NOT a proof for MessageSearch.Refresh, and the hub does not use it as
// one: Refresh runs unconditionally. Refresh needs a transcript's read to
// succeed, and it defers a session without recording a stamp when that read is
// refused for a missing sidecar, a contended lock or a stale cursor — none of
// which moves any file the digest folds, because the sidecar the repair then
// builds lives in the excluded .index-<projection> directory. Transcripts are
// therefore not folded at all: the rebuild never reads one, so folding them
// would buy a full rebuild on every appended turn and find nothing.
//
// The digest covers, per project directory the glob matches:
//
//   - the project path itself, so a project appearing or disappearing counts;
//   - every *.meta.json entry's name, size and modification time.
//
// That is the rebuild's entire input. The transcripts, api and job logs, boot
// generations and meta locks beside the metas all move on ordinary turns
// without changing what the rebuild reads, so folding them in would defeat the
// digest rather than sharpen it.
//
// Every writer of a covered file moves at least one folded fact: a covered
// entry's name, its size or its modification time. A meta write is a temp file
// renamed into place (schema.writeSessionMetaLocked), which installs a fresh
// inode with a new modification time. A transcript-index rebuild writes only
// inside its own .index-<projection> directory, so it correctly does not move
// the digest.
//
// An entry that cannot be stat'd is folded in by name with a fixed marker, so a
// file appearing or disappearing still moves the digest and a transient stat
// failure cannot silently read as "unchanged": the marker differs from every
// real (size, mtime) pair.
//
// The transcript-index sidecar directories are deliberately excluded: they are
// derived data, and a rebuild writes inside the sidecar directory only.
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
// It reads the directory through a raw handle and calls Readdirnames rather
// than os.ReadDir: os.ReadDir builds a DirEntry for every entry and stats the
// ones it cannot type from the directory entry, which on a sessions dir
// dominated by per-turn artifacts (api logs, job logs, sidecars) costs several
// times what this whole digest should. Readdirnames returns the names and
// nothing else, so only the handful of entries this function keeps are stat'd.
func foldSessionDirStamp(h hash.Hash64, sessionsDir string) {
	dir, err := os.Open(sessionsDir)
	if err != nil {
		// A missing or unreadable sessions dir is itself a state change: fold a
		// marker so a dir that appears or disappears moves the digest.
		foldStampString(h, "\x00unreadable")
		return
	}
	defer func() { _ = dir.Close() }()
	// Names only: an entry's type is not needed, so the lstat below is left to
	// the kept entries and every skipped artifact costs a name comparison.
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

// stampWatchesSessionEntry reports whether a sessions-dir entry is part of the
// past index rebuild's input.
func stampWatchesSessionEntry(name string) bool {
	// A writer's intermediate "<id>.meta.json.tmp" does not match this suffix,
	// and does not need to: the rename that installs the real file moves its own
	// entry within the same write.
	return strings.HasSuffix(name, schema.SessionMetaSuffix)
}

func foldStampString(h hash.Hash64, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
