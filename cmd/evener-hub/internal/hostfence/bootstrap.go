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
	// HelperInstalled is §6:137's converged flag: the delivery finalized and the
	// pinned helper lives on the host.
	HelperInstalled bool
	// HelperVersion is the version the finalize recorded (§6:131's "prior helper
	// version record"). Zero means no version record.
	HelperVersion uint64
}

// Provisioned reports whether the host carries the converged helperInstalled
// flag: the ordinary fenced path, with no bootstrap step at all.
func (p Provisioning) Provisioned() bool { return p.HelperInstalled }

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
	// PersistAttemptFence writes the durable bootstrap-attempt fence in its own
	// atomic hub.toml write, before the attempt's first remote side effect.
	PersistAttemptFence(host string) (Provisioning, error)
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

// ClaimQuiesce is §6:135's "pre-existing trusted host-side primitive": one
// atomic claim-plus-quiesce naming this controller's fencing epoch. Where no
// such primitive exists delivery is unavailable — nil is that "unavailable",
// never a fallback to an ordinary-SSH claim-then-check, whose check cannot
// cover processes starting mid-delivery.
type ClaimQuiesce interface {
	ClaimAndQuiesce(ctx context.Context, epoch Epoch) (QuiesceReport, error)
}

// AttemptProbe is §6:139's read-only recovery re-probe: it answers whether any
// process the crashed bootstrap attempt start is still live. It is read-only;
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
// write before the first remote side effect (§6:133), delivers only through the
// atomic claim-plus-quiesce (§6:135), self-tests the delivered helper through
// §6's verified-handle gate, and converges helperInstalled with the pinned
// version in the finalizing write (§6:137) — or refuses finalize on any failure.
// A host carrying the attempt fence without helperInstalled takes the recovery
// path (§6:139): a read-only re-probe first, and the typed `fencing-helper-absent`
// when that verification is unavailable.
//
// Nothing here auto-installs out of band, migrates in band, or degrades the
// exemption to an overwrite. Every refusal the helper gate owns is typed; the
// caller maps it onto §8's conflict-class envelope.
func Bootstrap(ctx context.Context, req BootstrapRequest) (BootstrapOutcome, error) {
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
	fenced, err := req.Store.PersistAttemptFence(req.Host)
	if err != nil {
		return BootstrapOutcome{}, err
	}
	if fenced.Provisioned() {
		// A concurrent finalize converged the record between the read and the
		// fence write: replay as provisioned rather than delivering a second time.
		return BootstrapOutcome{Kind: BootstrapProvisioned, Provisioning: fenced}, nil
	}

	// §6:135 — delivery is permitted only through the atomic claim-plus-quiesce.
	if req.Quiesce == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the host carries no pre-existing trusted atomic claim-plus-quiesce primitive; provision the helper out-of-band through the one-time migration path")
	}
	step("claim")
	report, err := req.Quiesce.ClaimAndQuiesce(ctx, req.Epoch)
	if err != nil {
		if ctx.Err() != nil {
			// The caller's own context ended: that is not the claim's refusal and
			// nothing about the helper is known, so the raw error stays.
			return BootstrapOutcome{}, err
		}
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the host's atomic claim-plus-quiesce primitive failed: "+err.Error())
	}
	if !report.Bare() {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, describeClaim(report))
	}

	// The one exempt delivery step: ship the deployed payload with the helper
	// bytes inside (§6:131), unfenced but under the won claim.
	step("deliver")
	if err := runDelivery(ctx, req); err != nil {
		return BootstrapOutcome{}, err
	}

	// The delivered helper must pass §6's own read-only presence/version gate
	// before the flag converges: a finalize behind a helper that cannot run the
	// pinned protocol would record a convergence that is not true.
	step("verify")
	wrapper := Wrapper{Runner: req.Runner, Host: req.Host, Path: req.Path}
	if _, err := wrapper.Check(ctx); err != nil {
		return BootstrapOutcome{}, err
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
// fence without helperInstalled: it re-probes the remote read-only and verifies
// no bootstrapped process from the crashed attempt is live before the next
// mutation. A live process refuses with AttemptOrphanError; an unavailable
// verification refuses fail-closed with the typed fencing-helper-absent, the
// class §8:162 gives the unverifiable bootstrap-guard claim. Only a verified-
// gone attempt opens the fenced path.
func recoverFencedAttempt(ctx context.Context, req BootstrapRequest, record Provisioning) (BootstrapOutcome, error) {
	if req.Probe == nil {
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host,
			"the crashed bootstrap attempt's process cannot be verified: the host carries no read-only attempt probe; provision the helper out-of-band and repair the host through the one-time migration path")
	}
	live, err := req.Probe.BootstrappedProcessLive(ctx, req.Epoch)
	if err != nil {
		if ctx.Err() != nil {
			return BootstrapOutcome{}, err
		}
		return BootstrapOutcome{}, helperAbsentRefusal(req.Host, "the crashed bootstrap attempt could not be verified: "+err.Error())
	}
	if live {
		return BootstrapOutcome{}, &AttemptOrphanError{
			Host: req.Host, Epoch: req.Epoch,
			Detail: "a bootstrapped process from the crashed attempt is live",
		}
	}
	return BootstrapOutcome{Kind: BootstrapFenced, Provisioning: record}, nil
}

// runDelivery executes the one exempt delivery step through the remote runner.
// It is a single remote command: decode the embedded helper bytes to a temp
// path, make it executable, and move it into place atomically. A non-zero exit
// is the command's own failure and leaves the attempt fence standing.
func runDelivery(ctx context.Context, req BootstrapRequest) error {
	if req.Runner == nil {
		return errors.New("hostfence: bootstrap delivery needs a remote runner")
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

// describeClaim renders a non-bare claim report for the refusal's detail.
func describeClaim(report QuiesceReport) string {
	switch {
	case !report.Claimed:
		return "the host's bootstrap claim was lost to another claimant (a lost claim race)"
	case len(report.ForeignProcesses) > 0:
		return "a foreign process is live outside the claim: " + strings.Join(report.ForeignProcesses, ", ")
	default:
		return "a foreign guard holder is live: " + strings.Join(report.ForeignGuardHolders, ", ")
	}
}
