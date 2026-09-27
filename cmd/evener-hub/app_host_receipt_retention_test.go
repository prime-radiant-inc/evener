package hub

// Registry tests for slice S11's receipt retention half (registry spec 08 §6
// 1068-1115, §11 1541, §16 2246-2276): superseded-receipt compaction under the
// count/TTL bounds, the bounded pruned markers a compaction persists, the
// tombstone-purge clean slate (replay refuses `stale-entry` on the surviving
// marker; a cross-name replay commits fresh), the tombstoned name's backstop
// marker exemption, and keyless-audit compaction.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// receiptScopesFor returns the stored receipts and pruned markers for one name.
func receiptScopesFor(t *testing.T, m *hubHostManager, name string) (map[string]HostMutationReceipt, map[string]PrunedReceiptMarker) {
	t.Helper()
	receipts := map[string]HostMutationReceipt{}
	for key, receipt := range m.cfg.store.receiptsSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			t.Fatalf("stored receipt key %q does not parse", key)
		}
		if scope.Name == name {
			receipts[key] = receipt
		}
	}
	markers := map[string]PrunedReceiptMarker{}
	for key, marker := range m.cfg.store.prunedReceiptSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			t.Fatalf("stored marker key %q does not parse", key)
		}
		if scope.Name == name {
			markers[key] = marker
		}
	}
	return receipts, markers
}

// churnUpdates advances name's generation n times with distinct keys and
// returns each mutation's key.
func churnUpdates(t *testing.T, m *hubHostManager, name string, n int) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := range n {
		key := newTestMutationID()
		params := updateRequest(t, m, name, appwire.HostEntry{Address: fmt.Sprintf("churn-%d.example", i)})
		params.MutationID = key
		if _, err := m.Update(context.Background(), params); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
		keys = append(keys, key)
	}
	return keys
}

// scopeForKey returns the scoped key of the single stored receipt or marker
// whose mutation id matches, preferring the receipt.
func scopeForKey(t *testing.T, m *hubHostManager, name, mutationID string) (string, hostReceiptScope, bool) {
	t.Helper()
	for key := range m.cfg.store.receiptsSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if ok && scope.Name == name && scope.MutationID == mutationID {
			return key, scope, true
		}
	}
	for key := range m.cfg.store.prunedReceiptSnapshot() {
		scope, ok := parseHostReceiptScopedKey(key)
		if ok && scope.Name == name && scope.MutationID == mutationID {
			return key, scope, false
		}
	}
	t.Fatalf("no receipt or marker for mutation %q of host %q", mutationID, name)
	return "", hostReceiptScope{}, false
}

