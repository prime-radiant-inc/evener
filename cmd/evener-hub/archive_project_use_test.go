package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

func newArchiveDecisionStore(t *testing.T) *hubcore.ArchiveStore {
	t.Helper()
	return closeOnCleanup(t, hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "index.db")))
}

func archiveProject(t *testing.T, store *hubcore.ArchiveStore, source string, decided time.Time) hubcore.ArchiveKey {
	t.Helper()
	key := hubcore.ProjectDecisionKey(source, "p")
	if err := store.Set(key.Source, key.Kind, key.ID, true, decided); err != nil {
		t.Fatal(err)
	}
	return key
}

func archiveDecision(t *testing.T, store *hubcore.ArchiveStore, key hubcore.ArchiveKey) (bool, bool) {
	t.Helper()
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	archived, ok := decisions[key]
	return archived, ok
}

func assertProjectArchived(t *testing.T, store *hubcore.ArchiveStore, key hubcore.ArchiveKey) {
	t.Helper()
	if archived, ok := archiveDecision(t, store, key); !ok || !archived {
		t.Fatalf("project decision = (%t, present=%t), want the archive kept", archived, ok)
	}
}

func assertProjectUnarchived(t *testing.T, store *hubcore.ArchiveStore, key hubcore.ArchiveKey) {
	t.Helper()
	if archived, _ := archiveDecision(t, store, key); archived {
		t.Fatalf("project decision = %t, want the project unarchived", archived)
	}
}

// A session launched into an archived project means the user is using the
// project again, so the project decision that would file the new session as
// archived is cleared. The session's own decisions are not touched.
func TestNewTopLevelSessionUnarchivesItsProject(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	project := archiveProject(t, store, "", decided)
	session := hubcore.ArchiveKey{Kind: "session", ID: "s-archived"}
	if err := store.Set("", session.Kind, session.ID, true, decided); err != nil {
		t.Fatal(err)
	}

	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		metas: []schema.SessionMeta{{
			ID:        "s-new",
			CreatedAt: decided.Add(time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/work/proj"},
		}},
		projects: map[string]identifier.Project{"/work/proj": {ID: project.ID}},
	})

	assertProjectUnarchived(t, store, project)
	if archived, ok := archiveDecision(t, store, session); !ok || !archived {
		t.Fatalf("session decision = (%t, present=%t), want the explicit archive kept", archived, ok)
	}
}

// A fork continuation is a new top-level session, so launching one after the
// archive clears the decision even though its parent predates it.
func TestForkContinuationLaunchedAfterTheArchiveUnarchivesItsProject(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	project := archiveProject(t, store, "", decided)
	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		metas: []schema.SessionMeta{
			{ID: "s-parent", CreatedAt: decided.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/proj"}},
			{ID: "s-fork", CreatedAt: decided.Add(time.Hour), ParentSessionID: "s-parent", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/proj"}},
		},
		projects: map[string]identifier.Project{"/work/proj": {ID: project.ID}},
	})
	assertProjectUnarchived(t, store, project)
}

// Only a session created after the decision counts. An existing session the
// archive already covered, a subagent, and a fork original superseded by its
// continuation all leave the decision alone.
func TestPreDecisionOrNestedSessionKeepsTheProjectArchive(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		metas []schema.SessionMeta
	}{
		{
			name:  "created before the archive",
			metas: []schema.SessionMeta{{ID: "s-old", CreatedAt: decided.Add(-time.Minute)}},
		},
		{
			name:  "created at the decision instant",
			metas: []schema.SessionMeta{{ID: "s-old", CreatedAt: decided}},
		},
		{
			name: "subagent created after the archive",
			metas: []schema.SessionMeta{{
				ID: "s-child", CreatedAt: decided.Add(time.Hour), IsSubagent: true, ParentSessionID: "s-parent",
			}},
		},
		{
			name: "superseded fork original created after the archive",
			metas: []schema.SessionMeta{
				{ID: "s-parent", CreatedAt: decided.Add(time.Hour), ForkLabel: "fork"},
				{ID: "s-continuation", CreatedAt: decided.Add(-time.Hour), ParentSessionID: "s-parent"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newArchiveDecisionStore(t)
			project := archiveProject(t, store, "", decided)
			for i := range tc.metas {
				tc.metas[i].EnvInfo.WorkingDir = "/work/proj"
			}
			unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
				metas:    tc.metas,
				projects: map[string]identifier.Project{"/work/proj": {ID: project.ID}},
			})
			assertProjectArchived(t, store, project)
		})
	}
}

