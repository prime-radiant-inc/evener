package frontmatter

import (
	"math"
	"reflect"
	"testing"

	"primeradiant.com/serf/fuzz/edgeseeds"
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
	// Generic YAML-frontmatter decoder stressors (anchors, merge keys, the
	// boolean/null/number coercion zoo, framing-boundary probes, …).
	for _, s := range edgeseeds.FrontmatterYAML() {
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

		// Determinism. Use a NaN-aware equality: YAML's .nan decodes to a
		// float64 NaN, and reflect.DeepEqual(NaN, NaN) is false even though the
		// parse is perfectly deterministic — so a plain DeepEqual would
		// false-flag any input that yields a NaN-valued field.
		doc2, err2 := Parse(raw)
		if err2 != nil || doc.Body != doc2.Body || !yamlValueEqual(doc.Meta, doc2.Meta) {
			t.Fatalf("Parse not deterministic for %q", raw)
		}
	})
}

// yamlValueEqual is reflect.DeepEqual extended to treat two NaN float64 values
// as equal, recursing through the map / slice shapes a yaml.v3 decode produces.
func yamlValueEqual(a, b any) bool {
	switch av := a.(type) {
	case float64:
		bv, ok := b.(float64)
		if !ok {
			return false
		}
		if math.IsNaN(av) && math.IsNaN(bv) {
			return true
		}
		return av == bv
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			vb, ok := bv[k]
			if !ok || !yamlValueEqual(va, vb) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !yamlValueEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
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
