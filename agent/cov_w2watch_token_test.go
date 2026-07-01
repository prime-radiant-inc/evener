package agent

import "testing"

// jobManagerForToken resolves which jobManager owns a token's pending state:
// nil token -> nil; own-session token (empty child id) -> the session's manager;
// child token with no subagent manager -> nil; child token whose subagent exists
// -> that child's manager; child token with no matching subagent -> nil.
func TestW2Watch_jobManagerForToken(t *testing.T) {
	jm := newTestJM(t)

	// No subagent manager: a child-scoped token cannot resolve.
	noSub := &Session{id: "S1", jobManager: jm}
	if got := noSub.jobManagerForToken(nil); got != nil {
		t.Fatalf("nil token -> %v, want nil", got)
	}
	if got := noSub.jobManagerForToken(&watchSendToken{}); got != jm {
		t.Fatalf("own-session token -> %v, want the session manager", got)
	}
	if got := noSub.jobManagerForToken(&watchSendToken{ChildSessionID: "C1"}); got != nil {
		t.Fatalf("child token with no subagent manager -> %v, want nil", got)
	}

	// With a subagent manager holding one child session.
	sm := newSubagentManager(nil)
	parent := &Session{id: "S1", jobManager: jm, subagents: sm}
	child := newSession(t)
	sm.track(&subagent{id: "C1", sess: child})

	if got := parent.jobManagerForToken(&watchSendToken{ChildSessionID: "C1"}); got != child.jobManager {
		t.Fatalf("child token -> %v, want the child's manager", got)
	}
	if got := parent.jobManagerForToken(&watchSendToken{ChildSessionID: "MISSING"}); got != nil {
		t.Fatalf("child token with no matching subagent -> %v, want nil", got)
	}
}
