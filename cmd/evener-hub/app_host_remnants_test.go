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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
