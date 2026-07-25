package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// writeUsageTranscript writes a transcript whose Nth entry carries the Nth
// usage triple, so a test can name the exact tokens it expects a range to sum.
// A nil triple writes an entry with no usage at all (the shape a USER_INPUT or
// TOOL_RESULTS entry really has on disk).
func writeUsageTranscript(t testing.TB, usages []*llm.Usage) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.transcript.jsonl")
	header := transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: "usage"}
	records := []any{header}
	for i, u := range usages {
		turn := schema.Turn{
			Kind:      schema.TurnAssistant,
			Message:   llm.Assistant("turn"),
			Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC(),
		}
		if u != nil {
			turn.Usage = *u
		} else {
			turn.Kind = schema.TurnUserInput
			turn.Message = llm.User("turn")
		}
		records = append(records, transcript.Entry{Kind: "entry", Seq: i + 1, Turn: turn})
	}
	var data []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func usage(in, out, total, cacheRead int) *llm.Usage {
	u := llm.Usage{InputTokens: in, OutputTokens: out, TotalTokens: total}
	if cacheRead != 0 {
		u.CacheReadTokens = &cacheRead
	}
	return &u
}

func requireUsageTotalFromFile(t testing.TB, cache *TurnCache, path string, maxLineBytes, fromEntryOrdinal int) *appwire.SerfUsage {
	t.Helper()
	got, err := cache.UsageTotalFromFile(path, maxLineBytes, fromEntryOrdinal)
	if err != nil {
		t.Fatalf("UsageTotalFromFile: %v", err)
	}
	return got
}

// The whole point of the full sum: it covers turns no windowed read ever
// loaded. Ordinal 0 means "no divergence cut", i.e. the entire transcript.
func TestUsageTotalFromFileSumsEveryEntryInTheTranscript(t *testing.T) {
	path := writeUsageTranscript(t, []*llm.Usage{
		usage(100, 10, 110, 40),
		nil,
		usage(200, 20, 220, 80),
		nil,
		usage(300, 30, 330, 0),
	})

	got := requireUsageTotalFromFile(t, NewTurnCache(), path, testMaxLineBytes, 0)
	want := &appwire.SerfUsage{InputTokens: 600, OutputTokens: 60, TotalTokens: 660, CacheReadTokens: 120}
	if got == nil || *got != *want {
		t.Fatalf("usage total = %+v, want %+v", got, want)
	}
}

// A fork's child transcript OPENS with a verbatim copy of the parent's prefix.
// Those tokens were spent by the parent, so counting them into the child's
// total would attribute another session's spend to this one. fromEntryOrdinal
// is the child's SessionMeta.DivergenceTurn: the 1-based entry ordinal where
// the child's own history begins.
func TestUsageTotalFromFileSkipsTheInheritedForkPrefix(t *testing.T) {
	path := writeUsageTranscript(t, []*llm.Usage{
		usage(100, 10, 110, 40), // parent's, entry 1
		usage(200, 20, 220, 80), // parent's, entry 2
		nil,                     // the child's own diverging input, entry 3
		usage(300, 30, 330, 16), // the child's own, entry 4
	})

	got := requireUsageTotalFromFile(t, NewTurnCache(), path, testMaxLineBytes, 3)
	want := &appwire.SerfUsage{InputTokens: 300, OutputTokens: 30, TotalTokens: 330, CacheReadTokens: 16}
	if got == nil || *got != *want {
		t.Fatalf("post-divergence total = %+v, want %+v (never the parent's prefix)", got, want)
	}
}

// An aside fork's child holds ONLY the inherited prefix until it is opened and
// run, so its own span is empty. An empty span has no total to report, and the
// honest answer for "no measurement" is absent - never a zero that renders as
// a real "0 tokens" figure.
func TestUsageTotalFromFileReportsAbsentForAnEmptyOwnSpan(t *testing.T) {
	path := writeUsageTranscript(t, []*llm.Usage{
		usage(100, 10, 110, 40),
		usage(200, 20, 220, 80),
	})

	if got := requireUsageTotalFromFile(t, NewTurnCache(), path, testMaxLineBytes, 3); got != nil {
		t.Fatalf("usage total = %+v, want nil (an unopened fork has spent nothing)", got)
	}
}

