package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestSessionAwaitingStringIsWireAwaiting(t *testing.T) {
	t.Parallel()
	// The string is load-bearing: SessionProcessing is "active", and every
	// status switch on the wire journey defaults unknown strings to idle.
	if got := string(SessionAwaiting); got != "awaiting" {
		t.Fatalf("SessionAwaiting = %q, want %q", got, "awaiting")
	}
}

// TestMeta_CreatedAtStableAcrossCalls pins the WS2 A2 fix: Meta().CreatedAt
// must reflect the session's true creation time and stay stable across
// repeated calls (i.e. across autosaves), not get re-stamped to "now" every
// time Meta() is called. UpdatedAt, by contrast, is expected to keep tracking
// the clock.
func TestMeta_CreatedAtStableAcrossCalls(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}))

	first := sess.Meta()

	clk.Advance(time.Hour)

	second := sess.Meta()

	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed across Meta() calls: first=%v second=%v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt did not advance: first=%v second=%v", first.UpdatedAt, second.UpdatedAt)
	}
}

// TestRestoreSeedsMetricsIntoMeta pins the WS2 A3 K2-seeding-half fix: restoring
// a session from a SessionMeta carrying persisted WorkMillis/CumulativeUsage
// must seed those totals into the live session so Meta(), read immediately
// after restore (before any autosave has a chance to run), reflects the
// persisted values rather than a freshly constructed session's zeroes.
func TestRestoreSeedsMetricsIntoMeta(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	meta := schema.SessionMeta{
		ID:         "01RESTOREMETRICSSEED0001",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		CreatedAt:  time.Now().UTC(),
		WorkMillis: 5000,
		CumulativeUsage: schema.CumulativeUsage{
			InputTokens:     100,
			OutputTokens:    200,
			CacheReadTokens: 50,
			TotalTokens:     300,
		},
	}

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), env, meta, "")
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	got := sess.Meta()
	if got.WorkMillis != 5000 {
		t.Fatalf("Meta().WorkMillis = %d, want 5000", got.WorkMillis)
	}
	if got.CumulativeUsage != meta.CumulativeUsage {
		t.Fatalf("Meta().CumulativeUsage = %+v, want %+v", got.CumulativeUsage, meta.CumulativeUsage)
	}
	resume := func(t *testing.T, s *Session) *Session {
		t.Helper()
		s.Close()
		meta, err := schema.LoadSessionMeta(s.StateDir(), s.ID())
		if err != nil {
			t.Fatal(err)
		}
		restored, err := RestoreSessionFromMetaWithConfig(s.Client(), s.Profile(), s.currentEnv(), meta,
			RestoreSessionConfig{StateDir: s.StateDir(), testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true})})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restored.Close)
		return restored
	}

	t.Run("private API result is only measured live", func(t *testing.T) {
		const private = "q81-private-body-36"
		state := newBucket(t)
		attempt := testAPIAttemptRecord("ag_private", 1, []byte(strings.Repeat(private, 300)), nil)
		args, err := json.Marshal(map[string]string{"source": "api_log", "attempt_id": attempt.AttemptID, "body": "request"})
		if err != nil {
			t.Fatal(err)
		}
		read := llm.ToolCallData{ID: "read-36", Name: "read_session_transcript", Arguments: args}
		client := llm.NewClient()
		client.Register(newFakeAdapter("openai", fixedStep(agenttest.ToolCallResponse(read)), func(req llm.Request) llm.Response {
			if !messagesContainToolResultText(req.Messages, private) {
				t.Error("live provider request lost the private API result")
			}
			answer := agenttest.FinalResponse("answer-42")
			answer.Usage = llm.Usage{InputTokens: 90_000}
			return answer
		}, func(req llm.Request) llm.Response {
			if messagesContainToolResultText(req.Messages, private) || !messagesContainToolResultText(req.Messages, attempt.AttemptID) {
				t.Error("resumed provider request must carry only the reread handle for the private result")
			}
			answer := agenttest.FinalResponse("answer-67")
			answer.Usage = llm.Usage{InputTokens: 1_200}
			return answer
		}))
		s := newSession(t, withClient(client), withStateDir(state))
		deps := &toolDeps{stateDir: state, sessionID: s.ID()}
		s.registerTool(read.Name, "Read the fixture's API evidence", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
			return execReadSessionTranscript(ctx, deps, args.(map[string]any))
		})
		writeTestAPILog(t, state, s.ID(), attempt)
		ctx := tripwireCtx(t)
		if _, err := s.ProcessInput(ctx, "input-36", nil); err != nil {
			t.Fatal(err)
		}
		if used := s.contextMgr.LastInputTokens(); used != 90_000 {
			t.Fatalf("live measured input = %d, want 90000", used)
		}
		durable, ok := findToolResultInEntries(readPersistedTranscriptEntries(t, state, s.ID()), read.ID)
		body, _ := durable.Content.(string)
		var handle apiLogTranscriptPlaceholder
		if !ok || strings.Contains(body, private) || json.Unmarshal([]byte(body), &handle) != nil || !handle.PrivateEvidenceOmitted || handle.ReRead.AttemptID != attempt.AttemptID {
			t.Fatal("semantic transcript must persist only the matching API-log reread handle")
		}
		s = resume(t, s)
		if used := s.contextMgr.LastInputTokens(); used != 0 {
			t.Errorf("restored input measurement for the private result = %d, want a fresh estimate", used)
		}
		if _, err := s.ProcessInput(ctx, "input-97", nil); err != nil {
			t.Fatal(err)
		}
		if used := resume(t, s).contextMgr.LastInputTokens(); used != 1_200 {
			t.Fatalf("restored input measurement for the reread handle = %d, want 1200", used)
		}
	})

	for _, tc := range []struct {
		name     string
		recorded bool
		want     int
	}{
		{"buffered tool result lost before recording", false, 0},
		{"buffered tool result retained after sync failure", true, 90_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Repeat("q75-result-27", 450)
			call := llm.ToolCallData{ID: "call-75", Name: "result_probe", Arguments: json.RawMessage(`{}`)}
			s := newSession(t, withStateDir(t.TempDir()), withSteps(fixedStep(agenttest.ToolCallResponse(call)), func(req llm.Request) llm.Response {
				if !messagesContainToolResultText(req.Messages, body) {
					t.Error("live provider request lost the successful tool result")
				}
				answer := agenttest.FinalResponse("answer-63")
				answer.Usage = llm.Usage{InputTokens: 90_000}
				return answer
			}, func(req llm.Request) llm.Response {
				result := findRequestToolResult(req.Messages, call.ID)
				if result == nil || result.IsError == tc.recorded || messagesContainToolResultText(req.Messages, body) != tc.recorded {
					t.Error("resumed provider request must carry the recorded result or its orphan repair")
				}
				answer := agenttest.FinalResponse("answer-47")
				answer.Usage = llm.Usage{InputTokens: 95_000}
				return answer
			}))
			fs := attachEnvironmentFailureFS(t, s)
			s.attachedTranscript().SyncInterval = 0
			s.registerTool(call.Name, "Return the measured result", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
				fs.mu.Lock()
				defer fs.mu.Unlock()
				if tc.recorded {
					fs.failure = errors.New("sync fault")
				} else {
					fs.writeFailure = errors.New("write fault")
				}
				return body, nil
			})
			ctx := tripwireCtx(t)
			if _, err := s.ProcessInput(ctx, "input-75", nil); err != nil {
				t.Fatal(err)
			}
			if _, ok := findToolResultInEntries(readPersistedTranscriptEntries(t, s.StateDir(), s.ID()), call.ID); ok != tc.recorded {
				t.Fatalf("tool result recorded = %t, want %t", ok, tc.recorded)
			}
			if got := s.contextMgr.LastInputTokens(); got != 90_000 {
				t.Fatalf("live input measurement = %d, want 90000", got)
			}
			s = resume(t, s)
			if got := s.contextMgr.LastInputTokens(); got != tc.want {
				t.Errorf("restored input measurement = %d, want %d", got, tc.want)
			}
			if _, err := s.ProcessInput(ctx, "input-47", nil); err != nil {
				t.Fatal(err)
			}
			if got := resume(t, s).contextMgr.LastInputTokens(); got != 95_000 {
				t.Fatalf("new measurement after resume = %d, want 95000", got)
			}
		})
	}

	t.Run("input measurement follows the answering target", func(t *testing.T) {
		const original = "https://measure-a.example.test/v1"
		const replacement = "https://measure-b.example.test/v1"
		const private = "private-38"
		appendPrivate := func(s *Session) {
			s.appendTurnWithTranscriptMessage(schema.TurnToolResults,
				llm.ToolResultNamed("read-38", "read_session_transcript", private, false),
				llm.ToolResultNamed("read-38", "read_session_transcript", "handle-38", false))
		}
		clientFor := func(wire, endpoint string, replay bool, steps ...func(llm.Request) llm.Response) *llm.Client {
			inst := testRegistryInstances()["gw"]
			inst.Transport.BaseURL = endpoint
			inst.Models = map[string]registry.Model{"reasoner": {
				WireID: wire, Protocol: registry.ProtocolOpenAIResponses,
				Caps: registry.Caps{ReplayReasoningText: &replay, ContextWindow: new(500_000)},
			}}
			return registryClient(t, map[string]registry.Provider{"gw": inst}, newFakeAdapter("gw", steps...))
		}
		for _, tc := range []struct {
			name, wire, endpoint string
			before, after        bool
			want                 int
		}{
			{"same", "wire-a", original, true, true, 90_000},
			{"private result appended after dispatch", "wire-a", original, true, true, 90_000},
			{"measured after committed fold", "wire-a", original, true, true, 90_000},
			{"measured after repaired tool result", "wire-a", original, true, true, 90_000},
			{"compacted during dispatch", "wire-a", original, true, true, 0},
			{"unknown measurement owner", "wire-a", original, true, true, 0},
			{"unknown measured prefix", "wire-a", original, true, true, 0},
			{"replay disabled", "wire-a", original, true, false, 0},
			{"redirected model", "wire-b", original, true, true, 0},
			{"same endpoint without replay", "wire-a", original, false, false, 90_000},
			{"redirected endpoint without replay", "wire-a", replacement, false, false, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				state := t.TempDir()
				report := strings.Repeat("q83x", 4_000)
				dispatched, release := make(chan struct{}), make(chan struct{})
				client := clientFor("wire-a", original, tc.before, func(req llm.Request) llm.Response {
					if !requestCarriesText(req, "measured-41") || requestCarriesText(req, report) {
						t.Error("the measured request must contain the input that preceded the report")
					}
					if messagesContainToolResultText(req.Messages, private) {
						t.Error("measured request contains a private result outside its expected prefix")
					}
					close(dispatched)
					awaitSignal(t, release, "history change before the provider answer")
					resp := agenttest.FinalResponse("answer-59")
					resp.Usage = llm.Usage{InputTokens: 90_000, OutputTokens: 10, TotalTokens: 90_010}
					return resp
				})
				s := newSession(t, withClient(client), withStateDir(state), withProfile(resolveClientProfile(t, client, "gw/reasoner")))
				ctx := tripwireCtx(t)
				fold := tc.name == "compacted during dispatch"
				if tc.name == "measured after committed fold" {
					appendPrivate(s)
				}
				if fold || tc.name == "measured after committed fold" {
					for range 12 {
						s.appendTurn(schema.TurnUserInput, llm.User("old-15"))
						s.appendTurn(schema.TurnAssistant, llm.Assistant("earlier-34"))
					}
				}
				if tc.name == "measured after committed fold" {
					if err := s.Compact(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if tc.name == "measured after repaired tool result" {
					s.appendTurn(schema.TurnAssistant, agenttest.ToolCallResponse(communicateCall("orphan-31", "result")).Message)
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					if _, err := s.ProcessInput(ctx, "measured-41", nil); err != nil {
						t.Errorf("process measured input: %v", err)
					}
				}()
				waitForTestSignal(t, dispatched, "provider dispatch")
				s.mu.RLock()
				measured := len(s.history)
				s.mu.RUnlock()
				if tc.name == "private result appended after dispatch" {
					appendPrivate(s)
				}
				if fold {
					if err := s.Compact(ctx); err != nil {
						t.Errorf("compact dispatched history: %v", err)
					}
				}
				if ok, err := s.appendDelegateNotificationDurably("report-83", report); err != nil || !ok {
					t.Errorf("append concurrent report = %v, %v", ok, err)
				}
				close(release)
				waitForTestSignal(t, done, "provider settlement")
				s.Close()
				meta, err := schema.LoadSessionMeta(state, s.id)
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "unknown measurement owner" {
					meta.LastInputTarget = ""
				}
				if tc.name == "unknown measured prefix" {
					meta.LastInputEntryCount = 0
				}
				client = clientFor(tc.wire, tc.endpoint, tc.after)
				restored, err := RestoreSessionFromMetaWithConfig(client, resolveClientProfile(t, client, "gw/reasoner"),
					execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
						StateDir: state, testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true}),
					})
				if err != nil {
					t.Fatal(err)
				}
				defer restored.Close()
				saved := restored.Meta()
				if saved.CumulativeUsage.InputTokens != 90_000 {
					t.Errorf("accounted provider input = %d, want 90000", saved.CumulativeUsage.InputTokens)
				}
				if saved.LastInputTokens != tc.want {
					t.Fatalf("restored measurement = %d, want %d", saved.LastInputTokens, tc.want)
				}
				if tc.want != 0 && saved.LastInputHistoryLength != measured {
					t.Fatalf("measured prefix = %d, want %d", saved.LastInputHistoryLength, measured)
				}
				used := restored.contextMgr.EstimateUsage(restored.history, 100).Used
				if used < tc.want+len(report)/4 || (tc.want == 0 && used >= 90_000) {
					t.Fatalf("restored pressure = %d, want the unsent report above baseline %d", used, tc.want)
				}
			})
		}

		t.Run("answer settles while forced fold awaits publication", func(t *testing.T) {
			state := t.TempDir()
			answer := agenttest.FinalResponse("answer-12")
			answer.Usage = llm.Usage{InputTokens: 90_000}
			client := clientFor("wire-a", original, true, fixedStep(answer))
			profile := resolveClientProfile(t, client, "gw/reasoner")
			s := newSession(t, withClient(client), withStateDir(state), withProfile(profile))
			if _, err := s.appendDelegateNotificationDurably("retained-63", "prior report"); err != nil {
				t.Fatal(err)
			}
			s.appendTurn(schema.TurnUserInput, llm.User("measured-12"))
			ctx := tripwireCtx(t)
			_, _, _, req, full, effort, err := s.prepareModelRequestWithError(ctx, 0, new(events.RoundTimings))
			if err != nil {
				t.Fatal(err)
			}
			resp, req, _, target, err := s.callModelWithFallback(ctx, profile, req, full, effort, 0)
			if err != nil {
				t.Fatal(err)
			}
			// These unmeasured exchanges give /compact an old prefix to fold
			// and leave enough turns for an obsolete measured length to fit.
			for range 12 {
				s.appendTurn(schema.TurnUserInput, llm.User("later-27"))
				s.appendTurn(schema.TurnAssistant, llm.Assistant("later-83"))
			}
			publishing, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			pause := sync.OnceFunc(func() {
				close(publishing)
				awaitSignal(t, release, "answer measurement before fold publication")
			})
			// Publication reads the pending report after the fold finishes its
			// token resets, before it swaps the old resident prefix.
			updateSessionTestConfig(s, func(cfg *testConfig) {
				cfg.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
					pause()
					return s.attentionFoldCursor.read(path, sessionID)
				}
			})
			go func() {
				defer close(done)
				if err := s.Compact(ctx); err != nil {
					t.Errorf("publish forced fold: %v", err)
				}
			}()
			waitForTestSignal(t, publishing, "computed fold waiting to publish")
			s.recordResponseUsage(resp, req, target)
			tokens, measured, _ := s.contextMgr.InputMeasurement()
			if tokens != 90_000 || measured == 0 {
				t.Errorf("measurement before publication = %d covering %d turns", tokens, measured)
			}
			if err := s.saveMeta(); err != nil {
				t.Error(err)
			}
			beforeFold, err := schema.LoadSessionMeta(state, s.id)
			if err != nil || beforeFold.LastInputTokens != 90_000 {
				t.Errorf("pre-fold snapshot measured %d tokens, load error %v", beforeFold.LastInputTokens, err)
			}
			close(release)
			waitForTestSignal(t, done, "fold publication")
			s.Close()
			meta, err := schema.LoadSessionMeta(state, s.id)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name string
				meta schema.SessionMeta
			}{{"committed metadata", meta}, {"process exited before metadata save", beforeFold}} {
				t.Run(tc.name, func(t *testing.T) {
					restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), tc.meta, RestoreSessionConfig{
						StateDir: state, testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true}),
					})
					if err != nil {
						t.Fatal(err)
					}
					defer restored.Close()
					if len(restored.history) == 0 || len(restored.history) < measured || (restored.history[0].Kind != schema.TurnSummary && restored.history[0].Kind != schema.TurnCheckpoint) {
						t.Fatal("the restored fold must replace the measured prefix while retaining at least as many turns")
					}
					if got := restored.Meta().LastInputTokens; got != 0 {
						t.Errorf("restored the old prefix's %d tokens on the new fold", got)
					}
				})
			}
		})
	})
}

