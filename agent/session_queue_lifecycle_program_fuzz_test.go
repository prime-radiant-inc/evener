//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
)

// FuzzSessionQueueLifecycleProgram drives the small state machines surrounding
// queued input, steering, follow-ups, interrupt draining, and job-notification
// retry scheduling. The target is deliberately single-threaded and uses a fake
// clock so replay never depends on provider, network, or wall-clock behavior.
func FuzzSessionQueueLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{1},
		{2},
		{3},
		{4},
		{5},
		{6},
		{7},
		{0, 1, 2, 3, 4, 5, 6, 7},
		{8, 9, 10, 11, 12, 13, 14, 16, 15},
		{17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33},
		{102, 119, 204, 205, 206, 207, 208, 209, 210, 211, 212, 213, 214, 215, 216, 217, 218, 219, 220},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		first := qlifRunProgram(t, data)
		second := qlifRunProgram(t, data)
		if first != second {
			t.Fatalf("non-deterministic replay:\nfirst:  %s\nsecond: %s", first, second)
		}
		qlifCheckDrainContext(t, data)
	})
}

func qlifRunProgram(t *testing.T, data []byte) string {
	t.Helper()
	clk := agenttest.NewFakeClock()
	s := newSession(t, withConfig(SessionConfig{clock: clk}))
	if len(data) == 0 {
		data = []byte{0}
	}
	if len(data) > 32 {
		data = data[:32]
	}
	var trace strings.Builder
	notifyCount := 0
	qlifExerciseNotificationRetry(t, s, clk)
	s.SetNotifyFunc(func() { notifyCount++ })
	qlifExerciseQueueHelpers(t, s)

	for _, raw := range data {
		op := int(raw) % 17
		variant := int(raw) / 17
		imageCount := (variant / 6) % 3
		text := []string{"", "  ", "alpha", "beta\nrest", "gamma\r", "delta"}[variant%6]
		switch op {
		case 0, 1: // enqueue, including image-only and canceled-context branches
			ctx := context.Background()
			if op == 1 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			images := qlifImages(imageCount)
			err := s.EnqueueWithImages(ctx, text, images)
			if err == nil && len(images) > 0 {
				images[0] = ImageAttachment{Name: "replacement"}
				s.mu.Lock()
				copied := s.inputQueue[len(s.inputQueue)-1].Images[0].Name
				s.mu.Unlock()
				if copied == images[0].Name {
					t.Fatal("EnqueueWithImages retained caller image storage")
				}
			}
			fmt.Fprintf(&trace, "e:%v:%d;", err != nil, s.QueueDepth())
		case 2:
			s.pushQueueHead(queuedInput{Text: text, Images: qlifImages(imageCount), Provenance: qlifQueueProvenance()})
			fmt.Fprintf(&trace, "h:%d;", s.QueueDepth())
		case 3:
			entry := s.popQueueHead()
			fmt.Fprintf(&trace, "p:%q:%d;", entry.Text, len(entry.Images))
		case 4:
			before := len(s.SteeringQueueSnapshot())
			images := qlifImages(imageCount)
			s.SteerWithImages(text, images)
			if len(images) > 0 {
				images[0] = ImageAttachment{Name: "replacement"}
			}
			after := s.SteeringQueueSnapshot()
			if len(after) > before && len(after[len(after)-1].Images) > 0 && after[len(after)-1].Images[0].Name == images[0].Name {
				t.Fatal("SteerWithImages retained caller image storage")
			}
			fmt.Fprintf(&trace, "s:%d;", len(after))
		case 5:
			drained := s.drainSteering()
			s.prependSteering(drained)
			fmt.Fprintf(&trace, "d:%d;", len(drained))
		case 6:
			s.FollowUp(text)
			fmt.Fprintf(&trace, "f:%q;", s.popFollowUp())
		case 7:
			preview := s.QueuePreview()
			if len(preview) > 0 {
				preview[0] = "mutated"
				if s.QueuePreview()[0] == "mutated" {
					t.Fatal("QueuePreview returned mutable session storage")
				}
			}
			fmt.Fprintf(&trace, "v:%q;", preview)
		case 8:
			n := jobNotification{JobID: fmt.Sprintf("job-%d", variant%4), Reason: text}
			s.enqueueJobNotificationAndNotify(n)
			fmt.Fprintf(&trace, "n:%d:%d;", s.peekNotifications(), notifyCount)
		case 9:
			n := jobNotification{JobID: fmt.Sprintf("retry-%d", variant%4), Reason: text}
			s.requeueJobNotifications([]jobNotification{n})
			s.pendingJobNotifsMu.Lock()
			active := s.jobNotifyRetry.active
			s.pendingJobNotifsMu.Unlock()
			fmt.Fprintf(&trace, "r:%d:%v;", s.peekNotifications(), active)
		case 10:
			drained := s.drainJobNotifications()
			if len(drained) > 0 {
				fmt.Fprintf(&trace, "j:%s:%d;", drained[0].JobID, len(drained))
			} else {
				trace.WriteString("j::0;")
			}
		case 11:
			s.resetJobNotificationRetry()
			s.pendingJobNotifsMu.Lock()
			active := s.jobNotifyRetry.active
			delay := s.jobNotifyRetry.delay
			s.pendingJobNotifsMu.Unlock()
			fmt.Fprintf(&trace, "x:%v:%s;", active, delay)
		case 12:
			s.SetNotifyFunc(nil)
			trace.WriteString("z;")
		case 13:
			s.SetNotifyFunc(func() { notifyCount++ })
			fmt.Fprintf(&trace, "w:%d;", notifyCount)
		case 14:
			entries := s.drainSteering()
			if len(entries) > 0 {
				msg := steeringMessageToLLM(entries[0])
				fmt.Fprintf(&trace, "m:%s:%d;", msg.Role, len(msg.Content))
			} else {
				trace.WriteString("m::0;")
			}
		case 15:
			s.Close()
			if err := s.Enqueue(context.Background(), "after-close"); err == nil {
				t.Fatal("enqueue succeeded after Close")
			}
			trace.WriteString("c;")
		case 16:
			entry := steeringMessage{Text: text, Images: qlifImages(imageCount), Provenance: qlifQueueProvenance()}
			data := steeringInjectedDataFromMessage(entry)
			fmt.Fprintf(&trace, "i:%q:%d;", data.Text, len(data.Images))
		}
	}

	return trace.String()
}

