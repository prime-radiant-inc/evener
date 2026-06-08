package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// spawnFinalizedUnconsumedChild spawns a non-blocking child that completes
// immediately, then blocks until the run finalizes WITHOUT consuming the result
// (it polls running rather than calling wait). Returns the child's agent_id. This
// is the precondition the non-consuming peek test needs: a finalized result that
// no wait has consumed yet.
func spawnFinalizedUnconsumedChild(t *testing.T, sess *Session) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spawnRes := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "spawn",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do work"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))

	// Block until the child's run finalizes WITHOUT consuming the result. We poll the
	// tracked sub's running flag (channel-free, deterministic: the run goroutine sets
	// running=false under sub.mu before closing done).
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatalf("child %q not tracked after spawn", agentID)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		sub.mu.Lock()
		running := sub.running
		sub.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not finalize in time")
		}
		time.Sleep(time.Millisecond)
	}
	return agentID
}

// oneStepSession builds a Session whose fake adapter completes the child's first
// run with finalMessage. StateDir is set so transcript views can resolve refs.
func oneStepSession(t *testing.T, finalMessage string) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse(finalMessage) },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
		StateDir:         dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func callSubagentOutput(t *testing.T, sess *Session, argsJSON string) tool.ExecResult {
	t.Helper()
	return sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "out",
		Name:      "subagent_output",
		Arguments: json.RawMessage(argsJSON),
	})
}

// TestSubagentOutput_ResultIsNonConsuming is the load-bearing non-consuming guard:
// a view=result peek must NOT consume the child's result, so a FOLLOWING wait still
// returns and consumes it. If execSubagentOutput set resultConsumed, the wait would
// instead return the "already consumed" error and this test would fail.
func TestSubagentOutput_ResultIsNonConsuming(t *testing.T) {
	sess := oneStepSession(t, "child findings here")
	agentID := spawnFinalizedUnconsumedChild(t, sess)

	// Peek via view=result.
	peek := callSubagentOutput(t, sess, fmt.Sprintf(`{"agent_id":%q,"view":"result"}`, agentID))
	if peek.IsError {
		t.Fatalf("subagent_output result error: %s", peek.Output)
	}
	var peeked subagentOutputResult
	if err := json.Unmarshal([]byte(peek.Output), &peeked); err != nil {
		t.Fatalf("unmarshal peek: %v (output: %s)", err, peek.Output)
	}
	if !strings.Contains(peeked.Content, "child findings here") {
		t.Fatalf("peek did not return the snapshot output; content: %s", peeked.Content)
	}

	// A following wait MUST still return the result (proving the peek did not consume).
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "w1",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait after peek must succeed (peek must be non-consuming); got error: %s", waitRes.Output)
	}
	var waited subagentResult
	if err := json.Unmarshal([]byte(waitRes.Output), &waited); err != nil {
		t.Fatalf("unmarshal wait: %v (output: %s)", err, waitRes.Output)
	}
	if waited.Status != SubagentCompleted || !strings.Contains(waited.Output, "child findings here") {
		t.Fatalf("wait did not return the completed result: %+v", waited)
	}

	// And wait DID consume: a second wait now reports already-consumed. This proves
	// the first peek's non-consumption is meaningful (consumption really happens, just
	// not in the peek).
	waitRes2 := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "w2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if !waitRes2.IsError {
		t.Fatalf("second wait should report already-consumed; got: %s", waitRes2.Output)
	}
	if !strings.Contains(waitRes2.Output, "already consumed") {
		t.Fatalf("second wait error should mention already consumed; got: %s", waitRes2.Output)
	}
}

// TestSubagentOutput_XORValidation: both → error, neither → error, exactly one → ok.
func TestSubagentOutput_XORValidation(t *testing.T) {
	sess := oneStepSession(t, "done")
	agentID := spawnFinalizedUnconsumedChild(t, sess)

	// Neither: error.
	if res := callSubagentOutput(t, sess, `{"view":"result"}`); !res.IsError {
		t.Fatalf("neither agent_id nor transcript_ref must error; got: %s", res.Output)
	}

	// Both: error.
	both := fmt.Sprintf(`{"agent_id":%q,"transcript_ref":"local:%s","view":"result"}`, agentID, sess.id)
	if res := callSubagentOutput(t, sess, both); !res.IsError {
		t.Fatalf("both agent_id and transcript_ref must error; got: %s", res.Output)
	}

	// Exactly one (agent_id): ok.
	if res := callSubagentOutput(t, sess, fmt.Sprintf(`{"agent_id":%q,"view":"result"}`, agentID)); res.IsError {
		t.Fatalf("exactly one (agent_id) must succeed; got error: %s", res.Output)
	}
}

