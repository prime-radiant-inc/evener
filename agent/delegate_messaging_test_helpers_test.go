package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// messagingAdapter scripts a root session and the delegates under it, each
// from its own step list, so sessions running at the same time never race
// for one script. The root is recognized by its session id. A delegate is
// recognized by the first marker one of its user messages carries (its brief
// names it) and keeps that script for the rest of its life. A step may block
// on a channel the test controls: steps run outside the adapter's lock. An
// unscripted request is answered with a plain final report.
type messagingAdapter struct {
	mu           sync.Mutex
	rootID       string
	markers      []string
	scripts      map[string][]func(llm.Request) llm.Response
	sessions     map[string]string
	requests     map[string][]llm.Request
	arrived      chan struct{}
	finite       map[string][]func(context.Context, llm.Request) (llm.Response, error)
	answered     map[string][]llm.Response
	onUnexpected func(string)
}

func newMessagingAdapter(markers ...string) *messagingAdapter {
	return &messagingAdapter{
		markers:  markers,
		scripts:  make(map[string][]func(llm.Request) llm.Response),
		sessions: make(map[string]string),
		requests: make(map[string][]llm.Request),
		arrived:  make(chan struct{}, 1),
	}
}

func (*messagingAdapter) Name() string { return "openai" }

func (*messagingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// script appends steps to a script: "root", or one of the markers.
func (a *messagingAdapter) script(key string, steps ...func(llm.Request) llm.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scripts[key] = append(a.scripts[key], steps...)
}

func (a *messagingAdapter) requestsFor(key string) []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.requests[key])
}

// waitForRequests waits until key's script has received at least n requests.
func (a *messagingAdapter) waitForRequests(t *testing.T, key string, n int) []llm.Request {
	t.Helper()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	deadline := time.After(30 * time.Second)
	for {
		if requests := a.requestsFor(key); len(requests) >= n {
			return requests
		}
		select {
		case <-a.arrived:
		case <-deadline:
			t.Fatalf("%s made %d requests, want %d", key, len(a.requestsFor(key)), n)
		}
	}
}

func (a *messagingAdapter) keyLocked(req llm.Request) string {
	if req.SessionID == a.rootID {
		return "root"
	}
	if key, ok := a.sessions[req.SessionID]; ok {
		return key
	}
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, marker := range a.markers {
			if strings.Contains(msg.Text(), marker) {
				a.sessions[req.SessionID] = marker
				return marker
			}
		}
	}
	return ""
}

// finiteScript opts this adapter into bounded scripts. Exhaustion cancels the
// provider call instead of manufacturing another successful done response.
func (a *messagingAdapter) finiteScript(key string, steps ...func(context.Context, llm.Request) (llm.Response, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finite == nil {
		a.finite = make(map[string][]func(context.Context, llm.Request) (llm.Response, error))
		a.answered = make(map[string][]llm.Response)
	}
	a.finite[key] = append(a.finite[key], steps...)
}

func (a *messagingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	key := a.keyLocked(req)
	a.requests[key] = append(a.requests[key], req)
	n := len(a.requests[key])
	finite := a.finite != nil
	var finiteStep func(context.Context, llm.Request) (llm.Response, error)
	if steps := a.finite[key]; len(steps) > 0 {
		finiteStep, a.finite[key] = steps[0], steps[1:]
	}
	var step func(llm.Request) llm.Response
	if steps := a.scripts[key]; len(steps) > 0 {
		step, a.scripts[key] = steps[0], steps[1:]
	}
	a.mu.Unlock()
	select {
	case a.arrived <- struct{}{}:
	default:
	}
	if finite {
		if finiteStep == nil {
			if a.onUnexpected != nil {
				a.onUnexpected(fmt.Sprintf("unexpected provider request session=%s script=%s request=%d", req.SessionID, key, n))
			}
			return llm.Response{}, context.Canceled
		}
		response, err := finiteStep(ctx, req)
		if err != nil {
			return llm.Response{}, err
		}
		response.ID = fmt.Sprintf("fixture-%s-%s-%d", req.SessionID, key, n)
		response.Provider, response.Model = a.Name(), req.Model
		a.mu.Lock()
		a.answered[key] = append(a.answered[key], response)
		a.mu.Unlock()
		return response, nil
	}
	response := finalResponse("done")
	if step != nil {
		response = step(req)
	}
	response.Provider = a.Name()
	response.Model = req.Model
	return response, nil
}

// newMessagingRoot starts a root session answered by adapter. configure, when
// set, adjusts the root's config first, for test-only hooks.
func newMessagingRoot(t *testing.T, adapter *messagingAdapter, configure func(*SessionConfig)) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	cfg := SessionConfig{StateDir: t.TempDir()}
	if configure != nil {
		configure(&cfg)
	}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	adapter.mu.Lock()
	adapter.rootID = sess.ID()
	adapter.mu.Unlock()
	return sess
}

