package mcp

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/sandbox"
)

// unconfirmedStdioDial is a stdio server dial under a sandbox whose host check
// answers from outcomes, one per recheck, the last repeating.
func unconfirmedStdioDial(t *testing.T, outcomes ...sandbox.CheckOutcome) (func(context.Context) (mcpsdk.Transport, error), string) {
	t.Helper()
	recheck := sandbox.NewBackendRecheck(func() sandbox.CheckOutcome {
		outcome := outcomes[0]
		if len(outcomes) > 1 {
			outcomes = outcomes[1:]
		}
		return outcome
	})
	home := t.TempDir()
	host := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck}
	_, w := resolveWrapper(t, host, t.TempDir(), t.TempDir(), true)
	return productionDialWithEnv(stdioCfg(), w, func() []string { return []string{"HOME=" + home} }), host.BwrapPath
}

// TestStdioServerStartWaitsForTheSandboxCheck: a server starts once, at
// session start, so its start keeps rechecking an unanswered sandbox check and
// starts confined once the check passes.
func TestStdioServerStartWaitsForTheSandboxCheck(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		dial, bwrapPath := unconfirmedStdioDial(t, sandbox.CheckInconclusive, sandbox.CheckPassed)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tr, err := dial(ctx)
		if err != nil {
			t.Fatalf("the start must succeed once the check passes within its time bound: %v", err)
		}
		ct, ok := tr.(*mcpsdk.CommandTransport)
		if !ok || ct.Command == nil || ct.Command.Path != bwrapPath {
			t.Fatalf("the confirmed server must spawn confined under bwrap, got %T", tr)
		}
	})
}

// TestStdioServerStartRefusesWhenTheCheckNeverAnswers: a check still without
// an answer when the start's time bound ends refuses the start as retryable,
// and nothing is spawned.
func TestStdioServerStartRefusesWhenTheCheckNeverAnswers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		dial, _ := unconfirmedStdioDial(t, sandbox.CheckInconclusive)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tr, err := dial(ctx)
		var refusal *sandbox.RefusalError
		if !errors.As(err, &refusal) || refusal.Kind != sandbox.RefusalUnconfirmed || tr != nil {
			t.Fatalf("an unanswered check must refuse the start: transport=%v err=%v", tr, err)
		}
	})
}
