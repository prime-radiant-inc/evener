package agent

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestSetModelPendingFoldRefusesBeforeMutation(t *testing.T) {
	for _, delayedResolution := range []bool{false, true} {
		name := "already pending"
		if delayedResolution {
			name = "becomes pending during model resolution"
		}
		t.Run(name, func(t *testing.T) {
			const target = "anthropic/claude-opus-4-6"
			s := newScriptedSummaryCompactSession(t, "model-pending-fold", func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("summary")}
			}, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true,
				ResolveProfile: testResolver, ModelFallbacks: []string{"openai/gpt-4.1-mini"}}))
			disableSessionNaming(s)
			seedNumberedSessionHistory(t, s, 12)
			if err := s.autoSaveMeta(); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var gate, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			s.client.Register(&fakeAdapter{name: "anthropic", liveModels: func(context.Context) ([]registry.Model, error) {
				if delayedResolution {
					gate.Do(func() { close(entered); <-release })
				}
				return []registry.Model{{ID: "claude-opus-4-6"}}, nil
			}})
			evs, evMu, eventsDone := collectEvents(s)
			t.Cleanup(func() { unblock(); s.Close(); <-eventsDone })
			switchDone := make(chan error, 1)
			if delayedResolution {
				go func() { switchDone <- s.SetModel(target) }()
				<-entered
			}
			if err := s.transcript.Close(); err != nil {
				t.Fatal(err)
			}
			fs := &foldFaultFS{Fs: afero.NewOsFs(), mode: "retained"}
			writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.id)
			if err != nil {
				t.Fatal(err)
			}
			s.attachTranscript(writer)
			if err := s.Compact(t.Context()); !errors.Is(err, transcript.ErrRetainedUnsynced) {
				t.Fatalf("actual pending marker: %v", err)
			}
			if fs.reached != 1 || s.pendingFold == nil {
				t.Fatal("did not reach retained final marker")
			}
			oldProfile := s.currentProfile()
			s.contextMgr.RecordInputTokens(77, len(s.history))
			oldPressure := s.contextMgr.Pressure(s.history, len(s.cachedSystemPrompt))
			oldPrompt := s.cachedSystemPrompt
			oldTools := slices.Clone(s.cachedToolDefs)
			oldFallbacks := slices.Clone(s.cfg.ModelFallbacks)
			oldHistory := slices.Clone(s.history)
			oldMeta, err := schema.LoadSessionMeta(s.stateDir, s.id)
			if err != nil {
				t.Fatal(err)
			}
			oldRaw, err := readTranscriptFull(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			if delayedResolution {
				unblock()
				err = <-switchDone
			} else {
				err = s.SetModel(target)
			}
			if !errors.Is(err, errFoldDurabilityPending) {
				t.Fatalf("pending switch err=%v", err)
			}
			if s.currentProfile() != oldProfile || s.contextMgr.LastInputTokens() != 77 || s.contextMgr.Pressure(s.history, len(s.cachedSystemPrompt)) != oldPressure ||
				s.cachedSystemPrompt != oldPrompt || !reflect.DeepEqual(s.cachedToolDefs, oldTools) || !slices.Equal(s.cfg.ModelFallbacks, oldFallbacks) || !reflect.DeepEqual(s.history, oldHistory) {
				t.Fatal("refused model switch changed effective profile/context/tools/prompt/fallbacks/history")
			}
			meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
			if err != nil || !reflect.DeepEqual(meta, oldMeta) {
				t.Fatalf("refused switch changed metadata: %v", err)
			}
			raw, err := readTranscriptFull(s.TranscriptPath())
			if err != nil || len(raw.Entries) != len(oldRaw.Entries) {
				t.Fatalf("refused switch changed raw history: %v", err)
			}
			fs.mu.Lock()
			fs.mode = ""
			fs.mu.Unlock()
			if err := s.Compact(t.Context()); err != nil {
				t.Fatal(err)
			}
			if fs.reached != 1 || s.pendingFold != nil {
				t.Fatal("settlement did not use the same final marker")
			}
			if err := s.SetModel(target); err != nil {
				t.Fatalf("settled switch: %v", err)
			}
			if s.currentProfile().ID() != "anthropic" || len(s.cfg.ModelFallbacks) != 0 {
				t.Fatal("normal cross-provider switch was not applied")
			}
			path, stateDir, id, dir, client := s.TranscriptPath(), s.stateDir, s.id, s.currentEnv().WorkingDirectory(), s.client
			s.Close()
			<-eventsDone
			evMu.Lock()
			changes := 0
			for _, ev := range *evs {
				if ev.Kind == events.EventModelChanged {
					changes++
				}
			}
			evMu.Unlock()
			if changes != 1 {
				t.Fatalf("model switch events=%d, want one successful switch", changes)
			}
			raw, err = readTranscriptFull(path)
			if err != nil {
				t.Fatal(err)
			}
			markers := 0
			for _, entry := range raw.Entries {
				if entry.Turn.Kind == schema.TurnModelSwitch {
					markers++
					if entry.Turn.ModelSwitch == nil || entry.Turn.ModelSwitch.OldProvider != oldProfile.ID() || entry.Turn.ModelSwitch.NewProvider != "anthropic" {
						t.Fatal("switch marker lost exact transition")
					}
				}
			}
			if markers != 1 {
				t.Fatalf("raw switch markers=%d", markers)
			}
			meta, err = schema.LoadSessionMeta(stateDir, id)
			if err != nil || meta.Model != "claude-opus-4-6" {
				t.Fatalf("successful switch metadata: model=%s err=%v", meta.Model, err)
			}
			profile, err := testResolver(target)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: stateDir})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if restored.currentProfile().ID() != "anthropic" || lastMarkerTurn(t, restored).ModelSwitch.NewProvider != "anthropic" {
				t.Fatal("normal switch did not survive reopen")
			}
		})
	}
}

func TestSetModelOrdinaryWriteFailureRemainsWarning(t *testing.T) {
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
	disableSessionNaming(s)
	if err := s.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	fs := &foldFaultFS{Fs: afero.NewOsFs(), mode: "record"}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.id)
	if err != nil {
		t.Fatal(err)
	}
	s.attachTranscript(writer)
	evs, evMu, done := collectEvents(s)
	if err := s.SetModel("gpt-4.1-mini"); err != nil {
		t.Fatalf("ordinary write failure changed switch return behavior: %v", err)
	}
	if fs.reached != 1 || s.currentProfile().Model() != "gpt-4.1-mini" || lastMarkerTurn(t, s).ModelSwitch.NewModel != "gpt-4.1-mini" {
		t.Fatal("did not reach the ordinary warning-only switch write")
	}
	s.Close()
	<-done
	evMu.Lock()
	defer evMu.Unlock()
	warnings, changes := 0, 0
	for _, ev := range *evs {
		if ev.Kind == events.EventWarning {
			warnings++
		}
		if ev.Kind == events.EventModelChanged {
			changes++
		}
	}
	if warnings == 0 || changes != 1 {
		t.Fatalf("ordinary write outcome warnings=%d switches=%d", warnings, changes)
	}
	raw, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range raw.Entries {
		if entry.Turn.Kind == schema.TurnModelSwitch {
			t.Fatal("failed physical write invented a raw switch record")
		}
	}
}
