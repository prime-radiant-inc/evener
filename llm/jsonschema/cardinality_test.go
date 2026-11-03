package jsonschema_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/jsonschema"
	"primeradiant.com/evener/llm/jsonschema/decimal"
)

var cardinalityDrafts = []struct {
	name                        string
	uri                         string
	contains, limits, evaluates bool
}{
	{"draft4", "http://json-schema.org/draft-04/schema#", false, false, false},
	{"draft6", "http://json-schema.org/draft-06/schema#", true, false, false},
	{"draft7", "http://json-schema.org/draft-07/schema#", true, false, false},
	{"draft2019", "https://json-schema.org/draft/2019-09/schema", true, true, false},
	{"draft2020", "https://json-schema.org/draft/2020-12/schema", true, true, true},
}

const cardinalityURI = "https://cardinality.test/schema"

func cardinalityFields(s *jsonschema.Schema) map[string]*decimal.Number {
	return map[string]*decimal.Number{
		"minProperties": s.MinProperties, "maxProperties": s.MaxProperties,
		"minItems": s.MinItems, "maxItems": s.MaxItems,
		"minLength": s.MinLength, "maxLength": s.MaxLength,
		"minContains": s.MinContains, "maxContains": s.MaxContains,
	}
}

func compileCardinality(t *testing.T, text string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource(cardinalityURI, strings.NewReader(text)); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(cardinalityURI)
	if err != nil {
		t.Fatal(err)
	}
	// Successful schemas remain cached and reusable, not consumed by validation.
	again, err := c.Compile(cardinalityURI)
	if err != nil || again != s {
		t.Fatalf("compiled reuse: same=%v err=%v", again == s, err)
	}
	return s
}

func cardinalityError(t *testing.T, err error, keyword, instance string) *jsonschema.ValidationError {
	t.Helper()
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %T, want ValidationError", err)
	}
	if ve.KeywordLocation != keyword || ve.InstanceLocation != instance || ve.AbsoluteKeywordLocation != cardinalityURI+"#"+keyword {
		t.Fatalf("locations: keyword=%q instance=%q absolute=%q", ve.KeywordLocation, ve.InstanceLocation, ve.AbsoluteKeywordLocation)
	}
	return ve
}

func TestExactCardinality(t *testing.T) {
	for _, draft := range cardinalityDrafts {
		t.Run(draft.name, func(t *testing.T) {
			for _, kind := range []struct {
				name, suffix string
				value        any
			}{
				{"Properties", "", map[string]any{"a": nil, "b": nil}},
				{"Items", "", []any{nil, nil}},
				{"Length", "", "é🙂"},
				{"Contains", `,"contains":{"type":"string"}`, []any{"a", "b"}},
			} {
				if kind.name == "Contains" && !draft.limits {
					continue
				}
				for _, prefix := range []string{"min", "max"} {
					keyword := prefix + kind.name
					t.Run(keyword, func(t *testing.T) {
						for _, tc := range []struct {
							threshold, canonical string
							minValid, maxValid   bool
						}{
							{"1e1000000000000000000000", "1e1000000000000000000000", false, true},
							{"18446744073709551616", "18446744073709551616", false, true},
							{"9007199254740993", "9007199254740993", false, true},
							{"1", "1", true, false},
							{"2.000", "2", true, true},
							{"3", "3", false, true},
							{"0", "0", true, false},
							{"-0e1000000000000000000000", "0", true, false},
						} {
							t.Run(tc.threshold, func(t *testing.T) {
								s := compileCardinality(t, fmt.Sprintf(`{"$schema":%q,%q:%s%s}`, draft.uri, keyword, tc.threshold, kind.suffix))
								field := cardinalityFields(s)[keyword]
								if field == nil || field.String() != tc.canonical {
									t.Fatalf("loaded %s=%v want=%s", keyword, field, tc.canonical)
								}
								wantValid := tc.minValid
								if prefix == "max" {
									wantValid = tc.maxValid
								}
								for repeat := 0; repeat < 2; repeat++ {
									err := s.Validate(kind.value)
									if got := err == nil; got != wantValid {
										t.Fatalf("compiled threshold=%s actual=2: valid=%v want=%v error=%T", tc.threshold, got, wantValid, err)
									}
									if !wantValid {
										root := cardinalityError(t, err, "", "")
										if len(root.Causes) != 1 {
											t.Fatalf("root causes=%d", len(root.Causes))
										}
										leaf := cardinalityError(t, root.Causes[0], "/"+keyword, "")
										if len(leaf.Causes) != 0 {
											t.Fatalf("leaf causes=%d", len(leaf.Causes))
										}
									}
								}
								if field.String() != tc.canonical {
									t.Fatal("validation mutated threshold")
								}
							})
						}
						for _, threshold := range []string{"-1", "-1e-1000000000000000000000", "0.5", "2.0000000000000000000001"} {
							t.Run("invalid_"+threshold, func(t *testing.T) {
								c := jsonschema.NewCompiler()
								if err := c.AddResource(cardinalityURI, strings.NewReader(fmt.Sprintf(`{"$schema":%q,%q:%s%s}`, draft.uri, keyword, threshold, kind.suffix))); err != nil {
									t.Fatal(err)
								}
								_, err := c.Compile(cardinalityURI)
								var se *jsonschema.SchemaError
								var ve *jsonschema.ValidationError
								if !errors.As(err, &se) || !errors.As(err, &ve) || se.SchemaURL != cardinalityURI {
									t.Fatalf("invalid threshold=%s compiled or lost SchemaError/ValidationError: %T", threshold, err)
								}
								for len(ve.Causes) != 0 {
									ve = ve.Causes[0]
								}
								if ve.InstanceLocation != "/"+keyword {
									t.Fatalf("invalid threshold instance=%q", ve.InstanceLocation)
								}
							})
						}
					})
				}
			}
		})
	}
}

