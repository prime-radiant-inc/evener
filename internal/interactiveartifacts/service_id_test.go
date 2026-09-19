package interactiveartifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func sizedProtocolID(t *testing.T, prefix string, size int) string {
	t.Helper()
	id, err := jsonrpc.MakeID(prefix)
	requireNoError(t, err)
	envelope, err := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: id})
	requireNoError(t, err)
	if len(envelope) > size {
		t.Fatal("ID prefix exceeds fixture size")
	}
	return prefix + strings.Repeat("x", size-len(envelope))
}

func toolMessageWithID(t *testing.T, value any, name string, arguments any) []byte {
	t.Helper()
	id, err := jsonrpc.MakeID(value)
	requireNoError(t, err)
	params, err := json.Marshal(&mcp.CallToolParams{Name: name, Arguments: arguments})
	requireNoError(t, err)
	body, err := jsonrpc.EncodeMessage(&jsonrpc.Request{ID: id, Method: "tools/call", Params: params})
	requireNoError(t, err)
	if len(body) > MaxRequestBytes {
		t.Fatal("fixture exceeds outer body limit")
	}
	return body
}

func listResponseWithID(t *testing.T, wire []byte, want any) ListResult {
	t.Helper()
	message, err := jsonrpc.DecodeMessage(wire)
	requireNoError(t, err)
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.Error != nil || response.ID.Raw() != want {
		t.Fatalf("incorrect SDK response identity/type: %T", message)
	}
	var result struct {
		IsError           bool       `json:"isError"`
		StructuredContent ListResult `json:"structuredContent"`
	}
	requireNoError(t, json.Unmarshal(response.Result, &result))
	if result.IsError {
		t.Fatal("list returned domain rejection")
	}
	return result.StructuredContent
}

func TestServiceRequestIDEnvelopeBoundary(t *testing.T) {
	for name, prefix := range map[string]string{"ascii": "id", "json-escapes": "\"\\\x00", "line-separator": "\u2028", "html": "<>&"} {
		t.Run(name, func(t *testing.T) {
			s, token, entered := protocolServiceFixture(t)
			id := sizedProtocolID(t, prefix, 1024)
			body := toolMessageWithID(t, id, "artifact_list", ListRequest{})
			// Different lexical representations have the same echoed SDK identity.
			for _, encoded := range [][]byte{body, bytes.ReplaceAll(body, []byte("x"), []byte(`\u0078`))} {
				status, wire := postProtocol(t, s, token, []string{"2025-11-25"}, encoded)
				if status != http.StatusOK {
					t.Fatalf("boundary ID rejected: %d", status)
				}
				if page := listResponseWithID(t, wire, id); len(page.Artifacts) != 0 {
					t.Fatal("unexpected artifacts")
				}
			}
			over := toolMessageWithID(t, id+"x", "artifact_publish", json.RawMessage(createJSON("oversized-id")))
			status, wire := postProtocol(t, s, token, []string{"2025-11-25"}, over)
			page, err := s.store.List(t.Context(), sha256.Sum256([]byte(token)), []byte(`{}`))
			requireNoError(t, err)
			if status != http.StatusBadRequest || entered.Load() != 0 || len(page.Artifacts) != 0 || len(wire) > 256 {
				t.Errorf("overlimit ID reached domain: status=%d admission=%d artifacts=%d reply bytes=%d", status, entered.Load(), len(page.Artifacts), len(wire))
			}
		})
	}
}

func TestServiceLargeListWithBoundedRequestID(t *testing.T) {
	s, token := serviceFixture(t)
	hash := sha256.Sum256([]byte(token))
	seed, err := json.Marshal(PublishRequest{MutationID: "seed", Title: "x", Summary: "s", HTML: "ok"})
	requireNoError(t, err)
	first, err := s.store.Publish(t.Context(), hash, seed, PublicationOrigin{})
	requireNoError(t, err)
	opened, err := s.store.Open(t.Context(), hash, readJSON(first.ArtifactID))
	requireNoError(t, err)
	metadata, err := json.Marshal(opened.ArtifactMetadata)
	requireNoError(t, err)
	// Sixteen ASCII entries fill the real response budget closely. Derive fixed
	// metadata overhead from the Store result, rather than fabricating DB rows.
	title := strings.Repeat("x", ((16<<20)-4096-512)/16-1-(len(metadata)-1))
	want := map[string]string{first.ArtifactID: "x"}
	for i := range 17 {
		raw, err := json.Marshal(PublishRequest{MutationID: fmt.Sprintf("large-%d", i), Title: title, Summary: "s", HTML: "ok"})
		requireNoError(t, err)
		if len(raw) >= MaxRequestBytes {
			t.Fatal("seed exceeds publication request limit")
		}
		created, err := s.store.Publish(t.Context(), hash, raw, PublicationOrigin{})
		requireNoError(t, err)
		want[created.ArtifactID] = title
	}
	largeID := strings.Repeat("L", 1<<20)
	status, _ := postProtocol(t, s, token, []string{"2025-11-25"}, toolMessageWithID(t, largeID, "artifact_list", ListRequest{Limit: 100}))
	if status != http.StatusBadRequest {
		t.Errorf("oversized ID with near-limit page: HTTP %d, want size rejection before dispatch", status)
	}
	id := sizedProtocolID(t, "boundary", 1024)
	cursor := ""
	pages := 0
	for {
		status, wire := postProtocol(t, s, token, []string{"2025-11-25"}, toolMessageWithID(t, id, "artifact_list", ListRequest{Limit: 100, Cursor: cursor}))
		if status != http.StatusOK || len(wire) > MaxResponseBytes {
			t.Fatalf("bounded ID response: status=%d bytes=%d", status, len(wire))
		}
		page := listResponseWithID(t, wire, id)
		if pages == 0 {
			if len(wire) < MaxResponseBytes-8192 || page.NextCursor == "" {
				t.Fatalf("fixture not near response boundary: bytes=%d has cursor=%v", len(wire), page.NextCursor != "")
			}
			t.Logf("actual first-page response bytes=%d; SDK ID-only envelope bytes=1024", len(wire))
		}
		for _, item := range page.Artifacts {
			if expected, ok := want[item.ArtifactID]; !ok || item.Title != expected {
				t.Fatal("lost, duplicated or truncated metadata")
			}
			delete(want, item.ArtifactID)
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(want) != 0 || pages != 2 {
		t.Fatalf("pagination missing=%d pages=%d", len(want), pages)
	}
}

func TestServiceRequestIDLimitKeepsNumericAndNotificationHandling(t *testing.T) {
	s, token := serviceFixture(t)
	status, wire := postProtocol(t, s, token, []string{"2025-11-25"}, toolMessageWithID(t, float64(7), "artifact_list", ListRequest{}))
	if status != http.StatusOK {
		t.Fatalf("numeric request ID: HTTP %d", status)
	}
	listResponseWithID(t, wire, int64(7))
	body, err := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "notifications/initialized", Params: json.RawMessage(`{}`)})
	requireNoError(t, err)
	status, _ = postProtocol(t, s, token, []string{"2025-11-25"}, body)
	if status != http.StatusAccepted {
		t.Fatalf("notification: HTTP %d", status)
	}
}
