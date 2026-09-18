package interactiveartifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	validator "github.com/santhosh-tekuri/jsonschema/v5"
)

// ViewerResource identifies the immutable v1 viewer. The viewer implementation
// must supply its bundled bytes; this package grants no arbitrary URL fetching.
var ViewerResource = mcp.Resource{URI: "ui://evener-artifacts/viewer-v1.html", Name: "Artifact viewer v1", MIMEType: "text/html;profile=mcp-app"}

func forbids(names ...string) *jsonschema.Schema {
	schemas := make([]*jsonschema.Schema, 0, len(names))
	for _, name := range names {
		schemas = append(schemas, &jsonschema.Schema{Required: []string{name}})
	}
	return &jsonschema.Schema{Not: &jsonschema.Schema{AnyOf: schemas}}
}

// infer uses the public Go types as the authority for names, required fields and
// typed results. Type overrides express semantic ranges/enums once. Conditional
// request branches supplement the generator without a second schema catalog.
func infer[T any]() (*jsonschema.Schema, error) {
	return jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[Version]():            {Type: "integer", Minimum: new(float64(1)), Maximum: new(float64(MaxSafeInteger))},
		reflect.TypeFor[Format]():             {Type: "string", Enum: []any{"html"}},
		reflect.TypeFor[FormatVersion]():      {Type: "integer", Enum: []any{1}},
		reflect.TypeFor[Include]():            {Type: "string", Enum: []any{"source", "state", "diagnostics"}},
		reflect.TypeFor[DiagnosticKind]():     {Type: "string", Enum: []any{"runtime", "validation"}},
		reflect.TypeFor[json.RawMessage]():    {Type: "object"},
		reflect.TypeFor[CommittedStatus]():    {Type: "string", Enum: []any{"committed"}},
		reflect.TypeFor[RejectedStatus]():     {Type: "string", Enum: []any{"rejected"}},
		reflect.TypeFor[AcknowledgedStatus](): {Type: "string", Enum: []any{"acknowledged"}},
		reflect.TypeFor[ErrorCode]():          {Type: "string", Enum: []any{string(NotFoundOrForbidden), string(SourceConflict), string(StateConflict), string(MutationIDReused), string(UnsupportedFormat), string(InvalidSource), string(InvalidState), string(TooLarge), string(QuotaExceeded), string(Deleted), string(ServiceUnavailable), string(Busy)}},
	}})
}

// ModelSchema copies the complete JSON schema, excluding the trusted mutation ID.
// Nonserialized property-order hints are omitted; all mutable wire fields detach.
// Unknown-field rejection remains intact, so a model cannot inject that field.
func ModelSchema(schema *jsonschema.Schema) (*jsonschema.Schema, error) {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var model jsonschema.Schema
	if err := json.Unmarshal(encoded, &model); err != nil {
		return nil, err
	}
	delete(model.Properties, "mutationId")
	required := make([]string, 0, len(model.Required))
	for _, name := range model.Required {
		if name != "mutationId" {
			required = append(required, name)
		}
	}
	model.Required = required
	return &model, nil
}

func inputSchema(name string) (*jsonschema.Schema, error) {
	var schema *jsonschema.Schema
	var err error
	switch name {
	case "artifact_publish":
		schema, err = infer[PublishRequest]()
		if err != nil {
			return nil, err
		}
		schema.OneOf = []*jsonschema.Schema{
			forbids("artifactId", "expectedSourceRevision", "expectedStateVersion"),
			{Required: []string{"artifactId", "expectedSourceRevision", "expectedStateVersion"}, Not: &jsonschema.Schema{Required: []string{"initialState"}}},
		}
		schema.Properties["html"].MinLength = new(1)
		schema.Properties["format"].Default = json.RawMessage(`"html"`)
		schema.Properties["formatVersion"].Default = json.RawMessage(`1`)
		schema.Properties["initialState"].Default = json.RawMessage(`{}`)
	case "artifact_read":
		schema, err = infer[ReadRequest]()
		if err != nil {
			return nil, err
		}
		schema.If = &jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Required: []string{"sourceStartLine"}}, {Required: []string{"sourceEndLine"}}}}
		schema.Then = &jsonschema.Schema{Required: []string{"include"}, Properties: map[string]*jsonschema.Schema{"include": {Contains: &jsonschema.Schema{Const: new(any("source"))}}}}
	case "artifact_list":
		schema, err = infer[ListRequest]()
		if err != nil {
			return nil, err
		}
		schema.Properties["limit"].Maximum = new(float64(100))
		schema.Properties["limit"].Default = json.RawMessage(`20`)
	case "artifact_open":
		schema, err = infer[OpenRequest]()
	case "artifact_get_view":
		schema, err = infer[GetViewRequest]()
	case "artifact_save_state":
		schema, err = infer[SaveStateRequest]()
	case "artifact_report_diagnostic":
		schema, err = infer[ReportDiagnosticRequest]()
	default:
		return nil, errors.New("unknown artifact tool")
	}
	if err == nil {
		for _, name := range []string{"artifactId", "mutationId"} {
			if property, ok := schema.Properties[name]; ok {
				property.MinLength = new(1)
			}
		}
	}
	return schema, err
}

