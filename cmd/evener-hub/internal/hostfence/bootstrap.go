package hostfence

// This file owns crash-fencing §6's first-contact bootstrap: the durable
// bootstrap-attempt fence and `helperInstalled` convergence of §6:133/:137, the
// one exempt delivery step of §6:131 with its atomic claim-plus-quiesce gate of
// §6:135, and the post-crash recovery rule of §6:139. The per-host record the
// flags live on is hub.toml's `[host_records."<name>"]` machine record
// (registry spec 08 §6); this package names the shape and the sequence, and the
// hub's record machinery persists it in the record's existing atomic write.
//
// S17 recorded this slice's boundary in the package comment: "bootstrap
// delivery with `helperInstalled` (S21). They consume the types and decoders
// here" (epoch.go). So this file consumes the epoch model, the helper gate, and
// the wrapper's Verified handle, and reinvents none of them: every remote step
// here either ships the embedded helper bytes exactly once (§6:131) or runs the
// read-only self-test through the Verified gate.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Provisioning is the bootstrap half of a host's machine record: the durable
// bootstrap-attempt fence and the converged helperInstalled flag with the
// version record it carries (crash-fencing §6:131-137; the hub.toml machine
// record is registry spec 08 §6's `[host_records."<name>"]`). It is the state
// this package decides against; the hub maps its file record onto it.
type Provisioning struct {
	// AttemptFenced is §6:133's durable bootstrap-attempt fence: a first-contact
	// delivery started, written in its own atomic hub.toml write before the
	// attempt's first remote side effect. It is never cleared — a host carrying
	// it takes the fenced path on every later attempt (§6:137).
	AttemptFenced bool
	// AttemptEpoch is the fencing epoch the attempt fence was minted under
	// (§6:131: the exemption "runs under the worker's persisted epoch"). It is
	// what §6:139's recovery re-probe names after any restart: the crashed
	// attempt's process belongs to that epoch, not to whatever epoch the next
	// attempt mints. A fence without it cannot identify the crashed attempt and
	// recovery fails closed.
	AttemptEpoch Epoch
	// HelperInstalled is §6:137's converged flag: the delivery finalized and the
	// pinned helper lives on the host.
	HelperInstalled bool
	// HelperVersion is the version the finalize recorded (§6:131's "prior helper
	// version record"). Zero means no version record.
	HelperVersion uint64
	// AttemptToken is the durable ownership token the fence write minted for the
	// attempt that won it. Delivery must revalidate it immediately before the
	// delivery step, and recovery invalidates it on the verified-gone path, so a
	// paused owner cannot deliver after recovery concluded (its epoch still
	// matches; only the token tells the two apart).
	AttemptToken string
}

// Provisioned reports whether the host carries the whole converged record: the
// attempt fence, the installed flag, and the version record its finalize wrote.
// An installed-without-fence or installed-without-version record is not
// provisioned — both are shapes no writer emits (the record loader refuses
// them), and reading one as provisioned would hide that.
func (p Provisioning) Provisioned() bool {
	return p.AttemptFenced && p.HelperInstalled && p.HelperVersion != 0
}

// ErrStaleBootstrapAttempt is the sentinel of the stale-registration class: a
// bootstrap write refused because the host no longer carries the identity the
// attempt was bound to.
var ErrStaleBootstrapAttempt = errors.New("hostfence: the bootstrap attempt's host registration changed")

// StaleAttemptError reports a bootstrap write refused because the live host no
// longer carries the identity the attempt was bound to — a remove and re-add
// landed while the attempt paused. It is deliberately not one of §8's
// helper-gate classes: the helper is not the problem; the entry the caller
// resolved is stale, which is the deploy pipeline's `stale-entry` class.
type StaleAttemptError struct {
	Host  string
	Bound BootstrapIdentity
	Live  BootstrapIdentity
}

// Error renders the refusal with both registrations.
func (e *StaleAttemptError) Error() string {
	return fmt.Sprintf(
		"host %q: the bootstrap attempt is bound to generation %d/%q/%d but the live host carries %d/%q/%d; re-resolve the host and retry",
		e.Host, e.Bound.Generation, e.Bound.IncarnationID, e.Bound.PresenceEpoch,
		e.Live.Generation, e.Live.IncarnationID, e.Live.PresenceEpoch)
}

// Unwrap maps the refusal into the stale-registration class.
func (e *StaleAttemptError) Unwrap() error { return ErrStaleBootstrapAttempt }

