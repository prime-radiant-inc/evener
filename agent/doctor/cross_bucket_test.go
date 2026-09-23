package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// hash3 is a third slug-form project id (readable portion + the 10-character
// base62 suffix identifier.Project mints), for fixtures that need three
// buckets so a not-found error's scanned-bucket count is distinguishable from
// "one".
const hash3 = "Project-three-0123456789"

// The daemon runs every session with its state dir set to the session's
// project bucket directory (see userdirs.StateHomeForBucketDir), so a
// forensic sweep handed that base must still see every sibling bucket under
// the state root, and a miss must say where it looked, not come back empty.
// These tests pin that sweep for both surfaces that share this package — the
// `evener doctor` CLI and the in-process doctor_evener tool.

// newTwoBucketState lays out a state home with two project buckets (hash1,
// hash2) — the smallest fixture that can hide a session in a sibling bucket.
func newTwoBucketState(t *testing.T) (stateHome, bucketA, bucketB string) {
	t.Helper()
	stateHome = t.TempDir()
	return stateHome, stateHomeBucket(stateHome, hash1), stateHomeBucket(stateHome, hash2)
}

// writeListedSession writes one minimal session whose ListSessions row is
// deterministic: a plain header and meta stamped now, no turns, no jobs
// events.
func writeListedSession(t *testing.T, dir, sid string) {
	t.Helper()
	now := time.Now()
	writeSessionsFixtureSession(t, dir, sid,
		transcript.Header{CreatedAt: now, Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, now)
}

// TestLocate_BucketDirBase_BareIDSweepsSiblingBuckets proves a bare id
// resolves from one project bucket into a sibling bucket of the same state
// root — the exact shape the doctoring diagnosis hit when a bare-id locate
// silently missed a session living in a sibling project bucket.
func TestLocate_BucketDirBase_BareIDSweepsSiblingBuckets(t *testing.T) {
	_, bucketA, bucketB := newTwoBucketState(t)
	writeSession(t, bucketA, sidA)
	writeSession(t, bucketB, sidB)

	got, err := Locate(bucketB, sidA)
	if err != nil {
		t.Fatalf("Locate from bucket dir %s: %v", bucketB, err)
	}
	if want := filepath.Join(bucketA, "sessions", sidA+".transcript.jsonl"); got.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want sibling-bucket path %q", got.TranscriptPath, want)
	}
	if got.ProjectID != hash1 {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, hash1)
	}
	if want := "proj:" + hash1 + ":" + sidA; got.TranscriptRef != want {
		t.Errorf("TranscriptRef = %q, want %q", got.TranscriptRef, want)
	}
}

// TestLocate_BucketDirBase_ProjSelectorAddressesSibling proves the
// proj:<project-id>:<sid> selector names a sibling bucket when the base is
// itself a project bucket directory.
func TestLocate_BucketDirBase_ProjSelectorAddressesSibling(t *testing.T) {
	_, bucketA, bucketB := newTwoBucketState(t)
	writeSession(t, bucketA, sidA)
	writeSession(t, bucketB, sidB)

	got, err := Locate(bucketB, "proj:"+hash1+":"+sidA)
	if err != nil {
		t.Fatalf("Locate proj ref from bucket dir: %v", err)
	}
	if got.ProjectID != hash1 || got.SessionID != sidA {
		t.Errorf("resolved project/session = %q/%q, want %q/%q", got.ProjectID, got.SessionID, hash1, sidA)
	}
	if want := filepath.Join(bucketA, "sessions", sidA+".transcript.jsonl"); got.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, want)
	}
}

