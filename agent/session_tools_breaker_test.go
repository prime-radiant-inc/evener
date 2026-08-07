package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// A human approving a sandbox denial has just authorized this exact call, so
// the repeated-call breaker must not refuse the re-dispatch even though the
// signature has already failed twice.
func TestRerunToolWithGrant_HumanApprovedRetryIsNotParked(t *testing.T) {
	s := newSession(t, withDir(t.TempDir()), withoutGitSnapshot())
	calls := 0
	s.RegisterTool("denied_probe", "always fails identically", map[string]any{"type": "object"},
		func(context.Context, any) (any, error) {
			calls++
			return nil, errors.New("sandbox denied: /etc/hosts")
		})

	call := llm.ToolCallData{ID: "grant", Name: "denied_probe", Arguments: json.RawMessage(`{}`)}
	ctx := context.Background()
	for range 3 {
		s.reg.ExecuteCall(ctx, s.currentEnv(), call)
	}
	if calls != 2 {
		t.Fatalf("setup invocations = %d, want 2 (the third call parked)", calls)
	}

	res := s.rerunToolWithGrant(ctx, call)
	if calls != 3 {
		t.Errorf("an approved rerun must dispatch: invocations = %d, want 3", calls)
	}
	if strings.Contains(res.Output, "serf did not execute this call:") {
		t.Errorf("approved rerun was parked: %q", res.Output)
	}
}

// approveNextEscalation answers the next escalation this session raises the way
// the daemon's resolve handler does when a human clicks Allow, and gives up when
// the test ends so a never-raised card cannot log after completion.
func approveNextEscalation(t *testing.T, s *Session) {
	t.Helper()
	ctx := t.Context()
	go func() {
		for ctx.Err() == nil {
			pending := s.PendingEscalations()
			if len(pending) == 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			if err := s.ResolveSandboxEscalation(pending[0].EscalationID, true); err != nil {
				t.Errorf("ResolveSandboxEscalation: %v", err)
			}
			return
		}
	}()
}

// The sandbox grant is per-invocation, so an approved call comes back around and
// is denied again. Every denial the human answered must be retired as evidence:
// otherwise the third identical call parks before dispatch, its typed denial is
// gone, and escalateOnSandboxDenial can never raise the card that would let the
// human approve it a third time.
func TestRerunToolWithGrant_RepeatedApprovalsKeepTheCallDispatchable(t *testing.T) {
	s := newSession(t, withDir(t.TempDir()), withoutGitSnapshot())
	s.SetSubscriberCountFunc(func() int { return 1 })

	const secret = "TOP-SECRET-PAYLOAD"
	denied := &sandbox.DeniedError{
		Mode:       sandbox.ModeRestricted,
		Tool:       "read_file",
		Path:       "/outside/secret.txt",
		Reason:     "outside the readable roots",
		ReasonKind: sandbox.DenialOutsideReadRoots,
	}
	dispatches := 0
	s.RegisterTool("read_file", "denied unless this one invocation was granted", map[string]any{"type": "object"},
		func(ctx context.Context, _ any) (any, error) {
			dispatches++
			if _, granted := invocationGrant(ctx); granted {
				return secret, nil
			}
			return nil, denied
		})

	call := llm.ToolCallData{ID: "esc", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"/outside/secret.txt"}`)}
	ctx := t.Context()

	// Two full denial → human approval → granted re-run cycles, exactly as
	// execTool runs them.
	for round := 1; round <= 2; round++ {
		res := s.reg.ExecuteCall(ctx, s.currentEnv(), call)
		if _, ok := sandbox.AsDenied(res.Err); !ok {
			t.Fatalf("round %d: the dispatch must carry the typed denial, got %#v", round, res)
		}
		approveNextEscalation(t, s)
		res = s.escalateOnSandboxDenial(ctx, call.Name, res, toolCallRerunner{session: s, call: call}.run)
		if !strings.Contains(res.Output, secret) {
			t.Fatalf("round %d: the approved re-run must succeed, got %q", round, res.Output)
		}
	}
	if dispatches != 4 {
		t.Fatalf("two approved cycles = %d dispatches, want 4", dispatches)
	}

	third := s.reg.ExecuteCall(ctx, s.currentEnv(), call)
	if dispatches != 5 {
		t.Fatalf("the call after two approvals was parked: dispatches = %d, want 5", dispatches)
	}
	if _, ok := sandbox.AsDenied(third.Err); !ok {
		t.Fatalf("the call after two approvals must still carry a typed denial so a card can be raised, got %#v", third)
	}
}
