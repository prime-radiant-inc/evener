package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
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
	switch def.Name {
	case "job_list":
		hub = map[string]any{"type": "object", "anyOf": []any{
			map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mode"}, "properties": map[string]any{
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
			}},
			map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mode", "ref"}, "properties": map[string]any{
				"mode":     map[string]any{"type": "string", "enum": []string{"tasks"}},
				"ref":      map[string]any{"type": "string"},
				"offset":   map[string]any{"type": "integer"},
				"limit":    map[string]any{"type": "integer"},
				"snapshot": map[string]any{"type": "string"},
			}},
			map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mode", "ref", "resource"}, "properties": map[string]any{
				"mode":     map[string]any{"type": "string", "enum": []string{"activity"}},
				"ref":      map[string]any{"type": "string"},
				"resource": map[string]any{"type": "string", "enum": []string{"summary", "delegates", "jobs", "watches"}},
				"scope":    map[string]any{"type": "string"},
				"cursor":   map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}},
		}}
		def.Description += " Manager hub discovery lists source-qualified sessions and sources, including empty hosts. Set hub.mode to discovery. Do not combine hub with ordinary job filters."
	case "model_list":
		hub["properties"] = map[string]any{"host": map[string]any{"type": "string"}, "cwd": map[string]any{"type": "string"}, "refresh": map[string]any{"type": "boolean"}}
		hub["required"] = []string{"host"}
		def.Description += " Manager hub models lists the selected host's available models and diagnostics. refresh waits for a fresh catalog."
	case "read_transcript":
		// The decoder treats these explicit zero values like omitted fields.
		// Keep those calls valid without allowing nonzero cross-branch bounds.
		hub = map[string]any{"type": "object", "anyOf": []any{
			map[string]any{"type": "object", "additionalProperties": false, "required": []string{"ref"}, "properties": map[string]any{
				"ref":       map[string]any{"type": "string"},
				"jobId":     map[string]any{"type": "string", "enum": []string{""}},
				"cursor":    map[string]any{"type": "string"},
				"itemLimit": map[string]any{"type": "integer"},
				"maxBytes":  map[string]any{"type": "integer", "enum": []int{0}},
			}},
			map[string]any{"type": "object", "additionalProperties": false, "required": []string{"ref", "jobId"}, "properties": map[string]any{
				"ref":         map[string]any{"type": "string"},
				"jobId":       map[string]any{"type": "string", "minLength": 1},
				"maxBytes":    map[string]any{"type": "integer"},
				"beforeBytes": map[string]any{"type": "integer"},
				"cursor":      map[string]any{"type": "string", "enum": []string{""}},
				"itemLimit":   map[string]any{"type": "integer", "enum": []int{0}},
			}},
		}}
		def.Description += " Manager hub reads a canonical owner ref. jobId selects job metadata in job_status, or bounded job output in read_transcript. read_transcript without jobId pages session history with cursor. Local JSONL, expansion, artifacts, and range arguments do not apply."
	default:
		hub["properties"] = map[string]any{"ref": map[string]any{"type": "string"}, "jobId": map[string]any{"type": "string"}}
		hub["required"] = []string{"ref"}
		def.Description += " Manager hub reads a canonical owner ref. jobId selects job metadata in job_status, or bounded job output in read_transcript. read_transcript without jobId pages session history with cursor. Local JSONL, expansion, artifacts, and range arguments do not apply."
	}
	props["hub"] = hub
	required := params["required"]
	delete(params, "required")
	if ordinary {
		ordinaryBranch := map[string]any{"type": "object", "not": map[string]any{"type": "object", "required": []string{"hub"}}}
		if required != nil {
			ordinaryBranch["required"] = required
		}
		managerBranch := map[string]any{"type": "object", "required": []string{"hub"}, "properties": map[string]any{"hub": hub, "intent": map[string]any{"type": "string"}}, "additionalProperties": false}
		params["anyOf"] = []any{managerBranch, ordinaryBranch}
	} else {
		params["required"] = []string{"hub"}
	}
	// Strictification requires every property, breaking optional arguments and
	// mutually exclusive ordinary/Manager branches. Keep the validated schema.
	strictFalse := false
	def.Strict = &strictFalse
	def.Parameters = params
	return def
}

