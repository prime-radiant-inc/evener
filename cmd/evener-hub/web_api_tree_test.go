package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// A project favorite is stored under (source, project ID). The navigation
// projection must keep that source dimension: registering every favorite under
// its bare ID would let one host's favorite decorate another host's project.
func TestProjectFavoritePresentationIsSourceQualified(t *testing.T) {
	presentation := map[hubcore.ArchiveKey]bool{
		{Kind: "project", ID: "shared", Source: "host-a"}: true,
		{Kind: "project", ID: "mine", Source: ""}:         true,
	}
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, hubcore.Tree{}, nil, hubapi.AttentionSummary{}, nil, nil, projectFavoritePresentation(presentation), nil, nil)

	if !inputs.ProjectFavorite[projectFavoriteKey("host-a", "shared")] {
		t.Fatalf("remote favorite not registered under its source: %v", inputs.ProjectFavorite)
	}
	if inputs.ProjectFavorite["shared"] {
		t.Fatalf("remote favorite leaked onto the bare project key: %v", inputs.ProjectFavorite)
	}
	if !inputs.ProjectFavorite["mine"] {
		t.Fatalf("controller favorite not registered under the bare key: %v", inputs.ProjectFavorite)
	}
	if !projectFavoriteForSources(inputs.ProjectFavorite, hubcore.TreeProject{Key: "shared", Sources: []string{"host-a"}}) {
		t.Fatal("project owned by host-a did not resolve its favorite")
	}
	if projectFavoriteForSources(inputs.ProjectFavorite, hubcore.TreeProject{Key: "shared", Sources: []string{"host-b"}}) {
		t.Fatal("project owned by host-b inherited host-a's favorite")
	}
}

// The rail sends no source on a project favorite, so the decision is stored
// under the controller key. When the project is owned by one remote host the
// classifier resolves that bare decision to the host's authority; the
// presentation must then register under the host-qualified key the project row
// reads, or the favorite returns OK:true but no star ever appears.
func TestProjectFavoriteBareDecisionResolvesRemoteProject(t *testing.T) {
	const projectID = "remote-project"
	bare := hubcore.ArchiveKey{Kind: "project", ID: projectID}
	authority := hubcore.FavoriteAuthority{Projects: []hubcore.FavoriteProjectAuthority{
		{ID: projectID, Source: "host-a", Quality: hubcore.FavoriteAuthorityComplete, ClaimKey: "/srv/a\x00host-a"},
	}}
	classified := hubcore.ClassifyFavoriteDecisions(map[hubcore.ArchiveKey]bool{bare: true}, authority)
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, hubcore.Tree{}, nil, hubapi.AttentionSummary{}, nil, nil,
		projectFavoritePresentation(classified.Presentation), nil, nil)

	if !projectFavoriteForSources(inputs.ProjectFavorite, hubcore.TreeProject{Key: projectID, Sources: []string{"host-a"}}) {
		t.Fatalf("bare remote favorite did not present on the project: %v", inputs.ProjectFavorite)
	}
}

func testProjectID(t *testing.T, path string) string {
	t.Helper()
	project, err := identifier.ResolveProject(path)
	if err != nil {
		// Fuzz/coverage callers use synthetic paths only to exercise request
		// decoding; ordinary endpoint tests use real temp directories.
		return "test-project-0000000000"
	}
	return project.ID
}

func TestArchiveDecisionsFlowIntoTree(t *testing.T) {
	// Verify that an ArchiveStore decision actually changes where a project lands
	// in the tree — i.e. the real integration path flows through BuildTreeAt.
	now := time.Unix(1_700_000_000, 0)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	// Manually archive the canonical project even though it has a fresh session.
	if err := store.Set("", "project", project.ID, true, now); err != nil {
		t.Fatal(err)
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatalf("Decisions() error: %v", err)
	}
	// A fresh session for "alpha" — without the decision it would be in Projects.
	metas := []schema.SessionMeta{{
		ID:        "01ALPHA",
		UpdatedAt: now.Add(-time.Hour),
		CreatedAt: now.Add(-time.Hour),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
	}}
	tree := hubcore.BuildTreeAt(metas, nil, decisions, now)
	// The manual-archive decision must push alpha out of Projects and into ArchivedProjects.
	for _, p := range tree.Projects {
		if p.Name == "alpha" {
			t.Fatalf("alpha should not be in Projects after manual archive; got %v", tree.Projects)
		}
	}
	found := false
	for _, p := range tree.ArchivedProjects {
		if p.Name == "alpha" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alpha should be in ArchivedProjects after manual archive; got %v", tree.ArchivedProjects)
	}
}