func qlifExerciseNotificationRetry(t *testing.T, s *Session, clk *agenttest.FakeClock) {
	t.Helper()
	s.requeueJobNotifications(nil)
	fired := make(chan struct{}, 2)
	s.SetNotifyFunc(func() { fired <- struct{}{} })
	s.requeueJobNotifications([]jobNotification{{JobID: "retry-first"}})
	s.requeueJobNotifications([]jobNotification{{JobID: "retry-second"}})
	clk.Advance(jobNotificationRetryInitialDelay)
	<-fired
	s.pendingJobNotifsMu.Lock()
	active := s.jobNotifyRetry.active
	delay := s.jobNotifyRetry.delay
	s.pendingJobNotifsMu.Unlock()
	if active || delay != 2*jobNotificationRetryInitialDelay {
		t.Fatalf("first retry state active=%v delay=%s", active, delay)
	}
	_ = s.drainJobNotifications()
	s.resetJobNotificationRetry()

	s.pendingJobNotifsMu.Lock()
	s.jobNotifyRetry.delay = jobNotificationRetryMaxDelay
	s.pendingJobNotifsMu.Unlock()
	s.requeueJobNotifications([]jobNotification{{JobID: "retry-capped"}})
	clk.Advance(jobNotificationRetryMaxDelay)
	<-fired
	s.pendingJobNotifsMu.Lock()
	active = s.jobNotifyRetry.active
	delay = s.jobNotifyRetry.delay
	s.pendingJobNotifsMu.Unlock()
	if active || delay != jobNotificationRetryMaxDelay {
		t.Fatalf("capped retry state active=%v delay=%s", active, delay)
	}
	_ = s.drainJobNotifications()
	s.resetJobNotificationRetry()
}

