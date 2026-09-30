package hubcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// countReads wraps index's transcript reads and records the file each read,
// with "+" after it when the read took only what changed since the snapshot
// the index held.
func countReads(index *MessageSearch) *[]string {
	var read []string
	next := index.read
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		items, err := next(path, held)
		name := filepath.Base(path)
		if err == nil && !items.replace {
			name += "+"
		}
		read = append(read, name)
		return items, err
	}
	return &read
}

func openTestMessageSearch(t *testing.T) *MessageSearch {
	t.Helper()
	index, err := OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index
}

// writeTestTranscript writes a format-2 transcript of turns for sessionID at
// path.
func writeTestTranscript(t *testing.T, path, sessionID string, turns ...schema.Turn) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC),
		ProfileID: "openai", Model: "gpt-5", SystemPrompt: "You are a careful engineer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// appendTestTranscript appends turns to the transcript at path.
func appendTestTranscript(t *testing.T, path string, turns ...schema.Turn) {
	t.Helper()
	w, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// settleTurns is a user message, then an assistant turn that reasons, speaks
// and runs a tool, then the tool's output and the agent's answer. Every one of
// them says "settle"; only the two messages are searchable.
func settleTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Why does the settle pass race the drain?")),
		{Kind: schema.TurnAssistant, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning about settle"}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"grep settle"}`)}},
		}}},
		{Kind: schema.TurnToolResults, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_1", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "shell", Content: "settle.go:12: settle()"},
		}}}},
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass takes the tree lock first.")),
	}
}

// messageItems is every message item a thread read of path shows, by key,
// with its position: the latest window of the transcript's index, which holds
// every item of these short fixtures.
func messageItems(t *testing.T, path string) map[string]appwire.ThreadItemPosition {
	t.Helper()
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	window, err := index.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if window.HasOlder {
		t.Fatal("the fixture outgrew one window")
	}
	items := map[string]appwire.ThreadItemPosition{}
	for _, candidate := range window.Candidates {
		if searchableMessage(candidate.Item) {
			items[candidate.Item.TranscriptKey] = candidate.Position
		}
	}
	return items
}

// The index keeps what the user typed and what the agent said, each under the
// transcript key and position a thread read gives that message's item, so a
// hit opens the session at the message. Reasoning, a tool's call and its
// output are not messages, whatever they say.
func TestMessageSearchKeepsUserAndAgentMessagesUnderTheirReadKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}

	matches, err := index.Match(context.Background(), "settle", 10)
	if err != nil {
		t.Fatal(err)
	}
	match, ok := matches["s1"]
	if !ok || match.Count != 2 || len(match.Hits) != 2 {
		t.Fatalf("matches = %+v, want s1's two messages", matches)
	}
	want := messageItems(t, path)
	if len(want) != 2 {
		t.Fatalf("the fixture projects %d message items, want 2", len(want))
	}
	for _, hit := range match.Hits {
		if position, ok := want[hit.TranscriptKey]; !ok || position != hit.Position {
			t.Fatalf("hit %+v is not a message item the read shows (%v)", hit, want)
		}
	}
	if compareItemPositions(match.Hits[0].Position, match.Hits[1].Position) <= 0 {
		t.Fatalf("hits = %+v, want the newest first", match.Hits)
	}
	texts, err := index.Texts(context.Background(), match.Hits)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"The settle pass takes the tree lock first.", "Why does the settle pass race the drain?"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q", texts, want)
	}
}

// Every word must match, each as a prefix, letter case aside. A search whose
// words are all one letter searches nothing, and FTS5's own syntax in a search
// is only words.
func TestMessageSearchMatchesEveryWordAsAPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{
		"SETT":                       2,
		"settle drain":               1,
		"settle lunaroute":           0,
		"t":                          0,
		`"settle" OR (drain* NEAR x`: 0,
		`settle:drain`:               1,
	} {
		matches, err := index.Match(context.Background(), query, 10)
		if err != nil {
			t.Fatalf("Match(%q): %v", query, err)
		}
		if got := matches["s1"].Count; got != want {
			t.Errorf("Match(%q) found %d messages, want %d", query, got, want)
		}
	}
}

