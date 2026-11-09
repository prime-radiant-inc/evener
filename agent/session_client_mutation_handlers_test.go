package agent

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

// clientMutationHandlerCase drives one retry-safe input handler through the
// shared accept contract: fence refusal, replay, and invalid input.
type clientMutationHandlerCase struct {
	name string
	// seed puts one queued entry in the durable queue for handlers that act on
	// the queue.
	seed func(t *testing.T, sess *Session)
	// call invokes the handler with a stable id and returns its receipt.
	call func(sess *Session, id string) (appwire.MutationReceipt, error)
	// callInvalid invokes the handler with an input item the daemon boundary
	// refuses; nil for handlers that take no input.
	callInvalid func(sess *Session, id string) error
	// fenceExempt marks a handler that stays available under a pending
	// interrupt.
	fenceExempt bool
	// mutatesQueue marks handlers whose effect changes the queue revision.
	mutatesQueue bool
}

func seedQueuedEntry(t *testing.T, sess *Session) {
	t.Helper()
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "seed",
		Input:            []appwire.InputItem{{Type: "text", Text: "seed"}},
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
}

func clientMutationHandlerCases() []clientMutationHandlerCase {
	text := []appwire.InputItem{{Type: "text", Text: "hello"}}
	bad := []appwire.InputItem{{Text: "no type"}}
	return []clientMutationHandlerCase{
		{
			name: "queue",
			seed: func(t *testing.T, sess *Session) { setTestClientMutationActiveTurn(t, sess, "turn-1") },
			call: func(sess *Session, id string) (appwire.MutationReceipt, error) {
				r, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: id, Input: text})
				return r.Receipt, err
			},
			callInvalid: func(sess *Session, id string) error {
				_, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: id, Input: bad})
				return err
			},
			mutatesQueue: true,
		},
		{
			name: "steer",
			seed: func(t *testing.T, sess *Session) {},
			call: func(sess *Session, id string) (appwire.MutationReceipt, error) {
				r, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: id, Input: text})
				return r.Receipt, err
			},
			callInvalid: func(sess *Session, id string) error {
				_, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: id, Input: bad})
				return err
			},
		},
		{
			name: "drain",
			seed: seedQueuedEntry,
			call: func(sess *Session, id string) (appwire.MutationReceipt, error) {
				r, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: id, ExpectedQueueRevision: 1})
				return r.Receipt, err
			},
			callInvalid: func(sess *Session, id string) error {
				_, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: id, ExpectedQueueRevision: 1, Input: bad})
				return err
			},
			mutatesQueue: true,
		},
		{
			name: "promote",
			seed: seedQueuedEntry,
			call: func(sess *Session, id string) (appwire.MutationReceipt, error) {
				r, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: id, Index: 0})
				return r.Receipt, err
			},
			mutatesQueue: true,
		},
		{
			name: "cancel",
			seed: seedQueuedEntry,
			call: func(sess *Session, id string) (appwire.MutationReceipt, error) {
				r, err := sess.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: id, Index: 0})
				return r.Receipt, err
			},
			fenceExempt:  true,
			mutatesQueue: true,
		},
		{
			name: "start",
			seed: func(t *testing.T, sess *Session) {},
			call: func(sess *Session, id string) (appwire.MutationReceipt, error) {
				r, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, Input: text})
				return r.Receipt, err
			},
			callInvalid: func(sess *Session, id string) error {
				_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, Input: bad})
				return err
			},
		},
	}
}

func wireCode(t *testing.T, err error) int {
	t.Helper()
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want an appwire error", err)
	}
	return wireErr.Code
}

func setInterruptFence(t *testing.T, sess *Session, on bool) {
	t.Helper()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if on {
			snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "stop"}
		} else {
			snapshot.InterruptFence = nil
		}
		return nil
	}); err != nil {
		t.Fatalf("set interrupt fence: %v", err)
	}
}

