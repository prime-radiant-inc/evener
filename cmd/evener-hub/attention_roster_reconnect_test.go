package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type rosterReconnectCall struct {
	request llm.Request
	reply   chan llm.Response
}

type rosterReconnectAdapter struct{ calls chan rosterReconnectCall }

func (*rosterReconnectAdapter) Name() string { return "openai" }
func (*rosterReconnectAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *rosterReconnectAdapter) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	if len(request.Tools) == 0 {
		return llm.Response{Message: llm.Assistant("roster reconnect")}, nil
	}
	call := rosterReconnectCall{request: request, reply: make(chan llm.Response, 1)}
	select {
	case a.calls <- call:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case response := <-call.reply:
		return response, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

type rosterReconnectClient struct {
	appwire.SessionHubClient
	life   context.Context
	broker *attentionBroker
}

func (c *rosterReconnectClient) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Ref: "local:manager", InstanceID: "manager", Manager: true}, c.life.Err()
}
func (c *rosterReconnectClient) Watch(ctx context.Context, _ appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.life, cancel)
	go func() { <-ctx.Done(); stop() }()
	return watchRoster(ctx, c.broker, appwire.ManagerView{Ref: "local:manager", Manager: true})
}

// TestHubRosterWatchBrokerReconnectDiscoversWorkingWorker: the model must
// discover the worker that appeared during the reconnect. Every documented
// recovery shape after a transient watch-send journal append failure is valid
// (#5756): the healthy [B new] delta, the pending rail's merged [A idle, B
// new], a full roster_initial snapshot after a failed delta apply, and no
// frame at all when the re-register's handoff failed (recovered by one
// re-register retry on the next notification turn). Pinning only the healthy
// delta made a transient store failure look like a reconnect regression.
func TestHubRosterWatchBrokerReconnectDiscoversWorkingWorker(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	adapter := &rosterReconnectAdapter{calls: make(chan rosterReconnectCall)}
	client := llm.NewClient()
	client.Register(adapter)
	workDir := t.TempDir()
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
		StateDir: filepath.Join(t.TempDir(), "state"), Manager: true, Sandbox: "off",
		AgentsDocPath: filepath.Join(workDir, "absent-AGENTS.md"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	broker := &attentionBroker{}
	entries := map[string]appwire.AttentionEntry{"a": {ID: "a", Level: "working"}}
	identity := func(id string) (rosterIdentity, bool) { return rosterIdentity{Ref: "local:" + id}, true }
	broker.Publish(entries, identity)
	transport, disconnect := context.WithCancel(ctx)
	defer disconnect()
	sess.SetHubClient(&rosterReconnectClient{life: transport, broker: broker})
	done := make(chan error, 1)
	running := false
	defer func() {
		cancel()
		if running {
			<-done
		}
	}()
	next := func() rosterReconnectCall {
		t.Helper()
		select {
		case call := <-adapter.calls:
			return call
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return rosterReconnectCall{}
		}
	}
	startTurn := func(input string) {
		running = true
		go func() { _, err := sess.ProcessInput(ctx, input, nil); done <- err }()
	}
	finishTurn := func() {
		t.Helper()
		for {
			select {
			case err := <-done:
				running = false
				if err != nil {
					t.Fatal(err)
				}
				return
			case call := <-adapter.calls:
				// A transient watch-send failure can leave a notification queued;
				// ending the turn may run that notification turn before
				// ProcessInput returns. Answer it so the join can complete.
				call.reply <- activityRelayTool("communicate", communicateArgs("turn drained"))
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	startTurn("roster reconnect regression")
	next().reply <- rosterRegister("roster-initial")
	initial := next()
	initialFrames := rosterFrames(t, initial.request)
	// The first registration's initial persist may fail transiently: the create
	// errors, no frame is delivered, and the watch is durably ended. That is
	// documented; the reconnect round's re-register then delivers the fresh full
	// snapshot. A delivered but empty or wrong initial frame stays a failure.
	if len(initialFrames) != 0 && !rosterDiscoversFrames(initialFrames, "local:a", "working") {
		t.Fatalf("broker initial frames = %+v, want local:a in a delta or full snapshot (or no frame after a failed initial persist)", initialFrames)
	}
	initial.reply <- activityRelayTool("communicate", communicateArgs("initial received"))
	finishTurn()
	// The first snapshot has reached the model and settled. Remove the
	// transport before B appears, so B can arrive only in a reconnect initial.
	sess.SetHubClient(nil)
	disconnect()
	broker.mu.Lock()
	subscriptions := make([]*rosterSubscription, 0, len(broker.subscribers))
	for sub := range broker.subscribers {
		subscriptions = append(subscriptions, sub)
	}
	broker.mu.Unlock()
	for _, sub := range subscriptions {
		sub.Cancel()
	}
	entries["b"] = appwire.AttentionEntry{ID: "b", Level: "working"}
	broker.Publish(entries, identity)
	sess.SetHubClient(&rosterReconnectClient{life: ctx, broker: broker})
	// Re-registering uses the same durable watch and hands the broker snapshot
	// to the watch send rail; the frame may arrive in the tool-result
	// continuation or in a later notification turn of this model turn.
	startTurn("reconnect roster")
	next().reply <- rosterRegister("roster-reconnect")
	// The reconnect frame can arrive in the tool-result continuation or in a
	// later notification turn of the same model turn. Await it across calls
	// instead of pinning the first one; an end_turn=false acknowledgement keeps
	// the turn alive for the pending frame.
	awaitRoster := func() (frames []rosterFrameView, discovered bool) {
		// A missing frame must fail naming the state it waited for, not with a
		// bare context deadline after the end_turn=false acknowledgements.
		const callBound = 50
		for calls := 0; ; {
			select {
			case call := <-adapter.calls:
				calls++
				frames = rosterFrames(t, call.request)
				if rosterDiscoversFrames(frames, "local:b", "working") {
					call.reply <- activityRelayTool("communicate", communicateArgs("reconnect discovered"))
					return frames, true
				}
				if calls >= callBound {
					t.Fatalf("roster frame local:b never arrived after %d model calls; last frames = %+v", calls, frames)
				}
				call.reply <- rosterContinue("awaiting reconnect roster")
			case err := <-done:
				running = false
				if err != nil {
					t.Fatal(err)
				}
				return frames, false
			case <-ctx.Done():
				t.Fatalf("roster frame local:b never arrived; last frames = %+v: %v", frames, ctx.Err())
			}
		}
	}
	_, discovered := awaitRoster()
	if !discovered {
		// A failed reconnect handoff can durably end the watch with no frame at
		// all. Drive the one documented re-register retry on its own input; the
		// drain loop never runs two notification turns in a row.
		startTurn("reconnect roster retry")
		next().reply <- rosterRegister("roster-reconnect-retry")
		frames, retried := awaitRoster()
		if !retried {
			t.Fatalf("broker reconnect frames after one re-register = %+v, want local:b as a working worker in a delta or full snapshot", frames)
		}
	}
	finishTurn()
}

func rosterRegister(toolCallID string) llm.Response {
	response := activityRelayTool("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}})
	response.Message.Content[0].ToolCall.ID = toolCallID
	return response
}

// rosterContinue acknowledges without ending the turn, so a frame the watch
// send rail has not delivered yet can arrive in the next notification turn.
func rosterContinue(message string) llm.Response {
	args := communicateArgs(message)
	args["end_turn"] = false
	return activityRelayTool("communicate", args)
}

type rosterFrameView struct {
	events  []appwire.RosterEvent
	initial bool
	resync  bool
}

// rosterFrames parses roster segments at line granularity. A marker line
// applies to the roster_events segment that follows it, so the pending rail's
// roster_initial marker pairs with the merged delta instead of being treated
// as a separate frame.
func rosterFrames(t *testing.T, request llm.Request) []rosterFrameView {
	t.Helper()
	var frames []rosterFrameView
	for _, message := range request.Messages {
		initial, resync := false, false
		for line := range strings.SplitSeq(message.Text(), "\n") {
			if value, ok := strings.CutPrefix(line, "roster_initial: "); ok {
				initial = strings.TrimSpace(value) == "true"
				continue
			}
			if value, ok := strings.CutPrefix(line, "roster_resync: "); ok {
				resync = strings.TrimSpace(value) == "true"
				continue
			}
			payload, ok := strings.CutPrefix(line, "roster_events: ")
			if !ok {
				continue
			}
			var entries []appwire.RosterEvent
			if err := json.Unmarshal([]byte(payload), &entries); err != nil {
				t.Fatalf("parse roster frame: %v", err)
			}
			frames = append(frames, rosterFrameView{events: entries, initial: initial, resync: resync})
			initial, resync = false, false
		}
	}
	return frames
}

// rosterDiscoversFrames accepts every documented recovery shape: a delta that
// names the worker new, or a full initial/resync snapshot that carries it. The
// pending rail's merge can render the reconnect delta as [A idle, B new], and a
// failed delta apply recovers with a full snapshot [A idle, B idle]; both tell
// the model about B. The discovering frame must carry exactly one row for the
// ref, and a snapshot row that says disappeared or error is not discovery.
func rosterDiscoversFrames(frames []rosterFrameView, ref, level string) bool {
	for _, frame := range frames {
		matches := 0
		announced := false
		for _, event := range frame.events {
			if event.Ref != ref || event.Level != level {
				continue
			}
			matches++
			announced = event.Kind == "new" || ((frame.initial || frame.resync) && event.Kind != "disappeared" && event.Kind != "error")
		}
		if matches == 1 && announced {
			return true
		}
	}
	return false
}
