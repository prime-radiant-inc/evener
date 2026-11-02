package repair

import (
	"strings"
	"testing"
)

// wantMsg asserts msg contains want; desc names the expectation on failure.
func wantMsg(t *testing.T, msg, want, desc string) {
	t.Helper()
	if !strings.Contains(msg, want) {
		t.Fatalf("%s: %q", desc, msg)
	}
}

// notMsg asserts msg does not contain want; desc names the expectation on failure.
func notMsg(t *testing.T, msg, want, desc string) {
	t.Helper()
	if strings.Contains(msg, want) {
		t.Fatalf("%s: %q", desc, msg)
	}
}

// nestedEnumBranchParams nests a oneOf inside `opts` whose first branch
// narrows the enum on a property that the enclosing schema also declares with a
// broader enum. A branch failure must surface the conditional rule, not the
// enclosing schema's broader limit (issue #624 review, finding 1).
func nestedEnumBranchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string", "enum": []any{"x", "y"}},
				},
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"enum": []any{"x"}}},
					},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
}

// Finding 1: the deepest cause is an enum inside a oneOf branch, on opts.a. The
// enclosing opts.a schema allows "x" and "y", but the branch narrows it to "x".
// Reporting the enclosing enum would name values no branch accepts.
func TestExplainSchemaError_NestedBranchConstraintWinsOverEnclosingEnum(t *testing.T) {
	msg := ExplainSchemaError("my_tool", nestedEnumBranchParams(),
		map[string]any{"opts": map[string]any{"a": "y"}},
		"opts/a", "properties/opts/oneOf/0/properties/a/enum")
	notMsg(t, msg, `allowed values: "x", "y"`, "branch-narrowed enum reported from the enclosing broader enum")
	wantMsg(t, msg, "oneOf constraint", "must surface the conditional rule, not the enclosing enum")
	wantMsg(t, msg, `argument "opts"`, "must be scoped to the combinator's container (opts)")
	wantMsg(t, msg, `"a" must be one of "x"`, "must name the branch's narrowed enum")
}

// nestedDeepBranchParams nests a oneOf inside `opts` whose branch constrains a
// property (a) of the same object. The deepest cause is a keyword on opts.a,
// while the combinator lives on opts itself (issue #624 review, finding 3).
func nestedDeepBranchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "integer"},
				},
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"minimum": 5}},
					},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
}

// Finding 3: the failing keyword location is deeper (opts/oneOf/0/properties/a)
// than the combinator's own schema node (opts). The message must be scoped to
// the combinator's container path, not the leaf's, and the Example must be the
// container's real shape — never a single key literally named "opts.a".
func TestExplainSchemaError_NestedBranchFailureScopedToCombinatorContainer(t *testing.T) {
	msg := ExplainSchemaError("my_tool", nestedDeepBranchParams(),
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/properties/a/minimum")
	notMsg(t, msg, `"opts.a"`, "deep leaf path used as the combinator's container")
	wantMsg(t, msg, `argument "opts"`, "must be scoped to the combinator's container (opts)")
	wantMsg(t, msg, `Example: {"opts": {"a": 5}}`, "Example must be the container's structural shape with a branch-valid value")
}

// nestedItemOneOfParams nests a oneOf inside the items schema of an array
// property: properties.opts.items.oneOf.
func nestedItemOneOfParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			},
		},
	}
}

// Finding 2: a nested oneOf inside an array item's schema. The terminal array
// index isn't a present field (the required properties live only inside the
// branches), so without handling it the failure was reported as missing
// argument "opts/0".
func TestExplainSchemaError_NestedOneOfInArrayItem(t *testing.T) {
	msg := ExplainSchemaError("my_tool", nestedItemOneOfParams(),
		map[string]any{"opts": []any{map[string]any{"z": 1}}},
		"opts/0", "properties/opts/items/oneOf/0/required")
	notMsg(t, msg, "missing required argument", "array item reported as a missing argument (issue #624 review)")
	wantMsg(t, msg, "oneOf constraint", "must surface the conditional rule")
	wantMsg(t, msg, `argument "opts[0]"`, "must name the array item")
	// Finding 4: the Example must be a structural array of objects, not a
	// single key literally named "opts[0]".
	wantMsg(t, msg, `Example: {"opts": [{"x": "..."}]}`, "Example must render the array item structurally")
	notMsg(t, msg, `{"opts[0]"`, "Example quoted the display path as one key")
}

// nestedPointerParams names the combinator's property with a "/" (JSON Pointer
// escaped as ~1 in the locations).
func nestedPointerParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a/b": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
}

// Finding 5: jsonschema escapes "/" and "~" in property names as "~1"/"~0" in
// both locations. Decoding only the keyword path (or neither) would fail to
// resolve the property and fall back to the generic message.
func TestExplainSchemaError_NestedOneOfDecodesPointerEscapes(t *testing.T) {
	t.Run("slash", func(t *testing.T) {
		msg := ExplainSchemaError("my_tool", nestedPointerParams(),
			map[string]any{"a/b": map[string]any{"z": 1}},
			"a~1b", "properties/a~1b/oneOf/0/required")
		wantMsg(t, msg, "oneOf constraint", "slash property name not decoded")
		wantMsg(t, msg, `argument "a/b"`, "display path must decode ~1 to /")
	})
	t.Run("tilde", func(t *testing.T) {
		params := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a~b": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			},
		}
		msg := ExplainSchemaError("my_tool", params,
			map[string]any{"a~b": map[string]any{"z": 1}},
			"a~0b", "properties/a~0b/oneOf/0/required")
		wantMsg(t, msg, "oneOf constraint", "tilde property name not decoded")
		wantMsg(t, msg, `argument "a~b"`, "display path must decode ~0 to ~")
	})
}

