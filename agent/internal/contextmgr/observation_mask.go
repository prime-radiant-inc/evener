package contextmgr

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/searchresult"
	"primeradiant.com/evener/llm"
)

// maskObservations replaces tool result content in old turns with one-line summaries.
// Preserves: error results, communicate tool results, already-masked results,
// recent turns, and results where the summary would be longer than the original.
func maskObservations(history []schema.Turn, preserveRecent int, resultToolName string) {
	if len(history) == 0 {
		return
	}

	cutoff := attentionTransparentRecentCutoff(history, preserveRecent)
	if cutoff <= 0 {
		return
	}
	for i := range cutoff {
		t := &history[i]
		if t.Kind != schema.TurnTool && t.Kind != schema.TurnToolResults {
			continue
		}
		// Copy-on-write: the caller's history is typically a fold snapshot
		// whose Turn structs share Message.Content backing arrays and
		// *ToolResultData payloads with the live s.history. Writing through
		// them would leak a LOSING fold's masking into the live history, so
		// a turn that masks anything gets a fresh Content slice and fresh
		// ToolResultData; the shared originals are never written.
		var rebuilt []llm.ContentPart
		for j := range t.Message.Content {
			p := t.Message.Content[j]
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			tr := p.ToolResult

			// Find the tool call arguments from the preceding assistant turn.
			args := findToolCallArgs(history[:i], tr.ToolCallID)

			if newContent, mask := maskToolResultContent(tr, resultToolName, summarizeToolResult, args); mask {
				if rebuilt == nil {
					rebuilt = append([]llm.ContentPart(nil), t.Message.Content...)
				}
				masked := *tr
				masked.Content = newContent
				rebuilt[j].ToolResult = &masked
			}
		}
		if rebuilt != nil {
			t.Message.Content = rebuilt
		}
	}
}

// maskToolResultContent decides whether a tool result's content should be
// replaced with a one-line summary, and computes that summary. It preserves
// error results, result-tool results, non-string content, and already-masked
// content ("[…]"); it also declines to mask when the summary would not be
// strictly shorter than the original. summarize is injected (summarizeToolResult)
// and args are the matching tool-call arguments. When mask is false newContent
// is empty and the caller leaves the result untouched.
func maskToolResultContent(tr *llm.ToolResultData, resultToolName string, summarize func(name string, content any, isError bool, args json.RawMessage) string, args json.RawMessage) (newContent string, mask bool) {
	// Never mask error results.
	if tr.IsError {
		return "", false
	}
	// Never mask result tool results.
	if tr.Name == resultToolName {
		return "", false
	}

	content, ok := tr.Content.(string)
	if !ok {
		return "", false
	}
	// Skip already-masked results (start with "[").
	if strings.HasPrefix(content, "[") && strings.HasSuffix(strings.TrimSpace(content), "]") {
		return "", false
	}

	summary := summarize(tr.Name, content, tr.IsError, args)
	// Skip masking if the summary is not shorter than the original.
	if len(summary) >= len(content) {
		return "", false
	}
	return summary, true
}