// TestActiveTurnStartedAtMillis_MidTurnVsIdle pins the WS2 A7 accessor: an idle
// session reports 0, while a session mid-turn (SessionProcessing with a
// stamped turnStartedAt) reports that instant as a Unix timestamp in
// milliseconds — the wire contract for EvenerThread.ActiveTurnStartedAt, which
// the web reducer reads as epoch-ms. Directly sets the unexported
// state/turnStartedAt fields to simulate mid-turn without driving a real model
// call, mirroring TestWorkMillis_CloseMidTurnCounts' established idiom in
// session_workmillis_test.go.
func TestActiveTurnStartedAtMillis_MidTurnVsIdle(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}))

	if got := sess.ActiveTurnStartedAtMillis(); got != 0 {
		t.Fatalf("idle ActiveTurnStartedAtMillis() = %d, want 0", got)
	}

	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.turnStartedAt = clk.Now()
	sess.mu.Unlock()

	if want, got := clk.Now().UnixMilli(), sess.ActiveTurnStartedAtMillis(); got != want {
		t.Fatalf("mid-turn ActiveTurnStartedAtMillis() = %d, want %d", got, want)
	}
}

// TestWorkMillisSnapshot_MatchesAccumulatedWork pins the WS2 A7 accessor:
// WorkMillisSnapshot reads the same accumulated total Meta().WorkMillis
// reports, without requiring a full Meta() call.
func TestWorkMillisSnapshot_MatchesAccumulatedWork(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	sess.mu.Lock()
	sess.workMillis = 4200
	sess.mu.Unlock()

	if got := sess.WorkMillisSnapshot(); got != 4200 {
		t.Fatalf("WorkMillisSnapshot() = %d, want 4200", got)
	}
}

