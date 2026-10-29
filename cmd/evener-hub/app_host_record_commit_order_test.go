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
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
// start, once the write works, rolls the commit back and resumes them.
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
	recovered, store = hub.boot(t, true)
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
// is not answered as a committed removal; the next start, which can write,
// rolls it back for good.
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
	recovered, store = hub.boot(t, true)
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
// marker is rolled back or finalized, host changes pause, and the next start,
// which can read the file, settles them.
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
	doc := hostBootDocument(cfg, store)
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

	again, store := hub.boot(t, true)

	if err := again.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still refused once hub.toml reads: %v", err)
	}
	if staged := mustRecords(t, store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("the next start left staged markers: %+v", staged)
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

// TestRollbackLeavesTheHostListInHubTOMLAlone pins the rollback's reach: it
// settles machine records, so a hub.toml the operator edited after the crash (a
// host edited, another removed) reads back exactly as they left it once the
// unrelated staged commit is rolled back.
func TestRollbackLeavesTheHostListInHubTOMLAlone(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}, {Name: "gone", SSH: "gone.example"}})
	m, _ := hub.boot(t, false)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	params.MutationID = "staged-mutation"
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "keep", SSH: "keep-by-hand.example"}, {Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("edit hub.toml by hand: %v", err)
	}
	edited := fileBytes(t, hub.configPath)

	_, store := hub.boot(t, true)

	if len(mustRecords(t, store).StagedReceipts) != 0 {
		t.Fatal("the staged commit was not rolled back")
	}
	if !bytes.Equal(fileBytes(t, hub.configPath), edited) {
		t.Fatal("the rollback rewrote the host list the operator left")
	}
}

// TestRollbackLeavesAnotherHostsTombstoneForTheCollisionRule pins what the
// rollback's write may touch: a removed name hub.toml carries again, with an
// open teardown remnant, is the collision rule's to handle (it is excluded until
// the remnant resolves), so settling an unrelated staged commit first must not
// consume its tombstone or publish the new identity, which would strand the
// remnant's cleanup handle.
func TestRollbackLeavesAnotherHostsTombstoneForTheCollisionRule(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}, {Name: "gone", SSH: "gone.example"}})
	m, _ := hub.boot(t, false)
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	removal, err := m.RemoveResult(context.Background(), removeRequest(t, m, "gone"))
	if err != nil || removal.HostMutationTeardownFailureRemoved == nil {
		t.Fatalf("Remove(gone) = (%+v, %v), want the teardown-failure arm", removal, err)
	}
	remnantID := removal.HostMutationTeardownFailureRemoved.RemnantID
	m.testOnlyTeardown = nil
	beforeBytes := fileBytes(t, hub.configPath)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	params.MutationID = "staged-mutation"
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, append(beforeBytes, []byte("\n[[hosts]]\nname = \"gone\"\nssh = \"gone-again.example\"\n")...), 0o600); err != nil {
		t.Fatalf("add gone again by hand: %v", err)
	}

	recovered, store := hub.boot(t, true)

	if _, tombstoned := mustRecords(t, store).Tombstones["gone"]; !tombstoned {
		t.Fatal("the unrelated rollback consumed the removed host's tombstone")
	}
	if _, err := recovered.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID}); err != nil {
		t.Fatalf("TeardownRetry after the recovery: %v", err)
	}
}

