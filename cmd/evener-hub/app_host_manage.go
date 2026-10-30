package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appserver"
)

// hubHostManager serves evener/host/add, evener/host/list,
// evener/host/status, evener/host/remove, and evener/host/update: the host
// registry surface. It owns no connections: list and status resolve through
// the attached-only seams, add validates hub.toml-authoritatively and wires the
// new host's source with the same seams startup uses, remove tears the host
// down through the manager's atomic RemoveHostUnderGate, and update swaps its
// entry and retires its channel through UpdateHostUnderGate — both under the
// mutation's held per-host reservation (spec 08 §4's "gate released last"). It
// never dials.
//
// It is controller-LOCAL: these methods act on the controller's own config and
// channels, so they MUST NOT be added to the AppWire host-request catalog (pinned by
// TestHostManageNotForwarded), and every handler refuses a request whose
// routing origin is non-empty (a peer hub calling over its attach bridge) —
// pinned by TestHostManageRefusesRemoteOrigin.
type hubHostManager struct {
	cfg *hostManagerConfig
	// hostFileSyncOwed is set when a compensation restored hub.toml and only the
	// directory sync behind the rename failed. The store keeps the change's
	// staged records until a sync succeeds, so no later write can discard the
	// evidence a crash needs to roll the change back.
	hostFileSyncOwed atomic.Bool
	// testOnlyParkPostCommit, when non-nil, is called by Update and Remove after
	// their durable commit, with the mutation mutex released and the per-host
	// gate reservation still held, and immediately before the post-commit live
	// phase (the manager rebind/teardown, which runs under that reservation —
	// spec 08 §4's "gate released last"). It exists so a test can hold that
	// window open deterministically instead of racing it (testing.md's "Prove a
	// Wait with a Signal" rule); nil in production.
	testOnlyParkPostCommit func(name string)
	// testOnlyParkInCommit, when non-nil, is called by Add immediately after it
	// enters its mutation critical section (the mutation lock held, nothing
	// exposed yet). It exists so a test can park a commit inside that section
	// deterministically instead of racing it — the testOnlyParkPostCommit
	// precedent, for the *pre*-commit window. Nil in production.
	testOnlyParkInCommit func(name string)
	// testOnlyBeforePinnedRun, when non-nil, is called by `teardown-retry` after
	// it releases its gate reservation and immediately before the pinned teardown
	// runs. It exists so a test can hold the host gate at that instant and prove
	// the bounded deadline still terminates the retry — the manager's teardown
	// paths block on that gate and take no context. Nil in production.
	testOnlyBeforePinnedRun func(name string)
	// testOnlyAfterStage, when non-nil, is called at the end of a commit's
	// step-(2) write, with the mutation lock still held — the staged-write→flip
	// window. It exists so a test can open that window deterministically (a
	// foreign hub.toml write) and prove the compensation drops the marker and
	// the provisional receipt the staged write installed. Nil in production.
	testOnlyAfterStage func(name string)
	// testOnlyTeardown, when non-nil, replaces the post-commit teardown the
	// mutation handlers run (the manager's RemoveHost/UpdateHost, or the
	// registry's own Remove/Update with no manager wired). It exists so a test
	// can produce the commit-point failure spec 08 §6 defines — a teardown that
	// fails after the commit landed — without a live host, the
	// testOnlyParkPostCommit precedent. Nil in production.
	testOnlyTeardown func(ctx context.Context, name string) error
	// testOnlyFailRuntimeRevert, when non-nil, fails the boot compensation's
	// runtime arm for the named host. It exists so §9's failure posture — a
	// record left in `compensating-runtime` with its stash intact, never a
	// cleared compensation beside a diverged runtime — is falsifiable without
	// a live diverged registry. Nil in production.
	testOnlyFailRuntimeRevert func(host string) error
	// testOnlyFailCompensationStep, when non-nil, fails one named step of the
	// live cross-file compensation ("advance-rows", "reinsert",
	// "advance-clear", "clear") for the host. It exists so §9's failure posture
	// — the record and its stash left for the next boot after any failed step —
	// is falsifiable without a store write failure. Nil in production.
	testOnlyFailCompensationStep func(host, step string) error
}

