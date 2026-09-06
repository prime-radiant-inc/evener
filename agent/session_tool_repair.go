package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

// maxCommunicateOutputJSONDepth bounds the nested JSON value accepted when a
// provider double-encodes the default communicate output object. The raw
// argument cap bounds bytes; this separate structural bound keeps a compact
// deeply nested value from consuming disproportionate parser resources.
const maxCommunicateOutputJSONDepth = 64

// prepareResult is the outcome of the pre-dispatch repair step. When PrevalErr
// is non-empty, execTool returns it as the tool's error result WITHOUT calling
// ExecuteCall — but still runs the full event/hook lifecycle.
type prepareResult struct {
	Call              llm.ToolCallData
	Changes           []repair.Change
	Lifetime          tool.PrevalidationSnapshot
	PreparedArguments json.RawMessage
	SemanticArguments json.RawMessage
	PrevalErr         string
	Boundary          string
	Err               error
	RegisteredHookErr bool
	// RawArgumentsRejected keeps unvalidated bytes out of hook input and
	// prevents a hook from replacing them. It is separate from PrevalErr so an
	// unknown tool can retain its established unknown-tool diagnostic.
	RawArgumentsRejected bool
}

// prepareToolCall heals a tool call before dispatch. t is the resolved tool
// (nil if the name is unknown). visibleNames and requestedVisible are already
// provider-visible names (the caller snapshots the name-map outside s.mu).
// resultToolName is the session's result tool (communicate) — the only tool
// whose default output envelope is eligible for the documented-defaults fill.
// finishReason is the round's stop reason ("" when no model response is in
// play); llm.FinishReasonLength disables JSON repair, since closing a
// truncated string would execute a silently truncated call.
func prepareToolCall(call llm.ToolCallData, t *tool.RegisteredTool, visibleNames []string, requestedVisible, resultToolName, finishReason string) prepareResult {
	res := prepareResult{Call: call}
	if strings.TrimSpace(res.Call.ID) == "" {
		res.Call.ID = "call_" + shortHash(res.Call.Arguments)
	}
	if err := tool.ValidateRawArguments(res.Call.Arguments); err != nil {
		res.RawArgumentsRejected = true
		if t != nil {
			res.PrevalErr = err.Error()
			return res
		}
	}
	if t == nil {
		res.PrevalErr = repair.UnknownToolMessage(requestedVisible, visibleNames)
		res.Boundary = "unknown_tool"
		return res
	}

	args := map[string]any{}
	var pendingJSONChanges []repair.Change
	if len(res.Call.Arguments) > 0 { // raw len, mirroring ExecuteCall (no TrimSpace)
		if err := json.Unmarshal(res.Call.Arguments, &args); err != nil {
			// A length-stopped turn cut the argument stream mid-JSON. Never
			// repair that: closing the open string would execute a silently
			// truncated call (e.g. write half a file). Fail with the real
			// diagnosis instead.
			if finishReason == llm.FinishReasonLength {
				res.PrevalErr = repair.ExplainTruncatedCall(requestedVisible)
				res.Boundary = "truncated_call"
				return res
			}
			repaired, c := repair.RepairJSON(res.Call.Arguments)
			args = map[string]any{}
			if err2 := json.Unmarshal(repaired, &args); err2 != nil {
				// Show the model's own bytes and the original parse error
				// (its offset refers to them, not the repaired form).
				res.PrevalErr = repair.ExplainJSONError(requestedVisible, t.Definition.Parameters, err, res.Call.Arguments)
				res.Boundary = "arguments_json"
				return res
			}
			// Keep repair changes pending until their repaired arguments are
			// committed below. Every early return through preparation otherwise
			// retains the model's raw bytes and must not claim a repair event.
			pendingJSONChanges = c
		}
	}
	if errText := unsupportedDelegateWaitOption(call.Name, args); errText != "" {
		res.PrevalErr = errText
		res.Boundary = "delegate_wait_option"
		return res
	}
	if errText := retiredTaskListShapeError(call.Name, args); errText != "" {
		res.PrevalErr = errText
		res.Boundary = "retired_task_shape"
		return res
	}

	if err := rejectUnavailableDelegateSandboxControls(t.Definition.Name, t.Definition.Parameters, args); err != nil {
		res.PrevalErr = err.Error()
		res.Boundary = "delegate_sandbox_control"
		res.Err = err
		return res
	}
	if t.Definition.Name == "ask_user" {
		normalized, err := normalizeAskArgs(args)
		if err != nil {
			res.PrevalErr = err.Error()
			res.Boundary = "ask_user_normalization"
			return res
		}
		args = normalized
	}
	var retainedReadChanges []repair.Change
	if t.Definition.Name == "read_transcript" {
		args, retainedReadChanges = normalizeRetainedReadArgs(args)
	}

	// The default communicate envelope documents message/data/artifacts as
	// always-present with empty defaults (issue #627), but the schema demands
	// them as required — reconcile by filling the documented defaults before
	// validation. communicateEnvelopeFor owns both gates (identity: the
	// session's result tool only; exact shape: precisely the default
	// envelope), and returns the envelope it validated so the fill cannot
	// diverge from what was checked. The fill runs on a working copy and is
	// committed (args + recorded changes) only if validation passes, so a
	// call that still fails never emits a ToolCallRepaired event whose
	// Arguments bytes were never applied.
	var fillChanges []repair.Change
	promotedOutputString := false
	if envelope, ok := communicateEnvelopeFor(t, resultToolName); ok {
		filled := make(map[string]any, len(args))
		maps.Copy(filled, args)
		var err error
		fillChanges, promotedOutputString, err = repairDefaultCommunicateEnvelope(envelope, filled)
		if err != nil {
			res.PrevalErr = communicateOutputStringObjectError(err.Error())
			return res
		}
		args = filled
	}
	runRegisteredHooks := func() bool {
		if t.NormalizeArgs != nil {
			normalized, err := t.NormalizeArgs(args)
			if err != nil {
				res.PrevalErr = err.Error()
				res.Err = err
				res.RegisteredHookErr = true
				return false
			}
			args = normalized
			res.SemanticArguments, _ = json.Marshal(args)
		}
		if t.PreValidate != nil {
			if err := t.PreValidate(args); err != nil {
				res.PrevalErr = err.Error()
				res.Err = err
				res.RegisteredHookErr = true
				return false
			}
		}
		return true
	}
	if !t.NormalizeAfterRepair && !runRegisteredHooks() {
		return res
	}

	if err := t.Schema.Validate(args); err != nil {
		// A length stop that cut the stream before any argument byte leaves
		// empty args; on a tool with required parameters that reads as a
		// "missing required field" schema error — the same misdiagnosis the
		// truncation message exists to prevent.
		if finishReason == llm.FinishReasonLength && len(res.Call.Arguments) == 0 {
			res.PrevalErr = repair.ExplainTruncatedCall(requestedVisible)
			res.Boundary = "truncated_call"
			return res
		}
		healed, c := repair.RepairArgs(t.Definition.Parameters, args)
		finalErrorArgs := args
		// Scalar repair can turn provider-materialized strings such as
		// expand_turn="0" into their neutral numeric forms. Normalize those
		// newly typed defaults before the final schema gate and dispatch.
		if t.Definition.Name == "read_transcript" {
			var normalizedChanges []repair.Change
			healed, normalizedChanges = normalizeRetainedReadArgs(healed)
			retainedReadChanges = append(retainedReadChanges, normalizedChanges...)
			if len(normalizedChanges) > 0 {
				// finalErrorArgs starts from the first-pass normalized form, so
				// only carry over fields deleted by second-pass retained-read
				// normalization. Generic repair coercions stay unapplied.
				finalErrorArgs = make(map[string]any, len(args))
				maps.Copy(finalErrorArgs, args)
				for _, change := range normalizedChanges {
					delete(finalErrorArgs, change.Field)
				}
			}
		}
		if err2 := t.Schema.Validate(healed); err2 != nil {
			// Retained-read normalization was already applied to args. Preserve
			// that real, applied change in telemetry even when another field
			// remains invalid; failed generic repairs and envelope fills remain
			// deliberately unrecorded.
			if len(retainedReadChanges) > 0 {
				// finalErrorArgs is committed for retained-read normalization even
				// though another field failed. It includes any successful outer
				// JSON repair, so record both applied repair sets together.
				committedChanges := append([]repair.Change(nil), pendingJSONChanges...)
				committedChanges = append(committedChanges, retainedReadChanges...)
				if err := commitPreparedRepairs(&res, finalErrorArgs, committedChanges); err != nil {
					res.PrevalErr = repairCommitError()
					return res
				}
			}
			offendingPath := offendingField(err2)
			res.PrevalErr = repair.ExplainSchemaError(requestedVisible, t.Definition.Parameters, healed, offendingPath, offendingKeyword(err2))
			if promotedOutputString && isCommunicateOutputSchemaPath(offendingPath) {
				res.PrevalErr += "\n" + communicateOutputStringObjectError("the decoded object did not satisfy the communicate output schema")
			}
			res.Boundary = "schema_validation"
			if t.Definition.Name == "ask_user" || len(pendingJSONChanges) > 0 || t.NormalizeArgs != nil {
				res.SemanticArguments, _ = json.Marshal(finalErrorArgs)
			}
			return res
		}
		args = healed
		committedChanges := append([]repair.Change(nil), pendingJSONChanges...)
		// The healed form carries the fill (it was validated above), so the
		// fill's changes belong in the record alongside the healing changes.
		committedChanges = append(committedChanges, fillChanges...)
		committedChanges = append(committedChanges, c...)
		committedChanges = append(committedChanges, retainedReadChanges...)
		if err := commitPreparedRepairs(&res, args, committedChanges); err != nil {
			res.PrevalErr = repairCommitError()
			return res
		}
	} else {
		committedChanges := append([]repair.Change(nil), pendingJSONChanges...)
		committedChanges = append(committedChanges, fillChanges...)
		committedChanges = append(committedChanges, retainedReadChanges...)
		if err := commitPreparedRepairs(&res, args, committedChanges); err != nil {
			res.PrevalErr = repairCommitError()
			return res
		}
	}
	if t.NormalizeAfterRepair && !runRegisteredHooks() {
		res.Call.Arguments = call.Arguments
		res.Changes = nil
		return res
	}
	if t.NormalizeAfterRepair {
		if err := t.Schema.Validate(args); err != nil {
			res.PrevalErr = repair.ExplainSchemaError(requestedVisible, t.Definition.Parameters, args, offendingField(err), offendingKeyword(err))
			res.Boundary = "schema_validation"
			res.SemanticArguments, _ = json.Marshal(args)
			res.Call.Arguments = call.Arguments
			res.Changes = nil
			return res
		}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		res.PrevalErr = repairCommitError()
		res.Call.Arguments = call.Arguments
		res.Changes = nil
		return res
	}
	res.PreparedArguments = encoded
	return res
}

