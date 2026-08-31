package sandbox

import (
	"strings"
	"testing"
)

// TestProviderWebRegistryKnowsRegistryProviderIDs pins the table's keys as
// registry provider ids (spec §7.5): every vendor that runs server-side web
// egress must be listed under the id the registry resolves for it, so the
// net=off decision is made on vendor identity rather than on the instance
// name or the prompt surface.
func TestProviderWebRegistryKnowsRegistryProviderIDs(t *testing.T) {
	for _, id := range []string{"openai", "openai-codex", "anthropic", "google"} {
		egress, known := WebEgress(id)
		if !known {
			t.Errorf("provider id %q must be known to the table", id)
		}
		if !egress {
			t.Errorf("provider id %q must be recorded as egress-capable", id)
		}
	}
	// "gemini" was the old behavior tag; google is the registry provider id.
	if _, known := WebEgress("gemini"); known {
		t.Errorf("the retired gemini tag must not be a table key")
	}
}

func TestProviderWebRegistryFailsClosed(t *testing.T) {
	// A KNOWN egress-capable provider is refused under net=off.
	if ProviderWebAllowedUnderNetOff("openai") {
		t.Errorf("a known egress-capable provider must be refused under net=off")
	}
	if _, known := WebEgress("openai"); !known {
		t.Errorf("openai must be a known provider")
	}
	// An UNKNOWN provider is ALSO refused (fail closed) — net=off can never be
	// silently false through a provider whose web capability we cannot inspect.
	egress, known := WebEgress("some-new-provider")
	if known {
		t.Errorf("an unlisted provider must report known=false")
	}
	if !egress {
		t.Errorf("an unknown provider must be treated as egress-capable (fail closed)")
	}
	if ProviderWebAllowedUnderNetOff("some-new-provider") {
		t.Errorf("an unknown provider must be refused under net=off (fail closed)")
	}
}

func TestEnforcementLine(t *testing.T) {
	// off announces nothing.
	if got := EnforcementLine(ResolvedPolicy{Mode: ModeOff}); got != "" {
		t.Errorf("off must produce no enforcement line, got %q", got)
	}
	rp := ResolvedPolicy{Mode: ModeWorkspaceWrite, Network: false, Backend: BackendBwrap, CacheStrategy: CacheSessionPrivate}
	line := EnforcementLine(rp)
	for _, want := range []string{"bwrap", "enforcing workspace-write", "network off", "secrets masked", "cache private"} {
		if !strings.Contains(line, want) {
			t.Errorf("enforcement line %q must mention %q", line, want)
		}
	}
	// The old CLI-flag/cache-jargon must be gone: plain words only.
	if strings.Contains(line, "--sandbox") || strings.Contains(line, "session-private") || strings.Contains(line, "overlay") {
		t.Errorf("enforcement line %q must not use flag/cache jargon", line)
	}
	overlay := EnforcementLine(ResolvedPolicy{Mode: ModeWorkspaceWrite, Network: true, Backend: BackendBwrap, CacheStrategy: CacheOverlay})
	if !strings.Contains(overlay, "network on") || !strings.Contains(overlay, "secrets masked") || !strings.Contains(overlay, "cache shared-read/private-write") {
		t.Errorf("enforcement line %q must reflect net on + secrets masked + shared-read/private-write cache", overlay)
	}
}
