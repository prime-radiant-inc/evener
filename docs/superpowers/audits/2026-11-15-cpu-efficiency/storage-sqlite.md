# CPU efficiency audit: durable storage / SQLite

Audited tree: `cpu-efficiency-audit`, branch off `origin/main` at
`377ac0867f580bc65981dee425bf83b3f89af72e` (2026-11-14). Go 1.27.4,
darwin/arm64.

## Verdict

SQLite is **not** on the agent's per-round path: `modernc.org/sqlite` is imported
by only five files, all in the hub (`cmd/evener-hub/internal/hubcore/`) and one
doctor CLI, so none of the 150 fsyncs the coordinator measured in a 200-round
turn run come from SQLite. Inside the hub it is misused: every store call opens
a brand-new `*sql.DB` and closes it, so one navigation build, which the roster
status hook fires at least once and up to twice per model round, pays 4–5 fresh
SQLite connections and 24–27 statements to read five small tables (measured:
5 opens, 27 statements). Every commit
also runs `synchronous=FULL` (the modernc default) even though the DSN opts into
WAL, so each write pays a WAL fsync it does not need. Checked clean:
`MessageSearch.apply` batches correctly in one transaction, no `SELECT *`, no
`json_extract`/`json_each` in any `WHERE` clause, and the operation store's read
path is in-memory.

## Findings, ranked

### 1. HIGH: every hub SQLite store call opens and closes a whole database connection

**Citations.** `cmd/evener-hub/internal/hubcore/archive.go:124-141`
(`ArchiveStore.open`), `favorite.go:51-68` (`FavoriteStore.open`),
`pin_section.go:147-204` (`PinSectionStore.open` →
`openWithImmediateTransaction`), `session_seen.go:107-126`
(`SessionSeenStore.open`), `past.go:982-1042` (`PastIndex.writeFTSTx`),
`past.go:1091` and `past.go:1224` (both FTS search paths). Each is
`sql.Open` + DDL + `ensureIndexSchema` + one query + `defer db.Close()`.

**Call path (per model round, in the hub process).**
`Roster.Watch` (`roster.go:1238`) → `Roster.Refresh` → `listingPublication.fire`
(`roster.go:702-706`) fires `onStatusChange` for a status change *or* a
`LastTurnEndedAt` advance, which is every turn end → registered at
`hub_startup.go:381` → `refreshPastOnStatus` (`main_background.go:40-42`) →
`PastIndex.RefreshOne` (`past.go:580`) → `UpdateMeta` → `publishAndSignal`
(`past.go:542`) → `publishFTS` (`past.go:808`) → `writeFTSTx` →
`i.openDB("sqlite", …)` at `past.go:993`.
The same `Refresh` also fires `onChange` (`hub_startup.go:762-764`) whenever the
live set's membership or per-session status changes →
`web.navigation.Invalidate` → a navigation build →
`webNavigationSource.Capture` (`cmd/evener-hub/navigation_service.go:1818`) which
reads **five** stores: `archiveDecisions` (`web_api_tree.go:98`),
`favoriteDecisions` (`web_api_tree.go:1101`), `pinSectionAssignments`,
`pinSections` (`web_api_tree.go:113-125`), and
`SessionSeen.Snapshot` (`navigation_service.go:1851`).
A user action takes the same path synchronously: `app_session_seen.go:63`
`MarkBatch` then `commitNavigationChange` → `navigation.Refresh`
(`app_pin_section.go:109-128`), i.e. one write plus a full five-store read set,
before the AppWire response returns.

**Mechanism.** `sql.Open` is lazy but the first `Exec` opens a real SQLite
connection: a new file handle, WAL index attach, schema read, then the two DSN
pragmas (`busy_timeout(5000)`, `journal_mode(WAL)`), then the store's own
statements. Counted from the code, one navigation build issues:

| store | statements per `open()` | plus query |
|---|---|---|
| `ArchiveStore` | `CREATE TABLE IF NOT EXISTS` + `PRAGMA user_version` | 1 `SELECT` |
| `FavoriteStore` | same 2 | 1 `SELECT` |
| `PinSectionStore` | `PRAGMA foreign_keys=ON`, `PRAGMA foreign_keys` (read-back), `PRAGMA busy_timeout`, 3× `CREATE TABLE IF NOT EXISTS`, `PRAGMA user_version` = **7** | 1 `SELECT` |
| `SessionSeenStore` | 2× `CREATE TABLE IF NOT EXISTS` + `PRAGMA user_version` = 3 | 2 `SELECT`s (records, plus the epoch read `session_seen.go:165-179` every snapshot) |

That is **5 connections and 27 statements** per build on a cold archive cache
(`archive.go:179-197` is the only store with a read cache, so a warm build is 4
connections and 24 statements). Only **6** of those 27 statements are the
queries the build wanted: `archive`, `favorite`, `session_pin` and
`session_seen`, plus the seen epoch read and the pin section count. The other
**21** are schema and pragma work repeated on every build. No prepared statement
survives an `Exec`/`Query`: `database/sql` prepares and finalizes around every
parameterised call, and modernc keeps no per-connection statement cache
(`modernc.org/sqlite@v1.50.1` exposes `Driver.Open` → `newConn` at
`driver.go:90`; each `Stmt` is finalized when it closes).

