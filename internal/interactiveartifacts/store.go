package interactiveartifacts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	_ "modernc.org/sqlite" // SQLite is the durable artifact domain store.
)

// StoreSchemaVersion identifies the durable database schema used by service readiness.
const StoreSchemaVersion = 2

// storeHooks expose narrow fault boundaries to package process qualification.
// beforeAdmission runs before mu; commit hooks run while the writer owns mu.
// They are fixed when opening the store and must not call back into it.
type storeHooks struct{ beforeAdmission, beforeCommit, afterCommit func() }

// StoreOptions bounds persistent content and reader connections. Clock must be
// safe for concurrent calls. A zero quota uses 256 MiB of logical row content.
type StoreOptions struct {
	hooks             storeHooks
	Clock             func() time.Time
	QuotaBytes        int64
	ReaderConnections int
}

// Scope is trusted control-plane authority, never decoded from tool arguments.
// An empty ArtifactID permits the namespace; a nonempty ID narrows it to one object.
// OriginatingThreadID binds publications to the actual publishing conversation,
// which may be a child of the namespace owner. Publication grants require it.
// Generation identifies the issuing host's lease; it is not receipt identity.
type Scope struct {
	OriginatingThreadID                           string
	RealmID, PrincipalID, NamespaceID, ArtifactID string
	Methods                                       []string
	ExpiresAt                                     time.Time
	Generation                                    uint64
}

// Store serializes grant changes, deletion and writes through mu. Readers hold
// its read lock so authorization and the selected snapshot share that ordering.
// Only hashed grant identifiers enter this object, and no grants enter SQLite.
type Store struct {
	cursorKey [32]byte
	hooks     storeHooks
	mu        sync.RWMutex
	db        *sql.DB
	clock     func() time.Time
	quota     int64
	identity  string
	grants    map[[32]byte]Scope
	closed    bool
}

