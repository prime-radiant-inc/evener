package activitybound

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateCapsOnRuneBoundaries(t *testing.T) {
	t.Parallel()
	within := strings.Repeat("é", 4)
	if got := Truncate(within, 4); got != within {
		t.Fatalf("within cap = %q, want %q", got, within)
	}
	long := strings.Repeat("界", 10)
	got := Truncate(long, 4)
	if got != strings.Repeat("界", 3)+"…" {
		t.Fatalf("over cap = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("truncated runes = %d, want 4", n)
	}
}

func TestReportPreviewMatchesTheReadRule(t *testing.T) {
	t.Parallel()
	runeLimit := MaxDelegateProseRunes
	cases := []struct {
		name      string
		message   json.RawMessage
		want      string
		truncated bool
	}{
		{"plain", json.RawMessage(`"report"`), "report", false},
		{"surrounding whitespace", json.RawMessage(" \n\t\"report\"\r\n "), "report", false},
		{"exact cap", json.RawMessage(`"` + strings.Repeat("界", runeLimit) + `"`), strings.Repeat("界", runeLimit), false},
		{"one beyond cap", json.RawMessage(`"` + strings.Repeat("界", runeLimit+1) + `"`), strings.Repeat("界", runeLimit-1) + "…", true},
		{"surrogate prefix cut", json.RawMessage(`"a` + strings.Repeat(`\ud83d\ude00`, runeLimit*10) + `"`), "a" + strings.Repeat("😀", runeLimit-2) + "…", true},
		{"leading whitespace before huge report", json.RawMessage(strings.Repeat(" ", 100) + `"` + strings.Repeat(`\ud83d\ude00`, runeLimit*10) + `"`), strings.Repeat("😀", runeLimit-1) + "…", true},
		{"empty", json.RawMessage(`""`), "", false},
		{"malformed escape", json.RawMessage(`"\q` + strings.Repeat("x", runeLimit*20) + `"`), "", false},
		{"unterminated short string", json.RawMessage(`"short`), "", false},
		{"structured result", json.RawMessage(`{"report":"text"}`), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := ReportPreview(tc.message)
			if got != tc.want || truncated != tc.truncated || !utf8.ValidString(got) {
				t.Fatalf("preview=%q truncated=%v, want %q truncated=%v", got, truncated, tc.want, tc.truncated)
			}
		})
	}
}

func TestBoundMessageBoundsTheWindow(t *testing.T) {
	t.Parallel()
	// Leading whitespace is trimmed; an oversized message keeps only the window.
	raw := json.RawMessage("   " + `"` + strings.Repeat("x", MaxReportPreviewBytes*2) + `"`)
	window, complete := BoundMessage(raw)
	if complete {
		t.Fatal("window reached the end of an oversized message")
	}
	if len(window) > MaxReportPreviewBytes {
		t.Fatalf("window = %d bytes, limit %d", len(window), MaxReportPreviewBytes)
	}
	if window[0] != '"' {
		t.Fatalf("leading whitespace not trimmed: %q", window[:1])
	}
	small := json.RawMessage(`"report"`)
	if window, complete = BoundMessage(small); !complete || string(window) != `"report"` {
		t.Fatalf("small window = %q complete=%v", window, complete)
	}
	if window, complete = BoundMessage(json.RawMessage("   ")); window != nil || complete {
		t.Fatalf("whitespace-only = %q complete=%v, want nil false", window, complete)
	}
}
