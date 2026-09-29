package hostfence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The bootstrap tests drive §6's first-contact fence and delivery entirely
// through the package's seams: a scripted store, a scripted claim-plus-quiesce
// primitive, a scripted remote runner, and a scripted recovery probe. No ssh
// and no host is contacted; the delivery command's bytes are executed locally
// through a POSIX shell against a temporary HOME, exactly as the script tests
// run the real helper.

// scriptedStore is a BootstrapStore over an in-memory record, with fault
// injection for the two crash windows §6:133-137 names.
type scriptedStore struct {
	record      Provisioning
	writes      []string
	persistErr  error
	finalizeErr error
	// racingFinalize makes PersistAttemptFence land a concurrent finalize's
	// converged record: the retry window §6:139 closes under dedup.
	racingFinalize bool
	// foreignFence, when set, is another attempt's fence the conditional write
	// returns instead of the caller's: the interleaving where a delayed attempt's
	// write finds the fence already owned.
	foreignFence *Provisioning
	// boundIdentity, when set, is the only registration this store accepts: any
	// other identity is refused as stale (the remove-and-re-add case).
	boundIdentity *BootstrapIdentity
	// staleOn, when set, makes the named write arm ("attempt", "revalidate",
	// "invalidate", "finalize") refuse as stale regardless of the identity: the
	// per-arm binding check.
	staleOn map[string]bool
}

// checkIdentity refuses the named arm when its binding no longer stands: either
// the store's bound registration differs, or the arm is flagged stale.
func (s *scriptedStore) checkIdentity(arm string, identity BootstrapIdentity) error {
	if s.staleOn[arm] || (s.boundIdentity != nil && identity != *s.boundIdentity) {
		return &StaleAttemptError{Host: "h1", Bound: identity, Live: BootstrapIdentity{Generation: 9, IncarnationID: "inc-new", PresenceEpoch: 9}}
	}
	return nil
}

func (s *scriptedStore) Provisioning(_ string, identity BootstrapIdentity) (Provisioning, error) {
	if err := s.checkIdentity("read", identity); err != nil {
		return Provisioning{}, err
	}
	return s.record, nil
}

// PersistAttemptFence mirrors the real store's conditional write: a record that
// already carries a fence is returned unchanged, never overwritten. foreignFence,
// when set, is the interleaving where a delayed attempt's write finds another
// attempt's fence already landed.
func (s *scriptedStore) PersistAttemptFence(_ string, identity BootstrapIdentity, epoch Epoch) (Provisioning, bool, error) {
	if err := s.checkIdentity("attempt", identity); err != nil {
		return Provisioning{}, false, err
	}
	if s.persistErr != nil {
		return Provisioning{}, false, s.persistErr
	}
	if s.foreignFence != nil {
		s.record = *s.foreignFence
		return s.record, false, nil
	}
	if s.record.AttemptFenced {
		return s.record, false, nil
	}
	s.writes = append(s.writes, "attempt")
	s.record.AttemptFenced = true
	s.record.AttemptEpoch = epoch
	s.record.AttemptToken = "tok-attempt"
	if s.racingFinalize {
		s.record.HelperInstalled = true
		s.record.HelperVersion = HelperVersion
	}
	return s.record, true, nil
}

func (s *scriptedStore) RevalidateAttemptFence(_ string, identity BootstrapIdentity, epoch Epoch, token string) (Provisioning, bool, error) {
	if err := s.checkIdentity("revalidate", identity); err != nil {
		return s.record, false, err
	}
	ok := s.record.AttemptFenced && s.record.AttemptEpoch == epoch && s.record.AttemptToken == token && !s.record.HelperInstalled
	return s.record, ok, nil
}

func (s *scriptedStore) InvalidateAttemptFence(_ string, identity BootstrapIdentity, epoch Epoch, token string) (Provisioning, error) {
	if err := s.checkIdentity("invalidate", identity); err != nil {
		return s.record, err
	}
	if s.record.AttemptFenced && s.record.AttemptEpoch == epoch && s.record.AttemptToken == token && !s.record.HelperInstalled {
		s.record.AttemptToken = ""
		s.writes = append(s.writes, "invalidate")
	}
	return s.record, nil
}

func (s *scriptedStore) FinalizeBootstrap(_ string, identity BootstrapIdentity, version uint64) (Provisioning, error) {
	if err := s.checkIdentity("finalize", identity); err != nil {
		return Provisioning{}, err
	}
	if s.finalizeErr != nil {
		return Provisioning{}, s.finalizeErr
	}
	s.writes = append(s.writes, "finalize")
	if !s.record.AttemptFenced {
		return Provisioning{}, errors.New("finalize without an attempt fence")
	}
	s.record.HelperInstalled = true
	s.record.HelperVersion = version
	return s.record, nil
}

// scriptedClaim is a held claim that records its release and, when err is set,
// fails it.
type scriptedClaim struct {
	released int
	err      error
}

func (c *scriptedClaim) Release(context.Context) error {
	c.released++
	return c.err
}

// scriptedQuiesce is the claim-plus-quiesce primitive's scripted answer. claim,
// when set, is the held claim returned; noClaim forces a nil claim (the
// primitive that can only report a point in time).
type scriptedQuiesce struct {
	report  QuiesceReport
	err     error
	claim   BootstrapClaim
	noClaim bool
	calls   int
	epoch   Epoch
}

func (q *scriptedQuiesce) ClaimAndQuiesce(_ context.Context, epoch Epoch) (QuiesceReport, BootstrapClaim, error) {
	q.calls++
	q.epoch = epoch
	if q.err != nil {
		// A primitive may answer with a claim beside its error: the flow must
		// release it (L1), so the fake can reproduce that shape.
		return QuiesceReport{}, q.claim, q.err
	}
	if q.noClaim {
		return q.report, nil, nil
	}
	if q.claim != nil {
		return q.report, q.claim, nil
	}
	return q.report, &scriptedClaim{}, nil
}

// bareClaim is the winning claim with no foreign presence.
func bareClaim() QuiesceReport { return QuiesceReport{Claimed: true} }

// bareQuiesce is a claim primitive that wins with no foreign presence and holds
// a fresh claim.
func bareQuiesce() *scriptedQuiesce { return &scriptedQuiesce{report: bareClaim()} }

// scriptedProbe is the recovery re-probe's scripted answer.
type scriptedProbe struct {
	live  bool
	err   error
	calls int
	seen  Epoch
}

func (p *scriptedProbe) BootstrappedProcessLive(_ context.Context, epoch Epoch) (bool, error) {
	p.calls++
	p.seen = epoch
	return p.live, p.err
}

// scriptedRunner records the remote commands a flow issues and answers them
// from fn.
type scriptedRunner struct {
	calls []string
	fn    func(command string) (stdout, stderr string, exit int, err error)
}

func (r *scriptedRunner) Run(_ context.Context, command string) (string, string, int, error) {
	r.calls = append(r.calls, command)
	if r.fn == nil {
		return "", "", 0, nil
	}
	return r.fn(command)
}

