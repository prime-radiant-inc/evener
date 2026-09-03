package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	fmt.Fprintf(&b, "\nProgress: %s.", summary.ProgressText())
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

// formatMutationAck renders the terse acknowledgement prefix for a call that
// applied adds and/or updates: "Added N task(s); updated 1→done" (or either
// half alone). formatTaskUpdates carries the id→status detail.
func formatMutationAck(added int, updates []taskpkg.TaskUpdate) string {
	detail := formatTaskUpdates(updates)
	if added > 0 {
		return fmt.Sprintf("Added %d task(s); updated %s.", added, detail)
	}
	return "Updated " + detail + "."
}

// taskToolState is the task-list mutation snapshot carried to human clients.
// Started is present for every in_progress task in a mutation snapshot. It is
// true only when this call transitioned that task into progress (explicitly or
// by auto-advance), letting the frontend distinguish that from an existing
// current task or a status reassertion without changing the model-facing tool
// schema or the persisted Task shape.
type taskToolState struct {
	taskpkg.Task
	Started *bool `json:"started,omitempty"`
}

func taskToolStateSnapshot(tasks []taskpkg.Task, started map[int]bool) []taskToolState {
	snapshot := make([]taskToolState, len(tasks))
	for i, task := range tasks {
		snapshot[i].Task = task
		if task.Status != taskpkg.TaskInProgress {
			continue
		}
		transitioned := started[task.ID]
		snapshot[i].Started = &transitioned
	}
	return snapshot
}

func mutateAndPublishTaskStore(store *taskpkg.TaskStore, mutation func(epoch, revision uint64) (any, error)) (any, error) {
	var result any
	err := store.MutateAndPublish(func(epoch, revision uint64) error {
		var err error
		result, err = mutation(epoch, revision)
		return err
	})
	return result, err
}

