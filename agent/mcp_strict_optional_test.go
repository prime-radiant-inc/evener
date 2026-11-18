package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/mcp"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

// stickyBrowser is an in-process MCP server that mirrors the browser tool's
// tab contract: switch_tab moves the sticky selection, and the legacy
// tab_index overrides it only when the caller actually sends a number.
// Its InputSchema mirrors use_browser: action is required, tab_index is
// documented optional.
type stickyBrowser struct {
	activeTab int
	titles    []string
}

func (b *stickyBrowser) tool() *mcpsdk.Tool {
	return &mcpsdk.Tool{
		Name:        "use_browser",
		Description: "Browser control with sticky tab state.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"switch_tab", "eval"}},
				"payload":   map[string]any{"type": "string"},
				"tab_index": map[string]any{"type": "integer", "minimum": 0, "description": "Legacy: behaves like switch_tab. Prefer the switch_tab action."},
			},
			"required": []string{"action"},
		},
	}
}

func (b *stickyBrowser) handle(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, err
	}
	// Same rule as the real server: only a numeric tab_index overrides the
	// sticky selection. An absent key (or null) leaves it alone.
	if idx, ok := args["tab_index"].(float64); ok {
		b.activeTab = int(idx)
	}
	switch args["action"] {
	case "switch_tab":
		var idx int
		if _, err := fmt.Sscanf(fmt.Sprint(args["payload"]), "%d", &idx); err != nil {
			return nil, err
		}
		b.activeTab = idx
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Switched to tab %d", idx)}}}, nil
	case "eval":
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: b.titles[b.activeTab]}}}, nil
	}
	return nil, fmt.Errorf("unknown action %v", args["action"])
}

// stickyBrowserTools connects the fake browser server through the real MCP
// manager and registry, returning the registered definitions and registry.
func stickyBrowserTools(t *testing.T) (*stickyBrowser, *tool.Registry) {
	t.Helper()
	browser := &stickyBrowser{titles: []string{"first-fixture", "second-fixture"}}

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "browser", Version: "v0"}, nil)
	server.AddTool(browser.tool(), browser.handle)

	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mgr, outcomes := mcp.NewManager(t.Context(), []mcpconfig.ServerConfig{{Name: "browser", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){
			func(context.Context) (mcpsdk.Transport, error) { return ct, nil },
		})
	if len(outcomes) != 0 {
		t.Fatalf("mcp.NewManager: %+v", outcomes)
	}
	t.Cleanup(mgr.Close)

	reg := tool.NewRegistry()
	if out := mgr.RegisterTools(reg); len(out) != 0 {
		t.Fatalf("RegisterTools: %+v", out)
	}
	return browser, reg
}

// projectedStrictSchema runs an imported MCP tool definition through the
// advertise edge and the OpenAI Responses strict projection, returning the
// schema the model actually sees.
func projectedStrictSchema(t *testing.T, def llm.ToolDefinition) map[string]any {
	t.Helper()
	req := llm.Request{
		Model:    "gpt-5.5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}}},
		// The advertise edge marks intent required, exactly as the session's
		// tool list does for a work tool.
		Tools: []llm.ToolDefinition{tool.WithIntentParameterRequired(def)},
	}
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIResponses), StrictTools: new(true)}
	res := registry.Resolved{Instance: "openai", Protocol: registry.ProtocolOpenAIResponses, ModelID: "gpt-5.5", WireID: "gpt-5.5", Caps: caps}
	body, err := (&responses.Protocol{}).BuildBody(llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	tools, _ := body["tools"].([]map[string]any)
	if len(tools) != 1 {
		t.Fatalf("projected %d tools, want 1", len(tools))
	}
	params, _ := tools[0]["parameters"].(map[string]any)
	if params == nil {
		t.Fatalf("projected tool carries no parameters: %#v", tools[0])
	}
	return params
}

