package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func managerReadDefinition(def llm.ToolDefinition, ordinary bool) llm.ToolDefinition {
	var params map[string]any
	if ordinary {
		data, _ := json.Marshal(def.Parameters)
		_ = json.Unmarshal(data, &params)
	} else {
		params = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	}
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		params["properties"] = props
	}
	hub := map[string]any{"type": "object", "additionalProperties": false}
	if def.Name == "job_list" {
		hub["properties"] = map[string]any{
			"mode":             map[string]any{"type": "string", "enum": []string{"discovery"}},
			"cursor":           map[string]any{"type": "string"},
			"limit":            map[string]any{"type": "integer"},
			"sortKey":          map[string]any{"type": "string"},
			"sortDirection":    map[string]any{"type": "string"},
			"searchTerm":       map[string]any{"type": "string"},
			"statuses":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"sourceIds":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"includeSubagents": map[string]any{"type": "boolean"},
			"statusOnly":       map[string]any{"type": "boolean"},
		}
		hub["required"] = []string{"mode"}
		def.Description += " Manager hub discovery lists source-qualified sessions and sources, including empty hosts. Set hub.mode to discovery. Do not combine hub with ordinary job filters."
	} else {
		hub["properties"] = map[string]any{"ref": map[string]any{"type": "string"}}
		hub["required"] = []string{"ref"}
		def.Description += " Manager hub status reads a canonical session ref, its current state, pending questions and approvals, and supported capabilities. Do not combine hub with an owned target."
	}
	props["hub"] = hub
	required := params["required"]
	delete(params, "required")
	if ordinary {
		ordinaryBranch := map[string]any{"not": map[string]any{"required": []string{"hub"}}}
		if required != nil {
			ordinaryBranch["required"] = required
		}
		managerBranch := map[string]any{"required": []string{"hub"}, "properties": map[string]any{"hub": map[string]any{}, "intent": map[string]any{}}, "additionalProperties": false}
		params["anyOf"] = []any{managerBranch, ordinaryBranch}
	} else {
		params["required"] = []string{"hub"}
	}
	def.Parameters = params
	return def
}

// composeHubRegistry runs under s.mu. The original registrations are restored
// on disable, so synthetic Manager tools never grant an ordinary branch.
func (s *Session) composeHubRegistry() {
	if s.hubRegistry != s.reg {
		s.hubOrdinary = nil
		s.hubRegistry = s.reg
	}
	if s.hubOrdinary != nil {
		for name, base := range s.hubOrdinary {
			if base == nil {
				s.reg.Remove(name)
			} else {
				_ = s.reg.Register(*base)
			}
		}
	}
	if !s.hubEnabled {
		s.hubOrdinary = nil
		return
	}
	if s.hubOrdinary == nil {
		s.hubOrdinary = map[string]*tool.RegisteredTool{}
		for _, name := range []string{"job_list", "job_status", "delegate_send", "session_control"} {
			if base := s.reg.Get(name); base != nil {
				copy := *base
				s.hubOrdinary[name] = &copy
			} else {
				s.hubOrdinary[name] = nil
			}
		}
	}
	for _, name := range []string{"job_list", "job_status"} {
		if s.hubToolDenied(name) {
			s.reg.Remove(name)
			continue
		}
		base := s.hubOrdinary[name]
		var registered tool.RegisteredTool
		if base != nil {
			registered = *base
		} else if name == "job_list" {
			registered.Definition = tool.DefJobListNamed(s.resultToolName())
		} else {
			registered.Definition = tool.DefJobStatus()
		}
		ordinary := s.hubOrdinaryAvailable(name)
		registered.Definition = managerReadDefinition(registered.Definition, ordinary)
		registered.Schema = nil
		registered.ReadOnly = true
		registered.Execute = nil
		registered.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			if _, ok := args["hub"]; ok {
				return s.invokeHubRead(ctx, name, args)
			}
			if !ordinary {
				return nil, errors.New("ordinary tool branch is unavailable")
			}
			return base.Exec(ctx, env, args)
		}
		_ = s.reg.Register(registered)
	}
	s.composeHubInputTools()
}

