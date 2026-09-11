package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/jobstore"
	tooldefs "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// This forwards the real launcher and retains its actual completion receipt.
// It does not expose or pretend to observe the launcher's private signal API.
type retirementDetachedEnvironment struct {
	*execenv.LocalExecutionEnvironment
	entered chan struct{}
	resume  chan struct{}
	started execenv.DetachedProcess
	calls   atomic.Int32
}

func (e *retirementDetachedEnvironment) DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (execenv.DetachedProcess, error) {
	e.calls.Add(1)
	close(e.entered)
	<-e.resume
	started, err := e.LocalExecutionEnvironment.DetachCommand(ctx, command, workingDir, envVars)
	e.started = started
	return started, err
}

func TestRetirementSafetyDetachedLifetime(t *testing.T) {
	for _, claimFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("claim-first=%t", claimFirst), func(t *testing.T) {
			dir := t.TempDir()
			mkfifo := exec.CommandContext(t.Context(), "mkfifo", "retirement-gate")
			mkfifo.Dir = dir
			if out, err := mkfifo.CombinedOutput(); err != nil {
				t.Fatalf("real detached FIFO setup: %v: %s", err, out)
			}
			gate, err := os.OpenFile(filepath.Join(dir, "retirement-gate"), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			env := &retirementDetachedEnvironment{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir), entered: make(chan struct{}), resume: make(chan struct{})}
			var turnDone chan error
			turnJoined := false
			client := llm.NewClient()
			adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response {
					return toolCallResponse(llm.ToolCallData{ID: "original-detached-launch", Name: "shell", Type: "function", Arguments: []byte(`{"command":"read -r line < retirement-gate; printf '%s' \"$line\" > retirement-finished","mode":"detached"}`)})
				},
				func(llm.Request) llm.Response { return finalResponse("detached launch settled") },
			}}
			client.Register(adapter)
			root, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), env, SessionConfig{StateDir: t.TempDir()})
			if err != nil {
				_ = gate.Close()
				t.Fatal(err)
			}
			defer root.Close()
			defer func() {
				select {
				case <-env.resume:
				default:
					close(env.resume)
				}
				if turnDone != nil && !turnJoined {
					if err := <-turnDone; err != nil {
						t.Error(err)
					}
				}
				if _, err := gate.WriteString("original-natural-completion\n"); err != nil {
					t.Error(err)
				}
				if env.started.Done != nil {
					retirementAwait(t, env.started.Done)
				}
				if err := gate.Close(); err != nil {
					t.Error(err)
				}
			}()
			c := retirementEvidenceController(t, root)
			if claimFirst {
				claim, state, err := c.TryClaim(true)
				if err != nil || claim == nil {
					t.Fatalf("initial claim: %+v %v", state, err)
				}
				_, runErr := root.ProcessInput(context.Background(), "launch independent detached process", nil)
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
				if !errors.Is(runErr, ErrRetirementUnavailable) || env.calls.Load() != 0 || len(adapter.Requests()) != 0 {
					t.Fatalf("refused launch reached provider/launcher: %v calls=%d", runErr, env.calls.Load())
				}
			}
			turnDone = make(chan error, 1)
			go func() {
				_, err := root.ProcessInput(context.Background(), "launch independent detached process", nil)
				turnDone <- err
			}()
			retirementAwait(t, env.entered)
			claim, state, claimErr := c.TryClaim(true)
			close(env.resume)
			runErr := <-turnDone
			turnJoined = true
			if runErr != nil {
				t.Fatal(runErr)
			}
			if claim != nil {
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
			}
			if claimErr != nil || claim != nil {
				t.Fatalf("launching real detached tool escaped: %+v %v", state, claimErr)
			}
			original := env.started
			sawOriginal := false
			for _, req := range adapter.Requests() {
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if result := part.ToolResult; result != nil && result.ToolCallID == "original-detached-launch" {
							var receipt struct {
								PID          int
								Mode, Status string
							}
							if err := json.Unmarshal([]byte(fmt.Sprint(result.Content)), &receipt); err != nil || result.IsError || receipt.PID != original.PID || receipt.Mode != "detached" || receipt.Status != "started" {
								t.Fatalf("original launch tool receipt: %+v %v", result, err)
							}
							sawOriginal = true
						}
					}
				}
			}
			if original.PID <= 0 || original.Done == nil || !sawOriginal {
				t.Fatalf("actual detached launch receipt missing: %+v", original)
			}
			root.mu.Lock()
			before := slices.Clone(root.detachedProcesses)
			root.mu.Unlock()
			if len(before) != 1 || before[0].pid != original.PID || before[0].done != original.Done {
				t.Fatal("warning owner lost exact launch receipt")
			}
			claim, state, err = c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("independent detached lifetime blocked eligibility: %+v %v", state, err)
			}
			root.mu.Lock()
			after := slices.Clone(root.detachedProcesses)
			root.mu.Unlock()
			select {
			case <-original.Done:
				t.Error("original independent process exited before fixture completion")
			default:
			}
			if !reflect.DeepEqual(after, before) {
				t.Error("claim changed original detached warning receipt")
			}
			if err := c.Abort(claim, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := gate.WriteString("original-natural-completion\n"); err != nil {
				t.Fatal(err)
			}
			retirementAwait(t, original.Done)
			content, err := os.ReadFile(filepath.Join(dir, "retirement-finished"))
			if err != nil || string(content) != "original-natural-completion" {
				t.Fatalf("original process did not complete naturally: %q %v", content, err)
			}
			if live := root.runningDetachedProcessIDs(); len(live) != 0 {
				t.Fatalf("exited process retained warning: %v", live)
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

type retirementAttentionAdapter struct {
	retirementDelegateAdapter
	fail    atomic.Bool
	settled atomic.Int32
}

func (a *retirementAttentionAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	if a.fail.Load() {
		return llm.Response{}, context.Canceled
	}
	a.settled.Add(1)
	return a.retirementDelegateAdapter.Complete(ctx, req)
}

// A real delegate result creates root attention. A failed provider turn arms
// the actual retry, whose wake can outlive successful receipt consumption.
func TestRetirementAutonomousAttentionRetryWake(t *testing.T) {
	for _, attachBefore := range []bool{true, false} {
		t.Run(fmt.Sprintf("attached-first=%t", attachBefore), func(t *testing.T) {
			retirementAttentionRetryWake(t, attachBefore)
		})
	}
}

func retirementAttentionRetryWake(t *testing.T, attachBefore bool) {
	t.Helper()
	clk := agenttest.NewFakeClock()
	adapter := &retirementAttentionAdapter{retirementDelegateAdapter: retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk, MaxSubagentDepth: 1}), withAdapter(adapter))
	var c *RetirementController
	if attachBefore {
		c = retirementEvidenceController(t, root)
	}
	d := root.createDelegate(context.Background(), delegateArgs{Task: "original attention retry source"})
	if d.Err != nil {
		t.Fatal(d.Err)
	}
	sub := root.subagents.get(d.ChildSessionID)
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	retirementAwait(t, done)
	path := transcriptPath(root.stateDir, root.id)
	original, err := readDelegateAttentionFold(path, root.id)
	if err != nil || len(original.pendingIDs()) != 1 {
		t.Fatalf("actual delegate result did not create original attention: %+v %v", original.pendingIDs(), err)
	}
	id := original.pendingIDs()[0]
	adapter.fail.Store(true)
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); !errors.Is(err, context.Canceled) {
		t.Fatalf("provider failure = %v", err)
	}
	adapter.fail.Store(false)
	root.attentionMu.Lock()
	active := root.rootAttentionRetry.active
	root.attentionMu.Unlock()
	if !active {
		t.Fatal("failed original attention turn did not arm retry")
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	root.SetNotifyFunc(func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-resume
		}
	})
	defer func() { close(resume); clk.Drain(); root.SetNotifyFunc(nil) }()
	armed.Store(true)
	clk.Advance(jobNotificationRetryInitialDelay)
	retirementAwait(t, entered)
	if c == nil {
		c = retirementEvidenceController(t, root)
	}
	pendingClaim, pendingState, pendingErr := c.TryClaim(true)
	if pendingClaim != nil {
		if err := c.Abort(pendingClaim, ""); err != nil {
			t.Fatal(err)
		}
	}
	if pendingErr != nil || pendingClaim != nil {
		t.Fatalf("original pending attention escaped: %+v %v", pendingState, pendingErr)
	}
	preserved, err := readDelegateAttentionFold(path, root.id)
	if err != nil || !reflect.DeepEqual(preserved, original) {
		t.Fatalf("refused claim changed original pending source: %v", err)
	}
	beforeCalls := adapter.settled.Load()
	beforeRequests := len(adapter.Requests())
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	settled, err := readDelegateAttentionFold(path, root.id)
	if err != nil || !reflect.DeepEqual(settled.content[id], original.content[id]) || settled.resolutions[id] != delegateAttentionConsumed || len(settled.pendingIDs()) != 0 || adapter.settled.Load() <= beforeCalls || !requestsContain(adapter.Requests()[beforeRequests:], original.content[id].Text()) {
		t.Fatalf("original source did not reach provider and settle: %v %+v", err, settled.resolutions)
	}
	claim, state, claimErr := c.TryClaim(true)
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	if claimErr != nil || claim != nil {
		t.Fatalf("unsettled original attention wake escaped: %+v %v", state, claimErr)
	}
	resume <- struct{}{}
	clk.Drain()
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementAutonomousAttentionRetryOverlap(t *testing.T) {
	clk := agenttest.NewFakeClock()
	adapter := &retirementAttentionAdapter{retirementDelegateAdapter: retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk, MaxSubagentDepth: 1}), withAdapter(adapter))
	entered := []chan struct{}{make(chan struct{}), make(chan struct{})}
	resume := []chan struct{}{make(chan struct{}), make(chan struct{})}
	defer func() {
		for _, ch := range resume {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		clk.Drain()
		root.SetNotifyFunc(nil)
	}()
	var gate atomic.Int32
	root.SetNotifyFunc(func() {
		if selected := gate.Swap(0); selected != 0 {
			close(entered[selected-1])
			<-resume[selected-1]
		}
	})
	var c *RetirementController
	for i := range 2 {
		d := root.createDelegate(context.Background(), delegateArgs{Task: "original overlapping attention"})
		if d.Err != nil {
			t.Fatal(d.Err)
		}
		sub := root.subagents.get(d.ChildSessionID)
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		retirementAwait(t, done)
		path := transcriptPath(root.stateDir, root.id)
		original, err := readDelegateAttentionFold(path, root.id)
		if err != nil || len(original.pendingIDs()) != 1 {
			t.Fatalf("original overlapping source missing: %v %v", original.pendingIDs(), err)
		}
		id := original.pendingIDs()[0]
		adapter.fail.Store(true)
		if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); !errors.Is(err, context.Canceled) {
			t.Fatalf("provider failure = %v", err)
		}
		adapter.fail.Store(false)
		gate.Store(int32(i + 1))
		clk.Advance(jobNotificationRetryInitialDelay)
		retirementAwait(t, entered[i])
		if c == nil {
			c = retirementEvidenceController(t, root)
		}
		assertRetirementEvidenceBlocked(t, c, "notification")
		preserved, err := readDelegateAttentionFold(path, root.id)
		if err != nil || !reflect.DeepEqual(preserved, original) {
			t.Fatalf("refusal changed original overlapping source: %v", err)
		}
		beforeRequests := len(adapter.Requests())
		if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		settled, err := readDelegateAttentionFold(path, root.id)
		if err != nil || !reflect.DeepEqual(settled.content[id], original.content[id]) || settled.resolutions[id] != delegateAttentionConsumed || !requestsContain(adapter.Requests()[beforeRequests:], original.content[id].Text()) {
			t.Fatalf("original overlapping source did not reach provider and settle: %v", err)
		}
	}
	close(resume[1])
	// TRIPWIRE: hang guard only; the resumed callback is already unblocked,
	// so settlement completes in microseconds absent a deadlock.
	waitForCondition(t, 5*time.Second, "later attention callback settlement", func() bool {
		root.attentionMu.Lock()
		defer root.attentionMu.Unlock()
		return root.attentionCallbacks == 1
	})
	assertRetirementEvidenceBlocked(t, c, "notification")
	close(resume[0])
	clk.Drain()
	root.attentionMu.Lock()
	remaining := root.attentionCallbacks
	root.attentionMu.Unlock()
	if remaining != 0 {
		t.Fatalf("settled callbacks retained %d registrations", remaining)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementAutonomousAttentionRetryStale(t *testing.T) {
	clk := agenttest.NewFakeClock()
	adapter := &retirementAttentionAdapter{retirementDelegateAdapter: retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk, MaxSubagentDepth: 1}), withAdapter(adapter))
	c := retirementEvidenceController(t, root)
	var wakes atomic.Int32
	root.SetNotifyFunc(func() { wakes.Add(1) })
	for _, stale := range []bool{true, false} {
		d := root.createDelegate(context.Background(), delegateArgs{Task: "original stale/rearmed attention"})
		if d.Err != nil {
			t.Fatal(d.Err)
		}
		sub := root.subagents.get(d.ChildSessionID)
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		retirementAwait(t, done)
		path := transcriptPath(root.stateDir, root.id)
		original, err := readDelegateAttentionFold(path, root.id)
		if err != nil || len(original.pendingIDs()) != 1 {
			t.Fatalf("original source not pending: %v %v", original.pendingIDs(), err)
		}
		id := original.pendingIDs()[0]
		adapter.fail.Store(true)
		if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); !errors.Is(err, context.Canceled) {
			t.Fatalf("provider failure = %v", err)
		}
		adapter.fail.Store(false)
		root.attentionMu.Lock()
		active := root.rootAttentionRetry.active
		root.attentionMu.Unlock()
		if !active {
			t.Fatal("real failure did not schedule retry")
		}
		if !stale {
			beforeWake := wakes.Load()
			clk.Advance(jobNotificationRetryInitialDelay)
			clk.Drain()
			if wakes.Load() != beforeWake+1 {
				t.Fatalf("retry after Abort did not wake: %d -> %d", beforeWake, wakes.Load())
			}
		}
		beforeRequests := len(adapter.Requests())
		if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		beforeClaim, err := readDelegateAttentionFold(path, root.id)
		if err != nil || !reflect.DeepEqual(beforeClaim.content[id], original.content[id]) || beforeClaim.resolutions[id] != delegateAttentionConsumed || !requestsContain(adapter.Requests()[beforeRequests:], original.content[id].Text()) {
			t.Fatalf("original source not delivered/consumed: %v", err)
		}
		claim, state, err := c.TryClaim(true)
		if err != nil || claim == nil {
			t.Fatalf("settled source remained blocked: %+v %v", state, err)
		}
		beforeWake := wakes.Load()
		clk.Advance(jobNotificationRetryInitialDelay)
		clk.Drain()
		afterClaim, readErr := readDelegateAttentionFold(path, root.id)
		root.attentionMu.Lock()
		active = root.rootAttentionRetry.active
		root.attentionMu.Unlock()
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
		if readErr != nil || !reflect.DeepEqual(afterClaim, beforeClaim) || active || wakes.Load() != beforeWake {
			t.Fatalf("stale callback changed original source or stranded retry: %v active=%t wakes=%d/%d", readErr, active, beforeWake, wakes.Load())
		}
	}
	assertRetirementEvidenceEligible(t, c)
}

