package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const toolPurposeDescription = "A short verb-first gerund phrase naming what this call is doing, e.g. \"Reading the config file\" or \"Searching for the handler\". Keep it to a few words so it renders nicely as an inline activity label."

// maxToolArgumentBytes caps the size of a tool call's raw argument payload
// before it is parsed, so a runaway generation can't push a multi-hundred-KB
// blob through JSON unmarshaling and schema validation for no useful reason.
// This must stay above agent/jobs.go's maxPersistedStructuredResultJSONBytes
// (1MB) — that constant governs how large a communicate structured-result
// payload may legitimately be before it's gracefully dropped at persistence
// time, and a lower registry-level cap would hard-reject the tool call
// before it ever reached that graceful path. (Cross-package constant:
// agent/internal/tool cannot import agent to derive this by reference, so
// keep the two values in sync by comment.)
const maxToolArgumentBytes = 2 * 1024 * 1024

func WithPurposeParameter(td llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(td.Parameters)
	if params == nil {
		params = map[string]any{"type": "object"}
	}
	if params["type"] != nil && params["type"] != "object" {
		td.Parameters = params
		return td
	}
	params["type"] = "object"
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		params["properties"] = props
	}
	if _, exists := props["purpose"]; !exists {
		props["purpose"] = map[string]any{
			"type":        "string",
			"description": toolPurposeDescription,
		}
	}
	td.Parameters = params
	return td
}

func WithoutPurposeParameter(td llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(td.Parameters)
	props, _ := params["properties"].(map[string]any)
	if props != nil {
		delete(props, "purpose")
	}
	switch required := params["required"].(type) {
	case []string:
		params["required"] = removeRequiredField(required, "purpose")
	case []any:
		params["required"] = removeRequiredFieldAny(required, "purpose")
	}
	td.Parameters = params
	return td
}

func removeRequiredField(values []string, field string) []string {
	out := values[:0]
	for _, value := range values {
		if value != field {
			out = append(out, value)
		}
	}
	return out
}

func removeRequiredFieldAny(values []any, field string) []any {
	out := values[:0]
	for _, value := range values {
		if s, ok := value.(string); ok && s == field {
			continue
		}
		out = append(out, value)
	}
	return out
}

func CloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneSchemaValue(v)
	}
	return out
}

func cloneSchemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return CloneSchemaMap(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneSchemaValue(item)
		}
		return out
	case []string:
		return append([]string(nil), x...)
	case []int:
		return append([]int(nil), x...)
	case []float64:
		return append([]float64(nil), x...)
	case []bool:
		return append([]bool(nil), x...)
	default:
		return v
	}
}

// ExecResult holds the outcome of executing a single tool call, including
// the truncated output sent to the model, the full untruncated output, timing,
// and any image or state side-channel data.
type ExecResult struct {
	ToolName string
	CallID   string

	// Output is the truncated output sent back to the model.
	Output string

	// FullOutput is the untruncated output (available via TOOL_CALL_END).
	FullOutput string

	IsError bool

	// PrevalOnly is true when this result came from execTool's pre-dispatch
	// repair step rejecting the call (kata hgm1) rather than from actually
	// running the tool - ExecuteCall below never saw this call at all.
	// Meaningless when IsError is false.
	PrevalOnly bool

	// DurationMS is the wall-clock duration of the tool execution in milliseconds.
	DurationMS int64

	// ImageData and ImageMediaType carry image bytes when a tool returns
	// an ImageResult (e.g. read_file on a PNG). Providers include these
	// alongside the text output so the model can "see" the image.
	ImageData      []byte
	ImageMediaType string
	ImagePurpose   string // from the caller: what they hope to learn

	// ToolState is an optional JSON-encoded snapshot emitted alongside
	// Output via the TOOL_CALL_END event. The LLM never sees this — it's a
	// side channel for dashboards and other consumers that would otherwise
	// have to reconstruct state by replaying the whole event stream.
	ToolState json.RawMessage

	// Err is the raw error the tool executor returned, preserved verbatim so a
	// caller can type-inspect it after FullOutput has already been rendered —
	// M7's escalation seam recovers the typed sandbox.DeniedError via
	// sandbox.AsDenied(res.Err). Nil on success. Never serialized (it rides only
	// in-process, between ExecuteCall and its immediate caller).
	Err error `json:"-"`
}

