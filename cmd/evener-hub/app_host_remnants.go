package hub

// This file owns the durable teardown-repair records registry spec 08 §5/§6
// defines: the staged-receipt marker one commit's step-(2) hub.toml write
// carries, the teardown-remnant records a committed-with-teardown-failure
// leaves behind, the typed resolved-remnant records a clearance records, and
// the attempt records `teardown-retry`/`teardown-recover` claim.
//
// Spec §5 (staged commit, order matters) puts the marker on the step-(2) write:
// "The step-(2) `hub.toml` write carries a transient staged-receipt marker: the
// scoped key plus `stagedAt`, a `swapStarted` intent (false at stage time,
// flipped true in its own atomic `hub.toml` write under the mutation lock
// before the runtime transition begins ...), a `teardownStarted` flag (false at
// stage time, flipped true in its own atomic write after the swap and before
// the first teardown ...), the collision-reconcile armed intent (the
// validation-read `hub.toml` fingerprint the commit staged against ...), and
// the staged provisional payload (explicitly provisional outcome, row,
// generation, a pre-minted `remnantId`, and the pinned teardown target — the
// in-progress remnant ...)". "The markers form a map keyed by host name — at
// most one staged entry per host."
//
// Spec §6 (persistence) owns the remnant record's shape: "`teardownRemnants`
// maps the server-generated opaque `remnantId` to `{host, kind, seam,
// pendingTeardown, generation, incarnationId, mutationKey, committedAt,
// cleanupHandle}`; `cleanupHandle` is the independently actionable
// ownership/remote-cleanup handle persisted at commit, resolvable without any
// live in-process handle." A cleared remnant becomes a typed resolved record in
// the same section: "{clearedAt, resolutionKind: "retry" | "recover", hostKind:
// "live" | "removed", host, name, attestation?} plus the receipt reference the
// clearance recorded".
//
// `pendingTeardown` is "the self-contained generation-scoped teardown target —
// the staged supervisor/channel/fan-out teardown description pinned at commit,
// resolvable without the live entry; a remnant never depends on the live
// registry to execute."

// Boundaries this slice deliberately does not cross (each is recorded where it
// is felt as well):
//
//   - Read-path fingerprint discipline (registry spec 08 §4/§15): `list`'s
//     changed-entry refusal and the async debounced reconcile are the read
//     path's own slice. The `concurrent-edit` type is defined here because the
//     commit path needs it; the read path does not consult it yet.
//   - Fencing execution (withdrawn, comp08): the remote lease/guard wrapper,
//     kill/wait of superseded epochs, and the guard advance are gone. The retry
//     and recover attempt records keep their epoch fields as historical data;
//     the takeover of a timed-out attempt records its fenced-closed mark only,
//     and the abandoned run may still execute (the accepted residual).
//   - The store-side compensation record (deploy-pipeline spec §9, S7's
//     `pendingStoreSync`/`pendingCompensation`): pre-commit compensation stays
//     the hub.toml rollback (plus the re-read preimage), with no cross-file
//     intent record.
//   - `attachUnderGate`'s reservation primitive (S13): the attach fence refusal
//     is here; the reservation is not.
//   - The retry/recover UI affordances (S16): the store narrows the union and
//     never reads a failure arm as success; the buttons and the escalation
//     display are S16's.
//   - §11's spelling reshape (`address` -> `ssh`, ...): owed, not done here.
//   - The `host-removed` operation-store mark path on remove: not built here.
//
//	The durable teardown-repair records: the staged-receipt marker one commit's
//	step-(2) hub.toml write carries, the teardown-remnant records a
//	committed-with-teardown-failure leaves behind, the typed resolved-remnant
//	records a clearance records, and the attempt records `teardown-retry` and
//	`teardown-recover` claim.

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// MaxRemnantIDBytes bounds a server-generated remnant id: spec §6 requires it
// "non-empty, at most 128 bytes, unique per remnant". The minted uuid is far
// inside it; the bound is what the record validation enforces.
const MaxRemnantIDBytes = 128

// The mutation kinds a remnant and a staged marker name — the same durable
// vocabulary the receipt scoped key carries.
const (
	hostRemnantKindAdd    = "add"
	hostRemnantKindUpdate = "update"
	hostRemnantKindRemove = "remove"
)

// The teardown target's kind: which post-commit rebind step the remnant's
// pinned teardown re-runs.
const (
	hostTeardownKindUpdate = "update"
	hostTeardownKindRemove = "remove"
)

