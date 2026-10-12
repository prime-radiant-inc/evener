package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryPageFromFieldsAssemblesFrontmatter(t *testing.T) {
	page, err := memoryPageFromFields(map[string]any{
		"description": "Money is integer cents: never floats",
		"when":        "When code stores, computes or prints a money amount",
		"tags":        []any{"money", "types"},
		"evidence":    "partner, 2026-10-12",
		"body":        "Money is integer cents.\n\n**Why:** a rounding bug.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ndescription: 'Money is integer cents: never floats'\nwhen: When code stores, computes or prints a money amount\ntags:\n    - money\n    - types\nevidence: partner, 2026-10-12\n---\n\nMoney is integer cents.\n\n**Why:** a rounding bug.\n"
	if page != want {
		t.Fatalf("got %q\nwant %q", page, want)
	}
	p := parseMemoryPage("cents.md", []byte(page), time.Time{})
	if p.Unreadable || p.Description != "Money is integer cents: never floats" || p.When != "When code stores, computes or prints a money amount" || strings.Join(p.Tags, ",") != "money,types" {
		t.Fatalf("page reads back as %+v", p)
	}
	bare, err := memoryPageFromFields(map[string]any{"description": "d", "when": "w", "body": "b\n"})
	if err != nil {
		t.Fatal(err)
	}
	if bare != "---\ndescription: d\nwhen: w\n---\n\nb\n" {
		t.Fatalf("optional fields not omitted: %q", bare)
	}
}

func TestMemoryPageFromFieldsRequiresDescriptionAndWhen(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"no description":    {"when": "w", "body": "b"},
		"blank description": {"description": "  ", "when": "w", "body": "b"},
		"no when":           {"description": "d", "body": "b"},
		"blank when":        {"description": "d", "when": "\n", "body": "b"},
	} {
		field := "description"
		if strings.Contains(name, "when") {
			field = "when"
		}
		if _, err := memoryPageFromFields(args); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("%s: err = %v, want one naming %s", name, err, field)
		}
	}
}

func TestMemoryPageFromFieldsRefusesFrontmatterInBody(t *testing.T) {
	for _, body := range []string{"---\ndescription: d\n---\nfact\n", "\n---\nwhen: w\n---\n"} {
		_, err := memoryPageFromFields(map[string]any{"description": "d", "when": "w", "body": body})
		if err == nil || !strings.Contains(err.Error(), "fields") {
			t.Fatalf("body %q: err = %v, want one pointing at the fields", body, err)
		}
	}
	if _, err := memoryPageFromFields(map[string]any{"description": "d", "when": "w", "body": "fact\n\n---\n\nmore\n"}); err != nil {
		t.Fatalf("a rule later in the body was refused: %v", err)
	}
}

// With the fields switch on, memory_write takes the fields, writes the
// assembled page stamped as today, and a refused call writes nothing.
func TestMemoryWriteFieldsWritesStampedPage(t *testing.T) {
	t.Setenv(memoryWriteFieldsEnv, "on")
	s, scope := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "cents.md", "description": "Money is integer cents", "when": "When code handles money", "body": "Money is integer cents.\n"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "cents.md"))
	want := "---\ndescription: Money is integer cents\nwhen: When code handles money\n" + memoryOwnStamps(s) + "---\n\nMoney is integer cents.\n"
	if string(raw) != want {
		t.Fatalf("got %q\nwant %q", raw, want)
	}
	res = memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "x.md", "description": "d", "when": "w", "body": "---\ndescription: d\n---\n"})
	if !res.IsError {
		t.Fatalf("frontmatter body accepted: %q", res.Output)
	}
	if _, err := os.Stat(filepath.Join(scope, "x.md")); !os.IsNotExist(err) {
		t.Fatalf("refused write left a file: %v", err)
	}
}

func TestMemoryWriteDefinitionFollowsFieldsSwitch(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Setenv(memoryWriteFieldsEnv, map[bool]string{false: "", true: "on"}[on])
		props := memoryWriteDefinition().Parameters["properties"].(map[string]any)
		_, hasWhen := props["when"]
		_, hasContent := props["content"]
		if hasWhen != on || hasContent == on {
			t.Fatalf("switch %v: when=%v content=%v", on, hasWhen, hasContent)
		}
	}
}

// Under the fields switch the field guidance lives in the tool's parameters,
// so the prompt drops its frontmatter sentence instead of repeating it.
func TestMemoryPromptLeavesFieldGuidanceToTheTool(t *testing.T) {
	for _, fields := range []bool{false, true} {
		var b strings.Builder
		data := promptData{MemoryRead: true, MemorySaves: true, MemoryWhen: true, MemoryWriteFields: fields}
		if err := systemPromptTemplate.Execute(&b, data); err != nil {
			t.Fatal(err)
		}
		prompt := b.String()
		if got := strings.Contains(prompt, "Start it with frontmatter"); got == fields {
			t.Fatalf("fields=%v: frontmatter sentence present = %v", fields, got)
		}
		if got := strings.Contains(prompt, "its fields say what each part holds"); got != fields {
			t.Fatalf("fields=%v: fields pointer present = %v", fields, got)
		}
	}
}
