package hubcore

import (
	"bufio"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/transcriptindex"
)

// messageSearchSchemaVersion is the layout OpenMessageSearch creates, kept in
// PRAGMA user_version. search.db is its own file, so its header is free for
// it, and the index is a cache of the transcripts: a file of any other
// version is dropped and rebuilt rather than migrated. Bump it when what the
// index keeps changes too (searchableMessage), so every transcript is read
// again.
const messageSearchSchemaVersion = 1

// messageSearchSchema is the index's layout. messages holds each message's
// text once, under its session and the transcript key a thread read gives its
// item; messages_fts indexes the text through FTS5's external-content mode,
// kept in step by the two triggers. transcripts records, per session, the
// file state the index last read and the transcript-index snapshot its rows
// describe.
//
// The tokenizer leaves diacritics alone, so a word matches exactly what the
// search's highlighting finds (package hub's snippets), letter case aside.
//
// messages.id is AUTOINCREMENT so a row id is never reused: a MessageHit
// captured by Match can outlive its message (a later Refresh or Forget can
// remove it), and Texts must then find nothing rather than resolve the hit's
// id to an unrelated message that reused it.
var messageSearchSchema = []string{
	`CREATE TABLE transcripts(
	session_id TEXT PRIMARY KEY,
	size INTEGER NOT NULL,
	mod_time_ns INTEGER NOT NULL,
	incarnation TEXT NOT NULL,
	length INTEGER NOT NULL
)`,
	`CREATE TABLE messages(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL,
	transcript_key TEXT NOT NULL,
	entry INTEGER NOT NULL,
	item INTEGER NOT NULL,
	sub INTEGER NOT NULL,
	text TEXT NOT NULL
)`,
	`CREATE UNIQUE INDEX messages_by_key ON messages(session_id, transcript_key)`,
	`CREATE VIRTUAL TABLE messages_fts USING fts5(text, content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 0')`,
	`CREATE TRIGGER messages_indexed AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END`,
	`CREATE TRIGGER messages_unindexed AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.id, old.text);
END`,
}

// messageSearchDrop removes every object messageSearchSchema creates, triggers
// first.
var messageSearchDrop = []string{
	`DROP TRIGGER IF EXISTS messages_indexed`,
	`DROP TRIGGER IF EXISTS messages_unindexed`,
	`DROP TABLE IF EXISTS messages_fts`,
	`DROP TABLE IF EXISTS messages`,
	`DROP TABLE IF EXISTS transcripts`,
}

// minMessageSearchTokenRunes is the shortest word a message search runs for.
// A single letter prefixes most words in every transcript, so it would list
// nearly every session and cost the most to answer; the title search still
// answers it.
const minMessageSearchTokenRunes = 2

// MessageSearch is the hub's message-text search index (S14): the text of
// every user and agent message in the hub's local transcripts, kept under the
// transcript key and position a thread read gives the message's item, so a
// hit opens its session at the message. It reads transcripts through their
// transcript indexes (internal/transcriptindex), the read model thread reads
// use, and lives in its own SQLite file beside index.db.
type MessageSearch struct {
	db *sql.DB
	// read reads a transcript's items for the index: readTranscriptItems,
	// which a test wraps to count the reads.
	read func(transcriptPath string, held *appwire.SnapshotIdentity) (transcriptItems, error)
	// writeMu keeps a Refresh and a Forget from interleaving their writes to
	// one session.
	writeMu sync.Mutex
	// forgetGen counts, per session, how many times Forget has removed it.
	// A Refresh reads a transcript before it writes what it found, and a
	// deletion's Forget can run on another goroutine in between: Refresh
	// captures the session's generation before the read and apply refuses to
	// write unless the generation is still the one it captured, so a
	// concurrent Forget is never undone by a read that started before it.
	// An entry stays for the hub's lifetime, one per forgotten session. That
	// growth is accepted rather than pruned: pruning an entry is safe only
	// once no in-flight read can still hold its old generation, which would
	// mean tracking every read in flight, and each entry is one session ID
	// and an int64. The count is bounded by the sessions the hub has ever
	// forgotten.
	forgetGen map[string]int64
}