// Finding 1: a nested oneOf matching more than one branch is the over-match
// shape (oneOf means exactly-one). Enumerating branches the arguments already
// satisfy coaches nothing and reintroduces the #623 misdirection; the message
// must name the over-match, give the recovery, and append no Example.
func TestExplainSchemaError_NestedOneOfOverMatch(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"a"}},
					map[string]any{"required": []any{"a"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": "x"}},
		"opts", "properties/opts/oneOf")
	if strings.Contains(msg, "Branch 0 requires") || strings.Contains(msg, "Branch 1 requires") {
		t.Fatalf("nested over-match rendered satisfied branch requirements: %q", msg)
	}
	wantMsg(t, msg, `argument "opts" matched more than one branch`, "message must name the nested over-match")
	wantMsg(t, msg, "satisfy exactly one", "message must give the recovery direction")
	notMsg(t, msg, "Example:", "over-match message must not append an example matching zero branches")
}

// A property-scoped oneOf that over-matches inside a root oneOf branch is still
// an over-match of the property combinator, so it must be named rather than
// enumerated — the #623 guard only covers the root-ancestor location
// "/oneOf/0/oneOf", where the *outer* combinator matched zero branches.
func TestExplainSchemaError_NestedOneOfOverMatchUnderRootBranch(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"properties": map[string]any{
				"opts": map[string]any{"oneOf": []any{
					map[string]any{"required": []any{"a"}},
					map[string]any{"required": []any{"a"}},
				}},
			}},
			map[string]any{"required": []any{"b"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": "x"}},
		"opts", "oneOf/0/properties/opts/oneOf")
	wantMsg(t, msg, `argument "opts" matched more than one branch`, "property-scoped over-match under a root branch not named")
	if strings.Contains(msg, "Branch 1 requires") || strings.Contains(msg, "Example:") {
		t.Fatalf("over-match must not enumerate branches or add an Example: %q", msg)
	}
}

// Finding 2: a property-level oneOf nested inside a root oneOf branch
// ("/oneOf/0/properties/opts/oneOf") must still be found; otherwise the failure
// falls back to the generic wrong-value message this change replaces.
func TestExplainSchemaError_NestedOneOfInsideRootBranch(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"properties": map[string]any{
				"opts": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			}},
			map[string]any{"required": []any{"b"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"z": 1}},
		"opts", "oneOf/0/properties/opts/oneOf/0/required")
	wantMsg(t, msg, "oneOf constraint", "nested oneOf inside a root branch not explained")
	for _, want := range []string{`argument "opts"`, `send all of "x"`, `send all of "y"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %q", want, msg)
		}
	}
}

// Finding 3: the Example must satisfy the branch's own value constraints. A
// branch enum narrowed below the holder's enum must be rendered with a value
// the branch accepts, not a generic placeholder.
func TestExplainSchemaError_NestedBranchExampleHonorsBranchEnum(t *testing.T) {
	msg := ExplainSchemaError("my_tool", nestedEnumBranchParams(),
		map[string]any{"opts": map[string]any{"a": "y"}},
		"opts/a", "properties/opts/oneOf/0/properties/a/enum")
	wantMsg(t, msg, `Example: {"opts": {"a": "x"}}`, "Example must use the branch's accepted enum value")
}

// A branch const is rendered as its own JSON literal.
func TestExplainSchemaError_NestedBranchExampleHonorsConst(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"const": 7}},
					},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/properties/a/const")
	wantMsg(t, msg, `Example: {"opts": {"a": 7}}`, "Example must use the branch's const value")
}

// When no required-list branch yields a value that satisfies its constraints,
// the Example is omitted rather than coaching a retry that fails validation.
func TestExplainSchemaError_NestedBranchExampleOmittedWhenUnbuildable(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"type": "string", "pattern": "^[0-9]+$"}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": "z"}},
		"opts", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, "oneOf constraint", "message must still explain the combinator")
	notMsg(t, msg, "Example:", "Example must be omitted when no branch value can be constructed")
}

// Finding 4: an object property named "0" is a key, not an array element — the
// display path and Example must keep it an object key.
func TestExplainSchemaError_NumericPropertyNameStaysObjectKey(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"0": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"0": map[string]any{"z": 1}},
		"0", "properties/0/oneOf/0/required")
	notMsg(t, msg, `argument "[0]"`, "numeric object property rendered as an array index")
	wantMsg(t, msg, `argument "0"`, "message must name the object key 0")
	wantMsg(t, msg, `Example: {"0": {"x": "..."}}`, "Example must render 0 as an object key")
}

// Second review: a const/enum candidate must also satisfy the property's other
// constraints. Here the branch's enum [1, 10] plus minimum 5 must not render 1
// (which violates the minimum) — 10 is the only accepted value.
func TestExplainSchemaError_EnumExampleHonorsSiblingNumericBound(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required": []any{"a"},
						"properties": map[string]any{"a": map[string]any{
							"type":    "integer",
							"enum":    []any{float64(1), float64(10)},
							"minimum": 5,
						}},
					},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/properties/a/enum")
	notMsg(t, msg, `Example: {"opts": {"a": 1}}`, "Example used an enum candidate that violates the branch minimum")
	wantMsg(t, msg, `Example: {"opts": {"a": 10}}`, "Example must use the enum candidate that satisfies the bound")
}

// When no enum/const candidate satisfies the sibling constraints, the Example
// is omitted rather than asserting a value the branch rejects.
func TestExplainSchemaError_EnumExampleOmittedWhenNoCandidateValid(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required": []any{"a"},
						"properties": map[string]any{"a": map[string]any{
							"type":    "integer",
							"enum":    []any{float64(1), float64(2)},
							"minimum": 5,
						}},
					},
					map[string]any{
						"required":   []any{"b"},
						"properties": map[string]any{"b": map[string]any{"type": "string", "pattern": "^x$"}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/properties/a/enum")
	notMsg(t, msg, "Example:", "Example must be omitted when no enum candidate satisfies the constraints")
}

// Second review: a fractional numeric minimum must be rendered faithfully, not
// truncated by schemaInt. type "number" with minimum 0.5 renders 0.5.
func TestExplainSchemaError_NumericExamplePreservesFractionalMinimum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required":   []any{"n"},
						"properties": map[string]any{"n": map[string]any{"type": "number", "minimum": 0.5}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1.0}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 0.5}}`, "fractional minimum truncated instead of preserved")
}