// A session keeps its newest hits and counts the rest.
func TestMessageSearchKeepsTheNewestHitsPerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	var turns []schema.Turn
	for range 5 {
		turns = append(turns, schema.NewTurn(schema.TurnUserInput, llm.User("check the drain")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("the drain is fine")))
	}
	writeTestTranscript(t, path, "s1", turns...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	matches, err := index.Match(context.Background(), "drain", 3)
	if err != nil {
		t.Fatal(err)
	}
	match := matches["s1"]
	if match.Count != 10 || len(match.Hits) != 3 {
		t.Fatalf("match = %+v, want 10 counted and the newest 3 kept", match)
	}
	all := messageItems(t, path)
	positions := make([]appwire.ThreadItemPosition, 0, len(all))
	for _, position := range all {
		positions = append(positions, position)
	}
	sort.Slice(positions, func(i, j int) bool { return compareItemPositions(positions[i], positions[j]) > 0 })
	for i, hit := range match.Hits {
		if hit.Position != positions[i] {
			t.Fatalf("hit %d = %+v, want position %+v, the %d-th newest", i, hit, positions[i], i+1)
		}
	}
}

// A refresh reads again only the transcripts that changed: a session whose
// transcript grew is re-read and its new message is found, and a session that
// did not change is not read at all.
func TestMessageSearchRefreshReadsOnlyChangedTranscripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	first, second := filepath.Join(dir, "s1.transcript.jsonl"), filepath.Join(dir, "s2.transcript.jsonl")
	writeTestTranscript(t, first, "s1", settleTurns()...)
	writeTestTranscript(t, second, "s2", schema.NewTurn(schema.TurnUserInput, llm.User("unrelated work")))
	index := openTestMessageSearch(t)
	read := countReads(index)
	sessions := []MessageSearchSession{{ID: "s1", TranscriptPath: first}, {ID: "s2", TranscriptPath: second}}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if want := []string{"s1.transcript.jsonl", "s2.transcript.jsonl"}; !reflect.DeepEqual(*read, want) {
		t.Fatalf("read %v, want each transcript once across two refreshes", *read)
	}

	appendTestTranscript(t, first, schema.NewTurn(schema.TurnUserInput, llm.User("now fix the retirement drain")))
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if want := []string{"s1.transcript.jsonl", "s2.transcript.jsonl", "s1.transcript.jsonl+"}; !reflect.DeepEqual(*read, want) {
		t.Fatalf("read %v, want only the grown transcript read again, and only what it gained", *read)
	}
	matches, err := index.Match(context.Background(), "retirement", 3)
	if err != nil {
		t.Fatal(err)
	}
	if matches["s1"].Count != 1 {
		t.Fatalf("matches = %+v, want the appended message found", matches)
	}
	// The read took only what changed: the messages from before stay once.
	if matches, _ := index.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("settle matches = %+v, want the two earlier messages once each", matches)
	}
}

// A transcript that stops being its old self grown by appends (a rewrite) has
// its transcript index rebuilt under a new incarnation, and the index reads it
// whole again: the old messages go, the new ones come.
func TestMessageSearchRereadsARewrittenTranscriptWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	sessions := []MessageSearchSession{{ID: "s1", TranscriptPath: path}}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeTestTranscript(t, path, "s1",
		schema.NewTurn(schema.TurnUserInput, llm.User("audit the tool descriptions")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Fourteen descriptions mention flags.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("drop them")),
	)
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 0 {
		t.Fatalf("settle matches = %+v, want none after the rewrite", matches)
	}
	if matches, _ := index.Match(context.Background(), "descriptions", 3); matches["s1"].Count != 2 {
		t.Fatalf("descriptions matches = %+v, want the rewritten transcript's two messages", matches)
	}
}

