// Package doctor is the data plane of serf's on-demand doctoring system: a
// read-only forensic reader over a session's settled on-disk state (transcript,
// meta, jobs.jsonl). It lives under agent/ so it can import the internal
// jobstore folds, and it imports ONLY the durable-format packages it reads —
// jobstore, provenance, transcript, schema — never the agent session/runtime.
// That keeps the serf-doctor binary a lean, type-coupled reader: a schema change
// either flows through automatically or fails to compile. There is no second
// implementation of the on-disk format to drift.
//
// The cmd/serf-doctor binary is a thin main over this package.
package doctor

// Paths is the resolved on-disk location set for one session.
//
// Note the asymmetry the §8 correction pinned down: transcript and meta are
// flat, SID-prefixed files directly under sessions/, while jobs.jsonl lives in a
// per-session SUBDIR (sessions/<sid>/jobs.jsonl).
type Paths struct {
	SessionID      string `json:"session_id"`
	TranscriptRef  string `json:"transcript_ref"`  // proj:<hash>:<sid>, or local:<sid> in an override/scratch root
	BucketHash     string `json:"bucket_hash"`     // bucket dir name (16 hex) under serf/projects/, else ""
	BucketDir      string `json:"-"`               // absolute bucket dir (internal pivot for other subcommands)
	TranscriptPath string `json:"transcript_path"` // <bucket>/sessions/<sid>.transcript.jsonl
	MetaPath       string `json:"meta_path"`       // <bucket>/sessions/<sid>.meta.json
	JobsPath       string `json:"jobs_path"`       // <bucket>/sessions/<sid>/jobs.jsonl  (SUBDIR)
}

// validToken reports whether s is a bare identifier safe to splice into a path:
// non-empty, and limited to the characters serf session ids and bucket hashes
// use. It rejects path separators and dots so a selector can never traverse out
// of the state root.
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
