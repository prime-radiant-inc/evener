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
		"when": "When code stores, computes or prints a money amount: any of them",
		"body": "# Money is integer cents\n\nMoney is integer cents.\n\n**Why:** a rounding bug.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nwhen: 'When code stores, computes or prints a money amount: any of them'\n---\n\n# Money is integer cents\n\nMoney is integer cents.\n\n**Why:** a rounding bug.\n"
	if page != want {
		t.Fatalf("got %q\nwant %q", page, want)
	}
	p := parseMemoryPage("cents.md", []byte(page), time.Time{})
	if p.Unreadable || p.When != "When code stores, computes or prints a money amount: any of them" || p.Title != "Money is integer cents" {
		t.Fatalf("page reads back as %+v", p)
	}
}

func TestMemoryPageFromFieldsRequiresWhen(t *testing.T) {
	for _, when := range []any{nil, "", "  \n"} {
		args := map[string]any{"body": "# T\nfact\n"}
		if when != nil {
			args["when"] = when
		}
		if _, err := memoryPageFromFields(args); err == nil || !strings.Contains(err.Error(), "when") {
			t.Fatalf("when %q: err = %v, want one naming when", when, err)
		}
	}
}

func TestMemoryPageFromFieldsRefusesFrontmatterInBody(t *testing.T) {
	for _, body := range []string{"---\ndescription: d\n---\nfact\n", "\n---\nwhen: w\n---\n"} {
		_, err := memoryPageFromFields(map[string]any{"when": "w", "body": body})
		if err == nil || !strings.Contains(err.Error(), "field") {
			t.Fatalf("body %q: err = %v, want one pointing at the field", body, err)
		}
	}
	if _, err := memoryPageFromFields(map[string]any{"when": "w", "body": "# T\nfact\n\n---\n\nmore\n"}); err != nil {
		t.Fatalf("a rule later in the body was refused: %v", err)
	}
}

// With the fields switch on, memory_write takes when and body, writes the
// assembled page stamped as today with no missing-description note, the page
// indexes by its title and when line, and a refused call writes nothing.
func TestMemoryWriteFieldsWritesStampedPage(t *testing.T) {
	t.Setenv(memoryWriteFieldsEnv, "on")
	t.Setenv(memoryWhenEnv, "only")
	s, scope := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "cents.md", "when": "When code handles money", "body": "# Money is integer cents\n\nMoney is integer cents.\n"})
	if res.IsError || strings.Contains(res.Output, "description") {
		t.Fatalf("%+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "cents.md"))
	want := "---\nwhen: When code handles money\n" + memoryOwnStamps(s) + "---\n\n# Money is integer cents\n\nMoney is integer cents.\n"
	if string(raw) != want {
		t.Fatalf("got %q\nwant %q", raw, want)
	}
	if got := memoryIndexLine(parseMemoryPage("cents.md", raw, time.Time{})); !strings.HasPrefix(got, "- [Money is integer cents](cents.md) — When code handles money (updated ") {
		t.Fatalf("index line %q", got)
	}
	res = memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "x.md", "when": "w", "body": "---\ndescription: d\n---\n"})
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
		_, hasDescription := props["description"]
		if hasWhen != on || hasContent == on || hasDescription {
			t.Fatalf("switch %v: when=%v content=%v description=%v", on, hasWhen, hasContent, hasDescription)
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