// summarizeToolResult generates a one-line summary for a tool result.
func summarizeToolResult(toolName string, content any, isError bool, args json.RawMessage) string {
	contentStr := fmt.Sprint(content)
	var argsMap map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argsMap)
	}

	getArg := func(key string) string {
		if argsMap == nil {
			return ""
		}
		if v, ok := argsMap[key]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}

	// Match the breaker's exact leading parkPrefix (agent/internal/tool/breaker.go).
	// Successful search data can start with the same bytes, so require its error flag.
	if isError && (toolName == "grep" || toolName == "glob" || toolName == "memory_search") &&
		strings.HasPrefix(contentStr, "evener did not execute this call: ") {
		return searchSummary(fmt.Sprintf("%s: %q → refused", toolName, getArg("pattern")), contentStr)
	}

	switch toolName {
	case "read_file":
		path := getArg("file_path")
		lines := countLines(contentStr)
		return fmt.Sprintf("[read_file: %s, %d lines]", path, lines)

	case "shell":
		cmd := getArg("command")
		cmd = truncate(cmd, 60)
		exitCode := parseExitCode(contentStr)
		return fmt.Sprintf("[shell: %q → exit %s]", cmd, exitCode)

	case "grep", "memory_search":
		pattern := getArg("pattern")
		matches := searchresult.EntryCount(contentStr)
		return searchSummary(fmt.Sprintf("%s: %q → %d matches", toolName, pattern, matches), contentStr)

	case "glob":
		pattern := getArg("pattern")
		files := searchresult.EntryCount(contentStr)
		return searchSummary(fmt.Sprintf("glob: %q → %d files", pattern, files), contentStr)

	case "edit_file":
		path := getArg("file_path")
		return fmt.Sprintf("[edit_file: %s → OK]", path)

	case "apply_patch":
		return "[apply_patch → OK]"

	case "write_file":
		path := getArg("file_path")
		return fmt.Sprintf("[write_file: %s → OK]", path)

	case "web_fetch":
		url := getArg("url")
		return fmt.Sprintf("[web_fetch: %s → %d chars]", url, len(contentStr))

	case "delegate":
		jobID := extractJSONField(contentStr, "job_id")
		if jobID != "" {
			return fmt.Sprintf("[delegate: %s]", jobID)
		}
		return fmt.Sprintf("[delegate: %d chars]", len(contentStr))

	case "task_list":
		// Presence-based dispatch: derive the verb from whichever array the
		// call carried with at least one entry (add/update, either or both;
		// none = view). getArg stringifies, so an empty array would read as
		// present — inspect argsMap directly instead.
		verb := "view"
		hasAdd, hasUpdate := false, false
		if argsMap != nil {
			if arr, ok := argsMap["add"].([]any); ok && len(arr) > 0 {
				hasAdd = true
			}
			if arr, ok := argsMap["update"].([]any); ok && len(arr) > 0 {
				hasUpdate = true
			}
		}
		switch {
		case hasAdd && hasUpdate:
			verb = "add+update"
		case hasAdd:
			verb = "add"
		case hasUpdate:
			verb = "update"
		}
		tasks := countJSONArrayElements(contentStr)
		return fmt.Sprintf("[task_list: %s → %d tasks]", verb, tasks)

	case "use_skill":
		name := getArg("skill_name")
		return fmt.Sprintf("[use_skill: %s → %d chars]", name, len(contentStr))

	case "communicate":
		// Should never reach here (masked in caller), but be safe.
		return fmt.Sprintf("[communicate: %d chars]", len(contentStr))

	default:
		return fmt.Sprintf("[%s: %d chars]", toolName, len(contentStr))
	}
}

// clearThinking removes thinking text from old assistant turns, replacing it
// with a placeholder. Redacted thinking blocks are left untouched.
func clearThinking(history []schema.Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := attentionTransparentRecentCutoff(history, preserveRecent)
	if cutoff <= 0 {
		return
	}

	for i := range cutoff {
		t := &history[i]
		if t.Kind != schema.TurnAssistant {
			continue
		}
		// Copy-on-write over the shared *ThinkingData payloads, for the same
		// losing-fold leak maskObservations documents.
		var rebuilt []llm.ContentPart
		for j := range t.Message.Content {
			p := t.Message.Content[j]
			if p.Kind != llm.ContentThinking || p.Thinking == nil {
				continue
			}
			// Skip redacted thinking blocks.
			if p.Thinking.Redacted {
				continue
			}
			// Skip already-cleared thinking.
			if strings.HasPrefix(p.Thinking.Text, "[thinking:") {
				continue
			}
			oldLen := len(p.Thinking.Text)
			if oldLen == 0 {
				continue
			}
			if rebuilt == nil {
				rebuilt = append([]llm.ContentPart(nil), t.Message.Content...)
			}
			cleared := *p.Thinking
			cleared.Text = fmt.Sprintf("[thinking: %d chars]", oldLen)
			rebuilt[j].Thinking = &cleared
		}
		if rebuilt != nil {
			t.Message.Content = rebuilt
		}
	}
}
