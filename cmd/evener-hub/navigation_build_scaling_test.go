package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// The hub-scale tree fixture below is 150 roots of 26 subagents: 3,900 session
// rows besides the roots, spread across 20 projects.
const (
	// navigationTreeAllocationBudgetBytes is the per-build byte ceiling. It is
	// an allocation bound, not a time bound: elapsed time measures the host and
	// its load, so a wall-clock ceiling cannot fail for the reason this test
	// exists, while allocated bytes are the work the build actually does.
	//
	// The measured cost is about 9.0 MB, and the spread across runs on a busy
	// host is under 0.1%, so the ceiling leaves room for the tree's output to
	// grow while still failing on a return of the per-row metadata value copies
	// (about 14.7 MB) or of a whole-title rune copy per row (about 27.7 MB).
	navigationTreeAllocationBudgetBytes = 12 << 20
	// navigationTreeAllocationBudgetAllocs is the per-build allocation count
	// ceiling. It catches per-row small allocations that a byte ceiling this
	// wide would not notice.
	navigationTreeAllocationBudgetAllocs = 8000
	// navigationTreeAllocationRuns is how many builds testing.AllocsPerRun
	// averages the count over.
	navigationTreeAllocationRuns = 5
)

// navigationTreeMetaFixture is the hub-scale session shape the sidebar tree is
// built from: many roots carrying prompt-derived titles, each with many
// subagents, spread across a few projects. It returns the metas and the project
// map a hub would have resolved for them at ingestion.
func navigationTreeMetaFixture(tb testing.TB) ([]schema.SessionMeta, map[string]identifier.Project, time.Time) {
	tb.Helper()
	const roots, subagentsPerRoot, projectsCount = 150, 26, 20
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// A prompt-derived title runs past the sidebar's rune cap, as a real one
	// does; the cap is what a build has to cut, not carry.
	prompt := strings.Repeat("investigate the flaky navigation build and report ", 20)
	metas := make([]schema.SessionMeta, 0, roots*(subagentsPerRoot+1))
	for r := range roots {
		rootID := fmt.Sprintf("root-%04d", r)
		workingDir := fmt.Sprintf("/workspace/project-%d", r%projectsCount)
		build := func(id, parent string, subagent bool, i int) schema.SessionMeta {
			m := schema.SessionMeta{
				ID: id, ParentSessionID: parent, IsSubagent: subagent,
				OriginalPrompt: fmt.Sprintf("%s %d", prompt, i),
				CreatedAt:      now.Add(-time.Duration(i) * time.Minute),
				UpdatedAt:      now.Add(-time.Duration(i%7) * time.Minute),
			}
			m.EnvInfo.WorkingDir = workingDir
			return m
		}
		metas = append(metas, build(rootID, "", false, r))
		for s := range subagentsPerRoot {
			m := build(fmt.Sprintf("%s-sub-%03d", rootID, s), rootID, true, r+s)
			m.JobTreeRootSessionID = rootID
			metas = append(metas, m)
		}
	}
	projects := make(map[string]identifier.Project, projectsCount)
	for p := range projectsCount {
		path := fmt.Sprintf("/workspace/project-%d", p)
		projects[path] = identifier.Project{ID: fmt.Sprintf("project-%d", p), CanonicalPath: path}
	}
	return metas, projects, now
}

