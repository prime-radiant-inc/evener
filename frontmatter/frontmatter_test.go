package frontmatter

import (
	"strings"
	"testing"
)

func TestParse_ValidFrontmatter(t *testing.T) {
	raw := "---\nname: my-skill\ndescription: \"A test skill\"\n---\n# Instructions\nDo things.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta == nil {
		t.Fatal("Meta should not be nil")
	}
	if doc.Meta["name"] != "my-skill" {
		t.Errorf("name = %q, want %q", doc.Meta["name"], "my-skill")
	}
	if doc.Meta["description"] != "A test skill" {
		t.Errorf("description = %q, want %q", doc.Meta["description"], "A test skill")
	}
	want := "# Instructions\nDo things.\n"
	if doc.Body != want {
		t.Errorf("Body = %q, want %q", doc.Body, want)
	}
}

func TestParse_NoFrontmatter(t *testing.T) {
	raw := "# Just Markdown\nNo frontmatter here.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta != nil {
		t.Errorf("Meta should be nil, got %v", doc.Meta)
	}
	if doc.Body != raw {
		t.Errorf("Body should be the full input")
	}
}

func TestParse_EmptyFrontmatter(t *testing.T) {
	raw := "---\n---\nBody here.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta == nil {
		t.Fatal("Meta should not be nil for empty frontmatter")
	}
	if len(doc.Meta) != 0 {
		t.Errorf("Meta should be empty, got %v", doc.Meta)
	}
	if doc.Body != "Body here.\n" {
		t.Errorf("Body = %q, want %q", doc.Body, "Body here.\n")
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	raw := "---\n: bad: yaml: [unclosed\n---\nBody.\n"
	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParse_NoClosingDelimiter(t *testing.T) {
	raw := "---\nname: test\nThis never closes.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No closing delimiter means it's treated as no frontmatter.
	if doc.Meta != nil {
		t.Errorf("Meta should be nil when no closing delimiter, got %v", doc.Meta)
	}
	if doc.Body != raw {
		t.Errorf("Body should be the full input")
	}
}

func TestParse_ComplexMetadata(t *testing.T) {
	raw := "---\nname: complex\ntags:\n  - go\n  - yaml\nnested:\n  key: value\n---\nBody.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta["name"] != "complex" {
		t.Errorf("name = %q, want %q", doc.Meta["name"], "complex")
	}
	tags, ok := doc.Meta["tags"].([]any)
	if !ok {
		t.Fatalf("tags should be []any, got %T", doc.Meta["tags"])
	}
	if len(tags) != 2 {
		t.Errorf("tags length = %d, want 2", len(tags))
	}
	if tags[0] != "go" {
		t.Errorf("tags[0] = %q, want %q", tags[0], "go")
	}
	if tags[1] != "yaml" {
		t.Errorf("tags[1] = %q, want %q", tags[1], "yaml")
	}
	nested, ok := doc.Meta["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested should be map[string]any, got %T", doc.Meta["nested"])
	}
	if nested["key"] != "value" {
		t.Errorf("nested.key = %q, want %q", nested["key"], "value")
	}
}

func TestParse_BodyPreserved(t *testing.T) {
	// Verify leading/trailing whitespace in body is preserved exactly.
	raw := "---\nname: test\n---\n\n  indented\n\ntrailing\n\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "\n  indented\n\ntrailing\n\n"
	if doc.Body != want {
		t.Errorf("Body = %q, want %q", doc.Body, want)
	}
}

// A "---" glued to the end of a value is not a closing delimiter.
func TestParse_ValueEndingInDashesDoesNotCloseTheBlock(t *testing.T) {
	raw := "---\nname: t\nnote: see---\n---\nBody.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta["note"] != "see---" {
		t.Errorf("note = %q, want %q", doc.Meta["note"], "see---")
	}
	if doc.Body != "Body.\n" {
		t.Errorf("Body = %q, want %q", doc.Body, "Body.\n")
	}
}

// An indented "---" inside a block scalar is content, not a closing delimiter.
func TestParse_IndentedDelimiterInBlockScalarDoesNotClose(t *testing.T) {
	raw := "---\ndescription: |\n  line\n  ---\n  more\nname: t\n---\nBody.\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta["name"] != "t" {
		t.Errorf("name = %q, want %q", doc.Meta["name"], "t")
	}
	description, ok := doc.Meta["description"].(string)
	if !ok || !strings.Contains(description, "---") {
		t.Errorf("description = %#v, want a string containing the indented ---", doc.Meta["description"])
	}
	if doc.Body != "Body.\n" {
		t.Errorf("Body = %q, want %q", doc.Body, "Body.\n")
	}
}

func TestSplit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw, block, body string
		ok                     bool
	}{
		{"complete", "---\na: 1\n---\nbody\n", "a: 1\n", "body\n", true},
		{"no opening", "body\n", "", "body\n", false},
		{"no closing", "---\na: 1\n", "", "---\na: 1\n", false},
		{"empty block", "---\n---\nbody\n", "", "body\n", true},
		{"--- ending a value is not the closing delimiter", "---\ndescription: a---\nb: 1\n---\nbody\n", "description: a---\nb: 1\n", "body\n", true},
		{"--- ending a value with no closing line", "---\ndescription: a---\nbody\n", "", "---\ndescription: a---\nbody\n", false},
		{"--- with no newline at the end is not a closing delimiter", "---\na: 1\n---", "", "---\na: 1\n---", false},
		{"CRLF line endings read as newlines", "---\r\na: 1\r\n---\r\nbody\r\n", "a: 1\n", "body\n", true},
		{"lone carriage returns read as newlines", "---\ra: 1\r---\rbody\r", "a: 1\n", "body\n", true},
		{"an unframed document comes back with newline endings", "body\r\nmore\r", "", "body\nmore\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			block, body, ok := Split(tc.raw)
			if block != tc.block || body != tc.body || ok != tc.ok {
				t.Fatalf("Split=%q,%q,%v want %q,%q,%v", block, body, ok, tc.block, tc.body, tc.ok)
			}
		})
	}
}

func TestParse_EmptyInput(t *testing.T) {
	doc, err := Parse("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta != nil {
		t.Errorf("Meta should be nil for empty input")
	}
	if doc.Body != "" {
		t.Errorf("Body should be empty")
	}
}

func TestParse_OnlyDelimiters(t *testing.T) {
	// Just "---\n---\n" with nothing else.
	raw := "---\n---\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta == nil {
		t.Fatal("Meta should not be nil")
	}
	if len(doc.Meta) != 0 {
		t.Errorf("Meta should be empty, got %v", doc.Meta)
	}
	if doc.Body != "" {
		t.Errorf("Body should be empty, got %q", doc.Body)
	}
}