// commitPreparedRepairs is the only repair commit point: changes and their
// encoded arguments become observable together, or neither does.
func commitPreparedRepairs(res *prepareResult, args map[string]any, changes []repair.Change) error {
	if len(changes) == 0 {
		return nil
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	res.Call.Arguments = encoded
	res.Changes = append([]repair.Change(nil), changes...)
	return nil
}

func repairCommitError() string {
	return "invalid_request: repaired tool arguments could not be encoded as JSON; the call was not applied."
}

// repairDefaultCommunicateEnvelope applies the documented defaults for the one
// canonical communicate output contract. The caller obtains envelope only from
// communicateEnvelopeFor, but this function repeats the equality guard so no
// future caller can accidentally apply a default-contract repair to a custom
// result schema.
func repairDefaultCommunicateEnvelope(envelope, args map[string]any) ([]repair.Change, bool, error) {
	if !isCanonicalDefaultCommunicateOutputEnvelope(envelope) {
		return nil, false, nil
	}

	var changes []repair.Change
	if raw, present := args["output"]; !present || raw == nil {
		args["output"] = map[string]any{}
		changes = append(changes, repair.Change{Kind: repair.ChangeSynthesize, Field: "output", Detail: "synthesized default envelope"})
	} else if encoded, ok := raw.(string); ok {
		decoded, err := decodeDefaultCommunicateOutputString(encoded)
		if err != nil {
			return nil, false, err
		}
		args["output"] = decoded
		changes = append(changes, repair.Change{Kind: repair.ChangePromoteJSONObject, Field: "output", Detail: "promoted JSON object string"})
	}

	changes = append(changes, fillCommunicateEnvelope(envelope, args)...)
	if _, present := args["message"]; !present {
		if output, ok := args["output"].(map[string]any); ok {
			if message, ok := output["message"].(string); ok && strings.TrimSpace(message) != "" {
				args["message"] = message
				changes = append(changes, repair.Change{Kind: repair.ChangeCopy, Field: "message", Detail: "copied output.message"})
			}
		}
	}
	for _, change := range changes {
		if change.Kind == repair.ChangePromoteJSONObject {
			return changes, true, nil
		}
	}
	return changes, false, nil
}

func decodeDefaultCommunicateOutputString(encoded string) (map[string]any, error) {
	if len(encoded) > tool.MaxToolArgumentBytes {
		return nil, fmt.Errorf("exceeds the %d byte argument limit", tool.MaxToolArgumentBytes)
	}
	if !utf8.ValidString(encoded) {
		return nil, errors.New("is not valid UTF-8")
	}
	if err := withinJSONDepth(encoded, maxCommunicateOutputJSONDepth); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("is not valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("contains trailing content")
		}
		return nil, errors.New("contains trailing invalid content")
	}
	output, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("does not decode to an object")
	}
	return output, nil
}