func TestExactCardinalityDefaults(t *testing.T) {
	for _, draft := range cardinalityDrafts {
		t.Run(draft.name, func(t *testing.T) {
			t.Run("fields", func(t *testing.T) {
				absent := compileCardinality(t, fmt.Sprintf(`{"$schema":%q}`, draft.uri))
				for keyword, field := range cardinalityFields(absent) {
					if field != nil {
						t.Fatalf("absent %s=%v", keyword, field)
					}
				}
				zero := compileCardinality(t, fmt.Sprintf(`{"$schema":%q,"minProperties":0,"maxProperties":0,"minItems":0,"maxItems":0,"minLength":0,"maxLength":0,"minContains":0,"maxContains":0}`, draft.uri))
				for keyword, field := range cardinalityFields(zero) {
					if strings.HasSuffix(keyword, "Contains") && !draft.limits {
						if field != nil {
							t.Fatalf("inactive %s=%v", keyword, field)
						}
					} else if field == nil || field.String() != "0" {
						t.Fatalf("explicit zero %s=%v", keyword, field)
					}
				}
			})
			for _, tc := range []struct {
				name, body string
				value      any
				valid      bool
			}{
				{"absent_object", ``, map[string]any{}, true},
				{"absent_array", ``, []any{}, true},
				{"absent_string", ``, "", true},
				{"zero_object", `"minProperties":0,"maxProperties":0`, map[string]any{}, true},
				{"zero_array", `"minItems":0,"maxItems":0`, []any{}, true},
				{"zero_string", `"minLength":0,"maxLength":0`, "", true},
				{"zero_reject_object", `"maxProperties":0`, map[string]any{"a": nil}, false},
				{"zero_reject_array", `"maxItems":0`, []any{nil}, false},
				{"zero_reject_string", `"maxLength":0`, "🙂", false},
				{"empty_min_object", `"minProperties":1`, map[string]any{}, false},
				{"empty_min_array", `"minItems":1`, []any{}, false},
				{"empty_min_string", `"minLength":1`, "", false},
				{"contains_empty", `"contains":{"type":"integer"}`, []any{}, !draft.contains},
				{"contains_no_match", `"contains":{"type":"integer"}`, []any{"x"}, !draft.contains},
				{"contains_match", `"contains":{"type":"integer"}`, []any{1}, true},
				{"contains_zero_empty", `"contains":{"type":"integer"},"minContains":0`, []any{}, !draft.contains || draft.limits},
				{"contains_zero_no_match", `"contains":{"type":"integer"},"minContains":0`, []any{"x"}, !draft.contains || draft.limits},
				{"contains_zero_match", `"contains":{"type":"integer"},"minContains":0`, []any{1}, true},
				{"contains_max_zero", `"contains":{"type":"integer"},"minContains":0,"maxContains":0`, []any{1}, !draft.limits},
				{"contains_max_zero_empty", `"contains":{"type":"integer"},"minContains":0,"maxContains":0`, []any{}, !draft.contains || draft.limits},
				{"contains_max_default_min", `"contains":{"type":"integer"},"maxContains":0`, []any{}, !draft.contains},
				{"contains_min_ignored", `"contains":{"type":"integer"},"minContains":3`, []any{1}, !draft.limits},
				{"contains_absent", `"minContains":3,"maxContains":0`, []any{}, true},
				{"contains_absent_nonempty", `"minContains":3,"maxContains":0`, []any{1}, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := tc.body
					if body != "" {
						body = "," + body
					}
					s := compileCardinality(t, fmt.Sprintf(`{"$schema":%q%s}`, draft.uri, body))
					if got := s.Validate(tc.value) == nil; got != tc.valid {
						t.Fatalf("valid=%v want=%v", got, tc.valid)
					}
				})
			}
			if draft.limits {
				t.Run("vocabulary", func(t *testing.T) {
					cardinalityVocabulary(t, draft.uri)
				})
				t.Run("cause_order", func(t *testing.T) {
					s := compileCardinality(t, fmt.Sprintf(`{"$schema":%q,"properties":{"a":{"minItems":3,"maxItems":0,"contains":{"type":"integer"},"minContains":2,"maxContains":0}}}`, draft.uri))
					root := cardinalityError(t, s.Validate(map[string]any{"a": []any{1, "x"}}), "", "")
					if len(root.Causes) != 4 {
						t.Fatalf("root causes=%d", len(root.Causes))
					}
					for i, keyword := range []string{"minItems", "maxItems", "minContains", "maxContains"} {
						cause := cardinalityError(t, root.Causes[i], "/properties/a/"+keyword, "/a")
						if keyword == "minContains" {
							if len(cause.Causes) != 1 {
								t.Fatalf("minContains causes=%d", len(cause.Causes))
							}
							cardinalityError(t, cause.Causes[0], "/properties/a/contains/type", "/a/1")
						} else if len(cause.Causes) != 0 {
							t.Fatalf("%s causes=%d", keyword, len(cause.Causes))
						}
					}
				})
				for _, minimum := range []string{"", `,"minContains":0`} {
					for _, tc := range []struct {
						value []any
						valid bool
					}{
						{[]any{}, minimum != ""},
						{[]any{1}, draft.evaluates},
						{[]any{1, "x"}, false},
						{[]any{"x"}, false},
					} {
						s := compileCardinality(t, fmt.Sprintf(`{"$schema":%q,"contains":{"type":"integer"},"unevaluatedItems":false%s}`, draft.uri, minimum))
						if got := s.Validate(tc.value) == nil; got != tc.valid {
							t.Fatalf("evaluation min=%q value=%v valid=%v want=%v", minimum, tc.value, got, tc.valid)
						}
					}
				}
			}
			if draft.contains {
				t.Run("contains_causes", func(t *testing.T) {
					s := compileCardinality(t, fmt.Sprintf(`{"$schema":%q,"properties":{"a":{"contains":{"type":"integer"}}}}`, draft.uri))
					root := cardinalityError(t, s.Validate(map[string]any{"a": []any{"x", "y"}}), "", "")
					if len(root.Causes) != 1 {
						t.Fatalf("root causes=%d", len(root.Causes))
					}
					min := cardinalityError(t, root.Causes[0], "/properties/a/minContains", "/a")
					if len(min.Causes) != 2 {
						t.Fatalf("contains causes=%d", len(min.Causes))
					}
					for i, cause := range min.Causes {
						leaf := cardinalityError(t, cause, "/properties/a/contains/type", fmt.Sprintf("/a/%d", i))
						if len(leaf.Causes) != 0 {
							t.Fatalf("type causes=%d", len(leaf.Causes))
						}
					}
				})
			}
		})
	}
}

