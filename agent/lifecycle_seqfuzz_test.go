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
	"time"

	"pgregory.net/rapid"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/fuzz/promoter"
	"primeradiant.com/serf/llm"
)

// TestLifecycleSeqFuzz is roadmap item 8.3's first stateful fuzz of the agent
// turn lifecycle. A rapid state machine draws sequences of lifecycle operations
// — ProcessInput (with the model's per-round responses scripted by the fuzzer),
// interrupt, steer, enqueue, follow-up, goal set/clear, force-compaction, a
// fake-clock advance, close, and observe — and replays each against a REAL
// agent.Session built entirely offline: a deny exec env (no FS/process/network),
// an injected fake clock (all timers/sleeps are virtual), and a scripted LLM
// provider. Oracles are checked weakest-first:
//
//	Oracle 1 (panic):     no sequence panics (each op runs under a recovering
//	                      goroutine; the turn loop re-panics rather than swallowing).
//	Oracle 2 (wedge):     every op returns under a real wall-time watchdog (distinct
//	                      from the in-session fake clock) — a hang is a wedge.
//	Oracle 3 (status):    State() is observed only at op boundaries, where it must be
//	                      idle or closed, and once closed it stays closed.
//	Oracle 4 (counters):  the turns and modelResponses counters never decrease.
//	Oracle 5 (transcript): history stays well-formed (no orphaned tool call) and
//	                      len(history) never decreases except across a compaction —
//	                      the one sanctioned shrink.
//
// SCOPE (honest): v1 drives the single-session lifecycle including foreground
// shell jobs (deny-env backed, on the fake clock), forced compaction, and the
// goal store. Delegate/subagent spawning is OUT of v1: a spawned child session
// reuses the parent's llm.Client and therefore its ScriptedAdapter, so a child
// turn running on its own goroutine would race the parent's Responder draw
// sequence — the exact concurrency hazard the design flags. Driving subagents
// deterministically needs a per-child adapter seam; it is a scoped follow-up.
// Background shell jobs are likewise deferred (their async finalize goroutine is
// not yet wired into the quiescence handshake). What is in scope is already a
// large, previously-unfuzzed surface, and it is fully deterministic.
//
// A discovered failure is routed through fuzz/promoter (mirroring the Phase-2
// appwire target): a deterministic reproduction survives the flake-guard and is
// emitted to a temp dir as a regression test; a flaky one is quarantined.
func TestLifecycleSeqFuzz(t *testing.T) {
	adapter := &lifecycleAdapter{emitDir: t.TempDir()}
	store, err := promoter.OpenBucketStore(filepath.Join(t.TempDir(), "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	promo := promoter.New(adapter, store, quietLifecycleQuarantiner{}, 5)

	var captured *promoter.Failure
	t.Cleanup(func() {
		if captured == nil {
			return
		}
		out, err := promo.Promote(context.Background(), *captured)
		t.Logf("lifecycle-fuzz failure promoted: outcome=%v err=%v detail=%q", out, err, captured.Detail)
	})

	rapid.Check(t, func(rt *rapid.T) {
		art := drawLifecycleArtifact(rt)
		if f := lifecycleOracleRun(art); f != nil {
			captured = f
			rt.Fatalf("lifecycle oracle: %s", f.Detail)
		}
	})
}

// --- response vocabulary ---

// responseKind enumerates the model behaviors the fuzzer scripts per round.
type responseKind int

const (
	kindFinal    responseKind = iota // communicate end_turn=true (terminal)
	kindAwait                        // communicate end_turn=false (awaits reply)
	kindText                         // bare assistant text, no tool calls
	kindEmpty                        // empty response (null content)
	kindPause                        // pause_turn finish reason
	kindReadFile                     // tool call: read_file
	kindGlob                         // tool call: glob
	kindGrep                         // tool call: grep
	kindShell                        // tool call: shell (foreground)
	kindCompact                      // tool call: compact (forces history shrink)
	numResponseKinds
)

// drawResponseKind samples a non-terminal-biased response kind. The terminal
// kinds are reachable too; the Responder always falls back to Final once a
// script is exhausted, so a turn never runs unbounded.
func drawResponseKind(rt *rapid.T) int {
	return rapid.IntRange(0, int(numResponseKinds)-1).Draw(rt, "respKind")
}

// buildResponse renders one scripted response. It is a pure function of the
// kind, so replay from a recorded script is identical.
func buildResponse(kind responseKind, callSeq int) llm.Response {
	id := "call_" + strconv.Itoa(callSeq)
	switch kind {
	case kindFinal:
		return agenttest.FinalResponse("done")
	case kindAwait:
		return agenttest.CommunicateResponse(false, "awaiting")
	case kindText:
		return llm.Response{Message: llm.Assistant("thinking " + strconv.Itoa(callSeq))}
	case kindEmpty:
		return agenttest.EmptyResponse()
	case kindPause:
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant},
			Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn},
		}
	case kindReadFile:
		return lifecycleToolCall(id, "read_file", map[string]any{"file_path": "f" + strconv.Itoa(callSeq) + ".txt"})
	case kindGlob:
		return lifecycleToolCall(id, "glob", map[string]any{"pattern": "*.go"})
	case kindGrep:
		return lifecycleToolCall(id, "grep", map[string]any{"pattern": "x", "path": "."})
	case kindShell:
		return lifecycleToolCall(id, "shell", map[string]any{"command": "echo " + strconv.Itoa(callSeq)})
	case kindCompact:
		return lifecycleToolCall(id, "compact", map[string]any{"instructions": "tighten"})
	case kindBoom:
		return lifecycleToolCall(id, lifecycleBoomTool, map[string]any{})
	default:
		return agenttest.FinalResponse("done")
	}
}

