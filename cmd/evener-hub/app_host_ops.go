package hub

// evener/host/plan (deploy pipeline 08b §6, §10): the planning read that mints
// the single-use confirmation token evener/host/deploy consumes. It is a
// mutation — the mint is a durable store write — so it admits exactly like the
// host settings mutations: the edge's AuthGuard admits the connection, and the
// handler registers through hostManageHandler, the same
// controller-local origin guard add/update/remove register through, so a peer
// hub calling over its attach bridge can never plan against this hub's hosts.
//
// This handler builds the plan's shape and its token AND fences them: the
// ungated facts refresh runs first with nothing held, then the per-host gate is
// try-acquired once and held through the durable probe-epoch write, the gated
// running-state probe, the under-gate re-checks, the mint, and the return
// (§5's gate-first order, §6 step 2). The probe epoch is the fencing-shaped
// durable record that authorizes exactly the bounded probe window; the mint
// supersedes it, a refusal deletes it, and boot deletes an epoch-only row
// silently. The last-known publication and the operation store's dedup/consume
// half remain the slices that own them (§6 steps 3-4, §10): where this handler
// needs one it goes through a seam and reports the arm the missing half
// produces, never a stand-in that would answer as if the work had happened.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appserver"
)

// errPlanHandlerAbsent marks a running-state probe refused because the remote
// build predates the deploy pipeline's probe handler (§6 step 2's
// `handler-absent` arm). It is not a probe failure: nothing about the host is
// wrong, and retrying the same plan cannot change the answer until the host is
// migrated. The seam classifies it, so the handler never has to guess from
// prose.
var errPlanHandlerAbsent = errors.New("hub: the remote hub predates the deploy pipeline's running handler")

// PlanHandlerAbsentError is how a probe seam outside this package reports the
// §6 `handler-absent` arm — a remote build that predates the deploy pipeline's
// running handler. A seam wraps it (or returns it directly) to answer
// errors.Is against errPlanHandlerAbsent, which the handler classifies on.
type PlanHandlerAbsentError struct{}

func (PlanHandlerAbsentError) Error() string { return errPlanHandlerAbsent.Error() }

// Unwrap makes every handler-absent refusal answer errors.Is against
// errPlanHandlerAbsent.
func (PlanHandlerAbsentError) Unwrap() error { return errPlanHandlerAbsent }

// planNoTokenTerminal is §10's reason table: the arms a retry cannot clear on
// its own are terminal; the five retryable arms (unattached, refresh-failed,
// probe-failed, handler-absent, remnant-open) name conditions the next plan can
// find changed. The table is also §10's closed reason set: a reason outside it
// is a programming error in this package, refused rather than rendered with a
// wrong flag.
var planNoTokenTerminal = map[string]bool{
	appwire.HostPlanReasonUnattached:          false,
	appwire.HostPlanReasonRefreshFailed:       false,
	appwire.HostPlanReasonProbeFailed:         false,
	appwire.HostPlanReasonHandlerAbsent:       false,
	appwire.HostPlanReasonRemnantOpen:         false,
	appwire.HostPlanReasonControllerDirty:     true,
	appwire.HostPlanReasonTargetUnwritable:    true,
	appwire.HostPlanReasonTargetMissingPrereq: true,
	appwire.HostPlanReasonTargetUnitFindings:  true,
}

// planNoToken builds §10's no-token arm: the refusal reason, its prose, whether
// the host was attached when the plan refused, whether the refusal is terminal
// (§10's terminal set is exactly the four target/controller arms), and — exactly
// on the `remnant-open` arm — the id of the remnant that fences the name.
//
// The two contract rules around remnantId are enforced here rather than left to
// call sites: `remnant-open` without an id would render a refusal a client
// cannot act on, and any other reason carrying one would attach an unrelated
// remnant to a refusal that is not about remnants (§10: "remnantId present
// exactly on remnant-open"). A violation, like a reason outside the set, is a
// programming error in this package and is refused loudly rather than rendered.
func planNoToken(reason, message string, attached bool, remnantID string) (appwire.HostPlanResult, error) {
	terminal, known := planNoTokenTerminal[reason]
	if !known {
		return appwire.HostPlanResult{}, fmt.Errorf("hub: %q is not a plan no-token reason", reason)
	}
	switch {
	case reason == appwire.HostPlanReasonRemnantOpen && remnantID == "":
		return appwire.HostPlanResult{}, fmt.Errorf("hub: the %q arm needs the remnant it names", reason)
	case reason != appwire.HostPlanReasonRemnantOpen && remnantID != "":
		return appwire.HostPlanResult{}, fmt.Errorf("hub: the %q arm carries remnant %q, which §10 reserves for %q",
			reason, remnantID, appwire.HostPlanReasonRemnantOpen)
	}
	return appwire.HostPlanResult{HostPlanNoToken: &appwire.HostPlanNoToken{
		Outcome: appwire.HostPlanOutcomeNoToken,
		StaleFacts: appwire.HostPlanStaleFacts{
			Message:  message,
			Attached: attached,
			Reason:   reason,
		},
		Terminal:  terminal,
		RemnantID: remnantID,
	}}, nil
}

