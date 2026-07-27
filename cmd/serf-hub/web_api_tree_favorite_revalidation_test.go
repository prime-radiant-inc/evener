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
	"reflect"
	"sort"
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

type revalidationRemoteSource struct {
	scriptedAppSource
	response  appwire.ThreadListResponse
	responses []appwire.ThreadListResponse
	err       error
	calls     int
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
