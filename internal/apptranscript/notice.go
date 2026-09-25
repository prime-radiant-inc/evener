package apptranscript

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// NoticeAnnouncement is how a presentational notice displays: the
// systemMessage's event kind, description and text. The live projector
// announces the event and the transcript projects the persisted NOTICE entry
// through the same builders, so history and live never disagree about a
// notice.
type NoticeAnnouncement struct {
	EventKind   appwire.ThreadItemEventKind
	Description string
	Text        string
}

// CommunicateItem projects a COMMUNICATE entry: the delivered message as an
// agentMessage keyed by part 0. It reports false for any other entry, or one
// with no payload.
func CommunicateItem(turnID string, entryIndex int, entry schema.Turn) (appwire.ThreadItem, bool) {
	if entry.Kind != schema.TurnCommunicate || entry.Communicate == nil {
		return appwire.ThreadItem{}, false
	}
	item := appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     fmt.Sprintf("item_assistant_%d_0", entryIndex),
		TurnID: turnID,
		Text:   entry.Communicate.Message,
		CallID: entry.Communicate.CallID,
		Status: appwire.TurnStatusCompleted,
	}
	if !entry.Timestamp.IsZero() {
		ms := entry.Timestamp.UnixMilli()
		item.StartedAt = &ms
	}
	return item, true
}

// NoticeItem projects a NOTICE entry as the systemMessage the live projector
// announces for the same event. It reports false for any other entry, for a
// notice whose payload is missing or does not match its kind, and for one
// with nothing to show.
func NoticeItem(turnID string, entryIndex int, entry schema.Turn) (appwire.ThreadItem, bool) {
	if entry.Kind != schema.TurnNotice || entry.Notice == nil {
		return appwire.ThreadItem{}, false
	}
	announcement, ok := noticeAnnouncement(*entry.Notice)
	if !ok {
		return appwire.ThreadItem{}, false
	}
	// Trimmed and dropped when empty exactly as the live announcement is.
	text := strings.TrimSpace(announcement.Text)
	if text == "" {
		return appwire.ThreadItem{}, false
	}
	return appwire.ThreadItem{
		Type:                 "systemMessage",
		ID:                   fmt.Sprintf("item_%s_%d", announcement.EventKind, entryIndex),
		TurnID:               turnID,
		TranscriptEntryIndex: entryIndex,
		Description:          strings.TrimSpace(announcement.Description),
		Text:                 text,
		Status:               appwire.TurnStatusCompleted,
		EventKind:            announcement.EventKind,
	}, true
}

func noticeAnnouncement(notice schema.NoticeInfo) (NoticeAnnouncement, bool) {
	switch {
	case notice.Kind == schema.NoticeToolRepair && notice.ToolRepair != nil:
		return ToolRepairAnnouncement(*notice.ToolRepair), true
	case notice.Kind == schema.NoticeGoalEnded && notice.GoalEnded != nil:
		return GoalEndedAnnouncement(*notice.GoalEnded), true
	case notice.Kind == schema.NoticeTurnLimit && notice.TurnLimit != nil:
		return TurnLimitAnnouncement(*notice.TurnLimit), true
	case notice.Kind == schema.NoticeSkillActivated && notice.SkillActivated != nil:
		return SkillActivatedAnnouncement(*notice.SkillActivated), true
	default:
		return NoticeAnnouncement{}, false
	}
}

// ToolRepairAnnouncement reports that a tool call needed a small, automatic
// correction before it ran. The notice's Changes entries are the repair
// engine's own machine format ("kind:field:detail", e.g.
// "drop_unknown:artifacts:dropped artifacts") — telemetry for the CLI's raw
// event trace, never meant for a reader parsing their transcript (kata k4v8).
// This builds the reader-facing sentence instead: what changed, named by the
// tool argument involved, with no internal enum or punctuation leaking through.
func ToolRepairAnnouncement(notice schema.ToolRepairNotice) NoticeAnnouncement {
	name := strings.TrimSpace(notice.ToolName)
	if name == "" {
		name = "tool call"
	}
	text := "Repaired " + name
	if len(notice.Changes) > 0 {
		phrases := make([]string, 0, len(notice.Changes))
		for _, raw := range notice.Changes {
			phrases = append(phrases, repairChangePhrase(raw))
		}
		text = fmt.Sprintf("Fixed the %s call: %s.", name, strings.Join(phrases, "; "))
	}
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindToolRepair, Description: "Tool call repaired", Text: text}
}

