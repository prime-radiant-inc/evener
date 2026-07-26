package apptranscript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/invariant"
	"primeradiant.com/serf/llm"
)

// EntryProjector converts one already-decoded transcript turn into AppWire
// items. The caller (TurnsFromFile, the turn index's scan/range readers) has
// already decoded the entry's raw JSON once — for its own Usage/Timestamp/
// failure stamping — so the projector receives that same schema.Turn directly
// rather than decoding the entry a second time (kata j13r).
type EntryProjector func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem

// BoundedEntryProjector converts one already-decoded transcript turn into
// AppWire items. The supported bounded-reader contract is a named adapter that
// calls ProjectTurn. Its toolNames argument is mutable, ephemeral state for
// that record only; callers must not inspect unrelated history.
// EntryProjector remains the full-read contract for existing callers.
type BoundedEntryProjector func(turn schema.Turn, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem

// ImageProjector converts transcript image content into an AppWire image item.
type ImageProjector func(image llm.ImageData) appwire.InputItem

// OutputImageProjector converts a transcript tool result into AppWire output
// image descriptors. The descriptors carry fetch metadata only, not image bytes.
type OutputImageProjector func(result *llm.ToolResultData) []appwire.OutputImage

// ScanPrelude validates the full semantic transcript and returns its v2 header.
func ScanPrelude(path string, maxLineBytes int) (transcript.Header, error) {
	return scanSemanticTranscript(path, maxLineBytes, nil)
}

// PreludeTurn projects semantic transcript header context into the synthetic
// system turn shown before conversation entries.
func PreludeTurn(header transcript.Header) *appwire.Turn {
	var items []appwire.ThreadItem
	systemPrompt := strings.TrimSpace(header.SystemPrompt)
	if systemPrompt != "" {
		items = append(items, appwire.ThreadItem{
			Type:        "systemMessage",
			ID:          "item_system_prompt",
			TurnID:      appwire.SystemPreludeTurnID,
			Description: "System prompt",
			Text:        systemPrompt,
			Status:      appwire.TurnStatusCompleted,
			EventKind:   appwire.ThreadItemEventKindSystemPrompt,
		})
	}
	if len(items) == 0 {
		return nil
	}
	return &appwire.Turn{ID: appwire.SystemPreludeTurnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}
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

// ExitCodeFromToolState extracts a shell tool call's process exit code from
// its raw ToolState JSON snapshot (the "exit_code" field of shellToolResult,
// agent/session_tools_shell.go:483), so the live projector and transcript
// reload can both promote it onto the settled commandExecution item's typed
// ExitCode field (wire-honesty spec Part A). Returns nil when the snapshot is
// empty, unparseable, or simply omits the field (any non-shell tool) — the
// absence is honest, never fabricated as zero.
//
// The read itself belongs to the transcript package, which owns the failure
// rule this exit code feeds: an exit code read one way here and another way
// where the failure count is computed is a row whose glyph and whose tally
// disagree.
func ExitCodeFromToolState(raw json.RawMessage) *int64 {
	return transcript.ExitCodeFromToolState(raw)
}

// SettledToolStatus is the wire-honest terminal status for a settled
// commandExecution item: TurnStatusFailed when the tool result carries an
// error, TurnStatusCompleted otherwise. Both the live projector
// (EventToolCallEnd, internal/appprojector/appwire_projection.go) and reload
// (ProjectTurn's TurnToolResults case below) call this, so a settled item's
// Status agrees with its own Error field instead of unconditionally claiming
// "completed" regardless of outcome — previously clients had to infer error
// state by checking Error's presence instead of trusting Status.
func SettledToolStatus(isError bool) string {
	if isError {
		return appwire.TurnStatusFailed
	}
	return appwire.TurnStatusCompleted
}

// FailedTurnFallbackText is what a persisted turn failure reads as when it
// carries no diagnostic text of its own. Saying the turn failed without
// detail still beats the old behaviour, where the failure left no trace and
// the session read as a hang.
const FailedTurnFallbackText = "The turn failed."

// StampTurnFailure applies a persisted TurnFailure entry's terminal status and
// diagnostic to the reloaded turn that wraps it, so a reloaded failure carries
// the same status/error shape the live NotifyTurnCompleted did instead of the
// blanket "completed" every reloaded turn otherwise claims. Both bounded and
// whole-file reads call this, so the two paths cannot disagree. Non-failure
// entries are left untouched.
func StampTurnFailure(turn *appwire.Turn, entryTurn schema.Turn) {
	if turn == nil || entryTurn.Kind != schema.TurnFailure {
		return
	}
	turn.Status = appwire.TurnStatusFailed
	info := entryTurn.Error
	if info == nil {
		message := strings.TrimSpace(entryTurn.Message.Text())
		if message == "" {
			message = FailedTurnFallbackText
		}
		turn.Error = &appwire.TurnError{Message: message}
		return
	}
	turnError := &appwire.TurnError{
		Message: strings.TrimSpace(info.Message),
		Source:  info.Source,
		Title:   info.Title,
		Hint:    info.Hint,
	}
	if turnError.Message == "" {
		turnError.Message = FailedTurnFallbackText
	}
	if info.Cause != nil {
		turnError.Cause = &appwire.DiagnosticCause{
			Kind:     info.Cause.Kind,
			Provider: info.Cause.Provider,
			Model:    info.Cause.Model,
			Status:   info.Cause.Status,
		}
	}
	turn.Error = turnError
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
			EventKind:            appwire.ThreadItemEventKindCompaction,
		}}
	case schema.TurnModelSwitch:
		text := strings.TrimSpace(turn.Message.Text())
		if text == "" {
			return nil
		}
		return []appwire.ThreadItem{{
			Type:                 "systemMessage",
			ID:                   fmt.Sprintf("item_model_switch_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Description:          "Model switch",
			Text:                 text,
			Status:               appwire.TurnStatusCompleted,
			EventKind:            appwire.ThreadItemEventKindModelSwitch,
		}}
	case schema.TurnFailure:
		// Unlike the marker kinds above, a failure with no text still renders:
		// the whole point of persisting it is that a returning reader can tell
		// a broken turn from an unanswered one (kata mcgh).
		text := strings.TrimSpace(turn.Message.Text())
		if turn.Error != nil && strings.TrimSpace(turn.Error.Message) != "" {
			text = strings.TrimSpace(turn.Error.Message)
		}
		if text == "" {
			text = FailedTurnFallbackText
		}
		return []appwire.ThreadItem{{
			Type:                 "systemMessage",
			ID:                   fmt.Sprintf("item_turn_failure_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Description:          "Turn failed",
			Text:                 text,
			Error:                text,
			Status:               appwire.TurnStatusFailed,
			EventKind:            appwire.ThreadItemEventKindError,
		}}
	case schema.TurnHookCompleted:
		// The reload counterpart of the live projector's hook_completed
		// systemMessage. The typed exit code rides ThreadItem.ExitCode, which
		// is what Settings → Transcript's two hook-exit toggles split on —
		// without this case they governed nothing at all after a reload
		// (kata qm9y).
		text := strings.TrimSpace(turn.Message.Text())
		if turn.Hook != nil && text == "" {
			text = turn.Hook.Announcement()
		}
		if text == "" {
			return nil
		}
		item := appwire.ThreadItem{
			Type:                 "systemMessage",
			ID:                   fmt.Sprintf("item_hook_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Description:          "Hook",
			Text:                 text,
			Status:               appwire.TurnStatusCompleted,
			EventKind:            appwire.ThreadItemEventKindHookCompleted,
		}
		// Absent, never a fabricated zero: a nil ExitCode means this entry
		// records no code, which the normal-only toggle deliberately hides.
		if turn.Hook != nil {
			code := int64(turn.Hook.ExitCode)
			item.ExitCode = &code
		}
		return []appwire.ThreadItem{item}
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
			// SteeringSource persists who sent the steering (issue #24):
			// "user" reaches the web UI so reload renders human-sent
			// steering as a user message, matching the live path.
			Source:       turn.SteeringSource,
			SteeringKind: turn.SteeringKind,
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
				item := appwire.ThreadItem{
					Type:          "commandExecution",
					ID:            fmt.Sprintf("item_tool_%d_%d", turnIndex, i),
					TurnID:        turnID,
					ToolName:      part.ToolCall.Name,
					CallID:        part.ToolCall.ID,
					ArgumentsJSON: string(part.ToolCall.Arguments),
					Description:   ToolIntentFromArguments(part.ToolCall.Arguments),
					Status:        appwire.TurnStatusInProgress,
				}
				// The entry's recorded timestamp is the server truth for when the
				// call was issued (issue #37); a zero timestamp mints no stamp.
				if !turn.Timestamp.IsZero() {
					ms := turn.Timestamp.UnixMilli()
					item.StartedAt = &ms
				}
				items = append(items, item)
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
				Status:   SettledToolStatus(part.ToolResult.IsError),
				Raw:      part.ToolResult.ToolState,
				ExitCode: ExitCodeFromToolState(part.ToolResult.ToolState),
			}
			// The entry's recorded timestamp is the server truth for when the
			// result landed (issue #37); a zero timestamp mints no stamp. The
			// matching StartedAt rides the earlier assistant entry's item; the
			// client merges the two by call id.
			if !turn.Timestamp.IsZero() {
				ms := turn.Timestamp.UnixMilli()
				item.CompletedAt = &ms
			}
			if part.ToolResult.IsError {
				item.Error = StringifyToolContent(part.ToolResult.Content)
				item.PrevalOnly = part.ToolResult.PrevalOnly
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

// TurnsFromFile projects a semantic transcript-v2 JSONL file into AppWire turns.
func TurnsFromFile(path string, maxLineBytes int, project EntryProjector) ([]appwire.Turn, error) {
	var turns []appwire.Turn
	preludeEmitted := false
	entryIndex := 0
	header, err := ScanPrelude(path, maxLineBytes)
	if err != nil {
		return nil, err
	}
	emitPrelude := func() {
		if preludeEmitted {
			return
		}
		preludeEmitted = true
		if prelude := PreludeTurn(header); prelude != nil {
			turns = append(turns, *prelude)
		}
	}
	_, err = scanSemanticTranscript(path, maxLineBytes, func(raw json.RawMessage) error {
		emitPrelude()
		entryIndex++
		turnID := fmt.Sprintf("turn_%d", entryIndex)
		// A malformed entry decodes to neither a projection nor a stamp: skip it
		// (matching the pre-fix behavior, where a projector's own internal decode
		// of the same malformed bytes would likewise fail and yield no items)
		// rather than aborting the whole read over one bad line.
		entry, decodeErr := transcript.DecodeEntry(raw)
		if decodeErr != nil {
			return nil //nolint:nilerr // skip a malformed entry rather than aborting the whole read over one bad line
		}
		var items []appwire.ThreadItem
		if project != nil {
			items = project(entry.Turn, turnID, entryIndex)
		}
		if len(items) == 0 {
			return nil
		}
		turn := appwire.Turn{ID: turnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted}
		StampTurnFailure(&turn, entry.Turn)
		if !entry.Turn.Timestamp.IsZero() {
			startedAt := entry.Turn.Timestamp.UnixMilli()
			turn.StartedAt = &startedAt
		}
		if usage := appwire.SerfUsageFromLLM(entry.Turn.Usage); usage != nil {
			turn.Usage = usage
		}
		turns = append(turns, turn)
		return nil
	})
	if err != nil {
		return nil, err
	}
	emitPrelude()
	return turns, nil
}

func scanSemanticTranscript(path string, maxLineBytes int, visit func(json.RawMessage) error) (transcript.Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return transcript.Header{}, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable
	if maxLineBytes <= 0 {
		maxLineBytes = 128 << 20
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	var header transcript.Header
	headerRead := false
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, maxLineBytes)
		if readErr != nil {
			return transcript.Header{}, readErr
		}
		if !complete {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			header, err = transcript.DecodeHeader(line)
			if err != nil {
				return transcript.Header{}, fmt.Errorf("parse transcript header: %w", err)
			}
			headerRead = true
			continue
		}
		if _, err := transcript.DecodeEntry(line); err != nil {
			return transcript.Header{}, fmt.Errorf("parse transcript entry: %w", err)
		}
		if visit != nil {
			if err := visit(json.RawMessage(append([]byte(nil), line...))); err != nil {
				return transcript.Header{}, err
			}
		}
	}
	if !headerRead {
		return transcript.Header{}, fmt.Errorf("%w: missing transcript header", transcript.ErrUnsupportedFormat)
	}
	return header, nil
}
