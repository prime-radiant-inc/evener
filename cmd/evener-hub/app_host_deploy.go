package hub

// evener/host/deploy and evener/host/restart (deploy pipeline 08b §6, §10, §11):
// the operation model behind the plan's confirmation token. Both handlers run
// the same fixed processing order — dedup first, the provisional token check
// and remnant fence, the per-host gate with the gated probe and under-gate
// re-resolution, then the atomic durable record write — and both hand their
// long-running work to a worker under the controller-lifetime context, never
// the RPC's.
//
// Seams:
//
//   - remnants: the remnant fence (m.remnantOpen) is consulted exactly where §6
//     step 2 puts it — past dedup, before any probe or acquisition — and
//     renders the typed `remnant-open` arm.
//   - restart reattach: §6 seam (d) is operation-owned — the worker keeps the
//     host's gate across the restart's channel drop, reattaches through the
//     manager's gate-aware `attachUnderGate` primitive (which suppresses
//     supervisor startup), re-probes over the reattached channel, and hands the
//     supervisor off under the still-held gate. An initially unattached restart
//     attach-firsts the same way and names the path in the record. The reattach
//     tolerates the retryable attach classes within the refresh window — a
//     restarted host may refuse dials while it comes back up — and fails fast
//     on terminal causes.
//   - the fencing epoch: persisted and presented before each probe, per the
//     deploy pipeline's own §6/§10 subset (hostops/probeepoch.go).

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// remnantOpen reports the open remnant fencing name, if one does. §6 step 2
// puts the refusal past dedup and before any probe or acquisition.
func (m *hubHostManager) remnantOpen(name string) string {
	remnantID, _ := m.openRemnantID(name)
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
	ops := m.opsStore()
	if ops == nil {
		return hostops.Record{}, appwire.InternalError("the host operation store is not configured, so no operation can be recorded")
	}
	pair := hostops.OperationPair{Generation: entry.Generation, IncarnationID: entry.IncarnationID}

	query, replay, err := m.admitOperation(entry, req, pair)
	if err != nil {
		return hostops.Record{}, err
	}
	if replay != nil {
		return *replay, nil
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
	if req.kind == hostops.KindDeploy && !attached {
		// §6's detached-during-deploy rule: deploy must not consume its
		// single-use token to open a worker that then attach-firsts into an
		// unvalidated channel.
		return hostops.Record{}, hostDetachedRefusal(entry.Name)
	}

	// sequenceBefore is §6 step 3's probe-window position: any terminal
	// transition above it — seen by the local scan below or by the consume
	// write's own locked read — is a concurrent-terminal-op refusal.
	sequenceBefore := ops.Sequence()

	// The deploy path's durable probe epoch precedes the first probe and is
	// promoted by the consume. Every refusal past this point drops it; a dropped
	// epoch that the consume already promoted is a no-op, and a restart has none.
	var epoch hostops.ProbeEpoch
	defer func() {
		if !handedOff {
			m.dropProbeEpoch(entry.Name, epoch)
		}
	}()
	var probe hubcore.HostRuntimeProbe
	var facts sshconn.Preflight
	if req.kind == hostops.KindDeploy {
		epoch, probe, facts, err = m.probeDeployUnderGate(ctx, entry, client)
		if err != nil {
			return hostops.Record{}, err
		}
	}

	// The under-gate re-resolution: a mutation can legally commit between the
	// resolution above and this gate.
	if err := m.revalidateUnderGate(entry, req, probe, facts); err != nil {
		return hostops.Record{}, err
	}

	// The probe window closes with the local scan: an operation that finished
	// while the probe ran is a re-plan refusal. The consume write re-runs the
	// same check inside its locked read, so nothing can slip between.
	if id, landed := ops.TerminalOperationSince(entry.Name, sequenceBefore); landed {
		return hostops.Record{}, appwire.StaleEntry(appwire.StaleEntryBindingConcurrentTerminalOp, fmt.Sprintf(
			"host %q completed operation %q while this operation was being resolved; retry", entry.Name, id))
	}

	outcome, err := m.createOperationRecord(req, entry, pair, query, sequenceBefore)
	if err != nil {
		return hostops.Record{}, err
	}
	if outcome.Replayed {
		// A concurrent call with this operation ID created the record while this
		// one waited for the gate: answer with that record, launch no worker,
		// and leave the token (deploy) and any persisted probe epoch alone.
		return outcome.Record, nil
	}
	// outcome.Token is the token row the atomic write consumed: its bindings
	// (the target, the entry fingerprint, the controller revision, the running
	// state) are the worker's references for the operation's whole life, so the
	// worker must carry them from this write rather than re-reading a row the
	// write just deleted.
	created := outcome.Record
	consumed := outcome.Token

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
		work.token = consumed
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

// admitOperation is the start path's pre-gate half: §6 steps 1 and 2. It
// returns the dedup query the atomic write re-runs, and the existing record when
// the request is a replay.
func (m *hubHostManager) admitOperation(entry hostreg.Host, req operationRequest, pair hostops.OperationPair) (hostops.OperationDedupQuery, *hostops.Record, error) {
	ops := m.opsStore()

	// (1) Dedup first: a hit returns the record with no token validation and no
	// consumption, so a lost-response replay succeeds without a fresh token and
	// without acquiring a contested gate.
	query := m.operationDedupQuery(entry, req, pair)
	record, hit, err := ops.LookupOperation(query)
	if err != nil {
		return query, nil, operationRefusal(entry.Name, err)
	}
	if hit {
		return query, &record, nil
	}

	// (2) The provisional token check (deploy) is a fail-fast readability check
	// only: nothing is decided here, and a concurrent plan can supersede the
	// value before the gate is acquired. Then the remnant fence: a fenced name
	// never reaches the gate.
	if req.kind == hostops.KindDeploy {
		if _, err := ops.ValidateToken(entry.Name, req.tokenValue); err != nil {
			return query, nil, tokenRefusalWire(entry.Name, err)
		}
	}
	if remnantID := m.remnantOpen(entry.Name); remnantID != "" {
		return query, nil, appwire.RemnantOpen(remnantID, fmt.Sprintf(
			"host %q has an open teardown remnant; resume it through teardown-retry before deploying or restarting", entry.Name))
	}
	return query, nil, nil
}

// operationDedupQuery is the identity a retry of req is looked up by.
func (m *hubHostManager) operationDedupQuery(entry hostreg.Host, req operationRequest, pair hostops.OperationPair) hostops.OperationDedupQuery {
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
		if row, ok := m.opsStore().OutstandingToken(entry.Name); ok {
			intended := hostops.OperationPair{Generation: row.Generation, IncarnationID: row.IncarnationID}
			query.Intended = &intended
		}
	case hostops.KindRestart:
		query.Intended = req.intended
	}
	return query
}

// probeDeployUnderGate is a deploy's §6 step 3 under the held gate: its own
// atomic store write persists the probe epoch bound to the pair, then the gated
// running probe presents it, then the attached channel's captured preflight is
// read for the deployment's target re-resolution and running-state checks (the
// deploy path runs no fresh preflight of its own). The epoch is returned even
// on a refusal so the caller can drop it.
func (m *hubHostManager) probeDeployUnderGate(ctx context.Context, entry hostreg.Host, client *appwire.Client) (hostops.ProbeEpoch, hubcore.HostRuntimeProbe, sshconn.Preflight, error) {
	epoch, err := m.persistProbeEpoch(entry)
	switch {
	case err != nil && !hostops.RenameLanded(err):
		return epoch, hubcore.HostRuntimeProbe{}, sshconn.Preflight{}, appwire.ProbeFailed(entry.Name, appwire.ProbeFailureReadFailed, fmt.Sprintf(
			"host %q: the durable probe epoch could not be persisted, so nothing was probed: %v", entry.Name, err))
	case err != nil:
		m.logf("host %q: the probe epoch's directory sync failed after its rename landed; the epoch is durable: %v", entry.Name, err)
	}
	probe, err := m.probeForOperation(ctx, entry, client, epoch)
	if err != nil {
		return epoch, probe, sshconn.Preflight{}, m.operationProbeRefusal(entry.Name, err)
	}
	facts, ok := m.attachedFactsFor(entry.Name)
	if !ok {
		return epoch, probe, sshconn.Preflight{}, hostDetachedRefusal(entry.Name)
	}
	return epoch, probe, facts, nil
}

// createOperationRecord is §6 step 4's atomic write. For a deploy it re-reads
// and consumes the token and promotes the probe epoch; for a restart it creates
// the record with its freshly minted epoch. Either way the record exists before
// the RPC answers, and the worker that follows owns the gate release.
// The in-write dedup query closes the window between the pre-gate lookup and
// this write: a concurrent call with the same client operation ID that won the
// gate first is answered with its record instead of a second one (§4 holds one
// store mutex across the lookup that leads to a write).
func (m *hubHostManager) createOperationRecord(req operationRequest, entry hostreg.Host, pair hostops.OperationPair, query hostops.OperationDedupQuery, sequenceBefore uint64) (hostops.OperationCreateOutcome, error) {
	var outcome hostops.OperationCreateOutcome
	var err error
	switch req.kind {
	case hostops.KindDeploy:
		outcome, err = m.opsStore().ConsumeTokenAndCreateOperation(hostops.OperationCreateRequest{
			ClientOperationID: req.clientOperationID,
			Host:              entry.Name,
			Kind:              hostops.KindDeploy,
			Pair:              pair,
			TokenValue:        req.tokenValue,
			SequenceBefore:    sequenceBefore,
			Dedup:             &query,
		})
	case hostops.KindRestart:
		outcome, err = m.opsStore().CreateOperation(hostops.OperationCreateRequest{
			ClientOperationID: req.clientOperationID,
			Host:              entry.Name,
			Kind:              hostops.KindRestart,
			Pair:              pair,
			BootID:            m.cfg.bootID,
			SequenceBefore:    sequenceBefore,
			Dedup:             &query,
		})
	}
	if err != nil && !hostops.RenameLanded(err) {
		return outcome, operationRefusal(entry.Name, err)
	}
	if err != nil {
		// The rename landed, so the record is durable (see hostops.RenameLanded):
		// the operation proceeds and the sync trouble is logged.
		m.logf("host %q: operation %s's directory sync failed after its rename landed; the record is durable: %v",
			entry.Name, outcome.Record.ID, err)
	}
	return outcome, nil
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
		return hostDetachedRefusal(name)
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
func hostDetachedRefusal(name string) error {
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
	if _, attached := m.attachedClient(entry); !attached && req.kind == hostops.KindDeploy {
		// The deploy's detached-during-deploy rule. A restart proceeds without a
		// channel: §6's attach-first arm attaches the host under this same held
		// gate.
		return hostDetachedRefusal(entry.Name)
	}
	fingerprint, ok := m.hostTOMLFingerprint(entry.Name)
	if !ok {
		return appwire.InternalError(fmt.Sprintf(
			"host %q's hub.toml entry could not be fingerprinted: the selected hub.toml (%s) does not validate; fix the file before deploying",
			entry.Name, m.cfg.configPath))
	}
	if req.kind == hostops.KindDeploy {
		ops := m.opsStore()
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
	if _, ok := errors.AsType[*hostops.StaleEntryError](err); ok {
		return staleEntryWire(name, err)
	}
	if errors.Is(err, hostops.ErrTokenMissing) || errors.Is(err, hostops.ErrTokenMismatched) ||
		errors.Is(err, hostops.ErrTokenSuperseded) || errors.Is(err, hostops.ErrTokenExpired) {
		return tokenRefusalWire(name, err)
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

// epochOf renders a record's fencing epoch for the wire. The store owns the
// epoch's encoding (hostops.Record.FencingEpochValue); a record without a
// usable epoch reads as the zero epoch, which the probe refuses — never an
// unfenced write.
func epochOf(record hostops.Record) appwire.FencingEpoch {
	epoch, ok := record.FencingEpochValue()
	if !ok {
		return appwire.FencingEpoch{}
	}
	return appwire.FencingEpoch{BootID: epoch.BootID, OpSeq: epoch.OpSeq}
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
	if m.opsStore() == nil {
		return 0
	}
	moved, err := m.opsStore().InterruptInFlight(operationsShutdownNote)
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
// state, the restart's reattach included (§6 seam (d): the reattach is
// operation-owned and runs under the same held gate).
func (m *hubHostManager) runOperationWorker(ctx context.Context, work opWork) {
	defer work.release()

	run := &opRun{m: m, ctx: ctx, work: work, id: work.record.ID}
	defer func() { run.handOff("as the operation ended") }()

	if m.interruptIfShuttingDown(ctx, run.id) {
		return
	}
	if _, err := m.opsStore().TransitionToState(run.id, hostops.StateRunning, nil, fmt.Sprintf(
		"%s operation for host %q started", work.record.Kind, work.entry.Name)); err != nil {
		m.logf("operation %s: could not mark running: %v", run.id, err)
	}

	plan := planOperationRun(work)
	beforeMutation := m.operationEntryValidator(work.entry, plan.fingerprint)

	if m.interruptIfShuttingDown(ctx, run.id) {
		return
	}
	// The irreversible steps, each behind a fresh fingerprint check.
	var err error
	if work.record.Kind == hostops.KindDeploy {
		err = run.deploy(&plan, beforeMutation)
	} else {
		err = run.restart(&plan, beforeMutation)
	}
	if err != nil {
		run.fail(err)
		return
	}
	if m.interruptIfShuttingDown(ctx, run.id) {
		return
	}
	run.verify(plan)
}

// opRun is one worker's state: the persisted operation it drives and the
// supervisor handoff it owes for the channel an attach left live.
type opRun struct {
	m    *hubHostManager
	ctx  context.Context
	work opWork
	id   string
	// handoff is the supervisor handoff for the channel a restart left live:
	// the attach-first attach's, superseded by the post-restart reattach's once
	// that replacement is published. It is invoked exactly once, always under
	// the still-held gate — explicitly at terminal verification, or by the
	// deferred finish in runOperationWorker on any earlier exit — so a restart
	// that fails after an attach-first can never strand an attached channel
	// without a supervisor.
	handoff   func() bool
	handedOff bool
}

// opPlan is the state the deploy and restart flows hand to the verification.
type opPlan struct {
	// fingerprint is the hub.toml entry fingerprint every irreversible step
	// re-reads against.
	fingerprint    string
	restartFollows bool
	// beforeProbe is the pre-replacement running-state the post-operation
	// refresh compares the new process against: the step-(3) probe for a deploy,
	// and the worker's own pre-restart probe for a restart.
	beforeProbe hubcore.HostRuntimeProbe
	// targetVersion is the requested build, separate from that predecessor's
	// identity: a standalone restart can finish an earlier deployment to another
	// build.
	targetVersion string
	startedAt     time.Time
}

// planOperationRun is the plan an operation starts with, before either flow has
// run: a deploy decides from its token's bindings whether a restart follows.
func planOperationRun(work opWork) opPlan {
	plan := opPlan{
		fingerprint:   work.fingerprint,
		beforeProbe:   work.probe,
		targetVersion: work.token.ControllerRevision,
	}
	if work.record.Kind == hostops.KindDeploy {
		plan.fingerprint = work.token.HubTOMLFingerprint
		plan.restartFollows = planRestartFollows(hubcore.HostRuntimeProbe{
			Version:          work.token.RunningVersion,
			RunningHealthy:   work.token.RunningHealthy,
			ProcessStartTime: work.token.ProcessStartTime,
		}, work.token.ControllerRevision)
	}
	return plan
}

// handOff starts the supervisor for the channel the operation left live, once.
func (r *opRun) handOff(when string) bool {
	if r.handedOff || r.handoff == nil {
		return true
	}
	r.handedOff = true
	if r.handoff() {
		return true
	}
	r.m.logf("operation %s: the host's supervisor could not be started %s; the host may be left without automatic reconnect", r.id, when)
	return false
}

// fail records a terminal failure, first handing the channel a completed
// attach left to its supervisor under the still-held gate. A handoff that
// cannot start the supervisor is joined into the recorded failure instead
// of being masked by it, so an attached-but-unsupervised host is visible in
// the record. The deferred finish in runOperationWorker stays the backstop for
// paths that return without recording a failure (an interrupted shutdown).
func (r *opRun) fail(err error) {
	if !r.handOff("as the operation failed") {
		err = errors.Join(err, errors.New("the host's supervisor could not be started, so the host may be attached without automatic reconnect"))
	}
	r.m.failOperation(r.ctx, r.id, err)
}

// deploy pushes the controller's build, then runs the planned restart when the
// token's bindings call for one.
func (r *opRun) deploy(plan *opPlan, beforeMutation func() error) error {
	m, id, entry := r.m, r.id, r.work.entry
	if err := beforeMutation(); err != nil {
		return err
	}
	if m.cfg.deployHost == nil {
		return errors.New("this hub has no deploy step wired, so the operation cannot run")
	}
	facts, ok := m.attachedFactsFor(entry.Name)
	if !ok {
		return hostDetachedRefusal(entry.Name)
	}
	m.recordProgress(id, "pushing the controller's build")
	_, afterDeploy, err := m.cfg.deployHost(r.ctx, entry, facts, plan.restartFollows, beforeMutation, &sshconn.DeploymentJournal{
		OperationID: id,
		Supersede: func(priorID string) error {
			prior, found := m.opsStore().Record(priorID)
			if !found {
				return hostops.ErrRecordNotFound
			}
			note := fmt.Sprintf("supersedes pending intent: priorOperationId=%s host=%q generation=%d incarnationId=%q", prior.ID, prior.Host, prior.Generation, prior.IncarnationID)
			_, err := m.opsStore().AppendProgress(id, note)
			return err
		},
	})
	if err != nil {
		return err
	}
	m.recordProgress(id, "the controller's build is installed and verified")
	if !plan.restartFollows {
		return nil
	}
	if err := beforeMutation(); err != nil {
		return err
	}
	_, err = m.runRestartStep(r.ctx, id, entry, afterDeploy, beforeMutation)
	return err
}

// restart runs a standalone restart: first any deployment repair a pending
// installation authorized, then, when that left no replacement to verify, the
// attach-first restart.
func (r *opRun) restart(plan *opPlan, beforeMutation func() error) error {
	m, id, entry := r.m, r.id, r.work.entry
	if err := beforeMutation(); err != nil {
		return err
	}
	if m.cfg.recoverDeployment != nil {
		result, err := m.cfg.recoverDeployment(r.ctx, entry, hostops.Holder{Kind: hostops.HolderOperation, OperationID: id}, beforeMutation)
		if err != nil {
			return err
		}
		if result != nil {
			// The pending installation already authorized repair. Recovery has
			// replaced its original predecessor under this persisted operation,
			// not attached a predecessor that needs another restart. Keep that
			// identity for the independent epoch-bearing post-reattach probe.
			plan.beforeProbe.ProcessStartTime = nil
			if !result.ReplacedStartedAt.IsZero() {
				plan.beforeProbe.ProcessStartTime = &result.ReplacedStartedAt
			}
			plan.targetVersion = result.TargetVersion
			plan.startedAt = result.StartedAt
			plan.restartFollows = true
			m.recordProgress(id, "the pending deployment was recovered and its replacement verified before attachment")
		}
	}
	if plan.restartFollows {
		return nil
	}
	return r.attachFirstRestart(plan, beforeMutation)
}

// attachFirstRestart is §6's attach-first arm: a restart with no usable
// attached channel — the handler saw none, or the link dropped before this
// worker ran — attaches the host under the held gate first, and names the path
// in the record. The pre-restart probe then reads the identity of the process
// the restart must replace over that channel. The handoff from this attach is
// provisional: the reattach in verify supersedes it when its replacement
// publishes, and an earlier exit hands this channel off through fail() before
// the failure is recorded — the deferred finish is the backstop for an exit
// that records no failure — so a failed probe or restart cannot strand it.
func (r *opRun) attachFirstRestart(plan *opPlan, beforeMutation func() error) error {
	m, id, entry := r.m, r.id, r.work.entry
	if _, attached := m.attachedClient(entry); !attached {
		h, err := m.attachOperationChannel(r.ctx, id, entry,
			"attach-first: the host had no usable attached channel, so the restart attaches it under the held host gate", true)
		if h != nil {
			// A published channel keeps its handoff even when the call
			// reports a failure, so it is never left unsupervised.
			r.handoff = h
		}
		if err != nil {
			return err
		}
	}
	// Restart has no pre-operation probe step of its own (no token, no
	// plan), so the worker takes the "pre-operation probe" value the
	// post-operation refresh compares against here, under the held gate,
	// presenting the epoch the record was created with.
	probe, err := m.probeBeforeRestart(r.ctx, r.work)
	if err != nil {
		return err
	}
	plan.beforeProbe = probe
	facts, ok := m.attachedFactsFor(entry.Name)
	if !ok {
		return hostDetachedRefusal(entry.Name)
	}
	plan.targetVersion, err = m.runRestartStep(r.ctx, id, entry, facts, beforeMutation)
	if err != nil {
		return err
	}
	plan.restartFollows = true
	return nil
}

// verify finishes an operation whose irreversible steps ran: the operation-owned
// reattach, §6's post-operation refresh, the supervisor handoff, and the
// terminal record.
func (r *opRun) verify(plan opPlan) {
	m, id, entry := r.m, r.id, r.work.entry
	// §6 seam (d): the operation-owned reattach. The worker retains the host's
	// gate across the restart's channel drop and re-runs the attach dialing
	// closure through the gate-aware primitive, which accepts the already-held
	// gate and suppresses supervisor startup. The post-operation refresh below
	// probes the reattached channel, and the handoff that follows it starts the
	// supervisor under the still-held gate. Re-running the normal attach path
	// while holding the gate is a deadlock: the gate is non-reentrant.
	// A retryable attach failure — a host still coming back up — is retried
	// within the refresh window, and a terminal cause fails fast; see
	// reattachOperationChannel.
	if plan.restartFollows {
		h, err := m.reattachOperationChannel(r.ctx, id, entry)
		if h != nil {
			// A published replacement keeps its handoff even when the call
			// reports an error, so it is never left unsupervised.
			r.handoff = h
		}
		if err != nil {
			r.fail(err)
			return
		}
	}

	// §6's post-operation refresh: the verified channel-free preflight, then
	// the running probe over the attached (reattached) channel, then the
	// verification.
	refreshErr := m.refreshOperation(r.ctx, id, r.work, plan.beforeProbe, plan.targetVersion, plan.restartFollows, plan.startedAt)
	// The suppress-supervisor scope ends at verification: hand the channel back
	// to a supervisor under the still-held gate, whether or not the refresh
	// verified the outcome — the operation is over either way, and the host
	// must keep automatic reconnect after the restart. A failed handoff is
	// recorded alongside a failed refresh rather than masked by it.
	var handoffErr error
	if !r.handOff("after the restart verification") {
		handoffErr = errors.New("the host's supervisor could not be started after the restart, so the host is attached without automatic reconnect")
	}
	if m.interruptIfShuttingDown(r.ctx, id) {
		return
	}
	if refreshErr != nil || handoffErr != nil {
		r.fail(errors.Join(refreshErr, handoffErr))
		return
	}
	m.recordTerminal(id, hostops.StateComplete, true, "operation complete")
}

// probeBeforeRestart runs the pre-restart running probe under the worker's held
// gate, presenting the operation's persisted fencing epoch. It is the
// "pre-operation probe" value the post-operation refresh compares against
// (§6's same-clock process-instance rule), and it names the build the restart
// must replace.
func (m *hubHostManager) probeBeforeRestart(ctx context.Context, work opWork) (hubcore.HostRuntimeProbe, error) {
	client, attached := m.attachedClient(work.entry)
	if !attached {
		return hubcore.HostRuntimeProbe{}, hostDetachedRefusal(work.entry.Name)
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

// runRestartStep runs the 04b restart path, returning its selected target build,
// and records the progress line both standalone and planned restarts share.
func (m *hubHostManager) runRestartStep(ctx context.Context, id string, entry hostreg.Host, facts sshconn.Preflight, beforeMutation func() error) (string, error) {
	if m.cfg.restartHost == nil {
		return "", errors.New("this hub has no restart step wired, so the operation cannot run")
	}
	m.recordProgress(id, "restarting the host")
	target, err := m.cfg.restartHost(ctx, entry, facts, beforeMutation)
	if err != nil {
		return "", err
	}
	m.recordProgress(id, "restart verified healthy")
	return target, nil
}

// The SSH manager may retain this validator after the worker finishes. Capture
// only a copied entry, its fingerprint and the long-lived hub manager.
func (m *hubHostManager) operationEntryValidator(entry hostreg.Host, fingerprint string) func() error {
	entry.Roots = slices.Clone(entry.Roots)
	return func() error { return m.checkOperationFingerprint(entry, fingerprint) }
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
			"stale-entry (hub.toml-fingerprint): host %q's hub.toml entry changed since this operation was confirmed; the next irreversible step was refused", entry.Name))
	}
	return nil
}

// refreshOperation runs §6's post-operation refresh: the verified channel-free
// one-shot preflight, then a re-probe of the running build and health over the
// attached channel presenting the operation's persisted fencing epoch. It
// records the failure verbatim — never a clean success it could not verify —
// and returns nil only when the refresh verified the outcome.
func (m *hubHostManager) refreshOperation(ctx context.Context, id string, work opWork, beforeProbe hubcore.HostRuntimeProbe, target string, restarted bool, startedAt time.Time) error {
	if m.cfg.planFacts == nil {
		return errors.New("the post-operation preflight is not wired")
	}
	_, err := m.cfg.planFacts(ctx, work.entry)
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
	m.recordProgress(id, fmt.Sprintf("post-operation probe: build %q healthy=%t", probe.Version, probe.RunningHealthy))

	if !probe.RunningHealthy {
		return fmt.Errorf("the post-operation probe reports the host unhealthy (build %q), so the operation is not a verified success", probe.Version)
	}
	if !startedAt.IsZero() {
		// An owned start or unavailable predecessor can lack a start time. The
		// proof is sshconn's verified successor, independently observed at this epoch.
		if probe.ProcessStartTime == nil || !probe.ProcessStartTime.Equal(startedAt) {
			return errors.New("the post-operation probe did not identify the verified startup process")
		}
	}
	if restarted && (startedAt.IsZero() || beforeProbe.ProcessStartTime != nil) {
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

// attachOperationChannel runs one §6 operation-owned attach attempt under the
// caller's held gate: where a channel manager is wired, it records the progress
// line naming the path (the attach-first arm's entry), then hands the work to
// the gate-aware primitive, which never re-acquires the non-reentrant gate.
// The returned handoff starts the channel's supervisor under the same held gate
// after the post-operation verification.
func (m *hubHostManager) attachOperationChannel(ctx context.Context, id string, entry hostreg.Host, progress string, explicit bool) (func() bool, error) {
	if m.cfg.attachUnderGate == nil {
		// No manager owns channels here (tests, embedders): there is nothing to
		// reattach, so the arm is the no-op success the interim wait had — and
		// no progress line is recorded, because the record must not claim an
		// attach that never ran. The post-operation refresh's own attachment
		// check still decides whether the operation can complete.
		return func() bool { return true }, nil
	}
	m.recordProgress(id, progress)
	return m.attachUnderGateOnce(ctx, id, entry, explicit)
}

// attachUnderGateOnce runs one operation-owned attach attempt through the
// gate-aware primitive. The record's holder promotion is logged, never fatal
// (the record is durable and the worker still runs), but the gate-aware
// primitive requires the gate to carry the operation holder the worker
// presents, so it is re-asserted before every attempt — a promotion that could
// not publish must not turn into a failed operation, and a retry must present
// the same holder the first try did.
func (m *hubHostManager) attachUnderGateOnce(ctx context.Context, id string, entry hostreg.Host, explicit bool) (func() bool, error) {
	holder := hostops.Holder{Kind: hostops.HolderOperation, OperationID: id}
	if err := m.cfg.gate.HoldAs(entry.Name, holder); err != nil {
		return nil, fmt.Errorf("the operation holder could not be published on host %q's gate, so the attach under it cannot run: %w", entry.Name, err)
	}
	handoff, err := m.cfg.attachUnderGate(ctx, entry, holder, explicit)
	if err != nil {
		return nil, fmt.Errorf("the operation-owned attach for host %q failed: %w", entry.Name, err)
	}
	// The attach ladder can run an Ensure-triggered deploy, whose recorder
	// promotes the gate to the inner operation and whose finish restores the
	// manager holder (finishEnsureOperation). The returned handoff requires the
	// gate to carry this operation's holder when it runs, so re-assert it after
	// the attach: a holder change performed inside the ladder must not make the
	// handoff refuse and leave the published channel unsupervised.
	if err := m.cfg.gate.HoldAs(entry.Name, holder); err != nil {
		// The channel is already published; return its handoff alongside the
		// error so the caller can still invoke it, rather than discarding the
		// only closure that would start its supervisor.
		return handoff, fmt.Errorf("the operation holder could not be restored after the attach for host %q: %w", entry.Name, err)
	}
	return handoff, nil
}

// operationPollInterval is how often the worker re-runs a bounded operation
// step whose condition the host may resolve on its own — today the reattach of
// a restarted host — inside the refresh window. It is the interval the
// pre-seam-(d) interim wait used.
const operationPollInterval = 250 * time.Millisecond

// refreshWindow bounds the worker's post-restart reattach tolerance. It is the
// pre-seam-(d) interim's bound, recovered as-is: the probe timeout plus a
// connect allowance — long enough for a restarted host to accept dials again,
// short enough that the operation's record still reaches a terminal state
// either way.
func (m *hubHostManager) refreshWindow() time.Duration {
	return 4 * hostProbeTimeoutFor(m.cfg.probeTimeout)
}

// reattachOperationChannel runs §6 seam (d)'s post-restart reattach with the
// interim's tolerance for the retryable class restored: under the held gate the
// dropped channel's supervisor cannot reconnect (the gate is non-reentrant), so
// a host still coming back up after a reboot can refuse the dial with a
// retryable transport error, and failing the whole restart on the first one is
// harsher than the shape seam (d) replaced. Retryable failures are retried
// within refreshWindow; a terminal cause (sshconn.Terminal's class) fails fast
// exactly as the attach ladder classes it; an exhausted window reports the
// honest failure. The progress line is recorded once, before the first
// attempt — retries must not look like new steps in the record.
func (m *hubHostManager) reattachOperationChannel(ctx context.Context, id string, entry hostreg.Host) (func() bool, error) {
	if m.cfg.attachUnderGate == nil {
		// No manager owns channels here (tests, embedders): the same no-op
		// success the attach arm is, and no progress line is recorded for an
		// attach that never ran.
		return func() bool { return true }, nil
	}
	m.recordProgress(id, "restart verified; reattaching the host under the held host gate")
	deadline := time.Now().Add(m.refreshWindow())
	var last error
	for {
		handoff, err := m.attachUnderGateOnce(ctx, id, entry, false)
		if err == nil {
			return handoff, nil
		}
		last = err
		// A published channel keeps its handoff: the caller must still hand it
		// off, and a channel that already published is not a dial to retry. A
		// terminal cause and an ended context are the other no-retry arms.
		if handoff != nil || sshconn.Terminal(err) || ctx.Err() != nil {
			return handoff, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("the host did not reattach within the refresh window after the restart, so the post-operation probe could not run: %w", last)
		}
		select {
		case <-ctx.Done():
			return nil, last
		case <-time.After(operationPollInterval):
		}
	}
}

// interruptIfShuttingDown reports whether the controller-lifetime context has
// ended, and — when it has — moves the record to `interrupted` with the
// shutdown note first. Every worker wait point runs it, so a shutdown can never
// leave a record pending/running: the operation's outcome is unknown, and the
// note names why.
func (m *hubHostManager) interruptIfShuttingDown(ctx context.Context, id string) bool {
	if ctx.Err() == nil {
		return false
	}
	m.recordTerminal(id, hostops.StateInterrupted, false, operationsShutdownNote)
	return true
}

// recordTerminal writes one terminal outcome — its state, result and progress
// line — in one atomic store write, logging a failure to record it. Every
// terminal path (worker success, worker failure, shutdown, Ensure) goes through
// here so their records cannot drift apart.
func (m *hubHostManager) recordTerminal(id string, state hostops.State, ok bool, message string) {
	if _, err := m.opsStore().TransitionToState(id, state, &hostops.Result{
		OK: ok, Message: message,
	}, message); err != nil {
		m.logf("operation %s: could not record its %s outcome %q: %v", id, state, message, err)
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
		m.recordTerminal(id, hostops.StateInterrupted, false, operationsShutdownNote)
		return
	}
	m.recordTerminal(id, hostops.StateFailed, false, err.Error())
}

// recordProgress appends one progress line; a failure to write it is logged,
// never fatal to the operation.
func (m *hubHostManager) recordProgress(id, message string) {
	if _, err := m.opsStore().AppendProgress(id, message); err != nil {
		m.logf("operation %s: could not append progress %q: %v", id, message, err)
	}
}

// EnsureDeploy records one Ensure-triggered deploy as a durable operation (§6:
// "Ensure-triggered operations are durable fenced operations"): it mints the
// server-side client operation ID, persists the record with its fencing epoch
// under the caller's held gate before the deploy's first remote write, and
// publishes the operation as the gate's holder so a contender's busy refusal is
// `host-busy-operation` naming the record (§12's "Ensure busy names its
// operation"). The returned finish records the step's outcome. A nil hook (no
// operation store wired) refuses, never running the deploy unrecorded.
func (m *hubHostManager) EnsureDeploy(host hostreg.Host) (func(error), error) {
	ops := m.opsStore()
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
	outcome, err := ops.CreateOperation(hostops.OperationCreateRequest{
		ClientOperationID: clientOperationID,
		Host:              host.Name,
		Kind:              hostops.KindDeploy,
		Pair:              hostops.OperationPair{Generation: host.Generation, IncarnationID: host.IncarnationID},
		BootID:            m.cfg.bootID,
		// The caller holds the host's gate, so this read is the pre-operation
		// position: an operation that finished before the Ensure attempt started
		// must not refuse the attempt (only one that lands while it runs).
		SequenceBefore: ops.Sequence(),
	})
	if err != nil {
		return nil, fmt.Errorf("persisting the Ensure-triggered deploy's operation record failed, so nothing was launched: %w", err)
	}
	record := outcome.Record
	if err := m.cfg.gate.HoldAs(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: record.ID}); err != nil {
		m.logf("host %q: the Ensure-triggered operation %s could not publish itself as the gate holder: %v",
			host.Name, record.ID, err)
	}
	return func(err error) { m.finishEnsureOperation(record.ID, host.Name, hostops.KindDeploy, err) }, nil
}

// EnsureRestart records one restart-only Ensure attempt as a durable operation
// (§6: "a reconnect with no durable record performs no mutating SSH command"),
// the restart twin of EnsureDeploy: it mints the server-side client operation
// ID, persists the restart record with its fencing epoch under the caller's
// held gate before the leg's first remote command, and publishes the operation
// as the gate's holder so a contender's busy refusal names it. The returned
// finish records the leg's outcome. A nil hook (no operation store wired)
// leaves the attempt unrecorded, which production never does.
func (m *hubHostManager) EnsureRestart(host hostreg.Host) (func(error), error) {
	ops := m.opsStore()
	if ops == nil {
		return nil, errors.New("the host operation store is not configured, so a restart-only Ensure attempt cannot be recorded; nothing was launched")
	}
	if strings.TrimSpace(m.cfg.bootID) == "" {
		return nil, errors.New("this hub carries no boot id, so a restart-only Ensure attempt cannot bind its fencing epoch; nothing was launched")
	}
	clientOperationID, err := newEnsureOperationID()
	if err != nil {
		return nil, err
	}
	outcome, err := ops.CreateOperation(hostops.OperationCreateRequest{
		ClientOperationID: clientOperationID,
		Host:              host.Name,
		Kind:              hostops.KindRestart,
		Pair:              hostops.OperationPair{Generation: host.Generation, IncarnationID: host.IncarnationID},
		BootID:            m.cfg.bootID,
		// The caller holds the host's gate, so this read is the pre-operation
		// position: an operation that finished before the Ensure attempt started
		// must not refuse the attempt (only one that lands while it runs).
		SequenceBefore: ops.Sequence(),
	})
	if err != nil {
		return nil, fmt.Errorf("persisting the restart-only Ensure attempt's operation record failed, so nothing was launched: %w", err)
	}
	record := outcome.Record
	if err := m.cfg.gate.HoldAs(host.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: record.ID}); err != nil {
		m.logf("host %q: the restart-only Ensure operation %s could not publish itself as the gate holder: %v",
			host.Name, record.ID, err)
	}
	return func(err error) { m.finishEnsureOperation(record.ID, host.Name, hostops.KindRestart, err) }, nil
}

// finishEnsureOperation records one Ensure attempt's outcome on the record its
// hook persisted: success is `complete`, a failure is `failed` with the cause
// verbatim, and a failure whose cause is the manager's own shutdown is
// `interrupted` — the same controller-lifetime rule the RPC-triggered workers
// follow. Competing concurrent terminal operations never share a call: the
// Ensure attempt is serialized by the host's gate. The step is over at this
// point, so the gate's holder goes back to the manager's attach class: a
// contender must not be told a finished operation is still running.
func (m *hubHostManager) finishEnsureOperation(id, name string, kind hostops.Kind, err error) {
	defer func() {
		restored := hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"}
		if herr := m.cfg.gate.HoldAs(name, restored); herr != nil {
			m.logf("host %q: the gate's holder could not be restored after the Ensure %s %s: %v", name, kind, id, herr)
		}
	}()
	if m.opsStore() == nil {
		return
	}
	switch {
	case err == nil:
		m.recordTerminal(id, hostops.StateComplete, true, fmt.Sprintf("Ensure-triggered %s complete", kind))
	case errors.Is(err, sshconn.ErrManagerClosed), errors.Is(err, context.Canceled):
		m.recordTerminal(id, hostops.StateInterrupted, false, operationsShutdownNote)
	default:
		m.recordTerminal(id, hostops.StateFailed, false, err.Error())
	}
}

// newEnsureOperationID mints the server-side client operation ID an
// Ensure-triggered deploy's record carries (§6). It is a fresh random value per
// attempt: Ensure retries are new operations, never replays, so the ID is
// unique rather than idempotent.
func newEnsureOperationID() (string, error) {
	// The random half is the running probe's CSPRNG-hex drawing (probeNonce):
	// one helper for the hub's opaque random tokens, so their entropy source
	// cannot drift apart.
	nonce, err := probeNonce()
	if err != nil {
		return "", fmt.Errorf("minting the Ensure-triggered deploy's operation id failed: %w", err)
	}
	return "ensure-" + nonce, nil
}
