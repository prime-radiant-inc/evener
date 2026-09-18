package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func setupStore(t *testing.T, options StoreOptions) (*Store, [32]byte, string) {
	t.Helper()
	if options.Clock == nil {
		options.Clock = fixedClock
	}
	path := filepath.Join(t.TempDir(), "private", "artifacts.sqlite")
	s := openTestStore(t, path, options)
	ctx := context.Background()
	requireNoError(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"))
	hash := sha256.Sum256([]byte("grant one"))
	requireNoError(t, s.InstallGrant(ctx, hash, testScope()))
	return s, hash, path
}
func createJSON(id string) []byte {
	return []byte(`{"mutationId":"` + id + `","title":"Table","summary":"Options","html":"<p>é</p>\r\n<script>throw 1</script>"}`)
}
func saveJSON(artifact, id string, source, state int, body string) []byte {
	return fmt.Appendf(nil, `{"artifactId":%q,"mutationId":%q,"expectedSourceRevision":%d,"expectedStateVersion":%d,"state":%s}`, artifact, id, source, state, body)
}
func publishJSON(artifact, id string, source, state int) []byte {
	return fmt.Appendf(nil, `{"artifactId":%q,"mutationId":%q,"expectedSourceRevision":%d,"expectedStateVersion":%d,"title":"Revised","summary":"New layout","html":"new source","format":"html","formatVersion":1}`, artifact, id, source, state)
}
func readJSON(id string, includes ...string) []byte {
	raw, _ := json.Marshal(map[string]any{"artifactId": id, "include": includes})
	if includes == nil {
		return fmt.Appendf(nil, `{"artifactId":%q}`, id)
	}
	return raw
}
func readState(t *testing.T, s *Store, hash [32]byte, id string) ReadResult {
	t.Helper()
	r, err := s.Read(context.Background(), hash, readJSON(id, "source", "state"))
	requireNoError(t, err)
	return r
}

func TestStoreMutationReceiptsSurviveRenewalAndLaterHeads(t *testing.T) {
	s, hash, path := setupStore(t, StoreOptions{})
	ctx := context.Background()
	create := createJSON("create")
	first, err := s.Publish(ctx, hash, create)
	requireNoError(t, err)
	if first.ArtifactID == "" || first.SourceRevision != 1 || first.StateVersion != 1 || first.Status != StatusCommitted {
		t.Fatalf("bad first receipt %+v", first)
	}
	duplicate, err := s.Publish(ctx, hash, create)
	requireNoError(t, err)
	if duplicate != first {
		t.Fatal("duplicate created a new artifact")
	}
	state := readState(t, s, hash, first.ArtifactID)
	if string(state.State) != "{}" || state.Source.HTML != "<p>é</p>\r\n<script>throw 1</script>" {
		t.Fatalf("altered initial content %+v", state)
	}
	request := saveJSON(first.ArtifactID, "save", 1, 1, `{"unknown":{"kept":true},"zero":0,"off":false,"empty":"","nil":null,"number":1.0}`)
	saved, err := s.SaveState(ctx, hash, request)
	requireNoError(t, err)
	if saved.SourceRevision != 1 || saved.StateVersion != 2 {
		t.Fatalf("wrong checkpoint counters %+v", saved)
	}
	published, err := s.Publish(ctx, hash, publishJSON(first.ArtifactID, "publish", 1, 2))
	requireNoError(t, err)
	if published.SourceRevision != 2 || published.StateVersion != 2 {
		t.Fatalf("wrong publication counters %+v", published)
	}
	after := readState(t, s, hash, first.ArtifactID)
	if string(after.State) != `{"empty":"","nil":null,"number":1.0,"off":false,"unknown":{"kept":true},"zero":0}` {
		t.Fatalf("publication changed state %s", after.State)
	}
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
	_, err = s.SaveState(ctx, hash, request)
	requireCode(t, err, NotFoundOrForbidden)
	fresh := sha256.Sum256([]byte("renewed"))
	requireNoError(t, s.InstallGrant(ctx, fresh, testScope()))
	retried, err := s.SaveState(ctx, fresh, request)
	requireNoError(t, err)
	if retried != saved {
		t.Fatalf("retry returned current head: %+v", retried)
	}
	changed := saveJSON(first.ArtifactID, "save", 1, 1, `{"changed":true}`)
	_, err = s.SaveState(ctx, fresh, changed)
	requireCode(t, err, MutationIDReused)
	after = readState(t, s, fresh, first.ArtifactID)
	if after.SourceRevision != 2 || after.StateVersion != 2 {
		t.Fatal("retry advanced head")
	}
	metadata, err := s.Read(ctx, fresh, readJSON(first.ArtifactID))
	requireNoError(t, err)
	if metadata.Source != nil || metadata.State != nil {
		t.Fatal("metadata read disclosed content")
	}
}

func TestStoreFingerprintAndCreationScope(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"))
	requireNoError(t, err)
	raw := saveJSON(created.ArtifactID, "save", 1, 1, `{"x":1,"y":["a","c"]}`)
	saved, err := s.SaveState(ctx, hash, raw)
	requireNoError(t, err)
	equivalent := saveJSON(created.ArtifactID, "save", 1, 1, `{"y":["a","c"],"x":1}`)
	again, err := s.SaveState(ctx, hash, equivalent)
	requireNoError(t, err)
	if again != saved {
		t.Fatal("object order changed fingerprint")
	}
	for _, body := range []string{`{"x":1.0,"y":["a","c"]}`, `{"x":1,"y":["c","a"]}`} {
		_, err := s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "save", 1, 1, body))
		requireCode(t, err, MutationIDReused)
	}
	withDefault := strings.Replace(string(createJSON("create")), `"title"`, `"initialState":{},"title"`, 1)
	_, err = s.Publish(ctx, hash, []byte(withDefault))
	requireCode(t, err, MutationIDReused)
	scope := testScope()
	scope.ArtifactID = "other"
	restricted := sha256.Sum256([]byte("restricted"))
	requireNoError(t, s.InstallGrant(ctx, restricted, scope))
	_, err = s.Publish(ctx, restricted, createJSON("create"))
	requireCode(t, err, NotFoundOrForbidden)
	_, err = s.Publish(ctx, restricted, createJSON("new"))
	requireCode(t, err, NotFoundOrForbidden)
	scope.ArtifactID = created.ArtifactID
	restricted = sha256.Sum256([]byte("matching restricted"))
	requireNoError(t, s.InstallGrant(ctx, restricted, scope))
	again, err = s.Publish(ctx, restricted, createJSON("create"))
	requireNoError(t, err)
	if again != created {
		t.Fatal("scoped exact receipt changed")
	}
}

