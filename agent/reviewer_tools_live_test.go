//go:build integration

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

const testModel = "gpt-5.3-codex"

// responsesRequest is the body sent to the OpenAI Responses API.
type responsesRequest struct {
	Model      string `json:"model"`
	Input      []any  `json:"input"`
	Tools      []any  `json:"tools,omitempty"`
	ToolChoice any    `json:"tool_choice,omitempty"`
	Store      bool   `json:"store"`
}

// apiKeyOrSkip reads OPENAI_API_KEY from the environment or skips the test.
func apiKeyOrSkip(t *testing.T) string {
	t.Helper()
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	return key
}

// callResponsesAPI sends a raw HTTP POST to the OpenAI Responses API and
// returns the parsed JSON response body.
func callResponsesAPI(t *testing.T, apiKey string, body any) map[string]any {
	t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	t.Logf("REQUEST BODY:\n%s", indentJSON(t, b))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	t.Logf("HTTP STATUS: %d", resp.StatusCode)
	t.Logf("RESPONSE BODY:\n%s", indentJSON(t, respBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("API returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var result map[string]any
	if err := json.Unmarshal(respBytes, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return result
}

// indentJSON returns pretty-printed JSON for logging, or the raw bytes if
// formatting fails.
func indentJSON(t *testing.T, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return string(b)
	}
	return buf.String()
}

// extractToolCalls walks the response output array and returns all
// function_call items as a slice of maps.
func extractToolCalls(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	output, ok := resp["output"].([]any)
	if !ok {
		t.Logf("no output array in response")
		return nil
	}
	var calls []map[string]any
	for _, itemAny := range output {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		if typ == "function_call" {
			calls = append(calls, item)
		}
	}
	return calls
}

// logOutputItems logs every item in the response output for debugging.
func logOutputItems(t *testing.T, resp map[string]any) {
	t.Helper()
	output, ok := resp["output"].([]any)
	if !ok {
		t.Logf("OUTPUT: (no output array)")
		return
	}
	for i, itemAny := range output {
		item, ok := itemAny.(map[string]any)
		if !ok {
			t.Logf("OUTPUT[%d]: (not a map) %v", i, itemAny)
			continue
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "function_call":
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			callID, _ := item["call_id"].(string)
			t.Logf("OUTPUT[%d]: function_call name=%q call_id=%q args=%s", i, name, callID, args)
		case "message":
			content, _ := item["content"].([]any)
			for j, cAny := range content {
				c, ok := cAny.(map[string]any)
				if !ok {
					continue
				}
				ct, _ := c["type"].(string)
				text, _ := c["text"].(string)
				t.Logf("OUTPUT[%d].content[%d]: type=%q text=%q", i, j, ct, truncate(text, 200))
			}
		default:
			b, _ := json.Marshal(item)
			t.Logf("OUTPUT[%d]: type=%q raw=%s", i, typ, truncate(string(b), 300))
		}
	}
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// approveRejectTools returns the minimal approve+reject tool definitions.
func approveRejectTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"name": "approve",
			"description": "Approve the work. Call when the agent's implementation meets the task requirements.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "Brief summary of what you verified"},
				},
				"required": []any{"message"},
			},
			"strict": true,
		},
		map[string]any{
			"type": "function",
			"name": "reject",
			"description": "Reject the work. Call when the agent's implementation has issues that must be fixed.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"feedback": map[string]any{"type": "string", "description": "Specific issues with file paths and evidence"},
				},
				"required": []any{"feedback"},
			},
			"strict": true,
		},
	}
}

// fullReviewerToolSet returns the same tools serf registers for a reviewer
// subagent: read_file, shell, glob, grep + approve + reject.
func fullReviewerToolSet() []any {
	return []any{
		// read_file
		map[string]any{
			"type":        "function",
			"name":        "read_file",
			"description": "Read a file from the filesystem. Returns line-numbered content for text files.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string"},
					"offset":    map[string]any{"type": "integer"},
					"limit":     map[string]any{"type": "integer"},
				},
				"required": []any{"file_path", "offset", "limit"},
			},
			"strict": true,
		},
		// shell
		map[string]any{
			"type":        "function",
			"name":        "shell",
			"description": "Execute a shell command. Returns stdout, stderr, and exit code.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"command":     map[string]any{"type": "string"},
					"timeout_ms":  map[string]any{"type": "integer"},
					"description": map[string]any{"type": "string"},
				},
				"required": []any{"command", "timeout_ms", "description"},
			},
			"strict": true,
		},
		// glob
		map[string]any{
			"type":        "function",
			"name":        "glob",
			"description": "Find files matching a glob pattern.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string"},
					"path":    map[string]any{"type": "string"},
				},
				"required": []any{"pattern", "path"},
			},
			"strict": true,
		},
		// grep
		map[string]any{
			"type":        "function",
			"name":        "grep",
			"description": "Search file contents using regex patterns.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"pattern":          map[string]any{"type": "string"},
					"path":             map[string]any{"type": "string"},
					"glob_filter":      map[string]any{"type": "string"},
					"case_insensitive": map[string]any{"type": "boolean"},
					"max_results":      map[string]any{"type": "integer"},
					"output_mode": map[string]any{
						"type": "string",
						"enum": []any{"content", "files_with_matches", "count"},
					},
				},
				"required": []any{"pattern", "path", "glob_filter", "case_insensitive", "max_results", "output_mode"},
			},
			"strict": true,
		},
		// approve
		map[string]any{
			"type":        "function",
			"name":        "approve",
			"description": "Approve the work. Call when the agent's implementation meets the task requirements.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "Brief summary of what you verified"},
				},
				"required": []any{"message"},
			},
			"strict": true,
		},
		// reject
		map[string]any{
			"type":        "function",
			"name":        "reject",
			"description": "Reject the work. Call when the agent's implementation has issues that must be fixed.",
			"parameters": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"feedback": map[string]any{"type": "string", "description": "Specific issues with file paths and evidence"},
				},
				"required": []any{"feedback"},
			},
			"strict": true,
		},
	}
}

