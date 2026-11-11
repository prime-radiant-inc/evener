package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
)

func TestSessionActivityReportPreviewFollowsSettledGeneration(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
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
				ReportPreview string `json:"reportPreview"` //nolint:tagliatelle // AppWire fixture asserts the public camelCase wire contract.
				RunGeneration uint64 `json:"runGeneration"` //nolint:tagliatelle // AppWire fixture asserts the public camelCase wire contract.
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

func TestSessionActivityOpenGenerationOmitsPriorOutcome(t *testing.T) {
	t.Parallel()
	type ending struct {
		outcome       string
		reason        string
		errorText     string
		endedAt       string
		terminal      bool
		reportPreview string
	}
	for _, tc := range []struct {
		name        string
		outcome     delegatestore.OutcomeStatus
		disposition delegatestore.RunDisposition
		reason      string
		errorText   string
		packet      delegatestore.TerminalPacket
		wantReport  string
	}{
		{
			name:        "successful prior generation",
			outcome:     delegatestore.OutcomeCompleted,
			disposition: delegatestore.DispositionReported,
			packet:      delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"prior report"`)},
			wantReport:  "prior report",
		},
		{
			name:        "failed prior generation",
			outcome:     delegatestore.OutcomeFailed,
			disposition: delegatestore.DispositionTerminalError,
			reason:      "run_error",
			errorText:   "provider returned 500",
			packet:      delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"provider returned 500"`)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
			at := time.Unix(100, 0).UTC()
			id := "dlg_generation_ending"
			descriptor := stableToolDescriptor(s, id, "")
			appendEvents := func(events ...delegatestore.Event) {
				t.Helper()
				s.delegateController.mu.Lock()
				_, err := s.delegateController.appendLocked(events...)
				s.delegateController.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			seedStableReadonlyFinish(t, s, id, descriptor, at, delegateFinish{outcome: tc.outcome, disposition: tc.disposition, reason: tc.reason, errorText: tc.errorText, packet: &tc.packet, endedAt: at.Add(time.Second)}, false)

			prior := ending{outcome: string(tc.outcome), reason: tc.reason, errorText: tc.errorText, endedAt: at.Add(time.Second).Format(time.RFC3339Nano), terminal: true, reportPreview: tc.wantReport}
			open := ending{}
			second := ending{outcome: string(delegatestore.OutcomeCompleted), endedAt: at.Add(3 * time.Second).Format(time.RFC3339Nano), terminal: true, reportPreview: "second report"}
			assertRows := func(stage string, generation uint64, phase delegatestore.Phase, want ending) {
				t.Helper()
				for _, scope := range []appwire.SessionActivityScope{appwire.SessionActivityScopeSession, appwire.SessionActivityScopeSubtree} {
					params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: scope}
					live, err := s.ListActivityDelegates(t.Context(), params)
					if err != nil {
						t.Fatalf("%s scope=%s live: %v", stage, scope, err)
					}
					// Remove this test's disposable index so the retained read folds the journal afresh.
					key := s.stateDir + "\x00" + s.ID()
					sessionActivityIndexes.Lock()
					if cached := sessionActivityIndexes.entries[key]; cached != nil {
						delete(sessionActivityIndexes.entries, key)
						sessionActivityIndexes.order.Remove(cached.element)
					}
					sessionActivityIndexes.Unlock()
					retained, err := LoadSessionActivityDelegates(t.Context(), s.stateDir, s.ID(), params)
					if err != nil {
						t.Fatalf("%s scope=%s replay: %v", stage, scope, err)
					}
					for source, page := range map[string]appwire.SessionDelegatesResponse{"live": live, "replayed": retained} {
						if len(page.Delegates) != 1 {
							t.Fatalf("%s scope=%s source=%s rows=%+v", stage, scope, source, page)
						}
						row := page.Delegates[0]
						if row.RunGeneration != generation || row.Phase != string(phase) {
							t.Fatalf("%s scope=%s source=%s did not reach generation=%d phase=%s: %+v", stage, scope, source, generation, phase, row)
						}
						got := ending{outcome: row.Outcome, reason: row.Reason, errorText: row.Error, endedAt: row.RunEndedAt, terminal: row.Terminal, reportPreview: row.ReportPreview}
						if got != want {
							t.Fatalf("%s scope=%s source=%s generation=%d ending=%+v, want %+v; row=%+v", stage, scope, source, generation, got, want, row)
						}
					}
				}
			}
			assertProjectedFrame := func(stage string, generation uint64, phase delegatestore.Phase, want ending) {
				t.Helper()
				var snapshot delegateSnapshot
				for _, candidate := range s.delegateController.Snapshot().rows {
					if candidate.id == id {
						snapshot = candidate
					}
				}
				data := delegateUpdatedDataFromStatus(delegateFrameStatusInfo(at, s.ID(), snapshot))
				projector := appprojector.NewAppEventProjector(s.ID(), encodeRef("", s.ID()))
				notifications := projector.Project(events.SessionEvent{Kind: events.EventDelegateUpdated, SessionID: s.ID(), Data: data})
				if len(notifications) != 1 {
					t.Fatalf("%s projected notifications=%+v, want one", stage, notifications)
				}
				params, ok := notifications[0].Params.(appwire.EvenerDelegateParams)
				if !ok {
					t.Fatalf("%s projected params=%T, want appwire.EvenerDelegateParams", stage, notifications[0].Params)
				}
				frame := params.Delegate
				if frame.RunGeneration != generation || frame.Phase != string(phase) {
					t.Fatalf("%s projected frame did not reach generation=%d phase=%s: %+v", stage, generation, phase, frame)
				}
				got := ending{outcome: frame.Outcome, reason: frame.Reason, errorText: frame.Error, endedAt: frame.RunEndedAt, terminal: frame.Terminal, reportPreview: frame.ReportPreview}
				if got != want {
					t.Fatalf("%s projected generation=%d ending=%+v, want %+v; frame=%+v", stage, generation, got, want, frame)
				}
				fresh, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSession})
				if err != nil || len(fresh.Delegates) != 1 {
					t.Fatalf("%s fresh parity read rows=%d err=%v", stage, len(fresh.Delegates), err)
				}
				if frame.RunGeneration != fresh.Delegates[0].RunGeneration || frame.ProjectionRevision != fresh.Delegates[0].ProjectionRevision {
					t.Fatalf("%s frame generation/revision=%d/%d, fresh read=%d/%d", stage, frame.RunGeneration, frame.ProjectionRevision, fresh.Delegates[0].RunGeneration, fresh.Delegates[0].ProjectionRevision)
				}
			}
			assertRows("settled prior generation", 1, delegatestore.PhaseIdle, prior)

			read, err := s.activityRead(t.Context(), appwire.SessionActivityReadParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSession})
			if err != nil {
				t.Fatal(err)
			}
			token, walk, err := read.index.token(appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSession}, appwire.SessionActivityResourceDelegates, s.ID())
			if err != nil {
				read.index.release()
				t.Fatal(err)
			}
			candidates, complete, err := read.captureDelegateCandidates(t.Context(), appwire.SessionActivityListParams{Limit: 1}, token, walk)
			read.index.release()
			if err != nil || !complete || len(candidates) != 1 {
				t.Fatalf("capture: candidates=%d complete=%v err=%v", len(candidates), complete, err)
			}

			appendEvents(delegateControllerRunStartedEvent(id, 2, delegatestore.TriggerOwnerInput, at.Add(2*time.Second)))
			assertRows("open generation", 2, delegatestore.PhaseRunning, open)
			assertProjectedFrame("open generation", 2, delegatestore.PhaseRunning, open)
			packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"second report"`)}
			appendEvents(delegatestore.Event{Kind: delegatestore.EventDelegateTerminalPrepared, DelegateID: id, TerminalPrepared: &delegatestore.TerminalPrepared{Generation: 2, Packet: packet}})
			assertRows("prepared open generation", 2, delegatestore.PhaseSettling, open)
			assertProjectedFrame("prepared open generation", 2, delegatestore.PhaseSettling, open)
			appendEvents(delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 2}, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", at.Add(3*time.Second), delegateDeliveryID(id, 2), nil))
			assertRows("settled second generation", 2, delegatestore.PhaseIdle, second)
			assertProjectedFrame("settled second generation", 2, delegatestore.PhaseIdle, second)

			captured := candidates[0].project()
			capturedEnding := ending{outcome: captured.Outcome, reason: captured.Reason, errorText: captured.Error, endedAt: captured.RunEndedAt, terminal: captured.Terminal, reportPreview: captured.ReportPreview}
			if captured.RunGeneration != 1 || capturedEnding != prior {
				t.Fatalf("captured generation one changed after generation two settlement: %+v", captured)
			}
		})
	}
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
				Text      string `json:"reportPreview"`          //nolint:tagliatelle // AppWire fixture asserts the public camelCase wire contract.
				Truncated bool   `json:"reportPreviewTruncated"` //nolint:tagliatelle // AppWire fixture asserts the public camelCase wire contract.
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
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
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
