// Command intgmcpserver is a minimal stdio MCP server used by the agent
// package's initMCP integration tests. It advertises a single "echo" tool that
// returns "echo: <message>", speaks MCP over stdin/stdout, and exits when its
// stdin is closed (so a test's Manager.Close tears it down promptly).
//
// It lives under testdata so the normal `go build ./...` skips it; the tests
// compile it explicitly with `go build` and run the resulting binary as a real
// subprocess.
//
// An optional argv[1] names an exit-marker file: when set, it is written
// right before the process exits, letting a test confirm this subprocess
// actually terminated (in response to Manager.Close closing its stdin)
// instead of being orphaned.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

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

	runErr := server.Run(context.Background(), &mcpsdk.StdioTransport{})
	if len(os.Args) > 1 {
		_ = os.WriteFile(os.Args[1], []byte("exited\n"), 0644)
	}
	if runErr != nil {
		log.Fatalf("intgmcpserver: %v", runErr)
	}
}
