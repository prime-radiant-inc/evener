package interactiveartifacts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) ReportDiagnostic(ctx context.Context, hash [32]byte, raw []byte) (DiagnosticResult, error) {
	parsed, err := ParseRequest("artifact_report_diagnostic", raw, false)
	if err != nil {
		return DiagnosticResult{}, err
	}
	request := parsed.(*ReportDiagnosticRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	scope, err := s.authorize(ctx, hash, "artifact_report_diagnostic", request.ArtifactID)
	if err != nil {
		return DiagnosticResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DiagnosticResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := artifactMetadata(ctx, tx, scope, request.ArtifactID); err != nil {
		return DiagnosticResult{}, err
	}
	var count int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM artifact_revisions WHERE artifact_id=? AND revision=?", request.ArtifactID, request.SourceRevision).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return DiagnosticResult{}, &DomainError{Code: NotFoundOrForbidden}
	}
	if err != nil {
		return DiagnosticResult{}, err
	}
	before, err := logicalUsage(ctx, tx)
	if err != nil {
		return DiagnosticResult{}, err
	}
	now := s.clock()
	// The artifact-wide bound remains in force across grant renewal and restart.
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM artifact_diagnostics WHERE artifact_id=? AND reported_at>?", request.ArtifactID, now.Add(-time.Minute).UnixMicro()).Scan(&count); err != nil {
		return DiagnosticResult{}, err
	}
	if count >= 20 {
		return DiagnosticResult{}, &DomainError{Code: Busy, Retryable: true}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO artifact_diagnostics(artifact_id,revision,message,kind,reported_at) VALUES(?,?,?,?,?)", request.ArtifactID, request.SourceRevision, request.Message, string(request.Kind), now.UnixMicro()); err != nil {
		return DiagnosticResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM artifact_diagnostics WHERE artifact_id=? AND revision=? AND id NOT IN (SELECT id FROM artifact_diagnostics WHERE artifact_id=? AND revision=? ORDER BY id DESC LIMIT 20)", request.ArtifactID, request.SourceRevision, request.ArtifactID, request.SourceRevision); err != nil {
		return DiagnosticResult{}, err
	}
	if err := s.checkGrowth(ctx, tx, before); err != nil {
		return DiagnosticResult{}, err
	}
	if _, err := s.authorize(ctx, hash, "artifact_report_diagnostic", request.ArtifactID); err != nil {
		return DiagnosticResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DiagnosticResult{}, err
	}
	return DiagnosticResult{Status: StatusAcknowledged}, nil
}

func readDiagnostics(ctx context.Context, tx *sql.Tx, id string, revision Version) ([]Diagnostic, error) {
	rows, err := tx.QueryContext(ctx, "SELECT revision,message,kind FROM artifact_diagnostics WHERE artifact_id=? AND revision=? ORDER BY id DESC LIMIT 20", id, revision)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]Diagnostic, 0)
	for rows.Next() {
		var diagnostic Diagnostic
		if err := rows.Scan(&diagnostic.SourceRevision, &diagnostic.Message, &diagnostic.Kind); err != nil {
			return nil, err
		}
		result = append(result, diagnostic)
	}
	return result, rows.Err()
}