// TestRecoveryClassifiesAStagedRemovalAgainstTheFileAsItWas pins the order of the
// legacy-section strip: it rewrites hub.toml, so the comparison with the file the
// commit staged against has to come first. A strip that never landed before the
// crash leaves the sections in the file the removal staged against, and the
// unlanded removal must still read as unlanded.
func TestRecoveryClassifiesAStagedRemovalAgainstTheFileAsItWas(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, m, "side")
	leftover := "\n[generations.leftover]\ngeneration = 3\nincarnation_id = \"inc-leftover\"\npresence_epoch = 1\n"
	if err := os.WriteFile(hub.configPath, append(fileBytes(t, hub.configPath), []byte(leftover)...), 0o600); err != nil {
		t.Fatalf("leave legacy sections in hub.toml: %v", err)
	}
	staged := fileBytes(t, hub.configPath)
	params := removeRequest(t, m, "side")
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, staged, 0o600); err != nil {
		t.Fatalf("put hub.toml back as the removal staged against it: %v", err)
	}

	recovered, store := hub.boot(t, true)

	if _, got := testMutationIdentity(t, recovered, "side"); got != incarnation {
		t.Fatalf("side serves incarnation %q, want the one it had, %q: the unlanded removal was read as a landed one", got, incarnation)
	}
	if _, tombstoned := mustRecords(t, store).Tombstones["side"]; tombstoned {
		t.Fatal("the rolled-back removal left its tombstone")
	}
	requireNoRecordSections(t, hub.configPath)
}

// TestMigrationRaisesALiveHostToTheMirrorsHigherGeneration covers a legacy
// reconciliation whose raise of a live host never reached hub.toml: the import
// must not replace the mirror's higher generation with the file's.
func TestMigrationRaisesALiveHostToTheMirrorsHigherGeneration(t *testing.T) {
	fixture := newPipelineFixture(t, []hostreg.Host{pipelineEntry("keep", 3)}, nil)
	spent := hostops.Boundary{Generation: 40, IncarnationID: "inc-keep", PresenceEpoch: 3}
	if err := fixture.store.MirrorHostState(hostops.HostMirror{Boundaries: map[string]hostops.Boundary{"keep": spent}, Live: []string{"keep"}}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	m := fixture.legacyManager(t)

	m.migrateLegacyRecords(fixture.store)

	if mark := mustRecords(t, fixture.store).Generations["keep"]; mark.Generation < spent.Generation {
		t.Fatalf("the family holds generation %d for keep, below the mirror's %d", mark.Generation, spent.Generation)
	}
	if got, _ := m.cfg.hosts.Get("keep"); got.Generation < spent.Generation {
		t.Fatalf("keep is served at generation %d, below the mirror's %d", got.Generation, spent.Generation)
	}
}

// TestUnreadableHubTOMLAtStartupStillFencesAnOpenRemnantHost pins the fences'
// place in the attempt: the store's readable teardown remnants fence their hosts
// before the settlement can stop on an unreadable host file, so a host with an
// unresolved cleanup is not dialed in the meantime.
func TestUnreadableHubTOMLAtStartupStillFencesAnOpenRemnantHost(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	m.testOnlyTeardown = func(context.Context, string) error { return errors.New("injected teardown failure") }
	if result, err := m.UpdateResult(context.Background(), updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})); err != nil || result.HostMutationTeardownFailure == nil {
		t.Fatalf("Update = (%+v, %v), want the teardown-failure arm", result, err)
	}
	hub.restartStateRoot(t)
	reopened, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	boot := hostBootDocument(mustLoadConfig(t, hub.configPath), reopened)
	hosts, err := hostreg.NewSeeded(boot.Hosts, boot.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	previous := configReadFile
	configReadFile = func(string) ([]byte, error) { return nil, errors.New("injected read failure") }
	recovered := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: reopened, RemoteHostConfigPath: hub.configPath,
	}, hub.configPath, hosts, t.Logf)
	configReadFile = previous

	if recovered.mutationRefusal() == nil {
		t.Fatal("the settlement went ahead although hub.toml could not be read")
	}
	if _, fenced := recovered.openRemnantID("side"); !fenced {
		t.Fatal("the open remnant did not fence its host while the settlement was stopped")
	}
}

