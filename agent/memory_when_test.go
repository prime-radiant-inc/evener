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
