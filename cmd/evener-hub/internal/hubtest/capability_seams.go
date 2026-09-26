package hubtest

import (
	"context"

	"primeradiant.com/evener/appwire"
	daemonserver "primeradiant.com/evener/server"
)

// WireCapabilitySeams installs a no-op implementation of every seam a
// server consults in appCapabilitiesLocked, the production shape
// cmd/evener/serve.go wires, so a test daemon answers with every capability
// but fork true at idle (the daemon hardwires that one false). A seam added
// to appCapabilitiesLocked belongs here, once, so every test that needs a
// production-shaped capability answer shares one definition of "fully
// wired".
func WireCapabilitySeams(srv *daemonserver.Server) {
	srv.SetRetrySafeTurnFunctions(daemonserver.RetrySafeTurnFunctions{
		Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, nil
		},
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return appwire.TurnSteerResponse{}, nil
		},
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return appwire.TurnQueueResponse{}, nil
		},
		Drain: func(appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			return appwire.TurnDrainAsSteerResponse{}, nil
		},
		Promote: func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
		},
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return appwire.TurnCancelQueuedResponse{}, nil
		},
		Interrupt: func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			return appwire.TurnInterruptResponse{}, nil
		},
	})
	srv.SetCompactFunc(func(context.Context) error { return nil })
	srv.SetClearFunc(func(context.Context, appwire.ThreadClearParams) error { return nil })
	srv.SetShutdownFunc(func() {})
	srv.SetModelFunc(func(string) error { return nil })
	srv.SetVisionModelFunc(func(string) error { return nil })
	srv.SetNameFunc(func(string) error { return nil })
	srv.SetGoalFunc(func(string) (bool, error) { return false, nil })
	srv.SetNotesHumanSetFunc(func(outerID, note string) (appwire.NotesHumanSetResponse, error) {
		return appwire.NotesHumanSetResponse{}, nil
	})
	srv.SetUrlsRemoveFunc(func(outerID, id string) (bool, error) { return false, nil })
}