// TestHostReceiptSupersededCountBound pins spec §6's live-name compaction: the
// write that finalizes a receipt drops that name's receipts pinned to earlier
// generations past the count bound, persisting a bounded pruned marker for
// each drop; a replay of a pruned key is the typed `stale-entry`
// (pruned-generation), while a retained superseded key still returns its
// recorded receipt.
func TestHostReceiptSupersededCountBound(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.policy.supersededMaxCount = 2
	// add (gen1) + three updates: four receipts, one current, three superseded.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "other", Address: "other.example"}}); err != nil {
		t.Fatalf("Add(other): %v", err)
	}
	keys := churnUpdates(t, f.m, "side", 5)
	receipts, markers := receiptScopesFor(t, f.m, "side")
	// The current-generation receipt survives, and at most the two newest
	// superseded receipts do.
	current := 0
	superseded := 0
	for _, receipt := range receipts {
		if receipt.Generation == receiptsGeneration(t, f, "side") {
			current++
		} else {
			superseded++
		}
	}
	if current != 1 {
		t.Fatalf("stored current-generation receipts = %d, want 1", current)
	}
	if superseded > 2 {
		t.Fatalf("stored superseded receipts = %d, want at most the count bound 2", superseded)
	}
	if len(markers) == 0 {
		t.Fatal("no pruned marker was persisted for a dropped superseded receipt")
	}
	for key, marker := range markers {
		if _, err := time.Parse(time.RFC3339, marker.PrunedAt); err != nil {
			t.Fatalf("marker %q pruned_at %q: %v", key, marker.PrunedAt, err)
		}
	}
	// Replaying a pruned key refuses stale-entry (pruned-generation): the
	// client's expected pair cannot override the marker.
	prunedKey := ""
	for key := range markers {
		scope, _ := parseHostReceiptScopedKey(key)
		prunedKey = scope.MutationID
	}
	generation, incarnation := testMutationIdentity(t, f.m, "side")
	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name: "side", Entry: appwire.HostEntry{Address: "replay.example"},
		MutationID: prunedKey, ExpectedGeneration: generation, ExpectedIncarnationID: incarnation,
	}); err == nil {
		t.Fatal("a replay naming a pruned key committed fresh")
	} else {
		info, data, code := deployWireInfo(t, err)
		if info != appwire.ErrorStaleEntry || code != appwire.CodeConflict {
			t.Fatalf("refusal = (%q, %d), want (%q, %d)", info, code, appwire.ErrorStaleEntry, appwire.CodeConflict)
		}
		if binding := receiptField(t, data, "binding"); binding != string(appwire.StaleEntryBindingPrunedGeneration) {
			t.Fatalf("refusal binding = %q, want %q", binding, appwire.StaleEntryBindingPrunedGeneration)
		}
	}
	// A retained superseded key returns its recorded receipt, never a fresh
	// apply: the newest churned key is superseded after the final update.
	generation, incarnation = testMutationIdentity(t, f.m, "side")
	params := appwire.HostUpdateParams{
		Name: "side", Entry: appwire.HostEntry{Address: "retained.example"},
		MutationID: keys[len(keys)-1], ExpectedGeneration: generation, ExpectedIncarnationID: incarnation,
	}
	resp, err := f.m.Update(context.Background(), params)
	if err != nil {
		t.Fatalf("replay of a retained superseded key: %v", err)
	}
	if resp.Host.Address == "retained.example" {
		t.Fatal("a superseded replay re-applied its mutation")
	}
}

// receiptsGeneration reads name's current generation.
func receiptsGeneration(t *testing.T, f *updateFixture, name string) uint64 {
	t.Helper()
	host, ok := f.m.cfg.hosts.Get(name)
	if !ok {
		t.Fatalf("host %q is not live", name)
	}
	return host.Generation
}

// TestHostReceiptSupersededTTL pins the TTL half of the superseded bound: a
// same-key superseded receipt older than the owner-set TTL compacts the same
// way and leaves a marker.
func TestHostReceiptSupersededTTL(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.policy.supersededTTL = time.Hour
	keys := churnUpdates(t, f.m, "side", 2)
	// The clock moves past the TTL; the next mutation-path write compacts.
	base := time.Now().UTC()
	f.m.cfg.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "other", Address: "other.example"}}); err != nil {
		t.Fatalf("Add(other): %v", err)
	}
	prunedKey := keys[0]
	_, scope, isReceipt := scopeForKey(t, f.m, "side", prunedKey)
	if isReceipt {
		t.Fatalf("the TTL did not compact the stale superseded receipt %+v", scope)
	}
	generation, incarnation := testMutationIdentity(t, f.m, "side")
	if _, err := f.m.Update(context.Background(), appwire.HostUpdateParams{
		Name: "side", Entry: appwire.HostEntry{Address: "replay.example"},
		MutationID: prunedKey, ExpectedGeneration: generation, ExpectedIncarnationID: incarnation,
	}); err == nil {
		t.Fatal("a TTL-pruned replay committed fresh")
	} else {
		info, _, _ := deployWireInfo(t, err)
		if info != appwire.ErrorStaleEntry {
			t.Fatalf("refusal = %q, want %q", info, appwire.ErrorStaleEntry)
		}
	}
}

