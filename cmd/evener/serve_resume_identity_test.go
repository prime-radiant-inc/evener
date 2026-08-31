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
// a real --resume can restore: OpenWriterForSession will strict-decode the
// entries and RestoredTranscript will report them, so serve's identity
// preparation has a choice of forms to make.
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

// serveResumeIdentityProbe records which app-identity preparation form a
// --resume run used. Plain fields suffice: every write happens on the
// goroutine that called runServeWithDeps (identity preparation at
// serve.go:551 and the serveHTTP override at serve.go:1181), before it
// returns, and the test reads the probe only after that return. The serve
// goroutines spawned along the way never touch the probe.
type serveResumeIdentityProbe struct {
	entriesFormUsed       bool
	fileFormUsed          bool
	windowedFormUsed      bool
	windowedPrefixTurns   int
	windowedPrefixEntries int
	gotThreadID           string
	gotEntryCount         int
}

// runServeResumeWithProbe drives one --resume through runServeWithDeps and
// returns the probe's observations plus the serve error.
func runServeResumeWithProbe(t *testing.T, stateDir, sessionID string) (*serveResumeIdentityProbe, error) {
	t.Helper()
	probe := &serveResumeIdentityProbe{}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	var cancel context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		cancel = stop
		return next, stop
	}
	entriesForm := deps.prepareAppIdentityFromEntries
	deps.prepareAppIdentityFromEntries = func(sourceID, threadID, ref string, header transcript.Header, entries []transcript.Entry) (server.PreparedAppIdentity, error) {
		probe.entriesFormUsed = true
		probe.gotThreadID = threadID
		probe.gotEntryCount = len(entries)
		return entriesForm(sourceID, threadID, ref, header, entries)
	}
	fileForm := deps.prepareAppIdentity
	deps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		probe.fileFormUsed = true
		return fileForm(sourceID, threadID, ref, transcriptPath)
	}
	windowedForm := deps.prepareAppIdentityFromEntriesWindowed
	deps.prepareAppIdentityFromEntriesWindowed = func(sourceID, threadID, ref string, header transcript.Header, entries []transcript.Entry, prefixEntryCount, prefixTurnCount int) (server.PreparedAppIdentity, error) {
		probe.windowedFormUsed = true
		probe.windowedPrefixEntries = prefixEntryCount
		probe.windowedPrefixTurns = prefixTurnCount
		return windowedForm(sourceID, threadID, ref, header, entries, prefixEntryCount, prefixTurnCount)
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

// seedWindowedResumableSession seeds a session whose transcript carries a
// validated resume sidecar: two prefix entries, a checkpoint entry (the
// boundary the sidecar anchors at), then one suffix entry. The FIRST resume
// writes the opportunistic sidecar (full scan), so the SECOND resume — the
// one this helper's caller drives — reads the windowed form.
func seedWindowedResumableSession(t *testing.T, stateDir, sessionID string) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-test",
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	path := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID:  sessionID,
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
	if err := writer.Append(schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction summary"))); err != nil {
		t.Fatalf("Append checkpoint: %v", err)
	}
	if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User("after compaction"))); err != nil {
		t.Fatalf("Append suffix: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
	// First resume: full scan, which writes the opportunistic sidecar with
	// the exact prefix-turn count.
	first, _, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
}

// TestServeResumeWindowedIdentityArmsWindowedPaging pins the serve-level
// wiring issue #643's windowed snapshot: a --resume whose sidecar validated
// must seed its identity through the WINDOWED form — armed with the sidecar's
// exact prefix-turn count, not zero. The zero-count bug left every windowed
// Latest/Page taking the non-windowed branch, so windowed paging never ran
// in production despite its tests passing.
func TestServeResumeWindowedIdentityArmsWindowedPaging(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	seedWindowedResumableSession(t, stateDir, sessionID)

	probe, serveErr := runServeResumeWithProbe(t, stateDir, sessionID)
	if serveErr != nil {
		t.Fatalf("runServeWithDeps(resume): %v", serveErr)
	}
	if !probe.windowedFormUsed {
		t.Fatal("resume with a valid sidecar did not use the windowed identity form")
	}
	if probe.entriesFormUsed || probe.fileFormUsed {
		t.Fatal("windowed resume also used another identity form")
	}
	// Two prefix entries + the checkpoint entry; the sidecar's prefix spans
	// the two user turns (the checkpoint is the suffix's first entry).
	if probe.windowedPrefixEntries != 2 {
		t.Fatalf("windowed prefix entries = %d, want 2", probe.windowedPrefixEntries)
	}
	// Both prefix entries project turns, and no system prompt means no
	// prelude: the prefix-turn count is 2, not 0 — the arming value.
	if probe.windowedPrefixTurns != 2 {
		t.Fatalf("windowed prefix turns = %d, want 2 (armed, not zero)", probe.windowedPrefixTurns)
	}
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
	if probe.entriesFormUsed || probe.fileFormUsed {
		t.Fatal("identity preparation ran over a foreign-header transcript; restore should have failed first")
	}
	if !strings.Contains(serveErr.Error(), "restore session") {
		t.Fatalf("serve error = %v, want the restore-stage failure naming the mismatch", serveErr)
	}
}

// TestServeResumeCompactionFallbackRefreshesSidecar pins the steady-state
// repair: a --resume over a compaction-anchored sidecar (whose prefix-turn
// count is -1) must take the file form ONCE and convert the sidecar to the
// full-scan one — so the NEXT resume windows and arms paging instead of
// re-reading the whole file on every daemon start forever. Without the
// refresh, compacting sessions pay the full file re-read per resume as a
// permanent steady state, which is exactly the cost the sidecar exists to
// remove.
func TestServeResumeCompactionFallbackRefreshesSidecar(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	// Seed the windowed fixture, then overwrite its sidecar with the
	// compaction-anchored shape: same boundary, but PrefixTurnCount -1 and
	// no fold snapshots — what the compaction anchor writes after a
	// checkpoint that no full scan has followed.
	seedWindowedResumableSession(t, stateDir, sessionID)
	path := filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")
	sidecar, ok := transcript.ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing after seeded first resume")
	}
	sidecar.PrefixTurnCount = -1
	sidecar.SnapshotsComplete = false
	sidecar.PendingAttention = nil
	sidecar.DeliveryCommits = nil
	sidecar.ClientMutationTurns = nil
	if err := transcript.WriteSidecar(path, sidecar); err != nil {
		t.Fatalf("rewrite sidecar with compaction-anchor shape: %v", err)
	}

	// First serve run: the compaction-anchored sidecar validates (windowed
	// read) but cannot arm paging, so the file form runs — and the refresh
	// must follow it.
	probe, serveErr := runServeResumeWithProbe(t, stateDir, sessionID)
	if serveErr != nil {
		t.Fatalf("first serve run: %v", serveErr)
	}
	if !probe.fileFormUsed {
		t.Fatal("compaction-anchored resume did not take the file form")
	}
	if probe.windowedFormUsed || probe.entriesFormUsed {
		t.Fatal("compaction-anchored resume used a non-file identity form")
	}
	refreshed, ok := transcript.ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing after the fallback run")
	}
	if !refreshed.SnapshotsComplete || refreshed.PrefixTurnCount != 2 {
		t.Fatalf("sidecar was not refreshed to the full-scan shape: complete=%v prefixTurns=%d, want complete=true prefixTurns=2", refreshed.SnapshotsComplete, refreshed.PrefixTurnCount)
	}

	// Second serve run over the refreshed sidecar: it must go windowed with
	// the armed count — the steady state is repaired, not repeated.
	probe, serveErr = runServeResumeWithProbe(t, stateDir, sessionID)
	if serveErr != nil {
		t.Fatalf("second serve run: %v", serveErr)
	}
	if !probe.windowedFormUsed {
		t.Fatal("resume after the refresh did not use the windowed form")
	}
	if probe.fileFormUsed {
		t.Fatal("resume after the refresh repeated the file form — the steady state was not repaired")
	}
	if probe.windowedPrefixTurns != 2 {
		t.Fatalf("windowed prefix turns after refresh = %d, want 2", probe.windowedPrefixTurns)
	}
}
