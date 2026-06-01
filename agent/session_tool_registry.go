package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
)

// toolDeps is the dependency surface the core tool handler closures need from
// their owning session. The registerXxxTools helpers capture a *toolDeps
// instead of a *Session, which cuts the tools⇄session back-cycle: the handler
// closures no longer reference the concrete *Session type. Every member
// forwards to an existing *Session method or field, preserving all locking and
// ordering; toolDeps adds no behavior of its own.
//
// Subagent spawn/wait/send/close are deliberately NOT here — that is a separate
// seam. registerSubagentTools still captures *Session directly.
type toolDeps struct {
	// emit publishes a session event (best-effort, same as Session.emit).
	emit func(kind EventKind, data EventData)

	// steering queue access for the communicate handler.
	steer           func(msg string)
	drainSteering   func() []steeringMessage
	prependSteering func(entries []steeringMessage)

	// abort returns a non-nil error when the session is closing (= Session.abortIfClosing).
	abort func(ctx context.Context) error

	// resultToolName is the effective name of the communicate tool.
	resultToolName func() string

	// cmdTimeouts is a live getter for the default and max shell command
	// timeouts. It reads cfg on every call so SetTimeout mutations are visible;
	// the values are NOT snapshotted at registration time.
	cmdTimeouts func() (def, max int)

	// readGuard exposes the read-before-write guardrail without leaking the raw
	// readFiles map or its mutex.
	readGuard readGuard

	// taskGuard exposes task-store access and the task reminder bookkeeping,
	// all guarded by the session's own mutex.
	taskGuard taskGuard

	// web exposes the web tools with the profile and client hidden behind them.
	web webDeps

	// setCommunicateResult records the communicate tool's result on the session
	// (fields stay Session-owned; this is the only writer reachable from the handler).
	setCommunicateResult func(awaitReply bool, message, reply, output string)

	// skill looks up a discovered skill by name.
	skill func(name string) (SkillMeta, bool)

	// reasoningEffortLevels is captured once for the task_list tool definition.
	reasoningEffortLevels []string

	// webSearchEnabled is the resolved decision (BehaviorTag == "google") for
	// whether the function-tool web_search should be registered.
	webSearchEnabled bool
}

// readGuard wraps the read-before-write guardrail. It forwards to the
// Session-owned readFiles map + mutex via TrackRead/ReadBeforeWriteWarning so
// the handlers never touch the raw map.
type readGuard struct {
	trackRead              func(path string)
	readBeforeWriteWarning func(path string) string
}

func (g readGuard) TrackRead(path string) { g.trackRead(path) }

func (g readGuard) ReadBeforeWriteWarning(path string) string {
	return g.readBeforeWriteWarning(path)
}

// taskGuard is a thin facade over Session-owned task state. It uses the same
// s.mu as the rest of the session — it does NOT introduce a second mutex.
type taskGuard struct {
	getOrCreateTaskStore func() *TaskStore
	markUsed             func()
	setReasoningEffort   func(effort string)
}

func (g taskGuard) Store() *TaskStore { return g.getOrCreateTaskStore() }

// MarkUsed records that the task_list tool was invoked this round (updates the
// reminder counters under s.mu).
func (g taskGuard) MarkUsed() { g.markUsed() }

func (g taskGuard) SetReasoningEffort(effort string) { g.setReasoningEffort(effort) }

// webDeps holds the bound web tool functions. The profile and client stay
// hidden inside the closures captured here.
type webDeps struct {
	fetch  func(ctx context.Context, rawURL, question string) (any, error)
	search func(ctx context.Context, query string) (any, error)
}

