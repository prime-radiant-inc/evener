// Package hostfence owns crash-fencing spec 08c's fencing-epoch model and the
// remote `evener-fence` lease wrapper contract — the pieces a fenced mutation
// stands on.
//
// What this package owns, cited to spec 08c:
//
//   - The canonical fencing epoch: the (controller boot id, per-host monotonic
//     op sequence) pair of §1 and §4, its shape (which hostops keeps verbatim
//     because "its shape is defined in the crash-fencing spec", deploy-pipeline
//     08b §10), and its within-boot order. §4: "That sequence is the total
//     order across controller restarts, never the (boot id, per-host sequence)
//     pair alone, which has no defined cross-restart order" — so the pair
//     orders only within one boot id, and the guard file's sequence is the
//     cross-restart order.
//   - The guard-file state and its compare-and-swap rules (§4): the totally
//     ordered fencing sequence, the current guard epoch, the fence state a
//     takeover records (fencing epoch, superseded epoch, monotonic fence
//     sequence), and the advance rule "an older epoch never overwrites a newer
//     one".
//   - The remote lease file's entry shape: command, registration time, and the
//     ownership identity §4 and §9 require (the remote PID plus its start time,
//     or the wrapper's per-spawn nonce, or a cgroup membership).
//   - The §6 helper gate: the pinned helper name, version, and install path;
//     the read-only presence/version verification whose absence or distrust
//     refuses fail-closed before any remote mutation.
//   - The wrapper's command protocol and the decode validation of its
//     responses, which are a trust boundary: a response outside the schema the
//     helper emits is refused, never half-understood.
//
// Beyond that layer this package owns the fencing worker's sequence — the
// takeover decision, the bounded kill/wait, and the guard advance (takeover.go
// and kill.go) — and the fencing-failure outcome carrying the `remote-fencing`
// boundary the quarantine record persists (the per-host marker write itself
// lives in hostops). What it deliberately does not own: the local-reap
// enumeration (S19), the `orphan-resolve` handler and the `BoundaryEntry` union
// (S20), and bootstrap delivery with `helperInstalled` (S21). They consume the
// types and decoders here; the guard rules below are the same rules the helper
// enforces remotely, restated controller-side so a caller can decide (and
// verify what the helper reports) without trusting a remote string.
package hostfence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// MaxBootIDBytes bounds a controller boot id this layer accepts. The epoch is
// presented as an argument on a remote command line, so the value is held to a
// shell-token-safe character set below; the bound keeps a malformed or
// malicious id from inflating a command or the persisted state.
const MaxBootIDBytes = 128

// ErrInvalidEpoch reports a fencing epoch outside the schema this layer
// presents: an absent or oversized boot id, a boot id outside the
// shell-token-safe set, or a zero op sequence.
var ErrInvalidEpoch = errors.New("hostfence: invalid fencing epoch")

// ErrInvalidGuard reports a guard-file state outside the schema the helper
// writes: a wrong version, a fence whose epoch is absent, a malformed
// ownership shape, and so on.
var ErrInvalidGuard = errors.New("hostfence: invalid guard state")

// Epoch is the canonical fencing epoch: crash-fencing §1's "controller boot id
// plus per-host monotonic op sequence", presented on every SSH command a worker
// runs and persisted in the operation-store file alongside its record before
// the worker launches (§4). The JSON shape is the wire shape the deploy
// pipeline pinned (`{bootId, opSeq}`, 08b §10) and the shape of the record's
// `fencingEpoch` raw field.
type Epoch struct {
	BootID string `json:"bootId"`
	OpSeq  uint64 `json:"opSeq"`
}

// IsZero reports whether e carries no epoch at all. A zero epoch is never
// presented and never authorizes anything: §6's pre-fence verification refuses
// an absent epoch rather than defaulting one.
func (e Epoch) IsZero() bool { return e.BootID == "" && e.OpSeq == 0 }

// Validate checks e against the schema every presenter of an epoch produces.
// The boot id is held to a shell-token-safe character set because the epoch is
// presented as an argument on a remote command line: a value outside the set is
// refused fail-closed rather than relied on shell quoting to contain it, and
// the helper refuses the same values server-side.
func (e Epoch) Validate() error {
	if err := validateBootID(e.BootID); err != nil {
		return err
	}
	if e.OpSeq == 0 {
		return fmt.Errorf("%w: the epoch presents no op sequence", ErrInvalidEpoch)
	}
	return nil
}