// An integer property with a fractional minimum takes the smallest satisfying
// integer (ceil), not the truncated floor.
func TestExplainSchemaError_IntegerExampleCeilsFractionalMinimum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required":   []any{"n"},
						"properties": map[string]any{"n": map[string]any{"type": "integer", "minimum": 0.5}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 1}}`, "fractional minimum for an integer must ceil to 1")
}

// Third review, finding 1: a candidate must satisfy BOTH schemas' enum sets.
// The holder's {enum:[1,3]} and the branch's {enum:[1,2,3], minimum:2} intersect
// at 3, so the Example must use 3 — not 2 (holder rejects it) and not 1
// (minimum rejects it).
func TestExplainSchemaError_ExampleUsesCrossSchemaEnumIntersection(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"enum": []any{float64(1), float64(3)}},
				},
				"oneOf": []any{
					map[string]any{
						"required": []any{"a"},
						"properties": map[string]any{"a": map[string]any{
							"enum":    []any{float64(1), float64(2), float64(3)},
							"minimum": 2,
						}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 2}},
		"opts/a", "properties/opts/oneOf/0/properties/a/enum")
	notMsg(t, msg, `Example: {"opts": {"a": 2}}`, "Example used a value the holder's enum rejects")
	wantMsg(t, msg, `Example: {"opts": {"a": 3}}`, "Example must use the cross-schema enum intersection value")
}

// A const from one schema that the other schema's enum rejects must not be
// emitted; with no satisfying candidate the Example is omitted.
func TestExplainSchemaError_ExampleOmitsConstRejectedByOtherEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"enum": []any{float64(1), float64(3)}},
				},
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"const": float64(5)}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/properties/a/const")
	notMsg(t, msg, "Example:", "Example must be omitted when the const violates the other schema's enum")
}

// Third review, finding 2: a multipleOf smaller than a fixed absolute epsilon
// must not be waved through. minimum 5e-10 with multipleOf 1e-9 snaps up to
// 1e-9 rather than emitting the off-grid bound 5e-10.
func TestExplainSchemaError_ExampleSnapsToSubToleranceMultiple(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n": map[string]any{"type": "number", "minimum": 5e-10, "multipleOf": 1e-9},
				},
				"oneOf": []any{map[string]any{"required": []any{"n"}}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1.0}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 0.000000001}}`, "Example must snap to a declared multiple, not the off-grid bound")
}

// Third review, finding 3: the Example must also carry the holder's own
// required properties, not just the branch's.
func TestExplainSchemaError_ExampleIncludesHolderRequiredProperties(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":     "object",
				"required": []any{"mode"},
				"properties": map[string]any{
					"mode": map[string]any{"type": "string"},
					"a":    map[string]any{"type": "integer"},
					"b":    map[string]any{"type": "integer"},
				},
				"oneOf": []any{
					map[string]any{"required": []any{"a"}},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"mode": "m", "a": 1}},
		"opts/a", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"a": 0, "mode": "..."}}`, "Example must include the holder's required property")
}

// Third review, finding 4: type "null" must render the null literal, not a
// string placeholder the schema rejects.
func TestExplainSchemaError_ExampleRendersNullType(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": "null"}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": nil}},
		"opts/n", "properties/opts/oneOf/0/properties/n/type")
	wantMsg(t, msg, `Example: {"opts": {"n": null}}`, "null type must render the null literal")
}

// A nullable union renders its non-null member's value.
func TestExplainSchemaError_ExampleRendersNullableUnion(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": []any{"integer", "null"}}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/properties/n/type")
	wantMsg(t, msg, `Example: {"opts": {"n": 0}}`, "nullable union must render the non-null member")
}

// A union with no single non-null member cannot be rendered faithfully, so the
// Example is omitted rather than emitting a wrong-typed placeholder.
func TestExplainSchemaError_ExampleOmittedForAmbiguousUnionType(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": []any{"integer", "number"}}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/properties/n/type")
	notMsg(t, msg, "Example:", "ambiguous union type must omit the Example")
}

// Fourth review, finding 1: a generated value must satisfy BOTH schemas'
// declared types. A holder string property with a branch integer property has
// no joint value, so the Example must be omitted.
func TestExplainSchemaError_ExampleOmittedOnHolderBranchTypeConflict(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "string"}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": "integer"}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/properties/n/type")
	notMsg(t, msg, "Example:", "holder/branch type conflict must omit the Example")
}

// When the holder requires an integer and the branch only requires a number
// with a fractional minimum, the generated value must be the integer, not the
// fractional bound.
func TestExplainSchemaError_ExamplePrefersIntegerWhenEitherSchemaRequiresIt(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "integer"}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": "number", "minimum": 0.5}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/properties/n/minimum")
	wantMsg(t, msg, `Example: {"opts": {"n": 1}}`, "integer-requiring holder must yield an integral Example")
}

// Fourth review, finding 2: an integer property with a fractional multipleOf
// must land on an integral multiple, not a snapped non-integer.
func TestExplainSchemaError_IntegerExampleStaysIntegralWithFractionalMultiple(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": "integer", "minimum": 1, "multipleOf": 0.7}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 7}}`, "integer multipleOf must yield an integral multiple")
	notMsg(t, msg, `Example: {"opts": {"n": 1.4}}`, "integer property rendered a non-integral value")
}

