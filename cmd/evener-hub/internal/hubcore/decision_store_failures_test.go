package hubcore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// fuzzScenarioDecisionStoresSurfaceStorageFailures pins the archive and
// favorite decision stores at their storage edges: with no database path they
// accept writes as no-ops and read empty, and every filesystem, database,
// schema and row-decoding failure is returned to the caller.
func fuzzScenarioDecisionStoresSurfaceStorageFailures(t *testing.T) {
	t.Run("setters callbacks and empty paths", func(t *testing.T) {
		calls := 0
		a := NewArchiveStore("").SetFs(afero.NewMemMapFs())
		a.SetOnChange(func() { calls++ })
		a.fireChange()
		if err := a.Set("", "session", "s", true, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := a.Delete("", "session", "s"); err != nil {
			t.Fatal(err)
		}
		if got, err := a.Decisions(); err != nil || len(got) != 0 {
			t.Fatalf("Decisions = %v, %v", got, err)
		}

		f := NewFavoriteStore("").SetFs(afero.NewMemMapFs())
		f.SetOnChange(func() { calls++ })
		f.fireChange()
		if err := f.Set("", "session", "s", true, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := f.Delete("", "session", "s"); err != nil {
			t.Fatal(err)
		}
		if got, err := f.Favorites(); err != nil || len(got) != 0 {
			t.Fatalf("Favorites = %v, %v", got, err)
		}
		if calls != 2 {
			t.Fatalf("callbacks = %d", calls)
		}

		idx := NewPastIndex("glob").SetFs(afero.NewMemMapFs())
		if idx.StateGlob() != "glob" {
			t.Fatal(idx.StateGlob())
		}
	})

	errBoom := errors.New("boom")
	t.Run("filesystem errors", func(t *testing.T) {
		a := NewArchiveStore("x").SetFs(failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errBoom, statErr: errors.New("stat")})
		if err := a.Set("", "session", "s", true, time.Time{}); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if err := a.Delete("", "session", "s"); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if _, err := a.Decisions(); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}

		f := NewFavoriteStore("x").SetFs(failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errBoom, statErr: errors.New("stat")})
		if err := f.Set("", "session", "s", true, time.Time{}); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if err := f.Delete("", "session", "s"); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if _, err := f.Favorites(); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}

		i := NewPastIndexWithDB("", "x").SetFs(failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errBoom})
		if err := i.rebuildFTS(nil); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if err := chmodSQLiteIndexFilesFS(failingFs{Fs: afero.NewMemMapFs(), chmodErr: errBoom}, "x"); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
	})

	t.Run("real database errors", func(t *testing.T) {
		dir := t.TempDir()
		for _, tc := range []struct {
			name string
			run  func(string) error
		}{
			{"archive set", func(p string) error { return NewArchiveStore(p).Set("", "session", "s", true, time.Now()) }},
			{"archive delete", func(p string) error { return NewArchiveStore(p).Delete("", "session", "s") }},
			{"favorite set", func(p string) error { return NewFavoriteStore(p).Set("", "session", "s", true, time.Now()) }},
			{"favorite delete", func(p string) error { return NewFavoriteStore(p).Delete("", "session", "s") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.run(dir); err == nil {
					t.Fatal("expected error")
				}
			})
		}
	})

	t.Run("incompatible schemas", func(t *testing.T) {
		for _, table := range []string{"archive", "favorite"} {
			t.Run(table, func(t *testing.T) {
				p := filepath.Join(t.TempDir(), "index.db")
				db, err := sql.Open("sqlite", p)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("CREATE TABLE " + table + " (wrong TEXT)"); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if table == "archive" {
					s := NewArchiveStore(p)
					if err := s.Set("", "session", "s", true, time.Now()); err == nil {
						t.Fatal("expected set error")
					}
					if err := s.Delete("", "session", "s"); err == nil {
						t.Fatal("expected delete error")
					}
					if _, err := s.Decisions(); err == nil {
						t.Fatal("expected query error")
					}
				} else {
					s := NewFavoriteStore(p)
					if err := s.Set("", "session", "s", true, time.Now()); err == nil {
						t.Fatal("expected set error")
					}
					if err := s.Delete("", "session", "s"); err == nil {
						t.Fatal("expected delete error")
					}
					if _, err := s.Favorites(); err == nil {
						t.Fatal("expected query error")
					}
				}
			})
		}
	})

	t.Run("scan errors", func(t *testing.T) {
		for _, table := range []string{"archive", "favorite"} {
			p := filepath.Join(t.TempDir(), "index.db")
			db, err := sql.Open("sqlite", p)
			if err != nil {
				t.Fatal(err)
			}
			if table == "archive" {
				_, err = db.Exec("CREATE TABLE archive(kind TEXT, id TEXT, archived INTEGER, decided_at INTEGER); INSERT INTO archive VALUES(NULL, 's', 1, 0)")
			} else {
				_, err = db.Exec("CREATE TABLE favorite(kind TEXT, id TEXT, favorited INTEGER, decided_at INTEGER); INSERT INTO favorite VALUES(NULL, 's', 1, 0)")
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if table == "archive" {
				if _, err := NewArchiveStore(p).Decisions(); err == nil {
					t.Fatal("expected scan error")
				}
			} else if _, err := NewFavoriteStore(p).Favorites(); err == nil {
				t.Fatal("expected scan error")
			}
		}
	})
}
