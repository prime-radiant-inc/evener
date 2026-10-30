package hubcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestOpenMessageSearchPathFailures(t *testing.T) {
	t.Parallel()
	t.Run("parent is a file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenMessageSearch(filepath.Join(blocker, "search.db")); err == nil {
			t.Fatal("a path under a file was accepted")
		}
	})
	t.Run("path is a directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if _, err := OpenMessageSearch(dir); err == nil {
			t.Fatal("a directory was accepted as the index file")
		}
	})
}

// stubTranscriptReads replaces the index's transcript read for the next calls,
// returning the scripted result and restoring the real read after.
func stubTranscriptReads(index *MessageSearch, result func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error)) (restore func()) {
	prev := index.read
	index.read = result
	return func() { index.read = prev }
}

func TestMessageSearchRefreshFailureArms(t *testing.T) {
	t.Parallel()
	newSession := func(t *testing.T) (MessageSearchSession, string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
		writeTestTranscript(t, path, "s1", settleTurns()...)
		return MessageSearchSession{ID: "s1", TranscriptPath: path}, path
	}

	t.Run("canceled refresh reports the cancellation", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := index.Refresh(ctx, []MessageSearchSession{session}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	})
	t.Run("transient stat failure is reported without dropping the index", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: filepath.Join(blocker, "inner")}})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 || failures[0].SessionID != "s1" {
			t.Fatalf("failures = %+v, want the stat failure recorded", failures)
		}
	})
	t.Run("removed transcript forgets the session", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, path := newSession(t)
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		if matches, err := index.Match(context.Background(), "settle", 10); err != nil || len(matches) != 0 {
			t.Fatalf("matches = %+v (%v), want the session forgotten", matches, err)
		}
	})
	t.Run("unlisted session is forgotten", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		if _, err := index.Refresh(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if matches, err := index.Match(context.Background(), "settle", 10); err != nil || len(matches) != 0 {
			t.Fatalf("matches = %+v (%v), want the unlisted session gone", matches, err)
		}
	})
	t.Run("unknown session read failure is recorded once", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		readFailure := errors.New("disk hiccup")
		reads := 0
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			reads++
			return transcriptItems{}, readFailure
		})
		defer restore()
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 || failures[0].SessionID != "s1" {
			t.Fatalf("failures = %+v, want the read failure recorded", failures)
		}
		failures, err = index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 || reads != 1 {
			t.Fatalf("failures = %+v reads = %d, want the failed session recorded and not retried", failures, reads)
		}
	})
	t.Run("known session transient failure keeps the indexed rows", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, path := newSession(t)
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		appendTestTranscript(t, path, schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass also drains the queue.")))
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			return transcriptItems{}, errors.New("disk hiccup")
		})
		defer restore()
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 1 {
			t.Fatalf("failures = %+v, want the transient failure reported", failures)
		}
		if matches, err := index.Match(context.Background(), "settle", 10); err != nil || matches["s1"].Count != 2 {
			t.Fatalf("matches = %+v (%v), want the previously indexed rows kept", matches, err)
		}
	})
	t.Run("cursor-stale read is retried next cycle", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, path := newSession(t)
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		appendTestTranscript(t, path, schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass also drains the queue.")))
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			return transcriptItems{}, appwire.TranscriptItemCursorStale()
		})
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 {
			t.Fatalf("failures = %+v, want the paging race skipped silently", failures)
		}
		restore()
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		if matches, err := index.Match(context.Background(), "settle", 10); err != nil || matches["s1"].Count != 3 {
			t.Fatalf("matches = %+v (%v), want the changed transcript indexed on the retry", matches, err)
		}
	})
	t.Run("rebuild-required read asks for the shared repair", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, path := newSession(t)
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err != nil {
			t.Fatal(err)
		}
		appendTestTranscript(t, path, schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass also drains the queue.")))
		var askedPath, askedBuild string
		index.SetRepairAsker(func(transcriptPath, refusedBuild string) {
			askedPath, askedBuild = transcriptPath, refusedBuild
		})
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			return transcriptItems{}, &transcriptindex.RebuildRequiredError{Build: "stale-build"}
		})
		defer restore()
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 {
			t.Fatalf("failures = %+v, want the refusing read skipped silently", failures)
		}
		if askedPath != path || askedBuild != "stale-build" {
			t.Fatalf("repair ask = (%q, %q), want the refused build named", askedPath, askedBuild)
		}
	})
	t.Run("lock-busy read is skipped silently", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			return transcriptItems{}, transcriptindex.ErrLockBusy
		})
		defer restore()
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 {
			t.Fatalf("failures = %+v, want the busy lock skipped silently", failures)
		}
	})
	t.Run("unsupported transcript is recorded without a failure", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		reads := 0
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			reads++
			return transcriptItems{}, transcript.ErrUnsupportedFormat
		})
		defer restore()
		failures, err := index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 {
			t.Fatalf("failures = %+v, want the unsupported format recorded without a failure", failures)
		}
		failures, err = index.Refresh(context.Background(), []MessageSearchSession{session})
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 || reads != 1 {
			t.Fatalf("failures = %+v reads = %d, want the unsupported transcript not retried", failures, reads)
		}
	})
	t.Run("cancellation during the read fails the write", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		restore := stubTranscriptReads(index, func(string, *appwire.SnapshotIdentity) (transcriptItems, error) {
			cancel()
			return transcriptItems{replace: true}, nil
		})
		defer restore()
		if _, err := index.Refresh(ctx, []MessageSearchSession{session}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	})
	t.Run("closed index refuses to refresh", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		session, _ := newSession(t)
		if err := index.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := index.Refresh(context.Background(), []MessageSearchSession{session}); err == nil {
			t.Fatal("a closed index refreshed")
		}
		if err := index.Forget(context.Background(), "s1"); err == nil {
			t.Fatal("a closed index forgot a session")
		}
	})
	t.Run("a forget generation gap drops the stale write", func(t *testing.T) {
		t.Parallel()
		index := openTestMessageSearch(t)
		if err := index.apply(context.Background(), "s1", TranscriptStamp{Size: 1, ModTimeNS: 2}, transcriptItems{replace: true}, 7); err != nil {
			t.Fatalf("stale write = %v", err)
		}
		if matches, err := index.Match(context.Background(), "anything", 10); err != nil || len(matches) != 0 {
			t.Fatalf("matches = %+v (%v), want nothing written across the generation gap", matches, err)
		}
	})
}

