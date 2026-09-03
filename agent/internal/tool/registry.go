package tool

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const toolIntentDescription = "Describe what you expect to learn or accomplish from this tool call, using a verb-first gerund. Make the expected outcome clear to the user and your future self; e.g. \"Reading config to identify the active profile\" or \"Searching handlers to locate request routing.\""

// MaxToolArgumentBytes caps the size of a tool call's raw argument payload
// before it is parsed, so a runaway generation can't push a multi-hundred-KB
// blob through JSON unmarshaling and schema validation for no useful reason.
// This must stay above agent/jobs.go's maxPersistedStructuredResultJSONBytes
// (1MB) — that constant governs how large a communicate structured-result
// payload may legitimately be before it's gracefully dropped at persistence
// time, and a lower registry-level cap would hard-reject the tool call
// before it ever reached that graceful path. (Cross-package constant:
// agent/internal/tool cannot import agent to derive this by reference, so
// keep the two values in sync by comment.)
const MaxToolArgumentBytes = 2 * 1024 * 1024

// maxToolArgumentBytes remains the internal spelling used by breaker paths.
// Keep it identical to the exported raw-validation boundary.
const maxToolArgumentBytes = MaxToolArgumentBytes

// ValidateRawArguments rejects raw tool argument bytes that must never reach a
// lossy JSON decode or an allocation-heavy schema path. Callers return this
// bounded diagnostic directly to keep all prevalidation paths consistent.
func ValidateRawArguments(arguments []byte) error {
	if len(arguments) > MaxToolArgumentBytes {
		return fmt.Errorf("tool arguments too large: %d bytes exceeds the %d byte limit", len(arguments), MaxToolArgumentBytes)
	}
	if !utf8.Valid(arguments) {
		return errors.New("invalid tool arguments JSON: input is not valid UTF-8")
	}
	return nil
}

// ctxIntentKey is the context key carrying a tool call's `intent` argument
// past the registry's strip point (see ExecuteCall): handlers that want it —
// the shell tool, stamping it onto the job record — read it with IntentFromContext.
type ctxIntentKey struct{}

// IntentFromContext returns the tool call's `intent` argument threaded onto
// ctx by the registry, or "" when the call carried none. It exists so a
// handler can still see intent after the registry removed it from args.
func IntentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	intent, _ := ctx.Value(ctxIntentKey{}).(string)
	return intent
}

func WithIntentParameter(td llm.ToolDefinition) llm.ToolDefinition {
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
	if _, exists := props["intent"]; !exists {
		props["intent"] = map[string]any{
			"type":        "string",
			"description": toolIntentDescription,
		}
	}
	td.Parameters = params
	return td
}

