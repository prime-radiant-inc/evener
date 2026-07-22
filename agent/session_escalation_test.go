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
		Mode:       sandbox.ModeReadOnly,
		Tool:       "write_file",
		Path:       path,
		Reason:     "outside the sandbox's writable roots",
		ReasonKind: sandbox.DenialOutsideWriteRoots, // a CURABLE containment denial
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
	d := &sandbox.DeniedError{Mode: sandbox.ModeReadOnly, Tool: "read_file", Path: "/home/u/.ssh/id_rsa", Reason: "credential path masked", Sensitive: true, ReasonKind: sandbox.DenialMasked}
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
	d := &sandbox.DeniedError{Mode: sandbox.ModeReadOnly, Tool: "write_file", Path: "/wt/f", Reason: "writes are denied in this sandbox mode", ReasonKind: sandbox.DenialWritesDisabled}
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

func TestEscalation_OnlyCurableContainmentDenialsEscalate(t *testing.T) {
	// Uncurable reasons re-deny deterministically on re-run, so they must NOT raise
	// a futile approval card — they stay final like today. Includes the unspecified
	// zero value (fail-closed).
	final := []sandbox.DenialReason{
		sandbox.DenialGitProtected, sandbox.DenialSymlink, sandbox.DenialEscape,
		sandbox.DenialMasked, sandbox.DenialRootTarget, sandbox.DenialUnspecified,
	}
	for _, rk := range final {
		s := escalatableSession(t)
		d := &sandbox.DeniedError{Tool: "write_file", Path: "/wt/f", ReasonKind: rk}
		res := tool.ExecResult{ToolName: "write_file", CallID: "c", IsError: true, Err: d, FullOutput: d.Error()}
		got := s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
		if !errors.Is(got.Err, d) {
			t.Fatalf("reason %v is uncurable and must stay final", rk)
		}
		if len(pendingIDs(s)) != 0 {
			t.Fatalf("reason %v must not raise an escalation", rk)
		}
	}

	// Curable containment reasons DO escalate (for a single-file tool).
	curable := []sandbox.DenialReason{
		sandbox.DenialOutsideReadRoots, sandbox.DenialOutsideWriteRoots, sandbox.DenialWritesDisabled,
	}
	for _, rk := range curable {
		s := escalatableSession(t)
		d := &sandbox.DeniedError{Tool: "read_file", Path: "/etc/x", ReasonKind: rk}
		res := tool.ExecResult{ToolName: "read_file", CallID: "c", IsError: true, Err: d, FullOutput: d.Error()}
		done := make(chan tool.ExecResult, 1)
		go func() {
			done <- s.escalateOnSandboxDenial(context.Background(), "read_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
		}()
		ids := awaitPending(t, s, 1)
		if err := s.ResolveSandboxEscalation(ids[0], false); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		<-done
	}
}

func TestEscalation_HasAndListPending(t *testing.T) {
	s := escalatableSession(t)
	if s.HasPendingEscalations() {
		t.Fatal("no escalation should be pending initially")
	}
	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "read_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
	}()
	ids := awaitPending(t, s, 1)
	if !s.HasPendingEscalations() {
		t.Fatal("HasPendingEscalations must be true while blocked (drives the cross-session attention flag)")
	}
	list := s.PendingEscalations()
	if len(list) != 1 || list[0].EscalationID != ids[0] || list[0].DeniedPath != "/etc/hosts" {
		t.Fatalf("PendingEscalations must expose the redacted card payload for the snapshot: %+v", list)
	}
	if err := s.ResolveSandboxEscalation(ids[0], false); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	<-done
	if s.HasPendingEscalations() || len(s.PendingEscalations()) != 0 {
		t.Fatal("pending escalation state must clear after resolve")
	}
}

