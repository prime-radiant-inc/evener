package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// mcpManager manages connections to external MCP servers.
type mcpManager struct {
	conns []mcpConn
}

type mcpConn struct {
	name      string
	session   *mcp.ClientSession
	tools     []llm.ToolDefinition // namespaced: servername__toolname
	origNames map[string]string    // sanitized namespaced name → original MCP tool name
}

// newMCPManager connects to all configured MCP servers, discovers their tools,
// and namespaces them. The transports parameter is optional: when nil, transports
// are created from configs. When provided (for testing), each transport[i]
// corresponds to configs[i].
func newMCPManager(ctx context.Context, configs []MCPServerConfig, transports []mcp.Transport) (*mcpManager, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "serf",
		Version: "v1",
	}, nil)

	mgr := &mcpManager{}
	for i, cfg := range configs {
		var transport mcp.Transport
		if transports != nil && i < len(transports) {
			transport = transports[i]
		} else {
			var err error
			transport, err = transportForConfig(cfg)
			if err != nil {
				mgr.Close()
				return nil, fmt.Errorf("MCP server %q: %w", cfg.Name, err)
			}
		}

		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			mgr.Close()
			return nil, fmt.Errorf("MCP server %q connect: %w", cfg.Name, err)
		}

		// Discover tools from the server.
		result, err := session.ListTools(ctx, nil)
		if err != nil {
			_ = session.Close()
			mgr.Close()
			return nil, fmt.Errorf("MCP server %q list tools: %w", cfg.Name, err)
		}

		var tools []llm.ToolDefinition
		origNames := make(map[string]string, len(result.Tools))
		for _, t := range result.Tools {
			namespacedName := sanitizeToolName(cfg.Name + "__" + t.Name)
			origNames[namespacedName] = t.Name
			params := mcpSchemaToParams(t.InputSchema)
			tools = append(tools, llm.ToolDefinition{
				Name:        namespacedName,
				Description: t.Description,
				Parameters:  params,
			})
		}

		mgr.conns = append(mgr.conns, mcpConn{
			name:      cfg.Name,
			session:   session,
			tools:     tools,
			origNames: origNames,
		})
	}

	return mgr, nil
}

// ToolDefinitions returns all namespaced tool definitions from all MCP servers.
func (m *mcpManager) ToolDefinitions() []llm.ToolDefinition {
	if m == nil {
		return nil
	}
	var defs []llm.ToolDefinition
	for _, c := range m.conns {
		defs = append(defs, c.tools...)
	}
	return defs
}

// RegisterTools registers execution closures for all MCP tools into the
// given toolRegistry. Returns an error if any namespaced tool name collides
// with an existing registration or fails validation.
func (m *mcpManager) RegisterTools(reg *tool.Registry) error {
	if m == nil {
		return nil
	}
	for _, conn := range m.conns {
		for _, td := range conn.tools {
			// Validate the namespaced name (length, charset).
			if err := llm.ValidateToolName(td.Name); err != nil {
				return fmt.Errorf("MCP tool %q: %w", td.Name, err)
			}

			// Check for collision with existing tools. This is safe because
			// RegisterTools is called only during single-threaded session init,
			// after registerCoreTools has already completed.
			if reg.Get(td.Name) != nil {
				return fmt.Errorf("MCP tool %q collides with existing tool", td.Name)
			}

			// Look up the original MCP tool name for CallTool.
			origName := conn.origNames[td.Name]
			sess := conn.session

			if err := reg.Register(tool.RegisteredTool{
				Tool: llm.Tool{Definition: td},
				Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
					result, err := sess.CallTool(ctx, &mcp.CallToolParams{
						Name:      origName,
						Arguments: args,
					})
					if err != nil {
						return nil, err
					}
					return mcpResultToString(result), nil
				},
			}); err != nil {
				return fmt.Errorf("registering MCP tool %q: %w", td.Name, err)
			}
		}
	}
	return nil
}