// TestRollbackOfAReAddBesideADifferentlyConfiguredHostOfTheSameName pins the
// restore's clash with a live entry: the re-add never landed, an operator has
// since configured the name differently, and the restored tombstone beside that
// live host is the collision rule's to settle, not a write the records refuse
// on every retry.
func TestRollbackOfAReAddBesideADifferentlyConfiguredHostOfTheSameName(t *testing.T) {
	const staged = "staged-re-add"
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	if _, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "again.example"}, MutationID: staged}
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.AddResult(context.Background(), params) })
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "configured-differently.example"}}); err != nil {
		t.Fatalf("configure side differently: %v", err)
	}

	recovered, _ := hub.boot(t, true)

	if err := recovered.mutationRefusal(); err != nil {
		t.Fatalf("host changes are still paused: %v", err)
	}
	if staged := mustRecords(t, hub.store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("recovery left staged markers: %+v", staged)
	}
	if row := hostRows(t, recovered)["side"]; row.Removed || row.Address != "configured-differently.example" {
		t.Fatalf("side serves %+v, want the host the operator configured", row)
	}
}

// flakyDirSync fails the store's directory sync while armed, which makes every
// store write publish its state and report RenameLanded.
type flakyDirSync struct {
	failing  atomic.Bool
	storeDir string
}

// sync fails only the store's own directory, the sync behind the rename; the
// syncs that create the directory tree before a first write still succeed.
func (f *flakyDirSync) sync(dir string) error {
	if f.failing.Load() && dir == f.storeDir {
		return errors.New("injected directory sync failure")
	}
	return nil
}

// TestAHostChangeWaitsForTheStoreToBeDurableBeforeTouchingHubTOML pins the order
// of the two files' durability: a store write whose rename landed but whose
// directory sync failed is published, but the host file does not move forward on
// it, so a power loss cannot keep the host change and lose its records. The
// change is refused as retryable, leaves hub.toml and the store as they were, and
// goes through once the sync works.
func TestAHostChangeWaitsForTheStoreToBeDurableBeforeTouchingHubTOML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "keep", SSH: "keep.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	storePath := hostops.StorePath(t.TempDir())
	dir := flakyDirSync{storeDir: filepath.Dir(storePath)}
	store, err := hostops.OpenWithDirSync(storePath, dir.sync)
	if err != nil {
		t.Fatalf("OpenWithDirSync: %v", err)
	}
	cfg := mustLoadConfig(t, configPath)
	boot := hostBootDocument(cfg, store)
	hosts, err := hostreg.NewSeeded(boot.Hosts, boot.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)
	before := fileBytes(t, configPath)

	dir.failing.Store(true)
	_, err = m.UpdateResult(context.Background(), updateRequest(t, m, "keep", appwire.HostEntry{Address: "edited.example"}))
	if err == nil {
		t.Fatal("the change went ahead on a store write that was not durable")
	}
	if !bytes.Equal(fileBytes(t, configPath), before) {
		t.Fatal("hub.toml moved forward on a store write that was not durable")
	}
	if staged := mustRecords(t, store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("the refused change left staged markers: %+v", staged)
	}

	dir.failing.Store(false)
	if _, err := m.UpdateResult(context.Background(), updateRequest(t, m, "keep", appwire.HostEntry{Address: "edited.example"})); err != nil {
		t.Fatalf("the change after the sync works: %v", err)
	}
}

// TestMigrationKeepsTheLegacySectionsUntilTheStoreIsDurable pins the same order
// for the move: the legacy sections are the records' other copy, so they stay in
// hub.toml while the import is published but not proven durable, and the next
// start, which can sync, drops them.
func TestMigrationKeepsTheLegacySectionsUntilTheStoreIsDurable(t *testing.T) {
	configPath, records := legacyHub(t)
	storePath := hostops.StorePath(t.TempDir())
	dir := flakyDirSync{storeDir: filepath.Dir(storePath)}
	dir.failing.Store(true)
	store, err := hostops.OpenWithDirSync(storePath, dir.sync)
	if err != nil {
		t.Fatalf("OpenWithDirSync: %v", err)
	}
	cfg := mustLoadConfig(t, configPath)
	boot := hostBootDocument(cfg, nil)
	hosts, err := hostreg.NewSeeded(boot.Hosts, boot.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath,
	}, configPath, hosts, t.Logf)

	requireFamilyEquals(t, store, records)
	if sections := legacyRecordSections(mustReadDocument(t, configPath)); len(sections) == 0 {
		t.Fatal("the legacy sections were dropped before the import was durable")
	}

	// The next start, with the directory sync working, drops them.
	dir.failing.Store(false)
	next := hostBootDocument(mustLoadConfig(t, configPath), store)
	nextHosts, err := hostreg.NewSeeded(next.Hosts, next.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: nextHosts, RemoteHostOpsStore: store, RemoteHostConfigPath: configPath,
	}, configPath, nextHosts, t.Logf)

	requireNoRecordSections(t, configPath)
}

