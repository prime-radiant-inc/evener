package hubcore

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/rendezvous"
)

type failingFs struct {
	afero.Fs
	mkdirErr error
	statErr  error
	chmodErr error
}

func (f failingFs) MkdirAll(string, os.FileMode) error { return f.mkdirErr }
func (f failingFs) Stat(string) (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return nil, os.ErrNotExist
}
func (f failingFs) Chmod(string, os.FileMode) error { return f.chmodErr }

func fuzzScenarioCoveragePersistenceEdges(t *testing.T) {
	t.Run("setters callbacks and empty paths", func(t *testing.T) {
		calls := 0
		a := NewArchiveStore("").SetFs(afero.NewMemMapFs())
		a.SetOnChange(func() { calls++ })
		a.fireChange()
		if err := a.Set("session", "s", true, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := a.Delete("session", "s"); err != nil {
			t.Fatal(err)
		}
		if got, err := a.Decisions(); err != nil || len(got) != 0 {
			t.Fatalf("Decisions = %v, %v", got, err)
		}

		f := NewFavoriteStore("").SetFs(afero.NewMemMapFs())
		f.SetOnChange(func() { calls++ })
		f.fireChange()
		if err := f.Set("session", "s", true, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := f.Delete("session", "s"); err != nil {
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
		if err := a.Set("session", "s", true, time.Time{}); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if err := a.Delete("session", "s"); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if _, err := a.Decisions(); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}

		f := NewFavoriteStore("x").SetFs(failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errBoom, statErr: errors.New("stat")})
		if err := f.Set("session", "s", true, time.Time{}); !errors.Is(err, errBoom) {
			t.Fatal(err)
		}
		if err := f.Delete("session", "s"); !errors.Is(err, errBoom) {
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
			{"archive set", func(p string) error { return NewArchiveStore(p).Set("session", "s", true, time.Now()) }},
			{"archive delete", func(p string) error { return NewArchiveStore(p).Delete("session", "s") }},
			{"favorite set", func(p string) error { return NewFavoriteStore(p).Set("session", "s", true, time.Now()) }},
			{"favorite delete", func(p string) error { return NewFavoriteStore(p).Delete("session", "s") }},
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
					if err := s.Set("session", "s", true, time.Now()); err == nil {
						t.Fatal("expected set error")
					}
					if err := s.Delete("session", "s"); err == nil {
						t.Fatal("expected delete error")
					}
					if _, err := s.Decisions(); err == nil {
						t.Fatal("expected query error")
					}
				} else {
					s := NewFavoriteStore(p)
					if err := s.Set("session", "s", true, time.Now()); err == nil {
						t.Fatal("expected set error")
					}
					if err := s.Delete("session", "s"); err == nil {
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

func fuzzScenarioCoveragePureEdges(t *testing.T) {
	if attentionLevel("unknown") != "idle" {
		t.Fatal("attention fallback")
	}
	attn, _ := DeriveAttention(nil, []LiveEntry{{SessionID: "abcdefghijklmnop", Status: "active"}, {SessionID: "", Status: "active"}}, nil)
	if attn["abcdefghijklmnop"].Title != "session klmnop" {
		t.Fatalf("attention = %#v", attn)
	}

	i := NewPastIndex("")
	if err := i.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := i.mergeSearchResults(nil, nil, 1, 0); got != nil {
		t.Fatal(got)
	}
	if got := i.RecentModels(0); got != nil {
		t.Fatal(got)
	}
	d := map[string][]string{"w": {"", "b", "a", "b"}}
	dedupObserverLists(d)
	if strings.Join(d["w"], ",") != "a,b" {
		t.Fatal(d)
	}
	if q := ftsQuery("!!!"); q != "" {
		t.Fatal(q)
	}
	if q := ftsQuery("A_b c"); q != "a_b* AND c*" {
		t.Fatal(q)
	}

	if decisionFor(nil, "") != nil {
		t.Fatal("empty decision")
	}
	path := t.TempDir()
	projects := ResolveProjectMap([]schema.SessionMeta{{ID: "s", EnvInfo: schema.EnvironmentInfo{WorkingDir: path}}}, nil)
	_, _ = BuildProjectTree([]schema.SessionMeta{{ID: "s", EnvInfo: schema.EnvironmentInfo{WorkingDir: path}}}, nil, nil, projects[path].ID)
	BuildTreeAt([]schema.SessionMeta{{ID: "sub", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/p"}}}, []LiveEntry{{SessionID: ""}, {SessionID: "missing-session-id", Status: "awaiting"}, {SessionID: "sub", Status: "awaiting"}}, nil, time.Now())
}

func fuzzScenarioCoverageHTTPFailures(t *testing.T) {
	for _, h := range []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) }),
	} {
		s := httptest.NewServer(h)
		addr := strings.TrimPrefix(s.URL, "http://")
		ok := (&StatusProber{}).Probe(rendezvous.Entry{Address: addr}).OK
		s.Close()
		if ok {
			t.Fatal("probe unexpectedly succeeded")
		}
	}
	ok := (&StatusProber{Timeout: time.Nanosecond}).Probe(rendezvous.Entry{Address: "127.0.0.1:1"}).OK
	if ok {
		t.Fatal("probe unexpectedly succeeded")
	}
	ok = (&StatusProber{}).Probe(rendezvous.Entry{Address: "%"}).OK
	if ok {
		t.Fatal("invalid URL unexpectedly succeeded")
	}

	p := NewRESTProxy(fakeResolver{})
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/bad", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatal(rr.Code)
	}
	p.proxyFor("127.0.0.1:1")
	p.proxyFor("127.0.0.1:1")
}

type fakeResolver struct{}

func (fakeResolver) Find(string) (LiveEntry, bool) { return LiveEntry{}, false }
