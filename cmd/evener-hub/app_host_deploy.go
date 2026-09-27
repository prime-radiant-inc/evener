package hub

// evener/host/deploy and evener/host/restart (deploy pipeline 08b §6, §10, §11):
// the operation model behind the plan's confirmation token. Both handlers run
// the same fixed processing order — dedup first, the provisional token check
// and remnant fence, the per-host gate with the gated probe and under-gate
// re-resolution, then the atomic durable record write — and both hand their
// long-running work to a worker under the controller-lifetime context, never
// the RPC's.
//
// Boundary seams this slice deliberately leaves to later ones:
//
//   - remnants (S12): the remnant fence is a seam (m.remnantOpen) consulted
//     exactly where §6 step 2 puts it — past dedup, before any probe or
//     acquisition — and renders the typed `remnant-open` arm. No remnant store
//     is built here.
//   - last-known publication (its own slice): the post-operation refresh routes
//     its verified facts through m.cfg.lastKnownPublish, a seam that is nil in
//     production today. The worker's success does not depend on it, and the
//     residual is recorded where the seam is declared.
//   - restart reattach (S13): the interim shape holds the gate through the
//     restart and its health verification, releases it at one named point so
//     the existing manager reconnect can reattach, re-acquires it, and
//     re-probes. §6's operation-owned `attachUnderGate` is not built here; the
//     release point names the deviation.
//   - crash fencing (S17/S18): the fencing epoch is persisted and presented;
//     the remote write half keeps S3's same-boot subset (hostops/probeepoch.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// operationsShutdownNote is the note the worker writes when the controller
// shuts down mid-operation (§6's worker lifetime): the record transitions to
// `interrupted` and the host's gate is released.
const operationsShutdownNote = "interrupted: the controller is shutting down"

// operationsShutdownTimeout bounds how long the hub's teardown waits for
// in-flight workers to settle their records after their context is canceled.
const operationsShutdownTimeout = 5 * time.Second

// errHostDetached marks a probe refused because the attached channel the
// operation resolved is gone (never a dial). Deploy maps it to the typed
// `host-detached` refusal; plan keeps its own prose classification.
var errHostDetached = errors.New("hub: the host's attached channel is gone")

// errProbeTimedOut marks a probe that hit its own deadline (never the caller's
// context ending), so the refusal's data can name `timed-out` distinctly.
var errProbeTimedOut = errors.New("hub: the running probe timed out")

// operationRequest is one deploy/restart request after validation and host
// resolution: the shared machinery's input.
type operationRequest struct {
	name              string
	clientOperationID string
	kind              hostops.Kind
	// tokenValue is the presented confirmation token (deploy only).
	tokenValue string
	// intended is the pair a restart request names. A deploy names none here:
	// its intended pair comes from the host's outstanding token row, which the
	// lookup reads itself.
	intended *hostops.OperationPair
	// fingerprint is the host's hub.toml entry fingerprint bound at resolution
	// (restart only — restart has no token to carry it).
	fingerprint string
}

// Deploy implements evener/host/deploy (§6, §10). Order: dedup first — a hit
// returns the existing record with no token validation and no consumption —
// then the provisional token check and the remnant fence, then the gated probe
// window with the under-gate re-resolution, then the atomic consume-and-create
// write, after which the RPC returns and the worker owns the operation.
func (m *hubHostManager) Deploy(ctx context.Context, params appwire.HostDeployParams) (appwire.HostDeployResponse, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.HostDeployResponse{}, appwire.InvalidParams("evener/host/deploy needs a host name")
	}
	if params.Token == "" {
		return appwire.HostDeployResponse{}, appwire.InvalidParams("evener/host/deploy needs the plan's confirmation token")
	}
	if err := validateClientOperationID(params.OperationID); err != nil {
		return appwire.HostDeployResponse{}, err
	}
	record, err := m.startOperation(ctx, operationRequest{
		name:              name,
		clientOperationID: params.OperationID,
		kind:              hostops.KindDeploy,
		tokenValue:        params.Token,
	})
	if err != nil {
		return appwire.HostDeployResponse{}, err
	}
	state, err := operationWireState(record.State)
	if err != nil {
		return appwire.HostDeployResponse{}, appwire.InternalError(err.Error())
	}
	return appwire.HostDeployResponse{ID: record.ID, ClientOperationID: record.ClientOperationID, State: state}, nil
}

// Restart implements evener/host/restart (§6, §10). It is the same operation
// model without a token: the request carries the intended (generation,
// incarnation id) pair, restart binds the host's hub.toml entry fingerprint at
// resolution and re-checks it under the gate, and the atomic record creation
// mints the worker's fencing epoch in the same write.
func (m *hubHostManager) Restart(ctx context.Context, params appwire.HostRestartParams) (appwire.HostRestartResponse, error) {
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.HostRestartResponse{}, appwire.InvalidParams("evener/host/restart needs a host name")
	}
	if err := validateClientOperationID(params.OperationID); err != nil {
		return appwire.HostRestartResponse{}, err
	}
	if params.Generation == 0 || strings.TrimSpace(params.IncarnationID) == "" {
		return appwire.HostRestartResponse{}, appwire.InvalidParams(
			"evener/host/restart needs the intended (generation, incarnationId) pair")
	}
	intended := hostops.OperationPair{Generation: params.Generation, IncarnationID: params.IncarnationID}
	// The fingerprint is bound at resolution (§6): the request names no token,
	// so the reference the worker re-reads before the irreversible restart is
	// captured here, before the gate, and re-checked under it.
	fingerprint, ok := m.hostTOMLFingerprint(name)
	if !ok {
		return appwire.HostRestartResponse{}, appwire.InternalError(fmt.Sprintf(
			"host %q's hub.toml entry could not be fingerprinted: the selected hub.toml (%s) does not validate, so restart cannot bind the entry it must re-check before restarting",
			name, m.cfg.configPath))
	}
	record, err := m.startOperation(ctx, operationRequest{
		name:              name,
		clientOperationID: params.OperationID,
		kind:              hostops.KindRestart,
		intended:          &intended,
		fingerprint:       fingerprint,
	})
	if err != nil {
		return appwire.HostRestartResponse{}, err
	}
	state, err := operationWireState(record.State)
	if err != nil {
		return appwire.HostRestartResponse{}, appwire.InternalError(err.Error())
	}
	return appwire.HostRestartResponse{ID: record.ID, ClientOperationID: record.ClientOperationID, State: state}, nil
}

