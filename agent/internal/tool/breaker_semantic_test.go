package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The tests here cover the semantic repeated-failure fingerprint: a call that
// changes only free-text or neutral/default fields is the same failing
// operation, while a meaningful change (target, mode, offset) is a fresh
// attempt that must not erase the old fingerprint's run.

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

func TestBreakerDispatch_NeutralDefaultsAndFormattingStillPark(t *testing.T) {
	r := NewRegistry()
	fake := semanticRefTool(t, r, func(int) (any, error) { return nil, errors.New(semanticBoom) })
	env := breakerEnv(t)
	ctx := context.Background()

	// Same effective call every time: differing JSON key order, whitespace, and
	// provider-materialized default-equivalent values (empty strings and nulls,
	// which the tool schemas call omitted) must all fingerprint the same.
	// Explicit numeric zeros are deliberately absent here; see
	// TestFailureFingerprint_ExplicitZeroIsDistinct.
	calls := []llmCallArgs{
		{id: "n1", args: `{"transcript_ref":"job:job_1","intent":"look"}`},
		{id: "n2", args: `{ "intent" : "look again" , "transcript_ref" : "job:job_1" }`},
		{id: "n3", args: `{"range":"","transcript_ref":"job:job_1"}`},
		{id: "n4", args: `{"output_match":"","format":null,"transcript_ref":"job:job_1"}`},
	}
	results := make([]ExecResult, 0, len(calls))
	for _, c := range calls {
		results = append(results, r.ExecuteCall(ctx, env, breakerCall(c.id, "read_transcript", c.args)))
	}

	if fake.calls != 2 {
		t.Fatalf("neutral defaults reset the failure streak: invocations = %d, want 2", fake.calls)
	}
	if !strings.HasPrefix(results[2].Output, wantFailurePark("read_transcript")) {
		t.Fatalf("third neutral-default variant was not parked: %q", results[2].Output)
	}
	if !strings.HasPrefix(results[3].Output, wantFailurePark("read_transcript")) {
		t.Fatalf("fourth neutral-default variant was not parked: %q", results[3].Output)
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

	if streak, _, _ := r.breaker.check("read_transcript", []byte(`{"transcript_ref":"job:job_a"}`)); streak != 1 {
		t.Fatalf("A's run after its own success = %d, want 1", streak)
	}
	if streak, _, _ := r.breaker.check("read_transcript", []byte(`{"transcript_ref":"job:job_b"}`)); streak != 2 {
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

func TestFailureFingerprint_NeutralDefaultsAreEquivalent(t *testing.T) {
	want := fp("read_transcript", `{"transcript_ref":"job:j1"}`)
	for _, args := range []string{
		`{"transcript_ref":"job:j1","range":"","output_match":""}`,
		`{"transcript_ref":"job:j1","format":null,"extra":{},"list":[]}`,
	} {
		if got := fp("read_transcript", args); got != want {
			t.Errorf("fingerprint(%s) = %q, want %q", args, got, want)
		}
	}
	// Empty and absent arguments are the same call.
	if fp("read_transcript", ``) != fp("read_transcript", `{}`) {
		t.Errorf("empty and {} must fingerprint the same")
	}
}

// Explicit numeric zero is presence, not omission. read_transcript treats a
// present offset_bytes as the retained-page operation even when it is zero (see
// TestNormalizeRetainedReadArgsPreservesExplicitZeroOffset), so folding zero
// into the omitted form would hide a meaningful correction and could park a
// call the model legitimately changed. A value-based rule cannot know a
// schema's numeric defaults, so it errs toward a distinct fingerprint.
func TestFailureFingerprint_ExplicitZeroIsDistinct(t *testing.T) {
	base := fp("read_transcript", `{"transcript_ref":"job:j1"}`)
	for _, args := range []string{
		`{"transcript_ref":"job:j1","offset_bytes":0}`,
		`{"transcript_ref":"job:j1","context_lines":0}`,
		`{"transcript_ref":"job:j1","expand_turn":0}`,
	} {
		if got := fp("read_transcript", args); got == base {
			t.Errorf("fingerprint(%s) = %q, must differ from the omitted form", args, got)
		}
	}
	if fp("read_file", `{"offset_bytes":0}`) == fp("read_file", `{}`) {
		t.Error("explicit offset_bytes=0 must not fingerprint as omitted")
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
// calls whose raw arguments differ only by neutral defaults are the same
// semantic operation but not a byte-identical repeat, so a caller comparing
// their bodies must see the unmodified result. This is the shape
// TestRegistryExecuteCallNormalizesMaterializedRetainedDefaults exercises one
// layer up.
func TestBreakerDispatch_NeutralDefaultsDoNotTriggerRepetitionNudge(t *testing.T) {
	const body = "ready\n"
	r := NewRegistry()
	fake := registerBreakerFake(t, r, "read_transcript", func(int) (any, error) { return body, nil })
	env := breakerEnv(t)
	ctx := context.Background()

	omitted := r.ExecuteCall(ctx, env, breakerCall("o1", "read_transcript", `{"transcript_ref":"job:job_1"}`))
	if omitted.Output != body {
		t.Fatalf("omitted output = %q, want %q", omitted.Output, body)
	}
	materialized := r.ExecuteCall(ctx, env, breakerCall("m1", "read_transcript", `{"transcript_ref":"job:job_1","range":"","output_match":""}`))
	if fake.calls != 2 {
		t.Fatalf("materialized defaults must dispatch: invocations = %d, want 2", fake.calls)
	}
	if strings.Contains(materialized.Output, repetitionNudgeMarker) {
		t.Fatalf("neutral defaults triggered the exact-call repetition nudge: %q", materialized.Output)
	}
	if materialized.Output != body {
		t.Fatalf("materialized output = %q, want the unmodified %q", materialized.Output, body)
	}
}
