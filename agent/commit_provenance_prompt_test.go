package agent

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/commitprovenance"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

func responsesPromptBody(t *testing.T, req llm.Request, row registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&responses.Protocol{}).BuildBody(llm.ShapeRequest(req, row), row)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func responsesInstructionsAndHistory(body map[string]any) (string, []string) {
	instructions, _ := body["instructions"].(string)
	var history []string
	for _, item := range body["input"].([]any) {
		msg := item.(map[string]any)
		parts, _ := msg["content"].([]any)
		for _, part := range parts {
			text, _ := part.(map[string]any)["text"].(string)
			switch msg["role"] {
			case "developer":
				instructions = text
			case "user":
				history = append(history, text)
			}
		}
	}
	return instructions, history
}

func TestSystemPromptCarriesCommitTrailer(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("no hostname available")
	}

	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed: %s", warning)
	}
	want, err := commitprovenance.Trailer(BuildVersion, s.profile.ID()+"/"+s.profile.Model(), host, s.ID())
	if err != nil {
		t.Skipf("hostname %q is not usable in a trailer: %v", host, err)
	}
	if !strings.Contains(prompt, want) {
		t.Fatalf("system prompt does not carry the provenance trailer %q", want)
	}
	t.Run("fork and resume", func(t *testing.T) {
		stateDir := t.TempDir()
		adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") }}
		parent := newSession(t, withStateDir(stateDir), withAdapter(adapter), withConfig(SessionConfig{MaxSubagentDepth: 1}))
		sendOneUserInput(t, parent, "input-a1")
		sendOneUserInput(t, parent, "input-b2")
		entries, err := readTranscriptFull(t.Context(), transcriptPath(stateDir, parent.ID()), stateDir)
		if err != nil {
			t.Fatal(err)
		}
		var position int
		for i, entry := range entries.Entries {
			if entry.Turn.Kind == schema.TurnUserInput {
				position = i + 1
			}
		}
		childID, err := ForkSession(stateDir, parent.ID(), position, "input-c3", "")
		if err != nil {
			t.Fatal(err)
		}
		wireBody := func(req llm.Request) map[string]any {
			return responsesPromptBody(t, req, parent.profile.Resolved())
		}
		wirePrompt := func(req llm.Request) (string, []string) {
			return responsesInstructionsAndHistory(wireBody(req))
		}
		parentRequests := adapter.Requests()
		base, parentHistory := wirePrompt(parentRequests[0])
		parentTrailer := parent.commitTrailerPrompt()
		if parentTrailer == "" || !strings.Contains(base, parentTrailer) || strings.Contains(strings.Join(parentHistory, "\n"), parentTrailer) {
			t.Fatal("ordinary first request did not keep its attribution in its startup prompt")
		}
		checkAttribution := func(t *testing.T, req llm.Request, startup, launchTrailer, want string, wantUpdates int) {
			t.Helper()
			instructions, history := wirePrompt(req)
			tracker := envctx.NewTracker(envctx.State{Last: envctx.Snapshot{CommitTrailer: launchTrailer}})
			updates := 0
			for _, message := range history {
				if tracker.ReplayBlock(message) {
					var block envctx.Tracker
					block.ReplayBlock(message)
					if last := block.State().Last; last.CommitTrailer != "" || last.OmitCommitTrailer {
						updates++
					}
				}
			}
			if got := tracker.State().Last.CommitTrailer; instructions != startup || got != want || updates != wantUpdates {
				t.Errorf("frozen instructions = %t, trailer = %q, updates = %d; want %q and %d", instructions == startup, got, updates, want, wantUpdates)
			}
		}
		for _, initial := range []string{commitprovenance.ModeCoauthor, commitprovenance.ModeOff} {
			t.Run("restore overrides "+initial, func(t *testing.T) {
				dir := t.TempDir()
				original := newSession(t, withStateDir(dir), withAdapter(adapter), withConfig(SessionConfig{CommitProvenance: initial}))
				sendOneUserInput(t, original, "before-override")
				initialRequests := adapter.Requests()
				startup, _ := wirePrompt(initialRequests[len(initialRequests)-1])
				launchTrailer := original.commitTrailerPrompt()
				original.Close()
				opposite := commitprovenance.ModeOff
				if initial == opposite {
					opposite = commitprovenance.ModeCoauthor
				}
				for i, override := range []string{opposite, "", initial} {
					meta, err := schema.LoadSessionMeta(dir, original.ID())
					if err != nil {
						t.Fatal(err)
					}
					restored, err := RestoreSessionFromMetaWithConfig(original.client, original.profile, execenv.NewLocalExecutionEnvironment(original.currentEnv().WorkingDirectory()), meta, RestoreSessionConfig{StateDir: dir, CommitProvenance: override, testOnly: testConfig{skipGitSnapshot: true}})
					if err != nil {
						t.Fatal(err)
					}
					want := restored.commitTrailerPrompt()
					sendOneUserInput(t, restored, "after-override")
					requests := adapter.Requests()
					wantUpdates := 1
					if i == 2 {
						wantUpdates++
					}
					checkAttribution(t, requests[len(requests)-1], startup, launchTrailer, want, wantUpdates)
					if i == 1 {
						seedNumberedSessionHistory(t, restored, 8)
						restored.contextMgr.PreserveRecentTurns = 1
						if err := restored.Compact(t.Context()); err != nil {
							t.Fatal(err)
						}
						if n := countEnvironmentTurns(restored); n != 0 {
							t.Fatalf("fold retained %d environment turns", n)
						}
						_, _, _, req, _, _, err := restored.prepareModelRequestWithError(t.Context(), 1, &events.RoundTimings{})
						if err != nil {
							t.Fatal(err)
						}
						checkAttribution(t, req, startup, launchTrailer, want, 1)
					}
					restored.Close()
				}
			})
		}
		off := parent.Meta().Config
		off.CommitProvenance = commitprovenance.ModeOff
		asideID, err := AsideSessionWithConfig(stateDir, parent.ID(), off)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{childID, childID, asideID, asideID} {
			meta, err := schema.LoadSessionMeta(stateDir, id)
			if err != nil {
				t.Fatal(err)
			}
			child, err := RestoreSessionFromMetaWithConfig(parent.client, parent.profile, execenv.NewLocalExecutionEnvironment(parent.currentEnv().WorkingDirectory()), meta, RestoreSessionConfig{StateDir: stateDir, testOnly: testConfig{skipGitSnapshot: true}})
			if err != nil {
				t.Fatal(err)
			}
			var childTrailer string
			if id != asideID {
				childTrailer, err = commitprovenance.Trailer(BuildVersion, parent.profile.ID()+"/"+parent.profile.Model(), host, id)
				if err != nil {
					t.Fatal(err)
				}
			}
			sendOneUserInput(t, child, "input-d4")
			child.Close()
			reqs := adapter.Requests()
			inherited, history := wirePrompt(reqs[len(reqs)-1])
			if inherited != base {
				t.Error("fork changed the parent's instructions on the wire")
			}
			tracker := envctx.NewTracker(envctx.State{Last: envctx.Snapshot{CommitTrailer: parentTrailer}})
			identityBlocks := 0
			for _, text := range history {
				if tracker.ReplayBlock(text) {
					block := envctx.NewTracker(envctx.State{})
					block.ReplayBlock(text)
					if block.State().Last.SessionID == id {
						identityBlocks++
					}
				}
			}
			if current := tracker.State().Last; identityBlocks != 1 || current.SessionID != id || current.CommitTrailer != childTrailer {
				t.Errorf("child identity appears in %d environment blocks, effective state %+v", identityBlocks, current)
			}
			if childTrailer != "" && strings.Count(strings.Join(history, "\n"), childTrailer) != 1 {
				t.Error("child's commit trailer did not appear exactly once in the wire history")
			}
		}
		for _, tc := range []struct{ name, build, host, retainedHost, mode, want string }{
			{name: "build upgrade", build: "fixture-earlier-build", host: host, want: parentTrailer},
			{name: "disable trailer in saved prompt", build: BuildVersion, host: host, mode: commitprovenance.ModeOff},
			{name: "renamed launch host", build: BuildVersion, host: host + "-prior", want: parentTrailer},
			{name: "renamed launch host with explicit coauthor", build: BuildVersion, host: host + "-prior", mode: commitprovenance.ModeCoauthor, want: parentTrailer},
			{name: "renamed retained host", build: BuildVersion, host: host, retainedHost: host + "-prior", want: parentTrailer},
		} {
			t.Run(tc.name, func(t *testing.T) {
				oldState := t.TempDir()
				meta := parent.Meta()
				old := entries.Header
				old.BuildVersion = tc.build
				oldTrailer, err := commitprovenance.Trailer(old.BuildVersion, old.ProfileID+"/"+old.Model, tc.host, old.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				// This saved instruction requires its original trailer unconditionally.
				old.SystemPrompt = "## Commit provenance\nWhen you create a git commit, end the commit message with this exact line:\n" +
					oldTrailer + "\nDo not add it to pull request descriptions. Do not amend a commit only to add it.\n"
				writer, err := transcript.NewWriter(schema.SessionTranscriptPath(oldState, meta.ID), old)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries.Entries {
					if err := writer.Append(entry.Turn); err != nil {
						t.Fatal(err)
					}
				}
				wantUpdates := 1
				if tc.retainedHost != "" {
					retained, err := commitprovenance.Trailer(old.BuildVersion, old.ProfileID+"/"+old.Model, tc.retainedHost, old.SessionID)
					if err != nil {
						t.Fatal(err)
					}
					block := envctx.NewTracker(envctx.State{HasSent: true}).RenderDiff(envctx.Snapshot{CommitTrailer: retained})
					if err := writer.Append(schema.NewTurn(schema.TurnEnvironment, llm.User(block))); err != nil {
						t.Fatal(err)
					}
					wantUpdates++
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := schema.SaveSessionMeta(oldState, meta); err != nil {
					t.Fatal(err)
				}
				previous := parentRequests[len(parentRequests)-1]
				previous.Messages = slices.Clone(previous.Messages)
				previous.Messages[0] = llm.System(old.SystemPrompt)
				for range 2 {
					meta, err := schema.LoadSessionMeta(oldState, parent.ID())
					if err != nil {
						t.Fatal(err)
					}
					restored, err := RestoreSessionFromMetaWithConfig(parent.client, parent.profile, execenv.NewLocalExecutionEnvironment(parent.currentEnv().WorkingDirectory()), meta, RestoreSessionConfig{StateDir: oldState, CommitProvenance: tc.mode, testOnly: testConfig{skipGitSnapshot: true}})
					if err != nil {
						t.Fatal(err)
					}
					sendOneUserInput(t, restored, "after-upgrade-e5")
					restored.Close()
					reqs := adapter.Requests()
					req := reqs[len(reqs)-1]
					checkAttribution(t, req, old.SystemPrompt, oldTrailer, tc.want, wantUpdates)
					before, after := wireBody(previous)["input"].([]any), wireBody(req)["input"].([]any)
					if len(after) < len(before) || !reflect.DeepEqual(after[:len(before)], before) {
						t.Error("restore changed the previously dispatched input items")
					}
					previous = req
				}
			})
		}
		if err := parent.SetModel("gpt-5.4"); err != nil {
			t.Fatal(err)
		}
		// A model picked during a quota wait can retry before the next user input.
		_, _, _, retry, _, _, err := parent.prepareModelRequestWithError(t.Context(), 1, &events.RoundTimings{})
		if err != nil {
			t.Fatal(err)
		}
		instructions, history := wirePrompt(retry)
		if retry.Model != "gpt-5.4" || instructions != base || strings.Count(strings.Join(history, "\n"), parent.commitTrailerPrompt()) != 1 {
			t.Error("selected model's first request did not preserve the startup instructions and carry its own commit trailer")
		}
	})
}

func TestSystemPromptOmitsCommitTrailerWhenOff(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		CommitProvenance: commitprovenance.ModeOff,
	}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatalf("render failed: %s", warning)
	}
	if strings.Contains(prompt, "Co-authored-by: Evener") {
		t.Fatal("system prompt carries a provenance trailer while the mode is off")
	}
}

