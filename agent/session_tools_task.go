package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

// formatTaskList renders the task list as plain text, like a to-do list: one task
// per line as "<id>. [<status>] <type> — <description>" with dependencies and any
// accumulated notes, then a progress footer. The structured snapshot rides along
// separately as the StateResult.State for the hub UI.
func formatTaskList(tasks []taskpkg.Task) string {
	if len(tasks) == 0 {
		return "No tasks."
	}
	var b strings.Builder
	done := 0
	for _, t := range tasks {
		if t.Status == taskpkg.TaskDone {
			done++
		}
		fmt.Fprintf(&b, "%d. [%s] %s — %s", t.ID, t.Status, t.Type, t.Description)
		if len(t.DependsOn) > 0 {
			parts := make([]string, len(t.DependsOn))
			for i, d := range t.DependsOn {
				parts[i] = strconv.Itoa(d)
			}
			fmt.Fprintf(&b, " (depends on: %s)", strings.Join(parts, ", "))
		}
		b.WriteByte('\n')
		if t.ReasoningEffort != "" {
			fmt.Fprintf(&b, "   effort: %s\n", t.ReasoningEffort)
		}
		for _, n := range t.Notes {
			fmt.Fprintf(&b, "   note: %s\n", n)
		}
	}
	fmt.Fprintf(&b, "\nProgress: %d/%d tasks complete.", done, len(tasks))
	return b.String()
}

// formatTaskUpdates summarizes the explicit changes in an update batch as
// "id→status" (or just "id" when only notes/deps/effort changed), so the update
// acknowledgement reports what the agent changed without a separate view. It
// deliberately covers only the caller's own updates, never an auto-advanced next
// task (that is announced via separate current-task steering).
func formatTaskUpdates(updates []taskpkg.TaskUpdate) string {
	parts := make([]string, len(updates))
	for i, u := range updates {
		if u.Status != "" {
			parts[i] = fmt.Sprintf("%d→%s", u.ID, u.Status)
		} else {
			parts[i] = strconv.Itoa(u.ID)
		}
	}
	return strings.Join(parts, ", ")
}

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
				return tool.StateResult{Output: formatTaskList(store.View()), State: store.View()}, nil
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
					return tool.StateResult{Output: "Updated " + formatTaskUpdates(updates) + ".", State: store.View()}, nil
				}

				var msg strings.Builder
				msg.WriteString("Updated " + formatTaskUpdates(updates) + ". ")

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
				fmt.Fprintf(&msg, "Progress: %d/%d tasks complete.", done, total)
				return tool.StateResult{Output: msg.String(), State: store.View()}, nil
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})
}
