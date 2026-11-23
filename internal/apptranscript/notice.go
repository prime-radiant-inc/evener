package apptranscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// NoticeAnnouncement is how a presentational notice displays: the
// systemMessage's event kind, description and text. The live projector
// announces the event and the transcript projects the persisted NOTICE entry
// through the same builders, so history and live never disagree about a
// notice. Raw is the structured detail a client draws from instead of
// re-parsing the text; nil when the notice has none.
type NoticeAnnouncement struct {
	EventKind   appwire.ThreadItemEventKind
	Description string
	Text        string
	Raw         json.RawMessage
}

// CommunicateItem projects a COMMUNICATE entry: the delivered message as an
// agentMessage keyed by part 0. It reports false for any other entry, or one
// with no payload.
func CommunicateItem(turnID string, entryIndex int, entry schema.Turn) (appwire.ThreadItem, bool) {
	if entry.Kind != schema.TurnCommunicate || entry.Communicate == nil {
		return appwire.ThreadItem{}, false
	}
	item := appwire.ThreadItem{
		Authorship: ProjectAuthorship(entry.Authorship),
		Type:       "agentMessage",
		ID:         fmt.Sprintf("item_assistant_%d_0", entryIndex),
		TurnID:     turnID,
		Text:       entry.Communicate.Message,
		CallID:     entry.Communicate.CallID,
		Status:     appwire.TurnStatusCompleted,
		Observation: &appwire.SessionObservationMetadata{
			TurnID: turnID, EndTurn: entry.Communicate.EndTurn,
			EndReason:   entry.Communicate.EndReason,
			CausalKnown: entry.CausalKnown, Causal: provenance.Clone(entry.Causal),
		},
	}
	// Older Hub watchers still recognize terminal replies by this wire value.
	// It is a projection default, not an option in the communicate tool.
	if item.Observation.EndTurn && item.Observation.EndReason == "" {
		item.Observation.EndReason = "done"
	}
	if entry.AcceptedReply {
		item.Observation.TerminalAccepted = new(true)
	}
	if p := entry.ConversationProvenance; p != nil {
		item.ConversationProvenance = &appwire.ConversationOutputProvenance{Version: p.Version, InstanceID: p.InstanceID,
			RunGeneration: p.RunGeneration, RequestID: p.RequestID, FrontierID: p.FrontierID, SourceItemID: item.ID}
	}
	item.StartedAt = entryStartedAt(entry)
	return item, true
}

// IsCommunicateTool identifies the result tool in a recorded assistant or
// tool-result turn. Older entries only knew the default name.
func IsCommunicateTool(name, recordedName string) bool {
	return name == "communicate" || name != "" && name == recordedName
}

// PossiblyQuietCommunicateCall reports whether call is a communicate tool call
// whose sent arguments select `log_quietly_and_end_turn` or carry no reply
// message (for example `{}` or malformed bytes) and may therefore settle
// quietly. It avoids heap allocations and regex repair on the hot projection path.
func PossiblyQuietCommunicateCall(call *llm.ToolCallData, resultToolName string) bool {
	if call == nil || !IsCommunicateTool(call.Name, resultToolName) {
		return false
	}
	return communicateArgsPossiblyQuiet(call.SentArguments())
}

func communicateArgsPossiblyQuiet(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw[0] != '{' {
		return true
	}
	hasMsg, quietNext, decided := scanTopLevelCommunicate(raw, 1)
	if decided {
		return quietNext || !hasMsg
	}
	return communicateArgsPossiblyQuietSlow(raw)
}

func communicateArgsPossiblyQuietSlow(raw string) bool {
	normalized := NormalizeCommunicateArguments(json.RawMessage(raw))
	if _, quiet, ok := schema.CommunicateArgsOutcome(normalized); ok && quiet {
		return true
	}
	return CommunicateMessageFromArguments(normalized) == ""
}