// kindBoom calls the injected panicking fixture tool. It is past the drawn range
// (drawResponseKind never produces it), so the live fuzzer never emits it; only
// the determinism tests script it, paired with the panicTool injection.
const kindBoom responseKind = numResponseKinds

const lifecycleBoomTool = "boom"

func lifecycleToolCall(id, name string, args map[string]any) llm.Response {
	raw, _ := json.Marshal(args)
	return agenttest.ToolCallResponse(llm.ToolCallData{ID: id, Name: name, Arguments: raw, Type: "function"})
}

// --- the op model (declarative, mirroring Phase 2) ---

type lifecycleOpCode int

const (
	opProcessInput lifecycleOpCode = iota
	opProcessInterrupted
	opSteer
	opEnqueue
	opFollowUp
	opSetGoal
	opClearGoal
	opAdvanceClock
	opObserve
	opClose
)

var allLifecycleOps = []lifecycleOpCode{
	opProcessInput, opProcessInterrupted, opSteer, opEnqueue, opFollowUp,
	opSetGoal, opClearGoal, opAdvanceClock, opObserve, opClose,
}

// opContainsCompact reports whether the op's scripted responses include a
// force-compaction, the one boundary at which history may shrink.
func (r opRecord) opContainsCompact() bool {
	for _, k := range r.Script {
		if responseKind(k) == kindCompact {
			return true
		}
	}
	return false
}

// opRecord is one drawn operation with every parameter it needs, so the artifact
// is self-contained and a replay never touches rapid.
type opRecord struct {
	Code   int    `json:"c"`
	Script []int  `json:"s,omitempty"` // per-round response kinds (ProcessInput family)
	Text   string `json:"t,omitempty"` // input/steer/followup/goal text
	Dur    int64  `json:"d,omitempty"` // advance duration (ns) for opAdvanceClock
	IntAt  int    `json:"i,omitempty"` // round at which to cancel ctx (interrupt)
}

// lifecycleArtifact is the minimized reproducer: the op sequence plus the env
// seed. rapid minimizes it already (Minimize is a passthrough).
type lifecycleArtifact struct {
	Ops     []opRecord `json:"ops"`
	EnvSeed uint64     `json:"seed"`
}

var inputTexts = []string{"hello", "do x", "please", "step 2", "??", "résumé"}

