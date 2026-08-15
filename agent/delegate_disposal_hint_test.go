package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

const wantDisposalSentence = "When you're done with this delegate's work (e.g., after merging it), dispose its worktree and branch: manage_worktree op=dispose id=dlg_01ISODISPOSALHINT0000001."

func TestDisposalHint_StableOwnedDelegateCarriesSentence(t *testing.T) {
	s := newTestSession(t)
	desc := delegatestore.Descriptor{OwnerSessionID: s.id}
	if got := s.stableDelegateDisposalHint(desc, "dlg_01ISODISPOSALHINT0000001"); got != wantDisposalSentence {
		t.Fatalf("stable disposal hint = %q, want %q", got, wantDisposalSentence)
	}
}

func TestDisposalHint_JSONRoundTripEmitsField(t *testing.T) {
	in := &delegateWorktreeToolResult{Path: "/lane", Branch: "b", HeadSHA: "abc123", DisposalHint: wantDisposalSentence}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"disposal_hint":`) {
		t.Fatalf("JSON missing disposal_hint key: %s", blob)
	}
	var back delegateWorktreeToolResult
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.DisposalHint != wantDisposalSentence || back.HeadSHA != "abc123" {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestDisposalHint_EmptyHintOmittedFromJSON(t *testing.T) {
	blob, err := json.Marshal(&delegateWorktreeToolResult{Path: "/lane", Branch: "b"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "disposal_hint") {
		t.Fatalf("empty hint must be omitted, got: %s", blob)
	}
}

func TestDisposalHint_StableOwnershipAndToolGate(t *testing.T) {
	s := newTestSession(t)
	id := "dlg_01ISODISPOSALHINT0000001"
	if got := s.stableDelegateDisposalHint(delegatestore.Descriptor{OwnerSessionID: "another-session"}, id); got != "" {
		t.Fatalf("foreign descriptor hint = %q", got)
	}
	s.reg.Remove("manage_worktree")
	if got := s.stableDelegateDisposalHint(delegatestore.Descriptor{OwnerSessionID: s.id}, id); got != "" {
		t.Fatalf("session without dispose tool hint = %q", got)
	}
}

func TestDisposalHint_NonIsolatedStableDelegateHasNoReport(t *testing.T) {
	s := newTestSession(t)
	if report := s.stableDelegateWorktreeReport(delegatestore.Descriptor{OwnerSessionID: s.id}); report != nil {
		t.Fatalf("non-isolated delegate report = %+v", report)
	}
}
