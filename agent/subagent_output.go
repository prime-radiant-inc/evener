package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// defaultSubagentOutputMaxBytes bounds the subagent_output payload.
const defaultSubagentOutputMaxBytes = 32768

// subagentOutputResult is the wire shape returned by subagent_output. It is a
// peek: the result snapshot or rendered transcript for a child, bounded by
// max_bytes. It is archived evidence, not instructions.
type subagentOutputResult struct {
	AgentID       string `json:"agent_id,omitempty"`
	TranscriptRef string `json:"transcript_ref,omitempty"`
	View          string `json:"view"`
	Content       string `json:"content"`
	Truncated     bool   `json:"truncated"`
	Note          string `json:"note,omitempty"`
}

// execSubagentOutput is the subagent_output dispatcher. It is non-consuming: it
// never sets resultConsumed. It resolves agent_id→snapshot/ref or uses a bare
// transcript_ref, renders the requested view, then bounds it by max_bytes and
// reports truncated.
func execSubagentOutput(s *Session, deps *toolDeps, args map[string]any) (any, error) {
	agentID := strings.TrimSpace(stringArg(args, "agent_id"))
	transcriptRef := strings.TrimSpace(stringArg(args, "transcript_ref"))

	// Runtime XOR: exactly one of agent_id / transcript_ref.
	switch {
	case agentID == "" && transcriptRef == "":
		return nil, errors.New("subagent_output requires exactly one of agent_id or transcript_ref")
	case agentID != "" && transcriptRef != "":
		return nil, errors.New("subagent_output takes agent_id OR transcript_ref, not both")
	}

	view := strings.TrimSpace(stringArg(args, "view"))
	if view == "" {
		view = "result"
	}
	maxBytes := defaultSubagentOutputMaxBytes
	if v := optionalPositiveIntArg(args, "max_bytes"); v != nil {
		maxBytes = *v
	}

	if view == "result" {
		return subagentResultView(s, agentID, transcriptRef, maxBytes)
	}

	// Transcript views (outline|markdown|jsonl): resolve a ref to delegate with.
	ref := transcriptRef
	if agentID != "" {
		sub := s.getSub(agentID)
		if sub == nil {
			return unavailableSubagentOutput(agentID, "", view,
				fmt.Sprintf("agent_id %q is not a tracked child; pass its transcript_ref to read the on-disk transcript", agentID)), nil
		}
		ref = encodeRef("", sub.sess.ID())
	}
	return subagentTranscriptView(deps, ref, agentID, view, maxBytes, args)
}

// subagentResultView returns the retained result snapshot for a tracked child
// WITHOUT consuming it (resultConsumed is never set). A closed child's snapshot
// reports closed=true. An absent child (or a bare transcript_ref, which has no
// in-memory snapshot) yields an unavailable diagnostic.
func subagentResultView(s *Session, agentID, transcriptRef string, maxBytes int) (any, error) {
	if agentID == "" {
		// view=result needs a tracked child; a transcript_ref alone has no snapshot.
		return unavailableSubagentOutput("", transcriptRef, "result",
			"view=result needs a tracked agent_id; use a transcript view (outline|markdown|jsonl) to read a transcript_ref"), nil
	}
	sub := s.getSub(agentID)
	if sub == nil {
		return unavailableSubagentOutput(agentID, "", "result",
			fmt.Sprintf("agent_id %q is not a tracked child (no retained result)", agentID)), nil
	}

	sub.mu.Lock()
	snap := sub.resultSnapshotLocked() // MUST NOT set resultConsumed: this is a peek.
	sub.mu.Unlock()

	b, _ := json.Marshal(snap)
	content, truncated := boundContent(string(b), maxBytes)
	return marshalSubagentOutput(subagentOutputResult{
		AgentID:   agentID,
		View:      "result",
		Content:   content,
		Truncated: truncated,
	})
}

// subagentTranscriptView delegates to the existing transcript read path
// (execReadSessionTranscript), then bounds the rendered output. It tolerates a
// closed/absent registry entry: the transcript file persists on disk independent
// of the registry, so a bare transcript_ref still reads.
func subagentTranscriptView(deps *toolDeps, ref, agentID, view string, maxBytes int, args map[string]any) (any, error) {
	readArgs := map[string]any{
		"transcript_ref": ref,
		"format":         view,
	}
	// Single-turn markdown: spec maps `turn` to range:"N-N" + expand_turn:N.
	if turn := optionalPositiveIntArg(args, "turn"); turn != nil && view == "markdown" {
		readArgs["range"] = fmt.Sprintf("%d-%d", *turn, *turn)
		readArgs["expand_turn"] = *turn
	} else if rng := strings.TrimSpace(stringArg(args, "range")); rng != "" {
		readArgs["range"] = rng
	}

	rendered, err := execReadSessionTranscript(deps, readArgs)
	if err != nil {
		return unavailableSubagentOutput(agentID, ref, view, err.Error()), nil
	}

	b, _ := json.Marshal(rendered)
	content, truncated := boundContent(string(b), maxBytes)
	out := subagentOutputResult{
		View:      view,
		Content:   content,
		Truncated: truncated,
	}
	if agentID != "" {
		out.AgentID = agentID
	}
	out.TranscriptRef = ref
	return marshalSubagentOutput(out)
}

// boundContent enforces maxBytes by truncating content. Returns the bounded string
// and whether it was truncated.
func boundContent(content string, maxBytes int) (string, bool) {
	if maxBytes > 0 && len(content) > maxBytes {
		return content[:maxBytes], true
	}
	return content, false
}

// unavailableSubagentOutput is the structured "no content" diagnostic returned
// when a result/transcript cannot be resolved. It is a normal result (not an
// error) so the model gets an actionable note instead of a tool failure.
func unavailableSubagentOutput(agentID, transcriptRef, view, note string) any {
	out, _ := marshalSubagentOutput(subagentOutputResult{
		AgentID:       agentID,
		TranscriptRef: transcriptRef,
		View:          view,
		Content:       "",
		Truncated:     false,
		Note:          note,
	})
	return out
}

func marshalSubagentOutput(out subagentOutputResult) (any, error) {
	b, _ := json.Marshal(out)
	return string(b), nil
}
