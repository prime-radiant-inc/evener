package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The tests here cover the semantic repeated-failure fingerprint: a call that
// changes only free-text fields (intent, the shell job description) or JSON
// formatting is the same failing operation, while any change to a field the
// tool executes on (target, mode, offset) is a fresh attempt that must not
// erase the old fingerprint's run. A value that merely looks like a default is
// not normalized away: presence is meaningful per the field's contract.

const semanticBoom = "invalid_request: context_lines requires output_match"

// semanticRefTool registers a failing fake whose only meaningful argument is a
// transcript ref, mirroring the read_transcript loop from issue #829.
func semanticRefTool(t *testing.T, r *Registry, fn func(calls int) (any, error)) *breakerFake {
	t.Helper()
	return registerBreakerFake(t, r, "read_transcript", fn)
}

func semanticRefCall(id, ref, intent string) llmCallArgs {
	return llmCallArgs{id: id, args: fmt.Sprintf(`{"transcript_ref":%q,"intent":%q}`, ref, intent)}
}

// llmCallArgs is a tiny carrier so table-driven tests read clearly.
type llmCallArgs struct {
	id   string
	args string
}

func TestBreakerDispatch_IntentOnlyChangeStillParks(t *testing.T) {
	r := NewRegistry()
	fake := semanticRefTool(t, r, func(int) (any, error) { return nil, errors.New(semanticBoom) })
	env := breakerEnv(t)
	ctx := context.Background()

	calls := []llmCallArgs{
		semanticRefCall("c1", "job:job_1", "first look at the job output"),
		semanticRefCall("c2", "job:job_1", "try reading the job again"),
		semanticRefCall("c3", "job:job_1", "one more attempt at the job"),
	}
	results := make([]ExecResult, 0, len(calls))
	for _, c := range calls {
		results = append(results, r.ExecuteCall(ctx, env, breakerCall(c.id, "read_transcript", c.args)))
	}

	if fake.calls != 2 {
		t.Fatalf("intent-only changes reset the failure streak: invocations = %d, want 2", fake.calls)
	}
	if !strings.HasPrefix(results[2].Output, wantFailurePark("read_transcript")) {
		t.Fatalf("third intent-only variant was not parked: %q", results[2].Output)
	}
}

func TestBreakerDispatch_IntentAndFormattingStillPark(t *testing.T) {
	r := NewRegistry()
	fake := semanticRefTool(t, r, func(int) (any, error) { return nil, errors.New(semanticBoom) })
	env := breakerEnv(t)
	ctx := context.Background()

	// Same effective call every time: differing JSON key order, whitespace, and
	// free-text intent must all fingerprint the same. A value that merely looks
	// like a default is deliberately absent here, because it is no longer
	// normalized away (see TestFailureFingerprint_MeaningfulDefaultsArePreserved).
	calls := []llmCallArgs{
		{id: "n1", args: `{"transcript_ref":"job:job_1","intent":"look"}`},
		{id: "n2", args: `{ "intent" : "look again" , "transcript_ref" : "job:job_1" }`},
		{id: "n3", args: `{"transcript_ref":"job:job_1"}`},
	}
	results := make([]ExecResult, 0, len(calls))
	for _, c := range calls {
		results = append(results, r.ExecuteCall(ctx, env, breakerCall(c.id, "read_transcript", c.args)))
	}

	if fake.calls != 2 {
		t.Fatalf("intent-only changes reset the failure streak: invocations = %d, want 2", fake.calls)
	}
	if !strings.HasPrefix(results[2].Output, wantFailurePark("read_transcript")) {
		t.Fatalf("third intent-only variant was not parked: %q", results[2].Output)
	}
}

