package repair

import (
	"testing"
)

// managerDelegateAnyOfParams mirrors the Manager-mode delegate schema the
// session composes for the hub branch: a root anyOf whose ordinary arm forbids
// `hub` and whose hub arm requires it while accepting a closed argument set.
// A call carrying hub plus an argument outside that set fails both arms, and
// the deepest cause is the ordinary arm's `not` at the root instance — the
// shape whose explanation used to claim the supplied prompt argument was
// missing.
func managerDelegateAnyOfParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"prompt":           map[string]any{"type": "string"},
			"hub":              map[string]any{"type": "object"},
			"name":             map[string]any{"type": "string"},
			"reasoning_effort": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
		},
		"required": []string{"prompt"},
		"anyOf": []any{
			// The not keeps its explicit "type":"object" for the provider's
			// explicit-type contract; the arm's own object gate makes that
			// type non-discriminating, so the prohibition still renders.
			map[string]any{"type": "object", "not": map[string]any{"type": "object", "required": []string{"hub"}}},
			map[string]any{
				"type": "object", "required": []string{"hub"}, "additionalProperties": false,
				"properties": map[string]any{
					"hub":              map[string]any{"type": "object"},
					"prompt":           map[string]any{"type": "string"},
					"reasoning_effort": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
					"intent":           map[string]any{"type": "string"},
				},
			},
		},
	}
}

// An anyOf-branch failure carries no resolvable instance location, so the
// explainer used to fall back to the generic mismatch with the top-level
// required list — naming a prompt the call already supplied as the thing to
// send. The explanation must instead render the branch rules: the ordinary
// arm's prohibition and the hub arm's accepted-argument set, which is what
// makes the extra argument identifiable as the rejected one.
func TestExplainSchemaError_AnyOfBranchFailureDoesNotNameSuppliedRequired(t *testing.T) {
	args := map[string]any{
		"prompt":           "worker task",
		"hub":              map[string]any{"cwd": "/repo"},
		"name":             "worker-lane",
		"reasoning_effort": "high",
	}
	msg := ExplainSchemaError("delegate", managerDelegateAnyOfParams(), args, "", "/anyOf/0/not")
	notMsg(t, msg, "Required arguments: prompt", "anyOf failure misattributed to the supplied prompt argument")
	wantMsg(t, msg, "anyOf constraint", "message must name the anyOf constraint")
	wantMsg(t, msg, "Branch 0 requires", "message must render the ordinary arm's rule")
	wantMsg(t, msg, "Branch 1 requires", "message must render the hub arm's rule")
	wantMsg(t, msg, "reasoning_effort", "hub arm must name its accepted-argument set")
	wantMsg(t, msg, "intent", "hub arm's accepted set includes the shared intent parameter")
}

// The hub arm's accepted-argument set renders for any closed arm, naming the
// arguments that branch alone accepts — zz_marker is declared only on the arm,
// never required, so its presence pins the closed-set rendering rather than
// the required list.
func TestExplainSchemaError_ArmAcceptedSetRendersForClosedArms(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"type": "object", "not": map[string]any{"type": "object", "required": []string{"hub"}}},
			map[string]any{
				"type": "object", "required": []string{"hub"}, "additionalProperties": false,
				"properties": map[string]any{
					"hub":       map[string]any{"type": "object"},
					"zz_marker": map[string]any{"type": "string"},
					"zz_second": map[string]any{"type": "string"},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"hub": map[string]any{}, "name": "x"}, "", "/anyOf/0/not")
	wantMsg(t, msg, "zz_marker", "closed arm must name its accepted arguments")
	wantMsg(t, msg, "zz_second", "closed arm must name every accepted argument")
}

// Only branch-structural anyOf locations are attributed to the combinator: an
// arm-internal leaf keeps its leaf keyword (the #622 rule for a root anyOf), so
// its enum still renders through the present-field path instead of being
// blanked by branch attribution.
func TestExplainSchemaError_AnyOfLeafKeepsLeafKeyword(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"hub": map[string]any{"type": "object"}, "sandbox": map[string]any{"type": "string", "enum": []any{"off", "read-only", "workspace-write", "restricted"}}},
		"required":   []any{"hub"},
		"anyOf": []any{
			map[string]any{"type": "object", "not": map[string]any{"type": "object", "required": []string{"hub"}}},
			map[string]any{
				"type": "object", "required": []any{"hub", "sandbox"}, "additionalProperties": false,
				"properties": map[string]any{
					"hub":     map[string]any{"type": "object"},
					"sandbox": map[string]any{"enum": []any{"read-only", "workspace-write", "restricted"}},
				},
			},
		},
	}
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"hub": map[string]any{}, "sandbox": "off"}, "sandbox", "/anyOf/1/properties/sandbox/enum")
	// The branch's narrowed enum token appears only in the constraint
	// message; blanking the keyword by branch attribution would drop it.
	wantMsg(t, msg, "workspace-write", "arm-internal enum must keep its constraint message")
	wantMsg(t, msg, "sandbox", "message must name the offending property")
}

