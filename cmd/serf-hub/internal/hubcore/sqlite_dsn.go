package hubcore

import "strings"

// sqliteURIPathEscaper escapes the characters SQLite's URI parser would
// otherwise treat as query/fragment delimiters (or as the start of a %XX
// escape). SQLite percent-decodes the path portion of a file: URI, so this
// round-trips exactly; without it a path containing "#" or "?" — which Go's
// t.TempDir() produces for fuzz seeds — silently opens a truncated path.
var sqliteURIPathEscaper = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// sqliteDSN wraps a database path in the DSN every store sharing index.db must
// open it with. Four stores (archive, favorite, pin sections, past-index FTS)
// plus the periodic FTS rebuild all write to the same file from short-lived
// connections, so a writer must wait out a concurrent writer's lock instead of
// failing instantly with SQLITE_BUSY, and WAL journaling keeps readers from
// blocking the writer. WAL mode is persistent in the database file; the
// busy_timeout pragma applies per connection, which is why it lives here in
// the DSN rather than in a one-time migration.
func sqliteDSN(dbPath string) string {
	return "file:" + sqliteURIPathEscaper.Replace(dbPath) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}
