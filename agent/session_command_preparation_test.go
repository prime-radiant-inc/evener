package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func commandPreparationSession(t *testing.T, root, pluginDir string) (*Session, *agenttest.ScriptedAdapter) {
	t.Helper()
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response { return toolCallResponse(communicateCall("done-1", "ok")) }}
	s := newSession(t, withAdapter(adapter), withDir(root), withProfile(newAnthropicProfile("claude-test")), withConfig(SessionConfig{StateDir: root, PluginDirs: []string{pluginDir}}), withoutGitSnapshot())
	_, stop := captureEvents(s)
	t.Cleanup(stop)
	return s, adapter
}

func acceptedCommandPreparation(t *testing.T, s *Session, id, original string, names ...string) queuedInput {
	t.Helper()
	input := []appwire.InputItem{{Type: "text", Text: original}}
	for _, name := range names {
		input = append(input, appwire.InputItem{Type: "command", Name: name})
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: id, Input: input}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range s.clientMutations.snapshot().InputQueue {
		if entry.ClientMutationID == id {
			queued := queuedInputFromClientMutation(entry)
			queued.ClientMutationID = entry.ClientMutationID
			return queued
		}
	}
	t.Fatal("accepted input missing")
	return queuedInput{}
}

func assertCommandEffect(t *testing.T, root, file, want string) {
	t.Helper()
	actual, err := os.ReadFile(filepath.Join(root, file))
	if err != nil || string(actual) != want {
		t.Fatalf("%s effect=%q err=%v, want %q", file, actual, err, want)
	}
}

func TestClientMutationCommandPreparationStartAndSteerRetry(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"start/environment", "start/user", "steer", "drain"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			pluginDir := writePluginCommand(t, "pkg", "probe", "PREPARED_ROUTE_318 !`printf x >> invoked`")
			s, adapter := commandPreparationSession(t, root, pluginDir)
			const original = "ORIGINAL_ROUTE_318 /pkg:probe prose"
			input := []appwire.InputItem{{Type: "text", Text: original}, {Type: "command", Name: "pkg:probe"}}
			id := "route-318"
			var err error
			switch route {
			case "start/environment", "start/user":
				_, err = s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, Input: input})
			case "steer":
				_, err = s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: id, Input: input})
			case "drain":
				acceptedCommandPreparation(t, s, "source-318", original, "pkg:probe")
				queue, _ := s.ClientMutationProjection()
				_, err = s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: id, ExpectedQueueRevision: queue.Revision})
			}
			if err != nil {
				t.Fatal(err)
			}
			if route == "start/user" {
				if err := s.maybeAppendEnvironmentContext(); err != nil {
					t.Fatal(err)
				}
			}
			fs := attachEnvironmentFailureFS(t, s)
			failure := errors.New("zero-byte selected input transcript failure")
			fs.mu.Lock()
			fs.writeFailure = failure
			fs.mu.Unlock()
			if strings.HasPrefix(route, "start/") {
				if _, _, err := s.ProcessClientMutationStart(context.Background(), nil); !errors.Is(err, failure) {
					t.Fatalf("initial admission: %v", err)
				}
			} else if s.injectDrainedSteering() {
				t.Fatal("failed steering append reported delivery")
			}
			assertCommandEffect(t, root, "invoked", "x")
			if len(adapter.Requests()) != 0 {
				t.Fatal("failed input dispatched")
			}
			delete(s.pluginCommands, "pkg:probe")
			if strings.HasPrefix(route, "start/") {
				if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran {
					t.Fatalf("retry ran=%v err=%v", ran, err)
				}
			} else if _, err := s.ProcessInput(context.Background(), "CARRIER_ROUTE_318", nil); err != nil {
				t.Fatal(err)
			}
			assertCommandEffect(t, root, "invoked", "x")
			kind := schema.TurnUserInput
			if !strings.HasPrefix(route, "start/") {
				kind = schema.TurnSteering
			}
			turn := findClientMutationTurn(s, id, kind)
			if turn == nil || turn.CommandInput == nil || turn.CommandInput.OriginalText != original || !strings.Contains(turn.Message.Text(), "PREPARED_ROUTE_318") {
				t.Fatalf("retry lost prepared body/prompt: %+v", turn)
			}
		})
	}
}