func TestBreakerDispatch_MeaningfulChangeGetsFreshAttempt(t *testing.T) {
	r := NewRegistry()
	fake := semanticRefTool(t, r, func(int) (any, error) { return nil, errors.New(semanticBoom) })
	env := breakerEnv(t)
	ctx := context.Background()

	r.ExecuteCall(ctx, env, breakerCall("m1", "read_transcript", semanticRefCall("", "job:job_a", "x").args))
	r.ExecuteCall(ctx, env, breakerCall("m2", "read_transcript", semanticRefCall("", "job:job_a", "y").args))

	changed := r.ExecuteCall(ctx, env, breakerCall("m3", "read_transcript", semanticRefCall("", "job:job_b", "z").args))
	if fake.calls != 3 {
		t.Fatalf("a meaningful ref change must dispatch: invocations = %d, want 3", fake.calls)
	}
	if strings.HasPrefix(changed.Output, "evener did not execute this call:") {
		t.Fatalf("a meaningful ref change was parked: %q", changed.Output)
	}
}

func TestBreakerDispatch_AlternatingFingerprintsKeepBothRuns(t *testing.T) {
	r := NewRegistry()
	fake := semanticRefTool(t, r, func(int) (any, error) { return nil, errors.New(semanticBoom) })
	env := breakerEnv(t)
	ctx := context.Background()

	calls := []llmCallArgs{
		semanticRefCall("a1", "job:job_a", "a one"),
		semanticRefCall("b1", "job:job_b", "b one"),
		semanticRefCall("a2", "job:job_a", "a two"),
		semanticRefCall("b2", "job:job_b", "b two"),
	}
	for _, c := range calls {
		r.ExecuteCall(ctx, env, breakerCall(c.id, "read_transcript", c.args))
	}
	if fake.calls != 4 {
		t.Fatalf("setup invocations = %d, want 4", fake.calls)
	}

	// Both runs reached the threshold independently; neither was erased by the
	// other's interleaved calls, so the next call of each parks.
	a3 := r.ExecuteCall(ctx, env, breakerCall("a3", "read_transcript", semanticRefCall("", "job:job_a", "a three").args))
	b3 := r.ExecuteCall(ctx, env, breakerCall("b3", "read_transcript", semanticRefCall("", "job:job_b", "b three").args))
	if fake.calls != 4 {
		t.Fatalf("interleaved runs erased each other: invocations = %d, want 4", fake.calls)
	}
	if !strings.HasPrefix(a3.Output, wantFailurePark("read_transcript")) {
		t.Fatalf("fingerprint A was not parked: %q", a3.Output)
	}
	if !strings.HasPrefix(b3.Output, wantFailurePark("read_transcript")) {
		t.Fatalf("fingerprint B was not parked: %q", b3.Output)
	}
}

func TestBreakerDispatch_SuccessClearsOnlyMatchingRun(t *testing.T) {
	r := NewRegistry()
	type step struct {
		ref     string
		success bool
	}
	seq := []step{
		{ref: "job:job_a"},
		{ref: "job:job_b"},
		{ref: "job:job_b"},
		{ref: "job:job_a", success: true},
		{ref: "job:job_a"},
	}
	fake := semanticRefTool(t, r, func(n int) (any, error) {
		if n < 1 || n > len(seq) {
			return nil, fmt.Errorf("unexpected dispatch %d", n)
		}
		if seq[n-1].success {
			return "ok", nil
		}
		return nil, errors.New(semanticBoom)
	})
	env := breakerEnv(t)
	ctx := context.Background()

	send := func(id, ref, intent string) ExecResult {
		return r.ExecuteCall(ctx, env, breakerCall(id, "read_transcript", semanticRefCall("", ref, intent).args))
	}

	send("s1", "job:job_a", "one")
	send("s2", "job:job_b", "one")
	send("s3", "job:job_b", "two")
	send("s4", "job:job_a", "cleared") // success
	send("s5", "job:job_a", "rebuilt")
	if fake.calls != 5 {
		t.Fatalf("setup invocations = %d, want 5", fake.calls)
	}

	if streak, _, _ := r.breaker.check(newDispatchKey("read_transcript", []byte(`{"transcript_ref":"job:job_a"}`))); streak != 1 {
		t.Fatalf("A's run after its own success = %d, want 1", streak)
	}
	if streak, _, _ := r.breaker.check(newDispatchKey("read_transcript", []byte(`{"transcript_ref":"job:job_b"}`))); streak != 2 {
		t.Fatalf("B's run was altered by A's success: streak = %d, want 2", streak)
	}

	// B has reached the threshold and parks; A still has room for one more.
	parkedB := send("s6", "job:job_b", "three")
	if fake.calls != 5 {
		t.Fatalf("B was not parked: invocations = %d, want 5", fake.calls)
	}
	if !strings.HasPrefix(parkedB.Output, wantFailurePark("read_transcript")) {
		t.Fatalf("B's third failure was not parked: %q", parkedB.Output)
	}
	send("s7", "job:job_a", "four")
	if fake.calls != 6 {
		t.Fatalf("A should still dispatch after its success reset: invocations = %d, want 6", fake.calls)
	}
}

