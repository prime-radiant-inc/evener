package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestLiveSteeringOwnerIdentityReplaysFullTranscriptParity exercises the real
// session event bridge for both steering delivery sites. The warmup turn is
// persisted before the server is installed, so environment setup and cold
// hydration cannot hide a missing owner on a live steering append. Every
// projected item is compared across the live and cold paths, including round
// timings and answers after the steering carrier.
func TestLiveSteeringOwnerIdentityReplaysFullTranscriptParity(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		name := "inline"
		if delayed {
			name = "delayed"
		}
		t.Run(name, func(t *testing.T) {
			var sess *agent.Session
			adapter := &steeringOwnerReplayAdapter{delayed: delayed}
			sess = newMutationReplaySessionWithAdapter(t, adapter)
			adapter.sess = sess

			warmupDone := make(chan struct{})
			turnDone := make(chan struct{}, 2)
			var bridgeMu sync.Mutex
			live := false
			var srv *Server
			go sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
				bridgeMu.Lock()
				enabled, current := live, srv
				bridgeMu.Unlock()
				if !enabled {
					if ev.Kind == events.EventSessionEnd {
						close(warmupDone)
					}
					return
				}
				if current != nil {
					current.RecordAppEvent(ev)
					if ev.Kind == events.EventSessionEnd {
						turnDone <- struct{}{}
					}
				}
			}, func() {})

			_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
				ClientMutationID:   "warmup",
				ExpectedInstanceID: sess.ID(),
				Input:              []appwire.InputItem{{Type: "text", Text: "warmup environment"}},
			})
			if err != nil {
				t.Fatalf("warmup start: %v", err)
			}
			if _, _, err := sess.ProcessClientMutationStart(context.Background(), nil); err != nil {
				t.Fatalf("warmup process: %v", err)
			}
			<-warmupDone

			server := NewServer(ServerConfig{})
			installTranscriptIdentity(t, server, sess.ID(), sess.TranscriptPath())
			bridgeMu.Lock()
			srv = server
			live = true
			bridgeMu.Unlock()
			start, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
				ClientMutationID:   "actual-start",
				ExpectedInstanceID: sess.ID(),
				Input:              []appwire.InputItem{{Type: "text", Text: "actual question"}},
			})
			if err != nil {
				t.Fatalf("actual start: %v", err)
			}
			if _, _, err := sess.ProcessClientMutationStart(context.Background(), nil); err != nil {
				t.Fatalf("actual process: %v", err)
			}
			<-turnDone
			if delayed {
				if _, ran, err := sess.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
					t.Fatalf("delayed carrier: ran=%v err=%v", ran, err)
				}
				<-turnDone
			}

			if adapter.steerStableID == "" {
				t.Fatalf("adapter never accepted steering (calls=%d)", adapter.calls)
			}
			assertSteeringOwnerInTranscript(t, sess.TranscriptPath(), start.Turn.ID, adapter.steerStableID, delayed)
			full, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
			if err != nil {
				t.Fatalf("cold projection: %v", err)
			}
			if got := len(steeringReplayIdentities(full)); got != 1 {
				t.Fatalf("projected steering items = %d, want one accepted mutation", got)
			}
			if !delayed {
				assertInlineTimingAndLaterAnswer(t, full)
			}
			read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sess.ID(), IncludeTurns: true})
			if err != nil {
				t.Fatalf("live read: %v", err)
			}
			liveIdentities := replayItemIdentities(read.Thread.Turns)
			coldIdentities := replayItemIdentities(full)
			if !reflect.DeepEqual(liveIdentities, coldIdentities) {
				t.Fatalf("live projection identities differ from cold projection:\nlive=%#v\ncold=%#v", liveIdentities, coldIdentities)
			}

			initial, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sess.ID(), IncludeTurns: true, ItemLimit: 1})
			if err != nil {
				t.Fatalf("tiny initial read: %v", err)
			}
			paged := initial.Thread.Turns
			cursor := initial.OlderCursor
			for cursor != "" {
				page, err := srv.handleAppThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{Ref: "local:" + sess.ID(), Cursor: cursor, ItemLimit: 1})
				if err != nil {
					t.Fatalf("tiny page: %v", err)
				}
				paged = append(page.Data, paged...)
				cursor = page.NextCursor
			}
			pagedIdentities := replayItemIdentities(paged)
			if !reflect.DeepEqual(pagedIdentities, coldIdentities) {
				t.Fatalf("tiny page identities differ from cold projection:\npaged=%#v\ncold=%#v", pagedIdentities, coldIdentities)
			}
		})
	}
}

