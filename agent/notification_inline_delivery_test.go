package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Block only the filesystem boundary, keeping read_file, the tool batch,
// delegate settlement, transcript writes and provider projection real.
type notificationReadGate struct {
	*execenv.LocalExecutionEnvironment
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *notificationReadGate) ReadFile(path string, offset, limit *int) (string, error) {
	return e.ReadFileCtx(context.Background(), path, offset, limit)
}
func (e *notificationReadGate) ReadFileCtx(ctx context.Context, path string, offset, limit *int) (string, error) {
	if filepath.Base(path) == "notification-gate.txt" {
		e.once.Do(func() { close(e.entered) })
		select {
		case <-e.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return e.LocalExecutionEnvironment.ReadFileCtx(ctx, path, offset, limit)
}

type notificationDeliveryAdapter struct {
	mu        sync.Mutex
	root      string
	rootSteps []func(context.Context, llm.Request) (llm.Response, error)
	child     func(context.Context, llm.Request) (llm.Response, error)
	requests  []llm.Request
}

func (*notificationDeliveryAdapter) Name() string { return "openai" }
func (*notificationDeliveryAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *notificationDeliveryAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	var step func(context.Context, llm.Request) (llm.Response, error)
	if req.SessionID == a.root {
		at := len(a.requests)
		a.requests = append(a.requests, req)
		if at < len(a.rootSteps) {
			step = a.rootSteps[at]
		}
	} else {
		step = a.child
	}
	a.mu.Unlock()
	if step == nil {
		return llm.Response{}, errors.New("unexpected provider request")
	}
	r, err := step(ctx, req)
	r.Provider = a.Name()
	r.Model = req.Model
	return r, err
}
func notificationToolCall(id, name string, args map[string]any) llm.Response {
	data, _ := json.Marshal(args)
	return toolCallResponse(llm.ToolCallData{ID: id, Name: name, Arguments: data, Type: "function"})
}
func notificationAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("async boundary did not complete")
	}
}
func notificationSession(t *testing.T, a *notificationDeliveryAdapter, env execenv.ExecutionEnvironment, delivered chan struct{}, configure ...func(*SessionConfig)) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(a)
	cfg := SessionConfig{StateDir: t.TempDir()}
	cfg.testOnly.disableDelegateIdleRelease = true
	var once sync.Once
	cfg.testOnly.subagentBeforeGenerationReleased = func(*subagent, delegateLease) { once.Do(func() { close(delivered) }) }
	for _, apply := range configure {
		apply(&cfg)
	}
	s, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), env, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.root = s.ID()
	t.Cleanup(s.Close)
	return s
}
func notificationCanonicalCount(t *testing.T, s *Session, id string) int {
	t.Helper()
	count := 0
	for _, e := range notificationRawEntries(t, s.TranscriptPath()) {
		if e.Turn.AttentionID == id {
			count++
		}
	}
	return count
}
func notificationRequestCount(req llm.Request, sentinel string) int {
	count := 0
	for _, m := range req.Messages {
		for _, p := range m.Content {
			count += strings.Count(p.Text, sentinel)
			if p.ToolResult != nil {
				count += strings.Count(fmt.Sprint(p.ToolResult.Content), sentinel)
			}
		}
	}
	return count
}
func notificationValidToolPairs(t *testing.T, req llm.Request) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.ToolCall != nil {
				pending[p.ToolCall.ID] = true
			}
			if p.ToolResult != nil {
				if !pending[p.ToolResult.ToolCallID] {
					t.Errorf("unmatched tool result %q", p.ToolResult.ToolCallID)
				}
				delete(pending, p.ToolResult.ToolCallID)
			}
		}
		if m.Role != llm.RoleTool && m.Role != llm.RoleAssistant && len(pending) != 0 {
			t.Errorf("message role %s interrupts pending calls %v", m.Role, pending)
		}
	}
	if len(pending) != 0 {
		t.Errorf("request has unanswered calls %v", pending)
	}
}