// hostEntryFingerprint is §3's host-own-entry fingerprint for one effective
// entry: a canonical digest over the entry's configured, operator-authored
// fields. The registry's machine-managed identity (generation, incarnation id,
// presence epoch) is deliberately outside it — the token binds that pair
// separately, and the fingerprint must not move when a receipt write or an
// unrelated host's mutation lands (registry spec 08 §6's fingerprint
// semantics).
func hostEntryFingerprint(entry hostreg.Host) string {
	canonical, err := json.Marshal(struct {
		Name       string   `json:"name"`
		SSH        string   `json:"ssh"`
		User       string   `json:"user"`
		EvenerPath string   `json:"evener_path"`
		ConfigPath string   `json:"config_path"`
		Addr       string   `json:"addr"`
		Roots      []string `json:"roots"`
		KeyPath    string   `json:"key_path"`
	}{entry.Name, entry.SSH, entry.User, entry.EvenerPath, entry.ConfigPath, entry.Addr, rootsOrEmpty(entry.Roots), entry.KeyPath})
	if err != nil {
		// A struct of strings cannot fail to marshal; a digest-less entry must
		// never be mistaken for a fingerprinted one, so the empty string is the
		// documented "no fingerprint" answer and callers refuse.
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// rootsOrEmpty normalizes a nil root list to an empty one, so "no roots" has one
// canonical spelling.
func rootsOrEmpty(roots []string) []string {
	if roots == nil {
		return []string{}
	}
	return roots
}

// hostTOMLFingerprint is the token's hubTomlFingerprint binding: the selected
// hub.toml's entry fingerprint for name, read at plan time (§3: "the host's
// hub.toml entry fingerprint as on disk at plan time"). A file that carries no
// entry for the name — a hub with no config path, or a name the file does not
// declare yet — fingerprints as absent, which is a value of its own: the entry
// appearing later reads as drift and refuses the token, exactly as a hand edit
// to a bound entry does. It reports false only when the file exists and cannot
// be validated, where nothing about the on-disk entry can be asserted.
func (m *hubHostManager) hostTOMLFingerprint(name string) (string, bool) {
	if strings.TrimSpace(m.cfg.configPath) == "" {
		return hostEntryFingerprintAbsent, true
	}
	raw, err := configReadFile(m.cfg.configPath)
	switch {
	case errors.Is(err, os.ErrNotExist), err == nil && len(raw) == 0:
		// A selected path with no file behind it — a default-config hub, or one
		// whose file has not been written yet — declares no entries, so the name
		// fingerprints as absent.
		return hostEntryFingerprintAbsent, true
	case err != nil:
		return "", false
	}
	cfg, err := decodeConfig(m.cfg.configPath, string(raw))
	if err != nil {
		return "", false
	}
	for _, entry := range hostRegistryEntries(cfg) {
		if entry.Name == name {
			fingerprint := hostEntryFingerprint(entry)
			return fingerprint, fingerprint != ""
		}
	}
	return hostEntryFingerprintAbsent, true
}

// hostEntryFingerprintAbsent is the fingerprint of a name the selected hub.toml
// does not carry: the absence of an effective entry is itself a value the token
// binds.
var hostEntryFingerprintAbsent = func() string {
	sum := sha256.Sum256([]byte(`"absent"`))
	return hex.EncodeToString(sum[:])
}()

// hostPlanFactsFromPreflight maps component 04's preflight facts onto §1's fact
// set. capturedAt is the instant the caller's read returned — the caller must
// pass when these values were read, never the mint's own clock: a token's
// freshness term measures from here, so a later stamp would hand it a lifetime
// the facts never earned (08b §3).
func hostPlanFactsFromPreflight(host hostreg.Host, preflight sshconn.Preflight, capturedAt time.Time) hubcore.HostPlanFacts {
	return hubcore.HostPlanFacts{
		OS:          preflight.OS,
		Arch:        preflight.Arch,
		Home:        preflight.Home,
		Roots:       slices.Clone(host.Roots),
		UID:         preflight.UID,
		Version:     preflight.Version,
		Protocol:    preflight.Protocol,
		LaunchFlags: slices.Clone(preflight.LaunchFlags),
		CapturedAt:  capturedAt,
	}
}

// Plan implements evener/host/plan (§6, §10). The order is the spec's:
//
//  1. resolve the live entry, and refuse a controller whose own build is
//     unverifiable (the terminal `controller-dirty` arm — a dirty controller
//     can neither install its own build nor prove a host's matches it, the same
//     condition the deploy paths refuse on);
//  2. refresh the host's preflight facts with no gate held, on the attached
//     channel: not attached is `unattached`, a failed refresh is
//     `refresh-failed`, and resolve the deploy target the plan names
//     (`target-missing-prereq` when the entry and facts cannot name a run target
//     a hub could serve);
//  3. try-acquire the per-host gate once (§5: gate first, nothing waits) and
//     hold it through everything below — a held gate fails fast with the typed
//     busy error naming its holder;
//  4. immediately re-read the registry entry under the gate (§5's
//     post-acquisition re-read): a mutation that landed in the ungated window
//     is a typed `stale-entry` refusal, never a plan against the superseded
//     entry;
//  5. persist the durable probe epoch first — its own atomic store write,
//     bound to the entry's current (generation, incarnation id) pair — so the
//     probe's remote write half is recoverable under the persisted-before-write
//     rule; a write failure refuses `probe-failed` with nothing launched;
//  6. probe the running state through the gate-aware primitive, presenting the
//     persisted epoch (never a default, never absent): a remote that predates
//     the handler is `handler-absent`, any other failure — timeout included —
//     is `probe-failed`, with the epoch dropped on the way out;
//  7. re-check the entry, the attachment, the facts' freshness, and the
//     terminal-operation sequence under the same gate and mint, superseding any
//     outstanding token and the probe epoch for the name — all in the mint's
//     own locked write.
//
// Never dials: the facts and probe seams read the host's attached channel, or
// report the arm that says they could not.
func (m *hubHostManager) Plan(ctx context.Context, params appwire.HostPlanParams) (appwire.HostPlanResult, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostPlanResult{}, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return appwire.HostPlanResult{}, appwire.InvalidParams("evener/host/plan needs a host name")
	}
	m.cfg.mu.Lock()
	entry, ok := m.cfg.hosts.Get(name)
	m.cfg.mu.Unlock()
	if !ok {
		return appwire.HostPlanResult{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	// The local attachment answer is resolved first so every arm this handler can
	// emit reports it truthfully — including the controller-dirty refusal, which
	// is about this hub but still tells a client whether its host is attached: a
	// hard-coded false there would push a client into a reconnect it does not
	// need. It reads the channel registry only; nothing here dials.
	_, attached := m.attachedClient(entry)
	if m.controllerDirty() {
		return planNoToken(appwire.HostPlanReasonControllerDirty,
			fmt.Sprintf("this controller was built from a dirty tree (version %q), so it cannot install its own build or prove a host's build matches it; rebuild the controller from a clean checkout", buildinfo.Version()), attached, "")
	}

	// §6 step 2's local race scan: the position every terminal transition is
	// compared against is read before the ungated refresh, so an operation that
	// finishes while the plan is being built is seen.
	sequenceBefore := uint64(0)
	if m.cfg.ops != nil {
		sequenceBefore = m.cfg.ops.Sequence()
	}

	// §6 step 1: refresh the facts with no gate held, on the attached channel.
	if !attached {
		return planNoToken(appwire.HostPlanReasonUnattached,
			fmt.Sprintf("host %q is not attached; connect it and plan again", name), false, "")
	}
	if m.cfg.planFacts == nil {
		return planNoToken(appwire.HostPlanReasonRefreshFailed,
			fmt.Sprintf("no preflight-facts refresh is wired for host %q", name), attached, "")
	}
	facts, err := m.cfg.planFacts(ctx, entry)
	if err != nil {
		return planNoToken(appwire.HostPlanReasonRefreshFailed,
			fmt.Sprintf("refreshing host %q's preflight facts failed: %v", name, err), attached, "")
	}
	target, err := sshconn.DeployRunTargetFor(entry, facts.Home)
	if err != nil {
		return planNoToken(appwire.HostPlanReasonTargetMissingPrereq,
			fmt.Sprintf("host %q: %v", name, err), attached, "")
	}

	// §6 step 2's gate acquisition, after the ungated refresh. The hold spans
	// the probe window, the re-checks, the mint, and the return.
	release, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		return appwire.HostPlanResult{}, err
	}
	defer release()

	// §5's post-acquisition re-read, immediately after acquisition: a mutation
	// can legally commit in the window between the resolution above and this
	// gate, and a plan must never proceed on the superseded entry.
	if !m.cfg.hosts.SameRegistration(name, entry) {
		return appwire.HostPlanResult{}, appwire.StaleEntry(appwire.StaleEntryBindingGeneration,
			fmt.Sprintf("host %q's registration moved while this plan was being built; plan again", name))
	}
	// The probe pairs with the client the gate resolves, not the one the ungated
	// refresh started from: a reconnect that replaced the channel in that window
	// must not be probed through the retired generation.
	client, attachedNow := m.attachedClient(entry)
	if !attachedNow {
		return planNoToken(appwire.HostPlanReasonUnattached,
			fmt.Sprintf("host %q detached while this plan was being built; connect it and plan again", name), false, "")
	}

	// §6 step 2: the durable probe epoch first. Nothing is probed — and no
	// remote write half can run — until the epoch is recoverable in the store.
	epoch, err := m.persistProbeEpoch(entry)
	switch {
	case err != nil && !hostops.RenameLanded(err):
		// Nothing was written: no epoch, no probe, nothing launched.
		return planNoToken(appwire.HostPlanReasonProbeFailed,
			fmt.Sprintf("host %q: the durable probe epoch could not be persisted, so nothing was probed: %v", name, err), true, "")
	case err != nil:
		// The rename landed and the store adopted the row, so the epoch is
		// durable and memory agrees — only the directory sync behind it failed.
		// This is the same landed-write posture the mint takes for its token
		// (see hostops.RenameLanded): the plan proceeds with the durable epoch,
		// and a later refusal drops it through the ordinary drop path rather
		// than leaving an epoch-only row behind.
		m.logf("host %q: the probe epoch's directory sync failed after its rename landed; the epoch is durable: %v", name, err)
	}
	result, err := m.probeAndMint(ctx, name, planMintInputs{
		entry:          entry,
		client:         client,
		facts:          facts,
		target:         target,
		epoch:          epoch,
		sequenceBefore: sequenceBefore,
		// The attachment state the gate resolved, never the pre-gate read: the
		// planned response and every under-gate refusal report what holds at the
		// fence. (The early `!attachedNow` return below means this is always a
		// true answer here, but it is now derived from the gate's own observation
		// rather than an older one.)
		attached: attachedNow,
	})
	if err == nil && result.HostPlanPlanned != nil {
		// The mint's own atomic write superseded the epoch; nothing is left to
		// drop.
		return result, nil
	}
	// §6 step 2: a probe epoch never outlives its plan call — an arm that did
	// not mint deletes it. (A mint whose rename landed with a directory-sync
	// failure is still the planned arm and already superseded it.)
	m.dropProbeEpoch(name, epoch)
	return result, err
}

// probeAndMint runs the gated probe, then the under-gate re-checks and the
// mint. The caller holds the per-host gate across all of it.
func (m *hubHostManager) probeAndMint(ctx context.Context, name string, in planMintInputs) (appwire.HostPlanResult, error) {
	if m.cfg.planProbe == nil {
		return planNoToken(appwire.HostPlanReasonHandlerAbsent,
			fmt.Sprintf("host %q's hub does not serve the deploy pipeline's running probe yet, so nothing could be probed; upgrade the host's build before planning", name), in.attached, "")
	}
	probe, err := m.cfg.planProbe(ctx, in.entry, in.client, appwire.FencingEpoch{BootID: in.epoch.BootID, OpSeq: in.epoch.OpSeq})
	if err != nil {
		if errors.Is(err, errPlanHandlerAbsent) {
			return planNoToken(appwire.HostPlanReasonHandlerAbsent,
				fmt.Sprintf("host %q's hub predates the deploy pipeline's running probe: %v", name, err), in.attached, "")
		}
		return planNoToken(appwire.HostPlanReasonProbeFailed,
			fmt.Sprintf("probing host %q's running state failed: %v", name, err), in.attached, "")
	}
	in.probe = probe
	return m.mintPlannedToken(name, in)
}

// planMintInputs is everything the fenced final block needs: the resolution,
// the probe epoch, and the probe the plan was built from, plus the state the
// re-checks compare against.
type planMintInputs struct {
	entry          hostreg.Host
	client         *appwire.Client
	facts          hubcore.HostPlanFacts
	target         string
	epoch          hostops.ProbeEpoch
	probe          hubcore.HostRuntimeProbe
	sequenceBefore uint64
	attached       bool
}

// acquireHostGate try-acquires the per-host gate for one holder and answers
// §5's typed busy refusal when it is held. Nothing waits: a held gate is a
// fail-fast refusal, rendered by holder class — an operation-held gate names
// the operation (open/wait-able), and every other holder renders the typed
// transient form with no operation reference. `plan` passes its
// validation-plus-mint holder; a deploy/restart slice passes
// hostops.HolderOperation with its record id.
func (m *hubHostManager) acquireHostGate(name string, holder hostops.Holder) (func(), error) {
	if m.cfg.gate == nil {
		return nil, appwire.InternalError("the per-host gate is not configured, so no plan can be fenced")
	}
	release, err := m.cfg.gate.TryAcquire(name, holder)
	if err != nil {
		return nil, hostBusyWireError(err)
	}
	return release, nil
}

// hostBusyWireError maps the gate's typed busy refusal onto §11's envelope:
// `host-busy-operation` with the operation record id for an operation holder,
// `host-busy-transient` with no operation reference for every other class. An
// operation holder that carries no record id (a caller bug) renders the
// transient form rather than an `operationId` no open/wait reference could
// resolve — the same fallback BusyError.Error() renders in prose.
func hostBusyWireError(err error) error {
	var busy *hostops.BusyError
	if !errors.As(err, &busy) {
		return err
	}
	if busy.Holder.Kind == hostops.HolderOperation && strings.TrimSpace(busy.Holder.OperationID) != "" {
		return appwire.HostBusyOperation(busy.Holder.OperationID, busy.Error())
	}
	return appwire.HostBusyTransient(busy.Error())
}

// persistProbeEpoch writes one durable probe epoch for entry (§6): its own
// atomic store write, bound to the entry's current (generation, incarnation id)
// pair, with the store assigning the per-host op sequence. Plan, deploy and
// restart all persist through here, so no probe path can present an epoch the
// store never held. A hub with no operation store or no boot id cannot persist
// one, so it refuses — the honest answer, because a probe without a durable
// epoch is exactly the unfenced write the spec forbids.
func (m *hubHostManager) persistProbeEpoch(entry hostreg.Host) (hostops.ProbeEpoch, error) {
	if m.cfg.ops == nil {
		return hostops.ProbeEpoch{}, errors.New("the host operation store is not configured")
	}
	if strings.TrimSpace(m.cfg.bootID) == "" {
		return hostops.ProbeEpoch{}, errors.New("this hub carries no boot id, so no probe epoch can be bound")
	}
	return m.cfg.ops.PersistProbeEpoch(hostops.ProbeEpochRequest{
		Host:          entry.Name,
		BootID:        m.cfg.bootID,
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
	})
}

// dropProbeEpoch deletes one probe epoch on a refusal path. The match is exact,
// so a stale drop can never remove a row another call persisted, and a drop
// after the mint (or the consume) superseded the row is a no-op. A drop that
// cannot write is logged, not returned: the row left behind is inert and the
// next boot's reap deletes it.
func (m *hubHostManager) dropProbeEpoch(name string, epoch hostops.ProbeEpoch) {
	if m.cfg.ops == nil || epoch.BootID == "" {
		return
	}
	if err := m.cfg.ops.DropProbeEpoch(name, epoch.BootID, epoch.OpSeq); err != nil {
		m.logf("host %q: the probe epoch %s/%d could not be dropped after a refused operation: %v",
			name, epoch.BootID, epoch.OpSeq, err)
	}
}

// mintPlannedToken runs §6 step 2's under-gate re-checks and the mint. Its
// caller (Plan) holds §5's per-host gate across this call, so the gate is
// already held: nothing here acquires it again (it is non-reentrant), and the
// gate's hold covers the re-checks, the durable mint write, and the return.
//
// The manager's mutation mutex is taken across the block as the durable-write
// outermost lock — lock order is §5's, gate first, then the mutation mutex,
// then the store mutex the mint's write takes innermost. It closes the one
// interleaving the gate alone cannot: the host-management mutations that move
// this very entry. A removal or update in flight refuses the plan — the name is
// transiently held, which is the conflict the other host mutations report too —
// and one that starts while this block runs waits for it, so the removal's
// revocation can never land between a plan's checks and its mint and leave a
// token for a name whose registration is gone (or, if the removal rolls back,
// one that outlived its plan).
func (m *hubHostManager) mintPlannedToken(name string, in planMintInputs) (appwire.HostPlanResult, error) {
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()

	switch {
	case m.isMutating(name):
		return appwire.HostPlanResult{}, hostMutationConflict(name)
	case !m.cfg.hosts.SameRegistration(name, in.entry):
		return appwire.HostPlanResult{}, appwire.StaleEntry(appwire.StaleEntryBindingGeneration,
			fmt.Sprintf("host %q's registration moved while this plan was being built; plan again", name))
	}
	// §6 step 2's attachment re-check, from the fence rather than from the
	// pre-refresh read: a host that dropped its channel while the refresh and the
	// probe ran must not reach the mint with a stale attached answer.
	if _, attachedNow := m.attachedClient(in.entry); !attachedNow {
		return planNoToken(appwire.HostPlanReasonUnattached,
			fmt.Sprintf("host %q detached while this plan was being built; connect it and plan again", name), false, "")
	}

	fingerprint, ok := m.hostTOMLFingerprint(name)
	if !ok {
		// A file that does not validate is not a facts-refresh failure the next
		// plan can clear: the selected hub.toml is this hub's own configuration,
		// and every retry re-reads the same bytes. It is this hub's problem, not
		// the host's, so it refuses as an internal error naming the file rather
		// than as a retryable no-token arm that would blame the host.
		return appwire.HostPlanResult{}, appwire.InternalError(fmt.Sprintf(
			"host %q's hub.toml entry could not be fingerprinted: the selected hub.toml (%s) does not validate, so nothing about the host's on-disk entry can be bound; fix the file before planning",
			name, m.cfg.configPath))
	}
	if m.cfg.ops == nil {
		return appwire.HostPlanResult{}, appwire.InternalError("the host operation store is not configured, so no confirmation token can be minted")
	}

	now := time.Now().UTC()
	// The terminal-operation guard rides the mint's own locked read
	// (MintTokenIfQuiescent): a scan run here could not see a terminal transition
	// landing between it and the mint, while the store's guard cannot miss one.
	token, err := m.cfg.ops.MintTokenIfQuiescent(hostops.MintRequest{
		Host:               name,
		Generation:         in.entry.Generation,
		IncarnationID:      in.entry.IncarnationID,
		EntryHash:          hostEntryFingerprint(in.entry),
		HubTOMLFingerprint: fingerprint,
		FactsRevision:      in.facts.Revision(),
		FactsCapturedAt:    in.facts.CapturedAt,
		TargetPath:         in.target,
		ControllerRevision: buildinfo.Version(),
		RunningVersion:     in.probe.Version,
		RunningHealthy:     in.probe.RunningHealthy,
		ProcessStartTime:   in.probe.ProcessStartTime,
	}, in.sequenceBefore)
	var concurrent *hostops.ConcurrentTerminalOpError
	switch {
	case errors.As(err, &concurrent):
		return appwire.HostPlanResult{}, appwire.StaleEntry(appwire.StaleEntryBindingConcurrentTerminalOp,
			fmt.Sprintf("host %q completed operation %q while this plan was being built; plan again", name, concurrent.ID))
	case errors.Is(err, hostops.ErrFactsStale):
		// §3: stale facts at mint read as a refresh failure, and re-planning
		// refreshes them — never an already-expired token.
		return planNoToken(appwire.HostPlanReasonRefreshFailed,
			fmt.Sprintf("host %q's refreshed facts went stale before the token was minted: %v", name, err), in.attached, "")
	case err != nil && !hostops.RenameLanded(err):
		return appwire.HostPlanResult{}, appwire.InternalError(fmt.Sprintf("minting the confirmation token for host %q failed: %v", name, err))
	}
	if err != nil {
		// The rename landed, so the token row is durable (see RenameLanded) and
		// the client must not be told nothing was minted: answer the planned arm
		// with the token the store already holds, and log the sync trouble.
		m.logf("host %q: the mint's directory sync failed after its rename landed; the token is durable: %v", name, err)
	}

	plan := appwire.HostPlan{
		Host:               name,
		Generation:         token.Generation,
		TargetPath:         token.TargetPath,
		ControllerRevision: token.ControllerRevision,
		RestartFollows:     planRestartFollows(in.probe, token.ControllerRevision),
		FactsRevision:      token.FactsRevision,
		HubTOMLFingerprint: token.HubTOMLFingerprint,
		FactsCapturedAt:    token.FactsCapturedAt.Format(time.RFC3339),
		FactsAgeSec:        planFactsAgeSec(now, token.FactsCapturedAt),
		RunningVersion:     token.RunningVersion,
		RunningHealthy:     token.RunningHealthy,
	}
	if token.ProcessStartTime != nil {
		// The same sub-second precision the probe carried: the plan re-renders
		// the value a deploy will compare after the restart, and a truncated
		// string cannot distinguish two incarnations started in one second.
		plan.RunningProcessStartTime = token.ProcessStartTime.Format(time.RFC3339Nano)
	}
	return appwire.HostPlanResult{HostPlanPlanned: &appwire.HostPlanPlanned{
		Outcome: appwire.HostPlanOutcomePlanned,
		Plan:    plan,
		Token:   token.Value,
	}}, nil
}

// planFactsAgeSec is how old the plan's facts were, in whole seconds, never
// negative: a capture stamped ahead of this hub's clock (a host whose clock
// leads, a capture anchored at the durable mark during a rollback) is a fact
// about timing the client does not need a signed number for, and a negative age
// would read as fresher than fresh.
func planFactsAgeSec(now, capturedAt time.Time) int64 {
	age := now.Sub(capturedAt)
	if age < 0 {
		return 0
	}
	return int64(age / time.Second)
}

// planRestartFollows answers the plan's restartFollows field from the inputs the
// plan actually has. §6 states one rule outright — "a probed unverifiable
// revision (`dev` or dirty) always reads as outdated: restart follows. No
// timestamp comparison exempts it" — and the same field must not read "no
// restart" for the two other ways a running host is already known to need one:
//
//   - the host reports itself unhealthy, so the process must be replaced
//     whatever it runs (the health flag is §10's third running-handler
//     condition, computed by the host itself);
//   - the probed revision is verifiable but is not the controller's own, so the
//     host is running someone else's build and only a deploy-plus-restart makes
//     it the build the plan is for.
//
// What a *matching* verifiable revision implies for the push and restart steps
// beyond this — whether an already-current host still needs its process replaced
// — is the deploy decision ladder's to state, and this slice does not guess on
// its behalf: it reports no restart exactly when the host is healthy and already
// runs the controller's revision.
func planRestartFollows(probe hubcore.HostRuntimeProbe, controllerRevision string) bool {
	if !probe.RunningHealthy {
		return true
	}
	if sshconn.UnverifiableVersion(probe.Version) {
		return true
	}
	return probe.Version != controllerRevision
}

// controllerDirty reports whether the running controller's build cannot prove a
// host's build matches it (§6's terminal controller-dirty arm). It is the same
// signal the deploy paths refuse on: a build from a dirty tree carries a version
// any other dirty checkout at that commit would report too.
func (m *hubHostManager) controllerDirty() bool {
	if m.cfg.planControllerDirty != nil {
		return m.cfg.planControllerDirty()
	}
	return strings.TrimSpace(buildinfo.GitDirty) == "true"
}

// registerOpsHandlers installs evener/host/plan on the host surface. It is
// called by registerHostManageHandlers beside the add/list/status/remove/update
// registrations, so the plan rides the same manager, the same live registry and
// the same admission path: the edge's auth admits the connection, the
// registration-time hostManageHandler origin guard refuses a remote-originated
// request, and the handler itself holds nothing.
func (m *hubHostManager) registerOpsHandlers(server *appserver.Server) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostPlan, hostManageHandler(m.Plan))
	// deploy and restart are mutations of the same surface and the same
	// admission: the edge's auth admits the connection, the registration-time
	// origin guard refuses a remote-originated request, and each handler runs
	// its own fixed processing order (app_host_deploy.go).
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostDeploy, hostManageHandler(m.Deploy))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRestart, hostManageHandler(m.Restart))
	// operations is the surface's read: no gate, no dial, no registry
	// resolution — the same admission as the rest (controller-local; a remote
	// origin refused) and nothing held while it answers from the store.
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostOperations, hostManageHandler(m.Operations))
}

