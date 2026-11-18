package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

// openJobJournal opens a session's jobs.jsonl for a fixture, creating the
// directory the store lives in.
func openJobJournal(t *testing.T, dir string) *jobstore.Store {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create job dir: %v", err)
	}
	store, err := jobstore.OpenNoSync(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open job journal: %v", err)
	}
	return store
}

func appendJobEvent(t *testing.T, store *jobstore.Store, event jobstore.Event) {
	t.Helper()
	if err := store.Append(event); err != nil {
		t.Fatalf("append job event: %v", err)
	}
}

func closeJobJournal(t *testing.T, store *jobstore.Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close job journal: %v", err)
	}
}

// writeJobJournalLines writes raw journal lines to dir/jobs.jsonl, for fixtures
// that must control the exact bytes (a torn append, a corrupt line).
func writeJobJournalLines(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create job dir: %v", err)
	}
	content := strings.Join(lines, "")
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write job journal: %v", err)
	}
}

// jobStartedLine is the JSON a job's start lands in the journal as, so a raw
// fixture and a store-appended one describe the same event.
func jobStartedLine(jobID, ownerSession string, started time.Time) string {
	return `{"kind":"job_started","seq":1,"ts":"` + started.Format(time.RFC3339) +
		`","job_id":"` + jobID + `","type":"shell","owner_session_id":"` + ownerSession +
		`","visible_to_session_id":"` + ownerSession +
		`","started_at":"` + started.Format(time.RFC3339) + `"}`
}

// TestLoadSessionJobGetPreferOwnerCompletedOverForwardedRunning pins the defect
// the issue names: a cold consumer that deduplicates by JobID alone keeps a
// forwarded `running` row even when the owner's journal holds the completed
// status, end time and exit code.
func TestLoadSessionJobGetPreferOwnerCompletedOverForwardedRunning(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	ended := started.Add(90 * time.Second)
	exit := 3

	// The ancestor's journal holds the FORWARDED copy: the owner's start event as
	// the ancestor saw it, with the forwarding session's visibility. No terminal
	// forward landed, so it folds to a running record.
	rootStore := openJobJournal(t, jobsDir(stateDir, rootSession))
	appendJobEvent(t, rootStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: rootSession,
		StartedAt: &started,
	})
	closeJobJournal(t, rootStore)

	// The owner's journal holds the authoritative lifecycle: the completed
	// status, the end time and the exit code the forwarded copy never got.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, Reason: "exit_nonzero", ExitCode: &exit,
		EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	// Read the job from the ANCESTOR's session: that is where a hub past-session
	// fallback lands when it is pointed at the wrong branch of the tree, and it is
	// where the stale copy lives.
	job, found, err := LoadSessionJobGet(stateDir, rootSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's completed record")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q from the owner journal (the forwarded copy is stale running)",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.EndedAt == "" {
		t.Fatal("endedAt is empty, want the owner journal's end time")
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the owner journal", job.ExitCode, exit)
	}
	if job.OwnerSessionID != ownerSession {
		t.Fatalf("ownerSessionId = %q, want the owner %q", job.OwnerSessionID, ownerSession)
	}
}

