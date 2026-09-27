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
// the host was attached when the plan refused, and whether the refusal is
// terminal (§10's terminal set is exactly the four target/controller arms). A
// reason outside the set is a programming error in this package, so it is
// refused loudly rather than rendered with a wrong `terminal` flag.
func planNoToken(reason, message string, attached bool) (appwire.HostPlanResult, error) {
	terminal, known := planNoTokenTerminal[reason]
	if !known {
		return appwire.HostPlanResult{}, fmt.Errorf("hub: %q is not a plan no-token reason", reason)
	}
	return appwire.HostPlanResult{HostPlanNoToken: &appwire.HostPlanNoToken{
		Outcome: appwire.HostPlanOutcomeNoToken,
		StaleFacts: appwire.HostPlanStaleFacts{
			Message:  message,
			Attached: attached,
			Reason:   reason,
		},
		Terminal: terminal,
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
	if m.controllerDirty() {
		return planNoToken(appwire.HostPlanReasonControllerDirty,
			fmt.Sprintf("this controller was built from a dirty tree (version %q), so it cannot install its own build or prove a host's build matches it; rebuild the controller from a clean checkout", buildinfo.Version()), false)
	}

	// §6 step 2's local race scan: the position every terminal transition is
	// compared against is read before the ungated refresh, so an operation that
	// finishes while the plan is being built is seen.
	sequenceBefore := uint64(0)
	if m.cfg.ops != nil {
		sequenceBefore = m.cfg.ops.Sequence()
	}

	// §6 step 1: refresh the facts with no gate held, on the attached channel.
	client, attached := m.attachedClient(entry)
	if !attached {
		return planNoToken(appwire.HostPlanReasonUnattached,
			fmt.Sprintf("host %q is not attached; connect it and plan again", name), false)
	}
	if m.cfg.planFacts == nil {
		return planNoToken(appwire.HostPlanReasonRefreshFailed,
			fmt.Sprintf("no preflight-facts refresh is wired for host %q", name), attached)
	}
	facts, err := m.cfg.planFacts(ctx, entry)
	if err != nil {
		return planNoToken(appwire.HostPlanReasonRefreshFailed,
			fmt.Sprintf("refreshing host %q's preflight facts failed: %v", name, err), attached)
	}
	target, err := sshconn.DeployRunTargetFor(entry, sshconn.Preflight{Home: facts.Home})
	if err != nil {
		return planNoToken(appwire.HostPlanReasonTargetMissingPrereq,
			fmt.Sprintf("host %q: %v", name, err), attached)
	}

	// §6 step 2's gated running probe. The gate, the durable probe epoch and the
	// probe's fencing write half belong to the slices that ship them; the probe
	// value and its two failure arms are this handler's.
	if m.cfg.planProbe == nil {
		return planNoToken(appwire.HostPlanReasonHandlerAbsent,
			fmt.Sprintf("host %q's hub does not serve the deploy pipeline's running probe yet, so nothing could be probed; upgrade the host's build before planning", name), attached)
	}
	var probe hubcore.HostRuntimeProbe
	probe, err = m.cfg.planProbe(ctx, entry, client)
	if err != nil {
		if errors.Is(err, errPlanHandlerAbsent) {
			return planNoToken(appwire.HostPlanReasonHandlerAbsent,
				fmt.Sprintf("host %q's hub predates the deploy pipeline's running probe: %v", name, err), attached)
		}
		return planNoToken(appwire.HostPlanReasonProbeFailed,
			fmt.Sprintf("probing host %q's running state failed: %v", name, err), attached)
	}

	// §6 step 2's under-gate re-checks, as far as this slice can run them
	// without the gate: the entry the plan was built from must still be the
	// registry's registration, and no operation may have finished meanwhile. A
	// detach, mutation or completed operation landing between the refresh and
	// here is a re-plan refusal, never a plan against the superseded entry.
	if !m.planEntryCurrent(name, entry) {
		return appwire.HostPlanResult{}, appwire.StaleEntry(appwire.StaleEntryBindingGeneration,
			fmt.Sprintf("host %q's registration moved while this plan was being built; plan again", name))
	}
	if finished, ok := m.terminalOperationSince(name, sequenceBefore); ok {
		return appwire.HostPlanResult{}, appwire.StaleEntry(appwire.StaleEntryBindingConcurrentTerminalOp,
			fmt.Sprintf("host %q completed operation %q while this plan was being built; plan again", name, finished))
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
	token, err := m.cfg.ops.MintToken(hostops.MintRequest{
		Host:               name,
		Generation:         entry.Generation,
		IncarnationID:      entry.IncarnationID,
		EntryHash:          hostEntryFingerprint(entry),
		HubTOMLFingerprint: fingerprint,
		FactsRevision:      facts.Revision(),
		FactsCapturedAt:    facts.CapturedAt,
		TargetPath:         target,
		ControllerRevision: buildinfo.Version(),
		RunningVersion:     probe.Version,
		RunningHealthy:     probe.RunningHealthy,
		ProcessStartTime:   probe.ProcessStartTime,
	})
	if errors.Is(err, hostops.ErrFactsStale) {
		// §3: stale facts at mint read as a refresh failure, and re-planning
		// refreshes them — never an already-expired token.
		return planNoToken(appwire.HostPlanReasonRefreshFailed,
			fmt.Sprintf("host %q's refreshed facts went stale before the token was minted: %v", name, err), attached)
	}
	if err != nil {
		return appwire.HostPlanResult{}, appwire.InternalError(fmt.Sprintf("minting the confirmation token for host %q failed: %v", name, err))
	}

	plan := appwire.HostPlan{
		Host:               name,
		Generation:         token.Generation,
		TargetPath:         token.TargetPath,
		ControllerRevision: token.ControllerRevision,
		RestartFollows:     planRestartFollows(probe, token.ControllerRevision),
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

// planEntryCurrent reports whether entry is still the registry's registration
// for its name: content and generation both, the predicate hostreg states and
// the attach identity rechecks use. Callers hold no lock across the plan, so
// this is a fresh read, not a fence.
func (m *hubHostManager) planEntryCurrent(name string, entry hostreg.Host) bool {
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	return m.cfg.hosts.SameRegistration(name, entry)
}

// terminalOperationSince reports the first operation on host whose terminal
// transition carries a sequence above sequenceBefore, which is §6 step 2's
// concurrent-terminal-op condition. It is a local store read, never a network
// call.
func (m *hubHostManager) terminalOperationSince(host string, sequenceBefore uint64) (string, bool) {
	if m.cfg.ops == nil {
		return "", false
	}
	for _, record := range m.cfg.ops.Records() {
		if record.Host != host || !record.State.Terminal() {
			continue
		}
		if record.Sequence > sequenceBefore {
			return record.ID, true
		}
	}
	return "", false
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
