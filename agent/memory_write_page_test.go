package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/memorypages"
)

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
	page := memorypages.ParsePage("cents.md", raw, time.Time{})
	if got := memorypages.IndexLine(page); !strings.HasPrefix(got, "- [cents.md](cents.md) — When code handles money (updated ") {
		t.Fatalf("index line %q", got)
	}
	if strings.Contains(page.Description, memorypages.NoDescription) {
		t.Fatalf("a page with a when is marked %q: %q", memorypages.NoDescription, page.Description)
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