// TestRetainedJobAuthorityKeysOnTheEncodedOwner pins the rule itself: authority
// is a fact about the journal a record was read from against the owner the job
// ID encodes, never a fact about what the row claims.
//
// The two records here are byte-identical in every field. The only difference is
// which journal each was read from, and that difference is the whole answer —
// which is why ranking on the row's own claim cannot express this rule.
func TestRetainedJobAuthorityKeysOnTheEncodedOwner(t *testing.T) {
	t.Parallel()
	ownerSession := identifier.MustNewSessionID()
	ancestorSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	rec := &jobstore.JobRecord{JobID: jobID, OwnerSessionID: ownerSession}

	if !retainedJobAuthority(jobID, ownerSession, rec) {
		t.Fatal("the owner journal's record is not authoritative")
	}
	if retainedJobAuthority(jobID, ancestorSession, rec) {
		t.Fatal("a forwarded copy read from an ancestor is authoritative; the field it carries is the owner's own, so only the journal can separate them")
	}
	if retainedJobAuthority(jobID, ownerSession, nil) {
		t.Fatal("a nil record is authoritative")
	}

	// A row whose claim the identifier contradicts is never authoritative, even
	// read from the encoded owner's own journal: the contradiction means the
	// record was folded from a start that described some other session's launch.
	contradictory := &jobstore.JobRecord{JobID: jobID, OwnerSessionID: ancestorSession}
	if retainedJobAuthority(jobID, ownerSession, contradictory) {
		t.Fatal("a record naming an owner the job ID contradicts is authoritative")
	}

	// An empty claim states nothing to contradict, and a job ID encoding no owner
	// leaves nothing to rank against.
	claimless := &jobstore.JobRecord{JobID: jobID}
	if !retainedJobAuthority(jobID, ownerSession, claimless) {
		t.Fatal("a pre-owner record read from the encoded owner's journal is not authoritative")
	}
	if retainedJobAuthority("job_x", ownerSession, claimless) {
		t.Fatal("a job ID encoding no owner gave its record authority")
	}
}

// TestLoadSessionJobRecordContradictoryAncestorRowIsNotAuthoritative pins the
// round-two medium: a stale ancestor row that DECLARES the ancestor as its owner
// while the job ID encodes the descendant. The fold flags the contradiction, and
// an authority rule reading the row's claim would rank that ancestor row as
// owner-authored and keep its stale running row over the real owner's finish.
func TestLoadSessionJobRecordContradictoryAncestorRowIsNotAuthoritative(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	ended := started.Add(30 * time.Second)
	exit := 7

	// The ancestor's row claims the ANCESTOR as its owner. Read against the job
	// ID, that claim is a contradiction, not an authority.
	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ancestorSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	closeJobJournal(t, ancestorStore)

	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_owner",
	})
	closeJobJournal(t, ownerStore)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's completed record")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q from the owner journal: the ancestor row's claim contradicts the job ID, so it is not owner-authored",
			job.Status, jobstore.StatusCompleted)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d from the owner journal", job.ExitCode, exit)
	}
}

// TestLoadSessionJobRecordFallsBackToForwardedCopyWhenOwnerUnavailable pins the
// other half of the rule: when the owner's journal holds nothing, the forwarded
// copy is the answer rather than a not-found. Authoritative records are
// preferred, but they are not required — a dead descendant's only surviving row
// is the copy an ancestor forwarded, and dropping it would lose the job.
func TestLoadSessionJobRecordFallsBackToForwardedCopyWhenOwnerUnavailable(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	// No journal at all for the owner: the descendant never ran, or its state is
	// gone. The ancestor's forwarded copy is the only surviving evidence.
	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	closeJobJournal(t, ancestorStore)

	read, err := loadSessionJobRecord(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.found || read.record == nil {
		t.Fatal("found = false, want the forwarded copy as fallback evidence")
	}
	if read.sourceSessionID != ancestorSession {
		t.Fatalf("sourceSessionID = %q, want %q: the copy came from this journal because the owner's held nothing",
			read.sourceSessionID, ancestorSession)
	}
	if read.record.OwnerSessionID != ownerSession {
		t.Fatalf("ownerSessionID = %q, want the owner %q the forwarded event names", read.record.OwnerSessionID, ownerSession)
	}
}

// TestLoadSessionJobGetRequiresRequestedScope pins scope proof: a job the
// requested session has no record of is not in its scope, however well-formed
// the job ID looks. Without that evidence the lookup would serve an unrelated
// session's data behind the ref the caller asked about.
func TestLoadSessionJobGetRequiresRequestedScope(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	askedSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)

	// The owner's journal holds the job. The asked session's journal holds a
	// DIFFERENT job, so it never forwarded this one and has no claim on it.
	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started,
	})
	closeJobJournal(t, ownerStore)

	askedStore := openJobJournal(t, jobsDir(stateDir, askedSession))
	appendJobEvent(t, askedStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: identifier.MustNewJobID(askedSession),
		Type: jobstore.JobShell, Status: jobstore.StatusRunning, OwnerSessionID: askedSession,
		VisibleToSession: askedSession, StartedAt: &started,
	})
	closeJobJournal(t, askedStore)

	job, found, err := LoadSessionJobGet(stateDir, askedSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if found {
		t.Fatalf("found = true (job %+v), want false: the asked session holds no record of this job, so the lookup would serve another session's data behind its ref", job)
	}
}