// A delegate attention arm that failed and owes a retry is session-level
// pending work: sessionWorkPending already includes
// hasPendingDelegateAttentionArmRetry, so retirement evidence must read the
// same owner state and keep the session blocked until the retry callback
// settles the exact arm it retained.
func TestRetirementAttentionArmRetryPending(t *testing.T) {
	clk := agenttest.NewFakeClock()
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk, MaxSubagentDepth: 1}))
	defer root.Close()
	c := retirementEvidenceController(t, root)
	retirementIdleDelegate(t, root)
	assertRetirementEvidenceEligible(t, c)
	injected := errors.New("injected attention arm fold failure")
	root.cfg.testOnly.delegateAttentionReadFold = func(string, string) (delegateAttentionFold, error) {
		return delegateAttentionFold{}, injected
	}
	if err := root.armDelegateAttention("original-arm-retry-attention"); !errors.Is(err, injected) {
		t.Fatalf("arm = %v, want %v", err, injected)
	}
	root.attentionMu.Lock()
	_, held := root.delegateAttentionArmIDs["original-arm-retry-attention"]
	retryActive := root.delegateAttentionArmRetry.active
	root.attentionMu.Unlock()
	if !held || !retryActive {
		t.Fatalf("failed arm not retained for retry: held=%t active=%t", held, retryActive)
	}
	claim, state, err := c.TryClaim(true)
	if claim != nil {
		if abortErr := c.Abort(claim, ""); abortErr != nil {
			t.Fatal(abortErr)
		}
	}
	if claim != nil || err != nil {
		t.Fatalf("pending attention arm retry escaped: %+v %v", state, err)
	}
	if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "notification" && b.SessionID == root.id
	}) {
		t.Fatalf("arm retry evidence lost its original owner: %+v", state)
	}
	// Real settlement: the armed retry callback re-arms and clears the exact ID.
	root.cfg.testOnly.delegateAttentionReadFold = nil
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	root.attentionMu.Lock()
	remaining := len(root.delegateAttentionArmIDs)
	stillActive := root.delegateAttentionArmRetry.active
	root.attentionMu.Unlock()
	if remaining != 0 || stillActive {
		t.Fatalf("settled arm retry still pending: ids=%d active=%t", remaining, stillActive)
	}
	assertRetirementEvidenceEligible(t, c)
}

// Sandbox teardown is terminal owner work: while Close reaps the session's
// execution environment, a retirement claim would tear the same runtime down
// under it. The closing session must block claims, and the settled (closed)
// session must keep blocking them — retirement of a torn-down runtime is
// never eligible.
func TestRetirementSandboxTeardownBlocks(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	// This test owns the close; no deferred root.Close().
	c := retirementEvidenceController(t, root)
	assertRetirementEvidenceEligible(t, c)
	entered := make(chan struct{})
	resume := make(chan struct{})
	root.cfg.testOnly.envCleanupObserved = func(execenv.ExecutionEnvironment) {
		close(entered)
		<-resume
	}
	closed := make(chan struct{})
	go func() {
		root.Close()
		close(closed)
	}()
	<-entered
	claim, state, err := c.TryClaim(true)
	if claim != nil {
		if abortErr := c.Abort(claim, ""); abortErr != nil {
			t.Fatal(abortErr)
		}
	}
	if claim != nil || err != nil {
		t.Fatalf("sandbox teardown escaped: %+v %v", state, err)
	}
	if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "delegate" && b.SessionID == root.id
	}) {
		t.Fatalf("teardown evidence lost its original owner: %+v", state)
	}
	close(resume)
	<-closed
	claim, state, err = c.TryClaim(true)
	if claim != nil {
		if abortErr := c.Abort(claim, ""); abortErr != nil {
			t.Fatal(abortErr)
		}
	}
	if claim != nil || err != nil {
		t.Fatalf("settled teardown escaped: %+v %v", state, err)
	}
	if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "delegate" && b.SessionID == root.id
	}) {
		t.Fatalf("settled teardown evidence lost its original owner: %+v", state)
	}
}

