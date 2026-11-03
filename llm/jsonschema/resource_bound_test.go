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

// These are regression budgets for arithmetic in encoded size, not production
// admission limits or a universal complexity proof. Run includes compilation,
// Validate, identity and callback completion in its measured/contained region.
func TestNumericResourceBound(t *testing.T) {
	for _, digits := range []int{32, 1024} {
		for _, negative := range []bool{false, true} {
			t.Run(fmt.Sprintf("digits_%d_negative_%t", digits, negative), func(t *testing.T) {
				numerictest.Run(t, "compile-validate-identity", func(t *testing.T) {
					exponent := strings.Repeat("9", digits)
					previous := strings.Repeat("9", digits-1) + "8"
					if negative {
						exponent = "-" + exponent
						// E-1 for E = -(10^digits-1) is -10^digits.
						previous = "-1" + strings.Repeat("0", digits)
					}
					value := json.Number("2e" + exponent)
					s := numericSchema(t, fmt.Sprintf(`{"type":"number","minimum":1e%s,"maximum":3e%s}`, exponent, exponent))
					if err := s.Validate(value); err != nil {
						t.Fatal(err)
					}
					for _, tc := range []struct{ token, keyword string }{{"0", "/minimum"}, {"4e" + exponent, "/maximum"}} {
						err := s.Validate(json.Number(tc.token))
						var ve *jsonschema.ValidationError
						if !errors.As(err, &ve) || ve.InstanceLocation != "" || len(ve.Causes) != 1 || ve.Causes[0].KeywordLocation != tc.keyword || ve.Causes[0].InstanceLocation != "" {
							t.Fatalf("range error: %#v", err)
						}
					}
					integer := numericSchema(t, `{"type":"integer"}`)
					if got := integer.Validate(value) == nil; got == negative {
						t.Fatalf("integer=%t negative exponent=%t", got, negative)
					}
					zero := numericSchema(t, `{"type":"integer","const":0,"minimum":0,"maximum":0}`)
					if err := zero.Validate(json.Number("-0e" + exponent)); err != nil {
						t.Fatal(err)
					}

					// 5*10^E / (25*10^(E-1)) = 2, while 6 gives 12/5.
					divisibility := numericSchema(t, fmt.Sprintf(`{"multipleOf":25e%s}`, previous))
					if err := divisibility.Validate(json.Number("5e" + exponent)); err != nil {
						t.Fatal(err)
					}
					var multiple *jsonschema.ValidationError
					if err := divisibility.Validate(json.Number("6e" + exponent)); !errors.As(err, &multiple) || len(multiple.Causes) != 1 || multiple.Causes[0].KeywordLocation != "/multipleOf" {
						t.Fatalf("divisibility: %#v", err)
					}

					for _, keyword := range []string{"const", "enum"} {
						constant := fmt.Sprintf(`{"nested":[2e%s]}`, exponent)
						if keyword == "enum" {
							constant = "[" + constant + "]"
						}
						identity := numericSchema(t, fmt.Sprintf(`{%q:%s}`, keyword, constant))
						equivalent := map[string]any{"nested": []any{json.Number("20e" + previous)}}
						if err := identity.Validate(equivalent); err != nil {
							t.Fatal(err)
						}
						var ve *jsonschema.ValidationError
						if err := identity.Validate(map[string]any{"nested": []any{json.Number("3e" + exponent)}}); !errors.As(err, &ve) {
							t.Fatalf("identity rejection: %T", err)
						}
					}
					unique := numericSchema(t, `{"type":"array","uniqueItems":true}`)
					items := make([]any, 21)
					for i := range items {
						items[i] = map[string]any{"index": i, "nested": []any{value}}
					}
					if err := unique.Validate(items); err != nil {
						t.Fatal(err)
					}
					items[20] = map[string]any{"nested": []any{json.Number("20e" + previous)}, "index": 0}
					var duplicate *jsonschema.ValidationError
					if err := unique.Validate(items); !errors.As(err, &duplicate) || len(duplicate.Causes) != 1 || duplicate.Causes[0].KeywordLocation != "/uniqueItems" || duplicate.Causes[0].InstanceLocation != "" {
						t.Fatalf("duplicate: %#v", err)
					}
					t.Logf("verified range, integrality, zero, cancelling divisibility, const/enum and 21-item nested uniqueness, exponent digits=%d negative=%t", digits, negative)
				})
			})
		}
	}
	t.Run("custom_metaschema_rejects_above_ceiling", func(t *testing.T) {
		numerictest.Run(t, "custom-meta-maximum", func(t *testing.T) {
			c := jsonschema.NewCompiler()
			for _, r := range []struct{ uri, text string }{
				{"https://resource.test/meta", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"powerOf":{"type":"integer","exclusiveMinimum":0},"ceiling":{"type":"number","maximum":1e99999999999999999999999999999999}}}`},
				{"https://resource.test/valid", `{"$schema":"https://resource.test/meta","powerOf":10,"ceiling":1e99999999999999999999999999999999}`},
				{"https://resource.test/invalid", `{"$schema":"https://resource.test/meta","powerOf":10,"ceiling":2e99999999999999999999999999999999}`},
			} {
				if err := c.AddResource(r.uri, strings.NewReader(r.text)); err != nil {
					t.Fatal(err)
				}
			}
			meta, err := c.Compile("https://resource.test/meta")
			if err != nil {
				t.Fatal(err)
			}
			// Declared custom $schema selects the dialect but does not apply its
			// assertions by itself. RegisterExtension is the inherited compile-time
			// metaschema validation hook. Reuse the real example extension.
			c.RegisterExtension("powerOf", meta, powerOfCompiler{})
			s, err := c.Compile("https://resource.test/valid")
			if err != nil || s == nil {
				t.Fatalf("valid: %v", err)
			}
			if err := s.Validate(100); err != nil {
				t.Fatal(err)
			}
			var extensionError *jsonschema.ValidationError
			if err := s.Validate(111); !errors.As(err, &extensionError) || len(extensionError.Causes) != 1 || extensionError.Causes[0].KeywordLocation != "/powerOf" {
				t.Fatalf("extension did not validate: %#v", err)
			}
			_, err = c.Compile("https://resource.test/invalid")
			var se *jsonschema.SchemaError
			var ve *jsonschema.ValidationError
			if !errors.As(err, &se) || !errors.As(err, &ve) || se.SchemaURL != "https://resource.test/invalid" {
				t.Fatalf("custom metaschema error: %#v", err)
			}
			leaf := ve
			for len(leaf.Causes) == 1 {
				leaf = leaf.Causes[0]
			}
			if len(leaf.Causes) != 0 || leaf.KeywordLocation != "/properties/ceiling/maximum" || leaf.InstanceLocation != "/ceiling" || leaf.AbsoluteKeywordLocation != "https://resource.test/meta#/properties/ceiling/maximum" {
				t.Fatalf("metaschema location: %#v", leaf)
			}
			t.Log("custom metaschema accepted ceiling, rejected twice ceiling at /ceiling and /properties/ceiling/maximum")
		})
	})
}
