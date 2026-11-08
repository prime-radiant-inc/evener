package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestPendingAskRowClampsWholeSet(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "root")
	state := &appwire.PendingAskState{Version: 1, InstanceID: "root", BootGeneration: "1", Revision: 1, CalllessCount: 2}
	for i := 0; i < appwire.MaxPendingAskComposites+1; i++ {
		state.Calls = append(state.Calls, appwire.PendingAskCall{TurnID: "turn", CallID: fmt.Sprint(i)})
	}
	state.CompositeTotal = len(state.Calls)
	s.SetThreadEnvelopeSource(&pendingAskEnvelopeSource{sample: func() *appwire.PendingAskState { return state }})
	s.RefreshThreadEnvelope()
	row := readThreadOverWire(t, s, "local:root")
	got := row.Evener.PendingAsk
	if len(got.Calls) != appwire.MaxPendingAskComposites || got.CompositeTotal != len(state.Calls) || !got.CallsTruncated || got.CalllessCount != 2 {
		t.Fatalf("projected pending set=%+v", got)
	}
	if len(state.Calls) != appwire.MaxPendingAskComposites+1 || state.CallsTruncated {
		t.Fatal("projection mutated the whole-set owner")
	}
	if !row.Evener.Capabilities.AskFence {
		t.Fatal("daemon did not advertise ask fence and pager")
	}
}

func TestPendingAskPageWireRequest(t *testing.T) {
	t.Parallel()
	s := NewServer(ServerConfig{})
	s.SetAppIdentity("local", "root")
	state := &appwire.PendingAskState{Version: 1, InstanceID: "root", BootGeneration: "1", Revision: 0, Calls: []appwire.PendingAskCall{{TurnID: "turn", CallID: "a"}, {TurnID: "turn", CallID: "b"}}, CompositeTotal: 2, CalllessCount: 3}
	s.SetThreadEnvelopeSource(&pendingAskEnvelopeSource{sample: func() *appwire.PendingAskState { return state }})
	s.RefreshThreadEnvelope()
	conn := s.AppServer().NewConnection("pending-ask-page")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("initialize kind=%v", init.Kind())
	}
	message := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, json.RawMessage(`{"ref":"local:root","expectedAskRevision":0,"expectedAskBootGeneration":"1","offset":1,"limit":1}`)))
	if message.Kind() != appwire.MessageResponse {
		t.Fatalf("page response kind=%v", message.Kind())
	}
	response, ok := message.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("page response type=%T", message.Response.Result)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var pageFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["pendingAskPage"], &pageFields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"composites", "total", "calllessCount"} {
		if _, present := pageFields[key]; !present {
			t.Fatalf("page lacks wire field %q", key)
		}
	}
	if _, present := pageFields["nextOffset"]; present {
		t.Fatal("completed page encoded a nextOffset field")
	}
	var wire struct {
		PendingAskPage *struct {
			Composites    []appwire.PendingAskCall
			Total         int
			NextOffset    *int
			CalllessCount int
		}
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.PendingAskPage == nil {
		t.Fatal("page request returned no pendingAskPage")
	}
	got := wire.PendingAskPage
	if len(got.Composites) != 1 || got.Composites[0].CallID != "b" || got.Total != 2 || got.NextOffset != nil || got.CalllessCount != 3 {
		t.Fatalf("page=%+v", got)
	}
}