type steeringOwnerReplayAdapter struct {
	sess          *agent.Session
	delayed       bool
	calls         int
	steerStableID string
}

func (a *steeringOwnerReplayAdapter) Name() string { return "openai" }

func (a *steeringOwnerReplayAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	if !requestHasTool(req, "communicate") {
		return llm.Response{Provider: a.Name(), Model: req.Model, Message: llm.Assistant("warmup")}, nil
	}
	a.calls++
	if a.calls >= 2 && a.steerStableID == "" {
		response, err := a.sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID:   "steer-stable",
			ExpectedInstanceID: a.sess.ID(),
			Input:              []appwire.InputItem{{Type: "text", Text: "steering input"}},
		})
		if err != nil {
			return llm.Response{}, err
		}
		a.steerStableID = response.Receipt.TurnID
	}
	response := steeringOwnerCommunicateResponse(a.delayed || a.calls != 2, a.calls)
	response.Provider = a.Name()
	response.Model = req.Model
	return response, nil
}

func (a *steeringOwnerReplayAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func steeringOwnerCommunicateResponse(endTurn bool, call int) llm.Response {
	args, _ := json.Marshal(map[string]any{"message": "done", "end_turn": endTurn, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}})
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("steering-call-%d", call), Name: "communicate", Arguments: args, Type: "function"}}}}}
}

func assertSteeringOwnerInTranscript(t *testing.T, path, startedID, steerStableID string, delayed bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	var found *schema.Turn
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		entry, decodeErr := transcript.DecodeEntry(sc.Bytes())
		if decodeErr == nil && entry.Turn.Kind == schema.TurnSteering && entry.Turn.ClientMutationID == "steer-stable" {
			steering := entry.Turn
			found = &steering
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	if found == nil {
		t.Fatal("transcript has no steering entry")
	}
	wantOwner := startedID
	if delayed {
		wantOwner = steerStableID
	}
	if steerStableID == "" {
		t.Fatal("steering receipt has no stable turn id")
	}
	if found.OwningTurnID != wantOwner {
		t.Fatalf("steering owner=%q, want %q", found.OwningTurnID, wantOwner)
	}
	if found.StableTurnID != steerStableID {
		t.Fatalf("steering stable id=%q, want %q", found.StableTurnID, steerStableID)
	}
}

type steeringReplayIdentity struct {
	TurnID, Type, Key string
	Position          appwire.ThreadItemPosition
}

func steeringReplayIdentities(turns []appwire.Turn) []steeringReplayIdentity {
	var result []steeringReplayIdentity
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type != "steering" {
				continue
			}
			position := appwire.ThreadItemPosition{}
			if item.Position != nil {
				position = *item.Position
			}
			result = append(result, steeringReplayIdentity{turn.ID, item.Type, item.TranscriptKey, position})
		}
	}
	return result
}

type replayItemIdentity struct {
	TurnID, Type, Key string
	Position          appwire.ThreadItemPosition
	Text, Description string
	EventKind         appwire.ThreadItemEventKind
	Status            string
	Raw               any
}

func replayItemIdentities(turns []appwire.Turn) []replayItemIdentity {
	var result []replayItemIdentity
	for _, turn := range turns {
		for _, item := range turn.Items {
			position := appwire.ThreadItemPosition{}
			if item.Position != nil {
				position = *item.Position
			}
			result = append(result, replayItemIdentity{
				TurnID: turn.ID, Type: item.Type, Key: item.TranscriptKey, Position: position,
				Text: item.Text, Description: item.Description, EventKind: item.EventKind,
				Status: item.Status, Raw: semanticReplayJSON(item.Raw),
			})
		}
	}
	return result
}

func semanticReplayJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func assertInlineTimingAndLaterAnswer(t *testing.T, turns []appwire.Turn) {
	t.Helper()
	items := replayItemIdentities(turns)
	steeringIndex := -1
	for i, item := range items {
		if item.Type == "steering" {
			steeringIndex = i
			break
		}
	}
	if steeringIndex < 1 {
		t.Fatalf("inline replay has no preceding items before steering: %#v", items)
	}
	timingIndex := -1
	for i, item := range items[steeringIndex+1:] {
		if item.EventKind == appwire.ThreadItemEventKindRoundTimings {
			timingIndex = steeringIndex + 1 + i
			break
		}
	}
	if timingIndex < 0 {
		t.Fatalf("inline replay has no intermediate round timings after steering: %#v", items)
	}
	for _, item := range items[timingIndex+1:] {
		if item.Type == "agentMessage" && item.Text == "done" {
			return
		}
	}
	t.Fatalf("inline replay has no later answer after steering: %#v", items)
}