**Evidence: executed.** A counting `database/sql` driver wrapped around
modernc's own driver (a temporary `zz_audit_count_test.go`, since deleted)
recorded the real store calls through their `openDB` seam:

```
archive Set:                              opens=1 execs=5 queries=6
favorite Set:                             opens=1 execs=2 queries=1
favorite Favorites (no cache):            opens=1 execs=1 queries=2
pin Sections:                             opens=1 execs=5 queries=3
pin Assignments:                          opens=1 execs=5 queries=3
sessionSeen Snapshot (second):            opens=1 execs=2 queries=3
navigation build store reads (one pass):  opens=5 execs=14 queries=13
```

`opens=5 execs=14 queries=13` is 27 driver-level calls (14 `Exec`, 13 `Query`)
for one navigation build's five store reads, with 5 connection opens, the table
above predicted 27 and it measured 27. Two details the measurement adds to the
count: both `archive
Decisions` probes show `opens=0`, the first because `readDecisions`'s
`fs.Stat` guard (`archive.go:204`) returns an empty map before opening when the
file does not exist yet, the second because it is a cache hit (`archive.go:181`),
so the archive read is free only *after* something has opened the file. And
`pin CreateOrReuseAndAssign` costs `opens=1 execs=7 queries=4 begins=1 commits=1`: one connection, one transaction, one fsync. `archive Set` reading 11 calls
rather than 3 is the first-touch schema migration: on a file with
`user_version = 0`, `ensureIndexSchema` (`index_schema.go:45-93`) takes
`BEGIN IMMEDIATE`, re-reads the version, then `rebuildLegacyIndexTables` probes
`sqlite_master` and the column shape of `favorite`, `archive` and `session_pin`
before stamping the version and committing. That cost is once per new database.
(`prepares=0` throughout because
`database/sql` takes the `ExecerContext`/`QueryerContext` fast path, so the
driver's `Prepare` is never called; the prepare still happens inside modernc,
which is the point.)

Executed open-cost proxy, same DSN string and same statement sequence, run under
python3's sqlite3 on this host:

```
open+DDL+PRAGMA+SELECT+close per read: 139 us
reused handle SELECT only per read:    18 us
delta per read:                        121 us  (7.9x)
```

(The probe script printed "6 opens per navigation build: 0.73 ms" because it
counted six; the real count is 5, so 5 × 121 µs ≈ 0.6 ms of pure connection cost
per build. That is the **C** SQLite cost; modernc's pure-Go path is slower.)

The proxy is C SQLite, so it *understates* the modernc cost: the pure-Go VFS and
cgo-free SQLite add parsing and allocation the C library amortises better. A Go
benchmark of the real shape against a reused handle
(`BenchmarkStoreReadOpenPerCall` vs `BenchmarkStoreReadReusedHandle`) was
written and launched, but no `ns/op` came back: Go execs on this Mac cost 9–19 s
each, so a `-bench` run's compile/link children never finish on the host (the
`hubcore` link was still running after 10 minutes, three attempts). Run the same
bench inside `golang:1.27` with the module cache mounted to get the real delta.
The 121 µs delta here is the C-SQLite floor.

**Scale.** A model round produces two status transitions in the hub (running,
then idle), and each transition fires `onStatusChange` and can fire `onChange`;
`NavigationService.Invalidate` (`navigation_service.go:295-317`) merges hints
into one `pendingInvalidation` and wakes the scheduler
(`navigation_service.go:942, 962, 1028`), so a burst collapses to one build.
The count I can defend from the code is therefore **≥1 and up to 2 builds per
round**, each 4–5 connections and 24–27 statements, plus the FTS write
connection per status transition; each user mutation (seen mark, archive toggle,
pin, rename, delete) adds one more build before its response returns. Cost is
constant per build, not linear in sessions, but it is
paid on the hub's hot path, once per build, and it grows with the number of hub
clients: ten sessions each taking a 10-second round means ten sessions × two
status transitions × 4–5 connections, i.e. ~90 connection opens per ten seconds,
to re-read five decision tables that changed at most once.

