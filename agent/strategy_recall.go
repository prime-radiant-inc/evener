package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"primeradiant.com/serf/llm"
)

// recallStrategy extends compactStrategy by adding a "recall" tool that spawns
// a sub-agent to search through the session transcript. This allows the main
// agent to retrieve information from compacted-away history.
type recallStrategy struct {
	compact *compactStrategy
	session strategyHost // parent session for saving/transcript access
}

// newRecallStrategy creates a recallStrategy backed by the given contextManager.
// The host reference may be nil during construction if not yet available.
func newRecallStrategy(cm *contextManager, host strategyHost) *recallStrategy {
	return &recallStrategy{
		compact: newCompactStrategy(cm),
		session: host,
	}
}

// Name returns the strategy's identifier, "recall".
func (s *recallStrategy) Name() string { return "recall" }

// ManageContext delegates to the underlying compactStrategy to manage the
// conversation history.
func (s *recallStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error {
	return s.compact.ManageContext(ctx, history, sysPromptChars, emitFn)
}

// AfterAction is a no-op for recallStrategy.
func (s *recallStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
	return nil
}

// Tools returns the strategy's registered tools, namely the "recall" tool.
func (s *recallStrategy) Tools() []RegisteredTool {
	return []RegisteredTool{recallToolDef(s)}
}

// transcriptPath returns the path where the session snapshot is stored.
func transcriptPath(stateDir, sessionID string) string {
	return filepath.Join(stateDir, "sessions", sessionID+".json")
}

// recallToolDef builds the RegisteredTool for the "recall" tool.
func recallToolDef(strategy *recallStrategy) RegisteredTool {
	return buildRecallTool(func() strategyHost { return strategy.session })
}

// buildRecallTool creates a recall RegisteredTool that uses getHost to
// obtain the parent session at call time. Shared by recallStrategy and
// sessionLogStrategy.
func buildRecallTool(getHost func() strategyHost) RegisteredTool {
	return RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        "recall",
				Description: "Search through earlier parts of this session's history that may have been compacted away. Use this when you need to remember details from earlier in the session.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "What you want to recall from earlier in the session",
						},
					},
					"required": []string{"question"},
				},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			question, ok := args["question"].(string)
			if !ok || question == "" {
				return nil, fmt.Errorf("recall requires a non-empty 'question' string")
			}

			host := getHost()
			if host == nil {
				return nil, fmt.Errorf("recall: no session reference available")
			}

			// Save a full snapshot (with history) for the transcript search tools.
			// maybeAutoSave only writes lightweight meta now, so we need the full snapshot here.
			snap := host.Snapshot()
			if err := SaveSession(host.StateDir(), snap); err != nil {
				return nil, fmt.Errorf("recall: save snapshot for search: %w", err)
			}

			path := transcriptPath(host.StateDir(), host.ID())
			return recallExecute(ctx, host.Client(), host.Profile().ID(), host.Profile().Model(), path, question)
		},
	}
}

var recallSystemPrompt = `You are a transcript search agent. Your job is to search through a session transcript to find information relevant to a question.

You have access to transcript search tools. Use them to find and read relevant turns from the session history, then provide a clear, concise answer to the question.

Strategy:
1. Start with search_transcript to find turns matching key terms from the question
2. Use read_turns to read the full content of promising matches
3. Use filter_turns if you need to narrow by turn type or error status
4. Once you have enough information, return your answer as plain text

Be thorough but concise. Include specific details (file names, code snippets, error messages) when relevant.`

// recallExecute runs a mini tool loop with transcript search tools to answer a question.
func recallExecute(ctx context.Context, client *llm.Client, provider, model, snapPath string, question string) (string, error) {
	// Verify the transcript file exists by attempting to load it.
	if _, err := loadSnapshotFromPath(snapPath); err != nil {
		return "", fmt.Errorf("recall: cannot load transcript: %w", err)
	}

	tools := recallTranscriptTools(snapPath)

	maxRounds := 10
	systemPrompt := recallSystemPrompt
	result, err := llm.Generate(ctx, llm.GenerateOptions{
		Client:        client,
		Model:         model,
		Provider:      provider,
		System:        &systemPrompt,
		Prompt:        &question,
		Tools:         tools,
		MaxToolRounds: &maxRounds,
	})
	if err != nil {
		return "", fmt.Errorf("recall sub-agent: %w", err)
	}

	return result.Text, nil
}

// recallTranscriptTools builds llm.Tool instances for the transcript search functions,
// with the snapshot path pre-bound.
func recallTranscriptTools(snapPath string) []llm.Tool {
	return []llm.Tool{
		{
			Definition: llm.ToolDefinition{
				Name:        "search_transcript",
				Description: "Search the session transcript for turns containing the given query string (case-insensitive). Returns matching turn indices, kinds, and previews.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "The search query string",
						},
					},
					"required": []string{"query"},
				},
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				m, ok := args.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("expected map args")
				}
				query, _ := m["query"].(string)
				matches, err := SearchTranscript(snapPath, query)
				if err != nil {
					return nil, err
				}
				b, err := json.Marshal(matches)
				if err != nil {
					return nil, err
				}
				return string(b), nil
			},
		},
		{
			Definition: llm.ToolDefinition{
				Name:        "read_turns",
				Description: "Read full turn content from the session transcript by index range [start, end). Use after search_transcript to read matching turns in detail.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"start": map[string]any{
							"type":        "integer",
							"description": "Start index (inclusive)",
						},
						"end": map[string]any{
							"type":        "integer",
							"description": "End index (exclusive)",
						},
					},
					"required": []string{"start", "end"},
				},
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				m, ok := args.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("expected map args")
				}
				start := int(m["start"].(float64))
				end := int(m["end"].(float64))
				turns, err := ReadTurnsFromSnapshot(snapPath, start, end)
				if err != nil {
					return nil, err
				}
				b, err := json.Marshal(turns)
				if err != nil {
					return nil, err
				}
				return string(b), nil
			},
		},
		{
			Definition: llm.ToolDefinition{
				Name:        "filter_turns",
				Description: "Filter session transcript turns by kind, content substring, and/or error status. Returns matching turn indices, kinds, and previews.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"kind": map[string]any{
							"type":        "string",
							"description": "Turn kind filter (e.g., USER_INPUT, ASSISTANT, TOOL_RESULTS, STEERING). Empty string means no kind filter.",
						},
						"contains": map[string]any{
							"type":        "string",
							"description": "Case-insensitive substring match on turn text. Empty string means no content filter.",
						},
						"errors_only": map[string]any{
							"type":        "boolean",
							"description": "If true, only return turns with error tool results.",
						},
					},
				},
			},
			Execute: func(ctx context.Context, args any) (any, error) {
				m, ok := args.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("expected map args")
				}
				kind, _ := m["kind"].(string)
				contains, _ := m["contains"].(string)
				errorsOnly, _ := m["errors_only"].(bool)
				matches, err := FilterTurns(snapPath, kind, contains, errorsOnly)
				if err != nil {
					return nil, err
				}
				b, err := json.Marshal(matches)
				if err != nil {
					return nil, err
				}
				return string(b), nil
			},
		},
	}
}
