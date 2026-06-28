package appserver

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

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/fuzz/promoter"
)

// TestRouterSeqFuzz is Phase-2 target #6: a stateful (sequence) fuzz of the
// appwire dispatch lifecycle. A rapid state machine draws sequences of protocol
// operations and replays each against the REAL connection seam
// (Connection.HandleMessage → Router.Dispatch), checking, weakest-first:
//
//	Oracle 1 (floor):  no legal-ish sequence panics. The router does NOT recover
//	                   handler panics (Dispatch calls fn directly), so this is a
//	                   live panic-hunt of the dispatch path.
//	Oracle 2 (wedge):  every dispatch returns under a bounded context; a call that
//	                   never returns is a wedge.
//	Oracle 3 (status): the connection's lifecycle status is monotonic. The model is
//	                   the monotonic initialize-gate (fresh answers ping, accepts the
//	                   first initialize, rejects everything else until initialized,
//	                   then stays initialized). Any real status regression surfaces as
//	                   a response-KIND divergence from the monotonic model — once the
//	                   model is initialized it predicts Response for a gated method, so
//	                   a connection that reverted to "initialize required" is caught.
//
// SCOPE (honest): the turn/job *status* lifecycle (a turn going processing→idle,
// steer/interrupt/queue semantics) is NOT in this package. Those handlers live in
// server/appwire_runtime.go (driven by a live agent/LLM, not deterministic offline)
// and cmd/serf-hub/app_rpc.go (package main; and importing server here would be an
// import cycle). internal/appserver is pure transport: a generic Router with
// injected handler closures plus the real connection gate. So the model is scoped to
// the dispatch/lifecycle layer that needs no live LLM — the connection protocol
// state machine — and the lifecycle methods (thread/turn) are exercised as real
// routed dispatch targets backed by deterministic stubs. Their business status logic
// is out of reach by design and is reported, not faked.
//
// The transition model is a DECLARATIVE TABLE (opTable): one entry per operation,
// each declaring how to build its wire message and what kind/post-state the
// monotonic model predicts. The table is the reusable artifact (design §7 #5); the
// rapid machine just draws ops and replays them.
//
// A discovered failure is routed through fuzz/promoter so a deterministic
// reproduction becomes a flake-guarded regression test (emitted to a temp dir;
// promotion into the tree is the human/opt-in step), mirroring Phase 1.
func TestRouterSeqFuzz(t *testing.T) {
	adapter := &seqAdapter{table: liveOpTable, registrar: registerSeqStubs, emitDir: t.TempDir()}
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
		t.Logf("seq-fuzz failure promoted: outcome=%v err=%v detail=%q", out, err, captured.Detail)
	})

	rapid.Check(t, func(rt *rapid.T) {
		ops := rapid.SliceOfN(rapid.SampledFrom(allLiveOps), 1, 32).Draw(rt, "ops")
		if f := seqOracleRun(ops, liveOpTable, registerSeqStubs); f != nil {
			captured = f
			rt.Fatalf("seq oracle: %s", f.Detail)
		}
	})
}

