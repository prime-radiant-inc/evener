package hub

// Registry tests for the guarded host mutations and their durable receipts
// (registry spec 08 §4, §5, §6, §12; §16's "update-generation tests" and
// "Mutation-idempotency tests" bullets): guard-field presence, the under-lock
// pair check, dedup-first replay (direct and superseded arms), the typed
// refusals, the busy classes, the receipt's atomic write and boot load, and
// the un-commit paths that must leave no receipt behind.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// testMutationCounter mints one distinct mutation id per guarded call, the way
// a client mints a fresh key per mutation.
var testMutationCounter atomic.Uint64

func newTestMutationID() string {
	return fmt.Sprintf("test-mutation-%d", testMutationCounter.Add(1))
}

// testMutationIdentity reads name's live (generation, incarnation id) pair, the
// pair a guarded request must echo.
func testMutationIdentity(t *testing.T, m *hubHostManager, name string) (uint64, string) {
	t.Helper()
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		t.Fatalf("host %q is not live", name)
	}
	return host.Generation, host.IncarnationID
}

// updateRequest builds an update carrying a fresh mutation id and the live
// pair — what the UI's edit path sends.
func updateRequest(t *testing.T, m *hubHostManager, name string, entry appwire.HostEntry) appwire.HostUpdateParams {
	t.Helper()
	generation, incarnation := testMutationIdentity(t, m, name)
	return appwire.HostUpdateParams{
		Name:                  name,
		Entry:                 entry,
		MutationID:            newTestMutationID(),
		ExpectedGeneration:    generation,
		ExpectedIncarnationID: incarnation,
	}
}

// removeRequest builds a remove carrying a fresh mutation id and the live pair.
func removeRequest(t *testing.T, m *hubHostManager, name string) appwire.HostRemoveParams {
	t.Helper()
	generation, incarnation := testMutationIdentity(t, m, name)
	return appwire.HostRemoveParams{
		Name:                  name,
		MutationID:            newTestMutationID(),
		ExpectedGeneration:    generation,
		ExpectedIncarnationID: incarnation,
	}
}

// updateRequestFor builds an update with explicit expectations, for refusals
// that fire without a live pair to read (an unknown name) or in a goroutine
// where a test helper must not run.
func updateRequestFor(name string, generation uint64, incarnation string, entry appwire.HostEntry) appwire.HostUpdateParams {
	return appwire.HostUpdateParams{
		Name:                  name,
		Entry:                 entry,
		MutationID:            newTestMutationID(),
		ExpectedGeneration:    generation,
		ExpectedIncarnationID: incarnation,
	}
}

// removeRequestFor is updateRequestFor's remove sibling.
func removeRequestFor(name string, generation uint64, incarnation string) appwire.HostRemoveParams {
	return appwire.HostRemoveParams{
		Name:                  name,
		MutationID:            newTestMutationID(),
		ExpectedGeneration:    generation,
		ExpectedIncarnationID: incarnation,
	}
}

// wireRemoveRequest reads name's current pair from the hub's own list row — the
// UI's echo-the-row path — and builds a guarded remove for a wire-level test
// that talks to the real server over appwire.
func wireRemoveRequest(t *testing.T, client *appwire.Client, name string) appwire.HostRemoveParams {
	t.Helper()
	var list appwire.HostListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostList, appwire.EmptyParams{}, &list); err != nil {
		t.Fatalf("evener/host/list reading the pair for %q: %v", name, err)
	}
	for _, row := range list.Hosts {
		if row.Name == name {
			return appwire.HostRemoveParams{
				Name:                  name,
				MutationID:            newTestMutationID(),
				ExpectedGeneration:    row.Generation,
				ExpectedIncarnationID: row.IncarnationID,
			}
		}
	}
	t.Fatalf("host %q is not listed, so there is no pair to guard its removal with", name)
	return appwire.HostRemoveParams{}
}

// receiptsFor returns the stored receipts whose mutation id matches, decoded
// from their scoped keys.
func receiptsFor(t *testing.T, m *hubHostManager, mutationID string) map[string]HostMutationReceipt {
	t.Helper()
	out := map[string]HostMutationReceipt{}
	for key, receipt := range m.cfg.store.receiptsSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			t.Fatalf("stored receipt key %q does not parse", key)
		}
		if scope.MutationID == mutationID {
			out[key] = receipt
		}
	}
	return out
}

// receiptField reads one field from a typed refusal's JSON data.
func receiptField(t *testing.T, data map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(data[key], &value); err != nil {
		t.Fatalf("refusal data carries no readable %q: %v", key, err)
	}
	return value
}

