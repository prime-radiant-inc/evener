package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

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
		for _, name := range []string{"job_list", "job_status"} {
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
