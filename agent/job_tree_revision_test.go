package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

func TestJobTreeRevisionSharedAcrossSpawnAndRestore(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root := newSession(t,
		withClient(client),
		withDir(stateDir),
		withConfig(SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 3,
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)

	childPrepared := prepareSubagentForTreeRevisionTest(t, root, "child task")
	defer releasePreparedTreeSlot(childPrepared)
	defer childPrepared.runCancel()
	child := childPrepared.sub.sess
	defer child.Close()
	if child.jobTreeClock != root.jobTreeClock {
		t.Fatal("child did not inherit root tree clock")
	}

	grandPrepared := prepareSubagentForTreeRevisionTest(t, child, "grandchild task")
	defer releasePreparedTreeSlot(grandPrepared)
	defer grandPrepared.runCancel()
	grandchild := grandPrepared.sub.sess
	defer grandchild.Close()
	if grandchild.jobTreeClock != root.jobTreeClock {
		t.Fatal("grandchild did not inherit root tree clock")
	}

	rootStarted := createShellAndReadJobStarted(t, root, "printf root")
	if rootStarted.RootSessionID != root.ID() || rootStarted.TreeRevision != 1 {
		t.Fatalf("root started=%+v, want root_session_id=%q revision=1", rootStarted, root.ID())
	}

	childStarted := createShellAndReadJobStarted(t, child, "printf child")
	if childStarted.RootSessionID != root.ID() || childStarted.TreeRevision != 2 {
		t.Fatalf("child started=%+v, want root_session_id=%q revision=2", childStarted, root.ID())
	}

	grandStarted := createShellAndReadJobStarted(t, grandchild, "printf grandchild")
	if grandStarted.RootSessionID != root.ID() || grandStarted.TreeRevision != 3 {
		t.Fatalf("grandchild started=%+v, want root_session_id=%q revision=3", grandStarted, root.ID())
	}

	childMeta := child.Meta()
	child.Close()
	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		childMeta,
		RestoreSessionConfig{StateDir: stateDir, spawn: child.cfg.spawn},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()
	if restored.jobTreeClock != root.jobTreeClock {
		t.Fatal("restored child did not inherit root tree clock")
	}

	restoredStarted := createShellAndReadJobStarted(t, restored, "printf restored")
	if restoredStarted.RootSessionID != root.ID() || restoredStarted.TreeRevision != 4 {
		t.Fatalf("restored child started=%+v, want root_session_id=%q revision=4", restoredStarted, root.ID())
	}
}

func prepareSubagentForTreeRevisionTest(t *testing.T, parent *Session, task string) *preparedSubagentRun {
	t.Helper()
	ctx := context.Background()
	parent.mu.Lock()
	allowance := parent.delegationAllowance
	parent.mu.Unlock()
	if allowance > 0 {
		ctx = context.WithValue(ctx, ctxDelegationAllowance, allowance-1)
	}
	prepared, err := parent.prepareSubagentRun(ctx, task, "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun(%q): %v", task, err)
	}
	return prepared
}

func createShellAndReadJobStarted(t *testing.T, sess *Session, command string) events.JobStartedData {
	t.Helper()
	rec, err := sess.jobManager.createShell(createShellOpts{Command: command, Description: command})
	if err != nil {
		t.Fatalf("createShell(%q): %v", command, err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, sess.jobManager, rec.JobID) })

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind != events.EventJobStarted {
				continue
			}
			data, ok := ev.Data.(events.JobStartedData)
			if !ok {
				t.Fatalf("job started payload=%T", ev.Data)
			}
			return data
		case <-deadline:
			t.Fatalf("timed out waiting for job start event from %s", sess.ID())
		}
	}
}