// mustReadDocument reads hub.toml's raw TOML document.
func mustReadDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, _, err := readHubTOMLDocument(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return doc
}

// TestACompensationRestoresTheHostFileBeforeDroppingTheStagedRecords pins the
// order of an un-commit: the host change being taken back already reached
// hub.toml, so the file is restored first and the staged marker goes second. A
// crash between then leaves the host where it belongs with a marker boot rolls
// back; the other order would leave the staged commit's trace gone while the file
// still holds its change.
func TestACompensationRestoresTheHostFileBeforeDroppingTheStagedRecords(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}})
	m, store := hub.boot(t, false)
	m.testOnlyAfterStage = func(name string) {
		if err := m.cfg.hosts.Add(hostreg.Host{Name: name, SSH: "raced.example"}); err != nil {
			t.Errorf("race the registry: %v", err)
		}
	}
	var fileRestored, markerStillStaged bool
	hostFileFirstSeam = func() error {
		for _, h := range mustLoadConfig(t, hub.configPath).Hosts {
			if h.Name == "new" {
				return nil
			}
		}
		fileRestored = true
		_, markerStillStaged = mustRecords(t, store).StagedReceipts["new"]
		return nil
	}
	t.Cleanup(func() { hostFileFirstSeam = nil })

	_, err := m.AddResult(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}, MutationID: newTestMutationID()})
	requireRefusal(t, err)

	if !fileRestored || !markerStillStaged {
		t.Fatalf("the compensation did not write the host file before the records: fileRestored=%v markerStillStaged=%v", fileRestored, markerStillStaged)
	}
	if staged := mustRecords(t, store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("the compensation left staged markers: %+v", staged)
	}
}

// TestACompensationWhoseStoreWriteFailsStillSettlesTheManagersRecords pins what
// the manager believes once the host file is back: a store outage between the
// restore and the record write must not leave the manager holding the failed
// commit's provisional receipt, which a retry of the same mutation would replay
// as a committed one.
func TestACompensationWhoseStoreWriteFailsStillSettlesTheManagersRecords(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}})
	m, _ := hub.boot(t, false)
	m.testOnlyAfterStage = func(name string) {
		if err := m.cfg.hosts.Add(hostreg.Host{Name: name, SSH: "raced.example"}); err != nil {
			t.Errorf("race the registry: %v", err)
		}
	}
	hostFileFirstSeam = func() error { return errors.New("injected store outage") }
	t.Cleanup(func() { hostFileFirstSeam = nil })
	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}, MutationID: "failed-add"}

	if _, err := m.AddResult(context.Background(), params); err == nil {
		t.Fatal("the add succeeded although its registration failed")
	}

	if _, staged := m.cfg.store.stagedSnapshot()["new"]; staged {
		t.Fatal("the manager still holds the failed commit's staged marker")
	}
	m.testOnlyAfterStage = nil
	result, err := m.AddResult(context.Background(), params)
	if err == nil && result.HostMutationCommitted != nil {
		t.Fatalf("the retry replayed the failed commit as a committed add: %+v", result.HostMutationCommitted)
	}
}

