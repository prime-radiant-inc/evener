package frontmatter

import (
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