**Fix design.** Give each store one long-lived `*sql.DB` instead of an `open()`
per call. `ArchiveStore`, `FavoriteStore`, `PinSectionStore`,
`SessionSeenStore` already hold `openDB func(string, string) (*sql.DB, error)`
as a test seam; add a `db *sql.DB` field plus `sync.Once`, and change `open()`
to return the shared handle (creating it on first use, keeping the
`MkdirAll` + DDL + `ensureIndexSchema` in the create step only). Better still,
one handle per **index.db** shared by all five stores: they already open the
same file (`sqliteDSN`'s doc comment at `sqlite_dsn.go:20-24` says so
explicitly), so a per-path handle cache keyed on `dbPath` removes four of the
five connections. Set `db.SetMaxOpenConns(1)`; `PinSectionStore` already does
this at `pin_section.go:172-173` and treats it as correct.
Prepared statements then become worth keeping: prepare the five read queries
once per handle and reuse them.
*What could break:* lifetime. A hub-shared handle must be closed on shutdown
(add `func (s *XStore) Close() error`, called from the same place the stores are
torn down), and tests that delete the temp dir under a live handle need either a
`t.Cleanup(store.Close)` or the current `TemporaryDB`-style seam. `PinSectionStore`
already varies its DSN per attempt (`sqliteDSNImmediate` on retry,
`pin_section.go:154-157`), so a shared handle loses that retry upgrade, the
retry must keep opening a fresh handle with the IMMEDIATE DSN, or the handle
cache must carry two entries per path.
*Tests:* `archive_test.go`, `favorite_test.go`, `pin_section_test.go`,
`session_seen_test.go` all construct stores against `t.TempDir()`; a `-race`
run of `hubcore` plus a test that two stores on one path both read after one
writes (the cache-invalidation test, `archive.go:170-176`) is the proof.

**How to verify the win.** `go test ./cmd/evener-hub/internal/hubcore/
-bench 'BenchmarkStoreRead' -benchtime 200x -run '^$'` with two shapes:
`BenchmarkStoreReadOpenPerCall` (today: five store reads, each through its own
`open()`+`Close`) against `BenchmarkStoreReadReusedHandle` (the fix: the same
five queries on one handle). Expect the `open`-per-call shape to report ~8× the
ns/op and several extra allocs/op; the allocs delta is the connection object
plus its pragma statements. Both were written for this lane and deleted after
the audit; recreate them from the table above.

### 2. HIGH: WAL is enabled but `synchronous` stays at FULL, so every commit fsyncs the WAL

**Citations.** `cmd/evener-hub/internal/hubcore/sqlite_dsn.go:25`, the DSN is
`file:<path>?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)`. Nothing sets
`synchronous`. `modernc.org/sqlite@v1.50.1/lib/sqlite_darwin_arm64.go:2693`
(and `sqlite_darwin_amd64.go:2698`, `sqlite_linux_amd64.go:1805`) compile in
`SQLITE_DEFAULT_SYNCHRONOUS = 2`, i.e. `FULL`. The only repo code that sets it
is `voice_journal.go:329`, and it sets `synchronous(FULL)` explicitly.

**Call path.** Every hub write: `app_session_seen.go:63` → `MarkBatch`
(`session_seen.go:217-253`, one `BeginTx` + one `Commit`) →
`commitNavigationChange` → `navigation.Refresh` →
`PastIndex.writeFTSTx` (`past.go:982`, one `Begin`/`Commit`) → the archive,
favorite and pin stores' single-statement writes (`archive.go:158`,
`favorite.go:76`, `pin_section.go` write attempts). Each commit at `FULL` in WAL
mode fsyncs the WAL file.

**Mechanism.** In WAL mode SQLite still honours `synchronous`: `FULL` fsyncs the
WAL on every commit, `NORMAL` fsyncs it only at checkpoint. `NORMAL` in WAL is
the documented durable choice, a crash can lose the tail of the WAL, never
corrupt the database, and it is what every other WAL SQLite deployment uses.
With the store shape unchanged this converts one fsync per hub write into one
per checkpoint (default 1000 pages).

**Evidence: read + executed.** Executed pragma probe on this host (python3
`sqlite3`, temp file, `PRAGMA` reads):

```
default journal_mode = delete
default synchronous  = 2
after WAL, journal   = wal
after WAL, synchronous = 2
```

So opting into WAL does not change `synchronous`; the DSN must say so.

**Scale.** One fsync per write transaction. A seen-mark set is 2 (`MarkBatch`
commit + the FTS commit chained onto it by `commitNavigationChange`); an archive
toggle is 2 as well. At the coordinator's measured 459 µs per fsync on this
host, one archive toggle costs ~0.9 ms of barrier and one turn end's FTS commit
costs ~0.46 ms more than it needs to. Frequency is user-driven plus
per-status-change for the FTS write, so this is a small absolute number, but it
is the cheapest barrier in the entire codebase to remove: one DSN string, no
code and no test changes.

**Fix design.** Append `&_pragma=synchronous(NORMAL)` in `sqliteDSN`
(`sqlite_dsn.go:25`); `sqliteDSNImmediate` inherits it via its prefix. Nothing
else changes. `_pragma=synchronous(NORMAL)` applies per connection, exactly like
`busy_timeout`, so it needs no migration and no restart-time ordering.
*What could break:* the durability contract weakens from "the commit is on
stable storage" to "the commit is in the WAL, which is on stable storage at the
next checkpoint". A crash between the commit and the checkpoint loses recent hub
decision rows (seen marks, archive/favorite flags, pins), never corrupts.
Sessions' own state is still in `agent/internal/jobstore`, which fsyncs
independently. If a seen marker must survive a power cut the instant it is
written, this is not for that table; `voice_journal.go:329` shows the pattern
for opting one store back into `FULL`.
*Tests:* the durability tests in `hubcore` (`session_seen_test.go`,
`archive_test.go`) do not assert on `synchronous`; add one that opens with
`sqliteDSN`, reads `PRAGMA synchronous`, and asserts `1`, so a future DSN edit
cannot silently re-cost every hub commit.

