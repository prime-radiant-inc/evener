package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/anthropic"
)

// foldTailFaultFS counts Sync calls on the transcript file once armed and
// fails exactly the Nth, one-shot. The fold's compaction markers are buffered
// writes that carry no fsync of their own, so with a young SyncInterval the
// syncs after arming are the staged retained-tail copies' durable writes:
// arming on the second makes the first copy land durably and the second fail
// and roll back — the transcript I/O failure shape of issue #1158, with lines
// on both sides of the failure for the withhold to take back out.
type foldTailFaultFS struct {
	afero.Fs
	mu         sync.Mutex
	failOnNth  int // 0 disarms
	syncsSince int
}

type foldTailFaultFile struct {
	afero.File
	fs *foldTailFaultFS
}

func (fs *foldTailFaultFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return &foldTailFaultFile{File: file, fs: fs}, nil
}

// arm fails exactly the nth Sync call made after arming.
func (fs *foldTailFaultFS) arm(nth int) {
	fs.mu.Lock()
	fs.syncsSince = 0
	fs.failOnNth = nth
	fs.mu.Unlock()
}

// syncsSinceArm reports how many Sync calls were made since the last arming.
func (fs *foldTailFaultFS) syncsSinceArm() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.syncsSince
}

func (file *foldTailFaultFile) Sync() error {
	file.fs.mu.Lock()
	file.fs.syncsSince++
	nth := file.fs.failOnNth
	if nth > 0 && file.fs.syncsSince == nth {
		file.fs.failOnNth = 0
		file.fs.mu.Unlock()
		return errors.New("opaque staged tail sync failure")
	}
	file.fs.mu.Unlock()
	return file.File.Sync()
}

// TestCompactionWithholdsMarkerWhenRetainedTailWriteFails pins the durability
// contract of issue #1158: a fold whose staged retained-tail copies cannot
// all be written durably must not publish its compaction marker. The marker
// is what a resume anchors on to discard every entry before it, so a marker
// whose replacement copies never landed silently loses the turns it folded
// away. Here the first staged copy lands durably and the second fails, so the
// transaction's lines span both sides of the failure: the original entries
// must stay readable through a resume with the landed copy taken back out
// (each original exactly once), the fold's records must never reach a
// recorded-entry or published-length consumer, and the in-memory fold still
// stands (the live history is compacted and Compact succeeds).
func TestCompactionWithholdsMarkerWhenRetainedTailWriteFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "withhold-marker-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-withhold-summary\n[END SUMMARY]")}
	}, withStateDir(dir))
	if err := s.Rename("Withheld fold fixture"); err != nil {
		t.Fatal(err)
	}
	const inputs = 20
	for i := range inputs {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("opaque-withhold-input-%d", i)))
		s.recordTurn(turn, turn)
	}

	// Swap in a writer over a faultable filesystem, mirroring the production
	// interval so the buffered marker records take no opportunistic fsync: the
	// armed fault then lands on the staged copy writes, not on the markers.
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &foldTailFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	s.attachTranscript(writer, nil)
	boundaryLength := s.TranscriptRecordedLength()

	// The wired consumers a served session projects through: the recorded
	// entry hook and the published-length handoff. A withheld fold must not
	// deliver a single record or a length past the pre-fold boundary to
	// either.
	var recordedMu sync.Mutex
	var recorded []transcript.Record
	publishedLengths := make(chan int64, 16)
	s.SetTranscriptRecordedFunc(func(rec transcript.Record) {
		recordedMu.Lock()
		recorded = append(recorded, rec)
		recordedMu.Unlock()
	})
	s.SetTranscriptPublishedFunc(func(length int64, _ *appwire.ConversationProvenanceState) {
		select {
		case publishedLengths <- length:
		default:
		}
	})
	// SetTranscriptPublishedFunc seeds the handoff at install time with one
	// publication of the current length: discard it, so every length the
	// assertions below judge is one the fold itself published.
	select {
	case <-publishedLengths:
	default:
		t.Fatal("the installation published no length before the fold ran")
	}
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			fs.arm(2) // the first staged copy lands, the second fails
		}
	})

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The scenario really ran: the first staged copy's durable write synced
	// (and landed) before the armed fault hit the second. Fewer than two
	// syncs means the fault hit some other write and the assertions below
	// would prove nothing.
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("only %d sync(s) since arming, want the first staged copy to land before the second failed: the fault hit the wrong write", n)
	}

	entries := readPersistedTranscriptEntries(t, dir, s.ID())
	markers := 0
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			markers++
		}
	}
	if markers != 0 {
		t.Fatalf("fold published %d compaction marker(s) whose staged retained-tail copy write failed; the marker a resume anchors on must be withheld", markers)
	}

	// The original transcript must survive a resume: every original input is
	// readable exactly once — neither folded away by a half-applied marker nor
	// duplicated by a copy the failed fold left behind.
	resumed := ResumeHistory(entries)
	for i := range inputs {
		want := fmt.Sprintf("opaque-withhold-input-%d", i)
		n := 0
		for _, turn := range resumed {
			if turn.Message.Text() == want {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("resumed history holds input %d %d times, want exactly 1: the original must stay readable and the landed copy must be taken back out", i, n)
		}
	}

	// Nothing the transaction wrote reached a consumer: no recorded entry was
	// delivered, and the settled published length is the pre-fold boundary.
	recordedMu.Lock()
	delivered := recorded
	recordedMu.Unlock()
	if len(delivered) != 0 {
		t.Fatalf("the withheld fold delivered %d recorded entr(ies) to the consumer hook, want none: the first is %+v", len(delivered), delivered[0])
	}
	// Collect every publication the fold itself made: the hold must have
	// suppressed each transient per-record length, and the settled publication
	// must land exactly on the pre-fold boundary.
	var published []int64
	for done := false; !done; {
		select {
		case length := <-publishedLengths:
			published = append(published, length)
		default:
			done = true
		}
	}
	if len(published) == 0 {
		t.Fatal("the withheld fold published no settled length to the consumer")
	}
	for _, length := range published {
		if length > boundaryLength {
			t.Fatalf("the withheld fold published transient length %d past the pre-fold boundary %d to a consumer: a held record's length must never reach one", length, boundaryLength)
		}
	}
	if !slices.Contains(published, boundaryLength) {
		t.Fatalf("the withheld fold's publications = %v, want the settled pre-fold boundary %d among them", published, boundaryLength)
	}

	// The fold still published in memory: the live history is compacted and
	// the folded-away turns are gone from it, while the readable transcript
	// keeps them for the next resume.
	live := currentHistory(t, s)
	if at := indexOfTurnText(live, "opaque-withhold-input-0"); at >= 0 {
		t.Fatalf("live history was not compacted: folded-away input still at %d", at)
	}
	if at := indexOfTurnText(live, "opaque-withhold-input-19"); at < 0 {
		t.Fatal("live history lost its retained tail: the in-memory fold must stand")
	}
}

