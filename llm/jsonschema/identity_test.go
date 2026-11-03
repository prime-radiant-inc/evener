package jsonschema

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"math"
	"testing"

	"primeradiant.com/evener/llm/internal/numerictest"
)

func identitySchema(t *testing.T, text string) *Schema {
	t.Helper()
	s, err := CompileString("https://identity.test/schema", text)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func identityHashes(a, b any) (uint64, uint64) {
	var h1, h2 maphash.Hash
	seed := maphash.MakeSeed()
	h1.SetSeed(seed)
	h2.SetSeed(seed)
	hash(a, &h1)
	hash(b, &h2)
	return h1.Sum64(), h2.Sum64()
}

func identityError(t *testing.T, err error, instance string, keywords ...string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %T, want *ValidationError", err)
	}
	if ve.KeywordLocation != "" || ve.InstanceLocation != "" || ve.AbsoluteKeywordLocation != "https://identity.test/schema#" || len(ve.Causes) != len(keywords) {
		t.Fatalf("root locations/count: %#v", ve)
	}
	for i, keyword := range keywords {
		c := ve.Causes[i]
		if c.KeywordLocation != keyword || c.InstanceLocation != instance || c.AbsoluteKeywordLocation != "https://identity.test/schema#"+keyword || len(c.Causes) != 0 {
			t.Fatalf("cause %d locations/count: %#v", i, c)
		}
	}
}