func WithoutIntentParameter(td llm.ToolDefinition) llm.ToolDefinition {
	params := CloneSchemaMap(td.Parameters)
	props, _ := params["properties"].(map[string]any)
	if props != nil {
		delete(props, "intent")
	}
	switch required := params["required"].(type) {
	case []string:
		params["required"] = removeRequiredField(required, "intent")
	case []any:
		params["required"] = removeRequiredFieldAny(required, "intent")
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

// normalizeAskUserArgs normalizes ask_user arguments by wrapping the shorthand
// form (question + options) into the canonical batch form (questions array).
// When questions is absent but question + options are present (plus optional
// why, if_unanswered, multi_select, header), wraps them into a one-element
// questions array. Returns normalized args or an error if the shape is invalid.
func normalizeAskUserArgs(args map[string]any) (map[string]any, error) {
	_, hasQuestions := args["questions"]
	question, hasQuestion := args["question"]
	options, hasOptions := args["options"]

	// Case 1: questions present, question/options absent → use as-is (batch form)
	if hasQuestions && !hasQuestion && !hasOptions {
		return args, nil
	}

	// Case 2: question + options present, questions absent → wrap into batch form
	if !hasQuestions && hasQuestion && hasOptions {
		// Collect optional fields
		wrapped := map[string]any{
			"question": question,
			"options":  options,
		}
		if why, ok := args["why"].(string); ok && why != "" {
			wrapped["why"] = why
		}
		if ifUnanswered, ok := args["if_unanswered"].(string); ok && ifUnanswered != "" {
			wrapped["if_unanswered"] = ifUnanswered
		}
		if multiSelect, ok := args["multi_select"].(bool); ok && multiSelect {
			wrapped["multi_select"] = multiSelect
		}
		if header, ok := args["header"].(string); ok && header != "" {
			wrapped["header"] = header
		}

		// Create normalized args with wrapped questions
		out := make(map[string]any, 1)
		out["questions"] = []any{wrapped}
		return out, nil
	}

	// Case 3: distinguish sub-cases within invalid shapes
	minimalExample := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which option?",
				"options": []any{
					map[string]any{"label": "Option A", "detail": "First choice"},
					map[string]any{"label": "Option B", "detail": "Second choice"},
				},
			},
		},
	}
	exBytes, _ := json.MarshalIndent(minimalExample, "", "  ")

	// Sub-case 3a: both questions and question/options present
	if hasQuestions && hasQuestion {
		errorMsg := "ask_user: both 'questions' and 'question'/'options' given — supply exactly one form. Minimal example:\n" + string(exBytes)
		return nil, errors.New(errorMsg)
	}

	// Sub-case 3b: question present but options missing (shorthand attempted but incomplete)
	if hasQuestion && !hasOptions {
		errorMsg := "ask_user: 'options' is required when using the 'question' shorthand. Minimal example:\n" + string(exBytes)
		return nil, errors.New(errorMsg)
	}

	// Sub-case 3c: neither questions nor question+options present
	errorMsg := "ask_user: 'questions' is required (or use the 'question'+'options' shorthand for a single question). Minimal example:\n" + string(exBytes)
	return nil, errors.New(errorMsg)
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

	// RecoverableOutput is the exact model-facing text before the registry's
	// generic output limit is applied. Unlike FullOutput, it is not replaced by
	// a TextResult's event-facing FullOutput override.
	RecoverableOutput string

	// Truncated reports whether the registry's generic output limit changed the
	// model-facing text.
	Truncated bool

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
	ImageIntent    string // from the caller: what they hope to learn

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

	// BreakerExactSignature and BreakerSemanticSignature are bounded hashes for
	// failure-loop telemetry. They intentionally never contain raw arguments,
	// bodies, or secrets. The semantic signature is populated after registered
	// argument normalization and gains the stable error class on failures.
	BreakerExactSignature    string `json:"-"`
	BreakerSemanticSignature string `json:"-"`
	BreakerBypassed          bool   `json:"-"`
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
	Intent    string // what the caller hopes to learn from this image
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
func ParseDocumentResult(_ string, readFileOutput string) *ImageResult {
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
	return &ImageResult{
		Text:      text,
		Data:      data,
		MediaType: "application/pdf",
	}
}

// RegisteredTool is a registered tool: the embedded llm.Tool (definition and
// Execute), its compiled validation schema, output limit, and the agent-layer
// executor that receives the execenv.ExecutionEnvironment.
type RegisteredTool struct {
	llm.Tool   // embeds Definition + Execute
	Schema     *jsonschema.Schema
	Limit      schema.ToolOutputLimit
	OmitIntent bool
	// OmitDescriptionFromSemanticIdentity is set only for built-in registrations
	// whose top-level description is presentation metadata, never by name alone.
	OmitDescriptionFromSemanticIdentity bool
	// ApplyBuiltInSemanticDefaults is set only for core registrations whose
	// handlers own the documented shell/job_stop/job_list/ask_user neutral
	// defaults. read_transcript uses the same core-only policy through its
	// registered NormalizeArgs hook.
	ApplyBuiltInSemanticDefaults bool
	generation                   uint64
	// NormalizeArgs optionally canonicalizes arguments immediately before schema
	// validation. It must preserve all non-normalized caller values.
	NormalizeArgs func(map[string]any) (map[string]any, error)
	// Agent-layer executor with environment context.
	Exec func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error)
}

// PrevalidationSnapshot is an immutable registry observation captured before
// session-level repair. Its contents are intentionally opaque: callers may
// carry it to FinalizePrevalidationFailure, but cannot forge or alter the
// lifetime it represents.
type PrevalidationSnapshot struct {
	registry   *Registry
	name       string
	lifetime   uint64
	registered *RegisteredTool
	limit      schema.ToolOutputLimit
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
	// semanticBreaker retains failed normalized call signatures separately from
	// the exact raw-argument fast path above.
	semanticBreaker *semanticFailureLedger
	telemetryKey    [32]byte
	nextGeneration  uint64
	// lifetimes retains a name's current semantic lifetime even while absent.
	// Its entries are tombstones as well as registered generations: an old
	// unknown call must not match a name that was registered then removed.
	lifetimes map[string]uint64
}

