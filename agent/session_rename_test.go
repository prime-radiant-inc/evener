package agent

import "testing"

// TestRenameSetsUserSourceAndSurvivesCompaction proves Session.Rename sets a
// user-chosen title with NameSource="user" and that neither auto-namer can
// overwrite it afterward (round-2 A8). No ShouldNameFromCompactionForTest seam
// exists, so this asserts the suppression directly against the two real gates:
// shouldNameFromCompaction (the compaction-namer launch gate) and
// shouldApplySessionName (the locked shouldApplySessionNameLocked predicate's
// self-locking wrapper, gating the actual name write in nameSessionFromText).
// Both already reject any source that is not "prompt"/"compaction", so a
// "user" source falls out as suppressed for free.
func TestRenameSetsUserSourceAndSurvivesCompaction(t *testing.T) {
	sess := newTestSession(t)
	sess.Rename("my chosen title")
	m := sess.Meta()
	if m.Name != "my chosen title" || m.NameSource != "user" {
		t.Fatalf("rename should set Name + NameSource=user, got %+v", m)
	}
	// A compaction-derived name must NOT overwrite a user rename.
	if sess.shouldNameFromCompaction() {
		t.Fatal("user-named session must not accept a compaction name")
	}
	if sess.shouldApplySessionName(sessionNameSourceCompaction) {
		t.Fatal("compaction-sourced name must not apply over a user rename")
	}
	if sess.shouldApplySessionName(sessionNameSourcePrompt) {
		t.Fatal("prompt-sourced name must not apply over a user rename")
	}
}
