package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/fuzz/promoter"
	"primeradiant.com/serf/fuzz/schemagen"
	"primeradiant.com/serf/llm"
)

// TestToolArgsSchemaFuzz is Phase-1 target #5: schema-AWARE tool-argument
// fuzzing. For every registered tool it feeds that tool's own JSON Schema
// (Definition.Parameters) into schemagen and drives the real compiled validator
// (Registry's *jsonschema.Schema) with adversarial-but-structured input.
//
// SAFETY: like the Phase-0 byte-fuzz target, this stops at the decode+validate
// boundary and NEVER calls a tool's handler. The core set includes shell
// (bash -c), web_fetch (network + out-of-root writes), read_file/edit_file/
// write_file (absolute paths escape any temp dir), and job/delegate (subagent
// spawning) — a temp dir does not sandbox these, so executing them under a
// fuzzer is unsafe and non-deterministic. Schema.Validate is the recover-free
// seam the research flagged, so the validate boundary is itself a live
// panic-hunt; schema-aware generation reaches deeper validator paths than raw
// byte fuzzing.
//
// Oracles (research §3):
//   - Floor: Validate must never panic, for valid OR adjacent input.
//   - Divergence (the payoff): a schema-VALID input must be ACCEPTED. If
//     schemagen produces a value it considers schema-valid and the real
//     validator rejects it, generator and schema disagree — a soft spot worth a
//     permanent regression. Adjacent input may be accepted or rejected; only a
//     panic counts against it.
//
// A discovered failure is routed through the fuzz/promoter so a deterministic
// reproduction becomes a flake-guarded regression test (written to a temp dir;
// promotion into the tree is the human/opt-in step). rapid shrinks to a minimal
// failing case before the cleanup promotes it.
func TestToolArgsSchemaFuzz(t *testing.T) {
	tools := coreToolSchemaDefs(t)
	if len(tools) == 0 {
		t.Fatal("no core tools registered")
	}

	schemas := make(map[string]schemaValidator, len(tools))
	for _, td := range tools {
		schemas[td.name] = td.schema
	}
	adapter := &toolArgsAdapter{schemas: schemas, emitDir: t.TempDir()}
	store, err := promoter.OpenBucketStore(filepath.Join(t.TempDir(), "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	promo := promoter.New(adapter, store, quietQuarantiner{}, 5)

	var captured *promoter.Failure
	t.Cleanup(func() {
		if captured == nil {
			return
		}
		out, err := promo.Promote(context.Background(), *captured)
		t.Logf("schema-fuzz failure promoted: outcome=%v err=%v detail=%q", out, err, captured.Detail)
	})

	rapid.Check(t, func(rt *rapid.T) {
		td := tools[rapid.IntRange(0, len(tools)-1).Draw(rt, "tool")]

		mode := schemagen.Valid
		if rapid.Bool().Draw(rt, "adjacent") {
			mode = schemagen.Adjacent
		}
		value := schemagen.Generator(td.params, mode).Draw(rt, "args")

		// Mirror ExecuteCall's real decode path exactly: marshal the generated
		// value and json.Unmarshal it into map[string]any (numbers become
		// float64, etc.). A non-object adjacent value cannot form args — that is
		// the JSON-tokenizer's job, not the validator seam under test.
		raw, err := json.Marshal(value)
		if err != nil {
			return
		}
		var args map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return
			}
		}
		if args == nil {
			args = map[string]any{}
		}

		if f := toolArgsFailure(td.name, mode, args, td.schema); f != nil {
			captured = f
			rt.Fatalf("tool %q (%s): %s", td.name, modeName(mode), f.Detail)
		}
	})
}