// An admitted environment operation must prevent a claim until its real owner
// ends it; omitting environment ownership from retirement makes this fail.
func TestRetirementEnvironmentLeaseBlocks(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	work, ok := root.beginEnvWork("retirement-test")
	if !ok {
		t.Fatal("environment work was refused")
	}
	claim, state, err := c.TryClaim(true)
	root.endEnvWork(work)
	if claim != nil {
		defer func() {
			if err := c.Abort(claim, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil || !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "environment" || b.Category == "admission"
	}) {
		t.Fatalf("environment operation escaped: %+v", state)
	}
	settled, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	if settled == nil {
		t.Fatalf("settled environment remained blocked: %+v", state)
	}
	if err := c.Abort(settled, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementEnvironmentClaimFirstRefuses(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("initial claim: %+v, %v", state, err)
	}
	defer func() {
		if err := c.Abort(claim, ""); err != nil {
			t.Error(err)
		}
	}()
	work, ok := root.beginEnvWork("refused-work")
	if ok {
		root.endEnvWork(work)
		t.Fatal("environment work admitted after claim")
	}
	if labels := root.outstandingEnvWork(); len(labels) != 0 {
		t.Fatalf("refusal registered work: %v", labels)
	}
}

// A direct setter must not bypass a claim even when no daemon route wraps it.
func TestRetirementSafetyDirectSetterClaimFirst(t *testing.T) {
	for _, setter := range []string{"Rename", "ClearGoal"} {
		t.Run(setter, func(t *testing.T) {
			dir := t.TempDir()
			root := newQueuePersistTestSession(t, dir)
			defer root.Close()
			if err := root.Rename("original-name"); err != nil {
				t.Fatal(err)
			}
			if _, err := root.SetGoal(context.Background(), "original-objective"); err != nil {
				t.Fatal(err)
			}
			if _, changed := root.setGoalTerminal(goal.StatusComplete, ""); !changed {
				t.Fatal("goal did not complete")
			}
			root.reportGoalEnded()
			root.maybeAutoSave()
			before := root.Meta()
			persisted, err := schema.LoadSessionMeta(dir, root.ID())
			if err != nil {
				t.Fatal(err)
			}
			drainRetirementTestEvents(root)
			c, err := NewRetirementController(0, clock.Real())
			if err != nil {
				t.Fatal(err)
			}
			if err := c.AttachRoot(root); err != nil {
				t.Fatal(err)
			}
			claim, state, err := c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("initial claim: %+v, %v", state, err)
			}
			defer func() {
				if err := c.Abort(claim, ""); err != nil {
					t.Error(err)
				}
			}()
			switch setter {
			case "Rename":
				err = root.Rename("refused-name")
			case "ClearGoal":
				err = root.ClearGoal()
			}
			if !errors.Is(err, ErrRetirementUnavailable) {
				t.Errorf("setter error = %v", err)
			}
			after := root.Meta()
			if before.Name != after.Name || before.NameSource != after.NameSource || !reflect.DeepEqual(before.Goal, after.Goal) {
				t.Errorf("refused setter changed original state: before=%+v after=%+v", before, after)
			}
			got, err := schema.LoadSessionMeta(dir, root.ID())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(persisted, got) {
				t.Error("refused setter changed original persisted record")
			}
			if emitted := drainRetirementTestEvents(root); len(emitted) != 0 {
				t.Errorf("refused setter emitted events: %v", emitted)
			}
		})
	}
}

// The maintenance timer must fence launch before the first Git operation and
// retain ownership through its real control-environment cleanup. The runner
// barrier delegates to the actual Git implementation; no owner state is forged.
func TestRetirementAutonomousSweep(t *testing.T) {
	for _, order := range []string{"claim-first", "callback-first"} {
		t.Run(order, func(t *testing.T) {
			cfg := worktreeTestSessionConfig()
			cfg.clock = agenttest.NewFakeClock()
			r := newWorktreeRepoWithConfig(t, cfg)
			root := r.s
			c := retirementEvidenceController(t, root)
			assertRetirementEvidenceEligible(t, c)
			before, err := gitRunner(t.Context(), root.currentEnv())("worktree", "list", "--porcelain")
			if err != nil {
				t.Fatal(err)
			}
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			root.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
				next := gitRunner(ctx, env)
				return func(args ...string) (string, error) {
					if calls.Add(1) == 1 && order == "callback-first" {
						close(entered)
						<-resume
					}
					return next(args...)
				}
			}
			if order == "claim-first" {
				claim, state, err := c.TryClaim(true)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v, %v", state, err)
				}
				root.fireOpenLaneResidueSweep()
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 0 {
					t.Fatalf("refused maintenance performed %d Git calls", calls.Load())
				}
				root.fireOpenLaneResidueSweep()
			} else {
				go func() { defer close(done); root.fireOpenLaneResidueSweep() }()
				defer func() { close(resume); <-done }()
				select {
				case <-entered:
				// TRIPWIRE: hang guard only; the real signal is <-entered from
				// the actual Git boundary, which is reached in milliseconds.
				case <-time.After(5 * time.Second):
					t.Fatal("sweep did not reach real Git boundary")
				}
				assertRetirementEvidenceBlocked(t, c, "environment")
				// Let the real sweep and control-environment disposal settle before
				// checking eligibility; deferred cleanup also releases failed REDs.
				resume <- struct{}{}
				<-done
			}
			if calls.Load() == 0 {
				t.Fatal("resumed sweep never performed Git work")
			}
			after, err := gitRunner(t.Context(), root.currentEnv())("worktree", "list", "--porcelain")
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("maintenance changed original registry: before %q after %q", before, after)
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func TestRetirementAutonomousReLock(t *testing.T) {
	for _, order := range []string{"claim-first", "resume-first", "pending-retry", "retry-first"} {
		t.Run(order, func(t *testing.T) {
			cfg := worktreeTestSessionConfig()
			cfg.clock = agenttest.NewFakeClock()
			cfg.StateDir = t.TempDir()
			cfg.testOnly.minimalWorktreeToolRegistry = false
			mainRoot := t.TempDir()
			copyWorktreeBaseRepo(t, mainRoot)
			root := newSession(t, withDir(mainRoot), withConfig(cfg))
			r := &wtRepo{s: root, mainRoot: mainRoot, stateDir: cfg.StateDir}
			root.client.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
			result := root.createDelegate(t.Context(), delegateArgs{Task: "retained relock source", Isolation: "worktree"})
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			retirementSettleDelegate(t, root, result)
			if result.Worktree == nil {
				t.Fatal("real isolated delegate omitted worktree")
			}
			lane := result.Worktree.Path
			original := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
			c := retirementEvidenceController(t, root)
			assertRetirementEvidenceEligible(t, c)
			wtGit(t, r.mainRoot, "worktree", "unlock", lane)
			entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			var failLock atomic.Bool
			var pause atomic.Bool
			pause.Store(order == "resume-first")
			failLock.Store(order == "pending-retry" || order == "retry-first")
			root.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
				next := gitRunner(ctx, env)
				return func(args ...string) (string, error) {
					calls.Add(1)
					if pause.CompareAndSwap(true, false) {
						close(entered)
						<-resume
					}
					if len(args) > 1 && args[0] == "worktree" && args[1] == "lock" && failLock.Load() {
						return "", errors.New("fixture Git lock refusal")
					}
					return next(args...)
				}
			}
			if order == "claim-first" {
				claim, state, err := c.TryClaim(true)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v, %v", state, err)
				}
				root.resumeReLockOwnLanes()
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 0 {
					t.Fatalf("refused relock performed %d Git calls", calls.Load())
				}
				if entry := r.porcelainEntry(t, lane); entry.Locked {
					t.Fatal("refused relock changed original lane")
				}
				root.resumeReLockOwnLanes()
			} else if order == "resume-first" {
				go func() { defer close(done); root.resumeReLockOwnLanes() }()
				defer func() { close(resume); <-done }()
				select {
				case <-entered:
				// TRIPWIRE: hang guard only; the real signal is <-entered from
				// the actual Git boundary, which is reached in milliseconds.
				case <-time.After(5 * time.Second):
					t.Fatal("resume did not reach real Git boundary")
				}
				assertRetirementEvidenceBlocked(t, c, "environment")
				resume <- struct{}{}
				<-done
			} else {
				root.resumeReLockOwnLanes()
				root.mu.Lock()
				pending := slices.Clone(root.pendingReLock)
				root.mu.Unlock()
				if len(pending) != 1 || pending[0].delegateID != result.DelegateID || pending[0].path != lane {
					t.Fatalf("original pending retry: %+v", pending)
				}
				if order == "pending-retry" {
					assertRetirementEvidenceBlocked(t, c, "environment")
				}
				root.mu.Lock()
				retained := slices.Clone(root.pendingReLock)
				root.mu.Unlock()
				if !reflect.DeepEqual(pending, retained) {
					t.Fatalf("claim consumed retry: %+v", retained)
				}
				failLock.Store(false)
				if order == "retry-first" {
					pause.Store(true)
					go func() { defer close(done); root.fireLaneReLockRetry() }()
					defer func() { close(resume); <-done }()
					select {
					case <-entered:
					// TRIPWIRE: hang guard only; the real signal is <-entered
					// from the actual Git boundary.
					case <-time.After(5 * time.Second):
						t.Fatal("retry did not reach real Git boundary")
					}
					root.mu.Lock()
					drained := len(root.pendingReLock) == 0
					root.mu.Unlock()
					if !drained {
						t.Fatal("retry barrier precedes pending-source drain")
					}
					assertRetirementEvidenceBlocked(t, c, "environment")
					resume <- struct{}{}
					<-done
				} else {
					root.fireLaneReLockRetry()
				}
			}
			if entry := r.porcelainEntry(t, lane); !entry.Locked || entry.LockReason != worktree.FormatDelegateMarker(result.DelegateID, root.ID()) {
				t.Fatalf("original lane not reclaimed: %+v", entry)
			}
			if current := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID); !reflect.DeepEqual(original, current) {
				t.Fatal("retirement/relock changed original delegate source")
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func TestRetirementSafetyEscalation(t *testing.T) {
	for _, order := range []string{"claim-first", "pending", "resolved-rerun", "pre-attach", "close"} {
		t.Run(order, func(t *testing.T) {
			home := t.TempDir()
			workdir := filepath.Join(home, "work")
			if err := os.Mkdir(workdir, 0o755); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "original.txt")
			originalBytes := []byte("original escalation content")
			if err := os.WriteFile(outside, originalBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			// Only in-process file-tool enforcement is exercised, not the bwrap
			// executable or kernel sandbox capability represented by these facts.
			policy, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}, workdir)
			if err != nil {
				t.Fatal(err)
			}
			env := execenv.NewLocalExecutionEnvironment(workdir)
			env.Sandbox = &policy
			client := llm.NewClient()
			client.Register(&fakeAdapter{name: "openai"})
			root, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true}})
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			root.SetSubscriberCountFunc(func() int { return 1 })
			call := llm.ToolCallData{ID: "original-denied-read", Name: "read_file", Arguments: []byte(fmt.Sprintf(`{"file_path":%q}`, outside))}
			original := root.reg.ExecuteCall(t.Context(), env, call)
			if denied, ok := sandbox.AsDenied(original.Err); !ok || denied.Path != outside || !denied.ReasonKind.Curable() {
				t.Fatalf("real read did not produce original curable denial: %+v", original)
			}
			var c *RetirementController
			if order != "pre-attach" {
				c = retirementEvidenceController(t, root)
			}
			rerunEntered, resume := make(chan struct{}), make(chan struct{})
			rerun := func(ctx context.Context) tooldefs.ExecResult {
				if order == "resolved-rerun" || order == "pre-attach" {
					close(rerunEntered)
					<-resume
				}
				grant, ok := invocationGrant(ctx)
				if !ok || grant != outside {
					return tooldefs.ExecResult{IsError: true, Err: errors.New("original invocation grant missing")}
				}
				return root.reg.ExecuteCall(ctx, env.WithSandboxInvocationGrant(grant), call)
			}
			if order == "claim-first" {
				claim, state, err := c.TryClaim(true)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", state, err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel() // bounds a broken admission path without fabricating a phase
				got := root.escalateOnSandboxDenial(ctx, call.Name, original, rerun)
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(original, got) {
					t.Fatal("refusal changed original typed denial")
				}
				for _, kind := range drainRetirementTestEvents(root) {
					if kind == events.EventSandboxEscalationRequested {
						t.Fatal("claim-first published escalation")
					}
				}
			} else {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan tooldefs.ExecResult, 1)
				finished := make(chan struct{})
				go func() { defer close(finished); done <- root.escalateOnSandboxDenial(ctx, call.Name, original, rerun) }()
				defer func() { cancel(); close(resume); <-finished }()
				var card events.SandboxEscalationRequestedData
				for card.EscalationID == "" {
					select {
					case ev := <-root.Events():
						if ev.Kind == events.EventSandboxEscalationRequested {
							card = ev.Data.(events.SandboxEscalationRequestedData)
						}
					// TRIPWIRE: hang guard only; the real signal is the actual
					// EventSandboxEscalationRequested event on root.Events().
					case <-time.After(5 * time.Second):
						t.Fatal("actual escalation card not published")
					}
				}
				if card.DeniedPath != outside {
					t.Fatalf("original denied path changed: %+v", card)
				}
				if order == "close" {
					joined, continueClose, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
					root.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(joined); <-continueClose }
					go func() { defer close(closed); root.Close() }()
					<-joined
					stillPending := root.HasPendingEscalations()
					// Release the broken implementation without waiting its close
					// budget only on failure. On success, Close must supply the
					// original denial without cancellation from the test context.
					if stillPending {
						cancel()
					}
					close(continueClose)
					<-closed
					got := <-done
					if stillPending {
						t.Error("Close reached environment join before denying original escalation")
					} else if err := ctx.Err(); err != nil {
						t.Errorf("Close denial relied on caller context cancellation: %v", err)
					}
					if !reflect.DeepEqual(original, got) {
						t.Error("Close changed original typed denial")
					}
					return
				}
				if order == "pre-attach" {
					c = retirementEvidenceController(t, root)
				}
				if order == "pending" {
					before := root.PendingEscalations()
					assertRetirementEvidenceBlocked(t, c, "environment")
					if !reflect.DeepEqual(before, root.PendingEscalations()) {
						t.Fatal("claim consumed original escalation")
					}
				}
				if err := root.ResolveSandboxEscalation(card.EscalationID, true); err != nil {
					t.Fatal(err)
				}
				if order != "pending" {
					select {
					case <-rerunEntered:
					// TRIPWIRE: hang guard only; the real signal is
					// <-rerunEntered once the approved rerun starts.
					case <-time.After(5 * time.Second):
						t.Fatal("approved rerun not entered")
					}
					if root.HasPendingEscalations() {
						t.Fatal("rerun barrier precedes actual resolution")
					}
					assertRetirementEvidenceBlocked(t, c, "environment")
					resume <- struct{}{}
				}
				got := <-done
				if got.IsError || !bytes.Contains([]byte(got.FullOutput), originalBytes) {
					t.Fatalf("original read did not settle through grant: %+v", got)
				}
			}
			got, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(got, originalBytes) {
				t.Fatalf("original file changed: %q %v", got, err)
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func retirementEvidenceController(t *testing.T, root *Session) *RetirementController {
	t.Helper()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	return c
}

func assertRetirementEvidenceBlocked(t *testing.T, c *RetirementController, category string) {
	t.Helper()
	claim, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("%s work escaped: %+v", category, state)
	}
	if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool { return b.Category == category }) {
		t.Fatalf("missing %s evidence: %+v", category, state)
	}
}

func assertRetirementEvidenceEligible(t *testing.T, c *RetirementController) {
	t.Helper()
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("settled owner not eligible: %+v, %v", state, err)
	}
	if err := c.Abort(claim, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementSafetyDirectSetterAdmissionFirst(t *testing.T) {
	for _, setter := range []string{"Rename", "ClearGoal"} {
		t.Run(setter, func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), spawn: spawnConfig{
				descendantEvent: func(ev events.SessionEvent) {
					if (ev.Kind == events.EventSessionNameChanged || ev.Kind == events.EventGoalUpdated) && armed.CompareAndSwap(true, false) {
						close(entered)
						<-resume
					}
				},
			}}))
			c := retirementEvidenceController(t, root)
			armed.Store(true)
			done := make(chan error, 1)
			go func() {
				if setter == "Rename" {
					done <- root.Rename("accepted-name")
				} else {
					done <- root.ClearGoal()
				}
			}()
			<-entered // actual external event receiver, after mutation, before return
			claim, state, err := c.TryClaim(true)
			close(resume)
			if result := <-done; result != nil {
				t.Fatal(result)
			}
			if err != nil || claim != nil {
				t.Fatalf("claim passed unsettled setter event: %+v, %v", state, err)
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func TestRetirementAutonomousActiveGoal(t *testing.T) {
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}), withAdapter(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "goal-settle", Name: "update_goal", Type: "function", Arguments: []byte(`{"status":"complete","intent":"settling original goal"}`)})
		},
		func(llm.Request) llm.Response { return finalResponse("settled") },
	}}))
	c := retirementEvidenceController(t, root)
	if _, err := root.SetGoal(context.Background(), "original-goal"); err != nil {
		t.Fatal(err)
	}
	assertRetirementEvidenceBlocked(t, c, "autonomous")
	if _, err := root.ProcessInput(context.Background(), "complete", nil); err != nil {
		t.Fatal(err)
	}
	if got := root.Meta().Goal; got == nil || got.Objective != "original-goal" || got.Status != "complete" {
		t.Fatalf("original goal not settled by tool: %+v", got)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementQuestionPendingAndReply(t *testing.T) {
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}), withAdapter(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolCallResponse(askUserCall("original-question", askUserArgsValid()))
		},
		func(llm.Request) llm.Response { return finalResponse("answered") },
	}}))
	c := retirementEvidenceController(t, root)
	if _, err := root.ProcessInput(context.Background(), "ask", nil); err != nil {
		t.Fatal(err)
	}
	if !root.HasPendingAsk() {
		t.Fatal("scripted tool did not register question")
	}
	before := currentHistory(t, root)
	assertRetirementEvidenceBlocked(t, c, "question")
	if !root.HasPendingAsk() || !reflect.DeepEqual(before, currentHistory(t, root)) {
		t.Fatal("claim changed original question source")
	}
	if _, err := root.ProcessInput(context.Background(), "answer", nil); err != nil {
		t.Fatal(err)
	}
	if root.HasPendingAsk() {
		t.Fatal("real answer did not settle pending question")
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementSafetyShellRecordLifecycle(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)
	rec, err := root.jobManager.createShell(createShellOpts{Command: "fixture-owned"})
	if err != nil {
		t.Fatal(err)
	}
	assertRetirementEvidenceBlocked(t, c, "job")
	// This is the owner's terminal-read settlement path: a result consumed
	// synchronously owes no asynchronous completion notification.
	if err := root.jobManager.finalizeWithRunNoNotification(rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) {
		return jobstore.StatusCompleted, "", nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := root.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := stored[rec.JobID]; got == nil || got.JobID != rec.JobID || got.Status != jobstore.StatusCompleted {
		t.Fatalf("original shell record not settled: %+v", got)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementSafetyShellClaimFirst(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", state, err)
	}
	defer func() {
		if err := c.Abort(claim, ""); err != nil {
			t.Error(err)
		}
	}()
	before, err := root.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, err = root.jobManager.createShell(createShellOpts{Command: "must-not-publish"})
	if !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("shell admission error: %v", err)
	}
	after, err := root.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("refused shell created durable source")
	}
}

