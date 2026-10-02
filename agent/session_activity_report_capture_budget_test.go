package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivityReportCaptureBudgetPreservesPageContinuation(t *testing.T) {
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-AGENTS.md")}))
	const count = 200
	at := time.Unix(100, 0).UTC()
	message := json.RawMessage(`"` + strings.Repeat(`\ud83d\ude00`, 5000) + `"`)
	packet := &delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: message}
	for i := range count {
		id := fmt.Sprintf("dlg_report_budget_%03d", i)
		descriptor := stableToolDescriptor(s, id, "")
		// Wide facts force response admission to exclude a later captured row.
		descriptor.Description = strings.Repeat("😀", activityMaxDelegateProseRunes)
		descriptor.Task = descriptor.Description
		started := at.Add(time.Duration(i) * time.Second)
		s.delegateController.mu.Lock()
		_, err := s.delegateController.appendLocked(
			delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
			delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, started),
			delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 1}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", started.Add(time.Second), delegateDeliveryID(id, 1), packet),
		)
		s.delegateController.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Limit: count}
	read, err := s.activityRead(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil {
		t.Fatal(err)
	}
	token, walk, err := read.index.token(params, appwire.SessionActivityResourceDelegates, s.ID())
	if err != nil {
		read.index.release()
		t.Fatal(err)
	}
	candidates, complete, err := read.captureDelegateCandidates(t.Context(), params, token, walk)
	read.index.release()
	if err != nil {
		t.Fatal(err)
	}
	capturedBytes := 0
	for _, candidate := range candidates {
		capturedBytes += len(candidate.report)
	}
	if capturedBytes > sessionActivityPageBytes+activityMaxReportPreviewBytes {
		t.Fatalf("captured reports=%d bytes across %d candidates, bound=%d", capturedBytes, len(candidates), sessionActivityPageBytes+activityMaxReportPreviewBytes)
	}
	if complete || len(candidates) == 0 {
		t.Fatalf("capture complete=%v candidates=%d", complete, len(candidates))
	}
	// Mutating an owned candidate must not mutate its durable packet.
	first := &candidates[0]
	if len(first.report) == 0 {
		t.Fatal("missing captured report")
	}
	s.delegateController.mu.Lock()
	durableReport := s.delegateController.durable[first.key.ID].LatestPacket.Message
	original := durableReport[0]
	first.report[0] = ' '
	aliases := durableReport[0] != original
	first.report[0] = original
	s.delegateController.mu.Unlock()
	if aliases {
		t.Fatal("candidate retains the durable report backing array")
	}
	seen := make(map[string]bool)
	for pages := 0; pages <= count; pages++ {
		page, err := s.ListActivityDelegates(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(page)
		if err != nil || len(encoded) > sessionActivityPageBytes {
			t.Fatalf("page bytes=%d error=%v", len(encoded), err)
		}
		if pages == 0 && len(page.Delegates) >= len(candidates) {
			t.Fatal("fixture did not exercise later captured-row response exclusion")
		}
		for _, row := range page.Delegates {
			if seen[row.DelegateID] {
				t.Fatalf("duplicate delegate %s", row.DelegateID)
			}
			seen[row.DelegateID] = true
			if row.RunGeneration != 1 || row.ReportPreview != strings.Repeat("😀", activityMaxDelegateProseRunes-1)+"…" || !row.ReportPreviewTruncated {
				t.Fatalf("lost settled report facts for %s", row.DelegateID)
			}
		}
		if page.Page.Complete {
			if len(seen) != count {
				t.Fatalf("terminal page retained %d of %d delegates", len(seen), count)
			}
			return
		}
		if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
			t.Fatal("report-budget page did not advance its opaque cursor")
		}
		params.Cursor = page.Page.NextCursor
	}
	t.Fatal("delegate walk did not reach completion")
}