// validateBootID checks one controller boot id. "-" is refused: the helper's
// state files use it as the absent-field sentinel, so admitting it as a boot id
// would let an epoch serialize as absent and never settle.
func validateBootID(bootID string) error {
	switch {
	case bootID == "":
		return fmt.Errorf("%w: the epoch presents no boot id", ErrInvalidEpoch)
	case bootID == "-":
		return fmt.Errorf("%w: %q is the state files' absence sentinel, not a boot id", ErrInvalidEpoch, bootID)
	case len(bootID) > MaxBootIDBytes:
		return fmt.Errorf("%w: the boot id is %d bytes, over the %d-byte bound", ErrInvalidEpoch, len(bootID), MaxBootIDBytes)
	case !utf8.ValidString(bootID):
		return fmt.Errorf("%w: the boot id is not valid UTF-8", ErrInvalidEpoch)
	case !shellTokenSafe(bootID):
		return fmt.Errorf("%w: the boot id %q is outside the shell-token-safe set [A-Za-z0-9._-]", ErrInvalidEpoch, bootID)
	}
	return nil
}

// shellTokenSafe reports whether s consists only of characters that survive a
// remote command line as one word without quoting.
func shellTokenSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// Order is the result of comparing two epochs minted under one boot id.
type Order int

const (
	// OrderOlder reports that the left epoch precedes the right one.
	OrderOlder Order = -1
	// OrderEqual reports that the two epochs are the same epoch.
	OrderEqual Order = 0
	// OrderNewer reports that the left epoch follows the right one.
	OrderNewer Order = 1
)

// Compare orders two epochs within one controller boot. ok is false when the
// boot ids differ: §4 gives the pair alone "no defined cross-restart order", so
// this layer refuses to invent one — the remote guard file's monotonic sequence
// is that order, and a caller comparing across boots must read the guard state,
// never this function's absence of an answer.
func (e Epoch) Compare(other Epoch) (Order, bool) {
	if e.BootID != other.BootID {
		return 0, false
	}
	switch {
	case e.OpSeq < other.OpSeq:
		return OrderOlder, true
	case e.OpSeq > other.OpSeq:
		return OrderNewer, true
	default:
		return OrderEqual, true
	}
}

// EpochFromRecord returns the fencing epoch a store record carries. It is the
// binding between the fencing layer and the operation store's records: hostops
// persists the epoch verbatim (hostops.Record carries the crash-fencing shape
// and never restates it), and this is the typed read of it.
//
// ok is false for a record this layer's schema does not admit as carrying an
// epoch — an absent or malformed field, a zero op sequence, an unknown key —
// which a caller must treat as "no fencible epoch", never as a zero one.
func EpochFromRecord(record hostops.Record) (Epoch, bool) {
	return EpochFromRaw(record.FencingEpoch)
}

// EpochFromRaw decodes the record's `fencingEpoch` raw field into the canonical
// epoch. The raw field's interior is the crash-fencing spec's schema, so this
// decode is strict like the store's own tables: a value outside it is refused
// rather than half-read.
//
// Deliberate narrowing: hostops keeps the raw field verbatim and its own
// Record.FencingEpochValue admits any object carrying bootId/opSeq (the store's
// retention tests persist an extra `pad` key), while the fencing layer refuses
// any key beyond the two the spec pins. That asymmetry is intentional — a
// record whose epoch carries fields this layer does not understand is not a
// fencible epoch, and acting on it would present a shape the spec never
// defined — and TestEpochFromRawNarrowsHostopsShape pins both readings.
func EpochFromRaw(raw json.RawMessage) (Epoch, bool) {
	if len(raw) == 0 {
		return Epoch{}, false
	}
	var epoch Epoch
	if err := decodeStrict(raw, &epoch); err != nil {
		return Epoch{}, false
	}
	if err := epoch.Validate(); err != nil {
		return Epoch{}, false
	}
	return epoch, true
}

// FenceState is the fence record a preemptive fence-takeover writes in the same
// atomic step that installs the new lease holder (§4): the fencing epoch, the
// superseded epoch, and the monotonic fence sequence. It persists until the
// guard advance lands, so no window exists where the lease is released but the
// guard has not advanced.
type FenceState struct {
	Epoch      Epoch  `json:"epoch"`
	Superseded *Epoch `json:"superseded"`
	// GuardEpoch is the guard file's monotonic sequence value at the takeover.
	// The next successful advance lands strictly above it.
	GuardEpoch uint64 `json:"guardEpoch"`
}

