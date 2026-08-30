package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server")

// seedResumableSession writes a session meta and a two-entry transcript that
// a real --resume can restore: OpenWriterForSession will strict-decode the
// entries and RestoredTranscript will report them, so serve's identity
// preparation has a choice of forms to make.
func seedResumableSession(t *testing.T, stateDir, sessionID string, headerSessionID string) string {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-test",
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(sessionsDir, sessionID+".transcript.jsonl")
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
	return path
}

// serveResumeIdentityProbe records which app-identity preparation form a
// --resume run used. The serveHTTP override samples inside the live loop
// (same pattern as runClearAttempt) and ends it by returning
// http.ErrServerClosed, which runServeWithDeps treats as a clean stop.
type serveResumeIdentityProbe struct {
	mu sync.Mutex

	entriesFormUsed bool
	fileFormUsed     bool
	gotThreadID      string
	gotEntryCount    int
}

func (p *serveResumeIdentityProbe) recordEntries(threadID string, entryCount int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entriesFormUsed = true
	p.gotThreadID = threadID
	p.gotEntryCount = entryCount
}

func (p *serveResumeIdentityProbe) recordFile() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fileFormUsed = true
}

// runServeResumeWithProbe drives one --resume through runServeWithDeps and
// returns the probe's observations plus the serve error.
func runServeResumeWithProbe(t *testing.T, stateDir, sessionID string) (*serveResumeIdentityProbe, error) {
	t.Helper()
	probe := &serveResumeIdentityProbe{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	// Capture the serve context's cancel the way the existing tests do (see
	// TestRunResumeWithFailedReservationPreservesForeignOwnedChild): the
	// shutdown goroutine waits on ctx.Done() even after serveHTTP returns,
	// so the override below must cancel before returning.
	var cancelMu sync.Mutex
	var cancel context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		cancelMu.Lock()
		cancel = stop
		cancelMu.Unlock()
		return next, stop
	}
	entriesForm := deps.prepareAppIdentityFromEntries
	deps.prepareAppIdentityFromEntries = func(sourceID, threadID, ref string, header transcript.Header, entries []transcript.Entry) (server.PreparedAppIdentity, error) {
		probe.recordEntries(threadID, len(entries))
		return entriesForm(sourceID, threadID, ref, header, entries)
	}
	fileForm := deps.prepareAppIdentity
	deps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		probe.recordFile()
		return fileForm(sourceID, threadID, ref, transcriptPath)
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		// The identity is installed by the time HTTP serves; observe, cancel
		// the serve context (the shutdown goroutine waits on ctx.Done()), and
		// stop the loop with the clean-shutdown sentinel.
		cancelMu.Lock()
		stop := cancel
		cancelMu.Unlock()
		stop()
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

// TestServeResumeSeedsIdentityFromRestoredEntries pins the serve-level
// wiring issue #647: a --resume whose restore decoded the transcript must
// seed its prepared app identity from those entries (the entries form),
// NOT from a second file read. The differential facts asserted:
//   - the entries form ran and the file form did not;
//   - it ran for the resumed session id with the restored entry count;
//   - the run completed cleanly.
func TestServeResumeSeedsIdentityFromRestoredEntries(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	seedResumableSession(t, stateDir, sessionID, sessionID)

	probe, serveErr := runServeResumeWithProbe(t, stateDir, sessionID)
	if serveErr != nil {
		t.Fatalf("runServeWithDeps(resume): %v", serveErr)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if !probe.entriesFormUsed {
		t.Fatal("resume did not seed its app identity from the restored entries (entries form unused)")
	}
	if probe.fileFormUsed {
		t.Fatal("resume re-read the transcript file for its app identity (file form used)")
	}
	if probe.gotThreadID != sessionID {
		t.Fatalf("entries form thread id = %q, want %q", probe.gotThreadID, sessionID)
	}
	if probe.gotEntryCount != 2 {
		t.Fatalf("entries form entry count = %d, want 2 (the restored two-entry transcript)", probe.gotEntryCount)
	}
}

// TestServeResumeForeignHeaderEntryListFailsStartup pins the error contract
// at the same serve-level boundary: an entries list whose header names
// another session fails daemon startup with the same error the file form
// produces for a foreign-header transcript, rather than serving a
// conversation that never happened.
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
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.entriesFormUsed || probe.fileFormUsed {
		t.Fatal("identity preparation ran over a foreign-header transcript; restore should have failed first")
	}
	if !strings.Contains(serveErr.Error(), "restore session") {
		t.Fatalf("serve error = %v, want the restore-stage failure naming the mismatch", serveErr)
	}
}
