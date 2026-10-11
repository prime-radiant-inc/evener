package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// stoppingAppSource is a scripted source that can stop a subagent, recording
// what it was asked.
type stoppingAppSource struct {
	*scriptedAppSource
	got     []appwire.DelegateStopParams
	outcome appwire.DelegateStopOutcome
}

func (s *stoppingAppSource) StopDelegate(_ context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
	s.got = append(s.got, params)
	return appwire.DelegateStopResponse{Outcome: s.outcome}, nil
}

// The hub routes a stop to the source that owns the root session, and hands
// back its outcome (S6).
func TestHubRoutesDelegateStopToTheRootsSource(t *testing.T) {
	source := &stoppingAppSource{scriptedAppSource: &scriptedAppSource{id: "local"}, outcome: appwire.DelegateStopStopping}
	registry := appsource.NewRegistry()
	registry.Add(source)
	server := newHubAppServer(hubcore.WebConfig{}, registry)

	params := appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_1"}
	got, err := exactDispatch(t.Context(), t, server, appwire.MethodEvenerDelegateStop, params)
	if err != nil {
		t.Fatalf("evener/delegate/stop: %v", err)
	}
	if resp, ok := got.(appwire.DelegateStopResponse); !ok || resp.Outcome != appwire.DelegateStopStopping {
		t.Fatalf("response = %#v, want stopping", got)
	}
	if len(source.got) != 1 || source.got[0] != params {
		t.Fatalf("source received %+v, want exactly %+v", source.got, params)
	}
}

// A source that cannot stop a subagent answers Unavailable, which is the
// phone's cue to keep asking the coordinator instead.
func TestHubDelegateStopIsUnavailableWhereTheSourceCannotStop(t *testing.T) {
	registry := appsource.NewRegistry()
	registry.Add(&scriptedAppSource{id: "local"})
	server := newHubAppServer(hubcore.WebConfig{}, registry)

	_, err := exactDispatch(t.Context(), t, server, appwire.MethodEvenerDelegateStop, appwire.DelegateStopParams{Ref: "local:root", DelegateID: "dlg_1"})
	if wire := appserver.WireError(err); wire.Code != appwire.CodeUnavailable {
		t.Fatalf("stop through a source without it = %v, want unavailable", err)
	}
}
