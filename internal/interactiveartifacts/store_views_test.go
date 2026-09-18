package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestStoreViewBodiesAndSourceRange(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	view, err := s.GetView(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q}`, created.ArtifactID))
	requireNoError(t, err)
	if view.Source == nil || string(view.State) != "{}" {
		t.Fatal("view missing initial content")
	}
	_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "save", 1, 1, `{"n":1}`))
	requireNoError(t, err)
	view, err = s.GetView(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"knownSourceRevision":1,"knownStateVersion":1}`, created.ArtifactID))
	requireNoError(t, err)
	if view.Source != nil || string(view.State) != `{"n":1}` || view.StateVersion != 2 {
		t.Fatalf("wrong conditional body %+v", view)
	}
	view, err = s.GetView(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"knownSourceRevision":1,"knownStateVersion":2}`, created.ArtifactID))
	requireNoError(t, err)
	if view.Source != nil || view.State != nil {
		t.Fatal("unchanged content was returned")
	}
	result, err := s.Read(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"include":["source"],"sourceStartLine":2,"sourceEndLine":2}`, created.ArtifactID))
	requireNoError(t, err)
	if result.Source.HTML != "<script>throw 1</script>" || result.Source.StartLine != 2 || result.Source.EndLine != 2 {
		t.Fatalf("wrong line range %+v", result.Source)
	}
	opened, err := s.Open(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q}`, created.ArtifactID))
	requireNoError(t, err)
	if opened.Launch.ArtifactID != created.ArtifactID || opened.StateVersion != 2 {
		t.Fatalf("wrong launch %+v", opened)
	}
}

func TestStoreListIsBoundedAndNamespaceScoped(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	ids := make(map[string]bool)
	for i := range 23 {
		created, err := s.Publish(ctx, hash, createJSON(strconv.Itoa(i)), PublicationOrigin{})
		requireNoError(t, err)
		ids[created.ArtifactID] = true
	}
	page, err := s.List(ctx, hash, []byte(`{}`))
	requireNoError(t, err)
	if len(page.Artifacts) != 20 || page.NextCursor == "" {
		t.Fatalf("unbounded first page %+v", page)
	}
	for _, a := range page.Artifacts {
		if !ids[a.ArtifactID] {
			t.Fatal("unknown or duplicate artifact")
		}
		delete(ids, a.ArtifactID)
	}
	tail, err := s.List(ctx, hash, fmt.Appendf(nil, `{"cursor":%q}`, page.NextCursor))
	requireNoError(t, err)
	if len(tail.Artifacts) != 3 || tail.NextCursor != "" {
		t.Fatalf("bad final page %+v", tail)
	}
	for _, a := range tail.Artifacts {
		if !ids[a.ArtifactID] {
			t.Fatal("unknown or duplicate tail")
		}
		delete(ids, a.ArtifactID)
	}
	if len(ids) != 0 {
		t.Fatal("list omitted artifacts")
	}
	_, err = s.List(ctx, hash, []byte(`{"limit":101}`))
	if err == nil {
		t.Fatal("accepted oversized page")
	}
	requireNoError(t, s.EnsureNamespace(ctx, "other", "realm", "other owner"))
	scope := testScope()
	scope.NamespaceID = "other"
	other := sha256.Sum256([]byte("other namespace"))
	requireNoError(t, s.InstallGrant(ctx, other, scope))
	_, err = s.List(ctx, other, fmt.Appendf(nil, `{"cursor":%q}`, page.NextCursor))
	if err == nil {
		t.Fatal("cursor crossed namespace")
	}
	scope = testScope()
	scope.ArtifactID = tail.Artifacts[0].ArtifactID
	restricted := sha256.Sum256([]byte("single artifact"))
	requireNoError(t, s.InstallGrant(ctx, restricted, scope))
	single, err := s.List(ctx, restricted, []byte(`{}`))
	requireNoError(t, err)
	if len(single.Artifacts) != 1 || single.Artifacts[0].ArtifactID != scope.ArtifactID {
		t.Fatal("list expanded artifact scope")
	}
}

func TestStoreDiagnosticsBoundHistoryAndRateWithoutMutatingContent(t *testing.T) {
	now := fixedClock()
	s, hash, _ := setupStore(t, StoreOptions{Clock: func() time.Time { return now }})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	before := readState(t, s, hash, created.ArtifactID)
	for i := range 21 {
		now = now.Add(time.Minute)
		_, err = s.ReportDiagnostic(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"sourceRevision":1,"message":%q}`, created.ArtifactID, strconv.Itoa(i)))
		requireNoError(t, err)
	}
	got, err := s.Read(ctx, hash, readJSON(created.ArtifactID, "diagnostics"))
	requireNoError(t, err)
	if len(got.Diagnostics) != 20 || got.Diagnostics[0].Message != "20" || got.Diagnostics[19].Message != "1" {
		t.Fatalf("unbounded or unordered diagnostics %+v", got.Diagnostics)
	}
	if after := readState(t, s, hash, created.ArtifactID); !reflect.DeepEqual(before, after) {
		t.Fatal("diagnostic modified source/state")
	}
	var busy bool
	for range 25 {
		_, err = s.ReportDiagnostic(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"sourceRevision":1,"message":"repeat"}`, created.ArtifactID))
		if err != nil {
			domain := requireCode(t, err, Busy)
			if !domain.Retryable {
				t.Fatal("diagnostic rate error not retryable")
			}
			busy = true
			break
		}
	}
	if !busy {
		t.Fatal("diagnostics are not rate bounded")
	}
	_, err = s.ReportDiagnostic(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"sourceRevision":2,"message":"invented revision"}`, created.ArtifactID))
	requireCode(t, err, NotFoundOrForbidden)
}