// decodeTaskArgs converts a presence-based task_list call into typed inputs:
// whichever array is present is the operation, both can appear in one call.
// Decoding is strict — a wrong-typed field is an error naming the entry, and
// an update entry that sets none of status/notes/depends_on/reasoning_effort
// is rejected rather than silently no-opping. No fmt.Sprint coercion: the
// old handler's fmt.Sprint(m["status"]) turned a missing status into the
// literal string "<nil>" and broke the schema-documented optional status.
//
// Retired keys (action/tasks/updates) are rejected here too, not only in
// prepareToolCall's guard: a direct handler caller (tests, internal callers)
// bypasses prevalidation, and silently treating an action-shaped call as a
// view would let the caller believe its mutations applied.
func decodeTaskArgs(args map[string]any) (adds []taskpkg.TaskInput, updates []taskpkg.TaskUpdate, err error) {
	for _, retired := range []string{"action", "tasks", "updates"} {
		if _, supplied := args[retired]; supplied {
			return nil, nil, fmt.Errorf("task_list no longer takes %s; use add and/or update, or a bare call to view", retired)
		}
	}
	rawAdds, _ := args["add"].([]any)
	adds = make([]taskpkg.TaskInput, 0, len(rawAdds))
	for i, r := range rawAdds {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("add entry %d must be an object with type, description, and prompt", i)
		}
		if err := validateTaskListItemFields("add", i, m); err != nil {
			return nil, nil, err
		}
		var input taskpkg.TaskInput
		if t, ok := m["type"].(string); ok {
			input.Type = taskpkg.TaskType(t)
		}
		description, ok := m["description"].(string)
		if !ok {
			return nil, nil, fmt.Errorf("add entry %d requires the field named description as a string; valid add shape: {type, description, prompt}", i)
		}
		input.Description = description
		prompt, ok := m["prompt"].(string)
		if !ok {
			return nil, nil, fmt.Errorf("add entry %d requires a string prompt", i)
		}
		input.Prompt = prompt
		if depsRaw, has := m["depends_on"]; has {
			arr, ok := depsRaw.([]any)
			if !ok {
				return nil, nil, fmt.Errorf("add entry %d depends_on must be an array of task IDs", i)
			}
			depIDs, err := decodeIDList(arr)
			if err != nil {
				return nil, nil, fmt.Errorf("add entry %d depends_on: %w", i, err)
			}
			input.DependsOn = depIDs
		}
		if re, ok := m["reasoning_effort"].(string); ok {
			input.ReasoningEffort, err = validateTaskEffort(re)
			if err != nil {
				return nil, nil, err
			}
		}
		adds = append(adds, input)
	}
	rawUpdates, _ := args["update"].([]any)
	updates = make([]taskpkg.TaskUpdate, 0, len(rawUpdates))
	for i, r := range rawUpdates {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("update entry %d must be an object with id", i)
		}
		if err := validateTaskListItemFields("update", i, m); err != nil {
			return nil, nil, err
		}
		idFloat, ok := m["id"].(float64)
		if !ok {
			return nil, nil, fmt.Errorf("update entry %d requires an integer id", i)
		}
		u := taskpkg.TaskUpdate{ID: int(idFloat)}
		if s, ok := m["status"].(string); ok {
			u.Status = taskpkg.TaskStatus(s)
		}
		if n, ok := m["notes"].(string); ok {
			u.Notes = n
		}
		if depsRaw, has := m["depends_on"]; has {
			arr, ok := depsRaw.([]any)
			if !ok {
				return nil, nil, fmt.Errorf("update entry %d depends_on must be an array of task IDs", i)
			}
			depIDs, err := decodeIDList(arr)
			if err != nil {
				return nil, nil, fmt.Errorf("update entry %d depends_on: %w", i, err)
			}
			u.DependsOn = &depIDs
		}
		if re, ok := m["reasoning_effort"].(string); ok {
			u.ReasoningEffort, err = validateTaskEffort(re)
			if err != nil {
				return nil, nil, err
			}
		}
		if u.Status == "" && u.Notes == "" && u.DependsOn == nil && u.ReasoningEffort == "" {
			return nil, nil, fmt.Errorf("update entry for task %d changes nothing; include status, notes, depends_on, or reasoning_effort", u.ID)
		}
		updates = append(updates, u)
	}
	return adds, updates, nil
}

func validateTaskListItemFields(kind string, index int, item map[string]any) error {
	allowed := map[string]bool{}
	switch kind {
	case "add":
		for _, field := range []string{"type", "description", "prompt", "depends_on", "reasoning_effort"} {
			allowed[field] = true
		}
	case "update":
		for _, field := range []string{"id", "status", "notes", "depends_on", "reasoning_effort"} {
			allowed[field] = true
		}
	}
	if _, hasBrief := item["brief"]; hasBrief {
		if kind == "add" {
			return fmt.Errorf("add entry %d has invalid field %q; use the required field named %q instead; valid add shape: {type, description, prompt}", index, "brief", "description")
		}
		return fmt.Errorf("update entry %d has invalid field %q; valid update shape: {id, status, notes, depends_on, reasoning_effort}", index, "brief")
	}
	unknown := make([]string, 0, len(item))
	for field := range item {
		if !allowed[field] {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		quoted := make([]string, len(unknown))
		for i, field := range unknown {
			quoted[i] = strconv.Quote(field)
		}
		return fmt.Errorf("%s entry %d has unknown fields %s", kind, index, strings.Join(quoted, ", "))
	}
	return nil
}

func validateTaskListArgs(args map[string]any) error {
	for _, kind := range []string{"add", "update"} {
		raw, supplied := args[kind]
		if !supplied {
			continue
		}
		items, ok := raw.([]any)
		if !ok {
			continue // The JSON schema reports an array type mismatch.
		}
		for i, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue // The JSON schema reports an object type mismatch.
			}
			if err := validateTaskListItemFields(kind, i, item); err != nil {
				return err
			}
			if kind == "add" {
				if _, ok := item["description"]; !ok {
					return fmt.Errorf("add entry %d requires the field named description; valid add shape: {type, description, prompt}", i)
				}
			}
		}
	}
	return nil
}