// composeHubRegistry runs under s.mu. Rebuild from the original registrations
// so repeated composition never grants an ordinary branch to a synthetic tool.
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
		for _, name := range []string{"job_list", "job_status", "read_transcript", "model_list", "job_watch", "delegate_send", "delegate", "job_stop", "session_control"} {
			if base := s.reg.Get(name); base != nil {
				definition := *base
				s.hubOrdinary[name] = &definition
			} else {
				s.hubOrdinary[name] = nil
			}
		}
	}
	for _, name := range []string{"job_list", "job_status", "read_transcript", "model_list"} {
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
		} else if name == "job_status" {
			registered.Definition = tool.DefJobStatus()
		} else {
			continue
		}
		ordinary := s.hubOrdinaryAvailable(name)
		registered.Definition = managerReadDefinition(registered.Definition, ordinary)
		registered.Schema = nil
		registered.Limit = schema.ToolOutputLimit{MaxChars: transcriptToolMaxChars}
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
	s.composeHubWatchTool()
	s.composeHubInputTools()
	s.composeHubDelegateTool()
	s.composeHubStopTool()
}

// managerDelegateDefinition keeps the owned-delegate contract on calls without
// hub. A hub call creates a top-level session instead; none of its result is an
// owned delegate report or an acknowledgement of one.
func managerDelegateDefinition(def llm.ToolDefinition, ordinary bool) llm.ToolDefinition {
	var params map[string]any
	if ordinary {
		data, _ := json.Marshal(def.Parameters)
		_ = json.Unmarshal(data, &params)
	} else {
		params = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"prompt": map[string]any{"type": "string"}}}
	}
	props := params["properties"].(map[string]any)
	props["hub"] = map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"cwd":   map[string]any{"type": "string"},
			"model": map[string]any{"type": "string"},
			"watch": hubWatchSelectorSchema(nil, nil),
		}, "required": []string{"cwd", "model", "watch"},
	}
	params["required"] = []string{"prompt"}
	if ordinary {
		params["anyOf"] = []any{
			map[string]any{"type": "object", "not": map[string]any{"type": "object", "required": []string{"hub"}}},
			map[string]any{"type": "object", "required": []string{"hub"}, "properties": map[string]any{"hub": props["hub"], "prompt": props["prompt"], "intent": map[string]any{"type": "string"}}, "additionalProperties": false},
		}
	} else {
		params["required"] = []string{"prompt", "hub"}
	}
	def.Description += " In a Manager session, hub={cwd,model,watch} creates an independent local top-level session, attaches the requested watch and reads its initial state before sending prompt. Creation, watch and input are reported separately. A failed watch leaves the created session empty; an unknown launch is never retried. Hub results are not owned delegate reports. Remote creation is not supported."
	strictFalse := false
	def.Strict = &strictFalse
	def.Parameters = params
	return def
}

func hubWatchSelectorSchema(extraProperties map[string]any, required []string) map[string]any {
	properties := map[string]any{
		"events":     map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": slices.Clone(hubObservationEvents[:])}},
		"completion": map[string]any{"type": "boolean"},
	}
	maps.Copy(properties, extraProperties)
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"anyOf": []any{
			map[string]any{"type": "object", "required": []string{"events"}, "properties": map[string]any{"events": map[string]any{"type": "array", "minItems": 1}}},
			map[string]any{"type": "object", "required": []string{"completion"}, "properties": map[string]any{"completion": map[string]any{"type": "boolean", "const": true}}},
		},
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (s *Session) composeHubDelegateTool() {
	const name = "delegate"
	if s.hubToolDenied(name) || s.delegationAllowance <= 0 || s.hubOrdinary[name] == nil {
		s.reg.Remove(name)
		return
	}
	base := s.hubOrdinary[name]
	registered := *base
	ordinary := s.hubOrdinaryAvailable(name)
	registered.Definition = managerDelegateDefinition(registered.Definition, ordinary)
	registered.Schema = nil
	registered.Execute = nil
	registered.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		if _, ok := args["hub"]; ok {
			return s.createHubDelegate(ctx, args)
		}
		if !ordinary {
			return nil, appwire.InvalidRequest("ordinary delegation is unavailable")
		}
		return base.Exec(ctx, env, args)
	}
	_ = s.reg.Register(registered)
}