**How to verify the win.** `strace -f -c -e trace=fsync,fdatasync` around a hub
that serves one seen-mark request and one archive toggle, before and after: the
`fsync` count should drop from 2 per mutation to 0 (the WAL checkpoint syncs
later, once, in the background). If `strace` is unavailable, the go test above
plus `PRAGMA wal_checkpoint` counters cover it.

### 3. HIGH: a no-change status refresh rewrites the FTS mirror row: connection, 2 DDL, 2 reads, 2 writes, commit, 4 chmods

**Citations.** `past.go:484-513` `UpdateMeta` → `past.go:542-560`
`publishAndSignal` → `past.go:808-848` `publishFTS` → `past.go:875-980`
`rewriteFTSDelta` → `past.go:982-1042` `writeFTSTx` →
`past.go:1071-1078` `chmodSQLiteIndexFilesFS`.

**Call path.** `Roster.Refresh` → `listingPublication.fire` →
`onStatusChange(id)` (`roster.go:702-706`; the hook fires when
`prev.Status != cur.Status || cur.LastTurnEndedAt.After(prev.LastTurnEndedAt)`,
`roster.go:691-692`) → `refreshPastOnStatus` (`main_background.go:40-42`) →
`PastIndex.RefreshOne` (`past.go:580-591`) → `UpdateMeta` → `publishAndSignal`
→ `publishFTS` → `rewriteFTSDelta`. `Roster.Watch` runs `Refresh` on every
fsnotify event **and** on a 5-second ticker (`roster.go:1238-1277`), so the
firing opportunity is once per status transition and once per tick that sees one.

**Mechanism.** `UpdateMeta` publishes whenever the incoming meta is not
*older* than the indexed one: `metaNewer(old.Meta, meta)` (`past.go:492`) returns
false on an undecidable tie (`past.go:1731-1736`), so a meta whose revision and
timestamps are unchanged still reaches `publishAndSignal`. `publishFTS` then
clears the mirror-healthy flag (`i.fts = false`, `past.go:823`) and calls
`rewriteFTSDelta` while `i.published` is set, so the write path always runs.
`rewriteFTSDelta` opens a new connection (see finding 1), runs `CREATE TABLE IF
NOT EXISTS` twice,
`BEGIN`s, executes `SELECT COUNT(*)`, `SELECT owner, seq`, then, because
`len(removals)==0 && len(inserts)==0` returns early, `writeFTSTx` still
`DELETE`s and re-`INSERT`s the single `past_sessions_fts_state` row and commits,
closing and then `chmod`ing four paths (`db`, `-journal`, `-wal`, `-shm`).
A refresh in which *nothing about the session changed* therefore costs a
connection, 2 DDL, 2 selects, 2 writes, a commit and 4 `chmod` syscalls. Note
that a status change (turn start or end) does not alter any of the five columns
`ftsRowEqual` (`past.go:851-859`) compares, so the common case on a live hub is
exactly this no-change write.

**Evidence: read.** Traced through the five functions above; the Go benchmark
written to measure it (`BenchmarkPublishFTSNoChange`) never ran on this host.

**Scale.** Once per session status change: two per model round (running → idle),
plus one per session that advances `LastTurnEndedAt` on any 5-second tick. On a
hub with ten active sessions that is ~20 FTS write transactions per round of
concurrent work, each one a connection, a commit and four `chmod`s, for a
`DELETE`+`INSERT` of one row.

**Fix design.** Two changes, both small and local:
1. In `UpdateMeta` (`past.go:484`), compute `ftsRowEqual` for the changed entry
   against the previous one and skip publishing entirely when nothing the mirror
   stores changed, the same test `rewriteFTSDelta` already applies per row.
   This is the "edit did not change indexed content" case the function's own doc
   comment describes for `onChange`, just applied one layer earlier.
2. In `writeFTSTx` (`past.go:982`), hoist the two `CREATE TABLE IF NOT EXISTS`
   statements and the `chmod` out of the per-write path: run them once per
   `PastIndex` instance (a `schemaOnce sync.Once` and a `chmodOnce`), since the
   index owns one `dbPath` for its whole life. The `chmod` can also be dropped
   entirely when the WAL/SHM files already carry the right mode, or reduced to
   one `chmod` on the db file, which is what `openOutputFs`-style stores do.
