package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

const navigationTestSessionID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type testNavigationSource struct {
	mu           sync.Mutex
	revision     uint64
	inputs       navigationBuildInputs
	nextBoundary time.Time
	err          error
	captures     int
	entered      chan struct{}
	release      chan struct{}
	captured     chan struct{}
	enterOnce    sync.Once
}

func newTestNavigationSource(now time.Time) *testNavigationSource {
	node := hubcore.TreeNode{ID: navigationTestSessionID, Title: "one", Project: "p1", Kind: "session", State: "idle", UpdatedAt: now.Add(-time.Hour)}
	project := hubcore.TreeProject{Key: "p1", Name: "p1", Current: []hubcore.TreeNode{node}}
	return &testNavigationSource{revision: 1, inputs: navigationBuildInputs{Tree: hubcore.Tree{Projects: []hubcore.TreeProject{project}}}}
}

func (s *testNavigationSource) Revision() navigationSourceRevision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return navigationSourceRevision{Inputs: s.revision}
}

func (s *testNavigationSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	s.mu.Lock()
	s.captures++
	if s.captured != nil {
		select {
		case s.captured <- struct{}{}:
		default:
		}
	}
	inputs := cloneNavigationInputs(s.inputs)
	inputs.Tree = s.inputs.Tree.Snapshot()
	inputs.GenerationID = generation
	inputs.Revision = 0
	next := s.nextBoundary
	err := s.err
	entered, release := s.entered, s.release
	s.mu.Unlock()
	if entered != nil {
		s.enterOnce.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return navigationSourceSnapshot{}, ctx.Err()
		case <-release:
		}
	}
	if err != nil {
		return navigationSourceSnapshot{}, err
	}
	return navigationSourceSnapshot{Inputs: inputs, NextBoundary: next}, nil
}

func (s *testNavigationSource) changeTitle(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs.Tree.Projects[0].Current[0].Title = title
	s.revision++
}

// retitle changes the captured tree without advancing the source revision, so
// builds stay non-stale while resource fingerprints still move.
func (s *testNavigationSource) retitle(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs.Tree.Projects[0].Current[0].Title = title
}

func (s *testNavigationSource) captureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captures
}

func newTestNavigationService(t *testing.T, source *testNavigationSource, options ...func(*navigationServiceConfig)) *NavigationService {
	t.Helper()
	cfg := navigationServiceConfig{
		Source:     source,
		Generation: func() (string, error) { return "00112233445566778899aabbccddeeff", nil },
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	for _, option := range options {
		option(&cfg)
	}
	return newNavigationService(cfg)
}

func TestNavigationServiceUsesOneCoreSnapshotAndStableNoOpRevisions(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)

	manifest, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest})
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if source.captureCount() != 1 {
		t.Fatalf("captures = %d, want one core snapshot", source.captureCount())
	}
	if manifest.Generation != project.Generation || manifest.Revision == 0 || project.Revision == 0 {
		t.Fatalf("manifest=%+v project=%+v", manifest, project)
	}

	before := service.Stats()
	mutation, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mutation.Targets) != 0 {
		t.Fatalf("no-op targets = %+v", mutation.Targets)
	}
	if got := service.CurrentRevision((navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}).Semantic()); got != project.Revision {
		t.Fatalf("project revision = %d, want stable %d", got, project.Revision)
	}
	if after := service.Stats(); after.CoreBuilds != before.CoreBuilds+1 {
		t.Fatalf("core builds before=%+v after=%+v", before, after)
	}
}

func TestNavigationServiceCacheBudgetCountsObjectAndBothEncodings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.mu.Lock()
	source.inputs.Tree.Projects[0].Current = make([]hubcore.TreeNode, 200)
	for index := range source.inputs.Tree.Projects[0].Current {
		source.inputs.Tree.Projects[0].Current[index] = hubcore.TreeNode{
			ID:        fmt.Sprintf("01ARZ3NDEKTSV4RRFFQ69G%03d", index),
			Title:     strings.Repeat("compressible navigation title ", 20),
			Project:   "p1",
			Kind:      "session",
			State:     "idle",
			UpdatedAt: now,
		}
	}
	source.mu.Unlock()
	const budget = int64(32 << 10)
	cache := newNavigationRepresentationCache(8, budget)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Cache = cache
	})

	representation, err := service.Representation(t.Context(), navigationResourceKey{
		Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldEstimate := int64(len(representation.JSON) + len(representation.Gzip))
	retainedEstimate := int64(2*len(representation.JSON) + len(representation.Gzip))
	if oldEstimate > budget || retainedEstimate <= budget {
		t.Fatalf("fixture estimates old=%d retained=%d budget=%d, want old within and retained above budget", oldEstimate, retainedEstimate, budget)
	}
	if representation.SizeEstimate != retainedEstimate {
		t.Fatalf("representation size estimate=%d, want object+JSON+gzip estimate %d", representation.SizeEstimate, retainedEstimate)
	}
	if stats := cache.Stats(); stats.Entries != 0 || stats.Bytes != 0 || stats.Bytes > budget {
		t.Fatalf("oversized retained representation escaped budget: %+v (budget %d)", stats, budget)
	}
}

func TestNavigationServiceEmitsExactDependentTargetsAndWildcard(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}

	source.changeTitle("changed")
	mutation, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mutation.Targets) != 1 || mutation.Targets[0].Kind != appwire.NavigationTargetProject || mutation.Targets[0].ProjectKey != "p1" {
		t.Fatalf("targets = %+v, want only project p1", mutation.Targets)
	}

	mutation, err = service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mutation.Targets) != 0 {
		t.Fatalf("no-op wildcard targets = %+v", mutation.Targets)
	}
	if service.Capability().Sequence != 1 {
		t.Fatalf("sequence = %d, want one committed invalidation", service.Capability().Sequence)
	}

	source.changeTitle("wildcard")
	mutation, err = service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mutation.Targets) != 2 || mutation.Targets[len(mutation.Targets)-1].Kind != appwire.NavigationTargetAllLoadedProjects {
		t.Fatalf("precise + wildcard targets = %+v", mutation.Targets)
	}
	if service.Capability().Sequence != 2 {
		t.Fatalf("sequence = %d, want two committed invalidations", service.Capability().Sequence)
	}
}

func TestNavigationServiceRejectsSnapshotInvalidatedDuringBuild(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered = make(chan struct{})
	source.release = make(chan struct{})
	service := newTestNavigationService(t, source)
	done := make(chan navigationRepresentation, 1)
	errs := make(chan error, 1)
	go func() {
		representation, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"})
		done <- representation
		errs <- err
	}()
	<-source.entered
	source.changeTitle("fresh")
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	close(source.release)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	representation := <-done
	if representation.Revision != service.CurrentRevision((navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}).Semantic()) {
		t.Fatalf("published stale revision %d", representation.Revision)
	}
	if !bytes.Contains(representation.JSON, []byte("fresh")) {
		t.Fatalf("representation cached stale core bytes: %s", representation.JSON)
	}
	if source.captureCount() < 2 {
		t.Fatalf("captures = %d, want retry after invalidation", source.captureCount())
	}
}

func TestNavigationServiceLogicalOffPageChangeAndRemovalInvalidateProjectAndCatalog(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.mu.Lock()
	rows := make([]hubcore.TreeNode, 51)
	for index := range rows {
		rows[index] = hubcore.TreeNode{ID: fmt.Sprintf("%026d", index+1), Title: fmt.Sprintf("row-%d", index), Project: "p1", Kind: "session", State: "idle", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()}
	}
	source.inputs.Tree.Projects[0].Current = rows
	source.mu.Unlock()
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}); err != nil {
		t.Fatal(err)
	}
	projectKey := (navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}).Semantic()
	firstRevision := service.CurrentRevision(projectKey)

	source.mu.Lock()
	source.inputs.Tree.Projects[0].Current[50].Title = "off-page-change"
	source.revision++
	source.mu.Unlock()
	mutation, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := service.CurrentRevision(projectKey); got <= firstRevision {
		t.Fatalf("off-page project revision = %d, want > %d", got, firstRevision)
	}
	if !hasNavigationTarget(mutation.Targets, appwire.NavigationTargetProject, "p1") {
		t.Fatalf("off-page targets = %+v, missing project", mutation.Targets)
	}

	source.mu.Lock()
	source.inputs.Tree.Projects[0].Current = source.inputs.Tree.Projects[0].Current[:50]
	source.revision++
	source.mu.Unlock()
	mutation, err = service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasNavigationTarget(mutation.Targets, appwire.NavigationTargetProject, "p1") || !hasNavigationTarget(mutation.Targets, appwire.NavigationTargetCatalog, "projects") {
		t.Fatalf("removal targets = %+v, want project and catalog", mutation.Targets)
	}
}

