package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serviceFixture(t *testing.T) (*service, string) {
	t.Helper()
	s, err := startService(filepath.Join(t.TempDir(), "private"), StoreOptions{Clock: fixedClock})
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, s.close(context.Background())) })
	requireNoError(t, s.store.EnsureNamespace(context.Background(), "namespace", "realm", "owner"))
	token := "private-opaque-test-token"
	requireNoError(t, s.store.InstallGrant(context.Background(), sha256.Sum256([]byte(token)), testScope()))
	return s, token
}
func sdkClient(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	httpClient := NewHTTPClient(token)
	c, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, nil)
	requireNoError(t, err)
	t.Cleanup(func() { _ = c.Close(); httpClient.CloseIdleConnections() })
	return c
}
func TestServiceSDKProfileAuthorityAndResource(t *testing.T) {
	s, token := serviceFixture(t)
	c := sdkClient(t, s.ready.Endpoint, token)
	init := c.InitializeResult()
	if init.ProtocolVersion != "2025-11-25" || init.Capabilities.Logging != nil || init.Capabilities.Completions != nil || init.Capabilities.Resources.Subscribe || init.Capabilities.Tools.ListChanged {
		t.Fatalf("wrong profile: %+v", init)
	}
	list, err := c.ListTools(context.Background(), nil)
	requireNoError(t, err)
	if len(list.Tools) != 7 {
		t.Fatalf("catalog: %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Meta["ui"] == nil {
			t.Fatal("lost UI metadata")
		}
	}
	result, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("sdk"))})
	requireNoError(t, err)
	if result.IsError {
		t.Fatalf("publish: %+v", result)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var receipt MutationReceipt
	requireNoError(t, json.Unmarshal(data, &receipt))
	if receipt.ArtifactID == "" || receipt.SourceRevision != 1 {
		t.Fatalf("lost receipt: %s", data)
	}
	requireNoError(t, s.store.EnsureNamespace(context.Background(), "other-namespace", "realm", "other-owner"))
	otherScope := testScope()
	otherScope.NamespaceID = "other-namespace"
	otherScope.PrincipalID = "other-principal"
	requireNoError(t, s.store.InstallGrant(context.Background(), sha256.Sum256([]byte("other-scope")), otherScope))
	otherClient := sdkClient(t, s.ready.Endpoint, "other-scope")
	for _, id := range []string{receipt.ArtifactID, "nonexistent"} {
		result, err := otherClient.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_read", Arguments: ReadRequest{ArtifactID: id}})
		requireNoError(t, err)
		encoded, _ := json.Marshal(result.StructuredContent)
		var denied RejectedResult
		requireNoError(t, json.Unmarshal(encoded, &denied))
		if !result.IsError || denied.Error.Code != NotFoundOrForbidden || denied.Error.SourceRevision != 0 || denied.Error.StateVersion != 0 {
			t.Fatalf("cross-namespace disclosure: %s", encoded)
		}
	}
	forged, err := c.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(`{"mutationId":"forged","title":"x","summary":"x","html":"x","originatingThreadId":"forged"}`)})
	requireNoError(t, err)
	if !forged.IsError {
		t.Fatal("caller-supplied provenance accepted")
	}
	scope := testScope()
	scope.Methods = []string{"artifact_read"}
	narrow := "narrow"
	requireNoError(t, s.store.InstallGrant(context.Background(), sha256.Sum256([]byte(narrow)), scope))
	nc := sdkClient(t, s.ready.Endpoint, narrow)
	denied, err := nc.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: json.RawMessage(createJSON("forged"))})
	requireNoError(t, err)
	encoded, _ := json.Marshal(denied.StructuredContent)
	var rejection RejectedResult
	requireNoError(t, json.Unmarshal(encoded, &rejection))
	if !denied.IsError || rejection.Error.Code != NotFoundOrForbidden {
		t.Fatalf("lost domain error: %s", encoded)
	}
	if _, err = c.CallTool(context.Background(), &mcp.CallToolParams{Name: "unknown", Arguments: map[string]any{}}); err == nil {
		t.Fatal("unknown method accepted")
	}
	resource, err := c.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: ViewerResource.URI})
	requireNoError(t, err)
	if len(resource.Contents) != 1 || resource.Contents[0].MIMEType != ViewerResource.MIMEType || strings.Contains(resource.Contents[0].Text, "<script") {
		t.Fatal("unsafe interim viewer")
	}
	if _, err = c.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "file:///etc/passwd"}); err == nil {
		t.Fatal("arbitrary resource accepted")
	}
	requireNoError(t, s.store.RevokeGrant(context.Background(), sha256.Sum256([]byte(token))))
	if _, err = c.ListTools(context.Background(), nil); err == nil {
		t.Fatal("revoked grant disclosed catalog")
	}
}
func TestServiceHTTPBoundary(t *testing.T) {
	s, token := serviceFixture(t)
	for _, test := range []struct {
		name, host string
		headers    http.Header
		body       string
		want       int
	}{
		{name: "missing", want: 401},
		{name: "unknown", headers: http.Header{"Authorization": {"Bearer unknown"}}, want: 401},
		{name: "empty", headers: http.Header{"Authorization": {"Bearer "}}, want: 401},
		{name: "duplicate", headers: http.Header{"Authorization": {"Bearer " + token, "Bearer " + token}}, want: 401},
		{name: "origin empty", headers: http.Header{"Authorization": {"Bearer " + token}, "Origin": {""}}, want: 403},
		{name: "origin null", headers: http.Header{"Authorization": {"Bearer " + token}, "Origin": {"null"}}, want: 403},
		{name: "host", host: "localhost:9999", headers: http.Header{"Authorization": {"Bearer " + token}}, want: 403},
		{name: "oversize", headers: http.Header{"Authorization": {"Bearer " + token}}, body: strings.Repeat(" ", (2<<20)+1), want: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.ready.Endpoint, bytes.NewBufferString(test.body))
			requireNoError(t, err)
			req.Header = test.headers
			if test.host != "" {
				req.Host = test.host
			}
			resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
			requireNoError(t, err)
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			requireNoError(t, err)
			if resp.StatusCode != test.want || strings.Contains(string(b), token) || resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("boundary status=%d body=%s", resp.StatusCode, b)
			}
		})
	}
}
