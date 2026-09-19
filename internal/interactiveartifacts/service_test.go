package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
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
	if init.ProtocolVersion != "2025-11-25" || init.Capabilities.Tools == nil || init.Capabilities.Resources == nil {
		t.Fatalf("missing selected profile or required capabilities: %+v", init)
	}
	capabilities := init.Capabilities
	if capabilities.Logging != nil || capabilities.Completions != nil || capabilities.Prompts != nil || len(capabilities.Experimental) != 0 || len(capabilities.Extensions) != 0 || capabilities.Resources.Subscribe || capabilities.Resources.ListChanged || capabilities.Tools.ListChanged {
		t.Fatalf("unsupported capabilities advertised: %+v", capabilities)
	}
	list, err := c.ListTools(context.Background(), nil)
	requireNoError(t, err)
	catalog, err := Tools()
	requireNoError(t, err)
	if len(list.Tools) != len(catalog) {
		t.Fatalf("catalog count: live=%d bundled=%d", len(list.Tools), len(catalog))
	}
	expected := make(map[string]*mcp.Tool, len(catalog))
	for _, tool := range catalog {
		expected[tool.Name] = tool
	}
	for _, tool := range list.Tools {
		bundled, ok := expected[tool.Name]
		if !ok {
			t.Fatalf("unexpected or duplicate live tool %q", tool.Name)
		}
		for name, pair := range map[string][2]any{"inputSchema": {tool.InputSchema, bundled.InputSchema}, "outputSchema": {tool.OutputSchema, bundled.OutputSchema}, "ui": {tool.Meta["ui"], bundled.Meta["ui"]}} {
			if !reflect.DeepEqual(jsonValue(t, pair[0]), jsonValue(t, pair[1])) {
				t.Fatalf("live tool %s changed %s contract", tool.Name, name)
			}
		}
		delete(expected, tool.Name)
	}
	resources, err := c.ListResources(context.Background(), nil)
	requireNoError(t, err)
	if len(resources.Resources) != 1 || resources.Resources[0].URI != ViewerResource.URI || resources.Resources[0].MIMEType != ViewerResource.MIMEType {
		t.Fatalf("unexpected resource catalog: %+v", resources.Resources)
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

// jsonValue compares machine fields after the same ordinary JSON type mapping
// used by the SDK, without comparing natural-language tool descriptions.
func jsonValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	requireNoError(t, err)
	var decoded any
	requireNoError(t, json.Unmarshal(encoded, &decoded))
	return decoded
}
