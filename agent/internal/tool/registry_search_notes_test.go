package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/searchresult"
	"primeradiant.com/evener/llm"
)

// Keeping a prefix of the notes instead of dropping their whole suffix must
// fail this test. The real grep producer supplies the note, not copied prose.
func TestToolRegistry_GrepCharacterCapDropsWholeNotes(t *testing.T) {
	t.Parallel()
	// The issue reported sixteen reachable sizes. Include that whole window,
	// the wider window with today's producer, and its neighboring boundaries.
	for size := 19_999; size <= 20_024; size++ {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "matches.go")
			env := execenv.NewLocalExecutionEnvironment(dir)
			rows := make([]string, 198)
			for i := range rows {
				rows[i] = "x"
			}
			grep := func(ctx context.Context) (string, error) {
				// A skip predicate selects the real deterministic native reader,
				// independent of whether ripgrep is installed on the host.
				return env.GrepSkipping(ctx, "x", path, "", false, 197, "content", 0, func(string) bool { return false })
			}
			writeRows := func() {
				t.Helper()
				if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeRows()
			baseline, err := grep(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			rows[0] += strings.Repeat("x", size-utf8.RuneCountInString(baseline))
			writeRows()
			raw, err := grep(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			entries, notes := searchresult.Split(raw)
			if utf8.RuneCountInString(raw) != size || notes == "" || searchresult.EntryCount(raw) != 197 {
				t.Fatalf("invalid fixture: size=%d, entries=%d, notes=%q", utf8.RuneCountInString(raw), searchresult.EntryCount(raw), notes)
			}
			reg := NewRegistry()
			limit := defaultToolLimit("grep")
			if err := reg.Register(RegisteredTool{
				Definition: llm.ToolDefinition{Name: "grep", Description: "Search the fixture"},
				Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, _ map[string]any) (any, error) {
					return grep(ctx)
				},
				Limit: limit,
			}); err != nil {
				t.Fatal(err)
			}
			got := reg.ExecuteCall(t.Context(), env, llm.ToolCallData{ID: "grep-note", Name: "grep", Arguments: json.RawMessage(`{}`)})
			if got.IsError || got.FullOutput != raw || got.RecoverableOutput != raw {
				t.Fatal("dispatch failed or original output was not preserved")
			}
			if size <= limit.MaxChars {
				if got.Truncated || got.Output != raw {
					t.Fatalf("fitting result changed: truncated=%v", got.Truncated)
				}
				return
			}
			shown, suffix := searchresult.Split(got.Output)
			if !got.Truncated || shown != entries {
				t.Fatalf("entry prefix changed or truncation not reported: truncated=%v, shown=%d runes, entries=%d runes", got.Truncated, utf8.RuneCountInString(shown), utf8.RuneCountInString(entries))
			}
			if blocks := strings.Split(suffix, "\n\n"); len(blocks) != 1 {
				t.Fatalf("source note survived the character cut: notes=%q", suffix)
			}
			marker, _, _ := strings.Cut(suffix, "\n") // The registry indicator follows the character marker.
			if n := utf8.RuneCountInString(shown + "\n\n" + marker); n > limit.MaxChars {
				t.Fatalf("character cutter exceeded cap: %d > %d", n, limit.MaxChars)
			}
			assertRemovedRunes(t, marker, size-utf8.RuneCountInString(shown))
		})
	}
}

