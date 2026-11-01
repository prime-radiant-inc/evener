package agent

import (
	"encoding/json"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// notesURLAddedPrefix opens the tool output of urls_add ahead of the rendered
// Link: line. Shared between the tool handler and self-echo classification.
const notesURLAddedPrefix = "URL added: "

type parsedNotesContext struct {
	human string
	agent string
	links map[string]string
}

func parseNotesField(lines []string, start int, prefix string) (string, int) {
	field := []string{strings.TrimPrefix(lines[start], prefix)}
	i := start + 1
	for i < len(lines) && (lines[i] == "" || strings.HasPrefix(lines[i], "  ")) {
		field = append(field, strings.TrimPrefix(lines[i], "  "))
		i++
	}
	return strings.Join(field, "\n"), i
}

func parseNotesContextBlock(block string) (parsedNotesContext, bool) {
	if block == notesClearedBlock {
		return parsedNotesContext{}, true
	}
	inner, ok := strings.CutPrefix(block, notesBlockOpen)
	if !ok {
		return parsedNotesContext{}, false
	}
	body, ok := strings.CutSuffix(inner, notesBlockClose)
	if !ok {
		return parsedNotesContext{}, false
	}
	body = neutralizeNotesFraming(strings.TrimSuffix(body, "\n"))
	if body == "" {
		return parsedNotesContext{}, true
	}
	var out parsedNotesContext
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "Human: "):
			out.human, i = parseNotesField(lines, i, "Human: ")
		case strings.HasPrefix(line, "Agent: "):
			out.agent, i = parseNotesField(lines, i, "Agent: ")
		case strings.HasPrefix(line, "Link: "):
			id := extractNotesLinkID(line)
			if id == "" {
				id = line
			}
			if out.links == nil {
				out.links = make(map[string]string)
			}
			out.links[id] = line
			i++
		default:
			return parsedNotesContext{}, false
		}
	}
	return out, true
}

func extractNotesLinkID(line string) string {
	idx := strings.LastIndex(line, " [id: ")
	if idx < 0 || !strings.HasSuffix(line, "]") {
		return ""
	}
	return line[idx+len(" [id: ") : len(line)-1]
}

// isNotesSelfEchoTurn reports whether history[idx] is a TurnNotesContext turn
// whose entire diff from the prior post-compaction projection was authored by
// the immediately preceding tool round's notes_agent_set, urls_add, or
// urls_remove calls, with no concurrent human note or URL mutation and no
// intervening compaction fold boundary.
func isNotesSelfEchoTurn(history []schema.Turn, idx int) bool {
	if idx < 0 || idx >= len(history) || history[idx].Kind != schema.TurnNotesContext {
		return false
	}
	curr, ok := parseNotesContextBlock(history[idx].Message.Text())
	if !ok {
		return false
	}
	compIdx := latestContextBoundaryIndex(history[:idx])
	trIdx := -1
scanRound:
	for j := idx - 1; j > compIdx; j-- {
		t := history[j]
		if t.Kind.TranscriptOnly() {
			continue
		}
		switch t.Kind {
		case schema.TurnSteering:
			if (steeringOrigin{kind: t.SteeringKind}).isHumanNoteSteer(t.Message.Text()) {
				return false
			}
		case schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnEnvironment, schema.TurnMemoryContext:
		case schema.TurnToolResults:
			trIdx = j
			break scanRound
		default:
			return false
		}
	}
	if trIdx < 0 {
		return false
	}
	if compIdx >= 0 {
		comp := history[compIdx]
		tr := history[trIdx]
		notes := history[idx]
		if tr.OriginalOrdinal != nil && notes.OriginalOrdinal == nil {
			return false
		}
		if !comp.Timestamp.IsZero() && !tr.Timestamp.IsZero() && !notes.Timestamp.IsZero() &&
			tr.Timestamp.Before(comp.Timestamp) && !notes.Timestamp.Before(comp.Timestamp) {
			return false
		}
	}

	hasAgentNoteSet := false
	var addedLinks map[string]bool
	var removedIDs map[string]bool
	var removeCallIDs map[string]bool

	for _, p := range history[trIdx].Message.Content {
		if p.Kind != llm.ContentToolResult || p.ToolResult == nil || p.ToolResult.IsError {
			continue
		}
		switch p.ToolResult.Name {
		case "notes_agent_set":
			hasAgentNoteSet = true
		case "urls_add":
			text, _ := p.ToolResult.Content.(string)
			if line, ok := strings.CutPrefix(strings.TrimSpace(text), notesURLAddedPrefix); ok {
				if addedLinks == nil {
					addedLinks = make(map[string]bool)
				}
				addedLinks[neutralizeNotesFraming(line)] = true
			}
		case "urls_remove":
			if removedIDs == nil {
				removedIDs = make(map[string]bool)
			}
			if removeCallIDs == nil {
				removeCallIDs = make(map[string]bool)
			}
			if p.ToolResult.ToolCallID != "" {
				removeCallIDs[p.ToolResult.ToolCallID] = true
			}
			if len(p.ToolResult.ToolState) > 0 {
				var st struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(p.ToolResult.ToolState, &st) == nil && st.ID != "" {
					removedIDs[st.ID] = true
				}
			}
		}
	}

	if len(removeCallIDs) > 0 {
		for j := trIdx - 1; j > compIdx; j-- {
			if history[j].Kind != schema.TurnAssistant {
				continue
			}
			for _, p := range history[j].Message.Content {
				if p.Kind != llm.ContentToolCall || p.ToolCall == nil || p.ToolCall.Name != "urls_remove" {
					continue
				}
				if !removeCallIDs[p.ToolCall.ID] {
					continue
				}
				var args struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(p.ToolCall.Arguments, &args) == nil && args.ID != "" {
					removedIDs[args.ID] = true
				}
			}
			break
		}
	}

	if !hasAgentNoteSet && len(addedLinks) == 0 && len(removedIDs) == 0 {
		return false
	}

	var prev parsedNotesContext
	for j := trIdx - 1; j > compIdx; j-- {
		if history[j].Kind != schema.TurnNotesContext {
			continue
		}
		var prevOK bool
		prev, prevOK = parseNotesContextBlock(history[j].Message.Text())
		if !prevOK {
			return false
		}
		break
	}

	if curr.human != prev.human {
		return false
	}
	if curr.agent != prev.agent && !hasAgentNoteSet {
		return false
	}
	for id, line := range curr.links {
		if prev.links[id] != line && !addedLinks[line] {
			return false
		}
	}
	for id := range prev.links {
		if _, stillPresent := curr.links[id]; !stillPresent && !removedIDs[id] {
			return false
		}
	}
	return true
}