// TestLoadSessionJobGetReportsUnresolvedScopeOnTornTail pins the difference
// between "this job is not here" and "this journal cannot say". The requested
// session's journal is an unterminated append whose intact prefix mentions no
// other job, so the torn line could BE the requested job's record. Answering
// not-found asserts an absence the scan never established, and the hub turns
// that into a permanent "job not found". The condition is retryable, so it must
// surface as unavailable.
func TestLoadSessionJobGetReportsUnresolvedScopeOnTornTail(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	otherJob := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

	// A complete record for a DIFFERENT job, then an append that never ended.
	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(otherJob, sessionID, started)+"\n",
		`{"kind":"job_started","seq":2,"job_id":"`+jobID+`","type":"shell"`,
	)

	_, found, err := LoadSessionJobGet(stateDir, sessionID, jobID)
	if found {
		t.Fatal("found = true, want the torn append to leave the job unresolved")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want a wire error: an unresolved scope is retryable, not a permanent miss", err)
	}
	if wireErr.Code != appwire.CodeUnavailable {
		t.Fatalf("error code = %d, want %d (unavailable): not-found would assert absence the scan never established",
			wireErr.Code, appwire.CodeUnavailable)
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverDamagedOwnerPrefix pins the
// governing invariant: a record that is not fully established must not outrank
// one that is.
//
// The owner's journal decodes a start and then stops at a corrupt line, so its
// folded row reads `running` — but the finish may be the very line that failed
// to decode. The ancestor holds a forwarded terminal observation, which is a
// complete statement however stale the copy around it. The owner row is
// authoritative on what the job IS, and it still may not displace the only
// settled answer there is.
func TestLoadSessionJobGetKeepsForwardedTerminalOverDamagedOwnerPrefix(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	ended := started.Add(45 * time.Second)
	exit := 9

	// The ancestor holds both halves: a forwarded start and a forwarded finish.
	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_fwd",
	})
	closeJobJournal(t, ancestorStore)

	// The owner's journal has its start, then a line that cannot decode. The
	// finish is unreachable, so the prefix folds to a running row.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`","status":"completed"`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded terminal observation")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q: the owner's prefix cannot establish that the job finished, so its running row may not displace the forwarded finish",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d: the forwarded terminal observation is a complete statement", job.ExitCode, exit)
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverTornOwnerTail pins the same
// invariant on the other kind of incomplete evidence. A torn tail is an append
// that landed without its newline — the in-flight finish, or one that will never
// complete — and the scan returns a NIL error for it. A rule that read "clean
// scan" as "established" would let the owner's running prefix hide a known exit
// code and end time.
func TestLoadSessionJobGetKeepsForwardedTerminalOverTornOwnerTail(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	ended := started.Add(20 * time.Second)
	exit := 4

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_fwd",
	})
	closeJobJournal(t, ancestorStore)

	// The owner's start, terminated, then a finish whose JSON never closes.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`","status":"completed"`,
	)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded terminal observation")
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want %q: a torn tail leaves the finish unaccounted for, and a nil scan error is not evidence that it landed",
			job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d: the forwarded terminal observation is a complete statement", job.ExitCode, exit)
	}
}

// TestLoadSessionJobGetKeepsForwardedTerminalOverStatuslessOwnerRow pins the
// third form of "not established": a journal that holds only a notification for
// the job folds a row for it because the job was NAMED, not because anyone said
// what it did. That row has no status at all, and a clean scan of a journal that
// never described the state is not evidence about the state.
func TestLoadSessionJobGetKeepsForwardedTerminalOverStatuslessOwnerRow(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 16, 0, 0, 0, time.UTC)
	ended := started.Add(10 * time.Second)
	exit := 2

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_fwd",
	})
	closeJobJournal(t, ancestorStore)

	// The owner's journal names the job only through a notification, which folds
	// a row for it with no status.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_notification_pending","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","owner_session_id":"`+ownerSession+`"}`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded terminal observation")
	}
	if job.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q: a statusless owner fragment established nothing, so it cannot displace an observed finish",
			job.Status, jobstore.StatusCompleted)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d: the forwarded terminal observation is a complete statement", job.ExitCode, exit)
	}
}

