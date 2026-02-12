package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/internal/llm"
)

type TruncationStrategy string

const (
	TruncHeadTail TruncationStrategy = "head_tail"
	TruncTail     TruncationStrategy = "tail"
)

type ToolOutputLimit struct {
	MaxChars int                `json:"max_chars,omitempty"`
	MaxLines int                `json:"max_lines,omitempty"`
	Strategy TruncationStrategy `json:"strategy,omitempty"`
}

type ToolExecResult struct {
	ToolName string
	CallID   string

	// Output is the truncated output sent back to the model.
	Output string

	// FullOutput is the untruncated output (available via TOOL_CALL_END).
	FullOutput string

	IsError bool
}

type RegisteredTool struct {
	Definition llm.ToolDefinition
	Schema     *jsonschema.Schema
	Exec       func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error)

	Limit ToolOutputLimit
}

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]RegisteredTool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]RegisteredTool{}}
}

func (r *ToolRegistry) Register(t RegisteredTool) error {
	if err := llm.ValidateToolName(t.Definition.Name); err != nil {
		return err
	}
	if t.Exec == nil {
		return fmt.Errorf("tool %s missing executor", t.Definition.Name)
	}
	if t.Limit.MaxChars == 0 {
		t.Limit = defaultToolLimit(t.Definition.Name)
	}
	if t.Schema == nil {
		s, err := compileSchema(t.Definition.Parameters)
		if err != nil {
			return fmt.Errorf("tool %s schema: %w", t.Definition.Name, err)
		}
		t.Schema = s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = map[string]RegisteredTool{}
	}
	r.tools[t.Definition.Name] = t
	return nil
}

func (r *ToolRegistry) Definitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition)
	}
	return out
}

func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

func (r *ToolRegistry) Get(name string) *RegisteredTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil
	}
	return &t
}

// nameSet returns the current set of registered tool names as a map.
func (r *ToolRegistry) nameSet() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]bool, len(r.tools))
	for name := range r.tools {
		out[name] = true
	}
	return out
}

func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (r *ToolRegistry) ExecuteCall(ctx context.Context, env ExecutionEnvironment, call llm.ToolCallData) ToolExecResult {
	name := call.Name
	callID := call.ID
	if strings.TrimSpace(callID) == "" {
		callID = "call_" + shortHash(call.Arguments)
	}

	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		msg := fmt.Sprintf("unknown tool: %s", name)
		return truncateResult(name, callID, msg, true, defaultToolLimit(name))
	}

	var args map[string]any
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			msg := fmt.Sprintf("invalid tool arguments JSON: %v", err)
			return truncateResult(name, callID, msg, true, t.Limit)
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	if err := t.Schema.Validate(args); err != nil {
		msg := fmt.Sprintf("tool args schema validation failed: %v", err)
		return truncateResult(name, callID, msg, true, t.Limit)
	}

	v, err := t.Exec(ctx, env, args)
	if err != nil {
		full := ""
		if v != nil {
			full = toolValueToString(v)
		}
		if strings.TrimSpace(full) == "" {
			full = fmt.Sprintf("%v", err)
		}
		return truncateResult(name, callID, full, true, t.Limit)
	}

	full := toolValueToString(v)
	return truncateResult(name, callID, full, false, t.Limit)
}

func truncateResult(toolName, callID, full string, isErr bool, lim ToolOutputLimit) ToolExecResult {
	out := full
	out = truncateChars(out, lim.MaxChars, lim.Strategy)
	if lim.MaxLines > 0 {
		out = truncateLines(out, lim.MaxLines)
	}
	return ToolExecResult{
		ToolName:   toolName,
		CallID:     callID,
		Output:     out,
		FullOutput: full,
		IsError:    isErr,
	}
}

func truncateChars(s string, max int, strat TruncationStrategy) string {
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	removed := len(runes) - max
	switch strat {
	case TruncTail:
		// Spec: keep the last max_chars characters and prepend a warning.
		marker := fmt.Sprintf("[WARNING: Tool output was truncated. First %d characters were removed. The full output is available in the event stream.]\n\n", removed)
		return marker + string(runes[len(runes)-max:])
	default:
		// Spec: head/tail split plus an explicit warning about omitted middle.
		headCount := max / 2
		tailCount := max - headCount
		marker := fmt.Sprintf("\n\n[WARNING: Tool output was truncated. %d characters were removed from the middle. The full output is available in the event stream. If you need to see specific parts, re-run the tool with more targeted parameters.]\n\n", removed)
		return string(runes[:headCount]) + marker + string(runes[len(runes)-tailCount:])
	}
}

func truncateLines(s string, max int) string {
	if max <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	headCount := max / 2
	tailCount := max - headCount
	omitted := len(lines) - headCount - tailCount
	marker := fmt.Sprintf("\n[... %d lines omitted ...]\n", omitted)
	head := strings.Join(lines[:headCount], "\n")
	tail := strings.Join(lines[len(lines)-tailCount:], "\n")
	return head + marker + tail
}

func defaultToolLimit(toolName string) ToolOutputLimit {
	switch toolName {
	case "read_file":
		return ToolOutputLimit{MaxChars: 50_000, Strategy: TruncHeadTail}
	case "shell":
		return ToolOutputLimit{MaxChars: 30_000, MaxLines: 256, Strategy: TruncHeadTail}
	case "grep":
		return ToolOutputLimit{MaxChars: 20_000, MaxLines: 200, Strategy: TruncTail}
	case "glob":
		return ToolOutputLimit{MaxChars: 20_000, MaxLines: 500, Strategy: TruncTail}
	case "edit_file":
		return ToolOutputLimit{MaxChars: 10_000, Strategy: TruncTail}
	case "apply_patch":
		return ToolOutputLimit{MaxChars: 10_000, Strategy: TruncTail}
	case "write_file":
		return ToolOutputLimit{MaxChars: 1_000, Strategy: TruncTail}
	case "spawn_agent":
		return ToolOutputLimit{MaxChars: 20_000, Strategy: TruncHeadTail}
	case "task_list":
		return ToolOutputLimit{MaxChars: 20_000, Strategy: TruncTail}
	case "web_fetch":
		return ToolOutputLimit{MaxChars: 20_000, Strategy: TruncHeadTail}
	case "communicate":
		return ToolOutputLimit{MaxChars: 5_000, Strategy: TruncTail}
	case "use_skill":
		return ToolOutputLimit{MaxChars: 32_000, Strategy: TruncTail}
	default:
		return ToolOutputLimit{MaxChars: 20_000, Strategy: TruncHeadTail}
	}
}

func compileSchema(params map[string]any) (*jsonschema.Schema, error) {
	if params == nil {
		// Default to empty object schema.
		params = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	if t, ok := params["type"].(string); ok && t != "object" {
		return nil, fmt.Errorf("tool schema root type must be \"object\", got %q", t)
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", strings.NewReader(string(b))); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

func toolValueToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
