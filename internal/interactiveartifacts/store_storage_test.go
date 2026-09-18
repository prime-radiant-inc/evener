package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

func TestStoreLogicalQuotaRetainsReceipts(t *testing.T) {
	s, hash, path := setupStore(t, StoreOptions{QuotaBytes: 3000})
	ctx := context.Background()
	original, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	var saved MutationReceipt
	var savedRaw, deniedRaw []byte
	current := 1
	for i := range 20 {
		raw := saveJSON(original.ArtifactID, fmt.Sprint("save", i), 1, current, `{}`)
		result, err := s.SaveState(ctx, hash, raw)
		if err != nil {
			requireCode(t, err, QuotaExceeded)
			deniedRaw = raw
			break
		}
		saved, savedRaw = result, raw
		current++
	}
	if deniedRaw == nil {
		t.Fatal("receipt growth ignored logical quota")
	}
	if savedRaw == nil {
		t.Fatal("fixture never accepted a checkpoint")
	}
	duplicate, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	if duplicate != original {
		t.Fatal("quota evicted creation receipt")
	}
	duplicate, err = s.SaveState(ctx, hash, savedRaw)
	requireNoError(t, err)
	if duplicate != saved {
		t.Fatal("quota evicted saved receipt")
	}
	if got := readState(t, s, hash, original.ArtifactID); got.StateVersion != Version(current) {
		t.Fatal("quota failure advanced state")
	}
	requireNoError(t, s.Close())
	s = openTestStore(t, path, StoreOptions{Clock: fixedClock, QuotaBytes: 100000})
	requireNoError(t, s.InstallGrant(ctx, hash, testScope()))
	accepted, err := s.SaveState(ctx, hash, deniedRaw)
	requireNoError(t, err)
	if accepted.StateVersion != Version(current+1) {
		t.Fatal("quota failure recorded acceptance")
	}
}

func TestStoreCoherentBackupRestoresIdentityAndReceipt(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	original, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	raw := saveJSON(original.ArtifactID, "save", 1, 1, `{"n":1}`)
	saved, err := s.SaveState(ctx, hash, raw)
	requireNoError(t, err)
	target := filepath.Join(t.TempDir(), "private", "snapshot ?#'.sqlite")
	requireNoError(t, s.Backup(ctx, target))
	info, err := os.Stat(target)
	requireNoError(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot is public")
	}
	if err := s.Backup(ctx, target); !errors.Is(err, os.ErrExist) {
		t.Fatalf("backup overwrote snapshot: %v", err)
	}
	restored := openTestStore(t, target, StoreOptions{Clock: fixedClock})
	if restored.ServiceID() != s.ServiceID() {
		t.Fatal("snapshot lost identity")
	}
	_, err = restored.SaveState(ctx, hash, raw)
	requireCode(t, err, NotFoundOrForbidden)
	fresh := sha256.Sum256([]byte("restore grant"))
	requireNoError(t, restored.InstallGrant(ctx, fresh, testScope()))
	retry, err := restored.SaveState(ctx, fresh, raw)
	requireNoError(t, err)
	if retry != saved {
		t.Fatal("snapshot lost exact durable receipt")
	}
	if got := readState(t, restored, fresh, original.ArtifactID); got.StateVersion != 2 || string(got.State) != `{"n":1}` {
		t.Fatal("snapshot lost committed WAL data")
	}
}

func TestStoreSQLiteFullDoesNotAcknowledgeMutation(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{})
	ctx := context.Background()
	original, err := s.Publish(ctx, hash, createJSON("create"), PublicationOrigin{})
	requireNoError(t, err)
	// max_page_count makes the actual SQLite writer fail with SQLITE_FULL. This
	// exercises database write failure, not a claim of physical filesystem ENOSPC.
	var pages int
	requireNoError(t, s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages))
	var capped int
	requireNoError(t, s.db.QueryRowContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages)).Scan(&capped))
	raw := fmt.Appendf(nil, `{"artifactId":%q,"mutationId":"full","expectedSourceRevision":1,"expectedStateVersion":1,"title":"T","summary":"S","html":%q}`, original.ArtifactID, strings.Repeat("large source ", 60000))
	_, err = s.Publish(ctx, hash, raw, PublicationOrigin{})
	var dbError *sqlite.Error
	if !errors.As(err, &dbError) || dbError.Code() != 13 {
		t.Fatalf("want real SQLITE_FULL, got %v", err)
	}
	if got := readState(t, s, hash, original.ArtifactID); got.SourceRevision != 1 {
		t.Fatal("failed write changed source head")
	}
	var receipts int
	requireNoError(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifact_mutations WHERE mutation_id='full'").Scan(&receipts))
	if receipts != 0 {
		t.Fatal("failed write persisted receipt")
	}
	requireNoError(t, s.db.QueryRowContext(ctx, "PRAGMA max_page_count=10000").Scan(&capped))
	receipt, err := s.Publish(ctx, hash, raw, PublicationOrigin{})
	requireNoError(t, err)
	if receipt.SourceRevision != 2 {
		t.Fatal("failed request cannot be retried")
	}
}

func TestStoreQuotaCountsRetainedSourceRevisions(t *testing.T) {
	s, hash, _ := setupStore(t, StoreOptions{QuotaBytes: 10000})
	ctx := context.Background()
	source := strings.Repeat("a", 4000)
	raw := fmt.Appendf(nil, `{"mutationId":"first","title":"T","summary":"S","html":%q}`, source)
	created, err := s.Publish(ctx, hash, raw, PublicationOrigin{})
	requireNoError(t, err)
	next := func(id string, revision int) []byte {
		return fmt.Appendf(nil, `{"artifactId":%q,"mutationId":%q,"expectedSourceRevision":%d,"expectedStateVersion":1,"title":"T","summary":"S","html":%q}`, created.ArtifactID, id, revision, source)
	}
	_, err = s.Publish(ctx, hash, next("second", 1), PublicationOrigin{})
	requireNoError(t, err)
	_, err = s.Publish(ctx, hash, next("third", 2), PublicationOrigin{})
	requireCode(t, err, QuotaExceeded)
	var firstSource string
	requireNoError(t, s.db.QueryRowContext(ctx, "SELECT html_utf8 FROM artifact_revisions WHERE artifact_id=? AND revision=1", created.ArtifactID).Scan(&firstSource))
	if firstSource != source {
		t.Fatal("quota discarded immutable source history")
	}
	if got := readState(t, s, hash, created.ArtifactID); got.SourceRevision != 2 {
		t.Fatal("quota rejection changed current source")
	}
}