// newToolDeps builds the tool dependency surface from a session. Every member
// is a forwarder to an existing method or field, so behavior and locking are
// unchanged. Built once in registerCoreTools.
func newToolDeps(s *Session) *toolDeps {
	return &toolDeps{
		emit:            s.emit,
		steer:           s.Steer,
		drainSteering:   s.drainSteering,
		prependSteering: s.prependSteering,
		abort:           s.abortIfClosing,
		resultToolName:  s.resultToolName,
		cmdTimeouts: func() (int, int) {
			return s.cfg.DefaultCommandTimeoutMS, s.cfg.MaxCommandTimeoutMS
		},
		readGuard: readGuard{
			trackRead:              s.trackReadFile,
			readBeforeWriteWarning: s.readBeforeWriteWarning,
		},
		taskGuard: taskGuard{
			getOrCreateTaskStore: s.getOrCreateTaskStore,
			markUsed: func() {
				s.mu.Lock()
				s.taskToolEverUsed = true
				s.taskToolLastRound = s.totalRounds
				s.mu.Unlock()
			},
			setReasoningEffort: s.SetReasoningEffort,
		},
		web: webDeps{
			fetch:  s.webFetch,
			search: s.webSearch,
		},
		setCommunicateResult: func(awaitReply bool, message, reply, output string) {
			s.mu.Lock()
			s.communicated = true
			s.communicateAwaitReply = awaitReply
			s.communicateText = message
			s.communicateReply = reply
			s.communicateOutput = output
			s.mu.Unlock()
		},
		skill: func(name string) (SkillMeta, bool) {
			meta, ok := s.skills[name]
			return meta, ok
		},
		reasoningEffortLevels: s.profile.ReasoningEffortLevels(),
		webSearchEnabled:      s.profile.BehaviorTag() == "google",
	}
}

func registerCoreTools(reg *toolRegistry, s *Session) error {
	deps := newToolDeps(s)
	if err := registerFileTools(reg, deps); err != nil {
		return err
	}
	if err := registerShellTools(reg, deps); err != nil {
		return err
	}
	registerSubagentTools(reg, s)
	registerTaskTools(reg, deps)
	registerWebTools(reg, deps)
	registerCommunicateTool(reg, deps)
	registerSkillTool(reg, deps)
	return nil
}

func registerFileTools(reg *toolRegistry, deps *toolDeps) error {
	// read_file
	if err := reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defReadFile(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			offset := optionalIntArg(args, "offset")
			limit := optionalIntArg(args, "limit")
			purpose, _ := args["purpose"].(string)
			result, err := env.ReadFile(path, offset, limit)
			if err == nil {
				deps.readGuard.TrackRead(path)
				// If the file is an image or document (PDF), return an
				// imageResult so the vision side-channel can process it.
				if img := parseImageResult(path, result); img != nil {
					img.Purpose = purpose
					return *img, nil
				}
				if doc := parseDocumentResult(path, result); doc != nil {
					doc.Purpose = purpose
					return *doc, nil
				}
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// write_file
	if err := reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defWriteFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			warn := deps.readGuard.ReadBeforeWriteWarning(path)
			result, err := env.WriteFile(path, fmt.Sprint(args["content"]))
			if err == nil && warn != "" {
				return warn + fmt.Sprint(result), nil
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// edit_file
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defEditFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			replaceAll := false
			if v, ok := args["replace_all"].(bool); ok {
				replaceAll = v
			}
			warn := deps.readGuard.ReadBeforeWriteWarning(path)
			result, err := env.EditFile(path, fmt.Sprint(args["old_string"]), fmt.Sprint(args["new_string"]), replaceAll)
			if err == nil && warn != "" {
				return warn + fmt.Sprint(result), nil
			}
			return result, err
		},
	})

	return nil
}

func registerShellTools(reg *toolRegistry, deps *toolDeps) error {
	// shell
	if err := reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defShell()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			cmd := fmt.Sprint(args["command"])
			defTimeout, maxTimeout := deps.cmdTimeouts()
			timeout := defTimeout
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			if maxTimeout > 0 && timeout > maxTimeout {
				timeout = maxTimeout
			}
			res, err := env.ExecCommand(ctx, cmd, timeout, "", nil)

			// Return a line-oriented tool output so line truncation works as intended for shell output.
			var b strings.Builder
			if strings.TrimSpace(res.Stdout) != "" {
				b.WriteString(res.Stdout)
				if !strings.HasSuffix(res.Stdout, "\n") {
					b.WriteString("\n")
				}
			}
			if strings.TrimSpace(res.Stderr) != "" {
				b.WriteString(res.Stderr)
				if !strings.HasSuffix(res.Stderr, "\n") {
					b.WriteString("\n")
				}
			}
			if errors.Is(err, context.Canceled) && !res.TimedOut {
				b.WriteString("[ERROR: Command was canceled before completion. Partial output is shown above.]\n")
			} else if res.TimedOut {
				b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]\n", timeout))
			}
			b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
			return b.String(), err
		},
	}); err != nil {
		return err
	}

	// list_dir (Gemini-aligned)
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defListDir(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["path"])
			depth := 1
			if v, ok := args["depth"].(float64); ok && int(v) > 0 {
				depth = int(v)
			}
			return env.ListDirectory(path, depth)
		},
	})

	// grep
	if err := reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defGrep(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			glob := fmt.Sprint(args["glob_filter"])
			ci := false
			if v, ok := args["case_insensitive"].(bool); ok {
				ci = v
			}
			maxRes := 100
			if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
				maxRes = int(v)
			}
			outputMode := ""
			if v, ok := args["output_mode"].(string); ok {
				outputMode = v
			}
			return env.Grep(pat, path, glob, ci, maxRes, outputMode)
		},
	}); err != nil {
		return err
	}

	// glob
	if err := reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defGlob(), ReadOnly: true},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			matches, err := env.Glob(pat, path)
			if err != nil {
				return "", err
			}
			return strings.Join(matches, "\n"), nil
		},
	}); err != nil {
		return err
	}

	// apply_patch (OpenAI-specific; best-effort implementation lives in this repo)
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defApplyPatch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := fmt.Sprint(args["patch"])
			return applyPatch(env.WorkingDirectory(), patch)
		},
	})

	return nil
}

