package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Only the hub boundary is scripted. Input acceptance and replay use the real
// worker's durable mutation owner, including its persisted pending execution.
type managerCreationClient struct {
	worker  *Session
	stage   string
	creates int
	starts  []appwire.TurnStartParams
}

func (*managerCreationClient) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Ref: "local:manager", InstanceID: "manager-instance", Manager: true}, nil
}
func (c *managerCreationClient) Invoke(_ context.Context, req appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	if req.Create != nil {
		c.creates++
		if c.stage == "createFailed" {
			return appwire.SessionOperationResponse{}, appwire.WireError{Code: appwire.CodeConflict, Data: appwire.ErrorData{MutationOutcome: appwire.MutationOutcomeNotAccepted}}
		}
		return appwire.SessionOperationResponse{Create: &appwire.ThreadStartResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:" + c.worker.ID(), InstanceID: c.worker.ID()}}}}, nil
	}
	if req.Start != nil {
		c.starts = append(c.starts, *req.Start)
		if c.stage == "notAccepted" {
			return appwire.SessionOperationResponse{}, appwire.WireError{Code: appwire.CodeConflict, Data: appwire.ErrorData{MutationOutcome: appwire.MutationOutcomeNotAccepted}}
		}
		response, err := c.worker.AcceptClientMutationStart(*req.Start)
		if err != nil {
			return appwire.SessionOperationResponse{}, err
		}
		if len(c.starts) == 1 {
			if c.stage == "unknown" {
				return appwire.SessionOperationResponse{}, errors.New("dropped reply")
			}
			if c.stage == "missingReceipt" {
				return appwire.SessionOperationResponse{}, nil
			}
		}
		return appwire.SessionOperationResponse{Start: &response}, nil
	}
	return appwire.SessionOperationResponse{}, errors.New("unexpected operation")
}

func TestManagerCreateRejectsInvalidWatchSelectorsBeforeCreate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		watch map[string]any
	}{
		{name: "empty selector set", watch: map[string]any{"events": []string{}}},
		{name: "unknown event", watch: map[string]any{"events": []string{"unknown"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := newTestSession(t)
			client := &managerCreationClient{worker: worker}
			root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
			root.SetHubClient(client)
			_, err := root.createHubDelegate(t.Context(), map[string]any{
				"prompt": "worker task",
				"hub": map[string]any{
					"cwd": t.TempDir(), "model": "openai/gpt-5.2", "watch": tc.watch,
				},
			})
			if client.creates != 0 {
				t.Fatalf("Create calls=%d, want zero before selector rejection", client.creates)
			}
			if err == nil {
				t.Fatal("invalid watch selectors were accepted")
			}
		})
	}
}

