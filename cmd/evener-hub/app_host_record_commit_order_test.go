package hub

// A host mutation writes the store first (the staged marker, the provisional
// receipt and the identities it mints) and hub.toml second (the host change).
// These tests cut that order where the two writes disagree and require the hub
// to settle on what hub.toml holds, keep every minted generation spent, and go
// on taking host changes.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// requireNoReceiptFor fails when the records hold any receipt for mutationID.
func requireNoReceiptFor(t *testing.T, records hostRecordSet, mutationID string) {
	t.Helper()
	for key := range records.MutationReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.MutationID == mutationID {
			t.Fatalf("the records hold a receipt %q for the mutation hub.toml never received", key)
		}
	}
}

func TestBootRollsBackACommitWhoseHostChangeNeverReachedHubTOML(t *testing.T) {
	const staged = "staged-mutation"
	cases := map[string]struct {
		mutate func(t *testing.T, m *hubHostManager) crashMutation
		// wantLive is the address side serves at once the commit is rolled back.
		wantLive string
		// stagedHost is the host the staged commit names.
		stagedHost string
	}{
		"add": {
			mutate: func(*testing.T, *hubHostManager) crashMutation {
				params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "ghost", Address: "ghost.example"}, MutationID: staged}
				return crashMutation{id: staged, issue: func(m *hubHostManager) (appwire.HostMutationResult, error) {
					return m.AddResult(context.Background(), params)
				}}
			},
			stagedHost: "ghost",
		},
		"update": {
			mutate: func(t *testing.T, m *hubHostManager) crashMutation {
				params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
				params.MutationID = staged
				return crashMutation{id: staged, issue: func(m *hubHostManager) (appwire.HostMutationResult, error) {
					return m.UpdateResult(context.Background(), params)
				}}
			},
			wantLive:   "side.example",
			stagedHost: "side",
		},
		"remove": {
			mutate: func(t *testing.T, m *hubHostManager) crashMutation {
				params := removeRequest(t, m, "side")
				params.MutationID = staged
				return crashMutation{id: staged, issue: func(m *hubHostManager) (appwire.HostMutationResult, error) {
					return m.RemoveResult(context.Background(), params)
				}}
			},
			wantLive:   "side.example",
			stagedHost: "side",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
			m, _ := hub.boot(t, false)
			beforeBytes := fileBytes(t, hub.configPath)
			mutation := tc.mutate(t, m)
			armCut(m, cutStaged)
			runUntilCrash(t, func() { _, _ = mutation.issue(m) })

			// The crash landed between the two writes: the store holds the staged
			// commit, hub.toml still holds the hosts it had.
			marker, ok := mustRecords(t, hub.store).StagedReceipts[tc.stagedHost]
			if !ok {
				t.Fatal("the store holds no staged marker after the cut")
			}
			spent := marker.Provisional.Generation
			if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
				t.Fatalf("put the pre-mutation hub.toml back: %v", err)
			}

			recovered, _ := hub.boot(t, true)

			records := mustRecords(t, hub.store)
			if len(records.StagedReceipts) != 0 {
				t.Fatalf("recovery left staged markers: %+v", records.StagedReceipts)
			}
			requireNoReceiptFor(t, records, staged)
			rows := hostRows(t, recovered)
			if tc.wantLive == "" {
				if row, present := rows[tc.stagedHost]; present {
					t.Fatalf("the rolled-back add left a row for %q: %+v", tc.stagedHost, row)
				}
			} else if row := rows[tc.stagedHost]; row.Removed || row.Address != tc.wantLive {
				t.Fatalf("the rolled-back commit left %q as %+v, want it live at %q", tc.stagedHost, row, tc.wantLive)
			}
			if rows["keep"].Removed {
				t.Fatal("the rollback disturbed a sibling host")
			}

			// The generation the rolled-back commit minted stays spent, and the hub
			// takes the host change again.
			if tc.wantLive == "" {
				if _, err := recovered.AddResult(context.Background(), appwire.HostAddParams{
					Entry: appwire.HostEntry{Name: "ghost", Address: "ghost.example"}, MutationID: newTestMutationID(),
				}); err != nil {
					t.Fatalf("the add again after the rollback: %v", err)
				}
				if again := hostRows(t, recovered)["ghost"]; again.Generation <= spent {
					t.Fatalf("the add again was handed generation %d, which the rolled-back commit already minted (%d)", again.Generation, spent)
				}
				return
			}
			if _, err := recovered.UpdateResult(context.Background(), updateRequest(t, recovered, "side", appwire.HostEntry{Address: "again.example"})); err != nil {
				t.Fatalf("the edit after the rollback: %v", err)
			}
			if after := hostRows(t, recovered)["side"]; after.Generation <= spent {
				t.Fatalf("the edit after the rollback was handed generation %d, not above the rolled-back commit's %d", after.Generation, spent)
			}
		})
	}
}