type managerCreateStage struct {
	Status     string `json:"status"`
	Ref        string `json:"ref,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`
	Error      string `json:"error,omitempty"`
}

type managerWatchStage struct {
	Status  string `json:"status"`
	WatchID string `json:"watch_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

type managerInputStage struct {
	Status           string                      `json:"status"`
	Disposition      appwire.MutationDisposition `json:"disposition,omitempty"`
	Error            string                      `json:"error,omitempty"`
	ClientMutationID string                      `json:"clientMutationId,omitempty"`
}

type managerCreateOutcome struct {
	Created managerCreateStage `json:"created"`
	Watch   managerWatchStage  `json:"watch"`
	Input   managerInputStage  `json:"input"`
}

func (s *Session) createHubDelegate(ctx context.Context, args map[string]any) (any, error) {
	for key := range args {
		if key != "prompt" && key != "hub" && key != "intent" {
			return nil, appwire.InvalidParams("hub creation cannot combine owned delegate arguments")
		}
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return nil, appwire.InvalidParams("prompt is required")
	}
	raw, err := json.Marshal(args["hub"])
	if err != nil {
		return nil, err
	}
	var hub struct {
		CWD   string                      `json:"cwd"`
		Model string                      `json:"model"`
		Watch appwire.SessionWatchRequest `json:"watch"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&hub); err != nil || hub.CWD == "" || hub.Model == "" {
		return nil, appwire.InvalidParams("hub creation requires cwd, model and watch")
	}
	if err := validateHubWatchSelectors(hub.Watch.Events, hub.Watch.Completion); err != nil {
		return nil, appwire.InvalidParams("invalid hub watch selectors")
	}
	s.mu.Lock()
	client, enabled := s.hubClient, s.hubEnabled
	s.mu.Unlock()
	if client == nil || !enabled {
		return nil, appwire.InvalidRequest("Manager tool unavailable")
	}
	out := managerCreateOutcome{Created: managerCreateStage{Status: "unknown"}, Watch: managerWatchStage{Status: "notAttached"}, Input: managerInputStage{Status: "notSubmitted"}}
	created, err := client.Invoke(ctx, appwire.SessionOperationRequest{Create: &appwire.ThreadStartParams{
		Harness: "evener", Source: "local", CWD: hub.CWD, Model: hub.Model,
	}})
	if err != nil {
		out.Created.Error = err.Error()
		if wire, ok := errors.AsType[appwire.WireError](err); ok {
			var partial appwire.ThreadStartPartialErrorData
			if body, marshalErr := json.Marshal(wire.Data); marshalErr == nil && json.Unmarshal(body, &partial) == nil {
				out.Created.Ref, out.Created.InstanceID = partial.CreatedRef, partial.CreatedInstanceID
				if partial.CreatedRef != "" {
					out.Created.Status = "created"
				} else if partial.MutationOutcome == appwire.MutationOutcomeNotAccepted {
					out.Created.Status = "notAccepted"
				}
			}
		}
		return out, nil
	}
	if created.Create == nil || created.Create.Thread.Evener.Ref == "" || created.Create.Thread.Evener.InstanceID == "" {
		out.Created.Error = "creation returned no authoritative identity"
		return out, nil
	}
	out.Created = managerCreateStage{Status: "created", Ref: created.Create.Thread.Evener.Ref, InstanceID: created.Create.Thread.Evener.InstanceID}
	// The watch owner subscribes, reads an authoritative initial snapshot and
	// persists the watch before this adapter may submit any task input.
	hub.Watch.Ref = out.Created.Ref
	watch, err := s.configureHubWatchRuntime(ctx, map[string]any{"operation": "create", "hub": hub.Watch})
	if err != nil {
		out.Watch.Status = "failed"
		out.Watch.Error = err.Error()
		// The partial watch outcome is returned structurally to preserve the created identity.
		//nolint:nilerr
		return out, nil
	}
	out.Watch.Status = "attached"
	if result, ok := watch.(tool.StateResult); ok {
		watch = result.State
	}
	if encoded, marshalErr := json.Marshal(watch); marshalErr == nil {
		var registered struct {
			WatchID string `json:"watch_id"`
		}
		if json.Unmarshal(encoded, &registered) == nil {
			out.Watch.WatchID = registered.WatchID
		}
	}
	if out.Watch.WatchID == "" {
		out.Watch = managerWatchStage{Status: "failed", Error: "watch returned no durable identity"}
		return out, nil
	}
	id, err := identifier.NewClientMutationID()
	if err != nil {
		out.Input.Error = err.Error()
		// The partial input outcome is returned structurally to preserve the created identity.
		//nolint:nilerr
		return out, nil
	}
	input, err := client.Invoke(ctx, appwire.SessionOperationRequest{Start: &appwire.TurnStartParams{
		Ref: out.Created.Ref, ExpectedInstanceID: out.Created.InstanceID, ClientMutationID: id,
		Input: []appwire.InputItem{{Type: "text", Text: prompt}},
	}})
	if err != nil {
		out.Input = managerInputStage{Status: "unknown", Error: err.Error(), ClientMutationID: id}
		if wire, ok := errors.AsType[appwire.WireError](err); ok {
			var detail appwire.ErrorData
			if raw, marshalErr := json.Marshal(wire.Data); marshalErr == nil && json.Unmarshal(raw, &detail) == nil && detail.MutationOutcome == appwire.MutationOutcomeNotAccepted {
				out.Input.Status = "notAccepted"
			}
		}
		return out, nil
	}
	if input.Start == nil {
		out.Input = managerInputStage{Status: "unknown", Error: "input returned no receipt", ClientMutationID: id}
		return out, nil
	}
	out.Input = managerInputStage{Status: "submitted", Disposition: input.Start.Receipt.Disposition, ClientMutationID: id}
	return out, nil
}