func TestManagerCreateAcceptsCompletionOnlyWatch(t *testing.T) {
	worker := newTestSession(t)
	client := &managerCreationClient{worker: worker}
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	root.SetHubClient(client)
	value, err := root.createHubDelegate(t.Context(), map[string]any{
		"prompt": "worker task",
		"hub": map[string]any{
			"cwd": t.TempDir(), "model": "openai/gpt-5.2",
			"watch": map[string]any{"events": []string{}, "completion": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := value.(managerCreateOutcome)
	if !ok || out.Watch.Status != "attached" || out.Watch.WatchID == "" {
		t.Fatalf("completion-only watch was not attached: %#v", value)
	}
	if client.creates != 1 || len(client.starts) != 1 {
		t.Fatalf("Create calls=%d, Start calls=%d; want one each", client.creates, len(client.starts))
	}
}

func (c *managerCreationClient) Watch(ctx context.Context, req appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	if c.stage == "watchFailed" {
		return nil, appwire.InvalidParams("fixture refusal")
	}
	stream := make(chan appwire.SessionWatchUpdate, 1)
	stream <- appwire.SessionWatchUpdate{Snapshot: &appwire.ThreadReadResponse{BootGeneration: "worker-generation", Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: req.Ref, InstanceID: c.worker.ID()}}}, Metadata: appwire.SessionObservationMetadata{Ref: req.Ref, InstanceID: c.worker.ID(), GenerationID: "worker-generation"}}
	go func() { <-ctx.Done(); close(stream) }()
	return stream, nil
}

func TestManagerCreationReturnsPersistedMutationIdentity(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"submitted", "unknown", "missingReceipt", "notAccepted", "watchFailed", "createFailed"} {
		t.Run(stage, func(t *testing.T) {
			worker := newTestSession(t)
			client := &managerCreationClient{worker: worker, stage: stage}
			adapter := newMessagingAdapter()
			adapter.script("root", func(llm.Request) llm.Response {
				return delegateToolCall("create-worker", map[string]any{"prompt": "worker-input", "hub": map[string]any{"cwd": t.TempDir(), "model": "openai/gpt-5.2", "watch": map[string]any{"events": []string{"communicate"}, "completion": true}}})
			})
			root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.Manager = true })
			root.SetHubClient(client)
			if _, err := root.ProcessInput(t.Context(), "create-input", nil); err != nil {
				t.Fatal(err)
			}
			data, err := readTranscriptFull(t.Context(), transcriptPath(root.stateDir, root.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Created managerCreateStage `json:"created"`
				Watch   managerWatchStage  `json:"watch"`
				Input   managerInputStage  `json:"input"`
			}
			found := false
			for _, entry := range data.Entries {
				for _, part := range entry.Turn.Message.Content {
					if part.ToolResult != nil && part.ToolResult.ToolCallID == "create-worker" {
						body, err := json.Marshal(part.ToolResult.Content)
						if text, ok := part.ToolResult.Content.(string); ok {
							body = []byte(text)
						}
						if err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(body, &out); err != nil {
							t.Fatal(err)
						}
						found = true
					}
				}
			}
			if !found {
				t.Fatal("creation tool result not persisted")
			}
			wantInput := stage
			if stage == "missingReceipt" {
				wantInput = "unknown"
			}
			if stage == "watchFailed" || stage == "createFailed" {
				wantInput = "notSubmitted"
			}
			if out.Input.Status != wantInput {
				t.Fatalf("input status=%q want %q", out.Input.Status, wantInput)
			}
			clientMutationID := out.Input.ClientMutationID
			if len(client.starts) == 0 {
				if clientMutationID != "" {
					t.Fatal("returned mutation ID before generation")
				}
				if stage == "watchFailed" && (out.Created.Ref == "" || out.Watch.Status != "failed") {
					t.Fatalf("lost watch failure stage: %+v", out)
				}
				if stage == "createFailed" && out.Created.Status != "notAccepted" {
					t.Fatalf("lost creation failure: %+v", out)
				}
				return
			}
			if clientMutationID == "" || clientMutationID != client.starts[0].ClientMutationID {
				t.Fatalf("persisted mutation ID=%q, submitted=%q", clientMutationID, client.starts[0].ClientMutationID)
			}
			if out.Created.Ref == "" || out.Created.InstanceID != worker.ID() || out.Watch.Status != "attached" {
				t.Fatalf("lost recovery identity: %+v", out)
			}
			if stage == "unknown" || stage == "missingReceipt" {
				value, err := root.invokeHubInput(t.Context(), map[string]any{"to": out.Created.Ref, "input_mode": "start", "message": "worker-input", "client_mutation_id": clientMutationID, "expected_instance_id": out.Created.InstanceID})
				if err != nil {
					t.Fatal(err)
				}
				response, ok := value.(appwire.SessionOperationResponse)
				if !ok || response.Start == nil || response.Start.Receipt.Disposition != appwire.MutationDispositionReplayed {
					t.Fatalf("retry did not replay: %+v", value)
				}
				snapshot := worker.clientMutations.snapshot()
				if len(snapshot.PendingExecutions) != 1 || len(snapshot.Journal) != 1 {
					t.Fatalf("retry duplicated work: pending=%d journal=%d", len(snapshot.PendingExecutions), len(snapshot.Journal))
				}
			}
		})
	}
}
