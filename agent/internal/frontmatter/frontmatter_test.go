package frontmatter

import (
	"reflect"
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
	doc, err := Parse(raw)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if doc.Meta != nil {
		t.Errorf("Meta should be nil on error, got %v", doc.Meta)
	}
	if doc.Body != "" {
		t.Errorf("Body should be empty on error, got %q", doc.Body)
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("error should mention frontmatter, got %q", err.Error())
	}
}

// Issue #4698: yaml.Unmarshal reads only the first YAML document, so anything
// after a document-end marker vanished without an error. A control field after
// "..." silently fell back to its default. Parse must reject trailing content
// rather than truncate it.
func TestParse_RejectsContentAfterDocumentEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw string }{
		{
			"trailing control field",
			"---\nname: probe\ndescription: \"LEGACY: sample\"\n...\nuser-invocable: false\n---\nBody.\n",
		},
		{
			"trailing garbage",
			"---\nname: probe\n...\n[\n---\nBody.\n",
		},
		{
			"second document",
			"---\nname: probe\n...\n--- # another document\nname: other\n---\nBody.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := Parse(tc.raw)
			if err == nil {
				t.Fatalf("Parse accepted content after the document end: %#v", doc.Meta)
			}
			if !reflect.DeepEqual(doc, Document{}) {
				t.Errorf("Parse error returned non-zero Document: %#v", doc)
			}
			if !strings.Contains(err.Error(), "frontmatter") {
				t.Errorf("error should mention frontmatter, got %q", err.Error())
			}
		})
	}
}

// A terminal "..." ends the document harmlessly: it follows every field and
// nothing but comments or blank lines comes after it. Parse keeps accepting
// that shape.
func TestParse_AcceptsTerminalDocumentEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, raw string }{
		{"bare terminal end", "---\nname: probe\n...\n---\nBody.\n"},
		{"terminal end with comment", "---\nname: probe\n... # end\n---\nBody.\n"},
		{"terminal end then comment line", "---\nname: probe\n...\n# final comment\n---\nBody.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := Parse(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if doc.Meta["name"] != "probe" {
				t.Errorf("name = %#v, want probe", doc.Meta["name"])
			}
			if doc.Body != "Body.\n" {
				t.Errorf("Body = %q, want %q", doc.Body, "Body.\n")
			}
		})
	}
}

// Issue #4701: yaml.v3's duplicate-key check compares an alias node's anchor
// name ("key") instead of the scalar it resolves to ("user-invocable"), so a
// mapping that declares a resolved key twice decodes without error and the
// later value silently overwrites the earlier one. Parse must reject the
// duplicate before the decode loses one of the values.
func TestParse_RejectsDuplicateResolvedKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, block string }{
		{
			"alias duplicate user control",
			"name: fixture\ndescription: 'LEGACY: fixture'\nmetadata: &key user-invocable\n*key: 'false'\nuser-invocable: true\n",
		},
		{
			"alias duplicate model control",
			"name: fixture\ndescription: 'LEGACY: fixture'\nmetadata: &key disable-model-invocation\n*key: 'true'\ndisable-model-invocation: false\n",
		},
		{
			"alias duplicate allowed tools",
			"name: fixture\ndescription: 'LEGACY: fixture'\nmetadata: &key allowed-tools\n*key: [true]\nallowed-tools: [read_file]\n",
		},
		{
			"anchored duplicate key",
			"name: fixture\ndescription: 'LEGACY: fixture'\n&key user-invocable: 'false'\n*key: true\n",
		},
		{
			"encoded duplicate control",
			"name: fixture\ndescription: 'LEGACY: fixture'\n!!binary dXNlci1pbnZvY2FibGU=: 'false'\nuser-invocable: true\n",
		},
		{
			"nested alias duplicate",
			"name: fixture\ndescription: 'LEGACY: fixture'\nmetadata:\n  key: &key nested\n  *key: one\n  nested: two\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := Parse("---\n" + tc.block + "---\nBODY\n")
			if err == nil {
				t.Fatalf("Parse accepted duplicate resolved keys: %#v", doc.Meta)
			}
			if !reflect.DeepEqual(doc, Document{}) {
				t.Errorf("Parse error returned non-zero Document: %#v", doc)
			}
		})
	}
}

// Rejecting duplicates must not reject YAML's legitimate alias and merge
// forms: an alias key that appears once, and a merge key that folds another
// mapping in without declaring its keys here.
func TestParse_PreservesAliasAndMergeSemantics(t *testing.T) {
	t.Parallel()
	doc, err := Parse("---\nname: fixture\ndescription: 'LEGACY: fixture'\nmetadata: {user-key: &user user-invocable}\n*user: false\n---\nBODY\n")
	if err != nil {
		t.Fatalf("alias key rejected: %v", err)
	}
	if doc.Meta["user-invocable"] != false {
		t.Errorf("user-invocable = %#v, want false", doc.Meta["user-invocable"])
	}

	doc, err = Parse("---\nbase: &base\n  a: 1\nmerged:\n  <<: *base\n  a: 2\n---\nBODY\n")
	if err != nil {
		t.Fatalf("merge key rejected: %v", err)
	}
	merged, ok := doc.Meta["merged"].(map[string]any)
	if !ok {
		t.Fatalf("merged = %#v, want map[string]any", doc.Meta["merged"])
	}
	if merged["a"] != 2 {
		t.Errorf("merged[a] = %#v, want 2", merged["a"])
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

func TestParse_DelimiterInBody(t *testing.T) {
	// A second "---\n" in the body must not be treated as a closing delimiter.
	// Parse must use the FIRST whole-line occurrence.
	raw := "---\nname: t\n---\nbody\n---\nnot-fm\n"
	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Meta["name"] != "t" {
		t.Errorf("name = %q, want %q", doc.Meta["name"], "t")
	}
	want := "body\n---\nnot-fm\n"
	if doc.Body != want {
		t.Errorf("Body = %q, want %q", doc.Body, want)
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
