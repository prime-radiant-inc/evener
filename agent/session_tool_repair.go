package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

// prepareResult is the outcome of the pre-dispatch repair step. When PrevalErr
// is non-empty, execTool returns it as the tool's error result WITHOUT calling
// ExecuteCall — but still runs the full event/hook lifecycle.
type prepareResult struct {
	Call      llm.ToolCallData
	Changes   []repair.Change
	PrevalErr string
	Err       error
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
	if t == nil {
		res.PrevalErr = repair.UnknownToolMessage(requestedVisible, visibleNames)
		return res
	}

	args := map[string]any{}
	if len(res.Call.Arguments) > 0 { // raw len, mirroring ExecuteCall (no TrimSpace)
		if err := json.Unmarshal(res.Call.Arguments, &args); err != nil {
			// A length-stopped turn cut the argument stream mid-JSON. Never
			// repair that: closing the open string would execute a silently
			// truncated call (e.g. write half a file). Fail with the real
			// diagnosis instead.
			if finishReason == llm.FinishReasonLength {
				res.PrevalErr = repair.ExplainTruncatedCall(requestedVisible)
				return res
			}
			repaired, c := repair.RepairJSON(res.Call.Arguments)
			res.Changes = append(res.Changes, c...)
			args = map[string]any{}
			if err2 := json.Unmarshal(repaired, &args); err2 != nil {
				// Show the model's own bytes and the original parse error
				// (its offset refers to them, not the repaired form).
				res.PrevalErr = repair.ExplainJSONError(requestedVisible, t.Definition.Parameters, err, res.Call.Arguments)
				return res
			}
		}
	}
	if errText := unsupportedDelegateWaitOption(call.Name, args); errText != "" {
		res.PrevalErr = errText
		return res
	}
	if errText := retiredTaskListShapeError(call.Name, args); errText != "" {
		res.PrevalErr = errText
		return res
	}

	if err := rejectUnavailableDelegateSandboxControls(t.Definition.Name, t.Definition.Parameters, args); err != nil {
		res.PrevalErr = err.Error()
		res.Err = err
		return res
	}
	if t.Definition.Name == "ask_user" {
		normalized, err := normalizeAskArgs(args)
		if err != nil {
			res.PrevalErr = err.Error()
			return res
		}
		args = normalized
	}
	var retainedReadChanges []repair.Change
	if t.Definition.Name == "read_transcript" {
		args, retainedReadChanges = normalizeRetainedReadArgsForRepair(args)
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
	if envelope, ok := communicateEnvelopeFor(t, resultToolName); ok {
		filled := make(map[string]any, len(args))
		maps.Copy(filled, args)
		fillChanges = fillCommunicateEnvelope(envelope, filled)
		args = filled
	}

	if err := t.Schema.Validate(args); err != nil {
		// A length stop that cut the stream before any argument byte leaves
		// empty args; on a tool with required parameters that reads as a
		// "missing required field" schema error — the same misdiagnosis the
		// truncation message exists to prevent.
		if finishReason == llm.FinishReasonLength && len(res.Call.Arguments) == 0 {
			res.PrevalErr = repair.ExplainTruncatedCall(requestedVisible)
			return res
		}
		healed, c := repair.RepairArgs(t.Definition.Parameters, args)
		if err2 := t.Schema.Validate(healed); err2 != nil {
			res.PrevalErr = repair.ExplainSchemaError(requestedVisible, t.Definition.Parameters, healed, offendingField(err2), offendingKeyword(err2))
			return res
		}
		args = healed
		// The healed form carries the fill (it was validated above), so the
		// fill's changes belong in the record alongside the healing changes.
		res.Changes = append(res.Changes, fillChanges...)
		res.Changes = append(res.Changes, c...)
	} else if len(fillChanges) > 0 {
		res.Changes = append(res.Changes, fillChanges...)
	}
	res.Changes = append(res.Changes, retainedReadChanges...)

	if len(res.Changes) > 0 {
		if b, err := json.Marshal(args); err == nil {
			res.Call.Arguments = b
		}
	}
	return res
}

// normalizeRetainedReadArgsForRepair removes only the semantically empty
// retained-output options that providers commonly materialize. It runs before
// the registry schema gate, so a nullable default reaches the handler as an
// omission and the applied removal is visible in repair telemetry. Artifact
// format is deliberately never removed: every explicit artifact format must
// remain available for the handler to reject.
func normalizeRetainedReadArgsForRepair(args map[string]any) (map[string]any, []repair.Change) {
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
	if value, present := normalized["output_match"]; present && value == "" {
		remove("output_match")
	}
	if value, present := normalized["context_lines"]; present && isNeutralRetainedInteger(value) && stringArg(normalized, "output_match") == "" {
		remove("context_lines")
	}
	if jobRef {
		if value, present := normalized["format"]; present && isNeutralJobFormat(value) {
			remove("format")
		}
	}
	return normalized, changes
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
