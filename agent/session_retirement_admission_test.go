package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/spf13/afero"
	"os"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/appwire"
)

func TestRetirementAcceptedStartBlocksBeforeRunnerWake(t *testing.T) {
	testRetirementPendingStart(t, false)
}

func TestRetirementClaimedStartBlocksBeforeTurn(t *testing.T) {
	testRetirementPendingStart(t, true)
}

// Removing pending/claimed durable input from the predicate would allow a
// process retirement to strand input before its runner has executed anything.
func testRetirementPendingStart(t *testing.T, claimFirst bool) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	p := appwire.TurnStartParams{ClientMutationID: "retirement-start-race", Input: []appwire.InputItem{{Type: "text", Text: "opaque-input"}}}
	response, err := root.AcceptClientMutationStart(p)
	if err != nil {
		t.Fatal(err)
	}
	if claimFirst {
		claimed, ok, err := root.claimClientMutationStart()
		if err != nil || !ok || claimed.StableTurnID != response.Turn.ID {
			t.Fatalf("claim: %#v %v %v", claimed, ok, err)
		}
	}
	claim, snapshot, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatal("retired accepted or claimed input before runner wake")
	}
	if !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "input" && b.SessionID == root.ID() }) {
		t.Fatalf("missing input blocker: %+v", snapshot)
	}
	replay, err := root.AcceptClientMutationStart(p)
	if err != nil {
		t.Fatal(err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || response.Turn.ID == "" || replay.Turn.ID != response.Turn.ID || response.Receipt.TurnID != response.Turn.ID || replay.Receipt.TurnID != response.Turn.ID || response.Receipt.ProjectionState != appwire.MutationProjectionPending || replay.Receipt.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("wrong acceptance/replay identity or receipt: %#v %#v", response, replay)
	}
	journal := root.clientMutations.snapshot()
	pending := journal.PendingExecutions[p.ClientMutationID]
	wantAccepted := uint64(0)
	if claimFirst {
		wantAccepted = 1
	}
	if pending.TurnID != response.Turn.ID || !reflect.DeepEqual(pending.Input, p.Input) || len(journal.PendingExecutions) != 1 || journal.AcceptedTurns != wantAccepted {
		t.Fatalf("replay duplicated or changed input: %#v", journal)
	}
}

func retirementRefusalRoot(t *testing.T) (*Session, *RetirementController) {
	t.Helper()
	root := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(root.Close)
	// Persist real rejected history so refusal must preserve an existing primary
	// journal, not merely leave an absent file absent.
	_, err := root.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "seed-rejected", Index: 0})
	if err == nil {
		t.Fatal("expected empty queue rejection")
	}
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, snapshot, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %+v", err, snapshot)
	}
	return root, c
}

func TestRetirementClaimFirstPreservesOriginalJournal(t *testing.T) {
	input := []appwire.InputItem{{Type: "text", Text: "opaque-input"}}
	cases := []struct {
		name   string
		effect func(*Session) error
	}{
		{"start", func(s *Session) error {
			_, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "refused-start", Input: input})
			return err
		}},
		{"queue", func(s *Session) error {
			_, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "refused-queue", Input: input})
			return err
		}},
		{"steer", func(s *Session) error {
			_, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "refused-steer", Input: input})
			return err
		}},
		{"drain", func(s *Session) error {
			_, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: "refused-drain", Input: input})
			return err
		}},
		{"promote", func(s *Session) error {
			_, err := s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "refused-promote"})
			return err
		}},
		{"cancel", func(s *Session) error {
			_, err := s.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "refused-cancel"})
			return err
		}},
		{"interrupt", func(s *Session) error {
			_, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{ClientMutationID: "refused-interrupt"}, func() { t.Error("refused interrupt invoked callback") })
			return err
		}},
		{"pre-turn-claim", func(s *Session) error {
			_, ok, err := s.claimClientMutationStart()
			if ok {
				t.Error("refused claim returned work")
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := retirementRefusalRoot(t)
			before := root.clientMutations.snapshot()
			path := clientMutationFilePath(root.clientMutations.stateDir, root.clientMutations.sessionID)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.effect(root); !errors.Is(err, ErrRetirementUnavailable) {
				t.Errorf("effect error = %v, want retirement unavailable", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Error("refused effect changed original journal bytes")
			}
			if !reflect.DeepEqual(before, root.clientMutations.snapshot()) {
				t.Error("refused effect changed journal snapshot")
			}
		})
	}
}

