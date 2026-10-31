package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// refusingStateClient implements SessionHubClient with a State call that always
// fails. A session started as a Manager must never depend on it: enable comes
// from the session's own start-time config, so hub tools are present with no
// hub state read at all.
type refusingStateClient struct{ invoked bool }

func (c *refusingStateClient) State(context.Context) (appwire.ManagerView, error) {
	c.invoked = true
	return appwire.ManagerView{}, errors.New("no state read may gate Manager tools")
}

func (*refusingStateClient) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, errors.New("unit fixture does not implement hub operations")
}

func (*refusingStateClient) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, errors.New("unit fixture does not implement observations")
}

// TestManagerToolsComeFromSessionConfigNotHubState pins the start-flag design
// at the runtime seam: a session constructed with Manager: true exposes the
// Manager tools without reading any hub state, and a session constructed
// without the flag never exposes them no matter what a hub would say.
func TestManagerToolsComeFromSessionConfigNotHubState(t *testing.T) {
	for _, tc := range []struct {
		manager  bool
		wantTool bool
	}{
		{manager: true, wantTool: true},
		{manager: false, wantTool: false},
	} {
		root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
			cfg.Manager = tc.manager
		})
		client := &refusingStateClient{}
		root.SetHubClient(client)
		if got := root.reg.Get("session_control"); (got != nil) != tc.wantTool {
			t.Fatalf("Manager=%v: session_control registered = %v, want %v", tc.manager, got != nil, tc.wantTool)
		}
		if client.invoked {
			t.Fatal("Manager tool availability read hub state")
		}
	}
}
