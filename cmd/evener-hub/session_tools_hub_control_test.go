package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
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
		{"model", appwire.MethodThreadModelSet, appwire.SessionControlRequest{Action: "model", Model: &appwire.SessionControlModelParams{Ref: "local:missing", ModelProvider: "test", Model: "test"}}, nil, false},
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
					past := closeOnCleanup(t, hubcore.NewPastIndex(filepath.Join(root, "projects", "*")))
					if _, err := past.Rebuild(); err != nil {
						t.Fatal(err)
					}
					return NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries(), Archive: closeOnCleanup(t, hubcore.NewArchiveStore(filepath.Join(root, "archive.db"))), PinSections: closeOnCleanup(t, hubcore.NewPinSectionStore(filepath.Join(root, "pins.db"))), SessionSeen: closeOnCleanup(t, hubcore.NewSessionSeenStore(filepath.Join(root, "seen.db")))})
				}
				w := NewWebServer(testWebConfig(t, hubcore.WebConfig{Archive: closeOnCleanup(t, hubcore.NewArchiveStore(t.TempDir()+"/archive.db")), PinSections: closeOnCleanup(t, hubcore.NewPinSectionStore(t.TempDir()+"/pins.db")), SessionSeen: closeOnCleanup(t, hubcore.NewSessionSeenStore(t.TempDir()+"/seen.db"))}))
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
			_, humanErr := human.appRPC.Router().Dispatch(t.Context(), appwire.Request{ID: appwire.NewIntID(1), Method: tc.method, Params: raw})
			owner := &sessionOperations{cfg: manager.cfg, sources: manager.sources, navigation: manager.navigation, resolve: manager.resolveTopLevelSessionRef}
			_, managerErr := owner.Control(t.Context(), tc.request)
			if (humanErr == nil) != tc.accepted || (managerErr == nil) != tc.accepted {
				t.Fatalf("human=%v manager=%v accepted=%v", humanErr, managerErr, tc.accepted)
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

type managerModelControlSource struct {
	*scriptedAppSource
	readResponse appwire.ThreadReadResponse
	readErr      error
	setModel     func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error)
	readCalls    []appwire.ThreadReadParams
	modelCalls   []appwire.ThreadModelSetParams
}

func (s *managerModelControlSource) ReadThread(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.readCalls = append(s.readCalls, params)
	return s.readResponse, s.readErr
}

func (s *managerModelControlSource) SetThreadModel(_ context.Context, params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
	s.modelCalls = append(s.modelCalls, params)
	return s.setModel(params)
}

func managerModelReadyRead(ref string) appwire.ThreadReadResponse {
	snapshot := appwire.ModelControlSnapshot{
		Authority: appwire.ModelControlAuthority{InstanceID: "sess_1", BootGeneration: "boot-2"},
		Revision:  22,
		Active:    appwire.ModelControlActive{ModelProvider: "openai", Model: "gpt-5.5", ReasoningEffortLevels: []string{}},
	}
	return appwire.ThreadReadResponse{
		Thread: appwire.Thread{ID: "model-response", Evener: appwire.EvenerThread{
			Ref: ref, InstanceID: "sess_1",
			Capabilities: appwire.ThreadCapabilities{ChangeModel: true, ModelControlVersion: appwire.ModelControlVersion1},
			ModelControl: &snapshot,
		}},
		BootGeneration: "boot-2",
	}
}