// TestAHostChangeHubTOMLRefusesLeavesTheStoreAsItWasAndRetriesCleanly pins the
// refusal half of the order: the store is written first, so a host change that
// then cannot reach hub.toml must take its staged marker, provisional receipt
// and minted identity back out of the store, and the same request, retried
// once hub.toml is writable, commits as if the first attempt had never been.
func TestAHostChangeHubTOMLRefusesLeavesTheStoreAsItWasAndRetriesCleanly(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}})
	m, store := hub.boot(t, false)
	before := mustRecords(t, store)
	boundariesBefore := store.Boundaries()

	failing := true
	previous := configReadFile
	configReadFile = func(path string) ([]byte, error) {
		if failing {
			return nil, os.ErrPermission
		}
		return previous(path)
	}
	t.Cleanup(func() { configReadFile = previous })

	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "ghost", Address: "ghost.example"}, MutationID: "retried-add"}
	if _, err := m.AddResult(context.Background(), params); err == nil {
		t.Fatal("an add whose host change cannot reach hub.toml succeeded")
	}
	requireSameMap(t, "host records", mustRecords(t, store).HostRecords, before.HostRecords)
	requireSameMap(t, "receipts", mustRecords(t, store).MutationReceipts, before.MutationReceipts)
	requireSameMap(t, "staged markers", mustRecords(t, store).StagedReceipts, before.StagedReceipts)
	if got := store.Boundaries(); len(got) != len(boundariesBefore) {
		t.Fatalf("the refused change left the boundary mirror at %v, was %v", got, boundariesBefore)
	}

	failing = false
	result, err := m.AddResult(context.Background(), params)
	if err != nil || result.HostMutationCommitted == nil {
		t.Fatalf("the retried add = (%+v, %v), want the committed arm", result, err)
	}
	if rows := hostRows(t, m); rows["ghost"].Address != "ghost.example" || rows["ghost"].Removed {
		t.Fatalf("the retried add left %+v", rows["ghost"])
	}
}

// TestBootDoesNotFinalizeACommitItCouldNotRollBack pins the rollback's failure
// posture: a staged commit whose host change never reached hub.toml, and whose
// rollback write fails, stays staged and is never finalized forward (that would
// commit an edit hub.toml does not hold); host changes pause, and the next
// attempt, once the write works, rolls the commit back and resumes them.
func TestBootDoesNotFinalizeACommitItCouldNotRollBack(t *testing.T) {
	const staged = "staged-mutation"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	beforeBytes := fileBytes(t, hub.configPath)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	params.MutationID = staged
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	bootRollbackFault = func(string) error { return errors.New("injected rollback write failure") }
	t.Cleanup(func() { bootRollbackFault = nil })
	recovered, store := hub.boot(t, true)

	records := mustRecords(t, store)
	marker, stillStaged := records.StagedReceipts["side"]
	if !stillStaged {
		t.Fatal("the commit that could not be rolled back lost its marker")
	}
	for key, receipt := range records.MutationReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.MutationID == staged && receipt.BootRecovered {
			t.Fatalf("the unrolled commit was finalized forward: %q = %+v", key, receipt)
		}
	}
	if row := hostRows(t, recovered)["side"]; row.Address != "side.example" {
		t.Fatalf("side serves %+v, want the address hub.toml still holds", row)
	}
	_, err := recovered.UpdateResult(context.Background(), updateRequest(t, recovered, "side", appwire.HostEntry{Address: "again.example"}))
	requireWireError(t, err, appwire.CodeUnavailable)
	// The client retrying the very request that was staged must not be told it
	// succeeded: hub.toml does not hold the edit.
	replay := updateRequest(t, recovered, "side", appwire.HostEntry{Address: "edited.example"})
	replay.MutationID = staged
	_, err = recovered.UpdateResult(context.Background(), replay)
	requireWireError(t, err, appwire.CodeUnavailable)

	bootRollbackFault = nil
	recovered.attachOps(nil)
	if err := recovered.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused after the rollback could be written: %v", err)
	}
	records = mustRecords(t, store)
	if len(records.StagedReceipts) != 0 {
		t.Fatalf("the retry left staged markers: %+v", records.StagedReceipts)
	}
	requireNoReceiptFor(t, records, staged)
	if after := hostRows(t, recovered)["side"]; after.Generation <= marker.Provisional.Generation-1 {
		t.Fatalf("side serves generation %d after the rollback", after.Generation)
	}
	if _, err := recovered.UpdateResult(context.Background(), updateRequest(t, recovered, "side", appwire.HostEntry{Address: "again.example"})); err != nil {
		t.Fatalf("the edit after the rollback: %v", err)
	}
}