func TestNumericTreeIdentity(t *testing.T) {
	t.Run("operand-examination", func(t *testing.T) {
		for _, tc := range []struct {
			a, b any
			want InvalidJSONTypeError
		}{
			{"different type", json.Number("01"), InvalidJSONTypeError("json.Number")},
			{json.Number("01"), "different type", InvalidJSONTypeError("json.Number")},
			{int16(1), json.Number("01"), InvalidJSONTypeError("int16")},
		} {
			func() {
				defer func() {
					got, ok := recover().(InvalidJSONTypeError)
					if !ok || got != tc.want {
						t.Errorf("operands %T/%T: got %T(%v), want invalid Go type %v", tc.a, tc.b, got, got, tc.want)
					}
				}()
				equals(tc.a, tc.b)
			}()
		}
	})
	for _, operation := range []string{"equals", "hash", "const", "enum"} {
		t.Run("extreme-"+operation, func(t *testing.T) {
			numerictest.Run(t, operation, func(t *testing.T) {
				a := map[string]any{"a": []any{json.Number("1e1000000000000000000000")}, "b": 0}
				b := map[string]any{"b": json.Number("-0e999999999999999999999"), "a": []any{json.Number("10e999999999999999999999")}}
				t.Logf("entering %s witness with nested huge exponents", operation)
				switch operation {
				case "equals":
					if !equals(a, b) {
						t.Fatal("equivalent nested identities differ")
					}
				case "hash":
					if h1, h2 := identityHashes(a, b); h1 != h2 {
						t.Fatal("unequal hashes for equal values")
					}
				default:
					value := `{"a":[1e1000000000000000000000],"b":0}`
					if operation == "enum" {
						value = "[" + value + "]"
					}
					s := identitySchema(t, fmt.Sprintf(`{"%s":%s}`, operation, value))
					if err := s.Validate(b); err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
	t.Run("small", func(t *testing.T) {
		for _, v := range []any{json.Number("1.00"), float32(1), float64(1), int(1), int8(1), int32(1), int64(1), uint(1), uint8(1), uint32(1), uint64(1)} {
			a := map[string]any{"a": []any{v}, "b": nil}
			b := map[string]any{"b": nil, "a": []any{json.Number("10e-1")}}
			if !equals(a, b) || !equals(b, a) {
				t.Fatalf("numeric kind %T differs", v)
			}
			if h1, h2 := identityHashes(a, b); h1 != h2 {
				t.Fatalf("numeric kind %T hashes differently", v)
			}
			for _, text := range []string{`{"const":{"a":[1],"b":null}}`, `{"enum":[{"a":[1],"b":null}]}`} {
				if err := identitySchema(t, text).Validate(a); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, v := range []any{float32(0.1), float64(0.1)} {
			if !equals(v, json.Number("0.1")) {
				t.Fatalf("fmt.Sprint interpretation differs for %T", v)
			}
			if h1, h2 := identityHashes(v, json.Number("0.1")); h1 != h2 {
				t.Fatalf("fmt.Sprint hash differs for %T", v)
			}
		}
		pairs := [][2]any{
			{[]any{"ab", "c"}, []any{"a", "bc"}},
			{[]any{"a\x03b", "c"}, []any{"a", "b\x03c"}},
			{[]any{[]any{}, []any{}}, []any{[]any{[]any{}}}},
			{json.Number("-1"), json.Number("1")},
			{map[string]any{"a": nil}, map[string]any{"b": nil}},
			{[]any{1, 2}, []any{2, 1}},
			{nil, false}, {false, 0}, {"1", 1},
		}
		for i, pair := range pairs {
			if equals(pair[0], pair[1]) || equals(pair[1], pair[0]) {
				t.Errorf("distinct pair %d compares equal", i)
			}
			if h1, h2 := identityHashes(pair[0], pair[1]); h1 == h2 {
				t.Errorf("distinct framed pair %d hashes identically", i)
			}
		}
		for _, keyword := range []string{"const", "enum"} {
			text := `{"const":[1,2]}`
			if keyword == "enum" {
				text = `{"enum":[[1,2]]}`
			}
			identityError(t, identitySchema(t, text).Validate([]any{2, 1}), "", "/"+keyword)
		}
	})
	t.Run("tiny", func(t *testing.T) {
		numerictest.Run(t, "tiny-equals", func(t *testing.T) {
			tiny := json.Number("1e-1000000000")
			t.Log("entering equals with tiny nonzero versus zero")
			if equals(tiny, 0) {
				t.Fatal("tiny nonzero equals zero")
			}
			if h1, h2 := identityHashes(tiny, 0); h1 == h2 {
				t.Fatal("tiny nonzero hashes as zero")
			}
			identityError(t, identitySchema(t, `{"const":0}`).Validate(tiny), "", "/const")
			identityError(t, identitySchema(t, `{"enum":[0]}`).Validate(tiny), "", "/enum")
		})
	})
}

func TestUniqueItemsPaths(t *testing.T) {
	for _, n := range []int{2, 20, 21} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			numerictest.Run(t, "unique-tiny", func(t *testing.T) {
				s := identitySchema(t, `{"uniqueItems":true}`)
				items := make([]any, n)
				items[0] = 0
				for i := 1; i < n; i++ {
					items[i] = json.Number(fmt.Sprintf("%de-1000000000", i))
				}
				t.Logf("entering Schema.Validate uniqueItems with length=%d tiny distinct values", n)
				if err := s.Validate(items); err != nil {
					t.Fatal(err)
				}
				if n == 2 {
					items[0] = items[1]
				}
				items[n-1] = json.Number("10e-1000000001")
				identityError(t, s.Validate(items), "", "/uniqueItems")
				items[0] = 0
				items[n-1] = json.Number("-0e999999999999999999999")
				identityError(t, s.Validate(items), "", "/uniqueItems")
				items[0] = map[string]any{"a": []any{json.Number("1e1000000000000000000000")}, "b": 0}
				items[n-1] = map[string]any{"b": json.Number("-0e999999999999999999999"), "a": []any{json.Number("10e999999999999999999999")}}
				identityError(t, s.Validate(items), "", "/uniqueItems")
			})
		})
	}
	t.Run("locations-and-order", func(t *testing.T) {
		s := identitySchema(t, `{"properties":{"values":{"minItems":22,"uniqueItems":true}}}`)
		for _, n := range []int{2, 20, 21} {
			items := make([]any, n)
			for i := range items {
				items[i] = i
			}
			items[n-1] = 0
			identityError(t, s.Validate(map[string]any{"values": items}), "/values", "/properties/values/minItems", "/properties/values/uniqueItems")
		}
	})
	t.Run("invalid-examined-numbers", func(t *testing.T) {
		for _, n := range []int{2, 20, 21} {
			for i, invalid := range []any{math.NaN(), math.Inf(1), math.Inf(-1), float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), json.Number("01"), json.Number("1/2"), json.Number("bad"), json.Number("")} {
				t.Run(fmt.Sprintf("%d/%d", n, i), func(t *testing.T) {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("examined %T panicked: %T", invalid, r)
						}
					}()
					s := identitySchema(t, `{"uniqueItems":true}`)
					items := make([]any, n)
					for j := range items {
						items[j] = j + 1
					}
					// Nested numeric leaves reach equality or raw hashing, not type validation.
					for j := range items {
						items[j] = map[string]any{"n": items[j]}
					}
					items[0] = map[string]any{"n": invalid}
					var got InvalidJSONTypeError
					if err := s.Validate(items); !errors.As(err, &got) {
						t.Errorf("examined %T: got %T, want InvalidJSONTypeError", invalid, err)
					}
				})
			}
		}
		// Identity must not introduce an eager whole-tree admission boundary.
		if err := identitySchema(t, `{"uniqueItems":true}`).Validate([]any{map[string]any{"n": math.NaN()}}); err != nil {
			t.Fatal(err)
		}
		if err := identitySchema(t, `{}`).Validate(map[string]any{"n": math.NaN()}); err != nil {
			t.Fatal(err)
		}
		if err := identitySchema(t, `{"uniqueItems":true}`).Validate([]any{map[string]any{"a": math.NaN()}, map[string]any{"b": 0}}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUniqueItemsCollisionResolution(t *testing.T) {
	t.Run("shared-helper", func(t *testing.T) {
		numerictest.Run(t, "collision", func(t *testing.T) {
			for _, n := range []int{0, 1, 2, 20, 21} {
				items := make([]any, n)
				for i := range items {
					items[i] = map[string]any{"a": []any{i}, "b": 0}
				}
				if n == 21 {
					items[7] = map[string]any{"a": []any{json.Number("1e1000000000000000000000")}, "b": 0}
				}
				calls := 0
				constantHash := func(any) uint64 { calls++; return 0 }
				first, second := duplicateItems(items, constantHash)
				wantCalls := 0
				if n > 20 {
					wantCalls = n
				}
				if first != -1 || second != -1 || calls != wantCalls {
					t.Fatalf("distinct length %d: pair=(%d,%d) calls=%d, want (-1,-1) calls=%d", n, first, second, calls, wantCalls)
				}
				t.Logf("distinct length=%d constant hash calls=%d pair=(%d,%d)", n, calls, first, second)
				if n == 21 {
					items[20] = map[string]any{"b": json.Number("-0e999999999999999999999"), "a": []any{json.Number("10e999999999999999999999")}}
					calls = 0
					first, second = duplicateItems(items, constantHash)
					if first != 7 || second != 20 || calls != 21 {
						t.Fatalf("equivalent nested final item: pair=(%d,%d) calls=%d", first, second, calls)
					}
					t.Logf("nested equivalent length=21 constant hash calls=%d pair=(%d,%d)", calls, first, second)
				}
			}
		})
	})
	t.Run("earliest-second-then-first", func(t *testing.T) {
		for _, n := range []int{20, 21} {
			items := make([]any, n)
			for i := range items {
				items[i] = i
			}
			// (3,5) must win over the later (0,6), not the reverse.
			items[5], items[6] = json.Number("30e-1"), json.Number("-0")
			calls := 0
			first, second := duplicateItems(items, func(any) uint64 { calls++; return 0 })
			wantCalls := 0
			if n > 20 {
				wantCalls = 6
			}
			if first != 3 || second != 5 || calls != wantCalls {
				t.Fatalf("length %d: pair=(%d,%d) calls=%d, want (3,5) calls=%d", n, first, second, calls, wantCalls)
			}
			t.Logf("early duplicate length=%d constant hash calls=%d pair=(%d,%d)", n, calls, first, second)
			identityError(t, identitySchema(t, `{"uniqueItems":true}`).Validate(items), "", "/uniqueItems")
		}
	})
	// These values collided in the inherited hash encoding. They remain distinct.
	s := identitySchema(t, `{"uniqueItems":true}`)
	items := []any{[]any{[]any{}, []any{}}, []any{[]any{[]any{}}}, -1, 1}
	for len(items) < 21 {
		items = append(items, fmt.Sprint(len(items)))
	}
	if err := s.Validate(items); err != nil {
		t.Fatal(err)
	}
	items[20] = []any{[]any{}, []any{}}
	identityError(t, s.Validate(items), "", "/uniqueItems")
}
