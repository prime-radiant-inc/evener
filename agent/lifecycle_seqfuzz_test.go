package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
// SCOPE: the harness drives the single-session lifecycle (turn loop, tool
// execution, foreground shell jobs, forced compaction, the goal store) PLUS the
// two pieces that were deferred from 8.3 and are now landed:
//
//   - Delegate/subagent spawning (C1). A spawned child no longer reuses the
//     parent's ScriptedAdapter: an injected childClientFactory gives each child
//     its OWN client + Responder playing a pre-recorded per-child script, so the
//     child's concurrent turn never races the parent's draw sequence. opDelegate
//     spawns a leaf delegate (delegation_allowance 0, MaxSubagentDepth 1); the
//     child client factory is invoked synchronously on the parent op goroutine
//     inside createDelegate so child-script assignment is deterministic. The
//     delegate tool defaults to background (no max_wait_ms), so opDelegate returns
//     while the child runs concurrently and does NOT quiesce it — that exercises
//     the spawn-and-interleave path. Driving a background delegate all the way to
//     terminal is opBackgroundDelegate's job (C3, below).
//
//   - Background shell jobs (C2). opBackgroundShell launches a shell job with
//     background=true and then quiesces it deterministically: the async finalize
//     goroutine reads the deny env's instant Wait, the fake clock is advanced past
//     any finalize backoff, and the harness joins each job's done channel (the
//     advance + done-join handshake). The root session has no notifyFunc, so a
//     completed job closes its done channel without driving any concurrent turn.
//
//   - Background delegates (C3). opBackgroundDelegate spawns a delegate that runs
//     in the background (the delegate tool defaults to background when no
//     max_wait_ms is given): createDelegate returns immediately and a
//     fire-and-forget finalize-bridge goroutine settles the child off the op
//     goroutine. The harness quiesces it deterministically by JOINING the
//     delegate job's done channel (which closes only after the child finishes AND
//     the bridge finalizes — the bridge enqueues the owner notification BEFORE
//     closing done) and then DRAINING the notification rail: with a nil notifyFunc
//     nothing else consumes the owner notification, so drainJobNotificationTurns
//     runs an EntryNotification turn to surface it exactly once. The quiet-watchdog
//     ticker runs on the SAME injected clock (jm.clock.NewTicker), so it cannot fire
//     on wall time and is stopped at finalize; its firing is covered deterministically
//     by the dedicated TestLifecycleBackgroundDelegateWatchdogFires (a gated child +
//     BlockUntil handshake + advance past the quiet window).
//
// All of it is fully deterministic: deny env, fake clock, per-child scripted
// adapters, bounded contexts, and a wall-time wedge backstop.
//
// A discovered failure is routed through fuzz/promoter (mirroring the Phase-2
// appwire target): a deterministic reproduction survives the flake-guard and is
// emitted to a temp dir as a regression test; a flaky one is quarantined.
func TestLifecycleSeqFuzz(t *testing.T) {
	// Default-off: PersistPaths returns the temp fallbacks (no tree writes) for
	// every gate run; the local triage tool sets SERF_FUZZ_PERSIST to capture a
	// live-found crasher durably (see fuzz/promoter/persist.go).
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	emitDir, bucketsPath, _ := promoter.PersistPaths(pkgDir, t.TempDir(), filepath.Join(t.TempDir(), "buckets.json"))
	adapter := &lifecycleAdapter{emitDir: emitDir}
	store, err := promoter.OpenBucketStore(bucketsPath)
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
	kindFinal     responseKind = iota // communicate end_turn=true (terminal)
	kindAwait                         // communicate end_turn=false (awaits reply)
	kindText                          // bare assistant text, no tool calls
	kindEmpty                         // empty response (null content)
	kindPause                         // pause_turn finish reason
	kindReadFile                      // tool call: read_file
	kindGlob                          // tool call: glob
	kindGrep                          // tool call: grep
	kindShell                         // tool call: shell (foreground)
	kindCompact                       // tool call: compact (forces history shrink)
	kindWebSearch                     // assistant text carrying a server web_search part
	kindThinking                      // assistant text carrying a thinking part
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
		// Non-zero input-token usage drives recordResponseUsage's record branch
		// (RecordInputTokens): without usage, tokens==0 and it never records.
		return llm.Response{
			Message: llm.Assistant("thinking " + strconv.Itoa(callSeq)),
			Usage:   llm.Usage{InputTokens: 40 + callSeq, OutputTokens: 5},
		}
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
		return lifecycleToolCall(id, "compact_context", map[string]any{"instructions": "tighten"})
	case kindWebSearch:
		// A response whose content carries a server-side web_search part plus text.
		// Drives recordResponseUsage's web-search usage-suppression arm
		// (responseHasServerWebSearch) and the bare-text no-tool-calls path.
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "q" + strconv.Itoa(callSeq)}},
				{Kind: llm.ContentText, Text: "searched " + strconv.Itoa(callSeq)},
			}},
			// Non-zero usage present, so the web-search SUPPRESSION arm is exercised
			// with real usage (recordResponseUsage must skip recording it).
			Usage: llm.Usage{InputTokens: 200 + callSeq, OutputTokens: 8},
		}
	case kindThinking:
		// A response carrying a thinking part plus text — drives thinking content
		// classification/handling in the round.
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think " + strconv.Itoa(callSeq)}},
			{Kind: llm.ContentText, Text: "thought " + strconv.Itoa(callSeq)},
		}}}
	case kindShellBackground:
		return lifecycleToolCall(id, "shell", map[string]any{"command": "echo bg " + strconv.Itoa(callSeq), "background": true})
	case kindDelegate:
		return lifecycleToolCall(id, "delegate", map[string]any{"task": "child task " + strconv.Itoa(callSeq), "delegation_allowance": 0})
	case kindBoom:
		return lifecycleToolCall(id, lifecycleBoomTool, map[string]any{})
	default:
		return agenttest.FinalResponse("done")
	}
}

