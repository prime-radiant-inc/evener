package agent

import (
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/llm"
)

// escapeModelAuthoredTurnFrames returns msg with line-start harness input
// frames defused in the text of an assistant-role turn, so a request the
// session serves cannot hand the model its own earlier fabrication back with
// the spelling a harness-written frame or a real reply opens with (#6493).
//
// Only the assistant role is escaped. RoleUser is the human partner speaking —
// their text is delivered as they typed it, and an escaped copy there would
// change what the model reads as their words — and a tool role is a result the
// harness itself recorded. Assistant text is the one role whose bytes the model
// invented, and the one that must not come back looking like input.
//
// A checkpoint or summary turn is user-role and so is not reached here, even
// though part of its body is the model's own text. Escaping it would mean
// deciding authorship by scanning for structural headings, and a model can
// write those headings itself: the scanner and the compaction reader then
// disagree about the same bytes. That is tracked separately as #6734, whose
// direction is provenance carried on the content part rather than re-derived
// from text.
//
// The turn is never modified: the caller gets a message whose changed text
// parts are copies, so the transcript keeps exactly what the model wrote.
func escapeModelAuthoredTurnFrames(msg llm.Message) llm.Message {
	if msg.Role != llm.RoleAssistant {
		return msg
	}
	var indexes []int
	var texts []string
	for i, p := range msg.Content {
		// A machinery part is content this session manufactured, and escaping
		// it would rewrite the harness's own note. Every genuine one is
		// user-role: the constructors that set the flag (llm.UserMachinery,
		// llm.MachineryText) are used on user-role messages only, so the
		// early return above already keeps them out of this function.
		//
		// On an assistant part the flag cannot be genuine, because nothing in
		// the tree builds one. It can only come from inferPreFlagMachinery's
		// decode-time guess for a transcript written before the flag existed,
		// which classifies any text part shaped like one whole
		// <system-notification> block — the exact shape a model invents when
		// it imitates one. Skipping that part would replay the model's
		// imitation verbatim, so the flag is not consulted here (#6493
		// review).
		if p.Kind != llm.ContentText || p.Text == "" {
			continue
		}
		indexes = append(indexes, i)
		texts = append(texts, p.Text)
	}
	if len(indexes) == 0 {
		return msg
	}

	// Scan the text as the adapter will assemble it, not part by part. Both
	// adapters group consecutive text parts by Phase and emit one message per
	// group: Chat Completions joins a turn's parts into one message (its Phase
	// is always empty, so the whole turn is one group), and Responses emits a
	// separate assistant message per phase. A fence one part opens and the next
	// closes is therefore one fenced block within a group, while the next
	// phase starts a fresh message with fresh fence state. Scanning part by
	// part would read the closing fence as a new opener and leave the frame
	// after it unescaped, and scanning across a phase boundary would treat a
	// new message's frame as still fenced (#6493 review).
	var parts []llm.ContentPart
	for _, group := range phaseGroups(texts, msg.Content, indexes) {
		joined := strings.Join(group.texts, "\n")
		escaped := sysmsg.EscapeModelAuthoredFrames(joined)
		if escaped == joined {
			continue
		}
		// Escaping rewrites a frame's opening character and never adds or
		// removes a line break, so the escaped text splits back into the same
		// per-part line counts it was joined from.
		lines := strings.Split(escaped, "\n")
		at := 0
		for k, text := range group.texts {
			n := strings.Count(text, "\n") + 1
			if at+n > len(lines) {
				// The escape changed the line structure; leave the turn as
				// stored rather than hand back text split somewhere it was not.
				return msg
			}
			segment := strings.Join(lines[at:at+n], "\n")
			at += n
			if segment == text {
				continue
			}
			if parts == nil {
				parts = slices.Clone(msg.Content)
			}
			parts[group.indexes[k]].Text = segment
		}
	}
	if parts == nil {
		return msg
	}
	msg.Content = parts
	return msg
}

// textGroup is one wire message's worth of an assistant turn's text: the
// consecutive parts an adapter emits together.
type textGroup struct {
	phase   string
	indexes []int
	texts   []string
}

// phaseGroups walks content in order and splits the selected text parts at
// phase changes, which is where both adapters start a new wire message. A part
// whose Phase differs from the previous one begins a new group; Phase is empty
// throughout for a turn Chat Completions assembles, so the whole turn is one
// group there.
func phaseGroups(texts []string, content []llm.ContentPart, indexes []int) []textGroup {
	groups := make([]textGroup, 0, 1)
	for k, text := range texts {
		phase := content[indexes[k]].Phase
		if len(groups) == 0 || groups[len(groups)-1].phase != phase {
			groups = append(groups, textGroup{phase: phase})
		}
		g := &groups[len(groups)-1]
		g.indexes = append(g.indexes, indexes[k])
		g.texts = append(g.texts, text)
	}
	return groups
}
