package jsonschema_test

import (
	"strings"
	"testing"

	"primeradiant.com/evener/llm/jsonschema"
)

// This is a preservation check, first run against upstream before transfer.
func TestValidatorDraftBaseline(t *testing.T) {
	for _, draft := range []*jsonschema.Draft{jsonschema.Draft4, jsonschema.Draft6, jsonschema.Draft7, jsonschema.Draft2019, jsonschema.Draft2020} {
		t.Run(draft.String(), func(t *testing.T) {
			compiler := jsonschema.NewCompiler()
			compiler.Draft = draft
			if err := compiler.AddResource("urn:evener:positive", strings.NewReader(`{"type":"integer","minimum":0}`)); err != nil {
				t.Fatal(err)
			}
			if err := compiler.AddResource("urn:evener:entry", strings.NewReader(`{"$ref":"urn:evener:positive"}`)); err != nil {
				t.Fatal(err)
			}
			schema, err := compiler.Compile("urn:evener:entry")
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(1); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(-1); err == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}
