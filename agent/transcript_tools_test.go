package agent

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// Helper to create a test SessionSnapshot with known turns
func createTestSnapshot(t *testing.T, dir string, id string, history []schema.Turn) string {
	t.Helper()
	snap := SessionSnapshot{
		ID:        id,
		ProfileID: "test-profile",
		Model:     "test-model",
		Config:    SessionConfig{},
		EnvInfo:   EnvironmentInfo{},
		History:   history,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		TurnCount: len(history),
	}
	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("failed to save test snapshot: %v", err)
	}
	return filepath.Join(dir, "sessions", id+".json")
}

func TestSearchTranscript(t *testing.T) {
	dir := t.TempDir()

	// Create a snapshot with known turns
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Hello world")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Hi there! How can I help you today?")),
		schema.NewTurn(schema.TurnUserInput, llm.User("Tell me about Go programming")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Go is a statically typed, compiled programming language.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("What about error handling?")),
	}
	path := createTestSnapshot(t, dir, "search-test", history)

	tests := []struct {
		name        string
		query       string
		wantCount   int
		wantIndices []int
		wantPreview string // substring to check in first match
	}{
		{
			name:        "find hello",
			query:       "hello",
			wantCount:   1,
			wantIndices: []int{0},
			wantPreview: "Hello world",
		},
		{
			name:        "case insensitive",
			query:       "PROGRAMMING",
			wantCount:   2,
			wantIndices: []int{2, 3},
			wantPreview: "Go programming",
		},
		{
			name:        "multiple matches",
			query:       "Go",
			wantCount:   2,
			wantIndices: []int{2, 3},
			wantPreview: "Go programming",
		},
		{
			name:      "no matches",
			query:     "nonexistent",
			wantCount: 0,
		},
		{
			name:        "partial word match",
			query:       "err",
			wantCount:   1,
			wantIndices: []int{4},
			wantPreview: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches, err := SearchTranscript(path, tt.query)
			if err != nil {
				t.Fatalf("SearchTranscript failed: %v", err)
			}
			if len(matches) != tt.wantCount {
				t.Errorf("got %d matches, want %d", len(matches), tt.wantCount)
			}
			for i, wantIdx := range tt.wantIndices {
				if i >= len(matches) {
					break
				}
				if matches[i].Index != wantIdx {
					t.Errorf("match %d: got index %d, want %d", i, matches[i].Index, wantIdx)
				}
			}
			if tt.wantCount > 0 && tt.wantPreview != "" {
				if !contains(matches[0].Preview, tt.wantPreview) {
					t.Errorf("preview %q does not contain %q", matches[0].Preview, tt.wantPreview)
				}
			}
		})
	}
}

func TestSearchTranscript_EmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	path := createTestSnapshot(t, dir, "empty", []schema.Turn{})

	matches, err := SearchTranscript(path, "anything")
	if err != nil {
		t.Fatalf("SearchTranscript failed: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("got %d matches, want 0 for empty transcript", len(matches))
	}
}

func TestSearchTranscript_InvalidPath(t *testing.T) {
	_, err := SearchTranscript("/nonexistent/path.json", "query")
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

func TestReadTurnsFromSnapshot(t *testing.T) {
	dir := t.TempDir()

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Turn 0")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Turn 1")),
		schema.NewTurn(schema.TurnUserInput, llm.User("Turn 2")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Turn 3")),
		schema.NewTurn(schema.TurnUserInput, llm.User("Turn 4")),
	}
	path := createTestSnapshot(t, dir, "read-test", history)

	tests := []struct {
		name      string
		start     int
		end       int
		wantCount int
		wantFirst string // text in first returned turn
		wantLast  string // text in last returned turn
	}{
		{
			name:      "read middle range",
			start:     1,
			end:       3,
			wantCount: 2,
			wantFirst: "Turn 1",
			wantLast:  "Turn 2",
		},
		{
			name:      "read all",
			start:     0,
			end:       5,
			wantCount: 5,
			wantFirst: "Turn 0",
			wantLast:  "Turn 4",
		},
		{
			name:      "read single",
			start:     2,
			end:       3,
			wantCount: 1,
			wantFirst: "Turn 2",
			wantLast:  "Turn 2",
		},
		{
			name:      "clamp negative start",
			start:     -5,
			end:       2,
			wantCount: 2,
			wantFirst: "Turn 0",
			wantLast:  "Turn 1",
		},
		{
			name:      "clamp end beyond bounds",
			start:     3,
			end:       100,
			wantCount: 2,
			wantFirst: "Turn 3",
			wantLast:  "Turn 4",
		},
		{
			name:      "empty range",
			start:     2,
			end:       2,
			wantCount: 0,
		},
		{
			name:      "inverted range",
			start:     3,
			end:       1,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turns, err := ReadTurnsFromSnapshot(path, tt.start, tt.end)
			if err != nil {
				t.Fatalf("ReadTurnsFromSnapshot failed: %v", err)
			}
			if len(turns) != tt.wantCount {
				t.Errorf("got %d turns, want %d", len(turns), tt.wantCount)
			}
			if tt.wantCount > 0 {
				firstText := turns[0].Message.Text()
				if !contains(firstText, tt.wantFirst) {
					t.Errorf("first turn text %q does not contain %q", firstText, tt.wantFirst)
				}
				lastText := turns[len(turns)-1].Message.Text()
				if !contains(lastText, tt.wantLast) {
					t.Errorf("last turn text %q does not contain %q", lastText, tt.wantLast)
				}
			}
		})
	}
}

