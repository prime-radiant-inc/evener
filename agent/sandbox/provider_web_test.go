package sandbox

import (
	"strings"
	"testing"
)

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
