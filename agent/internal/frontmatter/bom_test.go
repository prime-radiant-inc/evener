package frontmatter

import "testing"

// An editor may leave a UTF-8 byte-order mark at the very start of a file. Only
// the skill parser trimmed it, so a BOM-prefixed plugin agent, plugin command,
// plugin settings file, memory page or prompt_data agent file read as having no
// frontmatter at all. Split normalizes line endings for every caller; it now
// drops a leading BOM on the same terms.
func TestSplitTrimsALeadingBOM(t *testing.T) {
	raw := "\ufeff---\nname: probe\n---\nBODY\n"

	block, body, ok := Split(raw)
	if !ok {
		t.Fatal("Split reported no frontmatter for a BOM-prefixed document")
	}
	if block != "name: probe\n" {
		t.Errorf("block = %q, want %q", block, "name: probe\n")
	}
	if body != "BODY\n" {
		t.Errorf("body = %q, want %q", body, "BODY\n")
	}

	doc, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Meta == nil || doc.Meta["name"] != "probe" {
		t.Fatalf("Meta = %#v, want the frontmatter parsed", doc.Meta)
	}
	if doc.Body != "BODY\n" {
		t.Errorf("Body = %q, want %q", doc.Body, "BODY\n")
	}
}

// The framing must still be reported for the shapes the skill parser
// distinguishes, so it stops re-deriving the rule from the body.
func TestParseReportsFraming(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Framing
	}{
		{"complete", "---\nname: probe\n---\nBODY\n", Framed},
		{"complete with BOM", "\ufeff---\nname: probe\n---\nBODY\n", Framed},
		{"no opening delimiter", "name: probe\n---\nBODY\n", NoOpeningDelimiter},
		{"no closing delimiter", "---\nname: probe\nBODY\n", NoClosingDelimiter},
		{"two BOMs, one dropped", "\ufeff\ufeff---\nname: probe\n---\nBODY\n", NoOpeningDelimiter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse(tc.raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if doc.Framing != tc.want {
				t.Fatalf("Framing = %v, want %v", doc.Framing, tc.want)
			}
		})
	}
}

// The BOM is framing, not content: an unframed document comes back without it,
// like a line-ending difference, and a document Parse rejected carries the zero
// framing rather than claiming it found no opening delimiter.
func TestUnframedBodyDropsTheBOM(t *testing.T) {
	block, body, ok := Split("\ufeffno frontmatter here\n")
	if ok {
		t.Fatal("Split reported frontmatter for a BOM-prefixed plain document")
	}
	if block != "" {
		t.Errorf("block = %q, want empty", block)
	}
	if body != "no frontmatter here\n" {
		t.Errorf("body = %q, want the BOM dropped", body)
	}

	doc, err := Parse("\ufeff")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Body != "" || doc.Meta != nil || doc.Framing != NoOpeningDelimiter {
		t.Fatalf("BOM-only document = %+v, want an empty body, nil Meta and NoOpeningDelimiter", doc)
	}

	doc, err = Parse("---\n: : not valid yaml :\n---\nbody\n")
	if err == nil {
		t.Fatal("expected a YAML error")
	}
	if doc.Framing != FramingUnknown {
		t.Errorf("rejected document framing = %v, want the zero FramingUnknown", doc.Framing)
	}
}