// versionRunner answers the helper version command with the pinned version and
// every other command with success: the delivery's own self-test.
func versionRunner() *scriptedRunner {
	return &scriptedRunner{fn: func(command string) (string, string, int, error) {
		if strings.HasSuffix(command, " version") {
			return strconv.Itoa(HelperVersion) + "\n", "", 0, nil
		}
		return "", "", 0, nil
	}}
}

// bootstrapEpoch is one valid fencing epoch for these tests.
func bootstrapEpoch() Epoch { return Epoch{BootID: "boot-1", OpSeq: 1} }

// bootstrapIdentity is the resolved registration these tests bind attempts to.
func bootstrapIdentity() BootstrapIdentity {
	return BootstrapIdentity{Generation: 1, IncarnationID: "inc-1", PresenceEpoch: 1}
}

// eligibleFacts is the never-provisioned evidence §6:131's exemption requires.
func eligibleFacts() BootstrapEvidence { return BootstrapEvidence{} }

// TestExemptDeliveryPermitted pins §6:131's exactly-once exemption against
// every evidence item the rule names: a never-provisioned host is eligible, and
// each of the attempt fence, the installed flag, a version record, a prior
// fenced epoch, and an interrupted record closes it.
func TestExemptDeliveryPermitted(t *testing.T) {
	cases := []struct {
		name string
		rec  Provisioning
		ev   BootstrapEvidence
		want bool
	}{
		{name: "never provisioned", rec: Provisioning{}, ev: BootstrapEvidence{}, want: true},
		{name: "attempt fenced", rec: Provisioning{AttemptFenced: true}, want: false},
		{name: "helper installed", rec: Provisioning{HelperInstalled: true, HelperVersion: 1}, want: false},
		{name: "installed without a version record", rec: Provisioning{HelperInstalled: true}, want: false},
		{name: "prior helper version record", rec: Provisioning{HelperVersion: 1}, want: false},
		{name: "prior fenced epoch", ev: BootstrapEvidence{FencedEpoch: true}, want: false},
		{name: "interrupted record", ev: BootstrapEvidence{Interrupted: true}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExemptDeliveryPermitted(tc.rec, tc.ev); got != tc.want {
				t.Fatalf("ExemptDeliveryPermitted(%+v, %+v) = %v, want %v", tc.rec, tc.ev, got, tc.want)
			}
		})
	}
}

// TestProvisioningPostures pins the two derived record postures: provisioned,
// and the crash posture §6:137 gives the fenced path forever.
func TestProvisioningPostures(t *testing.T) {
	if !(Provisioning{AttemptFenced: true, HelperInstalled: true, HelperVersion: HelperVersion}).Provisioned() {
		t.Error("a fully converged record does not read as provisioned")
	}
	if (Provisioning{AttemptFenced: true}).Provisioned() {
		t.Error("an attempt-fenced record reads as provisioned")
	}
	if (Provisioning{HelperInstalled: true}).Provisioned() {
		t.Error("an installed-without-version record reads as provisioned")
	}
	if (Provisioning{HelperInstalled: true, HelperVersion: HelperVersion}).Provisioned() {
		t.Error("an installed-without-fence record reads as provisioned")
	}
	fenced := Provisioning{AttemptFenced: true}
	if !fenced.FencedWithoutHelper() {
		t.Error("an attempt-fenced record without the installed flag does not read as the fenced posture")
	}
	if (Provisioning{AttemptFenced: true, HelperInstalled: true}).FencedWithoutHelper() {
		t.Error("an installed record reads as the fenced posture")
	}
}

// TestBootstrapRefusesWithoutClaimPrimitive pins §6:135's unavailable-primitive
// arm: no host-side atomic claim-plus-quiesce means delivery is unavailable and
// the refusal is the typed fencing-helper-absent — and the attempt fence was
// still persisted first, in its own write, before any remote step.
func TestBootstrapRefusesWithoutClaimPrimitive(t *testing.T) {
	store := &scriptedStore{}
	runner := &scriptedRunner{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: nil,
	})
	if outcome.Kind != BootstrapUnset {
		t.Fatalf("outcome = %+v, want zero on a refusal", outcome)
	}
	var gate *HelperGateError
	if !errors.As(err, &gate) {
		t.Fatalf("err = %v, want a *HelperGateError", err)
	}
	if gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("discriminator = %q, want %q", gate.Discriminator, DiscriminatorHelperAbsent)
	}
	if gate.PinnedVersion != HelperVersion {
		t.Fatalf("pinned version = %d, want %d", gate.PinnedVersion, HelperVersion)
	}
	if len(store.writes) != 1 || store.writes[0] != "attempt" {
		t.Fatalf("store writes = %v, want exactly [attempt] before the refusal", store.writes)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none", runner.calls)
	}
}

// TestBootstrapRefusesLostClaimRaceAndForeignPresence pins §6:135/:162's
// class: a lost claim race and any live foreign process or guard holder all
// refuse fail-closed with the typed fencing-helper-absent, whose detail names
// the observation, and none degrades to overwrite.
func TestBootstrapRefusesLostClaimRaceAndForeignPresence(t *testing.T) {
	cases := []struct {
		name    string
		report  QuiesceReport
		err     error
		wantSub string
	}{
		{name: "lost claim race", report: QuiesceReport{Claimed: false}, wantSub: "lost claim race"},
		{name: "foreign process", report: QuiesceReport{Claimed: true, ForeignProcesses: []string{"hub@h1"}}, wantSub: "hub@h1"},
		{name: "foreign guard holder", report: QuiesceReport{Claimed: true, ForeignGuardHolders: []string{"boot-9"}}, wantSub: "boot-9"},
		{name: "primitive failed", err: errors.New("claim primitive unavailable"), wantSub: "claim primitive unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &scriptedStore{}
			runner := &scriptedRunner{}
			_, err := Bootstrap(context.Background(), BootstrapRequest{
				Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
				Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: tc.report, err: tc.err},
			})
			var gate *HelperGateError
			if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
				t.Fatalf("err = %v, want a typed %s refusal", err, DiscriminatorHelperAbsent)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want the detail naming %q", err, tc.wantSub)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("remote calls = %v, want none after a refused claim", runner.calls)
			}
			if !store.record.AttemptFenced {
				t.Fatal("the attempt fence is not durable after a refused claim")
			}
			if store.record.HelperInstalled {
				t.Fatal("a refused claim converged helperInstalled")
			}
		})
	}
}

