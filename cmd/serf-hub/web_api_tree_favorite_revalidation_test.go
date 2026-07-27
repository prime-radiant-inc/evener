package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
)

var favoriteRevalidationTreeTime = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func TestAPITreeFavoriteRevalidation_EndedOfflineSessionRemainsPinned(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	sessionID := hubtest.SessionID(t)
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", sessionID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, []schema.SessionMeta{{
		ID: sessionID, CreatedAt: favoriteRevalidationTreeTime.Add(-time.Hour), UpdatedAt: favoriteRevalidationTreeTime,
	}})

	response := getTreeResponse(t, web)
	node, ok := treeResponseNode(response, sessionID)
	if !ok || !node.Favorite {
		t.Fatalf("ended offline session node = %+v, want favorite=true", node)
	}
	if !treeResponseHasFavorite(response, sessionID) {
		t.Fatalf("ended offline session missing from pinned favorites: %+v", response.Favorites)
	}
}

func TestAPITreeFavoriteRevalidation_CappedSessionRemainsPinned(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	metas := make([]schema.SessionMeta, 0, 51)
	targetID := ""
	for i := range 51 {
		id := hubtest.SessionID(t)
		if i == 50 {
			targetID = id
		}
		at := favoriteRevalidationTreeTime.Add(-time.Duration(i) * time.Minute)
		metas = append(metas, schema.SessionMeta{
			ID: id, Name: fmt.Sprintf("task %02d", i), CreatedAt: at, UpdatedAt: at,
		})
	}
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", targetID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, metas)

	response := getTreeResponse(t, web)
	project := onlyActiveTreeProject(t, response)
	if project.MoreCurrent != 1 || len(project.Sessions) != 50 {
		t.Fatalf("presentation cap = rows %d more %d, want 50 rows and 1 overflow", len(project.Sessions), project.MoreCurrent)
	}
	if _, ok := treeResponseNode(response, targetID); ok {
		t.Fatalf("oldest capped session unexpectedly appeared in the ordinary tree rows")
	}
	if !treeResponseHasFavorite(response, targetID) {
		t.Fatalf("capped valid favorite missing from pinned favorites: %+v", response.Favorites)
	}
}