// TestWithheldFoldKeepsTheConversationCovered pins the conversation side of a
// withheld fold: the fold's live marker references the conversation frontier
// its transaction minted — the marker's record was that node's durable
// carrier, and the retract took it back out. The next model request must
// still resolve the marker's coverage, so the folded inputs stay carried and
// the conversation stays authoritative, and a reply recorded after the
// withheld fold must remain eligible for voice output instead of failing
// ConversationOutputEligible until a restart.
func TestWithheldFoldKeepsTheConversationCovered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "withhold-coverage-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-withhold-coverage-summary\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	const afterFold = "opaque-withhold-coverage-after"
	response := func(req llm.Request) llm.Response {
		call := communicateCall("withheld-fold-result", "result")
		resp := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
		if requestCarriesText(req, afterFold) {
			resp.Usage.InputTokens = 90_000
		}
		return resp
	}
	steps := make([]func(llm.Request) llm.Response, 0, 12)
	for range 12 {
		steps = append(steps, response)
	}
	s.client.Register(newFakeAdapter("openai", steps...))
	const rounds = 8
	for i := range rounds {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("opaque-withhold-coverage-input-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	original := s.ConversationProvenance().AdmittedInputs
	if len(original) != rounds {
		t.Fatalf("admitted inputs before the fold = %v, want %d", original, rounds)
	}

	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &foldTailFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	s.attachTranscript(writer, nil)
	var faultArmed bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			if faultArmed {
				return // a later request's empty publication must not re-arm the fault
			}
			faultArmed = true
			fs.arm(2) // the first staged copy lands, the second fails
		}
	})

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("only %d sync(s) since arming, want the first staged copy to land before the second failed: the fault hit the wrong write", n)
	}
	for _, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			t.Fatal("the withheld fold published a compaction marker whose staged retained-tail copy write failed")
		}
	}

	// A full round after the withheld fold: its model request stages the
	// folded conversation, and its reply is the output voice eligibility
	// judges.
	if _, err := s.ProcessInput(context.Background(), afterFold, nil); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	prepared := slices.Clone(s.conversation.preparedInputs)
	s.mu.Unlock()
	for _, id := range original {
		if !slices.Contains(prepared, id) {
			t.Fatalf("the request after the withheld fold lost the folded input %s: prepared=%v", id, prepared)
		}
	}
	state := s.ConversationProvenance()
	if !state.Authoritative {
		t.Fatalf("the withheld fold left the conversation non-authoritative: state=%+v", state)
	}
	var output *appwire.ConversationOutputProvenance
	for index, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if item, ok := apptranscript.CommunicateItem(entry.Turn.TurnID, index, entry.Turn); ok {
			output = item.ConversationProvenance
		}
	}
	if !appwire.ConversationOutputEligible(state, output) {
		t.Fatalf("the reply after the withheld fold is not eligible for voice output: state=%+v output=%+v", state, output)
	}
	if got := s.contextMgr.LastInputTokens(); got != 90_000 {
		t.Fatalf("the provider measured the live compacted history at 90000 tokens, got %d", got)
	}
	s.Close()
	meta, err := schema.LoadSessionMeta(dir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(s.client, s.profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: dir, testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true})})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("restored %d measured tokens on a transcript without the measured fold", got)
	}
}