func TestClientMutationCommandPreparationPartialRetry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pluginDir := writePluginCommand(t, "pkg", "first", "FIRST_BODY_318 !`printf x >> first-invoked`")
	if err := os.WriteFile(filepath.Join(pluginDir, "commands", "second.md"), []byte("SECOND_BODY_318 !`printf x >> second-invoked`"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := commandPreparationSession(t, root, pluginDir)
	queued := acceptedCommandPreparation(t, s, "partial-318", "PARTIAL_ORIGINAL_318", "pkg:first", "pkg:second", "pkg:first")
	ctx := withQueuedClientMutation(context.Background(), queued)
	failure := errors.New("second preparation intent cannot persist")
	writes := 0
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		writes++
		if writes == 3 {
			return failure
		}
		return nil
	}
	if _, err := s.prepareSelectedCommands(ctx, queued.CommandNames); !errors.Is(err, failure) {
		t.Fatalf("partial error=%v", err)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	assertCommandEffect(t, root, "first-invoked", "x")
	if _, err := os.Stat(filepath.Join(root, "second-invoked")); !os.IsNotExist(err) {
		t.Fatalf("second executed before intent persisted: %v", err)
	}
	bodies, err := s.prepareSelectedCommands(ctx, queued.CommandNames)
	if err != nil || len(bodies) != 2 || !strings.Contains(bodies[0], "FIRST_BODY_318") || !strings.Contains(bodies[1], "SECOND_BODY_318") {
		t.Fatalf("partial retry bodies=%q err=%v", bodies, err)
	}
	assertCommandEffect(t, root, "first-invoked", "x")
	assertCommandEffect(t, root, "second-invoked", "x")
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("consume prepared input: ran=%v err=%v", ran, err)
	}
	assertCommandEffect(t, root, "first-invoked", "x")
	assertCommandEffect(t, root, "second-invoked", "x")
}

func TestClientMutationCommandPreparationUncertainTransform(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"drain/completed-first", "drain/uncertain-first", "promote"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			pluginDir := writePluginCommand(t, "pkg", "probe", "UNCERTAIN_BODY_318 !`printf x >> invoked`")
			s, adapter := commandPreparationSession(t, root, pluginDir)
			completed := queuedInput{}
			uncertain := queuedInput{}
			if route == "drain/uncertain-first" {
				uncertain = acceptedCommandPreparation(t, s, "uncertain-318", "UNCERTAIN_ORIGINAL_318", "pkg:probe", "pkg:probe")
				completed = acceptedCommandPreparation(t, s, "completed-318", "COMPLETED_ORIGINAL_318", "pkg:probe")
			} else {
				completed = acceptedCommandPreparation(t, s, "completed-318", "COMPLETED_ORIGINAL_318", "pkg:probe")
				uncertain = acceptedCommandPreparation(t, s, "uncertain-318", "UNCERTAIN_ORIGINAL_318", "pkg:probe", "pkg:probe")
			}
			if _, err := s.prepareSelectedCommands(withQueuedClientMutation(context.Background(), completed), completed.CommandNames); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("shell effects happened, completion save failed")
			writes := 0
			s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
				writes++
				if writes == 2 {
					return failure
				}
				return nil
			}
			if _, err := s.prepareSelectedCommands(withQueuedClientMutation(context.Background(), uncertain), uncertain.CommandNames); !errors.Is(err, failure) {
				t.Fatalf("uncertain completion error=%v", err)
			}
			s.clientMutations.faults.BeforeEffectSnapshotRename = nil
			assertCommandEffect(t, root, "invoked", "xx")
			id := "transformed-318"
			if route == "promote" {
				if _, err := s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: id, Index: 1}); err != nil {
					t.Fatal(err)
				}
			} else {
				queue, _ := s.ClientMutationProjection()
				if _, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: id, ExpectedQueueRevision: queue.Revision}); err != nil {
					t.Fatal(err)
				}
			}
			if s.injectDrainedSteering() {
				t.Fatal("uncertain transformed input delivered")
			}
			if len(adapter.Requests()) != 0 {
				t.Fatal("uncertain input dispatched")
			}
			assertCommandEffect(t, root, "invoked", "xx")
			turn := findClientMutationTurn(s, id, schema.TurnFailure)
			if turn == nil || turn.CommandInput == nil || !strings.Contains(turn.CommandInput.OriginalText, "UNCERTAIN_ORIGINAL_318") || !slices.Contains(turn.CommandInput.Names, "pkg:probe") || turn.Error == nil || !strings.Contains(turn.Error.Message, "uncertain") {
				t.Fatalf("uncertainty/original input lost: %+v", turn)
			}
			if route != "promote" && !strings.Contains(turn.CommandInput.OriginalText, "COMPLETED_ORIGINAL_318") {
				t.Fatalf("drain discarded completed source prompt: %+v", turn.CommandInput)
			}
			if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "healthy-318", Input: []appwire.InputItem{{Type: "text", Text: "HEALTHY_ORIGINAL_318"}}}); err != nil {
				t.Fatal(err)
			}
			if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran {
				t.Fatalf("healthy input: ran=%v err=%v", ran, err)
			}
			if findClientMutationTurn(s, "healthy-318", schema.TurnUserInput) == nil || len(adapter.Requests()) == 0 {
				t.Fatal("uncertain input blocked healthy input")
			}
			assertCommandEffect(t, root, "invoked", "xx")
		})
	}
}

