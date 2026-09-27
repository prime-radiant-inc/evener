package bucketref

import (
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/identifier"
)

// Bucket is a resolved project-bucket directory. ProjectID is the directory
// name under <stateHome>/evener/projects (the bucket identity on disk — any
// name, including legacy- or foreign-named ones, never filtered to
// identifier.ValidateProjectID's alphabet).
type Bucket struct {
	Dir       string
	ProjectID string
}

// SymlinkPolicy selects how EnumerateBuckets treats a bucket directory that is
// itself a symlink. The agent and doctor deliberately diverge here (owner
// ruling on #2275: "this is a user tool. allow symlinked buckets."), so the
// shared enumeration parameterizes the traversal rather than choosing one.
type SymlinkPolicy int

const (
	// FollowSymlinks treats a symlinked bucket dir like any other directory:
	// it is enumerated. This is the doctor's policy — a user-facing diagnostic
	// tool that must see symlinked state layouts an operator has wired up.
	FollowSymlinks SymlinkPolicy = iota
	// RefuseSymlinks skips a symlinked bucket dir entirely (it never enters the
	// result). This is the agent's policy — model-facing transcript read paths
	// hold the higher bar, since a symlink under projects/ could point outside
	// the state root and expose transcripts from elsewhere (#2205).
	RefuseSymlinks
)

// Options configures EnumerateBuckets. The zero value enumerates with
// filepath.Glob and follows symlinks (the doctor's behavior); callers pass
// WithGlob and/or WithSymlinkPolicy to match their own policy.
type Options struct {
	glob          func(string) ([]string, error)
	symlinkPolicy SymlinkPolicy
}

// WithGlob overrides the glob used to list entries under projects/ (default
// filepath.Glob). Both the agent and doctor read their own package-level glob
// variable at call time and pass it here, so each component's test override
// hook (agent transcriptBucketGlob, doctor globProjectBuckets) keeps working.
func WithGlob(g func(string) ([]string, error)) func(*Options) {
	return func(o *Options) { o.glob = g }
}

// WithSymlinkPolicy selects the symlink traversal policy. The agent passes
// RefuseSymlinks; the doctor passes FollowSymlinks.
func WithSymlinkPolicy(p SymlinkPolicy) func(*Options) {
	return func(o *Options) { o.symlinkPolicy = p }
}

// EnumerateBuckets lists the bucket directories under projects/ (i.e. under
// <stateHome>/evener/projects), in glob order. Every directory is a bucket:
// bucket identity is the actual directory name, and filtering on
// identifier.ValidateProjectID would hide a legacy- or foreign-named bucket
// that holds real sessions — a sweep must see what is on disk. Refs for
// grammar-incompatible bucket names are suppressed at emission time by RefFor,
// not here.
//
// Symlink handling is per WithSymlinkPolicy: FollowSymlinks (default) includes
// a symlinked bucket dir; RefuseSymlinks skips it. Per-entry stat errors are
// SKIPPED (not propagated) under both policies — matching today's behavior on
// both sides (the agent skips on Lstat error; the doctor's isDir skips on Stat
// error) — so a transiently-unreadable entry never aborts the sweep.
//
// The caller is responsible for any prefix guards (symlinked evener/ or
// evener/projects/ ancestors, pattern well-formedness); this function only
// filters the glob's matches. Glob errors are returned already wrapped (as
// "glob project buckets: %w"); the caller is responsible only for the prefix
// guards and pattern well-formedness.
func EnumerateBuckets(projects string, opts ...func(*Options)) ([]Bucket, error) {
	o := Options{glob: filepath.Glob, symlinkPolicy: FollowSymlinks}
	for _, opt := range opts {
		opt(&o)
	}
	matches, err := o.glob(filepath.Join(projects, "*"))
	if err != nil {
		return nil, fmt.Errorf("glob project buckets: %w", err)
	}
	buckets := make([]Bucket, 0, len(matches))
	for _, m := range matches {
		info, statErr := os.Lstat(m)
		if statErr != nil {
			continue // per-entry stat error → skip, do not propagate
		}
		if o.symlinkPolicy == RefuseSymlinks && info.Mode()&os.ModeSymlink != 0 {
			continue // symlinked bucket dir → skip under RefuseSymlinks
		}
		if !info.IsDir() {
			// Under FollowSymlinks a symlink-to-a-dir reports IsDir true
			// (Lstat on the symlink returns the symlink's mode, but the
			// doctor's isDir uses os.Stat which follows; match that by
			// re-checking with Stat when following). Under RefuseSymlinks a
			// symlink was already skipped above, so a non-dir here is a plain
			// file and is skipped.
			if o.symlinkPolicy == FollowSymlinks && info.Mode()&os.ModeSymlink != 0 {
				if s, e := os.Stat(m); e == nil && s.IsDir() {
					buckets = append(buckets, Bucket{Dir: m, ProjectID: filepath.Base(m)})
					continue
				}
			}
			continue
		}
		buckets = append(buckets, Bucket{Dir: m, ProjectID: filepath.Base(m)})
	}
	return buckets, nil
}

// RefFor builds the transcript ref: proj:<project-id>:<sid> for a project
// bucket, or local:<sid> for an override / scratch root (empty projectID). A
// bucket whose directory name the shared agent ref grammar cannot consume gets
// NO ref — identifier.ValidateProjectID is exactly the token the agent-side
// transcript tools' parsers admit, so a ref emitted for any other name would
// hand the model a handle read_transcript and find_session_transcripts reject.
// Such sessions stay fully locatable (session id, bucket, paths) and remain
// addressable through the doctor's own selector grammar — a bare id sweeps to
// them, and an explicit proj:<name>:<sid> parses for every traversal-safe
// name. Both the agent and the doctor call this; their refFor helpers are now
// thin wrappers so the two cannot diverge on ref formatting.
func RefFor(projectID, sid string) string {
	if projectID == "" {
		return LocalScheme + sid
	}
	if identifier.ValidateProjectID(projectID) != nil {
		return ""
	}
	return ProjScheme + projectID + ":" + sid
}
