package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

type recordingArtifactStore struct {
	closeCount atomic.Int32
	onClose    func()
}

type fakeArtifactStore struct {
	puts   [][]byte
	ref    string
	putErr error
}

func newFakeArtifactStore() *fakeArtifactStore {
	return &fakeArtifactStore{ref: "artifact:abc"}
}

func (s *fakeArtifactStore) Put(data []byte) (string, error) {
	s.puts = append(s.puts, append([]byte(nil), data...))
	if s.putErr != nil {
		return "", s.putErr
	}
	return s.ref, nil
}

func (s *fakeArtifactStore) Open(string) (*os.File, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeArtifactStore) Close() error { return nil }

func (s *recordingArtifactStore) Put([]byte) (string, error) {
	return "", errors.New("not implemented")
}

func (s *recordingArtifactStore) Open(string) (*os.File, error) {
	return nil, errors.New("not implemented")
}

func (s *recordingArtifactStore) Close() error {
	s.closeCount.Add(1)
	if s.onClose != nil {
		s.onClose()
	}
	return nil
}

func installArtifactStoreFactory(t *testing.T, factory func() (artifactStore, error)) {
	t.Helper()
	previous := sessionArtifactStoreFactory
	sessionArtifactStoreFactory = factory
	t.Cleanup(func() { sessionArtifactStoreFactory = previous })
}

func replaceRootArtifactStore(t *testing.T, root *Session, store artifactStore) {
	t.Helper()
	if root.artifactStore == nil {
		t.Fatal("root has no artifact store")
	}
	if err := root.artifactStore.Close(); err != nil {
		t.Fatalf("close original root store: %v", err)
	}
	// Keep cfg.artifactStore nil deliberately: production child wiring must copy
	// the Session field, not rely on a test-preloaded child config.
	root.artifactStore = store
	root.ownsArtifactStore = true
}

func newArtifactTestRoot(t *testing.T) *Session {
	t.Helper()
	return newSession(t,
		withSteps(func(llm.Request) llm.Response { return finalResponse("child done") }),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		}),
	)
}

func artifactRestoreMeta(t *testing.T) schema.SessionMeta {
	t.Helper()
	id, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return schema.SessionMeta{ID: id, ProfileID: "openai", Model: "gpt-5.2"}
}

func artifactRestoreConfig(t *testing.T, stateDir string) RestoreSessionConfig {
	t.Helper()
	return RestoreSessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
		deferRestoreSideEffects: true,
	}
}

func newArtifactTestClient() *llm.Client {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	return client
}

func TestRetainToolArtifactUsesRecoverableOutput(t *testing.T) {
	store := newFakeArtifactStore()
	s := &Session{artifactStore: store}
	res := tool.ExecResult{
		Output:            "preview",
		FullOutput:        "event",
		RecoverableOutput: "model full",
		Truncated:         true,
	}

	ref := s.retainToolArtifact(&res)

	if ref != "artifact:abc" {
		t.Fatalf("ref = %q, want artifact:abc", ref)
	}
	if len(store.puts) != 1 || string(store.puts[0]) != "model full" {
		t.Fatalf("stored = %q, want exact recoverable output", store.puts)
	}
	if !strings.Contains(res.Output, "Full output: "+ref) {
		t.Fatalf("output = %q, want artifact handle footer", res.Output)
	}
	if !strings.Contains(res.Output, `read_transcript(transcript_ref="artifact:abc")`) {
		t.Fatalf("output = %q, want read_transcript instruction", res.Output)
	}
	if res.FullOutput != "event" {
		t.Fatalf("FullOutput = %q, want event text unchanged", res.FullOutput)
	}
}

func TestRetainToolArtifactFailureIsAvailabilityNeutralAndPreservesError(t *testing.T) {
	store := newFakeArtifactStore()
	const sensitivePath = "/Users/operator/.evener/private/session-artifacts/model-output"
	store.putErr = &os.PathError{
		Op:   "write",
		Path: sensitivePath,
		Err:  errors.New("permission denied: storage detail secret=fixture"),
	}
	s := &Session{artifactStore: store}
	res := tool.ExecResult{
		Output:            "preview",
		FullOutput:        "event error",
		RecoverableOutput: "model error",
		Truncated:         true,
		IsError:           true,
	}

	ref := s.retainToolArtifact(&res)

	if ref != "" {
		t.Fatalf("ref = %q, want no handle after failed retention", ref)
	}
	if len(store.puts) != 1 {
		t.Fatalf("Put called %d times after retention failure, want exactly once", len(store.puts))
	}
	if !res.IsError {
		t.Fatal("retention failure cleared tool error state")
	}
	const wantOutput = "preview\n[retention_failed: full output could not be retained]"
	if res.Output != wantOutput {
		t.Fatalf("output = %q, want stable generic warning %q", res.Output, wantOutput)
	}
	for _, unavailableClaim := range []string{"artifact:", "event stream", "Full output:", "Read with:"} {
		if strings.Contains(res.Output, unavailableClaim) {
			t.Fatalf("output = %q, must not claim unavailable output via %q", res.Output, unavailableClaim)
		}
	}
	for _, sensitiveDetail := range []string{sensitivePath, "permission denied", "storage detail", "secret=fixture"} {
		if strings.Contains(res.Output, sensitiveDetail) {
			t.Fatalf("output = %q leaked sensitive retention detail %q", res.Output, sensitiveDetail)
		}
	}
	if res.FullOutput != "event error" {
		t.Fatalf("FullOutput = %q, want original error event text", res.FullOutput)
	}
}