// A call that omits the browser tool's legacy tab_index must keep the tab that
// switch_tab selected. The schema the model sees decides whether that call is
// even expressible: under strict projection every property is required, so an
// omitting call is invalid and every call carries an explicit index that
// overrides the sticky selection (#6200).
//
// This is the issue's own reproduction, driven through the projection and the
// real MCP executor rather than a browser session: the projected schema must
// admit the omitting call, and the executor must then leave the selection
// alone.
func TestStickyTabSurvivesACallThatOmitsTabIndex(t *testing.T) {
	t.Parallel()
	browser, reg := stickyBrowserTools(t)

	definition := reg.Get("browser__use_browser")
	if definition == nil {
		t.Fatal("browser tool not registered")
	}
	projected := projectedStrictSchema(t, definition.Definition)

	// The omitting call, exactly as a model would send it when it wants the
	// current tab.
	omitting := map[string]any{"action": "eval", "payload": "document.title", "intent": "read the current tab title"}
	omittingJSON, err := json.Marshal(omitting)
	if err != nil {
		t.Fatal(err)
	}

	// The advertised schema must have a legal omit form at all. Serialize with
	// the projected schema's own required list so a caller constrained by it
	// can be emulated faithfully below.
	projectedJSON, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.CompileString("https://evener.test/use_browser.json", string(projectedJSON))
	if err != nil {
		t.Fatalf("compile projected schema: %v", err)
	}
	if err := schema.Validate(omitting); err != nil {
		t.Fatalf("the projected schema admits no call that omits tab_index, so the documented current-tab workflow is unreachable: %v", err)
	}

	// switch_tab selects the second fixture (tab 1).
	selected := reg.ExecuteCall(t.Context(), &agenttest.FakeEnv{WorkDir: t.TempDir()}, llm.ToolCallData{
		ID:        "call_select",
		Name:      "browser__use_browser",
		Arguments: json.RawMessage(`{"action":"switch_tab","payload":"1","intent":"select the second fixture"}`),
	})
	if selected.IsError {
		t.Fatalf("switch_tab: %s", selected.Output)
	}
	if browser.activeTab != 1 {
		t.Fatalf("activeTab = %d after switch_tab, want 1", browser.activeTab)
	}

	// The omitting call must not override that selection.
	got := reg.ExecuteCall(t.Context(), &agenttest.FakeEnv{WorkDir: t.TempDir()}, llm.ToolCallData{
		ID:        "call_eval",
		Name:      "browser__use_browser",
		Arguments: omittingJSON,
	})
	if got.IsError {
		t.Fatalf("eval: %s", got.Output)
	}
	if got.Output != "second-fixture" {
		t.Fatalf("eval returned %q, want the sticky-selected tab's title %q: an omitted tab_index overrode the switch_tab selection", got.Output, "second-fixture")
	}
	if browser.activeTab != 1 {
		t.Fatalf("activeTab = %d after an omitting call, want the sticky selection 1", browser.activeTab)
	}
}

// An explicit index still overrides the sticky tab. The fix must not remove
// the legacy override; it only stops the schema from forcing it on every call.
func TestExplicitTabIndexStillOverridesTheStickyTab(t *testing.T) {
	t.Parallel()
	browser, reg := stickyBrowserTools(t)

	calls := []string{
		`{"action":"switch_tab","payload":"1","intent":"select the second fixture"}`,
		`{"action":"eval","payload":"document.title","tab_index":0,"intent":"read the first fixture"}`,
	}
	for i, raw := range calls {
		got := reg.ExecuteCall(t.Context(), &agenttest.FakeEnv{WorkDir: t.TempDir()}, llm.ToolCallData{
			ID:        "call_" + string(rune('a'+i)),
			Name:      "browser__use_browser",
			Arguments: json.RawMessage(raw),
		})
		if got.IsError {
			t.Fatalf("call %d: %s", i, got.Output)
		}
		if i == 1 && got.Output != "first-fixture" {
			t.Fatalf("eval returned %q, want the explicitly requested tab 0's title %q", got.Output, "first-fixture")
		}
	}
	if browser.activeTab != 0 {
		t.Fatalf("activeTab = %d, want 0 after an explicit tab_index override", browser.activeTab)
	}
}

// The projected schema for every registered MCP tool that leaves a property
// optional must keep that property optional: none of them may appear in the
// projected required list unless the server declared it required.
func TestImportedMCPOptionalArgumentsStayOptionalInProjection(t *testing.T) {
	t.Parallel()
	_, reg := stickyBrowserTools(t)
	definition := reg.Get("browser__use_browser")
	if definition == nil {
		t.Fatal("browser tool not registered")
	}
	projected := projectedStrictSchema(t, definition.Definition)

	var required []string
	switch list := projected["required"].(type) {
	case []string:
		required = append(required, list...)
	case []any:
		for _, v := range list {
			if name, ok := v.(string); ok {
				required = append(required, name)
			}
		}
	}
	if slices.Contains(required, "tab_index") {
		t.Fatalf("projected required = %v: tab_index is documented optional and must not be forced", required)
	}
	if !slices.Contains(required, "action") {
		t.Fatalf("projected required = %v, want the server-declared required argument action", required)
	}
}
