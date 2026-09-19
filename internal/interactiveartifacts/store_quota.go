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
	err := tx.QueryRowContext(ctx, "SELECT logical_bytes FROM artifact_usage WHERE singleton=1").Scan(&size)
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

// Each row delta belongs to the content transaction, including pruning, purge
// and rollback. The singleton makes admission independent of retained history.
const storeQuotaSchema = `
CREATE TABLE artifact_usage(singleton INTEGER PRIMARY KEY CHECK(singleton=1),logical_bytes INTEGER NOT NULL CHECK(logical_bytes>=0));
INSERT INTO artifact_usage VALUES(1,0);
CREATE TRIGGER artifacts_usage_insert AFTER INSERT ON artifacts BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.artifact_id||NEW.namespace_id||NEW.created_at||NEW.updated_at AS BLOB))+COALESCE(length(NEW.state_json),0)+16) WHERE singleton=1;
END;
CREATE TRIGGER artifacts_usage_update AFTER UPDATE ON artifacts BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.artifact_id||NEW.namespace_id||NEW.created_at||NEW.updated_at AS BLOB))+COALESCE(length(NEW.state_json),0)+16)-(length(CAST(OLD.artifact_id||OLD.namespace_id||OLD.created_at||OLD.updated_at AS BLOB))+COALESCE(length(OLD.state_json),0)+16) WHERE singleton=1;
END;
CREATE TRIGGER artifacts_usage_delete AFTER DELETE ON artifacts BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes-(length(CAST(OLD.artifact_id||OLD.namespace_id||OLD.created_at||OLD.updated_at AS BLOB))+COALESCE(length(OLD.state_json),0)+16) WHERE singleton=1;
END;
CREATE TRIGGER artifact_revisions_usage_insert AFTER INSERT ON artifact_revisions BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.artifact_id||NEW.title||NEW.summary||NEW.html_utf8||NEW.source_sha256||NEW.created_by_principal_id||NEW.originating_thread_id||COALESCE(NEW.originating_tool_call_id,'')||NEW.format||NEW.created_at AS BLOB))+16) WHERE singleton=1;
END;
CREATE TRIGGER artifact_revisions_usage_update AFTER UPDATE ON artifact_revisions BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.artifact_id||NEW.title||NEW.summary||NEW.html_utf8||NEW.source_sha256||NEW.created_by_principal_id||NEW.originating_thread_id||COALESCE(NEW.originating_tool_call_id,'')||NEW.format||NEW.created_at AS BLOB))+16)-(length(CAST(OLD.artifact_id||OLD.title||OLD.summary||OLD.html_utf8||OLD.source_sha256||OLD.created_by_principal_id||OLD.originating_thread_id||COALESCE(OLD.originating_tool_call_id,'')||OLD.format||OLD.created_at AS BLOB))+16) WHERE singleton=1;
END;
CREATE TRIGGER artifact_revisions_usage_delete AFTER DELETE ON artifact_revisions BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes-(length(CAST(OLD.artifact_id||OLD.title||OLD.summary||OLD.html_utf8||OLD.source_sha256||OLD.created_by_principal_id||OLD.originating_thread_id||COALESCE(OLD.originating_tool_call_id,'')||OLD.format||OLD.created_at AS BLOB))+16) WHERE singleton=1;
END;
CREATE TRIGGER artifact_mutations_usage_insert AFTER INSERT ON artifact_mutations BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.realm_id||NEW.principal_id||NEW.operation||NEW.mutation_id||NEW.request_fingerprint||NEW.namespace_id||NEW.artifact_id||NEW.outcome_code||NEW.committed_at AS BLOB))+COALESCE(length(NEW.result_json),0)) WHERE singleton=1;
END;
CREATE TRIGGER artifact_mutations_usage_update AFTER UPDATE ON artifact_mutations BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.realm_id||NEW.principal_id||NEW.operation||NEW.mutation_id||NEW.request_fingerprint||NEW.namespace_id||NEW.artifact_id||NEW.outcome_code||NEW.committed_at AS BLOB))+COALESCE(length(NEW.result_json),0))-(length(CAST(OLD.realm_id||OLD.principal_id||OLD.operation||OLD.mutation_id||OLD.request_fingerprint||OLD.namespace_id||OLD.artifact_id||OLD.outcome_code||OLD.committed_at AS BLOB))+COALESCE(length(OLD.result_json),0)) WHERE singleton=1;
END;
CREATE TRIGGER artifact_mutations_usage_delete AFTER DELETE ON artifact_mutations BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes-(length(CAST(OLD.realm_id||OLD.principal_id||OLD.operation||OLD.mutation_id||OLD.request_fingerprint||OLD.namespace_id||OLD.artifact_id||OLD.outcome_code||OLD.committed_at AS BLOB))+COALESCE(length(OLD.result_json),0)) WHERE singleton=1;
END;
CREATE TRIGGER artifact_diagnostics_usage_insert AFTER INSERT ON artifact_diagnostics BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.artifact_id||NEW.message||NEW.kind AS BLOB))+24) WHERE singleton=1;
END;
CREATE TRIGGER artifact_diagnostics_usage_update AFTER UPDATE ON artifact_diagnostics BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes+(length(CAST(NEW.artifact_id||NEW.message||NEW.kind AS BLOB))+24)-(length(CAST(OLD.artifact_id||OLD.message||OLD.kind AS BLOB))+24) WHERE singleton=1;
END;
CREATE TRIGGER artifact_diagnostics_usage_delete AFTER DELETE ON artifact_diagnostics BEGIN
 UPDATE artifact_usage SET logical_bytes=logical_bytes-(length(CAST(OLD.artifact_id||OLD.message||OLD.kind AS BLOB))+24) WHERE singleton=1;
END;
`