// MessageSearchSession names one session the index covers: its ID and where
// its transcript is.
type MessageSearchSession struct {
	ID             string
	TranscriptPath string
}

// MessageSearchFailure is a transcript the index could not read, for a reason
// other than its format or a paging race (transcriptItemCursorStale), which
// are not failures. A session never indexed before is recorded the way an
// unreadable format is, so it is not tried again until the transcript
// changes, and the failure is reported once. A session already indexed keeps
// its existing rows instead: recording it would freeze that loss, so it is
// retried and re-reported every refresh until it succeeds.
type MessageSearchFailure struct {
	SessionID string
	Err       error
}

// MessageMatch is one session's answer to a message search: how many of its
// messages match, and the newest of them, newest first.
type MessageMatch struct {
	Count int
	Hits  []MessageHit
}

// MessageHit is one matching message: the transcript item it is.
type MessageHit struct {
	id            int64
	TranscriptKey string
	Position      appwire.ThreadItemPosition
}

// transcriptStamp is the transcript file's state when the index last read it:
// a transcript only grows, so an append moves its size, and a rewrite moves
// its modification time.
type transcriptStamp struct {
	size      int64
	modTimeNS int64
}

// indexedTranscript is what the index holds for one session: the file state it
// last read and the transcript-index snapshot its rows describe (no
// incarnation when the transcript could not be read).
type indexedTranscript struct {
	stamp    transcriptStamp
	snapshot appwire.SnapshotIdentity
}

// transcriptItems is one read of a transcript: the items it returned and the
// snapshot they describe. replace says they are every searchable message;
// otherwise they are every item an entry at or past the held snapshot's
// length created or changed, messages or not, since a changed item that is no
// longer a message still drops its row.
type transcriptItems struct {
	items    []appwire.ThreadItem
	snapshot appwire.SnapshotIdentity
	replace  bool
}

// OpenMessageSearch opens the message-text search index at path, creating it
// when it is missing and rebuilding it empty when it holds another schema
// version.
func OpenMessageSearch(path string) (*MessageSearch, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create message search directory: %w", err)
	}
	// Create the file owner-only before SQLite opens it: SQLite gives the WAL
	// and shared-memory files it creates later the database file's own mode,
	// and the index holds message text.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create message search index: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("create message search index: %w", err)
	}
	if err := chmodSQLiteIndexFiles(path); err != nil {
		return nil, fmt.Errorf("restrict message search index: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open message search index: %w", err)
	}
	if err := migrateMessageSearch(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &MessageSearch{db: db, read: readTranscriptItems, forgetGen: map[string]int64{}}, nil
}

// migrateMessageSearch leaves a current index alone and replaces anything
// else, a new file included, with an empty current one.
func migrateMessageSearch(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read message search version: %w", err)
	}
	if version == messageSearchSchemaVersion {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate message search index: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range slices.Concat(messageSearchDrop, messageSearchSchema) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate message search index: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, messageSearchSchemaVersion)); err != nil {
		return fmt.Errorf("migrate message search index: %w", err)
	}
	return tx.Commit()
}

// Close closes the index's database.
func (x *MessageSearch) Close() error {
	return x.db.Close()
}

