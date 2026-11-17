package hubcore

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// captureSkipReport runs fn with os.Stderr redirected to a temp file and
// returns the past-index lines written to it. Other hubcore machinery logs to
// the same stream from background goroutines, so the result is filtered to
// this reporter's prefix rather than returned raw. It captures to a file, not
// a pipe: a pipe's writer blocks when its buffer fills, and on a busy Mac that
// buffer is 512 bytes (#2495).
func captureSkipReport(t *testing.T, fn func()) string {
	t.Helper()
	stderr, err := os.CreateTemp(t.TempDir(), "skip-report-stderr-")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	original := os.Stderr
	os.Stderr = stderr
	// Deferred so a t.Fatalf inside fn still restores stderr.
	defer func() {
		os.Stderr = original
		_ = stderr.Close()
	}()

	fn()

	raw, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var report strings.Builder
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(line, "[hub] past index: ") {
			report.WriteString(line)
			report.WriteString("\n")
		}
	}
	return report.String()
}

// A hand-seeded project directory or session meta whose id fails validation is
// correctly left out of the index, but the absence on its own points nowhere
// near the cause. Rebuild must name the path and the validation that rejected
// it, while still indexing everything valid around it.
func fuzzScenarioPastIndex_RebuildNamesSkippedEntries(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")

	strayProject := filepath.Join(projects, "no-suffix")
	if err := os.MkdirAll(strayProject, 0o755); err != nil {
		t.Fatal(err)
	}

	good := hubtest.ProjectDir(t, projects, "good")
	validID := hubtest.SessionID(t)
	writeMeta(t, good, schema.SessionMeta{ID: validID, UpdatedAt: time.Now()})
	// The exact shape that cost three agents a day: a readable placeholder that
	// looks like a session id and is not one.
	writeMeta(t, good, schema.SessionMeta{ID: "02wMz5Txv6USAGE000001", UpdatedAt: time.Now()})

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	out := captureSkipReport(t, func() {
		if _, err := idx.Rebuild(); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	})

	if !strings.Contains(out, strayProject) || !strings.Contains(out, "invalid project id") {
		t.Fatalf("skip report must name the stray project dir and its validation:\n%s", out)
	}
	badMeta := filepath.Join(good, "sessions", "02wMz5Txv6USAGE000001.meta.json")
	if !strings.Contains(out, badMeta) || !strings.Contains(out, "invalid session id") {
		t.Fatalf("skip report must name the rejected session meta and its validation:\n%s", out)
	}
	// The identifier errors name the broken rule, not the shape to write
	// instead; without the shape the report still leaves the reader guessing.
	if !strings.Contains(out, projectIDShape) || !strings.Contains(out, sessionIDShape) {
		t.Fatalf("skip report must spell out the id shapes %q and %q:\n%s", projectIDShape, sessionIDShape, out)
	}
	if _, ok := idx.Find(validID); !ok {
		t.Fatalf("the valid session must still be indexed; report was:\n%s", out)
	}
	if all := idx.All(); len(all) != 1 {
		t.Fatalf("index holds %d entries, want only the valid session", len(all))
	}
}

// A projects root full of pre-identifier directories must announce itself once,
// not on every rebuild tick — and when a new unindexable entry appears, only
// that one is reported, so it is not buried under the standing set.
func fuzzScenarioPastIndex_RebuildReportsOnlyNewlySkippedEntries(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	standing := filepath.Join(projects, "legacy16hexname")
	if err := os.MkdirAll(standing, 0o755); err != nil {
		t.Fatal(err)
	}

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	first := captureSkipReport(t, func() { _, _ = idx.Rebuild() })
	if !strings.Contains(first, standing) {
		t.Fatalf("first rebuild must report the standing skip:\n%s", first)
	}

	second := captureSkipReport(t, func() { _, _ = idx.Rebuild() })
	if second != "" {
		t.Fatalf("an unchanged skip set must be silent on re-rebuild, got:\n%s", second)
	}

	fresh := filepath.Join(projects, "another-stray")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	third := captureSkipReport(t, func() { _, _ = idx.Rebuild() })
	if !strings.Contains(third, fresh) {
		t.Fatalf("a newly unindexable entry must be reported:\n%s", third)
	}
	if strings.Contains(third, standing) {
		t.Fatalf("an already-reported skip must not repeat:\n%s", third)
	}
}

