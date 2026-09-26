//go:build unix

package sshconn

import (
	"context"
	"testing"

	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

type runResult struct {
	out []byte
	err error
}

// Every one-shot remote command's deadline is its ctx, and ssh can leave a
// process behind on its output: a ProxyCommand (nc, cloudflared, an SSM
// session) inherits ssh's stderr, and killing ssh when ctx ends does not kill
// it. Without a WaitDelay, Run then lasts as long as that process does, so a
// preflight against an unreachable host is bounded by nothing.
func TestExecRunnerRunDeadlineDoesNotWaitForAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		h.AwaitStarted()
		cancel()
	}()
	done := make(chan runResult, 1)
	go func() {
		out, err := execRunner{}.Run(ctx, []string{"sh", "-c", h.Spawn() + "\nwait"}, nil)
		done <- runResult{out, err}
	}()
	if got := orphanpipetest.Await(t, h, done); got.err == nil {
		t.Fatalf("Run after its context ended = nil error, output %q", got.out)
	}
}

// The other side of the bound: a command that answered and exited 0 while a
// process it left behind held its output answered all the same. With ssh that
// is anything the user's ssh config starts that outlives ssh itself, such as a
// LocalCommand that backgrounds a job.
func TestExecRunnerRunSuccessSurvivesAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	done := make(chan runResult, 1)
	go func() {
		out, err := execRunner{}.Run(context.Background(), []string{"sh", "-c", "printf answer\n" + h.Spawn()}, nil)
		done <- runResult{out, err}
	}()
	got := orphanpipetest.Await(t, h, done)
	if got.err != nil {
		t.Fatalf("a command that answered and exited 0 was reported as: %v", got.err)
	}
	if string(got.out) != "answer" {
		t.Fatalf("output = %q, want the command's answer", got.out)
	}
}