// branchCombinator attributes a root-level anyOf only when the location names
// branch structure the prose can render; an arm-internal leaf stays
// unattributed so it keeps its leaf keyword.
func TestBranchCombinator_AnyOfAttributionIsStructural(t *testing.T) {
	for kwLoc, want := range map[string]string{
		"/anyOf":                           "anyOf", // bare combinator: no arm matched
		"/anyOf/0":                         "anyOf", // an arm node
		"/anyOf/0/not":                     "anyOf", // the shape the Manager delegate produces
		"/anyOf/1/required":                "anyOf", // an arm's own required list
		"/anyOf/0/type":                    "",      // arm-internal leaf keeps its leaf keyword
		"/anyOf/1/additionalProperties":    "",
		"/properties/hub/anyOf/0/required": "", // nested combinator is not a root branch
	} {
		if got := branchCombinator(kwLoc); got != want {
			t.Errorf("branchCombinator(%q) = %q, want %q", kwLoc, got, want)
		}
	}
}

// The anyOf branch message's Example must match at least one arm: a Manager
// send shape whose two arms each demand properties the top-level required
// shape does not carry would otherwise coach a retry matching no branch.
func TestExplainSchemaError_AnyOfExampleMatchesABranchOrIsOmitted(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"to": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}},
		"required":   []any{"to"},
		"anyOf": []any{
			map[string]any{"type": "object", "required": []any{"message"}, "not": map[string]any{"anyOf": []any{
				map[string]any{"required": []any{"input_mode"}},
			}}},
			map[string]any{"type": "object", "required": []any{"to", "input_mode", "client_mutation_id", "expected_instance_id"}},
		},
	}
	// The arm-0 `not` with a nested anyOf is branch-structural, so the branch
	// prose renders; the example {"to": ...} satisfies neither arm and must be
	// withheld.
	msg := ExplainSchemaError("probe_tool", params, map[string]any{"to": "local:s", "message": "m", "input_mode": "start"}, "", "/anyOf/0/not")
	wantMsg(t, msg, "anyOf constraint", "message must name the anyOf constraint")
	notMsg(t, msg, `{"to"`, "example satisfied no anyOf arm and would coach a failing retry")
}

// An arm that carries its own oneOf/allOf/anyOf beside the clauses the branch
// renderer describes has a constraint the prose cannot name. The Manager
// delegate_send input arm has exactly that shape — its required list plus a
// sibling oneOf (message or input) plus a `not` — and a call that satisfies
// every required name but neither oneOf option must not be told "Branch 1
// requires: send all of <the four it already sent>, do not send
// max_wait_ms": every rendered clause would be satisfied while the actually
// missing requirement stays unnamed. The arm must be withheld so the caller
// gets the honest generic mismatch instead of confident misdirection.
func TestExplainSchemaError_ArmWithSiblingCombinatorIsWithheld(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"to":                   map[string]any{"type": "string"},
			"input_mode":           map[string]any{"type": "string"},
			"client_mutation_id":   map[string]any{"type": "string"},
			"expected_instance_id": map[string]any{"type": "string"},
			"message":              map[string]any{"type": "string"},
			"input":                map[string]any{"type": "array"},
		},
		"required": []any{"to"},
		"anyOf": []any{
			map[string]any{"type": "object", "required": []any{"message"}, "not": map[string]any{"type": "object", "anyOf": []any{
				map[string]any{"type": "object", "required": []any{"input_mode"}},
			}}},
			map[string]any{"type": "object", "required": []any{"to", "input_mode", "client_mutation_id", "expected_instance_id"},
				"oneOf": []any{
					map[string]any{"type": "object", "required": []any{"message"}},
					map[string]any{"type": "object", "required": []any{"input"}},
				},
				"not": map[string]any{"type": "object", "required": []any{"max_wait_ms"}}},
		},
	}
	// Every required name the arm-1 prose could list is supplied here; the
	// failure is the arm's own oneOf — neither message nor input was sent.
	args := map[string]any{"to": "local:s", "input_mode": "start", "client_mutation_id": "m1", "expected_instance_id": "i1"}
	for _, kwLoc := range []string{"/anyOf/1/oneOf", "/anyOf/1/required", "/anyOf/1"} {
		msg := ExplainSchemaError("delegate_send", params, args, "", kwLoc)
		notMsg(t, msg, "Branch 1 requires", "arm carrying a sibling oneOf must not present its satisfied clauses as the requirement (cause "+kwLoc+")")
		notMsg(t, msg, "Required arguments: to", "a supplied required argument must not be claimed missing (cause "+kwLoc+")")
	}
}