func TestAPITreeFavoriteRevalidation_ConfirmedInvalidRowsRemainUnchanged(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	parentID := hubtest.SessionID(t)
	subagentID := hubtest.SessionID(t)
	forkID := hubtest.SessionID(t)
	branchID := hubtest.SessionID(t)
	malformedID := hubtest.SessionID(t)
	metas := []schema.SessionMeta{
		{ID: parentID, UpdatedAt: favoriteRevalidationTreeTime},
		{ID: subagentID, ParentSessionID: parentID, IsSubagent: true, UpdatedAt: favoriteRevalidationTreeTime},
		{ID: forkID, ForkLabel: "before edit", UpdatedAt: favoriteRevalidationTreeTime},
		{ID: branchID, ParentSessionID: forkID, UpdatedAt: favoriteRevalidationTreeTime},
		{ID: malformedID, ParentSessionID: "missing-parent", IsSubagent: true, UpdatedAt: favoriteRevalidationTreeTime},
	}
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	for _, id := range []string{subagentID, forkID} {
		if err := favorites.Set("session", id, true, favoriteRevalidationTreeTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := favorites.Set("session", malformedID, false, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := favoriteRevalidationWeb(t, favorites, metas)

	response := getTreeResponse(t, web)
	for _, id := range []string{subagentID, forkID} {
		if treeResponseHasFavorite(response, id) {
			t.Fatalf("confirmed-invalid session %q appeared in pinned favorites", id)
		}
		if node, ok := treeResponseNode(response, id); ok && node.Favorite {
			t.Fatalf("confirmed-invalid session %q was rendered as favorite: %+v", id, node)
		}
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("confirmed-invalid/false favorite rows changed during tree read")
	}
}

func TestAPITreeFavoriteRevalidation_RemoteFailureDormantThenRecovers(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	threadID := hubtest.SessionID(t)
	remoteID := "remote:" + threadID
	source := &revalidationRemoteSource{
		scriptedAppSource: scriptedAppSource{id: "remote"},
		response: appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID: threadID, Source: "remote", Serf: appwire.SerfThread{Ref: remoteID},
			CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		}}},
	}
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", remoteID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)

	_ = getTreeResponse(t, web)
	before := favoriteDecisionRows(t, favorites)
	source.err = errors.New("remote unavailable")
	dormant := getTreeResponse(t, web)
	if treeResponseHasFavorite(dormant, remoteID) {
		t.Fatalf("last-known-good remote favorite remained visible after source failure")
	}
	if node, ok := treeResponseNode(dormant, remoteID); ok && node.Favorite {
		t.Fatalf("last-known-good remote row remained rendered as favorite: %+v", node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("remote source failure changed stored favorite rows")
	}

	source.err = nil
	recovered := getTreeResponse(t, web)
	if !treeResponseHasFavorite(recovered, remoteID) {
		t.Fatalf("favorite did not reappear after authoritative remote recovery: %+v", recovered.Favorites)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("remote recovery changed stored favorite rows")
	}
}

func TestAPITreeFavoriteRevalidation_AmbiguousAuthorityIsDormant(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	id := hubtest.SessionID(t)
	ambiguousID := "local:" + id
	missingID := hubtest.SessionID(t)
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", ambiguousID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	if err := favorites.Set("session", missingID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := favoriteRevalidationWeb(t, favorites, []schema.SessionMeta{
		{ID: id, UpdatedAt: favoriteRevalidationTreeTime},
		{ID: ambiguousID, UpdatedAt: favoriteRevalidationTreeTime},
	})

	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, ambiguousID) {
		t.Fatalf("ambiguous local/ref authority appeared in pinned favorites")
	}
	if treeResponseHasFavorite(response, missingID) {
		t.Fatalf("missing authority appeared in pinned favorites")
	}
	if node, ok := treeResponseNode(response, ambiguousID); ok && node.Favorite {
		t.Fatalf("ambiguous local/ref authority received a favorite flag: %+v", node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("ambiguous authority changed stored favorite rows")
	}
}

func TestAPITreeFavoriteRevalidation_ClusterSpellingDoesNotDecideValidity(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	legitimateID := "cluster:" + hubtest.SessionID(t)
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", legitimateID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, []schema.SessionMeta{{
		ID: legitimateID, Name: "real session", UpdatedAt: favoriteRevalidationTreeTime,
	}})
	response := getTreeResponse(t, web)
	if node, ok := treeResponseNode(response, legitimateID); !ok || !node.Favorite {
		t.Fatalf("legitimate cluster-shaped session = %+v, want favorite=true", node)
	}
	if !treeResponseHasFavorite(response, legitimateID) {
		t.Fatalf("legitimate cluster-shaped session missing from pinned favorites")
	}

	clusterMetas := make([]schema.SessionMeta, 0, 3)
	for i := range 3 {
		id := hubtest.SessionID(t)
		clusterMetas = append(clusterMetas, schema.SessionMeta{
			ID: id, Name: "same title", UpdatedAt: favoriteRevalidationTreeTime.Add(-time.Duration(i) * time.Minute),
		})
	}
	clusterWeb := favoriteRevalidationWeb(t, hubcore.NewFavoriteStore(t.TempDir()+"/index.db"), clusterMetas)
	clusterResponse := getTreeResponse(t, clusterWeb)
	clusterNode := findClusterNode(t, clusterResponse)
	if err := clusterWeb.cfg.Favorite.Set("session", clusterNode.Ref, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	clusterResponse = getTreeResponse(t, clusterWeb)
	if treeResponseHasFavorite(clusterResponse, clusterNode.Ref) {
		t.Fatalf("synthetic cluster row received a favorite flag: %+v", clusterResponse.Favorites)
	}
	if node, ok := treeResponseNode(clusterResponse, clusterNode.Ref); ok && node.Favorite {
		t.Fatalf("synthetic cluster row was presented as favorite: %+v", node)
	}
}

