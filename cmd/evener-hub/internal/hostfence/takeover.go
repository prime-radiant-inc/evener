package hostfence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// The controller-side fencing sequence of crash-fencing §4: observe the
// guard/lease state, take over the lease for the new operation's epoch,
// kill/wait the superseded epoch's lease-tracked work under two bounded
// contexts, and only then advance the guard. A kill/wait timeout is §4's
// fencing-failure outcome: terminal for the operation's result, while the
// record's state is orphan-unverified carrying the timed-out epoch's
// remote-fencing boundary, and the per-host fencing-quarantine marker lands in
// the same atomic write — "never an operable-but-unfenced host" (§10:189).

// DiscriminatorFencingFailure is §8's typed refusal class: the fencing-timeout
// outcome the operation reports, and the form the quarantined host's later
// calls refuse with. It is never a generic transport error.
const DiscriminatorFencingFailure = "fencing-failure"

// ErrFencingFailure is the sentinel of the fencing-failure class: a fencing
// step that could not be completed within its bound, so the operation's result
// is terminal-failed while its record stays orphan-unverified.
var ErrFencingFailure = errors.New("hostfence: the fencing step failed")

// ErrFenceDisagreement reports a helper-reported fencing state the controller's
// own guard rules do not produce: the worker's records must agree with the
// helper's guard (§4's fence state), and a state the two read differently is
// never acted on.
var ErrFenceDisagreement = errors.New("hostfence: the helper's fence state disagrees with the controller's rules")

// FencingDeadlines is §4's deadline family: "the kill runs under its own
// bounded context (owner-set fencing-kill deadline; the default ships in the
// implementing PR) and the exit wait under a second bounded context of the same
// family." The zero value takes the shipped defaults.
type FencingDeadlines struct {
	// Kill bounds the signaling pass: every verified kill the worker issues.
	Kill time.Duration
	// Wait bounds the exit confirmation after the kills.
	Wait time.Duration
}

// The shipped deadline defaults: generous enough for a slow remote round trip,
// bounded enough that "A stuck remote must neither hold the gate forever nor
// overlap a possibly-live orphan" (§4:107).
const (
	DefaultFencingKillDeadline = 30 * time.Second
	DefaultFencingWaitDeadline = 60 * time.Second
	// fencingPollInterval is the wait's recheck cadence.
	fencingPollInterval = 100 * time.Millisecond
)

// withDefaults fills a zero deadline with the shipped default.
func (d FencingDeadlines) withDefaults() FencingDeadlines {
	if d.Kill <= 0 {
		d.Kill = DefaultFencingKillDeadline
	}
	if d.Wait <= 0 {
		d.Wait = DefaultFencingWaitDeadline
	}
	return d
}

// RemoteFencingBoundaryKind is §9's discriminator for the fencing-quarantine
// boundary variant.
const RemoteFencingBoundaryKind = "remote-fencing"

// RemoteFencingBoundary is §9's `remote-fencing` BoundaryEntry: "the timed-out
// operation's fencing epoch plus the guard-file epoch plus the superseded
// epoch's lease-tracked entries". The persisted variant reaches
// `orphan-resolve`, which enumerates every entry against the live lease state
// under its stored ownership identity. The BoundaryEntry union itself is
// S20's; this is the one variant the fencing worker persists, in the union's
// exact shape.
type RemoteFencingBoundary struct {
	Kind         string     `json:"kind"`
	FencingEpoch Epoch      `json:"fencingEpoch"`
	GuardEpoch   uint64     `json:"guardEpoch"`
	LeaseEntries []LeaseRef `json:"leaseEntries"`
}

// NewRemoteFencingBoundary builds the boundary for one timed-out fencing. A
// nil entry list writes the empty array §9 defines ("An empty array means no
// spawned subprocess survived the crash"), never null.
func NewRemoteFencingBoundary(epoch Epoch, guardEpoch uint64, entries []LeaseRef) RemoteFencingBoundary {
	if entries == nil {
		entries = []LeaseRef{}
	}
	return RemoteFencingBoundary{
		Kind: RemoteFencingBoundaryKind, FencingEpoch: epoch, GuardEpoch: guardEpoch, LeaseEntries: entries,
	}
}