func TestMessageSearchQueryEdges(t *testing.T) {
	t.Parallel()
	index := openTestMessageSearch(t)
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	t.Run("a query without a searchable word matches nothing", func(t *testing.T) {
		t.Parallel()
		matches, err := index.Match(context.Background(), "a", 10)
		if err != nil || len(matches) != 0 {
			t.Fatalf("matches = %+v (%v), want an empty result", matches, err)
		}
	})
	t.Run("texts for hits that left the index come back empty", func(t *testing.T) {
		t.Parallel()
		texts, err := index.Texts(context.Background(), []MessageHit{{id: 999999, TranscriptKey: "gone"}})
		if err != nil || len(texts) != 1 || texts[0] != "" {
			t.Fatalf("texts = %q (%v), want one empty text", texts, err)
		}
	})
	t.Run("no hits need no texts", func(t *testing.T) {
		t.Parallel()
		texts, err := index.Texts(context.Background(), nil)
		if err != nil || len(texts) != 0 {
			t.Fatalf("texts = %q (%v), want no texts", texts, err)
		}
	})
	t.Run("a closed index refuses to search", func(t *testing.T) {
		t.Parallel()
		closed := openTestMessageSearch(t)
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := closed.Match(context.Background(), "settle", 10); err == nil {
			t.Fatal("a closed index searched")
		}
		if _, err := closed.Texts(context.Background(), []MessageHit{{id: 1}}); err == nil {
			t.Fatal("a closed index read texts")
		}
	})
}