// NewRegistry returns an empty Registry ready for tool registration.
func NewRegistry() *Registry {
	r := &Registry{tools: map[string]RegisteredTool{}, breaker: newFailureLedger(), semanticBreaker: newSemanticFailureLedger(), lifetimes: map[string]uint64{}}
	if _, err := rand.Read(r.telemetryKey[:]); err != nil {
		panic("tool telemetry key: " + err.Error())
	}
	return r
}

func (r *Registry) telemetryExactSignature(name string, args []byte) string {
	h := hmac.New(sha256.New, r.telemetryKey[:])
	_, _ = h.Write(args)
	sum := h.Sum(nil)
	return name + ":" + hex.EncodeToString(sum[:8])
}

func (r *Registry) semanticSignature(name string, args map[string]any) string {
	r.mu.RLock()
	registered, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return r.semanticSignatureFor(name, args, nil)
	}
	return r.semanticSignatureFor(name, args, &registered)
}

func (r *Registry) semanticSignatureFor(name string, args map[string]any, registered *RegisteredTool) string {
	omitDescription := registered != nil && registered.OmitDescriptionFromSemanticIdentity
	applyDefaults := registered != nil && registered.ApplyBuiltInSemanticDefaults
	encoded, err := semanticCanonicalBytes(name, args, omitDescription, applyDefaults)
	if err != nil {
		encoded = []byte("unencodable")
	}
	h := hmac.New(sha256.New, r.telemetryKey[:])
	_, _ = h.Write(encoded)
	return name + ":" + hex.EncodeToString(h.Sum(nil)[:8])
}

// MarkRegisteredToolsCoreSemanticMetadata records that the tools currently
// registered by core bootstrap use narrated descriptions and built-in runtime
// defaults. Later replacement registrations retain false-by-default policies.
func (r *Registry) MarkRegisteredToolsCoreSemanticMetadata() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, registered := range r.tools {
		// This changes the semantic identity policy. Treat it like a replacement so
		// failures classified under the old policy cannot affect the new one.
		registered.generation = r.advanceLifetimeLocked(name)
		registered.OmitDescriptionFromSemanticIdentity = true
		registered.ApplyBuiltInSemanticDefaults = true
		r.tools[name] = registered
		r.breaker.clearTool(name)
		r.semanticBreaker.clearTool(name)
	}
}

// telemetryComponent returns an opaque, session-scoped token for a semantic
// marker or error class. Components become part of externally observable
// breaker signatures, so even fixed sentinels must not be cross-session
// correlatable or permit offline verification of a guessed error class.
func (r *Registry) telemetryComponent(domain, value string) string {
	h := hmac.New(sha256.New, r.telemetryKey[:])
	_, _ = h.Write([]byte(domain))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func (r *Registry) semanticSignatureFromRawFor(name string, raw []byte, registered *RegisteredTool) string {
	if len(raw) > maxToolArgumentBytes {
		return name + ":" + r.telemetryComponent("semantic-marker", "oversize")
	}
	if !utf8.Valid(raw) {
		return name + ":" + r.telemetryComponent("semantic-marker", "invalid-utf8")
	}
	args := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &args) != nil {
		return name + ":" + r.telemetryComponent("semantic-marker", "invalid-json")
	}
	return r.semanticSignatureFor(name, args, registered)
}

func (r *Registry) semanticSignatureFromRaw(name string, raw []byte) string {
	return r.semanticSignatureFromRawFor(name, raw, nil)
}

func (r *Registry) semanticPark(name, callID, semanticSignature, exactSignature string, generation uint64, judged bool) (ExecResult, bool) {
	if !judged {
		return ExecResult{}, false
	}
	// Registry lifetime changes take r.mu before either ledger. Keep this
	// read lock through the ledger check so a replacement cannot install and
	// clear a new lifetime between the generation check and this judgement.
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.semanticParkLocked(name, callID, semanticSignature, exactSignature, generation, judged)
}

// semanticParkLocked requires r.mu to be read-locked. Keeping the lifetime
// check and semantic-ledger read under that lock prevents a transition between
// deciding to park and observing the failure history.
func (r *Registry) semanticParkLocked(name, callID, semanticSignature, exactSignature string, lifetime uint64, judged bool) (ExecResult, bool) {
	if !judged || !r.isCurrentOrAbsentLocked(name, lifetime) {
		return ExecResult{}, false
	}
	if count, boundary, fingerprint := r.semanticBreaker.check(semanticSignature); count >= breakerThreshold {
		// A parked result is breaker control information, not executor output.
		// Its stable prefix and remediation must remain model-visible even when
		// the registered executor uses a small tail-truncation limit.
		res := truncateResult(name, callID, semanticFailureParkText(name, fingerprint, boundary, count), true, defaultToolLimit(name))
		res.BreakerExactSignature = exactSignature
		res.BreakerSemanticSignature = fingerprint
		return res, true
	}
	return ExecResult{}, false
}

