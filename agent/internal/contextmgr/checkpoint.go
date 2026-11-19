package contextmgr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/runetrim"
	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// escapeWorkingNote returns the text a working note carries for msg: the
// model's own text with line-start harness frames defused, escaped with its
// text phases intact and only then flattened.
//
// Flattening first loses a boundary the model's own message had. A Responses
// commentary part that ends with an open fence and a newline makes a bare frame
// at the start of the final answer look like fenced content to a single scan,
// so the frame survives into the working notes of a user-role checkpoint even
// though direct replay escapes it (#6493 review). Escaping each phase segment
// as its own block keeps that frame exposed, which is the safe direction: a
// quoted example is still spared inside its own segment.
func escapeWorkingNote(msg llm.Message) string {
	// The note is read as the adapter would send it, so the parts it carries are
	// the parts CarriesWireText keeps — the same call the replay projection
	// makes, through one helper (#6493 review).
	wireIndexes, texts, phases, breaks := sysmsg.WireTextPartsFlushed(msg)
	if len(texts) == 0 {
		return sysmsg.EscapeModelAuthoredFrames(msg.Text())
	}
	// One scan, shared with the replay projection: the note is read as the
	// adapter would send it, then written out flat.
	//
	// The note has two readers and they disagree, so it is escaped for both.
	// The replay projection reads the phases as separate messages, and the
	// checkpoint's own reader reads one flat block. Fence state is what
	// decides whether an opener is spared, and it differs between those two
	// readings: a fence left open in commentary hides a frame at the start of
	// the final answer from the flat reader, while a fence opened in one phase
	// and closed at the start of the next hides one from the per-phase reader.
	// Escaping per segment and then once more over the join covers both — an
	// opener either reading leaves raw gets escaped, and an example both
	// readings see as fenced keeps its bytes. Escaping is idempotent and never
	// adds or removes a fence delimiter, so the second pass cannot re-open what
	// the first closed.
	//
	// The escaped segments are put back onto the parts they came from and the
	// result is the concatenation of every original text part. Both halves of
	// that matter, and a note built from the wire-filtered parts alone would
	// lose one of them:
	//
	//   - Concatenation, not a separator. The adapter groups same-phase parts
	//     into one message, so the model read them as one run of characters and
	//     Message.Text() concatenated them; a path split across two parts has
	//     to stay one token (#6493 review).
	//   - Every original part, including the ones the wire drops. A part that
	//     is only a space or a newline never reaches the provider, so the scan
	//     must not see it, but the note is read by a human-facing summarizer and
	//     Text() did keep it. Dropping it merges two words the model wrote
	//     apart (#6493 review).
	//
	// So the scan runs over the wire parts, its rewrites go back at the indexes
	// they came from, and the note is the original parts concatenated. The
	// second pass then reads the note as its own flat reader will.
	escaped := make([]string, len(texts))
	copy(escaped, texts)
	segments := sysmsg.TextSegmentsFlushed(phases, breaks, "openai-responses")
	escapedSegments := sysmsg.EscapeFlushedText(texts, phases, breaks, "openai-responses")
	for s, span := range segments {
		pieces, ok := sysmsg.SplitJoinedEscape(escapedSegments[s], texts[span[0]:span[1]])
		if !ok {
			// The scan changed the line structure; carry the parts as they are
			// rather than invent boundaries the model did not write.
			continue
		}
		copy(escaped[span[0]:span[1]], pieces)
	}
	// A part is also a block of its own on the reading that matters here: the
	// wire form emits each part separately for Responses, Anthropic and Google,
	// so a fence left open at the end of one part does not run into the next.
	// The segment pass above reads same-phase parts as one run, which is the
	// other reading, and the two disagree exactly where a fence crosses a part
	// boundary. Applying both leaves no opener raw that either reading exposes,
	// and the pass below over the flattened note is the third reader — the
	// summarizer's — which sees the parts as one block again (#6493 review).
	for k, defused := range sysmsg.EscapePartsIndividually(escaped) {
		escaped[k] = defused
	}
	// Start from every original text part, then put the escaped wire parts back
	// at their own indexes. A part the wire drops is still in the note, with
	// the bytes the model wrote.
	noteParts := make([]string, len(msg.Content))
	for i, part := range msg.Content {
		if part.Kind == llm.ContentText {
			noteParts[i] = part.Text
		}
	}
	for k, at := range wireIndexes {
		noteParts[at] = escaped[k]
	}
	var note strings.Builder
	for _, part := range noteParts {
		note.WriteString(part)
	}
	return sysmsg.EscapeModelAuthoredFrames(note.String())
}