// FencedWithoutHelper reports §6:137's crash posture: the attempt fence landed
// but helperInstalled did not, so every later attempt takes the fenced recovery
// path — never a second unfenced delivery.
func (p Provisioning) FencedWithoutHelper() bool { return p.AttemptFenced && !p.HelperInstalled }

// BootstrapEvidence is §6:131's non-record half of the exemption rule: a host
// "the controller never fenced an epoch on" and one with no "interrupted record
// from a crashed incarnation". Both are operation-store evidence, so the caller
// supplies them; this package never reads the store.
type BootstrapEvidence struct {
	// FencedEpoch reports that the controller fenced a fencing epoch on this
	// host before.
	FencedEpoch bool
	// Interrupted reports an interrupted record from a crashed incarnation on
	// this host.
	Interrupted bool
}

// BootstrapIdentity is the host registration one bootstrap attempt is bound to:
// the registry spec §1 (generation, incarnation id, presence epoch) triple the
// caller resolved. Every fence, revalidation, and finalize write verifies the
// live host still carries it, so an attempt that pauses across a remove and
// re-add can never fence or deliver against the new incarnation with its stale
// epoch.
type BootstrapIdentity struct {
	Generation    uint64
	IncarnationID string
	PresenceEpoch uint64
}

// IsZero reports whether the identity carries nothing: an unbound attempt, which
// every write refuses.
func (i BootstrapIdentity) IsZero() bool {
	return i.Generation == 0 && i.IncarnationID == "" && i.PresenceEpoch == 0
}

// ExemptDeliveryPermitted reports §6:131's exactly-once exemption: the
// first-attach repair "runs unfenced exactly once per never-provisioned host",
// and it "never runs for a host with a prior fenced epoch, a prior helper
// version record, or an interrupted record from a crashed incarnation". A host
// carrying the attempt fence is no longer never-provisioned either — the
// exemption has already been spent.
func ExemptDeliveryPermitted(p Provisioning, ev BootstrapEvidence) bool {
	return !p.AttemptFenced && !p.HelperInstalled && p.HelperVersion == 0 && !ev.FencedEpoch && !ev.Interrupted
}

// BootstrapStore is the hub.toml half the bootstrap flow needs: the host
// record's provisioning state and the two atomic writes §6:133/:137 pins. Each
// write is its own hub.toml write through the record machinery's existing
// atomic protocol. hostops is not this seam — the record lives in hub.toml —
// and a failure returns the record as it stands, so the flow never reports a
// convergence the file does not carry.
type BootstrapStore interface {
	// Provisioning reads the host's current record.
	Provisioning(host string) (Provisioning, error)
	// PersistAttemptFence writes the durable bootstrap-attempt fence with the
	// attempt's epoch in its own atomic hub.toml write, before the attempt's
	// first remote side effect. The write is conditional and atomic: a record
	// that already carries a fence is returned unchanged — the record still
	// carries that first attempt's fenced epoch — never overwritten by a later
	// attempt, so two concurrent first-contacts cannot both deliver. won is true
	// exactly when this call performed the fence write, so ownership is explicit
	// and never inferred from the epoch (two attempts of one operation may share
	// one persisted epoch). The winning record carries the durable AttemptToken
	// the caller revalidates before delivering. A record returned without the
	// fence is refused by the caller, never delivered behind.
	PersistAttemptFence(host string, identity BootstrapIdentity, epoch Epoch) (Provisioning, bool, error)
	// FinalizeBootstrap converges helperInstalled with the delivered version in
	// the same atomic hub.toml write that finalizes bootstrap (§6:137). A
	// failure refuses finalize: the attempt fence stays and helperInstalled is
	// never converged behind it.
	FinalizeBootstrap(host string, identity BootstrapIdentity, helperVersion uint64) (Provisioning, error)
	// RevalidateAttemptFence reports whether token still stands for the attempt
	// at epoch on host: the record still carries the fence, names that epoch,
	// carries that token, and has not converged helperInstalled. The delivery
	// path calls it after winning its claim and before its first delivery step.
	RevalidateAttemptFence(host string, identity BootstrapIdentity, epoch Epoch, token string) (Provisioning, bool, error)
	// InvalidateAttemptFence retires token in the same atomic write discipline as
	// the other record writes, preconditioned on the record still naming that
	// attempt and not having converged helperInstalled. Recovery calls it on the
	// verified-gone path, while it holds its claim, before releasing and
	// returning the fenced path. It returns the record as it stands after the
	// call.
	InvalidateAttemptFence(host string, identity BootstrapIdentity, epoch Epoch, token string) (Provisioning, error)
}