func hasNavigationTarget(targets []appwire.NavigationInvalidationTarget, kind appwire.NavigationTargetKind, selector string) bool {
	for _, target := range targets {
		if target.Kind != kind {
			continue
		}
		if kind == appwire.NavigationTargetProject && target.ProjectKey == selector {
			return true
		}
		if kind == appwire.NavigationTargetCatalog && target.Catalog == selector {
			return true
		}
	}
	return false
}

func TestNavigationServiceReusesRetainedProjectionAcrossResourceMisses(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	previous := buildNavigationServiceProjectionContext
	var builds atomic.Int32
	buildNavigationServiceProjectionContext = func(ctx context.Context, inputs navigationBuildInputs) (navigationProjection, error) {
		builds.Add(1)
		return previous(ctx, inputs)
	}
	t.Cleanup(func() { buildNavigationServiceProjectionContext = previous })
	service := newTestNavigationService(t, source)
	for _, key := range []navigationResourceKey{
		{Kind: navigationResourceManifest},
		{Kind: navigationResourceProject, ProjectKey: "p1"},
		{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current", Limit: 1},
		{Kind: navigationResourceLocation, ID: "local:" + navigationTestSessionID},
	} {
		if _, err := service.Representation(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("projection builds = %d, want one retained core build", got)
	}
}

func TestNavigationReadV2ExactCurrentDoesNotReinsertHistory(t *testing.T) {
	service := newTestNavigationService(t, newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC()))
	key := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}
	initial, err := service.readV2(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := appwire.NavigationReadBase{
		GenerationID: initial.Response.GenerationID,
		Revision:     initial.Response.Revision,
		ETag:         initial.Response.ETag,
	}

	history := newNavigationHistory(1, 1<<20)
	service.history = history
	current, err := service.readV2(t.Context(), key, &base)
	if err != nil {
		t.Fatal(err)
	}
	if current.Response.Status != "not_modified" {
		t.Fatalf("exact-current response = %+v, want not_modified", current.Response)
	}
	history.mu.Lock()
	entries, indexed, bytes := history.order.Len(), len(history.entries), history.bytes
	history.mu.Unlock()
	if entries != 0 || indexed != 0 || bytes != 0 {
		t.Fatalf("exact-current read rebuilt history: order=%d entries=%d bytes=%d, want empty", entries, indexed, bytes)
	}
}

func TestNavigationReadV2UsesOnlyExactCompleteViewBaseline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.mu.Lock()
	current := make([]hubcore.TreeNode, 6)
	archived := make([]hubcore.TreeNode, 6)
	for index := range current {
		current[index] = hubcore.TreeNode{ID: fmt.Sprintf("current-%02d", index), Title: fmt.Sprintf("current %d", index), Project: "p1", Kind: "session", State: "idle", UpdatedAt: now}
		archived[index] = hubcore.TreeNode{ID: fmt.Sprintf("archived-%02d", index), Title: fmt.Sprintf("archived %d", index), Project: "p1", Kind: "session", State: "ended", UpdatedAt: now}
	}
	source.inputs.Tree.Projects[0].Current = current
	source.inputs.Tree.Projects[0].Archived = archived
	source.mu.Unlock()
	service := newTestNavigationService(t, source)

	rootKey := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}
	currentKey := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current", Offset: 2, Limit: 2}
	archivedKey := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "archived", Offset: 2, Limit: 2}
	readSnapshot := func(key navigationResourceKey) appwire.NavigationReadBase {
		t.Helper()
		result, err := service.readV2(t.Context(), key, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Response.Representation != appwire.NavigationRepresentationSnapshot {
			t.Fatalf("initial %s representation = %q, want snapshot", key.Kind, result.Response.Representation)
		}
		return appwire.NavigationReadBase{GenerationID: result.Response.GenerationID, Revision: result.Response.Revision, ETag: result.Response.ETag}
	}
	rootBase := readSnapshot(rootKey)
	currentBase := readSnapshot(currentKey)
	archivedBase := readSnapshot(archivedKey)

	source.mu.Lock()
	source.inputs.Tree.Projects[0].Current[2].Title = "changed exact current page"
	source.revision++
	source.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}

	exact, err := service.readV2(t.Context(), currentKey, &currentBase)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Response.Representation != appwire.NavigationRepresentationDelta {
		t.Fatalf("exact current-page base representation = %q, want delta", exact.Response.Representation)
	}
	var delta hubapi.NavigationDelta
	if err := json.Unmarshal(exact.Response.Data, &delta); err != nil {
		t.Fatal(err)
	}
	foundChanged := false
	for _, entity := range delta.UpsertedEntities {
		if bytes.Contains(entity.Value, []byte("changed exact current page")) {
			foundChanged = true
		}
	}
	if !foundChanged {
		t.Fatalf("exact-view delta omitted changed entity: %+v", delta.UpsertedEntities)
	}

	for name, crossViewBase := range map[string]appwire.NavigationReadBase{
		"project root":  rootBase,
		"archived page": archivedBase,
	} {
		result, err := service.readV2(t.Context(), currentKey, &crossViewBase)
		if err != nil {
			t.Fatal(err)
		}
		if result.Response.Representation != appwire.NavigationRepresentationSnapshot {
			t.Errorf("%s base representation = %q, want snapshot fallback", name, result.Response.Representation)
		}
	}
}