// A rebuild of the transcript index (another handle compacting or resuming
// it) can land between readTranscriptItems' own incarnation check and its
// ChangedSince call: a rebuild always mints a new incarnation
// (transcriptindex.TestRebuildMintsANewIncarnation), and a rebuild resets the
// update log's floor to zero, so ChangedSince no longer recognizes held.Length
// as stale and would otherwise answer from the new incarnation's update log
// using the old incarnation's length. readTranscriptItems must notice the
// incarnation moved and read the whole (new) incarnation instead of trusting
// that partial view.
func TestReadTranscriptItemsFallsBackWholeWhenARebuildRacesTheIncarnationCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", schema.NewTurn(schema.TurnUserInput, llm.User("first message")))
	first, err := readTranscriptItems(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	held := appwire.SnapshotIdentity{Incarnation: first.snapshot.Incarnation, Length: first.snapshot.Length}
	appendTestTranscript(t, path, schema.NewTurn(schema.TurnUserInput, llm.User("second message")))

	t.Cleanup(func() { testHookAfterIncarnationCheck = func() {} })
	testHookAfterIncarnationCheck = func() {
		other, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = other.Close() }()
		window, err := other.Latest(1)
		if err != nil {
			t.Fatal(err)
		}
		if err := other.Rebuild(window.Length); err != nil {
			t.Fatal(err)
		}
	}

	read, err := readTranscriptItems(path, &held)
	if err != nil {
		t.Fatal(err)
	}
	if !read.replace {
		t.Fatalf("read = %+v, want a whole re-read after the racing rebuild, not a partial 'changes since' view", read)
	}
	var texts []string
	for _, item := range read.items {
		if searchableMessage(item) {
			texts = append(texts, item.Text)
		}
	}
	if want := []string{"first message", "second message"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q: both messages, from a whole read of the new incarnation", texts, want)
	}
}

// A held snapshot whose length predates the transcript index's kept update log
// makes ChangedSince report ErrUpdateLogTruncated: the log is bounded and cut
// back to its newest records, so the changes since that length are no longer
// all known. readTranscriptItems must then read the whole current incarnation
// rather than trust a partial "changes since" view. SetUpdateLogRecordsForTest
// shrinks the log so a few appends overflow it, standing in for a transcript
// that grew past the records the index kept. Only entries that update an item
// in place (a tool result completing a tool call) log records, which is why the
// fixture appends settle turns.
func TestReadTranscriptItemsReadsWholeWhenTheUpdateLogIsTruncated(t *testing.T) {
	defer transcriptindex.SetUpdateLogRecordsForTest(2)()
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	first, err := readTranscriptItems(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	held := appwire.SnapshotIdentity{Incarnation: first.snapshot.Incarnation, Length: first.snapshot.Length}
	for range 4 {
		appendTestTranscript(t, path, settleTurns()...)
	}

	// The fixture must actually overflow the kept log: otherwise the read below
	// would pass without ever reaching the truncated-log fallthrough.
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	if _, err := index.ChangedSince(held.Length); !errors.Is(err, transcriptindex.ErrUpdateLogTruncated) {
		t.Fatalf("ChangedSince(%d) = %v, want ErrUpdateLogTruncated: the appends did not overflow the kept log", held.Length, err)
	}

	read, err := readTranscriptItems(path, &held)
	if err != nil {
		t.Fatal(err)
	}
	if !read.replace {
		t.Fatalf("read = %+v, want a whole re-read after the update log truncated, not a partial 'changes since' view", read)
	}
	got := map[string]appwire.ThreadItemPosition{}
	for _, item := range read.items {
		if searchableMessage(item) {
			got[item.TranscriptKey] = *item.Position
		}
	}
	if want := messageItems(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("read covers %v, want every current message %v: a whole read of the current incarnation", got, want)
	}
}

// The index outlives the hub: reopened, it keeps what it read and reads
// nothing again until a transcript changes.
func TestMessageSearchKeepsWhatItReadAcrossAReopen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	dbPath := filepath.Join(root, "search.db")
	index, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions := []MessageSearchSession{{ID: "s1", TranscriptPath: path}}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	read := countReads(reopened)
	if _, err := reopened.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if len(*read) != 0 {
		t.Fatalf("the reopened index read %v again, want nothing", *read)
	}
	if matches, err := reopened.Match(context.Background(), "settle", 3); err != nil || matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v (%v), want what the first index read", matches, err)
	}
}