func TestBreakerDispatch_BypassStillExecutesSemanticallyParkedCall(t *testing.T) {
	r := NewRegistry()
	fake := semanticRefTool(t, r, func(int) (any, error) { return nil, errors.New(semanticBoom) })
	env := breakerEnv(t)
	ctx := context.Background()

	send := func(c context.Context, id, ref, intent string) ExecResult {
		return r.ExecuteCall(c, env, breakerCall(id, "read_transcript", semanticRefCall("", ref, intent).args))
	}
	send(ctx, "y1", "job:job_a", "one")
	send(ctx, "y2", "job:job_a", "two")
	if fake.calls != 2 {
		t.Fatalf("setup invocations = %d, want 2", fake.calls)
	}
	parked := send(ctx, "y3", "job:job_a", "three")
	if !strings.HasPrefix(parked.Output, wantFailurePark("read_transcript")) {
		t.Fatalf("third call was not parked: %q", parked.Output)
	}
	if fake.calls != 2 {
		t.Fatalf("parked call executed: invocations = %d, want 2", fake.calls)
	}

	bypassed := send(WithBreakerBypass(ctx), "y4", "job:job_a", "authorized")
	if fake.calls != 3 {
		t.Fatalf("a bypassed call must execute: invocations = %d, want 3", fake.calls)
	}
	if strings.HasPrefix(bypassed.Output, "evener did not execute this call:") {
		t.Fatalf("bypassed call was parked: %q", bypassed.Output)
	}
}

func fp(name, args string) string {
	return failureFingerprint(name, []byte(args))
}

func TestFailureFingerprint_KeyOrderWhitespaceAndIntentAreEquivalent(t *testing.T) {
	want := fp("read_transcript", `{"transcript_ref":"job:j1","intent":"first"}`)
	for _, args := range []string{
		`{"transcript_ref":"job:j1","intent":"second"}`,
		`{ "intent" : "third" , "transcript_ref" : "job:j1" }`,
		`{"transcript_ref":"job:j1"}`, // intent omitted entirely
		`{"transcript_ref":"job:j1","intent":null}`,
	} {
		if got := fp("read_transcript", args); got != want {
			t.Errorf("fingerprint(%s) = %q, want %q", args, got, want)
		}
	}
}

// A value that looks like a default is presence, not omission. Fields are
// dropped by name only, so each of these keeps its own fingerprint rather than
// folding into the omitted form. read_transcript's offset_bytes=0 selects the
// retained-page operation and task_list's depends_on: [] clears dependencies; a
// provider-materialized default is a real argument too.
func TestFailureFingerprint_MeaningfulDefaultsArePreserved(t *testing.T) {
	base := fp("read_transcript", `{"transcript_ref":"job:j1"}`)
	for _, args := range []string{
		`{"transcript_ref":"job:j1","offset_bytes":0}`,
		`{"transcript_ref":"job:j1","range":""}`,
		`{"transcript_ref":"job:j1","format":null}`,
		`{"transcript_ref":"job:j1","context_lines":0}`,
		`{"transcript_ref":"job:j1","extra":{}}`,
		`{"transcript_ref":"job:j1","list":[]}`,
	} {
		if got := fp("read_transcript", args); got == base {
			t.Errorf("fingerprint(%s) = %q, must differ from the omitted form", args, got)
		}
	}
	if fp("read_file", `{"offset_bytes":0}`) == fp("read_file", `{}`) {
		t.Error("explicit offset_bytes=0 must not fingerprint as omitted")
	}
	// Empty and absent arguments are the same call.
	if fp("read_transcript", ``) != fp("read_transcript", `{}`) {
		t.Errorf("empty and {} must fingerprint the same")
	}
}

