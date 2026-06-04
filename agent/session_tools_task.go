package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func registerTaskTools(reg *tool.Registry, deps *toolDeps) {
	// Task management.
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefTaskList(deps.reasoningEffortLevels)},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			deps.taskGuard.MarkUsed()
			store := deps.taskGuard.Store()
			action := fmt.Sprint(args["action"])
			switch action {
			case "view":
				return store.View(), nil
			case "append":
				raw, ok := args["tasks"].([]any)
				if !ok || len(raw) == 0 {
					return nil, errors.New("append requires a non-empty 'tasks' array")
				}
				var items []taskpkg.TaskInput
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, errors.New("each task must be an object with description and prompt")
					}
					var depIDs []int
					if depsRaw, ok := m["depends_on"].([]any); ok {
						for _, d := range depsRaw {
							if v, ok := d.(float64); ok {
								depIDs = append(depIDs, int(v))
							}
						}
					}
					var taskType taskpkg.TaskType
					if t, ok := m["type"].(string); ok {
						taskType = taskpkg.TaskType(t)
					}
					reasoningEffort := ""
					if re, ok := m["reasoning_effort"].(string); ok {
						reasoningEffort = re
					}
					items = append(items, taskpkg.TaskInput{
						Type:            taskType,
						Description:     fmt.Sprint(m["description"]),
						Prompt:          fmt.Sprint(m["prompt"]),
						DependsOn:       depIDs,
						ReasoningEffort: reasoningEffort,
					})
				}
				added, err := store.Append(items)
				if err != nil {
					return nil, err
				}

				// The tool response is a terse acknowledgement. The current
				// task is announced via a separate SYSTEM-REMINDER steering
				// message when the agent actually transitions one to
				// in_progress, either manually or via auto-advance.
				total, done := store.Progress()
				return tool.StateResult{
					Output: fmt.Sprintf("Added %d task(s). Progress: %d/%d tasks complete.", len(added), done, total),
					State:  store.View(),
				}, nil
			case "update":
				raw, ok := args["updates"].([]any)
				if !ok || len(raw) == 0 {
					return nil, errors.New("update requires a non-empty 'updates' array")
				}
				var updates []taskpkg.TaskUpdate
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, errors.New("each update must be an object with id and status")
					}
					id := 0
					if v, ok := m["id"].(float64); ok {
						id = int(v)
					}
					u := taskpkg.TaskUpdate{
						ID:     id,
						Status: taskpkg.TaskStatus(fmt.Sprint(m["status"])),
					}
					if n, ok := m["notes"].(string); ok {
						u.Notes = n
					}
					if depsRaw, ok := m["depends_on"]; ok {
						var depIDs []int
						if arr, ok := depsRaw.([]any); ok {
							for _, d := range arr {
								if v, ok := d.(float64); ok {
									depIDs = append(depIDs, int(v))
								}
							}
						}
						u.DependsOn = &depIDs
					}
					if re, ok := m["reasoning_effort"].(string); ok {
						u.ReasoningEffort = re
					}
					updates = append(updates, u)
				}
				if err := store.Update(updates); err != nil {
					return nil, err
				}

				// Classify the batch so we know whether to auto-advance, fire
				// a manual-start steering, or emit the "all done" steering.
				var completedAny bool
				var manuallyStartedID int
				for _, u := range updates {
					if u.Status == taskpkg.TaskDone || u.Status == taskpkg.TaskCancelled {
						completedAny = true
					}
					if u.Status == taskpkg.TaskInProgress {
						manuallyStartedID = u.ID
					}
				}

				// If the agent explicitly started a task, fire its current-task
				// steering so the SYSTEM-REMINDER for the new task shows up on
				// the next turn.
				if manuallyStartedID != 0 {
					for _, t := range store.View() {
						if t.ID == manuallyStartedID {
							if t.ReasoningEffort != "" {
								deps.taskGuard.SetReasoningEffort(t.ReasoningEffort)
							}
							deps.steer(formatCurrentTaskSteering(t))
							break
						}
					}
				}

				if !completedAny && manuallyStartedID == 0 {
					return tool.StateResult{Output: "Updated.", State: store.View()}, nil
				}

				var msg strings.Builder
				msg.WriteString("Updated. ")

				if completedAny {
					// Auto-advance unless the agent already picked what to do next.
					if manuallyStartedID == 0 {
						eligible := store.NextEligible()
						if len(eligible) > 0 {
							next := eligible[0]
							if err := store.Update([]taskpkg.TaskUpdate{{ID: next.ID, Status: taskpkg.TaskInProgress}}); err == nil {
								if next.ReasoningEffort != "" {
									deps.taskGuard.SetReasoningEffort(next.ReasoningEffort)
								}
								deps.steer(formatCurrentTaskSteering(next))
							}
						} else {
							// No eligible task. If nothing remains open or in_progress,
							// signal the agent that the list is exhausted.
							allDone := true
							for _, t := range store.View() {
								if t.Status == taskpkg.TaskOpen || t.Status == taskpkg.TaskInProgress {
									allDone = false
									break
								}
							}
							if allDone && len(store.View()) > 0 {
								deps.steer(taskReminderAllDone())
								msg.WriteString("All tasks complete. ")
							}
						}
					}
				}

				total, done := store.Progress()
				msg.WriteString(fmt.Sprintf("Progress: %d/%d tasks complete.", done, total))
				return tool.StateResult{Output: msg.String(), State: store.View()}, nil
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})
}
