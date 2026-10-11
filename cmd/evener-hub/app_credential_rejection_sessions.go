package hub

import (
	"context"
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
// per roster change. The roster's failure carries no turn id, so a turn
// retried and refused again with the same cause between two roster refreshes
// reads as the same failure; the probe that the first one triggered already
// recorded the rejection.
type sessionCredentialWatch struct {
	auth *hubAuthController

	mu sync.Mutex
	// ctx and run are the hub's lifetime and background runner (start). The
	// hub has them only after the roster is wired; until then observe probes
	// nothing and remembers nothing, so a failure seen then is still new
	// once it can probe.
	ctx context.Context
	run func(func())
	// failed is the (session, instance) pairs the last observe saw resting on
	// a refused credential, keyed session + "\x00" + instance.
	failed map[string]bool
}

// start hands the watch the hub's lifetime and background runner.
func (w *sessionCredentialWatch) start(ctx context.Context, run func(func())) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ctx, w.run = ctx, run
}

// observer is the roster change hook that runs observe over roster's
// listing.
func (w *sessionCredentialWatch) observer(roster *hubcore.Roster) func() {
	return func() { w.observe(roster.List) }
}

// observe probes, in the background, every instance a session has newly
// failed on with a refused credential, once per instance per observe. It
// reads the listing under its own lock, so two roster changes observed at
// once cannot apply their listings out of order.
func (w *sessionCredentialWatch) observe(list func() []hubcore.LiveEntry) {
	w.mu.Lock()
	ctx, run := w.ctx, w.run
	if run == nil || ctx.Err() != nil {
		w.mu.Unlock()
		return
	}
	failed := map[string]bool{}
	var instances []string
	for _, entry := range list() {
		instance, ok := refusedCredential(entry)
		if !ok {
			continue
		}
		key := entry.SessionID + "\x00" + instance
		failed[key] = true
		if !w.failed[key] && !slices.Contains(instances, instance) {
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
// its credential: a provider failure whose HTTP status is a rejected
// credential (rejectedCredentialStatus). The daemon sends only the
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
	return instance, rejectedCredentialStatus(cause.Status)
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
