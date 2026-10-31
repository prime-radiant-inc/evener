package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestDisposalHint_StableOwnedDelegateDistinguishesDirtyWorktree(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	desc := delegatestore.Descriptor{OwnerSessionID: s.id}
	clean := s.delegateDisposalHint(desc, "dlg_01ISODISPOSALHINT0000001", false)
	dirty := s.delegateDisposalHint(desc, "dlg_01ISODISPOSALHINT0000001", true)
	if clean == "" || dirty == "" {
		t.Fatalf("clean hint %q and dirty hint %q must both be present", clean, dirty)
	}
	if clean == dirty {
		t.Fatalf("dirty hint must differ from clean disposal guidance: %q", dirty)
	}
	if !strings.Contains(dirty, "force_dirty") {
		t.Fatalf("dirty hint must identify the explicit discard option: %q", dirty)
	}
}

func TestDisposalHint_JSONRoundTripEmitsField(t *testing.T) {
	t.Parallel()
	in := &delegateWorktreeReport{Path: "/lane", Branch: "b", HeadSHA: "abc123", DisposalHint: "cleanup guidance"}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"disposal_hint":`) {
		t.Fatalf("JSON missing disposal_hint key: %s", blob)
	}
	var back delegateWorktreeReport
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.DisposalHint != in.DisposalHint || back.HeadSHA != "abc123" {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestDisposalHint_EmptyHintOmittedFromJSON(t *testing.T) {
	t.Parallel()
	blob, err := json.Marshal(&delegateWorktreeReport{Path: "/lane", Branch: "b"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "disposal_hint") {
		t.Fatalf("empty hint must be omitted, got: %s", blob)
	}
}

func TestDisposalHint_StableOwnershipAndToolGate(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	id := "dlg_01ISODISPOSALHINT0000001"
	if got := s.delegateDisposalHint(delegatestore.Descriptor{OwnerSessionID: "another-session"}, id, true); got != "" {
		t.Fatalf("foreign descriptor hint = %q", got)
	}
	s.reg.Remove("manage_worktree")
	if got := s.delegateDisposalHint(delegatestore.Descriptor{OwnerSessionID: s.id}, id, true); got != "" {
		t.Fatalf("session without dispose tool hint = %q", got)
	}
}

func TestDisposalHint_TerminalMetadataAndInlineResultPreserveHint(t *testing.T) {
	t.Parallel()
	hint := "inspect before disposal; force_dirty discards changes"
	inputs := delegateTerminalRunInputs{
		descriptor: delegatestore.Descriptor{OwnerSessionID: "owner"},
		worktree:   &delegateWorktreeReport{Path: "/lane", Branch: "feature", HeadSHA: "abc123", Ahead: 2, Dirty: true, DisposalHint: hint},
	}
	metadata := delegateTerminalMetadataFromRun(inputs)
	if metadata.Worktree == nil || metadata.Worktree.DisposalHint != hint || !metadata.Worktree.Dirty {
		t.Fatalf("terminal worktree metadata = %+v", metadata.Worktree)
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal terminal metadata: %v", err)
	}
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Metadata: rawMetadata}
	var result sendMessageResult
	populateDelegateSendResult(&result, packet)
	if result.Worktree == nil || result.Worktree.DisposalHint != hint || !result.Worktree.Dirty {
		t.Fatalf("inline worktree result = %+v", result.Worktree)
	}
}

func TestDisposalHint_NonIsolatedDelegateHasNoReport(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if report := s.descriptorWorktreeReport(delegatestore.Descriptor{OwnerSessionID: s.id}); report != nil {
		t.Fatalf("non-isolated delegate report = %+v", report)
	}
}
