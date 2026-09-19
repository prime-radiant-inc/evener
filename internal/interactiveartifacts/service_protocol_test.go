package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func protocolServiceFixture(t *testing.T) (*service, string, *atomic.Int64) {
	t.Helper()
	entered := new(atomic.Int64)
	s, err := startService(filepath.Join(t.TempDir(), "private"), StoreOptions{Clock: fixedClock, hooks: storeHooks{beforeAdmission: func() { entered.Add(1) }}})
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, s.close(context.Background())) })
	requireNoError(t, s.store.EnsureNamespace(context.Background(), "namespace", "realm", "owner"))
	token := "protocol-fixture"
	requireNoError(t, s.store.InstallGrant(context.Background(), sha256.Sum256([]byte(token)), testScope()))
	return s, token, entered
}
func postProtocol(t *testing.T, s *service, token string, versions []string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.ready.Endpoint, bytes.NewReader(body))
	requireNoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if versions != nil {
		req.Header["Mcp-Protocol-Version"] = versions
	}
	client := NewHTTPClient(token)
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	requireNoError(t, err)
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	requireNoError(t, err)
	return response.StatusCode, encoded
}
func publishProtocolMessage(id int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"artifact_publish","arguments":%s}}`, id, createJSON(fmt.Sprintf("protocol-%d", id))))
}
func TestServiceRejectsBatchBeforeStoreDispatch(t *testing.T) {
	for _, test := range []struct {
		name     string
		versions []string
	}{
		{name: "absent"},
		{name: "legacy", versions: []string{"2025-03-26"}},
		{name: "selected", versions: []string{"2025-11-25"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, token, entered := protocolServiceFixture(t)
			requests := make([]json.RawMessage, 40)
			for i := range requests {
				requests[i] = publishProtocolMessage(i + 1)
			}
			body, err := json.Marshal(requests)
			requireNoError(t, err)
			status, _ := postProtocol(t, s, token, test.versions, body)
			page, err := s.store.List(t.Context(), sha256.Sum256([]byte(token)), []byte(`{"limit":100}`))
			requireNoError(t, err)
			if status != http.StatusBadRequest || entered.Load() != 0 || len(page.Artifacts) != 0 {
				t.Fatalf("batch escaped profile guard: status=%d Store entries=%d artifacts=%d", status, entered.Load(), len(page.Artifacts))
			}
		})
	}
}
func TestServiceSelectedProfileBeforeSingleCallDispatch(t *testing.T) {
	for _, test := range []struct {
		name     string
		versions []string
	}{
		{name: "missing"},
		{name: "legacy", versions: []string{"2025-03-26"}},
		{name: "empty", versions: []string{""}},
		{name: "duplicate", versions: []string{"2025-11-25", "2025-11-25"}},
		{name: "conflicting", versions: []string{"2025-11-25", "2025-03-26"}},
		{name: "future", versions: []string{"2026-07-28"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, token, entered := protocolServiceFixture(t)
			status, _ := postProtocol(t, s, token, test.versions, publishProtocolMessage(1))
			if status != http.StatusBadRequest || entered.Load() != 0 {
				t.Fatalf("unsupported single-call profile: status=%d Store entries=%d", status, entered.Load())
			}
		})
	}
}
func TestServiceHeaderlessInitializeSelectsOnlyQualifiedProfile(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2025-03-26"} {
		t.Run(version, func(t *testing.T) {
			s, token, _ := protocolServiceFixture(t)
			for _, headers := range [][]string{nil, {"2025-11-25"}} {
				body := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"profile-test","version":"1"}}}`, version))
				status, encoded := postProtocol(t, s, token, headers, body)
				if version != "2025-11-25" {
					if status != http.StatusBadRequest {
						t.Fatalf("negotiated legacy initialization: status=%d", status)
					}
					continue
				}
				if status != http.StatusOK {
					t.Fatalf("valid single initialize rejected: status=%d body=%s", status, encoded)
				}
				message, err := jsonrpc.DecodeMessage(encoded)
				requireNoError(t, err)
				response, ok := message.(*jsonrpc.Response)
				if !ok || response.Error != nil {
					t.Fatalf("SDK initialize response: %+v", message)
				}
				var init mcp.InitializeResult
				requireNoError(t, json.Unmarshal(response.Result, &init))
				if init.ProtocolVersion != "2025-11-25" {
					t.Fatalf("negotiated profile=%s", init.ProtocolVersion)
				}
			}
		})
	}
}

func TestServiceOmittedArgumentsUseListDefaults(t *testing.T) {
	s, token := serviceFixture(t)
	for i := range 21 {
		_, err := s.store.Publish(t.Context(), sha256.Sum256([]byte(token)), createJSON(fmt.Sprintf("default-%d", i)), PublicationOrigin{})
		requireNoError(t, err)
	}
	for _, test := range []struct {
		name, tool, arguments string
		valid                 bool
	}{
		{name: "absent", tool: "artifact_list", valid: true},
		{name: "object", tool: "artifact_list", arguments: `,"arguments":{}`, valid: true},
		{name: "null", tool: "artifact_list", arguments: `,"arguments":null`},
		{name: "required", tool: "artifact_read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q%s}}`, test.tool, test.arguments))
			status, wire := postProtocol(t, s, token, []string{"2025-11-25"}, body)
			if status != http.StatusOK {
				t.Fatalf("HTTP status=%d", status)
			}
			var response struct {
				Error  json.RawMessage `json:"error"`
				Result struct {
					IsError           bool       `json:"isError"`
					StructuredContent ListResult `json:"structuredContent"`
				} `json:"result"`
			}
			requireNoError(t, json.Unmarshal(wire, &response))
			failed := len(response.Error) != 0 || response.Result.IsError
			if failed == test.valid {
				t.Fatalf("valid=%v response=%s", test.valid, wire)
			}
			if test.valid && (len(response.Result.StructuredContent.Artifacts) != 20 || response.Result.StructuredContent.NextCursor == "") {
				t.Fatalf("missing default pagination: %+v", response.Result.StructuredContent)
			}
		})
	}
}

func TestServiceUnknownHandlerNameFailsClosed(t *testing.T) {
	s, token := serviceFixture(t)
	ctx := context.WithValue(t.Context(), callerKey{}, sha256.Sum256([]byte(token)))
	result, err := s.call(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "not-in-the-catalog", Arguments: json.RawMessage(`{}`)}})
	if err == nil || result != nil {
		t.Fatalf("unknown internal dispatch succeeded: result=%+v err=%v", result, err)
	}
}