func TestManagerSessionControlModelAuthorityAdapter(t *testing.T) {
	const ref = "peer:model-response"
	request := appwire.SessionControlRequest{
		Action: "model",
		Model:  &appwire.SessionControlModelParams{Ref: ref, ModelProvider: "openai", Model: "gpt-5.6-sol"},
	}
	newOwner := func(t *testing.T, read appwire.ThreadReadResponse, readErr error, setModel func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error)) (*sessionOperations, *managerModelControlSource) {
		t.Helper()
		source := &managerModelControlSource{
			scriptedAppSource: &scriptedAppSource{id: "peer", thread: read.Thread},
			readResponse:      read,
			readErr:           readErr,
			setModel:          setModel,
		}
		web := NewWebServer(testWebConfig(t, hubcore.WebConfig{}))
		web.sources.Add(source)
		return &sessionOperations{cfg: web.cfg, sources: web.sources, navigation: web.navigation, resolve: web.resolveTopLevelSessionRef}, source
	}
	assertLeanReads := func(t *testing.T, source *managerModelControlSource, want int) {
		t.Helper()
		if len(source.readCalls) != want {
			t.Fatalf("read calls = %d, want %d", len(source.readCalls), want)
		}
		for _, params := range source.readCalls {
			if params.Ref != ref || params.IncludeTurns || params.Subscribe {
				t.Fatalf("Manager authority read = %+v", params)
			}
		}
	}

	t.Run("ready read generates exact authority and returns typed response", func(t *testing.T) {
		want := modelControlResponseForHubTest()
		owner, source := newOwner(t, managerModelReadyRead(ref), nil, func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
			want.ModelControl.Desired.ClientMutationID = params.ClientMutationID
			return want, nil
		})
		got, err := owner.Control(t.Context(), request)
		if err != nil {
			t.Fatalf("Control(model): %v", err)
		}
		assertLeanReads(t, source, 1)
		if len(source.modelCalls) != 1 {
			t.Fatalf("model calls = %d, want 1", len(source.modelCalls))
		}
		params := source.modelCalls[0]
		if params.Ref != ref || params.ModelProvider != "openai" || params.Model != "gpt-5.6-sol" || params.ClientMutationID == "" || params.ExpectedInstanceID != "sess_1" || params.ExpectedBootGeneration != "boot-2" {
			t.Fatalf("generated model mutation = %+v", params)
		}
		if got.Model == nil || !reflect.DeepEqual(*got.Model, want) {
			t.Fatalf("Manager model response = %+v, want %+v", got.Model, want)
		}
	})

	invalidReads := []struct {
		name    string
		mutate  func(*appwire.ThreadReadResponse)
		readErr error
	}{
		{name: "absent version", mutate: func(read *appwire.ThreadReadResponse) { read.Thread.Evener.Capabilities.ModelControlVersion = 0 }},
		{name: "unknown version", mutate: func(read *appwire.ThreadReadResponse) { read.Thread.Evener.Capabilities.ModelControlVersion = 2 }},
		{name: "missing snapshot", mutate: func(read *appwire.ThreadReadResponse) { read.Thread.Evener.ModelControl = nil }},
		{name: "missing reasoning levels", mutate: func(read *appwire.ThreadReadResponse) {
			read.Thread.Evener.ModelControl.Active.ReasoningEffortLevels = nil
		}},
		{name: "mismatched instance", mutate: func(read *appwire.ThreadReadResponse) { read.Thread.Evener.ModelControl.Authority.InstanceID = "other" }},
		{name: "mismatched boot", mutate: func(read *appwire.ThreadReadResponse) {
			read.Thread.Evener.ModelControl.Authority.BootGeneration = "other"
		}},
		{name: "read refusal", readErr: appwire.InvalidParams("read refused")},
		{name: "read cancellation", readErr: context.Canceled},
		{name: "read transport failure", readErr: errors.New("read transport failed")},
	}
	for _, test := range invalidReads {
		t.Run(test.name, func(t *testing.T) {
			read := managerModelReadyRead(ref)
			if test.mutate != nil {
				test.mutate(&read)
			}
			owner, source := newOwner(t, read, test.readErr, func(appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
				t.Fatal("non-ready read dispatched a model mutation")
				return appwire.ThreadModelSetResponse{}, nil
			})
			if _, err := owner.Control(t.Context(), request); err == nil {
				t.Fatal("non-ready authority read accepted")
			}
			assertLeanReads(t, source, 1)
			if len(source.modelCalls) != 0 {
				t.Fatalf("model calls = %d, want 0", len(source.modelCalls))
			}
		})
	}

	for _, test := range []struct {
		name string
		err  func(string) error
	}{
		{name: "stale authority is diagnostic only", err: func(id string) error { return appwire.ModelControlAuthorityStale(id, "stale") }},
		{name: "lost response is diagnostic only", err: func(id string) error { return appwire.MutationOutcomeUnknownBlocked(id, "lost") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := managerModelReadyRead(ref)
			var source *managerModelControlSource
			owner, source := newOwner(t, read, nil, func(params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
				source.readResponse.Thread.Evener.ModelControl.Revision++
				source.readResponse.Thread.Evener.ModelControl.Active = appwire.ModelControlActive{ModelProvider: "anthropic", Model: "claude-opus-4-6"}
				return appwire.ThreadModelSetResponse{}, test.err(params.ClientMutationID)
			})
			// The diagnostic read observes a newer canonical pick but may not turn it
			// into a replay or a synthetic success for this Manager request.
			_, err := owner.Control(t.Context(), request)
			if err == nil {
				t.Fatal("uncertain model mutation reported success")
			}
			assertLeanReads(t, source, 2)
			if len(source.modelCalls) != 1 {
				t.Fatalf("model calls = %d, want 1", len(source.modelCalls))
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("mutation error = %T %v, want WireError", err, err)
			}
			data, ok := appwire.ErrorDataOf(wire.Data)
			if !ok || data.ClientMutationID != source.modelCalls[0].ClientMutationID {
				t.Fatalf("mutation identity changed: data=%+v params=%+v", data, source.modelCalls[0])
			}
			// The diagnostic read already observed which model the daemon runs.
			// Discarding it makes the caller issue a second read for a fact this
			// path held, so the failure carries the observed state while keeping
			// the original outcome and blocking automatic replay.
			observed, ok := appwire.ModelControlObservedOf(err)
			if !ok {
				t.Fatalf("failure carries no observed model-control state: %v", err)
			}
			if observed.Active.ModelProvider != "anthropic" || observed.Active.Model != "claude-opus-4-6" {
				t.Fatalf("observed state = %+v, want the model the diagnostic read saw", observed)
			}
			// The evidence is additive: every field the original refusal carried
			// must read exactly as it did, including its outcome and whether a
			// replay is allowed, because a caller decides those from this data.
			var originalWire appwire.WireError
			if !errors.As(test.err(source.modelCalls[0].ClientMutationID), &originalWire) {
				t.Fatal("test refusal is not a wire error")
			}
			original, _ := appwire.ErrorDataOf(originalWire.Data)
			if data != original {
				t.Fatalf("observed evidence changed the refusal: got %+v, want %+v", data, original)
			}
		})
	}
}

