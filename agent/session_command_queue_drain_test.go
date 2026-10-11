package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestSelectDrainNextActionRunsACommandOnlyQueuedEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   drainInputs
		want drainAction
		skip bool
	}{
		{"command-only entry runs", drainInputs{QueuedCommands: 1}, runQueued, false},
		{"command-only entry runs while awaiting", drainInputs{Awaiting: true, QueuedCommands: 1}, runQueued, true},
		{"command-only entry outranks a notification", drainInputs{NotificationsPending: true, QueuedCommands: 2}, runQueued, false},
		{"a follow-up still outranks it", drainInputs{FollowUp: "FOLLOWUP_3660", QueuedCommands: 1}, runFollowUp, false},
		{"awaiting holds a follow-up but runs the command", drainInputs{Awaiting: true, FollowUp: "FOLLOWUP_3660", QueuedCommands: 1}, runQueued, true},
		{"command-only entry outranks a deferred continuation", drainInputs{HaveDeferredCont: true, QueuedCommands: 1}, runQueued, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, skip := selectDrainNextAction(tc.in)
			if got != tc.want || skip != tc.skip {
				t.Fatalf("selectDrainNextAction(%+v) = (%v,%v), want (%v,%v)", tc.in, got, skip, tc.want, tc.skip)
			}
		})
	}
}

// The completed-turn drain must not claim a command-only entry and then treat
// it as empty. The queue, command shell expansion and transcript are real;
// only the provider response is scripted.
func TestProcessInputCompletedTurnDrainsCommandOnlyQueuedInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pluginDir := writePluginCommand(t, "pkg", "probe", "BODY_DRAIN_3660[$ARGUMENTS]|[$1] !`printf x >> invoked; printf OUTPUT_DRAIN_3660`")
	const mutationID = "command-only-drain-3660"
	const body = "BODY_DRAIN_3660[]|[] OUTPUT_DRAIN_3660"
	started := make(chan struct{})
	release := make(chan struct{})
	finishProvider := sync.OnceFunc(func() { close(release) })
	calls := 0
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return toolCallResponse(communicateCall("first-3660", "FIRST_OUTPUT_3660"))
		}
		return toolCallResponse(communicateCall("queued-3660", "QUEUED_OUTPUT_3660"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root), withProfile(newAnthropicProfile("claude-test")),
		withConfig(SessionConfig{StateDir: root, PluginDirs: []string{pluginDir}}), withoutGitSnapshot())
	if _, ok := s.pluginCommands["pkg:probe"]; !ok {
		t.Fatal("fixture canonical command pkg:probe was not discovered")
	}
	_, stop := captureEvents(s)
	defer stop()
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(finished)
		_, err := s.ProcessInput(context.Background(), "FIRST_INPUT_3660", nil)
		result <- err
	}()
	t.Cleanup(func() {
		finishProvider()
		<-finished
	})
	select {
	case <-started:
	case <-finished:
		t.Fatalf("initial ProcessInput ended before the provider request: %v", <-result)
	}
	params := appwire.TurnQueueParams{ClientMutationID: mutationID, Input: []appwire.InputItem{{Type: "command", Name: "pkg:probe"}}}
	if _, err := s.AcceptClientMutationQueue(params); err != nil {
		t.Fatalf("queue during active provider request: %v", err)
	}
	readSnapshot := func() clientMutationSnapshot {
		t.Helper()
		data, err := os.ReadFile(clientMutationFilePath(root, s.ID()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := decodeClientMutationSnapshot(data, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	accepted := readSnapshot()
	if len(accepted.InputQueue) != 1 || accepted.InputQueue[0].ClientMutationID != mutationID || !reflect.DeepEqual(accepted.InputQueue[0].Input, params.Input) {
		t.Fatalf("durable queue did not retain exactly the command-only input: %+v", accepted.InputQueue)
	}
	finishProvider()
	<-finished
	if err := <-result; err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	snapshot := readSnapshot()
	if pending, ok := snapshot.PendingExecutions[mutationID]; ok {
		t.Errorf("completed-turn drain left mutation pending: state=%q input=%+v activeTurnID=%q", pending.ExecutionState, pending.Input, snapshot.ActiveTurnID)
	}
	if len(snapshot.InputQueue) != 0 || snapshot.ActiveTurnID != "" || snapshot.Journal[mutationID].ExecutionState != "terminal" {
		t.Errorf("durable drain not settled: queue=%+v activeTurnID=%q journalState=%q", snapshot.InputQueue, snapshot.ActiveTurnID, snapshot.Journal[mutationID].ExecutionState)
	}
	if invoked, err := os.ReadFile(filepath.Join(root, "invoked")); err != nil || string(invoked) != "x" {
		t.Errorf("real command must execute exactly once: invoked=%q err=%v", invoked, err)
	}
	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Errorf("completed-turn drain dispatched %d provider requests, want initial and command turns", len(requests))
	} else if data := strings.Join(userMessageTexts(requests[1]), "\n"); strings.Count(data, body) != 1 {
		t.Errorf("next real turn lost or duplicated opaque command expansion: %q", data)
	}
	transcript, err := readTranscriptFull(s.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []schema.Turn
	for _, entry := range transcript.Entries {
		if entry.Turn.Kind == schema.TurnUserInput {
			inputs = append(inputs, entry.Turn)
		}
	}
	if len(inputs) != 2 {
		t.Fatalf("real transcript has %d user turns, want initial then queued command", len(inputs))
	}
	turn := inputs[1]
	if turn.ClientMutationID != mutationID || turn.TurnID == "" || turn.StableTurnID != turn.TurnID || turn.TurnID != snapshot.Journal[mutationID].StableTurnID || turn.TurnID == inputs[0].TurnID {
		t.Errorf("queued command was not the next distinct durable turn: first=%q queued=%+v journal=%+v", inputs[0].TurnID, turn, snapshot.Journal[mutationID])
	}
	if turn.CommandInput == nil || turn.CommandInput.OriginalText != "" || !slices.Equal(turn.CommandInput.Names, []string{"pkg:probe"}) || turn.SkillState != nil {
		t.Errorf("disk transcript lost command-only intent: %+v", turn)
	}
	if strings.Count(turn.Message.Text(), body) != 1 {
		t.Errorf("disk transcript lost or duplicated opaque command expansion: %+v", turn.Message)
	}
}