*What could break:* skipping the publish on a no-change edit means `i.fts` stays
true, so `ensureFTSFresh` (`past.go:702`) will not fire a repair, correct, since
the mirror does match. If an entry's *rank* changed but its content did not,
`ftsRowEqual` already says "equal", and `rewriteFTSDelta`'s doc comment
(`past.go:866-871`) explains why that is fine (Search re-sorts). Hoisting the
DDL means a database replaced or restored underneath the index no longer gets
its tables re-created on the next write; `rewriteFTSDelta` already detects
exactly that with its owner/seq token and falls back to `rebuildFTS`, so the
recovery path is unchanged, but a test that deletes `index.db` mid-life must
still pass, and `past_fts_test.go` should say so.
*Tests:* `TestPastIndexUpdateMeta*`, `past_fts_test.go`'s baseline-stale tests,
and `past_test.go`'s mirror-repair cases.

**How to verify the win.** `go test ./cmd/evener-hub/internal/hubcore/ -bench
BenchmarkPublishFTSNoChange -benchtime 200x -run '^$'` (written for this audit,
deleted with the rest) with a `-count 1 -benchtime` run before and after: the
ns/op should fall by the connection-open plus DDL cost, and `strace -c -e
trace=chmod,fsync` around one refresh should show the four `chmod`s and the
commit disappear.

### 4. MEDIUM: the operation store rewrites, re-validates and double-fsyncs the whole `operations.json` per mutation

**Citations.** `cmd/evener-hub/internal/hostops/store.go:34`
(`storeFilename = "operations.json"`), `:641-653` `commitLocked` →
`:1055-1180` `saveFS` (validate → marshal → byte checks → temp write →
`temp.Sync()` at `:1143` → rename → `directory.Sync()` at `:1271`),
`:1372-1380` `checkStoreBytes` (`utf8.Valid` + lone-surrogate scan +
`validateStoreKeys` token walk), `:1826-1846` `cloneSnapshot`. Retention bounds
at `retention.go:40-50`: 50 terminal ops per host, 500 store-wide, 64 MiB.

**Call path.** Host lifecycle only, `Store.Create` (`store.go:480`) and
`Store.Transition` (`store.go:535`), reached from the hub's host-record boot and
`app_host_manage.go` request handlers. Not on a model round.

**Mechanism.** Each mutation folds a new snapshot, deep-clones it, marshals the
*entire* store, re-validates the entire byte stream three separate ways
(UTF-8 scan, lone-surrogate scan, a full JSON token walk), writes a temp file,
fsyncs it, renames, and fsyncs the directory. Two kernels barriers per
mutation, plus O(store) CPU on the marshal and on each of the three validation
passes. At the 64 MiB bound that is four full passes over 64 MiB per mutation.

**Evidence: read.** No run.

**Scale.** Low frequency (host add/remove, op transitions during a host
operation), O(store bytes) each. Real user state is small, 50 terminal rows per
host, so the typical store is tens of KB, not 64 MiB. Worth fixing for the
bound, not for the common case.

**Fix design.** Two cheap cuts that keep both barriers only where durability
demands it: (a) marshal through `encoding/json`'s `Encoder` into a
`bytes.Buffer` sized from the previous file's length instead of materialising the
document twice (`saveFS` currently marshals to `data`, then `temp.Write(data)`);
(b) run `checkStoreBytes` on the marshalled bytes only when the store was
*replaced* rather than written, it exists to validate a file this build did not
write, which `Open` can detect. Keeping both fsyncs is correct; the CPU is the
marshal and the three scans, not the barriers.
*What could break:* `checkStoreBytes` is the only guard against a
restored/foreign `operations.json` carrying a lone surrogate that would round-trip
badly; `Open` must keep it. `validateSnapshot` (`store.go`) must stay on every
write. A test that writes a store with a `\ud800` escape and expects the write to
fail, and one that opens such a file and expects the failure at open, cover both.
*Tests:* `statedir_test.go`, `store_test.go`, `blocker`-shaped tests in
`hostops`.

**How to verify the win.** `go test ./cmd/evener-hub/internal/hostops/ -bench
'BenchmarkStore' -benchmem` before/after, plus `strace -c -e trace=fsync` over
one `Create`: barriers must stay at 2, bytes-marshalled must halve.

### 5. MEDIUM: each `PinSectionStore` open re-verifies `PRAGMA foreign_keys` with a round trip, seven statements before the first read

**Citations.** `pin_section.go:147-204`, specifically the
`PRAGMA foreign_keys = ON` → `PRAGMA foreign_keys` read-back at `:175-186`, the
duplicate `PRAGMA busy_timeout = 5000` at `:187-189` (already in `sqliteDSN`),
and the three `CREATE TABLE IF NOT EXISTS` at `:191-197`.

**Call path.** `webNavigationSource.Capture` → `WebServer.pinSectionAssignments`
and `WebServer.pinSections` (`web_api_tree.go:113-125`), two separate opens per
navigation build, seven statements each, plus one query.

**Mechanism.** The read-back exists so a silently-failing pragma cannot leave
foreign keys off. That is a good guard to run **once per process**, and a waste
to run per call. The `busy_timeout` is set twice (DSN and explicit), which at
best is a redundant `sqlite3_exec`.

