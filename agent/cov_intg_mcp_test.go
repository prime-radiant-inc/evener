//go:build !short

package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// These tests drive Session.initMCP against a REAL stdio MCP server subprocess
// (agent/testdata/intgmcpserver), compiled in-test. They exercise the initMCP
// arms that only run when a server is actually configured: connect via
// mcp.NewManager, register the discovered tools into the session registry, and
// merge plugin-provided MCP configs. The server exits on stdin EOF, so the
// session's Close tears it down deterministically without a terminate timeout.

// intg_buildMCPServer compiles the testdata stdio MCP server into a fresh temp
// dir and returns the binary path. The Go build cache makes repeated builds
// (e.g. under -count) cheap, and the path has no spaces so it survives the
// whitespace-split of an inline MCP spec.
func intg_buildMCPServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "intgmcpserver")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/intgmcpserver")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building test MCP server: %v\n%s", err, out)
	}
	return bin
}

// intg_mcpEcho drives the named registered tool with a message and returns its
// output, failing the test on any error.
func intg_mcpEcho(t *testing.T, sess *Session, toolName, message string) string {
	t.Helper()
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "call_mcp",
		Name:      toolName,
		Arguments: json.RawMessage(`{"message":"` + message + `"}`),
	})
	if res.IsError {
		t.Fatalf("MCP tool %q errored: %s", toolName, res.Output)
	}
	return res.Output
}

func TestIntg_InitMCP_InlineServer(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"intgsvc:" + bin}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.mcpMgr == nil {
		t.Fatal("mcpMgr is nil after initMCP with a configured server")
	}
	// The discovered tool must appear both in the MCP tool-definition list and
	// the executable registry, namespaced by server name.
	const want = "intgsvc__echo"
	if !intg_hasToolDef(sess.mcpTools, want) {
		t.Errorf("mcpTools missing %q; got %v", want, intg_toolDefNames(sess.mcpTools))
	}
	if sess.reg.Get(want) == nil {
		t.Fatalf("registry missing MCP tool %q", want)
	}
	if out := intg_mcpEcho(t, sess, want, "inline-hello"); out != "echo: inline-hello" {
		t.Errorf("tool output = %q, want %q", out, "echo: inline-hello")
	}
}

func TestIntg_InitMCP_PluginProvidedServerMerges(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	// A plugin whose .mcp.json contributes an MCP server exercises the
	// plugin-config merge layer of initMCP (pluginMCPConfigs merged under the
	// discovered configs) in addition to the connect+register path.
	dir := makePluginDir(t, "mcpplug")
	mcpJSON, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"svc": map[string]any{"command": bin},
		},
	})
	if err != nil {
		t.Fatalf("marshal .mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), mcpJSON, 0644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Plugin MCP servers are namespaced "plugin_<plugin>_<server>".
	const want = "plugin_mcpplug_svc__echo"
	if sess.mcpMgr == nil {
		t.Fatal("mcpMgr is nil after initMCP with a plugin-provided server")
	}
	if sess.reg.Get(want) == nil {
		t.Fatalf("registry missing plugin MCP tool %q; have MCP tools %v", want, intg_toolDefNames(sess.mcpTools))
	}
	if out := intg_mcpEcho(t, sess, want, "plugin-hello"); out != "echo: plugin-hello" {
		t.Errorf("tool output = %q, want %q", out, "echo: plugin-hello")
	}
}

func TestIntg_InitMCP_RegisterToolsError(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	// A 60-char server name pushes the namespaced tool name ("<name>__echo")
	// past the 64-char provider limit, so RegisterTools fails validation after a
	// successful connect+discover — surfacing as a NewSession error.
	longName := strings.Repeat("a", 60)
	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{longName + ":" + bin}}
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		t.Fatal("expected NewSession to fail when an MCP tool name exceeds the length limit")
	}
	if !strings.Contains(err.Error(), "MCP") {
		t.Errorf("error %q does not mention MCP", err.Error())
	}
}

func TestIntg_InitMCP_ConnectError(t *testing.T) {
	t.Parallel()
	// `true` exits immediately without speaking MCP, so its stdout closes before
	// the initialize handshake completes: mcp.NewManager's Connect fails and
	// initMCP returns that error, which NewSession reports.
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found: %v", err)
	}
	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"deadsvc:" + truePath}}
	_, err = NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		t.Fatal("expected NewSession to fail when the MCP server does not speak the protocol")
	}
	if !strings.Contains(err.Error(), "MCP") {
		t.Errorf("error %q does not mention MCP", err.Error())
	}
}

func TestIntg_InitMCP_DiscoverError(t *testing.T) {
	t.Parallel()
	// A malformed inline spec (no colon) fails mcpconfig.Discover, so initMCP
	// returns before spawning anything and NewSession reports the error.
	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"missing-colon-spec"}}
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		t.Fatal("expected NewSession to fail on a malformed inline MCP spec")
	}
	if !strings.Contains(err.Error(), "MCP") {
		t.Errorf("error %q does not mention MCP", err.Error())
	}
}

func intg_hasToolDef(defs []llm.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func intg_toolDefNames(defs []llm.ToolDefinition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}