func registerSubagentTools(reg *toolRegistry, s *Session) {
	// Subagent tools (best-effort; synchronous completion for v1).
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defSpawnAgent()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			task := fmt.Sprint(args["task"])
			model := ""
			if v, ok := args["model"]; ok && v != nil {
				model = fmt.Sprint(v)
			}
			maxTurns := 0
			if v, ok := args["max_turns"].(float64); ok {
				maxTurns = int(v)
			}
			agentType := ""
			if v, ok := args["agent_type"]; ok && v != nil {
				agentType = fmt.Sprint(v)
			}
			blocking := false
			if v, ok := args["blocking"].(bool); ok {
				blocking = v
			}
			reasoningEffort := ""
			if v, ok := args["reasoning_effort"]; ok && v != nil {
				reasoningEffort = fmt.Sprint(v)
			}
			var grantTools []string
			if rawList, ok := args["grant_tools"].([]any); ok {
				for _, item := range rawList {
					s, ok := item.(string)
					if !ok {
						continue
					}
					grantTools = append(grantTools, s)
				}
			}
			var parentTasks []TaskTemplate
			if rawList, ok := args["task_list"].([]any); ok {
				for _, item := range rawList {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					tt := TaskTemplate{}
					if v, ok := m["title"].(string); ok {
						tt.Title = v
					}
					if v, ok := m["prompt"].(string); ok {
						tt.Prompt = v
					}
					if v, ok := m["reasoning_effort"].(string); ok {
						tt.ReasoningEffort = v
					}
					parentTasks = append(parentTasks, tt)
				}
			}
			result, err := s.spawnAgent(ctx, task, model, "", maxTurns, agentType, reasoningEffort, parentTasks, grantTools)
			if err != nil || !blocking {
				return result, err
			}
			// Blocking mode: extract agent_id and wait for completion.
			var spawnResult map[string]any
			if err := json.Unmarshal([]byte(result.(string)), &spawnResult); err != nil {
				return result, nil
			}
			agentID, _ := spawnResult["agent_id"].(string)
			if agentID == "" {
				return result, nil
			}
			waitResult, waitErr := s.waitAgent(ctx, agentID, 0) // 0 = wait indefinitely
			// Include agent_id in the blocking result so the caller can
			// use resume_agent later if needed (e.g. to iterate with a planner).
			if waitStr, ok := waitResult.(string); ok {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(waitStr), &parsed); err == nil {
					parsed["agent_id"] = agentID
					b, _ := json.Marshal(parsed)
					return string(b), waitErr
				}
			}
			return waitResult, waitErr
		},
	})
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defSendInput()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			agentID := fmt.Sprint(args["agent_id"])
			// Append task_list items before sending input.
			if rawList, ok := args["task_list"].([]any); ok && len(rawList) > 0 {
				var items []TaskInput
				for _, item := range rawList {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					ti := TaskInput{
						Description: fmt.Sprint(m["title"]),
						Prompt:      fmt.Sprint(m["prompt"]),
					}
					if v, ok := m["reasoning_effort"].(string); ok {
						ti.ReasoningEffort = v
					}
					items = append(items, ti)
				}
				if sub := s.subagents.get(agentID); sub != nil {
					store := sub.sess.getOrCreateTaskStore()
					store.Append(items)
				}
			}
			result, err := s.sendInput(ctx, agentID, fmt.Sprint(args["message"]))
			if err != nil {
				return result, err
			}
			blocking, _ := args["blocking"].(bool)
			if !blocking {
				return result, nil
			}
			// Blocking mode: wait for the agent to finish and return its result.
			waitResult, waitErr := s.waitAgent(ctx, agentID, 0)
			if waitStr, ok := waitResult.(string); ok {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(waitStr), &parsed); err == nil {
					parsed["agent_id"] = agentID
					b, _ := json.Marshal(parsed)
					return string(b), waitErr
				}
			}
			return waitResult, waitErr
		},
	})
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defWait()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			timeout := 0
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			// Clamp to minimum to prevent rapid-retry burn.
			if timeout > 0 && timeout < minWaitTimeoutMS {
				timeout = minWaitTimeoutMS
			}
			return s.waitAgent(ctx, fmt.Sprint(args["agent_id"]), timeout)
		},
	})
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defCloseAgent()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.closeAgent(fmt.Sprint(args["agent_id"]))
		},
	})
}