// maxCheckpointChars caps a checkpoint to avoid ballooning the context. The
// checkpoint either feeds into the LLM summarizer (which replaces it) or — if
// the summarizer is unavailable — becomes the sole history the main model sees.
// 60k chars ≈ 15k tokens: fits comfortably in the cheap model's context
// alongside the instruction prefix, and leaves room for preserved recent turns
// in the main model's context. The human partner's messages a model-written
// compaction turn carries share the same cap. The cap yields to those
// messages: none is ever dropped to meet it (fitCheckpointConversation).
// Metadata that alone would exceed the budget is cut after the transcript
// pointer, with fitCheckpointMetadata's marker disclosing the cut.
const maxCheckpointChars = 60_000

// checkpoint replaces old history with a structured state snapshot.
// Returns a new history slice: [checkpoint_message, ...preserved_recent_turns].
//
// The checkpoint is a deterministic extraction — no LLM call. It captures user
// messages verbatim, agent responses, file/tool metadata, task list, and skills.
// User messages and agent responses are stored as an interleaved Markdown
// conversation for readable round-tripping across repeated compactions.
func checkpoint(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) []schema.Turn {
	result, _ := checkpointFold(history, preserveRecent, meta, resultToolName)
	return result
}

// checkpointFold is checkpoint that also returns the turns the new checkpoint
// turn stands for (history[:cutoff]), or nil when nothing was folded. A
// summary completing the same fold reads the partner's messages from them.
func checkpointFold(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) ([]schema.Turn, []schema.Turn) {
	preserveRecent = preservePendingAskTail(history, preserveRecent, meta)
	if attentionTransparentTurnCount(history) <= preserveRecent {
		return history, nil
	}

	cutoff := foldCutoff(history, preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	data := collectCheckpointData(history, cutoff, resultToolName)
	checkpointTurn := schema.NewTurn(schema.TurnCheckpoint, llm.User(formatCheckpoint(data, meta, maxCheckpointChars)))

	result := make([]schema.Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result, history[:cutoff:cutoff]
}

// checkpointData is the raw material distilled from the to-be-compacted history
// prefix: which files were modified, which skills activated, per-tool call counts,
// the last few shell results, the conversation (user/agent turns), and the
// assistant's working notes.
type checkpointData struct {
	fileWrites       map[string]checkpointWriteStatus
	activatedSkills  map[string]bool
	toolCounts       map[string]int
	lastShellResults []string
	conversation     []checkpointConversationEntry
	workingNotes     []string
	// earlierSummaries are LLM summaries the checkpoint folds, kept whole: a
	// summary is the only record of the conversation it replaced, including
	// the permissions and holds it quotes.
	earlierSummaries []string
}

// collectCompaction carries a previous compaction turn's text into the new
// checkpoint so it survives repeated compactions. From a checkpoint it carries
// the conversation, working notes and earlier summaries it holds. From an LLM
// summary (summary is true) it carries the human partner's messages the summary
// turn holds verbatim, from its own conversation section only, and the rest of
// the summary whole as an earlier summary; the model's own text is not mined
// for conversation or working notes.
//
// This is the deterministic checkpoint's carry, which the default compaction
// paths use. The opt-in session-log strategy, and OODA which embeds it, now
// carries the partner conversation in the same section, but not earlier
// summaries.
func (d *checkpointData) collectCompaction(text string, summary bool) {
	if summary {
		if !carriesPartnerMessages(text) {
			d.earlierSummaries = append(d.earlierSummaries, text)
			return
		}
		d.conversation = append(d.conversation, extractMarkdownConversation(text)...)
		d.earlierSummaries = append(d.earlierSummaries, withoutMarkdownSection(text, checkpointConversationHeading))
		return
	}
	d.conversation = append(d.conversation, extractCheckpointConversation(text)...)
	d.workingNotes = append(d.workingNotes, extractCheckpointWorkingNotes(text)...)
	d.earlierSummaries = append(d.earlierSummaries, extractCheckpointEarlierSummaries(text)...)
}

// checkpointWriteStatus classifies a write tool call by the outcome of its
// paired tool result. The checkpoint may only report a file as modified when a
// result confirmed the write; an attempted write that failed or whose result is
// absent from the compacted prefix is kept distinct and never inferred as
// completed work.
type checkpointWriteStatus int

const (
	// writeUnconfirmed is the zero value, so a missing fileWrites entry — or any
	// unrecognized state — is never mistaken for confirmed completed work.
	writeUnconfirmed checkpointWriteStatus = iota // no result in the compacted prefix
	writeConfirmed                                // result present and not an error
	writeFailed                                   // result present and IsError
)

// recordWrite keeps a confirmed write from being downgraded by a later attempt
// of the same path, because the file was in fact modified. Otherwise the latest
// outcome wins.
func (d *checkpointData) recordWrite(path string, status checkpointWriteStatus) {
	if prev, ok := d.fileWrites[path]; ok && prev == writeConfirmed {
		return
	}
	d.fileWrites[path] = status
}

// escapeCheckpointPath renders a model-supplied single-line token — a file path
// or a skill name — for a checkpoint metadata line.
//
// The token is model-authored: a path comes straight from a write or edit
// call's file_path argument, and a skill name from a use_skill call. A token
// carrying a line break would put everything after it on a line of its own in a
// user-role checkpoint message, which is where a reserved frame reaches the
// model as a frame rather than as text. The break is written in its encoded form
// so the token occupies one logical line; it is otherwise unchanged, and the
// write statuses (confirmed, failed, no result) are appended by the caller.
func escapeCheckpointPath(token string) string {
	if !strings.ContainsAny(token, "\r\n") {
		return token
	}
	token = strings.ReplaceAll(token, "\r", `\r`)
	return strings.ReplaceAll(token, "\n", `\n`)
}

// isAgentAuthored reports whether another agent authored a turn, which makes its
// text model-authored even in a user-role channel. An unstamped turn and one a
// human authored both answer false: those are the partner's own words.
func isAgentAuthored(t schema.Turn) bool {
	return t.Authorship != nil && t.Authorship.Author.Kind == schema.TranscriptAuthorAgent
}

// collectCheckpointData walks history[:cutoff] and distills it into a
// checkpointData: modified files, tool counts, shell results, user messages,
// final agent responses, working notes, and activated skills.
func collectCheckpointData(history []schema.Turn, cutoff int, resultToolName string) checkpointData {
	data := checkpointData{
		fileWrites:      map[string]checkpointWriteStatus{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
	}

	for i := range cutoff {
		t := history[i]
		switch t.Kind {
		case schema.TurnCheckpoint, schema.TurnSummary:
			data.collectCompaction(t.Message.Text(), t.Kind == schema.TurnSummary)

		case schema.TurnSteering:
			// Steering the human partner sent during a turn is their message
			// too, and so is a parent agent's steer into its delegate, which
			// carries its own attribution; daemon nudges are not.
			if text := t.Message.Text(); (isPartnerSteering(t) || isParentSteering(t)) && text != "" {
				// A parent's steer is another agent speaking, so its body is
				// model-authored even though the turn is carried as the
				// partner's. Only the framing this turn was built with is
				// spared, and that framing opens no frame. A human partner's
				// steer is the authority and keeps its bytes.
				modelAuthored := isModelAuthoredTurn(t)
				if modelAuthored {
					text = sysmsg.EscapeModelAuthoredFrames(text)
				}
				data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text, modelAuthored: modelAuthored})
			}

		case schema.TurnUserInput:
			text := t.Message.Text()
			if text == "" {
				continue
			}
			// A user-role turn another agent authored reads in the partner's
			// channel without being the partner's words. An unstamped turn and a
			// human-stamped one are the partner's own and keep their bytes.
			//
			// Authorship is read before the legacy marker: a task another agent
			// wrote is task text whatever its first line spells, and mining it as
			// a stored compaction would lose the task and carry its body outside
			// the escape.
			if isModelAuthoredTurn(t) {
				text = sysmsg.EscapeModelAuthoredFrames(text)
			} else if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, summaryMarkerStart) {
				// Old-format checkpoint/summary stored as TurnUserInput — carry it
				// just like a typed compaction turn.
				data.collectCompaction(text, strings.HasPrefix(text, summaryMarkerStart))
				continue
			}
			data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text, modelAuthored: isModelAuthoredTurn(t)})

		case schema.TurnTool, schema.TurnToolResults:
			// Extract terminal communicate results (agent responses to the user).
			// Only an accepted result is a completed reply; a rejected result
			// (IsError) stays out of the conversation.
			for _, p := range t.Message.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				if p.ToolResult.Name == resultToolName {
					endTurn, msg := checkpointReplyText(history[:i+1], p.ToolResult.ToolCallID)
					if endTurn && msg != "" && !p.ToolResult.IsError {
						// The reply is the model's own words, and the checkpoint
						// is a user-role message: a frame it invented survives
						// the rollover as input it can cite (#6493). The
						// partner's entries stay verbatim — they are the
						// authority, the model is not.
						data.conversation = append(data.conversation, checkpointConversationEntry{
							Role:          "agent",
							Text:          sysmsg.EscapeModelAuthoredFrames(msg),
							modelAuthored: true,
						})
					}
					continue
				}
			}

		case schema.TurnAssistant:
			// Capture assistant analytical text as working notes.
			//
			// The checkpoint is a user-role message, so model-authored text
			// placed in it speaks in the user's channel. A frame the model
			// invented on its own turn (<delegate-notification>,
			// <system-notification>, [answers]) must not survive into the
			// checkpoint with the spelling a harness-written frame opens with,
			// or the rollover hands the model its own fabrication back as
			// input it can cite as a decision (#6493).
			if text := t.Message.Text(); len(text) > 50 {
				note := escapeWorkingNote(t.Message)
				if len(note) > 500 {
					// Escaping replaces one character with several, so a byte
					// cap that landed inside a rune before can land outside it
					// now, or the reverse. Cut on a rune boundary so the note
					// is always valid UTF-8.
					note = runetrim.Cut(note, 500) + "..."
				}
				data.workingNotes = append(data.workingNotes, note)
			}
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentWebSearch {
					data.toolCounts["web_search"]++
					continue
				}
				if p.Kind != llm.ContentToolCall || p.ToolCall == nil {
					continue
				}
				name := p.ToolCall.Name
				data.toolCounts[name]++

				var args map[string]any
				if len(p.ToolCall.Arguments) > 0 {
					_ = json.Unmarshal(p.ToolCall.Arguments, &args)
				}

				switch name {
				case "edit_file", "write_file":
					if path, ok := args["file_path"]; ok {
						data.recordWrite(fmt.Sprint(path), writeOutcome(history, i, cutoff, p.ToolCall.ID))
					}
				case "apply_patch":
					if patch, ok := args["patch"]; ok {
						status := writeOutcome(history, i, cutoff, p.ToolCall.ID)
						for line := range strings.SplitSeq(fmt.Sprint(patch), "\n") {
							line = strings.TrimSpace(line)
							for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
								if rest, ok := strings.CutPrefix(line, prefix); ok {
									data.recordWrite(rest, status)
								}
							}
						}
					}
				case "shell":
					if cmd, ok := args["command"]; ok {
						cmdStr := fmt.Sprint(cmd)
						cmdStr = truncate(cmdStr, 60)
						exitCode := "?"
						for j := i + 1; j < cutoff; j++ {
							if history[j].Kind != schema.TurnTool && history[j].Kind != schema.TurnToolResults {
								continue
							}
							content := findToolResultByCallID(history[j], p.ToolCall.ID)
							if content != "" {
								exitCode = parseExitCode(content)
								break
							}
						}
						data.lastShellResults = append(data.lastShellResults, fmt.Sprintf("  %q → exit %s", cmdStr, exitCode))
					}
				case "use_skill":
					if sn, ok := args["skill_name"]; ok {
						data.activatedSkills[fmt.Sprint(sn)] = true
					}
				}
			}
		}
	}

	return data
}