// TestAnUnlandedRemovalIsStillUnlandedAfterAnUnrelatedSettingsEdit pins what
// counts as the removal's landing evidence: the host list. An operator who
// changed a setting that is not a host between the crash and the restart has not
// changed whether the removal reached hub.toml, so the host keeps its identity
// and the removal rolls back instead of being read as a landed removal followed
// by a re-add.
func TestAnUnlandedRemovalIsStillUnlandedAfterAnUnrelatedSettingsEdit(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, m, "side")
	staged := fileBytes(t, hub.configPath)
	params := removeRequest(t, m, "side")
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	edited := append([]byte("status_poll_interval = \"13s\"\n"), staged...)
	if err := os.WriteFile(hub.configPath, edited, 0o600); err != nil {
		t.Fatalf("edit a setting by hand: %v", err)
	}

	recovered, store := hub.boot(t, true)

	if _, got := testMutationIdentity(t, recovered, "side"); got != incarnation {
		t.Fatalf("side serves incarnation %q, want the one it had, %q: the unlanded removal was read as a landed one", got, incarnation)
	}
	if _, tombstoned := mustRecords(t, store).Tombstones["side"]; tombstoned {
		t.Fatal("the rolled-back removal left its tombstone")
	}
}

// TestAnUnlandedRemovalIsJudgedByItsOwnHostNotItsSiblings pins the evidence's
// scope: editing or reordering other hosts between the crash and the restart does
// not change whether this host's removal reached hub.toml.
func TestAnUnlandedRemovalIsJudgedByItsOwnHostNotItsSiblings(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}, {Name: "other", SSH: "other.example"}})
	m, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, m, "side")
	params := removeRequest(t, m, "side")
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	// The siblings are edited and reordered; side's own entry is as it was.
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "other", SSH: "other-by-hand.example"}, {Name: "side", SSH: "side.example"}, {Name: "keep", SSH: "keep.example"}}); err != nil {
		t.Fatalf("edit the siblings by hand: %v", err)
	}

	recovered, store := hub.boot(t, true)

	if _, got := testMutationIdentity(t, recovered, "side"); got != incarnation {
		t.Fatalf("side serves incarnation %q, want the one it had, %q", got, incarnation)
	}
	if _, tombstoned := mustRecords(t, store).Tombstones["side"]; tombstoned {
		t.Fatal("the rolled-back removal left its tombstone")
	}
}

// TestACompensationWhoseDirectorySyncFailsStillSettlesTheManagersRecords pins the
// landed-restore case: the compensation's rename put the host file back and only
// its directory sync failed, so the failed commit's staged marker and provisional
// receipt are settled all the same and a retry of the same request is not
// answered with a committed outcome.
func TestACompensationWhoseDirectorySyncFailsStillSettlesTheManagersRecords(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}})
	m, store := hub.boot(t, false)
	m.testOnlyAfterStage = func(name string) {
		if err := m.cfg.hosts.Add(hostreg.Host{Name: name, SSH: "raced.example"}); err != nil {
			t.Errorf("race the registry: %v", err)
		}
	}
	flakyHubTOMLDirSync(t, func(call int) bool { return call == 2 })
	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}, MutationID: "failed-add"}

	if _, err := m.AddResult(context.Background(), params); err == nil {
		t.Fatal("the add succeeded although its registration failed")
	}

	if _, staged := m.cfg.store.stagedSnapshot()["new"]; staged {
		t.Fatal("the manager still holds the failed commit's staged marker")
	}
	// The restored file is not proven durable, so the store keeps the marker a
	// boot rolls back until it is.
	if _, held := mustRecords(t, store).StagedReceipts["new"]; !held {
		t.Fatal("the store dropped the staged marker before the restored host file was durable")
	}
	m.testOnlyAfterStage = nil
	result, err := m.AddResult(context.Background(), params)
	if err == nil && result.HostMutationCommitted != nil {
		t.Fatalf("the retry replayed the failed commit as a committed add: %+v", result.HostMutationCommitted)
	}
}

