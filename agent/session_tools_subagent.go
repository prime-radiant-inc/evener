package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/llm"
)

func registerSubagentTools(reg *tool.Registry, s *Session, deps *toolDeps) {
	// Subagent tools (best-effort; synchronous completion for v1).
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefSpawnAgent()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
			var parentTasks []taskpkg.TaskTemplate
			if rawList, ok := args["task_list"].([]any); ok {
				for _, item := range rawList {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					tt := taskpkg.TaskTemplate{}
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
				return result, nil //nolint:nilerr // spawn succeeded; unparseable result just skips the blocking-wait enhancement
			}
			agentID, _ := spawnResult["agent_id"].(string)
			if agentID == "" {
				return result, nil
			}
			return s.waitAgent(ctx, agentID, 0) // 0 = wait indefinitely
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefSendInput()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			agentID := fmt.Sprint(args["agent_id"])
			// Append task_list items before sending input.
			if rawList, ok := args["task_list"].([]any); ok && len(rawList) > 0 {
				var items []taskpkg.TaskInput
				for _, item := range rawList {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					ti := taskpkg.TaskInput{
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
					if _, err := store.Append(items); err != nil {
						return nil, fmt.Errorf("seed subagent task list: %w", err)
					}
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
			return s.waitAgent(ctx, agentID, 0)
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefWait()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefCloseAgent()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.closeAgent(fmt.Sprint(args["agent_id"]))
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefCancelAgent()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.cancelAgent(fmt.Sprint(args["agent_id"]))
		},
	})
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefListAgents()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			status := ""
			if v, ok := args["status"]; ok && v != nil {
				status = fmt.Sprint(v)
			}
			includeClosed := false
			if v, ok := args["include_closed"].(bool); ok {
				includeClosed = v
			}
			return s.listAgents(status, includeClosed)
		},
	})
	// subagent_output is the root-only, non-consuming diagnostic. It closes over
	// both s (for getSub on view=result) and deps (for the transcript read path).
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefSubagentOutput(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return execSubagentOutput(s, deps, args)
		},
	})
}
