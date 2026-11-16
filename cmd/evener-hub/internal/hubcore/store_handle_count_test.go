package hubcore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
)

// countingDriverName registers a pass-through wrapper around the real SQLite
// driver that counts the driver-level calls a store makes, so a test can read
// what one navigation build actually costs instead of predicting it.
const countingDriverName = "sqlite-count-test"

var (
	countingDriverOnce sync.Once
	countingOpens      atomic.Int64
	countingExecs      atomic.Int64
	countingQueries    atomic.Int64
	countingStats      atomic.Int64
)

// countingDriver records opens, execs and queries and delegates everything
// else to modernc's own driver.
type countingDriver struct{ real driver.Driver }

// countingConn records the two statement calls and leaves Prepare alone, the
// way the real driver behaves: modernc's conn implements ExecerContext and
// QueryerContext, so database/sql takes that path and never calls Prepare.
type countingConn struct{ real driver.Conn }

func initCountingDriver() {
	countingDriverOnce.Do(func() {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic("countingDriver: cannot open sqlite: " + err.Error())
		}
		realDrv := db.Driver()
		_ = db.Close()
		sql.Register(countingDriverName, &countingDriver{real: realDrv})
	})
}

// countingOpen is the openDB seam every store is given in this test: it counts
// one open and hands back a handle on the counting driver.
func countingOpen(_, dataSourceName string) (*sql.DB, error) {
	countingOpens.Add(1)
	return sql.Open(countingDriverName, dataSourceName)
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.real.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{real: conn}, nil
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) { return c.real.Prepare(query) }

func (c *countingConn) Close() error { return c.real.Close() }

func (c *countingConn) Begin() (driver.Tx, error) { return c.real.Begin() } //nolint:staticcheck // the wrapped driver's own Begin, not a new API

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginTx, ok := c.real.(driver.ConnBeginTx); ok {
		return beginTx.BeginTx(ctx, opts)
	}
	return c.Begin()
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	countingExecs.Add(1)
	if execer, ok := c.real.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	countingQueries.Add(1)
	if queryer, ok := c.real.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

// countingFs counts the stat calls the stores make through their afero seam.
// SQLite's own file access bypasses afero, so a stat counted here is one the
// store code asked for — the keeper's file-identity check is the only one the
// shared handle adds, and it must stay small enough to be noise against the
// connect and DDL the handle removes.
type countingFs struct{ afero.Fs }

func (f countingFs) Stat(name string) (os.FileInfo, error) {
	countingStats.Add(1)
	return f.Fs.Stat(name)
}

// countTotals is a snapshot of the counters.
type countTotals struct {
	opens   int64
	execs   int64
	queries int64
	stats   int64
}

func (c countTotals) statements() int64 { return c.execs + c.queries }

func countReset() {
	countingOpens.Store(0)
	countingExecs.Store(0)
	countingQueries.Store(0)
	countingStats.Store(0)
}

func countRead() countTotals {
	return countTotals{
		opens:   countingOpens.Load(),
		execs:   countingExecs.Load(),
		queries: countingQueries.Load(),
		stats:   countingStats.Load(),
	}
}

// navigationBuildStores is one navigation build's read set: the index.db
// stores webNavigationSource.Capture reads, each behind its openDB seam so this
// test sees exactly the driver calls the build makes.
type navigationBuildStores struct {
	archive    *ArchiveStore
	favorite   *FavoriteStore
	pins       *PinSectionStore
	seen       *SessionSeenStore
	indexStore *PastIndex
}