func registerTaskTools(reg *toolRegistry, deps *toolDeps) {
	// Task management.
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defTaskList(deps.reasoningEffortLevels)},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
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
					return nil, fmt.Errorf("append requires a non-empty 'tasks' array")
				}
				var items []TaskInput
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("each task must be an object with description and prompt")
					}
					var depIDs []int
					if depsRaw, ok := m["depends_on"].([]any); ok {
						for _, d := range depsRaw {
							if v, ok := d.(float64); ok {
								depIDs = append(depIDs, int(v))
							}
						}
					}
					var taskType TaskType
					if t, ok := m["type"].(string); ok {
						taskType = TaskType(t)
					}
					reasoningEffort := ""
					if re, ok := m["reasoning_effort"].(string); ok {
						reasoningEffort = re
					}
					items = append(items, TaskInput{
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
				return toolStateResult{
					Output: fmt.Sprintf("Added %d task(s). Progress: %d/%d tasks complete.", len(added), done, total),
					State:  store.View(),
				}, nil
			case "update":
				raw, ok := args["updates"].([]any)
				if !ok || len(raw) == 0 {
					return nil, fmt.Errorf("update requires a non-empty 'updates' array")
				}
				var updates []TaskUpdate
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("each update must be an object with id and status")
					}
					id := 0
					if v, ok := m["id"].(float64); ok {
						id = int(v)
					}
					u := TaskUpdate{
						ID:     id,
						Status: TaskStatus(fmt.Sprint(m["status"])),
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
					if u.Status == TaskDone || u.Status == TaskCancelled {
						completedAny = true
					}
					if u.Status == TaskInProgress {
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
					return toolStateResult{Output: "Updated.", State: store.View()}, nil
				}

				var msg strings.Builder
				msg.WriteString("Updated. ")

				if completedAny {
					// Auto-advance unless the agent already picked what to do next.
					if manuallyStartedID == 0 {
						eligible := store.NextEligible()
						if len(eligible) > 0 {
							next := eligible[0]
							if err := store.Update([]TaskUpdate{{ID: next.ID, Status: TaskInProgress}}); err == nil {
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
								if t.Status == TaskOpen || t.Status == TaskInProgress {
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
				return toolStateResult{Output: msg.String(), State: store.View()}, nil
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})
}

func registerWebTools(reg *toolRegistry, deps *toolDeps) {
	// Web fetch.
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: defWebFetch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			rawURL := fmt.Sprint(args["url"])
			question := fmt.Sprint(args["question"])
			return deps.web.fetch(ctx, rawURL, question)
		},
	})

	// Web search (Gemini only — see tool_web_search.go for why).
	// OpenAI and Anthropic handle web search natively via req.WebSearch;
	// registering a function tool named "web_search" for those providers
	// causes a duplicate name collision with the adapter-injected server tool.
	if deps.webSearchEnabled {
		_ = reg.Register(registeredTool{
			Tool: llm.Tool{Definition: defWebSearch()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				query := fmt.Sprint(args["query"])
				return deps.web.search(ctx, query)
			},
		})
	}
}

func registerCommunicateTool(reg *toolRegistry, deps *toolDeps) {
	// communicate is the only user-facing message channel.
	// Use the profile's definition if available (it may have been modified by
	// WithAllowedDecisions to add extra fields to the output schema).
	// Fall back to the base definition otherwise.
	resultToolDef := defCommunicateNamed(deps.resultToolName())
	if existing := reg.Get(deps.resultToolName()); existing != nil {
		resultToolDef = existing.Definition
	}
	_ = reg.Register(registeredTool{
		Tool: llm.Tool{Definition: resultToolDef},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			message := ""
			if v, ok := args["message"]; ok {
				message = strings.TrimSpace(fmt.Sprint(v))
			}
			awaitReply, ok := args["await_reply"].(bool)
			if !ok {
				return nil, fmt.Errorf("communicate requires await_reply")
			}

			originalOutput := normalizeNodeOutput(args["output"])
			if message == "" && strings.TrimSpace(originalOutput.Message) != "" {
				message = strings.TrimSpace(originalOutput.Message)
			}
			if message == "" {
				return nil, fmt.Errorf("communicate requires message or output.message")
			}
			explicitStructuredOutput := hasMeaningfulNodeOutput(originalOutput)
			effectiveOutput := originalOutput
			if strings.TrimSpace(effectiveOutput.Message) == "" {
				effectiveOutput.Message = message
			}
			resultText := message
			structuredText := canonicalNodeOutputText(effectiveOutput)
			if explicitStructuredOutput {
				resultText = structuredText
			}
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}

			deps.emit(EventCommunicate, CommunicateData{
				AwaitReply: awaitReply,
				Message:    message,
			})

			// Drain steering queue into the inbox. The inbox is text-only
			// in the wire shape, so image-bearing entries are also appended
			// as TurnSteering to keep their ContentImage parts available to
			// the next model round.
			drained := deps.drainSteering()
			inbox := make([]string, 0, len(drained))
			var deferred []steeringMessage
			for _, msg := range drained {
				if strings.TrimSpace(msg.Text) != "" {
					inbox = append(inbox, msg.Text)
				}
				if len(msg.Images) > 0 {
					deferred = append(deferred, msg)
				}
			}
			deps.prependSteering(deferred)

			deps.setCommunicateResult(awaitReply, message, resultText, structuredText)

			resp := map[string]any{
				"accepted":    true,
				"await_reply": awaitReply,
				"inbox":       inbox,
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	})
}

func registerSkillTool(reg *toolRegistry, deps *toolDeps) {
	// use_skill (progressive disclosure of skill instructions).
	// Present for provider profiles that include the use_skill tool definition.
	if reg.Get("use_skill") != nil {
		_ = reg.Register(registeredTool{
			Tool: llm.Tool{Definition: defUseSkill()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				_ = ctx
				_ = env
				skillName := fmt.Sprint(args["skill_name"])
				meta, ok := deps.skill(skillName)
				if !ok {
					return nil, fmt.Errorf("skill %q not found", skillName)
				}
				deps.emit(EventSkillActivated, SkillActivatedData{Name: skillName})
				body, err := LoadSkillBody(meta)
				if err != nil {
					return nil, fmt.Errorf("loading skill %q: %w", skillName, err)
				}
				return fmt.Sprintf("Skill: %s\nLocation: %s\n\n---\n\n%s", skillName, meta.Dir, body), nil
			},
		})
	}
}

type nodeOutput struct {
	Decision  string         `json:"decision,omitempty"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
	Artifacts []string       `json:"artifacts"`
}

func normalizeNodeOutput(raw any) nodeOutput {
	out := nodeOutput{
		Message:   "",
		Data:      map[string]any{},
		Artifacts: []string{},
	}
	if raw == nil {
		return out
	}
	if typed, ok := raw.(nodeOutput); ok {
		if typed.Data == nil {
			typed.Data = map[string]any{}
		}
		if typed.Artifacts == nil {
			typed.Artifacts = []string{}
		}
		return typed
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return out
	}

	if d, ok := m["decision"].(string); ok {
		out.Decision = d
	}
	if msg, ok := m["message"].(string); ok {
		out.Message = msg
	} else if v, ok := m["message"]; ok && v != nil {
		out.Message = fmt.Sprint(v)
	}
	if data, ok := m["data"].(map[string]any); ok {
		out.Data = data
	}
	if arts, ok := m["artifacts"]; ok {
		switch v := arts.(type) {
		case []string:
			out.Artifacts = append([]string{}, v...)
		case []any:
			out.Artifacts = make([]string, 0, len(v))
			for _, a := range v {
				out.Artifacts = append(out.Artifacts, fmt.Sprint(a))
			}
		}
	}
	return out
}

func hasMeaningfulNodeOutput(out nodeOutput) bool {
	return strings.TrimSpace(out.Decision) != "" ||
		strings.TrimSpace(out.Message) != "" ||
		len(out.Data) > 0 ||
		len(out.Artifacts) > 0
}

func canonicalNodeOutputText(raw any) string {
	out := normalizeNodeOutput(raw)
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}