// TestBootKeepsAnUnrolledRemovalStagedUntilItCanBeRolledBack pins the same
// posture for a removal: while its rollback cannot be written, the staged
// marker and tombstone stay, the host keeps serving, and a retried request
// is not answered as a committed removal; the retry that can write rolls it
// back for good.
func TestBootKeepsAnUnrolledRemovalStagedUntilItCanBeRolledBack(t *testing.T) {
	const staged = "staged-removal"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	beforeBytes := fileBytes(t, hub.configPath)
	params := removeRequest(t, m, "side")
	params.MutationID = staged
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	bootRollbackFault = func(string) error { return errors.New("injected rollback write failure") }
	t.Cleanup(func() { bootRollbackFault = nil })
	recovered, store := hub.boot(t, true)

	records := mustRecords(t, store)
	if _, stillStaged := records.StagedReceipts["side"]; !stillStaged {
		t.Fatal("the removal that could not be rolled back lost its marker")
	}
	if rows := hostRows(t, recovered); rows["side"].Removed || rows["side"].Address != "side.example" {
		t.Fatalf("side serves %+v while its removal is unsettled", rows["side"])
	}
	replay := removeRequestFor("side", params.ExpectedGeneration, params.ExpectedIncarnationID)
	replay.MutationID = staged
	_, err := recovered.RemoveResult(context.Background(), replay)
	requireWireError(t, err, appwire.CodeUnavailable)

	bootRollbackFault = nil
	recovered.attachOps(nil)
	if err := recovered.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused after the rollback could be written: %v", err)
	}
	records = mustRecords(t, store)
	if len(records.StagedReceipts) != 0 {
		t.Fatalf("the retry left staged markers: %+v", records.StagedReceipts)
	}
	requireNoReceiptFor(t, records, staged)
	if _, tombstoned := records.Tombstones["side"]; tombstoned {
		t.Fatal("the rolled-back removal left its tombstone")
	}
	if _, err := recovered.RemoveResult(context.Background(), removeRequest(t, recovered, "side")); err != nil {
		t.Fatalf("removing side after the rollback: %v", err)
	}
}

// receiptKeysFor lists the receipt keys the records hold for mutationID.
func receiptKeysFor(records hostRecordSet, mutationID string) []string {
	var keys []string
	for key := range records.MutationReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.MutationID == mutationID {
			keys = append(keys, key)
		}
	}
	return keys
}