// decodeIDList converts a JSON array of numbers into []int.
func decodeIDList(raw []any) ([]int, error) {
	ids := make([]int, 0, len(raw))
	for _, d := range raw {
		v, ok := d.(float64)
		if !ok {
			return nil, errors.New("each element must be an integer task ID")
		}
		ids = append(ids, int(v))
	}
	return ids, nil
}

func registerTaskTools(reg *tool.Registry, deps *toolDeps) {
	// Task management.
	_ = reg.Register(tool.RegisteredTool{
		Definition:  tool.DefTaskList(deps.reasoningEffortLevels),
		PreValidate: validateTaskListArgs,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			deps.taskGuard.MarkUsed()
			store := deps.taskGuard.Store()
			adds, updates, err := decodeTaskArgs(args)
			if err != nil {
				return nil, err
			}
			if len(adds) == 0 && len(updates) == 0 {
				// Bare or all-empty call: view. (Empty arrays decode to nil
				// slices; a mutation with nothing to mutate is the view.)
				tasks := store.View()
				return tool.StateResult{Output: formatTaskList(tasks), State: tasks}, nil
			}

			return mutateAndPublishTaskStore(store, func(epoch, revision uint64) (any, error) {
				if len(updates) == 0 {
					added, err := store.Append(adds)
					if err != nil {
						return nil, err
					}
					// Adds only: terse acknowledgement. The current task is
					// announced via a separate SYSTEM-REMINDER steering message
					// when the agent actually transitions one to in_progress,
					// either manually or via auto-advance.
					tasks := store.View()
					summary := taskpkg.Summarize(tasks)
					taskUpdate := taskUpdatedData(summary, "", epoch, revision)
					deps.emit(events.EventTaskUpdated, taskUpdate)
					return tool.StateResult{
						Output: fmt.Sprintf("Added %d task(s). Progress: %s.", len(added), summary.ProgressText()),
						State:  tasks,
					}, nil
				}
				var mutation taskpkg.TaskUpdateSnapshot
				if len(adds) == 0 {
					// Keep update-only validation order and snapshot behavior exactly
					// as before; ApplyBatch is needed only where additions and
					// updates must commit together.
					mutation, err = store.UpdateWithSnapshot(updates)
				} else {
					mutation, err = store.ApplyBatch(adds, updates)
				}
				if err != nil {
					return nil, err
				}

				// Classify each ID from the final status the store applied, so
				// duplicate entries cannot steer a task that ended completed or
				// suppress auto-advance after the final state is known. Note:
				// mutation.Before is the pre-combined-batch state. ApplyBatch
				// enforces that every update target pre-existed the call, so its
				// status is the caller's true pre-call status.
				previous := make(map[int]taskpkg.TaskStatus, len(mutation.Before))
				for _, task := range mutation.Before {
					previous[task.ID] = task.Status
				}
				// afterByID serves the classification below and the steering
				// lookup: one pass over the snapshot instead of a scan per
				// purpose. Final statuses come from the snapshot, not the raw
				// entries: an empty-status entry (no change) must classify
				// by the task's resulting status.
				afterByID := make(map[int]taskpkg.Task, len(mutation.After))
				for _, t := range mutation.After {
					afterByID[t.ID] = t
				}
				started := make(map[int]bool)
				var completedAny bool
				var manuallyStartedID int
				seenIDs := make(map[int]struct{}, len(updates))
				for _, u := range updates {
					if _, seen := seenIDs[u.ID]; seen {
						continue
					}
					seenIDs[u.ID] = struct{}{}
					status := afterByID[u.ID].Status
					if status == taskpkg.TaskDone || status == taskpkg.TaskCancelled {
						completedAny = true
					}
					if status == taskpkg.TaskInProgress {
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
					// Inside the task_list handler: the tool is registered by
					// construction, so the steering may name it.
					deps.steer(formatCurrentTaskSteering(afterByID[manuallyStartedID], true), events.SteeringKindCurrentTask)
				}

				if !completedAny && manuallyStartedID == 0 {
					deps.emit(events.EventTaskUpdated, taskUpdatedData(taskpkg.Summarize(mutation.After), "", epoch, revision))
					return tool.StateResult{
						Output: formatMutationAck(len(adds), updates),
						State:  taskToolStateSnapshot(mutation.After, started),
					}, nil
				}

				var msg strings.Builder
				msg.WriteString(formatMutationAck(len(adds), updates))
				msg.WriteString(" ")
				finalTasks := mutation.After

				if completedAny {
					// Auto-advance unless the agent already picked what to do next.
					if manuallyStartedID == 0 {
						eligible := store.NextEligible()
						if len(eligible) > 0 {
							next := eligible[0]
							if auto, err := store.UpdateWithSnapshot([]taskpkg.TaskUpdate{{ID: next.ID, Status: taskpkg.TaskInProgress}}); err == nil {
								finalTasks = auto.After
								started[next.ID] = true
								deps.steer(formatCurrentTaskSteering(next, true), events.SteeringKindCurrentTask)
							}
						} else {
							// No eligible task. If nothing remains open or in_progress,
							// signal the agent that the list is exhausted.
							summary := taskpkg.Summarize(finalTasks)
							if summary.NoActionableTasks() && summary.Total > 0 {
								var blockingDelegateIDs []string
								if deps.blockingDelegateIDs != nil {
									blockingDelegateIDs = deps.blockingDelegateIDs()
								}
								deps.sendTaskCompletionSteering(taskReminderTerminalWhileDelegatesRun(deps.resultToolName(), blockingDelegateIDs, summary.AllDone()), blockingDelegateIDs)
								if len(blockingDelegateIDs) == 0 {
									if summary.AllDone() {
										msg.WriteString("All tasks complete. ")
									} else {
										msg.WriteString("No actionable tasks remain. ")
									}
								} else {
									if summary.AllDone() {
										msg.WriteString("All tasks complete; waiting for delegate(s) ")
									} else {
										msg.WriteString("No actionable tasks remain; waiting for delegate(s) ")
									}
									msg.WriteString(strings.Join(blockingDelegateIDs, ", "))
									msg.WriteString(". ")
								}
							}
						}
					}
				}

				summary := taskpkg.Summarize(finalTasks)
				taskUpdate := taskUpdatedData(summary, "", epoch, revision)
				deps.emit(events.EventTaskUpdated, taskUpdate)
				fmt.Fprintf(&msg, "Progress: %s.", summary.ProgressText())
				return tool.StateResult{Output: msg.String(), State: taskToolStateSnapshot(finalTasks, started)}, nil
			})
		},
	})
}

// taskStateData is the single conversion from transport-neutral task semantics
// to event task state, shared by start seeds and mutation carriers.
func taskStateData(summary taskpkg.ListSummary) events.TaskStateData {
	data := events.TaskStateData{
		Total:     summary.Total,
		Done:      summary.Done,
		Cancelled: summary.Cancelled,
		Remaining: summary.Remaining,
	}
	if summary.Current != nil {
		data.Current = &events.TaskSummaryData{
			ID:          summary.Current.ID,
			Description: summary.Current.Description,
		}
	}
	return data
}

func taskUpdatedData(summary taskpkg.ListSummary, taskStoreOwnerSessionID string, publicationEpoch, publicationRevision uint64) events.TaskUpdatedData {
	state := taskStateData(summary)
	return events.TaskUpdatedData{
		Total:                   state.Total,
		Done:                    state.Done,
		Cancelled:               state.Cancelled,
		Remaining:               state.Remaining,
		Current:                 state.Current,
		TaskStoreOwnerSessionID: taskStoreOwnerSessionID,
		TaskPublicationEpoch:    publicationEpoch,
		TaskPublicationRevision: publicationRevision,
	}
}