func TestRetirementWatchActiveRegistration(t *testing.T) {
	for _, trigger := range []string{"event", "one-shot", "repeating"} {
		for _, claimFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/claim-first-%t", trigger, claimFirst), func(t *testing.T) {
				root := newQueuePersistTestSession(t, t.TempDir())
				defer root.Close()
				c := retirementEvidenceController(t, root)
				a := watchArgs{Target: "caller", Source: "self"}
				switch trigger {
				case "event":
					a.Events = []string{"communicate"}
				case "one-shot":
					a.AfterSeconds = 60
				case "repeating":
					a.RepeatSeconds = 60
				}
				var claim *RetirementClaim
				if claimFirst {
					var err error
					claim, _, err = c.TryClaim(true)
					if err != nil || claim == nil {
						t.Fatalf("claim: %v", err)
					}
					defer func() {
						if err := c.Abort(claim, ""); err != nil {
							t.Error(err)
						}
					}()
				}
				registered, err := root.jobManager.configureWatch(a)
				if claimFirst {
					if !errors.Is(err, ErrRetirementUnavailable) {
						t.Fatalf("watch admitted behind claim: %+v %v", registered, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				assertRetirementEvidenceBlocked(t, c, "watch")
				if _, err := root.jobManager.clearWatchByID(registered.WatchID); err != nil {
					t.Fatal(err)
				}
				assertRetirementEvidenceEligible(t, c)
			})
		}
	}
}

func TestRetirementEnvironmentPreAttachWork(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	work, ok := root.beginEnvWork("original-operation")
	if !ok {
		t.Fatal("begin refused")
	}
	defer root.endEnvWork(work)
	c := retirementEvidenceController(t, root)
	assertRetirementEvidenceBlocked(t, c, "environment")
}

func TestRetirementEnvironmentSwapClaimFirst(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)
	original := root.env.(*execenv.LocalExecutionEnvironment)
	next := original.WithWorkingDirectory(t.TempDir())
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", state, err)
	}
	defer func() {
		if err := c.Abort(claim, ""); err != nil {
			t.Error(err)
		}
	}()
	if err := root.swapEnvAndRefresh(next, nil); !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("swap admission = %v", err)
	}
	if root.env != original {
		t.Error("refused swap replaced original environment")
	}
}