// validateClientOperationID checks §1's client operation ID rules: opaque,
// non-empty, at most 128 bytes.
func validateClientOperationID(id string) error {
	switch {
	case id == "":
		return appwire.InvalidParams("the operation needs a client operation ID")
	case len(id) > hostops.MaxClientOperationIDBytes:
		return appwire.InvalidParams(fmt.Sprintf(
			"the client operation ID is %d bytes, over the %d-byte bound", len(id), hostops.MaxClientOperationIDBytes))
	}
	return nil
}

// operationWireState maps a record's durable state onto the wire's closed set.
func operationWireState(state hostops.State) (appwire.OperationState, error) {
	switch state {
	case hostops.StatePending:
		return appwire.OperationStatePending, nil
	case hostops.StateRunning:
		return appwire.OperationStateRunning, nil
	case hostops.StateComplete:
		return appwire.OperationStateComplete, nil
	case hostops.StateFailed:
		return appwire.OperationStateFailed, nil
	case hostops.StateInterrupted:
		return appwire.OperationStateInterrupted, nil
	case hostops.StateOrphanUnverified:
		return appwire.OperationStateOrphanUnverified, nil
	}
	return "", fmt.Errorf("hub: record state %q is outside the wire's operation set", state)
}

// liveHost resolves name from the live registry.
func (m *hubHostManager) liveHost(name string) (hostreg.Host, bool) {
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	return m.cfg.hosts.Get(name)
}

// remnantOpen reports the open remnant fencing name, if one does. Remnants are
// S12's (registry spec §6); this seam is where their fence lands — §6 step 2
// puts the refusal past dedup and before any probe or acquisition — and it
// answers "no remnant" until that slice ships a store.
func (m *hubHostManager) remnantOpen(name string) string {
	if m.cfg.remnantFence == nil {
		return ""
	}
	remnantID, ok := m.cfg.remnantFence(name)
	if !ok {
		return ""
	}
	return remnantID
}