// Operations implements evener/host/operations (§8, §10): the read-only page of
// the operation store's records. It is a read in every sense — no gate, no
// dial, no write, no registry resolution — so a removed host's retained history
// stays readable, and a read never attaches anything (the same
// no-lazy-attachment rule `list`/`status` carry). Admission is the host
// surface's: the registration-time origin guard refuses a remote-originated
// request, and the handler keeps the guard for direct callers (tests,
// embedders), exactly as Plan does.
func (m *hubHostManager) Operations(ctx context.Context, params appwire.HostOperationsParams) (appwire.HostOperationsResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostOperationsResponse{}, err
	}
	if m.cfg.ops == nil {
		return appwire.HostOperationsResponse{}, appwire.InternalError("the host operation store is not configured, so no operations can be read")
	}
	query, err := operationsQuery(params)
	if err != nil {
		return appwire.HostOperationsResponse{}, err
	}
	page, err := m.cfg.ops.ReadOperations(query)
	if err != nil {
		return appwire.HostOperationsResponse{}, operationsRefusal(err)
	}
	response, err := operationsResponse(page)
	if err != nil {
		return appwire.HostOperationsResponse{}, appwire.InternalError(err.Error())
	}
	return response, nil
}

// operationsQuery maps §10's params onto the store's read. The store owns the
// filter semantics; this translation only splits the wire's presence rules from
// the store's pointers and refuses a state outside the closed wire set before
// the store ever sees it.
func operationsQuery(params appwire.HostOperationsParams) (hostops.OperationsQuery, error) {
	query := hostops.OperationsQuery{
		Host:              strings.TrimSpace(params.Name),
		ClientOperationID: params.OperationID,
		IncarnationID:     params.IncarnationID,
		ID:                params.ID,
		Limit:             params.Limit,
		Cursor:            params.Cursor,
	}
	if params.State != "" {
		state, err := hostopsState(params.State)
		if err != nil {
			return hostops.OperationsQuery{}, appwire.InvalidParams(fmt.Sprintf("evener/host/operations: %v", err))
		}
		query.State = state
	}
	if params.Generation != 0 {
		generation := params.Generation
		query.Generation = &generation
	}
	return query, nil
}