// The staged marker's runtime phase (spec §5: "flipped to `runtime-swapped` in
// the same atomic write that flips `teardownStarted` after a successful swap").
const (
	hostStagedPhaseStaged         = "staged"
	hostStagedPhaseRuntimeSwapped = "runtime-swapped"
)

// The resolution kinds a resolved-remnant record carries (spec §6:
// `resolutionKind: "retry" | "recover"`).
const (
	hostRemnantResolutionRetry   = "retry"
	hostRemnantResolutionRecover = "recover"
)

// The host shapes a resolved-remnant record and a retry response name: whether
// the cleared generation left a live row or a tombstone.
const (
	hostRemnantHostKindLive    = "live"
	hostRemnantHostKindRemoved = "removed"
)

// The attempt record's states. An attempt is written open when it claims the
// remnant (spec §6: "the retry persists each attempt as a durable attempt
// record (server-generated attempt id, fencing epoch, start time, `open`
// state)"); a bounded run that times out stays open but records the timeout —
// "marks its attempt record timed-out-but-open" — until a later retry takes
// over its fencing epoch and marks it fenced-closed.
const (
	hostAttemptStateOpen         = "open"
	hostAttemptStateFencedClosed = "fenced-closed"
)

// hostRecoveryStatement is the one attestation statement `teardown-recover`
// accepts (spec §6/§11: `statement: "teardown-verified-absent"`).
const hostRecoveryStatement = "teardown-verified-absent"

// HostRecoveryAttestation is the audited recovery attestation (spec §6:
// "`recoveryAttestation` (`{operator, statement, observedAt}`) is present
// exactly on receipts resolved through `teardown-recover`, absent otherwise").
// It is validated against the wire's own shape before any clearance: the
// statement must be exactly hostRecoveryStatement, observedAt an RFC3339
// instant, and operator the session's authenticated identity.
type HostRecoveryAttestation struct {
	Operator   string `toml:"operator"`
	Statement  string `toml:"statement"`
	ObservedAt string `toml:"observed_at"` //nolint:tagliatelle // §6 stores this row in the lowerCamel wire vocabulary
}

// HostPendingTeardown is the remnant's pinned, self-contained teardown target
// (spec §6): the staged supervisor/channel/fan-out teardown description for one
// commit, bound to the generation and incarnation id the commit pinned, and
// resolvable without the live entry. A retry executes exactly this and never
// the name's live entry, live channel, or live supervisor set (spec §6:
// "Incarnation-scoped teardown").
type HostPendingTeardown struct {
	// Name is the host whose lifecycle handles the teardown destroys.
	Name string `toml:"name"`
	// Kind is the post-commit rebind step the teardown re-runs.
	Kind string `toml:"kind"`
	// Generation and IncarnationID are the pinned identity: every handle the
	// teardown may touch is targeted by incarnation-id equality against this
	// pair, never by name lookup alone.
	Generation    uint64 `toml:"generation"`
	IncarnationID string `toml:"incarnation_id"`
	// Supervisor, Channel, and FanOuts name the handles the teardown destroys:
	// the host's supervisor/channel binding and the per-host lifecycle fan-outs
	// (the remote-source subscription and the host-admin request/notification
	// fan-out). Where the pinned target is empty the re-run is a no-op (spec
	// §5).
	Supervisor bool     `toml:"supervisor,omitempty"`
	Channel    bool     `toml:"channel,omitempty"`
	FanOuts    []string `toml:"fan_outs,omitempty"`
}

// HostCleanupHandle is the remnant's durable ownership/remote-cleanup handle
// (spec §6): "the independently actionable ownership/remote-cleanup handle
// persisted at commit, resolvable without any live in-process handle". This
// build's local seams are resolved by the durable ownership boundary the
// operation store mirrors (registry spec 08 §7's `{generation, incarnationId,
// presenceEpoch}` record), so the handle is the persisted boundary identity
// loaded from the record — record load, never live-handle resurrection (spec
// §6: "rehydration is record load, never live-handle resurrection (no
// in-process handle survives restart — §7)").
//
// BOUNDARY (withdrawn, comp08): the remote arm an earlier revision specified
// here — the remote guard-file identity plus the orphan epoch's lease-entry
// ownership tokens, killed and verified through the lease wrapper — was removed
// with the crash-fencing program. The record's retired fields stay decodable;
// this build resolves and verifies the local boundary only.
type HostCleanupHandle struct {
	// Kind names which durable handle resolves the cleanup. This build writes
	// `local-boundary`; any other kind refuses the typed `teardown-unknown-key`.
	Kind string `toml:"kind"`
	// Generation, IncarnationID, and PresenceEpoch are the persisted ownership
	// boundary the handle resolves through.
	Generation    uint64 `toml:"generation"`
	IncarnationID string `toml:"incarnation_id"`
	PresenceEpoch uint64 `toml:"presence_epoch"`
	// RemoteGuardFile and LeaseEntryTokens are retired (comp08): a prior file's
	// fields stay decodable and are dropped, never written. Empty here.
	RemoteGuardFile   string   `toml:"remote_guard_file,omitempty"`
	LeaseEntryTokens  []string `toml:"lease_entry_tokens,omitempty"`
	FencingEpochBoot  string   `toml:"fencing_epoch_boot,omitempty"`
	FencingEpochOpSeq uint64   `toml:"fencing_epoch_op_seq,omitempty"`
}