// withinJSONDepth counts object and array nesting without interpreting values.
// json.Decoder remains the authority on JSON syntax; this pass only rejects an
// otherwise bounded input that exceeds the named structural limit.
func withinJSONDepth(s string, maxDepth int) error {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inString {
				escaped = !escaped
			}
			continue
		case '"':
			if !escaped {
				inString = !inString
			}
			escaped = false
			continue
		default:
			escaped = false
		}
		if inString {
			continue
		}
		switch s[i] {
		case '{', '[':
			depth++
			if depth > maxDepth {
				return fmt.Errorf("exceeds the maximum JSON depth of %d", maxDepth)
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

func communicateOutputStringObjectError(reason string) string {
	return "communicate: output is a JSON-looking string and must be passed as an object, not a quoted string; " + reason
}

func isCommunicateOutputSchemaPath(path string) bool {
	return path == "output" || strings.HasPrefix(path, "output/")
}

// normalizeRetainedReadArgs removes only the semantically empty retained-output
// options that providers commonly materialize. It runs before the registry
// schema gate for repair telemetry and again at the execution boundary so
// direct registry calls and post-hook updatedInput use the default view too.
// Artifact format is deliberately never removed: every explicit artifact
// format must remain available for the handler to reject.
func normalizeRetainedReadArgs(args map[string]any) (map[string]any, []repair.Change) {
	ref := strings.TrimSpace(stringArg(args, "transcript_ref"))
	jobRef := strings.HasPrefix(ref, "job:")
	artifactRef := strings.HasPrefix(ref, "artifact:")
	if !jobRef && !artifactRef {
		return args, nil
	}
	normalized := make(map[string]any, len(args))
	maps.Copy(normalized, args)
	changes := make([]repair.Change, 0, 5)
	remove := func(field string) {
		delete(normalized, field)
		changes = append(changes, repair.Change{Kind: repair.ChangeNormalizeDefault, Field: field, Detail: "removed neutral default"})
	}
	if value, present := normalized["range"]; present && (value == nil || value == "") {
		remove("range")
	}
	if value, present := normalized["expand_turn"]; present && isNeutralRetainedInteger(value) {
		remove("expand_turn")
	}
	if value, present := normalized["output_match"]; present && (value == nil || value == "") {
		remove("output_match")
	}
	if value, present := normalized["context_lines"]; present && (isNeutralRetainedInteger(value) || isNeutralRetainedStringInteger(value)) {
		remove("context_lines")
	}
	if jobRef {
		if value, present := normalized["format"]; present && isNeutralJobFormat(value) {
			remove("format")
		}
	}
	return normalized, changes
}

// normalizeRetainedReadArgsForValidation extends the typed retained-default
// normalization just enough for provider stringified zero defaults to pass the
// registry schema gate. It is used only by read_transcript's registered-tool
// pre-validation hook; preparation retains scalar coercion and its telemetry.
func normalizeRetainedReadArgsForValidation(args map[string]any) (map[string]any, error) {
	normalized, _ := normalizeRetainedReadArgs(args)
	ref := strings.TrimSpace(stringArg(normalized, "transcript_ref"))
	if !strings.HasPrefix(ref, "job:") && !strings.HasPrefix(ref, "artifact:") {
		for _, field := range []string{"format", "range", "expand_turn", "output_match", "context_lines"} {
			if value, present := normalized[field]; present && value == nil {
				return nil, fmt.Errorf("invalid_request: %s cannot be null for session transcript refs", field)
			}
		}
		return normalized, nil
	}
	copyNeeded := true
	copyForWrite := func() {
		if !copyNeeded {
			return
		}
		copyNeeded = false
		copyArgs := make(map[string]any, len(normalized))
		maps.Copy(copyArgs, normalized)
		normalized = copyArgs
	}
	remove := func(field string) {
		copyForWrite()
		delete(normalized, field)
	}
	if value, present := normalized["expand_turn"]; present && isNeutralRetainedStringInteger(value) {
		remove("expand_turn")
	}
	if value, present := normalized["context_lines"]; present && isNeutralRetainedStringInteger(value) {
		remove("context_lines")
	}
	return normalized, nil
}

func isNeutralRetainedStringInteger(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return err == nil && n == 0
}

func isNeutralRetainedInteger(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case float64:
		return value == 0
	case float32:
		return value == 0
	case int:
		return value == 0
	case int8:
		return value == 0
	case int16:
		return value == 0
	case int32:
		return value == 0
	case int64:
		return value == 0
	case uint:
		return value == 0
	case uint8:
		return value == 0
	case uint16:
		return value == 0
	case uint32:
		return value == 0
	case uint64:
		return value == 0
	default:
		return false
	}
}

func isNeutralJobFormat(value any) bool {
	if value == nil {
		return true
	}
	format, ok := value.(string)
	return ok && (format == "" || strings.TrimSpace(format) == formatMarkdown)
}

// unsupportedDelegateWaitOption prevents argument repair from turning an
// explicitly supplied, unsupported wait knob into an omitted field. That
// would otherwise let the delegate handler start work while the caller still
// believes it requested a wait.
func unsupportedDelegateWaitOption(toolName string, args map[string]any) string {
	if toolName != "delegate" {
		return ""
	}
	for _, field := range []string{"max_wait_ms", "block", "block_timeout_ms", "background"} {
		if _, supplied := args[field]; supplied {
			return fmt.Sprintf("invalid_request: delegate does not support %s; the option was not applied and no delegate was started", field)
		}
	}
	return ""
}

// retiredTaskListShapeError prevents argument repair from silently turning
// an old action-shaped task_list call into a bare view. Repair would
// otherwise drop the retired action/tasks/updates keys, validate the empty
// remainder, and return the list — while the model believes its mutations
// were applied. Naming the retired shape gets the model to the new contract
// in one retry instead.
func retiredTaskListShapeError(toolName string, args map[string]any) string {
	if toolName != "task_list" {
		return ""
	}
	var retired []string
	for _, key := range []string{"action", "tasks", "updates"} {
		if _, supplied := args[key]; supplied {
			retired = append(retired, key)
		}
	}
	if len(retired) == 0 {
		return ""
	}
	return fmt.Sprintf("invalid_request: task_list no longer takes %s; the call was not applied. Use {\"add\": [...]} to add tasks, {\"update\": [{\"id\": N, ...}]} to change them (omit status to leave it unchanged), or {} to view the list — all in one call.",
		strings.Join(retired, ", "))
}

// changeStrings encodes changes as "kind:field:detail" for the telemetry event.
func changeStrings(changes []repair.Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, string(c.Kind)+":"+c.Field+":"+c.Detail)
	}
	return out
}

// offendingField extracts the deepest cause's instance location from a
// jsonschema validation error, as a JSON-Pointer-style path (e.g.
// "updates/0", "questions/0/header"), or "" when it cannot be pinpointed
// (e.g. missing-required at the root, where the instance location is the
// root object itself). ExplainSchemaError walks this path against the schema
// and args to find the real container; it falls back to listing all required
// args when the path is empty.
func offendingField(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	return strings.Trim(ve.InstanceLocation, "/")
}

// offendingKeyword extracts the failing JSON-Schema keyword's name from a
// jsonschema validation error — the last segment of the deepest cause's
// KeywordLocation (e.g. "maxLength" for a string that exceeded its limit, or
// "required" for a missing required property). ExplainSchemaError uses it to
// surface the actual constraint that rejected a present field instead of the
// generic "wrong type or value" message. Returns "" when no keyword can be
// pinpointed.
func offendingKeyword(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	kw := strings.Trim(ve.KeywordLocation, "/")
	if i := strings.LastIndex(kw, "/"); i >= 0 {
		return kw[i+1:]
	}
	return kw
}