// Fourth review, finding 3: object-level constraints on the holder can make a
// required-only example invalid; here minProperties:2 with one required
// property must omit the Example.
func TestExplainSchemaError_ExampleOmittedWhenMinPropertiesUnmet(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":          "object",
				"minProperties": 2,
				"properties":    map[string]any{"a": map[string]any{"type": "integer"}},
				"oneOf":         []any{map[string]any{"required": []any{"a"}}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "minProperties-unmet example must be omitted")
}

// A branch-only required property under a holder that forbids additional
// properties cannot be rendered as a valid object, so the Example is omitted.
func TestExplainSchemaError_ExampleOmittedWhenAdditionalPropertiesForbid(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"oneOf": []any{map[string]any{
					"required":   []any{"extra"},
					"properties": map[string]any{"extra": map[string]any{"type": "integer"}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"extra": 1}},
		"opts/extra", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "additionalProperties-forbidden example must be omitted")
}

// Fifth review, finding 1: an object-level combinator this file cannot evaluate
// (allOf/not/...) must decline the Example rather than omit its required
// properties.
func TestExplainSchemaError_ExampleOmittedOnUnevaluableObjectCombinator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch map[string]any
	}{
		{
			name: "allOf",
			branch: map[string]any{
				"required": []any{"a"},
				"allOf":    []any{map[string]any{"required": []any{"z"}}},
			},
		},
		{
			name: "not",
			branch: map[string]any{
				"required": []any{"a"},
				"not":      map[string]any{"required": []any{"z"}},
			},
		},
		{
			name: "schema-valued additionalProperties",
			branch: map[string]any{
				"required":             []any{"a"},
				"additionalProperties": map[string]any{"type": "integer"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"opts": map[string]any{
						"type":       "object",
						"properties": map[string]any{"a": map[string]any{"type": "integer"}},
						"oneOf":      []any{tc.branch},
					},
				},
			}
			msg := ExplainSchemaError("my_tool", params,
				map[string]any{"opts": map[string]any{"a": 1}},
				"opts", "properties/opts/oneOf/0/required")
			notMsg(t, msg, "Example:", "unevaluable object combinator must omit the Example")
		})
	}
}

// Fifth review, finding 2: an object-valued required property with its own
// required list cannot be rendered as {}; the Example is omitted.
func TestExplainSchemaError_ExampleOmittedForNestedObjectRequirement(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"a": map[string]any{"type": "object", "required": []any{"x"}}},
				"oneOf":      []any{map[string]any{"required": []any{"a"}}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": map[string]any{}}},
		"opts/a", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "nested required object must omit the Example")
}

// Fifth review, finding 3: multipleOf requires exact divisibility; a near-miss
// enum candidate must not be accepted as a multiple.
func TestExplainSchemaError_ExampleRejectsNearMultipleCandidate(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n": map[string]any{"enum": []any{1.0000000005}},
				},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"enum": []any{1.0000000005}, "multipleOf": 1}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1.0000000005}},
		"opts/n", "properties/opts/oneOf/0/properties/n/multipleOf")
	notMsg(t, msg, "Example:", "a near-multiple candidate must not be emitted as an Example")
}

// Sixth review, finding 1: a magnitude-relative tolerance wrongly treats a
// large fractional value as integral; the validator's decimal rationals reject
// it, so the Example must be omitted.
func TestExplainSchemaError_ExampleRejectsLargeFractionalMultiple(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n": map[string]any{"enum": []any{1000000000000.5}},
				},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"enum": []any{1000000000000.5}, "multipleOf": 1}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1000000000000.5}},
		"opts/n", "properties/opts/oneOf/0/properties/n/multipleOf")
	notMsg(t, msg, "Example:", "large fractional multiple must not be emitted")
}

// Sixth review, finding 3: a structural example is an object, so a holder or
// branch typed as an array must not yield one.
func TestExplainSchemaError_ExampleOmittedWhenHolderTypeRejectsObject(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":  "array",
				"oneOf": []any{map[string]any{"type": "object", "required": []any{"a"}}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": []any{}},
		"opts", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "array-typed holder must not emit an object example")
}

// Sixth review, finding 4: unevaluatedProperties cannot be decided from a
// property-name list, so the Example is omitted.
func TestExplainSchemaError_ExampleOmittedOnUnevaluatedProperties(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":                  "object",
				"unevaluatedProperties": false,
				"properties":            map[string]any{"a": map[string]any{"type": "integer"}},
				"oneOf":                 []any{map[string]any{"required": []any{"a"}}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "unevaluatedProperties must omit the Example")
}

// Sixth review, finding 5: with only an upper bound, the multipleOf snap must go
// down (maximum 5, multipleOf 2 -> 4), not past the maximum.
func TestExplainSchemaError_UpperBoundOnlySnapsDownward(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": "integer", "maximum": 5, "multipleOf": 2}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 4}}`, "upper-bound-only multiple must snap down to 4")
}

// Seventh review, finding 1: an explicitly empty enum admits no value, so no
// Example (placeholder or numeric) may be emitted for that property.
func TestExplainSchemaError_ExampleOmittedForEmptyEnum(t *testing.T) {
	for _, tc := range []struct {
		name       string
		holderProp map[string]any
		branchProp map[string]any
	}{
		{
			name:       "branch empty enum",
			holderProp: map[string]any{"type": "string"},
			branchProp: map[string]any{"enum": []any{}},
		},
		{
			name:       "holder empty enum",
			holderProp: map[string]any{"enum": []any{}},
			branchProp: map[string]any{"type": "string"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"opts": map[string]any{
						"type":       "object",
						"properties": map[string]any{"n": tc.holderProp},
						"oneOf": []any{map[string]any{
							"required":   []any{"n"},
							"properties": map[string]any{"n": tc.branchProp},
						}},
					},
				},
			}
			msg := ExplainSchemaError("my_tool", params,
				map[string]any{"opts": map[string]any{"n": "v"}},
				"opts/n", "properties/opts/oneOf/0/required")
			notMsg(t, msg, "Example:", "empty enum must omit the Example")
		})
	}
}

