package agent

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

type recordingArtifactStore struct {
	closeCount atomic.Int32
	onClose    func()
}

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
		wg.Add(1)
		go func() {
			defer wg.Done()
			root.Close()
		}()
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

func TestSessionArtifactStoreInheritedRestoredConstructorFailurePreservesStore(t *testing.T) {
	root, rec, childID, preflight := w3dlg_restoreFixture(t)
	store := &recordingArtifactStore{}
	replaceRootArtifactStore(t, root, store)
	want := errors.New("restored child job manager fault")
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return want
		}
		return nil
	}

	if _, err := root.restoreTerminalDelegateChildClaimed(rec, childID, preflight); !errors.Is(err, want) {
		t.Fatalf("restored child constructor error = %v, want %v", err, want)
	}
	if got := store.closeCount.Load(); got != 0 {
		t.Fatalf("inherited store closed by failed restored constructor %d times", got)
	}
	root.Close()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("root close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreRestoredCandidateRejectionPreservesInheritedStore(t *testing.T) {
	root, rec, childID, preflight := w3dlg_restoreFixture(t)
	store := &recordingArtifactStore{}
	replaceRootArtifactStore(t, root, store)
	rec.DelegateRestore.FrozenToolNames = []string{"artifact-test-missing-tool"}

	if _, err := root.restoreTerminalDelegateChildClaimed(rec, childID, preflight); err == nil {
		t.Fatal("restored candidate rejection unexpectedly succeeded")
	}
	if got := store.closeCount.Load(); got != 0 {
		t.Fatalf("inherited store closed while rejecting candidate %d times", got)
	}
	root.Close()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("root close count = %d, want 1", got)
	}
}

func TestSessionArtifactStoreRestoredCandidateCollisionPreservesInheritedStore(t *testing.T) {
	root, rec, childID, preflight := w3dlg_restoreFixture(t)
	store := &recordingArtifactStore{}
	replaceRootArtifactStore(t, root, store)
	incumbent := &subagent{id: childID, sess: newTestSession(t), status: SubagentCompleted, done: make(chan struct{})}
	root.delegateRestoreBeforeTrack = func() { root.subagents.track(incumbent) }
	t.Cleanup(func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, childID)
		root.subagents.mu.Unlock()
	})

	got, err := root.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if err != nil || got != incumbent {
		t.Fatalf("collision restore = (%v, %v), want incumbent", got, err)
	}
	if got := store.closeCount.Load(); got != 0 {
		t.Fatalf("inherited store closed while discarding collision candidate %d times", got)
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
