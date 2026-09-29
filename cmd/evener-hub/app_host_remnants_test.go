package hub

// Slice 12's acceptance tests (registry spec 08 §16's commit-point,
// pending-marker, remnant-gate, adoption, and teardown-repair bullets).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// failingTeardownManager is a seam whose RemoveHost/UpdateHost always refuses —
// the post-commit teardown failure the commit-point rule is about. The
// hubHostManager's manager seam is an *sshconn.Manager in production and a nil
// in tests, so the failure is injected through the manager-less path instead:
// flakyRegistry refuses Remove for the named host.
type flakyRegistry struct {
	*hostreg.Registry
	refuseRemove atomic.Bool
	removeCalls  atomic.Int64
}

func newFlakyRegistry(t *testing.T, entries []hostreg.Host) *flakyRegistry {
	t.Helper()
	reg, err := hostreg.New(entries)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	return &flakyRegistry{Registry: reg}
}

func (r *flakyRegistry) Remove(name string) error {
	r.removeCalls.Add(1)
	if r.refuseRemove.Load() {
		return errors.New("injected teardown failure")
	}
	return r.Registry.Remove(name)
}

// newRemnantFixture boots a manager whose registry refuses removals on demand,
// so a commit-point failure can be produced without a live host.
func newRemnantFixture(t *testing.T, declared ...hostreg.Host) (*hubHostManager, *flakyRegistry, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	seed := append([]hostreg.Host{{Name: "side", SSH: "side.example"}}, declared...)
	if err := writeHubTOMLHosts(configPath, seed); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts := newFlakyRegistry(t, hostRegistryEntries(cfg))
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts.Registry, func(string, ...any) {})
	// The teardown the handlers run is swapped for one the test drives: the
	// commit-point rule is about a teardown that fails after the commit landed,
	// and no live host exists here to fail on its own.
	m.testOnlyTeardown = func(_ context.Context, _ string) error {
		hosts.removeCalls.Add(1)
		if hosts.refuseRemove.Load() {
			return errors.New("injected teardown failure")
		}
		return nil
	}
	return m, hosts, configPath
}

// TestHostRemoveTeardownFailureCommitsWithRemnant pins §16's commit-point
// bullet: "a committed-with-teardown-failure persists the remnant with its
// opaque `remnantId` in the response, replaying the original `mutationId` stays
// a no-op receipt return, and `teardown-retry` completes only the named
// teardown".
func TestHostRemoveTeardownFailureCommitsWithRemnant(t *testing.T) {
	m, hosts, configPath := newRemnantFixture(t)
	hosts.refuseRemove.Store(true)
	req := removeRequest(t, m, "side")

	result, err := m.RemoveResult(context.Background(), req)
	if err != nil {
		t.Fatalf("Remove = %v, want the committed-with-teardown-failure arm", err)
	}
	arm := result.HostMutationTeardownFailureRemoved
	if arm == nil {
		t.Fatalf("Remove returned %+v, want the teardown-failure arm carrying RemovedRow", result)
	}
	if arm.Outcome != appwire.HostMutationOutcomeTeardownFailure || arm.RemnantID == "" || arm.Seam == "" {
		t.Fatalf("failure arm = %+v, want outcome/seam/remnantId", arm)
	}
	if !arm.Host.Removed || arm.Host.Name != "side" {
		t.Fatalf("failure arm row = %+v, want the removed row", arm.Host)
	}
	// The remnant is durable, and the name reads tombstoned in the store even
	// though the live registry still holds it.
	remnant, ok := m.cfg.store.remnantByID(arm.RemnantID)
	if !ok || !remnant.open() {
		t.Fatalf("remnant %q not durable after the failure: %+v", arm.RemnantID, remnant)
	}
	if remnant.Host != "side" || remnant.Generation != req.ExpectedGeneration ||
		remnant.IncarnationID != req.ExpectedIncarnationID {
		t.Fatalf("remnant = %+v, want the pinned identity", remnant)
	}
	if _, ok := m.cfg.store.tombstoneSnapshot()["side"]; !ok {
		t.Fatal("the removal's tombstone is not durable")
	}
	// The file carries the open remnant and the tombstone across a reload.
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload hub.toml: %v", err)
	}
	if _, ok := reloaded.TeardownRemnants[arm.RemnantID]; !ok {
		t.Fatalf("hub.toml does not carry remnant %q: %+v", arm.RemnantID, reloaded.TeardownRemnants)
	}

	// Replaying the original mutationId is a no-op receipt return: same arm,
	// no second teardown.
	calls := hosts.removeCalls.Load()
	replay, err := m.RemoveResult(context.Background(), req)
	if err != nil {
		t.Fatalf("replay = %v", err)
	}
	if replay.HostMutationTeardownFailureRemoved == nil ||
		replay.HostMutationTeardownFailureRemoved.RemnantID != arm.RemnantID {
		t.Fatalf("replay = %+v, want the recorded failure arm with remnant %q", replay, arm.RemnantID)
	}
	if hosts.removeCalls.Load() != calls {
		t.Fatal("the replay ran the teardown again")
	}

	// The fence: every lifecycle mutation on the name refuses `remnant-open`
	// naming the blocking remnant, never the gate-busy form and never
	// not-found — including `add` on a tombstone-only name.
	for _, refusal := range []error{
		func() error {
			_, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "side.example"}})
			return err
		}(),
		func() error {
			_, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "side2.example"}))
			return err
		}(),
		func() error {
			_, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
			return err
		}(),
	} {
		assertRemnantOpenRefusal(t, refusal, arm.RemnantID)
	}

	// teardown-retry completes only the named teardown.
	hosts.refuseRemove.Store(false)
	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("TeardownRetry = %v", err)
	}
	if retry.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("retry = %+v, want the teardown-complete removed arm", retry)
	}
	if retry.HostTeardownRetryCompleteRemoved.HostKind != appwire.HostKindRemoved {
		t.Fatalf("retry hostKind = %q, want removed", retry.HostTeardownRetryCompleteRemoved.HostKind)
	}
	if _, open := m.cfg.store.markedRemnantFor("side"); open {
		t.Fatal("the fence still stands after the retry completed")
	}
	// The idempotent replay: the same id now returns `already-cleared`, from the
	// persisted typed resolved-remnant record.
	cleared, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("TeardownRetry (replay) = %v", err)
	}
	if cleared.HostTeardownRetryClearedRemoved == nil {
		t.Fatalf("replay = %+v, want the already-cleared arm", cleared)
	}
	// The original mutationId now replays a resolved receipt.
	resolved, err := m.RemoveResult(context.Background(), req)
	if err != nil {
		t.Fatalf("resolved replay = %v", err)
	}
	if resolved.HostMutationCommittedRemoved == nil {
		t.Fatalf("resolved replay = %+v, want the committed arm", resolved)
	}
	// In a fresh process, the same id still reads `already-cleared`.
	restarted, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	remnantAfter, ok := restarted.TeardownRemnants[arm.RemnantID]
	if !ok || remnantAfter.Resolved == nil {
		t.Fatalf("the resolved record did not survive the write: %+v", remnantAfter)
	}
	if remnantAfter.Resolved.ResolutionKind != hostRemnantResolutionRetry ||
		remnantAfter.Resolved.HostKind != hostRemnantHostKindRemoved {
		t.Fatalf("resolved record = %+v, want a retry-kind removed record", remnantAfter.Resolved)
	}
}

// assertRemnantOpenRefusal pins the typed `remnant-open` refusal: the conflict
// class carrying the blocking remnantId, never the gate-busy form and never a
// not-found.
func assertRemnantOpenRefusal(t *testing.T, err error, wantRemnantID string) {
	t.Helper()
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("refusal = %v, want the typed remnant-open envelope", err)
	}
	data, ok := wireErr.Data.(appwire.RemnantOpenErrorData)
	if !ok {
		t.Fatalf("data = %T, want RemnantOpenErrorData (%v)", wireErr.Data, err)
	}
	if data.EvenerErrorInfo != appwire.ErrorRemnantOpen || data.RemnantID != wantRemnantID {
		t.Fatalf("refusal = %+v, want remnant-open naming %q", data, wantRemnantID)
	}
}

// TestHostTeardownRetryUnknownKeyIsTyped pins §11's `teardown-unknown-key`
// entry: "It fires exactly when the named remnant id is unknown or purged".
func TestHostTeardownRetryUnknownKeyIsTyped(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	_, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: "01234567-0000-0000-0000-000000000000"})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("TeardownRetry(unknown) = %v, want the typed envelope", err)
	}
	data, ok := wireErr.Data.(appwire.TeardownUnknownKeyErrorData)
	if !ok {
		t.Fatalf("data = %T, want TeardownUnknownKeyErrorData", wireErr.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorTeardownUnknownKey {
		t.Fatalf("discriminator = %q, want %q", data.EvenerErrorInfo, appwire.ErrorTeardownUnknownKey)
	}
	if data.RemnantID == "" {
		t.Fatal("the refusal carries no remnantId")
	}
}

// TestHostTeardownRetryTimeoutFreesTheGateAndFencesWithAnAttempt pins §6's
// timeout rule: "on timeout the retry releases the host gate ... and marks its
// attempt record timed-out-but-open, then reports the terminal
// committed-with-teardown-failure outcome with the remnant still open".
func TestHostTeardownRetryTimeoutFreesTheGateAndFencesWithAnAttempt(t *testing.T) {
	m, hosts, _ := newRemnantFixture(t)
	// A teardown that blocks past the bounded deadline: the deadline is a
	// registry that refuses, and the retry's own deadline is pinned to a
	// nanosecond so the bounded run always expires.
	hosts.refuseRemove.Store(true)
	req := removeRequest(t, m, "side")
	result, err := m.RemoveResult(context.Background(), req)
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	arm := result.HostMutationTeardownFailureRemoved
	if arm == nil {
		t.Fatalf("Remove = %+v, want the failure arm", result)
	}
	m.cfg.policy.teardownTimeout = time.Nanosecond

	timeoutArm, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("TeardownRetry (timeout) = %v", err)
	}
	failed := timeoutArm.HostTeardownRetryFailedRemoved
	if failed == nil {
		t.Fatalf("timeout retry = %+v, want the committed-with-teardown-failure arm", timeoutArm)
	}
	if failed.Seam != "remove-host" {
		t.Fatalf("timeout arm seam = %q, want the failed seam named", failed.Seam)
	}
	// The remnant is still open and its attempt record is open and timed out.
	if _, open := m.cfg.store.markedRemnantFor("side"); !open {
		t.Fatal("the timeout closed the remnant")
	}
	attemptID, attempt, ok := m.cfg.store.openAttemptFor(arm.RemnantID)
	if !ok {
		t.Fatal("the timed-out attempt record is not open for fencing")
	}
	if !attempt.timedOut() || attemptID == "" {
		t.Fatalf("attempt = %+v, want timed-out-but-open", attempt)
	}
	// The gate is free: a concurrent retry try-acquires it and fences the
	// timed-out attempt before re-running.
	m.cfg.policy.teardownTimeout = time.Minute
	hosts.refuseRemove.Store(false)
	second, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("second retry = %v", err)
	}
	if second.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("second retry = %+v, want the teardown-complete arm", second)
	}
	fenced, ok := m.cfg.store.attemptsSnapshot()[attemptID]
	if !ok {
		t.Fatal("the prior attempt record is gone")
	}
	if fenced.State != hostAttemptStateFencedClosed || fenced.FencedAt == "" {
		t.Fatalf("prior attempt = %+v, want fenced-closed with an instant", fenced)
	}
}