func qlifExerciseQueueHelpers(t *testing.T, s *Session) {
	t.Helper()
	ctx := WithQueuedInputDrainOnInterrupt(context.Background(), nil)
	if got, ok := queuedInputDrainContext(ctx, context.Canceled); !ok || got == nil {
		t.Fatal("default queued-input drain context was rejected")
	}

	if s.hasPendingSteering() {
		t.Fatal("new session unexpectedly has pending steering")
	}
	s.Steer("plain")
	s.SteerWithProvenance("provenance", qlifQueueProvenance())
	if !s.trySteerWithProvenanceAndNotify("notify", qlifQueueProvenance()) {
		t.Fatal("open session rejected steering with notify")
	}
	s.deliverHookContext("")
	s.deliverHookContext("hook context")
	s.deliverHookUserMessage("")
	s.deliverHookUserMessage("hook warning")
	entries := s.drainSteering()
	if len(entries) != 4 {
		t.Fatalf("helper steering entries = %d, want 4", len(entries))
	}
	if got := wrapHookContext("hook context"); got != "<SYSTEM-REMINDER>hook context</SYSTEM-REMINDER>" {
		t.Fatalf("wrapped hook context = %q", got)
	}
	if msg := steeringMessageToLLM(entries[0]); msg.Role != llm.RoleUser {
		t.Fatalf("text steering role = %q, want user", msg.Role)
	}
	if s.hasPendingSteering() {
		t.Fatal("drained steering remains pending")
	}
}

func qlifImages(n int) []ImageAttachment {
	images := make([]ImageAttachment, n)
	for i := range images {
		images[i] = ImageAttachment{MediaType: "image/png", Name: fmt.Sprintf("image-%d", i), Data: []byte{byte(i + 1)}}
	}
	return images
}

func qlifQueueProvenance() *provenance.Causal {
	return &provenance.Causal{WatchKeys: []provenance.WatchKey{{WatchID: "queue-watch", WatchGeneration: "1"}}}
}

func qlifCheckDrainContext(t *testing.T, data []byte) {
	t.Helper()
	r := &seqReader{data: data}
	root, cancelRoot := context.WithCancel(context.Background())
	turn, cancelTurn := context.WithCancel(root)
	mode := r.intn(8)
	var nextCanceled context.CancelFunc
	marked := WithQueuedInputDrainOnInterruptHandler(turn, root, func(parent context.Context) (context.Context, context.CancelFunc) {
		if mode == 7 {
			return nil, func() {}
		}
		next, cancel := context.WithCancel(parent)
		nextCanceled = cancel
		return next, cancel
	})

	err := error(context.Canceled)
	want := true
	switch mode {
	case 0:
		marked = context.Background()
		want = false
	case 1:
		err = errors.New("ordinary failure")
		want = false
	case 2:
		err = context.DeadlineExceeded
		want = false
	case 3:
		err = llm.NewAbortError("sub-operation", context.Canceled)
		want = false
	case 4:
		cancelTurn()
		err = llm.NewAbortError("turn canceled", context.Canceled)
	case 5:
		cancelRoot()
		want = false
	case 6:
		cancelTurn()
	case 7:
		cancelTurn()
		want = false
	}
	got, ok := queuedInputDrainContext(marked, err)
	if ok != want {
		t.Fatalf("mode %d: queuedInputDrainContext ok=%v, want %v (err=%v)", mode, ok, want, err)
	}
	if ok && (got == nil || got.Err() != nil) {
		t.Fatalf("mode %d: drain context = %#v, err=%v", mode, got, got.Err())
	}
	if nextCanceled != nil {
		nextCanceled()
	}
	cancelTurn()
	cancelRoot()
}