func cardinalityVocabulary(t *testing.T, draftURI string) {
	t.Helper()
	for _, validation := range []bool{false, true} {
		t.Run(fmt.Sprintf("validation_%v", validation), func(t *testing.T) {
			c := jsonschema.NewCompiler()
			vocab := strings.TrimSuffix(draftURI, "/schema") + "/vocab/"
			metaURI := "https://cardinality.test/meta"
			meta := fmt.Sprintf(`{"$schema":%q,"$id":%q,"$vocabulary":{%q:true,%q:%v,%q:%v}}`, draftURI, metaURI, vocab+"core", vocab+"validation", validation, vocab+"applicator", !validation)
			if err := c.AddResource(metaURI, strings.NewReader(meta)); err != nil {
				t.Fatal(err)
			}
			text := fmt.Sprintf(`{"$schema":%q,"maxProperties":0,"maxItems":0,"maxLength":0,"contains":{"type":"integer"},"minContains":0,"maxContains":0}`, metaURI)
			if err := c.AddResource(cardinalityURI, strings.NewReader(text)); err != nil {
				t.Fatal(err)
			}
			s, err := c.Compile(cardinalityURI)
			if err != nil {
				t.Fatal(err)
			}
			for keyword, field := range cardinalityFields(s) {
				wantPresent := validation && !strings.HasPrefix(keyword, "min")
				if keyword == "minContains" {
					wantPresent = validation
				}
				if (field != nil) != wantPresent {
					t.Fatalf("%s present=%v want=%v", keyword, field != nil, wantPresent)
				}
			}
			if (s.Contains != nil) == validation {
				t.Fatalf("contains present=%v", s.Contains != nil)
			}
			for _, tc := range []struct {
				value any
				valid bool
			}{
				{map[string]any{"a": nil}, !validation},
				{"é🙂", !validation},
				{[]any{1}, !validation},
				{[]any{}, validation},
			} {
				if got := s.Validate(tc.value) == nil; got != tc.valid {
					t.Fatalf("validation vocabulary=%v value=%v valid=%v want=%v", validation, tc.value, got, tc.valid)
				}
			}
		})
	}
}