// TestCumulativeUsageSnapshot_MatchesContextManagerTotal pins the WS2 A7
// accessor: CumulativeUsageSnapshot mirrors the context manager's running
// total directly (the same source Meta().CumulativeUsage derives from).
func TestCumulativeUsageSnapshot_MatchesContextManagerTotal(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	want := llm.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
	sess.contextMgr.SetCumulativeUsage(want)

	got := sess.CumulativeUsageSnapshot()
	if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.TotalTokens != want.TotalTokens {
		t.Fatalf("CumulativeUsageSnapshot() = %+v, want %+v", got, want)
	}
}

// TestResumedSubagentMetaSurvivesAutosave pins that a bare `serve --resume
// <delegate-id>` keeps the whole persisted lineage pair — is_subagent AND
// parent_session_id. That restore leaves the spawn carrier empty (spawn is
// json:"-", never persisted), so a Meta() deriving either field from cfg.spawn
// alone rewrites the delegate as a parentless root on the next autosave. The
// pair has to hold together: the hub's tree hides is_subagent rows from the
// top level and nests them under their parent, so a delegate saved with the
// flag but no parent id disappears from both views.
func TestResumedSubagentMetaSurvivesAutosave(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	const parentID = "spawning-root"
	meta := schema.SessionMeta{
		ID:              "resumed-delegate",
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		IsSubagent:      true,
		ParentSessionID: parentID,
		Config:          (SessionConfig{}).toSnapshot(),
	}
	// restoreCfg.spawn intentionally left zero: the bare-resume case.
	sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer sess.Close()

	if sess.cfg.spawn.parentSessionID != "" {
		t.Fatalf("test setup: spawn carrier not empty (%q) — the case under test requires it empty", sess.cfg.spawn.parentSessionID)
	}
	live := sess.Meta()
	if !live.IsSubagent {
		t.Error("Meta().IsSubagent = false right after resuming a delegate, want true")
	}
	if live.ParentSessionID != parentID {
		t.Errorf("Meta().ParentSessionID = %q right after resuming a delegate, want %q", live.ParentSessionID, parentID)
	}

	sess.maybeAutoSave()
	saved, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if !saved.IsSubagent {
		t.Error("autosave rewrote is_subagent to false for a resumed delegate")
	}
	if saved.ParentSessionID != parentID {
		t.Errorf("autosave rewrote parent_session_id to %q for a resumed delegate, want %q", saved.ParentSessionID, parentID)
	}
	if saved.DivergenceTurn != 0 {
		t.Errorf("autosave invented a divergence turn (%d) for a spawned delegate, want 0", saved.DivergenceTurn)
	}
}

// TestMeta_VisionModelSurvivesRoundTrip pins that the SessionMeta population
// path in agent/session_state.go carries cfg.VisionModel through to the wire
// form with the correct JSON tag.
func TestMeta_VisionModelSurvivesRoundTrip(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withConfig(SessionConfig{VisionModel: "anthropic/claude-haiku-4-5"}))

	meta := sess.Meta()
	if meta.VisionModel != "anthropic/claude-haiku-4-5" {
		t.Fatalf("Meta().VisionModel = %q, want %q", meta.VisionModel, "anthropic/claude-haiku-4-5")
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if !strings.Contains(string(data), `"vision_model":"anthropic/claude-haiku-4-5"`) {
		t.Fatalf("meta JSON missing vision_model: %s", data)
	}
}
