package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// Each case crosses the real human RPC owner and the Manager's typed owner in
// separate stores. A rejected request may not alter either store.
func TestManagerHumanControlsParity(t *testing.T) {
	cases := []struct {
		name, method string
		request      appwire.SessionControlRequest
		effect       func(*testing.T, *WebServer) any
		accepted     bool
	}{
		{"approval", appwire.MethodEvenerSandboxEscalationResolve, appwire.SessionControlRequest{Action: "approval", Approval: &appwire.SandboxEscalationResolveParams{Ref: "local:missing", EscalationID: "missing", Approve: true}}, nil, false},
		{"resume", appwire.MethodThreadResume, appwire.SessionControlRequest{Action: "resume", Resume: &appwire.ThreadResumeParams{Ref: "local:missing"}}, nil, false},
		{"fork", appwire.MethodThreadFork, appwire.SessionControlRequest{Action: "fork", Fork: &appwire.ThreadForkParams{Ref: "local:02wMz5Txv1C3Hut0M8GCeB", Aside: true}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			matches, err := filepath.Glob(filepath.Join(w.cfg.StateDir, "projects", "project-control-0123456789", "sessions", "*.meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range matches {
				id := filepath.Base(match)
				id = id[:len(id)-len(".meta.json")]
				if id == "02wMz5Txv1C3Hut0M8GCeB" {
					continue
				}
				meta, err := schema.LoadSessionMeta(filepath.Dir(filepath.Dir(match)), id)
				if err != nil || meta.ParentSessionID != "02wMz5Txv1C3Hut0M8GCeB" {
					t.Fatalf("fork child meta=%+v err=%v", meta, err)
				}
			}
			return len(matches)
		}, true},
		{"clear", appwire.MethodThreadClear, appwire.SessionControlRequest{Action: "clear", Clear: &appwire.ThreadClearParams{Ref: "local:missing", ClientMutationID: "clear-1", ExpectedInstanceID: "missing"}}, nil, false},
		{"model", appwire.MethodThreadModelSet, appwire.SessionControlRequest{Action: "model", Model: &appwire.ThreadModelSetParams{Ref: "local:missing", Model: "test"}}, nil, false},
		{"vision_model", appwire.MethodThreadVisionModelSet, appwire.SessionControlRequest{Action: "vision_model", VisionModel: &appwire.ThreadVisionModelSetParams{Ref: "local:missing", VisionModel: "test"}}, nil, false},
		{"reasoning_effort", appwire.MethodThreadReasoningEffortSet, appwire.SessionControlRequest{Action: "reasoning_effort", ReasoningEffort: &appwire.ThreadReasoningEffortSetParams{Ref: "local:missing"}}, nil, false},
		{"compact", appwire.MethodThreadCompactStart, appwire.SessionControlRequest{Action: "compact", Compact: &appwire.ThreadCompactStartParams{Ref: "local:missing"}}, nil, false},
		{"drain_queue", appwire.MethodTurnDrainAsSteer, appwire.SessionControlRequest{Action: "drain_queue", DrainQueue: &appwire.TurnDrainAsSteerParams{Ref: "local:missing", ClientMutationID: "drain-1", ExpectedInstanceID: "missing"}}, nil, false},
		{"promote_queued", appwire.MethodTurnPromoteQueuedAsSteer, appwire.SessionControlRequest{Action: "promote_queued", PromoteQueued: &appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:missing", ClientMutationID: "promote-1", ExpectedInstanceID: "missing", ExpectedEntryID: "stale"}}, nil, false},
		{"cancel_queued", appwire.MethodTurnCancelQueued, appwire.SessionControlRequest{Action: "cancel_queued", CancelQueued: &appwire.TurnCancelQueuedParams{Ref: "local:missing", ClientMutationID: "cancel-1", ExpectedInstanceID: "missing", ExpectedEntryID: "stale"}}, nil, false},
		{"rename", appwire.MethodEvenerThreadNameSet, appwire.SessionControlRequest{Action: "rename", Rename: &appwire.ThreadNameSetParams{Ref: "local:" + projectDeleteCanonicalSessionIDs[0], Name: "Changed"}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			entry, ok := w.cfg.Past.Find(projectDeleteCanonicalSessionIDs[0])
			if !ok {
				t.Fatal("rename target missing")
			}
			meta, err := schema.LoadSessionMeta(entry.StateDir, entry.ID)
			if err != nil {
				t.Fatal(err)
			}
			return meta.Name
		}, true},
		{"goal", appwire.MethodGoalSet, appwire.SessionControlRequest{Action: "goal", Goal: &appwire.GoalSetParams{Ref: "local:missing", Objective: "Changed"}}, nil, false},
		{"human_notes", appwire.MethodNotesHumanSet, appwire.SessionControlRequest{Action: "human_notes", HumanNotes: &appwire.NotesHumanSetParams{Ref: "local:missing", ClientMutationID: "notes-1", ExpectedInstanceID: "missing", Note: "Changed"}}, nil, false},
		{"remove_url", appwire.MethodUrlsRemove, appwire.SessionControlRequest{Action: "remove_url", RemoveURL: &appwire.UrlsRemoveParams{Ref: "local:missing", ClientMutationID: "url-1", ExpectedInstanceID: "missing", ID: "missing"}}, nil, false},
		{"archive", appwire.MethodEvenerArchiveSet, appwire.SessionControlRequest{Action: "archive", Archive: &appwire.ArchiveParams{Kind: appwire.ArchiveTargetSession, ID: "local:session-a", Archived: true}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			d, e := w.cfg.Archive.Decisions()
			if e != nil {
				t.Fatal(e)
			}
			return d[hubcore.ArchiveKey{Kind: "session", ID: "session-a"}]
		}, true},
		{"delete", appwire.MethodEvenerSessionDelete, appwire.SessionControlRequest{Action: "delete", Delete: &appwire.SessionDeleteParams{Ref: "local:" + projectDeleteCanonicalSessionIDs[0]}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			_, present := w.cfg.Past.Find(projectDeleteCanonicalSessionIDs[0])
			return present
		}, true},
		{"pin", appwire.MethodEvenerSessionPinAssign, appwire.SessionControlRequest{Action: "pin", Pin: &appwire.SessionPinAssignParams{SessionRef: "local:session-a", SectionName: new("Research")}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			a, e := w.cfg.PinSections.Assignments()
			if e != nil {
				t.Fatal(e)
			}
			return len(a)
		}, true},
		{"unpin", appwire.MethodEvenerSessionPinUnpin, appwire.SessionControlRequest{Action: "unpin", Unpin: &appwire.SessionPinUnpinParams{SessionRef: "local:session-a"}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			a, e := w.cfg.PinSections.Assignments()
			if e != nil {
				t.Fatal(e)
			}
			return len(a)
		}, true},
		{"seen", appwire.MethodEvenerSessionSeenSet, appwire.SessionControlRequest{Action: "seen", Seen: &appwire.SessionSeenSetParams{Sessions: []appwire.SessionSeenMark{{Ref: "local:session-a", SeenThrough: time.Now().Add(-time.Minute).UnixMilli()}}}}, func(t *testing.T, w *WebServer) any {
			t.Helper()
			s, e := w.cfg.SessionSeen.Snapshot()
			if e != nil {
				t.Fatal(e)
			}
			return len(s.Records)
		}, true},
	}
	names := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names[tc.name] = true
			fixture := func() *WebServer {
				if tc.name == "delete" || tc.name == "rename" || tc.name == "fork" {
					root := t.TempDir()
					workDir := filepath.Join(root, "project")
					if err := os.MkdirAll(workDir, 0o755); err != nil {
						t.Fatal(err)
					}
					stateDir := filepath.Join(root, "projects", "project-control-0123456789")
					if tc.name == "fork" {
						buildRPCParentSessionWithWorkingDir(t, stateDir, workDir)
					} else {
						writeSession(t, stateDir, projectDeleteCanonicalSessionIDs[0], workDir)
					}
					past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
					if _, err := past.Rebuild(); err != nil {
						t.Fatal(err)
					}
					return NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries(), Archive: hubcore.NewArchiveStore(filepath.Join(root, "archive.db")), PinSections: hubcore.NewPinSectionStore(filepath.Join(root, "pins.db")), SessionSeen: hubcore.NewSessionSeenStore(filepath.Join(root, "seen.db"))})
				}
				w := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), HubStateRoot: t.TempDir(), Archive: hubcore.NewArchiveStore(t.TempDir() + "/archive.db"), PinSections: hubcore.NewPinSectionStore(t.TempDir() + "/pins.db"), SessionSeen: hubcore.NewSessionSeenStore(t.TempDir() + "/seen.db")})
				w.injectMetasForTest([]schema.SessionMeta{topLevelMeta("session-a")})
				return w
			}
			human, manager := fixture(), fixture()
			if tc.name == "unpin" {
				for _, w := range []*WebServer{human, manager} {
					_, _, e := w.cfg.PinSections.CreateOrReuseAndAssign("Research", "", "session-a", time.Now())
					if e != nil {
						t.Fatal(e)
					}
				}
			}
			var beforeHuman, beforeManager any
			beforeHumanStores, beforeManagerStores := controlStoresSnapshot(t, human), controlStoresSnapshot(t, manager)
			if tc.effect != nil {
				beforeHuman, beforeManager = tc.effect(t, human), tc.effect(t, manager)
			}
			payload := func() any {
				switch tc.name {
				case "approval":
					return tc.request.Approval
				case "resume":
					return tc.request.Resume
				case "fork":
					return tc.request.Fork
				case "clear":
					return tc.request.Clear
				case "model":
					return tc.request.Model
				case "vision_model":
					return tc.request.VisionModel
				case "reasoning_effort":
					return tc.request.ReasoningEffort
				case "compact":
					return tc.request.Compact
				case "drain_queue":
					return tc.request.DrainQueue
				case "promote_queued":
					return tc.request.PromoteQueued
				case "cancel_queued":
					return tc.request.CancelQueued
				case "rename":
					return tc.request.Rename
				case "goal":
					return tc.request.Goal
				case "human_notes":
					return tc.request.HumanNotes
				case "remove_url":
					return tc.request.RemoveURL
				case "archive":
					return tc.request.Archive
				case "delete":
					return tc.request.Delete
				case "pin":
					return tc.request.Pin
				case "unpin":
					return tc.request.Unpin
				default:
					return tc.request.Seen
				}
			}
			raw, e := json.Marshal(payload())
			if e != nil {
				t.Fatal(e)
			}
			var humanLog, managerLog bytes.Buffer
			humanCtx, managerCtx := t.Context(), t.Context()
			if tc.name == "resume" {
				humanCtx, _ = withThreadLifecycleLog(humanCtx, "resume", "missing", &humanLog)
				managerCtx, _ = withThreadLifecycleLog(managerCtx, "resume", "missing", &managerLog)
			}
			_, humanErr := human.appRPC.Router().Dispatch(humanCtx, appwire.Request{ID: appwire.NewIntID(1), Method: tc.method, Params: raw})
			owner := &sessionOperations{cfg: manager.cfg, sources: manager.sources, navigation: manager.navigation, resolve: manager.resolveTopLevelSessionRef}
			_, managerErr := owner.Control(managerCtx, tc.request)
			if (humanErr == nil) != tc.accepted || (managerErr == nil) != tc.accepted {
				t.Fatalf("human=%v manager=%v accepted=%v", humanErr, managerErr, tc.accepted)
			}
			if tc.name == "resume" {
				for _, output := range []string{humanLog.String(), managerLog.String()} {
					records := assertThreadLifecycleRecords(t, output)
					assertThreadLifecycleOutcome(t, records, "request", "error", "failed")
				}
			}
			if !tc.accepted {
				humanWire, managerWire := appserver.WireError(humanErr), appserver.WireError(managerErr)
				if humanWire.Code != managerWire.Code || humanWire.Message != managerWire.Message {
					t.Fatalf("human refusal=%v manager refusal=%v", humanWire, managerWire)
				}
				if after := controlStoresSnapshot(t, human); !reflect.DeepEqual(beforeHumanStores, after) {
					t.Fatalf("human refused %s but state changed: before=%v after=%v", tc.name, beforeHumanStores, after)
				}
				if after := controlStoresSnapshot(t, manager); !reflect.DeepEqual(beforeManagerStores, after) {
					t.Fatalf("Manager refused %s but state changed: before=%v after=%v", tc.name, beforeManagerStores, after)
				}
			}
			if tc.effect != nil {
				h, m := tc.effect(t, human), tc.effect(t, manager)
				if !reflect.DeepEqual(h, m) {
					t.Fatalf("effects diverged: human=%v manager=%v", h, m)
				}
				if tc.accepted && reflect.DeepEqual(h, beforeHuman) || !tc.accepted && (!reflect.DeepEqual(h, beforeHuman) || !reflect.DeepEqual(m, beforeManager)) {
					t.Fatalf("accepted mutation unchanged or rejected mutation changed: before=%v after=%v", beforeHuman, h)
				}
			}
		})
	}
	if len(names) != 20 {
		t.Fatalf("behavior cases=%d, want 20", len(names))
	}
	for _, action := range []string{"approval", "resume", "fork", "clear", "model", "vision_model", "reasoning_effort", "compact", "drain_queue", "promote_queued", "cancel_queued", "rename", "goal", "human_notes", "remove_url", "archive", "delete", "pin", "unpin", "seen"} {
		if !names[action] {
			t.Fatalf("schema-advertised action %q has no real-owner behavior case", action)
		}
	}
}
func controlStoresSnapshot(t *testing.T, w *WebServer) any {
	t.Helper()
	archive, err := w.cfg.Archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	pins, err := w.cfg.PinSections.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	seen, err := w.cfg.SessionSeen.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return struct{ Archive, Pins, Seen any }{archive, pins, seen.Records}
}

