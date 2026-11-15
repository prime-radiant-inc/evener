package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

type frontierEnvelopeSource struct {
	stubThreadEnvelopeSource
	frontier string
}

func (s *frontierEnvelopeSource) LatestExecutionTurnID() string { return s.frontier }

func TestThreadFrontierRefreshesOnRecordedExecutionRows(t *testing.T) {
	t.Parallel()
	for _, event := range []events.EventData{
		events.UserInputData{}, events.SteeringInjectedData{}, events.AssistantTextEndData{}, events.GoalContinuationData{}, events.TurnEndedData{}, events.ExecutionFirstRecordedData{},
	} {
		t.Run(string(events.KindOf(event)), func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			t.Cleanup(srv.Close)
			srv.SetAppIdentity("local", "root")
			source := &frontierEnvelopeSource{frontier: "turn_m201"}
			srv.SetThreadEnvelopeSource(source)
			srv.RefreshThreadEnvelope()
			source.frontier = "turn_m202"
			ev := events.New(event)
			ev.SessionID = "root"
			feedBridge(srv, ev)
			read, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:root"})
			if err != nil {
				t.Fatal(err)
			}
			if got := read.Thread.Evener.LatestExecutionTurnID; got != source.frontier {
				t.Fatalf("metadata frontier = %q, recorded source = %q", got, source.frontier)
			}
		})
	}
}
