package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubtest"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/rendezvous"
)

var favoriteRevalidationTreeTime = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func TestAPITreePinSectionsDoNotRenderInvalidAssignmentsOrDeleteThem(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	parentID := hubtest.SessionID(t)
	subagentID := hubtest.SessionID(t)
	forkID := hubtest.SessionID(t)
	branchID := hubtest.SessionID(t)
	metas := []schema.SessionMeta{
		{ID: parentID, UpdatedAt: favoriteRevalidationTreeTime},
		{ID: subagentID, ParentSessionID: parentID, IsSubagent: true, UpdatedAt: favoriteRevalidationTreeTime},
		{ID: forkID, ForkLabel: "before edit", UpdatedAt: favoriteRevalidationTreeTime},
		{ID: branchID, ParentSessionID: forkID, UpdatedAt: favoriteRevalidationTreeTime},
	}
	web, pins := namedPinTreeWeb(t, metas)
	section, _, err := pins.CreateOrReuseAndAssign("Research", subagentID, favoriteRevalidationTreeTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pins.Assign(section.ID, forkID, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	response := getTreeResponse(t, web)
	if len(response.PinSections) != 0 {
		t.Fatalf("invalid assignments rendered: %+v", response.PinSections)
	}
	assignments, err := pins.Assignments()
	if err != nil || len(assignments) != 2 {
		t.Fatalf("invalid assignments were mutated: %+v, %v", assignments, err)
	}
}

func TestAPITreeFavoriteRevalidation_FavoriteStoreReadFailureFailsTree(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	dbPath := t.TempDir() + "/favorite-db"
	if err := osMkdir(dbPath); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, hubcore.NewFavoriteStore(dbPath), nil)

	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tree", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("favorite read failure status=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
}

func TestAPITreeFavoriteRevalidation_MemoCapturesInputsVersionBeforeSnapshot(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	stateRoot := t.TempDir()
	projectDir := filepath.Join(stateRoot, "project-a-0123456789")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldID := hubtest.SessionID(t)
	newID := hubtest.SessionID(t)
	oldMeta := schema.SessionMeta{ID: oldID, OriginalPrompt: "old", UpdatedAt: favoriteRevalidationTreeTime}
	newMeta := schema.SessionMeta{ID: newID, OriginalPrompt: "new", UpdatedAt: favoriteRevalidationTreeTime}
	if err := schema.SaveSessionMeta(projectDir, oldMeta); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	inputs := &hubcore.InputsVersion{}
	past.SetOnChange(inputs.Bump)
	web := NewWebServer(hubcore.WebConfig{Inputs: inputs, Past: past, RemoteThreadCache: &hubcore.RemoteThreadCache{}})
	oldTree, _, _, oldAuthority := web.memoTreeWithAuthority(context.Background())
	if !treeContainsID(oldTree, oldID) || !favoriteAuthorityHasSession(oldAuthority, oldID) {
		t.Fatalf("old memo generation = tree %t authority %t, want old session", treeContainsID(oldTree, oldID), favoriteAuthorityHasSession(oldAuthority, oldID))
	}

	previousInputs := hubNavigationInputs
	snapshotEntered := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	var releaseSnapshotOnce sync.Once
	releaseSnapshotNow := func() { releaseSnapshotOnce.Do(func() { close(releaseSnapshot) }) }
	var snapshotCalls atomic.Int32
	hubNavigationInputs = func(server *WebServer, ctx context.Context) navigationSnapshot {
		snapshot := previousInputs(server, ctx)
		if snapshotCalls.Add(1) == 1 {
			close(snapshotEntered)
			<-releaseSnapshot
		}
		return snapshot
	}
	t.Cleanup(func() {
		hubNavigationInputs = previousInputs
	})
	type memoResult struct {
		tree      hubcore.Tree
		authority hubcore.FavoriteAuthority
	}
	firstDone := make(chan memoResult, 1)
	firstFinished := make(chan struct{})
	go func() {
		defer close(firstFinished)
		tree, _, _, authority := web.memoTreeWithAuthority(context.Background())
		firstDone <- memoResult{tree: tree, authority: authority}
	}()
	t.Cleanup(func() {
		releaseSnapshotNow()
		select {
		case <-firstFinished:
		case <-time.After(time.Second):
			t.Errorf("first memo goroutine did not join during cleanup")
		}
	})
	select {
	case <-snapshotEntered:
	case <-time.After(time.Second):
		t.Fatal("memo request did not reach the snapshot interleave")
	}

	if err := os.Remove(filepath.Join(projectDir, "sessions", oldID+".meta.json")); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(projectDir, newMeta); err != nil {
		t.Fatal(err)
	}
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if inputs.Load() == 0 {
		t.Fatal("Past.Rebuild did not bump inputs during the snapshot interleave")
	}
	releaseSnapshotNow()
	var first memoResult
	select {
	case first = <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("memo request did not complete after the snapshot interleave")
	}

	nextTree, _, _, nextAuthority := web.memoTreeWithAuthority(context.Background())
	if !treeContainsID(nextTree, newID) || treeContainsID(nextTree, oldID) {
		t.Fatalf("following memo generation = old %t new %t, want only new session", treeContainsID(nextTree, oldID), treeContainsID(nextTree, newID))
	}
	if !favoriteAuthorityHasSession(nextAuthority, newID) || favoriteAuthorityHasSession(nextAuthority, oldID) {
		t.Fatalf("following authority = old %t new %t, want only new session", favoriteAuthorityHasSession(nextAuthority, oldID), favoriteAuthorityHasSession(nextAuthority, newID))
	}
	if !treeContainsID(first.tree, oldID) || treeContainsID(first.tree, newID) {
		t.Fatalf("interleaved memo generation = old %t new %t, want the old snapshot", treeContainsID(first.tree, oldID), treeContainsID(first.tree, newID))
	}
}

func TestAPITreeFavoriteRevalidation_MemoReturnsOneGenerationDuringPastRebuildGap(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	stateRoot := t.TempDir()
	projectDir := filepath.Join(stateRoot, "project-b-0123456789")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldID := hubtest.SessionID(t)
	newID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(projectDir, schema.SessionMeta{ID: oldID, OriginalPrompt: "old", UpdatedAt: favoriteRevalidationTreeTime}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	inputs := &hubcore.InputsVersion{}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:     rendezvous.Entry{PID: 1},
		SessionID: oldID,
		Status:    "active",
	})
	web := NewWebServer(hubcore.WebConfig{Inputs: inputs, Past: past, Roster: roster, RemoteThreadCache: &hubcore.RemoteThreadCache{}})
	oldTree, _, oldLive, oldAuthority := web.memoTreeWithAuthority(context.Background())
	if !treeContainsID(oldTree, oldID) || !liveEntriesHaveSession(oldLive, oldID) || !favoriteAuthorityHasSession(oldAuthority, oldID) {
		t.Fatalf("old memo generation did not contain old tree/live/authority")
	}

	if err := os.Remove(filepath.Join(projectDir, "sessions", oldID+".meta.json")); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(projectDir, schema.SessionMeta{ID: newID, OriginalPrompt: "new", UpdatedAt: favoriteRevalidationTreeTime}); err != nil {
		t.Fatal(err)
	}
	published := make(chan struct{})
	releaseBump := make(chan struct{})
	past.SetOnChange(func() {
		close(published)
		<-releaseBump
		inputs.Bump()
	})
	rebuildDone := make(chan struct{})
	var releaseBumpOnce sync.Once
	releaseBumpNow := func() { releaseBumpOnce.Do(func() { close(releaseBump) }) }
	go func() {
		if _, err := past.Rebuild(); err != nil {
			t.Errorf("Past.Rebuild: %v", err)
		}
		close(rebuildDone)
	}()
	t.Cleanup(func() {
		releaseBumpNow()
		select {
		case <-rebuildDone:
		case <-time.After(time.Second):
			t.Errorf("Past.Rebuild goroutine did not join during cleanup")
		}
	})
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("Past.Rebuild did not publish data before onChange")
	}

	gapTree, _, gapLive, gapAuthority := web.memoTreeWithAuthority(context.Background())
	if !treeContainsID(gapTree, oldID) || treeContainsID(gapTree, newID) {
		t.Fatalf("gap tree = old %t new %t, want cached old generation", treeContainsID(gapTree, oldID), treeContainsID(gapTree, newID))
	}
	if !liveEntriesHaveSession(gapLive, oldID) || liveEntriesHaveSession(gapLive, newID) {
		t.Fatalf("gap live = old %t new %t, want cached old generation", liveEntriesHaveSession(gapLive, oldID), liveEntriesHaveSession(gapLive, newID))
	}
	if !favoriteAuthorityHasSession(gapAuthority, oldID) || favoriteAuthorityHasSession(gapAuthority, newID) {
		t.Fatalf("gap authority = old %t new %t, want cached old generation", favoriteAuthorityHasSession(gapAuthority, oldID), favoriteAuthorityHasSession(gapAuthority, newID))
	}

	releaseBumpNow()
	select {
	case <-rebuildDone:
	case <-time.After(time.Second):
		t.Fatal("Past.Rebuild did not complete after releasing onChange")
	}
	nextTree, _, nextLive, nextAuthority := web.memoTreeWithAuthority(context.Background())
	if !treeContainsID(nextTree, newID) || !favoriteAuthorityHasSession(nextAuthority, newID) {
		t.Fatalf("post-bump memo did not expose current Past generation: tree new=%t authority new=%t", treeContainsID(nextTree, newID), favoriteAuthorityHasSession(nextAuthority, newID))
	}
	if !liveEntriesHaveSession(nextLive, oldID) {
		t.Fatalf("post-bump memo lost the current live roster entry")
	}
}

