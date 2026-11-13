package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidateToolName rejects tool names outside the strict common subset
// across providers: [a-zA-Z][a-zA-Z0-9_]* with a max length of 64.
func ValidateToolName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return &ConfigurationError{Message: "tool name is required"}
	}
	if len(n) > 64 {
		return &ConfigurationError{Message: fmt.Sprintf("tool name too long: %d > 64", len(n))}
	}
	b0 := n[0]
	if (b0 < 'a' || b0 > 'z') && (b0 < 'A' || b0 > 'Z') {
		return &ConfigurationError{Message: fmt.Sprintf("invalid tool name %q: must start with a letter", n)}
	}
	for i := 1; i < len(n); i++ {
		b := n[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			continue
		}
		return &ConfigurationError{Message: fmt.Sprintf("invalid tool name %q: invalid character %q", n, string(b))}
	}
	return nil
}

func defaultToolParameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func validateToolParameters(params map[string]any) error {
	if params == nil {
		// Treat nil as an empty object schema (common "no-args tool" case).
		return nil
	}
	typAny, ok := params["type"]
	if !ok {
		return &ConfigurationError{Message: "tool parameters must include type=object at the schema root"}
	}
	typ, ok := typAny.(string)
	if !ok || strings.ToLower(strings.TrimSpace(typ)) != "object" {
		return &ConfigurationError{Message: fmt.Sprintf("tool parameters must have type=object at the schema root; got %#v", typAny)}
	}
	return nil
}

// ValidateSchemaDocument reports whether v is a valid JSON Schema document
// under the draft 2020-12 meta-schema: the check a provider that validates
// tool parameters applies to every tool schema it receives (and the check
// that rejected a spliced delegate output schema in issue #6298, whose
// failure message named a properties value two levels deep). Callers bind
// caller-supplied schemas into tool parameters — a delegate's result_schema
// becomes the communicate tool's output parameter — and an invalid document
// spliced verbatim makes the child's first model call fail with a cryptic
// provider 400. Validating the document at the binding point rejects such a
// schema before it reaches the wire.
//
// The meta-schema is the copy embedded in the vendored jsonschema library, so
// the check is deterministic and issues no network requests. v may be any
// JSON-marshalable value; it is normalized through a marshal round-trip, so
// Go-typed maps and slices (map[string]any with []string values) validate the
// same as their decoded forms.
func ValidateSchemaDocument(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("schema document is not JSON-marshalable: %w", err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("schema document is not valid JSON: %w", err)
	}
	checker, err := schemaDocumentChecker()
	if err != nil {
		return err
	}
	if err := checker.Validate(doc); err != nil {
		return fmt.Errorf("not a valid JSON Schema (draft 2020-12): %w", err)
	}
	return nil
}

// schemaDocumentChecker compiles, once, a schema whose only content is a
// reference to the draft 2020-12 meta-schema. The library resolves that
// reference to its embedded copy of the meta-schema, so validating an
// instance against the checker applies the full meta-schema — including the
// $dynamicRef walk that rejects a properties value which is not a schema.
var (
	schemaDocumentOnce    sync.Once
	schemaDocumentMeta    *jsonschema.Schema
	errSchemaDocumentInit error
)

func schemaDocumentChecker() (*jsonschema.Schema, error) {
	schemaDocumentOnce.Do(func() {
		c := jsonschema.NewCompiler()
		c.Draft = jsonschema.Draft2020
		if err := c.AddResource("urn:evener:meta-schema", strings.NewReader(`{"$ref": "https://json-schema.org/draft/2020-12/schema"}`)); err != nil {
			errSchemaDocumentInit = err
			return
		}
		s, err := c.Compile("urn:evener:meta-schema")
		if err != nil {
			errSchemaDocumentInit = err
			return
		}
		schemaDocumentMeta = s
	})
	return schemaDocumentMeta, errSchemaDocumentInit
}