// TestBootstrapDeliverySequence pins §6:131-137's successful order: the attempt
// fence is written first, then the claim-plus-quiesce, then the delivery, then
// the helper self-test, and only then the finalizing write that converges
// helperInstalled with the pinned version.
func TestBootstrapDeliverySequence(t *testing.T) {
	store := &scriptedStore{}
	claim := &scriptedClaim{}
	quiesce := &scriptedQuiesce{report: bareClaim(), claim: claim}
	runner := versionRunner()
	var order []string
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: quiesce,
		Order: func(step string) { order = append(order, step) },
	})
	if err != nil {
		t.Fatalf("Bootstrap = %v", err)
	}
	if outcome.Kind != BootstrapDelivered {
		t.Fatalf("outcome kind = %v, want BootstrapDelivered", outcome.Kind)
	}
	if !outcome.Provisioning.HelperInstalled || outcome.Provisioning.HelperVersion != HelperVersion {
		t.Fatalf("outcome provisioning = %+v, want the converged record", outcome.Provisioning)
	}
	// The claim is held across the whole delivery and released only after the
	// finalize: §6:135 requires the quiesce to hold for the entire delivery.
	want := []string{"attempt", "claim", "deliver", "verify", "finalize", "release"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("step order = %v, want %v", order, want)
	}
	if claim.released != 1 {
		t.Fatalf("the claim was released %d times, want exactly 1", claim.released)
	}
	// The delivery command ships the embedded helper bytes and the self-test
	// runs the pinned version command.
	if len(runner.calls) != 2 {
		t.Fatalf("remote calls = %v, want the delivery plus the self-test", runner.calls)
	}
	if !strings.Contains(runner.calls[0], "base64") {
		t.Fatalf("delivery command = %q, want the byte-shipping command", runner.calls[0])
	}
	if !strings.HasSuffix(runner.calls[1], " version") {
		t.Fatalf("self-test command = %q, want the version round trip", runner.calls[1])
	}
}

// TestBootstrapRefusesFinalizeUntilHelperVerifies pins the delivery->finalize
// rule: a delivered helper that cannot report the pinned version refuses
// finalize (no helperInstalled convergence) with the typed gate refusal.
func TestBootstrapRefusesFinalizeUntilHelperVerifies(t *testing.T) {
	store := &scriptedStore{}
	runner := &scriptedRunner{fn: func(command string) (string, string, int, error) {
		if strings.HasSuffix(command, " version") {
			return "99\n", "", 0, nil
		}
		return "", "", 0, nil
	}}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: bareClaim()},
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperUntrusted {
		t.Fatalf("err = %v, want a typed %s refusal", err, DiscriminatorHelperUntrusted)
	}
	if store.record.HelperInstalled {
		t.Fatal("finalize converged helperInstalled behind an untrusted helper")
	}
	if !store.record.AttemptFenced {
		t.Fatal("the attempt fence is not durable after a refused finalize")
	}
}

// TestBootstrapCrashWindowBeforeFirstSideEffect pins the first crash window:
// the attempt fence lands in its own write before any remote side effect, so a
// crash there leaves the host attempt-fenced and a later attempt takes the
// fenced recovery path — never a second unfenced delivery.
func TestBootstrapCrashWindowBeforeFirstSideEffect(t *testing.T) {
	store := &scriptedStore{}
	// The crash: the claim primitive never answers (the process died after the
	// fence write).
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: &scriptedRunner{}, Quiesce: &scriptedQuiesce{err: errors.New("crash")},
	})
	if err == nil {
		t.Fatal("Bootstrap = nil error, want the refused claim")
	}
	if !store.record.AttemptFenced || store.record.HelperInstalled {
		t.Fatalf("record after the crash = %+v, want attempt-fenced without helperInstalled", store.record)
	}
	// The retry: the record now carries the attempt fence, so the exemption is
	// closed. Recovery re-probes; no process from the crashed attempt is live,
	// so the fenced path opens — and the runner and claim primitive are never
	// touched again.
	probe := &scriptedProbe{}
	retryRunner := &scriptedRunner{}
	retryQuiesce := &scriptedQuiesce{report: bareClaim()}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: retryRunner, Quiesce: retryQuiesce, Probe: probe,
	})
	if err != nil {
		t.Fatalf("recovery Bootstrap = %v", err)
	}
	if outcome.Kind != BootstrapFenced {
		t.Fatalf("recovery outcome = %v, want BootstrapFenced", outcome.Kind)
	}
	if probe.calls != 1 {
		t.Fatalf("recovery probes = %d, want 1", probe.calls)
	}
	if retryQuiesce.calls != 1 || len(retryRunner.calls) != 0 {
		t.Fatalf("recovery claims once and never delivers: quiesce=%d runner=%v", retryQuiesce.calls, retryRunner.calls)
	}
	attempts := 0
	for _, write := range store.writes {
		if write == "attempt" {
			attempts++
		}
	}
	if attempts != 1 {
		t.Fatalf("store writes = %v, want exactly one attempt fence", store.writes)
	}
}

// TestBootstrapCrashWindowAfterDelivery pins the second crash window: the
// delivery's side effect landed, the finalize did not, and the record stays
// attempt-fenced without helperInstalled. Recovery re-probes before the next
// mutation; a live process refuses, and an unavailable probe refuses with the
// absent class (the bootstrap guard is unverifiable).
func TestBootstrapCrashWindowAfterDelivery(t *testing.T) {
	store := &scriptedStore{finalizeErr: errors.New("crash before the finalizing write")}
	runner := versionRunner()
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: bareClaim()},
	})
	if err == nil {
		t.Fatal("Bootstrap = nil error, want the refused finalize")
	}
	if !store.record.AttemptFenced || store.record.HelperInstalled {
		t.Fatalf("record after the crash = %+v, want attempt-fenced without helperInstalled", store.record)
	}
	if len(runner.calls) == 0 {
		t.Fatal("the delivery did not run before the crash window")
	}
	// Recovery with a live bootstrapped process: refuse, never mutate.
	live := &scriptedProbe{live: true}
	_, err = Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: live,
	})
	if _, ok := errors.AsType[*AttemptOrphanError](err); !ok {
		t.Fatalf("err = %v, want an *AttemptOrphanError for the live crashed-attempt process", err)
	}
	// Recovery with a claim but no probe primitive: unverifiable, refused as
	// absent.
	_, err = Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: nil,
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal for the unverifiable crashed attempt", err, DiscriminatorHelperAbsent)
	}
	// Recovery with the process confirmed gone: the fenced path opens.
	clearProbe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: clearProbe,
	})
	if err != nil || outcome.Kind != BootstrapFenced {
		t.Fatalf("clear recovery = (%v, %v), want BootstrapFenced", outcome.Kind, err)
	}
}

// TestBootstrapRetryRacingFinalizeReplaysUnderDedup pins §6:139's dedup rule: a
// retry that races the finalize observes the converged record and replays as
// provisioned instead of running a second delivery.
func TestBootstrapRetryRacingFinalizeReplaysUnderDedup(t *testing.T) {
	store := &scriptedStore{racingFinalize: true}
	runner := versionRunner()
	quiesce := &scriptedQuiesce{report: bareClaim()}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: quiesce,
	})
	if err != nil {
		t.Fatalf("Bootstrap = %v", err)
	}
	if outcome.Kind != BootstrapProvisioned {
		t.Fatalf("outcome = %v, want BootstrapProvisioned (the racing finalize won)", outcome.Kind)
	}
	if quiesce.calls != 0 || len(runner.calls) != 0 {
		t.Fatalf("the deduped retry touched the remote: quiesce=%d runner=%v", quiesce.calls, runner.calls)
	}
	if len(store.writes) != 1 || store.writes[0] != "attempt" {
		t.Fatalf("store writes = %v, want only the attempt fence", store.writes)
	}
}

