package appwire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// ThreadContentParams selects transcript content for reads, backfill and the
// subscription established by a read. Omitted fields include their category.
// These controls reduce content, not authority, delivery receipts or access.
type ThreadContentParams struct {
	// IncludeConversation includes user/assistant messages and human steering.
	IncludeConversation *bool `json:"includeConversation,omitempty"`
	// IncludeThinking includes reasoning items.
	IncludeThinking *bool `json:"includeThinking,omitempty"`
	// IncludeToolCalls includes tool arguments and results together.
	IncludeToolCalls *bool `json:"includeToolCalls,omitempty"`
	// IncludeToolCallIntents includes explicit tool descriptions. Without tool
	// calls, only their identity, description and lifecycle metadata are sent.
	IncludeToolCallIntents *bool `json:"includeToolCallIntents,omitempty"`
	// IncludeSystemMessages includes system notices and non-human steering.
	IncludeSystemMessages *bool `json:"includeSystemMessages,omitempty"`
	// IncludeDiagnostics includes detailed session diagnostics.
	IncludeDiagnostics *bool `json:"includeDiagnostics,omitempty"`
}

// ThreadContentSelection is the normalized set of excluded categories. Zero
// includes everything, making omitted and explicit-true options identical.
type ThreadContentSelection uint8

const (
	excludeConversation ThreadContentSelection = 1 << iota
	excludeThinking
	excludeToolCalls
	excludeToolCallIntents
	excludeSystemMessages
	excludeDiagnostics
)

func (p ThreadContentParams) Selection() ThreadContentSelection {
	var selection ThreadContentSelection
	for i, value := range []*bool{p.IncludeConversation, p.IncludeThinking, p.IncludeToolCalls, p.IncludeToolCallIntents, p.IncludeSystemMessages, p.IncludeDiagnostics} {
		if value != nil && !*value {
			selection |= 1 << i
		}
	}
	return selection
}

func (s ThreadContentSelection) Key() string {
	if s == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(s), 16)
}

// ViewKey names the candidate-shaping parts of a windowed transcript read.
// Budget only changes how much of that view fits in one response, so it is not
// part of the identity. A zero window is the legacy view and keeps its empty key.
func (p ThreadWindowParams) ViewKey(itemLimit int) string {
	if p.TurnWindow == nil {
		return ""
	}
	itemTypes := slices.Clone(p.ItemTypes)
	slices.Sort(itemTypes)
	itemTypes = slices.Compact(itemTypes)
	previewBytes := p.PreviewBytes
	if previewBytes == 0 {
		previewBytes = ThreadWindowPreviewBytesDefault
	}
	messageBytes := p.MessageBytes
	if messageBytes == 0 {
		messageBytes = ThreadWindowMessageBytesDefault
	}
	if itemLimit == 0 {
		itemLimit = ThreadWindowItemLimitDefault
	}
	normalized := struct {
		Window       ThreadTurnWindow `json:"window"`
		ItemTypes    []string         `json:"itemTypes,omitempty"`
		Preview      int              `json:"previewBytes"`
		Message      int              `json:"messageBytes"`
		PerTurnItems int              `json:"itemLimit"`
	}{
		Window: *p.TurnWindow, ItemTypes: itemTypes,
		Preview: previewBytes, Message: messageBytes, PerTurnItems: itemLimit,
	}
	encoded, _ := json.Marshal(normalized)
	digest := sha256.Sum256(encoded)
	return "window1." + base64.RawURLEncoding.EncodeToString(digest[:])
}

// ViewIncarnation binds a source incarnation to the normalized window view.
// The legacy view remains byte-for-byte unchanged.
func (p ThreadWindowParams) ViewIncarnation(incarnation string, itemLimit int) string {
	key := p.ViewKey(itemLimit)
	if key == "" {
		return incarnation
	}
	return key + ":" + base64.RawURLEncoding.EncodeToString([]byte(incarnation))
}

// SourceIncarnation removes this normalized window's view fence. A fence from
// another view is refused so callers can mint a fresh latest read instead of
// spending a held snapshot under changed parameters.
func (p ThreadWindowParams) SourceIncarnation(incarnation string, itemLimit int) (string, bool) {
	key := p.ViewKey(itemLimit)
	if key == "" {
		return incarnation, !strings.HasPrefix(incarnation, "window1.")
	}
	prefix := key + ":"
	if !strings.HasPrefix(incarnation, prefix) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(incarnation, prefix))
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

func (s ThreadContentSelection) Diagnostics() bool { return s&excludeDiagnostics == 0 }

func (s ThreadContentSelection) ToolCalls() bool { return s&excludeToolCalls == 0 }

func (s ThreadContentSelection) HasItems() bool {
	const content = excludeConversation | excludeThinking | excludeToolCalls | excludeToolCallIntents | excludeSystemMessages
	return s&content != content
}

// Item projects a value copy. Kept payloads share immutable slices with the
// source; no shared transcript or another subscriber's view is mutated.
func (s ThreadContentSelection) Item(item ThreadItem) (ThreadItem, bool) {
	switch item.Type {
	case "userMessage", "agentMessage":
		return item, s&excludeConversation == 0
	case "steering":
		if item.Source == "user" {
			return item, s&excludeConversation == 0
		}
		return item, s&excludeSystemMessages == 0
	case "reasoning":
		return item, s&excludeThinking == 0
	case "commandExecution":
		if s&excludeToolCalls != 0 {
			if s&excludeToolCallIntents != 0 || item.Description == "" {
				return ThreadItem{}, false
			}
			// Construct the small form explicitly so newly added tool payload
			// fields cannot accidentally become part of an intent-only view.
			return ThreadItem{
				Type: item.Type, ID: item.ID, TranscriptKey: item.TranscriptKey,
				Position: item.Position, TurnID: item.TurnID, TranscriptEntryIndex: item.TranscriptEntryIndex,
				ToolName: item.ToolName, CallID: item.CallID, Description: item.Description,
				Status: item.Status, StartedAt: item.StartedAt, CompletedAt: item.CompletedAt,
				Version: item.Version, RoundID: item.RoundID, CompletedAtEntry: item.CompletedAtEntry,
			}, true
		}
		if s&excludeToolCallIntents != 0 {
			item.Description = ""
		}
		return item, true
	default:
		return item, s&excludeSystemMessages == 0
	}
}

func (s ThreadContentSelection) Items(items []ThreadItem) []ThreadItem {
	if s == 0 {
		return items
	}
	selected := make([]ThreadItem, 0, len(items))
	for _, item := range items {
		if projected, keep := s.Item(item); keep {
			selected = append(selected, projected)
		}
	}
	return selected
}