// Refresh brings the index in step with sessions' transcripts. It reads only a
// transcript whose size or modification time moved since the index last read
// it, and then only what changed since the snapshot the index holds (all of it
// when the transcript's index was rebuilt since). It forgets every indexed
// session sessions no longer names. A transcript it cannot read is recorded
// with no messages, so it is not read again until it changes; a failure other
// than an unsupported format (a transcript older than format 2, which no
// reader opens) is returned, once. The error is the index's own: its database
// failed, or ctx ended.
func (x *MessageSearch) Refresh(ctx context.Context, sessions []MessageSearchSession) ([]MessageSearchFailure, error) {
	indexed, err := x.indexedTranscripts(ctx)
	if err != nil {
		return nil, err
	}
	var failures []MessageSearchFailure
	listed := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return failures, err
		}
		listed[session.ID] = true
		info, err := os.Stat(session.TranscriptPath)
		if err != nil {
			if !os.IsNotExist(err) {
				// A transient failure (permissions, a flaky disk): report it
				// and try again next refresh, but keep what is already
				// indexed rather than reading a mere stat error as deletion.
				failures = append(failures, MessageSearchFailure{SessionID: session.ID, Err: err})
				continue
			}
			// No transcript any more: nothing to search.
			if _, ok := indexed[session.ID]; ok {
				if err := x.Forget(ctx, session.ID); err != nil {
					return failures, err
				}
			}
			continue
		}
		stamp := transcriptStamp{size: info.Size(), modTimeNS: info.ModTime().UnixNano()}
		held, known := indexed[session.ID]
		if known && held.stamp == stamp {
			continue
		}
		var since *appwire.SnapshotIdentity
		if known && held.snapshot.Incarnation != "" {
			since = &held.snapshot
		}
		// Captured before the read, which can take a while: apply compares
		// this against the generation at write time, so a Forget that lands
		// during the read (a concurrent deletion) is never undone below.
		sinceGen := x.forgetGeneration(session.ID)
		read, err := x.read(session.TranscriptPath, since)
		if err != nil {
			switch {
			case errors.Is(err, transcript.ErrUnsupportedFormat):
				// Not a failure: record it as read, with no messages, so a
				// transcript no reader opens is not tried again until it
				// changes.
				read = transcriptItems{replace: true}
			case transcriptItemCursorStale(err):
				// A paging race between two reads of the same transcript
				// index (RetryDispositionAutomatic), not a broken transcript.
				// Recording it, known or not, would freeze the loss until the
				// file itself next changes, when readEveryItem's own promise
				// is that the very next refresh reads the new incarnation
				// whole: retry then instead.
				continue
			default:
				failures = append(failures, MessageSearchFailure{SessionID: session.ID, Err: err})
				if !known {
					// Never indexed, so there is nothing to lose: record it
					// the same way, rather than retrying every refresh.
					read = transcriptItems{replace: true}
				} else {
					// Already indexed: a transient failure must not wipe what
					// is there. Recording the current stamp here would freeze
					// that loss, since the next refresh would then see the
					// transcript as unchanged and skip it; leave both alone
					// and retry next refresh.
					continue
				}
			}
		}
		if err := x.apply(ctx, session.ID, stamp, read, sinceGen); err != nil {
			return failures, err
		}
	}
	for sessionID := range indexed {
		if !listed[sessionID] {
			if err := x.Forget(ctx, sessionID); err != nil {
				return failures, err
			}
		}
	}
	return failures, nil
}