// logf emits through the hub logging path when one is wired (tests may pass
// nil).
func (m *hubHostManager) logf(format string, args ...any) {
	if m.cfg.logf != nil {
		m.cfg.logf(format, args...)
	}
}

// observeEvent records one SSH lifecycle event into the host rows' retained
// attach state (main.go binds it to the sshconn manager's OnEvent). It only
// records: the event arrives with the per-host lock held, so it must never
// call back into the manager.
func (m *hubHostManager) observeEvent(ev sshconn.Event) {
	m.cfg.state.observe(ev)
}

// hostManageHandler wraps one host-management handler with the
// controller-local origin guard at the point of registration, so every method
// this surface wires — present and future — refuses a remote-originated
// request (a peer hub calling over its attach bridge) before it touches the
// registry; a future handler cannot forget it. The manager's own methods keep
// the same guard for direct calls (tests, embedders call the manager as an
// API), which TestHostManageRefusesRemoteOrigin pins.
func hostManageHandler[Req, Resp any](h func(context.Context, Req) (Resp, error)) func(context.Context, Req) (Resp, error) {
	return func(ctx context.Context, params Req) (Resp, error) {
		if err := guardControllerLocalHosts(ctx); err != nil {
			var zero Resp
			return zero, err
		}
		return h(ctx, params)
	}
}

// registerHostManageHandlers installs the add/list/status/remove/update
// host-management handlers. hosts is the one live registry the server constructor resolved —
// the same instance the attach handler validates against — and the manager
// and hub.toml path come from cfg: main.go threads the live sshconn.Manager,
// the live host registry, and the selected config path through WebConfig, so
// the surface is wired in production (a nil manager or config path there —
// tests, embedders — leaves the fallbacks: no channel teardown, no host
// persistence). navigation, when non-nil, is invalidated after successful
// add/remove/update commits so the manifest's sources converge without waiting
// for the next refresh tick. It returns the manager so tests can drive it
// directly.
func registerHostManageHandlers(server *appserver.Server, sources *appsource.Registry, cfg hubcore.WebConfig, hosts *hostreg.Registry, navigation *NavigationService, logf func(format string, args ...any)) *hubHostManager {
	m := newHubHostManager(sources, cfg.RemoteHostSSHManager, cfg, cfg.RemoteHostConfigPath, hosts, logf)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostAdd, hostManageHandler(func(ctx context.Context, params appwire.HostAddParams) (appwire.HostMutationResult, error) {
		row, err := m.AddResult(ctx, params)
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return row, err
	}))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostList, hostManageHandler(m.List))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostStatus, hostManageHandler(m.Status))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRemove, hostManageHandler(func(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostMutationResult, error) {
		resp, err := m.RemoveResult(ctx, params)
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return resp, err
	}))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostUpdate, hostManageHandler(func(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostMutationResult, error) {
		resp, err := m.UpdateResult(ctx, params)
		// An edit can change the host's roots, which is what a source's identity
		// addresses; both add and remove invalidate the manifest's sources on
		// commit, and an edit that moves a source must converge the same way
		// rather than wait for the next refresh tick.
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return resp, err
	}))
	// The teardown-repair mutations register here, with the registry surface
	// they repair: both act on this controller's own hub.toml records and
	// channels, so they are controller-local like add/update/remove — refused
	// for a remote origin and never added to the AppWire host-request catalog (pinned by
	// TestHostManageNotForwarded) — and routed with catalog entries in the same
	// change (the router-vs-catalog invariant).
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostTeardownRetry, hostManageHandler(func(ctx context.Context, params appwire.HostTeardownRetryParams) (appwire.HostTeardownRetryResult, error) {
		return m.TeardownRetry(ctx, params)
	}))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostTeardownRecover, hostManageHandler(func(ctx context.Context, params appwire.HostTeardownRecoverParams) (appwire.HostTeardownRecoverResult, error) {
		return m.TeardownRecover(ctx, params)
	}))
	// evener/host/plan rides the same manager and the same registration-time
	// origin guard the settings mutations do (app_host_ops.go): plan is a
	// mutation — it mints and persists the confirmation token — so it admits
	// exactly like them and never through a second, weaker path.
	m.registerOpsHandlers(server)
	// evener/host/running is the one direction-scoped exception (app_host_running.go):
	// every hub serves its own running state, and the handler admits only the
	// attached controller session — never a browser-origin or forwarded request.
	m.registerRunningHandler(server)
	return m
}