// TestHostMutationGuardPresenceRefusals pins §4/§11's presence rule: update and
// remove require mutationId, expectedGeneration, and expectedIncarnationId
// together, and a request missing any of them is a field-carrying validation
// refusal that commits nothing — the file bytes and the live generation are
// untouched.
func TestHostMutationGuardPresenceRefusals(t *testing.T) {
	f := newUpdateFixture(t)
	generation, incarnation := testMutationIdentity(t, f.m, "side")
	entry := appwire.HostEntry{Address: "edited.example"}

	updateCases := []struct {
		name   string
		params appwire.HostUpdateParams
		field  string
	}{
		{
			name:   "missing mutationId",
			params: appwire.HostUpdateParams{Name: "side", Entry: entry, ExpectedGeneration: generation, ExpectedIncarnationID: incarnation},
			field:  "mutationId",
		},
		{
			name:   "missing expectedGeneration",
			params: appwire.HostUpdateParams{Name: "side", Entry: entry, MutationID: newTestMutationID(), ExpectedIncarnationID: incarnation},
			field:  "expectedGeneration",
		},
		{
			name:   "missing expectedIncarnationId",
			params: appwire.HostUpdateParams{Name: "side", Entry: entry, MutationID: newTestMutationID(), ExpectedGeneration: generation},
			field:  "expectedIncarnationId",
		},
		{
			name:   "over-long mutationId",
			params: appwire.HostUpdateParams{Name: "side", Entry: entry, MutationID: strings.Repeat("m", MaxHostMutationIDBytes+1), ExpectedGeneration: generation, ExpectedIncarnationID: incarnation},
			field:  "mutationId",
		},
	}
	for _, tc := range updateCases {
		t.Run("update/"+tc.name, func(t *testing.T) {
			before := readHubTOMLBytes(t, f.configPath)
			_, err := f.m.Update(context.Background(), tc.params)
			info, data, code := deployWireInfo(t, err)
			if info != appwire.ErrorInvalidHostField || code != appwire.CodeInvalidParams {
				t.Fatalf("refusal = (%q, %d), want (%q, %d)", info, code, appwire.ErrorInvalidHostField, appwire.CodeInvalidParams)
			}
			if field := receiptField(t, data, "field"); field != tc.field {
				t.Fatalf("refusal field = %q, want %q", field, tc.field)
			}
			if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, before) {
				t.Fatalf("a presence refusal rewrote hub.toml:\n%s", after)
			}
			if got, _ := testMutationIdentity(t, f.m, "side"); got != generation {
				t.Fatalf("generation moved to %d on a presence refusal, want %d", got, generation)
			}
		})
	}

	removeCases := []struct {
		name   string
		params appwire.HostRemoveParams
		field  string
	}{
		{
			name:   "missing mutationId",
			params: appwire.HostRemoveParams{Name: "side", ExpectedGeneration: generation, ExpectedIncarnationID: incarnation},
			field:  "mutationId",
		},
		{
			name:   "missing expectedGeneration",
			params: appwire.HostRemoveParams{Name: "side", MutationID: newTestMutationID(), ExpectedIncarnationID: incarnation},
			field:  "expectedGeneration",
		},
		{
			name:   "missing expectedIncarnationId",
			params: appwire.HostRemoveParams{Name: "side", MutationID: newTestMutationID(), ExpectedGeneration: generation},
			field:  "expectedIncarnationId",
		},
	}
	for _, tc := range removeCases {
		t.Run("remove/"+tc.name, func(t *testing.T) {
			before := readHubTOMLBytes(t, f.configPath)
			_, err := f.m.Remove(context.Background(), tc.params)
			info, data, code := deployWireInfo(t, err)
			if info != appwire.ErrorInvalidHostField || code != appwire.CodeInvalidParams {
				t.Fatalf("refusal = (%q, %d), want (%q, %d)", info, code, appwire.ErrorInvalidHostField, appwire.CodeInvalidParams)
			}
			if field := receiptField(t, data, "field"); field != tc.field {
				t.Fatalf("refusal field = %q, want %q", field, tc.field)
			}
			if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, before) {
				t.Fatalf("a presence refusal rewrote hub.toml:\n%s", after)
			}
			if _, ok := f.m.cfg.hosts.Get("side"); !ok {
				t.Fatal("a presence refusal removed the host")
			}
		})
	}
}

