package apptranscript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/llm"
)

// EntryProjector converts one transcript entry JSON object into AppWire items.
type EntryProjector func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem

// ImageProjector converts transcript image content into an AppWire image item.
type ImageProjector func(image llm.ImageData) appwire.InputItem

// ScanPrelude reads the transcript header and first API call, if present.
func ScanPrelude(path string, maxLineBytes int) (agent.TranscriptHeader, *agent.TranscriptAPICall) {
	f, err := os.Open(path)
	if err != nil {
		return agent.TranscriptHeader{}, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var header agent.TranscriptHeader
	for scanner.Scan() {
		raw := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		switch head.Kind {
		case "header":
			_ = json.Unmarshal(raw, &header)
		case "api_call":
			var call agent.TranscriptAPICall
			if err := json.Unmarshal(raw, &call); err == nil {
				return header, &call
			}
		}
	}
	return header, nil
}

// PreludeTurn projects transcript header/API-call context into the synthetic
// system turn shown before conversation entries.
func PreludeTurn(header agent.TranscriptHeader, firstCall *agent.TranscriptAPICall) *appwire.Turn {
	var items []appwire.ThreadItem
	systemPrompt := strings.TrimSpace(header.SystemPrompt)
	if systemPrompt == "" && firstCall != nil {
		systemPrompt = strings.TrimSpace(firstCall.SystemPrompt)
	}
	if systemPrompt != "" {
		items = append(items, appwire.ThreadItem{
			Type:        "systemMessage",
			ID:          "item_system_prompt",
			TurnID:      "turn_system",
			Description: "System prompt",
			Text:        systemPrompt,
			Status:      appwire.TurnStatusCompleted,
		})
	}
	if firstCall != nil {
		if tools := FormatTools(firstCall.Request); tools != "" {
			items = append(items, appwire.ThreadItem{
				Type:        "systemMessage",
				ID:          "item_tools",
				TurnID:      "turn_system",
				Description: fmt.Sprintf("Tools (%d)", len(firstCall.Request.Tools)),
				Text:        tools,
				Status:      appwire.TurnStatusCompleted,
			})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &appwire.Turn{ID: "turn_system", Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}
}

// FormatTools renders the full tool definitions presented to the LLM.
func FormatTools(req llm.APILogRequest) string {
	if len(req.Tools) == 0 {
		return ""
	}
	data, err := json.MarshalIndent(req.Tools, "", "  ")
	if err != nil {
		return ""
	}
	return "```json\n" + string(data) + "\n```"
}

// CompactionDescription returns the user-facing label for compaction turns.
func CompactionDescription(kind string) string {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case string(agent.TurnSummary):
		return "Context summary"
	default:
		return "Context checkpoint"
	}
}

// ImagePlaceholder returns compact transcript text for image-only messages.
func ImagePlaceholder(count int) string {
	switch count {
	case 0:
		return ""
	case 1:
		return "[image]"
	default:
		return fmt.Sprintf("[%d images]", count)
	}
}

// CommunicateMessageFromArguments extracts the user-facing message from a
// communicate tool call.
func CommunicateMessageFromArguments(raw json.RawMessage) string {
	var args struct {
		Message string `json:"message"`
		Output  *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	if msg := strings.TrimSpace(args.Message); msg != "" {
		return msg
	}
	if args.Output != nil {
		return strings.TrimSpace(args.Output.Message)
	}
	return ""
}

// ToolIntentFromArguments extracts a compact tool-call description from common
// intent fields.
func ToolIntentFromArguments(raw json.RawMessage) string {
	var args map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &args) != nil {
		return ""
	}
	for _, key := range []string{"intent", "purpose", "description"} {
		if value, ok := args[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// StringifyToolContent returns transcript text for arbitrary tool result
// content.
func StringifyToolContent(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// DefaultImageProjector preserves the image media type without exposing bytes
// in transcript list responses.
func DefaultImageProjector(image llm.ImageData) appwire.InputItem {
	return appwire.InputItem{
		Type:      "input_image",
		MediaType: image.MediaType,
		Name:      "",
	}
}

// ProjectTurn maps a typed transcript turn into AppWire transcript items.
func ProjectTurn(turnID string, turnIndex int, turn agent.Turn, toolNames map[string]string, imageProjector ImageProjector) []appwire.ThreadItem {
	if imageProjector == nil {
		imageProjector = DefaultImageProjector
	}
	if toolNames == nil {
		toolNames = map[string]string{}
	}
	switch turn.Kind {
	case agent.TurnCheckpoint, agent.TurnSummary:
		text := strings.TrimSpace(turn.Message.Text())
		if text == "" {
			return nil
		}
		return []appwire.ThreadItem{{
			Type:                 "systemMessage",
			ID:                   fmt.Sprintf("item_compaction_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Description:          CompactionDescription(string(turn.Kind)),
			Text:                 text,
			Status:               appwire.TurnStatusCompleted,
		}}
	case agent.TurnUserInput:
		images := ImagesFromContent(turn.Message.Content, imageProjector)
		return []appwire.ThreadItem{{
			Type:                 "userMessage",
			ID:                   fmt.Sprintf("item_user_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Text:                 turn.Message.Text(),
			Images:               images,
			Status:               appwire.TurnStatusCompleted,
		}}
	case agent.TurnSteering:
		images := ImagesFromContent(turn.Message.Content, imageProjector)
		text := turn.Message.Text()
		if text == "" && len(images) > 0 {
			text = ImagePlaceholder(len(images))
		}
		return []appwire.ThreadItem{{
			Type:   "steering",
			ID:     fmt.Sprintf("item_steering_%d", turnIndex),
			TurnID: turnID,
			Text:   text,
			Images: images,
			Status: appwire.TurnStatusCompleted,
		}}
	case agent.TurnAssistant:
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			switch part.Kind {
			case llm.ContentText:
				if part.Text != "" {
					items = append(items, appwire.ThreadItem{
						Type:   "agentMessage",
						ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
						TurnID: turnID,
						Text:   part.Text,
						Status: appwire.TurnStatusCompleted,
					})
				}
			case llm.ContentToolCall:
				if part.ToolCall == nil {
					continue
				}
				toolNames[part.ToolCall.ID] = part.ToolCall.Name
				if part.ToolCall.Name == "communicate" {
					if text := CommunicateMessageFromArguments(part.ToolCall.Arguments); text != "" {
						items = append(items, appwire.ThreadItem{
							Type:   "agentMessage",
							ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
							TurnID: turnID,
							Text:   text,
							Status: appwire.TurnStatusCompleted,
						})
					}
					continue
				}
				items = append(items, appwire.ThreadItem{
					Type:          "commandExecution",
					ID:            fmt.Sprintf("item_tool_%d_%d", turnIndex, i),
					TurnID:        turnID,
					ToolName:      part.ToolCall.Name,
					CallID:        part.ToolCall.ID,
					ArgumentsJSON: string(part.ToolCall.Arguments),
					Description:   ToolIntentFromArguments(part.ToolCall.Arguments),
					Status:        appwire.TurnStatusInProgress,
				})
			}
		}
		return items
	case agent.TurnTool, agent.TurnToolResults:
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			name := part.ToolResult.Name
			if name == "" {
				name = toolNames[part.ToolResult.ToolCallID]
			}
			if name == "communicate" {
				delete(toolNames, part.ToolResult.ToolCallID)
				continue
			}
			item := appwire.ThreadItem{
				Type:     "commandExecution",
				ID:       fmt.Sprintf("item_tool_result_%d_%d", turnIndex, i),
				TurnID:   turnID,
				ToolName: name,
				CallID:   part.ToolResult.ToolCallID,
				Status:   appwire.TurnStatusCompleted,
			}
			if part.ToolResult.IsError {
				item.Error = StringifyToolContent(part.ToolResult.Content)
			} else {
				item.Output = StringifyToolContent(part.ToolResult.Content)
			}
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

// ImagesFromContent maps image content parts into AppWire image items.
func ImagesFromContent(parts []llm.ContentPart, imageProjector ImageProjector) []appwire.InputItem {
	if imageProjector == nil {
		imageProjector = DefaultImageProjector
	}
	var images []appwire.InputItem
	for _, part := range parts {
		if part.Kind != llm.ContentImage || part.Image == nil || len(part.Image.Data) == 0 {
			continue
		}
		images = append(images, imageProjector(*part.Image))
	}
	return images
}

// TurnsFromFile projects a transcript JSONL file into AppWire turns.
func TurnsFromFile(path string, maxLineBytes int, project EntryProjector) []appwire.Turn {
	header, firstCall := ScanPrelude(path, maxLineBytes)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var turns []appwire.Turn
	preludeEmitted := false
	entryIndex := 0
	emitPrelude := func() {
		if preludeEmitted {
			return
		}
		preludeEmitted = true
		if prelude := PreludeTurn(header, firstCall); prelude != nil {
			turns = append(turns, *prelude)
		}
	}
	for scanner.Scan() {
		raw := json.RawMessage(append([]byte(nil), scanner.Bytes()...))
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		switch head.Kind {
		case "header":
			continue
		case "api_call":
			var call agent.TranscriptAPICall
			if err := json.Unmarshal(raw, &call); err == nil && strings.TrimSpace(call.Error) != "" {
				emitPrelude()
				info := diagnostic.FromFields(call.Source, call.Title, call.Hint, call.Error)
				entryIndex++
				turns = append(turns, appwire.Turn{
					ID:        fmt.Sprintf("turn_%d", entryIndex),
					ItemsView: "full",
					Status:    appwire.TurnStatusFailed,
					Error: &appwire.TurnError{
						Message: call.Error,
						Source:  string(info.Source),
						Title:   info.Title,
						Hint:    info.Hint,
					},
				})
			}
			continue
		case "entry":
			emitPrelude()
			entryIndex++
			turnID := fmt.Sprintf("turn_%d", entryIndex)
			var items []appwire.ThreadItem
			if project != nil {
				items = project(raw, turnID, entryIndex)
			}
			if len(items) > 0 {
				turns = append(turns, appwire.Turn{ID: turnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted})
			}
		}
	}
	emitPrelude()
	return turns
}
