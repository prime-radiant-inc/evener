// Modified by Evener: local import and structured extension-error assertions.
// See UPSTREAM.md for the pinned Apache-2.0 source.
package jsonschema_test

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/jsonschema"
)

func TestPowerOfExt(t *testing.T) {
	t.Run("invalidSchema", func(t *testing.T) {
		c := jsonschema.NewCompiler()
		c.RegisterExtension("powerOf", powerOfMeta, powerOfCompiler{})
		if err := c.AddResource("test.json", strings.NewReader(`{"powerOf": "hello"}`)); err != nil {
			t.Fatal(err)
		}
		_, err := c.Compile("test.json")
		if err == nil {
			t.Fatal("error expected")
		}
		t.Log(err)
	})
	t.Run("validSchema", func(t *testing.T) {
		c := jsonschema.NewCompiler()
		c.RegisterExtension("powerOf", powerOfMeta, powerOfCompiler{})
		if err := c.AddResource("test.json", strings.NewReader(`{"powerOf": 10}`)); err != nil {
			t.Fatal(err)
		}
		sch, err := c.Compile("test.json")
		if err != nil {
			t.Fatal(err)
		}
		t.Run("validInstance", func(t *testing.T) {
			if err := sch.Validate(100); err != nil {
				t.Fatal(err)
			}
		})
		t.Run("invalidInstance", func(t *testing.T) {
			if err := sch.Validate(111); err == nil {
				t.Fatal("validation must fail")
			} else {
				t.Logf("%#v", err)
				var validationErr *jsonschema.ValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("got %T, want *jsonschema.ValidationError", err)
				}
				if len(validationErr.Causes) != 1 {
					t.Fatalf("got %d causes, want 1", len(validationErr.Causes))
				}
				cause := validationErr.Causes[0]
				if cause.KeywordLocation != "/powerOf" || cause.InstanceLocation != "" || len(cause.Causes) != 0 {
					t.Fatalf("unexpected extension cause: %#v", cause)
				}
			}
		})
	})
}