// Validate checks the boundary against §9's shape.
func (b RemoteFencingBoundary) Validate() error {
	if b.Kind != RemoteFencingBoundaryKind {
		return fmt.Errorf("%w: a remote-fencing boundary carries kind %q", ErrInvalidGuard, b.Kind)
	}
	if err := b.FencingEpoch.Validate(); err != nil {
		return err
	}
	if b.GuardEpoch == 0 {
		return fmt.Errorf("%w: a remote-fencing boundary carries no guard epoch", ErrInvalidGuard)
	}
	for _, entry := range b.LeaseEntries {
		if entry.Command == "" || entry.RegisteredAt == "" {
			return fmt.Errorf("%w: a remote-fencing lease entry carries no command or registration time", ErrInvalidGuard)
		}
		if err := entry.Ownership.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// BoundaryArray marshals the boundary as the one-member `BoundaryEntry[]` the
// orphan-unverified record persists: "a single `remote-fencing` entry for a
// fencing-timeout quarantine record" (§9).
func (b RemoteFencingBoundary) BoundaryArray() (json.RawMessage, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal([]RemoteFencingBoundary{b})
}

// FencingQuarantineStore is the operation-store half the fencing worker needs:
// §4's single atomic write that lands the fencing-timeout record, its
// remote-fencing boundary, and the per-host quarantine marker. hostops.Store
// implements it.
type FencingQuarantineStore interface {
	QuarantineFencing(recordID string, boundary json.RawMessage) (hostops.Record, error)
}

// FenceRequest is one new operation's fencing: the epoch it runs under, the
// operation record a timeout lands on, and the store that lands it.
type FenceRequest struct {
	Epoch     Epoch
	RecordID  string
	Store     FencingQuarantineStore
	Deadlines FencingDeadlines
}

// FenceOutcome is what a completed fencing sequence leaves: the epoch that now
// holds the lease and names the guard, the superseded epoch the worker's record
// agrees with the helper's fence on, and the lease-tracked work it enumerated
// and settled.
type FenceOutcome struct {
	Epoch      Epoch
	Superseded *Epoch
	// GuardEpoch is the guard file's monotonic sequence after the advance.
	GuardEpoch uint64
	// Entries is the superseded epoch's live lease-tracked work at kill time.
	Entries []LeaseRef
	// Settled is the subset of Entries whose exit was confirmed.
	Settled []LeaseRef
}

// FencingTimeoutError is §4's fencing-failure outcome: the kill/wait bound
// fired with the timed-out epoch's work unconfirmed. The operation's result is
// terminal, its record is orphan-unverified carrying Boundary, and Host is
// closed until `orphan-resolve` clears the marker.
type FencingTimeoutError struct {
	// Discriminator is §8's typed class (DiscriminatorFencingFailure).
	Discriminator string
	Host          string
	Epoch         Epoch
	Superseded    *Epoch
	// GuardEpoch is the guard file's fence sequence at the timeout.
	GuardEpoch uint64
	// Boundary is the remote-fencing entry persisted on the record.
	Boundary RemoteFencingBoundary
	// Live lists the entries still unconfirmed when the bound fired.
	Live []LeaseRef
	// Cause is the bound that fired (a context deadline).
	Cause error
	// Record is the persisted orphan-unverified record, nil when the write
	// could not land.
	Record *hostops.Record
	// PersistErr is why the record/marker write failed, nil when it landed.
	PersistErr error
}

// Error renders the fencing failure naming the host, the epoch, and whether the
// quarantine record landed.
func (e *FencingTimeoutError) Error() string {
	message := fmt.Sprintf("host %q: %s — the kill/wait for epoch %s/%d did not confirm exit (%d entries unconfirmed)",
		e.Host, e.Discriminator, e.Epoch.BootID, e.Epoch.OpSeq, len(e.Live))
	if e.Record != nil {
		return message + fmt.Sprintf("; record %s is orphan-unverified and the host is quarantined", e.Record.ID)
	}
	return message + "; the orphan-unverified record could not be persisted"
}

// Unwrap maps the timeout into the fencing-failure class.
func (e *FencingTimeoutError) Unwrap() error { return ErrFencingFailure }

// deadlineError is the internal marker that a bounded context fired while the
// caller's own context was still live: the condition §4 turns into the
// fencing-failure outcome. A caller-context cancellation is not this.
type deadlineError struct {
	// stillLive are the entries not exit-confirmed when the bound fired, in
	// enumeration order.
	stillLive []LeaseEntry
	cause     error
}

func (d *deadlineError) Error() string {
	return fmt.Sprintf("hostfence: the fencing bound fired with %d entries unconfirmed: %v", len(d.stillLive), d.cause)
}

// Fence runs §4's fencing sequence for one new operation's epoch: it decides
// from the observed live lease/guard state, takes over through the helper, runs
// the bounded kill/wait over the superseded epoch's lease-tracked work, and
// advances the guard only after every entry is exit-confirmed. On a kill/wait
// timeout it persists the fencing-failure record (orphan-unverified, carrying
// the remote-fencing boundary) and the per-host quarantine marker in one
// atomic store write, and returns the typed timeout — never an operable-but-
// unfenced host and never a silent success.
//
// The handle is a Verified one, so the trust boundary holds by construction:
// every remote step goes through the helper that passed §6's gate, and no
// refusal is ever read as a generic transport error.
func (v Verified) Fence(ctx context.Context, req FenceRequest) (FenceOutcome, error) {
	if req.Store == nil {
		return FenceOutcome{}, errors.New("hostfence: fencing needs the operation store for its timeout record")
	}
	if req.RecordID == "" {
		return FenceOutcome{}, errors.New("hostfence: fencing needs the operation record id")
	}
	if err := req.Epoch.Validate(); err != nil {
		return FenceOutcome{}, err
	}
	deadlines := req.Deadlines.withDefaults()
	w := v.WrapperHandle()
	observed, err := w.Status(ctx)
	if err != nil {
		return FenceOutcome{}, err
	}
	// The decision comes from the observed state; the helper's server-side
	// check stays the authority on the call itself (§4's re-check rule).
	planned, err := observed.Guard().Takeover(req.Epoch)
	if err != nil {
		return FenceOutcome{}, err
	}
	if observed.Guard().Admits(req.Epoch) {
		// The guard already names this epoch with no fence pending: the fencing
		// completed (a replay), so there is nothing to supersede or kill.
		return FenceOutcome{Epoch: req.Epoch, Superseded: observed.Superseded, GuardEpoch: observed.GuardEpoch}, nil
	}
	landed, err := v.Takeover(ctx, req.Epoch)
	if err != nil {
		return FenceOutcome{}, err
	}
	superseded, err := takeoverAgrees(planned, landed.Guard(), req.Epoch)
	if err != nil {
		return FenceOutcome{}, err
	}
	// Enumerate after the takeover: with the fence pending no new lease entry
	// can be registered, so every live entry here is the superseded epoch's
	// already-running work (§4:101).
	entries, err := w.Entries(ctx)
	if err != nil {
		return FenceOutcome{}, err
	}
	live := liveLeaseEntries(entries)
	settled, err := v.killAndWait(ctx, req.Epoch, live, deadlines)
	if err != nil {
		if deadline, ok := errors.AsType[*deadlineError](err); ok {
			return FenceOutcome{}, v.persistTimeout(req, landed.Guard(), superseded, live, deadline)
		}
		return FenceOutcome{}, err
	}
	// Only now does the guard advance: the fence record persists through it, so
	// no window exists where the lease is released but the guard has not moved.
	advanced, err := v.Advance(ctx, req.Epoch)
	if err != nil {
		return FenceOutcome{}, err
	}
	if err := advanceAgrees(landed.Guard(), advanced.Guard(), req.Epoch); err != nil {
		return FenceOutcome{}, err
	}
	return FenceOutcome{
		Epoch:      req.Epoch,
		Superseded: superseded,
		GuardEpoch: advanced.GuardEpoch,
		Entries:    leaseRefs(live),
		Settled:    settled,
	}, nil
}

// takeoverAgrees checks the helper's landed takeover against the controller's
// planned one and returns the superseded epoch the worker records. §4's fence
// state — fencing epoch, superseded epoch, monotonic fence sequence — must read
// the same on both sides; anything else is ErrFenceDisagreement and the worker
// issues no kill/wait and no advance.
func takeoverAgrees(planned, landed GuardState, e Epoch) (*Epoch, error) {
	if planned.Fence == nil || planned.Fence.Epoch != e {
		return nil, fmt.Errorf("%w: the controller's planned takeover carries no fence for %s/%d",
			ErrFenceDisagreement, e.BootID, e.OpSeq)
	}
	if landed.Fence == nil || landed.Fence.Epoch != e {
		return nil, fmt.Errorf("%w: the helper landed fence %+v, want one for %s/%d",
			ErrFenceDisagreement, landed.Fence, e.BootID, e.OpSeq)
	}
	if landed.Holder == nil || *landed.Holder != e {
		return nil, fmt.Errorf("%w: the helper landed holder %+v, want %s/%d",
			ErrFenceDisagreement, landed.Holder, e.BootID, e.OpSeq)
	}
	if landed.GuardEpoch != planned.GuardEpoch || landed.Fence.GuardEpoch != planned.Fence.GuardEpoch {
		return nil, fmt.Errorf("%w: the takeover landed guard sequence %d/%d, want %d/%d",
			ErrFenceDisagreement, landed.GuardEpoch, landed.Fence.GuardEpoch, planned.GuardEpoch, planned.Fence.GuardEpoch)
	}
	if !sameEpoch(landed.Fence.Superseded, planned.Fence.Superseded) ||
		!sameEpoch(landed.Superseded, planned.Superseded) {
		return nil, fmt.Errorf("%w: the takeover superseded %+v/%+v, want %+v/%+v",
			ErrFenceDisagreement, landed.Fence.Superseded, landed.Superseded, planned.Fence.Superseded, planned.Superseded)
	}
	if landed.Fence.Superseded == nil {
		return nil, nil
	}
	superseded := *landed.Fence.Superseded
	return &superseded, nil
}

// advanceAgrees checks the helper's advance settled the guard on e, exactly one
// sequence above the takeover's fence.
func advanceAgrees(landed, advanced GuardState, e Epoch) error {
	if !advanced.Admits(e) {
		return fmt.Errorf("%w: the advance did not settle the guard on %s/%d (epoch %+v, fence %+v)",
			ErrFenceDisagreement, e.BootID, e.OpSeq, advanced.Epoch, advanced.Fence)
	}
	if advanced.GuardEpoch != landed.GuardEpoch+1 {
		return fmt.Errorf("%w: the advance landed sequence %d, want %d",
			ErrFenceDisagreement, advanced.GuardEpoch, landed.GuardEpoch+1)
	}
	return nil
}

// killAndWait runs §4's two bounded contexts over the superseded epoch's live
// entries. It returns the refs of every entry whose exit was confirmed; a bound
// that fires while the caller's context is still live is the deadlineError
// the caller persists the fencing failure from. Any other error is the helper's
// typed refusal or a transport failure and aborts without persisting.
func (v Verified) killAndWait(ctx context.Context, e Epoch, live []LeaseEntry, deadlines FencingDeadlines) ([]LeaseRef, error) {
	if len(live) == 0 {
		return nil, nil
	}
	settled := make(map[string]bool, len(live))
	var unsettled []LeaseEntry
	killErr := func() error {
		killCtx, cancel := context.WithTimeout(ctx, deadlines.Kill)
		defer cancel()
		for _, entry := range live {
			report, err := v.Kill(killCtx, e, entry.ID)
			if err != nil {
				if killCtx.Err() != nil && ctx.Err() == nil {
					return &deadlineError{stillLive: unconfirmedEntries(live, settled), cause: killCtx.Err()}
				}
				return err
			}
			if report.Live {
				unsettled = append(unsettled, entry)
			} else {
				settled[entry.ID] = true
			}
		}
		return nil
	}()
	if killErr != nil {
		return nil, killErr
	}
	if len(unsettled) == 0 {
		return settledRefs(live, settled), nil
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, deadlines.Wait)
	defer cancelWait()
	for len(unsettled) > 0 {
		if err := ctx.Err(); err != nil {
			// The caller's own context ended: that is not this family's bound,
			// so nothing is quarantined for it — the caller may retry, and the
			// remote fence it left pending still refuses every mutation.
			return nil, err
		}
		if waitCtx.Err() != nil {
			return nil, &deadlineError{stillLive: unsettled, cause: waitCtx.Err()}
		}
		remaining := make([]LeaseEntry, 0, len(unsettled))
		for _, entry := range unsettled {
			recheck, err := v.WrapperHandle().Recheck(waitCtx, entry.ID)
			if err != nil {
				if waitCtx.Err() != nil && ctx.Err() == nil {
					return nil, &deadlineError{stillLive: unsettled, cause: waitCtx.Err()}
				}
				return nil, err
			}
			if recheck.Live {
				remaining = append(remaining, entry)
			} else {
				settled[entry.ID] = true
			}
		}
		unsettled = remaining
		if len(unsettled) == 0 {
			break
		}
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, &deadlineError{stillLive: unsettled, cause: waitCtx.Err()}
		case <-time.After(fencingPollInterval):
		}
	}
	return settledRefs(live, settled), nil
}

// persistTimeout lands §4's fencing-timeout write and returns the typed
// outcome. The boundary it persists is the enumerated superseded work — "the
// guard-file epoch plus the lease-tracked entries of the superseded epoch, each
// with its required ownership identity" — and the store lands it with the
// per-host marker in one atomic write.
func (v Verified) persistTimeout(req FenceRequest, landed GuardState, superseded *Epoch, enumerated []LeaseEntry, failure *deadlineError) error {
	boundary := NewRemoteFencingBoundary(req.Epoch, landed.Fence.GuardEpoch, leaseRefs(enumerated))
	timeout := &FencingTimeoutError{
		Discriminator: DiscriminatorFencingFailure,
		Host:          v.WrapperHandle().Host,
		Epoch:         req.Epoch,
		Superseded:    superseded,
		GuardEpoch:    landed.Fence.GuardEpoch,
		Boundary:      boundary,
		Live:          leaseRefs(failure.stillLive),
		Cause:         failure.cause,
	}
	raw, err := boundary.BoundaryArray()
	if err != nil {
		timeout.PersistErr = err
		return timeout
	}
	record, err := req.Store.QuarantineFencing(req.RecordID, raw)
	if err != nil {
		timeout.PersistErr = err
		return timeout
	}
	timeout.Record = &record
	return timeout
}

// liveLeaseEntries filters the enumerated lease entries to the work a fencing
// kill/wait addresses: an entry is live while it is registering or running.
func liveLeaseEntries(entries []LeaseEntry) []LeaseEntry {
	var live []LeaseEntry
	for _, entry := range entries {
		switch entry.State {
		case LeaseRegistering, LeaseRunning:
			live = append(live, entry)
		}
	}
	return live
}

// unconfirmedEntries lists the entries not (yet) settled, in enumeration order.
func unconfirmedEntries(live []LeaseEntry, settled map[string]bool) []LeaseEntry {
	var out []LeaseEntry
	for _, entry := range live {
		if !settled[entry.ID] {
			out = append(out, entry)
		}
	}
	return out
}

// settledRefs lists the settled entries' boundary refs, in enumeration order.
func settledRefs(live []LeaseEntry, settled map[string]bool) []LeaseRef {
	var out []LeaseRef
	for _, entry := range live {
		if settled[entry.ID] {
			out = append(out, entry.BoundaryRef())
		}
	}
	return out
}

// leaseRefs projects entries into §9's boundary shape, preserving order.
func leaseRefs(entries []LeaseEntry) []LeaseRef {
	refs := make([]LeaseRef, 0, len(entries))
	for _, entry := range entries {
		refs = append(refs, entry.BoundaryRef())
	}
	return refs
}

// sameEpoch compares two optional epochs.
func sameEpoch(a, b *Epoch) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