// GuardState is the guard file's decoded state: crash-fencing §1's "remote-side
// epoch file holding the totally ordered fencing sequence for one host". The
// helper writes it; this layer decodes and validates it and states the
// controller-side rules the same state obeys.
type GuardState struct {
	Version int `json:"version"`
	// GuardEpoch is the totally ordered fencing sequence (§4: it "advances only
	// by remote compare-and-swap"; it is the total order across controller
	// restarts). It is also the number a `remote-fencing` boundary carries as
	// its `guardEpoch` (§9).
	GuardEpoch uint64 `json:"guardEpoch"`
	// Epoch is the epoch the guard currently names, absent before the first
	// advance.
	Epoch *Epoch `json:"epoch"`
	// Fence is the pending takeover's fence record, absent when the guard is
	// settled.
	Fence *FenceState `json:"fence"`
	// Superseded is the most recently superseded epoch, retained so an epoch the
	// guard already superseded can never take over again.
	Superseded *Epoch `json:"superseded"`
	// Holder is the epoch holding the exclusive per-host remote lease (§4: the
	// takeover "installs the new epoch as the lease holder").
	Holder *Epoch `json:"holder"`
	// BootHighWater is the durable per-boot high-water the guard keeps: the
	// highest op sequence it has ever admitted from each controller boot id.
	// Within one boot the pair is ordered, and remembering the high-water makes
	// that order durable across boots, so an epoch the guard already admitted
	// can never take over again even after later boots have settled. Cross-boot
	// order stays the guard file's monotonic sequence (§4), never this map.
	BootHighWater map[string]uint64 `json:"bootHighWater"`
}

// Validate checks one decoded guard state against the schema the helper writes.
// A state outside it is refused, never used to decide whether a mutation may
// run.
func (g GuardState) Validate() error {
	if g.Version != ProtocolVersion {
		return fmt.Errorf("%w: guard version %d, want %d", ErrInvalidGuard, g.Version, ProtocolVersion)
	}
	for _, epoch := range []*Epoch{g.Epoch, g.Superseded, g.Holder} {
		if epoch != nil {
			if err := epoch.Validate(); err != nil {
				return err
			}
		}
	}
	for bootID, highWater := range g.BootHighWater {
		if err := validateBootID(bootID); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidGuard, err)
		}
		if highWater == 0 {
			return fmt.Errorf("%w: boot %q carries a zero high-water", ErrInvalidGuard, bootID)
		}
	}
	if g.Fence == nil {
		return nil
	}
	if err := g.Fence.Epoch.Validate(); err != nil {
		return err
	}
	if g.Fence.Superseded != nil {
		if err := g.Fence.Superseded.Validate(); err != nil {
			return err
		}
	}
	if g.Fence.GuardEpoch == 0 || g.Fence.GuardEpoch > g.GuardEpoch {
		return fmt.Errorf("%w: fence sequence %d outside the guard's %d", ErrInvalidGuard, g.Fence.GuardEpoch, g.GuardEpoch)
	}
	return nil
}

// Admits reports whether a mutating step presenting e may run against this
// guard state: §4's check — "A step whose presented epoch no longer equals the
// guard refuses server-side" — requires the guard to name e and no fence to be
// pending. The helper enforces this remotely; a caller uses it to decide what
// the next legal step is and to verify what the helper reports.
func (g GuardState) Admits(e Epoch) bool {
	return g.Fence == nil && g.Epoch != nil && *g.Epoch == e
}

// AdvancedPast reports whether the guard has advanced to e: the fence is
// cleared and the guard names e. §4's rule that "a fresh operation starts only
// after local reap completion under a new epoch with the guard advanced past
// kill/wait of the superseded epoch" (cited again in §5 and §7) is this
// predicate plus the caller's reap state; see FreshOperationPermitted.
func (g GuardState) AdvancedPast(e Epoch) bool { return g.Admits(e) }

// FreshOperationPermitted is §5's fresh-operation rule as one predicate: "A
// fresh operation starts only after local reap completion under a new epoch
// with the guard advanced past kill/wait of the superseded epoch." reapComplete
// is S19's local-reap outcome; the guard state is what this package owns.
func FreshOperationPermitted(reapComplete bool, g GuardState, e Epoch) bool {
	return reapComplete && g.AdvancedPast(e)
}

