package interactiveartifacts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
)

func (s *Store) List(ctx context.Context, hash [32]byte, raw []byte) (ListResult, error) {
	parsed, err := ParseRequest("artifact_list", raw, false)
	if err != nil {
		return ListResult{}, err
	}
	request := parsed.(*ListRequest)
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope, err := s.authorize(ctx, hash, "artifact_list", "")
	if err != nil {
		return ListResult{}, err
	}
	last := ""
	if request.Cursor != "" {
		last, err = s.decodeCursor(request.Cursor, scope.NamespaceID)
		if err != nil {
			return ListResult{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.artifact_id,r.title,r.summary,r.format,r.format_version,a.source_revision,a.state_version,a.created_at,a.updated_at FROM artifacts a JOIN artifact_revisions r ON r.artifact_id=a.artifact_id AND r.revision=a.source_revision WHERE a.namespace_id=? AND a.artifact_id>? AND (?='' OR a.artifact_id=?) ORDER BY a.artifact_id LIMIT ?`, scope.NamespaceID, last, scope.ArtifactID, scope.ArtifactID, int64(request.Limit)+1)
	if err != nil {
		return ListResult{}, err
	}
	defer func() { _ = rows.Close() }()
	result := ListResult{Artifacts: make([]ArtifactMetadata, 0)}
	encodedBytes := 0
	for rows.Next() {
		var item ArtifactMetadata
		if err := rows.Scan(&item.ArtifactID, &item.Title, &item.Summary, &item.Format, &item.FormatVersion, &item.SourceRevision, &item.StateVersion, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return ListResult{}, err
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return ListResult{}, err
		}
		if len(result.Artifacts) == int(request.Limit) || (len(result.Artifacts) > 0 && encodedBytes+len(encoded)+1 > maxListBytes) {
			result.NextCursor = s.encodeCursor(scope.NamespaceID, result.Artifacts[len(result.Artifacts)-1].ArtifactID)
			break
		}
		encodedBytes += len(encoded) + 1
		result.Artifacts = append(result.Artifacts, item)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	return result, nil
}

// Cursors carry position only. Their signature is per service run; every page
// separately authorizes the caller's current namespace and artifact restriction.
func (s *Store) encodeCursor(namespace, last string) string {
	raw, _ := json.Marshal(last)
	return base64.RawURLEncoding.EncodeToString(append(raw, s.cursorMAC(namespace, raw)...))
}
func (s *Store) decodeCursor(cursor, namespace string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) < sha256.Size {
		return "", errors.New("invalid artifact cursor")
	}
	body, signature := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	var position string
	if !hmac.Equal(signature, s.cursorMAC(namespace, body)) || json.Unmarshal(body, &position) != nil {
		return "", errors.New("invalid artifact cursor")
	}
	return position, nil
}

// JSON escaping excludes a literal NUL, separating namespace authority from the
// position without copying an unbounded namespace into each response cursor.
func (s *Store) cursorMAC(namespace string, position []byte) []byte {
	authority, _ := json.Marshal(namespace)
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(authority)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(position)
	return mac.Sum(nil)
}