func TestNavigationReadV2EvictedExactViewFallsBackWithoutRecordCounters(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.mu.Lock()
	source.inputs.Tree.Projects[0].Current = []hubcore.TreeNode{
		{ID: navigationTestSessionID, Title: "one", Project: "p1", Kind: "session", State: "idle", UpdatedAt: now.Add(-time.Hour)},
		{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Title: "two", Project: "p1", Kind: "session", State: "idle", UpdatedAt: now.Add(-time.Hour)},
	}
	source.mu.Unlock()
	// The service reserves the remainder of the default retention budget for
	// history, so maxEntries=255 gives this test exactly one history entry.
	cache := newNavigationRepresentationCache(255, 1<<20)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) { cfg.Cache = cache })
	originalKey := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current", Offset: 0, Limit: 1}
	otherKey := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current", Offset: 1, Limit: 1}
	type readBase = appwire.NavigationReadBase
	read := func(key navigationResourceKey) (readBase, hubapi.NavigationSnapshot) {
		t.Helper()
		result, err := service.readV2(t.Context(), key, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Response.Status != "ok" || result.Response.Representation != appwire.NavigationRepresentationSnapshot || result.Response.Base != nil {
			t.Fatalf("initial %s representation = %q, want snapshot", key, result.Response.Representation)
		}
		var snapshot hubapi.NavigationSnapshot
		if err := json.Unmarshal(result.Response.Data, &snapshot); err != nil {
			t.Fatal(err)
		}
		return readBase{GenerationID: result.Response.GenerationID, Revision: result.Response.Revision, ETag: result.Response.ETag}, snapshot
	}
	originalBase, originalSnapshot := read(originalKey)
	otherBase, otherSnapshot := read(otherKey)
	assertScope := func(key navigationResourceKey, snapshot hubapi.NavigationSnapshot, wantTitle string) {
		t.Helper()
		scope := navigationViewScope(key)
		if len(snapshot.Entities) != 1 || len(snapshot.Containers) == 0 {
			t.Fatalf("%s initial snapshot = %+v, want one entity and containers", key, snapshot)
		}
		for _, entity := range snapshot.Entities {
			if !strings.HasPrefix(entity.Key, scope+"/entity/") {
				t.Fatalf("%s entity key %q escaped requested scope %q", key, entity.Key, scope)
			}
			encoded, err := json.Marshal(entity)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(`"revision"`)) {
				t.Fatalf("%s entity %q emitted a record revision: %s", key, entity.Key, encoded)
			}
			if wantTitle != "" && !bytes.Contains(entity.Value, []byte(wantTitle)) {
				t.Fatalf("%s entity %q omitted title %q", key, entity.Key, wantTitle)
			}
		}
		for _, container := range snapshot.Containers {
			if !strings.HasPrefix(container.Key, scope+"/") {
				t.Fatalf("%s container key %q escaped requested scope %q", key, container.Key, scope)
			}
			encoded, err := json.Marshal(container)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(`"revision"`)) {
				t.Fatalf("%s container %q emitted a record revision: %s", key, container.Key, encoded)
			}
			if container.Owner.Kind == "entity" && !strings.HasPrefix(container.Owner.EntityKey, scope+"/entity/") {
				t.Fatalf("%s owner key %q escaped requested scope %q", key, container.Owner.EntityKey, scope)
			}
		}
	}
	assertScope(originalKey, originalSnapshot, "one")
	assertScope(otherKey, otherSnapshot, "two")

	source.mu.Lock()
	source.inputs.Tree.Projects[0].Current[0].Title = "original refreshed content"
	source.inputs.Tree.Projects[0].Current[1].Title = "evicted fresh content"
	source.revision++
	source.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	otherResult, err := service.readV2(t.Context(), otherKey, &otherBase)
	if err != nil {
		t.Fatal(err)
	}
	if otherResult.Response.Status != "ok" || otherResult.Response.Representation != appwire.NavigationRepresentationDelta {
		t.Fatalf("retained other base response = %+v, want delta", otherResult.Response)
	}
	if otherResult.Response.Base == nil || *otherResult.Response.Base != otherBase {
		t.Fatalf("retained other base echo = %+v, want exact %+v", otherResult.Response.Base, otherBase)
	}
	var delta hubapi.NavigationDelta
	if err := json.Unmarshal(otherResult.Response.Data, &delta); err != nil {
		t.Fatal(err)
	}
	if len(delta.UpsertedEntities) != 1 {
		t.Fatalf("retained other delta entities = %+v, want changed entity", delta.UpsertedEntities)
	}
	changedEntity := delta.UpsertedEntities[0]
	if scope := navigationViewScope(otherKey) + "/entity/"; !strings.HasPrefix(changedEntity.Key, scope) {
		t.Fatalf("retained other delta entity %q escaped requested scope %q", changedEntity.Key, scope)
	}
	if !bytes.Contains(changedEntity.Value, []byte("evicted fresh content")) {
		t.Fatalf("retained other delta entity = %+v, want changed content", changedEntity)
	}
	if otherResult.Response.Revision <= otherBase.Revision || otherResult.Response.ETag == otherBase.ETag {
		t.Fatalf("retained other authority = revision %d etag %q, want later than %+v", otherResult.Response.Revision, otherResult.Response.ETag, otherBase)
	}
	retainedOtherBase := appwire.NavigationReadBase{
		GenerationID: otherResult.Response.GenerationID,
		Revision:     otherResult.Response.Revision,
		ETag:         otherResult.Response.ETag,
	}
	crossView, err := service.readV2(t.Context(), originalKey, &retainedOtherBase)
	if err != nil {
		t.Fatal(err)
	}
	if crossView.Response.Status != "ok" || crossView.Response.Representation != appwire.NavigationRepresentationSnapshot || crossView.Response.Base != nil {
		t.Fatalf("cross-View base response = %+v, want snapshot without Base", crossView.Response)
	}
	var crossSnapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(crossView.Response.Data, &crossSnapshot); err != nil {
		t.Fatal(err)
	}
	assertScope(originalKey, crossSnapshot, "original refreshed content")
	if crossView.Response.Revision <= originalBase.Revision {
		t.Fatalf("cross-View snapshot revision = %d, want later than original base %d", crossView.Response.Revision, originalBase.Revision)
	}

	result, err := service.readV2(t.Context(), originalKey, &originalBase)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Status != "ok" || result.Response.Representation != appwire.NavigationRepresentationSnapshot || result.Response.Base != nil {
		t.Fatalf("evicted base response = %+v, want current snapshot without Base", result.Response)
	}
	var snapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(result.Response.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	assertScope(originalKey, snapshot, "original refreshed content")
	if result.Response.Revision != crossView.Response.Revision || result.Response.ETag != crossView.Response.ETag {
		t.Fatalf("evicted fallback authority = revision %d etag %q, want current %d/%q", result.Response.Revision, result.Response.ETag, crossView.Response.Revision, crossView.Response.ETag)
	}
}

func TestNavigationReadV2PresentGoneTombstoneAndReappearLifecycle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	service := newTestNavigationService(t, source)
	key := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current", Offset: 0, Limit: 1}
	read := func(base *appwire.NavigationReadBase) appwire.NavigationReadResponse {
		t.Helper()
		result, err := service.readV2(t.Context(), key, base)
		if err != nil {
			t.Fatal(err)
		}
		return result.Response
	}
	present := read(nil)
	if present.Status != "ok" || present.Representation != appwire.NavigationRepresentationSnapshot || len(present.Data) == 0 {
		t.Fatalf("first present response = %+v, want snapshot with data", present)
	}
	presentBase := &appwire.NavigationReadBase{GenerationID: present.GenerationID, Revision: present.Revision, ETag: present.ETag}
	source.mu.Lock()
	source.inputs.Tree.Projects = nil
	source.revision++
	source.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	gone := read(presentBase)
	if gone.Status != "gone" || gone.Data != nil || gone.Revision <= present.Revision || gone.ETag == present.ETag {
		t.Fatalf("gone response = %+v, want later tombstone without data", gone)
	}
	tombstoneBase := &appwire.NavigationReadBase{GenerationID: gone.GenerationID, Revision: gone.Revision, ETag: gone.ETag}
	if notModified := read(tombstoneBase); notModified.Status != "not_modified" || notModified.Data != nil {
		t.Fatalf("exact tombstone response = %+v, want not_modified without data", notModified)
	}
	source.mu.Lock()
	source.inputs.Tree.Projects = []hubcore.TreeProject{{Key: "p1", Name: "p1", Current: []hubcore.TreeNode{{
		ID: navigationTestSessionID, Title: "reappeared", Project: "p1", Kind: "session", State: "idle", UpdatedAt: now,
	}}}}
	source.revision++
	source.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	reappeared := read(tombstoneBase)
	if reappeared.Status != "ok" || reappeared.Representation != appwire.NavigationRepresentationSnapshot || reappeared.Revision <= gone.Revision {
		t.Fatalf("reappeared response = %+v, want later snapshot", reappeared)
	}
	var snapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(reappeared.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entities) != 1 || !bytes.Contains(snapshot.Entities[0].Value, []byte("reappeared")) {
		t.Fatalf("reappeared entities = %+v, want only current row", snapshot.Entities)
	}
	neverKnown := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "never-known"}
	if _, err := service.readV2(t.Context(), neverKnown, nil); err == nil {
		t.Fatal("never-known resource fabricated a tombstone")
	}
}

func TestNavigationReadV2MissingDecisionDoesNotMixReappearedAuthority(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	service := newTestNavigationService(t, source)
	key := navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: "p1", Tier: "current", Offset: 0, Limit: 1}

	present, err := service.readV2(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	presentBase := &appwire.NavigationReadBase{
		GenerationID: present.Response.GenerationID,
		Revision:     present.Response.Revision,
		ETag:         present.Response.ETag,
	}
	source.mu.Lock()
	source.inputs.Tree.Projects = nil
	source.revision++
	source.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	tombstoneRevision := service.CurrentRevision(key.Semantic())
	tombstoneETag := navigationETag(key, present.Response.GenerationID, tombstoneRevision)

	previous := navigationReadV2MissingCaptured
	var refreshOnce sync.Once
	navigationReadV2MissingCaptured = func() {
		refreshOnce.Do(func() {
			source.mu.Lock()
			source.inputs.Tree.Projects = []hubcore.TreeProject{{Key: "p1", Name: "p1", Current: []hubcore.TreeNode{{
				ID: navigationTestSessionID, Title: "reappeared during read", Project: "p1", Kind: "session", State: "idle", UpdatedAt: now,
			}}}}
			source.revision++
			source.mu.Unlock()
			if _, refreshErr := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); refreshErr != nil {
				t.Errorf("reappear refresh: %v", refreshErr)
			}
		})
	}
	t.Cleanup(func() { navigationReadV2MissingCaptured = previous })

	interleaved, err := service.readV2(t.Context(), key, presentBase)
	if err != nil {
		t.Fatal(err)
	}
	presentRevision := service.CurrentRevision(key.Semantic())
	presentETag := navigationETag(key, present.Response.GenerationID, presentRevision)
	if interleaved.Response.Status == "gone" && (interleaved.Response.Revision == presentRevision || interleaved.Response.ETag == presentETag) {
		t.Fatalf("gone response mixed in reappeared authority: response=%+v present=%d/%q", interleaved.Response, presentRevision, presentETag)
	}
	if interleaved.Response.Status == "gone" && (interleaved.Response.Revision != tombstoneRevision || interleaved.Response.ETag != tombstoneETag) {
		t.Fatalf("gone response authority=%d/%q, want captured tombstone %d/%q", interleaved.Response.Revision, interleaved.Response.ETag, tombstoneRevision, tombstoneETag)
	}
	if interleaved.Response.Status != "gone" && (interleaved.Response.Status != "ok" || interleaved.Response.Representation != appwire.NavigationRepresentationSnapshot || !bytes.Contains(interleaved.Response.Data, []byte("reappeared during read"))) {
		t.Fatalf("interleaved response = %+v, want captured tombstone or current snapshot", interleaved.Response)
	}

	if interleaved.Response.Status == "gone" {
		base := &appwire.NavigationReadBase{
			GenerationID: interleaved.Response.GenerationID,
			Revision:     interleaved.Response.Revision,
			ETag:         interleaved.Response.ETag,
		}
		followUp, readErr := service.readV2(t.Context(), key, base)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if followUp.Response.Status == "not_modified" || followUp.Response.Representation != appwire.NavigationRepresentationSnapshot || !bytes.Contains(followUp.Response.Data, []byte("reappeared during read")) {
			t.Fatalf("follow-up from gone authority = %+v, want present snapshot restoring graph", followUp.Response)
		}
	}
}