// cleanupHandleKindLocal is this build's only cleanup-handle kind.
const cleanupHandleKindLocal = "local-boundary"

// resolvable reports whether the handle names enough durable identity to act:
// the local arm needs the full boundary triple, the remote arm the guard file
// the lease wrapper resolves through.
func (h HostCleanupHandle) resolvable() bool {
	switch h.Kind {
	case cleanupHandleKindLocal:
		// The boundary identity the handle resolves through is
		// (generation, incarnation id): the presence epoch is provenance, and a
		// crash-window marker whose epoch the records no longer carry must still
		// yield an actionable handle rather than a stranding one.
		return h.Generation != 0 && validIncarnationID(h.IncarnationID)
	case "remote-lease":
		return h.RemoteGuardFile != "" && validIncarnationID(h.IncarnationID) && h.Generation != 0
	default:
		return false
	}
}

// HostResolvedRemnant is the typed resolved-remnant record a clearance writes
// in the same atomic hub.toml write that drops the open remnant (spec §6: "A
// cleared remnant persists as a typed resolved-remnant record in the same
// section, keyed by `remnantId` and carrying `{clearedAt, resolutionKind:
// "retry" | "recover", hostKind: "live" | "removed", host, name, attestation?}`
// plus the receipt reference the clearance recorded — the full replay payload a
// lost-response retry renders (`already-cleared` with its `hostKind` pairing,
// `recovered-cleared` with `clearedName`/`clearedAt`), reconstructable after
// restart without the live entry"). `Attestation` is present exactly on
// `recover` records.
type HostResolvedRemnant struct {
	ClearedAt      string                   `toml:"cleared_at"`
	ResolutionKind string                   `toml:"resolution_kind"`
	HostKind       string                   `toml:"host_kind"`
	MutationKey    string                   `toml:"mutation_key,omitempty"`
	Attestation    *HostRecoveryAttestation `toml:"attestation,omitempty"`
}

// HostTeardownRemnant is one [teardown_remnants."<remnantId>"] record: an open
// remnant (spec §6's map value) or, once cleared, the same key carrying the
// resolved record. One record shape for both keeps the clearance a single
// atomic write that never has to move a key between sections, and keeps
// `already-cleared` a lookup on the very id the retry names.
type HostTeardownRemnant struct {
	// remnantID is the record's own map key, carried in memory so a scan can
	// name the record without re-deriving the key. It is never encoded: the
	// section's key IS the id.
	remnantID string
	// Host is the fenced name.
	Host string `toml:"host"`
	// Kind is the mutation kind whose commit left the remnant: add, update, or
	// remove.
	Kind string `toml:"kind"`
	// Seam names the failed rebind step (spec §11: "`seam` names the failed
	// rebind step").
	Seam string `toml:"seam"`
	// PendingTeardown is the pinned, self-contained teardown target.
	PendingTeardown HostPendingTeardown `toml:"pending_teardown"`
	// Generation and IncarnationID are the pinned identity the retry validates
	// against — never the registry's current values (spec §6: "it re-resolves
	// the pinned teardown target by the remnant's recorded `(generation,
	// incarnationId)` plus its persisted `cleanupHandle`").
	Generation    uint64 `toml:"generation"`
	IncarnationID string `toml:"incarnation_id"`
	// MutationKey is the scoped receipt key the commit wrote (spec §6: "the
	// mutation's scoped receipt key plus the in-progress remnant already pinned
	// in the step-(2) marker"). The clearance records it on the resolved record
	// as the receipt reference.
	MutationKey string `toml:"mutation_key"`
	// CommittedAt is the commit instant.
	CommittedAt string `toml:"committed_at"`
	// CleanupHandle is the durable ownership/remote-cleanup handle.
	CleanupHandle HostCleanupHandle `toml:"cleanup_handle"`
	// Resolved is present exactly once the remnant is cleared.
	Resolved *HostResolvedRemnant `toml:"resolved,omitempty"`
}