// TestHostMutationUpdateCommitsTheGuardedPair pins §16's present-and-current
// arm: the pair commits, the edit advances the generation and keeps the
// incarnation id, the response row carries the post-commit pair, and the
// receipt rides the same write pinned to that pair.
func TestHostMutationUpdateCommitsTheGuardedPair(t *testing.T) {
	f := newUpdateFixture(t)
	before, _ := f.m.cfg.hosts.Get("side")
	params := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "edited.example", User: "operator"})

	resp, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, ok := f.m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the update dropped the live entry")
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("generation = %d after editing %d, want exactly one advance", after.Generation, before.Generation)
	}
	if after.IncarnationID != before.IncarnationID {
		t.Fatalf("incarnation id = %q after the edit, want the kept %q", after.IncarnationID, before.IncarnationID)
	}
	if resp.Host.Generation != after.Generation || resp.Host.IncarnationID != after.IncarnationID {
		t.Fatalf("response row pair = (%d, %q), want the post-commit (%d, %q)",
			resp.Host.Generation, resp.Host.IncarnationID, after.Generation, after.IncarnationID)
	}
	if resp.Host.Address != "edited.example" || resp.Host.User != "operator" {
		t.Fatalf("response row = %+v, want the edited entry", resp.Host)
	}

	receipts := receiptsFor(t, f.m, params.MutationID)
	if len(receipts) != 1 {
		t.Fatalf("receipts for the key = %d, want exactly 1", len(receipts))
	}
	for key, receipt := range receipts {
		scope, _ := parseHostReceiptScopedKey(key)
		if scope.Kind != hostMutationUpdate || scope.Name != "side" {
			t.Fatalf("receipt scope = %+v, want update of side", scope)
		}
		if receipt.Outcome != hostReceiptOutcomeCommitted {
			t.Fatalf("receipt outcome = %q, want %q", receipt.Outcome, hostReceiptOutcomeCommitted)
		}
		if receipt.Generation != after.Generation || receipt.IncarnationID != after.IncarnationID {
			t.Fatalf("receipt pair = (%d, %q), want the post-commit (%d, %q)", receipt.Generation, receipt.IncarnationID, after.Generation, after.IncarnationID)
		}
		if receipt.Row.Address != "edited.example" || receipt.Row.User != "operator" || receipt.Row.Name != "side" {
			t.Fatalf("receipt row = %+v, want the edited effective config", receipt.Row)
		}
		if _, err := time.Parse(time.RFC3339, receipt.CommittedAt); err != nil {
			t.Fatalf("receipt committed_at = %q, not RFC3339: %v", receipt.CommittedAt, err)
		}
	}

	// The durable file carries the same receipt — the commit and its receipt
	// are one atomic write.
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	if len(cfg.MutationReceipts) != 1 {
		t.Fatalf("hub.toml receipts = %d, want the one committed", len(cfg.MutationReceipts))
	}
}

// TestHostMutationStalePairRefusals pins §4/§16's present-and-stale arms: a
// mismatch on either half of the pair is the typed stale-entry refusal
// committing nothing — no file write, no receipt, no live change — and a
// delayed retry carrying a pre-bump generation with a fresh key is refused the
// same way.
func TestHostMutationStalePairRefusals(t *testing.T) {
	f := newUpdateFixture(t)
	generation, incarnation := testMutationIdentity(t, f.m, "side")

	cases := []struct {
		name   string
		params appwire.HostUpdateParams
	}{
		{
			name:   "stale generation",
			params: appwire.HostUpdateParams{Name: "side", Entry: appwire.HostEntry{Address: "edited.example"}, MutationID: newTestMutationID(), ExpectedGeneration: generation + 7, ExpectedIncarnationID: incarnation},
		},
		{
			name:   "stale incarnation",
			params: appwire.HostUpdateParams{Name: "side", Entry: appwire.HostEntry{Address: "edited.example"}, MutationID: newTestMutationID(), ExpectedGeneration: generation, ExpectedIncarnationID: "another-incarnation"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := readHubTOMLBytes(t, f.configPath)
			_, err := f.m.Update(context.Background(), tc.params)
			info, data, code := deployWireInfo(t, err)
			if info != appwire.ErrorStaleEntry || code != appwire.CodeConflict {
				t.Fatalf("refusal = (%q, %d), want (%q, %d)", info, code, appwire.ErrorStaleEntry, appwire.CodeConflict)
			}
			if binding := receiptField(t, data, "binding"); binding != string(appwire.StaleEntryBindingGeneration) {
				t.Fatalf("binding = %q, want %q", binding, appwire.StaleEntryBindingGeneration)
			}
			if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, before) {
				t.Fatalf("a stale refusal rewrote hub.toml:\n%s", after)
			}
			if got, _ := testMutationIdentity(t, f.m, "side"); got != generation {
				t.Fatalf("generation moved to %d on a stale refusal, want %d", got, generation)
			}
			if got := len(receiptsFor(t, f.m, tc.params.MutationID)); got != 0 {
				t.Fatalf("a stale refusal recorded %d receipts, want none", got)
			}
		})
	}

	// A delayed retry carrying the pre-bump generation with a fresh key: the
	// first edit bumps the generation, and the second request — which captured
	// the old pair — is refused stale, never applied over the intervening edit.
	if _, err := f.m.Update(context.Background(), updateRequest(t, f.m, "side", appwire.HostEntry{Address: "first.example"})); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	delayed := appwire.HostUpdateParams{
		Name:                  "side",
		Entry:                 appwire.HostEntry{Address: "delayed.example"},
		MutationID:            newTestMutationID(),
		ExpectedGeneration:    generation,
		ExpectedIncarnationID: incarnation,
	}
	_, err := f.m.Update(context.Background(), delayed)
	info, _, _ := deployWireInfo(t, err)
	if info != appwire.ErrorStaleEntry {
		t.Fatalf("delayed retry refusal = %q, want %q", info, appwire.ErrorStaleEntry)
	}
	live, _ := f.m.cfg.hosts.Get("side")
	if live.SSH != "first.example" {
		t.Fatalf("the delayed edit overwrote the intervening change: %+v", live)
	}
}