// TestLocate_BucketDirBase_ProjectIDAndRefPopulated proves a successful
// resolution from a project-bucket base carries the real bucket identity —
// project_id and a proj: ref — instead of the override root's empty
// project_id and local: ref. Empty identity on success is what made the
// diagnosis read every cross-bucket answer as "no bucket".
func TestLocate_BucketDirBase_ProjectIDAndRefPopulated(t *testing.T) {
	stateHome := t.TempDir()
	bucketA := stateHomeBucket(stateHome, hash1)
	writeSession(t, bucketA, sidA)

	got, err := Locate(bucketA, sidA)
	if err != nil {
		t.Fatalf("Locate own-bucket session from bucket dir: %v", err)
	}
	if got.ProjectID != hash1 {
		t.Errorf("ProjectID = %q, want %q (the base bucket's own dir name)", got.ProjectID, hash1)
	}
	if want := "proj:" + hash1 + ":" + sidA; got.TranscriptRef != want {
		t.Errorf("TranscriptRef = %q, want %q, not a local: ref", got.TranscriptRef, want)
	}
}

// TestLocate_NotFoundNamesStateRootAndBucketCount proves a miss names the
// state root that was swept and how many buckets it scanned — from both base
// shapes — so "not there" is distinguishable from "looked in one bucket".
func TestLocate_NotFoundNamesStateRootAndBucketCount(t *testing.T) {
	stateHome := t.TempDir()
	bucketA := stateHomeBucket(stateHome, hash1)
	bucketB := stateHomeBucket(stateHome, hash2)
	bucketC := stateHomeBucket(stateHome, hash3)
	writeSession(t, bucketA, sidA)
	writeSession(t, bucketB, sidB)
	// A third, sessionless bucket so the scanned count is 3, not 2.
	if err := os.MkdirAll(bucketC, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := newSessionsTestSID(t)

	for name, base := range map[string]string{
		"state-home base":     stateHome,
		"project-bucket base": bucketA,
	} {
		_, err := Locate(base, missing)
		if err == nil {
			t.Fatalf("%s: want not-found error, got nil", name)
		}
		if !strings.Contains(err.Error(), "under "+stateHome) {
			t.Errorf("%s: error should name the state root %s, got: %v", name, stateHome, err)
		}
		if !strings.Contains(err.Error(), "(3 buckets scanned)") {
			t.Errorf("%s: error should say how many buckets were scanned, got: %v", name, err)
		}
	}
}

// TestLocate_SweepsBucketsByDirectoryNotName proves bucket enumeration keys
// on actual directories, not on the project-id naming scheme: a bucket dir
// whose name is not a valid Project.ID (a legacy pure-hash dir, as the
// clean-break fixture uses) is still swept. A session that exists there is
// findable by bare id — a forensic tool must see what is on disk.
func TestLocate_SweepsBucketsByDirectoryNotName(t *testing.T) {
	stateHome := t.TempDir()
	// "0123456789abcdef": no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it.
	legacyBucket := stateHomeBucket(stateHome, "0123456789abcdef")
	writeSession(t, legacyBucket, sidA)

	got, err := Locate(stateHome, sidA)
	if err != nil {
		t.Fatalf("Locate into non-validated bucket dir name: %v", err)
	}
	if got.ProjectID != "0123456789abcdef" {
		t.Errorf("ProjectID = %q, want the actual directory name %q", got.ProjectID, "0123456789abcdef")
	}
	if want := filepath.Join(legacyBucket, "sessions", sidA+".transcript.jsonl"); got.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, want)
	}
}

// TestListSessions_BucketDirBase_SweepsSiblingsAndPopulatesBucket proves the
// sessions enumeration from a project-bucket base lists sibling buckets' rows
// and stamps every row with its real bucket, so a batch study sees the whole
// state root and can group by project.
func TestListSessions_BucketDirBase_SweepsSiblingsAndPopulatesBucket(t *testing.T) {
	_, bucketA, bucketB := newTwoBucketState(t)
	writeListedSession(t, bucketA, sidA)
	writeListedSession(t, bucketB, sidB)

	res, err := ListSessions(bucketA, SessionsOpts{})
	if err != nil {
		t.Fatalf("ListSessions from bucket dir: %v", err)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions = %d rows (%+v), want 2 — the sweep must cross sibling buckets", len(res.Sessions), res.Sessions)
	}
	byBucket := map[string]string{}
	for _, row := range res.Sessions {
		byBucket[row.Bucket] = row.SessionID
	}
	if byBucket[hash1] != sidA || byBucket[hash2] != sidB {
		t.Errorf("rows by bucket = %v, want %q in %q and %q in %q", byBucket, sidA, hash1, sidB, hash2)
	}
}