// open reports whether the remnant still fences its name.
func (r HostTeardownRemnant) open() bool { return r.Resolved == nil }

// hostKindOf names which row shape the cleared or fenced generation has:
// `removed` when the remnant belongs to a `remove`, `live` otherwise (spec §11).
func hostKindOf(kind string) string {
	if kind == hostRemnantKindRemove {
		return hostRemnantHostKindRemoved
	}
	return hostRemnantHostKindLive
}

// HostStagedReceipt is one [staged_receipts."<hostName>"] record: the transient
// staged-receipt marker spec §5's step-(2) write carries. Exactly one exists
// per host name, so a staged marker for host A never blocks a mutation on host
// B, and every marker write preserves other hosts' entries verbatim.
type HostStagedReceipt struct {
	// Key is the scoped receipt key the commit will finalize under.
	Key string `toml:"key"`
	// StagedAt is when the step-(2) write landed.
	StagedAt string `toml:"staged_at"`
	// SwapStarted is the durable swap intent: false at stage time, flipped true
	// in its own atomic write before the runtime transition begins. A finalizing
	// claim preserves the marker's persisted value, never forcing it true.
	SwapStarted bool `toml:"swap_started"`
	// TeardownStarted is the durable post-swap flag: false at stage time,
	// flipped true in its own atomic write after the swap and before the first
	// teardown. A finalizing claim preserves the persisted flag the same way —
	// boot recovery decides by it, "never by treating every claim as
	// teardown-started" (spec §5).
	TeardownStarted bool `toml:"teardown_started"`
	// Phase is the marker's runtime phase: hostStagedPhaseStaged at stage time,
	// hostStagedPhaseRuntimeSwapped in the same atomic write that flips
	// TeardownStarted.
	Phase string `toml:"phase"`
	// ReconcileFingerprint is the collision-reconcile armed intent: the
	// validation-read hub.toml fingerprint the commit staged against. The
	// post-commit write replaces the marker with the finalized receipt, clearing
	// the armed intent with it.
	ReconcileFingerprint string `toml:"reconcile_fingerprint,omitempty"`
	// FinalizingToken is the server-generated opaque attempt token of a
	// finalizing claim: non-empty exactly while a finder owns the marker's
	// finalization. "Any other path finding a finalizing claim waits for or
	// recovers the claim instead of re-finalizing" (spec §5).
	FinalizingToken string `toml:"finalizing_token,omitempty"`
	// Provisional is the staged provisional payload: "explicitly provisional
	// outcome, row, generation, a pre-minted `remnantId`, and the pinned
	// teardown target". It is never returned as-is: every finalization path
	// re-runs the pinned teardown and finalizes from the observed result.
	Provisional HostMutationReceipt `toml:"provisional"`
	// PendingTeardown is the pinned teardown target the commit staged. It is
	// durable before the first teardown executes, so the target and its remnant
	// survive any crash.
	PendingTeardown HostPendingTeardown `toml:"pending_teardown"`
}

// HostTeardownAttempt is one [teardown_attempts."<attemptId>"] record: the
// durable attempt `teardown-retry` (or `teardown-recover`, which fences a
// timed-out attempt first) claims in the same atomic hub.toml write that claims
// the remnant. Spec §6: "the retry persists each attempt as a durable attempt
// record (server-generated attempt id, fencing epoch, start time, `open`
// state) in the same atomic `hub.toml` write that claims the remnant, and holds
// the host gate only while its attempt is live".
type HostTeardownAttempt struct {
	// RemnantID is the remnant the attempt claimed.
	RemnantID string `toml:"remnant_id"`
	// State is open or fenced-closed. A timed-out attempt stays open — "marks
	// its attempt record timed-out-but-open" — with TimedOutAt set.
	State string `toml:"state"`
	// StartedAt is when the attempt claimed the remnant.
	StartedAt string `toml:"started_at"`
	// TimedOutAt is present exactly on an attempt whose bounded execution
	// deadline expired while it stayed open for fencing.
	TimedOutAt string `toml:"timed_out_at,omitempty"`
	// FencedAt is present exactly on a fenced-closed attempt: when a later
	// retry took over its fencing epoch.
	FencedAt string `toml:"fenced_at,omitempty"`
	// FencingEpoch is the attempt's epoch pair (boot id + op sequence), the
	// historical field name retained on the record.
	FencingEpochBoot  string `toml:"fencing_epoch_boot,omitempty"`
	FencingEpochOpSeq uint64 `toml:"fencing_epoch_op_seq,omitempty"`
}