func TestAPITreeFavoriteRevalidation_ArchivedSessionStaysOutOfPinnedTier(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	sessionID := hubtest.SessionID(t)
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", sessionID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, []schema.SessionMeta{{
		ID: sessionID, UpdatedAt: favoriteRevalidationTreeTime.Add(-15 * 24 * time.Hour),
	}})

	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, sessionID) {
		t.Fatalf("archived favorite entered pinned tier")
	}
	if len(response.ArchivedProjects) != 1 || response.ArchivedProjects[0].SessionCount != 1 {
		t.Fatalf("archived session was not retained by archive presentation: %+v", response.ArchivedProjects)
	}

	archive := hubcore.NewArchiveStore(t.TempDir() + "/index.db")
	if err := archive.Set("session", sessionID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	liveWeb := NewWebServer(hubcore.WebConfig{
		Favorite: favorites,
		Archive:  archive,
		Past:     hubcore.NewPastIndex(""),
		Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{
			SessionID: sessionID, Status: appwire.ThreadStatusActive,
		}),
	})
	liveWeb.injectMetasForTest([]schema.SessionMeta{{ID: sessionID, UpdatedAt: favoriteRevalidationTreeTime}})
	if liveResponse := getTreeResponse(t, liveWeb); treeResponseHasFavorite(liveResponse, sessionID) {
		t.Fatalf("archived live favorite entered pinned tier")
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

func TestAPITreeFavoriteRevalidation_UsesOneSourceGeneration(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	firstID := hubtest.SessionID(t)
	secondID := hubtest.SessionID(t)
	firstRef := "remote:" + firstID
	secondRef := "remote:" + secondID
	source := &revalidationRemoteSource{
		scriptedAppSource: scriptedAppSource{id: "remote"},
		responses: []appwire.ThreadListResponse{
			{Data: []appwire.Thread{{ID: firstID, Source: "remote", Serf: appwire.SerfThread{Ref: firstRef}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}}}},
			{Data: []appwire.Thread{{ID: secondID, Source: "remote", Serf: appwire.SerfThread{Ref: secondRef}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}}}},
		},
	}
	favorites := hubcore.NewFavoriteStore(t.TempDir() + "/index.db")
	if err := favorites.Set("session", firstRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)

	response := getTreeResponse(t, web)
	if source.calls != 1 {
		t.Fatalf("source list calls=%d, want exactly one snapshot read", source.calls)
	}
	if !treeResponseHasFavorite(response, firstRef) {
		t.Fatalf("favorite from the tree snapshot was not presented: %+v", response.Favorites)
	}
	if treeResponseHasFavorite(response, secondRef) {
		t.Fatalf("second source generation leaked into first response")
	}
}

func TestAPITreeFavoriteRevalidation_ConcurrentCacheReadUsesOneSnapshot(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	threadID := hubtest.SessionID(t)
	remoteID := "remote:" + threadID
	thread := appwire.Thread{
		ID: threadID, Source: "remote", Serf: appwire.SerfThread{Ref: remoteID},
		CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
	}
	cache := &hubcore.RemoteThreadCache{}
	cache.StoreSnapshot([]appwire.Thread{thread}, true)
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", remoteID, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Favorite: favorites, Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})

	previousInputs := hubNavigationInputs
	firstSnapshotReady := make(chan struct{})
	releaseFirstSnapshot := make(chan struct{})
	var calls atomic.Int32
	hubNavigationInputs = func(server *WebServer, ctx context.Context) navigationSnapshot {
		snapshot := previousInputs(server, ctx)
		if calls.Add(1) == 1 {
			close(firstSnapshotReady)
			<-releaseFirstSnapshot
		}
		return snapshot
	}
	t.Cleanup(func() {
		hubNavigationInputs = previousInputs
	})

	type result struct {
		status int
		body   hubapi.TreeResponse
	}
	request := func() result {
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tree", nil))
		var body hubapi.TreeResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("decode concurrent tree response: %v", err)
			}
		}
		return result{status: rec.Code, body: body}
	}
	firstDone := make(chan result, 1)
	go func() { firstDone <- request() }()
	select {
	case <-firstSnapshotReady:
	case <-time.After(time.Second):
		t.Fatal("first tree request did not reach the snapshot interleave")
	}

	cache.StoreSnapshot([]appwire.Thread{thread}, false)
	secondDone := make(chan result, 1)
	go func() { secondDone <- request() }()
	var second result
	select {
	case second = <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second tree request did not complete during the interleave")
	}
	close(releaseFirstSnapshot)
	var first result
	select {
	case first = <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first tree request did not complete after the interleave")
	}
	for name, got := range map[string]result{"first": first, "second": second} {
		if got.status != http.StatusOK {
			t.Fatalf("%s tree status=%d, want 200", name, got.status)
		}
	}
	if !treeResponseHasFavorite(first.body, remoteID) {
		t.Fatalf("first request paired its rows with the later incomplete authority: %+v", first.body.Favorites)
	}
}