// The kinds below are past the drawn range (drawResponseKind never produces
// them), so the live fuzzer never emits them as ordinary round responses. They
// are scripted only by their dedicated ops (opBackgroundShell, opDelegate) or,
// for kindBoom, by the determinism tests paired with the panicTool injection.
const (
	kindShellBackground responseKind = numResponseKinds + iota
	kindDelegate
	kindBoom
)

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
	opDelegate
	opBackgroundShell
	opBackgroundDelegate
	opObserve
	opLLMError // fault op: the first model call of this input fails once (W1)
	opClose
)

var allLifecycleOps = []lifecycleOpCode{
	opProcessInput, opProcessInterrupted, opSteer, opEnqueue, opFollowUp,
	opSetGoal, opClearGoal, opAdvanceClock, opDelegate, opBackgroundShell,
	opBackgroundDelegate, opObserve, opLLMError, opClose,
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

// opAllowsHistoryShrink reports whether this op is permitted to shrink history.
// Compaction is the one sanctioned shrink, reached two ways: an explicit compact
// tool call in the script, OR a content-filter fault — handleModelError's
// content-filter recovery force-compacts to drop the offending content before
// retrying the round, which legitimately shortens history.
func (r opRecord) opAllowsHistoryShrink() bool {
	if r.opContainsCompact() {
		return true
	}
	return lifecycleOpCode(r.Code) == opLLMError && llmFaultKind(r.FaultKind) == faultContentFilter
}

// opRecord is one drawn operation with every parameter it needs, so the artifact
// is self-contained and a replay never touches rapid.
type opRecord struct {
	Code        int    `json:"c"`
	Script      []int  `json:"s,omitempty"`  // per-round response kinds (ProcessInput family)
	Text        string `json:"t,omitempty"`  // input/steer/followup/goal text
	Dur         int64  `json:"d,omitempty"`  // advance duration (ns) for opAdvanceClock
	IntAt       int    `json:"i,omitempty"`  // round at which to cancel ctx (interrupt)
	ChildScript []int  `json:"cs,omitempty"` // delegate child's per-round response kinds
	FaultKind   int    `json:"fk,omitempty"` // llmFaultKind injected on opLLMError's first model call
}

// llmFaultKind selects the TYPED provider error opLLMError injects on a turn's
// first model call, so the fuzzer drives each distinct handleModelError arm:
// content-filter recovery (compact + retry), non-retryable close (auth), the
// context-length warning, and the retryable paths (rate-limit/server).
type llmFaultKind int

const (
	faultGeneric       llmFaultKind = iota // plain error — recoverable/unknown
	faultContentFilter                     // 400 content-filter → compact-and-retry recovery
	faultRateLimit                         // 429 → retryable
	faultServer                            // 503 → retryable
	faultContextLength                     // 413 → context-length warning + terminal
	faultAuth                              // 401 → non-retryable, closes the session
	numFaultKinds
)

// faultError builds the typed llm.Error for a fault kind via the real HTTP-status
// classifier, so llm.Kind/llm.Classify see a genuine *contentFilterError etc. (a
// hand-rolled llm.Error would classify as KindUnknown — Kind switches on concrete
// type). Any out-of-range kind (e.g. from a decoded fuzz artifact) → generic.
func faultError(k llmFaultKind) error {
	switch k {
	case faultContentFilter:
		return llm.ErrorFromHTTPStatus("openai", 400, "content filter policy violated", nil, nil)
	case faultRateLimit:
		return llm.ErrorFromHTTPStatus("openai", 429, "rate limit exceeded", nil, nil)
	case faultServer:
		return llm.ErrorFromHTTPStatus("openai", 503, "service unavailable", nil, nil)
	case faultContextLength:
		return llm.ErrorFromHTTPStatus("openai", 413, "context length exceeded", nil, nil)
	case faultAuth:
		return llm.ErrorFromHTTPStatus("openai", 401, "invalid api key", nil, nil)
	default:
		return errors.New("lifecycle-fuzz: injected model-call fault")
	}
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
		case opProcessInput, opProcessInterrupted, opLLMError:
			scriptLen := rapid.IntRange(1, 5).Draw(rt, "scriptLen")
			rec.Script = make([]int, scriptLen)
			for j := range rec.Script {
				rec.Script[j] = drawResponseKind(rt)
			}
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
			if code == opProcessInterrupted {
				rec.IntAt = rapid.IntRange(0, scriptLen).Draw(rt, "intAt")
			}
			if code == opLLMError {
				rec.FaultKind = rapid.IntRange(0, int(numFaultKinds)-1).Draw(rt, "faultKind")
			}
		case opSteer, opEnqueue, opFollowUp, opSetGoal:
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
		case opAdvanceClock:
			rec.Dur = int64(rapid.IntRange(0, int(5*time.Minute)).Draw(rt, "durNS"))
		case opDelegate:
			// One delegate tool call in the parent's turn; the child plays a
			// short pre-recorded script drawn from the ordinary vocabulary (no
			// delegate/background kinds, so the leaf child cannot itself spawn).
			rec.Script = []int{int(kindDelegate)}
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
			csLen := rapid.IntRange(1, 4).Draw(rt, "childScriptLen")
			rec.ChildScript = make([]int, csLen)
			for j := range rec.ChildScript {
				rec.ChildScript[j] = drawResponseKind(rt)
			}
		case opBackgroundShell:
			rec.Script = []int{int(kindShellBackground)}
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
		case opBackgroundDelegate:
			// One delegate tool call in the parent's turn. The delegate tool defaults
			// to background (no max_wait_ms), so the parent returns immediately and the
			// fire-and-forget finalize bridge settles the child off the parent goroutine.
			// The child plays a short pre-recorded script (no delegate/background kinds,
			// so the leaf child cannot itself spawn).
			rec.Script = []int{int(kindDelegate)}
			rec.Text = rapid.SampledFrom(inputTexts).Draw(rt, "text")
			csLen := rapid.IntRange(1, 4).Draw(rt, "bgChildScriptLen")
			rec.ChildScript = make([]int, csLen)
			for j := range rec.ChildScript {
				rec.ChildScript[j] = drawResponseKind(rt)
			}
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
	terminalJobs  map[string]string // job ID -> the terminal status first observed (Oracle 7)
	namerSeen     bool              // the session name has been observed set (Oracle 8)
	namerValue    string            // the name value first observed (Oracle 8)
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
	var cancelAt int             // -1 disables interrupt cancellation
	var cancelFn func()          // set per interrupt op
	var pendingChildScript []int // consumed by the next child the factory builds
	var armFault bool            // W1: when set, the next model call fails once
	var faultKind llmFaultKind   // W1: which TYPED provider error to inject
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
	// W1 fault injection: opLLMError arms this so the next model call fails once
	// with the op's TYPED fault (content-filter/rate-limit/server/context-length/
	// auth/generic), exercising every handleModelError arm — content-filter
	// compact-and-retry recovery, non-retryable close, the context-length warning,
	// and the retryable paths — under the full interleaving. One-shot (disarms
	// itself) so a recoverable fault lets the round succeed on the retry.
	adapter.FaultResponder = func(llm.Request) error {
		if armFault {
			armFault = false
			return faultError(faultKind)
		}
		return nil
	}
	client := llm.NewClient()
	client.Register(adapter)
	// WithCheapModel configures the auxiliary "cheap" model the session namer
	// runs on; without it sessionNamerEnabled is false and the namer never
	// launches (so W4's namer coverage below would be vacuous).
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-5.2")
	cfg := SessionConfig{
		clock:                 clk,
		MaxSubagentDepth:      1,
		MaxToolRoundsPerInput: 10,
		// Non-blocking: retry/backoff waits return instantly so the deterministic
		// harness never deadlocks on the frozen fake clock (the backoff DELAY is
		// irrelevant to the state oracle; the retry LOGIC still runs). W1's
		// opLLMError exercises exactly this retry path.
		LLMSleep: func(_ context.Context, _ time.Duration) error { return nil },
	}
	// C1 per-child adapter seam: each spawned child gets its own client whose
	// Responder plays the op's pre-recorded child script (a pure function of the
	// recorded kinds), so the child's concurrent turn never races the parent draw.
	// The factory is invoked synchronously on the parent op goroutine inside
	// createDelegate, so consuming pendingChildScript here is race-free.
	cfg.testOnly.childClientFactory = func() *llm.Client {
		childScript := pendingChildScript
		pendingChildScript = nil
		cc := llm.NewClient()
		cc.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: newChildResponder(childScript)})
		return cc
	}
	// W4: exercise the background session-namer goroutine alongside the other ops.
	// forceSessionNamer launches it without StateDir (so it never autosaves a meta
	// file per op — disk churn the search can't afford); namerClient gives it its
	// OWN scripted adapter so its detached draw never races the shared Responder
	// (the same race childClientFactory avoids for children). The goroutine runs
	// concurrently with delegation/compaction/close and is joined by Close
	// (sendersWG); -race validates its concurrent access to session state.
	cfg.testOnly.forceSessionNamer = true
	namerClient := llm.NewClient()
	namerClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(`{"name":"fuzz session"}`), Usage: llm.Usage{TotalTokens: 7}}
	}})
	cfg.testOnly.namerClient = namerClient
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

	model := &lifecycleModel{prevHistLen: snapshotHistoryLen(sess), terminalJobs: map[string]string{}}

	for i, op := range art.Ops {
		// Reset per-op responder state.
		script = op.Script
		callSeq = 0
		cancelAt = -1
		cancelFn = nil
		pendingChildScript = op.ChildScript
		armFault = lifecycleOpCode(op.Code) == opLLMError // W1: fail this op's first model call
		faultKind = llmFaultKind(op.FaultKind)

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
	case opProcessInput, opLLMError:
		// opLLMError is opProcessInput with a one-shot model-call fault armed by
		// the caller (W1): the turn's first Complete fails, exercising recovery.
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
	case opDelegate:
		// A leaf delegate: the parent turn issues one delegate tool call. The
		// delegate tool defaults to background (no max_wait_ms), so the call returns
		// while the child runs its own pre-recorded script concurrently; opDelegate
		// deliberately does NOT quiesce it (the spawn-and-interleave path). The
		// child is settled by the next op's quiesce, or at Close.
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
		defer cancel()
		_, _ = sess.ProcessInput(ctx, op.Text, nil)
	case opBackgroundShell:
		// Launch a background shell job, then quiesce it: advance the fake clock
		// past any finalize backoff and join each job's done channel so the async
		// finalize is observed deterministically (C2).
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
		defer cancel()
		_, _ = sess.ProcessInput(ctx, op.Text, nil)
		quiesceJobs(sess, clk)
	case opBackgroundDelegate:
		// Spawn a background delegate, then quiesce it: the parent turn returns
		// immediately, so the child + finalize bridge run off the op goroutine.
		// quiesceJobs joins every running job's done channel (the delegate job's done
		// closes only after the child finishes AND the bridge finalizes it), and
		// drainJobNotificationTurns drives the owner notification the bridge enqueues
		// to a notification turn — the C3 advance + done-join + notification-drain
		// handshake. The root session's nil notifyFunc never drives this otherwise.
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
		defer cancel()
		_, _ = sess.ProcessInput(ctx, op.Text, nil)
		quiesceJobs(sess, clk)
		drainJobNotificationTurns(sess)
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
	namingSet := sess.naming.set
	namingValue := sess.naming.value
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
	if histLen < m.prevHistLen && !op.opAllowsHistoryShrink() {
		return lifecycleFailure(promoter.Invariant, art, step, fmt.Sprintf("history-shrank-without-compaction:%d->%d", m.prevHistLen, histLen))
	}

	// Oracle 6 (jobs): a background job, once quiesced, reaches a terminal status —
	// no job is left stuck Running, and every recorded job is terminal. This is the
	// deterministic observation the C2 (shell) / C3 (delegate) advance + done-join
	// handshake makes possible. quiesceJobs joins EVERY running job's done channel,
	// so a background delegate left running by an earlier opDelegate is also settled
	// here before the check.
	switch lifecycleOpCode(op.Code) {
	case opBackgroundShell, opBackgroundDelegate:
		if f := checkJobsQuiesced(sess, art, step); f != nil {
			return f
		}
	}

	// Oracle 7 (cross-subsystem): a job's terminal state is FINAL through the full
	// interleaving. Once observed terminal, a job must never be seen non-terminal
	// again (terminal-stickiness), nor flip to a DIFFERENT terminal status (e.g.
	// Done -> Failed) — no matter what compaction / delegation / goal churn ran
	// between observations. The isolated jobstore model establishes these only
	// without that churn; this asserts they survive it. Only currently-present
	// jobs are checked, so eviction is not a violation; a reappearing-changed
	// record is.
	if jm := sess.jobManager; jm != nil {
		for _, rec := range jm.list(listFilter{}) {
			if prev, seen := m.terminalJobs[rec.JobID]; seen {
				if !rec.Status.IsTerminal() {
					return lifecycleFailure(promoter.Invariant, art, step,
						fmt.Sprintf("job-terminal-unstuck:%s:%s->%s", rec.JobID, prev, rec.Status))
				}
				if string(rec.Status) != prev {
					return lifecycleFailure(promoter.Invariant, art, step,
						fmt.Sprintf("job-terminal-status-changed:%s:%s->%s", rec.JobID, prev, rec.Status))
				}
			} else if rec.Status.IsTerminal() {
				m.terminalJobs[rec.JobID] = string(rec.Status)
			}
		}
	}

	// Oracle 8 (cross-subsystem): the background namer goroutine's effect is
	// monotonic and stable through the full interleaving — once the session name
	// is observed set it stays set and never changes value, regardless of what
	// delegation / compaction / close ran concurrently with the namer. (The
	// primary win is -race coverage of the namer's concurrent session-state
	// access; this invariant additionally pins the state it mutates.)
	if m.namerSeen {
		if !namingSet {
			return lifecycleFailure(promoter.Invariant, art, step, "session-name-unset-after-set")
		}
		if namingValue != m.namerValue {
			return lifecycleFailure(promoter.Invariant, art, step,
				fmt.Sprintf("session-name-changed:%q->%q", m.namerValue, namingValue))
		}
	} else if namingSet {
		m.namerSeen = true
		m.namerValue = namingValue
	}

	m.prevTurns = turns
	m.prevModelResp = modelResp
	m.prevHistLen = histLen
	return nil
}

