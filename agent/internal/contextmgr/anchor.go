package contextmgr

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"strconv"

	"primeradiant.com/evener/agent/schema"
)

// PrefixAnchor fingerprints the last turn a count measured at measuredLen covers,
// so a same-length rewritten prefix is recognizable (issue #7154). Empty if out of range.
func PrefixAnchor(history []schema.Turn, measuredLen int) string {
	if measuredLen <= 0 || measuredLen > len(history) {
		return ""
	}
	return turnAnchor(&history[measuredLen-1])
}

// prefixAnchorHolds reports whether history still carries the turn a count was
// anchored to. An empty anchor or measuredLen 0 means no prefix identity, so it holds.
func prefixAnchorHolds(anchor string, history []schema.Turn, measuredLen int) bool {
	if anchor == "" || measuredLen <= 0 {
		return true
	}
	return PrefixAnchor(history, measuredLen) == anchor
}

// turnAnchor fingerprints what one turn contributes to a request's input tokens:
// its kind, role, and every content part's kind and payload. The transcript copy a
// resume makes keeps the content a persist pass may re-mint TurnID on.
func turnAnchor(t *schema.Turn) string {
	h := fnv.New64a()
	writeAnchor(h, string(t.Kind))
	writeAnchor(h, string(t.Message.Role))
	writeAnchor(h, t.Message.Name)
	writeAnchor(h, t.Message.ToolCallID)
	for i := range t.Message.Content {
		p := &t.Message.Content[i]
		writeAnchor(h, string(p.Kind))
		writeAnchor(h, p.Text)
		switch {
		case p.ToolCall != nil:
			writeAnchor(h, p.ToolCall.ID)
			writeAnchor(h, p.ToolCall.Name)
			writeAnchor(h, string(p.ToolCall.Arguments))
		case p.ToolResult != nil:
			writeAnchor(h, p.ToolResult.ToolCallID)
			writeAnchor(h, p.ToolResult.Name)
			if s, ok := p.ToolResult.Content.(string); ok {
				writeAnchor(h, s)
			} else {
				writeAnchor(h, fmt.Sprint(p.ToolResult.Content))
			}
			writeAnchor(h, string(p.ToolResult.ImageData))
		case p.Thinking != nil:
			writeAnchor(h, p.Thinking.Text)
			writeAnchor(h, p.Thinking.Signature)
		case p.Image != nil:
			writeAnchor(h, p.Image.URL)
			writeAnchor(h, string(p.Image.Data))
		case p.Document != nil:
			writeAnchor(h, p.Document.URL)
			writeAnchor(h, string(p.Document.Data))
		case p.Audio != nil:
			writeAnchor(h, p.Audio.URL)
			writeAnchor(h, string(p.Audio.Data))
		case p.WebSearch != nil:
			writeAnchor(h, p.WebSearch.Query)
			writeAnchor(h, string(p.WebSearch.Raw))
		}
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// writeAnchor frames a string into h so adjacent fields cannot run together.
func writeAnchor(h hash.Hash64, s string) {
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = h.Write(n[:])
	_, _ = io.WriteString(h, s)
}