// TestHostReceiptTombstonePurgeCleanSlate pins spec §6's purge rule: the
// retention purge drops the tombstone and its name's receipts, persists
// markers for the dropped same-key receipts, so a same-key replay after the
// purge refuses `stale-entry` (pruned-generation) on the surviving marker,
// while a cross-name replay commits fresh.
func TestHostReceiptTombstonePurgeCleanSlate(t *testing.T) {
	f := newUpdateFixture(t, hostreg.Host{Name: "other", SSH: "other.example"})
	removeKey := newTestMutationID()
	params := removeRequest(t, f.m, "side")
	params.MutationID = removeKey
	if _, err := f.m.Remove(context.Background(), params); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Past retention: the next mutation purges the tombstone and its receipts.
	removedAt := time.Now().UTC()
	f.m.cfg.now = func() time.Time { return removedAt.Add(DefaultHostTombstoneRetention + time.Hour) }
	if _, err := f.m.Update(context.Background(), updateRequest(t, f.m, "other", appwire.HostEntry{Address: "other2.example"})); err != nil {
		t.Fatalf("Update(other): %v", err)
	}
	if _, still := f.m.cfg.store.tombstoneSnapshot()["side"]; still {
		t.Fatal("the purge did not drop the expired tombstone")
	}
	_, markers := receiptScopesFor(t, f.m, "side")
	found := false
	for key := range markers {
		scope, _ := parseHostReceiptScopedKey(key)
		if scope.MutationID == removeKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("the purge persisted no marker for the dropped remove receipt: %v", markers)
	}
	// A same-key replay after the purge refuses stale-entry on the marker.
	generation, incarnation := testMutationIdentity(t, f.m, "other")
	replay := appwire.HostRemoveParams{
		Name: "side", MutationID: removeKey, ExpectedGeneration: generation, ExpectedIncarnationID: incarnation,
	}
	if _, err := f.m.Remove(context.Background(), replay); err == nil {
		t.Fatal("a same-key replay after the purge committed")
	} else {
		info, _, _ := deployWireInfo(t, err)
		if info != appwire.ErrorStaleEntry {
			t.Fatalf("same-key replay refusal = %q, want %q", info, appwire.ErrorStaleEntry)
		}
	}
	// A cross-name replay of the same mutation id commits fresh.
	cross := updateRequest(t, f.m, "other", appwire.HostEntry{Address: "cross.example"})
	cross.MutationID = removeKey
	if _, err := f.m.Update(context.Background(), cross); err != nil {
		t.Fatalf("cross-name replay after the purge refused: %v", err)
	}
	row, _ := f.m.cfg.hosts.Get("other")
	if row.SSH != "cross.example" {
		t.Fatalf("cross-name replay row = %+v, want the fresh apply", row)
	}
}

// TestHostReceiptReAddRetainsNewestRemove pins spec §6/§4: the re-add purge
// retains that name's newest same-key superseded `remove` receipt, so a
// lost-response remove retry after the re-add still returns its recorded
// receipt instead of tearing down the new incarnation.
func TestHostReceiptReAddRetainsNewestRemove(t *testing.T) {
	f := newUpdateFixture(t)
	removeKey := newTestMutationID()
	params := removeRequest(t, f.m, "side")
	params.MutationID = removeKey
	if _, err := f.m.Remove(context.Background(), params); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	_, scope, isReceipt := scopeForKey(t, f.m, "side", removeKey)
	if !isReceipt {
		t.Fatalf("the re-add purge dropped the newest same-key remove receipt (%+v)", scope)
	}
	// The replay returns the recorded committed receipt: the old removed row,
	// and the re-added incarnation is untouched.
	generation, incarnation := testMutationIdentity(t, f.m, "side")
	replay := appwire.HostRemoveParams{
		Name: "side", MutationID: removeKey, ExpectedGeneration: generation, ExpectedIncarnationID: incarnation,
	}
	resp, err := f.m.Remove(context.Background(), replay)
	if err != nil {
		t.Fatalf("remove replay after re-add: %v", err)
	}
	if !resp.Host.Removed || resp.Host.Address != "side.example" {
		t.Fatalf("remove replay row = %+v, want the recorded removed row", resp.Host)
	}
	live, ok := f.m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("the remove replay tore down the re-added incarnation")
	}
	if live.SSH != "fresh.example" {
		t.Fatalf("the remove replay acted against the new incarnation: %+v", live)
	}
}