// delegateToolResponse is a model turn that starts one delegate with prompt.
func delegateToolResponse(id, prompt string) llm.Response {
	return delegateToolCall(id, map[string]any{"prompt": prompt})
}

// delegateToolResponseWithResultSchema starts one delegate whose final
// report must match resultSchema.
func delegateToolResponseWithResultSchema(id, prompt string, resultSchema map[string]any) llm.Response {
	return delegateToolCall(id, map[string]any{"prompt": prompt, "result_schema": resultSchema})
}

func delegateToolCall(id string, arguments map[string]any) llm.Response {
	args, _ := json.Marshal(arguments)
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}

// writeToolDescriptions renders each tool's description and its parameters'
// descriptions, parameters sorted by name, for a prompt golden.
func writeToolDescriptions(golden *strings.Builder, defs ...llm.ToolDefinition) {
	for _, def := range defs {
		fmt.Fprintf(golden, "\n## %s\n\n%s\n\n", def.Name, def.Description)
		props, _ := def.Parameters["properties"].(map[string]any)
		for _, param := range slices.Sorted(maps.Keys(props)) {
			description, _ := props[param].(map[string]any)["description"].(string)
			fmt.Fprintf(golden, "- `%s`: %s\n", param, description)
		}
	}
}

// rootPendingAttention lists root's unresolved attention in transcript order,
// read from its transcript the way restore reads it, or nil when the read
// fails.
func rootPendingAttention(root *Session) []string {
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		return nil
	}
	return fold.pendingIDs()
}

// rootAttentionFold is root's attention fold, read from its transcript.
func rootAttentionFold(t *testing.T, root *Session) delegateAttentionFold {
	t.Helper()
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("fold root attention: %v", err)
	}
	return fold
}

// updateResponse is a delegate's model turn that sends message to its parent
// as an update: communicate(end_turn=false).
func updateResponse(id, message string) llm.Response {
	return toolCallResponse(communicateCallArgs(id, map[string]any{"message": message, "end_turn": false}))
}

// delegateSendResponse is a model turn that sends message to delegateID and
// waits up to maxWaitMS for the generation it starts.
func delegateSendResponse(id, delegateID, message string, maxWaitMS int) llm.Response {
	args, _ := json.Marshal(map[string]any{"to": delegateID, "message": message, "max_wait_ms": maxWaitMS})
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "delegate_send", Arguments: args, Type: "function"})
}

// jobListResponse is a model turn that calls job_list: a tool round that
// changes nothing, so the request after it is the turn's next model boundary.
func jobListResponse(id string) llm.Response {
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "job_list", Arguments: json.RawMessage(`{}`), Type: "function"})
}

// waitForSoleChildIdle waits until root has exactly one child session and it
// is idle, and returns it.
func waitForSoleChildIdle(t *testing.T, root *Session) *Session {
	t.Helper()
	var child *Session
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the child to go idle", func() bool {
		children := root.subagents.sessions()
		if len(children) != 1 || children[0].State() != SessionIdle {
			return false
		}
		child = children[0]
		return true
	})
	return child
}

// awaitSignal waits for ch to close. Scripted steps call it on provider
// goroutines, so a hang is reported with Errorf, never Fatalf; waitForTestSignal
// is the Fatalf form for the test goroutine.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	select {
	case <-ch:
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Errorf("timed out waiting for %s", what)
	}
}

// Each factory invocation gets its own token, even when a resume keeps the
// lease and generation done. Only the test goroutine matches registrations.
type activationReturn struct {
	sessionID, delegateID string
	lease                 delegateLease
	generationDone        <-chan struct{}
	returned              chan struct{}
}

type activationReturns struct {
	registered chan *activationReturn
	pending    []*activationReturn
	all        []*activationReturn
}

func activationStillOpen(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("held activation completed")
	default:
	}
}

func newActivationReturns() *activationReturns {
	// The bounded scripts below create fewer than 128 activations, including
	// cancellation cleanup. Overflow is an invariant failure, never a lost join.
	return &activationReturns{registered: make(chan *activationReturn, 128)}
}

func (r *activationReturns) configure(cfg *SessionConfig) {
	cfg.testOnly.subagentActivationReturn = r.capture
}