// A transcript older than format 2, which no reader opens, is recorded with
// no messages and not read again until it changes; it is not a failure, and
// no transcript index is built beside it. A transcript that fails to read for
// another reason is recorded the same way, and reported, once.
func TestMessageSearchRecordsAnUnreadableTranscriptOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	legacy, failing := filepath.Join(dir, "old.transcript.jsonl"), filepath.Join(dir, "bad.transcript.jsonl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestTranscript(t, failing, "bad", settleTurns()...)
	index := openTestMessageSearch(t)
	next := index.read
	var read []string
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		read = append(read, filepath.Base(path))
		if path == failing {
			return transcriptItems{}, errors.New("the disk refused the read")
		}
		return next(path, held)
	}
	sessions := []MessageSearchSession{{ID: "old", TranscriptPath: legacy}, {ID: "bad", TranscriptPath: failing}}

	failures, err := index.Refresh(context.Background(), sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].SessionID != "bad" {
		t.Fatalf("failures = %+v, want one for the transcript that failed to read", failures)
	}
	failures, err = index.Refresh(context.Background(), sessions)
	if err != nil || len(failures) != 0 {
		t.Fatalf("second refresh failures = %+v (%v), want none", failures, err)
	}
	if want := []string{"old.transcript.jsonl", "bad.transcript.jsonl"}; !reflect.DeepEqual(read, want) {
		t.Fatalf("read %v, want each unreadable transcript read once", read)
	}
	if _, err := os.Stat(transcriptindex.DirFor(legacy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a transcript index was built beside the legacy transcript: %v", err)
	}
}

// A transient stat failure (permissions, a flaky disk) is not the same as a
// deleted transcript: it must not forget the session's already-indexed
// messages, only report the failure and try again next refresh.
func TestMessageSearchATransientStatFailureDoesNotForgetTheSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(dir, "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}

	// A path whose parent component is a plain file, not a directory: os.Stat
	// fails with "not a directory" rather than os.ErrNotExist, standing in for
	// any stat failure that is not "the transcript is gone."
	notADir := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreachable := filepath.Join(notADir, "sub", "s1.transcript.jsonl")
	if _, err := os.Stat(unreachable); os.IsNotExist(err) {
		t.Fatalf("the fixture's stat error is os.ErrNotExist, want another failure: %v", err)
	}

	failures, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: unreachable}})
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].SessionID != "s1" {
		t.Fatalf("failures = %+v, want one for the unreachable transcript", failures)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v, want the session's earlier messages kept", matches)
	}
}

// A read failure on a transcript that has already been indexed (the file
// changed, so a refresh attempts to re-read it, and this attempt fails for a
// reason other than an unsupported format) must not wipe the session's
// existing rows: there is something real to lose here, unlike a transcript
// that has never been read. The failure is retried next refresh rather than
// recorded as final.
func TestMessageSearchAFailedReadOnAnIndexedSessionKeepsItsMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	appendTestTranscript(t, path, schema.NewTurn(schema.TurnUserInput, llm.User("more settle work")))
	next := index.read
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		return transcriptItems{}, errors.New("the disk refused the read")
	}
	failures, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].SessionID != "s1" {
		t.Fatalf("failures = %+v, want one for the failed read", failures)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v, want the session's earlier messages kept, not wiped by the failed read", matches)
	}

	index.read = next
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); matches["s1"].Count != 3 {
		t.Fatalf("matches = %+v, want the appended message found once the retry succeeds", matches)
	}
}

// appwire.TranscriptItemCursorStale carries RetryDispositionAutomatic: a
// paging race between two reads of the same transcript index, not a broken
// transcript. It must be retried next refresh even on a session that has
// never been indexed, not recorded as permanently empty the way an
// unsupported format is.
func TestMessageSearchRetriesACursorStaleReadEvenOnAFirstAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	next := index.read
	failedOnce := false
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		if !failedOnce {
			failedOnce = true
			return transcriptItems{}, appwire.TranscriptItemCursorStale()
		}
		return next(path, held)
	}
	failures, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}})
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatalf("failures = %+v, want none: a cursor-stale race is not reported as a failure", failures)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 0 {
		t.Fatalf("matches = %+v, want none yet: the session was not indexed on the failed attempt", matches)
	}
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v, want the session indexed once the retry succeeds", matches)
	}
}