// TestSubagentOutput_MaxBytesTruncatesAfterRedaction proves redaction runs BEFORE
// truncation, where the cut lands MID-SECRET. The token is an unanchored sk- key
// whose secret TAIL is a long run of 'Z's; the standard sk- rule keeps the legible
// "sk-" prefix and masks the tail. max_bytes is calibrated (via a redaction:none
// probe) so the cut falls a few characters into the tail. Redact-first masks the
// whole tail to the marker, so no run of 'Z's reaches the output. If truncation ran
// first, the kept prefix would end in "sk-ZZZZ" — the fragment is below the sk-
// rule's 8-char threshold, so redaction would miss it and the 'Z' run would leak.
// Asserting the absence of a 'ZZZ' run therefore fails iff the ordering is inverted.
func TestSubagentOutput_MaxBytesTruncatesAfterRedaction(t *testing.T) {
	secret := "sk-" + strings.Repeat("Z", 64)
	final := "report key " + secret + " " + strings.Repeat("x", 4096)
	sess := oneStepSession(t, final)
	agentID := spawnFinalizedUnconsumedChild(t, sess)

	// Probe: full UNREDACTED content to find the true byte offset of the token. A
	// large max_bytes keeps everything; redaction:none leaves the token in place.
	probe := callSubagentOutput(t, sess, fmt.Sprintf(`{"agent_id":%q,"view":"result","redaction":"none","max_bytes":1000000}`, agentID))
	if probe.IsError {
		t.Fatalf("probe error: %s", probe.Output)
	}
	var probed subagentOutputResult
	if err := json.Unmarshal([]byte(probe.Output), &probed); err != nil {
		t.Fatalf("unmarshal probe: %v (output: %s)", err, probe.Output)
	}
	skIdx := strings.Index(probed.Content, "sk-")
	if skIdx < 0 {
		t.Fatalf("probe content should contain the unredacted token; content: %s", probed.Content)
	}
	// Cut four characters into the tail → "sk-ZZZZ" in a truncate-first prefix, below
	// the sk- rule's 8-char threshold (so it would leak under the wrong ordering).
	maxBytes := skIdx + len("sk-") + 4

	res := callSubagentOutput(t, sess, fmt.Sprintf(`{"agent_id":%q,"view":"result","redaction":"standard","max_bytes":%d}`, agentID, maxBytes))
	if res.IsError {
		t.Fatalf("subagent_output error: %s", res.Output)
	}
	var out subagentOutputResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if !out.Truncated {
		t.Fatalf("expected truncated=true for an oversized payload; out: %+v", out)
	}
	if len(out.Content) > maxBytes {
		t.Fatalf("content exceeds max_bytes: len=%d max=%d", len(out.Content), maxBytes)
	}
	// Load-bearing: redact-first masks the whole tail before the cut, so no run of
	// 'Z's survives. Truncate-first would leak "sk-ZZZZ".
	if strings.Contains(out.Content, "ZZZ") {
		t.Fatalf("secret tail leaked (redaction must run before truncation): %s", out.Content)
	}
	// The legible "sk-" prefix and the masking marker both land before the cut,
	// confirming redaction ran on the retained region (not that the region was empty).
	if !strings.Contains(out.Content, "«") {
		t.Fatalf("expected the redaction marker's opening rune in the kept prefix; content: %s", out.Content)
	}
}