func TestWithheldFoldSnapshotKeepsCoverageAfterAnotherFold(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "withhold-snapshot-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	response := func(llm.Request) llm.Response {
		call := communicateCall("withheld-snapshot-result", "result")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}
	steps := make([]func(llm.Request) llm.Response, 8)
	for i := range steps {
		steps[i] = response
	}
	s.client.Register(newFakeAdapter("openai", steps...))
	for i := range steps {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("snapshot-input-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	original := slices.Clone(s.ConversationProvenance().AdmittedInputs)
	if len(original) != len(steps) {
		t.Fatalf("admitted inputs = %d, want %d", len(original), len(steps))
	}
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &foldTailFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	s.attachTranscript(writer, nil)
	armed := false
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			if !armed {
				armed = true
				fs.arm(2)
			}
		}
	})
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("withheld Compact: %v", err)
	}
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("staged tail fault did not run: got %d syncs", n)
	}
	s.mu.Lock()
	oldFrontier := s.conversation.retractedFrontierID
	oldBase := s.conversation.frontierID
	s.mu.Unlock()
	if oldFrontier == "" {
		t.Fatal("withheld fold did not retain a live coverage frontier")
	}
	foundOldRef := false
	for _, turn := range currentHistory(t, s) {
		foundOldRef = foundOldRef || turn.ConversationCoverageRef == oldFrontier
	}
	if !foundOldRef {
		t.Fatal("history has no withheld fold marker referencing its live frontier")
	}
	var publishedSecond bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterModelHistorySnapshot = func() {
			if err := s.Compact(context.Background()); err != nil {
				t.Fatalf("competing Compact: %v", err)
			}
			s.mu.Lock()
			publishedSecond = s.conversation.frontierID != oldBase && s.conversation.retractedFrontierID == ""
			s.mu.Unlock()
		}
	})
	var timings events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if !publishedSecond {
		t.Fatal("second fold did not replace the withheld frontier after the history snapshot")
	}
	s.mu.Lock()
	prepared := slices.Clone(s.conversation.preparedInputs)
	s.mu.Unlock()
	for _, id := range original {
		if !slices.Contains(prepared, id) {
			t.Fatalf("history snapshot lost folded input %s: prepared=%v", id, prepared)
		}
	}
	if state := s.ConversationProvenance(); !state.Authoritative {
		t.Fatalf("history snapshot made conversation coverage unknown: state=%+v", state)
	}
	updateSessionTestConfig(s, func(cfg *testConfig) { cfg.afterModelHistorySnapshot = nil })
	answer := agenttest.FinalResponse("answer-recovered-91")
	answer.Usage.InputTokens = 36_000
	s.client.Register(newFakeAdapter("openai", fixedStep(answer)))
	if _, err := s.ProcessInput(t.Context(), "after-durable-fold-24", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	meta, err := schema.LoadSessionMeta(dir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if meta.LastInputTokens != 36_000 || meta.LastInputEntryCount == 0 {
		t.Fatalf("a committed replacement fold must restore measurement persistence: tokens=%d entries=%d", meta.LastInputTokens, meta.LastInputEntryCount)
	}
}

// TestWithheldFoldReEmitsNotesContextOnTheNextRequest pins the projection side
// of a withheld fold: the in-memory fold stands, so the NOTES_CONTEXT turn the
// fold folded away is gone from what the model sees, and the notes projection's
// change gate must forget the last projected block — the next model request
// re-emits the current notes rather than staying silent on state the model can
// no longer see. The marker's event and naming stay suppressed (the retract
// took the announcing record back out); the resets are fold-dependent, so they
// run.
func TestWithheldFoldReEmitsNotesContextOnTheNextRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "withhold-notes-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-withhold-notes-summary\n[END SUMMARY]")}
	}, withStateDir(dir))
	if err := s.Rename("Withheld fold notes fixture"); err != nil {
		t.Fatal(err)
	}
	// Project the notes first, so the NOTES_CONTEXT turn lands early and the
	// fold folds it away with the rest of the pre-fold span.
	if _, changed := s.setAgentNote("opaque-withheld-fold-note"); !changed {
		t.Fatal("note set not reported as change")
	}
	s.maybeAppendNotesContext()
	const inputs = 20
	for i := range inputs {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("opaque-withhold-notes-input-%d", i)))
		s.recordTurn(turn, turn)
	}

	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &foldTailFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	s.attachTranscript(writer, nil)
	var faultArmed bool
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			if faultArmed {
				return // a later request's empty publication must not re-arm the fault
			}
			faultArmed = true
			fs.arm(2) // the first staged copy lands, the second fails
		}
	})

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("only %d sync(s) since arming, want the first staged copy to land before the second failed: the fault hit the wrong write", n)
	}
	for _, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			t.Fatal("the withheld fold published a compaction marker whose staged retained-tail copy write failed")
		}
	}
	// The fold really took the projected NOTES_CONTEXT turn out of the live
	// history: only a re-emitted projection can carry the note to the model.
	for _, turn := range currentHistory(t, s) {
		if turn.Kind == schema.TurnNotesContext {
			t.Fatal("the folded history still holds the NOTES_CONTEXT turn: the fold did not take it, and the re-emit oracle below would prove nothing")
		}
	}

	var timings events.RoundTimings
	_, _, _, req, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	found := false
	for _, m := range req.Messages {
		if strings.Contains(m.Text(), "opaque-withheld-fold-note") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the request after the withheld fold omits the notes the fold projected away: the change gate must forget the last projected block so the next request re-emits it")
	}
}