func TestAPITreeFavoriteRevalidation_EmptyRemoteCacheDoesNotDowngradeLocalClusterAuthority(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	metas := make([]schema.SessionMeta, 0, 3)
	for i := range 3 {
		metas = append(metas, schema.SessionMeta{
			ID: hubtest.SessionID(t), Name: "same title",
			CreatedAt: favoriteRevalidationTreeTime.Add(-time.Duration(i) * time.Minute),
			UpdatedAt: favoriteRevalidationTreeTime.Add(-time.Duration(i) * time.Minute),
		})
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	cache := &hubcore.RemoteThreadCache{}
	web := favoriteRevalidationWeb(t, favorites, metas)
	web.cfg.RemoteThreadCache = cache
	tree, _, _, authority := web.memoTreeWithAuthority(context.Background())
	var clusterID string
	for _, node := range tree.FavoriteNodeAuthorities() {
		if node.Kind == hubcore.FavoriteNodeCluster {
			clusterID = node.ID
			break
		}
	}
	if clusterID == "" {
		t.Fatal("tree did not produce a local cluster authority")
	}
	key := hubcore.ArchiveKey{Kind: "session", ID: clusterID}
	classification := hubcore.ClassifyFavoriteDecisions(map[hubcore.ArchiveKey]bool{key: true}, authority).Classifications[key]
	if classification.State != hubcore.FavoriteDecisionConfirmedInvalid {
		t.Fatalf("empty unrelated remote cache changed local cluster classification to %q", classification.State)
	}
}

func TestAPITreeFavoriteRevalidation_RepeatedPageCursorIsIncomplete(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	targetID := hubtest.SessionID(t)
	targetRef := "remote:" + targetID
	source := &paginatedRevalidationSource{
		scriptedAppSource: scriptedAppSource{id: "remote"},
		pages: map[string]appwire.ThreadListResponse{
			"":     {Data: []appwire.Thread{revalidationClosedThread("remote", targetID, favoriteRevalidationTreeTime)}, NextCursor: "loop"},
			"loop": {NextCursor: "loop"},
		},
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", targetRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)
	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, targetRef) {
		t.Fatalf("repeated cursor was treated as complete authority")
	}
}

func TestAPITreeFavoriteRevalidation_SourceFieldConflictWithoutRefIsDormant(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	id := hubtest.SessionID(t)
	ref := "remote-a:" + id
	source := &revalidationRemoteSource{
		scriptedAppSource: scriptedAppSource{id: "remote-a"},
		response: appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID: id, Source: "remote-b", CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		}}},
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", ref, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)
	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, ref) {
		t.Fatalf("source field conflict without ref was treated as valid")
	}
}