// StateResult is returned by tool executors that want to emit a
// structured state snapshot alongside the terse string reply. Output is
// what goes to the LLM; State is JSON-marshaled into TOOL_CALL_END's
// tool_state field.
type StateResult struct {
	Output string
	State  any
}

// TextResult is returned by executors that need different text for the model
// and for the full TOOL_CALL_END event payload.
type TextResult struct {
	Output     string
	FullOutput string
}

// ImageResult is returned by tool executors (e.g. read_file) when a file is
// an image. Text is the human-readable description sent as the tool output;
// Data and MediaType carry the raw image for multimodal models.
type ImageResult struct {
	Text      string
	Data      []byte
	MediaType string
	Purpose   string // what the caller hopes to learn from this image
}

// ParseImageResult checks if ReadFile output is an image response (the [image: ...]
// format produced by execenv.LocalExecutionEnvironment) and extracts the raw bytes.
// Returns nil if the output is not an image.
func ParseImageResult(path, readFileOutput string) *ImageResult {
	if !strings.HasPrefix(readFileOutput, "[image:") {
		return nil
	}
	text, rest, found := strings.Cut(readFileOutput, "\n")
	if !found {
		return nil
	}
	b64 := strings.TrimSpace(rest)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	mt := llm.InferMimeTypeFromPath(path)
	if mt == "" {
		mt = "image/png"
	}
	return &ImageResult{
		Text:      text,
		Data:      data,
		MediaType: mt,
	}
}

// ParseDocumentResult checks if ReadFile output is a document response (the [document: ...]
// format produced by execenv.LocalExecutionEnvironment for PDFs). Returns an ImageResult so the
// same vision side-channel pipeline handles both images and documents.
func ParseDocumentResult(path, readFileOutput string) *ImageResult {
	if !strings.HasPrefix(readFileOutput, "[document:") {
		return nil
	}
	text, rest, found := strings.Cut(readFileOutput, "\n")
	if !found {
		return nil
	}
	b64 := strings.TrimSpace(rest)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	mt := llm.InferMimeTypeFromPath(path)
	if mt == "" {
		mt = "application/pdf"
	}
	return &ImageResult{
		Text:      text,
		Data:      data,
		MediaType: mt,
	}
}

// RegisteredTool is a registered tool: the embedded llm.Tool (definition and
// Execute), its compiled validation schema, output limit, and the agent-layer
// executor that receives the execenv.ExecutionEnvironment.
type RegisteredTool struct {
	llm.Tool    // embeds Definition + Execute
	Schema      *jsonschema.Schema
	Limit       schema.ToolOutputLimit
	OmitPurpose bool
	// Agent-layer executor with environment context.
	Exec func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error)
}

// toolMiddleware is called after argument validation but before tool execution.
// Return a non-nil error to block execution (the error message is returned to the LLM).
type toolMiddleware func(ctx context.Context, toolName string, args map[string]any) error

// Registry is a concurrency-safe collection of registered tools and the
// middleware run before their execution.
type Registry struct {
	mu         sync.RWMutex
	tools      map[string]RegisteredTool
	middleware []toolMiddleware

	// breaker is this dispatch scope's record of repeated identical calls.
	// One registry per session, so the ledger is per-session; it carries its
	// own mutex and is never guarded by r.mu.
	breaker *failureLedger
}

// NewRegistry returns an empty Registry ready for tool registration.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]RegisteredTool{}, breaker: newFailureLedger()}
}