// humanNoteSteerPrefix opens the steering text a shared-notes update carries.
// It is the last-resort marker for a record that persisted neither a
// SteeringKind nor a mutation method -- see steeringOrigin.isHumanNoteSteer.
const humanNoteSteerPrefix = "human updated their whiteboard:"

// steeringOrigin names the durable provenance of a steering entry. kind is the
// recorded SteeringKind, and method is the persisted client-mutation method that
// wrote the entry; both survive on the journal record a restored session rebuilds
// from. An empty field means the record predates that provenance.
type steeringOrigin struct {
	kind   string
	method string
}

// steeringOriginFromJournal returns the durable provenance of one journal
// record: its recorded kind, else the method that wrote it. A record that kept
// only the steer method is ordinary steering, whatever its id spells, and one
// that kept no provenance at all leaves the caller's own rule to decide.
func steeringOriginFromJournal(journal map[string]clientMutationRecord, id string) steeringOrigin {
	record := journal[id]
	return steeringOrigin{kind: record.SteeringKind, method: record.Method}
}

// isHumanNoteSteer reports whether steering text came from a shared-notes update.
// A recorded kind decides. Otherwise the persisted mutation method decides:
// notes/human/set is the note write, and any other named method (turn/steer, a
// drain or a promote) is ordinary steering, so a kindless steer whose text merely
// imitates the prefix keeps its bytes. Only when neither field was persisted --
// a record older than both, or a turn carrying neither -- does the write-path
// text shape remain the sole marker. Only note-origin steering is normalized:
// ordinary steering keeps the bytes the user typed, which is what the live path
// and the persisted transcript already show.
func (o steeringOrigin) isHumanNoteSteer(text string) bool {
	if o.kind != "" {
		// A recorded kind decides. Only the human-note kind is note-origin, so a
		// user's steering keeps its bytes even when it quotes the words back.
		return o.kind == events.SteeringKindHumanNote
	}
	if o.method != "" {
		// The mutation journal's method distinguishes the note write from
		// ordinary steering even when no kind was recorded.
		return o.method == clientMutationMethodNotesHumanSet
	}
	// Neither provenance was persisted, so the exact shape the write path emits
	// is the only marker left: the prefix followed by the space the note text
	// always comes after. A user text imitating that shape exactly is
	// indistinguishable and stays a documented ambiguity for these records alone.
	return strings.HasPrefix(text, humanNoteSteerPrefix+" ")
}

// rebuiltSteeringText returns a rebuilt steering entry's text: note-origin text is
// stripped like every other load path, and ordinary steering keeps its bytes.
func rebuiltSteeringText(origin steeringOrigin, text string) string {
	if !origin.isHumanNoteSteer(text) {
		return text
	}
	return stripTextControls(text)
}

