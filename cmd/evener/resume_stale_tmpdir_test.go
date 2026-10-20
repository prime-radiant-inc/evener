package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// A resume that outlives the temp container it was started in must still work:
// the hub that restores it can carry a TMPDIR whose evener-sandbox-* directory
// is gone, and every os.MkdirTemp("") under that process then fails. Reproduced
// live as "evener serve: restore session: create artifact store: stat
// /private/tmp/evener-sandbox-N/tmp: no such file or directory".
func TestRunResumeSucceedsWhenInheritedTMPDIRIsGone(t *testing.T) {
	// Every directory this test needs is created before TMPDIR goes dead: a
	// later t.TempDir() would otherwise create beneath the dead value.
	base := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(base, "gone", "tmp"))
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldEnsure := runEnsureUserConfigDirs
	t.Cleanup(func() { runEnsureUserConfigDirs = oldEnsure })
	runEnsureUserConfigDirs = func() error { return nil }

	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-test",
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	deps := defaultMainDeps()
	deps.args = []string{"--dir", stateDir, "--state-dir", stateDir, "--resume", sessionID}
	deps.stdin = bytes.NewReader(nil)
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	deps.stderr = &stderr
	deps.stdinMode = func() (os.FileMode, error) { return os.ModeCharDevice, nil }
	exitCode := -1
	deps.exit = func(code int) { exitCode = code }
	// The production entry point is runWithScratchReclaim, whose sweep bases on
	// the process temp dir. The repair under test clears the rig's private
	// TMPDIR, so that sweep would walk the host's real /tmp and could reclaim
	// other sessions' containers. This test is about resume, not the sweep.
	deps.run = run

	mainWithDeps(deps)
	if exitCode != -1 {
		t.Fatalf("resume exited %d: a resume must not fail because an inherited TMPDIR is gone\nstderr:\n%s", exitCode, stderr.String())
	}
	// A completed restore opens the session's transcript; a failure to build
	// one leaves the file absent.
	if _, statErr := os.Stat(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")); statErr != nil {
		t.Fatalf("resume restored no session (no transcript): %v\nstderr:\n%s", statErr, stderr.String())
	}
}