**Evidence: read** (statement count), the same open shape measured in finding 1
covers the cost.

**Scale.** Two of the four connections in every navigation build, 14 of the ~21
statements. Removing the pragma work would make the pin reads cost less than an
archive read despite touching two tables.

**Fix design.** Fold into finding 1's shared handle: run the pragma verification
in the constructor/create step, not in `open()`, and delete the explicit
`PRAGMA busy_timeout` since `sqliteDSN` already carries it. If the handle stays
per-call for now, at least drop the redundant `busy_timeout`.
*What could break:* the read-back is a real correctness guard, keep it, in the
create path. A build that links a modernc version with a different pragma
default would then fail at first open rather than per call, which is still the
right failure.
*Tests:* `pin_section_test.go`'s foreign-key enforcement tests are the proof
that the guard still runs.

**How to verify the win.** The finding-1 benchmark, run with the pin store
included: statements per build drop from ~21 to ~7.

### 6. MEDIUM: the message-search refresher full-scans its index and `os.Stat`s every session every 60 seconds

**Citations.** `message_search.go:255-364` `Refresh` →
`:367-383` `indexedTranscripts` (`SELECT session_id, size, mod_time_ns,
incarnation, length FROM transcripts`, full scan, no `WHERE`) → per session
`os.Stat(session.TranscriptPath)` at `:268` → on a changed stamp
`x.read` + `x.apply` (`:539-592`, correctly one transaction with prepared
`drop`/`insert` and one `transcripts` upsert).
Wired at `hub_startup.go:866` with `cfg.PastIndexRebuild`, default 60 s
(`config.go:191`); the session list is rebuilt per tick from
`messageSearchSessions(past.All())` (`main_background.go:128`).

**Mechanism.** Every tick: one full `transcripts` scan (O(indexed sessions)), one
`past.All()` copy (O(sessions), allocates), one `os.Stat` per session, and one
map allocation. Only sessions whose size/mtime changed do any work. The scan is
the waste: the refresher needs the stamps, not the rows, but it materialises the
whole map each time, and `past.All()` takes the index read lock and copies the
slice for a list that changes slowly.

**Evidence: read.**

**Scale.** Once per minute per hub, O(sessions) with a `stat` each. On a hub with
200 indexed sessions that is 200 syscalls and 200 map inserts per minute; small
but entirely avoidable, and it also copies `past.All()` per tick.