// TestToolArgsAdapter_PromotesDeterministicFailure exercises the agent-side
// promoter Adapter end-to-end against the REAL promoter: a deterministic
// validate divergence (a Valid-mode failure whose args genuinely violate the
// schema) must survive the flake-guard, emit a regression test, record its
// bucket, and dedup on the second sighting. This proves the four hooks wire up
// without depending on the live fuzzer finding a real bug.
func TestToolArgsAdapter_PromotesDeterministicFailure(t *testing.T) {
	tools := coreToolSchemaDefs(t)
	schemas := make(map[string]schemaValidator, len(tools))
	for _, td := range tools {
		schemas[td.name] = td.schema
	}
	readFile := schemas["read_file"]
	if readFile == nil {
		t.Fatal("read_file not registered")
	}

	adapter := &toolArgsAdapter{schemas: schemas, emitDir: t.TempDir()}
	store, err := promoter.OpenBucketStore(filepath.Join(t.TempDir(), "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	q := &countingQuarantiner{}
	promo := promoter.New(adapter, store, q, 5)

	// read_file requires file_path; these args omit it. Labelling the input
	// Valid makes the validator's rejection a deterministic ErrorShape divergence.
	args := map[string]any{"offset": float64(1)}
	f := toolArgsFailure("read_file", schemagen.Valid, args, readFile)
	if f == nil {
		t.Fatal("expected a divergence failure for required-field omission")
	}

	out, err := promo.Promote(context.Background(), *f)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out != promoter.Promoted {
		t.Fatalf("outcome = %v, want Promoted", out)
	}
	sig := adapter.Signature(*f)
	path, ok := store.Get(sig)
	if !ok {
		t.Fatalf("bucket %s not recorded", sig)
	}
	if !strings.HasSuffix(path, "_test.go") {
		t.Fatalf("emitted path %q is not a _test.go file", path)
	}
	if q.count != 0 {
		t.Fatalf("quarantine count = %d, want 0", q.count)
	}

	// Second sighting dedups: no second emission.
	out2, err := promo.Promote(context.Background(), *f)
	if err != nil {
		t.Fatalf("second Promote: %v", err)
	}
	if out2 != promoter.AlreadyKnown {
		t.Fatalf("second outcome = %v, want AlreadyKnown", out2)
	}

	// Exercise the body an emitted regression test runs: replaying a clean
	// artifact (a well-formed read_file call) must pass (the seam does not panic).
	clean, _ := json.Marshal(toolArgsArtifact{
		Tool: "read_file",
		Mode: int(schemagen.Valid),
		Args: map[string]any{"file_path": "x"},
	})
	replayToolArgsArtifact(t, string(clean))
}

// toolArgsAdapter is the agent-side promoter.Adapter for the tool-arg schema
// surface. It carries each tool's compiled validator so Replay can deterministic-
// ally re-run the validate seam against the captured artifact.
type toolArgsAdapter struct {
	schemas map[string]schemaValidator
	emitDir string
}

// toolArgsArtifact is the minimized reproducer: which tool, which generation
// mode, and the concrete (JSON-decoded) argument map.
type toolArgsArtifact struct {
	Tool string         `json:"tool"`
	Mode int            `json:"mode"`
	Args map[string]any `json:"args"`
}

func (a *toolArgsAdapter) Minimize(f promoter.Failure) promoter.Failure { return f }

func (a *toolArgsAdapter) Signature(f promoter.Failure) promoter.Signature {
	key := f.Detail
	if f.Oracle == promoter.Panic && len(f.Stack) > 0 {
		key = strings.Join(topFrames(f.Stack, 4), "|")
	}
	if key == "" {
		key = promoter.ShortHash(f)
	}
	return promoter.Signature{Oracle: f.Oracle, Key: key}
}

func (a *toolArgsAdapter) Replay(_ context.Context, f promoter.Failure) (bool, bool) {
	var art toolArgsArtifact
	if err := json.Unmarshal(f.Artifact, &art); err != nil {
		return false, false
	}
	sv := a.schemas[art.Tool]
	if sv == nil {
		return false, false
	}
	repro := toolArgsFailure(art.Tool, schemagen.Mode(art.Mode), art.Args, sv)
	if repro == nil {
		return false, false
	}
	return true, a.Signature(*repro) == a.Signature(f)
}

func (a *toolArgsAdapter) Emit(f promoter.Failure) (string, error) {
	return promoter.WriteGoTest(a.emitDir, promoter.GoTest{
		Package:    "agent",
		Surface:    f.Surface,
		Oracle:     f.Oracle,
		Signature:  a.Signature(f).String(),
		Seam:       "tool.Registry schema validation",
		Hash:       promoter.ShortHash(f),
		ReplayBody: "\treplayToolArgsArtifact(t, " + strconv.Quote(string(f.Artifact)) + ")",
	})
}

// toolArgsFailure runs the validate-boundary oracle and returns a Failure, or
// nil when the input is handled cleanly. It is the single source of the oracle
// so the live property and the adapter's Replay classify identically.
func toolArgsFailure(tool string, mode schemagen.Mode, args map[string]any, sv schemaValidator) *promoter.Failure {
	panicked, pv, stack, err := safeValidate(sv, args)
	artifact, _ := json.Marshal(toolArgsArtifact{Tool: tool, Mode: int(mode), Args: args})
	switch {
	case panicked:
		return &promoter.Failure{
			Surface:  "toolargs-schema",
			Oracle:   promoter.Panic,
			Stack:    stack,
			Detail:   "validate-panic:" + tool + ":" + firstLine(fmt.Sprint(pv)),
			Artifact: artifact,
		}
	case mode == schemagen.Valid && err != nil:
		return &promoter.Failure{
			Surface:  "toolargs-schema",
			Oracle:   promoter.ErrorShape,
			Detail:   "valid-input-rejected:" + tool,
			Artifact: artifact,
		}
	}
	return nil
}

// safeValidate runs Schema.Validate under a recover so a panic in the (non
// recover-wrapped) validator becomes a classifiable failure rather than crashing
// the fuzzer.
func safeValidate(sv schemaValidator, args map[string]any) (panicked bool, pv any, stack []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			pv = r
			stack = captureStack()
		}
	}()
	err = sv.Validate(args)
	return false, nil, nil, err
}

