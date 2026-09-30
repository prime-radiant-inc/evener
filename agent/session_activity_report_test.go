package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

func TestSessionActivityReportPreviewFollowsSettledGeneration(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	id := "dlg_report"
	descriptor := stableToolDescriptor(s, id, "")
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"first report"`)}
	appendEvents := func(events ...delegatestore.Event) {
		t.Helper()
		s.delegateController.mu.Lock()
		_, err := s.delegateController.appendLocked(events...)
		s.delegateController.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	appendEvents(delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}}, delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, at), delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 1}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", at.Add(time.Second), delegateDeliveryID(id, 1), &packet))
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
	assertReport := func(want string, generation uint64) {
		t.Helper()
		for _, snapshot := range s.delegateController.Snapshot().rows {
			if snapshot.id != id {
				continue
			}
			status := delegateStatusInfoFromSnapshot(at, s.ID(), snapshot)
			if status.RunGeneration != generation || delegateUpdatedDataFromStatus(status).RunGeneration != generation {
				t.Fatal("roster/update lost current run generation")
			}
		}
		live, err := s.ListActivityDelegates(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		// Drop only this test's disposable index to prove a fresh journal replay.
		key := s.stateDir + "\x00" + s.ID()
		sessionActivityIndexes.Lock()
		if cached := sessionActivityIndexes.entries[key]; cached != nil {
			delete(sessionActivityIndexes.entries, key)
			sessionActivityIndexes.order.Remove(cached.element)
		}
		sessionActivityIndexes.Unlock()
		retained, err := LoadSessionActivityDelegates(t.Context(), s.stateDir, s.ID(), params)
		if err != nil {
			t.Fatal(err)
		}
		for _, page := range []appwire.SessionDelegatesResponse{live, retained} {
			if len(page.Delegates) != 1 {
				t.Fatalf("rows=%+v", page)
			}
			raw, err := json.Marshal(page.Delegates[0])
			if err != nil {
				t.Fatal(err)
			}
			var row struct {
				ReportPreview string `json:"reportPreview"`
				RunGeneration uint64 `json:"runGeneration"`
			}
			if err = json.Unmarshal(raw, &row); err != nil {
				t.Fatal(err)
			}
			if row.RunGeneration != generation {
				t.Fatalf("run generation=%d, want %d", row.RunGeneration, generation)
			}
			if row.ReportPreview != want {
				t.Fatalf("report preview=%q, want %q", row.ReportPreview, want)
			}
		}
	}
	assertReport("first report", 1)
	read, err := s.activityRead(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref, Scope: appwire.SessionActivityScopeSession})
	if err != nil {
		t.Fatal(err)
	}
	token, walk, err := read.index.token(params, appwire.SessionActivityResourceDelegates, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	candidates, complete, err := read.captureDelegateCandidates(t.Context(), appwire.SessionActivityListParams{Limit: 1}, token, walk)
	read.index.release()
	if err != nil || !complete || len(candidates) != 1 {
		t.Fatalf("capture: candidates=%d complete=%v err=%v", len(candidates), complete, err)
	}
	appendEvents(delegateControllerRunStartedEvent(id, 2, delegatestore.TriggerOwnerInput, at.Add(2*time.Second)))
	assertReport("", 2)
	packet.Message = json.RawMessage(`"second report"`)
	appendEvents(delegatestore.Event{Kind: delegatestore.EventDelegateTerminalPrepared, DelegateID: id, TerminalPrepared: &delegatestore.TerminalPrepared{Generation: 2, Packet: packet}})
	assertReport("", 2)
	appendEvents(delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 2}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", at.Add(3*time.Second), delegateDeliveryID(id, 2), nil))
	assertReport("second report", 2)
	captured := candidates[0].project()
	if captured.RunGeneration != 1 || captured.ReportPreview != "first report" {
		t.Fatalf("captured report changed with controller: %+v", captured)
	}
	appendEvents(delegateControllerRunStartedEvent(id, 3, delegatestore.TriggerAttention, at.Add(4*time.Second)), delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 3}, delegatestore.OutcomeCompleted, delegatestore.DispositionCompletedNoAction, "", at.Add(5*time.Second), "", nil))
	assertReport("", 3)
}

func TestSessionActivityReportPreviewKeepsBoundedText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		kind      delegatestore.PacketKind
		message   json.RawMessage
		want      string
		truncated bool
	}{
		{"text", delegatestore.PacketReported, json.RawMessage(`"report"`), "report", false},
		{"large unicode", delegatestore.PacketReported, mustReportJSON(t, strings.Repeat("界", activityMaxDelegateProseRunes*10)), strings.Repeat("界", activityMaxDelegateProseRunes-1) + "…", true},
		{"empty", delegatestore.PacketReported, json.RawMessage(`""`), "", false},
		{"structured", delegatestore.PacketReported, json.RawMessage(`{"ok":true}`), "", false},
		{"terminal error", delegatestore.PacketTerminalError, json.RawMessage(`"failed"`), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aggregate := &delegatestore.Aggregate{DelegateID: "dlg", Phase: delegatestore.PhaseIdle, LatestOutcome: &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted}, LatestPacket: &delegatestore.TerminalPacket{Kind: tc.kind, Message: tc.message}}
			row := captureSessionActivityDelegate("root", delegatestore.State{"dlg": aggregate}, aggregate, time.Unix(100, 0)).project()
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var preview struct {
				Text      string `json:"reportPreview"`
				Truncated bool   `json:"reportPreviewTruncated"`
			}
			if err = json.Unmarshal(raw, &preview); err != nil {
				t.Fatal(err)
			}
			if preview.Text != tc.want || preview.Truncated != tc.truncated || !utf8.ValidString(preview.Text) {
				t.Fatalf("preview runes=%d truncated=%v, want runes=%d truncated=%v", utf8.RuneCountInString(preview.Text), preview.Truncated, utf8.RuneCountInString(tc.want), tc.truncated)
			}
		})
	}
}
func mustReportJSON(t *testing.T, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSessionActivityReportPreviewPreservesPageMembership(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(100, 0).UTC()
	report := strings.Repeat("\x01", activityMaxDelegateProseRunes+1)
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: mustReportJSON(t, report)}
	for i := range 50 {
		id := fmt.Sprintf("dlg_report_%02d", i)
		descriptor := stableToolDescriptor(s, id, "")
		seedStableReadonlyFinish(t, s, id, descriptor, at, delegateFinish{outcome: delegatestore.OutcomeCompleted, disposition: delegatestore.DispositionReported, endedAt: at.Add(time.Second), packet: &packet}, true)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Limit: 200}
	seen := make(map[string]bool)
	complete := false
	for pageNumber := range 10 {
		page, err := s.ListActivityDelegates(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		if pageNumber == 0 && (page.Page.Complete || len(page.Delegates) == 0 || len(page.Delegates) >= 50) {
			t.Fatalf("byte admission must retain excluded candidates for continuation: rows=%d complete=%v", len(page.Delegates), page.Page.Complete)
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > sessionActivityPageBytes {
			t.Fatalf("page bytes=%d exceeds cap", len(encoded))
		}
		for _, row := range page.Delegates {
			if seen[row.DelegateID] || row.ReportPreview == "" || !row.ReportPreviewTruncated {
				t.Fatalf("lost bounded report or duplicated row: %+v", row)
			}
			seen[row.DelegateID] = true
		}
		if page.Page.Complete {
			complete = true
			break
		}
		if page.Page.NextCursor == "" || params.Cursor == page.Page.NextCursor {
			t.Fatal("page did not advance")
		}
		params.Cursor = page.Page.NextCursor
	}
	if !complete || len(seen) != 50 {
		t.Fatalf("page membership=%d complete=%v", len(seen), complete)
	}
}
