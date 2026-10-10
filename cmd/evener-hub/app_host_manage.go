package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appserver"
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

// legacyHostSidecarFileName is the retired sidecar beside the selected
// hub.toml. It is read once by the boot migration — never written — and set
// aside under legacyHostSidecarAsideSuffix on success.
const legacyHostSidecarFileName = "hub.hosts.json"

// legacyHostSidecarAsideSuffix names the retired sidecar once its entries are
// folded into hub.toml: the file is renamed aside, never deleted, so its bytes
// survive for recovery while a later removal of a name it carried can never
// resurrect through it.
const legacyHostSidecarAsideSuffix = ".migrated"

// legacySidecarMigratedKey is the machine-managed key a successful migration
// records in hub.toml. It is what makes the retirement authoritative rather
// than only the rename's durability: on a filesystem whose directories cannot
// be synced (the tolerance hubTOMLSyncDir shares with the hub's other stores), a
// power loss can bring hub.hosts.json back after the UI has removed a host it
// carried — and a later boot that finds the marker in hub.toml knows the sidecar
// has already been folded in, so it ignores the stale file instead of
// re-merging names the UI removed. The marker rides the same atomic write as the
// merged host set, so any mutation that survives proves it survived too.
const legacySidecarMigratedKey = "legacy_sidecar_migrated"

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
	// ops is the operation store the per-host boundary records are mirrored
	// into (registry spec 08 §7): the triple each hub.toml write commits is
	// mirrored behind it, in one atomic store write. Nil (tests, embedders,
	// and a hub whose operation store failed to open) disables mirroring; the
	// mirror is a copy of hub.toml's records, never their authority.
	ops *hostops.Store
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
	// refuse on (sshconn's errControllerDirty).
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
	deployHost  func(ctx context.Context, host hostreg.Host, facts sshconn.Preflight) (string, sshconn.Preflight, error)
	restartHost func(ctx context.Context, host hostreg.Host, facts sshconn.Preflight) error
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
	// remnantFence reports the open teardown remnant fencing a host name, if
	// one does: §6 step 2's `remnant-open` refusal is emitted from here, past
	// the dedup check and before any probe or acquisition. Remnant semantics
	// are S12's (registry spec §6); no remnant store exists yet, so a nil seam
	// answers "no remnant".
	remnantFence func(name string) (string, bool)
	// lastKnownPublish, when set, publishes the post-operation refresh's
	// verified facts into the (generation, incarnation id)-scoped last-known
	// store (§6 seam (c)'s second half). That store is its own slice's and is
	// not built here: the seam is the routing point, and nil is the honest
	// "nowhere to publish yet".
	lastKnownPublish func(entry hostreg.Host, probe hubcore.HostRuntimeProbe, facts hubcore.HostPlanFacts) error
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

// pendingHostReceipt is one receipt a single hub.toml write carries beyond the
// store's own set: the mutation the write is committing names it, and the
// store adopts it only once the write returned success (persistOrCompensate).
type pendingHostReceipt struct {
	Key     string
	Receipt HostMutationReceipt
}

// hostPersistChange is the set of machine records one hub.toml write stages
// beyond the store's own set: the mutation's receipt and, for a removal, the
// tombstone the write persists. The derivation merges both into the record set
// it writes, and a successful write installs the derived set — so a failed
// write, and a compensation behind it, leave the store exactly as it was.
type hostPersistChange struct {
	receipt   *pendingHostReceipt
	tombstone *hostTombstoneStage
	// marker, remnant, resolved, and attempt are the teardown-repair records
	// one write stages beyond the store's own set: the staged-receipt marker a
	// commit's step-(2) write carries and its flip writes move, the durable
	// remnant a committed-with-teardown-failure writes in the write that
	// finalizes its receipt, the typed resolved record a clearance writes, and
	// the attempt record a `teardown-retry`/`teardown-recover` claim writes.
	// Like the receipt and the tombstone they are merged by the derivation and
	// installed only by a successful write, so a failed write and the
	// compensation behind it leave the store exactly as it was.
	marker        *pendingHostMarker
	remnant       *pendingHostRemnant
	resolved      *pendingHostRemnant
	attempt       *pendingHostAttempt
	fencedAttempt *pendingHostAttempt
	// carryReceipts are receipts this write carries beyond the store's own set
	// and its own commit's: the collision write adopts the file's bytes when a
	// foreign edit won the race, and must adopt that file's records with it
	// rather than drop them as an owned name's superseded history.
	carryReceipts map[string]HostMutationReceipt
	// dropReceipt names the scoped receipt key this write removes: a
	// compensation must take the provisional receipt its own staged write
	// installed with it, or an un-committed mutation leaves a receipt behind
	// that a later replay reads as a recorded outcome (S10's un-commit paths
	// dropped it the same way).
	dropReceipt string
	// dropMarker names a host whose staged-receipt marker this write removes:
	// the post-commit write that replaces the marker with the finalized receipt
	// is a write that carries no marker for its host, and the derivation must
	// drop the stored one rather than preserve it.
	dropMarker string
	// storeSync, when set, is the cross-file commit intent this write carries
	// (deploy-pipeline 08b §9): a removal's staged write stages the exact store
	// rows it is about to purge. dropStoreSync names a host whose intent this
	// write removes — the follow-up write that clears it once the purge landed,
	// and the compensation that drops it with the restored hub.toml.
	storeSync     *pendingHostStoreSync
	dropStoreSync string
	// highWaterRaises carries the boot mirror pass's raised marks: the
	// discarded generations §4 preserves as the names' high-water marks. The
	// derivation merges them after the tombstone stage so a raise wins over the
	// file's older mark, and a tombstoned name's tombstone is raised with its
	// [generations] twin (validateHostTombstones requires the pair to agree).
	highWaterRaises map[string]HostGeneration
}

// pendingHostStoreSync is one cross-file intent a hub.toml write carries,
// keyed by the host name the intent is keyed by.
type pendingHostStoreSync struct {
	Name   string
	Intent HostStoreSyncIntent
}

// pendingHostMarker is the staged-receipt marker one hub.toml write carries,
// keyed by the host name the marker map is keyed by.
type pendingHostMarker struct {
	Name   string
	Marker HostStagedReceipt
}

// pendingHostRemnant is one teardown-remnant record (open or resolved) a
// hub.toml write carries.
type pendingHostRemnant struct {
	RemnantID string
	Remnant   HostTeardownRemnant
}

// pendingHostAttempt is one teardown-attempt record a hub.toml write carries.
type pendingHostAttempt struct {
	AttemptID string
	Attempt   HostTeardownAttempt
}

// hostStore is the durable host set: every live entry, in the order a rewrite
// writes it — the boot set in the registry's own (name-sorted) order, then
// entries in add order, with an edit replacing in place. The file's host order
// is the hub's, not the operator's: the machine-managed banner says formatting
// does not survive, and a rewrite normalizes a hand-authored out-of-order file.
// The zero value is usable; all methods are safe for concurrent use. Callers that also
// mutate the registry hold hostManagerConfig.mu across both, so the two cannot
// drift apart under concurrency.
type hostStore struct {
	mu      sync.Mutex
	entries []hostreg.Host
	// highWater is the per-name generation high-water record the file carries
	// for names with no live entry: a removal records the removed incarnation's
	// (generation, incarnation id, presence epoch) triple with the epoch the
	// removal advanced to, and it stays until the name's history is pruned
	// (retention belongs to a later slice). A live name's record is always
	// derived from its entry, so an entry here for a live name is inert. The
	// boot load seeds it from the file, so a removal that lands before the next
	// start keeps its high-water record.
	highWater map[string]HostGeneration
	// loadErr records why the durable host set cannot be treated as fully
	// loaded: a legacy sidecar failed to parse or validate, or its one-time
	// migration failed. While it is set the in-memory snapshot is known
	// incomplete, so writes refuse — rewriting hub.toml would clobber the
	// entries that never made it into memory — and add/remove fail loudly
	// instead of silently losing them.
	loadErr error
	// receipts is the durable mutation-receipt set the file carries, keyed by
	// the five-part scoped key (spec 08 §5/§6; app_host_receipts.go). Boot
	// loads it from the file's [mutation_receipts] tables, a committed
	// mutation adds its own under the mutation mutex, and a mutation that
	// un-commits drops the one its commit wrote — so the map never names a
	// mutation whose commit did not stand. Unlike highWater the map is not
	// keyed by name alone: one name's receipts span its generations, and the
	// scoped key is what dedup matches.
	receipts map[string]HostMutationReceipt
	// tombstones is the durable removed-host set the file carries, keyed by
	// name (spec 08 §15; app_host_tombstones.go). Boot loads it from the
	// file's [tombstones] tables (validated there), a removal's commit
	// installs the tombstone its atomic write persisted, and a re-add or a
	// retention/capacity purge removes it — so the map never names a tombstone
	// the file does not carry.
	tombstones map[string]HostTombstone
	// prunedReceipts is the durable pruned-marker set the file carries, keyed
	// by the full five-part scoped receipt key (spec 08 §6): the bounded proof
	// that a superseded receipt was compacted away, so a replay naming the
	// marked key refuses `stale-entry` instead of fresh-applying.
	prunedReceipts map[string]PrunedReceiptMarker
	// stagedReceipts is the durable staged-receipt marker set the file carries,
	// keyed by host name (spec 08 §5; app_host_remnants.go): at most one staged
	// entry per host, so a staged marker for host A never blocks a mutation on
	// host B. A commit's step-(2) write installs its own marker here, the flip
	// writes move it, and the post-commit write replaces it with the finalized
	// receipt.
	stagedReceipts map[string]HostStagedReceipt
	// remnants is the durable teardown-remnant set the file carries, keyed by
	// the server-generated remnantId (spec 08 §6; app_host_remnants.go). An open
	// remnant fences its host name for every lifecycle and attach path; a
	// cleared one stays as the typed resolved record the `already-cleared` and
	// `recovered-cleared` replays render.
	remnants map[string]HostTeardownRemnant
	// attempts is the durable teardown-attempt set the file carries, keyed by
	// the server-generated attempt id: the claim `teardown-retry` and
	// `teardown-recover` write in the same atomic hub.toml write that claims the
	// remnant, and the fence a timed-out run leaves standing.
	attempts map[string]HostTeardownAttempt
	// storeSync is the durable cross-file commit intent set the file carries,
	// keyed by host name (deploy-pipeline spec 08b §9): the exact store rows a
	// removal's swap is deleting, plus the hub.toml generation the intent
	// belongs to. The commit's staged write installs it, the store purge applies
	// it after the swap, and a follow-up write drops it.
	storeSync map[string]HostStoreSyncIntent
}

// set installs entries as the store's contents; the constructor calls it once
// with the registry's boot set. The caller transfers ownership of the slice —
// the boot set is freshly built and held nowhere else — so set adopts it.
func (s *hostStore) set(entries []hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
}

// setHighWaterMap installs the file's high-water records at boot; the
// constructor calls it once with the records the file carried. The caller
// transfers ownership.
func (s *hostStore) setHighWaterMap(marks map[string]HostGeneration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.highWater = marks
}

// highWaterSnapshot returns copies of the retained high-water records, keyed by
// name. Callers hold hostManagerConfig.mu.
func (s *hostStore) highWaterSnapshot() map[string]HostGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.highWater)
}

// setReceipts installs the file's receipt set at boot; the constructor calls it
// once with the records the file carried. The caller transfers ownership.
func (s *hostStore) setReceipts(receipts map[string]HostMutationReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receipts = receipts
}

// addReceipt records one committed mutation's receipt. It is called only after
// the atomic write that persisted the commit returned success, so the map never
// names a receipt the file does not carry. Callers hold hostManagerConfig.mu.
func (s *hostStore) addReceipt(key string, receipt HostMutationReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipts == nil {
		s.receipts = map[string]HostMutationReceipt{}
	}
	s.receipts[key] = receipt
}

// receiptsSnapshot returns a copy of the stored receipt set, keyed by scoped
// key. It takes only the store's own mutex, so the dedup read can run before
// the host gate and the mutation lock (§5 orders dedup first); a writer's
// read-modify-write is serialized by the mutation mutex it already holds.
func (s *hostStore) receiptsSnapshot() map[string]HostMutationReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.receipts)
}

// setRecordMaps installs the file's tombstone and pruned-marker sets at boot;
// the constructor calls it once with the records the file carried. The caller
// transfers ownership.
func (s *hostStore) setRecordMaps(tombstones map[string]HostTombstone, prunedReceipts map[string]PrunedReceiptMarker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones = tombstones
	s.prunedReceipts = prunedReceipts
}

// setStoreSync installs the file's cross-file intent set at boot; the
// constructor calls it once with the intents the file carried. The caller
// transfers ownership.
func (s *hostStore) setStoreSync(intents map[string]HostStoreSyncIntent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeSync = intents
}

// storeSyncSnapshot returns a copy of the stored cross-file intent set, keyed
// by host name.
func (s *hostStore) storeSyncSnapshot() map[string]HostStoreSyncIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.storeSync)
}

// tombstoneSnapshot returns a copy of the stored tombstone set, keyed by name.
// It takes only the store's own mutex: `list`'s in-memory expiry filter and
// the tree merge read it without the mutation lock (spec 08 §4: "list takes no
// mutation lock").
func (s *hostStore) tombstoneSnapshot() map[string]HostTombstone {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.tombstones)
}

// entryByName returns the store's live entry for name, if it holds one.
func (s *hostStore) entryByName(name string) (hostreg.Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return hostreg.Host{}, false
}

// hasTombstone reports whether name is present solely as a tombstone — the
// re-add condition an `add` checks under the mutation mutex before its write
// purges the tombstone.
func (s *hostStore) hasTombstone(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tombstones[name]
	return ok
}

// prunedReceiptSnapshot returns a copy of the stored pruned-marker set, keyed
// by the full scoped receipt key.
func (s *hostStore) prunedReceiptSnapshot() map[string]PrunedReceiptMarker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.prunedReceipts)
}

// installRecords replaces the store's machine-record maps with the set one
// successful hub.toml write persisted. The derivation and the write ran
// together, so installing the same projection keeps the file and the store the
// same set; a failed write installs nothing. The tombstone and marker maps are
// always installed whole (the derivation owns them), while the high-water and
// receipt maps are installed only when non-nil so a caller cannot silently
// blank a section it did not derive. Callers hold hostManagerConfig.mu.
func (s *hostStore) installRecords(records hostTOMLRecords) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones = records.tombstones
	s.prunedReceipts = records.prunedReceipts
	s.stagedReceipts = records.stagedReceipts
	s.remnants = records.remnants
	s.attempts = records.attempts
	s.storeSync = records.storeSync
	if records.highWater != nil {
		s.highWater = records.highWater
	}
	if records.receipts != nil {
		s.receipts = records.receipts
	}
}

// poison records err as the reason the durable host set is not fully loaded.
// The first reason wins; later ones only add log lines at the call site.
func (s *hostStore) poison(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr == nil {
		s.loadErr = err
	}
}

// poisoned reports why the durable host set cannot be trusted as fully loaded,
// or nil when every entry is loaded (or there is nothing to migrate).
func (s *hostStore) poisoned() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// legacyHostSidecarFile is the on-disk shape of the retired sidecar: entries in
// add order. It is a migration input only; nothing writes it any more.
type legacyHostSidecarFile struct {
	// Hosts is a pointer so the shape gate is one decode: absent and null both
	// leave it nil, while a present array — empty included — sets it.
	Hosts *[]legacyHostSidecarFileEntry `json:"hosts"`
}