// TestHostMutationUpdateReplayReturnsTheRecordedReceipt pins §16's
// commit-then-replay arm: the retry carrying the original key (and the
// pre-bump expectations a lost response left behind) returns the recorded
// receipt pinned to the post-bump generation, with no re-bump and no second
// receipt.
func TestHostMutationUpdateReplayReturnsTheRecordedReceipt(t *testing.T) {
	f := newUpdateFixture(t)
	params := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "edited.example"})
	first, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	committed, _ := f.m.cfg.hosts.Get("side")

	replay, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("replayed Update: %v", err)
	}
	if replay.Host.Generation != first.Host.Generation || replay.Host.Address != first.Host.Address {
		t.Fatalf("replay row = %+v, want the recorded %+v", replay.Host, first.Host)
	}
	after, _ := f.m.cfg.hosts.Get("side")
	if after.Generation != committed.Generation {
		t.Fatalf("replay bumped generation to %d, want the committed %d", after.Generation, committed.Generation)
	}
	if got := len(receiptsFor(t, f.m, params.MutationID)); got != 1 {
		t.Fatalf("receipts after the replay = %d, want exactly the recorded 1", got)
	}
}

// TestHostMutationRemoveReplayReturnsTheRecordedRemovedRow pins §16's "a
// retried remove cannot fail not-found": the replay returns the recorded
// removed row — never a second teardown and never not-found.
func TestHostMutationRemoveReplayReturnsTheRecordedRemovedRow(t *testing.T) {
	f := newUpdateFixture(t)
	params := removeRequest(t, f.m, "side")
	first, err := f.m.Remove(context.Background(), params)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !first.Host.Removed || first.Host.Name != "side" {
		t.Fatalf("removal row = %+v, want side marked removed", first.Host)
	}
	if _, ok := f.m.cfg.hosts.Get("side"); ok {
		t.Fatal("the committed removal left the entry live")
	}
	file := readHubTOMLBytes(t, f.configPath)

	replay, err := f.m.Remove(context.Background(), params)
	if err != nil {
		t.Fatalf("replayed Remove: %v (a lost-response retry must not fail not-found)", err)
	}
	if !replay.Host.Removed || replay.Host.Name != "side" || replay.Host.Generation != first.Host.Generation ||
		replay.Host.IncarnationID != first.Host.IncarnationID {
		t.Fatalf("replay row = %+v, want the recorded removal %+v", replay.Host, first.Host)
	}
	if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, file) {
		t.Fatal("the replay rewrote hub.toml")
	}
	if got := len(receiptsFor(t, f.m, params.MutationID)); got != 1 {
		t.Fatalf("receipts after the replay = %d, want exactly the recorded 1", got)
	}
}

// TestHostMutationRemoveReplayAfterReAddLeavesTheNewIncarnation pins §4/§16's
// superseded-receipt arm: a lost-response remove retry landing after a re-add
// (different generation) returns the recorded receipt and never tears down the
// new incarnation.
func TestHostMutationRemoveReplayAfterReAddLeavesTheNewIncarnation(t *testing.T) {
	f := newUpdateFixture(t)
	params := removeRequest(t, f.m, "side")
	if _, err := f.m.Remove(context.Background(), params); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"},
	}); err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	readded, ok := f.m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the re-add left no live entry")
	}
	if readded.Generation == params.ExpectedGeneration {
		t.Fatalf("the re-add reused generation %d", readded.Generation)
	}

	replay, err := f.m.Remove(context.Background(), params)
	if err != nil {
		t.Fatalf("replayed Remove after re-add: %v", err)
	}
	if replay.Host.Generation != params.ExpectedGeneration {
		t.Fatalf("replay row generation = %d, want the recorded %d", replay.Host.Generation, params.ExpectedGeneration)
	}
	live, ok := f.m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the replay tore down the re-added incarnation")
	}
	if live.Generation != readded.Generation || live.IncarnationID != readded.IncarnationID || live.SSH != "fresh.example" {
		t.Fatalf("the replay changed the new incarnation: %+v, want %+v", live, readded)
	}
	if got := len(receiptsFor(t, f.m, params.MutationID)); got != 1 {
		t.Fatalf("receipts after the replay = %d, want exactly the recorded 1", got)
	}
}

// TestHostMutationIDConflictsAcrossNamesAndKinds pins §5/§16's cross-name and
// cross-kind rule: a mutationId already used by a current-generation receipt
// of a different name or kind is the typed conflicting-mutation-id refusal —
// never a hit, never a fresh apply under the colliding key.
func TestHostMutationIDConflictsAcrossNamesAndKinds(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "other", SSH: "other.example"})

	// A committed edit of side under key "collide".
	key := "collide-" + newTestMutationID()
	params := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "side2.example"})
	params.MutationID = key
	if _, err := f.m.Update(context.Background(), params); err != nil {
		t.Fatalf("Update(side): %v", err)
	}

	// The same key on a different name: a conflict, and nothing changes.
	before := readHubTOMLBytes(t, f.configPath)
	foreign := removeRequest(t, f.m, "other")
	foreign.MutationID = key
	_, err := f.m.Remove(context.Background(), foreign)
	info, _, code := deployWireInfo(t, err)
	if info != appwire.ErrorConflictingMutationID || code != appwire.CodeConflict {
		t.Fatalf("cross-name refusal = (%q, %d), want (%q, %d)", info, code, appwire.ErrorConflictingMutationID, appwire.CodeConflict)
	}
	if _, ok := f.m.cfg.hosts.Get("other"); !ok {
		t.Fatal("the conflicting refusal removed the other host")
	}
	if after := readHubTOMLBytes(t, f.configPath); !bytes.Equal(after, before) {
		t.Fatal("the conflicting refusal rewrote hub.toml")
	}

	// The same key with a different kind on its own name: also a conflict.
	sameName := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "side3.example"})
	sameName.MutationID = key
	_, err = f.m.Remove(context.Background(), appwire.HostRemoveParams{
		Name:                  sameName.Name,
		MutationID:            key,
		ExpectedGeneration:    sameName.ExpectedGeneration,
		ExpectedIncarnationID: sameName.ExpectedIncarnationID,
	})
	info, _, _ = deployWireInfo(t, err)
	if info != appwire.ErrorConflictingMutationID {
		t.Fatalf("cross-kind refusal = %q, want %q", info, appwire.ErrorConflictingMutationID)
	}
}

