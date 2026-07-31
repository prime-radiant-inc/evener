package agent

import (
	"fmt"
	"strings"
	"testing"
)

// readJobTranscriptFor spends the model-facing job:<job_id> read for one
// session, through the same dependency surface newToolDeps installs.
func readJobTranscriptFor(t *testing.T, s *Session, jobID string) (readMarkdownEnvelope, error) {
	t.Helper()
	result, err := readJobTranscript(&toolDeps{jobRead: sessionJobRead(s)}, "job:"+jobID, "", formatMarkdown)
	if err != nil {
		return readMarkdownEnvelope{}, err
	}
	envelope, ok := result.(readMarkdownEnvelope)
	if !ok {
		t.Fatalf("readJobTranscript returned %T, want readMarkdownEnvelope", result)
	}
	return envelope, nil
}

// TestReadTranscriptServesGrantedJobOutput is the spec §5.1 consumption
// direction on the sanctioned surface: the observer cannot resolve the watched
// job locally (it lives in the parent's store), so the read resolves through
// the durable grant the job.notification delivery minted, and returns the
// terminal job's retained output with honest byte accounting.
func TestReadTranscriptServesGrantedJobOutput(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)

	envelope, err := readJobTranscriptFor(t, fx.observer, fx.watched)
	if err != nil {
		t.Fatalf("granted read_transcript: %v", err)
	}
	if envelope.TranscriptRef != "job:"+fx.watched {
		t.Fatalf("transcript_ref = %q, want job:%s", envelope.TranscriptRef, fx.watched)
	}
	if !strings.Contains(envelope.Content, grantReadWatchedOutput) {
		t.Fatalf("content = %q, want the watched job's retained output", envelope.Content)
	}
	wantBytes := fmt.Sprintf("total_bytes: %d", len(grantReadWatchedOutput))
	if !strings.Contains(envelope.Content, wantBytes) {
		t.Fatalf("content = %q, want %q", envelope.Content, wantBytes)
	}
	if strings.Contains(envelope.Content, "dropped_bytes") {
		t.Fatalf("content = %q, want no dropped bytes for a fully retained job", envelope.Content)
	}
	if envelope.Meta.Truncated {
		t.Fatalf("meta = %+v, want an untruncated read of a fully retained job", envelope.Meta)
	}
}

// TestReadTranscriptStrangerKeepsOriginalNotFound: a session holding no grant
// gets back the byte-identical error it would get for a job id that does not
// exist anywhere. The message must never become an oracle for "this job is
// real, you just cannot read it".
func TestReadTranscriptStrangerKeepsOriginalNotFound(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)

	strangerJM := newWalkJobManager(t, "child_job_other")
	t.Cleanup(func() { _ = strangerJM.store.Close() })
	stranger := &Session{id: "child_job_other", jobManager: strangerJM, subagents: newSubagentManager(nil, 0)}
	stranger.cfg.spawn.parentGrantedJobRead = fx.parent.lookupGrantedJobRead

	_, err := readJobTranscriptFor(t, stranger, fx.watched)
	if err == nil {
		t.Fatal("stranger read succeeded, want the original not-found")
	}
	want := errJobNotFound(fx.watched).Error()
	if err.Error() != want {
		t.Fatalf("stranger error = %q, want %q", err.Error(), want)
	}

	// The same session asking for an id that exists nowhere gets the same shape,
	// so the two answers are indistinguishable.
	_, invented := readJobTranscriptFor(t, stranger, "job_does_not_exist")
	if invented == nil || invented.Error() != errJobNotFound("job_does_not_exist").Error() {
		t.Fatalf("invented-id error = %v, want the same not-found shape", invented)
	}
}

