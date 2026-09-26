package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

// seedResumableSession writes a session meta and a two-entry transcript that
// a real --resume can restore.
func seedResumableSession(t *testing.T, stateDir, sessionID string, headerSessionID string) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-test",
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	// SaveSessionMeta creates <stateDir>/sessions.
	path := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID:  headerSessionID,
		ProfileID:  "openai",
		Model:      "gpt-test",
		WorkingDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, text := range []string{"first turn", "second turn"} {
		if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User(text))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
}

// serveResumeIdentityProbe records the app-identity preparation a --resume
// run made. Plain fields suffice: every write happens on the goroutine that
// called runServeWithDeps, before it returns, and the test reads the probe
// only after that return. The serve goroutines spawned along the way never
// touch the probe.
type serveResumeIdentityProbe struct {
	prepared    bool
	gotThreadID string
	gotPath     string
}

// runServeResumeWithProbe drives one --resume through runServeWithDeps and
// returns the probe's observations plus the serve error.
func runServeResumeWithProbe(t *testing.T, stateDir, sessionID string) (*serveResumeIdentityProbe, error) {
	t.Helper()
	probe := &serveResumeIdentityProbe{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	var cancel context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		cancel = stop
		return next, stop
	}
	prepare := deps.prepareAppIdentity
	deps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		probe.prepared, probe.gotThreadID, probe.gotPath = true, threadID, transcriptPath
		return prepare(sourceID, threadID, ref, transcriptPath)
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		// Cancel before returning: the shutdown goroutine waits on ctx.Done()
		// even after serveHTTP returns, so returning without canceling would
		// deadlock the test (same pattern as
		// TestRunResumeWithFailedReservationPreservesForeignOwnedChild).
		cancel()
		return http.ErrServerClosed
	}
	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--resume", sessionID,
		"--dir", t.TempDir(),
		"--state-dir", stateDir,
		"--run-dir", t.TempDir(),
		"--no-project-prompts",
	}
	serveErr := runServeWithDeps(args, deps)
	return probe, serveErr
}

// TestServeResumeServesTheRestoredTranscript pins the serve-level wiring of a
// --resume: the identity is prepared over the resumed session's own
// transcript, whose history the daemon then serves, and the run completes
// cleanly. Preparation reads only the transcript's header
// (server.TestTranscriptHeaderReadsOnlyLeadingHeader).
func TestServeResumeServesTheRestoredTranscript(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	seedResumableSession(t, stateDir, sessionID, sessionID)

	probe, serveErr := runServeResumeWithProbe(t, stateDir, sessionID)
	if serveErr != nil {
		t.Fatalf("runServeWithDeps(resume): %v", serveErr)
	}
	if !probe.prepared || probe.gotThreadID != sessionID {
		t.Fatalf("resume prepared %v for thread %q, want the resumed session %q", probe.prepared, probe.gotThreadID, sessionID)
	}
	if want := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"); probe.gotPath != want {
		t.Fatalf("resume prepared over %q, want the resumed transcript %q", probe.gotPath, want)
	}
}

// TestServeResumeForeignHeaderEntryListFailsStartup pins the error contract
// at the same serve-level boundary: a transcript whose header names another
// session fails daemon startup, rather than serving a conversation that never
// happened.
func TestServeResumeForeignHeaderEntryListFailsStartup(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	const foreignSessionID = "02wMz5Txv2enqVTitaig6F"
	// The transcript's header names a DIFFERENT session than the one being
	// resumed: restore's OpenWriterForSession must reject it before serve.
	seedResumableSession(t, stateDir, sessionID, foreignSessionID)

	probe, serveErr := runServeResumeWithProbe(t, stateDir, sessionID)
	if serveErr == nil {
		t.Fatal("serve accepted a resume over a transcript whose header names another session")
	}
	if probe.prepared {
		t.Fatal("identity preparation ran over a foreign-header transcript; restore should have failed first")
	}
	if !strings.Contains(serveErr.Error(), "restore session") {
		t.Fatalf("serve error = %v, want the restore-stage failure naming the mismatch", serveErr)
	}
}
