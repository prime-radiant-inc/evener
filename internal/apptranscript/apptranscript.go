package apptranscript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/invariant"
	"primeradiant.com/serf/llm"
)

// EntryProjector converts one transcript entry JSON object into AppWire items.
type EntryProjector func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem

// ImageProjector converts transcript image content into an AppWire image item.
type ImageProjector func(image llm.ImageData) appwire.InputItem

// OutputImageProjector converts a transcript tool result into AppWire output
// image descriptors. The descriptors carry fetch metadata only, not image bytes.
type OutputImageProjector func(result *llm.ToolResultData) []appwire.OutputImage

// ScanPrelude reads the transcript header and first API call, if present.
func ScanPrelude(path string, maxLineBytes int) (transcript.Header, *transcript.APICall) {
	f, err := os.Open(path)
	if err != nil {
		return transcript.Header{}, nil
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	var header transcript.Header
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
			var call transcript.APICall
			if err := json.Unmarshal(raw, &call); err == nil {
				return header, &call
			}
		}
	}
	return header, nil
}

// PreludeTurn projects transcript header/API-call context into the synthetic
// system turn shown before conversation entries.
func PreludeTurn(header transcript.Header, firstCall *transcript.APICall) *appwire.Turn {
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
	case string(schema.TurnSummary):
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
func ProjectTurn(turnID string, turnIndex int, turn schema.Turn, toolNames map[string]string, imageProjector ImageProjector, outputImageProjector OutputImageProjector) (out []appwire.ThreadItem) {
	if imageProjector == nil {
		imageProjector = DefaultImageProjector
	}
	if toolNames == nil {
		toolNames = map[string]string{}
	}
	if invariant.Enabled {
		// Every item ProjectTurn emits belongs to the turn it was asked to
		// project (TurnID == the turnID argument) and carries a non-empty id the
		// client can address it by. Each construction below sets TurnID: turnID
		// and an item_..._N id, so these hold across all turn kinds.
		defer func() {
			for _, item := range out {
				invariant.Hold(item.TurnID == turnID,
					"apptranscript: ProjectTurn item %q has TurnID %q, want %q", item.ID, item.TurnID, turnID)
				invariant.Hold(item.ID != "",
					"apptranscript: ProjectTurn emitted item with empty ID (type=%s, turnID=%s)", item.Type, turnID)
			}
		}()
	}
	switch turn.Kind {
	case schema.TurnCheckpoint, schema.TurnSummary:
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
	case schema.TurnUserInput:
		images := ImagesFromContent(turn.Message.Content, imageProjector)
		images = append(images, AttachmentsFromContent(turn.Message.Content)...)
		return []appwire.ThreadItem{{
			Type:                 "userMessage",
			ID:                   fmt.Sprintf("item_user_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Text:                 turn.Message.Text(),
			Images:               images,
			Status:               appwire.TurnStatusCompleted,
		}}
	case schema.TurnSteering:
		images := ImagesFromContent(turn.Message.Content, imageProjector)
		text := turn.Message.Text()
		if text == "" && len(images) > 0 {
			text = ImagePlaceholder(len(images))
		}
		images = append(images, AttachmentsFromContent(turn.Message.Content)...)
		return []appwire.ThreadItem{{
			Type:   "steering",
			ID:     fmt.Sprintf("item_steering_%d", turnIndex),
			TurnID: turnID,
			Text:   text,
			Images: images,
			Status: appwire.TurnStatusCompleted,
		}}
	case schema.TurnAssistant:
		var items []appwire.ThreadItem
		// lastAssistantText mirrors the live projector's dedup: a communicate
		// tool call whose message echoes the assistant text already shown in this
		// turn is rendered once, not twice (see matchesLastAssistantMessage in
		// internal/appprojector). Reload must collapse the echo the same way.
		var lastAssistantText string
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
					lastAssistantText = strings.TrimSpace(part.Text)
				}
			case llm.ContentThinking:
				if part.Thinking != nil && part.Thinking.Text != "" {
					items = append(items, appwire.ThreadItem{
						Type:   "reasoning",
						ID:     fmt.Sprintf("item_reasoning_%d_%d", turnIndex, i),
						TurnID: turnID,
						Text:   part.Thinking.Text,
						Status: appwire.TurnStatusCompleted,
					})
				}
			case llm.ContentRedThinking:
				items = append(items, appwire.ThreadItem{
					Type:   "reasoning",
					ID:     fmt.Sprintf("item_reasoning_%d_%d", turnIndex, i),
					TurnID: turnID,
					Text:   "[redacted thinking]",
					Status: appwire.TurnStatusCompleted,
				})
			case llm.ContentWebSearch:
				if part.WebSearch == nil {
					continue
				}
				query, results := WebSearchProjection(part.WebSearch)
				args := "{}"
				if query != "" {
					if encoded, err := json.Marshal(map[string]string{"query": query}); err == nil {
						args = string(encoded)
					}
				}
				items = append(items, appwire.ThreadItem{
					Type:          "commandExecution",
					ID:            fmt.Sprintf("item_websearch_%d_%d", turnIndex, i),
					TurnID:        turnID,
					ToolName:      "web_search",
					ArgumentsJSON: args,
					Output:        results,
					Status:        appwire.TurnStatusCompleted,
				})
			case llm.ContentToolCall:
				if part.ToolCall == nil {
					continue
				}
				toolNames[part.ToolCall.ID] = part.ToolCall.Name
				if part.ToolCall.Name == "communicate" {
					if text := CommunicateMessageFromArguments(part.ToolCall.Arguments); text != "" && strings.TrimSpace(text) != lastAssistantText {
						items = append(items, appwire.ThreadItem{
							Type:   "agentMessage",
							ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
							TurnID: turnID,
							Text:   text,
							Status: appwire.TurnStatusCompleted,
						})
						lastAssistantText = strings.TrimSpace(text)
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
	case schema.TurnTool, schema.TurnToolResults:
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
				Raw:      part.ToolResult.ToolState,
			}
			if part.ToolResult.IsError {
				item.Error = StringifyToolContent(part.ToolResult.Content)
			} else {
				item.Output = StringifyToolContent(part.ToolResult.Content)
			}
			if outputImageProjector != nil {
				item.OutputImages = outputImageProjector(part.ToolResult)
			}
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

// AttachmentsFromContent maps non-image input attachments (audio, documents)
// into AppWire input items rendered as labeled chips. Images are handled
// separately by ImagesFromContent because they carry sha-addressed bytes the
// web UI fetches lazily; audio and documents have no byte-serving path, so a
// chip naming the file and media type is the honest representation.
func AttachmentsFromContent(parts []llm.ContentPart) []appwire.InputItem {
	var out []appwire.InputItem
	for _, part := range parts {
		switch part.Kind {
		case llm.ContentAudio:
			if part.Audio == nil {
				continue
			}
			out = append(out, appwire.InputItem{Type: "input_audio", MediaType: part.Audio.MediaType, URL: part.Audio.URL})
		case llm.ContentDocument:
			if part.Document == nil {
				continue
			}
			out = append(out, appwire.InputItem{Type: "input_document", MediaType: part.Document.MediaType, Name: part.Document.FileName, URL: part.Document.URL})
		}
	}
	return out
}

// webSearchRaw captures the provider-native web-search payload shapes serf's
// adapters store in WebSearchData.Raw: OpenAI's web_search_call (action.query),
// Anthropic's server_tool_use (input.query) and web_search_tool_result
// (content[]), and Gemini's grounding metadata (webSearchQueries +
// groundingChunks[]).
type webSearchRaw struct {
	Action struct{ Query string } `json:"action"`
	Input  struct{ Query string } `json:"input"`
	// serf:naming-ignore: Gemini grounding-metadata wire field name (camelCase, fixed by the Gemini API).
	WebSearchQueries []string `json:"webSearchQueries"`
	// serf:naming-ignore: Gemini grounding-metadata wire field name (camelCase, fixed by the Gemini API).
	GroundingChunks []struct {
		Web struct {
			URI   string `json:"uri"`
			Title string `json:"title"`
		} `json:"web"`
	} `json:"groundingChunks"`
	Content []struct {
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"content"`
}

// WebSearchProjection derives a display query and newline-separated result
// lines ("Title — URL") from a provider-native web-search content part, so it
// can render through the existing web_search tool card. Unrecognized payloads
// still yield the part's Query, keeping the search visible.
func WebSearchProjection(ws *llm.WebSearchData) (query string, results string) {
	if ws == nil {
		return "", ""
	}
	query = strings.TrimSpace(ws.Query)
	var raw webSearchRaw
	if len(ws.Raw) > 0 {
		_ = json.Unmarshal(ws.Raw, &raw)
	}
	if query == "" {
		query = firstNonEmpty(raw.Action.Query, raw.Input.Query, strings.Join(raw.WebSearchQueries, "; "))
	}
	var lines []string
	for _, chunk := range raw.GroundingChunks {
		if line := webSearchResultLine(chunk.Web.Title, chunk.Web.URI); line != "" {
			lines = append(lines, line)
		}
	}
	for _, c := range raw.Content {
		if c.Type != "" && c.Type != "web_search_result" {
			continue
		}
		if line := webSearchResultLine(c.Title, c.URL); line != "" {
			lines = append(lines, line)
		}
	}
	return query, strings.Join(lines, "\n")
}

func webSearchResultLine(title, url string) string {
	title = strings.TrimSpace(title)
	url = strings.TrimSpace(url)
	switch {
	case title != "" && url != "":
		return title + " — " + url
	case title != "":
		return title
	default:
		return url
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable

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
			var call transcript.APICall
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
				turn := appwire.Turn{ID: turnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}
				// Stamp StartedAt from the entry's recorded timestamp; DurationMS
				// stays nil because a message record captures a point in time, not
				// a span (unlike the live projector's EventTurnEnded timing).
				var entry transcript.Entry
				if json.Unmarshal(raw, &entry) == nil {
					if !entry.Turn.Timestamp.IsZero() {
						startedAt := entry.Turn.Timestamp.Unix()
						turn.StartedAt = &startedAt
					}
					if usage := appwire.SerfUsageFromLLM(entry.Turn.Usage); usage != nil {
						turn.Usage = usage
					}
				}
				turns = append(turns, turn)
			}
		}
	}
	emitPrelude()
	return turns
}