// TestBootRollbackOfAnUnlandedReAddKeepsTheRemovalItConsumed pins the rollback's
// preservation: a re-add's first store write consumes the name's removal
// tombstone, so rolling the re-add back must put the tombstone back, with the
// generation the re-add spent, or a crash in that window would erase the
// removed host's retained record. The purge also takes the name's superseded
// receipts, which come back with it.
func TestBootRollbackOfAnUnlandedReAddKeepsTheRemovalItConsumed(t *testing.T) {
	const staged = "staged-re-add"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	const earlier = "earlier-edit"
	edit := updateRequest(t, m, "side", appwire.HostEntry{Address: "earlier.example"})
	edit.MutationID = earlier
	if _, err := m.UpdateResult(context.Background(), edit); err != nil {
		t.Fatalf("Update(side): %v", err)
	}
	heldBefore := receiptKeysFor(mustRecords(t, hub.store), earlier)
	if len(heldBefore) == 0 {
		t.Fatal("the earlier edit left no receipt")
	}
	if _, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	removed, ok := mustRecords(t, hub.store).Tombstones["side"]
	if !ok {
		t.Fatal("the removal left no tombstone")
	}
	beforeBytes := fileBytes(t, hub.configPath)
	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "again.example"}, MutationID: staged}
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.AddResult(context.Background(), params) })
	marker, ok := mustRecords(t, hub.store).StagedReceipts["side"]
	if !ok {
		t.Fatal("the store holds no staged marker after the cut")
	}
	if _, consumed := mustRecords(t, hub.store).Tombstones["side"]; consumed {
		t.Fatal("the fixture's re-add did not consume the tombstone in its first write")
	}
	spent := marker.Provisional.Generation
	spentEpoch := mustRecords(t, hub.store).Generations["side"].PresenceEpoch
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	recovered, _ := hub.boot(t, true)

	records := mustRecords(t, hub.store)
	if len(records.StagedReceipts) != 0 {
		t.Fatalf("recovery left staged markers: %+v", records.StagedReceipts)
	}
	if mark := records.Generations["side"]; mark.PresenceEpoch < spentEpoch || mark.Generation < spent {
		t.Fatalf("the high-water mark after the rollback is %+v, below the spent generation %d and presence epoch %d", mark, spent, spentEpoch)
	}
	tombstone, kept := records.Tombstones["side"]
	if !kept {
		t.Fatal("the rolled-back re-add erased the removal tombstone")
	}
	if tombstone.IncarnationID != removed.IncarnationID || tombstone.Generation < spent {
		t.Fatalf("restored tombstone = %+v, want incarnation %q and a generation of at least the spent %d", tombstone, removed.IncarnationID, spent)
	}
	if kept := receiptKeysFor(records, earlier); len(kept) != len(heldBefore) {
		t.Fatalf("the rolled-back re-add left receipts %v for the earlier edit, want the %v it held", kept, heldBefore)
	}
	if row := hostRows(t, recovered)["side"]; !row.Removed {
		t.Fatalf("side serves %+v after the rollback, want it removed", row)
	}
	if _, err := recovered.AddResult(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "again.example"}, MutationID: newTestMutationID(),
	}); err != nil {
		t.Fatalf("the re-add after the rollback: %v", err)
	}
}

// TestBootRollsBackAFirstAddThatNeverCreatedHubTOML pins the rollback over an
// absent host file: the first add stages in the store before it creates
// hub.toml, and a crash between the two leaves a staged commit for a file that
// does not exist. That is an empty host set, so the commit rolls back instead
// of finalizing as a host that was never written.
func TestBootRollsBackAFirstAddThatNeverCreatedHubTOML(t *testing.T) {
	const staged = "staged-first-add"
	hub := newCrashHub(t, nil)
	m, _ := hub.boot(t, false)
	if err := os.Remove(hub.configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove hub.toml: %v", err)
	}
	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "ghost", Address: "ghost.example"}, MutationID: staged}
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.AddResult(context.Background(), params) })
	if _, ok := mustRecords(t, hub.store).StagedReceipts["ghost"]; !ok {
		t.Fatal("the store holds no staged marker after the cut")
	}
	if err := os.Remove(hub.configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove hub.toml: %v", err)
	}

	recovered, _ := hub.boot(t, true)

	records := mustRecords(t, hub.store)
	if len(records.StagedReceipts) != 0 {
		t.Fatalf("recovery left staged markers: %+v", records.StagedReceipts)
	}
	requireNoReceiptFor(t, records, staged)
	if row, present := hostRows(t, recovered)["ghost"]; present {
		t.Fatalf("the rolled-back add left a row for ghost: %+v", row)
	}
	if _, err := recovered.AddResult(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "ghost", Address: "ghost.example"}, MutationID: staged,
	}); err != nil {
		t.Fatalf("the add again after the rollback: %v", err)
	}
	if row := hostRows(t, recovered)["ghost"]; row.Removed || row.Address != "ghost.example" {
		t.Fatalf("the retried add left %+v", row)
	}
}