// TestNavigationTreeBuildStaysWithinAllocationBudget pins the hub-scale tree
// build's allocation cost, in bytes and in count. Neither number reads the
// clock: allocated bytes are the work the build actually does, and they
// reproduce run to run however busy the host is, whereas a ratio of elapsed
// times moves with the scheduler and cannot fail for the reason this test
// exists. testing.AllocsPerRun averages the count over several runs with
// GOMAXPROCS pinned, so another goroutine's allocations move the mean by a
// fraction of one run instead of by all of themselves.
func TestNavigationTreeBuildStaysWithinAllocationBudget(t *testing.T) {
	metas, projects, now := navigationTreeMetaFixture(t)
	// The fixture's rows must actually be in the tree, or the measurement could
	// pass by building nothing.
	if got, want := countTreeSessions(hubcore.BuildTreeAtWithProjects(metas, nil, nil, now, projects)), 150; got != want {
		t.Fatalf("tree holds %d top-level rows, want %d", got, want)
	}
	// Every measurement is preceded by one unmeasured build: whatever a first
	// build allocates and a later one does not — lazily built package state,
	// sort scratch growing to its steady size — lands outside the window.
	measurement := testing.Benchmark(func(b *testing.B) {
		hubcore.BuildTreeAtWithProjects(metas, nil, nil, now, projects)
		b.ReportAllocs()
		for b.Loop() {
			hubcore.BuildTreeAtWithProjects(metas, nil, nil, now, projects)
		}
	})
	allocs := testing.AllocsPerRun(navigationTreeAllocationRuns, func() {
		hubcore.BuildTreeAtWithProjects(metas, nil, nil, now, projects)
	})
	bytesPerBuild := measurement.AllocedBytesPerOp()
	t.Logf("hub-scale tree build: %d bytes and %.0f allocs per build, budgets %d bytes and %d allocs",
		bytesPerBuild, allocs, navigationTreeAllocationBudgetBytes, navigationTreeAllocationBudgetAllocs)
	if bytesPerBuild > navigationTreeAllocationBudgetBytes {
		t.Fatalf("tree build allocated %d bytes per build, budget %d", bytesPerBuild, navigationTreeAllocationBudgetBytes)
	}
	if allocs > navigationTreeAllocationBudgetAllocs {
		t.Fatalf("tree build made %.0f allocations per build, budget %d", allocs, navigationTreeAllocationBudgetAllocs)
	}
}

// countTreeSessions counts the top-level rows of every project tier.
func countTreeSessions(tree hubcore.Tree) int {
	total := 0
	for _, projects := range [][]hubcore.TreeProject{tree.Projects, tree.ArchivedProjects} {
		for _, project := range projects {
			total += len(project.Current) + len(project.Recent) + len(project.Archived)
		}
	}
	return total
}

func mustSessionID(tb testing.TB) string {
	tb.Helper()
	id, err := identifier.NewSessionID()
	if err != nil {
		tb.Fatal(err)
	}
	return id
}

// navigationScalingFixture is a hub with many restartRequired daemons and many
// past sessions, some in fork chains. It reproduces the shape that made one
// navigation build cost (past sessions x roster size).
func navigationScalingFixture(tb testing.TB, daemons, pastSessions int) *WebServer {
	tb.Helper()
	return navigationScalingFixtureWithWorkingDir(tb, daemons, pastSessions, func(i int) string {
		return fmt.Sprintf("/nonexistent/project-%d", i%60)
	})
}

// navigationScalingFixtureWithWorkingDir builds the scaling fixture with the
// session working directories chosen by workingDir.
func navigationScalingFixtureWithWorkingDir(tb testing.TB, daemons, pastSessions int, workingDir func(i int) string) *WebServer {
	tb.Helper()
	runDir := tb.TempDir()
	peer := protocolMismatchPeer(tb)
	for i := range daemons {
		id := mustSessionID(tb)
		writeRendezvous(tb, runDir, rendezvous.Entry{PID: 2000 + i, Protocol: "evener-appwire-v3", ThreadID: id, SessionID: id, WorkspaceRef: "local:" + id, Endpoint: peer})
	}
	roster := liveClaimRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()

	now := time.Now()
	metas := make([]schema.SessionMeta, 0, pastSessions)
	var previous string
	for i := range pastSessions {
		id := mustSessionID(tb)
		meta := schema.SessionMeta{ID: id, CreatedAt: now.Add(-time.Duration(i+1) * time.Minute), UpdatedAt: now.Add(-time.Duration(i) * time.Minute)}
		meta.EnvInfo.WorkingDir = workingDir(i)
		// Every third session forks the one before it, so ancestry walks have depth.
		if i%3 != 0 && previous != "" {
			meta.ParentSessionID = previous
		}
		previous = id
		metas = append(metas, meta)
	}
	past := hubcore.NewPastIndex("")
	past.SeedForTest(metas)
	return NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Past: past, Roster: roster})
}