// TestSeqAdapter_PromotesDeterministicFailure exercises the appserver-side
// promoter Adapter end-to-end against the REAL promoter, using a deterministic,
// real panic from the dispatch seam: a registered handler that panics. The router
// does not recover handler panics, so HandleMessage panics; the harness must catch
// it, classify it as a Panic failure, survive the flake-guard, emit a regression
// test, record its bucket, and dedup on the second sighting. This proves the four
// hooks wire up without depending on the live fuzzer finding a real bug. The
// panicking handler is a test fixture (it locks in that Dispatch propagates handler
// panics to the caller), not a claim about serf production code.
func TestSeqAdapter_PromotesDeterministicFailure(t *testing.T) {
	const opBoom = opCode(100)
	boomTable := opTableT{
		opInitialize: liveOpTable[opInitialize],
		opBoom: opSpec{
			name:   "boom",
			build:  func(id int64) appwire.Message { return appwire.RequestMessage(appwire.NewIntID(id), "test/boom", nil) },
			expect: func(s lifecycleState) (appwire.MessageKind, lifecycleState) { return appwire.MessageResponse, s },
		},
	}
	registrar := func(r *Router) {
		registerSeqStubs(r)
		r.Handle("test/boom", func(context.Context, json.RawMessage) (any, error) { panic("seqfuzz boom") })
	}

	adapter := &seqAdapter{table: boomTable, registrar: registrar, emitDir: t.TempDir()}
	store, err := promoter.OpenBucketStore(filepath.Join(t.TempDir(), "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	q := &countingQuarantiner{}
	promo := promoter.New(adapter, store, q, 5)

	ops := []opCode{opInitialize, opBoom}
	f := seqOracleRun(ops, boomTable, registrar)
	if f == nil {
		t.Fatal("expected a panic failure when a dispatched handler panics")
	}
	if f.Oracle != promoter.Panic {
		t.Fatalf("oracle = %v, want Panic", f.Oracle)
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

	// Exercise the body an emitted regression test runs against the live config: a
	// clean sequence must replay without a failure (the seam does not panic).
	clean, _ := json.Marshal(seqArtifact{Ops: []int{int(opInitialize), int(opThreadList)}, FailStep: -1})
	replaySeqArtifact(t, string(clean))
}

// lifecycleState is the connection's modeled status. The status is monotonic:
// fresh advances to initialized exactly once and never reverts.
type lifecycleState struct {
	initialized bool
}

type opCode int

// opSpec declares one protocol operation: how to build its wire message and what
// the monotonic model predicts. build takes a per-step request id so each request
// in a sequence carries a distinct id.
type opSpec struct {
	name   string
	build  func(id int64) appwire.Message
	expect func(s lifecycleState) (appwire.MessageKind, lifecycleState)
}

// opTableT is the declarative transition table — the model.
type opTableT map[opCode]opSpec

const (
	opInitialize opCode = iota
	opPing
	opNotification
	opThreadList
	opTurnStart
	opTurnSteer
	opTurnInterrupt
	opTurnQueue
	opThreadClear
	opUnknown
	opBadParams
)

// liveOpTable is the protocol model the live fuzzer drives. Each expect closure
// encodes the real initialize-gate contract from Connection.HandleMessage.
var liveOpTable = buildLiveOpTable()

// allLiveOps is the op set the fuzzer samples from (table keys, in a fixed order
// for deterministic sampling).
var allLiveOps = []opCode{
	opInitialize, opPing, opNotification,
	opThreadList, opTurnStart, opTurnSteer, opTurnInterrupt, opTurnQueue, opThreadClear,
	opUnknown, opBadParams,
}

func buildLiveOpTable() opTableT {
	req := func(method string, params any) func(int64) appwire.Message {
		return func(id int64) appwire.Message {
			return appwire.RequestMessage(appwire.NewIntID(id), method, params)
		}
	}
	always := func(k appwire.MessageKind) func(lifecycleState) (appwire.MessageKind, lifecycleState) {
		return func(s lifecycleState) (appwire.MessageKind, lifecycleState) { return k, s }
	}
	// gated: a routed method is rejected until initialized, then accepted.
	gated := func(s lifecycleState) (appwire.MessageKind, lifecycleState) {
		if !s.initialized {
			return appwire.MessageError, s
		}
		return appwire.MessageResponse, s
	}
	// initialize: accepted once on a fresh connection (advancing status), rejected
	// as "already initialized" thereafter.
	initialize := func(s lifecycleState) (appwire.MessageKind, lifecycleState) {
		if s.initialized {
			return appwire.MessageError, s
		}
		return appwire.MessageResponse, lifecycleState{initialized: true}
	}

	return opTableT{
		opInitialize:    {name: "initialize", build: req(appwire.MethodInitialize, appwire.InitializeParams{}), expect: initialize},
		opPing:          {name: "ping", build: req(appwire.MethodPing, nil), expect: always(appwire.MessageResponse)},
		opNotification:  {name: "notification", build: func(int64) appwire.Message { return appwire.NotificationMessage(appwire.MethodInitialized, nil) }, expect: always(appwire.MessageInvalid)},
		opThreadList:    {name: "thread/list", build: req(appwire.MethodThreadList, appwire.ThreadListParams{}), expect: gated},
		opTurnStart:     {name: "turn/start", build: req(appwire.MethodTurnStart, appwire.TurnStartParams{}), expect: gated},
		opTurnSteer:     {name: "turn/steer", build: req(appwire.MethodTurnSteer, appwire.TurnSteerParams{}), expect: gated},
		opTurnInterrupt: {name: "turn/interrupt", build: req(appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{}), expect: gated},
		opTurnQueue:     {name: "turn/queue", build: req(appwire.MethodTurnQueue, appwire.TurnQueueParams{}), expect: gated},
		opThreadClear:   {name: "thread/clear", build: req(appwire.MethodThreadClear, appwire.ThreadClearParams{}), expect: gated},
		// Unknown method and malformed params both yield an error in any state
		// (the gate fires first when fresh; MethodNotFound / InvalidParams once
		// initialized).
		opUnknown:   {name: "unknown-method", build: req("test/does-not-exist", nil), expect: always(appwire.MessageError)},
		opBadParams: {name: "bad-params", build: req(appwire.MethodTurnStart, []int{1, 2, 3}), expect: always(appwire.MessageError)},
	}
}

// registerSeqStubs wires deterministic, instant handlers for the routed lifecycle
// methods so dispatch routing, typed param decode, and WireError mapping run for
// real while staying offline. initialize is already registered by NewServer.
func registerSeqStubs(r *Router) {
	HandleTyped(r, appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	HandleTyped(r, appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		return appwire.TurnStartResponse{}, nil
	})
	HandleTyped(r, appwire.MethodTurnSteer, func(context.Context, appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	HandleTyped(r, appwire.MethodTurnInterrupt, func(context.Context, appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	HandleTyped(r, appwire.MethodTurnQueue, func(context.Context, appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, nil
	})
	HandleTyped(r, appwire.MethodThreadClear, func(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		return appwire.ThreadClearResponse{}, nil
	})
}

// seqCallTimeout bounds a single dispatch for the wedge oracle. HandleMessage is
// synchronous and the stubs are instant, so a real expiry means a wedge.
const seqCallTimeout = 2 * time.Second

// seqArtifact is the minimized reproducer: the op-code sequence and the failing
// step. It is replayed against a fresh connection to confirm determinism.
type seqArtifact struct {
	Ops      []int `json:"ops"`
	FailStep int   `json:"failStep"`
}

// seqOracleRun replays an op sequence against a fresh connection seam and returns
// the first failure, or nil when the whole sequence is handled as the model
// predicts. It is the single source of the oracle, so the live property and the
// adapter's Replay classify identically. table+registrar fully determine the
// reproduction environment.
func seqOracleRun(ops []opCode, table opTableT, registrar func(*Router)) *promoter.Failure {
	server := NewServer(ServerConfig{ServerName: "serf-hub", Version: "test", SourceID: "local"})
	registrar(server.Router())
	conn := server.NewConnection("conn-seq")

	state := lifecycleState{}
	for i, code := range ops {
		spec, ok := table[code]
		if !ok {
			continue
		}
		wantKind, wantState := spec.expect(state)
		res, wedged := safeHandle(context.Background(), conn, spec.build(int64(i+1)), seqCallTimeout)
		switch {
		case wedged:
			return seqFailure(promoter.Wedge, ops, i, "wedge:"+spec.name)
		case res.panicked:
			return seqPanicFailure(ops, i, spec.name, res)
		}
		if got := res.msg.Kind(); got != wantKind {
			return seqFailure(promoter.Invariant, ops, i,
				fmt.Sprintf("kind-mismatch:%s:init=%v:got=%s:want=%s",
					spec.name, state.initialized, kindName(got), kindName(wantKind)))
		}
		state = wantState
	}
	return nil
}

// callResult carries the outcome of one HandleMessage call: the response, or a
// recovered panic.
type callResult struct {
	msg      appwire.Message
	panicked bool
	panicVal any
	stack    []string
}

// safeHandle runs one HandleMessage under a bounded context, recovering a panic
// (the router does not) and reporting a wedge if the call never returns. The call
// runs in a goroutine so a recovered panic stays classifiable and a wedge cannot
// hang the fuzzer.
func safeHandle(ctx context.Context, conn *Connection, msg appwire.Message, timeout time.Duration) (callResult, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan callResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- callResult{panicked: true, panicVal: r, stack: captureStack()}
			}
		}()
		done <- callResult{msg: conn.HandleMessage(ctx, msg)}
	}()
	select {
	case res := <-done:
		return res, false
	case <-ctx.Done():
		return callResult{}, true
	}
}

func seqFailure(oracle promoter.OracleTag, ops []opCode, step int, detail string) *promoter.Failure {
	artifact, _ := json.Marshal(seqArtifact{Ops: opCodesToInts(ops), FailStep: step})
	return &promoter.Failure{Surface: "appwire-seq", Oracle: oracle, Detail: detail, Artifact: artifact}
}

func seqPanicFailure(ops []opCode, step int, name string, res callResult) *promoter.Failure {
	artifact, _ := json.Marshal(seqArtifact{Ops: opCodesToInts(ops), FailStep: step})
	return &promoter.Failure{
		Surface:  "appwire-seq",
		Oracle:   promoter.Panic,
		Stack:    res.stack,
		Detail:   "dispatch-panic:" + name + ":" + firstLine(fmt.Sprint(res.panicVal)),
		Artifact: artifact,
	}
}

// seqAdapter is the appserver-side promoter.Adapter for the appwire sequence
// surface. table+registrar describe the reproduction environment so Replay
// re-runs the exact configuration the failure was found under.
type seqAdapter struct {
	table     opTableT
	registrar func(*Router)
	emitDir   string
}

func (a *seqAdapter) Minimize(f promoter.Failure) promoter.Failure { return f }

func (a *seqAdapter) Signature(f promoter.Failure) promoter.Signature {
	key := f.Detail
	if f.Oracle == promoter.Panic && len(f.Stack) > 0 {
		key = strings.Join(topFrames(f.Stack, 4), "|")
	}
	if key == "" {
		key = promoter.ShortHash(f)
	}
	return promoter.Signature{Oracle: f.Oracle, Key: key}
}

func (a *seqAdapter) Replay(_ context.Context, f promoter.Failure) (bool, bool) {
	var art seqArtifact
	if err := json.Unmarshal(f.Artifact, &art); err != nil {
		return false, false
	}
	repro := seqOracleRun(intsToOpCodes(art.Ops), a.table, a.registrar)
	if repro == nil {
		return false, false
	}
	return true, a.Signature(*repro) == a.Signature(f)
}

func (a *seqAdapter) Emit(f promoter.Failure) (string, error) {
	return promoter.WriteGoTest(a.emitDir, promoter.GoTest{
		Package:    "appserver",
		Surface:    f.Surface,
		Oracle:     f.Oracle,
		Signature:  a.Signature(f).String(),
		Seam:       "appserver.Connection.HandleMessage / Router.Dispatch",
		Hash:       promoter.ShortHash(f),
		ReplayBody: "\treplaySeqArtifact(t, " + strconv.Quote(string(f.Artifact)) + ")",
	})
}

// replaySeqArtifact is the body of an emitted regression test: it replays the
// recorded op sequence against the live dispatch config and asserts the seam no
// longer fails. (Generated regression tests for live-found bugs call this; it is
// kept here so an emitted test compiles when moved into the package.)
func replaySeqArtifact(t *testing.T, artifact string) {
	t.Helper()
	var art seqArtifact
	if err := json.Unmarshal([]byte(artifact), &art); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	if f := seqOracleRun(intsToOpCodes(art.Ops), liveOpTable, registerSeqStubs); f != nil {
		t.Fatalf("seq oracle still fails on recorded artifact: %s", f.Detail)
	}
}

func opCodesToInts(ops []opCode) []int {
	out := make([]int, len(ops))
	for i, c := range ops {
		out[i] = int(c)
	}
	return out
}

func intsToOpCodes(ns []int) []opCode {
	out := make([]opCode, len(ns))
	for i, n := range ns {
		out[i] = opCode(n)
	}
	return out
}

func kindName(k appwire.MessageKind) string {
	switch k {
	case appwire.MessageInvalid:
		return "invalid"
	case appwire.MessageRequest:
		return "request"
	case appwire.MessageNotification:
		return "notification"
	case appwire.MessageResponse:
		return "response"
	case appwire.MessageError:
		return "error"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
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