func TestRetainToolArtifactLeavesUntruncatedResultAlone(t *testing.T) {
	store := newFakeArtifactStore()
	s := &Session{artifactStore: store}
	res := tool.ExecResult{
		Output:            "complete",
		FullOutput:        "complete",
		RecoverableOutput: "complete",
	}

	if ref := s.retainToolArtifact(&res); ref != "" {
		t.Fatalf("ref = %q, want no handle for untruncated output", ref)
	}
	if len(store.puts) != 0 {
		t.Fatalf("Put called %d times for untruncated output", len(store.puts))
	}
	if res.Output != "complete" {
		t.Fatalf("Output = %q, want unchanged untruncated output", res.Output)
	}
}

func TestRetainToolArtifactExecToolPublishesSplitTextResultHandle(t *testing.T) {
	modelOutput := strings.Repeat("model text ", 2_001)
	const eventOutput = "event-facing text remains complete and separate"
	sess, stop := imageToolSession(t, "retain_split", func() (any, error) {
		return tool.TextResult{Output: modelOutput, FullOutput: eventOutput}, nil
	})
	store := newFakeArtifactStore()
	replaceRootArtifactStore(t, sess, store)

	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "call_split", Name: "retain_split", Arguments: []byte(`{}`)}, "")
	end := toolCallEndData(t, stop(), "call_split")

	if len(store.puts) != 1 {
		t.Fatalf("Put called %d times, want once", len(store.puts))
	}
	if string(store.puts[0]) != modelOutput {
		t.Fatalf("stored split output length = %d, want exact %d-byte model output", len(store.puts[0]), len(modelOutput))
	}
	if res.FullOutput != eventOutput || end.Output != eventOutput {
		t.Fatalf("full outputs = result %q, event %q; want %q", res.FullOutput, end.Output, eventOutput)
	}
	if end.Error != "" {
		t.Fatalf("event Error = %q for successful split result", end.Error)
	}
	if end.OutputRef != "artifact:abc" {
		t.Fatalf("event OutputRef = %q, want artifact:abc", end.OutputRef)
	}
	if !strings.Contains(res.Output, "Full output: artifact:abc") {
		t.Fatalf("model Output = %q, want artifact footer", res.Output)
	}
}

func TestRetainToolArtifactExecToolPreservesErrorEvent(t *testing.T) {
	errorOutput := "tool failed: " + strings.Repeat("error text ", 2_001)
	sess, stop := imageToolSession(t, "retain_error", func() (any, error) {
		return nil, errors.New(errorOutput)
	})
	store := newFakeArtifactStore()
	replaceRootArtifactStore(t, sess, store)

	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "call_error", Name: "retain_error", Arguments: []byte(`{}`)}, "")
	end := toolCallEndData(t, stop(), "call_error")

	if !res.IsError {
		t.Fatal("truncated error result lost IsError")
	}
	if len(store.puts) != 1 {
		t.Fatalf("Put called %d times, want once", len(store.puts))
	}
	if string(store.puts[0]) != errorOutput {
		t.Fatalf("stored error output length = %d, want exact %d bytes", len(store.puts[0]), len(errorOutput))
	}
	if res.FullOutput != errorOutput || end.Error != errorOutput {
		t.Fatalf("full error output changed: result length %d, event length %d, want %d", len(res.FullOutput), len(end.Error), len(errorOutput))
	}
	if end.Output != "" {
		t.Fatalf("event Output = %q for error result", end.Output)
	}
	if end.OutputRef != "artifact:abc" {
		t.Fatalf("event OutputRef = %q, want artifact:abc", end.OutputRef)
	}
}

func TestRetainToolArtifactExecToolRetentionFailureOmitsOutputRef(t *testing.T) {
	modelOutput := strings.Repeat("model text ", 2_001)
	const eventOutput = "event-facing text"
	sess, stop := imageToolSession(t, "retain_failure", func() (any, error) {
		return tool.TextResult{Output: modelOutput, FullOutput: eventOutput}, nil
	})
	store := newFakeArtifactStore()
	store.putErr = errors.New("retention unavailable")
	replaceRootArtifactStore(t, sess, store)

	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "call_failure", Name: "retain_failure", Arguments: []byte(`{}`)}, "")
	end := toolCallEndData(t, stop(), "call_failure")

	if len(store.puts) != 1 {
		t.Fatalf("Put called %d times after common-seam retention failure, want exactly once", len(store.puts))
	}
	if end.OutputRef != "" {
		t.Fatalf("event OutputRef = %q after failed retention", end.OutputRef)
	}
	if end.Output != eventOutput || end.Error != "" {
		t.Fatalf("event payload changed after retention failure: Output=%q Error=%q", end.Output, end.Error)
	}
	if !strings.Contains(res.Output, "retention_failed") || strings.Contains(res.Output, "artifact:") {
		t.Fatalf("model Output = %q, want availability-neutral retention warning", res.Output)
	}
}

