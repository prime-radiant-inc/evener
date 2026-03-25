package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/llm"
)

// TestMCPIntegration_ToolCallThroughSession verifies the full flow:
// in-process MCP server -> MCPManager -> Session tool registry -> fakeAdapter
// calls the MCP tool -> result flows back through the session.
func TestMCPIntegration_ToolCallThroughSession(t *testing.T) {
	// Create a minimal MCP server with a "greet" tool.
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "v0.0.1",
	}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "greet",
		Description: "Greets someone by name",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Name to greet",
				},
			},
			"required": []string{"name"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		name := args["name"].(string)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Hello, " + name + "!"}},
		}, nil
	})

	// Set up in-memory transport.
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	_, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	// Create MCPManager directly with the transport (bypassing config discovery).
	mgr, err := NewMCPManager(ctx, []MCPServerConfig{
		{Name: "ext", Type: "stdio"},
	}, []mcp.Transport{ct})
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}

	// Create a fakeAdapter that calls the ext__greet tool, then returns a final text response.
	callID := "call_mcp_greet"
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Step 1: LLM decides to call the MCP tool.
			func(req llm.Request) llm.Response {
				// Verify MCP tool is in the tool list.
				found := false
				for _, td := range req.Tools {
					if td.Name == "ext__greet" {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("ext__greet not found in request tools: %v", req.Tools)
				}

				// MCP tools appear in the API tools parameter, not the system prompt.

				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        callID,
								Name:      "ext__greet",
								Arguments: json.RawMessage(`{"name":"World"}`),
							}},
						},
					},
				}
			},
			// Step 2: After tool result, LLM returns final text.
			func(req llm.Request) llm.Response {
				// Find the tool result in the conversation.
				for _, msg := range req.Messages {
					if msg.Role == llm.RoleTool {
						for _, p := range msg.Content {
							if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
								text, ok := p.ToolResult.Content.(string)
								if ok && strings.Contains(text, "Hello, World!") {
									return llm.Response{Message: llm.Assistant("The server said: Hello, World!")}
								}
							}
						}
					}
				}
				return llm.Response{Message: llm.Assistant("Tool result not found")}
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Manually inject the MCPManager into the session (since we don't go through config discovery).
	if err := mgr.RegisterTools(sess.reg); err != nil {
		t.Fatal(err)
	}
	sess.mcpMgr = mgr
	sess.mcpTools = mgr.ToolDefinitions()
	// Rebuild caches to pick up the MCP tools added after session creation.
	sess.rebuildToolDefsCache()
	sess.rebuildPromptCache()

	tctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := sess.ProcessInput(tctx, "Greet the world using the MCP tool")
	sess.Close()

	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(result, "Hello, World!") {
		t.Errorf("result = %q, expected it to contain 'Hello, World!'", result)
	}

	// Verify the adapter received 2 requests (tool call + final response).
	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(reqs))
	}

	// Verify ext__greet was in the tools list of both requests.
	for i, req := range reqs {
		found := false
		for _, td := range req.Tools {
			if td.Name == "ext__greet" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("request %d missing ext__greet in tools", i)
		}
	}
}