// TestBootLeavesStagedCommitsAloneWhileHubTOMLCannotBeRead pins the posture for
// an unreadable host file: it says nothing about which commits landed, so no
// marker is rolled back or finalized, host changes pause, and the retry that can
// read the file settles them.
func TestBootLeavesStagedCommitsAloneWhileHubTOMLCannotBeRead(t *testing.T) {
	const staged = "staged-mutation"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	beforeBytes := fileBytes(t, hub.configPath)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	params.MutationID = staged
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	hub.restartStateRoot(t)
	cfg := mustLoadConfig(t, hub.configPath)
	store, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	doc := hostBootDocument(cfg, hub.configPath, store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	previous := configReadFile
	configReadFile = func(string) ([]byte, error) { return nil, errors.New("injected read failure") }
	recovered := newHubHostManager(appsource.NewRegistry(), nil,
		hubcore.WebConfig{RemoteHostOpsStore: store, RemoteHostConfigPath: hub.configPath}, hub.configPath, hosts, t.Logf)
	configReadFile = previous

	records := mustRecords(t, store)
	if _, stillStaged := records.StagedReceipts["side"]; !stillStaged {
		t.Fatal("a commit was settled although hub.toml could not be read")
	}
	for key, receipt := range records.MutationReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.MutationID == staged && receipt.BootRecovered {
			t.Fatalf("the staged commit was finalized without reading hub.toml: %q", key)
		}
	}
	if recovered.mutationRefusal() == nil {
		t.Fatal("host changes are taken while staged commits are unsettled")
	}

	recovered.attachOps(nil)

	if err := recovered.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused once hub.toml reads: %v", err)
	}
	if staged := mustRecords(t, store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("the retry left staged markers: %+v", staged)
	}
}

// TestBootRollbackOfAnUnlandedRemovalKeepsTheHostsIdentity pins what a removal
// that never reached hub.toml leaves behind: the host was never removed, so it
// serves under the identity it had, its operations are not marked removed, and
// the removal's tombstone is gone.
func TestBootRollbackOfAnUnlandedRemovalKeepsTheHostsIdentity(t *testing.T) {
	const staged = "staged-removal"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, store := hub.boot(t, false)
	generation, incarnation := testMutationIdentity(t, m, "side")
	op := seedOperationRecord(t, store, "side", "op-before", generation, incarnation)
	beforeBytes := fileBytes(t, hub.configPath)
	params := removeRequest(t, m, "side")
	params.MutationID = staged
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	recovered, restarted := hub.boot(t, true)

	gotGeneration, gotIncarnation := testMutationIdentity(t, recovered, "side")
	if gotGeneration != generation || gotIncarnation != incarnation {
		t.Fatalf("side serves (%d, %q) after the rollback, want its identity (%d, %q)", gotGeneration, gotIncarnation, generation, incarnation)
	}
	records := mustRecords(t, restarted)
	if _, tombstoned := records.Tombstones["side"]; tombstoned {
		t.Fatal("the rolled-back removal left its tombstone")
	}
	if record, ok := restarted.Record(op.ID); !ok || record.HostRemoved {
		t.Fatalf("the host's operation is %+v after the rollback, want it not marked removed", record)
	}
	if _, err := recovered.RemoveResult(context.Background(), removeRequest(t, recovered, "side")); err != nil {
		t.Fatalf("removing side after the rollback: %v", err)
	}
}

// TestRetryAfterAFailedInitialSettlementLeavesLiveOperationsRunning pins the
// crash recovery's place in the boot: operations a crash left in flight are
// interrupted by the first boot even when its settlement stops early, so the
// retry that finally succeeds never sweeps up operations this process admitted
// in between.
func TestRetryAfterAFailedInitialSettlementLeavesLiveOperationsRunning(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, store := hub.boot(t, false)
	served := hostRows(t, m)["keep"]
	crashLeft := seedOperationRecord(t, store, "keep", "left-by-the-crash", served.Generation, served.IncarnationID)
	beforeBytes := fileBytes(t, hub.configPath)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	params.MutationID = "staged-mutation"
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	bootRollbackFault = func(string) error { return errors.New("injected rollback write failure") }
	t.Cleanup(func() { bootRollbackFault = nil })
	recovered, restarted := hub.boot(t, true)
	if after, ok := restarted.Record(crashLeft.ID); !ok || after.State != hostops.StateInterrupted {
		t.Fatalf("the operation the crash left in flight is %+v after the first boot, want interrupted", after)
	}
	live := seedOperationRecord(t, restarted, "keep", "admitted-since", served.Generation, served.IncarnationID)

	bootRollbackFault = nil
	recovered.attachOps(nil)

	if err := recovered.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused after the retry: %v", err)
	}
	if after, ok := restarted.Record(live.ID); !ok || after.State != live.State {
		t.Fatalf("the retry moved the live operation from %q to %+v", live.State, after)
	}
}