func TestArchiveDecisionsFlowIntoTreeForSession(t *testing.T) {
	// A decision on a session ID should affect that session's tier while leaving
	// the containing project active when another non-archived session exists.
	now := time.Unix(1_700_000_001, 0)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{
			ID:        "01ALP1",
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now.Add(-time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		},
		{
			ID:        "01ALP2",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: project.CanonicalPath},
		},
	}
	decisions := map[hubcore.ArchiveKey]bool{
		{Kind: "session", ID: "01ALP1"}: true,
	}
	tree := hubcore.BuildTreeAt(metas, nil, decisions, now)
	if len(tree.Projects) != 1 {
		t.Fatalf("len(projects)=%d, want 1", len(tree.Projects))
	}
	if tree.Projects[0].Key != project.ID {
		t.Fatalf("unexpected project key = %q, want %q", tree.Projects[0].Key, project.ID)
	}
	if len(tree.Projects[0].Current) != 1 || tree.Projects[0].Current[0].ID != "01ALP2" {
		t.Fatalf("active session tier mismatch: current=%v", tree.Projects[0].Current)
	}
	if len(tree.Projects[0].Archived) != 1 || tree.Projects[0].Archived[0].ID != "01ALP1" {
		t.Fatalf("archived session tier mismatch: archived=%v", tree.Projects[0].Archived)
	}
	if len(tree.ArchivedProjects) != 0 {
		t.Fatalf("archived project should stay active with a live companion: archivedProjects=%v", tree.ArchivedProjects)
	}
}

func TestArchiveDecisionsHelperNilSafe(t *testing.T) {
	// A WebServer whose cfg.Archive is nil must return an empty map, never panic.
	s := &WebServer{cfg: hubcore.WebConfig{}}
	got := s.archiveDecisions()
	if got == nil {
		t.Fatal("archiveDecisions() returned nil; want empty map")
	}
	if len(got) != 0 {
		t.Fatalf("archiveDecisions() returned %v; want empty map", got)
	}
}

func TestArchiveDecisionsHelperWithStore(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "beta")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	if err := store.Set("", "project", project.ID, true, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	s := &WebServer{cfg: hubcore.WebConfig{Archive: store}}
	got := s.archiveDecisions()
	if !got[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("archiveDecisions() missing expected decision; got %v", got)
	}
}

func TestLiveSessionGroupsBeforePastIndex(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "foo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "01L", WorkingDir: project.CanonicalPath}, SessionID: "01L", Status: "active"},
	)
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), Roster: roster})
	_, live, projects := web.navigationTreeInputs(context.Background())
	tree := hubBuildNavigationTree(nil, live, map[hubcore.ArchiveKey]bool{}, projects)
	if len(tree.Projects) != 1 {
		t.Fatalf("projects = %d, want 1: %+v", len(tree.Projects), tree.Projects)
	}
	if tree.Projects[0].Key != project.ID {
		t.Fatalf("project key = %q, want canonical ID %q", tree.Projects[0].Key, project.ID)
	}
	if sessions := tree.Projects[0].Current; len(sessions) != 1 || sessions[0].ID != "01L" {
		t.Fatalf("current sessions = %+v, want the live session", sessions)
	}
	lazy, ok := hubcore.BuildProjectTreeAt(nil, live, map[hubcore.ArchiveKey]bool{}, time.Now(), project.ID)
	if !ok {
		t.Fatalf("lazy project lookup did not find live project %q", project.ID)
	}
	if sessions := lazy.Current; len(sessions) != 1 || sessions[0].ID != "01L" {
		t.Fatalf("lazy current sessions = %+v, want the live session", sessions)
	}
}

func TestNavigationTreeInputsUsesRemoteCarriedProject(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "remote-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID:          "remote-thread",
		Source:      "remote",
		CWD:         filepath.Join(project.CanonicalPath, "linked-worktree"),
		ProjectID:   project.ID,
		ProjectPath: project.CanonicalPath,
		Status:      appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	_, live, _ := web.navigationTreeInputs(t.Context())
	if len(live) != 1 {
		t.Fatalf("live entries = %d, want 1", len(live))
	}
	if live[0].Project != project {
		t.Fatalf("remote carried project = %+v, want %+v", live[0].Project, project)
	}
}

func TestAppThreadTreeEntriesPreserveRemoteLineageAndKind(t *testing.T) {
	meta, _, ok := appThreadTreeEntries(appwire.Thread{
		ID:     "child",
		Source: "remote",
		Evener: appwire.EvenerThread{
			Ref:       "remote:child",
			ParentRef: "remote:parent",
			Kind:      "subagent",
		},
	})
	if !ok {
		t.Fatal("appThreadTreeEntries rejected remote subagent")
	}
	if meta.ID != "remote:child" || meta.ParentSessionID != "remote:parent" || !meta.IsSubagent {
		t.Fatalf("remote subagent metadata = %+v", meta)
	}
}