// Takeover applies §4's preemptive fence-takeover rules to g: the guard file's
// sequence advances by compare-and-swap, the new epoch is installed as the
// lease holder, the fence state records the fencing epoch, the superseded
// epoch, and the monotonic fence sequence, and an epoch the guard already
// superseded — or one no newer than the current holder within its own boot —
// never takes over. A replay of the current fence, or of an already-advanced
// epoch, is idempotent.
//
// A pending fence for another epoch does not block the takeover: the new epoch
// supersedes the epoch that fence names. That is the crashed incarnation whose
// work the new worker kills under the new lease — §4:107's "The next
// `deploy`/`restart` past the cleared marker runs its kill/wait plus guard
// advance under a fresh epoch" would be impossible otherwise, because a
// kill/wait timeout leaves exactly that pending fence behind. The superseded,
// same-boot and high-water checks below still refuse an epoch the guard has
// already retired, so a late orphan can never move the guard backward.
//
// This is the same rule the helper enforces in the guard file; a caller uses it
// to choose the next step and to verify a reported state, never as a substitute
// for the helper's server-side check.
func (g GuardState) Takeover(e Epoch) (GuardState, error) {
	if err := e.Validate(); err != nil {
		return GuardState{}, err
	}
	if g.Superseded != nil && *g.Superseded == e {
		// An epoch the guard already superseded is stale whatever else is
		// pending: it must never be reinstalled, so this refuses before the
		// takeover below can supersede anything.
		return GuardState{}, fmt.Errorf("%w: epoch %+v was superseded", ErrStaleEpoch, e)
	}
	if g.Fence != nil && g.Fence.Epoch == e {
		// A replay of this epoch's own pending fence is idempotent.
		return g, nil
	}
	if g.Epoch != nil && *g.Epoch == e {
		// Already settled on this epoch: the takeover happened and the advance
		// landed, so a retry writes nothing.
		return g, nil
	}
	previous := g.Holder
	if previous == nil {
		previous = g.Epoch
	}
	if previous != nil && previous.BootID == e.BootID && e.OpSeq <= previous.OpSeq {
		return GuardState{}, fmt.Errorf("%w: epoch %+v is no newer than the guard's holder %+v", ErrStaleEpoch, e, *previous)
	}
	if highWater, seen := g.BootHighWater[e.BootID]; seen && e.OpSeq <= highWater {
		// The durable per-boot high-water: an epoch this boot already admitted
		// never takes over again, even after later boots have settled.
		return GuardState{}, fmt.Errorf("%w: epoch %+v is at or below boot %q's high-water %d", ErrStaleEpoch, e, e.BootID, highWater)
	}
	next := g
	next.GuardEpoch++
	if previous != nil {
		superseded := *previous
		next.Superseded = &superseded
		next.Fence = &FenceState{Epoch: e, Superseded: &superseded, GuardEpoch: next.GuardEpoch}
	} else {
		next.Superseded = nil
		next.Fence = &FenceState{Epoch: e, GuardEpoch: next.GuardEpoch}
	}
	holder := e
	next.Holder = &holder
	next.BootHighWater = maps.Clone(g.BootHighWater)
	if next.BootHighWater == nil {
		next.BootHighWater = map[string]uint64{}
	}
	next.BootHighWater[e.BootID] = e.OpSeq
	return next, nil
}

// Advance applies §4's compare-and-advance rules to g: only the epoch a pending
// fence names may advance, the guard's sequence advances by that one
// compare-and-swap, the fence clears, and "an older epoch never overwrites a
// newer one" — so a late orphan cannot move the guard backward. A replay of the
// settled epoch is idempotent; anything else refuses stale.
func (g GuardState) Advance(e Epoch) (GuardState, error) {
	if err := e.Validate(); err != nil {
		return GuardState{}, err
	}
	if g.Fence == nil {
		if g.Epoch != nil && *g.Epoch == e {
			return g, nil
		}
		return GuardState{}, fmt.Errorf("%w: epoch %+v is not the guard's %+v", ErrStaleEpoch, e, g.Epoch)
	}
	if g.Fence.Epoch != e {
		return GuardState{}, fmt.Errorf("%w: the fence names %+v, not %+v", ErrStaleEpoch, g.Fence.Epoch, e)
	}
	next := g
	advanced := e
	next.Epoch = &advanced
	next.GuardEpoch = g.GuardEpoch + 1
	next.Superseded = g.Fence.Superseded
	next.Fence = nil
	return next, nil
}