// TestHostMutationBusyRefusalsAndReplayUnderAHeldGate pins §5's busy classes
// through update and remove — an operation holder names the operation record,
// every other class renders the transient form — and the ordering rule that a
// replay returns its receipt without consulting the gate.
func TestHostMutationBusyRefusalsAndReplayUnderAHeldGate(t *testing.T) {
	f := newUpdateFixture(t)
	gate := f.m.cfg.gate.(*hostops.ProcessGate)

	for _, tc := range []struct {
		name        string
		holder      hostops.Holder
		kind        hostops.HolderKind
		operationID string
	}{
		{name: "operation", holder: hostops.Holder{Kind: hostops.HolderOperation, OperationID: "00000000000000000042"}, kind: hostops.HolderOperation, operationID: "00000000000000000042"},
		{name: "plan", holder: hostops.Holder{Kind: hostops.HolderPlan}, kind: hostops.HolderPlan},
		{name: "manager", holder: hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"}, kind: hostops.HolderManager},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release, err := gate.TryAcquire("side", tc.holder)
			if err != nil {
				t.Fatalf("holding the gate: %v", err)
			}
			defer release()
			_, err = f.m.Update(context.Background(), updateRequest(t, f.m, "side", appwire.HostEntry{Address: "blocked.example"}))
			wantBusy(t, err, tc.kind, tc.operationID)
			_, err = f.m.Remove(context.Background(), removeRequest(t, f.m, "side"))
			wantBusy(t, err, tc.kind, tc.operationID)
			live, _ := f.m.cfg.hosts.Get("side")
			if live.SSH != "side.example" {
				t.Fatalf("a busy refusal changed the entry: %+v", live)
			}
		})
	}

	// A replay is dedup-first: it returns the recorded receipt even while the
	// gate is held, because the gate is consulted only past the dedup lookup.
	params := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "committed.example"})
	first, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	release, err := gate.TryAcquire("side", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("holding the gate for the replay: %v", err)
	}
	defer release()
	replay, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("replay under a held gate: %v", err)
	}
	if replay.Host.Generation != first.Host.Generation || replay.Host.Address != "committed.example" {
		t.Fatalf("replay row = %+v, want the recorded %+v", replay.Host, first.Host)
	}
}

// TestHostMutationAddAuditAndReplay pins §5/§11's add arms: a keyless add skips
// dedup and commits a server-keyed audit record, while a keyed add's replay
// returns the recorded receipt instead of hitting the duplicate-live refusal.
func TestHostMutationAddAuditAndReplay(t *testing.T) {
	f := newUpdateFixture(t)

	// Keyless: no client key, but the commit still writes a server-keyed audit
	// record, so the crash/audit trail has no keyless gap.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "keyless", Address: "keyless.example"},
	}); err != nil {
		t.Fatalf("keyless Add: %v", err)
	}
	keylessReceipts := map[string]HostMutationReceipt{}
	for key, receipt := range f.m.cfg.store.receiptsSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			t.Fatalf("stored receipt key %q does not parse", key)
		}
		if scope.Name == "keyless" {
			keylessReceipts[key] = receipt
			if scope.Kind != hostMutationAdd {
				t.Fatalf("keyless audit receipt kind = %q, want add", scope.Kind)
			}
			if scope.MutationID == "" {
				t.Fatalf("keyless audit receipt carries no server-generated key: %q", key)
			}
		}
	}
	if len(keylessReceipts) != 1 {
		t.Fatalf("keyless add receipts = %d, want exactly the one audit record", len(keylessReceipts))
	}

	// Keyed: the first call commits, the replay returns the recorded row — not
	// a duplicate-live-name refusal, and without committing twice.
	keyed := appwire.HostAddParams{Entry: appwire.HostEntry{Name: "keyed", Address: "keyed.example"}, MutationID: newTestMutationID()}
	first, err := f.m.Add(context.Background(), keyed)
	if err != nil {
		t.Fatalf("keyed Add: %v", err)
	}
	replay, err := f.m.Add(context.Background(), keyed)
	if err != nil {
		t.Fatalf("replayed keyed Add: %v (a replay must not hit the duplicate refusal)", err)
	}
	if replay.Name != first.Name || replay.Generation != first.Generation || replay.IncarnationID != first.IncarnationID {
		t.Fatalf("replay row = %+v, want the recorded %+v", replay, first)
	}
	if got := len(receiptsFor(t, f.m, keyed.MutationID)); got != 1 {
		t.Fatalf("keyed add receipts = %d, want exactly the recorded 1", got)
	}

	// Keyless adds carry no client idempotency semantics: a repeat keyless add
	// for a live name is the duplicate refusal, never a replay.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "keyless", Address: "keyless.example"},
	}); err == nil {
		t.Fatal("a duplicate keyless add succeeded, want the duplicate refusal")
	}
}

