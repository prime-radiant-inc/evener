package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/llm"
)

func registerCommunicateTool(reg *tool.Registry, deps *toolDeps) {
	// communicate is the only user-facing message channel.
	// Use the profile's definition if available (it may have been modified by
	// WithAllowedDecisions to add extra fields to the output schema).
	// Fall back to the base definition otherwise.
	resultToolDef := tool.DefCommunicateNamed(deps.resultToolName())
	if existing := reg.Get(deps.resultToolName()); existing != nil {
		resultToolDef = existing.Definition
	}
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: resultToolDef},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
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
				return nil, errors.New("communicate requires await_reply")
			}

			originalOutput := normalizeNodeOutput(args["output"])
			if message == "" && strings.TrimSpace(originalOutput.Message) != "" {
				message = strings.TrimSpace(originalOutput.Message)
			}
			if message == "" {
				return nil, errors.New("communicate requires message or output.message")
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

			deps.emit(events.EventCommunicate, events.CommunicateData{
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

func registerSkillTool(reg *tool.Registry, deps *toolDeps) {
	// use_skill (progressive disclosure of skill instructions).
	// Present for provider profiles that include the use_skill tool definition.
	if reg.Get("use_skill") != nil {
		_ = reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: tool.DefUseSkill()},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				_ = ctx
				_ = env
				skillName := fmt.Sprint(args["skill_name"])
				meta, ok := deps.skill(skillName)
				if !ok {
					return nil, fmt.Errorf("skill %q not found", skillName)
				}
				deps.emit(events.EventSkillActivated, events.SkillActivatedData{Name: skillName})
				body, err := skill.LoadSkillBody(meta)
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
