package server

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

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
	srv := NewServer(ServerConfig{})
	installTranscriptIdentity(t, srv, sess.ID(), sess.TranscriptPath())
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
	full, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	window, _, err := apptranscript.NewTurnCache().LatestItemWindowFromFile(sess.TranscriptPath(), appTranscriptMaxLineBytes,
		apptranscript.ItemWindowOptions{ThreadRef: "local:" + sess.ID(), Limit: 40},
		func(turn schema.Turn, turnID string, turnIndex int, names map[string]string) []appwire.ThreadItem {
			return apptranscript.ProjectTurn(turnID, turnIndex, turn, names, nil, apptranscript.ToolResultOutputImages)
		})
	if err != nil {
		t.Fatal(err)
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
	live := identities(srv.appTurns.Snapshot())
	if len(live) != 8 {
		t.Errorf("live relevant items = %d, want two environment/user/reasoning/answer sets: %+v", len(live), live)
	}
	if got := identities(full); !reflect.DeepEqual(live, got) {
		t.Errorf("live/cold identity differs:\nlive: %+v\ncold: %+v", live, got)
	}
	var bounded []appwire.Turn
	for _, candidate := range window.Candidates {
		bounded = append(bounded, appwire.Turn{ID: candidate.Item.TurnID, Items: []appwire.ThreadItem{candidate.Item}})
	}
	if got := identities(bounded); !reflect.DeepEqual(live, got) {
		t.Errorf("live/indexed identity differs:\nlive: %+v\nindexed: %+v", live, got)
	}
}