// Clone returns an independent registry with the same registered tools and
// middleware. Compiled schemas are immutable after registration, so clones share
// schema pointers while keeping their tool maps isolated. A clone is a new
// dispatch scope, so it starts with the fresh breaker ledger NewRegistry gives
// it — repeated-call state must never leak out of the session that produced it.
func (r *Registry) Clone() *Registry {
	out := NewRegistry()
	if r == nil {
		return out
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.tools) > 0 {
		out.tools = make(map[string]RegisteredTool, len(r.tools))
		maps.Copy(out.tools, r.tools)
	}
	out.middleware = append([]toolMiddleware(nil), r.middleware...)
	return out
}

// Use appends a middleware to the tool execution pipeline.
// Middleware runs after argument validation but before tool execution.
func (r *Registry) Use(mw toolMiddleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, mw)
}

// Register validates and stores a tool in the registry. It rejects an invalid
// tool name or a missing executor, injects the purpose parameter into work-tool
// definitions, applies a default output limit when none is set, compiles (or
// reuses) the argument schema, and bridges llm.Tool.Execute from Exec when unset.
func (r *Registry) Register(t RegisteredTool) error {
	if err := llm.ValidateToolName(t.Definition.Name); err != nil {
		return err
	}
	if t.OmitPurpose {
		t.Definition = WithoutPurposeParameter(t.Definition)
	} else {
		t.Definition = WithPurposeParameter(t.Definition)
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
		// Reuse already-compiled schema when re-registering a tool (e.g.
		// registerCoreTools re-registers tools from NewRegistry). This
		// avoids recompilation and guards against transient jsonschema
		// library panics from os.Getwd() failures in ephemeral worktrees.
		r.mu.RLock()
		if existing, ok := r.tools[t.Definition.Name]; ok && existing.Schema != nil {
			t.Schema = existing.Schema
		}
		r.mu.RUnlock()
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

// Definitions returns the tool definitions for all registered tools.
func (r *Registry) Definitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition)
	}
	return out
}

// Restrict removes all tools except those in the allowed set.
// The result tool (communicate or its configured alias) is always kept
// (subagents need it to return results). The resultToolName parameter
// specifies the name to preserve; pass empty string for "communicate".
func (r *Registry) Restrict(allowed map[string]bool) {
	r.RestrictKeepingResultTool(allowed, "communicate")
}

// RestrictKeepingResultTool is like Restrict but allows specifying the result tool name.
func (r *Registry) RestrictKeepingResultTool(allowed map[string]bool, resultToolName string) {
	if resultToolName == "" {
		resultToolName = "communicate"
	}
	allowed[resultToolName] = true
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if !allowed[name] {
			delete(r.tools, name)
		}
	}
}

// Remove deletes a single tool from the registry.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// RegisteredNames returns a set of all currently registered tool names.
func (r *Registry) RegisteredNames() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make(map[string]bool, len(r.tools))
	for name := range r.tools {
		names[name] = true
	}
	return names
}

// Unregister deletes the named tool from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Get returns the named registered tool, or nil if it is not registered.
func (r *Registry) Get(name string) *RegisteredTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil
	}
	return &t
}

// OverrideLimits applies per-tool output-limit overrides to already-registered
// tools. For each named tool present in the registry, the non-zero fields of
// the override replace the corresponding fields of that tool's current limit;
// unknown tool names are ignored. A nil or empty map is a no-op.
func (r *Registry) OverrideLimits(overrides map[string]schema.ToolOutputLimit) {
	if len(overrides) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, lim := range overrides {
		t, ok := r.tools[name]
		if !ok {
			continue
		}
		if lim.MaxChars > 0 {
			t.Limit.MaxChars = lim.MaxChars
		}
		if lim.MaxLines > 0 {
			t.Limit.MaxLines = lim.MaxLines
		}
		if lim.Strategy != "" {
			t.Limit.Strategy = lim.Strategy
		}
		r.tools[name] = t
	}
}

