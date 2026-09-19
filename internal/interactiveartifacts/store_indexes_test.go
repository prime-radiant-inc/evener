package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func assertStoreIndexes(t *testing.T, s *Store) {
	t.Helper()
	for name, want := range map[string][]string{
		"diagnostics_by_artifact_time": {"artifact_id", "reported_at"},
		"mutations_by_namespace":       {"namespace_id"},
		"diagnostics_by_artifact":      {"artifact_id", "revision", "id"},
	} {
		rows, err := s.db.Query("SELECT name FROM pragma_index_info(?) ORDER BY seqno", name)
		requireNoError(t, err)
		var got []string
		for rows.Next() {
			var column string
			requireNoError(t, rows.Scan(&column))
			got = append(got, column)
		}
		requireNoError(t, rows.Err())
		requireNoError(t, rows.Close())
		if !slices.Equal(got, want) {
			t.Errorf("index %s columns %v, want %v", name, got, want)
		}
	}
}

func assertSelectivePlan(t *testing.T, s *Store, query, index string, args ...any) {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	requireNoError(t, err)
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		requireNoError(t, rows.Scan(&id, &parent, &unused, &detail))
		t.Logf("plan node %d parent %d: %s", id, parent, detail)
		// Check the selective access operation and chosen index, not a full plan
		// snapshot, estimated cost, node ordering or timing threshold.
		if strings.HasPrefix(detail, "SEARCH ") && strings.Contains(detail, index) {
			found = true
		}
	}
	requireNoError(t, rows.Err())
	if !found {
		t.Fatalf("query did not search with %s", index)
	}
}

func TestStoreSelectiveIndexesPreserveDiagnosticsAndPurge(t *testing.T) {
	now := fixedClock()
	s, hash, _ := setupStore(t, StoreOptions{Clock: func() time.Time { return now }})
	assertStoreIndexes(t, s)
	if t.Failed() {
		return
	}
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("first"), PublicationOrigin{})
	requireNoError(t, err)
	diagnostic := func(revision int, message string) []byte {
		return fmt.Appendf(nil, `{"artifactId":%q,"sourceRevision":%d,"message":%q}`, created.ArtifactID, revision, message)
	}
	// Twelve retained source revisions, each with twenty old diagnostics. Recent
	// admission must not scan those historical rows merely because artifact_id matches.
	for revision := 1; revision <= 12; revision++ {
		if revision > 1 {
			_, err = s.Publish(ctx, hash, publishJSON(created.ArtifactID, fmt.Sprint("revision", revision), revision-1, 1), PublicationOrigin{})
			requireNoError(t, err)
		}
		for entry := range 20 {
			now = now.Add(time.Minute)
			_, err = s.ReportDiagnostic(ctx, hash, diagnostic(revision, fmt.Sprint("old", entry)))
			requireNoError(t, err)
		}
	}
	now = now.Add(2 * time.Minute)
	for entry := range 20 {
		_, err = s.ReportDiagnostic(ctx, hash, diagnostic(12, fmt.Sprint("recent", entry)))
		requireNoError(t, err)
	}
	_, err = s.ReportDiagnostic(ctx, hash, diagnostic(12, "limited"))
	requireCode(t, err, Busy)
	var currentCount, totalCount int
	requireNoError(t, s.db.QueryRow("SELECT count(*) FROM artifact_diagnostics WHERE artifact_id=? AND revision=12", created.ArtifactID).Scan(&currentCount))
	requireNoError(t, s.db.QueryRow("SELECT count(*) FROM artifact_diagnostics WHERE artifact_id=?", created.ArtifactID).Scan(&totalCount))
	if currentCount != 20 || totalCount != 240 {
		t.Fatalf("per-revision retention: current %d total %d", currentCount, totalCount)
	}
	assertSelectivePlan(t, s, "SELECT count(*) FROM artifact_diagnostics WHERE artifact_id=? AND reported_at>?", "diagnostics_by_artifact_time", created.ArtifactID, now.Add(-time.Minute).UnixMicro())
	otherHash := sha256.Sum256([]byte("other namespace"))
	scope := testScope()
	scope.NamespaceID = "other"
	requireNoError(t, s.EnsureNamespace(ctx, "other", "realm", "other-owner"))
	requireNoError(t, s.InstallGrant(ctx, otherHash, scope))
	otherRaw := createJSON("other creation")
	other, err := s.Publish(ctx, otherHash, otherRaw, PublicationOrigin{})
	requireNoError(t, err)
	for version := 1; version <= 12; version++ {
		_, err = s.SaveState(ctx, otherHash, saveJSON(other.ArtifactID, fmt.Sprint("other state", version), 1, version, `{"kept":true}`))
		requireNoError(t, err)
	}
	assertSelectivePlan(t, s, "UPDATE artifact_mutations SET outcome_code='DELETED',result_json=NULL WHERE namespace_id=?", "mutations_by_namespace", "namespace")
	assertUsage(t, s)
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	assertUsage(t, s)
	retry, err := s.Publish(ctx, otherHash, otherRaw, PublicationOrigin{})
	requireNoError(t, err)
	if retry != other {
		t.Fatal("namespace purge changed unrelated receipt")
	}
	state := readState(t, s, otherHash, other.ArtifactID)
	if state.StateVersion != 13 || string(state.State) != `{"kept":true}` {
		t.Fatalf("namespace purge changed unrelated content: %+v", state)
	}
	var purged, retained int
	requireNoError(t, s.db.QueryRow("SELECT count(*) FROM artifact_diagnostics WHERE artifact_id=?", created.ArtifactID).Scan(&purged))
	requireNoError(t, s.db.QueryRow("SELECT count(*) FROM artifact_mutations WHERE namespace_id='other' AND result_json IS NOT NULL").Scan(&retained))
	if purged != 0 || retained != 13 {
		t.Fatalf("namespace purge: diagnostics %d unrelated receipts %d", purged, retained)
	}
}

func TestStoreReopenMaintainsCurrentSchemaIndexes(t *testing.T) {
	s, hash, path := setupStore(t, StoreOptions{})
	ctx := context.Background()
	raw := createJSON("retained")
	original, err := s.Publish(ctx, hash, raw, PublicationOrigin{})
	requireNoError(t, err)
	saved, err := s.SaveState(ctx, hash, saveJSON(original.ArtifactID, "checkpoint", 1, 1, `{"kept":true}`))
	requireNoError(t, err)
	identity := s.ServiceID()
	usage := assertUsage(t, s)
	// This is already supported schema 2; omitted performance indexes must not
	// require a data migration or make index maintenance exclusive to fresh files.
	_, err = s.db.Exec("DROP INDEX IF EXISTS diagnostics_by_artifact_time; DROP INDEX IF EXISTS mutations_by_namespace")
	requireNoError(t, err)
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock})
	assertStoreIndexes(t, s)
	requireNoError(t, s.InstallGrant(ctx, hash, testScope()))
	retry, err := s.Publish(ctx, hash, raw, PublicationOrigin{})
	requireNoError(t, err)
	var version int
	requireNoError(t, s.db.QueryRow("PRAGMA user_version").Scan(&version))
	if version != 2 || retry != original || s.ServiceID() != identity || assertUsage(t, s) != usage {
		t.Fatal("index maintenance changed schema interpretation or content")
	}
	if state := readState(t, s, hash, original.ArtifactID); state.StateVersion != saved.StateVersion || string(state.State) != `{"kept":true}` {
		t.Fatalf("index maintenance changed checkpoint: %+v", state)
	}
}
