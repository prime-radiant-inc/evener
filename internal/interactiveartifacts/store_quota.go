package interactiveartifacts

import (
	"context"
	"database/sql"
)

// logicalUsage counts UTF-8/blob bytes in artifact, revision, receipt and
// diagnostic row fields, plus eight bytes per integer. NULL bodies count zero.
// Namespace/service control identity and SQLite indexes/pages/WAL are excluded.
// This is a logical retained-content quota, not a physical disk-space bound.
// Receipts are never evicted to make room; a rejected growth transaction rolls back.
func logicalUsage(ctx context.Context, tx *sql.Tx) (int64, error) {
	var size int64
	err := tx.QueryRowContext(ctx, `
 SELECT
 COALESCE((SELECT sum(length(CAST(artifact_id||namespace_id||created_at||updated_at AS BLOB))+COALESCE(length(state_json),0)+16) FROM artifacts),0)+
 COALESCE((SELECT sum(length(CAST(artifact_id||title||summary||html_utf8||source_sha256||created_by_principal_id||created_at AS BLOB))+8) FROM artifact_revisions),0)+
 COALESCE((SELECT sum(length(CAST(realm_id||principal_id||operation||mutation_id||request_fingerprint||namespace_id||artifact_id||outcome_code AS BLOB))+COALESCE(length(result_json),0)) FROM artifact_mutations),0)+
 COALESCE((SELECT sum(length(CAST(artifact_id||message||kind AS BLOB))+24) FROM artifact_diagnostics),0)
 `).Scan(&size)
	return size, err
}
func (s *Store) checkGrowth(ctx context.Context, tx *sql.Tx, before int64) error {
	after, err := logicalUsage(ctx, tx)
	if err != nil {
		return err
	}
	if after > s.quota && after > before {
		return &DomainError{Code: QuotaExceeded}
	}
	return nil
}
