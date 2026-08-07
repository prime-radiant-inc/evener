package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