// An empty enum on a sibling arm that is not the matched branch must not
// suppress the Example the satisfiable branch can provide.
func TestExplainSchemaError_EmptyEnumInSiblingArmDoesNotSuppressExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "string"},
					"b": map[string]any{"type": "string"},
				},
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"enum": []any{}}},
					},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"b": "v"}},
		"opts", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"b": "..."}}`, "a satisfiable sibling branch must still yield an Example")
}

// Seventh review, finding 2: an object-valued const/enum candidate must satisfy
// the other schema's object constraints (here required).
func TestExplainSchemaError_ObjectConstCandidateValidatedAgainstRequired(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "object", "required": []any{"x"}}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"const": map[string]any{}}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": map[string]any{}}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "object const candidate missing required keys must omit the Example")
}

// Eighth review, finding 1: a property schema written as the boolean false
// admits no value, so the Example must be omitted rather than treat it as
// unconstrained.
func TestExplainSchemaError_ExampleOmittedForFalsePropertySchema(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "string"}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": false},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": "v"}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "false property schema must omit the Example")
}

// A boolean true property schema is unconstrained and still yields an Example.
func TestExplainSchemaError_TruePropertySchemaStillYieldsExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": true},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": "v"}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": "..."}}`, "boolean true property schema must still yield an Example")
}

// Eighth review, finding 2: an array candidate is declined when the schema
// carries unevaluatedItems, which cannot be decided here.
func TestExplainSchemaError_ExampleRejectsArrayCandidateWithUnevaluatedItems(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"const":            []any{float64(1)},
						"unevaluatedItems": false,
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": []any{float64(1)}}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "unevaluatedItems array candidate must omit the Example")
}

// Eighth review, finding 3: the multipleOf snap must use exact decimal
// arithmetic, so minimum 0.3 with multipleOf 0.1 yields exactly 0.3.
func TestExplainSchemaError_SnappingIsDecimalExact(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"type": "number", "minimum": 0.3, "multipleOf": 0.1}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1.0}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 0.3}}`, "decimal-exact snapping must yield 0.3")
}

// Ninth review, finding 1: the Example must be a complete document, preserving
// the root schema's required fields alongside the nested combinator value.
func TestExplainSchemaError_ExampleIncludesRootRequiredFields(t *testing.T) {
	params := map[string]any{
		"type":     "object",
		"required": []any{"task", "opts"},
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"task": "t", "opts": map[string]any{"z": 1}},
		"opts", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"x": "..."}, "task": "..."}`, "Example must preserve the root's required fields")
}

// Ninth review, finding 2: a one-element array Example cannot satisfy an
// enclosing array that requires more items, so it is omitted.
func TestExplainSchemaError_ExampleOmittedWhenArrayCardinalityUnsatisfiable(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":     "array",
				"minItems": 2,
				"items": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": []any{map[string]any{"z": 1}, map[string]any{"z": 2}}},
		"opts/1", "properties/opts/items/oneOf/0/required")
	notMsg(t, msg, "Example:", "array-cardinality-unsatisfiable Example must be omitted")
}

// Tenth review, finding 1: a required root property must be filled with a value
// that satisfies its own constraints (const, bounds), not a bare placeholder.
func TestExplainSchemaError_RootRequiredFieldsUseConstraintAwareValues(t *testing.T) {
	params := map[string]any{
		"type":     "object",
		"required": []any{"task", "count", "opts"},
		"properties": map[string]any{
			"task":  map[string]any{"const": "run"},
			"count": map[string]any{"type": "integer", "minimum": 1},
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"task": "run", "count": 1, "opts": map[string]any{"z": 1}},
		"opts", "properties/opts/oneOf/0/required")
	for _, want := range []string{`"task": "run"`, `"count": 1`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("root required field must be constraint-satisfying (%s): %q", want, msg)
		}
	}
}

// Tenth review, finding 2: a required sibling on an INTERMEDIATE object ancestor
// must be filled too, not just the root's.
func TestExplainSchemaError_IntermediateAncestorRequiredSiblingFilled(t *testing.T) {
	params := map[string]any{
		"type":     "object",
		"required": []any{"request"},
		"properties": map[string]any{
			"request": map[string]any{
				"type":     "object",
				"required": []any{"task", "opts"},
				"properties": map[string]any{
					"task": map[string]any{"const": "run"},
					"opts": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"request": map[string]any{"task": "run", "opts": map[string]any{"z": 1}}},
		"request/opts", "properties/request/properties/opts/oneOf/0/required")
	wantMsg(t, msg, `"request": {"opts": {"x": "..."}, "task": "run"}`, "intermediate ancestor's required sibling must be filled")
}

// Tenth review, finding 3: a typed const (map[string]string) must be validated
// recursively against object constraints rather than bypassing them.
func TestExplainSchemaError_TypedConstCandidateValidatedRecursively(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"required": []any{"x"}}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"const": map[string]string{}}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": map[string]any{}}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "typed const violating required must omit the Example")
}

