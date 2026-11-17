package hubcore

import "strings"

// sqliteURIPathEscaper escapes the characters SQLite's URI parser would
// otherwise treat as query/fragment delimiters (or as the start of a %XX
// escape). SQLite percent-decodes the path portion of a file: URI, so this
// round-trips exactly; without it a path containing "#" or "?" — which Go's
// t.TempDir() produces for fuzz seeds — silently opens a truncated path.
var sqliteURIPathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// sqliteDSN wraps a database path in the DSN every store sharing index.db must
// open it with. The archive, favorite, pin-section, session-seen and hub-
// identity stores, past-index FTS and its periodic rebuild, and the rebuildable
// message-search index all open their own short-lived connections, so a writer
// must wait out a concurrent writer's lock instead of failing instantly with
// SQLITE_BUSY, and WAL journaling keeps readers from blocking the writer. WAL
// mode is persistent in the database file; the busy_timeout and synchronous
// pragmas apply per connection, which is why they live here in the DSN rather
// than in a one-time migration.
//
// synchronous=NORMAL is what keeps a store write from fsyncing the write-ahead
// log: SQLite's default is FULL, and entering WAL does not change it. Under WAL,
// NORMAL still fsyncs at checkpoints and cannot corrupt the database, but a
// power cut or OS crash may lose the last few seconds of commits. That is the
// deliberate trade for this database — it holds navigation state (archives,
// favorites, pins, seen markers), not anything a user was told was durably
// acknowledged. An application crash loses nothing: bytes already handed to the
// kernel survive the process dying.
func sqliteDSN(dbPath string) string {
	return sqliteDSNWithSynchronous(dbPath, "NORMAL")
}

// sqliteDSNWithSynchronous builds the shared DSN at the given synchronous
// level. Callers that must keep FULL (durable custody that has to survive a
// power cut) pass it here rather than appending a second synchronous pragma to
// sqliteDSN: the driver reorders _pragma entries itself — busy_timeout first,
// then lexicographically — so two synchronous entries resolve to whichever
// sorts last ("full" before "normal"), not to whichever the DSN lists last.
func sqliteDSNWithSynchronous(dbPath, level string) string {
	return "file:" + sqliteURIPathEscaper.Replace(dbPath) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(" + level + ")"
}

// sqliteDSNImmediate is sqliteDSN with an IMMEDIATE transaction lock. The FTS
// writer reads (row count, baseline token) before its first write, while
// archive/favorite/pin writers share index.db; under the default deferred begin
// a concurrent commit in that window invalidates the read snapshot and the
// write upgrade fails with SQLITE_BUSY_SNAPSHOT, which busy_timeout does not
// cover. Taking the write lock up front avoids it.
func sqliteDSNImmediate(dbPath string) string {
	return sqliteDSN(dbPath) + "&_txlock=immediate"
}

// sqliteDSNFullImmediate is sqliteDSNImmediate at synchronous=FULL. The voice
// journal persists custody of spoken input, so its commits keep the per-commit
// fsync the index.db stores gave up.
func sqliteDSNFullImmediate(dbPath string) string {
	return sqliteDSNWithSynchronous(dbPath, "FULL") + "&_txlock=immediate"
}