// TestHostTeardownRecoverRequiresTheAttestation pins §6's recover contract: a
// well-formed attestation refuses on a still-live pinned handle, and clears
// once the safety check passes, recording the attestation on the original
// receipt and replaying from the persisted record.
func TestHostTeardownRecoverRequiresTheAttestation(t *testing.T) {
	m, _, configPath := newRemnantFixture(t)
	// A remnant with no live entry: the update path's remnant for a name whose
	// live entry moved on is unresolvable, which is exactly the recover case.
	hosts := m.cfg.hosts
	host, ok := hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	remnantID := mintRemnantID()
	remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host, "", m.nowTime())
	// The pinned pair is a generation the live set no longer carries.
	remnant.Generation = host.Generation + 100
	remnant.IncarnationID = "older-incarnation"
	remnant.CleanupHandle.Generation = remnant.Generation
	remnant.CleanupHandle.IncarnationID = remnant.IncarnationID
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("stage remnant: %v", err)
	}

	// A malformed statement refuses validation before any clearance.
	_, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
		RemnantID:   remnantID,
		Attestation: appwire.HostTeardownAttestation{Operator: "op", Statement: "trust me", ObservedAt: "2026-09-27T12:00:00Z"},
	})
	if err == nil || !strings.Contains(err.Error(), hostRecoveryStatement) {
		t.Fatalf("recover(bad statement) = %v, want the statement refusal", err)
	}
	if _, stillOpen := m.cfg.store.markedRemnantFor("side"); !stillOpen {
		t.Fatal("a refused attestation cleared the remnant")
	}

	// The authenticated operator must match the session's identity when the
	// session carries one.
	_, err = m.TeardownRecover(withSessionOperator(context.Background(), "someone-else"), appwire.HostTeardownRecoverParams{
		RemnantID:   remnantID,
		Attestation: appwire.HostTeardownAttestation{Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z"},
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated identity") {
		t.Fatalf("recover(operator mismatch) = %v, want the identity refusal", err)
	}

	// The audited clearance.
	cleared, err := m.TeardownRecover(withSessionOperator(context.Background(), "op"), appwire.HostTeardownRecoverParams{
		RemnantID:   remnantID,
		Attestation: appwire.HostTeardownAttestation{Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z"},
	})
	if err != nil {
		t.Fatalf("recover = %v", err)
	}
	if cleared.Outcome != appwire.HostTeardownOutcomeRecovered || cleared.RemnantID != remnantID ||
		cleared.ClearedName != "side" || cleared.ClearedAt == "" || cleared.HostKind != appwire.HostKindRemoved {
		t.Fatalf("recover response = %+v", cleared)
	}
	if _, open := m.cfg.store.markedRemnantFor("side"); open {
		t.Fatal("the fence still stands after the clearance")
	}
	// The record carries the attestation and survives a reload.
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	record, ok := reloaded.TeardownRemnants[remnantID]
	if !ok || record.Resolved == nil || record.Resolved.Attestation == nil {
		t.Fatalf("recovery record = %+v, want a resolved record carrying the attestation", record)
	}
	if record.Resolved.Attestation.Operator != "op" || record.Resolved.Attestation.Statement != hostRecoveryStatement {
		t.Fatalf("attestation = %+v", record.Resolved.Attestation)
	}
	// A replay of the same id returns the same response, never not-found.
	replay, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{RemnantID: remnantID})
	if err != nil {
		t.Fatalf("recover replay = %v", err)
	}
	if replay != cleared {
		t.Fatalf("replay = %+v, want the recorded response %+v", replay, cleared)
	}
}

// TestHostStagedMarkerIsFinalizedByTheNextMutation pins §16's pending-marker
// bullet: a post-commit receipt write lost while the process stays alive leaves
// the staged marker staged, and the next mutation-path write finalizes it.
func TestHostStagedMarkerIsFinalizedByTheNextMutation(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	// A marker for a host this manager did not stage: the durable state a lost
	// post-commit write leaves behind.
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	remnantID := mintRemnantID()
	receipt := newHostMutationReceipt("lost-mutation", hostMutationUpdate, host, m.nowTime())
	receipt.RemnantID = remnantID
	key := hostMutationReceiptKey("lost-mutation", "side", hostMutationUpdate, hostMutationIdentity{
		Generation:    host.Generation,
		IncarnationID: host.IncarnationID,
	})
	marker := HostStagedReceipt{
		Key:             key,
		StagedAt:        m.nowTime().UTC().Format(time.RFC3339),
		Phase:           hostStagedPhaseRuntimeSwapped,
		TeardownStarted: true,
		SwapStarted:     true,
		Provisional:     receipt,
		PendingTeardown: HostPendingTeardown{Name: "side", Kind: hostTeardownKindUpdate, Generation: host.Generation, IncarnationID: host.IncarnationID},
	}
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{marker: &pendingHostMarker{Name: "side", Marker: marker}}); err != nil {
		t.Fatalf("stage marker: %v", err)
	}
	if _, ok := m.cfg.store.stagedSnapshot()["side"]; !ok {
		t.Fatal("the marker did not land")
	}

	// The next mutation on the name finalizes it first, then proceeds.
	result, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "side2.example"}))
	if err != nil {
		t.Fatalf("Update after a leftover marker = %v", err)
	}
	if result.HostMutationCommitted == nil {
		t.Fatalf("Update = %+v, want the committed arm", result)
	}
	if _, ok := m.cfg.store.stagedSnapshot()["side"]; ok {
		t.Fatal("the leftover marker survived the next mutation")
	}
	if _, ok := m.cfg.store.receiptsSnapshot()[key]; !ok {
		t.Fatal("the leftover marker was dropped without finalizing its receipt")
	}
}

// TestHostKeylessAddRetryReturnsTheAmbiguousArm pins §5/§11's keyless
// read-after-unknown path and the union's `ambiguous` arm.
func TestHostKeylessAddRetryReturnsTheAmbiguousArm(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	// A keyless add whose intended entry matches the listed row's effective
	// fields and pair: the retry cannot tell whether it committed, so it claims
	// no commit.
	result, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "side", Address: host.SSH,
	}})
	if err != nil {
		t.Fatalf("keyless Add = %v", err)
	}
	if result.HostMutationAmbiguous == nil {
		t.Fatalf("keyless Add = %+v, want the ambiguous arm", result)
	}
	if result.ObservedRow.Name != "side" {
		t.Fatalf("ambiguous arm row = %+v", result.ObservedRow)
	}
	// A changed effective field is the stale-entry path, not the ambiguous one:
	// the keyless retry does not match, and the live-name duplicate refusal is
	// what fires (it never re-applies).
	result, err = m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{
		Name: "side", Address: "other.example",
	}})
	if err == nil || result.HostMutationAmbiguous != nil {
		t.Fatalf("changed-field keyless Add = %+v / %v, want the duplicate refusal", result, err)
	}
}