// Canonicalization must not parse a body that ValidateRawArguments rejects:
// the size and UTF-8 limits are the pre-parse boundary, so an oversized or
// non-UTF-8 body falls back to the byte-exact signature rather than being
// decoded and re-encoded first.
// Only the top-level intent is free text: the registry strips just that one, so
// a nested field named intent reaches the handler and must stay meaningful.
func TestFailureFingerprint_NestedIntentIsMeaningful(t *testing.T) {
	a := fp("task_list", `{"update":[{"id":1,"intent":"alpha"}]}`)
	b := fp("task_list", `{"update":[{"id":1,"intent":"beta"}]}`)
	if a == b {
		t.Errorf("nested intent was normalized away: %q", a)
	}
	rootA := fp("task_list", `{"intent":"alpha","update":[{"id":1}]}`)
	rootB := fp("task_list", `{"intent":"beta","update":[{"id":1}]}`)
	if rootA != rootB {
		t.Errorf("top-level intent must be normalized: %q vs %q", rootA, rootB)
	}
}

// Whitespace-only arguments are not an empty object: ExecuteCall rejects them as
// invalid JSON, so the fingerprint must treat them as opaque bytes rather than
// folding them into the `{}` call.
func TestFailureFingerprint_WhitespaceOnlyIsNotAnEmptyObject(t *testing.T) {
	const spaces = "   "
	if got, want := fp("read_transcript", spaces), exactSignature("read_transcript", []byte(spaces)); got != want {
		t.Errorf("whitespace-only fingerprint = %q, want the exact signature %q", got, want)
	}
	if fp("read_transcript", `{}`) == fp("read_transcript", spaces) {
		t.Error("whitespace-only must not fingerprint as an empty object")
	}
}

func TestFailureFingerprint_RejectedRawArgumentsFallBackToExact(t *testing.T) {
	oversized := append([]byte(`{"patch":"`), bytes.Repeat([]byte("a"), MaxToolArgumentBytes)...)
	oversized = append(oversized, []byte(`"}`)...)
	if got, want := fp("write_file", string(oversized)), exactSignature("write_file", oversized); got != want {
		t.Errorf("oversized fingerprint = %q, want the exact signature %q", got, want)
	}
	invalid := []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
	if got, want := fp("write_file", string(invalid)), exactSignature("write_file", invalid); got != want {
		t.Errorf("non-UTF-8 fingerprint = %q, want the exact signature %q", got, want)
	}
}

func TestFailureFingerprint_EquivalentNumberLiteralsAreEqual(t *testing.T) {
	if fp("read_file", `{"offset_bytes":1}`) != fp("read_file", `{"offset_bytes":1.0}`) {
		t.Errorf("1 and 1.0 must fingerprint the same")
	}
	if fp("read_file", `{"offset_bytes":1}`) != fp("read_file", `{"offset_bytes":1e0}`) {
		t.Errorf("1 and 1e0 must fingerprint the same")
	}
}