// A session the past index no longer lists, and one Forget names, leave the
// index at once: their words are not found.
func TestMessageSearchForgetsSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	first, second := filepath.Join(dir, "s1.transcript.jsonl"), filepath.Join(dir, "s2.transcript.jsonl")
	writeTestTranscript(t, first, "s1", settleTurns()...)
	writeTestTranscript(t, second, "s2", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: first}, {ID: "s2", TranscriptPath: second}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Forget(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 1 || matches["s2"].Count != 2 {
		t.Fatalf("after Forget(s1) matches = %+v, want s2 alone", matches)
	}
	if _, err := index.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 0 {
		t.Fatalf("after the past index listed nothing, matches = %+v, want none", matches)
	}
}

// A hit's row id must never be reused by a later message, or a stale hit
// (captured by Match before the message it names left the index) would
// resolve, through Texts, to unrelated text instead of coming back empty as
// Texts documents.
func TestMessageSearchNeverReusesARowIDForAStaleHit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	matches, err := index.Match(context.Background(), "settle", 3)
	if err != nil {
		t.Fatal(err)
	}
	staleHits := matches["s1"].Hits
	if err := index.Forget(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}

	path2 := filepath.Join(t.TempDir(), "sessions", "s2.transcript.jsonl")
	writeTestTranscript(t, path2, "s2", settleTurns()...)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s2", TranscriptPath: path2}}); err != nil {
		t.Fatal(err)
	}

	texts, err := index.Texts(context.Background(), staleHits)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range texts {
		if text != "" {
			t.Fatalf("Texts(stale hit %+v) = %q, want empty: its row id must not have been reused by s2's re-indexing", staleHits[i], text)
		}
	}
}

// A Refresh reads a session's transcript, then writes what it found; a
// deletion's Forget can land in between, on another goroutine, after the read
// but before the write. The write must not resurrect what the concurrent
// Forget just removed. This drives that race deterministically: the read hook
// itself calls Forget the moment the read completes, standing in for the
// other goroutine that would otherwise need to win a timing race.
func TestMessageSearchRefreshDoesNotResurrectASessionForgottenDuringItsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	next := index.read
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		items, err := next(path, held)
		if err == nil {
			if err := index.Forget(context.Background(), "s1"); err != nil {
				t.Fatal(err)
			}
		}
		return items, err
	}
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 0 {
		t.Fatalf("matches = %+v, want none: the concurrent Forget must win over the read that started before it", matches)
	}
}

// search.db holds message text, so it and the WAL file SQLite creates beside
// it are the owner's alone.
func TestMessageSearchFilesAreOwnerOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	dbPath := filepath.Join(root, "search.db")
	index, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{dbPath, dbPath + "-wal"} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		hubtest.AssertFileMode0600(t, info, filepath.Base(file))
	}
}

// An index of another schema version is a cache of the transcripts, so
// opening it drops it and starts empty rather than failing.
func TestMessageSearchRebuildsAnotherSchemaVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	dbPath := filepath.Join(root, "search.db")
	index, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.db.ExecContext(context.Background(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if matches, err := reopened.Match(context.Background(), "settle", 3); err != nil || len(matches) != 0 {
		t.Fatalf("matches = %+v (%v), want an empty index", matches, err)
	}
	if _, err := reopened.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := reopened.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v, want the transcript read again", matches)
	}
}

// SearchTokens is the one word rule the title search, the message search and
// its highlighting share: lowercased runs of letters, digits and underscores.
func TestSearchTokensSplitsOnEverythingButWordCharacters(t *testing.T) {
	if got, want := SearchTokens(`Fix "the" settle_race (NEAR drain*)`), []string{"fix", "the", "settle_race", "near", "drain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchTokens = %q, want %q", got, want)
	}
	if got := strings.Join(SearchTokens("!!!"), ","); got != "" {
		t.Fatalf("SearchTokens(punctuation) = %q, want none", got)
	}
}

// A search word must hold two letters or digits, not merely two runes: the
// index's unicode61 tokenizer treats an underscore as a separator, so a query
// like "_t" or "__" would otherwise reach FTS5 as the broad single-letter
// prefix "t*" (or no term at all) rather than being rejected as too short.
func TestMessageSearchIgnoresUnderscoresWhenCountingAWordsLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"_t", "__", "_"} {
		matches, err := index.Match(context.Background(), query, 10)
		if err != nil {
			t.Fatalf("Match(%q): %v", query, err)
		}
		if len(matches) != 0 {
			t.Errorf("Match(%q) = %+v, want none: it has fewer than two real letters or digits", query, matches)
		}
	}
}