// TestBootstrapSkipsDeliveryForPriorEvidence pins §6:131's exclusion list: a
// host with a prior fenced epoch or an interrupted record is never a
// first-contact host, so no attempt fence is written and no delivery runs.
func TestBootstrapSkipsDeliveryForPriorEvidence(t *testing.T) {
	cases := []struct {
		name string
		ev   BootstrapEvidence
		rec  Provisioning
	}{
		{name: "prior fenced epoch", ev: BootstrapEvidence{FencedEpoch: true}},
		{name: "interrupted record", ev: BootstrapEvidence{Interrupted: true}},
		{name: "prior helper version record", rec: Provisioning{HelperVersion: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &scriptedStore{record: tc.rec}
			runner := &scriptedRunner{}
			outcome, err := Bootstrap(context.Background(), BootstrapRequest{
				Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: tc.ev,
				Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: bareClaim()},
			})
			if err != nil || outcome.Kind != BootstrapFenced {
				t.Fatalf("Bootstrap = (%v, %v), want the fenced path", outcome.Kind, err)
			}
			if len(store.writes) != 0 || len(runner.calls) != 0 {
				t.Fatalf("a non-exempt host was touched: writes=%v calls=%v", store.writes, runner.calls)
			}
		})
	}
}

// TestBootstrapAlreadyProvisioned pins the ordinary path: a converged record
// performs no bootstrap step at all.
func TestBootstrapAlreadyProvisioned(t *testing.T) {
	store := &scriptedStore{record: Provisioning{AttemptFenced: true, HelperInstalled: true, HelperVersion: HelperVersion}}
	runner := &scriptedRunner{}
	quiesce := &scriptedQuiesce{report: bareClaim()}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: runner, Quiesce: quiesce,
	})
	if err != nil || outcome.Kind != BootstrapProvisioned {
		t.Fatalf("Bootstrap = (%v, %v), want BootstrapProvisioned", outcome.Kind, err)
	}
	if len(store.writes) != 0 || quiesce.calls != 0 || len(runner.calls) != 0 {
		t.Fatalf("a provisioned host was touched: writes=%v quiesce=%d calls=%v", store.writes, quiesce.calls, runner.calls)
	}
}

// TestBootstrapCancelledClaimStaysRaw pins the caller-context rule: a claim
// that ends because the caller's own context ended is the caller's error, not a
// fencing-helper-absent refusal.
func TestBootstrapCancelledClaimStaysRaw(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &scriptedStore{}
	_, err := Bootstrap(ctx, BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: &scriptedRunner{},
		Quiesce: &scriptedQuiesce{err: context.Canceled},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if _, ok := errors.AsType[*HelperGateError](err); ok {
		t.Fatalf("err = %v, want the raw cancellation, not a gate refusal", err)
	}
}

// TestBootstrapValidatesRequest pins the request schema: an absent store, host,
// or epoch is refused before any write.
func TestBootstrapValidatesRequest(t *testing.T) {
	cases := []struct {
		name string
		req  BootstrapRequest
	}{
		{name: "no store", req: BootstrapRequest{Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch()}},
		{name: "no host", req: BootstrapRequest{Epoch: bootstrapEpoch(), Store: &scriptedStore{}}},
		{name: "no epoch", req: BootstrapRequest{Host: "h1", Identity: bootstrapIdentity(), Store: &scriptedStore{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Bootstrap(context.Background(), tc.req); err == nil {
				t.Fatal("Bootstrap = nil error, want a validation refusal")
			}
		})
	}
}

// TestDeliveryCommandInstallsTheEmbeddedHelper executes the delivery command
// through a POSIX shell against a temporary HOME and verifies the installed
// bytes answer the pinned version: the one exempt delivery step really ships
// HelperScript().
func TestDeliveryCommandInstallsTheEmbeddedHelper(t *testing.T) {
	home := t.TempDir()
	command, err := DeliveryCommand()
	if err != nil {
		t.Fatalf("DeliveryCommand = %v", err)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("delivery command failed: %v\n%s", err, out)
	}
	// Derive the expected path from the constant the delivery uses, so a changed
	// install path cannot leave the test asserting a stale one.
	installed := filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(HelperInstallPath, "~/")))
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("installed helper: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed helper mode = %v, want executable", info.Mode())
	}
	versionCmd := exec.Command("sh", "-c", "'"+installed+"' version")
	versionOut, err := versionCmd.Output()
	if err != nil {
		t.Fatalf("installed helper version: %v", err)
	}
	if got := strings.TrimSpace(string(versionOut)); got != strconv.Itoa(HelperVersion) {
		t.Fatalf("installed helper reports version %q, want 1", got)
	}
}

// TestBootstrapRefusesAnUnheldClaim pins §6:135's hold: a primitive that wins
// the claim but returns no held claim has only answered a point in time, so the
// flow refuses before any delivery.
func TestBootstrapRefusesAnUnheldClaim(t *testing.T) {
	store := &scriptedStore{}
	runner := &scriptedRunner{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: bareClaim(), noClaim: true},
	})
	if _, ok := errors.AsType[*HelperGateError](err); !ok {
		t.Fatalf("err = %v, want a typed fencing-helper-absent refusal for the unheld claim", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none without a held claim", runner.calls)
	}
	if !store.record.AttemptFenced {
		t.Fatal("the attempt fence is not durable after the refused claim")
	}
}

// unlandedFenceStore answers PersistAttemptFence without landing the fence: the
// store the flow must refuse before any delivery.
type unlandedFenceStore struct{ scriptedStore }

func (s *unlandedFenceStore) PersistAttemptFence(string, BootstrapIdentity, Epoch) (Provisioning, bool, error) {
	return Provisioning{}, false, nil
}

func (s *unlandedFenceStore) RevalidateAttemptFence(string, BootstrapIdentity, Epoch, string) (Provisioning, bool, error) {
	return Provisioning{}, false, nil
}

func (s *unlandedFenceStore) InvalidateAttemptFence(string, BootstrapIdentity, Epoch, string) (Provisioning, error) {
	return Provisioning{}, nil
}

// TestBootstrapRefusesAFenceThatDidNotLand pins §6:133's ordering: a store that
// answers without the fence cannot be delivered behind, because the delivery
// would then run unfenced.
func TestBootstrapRefusesAFenceThatDidNotLand(t *testing.T) {
	store := &unlandedFenceStore{}
	runner := &scriptedRunner{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: bareClaim()},
	})
	if err == nil || !strings.Contains(err.Error(), "returned without the fence") {
		t.Fatalf("err = %v, want the fence-did-not-land refusal", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none behind an unlanded fence", runner.calls)
	}
}

