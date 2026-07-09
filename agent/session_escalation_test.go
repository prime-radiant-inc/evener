package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
)

// deniedResult builds a tool.ExecResult carrying a typed file-tool sandbox denial,
// exactly as ExecuteCall would return one after a securepath refusal.
func deniedResult(path string) (tool.ExecResult, *sandbox.DeniedError) {
	d := &sandbox.DeniedError{
		Mode:   sandbox.ModeReadOnly,
		Tool:   "write_file",
		Path:   path,
		Reason: "outside the sandbox's writable roots",
	}
	return tool.ExecResult{
		ToolName:   "write_file",
		CallID:     "call_1",
		Output:     d.Error(),
		FullOutput: d.Error(),
		IsError:    true,
		Err:        d,
	}, d
}

// succeededResult is what a re-run under a grant returns.
func succeededResult() tool.ExecResult {
	return tool.ExecResult{ToolName: "write_file", CallID: "call_1", Output: "wrote /etc/hosts", FullOutput: "wrote /etc/hosts"}
}

// escalatableSession returns a root, interactive session with one live subscriber
// — the only configuration that escalates.
func escalatableSession(t *testing.T) *Session {
	t.Helper()
	s := newSession(t)
	s.SetSubscriberCountFunc(func() int { return 1 })
	return s
}

// pendingIDs snapshots the ids of currently-blocked escalations.
func pendingIDs(s *Session) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.pendingEscalations))
	for id := range s.pendingEscalations {
		ids = append(ids, id)
	}
	return ids
}

// awaitPending blocks until exactly want escalations are pending (or fails).
func awaitPending(t *testing.T, s *Session, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ids := pendingIDs(s); len(ids) == want {
			return ids
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted %d pending escalations, saw %d", want, len(pendingIDs(s)))
	return nil
}

// noRerun fails the test if the re-run path is ever taken.
func noRerun(t *testing.T) func(context.Context) tool.ExecResult {
	t.Helper()
	return func(context.Context) tool.ExecResult {
		t.Fatal("rerun must not be called for a non-escalating denial")
		return tool.ExecResult{}
	}
}

func TestEscalation_GateMatrix(t *testing.T) {
	res, _ := deniedResult("/etc/hosts")

	// Each case configures the session fully: only "with a live subscriber, root,
	// interactive, non-sensitive" escalates. The base leaves subscriberCountFn nil.
	cases := []struct {
		name   string
		mutate func(*Session)
	}{
		{"non_interactive", func(s *Session) {
			s.SetSubscriberCountFunc(func() int { return 1 })
			s.cfg.NonInteractive = true
		}},
		{"subagent", func(s *Session) {
			s.SetSubscriberCountFunc(func() int { return 1 })
			s.restoredMetaIsSubagent = true
		}},
		{"zero_subscribers", func(s *Session) { s.SetSubscriberCountFunc(func() int { return 0 }) }},
		{"nil_subscriber_probe", func(s *Session) { /* leave subscriberCountFn nil */ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(t)
			tc.mutate(s)
			got := s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
			if !got.IsError || got.Err == nil {
				t.Fatalf("non-escalating denial must return the original typed error, got %+v", got)
			}
			if len(pendingIDs(s)) != 0 {
				t.Fatalf("no escalation should be pending, saw %v", pendingIDs(s))
			}
		})
	}
}

func TestEscalation_SensitiveDenialNeverEscalates(t *testing.T) {
	s := escalatableSession(t)
	d := &sandbox.DeniedError{Mode: sandbox.ModeReadOnly, Tool: "read_file", Path: "/home/u/.ssh/id_rsa", Reason: "credential path masked", Sensitive: true}
	res := tool.ExecResult{ToolName: "read_file", CallID: "c", IsError: true, Err: d, FullOutput: d.Error()}
	got := s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
	if !errors.Is(got.Err, d) {
		t.Fatal("a sensitive (masked-secret) denial must stay final, never escalate")
	}
}

func TestEscalation_OnlySingleFileToolsEscalate(t *testing.T) {
	// Only the single-file tools escalate; multi-file (apply_patch) and the browse
	// tools (which walk a directory subtree) stay final, so one grant can never
	// widen more than one leaf. Their underlying denials carry Tool=="write_file"
	// or a read tool, but the allowlist keys on the invoking call name.
	d := &sandbox.DeniedError{Mode: sandbox.ModeReadOnly, Tool: "write_file", Path: "/wt/f", Reason: "writes are denied in this sandbox mode"}
	res := tool.ExecResult{ToolName: "x", CallID: "c", IsError: true, Err: d, FullOutput: d.Error()}
	for _, callName := range []string{"apply_patch", "glob", "grep", "list_dir", "shell", "read_file_all"} {
		s := escalatableSession(t)
		got := s.escalateOnSandboxDenial(context.Background(), callName, res, noRerun(t))
		if !errors.Is(got.Err, d) {
			t.Fatalf("%s denial must stay final (not a single-file tool)", callName)
		}
		if len(pendingIDs(s)) != 0 {
			t.Fatalf("%s must not register a pending escalation", callName)
		}
	}
}

func TestEscalation_NonSandboxErrorUntouched(t *testing.T) {
	s := escalatableSession(t)
	res := tool.ExecResult{ToolName: "shell", CallID: "c", IsError: true, FullOutput: "boom", Err: context.DeadlineExceeded}
	got := s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
	if got.FullOutput != "boom" {
		t.Fatal("a non-sandbox error must pass through untouched")
	}
}

func TestEscalation_ApproveThreadsInvocationGrant(t *testing.T) {
	s := escalatableSession(t)
	res, denied := deniedResult("/etc/hosts")

	var gotGrant string
	var grantOK bool
	rerun := func(ctx context.Context) tool.ExecResult {
		gotGrant, grantOK = invocationGrant(ctx)
		return succeededResult()
	}

	done := make(chan tool.ExecResult, 1)
	go func() { done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, rerun) }()

	ids := awaitPending(t, s, 1)
	if err := s.ResolveSandboxEscalation(ids[0], true); err != nil {
		t.Fatalf("resolve approve: %v", err)
	}
	got := <-done

	if !grantOK || gotGrant != denied.Path {
		t.Fatalf("re-run ctx must carry the granted path %q, got %q (ok=%v)", denied.Path, gotGrant, grantOK)
	}
	if got.IsError || got.FullOutput != "wrote /etc/hosts" {
		t.Fatalf("approve must return the successful re-run output, got %+v", got)
	}
	// The grant must not have leaked onto the session or its policy.
	if len(pendingIDs(s)) != 0 {
		t.Fatalf("escalation should be removed after resolve, saw %v", pendingIDs(s))
	}
}

