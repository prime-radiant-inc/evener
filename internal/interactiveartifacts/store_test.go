package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func requireCode(t *testing.T, err error, code ErrorCode) *DomainError {
	t.Helper()
	var domain *DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	return domain
}
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func openTestStore(t *testing.T, path string, options StoreOptions) *Store {
	t.Helper()
	s, err := OpenStore(path, options)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	return s
}
func testScope() Scope {
	return Scope{RealmID: "realm", PrincipalID: "principal", NamespaceID: "namespace", Methods: []string{"artifact_publish", "artifact_save_state", "artifact_read", "artifact_get_view", "artifact_list", "artifact_open", "artifact_report_diagnostic"}, ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Generation: 1}
}
func fixedClock() time.Time { return time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC) }

func TestStorePrivateIdentityAndNamespaceRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "artifact ?#' db.sqlite")
	s := openTestStore(t, path, StoreOptions{Clock: fixedClock})
	identity := s.ServiceID()
	if identity == "" {
		t.Fatal("missing durable service identity")
	}
	requireNoError(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"))
	requireNoError(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"))
	if err := s.EnsureNamespace(ctx, "namespace", "realm", "other"); err == nil {
		t.Fatal("reassigned owner")
	}
	hash := sha256.Sum256([]byte("temporary"))
	requireNoError(t, s.InstallGrant(ctx, hash, testScope()))
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
	if s.ServiceID() != identity {
		t.Fatal("service identity changed")
	}
	requireNoError(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"))
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	requireCode(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"), Deleted)
	requireCode(t, s.InstallGrant(ctx, hash, testScope()), NotFoundOrForbidden)
	requireNoError(t, s.TombstoneNamespace(ctx, "delayed", "realm", "owner"))
	requireCode(t, s.EnsureNamespace(ctx, "delayed", "realm", "owner"), Deleted)
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
	requireCode(t, s.EnsureNamespace(ctx, "namespace", "realm", "owner"), Deleted)
	for _, name := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(name)
		requireNoError(t, err)
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("public permissions on %s: %v", name, info.Mode())
		}
	}
}

func TestStoreRefusesFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "future.sqlite")
	requireNoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	requireNoError(t, err)
	requireNoError(t, f.Close())
	db, err := sql.Open("sqlite", path)
	requireNoError(t, err)
	_, err = db.Exec("PRAGMA user_version=200")
	requireNoError(t, err)
	requireNoError(t, db.Close())
	if s, err := OpenStore(path, StoreOptions{}); err == nil {
		requireNoError(t, s.Close())
		t.Fatal("opened future schema")
	}
	db, err = sql.Open("sqlite", path)
	requireNoError(t, err)
	defer db.Close()
	var version int
	requireNoError(t, db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != 200 {
		t.Fatalf("downgraded to %d", version)
	}
}