// open reports whether the attempt still fences the name.
func (a HostTeardownAttempt) open() bool { return a.State == hostAttemptStateOpen }

// timedOut reports whether the open attempt's bounded run already expired.
func (a HostTeardownAttempt) timedOut() bool { return a.open() && a.TimedOutAt != "" }

// mintRemnantID mints the server-generated opaque remnant id: "unique per
// remnant — never derived from the mutationId, so an ID-less failure still gets
// a retry handle and a reused mutationId can never collide with an earlier
// remnant" (spec §6).
func mintRemnantID() string { return uuid.NewString() }

// mintAttemptID mints the server-generated attempt id a claim records.
func mintAttemptID() string { return uuid.NewString() }

// mintFinalizingToken mints the opaque attempt token a finalizing claim
// carries (spec §5: "a finalizing claim carrying the same scoped key plus a
// server-generated opaque attempt token").
func mintFinalizingToken() string { return uuid.NewString() }

// pendingTeardownFor builds the pinned teardown target one mutation's commit
// stages: the lifecycle handles the post-commit rebind destroys for the entry
// as it stood before the mutation, bound to that entry's (generation,
// incarnation id) pair.
func pendingTeardownFor(entry hostreg.Host, kind string) HostPendingTeardown {
	target := HostPendingTeardown{
		Name:          entry.Name,
		Kind:          kind,
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
		Supervisor:    true,
		Channel:       true,
	}
	// The per-host lifecycle fan-outs the rebind cancels (spec §6: "its
	// remote-source subscription, its host-admin fan-out (request forwarding
	// plus notification fan-out), and its supervisor/channel binding").
	target.FanOuts = []string{"source-subscription", "host-admin-request", "host-admin-notification"}
	return target
}

// cleanupHandleFor builds the remnant's durable cleanup handle from the entry
// the commit pinned. The local boundary triple is the identity the retry
// verifies through — record load, never a live handle.
func cleanupHandleFor(entry hostreg.Host) HostCleanupHandle {
	return HostCleanupHandle{
		Kind:          cleanupHandleKindLocal,
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
		PresenceEpoch: entry.PresenceEpoch,
	}
}

// newTeardownRemnant builds the durable remnant one committed-with-teardown-failure
// records: the mutation's scoped receipt key, the pre-minted remnant id, the
// pinned teardown target, and the cleanup handle, all written by the very
// atomic hub.toml write that finalizes the commit (spec §6: "The same atomic
// `hub.toml` write that persists the committed receipt also persists a durable
// teardown-remnant record").
func newTeardownRemnant(kind, seam string, entry hostreg.Host, mutationKey string, committedAt time.Time) HostTeardownRemnant {
	return HostTeardownRemnant{
		Host:            entry.Name,
		Kind:            kind,
		Seam:            seam,
		PendingTeardown: pendingTeardownFor(entry, teardownKindFor(kind)),
		Generation:      entry.Generation,
		IncarnationID:   entry.IncarnationID,
		MutationKey:     mutationKey,
		CommittedAt:     committedAt.UTC().Format(time.RFC3339),
		CleanupHandle:   cleanupHandleFor(entry),
	}
}

// teardownKindFor maps a mutation kind onto its post-commit rebind step.
func teardownKindFor(kind string) string {
	if kind == hostRemnantKindRemove {
		return hostTeardownKindRemove
	}
	return hostTeardownKindUpdate
}

// ---------------------------------------------------------------------------
// store
// ---------------------------------------------------------------------------

// setRemnantMaps installs the file's staged-marker, remnant, and attempt sets at
// boot; the constructor calls it once with the records the file carried. The
// caller transfers ownership.
func (s *hostStore) setRemnantMaps(staged map[string]HostStagedReceipt, remnants map[string]HostTeardownRemnant, attempts map[string]HostTeardownAttempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stagedReceipts = staged
	s.remnants = withRemnantIDs(remnants)
	s.attempts = attempts
}

// withRemnantIDs stamps each remnant record with the map key it is stored
// under, so a scan can name the record it holds.
func withRemnantIDs(remnants map[string]HostTeardownRemnant) map[string]HostTeardownRemnant {
	if remnants == nil {
		return nil
	}
	out := make(map[string]HostTeardownRemnant, len(remnants))
	for id, remnant := range remnants {
		remnant.remnantID = id
		out[id] = remnant
	}
	return out
}