func TestEscalation_DenyReturnsTypedError(t *testing.T) {
	s := escalatableSession(t)
	res, denied := deniedResult("/etc/hosts")

	done := make(chan tool.ExecResult, 1)
	go func() { done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t)) }()

	ids := awaitPending(t, s, 1)
	if err := s.ResolveSandboxEscalation(ids[0], false); err != nil {
		t.Fatalf("resolve deny: %v", err)
	}
	got := <-done
	if !errors.Is(got.Err, denied) || !got.IsError || got.FullOutput != res.FullOutput {
		t.Fatalf("deny must return the original typed denial byte-for-byte, got %+v", got)
	}
}

func TestEscalation_ContextCancelDenies(t *testing.T) {
	s := escalatableSession(t)
	res, _ := deniedResult("/etc/hosts")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan tool.ExecResult, 1)
	go func() { done <- s.escalateOnSandboxDenial(ctx, "write_file", res, noRerun(t)) }()

	awaitPending(t, s, 1)
	cancel() // turn interrupt
	got := <-done
	if !got.IsError {
		t.Fatal("a context cancel (turn interrupt) must resolve to the typed denial")
	}
	if len(pendingIDs(s)) != 0 {
		t.Fatalf("cancel must clear the pending escalation, saw %v", pendingIDs(s))
	}
}

func TestEscalation_CloseCancels(t *testing.T) {
	s := escalatableSession(t)
	res, _ := deniedResult("/etc/hosts")

	done := make(chan tool.ExecResult, 1)
	go func() { done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t)) }()

	awaitPending(t, s, 1)
	s.Close()
	select {
	case got := <-done:
		if !got.IsError {
			t.Fatal("Close must resolve a blocked escalation to the typed denial")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the escalation (goroutine leak)")
	}
}