func (r *Registry) finalizeBreaker(res ExecResult, name string, rawArgs []byte, exactSignature, semanticSignature string, generation uint64, judged, humanBypassed bool, boundaryOverride string) ExecResult {
	// All ledger mutation is ordered under r.mu after the current-generation
	// validation. Register/Remove take the write lock and clear both ledgers, so
	// an in-flight result from an older registration cannot be recorded into its
	// successor's fresh lifetime.
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.finalizeBreakerLocked(res, name, rawArgs, exactSignature, semanticSignature, generation, judged, humanBypassed, boundaryOverride)
}

// finalizeBreakerLocked requires r.mu to be read-locked. Ledger operations
// therefore occur after lifetime validation while still excluding every
// registry write transition (registry lock, then ledger lock).
func (r *Registry) finalizeBreakerLocked(res ExecResult, name string, rawArgs []byte, exactSignature, semanticSignature string, generation uint64, judged, humanBypassed bool, boundaryOverride string) ExecResult {
	if !r.isCurrentOrAbsentLocked(name, generation) {
		return res
	}
	res.BreakerExactSignature = exactSignature
	res.BreakerSemanticSignature = semanticSignature
	res.BreakerBypassed = humanBypassed
	if humanBypassed {
		r.semanticBreaker.clear(semanticSignature)
		return res
	}
	if !judged {
		return res
	}
	body := res.FullOutput
	if body == "" {
		body = res.Output
	}
	semanticFailStreak := 0
	semanticFingerprint := ""
	semanticBoundary := ""
	if res.IsError {
		boundary := failureBoundary(body)
		class := semanticErrorClassFor(res.Err, body)
		if boundaryOverride != "" {
			boundary, class = boundaryOverride, boundaryOverride
		}
		if class != "" {
			semanticFingerprint, semanticFailStreak = r.semanticBreaker.record(semanticSignature, r.telemetryComponent("semantic-error-class", class), boundary)
			semanticBoundary = boundary
			res.BreakerSemanticSignature = semanticFingerprint
		}
	} else {
		r.semanticBreaker.clear(semanticSignature)
	}
	// The exact entry becomes visible to concurrent pre-dispatch checks only
	// after its semantic metadata is written under the exact ledger lock.
	failStreak, repeatStreak := r.breaker.recordWithSemantic(name, rawArgs, res.IsError, body, semanticFingerprint, semanticBoundary)
	switch {
	case failStreak >= breakerThreshold:
		appendIntervention(&res, failureNudgeText)
	case semanticFailStreak >= breakerThreshold:
		if boundaryOverride != "" {
			appendIntervention(&res, semanticFailureNudgeText(boundaryOverride))
		} else {
			appendIntervention(&res, semanticFailureNudgeText(failureBoundary(body)))
		}
	case repeatStreak >= breakerThreshold:
		appendIntervention(&res, repetitionNudgeText(repeatStreak))
	}
	return res
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
	out.nextGeneration = r.nextGeneration
	if len(r.lifetimes) > 0 {
		out.lifetimes = make(map[string]uint64, len(r.lifetimes))
		maps.Copy(out.lifetimes, r.lifetimes)
	}
	return out
}

// SnapshotPrevalidation captures the registered tool (if any) and the opaque
// semantic lifetime that repair observed. Session callers must pass the token
// back to FinalizePrevalidationFailure rather than re-resolving the tool after
// a hook or other concurrent registry transition.
func (r *Registry) SnapshotPrevalidation(name string) (*RegisteredTool, PrevalidationSnapshot) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := PrevalidationSnapshot{
		registry: r,
		name:     name,
		lifetime: r.lifetimeLocked(name),
		limit:    defaultToolLimit(name),
	}
	registered, found := r.tools[name]
	if !found {
		return nil, snapshot
	}
	snapshot.limit = registered.Limit
	snapshot.registered = &registered
	return &registered, snapshot
}

// Use appends a middleware to the tool execution pipeline.
// Middleware runs after argument validation but before tool execution.
func (r *Registry) Use(mw toolMiddleware) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, mw)
}