// A remote host's session carries its unanswered question and its blocked
// escalation cards into the live entry, as a local session's probe does, so
// its rows show them and the approval promotes it into NeedsYou.
func TestAppThreadTreeEntriesCarryRemoteAskAndApproval(t *testing.T) {
	cards := []appwire.SandboxEscalationRequested{
		{ThreadID: "thread-remote", Ref: "remote:thread-remote", EscalationID: "esc_1", Tool: "write_file", Kind: "file_tool", DeniedPath: "/srv/docs/a.md"},
		{ThreadID: "thread-remote", Ref: "remote:thread-remote", EscalationID: "esc_2", Tool: "edit_file", Kind: "file_tool", DeniedPath: "/srv/docs/b.md"},
	}
	_, entry, ok := appThreadTreeEntries(appwire.Thread{
		ID:     "thread-remote",
		Source: "remote",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Ref: "remote:thread-remote", AskPending: true, PendingEscalations: cards},
	})
	if !ok {
		t.Fatal("appThreadTreeEntries rejected a valid remote thread")
	}
	if !entry.PendingAsk {
		t.Fatalf("entry = %+v, want the remote question carried", entry)
	}
	if !entry.PendingEscalation || !slices.Equal(entry.PendingEscalations, cards) {
		t.Fatalf("entry flag %v, cards %+v; want the flag and both cards in raise order", entry.PendingEscalation, entry.PendingEscalations)
	}

	_, quiet, ok := appThreadTreeEntries(appwire.Thread{
		ID:     "thread-quiet",
		Source: "remote",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Ref: "remote:thread-quiet"},
	})
	if !ok {
		t.Fatal("appThreadTreeEntries rejected a valid remote thread")
	}
	if quiet.PendingAsk || quiet.PendingEscalation || len(quiet.PendingEscalations) != 0 {
		t.Fatalf("entry = %+v, want no question and no approval on a working remote row", quiet)
	}
}

// A remote row's live entry carries the same thread-row facts a local probe
// does — the ask flag, cards, jobs, watches, turn end and tasks, and the row
// summary fields beside them — because both read the one shared reader (#2638).
// Before that reader, the remote tree built these fields by hand and dropped
// the ones below it did not list.
func TestAppThreadTreeEntriesCarryTheSharedThreadRowFacts(t *testing.T) {
	ended := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	tally := appwire.SubagentTally{Running: 1, Failed: 1, Done: 2}
	activity := &appwire.ThreadActivity{Minutes: []int{1, 2, 3}, LastActivityAt: ended.UnixMilli()}
	tasks := &appwire.TaskAggregate{Total: 3, Done: 1, Current: &appwire.TaskSummary{ID: 2, Description: "Resume the migration"}}
	question := &appwire.PendingQuestion{Question: "keep or drop?", Options: []string{"keep", "drop"}, Count: 1}
	failure := &appwire.ThreadFailure{Title: "Provider error"}
	thread := appwire.Thread{
		ID: "thread-remote", Source: "remote", ModelProvider: "gpt-5.6",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting, ActiveFlags: []string{"resumeRequired"}},
		Evener: appwire.EvenerThread{
			Ref:             "remote:thread-remote",
			AskPending:      true,
			PendingQuestion: question,
			Failure:         failure,
			Profile:         "codex-jesse-fsck.com",
			LastMessage:     "the opening line",
			Tasks:           tasks,
			Activity:        activity,
			Subagents:       &tally,
			LastTurnEndedAt: ended.UnixMilli(),
			Diagnostics: &appwire.EvenerDiagnostics{
				Jobs: []appwire.EvenerJobInfo{
					{JobID: "job-live", JobType: "shell", Status: "running"},
					{JobID: "job-done", JobType: "shell", Status: "completed"},
				},
				Watches: []appwire.EvenerWatchInfo{{ID: "watch-a", Source: "timer"}},
			},
		},
	}
	_, entry, ok := appThreadTreeEntries(thread)
	if !ok {
		t.Fatal("appThreadTreeEntries rejected a valid remote thread")
	}
	if entry.Status != appwire.ThreadStatusAwaiting {
		t.Errorf("entry status = %q, want %q", entry.Status, appwire.ThreadStatusAwaiting)
	}
	if !entry.PendingAsk || entry.PendingQuestion == nil || entry.PendingQuestion.Question != "keep or drop?" {
		t.Errorf("entry ask = %v/%+v, want the row's pending question", entry.PendingAsk, entry.PendingQuestion)
	}
	if entry.Failure == nil || entry.Failure.Title != "Provider error" {
		t.Errorf("entry failure = %+v, want the row's failure", entry.Failure)
	}
	if got := entry.RunningJobs; len(got) != 1 || got[0].JobID != "job-live" {
		t.Errorf("entry running jobs = %+v, want only job-live", got)
	}
	if got := entry.CompletedJobs; len(got) != 1 || got[0].JobID != "job-done" {
		t.Errorf("entry completed jobs = %+v, want only job-done", got)
	}
	if len(entry.Watches) != 1 || entry.Watches[0].ID != "watch-a" {
		t.Errorf("entry watches = %+v, want watch-a", entry.Watches)
	}
	if entry.Subagents != tally {
		t.Errorf("entry subagents = %+v, want %+v", entry.Subagents, tally)
	}
	if !entry.LastTurnEndedAt.Equal(ended) {
		t.Errorf("entry turn end = %v, want %v", entry.LastTurnEndedAt, ended)
	}
	if entry.Tasks == nil || entry.Tasks.Total != 3 {
		t.Errorf("entry tasks = %+v, want the row's task progress", entry.Tasks)
	}
	if entry.Activity == nil || len(entry.Activity.Minutes) != 3 {
		t.Errorf("entry activity = %+v, want the row's sample", entry.Activity)
	}
	if entry.Profile != "codex-jesse-fsck.com" || entry.LastMessage != "the opening line" || entry.CurrentModel != "gpt-5.6" {
		t.Errorf("entry profile/model/message = %q/%q/%q, want the row's", entry.Profile, entry.CurrentModel, entry.LastMessage)
	}
	// Only a local probe reads capabilities and status flags; the remote row
	// keeps the tree's own projection for those.
	if entry.CapabilitiesKnown || entry.ActiveFlags != nil {
		t.Errorf("entry capabilities/flags = %v/%v, want the remote row's own (none)", entry.CapabilitiesKnown, entry.ActiveFlags)
	}
}