// The decision belongs to the source that owns the project: a new session on
// one host unarchives that host's project, never another host's project of the
// same ID.
func TestNewSessionUnarchivesOnlyItsOwningSource(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	local := archiveProject(t, store, "", decided)
	remote := archiveProject(t, store, "host-a", decided)
	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		metas: []schema.SessionMeta{{
			ID:        "host-a:s-new",
			CreatedAt: decided.Add(time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/work/proj"},
		}},
		projects: map[string]identifier.Project{"/work/proj": {ID: "p"}},
	})
	assertProjectUnarchived(t, store, remote)
	assertProjectArchived(t, store, local)
}

// A session the hub has not yet folded into its past index still counts: its
// own state dir carries the meta the launch decision needs, and navigation
// has already resolved the live entry's project.
func TestLiveSessionMissingFromTheIndexUnarchivesItsProject(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	project := archiveProject(t, store, "", decided)
	stateDir := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        "s-live",
		CreatedAt: decided.Add(time.Hour),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/work/proj"},
	}); err != nil {
		t.Fatal(err)
	}
	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		live: []hubcore.LiveEntry{{
			StateDir:  stateDir,
			SessionID: "s-live",
			Project:   identifier.Project{ID: project.ID},
		}},
	})
	assertProjectUnarchived(t, store, project)
}

// An in-process child is a subagent even before its meta is folded into the
// past index, so its launch does not unarchive the project.
func TestRunningSubagentLaunchKeepsTheProjectArchive(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	project := archiveProject(t, store, "", decided)
	stateDir := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        "s-child",
		CreatedAt: decided.Add(time.Hour),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/work/proj"},
	}); err != nil {
		t.Fatal(err)
	}
	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		live: []hubcore.LiveEntry{
			{SessionID: "s-worker", Project: identifier.Project{ID: project.ID}, RunningSubagentIDs: []string{"s-child"}},
			{SessionID: "s-child", StateDir: stateDir, Project: identifier.Project{ID: project.ID}},
		},
	})
	assertProjectArchived(t, store, project)
}

// A delegate that runs as its own daemon carries IsSubagent in its meta and
// lists itself in no parent's running children, so the meta must decide it.
func TestLiveOnlySubagentLaunchKeepsTheProjectArchive(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	project := archiveProject(t, store, "", decided)
	stateDir := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:         "s-delegate",
		CreatedAt:  decided.Add(time.Hour),
		IsSubagent: true,
		EnvInfo:    schema.EnvironmentInfo{WorkingDir: "/work/proj"},
	}); err != nil {
		t.Fatal(err)
	}
	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		live: []hubcore.LiveEntry{{
			StateDir:  stateDir,
			SessionID: "s-delegate",
			Project:   identifier.Project{ID: project.ID},
		}},
	})
	assertProjectArchived(t, store, project)
}

// The live pre-check and the rule must read the same owning source, or a
// roster-only launch is dropped before the rule sees it.
func TestLiveEntrySourceKeysTheUnarchive(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	store := newArchiveDecisionStore(t)
	remote := archiveProject(t, store, "host-a", decided)
	stateDir := t.TempDir()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        "s-live",
		CreatedAt: decided.Add(time.Hour),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/work/proj"},
	}); err != nil {
		t.Fatal(err)
	}
	unarchiveProjectsUsedSinceNewSessions(store, navigationSnapshot{
		live: []hubcore.LiveEntry{{
			StateDir:  stateDir,
			SessionID: "s-live",
			SourceID:  "host-a",
			Project:   identifier.Project{ID: remote.ID},
		}},
	})
	assertProjectUnarchived(t, store, remote)
}