func TestClientMutationCommandPreparationInterruptedRestore(t *testing.T) {
	t.Parallel()
	for _, interruption := range []string{"between-commands", "completion-save"} {
		t.Run(interruption, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			pluginDir := writePluginCommand(t, "pkg", "first", "FIRST_RESTORE_318 !`printf x >> first-invoked`")
			if err := os.WriteFile(filepath.Join(pluginDir, "commands", "second.md"), []byte("SECOND_RESTORE_318 !`printf x >> second-invoked`"), 0o600); err != nil {
				t.Fatal(err)
			}
			s, adapter := commandPreparationSession(t, root, pluginDir)
			queued := acceptedCommandPreparation(t, s, "interrupted-318", "INTERRUPTED_ORIGINAL_318", "pkg:first", "pkg:second")
			ctx, cancel := context.WithCancel(withQueuedClientMutation(context.Background(), queued))
			defer cancel()
			failure := errors.New("completion snapshot did not commit")
			writes := 0
			s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
				writes++
				if writes == 2 {
					if interruption == "between-commands" {
						cancel()
					} else {
						return failure
					}
				}
				return nil
			}
			_, err := s.prepareSelectedCommands(ctx, queued.CommandNames)
			if interruption == "between-commands" && !errors.Is(err, context.Canceled) || interruption == "completion-save" && !errors.Is(err, failure) {
				t.Fatalf("interrupted preparation: %v", err)
			}
			assertCommandEffect(t, root, "first-invoked", "x")
			if _, err := os.Stat(filepath.Join(root, "second-invoked")); !os.IsNotExist(err) {
				t.Fatalf("interrupted preparation executed second command: %v", err)
			}
			s.clientMutations.faults.BeforeEffectSnapshotRename = nil
			id := s.ID()
			s.Close()
			meta, err := schema.LoadSessionMeta(root, id)
			if err != nil {
				t.Fatal(err)
			}
			client := llm.NewClient()
			client.Register(adapter)
			restored, err := RestoreSessionFromMetaWithConfig(client, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), meta, RestoreSessionConfig{StateDir: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restored.Close)
			_, stop := captureEvents(restored)
			defer stop()
			if _, ran, err := restored.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
				t.Fatalf("restore consumption: ran=%v err=%v", ran, err)
			}
			assertCommandEffect(t, root, "first-invoked", "x")
			turn := findClientMutationTurn(restored, "interrupted-318", schema.TurnUserInput)
			if turn == nil || turn.CommandInput == nil || turn.CommandInput.OriginalText != queued.Text || !slices.Equal(turn.CommandInput.Names, queued.CommandNames) {
				t.Fatalf("restored original input/selections lost: %+v", turn)
			}
			if interruption == "between-commands" {
				assertCommandEffect(t, root, "second-invoked", "x")
				if !strings.Contains(turn.Message.Text(), "FIRST_RESTORE_318") || !strings.Contains(turn.Message.Text(), "SECOND_RESTORE_318") {
					t.Fatalf("partial completed bytes missing: %+v", turn.Message)
				}
			} else {
				var failureTurn *schema.Turn
				restored.mu.Lock()
				for _, candidate := range restored.history {
					if candidate.Kind == schema.TurnFailure && candidate.Error != nil && strings.Contains(candidate.Error.Message, "uncertain") {
						failureTurn = &candidate
					}
				}
				restored.mu.Unlock()
				if failureTurn == nil || failureTurn.Error == nil || !strings.Contains(failureTurn.Error.Message, "uncertain") || len(adapter.Requests()) != 0 {
					t.Fatalf("restored uncertain result not visible or dispatched: turn=%+v requests=%d", failureTurn, len(adapter.Requests()))
				}
				if _, err := os.Stat(filepath.Join(root, "second-invoked")); !os.IsNotExist(err) {
					t.Fatalf("uncertain input continued effects: %v", err)
				}
				if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "healthy-restored-318", Input: []appwire.InputItem{{Type: "text", Text: "HEALTHY_RESTORED_318"}}}); err != nil {
					t.Fatal(err)
				}
				if _, ran, err := restored.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran || len(adapter.Requests()) == 0 {
					t.Fatalf("healthy restored input: ran=%v err=%v requests=%d", ran, err, len(adapter.Requests()))
				}
			}
		})
	}
}