// A receiver queue would leave no synced canonical record until read_file
// finishes. Custody must instead complete while its external read is blocked.
func TestNotificationDeliveryDuringToolRound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notification-gate.txt"), []byte("file-data-8d22"), 0600); err != nil {
		t.Fatal(err)
	}
	gate := &notificationReadGate{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir), entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	delivered, childRelease := make(chan struct{}), make(chan struct{})
	var childOnce sync.Once
	releaseChild := func() { childOnce.Do(func() { close(childRelease) }) }
	t.Cleanup(releaseChild)
	a := &notificationDeliveryAdapter{}
	var delegateID string
	a.child = func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		select {
		case <-childRelease:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return toolCallResponse(communicateCall("report", "payload-cc81")), nil
	}
	a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
		func(context.Context, llm.Request) (llm.Response, error) {
			return notificationToolCall("create", "delegate", map[string]any{"prompt": "task-109b"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			delegateID = deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			return notificationToolCall("read", "read_file", map[string]any{"file_path": "notification-gate.txt", "intent": "reading fixture"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationValidToolPairs(t, req)
			if n := notificationRequestCount(req, "payload-cc81"); n != 1 {
				t.Errorf("report presentations=%d, want 1", n)
			}
			return finalResponse("answer-990d"), nil
		},
	}
	s := notificationSession(t, a, gate, delivered)
	done := make(chan error, 1)
	go func() { _, err := s.ProcessInput(context.Background(), "start-991e", nil); done <- err }()
	notificationAwait(t, gate.entered)
	releaseChild()
	notificationAwait(t, delivered)
	id := "delegate:" + delegateID + "/delivery/1"
	if n := notificationCanonicalCount(t, s, id); n != 1 {
		t.Errorf("canonical records before tool release=%d, want 1", n)
	}
	assertNotificationPending(t, s, []string{id})
	if pending := delegateAggregateSnapshot(t, s.delegateController, delegateID).PendingDeliveries; len(pending) != 0 {
		t.Errorf("source remained unacknowledged after receiver custody: %d", len(pending))
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
	a.mu.Lock()
	requests := len(a.requests)
	a.mu.Unlock()
	if requests != 3 {
		t.Fatalf("root requests=%d, want 3", requests)
	}
	t.Logf("canonical records=1, root requests=%d, report presentations in next request=1", requests)
}

// Inline completion is a subscriber: even before the tool-result carrier is
// stored, custody is canonical and pending, never owned by the waiter.
func TestNotificationInlineDelivery(t *testing.T) {
	t.Parallel()
	for _, duplicate := range []bool{false, true} {
		name := "result before timeout"
		if duplicate {
			name = "same generation duplicate"
		}
		t.Run(name, func(t *testing.T) {
			delivered, providerEntered, providerRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
			firstRelease := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(providerRelease) }) }
			t.Cleanup(release)
			a := &notificationDeliveryAdapter{}
			childCalls := 0
			a.child = func(ctx context.Context, _ llm.Request) (llm.Response, error) {
				childCalls++
				if childCalls == 1 {
					select {
					case <-firstRelease:
					case <-ctx.Done():
						return llm.Response{}, ctx.Err()
					}
					return toolCallResponse(communicateCall("first", "first-payload-688f")), nil
				}
				return toolCallResponse(communicateCall("report", "inline-payload-5cd1")), nil
			}
			a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
				func(context.Context, llm.Request) (llm.Response, error) {
					return notificationToolCall("create", "delegate", map[string]any{"prompt": "task-98cd"}), nil
				},
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					close(firstRelease)
					notificationAwait(t, delivered)
					id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
					return notificationToolCall("send", "delegate_send", map[string]any{"to": id, "message": "task-4fa2", "max_wait_ms": 60000}), nil
				},
				func(ctx context.Context, req llm.Request) (llm.Response, error) {
					close(providerEntered)
					select {
					case <-providerRelease:
					case <-ctx.Done():
						return llm.Response{}, ctx.Err()
					}
					notificationValidToolPairs(t, req)
					if n := notificationRequestCount(req, "inline-payload-5cd1"); n != 1 {
						t.Errorf("inline presentations=%d, want 1", n)
					}
					if n := notificationRequestCount(req, "first-payload-688f"); n != 1 {
						t.Errorf("earlier presentations=%d, want 1", n)
					}
					text := lastToolResultText(req.Messages)
					if strings.Index(text, "first-payload-688f") >= strings.Index(text, "inline-payload-5cd1") {
						t.Errorf("earlier packet not before latest packet: %s", text)
					}
					return finalResponse("answer-294d"), nil
				},
			}
			s := notificationSession(t, a, execenv.NewLocalExecutionEnvironment(t.TempDir()), delivered)
			done := make(chan error, 1)
			go func() { _, err := s.ProcessInput(context.Background(), "start-9de4", nil); done <- err }()
			notificationAwait(t, providerEntered)
			notificationAwait(t, delivered)
			a.mu.Lock()
			req := a.requests[1]
			a.mu.Unlock()
			delegateID := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			id := "delegate:" + delegateID + "/delivery/2"
			if n := notificationCanonicalCount(t, s, id); n != 1 {
				t.Errorf("inline canonical records=%d, want 1", n)
			}
			pending, err := s.pendingNotificationIDs()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, candidate := range pending {
				if candidate == id {
					found = true
				}
			}
			if !found {
				t.Errorf("inline ID missing before successful response, pending=%v", pending)
			}
			if duplicate {
				fold, err := readDelegateAttentionFold(s.TranscriptPath(), s.ID())
				if err != nil {
					t.Fatal(err)
				}
				if err := s.enqueueNotification(notification{ID: id, Message: fold.content[id]}); err != nil {
					t.Fatal(err)
				}
				if n := notificationCanonicalCount(t, s, id); n != 1 {
					t.Fatalf("duplicate canonical entries=%d", n)
				}
			}
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			assertNotificationPending(t, s, []string{})
			entries := notificationRawEntries(t, s.TranscriptPath())
			var bindings []schema.DelegateDeliveryCommit
			for _, e := range entries {
				if e.Turn.Kind == schema.TurnToolResults {
					bindings = append(bindings, e.Turn.DelegateDeliveryCommits...)
				}
			}
			wantBindings := []schema.DelegateDeliveryCommit{
				{ToolCallID: "send", DeliveryID: delegateDeliveryID(delegateID, 1)},
				{ToolCallID: "send", DeliveryID: delegateDeliveryID(delegateID, 2)},
			}
			if !slices.Equal(bindings, wantBindings) {
				t.Errorf("inline carrier bindings=%v, want %v", bindings, wantBindings)
			}
			var observed []string
			for _, entry := range entries {
				observed = append(observed, entry.Turn.ObservedNotificationIDs...)
			}
			wantObserved := []string{delegateAttentionID(delegateDeliveryID(delegateID, 1)), id}
			for _, canonicalID := range wantObserved {
				if n := notificationCanonicalCount(t, s, canonicalID); n != 1 {
					t.Errorf("canonical ID %s entries=%d, want 1", canonicalID, n)
				}
			}
			slices.Sort(observed)
			slices.Sort(wantObserved)
			if !slices.Equal(observed, wantObserved) {
				t.Fatalf("successful-request observation IDs=%v, want %v", observed, wantObserved)
			}
			t.Logf("canonical records=2, root requests=3, ordered inline carrier bindings=%v, each presentation=1, successful-response receipt IDs=%v", bindings, observed)
		})
	}
	for _, mode := range []string{"timeout before result", "canceled waiter", "tool result persistence failure"} {
		t.Run(mode, func(t *testing.T) { notificationInlineFallback(t, mode) })
	}
	t.Run("different target busy", notificationInlineDifferentTarget)
}

