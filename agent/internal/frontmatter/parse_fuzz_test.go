package frontmatter

import (
	"reflect"
	"testing"
)

// FuzzFrontmatterParse drives Parse — the package's real YAML-frontmatter decode
// seam (yaml.Unmarshal of the fenced block). Input is an arbitrary Markdown
// document. Beyond no-panic it asserts the documented contract: a document with
// no frontmatter framing (no leading delimiter, or a leading delimiter with no
// closing one) is returned verbatim as Body with nil Meta; a framed document
// splits Body exactly at the inner boundary the framing defines. Parsing is
// deterministic (a second parse matches the first).
//
// Note: when framing IS present, Meta may still come back nil if the YAML body
// parses to a null/empty mapping (e.g. "---\n!---\n"), so the contract keys on
// framing, not on Meta-nilness.
func FuzzFrontmatterParse(f *testing.F) {
	seeds := []string{
		"---\ntitle: hi\ntags: [a, b]\n---\nbody text\n",
		"---\n---\njust body\n",
		"no frontmatter here",
		"---\nunterminated frontmatter\n",
		"---\n: : : not valid yaml :\n---\nbody",
		"",
		"---\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		doc, err := Parse(raw)
		if err != nil {
			// A parse error must leave the zero Document.
			if !reflect.DeepEqual(doc, Document{}) {
				t.Fatalf("Parse error returned non-zero Document: %#v", doc)
			}
			return
		}

		// Framing contract: an unframed document is returned verbatim with nil
		// Meta; a framed one splits Body exactly at the inner boundary.
		const delim = "---\n"
		if idx := indexAfter(raw); idx < 0 {
			if doc.Meta != nil || doc.Body != raw {
				t.Fatalf("unframed document not returned verbatim:\n in  =%q\n meta=%#v\n body=%q", raw, doc.Meta, doc.Body)
			}
		} else {
			wantBody := raw[len(delim)+idx+len(delim):]
			if doc.Body != wantBody {
				t.Fatalf("framed document body mismatch:\n in  =%q\n want=%q\n got =%q", raw, wantBody, doc.Body)
			}
		}

		// Determinism.
		doc2, err2 := Parse(raw)
		if err2 != nil || !reflect.DeepEqual(doc, doc2) {
			t.Fatalf("Parse not deterministic for %q", raw)
		}
	})
}

// indexAfter reports whether a closing "---\n" delimiter exists after the
// opening one, mirroring Parse's own framing check.
func indexAfter(raw string) int {
	const delim = "---\n"
	if len(raw) < len(delim) || raw[:len(delim)] != delim {
		return -1
	}
	rest := raw[len(delim):]
	for i := 0; i+len(delim) <= len(rest); i++ {
		if rest[i:i+len(delim)] == delim {
			return i
		}
	}
	return -1
}
