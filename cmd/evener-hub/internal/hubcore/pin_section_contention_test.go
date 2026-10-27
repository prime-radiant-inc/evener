package hubcore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// contentionConns opens two connections on one temp-dir WAL database with no
// busy_timeout, so a lock conflict between them surfaces immediately as the
// driver's own error.
func contentionConns(t *testing.T) (holder, contender *sql.Conn) {
	t.Helper()
	dsn := "file:" + sqliteURIPathEscaper.Replace(filepath.Join(t.TempDir(), "contention.db")) + "?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	holder, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	contender, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close(); _ = contender.Close() })
	if _, err := holder.ExecContext(ctx, `CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	return holder, contender
}

// realBusyError is the error a writer gets when another connection holds the
// write lock.
func realBusyError(t *testing.T) error {
	t.Helper()
	holder, contender := contentionConns(t)
	ctx := context.Background()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	_, err := contender.ExecContext(ctx, `BEGIN IMMEDIATE`)
	if err == nil {
		t.Fatal("second BEGIN IMMEDIATE succeeded while the write lock was held")
	}
	return err
}

// realBusySnapshotError is the error a deferred transaction gets when it
// upgrades to a write after another connection committed since its read.
func realBusySnapshotError(t *testing.T) error {
	t.Helper()
	holder, contender := contentionConns(t)
	ctx := context.Background()
	if _, err := contender.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := contender.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	_, err := contender.ExecContext(ctx, `INSERT INTO t VALUES (2)`)
	if err == nil {
		t.Fatal("write upgrade succeeded on a stale read snapshot")
	}
	return err
}

// realNonLockError is a real driver error that retrying cannot fix. The table
// name contains "busy" so text matching on the message would misclassify it.
func realNonLockError(t *testing.T) error {
	t.Helper()
	holder, _ := contentionConns(t)
	_, err := holder.ExecContext(context.Background(), `INSERT INTO busy_table VALUES (1)`)
	if err == nil {
		t.Fatal("insert into a missing table named busy succeeded")
	}
	return err
}

func TestIsSQLiteRetryableClassifiesRealDriverErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"busy", realBusyError(t), true},
		{"busy snapshot", realBusySnapshotError(t), true},
		{"non-lock error", realNonLockError(t), false},
	}
	for _, c := range cases {
		if got := isSQLiteRetryable(c.err); got != c.want {
			t.Errorf("%s: isSQLiteRetryable = %v, want %v (%v)", c.name, got, c.want, c.err)
		}
	}
}