// hostopsState maps the wire's closed operation-state set onto the store's.
func hostopsState(state appwire.OperationState) (hostops.State, error) {
	switch state {
	case appwire.OperationStatePending:
		return hostops.StatePending, nil
	case appwire.OperationStateRunning:
		return hostops.StateRunning, nil
	case appwire.OperationStateComplete:
		return hostops.StateComplete, nil
	case appwire.OperationStateFailed:
		return hostops.StateFailed, nil
	case appwire.OperationStateInterrupted:
		return hostops.StateInterrupted, nil
	case appwire.OperationStateOrphanUnverified:
		return hostops.StateOrphanUnverified, nil
	}
	return "", fmt.Errorf("operation state %q is outside the wire's set", state)
}

// operationsRefusal maps the store's read refusals onto §11's envelopes:
// stale-entry (a cursor pin drifted), cursor-invalidated (a mid-pagination
// compaction), cursor-too-large (the over-cap first page), and invalid params
// for a malformed query. Anything else — a record host without its mirrored
// boundary included — is an internal error.
func operationsRefusal(err error) error {
	if stale, ok := errors.AsType[*hostops.CursorStaleError](err); ok {
		return appwire.StaleEntry(appwire.StaleEntryBinding(stale.Binding), err.Error())
	}
	if invalidated, ok := errors.AsType[*hostops.CursorInvalidatedError](err); ok {
		return appwire.CursorInvalidated(invalidated.CompactSeq, invalidated.Host,
			cursorBoundWire(invalidated.Bound), err.Error())
	}
	if tooLarge, ok := errors.AsType[*hostops.CursorTooLargeError](err); ok {
		return appwire.CursorTooLarge(tooLarge.CapBytes, err.Error())
	}
	if errors.Is(err, hostops.ErrInvalidOperationsQuery) {
		return appwire.InvalidParams(err.Error())
	}
	return appwire.InternalError(fmt.Sprintf("reading the operation store failed: %v", err))
}