func TestClientMutationHandlers_ReplayReturnsTheOriginalReceiptWithoutRepeatingTheEffect(t *testing.T) {
	t.Parallel()
	for _, tc := range clientMutationHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newTestSession(t)
			tc.seed(t, sess)

			first, err := tc.call(sess, "m-replay")
			if err != nil {
				t.Fatalf("first call: %v", err)
			}
			afterFirst := sess.clientMutations.snapshot()
			replayed, err := tc.call(sess, "m-replay")
			if err != nil {
				t.Fatalf("replayed call: %v", err)
			}
			if first.Disposition != appwire.MutationDispositionApplied {
				t.Fatalf("first disposition = %q, want applied", first.Disposition)
			}
			if replayed.Disposition != appwire.MutationDispositionReplayed {
				t.Fatalf("replay disposition = %q, want replayed", replayed.Disposition)
			}
			if replayed.TurnID != first.TurnID || !reflect.DeepEqual(replayed.QueueEntryIDs, first.QueueEntryIDs) {
				t.Fatalf("replay receipt %+v does not carry the original identity %+v", replayed, first)
			}
			afterReplay := sess.clientMutations.snapshot()
			if afterReplay.QueueRevision != afterFirst.QueueRevision || len(afterReplay.InputQueue) != len(afterFirst.InputQueue) {
				t.Fatalf("replay repeated the effect: revision %d -> %d", afterFirst.QueueRevision, afterReplay.QueueRevision)
			}
		})
	}
}

func TestClientMutationHandlers_PendingInterruptRefusesExceptCancel(t *testing.T) {
	t.Parallel()
	for _, tc := range clientMutationHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newTestSession(t)
			tc.seed(t, sess)
			setInterruptFence(t, sess, true)
			before := sess.clientMutations.snapshot()

			receipt, err := tc.call(sess, "m-fenced")
			if tc.fenceExempt {
				if err != nil {
					t.Fatalf("call under a pending interrupt: %v", err)
				}
				if receipt.Disposition != appwire.MutationDispositionApplied {
					t.Fatalf("disposition = %q, want applied", receipt.Disposition)
				}
				return
			}
			if code := wireCode(t, err); code != appwire.CodeConflict {
				t.Fatalf("refusal code = %d, want conflict", code)
			}
			after := sess.clientMutations.snapshot()
			if after.QueueRevision != before.QueueRevision || len(after.InputQueue) != len(before.InputQueue) {
				t.Fatalf("a refused call changed the queue: revision %d -> %d", before.QueueRevision, after.QueueRevision)
			}

			// The refusal is the journaled answer for this id: the same retry
			// replays it even after the fence clears.
			setInterruptFence(t, sess, false)
			if _, err := tc.call(sess, "m-fenced"); wireCode(t, err) != appwire.CodeConflict {
				t.Fatalf("retry after the fence cleared: error = %v, want the journaled conflict", err)
			}
		})
	}
}

func TestClientMutationHandlers_InvalidInputIsRefusedBeforeAnythingIsJournaled(t *testing.T) {
	t.Parallel()
	for _, tc := range clientMutationHandlerCases() {
		if tc.callInvalid == nil {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newTestSession(t)
			tc.seed(t, sess)
			before := sess.clientMutations.snapshot()

			err := tc.callInvalid(sess, "m-invalid")
			if code := wireCode(t, err); code != appwire.CodeInvalidParams {
				t.Fatalf("code = %d, want invalid params", code)
			}
			if _, journaled := sess.clientMutations.snapshot().Journal["m-invalid"]; journaled {
				t.Fatal("an input the daemon boundary refuses was journaled")
			}
			if after := sess.clientMutations.snapshot(); after.QueueRevision != before.QueueRevision {
				t.Fatalf("queue revision %d -> %d", before.QueueRevision, after.QueueRevision)
			}
		})
	}
}

func TestClientMutationHandlers_EmptyInputIsAJournaledRejection(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	setTestClientMutationActiveTurn(t, sess, "turn-1")
	for name, call := range map[string]func(id string) error{
		"queue": func(id string) error {
			_, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: id})
			return err
		},
		"steer": func(id string) error {
			_, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: id})
			return err
		},
		"start": func(id string) error {
			_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id})
			return err
		},
	} {
		id := "m-empty-" + name
		if code := wireCode(t, call(id)); code != appwire.CodeInvalidParams {
			t.Fatalf("%s: code = %d, want invalid params", name, code)
		}
		record, journaled := sess.clientMutations.snapshot().Journal[id]
		if !journaled || record.OperationState != clientMutationOperationRejected {
			t.Fatalf("%s: record = %+v (journaled %v), want a journaled rejection", name, record, journaled)
		}
		if code := wireCode(t, call(id)); code != appwire.CodeInvalidParams {
			t.Fatalf("%s: replayed rejection code = %d, want invalid params", name, code)
		}
	}
}