// ProtocolVersion is the version of the helper protocol (the guard/lease state
// schema and the response shapes) this package speaks. It is not the helper's
// own version constant; the helper reports that separately, and §6 pins it in
// HelperVersion.
const ProtocolVersion = 1

// OwnershipKind discriminates the ownership identity a lease entry carries
// (§4, §9): the remote PID plus its start time, the wrapper's per-spawn nonce,
// or a remote cgroup membership.
type OwnershipKind string

const (
	// OwnershipPID is the remote PID plus its kernel-owned start time.
	OwnershipPID OwnershipKind = "pid"
	// OwnershipNonce is the per-spawn nonce the wrapper mints at registration.
	OwnershipNonce OwnershipKind = "nonce"
	// OwnershipCgroup is a remote cgroup/job-object membership.
	OwnershipCgroup OwnershipKind = "cgroup"
)

// Ownership is one lease entry's required ownership identity. Exactly one
// variant is present: a reused PID never kills unrelated work (§4), and a
// persisted entry without its identity fails closed — no kill, no clear (§9).
type Ownership struct {
	// PID and PIDStartTime carry the PID variant. PIDStartTime is the
	// kernel-owned start-time token compared before any signal.
	PID          *int
	PIDStartTime string
	// Nonce carries the nonce variant the wrapper mints at registration.
	Nonce string
	// CgroupID carries the cgroup variant.
	CgroupID string
}

// Kind reports which ownership variant this value carries, or "" when it
// carries none or more than one (which Validate refuses).
func (o Ownership) Kind() OwnershipKind {
	switch {
	case o.PID != nil && o.PIDStartTime != "":
		if o.Nonce != "" || o.CgroupID != "" {
			return ""
		}
		return OwnershipPID
	case o.Nonce != "" && o.PID == nil && o.PIDStartTime == "" && o.CgroupID == "":
		return OwnershipNonce
	case o.CgroupID != "" && o.PID == nil && o.PIDStartTime == "" && o.Nonce == "":
		return OwnershipCgroup
	default:
		return ""
	}
}

// Validate checks the one-variant rule and each variant's own fields.
func (o Ownership) Validate() error {
	switch kind := o.Kind(); kind {
	case OwnershipPID:
		if *o.PID <= 0 {
			return fmt.Errorf("%w: the pid ownership carries pid %d", ErrInvalidGuard, *o.PID)
		}
		return nil
	case OwnershipNonce:
		return nil
	case OwnershipCgroup:
		return nil
	default:
		return fmt.Errorf("%w: a lease entry's ownership carries no single identity", ErrInvalidGuard)
	}
}