// formatCheckpoint renders collected checkpoint data into the [CONTEXT CHECKPOINT]
// … [END CHECKPOINT] block: the fixed metadata sections (transcript pointer,
// modified files, tool counts, recent shell results, activated skills), then the
// conversation and working notes fit into maxChars by shedding the oldest working
// notes first, then the agent's own replies, and never the human partner's
// messages (fitCheckpointConversation).
func formatCheckpoint(data checkpointData, meta *CompactionMeta, maxChars int) string {
	// Build the fixed-size sections first (metadata), then fill remaining
	// budget with user messages and agent responses.
	var fixed strings.Builder

	// Transcript pointer — tells the model how to access full history via transcript tools.
	fixed.WriteString(meta.compactionPointer("checkpoint"))

	// Files modified: only writes whose paired result confirmed success. Failed
	// and result-missing attempts render distinctly, so the checkpoint never
	// reports an attempted write as completed work.
	modified := make([]string, 0, len(data.fileWrites))
	attempted := make([]string, 0, len(data.fileWrites))
	for f, status := range data.fileWrites {
		switch status {
		case writeConfirmed:
			modified = append(modified, escapeCheckpointPath(f))
		case writeFailed:
			attempted = append(attempted, escapeCheckpointPath(f)+" (failed)")
		default:
			attempted = append(attempted, escapeCheckpointPath(f)+" (no result)")
		}
	}
	sort.Strings(modified)
	sort.Strings(attempted)
	if len(modified) > 0 {
		fmt.Fprintf(&fixed, "Files modified: %s\n", strings.Join(modified, ", "))
	}
	if len(attempted) > 0 {
		fmt.Fprintf(&fixed, "Files attempted but not applied: %s\n", strings.Join(attempted, ", "))
	}

	// Tool call counts.
	if total := sumCounts(data.toolCounts); total > 0 {
		fmt.Fprintf(&fixed, "Actions taken: %d tool calls (", total)
		toolNames := make([]string, 0, len(data.toolCounts))
		for name := range data.toolCounts {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)
		for i, name := range toolNames {
			if i > 0 {
				fixed.WriteString(", ")
			}
			fmt.Fprintf(&fixed, "%d %s", data.toolCounts[name], name)
		}
		fixed.WriteString(")\n")
	}

	// Last 3 shell results.
	if len(data.lastShellResults) > 0 {
		start := 0
		if len(data.lastShellResults) > 3 {
			start = len(data.lastShellResults) - 3
		}
		fixed.WriteString("Last shell results:\n")
		for _, r := range data.lastShellResults[start:] {
			fixed.WriteString(r)
			fixed.WriteString("\n")
		}
	}

	// Activated skills.
	allSkills := map[string]bool{}
	for s := range data.activatedSkills {
		allSkills[s] = true
	}
	if meta != nil {
		for _, s := range meta.ActivatedSkills {
			allSkills[s] = true
		}
	}
	if len(allSkills) > 0 {
		skillNames := make([]string, 0, len(allSkills))
		for s := range allSkills {
			skillNames = append(skillNames, escapeCheckpointPath(s))
		}
		sort.Strings(skillNames)
		fmt.Fprintf(&fixed, "Activated skills: %s\n", strings.Join(skillNames, ", "))
	}

	fixedStr := fixed.String()

	// Budget for variable content (conversation + working notes as Markdown).
	// Reserve space for [CONTEXT CHECKPOINT], [END CHECKPOINT], fixed sections,
	// earlier summaries, and the Markdown section headings. The variable
	// content always gets at least minVariableBudget.
	const minVariableBudget = 1000
	frameOverhead := len("[CONTEXT CHECKPOINT]\n") + len("[END CHECKPOINT]\n") + 200
	// The fixed metadata is bounded too: without this a large file list or
	// skill set alone can exceed maxChars, since only the variable content was
	// ever shed. Budgets at or below the frame plus the variable floor cannot
	// honor a hard cap at all (the floor already exceeds them), so metadata is
	// left alone there rather than manufacturing a smaller but unusable block.
	if metadataBudget := maxChars - frameOverhead - minVariableBudget; metadataBudget > 0 {
		fixedStr = fitCheckpointMetadata(fixedStr, meta.compactionPointer("checkpoint"), meta.hasTranscript(), metadataBudget)
	}
	frame := frameOverhead + len(fixedStr)

	// Earlier summaries get up to half the checkpoint, and never so much that
	// the variable content's floor would push it past maxChars. The newest are
	// kept, the last one trimmed from its tail: summaries put permissions and
	// holds first.
	summaryBudget := min(maxChars/2, maxChars-frame-minVariableBudget)
	summaries := data.earlierSummaries
	summariesMarkdown := renderCheckpointEarlierSummaries(summaries)
	for len(summaries) > 1 && len(summariesMarkdown) > summaryBudget {
		summaries = summaries[1:]
		summariesMarkdown = renderCheckpointEarlierSummaries(summaries)
	}
	if len(summaries) == 1 && len(summariesMarkdown) > summaryBudget {
		summary := truncateRendered(summaries[0], summaryBudget, func(text string) int {
			return len(renderCheckpointEarlierSummaries([]string{text}))
		})
		summariesMarkdown = renderCheckpointEarlierSummaries([]string{summary})
		if len(summariesMarkdown) > summaryBudget {
			// The fixed metadata leaves no room for even a trimmed summary.
			summariesMarkdown = ""
		}
	}

	variableBudget := max(maxChars-frame-len(summariesMarkdown), minVariableBudget)

	// Encode conversation and working notes as Markdown. Shed oldest notes
	// first — they may be shed entirely — then fit the conversation into what
	// remains (fitCheckpointConversation).
	conversation := cleanCheckpointConversation(data.conversation)
	workingNotes := data.workingNotes
	conversationMarkdown := renderCheckpointConversation(conversation)
	notesMarkdown := renderCheckpointWorkingNotes(workingNotes)
	for len(workingNotes) > 0 && len(conversationMarkdown)+len(notesMarkdown) > variableBudget {
		workingNotes = workingNotes[1:] // drop oldest note first
		notesMarkdown = renderCheckpointWorkingNotes(workingNotes)
	}
	conversationMarkdown = renderCheckpointConversation(fitCheckpointConversation(conversation, variableBudget-len(notesMarkdown), meta.hasTranscript()))

	// Assemble final checkpoint.
	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT]\n")
	// Earlier summaries come first so the summarizer, which reads a previous
	// compaction from its head, sees them before anything else.
	b.WriteString(summariesMarkdown)
	b.WriteString(fixedStr)
	if conversationMarkdown != "" {
		b.WriteString("\n")
		b.WriteString(conversationMarkdown)
	}
	if notesMarkdown != "" {
		b.WriteString("\n")
		b.WriteString(notesMarkdown)
	}
	b.WriteString("[END CHECKPOINT]\n")
	return b.String()
}