func TestAPITreeFavoriteRevalidation_MalformedParentRefIsDormant(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	parentID := hubtest.SessionID(t)
	childID := hubtest.SessionID(t)
	parentRef := "remote:" + parentID
	childRef := "remote:" + childID
	source := &revalidationRemoteSource{
		scriptedAppSource: scriptedAppSource{id: "remote"},
		response: appwire.ThreadListResponse{Data: []appwire.Thread{
			{ID: parentID, Source: "remote", Serf: appwire.SerfThread{Ref: parentRef}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}},
			{ID: childID, Source: "remote", Serf: appwire.SerfThread{Ref: childRef, ParentRef: parentID}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}},
		}},
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", childRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)
	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, childRef) {
		t.Fatalf("malformed parent ref was treated as complete lineage")
	}
}

func TestAPITreeFavoriteRevalidation_EndedRemoteCarriedProjectCanBeFavorited(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	threadID := hubtest.SessionID(t)
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: threadID, Source: "remote", CWD: filepath.Join(projectDir, "ended-worktree"),
		ProjectID: project.ID, ProjectPath: project.CanonicalPath,
		CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
	}})
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("project", project.ID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Favorite: favorites, Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})
	response := getTreeResponse(t, web)
	for _, candidate := range append(append([]hubapi.TreeProject{}, response.Projects...), response.ArchivedProjects...) {
		if candidate.Key == project.ID && !candidate.Favorite {
			t.Fatalf("ended remote carried project was not rendered favorite: %+v", candidate)
		}
	}
	if !treeResponseProjectHasFavorite(response, project.ID) {
		t.Fatalf("ended remote carried project was absent from tree: %+v", response)
	}
}