// attachedClient resolves the live client one row may use for entry, pairing
// the channel with the entry the row renders. It reads the manager's
// ChannelIfAttached once and returns that channel's client only while the
// registration the channel was published for is the entry being rendered —
// content and generation both, the predicate hostreg.SameRegistration states
// and sshconn.Channel.MatchesRegistration applies to the channel. A host's live
// state is keyed by name, and the name outlives the registration it names, so
// a row that snapshotted its entry outside the mutation mutex (so a parked
// facts read holds up no commit) must not adopt a channel built from another
// one's attached state and facts; a mismatch renders the row offline. The facts
// and handshake seams it enables still resolve the channel by name, so a
// channel swapped inside that window leaves a row that reports attached with
// empty facts until the next build. With no manager wired (tests, embedders)
// there is no registration to compare, so the row falls back to the name-only
// clientIfAttached seam, whose callers own whatever pairing they built. The
// slice spec's §3.6 carries the rationale in full.
func (m *hubHostManager) attachedClient(entry hostreg.Host) (*appwire.Client, bool) {
	if m.cfg.manager != nil {
		ch, ok := m.cfg.manager.ChannelIfAttached(entry.Name)
		if !ok {
			return nil, false
		}
		if !ch.MatchesRegistration(entry) {
			return nil, false
		}
		return ch.Client(), true
	}
	if m.cfg.clientIfAttached != nil {
		return m.cfg.clientIfAttached(entry.Name)
	}
	return nil, false
}

// registerSource wires entry's appsource source exactly the way startup does
// (newHubSourceRegistry): the Ensure-backed dialing client when one is
// wired, the attached-only client/handshake/facts lookups, and a live online
// signal from the manager — so a host added at runtime is Connect-able and
// serves attached-only reads without a restart. Both paths run through the
// one appsource-level registrar (registerRemoteHubSource in app_rpc.go). With
// no dialing seam (tests, embedders) the client func refuses
// SessionUnavailable while detached, the pre-attach contract a hub.toml host
// already follows. A source that already exists is left alone.
func (m *hubHostManager) registerSource(entry hostreg.Host) {
	if m.cfg.sources == nil {
		return
	}
	if _, ok := m.cfg.sources.Source(entry.Name); ok {
		return
	}
	client := m.cfg.client
	if client == nil {
		client = remoteClientFor(entry.Name)
	}
	// The online seam passed here is the manager's raw signal (cfg.online),
	// never this manager's hostOnline — that reads the source, so wiring it
	// here would recurse. The shared registrar installs the same fail-open
	// default startup's sources get.
	registerRemoteHubSource(m.cfg.sources, m.cfg.remoteCache, entry, remoteHostSourceSeams{
		client:           client,
		clientIfAttached: m.cfg.clientIfAttached,
		online:           m.cfg.online,
	})
}

// markMutating records name as having a mutation in flight, in the commit phase
// that already made its durable change. Callers hold mu.
func (m *hubHostManager) markMutating(name string) {
	m.cfg.mutating[name] = struct{}{}
}

// unmarkMutating clears the mark in the finish phase, on the success and the
// failure exit alike: a successful mutation leaves the name mutable again, a
// failed one leaves it fully intact and retryable. Callers hold mu.
func (m *hubHostManager) unmarkMutating(name string) {
	delete(m.cfg.mutating, name)
}

