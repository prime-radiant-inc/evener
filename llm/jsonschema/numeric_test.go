package jsonschema_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"

	"primeradiant.com/evener/llm/internal/numerictest"
	"primeradiant.com/evener/llm/jsonschema"
)

func numericSchema(t *testing.T, text string) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.CompileString("https://numeric.test/schema", text)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type namedNumeric int

func TestNumericGoAdmission(t *testing.T) {
	t.Run("small", func(t *testing.T) {
		s := numericSchema(t, `{"type":"number","minimum":0,"maximum":1}`)
		for _, v := range []any{json.Number("1"), float32(1), float64(1), int(1), int8(1), int32(1), int64(1), uint(1), uint8(1), uint32(1), uint64(1)} {
			if err := s.Validate(v); err != nil {
				t.Fatalf("%T: %v", v, err)
			}
		}
		for _, v := range []any{int16(1), uint16(1), uintptr(1), namedNumeric(1)} {
			var invalid jsonschema.InvalidJSONTypeError
			if err := s.Validate(v); !errors.As(err, &invalid) {
				t.Fatalf("%T: got %T", v, err)
			}
		}
		for _, v := range []any{float32(.1), float64(.1), float32(1.25), float64(1.25)} {
			exact := numericSchema(t, fmt.Sprintf(`{"minimum":%s,"maximum":%s}`, fmt.Sprint(v), fmt.Sprint(v)))
			if err := exact.Validate(v); err != nil {
				t.Fatalf("float rendering %T: %v", v, err)
			}
		}
		// Characterize the old implementation without unsafe exponent arithmetic.
		// Numeric type labels alone never prove finite/malformed admission behavior.
		for _, v := range []any{math.NaN(), math.Inf(1), math.Inf(-1), float32(math.Inf(1)), json.Number("bad"), json.Number("1/2"), json.Number("01")} {
			for _, schema := range []string{`{"type":"number"}`, `{"type":"integer"}`, `{"minimum":0}`} {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Logf("observation value=%T(%v) schema=%s panic=%T", v, v, schema, r)
						}
					}()
					err := numericSchema(t, schema).Validate(v)
					t.Logf("observation value=%T(%v) schema=%s error=%T", v, v, schema, err)
				}()
			}
		}
	})
	t.Run("invalid", func(t *testing.T) {
		for _, v := range []any{math.NaN(), math.Inf(1), math.Inf(-1), float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), json.Number("bad"), json.Number("01"), json.Number("1/2"), json.Number(""), json.Number("+1")} {
			for _, text := range []string{`{"type":"number"}`, `{"type":"integer"}`, `{"minimum":0}`, `{"maximum":1}`, `{"multipleOf":1}`, `{}`} {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("invalid value %T(%v) schema=%s panicked: %T", v, v, text, r)
						}
					}()
					var invalid jsonschema.InvalidJSONTypeError
					if err := numericSchema(t, text).Validate(v); !errors.As(err, &invalid) {
						t.Errorf("invalid value %T(%v) schema=%s: got %T, want InvalidJSONTypeError", v, v, text, err)
					}
				}()
			}
		}
	})
}

func TestNumericBoundsExtreme(t *testing.T) {
	cases := []struct {
		schema string
		value  json.Number
		valid  bool
	}{
		{`{"minimum":0,"maximum":1}`, "1e-1000000000", true},
		{`{"type":"integer"}`, "1e-1000000000", false},
		{`{"type":"integer"}`, "-0e999999999999999999999", true},
		{`{"multipleOf":1}`, "1e-1000000000", false},
		{`{"minimum":0}`, "-1e-1000000000", false},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			numerictest.Run(t, "bounds", func(t *testing.T) {
				s := numericSchema(t, tc.schema)
				if got := s.Validate(tc.value) == nil; got != tc.valid {
					t.Fatal(got, tc.valid)
				}
			})
		})
	}
	t.Run("structured", func(t *testing.T) {
		s := numericSchema(t, `{"properties":{"n":{"minimum":2,"maximum":0,"multipleOf":2}}}`)
		err := s.Validate(map[string]any{"n": json.Number("1")})
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("got %T", err)
		}
		if ve.KeywordLocation != "" || ve.InstanceLocation != "" || len(ve.Causes) != 3 {
			t.Fatalf("root: %#v", ve)
		}
		for i, k := range []string{"minimum", "maximum", "multipleOf"} {
			c := ve.Causes[i]
			if c.KeywordLocation != "/properties/n/"+k || c.InstanceLocation != "/n" || c.AbsoluteKeywordLocation != "https://numeric.test/schema#/properties/n/"+k || len(c.Causes) != 0 {
				t.Fatalf("cause %d: %#v", i, c)
			}
		}
	})
}
