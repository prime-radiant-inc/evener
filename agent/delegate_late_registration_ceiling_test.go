package agent

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/internal/mcp"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/llm"
)

// TestLateRegistrationJudgesAgainstTheEffectiveCeiling pins that a tool an MCP
// retry recovers after startup survives the session's tool policy.
//
// A cold-resumed delegate may re-admit a name the frozen ceiling excluded only
// for reachability. A fallback server whose first connection fails at startup
// connects during a later turn; the retry registers its tools and then applies
// the session's ceiling. Judging against the bare frozen list deletes the tool
// the retry just recovered, and no later retry restores it because the server is
// connected now — so the name is lost for the rest of the run.
//
// This drives retryFailedMCP, which is where the decision is made. Asserting on
// the ceiling helper directly would pass even if the retry went back to the
// frozen list, because it would never reach the code under test.
func TestLateRegistrationJudgesAgainstTheEffectiveCeiling(t *testing.T) {
	t.Parallel()

	const (
		serverName = "svc"
		recovered  = "svc__echo" // reachability-excluded at creation, registered by the retry
	)

	dial, calls := firstConnectFailsDial(t, serverName, "echo")
	mgr, outcomes := mcp.NewManager(context.Background(),
		[]mcpconfig.ServerConfig{{Name: serverName, Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){dial})
	if len(outcomes) != 1 {
		t.Fatalf("NewManager outcomes = %+v, want one failed first connect", outcomes)
	}
	t.Cleanup(mgr.Close)

	// A real session, not a hand-built struct: retryFailedMCP ends by rebuilding the
	// definition and prompt caches, which read the environment.
	s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{}))
	s.mcpMgr = mgr
	s.cfg.spawn.toolNameCeiling = []string{"read_file", "communicate"}
	s.cfg.spawn.ceilingReachabilityExcluded = []string{recovered}
	if err := s.reg.Register(regTool(llm.ToolDefinition{Name: "read_file", Description: "fixture"})); err != nil {
		t.Fatalf("register read_file: %v", err)
	}

	// The precondition the finding turns on: the server failed its first connect,
	// so the tool is not registered and the frozen ceiling does not admit it.
	if s.reg.Get(recovered) != nil {
		t.Fatalf("%q registered before the retry, so this fixture never exercises a late registration", recovered)
	}
	if !s.hubToolDenied(recovered) {
		t.Fatalf("fixture is wrong: the frozen ceiling does not exclude %q", recovered)
	}

	s.retryFailedMCP(context.Background())
	if got := calls.Load(); got < 2 {
		t.Fatalf("the retry never dialed: %d attempts, want the startup attempt plus one retry", got)
	}

	// The recovered tool is CALLABLE: the retry judged it against the effective
	// ceiling, which admits a reachability-excluded name the registry now holds.
	if s.reg.Get(recovered) == nil {
		t.Errorf("the retry deleted %q: a late registration was judged against the bare frozen ceiling", recovered)
	}
	// And it is REPORTED. A tool the child can call that availability omits is the
	// same defect pointing the other way, and a test that stopped at the registry
	// would not catch it.
	if observed := s.callableToolOrder(); !slices.Contains(observed, recovered) {
		t.Errorf("availability omitted the recovered %q: %v", recovered, observed)
	}

	// The other direction: the role's authorization is unchanged. A name outside
	// the frozen ceiling stays denied even when the registry holds it, so the
	// effective ceiling RE-ADMITS recorded exclusions rather than widening every
	// name.
	s.mcpMgr = nil
	roleDenied := "job_status"
	if err := s.reg.Register(regTool(llm.ToolDefinition{Name: roleDenied, Description: "fixture"})); err != nil {
		t.Fatalf("register %q: %v", roleDenied, err)
	}
	effective := effectiveChildToolCeiling(s.cfg.spawn.toolNameCeiling, s.cfg.spawn.ceilingReachabilityExcluded, s.reg)
	if !s.hubToolDeniedAgainst(effective, roleDenied) {
		t.Errorf("%q is outside the frozen ceiling and must stay denied", roleDenied)
	}
	// A name in neither list is never admitted, whether or not the registry holds
	// it: re-admission covers the recorded exclusions and nothing else.
	if !s.hubToolDeniedAgainst(effective, "svc__other") {
		t.Errorf("a name outside both lists was admitted by the effective ceiling: %v", effective)
	}
}

// firstConnectFailsDial returns a dial factory whose first attempt fails and whose
// second hands back a live in-process server exposing one tool. RetryFailed dials
// from its own goroutines, so the counter is atomic.
func firstConnectFailsDial(t *testing.T, serverName, toolName string) (func(context.Context) (mcpsdk.Transport, error), *atomic.Int32) {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: serverName, Version: "v0.0.1"}, nil)
	srv.AddTool(&mcpsdk.Tool{
		Name:        toolName,
		Description: "fixture",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("connect fixture server: %v", err)
	}
	var calls atomic.Int32
	return func(context.Context) (mcpsdk.Transport, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("first connect: server not ready")
		}
		return ct, nil
	}, &calls
}