func (r *activationReturns) capture(sub *subagent, lease delegateLease) func() {
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	receipt := &activationReturn{
		sessionID: sub.sess.ID(), delegateID: sub.sess.owningDelegateID,
		lease: lease, generationDone: done, returned: make(chan struct{}),
	}
	select {
	case r.registered <- receipt:
	default:
		panic("activation registration capacity exceeded")
	}
	// This runs after WG.Done, so it must not access the session or recorder.
	return func() { close(receipt.returned) }
}

func (r *activationReturns) collect() {
	for {
		select {
		case receipt := <-r.registered:
			r.pending = append(r.pending, receipt)
			r.all = append(r.all, receipt)
		default:
			return
		}
	}
}

// take follows a known causal boundary (normally held provider entry). The
// factory runs before execution, so no polling or registration timeout is needed.
func (r *activationReturns) take(t *testing.T, sessionID string) *activationReturn {
	t.Helper()
	r.collect()
	for i, receipt := range r.pending {
		if receipt.sessionID == sessionID {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			if receipt.lease.delegateID != receipt.delegateID || receipt.lease.generation == 0 {
				t.Fatalf("activation identity = %+v", receipt)
			}
			return receipt
		}
	}
	t.Fatalf("no activation registered for session %s", sessionID)
	return nil
}

// Call after stopping wake producers and releasing provider gates. Close fences
// future admission and cancels/joins execution, then receipts join its final tail.
func (r *activationReturns) close(t *testing.T, root *Session) {
	t.Helper()
	root.Close()
	r.collect()
	for _, receipt := range r.all {
		awaitSignal(t, receipt.returned, "activation cleanup")
	}
}

// waitForRootAttention waits until root holds at least n unresolved
// attention items and returns them in transcript order.
func waitForRootAttention(t *testing.T, root *Session, n int) []string {
	t.Helper()
	var pending []string
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, fmt.Sprintf("root to hold %d unresolved attention items", n), func() bool {
		pending = rootPendingAttention(root)
		return len(pending) >= n
	})
	return pending
}