// QuiesceReport is the atomic claim-plus-quiesce primitive's answer (§6:135):
// the single claim one concurrent claimant wins, plus the foreign presence the
// same atomic step observed.
type QuiesceReport struct {
	// Claimed reports whether this claimant won. Exactly one concurrent
	// claimant wins; a loser reads false.
	Claimed bool
	// ForeignProcesses names running managed processes outside the claimed
	// guard's ownership.
	ForeignProcesses []string
	// ForeignGuardHolders names live foreign guard claims.
	ForeignGuardHolders []string
}

// Bare reports §6:135's proof of bareness: this claimant won the claim and the
// same atomic quiesce saw no live foreign process and no live foreign guard
// holder.
func (r QuiesceReport) Bare() bool {
	return r.Claimed && len(r.ForeignProcesses) == 0 && len(r.ForeignGuardHolders) == 0
}

// BootstrapClaim is one won claim-plus-quiesce lease, held for the caller's
// whole delivery: §6:135 requires the quiesce to "hold for the entire delivery
// so no process or controller starting after the claim can overlap it". A
// primitive that can only report a point in time has not quiesced anything;
// releasing the claim is how the hold ends, and a crash drops it with the
// process.
type BootstrapClaim interface {
	// Release ends the hold. It is called once, after the delivery finished (or
	// failed), and its error is the caller's to log.
	Release(ctx context.Context) error
}

// ClaimQuiesce is §6:135's "pre-existing trusted host-side primitive": one
// atomic claim-plus-quiesce naming this controller's fencing epoch, returning
// the held claim the delivery runs under. Where no such primitive exists
// delivery is unavailable — nil is that "unavailable", never a fallback to an
// ordinary-SSH claim-then-check, whose check cannot cover processes starting
// mid-delivery.
type ClaimQuiesce interface {
	// ClaimAndQuiesce atomically claims the host and quiesces it for epoch. A
	// non-bare report refuses; a nil claim from a winning primitive is treated
	// as "cannot hold the claim" and refused before any delivery.
	ClaimAndQuiesce(ctx context.Context, epoch Epoch) (QuiesceReport, BootstrapClaim, error)
}

// AttemptProbe is §6:139's read-only recovery re-probe: it answers whether any
// process the crashed bootstrap attempt started is still live, named by the
// epoch the attempt fence persisted. It is read-only;
// no step of recovery mutates before it answers.
type AttemptProbe interface {
	BootstrappedProcessLive(ctx context.Context, epoch Epoch) (bool, error)
}

// BootstrapKind is what one Bootstrap call left the host as.
type BootstrapKind int

const (
	// BootstrapUnset is the zero value: the call refused before deciding.
	BootstrapUnset BootstrapKind = iota
	// BootstrapProvisioned means the host already carries helperInstalled: the
	// ordinary fenced path, with no bootstrap step.
	BootstrapProvisioned
	// BootstrapFenced means no delivery was permitted (or, after a crash, the
	// recovery re-probe cleared the crashed attempt): the ordinary fenced path,
	// where the helper gate decides.
	BootstrapFenced
	// BootstrapDelivered means this attempt delivered and finalized the helper.
	BootstrapDelivered
)

// String renders one kind for diagnostics.
func (k BootstrapKind) String() string {
	switch k {
	case BootstrapUnset:
		return "unset"
	case BootstrapProvisioned:
		return "provisioned"
	case BootstrapFenced:
		return "fenced"
	case BootstrapDelivered:
		return "delivered"
	default:
		return fmt.Sprintf("BootstrapKind(%d)", int(k))
	}
}

// BootstrapOutcome is what a decided attempt leaves: the kind and the record as
// it stood after the step.
type BootstrapOutcome struct {
	Kind         BootstrapKind
	Provisioning Provisioning
	// ReleaseErr is the held claim's release failure, when one happened: the
	// host-side claim may still be held, so a caller must not treat the attempt
	// as clean. Nil on a clean release and when no claim was held.
	ReleaseErr error
}