func scanTopLevelCommunicate(raw string, depth int) (hasMsg, quietNext, decided bool) {
	if depth > 2 {
		return false, false, false
	}
	i := skipWS(raw, 1)
	n := len(raw)
	ambiguous := false
	for i < n {
		if raw[i] == '}' {
			if ambiguous {
				return false, false, false
			}
			return hasMsg, false, true
		}
		var key string
		var keyHasEsc bool
		if raw[i] == '"' {
			start := i + 1
			end, esc, ok := scanJSONStringEnd(raw, i)
			if !ok {
				return false, true, true
			}
			key, keyHasEsc = raw[start:end], esc
			i = end + 1
		} else if isIdentStart(raw[i]) {
			start := i
			for i < n && isIdentPart(raw[i]) {
				i++
			}
			key = raw[start:i]
		} else {
			return false, true, true
		}
		if keyHasEsc {
			ambiguous = true
		}
		i = skipWS(raw, i)
		if i >= n || raw[i] != ':' {
			return false, true, true
		}
		i = skipWS(raw, i+1)
		if i >= n {
			return false, true, true
		}
		switch raw[i] {
		case '"':
			valStart := i + 1
			valEnd, esc, ok := scanJSONStringEnd(raw, i)
			if !ok {
				return false, true, true
			}
			i = valEnd + 1
			switch {
			case !keyHasEsc && key == "next" && depth == 1:
				if !esc {
					if strings.TrimSpace(raw[valStart:valEnd]) == schema.CommunicateNextLogQuietlyAndEndTurn {
						return false, true, true
					}
				} else {
					ambiguous = true
				}
			case !keyHasEsc && key == "message":
				if !esc {
					if strings.TrimSpace(raw[valStart:valEnd]) != "" {
						hasMsg = true
					}
				} else {
					ambiguous = true
				}
			case !keyHasEsc && key == "output" && depth == 1 && strings.Contains(raw[valStart:valEnd], "message"):
				ambiguous = true
			}
		case '{':
			end, ok := scanBalancedEnd(raw, i, '{', '}')
			if !ok {
				return false, true, true
			}
			if !keyHasEsc && key == "output" && depth == 1 {
				nestedHas, _, nestedDecided := scanTopLevelCommunicate(raw[i:end+1], depth+1)
				if !nestedDecided {
					ambiguous = true
				} else if nestedHas {
					hasMsg = true
				}
			}
			i = end + 1
		case '[':
			end, ok := scanBalancedEnd(raw, i, '[', ']')
			if !ok {
				return false, true, true
			}
			i = end + 1
		default:
			for i < n && raw[i] != ',' && raw[i] != '}' && !isASCIIWS(raw[i]) {
				i++
			}
		}
		i = skipWS(raw, i)
		if i < n && raw[i] == ',' {
			i = skipWS(raw, i+1)
		}
	}
	return false, true, true
}

func isASCIIWS(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func skipWS(s string, i int) int {
	for i < len(s) && isASCIIWS(s[i]) {
		i++
	}
	return i
}

func isIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isIdentPart(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func scanJSONStringEnd(s string, quoteAt int) (end int, hasEsc, ok bool) {
	for i := quoteAt + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			hasEsc = true
			i++
		case '"':
			return i, hasEsc, true
		}
	}
	return len(s), hasEsc, false
}

func scanBalancedEnd(s string, start int, open, closeCh byte) (int, bool) {
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '"':
			end, _, ok := scanJSONStringEnd(s, i)
			if !ok {
				return len(s), false
			}
			i = end
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return len(s), false
}

// assistantCommunicateScan checks a TurnAssistant entry for any communicate
// call and returns the IDs that may settle quietly without allocating when
// no communicate call is present.
func AssistantCommunicateCallIDs(turn schema.Turn, forceAllQuiet bool) (hasCommunicate bool, possiblyQuietIDs []string) {
	if turn.Kind != schema.TurnAssistant {
		return false, nil
	}
	for _, part := range turn.Message.Content {
		if part.Kind != llm.ContentToolCall || part.ToolCall == nil || !IsCommunicateTool(part.ToolCall.Name, turn.ResultToolName) {
			continue
		}
		hasCommunicate = true
		if forceAllQuiet || communicateArgsPossiblyQuiet(part.ToolCall.SentArguments()) {
			possiblyQuietIDs = append(possiblyQuietIDs, part.ToolCall.ID)
		}
	}
	return hasCommunicate, possiblyQuietIDs
}

// PossiblyQuietCommunicateCallIDs returns the tool call IDs in a TurnAssistant
// entry that may settle quietly, in content order.
func PossiblyQuietCommunicateCallIDs(turn schema.Turn) []string {
	_, ids := AssistantCommunicateCallIDs(turn, false)
	return ids
}

// IsQuietCommunicate reports typed evidence of a successful quiet yield.
func IsQuietCommunicate(result *llm.ToolResultData) bool {
	return result != nil && !result.IsError && IsQuietCommunicateState(result.ToolState)
}

// IsQuietCommunicateState decodes the typed result state shared by replay
// and the live tool-call end event.
func IsQuietCommunicateState(raw json.RawMessage) bool {
	if len(raw) == 0 || !bytes.Contains(raw, []byte("communicate_quiet")) {
		return false
	}
	return schema.IsQuietCommunicateState(raw)
}

func markAcceptedAssistantReply(items []appwire.ThreadItem, turnID string, entry schema.Turn) {
	if entry.Kind != schema.TurnAssistant || !entry.AcceptedReply {
		return
	}
	// A response can contain multiple text runs. They remain separate cards,
	// but only the last visible message carries the terminal observation.
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Type == "agentMessage" && strings.TrimSpace(items[i].Text) != "" {
			items[i].Observation = &appwire.SessionObservationMetadata{
				TurnID: turnID, EndTurn: true, EndReason: "done", TerminalAccepted: new(true),
				CausalKnown: entry.CausalKnown, Causal: provenance.Clone(entry.Causal),
			}
			return
		}
	}
}