func TestClientMutationCommandPreparationCanceledShell(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pluginDir := writePluginCommand(t, "pkg", "probe", "!`printf x >> invoked; printf PARTIAL_STDOUT_318; sleep 10`")
	s, adapter := commandPreparationSession(t, root, pluginDir)
	queued := acceptedCommandPreparation(t, s, "canceled-shell-318", "CANCELED_ORIGINAL_318", "pkg:probe")
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(root); err != nil {
		t.Fatal(err)
	}
	// TRIPWIRE: a local printf normally completes in milliseconds, five seconds only bounds a broken signal.
	ctx, cancel := context.WithTimeout(withQueuedClientMutation(context.Background(), queued), 5*time.Second)
	defer cancel()
	observed := make(chan bool, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				observed <- false
				return
			case <-watcher.Errors:
				cancel()
				observed <- false
				return
			case <-watcher.Events:
				if data, err := os.ReadFile(filepath.Join(root, "invoked")); err == nil && string(data) == "x" {
					cancel()
					observed <- true
					return
				}
			}
		}
	}()
	if _, err := s.prepareSelectedCommands(ctx, queued.CommandNames); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled expansion accepted partial output: %v", err)
	}
	if !<-observed {
		t.Fatal("shell did not reach its external effect before cancellation")
	}
	assertCommandEffect(t, root, "invoked", "x")
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("uncertain retry: ran=%v err=%v", ran, err)
	}
	assertCommandEffect(t, root, "invoked", "x")
	if len(adapter.Requests()) != 0 {
		t.Fatal("canceled shell's partial output dispatched")
	}
	turn := findClientMutationTurn(s, "canceled-shell-318", schema.TurnUserInput)
	if turn == nil || turn.CommandInput == nil || turn.CommandInput.OriginalText != queued.Text || strings.Contains(turn.Message.Text(), "PARTIAL_STDOUT_318") {
		t.Fatalf("canceled input lost prompt or delivered partial body: %+v", turn)
	}
}
