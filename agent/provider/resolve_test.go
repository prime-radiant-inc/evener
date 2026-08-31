package provider

import (
	"strings"
	"testing"
)

// TestResolveUnknownInstanceNamesTheAvailableOnes pins that Resolve reports
// the registry's own reference errors verbatim: naming an instance nobody
// configured lists the ones that exist, and an empty model half is a bad
// reference rather than a lookup miss.
func TestResolveUnknownInstanceNamesTheAvailableOnes(t *testing.T) {
	r := fixtureRegistry(t)
	_, err := Resolve(r, "nope/model")
	if err == nil || !strings.Contains(err.Error(), `unknown instance "nope"`) || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("want the registry's unknown-instance error, got %v", err)
	}
	if _, err := Resolve(r, "anthropic/"); err == nil || !strings.Contains(err.Error(), "empty model reference") {
		t.Fatalf("an empty model half is a bad reference, got %v", err)
	}
	// A bare model resolves on the default instance, so the ref the CLI
	// rejects the registry still accepts.
	p, err := Resolve(r, "claude-opus-5")
	if err != nil {
		t.Fatalf("bare model on the default instance: %v", err)
	}
	if p.ID() == "" || p.Model() != "claude-opus-5" {
		t.Fatalf("bare model resolved to %s/%s", p.ID(), p.Model())
	}
}

// TestResolveCodexAllowlist pins §7.3's one exception: the Codex transport
// serves a fixed roster, so an id off it fails to resolve at all.
func TestResolveCodexAllowlist(t *testing.T) {
	r := fixtureRegistry(t)
	if _, err := Resolve(r, "openai-codex/gpt-5.6"); err != nil {
		t.Fatalf("an allowlisted Codex model resolves without a credential: %v", err)
	}
	_, err := Resolve(r, "openai-codex/not-on-the-allowlist")
	if err == nil || !strings.Contains(err.Error(), "unknown model on the Codex transport") {
		t.Fatalf("want the Codex allowlist error, got %v", err)
	}
}
