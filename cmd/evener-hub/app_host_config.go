package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// hostOriginHubTOML is the HostRow origin marker. The storage decision (registry
// spec 08 §6/§19, decided 2026-09-26; the spec-correction PR rewrites §6/§19
// from the sidecar design this tree still shows) made hub.toml the
// machine-managed file every host lives in, so every row reports it; the wire
// field is retained with one value.
const hostOriginHubTOML = "hub.toml"

// hostTOMLBanner is the machine-managed banner every rewrite writes at the top
// of the selected hub.toml — the file saying, in itself, that the hub rewrites
// it and that operator comments and formatting do not survive (registry spec 08
// §6).
const hostTOMLBanner = "# This file is machine-managed by the evener hub.\n" +
	"# The hub rewrites it in place; comments and formatting are not preserved.\n"

// hostManagerConfig carries what the host-management handlers need. The
// registry is the controller's live one in production — the same
// *hostreg.Registry the SSH manager dials through and the attach handler
// validates against — so a host added at runtime is attachable without a
// restart. The stores are live: the hub owns them, mutations swap entries
// under mu, and reads never dial.
type hostManagerConfig struct {
	// hosts is the live host registry: the machine-managed hub.toml entries at
	// boot plus every entry added at runtime. Mutations hold mu; the SSH manager
	// and the attach handler consult the same instance in production.
	hosts *hostreg.Registry
	// store holds the durable host set: every live entry, in the order a
	// rewrite writes it (the boot set in the registry's name-sorted order,
	// then entries in add order; an edit replaces in place). The file's host
	// order is the hub's — the banner says formatting does not survive — so a
	// hand-authored out-of-order file is normalized by the first rewrite. It is
	// the model of the rewritten hub.toml — mutations derive their write from
	// it, so an entry a removal already committed can never be re-persisted by a
	// concurrent mutation whose write starts during the removal's teardown.
	store *hostStore
	// configPath is the selected hub.toml path — the file every mutation
	// rewrites in place. Empty (tests, embedders without a file) disables
	// persistence: the store stays memory-only and every method still works.
	configPath string
	// records is the durable home of the machine records and the host set they
	// travel with. The manager reads and writes records only through it
	// (recordStore), and boot swaps it from hub.toml's reserved sections to the
	// operation store once the records have moved.
	records   hostRecordStore
	recordsMu sync.RWMutex
	// ops is the operation store: the home of the machine records and of the
	// operation, token and boundary state beside them. Nil when no store is
	// configured (tests, embedders) or while the hub's store is unavailable
	// (see unavailable); a store that opens later is attached at runtime, so
	// every read goes through opsStore.
	ops atomic.Pointer[hostops.Store]
	// opsUnavailable marks a hub whose operation store was configured but could
	// not be opened: the hosts in hub.toml keep serving, host mutations refuse
	// until a store attaches.
	opsUnavailable bool
	// refusal, when set, is why host mutations are refused: the operation store
	// is unavailable, or the records it holds could not be settled or read. It
	// clears when a boot attach converges.
	refusal atomic.Pointer[hostRefusal]
	// interruptedHandled is set once the pass that interrupts what a crash left
	// in flight has been attempted over the attached store; later boots of the same store
	// (settlement retries) must not interrupt operations that are running now.
	interruptedHandled atomic.Bool
	// attaching is set while attachOps settles the records of a hub that is
	// already serving: hosts may hold connections then, so every step that
	// changes a live host's registration holds the host gates first.
	attaching atomic.Bool
	// sources is the component-05 registry; a remote host's source is where
	// attachment state (Online) and the per-host client live.
	sources *appsource.Registry
	// remoteCache is the controller's remote-thread snapshot cache. Remove
	// prunes the removed host's rows from it — and drops its registration
	// generation, so a refresh in flight during the remove cannot republish
	// them — so its sessions stop rendering with the removal instead of
	// lingering live until the refresher's next tick. registerSource assigns
	// the name a fresh generation when a remove/re-add registers it again,
	// so a walk captured under the old registration cannot publish the old
	// host's rows under the new identity while the re-added host's own walks
	// publish normally. Nil (tests,
	// embedders without a cache): every tree read then walks the live
	// sources per request, and a removed host — no source — contributes no
	// rows on its own.
	remoteCache *hubcore.RemoteThreadCache
	// forgetLastGoodThreads drops the web server's retained last-known-good
	// rows for a source. Remove calls it in the finish phase beside
	// remoteCache.RemoveSource: the background walk stores the host's last
	// successful list under its name, and the removal must take that
	// retention with it too — left behind, the entry and its thread rows
	// outlive the host for the process lifetime, and churning distinct host
	// names grows the map without bound. Nil (tests,
	// embedders without a web server): nothing was ever retained, so there
	// is nothing to forget.
	forgetLastGoodThreads func(sourceID string)
	// lastGoodThreads is the getter twin of forgetLastGoodThreads: it returns a
	// copy of the web server's retained last-known-good rows for a source.
	// Remove calls it in its commit phase — before the finish phase's forget
	// drops the rows — to capture the rows a removal's tombstone retains
	// (registry spec 08 §15: the tombstone carries "the retained projection of
	// the source's last-known-good rows"). Nil (tests, embedders without a web
	// server): the tombstone is written with no retained rows.
	lastGoodThreads func(sourceID string) []appwire.Thread
	// manager owns every live SSH channel; removal goes through its atomic,
	// gate-inheriting RemoveHostUnderGate — under the mutation's own held
	// reservation, so a concurrent attach cannot publish past deregistration and
	// no gate waiter can acquire a half-torn-down host (spec 08 §4's "gate
	// released last") — and the row path resolves the entry's attached client through its
	// ChannelIfAttached so a row can pair the channel with the very
	// registration it renders (attachedClient). Nil in tests that only
	// exercise validation.
	manager *sshconn.Manager
	// client is the Ensure-backed dialing seam a new source's client func
	// uses (cfg.RemoteHostClient). Nil (tests, embedders) leaves the source
	// on the detached refusal remoteClientFor serves.
	client func(ctx context.Context, host string) (*appwire.Client, error)
	// online reports whether the controller's channel to host is currently
	// attached. Nil leaves every remote host to the source registry's own
	// Online (tests).
	online func(host string) bool
	// clientIfAttached is the non-dialing attached-only client lookup list
	// and status fall back to when no manager owns the channels (tests,
	// embedders): it answers by name alone, with no channel registration to
	// pair against the entry the row renders. Nil leaves rows without live
	// facts.
	clientIfAttached func(host string) (*appwire.Client, bool)
	// handshake returns the attach handshake facts for the channel behind
	// client, or false when no live channel backs it. Nil leaves rows
	// without handshake identity.
	handshake func(host string, client *appwire.Client) (appwire.InitializeResponse, bool)
	// facts returns the preflight facts for the connection behind client.
	// Nil leaves rows without preflight facts.
	facts func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error)
	// planFacts refreshes one host's preflight facts for evener/host/plan
	// (deploy pipeline 08b §6 step 1). It is the ungated refresh a plan is built
	// from: the handler calls it on the attached channel and refuses the
	// no-token `refresh-failed` arm on any error. Nil (tests, embedders, and a
	// hub with no live channel) refuses `refresh-failed`.
	planFacts func(ctx context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error)
	// planProbe probes one host's running state for evener/host/plan (§6 step
	// 2), given the client the handler resolved for the host's live channel. Nil
	// refuses the no-token `handler-absent` arm — the honest state until
	// evener/host/running ships and the gate slice wires the gated probe.
	// epoch is the durable probe epoch the plan persisted first, presented on
	// the wire (never a default, never absent).
	planProbe func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error)
	// planControllerDirty reports whether the running controller's build is
	// unverifiable (built from a dirty tree), the §6 terminal `controller-dirty`
	// arm's condition. Nil reads buildinfo, the same signal the deploy paths
	// refuse on (sshconn's ErrControllerDirty).
	planControllerDirty func() bool
	// gate is THE per-host gate (deploy pipeline 08b §5): the one gate every
	// holder of a host's work contends on. In production it is the sshconn
	// Manager's own per-host lock, so `plan` try-acquires the very gate
	// `Ensure`, the reconnect supervisor, add/update, and remove take. With no
	// manager (tests, embedders) a standalone gate serves, so the handler still
	// try-acquires rather than proceeding ungated.
	gate hostops.Gate
	// bootID identifies this controller process incarnation for the durable
	// probe epochs `plan` persists (deploy pipeline 08b §6 step 2). Empty
	// refuses the probe: an epoch that cannot be bound to a boot is not a
	// usable epoch.
	bootID string
	// probeTimeout bounds one evener/host/running round trip in the plan's
	// gated probe (§6 step 2's "explicit owner-adjustable probe timeout").
	probeTimeout time.Duration
	// running is the evener/host/running handler's config (§10): this hub's own
	// build and process identity, the local restart-required predicate's roster,
	// and the state roots the write probe checks.
	running hostRunningConfig
	// deployHost and restartHost are the 04b execution seams the operation
	// workers run (deploy pipeline 08b §6): the deploy step with its guards and
	// the restart step, both called while the worker holds the host's gate. In
	// production they are the sshconn Manager's DeployForOperation and
	// RestartForOperation; a nil pair (tests, embedders) fails the operation's
	// worker with the refusal the missing seam means.
	deployHost  func(ctx context.Context, host hostreg.Host, facts sshconn.Preflight, restart bool, beforeMutation func() error, journal *sshconn.DeploymentJournal) (string, sshconn.Preflight, error)
	restartHost func(ctx context.Context, host hostreg.Host, facts sshconn.Preflight, beforeMutation func() error) (string, error)
	// recoverDeployment renews and recovers an unfinished deployment or saved
	// command before Restart attaches. Execution revalidates the bound entry
	// before each mutation phase. Nil result leaves ordinary attach-first intact.
	recoverDeployment func(context.Context, hostreg.Host, hostops.Holder, func() error) (*sshconn.RestartResult, error)
	// attachedFacts returns the attached channel's captured preflight facts —
	// the deploy path's facts source, since deploy runs no fresh preflight of
	// its own (§6 step 3), and the attach-first restart's probe source. Nil
	// (tests, embedders with no manager) leaves the workers to their own seam.
	attachedFacts func(name string) (sshconn.Preflight, bool)
	// attachUnderGate is the operation-owned attach/reattach seam (§6 seam (d)):
	// the worker's post-restart reattach (and an initially unattached restart's
	// attach-first), run under the caller's held gate by the sshconn Manager's
	// AttachUnderGate. It returns the post-verification handoff that starts the
	// channel's supervisor under the same held gate. Nil (a hub with no manager)
	// means no channels are owned to reattach, so the worker's arm is the no-op
	// success the interim wait had; running the normal attach path instead would
	// deadlock on the non-reentrant gate.
	// holder is the operation holder the worker's hold registered, which the
	// primitive requires the gate to currently carry.
	// explicit distinguishes the first attach (true: the first-attach bootstrap
	// applies, as Ensure's explicit attach) from the reconnect-shaped reattach
	// (false: never start a hub).
	attachUnderGate func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, explicit bool) (func() bool, error)
	// opsCtx is the controller-lifetime context the deploy/restart workers run
	// under (created on first use), opsCancel cancels it, and opsWG is the
	// workers' wait group the shutdown path waits on. opsMu guards the lazy
	// creation and the shutdown's read.
	opsCtx    context.Context
	opsCancel context.CancelFunc
	opsWG     sync.WaitGroup
	opsMu     sync.Mutex
	// runningProbeMu serializes evener/host/running's admission-plus-probe
	// window: the guard epoch row is hub-wide, so a concurrent call must not
	// advance the admitted epoch while another call is still probing under the
	// epoch it admitted. (The remote guard/lease protocol an earlier revision
	// named here was withdrawn with the crash-fencing program, comp08.)
	runningProbeMu sync.Mutex
	// state retains per-host attach state from the manager's lifecycle
	// events plus the last-known facts of the last attached render, so
	// offline and in-progress rows keep the metadata the wire contract
	// promises.
	state *hostAttachState
	// logf is the hub's logging path for store load problems; nil drops
	// the lines (tests that never load a broken file).
	logf func(format string, args ...any)
	// mu serializes add/remove/update read-modify-write cycles so concurrent calls
	// cannot lose updates or interleave a save with a registry mutation.
	mu sync.Mutex
	// mutating holds the names with a remove or update in flight: both mark the
	// name in their commit phase, after the durable change landed and the store
	// row moved with it, and clear it in their finish phase. Add needs no mark
	// because its whole mutation is atomic under mu, but it refuses a name that
	// remove or update already marked. The window a mutation releases the mutex
	// for — the post-commit teardown, which can be slow — must admit no second
	// mutation of the same name, so every mutation refuses a marked name until
	// its own finish clears the mark. The per-host reservation is held across
	// that same window (spec 08 §4's "gate released last"), so gate consumers
	// (attach, plan, deploy, another mutation) are refused busy; the mark is the
	// fence for the paths a held reservation does not reach, `add` first, which
	// checks its mark before any gate acquisition. Guarded by mu.
	mutating map[string]struct{}
	// policy is the resolved host-record retention knob set (registry spec 08
	// §6/§15): the tombstone retention and bounds and the superseded-receipt,
	// pruned-marker, and keyless-audit bounds every hub.toml write and every
	// boot derives by. hostRecordPolicyFor applies the documented defaults, so
	// a zero WebConfig still prunes and evicts by the spec's numbers.
	policy hostRecordPolicy
	// now is the derivation's clock seam: nil means the wall clock, and a test
	// can pin removal and prune instants against the retention period.
	now func() time.Time
}