// checkpointMetadataTruncated replaces fixed metadata that would push a
// checkpoint past its cap.
const checkpointMetadataTruncated = "[... checkpoint metadata truncated to keep this checkpoint within its size cap"

// checkpointMetadataMarker closes the disclosure begun by
// checkpointMetadataTruncated. The transcript pointer above the kept metadata
// tells the model where the full history can be read; the marker names it only
// when the session actually keeps a transcript to read.
func checkpointMetadataMarker(hasPointer, transcriptKept bool) string {
	if hasPointer && transcriptKept {
		return checkpointMetadataTruncated + "; the full session history remains available through the transcript reference above ...]\n"
	}
	return checkpointMetadataTruncated + " ...]\n"
}

// fitCheckpointMetadata bounds the fixed metadata sections to budget characters.
// pointer is the compaction pointer the caller wrote at the head of fixed ("" for
// a non-persistent session); it is kept whole because it is the reference that
// makes the dropped metadata recoverable. The remaining sections are cut on a
// rune boundary and followed by the truncation marker. When even the pointer and
// marker do not fit, they still stand: the cap yields here exactly as it does to
// the human partner's messages, and dropping the pointer would leave nothing
// pointing at the recoverable history.
func fitCheckpointMetadata(fixed, pointer string, transcriptKept bool, budget int) string {
	if len(fixed) <= budget {
		return fixed
	}
	if !strings.HasPrefix(fixed, pointer) {
		// The caller did not write this pointer at the head (or there is none):
		// treat the whole metadata block as cuttable rather than protecting a
		// metadata line as if it were the reference.
		pointer = ""
	}
	rest := fixed[len(pointer):]
	marker := "\n" + checkpointMetadataMarker(pointer != "", transcriptKept)
	keep := budget - len(pointer) - len(marker)
	if keep <= 0 {
		return pointer + marker
	}
	kept := runetrim.Cut(rest, keep)
	if len(kept) < len(rest) {
		// Back the cut up to the last complete metadata entry: cutting inside
		// the comma-joined file list would report a partial path as a modified
		// file, the exact confusion this section exists to avoid.
		if end := strings.LastIndexAny(kept, ",\n"); end >= 0 {
			kept = strings.TrimRight(kept[:end], " \t")
		}
	}
	return pointer + kept + marker
}