func TestSessionConfigCommitProvenanceSnapshotRoundTrip(t *testing.T) {
	cfg := SessionConfig{CommitProvenance: commitprovenance.ModeOff}
	snap := cfg.toSnapshot()
	if snap.CommitProvenance != commitprovenance.ModeOff {
		t.Fatalf("snapshot = %q, want off", snap.CommitProvenance)
	}
	if got := configFromSnapshot(snap).CommitProvenance; got != commitprovenance.ModeOff {
		t.Fatalf("round trip = %q, want off", got)
	}
}

func TestSessionConfigCommitProvenanceApplyDefaults(t *testing.T) {
	cfg := SessionConfig{CommitProvenance: " off "}
	cfg.applyDefaults()
	if cfg.CommitProvenance != commitprovenance.ModeOff {
		t.Fatalf("normalized mode = %q, want off", cfg.CommitProvenance)
	}
	empty := SessionConfig{}
	empty.applyDefaults()
	if empty.CommitProvenance != "" {
		t.Fatalf("unset mode = %q, want empty default", empty.CommitProvenance)
	}
}

func TestSystemPromptWarnsOnInvalidCommitProvenance(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, CommitProvenance: "bogus"}))
	prompt, warning, renderFailed := s.renderSystemPrompt(s.env)
	if warning != "" || renderFailed {
		t.Fatal("configuration diagnostic used the prompt-render failure channel")
	}
	if s.commitProvenanceWarning() == "" {
		t.Fatal("invalid mode produced no configuration diagnostic")
	}
	if strings.Contains(prompt, "Co-authored-by: Evener") {
		t.Fatal("invalid mode still rendered a trailer")
	}
}