// An integer literal that overflows int64 falls through to ParseFloat and is
// rounded, so two distinct integers beyond int64 collapse to the same float and
// therefore the same fingerprint, sharing one failure run. Integer-shaped
// literals must keep their exact value instead.
func TestFailureFingerprint_HugeIntegersStayDistinct(t *testing.T) {
	pairs := [][2]string{
		{`{"offset_bytes":9223372036854775808}`, `{"offset_bytes":9223372036854775809}`},
		{`{"offset_bytes":18446744073709551616}`, `{"offset_bytes":18446744073709551617}`},
		{`{"offset_bytes":-9223372036854775809}`, `{"offset_bytes":-9223372036854775810}`},
	}
	for _, p := range pairs {
		if fp("read_file", p[0]) == fp("read_file", p[1]) {
			t.Errorf("distinct integers beyond int64 collapsed to one fingerprint: %s and %s", p[0], p[1])
		}
	}

	// Suffixed spellings that denote the same huge integer are still
	// integer-valued: they must fold to the bare form by value rather than round
	// through float64, and a rounding must not let them collide with a distinct
	// integer.
	base := fp("read_file", `{"offset_bytes":9223372036854775808}`)
	for _, args := range []string{
		`{"offset_bytes":9223372036854775808.0}`,
		`{"offset_bytes":9223372036854775808e0}`,
		`{"offset_bytes":92.23372036854775808e17}`,
	} {
		if got := fp("read_file", args); got != base {
			t.Errorf("integer-valued spelling %s = %q, want the bare form's fingerprint %q", args, got, base)
		}
	}
	for _, suffix := range []string{".0", "e0"} {
		if fp("read_file", `{"offset_bytes":9223372036854775808`+suffix+`}`) == fp("read_file", `{"offset_bytes":9223372036854775809}`) {
			t.Errorf("suffixed integer 9223372036854775808%s collided with the distinct integer 9223372036854775809", suffix)
		}
	}
	// The residual rounding collision is between two suffixed spellings, since
	// the bare forms are now kept exact: 9223372036854775808<suffix> and
	// 9223372036854775809<suffix> must not round to the same float.
	for _, suffix := range []string{".0", "e0"} {
		left := `{"offset_bytes":9223372036854775808` + suffix + `}`
		right := `{"offset_bytes":9223372036854775809` + suffix + `}`
		if fp("read_file", left) == fp("read_file", right) {
			t.Errorf("suffixed integers %s and %s collided", left, right)
		}
	}
}

// The fix must leave the int64 and float paths exactly as they were: values that
// fit int64 stay exact and their equivalent literals keep folding, and genuine
// floats keep folding to the same value while distinct floats stay distinct.
func TestFailureFingerprint_Int64AndFloatPathsUnchanged(t *testing.T) {
	// Values that fit int64 are exact and unaffected: distinct integers stay
	// distinct, including the pair straddling the float64 integer-precision
	// boundary at 2^53.
	if fp("read_file", `{"offset_bytes":9007199254740992}`) == fp("read_file", `{"offset_bytes":9007199254740993}`) {
		t.Errorf("distinct int64 values collapsed")
	}
	if fp("read_file", `{"offset_bytes":42}`) == fp("read_file", `{"offset_bytes":43}`) {
		t.Errorf("distinct int64 values collapsed")
	}
	// Equivalent int64 literals still fold.
	if fp("read_file", `{"offset_bytes":42}`) != fp("read_file", `{"offset_bytes":42.0}`) {
		t.Errorf("42 and 42.0 must fingerprint the same")
	}
	if fp("read_file", `{"offset_bytes":42}`) != fp("read_file", `{"offset_bytes":4.2e1}`) {
		t.Errorf("42 and 4.2e1 must fingerprint the same")
	}
	// Boundary consistency: a value that fits int64 and its suffixed
	// integer-valued spelling must fold, which requires the int64 path and the
	// big-number path to emit identical canonical bytes for the same value.
	if fp("read_file", `{"offset_bytes":9223372036854775807}`) != fp("read_file", `{"offset_bytes":9223372036854775807.0}`) {
		t.Errorf("max int64 and its .0 spelling must fingerprint the same")
	}
	// Genuine floats are unaffected: equivalent literals fold, distinct values
	// stay distinct.
	if fp("read_file", `{"offset_bytes":1.5}`) != fp("read_file", `{"offset_bytes":1.50}`) {
		t.Errorf("1.5 and 1.50 must fingerprint the same")
	}
	if fp("read_file", `{"offset_bytes":1.5}`) == fp("read_file", `{"offset_bytes":1.6}`) {
		t.Errorf("distinct floats collapsed")
	}
}