// TestHostReceiptPrunedMarkerBound pins spec §6's marker bound: at most the
// owner-set count of newest markers per name survive, under the marker TTL.
func TestHostReceiptPrunedMarkerBound(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.policy.supersededMaxCount = 1
	f.m.cfg.policy.prunedMaxCount = 2
	churnUpdates(t, f.m, "side", 5)
	_, markers := receiptScopesFor(t, f.m, "side")
	if len(markers) > 2 {
		t.Fatalf("markers for side = %d, want at most the count bound 2", len(markers))
	}
	if len(markers) == 0 {
		t.Fatal("no markers survived a churn that dropped superseded receipts")
	}
}

// TestHostReceiptTombstonedNameBackstopExemption pins spec §6's exemption: a
// tombstoned name's remove-retry backstop marker is never dropped by the
// count/TTL bound while the tombstone lives; once the tombstone is purged the
// bound applies again.
func TestHostReceiptTombstonedNameBackstopExemption(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.policy.supersededMaxCount = 1
	f.m.cfg.policy.prunedMaxCount = 1
	// Churn one remove key into a marker, then a newer update key into a
	// newer marker, so the count bound would drop the older remove marker.
	removeKey := newTestMutationID()
	params := removeRequest(t, f.m, "side")
	params.MutationID = removeKey
	if _, err := f.m.Remove(context.Background(), params); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "fresh.example"}}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	keys := churnUpdates(t, f.m, "side", 5)
	_ = keys
	_, markers := receiptScopesFor(t, f.m, "side")
	hasRemoveMarker := false
	for key := range markers {
		scope, _ := parseHostReceiptScopedKey(key)
		if scope.MutationID == removeKey {
			hasRemoveMarker = true
		}
	}
	if !hasRemoveMarker {
		t.Fatalf("the remove-key marker was never created: %v", markers)
	}
	// Now tombstone the name again: the remove marker is the backstop and must
	// survive compaction past the count bound.
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "later", Address: "later.example"}}); err != nil {
		t.Fatalf("Add(later): %v", err)
	}
	_, markers = receiptScopesFor(t, f.m, "side")
	hasRemoveMarker = false
	for key := range markers {
		scope, _ := parseHostReceiptScopedKey(key)
		if scope.MutationID == removeKey {
			hasRemoveMarker = true
		}
	}
	if !hasRemoveMarker {
		t.Fatalf("the tombstoned name's backstop marker was compacted away: %v", markers)
	}
	// A re-add purges the tombstone and ends the exemption: the next write's
	// compaction may drop it under the count bound.
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "final.example"}}); err != nil {
		t.Fatalf("final re-add: %v", err)
	}
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "later2", Address: "later2.example"}}); err != nil {
		t.Fatalf("Add(later2): %v", err)
	}
	_, markers = receiptScopesFor(t, f.m, "side")
	if len(markers) > 1 {
		t.Fatalf("markers after the purge = %d, want the post-purge bound 1", len(markers))
	}
}

// TestHostKeylessAuditCompaction pins spec §11's audit bound: repeated keyless
// adds converge to the newest N audit records per name under the audit TTL.
func TestHostKeylessAuditCompaction(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.policy.auditMaxCount = 2
	// Repeated keyless adds of one name require a remove between them (a live
	// duplicate is refused): the audit record survives each re-add's purge and
	// compacts under its own bound.
	for i := range 4 {
		if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "audit", Address: fmt.Sprintf("audit-%d.example", i)}}); err != nil {
			t.Fatalf("keyless add %d: %v", i, err)
		}
		if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "audit")); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
	}
	audits := 0
	for key, receipt := range f.m.cfg.store.receiptsSnapshot() {
		scope, _ := parseHostReceiptScopedKey(key)
		if scope.Name == "audit" && receipt.Audit {
			audits++
		}
	}
	if audits != 2 {
		t.Fatalf("stored keyless audit records = %d, want the bound 2", audits)
	}
}