// TestHostListAndStatusCarryTheCurrentPair pins §11's row half: list and status
// rows carry the live entry's current (generation, incarnation id) pair the UI
// echoes back as its guarded expectations.
func TestHostListAndStatusCarryTheCurrentPair(t *testing.T) {
	f := newUpdateFixture(t)
	live, _ := f.m.cfg.hosts.Get("side")
	list, err := f.m.List(context.Background(), appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Hosts) != 1 || list.Hosts[0].Generation != live.Generation || list.Hosts[0].IncarnationID != live.IncarnationID {
		t.Fatalf("list row = %+v, want the live pair (%d, %q)", list.Hosts, live.Generation, live.IncarnationID)
	}
	status, err := f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Host.Generation != live.Generation || status.Host.IncarnationID != live.IncarnationID {
		t.Fatalf("status row = %+v, want the live pair (%d, %q)", status.Host, live.Generation, live.IncarnationID)
	}

	// An edit advances the pair both rows report.
	if _, err := f.m.Update(context.Background(), updateRequest(t, f.m, "side", appwire.HostEntry{Address: "edited.example"})); err != nil {
		t.Fatalf("Update: %v", err)
	}
	edited, _ := f.m.cfg.hosts.Get("side")
	status, err = f.m.Status(context.Background(), appwire.HostStatusParams{Name: "side"})
	if err != nil {
		t.Fatalf("Status after edit: %v", err)
	}
	if status.Host.Generation != edited.Generation || status.Host.IncarnationID != edited.IncarnationID {
		t.Fatalf("status row after the edit = (%d, %q), want the post-commit (%d, %q)",
			status.Host.Generation, status.Host.IncarnationID, edited.Generation, edited.IncarnationID)
	}
}

// TestHostMutationReceiptSurvivesBootAndReplays pins the receipt's durable
// half: a fresh boot loads the file's [mutation_receipts] tables and a replay
// then returns the recorded receipt, so a lost response is recoverable across a
// restart.
func TestHostMutationReceiptSurvivesBootAndReplays(t *testing.T) {
	f := newUpdateFixture(t)
	params := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "durable.example"})
	first, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	booted := bootHostManager(t, f.configPath)
	replay, err := booted.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("replay after boot: %v", err)
	}
	if replay.Host.Generation != first.Host.Generation || replay.Host.Address != "durable.example" {
		t.Fatalf("replay row = %+v, want the recorded %+v", replay.Host, first.Host)
	}
	live, _ := booted.cfg.hosts.Get("side")
	if live.Generation != first.Host.Generation {
		t.Fatalf("replaying after boot bumped the generation to %d, want %d", live.Generation, first.Host.Generation)
	}
}

// TestHostMutationReceiptDroppedWhenTheCommitUnCommits pins §5's rollback rule
// for receipts: a mutation the API reports as failed leaves no receipt behind —
// neither in memory nor in the file the failed commit wrote.
func TestHostMutationReceiptDroppedWhenTheCommitUnCommits(t *testing.T) {
	f := newUpdateFixture(t)
	params := removeRequest(t, f.m, "side")

	// The removal's own save fails behind its rename; the compensation save
	// that follows runs the real sync, restoring the pre-removal contents.
	flakyHubTOMLDirSync(t, func(call int) bool { return call == 1 })
	_, err := f.m.Remove(context.Background(), params)
	if err == nil {
		t.Fatal("Remove over a failing directory sync succeeded, want refusal")
	}
	if _, ok := f.m.cfg.hosts.Get("side"); !ok {
		t.Fatal("the compensated refusal left the host gone")
	}
	if got := len(receiptsFor(t, f.m, params.MutationID)); got != 0 {
		t.Fatalf("the un-committed removal left %d receipts in memory, want none", got)
	}
	after := readHubTOMLBytes(t, f.configPath)
	if strings.Contains(string(after), "mutation_receipts") {
		t.Fatalf("the un-committed removal left a receipt in hub.toml:\n%s", after)
	}
	if names := hubTOMLHostNames(t, f.configPath); len(names) != 1 || names[0] != "side" {
		t.Fatalf("hub.toml names = %v, want side restored", names)
	}
}