// reviewerSystemPrompt is the full reviewer.md prompt, embedded verbatim.
const reviewerSystemPrompt = `You are an adversarial code reviewer. Your job is to catch implementations that are
broken, lazy, or dishonest. You are the last line of defense before code ships.

## What to Check

Given the spec, tests, and implementation, check for:

### 1. Stubs and Placeholders
- Functions that return hardcoded values instead of computing results
- TODO comments or placeholder logic
- Functions with empty bodies or trivial implementations
- Code that compiles but doesn't actually do anything meaningful

### 2. Hardcoded Outputs
- Strings or values that appear to match expected test outputs literally
- Output that doesn't change when input changes
- Magic numbers or strings that correspond to test expectations

### 3. Test Gaming
- Code that detects it's being tested and behaves differently
- Implementations that satisfy the letter of the tests but not the spirit
- Code that works for specific test inputs but would fail on other valid inputs

### 4. Input Data Ignored
- Files opened but never read
- Data loaded but never used in computation
- Arguments accepted but not processed

### 5. Spec Violations
- Requirements listed in the spec but not implemented
- Behavior that contradicts the spec
- Edge cases mentioned in the spec but not handled

### 6. Correctness Issues
- Off-by-one errors, overflow risks, uninitialized variables
- Logic errors visible from code reading
- Missing error handling for likely failure modes

## How to Review

1. Read the spec to understand what was required.
2. Read the tests to understand what is being verified.
3. Read the implementation thoroughly — every line.
4. Run the code mentally or trace through it with test inputs.
5. Look for gaps between what the spec requires and what the code does.

## Verdict — MANDATORY TOOL CALL

**You MUST deliver your verdict by calling one of these tools. Do NOT write your verdict as text.**

- **approve** — Call when the work meets the task requirements.
- **reject** — Call when the work has issues that must be fixed.

You cannot complete your review without calling one of these tools. Text responses are not accepted as verdicts.

When rejecting, include specific issues with file paths and evidence in the ` + "`feedback`" + ` field.
When approving, briefly confirm what you verified in the ` + "`message`" + ` field.

Be direct. Do not soften failures.`

// simpleSystemPrompt is a shorter prompt for the first two subtests.
const simpleSystemPrompt = "You are a code reviewer. You MUST deliver your verdict by calling either the approve or reject tool. Do NOT write your verdict as text."