// startOperation runs the shared fixed processing order and returns the
// operation record the caller answers with — the existing record on a dedup
// hit, the fresh `pending` record once the atomic write lands.
func (m *hubHostManager) startOperation(ctx context.Context, req operationRequest) (hostops.Record, error) {
	entry, ok := m.liveHost(req.name)
	if !ok {
		return hostops.Record{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", req.name))
	}
	ops := m.cfg.ops
	if ops == nil {
		return hostops.Record{}, appwire.InternalError("the host operation store is not configured, so no operation can be recorded")
	}
	pair := hostops.OperationPair{Generation: entry.Generation, IncarnationID: entry.IncarnationID}

	// (1) Dedup first: a hit returns the record with no token validation and no
	// consumption, so a lost-response replay succeeds without a fresh token and
	// without acquiring a contested gate.
	query := hostops.OperationDedupQuery{
		ClientOperationID: req.clientOperationID,
		Host:              entry.Name,
		Kind:              req.kind,
		Current:           pair,
	}
	switch req.kind {
	case hostops.KindDeploy:
		// The intended pair a deploy names is the one its outstanding token row
		// carries; a consumed row names none, which is what makes a retry after
		// an intervening update replay the newest retained record instead of
		// opening fresh (§4).
		if row, ok := ops.OutstandingToken(entry.Name); ok {
			intended := hostops.OperationPair{Generation: row.Generation, IncarnationID: row.IncarnationID}
			query.Intended = &intended
		}
	case hostops.KindRestart:
		query.Intended = req.intended
	}
	record, hit, err := ops.LookupOperation(query)
	if err != nil {
		return hostops.Record{}, operationRefusal(entry.Name, err)
	}
	if hit {
		return record, nil
	}

	// (2) The provisional token check (deploy) is a fail-fast readability check
	// only: nothing is decided here, and a concurrent plan can supersede the
	// value before the gate is acquired. Then the remnant fence: a fenced name
	// never reaches the gate.
	if req.kind == hostops.KindDeploy {
		if _, err := ops.ValidateToken(entry.Name, req.tokenValue); err != nil {
			return hostops.Record{}, tokenRefusalWire(entry.Name, err)
		}
	}
	if remnantID := m.remnantOpen(entry.Name); remnantID != "" {
		return hostops.Record{}, appwire.RemnantOpen(remnantID, fmt.Sprintf(
			"host %q has an open teardown remnant; resume it through teardown-retry before deploying or restarting", entry.Name))
	}

	// (3) The gate, then the probe window and the under-gate re-resolution.
	release, err := m.acquireHostGate(entry.Name, hostops.Holder{Kind: hostops.HolderOperation})
	if err != nil {
		return hostops.Record{}, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()

	if !m.cfg.hosts.SameRegistration(entry.Name, entry) {
		return hostops.Record{}, appwire.StaleEntry(appwire.StaleEntryBindingGeneration, fmt.Sprintf(
			"host %q's registration moved while this operation was being resolved; retry", entry.Name))
	}
	client, attached := m.attachedClient(entry)
	if !attached {
		return hostops.Record{}, m.hostDetachedRefusal(entry.Name)
	}

	// sequenceBefore is §6 step 3's probe-window position: any terminal
	// transition above it — seen by the local scan below or by the consume
	// write's own locked read — is a concurrent-terminal-op refusal.
	sequenceBefore := ops.Sequence()

	// The deploy path's durable probe epoch precedes the first probe: its own
	// atomic store write, bound to the pair, and promoted by the consume.
	var epoch hostops.ProbeEpoch
	var probe hubcore.HostRuntimeProbe
	var facts sshconn.Preflight
	if req.kind == hostops.KindDeploy {
		epoch, err = m.persistOperationProbeEpoch(entry)
		switch {
		case err != nil && !hostops.RenameLanded(err):
			return hostops.Record{}, appwire.ProbeFailed(entry.Name, appwire.ProbeFailureReadFailed, fmt.Sprintf(
				"host %q: the durable probe epoch could not be persisted, so nothing was probed: %v", entry.Name, err))
		case err != nil:
			m.logf("host %q: the probe epoch's directory sync failed after its rename landed; the epoch is durable: %v", entry.Name, err)
		}
		probe, err = m.probeForOperation(ctx, entry, client, epoch)
		if err != nil {
			m.dropOperationProbeEpoch(entry.Name, epoch)
			return hostops.Record{}, m.operationProbeRefusal(entry.Name, err)
		}
		// The deployment's own target re-resolution and running-state checks
		// need the attached channel's captured preflight (the deploy path runs
		// no fresh preflight of its own, §6 step 3).
		facts, ok = m.attachedFactsFor(entry.Name)
		if !ok {
			m.dropOperationProbeEpoch(entry.Name, epoch)
			return hostops.Record{}, m.hostDetachedRefusal(entry.Name)
		}
	}

	// The under-gate re-resolution: a mutation can legally commit between the
	// resolution above and this gate.
	if err := m.revalidateUnderGate(entry, req, probe, facts); err != nil {
		if req.kind == hostops.KindDeploy {
			m.dropOperationProbeEpoch(entry.Name, epoch)
		}
		return hostops.Record{}, err
	}

	// The probe window closes with the local scan: an operation that finished
	// while the probe ran is a re-plan refusal. The consume write re-runs the
	// same check inside its locked read, so nothing can slip between.
	if id, landed := ops.TerminalOperationSince(entry.Name, sequenceBefore); landed {
		if req.kind == hostops.KindDeploy {
			m.dropOperationProbeEpoch(entry.Name, epoch)
		}
		return hostops.Record{}, appwire.StaleEntry(appwire.StaleEntryBindingConcurrentTerminalOp, fmt.Sprintf(
			"host %q completed operation %q while this operation was being resolved; retry", entry.Name, id))
	}

	// (4) The atomic write. For a deploy it re-reads and consumes the token and
	// promotes the probe epoch; for a restart it creates the record with its
	// freshly minted epoch. Either way the record exists before the RPC
	// answers, and the worker that follows owns the gate release.
	var created hostops.Record
	switch req.kind {
	case hostops.KindDeploy:
		created, _, err = ops.ConsumeTokenAndCreateOperation(hostops.OperationCreateRequest{
			ClientOperationID: req.clientOperationID,
			Host:              entry.Name,
			Kind:              hostops.KindDeploy,
			Pair:              pair,
			TokenValue:        req.tokenValue,
			SequenceBefore:    sequenceBefore,
		})
	case hostops.KindRestart:
		created, _, err = ops.CreateOperation(hostops.OperationCreateRequest{
			ClientOperationID: req.clientOperationID,
			Host:              entry.Name,
			Kind:              hostops.KindRestart,
			Pair:              pair,
			BootID:            m.cfg.bootID,
			SequenceBefore:    sequenceBefore,
		})
	}
	if err != nil && !hostops.RenameLanded(err) {
		return hostops.Record{}, operationRefusal(entry.Name, err)
	}
	if err != nil {
		// The rename landed, so the record is durable (see hostops.RenameLanded):
		// the operation proceeds and the sync trouble is logged.
		m.logf("host %q: operation %s's directory sync failed after its rename landed; the record is durable: %v",
			entry.Name, created.ID, err)
	}

	// Promotion (item §5's holder publication): the record exists, so a
	// contender's busy refusal upgrades from the recordless transient form to
	// the operation class naming the record id. A promotion that cannot publish
	// is logged, never fatal: the record is durable and the worker still runs.
	if err := m.cfg.gate.HoldAs(entry.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: created.ID}); err != nil {
		m.logf("host %q: operation %s could not publish itself as the gate holder: %v", entry.Name, created.ID, err)
	}

	work := opWork{
		record:      created,
		entry:       entry,
		probe:       probe,
		fingerprint: req.fingerprint,
		release:     release,
	}
	if req.kind == hostops.KindDeploy {
		if token, ok := ops.OutstandingToken(entry.Name); ok {
			work.token = token
		}
		if epoch.BootID != "" {
			work.epoch = appwire.FencingEpoch{BootID: epoch.BootID, OpSeq: epoch.OpSeq}
		}
	} else {
		work.epoch = epochOf(created)
	}
	handedOff = true
	m.startOperationWorker(work)
	return created, nil
}

// persistOperationProbeEpoch writes the operation's durable probe epoch (§6
// step 3): its own atomic store write, bound to the entry's current pair, with
// the store assigning the per-host op sequence. A hub with no operation store
// or no boot id cannot persist one, so it refuses — a probe without a durable
// epoch is exactly the unfenced write the spec forbids.
func (m *hubHostManager) persistOperationProbeEpoch(entry hostreg.Host) (hostops.ProbeEpoch, error) {
	if m.cfg.ops == nil {
		return hostops.ProbeEpoch{}, errors.New("the host operation store is not configured")
	}
	if strings.TrimSpace(m.cfg.bootID) == "" {
		return hostops.ProbeEpoch{}, errors.New("this hub carries no boot id, so no fencible probe epoch can be bound")
	}
	return m.cfg.ops.PersistProbeEpoch(hostops.ProbeEpochRequest{
		Host:          entry.Name,
		BootID:        m.cfg.bootID,
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
	})
}

// dropOperationProbeEpoch deletes the operation's epoch on a refusal path. The
// match is exact, so a stale drop can never remove a row another call
// persisted; a failure is logged, not returned — the row left behind is inert
// and the next boot's reap deletes it.
func (m *hubHostManager) dropOperationProbeEpoch(name string, epoch hostops.ProbeEpoch) {
	if m.cfg.ops == nil || epoch.BootID == "" {
		return
	}
	if err := m.cfg.ops.DropProbeEpoch(name, epoch.BootID, epoch.OpSeq); err != nil {
		m.logf("host %q: the probe epoch %s/%d could not be dropped after a refused operation: %v",
			name, epoch.BootID, epoch.OpSeq, err)
	}
}

// probeForOperation runs the gated running probe under the caller's held gate,
// presenting the persisted epoch. It is the plan's gate-aware primitive — the
// same channel resolution and the same epoch, never a default — so the probe
// can never pair one registration's facts with another's running state.
func (m *hubHostManager) probeForOperation(ctx context.Context, entry hostreg.Host, client *appwire.Client, epoch hostops.ProbeEpoch) (hubcore.HostRuntimeProbe, error) {
	if m.cfg.planProbe == nil {
		return hubcore.HostRuntimeProbe{}, fmt.Errorf("%w: this hub has no running-probe primitive wired", errPlanHandlerAbsent)
	}
	return m.cfg.planProbe(ctx, entry, client, appwire.FencingEpoch{BootID: epoch.BootID, OpSeq: epoch.OpSeq})
}

// operationProbeRefusal maps a probe failure onto §11's typed arms: a channel
// that is gone is `host-detached` (token unconsumed, no record); a remote that
// predates the handler and every other read failure is `probe-failed` with the
// failure half named.
func (m *hubHostManager) operationProbeRefusal(name string, err error) error {
	switch {
	case errors.Is(err, errHostDetached):
		return m.hostDetachedRefusal(name)
	case errors.Is(err, errPlanHandlerAbsent):
		return appwire.ProbeFailed(name, appwire.ProbeFailureReadFailed, fmt.Sprintf(
			"host %q's hub predates the deploy pipeline's running probe, so nothing could be probed; upgrade the host's build before deploying: %v", name, err))
	case errors.Is(err, errProbeTimedOut):
		return appwire.ProbeFailed(name, appwire.ProbeFailureTimedOut, fmt.Sprintf(
			"probing host %q's running state timed out: %v", name, err))
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// The RPC's own context ended: nothing was probed and nothing was
		// consumed, so the honest refusal is the cancellation itself.
		return err
	default:
		return appwire.ProbeFailed(name, appwire.ProbeFailureReadFailed, fmt.Sprintf(
			"probing host %q's running state failed: %v", name, err))
	}
}

// hostDetachedRefusal is §11's `host-detached`: the channel is gone, the token
// is unconsumed and no record exists; the client Connects and re-plans.
func (m *hubHostManager) hostDetachedRefusal(name string) error {
	return appwire.HostDetached(fmt.Sprintf(
		"host %q has no live attached channel, so nothing was probed and nothing was consumed; connect it and re-plan", name))
}

// attachedFactsFor returns the attached channel's captured preflight facts. It
// is the deploy path's facts source: deploy runs no fresh preflight of its own
// (§6 step 3), so it re-resolves the target against what the attached channel
// already read on disk.
func (m *hubHostManager) attachedFactsFor(name string) (sshconn.Preflight, bool) {
	if m.cfg.attachedFacts == nil {
		return sshconn.Preflight{}, false
	}
	return m.cfg.attachedFacts(name)
}

// revalidateUnderGate is §6 step 3's under-gate re-resolution, run while the
// caller holds the gate. Every check is a refusal with the token unconsumed and
// no record: a registry pair that moved, an absent or replaced channel, a
// hub.toml entry fingerprint that drifted, a deploy target that no longer
// resolves (or resolves elsewhere), facts past the token-bound freshness
// bound, and a re-probed running revision or health that differs from the
// token's bindings.
func (m *hubHostManager) revalidateUnderGate(entry hostreg.Host, req operationRequest, probe hubcore.HostRuntimeProbe, facts sshconn.Preflight) error {
	if !m.cfg.hosts.SameRegistration(entry.Name, entry) {
		return appwire.StaleEntry(appwire.StaleEntryBindingGeneration, fmt.Sprintf(
			"host %q's registration moved while this operation held its gate; retry", entry.Name))
	}
	if _, attached := m.attachedClient(entry); !attached {
		return m.hostDetachedRefusal(entry.Name)
	}
	fingerprint, ok := m.hostTOMLFingerprint(entry.Name)
	if !ok {
		return appwire.InternalError(fmt.Sprintf(
			"host %q's hub.toml entry could not be fingerprinted: the selected hub.toml (%s) does not validate; fix the file before deploying",
			entry.Name, m.cfg.configPath))
	}
	if req.kind == hostops.KindDeploy {
		ops := m.cfg.ops
		token, err := ops.ValidateToken(entry.Name, req.tokenValue)
		if err != nil {
			// The token moved while the probe ran (a concurrent plan superseded
			// it, or its deadline passed): the same typed refusal step 2 emits.
			return tokenRefusalWire(entry.Name, err)
		}
		if err := hostops.CheckTokenBindings(token, hostops.TokenExpectation{
			Generation:         entry.Generation,
			IncarnationID:      entry.IncarnationID,
			EntryHash:          hostEntryFingerprint(entry),
			HubTOMLFingerprint: fingerprint,
		}); err != nil {
			return staleEntryWire(entry.Name, err)
		}
		target, err := sshconn.DeployRunTargetFor(entry, facts.Home)
		if err != nil {
			return appwire.StaleEntry(appwire.StaleEntryBindingTarget, fmt.Sprintf(
				"host %q: the deploy target this operation resolved no longer resolves: %v", entry.Name, err))
		}
		if err := hostops.CheckTokenBindings(token, hostops.TokenExpectation{TargetPath: target}); err != nil {
			return staleEntryWire(entry.Name, err)
		}
		if err := hostops.CheckTokenFactsAge(token, ops.EffectiveNow()); err != nil {
			return staleEntryWire(entry.Name, err)
		}
		if err := hostops.CheckTokenRunningState(token, hostops.TokenRunningState{
			Version:          probe.Version,
			Healthy:          probe.RunningHealthy,
			ProcessStartTime: probe.ProcessStartTime,
		}); err != nil {
			return staleEntryWire(entry.Name, err)
		}
		return nil
	}
	// Restart: the fingerprint bound at resolution is re-checked under the gate
	// (§6), alongside the post-acquisition entry re-read above.
	if req.fingerprint != "" && fingerprint != req.fingerprint {
		return appwire.StaleEntry(appwire.StaleEntryBindingHubTOMLFingerprint, fmt.Sprintf(
			"host %q's hub.toml entry changed between restart's resolution and its gate; retry", entry.Name))
	}
	return nil
}

// staleEntryWire renders a hostops stale-entry refusal as §11's envelope,
// carrying the binding value the store named.
func staleEntryWire(name string, err error) error {
	stale, ok := errors.AsType[*hostops.StaleEntryError](err)
	if !ok {
		return appwire.InternalError(fmt.Sprintf("host %q: %v", name, err))
	}
	return appwire.StaleEntry(appwire.StaleEntryBinding(stale.Binding), fmt.Sprintf("host %q: %v", name, err))
}

// tokenRefusalWire maps the store's token refusals onto §11's four typed arms.
func tokenRefusalWire(name string, err error) error {
	switch {
	case errors.Is(err, hostops.ErrTokenMissing):
		return appwire.TokenMissing(fmt.Sprintf("host %q: %v", name, err))
	case errors.Is(err, hostops.ErrTokenMismatched):
		return appwire.TokenMismatched(fmt.Sprintf("host %q: %v", name, err))
	case errors.Is(err, hostops.ErrTokenSuperseded):
		return appwire.TokenSuperseded(fmt.Sprintf("host %q: %v", name, err))
	case errors.Is(err, hostops.ErrTokenExpired):
		return appwire.TokenExpired(fmt.Sprintf("host %q: %v", name, err))
	default:
		return appwire.InternalError(fmt.Sprintf("host %q: %v", name, err))
	}
}

// operationRefusal maps the store's operation refusals onto §11's arms.
func operationRefusal(name string, err error) error {
	if conflict, ok := errors.AsType[*hostops.ConflictingOperationIDError](err); ok {
		return appwire.ConflictingOperationID(fmt.Sprintf(
			"host %q: the client operation ID %q is already used by %s operation %s on %q",
			name, conflict.Record.ClientOperationID, conflict.Record.Kind, conflict.Record.ID, conflict.Record.Host))
	}
	if errors.Is(err, hostops.ErrTokenMissing) || errors.Is(err, hostops.ErrTokenMismatched) ||
		errors.Is(err, hostops.ErrTokenSuperseded) || errors.Is(err, hostops.ErrTokenExpired) {
		return operationRefusalForTokenPaths(name, err)
	}
	if _, ok := errors.AsType[*hostops.StaleEntryError](err); ok {
		return operationRefusalForTokenPaths(name, err)
	}
	if concurrent, ok := errors.AsType[*hostops.ConcurrentTerminalOpError](err); ok {
		return appwire.StaleEntry(appwire.StaleEntryBindingConcurrentTerminalOp, fmt.Sprintf(
			"host %q completed operation %q while this operation was being resolved; retry", name, concurrent.ID))
	}
	if errors.Is(err, hostops.ErrProbeEpochUnavailable) {
		return appwire.InternalError(fmt.Sprintf("host %q: %v", name, err))
	}
	return appwire.InternalError(fmt.Sprintf("recording the operation for host %q failed: %v", name, err))
}

// operationRefusalForTokenPaths routes the refusals the consume write can
// classify (token and stale-entry values) to their own renderers.
func operationRefusalForTokenPaths(name string, err error) error {
	if _, ok := errors.AsType[*hostops.StaleEntryError](err); ok {
		return staleEntryWire(name, err)
	}
	return tokenRefusalWire(name, err)
}

// epochOf reads a record's fencing epoch back off its raw field. A record this
// hub created always carries one; a record without one is rendered as the zero
// epoch, which the probe refuses (never an unfenced write).
func epochOf(record hostops.Record) appwire.FencingEpoch {
	var epoch appwire.FencingEpoch
	if len(record.FencingEpoch) == 0 {
		return epoch
	}
	_ = json.Unmarshal(record.FencingEpoch, &epoch)
	return epoch
}

// opWork is everything one operation's worker needs from the handler's
// resolution: the durable record, the entry and token bindings the worker
// re-checks, the pre-operation probe, the record's fencing epoch, and the
// gate release the worker owns until the record reaches a terminal state.
type opWork struct {
	record      hostops.Record
	entry       hostreg.Host
	token       hostops.Token
	probe       hubcore.HostRuntimeProbe
	epoch       appwire.FencingEpoch
	fingerprint string
	release     func()
}

// startOperationWorker launches the operation's worker under the
// controller-lifetime context — never the RPC's — so a client disconnect
// cannot cancel a persisted operation. Only the controller's shutdown cancels
// it: the worker then records `interrupted` (with the shutdown note) and
// releases the host's gate.
func (m *hubHostManager) startOperationWorker(work opWork) {
	ctx := m.operationContext()
	m.cfg.opsWG.Go(func() {
		m.runOperationWorker(ctx, work)
	})
}

// operationContext returns the controller-lifetime context the workers run
// under, creating it on first use so a hub built without one (tests) still
// gets a cancellable lifetime.
func (m *hubHostManager) operationContext() context.Context {
	m.cfg.opsMu.Lock()
	defer m.cfg.opsMu.Unlock()
	if m.cfg.opsCtx == nil {
		m.cfg.opsCtx, m.cfg.opsCancel = context.WithCancel(context.Background())
	}
	return m.cfg.opsCtx
}

// ShutdownHostOperations cancels every in-flight operation's context, waits
// (bounded) for the workers to settle their records, and reports how many
// records the shutdown interrupted. It is the controller-shutdown half of §6's
// worker lifetime, and it must run before the SSH manager closes so a worker
// can finish its own cancellation path. A hub whose operation context was never
// created has nothing to cancel.
func (m *hubHostManager) ShutdownHostOperations(timeout time.Duration) int {
	m.cfg.opsMu.Lock()
	cancel := m.cfg.opsCancel
	m.cfg.opsMu.Unlock()
	if cancel == nil {
		return 0
	}
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.cfg.opsWG.Wait()
	}()
	if timeout <= 0 {
		timeout = operationsShutdownTimeout
	}
	select {
	case <-done:
	case <-time.After(timeout):
		m.logf("shutdown: in-flight host operations did not settle within %s", timeout)
	}
	return m.interruptLeftoverOperations()
}

