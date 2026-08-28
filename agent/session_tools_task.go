package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// normalizeTaskEffort maps the "inherit" sentinel to "" (no override: the task
// runs at the session's configured effort). The sentinel exists because OpenAI
// strict mode force-requires every schema property, so a model there cannot
// simply omit reasoning_effort the way the non-strict (Anthropic) path can.
func normalizeTaskEffort(effort string) string {
	if strings.EqualFold(strings.TrimSpace(effort), "inherit") {
		return ""
	}
	return llm.NormalizeReasoningEffort(effort)
}

func validateTaskEffort(effort string) (string, error) {
	normalized := normalizeTaskEffort(effort)
	if err := llm.ValidateReasoningEffort(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// formatTaskList renders the task list as plain text, like a to-do list: one task
// per line as "<id>. [<status>] <type> — <description>" with dependencies and any
// accumulated notes, then a progress footer. The structured snapshot rides along
// separately as the StateResult.State for the hub UI.
func formatTaskList(tasks []taskpkg.Task) string {
	if len(tasks) == 0 {
		return "No tasks."
	}
	var b strings.Builder
	for _, t := range tasks {
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
	summary := taskpkg.Summarize(tasks)
	fmt.Fprintf(&b, "\nProgress: %d/%d tasks complete.", summary.Done, summary.Total)
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

// taskToolState is the task-list mutation snapshot carried to human clients.
// Started is present only for explicit updates whose final status is
// in_progress, so the frontend can distinguish a real transition from a
// status reassertion without changing the model-facing tool schema or the
// persisted Task shape.
type taskToolState struct {
	taskpkg.Task
	Started *bool `json:"started,omitempty"`
}

func taskToolStateSnapshot(tasks []taskpkg.Task, inProgressUpdates map[int]struct{}, started map[int]bool) []taskToolState {
	snapshot := make([]taskToolState, len(tasks))
	for i, task := range tasks {
		snapshot[i].Task = task
		if _, ok := inProgressUpdates[task.ID]; !ok {
			continue
		}
		transitioned := started[task.ID]
		snapshot[i].Started = &transitioned
	}
	return snapshot
}

func mutateAndPublishTaskStore(store *taskpkg.TaskStore, mutation func() (any, error)) (any, error) {
	var result any
	err := store.MutateAndPublish(func() error {
		var err error
		result, err = mutation()
		return err
	})
	return result, err
}

func registerTaskTools(reg *tool.Registry, deps *toolDeps) {
	// Task management.
	_ = reg.Register(tool.RegisteredTool{
		Definition: tool.DefTaskList(deps.reasoningEffortLevels),
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
						var err error
						reasoningEffort, err = validateTaskEffort(re)
						if err != nil {
							return nil, err
						}
					}
					items = append(items, taskpkg.TaskInput{
						Type:            taskType,
						Description:     fmt.Sprint(m["description"]),
						Prompt:          fmt.Sprint(m["prompt"]),
						DependsOn:       depIDs,
						ReasoningEffort: reasoningEffort,
					})
				}
				return mutateAndPublishTaskStore(store, func() (any, error) {
					added, err := store.Append(items)
					if err != nil {
						return nil, err
					}

					// The tool response is a terse acknowledgement. The current
					// task is announced via a separate SYSTEM-REMINDER steering
					// message when the agent actually transitions one to
					// in_progress, either manually or via auto-advance.
					tasks := store.View()
					taskUpdate := taskUpdatedData(taskpkg.Summarize(tasks), "")
					deps.emit(events.EventTaskUpdated, taskUpdate)
					return tool.StateResult{
						Output: fmt.Sprintf("Added %d task(s). Progress: %d/%d tasks complete.", len(added), taskUpdate.Done, taskUpdate.Total),
						State:  tasks,
					}, nil
				})
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
						var err error
						u.ReasoningEffort, err = validateTaskEffort(re)
						if err != nil {
							return nil, err
						}
					}
					updates = append(updates, u)
				}
				return mutateAndPublishTaskStore(store, func() (any, error) {
					mutation, err := store.UpdateWithSnapshot(updates)
					if err != nil {
						return nil, err
					}

					// Classify each ID from the final status the store applied, so
					// duplicate entries cannot steer a task that ended completed or
					// suppress auto-advance after the final state is known.
					previous := make(map[int]taskpkg.TaskStatus, len(mutation.Before))
					for _, task := range mutation.Before {
						previous[task.ID] = task.Status
					}
					finalStatus := make(map[int]taskpkg.TaskStatus, len(updates))
					for _, u := range updates {
						finalStatus[u.ID] = u.Status
					}
					inProgressUpdates := make(map[int]struct{}, len(updates))
					started := make(map[int]bool)
					var completedAny bool
					var manuallyStartedID int
					seenIDs := make(map[int]struct{}, len(updates))
					for _, u := range updates {
						if _, seen := seenIDs[u.ID]; seen {
							continue
						}
						seenIDs[u.ID] = struct{}{}
						status := finalStatus[u.ID]
						if status == taskpkg.TaskDone || status == taskpkg.TaskCancelled {
							completedAny = true
						}
						if status == taskpkg.TaskInProgress {
							inProgressUpdates[u.ID] = struct{}{}
							if previous[u.ID] != taskpkg.TaskInProgress {
								started[u.ID] = true
								manuallyStartedID = u.ID
							}
						}
					}

					// If the agent explicitly started a task, fire its current-task
					// steering so the SYSTEM-REMINDER for the new task shows up on
					// the next turn.
					if manuallyStartedID != 0 {
						for _, t := range mutation.After {
							if t.ID == manuallyStartedID {
								// Inside the task_list handler: the tool is registered by
								// construction, so the steering may name it.
								deps.steer(formatCurrentTaskSteering(t, true), events.SteeringKindCurrentTask)
								break
							}
						}
					}

					if !completedAny && manuallyStartedID == 0 {
						deps.emit(events.EventTaskUpdated, taskUpdatedData(taskpkg.Summarize(mutation.After), ""))
						return tool.StateResult{
							Output: "Updated " + formatTaskUpdates(updates) + ".",
							State:  taskToolStateSnapshot(mutation.After, inProgressUpdates, started),
						}, nil
					}

					var msg strings.Builder
					msg.WriteString("Updated ")
					msg.WriteString(formatTaskUpdates(updates))
					msg.WriteString(". ")
					finalTasks := mutation.After

					if completedAny {
						// Auto-advance unless the agent already picked what to do next.
						if manuallyStartedID == 0 {
							eligible := store.NextEligible()
							if len(eligible) > 0 {
								next := eligible[0]
								if auto, err := store.UpdateWithSnapshot([]taskpkg.TaskUpdate{{ID: next.ID, Status: taskpkg.TaskInProgress}}); err == nil {
									finalTasks = auto.After
									deps.steer(formatCurrentTaskSteering(next, true), events.SteeringKindCurrentTask)
								}
							} else {
								// No eligible task. If nothing remains open or in_progress,
								// signal the agent that the list is exhausted.
								allDone := taskListAllDone(finalTasks)
								if allDone && len(finalTasks) > 0 {
									var blockingDelegateIDs []string
									if deps.blockingDelegateIDs != nil {
										blockingDelegateIDs = deps.blockingDelegateIDs()
									}
									deps.sendTaskCompletionSteering(taskReminderAllDoneWhileDelegatesRun(deps.resultToolName(), blockingDelegateIDs), blockingDelegateIDs)
									if len(blockingDelegateIDs) == 0 {
										msg.WriteString("All tasks complete. ")
									} else {
										msg.WriteString("All tasks complete; waiting for delegate(s) ")
										msg.WriteString(strings.Join(blockingDelegateIDs, ", "))
										msg.WriteString(". ")
									}
								}
							}
						}
					}

					taskUpdate := taskUpdatedData(taskpkg.Summarize(finalTasks), "")
					deps.emit(events.EventTaskUpdated, taskUpdate)
					fmt.Fprintf(&msg, "Progress: %d/%d tasks complete.", taskUpdate.Done, taskUpdate.Total)
					return tool.StateResult{Output: msg.String(), State: taskToolStateSnapshot(finalTasks, inProgressUpdates, started)}, nil
				})
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})
}

// taskStateData is the single conversion from transport-neutral task semantics
// to event task state, shared by start seeds and mutation carriers.
func taskStateData(summary taskpkg.ListSummary) events.TaskStateData {
	data := events.TaskStateData{Total: summary.Total, Done: summary.Done}
	if summary.Current != nil {
		data.Current = &events.TaskSummaryData{
			ID:          summary.Current.ID,
			Description: summary.Current.Description,
		}
	}
	return data
}

func taskUpdatedData(summary taskpkg.ListSummary, taskStoreOwnerSessionID string) events.TaskUpdatedData {
	return events.TaskUpdatedData{
		TaskStateData:           taskStateData(summary),
		TaskStoreOwnerSessionID: taskStoreOwnerSessionID,
	}
}

func taskListAllDone(tasks []taskpkg.Task) bool {
	for _, task := range tasks {
		if task.Status == taskpkg.TaskOpen || task.Status == taskpkg.TaskInProgress {
			return false
		}
	}
	return true
}