// steeringKind returns the kind a rebuilt entry carries. The recorded kind
// decides when there is one; otherwise the same evidence isHumanNoteSteer reads
// for the text decides it, so a kindless notes/human/set record rebuilds with the
// note label instead of no label at all.
func (o steeringOrigin) steeringKind() string {
	if o.kind != "" {
		return o.kind
	}
	if o.method == clientMutationMethodNotesHumanSet {
		return events.SteeringKindHumanNote
	}
	return ""
}

// steeringOriginForTurn merges a history turn's own provenance with the
// provenance its journal record persisted. They normally agree -- the turn's kind
// is a copy of the record's -- and when they do not, note-origin evidence decides:
// the strip exists to keep note text out of the model copy, so a turn the write
// path already marked note-origin is not demoted by a record that kept no kind,
// and a record that proves note origin -- by kind, or by the kindless
// notes/human/set method its own rule reads -- is not overruled by an unrelated
// kind on the turn.
func steeringOriginForTurn(turn schema.Turn, origins map[string]steeringOrigin) steeringOrigin {
	origin := steeringOrigin{kind: turn.SteeringKind}
	if origin.kind == events.SteeringKindHumanNote || turn.ClientMutationID == "" {
		return origin
	}
	recorded, ok := origins[turn.ClientMutationID]
	if !ok {
		return origin
	}
	if recorded.kind == events.SteeringKindHumanNote ||
		(recorded.kind == "" && recorded.method == clientMutationMethodNotesHumanSet) {
		return recorded
	}
	if origin.kind != "" {
		return origin
	}
	return recorded
}

// escapeNotesHistoryTurns returns history with every NOTES_CONTEXT turn replaced
// by its model-facing escaped copy. A history built from a persisted source — a
// resumed transcript, or a forked delegate's inherited prefix — carries the raw
// block, which is only safe for display there: the raw block is what the
// transcript must keep so renderers and tool output show the user's real text,
// but model context must receive the escaped copy (see escapeNotesContextBlock),
// or a note carrying the closing tag regains the harness framing on every
// request the session serves. origins is the journal's steering provenance by
// client mutation id, which decides a steering turn that keeps no kind of its
// own (see steeringOrigins); nil means no record is in reach, and every steering
// turn is then decided by its own kind and the write-path text shape. The input
// is not modified.
func escapeNotesHistoryTurns(history []schema.Turn, origins map[string]steeringOrigin) []schema.Turn {
	out := make([]schema.Turn, len(history))
	copy(out, history)
	for i := range out {
		text := out[i].Message.Text()
		if text == "" {
			continue
		}
		switch out[i].Kind {
		case schema.TurnNotesContext:
			// A turn persisted before the write-path strip can still carry
			// controls, and the framing escape only knows the framing spellings:
			// strip the other controls as well before the model copy is built.
			out[i].Message = llm.User(escapeNotesContextBlock(stripTextControls(text)))
		case schema.TurnSteering:
			// The turn's own kind and the record that wrote it decide together
			// (see steeringOriginForTurn): a turn written before kinds were
			// stamped otherwise leaves the text shape to guess from, and a normal
			// steer whose text imitates the note prefix would lose its bytes on
			// every restart. Daemon steering carries no mutation id, and an
			// inherited prefix belongs to another session's journal, so both keep
			// the kind-then-shape decision.
			origin := steeringOriginForTurn(out[i], origins)
			if !origin.isHumanNoteSteer(text) {
				// Ordinary steering is delivered exactly as the user typed it, live
				// and restored alike; only notes-derived text is normalized, so this
				// copy keeps its bytes.
				continue
			}
			// A human-note update rides a steering turn, and this copy is what
			// resumed requests and inherited prefixes hand to the model, so the
			// text parts are stripped as well. Only the text parts change, and
			// the slice is copied before the first change: llm.User(text) would
			// replace the whole message and drop the image parts a steering turn
			// can legitimately carry (attachments, or queued image-bearing input
			// drained as steer), which is data loss on restore or fork. The
			// stored turn keeps its bytes.
			var rewritten []llm.ContentPart
			for j, part := range out[i].Message.Content {
				if part.Kind != llm.ContentText {
					continue
				}
				stripped := stripTextControls(part.Text)
				if stripped == part.Text {
					continue
				}
				if rewritten == nil {
					rewritten = append([]llm.ContentPart(nil), out[i].Message.Content...)
				}
				rewritten[j].Text = stripped
			}
			if rewritten != nil {
				out[i].Message.Content = rewritten
			}
		}
	}
	return out
}
