package agent

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/commitprovenance"
)

func TestSystemPromptCarriesCommitTrailer(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	s := newSession(t, withDir(dir), withEnvironmentSnapshot(), withConfig(SessionConfig{MaxSubagentDepth: 1}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed: %s", warning)
	}
	want, ok := commitTrailerFor(s.profile.ID()+"/"+s.profile.Model(), s.ID())
	if !ok {
		t.Skip("this host cannot mint a commit trailer")
	}
	if !strings.Contains(prompt, want) {
		t.Fatalf("system prompt does not carry the provenance trailer %q", want)
	}
	envStart := strings.Index(prompt, "<environment>\n")
	envEnd := strings.Index(prompt, "\n</environment>")
	if envStart < 0 || envEnd <= envStart {
		t.Fatalf("system prompt missing <environment> block:\n%s", prompt)
	}
	if !strings.Contains(prompt[envStart:envEnd], "<environment_context>") {
		t.Fatalf("<environment> block missing <environment_context> update reference:\n%s", prompt[envStart:envEnd])
	}
}

func TestSystemPromptCarriesCommitTrailerOutsideGitRepo(t *testing.T) {
	s := newSession(t, withDir(t.TempDir()), withEnvironmentSnapshot(), withConfig(SessionConfig{MaxSubagentDepth: 1}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed: %s", warning)
	}
	want, ok := commitTrailerFor(s.profile.ID()+"/"+s.profile.Model(), s.ID())
	if !ok {
		t.Skip("this host cannot mint a commit trailer")
	}
	if !strings.Contains(prompt, want) {
		t.Fatalf("system prompt outside a git repo missing provenance trailer %q", want)
	}
	if strings.Contains(prompt, "<git>") {
		t.Fatal("system prompt outside a git repo should omit <git> snapshot")
	}

	off := newSession(t, withDir(t.TempDir()), withEnvironmentSnapshot(), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		CommitProvenance: commitprovenance.ModeOff,
	}))
	offPrompt, warning, renderFailed := off.renderSystemPrompt(off.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed (off): %s", warning)
	}
	if strings.Contains(offPrompt, "Co-authored-by: Evener") || strings.Contains(offPrompt, "## Commit provenance") {
		t.Fatal("system prompt carries commit provenance outside a git repo when mode is off")
	}
}

// commitTrailerFor returns the trailer this host can mint for session, or
// ok=false when it cannot mint one: no usable hostname, or a field the trailer
// syntax rejects.
func commitTrailerFor(model, session string) (string, bool) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "", false
	}
	trailer, err := commitprovenance.Trailer(BuildVersion, model, host, session)
	return trailer, err == nil
}

func TestSystemPromptOmitsCommitTrailerWhenOff(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	s := newSession(t, withDir(dir), withEnvironmentSnapshot(), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		CommitProvenance: commitprovenance.ModeOff,
	}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed: %s", warning)
	}
	if strings.Contains(prompt, "Co-authored-by: Evener") {
		t.Fatal("system prompt carries a provenance trailer while the mode is off")
	}
}

func TestSessionConfigCommitProvenanceSnapshotRoundTrip(t *testing.T) {
	cfg := SessionConfig{CommitProvenance: commitprovenance.ModeOff}
	snap := cfg.toSnapshot()
	if snap.CommitProvenance != commitprovenance.ModeOff {
		t.Fatalf("snapshot = %q, want off", snap.CommitProvenance)
	}
	if got := configFromSnapshot(snap).CommitProvenance; got != commitprovenance.ModeOff {
		t.Fatalf("round trip = %q, want off", got)
	}
}

func TestSessionConfigCommitProvenanceApplyDefaults(t *testing.T) {
	cfg := SessionConfig{CommitProvenance: " off "}
	cfg.applyDefaults()
	if cfg.CommitProvenance != commitprovenance.ModeOff {
		t.Fatalf("normalized mode = %q, want off", cfg.CommitProvenance)
	}
	empty := SessionConfig{}
	empty.applyDefaults()
	if empty.CommitProvenance != "" {
		t.Fatalf("unset mode = %q, want empty default", empty.CommitProvenance)
	}
}

func TestSystemPromptWarnsOnInvalidCommitProvenance(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	s := newSession(t, withDir(dir), withEnvironmentSnapshot(), withConfig(SessionConfig{MaxSubagentDepth: 1, CommitProvenance: "bogus"}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatal("configuration diagnostic used the prompt-render failure channel")
	}
	if s.commitProvenanceWarning() == "" {
		t.Fatal("invalid mode produced no configuration diagnostic")
	}
	if strings.Contains(prompt, "Co-authored-by: Evener") {
		t.Fatal("invalid mode still rendered a trailer")
	}
}
