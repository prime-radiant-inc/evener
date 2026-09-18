package interactiveartifacts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

func (s *Store) Publish(ctx context.Context, hash [32]byte, raw []byte) (MutationReceipt, error) {
	return s.mutate(ctx, hash, "artifact_publish", raw)
}
func (s *Store) SaveState(ctx context.Context, hash [32]byte, raw []byte) (MutationReceipt, error) {
	return s.mutate(ctx, hash, "artifact_save_state", raw)
}

func (s *Store) authorize(ctx context.Context, hash [32]byte, method, artifact string) (Scope, error) {
	scope, ok := s.grants[hash]
	if !ok || !s.clock().Before(scope.ExpiresAt) || !slices.Contains(scope.Methods, method) || (artifact != "" && scope.ArtifactID != "" && scope.ArtifactID != artifact) {
		return Scope{}, &DomainError{Code: NotFoundOrForbidden}
	}
	if err := s.checkNamespace(ctx, scope); err != nil {
		return Scope{}, err
	}
	return scope, nil
}

func (s *Store) mutate(ctx context.Context, hash [32]byte, operation string, raw []byte) (MutationReceipt, error) {
	raw = slices.Clone(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	scope, err := s.authorize(ctx, hash, operation, "")
	if err != nil {
		return MutationReceipt{}, err
	}
	parsed, err := ParseRequest(operation, raw, false)
	if err != nil {
		return MutationReceipt{}, err
	}
	var artifact, id string
	var source, state Version
	switch request := parsed.(type) {
	case *PublishRequest:
		artifact, id, source, state = request.ArtifactID, request.MutationID, request.ExpectedSourceRevision, request.ExpectedStateVersion
	case *SaveStateRequest:
		artifact, id, source, state = request.ArtifactID, request.MutationID, request.ExpectedSourceRevision, request.ExpectedStateVersion
	}
	if artifact != "" && scope.ArtifactID != "" && artifact != scope.ArtifactID {
		return MutationReceipt{}, &DomainError{Code: NotFoundOrForbidden}
	}
	fingerprint, err := Fingerprint(scope.NamespaceID, raw)
	if err != nil {
		return MutationReceipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationReceipt{}, err
	}
	defer tx.Rollback()
	var head ArtifactMetadata
	if artifact != "" {
		head, err = artifactMetadata(ctx, tx, scope, artifact)
		if err != nil {
			return MutationReceipt{}, err
		}
	}
	receipt, outcome, found, err := lookupReceipt(ctx, tx, scope, operation, id, fingerprint)
	if err != nil {
		return MutationReceipt{}, err
	}
	if found {
		if outcome != nil {
			return MutationReceipt{}, outcome
		}
		return receipt, nil
	}
	if artifact == "" && scope.ArtifactID != "" {
		return MutationReceipt{}, &DomainError{Code: NotFoundOrForbidden}
	}
	if artifact != "" {
		if head.SourceRevision != source {
			outcome = &DomainError{Code: SourceConflict, SourceRevision: head.SourceRevision, StateVersion: head.StateVersion}
		} else if head.StateVersion != state {
			outcome = &DomainError{Code: StateConflict, SourceRevision: head.SourceRevision, StateVersion: head.StateVersion}
		}
	}
	if outcome == nil {
		switch request := parsed.(type) {
		case *PublishRequest:
			receipt, err = s.applyPublication(ctx, tx, scope, request, head)
		case *SaveStateRequest:
			if head.StateVersion >= Version(MaxSafeInteger) {
				return MutationReceipt{}, &DomainError{Code: TooLarge}
			}
			receipt = MutationReceipt{Status: StatusCommitted, MutationID: id, ArtifactID: artifact, SourceRevision: head.SourceRevision, StateVersion: head.StateVersion + 1}
			_, err = tx.ExecContext(ctx, "UPDATE artifacts SET state_version=?,state_json=?,updated_at=? WHERE artifact_id=?", receipt.StateVersion, []byte(request.State), s.clock().UTC().Format(time.RFC3339Nano), artifact)
		}
		if err != nil {
			return MutationReceipt{}, err
		}
		artifact = receipt.ArtifactID
	}
	var encoded []byte
	code := "committed"
	if outcome != nil {
		code = string(outcome.Code)
		encoded, err = json.Marshal(outcome)
	} else {
		encoded, err = json.Marshal(receipt)
	}
	if err != nil {
		return MutationReceipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_mutations(realm_id,principal_id,operation,mutation_id,request_fingerprint,namespace_id,artifact_id,outcome_code,result_json) VALUES(?,?,?,?,?,?,?,?,?)`, scope.RealmID, scope.PrincipalID, operation, id, fingerprint, scope.NamespaceID, artifact, code, encoded)
	if err != nil {
		return MutationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationReceipt{}, err
	}
	if outcome != nil {
		return MutationReceipt{}, outcome
	}
	return receipt, nil
}

func lookupReceipt(ctx context.Context, tx *sql.Tx, scope Scope, operation, id, fingerprint string) (MutationReceipt, *DomainError, bool, error) {
	var storedFingerprint, namespace, artifact, code string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT request_fingerprint,namespace_id,artifact_id,outcome_code,result_json FROM artifact_mutations WHERE realm_id=? AND principal_id=? AND operation=? AND mutation_id=?`, scope.RealmID, scope.PrincipalID, operation, id).Scan(&storedFingerprint, &namespace, &artifact, &code, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return MutationReceipt{}, nil, false, nil
	}
	if err != nil {
		return MutationReceipt{}, nil, false, err
	}
	if namespace != scope.NamespaceID || (scope.ArtifactID != "" && scope.ArtifactID != artifact) {
		return MutationReceipt{}, nil, false, &DomainError{Code: NotFoundOrForbidden}
	}
	if fingerprint != storedFingerprint {
		return MutationReceipt{}, nil, false, &DomainError{Code: MutationIDReused}
	}
	if code == "committed" {
		var receipt MutationReceipt
		err = json.Unmarshal(raw, &receipt)
		return receipt, nil, true, err
	}
	if code == string(Deleted) {
		return MutationReceipt{}, &DomainError{Code: Deleted}, true, nil
	}
	var domain DomainError
	err = json.Unmarshal(raw, &domain)
	return MutationReceipt{}, &domain, true, err
}

func (s *Store) applyPublication(ctx context.Context, tx *sql.Tx, scope Scope, request *PublishRequest, head ArtifactMetadata) (MutationReceipt, error) {
	id := request.ArtifactID
	source, state := head.SourceRevision, head.StateVersion
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if id == "" {
		id = randomID()
		source, state = 1, 1
		if _, err := tx.ExecContext(ctx, "INSERT INTO artifacts(artifact_id,namespace_id,source_revision,state_version,state_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", id, scope.NamespaceID, source, state, []byte(request.InitialState), now, now); err != nil {
			return MutationReceipt{}, err
		}
	} else {
		if source >= Version(MaxSafeInteger) {
			return MutationReceipt{}, &DomainError{Code: TooLarge}
		}
		source++
		if _, err := tx.ExecContext(ctx, "UPDATE artifacts SET source_revision=?,updated_at=? WHERE artifact_id=?", source, now, id); err != nil {
			return MutationReceipt{}, err
		}
	}
	sum := sha256.Sum256([]byte(request.HTML))
	_, err := tx.ExecContext(ctx, `INSERT INTO artifact_revisions(artifact_id,revision,title,summary,html_utf8,source_sha256,created_by_principal_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, source, request.Title, request.Summary, request.HTML, hex.EncodeToString(sum[:]), scope.PrincipalID, now)
	if err != nil {
		return MutationReceipt{}, err
	}
	return MutationReceipt{Status: StatusCommitted, MutationID: request.MutationID, ArtifactID: id, SourceRevision: source, StateVersion: state}, nil
}