// BootstrapRequest is one first-contact attempt: the host, the epoch it runs
// under (the worker's persisted epoch, §6:131), the operation-store evidence
// the exemption reads, and the seams — the record store, the remote runner, the
// host-side claim primitive, and the recovery probe.
type BootstrapRequest struct {
	// Host names the host, for refusals and the record keys.
	Host string
	// Epoch is the worker's persisted fencing epoch (§6:131: the exemption
	// "runs under the worker's persisted epoch").
	Epoch Epoch
	// Evidence is the non-record half of the exemption rule.
	Evidence BootstrapEvidence
	// Store is the hub.toml record seam. Required.
	Store BootstrapStore
	// Identity is the host registration this attempt is bound to. Required: every
	// write verifies the live host still carries it, so an attempt that pauses
	// across a remove and re-add cannot fence or deliver against the new
	// incarnation. The first-contact caller (S21b) supplies the expectation it
	// read from the row it resolved.
	Identity BootstrapIdentity
	// Runner is the remote exec seam, used for the delivery and the self-test.
	Runner Runner
	// Quiesce is the host-side atomic claim-plus-quiesce primitive. Nil, a lost
	// race, or foreign presence refuses delivery as `fencing-helper-absent`.
	Quiesce ClaimQuiesce
	// Probe is the read-only recovery re-probe of §6:139, used only on the
	// attempt-fenced-without-helper posture.
	Probe AttemptProbe
	// Path overrides the helper's remote path (tests and non-default layouts).
	Path string
	// Order, when set, is called with each step's name in sequence
	// ("attempt", "claim", "deliver", "verify", "finalize"): the ordering
	// evidence the crash-window tests pin.
	Order func(step string)
	// Logf, when set, receives diagnostics that must not change the outcome —
	// today the held claim's release failure. BootstrapOutcome.ReleaseErr always
	// carries it; this sink is for a caller's own log.
	Logf func(format string, args ...any)
}

// AttemptOrphanError reports §6:139's live crashed-attempt process: a
// bootstrapped process from the crashed attempt is still live, so the next
// mutation must not run until the operator repairs out-of-band through the
// one-time migration path. It is deliberately not one of §8's helper-gate
// classes: the helper is not absent or untrusted here — the crashed attempt's
// own process is live.
type AttemptOrphanError struct {
	Host   string
	Epoch  Epoch
	Detail string
}

// Error renders the refusal naming the host and epoch.
func (e *AttemptOrphanError) Error() string {
	detail := e.Detail
	if detail == "" {
		detail = "a bootstrapped process from the crashed attempt is live"
	}
	return fmt.Sprintf("host %q: the crashed bootstrap attempt for epoch %s/%d is not provably gone: %s; the operator repairs the host out-of-band",
		e.Host, e.Epoch.BootID, e.Epoch.OpSeq, detail)
}

