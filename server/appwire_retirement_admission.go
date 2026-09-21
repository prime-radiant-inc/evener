package server

import (
	"context"
	"errors"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// daemonRetirementAccess is exhaustive for the daemon catalog. Connection
// methods are intentionally not runtime borrowers.
var daemonRetirementAccessKinds = map[string]string{
	appwire.MethodThreadList:                     "read",
	appwire.MethodThreadRead:                     "read",
	appwire.MethodThreadUnsubscribe:              "read",
	appwire.MethodThreadTurnsList:                "read",
	appwire.MethodEvenerTasksList:                "read",
	appwire.MethodEvenerJobsList:                 "read",
	appwire.MethodEvenerJobsOutput:               "read",
	appwire.MethodModelList:                      "read",
	appwire.MethodThreadClear:                    "mutation",
	appwire.MethodThreadModelSet:                 "mutation",
	appwire.MethodEvenerThreadNameSet:            "mutation",
	appwire.MethodThreadReasoningEffortSet:       "mutation",
	appwire.MethodThreadVisionModelSet:           "mutation",
	appwire.MethodThreadCompactStart:             "mutation",
	appwire.MethodTurnStart:                      "mutation",
	appwire.MethodTurnSteer:                      "mutation",
	appwire.MethodTurnInterrupt:                  "mutation",
	appwire.MethodTurnQueue:                      "mutation",
	appwire.MethodTurnDrainAsSteer:               "mutation",
	appwire.MethodTurnPromoteQueuedAsSteer:       "mutation",
	appwire.MethodTurnCancelQueued:               "mutation",
	appwire.MethodGoalSet:                        "mutation",
	appwire.MethodEvenerSandboxEscalationResolve: "mutation",
	appwire.MethodNotesHumanSet:                  "mutation",
	appwire.MethodUrlsRemove:                     "mutation",
	appwire.MethodThreadShutdown:                 "control",
	appwire.MethodEvenerDaemonStatus:             "control",
	appwire.MethodEvenerDaemonRetire:             "control",
	appwire.MethodEvenerDaemonIdleTimeoutSet:     "control",
}

func daemonRetirementAccess(method string) (kind string, ok bool) {
	kind, ok = daemonRetirementAccessKinds[method]
	return kind, ok
}

// daemonConnectionMethods is the wire-name set of connection-level methods
// (initialize, ping). They are registered on the router but are part of the
// connection handshake, not application routing, so admission leaves them
// lease-free exactly like control methods.
var daemonConnectionMethods = func() map[string]struct{} {
	names := appwire.ConnectionMethodNames()
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}()

// SetRetirementAdmission installs process-owned admission before serving. fn
// receives the access kind (read or mutation), not a method name. Control retains
// the serve exit-owner path; connection methods do not access runtime resources.
// The lease ends when the handler returns, before response serialization.
func (s *Server) SetRetirementAdmission(fn func(context.Context, string) (func(), error)) {
	if fn == nil {
		s.AppServer().Router().SetAdmission(nil)
		return
	}
	s.AppServer().Router().SetAdmission(func(ctx context.Context, method string) (func(), error) {
		kind, ok := daemonRetirementAccess(method)
		if ok && kind == "control" {
			return func() {}, nil
		}
		if _, connection := daemonConnectionMethods[method]; connection {
			return func() {}, nil
		}
		if !ok {
			// Fail closed: a routed method the catalog table does not classify is
			// treated as a mutation, so a new mutation omitted from the table
			// cannot silently run with no retirement lease during
			// preparing/retiring (the missing-admission-fence shape).
			kind = "mutation"
		}
		release, err := fn(ctx, kind)
		if err != nil && errors.Is(err, agent.ErrRetirementUnavailable) {
			// The admission fence closed under this request: when a lifecycle
			// status hook is installed, type the race so the caller can retry
			// automatically. Without one, pass the sentinel through raw.
			if status, _ := s.daemonLifecycleHooks(); status != nil {
				return nil, appwire.LifecycleUnavailable(status().Phase)
			}
		}
		return release, err
	})
}
