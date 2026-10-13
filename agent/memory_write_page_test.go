package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
)

func TestMemoryPageFromFieldsAssemblesFrontmatter(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	for _, when := range []any{nil, "", "  \n"} {
		args := map[string]any{"body": "# T\nfact\n"}
		if when != nil {
			args["when"] = when
		}
		_, err := memoryPageFromFields(args)
		if !errors.Is(err, errMemoryWriteNeedsWhen) {
			t.Fatalf("when %q: err = %v", when, err)
		}
	}
}

func TestMemoryPageFromFieldsRefusesFrontmatterInBody(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"---\ndescription: d\n---\nfact\n", "\n---\nwhen: w\n---\n"} {
		_, err := memoryPageFromFields(map[string]any{"when": "w", "body": body})
		if !errors.Is(err, errMemoryBodyFrontmatter) {
			t.Fatalf("body %q: err = %v", body, err)
		}
	}
	// A horizontal rule is not a frontmatter block, however it opens the body.
	for _, body := range []string{"# T\nfact\n\n---\n\nmore\n", "----\n# T\nfact\n", "---\n# T\nfact\n"} {
		if _, err := memoryPageFromFields(map[string]any{"when": "w", "body": body}); err != nil {
			t.Fatalf("body %q was refused: %v", body, err)
		}
	}
}

// memory_write takes when and body, writes the assembled page stamped as an
// edit stamps it, with no note about a description, the page indexes by its
// title and when line, and a refused call writes nothing.
func TestMemoryWriteWritesStampedPageFromFields(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "cents.md", "when": "When code handles money", "body": "# Money is integer cents\n\nMoney is integer cents.\n"})
	if res.IsError || strings.Contains(res.Output, "\n\n") {
		t.Fatalf("%+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "cents.md"))
	want := "---\nwhen: When code handles money\n" + memoryOwnStamps(s) + "---\n\n# Money is integer cents\n\nMoney is integer cents.\n"
	if string(raw) != want {
		t.Fatalf("got %q\nwant %q", raw, want)
	}
	page := parseMemoryPage("cents.md", raw, time.Time{})
	if got := memoryIndexLine(page); !strings.HasPrefix(got, "- [Money is integer cents](cents.md) — When code handles money (updated ") {
		t.Fatalf("index line %q", got)
	}
	if strings.Contains(page.Description, memoryNoDescription) {
		t.Fatalf("a page with a when is marked %q: %q", memoryNoDescription, page.Description)
	}
	for _, args := range []map[string]any{
		{"when": "w", "body": "---\ndescription: d\n---\n"},
		{"when": " ", "body": "# T\nfact\n"},
	} {
		args["scope"], args["file_path"] = "personal", "x.md"
		if res := memoryExec(t, s, "memory_write", args); !res.IsError {
			t.Fatalf("%v accepted: %q", args, res.Output)
		}
		if _, err := os.Stat(filepath.Join(scope, "x.md")); !os.IsNotExist(err) {
			t.Fatalf("refused write left a file: %v", err)
		}
	}
}

// Writing a page that exists replaces it whole.
func TestMemoryWriteFromFieldsReplacesThePage(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	for _, body := range []string{"# Old\n\nold fact\n", "# New\n"} {
		if res := memoryExec(t, s, "memory_write", map[string]any{"scope": "personal", "file_path": "a.md", "when": "When w", "body": body}); res.IsError {
			t.Fatal(res.Output)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(scope, "a.md"))
	if want := "---\nwhen: When w\n" + memoryOwnStamps(s) + "---\n\n# New\n"; string(raw) != want {
		t.Fatalf("got %q\nwant %q", raw, want)
	}
}

// memory_write's schema holds exactly scope, file_path, when and body, all
// required.
func TestMemoryWriteDefinitionHoldsOnlyFieldParameters(t *testing.T) {
	t.Parallel()
	params := tool.DefMemoryWrite().Parameters
	props := params["properties"].(map[string]any)
	if len(props) != 4 {
		t.Fatalf("properties = %v", props)
	}
	for _, name := range []string{"scope", "file_path", "when", "body"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("no %s property", name)
		}
	}
	if got := params["required"].([]string); len(got) != 4 {
		t.Fatalf("required = %v", got)
	}
}