const reviewUserMessage = "The task was to write a hello world program. The implementation prints 'Hello, World!' to stdout. Review and deliver your verdict."

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReviewerToolCalling(t *testing.T) {
	apiKey := apiKeyOrSkip(t)

	t.Run("tool_choice_required", func(t *testing.T) {
		body := responsesRequest{
			Model: testModel,
			Store: false,
			Input: []any{
				map[string]any{
					"type": "message",
					"role": "developer",
					"content": []any{
						map[string]any{"type": "input_text", "text": simpleSystemPrompt},
					},
				},
				map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{
						map[string]any{"type": "input_text", "text": reviewUserMessage},
					},
				},
			},
			Tools:      approveRejectTools(),
			ToolChoice: "required",
		}

		resp := callResponsesAPI(t, apiKey, body)
		logOutputItems(t, resp)

		calls := extractToolCalls(t, resp)
		if len(calls) == 0 {
			t.Fatal("model did NOT make any tool calls with tool_choice=required")
		}

		name, _ := calls[0]["name"].(string)
		t.Logf("TOOL CALLED: %s", name)
		if name != "approve" && name != "reject" {
			t.Errorf("unexpected tool name: %q (expected approve or reject)", name)
		}

		// Parse and log the arguments.
		argsStr, _ := calls[0]["arguments"].(string)
		if argsStr != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
				t.Logf("TOOL ARGS: %v", args)
			}
		}
	})

	t.Run("tool_choice_auto", func(t *testing.T) {
		body := responsesRequest{
			Model: testModel,
			Store: false,
			Input: []any{
				map[string]any{
					"type": "message",
					"role": "developer",
					"content": []any{
						map[string]any{"type": "input_text", "text": simpleSystemPrompt},
					},
				},
				map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{
						map[string]any{"type": "input_text", "text": reviewUserMessage},
					},
				},
			},
			Tools:      approveRejectTools(),
			ToolChoice: "auto",
		}

		resp := callResponsesAPI(t, apiKey, body)
		logOutputItems(t, resp)

		calls := extractToolCalls(t, resp)
		if len(calls) == 0 {
			t.Log("WARNING: model did NOT call any tool with tool_choice=auto")
			t.Log("The model may have responded with text instead of a tool call.")

			// Log any text output for debugging.
			if output, ok := resp["output"].([]any); ok {
				for _, itemAny := range output {
					item, ok := itemAny.(map[string]any)
					if !ok {
						continue
					}
					if typ, _ := item["type"].(string); typ == "message" {
						if content, ok := item["content"].([]any); ok {
							for _, cAny := range content {
								c, ok := cAny.(map[string]any)
								if !ok {
									continue
								}
								text, _ := c["text"].(string)
								if text != "" {
									t.Logf("MODEL TEXT: %s", text)
								}
							}
						}
					}
				}
			}
			// This is the key question: does the model voluntarily call tools?
			// A failure here is informative, not a test bug.
			t.Fatal("model did not voluntarily call approve/reject with tool_choice=auto")
		}

		name, _ := calls[0]["name"].(string)
		t.Logf("TOOL CALLED (auto): %s", name)
		if name != "approve" && name != "reject" {
			t.Errorf("unexpected tool name: %q (expected approve or reject)", name)
		}

		argsStr, _ := calls[0]["arguments"].(string)
		if argsStr != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
				t.Logf("TOOL ARGS: %v", args)
			}
		}
	})

	t.Run("full_reviewer_toolset", func(t *testing.T) {
		// This mimics exactly what serf sends: the full reviewer tool list
		// with the full reviewer.md system prompt.
		body := responsesRequest{
			Model: testModel,
			Store: false,
			Input: []any{
				map[string]any{
					"type": "message",
					"role": "developer",
					"content": []any{
						map[string]any{"type": "input_text", "text": reviewerSystemPrompt},
					},
				},
				map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{
						map[string]any{"type": "input_text", "text": fmt.Sprintf(
							"Review the following implementation.\n\n"+
								"## Spec\nWrite a program that prints 'Hello, World!' to stdout.\n\n"+
								"## Tests\n```\ndef test_hello():\n    assert run_program() == 'Hello, World!\\n'\n```\n\n"+
								"## Implementation\n```python\nprint('Hello, World!')\n```\n\n"+
								"Deliver your verdict by calling the approve or reject tool.",
						)},
					},
				},
			},
			Tools:      fullReviewerToolSet(),
			ToolChoice: "auto",
		}

		resp := callResponsesAPI(t, apiKey, body)
		logOutputItems(t, resp)

		calls := extractToolCalls(t, resp)

		// Log every tool call, not just the first.
		for i, call := range calls {
			name, _ := call["name"].(string)
			argsStr, _ := call["arguments"].(string)
			t.Logf("TOOL CALL[%d]: name=%q args=%s", i, name, argsStr)
		}

		if len(calls) == 0 {
			t.Log("WARNING: model did NOT call any tool with full toolset + auto")

			// Log text output.
			if output, ok := resp["output"].([]any); ok {
				for _, itemAny := range output {
					item, ok := itemAny.(map[string]any)
					if !ok {
						continue
					}
					if typ, _ := item["type"].(string); typ == "message" {
						if content, ok := item["content"].([]any); ok {
							for _, cAny := range content {
								c, ok := cAny.(map[string]any)
								if !ok {
									continue
								}
								text, _ := c["text"].(string)
								if text != "" {
									t.Logf("MODEL TEXT: %s", text)
								}
							}
						}
					}
				}
			}
			t.Fatal("model did not call approve/reject among full reviewer toolset")
		}

		// Check whether any call is approve or reject.
		foundVerdict := false
		for _, call := range calls {
			name, _ := call["name"].(string)
			if name == "approve" || name == "reject" {
				foundVerdict = true
				t.Logf("VERDICT TOOL: %s", name)

				argsStr, _ := call["arguments"].(string)
				if argsStr != "" {
					var args map[string]any
					if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
						t.Logf("VERDICT ARGS: %v", args)
					}
				}
				break
			}
		}

		if !foundVerdict {
			calledNames := make([]string, len(calls))
			for i, call := range calls {
				calledNames[i], _ = call["name"].(string)
			}
			t.Fatalf("model called tools but NONE were approve/reject: %v", calledNames)
		}
	})
}