func TestAPITreeFavoriteRevalidation_HealthySourceSurvivesUnrelatedFailure(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	healthyID := hubtest.SessionID(t)
	healthyRef := "healthy:" + healthyID
	healthy := &revalidationRemoteSource{
		scriptedAppSource: scriptedAppSource{id: "healthy"},
		response: appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID: healthyID, Source: "healthy", Serf: appwire.SerfThread{Ref: healthyRef},
			CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(),
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed},
		}}},
	}
	failing := &revalidationRemoteSource{scriptedAppSource: scriptedAppSource{id: "failing"}, err: errors.New("source unavailable")}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", healthyRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(healthy)
	web.sources.Add(failing)

	response := getTreeResponse(t, web)
	if !treeResponseHasFavorite(response, healthyRef) {
		t.Fatalf("healthy source favorite was hidden by unrelated failure: %+v", response.Favorites)
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

func TestAPITreeFavoriteRevalidation_PaginatedSnapshotIncludesCappedAwayFavorite(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	threads := make([]appwire.Thread, 0, 51)
	for i := range 50 {
		id := hubtest.SessionID(t)
		threads = append(threads, revalidationClosedThread("remote", id, favoriteRevalidationTreeTime.Add(-time.Duration(i)*time.Minute)))
	}
	targetID := hubtest.SessionID(t)
	targetRef := "remote:" + targetID
	source := &paginatedRevalidationSource{
		scriptedAppSource: scriptedAppSource{id: "remote"},
		pages: map[string]appwire.ThreadListResponse{
			"":       {Data: threads, NextCursor: "page-2"},
			"page-2": {Data: []appwire.Thread{revalidationClosedThread("remote", targetID, favoriteRevalidationTreeTime.Add(-time.Hour))}},
		},
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", targetRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)

	response := getTreeResponse(t, web)
	if !treeResponseHasFavorite(response, targetRef) {
		t.Fatalf("favorite from terminal page was not presented: %+v", response.Favorites)
	}
}

func TestAPITreeFavoriteRevalidation_LaterPageFailureDormantsLastGoodRows(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	targetID := hubtest.SessionID(t)
	targetRef := "remote:" + targetID
	source := &paginatedRevalidationSource{
		scriptedAppSource: scriptedAppSource{id: "remote"},
		pages: map[string]appwire.ThreadListResponse{
			"":       {Data: []appwire.Thread{revalidationClosedThread("remote", targetID, favoriteRevalidationTreeTime)}, NextCursor: "page-2"},
			"page-2": {},
		},
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", targetRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)
	first := getTreeResponse(t, web)
	if !treeResponseHasFavorite(first, targetRef) {
		t.Fatalf("setup page did not present target favorite: %+v", first.Favorites)
	}
	before := favoriteDecisionRows(t, favorites)
	source.failCursor = "page-2"
	second := getTreeResponse(t, web)
	if treeResponseHasFavorite(second, targetRef) {
		t.Fatalf("later-page failure kept a last-good favorite visible")
	}
	if node, ok := treeResponseNode(second, targetRef); !ok || node.Favorite {
		t.Fatalf("later-page failure did not render retained row as non-favorite: found=%t node=%+v", ok, node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("later-page failure changed stored favorite rows")
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

func TestAPITreeFavoriteRevalidation_SourceRefConflictAndMalformedRowDoNotHideHealthyIdentity(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	conflictID := hubtest.SessionID(t)
	validID := hubtest.SessionID(t)
	malformedID := hubtest.SessionID(t)
	conflictRef := "remote-b:" + conflictID
	validRef := "remote-a:" + validID
	malformedRef := "remote-a:" + malformedID
	source := &revalidationRemoteSource{
		scriptedAppSource: scriptedAppSource{id: "remote-a"},
		response: appwire.ThreadListResponse{Data: []appwire.Thread{
			{ID: conflictID, Source: "remote-a", Serf: appwire.SerfThread{Ref: conflictRef}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}},
			{ID: validID, Source: "remote-a", Serf: appwire.SerfThread{Ref: validRef}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}},
			{ID: malformedID, Source: "remote-a", Serf: appwire.SerfThread{Ref: "malformed"}, CreatedAt: favoriteRevalidationTreeTime.Unix(), UpdatedAt: favoriteRevalidationTreeTime.Unix(), Status: appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}},
		}},
	}
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	for _, id := range []string{conflictRef, validRef, malformedRef} {
		if err := favorites.Set("session", id, true, favoriteRevalidationTreeTime); err != nil {
			t.Fatal(err)
		}
	}
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.sources.Add(source)
	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, conflictRef) {
		t.Fatalf("conflicting source/ref identity was treated as valid")
	}
	if treeResponseHasFavorite(response, malformedRef) {
		t.Fatalf("malformed ref identity was treated as valid")
	}
	if !treeResponseHasFavorite(response, validRef) {
		t.Fatalf("valid unrelated remote identity was hidden by malformed rows: %+v", response.Favorites)
	}
	for _, id := range []string{conflictRef, malformedRef} {
		if node, ok := treeResponseNode(response, id); ok && node.Favorite {
			t.Fatalf("invalid remote row %q was rendered as favorite: %+v", id, node)
		}
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

func TestAPITreeFavoriteRevalidation_RemoteCandidateWithoutOwningSourceSnapshotIsDormant(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	threadID := hubtest.SessionID(t)
	ref := "remote:" + threadID
	thread := revalidationClosedThread("remote", threadID, favoriteRevalidationTreeTime)
	cache := &hubcore.RemoteThreadCache{}
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads:  []appwire.Thread{thread},
		Complete: true,
		Sources:  map[string]hubcore.RemoteSourceSnapshot{},
	})
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", ref, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.cfg.RemoteThreadCache = cache

	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, ref) {
		t.Fatalf("remote candidate without owning source snapshot was pinned")
	}
	if node, ok := treeResponseNode(response, ref); !ok || node.Favorite {
		t.Fatalf("remote candidate without owning source snapshot node = found %t, %+v; want rendered non-favorite", ok, node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("missing remote source snapshot changed stored favorite row")
	}
}

func TestAPITreeFavoriteRevalidation_RemoteCandidateMissingExactSourceMembershipIsDormant(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	targetID := hubtest.SessionID(t)
	otherID := hubtest.SessionID(t)
	targetRef := "remote:" + targetID
	other := revalidationClosedThread("remote", otherID, favoriteRevalidationTreeTime)
	target := revalidationClosedThread("remote", targetID, favoriteRevalidationTreeTime)
	cache := &hubcore.RemoteThreadCache{}
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads:  []appwire.Thread{target},
		Complete: true,
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"remote": {Threads: []appwire.Thread{other}, Complete: true},
		},
	})
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", targetRef, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.cfg.RemoteThreadCache = cache

	response := getTreeResponse(t, web)
	if treeResponseHasFavorite(response, targetRef) {
		t.Fatalf("remote candidate absent from exact source membership was pinned")
	}
	if node, ok := treeResponseNode(response, targetRef); !ok || node.Favorite {
		t.Fatalf("remote candidate absent from exact source membership node = found %t, %+v; want rendered non-favorite", ok, node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("missing remote source membership changed stored favorite row")
	}
}