// TestBootstrapRecoveryProbesThePersistedAttemptEpoch pins §6:139's identity:
// after a crash and a restart the recovery probe names the epoch the attempt
// fence persisted — the crashed attempt's own epoch — never the new attempt's.
func TestBootstrapRecoveryProbesThePersistedAttemptEpoch(t *testing.T) {
	store := &scriptedStore{}
	crashed := Epoch{BootID: "boot-old", OpSeq: 7}
	if _, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: crashed, Evidence: eligibleFacts(),
		Store: store, Runner: &scriptedRunner{}, Quiesce: &scriptedQuiesce{err: errors.New("crash")},
	}); err == nil {
		t.Fatal("Bootstrap = nil error, want the crashed claim")
	}
	// A later restart mints a different epoch; recovery must still name the
	// crashed attempt.
	probe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: Epoch{BootID: "boot-new", OpSeq: 3},
		Store: store, Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: probe,
	})
	if err != nil || outcome.Kind != BootstrapFenced {
		t.Fatalf("recovery = (%v, %v), want the fenced path", outcome.Kind, err)
	}
	if probe.seen != crashed {
		t.Fatalf("recovery probe saw epoch %+v, want the persisted crashed attempt %+v", probe.seen, crashed)
	}
}

// TestBootstrapRecoveryRefusesAnUnidentifiableAttempt pins the fail-closed arm:
// an attempt fence with no epoch cannot name the crashed attempt, so recovery
// refuses with the typed fencing-helper-absent and probes nothing.
func TestBootstrapRecoveryRefusesAnUnidentifiableAttempt(t *testing.T) {
	store := &scriptedStore{record: Provisioning{AttemptFenced: true}}
	probe := &scriptedProbe{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Probe: probe,
	})
	if _, ok := errors.AsType[*HelperGateError](err); !ok {
		t.Fatalf("err = %v, want a typed fencing-helper-absent refusal for the unidentifiable attempt", err)
	}
	if probe.calls != 0 {
		t.Fatalf("recovery probes = %d, want 0 for an unidentifiable attempt", probe.calls)
	}
}

// TestBootstrapDoesNotDeliverWhenAnotherAttemptOwnsTheFence pins the
// compare-and-set fence: a delayed attempt whose conditional write finds
// another attempt's fence must neither clobber it nor deliver a second time; it
// takes the recovery path naming the fence owner's epoch.
func TestBootstrapDoesNotDeliverWhenAnotherAttemptOwnsTheFence(t *testing.T) {
	owner := Provisioning{AttemptFenced: true, AttemptEpoch: Epoch{BootID: "boot-a", OpSeq: 5}}
	store := &scriptedStore{foreignFence: &owner}
	runner := &scriptedRunner{}
	quiesce := &scriptedQuiesce{report: bareClaim()}
	probe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: Epoch{BootID: "boot-b", OpSeq: 9}, Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: quiesce, Probe: probe,
	})
	if err != nil || outcome.Kind != BootstrapFenced {
		t.Fatalf("delayed attempt = (%v, %v), want the fenced recovery path", outcome.Kind, err)
	}
	if quiesce.calls != 1 || len(runner.calls) != 0 {
		t.Fatalf("recovery claims once (the arbiter) and never delivers: quiesce=%d runner=%v", quiesce.calls, runner.calls)
	}
	if probe.seen != owner.AttemptEpoch {
		t.Fatalf("recovery probed %+v, want the fence owner's epoch %+v", probe.seen, owner.AttemptEpoch)
	}
	if store.record.AttemptEpoch != owner.AttemptEpoch {
		t.Fatalf("the fence was clobbered to %+v, want the owner's %+v", store.record.AttemptEpoch, owner.AttemptEpoch)
	}
}

// TestBootstrapRecoveryNamesThePersistedEpochInTheOrphan pins that a live
// refusal names the crashed attempt's persisted epoch, not the request's.

func TestBootstrapRecoveryNamesThePersistedEpochInTheOrphan(t *testing.T) {
	store := &scriptedStore{}
	crashed := Epoch{BootID: "boot-old", OpSeq: 7}
	if _, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: crashed, Evidence: eligibleFacts(),
		Store: store, Runner: &scriptedRunner{}, Quiesce: &scriptedQuiesce{err: errors.New("crash")},
	}); err == nil {
		t.Fatal("Bootstrap = nil error, want the crashed claim")
	}
	probe := &scriptedProbe{live: true}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: Epoch{BootID: "boot-new", OpSeq: 3},
		Store: store, Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: probe,
	})
	orphan, ok := errors.AsType[*AttemptOrphanError](err)
	if !ok {
		t.Fatalf("err = %v, want an AttemptOrphanError", err)
	}
	if orphan.Epoch != crashed {
		t.Fatalf("orphan epoch = %+v, want the persisted crashed attempt %+v", orphan.Epoch, crashed)
	}
}

// TestBootstrapReleasesTheClaimOnANonBareRefusal pins that a won claim is given
// back when foreign presence refuses the delivery: the refusal must not leak the
// hold it took.
func TestBootstrapReleasesTheClaimOnANonBareRefusal(t *testing.T) {
	store := &scriptedStore{}
	claim := &scriptedClaim{}
	runner := &scriptedRunner{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner,
		Quiesce: &scriptedQuiesce{report: QuiesceReport{Claimed: true, ForeignProcesses: []string{"hub@h1"}}, claim: claim},
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal", err, DiscriminatorHelperAbsent)
	}
	if claim.released != 1 {
		t.Fatalf("the won claim was released %d times on a non-bare refusal, want exactly 1", claim.released)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none", runner.calls)
	}
}

// TestBootstrapSurfacesAReleaseFailure pins that a claim that could not be
// released is never silent: it reaches the caller on an otherwise successful
// path, is joined with a primary failure, and goes to the log sink.
func TestBootstrapSurfacesAReleaseFailure(t *testing.T) {
	// (a) the delivery succeeded, but the release failed.
	claim := &scriptedClaim{err: errors.New("release failed")}
	var logs []string
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: &scriptedStore{}, Runner: versionRunner(), Quiesce: &scriptedQuiesce{report: bareClaim(), claim: claim},
		Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	})
	if err == nil || !strings.Contains(err.Error(), "release failed") {
		t.Fatalf("err = %v, want the release failure surfaced", err)
	}
	if outcome.Kind != BootstrapDelivered {
		t.Fatalf("outcome kind = %v, want BootstrapDelivered", outcome.Kind)
	}
	if outcome.ReleaseErr == nil {
		t.Fatal("outcome.ReleaseErr = nil, want the release failure")
	}
	if len(logs) == 0 {
		t.Fatal("the release failure did not reach the log sink")
	}

	// (b) a primary failure plus a release failure: both stay visible, and the
	// primary gate refusal stays matchable.
	claim2 := &scriptedClaim{err: errors.New("release failed again")}
	_, err = Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: &scriptedStore{}, Runner: &scriptedRunner{}, Quiesce: &scriptedQuiesce{report: bareClaim(), claim: claim2},
	})
	if err == nil || !strings.Contains(err.Error(), "release failed again") {
		t.Fatalf("err = %v, want the release failure joined with the primary failure", err)
	}
	if _, ok := errors.AsType[*HelperGateError](err); !ok {
		t.Fatalf("err = %v, want the primary gate refusal still matchable", err)
	}
}