// interruptLeftoverOperations moves any record this hub still holds
// pending/running to `interrupted` with the shutdown note. The workers do this
// for their own records as they observe cancellation; this pass is the
// belt-and-braces half for a worker that never started (a hub shutting down
// between the record write and the goroutine) and for a worker still parked in
// an un-cancellable call.
func (m *hubHostManager) interruptLeftoverOperations() int {
	if m.cfg.ops == nil {
		return 0
	}
	moved, err := m.cfg.ops.InterruptInFlight(operationsShutdownNote)
	if err != nil {
		m.logf("shutdown: in-flight host operations could not be marked interrupted: %v", err)
		return 0
	}
	return moved
}

// runOperationWorker executes one persisted operation to a terminal state,
// recording progress as it goes:
//
//  1. the fingerprint re-read immediately before the irreversible step (the
//     push, and again before a planned restart) — drift aborts as a recorded
//     `stale-entry` failure;
//  2. the 04b deploy path (or the 04b restart path), with its guards intact;
//  3. for a planned restart, the same 04b restart path the standalone operation
//     wraps, behind the same final fingerprint check;
//  4. the verified post-operation refresh: the channel-free one-shot preflight,
//     then the running probe over the attached channel, with §6's
//     process-instance and revision verification;
//  5. terminal success, or the failure recorded verbatim — never a clean
//     success the worker could not verify.
//
// The worker owns the host's gate from the record's creation to its terminal
// state (with the restart reattach deviation documented at the release point).
func (m *hubHostManager) runOperationWorker(ctx context.Context, work opWork) {
	released := false
	release := func() {
		if !released {
			released = true
			work.release()
		}
	}
	defer release()

	id := work.record.ID
	if canceled := m.workerCanceled(ctx); canceled {
		m.recordInterrupted(id)
		return
	}
	if _, err := m.cfg.ops.TransitionToState(id, hostops.StateRunning, nil, "operation started"); err != nil {
		m.logf("operation %s: could not mark running: %v", id, err)
	}
	m.recordProgress(id, fmt.Sprintf("%s operation for host %q started", work.record.Kind, work.entry.Name))

	restartFollows := false
	// beforeProbe is the pre-replacement running-state the post-operation
	// refresh compares the new process against: the step-(3) probe for a deploy,
	// and the worker's own pre-restart probe for a restart.
	beforeProbe := work.probe
	if work.record.Kind == hostops.KindDeploy {
		restartFollows = planRestartFollows(hubcore.HostRuntimeProbe{
			Version:          work.token.RunningVersion,
			RunningHealthy:   work.token.RunningHealthy,
			ProcessStartTime: work.token.ProcessStartTime,
		}, work.token.ControllerRevision)
	}

	if canceled := m.workerCanceled(ctx); canceled {
		m.recordInterrupted(id)
		return
	}
	// The irreversible steps, each behind a fresh fingerprint check.
	if work.record.Kind == hostops.KindDeploy {
		if err := m.checkOperationFingerprint(work.entry, work.token.HubTOMLFingerprint); err != nil {
			m.failOperation(ctx, id, err)
			return
		}
		facts, ok := m.attachedFactsFor(work.entry.Name)
		if !ok {
			m.failOperation(ctx, id, m.hostDetachedRefusal(work.entry.Name))
			return
		}
		m.recordProgress(id, "pushing the controller's build")
		_, afterDeploy, err := m.cfg.deployHost(ctx, work.entry, facts)
		if err != nil {
			m.failOperation(ctx, id, err)
			return
		}
		m.recordProgress(id, "the controller's build is installed and verified")
		if restartFollows {
			if err := m.checkOperationFingerprint(work.entry, work.token.HubTOMLFingerprint); err != nil {
				m.failOperation(ctx, id, err)
				return
			}
			if err := m.runRestartStep(ctx, id, work.entry, afterDeploy); err != nil {
				m.failOperation(ctx, id, err)
				return
			}
			restartFollows = true
		}
	} else {
		if err := m.checkOperationFingerprint(work.entry, work.fingerprint); err != nil {
			m.failOperation(ctx, id, err)
			return
		}
		// Restart has no pre-operation probe step of its own (no token, no
		// plan), so the worker takes the "pre-operation probe" value the
		// post-operation refresh compares against here, under the held gate,
		// presenting the epoch the record was created with.
		probe, err := m.probeBeforeRestart(ctx, work)
		if err != nil {
			m.failOperation(ctx, id, err)
			return
		}
		beforeProbe = probe
		facts, ok := m.attachedFactsFor(work.entry.Name)
		if !ok {
			m.failOperation(ctx, id, m.hostDetachedRefusal(work.entry.Name))
			return
		}
		if err := m.runRestartStep(ctx, id, work.entry, facts); err != nil {
			m.failOperation(ctx, id, err)
			return
		}
		restartFollows = true
	}
	if canceled := m.workerCanceled(ctx); canceled {
		m.recordInterrupted(id)
		return
	}

	// §6 seam (d) interim (S13 owns the operation-owned reattach): hold the gate
	// through the restart and its health verification above, then release it at
	// this one named point so the existing supervisor/reconnect can reattach,
	// wait for the channel, re-acquire the gate for the refresh probe, and
	// complete. Re-running the normal attach path while holding the gate would
	// deadlock on the non-reentrant gate, which is exactly why the spec's
	// attachUnderGate is the S13 deliverable this slice does not build.
	var refreshRelease func()
	if restartFollows {
		m.recordProgress(id, "restart verified; releasing the host gate so the existing reconnect can reattach (S13 owns the operation-owned reattach)")
		release()
		if _, ok := m.awaitOperationReattach(ctx, work.entry); !ok {
			m.failOperation(ctx, id, errors.New(
				"the host did not reattach within the refresh window after the restart, so the post-operation probe could not run"))
			return
		}
		if canceled := m.workerCanceled(ctx); canceled {
			m.recordInterrupted(id)
			return
		}
		releaseAgain, err := m.acquireOperationGate(ctx, work.entry.Name)
		if err != nil {
			m.failOperation(ctx, id, fmt.Errorf("re-acquiring the host gate for the post-operation probe failed: %w", err))
			return
		}
		refreshRelease = releaseAgain
		defer refreshRelease()
	}

	// §6's post-operation refresh: the verified channel-free preflight, then
	// the running probe over the attached channel, then the verification.
	if err := m.refreshOperation(ctx, id, work, beforeProbe, restartFollows); err != nil {
		m.failOperation(ctx, id, err)
		return
	}
	if canceled := m.workerCanceled(ctx); canceled {
		m.recordInterrupted(id)
		return
	}
	if _, err := m.cfg.ops.TransitionToState(id, hostops.StateComplete, &hostops.Result{
		OK: true, Message: "operation complete",
	}, "operation complete"); err != nil {
		m.logf("operation %s: could not mark complete: %v", id, err)
	}
}