// legacyHostSidecarFileEntry is one persisted sidecar entry: the seven
// HostConfig fields in snake_case (hub.toml's spelling for the same fields)
// plus the key path. The retired sidecar never crossed the wire, so it follows
// the repo's snake_case json default; the appwire package's camelCase HostRow
// is what clients see.
type legacyHostSidecarFileEntry struct {
	Name       string   `json:"name"`
	SSH        string   `json:"ssh"`
	User       string   `json:"user,omitempty"`
	EvenerPath string   `json:"evener_path,omitempty"`
	ConfigPath string   `json:"config_path,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	KeyPath    string   `json:"key_path,omitempty"`
}

// legacySidecarPathFor returns the retired sidecar's path beside the selected
// hub.toml: the one-time migration's input. An empty config path (tests,
// embedders without a file) disables persistence: the store stays memory-only
// and every method still works.
func legacySidecarPathFor(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), legacyHostSidecarFileName)
}

// loadLegacyHostSidecar reads path into entries in file order. A missing file
// is no sidecar, not an error: a hub that never added a host has no file.
func loadLegacyHostSidecar(path string) ([]hostreg.Host, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read legacy host sidecar: %w", err)
	}
	return parseLegacyHostSidecar(data)
}

// parseLegacyHostSidecar decodes the retired sidecar's bytes in file order: the
// one-time migration's parse. A present document must
// carry a "hosts" array: an absent or null field is a load error, not an empty
// sidecar — a present empty array is the legitimate one. `hosts` is also the
// only record a sidecar may carry: any other top-level record is a shape this
// series never shipped, and the migration refuses it (keys named) rather than
// folding the hosts and silently stripping records the operator may need.
func parseLegacyHostSidecar(data []byte) ([]hostreg.Host, error) {
	// The shape gate rides the typed decode: `hosts` must be present and
	// non-null, which is exactly "the pointer is non-nil" — absent and null
	// both leave it nil, while a present array (empty included) sets it. A
	// JSON-valid document without a usable array — {} or {"hosts":null} —
	// must be a load error, not an empty sidecar: loading it as "no entries"
	// would let the migration rewrite hub.toml from an empty snapshot,
	// silently discarding whatever the broken document carried. Loud, file
	// preserved, the corrupt-file contract.
	var file legacyHostSidecarFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse legacy host sidecar: %w", err)
	}
	if file.Hosts == nil {
		return nil, errors.New(`parse legacy host sidecar: missing or null "hosts" array`)
	}
	// Only `hosts` is a recognized record: a document carrying anything else
	// fails the migration loudly, with the unrecognized keys named — nothing
	// merges, both files stay untouched, and the operator archives or repairs
	// the file. The pre-decision revision sketched a records-bearing sidecar
	// (tombstones, generations, receipts, remnants, markers); folding its
	// hosts while ignoring the rest would strip exactly those records.
	var records map[string]json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse legacy host sidecar: %w", err)
	}
	var unknown []string
	for key := range records {
		if key != "hosts" {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, fmt.Errorf("parse legacy host sidecar: unrecognized records %s — only \"hosts\" is migrated; archive or repair the file (nothing was migrated)", strings.Join(unknown, ", "))
	}
	entries := make([]hostreg.Host, 0, len(*file.Hosts))
	for _, h := range *file.Hosts {
		entries = append(entries, hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
			KeyPath:    h.KeyPath,
		})
	}
	return entries, nil
}

// writeHubTOMLHosts rewrites path in place with the machine-managed banner at
// its top and entries as its [[hosts]] tables, preserving every other key the
// file already holds — the operator's addr, provider, and plugin settings are
// data, and a rewrite must never drop them. The produced bytes are re-parsed
// through the loader's own decode before anything reaches disk, so a hub.toml
// this function writes is always one the hub can read back at boot; a file that
// cannot be read or parsed refuses the write instead of being clobbered.
//
// The write is atomic: a 0600 temp file in the same directory, fsynced,
// renamed over the target, and the directory itself synced after the rename, so
// a crash lands the old file or the new one, never a half-write or a lost
// rename — without the directory sync, a successful add/remove could still
// vanish in a power failure despite the synced temp file. The rename is the
// write's commit point: a failure before it writes nothing, while a failure
// behind it (hubTOMLPostRenameError) means the file already holds the new
// entries and the caller owes the live state a compensation. 0600 keeps key
// paths from ever landing world-readable; a hub.toml that predates the storage
// decision keeps the operator's mode until the hub's first rewrite. An empty
// path (no config file) skips the write; the in-memory store stays
// authoritative for the process lifetime.
func writeHubTOMLHosts(path string, entries []hostreg.Host) error {
	return writeHubTOMLHostsKnown(path, entries, nil, nil, nil, false)
}

// writeHubTOMLHostsMarked is writeHubTOMLHosts plus the migration's marker:
// when migrated is true the same atomic write records
// legacySidecarMigratedKey, so the rewritten file itself says the retired
// sidecar has been folded in.
func writeHubTOMLHostsMarked(path string, entries []hostreg.Host, migrated bool) error {
	return writeHubTOMLHostsKnown(path, entries, nil, nil, nil, migrated)
}

// writeHubTOMLHostsKnown is the one writer. known is the set the rewrite is
// derived from — the store's pre-mutation snapshot — and it is what lets the
// writer tell a hand-added entry from an entry the mutation deliberately
// removed: only file entries whose names are absent from known are carried
// through as extras. A nil known disables preservation entirely (an exact
// write), which the exact-write entry points below use; a store snapshot is
// never nil, even when the store is empty — an empty snapshot must still
// preserve hand-added file entries, and nil would silently drop them.
//
// highWater carries the retained per-name generation high-water records for
// names this write does not carry a live entry for (a removal's record); nil
// means the write carries none, and every other record the file holds for a
// name the write does not own is preserved verbatim, exactly as the entries
// are.
//
// receipts is the mutation-receipt set this write carries, keyed by the scoped
// receipt key: the store's own set, plus — for a commit's single atomic write —
// the receipt of the mutation this write is committing. In preservation mode
// (known non-nil) the file's receipts for names this write does not own ride
// through untouched, exactly as its records do; a nil known is the exact-write
// sentinel and the set is written as given, so nil there writes no section.
func writeHubTOMLHostsKnown(path string, entries, known []hostreg.Host, highWater map[string]HostGeneration, receipts map[string]HostMutationReceipt, migrated bool) error {
	return writeHubTOMLHostsRecords(path, entries, known, hostTOMLRecords{highWater: highWater, receipts: receipts}, migrated)
}

// writeHubTOMLHostsRecords is the one writer, carrying every machine-managed
// record set this build writes: the per-host records and high-water marks, the
// mutation receipts, the removed-host tombstones, and the pruned markers. The
// manager's persist path builds the set with deriveHostTOMLRecords (which has
// already applied retention, capacity, and compaction) and installs the same
// set on success; the small wrappers above are the boot-migration and
// exact-write entry points.
func writeHubTOMLHostsRecords(path string, entries, known []hostreg.Host, records hostTOMLRecords, migrated bool) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	doc, raw, err := readHubTOMLDocument(path)
	if err != nil {
		return err
	}
	// One decode of the file's current bytes serves both preservation rules —
	// the hand-added entries the write carries through and the records it
	// carries through with them — and a write that preserves neither (the exact
	// writes, both sentinels nil) skips it: the round-trip check below still
	// holds the bytes it writes to the loader's rules. A nil known is the
	// exact-write sentinel (no snapshot: preserve nothing); an empty known is a
	// real empty snapshot, and preservation still applies.
	var fileCfg Config
	if known != nil || records.highWater != nil {
		fileCfg, err = decodeHubTOMLForRewrite(path, raw)
		if err != nil {
			return err
		}
	}
	var extra []HostConfig
	if known != nil {
		extra = fileOnlyHostEntries(fileCfg, known, entries)
	}
	tables := append(hubTOMLHostTables(entries), extra...)
	if len(tables) == 0 {
		// Write no `hosts` key at all for an empty set: `hosts = []` cannot be
		// reopened as an array of tables, so a later operator hand-add of a
		// `[[hosts]]` table would make the file unreadable at the next boot.
		// The banner-only shape stays hand-editable.
		delete(doc, "hosts")
	} else {
		doc["hosts"] = tables
	}
	// The preservation rule skips the names whose records this very derivation
	// raised (the boot mirror pass's discarded generations): their [generations]
	// record and tombstone twin are the raise, and the file's older copy must
	// not ride back over either.
	preserved := records.droppedTombstones
	if len(records.raisedHighWater) > 0 {
		preserved = make(map[string]struct{}, len(records.droppedTombstones)+len(records.raisedHighWater))
		maps.Copy(preserved, records.droppedTombstones)
		maps.Copy(preserved, records.raisedHighWater)
	}
	hostRecords, generations := hubTOMLRecordTables(fileCfg, entries, known, records.highWater, preserved)
	if len(hostRecords) == 0 {
		delete(doc, "host_records")
	} else {
		doc["host_records"] = hostRecords
	}
	if len(generations) == 0 {
		delete(doc, "generations")
	} else {
		doc["generations"] = generations
	}
	receiptTables := hubTOMLReceiptTables(fileCfg, entries, known, records.receipts, records.droppedReceipts)
	if len(receiptTables) == 0 {
		delete(doc, "mutation_receipts")
	} else {
		doc["mutation_receipts"] = receiptTables
	}
	tombstoneTables := hubTOMLTombstoneTables(fileCfg, entries, known, records.tombstones, preserved)
	if len(tombstoneTables) == 0 {
		delete(doc, "tombstones")
	} else {
		doc["tombstones"] = tombstoneTables
	}
	prunedTables := hubTOMLPrunedReceiptTables(fileCfg, entries, known, records.prunedReceipts, records.droppedMarkers)
	if len(prunedTables) == 0 {
		delete(doc, "pruned_receipts")
	} else {
		doc["pruned_receipts"] = prunedTables
	}
	stagedTables := hubTOMLStagedReceiptTables(fileCfg, entries, known, records.stagedReceipts, records.droppedStaged)
	if len(stagedTables) == 0 {
		delete(doc, "staged_receipts")
	} else {
		doc["staged_receipts"] = stagedTables
	}
	storeSyncTables := hubTOMLStoreSyncTables(fileCfg, entries, known, records.storeSync, records.droppedStoreSync)
	if len(storeSyncTables) == 0 {
		delete(doc, "pending_store_sync")
	} else {
		doc["pending_store_sync"] = storeSyncTables
	}
	mirrorTables := hubTOMLMirrorCommitTables(fileCfg, entries, known, generations)
	if len(mirrorTables) == 0 {
		delete(doc, "mirror_commits")
	} else {
		doc["mirror_commits"] = mirrorTables
	}
	remnantTables := hubTOMLTeardownRemnantTables(fileCfg, entries, known, records.remnants, records.droppedRemnants)
	if len(remnantTables) == 0 {
		delete(doc, "teardown_remnants")
	} else {
		doc["teardown_remnants"] = remnantTables
	}
	attemptTables := hubTOMLTeardownAttemptTables(fileCfg, entries, known, records.attempts, records.remnants, records.droppedAttempts)
	if len(attemptTables) == 0 {
		delete(doc, "teardown_attempts")
	} else {
		doc["teardown_attempts"] = attemptTables
	}
	if migrated {
		doc[legacySidecarMigratedKey] = true
	}
	var buf bytes.Buffer
	buf.WriteString(hostTOMLBanner)
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return fmt.Errorf("marshal hub.toml: %w", err)
	}
	data := buf.Bytes()
	// The rewrite is only as good as its round trip: run the bytes through the
	// loader's own decode before anything lands, so a file the hub would refuse
	// at boot can never be written by a mutation.
	if _, err := decodeConfig(path, string(data)); err != nil {
		return fmt.Errorf("hub.toml rewrite refused: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("hub.toml mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hub.toml-*.tmp")
	if err != nil {
		return fmt.Errorf("hub.toml temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hub.toml chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hub.toml write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hub.toml sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hub.toml close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("hub.toml rename: %w", err)
	}
	// The rename is only durable once the directory entry that carries it is
	// synced too: a crash right after it could otherwise lose the committed
	// add or remove despite the synced temp file above. Some filesystems
	// cannot sync a directory at all; the tolerance inside the sync seam keeps
	// the save working there instead of turning a durability nicety into a
	// hard failure, the same way the hub's deletion and transcript-display
	// stores and the server's thread-clear journal treat this idiom.
	//
	// The rename above is the save's commit point: the target file already
	// holds the new entries, so a failure behind it cannot be returned as
	// though nothing was written. It is wrapped in hubTOMLPostRenameError,
	// and the callers compensate the live state (rollbackHubTOML) before
	// reporting the failure.
	if err := hubTOMLSyncDir(filepath.Dir(path)); err != nil {
		return &hubTOMLPostRenameError{err: err}
	}
	return nil
}

// readHubTOMLDocument loads path's current keys as a generic document, so a
// rewrite replaces only the `hosts` table and preserves every other key as
// data. A missing file is an empty document (the rewrite creates it); an
// unreadable or unparsable file refuses the write — the hub never clobbers a
// file it could not read.
func readHubTOMLDocument(path string) (map[string]any, []byte, error) {
	data, err := configReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil, nil
		}
		return nil, nil, fmt.Errorf("hub.toml rewrite read %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("hub.toml rewrite parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, data, nil
}

// hubTOMLHostTables renders entries as the file's [[hosts]] tables, in the very
// HostConfig shape loadConfig decodes (its omitempty tags keep a rewrite from
// inventing a key the operator, or the dialog that added the host, never set).
// One schema, so the reader and the writer cannot drift apart.
func hubTOMLHostTables(entries []hostreg.Host) []HostConfig {
	tables := make([]HostConfig, 0, len(entries))
	for _, e := range entries {
		tables = append(tables, HostConfig{
			Name:       e.Name,
			SSH:        e.SSH,
			User:       e.User,
			EvenerPath: e.EvenerPath,
			ConfigPath: e.ConfigPath,
			Addr:       e.Addr,
			Roots:      append([]string(nil), e.Roots...),
			KeyPath:    e.KeyPath,
		})
	}
	return tables
}

// hubTOMLSyncDir opens dir, syncs it, and closes it — the durability half
// of the atomic-rename idiom, so a crash right after a hub.toml rename cannot
// lose the committed add or remove. It is a swappable package variable so
// tests can force the post-rename failure path deterministically, the one
// point where a failed save has already replaced the target file; the default
// keeps the tolerance for filesystems that cannot sync a directory at all.
var hubTOMLSyncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("hub.toml directory: %w", err)
	}
	if err := d.Sync(); err != nil && !hubTOMLSyncUnsupported(err) {
		_ = d.Close()
		return fmt.Errorf("hub.toml directory sync: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("hub.toml directory close: %w", err)
	}
	return nil
}

// hubTOMLStashSyncDir is the stash write's own directory-sync seam. It starts
// as the hub.toml seam's value at process start and is separate so a test that
// swaps hubTOMLSyncDir to drive hub.toml's post-rename failure path leaves the
// stash write (which runs before the staged write) unaffected.
var hubTOMLStashSyncDir = hubTOMLSyncDir

// hubTOMLPostRenameError marks a write failure that followed the rename
// that replaced hub.toml: the new entries are already the file's
// contents — only the directory sync that makes the rename durable failed. A
// write returning one is not a refusal that wrote nothing: the callers must
// bring the file and the live set back into step (rollbackHubTOML) before
// returning the failure, so an add the API reports as failed cannot
// resurrect from the file on the next start, and a removal the API reports
// as failed cannot lose its host there.
type hubTOMLPostRenameError struct{ err error }

func (e *hubTOMLPostRenameError) Error() string { return e.err.Error() }
func (e *hubTOMLPostRenameError) Unwrap() error { return e.err }

// hubTOMLRenameCommitted reports whether err is a hub.toml write failure the
// rename already committed: the file was replaced before the failure, so the
// caller owes the live state a compensation, not a plain refusal.
func hubTOMLRenameCommitted(err error) bool {
	var post *hubTOMLPostRenameError
	return errors.As(err, &post)
}

// hubTOMLSyncUnsupported reports whether a sync failed because the
// filesystem cannot sync a directory at all. It delegates to the hub's one
// canonical predicate (internal/fsdurability); hubcore's deletion store and
// the server's thread-clear journal keep matching package-private copies of
// the same tolerance in their own modules.
func hubTOMLSyncUnsupported(err error) bool {
	return fsdurability.SyncUnsupported(err)
}

// add inserts entry at the end. Callers hold hostManagerConfig.mu.
func (s *hostStore) add(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// remove deletes name, reporting whether it was present. Removed stays
// removed: nothing is retained, so a later add of the same name starts clean.
// Callers hold hostManagerConfig.mu.
func (s *hostStore) remove(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.Name == name {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

// snapshot returns entries in add order; the slice is a copy, and it is never
// nil — not even for an empty store. nil is the writer's exact-write sentinel
// ("drop nothing back"), so an empty store that handed back nil would disable
// file-entry preservation, and the first Add on a host-less hub would silently
// drop an operator's hand-added entry. Callers hold hostManagerConfig.mu.
func (s *hostStore) snapshot() []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hostreg.Host, 0, len(s.entries))
	return append(out, s.entries...)
}

// without returns a copy of the entries minus name, in add order — the
// snapshot a removal persists before it mutates anything. Callers hold
// hostManagerConfig.mu.
func (s *hostStore) without(name string) []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hostreg.Host, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return out
}

// replace swaps entry in for the entry already stored under entry.Name, in
// place, so an edit is a minimal change to the write order rather than a
// reordering nothing asked for. Callers hold hostManagerConfig.mu.
func (s *hostStore) replace(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = replaceEntry(s.entries, entry)
}

// withReplaced returns the entries a durable replace would write: the stored
// entries with entry swapped in for the same-name one, in place. Callers hold
// hostManagerConfig.mu.
func (s *hostStore) withReplaced(entry hostreg.Host) []hostreg.Host {
	return replaceEntry(s.snapshot(), entry)
}

// replaceEntry swaps entry in for the same-name entry, in place, appending when
// the name is absent — unreachable on the update path, whose commit checks
// liveness first, but keeping this helper total rather than silently dropping an
// edit.
func replaceEntry(entries []hostreg.Host, entry hostreg.Host) []hostreg.Host {
	for i, e := range entries {
		if e.Name == entry.Name {
			entries[i] = entry
			return entries
		}
	}
	return append(entries, entry)
}

// hostAttachRecord is one host's retained attach state: what the lifecycle
// events say about an in-progress or failed attach, plus the last-known facts
// of the last row that rendered attached. retiredThrough is the highest entry
// generation this name's record has been retired through.
type hostAttachRecord struct {
	midAttach bool
	lastErr   string
	known     appwire.HostRow // only the fact fields are read back
	// retiredThrough fences the retained state to the entry generations that are
	// still live: content is served only to a row whose entry generation is
	// above this mark, and a row at or below it writes nothing back. It is the
	// generation of the retired entry an edit's swap replaced (retire), kept
	// monotonically non-decreasing so a later retirement cannot lower it. Zero
	// means no identity has been retired for this name, which is why a fresh
	// name's rows are never fenced.
	retiredThrough uint64
}

// hostAttachState retains per-host attach records from the SSH manager's
// lifecycle events and from attached rows this surface renders, so offline
// and in-progress rows keep the metadata the wire contract promises (a host
// mid-attach renders midAttach; a host that failed renders lastAttachError;
// an offline row keeps its last-known facts).
//
// It is generation-scoped, not merely name-keyed: an update retires the
// identity a name's record describes, and the retirement is marked by that
// identity's entry generation (retire). Rows carry the generation of the entry
// they were built from, and only a row above the mark is served or may write
// back. That is what makes an edit's clear immune to a row that captured the
// pre-swap entry before the update but only reaches its state write after the
// swap's retirement: its generation is at or below the mark, so its stale facts
// are neither folded into the row it is building nor recorded for later rows.
// Records for removed hosts are dropped wholesale — remove, the clean-slate
// path — so a re-add starts clean, its mark included.
type hostAttachState struct {
	mu      sync.Mutex
	records map[string]*hostAttachRecord
}

func newHostAttachState() *hostAttachState {
	return &hostAttachState{records: map[string]*hostAttachRecord{}}
}

// observe records one SSH lifecycle event. It only writes the map: it runs
// from sshconn's OnEvent with the per-host lock held, so it must never call
// back into the manager.
func (s *hostAttachState) observe(ev sshconn.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[ev.Host]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[ev.Host] = rec
	}
	switch ev.Kind {
	case sshconn.EventAttached:
		// A fresh attach supersedes the previous attempt's error and ends
		// the in-progress state.
		rec.midAttach = false
		rec.lastErr = ""
	case sshconn.EventFailed:
		rec.midAttach = false
		if ev.Err != nil {
			rec.lastErr = ev.Err.Error()
		}
	case sshconn.EventDetached:
		// A reconnecting detach does not end the in-progress attach: the
		// supervisor's link-drop path emits StateReconnecting immediately
		// before this event and keeps retrying through its backoff, so the
		// row keeps reporting the attach and the Connect control stays
		// disabled while the manager is already reconnecting, instead of
		// inviting a redundant manual attempt. Every other detach — a
		// teardown, a shutdown, a publish race — carries StateDisconnected
		// and ends the in-progress attach.
		if ev.State != sshconn.StateReconnecting {
			rec.midAttach = false
		}
	case sshconn.EventState:
		switch ev.State {
		case sshconn.StatePreflighting, sshconn.StateDeploying, sshconn.StateRestarting,
			sshconn.StateAttaching, sshconn.StateReconnecting:
			rec.midAttach = true
		default:
			rec.midAttach = false
		}
	}
}

// hostFactsValidity records which of an attached row's fact fields the live
// lookups actually refreshed: the handshake seam owns the server identity
// pair, the facts seam the hub/os/arch triple. The seams report one success
// flag per group, so validity is per lookup, not per field — a successful
// lookup's values are authoritative even when empty.
type hostFactsValidity struct {
	handshake bool
	facts     bool
}

// recordKnown keeps the facts of the last row that rendered attached, so the
// host's later offline rows still render them. Only the fields whose live
// lookup succeeded are written: a transient handshake or facts failure on an
// attached host leaves those fields empty in the row, and recording them would
// blank the previously retained facts — the offline rows that follow would
// lose the metadata the wire contract promises. gen is the entry generation the
// row was built from; a row at or below the name's retired mark describes an
// identity the record no longer belongs to, so it writes nothing — this is the
// write that a retirement fences out, and without it a late pre-swap row would
// recreate the retired identity's facts after the clear.
func (s *hostAttachState) recordKnown(row appwire.HostRow, validity hostFactsValidity, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec != nil && gen <= rec.retiredThrough {
		return
	}
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[row.Name] = rec
	}
	if validity.handshake {
		rec.known.ServerName = row.ServerName
		rec.known.ServerVersion = row.ServerVersion
	}
	if validity.facts {
		rec.known.HubVersion = row.HubVersion
		rec.known.OS = row.OS
		rec.known.Arch = row.Arch
	}
}

// apply folds the retained record into row: the attach state always, the
// last-known facts only when the row is not attached (an attached row's facts
// come from the live channel). gen is the entry generation the row was built
// from; a row at or below the name's retired mark folds nothing, so a stale
// pre-swap row renders clean instead of the retired identity's state.
func (s *hostAttachState) apply(row *appwire.HostRow, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec == nil || gen <= rec.retiredThrough {
		return
	}
	row.MidAttach = rec.midAttach
	row.LastAttachErr = rec.lastErr
	if !row.Attached {
		row.ServerName = rec.known.ServerName
		row.ServerVersion = rec.known.ServerVersion
		row.HubVersion = rec.known.HubVersion
		row.OS = rec.known.OS
		row.Arch = rec.known.Arch
	}
}

// retire drops the record's content for name and marks every row whose entry
// generation is <= gen as retired: such a row must neither be served the
// name's retained state nor write any back. gen is the generation of the entry
// an update retired, so a row that captured that entry — or any earlier one —
// is fenced, while a row built from the new identity's higher generation is
// served and records normally. The mark is monotonic (the max of the existing
// and the new generation) so a retirement can never lower it, and the record
// entry is kept (with its mark) so the mark survives even though the content is
// gone.
func (s *hostAttachState) retire(name string, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[name]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[name] = rec
	}
	if gen > rec.retiredThrough {
		rec.retiredThrough = gen
	}
	rec.midAttach = false
	rec.lastErr = ""
	rec.known = appwire.HostRow{}
}

// remove drops name's record; a removed or re-added host starts clean.
func (s *hostAttachState) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
}

// detach sweeps name's record and hands it back, for a caller that must commit
// nothing until its commit phases land: the record leaves the live map now (a
// re-added name must not inherit it) and a refusal restores it, so a refused
// add leaves the retained state exactly as it was.
func (s *hostAttachState) detach(name string) *hostAttachRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[name]
	delete(s.records, name)
	return rec
}

// restore puts back the record detach handed out — or leaves the entry absent
// when there was none.
func (s *hostAttachState) restore(name string, rec *hostAttachRecord) {
	if rec == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[name] = rec
}

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
	// testOnlyRemnantGated, when non-nil, marks names the tombstone retention
	// and capacity rules must treat as holding an open teardown remnant — the
	// exemption and the tombstone-capacity refusal path. Remnants are S12's
	// records and no remnant store exists yet, so production leaves this nil
	// and remnantGated (app_host_record_pruning.go) answers from
	// cfg.remnantFence when one is wired; S12 replaces this seam by wiring that
	// fence to the real records. It exists so the gated paths are falsified
	// NOW, the S10 testOnlyParkPostCommit precedent; nil in production.
	testOnlyRemnantGated func(name string) bool
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

// newHubHostManager builds the manager over the live registries. hosts is the
// controller's live registry — in production the one *hostreg.Registry the
// SSH manager dials through and the attach handler validates against, so
// entries loaded here and hosts added at runtime are attachable
// without a restart; a nil hosts falls back to an empty registry rather than
// a panic. configPath is the selected hub.toml path ("" disables
// persistence); logf is the hub's logging path for store load problems.
//
// The durable host set starts as the registry's boot entries — every host
// lives in the one machine-managed hub.toml — and a legacy sidecar, when one is
// present, is folded into hub.toml exactly once (migrateLegacyHostSidecar): its
// entries join the live set, hub.toml is rewritten with the merged set and the
// banner, and the retired file is set aside. A sidecar that fails to load or
// migrate, or a legacy entry that fails validation, is logged here and poisons
// writes: hub.toml keeps every entry it had until an operator fixes the file,
// instead of the next rewrite dropping the entries that never loaded.
func newHubHostManager(sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hosts *hostreg.Registry, logf func(format string, args ...any)) *hubHostManager {
	if hosts == nil {
		hosts, _ = hostreg.New(nil)
	}
	m := &hubHostManager{cfg: &hostManagerConfig{
		hosts:            hosts,
		store:            &hostStore{},
		configPath:       strings.TrimSpace(configPath),
		ops:              cfg.RemoteHostOpsStore,
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
	// The remnant fence is wired to the real record set (registry spec 08 §6):
	// every gate that consults it — the retention and capacity exemptions, the
	// boot collision rule, `deploy`/`restart`/`plan`, attach, and the
	// re-add/update/remove refusals — reads the durable open remnants rather
	// than a placeholder. A nil seam would answer "no remnant" and silently
	// disable the host-wide fence, so it is never left nil.
	m.cfg.remnantFence = m.openRemnantID
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
	// The file's records are read once, before anything else can mint: its
	// retained marks seed the counters, so a host folded in below mints above
	// every mark the file carries, and any live host the file carries no
	// identity for gets its pair recorded in the same boot. Seeding runs before
	// the store takes its snapshot, so the entries the snapshot carries are the
	// seeded ones — a registry built before the marks were known would otherwise
	// record its pre-seed epochs.
	fileRecords, hasFile := m.hostFileRecords()
	if hasFile {
		m.seedHighWater(fileRecords)
	}
	// The file's receipts are the dedup table's durable half: they are loaded
	// as-is — decodeConfig already refused any record this build cannot decode
	// — so a lost-response retry after a restart still finds the receipt its
	// commit wrote.
	m.cfg.store.setReceipts(fileRecords.MutationReceipts)
	m.cfg.store.set(hosts.All())
	// The file's tombstones and pruned markers are the other durable halves:
	// decodeConfig validated every record (the same hard startup-error posture
	// a corrupt host entry takes), so they are loaded as-is and restored
	// alongside the host entries (spec §15: "boot restores them alongside the
	// host entries, and they survive controller restarts").
	m.cfg.store.setRecordMaps(fileRecords.Tombstones, fileRecords.PrunedReceipts)
	// The cross-file commit intents load with the other machine-managed
	// sections: decodeConfig already validated each shape, so they are restored
	// as-is, and the boot pipeline below (or the next mutation's write) is what
	// converges them.
	m.cfg.store.setStoreSync(fileRecords.PendingStoreSync)
	// The teardown-repair records load with the other machine-managed sections;
	// decodeConfig already validated each shape, so they are restored as-is (the
	// same hard startup-error posture a corrupt host entry takes).
	m.cfg.store.setRemnantMaps(fileRecords.StagedReceipts, fileRecords.TeardownRemnants, fileRecords.TeardownAttempts)
	collided := false
	if hasFile {
		collided = m.breakBootTombstoneCollision(fileRecords)
	}
	// Boot finalizes each staged-receipt marker by the persisted phase and flags
	// (spec §5: "Boot finalizes each host entry by the persisted phase and flags
	// ... and the receipt records `bootRecovered: true`"). A marker is the
	// durable evidence of a commit whose post-commit write was lost, so boot
	// runs the phase-aware recovery and finalizes from the observed result
	// before the hub serves; a failure is logged and leaves the marker for the
	// next mutation-path write or the next boot.
	for _, name := range sortedStagedMarkerNames(m.cfg.store.stagedSnapshot()) {
		if m.cfg.ops != nil {
			if _, open := m.cfg.ops.Compensation(name); open {
				// §9's compensation owns this name's convergence only while the
				// commit path has not passed its commit point, and the file still
				// carrying a live intent for the name is exactly that state: the
				// compensation may restore, and finalizing the marker as
				// committed here would run its pinned teardown and write a
				// committed receipt for a commit the compensation is about to
				// undo. An intent already cleared means the commit stands, so the
				// marker finalizes like any other — never deferred behind a
				// record the pipeline below is about to clear without restoring.
				if _, intentLive := fileRecords.PendingStoreSync[name]; intentLive {
					m.logf("boot staged-receipt marker for %q left staged: an open compensation owns its convergence", name)
					continue
				}
				m.logf("boot staged-receipt marker for %q finalized: its compensation's intent is already cleared, so the commit stands", name)
			}
		}
		if _, err := m.finalizeOrphanMarkerIfAny(context.Background(), name, true); err != nil {
			m.logf("boot finalization of the staged receipt marker for %q refused: %v", name, err)
		}
	}
	migrated, err := m.migrateLegacyHostSidecar()
	if err != nil {
		// Loud, not fatal: the hub.toml hosts still serve. The store stays
		// poisoned so no later rewrite can land before the sidecar's entries
		// are folded in.
		m.logf("legacy host sidecar %s not migrated: %v", legacySidecarPathFor(m.cfg.configPath), err)
		m.cfg.store.poison(err)
	}
	if !migrated {
		// The boot's durable prune: expired tombstones, a tombstone a live
		// entry consumed, and any record compaction past its bound land in one
		// atomic write before the hub serves requests (spec §15: "every boot
		// prunes durably in the same atomic-write posture before serving
		// requests"; §6: "every `hub.toml` mutation and every boot compacts
		// markers past either bound in the same atomic write"). A boot with
		// nothing to prune writes nothing, so an untouched file stays
		// byte-identical. The migration's own write records every live host's
		// identity, so only a boot without that write can need the initial
		// records.
		if attempted := m.reconcileBootRecords(hasFile, collided); !attempted {
			m.materializeHostRecords(fileRecords, hasFile)
		}
	}
	// Spec 08b §7's boot passes, in their fixed order, before the
	// manager serves: the tombstone-derived host-removed pass, the bidirectional
	// generation-mirror reconciliation, and the cross-file intent
	// reconciliation (§9).
	m.reconcilePipelineBoot()
	return m
}

// breakBootTombstoneCollision applies spec §15's boot collision rule: a
// retained tombstone that collides with a newly live entry for the same name
// is consumed by the live entry, and the live host is rebased as a new
// incarnation — its restored generation set strictly above the tombstone's
// high-water mark, with a fresh incarnation id — before any historical receipt
// can be read as current. The tombstone itself drops in the boot's atomic
// write (the derivation's live-name purge). It reports whether any name was
// rebased, which forces the boot write.
//
// BOUNDARY (S12): §15's remnant arm of this rule — a colliding name that holds
// an open teardown remnant excludes the live entry from the live set as
// `blocked-pending-teardown` instead of rebasing — needs the remnant records
// S12 owns. The gate below is empty today (remnantGated), so no colliding name
// is skipped; S12 replaces it by wiring cfg.remnantFence.
func (m *hubHostManager) breakBootTombstoneCollision(records Config) bool {
	collided := false
	for _, entry := range m.cfg.store.snapshot() {
		if _, collides := records.Tombstones[entry.Name]; !collides {
			continue
		}
		{
			if remnantID, open := m.openRemnantID(entry.Name); open {
				// Spec §6's fenced arm: "a boot collision involving a
				// remnant-gated name leaves the open remnant resumable by
				// `remnantId`: the boot-collision above-mark bump is forbidden
				// while a remnant is open for that name — boot excludes the
				// colliding live entry from the live set as
				// `blocked-pending-teardown` (never published live) until
				// `teardown-retry` resolves it, so no live incarnation is ever
				// created over an open remnant". The name stays in hub.toml and
				// leaves the live set here; `teardown-retry` resolves the remnant
				// and the operator's next boot (or re-add) publishes it.
				m.logf("host %q is blocked-pending-teardown: open remnant %s fences the colliding live entry; resume it through evener/host/teardown-retry", entry.Name, remnantID)
				m.cfg.store.remove(entry.Name)
				if err := m.cfg.hosts.Remove(entry.Name); err != nil {
					m.logf("host %q not excluded from the live set: %v", entry.Name, err)
				}
				collided = true
				continue
			}
		}
		rebased, err := m.cfg.hosts.RebaseBootCollision(entry.Name)
		if err != nil {
			m.logf("boot collision on host %q not rebased: %v", entry.Name, err)
			continue
		}
		m.cfg.store.replace(rebased)
		// The name's name-keyed attach state is process state a fresh boot
		// cannot hold; nothing to clear here. The rebased pair is persisted by
		// the boot write the caller forces.
		collided = true
	}
	return collided
}

// reconcileBootRecords runs the boot's durable record reconciliation: it
// derives the record set the file should hold (retention prune, live-name
// tombstone consumption, receipt/marker/audit compaction), and when that set
// differs from the store's — or a boot collision rebased a live entry, which
// must reach the file before the hub serves — it persists it in one atomic
// write. It reports whether a write was attempted, so the caller knows not to
// run the separate materialize write.
//
// A derivation that refuses (a hand-edited file already over a global
// tombstone cap with every candidate remnant-gated) is logged and leaves the
// file alone; the next mutation surfaces the typed refusal to its caller.
func (m *hubHostManager) reconcileBootRecords(hasFile bool, collided bool) bool {
	if !hasFile || strings.TrimSpace(m.cfg.configPath) == "" || m.cfg.store.poisoned() != nil {
		return false
	}
	entries := m.cfg.store.snapshot()
	required := collided
	if !required {
		records, err := m.deriveHostTOMLRecords(entries, entries, hostPersistChange{})
		if err != nil {
			m.logf("boot host-record prune refused: %v", err)
			return false
		}
		required = !sameHostTOMLRecords(records, m.cfg.store.recordsSnapshot())
	}
	if !required {
		return false
	}
	if err := m.persistHosts(entries, entries, hostPersistChange{}); err != nil {
		m.logf("boot host-record prune for %s not written: %v", m.cfg.configPath, err)
	}
	return true
}

// seedHighWater seeds the store's retained high-water records plus the
// registry's counters from the file's [generations] tables: a name with no live
// entry keeps the triple the removal recorded, so a later rewrite carries it
// through instead of dropping the advance, and a re-add of it mints above the
// marks it left (spec 08 §1) — its generation above the file's and its presence
// epoch above the removal's advance, never back at 1.
func (m *hubHostManager) seedHighWater(cfg Config) {
	m.cfg.store.setHighWaterMap(cfg.Generations)
	m.cfg.hosts.SeedHighWater(hostHighWaterMarks(cfg))
}

// materializeHostRecords performs spec 08 §15's boot mint: a hub.toml host the
// file carries no identity record for gets its initial (generation, incarnation
// id, presence epoch) triple recorded, "in that same atomic hub.toml write".
// The registry minted the pair at load (hostreg.New); this write is what makes
// it durable, so the pair survives the next reload unchanged instead of being
// minted again. A file that already records every live host is left
// byte-identical — no boot rewrites it.
//
// The achievement is not a re-registration: the write only fills records the
// file did not carry, it mints no generation above any mark, and the name stays
// live throughout — nothing a re-add does (a generation strictly above the
// retained high-water mark, the purge of the removed name's history) happens
// here. A write that cannot land is logged and retried by the next boot (or by
// the next mutation, which records the same in-memory pair); the hub still
// serves, because the pair is already the registry's current identity.
func (m *hubHostManager) materializeHostRecords(cfg Config, hasFile bool) {
	if !hasFile || m.cfg.store.poisoned() != nil {
		return
	}
	entries := m.cfg.store.snapshot()
	for _, entry := range entries {
		record := cfg.HostRecords[entry.Name]
		mark := cfg.Generations[entry.Name]
		// Both records must be present and complete: a file carrying only one
		// of them would leave the other's value unrecorded — the incarnation
		// pair or the generation the next boot restores — so the boot write
		// repairs the pair in the canonical shape instead of skipping it.
		if !record.complete() || !mark.complete() {
			if err := m.persistHosts(entries, entries, hostPersistChange{}); err != nil {
				m.logf("host %q records for %s not recorded yet: %v", entry.Name, m.cfg.configPath, err)
			}
			return
		}
	}
}

// hostFileRecords loads the machine records the selected hub.toml carries.
// ok is false when there is no document to read — the file is missing or empty,
// so the boot carries no records and nothing is materialized from it — and when
// the file cannot be read or decoded, which the boot load reports; these helpers
// leave the file alone rather than acting on a half-read one. It deliberately
// does not go through LoadConfig: that loader folds a missing file into
// DefaultConfig, which cannot express "no document", and the empty/missing case
// is exactly the one where a boot must not write.
func (m *hubHostManager) hostFileRecords() (Config, bool) {
	if strings.TrimSpace(m.cfg.configPath) == "" {
		return Config{}, false
	}
	raw, err := configReadFile(m.cfg.configPath)
	if err != nil || len(raw) == 0 {
		return Config{}, false
	}
	cfg, err := decodeConfig(m.cfg.configPath, string(raw))
	if err != nil {
		return Config{}, false
	}
	return cfg, true
}

// hostFileAbsentOrEmpty reports whether the selected config path genuinely holds
// no document: the file does not exist, or it exists with zero bytes. It
// deliberately differs from hostFileRecords' ok — which is also false for a file
// that cannot be read or that fails to decode — because the boot compensation
// pass may only converge over real absence: clearing an armed record on a read
// or decode failure would discard the compensation record and its stash, the
// only durable copy of the pre-mutation hub.toml. An unset path is the caller's
// to screen out (reconcilePipelineBoot returns before this for an empty path);
// it is not "absence" here, so this helper reports false for it.
func (m *hubHostManager) hostFileAbsentOrEmpty() bool {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return false
	}
	raw, err := configReadFile(path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return len(raw) == 0
}

// migrateLegacyHostSidecar folds a retired hub.hosts.json into hub.toml exactly
// once, at boot. The order is the crash-safety story, and it is fixed:
//
//  1. hub.toml is rewritten (atomically, directory synced) with the merged set
//     and the machine-managed banner — the durable merged state exists before
//     the retired file can go away.
//  2. the sidecar is renamed aside — never deleted — and the directory synced,
//     so the retirement is durable before anything can act on the merged state.
//
// A name hub.toml already declares wins when the entries agree: the migration
// drops a byte-identical duplicate in the file's favor, exactly as the retired
// merge dropped colliding sidecar entries — while a differing duplicate is
// refused, because that retired merge's silent drop would lose the sidecar's
// settings.
// The same atomic write records `legacy_sidecar_migrated` in hub.toml, and the
// marker — not the rename's durability alone — decides what a later boot does:
// with the marker present the sidecar's entries are never read again (it is
// stale; at most its set-aside rename is retried), so a name removed through
// the UI after a completed migration can never be resurrected by a rename a
// power loss undid, even on a filesystem that ignores directory syncs; without
// the marker (a crash before it landed) the migration re-runs and converges.
// The caller poisons writes on any other failure, including a failed set-aside
// rename: no mutation can land while an unmigrated sidecar remains. A sidecar that fails to parse or validate is not migrated at
// all — both files stay untouched and the caller poisons writes. A crash at any
// point re-runs the whole migration on the next boot and converges, because
// already-merged names collide with the live set and are skipped, and the
// set-aside rename either already happened or is retried.
func (m *hubHostManager) migrateLegacyHostSidecar() (wrote bool, err error) {
	path := legacySidecarPathFor(m.cfg.configPath)
	if path == "" {
		return wrote, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No sidecar: hub.toml is not rewritten at boot, so a hub that
			// never added a host keeps its file byte-identical.
			return wrote, nil
		}
		return wrote, fmt.Errorf("read legacy host sidecar: %w", err)
	}
	// A sidecar that reappears after a recorded migration is stale — the
	// marker and every mutation since are in the same file, so a survivor of
	// one survived both. Ignore it rather than re-merging names the UI may
	// have removed (the case a directory sync the filesystem ignores cannot
	// rule out).
	if doc, _, docErr := readHubTOMLDocument(m.cfg.configPath); docErr == nil && doc[legacySidecarMigratedKey] == true {
		// Retire it if the aside name is free; a failure here changes nothing,
		// because the marker — not the rename — is the authority.
		aside := path + legacyHostSidecarAsideSuffix
		if _, statErr := os.Stat(aside); statErr != nil && os.IsNotExist(statErr) {
			if renameErr := os.Rename(path, aside); renameErr != nil {
				m.logf("stale legacy host sidecar %s could not be set aside: %v", path, renameErr)
			}
		}
		m.logf("stale legacy host sidecar %s ignored: %s already records the migration", path, m.cfg.configPath)
		return wrote, nil
	}
	entries, err := parseLegacyHostSidecar(data)
	if err != nil {
		return wrote, err
	}
	// The store's set as the running hub held it: the rewrite's "known" set,
	// so only genuine hand-added file entries are carried through.
	pre := m.cfg.store.snapshot()
	// The merge is resolved over the whole sidecar before anything live
	// changes: every entry normalizes and validates, duplicates inside the
	// sidecar and collisions with the live set resolve, and only then does the
	// apply loop run. A sidecar that fails any of that leaves the live set
	// exactly as it was — no half-merged hosts sitting behind a poisoned store
	// — which is what makes "not migrated at all" true rather than aspirational.
	var toAdd []hostreg.Host
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		// Normalize before any use of the entry. The registry's Add would
		// normalize before storing, but the collision check and the source
		// registration read the entry as decoded: a padded sidecar name would
		// register a source under the padded spelling while the registry stores
		// the trimmed one.
		e = hostreg.Normalize(e)
		if err := validateHostEntry(e); err != nil {
			return wrote, fmt.Errorf("legacy host sidecar entry %q: %w", e.Name, err)
		}
		if _, dup := seen[e.Name]; dup {
			return wrote, fmt.Errorf("legacy host sidecar entry %q: the sidecar names it twice", e.Name)
		}
		seen[e.Name] = struct{}{}
		if live, ok := m.cfg.hosts.Get(e.Name); ok {
			// hub.toml wins a collision only when the two entries say the same
			// thing: a byte-identical duplicate is dropped (nothing is lost),
			// while a differing one is refused loudly — silently dropping the
			// sidecar's settings (a key path, say) would make the "lossless"
			// migration a lie.
			if !live.Equal(e) {
				return wrote, fmt.Errorf("legacy host sidecar entry %q differs from the %s entry of the same name; resolve the duplicate first", e.Name, m.cfg.configPath)
			}
			continue
		}
		toAdd = append(toAdd, e)
	}
	// The apply loop cannot refuse: every name is validated, unique, and free.
	// Add stays a checked call rather than a silent trust.
	for _, e := range toAdd {
		if err := m.cfg.hosts.Add(e); err != nil {
			return wrote, fmt.Errorf("legacy host sidecar entry %q: %w", e.Name, err)
		}
		// The store row is the entry the registry stored, not the local one: the
		// registry minted the migration's added hosts an identity, and the write
		// below records it.
		stored, ok := m.cfg.hosts.Get(e.Name)
		if !ok {
			return wrote, fmt.Errorf("legacy host sidecar entry %q: the registry did not store it", e.Name)
		}
		m.cfg.store.add(stored)
		m.registerSource(e)
	}
	if err := m.persistHostsMarked(m.cfg.store.snapshot(), pre, hostPersistChange{}, true); err != nil {
		return false, err
	}
	wrote = true
	aside := path + legacyHostSidecarAsideSuffix
	if _, err := os.Stat(aside); err == nil {
		return wrote, fmt.Errorf("cannot set legacy host sidecar %s aside: %s already exists", path, aside)
	}
	if err := os.Rename(path, aside); err != nil {
		return wrote, fmt.Errorf("set legacy host sidecar aside: %w", err)
	}
	// The rename is only durable once the directory entry carrying it is
	// synced: without this, a power loss could bring the retired sidecar back
	// and the next boot would re-merge names the UI had already removed. The
	// sync seam keeps its usual tolerance for filesystems that cannot sync a
	// directory at all.
	if err := hubTOMLSyncDir(filepath.Dir(path)); err != nil {
		return wrote, fmt.Errorf("set legacy host sidecar aside: %w", err)
	}
	return wrote, nil
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

// hostOnline reports whether host currently has a live channel. It reads the
// attached-only signal without spawning SSH: the dial seam belongs to
// evener/host/attach alone. It is the gate on the no-manager fallback only: a
// manager-backed row never consults it, because attachedClient confirms the
// live channel itself, so the source registry's fail-open default (Online()
// true with no signal wired) cannot mark a host attached that nothing dialed.
func (m *hubHostManager) hostOnline(host string) bool {
	if m.cfg.online != nil {
		return m.cfg.online(host)
	}
	// No signal wired (tests, embedders): fall back to the source registry's
	// own Online, which defaults true with no signal — the pre-06 default.
	// An unknown name is offline: the registry entry exists (the caller
	// resolved it), so a missing source means no channel was ever wired for
	// it. Every source this manager creates carries an explicit signal, so
	// this path cannot recurse.
	if m.cfg.sources == nil {
		return false
	}
	source, ok := m.cfg.sources.Source(host)
	if !ok {
		return false
	}
	online, ok := source.(appsource.OnlineSource)
	if !ok {
		return true
	}
	return online.Online()
}

// hostEntryRow builds the row fields every host row carries from the effective
// entry: the configured values and a clone of roots (never an alias — the row
// crosses the wire and a caller mutating its Roots must not be able to reach
// back into the registry entry). List, status, add, update, and remove rows all
// start from this, so the removal response carries exactly the entry it removed
// instead of a hand-picked subset that can silently drift from the wire shape.
// The origin marker is set here because it is one value: every host lives in
// the machine-managed hub.toml (registry spec 08 §6/§19, decided 2026-09-26;
// the spec-correction PR carries the §6/§19 rewrite).
func hostEntryRow(host hostreg.Host) appwire.HostRow {
	row := appwire.HostRow{
		Name:          host.Name,
		Address:       host.SSH,
		User:          host.User,
		KeyPath:       host.KeyPath,
		EvenerPath:    host.EvenerPath,
		ConfigPath:    host.ConfigPath,
		Addr:          host.Addr,
		Roots:         slices.Clone(host.Roots),
		Origin:        hostOriginHubTOML,
		Generation:    host.Generation,
		IncarnationID: host.IncarnationID,
	}
	return row
}

// hostRow renders one host's list row: the effective entry fields plus live
// state. Attached rows read the live channel's handshake and preflight facts
// through the attached-only lookups and record them as last-known — but only
// the channel paired with this row's entry, never one built from another
// generation of the name (attachedClient); a failed
// lookup keeps the previously retained fields instead of blanking them, so a
// transient probe failure cannot cost a later offline row its facts. Offline
// and in-progress rows render the retained attach state (midAttach,
// lastAttachError) and last-known facts from the record. It never dials:
// every seam here is attached-only. ctx is the caller's handler context — the
// facts read runs on it, so a caller that goes away cancels the read; List,
// Status, and Add build their rows without holding the mutation mutex, so a
// parked facts read holds up no commit either. The one
// mutex the row build itself takes is the brief mutation-mutex hold around
// the fenced retained-state fold at the end — after every network read has
// returned — so the parked read the fence exists for still holds up no
// commit.
func (m *hubHostManager) hostRow(ctx context.Context, host hostreg.Host) appwire.HostRow {
	row := hostEntryRow(host)
	// Attached is reported only when attachedClient confirms a live channel
	// built from the very entry this row renders — a channel under the same
	// name but another registration leaves the row offline rather than
	// borrowing its attached state. With a manager wired that confirmation is
	// the liveness signal itself: attachedClient resolves the channel once,
	// and reading the name again through hostOnline would resolve the same
	// channel a second time. The signal alone would not be sufficient anyway:
	// with no signal wired a hub.toml source fails open (Online() true), and
	// trusting that would render every configured host Attached with no facts
	// behind it — an online row the UI then refuses to Connect because it
	// looks already up. With no manager (tests, embedders) there is no
	// channel identity to confirm, so hostOnline stays the gate on the
	// name-only fallback seam, and no lookup wired leaves the row honestly
	// offline too: nothing can confirm a channel, so nothing may claim one.
	//
	// validity tracks which live lookups refreshed the row's fact fields, so
	// the retention below keeps the previously known values for the fields
	// whose lookup failed instead of blanking them with the failed read's
	// empties.
	var validity hostFactsValidity
	var (
		client   *appwire.Client
		attached bool
	)
	if m.cfg.manager != nil {
		client, attached = m.attachedClient(host)
	} else if m.hostOnline(host.Name) {
		client, attached = m.attachedClient(host)
	}
	if attached && client != nil {
		row.Attached = true
		if m.cfg.handshake != nil {
			if hs, ok := m.cfg.handshake(host.Name, client); ok {
				row.ServerName = hs.ServerInfo.Name
				row.ServerVersion = hs.ServerInfo.Version
				validity.handshake = true
			}
		}
		if m.cfg.facts != nil {
			if facts, err := m.cfg.facts(ctx, host.Name, client); err == nil {
				row.HubVersion = facts.HubVersion
				row.OS = facts.OS
				row.Arch = facts.Arch
				validity.facts = true
			}
			// A facts-read failure keeps the row attached: the dial
			// the attach already completed is authoritative
			// (app_host_attach.go's dial-authoritative rule), and a
			// failed facts read is not a detach. The row's empty fields
			// are the honest render of the failed read; what the row
			// retains for its later offline rows is decided by validity.
		}
	}
	// The retained-state fold and record are fenced on the entry generation
	// the row was built from: the row build runs outside the mutation mutex
	// so a parked facts read holds up no commit, and an unfenced recordKnown
	// would let a delayed row finish after a remove/re-add and recreate the
	// removed host's name-keyed attach record with facts the re-added host's
	// offline rows then rendered as their own. The fence and the state writes
	// share the mutation mutex, so
	// they serialize with both record drops (Remove's finish phase and Add's
	// clean re-add): a row that wins the race records before the removal's
	// drop deletes its record, and a row that loses sees the moved or missing
	// generation and records nothing. Either order leaves the re-added name's
	// record exactly as clean as its own commit left it.
	//
	// The fence also excludes a name whose retained state is in flight. A
	// mutation holds the name's mark from its commit phase to its finish
	// phase, and that span covers the window where an update's new entry is
	// already visible in the registry but its retirement has not run — the
	// swap and the retire hook are adjacent only inside the per-host gate,
	// with the mutation mutex released between them. A concurrent, gate-free
	// row that snapshots the new, higher-generation entry in that window still
	// passes hostEntryCurrent, and gen > retiredThrough still holds because
	// the retire has not run, so without this condition the row would fold the
	// retired identity's midAttach, lastAttachError, and last-known facts.
	// Suppressing both the fold and the record for the whole mark means no row
	// can fold a retired identity's state, and the rows render honestly blank
	// until the mutation's finish phase, after which the next row build folds
	// whatever the mutation left behind. Two consequences are deliberate:
	// during a removal window the host's own row also renders without its
	// retained state — the destination state of a removal anyway — and an
	// attached host's facts lookup that succeeds during the window is simply
	// re-recorded on the next row build.
	m.cfg.mu.Lock()
	if m.hostEntryCurrent(host) && !m.isMutating(host.Name) {
		m.cfg.state.apply(&row, host.Generation)
		if row.Attached {
			m.cfg.state.recordKnown(row, validity, host.Generation)
		}
	}
	m.cfg.mu.Unlock()
	return row
}

// hostEntryCurrent reports whether the live registry still holds host's name
// under the same registration host carries — the registration the row was
// built from, unchanged by any remove or remove/re-add: a re-add is a new
// insert (hostreg's registry-wide generation counter never reuses one), and
// a removed name has no live entry at all. It is hostreg's own
// same-registration predicate, so the row fence and the SSH manager's attach
// identity rechecks cannot drift apart. Callers hold the mutation mutex.
func (m *hubHostManager) hostEntryCurrent(host hostreg.Host) bool {
	return m.cfg.hosts.SameRegistration(host.Name, host)
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
		handshake:        m.cfg.handshake,
		facts:            m.cfg.facts,
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

// Add is the pre-union adapter over AddResult: it returns the committed row and
// reports a non-committed arm as the error that arm means, so a caller that
// speaks only the shipped shape never reads a teardown failure or a dropped
// commit as success. The wire handlers call the Result methods directly.
func (m *hubHostManager) Add(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
	return committedHostRow(m.AddResult(ctx, params))
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

// persistHosts rewrites hub.toml durably, refusing while the store is known
// incomplete (a legacy sidecar that failed to load or migrate): rewriting the
// file then would clobber the entries that never made it into memory. Callers
// treat the refusal as fatal (add/remove report it; the entry stays un-exposed
// or the host stays intact), so the operator hears about the broken file
// instead of losing it. A failure the rename already committed
// (hubTOMLPostRenameError) is not a plain refusal — the file holds the new
// entries — so the callers compensate the live state before reporting it.
func (m *hubHostManager) persistHosts(entries, known []hostreg.Host, change hostPersistChange) error {
	return m.persistHostsMarked(entries, known, change, false)
}

// persistHostsMarked is persistHosts with the migration's marker; only the
// migration passes true. known is the store's pre-mutation snapshot.
//
// The write is derive-then-install: the machine-record set is derived once
// (deriveHostTOMLRecords applies every retention, capacity, and compaction
// rule), that exact set is written, and only a successful write installs it
// into the store — so a failed write leaves both the durable file and the
// in-memory records untouched, and a compensation needs no staged-state
// bookkeeping. A capacity refusal (spec §15's `tombstone-capacity`) is mapped
// onto its wire envelope here, the one choke point every mutation path runs
// through.
func (m *hubHostManager) persistHostsMarked(entries, known []hostreg.Host, change hostPersistChange, migrated bool) error {
	if err := m.cfg.store.poisoned(); err != nil {
		return fmt.Errorf("hub.toml %s not rewritten: %w (fix the legacy host sidecar or remove its unloaded entries first)", m.cfg.configPath, err)
	}
	records, err := m.deriveHostTOMLRecords(entries, known, change)
	if err != nil {
		return tombstoneCapacityRefusal(err)
	}
	if err := writeHubTOMLHostsRecords(m.cfg.configPath, entries, known, records, migrated); err != nil {
		return err
	}
	m.cfg.store.installRecords(records)
	// The mirror rides behind the hub.toml commit: hub.toml is the authority
	// and the two files cannot be one write, so the mirror is written last and
	// a failure here leaves it behind the file rather than unwinding the
	// commit. See mirrorBoundaries.
	m.mirrorBoundaries(entries, known, records)
	return nil
}

// mirrorBoundaries records the triples the hub.toml write just committed in the
// operation store's per-host boundary records (registry spec 08 §7: "one record
// per host name — {generation, incarnationId, presenceEpoch} — written in the
// same atomic store writes that mirror the generation"). Every live entry's
// triple and every retained high-water record for a name with no live entry is
// carried, names whose entry holds no complete identity (a direct writer
// fixture) are skipped, and the whole set lands in one atomic store write
// together with the removal markers §4's removed-host ordering reads: every
// name carrying a removal tombstone is dated, every live name clears any
// marker it held.
//
// A mirror failure is logged, not returned: the hub.toml commit already landed
// and cannot be unwound, and the state it leaves — the file ahead of the
// mirror — is exactly what the deploy-pipeline spec §4 boot pass pushes
// forward. That pass, and the cursor validation that reads the mirror, belong
// to later slices; until they land the mirror trails the file and nothing
// reads it.
func (m *hubHostManager) mirrorBoundaries(entries, known []hostreg.Host, records hostTOMLRecords) {
	if m.cfg.ops == nil || strings.TrimSpace(m.cfg.configPath) == "" {
		return
	}
	// The mirrored triples are the same derivation the file's records come from
	// (hostFileRecordSet), so the mirror and the file cannot be projected by two
	// rules that drift apart.
	_, generations := hostFileRecordSet(entries, records.highWater)
	boundaries := make(map[string]hostops.Boundary, len(generations))
	for name, mark := range generations {
		boundaries[name] = hostops.Boundary{
			Generation:    mark.Generation,
			IncarnationID: mark.IncarnationID,
			PresenceEpoch: mark.PresenceEpoch,
		}
	}
	// The write owns the records of the names it carries and its pre-mutation
	// snapshot names — the same ownership rule the record tables use. A boundary
	// under an owned name this write carries no record for (a compensated add,
	// a name the live registry dropped while the write ran) is pruned in the
	// same atomic write, so the mirror never keeps a boundary for a host
	// hub.toml no longer records.
	owned := make(map[string]struct{}, len(known)+len(entries))
	for _, e := range known {
		owned[e.Name] = struct{}{}
	}
	for _, e := range entries {
		owned[e.Name] = struct{}{}
	}
	var remove []string
	for name := range owned {
		if _, carried := boundaries[name]; !carried {
			remove = append(remove, name)
		}
	}
	slices.Sort(remove)
	// The removal markers ride the same write: a tombstoned name is dated from
	// its own removed_at, a live name clears any marker, and a name the write
	// cannot classify (a pruned-tombstone high-water record) keeps whatever
	// marker it had — a removal the store cannot date is never invented, and
	// one it already knew is never forgotten (deploy pipeline 08b §4's
	// removed-host horizon).
	mirror := hostops.HostMirror{Boundaries: boundaries, Remove: remove}
	for _, entry := range entries {
		mirror.Live = append(mirror.Live, entry.Name)
	}
	for name, tombstone := range records.tombstones {
		removedAt, err := time.Parse(time.RFC3339, tombstone.RemovedAt)
		if err != nil {
			continue
		}
		if mirror.Removed == nil {
			mirror.Removed = map[string]hostops.RemovedHost{}
		}
		mirror.Removed[name] = hostops.RemovedHost{
			RemovedAt:     removedAt,
			Generation:    tombstone.Generation,
			IncarnationID: tombstone.IncarnationID,
		}
	}
	if len(mirror.Boundaries) == 0 && len(mirror.Remove) == 0 && len(mirror.Live) == 0 && len(mirror.Removed) == 0 {
		return
	}
	if err := m.cfg.ops.MirrorHostState(mirror); err != nil {
		m.logf("host boundary records not mirrored: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The cross-file commit intent and the generation-mirror markers
// (deploy-pipeline spec 08b §9, §4)
// ---------------------------------------------------------------------------

// HostStoreSyncIntent is hub.toml's `pending_store_sync` intent (§9): the
// exact store rows a commit's swap is deleting or invalidating, plus the
// hub.toml generation the intent belongs to. The commit's staged write carries
// it, the store's purge applies it after the swap succeeds, and a follow-up
// atomic write clears it.
type HostStoreSyncIntent struct {
	// Generation is the hub.toml generation the intent belongs to (the removed
	// entry's generation).
	Generation uint64 `toml:"generation"`
	// TokenValues names the exact store rows to delete, by token value. A
	// removal carries the host's outstanding row, when it has one.
	TokenValues []string `toml:"token_values"`
}

// HostMirrorCommit is hub.toml's generation-mirror commit marker (§4): the
// (hub.toml generation, store-mirror generation) pair every commit that
// advances a mirrored store-side generation writes into hub.toml's atomic
// write. Boot's bidirectional reconciliation reads it as the authorization
// evidence for a store mirror: a mirror newer than the file's mark with no
// matching marker was not authorized by a durable hub.toml write.
type HostMirrorCommit struct {
	HubTOMLGeneration uint64 `toml:"hub_toml_generation"`
	StoreGeneration   uint64 `toml:"store_generation"`
}

// validateMachineRecordName checks a name keying one of the machine-managed
// record sections: a bounded, valid-UTF-8, non-empty host name. No writer of
// these sections emits anything else.
func validateMachineRecordName(name string) error {
	if name == "" {
		return errors.New("a machine record is keyed by an empty host name")
	}
	if len(name) > hostops.MaxHostNameBytes {
		return fmt.Errorf("a machine record is keyed by a %d-byte host name, over the %d-byte bound", len(name), hostops.MaxHostNameBytes)
	}
	if !utf8.ValidString(name) {
		return errors.New("a machine record is keyed by a host name that is not valid UTF-8")
	}
	return nil
}

// validateHostStoreSync checks the cross-file intents a hub.toml document
// carries: a named key, a pinned generation, and at least one bounded,
// unique, non-empty row value.
func validateHostStoreSync(intents map[string]HostStoreSyncIntent) error {
	for name, intent := range intents {
		if err := validateMachineRecordName(name); err != nil {
			return err
		}
		if intent.Generation == 0 {
			return fmt.Errorf("pending_store_sync[%q] pins no hub.toml generation", name)
		}
		if len(intent.TokenValues) == 0 {
			return fmt.Errorf("pending_store_sync[%q] names no store rows", name)
		}
		seen := make(map[string]struct{}, len(intent.TokenValues))
		for _, value := range intent.TokenValues {
			if value == "" {
				return fmt.Errorf("pending_store_sync[%q] names an empty row value", name)
			}
			if len(value) > hostops.MaxStoreSyncValueBytes {
				return fmt.Errorf("pending_store_sync[%q] names a %d-byte row value, over the %d-byte bound",
					name, len(value), hostops.MaxStoreSyncValueBytes)
			}
			if !utf8.ValidString(value) {
				return fmt.Errorf("pending_store_sync[%q] names a row value that is not valid UTF-8", name)
			}
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("pending_store_sync[%q] names row %q twice", name, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

// validateHostMirrorCommits checks the generation-mirror markers a hub.toml
// document carries: a named key and two non-zero generations.
func validateHostMirrorCommits(commits map[string]HostMirrorCommit) error {
	for name, commit := range commits {
		if err := validateMachineRecordName(name); err != nil {
			return err
		}
		if commit.HubTOMLGeneration == 0 || commit.StoreGeneration == 0 {
			return fmt.Errorf("mirror_commits[%q] carries a zero generation", name)
		}
	}
	return nil
}

// hubTOMLStoreSyncTables derives the pending-store-sync tables a rewrite
// writes: the intent set this write carries, plus every file intent for a name
// this write does not own, preserved verbatim — the same ownership rule the
// other record tables apply, so an intent a mutation does not own is not
// silently dropped by its rewrite. A nil known is the exact-write sentinel.
func hubTOMLStoreSyncTables(cfg Config, entries, known []hostreg.Host, intents map[string]HostStoreSyncIntent, dropped map[string]struct{}) map[string]HostStoreSyncIntent {
	out := make(map[string]HostStoreSyncIntent, len(intents)+len(cfg.PendingStoreSync))
	maps.Copy(out, intents)
	owned := ownedRecordNames(entries, known)
	if owned == nil {
		return out
	}
	for name, intent := range cfg.PendingStoreSync {
		if _, carried := out[name]; carried {
			continue
		}
		if _, ok := owned[name]; ok {
			continue
		}
		if _, pruned := dropped[name]; pruned {
			// The derivation dropped this intent (a converged clear, a
			// compensation): the file's older copy must not ride back in.
			continue
		}
		out[name] = intent
	}
	return out
}

// hubTOMLMirrorCommitTables derives the generation-mirror marker tables a
// rewrite writes. Every name this write owns and carries a generation for gets
// the (G, G) marker for that generation — the pair the mirror write that
// follows this one is about to land — while every file marker for a name the
// write does not own is preserved verbatim. A name with no generation record
// (a purged name) gets none: the marker goes with the records it authorizes.
func hubTOMLMirrorCommitTables(cfg Config, entries, known []hostreg.Host, generations map[string]HostGeneration) map[string]HostMirrorCommit {
	out := make(map[string]HostMirrorCommit, len(generations)+len(cfg.MirrorCommits))
	owned := ownedRecordNames(entries, known)
	for name, mark := range generations {
		if owned != nil {
			if _, ok := owned[name]; !ok {
				continue
			}
		}
		if !mark.complete() {
			continue
		}
		out[name] = HostMirrorCommit{HubTOMLGeneration: mark.Generation, StoreGeneration: mark.Generation}
	}
	if owned == nil {
		return out
	}
	for name, commit := range cfg.MirrorCommits {
		if _, carried := out[name]; carried {
			continue
		}
		if _, ok := owned[name]; ok {
			continue
		}
		out[name] = commit
	}
	return out
}

// hubTOMLStashPath names one commit's stash: a per-commit file beside hub.toml
// (registry spec 08 §6: "stashing a durable copy of the prior `hub.toml`
// bytes"), keyed by the commit's scoped receipt key. A shared name would let a
// later removal of another host overwrite the restore source an open
// compensation still names, so the next boot would restore the wrong bytes as
// that record's pre-mutation state. The compensation record carries the exact
// path; only a record naming it keeps it from the boot prune.
//
// Compatibility: the pre-rename shared name (`<hub.toml>.stash`) never shipped
// — this branch introduced it — so there is no persisted record to migrate. A
// record that names it anyway (a file written by an earlier build of this
// branch) keeps it and restores it unchanged; the boot prune removes it only
// when no open record names it.
func hubTOMLStashPath(configPath, receiptKey string) string {
	sum := sha256.Sum256([]byte(receiptKey))
	return fmt.Sprintf("%s.stash.%x", configPath, sum[:8])
}

// stashKeyHexChars is the fixed width of the per-commit suffix
// hubTOMLStashPath emits (the first 8 bytes of a SHA-256 digest).
const stashKeyHexChars = 16

// isStashKeyHex reports whether suffix is a per-commit stash name's hex half.
func isStashKeyHex(suffix string) bool {
	if len(suffix) != stashKeyHexChars {
		return false
	}
	for _, digit := range suffix {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

// ownsStashPath reports whether stash names this hub's own stash family beside
// its hub.toml: the canonical legacy name or a per-commit
// `<hub.toml>.stash.<16 hex>` name. Anything else is not this hub's to read,
// restore, or remove — a tampered or foreign record names a path, never bytes
// this hub wrote.
func (m *hubHostManager) ownsStashPath(stash string) bool {
	return stashPathOwnedBy(m.cfg.configPath, stash)
}

// stashPathOwnedBy is ownsStashPath's path-based twin, usable before a manager
// exists (the load wiring validates the records it opened).
func stashPathOwnedBy(configPath, stash string) bool {
	path := strings.TrimSpace(configPath)
	if path == "" || strings.TrimSpace(stash) == "" {
		return false
	}
	if stash == path+".stash" {
		// The legacy spelling an earlier build of this branch wrote; a record
		// naming it keeps its restore source.
		return true
	}
	suffix, ok := strings.CutPrefix(stash, path+".stash.")
	return ok && isStashKeyHex(suffix)
}

// validateHostOpsStashReferences refuses a loaded compensation record whose
// stash reference is not this hub.toml family's own: the record's path becomes
// a restore source and an unconditional remove target, so a foreign path must
// refuse startup (the machine-managed-file posture: a record this build cannot
// account for is refused loudly, never served and never removed).
func validateHostOpsStashReferences(store *hostops.Store, configPath string) error {
	if store == nil {
		return nil
	}
	for host, record := range store.Compensations() {
		if stashPathOwnedBy(configPath, record.Stash) {
			continue
		}
		return fmt.Errorf("operation store compensation for %q names stash %q, which is not a stash path beside %s; refusing to serve rather than read or remove a foreign path",
			host, record.Stash, strings.TrimSpace(configPath))
	}
	return nil
}

// writeHubTOMLStash captures the selected hub.toml's current bytes into the
// stash before a removal's staged write replaces them, so the commit is
// compensable. It returns the stash reference the compensation record must
// carry. A hub with no config file has no cross-file commit to compensate and
// stashes nothing.
func (m *hubHostManager) writeHubTOMLStash(receiptKey string) (string, error) {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return "", nil
	}
	prior, err := configReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// No file yet: the hub is authoritative in memory and there is no
		// committed hub.toml bytes a restore would apply.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stash hub.toml %s: %w", path, err)
	}
	stash := hubTOMLStashPath(path, receiptKey)
	if err := hubTOMLReplaceWithBytes(stash, prior, hubTOMLStashSyncDir); err != nil {
		return "", fmt.Errorf("stash hub.toml %s: %w", path, err)
	}
	return stash, nil
}

// hubTOMLReplaceWithBytes atomically replaces path with data: a 0600 temp file
// in the same directory, fsynced, renamed over the target, then the directory
// synced — the same posture every hub.toml write keeps. sync is the caller's
// directory-sync seam: the stash keeps its own (hubTOMLStashSyncDir), so a
// test injecting a hub.toml sync failure drives the writes it names and not the
// stash beside them.
func hubTOMLReplaceWithBytes(path string, data []byte, sync func(string) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".hub.toml-*.tmp")
	if err != nil {
		return fmt.Errorf("temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s over %s: %w", tmpName, path, err)
	}
	return sync(dir)
}

// restoreHubTOMLFromStash applies a compensation's stash: the prior hub.toml
// bytes go back atomically, so a crash cannot land a half-restored file. The
// stash is re-parsed through the loader before anything lands, exactly as
// every rewrite does.
func (m *hubHostManager) restoreHubTOMLFromStash(stash string) error {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return nil
	}
	if strings.TrimSpace(stash) == "" {
		return errors.New("compensation record names no stash")
	}
	if !m.ownsStashPath(stash) {
		return fmt.Errorf("compensation record names stash %q, which is not a stash path beside %s", stash, path)
	}
	data, err := os.ReadFile(stash)
	if err != nil {
		return fmt.Errorf("read hub.toml stash %s: %w", stash, err)
	}
	if _, err := decodeConfig(path, string(data)); err != nil {
		return fmt.Errorf("hub.toml stash %s is not a readable document: %w", stash, err)
	}
	return hubTOMLReplaceWithBytes(path, data, hubTOMLSyncDir)
}

// pruneHubTOMLStash removes a stash once its commit reached an outcome: "The
// stash is deleted once the commit reaches either outcome" (registry spec
// 08 §6). A missing stash is already pruned.
func (m *hubHostManager) pruneHubTOMLStash(stash string) {
	if strings.TrimSpace(stash) == "" {
		return
	}
	if !m.ownsStashPath(stash) {
		// A record's path is not this hub's to remove: a tampered or foreign
		// record must never turn into an unconditional delete.
		m.logf("stash %q is not a stash path this hub owns; left untouched", stash)
		return
	}
	if err := os.Remove(stash); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.logf("hub.toml stash %s not pruned: %v", stash, err)
	}
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

// reconcilePipelineBoot runs spec 08b §7's boot passes in their fixed order,
// after hub.toml's records are loaded and before the manager serves:
// interrupted transition (idempotent — the store-open pass already moved
// in-flight records), tombstone-derived host-removed pass, bidirectional
// generation-mirror reconciliation, then the cross-file intent reconciliation
// (§9), which runs the compensation arms before the generic intent rules. Each
// pass writes nothing when it finds nothing to converge, so an untouched file
// stays byte-identical.
func (m *hubHostManager) reconcilePipelineBoot() {
	if m.cfg.ops == nil || strings.TrimSpace(m.cfg.configPath) == "" {
		return
	}
	fileCfg, hasFile := m.hostFileRecords()
	if m.cfg.store.poisoned() != nil {
		return
	}
	if !hasFile {
		// hostFileRecords reports false for a genuinely absent or empty document
		// AND for a file it could not read or decode. Only the first is a state
		// the compensation arms may converge over: an armed record clearing
		// through its explicit absent-file arm would delete the record and its
		// stash — the only durable copy of the pre-mutation hub.toml — on a mere
		// read or decode failure. An unreadable or undecodable document leaves
		// everything as it was for the operator to repair.
		if !m.hostFileAbsentOrEmpty() {
			return
		}
		// The document is genuinely absent or empty. The compensation arms that
		// need no bytes still converge here — an armed record (or one in the
		// hub.toml phase) clears through its own explicit `!hasFile` arm, and the
		// orphan-stash prune then removes the cleared record's stash. Returning
		// without this pass left the record and its stash until a file
		// reappeared, with `resumeCompensation`'s explicit absent-file clear arm
		// unreachable from the boot path.
		m.reconcileCompensations()
		m.pruneOrphanHubTOMLStash()
		return
	}
	// §7's interrupted transition, in its own position (store load, hub.toml
	// load, interrupted, host-removed, mirror, intents). The store-open pass
	// already ran it as an interim; the pass is idempotent — with nothing left
	// pending/running it moves nothing and writes nothing.
	if moved, err := m.cfg.ops.RecoverInterrupted(); err != nil {
		m.logf("boot interrupted transition not completed: %v", err)
	} else if moved > 0 {
		m.logf("boot moved %d in-flight operation(s) to interrupted", moved)
	}
	m.reconcileHostRemovedPass(fileCfg)
	m.reconcileGenerationMirror(fileCfg)
	m.reconcileCompensations()
	m.reconcileStoreSyncIntents()
	m.reconcileTokenRows()
	m.pruneOrphanHubTOMLStash()
}

// pruneOrphanHubTOMLStash removes the canonical stash when no open
// compensation record names it: "a stash left by a crash is ignored (safe to
// prune) at boot — EXCEPT a stash named by a live `pendingCompensation`
// record's stash reference … that record is the compensation's sole durable
// authority" (registry spec 08 §6). Only the canonical path is this pass's to
// prune; a record naming any other path keeps that path, and this pass leaves
// it alone.
func (m *hubHostManager) pruneOrphanHubTOMLStash() {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + ".stash"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	named := make(map[string]struct{})
	for _, record := range m.cfg.ops.Compensations() {
		named[record.Stash] = struct{}{}
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		stash := filepath.Join(dir, entry.Name())
		if _, keep := named[stash]; keep {
			continue
		}
		m.pruneHubTOMLStash(stash)
	}
}

// installRestoredRecords converges the store's machine-record model to the
// restored hub.toml's bytes: the live entries, the high-water marks, the
// tombstones and pruned markers, the receipts, the teardown-repair records, and
// the cross-file intents. Without it the model keeps the compensated commit's
// records (the intent, the staged marker, the tombstone, the provisional
// receipt) and the next write re-emits them over the restored bytes — the
// cleared intent would come back and the boot after that would purge the very
// row the compensation restored, and a stale tombstone beside a live entry
// refuses the write outright.
func (m *hubHostManager) installRestoredRecords(cfg Config) {
	m.cfg.store.set(hostRegistryEntries(cfg))
	m.cfg.store.setHighWaterMap(nonNilMap(cfg.Generations))
	m.cfg.store.setReceipts(nonNilMap(cfg.MutationReceipts))
	m.cfg.store.setRecordMaps(nonNilMap(cfg.Tombstones), nonNilMap(cfg.PrunedReceipts))
	m.cfg.store.setRemnantMaps(nonNilMap(cfg.StagedReceipts), nonNilMap(cfg.TeardownRemnants), nonNilMap(cfg.TeardownAttempts))
	m.cfg.store.setStoreSync(nonNilMap(cfg.PendingStoreSync))
	m.cfg.hosts.SeedHighWater(hostHighWaterMarks(cfg))
}

// nonNilMap returns m, or an empty map of its type when m is nil: a machine
// record set this store installs is always writable.
func nonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// reconcileHostRemovedPass applies every loaded tombstone to the store (§4's
// tombstone-derived pass): the removed incarnation's records are marked
// `host-removed` and the name's token rows drop with it (§7). A crash between
// a remove's hub.toml commit and its live mark recovers exactly here, and a
// tombstone colliding with a live re-add's different incarnation id matches
// nothing.
func (m *hubHostManager) reconcileHostRemovedPass(fileCfg Config) {
	if len(fileCfg.Tombstones) == 0 {
		return
	}
	marks := make(map[string]hostops.HostRemovedMark, len(fileCfg.Tombstones))
	for name, tombstone := range fileCfg.Tombstones {
		marks[name] = hostops.HostRemovedMark{Generation: tombstone.Generation, IncarnationID: tombstone.IncarnationID}
	}
	marked, dropped, err := m.cfg.ops.ApplyHostRemovedPass(marks)
	if err != nil {
		m.logf("boot host-removed pass not applied: %v", err)
		return
	}
	if marked > 0 || dropped > 0 {
		m.logf("boot host-removed pass marked %d operation record(s) and dropped %d token row(s)", marked, dropped)
	}
}

// reconcileGenerationMirror runs §4/§7's bidirectional generation-mirror
// reconciliation: the store mirror is rolled back to the file's mark where it
// is ahead with no matching commit marker (the discarded generation's records
// transition to interrupted, its dedup tombstones drop, and the number is
// preserved as the name's high-water), pushed forward where the file's mark is
// ahead, and preserved where the file carries no entry.
func (m *hubHostManager) reconcileGenerationMirror(fileCfg Config) {
	view := hostops.MirrorView{
		Marks:   map[string]hostops.Boundary{},
		Live:    map[string]struct{}{},
		Removed: map[string]struct{}{},
		Commits: map[string]hostops.MirrorCommit{},
	}
	for name, mark := range fileCfg.Generations {
		if !mark.complete() {
			continue
		}
		view.Marks[name] = hostops.Boundary{Generation: mark.Generation, IncarnationID: mark.IncarnationID, PresenceEpoch: mark.PresenceEpoch}
	}
	for _, entry := range hostRegistryEntries(fileCfg) {
		view.Live[entry.Name] = struct{}{}
		if !completeIdentity(entry) {
			continue
		}
		if _, carried := view.Marks[entry.Name]; !carried {
			view.Marks[entry.Name] = hostops.Boundary{
				Generation:    entry.Generation,
				IncarnationID: entry.IncarnationID,
				PresenceEpoch: entry.PresenceEpoch,
			}
		}
	}
	for name, tombstone := range fileCfg.Tombstones {
		view.Removed[name] = struct{}{}
		if _, carried := view.Marks[name]; !carried {
			view.Marks[name] = hostops.Boundary{
				Generation:    tombstone.Generation,
				IncarnationID: tombstone.IncarnationID,
				PresenceEpoch: tombstone.PresenceEpoch,
			}
		}
	}
	for name, commit := range fileCfg.MirrorCommits {
		view.Commits[name] = hostops.MirrorCommit{
			HubTOMLGeneration: commit.HubTOMLGeneration,
			StoreGeneration:   commit.StoreGeneration,
		}
	}
	result, err := m.cfg.ops.ReconcileMirror(view)
	if err != nil {
		m.logf("boot generation-mirror reconciliation not applied: %v", err)
		return
	}
	if len(result.RolledBack) > 0 || result.RecordsMoved > 0 || result.TombstonesDropped > 0 {
		m.logf("boot generation-mirror rollback: %v rolled back, %d record(s) interrupted, %d dedup tombstone(s) dropped",
			result.RolledBack, result.RecordsMoved, result.TombstonesDropped)
	}
	if len(result.PushedForward) > 0 {
		m.logf("boot generation-mirror push-forward: %v", result.PushedForward)
	}
	if len(result.HighWater) == 0 {
		return
	}
	m.applyMirrorHighWater(result.HighWater)
}

// applyMirrorHighWater writes the mirror pass's high-water raises into
// hub.toml in one atomic write: each discarded generation (or surviving
// mirror) becomes the name's [generations] mark, and a tombstoned name's
// tombstone twin is raised with it. The live registry's counters are seeded up
// (never lowered), so this process cannot mint a generation the raise covers.
//
// The live registry's entry for a name keeps its own generation until the next
// boot reads the raised mark: hostreg has no boot-restore setter, and an
// Update would mint a fresh identity. The durable high-water is what prevents
// reuse, and it is the file's own record.
func (m *hubHostManager) applyMirrorHighWater(highWater map[string]hostops.Boundary) {
	entries := m.cfg.store.snapshot()
	raises := make(map[string]HostGeneration, len(highWater))
	marks := make(map[string]hostreg.HighWater, len(highWater))
	for name, boundary := range highWater {
		raised := HostGeneration{
			Generation:    boundary.Generation,
			IncarnationID: boundary.IncarnationID,
			PresenceEpoch: boundary.PresenceEpoch,
		}
		raises[name] = raised
		marks[name] = hostreg.HighWater{Generation: raised.Generation, PresenceEpoch: raised.PresenceEpoch}
		for i := range entries {
			if entries[i].Name != name {
				continue
			}
			if entries[i].IncarnationID != raised.IncarnationID {
				// The raise came from a store-mirror identity the live entry
				// does not carry (a preserved mirror under an unmaterialized
				// name): only the counters are seeded, never the entry's own
				// incarnation.
				continue
			}
			// The entry carries the whole raised triple: a live name's
			// incarnation is the file mark's own (the raise preserves it), and
			// writing the full triple keeps the live entry, its [generations]
			// mark and the registry's seeded counters one operation rather than
			// three that could drift.
			entries[i].Generation = raised.Generation
			entries[i].IncarnationID = raised.IncarnationID
			entries[i].PresenceEpoch = raised.PresenceEpoch
		}
	}
	if err := m.persistHosts(entries, entries, hostPersistChange{highWaterRaises: raises}); err != nil {
		m.logf("boot generation-mirror high-water for %s not written: %v", m.cfg.configPath, err)
		return
	}
	// SeedHighWater raises the live registry's entry for a name whose counter
	// sits below the mark (§1: a live entry is never left below its mark), so
	// the next removal or edit records the raised generation, never the
	// discarded one.
	m.cfg.hosts.SeedHighWater(marks)
	m.cfg.store.set(entries)
}

// reconcileTokenRows runs §9's closing reverse direction: store rows with no
// covering intent whose hub.toml generation already advanced past them — or
// whose host resolves to a tombstone — are dropped, before the store serves.
func (m *hubHostManager) reconcileTokenRows() {
	fileCfg, hasFile := m.hostFileRecords()
	if !hasFile {
		return
	}
	view := hostops.TokenRowReconcile{
		Live:    map[string]uint64{},
		Removed: map[string]hostops.HostRemovedMark{},
		Covered: map[string]map[string]struct{}{},
	}
	for _, entry := range hostRegistryEntries(fileCfg) {
		view.Live[entry.Name] = entry.Generation
	}
	for name, tombstone := range fileCfg.Tombstones {
		if _, live := view.Live[name]; live {
			// A live re-add supersedes the tombstone: its rows belong to the new
			// incarnation and are not the removed one's to delete.
			continue
		}
		view.Removed[name] = hostops.HostRemovedMark{
			Generation:    tombstone.Generation,
			IncarnationID: tombstone.IncarnationID,
		}
	}
	for name, intent := range fileCfg.PendingStoreSync {
		covered := view.Covered[name]
		if covered == nil {
			covered = map[string]struct{}{}
			view.Covered[name] = covered
		}
		for _, value := range intent.TokenValues {
			covered[value] = struct{}{}
		}
	}
	dropped, err := m.cfg.ops.ReconcileTokenRows(view)
	if err != nil {
		m.logf("boot token-row reconciliation not applied: %v", err)
		return
	}
	if dropped > 0 {
		m.logf("boot token-row reconciliation dropped %d stale row(s)", dropped)
	}
}

// reconcileStoreSyncIntents applies §9's generic intent rules for every intent
// hub.toml still carries and no open compensation owns: the purge is re-applied
// where the intent's rows are still present (the swap landed, the store
// lagged), and the intent is cleared in its own follow-up atomic write once
// hub.toml and the store agree — the store-already-applied no-op included. No
// converged intent survives its boot; a store failure leaves the intent for the
// next boot or the next mutation's write.
func (m *hubHostManager) reconcileStoreSyncIntents() {
	fileCfg, hasFile := m.hostFileRecords()
	if !hasFile {
		return
	}
	for _, name := range sortedMapKeys(fileCfg.PendingStoreSync) {
		intent := fileCfg.PendingStoreSync[name]
		if _, open := m.cfg.ops.Compensation(name); open {
			// A live compensation record owns this name's convergence: its phase
			// arms decide whether the purge stands or the rows come back.
			continue
		}
		purged, err := m.cfg.ops.ApplyStoreSync(hostops.StoreSyncIntent{
			Host:       name,
			Generation: intent.Generation,
			Values:     intent.TokenValues,
		})
		if err != nil {
			m.logf("boot store-sync intent for %q not applied: %v", name, err)
			continue
		}
		entries := m.cfg.store.snapshot()
		if err := m.persistHosts(entries, entries, hostPersistChange{dropStoreSync: name}); err != nil {
			m.logf("boot store-sync intent for %q not cleared: %v", name, err)
			continue
		}
		if purged > 0 {
			m.logf("boot re-applied the store-sync purge for %q (%d row(s)) and cleared the intent", name, purged)
		} else {
			m.logf("boot found the store-sync intent for %q already converged and cleared it", name)
		}
	}
}

// reconcileCompensations resumes every open `pendingCompensation` record by
// phase (§9's arms), never by blind re-insert: an armed record checks the
// purge first, a hubtoml record restores hub.toml from its stash before
// touching rows, a rows record re-inserts exactly the rows the restored
// hub.toml revalidates, a runtime record re-applies the restored set before
// clearing, and a clear record clears without resurrecting rows. A failed step
// leaves the record where it is, with its stash intact — the next boot retries.
func (m *hubHostManager) reconcileCompensations() {
	records := m.cfg.ops.Compensations()
	for _, host := range sortedMapKeys(records) {
		record, ok := m.cfg.ops.Compensation(host)
		if !ok {
			continue
		}
		if hostops.NormalizeCompensationPhase(record.Phase) == hostops.CompensationArmed {
			m.resumeArmedCompensation(record)
			continue
		}
		m.resumeCompensation(record)
	}
}

// resumeArmedCompensation runs §9's armed arm: the purge is checked first.
// An intent already cleared means the commit path passed the commit point, so
// the record clears without resurrecting rows; an intent still present with the
// rows still present means the purge never landed, so hub.toml is restored from
// the stash and the rows stay untouched; an intent still present with the rows
// absent means the purge landed before the crash, so the record advances and
// the hubtoml arm follows.
func (m *hubHostManager) resumeArmedCompensation(record hostops.Compensation) {
	fileCfg, hasFile := m.hostFileRecords()
	intent, intentLive := fileCfg.PendingStoreSync[record.Host]
	rowsPresent := false
	if row, ok := m.cfg.ops.OutstandingToken(record.Host); ok {
		for _, preimage := range record.Rows {
			if preimage.Value == row.Value {
				rowsPresent = true
			}
		}
	}
	switch {
	case !hasFile || !intentLive:
		// The commit path passed the commit point: the purge stands and nothing
		// is resurrected. This arm also covers the crash window where the
		// rows-present restore below landed and the process died before the
		// advance: the record is still armed and the file is the restored
		// pre-mutation bytes, which carry no intent. That state converges by
		// construction — the boot that reads this record loaded those very
		// bytes, so its registry, store model, and runtime already serve the
		// restored set; the restored file carries no tombstone, so no mark was
		// taken; and the purge never landed, so the rows are untouched and
		// nothing needs re-inserting.
		m.clearCompensation(record, "the intent is already cleared")
		_ = intent
	case rowsPresent:
		if err := m.restoreHubTOMLFromStash(record.Stash); err != nil {
			m.logf("boot compensation for %q not resumed: %v", record.Host, err)
			return
		}
		// The restored bytes are the authority for the machine records too.
		if restored, ok := m.hostFileRecords(); ok {
			m.installRestoredRecords(restored)
		}
		// The purge never landed and the rows are untouched, but the swap
		// compensation still owes the runtime revert. Continue from the rows
		// arm: the hubtoml arm's commit-point check reads the intent off the
		// file, and the restored pre-mutation bytes carry no intent by
		// construction, so re-entering it here would clear the record without
		// re-inserting the preimage rows, re-applying the restored runtime, or
		// reversing the host-removed marks.
		// The restore just ran, so the record passes through the hubtoml phase
		// (the phase that names the restore step) and on to the rows arm. Each
		// advance is its own store write, exactly as the restorer's per-step
		// discipline requires: a crash between them leaves the record in
		// `compensating-hubtoml` with the file already restored, and the next
		// boot's hubtoml arm sees the restored bytes carry no intent and takes
		// the commit-point clear traced there.
		if err := m.cfg.ops.AdvanceCompensation(record.Host, hostops.CompensationHubTOML); err != nil {
			m.logf("boot compensation for %q not advanced past the restore: %v", record.Host, err)
			return
		}
		if err := m.cfg.ops.AdvanceCompensation(record.Host, hostops.CompensationRows); err != nil {
			m.logf("boot compensation for %q not advanced to the rows arm: %v", record.Host, err)
			return
		}
		record.Phase = hostops.CompensationRows
		m.resumeCompensation(record)
	default:
		if err := m.cfg.ops.AdvanceCompensation(record.Host, hostops.CompensationHubTOML); err != nil {
			m.logf("boot compensation for %q not advanced: %v", record.Host, err)
			return
		}
		record.Phase = hostops.CompensationHubTOML
		m.resumeCompensation(record)
	}
}

// resumeCompensation follows the non-armed arms of §9's phase machine: each
// step lands durably before the next, and a failure returns with the record in
// the phase the failing step belongs to (a runtime-revert failure leaves the
// record in `compensating-runtime` with the stash intact).
func (m *hubHostManager) resumeCompensation(record hostops.Compensation) {
	switch hostops.NormalizeCompensationPhase(record.Phase) {
	case hostops.CompensationHubTOML:
		// §9's commit-point check, the same one the armed arm makes.
		fileCfg, hasFile := m.hostFileRecords()
		if !hasFile {
			// No file to converge with: the boot pipeline enters here only for a
			// genuinely absent or empty document (hostFileAbsentOrEmpty gates it),
			// so there are no bytes to compare or restore and the record clears
			// as before.
			m.clearCompensation(record, "no hub.toml to converge with")
			return
		}
		if _, intentLive := fileCfg.PendingStoreSync[record.Host]; intentLive {
			// The commit path had not passed its commit point: restore and
			// follow the arms.
			if err := m.restoreHubTOMLFromStash(record.Stash); err != nil {
				m.logf("boot compensation for %q: hub.toml not restored: %v", record.Host, err)
				return
			}
			// The restored bytes are the authority for every machine record they
			// carry: the store's model converges to them here, so the rest of this
			// boot and every later write derive from the restored file rather than
			// re-emitting the compensated commit's records.
			if restored, ok := m.hostFileRecords(); ok {
				m.installRestoredRecords(restored)
			}
			if !m.advanceCompensationTo(record, hostops.CompensationRows, "advance-rows") {
				return
			}
			m.resumeFromRows(record)
			return
		}
		// The intent is cleared. Two windows land here:
		//
		//   - the commit path passed its commit point: the purge stands and the
		//     file carries the committed state — nothing is restored, nothing
		//     owed;
		//   - a previous boot's hubtoml arm restored the file and then failed (or
		//     crashed) before advancing to the rows arm: the purge landed, so the
		//     preimage rows are missing and the restored generation revalidates
		//     them — the re-insertion is still owed. Clearing here would drop the
		//     record and its stash with the rows missing, diverged forever.
		//
		// No whole-file comparison can tell these apart: any later write to
		// hub.toml — another host's mutation, another boot's reconcile — breaks
		// the restored file's byte equality with the stash, and the owed-rows
		// window would then read as "the commit stands". So the arm decides
		// per name instead: it advances to the rows arm and resumes, where the
		// rows pass re-inserts exactly the preimage rows the CURRENT file
		// revalidates (the name live, at the preimage generation and
		// incarnation) and re-applies the current file's runtime. For the
		// commit-passed removal the name is not live (or its live pair differs),
		// so the rows pass re-inserts nothing, the runtime pass converges the
		// store model to the current file, and the record clears — the same
		// outcome, decided per name rather than per file.
		if !m.advanceCompensationTo(record, hostops.CompensationRows, "advance-rows") {
			return
		}
		m.resumeFromRows(record)
	case hostops.CompensationRows:
		m.resumeFromRows(record)
	case hostops.CompensationRuntime:
		m.resumeFromRuntime(record)
	case hostops.CompensationClear:
		if !m.compensationStepAllowed(record.Host, "clear") {
			return
		}
		m.clearCompensation(record, "the rows are already converged")
	}
}

// resumeFromRows runs §9's rows arm and everything after it: the preimage rows
// the restored hub.toml's generation revalidates come back, the restored runtime
// set is re-applied to the live handles, and only then does the record clear.
func (m *hubHostManager) resumeFromRows(record hostops.Compensation) {
	restored, ok := m.hostFileRecords()
	generation, incarnation, live := uint64(0), "", false
	if ok {
		generation, incarnation, live = m.restoredIdentity(restored, record.Host)
	}
	if !m.compensationStepAllowed(record.Host, "reinsert") {
		return
	}
	inserted, err := m.cfg.ops.ReinsertCompensationRows(record.Host, func(row hostops.Token) bool {
		return live && row.Generation == generation && row.IncarnationID == incarnation
	})
	if err != nil {
		m.logf("boot compensation for %q: rows not re-inserted: %v", record.Host, err)
		return
	}
	if inserted > 0 {
		m.logf("boot compensation for %q re-inserted %d token row(s)", record.Host, inserted)
	}
	m.resumeFromRuntime(record)
}

// resumeFromRuntime runs §9's runtime arm and the clear: the restored hub.toml's
// runtime set is re-applied to the live handles first, and a failure leaves the
// record in `compensating-runtime` with its stash intact, never a cleared
// compensation beside a diverged runtime.
func (m *hubHostManager) resumeFromRuntime(record hostops.Compensation) {
	if err := m.reapplyRestoredRuntime(record.Host); err != nil {
		m.logf("boot compensation for %q: runtime revert failed, record left in %s: %v", record.Host, record.Phase, err)
		return
	}
	if !m.advanceCompensationTo(record, hostops.CompensationClear, "advance-clear") {
		return
	}
	if !m.compensationStepAllowed(record.Host, "clear") {
		return
	}
	m.clearCompensation(record, "the restoration converged")
}

// advanceCompensationTo advances one record to the next phase in its own store
// write, honoring the test-only step seam and logging a failure.
func (m *hubHostManager) advanceCompensationTo(record hostops.Compensation, to hostops.CompensationPhase, step string) bool {
	if !m.compensationStepAllowed(record.Host, step) {
		return false
	}
	if err := m.cfg.ops.AdvanceCompensation(record.Host, to); err != nil {
		m.logf("boot compensation for %q not advanced to %s: %v", record.Host, to, err)
		return false
	}
	return true
}

// compensationStepAllowed consults the test-only compensation-step seam; nil in
// production.
func (m *hubHostManager) compensationStepAllowed(host, step string) bool {
	if err := m.compensationStepFailure(host, step); err != nil {
		m.logf("boot compensation for %q: step %s failed; the record and its stash are left for the next boot: %v", host, step, err)
		return false
	}
	return true
}

// clearCompensation drops one record and its stash: the compensation
// converged, and no live record names the stash any more.
func (m *hubHostManager) clearCompensation(record hostops.Compensation, why string) {
	if err := m.cfg.ops.ClearCompensation(record.Host); err != nil {
		m.logf("boot compensation for %q not cleared (%s): %v", record.Host, why, err)
		return
	}
	m.pruneHubTOMLStash(record.Stash)
	m.logf("boot compensation for %q cleared: %s", record.Host, why)
}

// restoredIdentity returns the identity the rows arm revalidates a preimage row
// against: the restored hub.toml's live entry for name, with the generation and
// incarnation the restored bytes record. A hand-authored restored file (or one
// whose machine records were lost) carries no [generations] mark or no
// [host_records] record; the live registry's own pair is then the identity the
// row must bind to, exactly what the registry's load minted for the name — a
// zero pair would re-insert nothing.
func (m *hubHostManager) restoredIdentity(cfg Config, name string) (uint64, string, bool) {
	entry, present := hostEntryNamed(hostRegistryEntries(cfg), name)
	if !present {
		return 0, "", false
	}
	if entry.Generation == 0 || entry.IncarnationID == "" {
		if current, ok := m.cfg.hosts.Get(name); ok {
			if entry.Generation == 0 {
				entry.Generation = current.Generation
			}
			if entry.IncarnationID == "" {
				entry.IncarnationID = current.IncarnationID
			}
		}
	}
	return entry.Generation, entry.IncarnationID, true
}

// reapplyRestoredRuntime re-applies the restored hub.toml's runtime set to the
// live handles (§9's runtime arm): the durable entry set converges to the
// restored file, the registry's counters are seeded to its marks, and a name
// the restored file no longer carries drops its derived state. The process must
// actually serve the restored entry before the record may clear: hostreg has no
// boot-restore setter for a persisted identity, so a name the boot registry did
// not register (or registered under another identity) returns an error and
// leaves the compensation open — the next boot loads the restored file with its
// registry and converges it.
func (m *hubHostManager) reapplyRestoredRuntime(host string) error {
	if m.testOnlyFailRuntimeRevert != nil {
		if err := m.testOnlyFailRuntimeRevert(host); err != nil {
			return err
		}
	}
	restored, ok := m.hostFileRecords()
	if !ok {
		return fmt.Errorf("restored %s is unreadable", m.cfg.configPath)
	}
	entries := hostRegistryEntries(restored)
	entry, present := hostEntryNamed(entries, host)
	if !present {
		m.installRestoredRecords(restored)
		m.cfg.mu.Lock()
		defer m.cfg.mu.Unlock()
		m.dropHostDerivedState(host)
		return nil
	}
	// §9's runtime arm re-applies the restored set to the live handles: the
	// process must actually serve the restored entry before the record may
	// clear. hostreg has no boot-restore setter for a persisted identity, so a
	// name the boot registry did not register (or registered under another
	// identity) leaves the record open — the next boot loads the restored file
	// and converges it.
	current, registered := m.cfg.hosts.Get(host)
	if !registered {
		return fmt.Errorf("host %q is live in the restored %s but the boot registry did not register it; leaving the compensation open for the next boot",
			host, m.cfg.configPath)
	}
	if entry.IncarnationID != "" && current.IncarnationID != entry.IncarnationID {
		return fmt.Errorf("host %q carries a different restored incarnation id; leaving the compensation open", host)
	}
	if completeIdentity(entry) && !sameEffectiveHostEntry(current, entry) {
		return fmt.Errorf("host %q carries a different identity in the restored %s than the live registry; leaving the compensation open",
			host, m.cfg.configPath)
	}
	m.installRestoredRecords(restored)
	// §4's host-removed pass may have marked the restored incarnation's records
	// (the boot ran it against the compensated commit's tombstone) and dropped
	// its token rows. The restored file carries the incarnation live again, so
	// the marks the removed pair left are reversed: a marked record would make a
	// same-key retry read as a current-generation `host-removed` record
	// (conflicting-operation-id) instead of replaying the interrupted record,
	// and the row would render removed.
	if entry.Generation != 0 && entry.IncarnationID != "" {
		if cleared, err := m.cfg.ops.ClearHostRemovedMarks(host, hostops.HostRemovedMark{
			Generation:    entry.Generation,
			IncarnationID: entry.IncarnationID,
		}); err != nil {
			m.logf("boot compensation for %q: restored incarnation's host-removed marks not cleared: %v", host, err)
		} else if cleared > 0 {
			m.logf("boot compensation for %q cleared %d host-removed mark(s) on the restored incarnation", host, cleared)
		}
	}
	return nil
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

// rollbackHubTOML re-persists previous after a post-write live mutation failed,
// keeping hub.toml in step with the live set: the API reported the
// mutation as failed, so the file must not keep a copy the next start would
// resurrect (Add) or drop an entry the live set still holds (Remove).
// previous is the content the file must hold for the live set to stay in
// step: the pre-add contents for Add, and for Remove the live snapshot
// after the failed removal re-added its row — a stale pre-remove copy would
// clobber entries concurrently committed while the removal's teardown ran
// unlocked. This is the compensating half of the durable-first ordering: the
// save still leads, so a save failure still commits nothing live and the
// caller can retry, and a live mutation that fails after the save landed is
// rolled back rather than left diverging. A rollback save failure is
// surfaced alongside cause: the file is
// then known to diverge, and the caller must hear it rather than a
// clean-looking refusal. A rollback whose own rename landed while its
// directory step failed is the other case: the file already holds previous,
// so the state agrees and only the rollback's crash durability is uncertain
// — reported as landed beside the cause, never as a failed rollback.
func (m *hubHostManager) rollbackHubTOML(previous, known []hostreg.Host, cause error, change hostPersistChange) error {
	if err := m.persistHosts(previous, known, change); err != nil {
		if hubTOMLRenameCommitted(err) {
			// The rollback's own rename landed: the file holds previous, the
			// content the rollback exists to restore, and only its
			// durability step failed — the file and the live set agree. Say
			// that beside the cause rather than claiming a rollback failure
			// that did not happen.
			return fmt.Errorf("%w; hub.toml rollback landed but its directory step failed: %w", cause, err)
		}
		return fmt.Errorf("%w; %w: %w", cause, errHubTOMLRollbackFailed, err)
	}
	return cause
}

// errHubTOMLRollbackFailed marks the compensation return whose hub.toml
// restore did not converge: the rollback's own write failed before its rename,
// so the file may still hold the failed write. The deploy pipeline's cross-file
// compensation reads it to keep its store-local record and stash for a later
// boot rather than clearing a compensation beside a diverged file.
var errHubTOMLRollbackFailed = errors.New("hub.toml rollback failed")

// List returns every known host with truthful online state in name-sorted
// order (the registry's own; every row's origin field reads `hub.toml`).
// Attached rows report live channel facts and retain them
// as last-known; rows without a live channel render as offline with the
// retained attach state and last-known facts. It never dials.
//
// The mutation mutex covers only the snapshot — the registry rows — so a
// concurrent reader never observes the window between a registry insert and
// the store row and source registration that finish it: a half-committed host
// would list with no source.
// The row building that follows runs lock-free: hostRow's
// attached-only lookups and its facts read run on the network, and one slow
// or hung host must not block every concurrent Add and Remove commit or
// serialize other lists. The rows are the snapshot's point-in-time view: a
// host added after the snapshot is absent from that response, never
// half-committed in it. A host mid-removal still lists while its registry
// entry lasts — the removal mark stands in for the store row its commit
// already dropped — and every row carries the one origin marker, because every
// host lives in the machine-managed hub.toml.
func (m *hubHostManager) List(ctx context.Context, _ appwire.EmptyParams) (appwire.HostListResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostListResponse{}, err
	}
	m.cfg.mu.Lock()
	hosts := m.cfg.hosts.All()
	m.cfg.mu.Unlock()
	// rows is non-nil even when no host is registered: make returns a usable
	// empty slice, and the wire type's non-nullable hosts array must never
	// marshal as JSON null.
	rows := make([]appwire.HostRow, 0, len(hosts))
	live := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		live[host.Name] = struct{}{}
		rows = append(rows, m.hostRow(ctx, host))
	}
	// Tombstone rows render from the durable tombstone set: `removed: true`
	// plus the retained-row count, from the tombstone's retained effective
	// HostConfig with attached/midEnsure false and the removed entry's origin,
	// generation, and incarnation id; the facts/error optionals stay absent
	// (spec §4/§11). The expiry filter below is IN MEMORY only — `list` takes
	// no mutation lock and prunes nothing durably (spec §15: "the read path
	// omits it from the response without taking the lock, and the next
	// mutation-path atomic hub.toml write under the lock prunes it durably");
	// a tombstone whose name still holds an open teardown remnant is rendered
	// even past its retention (the gate keeps it alive).
	for name, tombstone := range m.cfg.store.tombstoneSnapshot() {
		if _, isLive := live[name]; isLive {
			// Defensive only: the derivation drops a live name's tombstone, so
			// a loaded set cannot hold one.
			continue
		}
		if !m.remnantGated(name) && tombstoneExpired(tombstone, m.nowTime(), m.cfg.policy.tombstoneRetention) {
			continue
		}
		rows = append(rows, tombstoneRow(tombstone))
	}
	// Every row — live or tombstone — carries the remnant fence's own fields
	// (spec §11): `openRemnantId` exactly when the name holds an open remnant,
	// and `escalationAgeSec` when that remnant is past the escalation bound, so
	// a reader can name the blocking remnant for the teardown-retry affordance
	// without a second lookup.
	for i := range rows {
		m.stampRemnantRow(&rows[i])
	}
	slices.SortStableFunc(rows, func(a, b appwire.HostRow) int { return strings.Compare(a.Name, b.Name) })
	return appwire.HostListResponse{Hosts: rows}, nil
}

// Status returns one host's row: the same HostRow evener/host/list serves.
// Unknown names are InvalidParams. Never dials. It snapshots the host under
// the same mutation mutex List does, for the same
// fully-committed-row guarantee, and then builds the row lock-free for the
// same reason List does: the facts read must not hold up commits.
func (m *hubHostManager) Status(ctx context.Context, params appwire.HostStatusParams) (appwire.HostStatusResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostStatusResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	m.cfg.mu.Lock()
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	m.cfg.mu.Unlock()
	row := m.hostRow(ctx, host)
	m.stampRemnantRow(&row)
	return appwire.HostStatusResponse{Host: row}, nil
}

// stampRemnantRow fills a row's remnant-fence fields from the durable remnant
// set: `openRemnantId` exactly when the name holds an open remnant, and
// `escalationAgeSec` only when that remnant is past the escalation bound. Both
// stay absent otherwise, per the absent-when-unknown rule.
func (m *hubHostManager) stampRemnantRow(row *appwire.HostRow) {
	remnantID, open := m.cfg.store.markedRemnantFor(row.Name)
	if !open {
		return
	}
	row.OpenRemnantID = remnantID
	if remnant, ok := m.cfg.store.remnantByID(remnantID); ok {
		row.EscalationAgeSec = m.escalationAgeSec(remnant)
	}
}

// sortedStagedMarkerNames returns a staged-marker set's host names in sorted
// order, so the boot's finalization pass walks a deterministic order.
func sortedStagedMarkerNames(markers map[string]HostStagedReceipt) []string {
	names := make([]string, 0, len(markers))
	for name := range markers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