func TestSearchableMessage(t *testing.T) {
	t.Parallel()
	position := appwire.ThreadItemPosition{Entry: 1}
	item := func(kind string, key string, pos *appwire.ThreadItemPosition, text, source string) appwire.ThreadItem {
		return appwire.ThreadItem{Type: kind, TranscriptKey: key, Position: pos, Text: text, Source: source}
	}
	for _, tc := range []struct {
		name string
		item appwire.ThreadItem
		want bool
	}{
		{"user message", item("userMessage", "k", &position, "hello", ""), true},
		{"agent message", item("agentMessage", "k", &position, "hello", ""), true},
		{"user steer", item("steering", "k", &position, "hello", events.SteeringSourceUser), true},
		{"parent steer", item("steering", "k", &position, "hello", events.SteeringSourceParent), true},
		{"daemon steer", item("steering", "k", &position, "hello", ""), false},
		{"tool output", item("toolOutput", "k", &position, "hello", ""), false},
		{"without a key", item("userMessage", "", &position, "hello", ""), false},
		{"without a position", item("userMessage", "k", nil, "hello", ""), false},
		{"without text", item("userMessage", "k", &position, "   ", ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := searchableMessage(tc.item); got != tc.want {
				t.Fatalf("searchableMessage = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeepNewestHits(t *testing.T) {
	t.Parallel()
	hit := func(entry uint64) MessageHit {
		return MessageHit{TranscriptKey: "k", Position: appwire.ThreadItemPosition{Entry: entry}}
	}
	t.Run("keeps hits newest first", func(t *testing.T) {
		t.Parallel()
		hits := keepNewestHits(nil, hit(3), 10)
		hits = keepNewestHits(hits, hit(1), 10)
		hits = keepNewestHits(hits, hit(2), 10)
		if len(hits) != 3 || hits[0].Position.Entry != 3 || hits[1].Position.Entry != 2 || hits[2].Position.Entry != 1 {
			t.Fatalf("hits = %+v, want newest first", hits)
		}
	})
	t.Run("drops a hit beyond the limit", func(t *testing.T) {
		t.Parallel()
		hits := keepNewestHits([]MessageHit{hit(2), hit(1)}, hit(3), 2)
		if len(hits) != 2 || hits[0].Position.Entry != 3 || hits[1].Position.Entry != 2 {
			t.Fatalf("hits = %+v, want the oldest dropped", hits)
		}
		hits = keepNewestHits(hits, hit(0), 2)
		if len(hits) != 2 || hits[1].Position.Entry != 2 {
			t.Fatalf("hits = %+v, want an older hit than the window refused", hits)
		}
	})
}

func TestTranscriptItemCursorStale(t *testing.T) {
	t.Parallel()
	if !transcriptItemCursorStale(appwire.TranscriptItemCursorStale()) {
		t.Fatal("the typed stale error is not recognized")
	}
	if transcriptItemCursorStale(appwire.InvalidParams("other")) {
		t.Fatal("another invalid-params error read as stale")
	}
	if transcriptItemCursorStale(errors.New("plain")) {
		t.Fatal("a plain error read as stale")
	}
}

func TestHasMessageSearchWord(t *testing.T) {
	t.Parallel()
	if hasMessageSearchWord(SearchTokens("a b !")) {
		t.Fatal("a single-letter and punctuation query counts as searchable")
	}
	if !hasMessageSearchWord(SearchTokens("to be")) {
		t.Fatal("a two-letter word is searchable")
	}
	if !hasMessageSearchWord(SearchTokens("42")) {
		t.Fatal("digits are word characters")
	}
	if got := wordRuneCount("ab1"); got != 3 {
		t.Fatalf("wordRuneCount = %d, want 3", got)
	}
}

// TestUnsupportedTranscriptHeader pins that a transcript a reader cannot open
// answers the history-failed error rather than a doomed repair ask.
func TestUnsupportedTranscriptHeader(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s1.transcript.jsonl")
	if err := os.WriteFile(path, []byte("neither json nor a transcript header\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckTranscriptHeader(path); err == nil {
		t.Fatal("an unopenable transcript passed the header check")
	}
	writeTestTranscript(t, path, "s1", settleTurns()...)
	if err := CheckTranscriptHeader(path); err != nil {
		t.Fatalf("a format-2 transcript failed the header check: %v", err)
	}
}
