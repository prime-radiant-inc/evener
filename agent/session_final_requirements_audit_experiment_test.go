package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func TestFinalRequirementsAudit_DefersFirstResultAndRunsAsFinalTask(t *testing.T) {
	t.Parallel()
	const original = "compile in /app/sqlite\nkeep this 'quoted' requirement"
	const auditPrompt = "The original task assigned to you was '" + original + "' - Manually check and confirm that each requirement was met and report that in your final response"

	taskListCall := func(id, arguments string) llm.Response {
		return toolCallResponse(llm.ToolCallData{
			ID:        id,
			Name:      "task_list",
			Arguments: json.RawMessage(arguments),
		})
	}
	sess := newSession(t,
		withDir(t.TempDir()),
		withConfig(SessionConfig{
			NonInteractive:                     true,
			ExperimentalFinalRequirementsAudit: true,
		}),
		withSteps(
			func(llm.Request) llm.Response {
				return taskListCall("append-work", `{"action":"append","tasks":[{"type":"implement","description":"Build SQLite","prompt":"compile the requested artifact"}]}`)
			},
			func(llm.Request) llm.Response {
				return taskListCall("start-work", `{"action":"update","updates":[{"id":1,"status":"in_progress"}]}`)
			},
			func(llm.Request) llm.Response {
				return taskListCall("finish-work", `{"action":"update","updates":[{"id":1,"status":"done"}]}`)
			},
			func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("premature-final", "missed requirement"))
			},
			func(req llm.Request) llm.Response {
				if !finalAuditRequestContainsText(req, auditPrompt) {
					t.Fatalf("post-final request does not contain the verbatim audit prompt: %#v", req.Messages)
				}
				return taskListCall("finish-audit", `{"action":"update","updates":[{"id":2,"status":"done"}]}`)
			},
			func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("audited-final", "audited final"))
			},
		),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := sess.ProcessInput(ctx, original, nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if output != "audited final" {
		t.Fatalf("ProcessInput returned %q, want the post-audit result", output)
	}

	tasks := sess.getOrCreateTaskStore().View()
	if len(tasks) != 2 {
		t.Fatalf("task count = %d, want the original task plus one audit: %#v", len(tasks), tasks)
	}
	if tasks[0].Status != taskpkg.TaskDone || tasks[1].Status != taskpkg.TaskDone {
		t.Fatalf("task statuses = %q, %q; want both done", tasks[0].Status, tasks[1].Status)
	}
	if tasks[1].Type != taskpkg.TaskTypeVerify {
		t.Fatalf("audit task type = %q, want %q", tasks[1].Type, taskpkg.TaskTypeVerify)
	}
	if tasks[1].Prompt != auditPrompt {
		t.Fatalf("audit task prompt did not preserve the original assignment verbatim\ngot:  %q\nwant: %q", tasks[1].Prompt, auditPrompt)
	}

	sess.Close()
	var communicated []string
	for event := range sess.Events() {
		if event.Kind != events.EventCommunicate {
			continue
		}
		data, ok := event.Data.(events.CommunicateData)
		if !ok {
			t.Fatalf("COMMUNICATE event data type = %T", event.Data)
		}
		communicated = append(communicated, data.Message)
	}
	if len(communicated) != 1 || communicated[0] != "audited final" {
		t.Fatalf("communicated messages = %#v, want only the post-audit result", communicated)
	}
}

func TestFinalRequirementsAudit_IneligibleSessionDeliversFirstResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      SessionConfig
		taskMode string
	}{
		{
			name:     "experiment disabled",
			cfg:      SessionConfig{NonInteractive: true},
			taskMode: "done",
		},
		{
			name:     "interactive root",
			cfg:      SessionConfig{ExperimentalFinalRequirementsAudit: true},
			taskMode: "done",
		},
		{
			name: "non-interactive subagent",
			cfg: SessionConfig{
				NonInteractive:                     true,
				ExperimentalFinalRequirementsAudit: true,
				spawn:                              spawnConfig{parentSessionID: "parent"},
			},
			taskMode: "done",
		},
		{
			name: "no task list",
			cfg: SessionConfig{
				NonInteractive:                     true,
				ExperimentalFinalRequirementsAudit: true,
			},
		},
		{
			name: "unfinished task list",
			cfg: SessionConfig{
				NonInteractive:                     true,
				ExperimentalFinalRequirementsAudit: true,
			},
			taskMode: "in_progress",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t,
				withDir(t.TempDir()),
				withConfig(tc.cfg),
				withSteps(
					func(llm.Request) llm.Response {
						return toolCallResponse(communicateCall("first-final", "first result"))
					},
					func(llm.Request) llm.Response {
						t.Fatal("ineligible session made another provider request")
						return llm.Response{}
					},
				),
			)
			if tc.taskMode != "" {
				store := sess.getOrCreateTaskStore()
				added, err := store.Append([]taskpkg.TaskInput{{
					Type:        taskpkg.TaskTypeImplement,
					Description: "existing work",
					Prompt:      "finish existing work",
				}})
				if err != nil {
					t.Fatalf("append task: %v", err)
				}
				status := taskpkg.TaskDone
				if tc.taskMode == "in_progress" {
					status = taskpkg.TaskInProgress
				}
				if err := store.Update([]taskpkg.TaskUpdate{{ID: added[0].ID, Status: status}}); err != nil {
					t.Fatalf("seed task status: %v", err)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			output, err := sess.ProcessInput(ctx, "original task", nil)
			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if output != "first result" {
				t.Fatalf("ProcessInput returned %q, want the first result unchanged", output)
			}
		})
	}
}

func finalAuditRequestContainsText(req llm.Request, want string) bool {
	for _, message := range req.Messages {
		if strings.Contains(message.Text(), want) {
			return true
		}
	}
	return false
}