// isMutating reports whether name has a mutation in flight — its durable commit
// landed and its live phase has not finished. Callers hold mu.
func (m *hubHostManager) isMutating(name string) bool {
	_, marked := m.cfg.mutating[name]
	return marked
}

// hostMutationConflict is the typed refusal for a name with a mutation already
// in flight: the conflict code tells the caller the name is transiently held and
// retryable, rather than mislabeling it a duplicate (Add) or file-declared (a
// second Remove). It commits nothing.
func hostMutationConflict(name string) error {
	return appwire.Conflict(fmt.Sprintf("host %q: a mutation is already in progress; retry once it finishes", name))
}

// remnantFenceRefusal is registry spec 08 §6's typed `remnant-open` conflict
// refusal: an open remnant fences the name for every lifecycle and attach path
// until teardown succeeds, and the refusal names the blocking `remnantId` —
// "never the gate-busy form — the fence names the blocking `remnantId`, not an
// in-flight operation".
func remnantFenceRefusal(name, remnantID string) error {
	return appwire.RemnantOpen(remnantID, fmt.Sprintf(
		"host %q: an open teardown remnant (%s) fences this name; resume it through evener/host/teardown-retry first",
		name, remnantID))
}

// teardownUnknownKeyRefusal is §6/§11's typed `teardown-unknown-key` not-found
// refusal: "unknown or purged ID → typed `teardown-unknown-key` not-found". A
// cleared remnant whose resolved record still survives is NOT this arm — it
// returns `already-cleared`.
func teardownUnknownKeyRefusal(remnantID string) error {
	return appwire.TeardownUnknownKey(remnantID, fmt.Sprintf(
		"no teardown remnant %q is recorded; it was never minted, or its cleared record was purged by retention", remnantID))
}

// concurrentEditRefusal is §11's typed `concurrent-edit` conflict refusal for
// the commit path: the hub.toml fingerprint moved between the validation read
// and the commit's final check after bounded retries.
func concurrentEditRefusal(stagedFingerprint, observedFingerprint string) error {
	return appwire.ConcurrentEdit(stagedFingerprint, observedFingerprint, fmt.Sprintf(
		"hub.toml changed under the mutation (staged %s, observed %s); re-read the host list and retry",
		stagedFingerprint, observedFingerprint))
}

// hostEntryField maps a hostreg validation refusal to the input it blames, in
// the wire spelling the dialog's own inputs use (HostEntry's fields), so a
// message lands on the control the operator can fix. A refusal that blames the
// entry as a whole — a cycle, or a half-specified config_path/addr pair, which
// blames two inputs at once — returns "" and the caller raises it form-level.
func hostEntryField(err error) string {
	switch {
	case errors.Is(err, hostreg.ErrMissingSSH):
		return "address"
	case errors.Is(err, hostreg.ErrAmbiguousSSHUser):
		// The field that made the destination ambiguous: user is set while ssh
		// already carries one, and the message says so.
		return "user"
	case errors.Is(err, hostreg.ErrEmptyRoot):
		return "roots"
	case errors.Is(err, hostreg.ErrInvalidKeyPath):
		// The path the dialog's key input holds: a relative value is refused
		// with the message on that control.
		return "keyPath"
	case errors.Is(err, ErrHostAddr):
		return "addr"
	case errors.Is(err, hostreg.ErrInvalidName), errors.Is(err, hostreg.ErrReservedName):
		// Add only: the edit dialog has no name input, so this field is what the
		// add form places.
		return "name"
	default:
		// ErrHostCycle and anything else blame the entry as a whole.
		return ""
	}
}

// hostValidationRefusal turns a hostreg validation refusal into the wire
// refusal: the field-carrying shape when the refusal blames one input, a plain
// InvalidParams when it blames the entry as a whole.
func hostValidationRefusal(name string, err error) error {
	message := fmt.Sprintf("host %q: %v", name, err)
	if field := hostEntryField(err); field != "" {
		return appwire.InvalidHostField(field, message)
	}
	return appwire.InvalidParams(message)
}