// TestRequestSnapshotDuringFoldCommitKeepsCoverageKnown pins the pairing of a
// request's history snapshot with the transcript door. A fold publishes the
// compacted history — whose marker references the frontier the fold minted —
// and records that frontier in the chain only when its transcript commit
// writes the marker, all inside one hold of the door. A request snapshotting
// inside that span would resolve the marker's frontier against a chain that
// does not hold it yet: coverage resolution marks the conversation unknown for
// good and caches empty coverage for the folded inputs, and a later successful
// commit cannot repair that request. Here a Compact parks between its
// publication and its transcript commit while a request builds; the request's
// coverage must stay known and complete.
func TestRequestSnapshotDuringFoldCommitKeepsCoverageKnown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "fold-commit-window-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-fold-commit-window-summary\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	// The strategy is cleared so the request below runs no context-management
	// fold of its own: a fold's publication holds the transcript door, and the
	// request must reach its history snapshot while the competing fold holds
	// that door mid-commit — the interleaving this test pins. The snapshot
	// itself is the same code every request runs.
	s.mu.Lock()
	s.strategy = nil
	s.mu.Unlock()
	final := func(llm.Request) llm.Response { return agenttest.FinalResponse("opaque-fold-commit-window-result") }
	steps := make([]func(llm.Request) llm.Response, 0, 4)
	for range 4 {
		steps = append(steps, final)
	}
	s.client.Register(newFakeAdapter("openai", steps...))
	const rounds = 4
	for i := range rounds {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("opaque-fold-commit-window-input-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	original := s.ConversationProvenance().AdmittedInputs
	if len(original) != rounds {
		t.Fatalf("admitted inputs before the fold = %v, want %d", original, rounds)
	}

	// Park the fold between its publication and its transcript commit: the
	// live history already references the minted frontier, and the chain does
	// not hold it yet.
	inWindow := make(chan struct{})
	releaseFold := make(chan struct{})
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			close(inWindow)
			select {
			case <-releaseFold:
			// TRIPWIRE: the park is released by the request goroutine, and a
			// snapshot that waits on the transcript door is exactly what this
			// bound exists to break: no completion signal can arrive while the
			// fold holds the door, so a hang here is the finding, not a slow
			// machine. Generous only to keep a real deadlock from consuming
			// the whole test binary timeout.
			case <-time.After(2 * time.Second):
				// A snapshot that holds the door cannot pass this park while
				// the fold holds it, so the timeout ends the park for both.
			}
		}
	})
	compactDone := make(chan error, 1)
	go func() { compactDone <- s.Compact(context.Background()) }()
	<-inWindow

	// The request builds against the parked fold. Its history snapshot runs
	// either mid-commit or behind the door, and either way must not adopt the
	// half-committed fold.
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterModelHistorySnapshot = func() { close(releaseFold) }
	})
	requestDone := make(chan error, 1)
	go func() {
		var timings events.RoundTimings
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
		requestDone <- err
	}()
	if err := <-compactDone; err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := <-requestDone; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	// The scenario really ran: the fold compacted the seeded prefix and left
	// its marker in the live history, so a frontier was published for the
	// snapshot to observe.
	live := currentHistory(t, s)
	if at := indexOfTurnText(live, "opaque-fold-commit-window-input-0"); at >= 0 {
		t.Fatalf("the parked fold did not compact the seeded prefix: the first input still stands at %d, so the window under test never opened", at)
	}
	foundMarker := false
	for _, turn := range live {
		foundMarker = foundMarker || turn.Kind == schema.TurnCheckpoint || turn.Kind == schema.TurnSummary
	}
	if !foundMarker {
		t.Fatal("the parked fold left no compaction marker in the live history, so no frontier was published for the snapshot to observe")
	}

	s.mu.Lock()
	prepared := slices.Clone(s.conversation.preparedInputs)
	s.mu.Unlock()
	for _, id := range original {
		if !slices.Contains(prepared, id) {
			t.Fatalf("a request built while the fold committed lost the folded input %s: prepared=%v", id, prepared)
		}
	}
	if state := s.ConversationProvenance(); !state.Authoritative {
		t.Fatalf("a request built while the fold committed made the conversation unknown: state=%+v", state)
	}
}