// Names returns the names of all registered tools, sorted alphabetically.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ExecuteCall runs a single tool call: it looks up the tool, parses and
// schema-validates the arguments, runs the registered middleware, invokes the
// executor, and returns a truncated ExecResult. Unknown tools, invalid
// arguments, failed validation, blocking middleware, and executor errors are
// each returned as an error result. ImageResult and StateResult values are
// unpacked into the result's image and state fields.
func (r *Registry) ExecuteCall(ctx context.Context, env execenv.ExecutionEnvironment, call llm.ToolCallData) ExecResult {
	name := call.Name
	callID := call.ID
	if strings.TrimSpace(callID) == "" {
		callID = "call_" + shortHash(call.Arguments)
	}

	// A signature that has already failed the same way twice is refused here,
	// before the tool is even looked up, and is deliberately not recorded:
	// recording the refusal's own body would replace the stored hash and
	// release the next identical call. Only failures park — a repeated
	// identical *success* may still be the call that finally sees the world
	// change, and refusing it would strand a session repeating its result tool.
	judged := !breakerBypassed(ctx)
	if judged {
		if failStreak, _, snippets := r.breaker.check(name, call.Arguments); failStreak >= breakerThreshold {
			return truncateResult(name, callID, failureParkText(name, snippets), true, defaultToolLimit(name))
		}
	} else {
		// A human authorized this dispatch, which retires the refusals that
		// led here as evidence. Clearing rather than merely skipping judgement
		// is what keeps a repeatedly-approved call approvable: the grant is
		// per-invocation, so the same call comes back and is denied again, and
		// a streak that only ever grows would park the next one before
		// dispatch — with no typed error left to raise another approval card.
		r.breaker.clearFailures(name, call.Arguments)
	}

	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		msg := "unknown tool: " + name
		return truncateResult(name, callID, msg, true, defaultToolLimit(name))
	}

	if len(call.Arguments) > maxToolArgumentBytes {
		msg := fmt.Sprintf("tool arguments too large: %d bytes exceeds the %d byte limit", len(call.Arguments), maxToolArgumentBytes)
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

	if name != "read_file" {
		delete(args, "purpose")
	}
	v, err := t.Exec(ctx, env, args)
	res := dispatchedResult(name, callID, t.Limit, v, err)
	if judged {
		// Recorded on the untruncated body, before any nudge is appended, so
		// the nudge cannot poison the body hash. FullOutput rather than
		// Output: a TruncTail tool renders every over-limit error behind the
		// same truncation banner, and classing on that banner would read two
		// unrelated failures as one and park a call that never repeated
		// itself. Nudging after truncation is deliberate: the text must
		// survive the limiter.
		judgedBody := res.FullOutput
		if judgedBody == "" {
			judgedBody = res.Output
		}
		failStreak, repeatStreak := r.breaker.record(name, call.Arguments, res.IsError, judgedBody)
		switch {
		case failStreak >= breakerThreshold:
			appendIntervention(&res, failureNudgeText)
		case repeatStreak >= breakerThreshold:
			// Repeats on every subsequent identical result: nothing else
			// applies pressure once repetition is never parked.
			appendIntervention(&res, repetitionNudgeText(repeatStreak))
		}
	}
	return res
}