func TestManagerHumanControlsAttributionAndClosedUnion(t *testing.T) {
	const managerRef = "local:manager"
	w := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), HubStateRoot: t.TempDir(), HubID: "hub-test", Archive: hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db")), PinSections: hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db")), SessionSeen: hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "seen.db"))})
	w.injectMetasForTest([]schema.SessionMeta{topLevelMeta("session-a")})
	o := &sessionOperations{cfg: w.cfg, sources: w.sources, navigation: w.navigation, resolve: w.resolveTopLevelSessionRef}
	caller := appwire.ManagerView{Ref: managerRef, InstanceID: "manager-instance", Manager: true}
	ctx := context.WithValue(t.Context(), sessionBindingContextKey{}, appwire.SessionBinding{Ref: managerRef, InstanceID: caller.InstanceID, HubID: "hub-test", BindingID: "authenticated-binding"})
	archive := &appwire.ArchiveParams{Kind: appwire.ArchiveTargetSession, ID: "local:session-a", Archived: true}
	bad := appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "archive", Archive: archive, Seen: &appwire.SessionSeenSetParams{}}}
	before := controlStoresSnapshot(t, w)
	if _, err := o.Invoke(ctx, caller, bad); err == nil {
		t.Fatal("multiple typed payloads accepted")
	}
	if after := controlStoresSnapshot(t, w); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected mixed payload mutated state: before=%v after=%v", before, after)
	}
	response, err := o.Invoke(ctx, caller, appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "archive", Archive: archive}})
	if err != nil || response.Control == nil || response.Control.Archive == nil {
		t.Fatalf("Manager archive response=%+v err=%v", response, err)
	}
	if response.Actor.Kind != "manager" || response.Actor.ManagerRef != managerRef || response.Control.Actor != response.Actor {
		t.Fatalf("authenticated control actor lost: operation=%+v control=%+v", response.Actor, response.Control.Actor)
	}
	decisions, err := w.cfg.Archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-a"}] {
		t.Fatalf("Manager archive did not reach archive store: %+v", decisions)
	}
	archive.Archived = false
	if _, err := o.Invoke(ctx, caller, appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "archive", Archive: archive}}); err != nil {
		t.Fatalf("Manager unarchive: %v", err)
	}
	decisions, err = w.cfg.Archive.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if decisions[hubcore.ArchiveKey{Kind: "session", ID: "session-a"}] {
		t.Fatalf("archive boolean did not unarchive: %+v", decisions)
	}
	for name, marks := range map[string][]appwire.SessionSeenMark{
		"exclusive": {{Ref: "local:session-a", SeenThrough: time.Now().Add(-time.Minute).UnixMilli(), Unread: true}},
		"future":    {{Ref: "local:session-a", SeenThrough: time.Now().Add(48 * time.Hour).UnixMilli()}},
		"over_500":  make([]appwire.SessionSeenMark, 501),
	} {
		t.Run(name, func(t *testing.T) {
			before := controlStoresSnapshot(t, w)
			_, err := o.Invoke(ctx, caller, appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Action: "seen", Seen: &appwire.SessionSeenSetParams{Sessions: marks}}})
			if err == nil {
				t.Fatal("invalid seen marks accepted")
			}
			if after := controlStoresSnapshot(t, w); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected seen marks changed store: before=%v after=%v", before, after)
			}
		})
	}
}