// TestLeasedRequestSnapshotDuringFoldCommitKeepsCoverageKnown pins the same
// pairing on the leased-delegate path. A leased round presents the
// controller's own snapshot, taken after the round's first capture; a fold
// committing between the two reads leaves that snapshot holding the fold's
// marker while the staged coverage still describes the pre-fold chain, and
// resolving the marker's frontier against that map observes the fold
// half-committed. Here a Compact parks between its publication and its
// transcript commit while a leased request builds; the leased read must pair
// its history with the coverage of the same read, and the request's coverage
// must stay known and complete.
func TestLeasedRequestSnapshotDuringFoldCommitKeepsCoverageKnown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const summaryProvider = "leased-fold-commit-window-cheap"
	profile := WithCheapModel(newAnthropicProfile("claude-opus-4-6"), summaryProvider+"/model")
	adapter := &agenttest.ScriptedAdapter{Provider: profile.ID(), Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("opaque-leased-fold-commit-window-result")
	}}
	s := newScriptedSummaryCompactSession(t, summaryProvider, func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-leased-fold-commit-window-summary\n[END SUMMARY]")}
	}, withProfile(profile), withAdapter(adapter), withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	s.delegateController = c
	c.mu.Lock()
	c.live[lease.delegateID].runtime = s
	c.live[lease.delegateID].binding.runtime = s
	c.mu.Unlock()

	// The strategy is cleared so the request below runs no context-management
	// fold of its own: the fold spawned from the snapshot hook must be the
	// only writer holding the transcript door, and the request must reach its
	// leased read while that fold is parked mid-commit — the interleaving
	// this test pins. The leased read itself is the same code every leased
	// round runs.
	s.mu.Lock()
	s.strategy = nil
	s.mu.Unlock()
	const rounds = 4
	for i := range rounds {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("opaque-leased-fold-commit-window-input-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	original := s.ConversationProvenance().AdmittedInputs
	if len(original) != rounds {
		t.Fatalf("admitted inputs before the fold = %v, want %d", original, rounds)
	}

	// Anthropic replays another model's signed block within the in-flight
	// round. Adjacent turns from that model distinguish both sides of the
	// boundary after the fold moves it left.
	const leasedCompletedText = "opaque-leased-fold-commit-window-completed"
	const leasedInFlightText = "opaque-leased-fold-commit-window-in-flight"
	const priorModel = "claude-sonnet-4-6"
	completed := assistantThinkingTurn(profile.ID(), priorModel, priorModel)
	completed.ResponseProtocol = profile.Protocol()
	completed.Message.Content[0].Thinking.Signature = "sig-completed"
	completed.Message.Content[1].Text = leasedCompletedText
	inFlight := assistantThinkingTurn(profile.ID(), priorModel, priorModel)
	inFlight.ResponseProtocol = profile.Protocol()
	inFlight.Message.Content[0].Thinking.Signature = "sig-in-flight"
	inFlight.Message.Content[1].Text = leasedInFlightText
	s.mu.Lock()
	s.history = append(s.history, completed, inFlight)
	s.turnHistoryBaseline = len(s.history) - 1
	s.mu.Unlock()

	// The snapshot hook spawns the fold only after the round's first capture,
	// then holds the request between its two history reads until the fold
	// parks: the leased read must land inside the park, never before the fold
	// publishes.
	inWindow := make(chan struct{})
	releaseFold := make(chan struct{})
	compactDone := make(chan error, 1)
	beforeFoldTurns := 0
	beforeFoldBaseline := 0
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			close(inWindow)
			select {
			case <-releaseFold:
			// TRIPWIRE: the request's completion releases this park in
			// milliseconds once its leased read passes the window; when that
			// read instead waits on the door this park holds, a lock waiter
			// exposes no signal to await, so this far-above ceiling is what
			// ends the park for it.
			case <-time.After(2 * time.Second):
			}
		}
		cfg.afterModelHistorySnapshot = func() {
			s.mu.Lock()
			beforeFoldTurns = len(s.history)
			beforeFoldBaseline = s.turnHistoryBaseline
			s.mu.Unlock()
			go func() { compactDone <- s.Compact(context.Background()) }()
			<-inWindow
		}
	})
	requestDone := make(chan error, 1)
	builtReq := make(chan llm.Request, 1)
	go func() {
		var timings events.RoundTimings
		ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
		// Round 1, not 0: round zero initializes the boundary to the current
		// history length, which would erase the in-flight boundary seeded above
		// and make the exemption vacuous.
		_, _, _, req, _, _, err := s.prepareModelRequestWithError(ctx, 1, &timings)
		builtReq <- req
		requestDone <- err
	}()
	if err := <-requestDone; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	req := <-builtReq
	close(releaseFold)
	if err := <-compactDone; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The scenario really ran: the fold compacted the seeded prefix and left
	// its marker in the live history, so a frontier was published for the
	// leased read to observe.
	live := currentHistory(t, s)
	if at := indexOfTurnText(live, "opaque-leased-fold-commit-window-input-0"); at >= 0 {
		t.Fatalf("the parked fold did not compact the seeded prefix: the first input still stands at %d, so the window under test never opened", at)
	}
	foundMarker := false
	for _, turn := range live {
		foundMarker = foundMarker || turn.Kind == schema.TurnCheckpoint || turn.Kind == schema.TurnSummary
	}
	if !foundMarker {
		t.Fatal("the parked fold left no compaction marker in the live history, so no frontier was published for the leased read to observe")
	}
	// The fold and the leased read are serialized by the transcript door, so
	// exactly one of them goes first. Whichever wins, the request must be
	// self-consistent: it either presents the pre-fold history (the fold had
	// not published when it read) or the compacted one (the fold went first).
	// A request built from a half-committed fold — the marker present with the
	// folded-away input still standing — is the failure this pins.
	presentedInput := false
	presentedSummary := false
	for _, m := range req.Messages {
		presentedInput = presentedInput || strings.Contains(m.Text(), "opaque-leased-fold-commit-window-input-0")
		presentedSummary = presentedSummary || strings.Contains(m.Text(), "[CONTEXT SUMMARY]")
	}
	if !presentedInput && !presentedSummary {
		t.Fatal("the leased request dropped the folded-away input but presented no summary: it read a fold mid-commit")
	}

	s.mu.Lock()
	prepared := slices.Clone(s.conversation.preparedInputs)
	s.mu.Unlock()
	for _, id := range original {
		if !slices.Contains(prepared, id) {
			t.Fatalf("a leased request built while the fold committed lost the folded input %s: prepared=%v", id, prepared)
		}
	}
	if state := s.ConversationProvenance(); !state.Authoritative {
		t.Fatalf("a leased request built while the fold committed made the conversation unknown: state=%+v", state)
	}

	// The boundary indexes the projection it ships with. A fold landing between
	// the round's first capture and the leased read shrinks the history, so the
	// earlier boundary can point past what the leased snapshot holds — or at a
	// turn that is not the first of the in-flight round. Either way an in-flight
	// round loses the replay exemption that keeps its thinking on a
	// same-protocol fallback. Assert on the request this path actually built,
	// not on a later re-projection: only the built request shows which boundary
	// the round used.
	if beforeFoldBaseline >= beforeFoldTurns {
		t.Fatalf("the round's pre-fold capture held %d turns and a boundary at %d: no turn was in flight, so this window proves nothing about re-pairing the boundary", beforeFoldTurns, beforeFoldBaseline)
	}
	row := profile.Resolved()
	body, err := anthropic.DefaultProtocol.BuildBody(llm.ShapeRequest(req, row), row)
	if err != nil {
		t.Fatal(err)
	}
	var signatures []string
	var carriedCompleted, carriedInFlight bool
	for _, msg := range body["messages"].([]map[string]any) {
		for _, part := range msg["content"].([]map[string]any) {
			if part["type"] == "thinking" {
				signatures = append(signatures, part["signature"].(string))
			}
			carriedCompleted = carriedCompleted || part["text"] == leasedCompletedText
			carriedInFlight = carriedInFlight || part["text"] == leasedInFlightText
		}
	}
	if !slices.Equal(signatures, []string{inFlight.Message.Content[0].Thinking.Signature}) {
		t.Fatalf("the leased request sent signatures %v, want only the in-flight signature (pre-fold boundary %d, request built from %d messages)", signatures, beforeFoldBaseline, len(req.Messages))
	}
	if !carriedCompleted || !carriedInFlight {
		t.Fatalf("leased request carried completed answer = %t, in-flight answer = %t, want both", carriedCompleted, carriedInFlight)
	}
	// The divergence the exemption rests on: the fold moved the in-flight turn
	// left, so the pre-fold boundary no longer indexes it. Reading a fresh
	// projection here would confirm the session's own bookkeeping, which the
	// fold corrects either way; this check proves the two captures disagree.
	projection := s.delegateModelHistorySnapshotWithCoverage()
	if projection.inFlightFrom < 0 || projection.inFlightFrom > len(projection.turns) {
		t.Fatalf("leased projection boundary %d does not index its own snapshot of %d turns", projection.inFlightFrom, len(projection.turns))
	}
	if len(projection.turns) >= beforeFoldTurns {
		t.Fatalf("the fold did not shrink the snapshot (%d turns, before %d): the two captures agree, so this window proves nothing about re-pairing the boundary", len(projection.turns), beforeFoldTurns)
	}
	if projection.inFlightFrom >= beforeFoldBaseline {
		t.Fatalf("the fold left the boundary where the round's first capture put it (%d, was %d) while shrinking the snapshot to %d turns: a boundary borrowed from the earlier capture indexes a turn this snapshot no longer holds, so the window never opens", projection.inFlightFrom, beforeFoldBaseline, len(projection.turns))
	}
	if at := indexOfTurnText(projection.turns, leasedInFlightText); at != projection.inFlightFrom {
		t.Fatalf("projection boundary %d does not open at the in-flight turn's position %d", projection.inFlightFrom, at)
	}
}