// probeBeforeRestart runs the pre-restart running probe under the worker's held
// gate, presenting the operation's persisted fencing epoch. It is the
// "pre-operation probe" value the post-operation refresh compares against
// (§6's same-clock process-instance rule), and it names the build the restart
// must replace.
func (m *hubHostManager) probeBeforeRestart(ctx context.Context, work opWork) (hubcore.HostRuntimeProbe, error) {
	client, attached := m.attachedClient(work.entry)
	if !attached {
		return hubcore.HostRuntimeProbe{}, m.hostDetachedRefusal(work.entry.Name)
	}
	if m.cfg.planProbe == nil {
		return hubcore.HostRuntimeProbe{}, errors.New("the pre-restart running probe is not wired")
	}
	probe, err := m.cfg.planProbe(ctx, work.entry, client, work.epoch)
	if err != nil {
		return hubcore.HostRuntimeProbe{}, fmt.Errorf("the pre-restart running probe failed: %w", err)
	}
	return probe, nil
}

// runRestartStep runs the 04b restart path and records the progress line both
// the standalone and planned-restart callers share.
func (m *hubHostManager) runRestartStep(ctx context.Context, id string, entry hostreg.Host, facts sshconn.Preflight) error {
	m.recordProgress(id, "restarting the host")
	if err := m.cfg.restartHost(ctx, entry, facts); err != nil {
		return err
	}
	m.recordProgress(id, "restart verified healthy")
	return nil
}

