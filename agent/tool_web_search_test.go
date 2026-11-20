package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestWebSearch_GoogleSessionRegistersNoFunctionToolAndDispatchesNoSideGrounding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	profile := withWebSearch(newGeminiProfile("gemini-3-pro"), true)
	if !profile.SupportsWebSearch() {
		t.Fatal("pre-condition: profile must carry WebSearch=true")
	}
	if profile.Protocol() != registry.ProtocolGoogle {
		t.Fatalf("pre-condition: Protocol() = %q, want %q", profile.Protocol(), registry.ProtocolGoogle)
	}

	fa := newFakeAdapter("google", func(req llm.Request) llm.Response {
		return agenttest.CommunicateResponse(true, "answered without side grounding")
	})
	c := llm.NewClient()
	c.Register(fa)

	sess, err := NewSession(c, withTestSessionNamer(c, profile), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.reg.Get("web_search") != nil {
		t.Fatal("ProtocolGoogle session registered legacy web_search function tool")
	}
	for _, td := range sess.allToolDefinitions(0) {
		if td.Name == "web_search" {
			t.Fatal("ProtocolGoogle session advertised legacy web_search function tool")
		}
	}

	out, err := sess.ProcessInput(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "answered without side grounding" {
		t.Fatalf("output = %q, want %q", out, "answered without side grounding")
	}

	reqs := fa.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model calls = %d, want 1 (no side Grounding calls)", len(reqs))
	}
	if reqs[0].WebSearch {
		t.Fatal("ProtocolGoogle session set req.WebSearch=true on outbound request")
	}
}

