package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArguments struct {
	Message string `json:"message"`
}

func newServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "evener-example", Version: "1.0.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name: "echo", Description: "Return the supplied message without side effects.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}, "required": []string{"message"}},
	}, func(_ context.Context, _ *mcp.CallToolRequest, args echoArguments) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Message}}}, nil, nil
	})
	return s
}

func main() {
	if err := newServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
