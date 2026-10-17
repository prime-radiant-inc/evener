package main

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEchoProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	server, err := newServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "example-check", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("tools: %+v", tools)
	}
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "extension check"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result: %+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "extension check" {
		t.Fatalf("echo result: %+v", result.Content)
	}
}
