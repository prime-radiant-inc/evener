package interactiveartifacts

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Recompute from stored SQLite values rather than the counter or its SQL size
// expressions. Every field of these four content tables belongs to the quota:
// strings/blobs count bytes, integers eight bytes, and NULL counts zero.
func assertUsage(t *testing.T, s *Store) int64 {
	t.Helper()
	var expected int64
	for _, table := range []string{"artifacts", "artifact_revisions", "artifact_mutations", "artifact_diagnostics"} {
		rows, err := s.db.Query("SELECT * FROM " + table)
		requireNoError(t, err)
		columns, err := rows.Columns()
		requireNoError(t, err)
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			requireNoError(t, rows.Scan(destinations...))
			for _, value := range values {
				switch v := value.(type) {
				case nil:
				case string:
					expected += int64(len(v))
				case []byte:
					expected += int64(len(v))
				case int64:
					expected += 8
				default:
					t.Fatalf("unexpected quota field %T", value)
				}
			}
		}
		requireNoError(t, rows.Err())
		requireNoError(t, rows.Close())
	}
	var actual int64
	requireNoError(t, s.db.QueryRow("SELECT logical_bytes FROM artifact_usage WHERE singleton=1").Scan(&actual))
	if actual != expected {
		t.Fatalf("counter %d differs from stored content %d", actual, expected)
	}
	return actual
}

func TestStoreQuotaCounterFieldDeltas(t *testing.T) {
	s, _, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	exec := func(query string) { t.Helper(); _, err := s.db.ExecContext(ctx, query); requireNoError(t, err) }
	check := func(want int64) {
		t.Helper()
		if got := assertUsage(t, s); got != want {
			t.Fatalf("hand-calculated bytes: got %d want %d", got, want)
		}
	}
	check(0)
	// a + namespace + c + u = 12 bytes, two integers = 16, NULL = 0.
	exec("INSERT INTO artifacts VALUES('a','namespace',1,1,NULL,'c','u')")
	check(28)
	exec("INSERT INTO artifact_revisions VALUES('a',1,'é','s','h','x','p','t',NULL,'html',1,'c')")
	check(28 + 29)
	exec("INSERT INTO artifact_mutations VALUES('r','p','o','m','f','namespace','a','OK',X'0001','c')")
	check(28 + 29 + 20)
	exec("INSERT INTO artifact_diagnostics VALUES(1,'a',1,'é','k',1)")
	check(28 + 29 + 20 + 28)
	exec("UPDATE artifacts SET state_version=20,state_json=X'000102'")
	exec("UPDATE artifact_revisions SET originating_tool_call_id='界'")
	exec("UPDATE artifact_mutations SET result_json=NULL,outcome_code='DELETED'")
	exec("UPDATE artifact_diagnostics SET message='x',revision=2")
	check(28 + 29 + 20 + 28 + 3 + 3 + 3 - 1)
	exec("DELETE FROM artifact_diagnostics")
	exec("DELETE FROM artifact_revisions")
	exec("DELETE FROM artifact_mutations")
	exec("DELETE FROM artifacts")
	check(0)
}

func TestStoreQuotaCounterLifecycle(t *testing.T) {
	now := fixedClock()
	s, hash, path := setupStore(t, StoreOptions{Clock: func() time.Time { return now }})
	ctx := context.Background()
	created, err := s.Publish(ctx, hash, createJSON("first"), PublicationOrigin{ToolCallID: "界"})
	requireNoError(t, err)
	assertUsage(t, s)
	_, err = s.Publish(ctx, hash, publishJSON(created.ArtifactID, "second", 1, 1), PublicationOrigin{})
	requireNoError(t, err)
	assertUsage(t, s)
	for i, state := range []string{`{"text":"é界-longer"}`, `{}`} {
		_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, fmt.Sprint("save", i), 2, i+1, state))
		requireNoError(t, err)
		assertUsage(t, s)
	}
	conflict := saveJSON(created.ArtifactID, "conflict", 1, 1, `{}`)
	_, err = s.SaveState(ctx, hash, conflict)
	requireCode(t, err, SourceConflict)
	before := assertUsage(t, s)
	_, err = s.SaveState(ctx, hash, conflict)
	requireCode(t, err, SourceConflict)
	if assertUsage(t, s) != before {
		t.Fatal("exact retry grew quota")
	}
	for i := range 25 {
		now = now.Add(time.Minute)
		_, err = s.ReportDiagnostic(ctx, hash, fmt.Appendf(nil, `{"artifactId":%q,"sourceRevision":2,"kind":"runtime","message":%q}`, created.ArtifactID, fmt.Sprint("é", i)))
		requireNoError(t, err)
		assertUsage(t, s)
	}
	var count int
	requireNoError(t, s.db.QueryRow("SELECT count(*) FROM artifact_diagnostics").Scan(&count))
	if count != 20 {
		t.Fatalf("pruning retained %d", count)
	}
	before = assertUsage(t, s)
	s.quota = before
	_, err = s.SaveState(ctx, hash, saveJSON(created.ArtifactID, "quota rejected", 2, 3, `{"new":"larger"}`))
	requireCode(t, err, QuotaExceeded)
	if assertUsage(t, s) != before {
		t.Fatal("rollback retained usage")
	}
	snapshot := filepath.Join(t.TempDir(), "private", "snapshot.sqlite")
	requireNoError(t, s.Backup(ctx, snapshot))
	restored := openTestStore(t, snapshot, StoreOptions{})
	if assertUsage(t, restored) != before {
		t.Fatal("backup lost usage")
	}
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{})
	if assertUsage(t, s) != before {
		t.Fatal("reopen lost usage")
	}
	requireNoError(t, s.TombstoneNamespace(ctx, "namespace", "realm", "owner"))
	if assertUsage(t, s) >= before {
		t.Fatal("purge did not reduce quota")
	}
}

func TestStoreRefusesSchemaOneWithoutChangingContent(t *testing.T) {
	s, hash, path := setupStore(t, StoreOptions{})
	_, err := s.Publish(context.Background(), hash, createJSON("retained"), PublicationOrigin{})
	requireNoError(t, err)
	_, err = s.db.Exec("PRAGMA user_version=1")
	requireNoError(t, err)
	requireNoError(t, s.Close())
	reopened, err := OpenStore(path, StoreOptions{})
	if err == nil {
		requireNoError(t, reopened.Close())
		t.Fatal("schema 1 was silently accepted")
	}
	db, err := sql.Open("sqlite", path)
	requireNoError(t, err)
	defer db.Close()
	var version, receipts int
	requireNoError(t, db.QueryRow("PRAGMA user_version").Scan(&version))
	requireNoError(t, db.QueryRow("SELECT count(*) FROM artifact_mutations WHERE mutation_id='retained'").Scan(&receipts))
	if version != 1 || receipts != 1 {
		t.Fatalf("refusal changed content: version %d receipts %d", version, receipts)
	}
}