// convergingStore answers the recovery invalidation with a converged record: a
// concurrent finalize landing while recovery invalidates.
type convergingStore struct {
	*scriptedStore
}

func (s *convergingStore) InvalidateAttemptFence(string, BootstrapIdentity, Epoch, string) (Provisioning, error) {
	return Provisioning{
		AttemptFenced: true, AttemptEpoch: s.record.AttemptEpoch,
		HelperInstalled: true, HelperVersion: HelperVersion,
	}, nil
}

// TestBootstrapDoesNotDeliverWhenTwoAttemptsShareTheEpoch pins explicit fence
// ownership: a call that lost the conditional write must not deliver even when
// the owner's epoch equals its own (a replay of one operation), so ownership can
// never be inferred from epoch equality.
func TestBootstrapDoesNotDeliverWhenTwoAttemptsShareTheEpoch(t *testing.T) {
	epoch := bootstrapEpoch()
	owner := Provisioning{AttemptFenced: true, AttemptEpoch: epoch}
	store := &scriptedStore{foreignFence: &owner}
	runner := &scriptedRunner{}
	quiesce := &scriptedQuiesce{report: bareClaim()}
	probe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: epoch, Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: quiesce, Probe: probe,
	})
	if err != nil || outcome.Kind != BootstrapFenced {
		t.Fatalf("same-epoch loser = (%v, %v), want the fenced recovery path", outcome.Kind, err)
	}
	if quiesce.calls != 1 || len(runner.calls) != 0 {
		t.Fatalf("recovery claims once (the arbiter) and never delivers: quiesce=%d runner=%v", quiesce.calls, runner.calls)
	}
}

// TestBootstrapReleasesTheClaimOnAClaimBesideAnError pins that a primitive
// returning claim+error cannot leak the host-side claim: the release is
// registered as soon as the claim is observed.
func TestBootstrapReleasesTheClaimOnAClaimBesideAnError(t *testing.T) {
	claim := &scriptedClaim{}
	store := &scriptedStore{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: &scriptedRunner{},
		Quiesce: &scriptedQuiesce{claim: claim, err: errors.New("claim primitive failed")},
	})
	if _, ok := errors.AsType[*HelperGateError](err); !ok {
		t.Fatalf("err = %v, want a typed fencing-helper-absent refusal", err)
	}
	if claim.released != 1 {
		t.Fatalf("the claim returned beside an error was released %d times, want exactly 1", claim.released)
	}
}

// TestBootstrapRecoveryReplaysWhenTheRecordConverged pins the recovery re-read:
// a concurrent finalize that lands while the re-probe runs makes recovery replay
// as provisioned instead of reporting a stale fenced posture.
func TestBootstrapRecoveryReplaysWhenTheRecordConverged(t *testing.T) {
	base := &scriptedStore{record: Provisioning{AttemptFenced: true, AttemptEpoch: Epoch{BootID: "boot-old", OpSeq: 7}}}
	store := &convergingStore{scriptedStore: base}
	probe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: probe,
	})
	if err != nil {
		t.Fatalf("recovery = %v", err)
	}
	if outcome.Kind != BootstrapProvisioned {
		t.Fatalf("outcome = %v, want BootstrapProvisioned from the converged re-read", outcome.Kind)
	}
	if outcome.Provisioning.HelperVersion != HelperVersion {
		t.Fatalf("outcome record = %+v, want the converged record", outcome.Provisioning)
	}
}

// TestBootstrapRecoveryRefusesWhileAnAttemptIsActive pins the recovery arbiter's
// losing arm: when the claim is already held (an attempt is active), recovery
// refuses with the typed absent class — never a fenced posture — and its probe
// never runs.
func TestBootstrapRecoveryRefusesWhileAnAttemptIsActive(t *testing.T) {
	store := &scriptedStore{record: Provisioning{AttemptFenced: true, AttemptEpoch: Epoch{BootID: "boot-old", OpSeq: 7}}}
	probe := &scriptedProbe{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{},
		Quiesce: &scriptedQuiesce{report: QuiesceReport{Claimed: false}}, Probe: probe,
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal while the owner is active", err, DiscriminatorHelperAbsent)
	}
	if !strings.Contains(err.Error(), "lost claim race") {
		t.Fatalf("err = %v, want the lost-claim detail", err)
	}
	if probe.calls != 0 {
		t.Fatalf("recovery probed %d times while an attempt is active, want 0", probe.calls)
	}
}

// TestBootstrapRecoveryRefusesOnForeignPresence pins the recovery arbiter's
// non-bare arm: foreign work live on the host keeps recovery in the typed absent
// class rather than concluding.
func TestBootstrapRecoveryRefusesOnForeignPresence(t *testing.T) {
	store := &scriptedStore{record: Provisioning{AttemptFenced: true, AttemptEpoch: Epoch{BootID: "boot-old", OpSeq: 7}}}
	probe := &scriptedProbe{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{},
		Quiesce: &scriptedQuiesce{report: QuiesceReport{Claimed: true, ForeignProcesses: []string{"hub@h1"}}}, Probe: probe,
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal for foreign presence during recovery", err, DiscriminatorHelperAbsent)
	}
	if probe.calls != 0 {
		t.Fatalf("recovery probed %d times with foreign presence, want 0", probe.calls)
	}
}

// TestBootstrapOwnerLosesTheClaimToARecoverer pins the owner's arm of the
// fence->claim window: the fence winner whose claim loses to a recoverer gets
// §6:135/:162's typed absent refusal (the class the pinned contract gives a lost
// claim race), and never delivers.
func TestBootstrapOwnerLosesTheClaimToARecoverer(t *testing.T) {
	store := &scriptedStore{}
	runner := &scriptedRunner{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: QuiesceReport{Claimed: false}},
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal for the owner's lost claim", err, DiscriminatorHelperAbsent)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none: the owner must not deliver after losing the claim", runner.calls)
	}
}