// TestNoSingleReadFailureAtBootFinalizesARemovalThatNeverLanded pins that a
// transient failure of any one hub.toml read during recovery is never taken as
// evidence about a staged removal: whatever that boot defers, a clean restart
// finds the host with the identity it had and no tombstone.
func TestNoSingleReadFailureAtBootFinalizesARemovalThatNeverLanded(t *testing.T) {
	crash := func(t *testing.T) (*crashHub, string) {
		hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
		m, _ := hub.boot(t, false)
		_, incarnation := testMutationIdentity(t, m, "side")
		params := removeRequest(t, m, "side")
		unlanded := fileBytes(t, hub.configPath)
		armCut(m, cutStaged)
		runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
		// The removal staged and never reached hub.toml.
		if err := os.WriteFile(hub.configPath, unlanded, 0o600); err != nil {
			t.Fatalf("restore the file the crash left behind: %v", err)
		}
		return hub, incarnation
	}
	bootFailingRead := func(t *testing.T, hub *crashHub, failAt int) (*hubHostManager, *hostops.Store, int) {
		hub.restartStateRoot(t)
		cfg := mustLoadConfig(t, hub.configPath)
		store, err := hostops.Open(hostops.StorePath(hub.stateRoot))
		if err != nil {
			t.Fatalf("hostops.Open: %v", err)
		}
		previous := configReadFile
		reads := 0
		configReadFile = func(path string) ([]byte, error) {
			reads++
			if reads == failAt {
				return nil, errors.New("injected read failure")
			}
			return previous(path)
		}
		defer func() { configReadFile = previous }()
		doc := hostBootDocument(cfg, store)
		hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
		if err != nil {
			t.Fatalf("hostreg.NewSeeded: %v", err)
		}
		m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
			RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: hub.configPath,
		}, hub.configPath, hosts, t.Logf)
		return m, store, reads
	}

	hub, _ := crash(t)
	_, _, total := bootFailingRead(t, hub, 0)
	if total == 0 {
		t.Fatal("recovery read hub.toml no times")
	}
	for failAt := 1; failAt <= total; failAt++ {
		hub, incarnation := crash(t)
		bootFailingRead(t, hub, failAt)

		recovered, store, _ := bootFailingRead(t, hub, 0)

		if _, got := testMutationIdentity(t, recovered, "side"); got != incarnation {
			t.Fatalf("read %d failing: side serves incarnation %q after a clean restart, want %q", failAt, got, incarnation)
		}
		if _, tombstoned := mustRecords(t, store).Tombstones["side"]; tombstoned {
			t.Fatalf("read %d failing: the never-landed removal left its tombstone", failAt)
		}
	}
}

// TestAnUnsyncedCompensationKeepsTheStagedRecordsThroughLaterWrites pins that
// the staged records a compensation left in the store are not dropped by a later
// write until the restored host file's directory is proven durable: a write that
// leaves the host file as it is would otherwise discard the only evidence a
// crash needs to roll the change back.
func TestAnUnsyncedCompensationKeepsTheStagedRecordsThroughLaterWrites(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}})
	m, store := hub.boot(t, false)
	m.testOnlyAfterStage = func(name string) {
		if err := m.cfg.hosts.Add(hostreg.Host{Name: name, SSH: "raced.example"}); err != nil {
			t.Errorf("race the registry: %v", err)
		}
	}
	syncFails := true
	flakyHubTOMLDirSync(t, func(call int) bool { return call >= 2 && syncFails })
	params := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "new", Address: "new.example"}, MutationID: "failed-add"}
	if _, err := m.AddResult(context.Background(), params); err == nil {
		t.Fatal("the add succeeded although its registration failed")
	}
	m.testOnlyAfterStage = nil

	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{recordsOnly: true}); err == nil {
		t.Fatal("a write went through while the restored host file was not proven durable")
	}
	if _, held := mustRecords(t, store).StagedReceipts["new"]; !held {
		t.Fatal("a later write dropped the staged marker before the restored host file was durable")
	}

	syncFails = false
	if err := m.persistHosts(entries, entries, hostPersistChange{recordsOnly: true}); err != nil {
		t.Fatalf("the write is still refused once the directory syncs: %v", err)
	}
	if _, held := mustRecords(t, store).StagedReceipts["new"]; held {
		t.Fatal("the staged marker outlived the proven restore")
	}
}