// TestSubagentOutput_RedactsEnvDumpEndToEnd is the end-to-end regression guard for
// the canonical leak vector: a child whose result text is a realistic multi-line env
// dump (the kind a child running `env`/`printenv` or echoing a JSON API-error body
// would return). Peeking it via the real subagent_output{view:"result"} tool with
// DEFAULT (standard) redaction must mask every secret value. It exercises the full
// path: result snapshot → JSON marshal → redact → tool output.
func TestSubagentOutput_RedactsEnvDumpEndToEnd(t *testing.T) {
	// Distinctive secret bodies, one per credential class the redactor must cover.
	const (
		openAISecret = "sk-EnvDumpProj0123456789abcdef"
		githubSecret = "ghp_EnvDumpToken0123456789ABCDEF"
		awsSecret    = "AKIAIOSFODNN7EXAMPLE"
		urlPwSecret  = "envDumpDbPw"
		stripeSecret = "StripeSecretBody0123456789"
		bearerSecret = "EnvDumpBearerSig123abc"
	)
	envDump := strings.Join([]string{
		"PATH=/usr/local/bin:/usr/bin",
		"OPENAI_API_KEY=" + openAISecret,
		"GITHUB_TOKEN=" + githubSecret,
		"AWS_ACCESS_KEY_ID=" + awsSecret,
		"DATABASE_URL=postgres://dbuser:" + urlPwSecret + "@db.host:5432/app",
		"STRIPE_SECRET_KEY=" + stripeSecret,
		`error body: {"authorization":"Bearer ` + bearerSecret + `"}`,
		"HOME=/root",
	}, "\n")

	sess := oneStepSession(t, envDump)
	agentID := spawnFinalizedUnconsumedChild(t, sess)

	// Peek via the real tool with DEFAULT redaction (no redaction arg → standard).
	peek := callSubagentOutput(t, sess, fmt.Sprintf(`{"agent_id":%q,"view":"result"}`, agentID))
	if peek.IsError {
		t.Fatalf("subagent_output result error: %s", peek.Output)
	}
	var peeked subagentOutputResult
	if err := json.Unmarshal([]byte(peek.Output), &peeked); err != nil {
		t.Fatalf("unmarshal peek: %v (output: %s)", err, peek.Output)
	}
	if peeked.Redaction != "standard" {
		t.Fatalf("default redaction must be standard; got %q", peeked.Redaction)
	}

	// None of the secret values may appear ANYWHERE in the tool output (content or
	// envelope). Checking the raw peek.Output is the strongest guard.
	for _, secret := range []string{openAISecret, githubSecret, awsSecret, urlPwSecret, stripeSecret, bearerSecret} {
		if strings.Contains(peek.Output, secret) {
			t.Fatalf("env-dump secret %q leaked through subagent_output:\n%s", secret, peek.Output)
		}
	}
	// Sanity: redaction actually ran (marker present) and legible env names survive.
	if !strings.Contains(peeked.Content, redactMarker) {
		t.Fatalf("expected redaction marker in content; got: %s", peeked.Content)
	}
	for _, legible := range []string{"OPENAI_API_KEY", "DATABASE_URL", "postgres://", "dbuser"} {
		if !strings.Contains(peeked.Content, legible) {
			t.Fatalf("redaction destroyed legible %q; content: %s", legible, peeked.Content)
		}
	}
}

// TestSubagentOutput_ChildCannotCall asserts subagent_output is root-only: it is in
// the root-only set and a depth>0 child's registry must not expose it.
func TestSubagentOutput_ChildCannotCall(t *testing.T) {
	if !isRootOnlyAgentManagementTool("subagent_output") {
		t.Fatal("subagent_output must be a root-only agent-management tool")
	}

	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	if child.reg.Get("subagent_output") != nil {
		t.Fatal("depth>0 child must not have subagent_output registered")
	}
}

// TestSubagentOutput_TranscriptViewDelegatesAndRedacts covers the transcript path:
// view=outline on a tracked child returns a rendered (redacted) outline, and a
// bare transcript_ref call works even when the sub is not tracked.
func TestSubagentOutput_TranscriptViewDelegatesAndRedacts(t *testing.T) {
	sess := oneStepSession(t, "child outline body")
	agentID := spawnFinalizedUnconsumedChild(t, sess)
	childRef := "local:" + sess.getSub(agentID).sess.ID()

	// 1. view=outline via agent_id: delegates to the renderer, returns content.
	res := callSubagentOutput(t, sess, fmt.Sprintf(`{"agent_id":%q,"view":"outline"}`, agentID))
	if res.IsError {
		t.Fatalf("subagent_output outline error: %s", res.Output)
	}
	var out subagentOutputResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res.Output)
	}
	if out.View != "outline" {
		t.Fatalf("view = %q, want outline", out.View)
	}
	if out.Content == "" {
		t.Fatalf("outline content must be non-empty (renderer delegation); out: %+v", out)
	}
	if out.Redaction != "standard" {
		t.Fatalf("default redaction must be standard; got %q", out.Redaction)
	}

	// 2. Bare transcript_ref (no agent_id), sub need not be tracked: still reads.
	res2 := callSubagentOutput(t, sess, fmt.Sprintf(`{"transcript_ref":%q,"view":"outline"}`, childRef))
	if res2.IsError {
		t.Fatalf("transcript_ref-only outline error: %s", res2.Output)
	}
	var out2 subagentOutputResult
	if err := json.Unmarshal([]byte(res2.Output), &out2); err != nil {
		t.Fatalf("unmarshal: %v (output: %s)", err, res2.Output)
	}
	if out2.Content == "" {
		t.Fatalf("transcript_ref-only outline content must be non-empty; out: %+v", out2)
	}
	if out2.TranscriptRef != childRef {
		t.Fatalf("transcript_ref echoed = %q, want %q", out2.TranscriptRef, childRef)
	}
}