// TestLoadSessionJobGetPreservesLaunchFieldsFromScopedStart pins the merge that
// keeps a finish-only owner record useful: the owner journal describes the END,
// and the command and type live in the start event only the scoped journal still
// has. The owner's terminal facts win; the launch fields fill from that start.
func TestLoadSessionJobGetPreservesLaunchFieldsFromScopedStart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 17, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Second)
	exit := 0

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		Command: "printf hello", Description: "say hello", StartedAt: &started,
	})
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCompleted, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_fwd",
	})
	closeJobJournal(t, ancestorStore)

	// The owner's journal holds ONLY the finish: no start, so no command and no
	// type survive there.
	//
	// Its end time and exit code are DELIBERATELY DIFFERENT from the ancestor's
	// copy. Without that difference the assertions below would also pass if the
	// reader returned the ancestor's record unchanged, which is what made an
	// earlier version of this test unable to fail. The owner finished later and
	// exited differently; only a read that selected the owner's record can report
	// those, and only a read that also filled the launch fields can report the
	// command.
	ownerEnded := ended.Add(7 * time.Second)
	ownerExit := 42
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_finished","seq":1,"ts":"`+ownerEnded.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","status":"command_exited_nonzero","exit_code":`+strconv.Itoa(ownerExit)+
			`,"ended_at":"`+ownerEnded.Format(time.RFC3339)+`","terminal_generation":"gen_owner"}`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's finish")
	}
	if job.Command != "printf hello" {
		t.Fatalf("command = %q, want %q recovered from the scoped start: an end alone cannot say what was launched",
			job.Command, "printf hello")
	}
	if job.Description != "say hello" {
		t.Fatalf("description = %q, want %q recovered from the scoped start", job.Description, "say hello")
	}
	// The owner's terminal facts, which the ancestor's copy does not carry. These
	// are what prove the selected record is the OWNER's, not the ancestor's.
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want the owner's %q, not the ancestor's copy", job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != ownerExit {
		t.Fatalf("exitCode = %v, want the owner's %d, not the ancestor's %d", job.ExitCode, ownerExit, exit)
	}
	if wantEnded := ownerEnded.UTC().Format(time.RFC3339); job.EndedAt != wantEnded {
		t.Fatalf("endedAt = %q, want the owner's %q, not the ancestor's copy", job.EndedAt, wantEnded)
	}
}

// TestLoadSessionJobGetIgnoresLaunchFieldsFromContradictoryStart pins the limit
// of that merge: a scoped start whose own owner claim the job ID contradicts
// never described this job, so its command and explicit output path belong to
// some other session's launch. Copying them would show a foreign command and
// send the output read to a path that is not this job's log.
func TestLoadSessionJobGetIgnoresLaunchFieldsFromContradictoryStart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Second)
	foreignPath := filepath.Join(t.TempDir(), "another-job.log")

	// The scoped start names the ANCESTOR as owner, which the job ID contradicts,
	// and it carries a command and an explicit path of its own.
	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ancestorSession, VisibleToSession: ancestorSession,
		Command: "rm -rf /tmp/other", StartedAt: &started, OutputPath: foreignPath,
	})
	closeJobJournal(t, ancestorStore)

	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		`{"kind":"job_finished","seq":1,"ts":"`+ended.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","status":"completed","terminal_generation":"gen_owner"}`+"\n",
	)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's finish")
	}
	if job.Command != "" {
		t.Fatalf("command = %q, want empty: the start named an owner the job ID contradicts, so its command belongs to another job", job.Command)
	}
}