// checkJobsQuiesced asserts every job has reached a terminal status after a
// background-shell op's quiescence.
func checkJobsQuiesced(sess *Session, art lifecycleArtifact, step int) *promoter.Failure {
	jm := sess.jobManager
	if jm == nil {
		return nil
	}
	if running := jm.runningJobIDs(); len(running) != 0 {
		return lifecycleFailure(promoter.Invariant, art, step, fmt.Sprintf("background-job-not-quiesced:%d-running", len(running)))
	}
	for _, rec := range jm.list(listFilter{}) {
		if !rec.Status.IsTerminal() {
			return lifecycleFailure(promoter.Invariant, art, step, "job-nonterminal-after-quiesce:"+string(rec.Status))
		}
	}
	return nil
}

func snapshotHistoryLen(sess *Session) int {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return len(sess.history)
}

// newChildResponder returns a Responder that plays a pre-recorded child script.
// It is a pure function of the script (its own call counter, the same hard cap
// and Final fallback as the parent), so a child turn running concurrently on its
// own goroutine is fully deterministic and never draws from rapid.
func newChildResponder(script []int) func(llm.Request) llm.Response {
	var seq int
	return func(req llm.Request) llm.Response {
		idx := seq
		seq++
		if idx >= responderHardCap {
			return agenttest.FinalResponse("child done")
		}
		if idx < len(script) {
			return buildResponse(responseKind(script[idx]), idx)
		}
		return agenttest.FinalResponse("child done")
	}
}