func TestRetirementEffectRefusalReportsError(t *testing.T) {
	root, _ := retirementRefusalRoot(t)
	effects, ok := any(root).(interface {
		SetReasoningEffort(string) error
		Steer(string) error
	})
	if !ok {
		t.Fatal("reasoning and steering effects do not report lifecycle errors")
	}
	beforeEffort := root.cfg.ReasoningEffort
	beforeQueue := root.SteeringQueueSnapshot()
	if err := effects.SetReasoningEffort("high"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("reasoning error: %v", err)
	}
	if err := effects.Steer("opaque-input"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("steer error: %v", err)
	}
	if root.cfg.ReasoningEffort != beforeEffort || !reflect.DeepEqual(beforeQueue, root.SteeringQueueSnapshot()) {
		t.Fatal("refused effect changed session")
	}
}

func TestRetirementAdmissionDirectEffects(t *testing.T) {
	cases := []struct {
		name   string
		effect func(*Session) error
	}{
		{"model", func(s *Session) error { return s.SetModel("gpt-5.2") }},
		{"vision", func(s *Session) error { return s.SetVisionModel("off") }},
		{"compact", func(s *Session) error { return s.Compact(context.Background()) }},
		{"goal", func(s *Session) error { _, err := s.SetGoal(context.Background(), "opaque-goal"); return err }},
		{"enqueue", func(s *Session) error { return s.Enqueue(context.Background(), "opaque-input") }},
		{"enqueue-images", func(s *Session) error { return s.EnqueueWithImages(context.Background(), "opaque-input", nil) }},
		{"drain", func(s *Session) error { return s.DrainAsSteer(context.Background()) }},
		{"drain-input", func(s *Session) error { return s.DrainAsSteerWithInput(context.Background(), "opaque-input", nil) }},
		{"promote", func(s *Session) error { return s.PromoteQueuedAsSteer(context.Background(), 0, "") }},
		{"cancel", func(s *Session) error { _, _, err := s.CancelQueued(context.Background(), 0, ""); return err }},
		{"turn", func(s *Session) error {
			_, err := s.ProcessInput(context.Background(), "opaque-input", nil)
			return err
		}},
		{"steer-kind", func(s *Session) error { return s.SteerKind("opaque-input", "") }},
		{"steer-task", func(s *Session) error { return s.SteerTaskCompletion("opaque-input", nil) }},
		{"steer-provenance", func(s *Session) error { return s.SteerWithProvenance("opaque-input", nil, "") }},
		{"steer-images", func(s *Session) error { return s.SteerWithImages("opaque-input", nil) }},
		{"steer-user", func(s *Session) error { return s.SteerFromUser("opaque-input") }},
		{"steer-user-images", func(s *Session) error { return s.SteerFromUserWithImages("opaque-input", nil) }},
		{"hook", func(s *Session) error { return s.deliverHookContext("opaque-input") }},
		{"fold", func(s *Session) error { return s.steerKindForFold("opaque-input", "", ungatedFoldRevision) }},
		{"task-tool-dependency", func(s *Session) error { return newToolDeps(s).sendTaskCompletionSteering("opaque-input", nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := retirementRefusalRoot(t)
			before := root.clientMutations.snapshot()
			if err := tc.effect(root); !errors.Is(err, ErrRetirementUnavailable) {
				t.Errorf("effect error = %v, want retirement unavailable", err)
			}
			if !reflect.DeepEqual(before, root.clientMutations.snapshot()) {
				t.Error("direct effect changed durable journal")
			}
		})
	}
}

func TestRetirementAdmissionNotificationStaysPending(t *testing.T) {
	root, _ := retirementRefusalRoot(t)
	if root.enqueueSystemNotification("opaque-notice") {
		t.Fatal("refused notification was acknowledged")
	}
	if len(root.SteeringQueueSnapshot()) != 0 {
		t.Fatal("refused notification enqueued")
	}
}

// The only substituted dependency is the OS filesystem. Every store serializer,
// validation, write, fsync, rename, projection and replay path remains real.
type retirementBarrierFS struct {
	afero.Fs
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}
type retirementBarrierFile struct {
	afero.File
	fs *retirementBarrierFS
}

func (f *retirementBarrierFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &retirementBarrierFile{File: file, fs: f}, nil
}
func (f *retirementBarrierFile) Write(data []byte) (int, error) {
	f.fs.once.Do(func() { close(f.fs.entered); <-f.fs.resume })
	return f.File.Write(data)
}

func TestRetirementAdmissionFilesystemBarrier(t *testing.T) {
	for _, name := range []string{"start", "queue", "steer", "drain", "promote", "cancel", "interrupt", "pre-turn-claim"} {
		t.Run(name, func(t *testing.T) {
			root := newQueuePersistTestSession(t, t.TempDir())
			defer root.Close()
			input := []appwire.InputItem{{Type: "text", Text: "opaque-input"}}
			if name == "pre-turn-claim" || name == "interrupt" {
				if _, err := root.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "seed-start", Input: input}); err != nil {
					t.Fatal(err)
				}
			}
			if name == "promote" || name == "cancel" {
				if _, err := root.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "seed-queue", Input: input}); err != nil {
					t.Fatal(err)
				}
			}
			if err := root.ensureClientMutationStore(); err != nil {
				t.Fatal(err)
			}
			c, err := NewRetirementController(0, clock.Real())
			if err != nil {
				t.Fatal(err)
			}
			if err := c.AttachRoot(root); err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			fs := &retirementBarrierFS{Fs: root.clientMutations.fs, entered: make(chan struct{}), resume: make(chan struct{})}
			resume := func() { releaseOnce.Do(func() { close(fs.resume) }) }
			defer resume()
			root.clientMutations.fs = fs
			effect := func() (any, error) {
				switch name {
				case "start":
					return root.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "barrier-start", Input: input})
				case "queue":
					return root.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "barrier-queue", Input: input})
				case "steer":
					return root.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "barrier-steer", Input: input})
				case "drain":
					return root.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: "barrier-drain", Input: input})
				case "promote":
					return root.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "barrier-promote"})
				case "cancel":
					return root.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "barrier-cancel"})
				case "interrupt":
					return root.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{ClientMutationID: "barrier-interrupt"}, nil)
				case "pre-turn-claim":
					item, ok, err := root.claimClientMutationStart()
					if !ok && err == nil {
						return item, errors.New("no start claimed")
					}
					return item, err
				default:
					panic("unknown test case")
				}
			}
			type result struct {
				value any
				err   error
			}
			done := make(chan result, 1)
			go func() { v, err := effect(); done <- result{v, err} }()
			select {
			case <-fs.entered:
			case r := <-done:
				t.Fatalf("effect missed write barrier: %v", r.err)
				// TRIPWIRE: fixture I/O/provider entry takes milliseconds; completion is the channel above, not this bound.
			case <-time.After(5 * time.Second):
				t.Fatal("write barrier was not reached")
			}
			// The claim must return before the filesystem is released, not wait for I/O.
			claimDone := make(chan error, 1)
			go func() {
				claim, snapshot, err := c.TryClaim(true)
				if err == nil && (claim != nil || !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "input" && b.SessionID == root.ID() })) {
					err = errors.New("write did not hold input admission")
				}
				claimDone <- err
			}()
			select {
			case err := <-claimDone:
				if err != nil {
					t.Fatal(err)
				}
				// TRIPWIRE: fixture I/O/provider entry takes milliseconds; completion is the channel above, not this bound.
			case <-time.After(5 * time.Second):
				t.Fatal("claim waited on mutation journal I/O")
			}
			resume()
			first := <-done
			if first.err != nil {
				t.Fatal(first.err)
			}
			store := root.clientMutations
			// Cold-load the primary file through the production loader, not a snapshot
			// copied from the writer. Restore normalization is deliberately retained.
			cold, err := newClientMutationStore(store.stateDir, store.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			before := store.snapshot()
			if name == "pre-turn-claim" {
				pending := before.PendingExecutions["seed-start"]
				if pending.ExecutionState != "claimed" || pending.TurnID == "" || !reflect.DeepEqual(pending.Input, input) {
					t.Fatalf("claim lost durable payload: %#v", pending)
				}
				if cold.snapshot().PendingExecutions["seed-start"].TurnID != pending.TurnID {
					t.Fatal("cold load changed claimed identity")
				}
				root.clientMutations = cold
				replay, err := root.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "seed-start", Input: input})
				if err != nil || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || replay.Receipt.ProjectionState != appwire.MutationProjectionPending || replay.Turn.ID != pending.TurnID || replay.Receipt.TurnID != pending.TurnID {
					t.Fatalf("cold pre-turn retry: %#v %v", replay, err)
				}
				if len(cold.snapshot().Journal) != len(before.Journal) {
					t.Fatal("cold pre-turn retry duplicated intent")
				}
				return
			}
			root.clientMutations = cold
			replay, replayErr := effect()
			if replayErr != nil {
				t.Fatal(replayErr)
			}
			var accepted, replayed struct{ Receipt appwire.MutationReceipt }
			b, err := json.Marshal(first.value)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &accepted); err != nil {
				t.Fatal(err)
			}
			b, err = json.Marshal(replay)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &replayed); err != nil {
				t.Fatal(err)
			}
			if accepted.Receipt.Disposition != appwire.MutationDispositionApplied || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed || accepted.Receipt.TurnID != replayed.Receipt.TurnID || !reflect.DeepEqual(accepted.Receipt.QueueEntryIDs, replayed.Receipt.QueueEntryIDs) {
				t.Fatalf("cold retry changed identity: %#v %#v", accepted, replayed)
			}
			if len(before.Journal) != len(cold.snapshot().Journal) {
				t.Fatal("retry duplicated journal intent")
			}
		})
	}
}

