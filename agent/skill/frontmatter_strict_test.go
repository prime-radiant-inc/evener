package skill

import (
	"path/filepath"
	"slices"
	"testing"
)

// Issue #4701: the strict parse path (valid YAML, so no description recovery)
// accepted a mapping whose resolved key appeared twice through an alias. The
// literal control won and the invalid string one vanished, so a skill that
// declared user-invocable twice was advertised. Parse must reject it.
func TestParseRejectsDuplicateResolvedAliasKey(t *testing.T) {
	t.Parallel()
	source := "---\nname: fixture\ndescription: 'LEGACY: fixture'\nmetadata: &key user-invocable\n*key: 'false'\nuser-invocable: true\n---\nBODY\n"
	descriptor, diagnostics, err := Parse([]byte(source), filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil || !descriptor.Unavailable {
		t.Fatalf("duplicate alias key accepted: unavailable=%v err=%v controls=%+v", descriptor.Unavailable, err, descriptor.Controls)
	}
	if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Category == "invalid_frontmatter" }) {
		t.Errorf("diagnostics = %+v, want invalid_frontmatter", diagnostics)
	}
}

// Issue #4698: user-invocable declared after a document-end marker used to be
// silently ignored, so an absent control defaulted to true. Parse must reject
// the truncated document instead of advertising the skill.
func TestParseRejectsControlAfterDocumentEnd(t *testing.T) {
	t.Parallel()
	source := "---\nname: fixture\ndescription: 'LEGACY: fixture'\n...\nuser-invocable: false\n---\nBODY\n"
	descriptor, diagnostics, err := Parse([]byte(source), filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil || !descriptor.Unavailable {
		t.Fatalf("trailing control accepted: unavailable=%v err=%v controls=%+v", descriptor.Unavailable, err, descriptor.Controls)
	}
	if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Category == "invalid_frontmatter" }) {
		t.Errorf("diagnostics = %+v, want invalid_frontmatter", diagnostics)
	}
}
