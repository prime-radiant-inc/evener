package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
)

func managerStopSchema(base map[string]any) map[string]any {
	props := base["properties"].(map[string]any)
	modes := []struct {
		name   string
		params any
	}{{"interrupt", appwire.TurnInterruptParams{}}, {"delegate", appwire.DelegateStopParams{}}, {"owned_delegate", appwire.SessionOwnedDelegateStopParams{}}, {"job", appwire.SessionJobStopParams{}}, {"shutdown", appwire.ThreadShutdownParams{}}, {"force_stop", appwire.ThreadForceStopParams{}}}
	names := make([]string, 0, len(modes))
	branches := make([]any, 0, len(modes))
	for _, mode := range modes {
		names = append(names, mode.name)
		branches = append(branches, map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"type": "string", "const": mode.name}, "arguments": managerParametersSchema(reflect.TypeOf(mode.params))}, "required": []string{"mode", "arguments"}})
	}
	props["hub"] = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"mode": map[string]any{"type": "string", "enum": names}, "arguments": map[string]any{"type": "object"}}, "required": []string{"mode", "arguments"}, "oneOf": branches}
	base["anyOf"] = []any{map[string]any{"type": "object", "required": []string{"target"}, "not": map[string]any{"type": "object", "required": []string{"hub"}}}, map[string]any{"type": "object", "required": []string{"hub"}, "not": map[string]any{"type": "object", "required": []string{"target"}}}}
	delete(base, "required")
	return base
}

func (s *Session) composeHubStopTool() {
	const name = "job_stop"
	if s.hubToolDenied(name) {
		s.reg.Remove(name)
		return
	}
	base := s.hubOrdinary[name]
	if base == nil {
		return
	}
	registered := *base
	var params map[string]any
	raw, _ := json.Marshal(registered.Definition.Parameters)
	_ = json.Unmarshal(raw, &params)
	registered.Definition.Parameters = managerStopSchema(params)
	// Requiring every property would require both target and hub, an invalid call.
	strictFalse := false
	registered.Definition.Strict = &strictFalse
	registered.Definition.Description += " With Manager enabled, hub={mode,arguments} selects interrupt, delegate (dlg_ ID, one run only, children survive), owned_delegate (dlg_ ID, recursive owned subtree), job (job_ ID, only that shell job plus immediate children when includeChildren is true), shutdown, or force-stop on a source-qualified session."
	registered.Schema = nil
	registered.Execute = nil
	registered.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		if _, hasHub := args["hub"]; hasHub {
			return s.invokeHubStop(ctx, args)
		}
		return base.Exec(ctx, env, args)
	}
	_ = s.reg.Register(registered)
}

func (s *Session) invokeHubStop(ctx context.Context, args map[string]any) (any, error) {
	for key := range args {
		if key != "hub" && key != "intent" {
			return nil, appwire.InvalidParams("Manager stop cannot combine ordinary job arguments")
		}
	}
	raw, err := json.Marshal(args["hub"])
	if err != nil {
		return nil, err
	}
	type stopArguments struct {
		Mode      string          `json:"mode"`
		Arguments json.RawMessage `json:"arguments"`
	}
	hub, err := decodeManagerArguments[stopArguments](raw)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(hub.Arguments, &fields); err != nil || fields == nil {
		return nil, appwire.InvalidParams("typed stop arguments required")
	}
	if _, exists := fields["threadId"]; exists {
		return nil, appwire.InvalidParams("a canonical ref is required")
	}
	var request appwire.SessionStopRequest
	request.Mode = hub.Mode
	switch hub.Mode {
	case "interrupt":
		request.Interrupt, err = decodeManagerArguments[appwire.TurnInterruptParams](hub.Arguments)
	case "delegate":
		request.Delegate, err = decodeManagerArguments[appwire.DelegateStopParams](hub.Arguments)
	case "owned_delegate":
		request.OwnedDelegate, err = decodeManagerArguments[appwire.SessionOwnedDelegateStopParams](hub.Arguments)
	case "job":
		request.Job, err = decodeManagerArguments[appwire.SessionJobStopParams](hub.Arguments)
	case "shutdown":
		request.Shutdown, err = decodeManagerArguments[appwire.ThreadShutdownParams](hub.Arguments)
	case "force_stop":
		request.ForceStop, err = decodeManagerArguments[appwire.ThreadForceStopParams](hub.Arguments)
	default:
		return nil, appwire.InvalidParams("unknown Manager stop mode")
	}
	if err != nil {
		return nil, err
	}
	var ref string
	switch hub.Mode {
	case "interrupt":
		ref = request.Interrupt.Ref
	case "delegate":
		ref = request.Delegate.Ref
	case "owned_delegate":
		ref = request.OwnedDelegate.Ref
	case "job":
		ref = request.Job.Ref
	case "shutdown":
		ref = request.Shutdown.Ref
	case "force_stop":
		ref = request.ForceStop.Ref
	}
	if _, err := appwire.ParseRef(strings.TrimSpace(ref)); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	client, enabled := s.hubClient, s.hubEnabled
	s.mu.Unlock()
	if !enabled || client == nil {
		return nil, appwire.InvalidRequest("Manager tool unavailable")
	}
	return client.Invoke(ctx, appwire.SessionOperationRequest{Stop: &request})
}