// TestReadTranscriptResolvesDescendantJobAtDepthTwo is Jesse's ruling 6: the
// one-hop resolver reaches only direct children, so a job owned at depth >= 2
// resolves through the live-subtree walk. cf84923c6 left this with no
// model-facing tool at all; the job: read path is now that tool.
func TestReadTranscriptResolvesDescendantJobAtDepthTwo(t *testing.T) {
	t.Parallel()
	root, _, worker, workerJobID := newDepthTwoJobTree(t)

	envelope, err := readJobTranscriptFor(t, root, workerJobID)
	if err != nil {
		t.Fatalf("depth-2 read_transcript: %v", err)
	}
	if !strings.Contains(envelope.Content, "worker grandchild line") {
		t.Fatalf("content = %q, want the worker's bytes", envelope.Content)
	}
	// Served from the OWNER's store: the root never held these bytes.
	if _, err := findJobRecord(root.jobManager, workerJobID); err == nil {
		t.Fatal("root store holds the worker job; the walk assertion would be vacuous")
	}
	if _, err := findJobRecord(worker.jobManager, workerJobID); err != nil {
		t.Fatalf("owner store missing the worker job: %v", err)
	}
}

// TestReadTranscriptChainFallsThroughWalkToGrantThenError pins the resolution
// ORDER (ruling 6): with a live subtree that owns nothing matching, the walk
// falls through to the grant table, and an id absent from both keeps the error
// the earlier steps produced.
func TestReadTranscriptChainFallsThroughWalkToGrantThenError(t *testing.T) {
	t.Parallel()
	fx := newGrantReadFixture(t)

	// Give the granted observer a live child of its own. The walk now has a
	// subtree to search, finds nothing, and must continue to the grant.
	childJM := newWalkJobManager(t, "child_of_observer")
	t.Cleanup(func() { _ = childJM.store.Close() })
	child := &Session{id: "child_of_observer", jobManager: childJM, subagents: newSubagentManager(nil, 0)}
	fx.observer.subagents = newSubagentManager(nil, 0)
	fx.observer.subagents.track(&subagent{id: "child_of_observer", sess: child, status: SubagentRunning})

	envelope, err := readJobTranscriptFor(t, fx.observer, fx.watched)
	if err != nil {
		t.Fatalf("granted read after a fruitless walk: %v", err)
	}
	if !strings.Contains(envelope.Content, grantReadWatchedOutput) {
		t.Fatalf("content = %q, want the watched job's output", envelope.Content)
	}

	// A parent job the observer was never granted stops at the original error.
	ungranted, err := fx.parentJM.createShell(createShellOpts{Command: "other"})
	if err != nil {
		t.Fatalf("create ungranted job: %v", err)
	}
	if _, err := readJobTranscriptFor(t, fx.observer, ungranted.JobID); err == nil || err.Error() != errJobNotFound(ungranted.JobID).Error() {
		t.Fatalf("ungranted read error = %v, want %q", err, errJobNotFound(ungranted.JobID).Error())
	}
}

// newDepthTwoJobTree builds root -> coord -> worker with single-hop forwarding
// and one running worker-owned shell job carrying output. It is the minimal
// topology in which a job is owned at depth 2 relative to the root.
func newDepthTwoJobTree(t *testing.T) (root, coord, worker *Session, workerJobID string) {
	t.Helper()
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"

	rec, err := workerJM.createShell(createShellOpts{Command: "sleep 1", Description: "worker job"})
	if err != nil {
		t.Fatalf("create worker shell: %v", err)
	}
	workerJM.mu.Lock()
	run := workerJM.running[rec.JobID]
	workerJM.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("worker job %q has no live output store", rec.JobID)
	}
	if _, err := workerJM.appendJobOutput(rec.JobID, run.output, []byte("worker grandchild line\n")); err != nil {
		t.Fatalf("append worker output: %v", err)
	}

	worker = &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil, 0)}
	coord = &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil, 0)}
	coord.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root = &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil, 0)}
	root.subagents.track(&subagent{id: "COORD", sess: coord, status: SubagentRunning})
	return root, coord, worker, rec.JobID
}