func TestStoreConflictsRemainTerminal(t *testing.T) {
	for _, saveFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(saveFirst), func(t *testing.T) {
			s, hash, _ := setupStore(t, StoreOptions{})
			ctx := context.Background()
			created, err := s.Publish(ctx, hash, createJSON("create"))
			requireNoError(t, err)
			save := saveJSON(created.ArtifactID, "save", 1, 1, `{"selected":["a"]}`)
			publish := publishJSON(created.ArtifactID, "publish", 1, 1)
			var firstErr error
			if saveFirst {
				_, err = s.SaveState(ctx, hash, save)
				requireNoError(t, err)
				_, firstErr = s.Publish(ctx, hash, publish)
				requireCode(t, firstErr, StateConflict)
				_, err = s.Publish(ctx, hash, publishJSON(created.ArtifactID, "later", 1, 2))
			} else {
				_, err = s.Publish(ctx, hash, publish)
				requireNoError(t, err)
				_, firstErr = s.SaveState(ctx, hash, save)
				requireCode(t, firstErr, SourceConflict)
				_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "later", 2, 1, `{}`))
			}
			requireNoError(t, err)
			var retryErr error
			if saveFirst {
				_, retryErr = s.Publish(ctx, hash, publish)
			} else {
				_, retryErr = s.SaveState(ctx, hash, save)
			}
			if !reflect.DeepEqual(firstErr, retryErr) {
				t.Fatalf("conflict changed after later head: %v / %v", firstErr, retryErr)
			}
			_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "both stale", 1, 1, `{}`))
			conflict := requireCode(t, err, SourceConflict)
			if conflict.SourceRevision != 2 || conflict.StateVersion != 2 {
				t.Fatalf("wrong conflict versions %+v", conflict)
			}
		})
	}
}

func TestStoreMutationAuthorizationAndTombstone(t *testing.T) {
	now := fixedClock()
	s, hash, path := setupStore(t, StoreOptions{Clock: func() time.Time { return now }})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"))
	requireNoError(t, err)
	methods := []string{"artifact_read"}
	scope := testScope()
	scope.Methods = methods
	readOnly := sha256.Sum256([]byte("read only"))
	requireNoError(t, s.InstallGrant(ctx, readOnly, scope))
	methods[0] = "artifact_publish"
	_, err = s.Publish(ctx, readOnly, createJSON("create"))
	requireCode(t, err, NotFoundOrForbidden)
	_, err = s.Read(ctx, readOnly, readJSON(created.ArtifactID))
	requireNoError(t, err)
	scope = testScope()
	scope.RealmID = "wrong"
	requireCode(t, s.InstallGrant(ctx, sha256.Sum256([]byte("wrong realm")), scope), NotFoundOrForbidden)
	_, err = s.Read(ctx, hash, readJSON("absent"))
	requireCode(t, err, NotFoundOrForbidden)
	now = testScope().ExpiresAt
	_, err = s.Publish(ctx, hash, createJSON("create"))
	requireCode(t, err, NotFoundOrForbidden)
	now = fixedClock()
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	_, err = s.Publish(ctx, hash, createJSON("create"))
	requireCode(t, err, NotFoundOrForbidden)
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
	requireCode(t, s.InstallGrant(ctx, hash, testScope()), NotFoundOrForbidden)
	var revisions int
	requireNoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_revisions").Scan(&revisions))
	if revisions != 0 {
		t.Fatal("tombstone retained content")
	}
	var receipts int
	requireNoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_mutations").Scan(&receipts))
	if receipts != 1 {
		t.Fatal("tombstone erased dedup identity")
	}
}