func drawLifecycleArtifact(rt *rapid.T) lifecycleArtifact {
	n := rapid.IntRange(1, 24).Draw(rt, "nops")
	ops := make([]opRecord, 0, n)
	for i := 0; i < n; i++ {
		code := rapid.SampledFrom(allLifecycleOps).Draw(rt, "op")
		rec := opRecord{Code: int(code)}
		switch code {
		case opProcessInput, opProcessInterrupted:
			scriptLen := rapid.IntRange(1, 5).Draw(rt, "scriptLen")
			rec.Script = make([]int, scriptLen)
			for j := range rec.Script {
				rec.Script[j] = drawResponseKind(rt)
			}
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
			if code == opProcessInterrupted {
				rec.IntAt = rapid.IntRange(0, scriptLen).Draw(rt, "intAt")
			}
		case opSteer, opEnqueue, opFollowUp, opSetGoal:
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
		case opAdvanceClock:
			rec.Dur = int64(rapid.IntRange(0, int(5*time.Minute)).Draw(rt, "durNS"))
		}
		ops = append(ops, rec)
	}
	return lifecycleArtifact{
		Ops:     ops,
		EnvSeed: uint64(rapid.Int64().Draw(rt, "envSeed")),
	}
}

// --- the oracle run (single source of truth: live property and Replay share it) ---

// responderHardCap bounds the rounds any single turn can run regardless of the
// drawn script, so a degenerate script (all non-terminal) cannot loop forever.
const responderHardCap = 16

// lifecycleCallTimeout is the real wall-time wedge bound for one op. The fake
// adapter is instant and all in-session time is virtual, so a real expiry means
// a genuine hang. Generous so a slow CI box never false-positives.
const lifecycleCallTimeout = 10 * time.Second

// lifecycleModel is the monotonic prediction the oracles check against.
type lifecycleModel struct {
	closed        bool
	prevTurns     int
	prevModelResp int
	prevHistLen   int
}

// lifecycleInject configures a fault for the determinism tests. The zero value
// (the live target) injects nothing.
type lifecycleInject struct {
	panicTool bool // register the boom fixture so a boom tool call panics
}

func lifecycleOracleRun(art lifecycleArtifact) *promoter.Failure {
	return lifecycleOracleRunInjected(art, lifecycleInject{})
}

func lifecycleOracleRunInjected(art lifecycleArtifact, inj lifecycleInject) *promoter.Failure {
	clk := agenttest.NewFakeClock()
	env := &agenttest.DenyEnv{WorkDir: lifecycleWorkDir, Seed: art.EnvSeed}

	var script []int
	var callSeq int
	var cancelAt int    // -1 disables interrupt cancellation
	var cancelFn func() // set per interrupt op
	cancelAt = -1

	responder := func(req llm.Request) llm.Response {
		idx := callSeq
		callSeq++
		if cancelAt >= 0 && idx == cancelAt && cancelFn != nil {
			cancelFn()
		}
		if idx >= responderHardCap {
			return agenttest.FinalResponse("done")
		}
		if idx < len(script) {
			return buildResponse(responseKind(script[idx]), idx)
		}
		return agenttest.FinalResponse("done")
	}

	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: responder}
	client := llm.NewClient()
	client.Register(adapter)
	profile := NewOpenAIProfile("gpt-5.2")
	cfg := SessionConfig{
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		LLMSleep:              func(_ context.Context, d time.Duration) error { clk.Sleep(d); return nil },
	}
	sess, err := NewSession(client, profile, env, cfg)
	if err != nil {
		return &promoter.Failure{Surface: lifecycleSurface, Oracle: promoter.Invariant, Detail: "NewSession: " + err.Error(), Artifact: lifecycleJSON(art)}
	}
	if inj.panicTool {
		_ = sess.reg.Register(tool.RegisteredTool{
			Tool: llm.Tool{Definition: llm.ToolDefinition{
				Name:        lifecycleBoomTool,
				Description: "fixture tool whose handler panics; determinism test only",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				panic("lifecycle-fuzz boom")
			},
		})
	}

	// Drain events for the whole run so a turn emitting >256 events never blocks.
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	defer func() {
		sess.Close()
		<-drainDone
	}()

	model := &lifecycleModel{prevHistLen: snapshotHistoryLen(sess)}

	for i, op := range art.Ops {
		// Reset per-op responder state.
		script = op.Script
		callSeq = 0
		cancelAt = -1
		cancelFn = nil

		res := runOpSafely(sess, clk, op, &cancelAt, &cancelFn)
		if res.wedged {
			return lifecycleFailure(promoter.Wedge, art, i, "wedge:"+opName(lifecycleOpCode(op.Code)))
		}
		if res.panicked {
			return lifecyclePanicFailure(art, i, op, res)
		}
		if f := checkLifecycleOracles(sess, model, op, art, i); f != nil {
			return f
		}
	}
	return nil
}