// Register validates and stores a tool in the registry. It rejects an invalid
// tool name or a missing executor, injects the intent parameter into work-tool
// definitions, applies a default output limit when none is set, compiles (or
// reuses) the argument schema, and bridges llm.Tool.Execute from Exec when unset.
func (r *Registry) Register(t RegisteredTool) error {
	if err := llm.ValidateToolName(t.Definition.Name); err != nil {
		return err
	}
	if t.OmitIntent {
		t.Definition = WithoutIntentParameter(t.Definition)
	} else {
		t.Definition = WithIntentParameter(t.Definition)
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
	t.generation = r.advanceLifetimeLocked(t.Definition.Name)
	r.tools[t.Definition.Name] = t
	r.breaker.clearTool(t.Definition.Name)
	r.semanticBreaker.clearTool(t.Definition.Name)
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
			r.advanceLifetimeLocked(name)
			r.breaker.clearTool(name)
			r.semanticBreaker.clearTool(name)
		}
	}
}

// Remove deletes a single tool from the registry.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, found := r.tools[name]; found {
		delete(r.tools, name)
		r.advanceLifetimeLocked(name)
	}
	r.breaker.clearTool(name)
	r.semanticBreaker.clearTool(name)
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

// RequiresOutputRecovery reports whether names includes a registered tool whose
// generic registry limit can omit model-facing text. Unknown names and the
// recovery reader itself do not create a recovery requirement.
func (r *Registry) RequiresOutputRecovery(names []string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range names {
		registered, ok := r.tools[name]
		if !ok || name == "read_transcript" {
			continue
		}
		lim := registered.Limit
		if lim.MaxChars > 0 || lim.MaxLines > 0 {
			return true
		}
	}
	return false
}

// Unregister deletes the named tool from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, found := r.tools[name]; found {
		delete(r.tools, name)
		r.advanceLifetimeLocked(name)
	}
	r.breaker.clearTool(name)
	r.semanticBreaker.clearTool(name)
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
	// model_list is an exact JSON continuation protocol. Repeating a page is a
	// valid retry, and appending breaker text would corrupt its bounded envelope.
	humanBypassed := breakerBypassed(ctx)
	protocolExempt := name == "model_list"
	judged := !humanBypassed && !protocolExempt
	exactSignature := r.telemetryExactSignature(name, call.Arguments)
	r.mu.RLock()
	t, ok := r.tools[name]
	currentGeneration := r.lifetimeLocked(name)
	if judged {
		if failStreak, _, snippets := r.breaker.check(name, call.Arguments); failStreak >= breakerThreshold {
			fingerprint, boundary := r.breaker.semanticMetadata(name, call.Arguments)
			message := failureParkText(name, snippets)
			if fingerprint != "" {
				message = failureParkWithSemanticText(name, snippets, fingerprint, boundary)
			}
			res := truncateResult(name, callID, message, true, defaultToolLimit(name))
			res.BreakerSemanticSignature = fingerprint
			res.BreakerExactSignature = exactSignature
			r.mu.RUnlock()
			return res
		}
	} else if humanBypassed {
		// A human authorized this dispatch, which retires the refusals that
		// led here as evidence. Clearing rather than merely skipping judgement
		// is what keeps a repeatedly-approved call approvable: the grant is
		// per-invocation, so the same call comes back and is denied again, and
		// a streak that only ever grows would park the next one before
		// dispatch — with no typed error left to raise another approval card.
		r.breaker.clearFailures(name, call.Arguments)
	}
	r.mu.RUnlock()
	var semanticRegistered *RegisteredTool
	if ok {
		semanticRegistered = &t
	}
	semanticSignature := r.semanticSignatureFromRawFor(name, call.Arguments, semanticRegistered)
	preNormalizationSignature := semanticSignature
	finish := func(res ExecResult) ExecResult {
		return r.finalizeBreaker(res, name, call.Arguments, exactSignature, semanticSignature, currentGeneration, judged, humanBypassed, "")
	}
	park := func() (ExecResult, bool) {
		return r.semanticPark(name, callID, semanticSignature, exactSignature, currentGeneration, judged)
	}
	if !ok {
		if res, blocked := park(); blocked {
			return res
		}
		msg := "unknown tool: " + name
		return finish(truncateResult(name, callID, msg, true, defaultToolLimit(name)))
	}

	if err := ValidateRawArguments(call.Arguments); err != nil {
		if res, blocked := park(); blocked {
			return res
		}
		return finish(truncateResult(name, callID, err.Error(), true, defaultToolLimit(name)))
	}

	var args map[string]any
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			if res, blocked := park(); blocked {
				return res
			}
			msg := fmt.Sprintf("invalid tool arguments JSON: %v", err)
			return finish(truncateResult(name, callID, msg, true, t.Limit))
		}
	}
	if args == nil {
		args = map[string]any{}
	}

	// Normalize ask_user shorthand form (question + options) into batch form (questions array)
	// before schema validation, so both forms are validated against the same schema.
	if name == "ask_user" {
		normalized, err := normalizeAskUserArgs(args)
		if err != nil {
			if res, blocked := park(); blocked {
				return res
			}
			return finish(truncateResult(name, callID, err.Error(), true, t.Limit))
		}
		args = normalized
		semanticSignature = r.semanticSignatureFor(name, args, &t)
	}
	if t.NormalizeArgs != nil {
		normalized, err := t.NormalizeArgs(args)
		if err != nil {
			if res, blocked := park(); blocked {
				return res
			}
			return finish(truncateResult(name, callID, err.Error(), true, t.Limit))
		}
		args = normalized
	}
	semanticSignature = r.semanticSignatureFor(name, args, &t)

	if err := t.Schema.Validate(args); err != nil {
		if res, blocked := park(); blocked {
			return res
		}
		msg := fmt.Sprintf("tool args schema validation failed: %v", err)
		return finish(truncateResult(name, callID, msg, true, t.Limit))
	}

	if res, blocked := park(); blocked {
		return res
	}
	r.mu.RLock()
	mws := r.middleware
	r.mu.RUnlock()
	for _, mw := range mws {
		if err := mw(ctx, name, args); err != nil {
			if res, blocked := park(); blocked {
				return res
			}
			return finish(truncateResult(name, callID, err.Error(), true, t.Limit))
		}
	}

	if name != "read_file" {
		intent, _ := args["intent"].(string)
		delete(args, "intent")
		// The handler can no longer see intent in args — but the shell tool
		// stamps it onto the job record (so job surfaces can show why the
		// model said it is running the command), so keep it reachable on ctx.
		// Set unconditionally: a nested call reusing a context that already
		// carries a stale intent would otherwise inherit it when this call
		// omits intent.
		ctx = context.WithValue(ctx, ctxIntentKey{}, strings.TrimSpace(intent))
	}
	v, err := t.Exec(ctx, env, args)
	res := dispatchedResult(name, callID, t.Limit, v, err)
	res = finish(res)
	if !res.IsError && semanticSignature != preNormalizationSignature {
		// A successful normalized execution retires prior repair/normalization
		// failures recorded against the raw pre-normalization identity too.
		r.clearSemanticIfCurrent(name, currentGeneration, preNormalizationSignature)
	}
	return res
}

