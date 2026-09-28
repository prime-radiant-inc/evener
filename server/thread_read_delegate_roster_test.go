package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// A coordinator that has launched hundreds of delegates must not pay for every
// brief, final message and structured result on its first thread/read. The
// roster keeps every identity and lifecycle field clients read from it; the
// long text lives in the activity tree and each delegate's own transcript.
func TestThreadReadDelegateRosterStaysSmallForDelegateHeavySession(t *testing.T) {
	const delegates = 300
	brief := strings.Repeat("Investigate the flaky scheduler and report every finding in detail. ", 70)
	message := json.RawMessage(strconv.Quote(strings.Repeat("final report line. ", 100)))
	structured := json.RawMessage(`{"findings":"` + strings.Repeat("x", 900) + `"}`)
	rows := make([]DelegateStatusInfo, 0, delegates)
	for i := range delegates {
		rows = append(rows, DelegateStatusInfo{
			DelegateID: fmt.Sprintf("dlg_%03d", i), OwnerSessionID: "th_1", RootSessionID: "th_1",
			ChildSessionID: fmt.Sprintf("child_%03d", i), TranscriptRef: fmt.Sprintf("local:child_%03d", i),
			Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle", Outcome: "completed", Terminal: true,
			Resumable: true, ProjectionRevision: 4, Task: brief, Description: brief, AgentType: "explorer",
			Model: "gpt-5", PacketKind: "final", Message: message, StructuredResult: structured,
		})
	}
	srv := NewServer(ServerConfig{})
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.detailedStatus = DetailedStatus{Delegates: rows} })

	thread := readThreadOverWire(t, srv, "local:th_1")

	roster := thread.Evener.Diagnostics.Delegates
	if len(roster) != delegates {
		t.Fatalf("roster has %d delegates, want %d", len(roster), delegates)
	}
	encoded, err := json.Marshal(roster)
	if err != nil {
		t.Fatal(err)
	}
	const budgetBytes = 300 * 1024
	if len(encoded) > budgetBytes {
		t.Fatalf("roster is %d bytes for %d delegates, want at most %d", len(encoded), delegates, budgetBytes)
	}
	t.Logf("roster bytes for %d delegates: %d", delegates, len(encoded))
	got := roster[7]
	if got.DelegateID != "dlg_007" || got.ChildSessionID != "child_007" || got.TranscriptRef != "local:child_007" ||
		got.Lifecycle != "idle" || got.Outcome != "completed" || !got.Terminal || !got.Resumable ||
		got.ProjectionRevision != 4 || got.AgentType != "explorer" || got.Model != "gpt-5" {
		t.Fatalf("roster row lost identity or lifecycle fields: %+v", got)
	}
	if got.Task == "" || !strings.HasPrefix(brief, strings.TrimSuffix(got.Task, "…")) || got.Description != "" {
		t.Fatalf("roster label = %q (description %q), want the start of the brief once", got.Task, got.Description)
	}
	if len(got.Message) != 0 || len(got.StructuredResult) != 0 {
		t.Fatalf("roster carries the final message/result (%d, %d bytes)", len(got.Message), len(got.StructuredResult))
	}
}
