package apptranscript

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// The announcements below are for events nothing records: the live projector
// announces them as it sees them and the live overlay keeps them as notices,
// both through these builders, so the two never disagree about a notice.

// marshalContextCompaction is json.Marshal, a seam so a test can reach the
// marshal-failure branch no real ContextCompactionData can.
var marshalContextCompaction = json.Marshal

// SystemMessage builds the systemMessage item an announcement displays as. It
// reports false when there is nothing to show: no text (only a plugin_loaded
// line may carry its whole summary in the description), or no description and
// no text.
func SystemMessage(announcement NoticeAnnouncement, id, turnID string) (appwire.ThreadItem, bool) {
	description := strings.TrimSpace(announcement.Description)
	text := strings.TrimSpace(announcement.Text)
	if text == "" && announcement.EventKind != appwire.ThreadItemEventKindPluginLoaded {
		return appwire.ThreadItem{}, false
	}
	if description == "" && text == "" {
		return appwire.ThreadItem{}, false
	}
	return appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          id,
		TurnID:      turnID,
		Description: description,
		Text:        text,
		Status:      appwire.TurnStatusCompleted,
		Raw:         announcement.Raw,
		EventKind:   announcement.EventKind,
	}, true
}

// LoopDetectionAnnouncement reports the loop detector's message.
func LoopDetectionAnnouncement(data events.LoopDetectionData) NoticeAnnouncement {
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindLoopDetection, Description: "Loop detection", Text: data.Message}
}

// ContextCompactionAnnouncement summarizes a compaction pass. Its Raw carries
// the structured numbers under a "compaction" key, so the web can draw an
// honest before→after expand (mockup #17 Alt A) from real numbers; Raw is nil
// when there is nothing to carry so the item stays clean.
func ContextCompactionAnnouncement(data events.ContextCompactionData) NoticeAnnouncement {
	return NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindContextCompaction,
		Description: "Context compaction",
		Text:        contextCompactionText(data),
		Raw:         contextCompactionRaw(data),
	}
}

func contextCompactionRaw(data events.ContextCompactionData) json.RawMessage {
	if data.Layer == "" && data.TurnsBefore == 0 && data.TurnsAfter == 0 &&
		data.EstTokensBefore == 0 && data.EstTokensAfter == 0 {
		return nil
	}
	raw, err := marshalContextCompaction(map[string]any{"compaction": data})
	if err != nil {
		return nil
	}
	return raw
}

func contextCompactionText(data events.ContextCompactionData) string {
	var lines []string
	if strings.TrimSpace(data.Layer) != "" {
		lines = append(lines, "Layer: "+strings.TrimSpace(data.Layer))
	}
	if data.TurnsBefore > 0 || data.TurnsAfter > 0 {
		lines = append(lines, fmt.Sprintf("Turns: %d -> %d", data.TurnsBefore, data.TurnsAfter))
	}
	if data.EstTokensBefore > 0 || data.EstTokensAfter > 0 {
		lines = append(lines, fmt.Sprintf("Estimated tokens: %d -> %d", data.EstTokensBefore, data.EstTokensAfter))
	}
	if len(lines) == 0 {
		return "Context compaction ran"
	}
	return strings.Join(lines, "\n")
}

// PluginLoadedAnnouncement is the one-line plugin summary, carried in the
// description with no text, plus the counts on Raw under "pluginLoaded".
func PluginLoadedAnnouncement(data events.PluginLoadedData) NoticeAnnouncement {
	return NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindPluginLoaded,
		Description: pluginLoadedSummary(data),
		Raw:         pluginLoadedRaw(data),
	}
}

func pluginLoadedRaw(data events.PluginLoadedData) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"pluginLoaded": struct {
			Name       string `json:"name"`
			SkillCount int    `json:"skillCount"` //nolint:tagliatelle // AppWire Raw payload the web reads (camelCase wire).
			AgentCount int    `json:"agentCount"` //nolint:tagliatelle // AppWire Raw payload the web reads (camelCase wire).
			MCPCount   int    `json:"mcpCount"`   //nolint:tagliatelle // AppWire Raw payload the web reads (camelCase wire).
		}{
			Name:       strings.TrimSpace(data.Name),
			SkillCount: data.SkillCount,
			AgentCount: data.AgentCount,
			MCPCount:   data.MCPCount,
		},
	})
	if err != nil {
		return nil
	}
	return raw
}