// truncateRendered shortens text (via truncate, which appends an ellipsis) until
// its rendered form fits in available bytes. Truncating never increases the
// rendered length, so the largest fitting prefix is found by binary search.
func truncateRendered(text string, available int, render func(string) int) string {
	if render(text) <= available {
		return text
	}
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if render(truncate(text, mid)) <= available {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return truncate(text, lo)
}

// findToolResultByCallID finds a tool result in a TurnTool by its ToolCallID
// and returns the string content, or "" if not found.
func findToolResultByCallID(t schema.Turn, toolCallID string) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == toolCallID {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

// writeOutcome classifies the write tool call at history[callIndex] by its
// paired result. A result shed from the compacted prefix leaves the outcome
// unconfirmed rather than inferring success.
func writeOutcome(history []schema.Turn, callIndex, cutoff int, toolCallID string) checkpointWriteStatus {
	result := findToolResultData(history, callIndex+1, cutoff, toolCallID)
	switch {
	case result == nil:
		return writeUnconfirmed
	case result.IsError:
		return writeFailed
	default:
		return writeConfirmed
	}
}

// findToolResultData scans history[from:cutoff] for the tool result part that
// answers toolCallID and returns it, or nil when the prefix holds no result.
func findToolResultData(history []schema.Turn, from, cutoff int, toolCallID string) *llm.ToolResultData {
	for j := from; j < cutoff; j++ {
		if history[j].Kind != schema.TurnTool && history[j].Kind != schema.TurnToolResults {
			continue
		}
		for _, p := range history[j].Message.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == toolCallID {
				return p.ToolResult
			}
		}
	}
	return nil
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}