func notificationInlineDifferentTarget(t *testing.T) {
	delivered := make(chan struct{})
	waiting := make(chan struct{})
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	settledB := make(chan struct{})
	var onceA, onceB sync.Once
	unblockA := func() { onceA.Do(func() { close(releaseA) }) }
	unblockB := func() { onceB.Do(func() { close(releaseB) }) }
	t.Cleanup(unblockA)
	t.Cleanup(unblockB)
	a := &notificationDeliveryAdapter{}
	var idA, idB string
	aCalls := 0
	a.child = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		if strings.Contains(messagesText(req.Messages), "target-b-prompt-758f") {
			select {
			case <-releaseB:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
			return toolCallResponse(communicateCall("report-b", "target-b-result-284c")), nil
		}
		aCalls++
		if aCalls == 1 {
			return toolCallResponse(communicateCall("report-a-first", "target-a-first-c359")), nil
		}
		select {
		case <-releaseA:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return toolCallResponse(communicateCall("report-a-second", "target-a-second-d918")), nil
	}
	a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
		func(context.Context, llm.Request) (llm.Response, error) {
			return notificationToolCall("create-a", "delegate", map[string]any{"prompt": "target-a-prompt-834c"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationAwait(t, delivered)
			idA = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
			return notificationToolCall("create-b", "delegate", map[string]any{"prompt": "target-b-prompt-758f"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			idB = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
			return notificationToolCall("send-a", "delegate_send", map[string]any{"to": idA, "message": "second target-a task", "max_wait_ms": 60000}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationValidToolPairs(t, req)
			for _, sentinel := range []string{"target-b-result-284c", "target-a-second-d918"} {
				if n := notificationRequestCount(req, sentinel); n != 1 {
					t.Errorf("%s presentations=%d, want 1", sentinel, n)
				}
			}
			return finalResponse("both targets observed"), nil
		},
	}
	s := notificationSession(t, a, execenv.NewLocalExecutionEnvironment(t.TempDir()), delivered)
	var firstOnce sync.Once
	s.cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) {
		if lease.delegateID == idB {
			close(settledB)
		} else if lease.generation == 1 {
			firstOnce.Do(func() { close(delivered) })
		}
	}
	s.cfg.testOnly.delegateInlineWaitReady = func(context.Context, time.Duration) { close(waiting) }
	done := make(chan error, 1)
	go func() { _, err := s.ProcessInput(context.Background(), "start targets", nil); done <- err }()
	notificationAwait(t, waiting)
	unblockB()
	notificationAwait(t, settledB)
	bID := delegateAttentionID(delegateDeliveryID(idB, 1))
	if n := notificationCanonicalCount(t, s, bID); n != 1 {
		t.Fatalf("unrelated target canonical entries before inline release=%d", n)
	}
	if pending := delegateAggregateSnapshot(t, s.delegateController, idB).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("unrelated target source pending=%v", pending)
	}
	ids, err := s.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		if id == bID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unrelated canonical ID not pending=%v", ids)
	}
	unblockA()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
	t.Log("unrelated canonical records before inline release=1, each next-request presentation=1, root requests=4")
}

func notificationInlineFallback(t *testing.T, mode string) {
	t.Helper()
	firstDone := make(chan struct{})
	secondRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(secondRelease) }) }
	t.Cleanup(release)
	a := &notificationDeliveryAdapter{}
	childCalls := 0
	a.child = func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		childCalls++
		if childCalls == 1 {
			return toolCallResponse(communicateCall("first", "first-fallback-94de")), nil
		}
		select {
		case <-secondRelease:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return toolCallResponse(communicateCall("second", "fallback-payload-649c")), nil
	}
	var delegateID string
	var s *Session
	waitEntered := make(chan struct{})
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()
	a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
		func(context.Context, llm.Request) (llm.Response, error) {
			return notificationToolCall("create", "delegate", map[string]any{"prompt": "first fallback task"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationAwait(t, firstDone)
			delegateID = deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			wait := 60000
			if mode == "timeout before result" {
				wait = 1
			}
			return notificationToolCall("send", "delegate_send", map[string]any{"to": delegateID, "message": "second fallback task", "max_wait_ms": wait}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			if mode == "timeout before result" {
				var state delegateSendResult
				found := false
				for _, message := range req.Messages {
					for _, part := range message.Content {
						if part.ToolResult != nil && part.ToolResult.ToolCallID == "send" {
							found = true
							if err := json.Unmarshal(part.ToolResult.ToolState, &state); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if !found || !state.TimedOut || !state.RunningInBackground {
					t.Errorf("send timeout state: found=%t timed_out=%t running_in_background=%t", found, state.TimedOut, state.RunningInBackground)
				}
				return finalResponse("timeout turn ended"), nil
			}
			notificationValidToolPairs(t, req)
			if n := notificationRequestCount(req, "fallback-payload-649c"); n != 1 {
				t.Errorf("fallback presentations=%d, want 1", n)
			}
			return finalResponse("ordinary fallback observed"), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationValidToolPairs(t, req)
			if n := notificationRequestCount(req, "fallback-payload-649c"); n != 1 {
				t.Errorf("ordinary fallback presentations=%d, want 1", n)
			}
			return finalResponse("ordinary fallback observed"), nil
		},
	}
	s = notificationSession(t, a, execenv.NewLocalExecutionEnvironment(t.TempDir()), firstDone)
	settled := make(chan struct{})
	s.cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) {
		if lease.generation == 1 {
			close(firstDone)
		} else if lease.generation == 2 {
			close(settled)
		}
	}
	fs := &notificationToolResultFailFS{Fs: afero.NewOsFs()}
	if mode == "tool result persistence failure" {
		if err := s.closeAttachedTranscript(); err != nil {
			t.Fatal(err)
		}
		writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
		if err != nil {
			t.Fatal(err)
		}
		s.attachTranscript(writer)
		s.cfg.testOnly.delegateDeliveryCommitsTaken = func() { fs.mu.Lock(); fs.armed = true; fs.mu.Unlock() }
	}
	s.cfg.testOnly.delegateInlineWaitReady = func(ctx context.Context, _ time.Duration) {
		close(waitEntered)
		if mode == "timeout before result" {
			<-ctx.Done()
		} else if mode == "canceled waiter" {
			cancelRoot()
		} else {
			release()
		}
	}
	_, err := s.ProcessInput(rootCtx, "start fallback", nil)
	notificationAwait(t, waitEntered)
	if mode == "timeout before result" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil {
		t.Fatal("canceled or failed persistence unexpectedly succeeded")
	}
	s.cfg.testOnly.delegateDeliveryCommitsTaken = nil
	if mode == "tool result persistence failure" {
		notificationAwait(t, settled)
	} else {
		release()
		notificationAwait(t, settled)
	}
	id := delegateAttentionID(delegateDeliveryID(delegateID, 2))
	if n := notificationCanonicalCount(t, s, id); n != 1 {
		t.Fatalf("canonical fallback records=%d, want 1", n)
	}
	ids, readErr := s.pendingNotificationIDs()
	if readErr != nil {
		t.Fatal(readErr)
	}
	found := false
	for _, candidate := range ids {
		if candidate == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed waiter swallowed ordinary notification: %v", ids)
	}
	if pending := delegateAggregateSnapshot(t, s.delegateController, delegateID).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("source still pending after canonical custody=%v", pending)
	}
	if _, err := s.ProcessInput(context.Background(), "observe fallback", nil); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, s, []string{})
	journal, err := s.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	ackCount := 0
	for _, event := range journal {
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DeliveryAcknowledged.DeliveryID == delegateDeliveryID(delegateID, 2) {
			ackCount++
		}
	}
	if ackCount != 1 {
		t.Fatalf("source acknowledgments=%d, want 1", ackCount)
	}
	t.Log("canonical fallback records=1, ordinary presentation=1, source acknowledgments=1")
}

func TestNotificationInlineDeliverySameBatchSubscribers(t *testing.T) {
	for _, mode := range []string{"persisted", "canceled", "write_failure"} {
		t.Run(mode, func(t *testing.T) { notificationSameBatchSubscribers(t, mode) })
	}
}

func notificationSameBatchSubscribers(t *testing.T, mode string) {
	t.Helper()
	a := &notificationDeliveryAdapter{}
	delivered, firstRelease := make(chan struct{}), make(chan struct{})
	childCalls := 0
	a.child = func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		childCalls++
		if childCalls == 1 {
			select {
			case <-firstRelease:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return toolCallResponse(communicateCall(fmt.Sprint("report", childCalls), fmt.Sprint("batch-payload-f31d-", childCalls))), nil
	}
	var delegateID string
	a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
		func(context.Context, llm.Request) (llm.Response, error) {
			return notificationToolCall("create", "delegate", map[string]any{"prompt": "batch-fixture"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			close(firstRelease)
			notificationAwait(t, delivered)
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			delegateID = id
			first := notificationToolCall("send1", "delegate_send", map[string]any{"to": id, "message": "next1", "max_wait_ms": 60000})
			second := notificationToolCall("send2", "delegate_send", map[string]any{"to": id, "message": "next2", "max_wait_ms": 60000})
			first.Message.Content = append(first.Message.Content, second.Message.Content...)
			return first, nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationValidToolPairs(t, req)
			for i := 1; i <= 3; i++ {
				if n := notificationRequestCount(req, fmt.Sprint("batch-payload-f31d-", i)); n != 1 {
					t.Errorf("generation %d presentations=%d, want 1", i, n)
				}
			}
			return finalResponse("batch-answer"), nil
		},
	}
	s := notificationSession(t, a, execenv.NewLocalExecutionEnvironment(t.TempDir()), delivered)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode == "canceled" {
		s.cfg.testOnly.delegateDeliveryCommitsTaken = cancel
	} else if mode == "write_failure" {
		if err := s.closeAttachedTranscript(); err != nil {
			t.Fatal(err)
		}
		fs := &notificationToolResultFailFS{Fs: afero.NewOsFs()}
		writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
		if err != nil {
			t.Fatal(err)
		}
		s.attachTranscript(writer)
		s.cfg.testOnly.delegateDeliveryCommitsTaken = func() { fs.mu.Lock(); fs.armed = true; fs.mu.Unlock() }
	}
	_, err := s.ProcessInput(ctx, "start", nil)
	if mode == "persisted" && err != nil {
		t.Fatal(err)
	}
	if mode != "persisted" {
		if err == nil {
			t.Fatal("unpersisted batch unexpectedly succeeded")
		}
		ids := make([]string, 3)
		for i := range ids {
			ids[i] = delegateAttentionID(delegateDeliveryID(delegateID, uint64(i+1)))
		}
		assertNotificationPending(t, s, ids)
		s.cfg.testOnly.delegateDeliveryCommitsTaken = nil
		if _, err := s.ProcessInput(context.Background(), "observe abandoned batch", nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		if n := notificationCanonicalCount(t, s, delegateAttentionID(delegateDeliveryID(delegateID, uint64(i)))); n != 1 {
			t.Errorf("generation %d canonical records=%d, want 1", i, n)
		}
	}
	assertNotificationPending(t, s, []string{})
	t.Log("canonical records=3, each next-request payload=1, pending after successful response=0")
}

func TestNotificationInlineHistoricalCarrierDoesNotCreateObservation(t *testing.T) {
	for _, withState := range []bool{false, true} {
		t.Run(fmt.Sprintf("structured_state_%t", withState), func(t *testing.T) {
			notificationHistoricalCarrier(t, withState)
		})
	}
}

func notificationHistoricalCarrier(t *testing.T, withState bool) {
	t.Helper()
	stateDir := t.TempDir()
	const sessionID = "historical-inline-owner"
	path := transcriptPath(stateDir, sessionID)
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID, ProfileID: "openai", Model: "gpt-5.4"})
	if err != nil {
		t.Fatal(err)
	}
	call := notificationToolCall("historical-send", "delegate_send", map[string]any{"to": "dlg_historical", "message": "old task"})
	message := "historical-payload-4b16"
	state := delegateSendResult{DelegateID: "dlg_historical", Action: "completed", Output: &message}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	carrier := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "historical-send", Name: "delegate_send", Content: formatDelegateSend(state), ToolState: encoded}}}})
	if !withState {
		carrier.Message.Content[0].ToolResult.Content = message
		carrier.Message.Content[0].ToolResult.ToolState = nil
	}
	carrier.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "historical-send", DeliveryID: delegateDeliveryID("dlg_historical", 1)}}
	for _, turn := range []schema.Turn{schema.NewTurn(schema.TurnAssistant, call.Message), carrier} {
		if err := w.AppendDurable(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	a := &notificationDeliveryAdapter{root: sessionID}
	step := func(_ context.Context, req llm.Request) (llm.Response, error) {
		notificationValidToolPairs(t, req)
		if n := notificationRequestCount(req, "historical-payload-4b16"); n != 1 {
			t.Errorf("historical presentations=%d, want 1", n)
		}
		return finalResponse("historical response"), nil
	}
	a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){step, step}
	client := llm.NewClient()
	client.Register(a)
	meta := schema.SessionMeta{ID: sessionID, ProfileID: "openai", Model: "gpt-5.4"}
	for reopen := 0; reopen < 2; reopen++ {
		s, err := RestoreSessionFromMeta(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
		if err != nil {
			t.Fatalf("reopen %d: %v", reopen, err)
		}
		_, runErr := s.ProcessInput(context.Background(), "continue historical", nil)
		s.Close()
		if runErr != nil {
			t.Fatal(runErr)
		}
		if _, err := readDelegateAttentionFold(path, sessionID); err != nil {
			t.Fatalf("fold after successful response %d: %v", reopen, err)
		}
	}
	for _, entry := range notificationRawEntries(t, path) {
		if entry.Turn.AttentionID != "" || len(entry.Turn.ObservedNotificationIDs) != 0 {
			t.Fatalf("historical-only carrier gained canonical acceptance/observation: %+v", entry.Turn)
		}
	}
}

// This is installed through the real strategy configuration and publication
// path, not by handing the presentation helper a preselected history slice.
type notificationReducingStrategy struct {
	spyStrategy
	mode string
	ran  bool
}

func (s *notificationReducingStrategy) ManageContext(_ context.Context, history *[]schema.Turn, _ int, _ func(events.EventKind, events.EventData)) error {
	for i, turn := range *history {
		if len(turn.DelegateDeliveryCommits) == 0 {
			continue
		}
		s.ran = true
		if s.mode == "carrier removal" {
			// Remove the assistant call and its result together, preserving pairs.
			filtered := make([]schema.Turn, 0, len(*history)-2)
			for at, candidate := range *history {
				remove := at == i
				for _, part := range candidate.Message.Content {
					remove = remove || (part.ToolCall != nil && part.ToolCall.ID == "send-inline")
				}
				if !remove {
					filtered = append(filtered, candidate)
				}
			}
			*history = filtered
			return nil
		}
		turn.Message.Content = append([]llm.ContentPart(nil), turn.Message.Content...)
		for j, part := range turn.Message.Content {
			if part.ToolResult != nil {
				result := *part.ToolResult
				result.Content = "strategy omitted tool body"
				if s.mode == "unicode body reduction" {
					result.Content = strings.Repeat("界", 12)
				}
				turn.Message.Content[j].ToolResult = &result
			}
		}
		(*history)[i] = turn
	}
	return nil
}

func TestNotificationInlineDeliveryTruncatedCarrier(t *testing.T) {
	for _, mode := range []string{"registry clipping", "body reduction", "unicode body reduction", "carrier removal"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "notification-gate.txt"), []byte("gate"), 0600); err != nil {
				t.Fatal(err)
			}
			gate := &notificationReadGate{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir), entered: make(chan struct{}), release: make(chan struct{})}
			var gateOnce sync.Once
			release := func() { gateOnce.Do(func() { close(gate.release) }) }
			t.Cleanup(release)
			settled := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
			firstRelease := make(chan struct{})
			a := &notificationDeliveryAdapter{}
			childCalls := 0
			a.child = func(ctx context.Context, _ llm.Request) (llm.Response, error) {
				childCalls++
				if childCalls == 1 {
					select {
					case <-firstRelease:
					case <-ctx.Done():
						return llm.Response{}, ctx.Err()
					}
				}
				message := fmt.Sprintf("shaped-payload-774b-%d", childCalls)
				if mode == "registry clipping" && childCalls != 2 {
					message += strings.Repeat(fmt.Sprint(childCalls), 40000)
				}
				return toolCallResponse(communicateCall(fmt.Sprint("report", childCalls), message)), nil
			}
			var delegateID string
			a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
				func(context.Context, llm.Request) (llm.Response, error) {
					return notificationToolCall("create", "delegate", map[string]any{"prompt": "shaping task"}), nil
				},
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					close(firstRelease)
					notificationAwait(t, settled[0])
					delegateID = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
					response := notificationToolCall("send-zero", "delegate_send", map[string]any{"to": delegateID, "message": "second task"})
					read := notificationToolCall("read-gate", "read_file", map[string]any{"file_path": "notification-gate.txt", "intent": "reading fixture"})
					send := notificationToolCall("send-inline", "delegate_send", map[string]any{"to": delegateID, "message": "third task", "max_wait_ms": 60000})
					response.Message.Content = append(response.Message.Content, read.Message.Content...)
					response.Message.Content = append(response.Message.Content, send.Message.Content...)
					return response, nil
				},
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					notificationValidToolPairs(t, req)
					for generation := 1; generation <= 3; generation++ {
						if n := notificationRequestCount(req, fmt.Sprintf("shaped-payload-774b-%d", generation)); n != 1 {
							t.Errorf("generation %d presentations=%d, want 1", generation, n)
						}
					}
					for _, m := range req.Messages {
						for _, part := range m.Content {
							if mode == "unicode body reduction" && part.ToolResult != nil && part.ToolResult.ToolCallID == "send-inline" && len([]rune(fmt.Sprint(part.ToolResult.Content))) > 12 {
								t.Errorf("status fallback exceeded reduced character budget: %d", len([]rune(fmt.Sprint(part.ToolResult.Content))))
							}
							// Registry warnings and the recovery footer sit outside the body cap.
							if part.ToolResult != nil && part.ToolResult.Name == "delegate_send" && len(fmt.Sprint(part.ToolResult.Content)) > jobToolResultDefaultMaxChar+512 {
								t.Errorf("delegate_send output bypassed limit: %d", len(fmt.Sprint(part.ToolResult.Content)))
							}
							if mode == "registry clipping" && part.ToolResult != nil && part.ToolResult.ToolCallID == "send-inline" {
								var state delegateSendResult
								if err := json.Unmarshal(part.ToolResult.ToolState, &state); err != nil {
									t.Fatal(err)
								}
								if len(state.EarlierResults) != 2 || len(formatDelegateSend(state)) <= jobToolResultDefaultMaxChar {
									t.Fatalf("registry clipping setup: earlier=%d full size=%d", len(state.EarlierResults), len(formatDelegateSend(state)))
								}
							}
						}
					}
					return finalResponse("shaped response"), nil
				},
			}
			strategy := &notificationReducingStrategy{mode: mode}
			s := notificationSession(t, a, gate, make(chan struct{}), func(cfg *SessionConfig) {
				if mode != "registry clipping" {
					cfg.testOnly.contextStrategyOverride = strategy
				}
				cfg.testOnly.subagentBeforeGenerationReleased = func(_ *subagent, lease delegateLease) { close(settled[lease.generation-1]) }
			})
			done := make(chan error, 1)
			go func() { _, err := s.ProcessInput(context.Background(), "start shaping", nil); done <- err }()
			notificationAwait(t, gate.entered)
			notificationAwait(t, settled[1])
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if mode != "registry clipping" && !strategy.ran {
				t.Fatal("configured strategy did not shape a carrier")
			}
			var observed []string
			for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
				if mode == "registry clipping" && len(entry.Turn.DelegateDeliveryCommits) != 0 {
					t.Errorf("clipped carrier retained presentation bindings: %v", entry.Turn.DelegateDeliveryCommits)
				}
				observed = append(observed, entry.Turn.ObservedNotificationIDs...)
			}
			want := []string{}
			for i := 1; i <= 3; i++ {
				id := delegateAttentionID(delegateDeliveryID(delegateID, uint64(i)))
				want = append(want, id)
				if n := notificationCanonicalCount(t, s, id); n != 1 {
					t.Errorf("canonical records for %s=%d", id, n)
				}
			}
			slices.Sort(observed)
			if !slices.Equal(observed, want) {
				t.Errorf("successful response IDs=%v, want %v", observed, want)
			}
			assertNotificationPending(t, s, []string{})
			t.Logf("canonical records=3, final-request payload counts=[1 1 1], successful response IDs=%v", observed)
		})
	}
}

