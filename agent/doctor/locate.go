package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bucket is a resolved project bucket directory. hash is the 16-hex bucket name
// when the bucket lives under serf/projects/, and "" for an override / scratch
// root whose sessions/ sit directly under the state base.
type bucket struct {
	dir  string
	hash string
}

// Locate resolves a session selector to its absolute on-disk paths within the
// given state base. It is the shared resolver the other subcommands reuse, so
// there is no second selector dialect.
//
// stateBase is the already-resolved state root (the cmd layer applies the
// --state-dir / SERF_STATE_DIR / XDG precedence). Locate auto-detects whether
// stateBase is an XDG state home (it contains serf/projects/<hash> buckets) or
// is itself a single bucket (an override / E2E scratch root with sessions/
// directly under it). It resolves by globbing the on-disk layout — it never
// recomputes a bucket hash.
func Locate(stateBase, selector string) (Paths, error) {
	sel, err := parseSelector(selector)
	if err != nil {
		return Paths{}, err
	}
	buckets, err := resolveBuckets(stateBase)
	if err != nil {
		return Paths{}, err
	}

	if sel.hash != "" {
		return locateInBucket(stateBase, buckets, sel)
	}
	return locateAcrossBuckets(stateBase, buckets, sel)
}

// locateInBucket resolves a proj:<hash>:<sid> selector to its named bucket.
func locateInBucket(stateBase string, buckets []bucket, sel selector) (Paths, error) {
	for _, b := range buckets {
		if b.hash == sel.hash {
			if !sessionInBucket(b, sel.sid) {
				return Paths{}, fmt.Errorf("session %s not found in bucket %s", sel.sid, sel.hash)
			}
			return pathsFor(b, sel.sid), nil
		}
	}
	// Bucket was not enumerated (e.g. addressed directly under an XDG home that
	// has no other buckets yet); construct the path and verify it exists.
	b := bucket{dir: filepath.Join(stateBase, "serf", "projects", sel.hash), hash: sel.hash}
	if sessionInBucket(b, sel.sid) {
		return pathsFor(b, sel.sid), nil
	}
	return Paths{}, fmt.Errorf("session %s not found in bucket %s", sel.sid, sel.hash)
}

// locateAcrossBuckets resolves a bare <sid> (or local:<sid>) by searching every
// bucket; a sid present in more than one bucket is reported as ambiguous.
func locateAcrossBuckets(stateBase string, buckets []bucket, sel selector) (Paths, error) {
	var found []bucket
	for _, b := range buckets {
		if sessionInBucket(b, sel.sid) {
			found = append(found, b)
		}
	}
	switch len(found) {
	case 0:
		return Paths{}, fmt.Errorf("session %s not found under %s", sel.sid, stateBase)
	case 1:
		return pathsFor(found[0], sel.sid), nil
	default:
		hashes := make([]string, 0, len(found))
		for _, b := range found {
			hashes = append(hashes, b.hash)
		}
		sort.Strings(hashes)
		return Paths{}, fmt.Errorf("session %s is ambiguous across %d buckets: %s (disambiguate with proj:<hash>:%s)",
			sel.sid, len(found), strings.Join(hashes, ", "), sel.sid)
	}
}

// resolveBuckets returns the project buckets under stateBase, auto-detecting the
// layout: an XDG state home enumerates serf/projects/*, while an override /
// scratch root (sessions/ directly under it) is itself the single bucket.
func resolveBuckets(stateBase string) ([]bucket, error) {
	projects := filepath.Join(stateBase, "serf", "projects")
	if isDir(projects) {
		matches, err := filepath.Glob(filepath.Join(projects, "*"))
		if err != nil {
			return nil, fmt.Errorf("glob project buckets: %w", err)
		}
		buckets := make([]bucket, 0, len(matches))
		for _, m := range matches {
			if isDir(m) {
				buckets = append(buckets, bucket{dir: m, hash: filepath.Base(m)})
			}
		}
		return buckets, nil
	}
	// Override / scratch layout: stateBase is itself the bucket.
	return []bucket{{dir: stateBase, hash: ""}}, nil
}

// pathsFor builds the resolved Paths for a session in a bucket. The jobs.jsonl
// path is the per-session SUBDIR form (sessions/<sid>/jobs.jsonl), never a
// suffix on the transcript path.
func pathsFor(b bucket, sid string) Paths {
	sess := filepath.Join(b.dir, "sessions")
	return Paths{
		SessionID:      sid,
		TranscriptRef:  refFor(b.hash, sid),
		BucketHash:     b.hash,
		BucketDir:      b.dir,
		TranscriptPath: filepath.Join(sess, sid+".transcript.jsonl"),
		MetaPath:       filepath.Join(sess, sid+".meta.json"),
		JobsPath:       filepath.Join(sess, sid, "jobs.jsonl"),
	}
}

// refFor builds the transcript ref: proj:<hash>:<sid> when the bucket is hashed,
// else local:<sid> for an override / scratch root.
func refFor(hash, sid string) string {
	if hash == "" {
		return "local:" + sid
	}
	return "proj:" + hash + ":" + sid
}

// sessionInBucket reports whether the session's transcript file is present in b.
func sessionInBucket(b bucket, sid string) bool {
	return fileExists(filepath.Join(b.dir, "sessions", sid+".transcript.jsonl"))
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
