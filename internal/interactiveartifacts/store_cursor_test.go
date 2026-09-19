package interactiveartifacts

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStoreCursorBoundAndNamespaceBinding(t *testing.T) {
	s, _, _ := setupStore(t, StoreOptions{})
	for _, namespace := range []string{"short", "a\x00b", strings.Repeat("<é\x00", 20000)} {
		requireNoError(t, s.EnsureNamespace(t.Context(), namespace, "realm", "owner"))
		scope := testScope()
		scope.NamespaceID = namespace
		hash := sha256.Sum256([]byte(namespace))
		scope.PrincipalID = fmt.Sprintf("principal-%x", hash)
		requireNoError(t, s.InstallGrant(t.Context(), hash, scope))
		ids := make(map[string]bool)
		for i := range 2 {
			created, err := s.Publish(t.Context(), hash, createJSON(fmt.Sprintf("cursor-%d", i)), PublicationOrigin{})
			requireNoError(t, err)
			ids[created.ArtifactID] = true
		}
		page, err := s.List(t.Context(), hash, []byte(`{"limit":1}`))
		requireNoError(t, err)
		if len(page.NextCursor) == 0 || len(page.NextCursor) > 256 {
			t.Errorf("namespace bytes=%d cursor bytes=%d", len(namespace), len(page.NextCursor))
		}
		if len(page.Artifacts) != 1 || !ids[page.Artifacts[0].ArtifactID] {
			t.Fatal("invalid first page")
		}
		delete(ids, page.Artifacts[0].ArtifactID)
		raw, err := json.Marshal(ListRequest{Cursor: page.NextCursor})
		requireNoError(t, err)
		tail, err := s.List(t.Context(), hash, raw)
		requireNoError(t, err)
		if len(tail.Artifacts) != 1 || !ids[tail.Artifacts[0].ArtifactID] || tail.NextCursor != "" {
			t.Fatal("lost or repeated cursor entry")
		}
		for _, other := range []string{namespace + "\x00", namespace + "x", "a"} {
			if _, err := s.decodeCursor(page.NextCursor, other); err == nil {
				t.Fatal("cursor namespace binding lost")
			}
		}
		encoded, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
		requireNoError(t, err)
		encoded[0] ^= 1
		if _, err := s.decodeCursor(base64.RawURLEncoding.EncodeToString(encoded), namespace); err == nil {
			t.Fatal("tampered cursor accepted")
		}
	}
	// Framing must distinguish data that would collide under raw concatenation.
	if s.encodeCursor("a", "bc") == s.encodeCursor("ab", "c") {
		t.Fatal("ambiguous namespace and position framing")
	}
}