func TestManagerHumanControlsClearWhitespaceGuard(t *testing.T) {
	w := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	params := appwire.ThreadClearParams{Ref: "local:missing", ClientMutationID: " ", ExpectedInstanceID: " "}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	_, humanErr := w.appRPC.Router().Dispatch(t.Context(), appwire.Request{ID: appwire.NewIntID(1), Method: appwire.MethodThreadClear, Params: raw})
	_, managerErr := (&sessionOperations{cfg: w.cfg, sources: w.sources}).Control(t.Context(), appwire.SessionControlRequest{Action: "clear", Clear: &params})
	if humanErr == nil || managerErr == nil {
		t.Fatalf("whitespace clear guard changed: human=%v Manager=%v", humanErr, managerErr)
	}
	// The contract is that a Manager refusal names the same reason as the human
	// one, not that either carries a particular phrasing.
	if human, manager := appserver.WireError(humanErr), appserver.WireError(managerErr); human.Code != manager.Code || human.Message != manager.Message {
		t.Fatalf("human and Manager disagree on the whitespace clear refusal: human=%v Manager=%v", humanErr, managerErr)
	}
}

func TestManagerStopModesOwners(t *testing.T) {
	cases := []struct {
		name, method string
		request      appwire.SessionStopRequest
		argument     any
	}{
		{"interrupt", appwire.MethodTurnInterrupt, appwire.SessionStopRequest{Mode: "interrupt", Interrupt: &appwire.TurnInterruptParams{Ref: "local:missing", ClientMutationID: "stop-1", ExpectedInstanceID: "missing"}}, appwire.TurnInterruptParams{Ref: "local:missing", ClientMutationID: "stop-1", ExpectedInstanceID: "missing"}},
		{"delegate", appwire.MethodEvenerDelegateStop, appwire.SessionStopRequest{Mode: "delegate", Delegate: &appwire.DelegateStopParams{Ref: "local:missing", DelegateID: "dlg_missing"}}, appwire.DelegateStopParams{Ref: "local:missing", DelegateID: "dlg_missing"}},
		{"owned_delegate", appwire.MethodEvenerJobsStop, appwire.SessionStopRequest{Mode: "owned_delegate", OwnedDelegate: &appwire.SessionOwnedDelegateStopParams{Ref: "local:missing", DelegateID: "dlg_missing"}}, appwire.SessionStopRequest{Mode: "owned_delegate", OwnedDelegate: &appwire.SessionOwnedDelegateStopParams{Ref: "local:missing", DelegateID: "dlg_missing"}}},
		{"job", appwire.MethodEvenerJobsStop, appwire.SessionStopRequest{Mode: "job", Job: &appwire.SessionJobStopParams{Ref: "local:missing", JobID: "job_missing"}}, appwire.SessionStopRequest{Mode: "job", Job: &appwire.SessionJobStopParams{Ref: "local:missing", JobID: "job_missing"}}},
		{"shutdown", appwire.MethodThreadShutdown, appwire.SessionStopRequest{Mode: "shutdown", Shutdown: &appwire.ThreadShutdownParams{Ref: "local:missing"}}, appwire.ThreadShutdownParams{Ref: "local:missing"}},
		{"force_stop", appwire.MethodEvenerThreadForceStop, appwire.SessionStopRequest{Mode: "force_stop", ForceStop: &appwire.ThreadForceStopParams{Ref: "local:missing"}}, appwire.ThreadForceStopParams{Ref: "local:missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newFixture := func() *WebServer {
				return NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), HubStateRoot: t.TempDir(), Archive: hubcore.NewArchiveStore(t.TempDir() + "/archive.db"), PinSections: hubcore.NewPinSectionStore(t.TempDir() + "/pins.db"), SessionSeen: hubcore.NewSessionSeenStore(t.TempDir() + "/seen.db")})
			}
			human, manager := newFixture(), newFixture()
			for _, w := range []*WebServer{human, manager} {
				if err := w.cfg.Archive.Set("", "session", "session-a", true, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			beforeHuman, beforeManager := controlStoresSnapshot(t, human), controlStoresSnapshot(t, manager)
			raw, err := json.Marshal(tc.argument)
			if err != nil {
				t.Fatal(err)
			}
			_, humanErr := human.appRPC.Router().Dispatch(t.Context(), appwire.Request{ID: appwire.NewIntID(1), Method: tc.method, Params: raw})
			owner := &sessionOperations{cfg: manager.cfg, sources: manager.sources}
			_, managerErr := owner.Stop(t.Context(), tc.request)
			if humanErr == nil || managerErr == nil {
				t.Fatalf("missing owner accepted stop: human=%v Manager=%v", humanErr, managerErr)
			}
			humanWire, managerWire := appserver.WireError(humanErr), appserver.WireError(managerErr)
			if humanWire.Code != managerWire.Code || humanWire.Message != managerWire.Message {
				t.Fatalf("human stop refusal=%v Manager stop refusal=%v", humanWire, managerWire)
			}
			if after := controlStoresSnapshot(t, human); !reflect.DeepEqual(beforeHuman, after) {
				t.Fatalf("human %s refused after changing persisted state", tc.name)
			}
			if after := controlStoresSnapshot(t, manager); !reflect.DeepEqual(beforeManager, after) {
				t.Fatalf("Manager %s refused after changing persisted state", tc.name)
			}
		})
	}
}