// Bootstrap runs §6's first-contact attempt for the host's current record. It
// decides from the store's record (re-read at entry, so a retry racing the
// finalize replays under dedup rather than delivering again, §6:139); on an
// eligible never-provisioned host it persists the attempt fence in its own
// write before the first remote side effect (§6:133), refuses a fence write
// that did not land, delivers only while holding the atomic claim-plus-quiesce
// lease (§6:135: the hold spans the delivery, the self-test, and the finalize),
// self-tests the delivered helper through §6's verified-handle gate, and
// converges helperInstalled with the pinned version in the finalizing write
// (§6:137) — or refuses finalize on any failure. A host carrying the attempt
// fence without helperInstalled takes the recovery path (§6:139): a read-only
// re-probe naming the fence's persisted attempt epoch first, and the typed
// `fencing-helper-absent` when the attempt cannot be identified or verified.
//
// Nothing here auto-installs out of band, migrates in band, or degrades the
// exemption to an overwrite. Every refusal the helper gate owns is typed; the
// caller maps it onto §8's conflict-class envelope.
func Bootstrap(ctx context.Context, req BootstrapRequest) (outcome BootstrapOutcome, err error) {
	if req.Store == nil {
		return BootstrapOutcome{}, errors.New("hostfence: bootstrap needs the record store for the attempt fence")
	}
	if strings.TrimSpace(req.Host) == "" {
		return BootstrapOutcome{}, errors.New("hostfence: bootstrap needs a host name")
	}
	if req.Identity.IsZero() {
		// Every write binds to the resolved registration; an unbound attempt could
		// fence an incarnation it never resolved.
		return BootstrapOutcome{}, errors.New("hostfence: bootstrap needs the host's resolved identity")
	}
	if err := req.Epoch.Validate(); err != nil {
		return BootstrapOutcome{}, err
	}
	step := func(name string) {
		if req.Order != nil {
			req.Order(name)
		}
	}

	record, err := req.Store.Provisioning(req.Host)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	switch {
	case record.Provisioned():
		// The converged record: the ordinary fenced path, no bootstrap step. This
		// is also the dedup replay — a retry that arrived after another attempt's
		// finalize observes it here and never runs a second delivery (§6:139).
		return BootstrapOutcome{Kind: BootstrapProvisioned, Provisioning: record}, nil
	case record.FencedWithoutHelper():
		// §6:137's crash posture: the exemption is spent, so every later attempt
		// takes the fenced path — after §6:139's recovery re-probe.
		return recoverFencedAttempt(ctx, req, record)
	case !ExemptDeliveryPermitted(record, req.Evidence):
		// Never a first-contact host: a prior fenced epoch, an interrupted record,
		// or a prior helper version record. The ordinary fenced path applies.
		return BootstrapOutcome{Kind: BootstrapFenced, Provisioning: record}, nil
	}

	// §6:133 — the attempt fence lands in its own atomic write before the first
	// remote side effect, so a crash before helperInstalled leaves the host
	// attempt-fenced, never never-provisioned again.
	step("attempt")
	fenced, won, err := req.Store.PersistAttemptFence(req.Host, req.Identity, req.Epoch)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	if !fenced.AttemptFenced {
		// A store that answered without the fence did not land §6:133's write:
		// delivering behind it would be the unfenced delivery the fence exists to
		// forbid.
		return BootstrapOutcome{}, fmt.Errorf("hostfence: the attempt-fence write for host %q returned without the fence; no delivery was attempted", req.Host)
	}
	if fenced.Provisioned() {
		// A concurrent finalize converged the record between the read and the
		// fence write: replay as provisioned rather than delivering a second time.
		return BootstrapOutcome{Kind: BootstrapProvisioned, Provisioning: fenced}, nil
	}
	if !won || fenced.AttemptEpoch != req.Epoch {
		// Ownership is explicit: only the call that won the conditional write
		// delivers, and only under the epoch it wrote. A call that lost the race —
		// even one sharing the same persisted epoch (a replay of one operation) —
		// must never deliver a second time; it takes the recovery path naming the
		// fence owner's epoch.
		return recoverFencedAttempt(ctx, req, fenced)
	}
	token := fenced.AttemptToken
	if token == "" {
		// The fence write must bind ownership durably; without a token the
		// pre-delivery revalidation cannot tell a retired attempt from a live one.
		return BootstrapOutcome{}, fmt.Errorf("hostfence: the attempt-fence write for host %q returned no ownership token; no delivery was attempted", req.Host)
	}

	// §6:135 — delivery is permitted only through the atomic claim-plus-quiesce.
	if req.Quiesce == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host carries no pre-existing trusted atomic claim-plus-quiesce primitive; provision the helper out-of-band through the one-time migration path")
	}
	step("claim")
	report, claim, err := req.Quiesce.ClaimAndQuiesce(ctx, req.Epoch)
	if claim != nil {
		// The claim is held from here on and released on every exit, including
		// the refusal paths below and a claim returned beside an error (a crash
		// drops it with the process). A release failure is surfaced, never
		// silently dropped: the host-side claim may still be held.
		defer func() {
			if releaseErr := claim.Release(context.WithoutCancel(ctx)); releaseErr != nil {
				outcome.ReleaseErr = releaseErr
				if req.Logf != nil {
					req.Logf("bootstrap: releasing the claim for host %q failed: %v", req.Host, releaseErr)
				}
				// A claim that could not be released is still held: surface it on
				// the returned error whether or not the step itself also failed,
				// so a silently held claim is impossible.
				if err == nil {
					err = releaseErr
				} else {
					err = errors.Join(err, releaseErr)
				}
			}
			step("release")
		}()
	}
	if err != nil {
		if ctx.Err() != nil {
			// The caller's own context ended: that is not the claim's refusal and
			// nothing about the helper is known, so the raw error stays.
			return BootstrapOutcome{}, err
		}
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the host's atomic claim-plus-quiesce primitive failed: "+err.Error())
	}
	if !report.Bare() {
		// §6:135/:162 pin every non-bare answer to the typed absent class: a lost
		// claim race, an unavailable primitive, and any live foreign/guard
		// presence all refuse fail-closed with `fencing-helper-absent`, whose
		// client surface is the one-time out-of-band migration step. The detail
		// names the observation factually; the exemption never degrades to
		// overwrite.
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, describeClaimObservation(report))
	}
	if claim == nil {
		// A winning report with no held claim is a point-in-time answer, not the
		// quiesce §6:135 requires to hold for the entire delivery: refuse before
		// any delivery rather than deliver outside a hold.
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host's claim-plus-quiesce primitive returned no held claim, so the delivery could not run under one")
	}
	// The last pre-delivery check, made while this attempt holds its claim: the
	// token under its own held claim, so a paused owner that resumes after
	// recovery concluded finds it retired here and refuses instead of delivering.
	// The two holds cannot overlap (the claim arbiter is exactly-one-wins), so a
	// pre-claim owner can only resume after recovery released, and this read is
	// what observes the invalidation.
	if _, ok, err := req.Store.RevalidateAttemptFence(req.Host, req.Identity, req.Epoch, token); err != nil {
		return BootstrapOutcome{}, err
	} else if !ok {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the attempt fence no longer names this attempt (a lost or retired bootstrap claim; another attempt recovered it)")
	}

	// The one exempt delivery step: ship the deployed payload with the helper
	// bytes inside (§6:131), unfenced but under the won claim.
	step("deliver")
	if err := runDelivery(ctx, req); err != nil {
		if ctx.Err() != nil || errors.Is(err, errNoDeliveryRunner) {
			return BootstrapOutcome{}, err
		}
		// A delivery that could not run the helper at all — a transport failure
		// or a non-zero exit — is §6:135's absent class, so a caller classifies
		// it as the conflict-class refusal rather than a generic/probe error.
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the helper delivery failed: "+err.Error())
	}

	// The delivered helper must pass §6's own read-only presence/version gate
	// before the flag converges: a finalize behind a helper that cannot run the
	// pinned protocol would record a convergence that is not true.
	step("verify")
	wrapper := Wrapper{Runner: req.Runner, Host: req.Host, Path: req.Path}
	if _, err := wrapper.Check(ctx); err != nil {
		switch {
		case ctx.Err() != nil:
			// The caller's own context ended: raw, as everywhere else.
			return BootstrapOutcome{}, err
		case helperGateRefusal(err):
			// The gate's own typed refusal (absent/untrusted) passes through with
			// its exact class and data, never double-wrapped.
			return BootstrapOutcome{}, err
		default:
			// The verify round trip read nothing (a transport failure): §6:135's
			// absent class, so a caller classifies it as the conflict-class
			// refusal and never maps it to `probe-failed` (§8:161-162).
			return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the delivered helper could not be verified: "+err.Error())
		}
	}

	// §6:137 — converge helperInstalled in the same finalizing atomic write, or
	// refuse finalize on failure.
	step("finalize")
	finalized, err := req.Store.FinalizeBootstrap(req.Host, req.Identity, HelperVersion)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	if !finalized.HelperInstalled {
		return BootstrapOutcome{}, fmt.Errorf("hostfence: the finalizing write for host %q did not converge helperInstalled", req.Host)
	}
	return BootstrapOutcome{Kind: BootstrapDelivered, Provisioning: finalized}, nil
}