func TestEscalation_SnapshotIsInStableRaiseOrder(t *testing.T) {
	s := escalatableSession(t)
	// Raise three escalations in a known order; the snapshot must come back in that
	// raise order every time (not Go's random map order), so a fresh-entry client
	// answers them in the same FIFO as a client that saw them live.
	paths := []string{"/a/first", "/b/second", "/c/third"}
	dones := make([]chan tool.ExecResult, len(paths))
	for i, p := range paths {
		res, _ := deniedResult(p)
		done := make(chan tool.ExecResult, 1)
		dones[i] = done
		go func() {
			done <- s.escalateOnSandboxDenial(context.Background(), "read_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
		}()
		awaitPending(t, s, i+1) // ensure this one registered before raising the next
	}

	for attempt := 0; attempt < 5; attempt++ {
		snap := s.PendingEscalations()
		if len(snap) != len(paths) {
			t.Fatalf("expected %d pending, got %d", len(paths), len(snap))
		}
		for i, p := range paths {
			if snap[i].DeniedPath != p {
				t.Fatalf("snapshot must be in raise order; index %d = %q, want %q (%+v)", i, snap[i].DeniedPath, p, snap)
			}
		}
	}

	for _, id := range pendingIDs(s) {
		_ = s.ResolveSandboxEscalation(id, false)
	}
	for _, d := range dones {
		<-d
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

// startEventDrain starts a goroutine appending every event on s.Events() to
// *evs (guarded by mu), for tests that need to inspect the raw event stream.
// The returned channel closes when the drain loop ends — i.e. after Close()
// has closed the event stream AND every delivered event has been appended —
// so a test that has closed the session can await it and then read *evs as
// final, with no sleep or deadline.
func startEventDrain(s *Session, mu *sync.Mutex, evs *[]events.SessionEvent) chan struct{} {
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range s.Events() {
			mu.Lock()
			*evs = append(*evs, ev)
			mu.Unlock()
		}
	}()
	return drained
}

// awaitExactlyOneResolvedEvent polls evs (guarded by mu) until exactly one
// EventSandboxEscalationResolved for id is observed. It fails immediately on a
// SECOND resolved event for the same id (over-emission), and fails after a 2s
// deadline if none ever arrives.
func awaitExactlyOneResolvedEvent(t *testing.T, mu *sync.Mutex, evs *[]events.SessionEvent, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := 0
		for _, ev := range *evs {
			if d, ok := ev.Data.(events.SandboxEscalationResolvedData); ok && d.EscalationID == id {
				n++
			}
		}
		mu.Unlock()
		switch {
		case n == 1:
			return
		case n > 1:
			t.Fatalf("escalation %q emitted %d resolved events, want exactly 1", id, n)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("escalation %q never emitted exactly one resolved event within the deadline", id)
}

// TestEscalation_EmitsResolvedEventOnExplicitResolve (wire-honesty spec Part
// B, clearing path 1 of 3: explicit resolve) proves escalateOnSandboxDenial's
// convergence-point exit emits EventSandboxEscalationResolved exactly once
// when ResolveSandboxEscalation delivers the human's decision.
func TestEscalation_EmitsResolvedEventOnExplicitResolve(t *testing.T) {
	s := escalatableSession(t)
	var mu sync.Mutex
	var evs []events.SessionEvent
	startEventDrain(s, &mu, &evs)

	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
	}()
	ids := awaitPending(t, s, 1)
	if err := s.ResolveSandboxEscalation(ids[0], false); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	<-done
	awaitExactlyOneResolvedEvent(t, &mu, &evs, ids[0])
}

// TestEscalation_EmitsResolvedEventOnTurnInterrupt (wire-honesty spec Part B,
// clearing path 2 of 3: turn-interrupt) proves the same convergence-point exit
// emits EventSandboxEscalationResolved exactly once when the tool-exec ctx is
// cancelled (the select's ctx.Done() arm), independent of any explicit
// resolve.
func TestEscalation_EmitsResolvedEventOnTurnInterrupt(t *testing.T) {
	s := escalatableSession(t)
	var mu sync.Mutex
	var evs []events.SessionEvent
	startEventDrain(s, &mu, &evs)

	res, _ := deniedResult("/etc/hosts")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(ctx, "write_file", res, noRerun(t))
	}()
	ids := awaitPending(t, s, 1)
	cancel() // turn interrupt
	<-done
	awaitExactlyOneResolvedEvent(t, &mu, &evs, ids[0])
}

// TestEscalation_CloseEmitsAtMostOneResolvedEvent (wire-honesty spec Part B,
// clearing path 3 of 3: session close) pins what the close path actually
// guarantees. Unlike resolve/interrupt (paths 1-2 above, where the session
// outlives the emit and exactly-once delivery is deterministic), close races
// the convergence defer against its own teardown: cancelAllEscalations
// unblocks the waiter (session_lifecycle.go:180) and Close then proceeds to
// close the event stream, so the defer's emit lands only if it reaches
// sendEvent before eventsClosed flips — sendEvent drops post-close sends by
// design (best-effort, accepted at review). This test's escalation goroutine
// is caller-owned and unjoined by Close (the production tool-execution path
// is ordered by toolEventsWG; a bare test goroutine is not), so
// delivery here is genuinely unordered. The close-path contract is therefore:
// the escalation returns, the waiter is pruned, and the resolved event
// appears AT MOST once — never twice, regardless of which select arm won.
func TestEscalation_CloseEmitsAtMostOneResolvedEvent(t *testing.T) {
	s := escalatableSession(t)
	var mu sync.Mutex
	var evs []events.SessionEvent
	drained := startEventDrain(s, &mu, &evs)

	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t))
	}()
	ids := awaitPending(t, s, 1)
	s.Close()
	// done receives only after escalateOnSandboxDenial has fully returned —
	// deferred waiter-prune and emit attempt included — and drained closes
	// only after Close's close(s.events) ended the drain loop with every
	// delivered event appended. After both, evs is final: no sleep, no
	// deadline, and a dropped emit is indistinguishable from none ever sent,
	// which is exactly the contract under test.
	<-done
	<-drained
	awaitPending(t, s, 0)
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, ev := range evs {
		if d, ok := ev.Data.(events.SandboxEscalationResolvedData); ok && d.EscalationID == ids[0] {
			n++
		}
	}
	if n > 1 {
		t.Fatalf("escalation %q emitted %d resolved events on close, want at most 1", ids[0], n)
	}
}