// The same typed const that DOES satisfy the constraints must still be emitted,
// which pins that canonicalization compares equal rather than suppressing every
// typed container (per-commit review of the canonicalization fix).
func TestExplainSchemaError_TypedConstCandidateAcceptedWhenValid(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"type": "object", "required": []any{"x"}}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"const": map[string]string{"x": "ok"}}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": map[string]any{"x": "ok"}}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": {"x":"ok"}}}`, "a valid typed const must still yield an Example")
}

// Eleventh review, finding 1: a typed container whose json.Marshal differs from
// its canonical reflected shape must not be validated as one JSON type and
// rendered as another. []byte marshals to a base64 string, a typed nil map to
// null; both are declined.
func TestExplainSchemaError_TypedContainerEncodingMismatchOmitted(t *testing.T) {
	for _, tc := range []struct {
		name       string
		typ        string
		constValue any
	}{
		{name: "byte slice", typ: "array", constValue: []byte{1}},
		{name: "typed nil map", typ: "object", constValue: map[string]string(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"opts": map[string]any{
						"type": "object",
						"oneOf": []any{map[string]any{
							"required": []any{"n"},
							"properties": map[string]any{"n": map[string]any{
								"const": tc.constValue,
								"type":  tc.typ,
							}},
						}},
					},
				},
			}
			msg := ExplainSchemaError("my_tool", params,
				map[string]any{"opts": map[string]any{"n": tc.constValue}},
				"opts/n", "properties/opts/oneOf/0/required")
			notMsg(t, msg, "Example:", "encoding-mismatched typed container must omit the Example")
		})
	}
}

// Eleventh review, finding 2: an ancestor object's own object-level constraints
// (here minProperties) must be honoured, not just its required list.
func TestExplainSchemaError_ExampleOmittedWhenAncestorMinPropertiesUnmet(t *testing.T) {
	params := map[string]any{
		"type":          "object",
		"minProperties": 2,
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"z": 1}},
		"opts", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "ancestor minProperties-unmet Example must be omitted")
}

// Eleventh review, finding 3: a nested oneOf inside a root oneOf branch must
// render an Example that also satisfies the enclosing root branch, including its
// required properties.
func TestExplainSchemaError_ExampleSatisfiesEnclosingRootOneOfBranch(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{
				"required": []any{"task", "opts"},
				"properties": map[string]any{
					"task": map[string]any{"const": "run"},
					"opts": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					},
				},
			},
			map[string]any{"required": []any{"alt"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"task": "run", "opts": map[string]any{"z": 1}},
		"opts", "oneOf/0/properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"x": "..."}, "task": "run"}`, "Example must satisfy the enclosing root oneOf branch")
}

// Eleventh review, finding 4: an integer candidate above 2^53 cannot be
// emit a value whose rendered form disagrees with the check.
func TestExplainSchemaError_LargeIntegerCandidateWithNumericConstraintOmitted(t *testing.T) {
	bigOdd := int64(1)<<53 + 1 // 9007199254740993, odd; float64 rounds to an even value
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"type":       "integer",
						"const":      bigOdd,
						"multipleOf": 2,
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": bigOdd}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "precision-losing integer candidate must omit the Example")
}

// Twelfth review, finding 1: when the root and the selected root oneOf branch
// both declare a property, the value must satisfy both (root type + branch
// const), not just the first declaration.
func TestExplainSchemaError_ExampleMergesRootAndBranchPropertyConstraints(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"task": map[string]any{"type": "string"}},
		"oneOf": []any{
			map[string]any{
				"required": []any{"task", "opts"},
				"properties": map[string]any{
					"task": map[string]any{"const": "run"},
					"opts": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					},
				},
			},
			map[string]any{"required": []any{"alt"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"task": "run", "opts": map[string]any{"z": 1}},
		"opts", "oneOf/0/properties/opts/oneOf/0/required")
	wantMsg(t, msg, `"task": "run"`, "Example must satisfy the branch's narrowing of the root property")
}

// Twelfth review, finding 2: a typed container nested inside an otherwise
// canonical map must also be declined when its JSON encoding diverges.
func TestExplainSchemaError_NestedTypedContainerEncodingMismatchOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"const": map[string]any{"data": []byte{1}},
						"type":  "object",
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": map[string]any{"data": []byte{1}}}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "nested encoding-mismatched container must omit the Example")
}

// Twelfth review, finding 3: completing the selected root oneOf branch must not
// produce a document that now matches a second root branch (an over-match).
func TestExplainSchemaError_ExampleOmittedWhenRepairOverMatchesRootOneOf(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{
				"required": []any{"task", "opts"},
				"properties": map[string]any{
					"task": map[string]any{"const": "run"},
					"opts": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					},
				},
			},
			map[string]any{
				"required":   []any{"task"},
				"properties": map[string]any{"opts": map[string]any{"type": "object", "required": []any{"x"}}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"task": "run", "opts": map[string]any{"z": 1}},
		"opts", "oneOf/0/properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "over-matching completed Example must be omitted")
}

// Thirteenth review, finding 1: a typed nil container marshals to null, so it
// must not canonicalize to an empty container and compare equal to one.
func TestExplainSchemaError_TypedNilContainerNotConflatedWithEmpty(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"const": map[string]any{"data": []any(nil)}}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"enum": []any{map[string]any{"data": []any{}}}}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": map[string]any{"data": []any{}}}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "typed nil container must not be conflated with empty")
}

// Thirteenth review, finding 2: the terminal property is constrained by every
// schema declaring it; when the root and the selected branch both declare it,
// the leaf built for the holder alone is declined rather than emitted.
func TestExplainSchemaError_ExampleOmittedWhenTerminalPropertyOverlaps(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"opts": map[string]any{"type": "object", "required": []any{"y"}}},
		"oneOf": []any{
			map[string]any{
				"required": []any{"task", "opts"},
				"properties": map[string]any{
					"task": map[string]any{"const": "run"},
					"opts": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					},
				},
			},
			map[string]any{"required": []any{"alt"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"task": "run", "opts": map[string]any{"z": 1}},
		"opts", "oneOf/0/properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "terminal property declared by root and branch must omit the Example")
}