// notificationReportWakesParent uses real delegate creation, delivery and
// provider requests. Worker B cannot send any stimulus until A's report has
// reached the coordinator and its successful request has consumed that report.
func notificationReportWakesParent(t *testing.T, _ bool) {
	t.Helper()
	const payload = "c18f90d2-45ca-4c50-b17b-a67fb0e92783"
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-A", "WORKER-B")
	reportGate, siblingGate := make(chan struct{}), make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	releaseSibling := sync.OnceFunc(func() { close(siblingGate) })
	defer releaseReport()
	defer releaseSibling()
	returns := newActivationReturns()
	initialGate, observedGate, terminalGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	terminalEntered := make(chan struct{})
	releaseInitial := sync.OnceFunc(func() { close(initialGate) })
	releaseObserved := sync.OnceFunc(func() { close(observedGate) })
	releaseTerminal := sync.OnceFunc(func() { close(terminalGate) })
	defer releaseInitial()
	defer releaseObserved()
	defer releaseTerminal()
	workerIDs := make(chan string, 2)
	presented := make(chan llm.Request, 1)
	var siblingID string
	waitResponse := func() llm.Response {
		// Done with live children is now refused at admission. The old
		// ordinary-drain route therefore converges on the same valid wait.
		return toolCallResponse(communicateCallArgs("wait", map[string]any{
			"message": "waiting", "end_turn": true,
			"end_reason": "waiting_on_work", "waiting_on": siblingID,
		}))
	}
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: coordinate two workers.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting") },
	)
	adapter.script("COORD-BRIEF",
		func(llm.Request) llm.Response { return delegateToolResponse("a", "WORKER-A: report.") },
		func(req llm.Request) llm.Response {
			workerIDs <- deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
			return delegateToolResponse("b", "WORKER-B: report.")
		},
		func(req llm.Request) llm.Response {
			siblingID = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
			workerIDs <- siblingID
			awaitSignal(t, initialGate, "initial coordinator activation capture")
			return waitResponse()
		},
		func(req llm.Request) llm.Response {
			presented <- req
			awaitSignal(t, observedGate, "observing coordinator activation capture")
			return waitResponse()
		},
		func(llm.Request) llm.Response {
			close(terminalEntered)
			awaitSignal(t, terminalGate, "terminal coordinator activation capture")
			return finalResponse("coordinator finished")
		},
	)
	adapter.script("WORKER-A", func(llm.Request) llm.Response {
		awaitSignal(t, reportGate, "the coordinator to wait")
		return finalResponse(payload)
	})
	adapter.script("WORKER-B", func(llm.Request) llm.Response {
		awaitSignal(t, siblingGate, "A's report to be observed")
		return finalResponse("sibling finished")
	})
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	serveSupervisionRootWakes(t, root)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	adapter.waitForRequests(t, "COORD-BRIEF", 3)
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("coordinator children = %d", len(children))
	}
	coordinator := children[0]
	initial := returns.take(t, coordinator.ID())
	releaseInitial()
	waitForTestSignal(t, initial.returned, "initial coordinator yield")
	if coordinator.State() != SessionIdle {
		t.Fatalf("coordinator state = %s", coordinator.State())
	}
	workerID, sibling := <-workerIDs, <-workerIDs
	if workerID == "" || sibling == "" || workerID == sibling {
		t.Fatalf("worker identities = %q, %q", workerID, sibling)
	}
	releaseReport()
	var req llm.Request
	select {
	case req = <-presented:
	case <-ctx.Done():
		t.Fatalf("report did not wake coordinator before sibling release: coordinator requests=%d, error=%v", len(adapter.requestsFor("COORD-BRIEF")), ctx.Err())
	}
	if text := messagesText(req.Messages); strings.Count(text, payload) != 1 || !strings.Contains(text, `delegate_id="`+workerID+`"`) {
		t.Fatalf("coordinator request did not carry worker A's payload exactly once: %s", text)
	}
	observed := returns.take(t, coordinator.ID())
	if observed.lease != initial.lease || observed.generationDone != initial.generationDone {
		t.Fatalf("coordinator resumed a different generation: %+v -> %+v", initial, observed)
	}
	releaseObserved()
	waitForTestSignal(t, observed.returned, "coordinator response and yield while B remains held")
	attentionID := "delegate:" + workerID + "/delivery/1"
	fold := rootAttentionFold(t, coordinator)
	if fold.resolutions[attentionID] != delegateAttentionConsumed {
		t.Fatalf("report resolution = %s", fold.resolutions[attentionID])
	}
	if !slices.Equal(fold.order, []string{attentionID}) || fold.resumeGenerations[attentionID] != 0 {
		t.Fatalf("report delivery fold = order %v generations %v, want the presenting generation only", fold.order, fold.resumeGenerations)
	}
	if got := len(adapter.requestsFor("COORD-BRIEF")); got != 4 {
		t.Fatalf("coordinator requests = %d, want 4 without a duplicate observation", got)
	}
	coordRows := stableDelegateRowsForSession(root, false)
	if len(coordRows) != 1 || !coordRows[0].snapshot.currentRunOpen {
		t.Fatalf("coordinator settled while B remained live: %#v", coordRows)
	}
	if row := delegateAggregateSnapshot(t, root.delegateController, sibling); !row.CurrentRunOpen {
		t.Fatal("worker B stopped before it was released")
	}
	ownerFold := rootAttentionFold(t, root)
	for _, id := range ownerFold.order {
		if !strings.HasPrefix(id, "delegate:"+coordinator.owningDelegateID+"/update/") || !strings.Contains(ownerFold.content[id].Text(), `kind="update"`) {
			t.Fatalf("coordinator published a terminal report while B remained live: %s", id)
		}
	}
	for _, event := range publicationEvents(t, root) {
		if event.DelegateID == coordinator.owningDelegateID && event.RunFinished != nil {
			t.Fatal("coordinator RunFinished while B remained live")
		}
		if update := event.UpdatePosted; event.DelegateID == coordinator.owningDelegateID && update != nil {
			if update.Communicate == nil || update.Communicate.EndReason != "waiting_on_work" || update.Communicate.WaitingOn != sibling {
				t.Fatalf("coordinator publication is not its valid wait: %+v", update)
			}
		}
	}
	releaseSibling()
	waitForTestSignal(t, terminalEntered, "terminal coordinator provider")
	terminal := returns.take(t, coordinator.ID())
	if terminal.lease != initial.lease || terminal.generationDone != initial.generationDone {
		t.Fatalf("terminal coordinator generation = %+v", terminal)
	}
	releaseTerminal()
	waitForTestSignal(t, initial.generationDone, "captured coordinator generation done")
	waitForTestSignal(t, terminal.returned, "terminal coordinator owner cleanup")
	for _, event := range publicationEvents(t, root) {
		if event.DelegateID == coordinator.owningDelegateID && event.RunFinished != nil {
			return
		}
	}
	t.Fatal("coordinator did not publish RunFinished after sibling release")
}
