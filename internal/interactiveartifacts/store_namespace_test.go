package interactiveartifacts

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreNamespaceDeletionTimeSurvivesRetryAndRecovery(t *testing.T) {
	now := fixedClock()
	options := StoreOptions{Clock: func() time.Time { return now }}
	s, _, path := setupStore(t, options)
	ctx := context.Background()
	assertDeletionTime(t, s, "namespace", sql.NullString{})
	now = now.Add(time.Minute)
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	first := sql.NullString{String: "2026-09-18T00:01:00Z", Valid: true}
	assertDeletionTime(t, s, "namespace", first)
	now = now.Add(time.Minute)
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	assertDeletionTime(t, s, "namespace", first)
	requireNoError(t, s.TombstoneNamespace(ctx, "delayed", "realm", "owner"))
	delayed := sql.NullString{String: "2026-09-18T00:02:00Z", Valid: true}
	assertDeletionTime(t, s, "delayed", delayed)
	requireNoError(t, s.Close())
	s = openTestStore(t, path, options)
	now = now.Add(time.Minute)
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	requireNoError(t, s.TombstoneNamespace(ctx, "delayed", "realm", "owner"))
	requireCode(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"), Deleted)
	requireCode(t, s.EnsureNamespace(ctx, "delayed", "realm", "owner"), Deleted)
	assertDeletionTime(t, s, "namespace", first)
	assertDeletionTime(t, s, "delayed", delayed)
	snapshot := filepath.Join(t.TempDir(), "private", "deleted.sqlite")
	requireNoError(t, s.Backup(ctx, snapshot))
	restored := openTestStore(t, snapshot, options)
	now = now.Add(time.Minute)
	requireNoError(t, restored.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	requireNoError(t, restored.TombstoneNamespace(ctx, "delayed", "realm", "owner"))
	assertDeletionTime(t, restored, "namespace", first)
	assertDeletionTime(t, restored, "delayed", delayed)
}

func assertDeletionTime(t *testing.T, s *Store, id string, want sql.NullString) {
	t.Helper()
	var got sql.NullString
	requireNoError(t, s.db.QueryRowContext(context.Background(), "SELECT tombstoned_at FROM artifact_namespaces WHERE namespace_id=?", id).Scan(&got))
	if got != want {
		t.Fatalf("namespace %s deletion time %+v, want %+v", id, got, want)
	}
}