func TestRetirementEnvironmentDispose(t *testing.T) {
	for _, claimFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("claim-first-%t", claimFirst), func(t *testing.T) {
			root := newQueuePersistTestSession(t, t.TempDir())
			defer root.Close()
			c := retirementEvidenceController(t, root)
			if claimFirst {
				claim, state, err := c.TryClaim(true)
				if err != nil || claim == nil {
					t.Fatalf("claim: %+v %v", state, err)
				}
				defer func() {
					if err := c.Abort(claim, ""); err != nil {
						t.Error(err)
					}
				}()
				if root.beginDispose() {
					root.endDispose()
					t.Fatal("dispose admitted behind claim")
				}
				return
			}
			if !root.beginDispose() {
				t.Fatal("dispose refused")
			}
			claim, state, err := c.TryClaim(true)
			root.endDispose()
			if claim != nil {
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err != nil || claim != nil {
				t.Fatalf("dispose escaped: %+v %v", state, err)
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func TestRetirementQuestionDirectClaimFirst(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c := retirementEvidenceController(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", state, err)
	}
	defer func() {
		if err := c.Abort(claim, ""); err != nil {
			t.Error(err)
		}
	}()
	result := root.reg.ExecuteCall(context.Background(), root.env, askUserCall("refused-question", askUserArgsValid()))
	if !result.IsError || !errors.Is(result.Err, ErrRetirementUnavailable) {
		t.Errorf("question admitted behind claim: %+v", result)
	}
	if root.HasPendingAsk() {
		t.Fatal("refused question published")
	}
}

func TestRetirementEnvironmentOverlappingDisposals(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	// One caller is admitted before process-controller attachment, the second
	// after it. Settlement of the later caller cannot erase the earlier work.
	if !root.beginDispose() {
		t.Fatal("first dispose refused")
	}
	c := retirementEvidenceController(t, root)
	if !root.beginDispose() {
		root.endDispose()
		t.Fatal("second dispose refused")
	}
	root.endDispose() // second caller settles first
	claim, state, err := c.TryClaim(true)
	root.endDispose() // first caller settles last
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err != nil || claim != nil {
		t.Fatalf("partial disposal settlement escaped: %+v %v", state, err)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementWatchFiredOneShot(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("notification-callback-paused-%t", paused), func(t *testing.T) {
			clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
			adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return finalResponse("timer acknowledged") },
			}}
			root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(adapter))
			c := retirementEvidenceController(t, root)
			registered, err := root.jobManager.configureWatch(watchArgs{Target: "caller", Source: "self", AfterSeconds: 60, Note: "original-timer-note"})
			if err != nil {
				t.Fatal(err)
			}
			jm := root.jobManager
			jm.mu.Lock()
			key, cfg, ok := jm.watchConfigByIDLocked(registered.WatchID)
			jm.mu.Unlock()
			if !ok {
				t.Fatal("timer not registered")
			}
			before, err := jm.store.LoadWatches()
			if err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			if paused {
				// Public external wake receiver: the actual callback has detached
				// the one-shot and queued its frame, but has not handed off the wake.
				root.SetNotifyFunc(func() { close(entered); <-resume })
			}
			done := make(chan bool, 1)
			go func() { done <- jm.fireProgressTick(key, cfg) }()
			if paused {
				<-entered
				jm.mu.Lock()
				_, _, live := jm.watchConfigByIDLocked(registered.WatchID)
				jm.mu.Unlock()
				claim, state, err := c.TryClaim(true)
				close(resume)
				if keep := <-done; keep {
					t.Error("one-shot kept timer alive")
				}
				root.SetNotifyFunc(nil)
				if claim != nil {
					if err := c.Abort(claim, ""); err != nil {
						t.Fatal(err)
					}
				}
				// BeginMutation retains the caller's category, not "admission".
				// No live watch remains, so this watch blocker is the firing lease.
				if live || err != nil || claim != nil || !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool { return b.Category == "watch" }) {
					t.Fatalf("timer callback escaped before wake handoff: %+v %v", state, err)
				}
			} else if keep := <-done; keep {
				t.Fatal("one-shot kept timer alive")
			}
			root.pendingJobNotifsMu.Lock()
			pending := slices.Clone(root.pendingJobNotifs)
			root.pendingJobNotifsMu.Unlock()
			if len(pending) != 1 || pending[0].WatchID != registered.WatchID || !pending[0].Terminal || pending[0].Note != "original-timer-note" {
				t.Fatalf("original timer source missing: %+v", pending)
			}
			assertRetirementEvidenceBlocked(t, c, "notification")
			root.pendingJobNotifsMu.Lock()
			unchanged := reflect.DeepEqual(pending, root.pendingJobNotifs)
			root.pendingJobNotifsMu.Unlock()
			if !unchanged {
				t.Fatal("claim changed original pending timer source")
			}
			after, err := jm.store.LoadWatches()
			if err != nil {
				t.Fatal(err)
			}
			if before[registered.WatchID] == nil || after[registered.WatchID] == nil || before[registered.WatchID].Generation != after[registered.WatchID].Generation {
				t.Fatal("timer source generation changed")
			}
			if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if !requestsContain(adapter.Requests(), registered.WatchID, "original-timer-note") {
				t.Fatal("original timer frame did not reach provider")
			}
			if root.peekNotifications() != 0 {
				t.Fatal("real notification turn did not settle timer")
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func TestRetirementWatchTerminalPendingSend(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("terminal watch acknowledged") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}), withAdapter(adapter))
	c := retirementEvidenceController(t, root)
	jm := root.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "original-watched-shell"})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := jm.configureWatch(watchArgs{Target: rec.JobID, Source: rec.JobID, OutputMatch: "never-seen", Send: &watchSendArgs{To: "caller", Message: "original-send-message"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
		t.Fatal(err)
	}
	original, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Pending) != 1 {
		t.Fatalf("real terminal source not populated: %+v", original)
	}
	var source jobstore.WatchSendState
	for _, p := range original.Pending {
		source = *p
	}
	if source.Key.WatchID != registered.WatchID || source.DeliveryID == "" || source.Key.WatchGeneration == "" {
		t.Fatalf("missing source identity: %+v", source)
	}
	jm.mu.Lock()
	_, _, live := jm.watchConfigByIDLocked(registered.WatchID)
	jm.mu.Unlock()
	if live {
		t.Fatal("terminal watch still live")
	}
	assertRetirementEvidenceBlocked(t, c, "watch")
	after, err := jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, after) {
		t.Fatal("claim altered original pending watch record")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if !requestsContain(adapter.Requests(), source.DeliveryID, "original-send-message") {
		t.Fatal("original pending send never reached provider")
	}
	after, err = jm.store.LoadWatchSends()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Pending) != 0 {
		t.Fatalf("real turn left pending sends: %+v", after)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementAutonomousNaming(t *testing.T) {
	for _, source := range []string{"prompt", "compaction"} {
		for _, order := range []string{"claim-first", "launch-first", "pre-attach"} {
			t.Run(source+"/"+order, func(t *testing.T) {
				root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
				entered, resume := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				namer := llm.NewClient()
				namer.Register(&agenttest.ScriptedAdapter{Provider: root.currentProfile().CheapProvider(), Responder: func(llm.Request) llm.Response {
					calls.Add(1)
					if order != "claim-first" {
						close(entered)
						<-resume
					}
					return llm.Response{Message: llm.Assistant(`{"name":"Original Naming Result"}`)}
				}})
				updateSessionTestConfig(root, func(cfg *testConfig) { cfg.namerClient = namer })
				launch := func() {
					if source == "prompt" {
						root.launchInitialPromptNamer(context.Background(), "original naming input")
					} else {
						root.launchCompactionNamer(context.Background(), schema.Turn{Kind: schema.TurnSummary, Message: llm.Assistant("original naming summary")})
					}
				}
				var c *RetirementController
				if order != "pre-attach" {
					c = retirementEvidenceController(t, root)
				}
				if order == "claim-first" {
					claim, state, err := c.TryClaim(true)
					if err != nil || claim == nil {
						t.Fatalf("claim: %+v %v", state, err)
					}
					before := root.Meta()
					launch()
					root.sendersWG.Wait()
					after := root.Meta()
					if err := c.Abort(claim, ""); err != nil {
						t.Fatal(err)
					}
					if calls.Load() != 0 || before.Name != after.Name || before.NameSource != after.NameSource {
						t.Fatalf("claim launched naming effects: calls=%d before=%+v after=%+v", calls.Load(), before, after)
					}
					return
				}
				launch()
				<-entered // scripted external provider is holding the actual naming call
				if c == nil {
					c = retirementEvidenceController(t, root)
				}
				claim, state, err := c.TryClaim(true)
				close(resume)
				root.sendersWG.Wait() // real naming writes/event/autosave have settled
				if claim != nil {
					if err := c.Abort(claim, ""); err != nil {
						t.Fatal(err)
					}
				}
				if err != nil || claim != nil {
					t.Fatalf("naming work escaped: %+v %v", state, err)
				}
				if calls.Load() != 1 || root.Meta().Name != "Original Naming Result" {
					t.Fatalf("original naming call did not settle: calls=%d meta=%+v", calls.Load(), root.Meta())
				}
				assertRetirementEvidenceEligible(t, c)
			})
		}
	}
}

func TestRetirementAutonomousNotificationRetryWake(t *testing.T) {
	for _, attachBefore := range []bool{true, false} {
		t.Run(fmt.Sprintf("attach-before=%t", attachBefore), func(t *testing.T) {
			retirementNotificationRetryWake(t, attachBefore)
		})
	}
}

func retirementNotificationRetryWake(t *testing.T, attachBefore bool) {
	t.Helper()
	clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("original notification settled") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(adapter))
	var c *RetirementController
	if attachBefore {
		c = retirementEvidenceController(t, root)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	root.SetNotifyFunc(func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-resume
		}
	})
	rec, err := root.jobManager.createShell(createShellOpts{Command: "original-retry-shell"})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
		t.Fatal(err)
	}
	before, err := root.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before[rec.JobID] == nil || before[rec.JobID].NotifyState != jobstore.NotifyPending || before[rec.JobID].TerminalGen == "" {
		t.Fatalf("source not armed: %+v", before[rec.JobID])
	}
	// Existing drain/requeue owner APIs retain this exact terminal source and
	// arm its actual backoff path; no test writes retry flags or queue entries.
	original := root.drainJobNotifications()
	if len(original) != 1 || original[0].JobID != rec.JobID {
		t.Fatalf("source notification missing: %+v", original)
	}
	root.requeueJobNotifications(original)
	clk.BlockUntil(1)
	armed.Store(true)
	clk.Advance(jobNotificationRetryInitialDelay)
	<-entered // the retry cleared active and is still inside its external wake
	if !attachBefore {
		c = retirementEvidenceController(t, root)
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		close(resume)
		clk.Drain()
		t.Fatal(err)
	}
	settled, err := root.jobManager.store.Load()
	if err != nil {
		close(resume)
		clk.Drain()
		t.Fatal(err)
	}
	claim, state, claimErr := c.TryClaim(true)
	close(resume)
	clk.Drain()
	root.SetNotifyFunc(nil)
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := settled[rec.JobID]; got == nil || got.TerminalGen != before[rec.JobID].TerminalGen || got.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("original receipt not settled: %+v", got)
	}
	if !requestsContain(adapter.Requests(), rec.JobID) || root.peekNotifications() != 0 {
		t.Fatal("original source did not actually reach provider and settle")
	}
	if claimErr != nil || claim != nil {
		t.Fatalf("unsettled retry wake escaped: %+v %v", state, claimErr)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementAutonomousNotificationRetryEmptySource(t *testing.T) {
	for _, settlement := range []string{"status-consume", "notification-turn"} {
		t.Run(settlement, func(t *testing.T) {
			clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
			adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return finalResponse("first settled") },
				func(llm.Request) llm.Response { return finalResponse("second settled") },
			}}
			root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(adapter))
			c := retirementEvidenceController(t, root)
			var wakes atomic.Int32
			root.SetNotifyFunc(func() { wakes.Add(1) })
			armSource := func() string {
				t.Helper()
				rec, err := root.jobManager.createShell(createShellOpts{Command: "retry-rearm-shell"})
				if err != nil {
					t.Fatal(err)
				}
				if err := root.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
					t.Fatal(err)
				}
				queued := root.drainJobNotifications()
				if len(queued) != 1 || queued[0].JobID != rec.JobID {
					t.Fatalf("source queue: %+v", queued)
				}
				root.requeueJobNotifications(queued)
				return rec.JobID
			}
			jobID := armSource()
			before, err := root.jobManager.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			wantState := jobstore.NotifyDelivered
			if settlement == "status-consume" {
				call := llm.ToolCallData{ID: "original-status-consume", Name: "job_status", Arguments: []byte(`{"target":` + mustJSONString(jobID) + `}`)}
				result := root.reg.ExecuteCall(context.Background(), root.env, call)
				if result.IsError {
					t.Fatal(result.Output)
				}
				if err := root.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
					t.Fatal(err)
				}
				wantState = jobstore.NotifyConsumed
			} else if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			after, err := root.jobManager.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got := after[jobID]; got == nil || got.TerminalGen != before[jobID].TerminalGen || got.NotifyState != wantState || root.peekNotifications() != 0 {
				t.Fatalf("original receipt not settled: %+v", got)
			}
			if settlement == "status-consume" {
				// Consumption does not reset the retry. Its original generation
				// remains scheduled, so a preparing claim must not hide it.
				assertRetirementEvidenceBlocked(t, c, "notification")
				clk.Advance(jobNotificationRetryInitialDelay)
				clk.Drain()
			}
			claim, state, err := c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("empty settled retry claim: %+v %v", state, err)
			}
			// A notification turn invalidates its retry generation. Fire that
			// stale callback behind real admission, then Abort and arm new work.
			clk.Advance(jobNotificationRetryInitialDelay)
			clk.Drain()
			root.pendingJobNotifsMu.Lock()
			active := root.jobNotifyRetry.active
			root.pendingJobNotifsMu.Unlock()
			if err := c.Abort(claim, ""); err != nil {
				t.Fatal(err)
			}
			if active {
				t.Fatal("refused stale callback left retry active")
			}
			secondID := armSource()
			beforeWake := wakes.Load()
			clk.Advance(jobNotificationRetryInitialDelay)
			clk.Drain()
			if wakes.Load() != beforeWake+1 {
				t.Fatalf("retry did not rearm after Abort: before=%d after=%d", beforeWake, wakes.Load())
			}
			if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if !requestsContain(adapter.Requests(), secondID) {
				t.Fatal("rearmed original source did not reach provider")
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

// Two real retry callbacks overlap: the first begins before controller
// attachment, the second after. Each original source settles through an actual
// notification turn while its callback remains paused; the later callback
// settles first, and eligibility requires the last callback's return.
func TestRetirementAutonomousNotificationRetryOverlap(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("first overlapping source settled") },
		func(llm.Request) llm.Response { return finalResponse("second overlapping source settled") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(adapter))
	entered := []chan struct{}{make(chan struct{}), make(chan struct{})}
	resume := []chan struct{}{make(chan struct{}), make(chan struct{})}
	defer func() {
		for _, ch := range resume {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		clk.Drain()
		root.SetNotifyFunc(nil)
	}()
	var gate atomic.Int32
	root.SetNotifyFunc(func() {
		if selected := gate.Swap(0); selected != 0 {
			close(entered[selected-1])
			<-resume[selected-1]
		}
	})
	armSource := func() (string, string) {
		t.Helper()
		rec, err := root.jobManager.createShell(createShellOpts{Command: "original-overlapping-retry-shell"})
		if err != nil {
			t.Fatal(err)
		}
		if err := root.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
			t.Fatal(err)
		}
		stored, err := root.jobManager.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if stored[rec.JobID] == nil || stored[rec.JobID].NotifyState != jobstore.NotifyPending {
			t.Fatalf("overlapping source not armed: %+v", stored[rec.JobID])
		}
		queued := root.drainJobNotifications()
		if len(queued) != 1 || queued[0].JobID != rec.JobID {
			t.Fatalf("overlapping source queue: %+v", queued)
		}
		root.requeueJobNotifications(queued)
		return rec.JobID, stored[rec.JobID].TerminalGen
	}
	var c *RetirementController
	for i := range 2 {
		jobID, terminalGen := armSource()
		gate.Store(int32(i + 1))
		clk.BlockUntil(1)
		clk.Advance(jobNotificationRetryInitialDelay)
		retirementAwait(t, entered[i])
		if c == nil {
			// The first callback began without a controller; attachment must
			// still observe its outstanding registration.
			c = retirementEvidenceController(t, root)
		}
		assertRetirementEvidenceBlocked(t, c, "notification")
		beforeRequests := len(adapter.Requests())
		if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		settled, err := root.jobManager.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := settled[jobID]; got == nil || got.TerminalGen != terminalGen || got.NotifyState != jobstore.NotifyDelivered || root.peekNotifications() != 0 {
			t.Fatalf("original overlapping receipt not settled: %+v", got)
		}
		if !requestsContain(adapter.Requests()[beforeRequests:], jobID) {
			t.Fatal("original overlapping source did not reach provider")
		}
		// The paused callback outlives its own source's settlement.
		assertRetirementEvidenceBlocked(t, c, "notification")
	}
	// Settle the later (post-attach) callback first.
	close(resume[1])
	// TRIPWIRE: hang guard only; the resumed callback is already unblocked,
	// so settlement completes in microseconds absent a deadlock.
	waitForCondition(t, 5*time.Second, "later notification callback settlement", func() bool {
		root.pendingJobNotifsMu.Lock()
		defer root.pendingJobNotifsMu.Unlock()
		return root.jobNotifyCallbacks == 1
	})
	assertRetirementEvidenceBlocked(t, c, "notification")
	close(resume[0])
	clk.Drain()
	root.pendingJobNotifsMu.Lock()
	remaining := root.jobNotifyCallbacks
	root.pendingJobNotifsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("settled callbacks retained %d registrations", remaining)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementAutonomousNamingOverlap(t *testing.T) {
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
	firstIn, secondIn := make(chan struct{}), make(chan struct{})
	firstResume, secondResume := make(chan struct{}), make(chan struct{})
	defer func() {
		for _, ch := range []chan struct{}{firstResume, secondResume} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	}()
	var calls atomic.Int32
	namer := llm.NewClient()
	namer.Register(&agenttest.ScriptedAdapter{Provider: root.currentProfile().CheapProvider(), Responder: func(llm.Request) llm.Response {
		if calls.Add(1) == 1 {
			close(firstIn)
			<-firstResume
			return llm.Response{Message: llm.Assistant(`{"name":"First Prompt Result"}`)}
		}
		close(secondIn)
		<-secondResume
		return llm.Response{Message: llm.Assistant(`{"name":"Second Compaction Result"}`)}
	}})
	updateSessionTestConfig(root, func(cfg *testConfig) { cfg.namerClient = namer })
	root.launchInitialPromptNamer(context.Background(), "first original prompt")
	<-firstIn
	c := retirementEvidenceController(t, root)
	root.launchCompactionNamer(context.Background(), schema.Turn{Kind: schema.TurnSummary, Message: llm.Assistant("second original summary")})
	<-secondIn
	close(secondResume) // later-started attempt settles before original attempt
	// Observe the real owner transition, never fabricate its count or use a
	// wall-clock delay as evidence that the later attempt finished.
	// TRIPWIRE: hang guard only; the resumed attempt is already unblocked,
	// so settlement completes in microseconds absent a deadlock.
	waitForCondition(t, 5*time.Second, "second naming attempt settlement", func() bool {
		root.mu.Lock()
		defer root.mu.Unlock()
		return root.naming.pending == 1
	})
	before := root.Meta()
	claim, state, err := c.TryClaim(true)
	close(firstResume)
	root.sendersWG.Wait()
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err != nil || claim != nil {
		t.Fatalf("overlapping naming escaped after partial settlement: %+v %v", state, err)
	}
	if before.Name != "Second Compaction Result" || root.Meta().Name != before.Name || root.Meta().NameSource != sessionNameSourceCompaction {
		t.Fatal("late prompt clobbered original settled compaction result")
	}
	root.mu.Lock()
	sticky := root.naming.promptPending
	root.mu.Unlock()
	if !sticky {
		t.Fatal("successful prompt attempt lost original sticky promptPending semantics")
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementSafetyUnreadableJobEvidence(t *testing.T) {
	for _, fault := range []string{"malformed", "incomplete-tail", "missing", "directory"} {
		t.Run(fault, func(t *testing.T) {
			root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
			c := retirementEvidenceController(t, root)
			jm := root.jobManager
			rec, err := jm.createShell(createShellOpts{Command: "original-terminal-source"})
			if err != nil {
				t.Fatal(err)
			}
			if err := jm.finalizeWithRunNoNotification(rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) {
				return jobstore.StatusCompleted, "", nil, nil
			}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(jm.dir, "jobs.jsonl")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var damaged []byte
			if fault == "missing" || fault == "directory" {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if fault == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				damaged = bytes.Clone(original)
				if fault == "malformed" {
					damaged = append(damaged, []byte("not-json\n")...)
				} else {
					damaged = append(damaged, []byte(`{"kind":`)...)
				}
				if err := os.WriteFile(path, damaged, 0600); err != nil {
					t.Fatal(err)
				}
			}
			claim, state, evidenceErr := c.TryClaim(true)
			var inspectionErr error
			var preserved bool
			if fault == "missing" || fault == "directory" {
				copied, err := os.ReadFile(path + ".original")
				inspectionErr = err
				preserved = bytes.Equal(copied, original)
				if fault == "directory" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("evidence read recreated missing journal: %v", err)
				}
				if err := os.Rename(path+".original", path); err != nil {
					t.Fatal(err)
				}
			} else {
				got, err := os.ReadFile(path)
				inspectionErr = err
				preserved = bytes.Equal(got, damaged)
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if claim != nil {
				if err := c.Abort(claim, ""); err != nil {
					t.Fatal(err)
				}
			}
			if inspectionErr != nil || !preserved {
				t.Fatalf("evidence read repaired or changed original bytes: %v", inspectionErr)
			}
			if claim != nil || evidenceErr == nil {
				t.Fatalf("unknown required job evidence allowed claim: %+v %v", state, evidenceErr)
			}
			assertRetirementEvidenceEligible(t, c)
		})
	}
}

func TestRetirementSafetyDurableShellNotification(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("durable receipt acknowledged") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}), withAdapter(adapter))
	c := retirementEvidenceController(t, root)
	jm := root.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "original-durable-shell"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
		t.Fatal(err)
	}
	before, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := before[rec.JobID]; got == nil || got.NotifyState != jobstore.NotifyPending || got.TerminalGen == "" {
		t.Fatalf("original receipt missing: %+v", got)
	}
	// The real source owner hands out a queue batch. That is not a durable
	// acknowledgement: the original terminal receipt still owes delivery.
	batch := root.drainJobNotifications()
	if len(batch) != 1 || batch[0].JobID != rec.JobID {
		t.Fatalf("source batch missing: %+v", batch)
	}
	claim, state, claimErr := c.TryClaim(true)
	after, err := jm.store.Load()
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	root.requeueJobNotifications(batch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("claim changed original terminal receipt")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	settled, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := settled[rec.JobID]; got == nil || got.TerminalGen != before[rec.JobID].TerminalGen || got.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("original receipt did not settle: %+v", got)
	}
	if !requestsContain(adapter.Requests(), rec.JobID) {
		t.Fatal("original job never reached provider")
	}
	if claimErr != nil || claim != nil {
		t.Fatalf("unacknowledged original durable receipt escaped: %+v %v", state, claimErr)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementSafetyColdGoal(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	if err := root.Rename("cold-goal-root"); err != nil {
		t.Fatal(err)
	}
	d := retirementIdleDelegate(t, root)
	child := tree.residentDelegateRuntime(d.DelegateID)
	if child == nil {
		t.Fatal("original idle runtime missing")
	}
	if err := child.Rename("cold-goal-child"); err != nil {
		t.Fatal(err)
	}
	firstClaim, firstState, err := c.TryClaim(true)
	if err != nil || firstClaim == nil {
		t.Fatalf("settled original delegate not eligible: %+v %v", firstState, err)
	}
	_, refused := child.SetGoal(t.Context(), "original-cold-goal-objective")
	if err := c.Abort(firstClaim, ""); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(refused, ErrRetirementUnavailable) || child.Meta().Goal != nil {
		t.Fatalf("claim-first mutated original goal: %v %+v", refused, child.Meta().Goal)
	}
	if _, err := child.SetGoal(t.Context(), "original-cold-goal-objective"); err != nil {
		t.Fatal(err)
	}
	if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
		t.Fatal(err)
	}
	if tree.residentDelegateRuntime(d.DelegateID) != nil || root.subagents.get(d.ChildSessionID) != nil {
		t.Fatal("real reclamation did not make delegate cold")
	}
	original, err := schema.LoadSessionMeta(root.stateDir, d.ChildSessionID)
	if err != nil || original.Goal == nil || original.Goal.Objective != "original-cold-goal-objective" || original.Goal.Status != "active" {
		t.Fatalf("original goal was not durably retained by actual reclamation: %+v %v", original.Goal, err)
	}
	claim, state, evidenceErr := c.TryClaim(true)
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	after, err := schema.LoadSessionMeta(root.stateDir, d.ChildSessionID)
	if err != nil || !reflect.DeepEqual(after, original) || tree.residentDelegateRuntime(d.DelegateID) != nil {
		t.Fatalf("claim changed original cold source or materialized runtime: %v", err)
	}
	if claim != nil || evidenceErr != nil || !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "autonomous" && b.SessionID == d.ChildSessionID
	}) {
		t.Fatalf("original cold active goal escaped: %+v %v", state, evidenceErr)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-resume
			return toolCallResponse(llm.ToolCallData{ID: "complete-original-cold-goal", Name: "update_goal", Type: "function", Arguments: []byte(`{"status":"complete","intent":"completing original objective"}`)})
		},
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("original-cold-goal-result") },
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("original-cold-goal-delivered") },
	}}
	root.client.Register(adapter)
	outcome := (delegateRuntime{owner: root}).send(t.Context(), d.DelegateID, "complete-original-cold-goal", 0)
	if outcome.result.Err != nil {
		t.Fatal(outcome.result.Err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second): // TRIPWIRE: bounds a broken real restore/provider rendezvous.
		t.Fatal("restored delegate did not reach provider")
	}
	restored := tree.residentDelegateRuntime(d.DelegateID)
	if restored == nil || restored == child || !reflect.DeepEqual(restored.Meta().Goal, original.Goal) {
		t.Fatal("real restoration did not retain the original goal")
	}
	assertRetirementEvidenceBlocked(t, c, "autonomous")
	close(resume)
	retirementSettleDelegate(t, root, d)
	settled := restored.Meta().Goal
	if settled == nil || settled.Objective != original.Goal.Objective || settled.Status != "complete" {
		t.Fatalf("actual resumed tool did not settle original goal: %+v", settled)
	}
	if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
		t.Fatal(err)
	}
	terminal, err := schema.LoadSessionMeta(root.stateDir, d.ChildSessionID)
	if err != nil {
		t.Fatal(err)
	}
	// GoalSnapshot is a JSON contract: unmarshalling preserves the timestamp
	// instant but not time.Time's Local location pointer. Compare every field
	// through the actual schema encoding, not internal time representation.
	wantGoal, err := json.Marshal(settled)
	if err != nil {
		t.Fatal(err)
	}
	gotGoal, err := json.Marshal(terminal.Goal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotGoal, wantGoal) || tree.residentDelegateRuntime(d.DelegateID) != nil {
		t.Fatalf("original terminal goal not durably retained cold: persisted=%#v runtime=%#v resident=%p err=%v", terminal.Goal, settled, tree.residentDelegateRuntime(d.DelegateID), err)
	}
	assertRetirementEvidenceEligible(t, c)
}

