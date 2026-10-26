package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
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
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)

	childPrepared := prepareStableChild(t, root, delegateArgs{Task: "child task"})
	defer childPrepared.runCancel()
	child := childPrepared.sub.sess
	defer child.Close()
	if child.jobActivityClock != root.jobActivityClock {
		t.Fatal("child did not inherit root tree clock")
	}

	grandPrepared := prepareStableChild(t, child, delegateArgs{Task: "grandchild task"})
	defer grandPrepared.runCancel()
	grandchild := grandPrepared.sub.sess
	defer grandchild.Close()
	if grandchild.jobActivityClock != root.jobActivityClock {
		t.Fatal("grandchild did not inherit root tree clock")
	}

	rootStarted := createShellAndReadJobStarted(t, root, "printf root")
	// Creating the delegates advanced the tree's revision already; every job
	// after this one must advance the same counter by exactly one.
	baseRevision := rootStarted.TreeRevision
	if rootStarted.RootSessionID != root.ID() || baseRevision < 1 {
		t.Fatalf("root started=%+v, want root_session_id=%q and a positive revision", rootStarted, root.ID())
	}

	childStarted := createShellAndReadJobStarted(t, child, "printf child")
	if childStarted.RootSessionID != root.ID() || childStarted.TreeRevision != baseRevision+1 {
		t.Fatalf("child started=%+v, want root_session_id=%q revision=%d", childStarted, root.ID(), baseRevision+1)
	}

	grandStarted := createShellAndReadJobStarted(t, grandchild, "printf grandchild")
	if grandStarted.RootSessionID != root.ID() || grandStarted.TreeRevision != baseRevision+2 {
		t.Fatalf("grandchild started=%+v, want root_session_id=%q revision=%d", grandStarted, root.ID(), baseRevision+2)
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
	if restored.jobActivityClock != root.jobActivityClock {
		t.Fatal("restored child did not inherit root tree clock")
	}

	restoredStarted := createShellAndReadJobStarted(t, restored, "printf restored")
	if restoredStarted.RootSessionID != root.ID() || restoredStarted.TreeRevision != baseRevision+3 {
		t.Fatalf("restored child started=%+v, want root_session_id=%q revision=%d", restoredStarted, root.ID(), baseRevision+3)
	}
}

func TestJobTreeRevisionBareChildRestoreStartsIndependentRootTree(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root := newSession(t,
		withClient(client),
		withDir(stateDir),
		withConfig(SessionConfig{
			StateDir:         stateDir,
			MaxSubagentDepth: 2,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)

	prepared := prepareStableChild(t, root, delegateArgs{Task: "child task"})
	defer prepared.runCancel()
	child := prepared.sub.sess
	childMeta := child.Meta()
	child.Close()

	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		childMeta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	started := createShellAndReadJobStarted(t, restored, "printf standalone")
	if started.RootSessionID != child.ID() {
		t.Fatalf("bare restored child root_session_id=%q, want independent root %q", started.RootSessionID, child.ID())
	}
}

func createShellAndReadJobStarted(t *testing.T, sess *Session, command string) events.JobStartedData {
	t.Helper()
	rec, err := sess.jobManager.createShell(createShellOpts{Command: command, Description: command})
	if err != nil {
		t.Fatalf("createShell(%q): %v", command, err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, sess.jobManager, rec.JobID) })

	// TRIPWIRE: createShell already dispatched synchronously above, so the
	// started event is queued or arrives within microseconds; this only
	// bounds a genuine hang.
	deadline := time.After(30 * time.Second)
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
