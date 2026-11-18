package agent

import (
	"context"
	"os"
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestTurnEndsProcessReachesChildSessions: children are built from the
// parent's own toSnapshot (delegate_runtime describe, subagents' frozen
// descriptor), so the flag has to ride the snapshot to reach them. Its json
// tag alone is inert — SessionConfig persists through ConfigSnapshot, not
// directly. A delegate of a one-shot run dies with the same process, so it
// needs the same answer.
func TestTurnEndsProcessReachesChildSessions(t *testing.T) {
	t.Parallel()
	parent := SessionConfig{TurnEndsProcess: true}
	child := configFromSnapshot(parent.toSnapshot().Clone())
	if !child.TurnEndsProcess {
		t.Error("TurnEndsProcess did not survive the parent->child snapshot: a delegate of a one-shot run would not know its process dies with the turn")
	}
}

// TestRestoreTakesTurnEndsProcessFromTheRestoringProcess: whether the process
// outlives the turn belongs to whoever is restoring the session, not to the
// session on disk. The same session id resumed by one-shot `evener run` dies
// with its turn; resumed by the daemon it does not. So restore REPLACES the
// persisted value rather than inheriting it — otherwise a run-created session
// resumed under serve would treat background jobs as dying with the turn.
// Question availability belongs to the registered tools and can change even
// when the restoring process has the same lifetime.
func TestRestoreTakesTurnEndsProcessFromTheRestoringProcess(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name             string
		persisted        bool
		restorer         bool
		aside            bool
		legacy           bool
		responder        bool
		interactive      bool
		interactiveAside bool
		questionUpdate   string
		resultTool       string
	}{
		{name: "serve resuming a one-shot session", persisted: true, restorer: false},
		{name: "one-shot resuming a serve session", persisted: false, restorer: true},
		{name: "one-shot resume-with a serve session", restorer: true, aside: true},
		{name: "daemon resuming a daemon session"},
		{name: "daemon resuming without recorded launch capabilities", legacy: true, questionUpdate: envctx.QuestionsUnavailable},
		{name: "one-shot resuming without its attached responder", persisted: true, restorer: true, responder: true, questionUpdate: envctx.QuestionsUnavailable, resultTool: "submit_result"},
		{name: "interactive aside of an unattended session", persisted: true, restorer: true, aside: true, interactiveAside: true, questionUpdate: envctx.QuestionsAvailable},
		{name: "interactive session keeping its question tool", interactive: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, cwd := t.TempDir(), t.TempDir()
			adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
				// Follow the launch contract until the conversation supplies a new one.
				var observed envctx.Tracker
				for _, msg := range req.Messages {
					observed.ReplayBlock(msg.Text())
				}
				accepts := tt.interactive || tt.responder
				if state := observed.State().Last.ResultEndReason; state != "" {
					accepts = state == envctx.EndReasonAvailable
				}
				report := map[string]any{"message": "done", "end_turn": true}
				if accepts {
					report["end_reason"] = "done"
				}
				call := communicateCallArgs("final-report", report)
				if tt.resultTool != "" {
					call.Name = tt.resultTool
				}
				return agenttest.ToolCallResponse(call)
			}}
			parent := newSession(t, withStateDir(dir), withDir(cwd), withAdapter(adapter), withConfig(SessionConfig{
				TurnEndsProcess: tt.persisted, NonInteractive: !tt.interactive, AskResponderAttached: tt.responder, ResultToolName: tt.resultTool,
			}))
			sendOneUserInput(t, parent, "launch")
			launch := adapter.Requests()[0]
			startup, _ := responsesInstructionsAndHistory(responsesPromptBody(t, launch, parent.profile.Resolved()))
			var prior []any
			wantAsk := tt.interactive || tt.responder
			var questionUpdates []string
			var endReasonUpdates []string
			check := func(req llm.Request, modes ...string) []any {
				t.Helper()
				body := responsesPromptBody(t, req, parent.profile.Resolved())
				prompt, history := responsesInstructionsAndHistory(body)
				input := body["input"].([]any)
				if prompt != startup || len(input) < len(prior) || !slices.EqualFunc(input[:len(prior)], prior, reflect.DeepEqual) {
					t.Fatal("restore rewrote the native prompt or an earlier input")
				}
				tools, _ := body["tools"].([]map[string]any)
				if hasAsk := slices.ContainsFunc(tools, func(def map[string]any) bool { return def["name"] == askUserToolName }); hasAsk != wantAsk {
					t.Errorf("native ask_user tool present = %t, want %t", hasAsk, wantAsk)
				}
				resultIndex := slices.IndexFunc(tools, func(def map[string]any) bool { return def["name"] == parent.resultToolName() })
				if resultIndex < 0 {
					t.Fatal("native result tool is missing")
				}
				params := tools[resultIndex]["parameters"].(map[string]any)
				props := params["properties"].(map[string]any)
				if _, accepts := props["end_reason"]; accepts != wantAsk {
					t.Errorf("native result tool accepts end_reason = %t, want %t", accepts, wantAsk)
				}
				var gotModes, questions, endReasons []string
				for _, msg := range history {
					var block envctx.Tracker
					if !block.ReplayBlock(msg) {
						continue
					}
					last := block.State().Last
					if last.ExecutionMode != "" {
						gotModes = append(gotModes, last.ExecutionMode)
					}
					if last.AskUserState != "" {
						questions = append(questions, last.AskUserState)
					}
					if last.ResultEndReason != "" {
						endReasons = append(endReasons, last.ResultEndReason)
					}
				}
				if !slices.Equal(endReasons, endReasonUpdates) {
					t.Errorf("native result-tool end_reason updates = %v, want %v", endReasons, endReasonUpdates)
				}
				if !slices.Equal(questions, questionUpdates) {
					t.Errorf("native question availability updates = %v, want %v", questions, questionUpdates)
				}
				if !slices.Equal(gotModes, modes) {
					t.Errorf("native execution mode updates = %v, want %v", gotModes, modes)
				}
				return input
			}
			prior = check(launch)
			id := parent.ID()
			if tt.aside {
				var err error
				cfg := parent.Meta().Config
				if tt.interactiveAside {
					cfg.NonInteractive = false
				}
				id, err = AsideSessionWithConfig(dir, id, cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			parent.Close()
			wantAsk = tt.interactive || tt.interactiveAside
			if tt.questionUpdate != "" {
				questionUpdates = []string{tt.questionUpdate}
				if wantAsk {
					endReasonUpdates = []string{"available"}
				} else {
					endReasonUpdates = []string{"omit"}
				}
			}
			previous := new(tt.persisted)
			if tt.legacy {
				previous = nil
				meta := loadMetaForTest(t, parent)
				meta.PromptLaunch.TurnEndsProcess = nil
				meta.PromptLaunch.HasAskUser = nil
				meta.PromptLaunch.HasEndReason = nil
				saved, err := os.ReadFile(schema.SessionTranscriptPath(dir, id))
				if err != nil {
					t.Fatal(err)
				}
				dir = t.TempDir()
				if err := schema.SaveSessionMeta(dir, meta); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(schema.SessionTranscriptPath(dir, id), saved, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var modes []string
			for resume, oneShot := range []bool{tt.restorer, tt.restorer, tt.persisted} {
				meta, err := schema.LoadSessionMeta(dir, id)
				if err != nil {
					t.Fatal(err)
				}
				sess, err := RestoreSessionFromMetaWithConfig(parent.client, parent.profile, execenv.NewLocalExecutionEnvironment(cwd), meta, RestoreSessionConfig{StateDir: dir, TurnEndsProcess: oneShot, testOnly: testConfig{skipGitSnapshot: true}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(sess.Close)
				if sess.cfg.TurnEndsProcess != oneShot {
					t.Fatalf("restored TurnEndsProcess = %v, want %v", sess.cfg.TurnEndsProcess, oneShot)
				}
				mode := envctx.DaemonMode
				if oneShot {
					mode = envctx.OneShotMode
				}
				if previous == nil || *previous != oneShot {
					modes = append(modes, mode)
					previous = new(oneShot)
				}
				beforeInput := func() llm.Request {
					t.Helper()
					_, _, _, req, _, _, err := sess.prepareModelRequestWithError(t.Context(), 1, &events.RoundTimings{})
					if err != nil {
						t.Fatal(err)
					}
					return req
				}
				check(beforeInput(), modes...)
				beforeReport := len(adapter.Requests())
				sendOneUserInput(t, sess, "continue")
				requests := adapter.Requests()
				if len(requests) != beforeReport+1 {
					t.Fatalf("report needed %d model requests", len(requests)-beforeReport)
				}
				prior = check(requests[len(requests)-1], modes...)
				if resume == 1 && !tt.aside {
					seedNumberedSessionHistory(t, sess, 8)
					sess.contextMgr.PreserveRecentTurns = 1
					if err := sess.Compact(t.Context()); err != nil {
						t.Fatal(err)
					}
					if n := countEnvironmentTurns(sess); n != 0 {
						t.Fatalf("fold retained %d environment turns", n)
					}
					prior = nil
					check(beforeInput(), modes...)
				}
				sess.Close()
			}
		})
	}
}

// TestAskResponderAttachedIsLiveOnly: a responder belongs to the process that
// attached it, not to the session. The flag offers ask_user in this process,
// never reaches the snapshot, and leaves the persisted NonInteractive as the
// session set it, so a resumed session is exactly what a plain run saves.
func TestAskResponderAttachedIsLiveOnly(t *testing.T) {
	t.Parallel()
	cfg := SessionConfig{NonInteractive: true, AskResponderAttached: true}
	if cfg.noOneToAsk() {
		t.Error("a session with a responder attached reports nobody to ask")
	}
	snap := cfg.toSnapshot()
	if !snap.NonInteractive {
		t.Error("attaching a responder changed the persisted NonInteractive")
	}
	if configFromSnapshot(snap.Clone()).AskResponderAttached {
		t.Error("AskResponderAttached survived the snapshot; a resume would think a responder is attached")
	}
	if !(SessionConfig{NonInteractive: true}).noOneToAsk() {
		t.Error("a non-interactive session with no responder reports someone to ask")
	}
}

// TestRestoreKeepsPersistedNonInteractive: restore takes NonInteractive from
// the snapshot whoever restores, so a serve-created interactive session
// resumed once by one-shot `evener run` is still interactive under serve.
func TestRestoreKeepsPersistedNonInteractive(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		persisted bool
		oneShot   bool
		want      bool
	}{
		{name: "one-shot resuming an interactive session", persisted: false, oneShot: true, want: false},
		{name: "one-shot resuming a non-interactive session", persisted: true, oneShot: true, want: true},
		{name: "serve resuming an interactive session", persisted: false, oneShot: false, want: false},
		{name: "serve resuming a non-interactive session", persisted: true, oneShot: false, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			meta := schema.SessionMeta{
				ID:        "01KONESHOTNONINTERACTIVE000",
				ProfileID: "openai",
				Model:     "gpt-5.2",
				Config:    schema.ConfigSnapshot{NonInteractive: tt.persisted},
			}
			sess, err := RestoreSessionFromMetaWithConfig(
				client,
				NewOpenAIProfile("gpt-5.2"),
				execenv.NewLocalExecutionEnvironment(t.TempDir()),
				meta,
				RestoreSessionConfig{StateDir: t.TempDir(), TurnEndsProcess: tt.oneShot},
			)
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			t.Cleanup(sess.Close)
			if got := sess.cfg.NonInteractive; got != tt.want {
				t.Fatalf("restored NonInteractive = %v, want %v (persisted %v, one-shot restorer %v)", got, tt.want, tt.persisted, tt.oneShot)
			}
		})
	}
}

// TestFrozenDescriptorTakesTurnEndsProcessFromTheLiveParent pins the third
// path, which the drain-guidance attempt missed: a stable delegate restarted
// from its frozen descriptor. The descriptor's snapshot answers "what was this
// delegate asked to be"; process lifetime belongs to the process now hosting
// it. A descriptor frozen during a one-shot run and restarted under serve must
// not carry true into the daemon, nor false the other way.
func TestFrozenDescriptorTakesTurnEndsProcessFromTheLiveParent(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		frozen bool
		parent bool
	}{
		{name: "one-shot descriptor restarted under serve", frozen: true, parent: false},
		{name: "serve descriptor restarted under one-shot", frozen: false, parent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			frozenConfig := SessionConfig{TurnEndsProcess: tt.frozen, NonInteractive: true}.toSnapshot()
			parentCfg := SessionConfig{TurnEndsProcess: tt.parent}
			got := subagentConfigFromFrozenDescriptor(frozenConfig, parentCfg)
			if got.TurnEndsProcess != tt.parent {
				t.Fatalf("frozen-descriptor TurnEndsProcess = %v, want the live parent's %v (frozen was %v)", got.TurnEndsProcess, tt.parent, tt.frozen)
			}
			if !got.NonInteractive {
				t.Fatal("frozen-descriptor merge dropped a descriptor-scoped field; the helper must still start from the snapshot")
			}
		})
	}
}

func TestFrozenDescriptorTakesLifetimeContextFromLiveParent(t *testing.T) {
	t.Parallel()
	owner, cancelOwner := context.WithCancel(context.Background())
	got := subagentConfigFromFrozenDescriptor(
		SessionConfig{NonInteractive: true}.toSnapshot(),
		SessionConfig{LifetimeContext: owner},
	)
	if got.LifetimeContext == nil {
		t.Fatal("frozen delegate dropped the live parent's lifetime context")
	}
	cancelOwner()
	select {
	case <-got.LifetimeContext.Done():
	default:
		t.Fatal("frozen delegate lifetime outlived the live parent")
	}
}
