package hubcore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/afero"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubtest"
	"primeradiant.com/serf/identifier"
)

var errExternalEdge = errors.New("external edge")

type edgeConnector struct{ stage string }

func (c edgeConnector) Connect(context.Context) (driver.Conn, error) {
	return &edgeConn{stage: c.stage}, nil
}
func (c edgeConnector) Driver() driver.Driver { return edgeDriver{} }

type edgeDriver struct{}

func (edgeDriver) Open(string) (driver.Conn, error) { return &edgeConn{}, nil }

type edgeConn struct {
	stage string
	execs int
}

func (c *edgeConn) Prepare(string) (driver.Stmt, error) {
	if c.stage == "prepare" {
		return nil, errExternalEdge
	}
	return edgeStmt{stage: c.stage}, nil
}
func (c *edgeConn) Close() error {
	if c.stage == "close" {
		return errExternalEdge
	}
	return nil
}
func (c *edgeConn) Begin() (driver.Tx, error) {
	if c.stage == "begin" {
		return nil, errExternalEdge
	}
	return edgeTx{stage: c.stage}, nil
}
func (c *edgeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.execs++
	if c.stage == "exec" || (c.stage == "tx-exec" && c.execs == 2) {
		return nil, errExternalEdge
	}
	return driver.RowsAffected(1), nil
}
func (c *edgeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.stage == "query" {
		return nil, errExternalEdge
	}
	return &edgeRows{stage: c.stage}, nil
}

type edgeTx struct{ stage string }

func (t edgeTx) Commit() error {
	if t.stage == "commit" {
		return errExternalEdge
	}
	return nil
}
func (edgeTx) Rollback() error { return nil }

type edgeStmt struct{ stage string }