// repairChangePhrase turns one "kind:field:detail" repair entry into a plain
// sentence fragment. An unrecognized kind (e.g. a newer daemon's repair
// category this build predates) falls back to naming just the field, never
// the raw encoding.
func repairChangePhrase(raw string) string {
	parts := strings.SplitN(raw, ":", 3)
	kind := parts[0]
	var field string
	if len(parts) > 1 {
		field = parts[1]
	}
	switch kind {
	case "alias":
		if oldName, _, ok := strings.Cut(fieldDetail(parts), "→"); ok && oldName != "" {
			return fmt.Sprintf("renamed %q to %q", oldName, field)
		}
		return fmt.Sprintf("renamed a field to %q", field)
	case "coerce_type":
		return fmt.Sprintf("adjusted the %q field's type", field)
	case "drop_unknown":
		return fmt.Sprintf("removed the unrecognized %q field", field)
	case "unicode_repair":
		return "fixed an invalid character in the arguments"
	case "fill_required":
		if fieldKey, ok := strings.CutPrefix(field, "output."); ok && fieldDetail(parts) == "filled default" && fieldKey != "" {
			return fmt.Sprintf("filled the required %q key", fieldKey)
		}
		if key, ok := strings.CutPrefix(fieldDetail(parts), "filled "); ok && key != "" {
			return fmt.Sprintf("filled the required %q key", key)
		}
		return fmt.Sprintf("filled a required key in the %q field", field)
	case "synthesize":
		if field == "output" && fieldDetail(parts) == "synthesized default envelope" {
			return "created the required output object"
		}
	case "copy":
		if field == "message" && fieldDetail(parts) == "copied output.message" {
			return "copied nested output.message to the required message"
		}
	case "promote_json_object":
		if field == "output" && fieldDetail(parts) == "promoted JSON object string" {
			return "converted the output JSON string to an object"
		}
	}
	if field == "" {
		return "adjusted the arguments"
	}
	return fmt.Sprintf("adjusted the %q field", field)
}

// fieldDetail returns the third ("detail") segment of a split "kind:field:detail"
// entry, or "" when the entry has fewer than three segments.
func fieldDetail(parts []string) string {
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// GoalEndedAnnouncement renders the terminal /goal report line. A completed
// goal reads "✓ Goal achieved"; a blocked goal "⊘ Goal blocked" (with the
// reason appended when present); any other terminal status falls back to
// "⊘ Goal stopped".
func GoalEndedAnnouncement(notice schema.GoalEndedNotice) NoticeAnnouncement {
	var text string
	switch notice.Status {
	case "complete":
		text = "✓ Goal achieved"
	case "blocked":
		text = "⊘ Goal blocked"
		if reason := strings.TrimSpace(notice.Reason); reason != "" {
			text += ": " + reason
		}
	default:
		text = "⊘ Goal stopped"
	}
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindGoalEnded, Description: "Goal", Text: text}
}

// TurnLimitAnnouncement names each limit that was reached, one per line.
func TurnLimitAnnouncement(notice schema.TurnLimitNotice) NoticeAnnouncement {
	var lines []string
	if notice.MaxTurns > 0 {
		lines = append(lines, fmt.Sprintf("Maximum turns reached: %d", notice.MaxTurns))
	}
	if notice.MaxToolRoundsPerInput > 0 {
		lines = append(lines, fmt.Sprintf("Maximum tool rounds per input reached: %d", notice.MaxToolRoundsPerInput))
	}
	text := "Turn limit reached"
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindTurnLimit, Description: "Turn limit", Text: text}
}

// SkillActivatedAnnouncement is the standalone line for a skill activation
// that no use_skill tool item absorbed.
func SkillActivatedAnnouncement(notice schema.SkillActivatedNotice) NoticeAnnouncement {
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindSkillActivated, Description: "Skill activated", Text: "Activated skill: " + notice.Name}
}
