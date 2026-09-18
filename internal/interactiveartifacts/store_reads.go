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
	return s.readArtifact(ctx, hash, "artifact_read", request, 0, 0)
}

func (s *Store) readArtifact(ctx context.Context, hash [32]byte, method string, request *ReadRequest, knownSource, knownState Version) (ReadResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope, err := s.authorize(ctx, hash, method, request.ArtifactID)
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
	if slices.Contains(request.Include, "source") && knownSource != metadata.SourceRevision {
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
	if slices.Contains(request.Include, "state") && knownState != metadata.StateVersion {
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM artifacts WHERE artifact_id=?", request.ArtifactID).Scan(&result.State); err != nil {
			return ReadResult{}, err
		}
	}
	if slices.Contains(request.Include, "diagnostics") {
		result.Diagnostics, err = readDiagnostics(ctx, tx, metadata.ArtifactID, metadata.SourceRevision)
		if err != nil {
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

func (s *Store) GetView(ctx context.Context, hash [32]byte, raw []byte) (GetViewResult, error) {
	parsed, err := ParseRequest("artifact_get_view", raw, false)
	if err != nil {
		return GetViewResult{}, err
	}
	request := parsed.(*GetViewRequest)
	result, err := s.readArtifact(ctx, hash, "artifact_get_view", &ReadRequest{ArtifactID: request.ArtifactID, Include: []Include{"source", "state"}}, request.KnownSourceRevision, request.KnownStateVersion)
	if err != nil {
		return GetViewResult{}, err
	}
	return GetViewResult{ArtifactMetadata: result.ArtifactMetadata, Source: result.Source, State: result.State}, nil
}
func (s *Store) Open(ctx context.Context, hash [32]byte, raw []byte) (OpenResult, error) {
	parsed, err := ParseRequest("artifact_open", raw, false)
	if err != nil {
		return OpenResult{}, err
	}
	request := parsed.(*OpenRequest)
	result, err := s.readArtifact(ctx, hash, "artifact_open", &ReadRequest{ArtifactID: request.ArtifactID}, 0, 0)
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{ArtifactMetadata: result.ArtifactMetadata, Launch: LaunchData{ArtifactID: request.ArtifactID}}, nil
}
