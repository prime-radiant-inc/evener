package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func TestStorePublicationRequiresTrustedThread(t *testing.T) {
	s, valid, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	scope := testScope()
	scope.OriginatingThreadID = ""
	hash := sha256.Sum256([]byte("missing thread"))
	requireCode(t, s.InstallGrant(ctx, hash, scope), NotFoundOrForbidden)
	scope.Methods = []string{"artifact_get_view", "artifact_save_state"}
	requireNoError(t, s.InstallGrant(ctx, hash, scope))
	for _, field := range []string{"originatingThreadId", "originatingToolCallId"} {
		raw := []byte(`{"mutationId":"injected","title":"T","summary":"S","html":"H","` + field + `":"forged"}`)
		_, err := s.Publish(ctx, valid, raw, PublicationOrigin{})
		requireCode(t, err, InvalidSource)
	}
}

func TestStoreRevisionOriginsAndAcceptanceTimesSurviveRecovery(t *testing.T) {
	now := fixedClock()
	options := StoreOptions{Clock: func() time.Time { return now }}
	s, hash, path := setupStore(t, options)
	ctx := context.Background()
	childA, childB := testScope(), testScope()
	childA.PrincipalID, childA.OriginatingThreadID = "principal-A", "child-thread-A"
	childB.PrincipalID, childB.OriginatingThreadID = "principal-B", "child-thread-B"
	grantA, grantB := sha256.Sum256([]byte("child A")), sha256.Sum256([]byte("child B"))
	requireNoError(t, s.InstallGrant(ctx, grantA, childA))
	requireNoError(t, s.InstallGrant(ctx, grantB, childB))
	created, err := s.Publish(ctx, grantA, createJSON("create"), PublicationOrigin{ToolCallID: "tool-call-A"})
	requireNoError(t, err)
	now = now.Add(time.Minute)
	save := saveJSON(created.ArtifactID, "save", 1, 1, `{"n":1}`)
	saved, err := s.SaveState(ctx, hash, save)
	requireNoError(t, err)
	now = now.Add(time.Minute)
	_, err = s.Publish(ctx, grantB, publishJSON(created.ArtifactID, "publish", 1, 2), PublicationOrigin{})
	requireNoError(t, err)
	now = now.Add(time.Minute)
	stale := saveJSON(created.ArtifactID, "conflict", 1, 2, `{"n":2}`)
	_, err = s.SaveState(ctx, hash, stale)
	requireCode(t, err, SourceConflict)
	now = now.Add(time.Minute)
	_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "later", 2, 2, `{"n":3}`))
	requireNoError(t, err)
	requireNoError(t, s.Close())
	s = openTestStore(t, path, options)
	requireNoError(t, s.InstallGrant(ctx, hash, testScope()))
	requireNoError(t, s.InstallGrant(ctx, grantA, childA))
	now = now.Add(time.Minute)
	retry, err := s.Publish(ctx, grantA, createJSON("create"), PublicationOrigin{ToolCallID: "different-correlation-on-retry"})
	requireNoError(t, err)
	if retry != created {
		t.Fatal("exact creation retry changed receipt")
	}
	retry, err = s.SaveState(ctx, hash, save)
	requireNoError(t, err)
	if retry != saved {
		t.Fatal("exact checkpoint retry changed receipt")
	}
	_, err = s.SaveState(ctx, hash, stale)
	conflict := requireCode(t, err, SourceConflict)
	if conflict.SourceRevision != 2 || conflict.StateVersion != 2 {
		t.Fatal("exact conflict retry changed outcome")
	}
	assertRevisionOriginsAndTimes(t, s, created.ArtifactID)
	snapshot := filepath.Join(t.TempDir(), "private", "snapshot.sqlite")
	requireNoError(t, s.Backup(ctx, snapshot))
	restored := openTestStore(t, snapshot, options)
	requireNoError(t, restored.InstallGrant(ctx, hash, testScope()))
	requireNoError(t, restored.InstallGrant(ctx, grantA, childA))
	now = now.Add(time.Minute)
	_, err = restored.Publish(ctx, grantA, createJSON("create"), PublicationOrigin{ToolCallID: "another-correlation"})
	requireNoError(t, err)
	_, err = restored.SaveState(ctx, hash, save)
	requireNoError(t, err)
	_, err = restored.SaveState(ctx, hash, stale)
	requireCode(t, err, SourceConflict)
	assertRevisionOriginsAndTimes(t, restored, created.ArtifactID)
}

