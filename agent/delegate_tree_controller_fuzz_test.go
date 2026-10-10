//go:build evenerfuzz

package agent

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func FuzzDelegateControllerTransitions(f *testing.F) {
	f.Add([]byte{0, 2, 3, 4, 5, 6, 2, 3, 4, 5})
	f.Add([]byte{0, 1, 0, 2, 3, 4, 5, 8, 9})
	f.Add([]byte{0, 2, 3, 4, 5, 7, 2, 5})
	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 64 {
			program = program[:64]
		}
		c, _ := newDelegateControllerTestHarness(t, 3, 2)
		seenGeneration := make(map[string]uint64)
		closed := make(map[string]bool)
		runtimes := make(map[string]*Session)

		for _, operation := range program {
			switch operation % 10 {
			case 0:
				_, _ = c.ReserveCreate(rootDelegateActor("root-session"), delegateControllerCreateDescriptor())
			case 1:
				if reservation := firstDelegateControllerReservation(c); reservation != nil {
					_ = c.AbortStart(reservation)
				}
			case 2:
				if reservation := firstDelegateControllerReservation(c); reservation != nil {
					_, _ = c.CommitStart(reservation)
				}
			case 3:
				if lease, ok := firstDelegateControllerBinding(c, false); ok {
					runtime := &Session{}
					if c.AttachRuntime(lease, runtime) == nil {
						runtimes[lease.delegateID] = runtime
					}
				}
			case 4:
				if lease, ok := firstDelegateControllerBinding(c, false); ok {
					_, _ = c.AdmitStartInput(lease, func() error { return nil })
				}
			case 5:
				if lease, ok := firstDelegateControllerBinding(c, true); ok {
					_, _ = c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "fuzz_finish"})
				}
			case 6:
				if id := firstDelegateControllerIdle(c, false); id != "" {
					_, _ = c.ReserveStart(rootDelegateActor("root-session"), id)
				}
			case 7:
				if id := firstDelegateControllerIdle(c, false); id != "" && runtimes[id] != nil {
					_, _ = c.ReserveAttention(runtimes[id], "attention-fuzz")
				}
			case 8:
				if id := firstDelegateControllerIdle(c, false); id != "" {
					c.mu.Lock()
					_, _ = c.appendLocked(delegatestore.Event{
						Kind:               delegatestore.EventDelegateResumabilityClosed,
						DelegateID:         id,
						ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "fuzz_closed"},
					})
					c.mu.Unlock()
				}
			case 9:
				if id := firstDelegateControllerIdle(c, true); id != "" {
					if _, err := c.ReserveStart(rootDelegateActor("root-session"), id); err == nil {
						t.Fatalf("closed delegate %s reopened", id)
					}
				}
			}
			assertDelegateControllerFuzzInvariants(t, c, seenGeneration, closed)
		}
	})
}

func firstDelegateControllerReservation(c *delegateTreeController) *delegateStartReservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	tokens := make([]uint64, 0, len(c.reservations))
	for token := range c.reservations {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
	if len(tokens) == 0 {
		return nil
	}
	return c.reservations[tokens[0]].receipt
}

func firstDelegateControllerBinding(c *delegateTreeController, requireReady bool) (delegateLease, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := delegateControllerSortedIDs(c.durable)
	for _, id := range ids {
		live := c.live[id]
		if live == nil || live.binding == nil || requireReady && !live.binding.ready {
			continue
		}
		return live.binding.lease, true
	}
	return delegateLease{}, false
}

func firstDelegateControllerIdle(c *delegateTreeController, closed bool) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range delegateControllerSortedIDs(c.durable) {
		aggregate := c.durable[id]
		if closed && aggregate.Phase == delegatestore.PhaseClosed && !aggregate.Resumable {
			return id
		}
		if !closed && aggregate.Phase == delegatestore.PhaseIdle && aggregate.Resumable {
			return id
		}
	}
	return ""
}

