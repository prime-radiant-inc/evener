package hub

// A host mutation writes the store first (the staged marker, the provisional
// receipt and the identities it mints) and hub.toml second (the host change).
// These tests cut that order where the two writes disagree and require the hub
// to settle on what hub.toml holds, keep every minted generation spent, and go
// on taking host changes.

import (
	"context"
	"errors"
	"os"
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
	if err := os.WriteFile(hub.configPath, beforeBytes, 0o600); err != nil {
		t.Fatalf("put the pre-mutation hub.toml back: %v", err)
	}

	recovered, _ := hub.boot(t, true)

	records := mustRecords(t, hub.store)
	if len(records.StagedReceipts) != 0 {
		t.Fatalf("recovery left staged markers: %+v", records.StagedReceipts)
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
