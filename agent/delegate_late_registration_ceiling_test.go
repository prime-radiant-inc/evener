package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// TestLateRegistrationJudgesAgainstTheEffectiveCeiling pins that a tool a retry
// recovers after startup survives the policy check.
//
// A cold-resumed delegate may re-admit a name the frozen ceiling excluded only
// for reachability. When a fallback server's first connection fails at startup
// and succeeds during a later turn, the retry registers the tool and then applies
// the session's ceiling. Judging against the bare frozen list deletes the tool the
// retry just recovered, and no later retry restores it because the server is
// connected now — so the name is lost for the rest of the run.
func TestLateRegistrationJudgesAgainstTheEffectiveCeiling(t *testing.T) {
	t.Parallel()

	const (
		recovered  = "svc__echo" // reachability-excluded at creation, registered by the retry
		roleDenied = "job_status"
	)
	s := &Session{}
	s.cfg.spawn.toolNameCeiling = []string{"read_file", "communicate"}
	s.cfg.spawn.ceilingReachabilityExcluded = []string{recovered}
	s.reg = tool.NewRegistry()
	for _, name := range []string{recovered, "read_file"} {
		if err := s.reg.Register(regTool(llm.ToolDefinition{Name: name, Description: "fixture"})); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}

	// The bare frozen list denies the recovered name — this is what the retry
	// used to consult.
	if !s.hubToolDenied(recovered) {
		t.Fatalf("fixture is wrong: the frozen ceiling does not exclude %q", recovered)
	}

	// The ceiling the session is actually running with admits it, because the
	// registry now holds the name.
	effective := effectiveChildToolCeiling(s.cfg.spawn.toolNameCeiling, s.cfg.spawn.ceilingReachabilityExcluded, s.reg)
	if s.hubToolDeniedAgainst(effective, recovered) {
		t.Errorf("a retry that recovered %q would delete it: the effective ceiling still denies it", recovered)
	}

	// The other direction: the role's authorization is unchanged, so a name
	// outside the frozen ceiling is still denied even though the registry holds it.
	if err := s.reg.Register(regTool(llm.ToolDefinition{Name: roleDenied, Description: "fixture"})); err != nil {
		t.Fatalf("register %q: %v", roleDenied, err)
	}
	if !s.hubToolDeniedAgainst(effective, roleDenied) {
		t.Errorf("%q is outside the frozen ceiling and must stay denied", roleDenied)
	}
	if !s.hubToolDenied(roleDenied) {
		t.Errorf("%q is outside the frozen ceiling and hubToolDenied must keep denying it", roleDenied)
	}
}