const (
	lifecycleSurface = "agent-lifecycle-seq"
	lifecycleWorkDir = "/serf-fuzz-nonexistent-workdir"
)

// lifecycleOpResult carries an op's outcome: completed, panicked (recovered), or wedged.
type lifecycleOpResult struct {
	panicked bool
	panicVal any
	stack    []string
	wedged   bool
}

// runOpSafely applies one op under a recovering goroutine and a real wall-time
// watchdog (Oracles 1 and 2). It never lets a panic or hang escape.
func runOpSafely(sess *Session, clk *agenttest.FakeClock, op opRecord, cancelAt *int, cancelFn *func()) lifecycleOpResult {
	done := make(chan lifecycleOpResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- lifecycleOpResult{panicked: true, panicVal: r, stack: captureLifecycleStack()}
			}
		}()
		applyOp(sess, clk, op, cancelAt, cancelFn)
		done <- lifecycleOpResult{}
	}()
	select {
	case res := <-done:
		return res
	case <-time.After(lifecycleCallTimeout):
		return lifecycleOpResult{wedged: true}
	}
}

func applyOp(sess *Session, clk *agenttest.FakeClock, op opRecord, cancelAt *int, cancelFn *func()) {
	switch lifecycleOpCode(op.Code) {
	case opProcessInput:
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
		defer cancel()
		_, _ = sess.ProcessInput(ctx, op.Text, nil)
	case opProcessInterrupted:
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		*cancelAt = op.IntAt
		*cancelFn = cancel
		_, _ = sess.ProcessInput(ctx, op.Text, nil)
	case opSteer:
		sess.Steer(op.Text)
	case opEnqueue:
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
		defer cancel()
		_ = sess.Enqueue(ctx, op.Text)
	case opFollowUp:
		sess.FollowUp(op.Text)
	case opSetGoal:
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
		defer cancel()
		_, _ = sess.SetGoal(ctx, op.Text)
	case opClearGoal:
		sess.ClearGoal()
	case opAdvanceClock:
		// Advance virtual time so any armed single-session timer (the job
		// notification retry, etc.) fires. No timers are required to be parked,
		// so this is safe even when the count is zero.
		clk.Advance(time.Duration(op.Dur))
	case opObserve:
		_ = sess.State()
		_ = sess.QueueDepth()
		_, _, _ = sess.GoalStatus()
	case opClose:
		sess.Close()
	}
}

func checkLifecycleOracles(sess *Session, m *lifecycleModel, op opRecord, art lifecycleArtifact, step int) *promoter.Failure {
	st := sess.State()

	// Oracle 3: status is observed only at boundaries; must be idle or closed,
	// and once closed it stays closed.
	if m.closed && st != SessionClosed {
		return lifecycleFailure(promoter.Invariant, art, step, "status-regression:closed->"+string(st))
	}
	if st == SessionClosed {
		m.closed = true
	} else if st != SessionIdle {
		return lifecycleFailure(promoter.Invariant, art, step, "status-nonboundary:"+string(st))
	}

	sess.mu.Lock()
	turns := sess.turns
	modelResp := sess.modelResponses
	histLen := len(sess.history)
	histCopy := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()

	// Oracle 4: the turns and modelResponses counters never decrease.
	if turns < m.prevTurns {
		return lifecycleFailure(promoter.Invariant, art, step, fmt.Sprintf("turns-regressed:%d->%d", m.prevTurns, turns))
	}
	if modelResp < m.prevModelResp {
		return lifecycleFailure(promoter.Invariant, art, step, fmt.Sprintf("modelResponses-regressed:%d->%d", m.prevModelResp, modelResp))
	}

	// Oracle 5: history stays well-formed, and len(history) never decreases
	// except across a compaction (the one sanctioned shrink).
	if _, repairs := repairOrphanedToolResults(histCopy); repairs != 0 {
		return lifecycleFailure(promoter.Invariant, art, step, fmt.Sprintf("orphaned-tool-results:%d", repairs))
	}
	if histLen < m.prevHistLen && !op.opContainsCompact() {
		return lifecycleFailure(promoter.Invariant, art, step, fmt.Sprintf("history-shrank-without-compaction:%d->%d", m.prevHistLen, histLen))
	}

	m.prevTurns = turns
	m.prevModelResp = modelResp
	m.prevHistLen = histLen
	return nil
}

