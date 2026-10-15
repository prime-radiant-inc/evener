package agent

import (
	"strings"
	"testing"
	"time"
)

func TestParseMemoryPageWhen(t *testing.T) {
	raw := "---\ndescription: Total is frozen\nwhen: \"Before changing   Total's\\nsignature\"\n---\n# Frozen\n"
	p := parseMemoryPage("frozen.md", []byte(raw), time.Time{})
	if want := "Before changing Total's signature"; p.When != want {
		t.Fatalf("When = %q, want %q", p.When, want)
	}
	long := "---\ndescription: d\nwhen: " + strings.Repeat("x", 500) + "\n---\n# T\n"
	if got := len([]rune(parseMemoryPage("l.md", []byte(long), time.Time{}).When)); got > memoryWhenRunes {
		t.Fatalf("when not bounded: %d runes", got)
	}
	if got := parseMemoryPage("n.md", []byte("---\ndescription: d\n---\n# T\n"), time.Time{}).When; got != "" {
		t.Fatalf("When = %q, want empty", got)
	}
}

func TestMemoryIndexLineWhenSwitch(t *testing.T) {
	p := memoryPage{Path: "a.md", Title: "A", Description: "desc", When: "Before X"}
	t.Setenv(memoryWhenEnv, "")
	if got := memoryIndexLine(p); got != "- [A](a.md) — desc" {
		t.Fatalf("off: %q", got)
	}
	t.Setenv(memoryWhenEnv, "both")
	if got := memoryIndexLine(p); got != "- [A](a.md) — desc | when: Before X" {
		t.Fatalf("both: %q", got)
	}
	p.When = ""
	if got := memoryIndexLine(p); got != "- [A](a.md) — desc" {
		t.Fatalf("both, no when: %q", got)
	}
	p.When = "Before X"
	t.Setenv(memoryWhenEnv, "only")
	if got := memoryIndexLine(p); got != "- [A](a.md) — Before X" {
		t.Fatalf("only: %q", got)
	}
	p.When = ""
	if got := memoryIndexLine(p); got != "- [A](a.md) — desc" {
		t.Fatalf("only, no when falls back to description: %q", got)
	}
}

func TestMemoryPromptAsksForWhenOnlyWhenSwitchedOn(t *testing.T) {
	// The write guidance lives in the system prompt template; render it both ways.
	for _, on := range []bool{false, true} {
		var b strings.Builder
		data := promptData{MemoryRead: true, MemorySaves: true, MemoryWhen: on}
		if err := systemPromptTemplate.Execute(&b, data); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(b.String(), "a one-line `when`"); got != on {
			t.Fatalf("MemoryWhen=%v: prompt has when guidance = %v", on, got)
		}
	}
}

// With the title switch off, a page's line links its path as the link text
// and keeps the rest of the line; memoryIndexLineFor still finds it.
func TestMemoryIndexLineWithoutTitles(t *testing.T) {
	p := memoryPage{Path: "money/cents.md", Title: "Money is integer cents", Description: "desc", When: "When code handles money", Tags: []string{"money"}, Updated: "2026-10-12"}
	t.Setenv(memoryWhenEnv, "only")
	t.Setenv(memoryIndexTitleEnv, "off")
	line := memoryIndexLine(p)
	if want := "- [money/cents.md](money/cents.md) — When code handles money [money] (updated 2026-10-12)"; line != want {
		t.Fatalf("got %q, want %q", line, want)
	}
	if !memoryIndexLineFor(line, "money/cents.md") {
		t.Fatalf("memoryIndexLineFor does not find %q", line)
	}
	t.Setenv(memoryIndexTitleEnv, "")
	if got := memoryIndexLine(p); !strings.HasPrefix(got, "- [Money is integer cents](money/cents.md) — ") {
		t.Fatalf("titles on: %q", got)
	}
}

// The off-scope cue puts the page's scope in its link text; memoryIndexLineFor
// still finds the line by its path.
func TestMemoryIndexLineScopeCue(t *testing.T) {
	t.Setenv(memoryWhenEnv, "only")
	t.Setenv(memoryIndexTitleEnv, "off-scope")
	p := memoryPage{Path: "shop/api-frozen.md", Title: "T", When: "When changing shop's exports", Scope: "project"}
	line := memoryIndexLine(p)
	if want := "- [project:shop/api-frozen.md](shop/api-frozen.md) — When changing shop's exports"; line != want {
		t.Fatalf("got %q, want %q", line, want)
	}
	if !memoryIndexLineFor(line, "shop/api-frozen.md") {
		t.Fatalf("memoryIndexLineFor does not find %q", line)
	}
}

// The off-note cue opens the index with one line saying what the links are;
// it is not a page line, and the projection counts it against the cap.
func TestMemoryIndexNoteCue(t *testing.T) {
	t.Setenv(memoryWhenEnv, "only")
	pages := []memoryPage{{Path: "a.md", Title: "A", When: "When A"}}
	t.Setenv(memoryIndexTitleEnv, "off-note")
	content, full, _ := projectMemoryIndex(pages, memoryProjectionCap)
	if !strings.HasPrefix(content, memoryIndexNote+"\n") || !strings.HasPrefix(full, memoryIndexNote+"\n") {
		t.Fatalf("index does not open with the note: %q", content)
	}
	if got := memoryIndexLines(content); len(got) != 1 || !strings.HasPrefix(got[0], "- [a.md](a.md) — ") {
		t.Fatalf("page lines = %q", got)
	}
	t.Setenv(memoryIndexTitleEnv, "off")
	if content, _, _ := projectMemoryIndex(pages, memoryProjectionCap); strings.Contains(content, memoryIndexNote) {
		t.Fatalf("plain off has the note: %q", content)
	}
}