// TestHostConcurrentEditRefusalCarriesBothFingerprints pins §11's
// `concurrent-edit` pair.
func TestHostConcurrentEditRefusalCarriesBothFingerprints(t *testing.T) {
	err := concurrentEditRefusal("staged-fingerprint", "observed-fingerprint")
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("concurrentEditRefusal = %v, want the typed envelope", err)
	}
	data, ok := wireErr.Data.(appwire.ConcurrentEditErrorData)
	if !ok {
		t.Fatalf("data = %T", wireErr.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorConcurrentEdit ||
		data.StagedFingerprint != "staged-fingerprint" || data.ObservedFingerprint != "observed-fingerprint" {
		t.Fatalf("data = %+v", data)
	}
	if wireErr.Code != appwire.CodeConflict {
		t.Fatalf("code = %d, want the conflict class", wireErr.Code)
	}
}

// TestHostMutationResultUnionRoundTrips pins the four-arm union's wire shapes,
// including the `ambiguous` arm and the dedicated removed-row arm.
func TestHostMutationResultUnionRoundTrips(t *testing.T) {
	cases := map[string]appwire.HostMutationResult{
		"committed": {HostMutationCommitted: &appwire.HostMutationCommitted{
			Outcome: appwire.HostMutationOutcomeCommitted, Host: appwire.HostRow{Name: "side"}}},
		"committed-removed": {HostMutationCommittedRemoved: &appwire.HostMutationCommittedRemoved{
			Outcome: appwire.HostMutationOutcomeCommitted, Host: appwire.RemovedRow{Name: "side", Removed: true}}},
		"teardown-failure": {HostMutationTeardownFailure: &appwire.HostMutationTeardownFailure{
			Outcome: appwire.HostMutationOutcomeTeardownFailure, Seam: "remove-host", RemnantID: "r-1",
			Host: appwire.HostRow{Name: "side"}}},
		"collision-dropped": {HostMutationCollisionDropped: &appwire.HostMutationCollisionDropped{
			Outcome: appwire.HostMutationOutcomeCollisionDropped, WinningFingerprint: "fp",
			DroppedEntry: appwire.HostRow{Name: "side"}, Removed: true}},
		"ambiguous": {HostMutationAmbiguous: &appwire.HostMutationAmbiguous{
			Outcome: appwire.HostMutationOutcomeAmbiguous, ObservedRow: appwire.HostRow{Name: "side"}}},
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded appwire.HostMutationResult
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal %s: %v", raw, err)
			}
			again, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if !bytes.Equal(again, raw) {
				t.Fatalf("round trip changed the shape: %s -> %s", raw, again)
			}
			var probe struct {
				Outcome string `json:"outcome"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil || probe.Outcome == "" {
				t.Fatalf("the discriminator is absent from %s (%v)", raw, err)
			}
		})
	}
}

// TestHostRemnantRecordsRefuseCorruptContent pins the reserved-namespace rule
// for the three new sections.
func TestHostRemnantRecordsRefuseCorruptContent(t *testing.T) {
	cases := []struct {
		name    string
		section string
		want    string
	}{
		{
			name: "staged marker with an unknown phase",
			section: `[staged_receipts."side"]
key = "mut-1/side/update/2/inc-1"
staged_at = "2026-09-27T12:00:00Z"
phase = "halfway"
[staged_receipts."side".provisional]
outcome = "committed"
remnant_id = "r-1"
`,
			want: "cannot produce or decode",
		},
		{
			name: "remnant with an unresolvable cleanup handle",
			section: `[teardown_remnants."r-1"]
host = "side"
kind = "remove"
seam = "remove-host"
generation = 2
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
[teardown_remnants."r-1".pending_teardown]
name = "side"
kind = "remove"
generation = 2
incarnation_id = "inc-1"
[teardown_remnants."r-1".cleanup_handle]
kind = "nonesuch"
`,
			want: "cannot resolve",
		},
		{
			name: "attempt naming an absent remnant",
			section: `[teardown_attempts."a-1"]
remnant_id = "r-absent"
state = "open"
started_at = "2026-09-27T12:00:00Z"
`,
			want: "which teardown_remnants does not carry",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "hub.toml")
			content := "[[hosts]]\nname = \"side\"\nssh = \"side.example\"\n\n" + tc.section
			if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
				t.Fatalf("write hub.toml: %v", err)
			}
			if _, err := LoadConfig(configPath); err == nil {
				t.Fatal("LoadConfig accepted a teardown-repair record this build cannot decode")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfig error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestHostMutationCollisionDroppedAdoptsTheHandEdit pins spec 08 §11's dropped
// arm end to end: a hand edit that lands while a mutation is between its commit
// and its finalizing write wins ("a hand edit the post-rename re-read observes
// is adopted (the file's bytes win, the receipt says `collision-dropped`)"), the
// response carries the staged entry that was dropped plus the winning
// fingerprint, and a replay returns the same arm from the receipt.
func TestHostMutationCollisionDroppedAdoptsTheHandEdit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	// Park the edit after its commit, before its finalizing write.
	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	m.testOnlyParkPostCommit = func(name string) {
		if name != "side" {
			return
		}
		once.Do(func() { close(parked) })
		<-release
	}
	req := updateRequest(t, m, "side", appwire.HostEntry{Address: "staged.example"})
	type outcome struct {
		result appwire.HostMutationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := m.UpdateResult(context.Background(), req)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("the edit never reached its post-commit window")
	}
	// The hand edit: the operator rewrites the entry the commit staged.
	handEdited := []byte("[[hosts]]\nname = \"side\"\nssh = \"hand.example\"\n")
	if err := os.WriteFile(configPath, handEdited, 0o600); err != nil {
		t.Fatalf("hand-edit hub.toml: %v", err)
	}
	wantFingerprint := hubTOMLFingerprint(handEdited)
	close(release)
	result := <-done
	if result.err != nil {
		t.Fatalf("Update = %v, want the collision-dropped arm", result.err)
	}
	arm := result.result.HostMutationCollisionDropped
	if arm == nil {
		t.Fatalf("Update = %+v, want the collision-dropped arm", result.result)
	}
	if arm.WinningFingerprint != wantFingerprint {
		t.Fatalf("winningFingerprint = %q, want the hand edit's %q", arm.WinningFingerprint, wantFingerprint)
	}
	if arm.DroppedEntry.Name != "side" || arm.DroppedEntry.Address != "staged.example" {
		t.Fatalf("droppedEntry = %+v, want the staged entry the reconcile dropped", arm.DroppedEntry)
	}
	if arm.Removed || arm.Host == nil || arm.Host.Address != "hand.example" {
		t.Fatalf("arm = %+v, want the winning live row for the hand edit", arm)
	}
	// The file keeps the hand edit — the adopted bytes, never the staged entry.
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].SSH != "hand.example" {
		t.Fatalf("hub.toml = %+v, want the hand edit to survive the finalizing write", cfg.Hosts)
	}
	// The runtime followed the file: the live row is the hand edit.
	live, ok := m.cfg.hosts.Get("side")
	if !ok || live.SSH != "hand.example" {
		t.Fatalf("live entry = %+v (ok=%v), want the adopted hand edit", live, ok)
	}
	// The receipt says collision-dropped, and a replay returns the same arm.
	var receipt HostMutationReceipt
	found := false
	for _, candidate := range cfg.MutationReceipts {
		if candidate.Outcome == hostReceiptOutcomeCollisionDropped {
			receipt, found = candidate, true
		}
	}
	if !found {
		t.Fatalf("stored receipts = %+v, want a collision-dropped record", cfg.MutationReceipts)
	}
	if receipt.WinningFingerprint != wantFingerprint || receipt.DroppedEntry == nil {
		t.Fatalf("stored receipt = %+v, want the collision-dropped fields", receipt)
	}
	replay, err := m.UpdateResult(context.Background(), req)
	if err != nil {
		t.Fatalf("replay = %v", err)
	}
	if replay.HostMutationCollisionDropped == nil || replay.WinningFingerprint != wantFingerprint {
		t.Fatalf("replay = %+v, want the recorded dropped arm", replay)
	}
}

// TestHostMutationCollisionDroppedHandEditDeletion pins the arm's other half:
// "when the re-read instead finds the name gone entirely (a hand-edit deletion)
// the arm carries no `host` and sets `removed: true` — the winning arm is the
// deletion, with the marker tombstone rows use and no tombstone minted". It is
// an EDIT whose staged entry the hand edit deleted: the staged state of an edit
// is the name's presence, so its absence is unambiguously a foreign deletion.
func TestHostMutationCollisionDroppedHandEditDeletion(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	m.testOnlyParkPostCommit = func(name string) {
		if name != "side" {
			return
		}
		once.Do(func() { close(parked) })
		<-release
	}
	req := updateRequest(t, m, "side", appwire.HostEntry{Address: "staged.example"})
	done := make(chan appwire.HostMutationResult, 1)
	go func() {
		result, _ := m.UpdateResult(context.Background(), req)
		done <- result
	}()
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("the edit never reached its post-commit window")
	}
	// The hand edit deletes the name (and carries nothing else, so no tombstone
	// is minted by the hub).
	deleted := []byte("addr = \"127.0.0.1:9180\"\n")
	if err := os.WriteFile(configPath, deleted, 0o600); err != nil {
		t.Fatalf("hand-edit hub.toml: %v", err)
	}
	wantFingerprint := hubTOMLFingerprint(deleted)
	close(release)
	result := <-done
	arm := result.HostMutationCollisionDropped
	if arm == nil {
		t.Fatalf("Update = %+v, want the collision-dropped arm", result)
	}
	if !arm.Removed || arm.Host != nil {
		t.Fatalf("arm = %+v, want removed=true with no host", arm)
	}
	if arm.WinningFingerprint != wantFingerprint || arm.DroppedEntry.Name != "side" {
		t.Fatalf("arm = %+v, want the deletion's fingerprint and the staged entry", arm)
	}
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("the deleted name is still live")
	}
}

// TestHostStagedCompensationLeavesNoMarkerOrReceipt pins roborev's High finding
// on the compensation path: a commit whose step-(2) write landed and whose flip
// then refused must leave NEITHER the staged-receipt marker NOR the provisional
// receipt behind — in the store or in the file. Leaving them would let the next
// mutation (or any boot) finalize a `committed` receipt for a mutation the API
// reported as refused, and the leaked provisional receipt would answer a replay
// of that mutationId as though it had committed.
func TestHostStagedCompensationLeavesNoMarkerOrReceipt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	// The foreign write lands in the staged-write→flip window: the flip's
	// fingerprint check observes it and refuses with the typed `concurrent-edit`,
	// so the mutation compensates.
	m.testOnlyAfterStage = func(name string) {
		if name != "side" {
			return
		}
		foreign := []byte("[[hosts]]\nname = \"side\"\nssh = \"foreign.example\"\n")
		if err := os.WriteFile(configPath, foreign, 0o600); err != nil {
			t.Fatalf("foreign hub.toml write: %v", err)
		}
	}
	req := updateRequest(t, m, "side", appwire.HostEntry{Address: "staged.example"})
	_, err := m.UpdateResult(context.Background(), req)
	if err == nil {
		t.Fatal("Update over the raced window succeeded, want the typed refusal")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeConflict {
		t.Fatalf("refusal = %v, want the typed concurrent-edit conflict", err)
	}
	if data, ok := wireErr.Data.(appwire.ConcurrentEditErrorData); !ok {
		t.Fatalf("refusal data = %#v, want ConcurrentEditErrorData", wireErr.Data)
	} else if data.StagedFingerprint == "" || data.ObservedFingerprint == "" {
		t.Fatalf("refusal data = %+v, want both fingerprints", data)
	}
	// Nothing staged survives in memory...
	if markers := m.cfg.store.stagedSnapshot(); len(markers) != 0 {
		t.Fatalf("store still holds staged markers: %+v", markers)
	}
	if receipts := m.cfg.store.receiptsSnapshot(); len(receipts) != 0 {
		t.Fatalf("store still holds receipts: %+v", receipts)
	}
	// ...and nothing staged survives on disk, for a reader that reloads it.
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	if len(cfg.StagedReceipts) != 0 || len(cfg.MutationReceipts) != 0 {
		t.Fatalf("hub.toml carries staged records after the compensation: staged=%+v receipts=%+v",
			cfg.StagedReceipts, cfg.MutationReceipts)
	}
	// A restart finalizes nothing for the name.
	restarted := bootHostManager(t, configPath)
	if markers := restarted.cfg.store.stagedSnapshot(); len(markers) != 0 {
		t.Fatalf("a restart inherited staged markers: %+v", markers)
	}
	if receipts := restarted.cfg.store.receiptsSnapshot(); len(receipts) != 0 {
		t.Fatalf("a restart inherited receipts: %+v", receipts)
	}
	// The same mutationId is no longer a dedup hit: the retry is a fresh
	// mutation under that key, not a replay of a fabricated outcome.
	if hit, err := restarted.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID: req.MutationID, Name: "side", Kind: hostMutationUpdate,
	}); err != nil {
		t.Fatalf("lookup after the compensation: %v", err)
	} else if hit != nil {
		t.Fatalf("the compensated mutationId still hit a receipt: %+v", hit.Receipt)
	}
	// The restart re-minted the name's incarnation (the foreign file carried no
	// identity record), so the fresh attempt carries the pair the restarted
	// manager now holds — the point is that the same mutationId is a fresh key,
	// not that the old guard still fits.
	retry := updateRequest(t, restarted, "side", appwire.HostEntry{Address: "staged.example"})
	retry.MutationID = req.MutationID
	fresh, err := restarted.UpdateResult(context.Background(), retry)
	if err != nil {
		t.Fatalf("fresh update under the same key = %v", err)
	}
	if fresh.HostMutationCommitted == nil || fresh.HostMutationCommitted.Host.Address != "staged.example" {
		t.Fatalf("fresh update = %+v, want a normal fresh commit", fresh)
	}
}

// TestHostRemnantFenceGatesRetentionFromTheRealRecords pins §6's retention rule
// against the REAL remnant set rather than the test-only override: "Retention
// expiry never purges a tombstone whose name still holds an open remnant (§15)",
// and a tombstone past its retention that is remnant-gated survives the next
// mutation's derivation write.
func TestHostRemnantFenceGatesRetentionFromTheRealRecords(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{
		{Name: "keep", SSH: "keep.example"},
		{Name: "side", SSH: "side.example"},
	}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	// A committed removal whose teardown fails leaves the tombstone plus an open
	// remnant — the durable state the fence is about.
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	result, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	arm := result.HostMutationTeardownFailureRemoved
	if arm == nil {
		t.Fatalf("Remove = %+v, want the teardown-failure arm", result)
	}
	m.testOnlyTeardown = nil
	if _, ok := m.cfg.store.tombstoneSnapshot()["side"]; !ok {
		t.Fatal("the removal left no tombstone to gate")
	}
	if _, ok := m.cfg.store.remnantByID(arm.RemnantID); !ok {
		t.Fatalf("no durable remnant %q", arm.RemnantID)
	}

	// Past the retention period, with an unrelated mutation writing hub.toml:
	// the gated tombstone is never an expiry candidate.
	m.cfg.now = func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
	if _, err := m.AddResult(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "other", Address: "other.example"},
	}); err != nil {
		t.Fatalf("Add(other) = %v", err)
	}
	if _, ok := m.cfg.store.tombstoneSnapshot()["side"]; !ok {
		t.Fatal("the remnant-gated tombstone was expired away by an unrelated write")
	}
	if id, open := m.openRemnantID("side"); !open || id != arm.RemnantID {
		t.Fatalf("the fence lifted: (%q, %v), want %q", id, open, arm.RemnantID)
	}
	// hub.toml still carries both records, so a restart cannot lose the repair
	// handle either.
	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload hub.toml: %v", err)
	}
	if _, ok := reloaded.Tombstones["side"]; !ok {
		t.Fatal("hub.toml dropped the gated tombstone")
	}
	if _, ok := reloaded.TeardownRemnants[arm.RemnantID]; !ok {
		t.Fatalf("hub.toml dropped remnant %q", arm.RemnantID)
	}
	// The row still names the fence, and the generation is pinned: no mutation
	// advanced the name past it.
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	found := false
	for _, row := range list.Hosts {
		if row.Name == "side" {
			found = true
			if row.OpenRemnantID != arm.RemnantID {
				t.Fatalf("tombstone row = %+v, want openRemnantId %q", row, arm.RemnantID)
			}
			if row.Generation != host.Generation {
				t.Fatalf("tombstone row generation = %d, want the pinned %d", row.Generation, host.Generation)
			}
		}
	}
	if !found {
		t.Fatal("list lost the remnant-gated tombstone row")
	}
}

// TestHostBootCollisionBlocksTheLiveEntryPendingTeardown pins §6's boot arm:
// "boot excludes the colliding live entry from the live set as
// `blocked-pending-teardown` (never published live) until `teardown-retry`
// resolves it, so no live incarnation is ever created over an open remnant".
func TestHostBootCollisionBlocksTheLiveEntryPendingTeardown(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	remnantID := "11111111-2222-3333-4444-555555555555"
	raw := hostTOMLBanner + `[[hosts]]
name = "side"
ssh = "side.example"

[tombstones."side"]
name = "side"
removed_at = "2026-09-26T00:00:00Z"
origin = "hub.toml"
generation = 3
incarnation_id = "old-incarnation"
presence_epoch = 4
rows_truncated = false

[tombstones."side".entry]
name = "side"
ssh = "side.example"

[generations."side"]
generation = 3
incarnation_id = "old-incarnation"
presence_epoch = 4

[teardown_remnants."` + remnantID + `"]
host = "side"
kind = "remove"
seam = "remove-host"
generation = 3
incarnation_id = "old-incarnation"
mutation_key = "mut-1/side/remove/3/old-incarnation"
committed_at = "2026-09-26T00:00:00Z"

[teardown_remnants."` + remnantID + `".pending_teardown]
name = "side"
kind = "remove"
generation = 3
incarnation_id = "old-incarnation"
supervisor = true
channel = true

[teardown_remnants."` + remnantID + `".cleanup_handle]
kind = "local-boundary"
generation = 3
incarnation_id = "old-incarnation"
presence_epoch = 4
`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	// The colliding live entry was never published: the remnant-gated name is
	// not in the live set.
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("boot published a live entry over an open remnant")
	}
	if _, ok := m.cfg.store.entryByName("side"); ok {
		t.Fatal("boot kept the blocked entry in the store's live set")
	}
	// The remnant is still resumable by id, and the tombstone still stands.
	remnant, ok := m.cfg.store.remnantByID(remnantID)
	if !ok || !remnant.open() {
		t.Fatalf("remnant = %+v (ok=%v), want the open record", remnant, ok)
	}
	if _, ok := m.cfg.store.tombstoneSnapshot()["side"]; !ok {
		t.Fatal("the colliding tombstone was dropped")
	}
	list, err := m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(list.Hosts) != 1 || list.Hosts[0].OpenRemnantID != remnantID {
		t.Fatalf("list rows = %+v, want the one gated tombstone row", list.Hosts)
	}
	// Forward repair resolves it, after which the name is free again.
	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	if err != nil {
		t.Fatalf("TeardownRetry = %v", err)
	}
	if retry.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("retry = %+v, want the teardown-complete removed arm", retry)
	}
	if _, open := m.cfg.store.markedRemnantFor("side"); open {
		t.Fatal("the fence still stands after the retry")
	}
}

// TestHostAttachRefusesTheRemnantFenceThroughTheRealServer pins roborev's High
// finding on the attach seam: `registerHostAttachHandler` takes hubcore.WebConfig
// BY VALUE and reads HostRemnantFence off that copy, so the fence has to be
// installed on the copy the handler captures — before its registration. The only
// way to observe that is over the wire, through the real server construction
// path, which is what this test drives: the hub boots over a hub.toml carrying
// an open remnant, and evener/host/attach refuses with the typed `remnant-open`
// naming the blocking id instead of dialing over an unfinished teardown.
func TestHostAttachRefusesTheRemnantFenceThroughTheRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	remnantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	raw := hostTOMLBanner + `[[hosts]]
name = "m4"
ssh = "m4.example"

[teardown_remnants."` + remnantID + `"]
host = "m4"
kind = "update"
seam = "update-host"
generation = 1
incarnation_id = "inc-1"
mutation_key = "mut-1/m4/update/1/inc-1"
committed_at = "2026-09-26T00:00:00Z"

[teardown_remnants."` + remnantID + `".pending_teardown]
name = "m4"
kind = "update"
generation = 1
incarnation_id = "inc-1"
supervisor = true
channel = true

[teardown_remnants."` + remnantID + `".cleanup_handle]
kind = "local-boundary"
generation = 1
incarnation_id = "inc-1"
presence_epoch = 1
`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cfg, _, _ := hostManageWiringConfig(t, configPath, []hostreg.Host{{Name: "m4", SSH: "m4.example"}}, &dialRecordingRunner{})
	hub, _ := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	err := client.Request(context.Background(), appwire.MethodEvenerHostAttach, appwire.HostAttachParams{Host: "m4"}, nil)
	if err == nil {
		t.Fatal("evener/host/attach dialed a remnant-fenced host, want the typed remnant-open refusal")
	}
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("attach refusal = %v, want the typed envelope", err)
	}
	// The wire client hands the payload back as decoded JSON, so the test reads
	// it through the typed shape the same way a consumer does.
	rawData, err := json.Marshal(wireErr.Data)
	if err != nil {
		t.Fatalf("marshal refusal data: %v", err)
	}
	var data appwire.RemnantOpenErrorData
	if err := json.Unmarshal(rawData, &data); err != nil {
		t.Fatalf("unmarshal refusal data %s: %v", rawData, err)
	}
	if data.EvenerErrorInfo != appwire.ErrorRemnantOpen {
		t.Fatalf("attach refusal data = %s, want remnant-open", rawData)
	}
	if data.RemnantID != remnantID {
		t.Fatalf("attach refusal names %q, want the blocking remnant %q", data.RemnantID, remnantID)
	}
}

// TestHostMarkerFinalizationReleasesTheGateForThePinnedTeardown pins roborev's
// other High finding: the orphan-marker finalizer used to hold the per-host gate
// across `runPinnedTeardown`, which in production is the manager's own
// non-reentrant per-host lock — so finalizing a `remove` marker for a host whose
// live registry still carries the pinned incarnation hung the caller (and boot
// with it). It must release the reservation for the run and re-acquire it for
// the finalizing write, exactly as `teardown-retry` does. With a manager wired,
// the old shape deadlocks here; the new one finalizes.
func TestHostMarkerFinalizationReleasesTheGateForThePinnedTeardown(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	reg, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	m := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{}, configPath, reg, nil)

	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	// A surviving staged marker for a REMOVAL whose live registry still holds
	// the pinned incarnation: the finalizer's pinned teardown is therefore a
	// real `RemoveHost` — the call that takes the same gate.
	receipt := newHostMutationReceipt("lost-mutation", hostMutationRemove, host, m.nowTime())
	receipt.RemnantID = mintRemnantID()
	key := hostMutationReceiptKey("lost-mutation", "side", hostMutationRemove, hostMutationIdentity{
		Generation:    host.Generation,
		IncarnationID: host.IncarnationID,
	})
	marker := HostStagedReceipt{
		Key:             key,
		StagedAt:        m.nowTime().UTC().Format(time.RFC3339),
		Phase:           hostStagedPhaseRuntimeSwapped,
		TeardownStarted: true,
		SwapStarted:     true,
		Provisional:     receipt,
		PendingTeardown: pendingTeardownFor(host, hostTeardownKindRemove),
	}
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{marker: &pendingHostMarker{Name: "side", Marker: marker}}); err != nil {
		t.Fatalf("stage marker: %v", err)
	}

	// Finalize it synchronously: the old shape deadlocks on the manager's gate
	// (the test would hang), the fixed shape completes.
	finalized, err := m.finalizeOrphanMarkerIfAny(context.Background(), "side", true)
	if err != nil {
		t.Fatalf("finalizeOrphanMarkerIfAny = %v", err)
	}
	if finalized == nil {
		t.Fatal("the marker was not finalized")
	}
	if finalized.Outcome != hostReceiptOutcomeCommitted || !finalized.BootRecovered {
		t.Fatalf("finalized receipt = %+v, want a boot-recovered committed receipt", finalized)
	}
	if _, ok := m.cfg.store.stagedSnapshot()["side"]; ok {
		t.Fatal("the marker survived its finalization")
	}
	// The pinned removal ran: the live registry no longer carries the host.
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("the pinned removal did not run")
	}
}

// TestHostTeardownRetryRefusesWhileAnAttemptIsLive pins roborev's Medium
// finding: the gate is released during a retry's run, so the gate cannot answer
// "is an attempt running?" — the attempt's own fencing epoch must. A second
// retry (and a recover) refuses with the typed busy while this boot's attempt is
// open and has not timed out, instead of fencing a live run and executing the
// same teardown twice.
func TestHostTeardownRetryRefusesWhileAnAttemptIsLive(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	m.cfg.bootID = "boot-1"
	remnantID := mintRemnantID()
	remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host, "", m.nowTime())
	remnant.Generation = host.Generation + 100
	remnant.IncarnationID = "pinned-incarnation"
	remnant.CleanupHandle.Generation = remnant.Generation
	remnant.CleanupHandle.IncarnationID = remnant.IncarnationID
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("stage remnant: %v", err)
	}
	// A live attempt of THIS boot, not timed out: the state a running retry
	// leaves while its gate is released.
	attemptID := mintAttemptID()
	live := HostTeardownAttempt{
		RemnantID:         remnantID,
		State:             hostAttemptStateOpen,
		StartedAt:         m.nowTime().UTC().Format(time.RFC3339),
		FencingEpochBoot:  "boot-1",
		FencingEpochOpSeq: 1,
	}
	if err := m.persistHosts(entries, entries, hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: live}}); err != nil {
		t.Fatalf("stage attempt: %v", err)
	}

	if _, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID}); err == nil {
		t.Fatal("a second retry ran against a live attempt, want the typed busy refusal")
	} else {
		assertWireCode(t, err, appwire.CodeConflict)
	}
	if _, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
		RemnantID:   remnantID,
		Attestation: appwire.HostTeardownAttestation{Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z"},
	}); err == nil {
		t.Fatal("a recover cleared past a live attempt, want the typed busy refusal")
	} else {
		assertWireCode(t, err, appwire.CodeConflict)
	}
	// The live attempt is untouched: still open, still this boot's, not fenced.
	stored, ok := m.cfg.store.attemptsSnapshot()[attemptID]
	if !ok || !stored.open() || stored.FencedAt != "" || stored.TimedOutAt != "" {
		t.Fatalf("attempt after the refusals = %+v, want the untouched live record", stored)
	}
	// A timed-out attempt of this boot IS takeover-able: the fence is for a
	// wedged run, not a live one.
	timedOut := stored
	timedOut.TimedOutAt = m.nowTime().UTC().Format(time.RFC3339)
	if err := m.persistHosts(entries, entries, hostPersistChange{attempt: &pendingHostAttempt{AttemptID: attemptID, Attempt: timedOut}}); err != nil {
		t.Fatalf("mark timed out: %v", err)
	}
	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	if err != nil {
		t.Fatalf("retry after the timeout = %v", err)
	}
	if retry.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("retry = %+v, want the teardown-complete arm", retry)
	}
	prior, ok := m.cfg.store.attemptsSnapshot()[attemptID]
	if !ok || prior.State != hostAttemptStateFencedClosed {
		t.Fatalf("prior attempt = %+v, want fenced-closed after the takeover", prior)
	}
}

// TestHostTeardownRetryRefusalIsNotRecordedAsATimeout pins roborev's Low
// finding: a non-deadline refusal (the pinned handle does not resolve) must not
// be durably recorded as a timeout, because consumers keyed on `timed_out_at`
// would then see a wedged run that never happened.
func TestHostTeardownRetryRefusalIsNotRecordedAsATimeout(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	m.cfg.bootID = "boot-1"
	remnantID := mintRemnantID()
	remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host, "", m.nowTime())
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("stage remnant: %v", err)
	}
	// A handle this build cannot resolve through: the remote-lease arm carries no
	// implementation here (it was withdrawn with the crash-fencing program,
	// comp08), so resolving it is the typed `teardown-unknown-key` refusal — the
	// canonical non-deadline failure a retry must surface without recording a
	// timeout.
	remnant.CleanupHandle.Kind = "remote-lease"
	remnant.CleanupHandle.RemoteGuardFile = "guard.json"
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("restage remnant: %v", err)
	}

	if _, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID}); err == nil {
		t.Fatal("retry over an unresolvable pinned handle succeeded, want the typed refusal")
	} else {
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) {
			t.Fatalf("refusal = %v, want the typed envelope", err)
		}
		rawData, marshalErr := json.Marshal(wireErr.Data)
		if marshalErr != nil {
			t.Fatalf("marshal refusal data: %v", marshalErr)
		}
		var data appwire.TeardownUnknownKeyErrorData
		if unmarshalErr := json.Unmarshal(rawData, &data); unmarshalErr != nil {
			t.Fatalf("unmarshal refusal data %s: %v", rawData, unmarshalErr)
		}
		if data.EvenerErrorInfo != appwire.ErrorTeardownUnknownKey {
			t.Fatalf("refusal data = %s, want teardown-unknown-key", rawData)
		}
	}
	for _, attempt := range m.cfg.store.attemptsSnapshot() {
		if attempt.TimedOutAt != "" {
			t.Fatalf("attempt = %+v: a non-deadline refusal was recorded as a timeout", attempt)
		}
		if attempt.open() {
			t.Fatalf("attempt = %+v: the refused attempt was left open", attempt)
		}
	}
}

// TestHostUpdateRemnantRepairsThroughRetryAndRecover pins roborev's High on the
// update remnant: an edit preserves the incarnation while advancing the
// generation, so a handle resolution that compared generations could never
// resolve an update remnant, and a recovery safety check that compared the
// incarnation alone could never clear one. Both paths are driven here against a
// NON-NIL operation store — the state every test with a zero WebConfig never
// reaches — with the name still live, which is the state a real operator's
// failed edit leaves behind.
func TestHostUpdateRemnantRepairsThroughRetryAndRecover(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	reg, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	ops, err := hostops.Open(filepath.Join(dir, "operations.json"))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{RemoteHostOpsStore: ops}, configPath, reg, nil)
	m.cfg.bootID = "boot-1"

	// The edit: it commits, its rebind refuses, and its remnant therefore pins
	// the RETIRED identity while the mirrored boundary below carries the
	// committed one — the mismatch the old resolution could not survive.
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected rebind failure") }
	before, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	result, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"}))
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	arm := result.HostMutationTeardownFailure
	if arm == nil {
		t.Fatalf("Update = %+v, want the teardown-failure arm", result)
	}
	m.testOnlyTeardown = nil
	// The COMMITTED row is the store's (the registry was never swapped: the
	// injected rebind failure replaced the manager call), and it carries the
	// bumped generation under the preserved incarnation — the pair the mirror
	// below publishes, and the one the old generation-comparing resolution could
	// not match.
	var committed hostreg.Host
	for _, entry := range m.cfg.store.snapshot() {
		if entry.Name == "side" {
			committed = entry
		}
	}
	if committed.Name == "" {
		t.Fatal("the committed edit left no store row")
	}
	if committed.Generation == before.Generation || committed.IncarnationID != before.IncarnationID {
		t.Fatalf("committed row = %+v, want a bumped generation under the same incarnation (before %+v)", committed, before)
	}
	remnant, ok := m.cfg.store.remnantByID(arm.RemnantID)
	if !ok {
		t.Fatalf("no durable remnant %q", arm.RemnantID)
	}
	if remnant.Generation != before.Generation || remnant.IncarnationID != before.IncarnationID {
		t.Fatalf("remnant = %+v, want the retired pair %d/%s", remnant, before.Generation, before.IncarnationID)
	}
	// The mirrored boundary is the name's live record: the committed generation
	// under the preserved incarnation, exactly what the mutation's own mirror
	// write leaves.
	if err := ops.MirrorBoundaries(map[string]hostops.Boundary{
		"side": {
			Generation:    committed.Generation,
			IncarnationID: committed.IncarnationID,
			PresenceEpoch: committed.PresenceEpoch,
		},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}

	// Retry: resolution must succeed on incarnation equality, and the pinned
	// rebind (re-applying the committed entry) must complete.
	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("TeardownRetry over an update remnant = %v, want the teardown-complete arm", err)
	}
	if retry.HostTeardownRetryCompleteLive == nil {
		t.Fatalf("retry = %+v, want the teardown-complete live arm", retry)
	}
	if _, open := m.cfg.store.markedRemnantFor("side"); open {
		t.Fatal("the fence still stands after the retry")
	}

	// Recover: a second failed edit whose SWAP landed and whose teardown then
	// failed — the real shape, and the one the reviewer named: the live entry is
	// the committed pair (bumped generation, preserved incarnation) while the
	// remnant pins the retired pair, so a safety check comparing the incarnation
	// alone would refuse forever.
	m.testOnlyTeardown = func(_ context.Context, name string) error {
		for _, entry := range m.cfg.store.snapshot() {
			if entry.Name == name {
				if err := m.cfg.hosts.Update(entry); err != nil {
					return err
				}
			}
		}
		return errors.New("injected teardown failure after the swap")
	}
	result, err = m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "edited2.example"}))
	if err != nil {
		t.Fatalf("second Update = %v", err)
	}
	m.testOnlyTeardown = nil
	arm = result.HostMutationTeardownFailure
	if arm == nil {
		t.Fatalf("second Update = %+v, want the teardown-failure arm", result)
	}
	cleared, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
		RemnantID: arm.RemnantID,
		Attestation: appwire.HostTeardownAttestation{
			Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("TeardownRecover over a live update remnant = %v, want the audited clearance", err)
	}
	if cleared.Outcome != appwire.HostTeardownOutcomeRecovered || cleared.HostKind != appwire.HostKindLive {
		t.Fatalf("recover response = %+v, want recovered-cleared for the live generation", cleared)
	}
	if _, open := m.cfg.store.markedRemnantFor("side"); open {
		t.Fatal("the fence still stands after the recovery")
	}
}

// TestHostMutationFinalizeKeepsAConcurrentSiblingCommit pins roborev's High on
// the finalizing write: `plan.Entries` is captured at plan time and the mutation
// lock is released across the post-commit teardown, so a concurrent mutation of
// a DIFFERENT, pre-existing host can commit inside that window. The finalizing
// write must carry the live set as it stands now — a stale snapshot would
// overwrite the sibling's commit on disk (file/memory divergence, and the
// sibling's commit lost on restart, or a removal resurrected).
func TestHostMutationFinalizeKeepsAConcurrentSiblingCommit(t *testing.T) {
	seed := func(t *testing.T) (*hubHostManager, string) {
		t.Helper()
		dir := t.TempDir()
		configPath := filepath.Join(dir, "hub.toml")
		if err := writeHubTOMLHosts(configPath, []hostreg.Host{
			{Name: "side", SSH: "side.example"},
			{Name: "keep", SSH: "keep.example"},
		}); err != nil {
			t.Fatalf("seed hub.toml: %v", err)
		}
		return bootHostManager(t, configPath), configPath
	}

	t.Run("a sibling update committed in the window survives", func(t *testing.T) {
		m, configPath := seed(t)
		// The outer mutation parks in its post-commit window — reservation held
		// for its own host — and, from there, a DIFFERENT pre-existing host is
		// edited to completion.
		parked := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		m.testOnlyParkPostCommit = func(name string) {
			if name != "side" {
				return
			}
			once.Do(func() { close(parked) })
			<-release
		}
		outer := make(chan error, 1)
		go func() {
			_, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
			outer <- err
		}()
		select {
		case <-parked:
		case <-time.After(5 * time.Second):
			t.Fatal("the outer mutation never reached its post-commit window")
		}
		if _, err := m.Update(context.Background(), updateRequest(t, m, "keep", appwire.HostEntry{Address: "keep2.example"})); err != nil {
			t.Fatalf("concurrent Update(keep) inside the window: %v", err)
		}
		close(release)
		if err := <-outer; err != nil {
			t.Fatalf("outer Remove(side): %v", err)
		}
		// The FILE is the authority here: reopen it and assert the sibling's
		// commit is still there.
		reopened, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("reload hub.toml: %v", err)
		}
		found := false
		for _, entry := range reopened.Hosts {
			if entry.Name == "keep" {
				found = true
				if entry.SSH != "keep2.example" {
					t.Fatalf("hub.toml kept %q for the sibling, want the edit that committed in the window", entry.SSH)
				}
			}
		}
		if !found {
			t.Fatalf("hub.toml lost the sibling entry entirely: %+v", reopened.Hosts)
		}
		// A restart over the same file must agree with the memory it left.
		restarted := bootHostManager(t, configPath)
		if entry, ok := restarted.cfg.hosts.Get("keep"); !ok || entry.SSH != "keep2.example" {
			t.Fatalf("restarted live entry for keep = %+v (ok=%v), want the window's edit", entry, ok)
		}
	})

	t.Run("a sibling removal committed in the window does not resurrect", func(t *testing.T) {
		m, configPath := seed(t)
		parked := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		m.testOnlyParkPostCommit = func(name string) {
			if name != "side" {
				return
			}
			once.Do(func() { close(parked) })
			<-release
		}
		outer := make(chan error, 1)
		go func() {
			_, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
			outer <- err
		}()
		select {
		case <-parked:
		case <-time.After(5 * time.Second):
			t.Fatal("the outer mutation never reached its post-commit window")
		}
		if _, err := m.Remove(context.Background(), removeRequest(t, m, "keep")); err != nil {
			t.Fatalf("concurrent Remove(keep) inside the window: %v", err)
		}
		close(release)
		if err := <-outer; err != nil {
			t.Fatalf("outer Remove(side): %v", err)
		}
		reopened, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("reload hub.toml: %v", err)
		}
		for _, entry := range reopened.Hosts {
			if entry.Name == "keep" {
				t.Fatalf("hub.toml resurrected the sibling the window removed: %+v", reopened.Hosts)
			}
		}
		if _, ok := reopened.Tombstones["keep"]; !ok {
			t.Fatalf("the window's removal left no tombstone: %+v", reopened.Tombstones)
		}
	})
}

// TestHostTeardownRecoverRefusalDoesNotWedgeTheName pins roborev's Medium: when
// the recover's locked re-check refuses, the attempt this call wrote must be
// fenced closed. Left open under this boot's epoch it would fence the name for
// the rest of the process — every later retry and recover refusing busy — which
// is exactly what the reviewer observed.
func TestHostTeardownRecoverRefusalDoesNotWedgeTheName(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	m.cfg.bootID = "boot-1"
	remnantID := mintRemnantID()
	remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host, "", m.nowTime())
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("stage remnant: %v", err)
	}
	// The locked re-check reads the STORE's live row while the first check reads
	// the registry: a store row carrying the remnant's own pinned pair makes the
	// first pass and the second refuse, deterministically.
	m.cfg.store.replace(hostreg.Host{
		Name:          host.Name,
		SSH:           host.SSH,
		Generation:    remnant.Generation,
		IncarnationID: remnant.IncarnationID,
		PresenceEpoch: host.PresenceEpoch,
	})

	_, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
		RemnantID: remnantID,
		Attestation: appwire.HostTeardownAttestation{
			Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z",
		},
	})
	if err == nil {
		t.Fatal("recover cleared a name whose live row still carries the pinned pair, want the safety refusal")
	}
	assertWireCode(t, err, appwire.CodeConflict)
	// The refusal must not leave a live attempt behind.
	if attemptID, live := m.liveAttemptInThisBoot(remnantID); live {
		t.Fatalf("the refused recover left attempt %q live, wedging the name until a restart", attemptID)
	}
	for id, attempt := range m.cfg.store.attemptsSnapshot() {
		if attempt.open() {
			t.Fatalf("attempt %q = %+v, want it fenced closed on the refusal", id, attempt)
		}
	}
	// The name is not falsely busy: a later recover refuses for the REAL reason
	// (the safety check), and a later retry proceeds past the attempt fence.
	_, err = m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
		RemnantID: remnantID,
		Attestation: appwire.HostTeardownAttestation{
			Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z",
		},
	})
	if err == nil {
		t.Fatal("the second recover cleared the name, want the same safety refusal")
	}
	if _, busy := errors.AsType[*hostops.BusyError](err); busy {
		t.Fatalf("the second recover refused busy (%v), want the safety refusal", err)
	}
	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	if err != nil {
		if _, busy := errors.AsType[*hostops.BusyError](err); busy {
			t.Fatalf("a later retry refused busy (%v), want the fence lifted", err)
		}
		t.Fatalf("a later retry = %v, want it past the attempt fence", err)
	}
	if retry.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("retry = %+v, want the teardown-complete arm", retry)
	}
}

// TestHostTeardownRetryRetiresTheRemovedHostsDerivedState pins roborev's Medium:
// a removal whose teardown failed at the commit point skipped the finish phase's
// derived-state drop, and `teardown-retry` was the only path to completion —
// without the drop, the removed host's source registration and cached session
// rows outlived the removal, and `sourceOnline`'s fail-open for an unregistered
// source kept rendering its sessions as live until a restart.
func TestHostTeardownRetryRetiresTheRemovedHostsDerivedState(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	sources := appsource.NewRegistry()
	cache := &hubcore.RemoteThreadCache{}
	var forgotten []string
	m := newHubHostManager(sources, nil, hubcore.WebConfig{RemoteThreadCache: cache}, configPath, hosts, nil)
	m.cfg.forgetLastGoodThreads = func(sourceID string) { forgotten = append(forgotten, sourceID) }
	// The seeded host's derived state, exactly as a running hub holds it: a
	// source registration, its cache generation, a name-keyed attach record, and
	// a retained last-known-good list.
	entry, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	m.registerSource(entry)
	m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventState, State: sshconn.StatePreflighting})
	if _, ok := sources.Source("side"); !ok {
		t.Fatal("the host has no source registration to retire")
	}
	if _, ok := cache.SourceGeneration("side"); !ok {
		t.Fatal("the host's source has no cache generation to retire")
	}
	m.cfg.state.mu.Lock()
	_, hasRecord := m.cfg.state.records["side"]
	m.cfg.state.mu.Unlock()
	if !hasRecord {
		t.Fatal("the host has no attach record to retire")
	}
	// The removal's teardown fails at the commit point, so the finish phase's
	// drop never runs.
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	result, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	arm := result.HostMutationTeardownFailureRemoved
	if arm == nil {
		t.Fatalf("Remove = %+v, want the teardown-failure arm", result)
	}
	m.testOnlyTeardown = nil

	// The retry completes the removal, and the name's derived state goes with it.
	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("TeardownRetry = %v", err)
	}
	if retry.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("retry = %+v, want the teardown-complete removed arm", retry)
	}
	if _, ok := sources.Source("side"); ok {
		t.Fatal("the removed host's source registration survived the retry")
	}
	if _, ok := cache.SourceGeneration("side"); ok {
		t.Fatal("the removed host's remote-thread cache entry survived the retry")
	}
	if got := forgotten; !slices.Equal(got, []string{"side"}) {
		t.Fatalf("forgotten = %v, want the removed host's own last-known-good drop", got)
	}
	m.cfg.state.mu.Lock()
	_, stillRecorded := m.cfg.state.records["side"]
	m.cfg.state.mu.Unlock()
	if stillRecorded {
		t.Fatal("the removed host's attach record survived the retry")
	}
}

// TestHostTeardownRetryDeadlineInterruptsAWedgedManager pins roborev's second
// Medium: the manager's teardown paths take no context — `RemoveHost` blocks on
// the per-host gate and then on the ssh child — so a genuinely wedged remote
// used to hold the retry forever, leaving the attempt open under this boot's
// epoch and every later retry/recover refusing busy until a restart. The retry
// must instead surface the terminal failure arm and keep the handle usable.
func TestHostTeardownRetryDeadlineInterruptsAWedgedManager(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	reg, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(reg, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	m := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{}, configPath, reg, nil)
	m.cfg.bootID = "boot-1"
	m.cfg.policy.teardownTimeout = 250 * time.Millisecond

	before, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	remnantID := mintRemnantID()
	remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", before, "", m.nowTime())
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("stage remnant: %v", err)
	}
	// The wedge: hold the host gate the manager's own teardown must take, from
	// the instant the retry releases its reservation until the deadline fires.
	released := make(chan struct{})
	var releaseOnce sync.Once
	unwedge := func() { releaseOnce.Do(func() { close(released) }) }
	m.testOnlyBeforePinnedRun = func(string) {
		release, gateErr := m.cfg.gate.TryAcquire("side", hostops.Holder{Kind: hostops.HolderManager, Activity: "wedge"})
		if gateErr != nil {
			t.Errorf("the wedge could not take the gate: %v", gateErr)
			return
		}
		go func() {
			<-released
			release()
		}()
	}
	defer unwedge()

	start := time.Now()
	result, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	if err != nil {
		t.Fatalf("TeardownRetry against a wedged manager = %v, want the terminal timeout arm", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("the retry took %s, want it bounded by the deadline", time.Since(start))
	}
	failed := result.HostTeardownRetryFailedRemoved
	if failed == nil {
		t.Fatalf("retry = %+v, want the committed-with-teardown-failure arm", result)
	}
	if failed.Seam != "remove-host" {
		t.Fatalf("timeout arm seam = %q, want remove-host", failed.Seam)
	}
	// The attempt is timed-out-but-open (takeover-able), not live: repair is not
	// wedged behind it.
	if attemptID, live := m.liveAttemptInThisBoot(remnantID); live {
		t.Fatalf("the timeout left attempt %q live, want it timed-out-but-open", attemptID)
	}
	closedAttempts := 0
	for _, attempt := range m.cfg.store.attemptsSnapshot() {
		if attempt.timedOut() {
			closedAttempts++
		}
	}
	if closedAttempts != 1 {
		t.Fatalf("attempts = %+v, want exactly one timed-out-but-open record", m.cfg.store.attemptsSnapshot())
	}
	if _, open := m.cfg.store.markedRemnantFor("side"); !open {
		t.Fatal("the timeout cleared the remnant, want it still open for a later retry")
	}
	// (c) A later retry is not falsely busy: the timed-out attempt is taken over
	// and the run proceeds to completion once the wedge lifts.
	//
	// The first run's teardown outlived its deadline: its RemoveHost still waits
	// for the gate the wedge holds, and runs once the wedge lifts. A free gate
	// alone doesn't mean that run is done: it may not have taken the gate yet,
	// and it takes it for "remove" the moment after, where the later retry would
	// meet it busy (#3178). That run removes the host from the registry while it
	// holds the gate, so the host gone from the registry and the gate free again
	// is the point past which it no longer touches the name.
	unwedge()
	waitFor(t, func() bool {
		if _, registered := reg.Get("side"); registered {
			return false
		}
		release, gateErr := m.cfg.gate.TryAcquire("side", hostops.Holder{Kind: hostops.HolderManager, Activity: "probe"})
		if gateErr != nil {
			return false
		}
		release()
		return true
	}, "the timed-out run never finished removing the host once the wedge lifted")
	m.testOnlyBeforePinnedRun = nil
	second, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	if err != nil {
		if _, busy := errors.AsType[*hostops.BusyError](err); busy {
			t.Fatalf("the later retry refused busy (%v), want it past the timed-out attempt", err)
		}
		t.Fatalf("the later retry = %v, want the teardown-complete arm", err)
	}
	if second.HostTeardownRetryCompleteRemoved == nil {
		t.Fatalf("the later retry = %+v, want the teardown-complete removed arm", second)
	}
	// (d) The gate is free once the retry finished: nothing holds the name.
	if release, gateErr := m.cfg.gate.TryAcquire("side", hostops.Holder{Kind: hostops.HolderManager, Activity: "probe-after"}); gateErr != nil {
		t.Fatalf("the gate is still held after the retry: %v", gateErr)
	} else {
		release()
	}
}

// TestHostTeardownRecoverRetiresTheClearedRemovalsDerivedState pins the same
// rule on the audited path: clearing a removal's remnant completes the removal,
// so the name's source registration, cache entry, attach record, and retained
// last-known-good list go with it exactly as they do on the retry's completion.
// A cleared removal leaves no live handle of the pinned incarnation behind, so
// nothing would ever retire that state afterwards.
func TestHostTeardownRecoverRetiresTheClearedRemovalsDerivedState(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	sources := appsource.NewRegistry()
	cache := &hubcore.RemoteThreadCache{}
	var forgotten []string
	m := newHubHostManager(sources, nil, hubcore.WebConfig{RemoteThreadCache: cache}, configPath, hosts, nil)
	m.cfg.forgetLastGoodThreads = func(sourceID string) { forgotten = append(forgotten, sourceID) }
	entry, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	m.registerSource(entry)
	m.observeEvent(sshconn.Event{Host: "side", Kind: sshconn.EventState, State: sshconn.StatePreflighting})

	// The removal commits and its teardown fails, so its finish-phase drop never
	// runs; the pinned handle is then unresolvable, which is the recover case.
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	result, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	arm := result.HostMutationTeardownFailureRemoved
	if arm == nil {
		t.Fatalf("Remove = %+v, want the teardown-failure arm", result)
	}
	m.testOnlyTeardown = nil
	remnant, ok := m.cfg.store.remnantByID(arm.RemnantID)
	if !ok {
		t.Fatalf("no durable remnant %q", arm.RemnantID)
	}
	remnant.CleanupHandle.Kind = "remote-lease"
	remnant.CleanupHandle.RemoteGuardFile = "guard.json"
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: arm.RemnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("restage remnant: %v", err)
	}
	// No handle of the pinned incarnation is reachable any more — the state the
	// recover exists for: the injected failure never dropped the registry entry,
	// so drop it the way a crash/restart leaves it, or the safety check
	// correctly refuses for a live handle carrying the pinned pair.
	if err := m.cfg.hosts.Remove("side"); err != nil {
		t.Fatalf("drop the live entry: %v", err)
	}

	cleared, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
		RemnantID: arm.RemnantID,
		Attestation: appwire.HostTeardownAttestation{
			Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("TeardownRecover = %v", err)
	}
	if cleared.Outcome != appwire.HostTeardownOutcomeRecovered || cleared.HostKind != appwire.HostKindRemoved {
		t.Fatalf("recover response = %+v, want recovered-cleared for the removed generation", cleared)
	}
	if _, ok := sources.Source("side"); ok {
		t.Fatal("the cleared removal's source registration survived the recovery")
	}
	if _, ok := cache.SourceGeneration("side"); ok {
		t.Fatal("the cleared removal's remote-thread cache entry survived the recovery")
	}
	if got := forgotten; !slices.Equal(got, []string{"side"}) {
		t.Fatalf("forgotten = %v, want the cleared removal's own last-known-good drop", got)
	}
	m.cfg.state.mu.Lock()
	_, stillRecorded := m.cfg.state.records["side"]
	m.cfg.state.mu.Unlock()
	if stillRecorded {
		t.Fatal("the cleared removal's attach record survived the recovery")
	}
}

// TestHostUpdateReplayAfterOrphanFinalizationDoesNotRestage pins roborev's
// Medium on the update path's ordering: the orphan finalizer can advance the
// name's identity while it finalizes a leftover marker (it re-applies the staged
// runtime set), so a dedup lookup built on a pair read BEFORE the finalization
// misses the receipt the finalization just wrote — and the replay of that
// mutationId stages the edit a second time. The pair must be read after the
// finalization, exactly as AddResult and RemoveResult already do it.
func TestHostUpdateReplayAfterOrphanFinalizationDoesNotRestage(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	// A leftover marker for the name under the RECORDED key, whose pinned target
	// is an edit: finalizing it re-applies the staged set, which registers the
	// entry and so advances the pair the pre-finalizer read would have held.
	replayKey := "replayed-mutation"
	receipt := newHostMutationReceipt(replayKey, hostMutationUpdate, host, m.nowTime())
	receipt.RemnantID = mintRemnantID()
	key := hostMutationReceiptKey(replayKey, "side", hostMutationUpdate, hostMutationIdentity{
		Generation:    host.Generation,
		IncarnationID: host.IncarnationID,
	})
	marker := HostStagedReceipt{
		Key:             key,
		StagedAt:        m.nowTime().UTC().Format(time.RFC3339),
		Phase:           hostStagedPhaseRuntimeSwapped,
		TeardownStarted: true,
		SwapStarted:     true,
		Provisional:     receipt,
		PendingTeardown: pendingTeardownFor(host, hostTeardownKindUpdate),
	}
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{marker: &pendingHostMarker{Name: "side", Marker: marker}}); err != nil {
		t.Fatalf("stage marker: %v", err)
	}
	beforeEdit, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the fixture host is not live")
	}

	// The replay: the same mutationId, carrying the pair the caller holds. Its
	// receipt is the one the finalization writes, so it must come back as the
	// recorded outcome — never a second stage of the edit.
	replay, err := m.UpdateResult(context.Background(), appwire.HostUpdateParams{
		Name:                  "side",
		Entry:                 appwire.HostEntry{Address: "replayed.example"},
		MutationID:            replayKey,
		ExpectedGeneration:    beforeEdit.Generation,
		ExpectedIncarnationID: beforeEdit.IncarnationID,
	})
	if err != nil {
		t.Fatalf("UpdateResult replay = %v", err)
	}
	if replay.HostMutationCommitted == nil {
		t.Fatalf("replay = %+v, want the recorded committed arm", replay)
	}
	// The recorded row is the one the finalization wrote — not a fresh stage of
	// the replay's own entry.
	if got := replay.HostMutationCommitted.Host.Address; got != "side.example" {
		t.Fatalf("replay row address = %q, want the recorded outcome (%q), not a fresh stage", got, "side.example")
	}
	if _, ok := m.cfg.store.stagedSnapshot()["side"]; ok {
		t.Fatal("the replay staged the edit instead of returning the recorded outcome")
	}
}

// TestHostUpdateFailureArmNamesItsOpenRemnant pins roborev's Low: the immediate
// `committed-with-teardown-failure` arm for an edit must render the same row
// fields as its replay — §11 has `openRemnantId`/`escalationAgeSec` "present
// exactly on rows whose name holds an open remnant", so the response a caller
// sees first cannot differ from the one a retry sees later.
func TestHostUpdateFailureArmNamesItsOpenRemnant(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected rebind failure") }
	result, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"}))
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	m.testOnlyTeardown = nil
	arm := result.HostMutationTeardownFailure
	if arm == nil {
		t.Fatalf("Update = %+v, want the teardown-failure arm", result)
	}
	if arm.Host.OpenRemnantID != arm.RemnantID {
		t.Fatalf("immediate arm row = %+v, want openRemnantId %q", arm.Host, arm.RemnantID)
	}
	// The replay renders the same row fields.
	replay, err := m.UpdateResult(context.Background(), updateRequestFor("side", 0, "", appwire.HostEntry{Address: "edited.example"}))
	if err == nil {
		// A fresh keyed update over the fenced name refuses `remnant-open`; the
		// recorded replay is what a lost-response retry carries, so ask with the
		// original key.
		t.Fatalf("a fresh keyed update = %+v, want the fence refusal", replay)
	}
	replayed, err := m.receiptArmForTest(arm.RemnantID)
	if err != nil {
		t.Fatalf("replay = %v", err)
	}
	if replayed.HostMutationTeardownFailure == nil {
		t.Fatalf("replay = %+v, want the recorded teardown-failure arm", replayed)
	}
	if replayed.HostMutationTeardownFailure.Host.OpenRemnantID != arm.RemnantID {
		t.Fatalf("replay row = %+v, want openRemnantId %q", replayed.HostMutationTeardownFailure.Host, arm.RemnantID)
	}
	if replayed.HostMutationTeardownFailure.Host.EscalationAgeSec != nil || arm.Host.EscalationAgeSec != nil {
		t.Fatalf("escalation age present before the bound: immediate %+v replay %+v",
			arm.Host.EscalationAgeSec, replayed.HostMutationTeardownFailure.Host.EscalationAgeSec)
	}
}

// receiptArmForTest renders one remnant's recorded outcome through the same
// replay arm a lost-response retry receives — the dedup lookup plus receiptArm —
// so a test can compare the immediate response's row fields with the replay's.
func (m *hubHostManager) receiptArmForTest(remnantID string) (appwire.HostMutationResult, error) {
	remnant, ok := m.cfg.store.remnantByID(remnantID)
	if !ok {
		return appwire.HostMutationResult{}, teardownUnknownKeyRefusal(remnantID)
	}
	scope, ok := parseHostReceiptScopedKey(remnant.MutationKey)
	if !ok {
		return appwire.HostMutationResult{}, appwire.InvalidParams("the remnant carries no scoped receipt key")
	}
	current, currentKnown := m.currentHostIdentity(scope.Name)
	hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID:   scope.MutationID,
		Name:         scope.Name,
		Kind:         scope.Kind,
		Current:      current,
		CurrentKnown: currentKnown,
	})
	if err != nil {
		return appwire.HostMutationResult{}, err
	}
	if hit == nil {
		return appwire.HostMutationResult{}, appwire.InvalidParams("no recorded receipt for the remnant")
	}
	return m.receiptArm(hit, scope.Kind), nil
}

// TestHostReAddStagedMarkerSurvivesItsOwnWrite pins roborev's Medium on the
// re-add: the step-(2) write stages its marker and then runs the re-add purge,
// which used to delete that very marker before the write landed — so a crash
// between the stage and the finalizing receipt left a re-add with no durable
// marker, and a later keyed replay found no receipt (failing the live-name
// duplicate check) instead of returning the recorded row.
func TestHostReAddStagedMarkerSurvivesItsOwnWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	// Tombstone the name, so the re-add's write takes the purge path.
	if _, err := m.Remove(context.Background(), removeRequest(t, m, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	if _, ok := m.cfg.store.tombstoneSnapshot()["side"]; !ok {
		t.Fatal("the removal left no tombstone for the re-add to purge")
	}
	// Capture the marker the step-(2) write stages, by parking at the stage seam.
	var staged HostStagedReceipt
	parked := false
	m.testOnlyAfterStage = func(name string) {
		if name != "side" {
			return
		}
		parked = true
		for _, marker := range m.cfg.store.stagedSnapshot() {
			staged = marker
		}
	}
	if _, err := m.AddResult(context.Background(), appwire.HostAddParams{
		Entry:      appwire.HostEntry{Name: "side", Address: "fresh.example"},
		MutationID: "re-add-key",
	}); err != nil {
		t.Fatalf("re-add = %v", err)
	}
	if !parked {
		t.Fatal("the re-add never staged a marker")
	}
	if staged.Key == "" {
		t.Fatal("the staged marker was absent from the store at the stage seam")
	}
	// The recorded receipt survives the commit, so the keyed replay returns the
	// recorded row instead of failing the duplicate check.
	replay, err := m.AddResult(context.Background(), appwire.HostAddParams{
		Entry:      appwire.HostEntry{Name: "side", Address: "fresh.example"},
		MutationID: "re-add-key",
	})
	if err != nil {
		t.Fatalf("replay = %v", err)
	}
	if replay.HostMutationCommitted == nil {
		t.Fatalf("replay = %+v, want the recorded committed arm", replay)
	}
	if _, ok := m.cfg.store.receiptsSnapshot()[staged.Key]; !ok {
		t.Fatalf("the staged marker's receipt %q is not in the store", staged.Key)
	}
	// The crash arm: a file captured mid-commit (the marker staged, the
	// finalizing receipt lost) must be repaired at boot — the marker is what
	// makes that possible, so booting over it finalizes the receipt.
	crashPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(crashPath, []hostreg.Host{{Name: "side", SSH: "fresh.example"}}); err != nil {
		t.Fatalf("seed the crash file: %v", err)
	}
	crashManager := bootHostManager(t, crashPath)
	entries := crashManager.cfg.store.snapshot()
	if err := crashManager.persistHosts(entries, entries, hostPersistChange{
		marker: &pendingHostMarker{Name: "side", Marker: staged},
	}); err != nil {
		t.Fatalf("stage the crash marker: %v", err)
	}
	if _, ok := crashManager.cfg.store.stagedSnapshot()["side"]; !ok {
		t.Fatal("the crash file carries no marker to repair")
	}
	recovered := bootHostManager(t, crashPath)
	if _, ok := recovered.cfg.store.stagedSnapshot()["side"]; ok {
		t.Fatal("boot left the staged marker unfinalized")
	}
	receipt, ok := recovered.cfg.store.receiptsSnapshot()[staged.Key]
	if !ok {
		t.Fatalf("boot wrote no receipt under %q", staged.Key)
	}
	if !receipt.BootRecovered {
		t.Fatalf("booted receipt = %+v, want bootRecovered true", receipt)
	}
}

// TestHostUpdateRepairKeepsTheCommittedGeneration pins roborev's Medium on the
// update repair path: re-stamping the file entry with the live host's identity
// made hostreg's pending-stamp match fail, so Registry.Update minted a fresh
// generation that no durable record ever carried. After a successful repair the
// live entry must carry the generation the store row and the file record.
func TestHostUpdateRepairKeepsTheCommittedGeneration(t *testing.T) {
	m, _, _ := newRemnantFixture(t)
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected rebind failure") }
	result, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"}))
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	arm := result.HostMutationTeardownFailure
	if arm == nil {
		t.Fatalf("Update = %+v, want the teardown-failure arm", result)
	}
	m.testOnlyTeardown = nil
	var committed hostreg.Host
	for _, entry := range m.cfg.store.snapshot() {
		if entry.Name == "side" {
			committed = entry
		}
	}
	if committed.Name == "" {
		t.Fatal("the committed edit left no store row")
	}

	retry, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID})
	if err != nil {
		t.Fatalf("TeardownRetry = %v", err)
	}
	if retry.HostTeardownRetryCompleteLive == nil {
		t.Fatalf("retry = %+v, want the teardown-complete live arm", retry)
	}
	live, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the repaired host is not live")
	}
	if live.Generation != committed.Generation {
		t.Fatalf("live generation = %d, want the committed %d the store and file record (a freshly minted generation is persisted nowhere)",
			live.Generation, committed.Generation)
	}
	if live.IncarnationID != committed.IncarnationID {
		t.Fatalf("live incarnation = %q, want the committed %q", live.IncarnationID, committed.IncarnationID)
	}
	// The original mutationId replays its recorded outcome rather than refusing
	// stale against a generation no record holds.
	replay, err := m.UpdateResult(context.Background(), updateRequestFor("side", committed.Generation, committed.IncarnationID, appwire.HostEntry{Address: "edited.example"}))
	if err != nil {
		t.Fatalf("replay = %v", err)
	}
	if replay.HostMutationCommitted == nil && replay.HostMutationTeardownFailure == nil {
		t.Fatalf("replay = %+v, want the recorded outcome", replay)
	}
}