func TestNavigationServiceCancelledOwnerDoesNotCancelSharedBuild(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered = make(chan struct{})
	source.release = make(chan struct{})
	service := newTestNavigationService(t, source)
	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	owner := make(chan error, 1)
	go func() {
		_, err := service.Representation(ownerCtx, navigationResourceKey{Kind: navigationResourceManifest})
		owner <- err
	}()
	<-source.entered
	waiter := make(chan error, 1)
	go func() {
		_, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest})
		waiter <- err
	}()
	cancelOwner()
	if err := <-owner; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner error = %v, want cancellation", err)
	}
	close(source.release)
	if err := <-waiter; err != nil {
		t.Fatalf("waiter inherited owner cancellation: %v", err)
	}
}

func TestNavigationServiceCommitsCanceledRefreshForLaterPublication(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("committed-without-waiter")
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := service.Refresh(ctx, navigationChangeHint{Projects: []string{"p1"}}); result <- err }()
	<-source.entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner = %v", err)
	}
	close(source.release)
	service.mu.Lock()
	flight := service.flight
	service.mu.Unlock()
	<-flight.done
	if capability := service.Capability(); capability.Sequence != 1 {
		t.Fatalf("sequence = %d, want committed change", capability.Sequence)
	}
	select {
	case <-service.PublicationReady():
	default:
		t.Fatal("committed publication did not signal readiness")
	}
	pending := service.DrainPublications()
	if len(pending) != 1 || pending[0].Sequence != 1 || !hasNavigationTarget(pending[0].Targets, appwire.NavigationTargetProject, "p1") {
		t.Fatalf("pending publications = %+v", pending)
	}
	select {
	case <-service.wake:
	default:
		t.Fatal("build commit with no remaining waiter did not wake scheduler")
	}
}

func TestNavigationServicePublicationFIFOHasExactSequencesAndRefreshNeverConsumes(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}

	source.changeTitle("first")
	first, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
	if err != nil {
		t.Fatal(err)
	}
	source.changeTitle("second")
	second, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}, AllLoadedProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.GenerationID != second.GenerationID || len(first.Targets) == 0 || len(second.Targets) == 0 {
		t.Fatalf("refresh metadata mismatch: first=%+v second=%+v", first, second)
	}
	if capability := service.Capability(); capability == nil || capability.Sequence != 2 {
		t.Fatalf("capability after commits = %+v", capability)
	}

	publications := service.DrainPublications()
	if len(publications) != 2 {
		t.Fatalf("publications = %+v, want two retained Refresh commits", publications)
	}
	for index, payload := range publications {
		wantSequence := uint64(index + 1)
		if payload.Sequence != wantSequence || payload.GenerationID != first.GenerationID {
			t.Fatalf("publication %d = %+v, want sequence %d", index, payload, wantSequence)
		}
	}
	if !hasNavigationTarget(publications[0].Targets, appwire.NavigationTargetProject, "p1") || publications[0].Targets[len(publications[0].Targets)-1].Kind == appwire.NavigationTargetAllLoadedProjects {
		t.Fatalf("first publication targets = %+v", publications[0].Targets)
	}
	if publications[1].Targets[len(publications[1].Targets)-1].Kind != appwire.NavigationTargetAllLoadedProjects {
		t.Fatalf("second publication targets = %+v", publications[1].Targets)
	}
	if again := service.DrainPublications(); len(again) != 0 {
		t.Fatalf("second drain replayed publications: %+v", again)
	}
	select {
	case <-service.PublicationReady():
		t.Fatal("publication-ready remained signaled after drain")
	default:
	}
}

func TestNavigationServiceRefreshRegistersCausalTicketOnCommittedFlight(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	service := newTestNavigationService(t, source)
	done := make(chan hubapi.NavigationMutation, 1)
	go func() {
		mutation, _ := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		done <- mutation
	}()
	<-source.entered
	service.mu.Lock()
	ticketCount := len(service.flight.tickets)
	service.mu.Unlock()
	if ticketCount != 1 {
		t.Fatalf("refresh flight tickets = %d, want one", ticketCount)
	}
	close(source.release)
	<-done
	publications := service.DrainPublications()
	if len(publications) != 1 {
		t.Fatalf("publications=%d, want one", len(publications))
	}
}

func TestNavigationServiceJoinedTicketsShareExactCommittedOutcome(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("joined")
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	results := make(chan hubapi.NavigationMutation, 2)
	attached := make(chan struct{}, 1)
	previousAttached := navigationRefreshTicketAttached
	navigationRefreshTicketAttached = func(_ *NavigationService, flight *navigationBuildFlight) {
		if len(flight.tickets) == 2 {
			attached <- struct{}{}
		}
	}
	t.Cleanup(func() { navigationRefreshTicketAttached = previousAttached })
	go func() {
		m, _ := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		results <- m
	}()
	<-source.entered
	go func() {
		m, _ := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
		results <- m
	}()
	select {
	case <-attached:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not attach")
	}
	close(source.release)
	one, two := <-results, <-results
	if !reflect.DeepEqual(one, two) {
		t.Fatalf("joined outcomes differ: %+v vs %+v", one, two)
	}
	publications := service.DrainPublications()
	if len(publications) != 1 || !reflect.DeepEqual(one.Targets, publications[0].Targets) {
		t.Fatalf("outcome/publication mismatch: %+v %+v", one, publications)
	}
	if len(service.DrainPublications()) != 0 {
		t.Fatal("publication replayed")
	}
}

func TestNavigationServiceBuildErrorCompletesEveryAttachedTicket(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	source.mu.Lock()
	source.err = errors.New("capture failed")
	source.revision++
	source.mu.Unlock()
	attached := make(chan struct{}, 1)
	previousAttached := navigationRefreshTicketAttached
	navigationRefreshTicketAttached = func(_ *NavigationService, flight *navigationBuildFlight) {
		if len(flight.tickets) == 2 {
			attached <- struct{}{}
		}
	}
	t.Cleanup(func() { navigationRefreshTicketAttached = previousAttached })
	errs := make(chan error, 2)
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		errs <- err
	}()
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		errs <- err
	}()
	select {
	case <-attached:
	case <-time.After(time.Second):
		t.Fatal("second ticket did not attach")
	}
	close(source.release)
	for range 2 {
		if err := <-errs; err == nil || !strings.Contains(err.Error(), "capture failed") {
			t.Fatalf("ticket error=%v", err)
		}
	}
	if got := service.DrainPublications(); len(got) != 0 {
		t.Fatalf("error flight published %+v", got)
	}
}