func TestAPITreeFavoriteRevalidation_IdenticalRemoteDuplicatesMakeCarriedProjectDormant(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	threadID := hubtest.SessionID(t)
	thread := appwire.Thread{
		ID: threadID, Source: "remote", CWD: filepath.Join(projectDir, "ended-worktree"),
		ProjectID: project.ID, ProjectPath: project.CanonicalPath,
		Serf:      appwire.SerfThread{Ref: "remote:" + threadID},
		CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
	}
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{thread, thread})
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("project", project.ID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := NewWebServer(hubcore.WebConfig{Favorite: favorites, Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})

	response := getTreeResponse(t, web)
	var found bool
	for _, candidate := range append(append([]hubapi.TreeProject{}, response.Projects...), response.ArchivedProjects...) {
		if candidate.Key == project.ID {
			found = true
			if candidate.Favorite {
				t.Fatalf("identical remote duplicate made carried project favorite: %+v", candidate)
			}
		}
	}
	if !found {
		t.Fatalf("identical remote duplicate removed carried project row: %+v", response)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("identical remote duplicate changed stored favorite rows")
	}
}

func TestAPITreeFavoriteRevalidation_ProjectClaimsWithSameIDAreAmbiguous(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	projectAPath := filepath.Join(t.TempDir(), "project-a")
	projectBPath := filepath.Join(t.TempDir(), "project-b")
	for _, path := range []string{projectAPath, projectBPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	project, err := identifier.ResolveProject(projectAPath)
	if err != nil {
		t.Fatal(err)
	}
	threads := []appwire.Thread{
		{ID: hubtest.SessionID(t), Source: "remote-a", CWD: filepath.Join(projectAPath, "worktree"), ProjectID: project.ID, ProjectPath: projectAPath, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		{ID: hubtest.SessionID(t), Source: "remote-b", CWD: filepath.Join(projectBPath, "worktree"), ProjectID: project.ID, ProjectPath: projectBPath, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	}
	cache := &hubcore.RemoteThreadCache{}
	cache.Store(threads)
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("project", project.ID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Favorite: favorites, Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})
	response := getTreeResponse(t, web)
	for _, candidate := range append(append([]hubapi.TreeProject{}, response.Projects...), response.ArchivedProjects...) {
		if candidate.Key == project.ID && candidate.Favorite {
			t.Fatalf("conflicting project claims were collapsed into a valid favorite: %+v", candidate)
		}
	}
}

func TestFavoriteProjectAuthorities_LocalColonIdentitySharesLocalClaim(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "project-local-colon")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	colonID := "cluster:" + hubtest.SessionID(t)
	normalID := hubtest.SessionID(t)
	key := hubcore.ArchiveKey{Kind: "project", ID: project.ID}
	var wantStates map[string]hubcore.FavoriteDecisionState
	for _, test := range []struct {
		name  string
		metas []schema.SessionMeta
	}{
		{name: "colon then normal", metas: []schema.SessionMeta{{ID: colonID}, {ID: normalID}}},
		{name: "normal then colon", metas: []schema.SessionMeta{{ID: normalID}, {ID: colonID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for i := range test.metas {
				test.metas[i].EnvInfo.WorkingDir = projectPath
			}
			authority := hubcore.FavoriteAuthority{Projects: favoriteProjectAuthorities(navigationSnapshot{
				metas:    test.metas,
				projects: map[string]identifier.Project{projectPath: project},
			})}
			if len(authority.Projects) != 1 {
				t.Fatalf("project claims = %+v, want one complete local claim", authority.Projects)
			}
			claim := authority.Projects[0]
			if claim.Quality != hubcore.FavoriteAuthorityComplete || claim.ClaimKey != project.CanonicalPath+"\x00local" {
				t.Fatalf("project claim = %+v, want complete local claim", claim)
			}
			got := hubcore.ClassifyFavoriteDecisions(map[hubcore.ArchiveKey]bool{key: true}, authority)
			assertFavoriteDecisionClassification(t, got, key, hubcore.FavoriteDecisionValid)
			state := got.Classifications[key].State
			if wantStates == nil {
				wantStates = map[string]hubcore.FavoriteDecisionState{"state": state}
			} else if wantStates["state"] != state {
				t.Fatalf("row-order state = %q, want %q", state, wantStates["state"])
			}
		})
	}
}

func TestFavoriteProjectAuthorities_ConflictingCarriedProjectsAreDormantRegardlessOfRowOrder(t *testing.T) {
	projectAPath := filepath.Join(t.TempDir(), "project-carried-a")
	projectBPath := filepath.Join(t.TempDir(), "project-carried-b")
	for _, path := range []string{projectAPath, projectBPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectA, err := identifier.ResolveProject(projectAPath)
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := identifier.ResolveProject(projectBPath)
	if err != nil {
		t.Fatal(err)
	}
	workingDir := filepath.Join(t.TempDir(), "shared-worktree")
	idA := hubtest.SessionID(t)
	idB := hubtest.SessionID(t)
	threads := []appwire.Thread{
		{
			ID: idA, Source: "remote", CWD: workingDir,
			ProjectID: projectA.ID, ProjectPath: projectA.CanonicalPath,
			CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		},
		{
			ID: idB, Source: "remote", CWD: workingDir,
			ProjectID: projectB.ID, ProjectPath: projectB.CanonicalPath,
			CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		},
	}
	decisions := map[hubcore.ArchiveKey]bool{
		{Kind: "project", ID: projectA.ID}: true,
		{Kind: "project", ID: projectB.ID}: true,
	}
	var wantStates map[hubcore.ArchiveKey]hubcore.FavoriteDecisionState
	wantPresentationProjectID := ""
	for _, test := range []struct {
		name    string
		threads []appwire.Thread
	}{
		{name: "a then b", threads: threads},
		{name: "b then a", threads: []appwire.Thread{threads[1], threads[0]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := &hubcore.RemoteThreadCache{}
			cache.Store(test.threads)
			web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
			snapshot := web.navigationSnapshotInputs(t.Context())
			presentationProject, ok := snapshot.projects[workingDir]
			if !ok {
				t.Fatalf("shared working directory lost its deterministic presentation project: %+v", snapshot.projects)
			}
			if wantPresentationProjectID == "" {
				wantPresentationProjectID = presentationProject.ID
			} else if wantPresentationProjectID != presentationProject.ID {
				t.Fatalf("row-order presentation project = %q, want %q", presentationProject.ID, wantPresentationProjectID)
			}
			authority := hubcore.FavoriteAuthority{Projects: favoriteProjectAuthorities(snapshot)}
			if len(authority.Projects) != 2 {
				t.Fatalf("conflicting carried project claims = %+v, want both identities retained", authority.Projects)
			}
			for _, projectID := range []string{projectA.ID, projectB.ID} {
				var found bool
				for _, claim := range authority.Projects {
					if claim.ID == projectID {
						found = true
						if claim.Quality != hubcore.FavoriteAuthorityIncomplete {
							t.Fatalf("conflicting carried project %q claim = %+v, want incomplete", projectID, claim)
						}
					}
				}
				if !found {
					t.Fatalf("conflicting carried project %q was not retained: %+v", projectID, authority.Projects)
				}
			}
			got := hubcore.ClassifyFavoriteDecisions(decisions, authority)
			states := make(map[hubcore.ArchiveKey]hubcore.FavoriteDecisionState, len(decisions))
			for key := range decisions {
				classification, ok := got.Classifications[key]
				if !ok {
					t.Fatalf("classification missing for %v", key)
				}
				if classification.State == hubcore.FavoriteDecisionValid {
					t.Fatalf("conflicting carried project %v was authorized: authority=%+v", key, authority.Projects)
				}
				states[key] = classification.State
			}
			if wantStates == nil {
				wantStates = states
			} else if !reflect.DeepEqual(wantStates, states) {
				t.Fatalf("row-order states = %v, want %v", states, wantStates)
			}
		})
	}
}

func TestFavoriteProjectAuthorities_CarriedProjectConflictsWithLocalIdentity(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "project-local")
	remotePath := filepath.Join(t.TempDir(), "project-remote")
	for _, path := range []string{localPath, remotePath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	localProject, err := identifier.ResolveProject(localPath)
	if err != nil {
		t.Fatal(err)
	}
	remoteProject, err := identifier.ResolveProject(remotePath)
	if err != nil {
		t.Fatal(err)
	}
	remoteID := hubtest.SessionID(t)
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: remoteID, Source: "remote", CWD: localProject.CanonicalPath,
		ProjectID: remoteProject.ID, ProjectPath: remoteProject.CanonicalPath,
		CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
	}})
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})
	web.injectMetasForTest([]schema.SessionMeta{{
		ID: hubtest.SessionID(t), UpdatedAt: favoriteRevalidationTreeTime,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: localProject.CanonicalPath},
	}})
	snapshot := web.navigationSnapshotInputs(t.Context())
	decisions := map[hubcore.ArchiveKey]bool{
		{Kind: "project", ID: localProject.ID}:  true,
		{Kind: "project", ID: remoteProject.ID}: true,
	}
	authority := hubcore.FavoriteAuthority{Projects: favoriteProjectAuthorities(snapshot)}
	for _, projectID := range []string{localProject.ID, remoteProject.ID} {
		var found bool
		for _, claim := range authority.Projects {
			if claim.ID == projectID {
				found = true
				if claim.Quality != hubcore.FavoriteAuthorityIncomplete {
					t.Fatalf("local/carried project %q claim = %+v, want incomplete", projectID, claim)
				}
			}
		}
		if !found {
			t.Fatalf("local/carried project %q was not retained: %+v", projectID, authority.Projects)
		}
	}
	got := hubcore.ClassifyFavoriteDecisions(decisions, authority)
	for key := range decisions {
		classification, ok := got.Classifications[key]
		if !ok {
			t.Fatalf("classification missing for %v", key)
		}
		if classification.State == hubcore.FavoriteDecisionValid {
			t.Fatalf("local/carried project conflict %v was authorized", key)
		}
	}
}

