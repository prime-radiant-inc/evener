package agent

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
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

func newTestSubagentSession(t *testing.T, parent *Session) *Session {
	t.Helper()
	cfg := parent.cfg
	cfg.artifactStore = parent.artifactStore
	cfg.spawn.parentSessionID = parent.id
	cfg.spawn.depth = parent.depth + 1
	cfg.spawn.delegationAllowance = 0
	child, err := NewSession(parent.client, parent.profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("new child session: %v", err)
	}
	t.Cleanup(child.Close)
	return child
}

func TestSessionArtifactStoreSharedByDescendantsOnly(t *testing.T) {
	rootA := newTestSession(t)
	rootB := newTestSession(t)
	child := newTestSubagentSession(t, rootA)
	if rootA.artifactStore != child.artifactStore {
		t.Fatal("child did not inherit store")
	}
	if rootA.artifactStore == rootB.artifactStore {
		t.Fatal("independent roots shared store")
	}
	if !rootA.ownsArtifactStore || child.ownsArtifactStore {
		t.Fatal("wrong ownership")
	}
}

func TestSessionArtifactStoreRootClosesAfterDescendants(t *testing.T) {
	root := newTestSession(t)
	oldStore := root.artifactStore
	if err := oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	store := &recordingArtifactStore{}
	root.artifactStore = store
	root.ownsArtifactStore = true
	child := newTestSubagentSession(t, root)
	store.onClose = func() {
		child.mu.Lock()
		defer child.mu.Unlock()
		if child.state != SessionClosed {
			t.Errorf("store closed before child shutdown: state=%s", child.state)
		}
	}

	child.Close()
	if got := store.closeCount.Load(); got != 0 {
		t.Fatalf("child close closed store %d times", got)
	}
	root.Close()
	if got := store.closeCount.Load(); got != 1 {
		t.Fatalf("root close count = %d, want 1", got)
	}
}

func TestRestoredDelegateArtifactStoreInheritance(t *testing.T) {
	root, rec, childID, preflight := w3dlg_restoreFixture(t)
	var restoreCfg RestoreSessionConfig
	previous := delegateRestoreSession
	delegateRestoreSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, cfg RestoreSessionConfig) (*Session, error) {
		restoreCfg = cfg
		return RestoreSessionFromMetaWithConfig(client, profile, env, meta, cfg)
	}
	t.Cleanup(func() { delegateRestoreSession = previous })

	child, err := root.restoreTerminalDelegateChildClaimed(rec, childID, preflight)
	if err != nil {
		t.Fatalf("restore delegate: %v", err)
	}
	if restoreCfg.artifactStore != root.artifactStore {
		t.Fatal("restored delegate config did not inherit root store")
	}
	if child == nil || child.sess == nil {
		t.Fatal("restored delegate session missing")
	}
	if child.sess.artifactStore != root.artifactStore {
		t.Fatal("restored delegate did not inherit root store")
	}
	if child.sess.ownsArtifactStore {
		t.Fatal("restored delegate owns inherited store")
	}
}