// hostEntryToHost converts the wire's configured-host shape to the registry's
// equivalent shape. name is supplied separately because Add takes it from the
// entry while Update takes it from the immutable request target.
func hostEntryToHost(name string, entry appwire.HostEntry) hostreg.Host {
	return hostreg.Host{
		Name:       name,
		SSH:        entry.Address,
		User:       entry.User,
		KeyPath:    entry.KeyPath,
		EvenerPath: entry.EvenerPath,
		ConfigPath: entry.ConfigPath,
		Addr:       entry.Addr,
		Roots:      entry.Roots,
	}
}

// addHostToRegistry inserts entry into the live registry. With the SSH
// manager wired it goes through the manager's AddHost, which registers under
// the same per-host gate the attach paths and RemoveHost coordinate on, so a
// remove/re-add of a name can never interleave with an in-flight attach for
// it; without one (tests, embedders) the registry's own Add is the whole
// story, since nothing can be mid-attach through this hub. The manager's
// acquisition is try-acquire, so a name whose gate is held arrives here as the
// typed busy refusal — mapped to §11's envelope, never a raw internal error —
// and this call never waits on a gate while the mutation mutex is held.
func (m *hubHostManager) addHostToRegistry(entry hostreg.Host) error {
	if m.cfg.manager != nil {
		return hostBusyWireError(m.cfg.manager.AddHost(entry))
	}
	return m.cfg.hosts.Add(entry)
}

// remoteClientFor returns the refusing client func for a host whose source
// has no dialing seam (an embedder or a test built without
// cfg.RemoteHostClient): the source serves attached-only reads until the
// first explicit Connect, exactly like a hub.toml host before its first
// attach.
func remoteClientFor(host string) func(ctx context.Context, _ string) (*appwire.Client, error) {
	return func(_ context.Context, _ string) (*appwire.Client, error) {
		return nil, appwire.SessionUnavailable(fmt.Sprintf("host %q is not attached", host))
	}
}

// dropHostDerivedState retires everything keyed to name once its entry is
// gone: the source registration, the name-keyed attach record, the remote-thread
// cache entry (its per-source generation included), then the web server's
// retained last-known-good list. The order is load-bearing: an in-flight walk
// must fail its cache-generation sweep or its source-ownership check, so it
// cannot re-store obsolete rows after the cache drop. The retained list must go
// too — left behind, the entry and its thread rows outlive the host for the
// process lifetime, and churning distinct host names grows the map without
// bound. Callers have already dropped (or committed the drop of) the store row,
// so nothing renders the name again, and they hold the mutation mutex.
func (m *hubHostManager) dropHostDerivedState(name string) {
	if m.cfg.sources != nil {
		m.cfg.sources.Remove(name)
	}
	m.cfg.state.remove(name)
	if m.cfg.remoteCache != nil {
		m.cfg.remoteCache.RemoveSource(name)
	}
	if m.cfg.forgetLastGoodThreads != nil {
		m.cfg.forgetLastGoodThreads(name)
	}
}

// unionHosts returns a and b's distinct entries by name, in a-then-b order.
// The result is never nil, even when both inputs are empty: the writers use
// nil known as their exact-write sentinel, so a compensation's known set must
// stay a real snapshot.
func unionHosts(a, b []hostreg.Host) []hostreg.Host {
	out := make([]hostreg.Host, 0, len(a)+len(b))
	out = append(out, a...)
	seen := make(map[string]struct{}, len(a))
	for _, e := range a {
		seen[e.Name] = struct{}{}
	}
	for _, e := range b {
		if _, ok := seen[e.Name]; ok {
			continue
		}
		seen[e.Name] = struct{}{}
		out = append(out, e)
	}
	return out
}

// sortedMapKeys returns a map's string keys in sorted order, so every boot
// pass walks its records deterministically.
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// hostEntryNamed returns the entry for name, if the slice carries one.
func hostEntryNamed(entries []hostreg.Host, name string) (hostreg.Host, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return hostreg.Host{}, false
}