func (r *Registry) isCurrentGenerationLocked(name string, generation uint64) bool {
	registered, ok := r.tools[name]
	return ok && registered.generation == generation
}

func (r *Registry) lifetimeLocked(name string) uint64 {
	return r.lifetimes[name]
}

// advanceLifetimeLocked rotates a name's semantic identity and leaves a
// tombstone behind when the tool becomes absent. It must be called with r.mu
// write-locked. nextGeneration is a registry-wide allocator so values are
// never reused, including after cloning a registry with live tombstones.
func (r *Registry) advanceLifetimeLocked(name string) uint64 {
	r.nextGeneration++
	if r.lifetimes == nil {
		r.lifetimes = make(map[string]uint64)
	}
	r.lifetimes[name] = r.nextGeneration
	return r.nextGeneration
}

// isCurrentOrAbsentLocked accepts a lifetime only when it is still the exact
// current identity for name. Absent names use their persistent tombstone epoch
// rather than the former reusable generation zero.
func (r *Registry) isCurrentOrAbsentLocked(name string, lifetime uint64) bool {
	if r.lifetimeLocked(name) != lifetime {
		return false
	}
	if registered, ok := r.tools[name]; ok {
		return registered.generation == lifetime
	}
	return true
}

func (r *Registry) clearSemanticIfCurrent(name string, generation uint64, signature string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.isCurrentGenerationLocked(name, generation) {
		r.semanticBreaker.clear(signature)
	}
}