func TestNavigationServicePendingEpochSurvivesCommitBeforeClear(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.captured = make(chan struct{}, 8)
	source.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	previous := navigationPublicationCommittedLocked
	previousCleared := navigationPendingCleared
	commit := make(chan struct{}, 1)
	secondCommit := make(chan struct{}, 1)
	releaseSecond := make(chan struct{})
	cleared := make(chan struct{}, 1)
	var commitCount int
	navigationPublicationCommittedLocked = func(locked *NavigationService) {
		// This hook runs at the commit cutoff while the service lock is held.
		// Model an identical producer event without attempting to reacquire it.
		commitCount++
		if commitCount == 1 {
			locked.epoch++
			locked.pendingHint = mergeNavigationChangeHints(locked.pendingHint, navigationChangeHint{Projects: []string{"p1"}})
			locked.pendingInvalidation = true
			locked.pendingEpoch++
			source.changeTitle("second")
		} else {
			secondCommit <- struct{}{}
			<-releaseSecond
		}
		commit <- struct{}{}
	}
	navigationPendingCleared = func() { cleared <- struct{}{} }
	t.Cleanup(func() {
		navigationPublicationCommittedLocked = previous
		navigationPendingCleared = previousCleared
	})
	source.changeTitle("first")
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	firstDone := make(chan struct{})
	go func() { service.refreshPending(ctx); close(firstDone) }()
	waitNavigationSignal(t, commit, "first commit")
	waitNavigationSignal(t, source.captured, "first forced refresh")
	waitNavigationSignal(t, firstDone, "first refresh completion")
	service.mu.Lock()
	if !service.pendingInvalidation {
		service.mu.Unlock()
		t.Fatal("pending invalidation cleared before second commit completed")
	}
	service.mu.Unlock()
	secondDone := make(chan struct{})
	go func() { service.refreshPending(ctx); close(secondDone) }()
	waitNavigationSignal(t, source.captured, "second forced refresh retained by raced epoch")
	waitNavigationSignal(t, secondCommit, "second commit")
	close(releaseSecond)
	waitNavigationSignal(t, cleared, "second refresh pending clear")
	waitNavigationSignal(t, secondDone, "second refresh completion")
	if got := source.captureCount(); got != 3 {
		t.Fatalf("captures = %d, want initial plus two forced refreshes", got)
	}
	service.mu.Lock()
	pending := service.pendingInvalidation
	service.mu.Unlock()
	if pending {
		t.Fatal("same-hint pending epoch was cleared by the first commit")
	}
}

func TestNavigationServiceCanceledJoinedCallerAtCommitCutoffDoesNotPoisonFlight(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("cutoff")
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	source.enterOnce = sync.Once{}
	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()
	ownerErr := make(chan error, 1)
	waiterResult := make(chan error, 1)
	attached := make(chan struct{}, 1)
	previousAttached := navigationRefreshTicketAttached
	navigationRefreshTicketAttached = func(_ *NavigationService, flight *navigationBuildFlight) {
		if len(flight.tickets) == 2 {
			attached <- struct{}{}
		}
	}
	t.Cleanup(func() { navigationRefreshTicketAttached = previousAttached })
	go func() {
		_, err := service.Refresh(ownerCtx, navigationChangeHint{Projects: []string{"p1"}})
		ownerErr <- err
	}()
	waitNavigationSignal(t, source.entered, "refresh capture")
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		waiterResult <- err
	}()
	waitNavigationSignal(t, attached, "joined refresh ticket")

	previousCutoff := navigationBeforeSnapshotCommit
	navigationBeforeSnapshotCommit = func(context.Context) { cancelOwner() }
	t.Cleanup(func() { navigationBeforeSnapshotCommit = previousCutoff })
	close(source.release)
	select {
	case err := <-ownerErr:
		if err == nil {
			t.Fatal("canceled owner unexpectedly succeeded")
		}
		var status interface{ StatusCode() int }
		if !errors.As(err, &status) || status.StatusCode() != 503 {
			t.Fatalf("owner error = %T %v, want 503", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled owner did not return")
	}
	select {
	case err := <-waiterResult:
		if err != nil {
			t.Fatalf("joined waiter inherited cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("joined waiter did not complete")
	}
	if got := service.Capability(); got == nil || got.Sequence != 1 {
		t.Fatalf("commit cutoff sequence = %+v, want one committed publication", got)
	}
	if got := service.DrainPublications(); len(got) != 1 {
		t.Fatalf("commit cutoff publications = %d, want one", len(got))
	}
}

func TestNavigationServiceFailedRefreshPreservesNewerPendingEpoch(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.captured = make(chan struct{}, 4)
	source.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	attached := make(chan struct{}, 1)
	var injectOnce sync.Once
	previousAttached := navigationRefreshTicketAttached
	navigationRefreshTicketAttached = func(locked *NavigationService, _ *navigationBuildFlight) {
		// Simulate a newer producer epoch arriving while the failed flight owns
		// the lock; the retry must not clear this newer hint.
		injectOnce.Do(func() {
			locked.epoch++
			locked.pendingEpoch++
			locked.pendingHint = mergeNavigationChangeHints(locked.pendingHint, navigationChangeHint{Sources: true})
			locked.pendingInvalidation = true
			attached <- struct{}{}
		})
	}
	t.Cleanup(func() { navigationRefreshTicketAttached = previousAttached })
	source.mu.Lock()
	source.err = errors.New("capture failed")
	source.revision++
	source.mu.Unlock()
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	go func() { service.refreshPending(ctx); close(done) }()
	waitNavigationSignal(t, attached, "failed refresh ticket")
	waitNavigationSignal(t, done, "failed refresh completion")
	service.mu.Lock()
	pending := service.pendingInvalidation
	hint := service.pendingHint
	service.mu.Unlock()
	if !pending || !hint.Sources || len(hint.Projects) != 1 || hint.Projects[0] != "p1" {
		t.Fatalf("failed refresh lost newer pending hint: pending=%v hint=%+v", pending, hint)
	}
	if got := service.DrainPublications(); len(got) != 0 {
		t.Fatalf("failed refresh published %+v", got)
	}
	source.mu.Lock()
	source.err = nil
	source.mu.Unlock()
	source.changeTitle("retry")
	retryDone := make(chan struct{})
	go func() { service.refreshPending(ctx); close(retryDone) }()
	waitNavigationSignal(t, retryDone, "retry refresh completion")
	service.mu.Lock()
	pending = service.pendingInvalidation
	service.mu.Unlock()
	if pending {
		t.Fatal("successful retry left the pending epoch set")
	}
	publications := service.DrainPublications()
	if len(publications) != 1 || !hasNavigationTarget(publications[0].Targets, appwire.NavigationTargetProject, "p1") {
		t.Fatalf("retry publications = %+v, want one project publication", publications)
	}
	cancel()
}

func TestNavigationServiceConcurrentAppendAndDrainKeepFIFOAndWakeAtomic(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}

	previousCommitHook := navigationPublicationCommittedLocked
	previousDrainHook := navigationBeforePublicationDrainLock
	commitState := make(chan [2]int, 1)
	releaseCommit := make(chan struct{})
	drainAttempted := make(chan struct{})
	var drainOnce sync.Once
	navigationPublicationCommittedLocked = func(locked *NavigationService) {
		commitState <- [2]int{len(locked.publications), len(locked.publicationReady)}
		<-releaseCommit
	}
	navigationBeforePublicationDrainLock = func() {
		drainOnce.Do(func() { close(drainAttempted) })
	}
	t.Cleanup(func() {
		navigationPublicationCommittedLocked = previousCommitHook
		navigationBeforePublicationDrainLock = previousDrainHook
	})

	source.changeTitle("atomic-publication")
	refreshDone := make(chan error, 1)
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		refreshDone <- err
	}()
	if state := <-commitState; state != [2]int{1, 1} {
		t.Fatalf("locked publication state = %v, want one FIFO payload and one level token", state)
	}
	drained := make(chan []appwire.NavigationInvalidatedPayload, 1)
	go func() { drained <- service.DrainPublications() }()
	<-drainAttempted
	select {
	case got := <-drained:
		t.Fatalf("drain crossed the append/token critical section: %+v", got)
	default:
	}
	close(releaseCommit)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	publications := <-drained
	if len(publications) != 1 || publications[0].Sequence != 1 {
		t.Fatalf("concurrent drain = %+v, want committed sequence 1", publications)
	}
	select {
	case <-service.PublicationReady():
		t.Fatal("empty FIFO retained a stale publication wake")
	default:
	}
}

func TestNavigationServiceConcurrentRefreshCoalescesCoreBuild(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("concurrent")
	source.mu.Lock()
	source.entered = make(chan struct{})
	source.release = make(chan struct{})
	source.enterOnce = sync.Once{}
	source.mu.Unlock()

	start := make(chan struct{})
	var failures atomic.Int32
	joined := make(chan struct{})
	var joinOnce sync.Once
	var attached atomic.Int32
	previousAttached := navigationRefreshTicketAttached
	navigationRefreshTicketAttached = func(_ *NavigationService, _ *navigationBuildFlight) {
		if attached.Add(1) == 20 {
			joinOnce.Do(func() { close(joined) })
		}
	}
	t.Cleanup(func() { navigationRefreshTicketAttached = previousAttached })
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			<-start
			if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
				failures.Add(1)
			}
		})
	}
	close(start)
	<-source.entered
	// The capture is held until every concurrent caller has registered its ticket.
	<-joined
	close(source.release)
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("refresh failures = %d", failures.Load())
	}
	if got := source.captureCount(); got != 2 { // initial + exactly one overlapping refresh build.
		t.Fatalf("captures = %d, want exactly two", got)
	}
	if capability := service.Capability(); capability == nil || capability.Sequence != 1 {
		t.Fatalf("sequence after one shared refresh = %+v, want one commit", capability)
	}
}

