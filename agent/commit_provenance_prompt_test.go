package agent

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/commitprovenance"
)

func TestSystemPromptCarriesCommitTrailer(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("no hostname available")
	}

	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed: %s", warning)
	}
	want, err := commitprovenance.Trailer(BuildVersion, s.profile.ID()+"/"+s.profile.Model(), host, s.ID())
	if err != nil {
		t.Skipf("hostname %q is not usable in a trailer: %v", host, err)
	}
	if !strings.Contains(prompt, want) {
		t.Fatalf("system prompt does not carry the provenance trailer %q", want)
	}
}

func TestSystemPromptOmitsCommitTrailerWhenOff(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{
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
	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, CommitProvenance: "bogus"}))
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