// recoverFencedAttempt runs §6:139's recovery for a host carrying the attempt
// fence without helperInstalled. It is NOT the read-only step its first
// description said: recovery's first step is the §6:135 claim arbiter, because
// the fence record alone cannot tell a crashed attempt's pre-delivery window
// from a live attempt about to deliver, and opening the fenced path while an
// attempt is active would overlap it. Recovery therefore:
//
//  1. refuses an unidentifiable attempt (a fence with no epoch) as absent;
//  2. requires the claim-plus-quiesce primitive and refuses without it as
//     absent (an active attempt cannot be excluded);
//  3. tries the claim. A lost claim (Claimed=false) or any non-bare answer means
//     an attempt is active or foreign work lives: refuse with the typed absent
//     refusal (§6:135/:162), never a clean fenced/provisioned outcome;
//  4. holding the won claim across the read-only re-probe and the decision,
//     probes the epoch the fence persisted, retires the attempt's ownership
//     token on the verified-gone path (so a paused owner's own pre-delivery
//     revalidation refuses), and releases on every path.
//
// Two convergence windows are closed explicitly: the record is re-read
// immediately after the claim is won (the owner may have finalized while
// recovery waited), and the invalidation step returns the record as it stands
// (the owner may have finalized during the probe). Either way a converged record
// replays as provisioned — recovery never probes or reports an orphan/absent
// posture for an attempt that already succeeded.
//
// The probe names the fence's own attempt epoch, not the request's, so a restart
// cannot make it miss the crashed attempt's process. A live process refuses with
// AttemptOrphanError; an unverifiable one refuses as absent (the class §8:162
// gives the unverifiable bootstrap-guard claim); a converged record replays as
// provisioned. Only a verified-gone attempt opens the fenced path.
//
// The claim is the arbiter the spec already defines ("exactly one concurrent
// claimant wins; a loser reads false", and the claim is "held for the caller's
// whole delivery"), so an owner parked between the fence write and its claim is
// caught by step 3 on either side: whichever of the owner and the recoverer
// claims first wins, and the other reads the honest active/busy class.
func recoverFencedAttempt(ctx context.Context, req BootstrapRequest, record Provisioning) (outcome BootstrapOutcome, err error) {
	attempt := record.AttemptEpoch
	if attempt.IsZero() {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the attempt fence records no epoch, so the crashed attempt cannot be identified; repair the host out-of-band through the one-time migration path")
	}
	if req.Quiesce == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host carries no pre-existing trusted atomic claim-plus-quiesce primitive, so an active bootstrap attempt cannot be excluded from recovery; provision the helper out-of-band and repair the host through the one-time migration path")
	}
	report, claim, err := req.Quiesce.ClaimAndQuiesce(ctx, req.Epoch)
	if claim != nil {
		defer func() {
			if releaseErr := claim.Release(context.WithoutCancel(ctx)); releaseErr != nil {
				outcome.ReleaseErr = releaseErr
				if req.Logf != nil {
					req.Logf("bootstrap: releasing the recovery claim for host %q failed: %v", req.Host, releaseErr)
				}
				if err == nil {
					err = releaseErr
				} else {
					err = errors.Join(err, releaseErr)
				}
			}
		}()
	}
	if err != nil {
		if ctx.Err() != nil {
			return BootstrapOutcome{}, err
		}
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the host's atomic claim-plus-quiesce primitive failed: "+err.Error())
	}
	if !report.Bare() {
		// §6:135/:162's class: a lost claim race, an unavailable primitive, or any
		// live foreign/guard presence refuses recovery with the typed absent
		// refusal, whose detail names the observation. Recovery must not conclude
		// while an attempt may be active, and the operator's surface is the
		// out-of-band migration step.
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, describeClaimObservation(report))
	}
	if claim == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host's claim-plus-quiesce primitive returned no held claim, so recovery cannot exclude an active attempt")
	}
	// The record read before the claim may be stale: the original attempt could
	// have finalized while this recovery waited for the claim. Re-read under the
	// held claim and replay as provisioned when it converged — never probe or
	// report an orphan/absent posture for an attempt that already succeeded.
	fresh, err := req.Store.Provisioning(req.Host)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	if fresh.Provisioned() {
		return BootstrapOutcome{Kind: BootstrapProvisioned, Provisioning: fresh}, nil
	}
	if !fresh.AttemptEpoch.IsZero() {
		attempt = fresh.AttemptEpoch
	}
	token := fresh.AttemptToken
	if req.Probe == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the crashed bootstrap attempt's process cannot be verified: the host carries no read-only attempt probe; repair the host through the one-time migration path")
	}
	live, err := req.Probe.BootstrappedProcessLive(ctx, attempt)
	if err != nil {
		if ctx.Err() != nil {
			return BootstrapOutcome{}, err
		}
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the crashed bootstrap attempt could not be verified: "+err.Error())
	}
	if live {
		return BootstrapOutcome{}, &AttemptOrphanError{
			Host: req.Host, Epoch: attempt,
			Detail: "a bootstrapped process from the crashed attempt is live",
		}
	}
	// The attempt is verified gone: retire its ownership token while this recovery
	// still holds its claim, in the record machinery's atomic write discipline
	// (preconditioned on the fence still naming that attempt and helperInstalled
	// not having converged). The paused owner then finds the token retired at its
	// own pre-delivery revalidation and refuses; the record it returns is the
	// record as it stands, so a concurrent finalize replays as provisioned here
	// rather than being reported as a stale fenced posture.
	retired, err := req.Store.InvalidateAttemptFence(req.Host, req.Identity, attempt, token)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	if retired.Provisioned() {
		return BootstrapOutcome{Kind: BootstrapProvisioned, Provisioning: retired}, nil
	}
	return BootstrapOutcome{Kind: BootstrapFenced, Provisioning: retired}, nil
}