func TestFavoriteProjectAuthorities_IncompleteClaimDominatesRegardlessOfRowOrder(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "project-a")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	completeID := hubtest.SessionID(t)
	malformedID := "remote:"
	key := hubcore.ArchiveKey{Kind: "project", ID: project.ID}
	for _, test := range []struct {
		name  string
		metas []schema.SessionMeta
	}{
		{name: "complete then malformed", metas: []schema.SessionMeta{{ID: completeID}, {ID: malformedID}}},
		{name: "malformed then complete", metas: []schema.SessionMeta{{ID: malformedID}, {ID: completeID}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for i := range test.metas {
				test.metas[i].EnvInfo.WorkingDir = projectPath
			}
			authority := hubcore.FavoriteAuthority{Projects: favoriteProjectAuthorities(navigationSnapshot{
				metas:               test.metas,
				projects:            map[string]identifier.Project{projectPath: project},
				remoteIncompleteIDs: map[string]struct{}{malformedID: {}},
			})}
			got := hubcore.ClassifyFavoriteDecisions(map[hubcore.ArchiveKey]bool{key: true}, authority)
			assertFavoriteDecisionClassification(t, got, key, hubcore.FavoriteDecisionDormant)
		})
	}
}

func TestFavoriteProjectAuthorities_EmptyIDOnlyIsDormant(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "project-b")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	key := hubcore.ArchiveKey{Kind: "project", ID: project.ID}
	authority := hubcore.FavoriteAuthority{Projects: favoriteProjectAuthorities(navigationSnapshot{
		metas:    []schema.SessionMeta{{EnvInfo: schema.EnvironmentInfo{WorkingDir: projectPath}}},
		projects: map[string]identifier.Project{projectPath: project},
	})}
	got := hubcore.ClassifyFavoriteDecisions(map[hubcore.ArchiveKey]bool{key: true}, authority)
	assertFavoriteDecisionClassification(t, got, key, hubcore.FavoriteDecisionDormant)
}