// newNavigationBuildStores seeds one index.db with a row in every table the
// build reads, then returns the stores a build uses.
func newNavigationBuildStores(t *testing.T) navigationBuildStores {
	t.Helper()
	initCountingDriver()
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	now := time.Unix(1_700_000_000, 0)

	seedArchive := closeOnCleanup(t, NewArchiveStore(dbPath))
	if err := seedArchive.Set("", "session", "seeded-archive", true, now); err != nil {
		t.Fatal(err)
	}
	seedFavorite := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if err := seedFavorite.Set("", "session", "seeded-favorite", true, now); err != nil {
		t.Fatal(err)
	}
	seedPins := closeOnCleanup(t, NewPinSectionStore(dbPath))
	if _, _, err := seedPins.CreateOrReuseAndAssign("Seeded", "", "seeded-pin", now); err != nil {
		t.Fatal(err)
	}
	seenClock := now.Add(time.Hour)
	seedSeen := closeOnCleanup(t, NewSessionSeenStore(dbPath))
	seedSeen.now = func() time.Time { return seenClock }
	if _, err := seedSeen.MarkBatch([]SessionSeenMark{{SessionID: "seeded-seen", SeenThrough: seenClock}}); err != nil {
		t.Fatal(err)
	}

	// The FTS index is its own consumer of index.db: a search reads it, and a
	// publish writes it. Seed one session so a search has a table to read.
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		Name:           "counted",
		UpdatedAt:      now,
		OriginalPrompt: "counted needle",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/w"},
	})
	indexStore := closeOnCleanup(t, NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath))
	if _, err := indexStore.Rebuild(); err != nil {
		t.Fatal(err)
	}
	indexStore.openDB = countingOpen

	archive := closeOnCleanup(t, NewArchiveStore(dbPath))
	archive.openDB = countingOpen
	favorite := closeOnCleanup(t, NewFavoriteStore(dbPath))
	favorite.openDB = countingOpen
	pins := closeOnCleanup(t, NewPinSectionStore(dbPath))
	pins.openDB = countingOpen
	seen := closeOnCleanup(t, NewSessionSeenStore(dbPath))
	seen.openDB = countingOpen
	seen.now = func() time.Time { return seenClock }
	// The stat counter rides the same seam, so the per-call file-identity check
	// is measured beside the opens and statements it trades against.
	countFs := countingFs{Fs: afero.NewOsFs()}
	archive.SetFs(countFs)
	favorite.SetFs(countFs)
	pins.SetFs(countFs)
	seen.fs = countFs
	indexStore.SetFs(countFs)

	return navigationBuildStores{
		archive:    archive,
		favorite:   favorite,
		pins:       pins,
		seen:       seen,
		indexStore: indexStore,
	}
}

