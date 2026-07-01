// Command intgmcpserver is a minimal stdio MCP server used by the agent
// package's initMCP integration tests. It advertises a single "echo" tool that
// returns "echo: <message>", speaks MCP over stdin/stdout, and exits when its
// stdin is closed (so a test's Manager.Close tears it down promptly).
//
// It lives under testdata so the normal `go build ./...` skips it; the tests
// compile it explicitly with `go build` and run the resulting binary as a real
// subprocess.
package main

import (
	"context"
	"encoding/json"
	"log"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "intgmcpserver",
		Version: "v0.0.1",
	}, nil)

	server.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "Echoes the input message back to the caller",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "The message to echo",
				},
			},
			"required": []string{"message"},
		},
	}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + args.Message}},
		}, nil
	})

	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		log.Fatalf("intgmcpserver: %v", err)
	}
}