// Call-name-only payload substitution loses completed errors and invents a
// notification for an unexecuted send. Compare with the result before cancel.
func TestNotificationInlineDeliveryCanceledPreservesNonCarrierResults(t *testing.T) {
	for _, prevalidation := range []bool{false, true} {
		t.Run(fmt.Sprintf("prevalidation_%t", prevalidation), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "notification-gate.txt"), []byte("gate"), 0600); err != nil {
				t.Fatal(err)
			}
			gate := &notificationReadGate{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir), entered: make(chan struct{}), release: make(chan struct{})}
			var original events.ToolCallEndData
			originalReady := make(chan struct{})
			a := &notificationDeliveryAdapter{}
			a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
				func(context.Context, llm.Request) (llm.Response, error) {
					args := map[string]any{"to": "caller", "message": "rejected send"}
					if prevalidation {
						args["max_wait_ms"] = map[string]any{"invalid": true}
					}
					response := notificationToolCall("send-error", "delegate_send", args)
					for _, next := range []llm.Response{
						notificationToolCall("read-gate", "read_file", map[string]any{"file_path": "notification-gate.txt", "intent": "blocking after send error"}),
						notificationToolCall("send-unexecuted", "delegate_send", map[string]any{"to": "caller", "message": "must not execute"}),
						notificationToolCall("read-unexecuted", "read_file", map[string]any{"file_path": "notification-gate.txt", "intent": "unexecuted cancellation control"}),
					} {
						response.Message.Content = append(response.Message.Content, next.Message.Content...)
					}
					return response, nil
				},
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					notificationValidToolPairs(t, req)
					results := map[string]*llm.ToolResultData{}
					for _, message := range req.Messages {
						for _, part := range message.Content {
							if part.ToolResult != nil {
								results[part.ToolResult.ToolCallID] = part.ToolResult
							}
						}
					}
					completed := results["send-error"]
					if completed == nil {
						t.Fatal("resumed request omitted completed send error")
					}
					if completed.Content != original.Error || !completed.IsError || completed.PrevalOnly != original.PrevalOnly {
						t.Errorf("completed result changed: content=%q error=%t prevalidation=%t, original=%q prevalidation=%t", completed.Content, completed.IsError, completed.PrevalOnly, original.Error, original.PrevalOnly)
					}
					skippedSend, skippedRead := results["send-unexecuted"], results["read-unexecuted"]
					if skippedSend == nil || skippedRead == nil {
						t.Fatal("resumed request omitted unexecuted result pairs")
					}
					if skippedSend.Content != skippedRead.Content || !skippedSend.IsError || skippedSend.PrevalOnly {
						t.Errorf("unexecuted send lost cancellation result: send=%+v, read control=%+v", skippedSend, skippedRead)
					}
					return finalResponse("resumed non-carrier response"), nil
				},
			}
			s := notificationSession(t, a, gate, make(chan struct{}))
			drained := make(chan struct{})
			var unexecutedEnds int
			s.ConsumeEventsLossless(func(event events.SessionEvent) {
				if data, ok := event.Data.(events.ToolCallEndData); ok {
					switch data.CallID {
					case "send-error":
						original = data
						close(originalReady)
					case "send-unexecuted", "read-unexecuted":
						unexecutedEnds++
					}
				}
			}, func() { close(drained) })
			t.Cleanup(func() { s.Close(); notificationAwait(t, drained) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.ProcessInput(ctx, "start non-carrier cancellation", nil); done <- err }()
			notificationAwait(t, gate.entered)
			notificationAwait(t, originalReady)
			if original.Error == "" || len(original.Error) > jobToolResultDefaultMaxChar || original.PrevalOnly != prevalidation {
				t.Fatalf("original bounded error missing or wrong execution path: %+v", original)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled batch error=%v", err)
			}
			if _, err := s.ProcessInput(context.Background(), "resume non-carrier cancellation", nil); err != nil {
				t.Fatal(err)
			}
			s.Close()
			notificationAwait(t, drained)
			if unexecutedEnds != 0 {
				t.Errorf("trailing calls executed before cancellation: %d", unexecutedEnds)
			}
			for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
				if entry.Turn.AttentionID != "" || len(entry.Turn.ObservedNotificationIDs) != 0 || len(entry.Turn.DelegateDeliveryCommits) != 0 {
					t.Fatalf("non-carrier fabricated notification state: %+v", entry.Turn)
				}
			}
			t.Logf("captured original error bytes=%d, prevalidation=%t, unexecuted tool ends=%d, canonical records=0, receipts=0", len(original.Error), prevalidation, unexecutedEnds)
		})
	}
}