func delegateControllerSortedIDs(state delegatestore.State) []string {
	ids := make([]string, 0, len(state))
	for id := range state {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func assertDelegateControllerFuzzInvariants(t *testing.T, c *delegateTreeController, seenGeneration map[string]uint64, closed map[string]bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()

	wantTurns, wantDrives := 0, 0
	for _, reservation := range c.reservations {
		if reservation.capacityKind == delegateDriveCapacity {
			wantDrives++
		} else {
			wantTurns++
		}
	}
	for id, aggregate := range c.durable {
		if aggregate.Generation < seenGeneration[id] {
			t.Fatalf("delegate %s generation regressed from %d to %d", id, seenGeneration[id], aggregate.Generation)
		}
		seenGeneration[id] = aggregate.Generation
		if closed[id] && aggregate.Resumable {
			t.Fatalf("delegate %s reopened resumability", id)
		}
		if !aggregate.Resumable {
			closed[id] = true
		}
		live := c.live[id]
		if live != nil && live.binding != nil {
			if !aggregate.CurrentRunOpen || live.binding.lease.delegateID != id || live.binding.lease.generation != aggregate.Generation {
				t.Fatalf("delegate %s binding %#v does not match durable aggregate %#v", id, live.binding, aggregate)
			}
			if aggregate.Trigger == delegatestore.TriggerAttention {
				wantDrives++
			} else {
				wantTurns++
			}
		}
		for _, delivery := range aggregate.PendingDeliveries {
			if err := pendingDeliveryIDShapeError(id, delivery); err != nil {
				t.Fatalf("delegate %s used non-deterministic durable correlation: %v", id, err)
			}
		}
	}
	if c.turnsInUse != wantTurns || c.drivesInUse != wantDrives {
		t.Fatalf("capacity = (%d,%d), reservations+bindings = (%d,%d)", c.turnsInUse, c.drivesInUse, wantTurns, wantDrives)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load persisted events: %v", err)
	}
	folded, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("Fold persisted events: %v", err)
	}
	if !reflect.DeepEqual(folded, c.durable) {
		t.Fatalf("persisted fold differs from c.durable:\n got %#v\nwant %#v", folded, c.durable)
	}
}

// pendingDeliveryIDShapeError reports a pending delivery whose id is not the
// deterministic one its packet kind names: <id>/delivery/<generation> for a
// generation's report, <id>/update/<journal seq> for an update.
func pendingDeliveryIDShapeError(delegateID string, delivery delegatestore.PendingDelivery) error {
	if delivery.Packet.Kind == delegatestore.PacketUpdate {
		seqText, found := strings.CutPrefix(delivery.DeliveryID, delegateID+"/update/")
		seq, err := strconv.ParseUint(seqText, 10, 64)
		if !found || err != nil || seq == 0 || delegatestore.UpdateDeliveryID(delegateID, seq) != delivery.DeliveryID {
			return fmt.Errorf("update delivery ID %q, want %s/update/<seq>", delivery.DeliveryID, delegateID)
		}
		return nil
	}
	if want := delegateDeliveryID(delegateID, delivery.Generation); delivery.DeliveryID != want {
		return fmt.Errorf("delivery ID %q, want %q", delivery.DeliveryID, want)
	}
	return nil
}

func TestPendingDeliveryIDShapeError(t *testing.T) {
	report := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported}
	update := delegatestore.TerminalPacket{Kind: delegatestore.PacketUpdate}
	for _, tc := range []struct {
		id     string
		packet delegatestore.TerminalPacket
		ok     bool
	}{
		{"dlg_a/delivery/2", report, true},
		{"dlg_a/delivery/1", report, false},
		{"dlg_a/update/7", report, false},
		{"dlg_a/update/7", update, true},
		{"dlg_a/update/0", update, false},
		{"dlg_a/update/07", update, false},
		{"dlg_b/update/7", update, false},
		{"dlg_a/delivery/2", update, false},
	} {
		err := pendingDeliveryIDShapeError("dlg_a", delegatestore.PendingDelivery{DeliveryID: tc.id, Generation: 2, Packet: tc.packet})
		if (err == nil) != tc.ok {
			t.Errorf("%s (%s): err = %v, want ok=%v", tc.id, tc.packet.Kind, err, tc.ok)
		}
	}
}
