package agent

import (
	"testing"
)

// SessionStart names the sandbox request the session started under, so a
// subagent's thread can report its own access (S15).
func TestSessionStartNamesTheSandboxRequest(t *testing.T) {
	networkOff := false
	dir := t.TempDir()
	session := newSession(t, withDir(dir), withConfig(SessionConfig{
		NonInteractive: true,
		StateDir:       dir,
		Sandbox:        "read-only",
		SandboxNet:     &networkOff,
		testOnly:       testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	start := queuedSessionStart(t, session)
	if start.Sandbox != "read-only" || start.SandboxNet == nil || *start.SandboxNet {
		t.Fatalf("SessionStart sandbox = %q network %v, want read-only with the network off", start.Sandbox, start.SandboxNet)
	}
}
