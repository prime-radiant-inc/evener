package mcp

import (
	"context"
	"errors"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/sandbox"
)

func TestStdioServerDialWaitsForAnUnconfirmedSandbox(t *testing.T) {
	t.Parallel()
	outcomes := []sandbox.CheckOutcome{sandbox.CheckInconclusive, sandbox.CheckPassed}
	recheck := sandbox.NewBackendRecheck(func() sandbox.CheckOutcome {
		outcome := outcomes[0]
		outcomes = outcomes[1:]
		return outcome
	})
	home := t.TempDir()
	host := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck, InconclusiveChecks: []string{"bwrap user namespaces"}}
	_, w := resolveWrapper(t, host, t.TempDir(), t.TempDir(), true)
	dial := productionDialWithEnv(stdioCfg(), w, func() []string { return []string{"HOME=" + home} })

	tr, err := dial(context.Background())
	var refusal *sandbox.RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != sandbox.RefusalUnconfirmed || tr != nil {
		t.Fatalf("an unconfirmed sandbox must refuse the server's spawn: transport=%v err=%v", tr, err)
	}

	tr, err = dial(context.Background())
	if err != nil {
		t.Fatalf("the redial after the check passed must succeed: %v", err)
	}
	ct, ok := tr.(*mcpsdk.CommandTransport)
	if !ok || ct.Command == nil || ct.Command.Path != host.BwrapPath {
		t.Fatalf("the confirmed server must spawn confined under bwrap, got %T", tr)
	}
}
