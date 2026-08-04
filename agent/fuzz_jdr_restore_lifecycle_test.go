//go:build serffuzz

package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzJdrDelegateRestoreLifecycle drives the durable delegate lifecycle through
// the real Session and job-manager paths: a completed delegate is made
// runtime_lost, its retained child runtime is discarded, then delegate_send
// performs strict preflight, child restoration, resumed descriptor creation, and
// terminal finalization. Fuzzed mutations instead make the retained descriptor
// or state invalid and must be rejected without minting a replacement job.
//
// The only external boundaries are a scripted provider, fake clock, temporary
// filesystem, and a LocalExecutionEnvironment configured with EnvPolicyNone.
// That concrete local type is required by the production restore path to rebuild
// the persisted working-directory policy; the scripted child only communicates,
// so it never executes a tool, shell, subprocess, or network call.
//
// Semantic oracles:
//   - resumability assessment is deterministic and a valid preflight carries the
//     retained child meta/profile;
//   - rejected restore state leaves the durable job/delegate sets unchanged;
//   - a successful restore preserves one durable delegate while minting exactly
//     one linked replacement job whose descriptor retains the restoration
//     contract and updates only the parent job linkage;
//   - every completed job has one terminal event and one terminal output, and
//     reopening the store yields the same restored descriptor.
//
// The jdr_ prefix reserves this target's helpers for the delegate-restore lane.

type jdr_reader struct {
	data []byte
	pos  int
}

func (r *jdr_reader) b() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *jdr_reader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.b()) % n
}

const jdrChildResult = "jdr child completed"

func jdrResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
		"required": []string{"message"},
	}
}

func jdrEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "jdr",
		OSVersion:  "jdr-offline",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func jdrNewSession(t *testing.T) (*Session, *agenttest.FakeClock, string) {
	t.Helper()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	clk := agenttest.NewFakeClock()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("jdr parent") },
	})
	cfg := SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		clock:            clk,
		LLMSleep:         func(context.Context, time.Duration) error { return nil },
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		environmentInfo:     jdrEnvironmentInfo,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
		childClientFactory: func() *llm.Client {
			child := llm.NewClient()
			child.Register(&agenttest.ScriptedAdapter{
				Provider:  "openai",
				Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse(jdrChildResult) },
			})
			return child
		},
	}
	env := execenv.NewLocalExecutionEnvironment(workDir)
	env.EnvPolicy = execenv.EnvPolicyNone
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("jdr: NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	t.Cleanup(func() {
		if sess.jobManager != nil {
			sess.jobManager.abandonRunningJobs()
		}
		sess.Close()
		<-drainDone
	})
	return sess, clk, workDir
}

func jdrAssertEmptyStore(t *testing.T, s *Session) {
	t.Helper()
	jobs, err := s.jobManager.store.Load()
	if err != nil {
		t.Fatalf("jdr: load empty jobs: %v", err)
	}
	delegates, err := s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("jdr: load empty delegates: %v", err)
	}
	if len(jobs) != 0 || len(delegates) != 0 {
		t.Fatalf("jdr: invalid delegate request changed durable state: jobs=%v delegates=%v", jobs, delegates)
	}
}

func jdrExerciseStartRejection(t *testing.T, s *Session, kind int) {
	t.Helper()
	var args delegateArgs
	switch kind {
	case 1:
		args = delegateArgs{Task: "  "}
	case 2:
		args = delegateArgs{Task: "jdr invalid isolation", Isolation: "unsupported"}
	case 3:
		args = delegateArgs{Task: "jdr invalid allowance", DelegationAllowance: 99}
	default:
		return
	}
	res := s.createDelegate(context.Background(), args)
	if res.Err == nil || res.Status != jobstore.StatusFailed || res.Reason != "start_failed" {
		t.Fatalf("jdr: invalid delegate request result=%+v, want start failure", res)
	}
	jdrAssertEmptyStore(t, s)
}

func jdrCreateCompletedDelegate(t *testing.T, s *Session, task string) (*jobstore.JobRecord, delegateResult) {
	t.Helper()
	res := s.createDelegate(context.Background(), delegateArgs{
		Task:                task,
		Background:          false,
		BlockTimeoutMS:      500,
		DelegationAllowance: 0,
		ResultSchema:        jdrResultSchema(),
	})
	if res.Err != nil || res.Status != jobstore.StatusCompleted || res.JobID == "" || res.DelegateID == "" {
		t.Fatalf("jdr: create completed delegate=%+v, want terminal durable delegate", res)
	}
	rec := loadShellRecord(t, s.jobManager, res.JobID)
	if rec.Type != jobstore.JobDelegate || rec.DelegateID != res.DelegateID || !rec.Status.IsTerminal() {
		t.Fatalf("jdr: initial durable record=%+v, result=%+v", rec, res)
	}
	return rec, res
}