func TestManagerSessionControlModelResumesPastThread(t *testing.T) {
	var sessionID string
	readCalls := 0
	modelCalls := 0
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			readCalls++
			if params.IncludeTurns || params.Subscribe {
				t.Fatalf("cold Manager authority read = %+v", params)
			}
			snapshot := appwire.ModelControlSnapshot{
				Authority: appwire.ModelControlAuthority{InstanceID: sessionID, BootGeneration: "boot-manager"},
				Revision:  1,
				Active:    appwire.ModelControlActive{ModelProvider: "openai", Model: "gpt-5.5", ReasoningEffortLevels: []string{}},
			}
			return appwire.ThreadReadResponse{
				Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{
					Ref: params.Ref, InstanceID: sessionID,
					Capabilities: appwire.ThreadCapabilities{ChangeModel: true, ModelControlVersion: appwire.ModelControlVersion1},
					ModelControl: &snapshot,
				}},
				BootGeneration: "boot-manager",
			}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
			modelCalls++
			if params.ClientMutationID == "" || params.ExpectedInstanceID != sessionID || params.ExpectedBootGeneration != "boot-manager" {
				t.Fatalf("resumed Manager mutation = %+v", params)
			}
			snapshot := appwire.ModelControlSnapshot{
				Authority: appwire.ModelControlAuthority{InstanceID: sessionID, BootGeneration: "boot-manager"},
				Revision:  2,
				Active:    appwire.ModelControlActive{ModelProvider: params.ModelProvider, Model: params.Model, ReasoningEffortLevels: []string{}},
				Desired: &appwire.ModelControlDesired{
					RequestSequence: 1, ClientMutationID: params.ClientMutationID,
					ModelProvider: params.ModelProvider, Model: params.Model,
					Lifecycle: appwire.ModelControlLifecycleApplied,
				},
			}
			return appwire.ThreadModelSetResponse{
				ModelControlVersion: appwire.ModelControlVersion1,
				Disposition:         appwire.ModelControlDispositionAccepted,
				ModelControl:        &snapshot,
			}, nil
		})
	})
	sessionID = sid
	server, web := newHubRPCTestServerWithWeb(t, cfg)
	defer server.Close()
	owner := &sessionOperations{cfg: web.cfg, sources: web.sources, navigation: web.navigation, resolve: web.resolveTopLevelSessionRef}

	response, err := owner.Control(t.Context(), appwire.SessionControlRequest{
		Action: "model",
		Model: &appwire.SessionControlModelParams{
			Ref: "local:" + sessionID, ModelProvider: "openai", Model: "gpt-5.6-sol",
		},
	})
	if err != nil {
		t.Fatalf("cold Manager model control: %v", err)
	}
	// Auto-resume reads the replacement thread once to establish the live
	// source. The adapter then performs its one post-resume authority read.
	if *resumeCalls != 1 || readCalls != 2 || modelCalls != 1 {
		t.Fatalf("resume=%d reads=%d model sends=%d, want 1/2/1", *resumeCalls, readCalls, modelCalls)
	}
	if response.Model == nil || response.Model.ModelControl == nil || response.Model.ModelControl.Active.Model != "gpt-5.6-sol" {
		t.Fatalf("cold Manager response = %+v", response.Model)
	}
}