// TestBootstrapTypesDeliveryFailuresAsAbsent pins §6:135's "delivery
// unavailable ⇒ typed fencing-helper-absent": a transport failure and a
// non-zero delivery exit are both the absent class (a caller can classify them
// as the conflict-class refusal, never probe-failed), while the caller's own
// cancellation stays raw.
func TestBootstrapTypesDeliveryFailuresAsAbsent(t *testing.T) {
	cases := []struct {
		name  string
		fn    func(command string) (string, string, int, error)
		check func(t *testing.T, err error)
	}{
		{
			name: "transport failure",
			fn: func(string) (string, string, int, error) {
				return "", "", 0, errors.New("ssh transport died")
			},
			check: func(t *testing.T, err error) {
				gate, ok := errors.AsType[*HelperGateError](err)
				if !ok || gate.Discriminator != DiscriminatorHelperAbsent {
					t.Fatalf("err = %v, want a typed %s refusal", err, DiscriminatorHelperAbsent)
				}
				if !strings.Contains(err.Error(), "ssh transport died") {
					t.Fatalf("err = %v, want the transport cause in the detail", err)
				}
			},
		},
		{
			name: "non-zero exit",
			fn: func(string) (string, string, int, error) {
				return "", "base64: command not found", 3, nil
			},
			check: func(t *testing.T, err error) {
				gate, ok := errors.AsType[*HelperGateError](err)
				if !ok || gate.Discriminator != DiscriminatorHelperAbsent {
					t.Fatalf("err = %v, want a typed %s refusal", err, DiscriminatorHelperAbsent)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Bootstrap(context.Background(), BootstrapRequest{
				Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
				Store: &scriptedStore{}, Runner: &scriptedRunner{fn: tc.fn}, Quiesce: bareQuiesce(),
			})
			tc.check(t, err)
		})
	}

	// The caller's own cancellation is not a helper-absent refusal.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Bootstrap(ctx, BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: &scriptedStore{},
		Runner: &scriptedRunner{fn: func(string) (string, string, int, error) {
			return "", "", 0, context.Canceled
		}},
		Quiesce: bareQuiesce(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if _, ok := errors.AsType[*HelperGateError](err); ok {
		t.Fatalf("err = %v, want the raw cancellation, not a gate refusal", err)
	}
}

// TestBootstrapTypesVerifyFailuresAsAbsent pins the verify arm: a transport
// failure reading the delivered helper's version is §6:135's absent class (never
// a raw error a caller maps to probe-failed), while the gate's own untrusted
// refusal passes through with its exact class and data.
func TestBootstrapTypesVerifyFailuresAsAbsent(t *testing.T) {
	// (a) the version read fails at the verify step.
	transport := &scriptedRunner{fn: func(command string) (string, string, int, error) {
		if strings.HasSuffix(command, " version") {
			return "", "", 0, errors.New("version read failed")
		}
		return "", "", 0, nil
	}}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: &scriptedStore{}, Runner: transport, Quiesce: bareQuiesce(),
	})
	gate, ok := errors.AsType[*HelperGateError](err)
	if !ok || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal for the verify transport failure", err, DiscriminatorHelperAbsent)
	}
	if !strings.Contains(err.Error(), "version read failed") {
		t.Fatalf("err = %v, want the transport cause in the detail", err)
	}

	// (b) a mismatched reported version surfaces as the untrusted gate refusal,
	// unchanged (no double-wrap, no absent).
	untrusted := &scriptedRunner{fn: func(command string) (string, string, int, error) {
		if strings.HasSuffix(command, " version") {
			return "99\n", "", 0, nil
		}
		return "", "", 0, nil
	}}
	_, err = Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: &scriptedStore{}, Runner: untrusted, Quiesce: bareQuiesce(),
	})
	gate, ok = errors.AsType[*HelperGateError](err)
	if !ok || gate.Discriminator != DiscriminatorHelperUntrusted {
		t.Fatalf("err = %v, want the typed %s refusal for the mismatched version", err, DiscriminatorHelperUntrusted)
	}
	if gate.ObservedVersion != 99 {
		t.Fatalf("observed version = %d, want 99", gate.ObservedVersion)
	}
}

// retiredTokenStore answers the pre-delivery revalidation with "no": the
// interleaving where recovery retired this attempt's token while the owner was
// paused after winning the fence and its claim.
type retiredTokenStore struct{ *scriptedStore }

func (s *retiredTokenStore) RevalidateAttemptFence(string, BootstrapIdentity, Epoch, string) (Provisioning, bool, error) {
	return s.record, false, nil
}

// TestBootstrapDeliveryRefusesWhenTheTokenWasRetired pins the delivery gate's
// ownership revalidation: a token recovery retired means no delivery, and the
// record stays attempt-fenced.
func TestBootstrapDeliveryRefusesWhenTheTokenWasRetired(t *testing.T) {
	store := &retiredTokenStore{scriptedStore: &scriptedStore{}}
	runner := &scriptedRunner{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: store, Runner: runner, Quiesce: bareQuiesce(),
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal for the retired token", err, DiscriminatorHelperAbsent)
	}
	if !strings.Contains(err.Error(), "no longer names this attempt") {
		t.Fatalf("err = %v, want the retired-ownership detail", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none after a retired token", runner.calls)
	}
	if !store.record.AttemptFenced {
		t.Fatal("the attempt fence is not durable after the retired-token refusal")
	}
}

// parkedOwnerStore lets a test run the recoverer while the owner is paused at
// its pre-delivery revalidation: the exact interleaving the token closes.
type parkedOwnerStore struct {
	*scriptedStore
	park func()
}

func (s *parkedOwnerStore) RevalidateAttemptFence(host string, identity BootstrapIdentity, epoch Epoch, token string) (Provisioning, bool, error) {
	if s.park != nil {
		park := s.park
		s.park = nil
		park()
	}
	return s.scriptedStore.RevalidateAttemptFence(host, identity, epoch, token)
}

// TestBootstrapOwnerParkedAcrossRecoveryRefusesToDeliver pins the owner/recovery
// race end to end: the owner wins the fence and is paused before its claim; a
// recoverer claims, probes the attempt gone, retires the token, and concludes
// the fenced path; the owner then resumes, claims, revalidates, and refuses with
// no delivery — the record still attempt-fenced.
func TestBootstrapOwnerParkedAcrossRecoveryRefusesToDeliver(t *testing.T) {
	base := &scriptedStore{}
	parked := &parkedOwnerStore{scriptedStore: base}

	var recoveryErr error
	var recoveryOutcome BootstrapOutcome
	parked.park = func() {
		recoveryOutcome, recoveryErr = Bootstrap(context.Background(), BootstrapRequest{
			Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: base,
			Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: &scriptedProbe{},
		})
	}

	ownerRunner := versionRunner()
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
		Store: parked, Runner: ownerRunner, Quiesce: bareQuiesce(),
	})

	// The recoverer concluded the fenced path while the owner was paused.
	if recoveryErr != nil || recoveryOutcome.Kind != BootstrapFenced {
		t.Fatalf("recovery = (%v, %v), want BootstrapFenced", recoveryOutcome.Kind, recoveryErr)
	}
	if base.record.AttemptToken != "" {
		t.Fatalf("recovery did not retire the token: %+v", base.record)
	}
	// The owner resumed, revalidated, and refused without delivering.
	if outcome.Kind != BootstrapUnset {
		t.Fatalf("owner outcome = %v, want a refusal", outcome.Kind)
	}
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("owner err = %v, want a typed %s refusal after the token was retired", err, DiscriminatorHelperAbsent)
	}
	if !strings.Contains(err.Error(), "no longer names this attempt") {
		t.Fatalf("owner err = %v, want the retired-ownership detail", err)
	}
	if len(ownerRunner.calls) != 0 {
		t.Fatalf("owner remote calls = %v, want none: the paused owner must not deliver", ownerRunner.calls)
	}
	if !base.record.AttemptFenced || base.record.HelperInstalled {
		t.Fatalf("record after the interleaving = %+v, want attempt-fenced without helperInstalled", base.record)
	}
}