func snapshotHistoryLen(sess *Session) int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return len(sess.history)
}

// --- promoter wiring (mirrors the Phase-2 seqAdapter) ---

type lifecycleAdapter struct {
	emitDir string
	inject  lifecycleInject
}

func (a *lifecycleAdapter) Minimize(f promoter.Failure) promoter.Failure { return f }

func (a *lifecycleAdapter) Signature(f promoter.Failure) promoter.Signature {
	key := f.Detail
	if f.Oracle == promoter.Panic && len(f.Stack) > 0 {
		key = strings.Join(topLifecycleFrames(f.Stack, 4), "|")
	}
	if key == "" {
		key = promoter.ShortHash(f)
	}
	return promoter.Signature{Oracle: f.Oracle, Key: key}
}

func (a *lifecycleAdapter) Replay(_ context.Context, f promoter.Failure) (bool, bool) {
	var art lifecycleArtifact
	if err := json.Unmarshal(f.Artifact, &art); err != nil {
		return false, false
	}
	repro := lifecycleOracleRunInjected(art, a.inject)
	if repro == nil {
		return false, false
	}
	return true, a.Signature(*repro) == a.Signature(f)
}

func (a *lifecycleAdapter) Emit(f promoter.Failure) (string, error) {
	return promoter.WriteGoTest(a.emitDir, promoter.GoTest{
		Package:    "agent",
		Surface:    f.Surface,
		Oracle:     f.Oracle,
		Signature:  a.Signature(f).String(),
		Seam:       "agent.Session turn/job/goal lifecycle",
		Hash:       promoter.ShortHash(f),
		ReplayBody: "\treplayLifecycleArtifact(t, " + strconv.Quote(string(f.Artifact)) + ")",
	})
}

// replayLifecycleArtifact is the body an emitted regression test runs: it
// replays the recorded op sequence against the live config and asserts the
// lifecycle no longer fails.
func replayLifecycleArtifact(t *testing.T, artifact string) {
	t.Helper()
	var art lifecycleArtifact
	if err := json.Unmarshal([]byte(artifact), &art); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	if f := lifecycleOracleRun(art); f != nil {
		t.Fatalf("lifecycle oracle still fails on recorded artifact: %s", f.Detail)
	}
}

// --- failure constructors / helpers ---

func lifecycleFailure(oracle promoter.OracleTag, art lifecycleArtifact, step int, detail string) *promoter.Failure {
	return &promoter.Failure{
		Surface:  lifecycleSurface,
		Oracle:   oracle,
		Detail:   detail + fmt.Sprintf(":step=%d", step),
		Artifact: lifecycleJSON(art),
	}
}

func lifecyclePanicFailure(art lifecycleArtifact, step int, op opRecord, res lifecycleOpResult) *promoter.Failure {
	return &promoter.Failure{
		Surface:  lifecycleSurface,
		Oracle:   promoter.Panic,
		Stack:    res.stack,
		Detail:   "panic:" + opName(lifecycleOpCode(op.Code)) + ":" + firstLifecycleLine(fmt.Sprint(res.panicVal)),
		Artifact: lifecycleJSON(art),
	}
}

func lifecycleJSON(art lifecycleArtifact) json.RawMessage {
	b, _ := json.Marshal(art)
	return b
}

func opName(c lifecycleOpCode) string {
	switch c {
	case opProcessInput:
		return "process_input"
	case opProcessInterrupted:
		return "process_interrupted"
	case opSteer:
		return "steer"
	case opEnqueue:
		return "enqueue"
	case opFollowUp:
		return "followup"
	case opSetGoal:
		return "set_goal"
	case opClearGoal:
		return "clear_goal"
	case opAdvanceClock:
		return "advance_clock"
	case opObserve:
		return "observe"
	case opClose:
		return "close"
	default:
		return "op(" + strconv.Itoa(int(c)) + ")"
	}
}

func firstLifecycleLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func topLifecycleFrames(frames []string, n int) []string {
	if len(frames) < n {
		return frames
	}
	return frames[:n]
}