// checkOperationFingerprint re-reads the host's own hub.toml entry fingerprint
// (§3) immediately before an irreversible step and compares it with the bound
// reference. Drift aborts as a recorded stale-entry failure — the read is a
// check, not an atomic compare-and-swap with external writers (§6's own
// caveat), so an edit landing right after it still drives the action and the
// post-operation refresh surfaces the drift.
func (m *hubHostManager) checkOperationFingerprint(entry hostreg.Host, bound string) error {
	fingerprint, ok := m.hostTOMLFingerprint(entry.Name)
	if !ok {
		return appwire.InternalError(fmt.Sprintf(
			"host %q's hub.toml entry could not be fingerprinted before an irreversible step", entry.Name))
	}
	if bound != "" && fingerprint != bound {
		return appwire.StaleEntry(appwire.StaleEntryBindingHubTOMLFingerprint, fmt.Sprintf(
			"stale-entry (hub.toml-fingerprint): host %q's hub.toml entry changed since this operation was confirmed; nothing irreversible ran", entry.Name))
	}
	return nil
}

// refreshOperation runs §6's post-operation refresh: the verified channel-free
// one-shot preflight, then a re-probe of the running build and health over the
// attached channel presenting the operation's persisted fencing epoch. It
// records the failure verbatim — never a clean success it could not verify —
// and returns nil only when the refresh verified the outcome.
func (m *hubHostManager) refreshOperation(ctx context.Context, id string, work opWork, beforeProbe hubcore.HostRuntimeProbe, restarted bool) error {
	if m.cfg.planFacts == nil {
		return errors.New("the post-operation preflight is not wired")
	}
	refreshFacts, err := m.cfg.planFacts(ctx, work.entry)
	if err != nil {
		return fmt.Errorf("the post-operation preflight failed: %w", err)
	}
	client, attached := m.attachedClient(work.entry)
	if !attached {
		return fmt.Errorf("%w: the host detached before the post-operation probe", errHostDetached)
	}
	if m.cfg.planProbe == nil {
		return errors.New("the post-operation running probe is not wired")
	}
	probe, err := m.cfg.planProbe(ctx, work.entry, client, work.epoch)
	if err != nil {
		return fmt.Errorf("the post-operation running probe failed: %w", err)
	}
	// The last-known publication is its own slice's store (registry spec §10);
	// this seam is where its verified facts land, and the residual is recorded
	// here rather than answered as if a store existed.
	if m.cfg.lastKnownPublish != nil {
		if err := m.cfg.lastKnownPublish(work.entry, probe, refreshFacts); err != nil {
			return fmt.Errorf("publishing the post-operation last-known state failed: %w", err)
		}
	}
	m.recordProgress(id, fmt.Sprintf("post-operation probe: build %q healthy=%t", probe.Version, probe.RunningHealthy))

	target := work.token.ControllerRevision
	if work.record.Kind == hostops.KindRestart {
		// A restart serves the build the host already had; the pre-operation
		// probe named it.
		target = beforeProbe.Version
	}
	if !probe.RunningHealthy {
		return fmt.Errorf("the post-operation probe reports the host unhealthy (build %q), so the operation is not a verified success", probe.Version)
	}
	if restarted {
		before := beforeProbe.ProcessStartTime
		if before == nil || probe.ProcessStartTime == nil {
			return fmt.Errorf("the post-operation probe cannot prove the new process replaced the old one: a process start time is absent (before=%v, after=%v)",
				before != nil, probe.ProcessStartTime != nil)
		}
		if probe.ProcessStartTime.Equal(*before) {
			return fmt.Errorf("the post-operation probe reports the same process start time (%s), so the restarted process was not proven to replace the old one",
				probe.ProcessStartTime.Format(time.RFC3339Nano))
		}
	}
	if !sshconn.UnverifiableVersion(target) && probe.Version != target {
		return fmt.Errorf("the post-operation probe reports build %q, want the %s build %q; the operation is not a verified success",
			probe.Version, work.record.Kind, target)
	}
	m.recordProgress(id, "post-operation refresh verified")
	return nil
}

