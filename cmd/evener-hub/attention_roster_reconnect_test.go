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
	go func() { _, err := sess.ProcessInput(ctx, "roster reconnect regression", nil); done <- err }()
	running := true
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
	frame := func(call rosterReconnectCall) []appwire.RosterEvent {
		t.Helper()
		var latest []appwire.RosterEvent
		for _, message := range call.request.Messages {
			for line := range strings.SplitSeq(message.Text(), "\n") {
				if payload, ok := strings.CutPrefix(line, "roster_events: "); ok {
					if err := json.Unmarshal([]byte(payload), &latest); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		return latest
	}
	watch := activityRelayTool("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}})
	next().reply <- watch
	initial := next()
	if got := frame(initial); len(got) != 1 || got[0].Ref != "local:a" || got[0].Kind != "idle" {
		t.Fatalf("broker initial frame = %+v", got)
	}
	initial.reply <- activityRelayTool("communicate", communicateArgs("initial received"))
	select {
	case err := <-done:
		running = false
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
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
	// Re-registering uses the same durable watch and forces a synchronous
	// handoff of the broker snapshot before the next model request.
	rebind := activityRelayTool("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"scope": "top_level"}})
	rebind.Message.Content[0].ToolCall.ID = "roster-reconnect"
	running = true
	go func() { _, err := sess.ProcessInput(ctx, "reconnect roster", nil); done <- err }()
	next().reply <- rebind
	reconnected := next()
	if got := frame(reconnected); len(got) != 1 || got[0].Ref != "local:b" || got[0].Level != "working" || got[0].Kind != "new" {
		t.Fatalf("broker reconnect delta = %+v, want only B as new", got)
	}
}