// The prefix must stop before the notes even if the marker's count grows a
// digit, the separator is only partly in budget, or the notes contain blocks.
func TestTruncateCharsFromTail_AtomicNotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		entries string
		notes   string
		limit   int
		kept    int
	}{
		{name: "cut before separator", entries: strings.Repeat("x", 105), notes: strings.Repeat("N", 80), limit: 160, kept: 103},
		{name: "cut at separator", entries: strings.Repeat("x", 103), notes: strings.Repeat("N", 80), limit: 160, kept: 103},
		{name: "cut inside separator", entries: strings.Repeat("x", 102), notes: strings.Repeat("N", 80), limit: 160, kept: 102},
		{name: "cut inside unbracketed note", entries: strings.Repeat("x", 90), notes: strings.Repeat("N", 80), limit: 160, kept: 90},
		{name: "multiple note blocks", entries: strings.Repeat("x", 90), notes: strings.Repeat("N", 40) + "\n" + strings.Repeat("Q", 40) + "\n\n" + strings.Repeat("R", 40), limit: 160, kept: 90},
		{name: "notes only", notes: strings.Repeat("N", 200), limit: 80, kept: 0},
		{name: "unicode entries and notes", entries: strings.Repeat("界", 90), notes: strings.Repeat("語", 80), limit: 160, kept: 90},
		{name: "removed count gains digits", entries: strings.Repeat("x", 100), notes: strings.Repeat("N", 98), limit: 199, kept: 100},
		{name: "no notes", entries: strings.Repeat("x", 100), limit: 80, kept: 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := tc.entries
			if tc.notes != "" {
				source = searchresult.WithNotes(tc.entries, tc.notes)
			}
			got, truncated := truncateCharsFromTailWithStatus(source, tc.limit)
			shown, marker := searchresult.Split(got)
			wantPrefix := string([]rune(tc.entries)[:tc.kept])
			if !truncated || shown != wantPrefix {
				t.Fatalf("wrong source prefix: truncated=%v, shown=%q, want=%q", truncated, shown, wantPrefix)
			}
			if strings.Contains(marker, "\n") {
				t.Fatalf("a source note or separator fragment survived: %q", marker)
			}
			if n := utf8.RuneCountInString(got); n > tc.limit {
				t.Fatalf("output exceeded character cap: %d > %d", n, tc.limit)
			}
			assertRemovedRunes(t, marker, utf8.RuneCountInString(source)-tc.kept)
		})
	}
}

func TestTruncateCharsFromTail_FittingNotesRemainWhole(t *testing.T) {
	t.Parallel()
	source := searchresult.WithNotes("a.go:1:x", "SENTINEL-NOTE", "SECOND-NOTE")
	for _, limit := range []int{-1, 0, utf8.RuneCountInString(source), utf8.RuneCountInString(source) + 1} {
		got, truncated := truncateCharsFromTailWithStatus(source, limit)
		if truncated || got != source {
			t.Fatalf("limit %d changed a fitting result: truncated=%v, got=%q", limit, truncated, got)
		}
	}
}

func TestTruncateHeadCount_CharacterCutDropsLineSummary(t *testing.T) {
	t.Parallel()
	// A large line count makes the summary wider than the character marker.
	// Cutting just one rune off the assembled result used to keep its "[".
	source := strings.Repeat("x\n", 9_999) + "x"
	assembled, _ := truncateHeadCountWithStatus(source, 5_000, 0)
	got, truncated := truncateHeadCountWithStatus(source, 5_000, utf8.RuneCountInString(assembled)-1)
	shown, marker := searchresult.Split(got)
	wantEntries := strings.Repeat("x\n", 4_999) + "x"
	if !truncated || shown != wantEntries || strings.Contains(marker, "\n") {
		t.Fatalf("partial line summary survived: truncated=%v, shown=%d runes, notes=%q", truncated, utf8.RuneCountInString(shown), marker)
	}
	assertRemovedRunes(t, marker, utf8.RuneCountInString(assembled)-utf8.RuneCountInString(wantEntries))
}

// The marker's numeric field, not its English wording, describes the cut.
func assertRemovedRunes(t *testing.T, marker string, want int) {
	t.Helper()
	fields := strings.FieldsFunc(marker, func(r rune) bool { return !unicode.IsDigit(r) })
	if len(fields) != 1 {
		t.Fatalf("marker must describe one removal count: %q", marker)
	}
	got, err := strconv.Atoi(fields[0])
	if err != nil || got != want {
		t.Fatalf("removed runes = %s (%v), want %d, marker=%q", fields[0], err, want, marker)
	}
	if !strings.HasPrefix(marker, "[") || !strings.HasSuffix(marker, "]") {
		t.Fatalf("marker is not complete: %q", marker)
	}
}