// TestLoadSessionJobGetWithholdsLaunchFieldsWhenTheContradictedRowIsSelected
// covers the case where the contradictory start is not merely a launch source
// but the SELECTED record: the owner's journal is unreadable, so the forwarded
// copy is the fallback answer. Its running status is the best surviving evidence
// of what the job is doing, but the command and description it carries describe
// the launch of some other session's job, and a pane that showed them would
// label this job's recovered output with the wrong command.
//
// The started-at time is the counter-evidence that the projection withholds a
// CONTRADICTION rather than blanking the record: it is a fact about this job's
// run and stays.
func TestLoadSessionJobGetWithholdsLaunchFieldsWhenTheContradictedRowIsSelected(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ancestorSession, VisibleToSession: ancestorSession,
		Command: "rm -rf /tmp/other", Description: "another session's job",
		StartedAt: &started,
	})
	closeJobJournal(t, ancestorStore)
	// The encoded owner's journal does not exist, so the forwarded copy is the
	// fallback answer and nothing replaces it.

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded copy as fallback evidence")
	}
	if job.Command != "" {
		t.Fatalf("command = %q, want empty: the selected record's owner claim contradicts the job ID, so its command belongs to another job", job.Command)
	}
	if job.Description != "" {
		t.Fatalf("description = %q, want empty for the same reason as the command", job.Description)
	}
	if job.StartedAt != started.UTC().Format(time.RFC3339) {
		t.Fatalf("startedAt = %q, want %q: the start time is a fact about this job's run and is not withheld",
			job.StartedAt, started.UTC().Format(time.RFC3339))
	}
}

// TestLoadSessionJobGetKeepsStoredLegacyOwnerWhenForwardedCopyWins pins the
// owner a pre-owner job ID cannot state for itself. Such an ID encodes nothing,
// so the record's stored OwnerSessionID is the only statement of which session
// ran the job. When the ancestor's forwarded copy is the record that wins, that
// field must survive: overwriting it with the ancestor's own id attributes the
// job to the session that merely forwarded it.
func TestLoadSessionJobGetKeepsStoredLegacyOwnerWhenForwardedCopyWins(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	// A pre-owner id: not shaped as job_<owner>_<suffix>, so it encodes no owner.
	jobID := "job_legacy"
	started := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	closeJobJournal(t, ancestorStore)

	job, found, err := LoadSessionJobGet(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded copy")
	}
	if job.OwnerSessionID != ownerSession {
		t.Fatalf("ownerSessionID = %q, want the stored %q: the ancestor merely forwarded the job, it did not run it",
			job.OwnerSessionID, ownerSession)
	}
}

// TestLoadSessionJobOutputPageIgnoresContradictoryRecordsOutputPath pins the
// read-side counterpart of that guard: an explicit path on a contradictory
// record must not be honored either, because the record states where some other
// job's bytes are.
func TestLoadSessionJobOutputPageIgnoresContradictoryRecordsOutputPath(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 19, 0, 0, 0, time.UTC)

	// A real file at the contradictory path, so a read that honored it would
	// return ANOTHER job's bytes rather than failing — the silent-wrong-output
	// case, which a missing-file fixture could not distinguish from a plain miss.
	stalePath := filepath.Join(t.TempDir(), "stale.log")
	if err := os.WriteFile(stalePath, []byte("someone else's output"), 0o644); err != nil {
		t.Fatalf("write stale log: %v", err)
	}

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ancestorSession, VisibleToSession: ancestorSession,
		StartedAt: &started, OutputPath: stalePath,
	})
	closeJobJournal(t, ancestorStore)

	// The owner's own default log, which is where this job's bytes actually are.
	ownerLogDir := filepath.Join(jobsDir(stateDir, ownerSession), "jobs")
	if err := os.MkdirAll(ownerLogDir, 0o755); err != nil {
		t.Fatalf("create owner log dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ownerLogDir, jobID+".log"), []byte("this job's output"), 0o644); err != nil {
		t.Fatalf("write owner log: %v", err)
	}

	page, found, err := LoadSessionJobOutputPage(stateDir, ancestorSession, jobID, nil, 0)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's default log")
	}
	if strings.Contains(page.Data, "someone else") {
		t.Fatalf("page data = %q, want the owner's log: a contradictory record's explicit path reaches another job's bytes", page.Data)
	}
	if !strings.Contains(page.Data, "this job") {
		t.Fatalf("page data = %q, want the owner's default log under the validated owner's directory", page.Data)
	}
}