// TestHostMutationReceiptKeptWhenTheLivePhaseFails pins the commit-point rule
// for an edit whose rebind refuses: the commit landed, so its receipt stays —
// carrying the `committed-with-teardown-failure` outcome and the pre-minted
// remnant id of the repair handle (spec 08 §6) — and the original mutationId
// replays it rather than re-applying.
func TestHostMutationReceiptKeptWhenTheLivePhaseFails(t *testing.T) {
	f := newUpdateFixture(t)
	otherReg, err := hostreg.New(nil)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	manager := sshconn.New(otherReg, sshconn.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	f.m.cfg.manager = manager
	// The manager is the per-host gate wherever it owns the channels (the same
	// wiring newHubHostManager performs with hostGateFor): the failing live
	// seam must be reached under the hub's own reservation, not refused at the
	// gate precondition.
	f.m.cfg.gate = manager

	params := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "edited.example"})
	if _, err := f.m.Update(context.Background(), params); err == nil {
		t.Fatal("Update over a live seam that refuses succeeded, want the failure")
	}
	receipts := receiptsFor(t, f.m, params.MutationID)
	if len(receipts) != 1 {
		t.Fatalf("the committed edit left %d receipts in memory, want exactly one", len(receipts))
	}
	var receipt HostMutationReceipt
	for _, stored := range receipts {
		receipt = stored
	}
	if receipt.Outcome != hostReceiptOutcomeTeardownFailure || receipt.RemnantID == "" {
		t.Fatalf("receipt = %+v, want the teardown-failure outcome with a remnant", receipt)
	}
	if _, open := f.m.cfg.store.markedRemnantFor("side"); !open {
		t.Fatal("the receipt names a remnant the store does not carry")
	}
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	if len(cfg.MutationReceipts) != 1 {
		t.Fatalf("hub.toml carries %d receipts, want the committed edit's one", len(cfg.MutationReceipts))
	}
	for _, stored := range cfg.MutationReceipts {
		if stored.Outcome != hostReceiptOutcomeTeardownFailure || stored.RemnantID != receipt.RemnantID {
			t.Fatalf("stored receipt = %+v, want the recorded failure arm with remnant %q", stored, receipt.RemnantID)
		}
	}
	if _, durable := cfg.TeardownRemnants[receipt.RemnantID]; !durable {
		t.Fatalf("hub.toml does not carry remnant %q beside the receipt", receipt.RemnantID)
	}
}

// TestHostMutationReceiptRecordsRefuseCorruptContent pins §6's reserved-record
// posture: a receipt record the build cannot decode — a non-canonical key, a
// disagreement between key and fields, an unknown outcome, an unknown field —
// is refused loudly at load, never read as a half-understood record and never
// dropped by a rewrite.
func TestHostMutationReceiptRecordsRefuseCorruptContent(t *testing.T) {
	validKey := "mut-1/side/update/2/inc-1"
	cases := []struct {
		name    string
		section string
		want    string
	}{
		{
			name: "non-canonical key",
			section: `[mutation_receipts."mut-1/side/update/2"]
outcome = "committed"
generation = 2
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
[mutation_receipts."mut-1/side/update/2".row]
name = "side"
`,
			want: "canonical five-part scoped key",
		},
		{
			name: "key and fields disagree",
			section: fmt.Sprintf(`[mutation_receipts.%q]
outcome = "committed"
generation = 3
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
[mutation_receipts.%q.row]
name = "side"
`, validKey, validKey),
			want: "pins generation 2 while the record carries 3",
		},
		{
			name: "unknown outcome",
			section: fmt.Sprintf(`[mutation_receipts.%q]
outcome = "torn-write"
generation = 2
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
[mutation_receipts.%q.row]
name = "side"
`, validKey, validKey),
			want: `outcome "torn-write"`,
		},
		{
			name: "collision-dropped without its fields",
			section: fmt.Sprintf(`[mutation_receipts.%q]
outcome = "collision-dropped"
generation = 2
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
[mutation_receipts.%q.row]
name = "side"
`, validKey, validKey),
			want: "with no dropped_entry",
		},
		{
			name: "unknown field",
			section: fmt.Sprintf(`[mutation_receipts.%q]
outcome = "committed"
generation = 2
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
superseded_token = "deadbeef"
[mutation_receipts.%q.row]
name = "side"
`, validKey, validKey),
			want: "does not decode",
		},
		{
			name: "row name disagrees with the key",
			section: fmt.Sprintf(`[mutation_receipts.%q]
outcome = "committed"
generation = 2
incarnation_id = "inc-1"
committed_at = "2026-09-27T12:00:00Z"
[mutation_receipts.%q.row]
name = "other"
`, validKey, validKey),
			want: `names host "side" in its key and "other" in its row`,
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
				t.Fatal("LoadConfig accepted a receipt record the build cannot decode")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfig error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestHostMutationReceiptsPreservedAcrossUnrelatedMutations pins that a
// rewrite preserves receipts for names it does not own: an unrelated mutation
// must not drop another host's receipt.
func TestHostMutationReceiptsPreservedAcrossUnrelatedMutations(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "other", SSH: "other.example"})
	kept := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "kept.example"})
	if _, err := f.m.Update(context.Background(), kept); err != nil {
		t.Fatalf("Update(side): %v", err)
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{
		Entry: appwire.HostEntry{Name: "third", Address: "third.example"},
	}); err != nil {
		t.Fatalf("Add(third): %v", err)
	}
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	found := false
	for key := range cfg.MutationReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.MutationID == kept.MutationID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unrelated add dropped side's receipt: %v", cfg.MutationReceipts)
	}
}

