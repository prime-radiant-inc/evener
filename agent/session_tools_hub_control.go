package agent

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"primeradiant.com/evener/appwire"
)

type managerActionSchema struct {
	name   string
	params any
}

var managerControlActions = []managerActionSchema{
	{"approval", appwire.SandboxEscalationResolveParams{}},
	{"resume", appwire.ThreadResumeParams{}},
	{"fork", appwire.ThreadForkParams{}},
	{"clear", appwire.ThreadClearParams{}},
	{"model", appwire.ThreadModelSetParams{}},
	{"vision_model", appwire.ThreadVisionModelSetParams{}},
	{"reasoning_effort", appwire.ThreadReasoningEffortSetParams{}},
	{"compact", appwire.ThreadCompactStartParams{}},
	{"drain_queue", appwire.TurnDrainAsSteerParams{}},
	{"promote_queued", appwire.TurnPromoteQueuedAsSteerParams{}},
	{"cancel_queued", appwire.TurnCancelQueuedParams{}},
	{"rename", appwire.ThreadNameSetParams{}},
	{"goal", appwire.GoalSetParams{}},
	{"human_notes", appwire.NotesHumanSetParams{}},
	{"remove_url", appwire.UrlsRemoveParams{}},
	{"archive", appwire.ArchiveParams{}},
	{"delete", appwire.SessionDeleteParams{}},
	{"pin", appwire.SessionPinAssignParams{}},
	{"unpin", appwire.SessionPinUnpinParams{}},
	{"seen", appwire.SessionSeenSetParams{}},
}

// managerParametersSchema derives only argument shape from existing typed wire
// parameters. Dispatch below still uses explicit concrete types, never reflection
// or a method name from a tool argument.
func managerParametersSchema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": managerParametersSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": managerParametersSchema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for f := range t.Fields() {
			tag := strings.Split(f.Tag.Get("json"), ",")
			key := tag[0]
			if !f.IsExported() || key == "" || key == "-" || key == "managerProof" || key == "threadId" {
				continue
			}
			property := managerParametersSchema(f.Type)
			if key == "kind" && t == reflect.TypeFor[appwire.ArchiveParams]() {
				property["const"] = "session"
			}
			props[key] = property
			if !strings.Contains(f.Tag.Get("json"), "omitempty") || key == "ref" && t == reflect.TypeFor[appwire.ThreadResumeParams]() {
				required = append(required, key)
			}
		}
		result := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
		if len(required) > 0 {
			result["required"] = required
		}
		return result
	}
	return map[string]any{"type": "object"}
}

func managerControlSchema() map[string]any {
	actions := make([]string, 0, len(managerControlActions))
	branches := make([]any, 0, len(managerControlActions))
	for _, a := range managerControlActions {
		actions = append(actions, a.name)
		branches = append(branches, map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "const": a.name}, "arguments": managerParametersSchema(reflect.TypeOf(a.params))}, "required": []string{"action", "arguments"}})
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"action":    map[string]any{"type": "string", "enum": actions},
		"arguments": map[string]any{"type": "object"},
	}, "required": []string{"action", "arguments"}, "oneOf": branches}
}

func decodeManagerArguments[T any](raw []byte) (*T, error) {
	var p T
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	return &p, nil
}

func decodeManagerControl(action any, raw []byte) (appwire.SessionControlRequest, error) {
	var r appwire.SessionControlRequest
	name, ok := action.(string)
	if !ok {
		return r, appwire.InvalidParams("session control action required")
	}
	r.Action = name
	// Internal actor proof and thread-id aliases are not tool arguments.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return r, appwire.InvalidParams(err.Error())
	}
	if fields == nil {
		return r, appwire.InvalidParams("typed session control arguments required")
	}
	if _, present := fields["managerProof"]; present {
		return r, appwire.InvalidParams("actor metadata is not a tool argument")
	}
	if _, present := fields["threadId"]; present {
		return r, appwire.InvalidParams("a canonical ref is required")
	}
	if value, present := fields["ref"]; present {
		var ref string
		if err := json.Unmarshal(value, &ref); err != nil {
			return r, appwire.InvalidParams(err.Error())
		}
		if _, err := appwire.ParseRef(ref); err != nil {
			return r, appwire.InvalidParams(err.Error())
		}
	}
	var err error
	switch name {
	case "approval":
		r.Approval, err = decodeManagerArguments[appwire.SandboxEscalationResolveParams](raw)
	case "resume":
		r.Resume, err = decodeManagerArguments[appwire.ThreadResumeParams](raw)
	case "fork":
		r.Fork, err = decodeManagerArguments[appwire.ThreadForkParams](raw)
	case "clear":
		r.Clear, err = decodeManagerArguments[appwire.ThreadClearParams](raw)
	case "model":
		r.Model, err = decodeManagerArguments[appwire.ThreadModelSetParams](raw)
	case "vision_model":
		r.VisionModel, err = decodeManagerArguments[appwire.ThreadVisionModelSetParams](raw)
	case "reasoning_effort":
		r.ReasoningEffort, err = decodeManagerArguments[appwire.ThreadReasoningEffortSetParams](raw)
	case "compact":
		r.Compact, err = decodeManagerArguments[appwire.ThreadCompactStartParams](raw)
	case "drain_queue":
		r.DrainQueue, err = decodeManagerArguments[appwire.TurnDrainAsSteerParams](raw)
	case "promote_queued":
		r.PromoteQueued, err = decodeManagerArguments[appwire.TurnPromoteQueuedAsSteerParams](raw)
	case "cancel_queued":
		r.CancelQueued, err = decodeManagerArguments[appwire.TurnCancelQueuedParams](raw)
	case "rename":
		r.Rename, err = decodeManagerArguments[appwire.ThreadNameSetParams](raw)
	case "goal":
		r.Goal, err = decodeManagerArguments[appwire.GoalSetParams](raw)
	case "human_notes":
		r.HumanNotes, err = decodeManagerArguments[appwire.NotesHumanSetParams](raw)
	case "remove_url":
		r.RemoveURL, err = decodeManagerArguments[appwire.UrlsRemoveParams](raw)
	case "archive":
		r.Archive, err = decodeManagerArguments[appwire.ArchiveParams](raw)
	case "delete":
		r.Delete, err = decodeManagerArguments[appwire.SessionDeleteParams](raw)
	case "pin":
		r.Pin, err = decodeManagerArguments[appwire.SessionPinAssignParams](raw)
	case "unpin":
		r.Unpin, err = decodeManagerArguments[appwire.SessionPinUnpinParams](raw)
	case "seen":
		r.Seen, err = decodeManagerArguments[appwire.SessionSeenSetParams](raw)
	default:
		return r, appwire.InvalidParams("unknown session control action")
	}
	if err != nil {
		return r, err
	}
	return r, r.Validate()
}
