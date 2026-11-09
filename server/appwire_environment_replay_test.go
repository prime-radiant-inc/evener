package server

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestDescendantWorkingDirectoryStartChangeReplayAndOwnerFence(t *testing.T) {
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	srv.SetAppIdentity("local", "root")
	srv.RecordAppEvent(threadEvent("root", events.EnvironmentChangedData{WorkingDir: "/root"}))
	read := func(id, want string) {
		t.Helper()
		response, err := srv.handleAppThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + id})
		if err != nil || response.Thread.ID != id || response.Thread.CWD != want {
			t.Fatalf("read %s = %+v, %v, want cwd %q", id, response.Thread, err, want)
		}
	}
	for _, id := range []string{"child", "sibling"} {
		srv.RecordDescendantAppEvent("root", threadEvent(id, events.SessionStartData{WorkingDir: "/" + id}))
		read(id, "/"+id)
	}
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.EnvironmentChangedData{WorkingDir: "/child/worktree"}))
	read("child", "/child/worktree")
	read("sibling", "/sibling")
	read("root", "/root")
	resyncs := 0
	for _, n := range srv.AppNotificationsAfter(0, "child") {
		if n.Notification.Method == appwire.NotifyThreadStarted {
			p := notificationParams[appwire.ThreadStartedParams](t, n)
			if p.Thread.CWD != "/child" {
				t.Fatalf("replayed start mutated: %+v", p)
			}
		}
		if n.Notification.Method == appwire.NotifyEvenerThreadResync {
			p := notificationParams[appwire.ThreadResyncParams](t, n)
			if p.ThreadID != "child" || p.Ref != "local:child" {
				t.Fatalf("resync targets wrong reader: %+v", p)
			}
			resyncs++
		}
	}
	if resyncs != 1 {
		t.Fatalf("child cwd resync count = %d", resyncs)
	}
	srv.SetAppIdentity("local", "replacement")
	srv.RecordDescendantAppEvent("replacement", threadEvent("child", events.SessionStartData{WorkingDir: "/restored/child", Restored: true}))
	replay := srv.AppNotificationsAfter(0, "child")
	cut := replay[len(replay)-1].Seq
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.EnvironmentChangedData{WorkingDir: "/stale"}))
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionStartData{WorkingDir: "/stale-start"}))
	read("child", "/restored/child")
	if replay := srv.AppNotificationsAfter(cut, "child"); len(replay) != 0 {
		t.Fatalf("old owner published after replacement: %+v", replay)
	}
}

type environmentReplayAdapter struct{ mutationProjectionAdapter }

func (a *environmentReplayAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	response, err := a.mutationProjectionAdapter.Complete(ctx, req)
	if err == nil && requestHasTool(req, "communicate") {
		response.Message.Content = append([]llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning"}}}, response.Message.Content...)
	}
	return response, err
}

func (a *environmentReplayAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	response, err := a.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	stream := llm.NewChanStream(nil)
	stream.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
	if requestHasTool(req, "communicate") {
		stream.Send(llm.StreamEvent{Type: llm.StreamEventReasoningStart})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "reasoning"})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventReasoningEnd})
		for _, part := range response.Message.Content {
			if part.ToolCall != nil {
				stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: part.ToolCall})
				stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: part.ToolCall})
			}
		}
	}
	stream.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &response})
	stream.CloseSend()
	return stream, nil
}

func TestEnvironmentChangesPreserveLiveTranscriptItemIdentity(t *testing.T) {
	adapter := &environmentReplayAdapter{mutationProjectionAdapter{blockAt: 100}}
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", sess.StateDir()}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "--initial-branch=first")
	git("-c", "user.name=Evener Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Environment fixture")
	srv := NewServer(ServerConfig{AppReplaySize: parityReplaySize})
	t.Cleanup(srv.Close)
	published := watchHistoryPublications(t)
	wireTranscriptHistory(t, sess, srv)
	initial, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{ThreadID: sess.ID(), IncludeTurns: true, ItemLimit: appwire.TranscriptItemPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	environmentCount := 0
	for input := range 2 {
		if input == 1 {
			git("symbolic-ref", "HEAD", "refs/heads/second")
		}
		_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
			ClientMutationID: fmt.Sprintf("environment-input-%d", input), ExpectedInstanceID: sess.ID(),
			Input: []appwire.InputItem{{Type: "text", Text: fmt.Sprintf("input-%d", input)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ran, err := sess.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran {
			t.Fatalf("process input: ran=%v err=%v", ran, err)
		}
	drain:
		for {
			select {
			case event := <-sess.Events():
				if event.Kind == events.EventEnvironment {
					environmentCount++
				}
				srv.RecordAppEvent(event)
			default:
				break drain
			}
		}
	}
	if environmentCount != 2 {
		t.Errorf("environment events = %d, want initial context and branch change", environmentCount)
	}
	published.await(t, srv.appHistoryForID(sess.ID()), sess.TranscriptRecordedLength())
	client := newHistoryClient(initial)
	for _, n := range srv.appNotifier.ReplayAfter(0, "") {
		if n.Notification.Method == appwire.NotifyHistoryUpdated {
			client.apply(t, notificationParams[appwire.HistoryUpdatedParams](t, n))
		}
	}
	type identity struct {
		TurnID, Type, Key string
		Position          appwire.ThreadItemPosition
	}
	identities := func(turns []appwire.Turn) []identity {
		var result []identity
		for _, turn := range turns {
			for _, item := range turn.Items {
				if item.Type != "userMessage" && item.Type != "agentMessage" && item.Type != "reasoning" && item.EventKind != appwire.ThreadItemEventKindEnvironment {
					continue
				}
				if item.Position == nil || item.TranscriptKey == "" {
					t.Fatalf("item lacks durable identity: %+v", item)
				}
				result = append(result, identity{turn.ID, item.Type, item.TranscriptKey, *item.Position})
			}
		}
		return result
	}
	live := identities(client.turnsInOrder())
	if len(live) != 8 {
		t.Errorf("live relevant items = %d, want two environment/user/reasoning/answer sets: %+v", len(live), live)
	}
	if got := identities(fileHistoryTurns(t, sess.TranscriptPath())); !reflect.DeepEqual(live, got) {
		t.Errorf("live/indexed identity differs:\nlive: %+v\nindexed: %+v", live, got)
	}
}
