package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/identifier"
)

var globProjectBuckets = filepath.Glob

// bucket is a resolved project bucket directory. projectID is the directory key
// when the bucket lives under evener/projects/, and "" for an override / scratch
// root whose sessions/ sit directly under the state base.
type bucket struct {
	dir       string
	projectID string
}

// Locate resolves a session selector to its absolute on-disk paths within the
// given state base. It is the shared resolver the other subcommands reuse, so
// there is no second selector dialect.
//
// stateBase is the already-resolved state root (the cmd layer applies the
// --state-dir / EVENER_STATE_DIR / XDG precedence). Locate auto-detects the
// base's shape via resolveBuckets: an XDG state home (it contains
// evener/projects/<project-id> buckets), a project bucket directory itself
// (the daemon's per-session state dir — the sweep still covers every sibling
// bucket under its state home), or a single override / scratch bucket
// (sessions/ directly under it). It resolves by globbing the on-disk layout —
// it never recomputes a project ID.
func Locate(stateBase, selector string) (Paths, error) {
	sel, err := parseSelector(selector)
	if err != nil {
		return Paths{}, err
	}
	buckets, stateRoot, err := resolveBuckets(stateBase)
	if err != nil {
		return Paths{}, err
	}

	if sel.projectID != "" {
		return locateInBucket(buckets, stateRoot, sel)
	}
	return locateAcrossBuckets(buckets, stateRoot, sel)
}

// locateInBucket resolves a proj:<project-id>:<sid> selector to its named
// bucket. A project id that names no enumerated bucket is an explicit
// not-found error carrying the state root and the scan breadth — never a
// silent empty result.
func locateInBucket(buckets []bucket, stateRoot string, sel selector) (Paths, error) {
	for _, b := range buckets {
		if b.projectID == sel.projectID {
			if !sessionInBucket(b, sel.sid) {
				return Paths{}, fmt.Errorf("session %s not found in project %s under %s", sel.sid, sel.projectID, stateRoot)
			}
			return pathsFor(b, sel.sid), nil
		}
	}
	return Paths{}, fmt.Errorf("project %s not found under %s (%d %s scanned)",
		sel.projectID, stateRoot, len(buckets), plural(len(buckets), "bucket"))
}

// locateAcrossBuckets resolves a bare <sid> (or local:<sid>) by searching every
// bucket; a sid present in more than one bucket is reported as ambiguous.
func locateAcrossBuckets(buckets []bucket, stateRoot string, sel selector) (Paths, error) {
	var found []bucket
	for _, b := range buckets {
		if sessionInBucket(b, sel.sid) {
			found = append(found, b)
		}
	}
	switch len(found) {
	case 0:
		return Paths{}, fmt.Errorf("session %s not found under %s (%d %s scanned)",
			sel.sid, stateRoot, len(buckets), plural(len(buckets), "bucket"))
	case 1:
		return pathsFor(found[0], sel.sid), nil
	default:
		projectIDs := make([]string, 0, len(found))
		for _, b := range found {
			projectIDs = append(projectIDs, b.projectID)
		}
		sort.Strings(projectIDs)
		return Paths{}, fmt.Errorf("session %s is ambiguous across %d buckets: %s (disambiguate with proj:<project-id>:%s)",
			sel.sid, len(found), strings.Join(projectIDs, ", "), sel.sid)
	}
}

// resolveBuckets returns the project buckets a selector can resolve into, plus
// the state root they were enumerated under (the root not-found errors name).
// Three base shapes are auto-detected, keyed on the on-disk layout the runtime
// itself writes (agent.RuntimeDir) and never on bucket names:
//
//   - a state home (the doctor's default resolution chain): buckets are the
//     directories under <base>/evener/projects, root is the base itself;
//   - a project bucket directory (the daemon's per-session state dir, what
//     EVENER_STATE_DIR carries): the sweep up-walks to the state home and
//     enumerates the same sibling set, root is that state home;
//   - an override / scratch root (sessions/ directly under it): it is itself
//     the single bucket, root is the base.
func resolveBuckets(stateBase string) (buckets []bucket, stateRoot string, err error) {
	if home := stateHomeForBucketDir(stateBase); home != "" && isDir(filepath.Join(home, "evener", "projects")) {
		buckets, err = globBuckets(filepath.Join(home, "evener", "projects"))
		return buckets, home, err
	}
	projects := filepath.Join(stateBase, "evener", "projects")
	if isDir(projects) {
		buckets, err = globBuckets(projects)
		return buckets, stateBase, err
	}
	// Override / scratch layout: stateBase is itself the bucket.
	return []bucket{{dir: stateBase, projectID: ""}}, stateBase, nil
}

// globBuckets enumerates the bucket directories under a projects dir. Every
// directory is a bucket: bucket identity is the actual directory name, and
// filtering on identifier.ValidateProjectID would hide a legacy- or
// foreign-named bucket that holds real sessions — a forensic sweep must see
// what is on disk.
func globBuckets(projects string) ([]bucket, error) {
	matches, err := globProjectBuckets(filepath.Join(projects, "*"))
	if err != nil {
		return nil, fmt.Errorf("glob project buckets: %w", err)
	}
	buckets := make([]bucket, 0, len(matches))
	for _, m := range matches {
		if isDir(m) {
			buckets = append(buckets, bucket{dir: m, projectID: filepath.Base(m)})
		}
	}
	return buckets, nil
}

// pathsFor builds the resolved Paths for a session in a bucket. The jobs.jsonl
// path is the per-session SUBDIR form (sessions/<sid>/jobs.jsonl), never a
// suffix on the transcript path. The client-mutation store is a third shape
// again: a flat <sid>.json under a bucket-level mutations/ dir beside sessions/.
func pathsFor(b bucket, sid string) Paths {
	sess := filepath.Join(b.dir, "sessions")
	return Paths{
		SessionID:      sid,
		TranscriptRef:  refFor(b.projectID, sid),
		ProjectID:      b.projectID,
		BucketDir:      b.dir,
		TranscriptPath: filepath.Join(sess, sid+".transcript.jsonl"),
		APILogPath:     filepath.Join(sess, sid+".api.jsonl"),
		MetaPath:       filepath.Join(sess, sid+".meta.json"),
		JobsPath:       filepath.Join(sess, sid, "jobs.jsonl"),
		DelegatesPath:  filepath.Join(sess, sid, "delegates.jsonl"),
		MutationsPath:  filepath.Join(b.dir, "mutations", sid+".json"),
	}
}

// refFor builds the transcript ref: proj:<project-id>:<sid> for a project bucket,
// or local:<sid> for an override / scratch root.
func refFor(projectID, sid string) string {
	if projectID == "" {
		return "local:" + sid
	}
	return "proj:" + projectID + ":" + sid
}

// sessionInBucket reports whether the session's transcript file is present in b.
func sessionInBucket(b bucket, sid string) bool {
	if identifier.ValidateSessionID(sid) != nil {
		return false
	}
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
