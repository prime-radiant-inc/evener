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
	prompt, warning := s.renderSystemPrompt(s.env)
	if warning != "" {
		t.Fatalf("render failed: %s", warning)
	}
	want, err := commitprovenance.Trailer(BuildVersion, s.profile.ID()+"/"+s.profile.Model(), host, s.ID())
	if err != nil {
		t.Fatalf("build trailer: %v", err)
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
	prompt, warning := s.renderSystemPrompt(s.env)
	if warning != "" {
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

func TestSystemPromptWarnsOnInvalidCommitProvenance(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, CommitProvenance: "bogus"}))
	prompt, warning := s.renderSystemPrompt(s.env)
	if warning == "" {
		t.Fatal("invalid mode rendered without a diagnostic")
	}
	if strings.Contains(prompt, "Co-authored-by: Evener") {
		t.Fatal("invalid mode still rendered a trailer")
	}
}
