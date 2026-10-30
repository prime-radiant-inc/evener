package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
)

// Full-output retention is best effort: Session already degrades when a Put
// fails, appending retention_failed to the tool result. Creating the store is
// the same kind of convenience, and must not turn a resumable session into a
// restore failure — a vanished temp dir is the live example.
func TestRestoreSurvivesArtifactStoreCreationFailure(t *testing.T) {
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return nil, errors.New("temp dir is gone")
	})
	sess, err := RestoreSessionFromMetaWithConfig(
		newArtifactTestClient(),
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		artifactRestoreMeta(t),
		artifactRestoreConfig(t, t.TempDir()),
	)
	if err != nil {
		t.Fatalf("restore failed on artifact store creation: %v", err)
	}
	if sess.artifactStore != nil {
		t.Fatal("session kept an artifact store the factory did not return")
	}
	sess.Close()
}

// The notice must reach the client, and it must arrive after SESSION_START:
// a warning emitted during construction is lost for good. A fresh session
// proves the same degradation path the restore uses.
func TestArtifactStoreCreationFailureWarnsAfterSessionStart(t *testing.T) {
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return nil, errors.New("temp dir is gone")
	})
	sess := newArtifactTestRoot(t)
	if sess.artifactStore != nil {
		t.Fatal("session kept an artifact store the factory did not return")
	}
	evs := transcriptWarningOrderingDrainAll(t, sess)

	sessionStartIdx, warningIdx := -1, -1
	for i, ev := range evs {
		switch ev.Kind {
		case events.EventSessionStart:
			if sessionStartIdx == -1 {
				sessionStartIdx = i
			}
		case events.EventWarning:
			if w, ok := ev.Data.(events.WarningData); ok &&
				w.Code == "artifact_store_unavailable" && warningIdx == -1 {
				warningIdx = i
			}
		}
	}
	if sessionStartIdx == -1 {
		t.Fatal("no SESSION_START on the stream")
	}
	if warningIdx == -1 {
		t.Fatal("no artifact_store_unavailable warning on the stream")
	}
	if warningIdx <= sessionStartIdx {
		t.Fatalf("warning (index %d) arrived before SESSION_START (index %d)", warningIdx, sessionStartIdx)
	}
}

// A delegate must share its tree's store, including when the tree has none: a
// delegate that minted its own would hand back artifact refs its root cannot
// read, and its close would delete them while the root is still live.
func TestDelegateInheritsADegradedRootsMissingStore(t *testing.T) {
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return nil, errors.New("temp dir is gone")
	})
	root := newArtifactTestRoot(t)
	if root.artifactStore != nil {
		t.Fatal("root unexpectedly got a store")
	}
	// Storage recovers before the delegate starts; the delegate must still not
	// take ownership of a store of its own.
	installArtifactStoreFactory(t, func() (artifactStore, error) {
		return newFakeArtifactStore(), nil
	})

	childID := spawnRuntimeAgent(t, root, delegateArgs{Task: "child task"})
	child := root.getSub(childID)
	if child == nil || child.sess == nil {
		t.Fatal("production child was not tracked")
	}
	if child.sess.artifactStore != nil {
		t.Fatal("delegate minted its own store under a degraded root")
	}
	root.Close()
}
