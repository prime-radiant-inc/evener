package interactiveartifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestServiceRejectsUnsafeTraversalBeforeOwnerLock(t *testing.T) {
	for _, traversal := range []string{"direct", "alias-parent", "dot-dot"} {
		t.Run(traversal, func(t *testing.T) {
			base := t.TempDir()
			unsafe := filepath.Join(base, "writable")
			requireNoError(t, os.Mkdir(unsafe, 0700))
			requireNoError(t, os.Chmod(unsafe, 0777))
			root := filepath.Join(unsafe, "private")
			switch traversal {
			case "alias-parent":
				target := filepath.Join(base, "target")
				requireNoError(t, os.Mkdir(target, 0700))
				requireNoError(t, os.Symlink(target, filepath.Join(unsafe, "alias")))
				root = filepath.Join(unsafe, "alias", "private")
			case "dot-dot":
				// Preserve the caller's traversal: filepath.Join would hide it.
				root = unsafe + "/../private"
			}
			s, err := startService(root, StoreOptions{})
			if err == nil {
				requireNoError(t, s.close(context.Background()))
				t.Error("service started through replaceable ancestor")
			}
			for _, name := range []string{"owner.lock", "artifacts.sqlite"} {
				if _, err := os.Stat(root + "/" + name); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("created %s before validating traversal: %v", name, err)
				}
			}
		})
	}
}

func TestServiceCanonicalAliasSharesOwnerLock(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	requireNoError(t, os.Mkdir(target, 0700))
	alias := filepath.Join(base, "alias")
	requireNoError(t, os.Symlink(target, alias))
	root := filepath.Join(alias, "private")
	s, err := startService(root, StoreOptions{})
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, s.close(context.Background())) })
	canonical, err := filepath.EvalSymlinks(root)
	requireNoError(t, err)
	var sequence int
	var name, databasePath string
	requireNoError(t, s.store.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &databasePath))
	if databasePath != filepath.Join(canonical, "artifacts.sqlite") {
		t.Fatalf("SQLite did not use canonical root: %s", databasePath)
	}
	for _, path := range []string{root, canonical} {
		other, err := startService(path, StoreOptions{})
		if err == nil {
			requireNoError(t, other.close(context.Background()))
			t.Fatal("alias bypassed the live owner's lock")
		}
	}
}

func TestServiceMacOSSystemAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system /var alias")
	}
	root, err := os.MkdirTemp("/var/tmp", "artifact-service-path-")
	requireNoError(t, err)
	t.Cleanup(func() {
		for _, name := range []string{"owner.lock", "artifacts.sqlite", "artifacts.sqlite-wal", "artifacts.sqlite-shm"} {
			_ = os.Remove(filepath.Join(root, name))
		}
		requireNoError(t, os.Remove(root))
	})
	s, err := startService(root, StoreOptions{})
	requireNoError(t, err)
	requireNoError(t, s.close(context.Background()))
}

func TestServiceReadinessMatchesStoreSchema(t *testing.T) {
	s, _ := serviceFixture(t)
	var schema int
	requireNoError(t, s.store.db.QueryRow("PRAGMA user_version").Scan(&schema))
	if schema != StoreSchemaVersion || s.ready.SchemaVersion != schema || s.ready.ContractVersion != 1 || s.ready.CatalogVersion != 1 {
		t.Fatalf("database schema=%d exported=%d readiness=%+v", schema, StoreSchemaVersion, s.ready)
	}
	supervisor := processSupervisor(t, filepath.Join(t.TempDir(), "private"), testPolicy)
	ready, err := supervisor.Ensure(t.Context())
	requireNoError(t, err)
	if ready.SchemaVersion != StoreSchemaVersion {
		t.Fatalf("supervisor accepted wrong schema: %+v", ready)
	}
}

func TestSupervisorReadinessCompatibility(t *testing.T) {
	s, _ := serviceFixture(t)
	if !compatible(s.ready) {
		t.Fatal("rejected actual bundled service readiness")
	}
	for name, change := range map[string]func(*Readiness){
		"schema-1":      func(r *Readiness) { r.SchemaVersion = 1 },
		"future-schema": func(r *Readiness) { r.SchemaVersion = StoreSchemaVersion + 1 },
		"contract":      func(r *Readiness) { r.ContractVersion++ },
		"catalog":       func(r *Readiness) { r.CatalogVersion++ },
		"core":          func(r *Readiness) { r.CoreVersion = "2025-03-26" },
		"application":   func(r *Readiness) { r.ApplicationVersion = "unknown" },
		"service-id":    func(r *Readiness) { r.ServiceID = "" },
		"run-id":        func(r *Readiness) { r.ServiceRunID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			ready := s.ready
			change(&ready)
			if compatible(ready) {
				t.Fatalf("accepted incompatible readiness: %+v", ready)
			}
		})
	}
}
