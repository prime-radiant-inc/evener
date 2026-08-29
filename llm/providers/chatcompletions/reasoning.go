package chatcompletions

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// applyThinkingFormat writes the reasoning control in the row's dialect
// (spec §8.4, the table kept verbatim from openaicompat's
// applyThinkingFormat). The effort arrives already clamped by ShapeRequest;
// none sends nothing on every dialect; ThinkingAlwaysOn with no effort
// sends the enable object, or medium on the two dialects that carry a
// default effort.
func applyThinkingFormat(body map[string]any, req llm.Request, caps registry.Caps) {
	if caps.Reasoning != nil && !*caps.Reasoning {
		return
	}
	explicit := req.ReasoningEffort != nil
	wire := ""
	if explicit {
		wire = *req.ReasoningEffort
	}
	if wire == "none" {
		return
	}
	alwaysOn := registry.BoolValue(caps.ThinkingAlwaysOn)
	if wire == "" {
		if !alwaysOn {
			return
		}
		wire = llm.ClampReasoningEffort("medium", caps.EffortValues)
	}
	capable := caps.EffortCapable()
	switch registry.StringValue(caps.ThinkingFormat) {
	case "", "openai":
		if capable {
			body["reasoning_effort"] = wire
		}
	case "openrouter":
		if explicit {
			body["reasoning"] = map[string]any{"effort": wire}
		} else {
			body["reasoning"] = map[string]any{"enabled": true}
		}
	case "zai":
		body["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
		if explicit && capable {
			body["reasoning_effort"] = wire
		}
	case "deepseek":
		body["thinking"] = map[string]any{"type": "enabled"}
		if explicit && capable {
			body["reasoning_effort"] = wire
		}
	case "together":
		body["reasoning"] = map[string]any{"enabled": true}
		if explicit && capable {
			body["reasoning_effort"] = wire
		}
	case "qwen":
		body["enable_thinking"] = true
	case "qwen-chat-template":
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": true, "preserve_thinking": true}
	case "chat-template":
		if len(caps.ChatTemplateKwargs) > 0 {
			body["chat_template_kwargs"] = caps.ChatTemplateKwargs
		}
	case "string-thinking":
		body["thinking"] = wire
	}
}