// indexedTranscripts is what the index holds for every indexed session.
func (x *MessageSearch) indexedTranscripts(ctx context.Context) (map[string]indexedTranscript, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT session_id, size, mod_time_ns, incarnation, length FROM transcripts`)
	if err != nil {
		return nil, fmt.Errorf("read message search transcripts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	indexed := map[string]indexedTranscript{}
	for rows.Next() {
		var sessionID string
		var held indexedTranscript
		if err := rows.Scan(&sessionID, &held.stamp.size, &held.stamp.modTimeNS, &held.snapshot.Incarnation, &held.snapshot.Length); err != nil {
			return nil, fmt.Errorf("read message search transcripts: %w", err)
		}
		indexed[sessionID] = held
	}
	return indexed, rows.Err()
}

// testHookAfterIncarnationCheck runs, if set, after readTranscriptItems
// confirms the held incarnation still matches the transcript index's current
// one, just before it calls ChangedSince. A test uses it to force a rebuild
// of the same transcript index into that exact window.
var testHookAfterIncarnationCheck = func() {}

// readTranscriptItems reads a transcript's items through its transcript index,
// the read model a thread read uses, so every item carries the key and
// position a reader shows. It opens its own handle and closes it, so indexing
// every transcript never evicts the handles readers hold. With held naming the
// index's current incarnation it reads only what changed since held;
// otherwise, or when the index's update log no longer reaches back to held, it
// reads every item.
func readTranscriptItems(transcriptPath string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
	// A legacy transcript has no transcript index: checking its header first
	// keeps Open from leaving an empty sidecar directory beside it.
	if err := checkTranscriptHeader(transcriptPath); err != nil {
		return transcriptItems{}, err
	}
	index, err := transcriptindex.Open(transcriptPath, transcriptindex.DirFor(transcriptPath))
	if err != nil {
		return transcriptItems{}, err
	}
	defer func() { _ = index.Close() }()
	if held != nil {
		incarnation, err := index.Incarnation()
		if err != nil {
			return transcriptItems{}, err
		}
		if incarnation == held.Incarnation {
			testHookAfterIncarnationCheck()
			changes, err := index.ChangedSince(held.Length)
			switch {
			case err == nil && changes.Incarnation == incarnation:
				return transcriptItems{items: candidateItems(changes.Items), snapshot: appwire.SnapshotIdentity{Incarnation: changes.Incarnation, Length: changes.Length}}, nil
			case err != nil && !errors.Is(err, transcriptindex.ErrUpdateLogTruncated):
				return transcriptItems{}, err
			}
			// Either ChangedSince reported the update log truncated, or it
			// succeeded but under an incarnation that raced past the one
			// just confirmed above: a rebuild landed between the two calls
			// (another handle compacting or resuming the same transcript
			// index always mints a new incarnation,
			// transcriptindex.TestRebuildMintsANewIncarnation) and also
			// resets the update log's floor, so ChangedSince no longer
			// recognizes held.Length as stale and would otherwise answer
			// from the new incarnation's update log using the old
			// incarnation's length. Either way the partial view it produced
			// cannot be trusted: read the whole (current) incarnation.
		}
	}
	return readEveryItem(index)
}

// readEveryItem pages through every item of the index, newest window first,
// and keeps only the searchable messages: a whole read replaces the session's
// rows, so the rest (tool output above all) would only be held to be dropped.
func readEveryItem(index *transcriptindex.Index) (transcriptItems, error) {
	window, err := index.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		return transcriptItems{}, err
	}
	read := transcriptItems{snapshot: appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length}, replace: true}
	for {
		for _, candidate := range window.Candidates {
			if searchableMessage(candidate.Item) {
				read.items = append(read.items, candidate.Item)
			}
		}
		if !window.HasOlder || len(window.Candidates) == 0 {
			return read, nil
		}
		window, err = index.Before(window.Candidates[0].Position, appwire.TranscriptItemPageLimit)
		if err != nil {
			return transcriptItems{}, err
		}
		// A rebuild between two pages renames every item; the next refresh
		// reads the new incarnation whole.
		if window.Incarnation != read.snapshot.Incarnation {
			return transcriptItems{}, appwire.TranscriptItemCursorStale()
		}
	}
}

func candidateItems(candidates []appitempaging.TranscriptItemCandidate) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.Item)
	}
	return items
}

// checkTranscriptHeader reads the transcript's leading header line (skipping
// blank lines) and returns transcript.ErrUnsupportedFormat (wrapped) unless it
// is a format-2 header.
func checkTranscriptHeader(transcriptPath string) error {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = transcript.ReadHeader(context.Background(), bufio.NewReader(f), 128<<20)
	return err
}

// transcriptItemCursorStale reports whether err is
// appwire.TranscriptItemCursorStale(): a rebuild of the transcript index
// between two of this package's own reads, not a failure of the transcript
// itself. Same check as appsource's localDaemonItemCursorStale, duplicated
// because that one is unexported in another package.
func transcriptItemCursorStale(err error) bool {
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInvalidParams {
		return false
	}
	data, ok := wire.Data.(appwire.ErrorData)
	return ok && data.EvenerErrorInfo == appwire.ErrorTranscriptItemCursorStale
}

// searchableMessage reports whether the index keeps item: what the user typed
// (a message, or a steer the user sent) and what the agent said. Reasoning,
// tool calls and their output, and system notices are not messages. An item
// without a key cannot be opened at, and one with no text has nothing to find.
func searchableMessage(item appwire.ThreadItem) bool {
	message := item.Type == "userMessage" || item.Type == "agentMessage" ||
		(item.Type == "steering" && item.Source == events.SteeringSourceUser)
	return message && item.TranscriptKey != "" && item.Position != nil && strings.TrimSpace(item.Text) != ""
}

// apply writes one read of a session in one transaction, so a search sees the
// session's messages from before the read or after it and never a mix: a
// replace drops every row first; otherwise each changed item's row is dropped
// and written again while the item is a message. Then it records stamp and the
// read's snapshot. sinceGen is the session's forget generation captured before
// the read; if a Forget landed since (the generation moved), the read is
// stale and apply writes nothing, so a concurrent deletion is never undone.
func (x *MessageSearch) apply(ctx context.Context, sessionID string, stamp transcriptStamp, read transcriptItems, sinceGen int64) error {
	x.writeMu.Lock()
	defer x.writeMu.Unlock()
	if x.forgetGen[sessionID] != sinceGen {
		return nil
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("index session %s: %w", sessionID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if read.replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
			return fmt.Errorf("index session %s: %w", sessionID, err)
		}
	}
	// One statement of each kind per read: a first read of a long transcript
	// writes a row per message.
	drop, err := tx.PrepareContext(ctx, `DELETE FROM messages WHERE session_id = ? AND transcript_key = ?`)
	if err != nil {
		return fmt.Errorf("index session %s: %w", sessionID, err)
	}
	defer func() { _ = drop.Close() }()
	insert, err := tx.PrepareContext(ctx, `INSERT INTO messages(session_id, transcript_key, entry, item, sub, text) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("index session %s: %w", sessionID, err)
	}
	defer func() { _ = insert.Close() }()
	for _, item := range read.items {
		if !read.replace {
			if _, err := drop.ExecContext(ctx, sessionID, item.TranscriptKey); err != nil {
				return fmt.Errorf("index session %s: %w", sessionID, err)
			}
		}
		if !searchableMessage(item) {
			continue
		}
		if _, err := insert.ExecContext(ctx, sessionID, item.TranscriptKey, int64(item.Position.Entry), int64(item.Position.Item), int64(item.Position.Sub), item.Text); err != nil {
			return fmt.Errorf("index session %s: %w", sessionID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transcripts(session_id, size, mod_time_ns, incarnation, length) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET size = excluded.size, mod_time_ns = excluded.mod_time_ns, incarnation = excluded.incarnation, length = excluded.length`,
		sessionID, stamp.size, stamp.modTimeNS, read.snapshot.Incarnation, read.snapshot.Length); err != nil {
		return fmt.Errorf("index session %s: %w", sessionID, err)
	}
	return tx.Commit()
}

// Forget removes one session from the index: its messages and its transcript
// record. Deleting a session calls it, so the session's words leave search at
// once rather than at the next Refresh. It also advances the session's forget
// generation, so a Refresh whose read of this session started before this
// call cannot write the session back afterward (apply, above).
func (x *MessageSearch) Forget(ctx context.Context, sessionID string) error {
	x.writeMu.Lock()
	defer x.writeMu.Unlock()
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("forget session %s: %w", sessionID, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{`DELETE FROM messages WHERE session_id = ?`, `DELETE FROM transcripts WHERE session_id = ?`} {
		if _, err := tx.ExecContext(ctx, stmt, sessionID); err != nil {
			return fmt.Errorf("forget session %s: %w", sessionID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("forget session %s: %w", sessionID, err)
	}
	x.forgetGen[sessionID]++
	return nil
}

// forgetGeneration is sessionID's current forget generation.
func (x *MessageSearch) forgetGeneration(sessionID string) int64 {
	x.writeMu.Lock()
	defer x.writeMu.Unlock()
	return x.forgetGen[sessionID]
}

// Match finds the messages that hold every word of query, each word matching
// as a prefix, letter case aside. It answers for every matching session: how
// many of its messages match, and its newest hitsPerSession hits. A query with
// no word of at least two letters or digits searches nothing.
func (x *MessageSearch) Match(ctx context.Context, query string, hitsPerSession int) (map[string]MessageMatch, error) {
	if !hasMessageSearchWord(SearchTokens(query)) {
		return map[string]MessageMatch{}, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT m.id, m.session_id, m.transcript_key, m.entry, m.item, m.sub
FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
WHERE messages_fts MATCH ?`, ftsQuery(query))
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	matches := map[string]MessageMatch{}
	for rows.Next() {
		var sessionID string
		var hit MessageHit
		var entry, item, sub int64
		if err := rows.Scan(&hit.id, &sessionID, &hit.TranscriptKey, &entry, &item, &sub); err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		hit.Position = appwire.ThreadItemPosition{Entry: uint64(entry), Item: uint32(item), Sub: uint32(sub)}
		match := matches[sessionID]
		match.Count++
		match.Hits = keepNewestHits(match.Hits, hit, hitsPerSession)
		matches[sessionID] = match
	}
	return matches, rows.Err()
}

// hasMessageSearchWord reports whether any token holds at least
// minMessageSearchTokenRunes letters or digits, so a query whose only word is
// a single letter does not reach FTS5 as the broad single-letter prefix search
// the minimum exists to reject.
func hasMessageSearchWord(tokens []string) bool {
	for _, token := range tokens {
		if wordRuneCount(token) >= minMessageSearchTokenRunes {
			return true
		}
	}
	return false
}

// wordRuneCount is how many of s's runes are index word characters.
func wordRuneCount(s string) int {
	n := 0
	for _, r := range s {
		if IsIndexWordRune(r) {
			n++
		}
	}
	return n
}

// keepNewestHits adds hit to hits, which is newest first, and keeps at most
// limit of them.
func keepNewestHits(hits []MessageHit, hit MessageHit, limit int) []MessageHit {
	at, _ := slices.BinarySearchFunc(hits, hit, func(kept, hit MessageHit) int {
		return compareItemPositions(hit.Position, kept.Position)
	})
	if at >= limit {
		return hits
	}
	hits = slices.Insert(hits, at, hit)
	return hits[:min(len(hits), limit)]
}

// compareItemPositions orders two items of one transcript: negative when a
// comes first.
func compareItemPositions(a, b appwire.ThreadItemPosition) int {
	return cmp.Or(cmp.Compare(a.Entry, b.Entry), cmp.Compare(a.Item, b.Item), cmp.Compare(a.Sub, b.Sub))
}

// Texts returns each hit's message text, in hits' order. A hit whose message
// left the index since the match comes back empty.
func (x *MessageSearch) Texts(ctx context.Context, hits []MessageHit) ([]string, error) {
	texts := make([]string, len(hits))
	if len(hits) == 0 {
		return texts, nil
	}
	args := make([]any, len(hits))
	for i, hit := range hits {
		args[i] = hit.id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hits)), ",")
	rows, err := x.db.QueryContext(ctx, `SELECT id, text FROM messages WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read message texts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byID := make(map[int64]string, len(hits))
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, fmt.Errorf("read message texts: %w", err)
		}
		byID[id] = text
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read message texts: %w", err)
	}
	for i, hit := range hits {
		texts[i] = byID[hit.id]
	}
	return texts, nil
}