func (edgeStmt) Close() error  { return nil }
func (edgeStmt) NumInput() int { return -1 }
func (edgeStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (s edgeStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	if s.stage == "stmt-exec" {
		return nil, errExternalEdge
	}
	return driver.RowsAffected(1), nil
}
func (edgeStmt) Query([]driver.Value) (driver.Rows, error) { return &edgeRows{}, nil }

type edgeRows struct {
	stage string
	done  bool
}

func (r *edgeRows) Columns() []string {
	if r.stage == "scan" {
		return []string{"id", "extra"}
	}
	return []string{"id"}
}
func (*edgeRows) Close() error { return nil }
func (r *edgeRows) Next(dest []driver.Value) error {
	if r.stage == "rows" {
		return errExternalEdge
	}
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = "missing"
	if r.stage == "scan" {
		dest[1] = "extra"
	}
	return nil
}

func edgeOpen(stage string) func(string, string) (*sql.DB, error) {
	return func(string, string) (*sql.DB, error) {
		if stage == "open" {
			return nil, errExternalEdge
		}
		return sql.OpenDB(edgeConnector{stage: stage}), nil
	}
}

func fuzzScenarioPersistenceExternalErrors(t *testing.T) {
	if got, err := NewArchiveStore("missing").SetFs(afero.NewMemMapFs()).Decisions(); err != nil || len(got) != 0 {
		t.Fatalf("missing archive: %v, %v", got, err)
	}
	if got, err := NewFavoriteStore("missing").SetFs(afero.NewMemMapFs()).Favorites(); err != nil || len(got) != 0 {
		t.Fatalf("missing favorite: %v, %v", got, err)
	}

	for _, stage := range []string{"open", "exec"} {
		a := NewArchiveStore("x").SetFs(afero.NewMemMapFs())
		a.openDB = edgeOpen(stage)
		if err := a.openOnly(); !errors.Is(err, errExternalEdge) {
			t.Fatalf("archive %s: %v", stage, err)
		}
		f := NewFavoriteStore("x").SetFs(afero.NewMemMapFs())
		f.openDB = edgeOpen(stage)
		if err := f.openOnly(); !errors.Is(err, errExternalEdge) {
			t.Fatalf("favorite %s: %v", stage, err)
		}
	}

	for _, stage := range []string{"rows"} {
		fs := afero.NewMemMapFs()
		if err := afero.WriteFile(fs, "x", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		a := NewArchiveStore("x").SetFs(fs)
		a.openDB = edgeOpen(stage)
		if _, err := a.Decisions(); !errors.Is(err, errExternalEdge) {
			t.Fatalf("archive rows: %v", err)
		}
		f := NewFavoriteStore("x").SetFs(fs)
		f.openDB = edgeOpen(stage)
		if _, err := f.Favorites(); !errors.Is(err, errExternalEdge) {
			t.Fatalf("favorite rows: %v", err)
		}
	}
}

func (s *ArchiveStore) openOnly() error {
	db, err := s.open()
	if db != nil {
		_ = db.Close()
	}
	return err
}

func (s *FavoriteStore) openOnly() error {
	db, err := s.open()
	if db != nil {
		_ = db.Close()
	}
	return err
}

func fuzzScenarioPastIndexSQLLifecycleErrors(t *testing.T) {
	entry := PastEntry{ID: "id", Meta: schema.SessionMeta{ID: "id"}}
	for _, stage := range []string{"open", "exec", "begin", "tx-exec", "prepare", "stmt-exec", "commit", "close"} {
		i := NewPastIndexWithDB("", "x").SetFs(afero.NewMemMapFs())
		i.openDB = edgeOpen(stage)
		if err := i.rebuildFTS([]PastEntry{entry}); !errors.Is(err, errExternalEdge) {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	for _, stage := range []string{"open", "query", "scan", "rows"} {
		i := NewPastIndexWithDB("", "x")
		i.openDB = edgeOpen(stage)
		i.fts = true
		if got, ok := i.searchFTS("term"); ok || got != nil {
			t.Fatalf("%s: %#v, %v", stage, got, ok)
		}
	}
}

type edgeWatcher struct {
	addErr error
	events chan fsnotify.Event
	errors chan error
}

func (w *edgeWatcher) Add(string) error              { return w.addErr }
func (*edgeWatcher) Close() error                    { return nil }
func (w *edgeWatcher) Events() <-chan fsnotify.Event { return w.events }
func (w *edgeWatcher) Errors() <-chan error          { return w.errors }

type edgeTicker struct{ c <-chan time.Time }

func (t edgeTicker) C() <-chan time.Time { return t.c }
func (edgeTicker) Stop()                 {}

func fuzzScenarioRosterWatcherExternalEdges(t *testing.T) {
	r := NewRoster("", nil).SetFs(afero.NewMemMapFs())
	r.newWatcher = func() (rosterWatcher, error) { return nil, errExternalEdge }
	if err := r.Watch(context.Background()); !errors.Is(err, errExternalEdge) {
		t.Fatal(err)
	}

	w := &edgeWatcher{addErr: errExternalEdge, events: make(chan fsnotify.Event), errors: make(chan error)}
	r.newWatcher = func() (rosterWatcher, error) { return w, nil }
	if err := r.Watch(context.Background()); !errors.Is(err, errExternalEdge) {
		t.Fatal(err)
	}

	for _, send := range []func(*edgeWatcher){
		func(w *edgeWatcher) { close(w.events) },
		func(w *edgeWatcher) { w.events <- fsnotify.Event{}; close(w.events) },
	} {
		w = &edgeWatcher{events: make(chan fsnotify.Event, 2), errors: make(chan error, 1)}
		r = NewRoster("", nil).SetFs(afero.NewMemMapFs())
		r.newWatcher = func() (rosterWatcher, error) { return w, nil }
		send(w)
		if err := r.Watch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	w = &edgeWatcher{events: make(chan fsnotify.Event), errors: make(chan error)}
	r = NewRoster("", nil).SetFs(afero.NewMemMapFs())
	r.newWatcher = func() (rosterWatcher, error) { return w, nil }
	go func() {
		w.errors <- nil
		w.errors <- errExternalEdge
		close(w.events)
	}()
	if err := r.Watch(context.Background()); err != nil {
		t.Fatal(err)
	}

	w = &edgeWatcher{events: make(chan fsnotify.Event), errors: make(chan error)}
	r = NewRoster("", nil).SetFs(afero.NewMemMapFs())
	r.newWatcher = func() (rosterWatcher, error) { return w, nil }
	tick := make(chan time.Time)
	r.newTicker = func(time.Duration) rosterTicker { return edgeTicker{tick} }
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		tick <- time.Now()
		cancel()
	}()
	if err := r.Watch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func fuzzScenarioRemainingPureBranches(t *testing.T) {
	NewRoster("", nil).SetFs(afero.NewMemMapFs())
	NewRoster("\x00", nil).Refresh()
	NewPastIndex("").UpdateMeta("missing", schema.SessionMeta{})
	if compareOrderText("b", "B") <= 0 {
		t.Fatal("case-sensitive fallback")
	}

	projectDir := t.TempDir()
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := []schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB", ParentSessionID: "02wMz5Txv2enqVTitaig6F", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p"}},
		{ID: "02wMz5Txv2enqVTitaig6F", ForkLabel: "b", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p"}, UpdatedAt: time.Unix(1, 0)},
		{ID: "02wMz5Txv2enqVTitaig6F", ForkLabel: "a", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p"}, UpdatedAt: time.Unix(2, 0)},
	}
	BuildTreeAt(metas, nil, nil, time.Now())
	if _, ok := BuildProjectTreeAt([]schema.SessionMeta{{ID: "02wMz5Txv1C3Hut0M8GCeB", EnvInfo: schema.EnvironmentInfo{WorkingDir: projectDir}}}, nil, nil, time.Now(), project.ID); !ok {
		t.Fatal("missing project")
	}
}

// A session dir whose jobs.jsonl cannot be read contributes no grants and is
// named in the skip report, the same silent-absence class as an unindexable
// meta or project. The fold walks the sessions dir through the injected
// afero.Fs while agent.LoadSessionObserverGrants reads the real filesystem, so
// the fixture is seeded on both: the memfs entry makes the fold consider the
// session at all, and the on-disk jobs.jsonl-as-a-directory is what makes the
// load fail.
func fuzzScenarioFoldObserverGrantsNamesUnreadableLog(t *testing.T) {
	projectRoot := t.TempDir()
	sessionID := hubtest.SessionID(t)
	sessionDir := filepath.Join(projectRoot, "sessions", sessionID)

	fs := afero.NewMemMapFs()
	if err := fs.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "jobs.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}

	into := make(map[string][]string)
	skipped := make(map[string]string)
	foldProjectObserverGrants(into, fs, projectRoot, skipped)

	if len(into) != 0 {
		t.Fatalf("unreadable log contributed grants: %v", into)
	}
	reason, ok := skipped[sessionDir]
	if !ok {
		t.Fatalf("skip report does not name %s, got %v", sessionDir, skipped)
	}
	if !strings.HasPrefix(reason, "load observer grants: ") {
		t.Fatalf("skip reason = %q, want the load-error reason", reason)
	}
}