// Thirteenth review, finding 3: an array-level enum/const cannot be satisfied by
// a synthesized one-element array, so the Example is declined.
func TestExplainSchemaError_ExampleOmittedForArrayLevelEnum(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "array",
				"enum": []any{[]any{}},
				"items": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": []any{map[string]any{"z": 1}}},
		"opts/0", "properties/opts/items/oneOf/0/required")
	notMsg(t, msg, "Example:", "array-level enum must omit the Example")
}

// Thirteenth review, finding 4: every Go numeric type must be validated; an
// int32 enum candidate with multipleOf 2 is not a multiple and is declined.
func TestExplainSchemaError_Int32CandidateWithMultipleOfValidated(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"enum": []any{int32(3)}, "multipleOf": 2}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": int32(3)}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "int32 candidate violating multipleOf must omit the Example")
}

// Fourteenth review: integer const/enum comparison must be exact, so a uint64
// const above 2^53 is not treated as equal to a nearby enum member by a lossy
// float64 round-trip.
func TestExplainSchemaError_LargeIntegerConstEnumComparedExactly(t *testing.T) {
	const big = uint64(1)<<53 + 1 // 9007199254740993, odd
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"const": big,
						"enum":  []any{uint64(1) << 53},
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": big}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "conflicting large integer const/enum must omit the Example")
}

// Fifteenth review: a property schema carrying if/then/else (or the other
// unevaluable keys) must decline the Example, since a placeholder can trigger
// the conditional and fail its consequence.
func TestExplainSchemaError_ExampleOmittedForConditionalPropertySchema(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"type": "string",
						"if":   map[string]any{"const": "..."},
						"then": map[string]any{"minLength": 4},
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": "..."}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "conditional property schema must omit the Example")
}

// Sixteenth review, finding 1: a mixed integer/float comparison must be exact.
// The holder enum member float64(2^53) and the branch const uint64(2^53+1) are
// distinct JSON numbers despite the float64 round-trip equating them.
func TestExplainSchemaError_MixedIntegerFloatComparedExactly(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":       "object",
				"properties": map[string]any{"n": map[string]any{"enum": []any{float64(1 << 53)}}},
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"const": uint64(1)<<53 + 1}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": uint64(1)<<53 + 1}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "mixed integer/float comparison must be exact")
}

// Sixteenth review, finding 2: a boolean property declaration is a constraint;
// a root declaring the path property false must block the Example even when the
// selected branch supplies a nested object schema.
func TestExplainSchemaError_BooleanPropertyDeclarationBlocksExample(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"opts": false},
		"oneOf": []any{
			map[string]any{"properties": map[string]any{
				"opts": map[string]any{
					"type": "object",
					"properties": map[string]any{"sub": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					}},
				},
			}},
			map[string]any{"required": []any{"alt"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"sub": map[string]any{"z": 1}}},
		"opts/sub", "oneOf/0/properties/opts/properties/sub/oneOf/0/required")
	notMsg(t, msg, "Example:", "root's false property declaration must block the Example")
}

// Sixteenth review, finding 3: a conditional (if/then) numeric property must
// decline the generated value rather than emit one the conditional rejects.
func TestExplainSchemaError_ConditionalNumericPropertyOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"type": "number",
						"if":   map[string]any{"maximum": 0},
						"then": map[string]any{"minimum": 5},
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 0}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "conditional numeric property must omit the Example")
}

// A conditional array candidate must be declined too.
func TestExplainSchemaError_ConditionalArrayCandidateOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"type":  "array",
						"const": []any{float64(1)},
						"if":    map[string]any{"minItems": 1},
						"then":  map[string]any{"minItems": 3},
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": []any{float64(1)}}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "conditional array candidate must omit the Example")
}

// A conditional on the enclosing array must decline the one-element example.
func TestExplainSchemaError_ConditionalArrayAncestorOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "array",
				"if":   map[string]any{"minItems": 1},
				"then": map[string]any{"minItems": 3},
				"items": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": []any{map[string]any{"z": 1}}},
		"opts/0", "properties/opts/items/oneOf/0/required")
	notMsg(t, msg, "Example:", "conditional enclosing array must omit the Example")
}

// Seventeenth review, Medium 1: a bound that does not survive the float64
// conversion (here a float32) must decline the Example rather than validate a
// rounded value.
func TestExplainSchemaError_InexactFloatBoundOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"type":    "number",
						"minimum": float32(0.1),
						"maximum": float32(0.1),
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 0.1}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "inexact float bound must omit the Example")
}

// Seventeenth review, Low: a bound beyond int64's exact range must not be
// converted for multipleOf snapping.
func TestExplainSchemaError_HugeBoundSnapOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required": []any{"n"},
					"properties": map[string]any{"n": map[string]any{
						"type":       "integer",
						"minimum":    1e20,
						"multipleOf": 2,
					}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1e20}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "a huge bound must omit the Example rather than snap")
}

// Seventeenth review, Medium 3: a oneOf nested behind an anyOf/allOf/not
// wrapper must still be found.
func TestExplainSchemaError_NestedOneOfBehindAnyOf(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"anyOf": []any{
					map[string]any{"properties": map[string]any{"sub": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					}}},
					map[string]any{"required": []any{"alt"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"sub": map[string]any{"z": 1}}},
		"opts/sub", "properties/opts/anyOf/0/properties/sub/oneOf/0/required")
	if !strings.Contains(msg, "oneOf constraint") || !strings.Contains(msg, `argument "opts.sub"`) {
		t.Fatalf("oneOf behind anyOf not explained scoped to its container: %q", msg)
	}
	for _, want := range []string{`send all of "x"`, `send all of "y"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %q", want, msg)
		}
	}
}

// Seventeenth review, Medium 4: the deepest oneOf scoped to a non-root path is
// the holder, so an inner combinator is explained rather than the outer one.
func TestExplainSchemaError_DeepestNestedOneOfIsHolder(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					}},
					map[string]any{"required": []any{"alt"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"z": 1}},
		"opts", "properties/opts/oneOf/0/oneOf/0/required")
	for _, want := range []string{`send all of "x"`, `send all of "y"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("deepest oneOf must be the holder; message missing %q: %q", want, msg)
		}
	}
	notMsg(t, msg, `send all of "alt"`, "message enumerated the outer oneOf's branches")
}

