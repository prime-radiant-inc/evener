package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// Manager manages connections to external MCP servers.
type Manager struct {
	conns []conn
}

type conn struct {
	name      string
	session   *mcpsdk.ClientSession
	tools     []llm.ToolDefinition // namespaced: servername__toolname
	origNames map[string]string    // sanitized namespaced name → original MCP tool name
}

// NewManager connects to all configured MCP servers, discovers their tools,
// and namespaces them. The transports parameter is optional: when nil, transports
// are created from configs. When provided (for testing), each transport[i]
// corresponds to configs[i].
func NewManager(ctx context.Context, configs []mcpconfig.ServerConfig, transports []mcpsdk.Transport) (*Manager, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "serf",
		Version: "v1",
	}, nil)

	mgr := &Manager{}
	for i, cfg := range configs {
		var transport mcpsdk.Transport
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

		mgr.conns = append(mgr.conns, conn{
			name:      cfg.Name,
			session:   session,
			tools:     tools,
			origNames: origNames,
		})
	}

	return mgr, nil
}

// ToolDefinitions returns all namespaced tool definitions from all MCP servers.
func (m *Manager) ToolDefinitions() []llm.ToolDefinition {
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
// given tool.Registry. Returns an error if any namespaced tool name collides
// with an existing registration or fails validation.
func (m *Manager) RegisterTools(reg *tool.Registry) error {
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
					result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
						Name:      origName,
						Arguments: args,
					})
					if err != nil {
						return nil, err
					}
					body := mcpResultToString(result)
					if result != nil && result.IsError {
						// Channel B: the server reported a tool-level error (e.g. an
						// upstream 4xx). Return it through the error path so the tool
						// result reaches the model as an error-typed tool_result and
						// renders red, instead of a green success carrying the error text.
						return body, errors.New("MCP tool reported an error")
					}
					return body, nil
				},
			}); err != nil {
				return fmt.Errorf("registering MCP tool %q: %w", td.Name, err)
			}
		}
	}
	return nil
}

// Servers returns per-server info including name and namespaced tool names.
func (m *Manager) Servers() []mcpconfig.ServerInfo {
	if m == nil {
		return nil
	}
	out := make([]mcpconfig.ServerInfo, len(m.conns))
	for i, c := range m.conns {
		tools := make([]string, len(c.tools))
		for j, td := range c.tools {
			tools[j] = td.Name
		}
		out[i] = mcpconfig.ServerInfo{Name: c.name, Tools: tools}
	}
	return out
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
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
func mcpResultToString(result *mcpsdk.CallToolResult) string {
	if result == nil {
		return ""
	}

	var parts []string
	for _, c := range result.Content {
		switch ct := c.(type) {
		case *mcpsdk.TextContent:
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

// commandTerminateDuration bounds how long a stdio MCP transport's Close waits
// for the server to exit (after closing its stdin) before escalating to SIGTERM.
// Zero means the SDK default (5s). It is the dominant cost of tearing down a
// stdio server that does not exit promptly on stdin-EOF, so tests shrink it to
// avoid paying ~5s per server on cleanup.
var commandTerminateDuration time.Duration

// transportForConfig creates the appropriate MCP transport for a config.
func transportForConfig(cfg mcpconfig.ServerConfig) (mcpsdk.Transport, error) {
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
		return &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: commandTerminateDuration}, nil

	case "sse":
		if cfg.URL == "" {
			return nil, errors.New("sse transport requires a url")
		}
		t := &mcpsdk.SSEClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	case "http":
		if cfg.URL == "" {
			return nil, errors.New("http transport requires a url")
		}
		t := &mcpsdk.StreamableClientTransport{Endpoint: cfg.URL}
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