// TestLeasedRequestKeepsASteerAcceptedAfterTheFirstCapture pins the leased
// snapshot's reach past history expansion. A leased round expands its request
// from the controller's own later snapshot, but continuation planning used to
// be handed the round's FIRST capture. Planning may rebuild the messages from
// the turns it is given — a delta is expressed relative to an anchor — so a
// turn present in the expanded request but absent from that earlier capture was
// dropped from the request the provider received, and the session then marked
// it consumed.
//
// The turn planted here is a message accepted while the round runs, so it can
// only reach the request through the later snapshot. It is user input rather
// than a steering turn because a continuation delta is only expressible over
// the kinds planning accepts, and a steering turn would force full history and
// leave this window unexercised.
func TestLeasedRequestKeepsASteerAcceptedAfterTheFirstCapture(t *testing.T) {
	t.Parallel()
	const steerText = "opaque-leased-continuation-steer"
	root, dir := t.TempDir(), t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	}
	adapter.SetSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("opaque-leased-continuation-result") },
	})
	client := llm.NewClient()
	client.Register(adapter)
	s := newScriptedSummaryCompactSession(t, "leased-continuation-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque\n[END SUMMARY]")}
	}, withClient(client), withConfig(SessionConfig{
		StateDir:        dir,
		MemoryStateRoot: root,
		// Continuation must be live, or planning returns before it can rebuild
		// anything and the window this test opens goes unexercised.
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       bwrapCapableProber(t.TempDir()),
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
		},
	}), withStateDir(dir))
	defer s.Close()
	drainSessionEvents(s)

	// An eligible anchor, so planning takes the delta path rather than falling
	// back to full history.
	anchor := phase9MatchingAnchor("resp_leased_continuation")
	anchor.Timestamp = time.Now()
	s.mu.Lock()
	s.history = append(s.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("opaque-leased-continuation-pre-anchor")),
		anchor,
		// A turn already after the anchor: without it the delta over the
		// round's first capture is empty and planning falls back to full
		// history, which carries every message and would hide the defect this
		// test exists to catch.
		schema.NewTurn(schema.TurnUserInput, llm.User("opaque-leased-continuation-first-delta-turn")),
	)
	s.strategy = nil
	s.mu.Unlock()

	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	s.delegateController = c
	c.mu.Lock()
	c.live[lease.delegateID].runtime = s
	c.live[lease.delegateID].binding.runtime = s
	c.mu.Unlock()

	// The steer lands after the round's first capture and before its leased
	// read: exactly the window a stale continuation slice cannot see.
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.afterModelHistorySnapshot = func() {
			s.mu.Lock()
			s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(steerText)))
			s.mu.Unlock()
		}
	})

	var timings events.RoundTimings
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	_, _, _, req, _, _, err := s.prepareModelRequestWithError(ctx, 0, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	// The scenario ran only if planning really chose the delta path: on full
	// history the whole request is present and a dropped steer is impossible.
	if req.HistoryMode != llm.HistoryModeResponsesDelta {
		t.Fatalf("history mode = %q, want %q: the request did not take the continuation path this window is about", req.HistoryMode, llm.HistoryModeResponsesDelta)
	}
	if !requestMessagesContainText(req.Messages, steerText) {
		t.Fatal("the leased request dropped a message accepted after the round's first capture: continuation planning rebuilt the messages from the earlier snapshot")
	}
}

