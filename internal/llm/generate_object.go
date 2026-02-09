package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

type GenerateObjectOptions struct {
	GenerateOptions
	Schema map[string]any
	Strict bool
}

func GenerateObject(ctx context.Context, opts GenerateObjectOptions) (*GenerateResult, error) {
	if opts.Schema == nil {
		return nil, &ConfigurationError{Message: "schema is required"}
	}
	strict := opts.Strict
	if !opts.Strict {
		// default
		strict = true
	}

	// Provider-specific structured output configuration is handled at the adapter layer.
	ro := opts.GenerateOptions
	ro.ResponseFormat = &ResponseFormat{
		Type:       "json_schema",
		JSONSchema: opts.Schema,
		Strict:     strict,
	}
	res, err := Generate(ctx, ro)
	if err != nil {
		return nil, err
	}

	var out any
	dec := json.NewDecoder(bytes.NewReader([]byte(res.Text)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, NewNoObjectGeneratedError(fmt.Sprintf("failed to parse JSON output: %v", err), res.Text)
	}

	schema, err := compileJSONSchema(opts.Schema)
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(out); err != nil {
		return nil, NewNoObjectGeneratedError(fmt.Sprintf("JSON output failed schema validation: %v", err), res.Text)
	}
	res.Output = out
	return res, nil
}

func compileJSONSchema(schema map[string]any) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	if err := c.AddResource("schema.json", bytes.NewReader(b)); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