// TestLoadSessionJobOutputPageHonorsAgreeingRecordsOutputPath is the
// counter-evidence for the guard above: a record that DOES describe this job
// keeps its explicit path, so the fix did not narrow the rule until everything
// fell back to the default.
func TestLoadSessionJobOutputPageHonorsAgreeingRecordsOutputPath(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)

	explicitPath := filepath.Join(t.TempDir(), "explicit.log")
	if err := os.WriteFile(explicitPath, []byte("explicit output"), 0o644); err != nil {
		t.Fatalf("write explicit log: %v", err)
	}

	ownerStore := openJobJournal(t, jobsDir(stateDir, ownerSession))
	appendJobEvent(t, ownerStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ownerSession,
		StartedAt: &started, OutputPath: explicitPath,
	})
	closeJobJournal(t, ownerStore)

	page, found, err := LoadSessionJobOutputPage(stateDir, ownerSession, jobID, nil, 0)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the explicit log")
	}
	if !strings.Contains(page.Data, "explicit output") {
		t.Fatalf("page data = %q, want the explicit path's bytes: an agreeing record still states where its log is", page.Data)
	}
}

// TestLoadSessionJobOutputPageReadsOwnerLogWhenAncestorSuppliedTheStatus pins the
// independence of "what happened" from "where the bytes are". The status comes
// from the ancestor's complete forwarded finish because the owner's journal is
// torn; the LOG still lives under the owner's directory, because that is the
// session that ran the job and wrote it.
func TestLoadSessionJobOutputPageReadsOwnerLogWhenAncestorSuppliedTheStatus(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)
	ended := started.Add(8 * time.Second)
	exit := 1

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID,
		Status: jobstore.StatusCommandExitedNonzero, ExitCode: &exit, EndedAt: &ended, TerminalGen: "gen_fwd",
		// A forwarded finish is a copy of the owner's own, so it carries the byte
		// count the owner recorded. A terminal record whose count disagrees with
		// its log is a different defect (checkOutputTotalForRecord); this fixture
		// is about which directory the bytes are read from.
		OutputBytes: int64(len("owner bytes")),
	})
	closeJobJournal(t, ancestorStore)

	// The owner's journal has its start and a torn finish, so it cannot establish
	// the status — but the log was still written beside it.
	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`","status":"completed"`,
	)
	ownerLogDir := filepath.Join(jobsDir(stateDir, ownerSession), "jobs")
	if err := os.MkdirAll(ownerLogDir, 0o755); err != nil {
		t.Fatalf("create owner log dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ownerLogDir, jobID+".log"), []byte("owner bytes"), 0o644); err != nil {
		t.Fatalf("write owner log: %v", err)
	}

	page, found, err := LoadSessionJobOutputPage(stateDir, ancestorSession, jobID, nil, 0)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the owner's log")
	}
	if !strings.Contains(page.Data, "owner bytes") {
		t.Fatalf("page data = %q, want the owner's log: the finish supplied the status, and that is a different question from where the bytes were written", page.Data)
	}
}

// TestLoadSessionJobRecordOwnerJournalTornTailIsReported pins the diagnostic the
// issue asks for on the owner branch: a torn append in the owner's journal is
// reported even though the read still answers.
func TestLoadSessionJobRecordOwnerJournalTornTailIsReported(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	ancestorSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC)

	ancestorStore := openJobJournal(t, jobsDir(stateDir, ancestorSession))
	appendJobEvent(t, ancestorStore, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell,
		Status: jobstore.StatusRunning, OwnerSessionID: ownerSession, VisibleToSession: ancestorSession,
		StartedAt: &started,
	})
	closeJobJournal(t, ancestorStore)

	writeJobJournalLines(t, jobsDir(stateDir, ownerSession),
		jobStartedLine(jobID, ownerSession, started)+"\n",
		`{"kind":"job_finished","seq":2,"job_id":"`+jobID+`"`,
	)

	read, err := loadSessionJobRecord(stateDir, ancestorSession, jobID)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.diagnostics.TornTail {
		t.Fatal("tornTail = false, want the owner journal's unterminated append reported")
	}
}

