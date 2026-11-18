package agent

import (
	"os"
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
)

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
		wirePrompt := func(req llm.Request) (string, []string) {
			t.Helper()
			row := parent.profile.Resolved()
			body, err := (&responses.Protocol{}).BuildBody(llm.ShapeRequest(req, row), row)
			if err != nil {
				t.Fatal(err)
			}
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
		base, parentHistory := wirePrompt(adapter.Requests()[0])
		parentTrailer := parent.commitTrailerPrompt()
		if parentTrailer == "" || !strings.Contains(base, parentTrailer) || strings.Contains(strings.Join(parentHistory, "\n"), parentTrailer) {
			t.Fatal("ordinary first request did not keep its attribution in its startup prompt")
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
		t.Run("build upgrade", func(t *testing.T) {
			oldState := t.TempDir()
			meta := parent.Meta()
			old := entries.Header
			old.BuildVersion = "fixture-earlier-build"
			oldTrailer, err := commitprovenance.Trailer(old.BuildVersion, old.ProfileID+"/"+old.Model, host, old.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			old.SystemPrompt = strings.Replace(old.SystemPrompt, parentTrailer, oldTrailer, 1)
			writer, err := transcript.NewWriter(schema.SessionTranscriptPath(oldState, meta.ID), old)
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := schema.SaveSessionMeta(oldState, meta); err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreSessionFromMetaWithConfig(parent.client, parent.profile, execenv.NewLocalExecutionEnvironment(parent.currentEnv().WorkingDirectory()), meta, RestoreSessionConfig{StateDir: oldState, testOnly: testConfig{skipGitSnapshot: true}})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			sendOneUserInput(t, restored, "after-upgrade-e5")
			reqs := adapter.Requests()
			inherited, history := wirePrompt(reqs[len(reqs)-1])
			if inherited != strings.Replace(base, parentTrailer, oldTrailer, 1) || strings.Count(strings.Join(history, "\n"), parentTrailer) != 1 {
				t.Error("build upgrade did not preserve the recorded prompt and report the running build to the model")
			}
		})
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
