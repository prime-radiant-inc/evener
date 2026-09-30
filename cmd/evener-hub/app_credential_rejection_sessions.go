package hub

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// sessionCredentialWatch probes an instance's credential the moment a live
// session's turn fails because the provider refused it (#3539), so the Hub
// shows Error then rather than when someone next presses Test. The session's
// failure only triggers the probe: the hub's own credential check decides,
// with the credential stored now, through the same record Test connection
// feeds. The hub lists no provider on a timer, so this is how a key that
// stops working mid-session reaches the record.
//
// The roster reports each live session's failed turn (LiveEntry.Failure);
// observe runs on every roster change and probes each instance a session has
// newly failed on. A session resting on its failure is one failure, not one
// per roster change.
type sessionCredentialWatch struct {
	auth *hubAuthController

	mu sync.Mutex
	// ctx and run are the hub's lifetime and background runner (start). The
	// hub has them only after the roster is wired; until then observe probes
	// nothing and remembers nothing, so a failure seen then is still new
	// once it can probe.
	ctx context.Context
	run func(func())
	// failed is the sessions the last observe saw resting on a refused
	// credential.
	failed map[string]bool
}

// start hands the watch the hub's lifetime and background runner.
func (w *sessionCredentialWatch) start(ctx context.Context, run func(func())) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ctx, w.run = ctx, run
}

// observe probes, in the background, every instance a session has newly
// failed on with a refused credential, once per instance per observe.
func (w *sessionCredentialWatch) observe(entries []hubcore.LiveEntry) {
	w.mu.Lock()
	ctx, run := w.ctx, w.run
	if run == nil {
		w.mu.Unlock()
		return
	}
	failed := map[string]bool{}
	var instances []string
	for _, entry := range entries {
		instance, ok := refusedCredential(entry)
		if !ok {
			continue
		}
		failed[entry.SessionID] = true
		if !w.failed[entry.SessionID] && !slices.Contains(instances, instance) {
			instances = append(instances, instance)
		}
	}
	w.failed = failed
	w.mu.Unlock()
	for _, instance := range instances {
		run(func() { w.auth.probeAfterSessionFailure(ctx, instance) })
	}
}

// refusedCredential is the instance a live session's failed turn says refused
// its credential: a provider failure with an HTTP 401 or 403, the statuses
// credentialRejectionStatus reads as a rejection. The daemon sends only the
// cause's kind and status, never the provider's text. A sign-in that expired
// is not one: it already reads as needsLogin.
func refusedCredential(entry hubcore.LiveEntry) (string, bool) {
	if entry.Crashed || entry.SessionID == "" || entry.Failure == nil || entry.Failure.Cause == nil {
		return "", false
	}
	cause := entry.Failure.Cause
	instance := strings.TrimSpace(cause.Provider)
	if cause.Kind != diagnosticCauseProvider || instance == "" {
		return "", false
	}
	if cause.Status != http.StatusUnauthorized && cause.Status != http.StatusForbidden {
		return "", false
	}
	return instance, true
}

// diagnosticCauseProvider is DiagnosticCause.Kind for an HTTP failure from an
// LLM adapter (appwire.DiagnosticCause).
const diagnosticCauseProvider = "provider"

// probeAfterSessionFailure checks name's credential the way Test connection
// does, recording what the provider says. An instance whose credential comes
// from a command is left alone: the hub mints one only when the user asks it
// to check the credential (spec §10.1), and a session's failure is not that.
func (c *hubAuthController) probeAfterSessionFailure(ctx context.Context, name string) {
	r := c.registry()
	if r == nil || r.LaunchMintsCredentialCommand(name) {
		return
	}
	_, _ = c.TestCredentials(ctx, appwire.AuthTestParams{Provider: name})
}