// TestBootKeepsALandedRemovalWhenTheNameWasAddedAgainByHand pins the other
// reading of a staged removal beside a configured host: the removal landed
// (hub.toml lost the host) and an operator added the name again before the next
// boot. The replacement is a new incarnation, the removal's receipt stands, and
// a client retrying the removal does not take the replacement out.
func TestBootKeepsALandedRemovalWhenTheNameWasAddedAgainByHand(t *testing.T) {
	const staged = "staged-removal"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	generation, incarnation := testMutationIdentity(t, m, "side")
	params := removeRequest(t, m, "side")
	params.MutationID = staged
	armCut(m, cutFlipped)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side-again.example"}}); err != nil {
		t.Fatalf("add side again by hand: %v", err)
	}

	recovered, _ := hub.boot(t, true)

	gotGeneration, gotIncarnation := testMutationIdentity(t, recovered, "side")
	if gotIncarnation == incarnation || gotGeneration <= generation {
		t.Fatalf("the replacement serves (%d, %q), want a new incarnation above (%d, %q)", gotGeneration, gotIncarnation, generation, incarnation)
	}
	if len(receiptKeysFor(mustRecords(t, hub.store), staged)) == 0 {
		t.Fatal("the landed removal lost its receipt")
	}
	_, _ = recovered.RemoveResult(context.Background(), params)
	if row := hostRows(t, recovered)["side"]; row.Removed || row.Address != "side-again.example" {
		t.Fatalf("retrying the removal left the replacement as %+v", row)
	}
}

// TestLateAttachKeepsTheIdentityAnOpenTeardownRemnantPins pins which side wins
// when a store attaches after the hub minted identities of its own: the records
// are the durable truth, operations and teardown handles are bound to them, so
// the live host comes up as the incarnation they name and an open remnant can
// still be retried to completion.
func TestLateAttachKeepsTheIdentityAnOpenTeardownRemnantPins(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	generation, incarnation := testMutationIdentity(t, m, "side")
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	result, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"}))
	if err != nil || result.HostMutationTeardownFailure == nil {
		t.Fatalf("Update = (%+v, %v), want the teardown-failure arm", result, err)
	}
	remnantID := result.HostMutationTeardownFailure.RemnantID
	stored := mustRecords(t, hub.store).Generations["side"]
	if stored.IncarnationID != incarnation || stored.Generation < generation {
		t.Fatalf("the stored mark for side is %+v after the edit from (%d, %q)", stored, generation, incarnation)
	}

	hub.restartStateRoot(t)
	reopened, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, hub.configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	late := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: hub.configPath, RemoteHostOpsUnavailable: true,
	}, hub.configPath, hosts, t.Logf)
	late.attachOps(reopened)

	if lateGeneration, lateIncarnation := testMutationIdentity(t, late, "side"); lateIncarnation != stored.IncarnationID || lateGeneration != stored.Generation {
		t.Fatalf("side serves (%d, %q) after the late attach, want the stored (%d, %q)", lateGeneration, lateIncarnation, stored.Generation, stored.IncarnationID)
	}
	retry, err := late.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	if err != nil {
		t.Fatalf("TeardownRetry after the late attach: %v", err)
	}
	if retry.HostTeardownRetryCompleteLive == nil {
		t.Fatalf("retry = %+v, want the teardown-complete live arm", retry)
	}
}