func pluginLoadedSummary(data events.PluginLoadedData) string {
	name := strings.TrimSpace(data.Name)
	if name == "" {
		return fmt.Sprintf("Loaded plugin (%d skills, %d agents, %d MCP servers)", data.SkillCount, data.AgentCount, data.MCPCount)
	}
	return fmt.Sprintf("Loaded plugin %s (%d skills, %d agents, %d MCP servers)", name, data.SkillCount, data.AgentCount, data.MCPCount)
}

// ForkSummaryAnnouncement reports a captured fork summary.
func ForkSummaryAnnouncement(data events.ForkSummaryData) NoticeAnnouncement {
	text := "Fork summary captured"
	if data.Turn > 0 {
		text = fmt.Sprintf("Fork summary captured at transcript turn %d", data.Turn)
	}
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindForkSummary, Description: "Fork summary", Text: text}
}

// PromptLoadedAnnouncement reports a loaded prompt by label and size.
func PromptLoadedAnnouncement(data events.PromptLoadedData) NoticeAnnouncement {
	label := strings.TrimSpace(data.Label)
	if label == "" {
		label = "prompt"
	}
	text := "Loaded prompt " + label
	if data.Size > 0 {
		text = fmt.Sprintf("Loaded prompt %s (%d B)", label, data.Size)
	}
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindPromptLoaded, Description: "Prompt loaded", Text: text}
}

// RoundTimingsAnnouncement lists a round's per-phase durations. Its Raw
// carries them under a "roundTimings" key, so the web draws a rounded,
// prioritized summary (kata 7zkv) instead of re-parsing the
// nanosecond-precision prose.
func RoundTimingsAnnouncement(data events.RoundTimings) NoticeAnnouncement {
	parts := []string{
		fmt.Sprintf("Round %d", data.Round),
		"total=" + data.TotalRound.String(),
		"llm=" + data.LLMCall.String(),
		"context=" + data.ContextMgmt.String(),
		"tools=" + data.ToolExec.String(),
		"prompt=" + data.SystemPrompt.String(),
		"history=" + data.HistoryExpand.String(),
		"tool_defs=" + data.ToolDefs.String(),
		"persistence=" + data.Persistence.String(),
		"after_action=" + data.AfterAction.String(),
		"overhead=" + data.LoopOverhead.String(),
	}
	var raw json.RawMessage
	if encoded, err := json.Marshal(map[string]any{"roundTimings": data}); err == nil {
		raw = encoded
	}
	return NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindRoundTimings,
		Description: "Round timings",
		Text:        strings.Join(parts, " "),
		Raw:         raw,
	}
}

// LiveOutputImages converts a settled call's image descriptors to the wire
// shape. It returns nil, never empty, when nothing survives: an item whose
// descriptors were all unusable never showed images to remove.
func LiveOutputImages(images []events.OutputImage) []appwire.OutputImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]appwire.OutputImage, 0, len(images))
	for _, img := range images {
		if img.URL == "" && img.SHA == "" {
			continue
		}
		out = append(out, appwire.OutputImage{
			Source:    img.Source,
			Name:      img.Name,
			MediaType: img.MediaType,
			Size:      img.Size,
			URL:       img.URL,
			SHA:       img.SHA,
			Path:      img.Path,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UnfetchableUntilRecorded reports whether an image's bytes came back inside
// the tool result, addressed by sha with no URL: no server can serve them
// until the round's tool-result entry is written, so a live view holds the
// descriptor until EventToolResultImagesPersisted. A descriptor that names a
// URL points at bytes a server can already re-read.
func UnfetchableUntilRecorded(image appwire.OutputImage) bool {
	return image.Source == events.OutputImageSourceToolResult && image.URL == ""
}
