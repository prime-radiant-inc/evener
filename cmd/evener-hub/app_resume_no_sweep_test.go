package hub

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestResumeRequestForConfigDoesNotSweepThePastIndex pins the cost contract of
// issue #645: building the resume request for a session the index has not
// seen must not decode every meta on disk. Before the targeted Find probe, a
// miss ran a full Rebuild synchronously inside the user-visible resume
// window (glob + decode of every session meta — 2,428 on a real machine).
//
// The proof: a Find for an unindexed session that HITS the probe (the meta
// exists on disk) must leave a different, concurrently persisted session
// unindexed — a full Rebuild would have swept it in.
func TestResumeRequestForConfigDoesNotSweepThePastIndex(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-resume-0000000000")
	targetID := "034GO1hl5aYRUROrrymZ2u"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:        targetID,
		ProfileID: "openai",
		Model:     "gpt-4o",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: t.TempDir()},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	// A second session lands on disk AFTER the index was built — the
	// situation whose full-Rebuild cost the resume window used to pay.
	otherID := "034GO1hl5ae7iTrbJfle3q"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:        otherID,
		ProfileID: "openai",
		Model:     "gpt-4o",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: t.TempDir()},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// A third session is what the resume actually asks for: unindexed, so
	// Find's miss path fires, but present on disk so the probe surfaces it.
	resumeID := "034GO1hl5agahWyK3OWOQ6"
	if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
		ID:        resumeID,
		ProfileID: "openai",
		Model:     "gpt-4o",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: t.TempDir()},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	req, err := resumeRequestForConfig(hubcore.WebConfig{Past: past}, resumeID)
	if err != nil {
		t.Fatalf("resumeRequestForConfig: %v", err)
	}
	if req.StateDir != proj || req.Provider != "openai" {
		t.Fatalf("resume request = %+v, want the probed session's state dir and provider", req)
	}

	if all := past.All(); len(all) != 2 {
		t.Fatalf("index holds %d entries after the resume-window Find, want 2 (the one it was built with + the resumed session); the miss path must be a targeted probe, not a full rebuild that sweeps in every meta on disk", len(all))
	}
	// The other concurrently persisted session must still be absent — a
	// full rebuild would have indexed it as a side effect. Assert through
	// the index snapshot rather than a second Find, which would itself
	// probe (and legitimately fold) the session.
	for _, e := range past.All() {
		if e.ID == otherID {
			t.Fatal("the resume window swept an unrelated session into the index; the miss path must be a targeted probe, not a full rebuild")
		}
	}
}