// ownershipJSON is the wire shape of the three ownership variants.
type ownershipJSON struct {
	PID          *int   `json:"pid,omitempty"`
	PIDStartTime string `json:"pidStartTime,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
	CgroupID     string `json:"cgroupId,omitempty"`
}

// MarshalJSON emits exactly one variant, in §9's field spelling.
func (o Ownership) MarshalJSON() ([]byte, error) {
	wire := ownershipJSON(o)
	switch o.Kind() {
	case OwnershipPID:
		wire.PIDStartTime = o.PIDStartTime
	case OwnershipNonce:
		wire.PID, wire.PIDStartTime, wire.CgroupID = nil, "", ""
	case OwnershipCgroup:
		wire.PID, wire.PIDStartTime, wire.Nonce = nil, "", ""
	default:
		return nil, fmt.Errorf("%w: ownership carries no single identity", ErrInvalidGuard)
	}
	return json.Marshal(wire)
}

// UnmarshalJSON decodes one ownership variant. A value carrying several or
// none is refused: the verifier needs exactly one identity to check.
func (o *Ownership) UnmarshalJSON(raw []byte) error {
	var wire ownershipJSON
	// The ownership object is the deepest object this package decodes, so the
	// strict decode is applied here too: a key the helper never writes is a
	// value this layer must not reinterpret.
	if err := decodeStrict(raw, &wire); err != nil {
		return fmt.Errorf("%w: ownership: %w", ErrInvalidGuard, err)
	}
	next := Ownership(wire)
	if err := next.Validate(); err != nil {
		return err
	}
	*o = next
	return nil
}

// LeaseEntry is one registered command in the per-host remote lease file (§4):
// the command, its registration time, and its ownership identity. The wrapper
// writes the entry before the command's side effects start and closes it with
// the exit status; the fencing verifier enumerates entries and checks each
// against its stored identity before any kill or clear (§9).
type LeaseEntry struct {
	// ID is the entry's identity in the lease file: the wrapper's per-spawn
	// nonce. It is what a verifier re-presents to the wrapper.
	ID string `json:"id"`
	// Command is the command text the wrapper ran.
	Command string `json:"command"`
	// RegisteredAt is the RFC3339 registration time, before the side effects.
	RegisteredAt string `json:"registeredAt"`
	// Ownership is the entry's required ownership identity.
	Ownership Ownership `json:"ownership"`
	// State is the entry's lifecycle in the lease file.
	State string `json:"state"`
	// Exit is the command's exit status, present once it exited or was killed.
	Exit *int `json:"exit,omitempty"`
	// ExitedAt is the RFC3339 exit time, present once it exited or was killed.
	ExitedAt string `json:"exitedAt,omitempty"`
	// Descendants are the surviving children of the command: work the wrapper's
	// own child left behind after it exited. While any of them still matches its
	// recorded identity the entry stays in a live state — a command's children
	// are never clean just because the immediate child exited (§9's mirror of
	// §3) — and a fencing kill revalidates the same identity before signaling.
	Descendants []Descendant `json:"descendants,omitempty"`
}

// Descendant is one surviving child of a wrapped command: the PID the wrapper
// observed carrying its per-spawn nonce, plus the kernel-owned start token it
// observed beside it. Both are revalidated — the exact nonce in the process's
// environment and the start token — before the process is treated as owned
// work or signaled, so a reused PID is never mistaken for the command's child.
type Descendant struct {
	// PID is the surviving process's process id.
	PID int `json:"pid"`
	// StartToken is the kernel-owned start token observed for it, or "unknown"
	// when the platform would not report one (which fails closed: the process
	// reads live and is never signaled).
	StartToken string `json:"startToken"`
}

// Validate checks one descendant record.
func (d Descendant) Validate() error {
	if d.PID < 1 {
		return fmt.Errorf("%w: a descendant carries pid %d", ErrInvalidGuard, d.PID)
	}
	if strings.TrimSpace(d.StartToken) == "" {
		return fmt.Errorf("%w: descendant %d carries no start token", ErrInvalidGuard, d.PID)
	}
	return nil
}

// Lease entry states.
const (
	// LeaseRegistering is the state written before the command is spawned.
	LeaseRegistering = "registering"
	// LeaseRunning is the state written once the spawned command's identity is
	// recorded.
	LeaseRunning = "running"
	// LeaseExited is the state of a command that ran to completion.
	LeaseExited = "exited"
	// LeaseKilled is the state of a command a fencing kill ended.
	LeaseKilled = "killed"
)

// Validate checks one lease entry against the schema the wrapper writes.
func (e LeaseEntry) Validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("%w: a lease entry carries no id", ErrInvalidGuard)
	case e.Command == "":
		return fmt.Errorf("%w: lease entry %q carries no command", ErrInvalidGuard, e.ID)
	case e.RegisteredAt == "":
		return fmt.Errorf("%w: lease entry %q carries no registration time", ErrInvalidGuard, e.ID)
	}
	if err := e.Ownership.Validate(); err != nil {
		return err
	}
	switch e.State {
	case LeaseRegistering, LeaseRunning:
		if e.Exit != nil || e.ExitedAt != "" {
			return fmt.Errorf("%w: lease entry %q carries an exit in state %q", ErrInvalidGuard, e.ID, e.State)
		}
	case LeaseExited, LeaseKilled:
		if e.Exit == nil {
			return fmt.Errorf("%w: lease entry %q carries no exit status in state %q", ErrInvalidGuard, e.ID, e.State)
		}
		if len(e.Descendants) > 0 {
			return fmt.Errorf("%w: settled lease entry %q carries live descendants", ErrInvalidGuard, e.ID)
		}
	default:
		return fmt.Errorf("%w: lease entry %q carries state %q", ErrInvalidGuard, e.ID, e.State)
	}
	for _, descendant := range e.Descendants {
		if err := descendant.Validate(); err != nil {
			return fmt.Errorf("%w: lease entry %q: %w", ErrInvalidGuard, e.ID, err)
		}
	}
	return nil
}

// LeaseRef is §9's three-field projection of a lease entry: the shape a
// `remote-fencing` boundary carries for every entry of the superseded epoch.
type LeaseRef struct {
	Command      string    `json:"command"`
	RegisteredAt string    `json:"registeredAt"`
	Ownership    Ownership `json:"ownership"`
}

// BoundaryRef projects the entry into the shape the remote-fencing boundary
// carries (§9). The entry's own id and state stay controller-side: the boundary
// holds the identity the verifier needs, never the lease file's bookkeeping.
func (e LeaseEntry) BoundaryRef() LeaseRef {
	return LeaseRef{Command: e.Command, RegisteredAt: e.RegisteredAt, Ownership: e.Ownership}
}

// Status is the guard file's state plus the lease file's live-entry count: what
// the helper's status/takeover/advance operations report.
type Status struct {
	Version    int         `json:"version"`
	GuardEpoch uint64      `json:"guardEpoch"`
	Epoch      *Epoch      `json:"epoch"`
	Fence      *FenceState `json:"fence"`
	Superseded *Epoch      `json:"superseded"`
	Holder     *Epoch      `json:"holder"`
	// Entries is how many lease entries the state holds.
	Entries int `json:"entries"`
	// BootHighWater is the guard's durable per-boot high-water map.
	BootHighWater map[string]uint64 `json:"bootHighWater"`
}

// Guard returns the guard half of the report as the state the guard rules
// above decide against.
func (s Status) Guard() GuardState {
	return GuardState{
		Version: s.Version, GuardEpoch: s.GuardEpoch, Epoch: s.Epoch,
		Fence: s.Fence, Superseded: s.Superseded, Holder: s.Holder,
		BootHighWater: s.BootHighWater,
	}
}

// Recheck is the wrapper's answer to a nonce re-presentation: whether the lease
// entry carrying that identity still names a live holder. §9: "a nonce
// re-presented to the lease wrapper", and an entry whose ownership no longer
// matches reads as already clean for that member only after the remote confirms
// no matching live holder, never by controller-side inference.
type Recheck struct {
	Version int `json:"version"`
	// ID is the entry id (the wrapper's per-spawn nonce) re-presented.
	ID string `json:"id"`
	// Live reports whether the entry still names a live holder: a running
	// command, or a registered entry whose liveness cannot be disproven (which
	// fails closed).
	Live bool `json:"live"`
	// State is the entry's lease state, empty when no entry matched.
	State string `json:"state"`
	// Ownership is the stored identity the answer was checked against.
	Ownership Ownership `json:"ownership"`
	// Descendants are the command's surviving children the answer was checked
	// against, each revalidated by its exact nonce and start token.
	Descendants []Descendant `json:"descendants,omitempty"`
}

// DecodeStatus decodes the helper's guard/lease report. The helper's output is
// a trust boundary, so this is strict: a wrong version, an unknown key, a
// duplicated key, trailing bytes, or a value outside the state schema is
// refused — never half-understood into an authorization.
func DecodeStatus(raw []byte) (Status, error) {
	var status Status
	if err := decodeStrict(raw, &status); err != nil {
		return Status{}, err
	}
	// Every field the helper emits is required: a response that omits one has
	// not been understood, so it is refused rather than read as a zero value
	// (an omitted guard epoch or entry count must never read as "no fence" or
	// "no live leases").
	if err := requireFields(raw, []string{
		"version", "guardEpoch", "epoch", "fence", "superseded", "holder", "entries", "bootHighWater",
	}, map[string]bool{"epoch": true, "fence": true, "superseded": true, "holder": true}); err != nil {
		return Status{}, err
	}
	if status.Version != ProtocolVersion {
		return Status{}, fmt.Errorf("%w: version %d, want %d", ErrInvalidGuard, status.Version, ProtocolVersion)
	}
	if err := status.Guard().Validate(); err != nil {
		return Status{}, err
	}
	if status.Entries < 0 {
		return Status{}, fmt.Errorf("%w: negative entry count %d", ErrInvalidGuard, status.Entries)
	}
	return status, nil
}

// entriesEnvelope is the helper's `entries` response.
type entriesEnvelope struct {
	Version int          `json:"version"`
	Entries []LeaseEntry `json:"entries"`
}

// DecodeEntries decodes the helper's lease-entry enumeration, validating every
// entry: an entry without its ownership identity is refused here exactly as the
// verifier refuses it before a kill or a clear.
func DecodeEntries(raw []byte) ([]LeaseEntry, error) {
	var envelope entriesEnvelope
	if err := decodeStrict(raw, &envelope); err != nil {
		return nil, err
	}
	if err := requireFields(raw, []string{"version", "entries"}, nil); err != nil {
		return nil, err
	}
	if envelope.Version != ProtocolVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrInvalidGuard, envelope.Version, ProtocolVersion)
	}
	for _, entry := range envelope.Entries {
		if err := entry.Validate(); err != nil {
			return nil, err
		}
	}
	return envelope.Entries, nil
}

// DecodeRecheck decodes one nonce re-presentation's answer.
func DecodeRecheck(raw []byte) (Recheck, error) {
	var recheck Recheck
	if err := decodeStrict(raw, &recheck); err != nil {
		return Recheck{}, err
	}
	if err := requireFields(raw, []string{"version", "id", "live", "state", "ownership"}, nil); err != nil {
		return Recheck{}, err
	}
	if recheck.Version != ProtocolVersion {
		return Recheck{}, fmt.Errorf("%w: version %d, want %d", ErrInvalidGuard, recheck.Version, ProtocolVersion)
	}
	if recheck.ID == "" {
		return Recheck{}, fmt.Errorf("%w: a recheck answer carries no id", ErrInvalidGuard)
	}
	switch recheck.State {
	case "":
		// No entry matched the presented identity: the answer is the not-live
		// one, and there is no stored state to validate.
	case LeaseRegistering, LeaseRunning:
		// A running entry may report not-live: that is the reused-or-dead
		// instance reading clean for that member. Live is the claim that must
		// match the state.
	case LeaseExited, LeaseKilled:
		if recheck.Live {
			return Recheck{}, fmt.Errorf("%w: a recheck answer reports a %s entry live", ErrInvalidGuard, recheck.State)
		}
	default:
		return Recheck{}, fmt.Errorf("%w: a recheck answer carries state %q", ErrInvalidGuard, recheck.State)
	}
	// A live answer requires a live state: "live" is the enumeration's clean
	// verdict, so it must never be claimed for a settled entry.
	if recheck.Live && recheck.State == "" {
		return Recheck{}, fmt.Errorf("%w: a recheck answer reports no entry live", ErrInvalidGuard)
	}
	for _, descendant := range recheck.Descendants {
		if err := descendant.Validate(); err != nil {
			return Recheck{}, err
		}
	}
	// A not-live answer may still carry the descendants it checked: each was
	// verified gone, and the record is the evidence of what was checked. What
	// must never happen is a *settled* entry carrying live descendants, which
	// LeaseEntry.Validate refuses.
	if err := recheck.Ownership.Validate(); err != nil {
		return Recheck{}, err
	}
	return recheck, nil
}

// requireFields refuses a decoded object that omits one of the keys the helper
// always emits. nullable names the keys whose value may be the JSON literal
// null (an explicit absence the helper writes); every other named key must be
// present and non-null. Strict decoding alone cannot catch this: an omitted
// field decodes to the zero value, which a verifier could otherwise read as the
// benign case.
func requireFields(raw []byte, required []string, nullable map[string]bool) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGuard, err)
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok {
			return fmt.Errorf("%w: the object carries no %q field", ErrInvalidGuard, key)
		}
		if !nullable[key] && jsonIsNull(value) {
			return fmt.Errorf("%w: the %q field is null", ErrInvalidGuard, key)
		}
	}
	return nil
}

// jsonIsNull reports whether a raw value is the JSON literal null.
func jsonIsNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// decodeStrict decodes one JSON value with the store's decode discipline: no
// unknown keys, no duplicated keys, no trailing bytes. The helper's responses
// cross a trust boundary, so a value this layer cannot understand is refused
// rather than silently reduced by the decoder.
func decodeStrict(raw []byte, out any) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing bytes after the JSON value")
	}
	return nil
}

// rejectDuplicateKeys walks the JSON token stream and refuses an object that
// names one key twice: the decoder silently keeps the last occurrence, so a
// value carrying two identities would otherwise be read as the reduced one.
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	return scanValue(decoder)
}

// scanValue recursively checks one value for duplicated object keys.
func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("the object names the key %q twice", key)
			}
			seen[key] = struct{}{}
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return nil
	}
}
