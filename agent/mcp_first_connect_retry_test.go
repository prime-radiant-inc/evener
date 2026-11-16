//go:build !short

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// TestIntg_FailedFirstConnectRecoversWithinSession drives a real Session whose
// configured MCP server fails its FIRST start and serves normally afterwards:
// the failed server registers no tool at startup, the session redials it before
// its next model request, and the recovered tool is advertised on that request
// and callable through the session's registry — with no new session.
func TestIntg_FailedFirstConnectRecoversWithinSession(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)
	marker := filepath.Join(t.TempDir(), "started.marker")
	const toolName = "intgsvc__echo"

	var firstRequestTools []string
	var echoReply string
	requests := 0
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
		requests++
		if requests == 1 {
			for _, td := range req.Tools {
				firstRequestTools = append(firstRequestTools, td.Name)
			}
			if !hasString(firstRequestTools, toolName) {
				return agenttest.FinalResponse("tool missing")
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "call_mcp_echo",
				Name:      toolName,
				Arguments: json.RawMessage(`{"message":"recovered"}`),
			})
		}
		for _, msg := range req.Messages {
			for _, p := range msg.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				if text, ok := p.ToolResult.Content.(string); ok {
					echoReply = text
				}
			}
		}
		return agenttest.FinalResponse("done")
	}})

	// --fail-first makes the server's first start record the marker and exit
	// without speaking MCP, so the session's initial connect fails; every later
	// start serves the echo tool.
	cfg := SessionConfig{MCPInline: []string{"intgsvc:" + bin + " " + marker + " --fail-first"}}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// The server did fail its first start, and the session recorded no tool for
	// it.
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("the fixture server's first start did not run: %v", statErr)
	}
	if intg_hasToolDef(sess.mcpTools, toolName) || sess.reg.Get(toolName) != nil {
		t.Fatalf("server registered a tool although its first connect failed: mcpTools=%v", intg_toolDefNames(sess.mcpTools))
	}

	// TRIPWIRE: a scripted in-process adapter plus a real stdio MCP subprocess;
	// only a genuine hang reaches this.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "echo the message", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if !hasString(firstRequestTools, toolName) {
		t.Fatalf("the recovered tool was not advertised on the turn's first request; tools were %v", firstRequestTools)
	}
	if echoReply != "echo: recovered" {
		t.Fatalf("recovered tool result = %q, want %q", echoReply, "echo: recovered")
	}
}

// TestIntg_FailedFirstConnectRecoveryWarns drives the same recovery and asserts
// the session announces it on its event stream: a server that comes back after
// a failed first connect must not do so silently.
func TestIntg_FailedFirstConnectRecoveryWarns(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)
	marker := filepath.Join(t.TempDir(), "started.marker")

	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	}})
	cfg := SessionConfig{MCPInline: []string{"intgsvc:" + bin + " " + marker + " --fail-first"}}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter plus a real stdio MCP subprocess;
	// only a genuine hang reaches this.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "say done", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	var notices []events.WarningData
	for ev := range sess.Events() {
		w, ok := ev.Data.(events.WarningData)
		if !ok || w.Source != "mcp" || w.Code != events.WarningCodeMCPReconnected {
			continue
		}
		notices = append(notices, w)
	}
	if len(notices) != 1 {
		t.Fatalf("MCP recovery notices = %d, want 1", len(notices))
	}
	if !strings.Contains(notices[0].Message, "intgsvc") {
		t.Fatalf("recovery notice %q does not name the server", notices[0].Message)
	}
}