// errNoDeliveryRunner reports a Bootstrap request with no remote runner: a
// caller/configuration error, never a helper-absent refusal.
var errNoDeliveryRunner = errors.New("hostfence: bootstrap delivery needs a remote runner")

// runDelivery executes the one exempt delivery step through the remote runner.
// It is a single remote command: decode the embedded helper bytes to a temp
// path, make it executable, and move it into place atomically. A non-zero exit
// is the command's own failure and leaves the attempt fence standing.
func runDelivery(ctx context.Context, req BootstrapRequest) error {
	if req.Runner == nil {
		return errNoDeliveryRunner
	}
	command, err := deliveryCommand(Wrapper{Path: req.Path}.remotePath())
	if err != nil {
		return err
	}
	_, stderr, exit, err := req.Runner.Run(ctx, command)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("hostfence: the helper delivery on host %q exited %d: %s", req.Host, exit, strings.TrimSpace(stderr))
	}
	return nil
}

// DeliveryCommand builds the one exempt delivery step's remote command: it
// writes the embedded helper bytes (HelperScript) to the pinned install path
// `~/.local/share/evener/fence` via a temp file and an atomic rename, mode 0700.
// S17 recorded the contract: "S21's bootstrap ships these bytes to the host and
// converges helperInstalled" (helper.go). The bytes cross the command line
// base64-encoded and single-quoted, so the script can never become shell syntax;
// the remote must provide a POSIX shell, `base64`, and `mktemp`, and a remote
// that cannot run the helper at all is §6's absent class. The temporary file is
// created by `mktemp` (exclusive, unpredictable) and only that path is written,
// chmodded, and renamed, so a predictable name can never be pre-created as a
// symlink and followed. The encoded bytes travel as one command-line argument,
// so the helper script's growth must stay under the platform's single-argument
// bound (128 KiB on Linux); a script past that bound needs a chunked transfer,
// which this one-step delivery does not have.
func DeliveryCommand() (string, error) { return deliveryCommand(HelperRemotePath) }