func TestRetirementSafetyColdJobEvidence(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
		t.Fatal(err)
	}
	if tree.residentDelegateRuntime(d.DelegateID) != nil || root.subagents.get(d.ChildSessionID) != nil {
		t.Fatal("real reclamation did not make delegate cold")
	}
	assertRetirementEvidenceEligible(t, c)
	path := filepath.Join(jobsDir(root.stateDir, d.ChildSessionID), "jobs.jsonl")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	damaged := append(bytes.Clone(original), []byte(`{"kind":`)...)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	claim, state, evidenceErr := c.TryClaim(true)
	after, readErr := os.ReadFile(path)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	if readErr != nil || !bytes.Equal(after, damaged) {
		t.Fatalf("cold evidence repaired original journal: %v", readErr)
	}
	if claim != nil || evidenceErr == nil {
		t.Fatalf("unknown cold job evidence escaped: %+v %v", state, evidenceErr)
	}
	if tree.residentDelegateRuntime(d.DelegateID) != nil {
		t.Fatal("evidence check materialized cold runtime")
	}
	assertRetirementEvidenceEligible(t, c)
}

// TestRetirementEvidenceStableWatchSettlementPending drives the real stable
// watch drain to a superseded source-acknowledgement failure so the exact
// delivery receipt and settlement retry outlive every watch projection the
// collector already had: the live config, its pending sends, and the terminal
// flush are then cleared for real while the receipt and retry remain.
func TestRetirementEvidenceStableWatchSettlementPending(t *testing.T) {
	fs := newAttentionSyncBarrierFS()
	fixture := newStableWatchRuntimeFixture(t, fs)
	old, _, originalAppend := seedSupersededStableWatchAckFailure(t, fixture, fs)
	jm := fixture.sourceJM
	jm.mu.Lock()
	receiptHeld := jm.stableWatchReceipts[old.DeliveryID] != nil
	retry, retryHeld := jm.stableWatchSettlementRetries[old.DeliveryID]
	jm.mu.Unlock()
	if !receiptHeld || !retryHeld {
		t.Fatalf("superseded acknowledgement failure did not retain receipt and retry: receipt=%v retry=%v", receiptHeld, retryHeld)
	}
	if retry.state.DeliveryID != old.DeliveryID || retry.state.UpdateSeq != old.UpdateSeq {
		t.Fatalf("settlement retry lost original identity: %+v vs %+v", retry.state, old)
	}
	cfg := fixture.onlyWatchConfig(t)
	if _, err := jm.clearWatchByIDMatching(cfg.watchID, func(*watchConfig) bool { return true }, true); err != nil {
		t.Fatalf("clear watch: %v", err)
	}
	jm.mu.Lock()
	residual := len(jm.watches) != 0 || len(jm.terminalFlush) != 0 || len(jm.running) != 0
	stillRetrying := len(jm.stableWatchSettlementRetries) != 0
	stillReceipt := len(jm.stableWatchReceipts) != 0
	jm.mu.Unlock()
	if residual {
		t.Fatal("cleared watch left live, terminal, or running projections that mask settlement evidence")
	}
	if !stillRetrying || !stillReceipt {
		t.Fatalf("watch clear erased settlement state: retries=%v receipts=%v", stillRetrying, stillReceipt)
	}
	blockers, err := jm.retirementEvidence(fixture.source.ID())
	if err != nil {
		t.Fatalf("collect evidence over pending settlement: %v", err)
	}
	if !slices.ContainsFunc(blockers, func(b RetirementBlocker) bool { return b.Category == "watch" }) {
		t.Fatalf("claimed watch delivery and settlement retry escaped evidence: %+v", blockers)
	}
	// Real settlement: the original acknowledgement append succeeds now, the
	// exact retry and receipt are consumed, and evidence goes quiet.
	jm.appendEvent = originalAppend
	settled, err := jm.retryStableWatchSettlements()
	if err != nil || !settled {
		t.Fatalf("settle original retry: settled=%v err=%v", settled, err)
	}
	if jm.hasPendingStableWatchSettlementRetry() {
		t.Fatal("settled retry still pending")
	}
	jm.mu.Lock()
	receiptsLeft := len(jm.stableWatchReceipts)
	jm.mu.Unlock()
	if receiptsLeft != 0 {
		t.Fatalf("settlement left %d receipts", receiptsLeft)
	}
	blockers, err = jm.retirementEvidence(fixture.source.ID())
	if err != nil {
		t.Fatalf("collect evidence after settlement: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("settled watch delivery still blocks: %+v", blockers)
	}
	journal, err := jm.store.LoadEvents()
	if err != nil {
		t.Fatalf("load source journal: %v", err)
	}
	if got := countExactWatchSendEvents(journal, jobstore.EventWatchSendDelivered, old); got != 1 {
		t.Fatalf("original acknowledgement count = %d, want exactly 1", got)
	}
}

// TestRetirementSafetyNotificationPinsDelegateResident documents the cold
// notification boundary: a delegate child's shell completion is delivered
// through the durable stable-attention stream at finalize, and production
// reclamation refuses to make a child with pending attention cold, so the
// obligation stays on the resident evidence path.
func TestRetirementSafetyNotificationPinsDelegateResident(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	child := tree.residentDelegateRuntime(d.DelegateID)
	if child == nil || child.jobManager == nil {
		t.Fatal("original idle runtime or its job manager missing")
	}
	rec, err := child.jobManager.createShell(createShellOpts{Command: "original-pinned-shell"})
	if err != nil {
		t.Fatal(err)
	}
	if err := child.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "pin-fixture-settled", nil); err != nil {
		t.Fatal(err)
	}
	recs, err := child.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if recs[rec.JobID] == nil || recs[rec.JobID].NotifyState != jobstore.NotifyDelivered || recs[rec.JobID].TerminalGen == "" {
		t.Fatalf("original shell completion not stably delivered: %+v", recs[rec.JobID])
	}
	if err := root.reclaimDelegateRuntimeCapacity(tree.maxRetainedTerminal); err != nil {
		t.Fatal(err)
	}
	if tree.residentDelegateRuntime(d.DelegateID) == nil {
		t.Fatal("pending shell attention did not pin the delegate resident")
	}
	// A TryClaim whose evidence collection races the still-settling durable
	// attention stream legitimately returns errDelegateTargetBusy — the tree
	// version moved mid-collection (delegate_tree_retirement.go:193); that is
	// a retry signal, never an escape. The pin holds when every attempt
	// refuses and a settled attempt still names the original owner.
	var state RetirementSnapshot
	// TRIPWIRE: hang guard only; the durable attention stream settles in
	// milliseconds once finalize's delivery lands.
	waitForCondition(t, 5*time.Second, "pinned shell attention refusal settles", func() bool {
		claim, got, err := c.TryClaim(true)
		if claim != nil {
			if abortErr := c.Abort(claim, ""); abortErr != nil {
				t.Fatal(abortErr)
			}
			t.Fatalf("pinned shell attention escaped: %+v", got)
		}
		state = got
		return err == nil
	})
	// The obligation's owner at the settled instant is timing-dependent:
	// still-pending child attention blocks with the original DelegateID,
	// while attention already delivered blocks with the session actually
	// driving it (a turn on the original child, or root-owned work). All
	// name the original root/child pair; an empty or foreign-owned refusal
	// would mean the obligation was lost.
	if len(state.Blockers) == 0 {
		t.Fatalf("pinned shell attention escaped with empty blockers: %+v", state)
	}
	for _, b := range state.Blockers {
		if b.DelegateID != d.DelegateID && b.SessionID != root.id && b.SessionID != d.ChildSessionID {
			t.Fatalf("pinned shell attention lost its original owner: %+v", state)
		}
	}
}