func captureLifecycleStack() []string {
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

type quietLifecycleQuarantiner struct{}

func (quietLifecycleQuarantiner) Quarantine(promoter.Failure, int) error { return nil }

// compile-time assertion that the adapter satisfies the promoter contract.
var _ promoter.Adapter = (*lifecycleAdapter)(nil)
var _ clock.Clock = (*agenttest.FakeClock)(nil)

// TestLifecycleAdapter_PromotesDeterministicFailure exercises the promoter
// Adapter end-to-end against the REAL promoter using a deterministic panic: a
// fixture tool ("boom") whose handler panics, scripted as the model's first
// response. The turn loop re-panics rather than swallowing, the harness's
// recovering op-goroutine catches it and classifies it Panic, it survives the
// flake-guard, a regression test is emitted, its bucket is recorded, and the
// second sighting dedups. This proves the four hooks wire up without depending
// on the live fuzzer finding a real bug. The panicking tool is a fixture (it
// locks in that ProcessInput propagates a tool-handler panic to the caller), not
// a claim about serf production code.
func TestLifecycleAdapter_PromotesDeterministicFailure(t *testing.T) {
	inject := lifecycleInject{panicTool: true}
	art := lifecycleArtifact{
		Ops:     []opRecord{{Code: int(opProcessInput), Script: []int{int(kindBoom)}, Text: "go"}},
		EnvSeed: 1,
	}

	f := lifecycleOracleRunInjected(art, inject)
	if f == nil {
		t.Fatal("expected a panic failure when a tool handler panics")
	}
	if f.Oracle != promoter.Panic {
		t.Fatalf("oracle = %v, want Panic", f.Oracle)
	}

	adapter := &lifecycleAdapter{emitDir: t.TempDir(), inject: inject}
	store, err := promoter.OpenBucketStore(filepath.Join(t.TempDir(), "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	q := &countingLifecycleQuarantiner{}
	promo := promoter.New(adapter, store, q, 5)

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

	// A clean artifact replays without a failure (the seam does not panic).
	clean, _ := json.Marshal(lifecycleArtifact{
		Ops: []opRecord{{Code: int(opProcessInput), Script: []int{int(kindFinal)}, Text: "go"}},
	})
	replayLifecycleArtifact(t, string(clean))
}

// TestLifecycleAdapter_QuarantinesFlaky proves the flake-guard backstop: a
// failure whose Replay does not reproduce identically all K times is quarantined
// and never emitted. The flaky adapter reports a reproduction on only some
// replays, so the K=5 guard fails and the promoter quarantines instead of
// writing a regression test.
func TestLifecycleAdapter_QuarantinesFlaky(t *testing.T) {
	adapter := &flakyLifecycleAdapter{}
	store, err := promoter.OpenBucketStore(filepath.Join(t.TempDir(), "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	q := &countingLifecycleQuarantiner{}
	promo := promoter.New(adapter, store, q, 5)

	f := promoter.Failure{Surface: lifecycleSurface, Oracle: promoter.Invariant, Detail: "synthetic-flaky", Artifact: json.RawMessage(`{}`)}
	out, err := promo.Promote(context.Background(), f)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out != promoter.Quarantined {
		t.Fatalf("outcome = %v, want Quarantined", out)
	}
	if q.count != 1 {
		t.Fatalf("quarantine count = %d, want 1", q.count)
	}
	if adapter.emitted {
		t.Fatal("a flaky failure must never be emitted")
	}
}

// flakyLifecycleAdapter reproduces only on every other Replay, so it never
// passes the K-consecutive flake-guard.
type flakyLifecycleAdapter struct {
	calls   int
	emitted bool
}

func (a *flakyLifecycleAdapter) Minimize(f promoter.Failure) promoter.Failure { return f }
func (a *flakyLifecycleAdapter) Signature(f promoter.Failure) promoter.Signature {
	return promoter.Signature{Oracle: f.Oracle, Key: f.Detail}
}
func (a *flakyLifecycleAdapter) Replay(context.Context, promoter.Failure) (bool, bool) {
	a.calls++
	return a.calls%2 == 1, true // fails on the 2nd replay → never K in a row
}
func (a *flakyLifecycleAdapter) Emit(promoter.Failure) (string, error) {
	a.emitted = true
	return "should-not-happen_test.go", nil
}

type countingLifecycleQuarantiner struct{ count int }

func (q *countingLifecycleQuarantiner) Quarantine(promoter.Failure, int) error {
	q.count++
	return nil
}