// deliveryCommand builds the delivery for one already-quoted remote path.
func deliveryCommand(remotePath string) (string, error) {
	script := HelperScript()
	if len(script) == 0 {
		return "", errors.New("hostfence: the embedded helper script is empty")
	}
	encoded := base64.StdEncoding.EncodeToString(script)
	// The template is the quoted path plus an unquoted suffix: shell
	// concatenation keeps a caller-supplied quoted override intact while the
	// XXXX characters stay literal for mktemp.
	tmpTemplate := remotePath + ".tmp.XXXXXX"
	return fmt.Sprintf(
		`umask 077; d="$(dirname %s)"; mkdir -p "$d" && t="$(mktemp %s)" && printf '%%s' %s | base64 -d > "$t" && chmod 700 "$t" && mv -f "$t" %s`,
		remotePath, tmpTemplate, shellQuote(encoded), remotePath,
	), nil
}

// helperAbsentRefusal is §8's typed `fencing-helper-absent` for this surface:
// helper absent, a remote unable to run the helper, and a lost or unverifiable
// bootstrap-guard claim all ride it, and it is never `probe-failed`.
func helperAbsentRefusal(host, detail string) error {
	gate := &HelperGateError{Host: host, Discriminator: DiscriminatorHelperAbsent, PinnedVersion: HelperVersion}
	return fmt.Errorf("%w (%s)", gate, detail)
}

// helperGateRefusal reports whether err already is the helper gate's own typed
// refusal, which a caller passes through rather than wraps again.
func helperGateRefusal(err error) bool {
	_, ok := errors.AsType[*HelperGateError](err)
	return ok
}

// describeClaimObservation renders a non-bare claim report factually for the
// absent refusal's detail: a lost claim race names the other claimant, foreign
// presence names what is live.
func describeClaimObservation(report QuiesceReport) string {
	switch {
	case !report.Claimed:
		return "another attempt holds the host's bootstrap claim (a lost claim race)"
	case len(report.ForeignProcesses) > 0:
		return "a foreign process is live outside the claim: " + strings.Join(report.ForeignProcesses, ", ")
	default:
		return "a foreign guard holder is live: " + strings.Join(report.ForeignGuardHolders, ", ")
	}
}