// CommunicateTerminalAccepted reads the result of exactly this communicate
// call. A COMMUNICATE entry was written before arbitration, so neither its
// presence nor its end reason can prove that it won the terminal capture.
func CommunicateTerminalAccepted(entry schema.Turn, callID string) *bool {
	if callID == "" || entry.Kind != schema.TurnToolResults && entry.Kind != schema.TurnTool {
		return nil
	}
	for _, part := range entry.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
			continue
		}
		result := part.ToolResult
		if result.ToolCallID != callID || result.IsError {
			continue
		}
		if state, ok := schema.DecodeCommunicateToolState(result.ToolState); ok {
			if state.IsQuiet() {
				continue
			}
			if state.Accepted != nil && state.EndTurn != nil && *state.EndTurn {
				if state.BoundaryPending {
					return nil // Wait for the execution completion record, not tool arbitration.
				}
				return state.Accepted
			}
		}
		var encoded []byte
		switch value := result.Content.(type) {
		case string:
			encoded = []byte(value)
		case json.RawMessage:
			encoded = value
		}
		var state struct {
			Accepted *bool `json:"accepted"`
			EndTurn  *bool `json:"end_turn"`
		}
		if json.Unmarshal(encoded, &state) == nil && state.EndTurn != nil && *state.EndTurn {
			return state.Accepted
		}
	}
	return nil
}

// entryStartedAt is an entry's recorded instant as a startedAt, or nil when
// the entry has no timestamp.
func entryStartedAt(entry schema.Turn) *int64 {
	if entry.Timestamp.IsZero() {
		return nil
	}
	ms := entry.Timestamp.UnixMilli()
	return &ms
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
	item, ok := SystemMessage(announcement, fmt.Sprintf("item_%s_%d", announcement.EventKind, entryIndex), turnID)
	if !ok {
		return appwire.ThreadItem{}, false
	}
	item.TranscriptEntryIndex = entryIndex
	item.Actor = projectedActor(entry.Notice.Actor)
	item.StartedAt = entryStartedAt(entry)
	return item, true
}

func projectedActor(actor schema.SessionActor) *appwire.SessionActor {
	if actor.Kind == "" {
		return nil
	}
	return &appwire.SessionActor{Kind: actor.Kind, ManagerRef: actor.ManagerRef}
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
	case notice.Kind == schema.NoticeApprovalDecision && notice.ApprovalDecision != nil:
		return ApprovalDecisionAnnouncement(*notice.ApprovalDecision), true
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

// ApprovalDecisionAnnouncement is the history line a human's Allow or Deny on
// a sandbox escalation leaves (S16). Its Raw carries the decision under
// "approvalDecision", so a client draws the approval history row without
// parsing the text.
func ApprovalDecisionAnnouncement(notice schema.ApprovalDecisionNotice) NoticeAnnouncement {
	text := fmt.Sprintf("Denied %s access to %s", notice.Tool, notice.DeniedPath)
	if notice.Approved {
		text = fmt.Sprintf("Allowed %s to access %s", notice.Tool, notice.DeniedPath)
	}
	// The conversion keeps the wire's field list tied to
	// ApprovalDecisionNotice's: Go ignores tags when converting between
	// struct types with identical fields.
	// Marshal cannot fail: every field is a string or a bool.
	raw, _ := json.Marshal(map[string]any{
		"approvalDecision": struct {
			EscalationID string `json:"escalationId"` //nolint:tagliatelle // AppWire Raw payload the clients read (camelCase wire).
			Approved     bool   `json:"approved"`
			Tool         string `json:"tool"`
			Kind         string `json:"kind"`
			DeniedPath   string `json:"deniedPath"` //nolint:tagliatelle // AppWire Raw payload the clients read (camelCase wire).
		}(notice),
	})
	return NoticeAnnouncement{EventKind: appwire.ThreadItemEventKindApprovalDecision, Description: "Approval", Text: text, Raw: raw}
}

// QuietYieldAnnouncement builds the muted notice for a successful quiet
// communicate turn or bare-text live-work quiet park. When note is empty (such
// as legacy empty-object records), it falls back to a neutral caption. When
// companion assistant commentary prose was emitted in the same response (or the
// full bare-text note was shortened), it rides in Raw under "commentary" so
// clients keep it inspectable behind the muted notice instead of as an unread
// chat bubble.
func QuietYieldAnnouncement(note, commentary string) NoticeAnnouncement {
	text := strings.TrimSpace(note)
	if text == "" {
		text = "Ended turn quietly"
	}
	var raw json.RawMessage
	if trimmed := strings.TrimSpace(commentary); trimmed != "" {
		raw, _ = json.Marshal(struct {
			Commentary string `json:"commentary"`
		}{Commentary: trimmed})
	}
	return NoticeAnnouncement{
		EventKind:   appwire.ThreadItemEventKindQuietYield,
		Description: "Quiet yield",
		Text:        text,
		Raw:         raw,
	}
}
