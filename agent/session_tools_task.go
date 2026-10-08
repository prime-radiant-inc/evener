package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
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
// schema or the persisted Task shape. Settled is the terminal-status analogue:
// present for every done or cancelled task in a mutation snapshot, true only
// when this call transitioned that task into its terminal status (the
// pre-call status differed), so the frontend can render a re-asserted settle
// as an annotation rather than as fresh news.
type taskToolState struct {
	taskpkg.Task
	Started *bool `json:"started,omitempty"`
	Settled *bool `json:"settled,omitempty"`
}

func taskToolStateSnapshot(tasks []taskpkg.Task, started map[int]bool, settled map[int]bool) []taskToolState {
	snapshot := make([]taskToolState, len(tasks))
	for i, task := range tasks {
		snapshot[i].Task = task
		switch task.Status {
		case taskpkg.TaskInProgress:
			transitioned := started[task.ID]
			snapshot[i].Started = &transitioned
		case taskpkg.TaskDone, taskpkg.TaskCancelled:
			settledThisCall := settled[task.ID]
			snapshot[i].Settled = &settledThisCall
		}
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
//
// Item-field validation deliberately runs again here as a standalone handler
// safety invariant for direct/internal callers that bypass registry
// PreValidate. The preparation and decode paths both call the single
// validateTaskListItemFields validator, so their allowlists and targeted
// diagnostics remain one shared contract rather than duplicated behavior.
//
// Placeholder update fields follow the one rule in normalizeTaskListArgs.
// Dispatch already ran it as the tool's NormalizeArgs; it is idempotent, so
// decoding runs it again and a direct caller gets the same reading.
func decodeTaskArgs(args map[string]any) (adds []taskpkg.TaskInput, updates []taskpkg.TaskUpdate, err error) {
	for _, retired := range []string{"action", "tasks", "updates"} {
		if _, supplied := args[retired]; supplied {
			return nil, nil, fmt.Errorf("task_list no longer takes %s; use add and/or update, or a bare call to view", retired)
		}
	}
	args, err = normalizeTaskListArgs(args)
	if err != nil {
		return nil, nil, err
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
			if errors.Is(err, errZeroTaskID) {
				return nil, nil, fmt.Errorf("add entry %d depends_on: %w, and a new task has no dependencies to clear", i, err)
			}
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
		id, ok := taskIDValue(m["id"])
		if !ok {
			return nil, nil, fmt.Errorf("update entry %d requires a positive integer id", i)
		}
		u := taskpkg.TaskUpdate{ID: id}
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
			if len(arr) == 1 && arr[0] == float64(clearDependsOnID) {
				u.DependsOn = &[]int{}
			} else {
				depIDs, err := decodeIDList(arr)
				if errors.Is(err, errZeroTaskID) {
					return nil, nil, fmt.Errorf("update entry %d depends_on: %w; [0] on its own clears the dependencies", i, err)
				}
				if err != nil {
					return nil, nil, fmt.Errorf("update entry %d depends_on: %w", i, err)
				}
				u.DependsOn = &depIDs
			}
		}
		if re, ok := m["reasoning_effort"].(string); ok {
			u.ReasoningEffort, err = validateTaskEffort(re)
			if err != nil {
				return nil, nil, err
			}
		}
		if u.Status == "" && u.Notes == "" && u.DependsOn == nil && u.ReasoningEffort == "" {
			return nil, nil, fmt.Errorf(`update entry for task %d changes nothing; depends_on of [] or null and notes of "" or "null" are placeholders and are ignored. Include a status, real notes, new depends_on IDs, depends_on: [0] to clear dependencies, or reasoning_effort`, u.ID)
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

// clearDependsOnID, alone in an update's depends_on, clears the task's
// dependencies. Task IDs start at 1, so it never names a real task.
const clearDependsOnID = 0

// errZeroTaskID rejects 0 inside an ID list; each caller says what [0] means
// for its operation.
var errZeroTaskID = errors.New("0 is not a task ID")

// decodeIDList converts a JSON array of numbers into []int.
func decodeIDList(raw []any) ([]int, error) {
	ids := make([]int, 0, len(raw))
	for _, d := range raw {
		v, ok := d.(float64)
		if !ok {
			return nil, errors.New("each element must be an integer task ID")
		}
		if int(v) == clearDependsOnID {
			return nil, errZeroTaskID
		}
		ids = append(ids, int(v))
	}
	return ids, nil
}

// updatePlaceholders is the one table of placeholder values for an update
// entry's optional fields. A placeholder is a value whose effect is identical
// to omitting the field: decodeTaskArgs and the store leave the task as it
// was. Some models fill every optional field on every call, so these are
// dropped before validation. Values that change something are not here:
// depends_on [0] clears the dependencies, and reasoning_effort "none" or
// "null" turn thinking off.
var updatePlaceholders = map[string]func(any) bool{
	// The store applies only a non-empty status.
	"status": func(v any) bool { return v == nil || v == "" },
	// The store skips an empty note; "null" text is a filler by ruling (#3879).
	"notes": func(v any) bool {
		text, isString := v.(string)
		trimmed := strings.TrimSpace(text)
		return v == nil || (isString && (trimmed == "" || strings.EqualFold(trimmed, "null")))
	},
	// A nil list means no change; only [0] clears.
	"depends_on": func(v any) bool {
		list, isList := v.([]any)
		return v == nil || (isList && len(list) == 0)
	},
	// normalizeTaskEffort turns "", whitespace and "inherit" into "", which
	// the store reads as no change.
	"reasoning_effort": func(v any) bool {
		effort, isString := v.(string)
		return v == nil || (isString && normalizeTaskEffort(effort) == "")
	},
}

// normalizeTaskListArgs is task_list's NormalizeArgs. It drops each update
// field whose value is a placeholder (updatePlaceholders). An entry left with only a
// valid id changed nothing: beside other work it is dropped, so the rest of
// the call still applies. When nothing else is left, the bare entries stay,
// so decoding rejects the call in the tool, where the failure breaker records
// it under this normalized form. Dispatch, the fingerprint and decoding all
// run it, which keeps the placeholder rule in one place. It never fails.
func normalizeTaskListArgs(args map[string]any) (map[string]any, error) {
	rawUpdates, isList := args["update"].([]any)
	if !isList || len(rawUpdates) == 0 {
		return args, nil
	}
	cleanedUpdates := make([]any, 0, len(rawUpdates))
	kept := make([]any, 0, len(rawUpdates))
	for _, raw := range rawUpdates {
		entry, isObject := raw.(map[string]any)
		if !isObject {
			cleanedUpdates = append(cleanedUpdates, raw)
			kept = append(kept, raw)
			continue
		}
		cleaned := maps.Clone(entry)
		for field, isPlaceholder := range updatePlaceholders {
			if value, has := cleaned[field]; has && isPlaceholder(value) {
				delete(cleaned, field)
			}
		}
		cleanedUpdates = append(cleanedUpdates, cleaned)
		// Only an entry left with a well-formed id may be dropped; a
		// malformed one stays so validation rejects the whole call.
		if _, validID := taskIDValue(cleaned["id"]); validID && len(cleaned) == 1 && len(entry) > 1 {
			continue
		}
		kept = append(kept, cleaned)
	}
	// A present add counts as other work unless it is an empty list, so an
	// add of the wrong type still reaches validation.
	rawAdd, hasAdd := args["add"]
	addList, addIsList := rawAdd.([]any)
	otherWork := len(kept) > 0 || (hasAdd && (!addIsList || len(addList) > 0))
	normalized := maps.Clone(args)
	switch {
	case !otherWork:
		normalized["update"] = cleanedUpdates
	case len(kept) == 0:
		delete(normalized, "update")
	default:
		normalized["update"] = kept
	}
	return normalized, nil
}

// maxTaskID bounds task ids below 2^53, so every accepted id is exact in
// float64 and dispatch (float64) and the fingerprint (int64) accept the same
// set: JSON 2^53+1 decodes to float64 2^53, which must not be accepted.
const maxTaskID = 1<<53 - 1

// taskIDValue reads a task id: an integer from 1 to maxTaskID. It takes the
// number types NormalizeArgs may see: float64 from dispatch's decoder, and
// int64 or (past int64) json.Number from the failure fingerprint's canonical
// view.
func taskIDValue(v any) (int, bool) {
	var id int64
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || n < 1 || n > maxTaskID {
			return 0, false
		}
		id = int64(n)
	case int64:
		id = n
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			return 0, false
		}
		id = parsed
	default:
		return 0, false
	}
	if id < 1 || id > maxTaskID {
		return 0, false
	}
	return int(id), true
}

func registerTaskTools(reg *tool.Registry, deps *toolDeps) {
	// Task management.
	_ = reg.Register(tool.RegisteredTool{
		Definition:    tool.DefTaskList(deps.reasoningEffortLevels),
		NormalizeArgs: normalizeTaskListArgs,
		PreValidate:   validateTaskListArgs,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			deps.taskGuard.MarkUsed()
			store := deps.taskGuard.Store()
			adds, updates, err := decodeTaskArgs(args)
			if err != nil {
				return nil, err
			}
			if err := store.LoadError(); err != nil {
				// A first load can fail transiently before the task file is
				// readable again. Retry the existing read path here; a continued
				// failure leaves the store fenced and preserves its bytes.
				if reloadErr := store.Load(); reloadErr != nil {
					if hook := deps.taskGuard.afterFailedTaskReload; hook != nil {
						hook()
					}
					return nil, fmt.Errorf("%w: %w", taskpkg.ErrTaskStoreUnavailable, reloadErr)
				}
			}
			if len(adds) == 0 && len(updates) == 0 {
				// Bare or all-empty call: view. (Empty arrays decode to nil
				// slices; a mutation with nothing to mutate is the view.)
				if hook := deps.taskGuard.beforeBareTaskView; hook != nil {
					hook()
				}
				tasks, err := store.ViewWithError()
				if err != nil {
					return nil, err
				}
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
				// The store reports which task IDs this batch actually
				// transitioned into a terminal status - the same per-update
				// rule that mints CompletedAt - so the snapshot's marker and
				// the stamp cannot disagree (a round-trip batch restamps AND
				// reports; a reassertion does neither).
				settled := mutation.Settled
				var completedAny bool
				var manuallyStartedID int
				seenIDs := make(map[int]struct{}, len(updates))
				for _, u := range updates {
					if _, seen := seenIDs[u.ID]; seen {
						continue
					}
					seenIDs[u.ID] = struct{}{}
					status := afterByID[u.ID].Status
					// Only a real settle counts as completion for the
					// auto-advance and steering below: an annotation that
					// re-asserts an already-terminal status must not start
					// the next task as if work had finished.
					if settled[u.ID] {
						completedAny = true
					}
					if status == taskpkg.TaskInProgress {
						if previous[u.ID] != taskpkg.TaskInProgress {
							started[u.ID] = true
							manuallyStartedID = u.ID
						}
					}
				}

				var postCommitErr error
				var postCommitAdvice string
				// If the agent explicitly started a task, fire its current-task
				// steering so the SYSTEM-REMINDER for the new task shows up on
				// the next turn.
				if manuallyStartedID != 0 {
					// Inside the task_list handler: the tool is registered by
					// construction, so the steering may name it.
					if err := deps.steer(formatCurrentTaskSteering(afterByID[manuallyStartedID], true), events.SteeringKindCurrentTask); err != nil {
						postCommitErr = fmt.Errorf("post-commit current-task steering failed: %w", err)
						postCommitAdvice = "The task update was committed, but the current-task reminder was not delivered; inspect the committed task state before choosing the next update."
					}
				}

				if !completedAny && manuallyStartedID == 0 {
					summary := taskpkg.Summarize(mutation.After)
					deps.emit(events.EventTaskUpdated, taskUpdatedData(summary, "", epoch, revision))
					return tool.StateResult{
						// Every successful mutation output carries Progress:
						// the card's footer (aggregate, meter, and the
						// Open-list affordance) renders from it, so a mixed
						// add + non-terminal update must not lose the footer.
						Output: fmt.Sprintf("%s Progress: %s.", formatMutationAck(len(adds), updates), summary.ProgressText()),
						State:  taskToolStateSnapshot(mutation.After, started, settled),
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
							auto, err := store.UpdateWithSnapshot([]taskpkg.TaskUpdate{{ID: next.ID, Status: taskpkg.TaskInProgress}})
							if err != nil {
								postCommitErr = fmt.Errorf("post-commit auto-advance failed: %w", err)
								postCommitAdvice = "The terminal update was committed, but the next-task update failed; inspect the committed task state and resolve the reported cause before choosing the next task update."
							} else {
								finalTasks = auto.After
								started[next.ID] = true
								if err := deps.steer(formatCurrentTaskSteering(next, true), events.SteeringKindCurrentTask); err != nil {
									postCommitErr = fmt.Errorf("post-commit auto-advance steering failed: %w", err)
									postCommitAdvice = "The terminal update and auto-advance were committed, but the current-task reminder was not delivered; inspect the committed task state before choosing the next update."
								}
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
								if err := deps.sendTaskCompletionSteering(taskReminderTerminalWhileDelegatesRun(deps.resultToolName(), blockingDelegateIDs, summary.AllDone()), blockingDelegateIDs); err != nil {
									postCommitErr = fmt.Errorf("post-commit task-completion steering failed: %w", err)
									postCommitAdvice = "The terminal update was committed, but the completion reminder was not delivered; inspect the committed task state before choosing the next update."
								} else if len(blockingDelegateIDs) == 0 {
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
				if postCommitErr != nil {
					msg.WriteString(postCommitErr.Error())
					msg.WriteString(" ")
					msg.WriteString(postCommitAdvice)
					msg.WriteString(" ")
				}
				fmt.Fprintf(&msg, "Progress: %s.", summary.ProgressText())
				return tool.StateResult{Output: msg.String(), State: taskToolStateSnapshot(finalTasks, started, settled)}, nil
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
