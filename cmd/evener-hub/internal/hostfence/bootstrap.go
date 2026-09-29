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
}

// Provisioned reports whether the host carries the whole converged record: the
// attempt fence, the installed flag, and the version record its finalize wrote.
// An installed-without-fence or installed-without-version record is not
// provisioned — both are shapes no writer emits (the record loader refuses
// them), and reading one as provisioned would hide that.
func (p Provisioning) Provisioned() bool {
	return p.AttemptFenced && p.HelperInstalled && p.HelperVersion != 0
}

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
	// one persisted epoch). A record returned without the fence is refused by the
	// caller, never delivered behind.
	PersistAttemptFence(host string, epoch Epoch) (Provisioning, bool, error)
	// FinalizeBootstrap converges helperInstalled with the delivered version in
	// the same atomic hub.toml write that finalizes bootstrap (§6:137). A
	// failure refuses finalize: the attempt fence stays and helperInstalled is
	// never converged behind it.
	FinalizeBootstrap(host string, helperVersion uint64) (Provisioning, error)
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

// AttemptActiveError reports §6:135's claim arbiter finding a live attempt this
// caller did not win: another attempt owns the fence and may be mid-delivery, so
// this caller must not deliver (it lost) and must not open the fenced path
// either (the owner's remote work may still be starting or running). It rides
// the transient busy class — retry with backoff — and is deliberately neither
// `fencing-helper-absent` (the helper is not the problem) nor `probe-failed`.
type AttemptActiveError struct {
	Host   string
	Epoch  Epoch
	Detail string
}

// Error renders the refusal naming the host and the attempt epoch.
func (e *AttemptActiveError) Error() string {
	detail := e.Detail
	if detail == "" {
		detail = "another attempt holds the host's bootstrap claim"
	}
	return fmt.Sprintf("host %q: bootstrap attempt %s/%d is active: %s; retry once it completes",
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
	fenced, won, err := req.Store.PersistAttemptFence(req.Host, req.Epoch)
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
	if !report.Claimed {
		// The claim arbiter found another claimant: an attempt is active on the
		// host and this one lost. The helper is not absent — reporting the absent
		// class here would tell the operator to provision out-of-band while
		// another attempt is mid-flight — so the honest class is the transient
		// busy one (retry with backoff).
		return BootstrapOutcome{}, &AttemptActiveError{
			Host: req.Host, Epoch: req.Epoch,
			Detail: "another attempt holds the host's bootstrap claim (a lost claim race)",
		}
	}
	if len(report.ForeignProcesses) > 0 || len(report.ForeignGuardHolders) > 0 {
		// §6:135: any live foreign presence refuses fail-closed with the typed
		// absent class, and the exemption never degrades to overwrite.
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, describeForeignPresence(report))
	}
	if claim == nil {
		// A winning report with no held claim is a point-in-time answer, not the
		// quiesce §6:135 requires to hold for the entire delivery: refuse before
		// any delivery rather than deliver outside a hold.
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host's claim-plus-quiesce primitive returned no held claim, so the delivery could not run under one")
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
	finalized, err := req.Store.FinalizeBootstrap(req.Host, HelperVersion)
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
//     an attempt is active or foreign work lives: refuse with AttemptActiveError
//     (the transient busy class), never a clean fenced/provisioned outcome;
//  4. holding the won claim across the read-only re-probe and the decision,
//     probes the epoch the fence persisted, and releases on every path.
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
		// Not bare means either another claimant holds the claim (an active
		// attempt) or foreign work lives. Recovery must not conclude while an
		// attempt may be active, so it refuses with the honest busy class rather
		// than reporting a fenced posture.
		detail := "an attempt holds the host's bootstrap claim"
		if report.Claimed {
			detail = describeForeignPresence(report)
		}
		return BootstrapOutcome{}, &AttemptActiveError{Host: req.Host, Epoch: attempt, Detail: detail}
	}
	if claim == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host's claim-plus-quiesce primitive returned no held claim, so recovery cannot exclude an active attempt")
	}
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
	// The probe answered about the remote, not the record: a concurrent finalize
	// may have converged the record meanwhile, so re-read it and replay as
	// provisioned instead of reporting a stale fenced posture.
	fresh, err := req.Store.Provisioning(req.Host)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	if fresh.Provisioned() {
		return BootstrapOutcome{Kind: BootstrapProvisioned, Provisioning: fresh}, nil
	}
	return BootstrapOutcome{Kind: BootstrapFenced, Provisioning: fresh}, nil
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
// the remote must provide a POSIX shell and `base64`, and a remote that cannot
// run the helper at all is §6's absent class. The encoded bytes travel as one
// command-line argument, so the helper script's growth must stay under the
// platform's single-argument bound (128 KiB on Linux); a script past that bound
// needs a chunked transfer, which this one-step delivery does not have.
func DeliveryCommand() (string, error) { return deliveryCommand(HelperRemotePath) }

// deliveryCommand builds the delivery for one already-quoted remote path.
func deliveryCommand(remotePath string) (string, error) {
	script := HelperScript()
	if len(script) == 0 {
		return "", errors.New("hostfence: the embedded helper script is empty")
	}
	encoded := base64.StdEncoding.EncodeToString(script)
	// The temp path is the quoted path plus an unquoted suffix: shell
	// concatenation keeps a caller-supplied quoted override intact while `$$`
	// still expands to the remote shell's pid.
	tmp := remotePath + ".tmp.$$"
	return fmt.Sprintf(
		`umask 077; mkdir -p "$(dirname %s)" && printf '%%s' %s | base64 -d > %s && chmod 700 %s && mv -f %s %s`,
		remotePath, shellQuote(encoded), tmp, tmp, tmp, remotePath,
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

// describeForeignPresence renders the foreign presence a bare-claiming report
// observed, for the absent refusal's detail.
func describeForeignPresence(report QuiesceReport) string {
	switch {
	case len(report.ForeignProcesses) > 0:
		return "a foreign process is live outside the claim: " + strings.Join(report.ForeignProcesses, ", ")
	default:
		return "a foreign guard holder is live: " + strings.Join(report.ForeignGuardHolders, ", ")
	}
}
