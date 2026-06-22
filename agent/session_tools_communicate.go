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
		Tool:        llm.Tool{Definition: resultToolDef},
		OmitPurpose: true,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}
			message := ""
			if v, ok := args["message"]; ok {
				message = strings.TrimSpace(fmt.Sprint(v))
			}
			endTurn, ok := args["end_turn"].(bool)
			if !ok {
				return nil, errors.New("communicate requires end_turn")
			}

			originalOutput := normalizeNodeOutput(args["output"])
			if message == "" && strings.TrimSpace(originalOutput.Message) != "" {
				message = strings.TrimSpace(originalOutput.Message)
			}
			if message == "" {
				return nil, errors.New("communicate requires message or output.message")
			}
			explicitNodeOutput := hasMeaningfulNodeOutput(originalOutput)
			rawOutput, outputPresent := args["output"]
			usesCustomOutputSchema := !usesDefaultCommunicateOutputEnvelope(resultToolDef)
			explicitStructuredOutput := explicitNodeOutput || (outputPresent && usesCustomOutputSchema) || hasMeaningfulRawOutput(rawOutput)
			effectiveOutput := originalOutput
			if strings.TrimSpace(effectiveOutput.Message) == "" {
				effectiveOutput.Message = message
			}
			resultText := message
			structuredText := canonicalNodeOutputText(effectiveOutput)
			if explicitNodeOutput {
				resultText = structuredText
			}
			if err := deps.abort(ctx); err != nil {
				return nil, err
			}

			deps.emit(events.EventCommunicate, events.CommunicateData{
				EndTurn: endTurn,
				Message: message,
			})

			inbox := []string{}
			if endTurn {
				// Drain steering queue into the inbox for terminal delivery. The
				// inbox is text-only in the wire shape, so image-bearing entries
				// are also appended as TurnSteering to keep their ContentImage
				// parts available to the next model round.
				drained := deps.drainSteering()
				inbox = make([]string, 0, len(drained))
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
			}

			if endTurn {
				deps.setCommunicateResult(message, resultText, structuredText)
				if explicitStructuredOutput {
					deps.setCommunicateStructured(rawOutput)
				}
				if deps.deliverWatchCallback != nil {
					deps.deliverWatchCallback(watchCommunicateCallbackText(message, structuredText))
				}
			}

			resp := map[string]any{
				"accepted": true,
				"end_turn": endTurn,
				"inbox":    inbox,
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	})
}

func watchCommunicateCallbackText(message, output string) string {
	message = strings.TrimSpace(message)
	output = strings.TrimSpace(output)
	if output == "" {
		return "Observer callback:\nmessage: " + message
	}
	return "Observer callback:\nmessage: " + message + "\noutput: " + output
}

func (s *Session) deliverWatchCommunicateCallback(message string) {
	if s == nil || s.currentEntryKind() != EntryWatchDelivery {
		return
	}
	if s.watchCallbackDeliveredForCurrentTurn() {
		return
	}
	steer := s.cfg.spawn.parentSteerDelivered
	if steer == nil {
		return
	}
	if !steer(message, s.activeCausalProvenance()) {
		return
	}
	if mark := s.cfg.spawn.parentMarkCallerCallbackDelivered; mark != nil {
		mark(s.cfg.spawn.parentJobID)
	}
	s.markWatchCallbackDeliveredForCurrentTurn()
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

func usesDefaultCommunicateOutputEnvelope(def llm.ToolDefinition) bool {
	props, _ := def.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	if outProps == nil {
		return false
	}
	for _, name := range []string{"message", "data", "artifacts"} {
		if _, ok := outProps[name]; !ok {
			return false
		}
	}
	required := communicateSchemaStringSlice(output["required"])
	for _, name := range []string{"message", "data", "artifacts"} {
		if !communicateSchemaContains(required, name) {
			return false
		}
	}
	return true
}

func communicateSchemaStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func communicateSchemaContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasMeaningfulRawOutput(raw any) bool {
	if raw == nil {
		return false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return true
	}
	for k, v := range m {
		switch k {
		case "decision", "message":
			if strings.TrimSpace(fmt.Sprint(v)) != "" {
				return true
			}
		case "data":
			if data, ok := v.(map[string]any); ok && len(data) == 0 {
				continue
			}
			if v != nil {
				return true
			}
		case "artifacts":
			switch arts := v.(type) {
			case []string:
				if len(arts) > 0 {
					return true
				}
			case []any:
				if len(arts) > 0 {
					return true
				}
			case nil:
			default:
				return true
			}
		default:
			return true
		}
	}
	return false
}

func canonicalNodeOutputText(raw any) string {
	out := normalizeNodeOutput(raw)
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}