// Real projects roots hold hundreds of pre-identifier directories, so the
// report names a bounded sample and counts the rest.
func fuzzScenarioPastIndex_RebuildCapsSkipReport(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	const strays = maxReportedSkips + 3
	for n := range strays {
		if err := os.MkdirAll(filepath.Join(projects, fmt.Sprintf("stray%02d", n)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	out := captureSkipReport(t, func() { _, _ = idx.Rebuild() })

	named := strings.Count(out, " path=")
	if named != maxReportedSkips {
		t.Fatalf("report named %d skips, want the %d cap:\n%s", named, maxReportedSkips, out)
	}
	if want := fmt.Sprintf("count=%d", strays-maxReportedSkips); !strings.Contains(out, want) {
		t.Fatalf("report must count the remainder as %q:\n%s", want, out)
	}
}

// A rebuild over a clean projects root says nothing.
func fuzzScenarioPastIndex_RebuildSilentWhenNothingSkipped(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	proj := hubtest.ProjectDir(t, projects, "clean")
	writeMeta(t, proj, schema.SessionMeta{ID: hubtest.SessionID(t), UpdatedAt: time.Now()})

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	out := captureSkipReport(t, func() {
		if _, err := idx.Rebuild(); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	})
	if out != "" {
		t.Fatalf("a rebuild with nothing to skip must be silent, got:\n%s", out)
	}
}

// A directory in the projects root whose name is not a project id is refused,
// and rightly so — nothing under it can reach either index, and SessionTreeStamp
// skips it for the same reason. Real projects roots hold hundreds of
// pre-identifier directories. That refusal must not count against coverage:
// no rescan could ever index it, so holding the sweep's digest gate open over
// it would re-scan an idle tree every tick, which is the cost #6424 removed.
func fuzzScenarioPastIndex_StrayProjectDirectoryDoesNotBlockCoverage(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	const stray = "legacy16hexname"
	if err := os.MkdirAll(filepath.Join(projects, stray), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := hubtest.ProjectDir(t, projects, "alpha")
	id := hubtest.SessionID(t)
	writeMeta(t, proj, schema.SessionMeta{ID: id, UpdatedAt: time.Now()})

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	out := captureSkipReport(t, func() {
		if _, err := idx.Rebuild(); err != nil {
			t.Fatalf("Rebuild: %v", err)
		}
	})
	if !idx.RebuildCoveredTree() {
		t.Fatal("a stray pre-identifier directory held coverage false; the sweep gate would never close and every idle tick would rescan the whole tree")
	}
	// The refusal still has to be reported; leaving the stray out of coverage
	// must not mean dropping it from the skip report.
	if !strings.Contains(out, stray) {
		t.Fatalf("the stray directory is not named in the skip report:\n%s", out)
	}
	if all := idx.All(); len(all) != 1 || all[0].ID != id {
		t.Fatalf("index holds %d entries, want only the valid project's session", len(all))
	}
}

// RebuildCoveredTree is what lets the hub's sweep gate its re-scan on a disk
// digest: the digest only stands for the whole tree when the rebuild that
// produced the index reached every project. A scan that swaps in with every
// session listed covered the tree, and it has to say so, or the gate never
// closes and every idle tick pays a full scan.
func fuzzScenarioPastIndex_CompleteRebuildCoversTheTree(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	projA := hubtest.ProjectDir(t, projects, "alpha")
	projB := hubtest.ProjectDir(t, projects, "bravo")
	idA, idB := hubtest.SessionID(t), hubtest.SessionID(t)
	writeMeta(t, projA, schema.SessionMeta{ID: idA, UpdatedAt: time.Now()})
	writeMeta(t, projB, schema.SessionMeta{ID: idB, UpdatedAt: time.Now()})

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	if idx.RebuildCoveredTree() {
		t.Fatal("an index that has never rebuilt reports it covered the tree")
	}
	if _, err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if !idx.RebuildCoveredTree() {
		t.Fatal("a rebuild that listed every project's sessions does not report the tree covered; the sweep gate can never close")
	}
	// Assert the size too: an empty index would satisfy a bare "no error"
	// reading of the rebuild above while covering nothing.
	if all := idx.All(); len(all) != 2 {
		t.Fatalf("index holds %d entries after a complete rebuild, want 2", len(all))
	}
}

// A rebuild superseded on every attempt swaps nothing in — the newer
// in-memory snapshot is kept on purpose (#724) — so the index still lacks
// whatever the scan that never landed would have brought it. It must not
// report the tree covered, or the sweep records a digest for an index that is
// short of the tree and never scans again.
func fuzzScenarioPastIndex_SupersededRebuildDoesNotCoverTheTree(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	proj := hubtest.ProjectDir(t, projects, "alpha")
	seededID := hubtest.SessionID(t)
	writeMeta(t, proj, schema.SessionMeta{ID: seededID, UpdatedAt: time.Now()})

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatalf("seed Rebuild: %v", err)
	}
	if !idx.RebuildCoveredTree() {
		t.Fatal("the seed rebuild did not cover the tree, so the abort below proves nothing")
	}

	// Supersede every attempt's scan so Rebuild never swaps one in. The session
	// that appears meanwhile is what the discarded scan would have indexed.
	lateID := hubtest.SessionID(t)
	writeMeta(t, proj, schema.SessionMeta{ID: lateID, UpdatedAt: time.Now().Add(time.Minute)})
	idx.SetBeforeRebuildSwapForTest(func() {
		idx.mu.Lock()
		idx.gen++
		idx.mu.Unlock()
	})
	defer idx.SetBeforeRebuildSwapForTest(nil)
	if _, err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if idx.RebuildCoveredTree() {
		t.Fatal("a rebuild superseded on every attempt reports the tree covered; the sweep would record a digest for an index it never updated")
	}
	if _, ok := idx.FindIndexed(lateID); ok {
		t.Fatal("the superseded rebuild indexed the late session, so nothing was abandoned")
	}
}

// A project whose sessions directory cannot be listed is recorded as a skip
// and the rebuild carries on, so the swapped index is silently missing that
// project's sessions. The directory's unreadability does move the digest
// (foldSessionDirStamp folds an unreadable marker), but the index is short of
// the tree until a scan manages to list it, so the rebuild that could not must
// not report the tree covered.
func fuzzScenarioPastIndex_UnlistableProjectDoesNotCoverTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod on a directory is a no-op on Windows; the permission gate cannot be exercised")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	readable := hubtest.ProjectDir(t, projects, "alpha")
	unreadable := hubtest.ProjectDir(t, projects, "bravo")
	readableID := hubtest.SessionID(t)
	writeMeta(t, readable, schema.SessionMeta{ID: readableID, UpdatedAt: time.Now()})
	writeMeta(t, unreadable, schema.SessionMeta{ID: hubtest.SessionID(t), UpdatedAt: time.Now()})

	sessionsDir := schema.SessionsDir(unreadable)
	if err := os.Chmod(sessionsDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessionsDir, 0o755) })

	idx := closeOnCleanup(t, NewPastIndex(filepath.Join(projects, "*")))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if idx.RebuildCoveredTree() {
		t.Fatal("a rebuild that could not list one project's sessions reports the tree covered; the sweep would record a digest for a partial index")
	}
	// Pin that the scan really was partial rather than empty: the readable
	// project is indexed and the unreadable one is not.
	if all := idx.All(); len(all) != 1 || all[0].ID != readableID {
		t.Fatalf("index holds %d entries (want only the readable project's session), so the unreadable project was not the reason coverage is false", len(all))
	}
}