func assertRevisionOriginsAndTimes(t *testing.T, s *Store, id string) {
	t.Helper()
	ctx := context.Background()
	type revision struct {
		Number            int
		Principal, Thread string
		Tool              sql.NullString
		Format            string
		FormatVersion     int
		Created           string
	}
	rows, err := s.db.QueryContext(ctx, `SELECT revision,created_by_principal_id,originating_thread_id,originating_tool_call_id,format,format_version,created_at FROM artifact_revisions WHERE artifact_id=? ORDER BY revision`, id)
	requireNoError(t, err)
	defer rows.Close()
	var got []revision
	for rows.Next() {
		var item revision
		requireNoError(t, rows.Scan(&item.Number, &item.Principal, &item.Thread, &item.Tool, &item.Format, &item.FormatVersion, &item.Created))
		got = append(got, item)
	}
	requireNoError(t, rows.Err())
	want := []revision{
		{1, "principal-A", "child-thread-A", sql.NullString{String: "tool-call-A", Valid: true}, "html", 1, "2026-09-18T00:00:00Z"},
		{2, "principal-B", "child-thread-B", sql.NullString{}, "html", 1, "2026-09-18T00:02:00Z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revision origin changed: %+v", got)
	}
	var owner string
	requireNoError(t, s.db.QueryRowContext(ctx, "SELECT owner_thread_id FROM artifact_namespaces WHERE namespace_id='namespace'").Scan(&owner))
	if owner != "owner" {
		t.Fatal("fixture lost distinct root namespace owner")
	}
	receipts, err := s.db.QueryContext(ctx, "SELECT mutation_id,committed_at FROM artifact_mutations ORDER BY mutation_id")
	requireNoError(t, err)
	defer receipts.Close()
	times := make(map[string]string)
	for receipts.Next() {
		var id, at string
		requireNoError(t, receipts.Scan(&id, &at))
		times[id] = at
	}
	requireNoError(t, receipts.Err())
	wantTimes := map[string]string{"create": "2026-09-18T00:00:00Z", "save": "2026-09-18T00:01:00Z", "publish": "2026-09-18T00:02:00Z", "conflict": "2026-09-18T00:03:00Z", "later": "2026-09-18T00:04:00Z"}
	if !reflect.DeepEqual(times, wantTimes) {
		t.Fatalf("receipt acceptance times changed: %v", times)
	}
}

func TestStoreProvenanceAndAcceptanceTimeCountTowardQuota(t *testing.T) {
	measure := func(thread, call string, at time.Time) int64 {
		t.Helper()
		s, _, _ := setupStore(t, StoreOptions{Clock: func() time.Time { return at }})
		scope := testScope()
		scope.OriginatingThreadID = thread
		hash := sha256.Sum256([]byte("publication"))
		requireNoError(t, s.InstallGrant(context.Background(), hash, scope))
		_, err := s.Publish(context.Background(), hash, createJSON("create"), PublicationOrigin{ToolCallID: call})
		requireNoError(t, err)
		tx, err := s.db.BeginTx(context.Background(), nil)
		requireNoError(t, err)
		defer tx.Rollback()
		total, err := logicalUsage(context.Background(), tx)
		requireNoError(t, err)
		return total
	}
	baseline := measure("a", "", fixedClock())
	// Six-byte thread replaces one byte; the optional UTF-8 call id adds seven.
	if extra := measure("longer", "call-ø", fixedClock()) - baseline; extra != 12 {
		t.Fatalf("provenance quota delta=%d, want 12", extra)
	}
	// The nanosecond spelling adds ten bytes in artifact created/updated, revision
	// created, and receipt committed timestamps; receipt time cannot be omitted.
	if extra := measure("a", "", fixedClock().Add(123456789*time.Nanosecond)) - baseline; extra != 40 {
		t.Fatalf("timestamp quota delta=%d, want 40", extra)
	}
}

func TestStoreRevisionFormatConstraints(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	for _, statement := range []string{"UPDATE artifact_revisions SET format='markdown' WHERE artifact_id=?", "UPDATE artifact_revisions SET format_version=2 WHERE artifact_id=?"} {
		_, err := s.db.ExecContext(ctx, statement, created.ArtifactID)
		var sqlError *sqlite.Error
		if !errors.As(err, &sqlError) || sqlError.Code() != 275 {
			t.Fatalf("want SQLite CHECK rejection, got %v", err)
		}
	}
}