func TestSessionArtifactStoreSharedByDescendantsOnly(t *testing.T) {
	rootA := newArtifactTestRoot(t)
	rootB := newArtifactTestRoot(t)
	childID := spawnRuntimeAgent(t, rootA, "child task", "", 1, "", "", nil)
	child := rootA.getSub(childID)
	if child == nil || child.sess == nil {
		t.Fatal("production child was not tracked")
	}
	if rootA.artifactStore != child.sess.artifactStore {
		t.Fatal("production child did not inherit store")
	}
	if rootA.artifactStore == rootB.artifactStore {
		t.Fatal("independent roots shared store")
	}
	if !rootA.ownsArtifactStore || child.sess.ownsArtifactStore {
		t.Fatal("wrong ownership")
	}
}

func TestSessionArtifactStoreChildCloseDoesNotCloseStore(t *testing.T) {
	root := newArtifactTestRoot(t)
	store := &recordingArtifactStore{}
	replaceRootArtifactStore(t, root, store)
	childID := spawnRuntimeAgent(t, root, "child task", "", 1, "", "", nil)
	child := root.getSub(childID)
	if child == nil || child.sess == nil {
		t.Fatal("production child was not tracked")
	}

	child.sess.Close()
	if got := store.closeCount.Load(); got != 0 {
		t.Fatalf("child close closed store %d times", got)
	}
	root.Close()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("root close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreRootCascadeClosesTrackedChildFirstAndExactlyOnce(t *testing.T) {
	root := newArtifactTestRoot(t)
	store := &recordingArtifactStore{}
	replaceRootArtifactStore(t, root, store)
	childID := spawnRuntimeAgent(t, root, "child task", "", 1, "", "", nil)
	child := root.getSub(childID)
	if child == nil || child.sess == nil {
		t.Fatal("production child was not tracked")
	}
	store.onClose = func() {
		child.sess.mu.Lock()
		defer child.sess.mu.Unlock()
		if child.sess.state != SessionClosed {
			t.Errorf("store closed before tracked child shutdown: state=%s", child.sess.state)
		}
	}

	root.Close()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			root.Close()
		})
	}
	wg.Wait()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("repeated/concurrent root close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreOwnedFreshConstructorFailureClosesStore(t *testing.T) {
	store := &recordingArtifactStore{}
	installArtifactStoreFactory(t, func() (artifactStore, error) { return store, nil })
	want := errors.New("new job manager fault")
	_, err := NewSession(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sessionInitFault: func(point string) error {
				if point == "new_job_manager" {
					return want
				}
				return nil
			},
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("NewSession error = %v, want %v", err, want)
	}
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("fresh constructor failure close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreOwnedRestoredConstructorFailureClosesStore(t *testing.T) {
	store := &recordingArtifactStore{}
	installArtifactStoreFactory(t, func() (artifactStore, error) { return store, nil })
	want := errors.New("restored job manager fault")
	cfg := artifactRestoreConfig(t, t.TempDir())
	cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return want
		}
		return nil
	}
	_, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), artifactRestoreMeta(t), cfg)
	if !errors.Is(err, want) {
		t.Fatalf("RestoreSession error = %v, want %v", err, want)
	}
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("restored constructor failure close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreInheritedFreshConstructorFailurePreservesStore(t *testing.T) {
	root := newArtifactTestRoot(t)
	store := &recordingArtifactStore{}
	replaceRootArtifactStore(t, root, store)
	want := errors.New("child job manager fault")
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "new_job_manager" {
			return want
		}
		return nil
	}

	if _, err := root.spawnAgent(context.Background(), "child task", "", "", 1, "", "", nil, nil); !errors.Is(err, want) {
		t.Fatalf("production child constructor error = %v, want %v", err, want)
	}
	if got := store.closeCount.Load(); got != 0 {
		t.Fatalf("inherited store closed by failed child constructor %d times", got)
	}
	root.Close()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("root close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreDiscardOwnedRestoredCandidateClosesStore(t *testing.T) {
	store := &recordingArtifactStore{}
	installArtifactStoreFactory(t, func() (artifactStore, error) { return store, nil })
	candidate, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), artifactRestoreMeta(t), artifactRestoreConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("restore candidate: %v", err)
	}
	candidate.discardRestoredCandidate()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("discard owned candidate close count = %d, want 1", got)
	}
}