func managerSendDefinition(def llm.ToolDefinition, ordinary bool) llm.ToolDefinition {
	manager := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"to":         map[string]any{"type": "string", "description": "Canonical source-qualified session ref, not an owned delegate id."},
			"input_mode": map[string]any{"type": "string", "enum": []string{"start", "queue", "steer"}},
			"message":    map[string]any{"type": "string"},
			"input": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"type": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"},
				"mediaType": map[string]any{"type": "string"}, "data": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"path": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"mentions": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"kind": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer"}}, "required": []string{"kind", "name", "offset"}}},
			}, "required": []string{"type"}}},
			"client_mutation_id":   map[string]any{"type": "string"},
			"expected_instance_id": map[string]any{"type": "string"},
		},
		"required": []string{"to", "input_mode", "client_mutation_id", "expected_instance_id"},
		"oneOf":    []any{map[string]any{"required": []string{"message"}}, map[string]any{"required": []string{"input"}}},
	}
	def.Description += " To send to a session on your Manager hub, set to to its canonical source-qualified ref and supply input_mode, client_mutation_id, expected_instance_id, and exactly one of message or typed input. The result is a human input receipt, not an owned delegate report. A notification does not answer a pending question."
	if ordinary {
		var ordinarySchema map[string]any
		encoded, _ := json.Marshal(def.Parameters)
		_ = json.Unmarshal(encoded, &ordinarySchema)
		properties := ordinarySchema["properties"].(map[string]any)
		for key, value := range manager["properties"].(map[string]any) {
			if _, present := properties[key]; !present {
				properties[key] = value
			}
		}
		properties["to"] = map[string]any{"type": "string", "description": "An owned delegate_id (`dlg_...`) for ordinary messages, or a canonical source-qualified session ref with input_mode for Manager input."}
		ordinarySchema["required"] = []string{"to"}
		ordinarySchema["anyOf"] = []any{
			map[string]any{"required": []string{"message"}, "not": map[string]any{"anyOf": []any{map[string]any{"required": []string{"input_mode"}}, map[string]any{"required": []string{"input"}}, map[string]any{"required": []string{"client_mutation_id"}}, map[string]any{"required": []string{"expected_instance_id"}}}}},
			map[string]any{"required": manager["required"], "oneOf": manager["oneOf"], "not": map[string]any{"required": []string{"max_wait_ms"}}},
		}
		def.Parameters = ordinarySchema
	} else {
		def.Parameters = manager
	}
	return def
}

func (s *Session) composeHubInputTools() {
	const send = "delegate_send"
	if !s.hubToolDenied(send) {
		base := s.hubOrdinary[send]
		var registered tool.RegisteredTool
		if base != nil {
			registered = *base
		} else {
			registered.Definition = tool.DefDelegateSendNamed(s.resultToolName())
		}
		ordinary := s.hubOrdinaryAvailable(send)
		registered.Definition = managerSendDefinition(registered.Definition, ordinary)
		registered.Schema = nil
		registered.Execute = nil
		registered.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			to, _ := args["to"].(string)
			if _, err := appwire.ParseRef(to); err == nil {
				return s.invokeHubInput(ctx, args)
			}
			if !ordinary || strings.Contains(to, ":") {
				return nil, appwire.InvalidParams("a canonical session ref is required for Manager input")
			}
			return base.Exec(ctx, env, args)
		}
		_ = s.reg.Register(registered)
	} else {
		s.reg.Remove(send)
	}
	const control = "session_control"
	if s.hubToolDenied(control) {
		return
	}
	registered := tool.RegisteredTool{}
	registered.Definition = llm.ToolDefinition{
		Name: control, Description: "Resolve a pending approval on a session reachable through your Manager hub. Only approval is implemented. The decision is attributed to the authenticated Manager session.",
		Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"approval"}},
			"arguments": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"ref": map[string]any{"type": "string"}, "escalationId": map[string]any{"type": "string"}, "approve": map[string]any{"type": "boolean"},
			}, "required": []string{"ref", "escalationId", "approve"}},
		}, "required": []string{"action", "arguments"}},
	}
	registered.Exec = func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		for key := range args {
			if key != "action" && key != "arguments" && key != "intent" {
				return nil, appwire.InvalidParams("unsupported session_control argument")
			}
		}
		if args["action"] != "approval" {
			return nil, appwire.InvalidParams("only approval is implemented")
		}
		raw, err := json.Marshal(args["arguments"])
		if err != nil {
			return nil, err
		}
		var p appwire.SandboxEscalationResolveParams
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil || p.ManagerProof != nil || p.ThreadID != "" {
			return nil, appwire.InvalidParams("invalid approval arguments")
		}
		if _, err := appwire.ParseRef(p.Ref); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		s.mu.Lock()
		client, enabled := s.hubClient, s.hubEnabled
		s.mu.Unlock()
		if !enabled || client == nil {
			return nil, appwire.InvalidRequest("Manager tool unavailable")
		}
		response, err := client.Invoke(ctx, appwire.SessionOperationRequest{Control: &appwire.SessionControlRequest{Approval: &p}})
		if err != nil {
			return nil, err
		}
		return response, nil
	}
	_ = s.reg.Register(registered)
}

