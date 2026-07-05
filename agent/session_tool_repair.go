package agent

import (
	"encoding/json"
	"errors"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/internal/tool/repair"
	"primeradiant.com/serf/llm"
)

// prepareResult is the outcome of the pre-dispatch repair step. When PrevalErr
// is non-empty, execTool returns it as the tool's error result WITHOUT calling
// ExecuteCall — but still runs the full event/hook lifecycle.
type prepareResult struct {
	Call      llm.ToolCallData
	Changes   []repair.Change
	PrevalErr string
}

// prepareToolCall heals a tool call before dispatch. t is the resolved tool
// (nil if the name is unknown). visibleNames and requestedVisible are already
// provider-visible names (the caller snapshots the name-map outside s.mu).
func prepareToolCall(call llm.ToolCallData, t *tool.RegisteredTool, visibleNames []string, requestedVisible string) prepareResult {
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
			repaired, c := repair.RepairJSON(res.Call.Arguments)
			res.Changes = append(res.Changes, c...)
			args = map[string]any{}
			if err2 := json.Unmarshal(repaired, &args); err2 != nil {
				res.PrevalErr = repair.ExplainJSONError(requestedVisible, t.Definition.Parameters, err2.Error())
				return res
			}
		}
	}

	if err := t.Schema.Validate(args); err != nil {
		healed, c := repair.RepairArgs(t.Definition.Parameters, args)
		if err2 := t.Schema.Validate(healed); err2 != nil {
			res.PrevalErr = repair.ExplainSchemaError(requestedVisible, t.Definition.Parameters, healed, offendingField(err2))
			return res
		}
		args = healed
		res.Changes = append(res.Changes, c...)
	}

	if len(res.Changes) > 0 {
		if b, err := json.Marshal(args); err == nil {
			res.Call.Arguments = b
		}
	}
	return res
}

// providerNameFromMap resolves a single canonical name to its provider-visible
// form using a pre-snapshotted nameMap, passing through unmapped names as-is.
// Pure over the snapshot so callers never need to lock (unlike providerToolName,
// which some callers invoke while already holding s.mu).
func providerNameFromMap(name string, nameMap map[string]string) string {
	if v, ok := nameMap[name]; ok {
		return v
	}
	return name
}

// providerVisibleFromMap maps each name to its provider-visible form via a
// pre-snapshotted nameMap, dropping empties and duplicates while preserving
// first-seen order.
func providerVisibleFromMap(names []string, nameMap map[string]string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		v := providerNameFromMap(n, nameMap)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// changeStrings encodes changes as "kind:field:detail" for the telemetry event.
func changeStrings(changes []repair.Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, string(c.Kind)+":"+c.Field+":"+c.Detail)
	}
	return out
}

// offendingField extracts the single offending property from a jsonschema
// validation error, or "" when it cannot be pinpointed (e.g. missing-required,
// where the instance location is the parent object). ExplainSchemaError falls
// back to listing all required args in that case.
func offendingField(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	loc := strings.Trim(ve.InstanceLocation, "/")
	if loc == "" {
		return ""
	}
	parts := strings.Split(loc, "/")
	return parts[len(parts)-1]
}
