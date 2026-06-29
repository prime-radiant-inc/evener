package frontmatter

import (
	"strings"
	"testing"
)

// FuzzParse drives the real frontmatter.Parse seam over arbitrary documents.
// The oracle is floor "no panic" plus the one structural invariant Parse always
// guarantees: the returned Body is a suffix of the raw document, because Parse
// only ever strips a leading delimiter…YAML…delimiter prefix and never rewrites
// the body. (Note Meta == nil is NOT equivalent to "no frontmatter": a present
// frontmatter block whose YAML is null — e.g. "---\n!---\n" — also nils the meta
// map without error, so a Meta-based body invariant would be unsound.)
func FuzzParse(f *testing.F) {
	f.Add("---\ntitle: hi\n---\nbody text\n")
	f.Add("---\n---\nempty meta\n")
	f.Add("no frontmatter at all\n")
	f.Add("---\nunterminated frontmatter\n")
	f.Add("---\n: : :\n---\nbad yaml key\n")
	f.Add("---\n[1,2,3]\n---\nsequence not map\n")
	f.Add("")
	f.Add("---\n")
	f.Add("------\n")

	f.Fuzz(func(t *testing.T, raw string) {
		doc, err := Parse(raw)
		if err != nil {
			// The only error path is malformed frontmatter YAML; nothing else to
			// assert about a rejected document.
			return
		}

		if !strings.HasSuffix(raw, doc.Body) {
			t.Fatalf("Body is not a suffix of the input:\n input=%q\n body=%q", raw, doc.Body)
		}
	})
}