func TestWebSearch_ExpandHistoryOmitsGoogleGroundingAndLegacyToolPairs(t *testing.T) {
	t.Parallel()

	t.Run("restore omits grounded output and unprefixed tool pairs while keeping transcript untouched", func(t *testing.T) {
		t.Parallel()
		stateDir := t.TempDir()
		workDir := t.TempDir()
		sessionID := "google-grounding-restore"

		meta := schema.SessionMeta{
			ID:        sessionID,
			ProfileID: "google",
			Model:     "gemini-3-pro",
			Config:    minimalTestSessionConfig(t).toSnapshot(),
		}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}

		tpath := schema.SessionTranscriptPath(stateDir, sessionID)
		tw, err := transcript.NewWriter(tpath, transcript.Header{
			SessionID: sessionID,
			ProfileID: "google",
			Model:     "gemini-3-pro",
		})
		if err != nil {
			t.Fatalf("transcript.NewWriter: %v", err)
		}

		legacyArgs := json.RawMessage(`{"query":"secret grounded query"}`)
		mcpArgs := json.RawMessage(`{"query":"mcp query"}`)
		pluginArgs := json.RawMessage(`{"query":"plugin query"}`)

		seededTurns := []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User("search for go 1.24")),
			{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Go 1.24 was grounded and released."},
						{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{
							Query:   "go 1.24 release",
							Sources: []llm.WebSearchSource{{Title: "Go Blog", URL: "https://go.dev/blog"}},
						}},
					},
				},
				ResponseProvider: "google",
				ResponseProtocol: registry.ProtocolGoogle,
				ResponseModel:    "gemini-3-pro",
			},
			{
				Kind: schema.TurnAssistant,
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:                "ws-legacy-1",
								Name:              "web_search",
								Arguments:         legacyArgs,
								ArgumentsSpelling: llm.Spelling(`{ "query" : "secret grounded query" }`),
								RawArguments:      "raw-ws-bytes",
								Type:              "function",
							},
						},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "ws-mcp-1",
								Name:      "mcp__srv__web_search",
								Arguments: mcpArgs,
								Type:      "function",
							},
						},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "ws-plugin-1",
								Name:      "plugin_foo_web_search",
								Arguments: pluginArgs,
								Type:      "function",
							},
						},
					},
				},
				ResponseProvider: "google",
				ResponseProtocol: registry.ProtocolGoogle,
				ResponseModel:    "gemini-3-pro",
			},
			schema.NewTurn(schema.TurnToolResults, llm.Message{
				Role: llm.RoleTool,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "ws-legacy-1",
						Name:       "web_search",
						Content:    "secret grounded web search result",
					}},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "ws-mcp-1",
						Name:       "mcp__srv__web_search",
						Content:    "mcp output",
					}},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "ws-plugin-1",
						Name:       "plugin_foo_web_search",
						Content:    "plugin output",
					}},
				},
			}),
		}
		for _, turn := range seededTurns {
			if err := tw.Append(turn); err != nil {
				t.Fatalf("AppendTurn: %v", err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("tw.Close: %v", err)
		}

		prefixBytes, err := os.ReadFile(tpath)
		if err != nil {
			t.Fatalf("ReadFile before restore: %v", err)
		}

		fa := newFakeAdapter("google", func(req llm.Request) llm.Response {
			return agenttest.CommunicateResponse(true, "follow-up ok")
		})
		c := llm.NewClient()
		c.Register(fa)

		profile := withTestSessionNamer(c, newGeminiProfile("gemini-3-pro"))
		restored, err := RestoreSessionFromMetaWithConfig(c, profile, execenv.NewLocalExecutionEnvironment(workDir), meta, RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		defer restored.Close()

		if _, err := restored.ProcessInput(context.Background(), "follow up", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}

		reqs := fa.Requests()
		if len(reqs) != 1 {
			t.Fatalf("model calls = %d, want 1", len(reqs))
		}
		msgs := reqs[0].Messages

		var sawOmittedGroundedText, sawOmittedToolArgs, sawOmittedToolResult bool
		var sawMCPArgs, sawMCPResult, sawPluginArgs, sawPluginResult bool
		for _, msg := range msgs {
			for _, p := range msg.Content {
				switch p.Kind {
				case llm.ContentWebSearch:
					t.Fatal("outbound replay still contained ContentWebSearch from ProtocolGoogle turn")
				case llm.ContentText:
					if p.Text == "Go 1.24 was grounded and released." {
						t.Fatal("outbound replay leaked original grounded ProtocolGoogle assistant text")
					}
					if p.Text == "[historical grounded output omitted from model replay]" {
						sawOmittedGroundedText = true
					}
				case llm.ContentToolCall:
					if p.ToolCall == nil {
						continue
					}
					switch p.ToolCall.Name {
					case "web_search":
						if string(p.ToolCall.Arguments) != `{"_omitted":"[historical grounded tool input omitted from model replay]"}` {
							t.Fatalf("omitted web_search args = %s", string(p.ToolCall.Arguments))
						}
						if p.ToolCall.ArgumentsSpelling != "" || p.ToolCall.RawArguments != "" {
							t.Fatalf("expected cleared ArgumentsSpelling/RawArguments, got spelling=%q raw=%q", p.ToolCall.ArgumentsSpelling, p.ToolCall.RawArguments)
						}
						sawOmittedToolArgs = true
					case "mcp__srv__web_search":
						if string(p.ToolCall.Arguments) != string(mcpArgs) {
							t.Fatalf("mcp__srv__web_search args mutated: %s", string(p.ToolCall.Arguments))
						}
						sawMCPArgs = true
					case "plugin_foo_web_search":
						if string(p.ToolCall.Arguments) != string(pluginArgs) {
							t.Fatalf("plugin_foo_web_search args mutated: %s", string(p.ToolCall.Arguments))
						}
						sawPluginArgs = true
					}
				case llm.ContentToolResult:
					if p.ToolResult == nil {
						continue
					}
					switch p.ToolResult.ToolCallID {
					case "ws-legacy-1":
						if p.ToolResult.Content != "[historical web_search output omitted from model replay]" {
							t.Fatalf("omitted web_search result = %#v", p.ToolResult.Content)
						}
						sawOmittedToolResult = true
					case "ws-mcp-1":
						if p.ToolResult.Content != "mcp output" {
							t.Fatalf("mcp__srv__web_search result mutated: %#v", p.ToolResult.Content)
						}
						sawMCPResult = true
					case "ws-plugin-1":
						if p.ToolResult.Content != "plugin output" {
							t.Fatalf("plugin_foo_web_search result mutated: %#v", p.ToolResult.Content)
						}
						sawPluginResult = true
					}
				}
			}
		}
		if !sawOmittedGroundedText || !sawOmittedToolArgs || !sawOmittedToolResult {
			t.Fatalf("missing expected omissions: text=%v args=%v result=%v", sawOmittedGroundedText, sawOmittedToolArgs, sawOmittedToolResult)
		}
		if !sawMCPArgs || !sawMCPResult || !sawPluginArgs || !sawPluginResult {
			t.Fatalf("prefixed tools were not preserved: mcp=(%v,%v) plugin=(%v,%v)", sawMCPArgs, sawMCPResult, sawPluginArgs, sawPluginResult)
		}

		afterBytes, err := os.ReadFile(tpath)
		if err != nil {
			t.Fatalf("ReadFile after restore: %v", err)
		}
		if !bytes.HasPrefix(afterBytes, prefixBytes) {
			t.Fatal("transcript prefix on disk was modified; historical turns must remain untouched on disk")
		}
	})

	t.Run("google web_search ineligible even on same-protocol replay", func(t *testing.T) {
		t.Parallel()
		turns := []schema.Turn{{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "q"}},
				{Kind: llm.ContentText, Text: "grounded text"},
			}},
			ResponseProvider: "google",
			ResponseProtocol: registry.ProtocolGoogle,
		}}
		scope := replayScope{
			Instance:     "google",
			Model:        "gemini-3-pro",
			Protocol:     registry.ProtocolGoogle,
			InFlightFrom: len(turns),
			protocolOf:   func(string) string { return registry.ProtocolGoogle },
		}
		if scope.webSearchReplayEligible(turns[0]) {
			t.Fatal("webSearchReplayEligible returned true for ProtocolGoogle producer")
		}
		out := expandHistory(turns, scope)
		if hasContentKind(out, llm.ContentWebSearch) {
			t.Fatal("ContentWebSearch replayed on same-protocol ProtocolGoogle expandHistory")
		}
		if len(out) != 1 || out[0].Text() != "[historical grounded output omitted from model replay]" {
			t.Fatalf("expanded text = %q, want omitted placeholder", out[0].Text())
		}
	})

	t.Run("ResultToolName web_search preserved on Google session and replay", func(t *testing.T) {
		t.Parallel()
		stateDir := t.TempDir()
		profile := withWebSearch(newGeminiProfile("gemini-3-pro"), true)
		fa := newFakeAdapter("google",
			func(req llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "res-ws-1",
					Name:      "web_search",
					Arguments: json.RawMessage(`{"message":"final result","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`),
				})
			},
			func(req llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "res-ws-2",
					Name:      "web_search",
					Arguments: json.RawMessage(`{"message":"second result","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`),
				})
			},
		)
		c := llm.NewClient()
		c.Register(fa)
		sess, err := NewSession(c, withTestSessionNamer(c, profile), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
			StateDir:       stateDir,
			ResultToolName: "web_search",
			testOnly:       testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		})
		if err != nil {
			t.Fatalf("NewSession with ResultToolName=web_search on Google: %v", err)
		}
		defer sess.Close()
		requireResultToolShape(t, sess, "web_search")

		out1, err := sess.ProcessInput(context.Background(), "turn 1", nil)
		if err != nil {
			t.Fatalf("turn 1: %v", err)
		}
		if out1 != "final result" {
			t.Fatalf("turn 1 output = %q", out1)
		}
		out2, err := sess.ProcessInput(context.Background(), "turn 2", nil)
		if err != nil {
			t.Fatalf("turn 2: %v", err)
		}
		if out2 != "second result" {
			t.Fatalf("turn 2 output = %q", out2)
		}

		reqs := fa.Requests()
		if len(reqs) != 2 {
			t.Fatalf("requests = %d, want 2", len(reqs))
		}
		var preservedCall bool
		for _, m := range reqs[1].Messages {
			for _, p := range m.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.ID == "res-ws-1" {
					if string(p.ToolCall.Arguments) == `{"message":"final result","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}` {
						preservedCall = true
					}
				}
			}
		}
		if !preservedCall {
			t.Fatal("SessionConfig{ResultToolName: web_search} call arguments were unexpectedly redacted on replay")
		}
	})
}