// A resident delegate child whose job manager still runs a shell blocks
// retirement through the resident evidence walk, and the blocker names the
// original owner — the child's session — not the root. Mirrored cold side:
// TestRetirementSafetyColdJobWatchContent.
func TestRetirementSafetyDescendantShellResidentRunning(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	child := tree.residentDelegateRuntime(d.DelegateID)
	if child == nil || child.jobManager == nil {
		t.Fatal("original idle runtime or its job manager missing")
	}
	rec, err := child.jobManager.createShell(createShellOpts{Command: "original-resident-running-shell"})
	if err != nil {
		t.Fatal(err)
	}
	child.jobManager.mu.Lock()
	running := child.jobManager.running[rec.JobID] != nil
	child.jobManager.mu.Unlock()
	if !running {
		t.Fatal("original shell not resident-running")
	}
	claim, state, err := c.TryClaim(true)
	if claim != nil {
		if abortErr := c.Abort(claim, ""); abortErr != nil {
			t.Fatal(abortErr)
		}
	}
	if claim != nil || err != nil {
		t.Fatalf("resident running shell escaped: %+v %v", state, err)
	}
	if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "job" && b.SessionID == d.ChildSessionID
	}) {
		t.Fatalf("running shell evidence lost its original owner: %+v", state)
	}
}

