package contextmgr

import (
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// partnerAuthorityRule keeps a summary from adding or dropping the human
// partner's permissions, approvals, holds and stops. The continuing agent
// treats the summary as the record of what it may do, so a paraphrase that
// hardens into a new hold, or drops a standing permission, changes its
// behavior (#4173). Both summary prompts carry it, and caller instructions
// cannot override it. Both put these quotes first in the summary, so when a
// re-compaction caps an oversized previous summary to its head, the quotes are
// what survives.
const partnerAuthorityRule = `Quote every permission, approval, hold or stop your human partner gave, word for word from their "User:" message, and say which of their messages it came from. Carry forward only what the conversation contains: never add one it lacks, never turn a question, a suggestion or your own caution into one, and never drop one that is still in force. An earlier compaction counts only for what it quotes. If there are none, say "None."`

// permissionsSection is the default summary's leading section, where the
// summary quotes the human partner's permissions and holds.
const permissionsSection = "Permissions and Holds"

// defaultSummaryPrefix is the instruction block used when no caller instructions
// are provided. It mandates eight specific sections, permissions and holds
// first, and directs the LLM to err on the side of verbosity.
var defaultSummaryPrefix = `You are performing a CONTEXT CHECKPOINT COMPACTION. This session is being continued from a previous conversation that ran out of context. Create a detailed handoff summary that another instance of yourself will use to seamlessly continue the work.

Your summary MUST include ALL of the following sections, in this order:

## ` + permissionsSection + `
` + partnerAuthorityRule + `

## Conversation Timeline
Reproduce user messages and agent replies in chronological, interleaved order. Preserve user messages verbatim. Summarize agent replies only when needed for brevity, but keep commitments, decisions, and final answers clear.

## Progress
What has been accomplished so far. Be specific about:
- Files created or modified (full paths)
- Specific changes made to each file
- Tests written or run and their results
- Commands executed and their outcomes

## Key Decisions
Important decisions made during the session and why. Include:
- Architecture or design choices
- Trade-offs considered
- User preferences or constraints discovered

## Current State
Precisely what was being worked on when context ran out:
- What file was being edited
- What problem was being debugged
- What test was failing

## Pending Work
Clear, actionable next steps that remain. Be specific enough that the next instance can immediately start working.

## Analytical Findings
Specific technical discoveries made during the session:
- What algorithms, approaches, or parameter values were found to work
- What debugging insights were gained (root causes, validated hypotheses)
- What code patterns, data structures, or API behaviors were discovered
- Include specific values, numbers, and names — not vague summaries

## Critical Context
Any data, file paths, variable names, error messages, API details, or other specific information needed to continue. Focus on information that CANNOT be re-derived from reading the codebase.

Be thorough and structured. Err on the side of including too much rather than too little — lost context is expensive, extra tokens are cheap.

`

// summarySections are the section names the default summary prompt requires,
// read from the prompt itself so the reply check and the prompt cannot drift.
var summarySections = promptSectionNames(defaultSummaryPrefix)

// promptSectionNames returns the names of a prompt's "## " headings, in order.
func promptSectionNames(prompt string) []string {
	var names []string
	for line := range strings.Lines(prompt) {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "## "); ok {
			names = append(names, name)
		}
	}
	return names
}

// checkNotCutOff rejects a summarization reply the model cut off at its output
// limit: it may have dropped what only it records, a hold among the partner
// messages a carry left out, now or in an earlier summary it replaces. Like
// any rejected reply it falls back to the session model, and if that is cut
// off too the summary fails and the checkpoint stands.
func checkNotCutOff(reply llm.Response) error {
	if reply.Finish.Reason == llm.FinishReasonLength {
		return errSummaryOutputLimit
	}
	return nil
}