// TestDeliveryCommandUsesAnExclusiveTempFile pins the Low finding's fix: the
// delivery creates its temporary file with mktemp (exclusive and unpredictable)
// rather than a predictable name opened with `>`, so a pre-created symlink can
// never redirect the helper payload.
func TestDeliveryCommandUsesAnExclusiveTempFile(t *testing.T) {
	command, err := DeliveryCommand()
	if err != nil {
		t.Fatalf("DeliveryCommand = %v", err)
	}
	if !strings.Contains(command, "mktemp ") {
		t.Fatalf("delivery command = %q, want an exclusive mktemp temporary", command)
	}
	if strings.Contains(command, ".tmp.$$") {
		t.Fatalf("delivery command = %q, want no predictable pid-suffixed temp path", command)
	}
	if !strings.Contains(command, `> "$t"`) {
		t.Fatalf("delivery command = %q, want the write addressed at mktemp's returned path", command)
	}
}

// convergingOnClaimStore answers the entry read with the plain fenced record and
// the post-claim re-read with a converged one: the owner finalized while
// recovery waited for its claim.
type convergingOnClaimStore struct {
	*scriptedStore
	reads int
}

func (s *convergingOnClaimStore) Provisioning(host string, identity BootstrapIdentity) (Provisioning, error) {
	if err := s.checkIdentity("read", identity); err != nil {
		return Provisioning{}, err
	}
	s.reads++
	if s.reads >= 2 {
		return Provisioning{
			AttemptFenced: true, AttemptEpoch: s.record.AttemptEpoch,
			HelperInstalled: true, HelperVersion: HelperVersion,
		}, nil
	}
	return s.scriptedStore.Provisioning(host, identity)
}

// TestBootstrapRecoveryReplaysWhenTheOwnerFinalizedDuringTheClaim pins the
// pre-probe window: recovery must not probe a record the owner converged while
// recovery waited for its claim — it replays as provisioned, never an orphan or
// absent posture for an attempt that already succeeded.
func TestBootstrapRecoveryReplaysWhenTheOwnerFinalizedDuringTheClaim(t *testing.T) {
	base := &scriptedStore{record: Provisioning{
		AttemptFenced: true, AttemptEpoch: Epoch{BootID: "boot-old", OpSeq: 7}, AttemptToken: "tok-owner",
	}}
	store := &convergingOnClaimStore{scriptedStore: base}
	probe := &scriptedProbe{live: true} // a live process would otherwise refuse as an orphan
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(), Store: store,
		Runner: &scriptedRunner{}, Quiesce: bareQuiesce(), Probe: probe,
	})
	if err != nil {
		t.Fatalf("recovery = %v", err)
	}
	if outcome.Kind != BootstrapProvisioned {
		t.Fatalf("outcome = %v, want BootstrapProvisioned (the owner finalized during the claim)", outcome.Kind)
	}
	if probe.calls != 0 {
		t.Fatalf("recovery probed %d times against a converged record, want 0", probe.calls)
	}
}

// TestBootstrapRefusesAStaleIdentityOnEveryWriteArm pins the identity binding
// (round 9): an attempt bound to a registration the host no longer carries is
// refused with the typed stale-attempt error on each write arm, and the arms
// that precede a delivery never deliver.
func TestBootstrapRefusesAStaleIdentityOnEveryWriteArm(t *testing.T) {
	cases := []struct {
		name           string
		arm            string
		wantDeliveries int
		recovery       bool
	}{
		{name: "attempt fence", arm: "attempt"},
		{name: "pre-delivery revalidation", arm: "revalidate"},
		{name: "finalize", arm: "finalize", wantDeliveries: 1},
		{name: "recovery invalidation", arm: "invalidate", recovery: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &scriptedStore{staleOn: map[string]bool{tc.arm: true}}
			runner := versionRunner()
			req := BootstrapRequest{
				Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(),
				Store: store, Runner: runner, Quiesce: bareQuiesce(), Probe: &scriptedProbe{},
			}
			if tc.recovery {
				// Recovery reaches the invalidation arm on the verified-gone path.
				store.record = Provisioning{AttemptFenced: true, AttemptEpoch: bootstrapEpoch(), AttemptToken: "tok"}
				req.Evidence = BootstrapEvidence{}
			} else {
				req.Evidence = eligibleFacts()
			}
			_, err := Bootstrap(context.Background(), req)
			if _, ok := errors.AsType[*StaleAttemptError](err); !ok {
				t.Fatalf("err = %v, want a typed StaleAttemptError from the %s arm", err, tc.arm)
			}
			if !errors.Is(err, ErrStaleBootstrapAttempt) {
				t.Fatalf("err = %v, want it to unwrap to ErrStaleBootstrapAttempt", err)
			}
			deliveries := 0
			for _, call := range runner.calls {
				if !strings.HasSuffix(call, " version") {
					deliveries++
				}
			}
			if deliveries != tc.wantDeliveries {
				t.Fatalf("deliveries = %d, want %d (arm %s)", deliveries, tc.wantDeliveries, tc.arm)
			}
			if store.record.HelperInstalled {
				t.Fatalf("a stale arm converged helperInstalled: %+v", store.record)
			}
		})
	}
}

// TestBootstrapRefusesAStaleIdentityBeforeTheClaim pins round 10: an attempt
// entered with a registration the host no longer carries refuses at the entry
// read — before the claim arbiter and before the probe — so a stale attempt can
// never touch the current incarnation.
func TestBootstrapRefusesAStaleIdentityBeforeTheClaim(t *testing.T) {
	reAdded := BootstrapIdentity{Generation: 2, IncarnationID: "inc-2", PresenceEpoch: 2}
	store := &scriptedStore{
		record:        Provisioning{AttemptFenced: true, AttemptEpoch: Epoch{BootID: "boot-old", OpSeq: 7}, AttemptToken: "tok"},
		boundIdentity: &reAdded,
	}
	quiesce := &scriptedQuiesce{report: bareClaim()}
	probe := &scriptedProbe{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Identity: bootstrapIdentity(), Epoch: bootstrapEpoch(),
		Store: store, Runner: &scriptedRunner{}, Quiesce: quiesce, Probe: probe,
	})
	if _, ok := errors.AsType[*StaleAttemptError](err); !ok {
		t.Fatalf("err = %v, want a typed StaleAttemptError from the entry read", err)
	}
	if quiesce.calls != 0 {
		t.Fatalf("the claim primitive was called %d times before the identity was validated, want 0", quiesce.calls)
	}
	if probe.calls != 0 {
		t.Fatalf("the probe was called %d times before the identity was validated, want 0", probe.calls)
	}
}
