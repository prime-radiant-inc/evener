package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// FuzzToolArgsValidate hunts panics in the tool-call argument decode+validate
// path that every model-generated tool call flows through. The real soft spot
// (research §3) is that Registry.ExecuteCall runs t.Schema.Validate(args)
// WITHOUT a recover() wrapper at runtime — only schema *compilation* is
// guarded — so a pathological-but-decodable argument map is a live panic site.
//
// The harness builds a real registry via registerCoreTools (by standing up a
// Session over a temp dir) and drives exactly that decode→validate seam over
// every registered tool's compiled schema. It deliberately stops short of
// t.Exec: the core tool set includes shell (arbitrary command execution),
// web_fetch (network + out-of-root cache writes), and job/delegate (subagent
// spawning), none of which a temp-dir env contains and none of which are
// deterministic — so executing them under a fuzzer is both unsafe and
// non-deterministic. The panic-hunt the design calls out lives entirely in
// decode+Validate, before execution.
//
// Oracle: decode+Validate must never panic, and must terminate with either a
// clean validation error or a clean accept — never a partial/garbage state.
func FuzzToolArgsValidate(f *testing.F) {
	names, schemas := coreToolSchemas(f)

	// Seeds: a valid-ish read_file call, an empty object, and adversarial shapes
	// (wrong types, deep nesting, huge numbers, nulls) that exercise the
	// validator rather than the JSON tokenizer.
	seeds := []struct {
		name int
		args string
	}{
		{0, `{}`},
		{0, `{"path":"x"}`},
		{0, `{"path":123}`},
		{0, `{"path":["a","b"]}`},
		{0, `{"path":null,"extra":{"deeply":{"nested":true}}}`},
		{0, `{"tail_lines":1e308}`},
		{0, `{"tail_lines":-9999999999999999}`},
		{0, `not json`},
		{0, `[]`},
		{0, `{"a":` + `"\ud800"` + `}`}, // lone surrogate
	}
	for _, s := range seeds {
		f.Add(s.name, []byte(s.args))
	}

	// manage_worktree seeds: the operation × name/path/base_ref/force/
	// delete_branch cross-product (spec §10 "Fuzz target for arg validation
	// (extends FuzzToolArgsValidate table)"), addressed by NAME rather than a
	// hardcoded table index — the sorted core-tool-name order the other seeds
	// above index into is otherwise incidental, but manage_worktree's own
	// index would silently drift if a tool were added/renamed alphabetically
	// around it. manage_worktree's schema (DefManageWorktree) only requires
	// "operation" and forbids additionalProperties; which of name/path/
	// force/delete_branch apply to which operation is enforced by the
	// HANDLER, not the schema (see the schema's own doc comment), so a
	// schema-valid combination here may still be operation-invalid at
	// execution — only decode+Validate must never panic.
	mwIdx := -1
	for i, n := range names {
		if n == "manage_worktree" {
			mwIdx = i
			break
		}
	}
	if mwIdx < 0 {
		f.Fatalf("manage_worktree not found among core tool schemas")
	}
	mwSeeds := []string{
		`{"operation":"create","name":"lane","base_ref":"main"}`,
		`{"operation":"create","name":"lane"}`,
		`{"operation":"create"}`,
		`{"operation":"list"}`,
		`{"operation":"switch","name":"lane"}`,
		`{"operation":"switch","path":"/tmp/x"}`,
		`{"operation":"switch","name":"lane","path":"/tmp/x"}`,
		`{"operation":"switch"}`,
		`{"operation":"exit"}`,
		`{"operation":"remove","name":"lane","force":true,"delete_branch":true}`,
		`{"operation":"remove","name":"lane","force":false,"delete_branch":false}`,
		`{"operation":"remove","name":"lane","force_dirty":true}`,
		`{"operation":"remove","name":"lane","force":true,"force_dirty":true,"delete_branch":true}`,
		`{"operation":"remove"}`,
		`{"operation":"prune"}`,
		`{"operation":"bogus"}`, // invalid enum value
		`{"operation":123}`,     // wrong type for operation
		`{"operation":"create","name":123,"base_ref":true,"force":"nope","delete_branch":[1,2,3]}`,
		`{"operation":"create","name":null}`,
		`{}`,                                            // missing required "operation"
		`{"operation":"create","unexpected_field":"x"}`, // additionalProperties:false
	}
	for _, s := range mwSeeds {
		f.Add(mwIdx, []byte(s))
	}

	f.Fuzz(func(t *testing.T, nameIndex int, argsBytes []byte) {
		if len(names) == 0 {
			t.Fatal("no core tools registered")
		}
		idx := nameIndex % len(names)
		if idx < 0 {
			idx += len(names)
		}

		// Mirror ExecuteCall's decode step: arguments decode to map[string]any.
		var args map[string]any
		if len(argsBytes) > 0 {
			if err := json.Unmarshal(argsBytes, &args); err != nil {
				return // ExecuteCall returns a clean error here; not the seam under test
			}
		}
		if args == nil {
			args = map[string]any{}
		}

		// The non-recover-wrapped seam. A panic here fails the test; a returned
		// error is the expected clean-rejection outcome.
		_ = schemas[idx].Validate(args)
	})
}

// coreToolSchemas stands up a real Session over a temp dir so registerCoreTools
// wires the full tool set, then returns each tool's name and compiled schema.
// The name order is sourced from agent.CoreToolNames — the same ordered set the
// corpus harvester (cmd/serf-fuzz-harvest) maps recorded tool-call names against
// — so the target's table index and the harvester's emitted index cannot drift.
// The Session is built once per fuzz run and closed when the run ends.
func coreToolSchemas(f *testing.F) ([]string, []schemaValidator) {
	f.Helper()

	names, err := CoreToolNames()
	if err != nil {
		f.Fatalf("CoreToolNames: %v", err)
	}

	dir := f.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		f.Fatalf("NewSession: %v", err)
	}
	f.Cleanup(sess.Close)

	schemas := make([]schemaValidator, len(names))
	for i, name := range names {
		rt := sess.reg.Get(name)
		if rt == nil || rt.Schema == nil {
			f.Fatalf("CoreToolNames returned %q but the live registry has no compiled schema for it", name)
		}
		schemas[i] = rt.Schema
	}
	return names, schemas
}

// schemaValidator is the slice of *jsonschema.Schema the harness depends on:
// validating a decoded argument map.
type schemaValidator interface {
	Validate(v interface{}) error
}