func TestReadTurnsFromSnapshot_InvalidPath(t *testing.T) {
	_, err := ReadTurnsFromSnapshot("/nonexistent/path.json", 0, 10)
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

func TestFilterTurns(t *testing.T) {
	dir := t.TempDir()

	// Create history with various turn kinds and tool results
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("First user message")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("First assistant response")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call1", Name: "TestTool"}},
			},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call1", "success result", false)),
		schema.NewTurn(schema.TurnUserInput, llm.User("Second user message with keyword")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Another assistant response")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call2", Name: "FailTool"}},
			},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call2", "error result", true)),
		schema.NewTurn(schema.TurnSteering, llm.User("Steering message")),
	}
	path := createTestSnapshot(t, dir, "filter-test", history)

	tests := []struct {
		name        string
		kind        string
		contains    string
		errorsOnly  bool
		wantCount   int
		wantIndices []int
	}{
		{
			name:        "filter by USER_INPUT",
			kind:        "USER_INPUT",
			wantCount:   2,
			wantIndices: []int{0, 4},
		},
		{
			name:        "filter by ASSISTANT",
			kind:        "ASSISTANT",
			wantCount:   4,
			wantIndices: []int{1, 2, 5, 6},
		},
		{
			name:        "filter by TOOL_RESULTS",
			kind:        "TOOL_RESULTS",
			wantCount:   2,
			wantIndices: []int{3, 7},
		},
		{
			name:        "filter by STEERING",
			kind:        "STEERING",
			wantCount:   1,
			wantIndices: []int{8},
		},
		{
			name:        "filter USER_INPUT with keyword",
			kind:        "USER_INPUT",
			contains:    "keyword",
			wantCount:   1,
			wantIndices: []int{4},
		},
		{
			name:        "filter ASSISTANT with 'another'",
			kind:        "ASSISTANT",
			contains:    "another",
			wantCount:   1,
			wantIndices: []int{5},
		},
		{
			name:        "filter errors only",
			kind:        "",
			errorsOnly:  true,
			wantCount:   1,
			wantIndices: []int{7},
		},
		{
			name:        "filter TOOL_RESULTS errors only",
			kind:        "TOOL_RESULTS",
			errorsOnly:  true,
			wantCount:   1,
			wantIndices: []int{7},
		},
		{
			name:      "filter with no matches",
			kind:      "USER_INPUT",
			contains:  "nonexistent",
			wantCount: 0,
		},
		{
			name:        "no filters - all turns",
			kind:        "",
			contains:    "",
			errorsOnly:  false,
			wantCount:   9,
			wantIndices: []int{0, 1, 2, 3, 4, 5, 6, 7, 8},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches, err := FilterTurns(path, tt.kind, tt.contains, tt.errorsOnly)
			if err != nil {
				t.Fatalf("FilterTurns failed: %v", err)
			}
			if len(matches) != tt.wantCount {
				t.Errorf("got %d matches, want %d", len(matches), tt.wantCount)
			}
			for i, wantIdx := range tt.wantIndices {
				if i >= len(matches) {
					break
				}
				if matches[i].Index != wantIdx {
					t.Errorf("match %d: got index %d, want %d", i, matches[i].Index, wantIdx)
				}
				if matches[i].Kind != string(history[wantIdx].Kind) {
					t.Errorf("match %d: got kind %s, want %s", i, matches[i].Kind, history[wantIdx].Kind)
				}
			}
		})
	}
}

func TestFilterTurns_InvalidPath(t *testing.T) {
	_, err := FilterTurns("/nonexistent/path.json", "", "", false)
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

// Helper function for substring checking
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