func TestNavigationServiceMergesJoinedWildcardHintBeforeCommit(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("mixed")
	source.entered, source.release = make(chan struct{}), make(chan struct{})
	precise := make(chan hubapi.NavigationMutation, 1)
	joined := make(chan struct{})
	var joinOnce sync.Once
	var attached atomic.Int32
	previousAttached := navigationRefreshTicketAttached
	navigationRefreshTicketAttached = func(_ *NavigationService, _ *navigationBuildFlight) {
		if attached.Add(1) == 2 {
			joinOnce.Do(func() { close(joined) })
		}
	}
	t.Cleanup(func() { navigationRefreshTicketAttached = previousAttached })
	go func() {
		m, _ := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		precise <- m
	}()
	<-source.entered
	wildcard := make(chan hubapi.NavigationMutation, 1)
	go func() {
		m, _ := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true})
		wildcard <- m
	}()
	<-joined
	close(source.release)
	for _, mutation := range []hubapi.NavigationMutation{<-precise, <-wildcard} {
		if !hasNavigationTarget(mutation.Targets, appwire.NavigationTargetProject, "p1") || mutation.Targets[len(mutation.Targets)-1].Kind != appwire.NavigationTargetAllLoadedProjects {
			t.Fatalf("merged targets = %+v", mutation.Targets)
		}
	}
}

func TestNavigationServicePreservesLastGoodAndMapsChurnCancellationTo503(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	good, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest})
	if err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.err = errors.New("store read failed")
	source.revision++
	source.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Sources: true}); err == nil {
		t.Fatal("refresh succeeded after source failure")
	}
	if got := service.CurrentRevision((navigationResourceKey{Kind: navigationResourceManifest}).Semantic()); got != good.Revision {
		t.Fatalf("last-good revision = %d, want %d", got, good.Revision)
	}

	source.mu.Lock()
	source.err = nil
	source.entered = make(chan struct{})
	source.release = make(chan struct{})
	source.enterOnce = sync.Once{}
	source.revision++
	source.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = service.Refresh(ctx, navigationChangeHint{Projects: []string{"p1"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 503 {
		t.Fatalf("error = %T %v, want 503 status", err, err)
	}
}

func TestNavigationServiceBuildDeadlineInterruptsProjectionWithoutCommitOrPublication(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	previous := buildNavigationServiceProjectionContext
	entered := make(chan struct{})
	buildNavigationServiceProjectionContext = func(ctx context.Context, inputs navigationBuildInputs) (navigationProjection, error) {
		close(entered)
		<-ctx.Done()
		return navigationProjection{}, ctx.Err()
	}
	t.Cleanup(func() { buildNavigationServiceProjectionContext = previous })
	service := newTestNavigationService(t, source)
	buildCtx := newTriggeredDeadlineContext(t.Context())
	service.lifecycleCtx = buildCtx
	result := make(chan error, 1)
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		result <- err
	}()
	<-entered
	buildCtx.expire()
	err := <-result
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 503 {
		t.Fatalf("error = %T %v, want typed 503", err, err)
	}
	if capability := service.Capability(); capability.Sequence != 0 {
		t.Fatalf("sequence advanced after canceled projection: %+v", capability)
	}
	if service.Stats().CoreBuilds != 0 || service.Stats().Cache.Entries != 0 || len(service.DrainPublications()) != 0 {
		t.Fatalf("canceled projection published state: stats=%+v", service.Stats())
	}
}

func TestNavigationServiceDeadlineAtCommitRevisesAndPublishesNothing(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	projectKey := (navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}).Semantic()
	beforeRevision := service.CurrentRevision(projectKey)
	beforeStats := service.Stats()
	beforeCapability := *service.Capability()
	service.mu.Lock()
	beforeCore := service.core
	beforeResources := make(map[navigationResourceKey]navigationResourceState, len(service.resources))
	for key, state := range service.resources {
		state.Dependencies = cloneNavigationDependencies(state.Dependencies)
		beforeResources[key] = state
	}
	service.mu.Unlock()

	previous := navigationBeforeSnapshotCommit
	entered := make(chan struct{})
	var once sync.Once
	navigationBeforeSnapshotCommit = func(ctx context.Context) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
	}
	t.Cleanup(func() { navigationBeforeSnapshotCommit = previous })
	source.changeTitle("deadline-at-commit")
	buildCtx := newTriggeredDeadlineContext(t.Context())
	service.lifecycleCtx = buildCtx
	result := make(chan error, 1)
	go func() {
		_, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}})
		result <- err
	}()
	<-entered
	buildCtx.expire()
	err := <-result
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want commit deadline", err)
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 503 {
		t.Fatalf("error = %T %v, want typed 503", err, err)
	}
	service.mu.Lock()
	afterCore := service.core
	afterResources := service.resources
	service.mu.Unlock()
	if afterCore != beforeCore || !reflect.DeepEqual(afterResources, beforeResources) {
		t.Fatal("expired commit changed retained core or resource states")
	}
	if got := service.CurrentRevision(projectKey); got != beforeRevision {
		t.Fatalf("expired commit revision = %d, want %d", got, beforeRevision)
	}
	if got := service.Stats(); !reflect.DeepEqual(got, beforeStats) {
		t.Fatalf("expired commit changed build/cache stats: before=%+v after=%+v", beforeStats, got)
	}
	if got := service.Capability(); got == nil || !reflect.DeepEqual(*got, beforeCapability) {
		t.Fatalf("expired commit changed capability: before=%+v after=%+v", beforeCapability, got)
	}
	if publications := service.DrainPublications(); len(publications) != 0 {
		t.Fatalf("expired commit published: %+v", publications)
	}
}

func TestNavigationLogicalStructuralFingerprintIsStableAndValueSensitive(t *testing.T) {
	now := time.Unix(1_700_000_000, 123_000_000).UTC()
	value := hubapi.NavigationSessionSummary{
		Ref:       "local:root",
		SessionID: navigationTestSessionID,
		Title:     "root",
		Live:      true,
		UpdatedAt: &now,
		Children: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{
			{Ref: "local:child", SessionID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", Title: "child"},
		},
	}
	first, err := navigationLogicalFingerprintContext(t.Context(), value)
	if err != nil {
		t.Fatal(err)
	}
	equal := value
	equal.Children = append(hubapi.NavigationArray[hubapi.NavigationSessionSummary](nil), value.Children...)
	second, err := navigationLogicalFingerprintContext(t.Context(), equal)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equal logical values produced unequal fingerprints: %x != %x", first, second)
	}
	equal.Children[0].Title = "changed"
	changed, err := navigationLogicalFingerprintContext(t.Context(), equal)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatalf("changed nested scalar retained fingerprint %x", changed)
	}
}

func TestNavigationLogicalFingerprintCancellationInterruptsLargeStringChunks(t *testing.T) {
	previous := navigationFingerprintStringChunkContext
	ctx, cancel := context.WithCancel(t.Context())
	var chunks atomic.Int32
	navigationFingerprintStringChunkContext = func(ctx context.Context, _ string) error {
		if chunks.Add(1) == 2 {
			cancel()
		}
		return ctx.Err()
	}
	t.Cleanup(func() { navigationFingerprintStringChunkContext = previous })
	_, err := navigationLogicalFingerprintContext(ctx, strings.Repeat("large-value-", navigationFingerprintStringChunkSize))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("large string fingerprint error = %v, want cancellation", err)
	}
	if got := chunks.Load(); got != 2 {
		t.Fatalf("large string fingerprint continued after cancellation: %d chunks", got)
	}
}

