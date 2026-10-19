package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEchoStdioProtocol(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "echo-server")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.Command(binary)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "XDG_CONFIG_HOME=" + root, "XDG_STATE_HOME=" + root, "XDG_CACHE_HOME=" + root, "XDG_DATA_HOME=" + root, "LANG=C.UTF-8"}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "stdio-check", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: time.Second}, nil)
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
	body, err := json.Marshal(tools.Tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type       string
		Properties map[string]struct{ Type string }
		Required   []string
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.Properties["message"].Type != "string" || len(schema.Required) != 1 || schema.Required[0] != "message" {
		t.Fatalf("schema: %s", body)
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": nonce}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result: %+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != nonce {
		t.Fatalf("nonce did not round trip: %+v", result)
	}
	// CommandTransport owns Wait, and Close waits for it before returning.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if command.ProcessState == nil || !command.ProcessState.Exited() || !command.ProcessState.Success() {
		t.Fatalf("server did not exit cleanly: %v", command.ProcessState)
	}
}