func outputSchema[T any]() (*jsonschema.Schema, error) {
	success, err := infer[T]()
	if err != nil {
		return nil, err
	}
	rejected, err := infer[RejectedResult]()
	if err != nil {
		return nil, err
	}
	domain := rejected.Properties["error"]
	domain.If = &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"code": {Enum: []any{"SOURCE_CONFLICT", "STATE_CONFLICT"}}}}
	domain.Then = &jsonschema.Schema{Required: []string{"sourceRevision", "stateVersion"}, Properties: map[string]*jsonschema.Schema{"retryable": {Const: new(any(false))}}}
	domain.Else = forbids("sourceRevision", "stateVersion")
	domain.AllOf = []*jsonschema.Schema{{If: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"code": {Const: new(any("BUSY"))}}}, Then: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"retryable": {Const: new(any(true))}}}}}
	return &jsonschema.Schema{Type: "object", OneOf: []*jsonschema.Schema{success, rejected}}, nil
}

// Tools returns a fresh public MCP catalog so projection and SDK registration
// cannot mutate the contracts used to validate a later request.
func Tools() ([]*mcp.Tool, error) {
	names := []string{"artifact_publish", "artifact_read", "artifact_list", "artifact_open", "artifact_get_view", "artifact_save_state", "artifact_report_diagnostic"}
	tools := make([]*mcp.Tool, 0, len(names))
	for _, name := range names {
		input, err := inputSchema(name)
		if err != nil {
			return nil, err
		}
		var output *jsonschema.Schema
		switch name {
		case "artifact_publish", "artifact_save_state":
			output, err = outputSchema[MutationReceipt]()
		case "artifact_read":
			output, err = outputSchema[ReadResult]()
		case "artifact_list":
			output, err = outputSchema[ListResult]()
		case "artifact_open":
			output, err = outputSchema[OpenResult]()
		case "artifact_get_view":
			output, err = outputSchema[GetViewResult]()
		case "artifact_report_diagnostic":
			output, err = outputSchema[DiagnosticResult]()
		}
		if err != nil {
			return nil, err
		}
		visibility := "model"
		if name == "artifact_get_view" || name == "artifact_save_state" || name == "artifact_report_diagnostic" {
			visibility = "app"
		}
		ui := map[string]any{"visibility": []string{visibility}}
		if name == "artifact_open" {
			ui["resourceUri"] = ViewerResource.URI
		}
		tools = append(tools, &mcp.Tool{Name: name, InputSchema: input, OutputSchema: output, Meta: mcp.Meta{"ui": ui}})
	}
	return tools, nil
}

