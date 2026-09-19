package interactiveartifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type compactSDKTransport struct {
	base        http.RoundTripper
	maxResponse int
	maxRequest  int
}

func (t *compactSDKTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		var value any
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		err := decoder.Decode(&value)
		_ = r.Body.Close()
		if err != nil {
			return nil, err
		}
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(&buffer)
		r.ContentLength = int64(buffer.Len())
		t.maxRequest = max(t.maxRequest, buffer.Len())
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	t.maxResponse = max(t.maxResponse, len(data))
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}
func TestServiceSDKLargeEscapedReadViewAndList(t *testing.T) {
	s, _ := serviceFixture(t)
	scope := testScope()
	scope.NamespaceID = strings.Repeat("<é\x00", 20000)
	requireNoError(t, s.store.EnsureNamespace(t.Context(), scope.NamespaceID, scope.RealmID, "owner"))
	token := "large-namespace-token"
	requireNoError(t, s.store.InstallGrant(t.Context(), sha256.Sum256([]byte(token)), scope))
	client := NewHTTPClient(token)
	wire := &compactSDKTransport{base: client.Transport}
	client.Transport = wire
	ctx := context.Background()
	c, err := mcp.NewClient(&mcp.Implementation{Name: "large", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.ready.Endpoint, HTTPClient: client}, nil)
	requireNoError(t, err)
	defer c.Close()
	title := strings.Repeat("<", (1<<20)-1024)
	source := strings.Repeat("<", (1<<20)-1024)
	created, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_publish", Arguments: PublishRequest{MutationID: "large", Title: title, Summary: "s", HTML: source}})
	requireNoError(t, err)
	data, _ := json.Marshal(created.StructuredContent)
	var receipt MutationReceipt
	requireNoError(t, json.Unmarshal(data, &receipt))
	if receipt.ArtifactID == "" {
		t.Fatalf("missing receipt: %s", data)
	}
	state := json.RawMessage(`{"v":"` + strings.Repeat("x", (256<<10)-16) + `"}`)
	saved, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_save_state", Arguments: SaveStateRequest{ArtifactID: receipt.ArtifactID, MutationID: "state", ExpectedSourceRevision: 1, ExpectedStateVersion: 1, State: state}})
	requireNoError(t, err)
	if saved.IsError {
		t.Fatalf("checkpoint rejected: %+v", saved)
	}
	for _, name := range []string{"artifact_read", "artifact_get_view"} {
		var args any = GetViewRequest{ArtifactID: receipt.ArtifactID}
		if name == "artifact_read" {
			args = ReadRequest{ArtifactID: receipt.ArtifactID, Include: []Include{"source", "state"}}
		}
		result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		requireNoError(t, err)
		encoded, _ := json.Marshal(result.StructuredContent)
		requireNoError(t, ValidateResult(name, encoded))
		var read ReadResult
		requireNoError(t, json.Unmarshal(encoded, &read))
		if result.IsError || read.Title != title || read.Source.HTML != source || string(read.State) != string(state) {
			t.Fatal("large response truncated or changed")
		}
	}
	for i := range 4 {
		result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_publish", Arguments: PublishRequest{MutationID: fmt.Sprintf("list-%d", i), Title: title, Summary: "s", HTML: "ok"}})
		requireNoError(t, err)
		if result.IsError {
			t.Fatal("publication rejected")
		}
	}
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	for {
		result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_list", Arguments: ListRequest{Limit: 100, Cursor: cursor}})
		requireNoError(t, err)
		data, _ := json.Marshal(result.StructuredContent)
		var page ListResult
		requireNoError(t, json.Unmarshal(data, &page))
		if result.IsError || len(page.Artifacts) == 0 {
			t.Fatal("empty oversized page")
		}
		for _, item := range page.Artifacts {
			if seen[item.ArtifactID] || item.Title != title {
				t.Fatal("pagination lost whole entry")
			}
			seen[item.ArtifactID] = true
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		if len(page.NextCursor) > 256 {
			t.Fatalf("cursor bytes=%d", len(page.NextCursor))
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 || pages < 2 || wire.maxResponse <= 2<<20 || wire.maxResponse > 16<<20 {
		t.Fatalf("size evidence entries=%d pages=%d response=%d", len(seen), pages, wire.maxResponse)
	}
	if wire.maxRequest > MaxRequestBytes || wire.maxResponse > MaxResponseBytes {
		t.Fatalf("wire budgets: request=%d response=%d", wire.maxRequest, wire.maxResponse)
	}
	t.Logf("actual SDK largest encoded HTTP response=%d bytes; 5 whole entries across %d pages", wire.maxResponse, pages)
}