func TestFailureFingerprint_MeaningfulChangesAreDistinct(t *testing.T) {
	cases := [][2]string{
		{`{"transcript_ref":"job:j1"}`, `{"transcript_ref":"job:j2"}`},
		{`{"offset_bytes":10}`, `{"offset_bytes":20}`},
		{`{"output_match":"alpha"}`, `{"output_match":"beta"}`},
		{`{"mode":"foreground"}`, `{"mode":"background"}`},
	}
	for _, c := range cases {
		if fp("read_transcript", c[0]) == fp("read_transcript", c[1]) {
			t.Errorf("meaningful change %s -> %s collapsed", c[0], c[1])
		}
	}
}

// A root null body dispatches as an empty argument object: registry.executeCall
// unmarshals it to a nil map and normalizes that to map[string]any{}, the same
// effective call as a zero-length body. Its fingerprint must therefore match
// "{}" rather than the literal null. A null nested inside an object or array
// reaches the handler as a real value and stays distinct from the key being
// absent.
func TestFailureFingerprint_RootNullIsAnEmptyArgumentObject(t *testing.T) {
	if got, want := fp("read_transcript", "null"), fp("read_transcript", "{}"); got != want {
		t.Errorf("root null fingerprint = %q, want the empty-object fingerprint %q", got, want)
	}
	if fp("read_transcript", "null") != fp("read_transcript", "") {
		t.Errorf("root null must fingerprint the same as the empty body")
	}
	if nested := fp("task_list", `{"update":[{"id":1,"value":null}]}`); nested == fp("task_list", `{"update":[{"id":1}]}`) {
		t.Errorf("a nested null must stay distinct from the key being absent")
	}
}

func TestFailureFingerprint_UnparseableArgumentsFallBackToExact(t *testing.T) {
	for _, args := range []string{
		`{"transcript_ref":`,
		`{"a":1}{"b":2}`,
		`not json`,
	} {
		if got, want := failureFingerprint("t", []byte(args)), exactSignature("t", []byte(args)); got != want {
			t.Errorf("fallback fingerprint(%s) = %q, want exact %q", args, got, want)
		}
	}
}

func TestFailureFingerprint_ShellDescriptionIsPresentationOnly(t *testing.T) {
	if fp("shell", `{"command":"ls","description":"list files"}`) != fp("shell", `{"command":"ls"}`) {
		t.Errorf("shell description must not change the fingerprint")
	}
	// A task's description is meaningful and must stay distinct.
	if fp("task_list", `{"update":[{"id":1,"description":"x"}]}`) == fp("task_list", `{"update":[{"id":1}]}`) {
		t.Errorf("task_list description must remain part of the fingerprint")
	}
}

func TestFailureFingerprint_IsBoundedAndSecretFree(t *testing.T) {
	const secret = "TOP-SECRET-CREDENTIAL"
	got := fp("shell", fmt.Sprintf(`{"command":%q,"intent":%q}`, secret, secret))
	if strings.Contains(got, secret) {
		t.Fatalf("fingerprint leaked a secret value: %q", got)
	}
	if len(got) > 128 {
		t.Fatalf("fingerprint is not bounded: %d bytes", len(got))
	}
}