func (s *Session) invokeHubInput(ctx context.Context, args map[string]any) (any, error) {
	for key := range args {
		switch key {
		case "to", "input_mode", "message", "input", "client_mutation_id", "expected_instance_id", "intent":
		default:
			return nil, appwire.InvalidParams("ordinary delegate arguments cannot be combined with Manager input")
		}
	}
	to, _ := args["to"].(string)
	if _, err := appwire.ParseRef(to); err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	_, hasText := args["message"]
	_, hasInput := args["input"]
	if hasText == hasInput {
		return nil, appwire.InvalidParams("provide exactly one of message or typed input")
	}
	var input []appwire.InputItem
	if hasText {
		text, ok := args["message"].(string)
		if !ok {
			return nil, appwire.InvalidParams("message must be text")
		}
		input = []appwire.InputItem{{Type: "text", Text: text}}
	} else {
		raw, err := json.Marshal(args["input"])
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&input); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
	}
	id, _ := args["client_mutation_id"].(string)
	instance, _ := args["expected_instance_id"].(string)
	var request appwire.SessionOperationRequest
	switch args["input_mode"] {
	case "start":
		request.Start = &appwire.TurnStartParams{Ref: to, Input: input, ClientMutationID: id, ExpectedInstanceID: instance}
	case "queue":
		request.Queue = &appwire.TurnQueueParams{Ref: to, Input: input, ClientMutationID: id, ExpectedInstanceID: instance}
	case "steer":
		request.Steer = &appwire.TurnSteerParams{Ref: to, Input: input, ClientMutationID: id, ExpectedInstanceID: instance}
	default:
		return nil, appwire.InvalidParams("input_mode must be start, queue or steer")
	}
	s.mu.Lock()
	client, enabled := s.hubClient, s.hubEnabled
	s.mu.Unlock()
	if !enabled || client == nil {
		return nil, appwire.InvalidRequest("Manager tool unavailable")
	}
	return client.Invoke(ctx, request)
}

func (s *Session) hubOrdinaryAvailable(name string) bool {
	if s.hubOrdinary[name] == nil {
		return false
	}
	for _, definition := range s.profile.ToolDefinitions() {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func (s *Session) invokeHubRead(ctx context.Context, name string, args map[string]any) (any, error) {
	s.mu.Lock()
	client := s.hubClient
	denied := s.hubToolDenied(name)
	s.mu.Unlock()
	if denied || client == nil {
		return nil, appwire.InvalidRequest("Manager tool unavailable")
	}
	for key := range args {
		if key != "hub" && key != "intent" {
			return nil, appwire.InvalidParams("hub arguments cannot be combined with ordinary arguments")
		}
	}
	raw, err := json.Marshal(args["hub"])
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request appwire.SessionOperationRequest
	if name == "job_list" {
		var p struct {
			Mode string `json:"mode"`
			appwire.ThreadListParams
		}
		if err := decoder.Decode(&p); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if p.Mode != "discovery" {
			return nil, appwire.InvalidParams("only discovery is implemented")
		}
		request.Discover = &p.ThreadListParams
	} else {
		var p struct {
			Ref string `json:"ref"`
		}
		if err := decoder.Decode(&p); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if _, err := appwire.ParseRef(p.Ref); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		request.Read = &appwire.ThreadReadParams{Ref: p.Ref}
	}
	response, err := client.Invoke(ctx, request)
	if err != nil {
		return nil, err
	}
	return response, nil
}
