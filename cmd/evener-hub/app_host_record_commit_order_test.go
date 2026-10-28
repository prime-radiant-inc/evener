package hub

// A host mutation writes the store first (the staged marker, the provisional
// receipt and the identities it mints) and hub.toml second (the host change).
// These tests cut that order where the two writes disagree and require the hub
// to settle on what hub.toml holds, keep every minted generation spent, and go
// on taking host changes.

import (
	"context"
	"os"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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