func TestRetirementStartReplayExecutesOnce(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	defer unblock()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { close(entered); <-resume; return finalResponse("opaque-result") }}}
	root.client.Register(adapter)
	root.profile = withTestSessionNamer(root.client, root.profile)
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	params := appwire.TurnStartParams{ClientMutationID: "replay-executes-once", Input: []appwire.InputItem{{Type: "text", Text: "opaque-input"}}}
	accepted, err := root.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := root.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Receipt.Disposition != appwire.MutationDispositionApplied || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || accepted.Receipt.ProjectionState != appwire.MutationProjectionPending || replay.Receipt.ProjectionState != appwire.MutationProjectionPending || accepted.Turn.ID == "" || replay.Turn.ID != accepted.Turn.ID {
		t.Fatalf("pending acceptance/replay: %#v %#v", accepted, replay)
	}
	done := make(chan error, 1)
	go func() {
		_, ok, err := root.ProcessClientMutationStart(context.Background(), nil)
		if !ok && err == nil {
			err = errors.New("runner did not claim input")
		}
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("runner missed provider: %v", err)
	// TRIPWIRE: fixture I/O/provider entry takes milliseconds; completion is the channel above, not this bound.
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	if claim, _, err := c.TryClaim(true); err != nil || claim != nil {
		t.Fatalf("active turn claimed: %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	reflected, err := root.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatal(err)
	}
	if reflected.Receipt.Disposition != appwire.MutationDispositionReplayed || reflected.Receipt.ProjectionState != appwire.MutationProjectionReflected || reflected.Turn.ID != accepted.Turn.ID || reflected.Receipt.TurnID != accepted.Turn.ID {
		t.Fatalf("settled replay: %#v", reflected)
	}
	if _, ok, err := root.ProcessClientMutationStart(context.Background(), nil); err != nil || ok {
		t.Fatalf("replay reran: %v %v", ok, err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider executions = %d, want one", got)
	}
	var users []schema.Turn
	root.mu.Lock()
	for _, turn := range root.history {
		if turn.Kind == schema.TurnUserInput {
			users = append(users, turn)
		}
	}
	root.mu.Unlock()
	if len(users) != 1 || users[0].ClientMutationID != params.ClientMutationID || users[0].StableTurnID != accepted.Turn.ID || users[0].Message.Text() != params.Input[0].Text {
		t.Fatalf("user input identity: %#v", users)
	}
	// Check the original primary transcript independently, not a reconstruction
	// from the in-memory history or the acceptance response.
	primary, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(primary), []byte("\n"))
	header, err := transcript.DecodeHeader(lines[0])
	if err != nil || header.SessionID != root.ID() {
		t.Fatalf("primary header: %#v %v", header, err)
	}
	var primaryUsers []schema.Turn
	for _, line := range lines[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Turn.Kind == schema.TurnUserInput {
			primaryUsers = append(primaryUsers, entry.Turn)
		}
	}
	if len(primaryUsers) != 1 || primaryUsers[0].ClientMutationID != params.ClientMutationID || primaryUsers[0].StableTurnID != accepted.Turn.ID || primaryUsers[0].Message.Text() != params.Input[0].Text {
		t.Fatalf("primary transcript user input identity: %#v", primaryUsers)
	}
	settled := root.clientMutations.snapshot()
	if len(settled.PendingExecutions) != 0 || settled.AcceptedTurns != 1 || len(settled.Journal) != 1 {
		t.Fatalf("settlement did not retain one executed intent: %#v", settled)
	}
	if claim, snapshot, err := c.TryClaim(true); err != nil || claim == nil {
		t.Fatalf("terminal history blocked retirement: %v %+v", err, snapshot)
	}
}

func TestRetirementClaimFirstPrivateInputHandoffs(t *testing.T) {
	for _, name := range []string{"queue-claim", "steering-carrier-claim", "turn-owned-steer", "durable-caller-steer"} {
		t.Run(name, func(t *testing.T) {
			root, _ := retirementRefusalRoot(t)
			before := root.clientMutations.snapshot()
			path := clientMutationFilePath(root.clientMutations.stateDir, root.clientMutations.sessionID)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			drainRetirementTestEvents(root)
			switch name {
			case "queue-claim":
				if got := root.popQueueHead(); got.ClientMutationID != "" {
					t.Fatal("refused queue claim returned input")
				}
			case "steering-carrier-claim":
				if _, ok := root.claimSteeringCarrierTurn(); ok {
					t.Fatal("refused carrier claimed")
				}
			case "turn-owned-steer":
				if root.trySteerTurnOwnedMessage(steeringMessage{Text: "opaque-input"}, &struct{ _ byte }{}) {
					t.Fatal("refused turn-owned steering appended")
				}
			case "durable-caller-steer":
				if err := root.enqueueDelegateCallerSteeringDurably("opaque-input", nil); !errors.Is(err, ErrRetirementUnavailable) {
					t.Fatalf("durable caller steering = %v", err)
				}
			}
			if name == "queue-claim" || name == "steering-carrier-claim" {
				if !slices.Contains(drainRetirementTestEvents(root), events.EventWarning) {
					t.Error("refused private claim did not report admission failure")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) || !reflect.DeepEqual(before, root.clientMutations.snapshot()) || len(root.SteeringQueueSnapshot()) != 0 {
				t.Fatal("refused private handoff changed input")
			}
		})
	}
}

func drainRetirementTestEvents(root *Session) []events.EventKind {
	var kinds []events.EventKind
	for {
		select {
		case ev := <-root.Events():
			kinds = append(kinds, ev.Kind)
		default:
			return kinds
		}
	}
}

func TestRetirementAdmissionFollowUp(t *testing.T) {
	root, _ := retirementRefusalRoot(t)
	enqueue, ok := any(root).(interface{ FollowUp(string) error })
	if !ok {
		t.Fatal("FollowUp cannot report retirement refusal")
	}
	before := append([]string(nil), root.followups...)
	if err := enqueue.FollowUp("opaque-followup"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("FollowUp error: %v", err)
	}
	if !reflect.DeepEqual(before, root.followups) {
		t.Fatal("refused followup changed queue")
	}
}

func TestRetirementAcceptedFollowUpBlocks(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	if err := root.FollowUp("opaque-followup"); err != nil {
		t.Fatal(err)
	}
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, snapshot, err := c.TryClaim(true)
	if err != nil || claim != nil || !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "input" && b.SessionID == root.ID() }) {
		t.Fatalf("accepted followup lost blocker: %v %+v", err, snapshot)
	}
}

func TestRetirementReasoningPersistenceError(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	root.cfg.testOnly.metaFS = afero.NewReadOnlyFs(afero.NewOsFs())
	if err := root.SetReasoningEffort("high"); err == nil {
		t.Fatal("reasoning setter hid metadata persistence failure")
	}
	if root.ReasoningEffort() != "high" {
		t.Fatal("setter must report save failure after mutation")
	}
}