// FinalizePrevalidationFailure records a session-level validation failure in
// the same breaker ledgers as ExecuteCall, without dispatching the tool. The
// immutable snapshot must have been captured before repair; a stale snapshot
// returns its original validation error but cannot alter successor telemetry,
// signatures, or breaker state.
func (r *Registry) FinalizePrevalidationFailure(ctx context.Context, snapshot PrevalidationSnapshot, call llm.ToolCallData, semanticArgs []byte, message, boundary string, err error) ExecResult {
	name := call.Name
	callID := call.ID
	if strings.TrimSpace(callID) == "" {
		callID = "call_" + shortHash(call.Arguments)
	}

	lim := snapshot.limit
	if lim.MaxChars == 0 {
		lim = defaultToolLimit(name)
	}
	res := truncateResult(name, callID, message, true, lim)
	res.PrevalOnly = true
	res.Err = err

	// Hold the registry read lock from lifetime validation through every ledger
	// read/write. Register, Remove, Restrict, and semantic-policy transitions
	// take its write lock before clearing ledgers, so this has no check-then-
	// finalize window and preserves registry -> ledger lock order.
	r.mu.RLock()
	defer r.mu.RUnlock()
	if snapshot.registry != r || snapshot.name != name || !r.isCurrentOrAbsentLocked(name, snapshot.lifetime) {
		return res
	}

	humanBypassed := breakerBypassed(ctx)
	judged := !humanBypassed && name != "model_list"
	exactSignature := r.telemetryExactSignature(name, call.Arguments)
	semanticRaw := call.Arguments
	if len(semanticArgs) > 0 {
		semanticRaw = semanticArgs
	}
	semanticSignature := r.semanticSignatureFromRawFor(name, semanticRaw, snapshot.registered)
	if boundary == "" {
		boundary = prevalidationBoundary(name, call.Arguments, snapshot.registered != nil)
	}
	if judged {
		if failStreak, _, snippets := r.breaker.check(name, call.Arguments); failStreak >= breakerThreshold {
			message := failureParkText(name, snippets)
			fingerprint, recordedBoundary := r.breaker.semanticMetadata(name, call.Arguments)
			if fingerprint != "" {
				message = failureParkWithSemanticText(name, snippets, fingerprint, recordedBoundary)
			}
			res := truncateResult(name, callID, message, true, defaultToolLimit(name))
			res.BreakerExactSignature = exactSignature
			res.BreakerSemanticSignature = fingerprint
			res.PrevalOnly = true
			return res
		}
	} else if humanBypassed {
		r.breaker.clearFailures(name, call.Arguments)
	}
	if judged {
		if res, blocked := r.semanticParkLocked(name, callID, semanticSignature, exactSignature, snapshot.lifetime, judged); blocked {
			res.PrevalOnly = true
			return res
		}
	}
	return r.finalizeBreakerLocked(res, name, call.Arguments, exactSignature, semanticSignature, snapshot.lifetime, judged, humanBypassed, boundary)
}

func prevalidationBoundary(name string, rawArgs []byte, registered bool) string {
	if !registered {
		return "unknown_tool"
	}
	if len(rawArgs) > maxToolArgumentBytes {
		return "arguments_too_large"
	}
	if len(rawArgs) > 0 {
		args := map[string]any{}
		if json.Unmarshal(rawArgs, &args) != nil {
			return "arguments_json"
		}
	}
	return "schema_validation"
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
		mediaType := strings.ToLower(strings.TrimSpace(img.MediaType))
		if mediaType != "application/pdf" {
			decodedMediaType, err := llm.RasterMediaType(img.Data)
			if err != nil {
				return truncateResult(name, callID, fmt.Sprintf("invalid image data: %v", err), true, lim)
			}
			img.MediaType = decodedMediaType
		}
		res := truncateResult(name, callID, img.Text, false, lim)
		res.ImageData = img.Data
		res.ImageMediaType = img.MediaType
		res.ImageIntent = img.Intent
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
	truncated := false
	if lim.Strategy == schema.TruncHeadCount {
		out, truncated = truncateHeadCountWithStatus(out, lim.MaxLines, lim.MaxChars)
	} else {
		var charTruncated bool
		out, charTruncated = truncateCharsWithStatus(out, lim.MaxChars, lim.Strategy)
		truncated = charTruncated
		if lim.MaxLines > 0 {
			var lineTruncated bool
			out, lineTruncated = truncateLinesWithStatus(out, lim.MaxLines)
			truncated = truncated || lineTruncated
		}
	}
	if truncated && out != full {
		out += "\n[Tool output was truncated.]"
	}
	return ExecResult{
		ToolName:          toolName,
		CallID:            callID,
		Output:            out,
		FullOutput:        full,
		RecoverableOutput: full,
		Truncated:         truncated,
		IsError:           isErr,
	}
}

func truncateChars(s string, limit int, strategy schema.TruncationStrategy) string {
	out, _ := truncateCharsWithStatus(s, limit, strategy)
	return out
}