// remnantSnapshot returns copies of the stored remnant set, keyed by remnantId.
// It takes only the store's own mutex, so the fence read and the retention
// derivation need no mutation lock.
func (s *hostStore) remnantSnapshot() map[string]HostTeardownRemnant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return withRemnantIDs(s.remnants)
}

// stagedSnapshot returns copies of the stored staged-marker set, keyed by host
// name.
func (s *hostStore) stagedSnapshot() map[string]HostStagedReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.stagedReceipts)
}

// attemptsSnapshot returns copies of the stored attempt set, keyed by attempt
// id.
func (s *hostStore) attemptsSnapshot() map[string]HostTeardownAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.attempts)
}

// markedRemnantFor returns the id of the open remnant fencing name, if one
// does: the host-wide fence spec §6 defines. It is the one derivation every
// gate — retention, capacity, boot collision, attach, deploy, restart, plan,
// and the mutations — reads.
func (s *hostStore) markedRemnantFor(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Deterministic: the lexicographically smallest open id, so a state no
	// writer of this build can produce (two open remnants for one name) still
	// answers the same way twice.
	best := ""
	found := false
	for id, remnant := range s.remnants {
		if remnant.Host != name || !remnant.open() {
			continue
		}
		if !found || id < best {
			best, found = id, true
		}
	}
	return best, found
}

// openAttemptFor returns the live attempt fencing a remnant, if one does: "The
// open attempt record is an attempt fence: while one stands, every lifecycle
// path on the name refuses except a later `teardown-retry` naming the same
// remnant" (spec §6).
func (s *hostStore) openAttemptFor(remnantID string) (string, HostTeardownAttempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	best := ""
	var bestAttempt HostTeardownAttempt
	found := false
	for id, attempt := range s.attempts {
		if attempt.RemnantID != remnantID || !attempt.open() {
			continue
		}
		if !found || id < best {
			best, bestAttempt, found = id, attempt, true
		}
	}
	return best, bestAttempt, found
}

// remnantByID returns the remnant one id names, open or resolved. It is the
// only lookup `teardown-retry`/`teardown-recover` do — "lookup never requires a
// current live entry" (spec §6).
func (s *hostStore) remnantByID(remnantID string) (HostTeardownRemnant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	remnant, ok := s.remnants[remnantID]
	if ok {
		remnant.remnantID = remnantID
	}
	return remnant, ok
}

// ---------------------------------------------------------------------------
// fence
// ---------------------------------------------------------------------------

// openRemnantID is the manager's remnant fence: the id of the open remnant
// fencing name, if one does. It is the function cfg.remnantFence is wired to in
// production (newHubHostManager), so every gate S10/S11 shipped — and every new
// one this slice adds — reads the real record set.
func (m *hubHostManager) openRemnantID(name string) (string, bool) {
	if strings.TrimSpace(name) == "" {
		return "", false
	}
	return m.cfg.store.markedRemnantFor(name)
}

// remnantRefusal is §6's typed `remnant-open` conflict refusal: the fence names
// the blocking `remnantId`, never the gate-busy form.
func (m *hubHostManager) remnantRefusal(name string) error {
	remnantID, open := m.openRemnantID(name)
	if !open {
		return nil
	}
	return remnantFenceRefusal(name, remnantID)
}

// ---------------------------------------------------------------------------
// validation
// ---------------------------------------------------------------------------