// TestLoadSessionJobRecordOrphanFinishIsReported pins the lifecycle diagnostic:
// a finish with no preceding start does not describe a whole job, and a reader
// that reported it as complete would present a fragment as history.
func TestLoadSessionJobRecordOrphanFinishIsReported(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	ended := time.Date(2026, 11, 18, 23, 0, 0, 0, time.UTC)

	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		`{"kind":"job_finished","seq":1,"ts":"`+ended.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","status":"completed","terminal_generation":"gen_orphan"}`+"\n",
	)

	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		t.Fatalf("loadSessionJobRecord: %v", err)
	}
	if !read.diagnostics.IncompleteLifecycle {
		t.Fatal("incompleteLifecycle = false, want the orphan finish reported")
	}
}

// TestLoadSessionJobGetReportsCorruptStatusAsUnavailable pins what a corrupt
// finish line means for a caller. The decoded prefix proves the job exists and
// what it ran, but the finish may be the very line that failed to decode, so the
// surviving `running` row is not a statement that the job is still running.
// Serving it with a nil error would report a finished job as running forever.
//
// The second half is the line that keeps this narrow: the same state directory
// with a readable finish is an ordinary settled read, because a terminal
// observation is complete on its own.
func TestLoadSessionJobGetReportsCorruptStatusAsUnavailable(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(sessionID)
	started := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	ended := started.Add(12 * time.Second)
	exit := 6

	// The start survives; the finish line cannot decode. No other journal holds
	// this job, so nothing else can state how it ended.
	corrupt := jobStartedLine(jobID, sessionID, started) + "\n" +
		`{"kind":"job_finished","seq":2,"job_id":"` + jobID + `","status":"completed"` + "\n"
	writeJobJournalLines(t, jobsDir(stateDir, sessionID), corrupt)

	_, found, err := LoadSessionJobGet(stateDir, sessionID, jobID)
	if found {
		t.Fatal("found = true, want unavailable: the prefix cannot say whether the job finished")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want a wire error: a damaged history is unreadable, not absent", err)
	}
	if wireErr.Code != appwire.CodeUnavailable {
		t.Fatalf("error code = %d, want %d (unavailable): the job exists, its outcome does not decode",
			wireErr.Code, appwire.CodeUnavailable)
	}

	// The output reader is independent of that verdict, and the bytes that did
	// land stay readable — recovering what happened is how a person settles it.
	ownerLogDir := filepath.Join(jobsDir(stateDir, sessionID), "jobs")
	if err := os.MkdirAll(ownerLogDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ownerLogDir, jobID+".log"), []byte("prefix bytes"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	page, pageFound, pageErr := LoadSessionJobOutputPage(stateDir, sessionID, jobID, nil, 0)
	if pageErr != nil || !pageFound {
		t.Fatalf("LoadSessionJobOutputPage: found=%v err=%v, want the readable output beside a corrupt finish", pageFound, pageErr)
	}
	if !strings.Contains(page.Data, "prefix bytes") {
		t.Fatalf("page data = %q, want the bytes that did land", page.Data)
	}

	// Same state directory, readable finish: an ordinary settled read.
	writeJobJournalLines(t, jobsDir(stateDir, sessionID),
		jobStartedLine(jobID, sessionID, started)+"\n",
		`{"kind":"job_finished","seq":2,"ts":"`+ended.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","status":"command_exited_nonzero","exit_code":6,`+
			`"ended_at":"`+ended.Format(time.RFC3339)+`","terminal_generation":"gen_ok"}`+"\n",
	)
	job, found, err := LoadSessionJobGet(stateDir, sessionID, jobID)
	if err != nil || !found {
		t.Fatalf("readable finish: found=%v err=%v, want the settled terminal record", found, err)
	}
	if job.Status != string(jobstore.StatusCommandExitedNonzero) {
		t.Fatalf("status = %q, want the recorded %q once the finish decodes", job.Status, jobstore.StatusCommandExitedNonzero)
	}
	if job.ExitCode == nil || *job.ExitCode != exit {
		t.Fatalf("exitCode = %v, want %d", job.ExitCode, exit)
	}
}
