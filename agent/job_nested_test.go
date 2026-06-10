package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func TestChildJobManagerHasForwardSeam(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("nested delegate complete")
			},
		},
	})
	parent := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "run nested shell work",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("delegate job_id is empty")
	}

	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := parent.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("tracked subagent %q not found", childID)
	}

	childJM := sub.sess.jobManager
	if childJM == nil {
		t.Fatal("child jobManager is nil")
	}
	assertJobForwardHookType(childJM.forward)
	if childJM.forward == nil {
		t.Fatal("child jobManager forward hook is nil")
	}
	if childJM.parentJobID != res.JobID {
		t.Fatalf("child parentJobID = %q, want delegate job_id %q", childJM.parentJobID, res.JobID)
	}
	if parent.jobManager.forward != nil {
		t.Fatal("root jobManager forward hook is non-nil")
	}
	if parent.jobManager.parentJobID != "" {
		t.Fatalf("root parentJobID = %q, want empty", parent.jobManager.parentJobID)
	}

	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai"})
	directParent := newDelegateTestSession(t, c2)
	spawned, err := directParent.spawnAgent(context.Background(), "run direct subagent work", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	directChildID, err := parseSpawnedAgentID(spawned)
	if err != nil {
		t.Fatalf("parse direct child id: %v", err)
	}
	directSub := directParent.subagents.get(directChildID)
	if directSub == nil || directSub.sess == nil {
		t.Fatalf("tracked direct subagent %q not found", directChildID)
	}
	directJM := directSub.sess.jobManager
	if directJM == nil {
		t.Fatal("direct child jobManager is nil")
	}
	if directJM.forward != nil {
		t.Fatal("direct child jobManager forward hook is non-nil")
	}
	if directJM.parentJobID != "" {
		t.Fatalf("direct child parentJobID = %q, want empty", directJM.parentJobID)
	}

	_, _ = parent.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, parent.jobManager, res.JobID)
}

func TestDelegateChildDirectSpawnDoesNotInheritForwardSeam(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("delegate complete")
			},
		},
	})
	parent := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "run delegate child",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := parent.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("tracked subagent %q not found", childID)
	}
	if sub.sess.jobManager == nil {
		t.Fatal("delegate child jobManager is nil")
	}
	if sub.sess.jobManager.forward == nil || sub.sess.jobManager.parentJobID != res.JobID {
		t.Fatalf("delegate child seam = forward:%v parentJobID:%q, want delegate seam for %q",
			sub.sess.jobManager.forward != nil,
			sub.sess.jobManager.parentJobID,
			res.JobID)
	}

	// Current production behavior rejects subagent management below the root.
	// This fixture opens only that guard so the test can exercise config-copy
	// inheritance through a delegate child.
	sub.sess.mu.Lock()
	sub.sess.depth = 0
	sub.sess.cfg.spawn.depth = 0
	sub.sess.cfg.MaxSubagentDepth = 1
	sub.sess.mu.Unlock()

	spawned, err := sub.sess.spawnAgent(context.Background(), "run direct grandchild work", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent from delegate child: %v", err)
	}
	grandchildID, err := parseSpawnedAgentID(spawned)
	if err != nil {
		t.Fatalf("parse grandchild id: %v", err)
	}
	grandchild := sub.sess.subagents.get(grandchildID)
	if grandchild == nil || grandchild.sess == nil {
		t.Fatalf("tracked grandchild %q not found", grandchildID)
	}
	grandchildJM := grandchild.sess.jobManager
	if grandchildJM == nil {
		t.Fatal("grandchild jobManager is nil")
	}
	if grandchildJM.forward != nil {
		t.Fatal("grandchild jobManager inherited delegate forward hook")
	}
	if grandchildJM.parentJobID != "" {
		t.Fatalf("grandchild parentJobID = %q, want empty", grandchildJM.parentJobID)
	}

	_, _ = parent.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, parent.jobManager, res.JobID)
}

func assertJobForwardHookType(_ func(jobstore.Event)) {}