func BenchmarkNavigationSnapshotInputs(b *testing.B) {
	web := navigationScalingFixture(b, 150, 5000)
	b.ResetTimer()
	for range b.N {
		web.navigationSnapshotInputs(b.Context())
	}
}

// BenchmarkNavigationSnapshotInputsExistingDirectories is the paired
// successful-directory variant of BenchmarkNavigationSnapshotInputs: the same
// fixture shape with real working directories instead of missing paths.
func BenchmarkNavigationSnapshotInputsExistingDirectories(b *testing.B) {
	root := b.TempDir()
	dirs := make([]string, 60)
	for i := range dirs {
		dirs[i] = filepath.Join(root, fmt.Sprintf("project-%d", i))
		if err := os.MkdirAll(dirs[i], 0o755); err != nil {
			b.Fatal(err)
		}
	}
	web := navigationScalingFixtureWithWorkingDir(b, 150, 5000, func(i int) string {
		return dirs[i%len(dirs)]
	})
	b.ResetTimer()
	for range b.N {
		web.navigationSnapshotInputs(b.Context())
	}
}

func BenchmarkNavigationCapture(b *testing.B) {
	web := navigationScalingFixture(b, 150, 5000)
	source := webNavigationSource{web: web}
	b.ResetTimer()
	for range b.N {
		if _, err := source.Capture(b.Context(), "generation", time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}

// A past session with incomplete ancestry that no incompatible or unconfirmed
// daemon can own must not withdraw rename from every other local session.
func TestNavigationOrphanSubagentDoesNotDisableRename(t *testing.T) {
	web := navigationScalingFixture(t, 2, 0)
	rootID, orphanID, missingParentID := mustSessionID(t), mustSessionID(t), mustSessionID(t)
	now := time.Now()
	web.cfg.Past.SeedForTest([]schema.SessionMeta{
		{ID: rootID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
		{ID: orphanID, ParentSessionID: missingParentID, IsSubagent: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute)},
	})
	snapshot, err := webNavigationSource{web: web}.Capture(t.Context(), "generation", now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Inputs.Renameable[rootID] && !snapshot.Inputs.Renameable[localAppRef(rootID)] {
		t.Errorf("unrelated orphaned subagent disabled rename for %s: %v", rootID, snapshot.Inputs.Renameable)
	}
}

// Ownership verification follows the descendants of restartRequired daemons,
// not every past session.
func TestNavigationOwnershipWalksOnlyAffectedSessions(t *testing.T) {
	web := navigationScalingFixture(t, 3, 300)
	daemon := web.cfg.Roster.List()[0]
	child, grandchild := mustSessionID(t), mustSessionID(t)
	now := time.Now()
	metas := web.cfg.Past.AllMetas()
	metas = append(metas,
		schema.SessionMeta{ID: child, ParentSessionID: daemon.SessionID, JobTreeRootSessionID: daemon.SessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		schema.SessionMeta{ID: grandchild, ParentSessionID: child, JobTreeRootSessionID: daemon.SessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
	)
	web.cfg.Past.SeedForTest(metas)
	walks := 0
	old := hubRosterList
	hubRosterList = func(r *hubcore.Roster) []hubcore.LiveEntry {
		walks++
		return old(r)
	}
	t.Cleanup(func() { hubRosterList = old })

	snapshot := web.navigationSnapshotInputs(t.Context())
	// The fixture writes no delegate descriptors, so verifying the affected
	// descendants must fail and surface, exactly as a missing descriptor does.
	if snapshot.ownershipErr == nil {
		t.Error("unverifiable descendants of a restartRequired daemon reported no ownership error")
	}
	if walks > 4 {
		t.Errorf("ownership walked %d sessions for 2 affected descendants among 302 past sessions", walks)
	}
}