// Servers returns per-server info including name and namespaced tool names.
func (m *mcpManager) Servers() []MCPServerInfo {
	if m == nil {
		return nil
	}
	out := make([]MCPServerInfo, len(m.conns))
	for i, c := range m.conns {
		tools := make([]string, len(c.tools))
		for j, td := range c.tools {
			tools[j] = td.Name
		}
		out[i] = MCPServerInfo{Name: c.name, Tools: tools}
	}
	return out
}

// Close shuts down all MCP server connections.
func (m *mcpManager) Close() {
	if m == nil {
		return
	}
	for _, c := range m.conns {
		if c.session != nil {
			_ = c.session.Close()
		}
	}
}

// sanitizeToolName replaces characters invalid in LLM tool names (e.g. hyphens)
// with underscores. MCP servers often use hyphens in tool names, but LLM
// providers only accept [a-zA-Z0-9_].
func sanitizeToolName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// mcpSchemaToParams converts an MCP tool's InputSchema (any) to our
// map[string]any format used by llm.ToolDefinition.Parameters.
func mcpSchemaToParams(schema any) map[string]any {
	emptySchema := func() map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if schema == nil {
		return emptySchema()
	}
	// The MCP SDK returns InputSchema as a map[string]any from JSON unmarshaling.
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	// Fallback: re-marshal and unmarshal.
	b, err := json.Marshal(schema)
	if err != nil {
		return emptySchema()
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return emptySchema()
	}
	return m
}

// mcpResultToString converts an MCP CallToolResult to a string suitable
// for returning as a tool result.
func mcpResultToString(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}

	var parts []string
	for _, c := range result.Content {
		switch ct := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, ct.Text)
		default:
			// For non-text content, marshal to JSON as best-effort.
			b, err := json.Marshal(c)
			if err != nil {
				parts = append(parts, fmt.Sprintf("[%T]", c))
			} else {
				parts = append(parts, string(b))
			}
		}
	}

	if result.IsError {
		return "[MCP Error] " + strings.Join(parts, "\n")
	}
	return strings.Join(parts, "\n")
}

// transportForConfig creates the appropriate MCP transport for a config.
func transportForConfig(cfg MCPServerConfig) (mcp.Transport, error) {
	switch cfg.Type {
	case "stdio", "":
		if cfg.Command == "" {
			return nil, errors.New("stdio transport requires a command")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...) //nolint:noctx // MCP SDK's CommandTransport.Connect(ctx) owns the server process lifecycle
		// Merge process env with configured env vars.
		if len(cfg.Env) > 0 {
			cmd.Env = mergeEnv(cfg.Env)
		}
		return &mcp.CommandTransport{Command: cmd}, nil

	case "sse":
		if cfg.URL == "" {
			return nil, errors.New("sse transport requires a url")
		}
		t := &mcp.SSEClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	case "http":
		if cfg.URL == "" {
			return nil, errors.New("http transport requires a url")
		}
		t := &mcp.StreamableClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	default:
		return nil, fmt.Errorf("unknown MCP transport type %q", cfg.Type)
	}
}

// mergeEnv creates a combined environment from os.Environ() with extra vars
// merged in. Existing keys are replaced rather than duplicated.
func mergeEnv(extra map[string]string) []string {
	env := os.Environ()

	// Build a set of extra keys for quick lookup.
	overrides := make(map[string]bool, len(extra))
	for k := range extra {
		overrides[k] = true
	}

	// Filter out existing entries that will be overridden.
	filtered := make([]string, 0, len(env)+len(extra))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if !overrides[key] {
			filtered = append(filtered, e)
		}
	}

	// Append the overrides.
	for k, v := range extra {
		filtered = append(filtered, k+"="+v)
	}
	return filtered
}

// headerRoundTripper wraps an http.RoundTripper to inject headers into requests.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// httpClientWithHeaders returns an *http.Client that injects the given headers.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	return &http.Client{
		Transport: &headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}