func TestAPITreeFavoriteRevalidation_RemoteCandidateWithExactSourceMembershipIsValid(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	threadID := hubtest.SessionID(t)
	ref := "remote:" + threadID
	thread := revalidationClosedThread("remote", threadID, favoriteRevalidationTreeTime)
	cache := &hubcore.RemoteThreadCache{}
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Threads:  []appwire.Thread{thread},
		Complete: true,
		Sources: map[string]hubcore.RemoteSourceSnapshot{
			"remote": {Threads: []appwire.Thread{thread}, Complete: true},
		},
	})
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", ref, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := favoriteRevalidationWeb(t, favorites, nil)
	web.cfg.RemoteThreadCache = cache

	response := getTreeResponse(t, web)
	if !treeResponseHasFavorite(response, ref) {
		t.Fatalf("remote candidate with exact complete source membership was not pinned")
	}
	if node, ok := treeResponseNode(response, ref); !ok || !node.Favorite {
		t.Fatalf("remote candidate with exact complete source membership node = found %t, %+v; want rendered favorite", ok, node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("valid remote source membership changed stored favorite row")
	}
}

func TestAPITreeFavoriteRevalidation_LocalCandidateUnaffectedByMissingRemoteSourceMetadata(t *testing.T) {
	useFavoriteRevalidationTreeClock(t)
	threadID := hubtest.SessionID(t)
	ref := "local:" + threadID
	cache := &hubcore.RemoteThreadCache{}
	cache.StoreSnapshotData(hubcore.RemoteThreadSnapshot{
		Complete: true,
		Sources:  map[string]hubcore.RemoteSourceSnapshot{},
	})
	favorites := hubcore.NewFavoriteStore(filepath.Join(t.TempDir(), "index.db"))
	if err := favorites.Set("session", ref, true, favoriteRevalidationTreeTime); err != nil {
		t.Fatal(err)
	}
	before := favoriteDecisionRows(t, favorites)
	web := favoriteRevalidationWeb(t, favorites, []schema.SessionMeta{{
		ID: ref, CreatedAt: favoriteRevalidationTreeTime.Add(-time.Hour), UpdatedAt: favoriteRevalidationTreeTime,
	}})
	web.cfg.RemoteThreadCache = cache

	response := getTreeResponse(t, web)
	if !treeResponseHasFavorite(response, ref) {
		t.Fatalf("local candidate was downgraded when remote source metadata was absent")
	}
	if node, ok := treeResponseNode(response, ref); !ok || !node.Favorite {
		t.Fatalf("local candidate node = found %t, %+v; want rendered favorite", ok, node)
	}
	if !reflect.DeepEqual(before, favoriteDecisionRows(t, favorites)) {
		t.Fatalf("local candidate check changed stored favorite row")
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
	web := NewWebServer(hubcore.WebConfig{Favorite: favorites, Past: hubcore.NewPastIndex("")})
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
	for _, node := range response.Favorites {
		if node.Ref == ref || node.SessionID == ref {
			return true
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

func onlyActiveTreeProject(t *testing.T, response hubapi.TreeResponse) hubapi.TreeProject {
	t.Helper()
	if len(response.Projects) != 1 {
		t.Fatalf("active projects=%d, want one: %+v", len(response.Projects), response.Projects)
	}
	return response.Projects[0]
}

func findClusterNode(t *testing.T, response hubapi.TreeResponse) hubapi.TreeNode {
	t.Helper()
	for _, project := range response.Projects {
		for _, node := range project.Sessions {
			if node.Kind == "cluster" {
				return node
			}
		}
	}
	t.Fatal("tree did not contain a cluster node")
	return hubapi.TreeNode{}
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