// The repetition nudge is an exact-call signal and must stay byte-keyed: two
// calls whose raw arguments differ only by a free-text intent are the same
// semantic operation but not a byte-identical repeat, so a caller comparing
// their bodies must see the unmodified result.
func TestBreakerDispatch_IntentChangeDoesNotTriggerRepetitionNudge(t *testing.T) {
	const body = "ready\n"
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "read_transcript", func(int) (any, error) { return body, nil })
	env := breakerEnv(t)
	ctx := context.Background()

	omitted := r.ExecuteCall(ctx, env, breakerCall("o1", "read_transcript", `{"transcript_ref":"job:job_1"}`))
	if omitted.Output != body {
		t.Fatalf("omitted output = %q, want %q", omitted.Output, body)
	}
	materialized := r.ExecuteCall(ctx, env, breakerCall("m1", "read_transcript", `{"transcript_ref":"job:job_1","intent":"again"}`))
	if fake.calls != 2 {
		t.Fatalf("an intent-only change must dispatch: invocations = %d, want 2", fake.calls)
	}
	if strings.Contains(materialized.Output, repetitionNudgeMarker) {
		t.Fatalf("an intent-only change triggered the exact-call repetition nudge: %q", materialized.Output)
	}
	if materialized.Output != body {
		t.Fatalf("intent-only output = %q, want the unmodified %q", materialized.Output, body)
	}
}

// A successful dispatch carries no failure evidence worth remembering, so it
// must not create a semantic entry or consume an LRU slot. Under the defect,
// every success inserted a key into the bounded semantic store, so more than
// maxFailureLedgerEntries distinct successful calls between two failures of one
// fingerprint evicted the pending run and the semantic breaker silently forgot
// it. This drives the real registry dispatch path: a failure, a flood of
// distinct successes, then the calls that must continue the run and park.
func TestBreakerDispatch_SuccessFloodDoesNotEvictPendingFailureRun(t *testing.T) {
	r := NewRegistry()
	flaky := registerBreakerFake(t, r, "flaky", func(int) (any, error) {
		return nil, errors.New(semanticBoom)
	})
	churn := registerBreakerFake(t, r, "churn", func(int) (any, error) { return "ok", nil })
	env := breakerEnv(t)
	ctx := context.Background()

	failing := breakerCall("f1", "flaky", `{"target":"a"}`)
	if res := r.ExecuteCall(ctx, env, failing); !res.IsError {
		t.Fatalf("setup failure did not run: %#v", res)
	}
	if flaky.calls != 1 {
		t.Fatalf("setup invocations = %d, want 1", flaky.calls)
	}

	// More distinct successful dispatches than the semantic store can hold. Each
	// carries a different semantic fingerprint, so under the defect each one
	// consumes a slot and pushes the pending run out.
	for i := range maxFailureLedgerEntries + 1 {
		r.ExecuteCall(ctx, env, breakerCall(fmt.Sprintf("churn-%d", i), "churn", fmt.Sprintf(`{"i":%d}`, i)))
	}
	if churn.calls != maxFailureLedgerEntries+1 {
		t.Fatalf("churn invocations = %d, want %d", churn.calls, maxFailureLedgerEntries+1)
	}
	if streak, _, _ := r.breaker.check(newDispatchKey("flaky", []byte(`{"target":"a"}`))); streak != 1 {
		t.Fatalf("pending failure run was evicted by successful traffic: streak = %d, want 1", streak)
	}

	// The run continues rather than restarting at 1: the second failure is the
	// one that nudges, and the third is refused.
	second := r.ExecuteCall(ctx, env, failing)
	if flaky.calls != 2 {
		t.Fatalf("the run's second failure was not dispatched: invocations = %d, want 2", flaky.calls)
	}
	if !strings.HasSuffix(second.Output, wantFailureNudge) {
		t.Fatalf("the continued run did not reach the nudge on its second failure: %q", second.Output)
	}
	third := r.ExecuteCall(ctx, env, failing)
	if flaky.calls != 2 {
		t.Fatalf("the run's third failure was not parked: invocations = %d, want 2", flaky.calls)
	}
	if !strings.HasPrefix(third.Output, wantFailurePark("flaky")) {
		t.Fatalf("the run's third failure was not parked: %q", third.Output)
	}
}
