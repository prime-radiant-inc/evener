package server

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestPendingAskPageFencesAndIteration(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "root")
	state := &appwire.PendingAskState{Version: 1, InstanceID: "root", BootGeneration: "2", Revision: 3, CalllessCount: 2, CompositeTotal: 3, Calls: []appwire.PendingAskCall{{TurnID: "t1", CallID: "a"}, {TurnID: "t2", CallID: "b"}, {TurnID: "t2", CallID: "c"}}}
	s.SetThreadEnvelopeSource(&pendingAskEnvelopeSource{sample: func() *appwire.PendingAskState { return state }})
	s.RefreshThreadEnvelope()
	rev := uint64(3)
	base := appwire.ThreadReadParams{Ref: "local:root", ExpectedAskRevision: &rev, ExpectedAskBootGeneration: "2"}
	var seen []appwire.PendingAskCall
	offset, limit := 0, 1
	for {
		p := base
		p.Offset = &offset
		p.Limit = &limit
		response, err := s.handleAppThreadRead(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		page := response.PendingAskPage
		if page == nil {
			t.Fatal("page absent")
		}
		if page.Total != 3 || page.CalllessCount != 2 {
			t.Fatalf("page counts=%+v", page)
		}
		seen = append(seen, page.Composites...)
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			t.Fatal("page did not advance")
		}
		offset = *page.NextOffset
	}
	if !reflect.DeepEqual(seen, state.Calls) {
		t.Fatalf("iteration=%+v", seen)
	}
	for _, tc := range []struct {
		name                string
		offset, limit, want int
	}{
		{"negative offset", -4, 1, 1}, {"negative limit", 0, -1, 3}, {"zero limit", 0, 0, 3}, {"huge limit", 0, math.MaxInt, 3}, {"at end", 3, 1, 0}, {"past end", math.MaxInt, math.MaxInt, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Offset = &tc.offset
			p.Limit = &tc.limit
			response, err := s.handleAppThreadRead(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			page := response.PendingAskPage
			if page == nil || len(page.Composites) != tc.want {
				t.Fatalf("page=%+v", page)
			}
			if tc.want == 0 && (page.Composites == nil || page.NextOffset != nil) {
				t.Fatalf("noncanonical end=%+v", page)
			}
		})
	}
	for _, change := range []string{"append", "restart", "empty"} {
		t.Run(change, func(t *testing.T) {
			next := *state
			switch change {
			case "append":
				next.Revision++
				next.Calls = append(append([]appwire.PendingAskCall{}, state.Calls...), appwire.PendingAskCall{TurnID: "t3", CallID: "d"})
			case "restart":
				next.BootGeneration = "3"
			case "empty":
				next.Calls = nil
				next.CalllessCount = 0
			}
			current := state
			state = &next
			defer func() { state = current }()
			_, err := s.handleAppThreadRead(context.Background(), base)
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("missing typed mismatch: %v", err)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok || data.EvenerErrorInfo != appwire.ErrorAskFenceMismatch {
				t.Fatalf("wrong mismatch=%+v", wire)
			}
		})
	}
	for _, ref := range []string{"local:missing", "remote:root", "not-a-ref"} {
		t.Run(ref, func(t *testing.T) {
			p := base
			p.Ref = ref
			p.ThreadID = "root"
			if _, err := s.handleAppThreadRead(context.Background(), p); err == nil {
				t.Fatal("page silently served another target")
			}
		})
	}
}
