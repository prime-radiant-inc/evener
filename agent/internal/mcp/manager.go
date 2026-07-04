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
	"sync"
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
	cfg       mcpconfig.ServerConfig
	session   *mcpsdk.ClientSession
	tools     []llm.ToolDefinition // namespaced: servername__toolname
	origNames map[string]string    // sanitized namespaced name → original MCP tool name
	status    string               // "connected" / "degraded" / "failed"
	lastErr   error
	lastErrAt time.Time
}

// ServerOutcome reports a per-server failure at one assembly stage. Stage is
// "connect" (transport build, connect handshake, or tools/list) or "register".
type ServerOutcome struct {
	Name  string
	Stage string
	Err   error
}

// failedConn builds a conn recording a connect-stage failure for cfg. session,
// tools, and origNames stay at their zero values: a failed conn has no live
// session and contributes no tools to ToolDefinitions/RegisterTools.
func failedConn(cfg mcpconfig.ServerConfig, err error) conn {
	return conn{name: cfg.Name, cfg: cfg, status: "failed", lastErr: err, lastErrAt: time.Now()}
}

// NewManager connects to all configured MCP servers concurrently and
// discovers their tools, namespacing them. Each server's transport build,
// connect, and tools/list run in their own goroutine under their own 10s
// timeout (derived from ctx), so one slow or hung server cannot delay or
// starve the others. It does not abort the batch when one server's transport
// build, connect, or tools/list fails: a conn is retained for every config
// (failed ones with status "failed" and no session), and each such failure is
// reported in the returned []ServerOutcome with Stage "connect". The manager
// is nil only when configs is empty. The transports parameter is optional:
// when nil, transports are created from configs. When provided (for testing),
// each transport[i] corresponds to configs[i].
//
// conns is preallocated to len(configs) and each goroutine writes exactly one
// index (mgr.conns[i], matching configs[i]), so the result is always in
// config order regardless of which server's connect finishes first — no
// mutex is needed on conns itself, since every index has exactly one writer
// and mgr.conns is only read after wg.Wait() establishes happens-before.
func NewManager(ctx context.Context, configs []mcpconfig.ServerConfig, transports []mcpsdk.Transport) (*Manager, []ServerOutcome) {
	if len(configs) == 0 {
		return nil, nil
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "serf",
		Version: "v1",
	}, nil)

	mgr := &Manager{conns: make([]conn, len(configs))}
	var wg sync.WaitGroup
	wg.Add(len(configs))
	for i, cfg := range configs {
		var transport mcpsdk.Transport
		if transports != nil && i < len(transports) {
			transport = transports[i]
		}
		go func(i int, cfg mcpconfig.ServerConfig, transport mcpsdk.Transport) {
			defer wg.Done()
			mgr.conns[i] = connectOne(ctx, client, cfg, transport)
		}(i, cfg, transport)
	}
	wg.Wait()

	var outcomes []ServerOutcome
	for _, c := range mgr.conns {
		if c.status != "connected" {
			outcomes = append(outcomes, ServerOutcome{Name: c.name, Stage: "connect", Err: c.lastErr})
		}
	}

	return mgr, outcomes
}

// connectOne builds (if transport is nil, from cfg) or reuses the given
// transport, connects to a single MCP server, and discovers its tools — all
// bounded by a 10s timeout derived from ctx. Any failure building the
// transport, connecting, or listing tools yields a "failed" conn recording
// the error; NewManager turns that into a connect-stage ServerOutcome.
func connectOne(ctx context.Context, client *mcpsdk.Client, cfg mcpconfig.ServerConfig, transport mcpsdk.Transport) conn {
	perServerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if transport == nil {
		var err error
		transport, err = transportForConfig(cfg)
		if err != nil {
			return failedConn(cfg, err)
		}
	}

	session, err := client.Connect(perServerCtx, transport, nil)
	if err != nil {
		return failedConn(cfg, err)
	}

	// Discover tools from the server.
	result, err := session.ListTools(perServerCtx, nil)
	if err != nil {
		_ = session.Close()
		return failedConn(cfg, err)
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

	return conn{
		name:      cfg.Name,
		cfg:       cfg,
		session:   session,
		tools:     tools,
		origNames: origNames,
		status:    "connected",
	}
}

// ToolDefinitions returns namespaced tool definitions from servers whose
// tools are actually registered and callable (status "connected"). A server
// demoted to "failed" during RegisterTools contributes no definitions, so the
// system-prompt-visible tool surface always matches the callable set.
func (m *Manager) ToolDefinitions() []llm.ToolDefinition {
	if m == nil {
		return nil
	}
	var defs []llm.ToolDefinition
	for _, c := range m.conns {
		if c.status != "connected" {
			continue
		}
		defs = append(defs, c.tools...)
	}
	return defs
}

// RegisterTools registers execution closures for all MCP tools from
// connected servers into the given tool.Registry. It does not abort the
// whole batch when one server's tools fail name validation or collide with
// an existing registration: that server is demoted to status "failed" and
// reported in the returned []ServerOutcome with Stage "register", and the
// next server is attempted. Only the namespaced names THAT SERVER itself
// successfully registered before the failure are rolled back — a name that
// already belongs to an earlier winner (or a built-in tool) is never
// touched, because it was never added to the failing server's own list.
func (m *Manager) RegisterTools(reg *tool.Registry) []ServerOutcome {
	if m == nil {
		return nil
	}
	var outcomes []ServerOutcome
	for i := range m.conns {
		c := &m.conns[i]
		if c.status != "connected" {
			continue
		}

		var added []string
		for _, td := range c.tools {
			// Validate the namespaced name (length, charset).
			if err := llm.ValidateToolName(td.Name); err != nil {
				outcomes = append(outcomes, failRegister(c, reg, added, fmt.Errorf("MCP tool %q: %w", td.Name, err)))
				break
			}

			// Check for collision with existing tools (built-ins, or an
			// earlier server's tools within this same call). This is safe
			// because RegisterTools is called only during single-threaded
			// session init, after registerCoreTools has already completed.
			if reg.Get(td.Name) != nil {
				outcomes = append(outcomes, failRegister(c, reg, added, fmt.Errorf("MCP tool %q collides with existing tool", td.Name)))
				break
			}

			// Look up the original MCP tool name for CallTool.
			origName := c.origNames[td.Name]
			sess := c.session

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
				outcomes = append(outcomes, failRegister(c, reg, added, fmt.Errorf("registering MCP tool %q: %w", td.Name, err)))
				break
			}

			added = append(added, td.Name)
		}
	}
	return outcomes
}

// failRegister rolls back exactly the names c itself added to reg (added)
// before hitting err, demotes c to status "failed", and returns the
// ServerOutcome to report. It must never be passed a name that belongs to a
// different conn: added tracks only names the CALLER appended after its own
// successful reg.Register calls, so a collision with an earlier winner's
// name — which never entered this conn's added list — leaves that name alone.
func failRegister(c *conn, reg *tool.Registry, added []string, err error) ServerOutcome {
	for _, n := range added {
		reg.Remove(n)
	}
	c.status = "failed"
	c.lastErr = err
	c.lastErrAt = time.Now()
	return ServerOutcome{Name: c.name, Stage: "register", Err: err}
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
		out[i] = mcpconfig.ServerInfo{Name: c.name, Tools: tools, Status: c.status}
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