// TestNoSingleReadFailureWhileStagingARemovalLetsItFinalizeAsLanded is the
// staging half of the read-failure rule: whichever one hub.toml read fails while
// a removal stages, the removal either refuses or stages with evidence a
// recovery can judge, so a crash before it reaches the file never costs the host
// its identity.
func TestNoSingleReadFailureWhileStagingARemovalLetsItFinalizeAsLanded(t *testing.T) {
	stageAndCrash := func(t *testing.T, failAt int) (*crashHub, string, int) {
		hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
		m, _ := hub.boot(t, false)
		_, incarnation := testMutationIdentity(t, m, "side")
		params := removeRequest(t, m, "side")
		unlanded := fileBytes(t, hub.configPath)
		armCut(m, cutStaged)
		previous := configReadFile
		reads := 0
		configReadFile = func(path string) ([]byte, error) {
			reads++
			if reads == failAt {
				return nil, errors.New("injected read failure")
			}
			return previous(path)
		}
		func() {
			defer func() {
				configReadFile = previous
				if recovered := recover(); recovered != nil {
					if _, crashed := recovered.(crashCut); !crashed {
						panic(recovered)
					}
				}
			}()
			_, _ = m.RemoveResult(context.Background(), params)
		}()
		// Whatever staged never reached hub.toml.
		if err := os.WriteFile(hub.configPath, unlanded, 0o600); err != nil {
			t.Fatalf("restore the file the crash left behind: %v", err)
		}
		return hub, incarnation, reads
	}
	_, _, total := stageAndCrash(t, 0)
	if total == 0 {
		t.Fatal("staging read hub.toml no times")
	}
	for failAt := 1; failAt <= total; failAt++ {
		hub, incarnation, _ := stageAndCrash(t, failAt)
		hub.restartStateRoot(t)
		store, err := hostops.Open(hostops.StorePath(hub.stateRoot))
		if err != nil {
			t.Fatalf("hostops.Open: %v", err)
		}
		recovered := restartOver(t, hub.configPath, store)

		if _, got := testMutationIdentity(t, recovered, "side"); got != incarnation {
			t.Fatalf("read %d failing while staging: side serves incarnation %q after the crash, want %q", failAt, got, incarnation)
		}
		if _, tombstoned := mustRecords(t, store).Tombstones["side"]; tombstoned {
			t.Fatalf("read %d failing while staging: the never-landed removal left its tombstone", failAt)
		}
	}
}

// TestBootFinalizationNeverRewritesTheHostList pins that settling a landed
// commit's marker at boot writes records alone: a host edited in hub.toml after
// the registry was built is the operator's to keep, not the registry's to put
// back.
func TestBootFinalizationNeverRewritesTheHostList(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	params := removeRequest(t, m, "side")
	armCut(m, cutFlipped)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })

	hub.restartStateRoot(t)
	store, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	doc := hostBootDocument(mustLoadConfig(t, hub.configPath), store)
	hosts, err := hostreg.NewSeeded(doc.Hosts, doc.Records.highWaterMarks())
	if err != nil {
		t.Fatalf("hostreg.NewSeeded: %v", err)
	}
	if err := writeHubTOMLHosts(hub.configPath, []hostreg.Host{{Name: "keep", SSH: "edited-by-hand.example"}}); err != nil {
		t.Fatalf("edit hub.toml by hand: %v", err)
	}
	newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostRegistry: hosts, RemoteHostOpsStore: store, RemoteHostConfigPath: hub.configPath,
	}, hub.configPath, hosts, t.Logf)

	var ssh string
	for _, host := range mustLoadConfig(t, hub.configPath).Hosts {
		if host.Name == "keep" {
			ssh = host.SSH
		}
	}
	if ssh != "edited-by-hand.example" {
		t.Fatalf("boot finalization put the registry's host list over the operator's edit: keep.ssh = %q", ssh)
	}
}

