package hub

// evener/host/plan (deploy pipeline 08b §6, §10): the planning read that mints
// the single-use confirmation token evener/host/deploy consumes. It is a
// mutation — the mint is a durable store write — so it admits exactly like the
// host settings mutations: the edge's AuthGuard admits the connection, and the
// handler registers through hostManageHandler, the same
// controller-local origin guard add/update/remove register through, so a peer
// hub calling over its attach bridge can never plan against this hub's hosts.
//
// What this slice builds is the plan's shape and its token: the ungated facts
// refresh, the running-state probe, the drift re-checks, the mint, and the
// no-token arms this hub can reach. The per-host gate, the durable probe epoch,
// the gated probe primitive, the last-known publication and the operation
// store's dedup/consume half are the slices that own them (§5, §6 steps 2-3,
// §10); where this slice needs one it goes through a seam and reports the arm
// the missing half produces, never a stand-in that would answer as if the work
// had happened.

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
//  3. probe the running state through the wired probe: a probe that reports the
//     remote predates the handler is `handler-absent`, any other failure is
//     `probe-failed`, and no probe seam at all is `handler-absent` too (the
//     honest answer until evener/host/running ships);
//  4. re-check that the entry has not moved and that no operation on this host
//     reached a terminal state while the plan was being built — both §6 step
//     2's re-plan refusals, emitted as typed `stale-entry`;
//  5. mint, superseding any outstanding token for the name, and answer the
//     planned arm.
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
	client, attached := m.attachedClient(entry)
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

	// §6 step 2's gated running probe. The gate, the durable probe epoch and the
	// probe's fencing write half belong to the slices that ship them; the probe
	// value and its two failure arms are this handler's.
	if m.cfg.planProbe == nil {
		return planNoToken(appwire.HostPlanReasonHandlerAbsent,
			fmt.Sprintf("host %q's hub does not serve the deploy pipeline's running probe yet, so nothing could be probed; upgrade the host's build before planning", name), attached, "")
	}
	var probe hubcore.HostRuntimeProbe
	probe, err = m.cfg.planProbe(ctx, entry, client)
	if err != nil {
		if errors.Is(err, errPlanHandlerAbsent) {
			return planNoToken(appwire.HostPlanReasonHandlerAbsent,
				fmt.Sprintf("host %q's hub predates the deploy pipeline's running probe: %v", name, err), attached, "")
		}
		return planNoToken(appwire.HostPlanReasonProbeFailed,
			fmt.Sprintf("probing host %q's running state failed: %v", name, err), attached, "")
	}

	return m.mintPlannedToken(name, planMintInputs{
		entry:          entry,
		facts:          facts,
		target:         target,
		probe:          probe,
		sequenceBefore: sequenceBefore,
		attached:       attached,
	})
}

// planMintInputs is everything the fenced final block needs: the resolution and
// probe the plan was built from, and the state the re-checks compare against.
type planMintInputs struct {
	entry          hostreg.Host
	facts          hubcore.HostPlanFacts
	target         string
	probe          hubcore.HostRuntimeProbe
	sequenceBefore uint64
	attached       bool
}

// mintPlannedToken runs §6 step 2's under-gate re-checks and the mint, holding
// the manager's mutation mutex across all of them.
//
// That mutex is not §5's per-host gate — the gate (try-acquire, typed busy
// classes, shared with deploy/restart/teardown) is the slice that ships the
// running probe, and this handler takes no stand-in for it. What the mutex does
// close is the one interleaving this slice can close without that gate: the host
// management mutations that move this very entry. A removal or update in flight
// refuses the plan — the name is transiently held, which is the conflict the
// other host mutations report too — and one that starts while this block runs
// waits for it, so the removal's revocation can never land between a plan's
// checks and its mint and leave a token for a name whose registration is gone
// (or, if the removal rolls back, one that outlived its plan).
//
// Lock order is the spec's: the mutation mutex is outermost among the durable
// writes, the store mutex innermost — MintToken's write sits inside this hold —
// and nothing here takes the host gate.
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
		plan.RunningProcessStartTime = token.ProcessStartTime.Format(time.RFC3339)
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
}

// revokeHostTokens drops name's outstanding confirmation tokens (§3's live
// removal revocation). A missing operation store has no rows to drop, and a
// store failure is logged rather than returned: the removal's hub.toml commit
// has already landed and cannot be unwound, and the rows it would leave behind
// are inert — see the call site in Remove, and the deploy slice's binding
// comparison that refuses them.
func (m *hubHostManager) revokeHostTokens(name string) {
	if m.cfg.ops == nil {
		return
	}
	if err := m.cfg.ops.RevokeTokens(name); err != nil {
		m.logf("host %q removed, but its outstanding confirmation tokens could not be dropped: %v", name, err)
	}
}