func OpenStore(path string, options StoreOptions) (_ *Store, resultErr error) {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.QuotaBytes == 0 {
		options.QuotaBytes = 256 << 20
	}
	if options.ReaderConnections == 0 {
		options.ReaderConnections = 4
	}
	if options.QuotaBytes < 0 || options.ReaderConnections < 1 || options.ReaderConnections > 32 {
		return nil, errors.New("invalid artifact store options")
	}
	directoryPath, name := filepath.Split(path)
	root, err := PrepareStoreDirectory(directoryPath)
	if err != nil {
		return nil, err
	}
	absolute := filepath.Join(root, name)
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		info, statErr := file.Stat()
		if statErr == nil {
			statErr = requirePrivateInfo(info, false)
		}
		if err := errors.Join(statErr, file.Close()); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := requirePrivatePath(absolute, false); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	if err := preflightStore(context.Background(), uri); err != nil {
		return nil, err
	}
	query := url.Values{}
	for _, pragma := range []string{"journal_mode(WAL)", "foreign_keys(ON)", "synchronous(FULL)", "busy_timeout(5000)"} {
		query.Add("_pragma", pragma)
	}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, db.Close())
		}
	}()
	db.SetMaxOpenConns(options.ReaderConnections + 1)
	db.SetMaxIdleConns(options.ReaderConnections + 1)
	s := &Store{db: db, clock: options.Clock, quota: options.QuotaBytes, grants: make(map[[32]byte]Scope), hooks: options.hooks}
	_, _ = rand.Read(s.cursorKey[:])
	if err := s.initialize(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func randomID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func (s *Store) initialize(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	version, err := acceptedStoreSchema(ctx, tx)
	if err != nil {
		return err
	}
	if version == 0 {
		if _, err := tx.ExecContext(ctx, storeSchema+storeQuotaSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", StoreSchemaVersion)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO store_identity(service_id) VALUES(?)", randomID()); err != nil {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT service_id FROM store_identity").Scan(&s.identity); err != nil {
		return err
	}
	// These access paths also belong to reopened, already-supported schema 2.
	if _, err := tx.ExecContext(ctx, storeIndexes); err != nil {
		return err
	}
	return tx.Commit()
}

const storeIndexes = `
 CREATE INDEX IF NOT EXISTS diagnostics_by_artifact_time ON artifact_diagnostics(artifact_id,reported_at);
 CREATE INDEX IF NOT EXISTS mutations_by_namespace ON artifact_mutations(namespace_id);
`

const storeSchema = `
 CREATE TABLE store_identity(service_id TEXT NOT NULL);
 CREATE TABLE artifact_namespaces(namespace_id TEXT PRIMARY KEY, realm_id TEXT NOT NULL, owner_thread_id TEXT NOT NULL, created_at TEXT NOT NULL, tombstoned_at TEXT);
 CREATE TABLE artifacts(artifact_id TEXT PRIMARY KEY, namespace_id TEXT NOT NULL REFERENCES artifact_namespaces(namespace_id), source_revision INTEGER NOT NULL CHECK(source_revision>=1), state_version INTEGER NOT NULL CHECK(state_version>=1), state_json BLOB, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
 CREATE INDEX artifacts_by_namespace ON artifacts(namespace_id,artifact_id);
 CREATE TABLE artifact_revisions(artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id), revision INTEGER NOT NULL, title TEXT NOT NULL, summary TEXT NOT NULL, html_utf8 TEXT NOT NULL, source_sha256 TEXT NOT NULL, created_by_principal_id TEXT NOT NULL, originating_thread_id TEXT NOT NULL, originating_tool_call_id TEXT, format TEXT NOT NULL CHECK(format='html'), format_version INTEGER NOT NULL CHECK(format_version=1), created_at TEXT NOT NULL, PRIMARY KEY(artifact_id,revision));
 CREATE TABLE artifact_mutations(realm_id TEXT NOT NULL,principal_id TEXT NOT NULL,operation TEXT NOT NULL,mutation_id TEXT NOT NULL,request_fingerprint TEXT NOT NULL,namespace_id TEXT NOT NULL REFERENCES artifact_namespaces(namespace_id),artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),outcome_code TEXT NOT NULL,result_json BLOB,committed_at TEXT NOT NULL,PRIMARY KEY(realm_id,principal_id,operation,mutation_id));
 CREATE TABLE artifact_diagnostics(id INTEGER PRIMARY KEY,artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),revision INTEGER NOT NULL,message TEXT NOT NULL,kind TEXT NOT NULL,reported_at INTEGER NOT NULL);
 CREATE INDEX diagnostics_by_artifact ON artifact_diagnostics(artifact_id,revision,id);
 
`

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	clear(s.grants)
	return s.db.Close()
}
func (s *Store) ServiceID() string { return s.identity }

func (s *Store) EnsureNamespace(ctx context.Context, id, realm, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.namespace(ctx, id, realm, owner, false)
}
func (s *Store) TombstoneNamespace(ctx context.Context, id, realm, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.namespace(ctx, id, realm, owner, true)
}
func (s *Store) namespace(ctx context.Context, id, realm, owner string, tombstone bool) error {
	if id == "" || realm == "" || owner == "" {
		return errors.New("namespace identity is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var storedRealm, storedOwner string
	var deleted sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT realm_id,owner_thread_id,tombstoned_at FROM artifact_namespaces WHERE namespace_id=?", id).Scan(&storedRealm, &storedOwner, &deleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		now := s.clock().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, "INSERT INTO artifact_namespaces(namespace_id,realm_id,owner_thread_id,created_at,tombstoned_at) VALUES(?,?,?,?,?)", id, realm, owner, now, sql.NullString{String: now, Valid: tombstone})
	case err != nil:
		return err
	case storedRealm != realm || storedOwner != owner:
		return &DomainError{Code: NotFoundOrForbidden}
	case deleted.Valid && !tombstone:
		return &DomainError{Code: Deleted}
	case tombstone && !deleted.Valid:
		_, err = tx.ExecContext(ctx, "UPDATE artifact_namespaces SET tombstoned_at=? WHERE namespace_id=?", s.clock().UTC().Format(time.RFC3339Nano), id)
	}
	if err != nil {
		return err
	}
	if tombstone {
		for _, statement := range []string{
			"DELETE FROM artifact_diagnostics WHERE artifact_id IN (SELECT artifact_id FROM artifacts WHERE namespace_id=?)",
			"DELETE FROM artifact_revisions WHERE artifact_id IN (SELECT artifact_id FROM artifacts WHERE namespace_id=?)",
			"UPDATE artifacts SET state_json=NULL WHERE namespace_id=?",
			"UPDATE artifact_mutations SET outcome_code='DELETED',result_json=NULL WHERE namespace_id=?",
		} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if tombstone {
		for hash, scope := range s.grants {
			if scope.NamespaceID == id {
				delete(s.grants, hash)
			}
		}
	}
	return nil
}

func (s *Store) InstallGrant(ctx context.Context, hash [32]byte, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if (slices.Contains(scope.Methods, "artifact_publish") && scope.OriginatingThreadID == "") || hash == ([32]byte{}) || scope.PrincipalID == "" || scope.Generation == 0 || !now.Before(scope.ExpiresAt) || len(scope.Methods) == 0 {
		return &DomainError{Code: NotFoundOrForbidden}
	}
	if err := s.checkNamespace(ctx, scope); err != nil {
		return err
	}
	// Issuance reclaims expired scopes at the same writer fence as revocation.
	// Active grants are retained; an idle service needs no cleanup goroutine.
	for installed, grant := range s.grants {
		if !now.Before(grant.ExpiresAt) {
			delete(s.grants, installed)
		}
	}
	if _, exists := s.grants[hash]; exists {
		return errors.New("artifact grant hash already installed")
	}
	scope.Methods = slices.Clone(scope.Methods)
	s.grants[hash] = scope
	return nil
}
func (s *Store) checkNamespace(ctx context.Context, scope Scope) error {
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM artifact_namespaces WHERE namespace_id=? AND realm_id=? AND tombstoned_at IS NULL", scope.NamespaceID, scope.RealmID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return &DomainError{Code: NotFoundOrForbidden}
	}
	return err
}

func (s *Store) RevokeGrant(ctx context.Context, hash [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Invoked revocation takes effect at the writer fence even if its caller
	// stopped waiting. Cancellation must not preserve already-received authority.
	delete(s.grants, hash)
	return ctx.Err()
}