// replayToolArgsArtifact is the body of an emitted regression test: it rebuilds
// the real registry, finds the tool's schema, and asserts the validate seam no
// longer panics on the recorded artifact. (Generated regression tests call this;
// it is kept here so an emitted test compiles when moved into the package.)
func replayToolArgsArtifact(t *testing.T, artifact string) {
	t.Helper()
	var art toolArgsArtifact
	if err := json.Unmarshal([]byte(artifact), &art); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	for _, td := range coreToolSchemaDefs(t) {
		if td.name != art.Tool {
			continue
		}
		if f := toolArgsFailure(art.Tool, schemagen.Mode(art.Mode), art.Args, td.schema); f != nil && f.Oracle == promoter.Panic {
			t.Fatalf("validate still panics on recorded artifact: %s", f.Detail)
		}
		return
	}
	t.Fatalf("tool %q no longer registered", art.Tool)
}

// toolSchemaDef pairs a tool's name with its raw JSON Schema (fed to schemagen)
// and its compiled validator (the real seam).
type toolSchemaDef struct {
	name   string
	params map[string]any
	schema schemaValidator
}

// coreToolSchemaDefs stands up a real Session over a temp dir so registerCoreTools
// wires the full tool set, then returns each tool's name, raw Parameters schema,
// and compiled validator. Mirrors the Phase-0 helper but also exposes Parameters
// so the schema-aware generator sees exactly the schema serf ships.
func coreToolSchemaDefs(t testing.TB) []toolSchemaDef {
	t.Helper()
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	var defs []toolSchemaDef
	for _, name := range sess.reg.Names() {
		rt := sess.reg.Get(name)
		if rt == nil || rt.Schema == nil || rt.Definition.Parameters == nil {
			continue
		}
		defs = append(defs, toolSchemaDef{name: name, params: rt.Definition.Parameters, schema: rt.Schema})
	}
	return defs
}

func modeName(m schemagen.Mode) string {
	if m == schemagen.Adjacent {
		return "adjacent"
	}
	return "valid"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func topFrames(frames []string, n int) []string {
	if len(frames) < n {
		return frames
	}
	return frames[:n]
}

// captureStack returns project-relative frames for panic dedup.
func captureStack() []string {
	var pcs [16]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		fr, more := frames.Next()
		fn := fr.Function
		if i := strings.LastIndex(fn, "serf/"); i >= 0 {
			fn = fn[i+len("serf/"):]
		}
		out = append(out, fmt.Sprintf("%s:%d", fn, fr.Line))
		if !more || len(out) >= 8 {
			break
		}
	}
	return out
}

// quietQuarantiner logs nothing; the live target's cleanup reports outcomes via
// t.Logf, and a quarantined flake is simply not promoted.
type quietQuarantiner struct{}

func (quietQuarantiner) Quarantine(promoter.Failure, int) error { return nil }

// countingQuarantiner records how many failures were quarantined, for assertions.
type countingQuarantiner struct{ count int }

func (q *countingQuarantiner) Quarantine(promoter.Failure, int) error {
	q.count++
	return nil
}