func TestNavigationServiceDeadlineInterruptsMidFingerprintWithoutPublication(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	const rowCount = 500
	rows := make([]hubcore.TreeNode, rowCount)
	for index := range rows {
		rows[index] = hubcore.TreeNode{
			ID:        fmt.Sprintf("%026d", index+1),
			Title:     "before",
			Project:   "p1",
			Kind:      "session",
			State:     "idle",
			UpdatedAt: now,
		}
	}
	source.inputs.Tree.Projects[0].Current = rows
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	projectKey := (navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}).Semantic()
	beforeRevision := service.CurrentRevision(projectKey)
	beforeStats := service.Stats()
	beforeCapability := *service.Capability()

	source.mu.Lock()
	for index := range source.inputs.Tree.Projects[0].Current {
		source.inputs.Tree.Projects[0].Current[index].Title = strings.Repeat("x", maxNavigationTitleRunes)
	}
	source.revision++
	source.mu.Unlock()
	previous := navigationFingerprintStringChunkContext
	entered := make(chan struct{})
	var matchingChunks atomic.Int32
	navigationFingerprintStringChunkContext = func(ctx context.Context, chunk string) error {
		if len(chunk) == maxNavigationTitleRunes && chunk[0] == 'x' && matchingChunks.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
		}
		return ctx.Err()
	}
	t.Cleanup(func() { navigationFingerprintStringChunkContext = previous })

	buildCtx := newTriggeredDeadlineContext(t.Context())
	flight := &navigationBuildFlight{
		done:    make(chan struct{}),
		hint:    navigationChangeHint{Projects: []string{"p1"}},
		mutated: true,
	}
	service.mu.Lock()
	service.flight = flight
	service.mu.Unlock()
	go service.buildSnapshot(buildCtx, source.Revision(), flight)
	<-entered
	buildCtx.expire()
	<-flight.done
	err := flight.err
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want hashing deadline", err)
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 503 {
		t.Fatalf("error = %T %v, want typed 503", err, err)
	}
	if got := matchingChunks.Load(); got != 1 {
		t.Fatalf("fingerprinting continued after cancellation: %d chunks", got)
	}
	if got := service.CurrentRevision(projectKey); got != beforeRevision {
		t.Fatalf("canceled fingerprint revision = %d, want %d", got, beforeRevision)
	}
	if got := service.Stats(); !reflect.DeepEqual(got, beforeStats) {
		t.Fatalf("canceled fingerprint changed build/cache stats: before=%+v after=%+v", beforeStats, got)
	}
	if got := service.Capability(); got == nil || !reflect.DeepEqual(*got, beforeCapability) {
		t.Fatalf("canceled fingerprint changed capability: before=%+v after=%+v", beforeCapability, got)
	}
	if publications := service.DrainPublications(); len(publications) != 0 {
		t.Fatalf("canceled fingerprint published: %+v", publications)
	}
}

type triggeredDeadlineContext struct {
	context.Context
	done chan struct{}
}

func newTriggeredDeadlineContext(parent context.Context) *triggeredDeadlineContext {
	return &triggeredDeadlineContext{Context: parent, done: make(chan struct{})}
}

func (ctx *triggeredDeadlineContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *triggeredDeadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (ctx *triggeredDeadlineContext) expire() {
	close(ctx.done)
}

type cancelAfterChecksContext struct {
	context.Context
	checks atomic.Int32
	limit  int32
}

func (c *cancelAfterChecksContext) Err() error {
	if c.checks.Add(1) >= c.limit {
		return context.Canceled
	}
	return nil
}

func TestNavigationNextStatesChecksContextWhileCreatingTombstones(t *testing.T) {
	previous := make(map[navigationResourceKey]navigationResourceState, 100)
	for index := range 100 {
		key := navigationResourceKey{Kind: navigationResourceLocation, ID: fmt.Sprintf("local:%026d", index)}
		previous[key] = navigationResourceState{Revision: 1, Present: true}
	}
	// One entry check plus all 100 previous-key union checks means this expires
	// inside the final per-key transition loop that creates tombstones.
	ctx := &cancelAfterChecksContext{Context: context.Background(), limit: 150}
	changes, next, err := navigationNextStatesContext(ctx, previous, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("transition error = %v, want cancellation", err)
	}
	if changes != nil || next != nil {
		t.Fatalf("canceled transition returned partial state: changes=%d next=%d", len(changes), len(next))
	}
	if got := ctx.checks.Load(); got > ctx.limit+1 {
		t.Fatalf("state transition continued after cancellation: %d checks", got)
	}
}

func TestNavigationProjectionAndLogicalFingerprintTraversalCheckContextInternally(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	inputs := newTestNavigationSource(now).inputs
	rows := make([]hubcore.TreeNode, 200)
	for index := range rows {
		rows[index] = hubcore.TreeNode{ID: fmt.Sprintf("%026d", index+1), Kind: "session", Title: "row", UpdatedAt: now}
	}
	inputs.Tree.Projects[0].Current = rows
	inputs.GenerationID = "0123456789abcdef0123456789abcdef"
	projectionCtx := &cancelAfterChecksContext{Context: context.Background(), limit: 25}
	if _, err := buildNavigationProjectionContext(projectionCtx, inputs); !errors.Is(err, context.Canceled) {
		t.Fatalf("projection error = %v, want internal cancellation", err)
	}
	if got := projectionCtx.checks.Load(); got > projectionCtx.limit+2 {
		t.Fatalf("projection continued after cancellation: %d checks", got)
	}

	projection, err := buildNavigationProjection(inputs)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintCtx := &cancelAfterChecksContext{Context: context.Background(), limit: 25}
	if _, _, err := navigationLogicalFingerprintsContext(fingerprintCtx, projection); !errors.Is(err, context.Canceled) {
		t.Fatalf("fingerprint error = %v, want internal cancellation", err)
	}
	if got := fingerprintCtx.checks.Load(); got > fingerprintCtx.limit+2 {
		t.Fatalf("fingerprint traversal continued after cancellation: %d checks", got)
	}
}

func TestNavigationServiceGenerationAndSafeIntegerOverflow(t *testing.T) {
	generated, err := newNavigationGenerationID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(generated) {
		t.Fatalf("generation %q is not 128-bit lowercase hex", generated)
	}

	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.sequence = maxNavigationSafeInteger
	service.mu.Unlock()
	if _, err := service.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true}); err == nil {
		t.Fatal("sequence overflow succeeded")
	}
	if service.Capability().Sequence != maxNavigationSafeInteger {
		t.Fatalf("unsafe sequence emitted: %d", service.Capability().Sequence)
	}

	service.mu.Lock()
	key := (navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1"}).Semantic()
	state := service.resources[key]
	state.Revision = maxNavigationSafeInteger
	service.resources[key] = state
	service.mu.Unlock()
	source.changeTitle("overflow")
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err == nil {
		t.Fatal("resource revision overflow succeeded")
	}
	if got := service.CurrentRevision(key); got != maxNavigationSafeInteger {
		t.Fatalf("unsafe revision emitted: %d", got)
	}
}

func TestNavigationServiceGenerationFailureOmitsCapabilityAndFailsClosed(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Generation = func() (string, error) { return "", errors.New("entropy unavailable") }
	})
	if capability := service.Capability(); capability != nil {
		t.Fatalf("capability = %+v, want omitted", capability)
	}
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err == nil {
		t.Fatal("representation succeeded after generation failure")
	}
	if _, err := service.Refresh(t.Context(), navigationChangeHint{}); err == nil {
		t.Fatal("refresh succeeded after generation failure")
	}
}

