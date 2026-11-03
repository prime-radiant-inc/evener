package jsonschema_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/internal/numerictest"
	"primeradiant.com/evener/llm/jsonschema"
)

func TestNumericMetaschemaReferences(t *testing.T) {
	for _, draft := range []string{"http://json-schema.org/draft-04/schema#", "http://json-schema.org/draft-06/schema#", "http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2019-09/schema", "https://json-schema.org/draft/2020-12/schema"} {
		t.Run(strings.TrimPrefix(draft, "http"), func(t *testing.T) {
			numerictest.Run(t, "draft", func(t *testing.T) {
				for _, divisor := range []string{"0", "-1"} {
					_, err := jsonschema.CompileString("https://numeric.test/invalid", fmt.Sprintf(`{"$schema":%q,"multipleOf":%s}`, draft, divisor))
					var se *jsonschema.SchemaError
					var ve *jsonschema.ValidationError
					if !errors.As(err, &se) || !errors.As(err, &ve) {
						t.Fatalf("invalid multipleOf %s: %T", divisor, err)
					}
				}
				bounds := `"exclusiveMinimum":1e999999999999999999999,"exclusiveMaximum":3e999999999999999999999`
				if strings.Contains(draft, "draft-04") {
					bounds = `"minimum":1e999999999999999999999,"exclusiveMinimum":true,"maximum":3e999999999999999999999,"exclusiveMaximum":true`
				}
				s := numericSchema(t, fmt.Sprintf(`{"$schema":%q,%s}`, draft, bounds))
				for _, tc := range []struct {
					v     json.Number
					valid bool
				}{{"1e999999999999999999999", false}, {"2e999999999999999999999", true}, {"3e999999999999999999999", false}} {
					if got := s.Validate(tc.v) == nil; got != tc.valid {
						t.Fatal(tc, got)
					}
				}
			})
		})
	}
	t.Run("custom", func(t *testing.T) {
		numerictest.Run(t, "custom-meta-ref", func(t *testing.T) {
			c := jsonschema.NewCompiler()
			resources := []struct{ url, text string }{
				{"https://numeric.test/meta", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"ceiling":{"type":"number","maximum":1e999999999999999999999}}}`},
				{"https://numeric.test/target", `{"$schema":"https://numeric.test/meta","ceiling":1e999999999999999999999,"minimum":1e999999999999999999999,"maximum":3e999999999999999999999,"multipleOf":2e999999999999999999998}`},
				{"https://numeric.test/root", `{"$ref":"https://numeric.test/target"}`},
			}
			for _, r := range resources {
				if err := c.AddResource(r.url, strings.NewReader(r.text)); err != nil {
					t.Fatal(err)
				}
			}
			s, err := c.Compile("https://numeric.test/root")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Validate(json.Number("2e999999999999999999999")); err != nil {
				t.Fatal(err)
			}
			for _, value := range []json.Number{"4e999999999999999999999", "5e999999999999999999998", "21e999999999999999999998"} {
				var ve *jsonschema.ValidationError
				if err := s.Validate(value); !errors.As(err, &ve) {
					t.Fatalf("reference value %s: got %T", value, err)
				}
			}
		})
	})
}