func jdrAssertInitialDescriptor(t *testing.T, s *Session, rec *jobstore.JobRecord, task, workDir string) string {
	t.Helper()
	if rec == nil || rec.DelegateRestore == nil {
		t.Fatalf("jdr: missing initial restore descriptor: %+v", rec)
	}
	desc := rec.DelegateRestore
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		t.Fatalf("jdr: decode initial transcript ref %q: %v", rec.TranscriptRef, err)
	}
	if desc.Version != 1 || desc.ChildSessionID != childID || desc.TranscriptRef != rec.TranscriptRef {
		t.Fatalf("jdr: initial descriptor linkage=%+v, record=%+v", desc, rec)
	}
	if desc.ParentSessionID != s.ID() || desc.ParentJobID != rec.JobID || desc.OwnerSessionID != s.ID() || desc.VisibleSessionID != s.ID() {
		t.Fatalf("jdr: initial descriptor parent linkage=%+v", desc)
	}
	if desc.Task != task || desc.ResolvedProfileID != "openai" || desc.ResolvedModel != "gpt-5.2" {
		t.Fatalf("jdr: initial descriptor launch fields=%+v", desc)
	}
	if desc.WorkingDir != workDir || desc.LocalEnvPolicy != "none" {
		t.Fatalf("jdr: initial descriptor environment=%q/%q, want %q/none", desc.WorkingDir, desc.LocalEnvPolicy, workDir)
	}
	if resultSchema := delegateResultSchemaMap(desc.ResultSchema); resultSchema == nil || resultSchema["type"] != "object" {
		t.Fatalf("jdr: initial descriptor result schema=%#v, want object", desc.ResultSchema)
	}
	if rec.Resumable == nil || !*rec.Resumable || rec.NotResumableWhy != "" {
		t.Fatalf("jdr: completed delegate resumability=%v/%q, want true/empty", rec.Resumable, rec.NotResumableWhy)
	}
	return childID
}

func jdrDiscardRetainedChild(t *testing.T, s *Session, childID string) {
	t.Helper()
	sub := s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("jdr: initial child %q was not retained", childID)
	}
	s.subagents.remove(childID)
	sub.sess.Close()
}

func jdrMakeRuntimeLost(t *testing.T, s *Session, rec *jobstore.JobRecord) *jobstore.JobRecord {
	t.Helper()
	setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusStopped, "runtime_lost")
	return loadShellRecord(t, s.jobManager, rec.JobID)
}

// jdrMutateRestore returns whether this state must be restorable. The first
// choice intentionally leaves the real descriptor untouched; every other choice
// breaks a distinct persisted preflight dependency before the send path reads it.
func jdrMutateRestore(t *testing.T, s *Session, rec *jobstore.JobRecord, kind int) bool {
	t.Helper()
	if kind == 0 {
		return true
	}
	desc := rec.DelegateRestore
	switch kind {
	case 1:
		rec.DelegateRestore = nil
	case 2:
		desc.ParentJobID = "job_wrong_parent"
	case 3:
		desc.ResolvedModel = ""
	case 4:
		desc.LocalEnvPolicy = "not-a-policy"
	case 5:
		desc.WorkingDir = desc.WorkingDir + "-missing"
	case 6:
		desc.FrozenSkillNames = []string{"jdr-skill"}
		desc.FrozenSkillBodies = nil
	case 7:
		if err := os.Remove(childSessionMetaPath(s, rec)); err != nil {
			t.Fatalf("jdr: remove retained child meta: %v", err)
		}
	case 8:
		if err := os.WriteFile(childTranscriptPath(s, rec), []byte("{not json}\n"), 0o600); err != nil {
			t.Fatalf("jdr: corrupt retained transcript: %v", err)
		}
	default:
		desc.TranscriptRef = "local:other-child"
	}
	if kind != 7 && kind != 8 {
		replaceStoredDelegateRecord(t, s, rec)
	}
	return false
}