func truncateCharsWithStatus(s string, limit int, strategy schema.TruncationStrategy) (string, bool) {
	runes := []rune(s)
	if limit <= 0 || len(runes) <= limit {
		return s, false
	}
	removed := len(runes) - limit
	switch strategy {
	case schema.TruncTail:
		// Spec: keep the last max_chars characters and prepend a warning.
		marker := fmt.Sprintf("[Output truncated: %d characters removed from the beginning.]\n\n", removed)
		return marker + string(runes[len(runes)-limit:]), true
	default:
		// Spec: head/tail split plus an explicit warning about omitted middle.
		headCount := limit / 2
		tailCount := limit - headCount
		marker := fmt.Sprintf("\n\n[Output truncated: %d characters removed from the middle.]\n\n", removed)
		return string(runes[:headCount]) + marker + string(runes[len(runes)-tailCount:]), true
	}
}

// truncateHeadCount bounds glob/grep-shaped output (one match per line, though
// grep's context_lines can add several output lines per match plus "--" group
// separators, so this counts LINES, not matches) by keeping the first
// maxEntries lines and appending a count-only structural summary. Unlike truncateChars'
// TruncTail case, this never drops the head of the result: an agent scanning
// an unscoped search always sees the earliest (and, per Glob/Grep's own
// ordering, most relevant) matches, with an explicit count of what was
// omitted rather than a silently smaller result.
//
// After the line-count bound is applied, maxChars (when positive) further
// bounds the result's character count by truncating from the TAIL and
// appending a warning — the line-count bound's head (and its summary line,
// reserved for by the budget) is always preserved, so a single enormous
// matched line still cannot balloon the result past both bounds.
func truncateHeadCount(s string, maxEntries, maxChars int) string {
	out, _ := truncateHeadCountWithStatus(s, maxEntries, maxChars)
	return out
}

func truncateHeadCountWithStatus(s string, maxEntries, maxChars int) (string, bool) {
	if maxEntries <= 0 || s == "" {
		return s, false
	}
	out := s
	truncated := false
	lines := strings.Split(s, "\n")
	total := len(lines)
	if total > maxEntries {
		shown := lines[:maxEntries]
		summary := fmt.Sprintf("\n[%d total lines; showing first %d; %d lines omitted]", total, maxEntries, total-maxEntries)
		out = strings.Join(shown, "\n") + summary
		truncated = true
	}
	if maxChars > 0 {
		var charTruncated bool
		out, charTruncated = truncateCharsFromTailWithStatus(out, maxChars)
		truncated = truncated || charTruncated
	}
	return out, truncated
}

// truncateCharsFromTailWithStatus bounds s to at most limit characters by dropping
// characters from the END and appending a warning marker, preserving
// whatever head content the caller already assembled (e.g. truncateHeadCount's
// earliest-first matches and their summary line), and reports whether it did so.
func truncateCharsFromTailWithStatus(s string, limit int) (string, bool) {
	runes := []rune(s)
	if limit <= 0 || len(runes) <= limit {
		return s, false
	}
	removed := len(runes) - limit
	for {
		marker := fmt.Sprintf("\n[Output truncated: %d characters removed from the end.]", removed)
		keep := max(limit-len([]rune(marker)), 0)
		actualRemoved := len(runes) - keep
		if actualRemoved == removed {
			return string(runes[:keep]) + marker, true
		}
		removed = actualRemoved
	}
}

func truncateLines(s string, limit int) string {
	out, _ := truncateLinesWithStatus(s, limit)
	return out
}

func truncateLinesWithStatus(s string, limit int) (string, bool) {
	if limit <= 0 {
		return s, false
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= limit {
		return s, false
	}
	headCount := limit / 2
	tailCount := limit - headCount
	omitted := len(lines) - headCount - tailCount
	marker := fmt.Sprintf("\n[... %d lines omitted ...]\n", omitted)
	head := strings.Join(lines[:headCount], "\n")
	tail := strings.Join(lines[len(lines)-tailCount:], "\n")
	return head + marker + tail, true
}

func defaultToolLimit(toolName string) schema.ToolOutputLimit {
	switch toolName {
	case "read_file":
		return schema.ToolOutputLimit{MaxChars: 50_000, Strategy: schema.TruncHeadTail}
	case "shell":
		return schema.ToolOutputLimit{MaxChars: 30_000, MaxLines: 512, Strategy: schema.TruncHeadTail}
	case "grep":
		return schema.ToolOutputLimit{MaxChars: 20_000, MaxLines: 200, Strategy: schema.TruncHeadCount}
	case "glob":
		return schema.ToolOutputLimit{MaxChars: 20_000, MaxLines: 500, Strategy: schema.TruncHeadCount}
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
	const schemaURI = "urn:evener:tool-schema"
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