// Eighteenth review, Medium 1: a scalar oneOf (const/enum/type arms, no
// required) must explain its branch constraints rather than fall back to the
// generic wrong-value message.
func TestExplainSchemaError_ScalarOneOfBranchesExplained(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "string",
				"oneOf": []any{
					map[string]any{"const": "a"},
					map[string]any{"const": "b"},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": "c"},
		"opts", "properties/opts/oneOf/0/const")
	for _, want := range []string{"oneOf constraint", `must equal "a"`, `must equal "b"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("scalar oneOf message missing %q: %q", want, msg)
		}
	}
}

// Eighteenth review, Medium 2: a branch-internal leaf constraint the prose does
// not describe must be named, not denied.
func TestExplainSchemaError_NestedLeafConstraintNamed(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"a"}, "properties": map[string]any{"a": map[string]any{"type": "integer"}}},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": "not-an-int"}},
		"opts/a", "properties/opts/oneOf/0/properties/a/type")
	wantMsg(t, msg, `failing branch constraint is "type"`, "branch-internal leaf constraint not named")
	notMsg(t, msg, "not the argument's own type or value", "message denied the leaf constraint it should name")
}

// Eighteenth review, Medium 3: a root oneOf the path did not descend through
// must not be silently dropped from the Example.
func TestExplainSchemaError_ExampleOmittedWhenRootOneOfUntraversed(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
		"oneOf": []any{
			map[string]any{"required": []any{"task"}},
			map[string]any{"required": []any{"alt"}},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"z": 1}},
		"opts", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "Example ignoring an untraversed root oneOf must be omitted")
}

// Eighteenth review, Low: an exclusive bound still admits interior values, so
// the Example must be generated rather than omitted.
func TestExplainSchemaError_ExclusiveMinimumExampleGenerated(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":  "object",
				"oneOf": []any{map[string]any{"required": []any{"n"}}},
				"properties": map[string]any{
					"n": map[string]any{"type": "number", "exclusiveMinimum": 0},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": 1}}`, "exclusiveMinimum must still yield an interior Example")
}

// An exclusiveMaximum alone admits interior values below it.
func TestExplainSchemaError_ExclusiveMaximumExampleGenerated(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type":  "object",
				"oneOf": []any{map[string]any{"required": []any{"n"}}},
				"properties": map[string]any{
					"n": map[string]any{"type": "number", "exclusiveMaximum": 0},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": -1}},
		"opts/n", "properties/opts/oneOf/0/required")
	wantMsg(t, msg, `Example: {"opts": {"n": -1}}`, "exclusiveMaximum must still yield an interior Example")
}

// Eighteenth review, Medium: a const on a required property is not rendered by
// the branch prose, so the message must name the failing constraint rather than
// deny that any value is at fault.
func TestExplainSchemaError_ConstOnRequiredPropertyNamed(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{
						"required":   []any{"a"},
						"properties": map[string]any{"a": map[string]any{"const": 7}},
					},
					map[string]any{"required": []any{"b"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"a": 1}},
		"opts/a", "properties/opts/oneOf/0/properties/a/const")
	wantMsg(t, msg, `failing branch constraint is "const"`, "const on a required property not named")
	notMsg(t, msg, "not the argument's own type or value", "message denied the value constraint it should name")
}

// Nineteenth review, Medium 1: a walk that traversed an anyOf/allOf/not left
// constraints the scoped builder does not model, so the Example is omitted.
func TestExplainSchemaError_ExampleOmittedWhenApplicatorTraversed(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"anyOf": []any{
					map[string]any{"properties": map[string]any{"sub": map[string]any{
						"type": "object",
						"oneOf": []any{
							map[string]any{"required": []any{"x"}},
							map[string]any{"required": []any{"y"}},
						},
					}}},
					map[string]any{"required": []any{"alt"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"sub": map[string]any{"z": 1}}},
		"opts/sub", "properties/opts/anyOf/0/properties/sub/oneOf/0/required")
	wantMsg(t, msg, "oneOf constraint", "combinator should still be explained")
	notMsg(t, msg, "Example:", "Example after traversing an applicator must be omitted")
}

// Nineteenth review, Medium 2: a property schema constrained only by
// $dynamicRef (or $recursiveRef) must decline the Example, not assert a
// placeholder the referenced schema rejects.
func TestExplainSchemaError_DynamicRefPropertyOmitted(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{map[string]any{
					"required":   []any{"n"},
					"properties": map[string]any{"n": map[string]any{"$dynamicRef": "#intField"}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"n": 1}},
		"opts/n", "properties/opts/oneOf/0/required")
	notMsg(t, msg, "Example:", "a $dynamicRef-constrained property must omit the Example")
}

// Nineteenth review, Medium 1 (demonstration): a oneOf nested inside a `not`
// yields a value the enclosing `not` necessarily rejects, so the Example must
// be omitted.
func TestExplainSchemaError_ExampleOmittedWhenNotTraversed(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"not": map[string]any{"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				}},
			},
		},
	}
	msg := ExplainSchemaError("my_tool", params,
		map[string]any{"opts": map[string]any{"z": 1}},
		"opts", "properties/opts/not/oneOf/0/required")
	wantMsg(t, msg, "oneOf constraint", "combinator should still be explained")
	notMsg(t, msg, "Example:", "Example inside a traversed `not` must be omitted")
}