// dispatchedResult turns an executor's return into an ExecResult, unpacking
// the side-channel result shapes. It is the single point every dispatched
// call funnels through, so the breaker sees every executed result exactly once.
func dispatchedResult(name, callID string, lim schema.ToolOutputLimit, v any, err error) ExecResult {
	if err != nil {
		full := ""
		if v != nil {
			full = toolValueToString(v)
		}
		if strings.TrimSpace(full) == "" {
			full = fmt.Sprintf("%v", err)
		}
		res := truncateResult(name, callID, full, true, lim)
		res.Err = err // preserved verbatim for typed inspection (sandbox.AsDenied)
		return res
	}

	// ImageResult carries both text and raw image bytes.
	if img, ok := v.(ImageResult); ok {
		res := truncateResult(name, callID, img.Text, false, lim)
		res.ImageData = img.Data
		res.ImageMediaType = img.MediaType
		res.ImagePurpose = img.Purpose
		return res
	}

	// StateResult carries an LLM-facing Output plus a JSON-serializable
	// State snapshot that rides along on the TOOL_CALL_END event.
	if st, ok := v.(StateResult); ok {
		res := truncateResult(name, callID, st.Output, false, lim)
		if st.State != nil {
			if data, err := json.Marshal(st.State); err == nil {
				res.ToolState = data
			} else {
				log.Printf("tool %s: marshal StateResult.State: %v", name, err)
			}
		}
		return res
	}

	if text, ok := v.(TextResult); ok {
		res := truncateResult(name, callID, text.Output, false, lim)
		if text.FullOutput != "" {
			res.FullOutput = text.FullOutput
		}
		return res
	}

	full := toolValueToString(v)
	return truncateResult(name, callID, full, false, lim)
}

func truncateResult(toolName, callID, full string, isErr bool, lim schema.ToolOutputLimit) ExecResult {
	out := full
	if lim.Strategy == schema.TruncHeadCount {
		out = truncateHeadCount(out, lim.MaxLines)
	} else {
		out = truncateChars(out, lim.MaxChars, lim.Strategy)
		if lim.MaxLines > 0 {
			out = truncateLines(out, lim.MaxLines)
		}
	}
	return ExecResult{
		ToolName:   toolName,
		CallID:     callID,
		Output:     out,
		FullOutput: full,
		IsError:    isErr,
	}
}

func truncateChars(s string, limit int, strategy schema.TruncationStrategy) string {
	runes := []rune(s)
	if limit <= 0 || len(runes) <= limit {
		return s
	}
	removed := len(runes) - limit
	switch strategy {
	case schema.TruncTail:
		// Spec: keep the last max_chars characters and prepend a warning.
		marker := fmt.Sprintf("[WARNING: Tool output was truncated. First %d characters were removed. The full output is available in the event stream.]\n\n", removed)
		return marker + string(runes[len(runes)-limit:])
	default:
		// Spec: head/tail split plus an explicit warning about omitted middle.
		headCount := limit / 2
		tailCount := limit - headCount
		marker := fmt.Sprintf("\n\n[WARNING: Tool output was truncated. %d characters were removed from the middle. The full output is available in the event stream. If you need to see specific parts, re-run the tool with more targeted parameters.]\n\n", removed)
		return string(runes[:headCount]) + marker + string(runes[len(runes)-tailCount:])
	}
}

// truncateHeadCount bounds glob/grep-shaped output (one match per line) by
// keeping the first maxEntries entries and appending a structural summary —
// total count, shown count, and a hint to narrow the search. Unlike
// truncateChars' TruncTail case, this never drops the head of the result: an
// agent scanning an unscoped search always sees the earliest (and, per
// Glob/Grep's own ordering, most relevant) matches, with an explicit count of
// what was omitted rather than a silently smaller result.
func truncateHeadCount(s string, maxEntries int) string {
	if maxEntries <= 0 || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	total := len(lines)
	if total <= maxEntries {
		return s
	}
	shown := lines[:maxEntries]
	summary := fmt.Sprintf("\n[%d total matches; showing first %d; narrow the pattern to see the rest]", total, maxEntries)
	return strings.Join(shown, "\n") + summary
}

func truncateLines(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= limit {
		return s
	}
	headCount := limit / 2
	tailCount := limit - headCount
	omitted := len(lines) - headCount - tailCount
	marker := fmt.Sprintf("\n[... %d lines omitted ...]\n", omitted)
	head := strings.Join(lines[:headCount], "\n")
	tail := strings.Join(lines[len(lines)-tailCount:], "\n")
	return head + marker + tail
}

