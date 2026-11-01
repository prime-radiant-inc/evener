package contextmgr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// maxCheckpointChars caps a checkpoint to avoid ballooning the context. The
// checkpoint either feeds into the LLM summarizer (which replaces it) or — if
// the summarizer is unavailable — becomes the sole history the main model sees.
// 60k chars ≈ 15k tokens: fits comfortably in the cheap model's context
// alongside the instruction prefix, and leaves room for preserved recent turns
// in the main model's context. The human partner's messages a model-written
// compaction turn carries share the same cap. The cap yields to those
// messages: none is ever dropped to meet it (fitCheckpointConversation).
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
// paths use. Opt-in strategies that write their own checkpoints (session-log,
// OODA) do not carry earlier summaries yet; that gap is tracked in #4181.
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
				data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text})
			}

		case schema.TurnUserInput:
			text := t.Message.Text()
			if text == "" {
				continue
			}
			// Old-format checkpoint/summary stored as TurnUserInput — carry it
			// just like a typed compaction turn.
			if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, summaryMarkerStart) {
				data.collectCompaction(text, strings.HasPrefix(text, summaryMarkerStart))
				continue
			}
			data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text})

		case schema.TurnTool, schema.TurnToolResults:
			// Extract terminal communicate results (agent responses to the user).
			// Only an accepted result is a completed reply; a rejected result
			// (IsError) stays out of the conversation.
			for _, p := range t.Message.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				if p.ToolResult.Name == resultToolName {
					endTurn, msg := communicateArgsFromHistory(history[:i+1], p.ToolResult.ToolCallID)
					if endTurn && msg != "" && !p.ToolResult.IsError {
						data.conversation = append(data.conversation, checkpointConversationEntry{Role: "agent", Text: msg})
					}
					continue
				}
			}

		case schema.TurnAssistant:
			// Capture assistant analytical text as working notes.
			if text := t.Message.Text(); len(text) > 50 {
				note := text
				if len(note) > 500 {
					note = note[:500] + "..."
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
			modified = append(modified, f)
		case writeFailed:
			attempted = append(attempted, f+" (failed)")
		default:
			attempted = append(attempted, f+" (no result)")
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
			skillNames = append(skillNames, s)
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
	frame := len("[CONTEXT CHECKPOINT]\n") + len(fixedStr) + len("[END CHECKPOINT]\n") + 200

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