func managerWatchDefinition(def llm.ToolDefinition) llm.ToolDefinition {
	var params map[string]any
	encoded, _ := json.Marshal(def.Parameters)
	_ = json.Unmarshal(encoded, &params)
	params["properties"].(map[string]any)["hub"] = hubWatchSelectorSchema(map[string]any{"ref": map[string]any{"type": "string"}}, []string{"ref"})
	def.Description += " In a Manager session, operation=create and hub={ref,events,completion} watches local or remote sessions through the shared hub subscription path. Completion requires accepted terminal done for the watched execution. Active or question-pending watches retain their turn across reconnect; fresh idle watches catch up only with the latest recorded work. Connection loss suspends, target replacement ends the watch, and Manager runtime restart requires re-registration."
	def.Parameters = params
	return def
}

func (s *Session) composeHubWatchTool() {
	const name = "job_watch"
	if s.hubToolDenied(name) {
		s.reg.Remove(name)
		return
	}
	base := s.hubOrdinary[name]
	var registered tool.RegisteredTool
	if base != nil {
		registered = *base
	} else {
		registered.Definition = tool.DefJobWatchNamed(availableEventKindNames(), s.resultToolName())
	}
	registered.Definition = managerWatchDefinition(registered.Definition)
	registered.Schema = nil
	registered.Execute = nil
	registered.Exec = func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		if _, ok := args["hub"]; ok {
			return s.configureHubWatchRuntime(ctx, args)
		}
		if !s.hubOrdinaryAvailable(name) {
			return nil, appwire.InvalidRequest("ordinary job_watch is unavailable")
		}
		return base.Exec(ctx, env, args)
	}
	_ = s.reg.Register(registered)
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
		"oneOf":    []any{map[string]any{"type": "object", "required": []string{"message"}}, map[string]any{"type": "object", "required": []string{"input"}}},
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
			map[string]any{"type": "object", "required": []string{"message"}, "not": map[string]any{"type": "object", "anyOf": []any{map[string]any{"type": "object", "required": []string{"input_mode"}}, map[string]any{"type": "object", "required": []string{"input"}}, map[string]any{"type": "object", "required": []string{"client_mutation_id"}}, map[string]any{"type": "object", "required": []string{"expected_instance_id"}}}}},
			map[string]any{"type": "object", "required": manager["required"], "oneOf": manager["oneOf"], "not": map[string]any{"type": "object", "required": []string{"max_wait_ms"}}},
		}
		def.Parameters = ordinarySchema
	} else {
		def.Parameters = manager
	}
	strictFalse := false
	def.Strict = &strictFalse
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
	strictFalse := false
	registered.Definition = llm.ToolDefinition{
		Name: control, Description: "Apply one typed human session control on the Manager hub. Returns the existing owner's result and authenticated Manager attribution.",
		Parameters: managerControlSchema(),
		Strict:     &strictFalse,
	}
	registered.Exec = func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		for key := range args {
			if key != "action" && key != "arguments" && key != "intent" {
				return nil, appwire.InvalidParams("unsupported session_control argument")
			}
		}
		raw, err := json.Marshal(args["arguments"])
		if err != nil {
			return nil, err
		}
		request, err := decodeManagerControl(args["action"], raw)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		client, enabled := s.hubClient, s.hubEnabled
		s.mu.Unlock()
		if !enabled || client == nil {
			return nil, appwire.InvalidRequest("Manager tool unavailable")
		}
		response, err := client.Invoke(ctx, appwire.SessionOperationRequest{Control: &request})
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
		decoded, err := decodeManagerArguments[[]appwire.InputItem](raw)
		if err != nil {
			return nil, err
		}
		input = *decoded
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
	switch name {
	case "job_list":
		var mode struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(raw, &mode); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		switch mode.Mode {
		case "discovery":
			var p struct {
				Mode string `json:"mode"`
				appwire.ThreadListParams
			}
			if err := decoder.Decode(&p); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			request.Discover = &p.ThreadListParams
		case "tasks":
			var page struct {
				Mode string `json:"mode"`
				appwire.SessionTasksRequest
			}
			if err := decoder.Decode(&page); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			if _, err := appwire.ParseRef(page.Ref); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			request.Tasks = &page.SessionTasksRequest
		case "activity":
			var page struct {
				Mode string `json:"mode"`
				appwire.SessionActivityRequest
			}
			if err := decoder.Decode(&page); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			if _, err := appwire.ParseRef(page.Ref); err != nil {
				return nil, appwire.InvalidParams(err.Error())
			}
			request.Activity = &page.SessionActivityRequest
		default:
			return nil, appwire.InvalidParams("unknown Manager list mode")
		}
	case "model_list":
		var p struct {
			Host    string `json:"host"`
			CWD     string `json:"cwd"`
			Refresh bool   `json:"refresh"`
		}
		if err := decoder.Decode(&p); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if p.Host == "" {
			return nil, appwire.InvalidParams("selected host is required")
		}
		request.Models = &appwire.ModelListParams{Harness: p.Host, CWD: p.CWD, Refresh: p.Refresh}
	case "read_transcript":
		var p struct {
			Ref         string `json:"ref"`
			JobID       string `json:"jobId"`
			Cursor      string `json:"cursor"`
			ItemLimit   int    `json:"itemLimit"`
			MaxBytes    int64  `json:"maxBytes"`
			BeforeBytes *int64 `json:"beforeBytes"`
		}
		if err := decoder.Decode(&p); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if _, err := appwire.ParseRef(p.Ref); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if p.JobID != "" {
			if p.Cursor != "" || p.ItemLimit != 0 {
				return nil, appwire.InvalidParams("job output cannot combine history cursors")
			}
			request.JobOutput = &appwire.JobsOutputParams{Ref: p.Ref, JobID: p.JobID, MaxBytes: p.MaxBytes, BeforeBytes: p.BeforeBytes}
		} else {
			if p.MaxBytes != 0 || p.BeforeBytes != nil {
				return nil, appwire.InvalidParams("history cannot combine job output bounds")
			}
			request.Turns = &appwire.ThreadTurnsListParams{Ref: p.Ref, Cursor: p.Cursor, ItemLimit: p.ItemLimit}
		}
	default:
		var p struct {
			Ref   string `json:"ref"`
			JobID string `json:"jobId"`
		}
		if err := decoder.Decode(&p); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if _, err := appwire.ParseRef(p.Ref); err != nil {
			return nil, appwire.InvalidParams(err.Error())
		}
		if p.JobID != "" {
			request.Job = &appwire.JobsGetParams{Ref: p.Ref, JobID: p.JobID}
		} else {
			request.Read = &appwire.ThreadReadParams{Ref: p.Ref}
		}
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	response, err := client.Invoke(ctx, request)
	if err != nil {
		return nil, err
	}
	return response, nil
}