func jdrAssertAssessment(t *testing.T, s *Session, rec *jobstore.JobRecord, wantResumable bool) {
	t.Helper()
	first := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
	second := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
	if first.Resumable != second.Resumable || first.Reason != second.Reason {
		t.Fatalf("jdr: resumability nondeterministic: first=%+v second=%+v", first, second)
	}
	if first.Resumable != wantResumable {
		t.Fatalf("jdr: resumability=%+v, want resumable=%t", first, wantResumable)
	}
	if !wantResumable {
		if first.Reason == "" || first.Preflight != nil {
			t.Fatalf("jdr: rejected resumability=%+v, want reason and no preflight", first)
		}
		return
	}
	if first.Preflight == nil || first.Preflight.Profile == nil || first.Preflight.Meta.ID != rec.DelegateRestore.ChildSessionID {
		t.Fatalf("jdr: valid resumability preflight=%+v", first.Preflight)
	}
}

func jdrCountTerminalEvents(t *testing.T, s *Session, jobID string) int {
	t.Helper()
	count := 0
	for _, event := range loadJobStoreEvents(t, s.jobManager) {
		if event.JobID == jobID && event.Kind == jobstore.EventJobFinished {
			count++
		}
	}
	return count
}

func jdrAssertResumedDescriptor(t *testing.T, before, after *jobstore.DelegateRestoreDescriptor, newJobID string) {
	t.Helper()
	if before == nil || after == nil {
		t.Fatalf("jdr: resumed descriptor missing: before=%+v after=%+v", before, after)
	}
	if after.Version != before.Version || after.ChildSessionID != before.ChildSessionID || after.TranscriptRef != before.TranscriptRef {
		t.Fatalf("jdr: resumed descriptor child linkage=%+v, before=%+v", after, before)
	}
	if after.ParentSessionID != before.ParentSessionID || after.OwnerSessionID != before.OwnerSessionID || after.VisibleSessionID != before.VisibleSessionID || after.ParentJobID != newJobID {
		t.Fatalf("jdr: resumed descriptor parent linkage=%+v, want parent job %q", after, newJobID)
	}
	if after.Task != before.Task || after.ResolvedProfileID != before.ResolvedProfileID || after.ResolvedModel != before.ResolvedModel || after.WorkingDir != before.WorkingDir || after.LocalEnvPolicy != before.LocalEnvPolicy || after.Isolation != before.Isolation {
		t.Fatalf("jdr: resumed descriptor changed retained restore contract: before=%+v after=%+v", before, after)
	}
	beforeSchema := delegateResultSchemaMap(before.ResultSchema)
	afterSchema := delegateResultSchemaMap(after.ResultSchema)
	if beforeSchema == nil || afterSchema == nil || beforeSchema["type"] != afterSchema["type"] || afterSchema["type"] != "object" {
		t.Fatalf("jdr: resumed descriptor result schema changed: before=%#v after=%#v", before.ResultSchema, after.ResultSchema)
	}
}

func jdrAssertOneTerminalOutput(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	if got := jdrCountTerminalEvents(t, s, rec.JobID); got != 1 {
		t.Fatalf("jdr: job %q terminal events=%d, want 1", rec.JobID, got)
	}
	output, _, _, err := s.jobManager.readOutput(rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("jdr: read output for %q: %v", rec.JobID, err)
	}
	line := strings.TrimSpace(output)
	if line == "" || !strings.HasSuffix(output, "\n") || strings.Count(output, line) != 1 {
		t.Fatalf("jdr: job %q must retain one complete terminal output, got %q", rec.JobID, output)
	}
}