func TestFavoriteProjectAuthorities_EmptyIDPlusValidLocalIsDormant(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "project-c")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	validID := hubtest.SessionID(t)
	key := hubcore.ArchiveKey{Kind: "project", ID: project.ID}
	for _, metas := range [][]schema.SessionMeta{
		{{EnvInfo: schema.EnvironmentInfo{WorkingDir: projectPath}}, {ID: validID, EnvInfo: schema.EnvironmentInfo{WorkingDir: projectPath}}},
		{{ID: validID, EnvInfo: schema.EnvironmentInfo{WorkingDir: projectPath}}, {EnvInfo: schema.EnvironmentInfo{WorkingDir: projectPath}}},
	} {
		authority := hubcore.FavoriteAuthority{Projects: favoriteProjectAuthorities(navigationSnapshot{
			metas:    metas,
			projects: map[string]identifier.Project{projectPath: project},
		})}
		got := hubcore.ClassifyFavoriteDecisions(map[hubcore.ArchiveKey]bool{key: true}, authority)
		assertFavoriteDecisionClassification(t, got, key, hubcore.FavoriteDecisionDormant)
	}
}

func TestAPITreeFavoriteRevalidation_MemoKeyDoesNotCollideAcrossInputsAndRemoteGeneration(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	firstID := hubtest.SessionID(t)
	secondID := hubtest.SessionID(t)
	cache := &hubcore.RemoteThreadCache{}
	firstThread := revalidationClosedThread("remote", firstID, favoriteRevalidationTreeTime)
	firstThread.Status.Type = appwire.ThreadStatusActive
	cache.Store([]appwire.Thread{firstThread})
	cache.Store([]appwire.Thread{firstThread})
	sharedTreeCache := &hubcore.TreeCache{}
	firstInputs := &hubcore.InputsVersion{}
	firstInputs.Bump()
	firstWeb := NewWebServer(hubcore.WebConfig{Inputs: firstInputs, RemoteThreadCache: cache, Past: hubcore.NewPastIndex("")})
	firstWeb.treeCache = sharedTreeCache
	firstTree, _, _, _ := firstWeb.memoTreeWithAuthority(context.Background())
	if !treeContainsID(firstTree, "remote:"+firstID) {
		t.Fatalf("first memoized tree omitted first generation row")
	}

	secondThread := revalidationClosedThread("remote", secondID, favoriteRevalidationTreeTime)
	secondThread.Status.Type = appwire.ThreadStatusActive
	cache.Store([]appwire.Thread{secondThread})
	cache.Store([]appwire.Thread{secondThread})
	secondWeb := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache, Past: hubcore.NewPastIndex("")})
	secondWeb.treeCache = sharedTreeCache
	secondTree, _, _, _ := secondWeb.memoTreeWithAuthority(context.Background())
	if treeContainsID(secondTree, "remote:"+firstID) || !treeContainsID(secondTree, "remote:"+secondID) {
		t.Fatalf("memo key reused a different input/generation pair: first=%t second=%t", treeContainsID(secondTree, "remote:"+firstID), treeContainsID(secondTree, "remote:"+secondID))
	}
}

