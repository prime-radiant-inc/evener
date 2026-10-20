package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FuzzEchoProtocol drives the real registered echo handler through MCP decoding
// and schema validation, not a copy of its JSON parser. Arbitrary messages must
// survive a tool call unchanged, including after an invalid call. Adding JSON
// whitespace must preserve the result. Wrong argument shapes must never succeed.
// In-memory transports keep this hermetic: no subprocesses, sockets, or models.
func FuzzEchoProtocol(f *testing.F) {
	for _, message := range []string{"", "hello", "\"\\\n\t\x00", "雪🌱", "\xff\xfe", "{\"message\":null}"} {
		f.Add(message)
	}
	f.Fuzz(func(t *testing.T, message string) {
		// Bound allocation and transport work per iteration. Convert invalid UTF-8
		// to replacement runes before encoding, as required by JSON strings.
		if len(message) > 64<<10 {
			t.Skip()
		}
		message = string([]rune(message))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		st, ct := mcp.NewInMemoryTransports()
		server, err := newServer().Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		client, err := mcp.NewClient(&mcp.Implementation{Name: "echo-fuzz", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()

		assertEcho := func(arguments any) {
			t.Helper()
			result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError || len(result.Content) != 1 {
				t.Fatalf("echo failed or returned unexpected content: %+v", result)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok || text.Text != message {
				t.Fatalf("echo changed message: got %+v, want %q", result.Content, message)
			}
		}
		assertEcho(map[string]any{"message": message})
		for _, invalid := range []any{
			map[string]any{},
			map[string]any{"message": nil},
			map[string]any{"message": 42},
			map[string]any{"message": true},
			map[string]any{"message": []string{message}},
			map[string]any{"message": map[string]string{"nested": message}},
		} {
			result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: invalid})
			if err == nil && (result == nil || !result.IsError) {
				t.Fatalf("invalid arguments succeeded: %#v, result: %+v", invalid, result)
			}
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		// This successful call also proves rejected requests did not poison the
		// session. Its expected value comes from fuzz input, not decoded output.
		assertEcho(json.RawMessage(" \n { \"message\" : " + string(encoded) + " } \t"))
	})
}