// TestBootRollbackWaitsUntilTheHostFileDirectoryIsDurable pins that a boot does
// not clear a staged commit's records on the strength of a hub.toml whose
// directory sync has not been proven: the file may be the restored one a
// compensation left unsynced, and the records are the evidence a crash needs.
func TestBootRollbackWaitsUntilTheHostFileDirectoryIsDurable(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	_, incarnation := testMutationIdentity(t, m, "side")
	params := removeRequest(t, m, "side")
	unlanded := fileBytes(t, hub.configPath)
	armCut(m, cutStaged)
	runUntilCrash(t, func() { _, _ = m.RemoveResult(context.Background(), params) })
	if err := os.WriteFile(hub.configPath, unlanded, 0o600); err != nil {
		t.Fatalf("restore the file the crash left behind: %v", err)
	}

	syncFails := true
	flakyHubTOMLDirSync(t, func(int) bool { return syncFails })
	hub.restartStateRoot(t)
	store, err := hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	waiting := restartOver(t, hub.configPath, store)

	if _, held := mustRecords(t, store).StagedReceipts["side"]; !held {
		t.Fatal("the boot cleared the staged records before the host file's directory was durable")
	}
	if waiting.mutationRefusal() == nil {
		t.Fatal("host changes are taken while the staged commit is unsettled")
	}

	syncFails = false
	hub.restartStateRoot(t)
	store, err = hostops.Open(hostops.StorePath(hub.stateRoot))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	recovered := restartOver(t, hub.configPath, store)

	if _, held := mustRecords(t, store).StagedReceipts["side"]; held {
		t.Fatal("the staged records outlived a durable host file")
	}
	if _, got := testMutationIdentity(t, recovered, "side"); got != incarnation {
		t.Fatalf("side serves incarnation %q after the rollback, want %q", got, incarnation)
	}
}

// TestBootRollsBackAFlippedEditWhoseHostFileWasRestored pins what recovery
// believes when a compensation restored hub.toml and its record write never
// landed, leaving the flipped marker behind: the file holds the host as it was
// before the edit, so the edit did not land, whatever phase the marker reached.
func TestBootRollsBackAFlippedEditWhoseHostFileWasRestored(t *testing.T) {
	hub := newCrashHub(t, []hostreg.Host{{Name: "keep", SSH: "keep.example"}, {Name: "side", SSH: "side.example"}})
	m, _ := hub.boot(t, false)
	before := fileBytes(t, hub.configPath)
	params := updateRequest(t, m, "side", appwire.HostEntry{Address: "edited.example"})
	armCut(m, cutFlipped)
	runUntilCrash(t, func() { _, _ = m.UpdateResult(context.Background(), params) })
	// The compensation put the host file back; its record write never landed.
	if err := os.WriteFile(hub.configPath, before, 0o600); err != nil {
		t.Fatalf("restore the host file: %v", err)
	}

	recovered, store := hub.boot(t, true)

	if staged := mustRecords(t, store).StagedReceipts; len(staged) != 0 {
		t.Fatalf("the boot left staged markers for an edit the host file does not hold: %+v", staged)
	}
	if entry, ok := recovered.cfg.hosts.Get("side"); !ok || entry.SSH != "side.example" {
		t.Fatalf("side serves %+v after the boot, want the entry the file holds", entry)
	}
	result, err := recovered.UpdateResult(context.Background(), params)
	if err == nil && result.HostMutationCommitted != nil {
		t.Fatalf("the replay reported the restored edit as committed: %+v", result.HostMutationCommitted)
	}
}