func TestNotificationInlineDeliveryCanceledBeforeBindingTake(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notification-gate.txt"), []byte("gate"), 0600); err != nil {
		t.Fatal(err)
	}
	gate := &notificationReadGate{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir), entered: make(chan struct{}), release: make(chan struct{})}
	delivered := make(chan struct{})
	firstRelease := make(chan struct{})
	a := &notificationDeliveryAdapter{}
	childCalls := 0
	a.child = func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		childCalls++
		if childCalls == 1 {
			select {
			case <-firstRelease:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return toolCallResponse(communicateCall(fmt.Sprint("report", childCalls), fmt.Sprint("cancel-payload-b831-", childCalls))), nil
	}
	var delegateID string
	a.rootSteps = []func(context.Context, llm.Request) (llm.Response, error){
		func(context.Context, llm.Request) (llm.Response, error) {
			return notificationToolCall("create", "delegate", map[string]any{"prompt": "cancel task"}), nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			close(firstRelease)
			notificationAwait(t, delivered)
			delegateID = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
			response := notificationToolCall("send", "delegate_send", map[string]any{"to": delegateID, "message": "second task", "max_wait_ms": 60000})
			read := notificationToolCall("read-gate", "read_file", map[string]any{"file_path": "notification-gate.txt", "intent": "reading fixture"})
			response.Message.Content = append(response.Message.Content, read.Message.Content...)
			return response, nil
		},
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			notificationValidToolPairs(t, req)
			for i := 1; i <= 2; i++ {
				if n := notificationRequestCount(req, fmt.Sprint("cancel-payload-b831-", i)); n != 1 {
					t.Errorf("generation %d presentations=%d, want 1", i, n)
				}
			}
			return finalResponse("resumed response"), nil
		},
	}
	s := notificationSession(t, a, gate, delivered)
	taken := false
	s.cfg.testOnly.delegateDeliveryCommitsTaken = func() { taken = true }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.ProcessInput(ctx, "start cancellation", nil); done <- err }()
	notificationAwait(t, gate.entered)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch error=%v", err)
	}
	if taken {
		t.Fatal("cancellation happened after binding collection")
	}
	want := []string{delegateAttentionID(delegateDeliveryID(delegateID, 1)), delegateAttentionID(delegateDeliveryID(delegateID, 2))}
	assertNotificationPending(t, s, want)
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		if len(entry.Turn.ObservedNotificationIDs) != 0 {
			t.Fatalf("canceled batch observed %v", entry.Turn.ObservedNotificationIDs)
		}
	}
	if _, err := s.ProcessInput(context.Background(), "resume cancellation", nil); err != nil {
		t.Fatal(err)
	}
	var observed []string
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		observed = append(observed, entry.Turn.ObservedNotificationIDs...)
	}
	slices.Sort(observed)
	if !slices.Equal(observed, want) {
		t.Errorf("resumed response IDs=%v, want %v", observed, want)
	}
	for _, id := range want {
		if n := notificationCanonicalCount(t, s, id); n != 1 {
			t.Errorf("canonical records for %s=%d", id, n)
		}
	}
	assertNotificationPending(t, s, []string{})
	t.Logf("cancellation before binding collection, canonical records=2, resumed payload counts=[1 1], response IDs=%v", observed)
}

type notificationToolResultFailFS struct {
	afero.Fs
	mu    sync.Mutex
	armed bool
}

func (fs *notificationToolResultFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &notificationToolResultFailFile{File: f, fs: fs}, nil
}

type notificationToolResultFailFile struct {
	afero.File
	fs *notificationToolResultFailFS
}

func (f *notificationToolResultFailFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	armed := f.fs.armed
	f.fs.armed = false
	f.fs.mu.Unlock()
	if armed {
		return 0, errInjectedTranscriptWrite
	}
	return f.File.Write(p)
}