func TestEscalation_UnknownResolveIsError(t *testing.T) {
	s := escalatableSession(t)
	if err := s.ResolveSandboxEscalation("no_such_id", true); err == nil {
		t.Fatal("resolving an unknown id must return an error, not panic or block")
	}
}

func TestEscalation_DoubleResolveSecondIsError(t *testing.T) {
	s := escalatableSession(t)
	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
	}()
	ids := awaitPending(t, s, 1)
	if err := s.ResolveSandboxEscalation(ids[0], true); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	<-done
	if err := s.ResolveSandboxEscalation(ids[0], true); err == nil {
		t.Fatal("a second resolve of the same id must error (no double-approve)")
	}
}

func TestEscalation_ConcurrentDenialsDistinctWaiters(t *testing.T) {
	s := escalatableSession(t)
	resA, _ := deniedResult("/etc/hosts")
	resB, _ := deniedResult("/etc/passwd")

	outA := make(chan tool.ExecResult, 1)
	outB := make(chan tool.ExecResult, 1)
	go func() {
		outA <- s.escalateOnSandboxDenial(context.Background(), "write_file", resA, func(context.Context) tool.ExecResult {
			return tool.ExecResult{FullOutput: "approved"}
		})
	}()
	go func() {
		outB <- s.escalateOnSandboxDenial(context.Background(), "write_file", resB, func(context.Context) tool.ExecResult {
			return tool.ExecResult{FullOutput: "approved"}
		})
	}()

	ids := awaitPending(t, s, 2)
	// Approve one, deny the other. Map order is nondeterministic, so both reruns
	// return "approved"; we assert exactly one waiter approved and one denied —
	// proving each id routes to its own distinct waiter, never cross-wiring.
	if err := s.ResolveSandboxEscalation(ids[0], true); err != nil {
		t.Fatalf("resolve[0]: %v", err)
	}
	if err := s.ResolveSandboxEscalation(ids[1], false); err != nil {
		t.Fatalf("resolve[1]: %v", err)
	}
	approved := 0
	denied := 0
	for _, r := range []tool.ExecResult{<-outA, <-outB} {
		if r.FullOutput == "approved" && !r.IsError {
			approved++
		} else if r.IsError {
			denied++
		}
	}
	if approved != 1 || denied != 1 {
		t.Fatalf("two distinct waiters must each get their own decision; approved=%d denied=%d", approved, denied)
	}
}

func TestEscalation_EmitsRedactedRequestedEvent(t *testing.T) {
	s := escalatableSession(t)
	var mu sync.Mutex
	var evs []events.SessionEvent
	go func() {
		for ev := range s.Events() {
			mu.Lock()
			evs = append(evs, ev)
			mu.Unlock()
		}
	}()

	// A non-sensitive denial carries the full literal path for informed consent.
	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
	}()
	ids := awaitPending(t, s, 1)
	_ = s.ResolveSandboxEscalation(ids[0], true)
	<-done

	deadline := time.Now().Add(2 * time.Second)
	var found *events.SandboxEscalationRequestedData
	for time.Now().Before(deadline) && found == nil {
		mu.Lock()
		for i := range evs {
			if d, ok := evs[i].Data.(events.SandboxEscalationRequestedData); ok {
				found = &d
			}
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if found == nil {
		t.Fatal("escalateOnSandboxDenial must emit EventSandboxEscalationRequested before blocking")
	}
	if found.DeniedPath != "/etc/hosts" {
		t.Fatalf("event must carry the full path for informed consent, got %q", found.DeniedPath)
	}
	if found.Kind != string(sandbox.EscalationFileTool) {
		t.Fatalf("a file-tool denial must project the file_tool kind, got %q", found.Kind)
	}
}

func TestEscalation_NeverAppendsHistory(t *testing.T) {
	s := escalatableSession(t)
	before := len(s.history)
	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
	}()
	ids := awaitPending(t, s, 1)
	_ = s.ResolveSandboxEscalation(ids[0], true)
	<-done
	if len(s.history) != before {
		t.Fatalf("escalation must never append a turn to history: before=%d after=%d", before, len(s.history))
	}
}