**Fix design.** Keep the `transcripts` table's stamp columns in a small
in-memory map owned by `MessageSearch`, updated by `apply`/`Forget` (both already
run under the index's own serialisation), and fall back to
`indexedTranscripts` only on the first tick after construction. Replace
`messageSearchSessions(past.All())` with an iteration over the index that yields
path + id without copying the slice, `PastIndex.All` already has a
`snapshot()` helper (`past.go:600-609`) to build on.
*What could break:* an out-of-process writer to the index would desync the map;
nothing else writes it. A `stat` on a path that disappeared must keep the
"already indexed, now gone → Forget" branch. The first-tick full read keeps
recovery honest.
*Tests:* `message_search_test.go`'s refresh/forget tests; add one where the
index is written by a second `OpenMessageSearch` and the first tick still
converges.

**How to verify the win.** `go test ./cmd/evener-hub/internal/hubcore/ -bench
'BenchmarkMessageSearchRefresh' -benchmem`, currently absent, so add a bench
that seeds 200 rows and calls `Refresh`: allocs/op should drop from O(sessions)
to O(changed).

### 7. MEDIUM: session meta autosave is a load-modify-increment-rename per call site, once per model round

**Citations.** `agent/schema/snapshot.go:619-623` `saveSessionMetaLocked` →
`:630-636` / `:649-662` cross-process lock → `:667-705`
`writeSessionMetaLocked`: tombstone `Stat`, `loadSessionMetaFS` (full previous
meta read), `ObservedBy` union, `Revision = previous + 1`, marshal, temp write,
rename. No fsync. Called from `agent/session.go` at ~25 `maybeAutoSave` sites,
including once per model round at `agent/session_model_call.go:1834`
(`forceCompactForModelRecovery`) and per committed environment event at
`agent/session.go:2735`.

**Mechanism.** The `Revision` bump forces a read-modify-write: every autosave
re-reads and re-parses the previous `meta.json`, unions `ObservedBy`, and
re-marshals. Two file writes (temp + rename) and one cross-process file lock per
call. No fsync, so this is CPU and syscalls, not barriers.

**Evidence: read.**

**Scale.** Once per model round minimum, up to several per round on a session
that renames, switches model, or publishes environment events. O(meta bytes),
and `meta.json` grows with `ObservedBy` and the environment block.

**Fix design.** Hold the last-written `SessionMeta` in memory alongside the
in-process striped lock, so `writeSessionMetaLocked` can union `ObservedBy`
against the cached value and skip the `loadSessionMetaFS` parse; keep the
cross-process file lock and the on-disk reload only when the in-memory revision
disagrees with the file (`loadSessionMetaFS`'s `Revision` is already the
detector). That turns the common case into one marshal plus one write+rename.
*What could break:* the cross-process lock exists precisely because the daemon
rewrites the same file out of process (`snapshot.go:650-652`); the cache must be
invalidated whenever the on-disk revision differs, and that comparison must stay
*inside* the lock. A test that writes a newer revision out of band and asserts
the next autosave adopts it covers it.
*Tests:* `agent/schema`'s meta tests, `agent`'s session rename/model-switch
tests (`session_test.go`).

**How to verify the win.** `go test ./agent/schema/ -bench 'BenchmarkSessionMeta'
-benchmem` (add one if absent): allocs/op should drop by the parsed previous
meta; `strace -c -e trace=openat,read,rename` over one round should show fewer
`openat`s in the sessions dir.

### 8. MEDIUM: search opens a fresh SQLite connection per query, on a path with no client-side debounce

**Citations.** `cmd/evener-hub/internal/hubcore/past.go:1091` (`searchFTS`) and
`past.go:1224` (`searchFTSAdmitted`'s id filter) both
`i.openDB("sqlite", …)` then `defer db.Close()`. Entry points:
`cmd/evener-hub/app_search.go:141` (`cfg.Past.SearchAdmitted`) and
`app_threadlist.go:237` (`cfg.Past.Search`). `Search` also calls
`ensureFTSFresh` (`past.go:702`) first, which can itself perform a full publish
(and so another connection + commit) whenever the mirror is stale.

**Mechanism.** Each search request: one connection open, `PRAGMA user_version`
is *not* run here (these paths skip `ensureIndexSchema`), one `MATCH` query, one
close. Searches can also issue a second query per 500 ids
(`past.go:1226-1245`). The frontend `cmd/evener-hub/frontend/src/shell/palette/
search.ts` contains no `debounce`/`setTimeout` (grep, exit 1), so the request
rate follows keystrokes.

**Evidence: read** (code + grep).

**Scale.** One connection per keystroke-driven request. At the measured 121 µs
open cost that is ~0.12 ms per keystroke, small in absolute terms, and it does
not compound, which is why this ranks below the build-path findings. It is
listed because the fix is the same handle and it is the one store path a *user*
feels.

**Fix design.** Fold into finding 1's shared handle: `PastIndex` already holds
`openDB` and a single `dbPath`; give it the same long-lived handle and prepare
the two `MATCH` statements once.
*What could break:* `ensureFTSFresh` may write from the search path, so the shared
handle needs `SetMaxOpenConns(1)` to avoid a read/write pile-up, and a stale
handle must still notice a deleted `index.db` (the `errFTSBaselineStale`
fallback at `past.go:836` already handles the logical case; a deleted file
surfaces as a query error, which the current code turns into "no FTS results"
(keep that).
*Tests:* `past_fts_test.go`'s search tests plus the mirror-repair test.

**How to verify the win.** The finding-1 benchmark extended with a search query,
or `strace -c -e trace=openat` around ten search requests: `openat` on
`index.db` and its `-wal`/`-shm` should drop from 10 to 1.

### 9. LOW: `voice_journal` reads promise N+1 inside one transaction

**Citations.** `voice_journal.go:329` opens one shared handle
(`SetMaxOpenConns(1)`, `synchronous(FULL)` explicit, the good shape), but
`ListIntents` / `ListSpeechDeliveries` (`voice_journal.go:363-410`) query the
id list, then call `readVoiceIntent` per id inside the same transaction.

**Mechanism.** One statement per row returned. `SetMaxOpenConns(1)` means the
per-row queries serialise on the one connection, so a list of 50 intents is 51
round trips.

**Evidence: read.**

**Scale.** Per voice-journal list request (user-driven), linear in rows.

**Fix design.** One join (or one `IN (...)` chunked like `ftsDeleteChunk`) in
place of the loop; `readVoiceIntent`'s per-row shape stays for the single-id
callers.

**How to verify the win.** `go test ./cmd/evener-hub/internal/hubcore/ -bench
'BenchmarkVoiceJournalList' -benchmem` (add if absent): statements per list drop
from N+1 to 1 or 2.

## Clean

- **SQLite is off the agent's per-round path.** `modernc.org/sqlite` is imported
  by five files only: `cmd/evener-hub/internal/hubcore/{session_seen,archive,
  favorite,pin_section,past}.go` and `cmd/evener-doctor/reconstruct.go`
  (`grep -rln 'modernc.org/sqlite' --include=*.go`). `hubcore` lives under
  `cmd/evener-hub/internal`, so `cmd/evener` (the binary the turncpu harness
  runs) cannot import it. Every SQLite fsync therefore lives in the hub process,
  not in the measured 7.1-per-round syscall census. Evidence: read (import
  census) + the coordinator's per-file fsync census showing no `index.db` entry.
- **`MessageSearch.apply` is correctly batched** (`message_search.go:539-592`):
  one `BeginTx`, a prepared `DELETE`/`INSERT`, one `transcripts` upsert, one
  commit. This is the shape the other stores should copy. Evidence: read.
- **`MessageSearch.Refresh` only writes for changed sessions.** The stamp check
  at `:284-287` (`held.stamp == stamp → continue`) means 200 unchanged sessions
  cost zero writes. Evidence: read.
- **`MessageSearch.Match` is one FTS join**, no per-row query
  (`message_search.go:624-663`). Evidence: read.
- **No `SELECT *` and no JSON extraction in a `WHERE`** anywhere under `cmd/` or
  `internal/` (`grep -rn 'SELECT \*\|json_extract\|json_each'`). Evidence:
  executed grep.
- **`ArchiveStore.Decisions` is cached** with a write-generation guard
  (`archive.go:170-197`), so repeated reads in one build are free. The other
  four stores should have the same. Evidence: read.
- **`PastIndex.searchFTS` returns ids only**, and the index re-sorts by its
  in-memory order (`past.go:866-871`, `mergeSearchResults`), so a rank shift
  never rewrites a row. Evidence: read.
- **`hostops.Store.Sequence` and `Record`/`Records` are in-memory**
  (`store.go:439-471`), no I/O per read, only a mutex and a `slices.IndexFunc`
  linear scan over a bounded list (≤500 rows). Evidence: read.
- **`hub_identity.go:31` opens without WAL** but runs once per boot, not on a
  request path. Evidence: read.
- **`cmd/evener-doctor/reconstruct.go:232`** opens read-only in a one-shot CLI.
  Evidence: read.

## Open questions

1. **Should hub decision stores share one handle, or hold one each?** One handle
   per `index.db` removes four of five connections per build but couples the
   five stores' lifetimes and defeats `PinSectionStore`'s
   deferred→IMMEDIATE retry DSN upgrade (`pin_section.go:151-157`). One handle
   per store is a smaller change and keeps the retry; it saves four of five
   connection opens but not all. A ruling is needed on whether the retry path may
   open a throwaway handle.
2. **Is losing the last few hub decision rows on a power cut acceptable?**
   Finding 2 (`synchronous=NORMAL`) trades a bounded, recoverable loss of seen
   markers / archive / pin rows for one fewer fsync per write. Seen markers are
   the ones a user would notice; `voice_journal.go:329` already opts one store
   back into `FULL`, so a per-store choice is available.
3. **Is a per-search debounce wanted?** Finding 8 is a client-side one-liner
   (a 100 ms debounce in the palette module), but it changes when results appear
   and is a frontend-lane call, not a storage one.
4. **Where does the operation store's 64 MiB bound actually land in practice?**
   `DefaultStoreMaxBytes = 64 MiB` (`retention.go:44`) against
   `DefaultTerminalStoreWide = 500` rows. If a real `operations.json` is
   kilobytes, finding 4 is a bounds exercise, not a cost; if a hub ever holds
   large host records, it is a per-mutation 64 MiB × 4 passes.

## What could not be measured

- **Go statement counts and allocs/op.** A counting `database/sql` driver and the
  benchmark were written for this lane. The counting driver ran and produced
  the `opens`/`execs`/`queries` table in finding 1 (5 opens, 27 statements per
  navigation build, matching the read count exactly). The `ns/op` benchmark did
  not report, and the cause is the host, not the code: Go execs here cost 9–19 s
  apiece, so a `go test -bench` run's sub-process storm never completes and
  prints zero bytes for tens of minutes (measured by another lane on this audit,
  2026-11-15). The fix is a container,
  `docker run --rm -v /Users/jesse/go/pkg/mod:/gomod -v $PWD:/src -w /src -e
  GOMODCACHE=/gomod -e GOPROXY=off -e GOMAXPROCS=2 golang:1.27 sh -c 'go test
  ./cmd/evener-hub/internal/hubcore -run "^$" -bench BenchmarkStoreRead
  -benchtime 20x -vet=off'`. So finding 1's *statement* counts are executed and
  its *time* figures come from the python3 proxy, which uses C SQLite and
  therefore understates modernc.
- **Whether modernc caches a prepared statement across `Exec` calls.** Read of
  `modernc.org/sqlite@v1.50.1` (`driver.go:90` `Driver.Open`, no statement cache
  field on `conn`) says no. The counter saw `prepares=0` because
  `database/sql` uses the driver's `ExecerContext`/`QueryerContext` fast path, so
  the driver-level `Prepare` hook is never called and the count cannot settle
  it either way; modernc prepares internally per statement.
- **Hub-side fsync counts.** The coordinator's census covers the agent
  (`cmd/evener`). Nothing here measured how many barriers a hub serves per
  second against its own `index.db`; findings 2 and 3 rely on the code's one
  commit per write.
- **Per-keystroke search request rate.** No `node_modules` in this worktree, so
  the frontend was read, not run.