// TestListSessions_UnknownBucketIsExplicitError proves a --bucket filter that
// matches no bucket is an explicit not-found error naming the state root and
// scan breadth — never a silently empty list, which is indistinguishable
// from "no sessions exist".
func TestListSessions_UnknownBucketIsExplicitError(t *testing.T) {
	stateHome, bucketA, bucketB := newTwoBucketState(t)
	writeSession(t, bucketA, sidA)
	writeSession(t, bucketB, sidB)

	_, err := ListSessions(stateHome, SessionsOpts{Bucket: "no-such-bucket-0123456789"})
	if err == nil {
		t.Fatal("unknown bucket filter: want explicit not-found error, got nil (silent empty list)")
	}
	if !strings.Contains(err.Error(), "under "+stateHome) {
		t.Errorf("error should name the state root %s, got: %v", stateHome, err)
	}
	if !strings.Contains(err.Error(), "(2 buckets scanned)") {
		t.Errorf("error should say how many buckets were scanned, got: %v", err)
	}
}

// TestListSessions_BucketDirBase_BucketFilterAddressesSibling proves the
// bucket argument accepts real bucket directory names and scopes to a
// sibling bucket even when the base is itself a project bucket directory.
func TestListSessions_BucketDirBase_BucketFilterAddressesSibling(t *testing.T) {
	_, bucketA, bucketB := newTwoBucketState(t)
	writeListedSession(t, bucketA, sidA)
	writeListedSession(t, bucketB, sidB)

	res, err := ListSessions(bucketA, SessionsOpts{Bucket: hash2})
	if err != nil {
		t.Fatalf("ListSessions scoped to sibling bucket %s: %v", hash2, err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].SessionID != sidB {
		t.Fatalf("bucket %s rows = %+v, want just %s", hash2, res.Sessions, sidB)
	}
	if res.Sessions[0].Bucket != hash2 {
		t.Errorf("row Bucket = %q, want %q", res.Sessions[0].Bucket, hash2)
	}
}

// TestLocate_TrailingSlashStateDirStillSweepsSiblings proves a trailing
// separator on a project-bucket state dir — the spelling `--state-dir
// "$DIR/"` produces — does not defeat the up-walk to the state home and
// resurface the silent single-bucket miss.
func TestLocate_TrailingSlashStateDirStillSweepsSiblings(t *testing.T) {
	_, bucketA, bucketB := newTwoBucketState(t)
	writeSession(t, bucketA, sidA)
	writeSession(t, bucketB, sidB)

	got, err := Locate(bucketB+string(filepath.Separator), sidA)
	if err != nil {
		t.Fatalf("Locate from trailing-slash bucket dir: %v", err)
	}
	if want := filepath.Join(bucketA, "sessions", sidA+".transcript.jsonl"); got.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want sibling-bucket path %q", got.TranscriptPath, want)
	}
	if got.ProjectID != hash1 {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, hash1)
	}
}

// TestLocate_PhantomBucketDirPathIsSingleBucketMiss proves a nonexistent
// bucket-dir spelling under a real projects dir is not promoted to the state
// root: no sibling sweep, just the ordinary single-path miss. Sweeping
// siblings from a path that never existed would report sessions a
// nonexistent base cannot contain.
func TestLocate_PhantomBucketDirPathIsSingleBucketMiss(t *testing.T) {
	stateHome, bucketA, _ := newTwoBucketState(t)
	writeSession(t, bucketA, sidA)
	phantom := filepath.Join(stateHome, "evener", "projects", "missing")

	_, err := Locate(phantom, sidA)
	if err == nil {
		t.Fatal("a session in a sibling bucket must not resolve from a phantom bucket-dir path")
	}
	if !strings.Contains(err.Error(), "under "+phantom) {
		t.Errorf("miss should name the phantom path, got: %v", err)
	}
	if !strings.Contains(err.Error(), "(1 bucket scanned)") {
		t.Errorf("miss should be the ordinary single-path miss, not a sibling sweep, got: %v", err)
	}
}