// TestHostReceiptPurgeNotResurrectedByPreservation pins the review finding: a
// receipt the tombstone purge dropped must leave hub.toml with the tombstone —
// the reserved-key preservation rule must not re-emit the file's older copy,
// or a restart would serve the purged key as the recorded outcome instead of
// the `stale-entry` (pruned-generation) refusal on the surviving marker.
func TestHostReceiptPurgeNotResurrectedByPreservation(t *testing.T) {
	f := newUpdateFixture(t)
	removeKey := newTestMutationID()
	params := removeRequest(t, f.m, "side")
	params.MutationID = removeKey
	if _, err := f.m.Remove(context.Background(), params); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	f.m.cfg.now = func() time.Time { return time.Now().UTC().Add(DefaultHostTombstoneRetention + time.Hour) }
	if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "later", Address: "later.example"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	for key := range cfg.MutationReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.Name == "side" {
			t.Fatalf("hub.toml still carries the purged receipt %q", key)
		}
	}
	markerSeen := false
	for key := range cfg.PrunedReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.MutationID == removeKey {
			markerSeen = true
		}
	}
	if !markerSeen {
		t.Fatal("the purge's marker is not in hub.toml")
	}
	// A restart keeps the refusal: the purged key reads stale-entry, never the
	// resurrected recorded outcome.
	boot := bootHostManager(t, f.configPath)
	generation, incarnation := testMutationIdentity(t, boot, "later")
	if _, err := boot.Remove(context.Background(), appwire.HostRemoveParams{
		Name: "side", MutationID: removeKey, ExpectedGeneration: generation, ExpectedIncarnationID: incarnation,
	}); err == nil {
		t.Fatal("a purged-key replay committed after restart")
	} else {
		info, _, _ := deployWireInfo(t, err)
		if info != appwire.ErrorStaleEntry {
			t.Fatalf("after restart the purged key refused %q, want %q", info, appwire.ErrorStaleEntry)
		}
	}
}

// TestHostReceiptEvictionMarkersBounded pins the review finding's fix: a
// capacity eviction's purge persists markers for the evicted name, and marker
// compaction must run in the SAME atomic write, so one persist can never write
// more markers than the bound (§6: "every `hub.toml` mutation and every boot
// compacts markers past either bound in the same atomic write").
func TestHostReceiptEvictionMarkersBounded(t *testing.T) {
	f := newUpdateFixture(t)
	f.m.cfg.policy.tombstoneMaxCount = 1
	f.m.cfg.policy.prunedMaxCount = 2
	base := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	f.m.cfg.now = func() time.Time { return base }
	// Churn one name through several keyless add/remove cycles so it carries
	// audit receipts the eviction purge will drop all at once.
	for i := range 5 {
		if _, err := f.m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "churn", Address: fmt.Sprintf("churn-%d.example", i)}}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
		if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "churn")); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
	}
	if _, ok := f.m.cfg.store.tombstoneSnapshot()["churn"]; !ok {
		t.Fatal("the churn tombstone is missing before the eviction")
	}
	// The next removal exceeds the count cap and evicts churn — its purge
	// drops the name's receipts and persists their markers.
	f.m.cfg.now = func() time.Time { return base.Add(time.Hour) }
	if _, err := f.m.Remove(context.Background(), removeRequest(t, f.m, "side")); err != nil {
		t.Fatalf("Remove(side): %v", err)
	}
	if _, still := f.m.cfg.store.tombstoneSnapshot()["churn"]; still {
		t.Fatal("the eviction did not drop the oldest tombstone")
	}
	cfg, err := LoadConfig(f.configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	total := 0
	for key := range cfg.PrunedReceipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.Name == "churn" {
			total++
		}
	}
	if total > 2 {
		t.Fatalf("hub.toml carries %d markers for the evicted name, want the bound 2 in the same write", total)
	}
	if total == 0 {
		t.Fatal("the eviction persisted no markers at all; the test no longer exercises the purge")
	}
}
