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

// The unquoted colon makes the strict parse fail, so this source drives the
// description-recovery path past its own guards — the quoted description above
// is rejected there before the shared key check runs. Recovery must still
// reject the duplicate resolved key; without that check the recovered mapping
// decodes (yaml compares the alias's anchor name, not the key it resolves to)
// and the skill is advertised with the literal control.
func TestParseRecoveryRejectsDuplicateResolvedAliasKey(t *testing.T) {
	t.Parallel()
	source := "---\nname: fixture\ndescription: LEGACY: fixture\nmetadata: &key user-invocable\n*key: 'false'\nuser-invocable: true\n---\nBODY\n"
	descriptor, diagnostics, err := Parse([]byte(source), filepath.Join(t.TempDir(), "SKILL.md"))
	if err == nil || !descriptor.Unavailable {
		t.Fatalf("recovered duplicate alias key accepted: unavailable=%v err=%v controls=%+v", descriptor.Unavailable, err, descriptor.Controls)
	}
	if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Category == "invalid_frontmatter" }) {
		t.Errorf("diagnostics = %+v, want invalid_frontmatter", diagnostics)
	}
}

// Control for the case above: the same recoverable shape succeeds when the
// alias key does not collide, so the duplicate is what recovery rejects
// rather than some other guard in the recovery path.
func TestParseRecoveryAcceptsDistinctAliasKey(t *testing.T) {
	t.Parallel()
	source := "---\nname: fixture\ndescription: LEGACY: fixture\nmetadata: &key user-invocable\n*key: false\n---\nBODY\n"
	descriptor, diagnostics, err := Parse([]byte(source), filepath.Join(t.TempDir(), "SKILL.md"))
	if err != nil || descriptor.Unavailable {
		t.Fatalf("distinct alias key rejected: unavailable=%v err=%v diagnostics=%+v", descriptor.Unavailable, err, diagnostics)
	}
	if descriptor.Controls.UserInvocable {
		t.Errorf("user-invocable = %v, want false", descriptor.Controls.UserInvocable)
	}
	if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Category == "unquoted_description" }) {
		t.Errorf("diagnostics = %+v, want unquoted_description", diagnostics)
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