// cursorBoundWire renders one cursor bounds entry as §11's value union: the
// {generation, incarnationId, presenceEpoch} object, or the literal "absent"
// the hostBoundaries map also carries.
func cursorBoundWire(bound hostops.CursorBound) any {
	if bound.Absent {
		return appwire.HostBoundaryAbsent
	}
	return appwire.HostBoundary{
		Generation:    bound.Boundary.Generation,
		IncarnationID: bound.Boundary.IncarnationID,
		PresenceEpoch: bound.Boundary.PresenceEpoch,
	}
}

// operationsResponse renders one store page as §10's response: the records
// field-for-field, the top-level pair exactly on host-pinned pages, and the
// hostBoundaries value union exactly on unfiltered cross-host pages.
func operationsResponse(page hostops.OperationsPage) (appwire.HostOperationsResponse, error) {
	response := appwire.HostOperationsResponse{
		Operations: make([]appwire.OperationRecord, 0, len(page.Records)),
		NextCursor: page.NextCursor,
	}
	for _, record := range page.Records {
		wire, err := operationRecordWire(record)
		if err != nil {
			return appwire.HostOperationsResponse{}, err
		}
		response.Operations = append(response.Operations, wire)
	}
	if page.Generation != nil {
		response.Generation = *page.Generation
	}
	response.IncarnationID = page.IncarnationID
	if page.HostBoundaries != nil {
		response.HostBoundaries = make(map[string]any, len(page.HostBoundaries))
		for host, bound := range page.HostBoundaries {
			if bound.Absent {
				response.HostBoundaries[host] = appwire.HostBoundaryAbsent
				continue
			}
			response.HostBoundaries[host] = appwire.HostBoundary{
				Generation:    bound.Boundary.Generation,
				IncarnationID: bound.Boundary.IncarnationID,
				PresenceEpoch: bound.Boundary.PresenceEpoch,
			}
		}
	}
	return response, nil
}

// operationRecordWire renders one stored record as §10's OperationRecord. The
// `compacted` marker is set exactly on a read-only replay rebuilt from a dedup
// tombstone.
func operationRecordWire(record hostops.Record) (appwire.OperationRecord, error) {
	state, err := operationWireState(record.State)
	if err != nil {
		return appwire.OperationRecord{}, err
	}
	wire := appwire.OperationRecord{
		ID:                record.ID,
		ClientOperationID: record.ClientOperationID,
		Host:              record.Host,
		Generation:        record.Generation,
		IncarnationID:     record.IncarnationID,
		Kind:              string(record.Kind),
		State:             state,
		CreatedAt:         record.CreatedAt.Format(time.RFC3339),
		UpdatedAt:         record.UpdatedAt.Format(time.RFC3339),
		HostRemoved:       record.HostRemoved,
		Compacted:         record.Compacted,
	}
	for _, entry := range record.Progress {
		wire.Progress = append(wire.Progress, appwire.OperationProgressEntry{
			TS:      entry.TS.Format(time.RFC3339),
			Message: entry.Message,
		})
	}
	if record.Result != nil {
		wire.Result = &appwire.OperationResult{OK: record.Result.OK, Message: record.Result.Message}
	}
	return wire, nil
}