// ParseRequest rejects lossy/ambiguous input before typed decoding. Model mode
// applies the trusted adapter's projection; it is not an authorization grant.
// Call Fingerprint on the original bytes before using the returned defaults.
func ParseRequest(tool string, data []byte, model bool) (result any, resultErr error) {
	mutation := tool == "artifact_publish" || tool == "artifact_save_state"
	if mutation {
		defer func() {
			var domain *DomainError
			if resultErr != nil && !errors.As(resultErr, &domain) {
				code := InvalidSource
				if tool == "artifact_save_state" {
					code = InvalidState
				}
				resultErr = &DomainError{Code: code}
			}
		}()
		if len(data) > MaxRequestBytes {
			return nil, &DomainError{Code: TooLarge}
		}
	}
	value, err := ParseJSON(data, MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	if mutation {
		if err := validateMutationFields(tool, value); err != nil {
			return nil, err
		}
	}
	if err := validateNumbers(value); err != nil {
		return nil, err
	}
	schema, err := inputSchema(tool)
	if err != nil {
		return nil, err
	}
	if model {
		schema, err = ModelSchema(schema)
		if err != nil {
			return nil, err
		}
	}
	if err := validateSchema(schema, value); err != nil {
		return nil, errors.New("invalid artifact arguments")
	}
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	var request any
	switch tool {
	case "artifact_publish":
		request = &PublishRequest{}
	case "artifact_read":
		request = &ReadRequest{}
	case "artifact_list":
		request = &ListRequest{}
	case "artifact_open":
		request = &OpenRequest{}
	case "artifact_get_view":
		request = &GetViewRequest{}
	case "artifact_save_state":
		request = &SaveStateRequest{}
	case "artifact_report_diagnostic":
		request = &ReportDiagnosticRequest{}
	}
	if err := json.Unmarshal(canonical, request); err != nil {
		return nil, errors.New("invalid artifact arguments")
	}
	switch request := request.(type) {
	case *PublishRequest:
		if len(request.HTML) > MaxSourceBytes {
			return nil, errors.New("source exceeds byte limit")
		}
		if request.InitialState != nil {
			state, err := ValidateState(request.InitialState)
			if err != nil {
				return nil, err
			}
			request.InitialState = state
		}
		if request.Format == "" {
			request.Format = "html"
		}
		if request.FormatVersion == 0 {
			request.FormatVersion = 1
		}
		if request.ArtifactID == "" && request.InitialState == nil {
			request.InitialState = json.RawMessage(`{}`)
		}
	case *SaveStateRequest:
		state, err := ValidateState(request.State)
		if err != nil {
			return nil, err
		}
		request.State = state
	case *ReadRequest:
		if request.SourceStartLine != 0 && request.SourceEndLine != 0 && request.SourceStartLine > request.SourceEndLine {
			return nil, errors.New("invalid source line range")
		}
	case *ListRequest:
		if request.Limit == 0 {
			request.Limit = 20
		}
	case *ReportDiagnosticRequest:
		if len(request.Message) > 4096 {
			return nil, errors.New("diagnostic exceeds byte limit")
		}
		if request.Kind == "" {
			request.Kind = "runtime"
		}
	}
	return request, nil
}

// validateSchema uses the existing exact-number validator. jsonschema-go's v0.4.3
// type checker classifies json.Number as a string, so it cannot validate the
// token-preserving input tree without a lossy conversion.
func validateSchema(schema *jsonschema.Schema, value any) error {
	data, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource("artifact.json", bytes.NewReader(data)); err != nil {
		return err
	}
	compiled, err := compiler.Compile("artifact.json")
	if err != nil {
		return err
	}
	return compiled.Validate(schemaInstance(value))
}

// ValidateResult checks the public structured result contract without returning
// private source/state in an error string.
func ValidateResult(tool string, data []byte) error {
	value, err := ParseJSON(data, MaxRequestBytes)
	if err != nil {
		return err
	}
	if err := validateNumbers(value); err != nil {
		return err
	}
	tools, err := Tools()
	if err != nil {
		return err
	}
	for _, candidate := range tools {
		if candidate.Name == tool {
			if err := validateSchema(candidate.OutputSchema.(*jsonschema.Schema), value); err != nil {
				return errors.New("invalid artifact result")
			}
			return nil
		}
	}
	return errors.New("unknown artifact tool")
}

// Zero exponents can exceed the validator's integer exponent parser despite a
// representable value. Project only those zeros for schema validation, retaining
// original tokens in state, typed arguments and fingerprint identity.
func schemaInstance(value any) any {
	switch v := value.(type) {
	case json.Number:
		n, integer, safe := exactSafeInteger(string(v))
		if integer && safe && n == 0 {
			return json.Number("0")
		}
		return v
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, child := range v {
			result[key] = schemaInstance(child)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = schemaInstance(child)
		}
		return result
	default:
		return value
	}
}

// validateMutationFields classifies semantic errors from parsed fields before
// schema validation. It never depends on a validator's human error wording.
func validateMutationFields(tool string, value any) error {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil
	} // The schema rejects nonobjects.
	stateField := "state"
	if tool == "artifact_publish" {
		stateField = "initialState"
		if format, present := fields["format"]; present && format != "html" {
			return &DomainError{Code: UnsupportedFormat}
		}
		if version, present := fields["formatVersion"]; present {
			number, ok := version.(json.Number)
			if !ok {
				return &DomainError{Code: UnsupportedFormat}
			}
			n, integer, safe := exactSafeInteger(string(number))
			if !integer || !safe || n != 1 {
				return &DomainError{Code: UnsupportedFormat}
			}
		}
		if source, ok := fields["html"].(string); ok && len(source) > MaxSourceBytes {
			return &DomainError{Code: TooLarge}
		}
	}
	if state, present := fields[stateField]; present {
		raw, err := CanonicalJSON(state)
		if err != nil {
			return &DomainError{Code: InvalidState}
		}
		if len(raw) > MaxStateBytes {
			return &DomainError{Code: TooLarge}
		}
		if _, err := ValidateState(raw); err != nil {
			return &DomainError{Code: InvalidState}
		}
	}
	return nil
}
