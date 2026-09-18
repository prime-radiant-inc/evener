package interactiveartifacts

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
)

func artifactMetadata(ctx context.Context, tx *sql.Tx, scope Scope, id string) (ArtifactMetadata, error) {
	var result ArtifactMetadata
	err := tx.QueryRowContext(ctx, `SELECT a.artifact_id,r.title,r.summary,a.source_revision,a.state_version,a.created_at,a.updated_at FROM artifacts a JOIN artifact_revisions r ON r.artifact_id=a.artifact_id AND r.revision=a.source_revision WHERE a.artifact_id=? AND a.namespace_id=?`, id, scope.NamespaceID).Scan(&result.ArtifactID, &result.Title, &result.Summary, &result.SourceRevision, &result.StateVersion, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactMetadata{}, &DomainError{Code: NotFoundOrForbidden}
	}
	result.Format = "html"
	result.FormatVersion = 1
	return result, err
}

func (s *Store) Read(ctx context.Context, hash [32]byte, raw []byte) (ReadResult, error) {
	parsed, err := ParseRequest("artifact_read", raw, false)
	if err != nil {
		return ReadResult{}, err
	}
	request := parsed.(*ReadRequest)
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope, err := s.authorize(ctx, hash, "artifact_read", request.ArtifactID)
	if err != nil {
		return ReadResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReadResult{}, err
	}
	defer tx.Rollback()
	metadata, err := artifactMetadata(ctx, tx, scope, request.ArtifactID)
	if err != nil {
		return ReadResult{}, err
	}
	result := ReadResult{ArtifactMetadata: metadata}
	if slices.Contains(request.Include, "source") {
		result.Source, err = readSource(ctx, tx, metadata.ArtifactID, metadata.SourceRevision)
		if err != nil {
			return ReadResult{}, err
		}
		if request.SourceStartLine != 0 || request.SourceEndLine != 0 {
			lines := strings.Split(result.Source.HTML, "\n")
			start, end := request.SourceStartLine, request.SourceEndLine
			if start == 0 {
				start = 1
			}
			if end == 0 || end > Version(len(lines)) {
				end = Version(len(lines))
			}
			if start > end {
				return ReadResult{}, errors.New("source range exceeds document")
			}
			result.Source.HTML = strings.Join(lines[start-1:end], "\n")
			result.Source.StartLine = start
			result.Source.EndLine = end
		}
	}
	if slices.Contains(request.Include, "state") {
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM artifacts WHERE artifact_id=?", request.ArtifactID).Scan(&result.State); err != nil {
			return ReadResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ReadResult{}, err
	}
	return result, nil
}
func readSource(ctx context.Context, tx *sql.Tx, id string, revision Version) (*SourceBody, error) {
	result := &SourceBody{}
	err := tx.QueryRowContext(ctx, "SELECT html_utf8,source_sha256 FROM artifact_revisions WHERE artifact_id=? AND revision=?", id, revision).Scan(&result.HTML, &result.SourceSHA256)
	return result, err
}