// TestLateAttachRestoresTheIdentityOfAHostWhoseStagedRemovalNeverLanded is the
// late-attach half of the unlanded-removal rule: a hub that started without its
// store minted an incarnation for the host, and the attach gives back the one
// the removal's tombstone names.
func TestLateAttachRestoresTheIdentityOfAHostWhoseStagedRemovalNeverLanded(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, m, "side")
	beforeBytes := fileBytes(t, hub.configPath)
	params := removeRequest(t, m, "side")
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	hub.restartStateRoot(t)
	reopened, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, hub.configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	late := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: hub.configPath, RemoteHostOpsUnavailable: true,
	}, hub.configPath, hosts, t.Logf)
	late.attachOps(reopened)

	if _, got := testMutationIdentity(t, late, "side"); got != incarnation {
		t.Fatalf("side serves incarnation %q after the late attach, want the one it had, %q", got, incarnation)
	}
	if _, tombstoned := mustRecords(t, reopened).Tombstones["side"]; tombstoned {
		t.Fatal("the rolled-back removal left its tombstone")
	}
}

// TestBootKeepsACommitWhoseRuntimeSwapStartedWhenHubTOMLWasEditedAfterwards pins
// the phase evidence: the runtime swap starts only after the host change is in
// hub.toml, so an operator's later edit of the same host does not make the
// commit look unlanded, and the committed mutation keeps its receipt.
func TestBootKeepsACommitWhoseRuntimeSwapStartedWhenHubTOMLWasEditedAfterwards(t *testing.T) {
	const staged = "swapped-mutation"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	params.MutationID = staged
	armCut(m, cutFlipped)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "by-hand.example"}}); err != nil {
		t.Fatalf("edit side by hand: %v", err)
	}

	hub.boot(t, true)

	if len(receiptKeysFor(mustRecords(t, hub.store), staged)) == 0 {
		t.Fatal("the committed mutation lost its receipt to a later edit of the file")
	}
}

// TestADegradedBootLeavesHubTOMLBytesAlone pins the write posture while the
// configured store has not opened: the hosts are served from what the file
// holds and nothing is rewritten, so a later settlement compares staged commits
// with the file exactly as they saw it.
func TestADegradedBootLeavesHubTOMLBytesAlone(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}})
	before := fileBytes(t, hub.configPath)
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, hub.configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}

	newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: hub.configPath, RemoteHostOpsUnavailable: true,
	}, hub.configPath, hosts, t.Logf)

	if !bytes.Equal(fileBytes(t, hub.configPath), before) {
		t.Fatal("a boot without its store rewrote hub.toml")
	}
}

// TestLateAttachDoesNotLeaveAChannelBoundToAnIdentityThatChanged pins the
// coordination with the SSH manager: a host that connected while the store was
// unavailable is rebound to its stored incarnation through the manager, so the
// channel it held is torn down with the old registration instead of staying
// attached to an entry that no longer matches it.
func TestLateAttachDoesNotLeaveAChannelBoundToAnIdentityThatChanged(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	first, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, first, "side")

	hub.restartStateRoot(t)
	reopened, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, hub.configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(hosts, sshconn.Options{Runner: &attachedUpdateRunner{}})
	t.Cleanup(func() { _ = manager.Close() })
	late := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: hub.configPath, RemoteHostOpsUnavailable: true,
	}, hub.configPath, hosts, t.Logf)
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure while the store is unavailable: %v", err)
	}

	late.attachOps(reopened)

	entry, _ := hosts.Get("side")
	if entry.IncarnationID != incarnation {
		t.Fatalf("side serves incarnation %q after the late attach, want the stored %q", entry.IncarnationID, incarnation)
	}
	if ch, attached := manager.ChannelIfAttached("side"); attached && !ch.MatchesRegistration(entry) {
		t.Fatal("the channel from before the attach is still attached to an identity the host no longer has")
	}
}