// TestManagerSessionControlModelColdResumeAttachesRelay pins the medium review
// finding: the authority read is what resumes a cold session, so the relay that
// carries the resumed daemon's control, status and model frames has to be
// attached from that resume. Without it the switch applies while every
// subscribed pane keeps rendering the exited session's model.
func TestManagerSessionControlModelColdResumeAttachesRelay(t *testing.T) {
	var sessionID string
	readCalls := 0
	cfg, sid, resumeCalls := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			readCalls++
			snapshot := appwire.ModelControlSnapshot{
				Authority: appwire.ModelControlAuthority{InstanceID: sessionID, BootGeneration: "boot-manager"},
				Revision:  1,
				Active:    appwire.ModelControlActive{ModelProvider: "openai", Model: "gpt-5.5", ReasoningEffortLevels: []string{}},
			}
			return appwire.ThreadReadResponse{
				Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{
					Ref: params.Ref, InstanceID: sessionID,
					Capabilities: appwire.ThreadCapabilities{ChangeModel: true, ModelControlVersion: appwire.ModelControlVersion1},
					ModelControl: &snapshot,
				}},
				BootGeneration: "boot-manager",
			}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
			snapshot := appwire.ModelControlSnapshot{
				Authority: appwire.ModelControlAuthority{InstanceID: sessionID, BootGeneration: "boot-manager"},
				Revision:  2,
				Active:    appwire.ModelControlActive{ModelProvider: params.ModelProvider, Model: params.Model, ReasoningEffortLevels: []string{}},
			}
			return appwire.ThreadModelSetResponse{
				ModelControlVersion: appwire.ModelControlVersion1,
				Disposition:         appwire.ModelControlDispositionAccepted,
				ModelControl:        &snapshot,
			}, nil
		})
	})
	sessionID = sid

	// The relay's own read is the attach: prepareRelay consults the seam before
	// every subscription read, so a Manager switch that leaves a relay listening
	// for the resumed daemon shows up here.
	relayReads := 0
	cfg.RelayHooks.BeforeReadThread = func() { relayReads++ }
	server, web := newHubRPCTestServerWithWeb(t, cfg)
	defer server.Close()
	owner := &sessionOperations{
		cfg: web.cfg, sources: web.sources, navigation: web.navigation,
		resolve: web.resolveTopLevelSessionRef,
		relays:  newHubRelay(web.appRPC, web.cfg, web.sources),
	}

	_, err := owner.Control(t.Context(), appwire.SessionControlRequest{
		Action: "model",
		Model:  &appwire.SessionControlModelParams{Ref: "local:" + sessionID, ModelProvider: "openai", Model: "gpt-5.6-sol"},
	})
	if err != nil {
		t.Fatalf("cold Manager model control: %v", err)
	}
	if *resumeCalls != 1 {
		t.Fatalf("resume calls = %d, want exactly one", *resumeCalls)
	}
	if relayReads == 0 {
		t.Fatalf("no relay was attached for the resumed daemon (reads=%d), so a subscribed pane would keep the exited session's model", readCalls)
	}
}