type revalidationRemoteSource struct {
	scriptedAppSource
	response  appwire.ThreadListResponse
	responses []appwire.ThreadListResponse
	err       error
	calls     int
}

type paginatedRevalidationSource struct {
	scriptedAppSource
	pages      map[string]appwire.ThreadListResponse
	failCursor string
	cursors    []string
}

func (s *paginatedRevalidationSource) ListThreads(_ context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.cursors = append(s.cursors, params.Cursor)
	if s.failCursor != "" && params.Cursor == s.failCursor {
		return appwire.ThreadListResponse{}, errors.New("page unavailable")
	}
	return s.pages[params.Cursor], nil
}

func revalidationClosedThread(sourceID, id string, at time.Time) appwire.Thread {
	return appwire.Thread{
		ID: id, Source: sourceID, Serf: appwire.SerfThread{Ref: sourceID + ":" + id},
		CreatedAt: at.Unix(), UpdatedAt: at.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
	}
}

func (s *revalidationRemoteSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.calls++
	if s.err != nil {
		return appwire.ThreadListResponse{}, s.err
	}
	if len(s.responses) > 0 {
		index := s.calls - 1
		if index >= len(s.responses) {
			index = len(s.responses) - 1
		}
		return s.responses[index], nil
	}
	return s.response, nil
}

func useFavoriteRevalidationTreeClock(t *testing.T) {
	t.Helper()
	previous := hubBuildNavigationTree
	hubBuildNavigationTree = func(metas []schema.SessionMeta, live []hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool, projects map[string]identifier.Project) hubcore.Tree {
		return hubcore.BuildTreeAtWithProjects(metas, live, decisions, favoriteRevalidationTreeTime, projects)
	}
	t.Cleanup(func() { hubBuildNavigationTree = previous })
}