// quiesceJobs deterministically drives every running job to terminal: it advances
// the fake clock past any finalize backoff (the success path parks on nothing,
// but a retry would Sleep on the clock) and joins each job's done channel — the
// C2 advance + done-join handshake. The wall-time bound is only a wedge backstop;
// the deny env's instant Wait means finalize completes promptly.
func quiesceJobs(sess *Session, clk *agenttest.FakeClock) {
	jm := sess.jobManager
	if jm == nil {
		return
	}
	dones := runningJobDones(jm)
	clk.Advance(shellFinalizeMaxRetryDelay)
	for _, d := range dones {
		select {
		case <-d:
		case <-time.After(lifecycleCallTimeout):
			return
		}
	}
}

// notificationDrainCap bounds the notification-drain loop so a degenerate
// requeue (a notification turn that re-enqueues its own input) can never spin
// forever. One EntryNotification turn drains the entire pending queue at once
// (acceptNotificationInput consumes the whole slice), so the happy path needs a
// single iteration; the cap is pure insurance.
const notificationDrainCap = 8

// drainJobNotificationTurns drives the notification rail to quiescence: while the
// session has a pending job-completion notification (the owner notification a
// background job's finalize bridge enqueues, plus any quiet-watchdog notification),
// it runs an EntryNotification turn so the notification is surfaced into the
// transcript and consumed exactly once. The root harness has a nil notifyFunc, so
// nothing else ever drains the queue; without this, a completed background
// delegate's notification would sit pending and be surfaced nondeterministically
// by some later op's drive loop. The per-op responder's Final fallback answers the
// notification turn, so the drain never draws from rapid and never blocks.
func drainJobNotificationTurns(sess *Session) {
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleCallTimeout)
	defer cancel()
	for i := 0; i < notificationDrainCap && sess.peekNotifications() > 0; i++ {
		_, _ = sess.ProcessInputKind(ctx, "", nil, EntryNotification)
	}
}

// runningJobDones snapshots the done channel of every currently-running job.
func runningJobDones(jm *jobManager) []chan struct{} {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	out := make([]chan struct{}, 0, len(jm.running))
	for _, run := range jm.running {
		out = append(out, run.done)
	}
	return out
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
	case opDelegate:
		return "delegate"
	case opBackgroundShell:
		return "background_shell"
	case opBackgroundDelegate:
		return "background_delegate"
	case opObserve:
		return "observe"
	case opLLMError:
		return "llm_error"
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