// An older daemon omits the watches field entirely, and a probe that listed
// nothing carries no diagnostics. Both must project an empty watch list onto
// the tree entry rather than failing the thread.
func TestAppThreadTreeEntryWithoutWatchesYieldsEmptyList(t *testing.T) {
	for _, tc := range []struct {
		name string
		diag *appwire.EvenerDiagnostics
	}{
		{name: "nil diagnostics", diag: nil},
		{name: "diagnostics without watches", diag: &appwire.EvenerDiagnostics{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, entry, ok := appThreadTreeEntries(appwire.Thread{
				ID:     "thread-empty",
				Source: "remote",
				Evener: appwire.EvenerThread{Ref: "remote:thread-empty", Diagnostics: tc.diag},
			})
			if !ok {
				t.Fatal("appThreadTreeEntries rejected a valid remote thread")
			}
			if len(entry.Watches) != 0 {
				t.Fatalf("entry.Watches = %+v, want empty when diagnostics omit Watches", entry.Watches)
			}
		})
	}
}

// A watch is reported by the session's own daemon. Two sessions that can both
// see a receiver watch each carry their own diagnostics rows; the entry must
// never aggregate the other session's rows, or a rollup would double count.
func TestAppThreadTreeEntryCarriesOnlyItsOwnWatches(t *testing.T) {
	threadA := appwire.Thread{
		ID:     "thread-a",
		Source: "remote",
		Evener: appwire.EvenerThread{
			Ref: "remote:thread-a",
			Diagnostics: &appwire.EvenerDiagnostics{Watches: []appwire.EvenerWatchInfo{
				{ID: "watch-a", Source: "timer", Note: "owner watch"},
			}},
		},
	}
	threadB := appwire.Thread{
		ID:     "thread-b",
		Source: "remote",
		Evener: appwire.EvenerThread{
			Ref: "remote:thread-b",
			Diagnostics: &appwire.EvenerDiagnostics{Watches: []appwire.EvenerWatchInfo{
				{ID: "watch-b", Source: "output", Note: "receiver watch"},
			}},
		},
	}

	_, entryA, okA := appThreadTreeEntries(threadA)
	_, entryB, okB := appThreadTreeEntries(threadB)
	if !okA || !okB {
		t.Fatalf("entries rejected: a=%v b=%v", okA, okB)
	}
	if len(entryA.Watches) != 1 || entryA.Watches[0].ID != "watch-a" {
		t.Fatalf("session A watches = %+v, want only watch-a", entryA.Watches)
	}
	if len(entryB.Watches) != 1 || entryB.Watches[0].ID != "watch-b" {
		t.Fatalf("session B watches = %+v, want only watch-b", entryB.Watches)
	}
	for _, watch := range entryB.Watches {
		if watch.ID == "watch-a" {
			t.Fatalf("session B carries session A's watch: %+v", entryB.Watches)
		}
	}
}