func TestManagerSessionControlModelResumesRemoteSavedThread(t *testing.T) {
	const (
		ref       = "remote:worker"
		remoteRef = "local:worker"
	)
	resumed := false
	readCalls := 0
	resumeCalls := 0
	modelCalls := 0
	readySnapshot := appwire.ModelControlSnapshot{
		Authority: appwire.ModelControlAuthority{InstanceID: "worker-instance", BootGeneration: "worker-boot"},
		Revision:  3,
		Active: appwire.ModelControlActive{
			ModelProvider: "openai", Model: "gpt-5.5", ReasoningEffortLevels: []string{},
		},
	}
	client, _, _ := newScriptedAdminClient(t, func(method string, raw json.RawMessage) hostAdminReply {
		switch method {
		case appwire.MethodThreadRead:
			readCalls++
			if !resumed {
				return hostAdminReply{result: appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: "worker", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
					Evener: appwire.EvenerThread{Ref: remoteRef},
				}}}
			}
			return hostAdminReply{result: appwire.ThreadReadResponse{
				Thread: appwire.Thread{ID: "worker", Evener: appwire.EvenerThread{
					Ref: remoteRef, InstanceID: "worker-instance",
					Capabilities: appwire.ThreadCapabilities{ChangeModel: true, ModelControlVersion: appwire.ModelControlVersion1},
					ModelControl: &readySnapshot,
				}},
				BootGeneration: "worker-boot",
			}}
		case appwire.MethodThreadResume:
			resumeCalls++
			resumed = true
			return hostAdminReply{result: appwire.ThreadResumeResponse{Thread: appwire.Thread{ID: "worker", Evener: appwire.EvenerThread{Ref: remoteRef}}}}
		case appwire.MethodThreadModelSet:
			modelCalls++
			var params appwire.ThreadModelSetParams
			if err := json.Unmarshal(raw, &params); err != nil {
				t.Fatalf("decode remote model mutation: %v", err)
			}
			if params.ExpectedInstanceID != readySnapshot.Authority.InstanceID || params.ExpectedBootGeneration != readySnapshot.Authority.BootGeneration {
				t.Fatalf("remote Manager mutation authority = %+v", params)
			}
			accepted := readySnapshot
			accepted.Revision++
			accepted.Active = appwire.ModelControlActive{ModelProvider: params.ModelProvider, Model: params.Model, ReasoningEffortLevels: []string{}}
			return hostAdminReply{result: appwire.ThreadModelSetResponse{
				ModelControlVersion: appwire.ModelControlVersion1,
				Disposition:         appwire.ModelControlDispositionAccepted,
				ModelControl:        &accepted,
			}}
		default:
			t.Fatalf("unexpected remote Manager method %q", method)
			return hostAdminReply{}
		}
	})

	sources := appsource.NewRegistry()
	sources.Add(appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	}))
	owner := &sessionOperations{cfg: hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}, sources: sources}
	response, err := owner.Control(t.Context(), appwire.SessionControlRequest{
		Action: "model",
		Model:  &appwire.SessionControlModelParams{Ref: ref, ModelProvider: "openai", Model: "gpt-5.6-sol"},
	})
	if err != nil {
		t.Fatalf("remote saved Manager model control: %v", err)
	}
	if readCalls != 2 || resumeCalls != 1 || modelCalls != 1 {
		t.Fatalf("remote reads=%d resumes=%d model sends=%d, want 2/1/1", readCalls, resumeCalls, modelCalls)
	}
	if response.Model == nil || response.Model.ModelControl == nil || response.Model.ModelControl.Active.Model != "gpt-5.6-sol" {
		t.Fatalf("remote Manager response = %+v", response.Model)
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
	w := NewWebServer(testWebConfig(t, hubcore.WebConfig{HubID: "hub-test", Archive: closeOnCleanup(t, hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "archive.db"))), PinSections: closeOnCleanup(t, hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))), SessionSeen: closeOnCleanup(t, hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "seen.db")))}))
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
				return NewWebServer(testWebConfig(t, hubcore.WebConfig{Archive: closeOnCleanup(t, hubcore.NewArchiveStore(t.TempDir()+"/archive.db")), PinSections: closeOnCleanup(t, hubcore.NewPinSectionStore(t.TempDir()+"/pins.db")), SessionSeen: closeOnCleanup(t, hubcore.NewSessionSeenStore(t.TempDir()+"/seen.db"))}))
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