// newHubHostManager builds the manager over the live registries. hosts is the
// controller's live registry — in production the one *hostreg.Registry the
// SSH manager dials through and the attach handler validates against, so
// entries loaded here and hosts added at runtime are attachable
// without a restart; a nil hosts falls back to an empty registry rather than
// a panic. configPath is the selected hub.toml path ("" disables
// persistence); logf is the hub's logging path for store load problems.
//
// The durable host set starts as the registry's boot entries — every host
// lives in the one machine-managed hub.toml.
func newHubHostManager(sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hosts *hostreg.Registry, logf func(format string, args ...any)) *hubHostManager {
	if hosts == nil {
		hosts, _ = hostreg.New(nil)
	}
	m := &hubHostManager{cfg: &hostManagerConfig{
		hosts:            hosts,
		store:            &hostStore{},
		configPath:       strings.TrimSpace(configPath),
		records:          hubTOMLRecordStore{path: strings.TrimSpace(configPath)},
		opsUnavailable:   cfg.RemoteHostOpsUnavailable,
		sources:          sources,
		remoteCache:      cfg.RemoteThreadCache,
		manager:          manager,
		client:           cfg.RemoteHostClient,
		online:           cfg.RemoteHostOnline,
		clientIfAttached: cfg.RemoteHostClientIfAttached,
		handshake:        cfg.RemoteHostHandshake,
		facts:            cfg.RemoteHostFacts,
		planFacts:        cfg.RemoteHostPlanFacts,
		planProbe:        cfg.RemoteHostPlanProbe,
		gate:             hostGateFor(manager),
		bootID:           strings.TrimSpace(cfg.HubBootID),
		probeTimeout:     hostProbeTimeoutFor(cfg.HostProbeTimeout),
		running:          newHostRunningConfig(cfg),
		state:            newHostAttachState(),
		mutating:         map[string]struct{}{},
		policy:           hostRecordPolicyFor(cfg),
		logf:             logf,
	}}
	m.cfg.ops.Store(cfg.RemoteHostOpsStore)
	if m.cfg.planFacts == nil && manager != nil {
		// The production refresh seam (deploy pipeline 08b §6 step 1): the
		// ungated one-shot SSH preflight the manager runs against the host, read
		// now and stamped with the instant the read returned. Reading the live
		// channel's captured snapshot instead would let a token's freshness term
		// measure from the wrong instant — the snapshot was taken whenever the
		// channel attached, which can be arbitrarily long before the plan.
		//
		// It is deliberately an SSH execution rather than a read of the attached
		// channel's snapshot: §6 step 1 defines the refresh that way ("the refresh
		// is an SSH command execution, not [the probe]"), precisely so a plan's
		// facts are read now instead of inherited from whenever the channel
		// attached. The identity pairing the two round-trips could otherwise
		// blur — one connection's facts against another registration's probe — is
		// pinned twice: this seam refuses a preflight that answers for a different
		// host name, and the plan's fence re-checks that the entry both legs were
		// resolved from is still the registry's registration before it mints, so
		// an entry that moved mid-plan refuses rather than pairing two
		// registrations. A refresh that fails (an unreachable host, a dropped
		// channel's transport) is the no-token `refresh-failed` arm, never a mint
		// from facts nothing read.
		m.cfg.planFacts = func(ctx context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
			preflight, err := manager.Preflight(ctx, host)
			if err != nil {
				return hubcore.HostPlanFacts{}, err
			}
			if preflight.Host != host.Name {
				return hubcore.HostPlanFacts{}, fmt.Errorf("the preflight answered for host %q, not %q", preflight.Host, host.Name)
			}
			return hostPlanFactsFromPreflight(host, preflight, time.Now().UTC()), nil
		}
	}
	if m.cfg.planProbe == nil && manager != nil {
		// The production gated probe (deploy pipeline 08b §6 step 2): the
		// gate-aware primitive over sshManager.ChannelIfAttached, presenting the
		// durable epoch the plan persisted and bounded by the owner-adjustable
		// probe timeout. It inherits the already-held gate and never re-acquires
		// it.
		m.cfg.planProbe = func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return probeHostRunning(ctx, manager, host, client, epoch, m.cfg.probeTimeout)
		}
	}
	if manager != nil {
		// The Ensure path's deploy step is recorded as a durable operation
		// (deploy pipeline 08b §6): the hub's recorder mints the record and
		// publishes it as the gate holder, so an Ensure-triggered deploy is
		// fenced and named exactly like a user one.
		manager.SetEnsureDeployHook(m.EnsureDeploy)
		// A restart-only Ensure attempt (a stale running build, or a recorded
		// pendingRestart) is its own durable operation for the same reason
		// (§6), so its ssh subprocesses are armed under the record too.
		manager.SetEnsureRestartHook(m.EnsureRestart)
	}
	if m.cfg.deployHost == nil && manager != nil {
		// The production deploy step (deploy pipeline 08b §6): the 04b deploy
		// path with its guards intact, run by the operation's worker while it
		// holds the host's gate.
		m.cfg.deployHost = manager.DeployForOperation
	}
	if m.cfg.restartHost == nil && manager != nil {
		// The production restart step: the 04b restart path — user-versus-system
		// unit decision and the proven-replacement wait — run by the worker
		// under the same gate.
		m.cfg.restartHost = manager.RestartForOperation
	}
	if m.cfg.recoverDeployment == nil && manager != nil {
		m.cfg.recoverDeployment = manager.RecoverDeploymentForRestart
	}
	if m.cfg.attachUnderGate == nil && manager != nil {
		// The production operation-owned reattach (§6 seam (d)): the manager's
		// gate-aware attach primitive, which accepts the already-held gate,
		// suppresses supervisor startup until the post-verification handoff, and
		// never re-acquires the non-reentrant lock.
		m.cfg.attachUnderGate = func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, explicit bool) (func() bool, error) {
			_, handoff, err := manager.AttachUnderGate(ctx, entry, holder, explicit)
			if err != nil {
				return nil, err
			}
			return handoff, nil
		}
	}
	if m.cfg.attachedFacts == nil && manager != nil {
		// The attached channel's captured preflight is the deploy path's facts
		// source (deploy runs no fresh preflight of its own) and the attach-first
		// restart's probe source. PreflightIfAttached never dials.
		m.cfg.attachedFacts = manager.PreflightIfAttached
	}
	m.bootRecords()
	return m
}