// TestLateAttachWaitsForABusyHostBeforeTakingItsStoredIdentity pins the
// failure posture of the rebind: a host whose gate is held cannot be rebound,
// so the settlement stays pending with host changes paused instead of recording
// the minted identity over the stored one, and a retry after the gate frees
// finishes it.
func TestLateAttachWaitsForABusyHostBeforeTakingItsStoredIdentity(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	first, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, first, "side")

	hub.restartStateRoot(t)
	reopened, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, hub.configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(hosts, sshconn.Options{Runner: &attachedUpdateRunner{}})
	t.Cleanup(func() { _ = manager.Close() })
	late := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: hub.configPath, RemoteHostOpsUnavailable: true,
	}, hub.configPath, hosts, t.Logf)
	acquired, err := late.acquireHostGate("side", hostops.Holder{Kind: hostops.HolderOperation, OperationID: "busy"})
	if err != nil {
		t.Fatalf("hold the gate: %v", err)
	}
	release := sync.OnceFunc(acquired)
	t.Cleanup(release)

	late.attachOps(reopened)

	if late.mutationRefusal() == nil {
		t.Fatal("host changes resumed although side could not take its stored identity")
	}
	if got := mustRecords(t, reopened).HostRecords["side"].IncarnationID; got != incarnation {
		t.Fatalf("the stored incarnation became %q while the host was busy, want %q kept", got, incarnation)
	}

	release()
	late.attachOps(nil)

	if err := late.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still paused after the gate freed: %v", err)
	}
	if _, got := testMutationIdentity(t, late, "side"); got != incarnation {
		t.Fatalf("side serves incarnation %q after the retry, want the stored %q", got, incarnation)
	}
}

// lateAttachOverAReAddedName builds a hub that started without its store over a
// hub.toml in which the removed name "side" was added again by hand, and
// connects side through a real SSH manager before the store attaches.
func lateAttachOverAReAddedName(t *testing.T) (*hubHostManager, *sshconn.Manager, *hostops.Store, *hostreg.Registry) {
	t.Helper()
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	first, _ := hub.boot(t, false)
	if _, err := first.RemoveResult(context.Background(), removeRequest(t, first, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side-again.example"}}); err != nil {
		t.Fatalf("add side again by hand: %v", err)
	}
	hub.restartStateRoot(t)
	reopened, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(mustLoadConfig(t, hub.configPath)))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(hosts, sshconn.Options{Runner: &attachedUpdateRunner{}})
	t.Cleanup(func() { _ = manager.Close() })
	late := newHubHostManager(appsource.NewRegistry(), manager, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostConfigPath: hub.configPath, RemoteHostOpsUnavailable: true,
	}, hub.configPath, hosts, t.Logf)
	if _, err := manager.Ensure(context.Background(), "side"); err != nil {
		t.Fatalf("Ensure while the store is unavailable: %v", err)
	}
	return late, manager, reopened, hosts
}

// TestLateAttachRetiresTheConnectionOfAHostItRebasesAsACollision pins the boot
// collision rule under a running hub: a removed name that hub.toml carries again
// is rebased as a new incarnation, and the channel the host held under the old
// identity is torn down under the gate rather than left attached to it.
func TestLateAttachRetiresTheConnectionOfAHostItRebasesAsACollision(t *testing.T) {
	late, manager, reopened, hosts := lateAttachOverAReAddedName(t)

	late.attachOps(reopened)

	entry, _ := hosts.Get("side")
	if ch, attached := manager.ChannelIfAttached("side"); attached && !ch.MatchesRegistration(entry) {
		t.Fatal("the channel from before the attach is still attached to an identity the collision replaced")
	}
	if err := late.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still paused: %v", err)
	}
}

// TestLateAttachWaitsForABusyHostBeforeRebasingACollision is the busy half: a
// host whose gate is held is not rebased under its running work; the settlement
// stays pending and finishes once the gate frees.
func TestLateAttachWaitsForABusyHostBeforeRebasingACollision(t *testing.T) {
	late, _, reopened, hosts := lateAttachOverAReAddedName(t)
	before, _ := hosts.Get("side")
	acquired, err := late.acquireHostGate("side", hostops.Holder{Kind: hostops.HolderOperation, OperationID: "busy"})
	if err != nil {
		t.Fatalf("hold the gate: %v", err)
	}
	release := sync.OnceFunc(acquired)
	t.Cleanup(release)

	late.attachOps(reopened)

	if late.mutationRefusal() == nil {
		t.Fatal("host changes resumed although side was busy")
	}
	if during, _ := hosts.Get("side"); during.IncarnationID != before.IncarnationID || during.Generation != before.Generation {
		t.Fatalf("a busy host was rebased: %+v became %+v", before, during)
	}

	release()
	late.attachOps(nil)

	if err := late.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still paused after the gate freed: %v", err)
	}
	if after, _ := hosts.Get("side"); after.IncarnationID == before.IncarnationID {
		t.Fatal("the collision was never rebased after the gate freed")
	}
}