func favoriteRevalidationWeb(t *testing.T, favorites *hubcore.FavoriteStore, metas []schema.SessionMeta) *WebServer {
	t.Helper()
	dbPath := reflect.ValueOf(favorites).Elem().FieldByName("dbPath").String()
	web := NewWebServer(hubcore.WebConfig{
		Favorite: favorites, PinSections: hubcore.NewPinSectionStore(dbPath), Past: hubcore.NewPastIndex(""),
	})
	web.injectMetasForTest(metas)
	return web
}

func getTreeResponse(t *testing.T, web *WebServer) hubapi.TreeResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tree", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("tree status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response hubapi.TreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode tree response: %v", err)
	}
	return response
}

func treeResponseHasFavorite(response hubapi.TreeResponse, ref string) bool {
	for _, section := range response.PinSections {
		for _, node := range section.Sessions {
			if node.Ref == ref || node.SessionID == ref {
				return true
			}
		}
	}
	return false
}

func treeResponseProjectHasFavorite(response hubapi.TreeResponse, key string) bool {
	for _, project := range append(append([]hubapi.TreeProject{}, response.Projects...), response.ArchivedProjects...) {
		if project.Key == key && project.Favorite {
			return true
		}
	}
	return false
}

func treeContainsID(tree hubcore.Tree, id string) bool {
	var walk func([]hubcore.TreeNode) bool
	walk = func(nodes []hubcore.TreeNode) bool {
		for _, node := range nodes {
			if node.ID == id || walk(node.Children) {
				return true
			}
		}
		return false
	}
	if walk(tree.Live) || walk(tree.NeedsYou) {
		return true
	}
	for _, project := range append(append([]hubcore.TreeProject{}, tree.Projects...), tree.ArchivedProjects...) {
		if walk(project.Current) || walk(project.Recent) || walk(project.Archived) {
			return true
		}
	}
	return false
}

func liveEntriesHaveSession(entries []hubcore.LiveEntry, id string) bool {
	for _, entry := range entries {
		if entry.SessionID == id {
			return true
		}
	}
	return false
}

func favoriteAuthorityHasSession(authority hubcore.FavoriteAuthority, id string) bool {
	for _, session := range authority.Sessions {
		if session.ID == id {
			return true
		}
	}
	return false
}

func assertFavoriteDecisionClassification(t *testing.T, got hubcore.FavoriteRevalidation, key hubcore.ArchiveKey, want hubcore.FavoriteDecisionState) {
	t.Helper()
	classification, ok := got.Classifications[key]
	if !ok {
		t.Fatalf("classification missing for %v: %#v", key, got.Classifications)
	}
	if classification.State != want {
		t.Fatalf("classification for %v = %q, want %q", key, classification.State, want)
	}
}

func treeResponseNode(response hubapi.TreeResponse, ref string) (hubapi.TreeNode, bool) {
	var walk func([]hubapi.TreeNode) (hubapi.TreeNode, bool)
	walk = func(nodes []hubapi.TreeNode) (hubapi.TreeNode, bool) {
		for _, node := range nodes {
			if node.Ref == ref || node.SessionID == ref {
				return node, true
			}
			if found, ok := walk(node.Children); ok {
				return found, true
			}
		}
		return hubapi.TreeNode{}, false
	}
	if node, ok := walk(response.Live); ok {
		return node, true
	}
	if node, ok := walk(response.NeedsYou); ok {
		return node, true
	}
	for _, project := range response.Projects {
		if node, ok := walk(project.Sessions); ok {
			return node, true
		}
	}
	return hubapi.TreeNode{}, false
}

type favoriteDecisionRow struct {
	kind      string
	id        string
	favorited int
	decidedAt int64
}

func favoriteDecisionRows(t *testing.T, store *hubcore.FavoriteStore) []favoriteDecisionRow {
	t.Helper()
	dbPath := reflect.ValueOf(store).Elem().FieldByName("dbPath").String()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, favorited, decided_at FROM favorite ORDER BY kind, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []favoriteDecisionRow
	for rows.Next() {
		var row favoriteDecisionRow
		if err := rows.Scan(&row.kind, &row.id, &row.favorited, &row.decidedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		return out[i].id < out[j].id
	})
	return out
}

func osMkdir(path string) error {
	return os.Mkdir(path, 0o755)
}