func defaultToolLimit(toolName string) schema.ToolOutputLimit {
	switch toolName {
	case "read_file":
		return schema.ToolOutputLimit{MaxChars: 50_000, Strategy: schema.TruncHeadTail}
	case "shell":
		return schema.ToolOutputLimit{MaxChars: 30_000, MaxLines: 512, Strategy: schema.TruncHeadTail}
	case "grep":
		return schema.ToolOutputLimit{MaxLines: 200, Strategy: schema.TruncHeadCount}
	case "glob":
		return schema.ToolOutputLimit{MaxLines: 500, Strategy: schema.TruncHeadCount}
	case "edit_file":
		return schema.ToolOutputLimit{MaxChars: 10_000, Strategy: schema.TruncTail}
	case "apply_patch":
		return schema.ToolOutputLimit{MaxChars: 10_000, Strategy: schema.TruncTail}
	case "write_file":
		return schema.ToolOutputLimit{MaxChars: 1_000, Strategy: schema.TruncTail}
	case "delegate":
		return schema.ToolOutputLimit{MaxChars: 20_000, Strategy: schema.TruncHeadTail}
	case "task_list":
		return schema.ToolOutputLimit{MaxChars: 20_000, Strategy: schema.TruncTail}
	case "web_fetch":
		return schema.ToolOutputLimit{MaxChars: 20_000, Strategy: schema.TruncHeadTail}
	case "communicate":
		return schema.ToolOutputLimit{MaxChars: 5_000, Strategy: schema.TruncTail}
	case "use_skill":
		return schema.ToolOutputLimit{MaxChars: 32_000, Strategy: schema.TruncTail}
	default:
		return schema.ToolOutputLimit{MaxChars: 20_000, Strategy: schema.TruncHeadTail}
	}
}

// compiledSchemaCache memoizes compiled schemas across registries. Tool
// parameter schemas are static (built-in tools are fixed; MCP/plugin tools are
// fixed per server), so compilation — ~2.4ms of every session's registry build —
// is pure repeated work. The compiled *jsonschema.Schema is read-only and safe
// for concurrent validation, so it is shared. Keyed by the marshaled params, so
// distinct schemas never collide; bounded by the number of distinct schemas.
var (
	compiledSchemaMu    sync.Mutex
	compiledSchemaCache = map[string]*jsonschema.Schema{}
)

func compileSchema(params map[string]any) (schema *jsonschema.Schema, err error) {
	return compileSchemaWith(params,
		func(c *jsonschema.Compiler, uri string, r io.Reader) error { return c.AddResource(uri, r) },
		func(c *jsonschema.Compiler, uri string) (*jsonschema.Schema, error) { return c.Compile(uri) },
	)
}

func compileSchemaWith(params map[string]any, addResource func(*jsonschema.Compiler, string, io.Reader) error, compile func(*jsonschema.Compiler, string) (*jsonschema.Schema, error)) (schema *jsonschema.Schema, err error) {
	// The jsonschema library has multiple panic() sites for malformed inputs.
	// Recover so a bad MCP/plugin tool schema doesn't crash the process.
	defer func() {
		if r := recover(); r != nil {
			schema = nil
			err = fmt.Errorf("schema compilation panicked: %v", r)
		}
	}()

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
	key := string(b)
	compiledSchemaMu.Lock()
	if cached, ok := compiledSchemaCache[key]; ok {
		compiledSchemaMu.Unlock()
		return cached, nil
	}
	compiledSchemaMu.Unlock()

	c := jsonschema.NewCompiler()
	// Use an absolute URI so the library never calls filepath.Abs → os.Getwd().
	// A relative URL like "schema.json" triggers os.Getwd() which can panic
	// in transient environments (e.g. deleted git worktrees).
	const schemaURI = "urn:serf:tool-schema"
	if err := addResource(c, schemaURI, bytes.NewReader(b)); err != nil {
		return nil, err
	}
	compiled, err := compile(c, schemaURI)
	if err != nil {
		return nil, err
	}
	compiledSchemaMu.Lock()
	compiledSchemaCache[key] = compiled
	compiledSchemaMu.Unlock()
	return compiled, nil
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