// TestRetirementSafetyColdJobWatchContent seeds a real active repeating timer
// and a real running shell on a resident delegate child, then crashes: runtime
// handles are abandoned and every store closes without teardown appends, so
// the durable journal keeps exactly what a dead daemon leaves — an active
// watch and a nonterminal job. (Clean reclamation can never produce this
// state: its teardown durably clears watches, and pending attention pins the
// delegate resident.) A real daemon restart — the same root session resumed
// from its durable meta over the same state dir — enumerates the cold member
// and must block retirement on both obligations, each through its real owner:
// boot reconciliation adopts the crashed shell into durable transcript
// attention, while the active watch stays in the cold journal and has no
// other evidence owner than the cold fold.
func TestRetirementSafetyColdJobWatchContent(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	d := retirementIdleDelegate(t, root1)
	tree1 := root1.delegateController
	child := tree1.residentDelegateRuntime(d.DelegateID)
	if child == nil || child.jobManager == nil {
		t.Fatal("original idle runtime or its job manager missing")
	}
	registered, err := child.jobManager.configureWatch(watchArgs{Target: "caller", Source: "self", RepeatSeconds: 3600, Note: "original-cold-watch"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := child.jobManager.createShell(createShellOpts{Command: "sleep 60", Description: "original-cold-shell"})
	if err != nil {
		t.Fatal(err)
	}
	watches, err := child.jobManager.store.LoadWatches()
	if err != nil {
		t.Fatal(err)
	}
	if watches[registered.WatchID] == nil || !watches[registered.WatchID].Active {
		t.Fatalf("original watch not durably active: %+v", watches[registered.WatchID])
	}
	meta := root1.Meta()
	journalPath := filepath.Join(jobsDir(dir, d.ChildSessionID), "jobs.jsonl")

	// Crash: abandon runtime handles and close stores with no teardown
	// appends. root1 is intentionally never Close()d — its child teardown
	// would durably clear the watch and erase the state under test.
	child.jobManager.abandonRunningJobs()
	if err := child.jobManager.closeStoreOnly(); err != nil {
		t.Fatal(err)
	}
	if err := root1.jobManager.closeStoreOnly(); err != nil {
		t.Fatal(err)
	}
	if err := child.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	if err := root1.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	// The crash fixture must genuinely leave the shell nonterminal on disk.
	crashEvents, _, err := jobstore.ScanEventsFrom(context.Background(), journalPath, 0, jobstore.ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if crashed := jobstore.Fold(crashEvents)[rec.JobID]; crashed == nil || crashed.Status.IsTerminal() {
		t.Fatalf("crash fixture lost the original running shell: %+v", crashed)
	}

	// Daemon restart: resume the same root session from its durable meta over
	// the same state dir. The restarted delegate controller folds its journal
	// with an empty live map, so the member is enumerated cold for real.
	client2 := llm.NewClient()
	client2.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client2, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer root2.Close()
	rc, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.AttachRoot(root2); err != nil {
		t.Fatal(err)
	}
	tree2 := root2.delegateController
	if tree2 == nil {
		t.Fatal("restored root missing delegate controller")
	}
	// Boot reconciliation adopts the crashed shell durably: a terminal record
	// with a delivered notification, and exactly one pending stable attention
	// entry naming the original job and its terminal generation.
	postEvents, _, err := jobstore.ScanEventsFrom(context.Background(), journalPath, 0, jobstore.ScanLimits{})
	if err != nil {
		t.Fatal(err)
	}
	repaired := jobstore.Fold(postEvents)[rec.JobID]
	if repaired == nil || !repaired.Status.IsTerminal() || repaired.NotifyState != jobstore.NotifyDelivered || repaired.TerminalGen == "" {
		t.Fatalf("restart did not durably reconcile original crashed shell: %+v", repaired)
	}
	tree2.mu.Lock()
	desc := tree2.durable[d.DelegateID].Descriptor
	tree2.mu.Unlock()
	transcriptPath, transcriptSessionID, err := delegateTranscriptPathFromRef(dir, desc.TranscriptRef)
	if err != nil || transcriptSessionID != d.ChildSessionID {
		t.Fatalf("original transcript ref: %q %v", transcriptSessionID, err)
	}
	attn, err := readExistingDelegateAttentionFold(transcriptPath, d.ChildSessionID)
	if err != nil {
		t.Fatal(err)
	}
	pending := attn.pendingIDs()
	wantAttention := stableShellAttentionID(rec.JobID, repaired.TerminalGen)
	if len(pending) != 1 || pending[0] != wantAttention {
		t.Fatalf("restart attention fold = %v, want exactly %q", pending, wantAttention)
	}
	journalBaseline, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	transcriptBaseline, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, state, evidenceErr := rc.TryClaim(true)
	if claim != nil {
		if err := rc.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
	}
	// The active watch is projected from the cold journal itself; the crashed
	// shell's obligation is the durable attention recorded by reconciliation.
	if claim != nil || evidenceErr != nil ||
		!slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
			return b.Category == "watch" && b.SessionID == d.ChildSessionID && b.DelegateID == d.DelegateID
		}) ||
		!slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
			return b.Category == "delegate" && b.DelegateID == d.DelegateID
		}) {
		t.Fatalf("original cold crash content escaped: %+v %v", state, evidenceErr)
	}
	if after, readErr := os.ReadFile(journalPath); readErr != nil || !bytes.Equal(after, journalBaseline) {
		t.Fatalf("evidence changed original cold journal: %v", readErr)
	}
	if after, readErr := os.ReadFile(transcriptPath); readErr != nil || !bytes.Equal(after, transcriptBaseline) {
		t.Fatalf("evidence changed original cold transcript: %v", readErr)
	}
	if tree2.residentDelegateRuntime(d.DelegateID) != nil {
		t.Fatal("evidence check materialized cold runtime")
	}

	// Settle for real: materialize the original delegate through the restored
	// root, let restore reconcile the crashed shell, and clear the original
	// watch through the resident API.
	client2.Register(&retirementDelegateAdapter{fakeAdapter: fakeAdapter{name: "openai"}})
	outcome := (delegateRuntime{owner: root2}).send(t.Context(), d.DelegateID, "settle-original-cold-content", 0)
	if outcome.result.Err != nil {
		t.Fatal(outcome.result.Err)
	}
	restored := tree2.residentDelegateRuntime(d.DelegateID)
	if restored == nil || restored.jobManager == nil {
		t.Fatal("real restoration did not occur")
	}
	recs, err := restored.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := recs[rec.JobID]; got == nil || got.Status != repaired.Status || got.TerminalGen != repaired.TerminalGen {
		t.Fatalf("restored original shell lost its reconciled identity: got %+v want %+v", got, repaired)
	}
	if _, err := restored.jobManager.clearWatchByID(registered.WatchID); err != nil {
		t.Fatal(err)
	}
	settled, err := restored.jobManager.store.LoadWatches()
	if err != nil {
		t.Fatal(err)
	}
	if got := settled[registered.WatchID]; got == nil || got.Active {
		t.Fatalf("restored original watch not cleared: %+v", got)
	}
	retirementSettleDelegate(t, root2, d)
	if _, err := root2.ProcessInput(context.Background(), "drain-original-crash-attention", nil); err != nil {
		t.Fatal(err)
	}
	settledAttn, err := readExistingDelegateAttentionFold(transcriptPath, d.ChildSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if pending := settledAttn.pendingIDs(); len(pending) != 0 {
		t.Fatalf("settled delegate still owes original shell attention: %v", pending)
	}
	if err := root2.reclaimDelegateRuntimeCapacity(tree2.maxRetainedTerminal); err != nil {
		t.Fatal(err)
	}
	if tree2.residentDelegateRuntime(d.DelegateID) != nil {
		t.Fatal("settled original did not go cold again")
	}
	assertRetirementEvidenceEligible(t, rc)
}

// The manage_worktree dispatch holds one environment admission for the whole
// call (session_tools_worktree.go: beginOp around every operation), so a held
// claim refuses a create before any git runs, an in-flight create blocks the
// claim, and the rollback a failed enter owes keeps its own admission — named
// for the rollback — until the lane is gone. Drives the registered tool
// surface over the scripted git boundary; lane creation and rollback effects
// are asserted on the real registry/sidecar state.
func TestRetirementEnvironmentWorktreeCreate(t *testing.T) {
	t.Run("claim-first", func(t *testing.T) {
		sr := newScriptedLaneRepo(t)
		r := sr.wt()
		c := retirementEvidenceController(t, r.s)
		assertRetirementEvidenceEligible(t, c)
		claim, state, err := c.TryClaim(true)
		if err != nil || claim == nil {
			t.Fatalf("claim: %+v, %v", state, err)
		}
		if _, err := r.create(t, map[string]any{"name": "lane"}); !errors.Is(err, errWorktreeOpWhileClosing) {
			t.Fatalf("create under claim = %v, want refusal", err)
		}
		if calls := sr.gitCalls(); len(calls) != 0 {
			t.Fatalf("refused create performed git: %v", calls)
		}
		if labels := r.s.outstandingEnvWork(); len(labels) != 0 {
			t.Fatalf("refused create leaked admissions: %v", labels)
		}
		if err := c.Abort(claim, ""); err != nil {
			t.Fatal(err)
		}
		path, _ := createLaneExpectations(t, r, "lane")
		if _, err := r.create(t, map[string]any{"name": "lane"}); err != nil {
			t.Fatalf("post-release create: %v", err)
		}
		if !sr.lanePresent(path) {
			t.Fatal("post-release create did not register the lane")
		}
		assertRetirementEvidenceEligible(t, c)
	})

	t.Run("work-first", func(t *testing.T) {
		sr := newScriptedLaneRepo(t)
		r := sr.wt()
		c := retirementEvidenceController(t, r.s)
		entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		var once sync.Once
		sr.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
			once.Do(func() { close(entered); <-resume })
			return next(args...)
		})
		go func() {
			rt := r.s.reg.Get("manage_worktree")
			_, err := rt.Exec(context.Background(), r.s.currentEnv(), map[string]any{"operation": "create", "name": "lane"})
			done <- err
		}()
		defer func() {
			select {
			case <-resume:
			default:
				close(resume)
			}
			// The main path consumes done on its way through; a tripwire exit
			// leaves the buffered result unread. Never block here either way.
			select {
			case <-done:
			default:
			}
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second): // TRIPWIRE: the scripted boundary rendezvous normally takes milliseconds; this only bounds a deadlock.
			t.Fatal("create did not reach the git boundary")
		}
		assertRetirementEvidenceBlocked(t, c, "environment")
		close(resume)
		if err := <-done; err != nil {
			t.Fatalf("in-flight create: %v", err)
		}
		path, _ := createLaneExpectations(t, r, "lane")
		if !sr.lanePresent(path) {
			t.Fatal("resumed create did not register the lane")
		}
		assertRetirementEvidenceEligible(t, c)
	})

	t.Run("rollback", func(t *testing.T) {
		sr := newScriptedLaneRepo(t)
		r := sr.wt()
		path, sidecar := createLaneExpectations(t, r, "lane")
		boom := errors.New("injected enter failure")
		installWorktreeSeams(t, r.s, worktreeTestSeams{enterWorktree: func(string, bool) error { return boom }})
		c := retirementEvidenceController(t, r.s)
		entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		var holding atomic.Bool
		sr.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
			// The rollback's first command is the lane unlock; hold there so the
			// claim observes the rollback's own admission, not the create's.
			if len(args) == 3 && args[0] == "worktree" && args[1] == "unlock" && args[2] == path && holding.CompareAndSwap(false, true) {
				close(entered)
				<-resume
			}
			return next(args...)
		})
		go func() {
			rt := r.s.reg.Get("manage_worktree")
			_, err := rt.Exec(context.Background(), r.s.currentEnv(), map[string]any{"operation": "create", "name": "lane"})
			done <- err
		}()
		defer func() {
			select {
			case <-resume:
			default:
				close(resume)
			}
			// The main path consumes done on its way through; a tripwire exit
			// leaves the buffered result unread. Never block here either way.
			select {
			case <-done:
			default:
			}
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second): // TRIPWIRE: the scripted boundary rendezvous normally takes milliseconds; this only bounds a deadlock.
			t.Fatal("rollback never ran its lane unlock; the test observed nothing")
		}
		assertRetirementEvidenceBlocked(t, c, "environment")
		if labels := r.s.outstandingEnvWork(); !slices.Contains(labels, "create rollback for "+path) {
			t.Fatalf("rollback admission not named for the rollback: %v", labels)
		}
		close(resume)
		if err := <-done; !errors.Is(err, boom) {
			t.Fatalf("create error = %v, want the injected %v", err, boom)
		}
		assertLaneRolledBack(t, sr, r, "lane", path, sidecar)
		assertRetirementEvidenceEligible(t, c)
	})
}
func TestRetirementWatchFiredRepeating(t *testing.T) {
	clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("repeat acknowledged") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(adapter))
	c := retirementEvidenceController(t, root)
	registered, err := root.jobManager.configureWatch(watchArgs{Target: "caller", Source: "self", RepeatSeconds: 60, Note: "repeating-timer-note"})
	if err != nil {
		t.Fatal(err)
	}
	jm := root.jobManager
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(registered.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatal("timer not registered")
	}

	// First tick: the fire delivers a non-terminal notification and keeps the
	// timer armed; both the live registration and the pending frame block.
	if keep := jm.fireProgressTick(key, cfg); !keep {
		t.Fatal("repeating tick stopped the timer")
	}
	root.pendingJobNotifsMu.Lock()
	pending := slices.Clone(root.pendingJobNotifs)
	root.pendingJobNotifsMu.Unlock()
	if len(pending) != 1 || pending[0].WatchID != registered.WatchID || pending[0].Terminal ||
		pending[0].Note != "repeating-timer-note" || pending[0].Fires != 1 || pending[0].IntervalSeconds != 60 {
		t.Fatalf("repeating tick notification wrong: %+v", pending)
	}
	assertRetirementEvidenceBlocked(t, c, "watch")
	assertRetirementEvidenceBlocked(t, c, "notification")

	// Settling the frame clears only the notification blocker; the live
	// registration keeps retirement blocked.
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if !requestsContain(adapter.Requests(), registered.WatchID, "repeating-timer-note") {
		t.Fatal("repeating frame did not reach provider")
	}
	if root.peekNotifications() != 0 {
		t.Fatal("real notification turn did not settle repeating tick")
	}
	assertRetirementEvidenceBlocked(t, c, "watch")

	// A second tick after settlement proves the timer survived retirement
	// pressure: it delivers again and stays armed.
	if keep := jm.fireProgressTick(key, cfg); !keep {
		t.Fatal("second repeating tick stopped the timer")
	}
	root.pendingJobNotifsMu.Lock()
	second := slices.Clone(root.pendingJobNotifs)
	root.pendingJobNotifsMu.Unlock()
	if len(second) != 1 || second[0].WatchID != registered.WatchID || second[0].Terminal || second[0].Note != "repeating-timer-note" {
		t.Fatalf("second tick notification wrong: %+v", second)
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if root.peekNotifications() != 0 {
		t.Fatal("second frame did not settle")
	}
	// Clearing through admission removes the last blocker.
	if _, err := jm.clearWatchByID(registered.WatchID); err != nil {
		t.Fatal(err)
	}
	assertRetirementEvidenceEligible(t, c)
}