// TestConsecutiveWithheldFoldsKeepTheConversationCovered pins the retracted
// frontier's lifetime across two withheld folds. The first fold mints a node
// and keeps it in the chain as the live-only head. A later fold whose folded
// coverage equals the durable frontier's inputs mints no replacement, and its
// marker references the standing frontier instead — so its withhold must
// leave the first fold's retraction in place. Clearing it would orphan the
// first node in the chain, and the next record's node, extending the same
// predecessor, would fork the chain — frontier resolution then rejects every
// node and voice output goes ineligible until restart. Here two consecutive
// withheld folds are followed by a new input, and the reply must stay
// eligible.
func TestConsecutiveWithheldFoldsKeepTheConversationCovered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "double-withhold-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-double-withhold-summary\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	final := func(llm.Request) llm.Response { return agenttest.FinalResponse("opaque-double-withhold-result") }
	steps := make([]func(llm.Request) llm.Response, 0, 3)
	for range 3 {
		steps = append(steps, final)
	}
	s.client.Register(newFakeAdapter("openai", steps...))
	if _, err := s.ProcessInput(context.Background(), "opaque-double-withhold-input-1", nil); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("opaque-double-withhold-filler-a-%d", i)))
		s.recordTurn(turn, turn)
	}
	if _, err := s.ProcessInput(context.Background(), "opaque-double-withhold-input-2", nil); err != nil {
		t.Fatal(err)
	}

	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &foldTailFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	s.attachTranscript(writer, nil)

	// The first fold folds the first input's turn and mints the node its
	// withheld marker leaves as the chain's live-only head.
	fs.arm(2)
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("only %d sync(s) since arming, want the first staged copy to land before the second failed: the fault hit the wrong write", n)
	}
	s.mu.Lock()
	firstRetracted := s.conversation.retractedFrontierID
	s.mu.Unlock()
	if firstRetracted == "" {
		t.Fatal("the first withheld fold minted no live coverage frontier")
	}
	live := currentHistory(t, s)
	if at := indexOfTurnText(live, "opaque-double-withhold-input-1"); at >= 0 {
		t.Fatalf("the first fold retained the turn it was to fold (at %d); the second fold would then mint a replacement and the pinned interleaving would not run", at)
	}
	if at := indexOfTurnText(live, "opaque-double-withhold-input-2"); at < 0 {
		t.Fatal("the first fold folded the remaining input's turn: the retained tail must keep it so the second fold's coverage can equal the frontier's inputs")
	}
	for _, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			t.Fatal("the first withheld fold published a compaction marker whose staged retained-tail copy write failed")
		}
	}

	// Additional recorded rounds move the remaining input's turn outside the
	// retained tail, so the second fold's folded coverage equals the durable
	// frontier's inputs and it mints no replacement node.
	for i := range 6 {
		turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("opaque-double-withhold-filler-b-%d", i)))
		s.recordTurn(turn, turn)
	}
	fs.arm(2)
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("only %d sync(s) since the second arming, want the first staged copy to land before the second failed: the fault hit the wrong write", n)
	}
	for _, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			t.Fatal("the second withheld fold published a compaction marker whose staged retained-tail copy write failed")
		}
	}
	if at := indexOfTurnText(currentHistory(t, s), "opaque-double-withhold-input-2"); at >= 0 {
		t.Fatalf("the second fold retained the remaining input's turn (at %d); its folded coverage must instead equal the frontier's inputs to mint no replacement", at)
	}
	s.mu.Lock()
	secondRetracted := s.conversation.retractedFrontierID
	secondFrontier := s.conversation.frontierID
	s.mu.Unlock()
	if secondFrontier == "" {
		t.Fatal("the second withheld fold lost the durable conversation frontier")
	}
	// The first fold's node must still be the chain's live-only head: the
	// second fold minted no node to replace it, and the first fold's live
	// marker still references it for coverage.
	if secondRetracted != firstRetracted {
		t.Fatalf("the second withheld fold moved the retracted frontier from %q to %q while minting no replacement node", firstRetracted, secondRetracted)
	}

	// The reply after both withheld folds is the output voice eligibility
	// judges: its node must replace the retracted head in the chain, not fork
	// beside it.
	if _, err := s.ProcessInput(context.Background(), "opaque-double-withhold-input-3", nil); err != nil {
		t.Fatal(err)
	}
	state := s.ConversationProvenance()
	var output *appwire.ConversationOutputProvenance
	for index, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if item, ok := apptranscript.CommunicateItem(entry.Turn.TurnID, index, entry.Turn); ok {
			output = item.ConversationProvenance
		}
	}
	if !appwire.ConversationOutputEligible(state, output) {
		t.Fatalf("the reply after two consecutive withheld folds is not eligible for voice output: state=%+v output=%+v", state, output)
	}
}