// validateHostStagedReceipts checks the staged markers a hub.toml document
// carries against the shapes this build writes. Spec §6's reserved namespace
// refuses a value whose shape this build cannot decode loudly before any
// rewrite, so a marker that names no key, no phase, or no pinned target is a
// hard startup error rather than a silently reinterpreted record.
func validateHostStagedReceipts(markers map[string]HostStagedReceipt) error {
	for name, marker := range markers {
		if strings.TrimSpace(name) == "" {
			return errors.New("staged_receipts carries an entry with an empty host name")
		}
		scope, ok := parseHostReceiptScopedKey(marker.Key)
		if !ok {
			return fmt.Errorf("staged_receipts[%q] carries key %q, not a canonical five-part scoped key this build writes", name, marker.Key)
		}
		if scope.Name != name {
			return fmt.Errorf("staged_receipts[%q] names host %q in its key", name, scope.Name)
		}
		switch marker.Phase {
		case hostStagedPhaseStaged, hostStagedPhaseRuntimeSwapped:
		default:
			return fmt.Errorf("staged_receipts[%q] carries phase %q, which this build cannot produce or decode", name, marker.Phase)
		}
		if _, err := time.Parse(time.RFC3339, marker.StagedAt); err != nil {
			return fmt.Errorf("staged_receipts[%q] carries staged_at %q, not an RFC3339 instant: %w", name, marker.StagedAt, err)
		}
		if err := validatePendingTeardown(name, marker.PendingTeardown); err != nil {
			return fmt.Errorf("staged_receipts[%q]: %w", name, err)
		}
		if marker.Provisional.RemnantID == "" {
			return fmt.Errorf("staged_receipts[%q] carries no pre-minted remnantId", name)
		}
		if !validRemnantID(marker.Provisional.RemnantID) {
			return fmt.Errorf("staged_receipts[%q] carries a remnantId of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				name, len(marker.Provisional.RemnantID), MaxRemnantIDBytes)
		}
		if marker.Provisional.Outcome != hostReceiptOutcomeCommitted {
			return fmt.Errorf("staged_receipts[%q] carries provisional outcome %q, which this build cannot produce or decode", name, marker.Provisional.Outcome)
		}
	}
	return nil
}

// validateHostTeardownRemnants checks the remnant records a hub.toml document
// carries: every id is within the bound, the record names a host and a pinned
// identity, the pinned target agrees with the record, and a resolved record
// carries exactly the fields its resolution kind defines.
func validateHostTeardownRemnants(remnants map[string]HostTeardownRemnant) error {
	for id, remnant := range remnants {
		if !validRemnantID(id) {
			return fmt.Errorf("teardown_remnants carries an id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				len(id), MaxRemnantIDBytes)
		}
		if strings.TrimSpace(remnant.Host) == "" {
			return fmt.Errorf("teardown_remnants[%q] names no host", id)
		}
		switch remnant.Kind {
		case hostRemnantKindAdd, hostRemnantKindUpdate, hostRemnantKindRemove:
		default:
			return fmt.Errorf("teardown_remnants[%q] carries kind %q, which this build cannot produce or decode", id, remnant.Kind)
		}
		if remnant.Generation == 0 || !validIncarnationID(remnant.IncarnationID) {
			return fmt.Errorf("teardown_remnants[%q] carries an incomplete pinned identity (%d, %q)", id, remnant.Generation, remnant.IncarnationID)
		}
		if _, err := time.Parse(time.RFC3339, remnant.CommittedAt); err != nil {
			return fmt.Errorf("teardown_remnants[%q] carries committed_at %q, not an RFC3339 instant: %w", id, remnant.CommittedAt, err)
		}
		if err := validatePendingTeardown(id, remnant.PendingTeardown); err != nil {
			return fmt.Errorf("teardown_remnants[%q]: %w", id, err)
		}
		if remnant.PendingTeardown.Name != remnant.Host {
			return fmt.Errorf("teardown_remnants[%q] names host %q and pins a teardown target for %q",
				id, remnant.Host, remnant.PendingTeardown.Name)
		}
		if !remnant.CleanupHandle.resolvable() {
			return fmt.Errorf("teardown_remnants[%q] carries a cleanup handle this build cannot resolve (kind %q)", id, remnant.CleanupHandle.Kind)
		}
		if remnant.MutationKey != "" {
			if _, ok := parseHostReceiptScopedKey(remnant.MutationKey); !ok {
				return fmt.Errorf("teardown_remnants[%q] carries mutation_key %q, not a canonical five-part scoped key this build writes", id, remnant.MutationKey)
			}
		}
		if resolved := remnant.Resolved; resolved != nil {
			if _, err := time.Parse(time.RFC3339, resolved.ClearedAt); err != nil {
				return fmt.Errorf("teardown_remnants[%q] carries cleared_at %q, not an RFC3339 instant: %w", id, resolved.ClearedAt, err)
			}
			switch resolved.ResolutionKind {
			case hostRemnantResolutionRetry:
				if resolved.Attestation != nil {
					return fmt.Errorf("teardown_remnants[%q] carries a retry resolution with an attestation", id)
				}
			case hostRemnantResolutionRecover:
				if resolved.Attestation == nil {
					return fmt.Errorf("teardown_remnants[%q] carries a recover resolution with no attestation", id)
				}
				if err := validateRecoveryAttestationShape(*resolved.Attestation); err != nil {
					return fmt.Errorf("teardown_remnants[%q]: %w", id, err)
				}
			default:
				return fmt.Errorf("teardown_remnants[%q] carries resolution_kind %q, which this build cannot produce or decode", id, resolved.ResolutionKind)
			}
			switch resolved.HostKind {
			case hostRemnantHostKindLive, hostRemnantHostKindRemoved:
			default:
				return fmt.Errorf("teardown_remnants[%q] carries host_kind %q, which this build cannot produce or decode", id, resolved.HostKind)
			}
		}
	}
	return nil
}

// validatePendingTeardown checks one pinned teardown target's shape.
func validatePendingTeardown(owner string, target HostPendingTeardown) error {
	if strings.TrimSpace(target.Name) == "" {
		return errors.New("pinned teardown target names no host")
	}
	switch target.Kind {
	case hostTeardownKindUpdate, hostTeardownKindRemove:
	default:
		return fmt.Errorf("pinned teardown target carries kind %q, which this build cannot produce or decode", target.Kind)
	}
	if target.Generation == 0 || !validIncarnationID(target.IncarnationID) {
		return fmt.Errorf("pinned teardown target carries an incomplete identity (%d, %q)", target.Generation, target.IncarnationID)
	}
	for _, fanOut := range target.FanOuts {
		if strings.TrimSpace(fanOut) == "" {
			return errors.New("pinned teardown target carries an empty fan-out name")
		}
	}
	return nil
}

// validateHostTeardownAttempts checks the attempt records a hub.toml document
// carries.
func validateHostTeardownAttempts(attempts map[string]HostTeardownAttempt, remnants map[string]HostTeardownRemnant) error {
	for id, attempt := range attempts {
		if !validRemnantID(id) {
			return fmt.Errorf("teardown_attempts carries an id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				len(id), MaxRemnantIDBytes)
		}
		if !validRemnantID(attempt.RemnantID) {
			return fmt.Errorf("teardown_attempts[%q] names a remnant id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				id, len(attempt.RemnantID), MaxRemnantIDBytes)
		}
		switch attempt.State {
		case hostAttemptStateOpen, hostAttemptStateFencedClosed:
		default:
			return fmt.Errorf("teardown_attempts[%q] carries state %q, which this build cannot produce or decode", id, attempt.State)
		}
		if _, err := time.Parse(time.RFC3339, attempt.StartedAt); err != nil {
			return fmt.Errorf("teardown_attempts[%q] carries started_at %q, not an RFC3339 instant: %w", id, attempt.StartedAt, err)
		}
		if attempt.TimedOutAt != "" {
			if _, err := time.Parse(time.RFC3339, attempt.TimedOutAt); err != nil {
				return fmt.Errorf("teardown_attempts[%q] carries timed_out_at %q, not an RFC3339 instant: %w", id, attempt.TimedOutAt, err)
			}
		}
		if attempt.FencedAt != "" {
			if _, err := time.Parse(time.RFC3339, attempt.FencedAt); err != nil {
				return fmt.Errorf("teardown_attempts[%q] carries fenced_at %q, not an RFC3339 instant: %w", id, attempt.FencedAt, err)
			}
			if attempt.open() {
				return fmt.Errorf("teardown_attempts[%q] carries a fenced instant on an open attempt", id)
			}
		}
		if _, ok := remnants[attempt.RemnantID]; !ok {
			return fmt.Errorf("teardown_attempts[%q] names remnant %q, which teardown_remnants does not carry", id, attempt.RemnantID)
		}
	}
	return nil
}

// validateRecoveryAttestationShape checks an attestation's stored shape: the
// statement is the one this build accepts and observed_at is an RFC3339
// instant. The operator-vs-session check is the recover handler's, not a stored
// shape's.
func validateRecoveryAttestationShape(attestation HostRecoveryAttestation) error {
	if attestation.Statement != hostRecoveryStatement {
		return fmt.Errorf("attestation statement %q is not %q", attestation.Statement, hostRecoveryStatement)
	}
	if strings.TrimSpace(attestation.Operator) == "" {
		return errors.New("attestation names no operator")
	}
	if _, err := time.Parse(time.RFC3339, attestation.ObservedAt); err != nil {
		return fmt.Errorf("attestation observed_at %q is not an RFC3339 instant: %w", attestation.ObservedAt, err)
	}
	return nil
}

// validRemnantID reports whether id is a shape this build accepts for a
// remnant, attempt, or finalizing token: non-empty, valid UTF-8, within spec
// §6's 128-byte bound.
func validRemnantID(id string) bool {
	return id != "" && len(id) <= MaxRemnantIDBytes && utf8.ValidString(id)
}

// ---------------------------------------------------------------------------
// ordering helpers
// ---------------------------------------------------------------------------