func FuzzJdrDelegateRestoreLifecycle(f *testing.F) {
	// The first byte chooses an early create rejection, the second a persisted
	// descriptor/state mutation, and the third selects a bounded task label.
	for _, seed := range [][]byte{
		{},
		{0, 0, 0}, // accepted create + successful restore/resume
		{1, 1, 1}, // empty task, then missing descriptor
		{2, 2, 2}, // invalid isolation, then parent linkage mismatch
		{3, 3, 3}, // invalid allowance, then missing resolved model
		{0, 4, 0}, // invalid local env policy
		{0, 5, 1}, // missing working directory
		{0, 6, 2}, // mismatched frozen skills
		{0, 7, 0}, // missing child meta
		{0, 8, 1}, // corrupt child transcript
		{0, 9, 2}, // descriptor transcript mismatch
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &jdr_reader{data: data}
		s, clk, workDir := jdrNewSession(t)
		jdrExerciseStartRejection(t, s, r.intn(4))

		task := []string{"jdr inspect durable state", "jdr resume retained child", "jdr verify terminal result"}[r.intn(3)]
		initial, started := jdrCreateCompletedDelegate(t, s, task)
		childID := jdrAssertInitialDescriptor(t, s, initial, task, workDir)
		jdrAssertOneTerminalOutput(t, s, initial)

		// A terminal delegate normally remains retained. Discarding its in-memory
		// child simulates process loss while keeping the real state/meta/transcript
		// files that the strict restore path consumes.
		jdrDiscardRetainedChild(t, s, childID)
		rec := jdrMakeRuntimeLost(t, s, initial)
		wantResumable := jdrMutateRestore(t, s, rec, r.intn(10))
		rec = loadShellRecord(t, s.jobManager, rec.JobID)
		jdrAssertAssessment(t, s, rec, wantResumable)

		beforeJobs, err := s.jobManager.store.Load()
		if err != nil {
			t.Fatalf("jdr: load jobs before send: %v", err)
		}
		beforeDelegates, err := s.jobManager.store.LoadDelegates()
		if err != nil {
			t.Fatalf("jdr: load delegates before send: %v", err)
		}
		if len(beforeJobs) != 1 || len(beforeDelegates) != 1 {
			t.Fatalf("jdr: unexpected initial durable sets: jobs=%v delegates=%v", beforeJobs, beforeDelegates)
		}

		result := s.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:         started.DelegateID,
			Message:        "jdr resume now",
			BackgroundSet:  true,
			Background:     false,
			BlockTimeoutMS: 500,
		})
		if !wantResumable {
			if result.Err == nil || !strings.Contains(result.Err.Error(), "target_not_resumable") || result.Action != "" {
				t.Fatalf("jdr: invalid restore send=%+v, want target_not_resumable", result)
			}
			afterJobs, err := s.jobManager.store.Load()
			if err != nil {
				t.Fatalf("jdr: load jobs after rejected send: %v", err)
			}
			afterDelegates, err := s.jobManager.store.LoadDelegates()
			if err != nil {
				t.Fatalf("jdr: load delegates after rejected send: %v", err)
			}
			if len(afterJobs) != len(beforeJobs) || len(afterDelegates) != len(beforeDelegates) || afterDelegates[started.DelegateID].LatestJobID != rec.JobID {
				t.Fatalf("jdr: rejected send changed durable state: jobs=%v delegates=%v", afterJobs, afterDelegates)
			}
			return
		}

		if result.Err != nil || result.Action != "started" || result.Status != jobstore.StatusCompleted || result.JobID == "" || result.JobID == rec.JobID || result.DelegateID != started.DelegateID || result.ResumedFromJobID != rec.JobID {
			t.Fatalf("jdr: restored delegate send=%+v, prior=%+v", result, rec)
		}
		resumed := loadShellRecord(t, s.jobManager, result.JobID)
		if resumed.Status != jobstore.StatusCompleted || resumed.DelegateID != started.DelegateID || resumed.Resumable == nil || !*resumed.Resumable {
			t.Fatalf("jdr: resumed durable record=%+v", resumed)
		}
		jdrAssertResumedDescriptor(t, rec.DelegateRestore, resumed.DelegateRestore, resumed.JobID)
		jdrAssertOneTerminalOutput(t, s, resumed)
		if sub := s.subagents.get(childID); sub == nil || sub.sess == nil || sub.sess.clock != clk || sub.sess.jobManager == nil || sub.sess.jobManager.clock != clk {
			t.Fatalf("jdr: restored child lost deterministic clock/runtime boundary: %+v", sub)
		} else if got := sub.sess.Meta().EnvInfo.OSVersion; got != "jdr-offline" {
			t.Fatalf("jdr: restored child escaped injected environment boundary: os_version=%q", got)
		}

		afterJobs, err := s.jobManager.store.Load()
		if err != nil {
			t.Fatalf("jdr: load jobs after resumed send: %v", err)
		}
		afterDelegates, err := s.jobManager.store.LoadDelegates()
		if err != nil {
			t.Fatalf("jdr: load delegates after resumed send: %v", err)
		}
		if len(afterJobs) != len(beforeJobs)+1 || len(afterDelegates) != len(beforeDelegates) || afterDelegates[started.DelegateID].LatestJobID != resumed.JobID {
			t.Fatalf("jdr: resumed send durable linkage: jobs=%v delegates=%v", afterJobs, afterDelegates)
		}

		// The reopened store is the persistence oracle: the resumed descriptor must
		// survive independent event-log folding, not merely remain in the live run.
		reopened, err := newJobManager(s.stateDir, s.ID(), func(jobNotification) {})
		if err != nil {
			t.Fatalf("jdr: reopen job manager: %v", err)
		}
		defer func() { _ = reopened.store.Close() }()
		persisted := loadShellRecord(t, reopened, resumed.JobID)
		jdrAssertResumedDescriptor(t, resumed.DelegateRestore, persisted.DelegateRestore, persisted.JobID)
	})
}
