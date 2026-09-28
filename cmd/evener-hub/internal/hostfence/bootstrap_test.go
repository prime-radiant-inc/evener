package hostfence

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
}

func (s *scriptedStore) Provisioning(string) (Provisioning, error) { return s.record, nil }

func (s *scriptedStore) PersistAttemptFence(_ string, epoch Epoch) (Provisioning, error) {
	if s.persistErr != nil {
		return Provisioning{}, s.persistErr
	}
	s.writes = append(s.writes, "attempt")
	s.record.AttemptFenced = true
	s.record.AttemptEpoch = epoch
	if s.racingFinalize {
		s.record.HelperInstalled = true
		s.record.HelperVersion = HelperVersion
	}
	return s.record, nil
}

func (s *scriptedStore) FinalizeBootstrap(_ string, version uint64) (Provisioning, error) {
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

// scriptedClaim is a held claim that records its release.
type scriptedClaim struct{ released int }

func (c *scriptedClaim) Release(context.Context) error {
	c.released++
	return nil
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
		return QuiesceReport{}, nil, q.err
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
			return "1\n", "", 0, nil
		}
		return "", "", 0, nil
	}}
}

// bootstrapEpoch is one valid fencing epoch for these tests.
func bootstrapEpoch() Epoch { return Epoch{BootID: "boot-1", OpSeq: 1} }

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
	if !(Provisioning{HelperInstalled: true, HelperVersion: 1}).Provisioned() {
		t.Error("an installed record does not read as provisioned")
	}
	if (Provisioning{AttemptFenced: true}).Provisioned() {
		t.Error("an attempt-fenced record reads as provisioned")
	}
	if (Provisioning{HelperInstalled: true}).Provisioned() {
		t.Error("an installed-without-version record reads as provisioned")
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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

// TestBootstrapRefusesLostClaimRaceAndForeignPresence pins §6:135's other two
// refusal arms: a lost claim race and any live foreign process or guard holder
// each refuse fail-closed with the typed fencing-helper-absent, and never
// degrade to overwrite.
func TestBootstrapRefusesLostClaimRaceAndForeignPresence(t *testing.T) {
	cases := []struct {
		name   string
		report QuiesceReport
		err    error
	}{
		{name: "lost claim race", report: QuiesceReport{Claimed: false}},
		{name: "foreign process", report: QuiesceReport{Claimed: true, ForeignProcesses: []string{"hub@h1"}}},
		{name: "foreign guard holder", report: QuiesceReport{Claimed: true, ForeignGuardHolders: []string{"boot-9"}}},
		{name: "primitive failed", err: errors.New("claim primitive unavailable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &scriptedStore{}
			runner := &scriptedRunner{}
			_, err := Bootstrap(context.Background(), BootstrapRequest{
				Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
				Store: store, Runner: runner, Quiesce: &scriptedQuiesce{report: tc.report, err: tc.err},
			})
			var gate *HelperGateError
			if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
				t.Fatalf("err = %v, want a typed %s refusal", err, DiscriminatorHelperAbsent)
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
	if retryQuiesce.calls != 0 || len(retryRunner.calls) != 0 {
		t.Fatalf("recovery touched the remote: quiesce=%d runner=%v", retryQuiesce.calls, retryRunner.calls)
	}
	if len(store.writes) != 1 {
		t.Fatalf("store writes = %v, want no second attempt fence", store.writes)
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
		Host: "h1", Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: nil, Probe: live,
	})
	if _, ok := errors.AsType[*AttemptOrphanError](err); !ok {
		t.Fatalf("err = %v, want an *AttemptOrphanError for the live crashed-attempt process", err)
	}
	// Recovery with no probe primitive at all: unverifiable, refused as absent.
	_, err = Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: nil, Probe: nil,
	})
	var gate *HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal for the unverifiable crashed attempt", err, DiscriminatorHelperAbsent)
	}
	// Recovery with the process confirmed gone: the fenced path opens.
	clearProbe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Quiesce: nil, Probe: clearProbe,
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
				Host: "h1", Epoch: bootstrapEpoch(), Evidence: tc.ev,
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
		Host: "h1", Epoch: bootstrapEpoch(), Store: store, Runner: runner, Quiesce: quiesce,
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
		{name: "no store", req: BootstrapRequest{Host: "h1", Epoch: bootstrapEpoch()}},
		{name: "no host", req: BootstrapRequest{Epoch: bootstrapEpoch(), Store: &scriptedStore{}}},
		{name: "no epoch", req: BootstrapRequest{Host: "h1", Store: &scriptedStore{}}},
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
	installed := filepath.Join(home, ".local", "share", "evener", "fence")
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
	if got := strings.TrimSpace(string(versionOut)); got != "1" {
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
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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

func (s *unlandedFenceStore) PersistAttemptFence(string, Epoch) (Provisioning, error) {
	return Provisioning{}, nil
}

// TestBootstrapRefusesAFenceThatDidNotLand pins §6:133's ordering: a store that
// answers without the fence cannot be delivered behind, because the delivery
// would then run unfenced.
func TestBootstrapRefusesAFenceThatDidNotLand(t *testing.T) {
	store := &unlandedFenceStore{}
	runner := &scriptedRunner{}
	_, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Epoch: bootstrapEpoch(), Evidence: eligibleFacts(),
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
		Host: "h1", Epoch: crashed, Evidence: eligibleFacts(),
		Store: store, Runner: &scriptedRunner{}, Quiesce: &scriptedQuiesce{err: errors.New("crash")},
	}); err == nil {
		t.Fatal("Bootstrap = nil error, want the crashed claim")
	}
	// A later restart mints a different epoch; recovery must still name the
	// crashed attempt.
	probe := &scriptedProbe{}
	outcome, err := Bootstrap(context.Background(), BootstrapRequest{
		Host: "h1", Epoch: Epoch{BootID: "boot-new", OpSeq: 3},
		Store: store, Runner: &scriptedRunner{}, Probe: probe,
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
		Host: "h1", Epoch: bootstrapEpoch(), Store: store, Runner: &scriptedRunner{}, Probe: probe,
	})
	if _, ok := errors.AsType[*HelperGateError](err); !ok {
		t.Fatalf("err = %v, want a typed fencing-helper-absent refusal for the unidentifiable attempt", err)
	}
	if probe.calls != 0 {
		t.Fatalf("recovery probes = %d, want 0 for an unidentifiable attempt", probe.calls)
	}
}