// TestHostMutationUnrelatedReceiptDoesNotBlockAnotherHost pins §5's per-host
// scope: a receipt for host A's key never blocks a mutation of host B.
func TestHostMutationUnrelatedReceiptDoesNotBlockAnotherHost(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "other", SSH: "other.example"})
	key := "shared-key-" + newTestMutationID()
	first := updateRequest(t, f.m, "side", appwire.HostEntry{Address: "side2.example"})
	first.MutationID = key
	if _, err := f.m.Update(context.Background(), first); err != nil {
		t.Fatalf("Update(side): %v", err)
	}
	second := updateRequest(t, f.m, "other", appwire.HostEntry{Address: "other2.example"})
	second.MutationID = newTestMutationID()
	if _, err := f.m.Update(context.Background(), second); err != nil {
		t.Fatalf("Update(other): %v", err)
	}
	live, _ := f.m.cfg.hosts.Get("other")
	if live.SSH != "other2.example" {
		t.Fatalf("the other host's edit did not land: %+v", live)
	}
}

// TestHostMutationReceiptRidesTheSameAtomicWriteAsTheRemove pins §5's
// single-write receipt for a removal: the write that drops the entry also
// writes the receipt, and the replay after a boot finds it.
func TestHostMutationRemoveReceiptRidesTheCommitWrite(t *testing.T) {
	f := newUpdateFixture(t)
	removed, _ := f.m.cfg.hosts.Get("side")
	params := removeRequest(t, f.m, "side")
	if _, err := f.m.Remove(context.Background(), params); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("load hub.toml: %v", err)
	}
	if len(cfg.Hosts) != 0 {
		t.Fatalf("hub.toml still carries hosts after the removal: %+v", cfg.Hosts)
	}
	var found bool
	for key, receipt := range cfg.MutationReceipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok || scope.MutationID != params.MutationID {
			continue
		}
		found = true
		if scope.Kind != hostMutationRemove {
			t.Fatalf("receipt kind = %q, want remove", scope.Kind)
		}
		if receipt.Generation != removed.Generation || receipt.IncarnationID != removed.IncarnationID {
			t.Fatalf("receipt pair = (%d, %q), want the removed (%d, %q)",
				receipt.Generation, receipt.IncarnationID, removed.Generation, removed.IncarnationID)
		}
	}
	if !found {
		t.Fatalf("the removal's atomic write carried no receipt: %v", cfg.MutationReceipts)
	}
	// The high-water record the removal persisted is what makes the direct hit
	// work across a restart.
	mark, ok := cfg.Generations["side"]
	if !ok || mark.Generation != removed.Generation || mark.IncarnationID != removed.IncarnationID {
		t.Fatalf("high-water mark = %+v (present %v), want the removed pair (%d, %q)", mark, ok, removed.Generation, removed.IncarnationID)
	}
}

// TestHostReceiptScopedKeyCodec covers the codec directly: the scoped key
// round-trips a canonical key and refuses non-canonical spellings (a missing or
// extra part, a zero or non-canonical generation, an unknown kind, a
// lowercase-hex escape).
func TestHostReceiptScopedKeyCodec(t *testing.T) {
	scope := hostReceiptScope{
		MutationID:    "mut/with%slash and space",
		Name:          "side",
		Kind:          hostMutationUpdate,
		Generation:    7,
		IncarnationID: "inc-1",
	}
	key := hostReceiptScopedKey(scope)
	if strings.ContainsAny(key, " \t\n") {
		t.Fatalf("scoped key %q carries an unescaped character", key)
	}
	got, ok := parseHostReceiptScopedKey(key)
	if !ok {
		t.Fatalf("canonical key %q did not parse", key)
	}
	if got != scope {
		t.Fatalf("round trip = %+v, want %+v", got, scope)
	}
	for _, bad := range []string{
		"",
		"mut/side/update/7",
		"mut/side/update/7/inc-1/extra",
		"mut/side/update/0/inc-1",
		"mut/side/update/07/inc-1",
		"mut/side/bogus/7/inc-1",
		"mut/side/update/7/inc%2d1",
	} {
		if _, ok := parseHostReceiptScopedKey(bad); ok {
			t.Fatalf("non-canonical key %q parsed", bad)
		}
	}
}

// TestHostMutationReceiptRowRendersTheRecordedOutcome pins the row a replay
// renders: the recorded effective config, the pinned pair, one origin marker,
// and `removed` set exactly on a removal's row.
func TestHostMutationReceiptRowRendersTheRecordedOutcome(t *testing.T) {
	entry := hostreg.Host{Name: "side", SSH: "side.example", User: "u", Roots: []string{"/srv"}}
	receipt := newHostMutationReceipt("key", hostMutationUpdate, entry, time.Now())
	row := hostReceiptRow(receipt, hostMutationUpdate)
	if row.Name != "side" || row.Address != "side.example" || row.User != "u" || row.Origin != hostOriginHubTOML {
		t.Fatalf("row = %+v, want the recorded entry", row)
	}
	if row.Removed {
		t.Fatal("an update's row carries removed")
	}
	removed := hostReceiptRow(receipt, hostMutationRemove)
	if !removed.Removed || removed.Origin != hostOriginHubTOML {
		t.Fatalf("removed row = %+v, want removed with the origin marker", removed)
	}
}