// checkSummaryReply rejects a summarization reply that cannot stand in for the
// history it folds: an empty one, or, under the default prompt, one with none
// of the required sections with content under it. Weak models sometimes
// answer the prompt in character ("I'll read the plan, then...") instead of
// summarizing (#3978). One filled section is enough, so a summary that drops
// some sections still keeps what it has. Caller instructions replace the
// sections, so under them only emptiness is checked.
//
// The permissions-and-holds section is not required either. Its presence
// would not show the quotes are faithful or complete (a reply can say "None."
// and drop them), and rejecting a summary for lacking it would discard
// everything else the summary holds (TestSummarizeWithLLM_StoresReplyWithRequiredSection
// pins that one filled section is accepted). The guarantee that the human
// partner's permissions and holds survive compaction does not rest on this
// check or on the model: by ruling on #4173, the summary turn carries the
// partner's messages verbatim beside the reply (partnerMessagesMarkdown).
func checkSummaryReply(reply, instructions string) error {
	if strings.TrimSpace(reply) == "" {
		return errSummaryEmptyReply
	}
	if instructions != "" {
		return nil
	}
	// Once a section heading appears, any later line is content — subheadings,
	// bold labels and "#3978" lines included — except another section heading.
	// The permissions section restates the human partner's own words, so
	// content there alone carries none of the folded work: a reply that is
	// only "## Permissions and Holds\nNone." is no summary.
	// A heading inside a fenced block is quoted text, not a section.
	// This reads the reply as the model wrote it and edits nothing: the framing a
	// model may echo back around its own text stays in the stored turn.
	inSection := false
	var fence fenceTracker
	// The lines are read as written: the reply's own indentation is Markdown's,
	// and trimming it would turn an indented block into a fence.
	for raw := range strings.Lines(reply) {
		line := strings.TrimSpace(raw)
		// The tracker reads the line as written: a delimiter indented by four
		// spaces is code inside the block, not a delimiter.
		if fence.step(raw) {
			continue
		}
		// A model sometimes echoes the framing around its own heading. Read through
		// the markers at the line's edges so the heading still counts; a marker
		// inside the line's own text is that text. An indented line is code: it is
		// content under a section, never one.
		if section, rest, ok := summarySectionHeading(withoutEdgeFraming(line)); ok && !fence.inside() && !indentedCode(raw) {
			inSection = !strings.EqualFold(section, permissionsSection)
			// The heading's own text counts only if it is text: the edge markers
			// are read through above, but markup can hold one inside it
			// ("**Progress: [END SUMMARY]**"), and a heading with nothing but
			// framing under it has summarized nothing.
			if inSection && rest != "" && !markerOnly(rest) {
				return nil
			}
			continue
		}
		// A line of framing markers is not content: quoting the framing is not
		// summarizing the folded history, whether the markers sit on their own line
		// or inside a fenced block.
		if inSection && line != "" && !markerOnly(line) {
			return nil
		}
	}
	return errSummaryMissingSection
}

// summarySectionHeading reports whether line is a heading naming one of the
// summary sections, and returns the section name as written and any text after
// it on the same line:
// "## Progress", "### progress", "**Progress**", "## Progress: fixed it",
// "**Progress:** fixed it". It needs heading markup: a bare "Progress" is
// prose, and "#3978" (no space after the #) is not a heading.
func summarySectionHeading(line string) (section, rest string, ok bool) {
	body := line
	switch {
	case strings.HasPrefix(line, "#"):
		body = strings.TrimLeft(line, "#")
		if !strings.HasPrefix(body, " ") {
			return "", "", false
		}
	case !strings.HasPrefix(line, "**"):
		return "", "", false
	}
	body = strings.ReplaceAll(body, "**", "")
	name, rest, _ := strings.Cut(body, ":")
	name = strings.TrimSpace(name)
	if !slices.ContainsFunc(summarySections, func(section string) bool { return strings.EqualFold(name, section) }) {
		return "", "", false
	}
	return name, strings.TrimSpace(rest), true
}

// buildSummaryPrompt constructs the full LLM prompt for context compaction.
// When instructions are non-empty the prompt is instruction-led: the
// mandatory-sections block is replaced by the caller's directive.
// When instructions are empty the default prompt is used unchanged.
func buildSummaryPrompt(historyText, instructions string) string {
	if instructions != "" {
		return `You are performing a CONTEXT CHECKPOINT COMPACTION for an agent continuing its own work.

## CALLER INSTRUCTIONS (these take precedence over the general guidance, never over your human partner's permissions and holds)
` + instructions + `

Follow the caller instructions above when deciding what to preserve verbatim and what to drop or condense. Where they conflict with the general guidance below, the caller instructions win, except over the rule on your human partner's permissions and holds. Still produce a coherent handoff: keep decisions, current state, and actionable next steps. Do not invent content.

` + partnerAuthorityRule + ` Put this first in your summary; the caller instructions do not override it.

` + historyText
	}
	return defaultSummaryPrefix + historyText
}

// isPartnerSteering reports whether a steering turn is the human partner's own
// message, sent during a turn, rather than a daemon nudge. The checkpoint's
// collector and the summarizer's "User:" label both use it, so they agree on
// whose words a steer is.
func isPartnerSteering(t schema.Turn) bool {
	return t.Kind == schema.TurnSteering && t.SteeringSource == events.SteeringSourceUser
}

// isParentSteering reports whether a steering turn is the delegate's parent
// agent speaking into the running turn (delegate_send). Compaction keeps it
// like the partner's own messages, since it is the instruction the delegate
// works from, but it is not the human partner's: only isPartnerSteering
// answers for them.
func isParentSteering(t schema.Turn) bool {
	return t.Kind == schema.TurnSteering && t.SteeringSource == events.SteeringSourceParent
}