func TestNavigationServiceVersionedKeyAtomicallyResolvesResourceVersion(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	request := navigationResourceKey{Kind: navigationResourceProject, ProjectKey: "p1", Offset: 9, Limit: 1}
	first, err := service.VersionedKey(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation != service.Capability().GenerationID || first.Revision == 0 {
		t.Fatalf("versioned key = %+v, capability = %+v", first, service.Capability())
	}
	if first.Offset != 0 || first.Limit != 0 {
		t.Fatalf("project root key retained irrelevant page fields: %+v", first)
	}

	source.changeTitle("versioned")
	if _, err := service.Refresh(t.Context(), navigationChangeHint{Projects: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	second, err := service.VersionedKey(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != first.Generation || second.Revision <= first.Revision {
		t.Fatalf("versioned key did not atomically advance: first=%+v second=%+v", first, second)
	}
}

func TestNavigationServiceStartUsesExactBoundaryAndResets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	first := now.Add(14 * 24 * time.Hour)
	second := now.Add(24 * time.Hour)
	source.nextBoundary = first
	timers := make(chan *fakeNavigationTimer, 4)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Now = func() time.Time { return now }
		cfg.NewTimer = func(delay time.Duration) navigationTimer {
			timer := &fakeNavigationTimer{delay: delay, ch: make(chan time.Time, 1)}
			timers <- timer
			return timer
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { service.Start(ctx); close(done) }()
	firstTimer := <-timers
	if got, want := firstTimer.delay, first.Sub(now); got != want {
		t.Fatalf("first boundary delay = %v, want %v", got, want)
	}
	source.mu.Lock()
	source.nextBoundary = second
	source.revision++
	source.mu.Unlock()
	service.Invalidate(navigationChangeHint{Time: true})
	secondTimer := <-timers
	if !firstTimer.stopped.Load() {
		t.Fatal("superseded boundary timer was not stopped")
	}
	if got, want := secondTimer.delay, second.Sub(now); got != want {
		t.Fatalf("reset boundary delay = %v, want %v", got, want)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop with lifecycle context")
	}
	if !secondTimer.stopped.Load() {
		t.Fatal("active boundary timer was not stopped")
	}
}

func TestNavigationServiceStartWaitsForOwnedBuildToStop(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	previous := buildNavigationServiceProjectionContext
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	buildNavigationServiceProjectionContext = func(ctx context.Context, inputs navigationBuildInputs) (navigationProjection, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return navigationProjection{}, ctx.Err()
	}
	t.Cleanup(func() { buildNavigationServiceProjectionContext = previous })
	service := newTestNavigationService(t, source)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { service.Start(ctx); close(done) }()
	<-entered
	cancel()
	<-canceled
	select {
	case <-done:
		t.Fatal("Start returned while its projection build was still running")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Start did not return after owned build stopped")
	}
}

type fakeNavigationTimer struct {
	delay   time.Duration
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *fakeNavigationTimer) C() <-chan time.Time { return t.ch }
func (t *fakeNavigationTimer) Stop() bool          { return !t.stopped.Swap(true) }

func TestNavigationServiceSchedulerRetriesEmptyAndFailedCaptures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	source.err = errors.New("temporary source failure")
	source.captured = make(chan struct{}, 4)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.RetryAfter = time.Millisecond
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	retried := make(chan struct{})
	var retryOnce sync.Once
	previousCommit := navigationBeforeSnapshotCommit
	navigationBeforeSnapshotCommit = func(context.Context) {
		retryOnce.Do(func() { close(retried) })
	}
	t.Cleanup(func() { navigationBeforeSnapshotCommit = previousCommit })
	go func() { service.Start(ctx); close(done) }()
	select {
	case <-source.captured:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not attempt failed capture")
	}
	source.mu.Lock()
	source.err = nil
	source.revision++
	source.mu.Unlock()
	<-retried
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after retry")
	}
}

// perCaptureRetitleSource serves a different title on every capture without
// advancing the revision. A forced rebuild that follows a successful
// non-forced rebuild therefore still has a fingerprint diff to publish, which
// is exactly the retry shape a transiently failed forced refresh produces.
type perCaptureRetitleSource struct {
	inner *testNavigationSource
	calls atomic.Int64
}

func (s *perCaptureRetitleSource) Revision() navigationSourceRevision {
	return s.inner.Revision()
}

func (s *perCaptureRetitleSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	s.inner.retitle(fmt.Sprintf("capture-%d", s.calls.Add(1)))
	return s.inner.Capture(ctx, generation, now)
}

// captureCountingSource counts Capture calls for pacing assertions.
type captureCountingSource struct {
	inner    navigationSource
	captures *atomic.Int64
}

func (c *captureCountingSource) Revision() navigationSourceRevision { return c.inner.Revision() }
func (c *captureCountingSource) Capture(ctx context.Context, generation string, now time.Time) (navigationSourceSnapshot, error) {
	c.captures.Add(1)
	return c.inner.Capture(ctx, generation, now)
}

func TestNavigationServiceStartRetriesFailedForcedRefreshWithoutWaitingForBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	source := newTestNavigationSource(now)
	// Park the scheduler a full day out: without a failure wake, a failed
	// forced refresh leaves the pending invalidation unpublished until that
	// boundary elapses.
	source.nextBoundary = now.Add(24 * time.Hour)
	retitling := &perCaptureRetitleSource{inner: source}
	var attempts atomic.Int64
	counted := &captureCountingSource{inner: retitling, captures: &attempts}
	created := make(chan *fakeNavigationTimer, 16)
	service := newTestNavigationService(t, source, func(cfg *navigationServiceConfig) {
		cfg.Source = counted
		cfg.RetryAfter = 10 * time.Millisecond
		// A clock anchored at the frozen base but advancing in real time, so
		// the failed refresh's retry deadline actually elapses.
		start := time.Now()
		base := time.Unix(1_700_000_000, 0).UTC()
		cfg.Now = func() time.Time { return base.Add(time.Since(start)) }
		cfg.NewTimer = func(delay time.Duration) navigationTimer {
			timer := &fakeNavigationTimer{delay: delay, ch: make(chan time.Time, 1)}
			created <- timer
			// Timers fire in real time; the paced retry depends on the
			// retryAfter timer elapsing rather than on a wake token.
			go func() {
				time.Sleep(delay)
				select {
				case timer.ch <- time.Now():
				default:
				}
			}()
			return timer
		}
	})
	if _, err := service.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	source.captured = make(chan struct{}, 8)
	source.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	cleared := make(chan struct{}, 1)
	previousCleared := navigationPendingCleared
	navigationPendingCleared = func() { cleared <- struct{}{} }
	t.Cleanup(func() { navigationPendingCleared = previousCleared })
	go func() { service.Start(ctx); close(done) }()
	firstTimer := <-created
	// The boundary is computed from the advancing test clock, so the parked
	// delay is 24h minus the (sub-millisecond) time since the service was
	// built; accept any delay within one second of the parked day.
	if d := 24*time.Hour - firstTimer.delay; d < 0 || d > time.Second {
		t.Fatalf("boundary delay = %v, want ~24h (within 1s)", firstTimer.delay)
	}
	// The forced rebuild fails once, transiently.
	source.mu.Lock()
	source.err = errors.New("transient capture failure")
	source.mu.Unlock()
	source.changeTitle("renamed")
	service.Invalidate(navigationChangeHint{Projects: []string{"p1"}})
	waitNavigationSignal(t, source.captured, "failed forced capture")
	// While the failure is still pending, the armed hint must not have
	// doubled (the self-merge bug this test guards against).
	service.mu.Lock()
	if got := len(service.pendingHint.Projects); got != 1 {
		service.mu.Unlock()
		t.Fatalf("pending hint Projects length = %d while failure pending, want 1", got)
	}
	service.mu.Unlock()
	source.mu.Lock()
	source.err = nil
	source.mu.Unlock()
	// The commit signals PublicationReady before refreshPending re-acquires
	// the lock to clear the pending epoch, so the clear notice is the
	// deterministic completion signal for the retry.
	waitNavigationSignal(t, cleared, "pending clear after failed forced refresh")
	service.mu.Lock()
	pending := service.pendingInvalidation
	service.mu.Unlock()
	if pending {
		t.Fatal("successful retry left the pending invalidation set")
	}
	publications := service.DrainPublications()
	if len(publications) != 1 || !hasNavigationTarget(publications[0].Targets, appwire.NavigationTargetProject, "p1") {
		t.Fatalf("retry publications = %+v, want one project publication", publications)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after retry")
	}
	// The retry was paced by the retryAfter window: the failed-phase
	// attempt count (captured by the counting wrapper below) stays small.
	// A spin — the pre-fix hot loop — burned ~50 attempts per 500ms.
	if n := attempts.Load(); n > 8 {
		t.Fatalf("capture attempts = %d during the failed phase, want a paced handful", n)
	}
}

func TestNavigationSnapshotBoundaryUsesNearest24HourOr14DayCutover(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	project := hubcore.TreeProject{Key: "p1", Name: "p1", Current: []hubcore.TreeNode{{ID: navigationTestSessionID, Kind: "session", UpdatedAt: now.Add(-23 * time.Hour)}}}
	tree := hubcore.Tree{Projects: []hubcore.TreeProject{project}}
	if got, want := navigationSnapshotBoundary(tree, now), now.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("24h boundary = %v, want %v", got, want)
	}
	project.Current = nil
	project.Recent = []hubcore.TreeNode{{ID: navigationTestSessionID, Kind: "session", UpdatedAt: now.Add(-(14*24 - 1) * time.Hour)}}
	tree.Projects = []hubcore.TreeProject{project}
	if got, want := navigationSnapshotBoundary(tree, now), now.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("14d boundary = %v, want %v", got, want)
	}
}