// build reads the stores once, in the order webNavigationSource.Capture reads
// them.
func (b navigationBuildStores) build(t *testing.T) {
	t.Helper()
	if _, err := b.archive.Decisions(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.favorite.Favorites(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pins.Assignments(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.pins.Sections(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.seen.Snapshot(); err != nil {
		t.Fatal(err)
	}
}

// search runs the past index's FTS read path once, the way Search does.
func (b navigationBuildStores) search(t *testing.T) {
	t.Helper()
	if _, ok := b.indexStore.searchFTS("counted"); !ok {
		t.Fatal("searchFTS did not serve the indexed session")
	}
}

// TestStoreReadsOpenOneHandlePerProcess pins the cost of a navigation build:
// each index.db store opens its connection, runs its DDL and checks the schema
// once per process, not once per call. The first build pays one connection per
// store; every later build pays none and runs only the reads it wanted.
//
// The counting driver makes the claim a measurement: opens, execs and queries
// are the driver-level calls the stores actually made, not what the code
// appears to do.
func TestStoreReadsOpenOneHandlePerProcess(t *testing.T) {
	stores := newNavigationBuildStores(t)

	countReset()
	stores.build(t)
	first := countRead()
	t.Logf("navigation build 1: opens=%d execs=%d queries=%d statements=%d stats=%d",
		first.opens, first.execs, first.queries, first.statements(), first.stats)

	countReset()
	stores.build(t)
	second := countRead()
	t.Logf("navigation build 2: opens=%d execs=%d queries=%d statements=%d stats=%d",
		second.opens, second.execs, second.queries, second.statements(), second.stats)

	// One handle per store: archive, favorite, pin sections (its two reads
	// share the one handle) and seen markers.
	const storesInABuild = 4
	if first.opens != storesInABuild {
		t.Fatalf("first navigation build opened %d connections, want %d, one per store: the first build must still pay the connect",
			first.opens, storesInABuild)
	}
	if second.opens != 0 {
		t.Fatalf("second navigation build opened %d connections, want 0: a build must reuse each store's handle", second.opens)
	}
	if second.execs != 0 {
		t.Fatalf("second navigation build ran %d execs, want 0: DDL and pragma setup must be paid once per process", second.execs)
	}
	// The reads the build wanted: favorite, both pin reads and the seen
	// store's epoch plus records read. The archive read is a cache hit.
	const wantedQueries = 5
	if second.queries != wantedQueries {
		t.Fatalf("second navigation build ran %d queries, want %d: only the wanted reads may remain", second.queries, wantedQueries)
	}
	// The keeper stat()s each store's file once per call to notice a replaced
	// index.db. That is the whole cost the shared handle adds, so it is pinned:
	// one per store read (archive's read is a cache hit and stats nothing).
	const keeperStats = 5
	if second.stats > keeperStats {
		t.Fatalf("second navigation build made %d stat calls, want at most %d: the file-identity check must stay one stat per read",
			second.stats, keeperStats)
	}
}

// TestPastIndexFTSReadsOpenOneHandlePerProcess pins the same cost for the past
// index's FTS read path: a search opens its connection on the first search and
// reuses it afterwards. The FTS write path is deliberately not part of this
// contract — each publish opens its own IMMEDIATE connection so a concurrent
// sibling-store commit cannot fail its write upgrade (past.go's writeFTSTx).
func TestPastIndexFTSReadsOpenOneHandlePerProcess(t *testing.T) {
	stores := newNavigationBuildStores(t)

	countReset()
	stores.search(t)
	first := countRead()
	t.Logf("fts search 1: opens=%d execs=%d queries=%d", first.opens, first.execs, first.queries)

	countReset()
	stores.search(t)
	second := countRead()
	t.Logf("fts search 2: opens=%d execs=%d queries=%d", second.opens, second.execs, second.queries)

	if first.opens != 1 {
		t.Fatalf("first FTS search opened %d connections, want 1", first.opens)
	}
	if second.opens != 0 {
		t.Fatalf("second FTS search opened %d connections, want 0: a search must reuse the index's read handle", second.opens)
	}
	if second.statements() != 1 {
		t.Fatalf("second FTS search ran %d statements, want 1 (the match query)", second.statements())
	}
}

// TestPastIndexFTSPublishKeepsItsOwnConnection pins what this change
// deliberately leaves alone: each FTS publish still opens its own connection,
// so it can take SQLite's write lock with the IMMEDIATE DSN. Sharing a handle
// with the read path would lose that upgrade and turn a concurrent sibling
// store's commit into SQLITE_BUSY_SNAPSHOT (past.go's writeFTSTx).
func TestPastIndexFTSPublishKeepsItsOwnConnection(t *testing.T) {
	stores := newNavigationBuildStores(t)

	countReset()
	stores.indexStore.UpdateMeta("02wMz5Txv1C3Hut0M8GCeB", schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		Name:           "renamed",
		UpdatedAt:      time.Unix(1_700_000_100, 0),
		OriginalPrompt: "counted needle",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/w"},
	})
	publish := countRead()
	t.Logf("fts publish: opens=%d execs=%d queries=%d", publish.opens, publish.execs, publish.queries)

	if publish.opens != 1 {
		t.Fatalf("an FTS publish opened %d connections, want 1: the write path keeps its own IMMEDIATE connection", publish.opens)
	}
}

// TestStoreHandlesSeeAnotherStoresCommit pins that a keeper handle reads the
// current table: a store that already read must see a write another store made
// to the same file. FavoriteStore has no read cache, so every call reads.
func TestStoreHandlesSeeAnotherStoresCommit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	now := time.Unix(1_700_000_000, 0)

	writer := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if err := writer.Set("", "session", "shared", true, now); err != nil {
		t.Fatal(err)
	}
	reader := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if got, err := reader.Favorites(); err != nil || !got[ArchiveKey{Kind: "session", ID: "shared"}] {
		t.Fatalf("reader before write = %v, %v, want the writer's row", got, err)
	}
	// A second writer's row must show through the reader's keeper handle.
	other := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if err := other.Set("", "session", "later", true, now); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !got[ArchiveKey{Kind: "session", ID: "later"}] {
		t.Fatalf("reader after another store's write = %v, want the new row: a keeper handle must see another connection's commit", got)
	}
}

// TestStoreHandlesUseOneConnectionUnderConcurrentReads pins that the handle is
// created once even when the first reads race: it is created under the store's
// mutex, and every reader shares it.
func TestStoreHandlesUseOneConnectionUnderConcurrentReads(t *testing.T) {
	initCountingDriver()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	now := time.Unix(1_700_000_000, 0)
	seed := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if err := seed.Set("", "session", "s", true, now); err != nil {
		t.Fatal(err)
	}

	favorite := closeOnCleanup(t, NewFavoriteStore(dbPath))
	favorite.openDB = countingOpen

	countReset()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := favorite.Favorites(); err != nil {
				t.Errorf("Favorites: %v", err)
			}
		})
	}
	wg.Wait()
	if got := countRead().opens; got != 1 {
		t.Fatalf("8 concurrent reads opened %d connections, want 1: the handle must be created once", got)
	}
}