// awaitOperationReattach waits (bounded by the manager's own refresh window)
// for the host's channel to be attached again after a restart released the
// gate. It is the interim half of §6 seam (d): the existing reconnect owns the
// reattach, and this waits for it rather than dialing.
func (m *hubHostManager) awaitOperationReattach(ctx context.Context, entry hostreg.Host) (*appwire.Client, bool) {
	if m.cfg.attachedFacts == nil || m.cfg.manager == nil {
		// No manager owns channels here (tests, embedders): the caller's own
		// reattach seam decides.
		if m.cfg.awaitReattach != nil {
			return m.cfg.awaitReattach(ctx, entry)
		}
		return nil, true
	}
	deadline := time.Now().Add(m.refreshWindow())
	for {
		if client, ok := m.attachedClient(entry); ok {
			return client, true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// acquireOperationGate re-acquires the host's gate for the post-operation probe
// after the restart's reattach window, retrying while the supervisor's own
// reconnect or another holder briefly owns it, bounded by the refresh window.
func (m *hubHostManager) acquireOperationGate(ctx context.Context, name string) (func(), error) {
	deadline := time.Now().Add(m.refreshWindow())
	for {
		release, err := m.cfg.gate.TryAcquire(name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: ""})
		if err == nil {
			return release, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// refreshWindow bounds the post-operation reattach wait and the gate
// re-acquisition. It is the probe timeout plus a connect allowance: the
// supervisor's reconnect is bounded by its own backoff, and the operation's
// record must reach a terminal state either way.
func (m *hubHostManager) refreshWindow() time.Duration {
	return 4 * hostProbeTimeoutFor(m.cfg.probeTimeout)
}

// workerCanceled reports whether the controller-lifetime context ended, and
// names the shutdown in the record.
func (m *hubHostManager) workerCanceled(ctx context.Context) bool {
	return ctx.Err() != nil
}

// recordInterrupted moves the record to `interrupted` with the shutdown note.
func (m *hubHostManager) recordInterrupted(id string) {
	if _, err := m.cfg.ops.TransitionToState(id, hostops.StateInterrupted, &hostops.Result{
		OK: false, Message: operationsShutdownNote,
	}, operationsShutdownNote); err != nil {
		m.logf("operation %s: could not record the shutdown interrupt: %v", id, err)
	}
}

// failOperation records the terminal outcome of a worker step that did not
// succeed: a plain step failure is recorded verbatim as `failed` (§6: the
// record carries what could not be verified), while a failure whose cause is
// the controller's own cancellation is the shutdown's `interrupted` — a client
// disconnect never reaches here, because the worker's context is not the RPC's.
func (m *hubHostManager) failOperation(ctx context.Context, id string, err error) {
	if ctx.Err() != nil {
		// The controller-lifetime context ended: a controller shutdown, never a
		// phase timeout (those arrive as a deadline error with this context
		// still live) and never a client disconnect (the worker never runs under
		// the RPC's context).
		m.recordInterrupted(id)
		return
	}
	message := err.Error()
	if _, terr := m.cfg.ops.TransitionToState(id, hostops.StateFailed, &hostops.Result{
		OK: false, Message: message,
	}, message); terr != nil {
		m.logf("operation %s: could not record its failure %q: %v", id, message, terr)
	}
}

// recordProgress appends one progress line; a failure to write it is logged,
// never fatal to the operation.
func (m *hubHostManager) recordProgress(id, message string) {
	if _, err := m.cfg.ops.AppendProgress(id, message); err != nil {
		m.logf("operation %s: could not append progress %q: %v", id, message, err)
	}
}

// EnsureDeploy records one Ensure-triggered deploy as a durable operation (§6:
// "Ensure-triggered operations are durable fenced operations"): it mints the
// server-side client operation ID, persists the record with its fencing epoch
// under the caller's held gate before the deploy's first remote write, and
// publishes the operation as the gate's holder so a contender's busy refusal is
// `host-busy-operation` naming the record (§12's "Ensure busy names its
// operation"). The returned finish records the step's outcome. A nil hook
// (no operation store wired) refuses, never running the deploy unrecorded.
func (m *hubHostManager) EnsureDeploy(host hostreg.Host) (func(error), error) {
	ops := m.cfg.ops
	if ops == nil {
		return nil, errors.New("the host operation store is not configured, so an Ensure-triggered deploy cannot be recorded; nothing was launched")
	}
	if strings.TrimSpace(m.cfg.bootID) == "" {
		return nil, errors.New("this hub carries no boot id, so an Ensure-triggered deploy cannot bind its fencing epoch; nothing was launched")
	}
	clientOperationID, err := newEnsureOperationID()
	if err != nil {
		return nil, err
	}
	record, _, err := ops.CreateOperation(hostops.OperationCreateRequest{
		ClientOperationID: clientOperationID,
		Host:              host.Name,
		Kind:              hostops.KindDeploy,
		Pair:              hostops.OperationPair{Generation: host.Generation, IncarnationID: host.IncarnationID},
		BootID:            m.cfg.bootID,
	})
	if err != nil {
		return nil, fmt.Errorf("persisting the Ensure-triggered deploy's operation record failed, so nothing was launched: %w", err)
	}
	if err := m.cfg.gate.HoldAs(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: record.ID}); err != nil {
		m.logf("host %q: the Ensure-triggered operation %s could not publish itself as the gate holder: %v",
			host.Name, record.ID, err)
	}
	m.recordProgress(record.ID, "Ensure-triggered deploy started")
	return func(err error) { m.finishEnsureDeploy(record.ID, err) }, nil
}

// finishEnsureDeploy records the Ensure deploy step's outcome: success is
// `complete`, a failure is `failed` with the cause verbatim, and a failure
// whose cause is the manager's own shutdown is `interrupted` — the same
// controller-lifetime rule the RPC-triggered workers follow. Competing
// concurrent terminal operations never share a call: the Ensure attempt is
// serialized by the host's gate.
func (m *hubHostManager) finishEnsureDeploy(id string, err error) {
	if m.cfg.ops == nil {
		return
	}
	switch {
	case err == nil:
		if _, terr := m.cfg.ops.TransitionToState(id, hostops.StateComplete, &hostops.Result{
			OK: true, Message: "Ensure-triggered deploy complete",
		}, "Ensure-triggered deploy complete"); terr != nil {
			m.logf("Ensure operation %s: could not mark complete: %v", id, terr)
		}
	case errors.Is(err, sshconn.ErrManagerClosed), errors.Is(err, context.Canceled):
		m.recordInterrupted(id)
	default:
		message := err.Error()
		if _, terr := m.cfg.ops.TransitionToState(id, hostops.StateFailed, &hostops.Result{
			OK: false, Message: message,
		}, message); terr != nil {
			m.logf("Ensure operation %s: could not record its failure %q: %v", id, message, terr)
		}
	}
}

// newEnsureOperationID mints the server-side client operation ID an
// Ensure-triggered deploy's record carries (§6). It is a fresh random value per
// attempt: Ensure retries are new operations, never replays, so the ID is
// unique rather than idempotent.
func newEnsureOperationID() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("minting the Ensure-triggered deploy's operation id failed: %w", err)
	}
	return "ensure-" + hex.EncodeToString(nonce), nil
}