// A remote thread's project identity comes from the host that reported it.
// Two hosts can name the same working directory, and the snapshot's
// path-keyed project map picks one of their projects; reconciliation must
// not use that pick for the other host's session.
func TestRemoteSessionUnarchivesItsReportedProject(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	zID := identifier.ProjectFromCanonicalPath("/work/z").ID
	aID := identifier.ProjectFromCanonicalPath("/work/a").ID
	remoteID, remotePath, decoyID := zID, "/work/z", aID
	if aID > zID {
		remoteID, remotePath, decoyID = aID, "/work/a", zID
	}
	cache := &hubcore.RemoteThreadCache{}
	cache.Store(hubcore.RemoteThreadSnapshot{Threads: []appwire.Thread{
		{
			ID: "t-remote", SessionID: "t-remote", Source: "host-a", CWD: "/work/task",
			ProjectID: remoteID, ProjectPath: remotePath, CreatedAt: decided.Add(time.Hour).Unix(),
		},
		{
			ID: "t-other", SessionID: "t-other", Source: "host-b", CWD: "/work/task",
			ProjectID: decoyID, ProjectPath: "/work/decoy", CreatedAt: decided.Add(time.Hour).Unix(),
		},
	}}, nil)
	store := newArchiveDecisionStore(t)
	web := NewWebServer(testWebConfig(t, hubcore.WebConfig{Archive: store, RemoteThreadCache: cache}))
	snapshot := web.navigationSnapshot(t.Context())
	if got := snapshot.projects["/work/task"].ID; got != decoyID {
		t.Fatalf("path-keyed project = %q, want the other host's %q so the test can tell the two apart", got, decoyID)
	}

	reported := hubcore.ArchiveKey{Kind: "project", ID: remoteID, Source: "host-a"}
	decoy := hubcore.ArchiveKey{Kind: "project", ID: decoyID, Source: "host-a"}
	for _, key := range []hubcore.ArchiveKey{reported, decoy} {
		if err := store.Set(key.Source, key.Kind, key.ID, true, decided); err != nil {
			t.Fatal(err)
		}
	}

	unarchiveProjectsUsedSinceNewSessions(store, snapshot)
	assertProjectUnarchived(t, store, reported)
	assertProjectArchived(t, store, decoy)
}

// The reconcile rides the hub's one snapshot wake, so the attention derive and
// the project archive read the same state: the derive sees the cleared flag
// in the same pass.
func TestSnapshotWakeUnarchivesProjectUsedByANewSession(t *testing.T) {
	decided := time.Date(2026, 8, 19, 16, 51, 56, 0, time.UTC)
	snapshot := navigationSnapshot{
		metas: []schema.SessionMeta{{
			ID:        "s-new",
			CreatedAt: decided.Add(time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/work/proj"},
		}},
		projects: map[string]identifier.Project{"/work/proj": {ID: "p"}},
	}
	old := hubNavigationInputs
	hubNavigationInputs = func(*WebServer, context.Context) navigationSnapshot { return snapshot }
	t.Cleanup(func() { hubNavigationInputs = old })
	var derived map[hubcore.ArchiveKey]bool
	oldDerive := hubDeriveNavigationAttention
	hubDeriveNavigationAttention = func(_ []schema.SessionMeta, _ []hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool) (map[string]appwire.AttentionEntry, appwire.AttentionSummary) {
		derived = decisions
		return nil, appwire.AttentionSummary{}
	}
	t.Cleanup(func() { hubDeriveNavigationAttention = oldDerive })

	server, web := newHubRPCTestServerWithWeb(t, testWebConfig(t, hubcore.WebConfig{}))
	defer server.Close()
	store := newArchiveDecisionStore(t)
	project := archiveProject(t, store, "", decided)
	runHubAttention(t.Context(), web, store, hubcore.NewAttentionWatcher(func(appwire.AttentionChangedPayload) {}))
	assertProjectUnarchived(t, store, project)
	if archived, ok := derived[project]; !ok || archived {
		t.Fatalf("attention derive saw project decision = (%t, present=%t), want the unarchived flag", archived, ok)
	}
}
