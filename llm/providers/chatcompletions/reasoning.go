package chatcompletions

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// applyThinkingFormat writes the reasoning control in the row's dialect
// (spec §8.4, the table kept verbatim from openaicompat's
// applyThinkingFormat). The effort arrives already clamped by ShapeRequest;
// an explicit off reaches the wire only on the two dialects that have a value
// for it, and only for a model whose ladder lists the off level;
// ThinkingAlwaysOn with no effort sends the enable object, or medium on the
// two dialects that carry a default effort.
func applyThinkingFormat(body map[string]any, req llm.Request, caps registry.Caps) {
	if caps.Reasoning != nil && !*caps.Reasoning {
		return
	}
	explicit := req.ReasoningEffort != nil
	wire := ""
	if explicit {
		wire = *req.ReasoningEffort
	}
	format := registry.StringValue(caps.ThinkingFormat)
	capable := caps.EffortCapable()
	if wire == "none" {
		// The user turned thinking off. Only these two dialects have a value
		// that says so, and only a model whose ladder lists the off level
		// accepts one; everywhere else the control is omitted. Returning here
		// is what keeps the mandatory-thinking backstop below from switching
		// thinking back on against the user's stated intent — every shape in
		// the table switches it ON.
		if caps.EffortOffCapable() {
			switch format {
			case "", "openai":
				if capable {
					body["reasoning_effort"] = wire
				}
			case "openrouter":
				body["reasoning"] = map[string]any{"effort": wire}
			}
		}
		return
	}
	alwaysOn := registry.BoolValue(caps.ThinkingAlwaysOn)
	if wire == "" {
		if !alwaysOn {
			return
		}
		wire = llm.ClampReasoningEffort("medium", caps.EffortValues)
	}
	// A field that spells the level (reasoning_effort, reasoning.effort,
	// thinking) is written only for a level the row's ladder vouches for. A
	// row with no ladder — an uncatalogued model — gets no level rather than
	// one a provider may reject. The dialects' non-level enable objects below
	// still fire unconditionally, so a mandatory-thinking row keeps thinking
	// on without being told a level it cannot accept.
	level := llm.VouchedEffort(wire, caps.EffortValues)
	switch format {
	case "", "openai":
		if capable && level != "" {
			body["reasoning_effort"] = level
		}
	case "openrouter":
		switch {
		case explicit && level != "":
			body["reasoning"] = map[string]any{"effort": level}
		case alwaysOn:
			body["reasoning"] = map[string]any{"enabled": true}
		}
	case "zai":
		body["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
		if explicit && capable && level != "" {
			body["reasoning_effort"] = level
		}
	case "deepseek":
		body["thinking"] = map[string]any{"type": "enabled"}
		if explicit && capable && level != "" {
			body["reasoning_effort"] = level
		}
	case "together":
		body["reasoning"] = map[string]any{"enabled": true}
		if explicit && capable && level != "" {
			body["reasoning_effort"] = level
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
		if level != "" {
			body["thinking"] = level
		}
	}
}
