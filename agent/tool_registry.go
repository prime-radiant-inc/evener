package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/llm"
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

	// ImageData and ImageMediaType carry image bytes when a tool returns
	// an ImageResult (e.g. read_file on a PNG). Providers include these
	// alongside the text output so the model can "see" the image.
	ImageData      []byte
	ImageMediaType string
}

// ImageResult is returned by tool executors (e.g. read_file) when a file is
// an image. Text is the human-readable description sent as the tool output;
// Data and MediaType carry the raw image for multimodal models.
type ImageResult struct {
	Text      string
	Data      []byte
	MediaType string
}

// parseImageResult checks if ReadFile output is an image response (the [image: ...]
// format produced by LocalExecutionEnvironment) and extracts the raw bytes.
// Returns nil if the output is not an image.
func parseImageResult(path, readFileOutput string) *ImageResult {
	if !strings.HasPrefix(readFileOutput, "[image:") {
		return nil
	}
	idx := strings.Index(readFileOutput, "\n")
	if idx < 0 {
		return nil
	}
	b64 := strings.TrimSpace(readFileOutput[idx+1:])
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	mt := llm.InferMimeTypeFromPath(path)
	if mt == "" {
		mt = "image/png"
	}
	return &ImageResult{
		Text:      readFileOutput[:idx],
		Data:      data,
		MediaType: mt,
	}
}

type RegisteredTool struct {
	llm.Tool // embeds Definition + Execute
	Schema   *jsonschema.Schema
	Limit    ToolOutputLimit
	// Agent-layer executor with environment context.
	Exec func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error)
}

// ToolMiddleware is called after argument validation but before tool execution.
// Return a non-nil error to block execution (the error message is returned to the LLM).
type ToolMiddleware func(ctx context.Context, toolName string, args map[string]any) error

type ToolRegistry struct {
	mu         sync.RWMutex
	tools      map[string]RegisteredTool
	middleware []ToolMiddleware
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]RegisteredTool{}}
}

// Use appends a middleware to the tool execution pipeline.
// Middleware runs after argument validation but before tool execution.
func (r *ToolRegistry) Use(mw ToolMiddleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, mw)
}

func (r *ToolRegistry) Register(t RegisteredTool) error {
	if err := llm.ValidateToolName(t.Definition.Name); err != nil {
		return err
	}
	if strings.TrimSpace(t.Definition.Description) == "" {
		log.Printf("WARNING: tool %q registered with empty description", t.Definition.Name)
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
	// Bridge llm.Tool.Execute from Exec if not already set.
	if t.Execute == nil && t.Exec != nil {
		exec := t.Exec // capture for closure
		t.Execute = func(ctx context.Context, args any) (any, error) {
			parsed, ok := args.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("expected map[string]any, got %T", args)
			}
			return exec(ctx, nil, parsed) // nil env for standalone usage
		}
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

// Restrict removes all tools except those in the allowed set.
// The "communicate" tool is always kept (subagents need it to return results).
func (r *ToolRegistry) Restrict(allowed map[string]bool) {
	allowed["communicate"] = true
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if !allowed[name] {
			delete(r.tools, name)
		}
	}
}

// Remove deletes a single tool from the registry.
func (r *ToolRegistry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// RegisteredNames returns a set of all currently registered tool names.
func (r *ToolRegistry) RegisteredNames() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make(map[string]bool, len(r.tools))
	for name := range r.tools {
		names[name] = true
	}
	return names
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

	r.mu.RLock()
	mws := r.middleware
	r.mu.RUnlock()
	for _, mw := range mws {
		if err := mw(ctx, name, args); err != nil {
			return truncateResult(name, callID, err.Error(), true, t.Limit)
		}
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

	// ImageResult carries both text and raw image bytes.
	if img, ok := v.(ImageResult); ok {
		res := truncateResult(name, callID, img.Text, false, t.Limit)
		res.ImageData = img.Data
		res.ImageMediaType = img.MediaType
		return res
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
		return ToolOutputLimit{MaxChars: 30_000, MaxLines: 512, Strategy: TruncHeadTail}
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
	if err := c.AddResource("schema.json", bytes.NewReader(b)); err != nil {
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
	return hexHash(string(b))
}