// A transcript whose entries carry no usage at all (every provider field
// unset) reports absent rather than an all-zero SerfUsage, matching
// appwire.SerfUsageFromLLM's own absent-vs-zero rule.
func TestUsageTotalFromFileReportsAbsentWhenNoEntryCarriesUsage(t *testing.T) {
	path := writeUsageTranscript(t, []*llm.Usage{nil, nil, nil})

	if got := requireUsageTotalFromFile(t, NewTurnCache(), path, testMaxLineBytes, 0); got != nil {
		t.Fatalf("usage total = %+v, want nil (no token data anywhere)", got)
	}
}

// The sum is memoized on the same file-identity gate the turn cache already
// uses for parses, so repeatedly reading one session's thread does not rescan
// its transcript. Proven by counting scans through the read observer.
func TestUsageTotalFromFileMemoizesByFileIdentity(t *testing.T) {
	path := writeUsageTranscript(t, []*llm.Usage{usage(100, 10, 110, 0)})
	cache := NewTurnCache()

	var scans int
	restore := InstallReadObserverForTesting(func(stats ReadStats) {
		if stats.usageScans > 0 {
			scans += int(stats.usageScans)
		}
	})
	t.Cleanup(restore)

	first := requireUsageTotalFromFile(t, cache, path, testMaxLineBytes, 0)
	second := requireUsageTotalFromFile(t, cache, path, testMaxLineBytes, 0)
	if first == nil || second == nil || *first != *second {
		t.Fatalf("memoized total differs: %+v vs %+v", first, second)
	}
	if scans != 1 {
		t.Fatalf("transcript usage scans = %d, want 1 (second read served from the memo)", scans)
	}
}

// Appending to a transcript changes its size and mtime, which must invalidate
// the memo: a session that ran another turn has a larger total.
func TestUsageTotalFromFileRescansAfterTheTranscriptGrows(t *testing.T) {
	path := writeUsageTranscript(t, []*llm.Usage{usage(100, 10, 110, 0)})
	cache := NewTurnCache()

	before := requireUsageTotalFromFile(t, cache, path, testMaxLineBytes, 0)
	if before == nil || before.InputTokens != 100 {
		t.Fatalf("initial total = %+v, want InputTokens=100", before)
	}

	extra, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: 2, Turn: schema.Turn{
		Kind: schema.TurnAssistant, Message: llm.Assistant("more"),
		Timestamp: time.Unix(1_700_000_100, 0).UTC(),
		Usage:     llm.Usage{InputTokens: 400, OutputTokens: 40, TotalTokens: 440},
	}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(extra, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// A same-second append can leave size as the only observable change; the
	// identity gate keys on size too, so this is deterministic.

	after := requireUsageTotalFromFile(t, cache, path, testMaxLineBytes, 0)
	want := &appwire.SerfUsage{InputTokens: 500, OutputTokens: 50, TotalTokens: 550}
	if after == nil || *after != *want {
		t.Fatalf("total after append = %+v, want %+v", after, want)
	}
}

// A transcript in an unreadable or unsupported shape must surface the error
// rather than reporting a made-up total. Callers treat an error as "unknown".
func TestUsageTotalFromFilePropagatesUnsupportedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := NewTurnCache().UsageTotalFromFile(path, testMaxLineBytes, 0)
	if err == nil || got != nil {
		t.Fatalf("UsageTotalFromFile = (%+v, %v), want (nil, error) for a v1 transcript", got, err)
	}
}

func TestUsageTotalFromFileReportsMissingTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.transcript.jsonl")

	got, err := NewTurnCache().UsageTotalFromFile(path, testMaxLineBytes, 0)
	if err == nil || got != nil {
		t.Fatalf("UsageTotalFromFile = (%+v, %v), want (nil, error) for a missing transcript", got, err)
	}
}
