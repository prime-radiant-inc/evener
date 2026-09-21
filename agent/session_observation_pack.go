package agent

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// Observation packing (SoL-Pi auto-research design, mechanism 3): tool
// results over 10 KiB are archived to the session artifact store the first
// time a request would carry them, replayed in full for the next two
// requests (so the model can act on the result immediately), and after that
// replaced in REQUEST contexts by a stable handle plus the original size and
// head/tail excerpt lines. The transcript and the in-memory history keep the
// full result; only the message list handed to the provider is packed, and
// the full content stays retrievable on demand through the same
// artifact-read machinery truncated shell output already uses
// (read_transcript with an artifact:<id> ref).
//
// The decision lives at the request-build boundary — packRequestObservations,
// called by prepareModelRequestWithError right after history expansion —
// rather than at tool-result recording, so it covers every tool uniformly
// (any result in history, from any tool, restored sessions included) and
// stays deterministic and offline-testable: one call per model request.
const (
	// observationPackThresholdBytes is the packing threshold: results
	// strictly over 10 KiB pack. This matches the oversized-result boundary
	// the research oracle measured (agent/research oracle.go,
	// measureLargeObservations).
	observationPackThresholdBytes = 10 * 1024
	// observationPackFullLooks is how many request builds carry a packed
	// observation in full before the packed view replaces it.
	observationPackFullLooks = 2
	// observationPackExcerptLines is the head and tail line count of the
	// packed view's excerpt.
	observationPackExcerptLines = 10
	// observationPackExcerptLineMax caps one excerpt line in bytes so a
	// single long line cannot keep the packed view large.
	observationPackExcerptLineMax = 240
)

// packedObservation is the packing state of one oversized tool result.
type packedObservation struct {
	ref        string   // artifact handle for the full content; "" means unpackable
	bytes      int      // original content size in bytes
	lines      int      // original content line count
	head       []string // first excerpt lines (each already line-capped)
	tail       []string // last excerpt lines, never overlapping head
	fullSends  int      // request builds that carried the full content
	unpackable bool     // archiving failed: this result always replays in full
}

// observationPack is the per-session packing state, keyed by tool call ID. It
// is a value on Session so a zero Session needs no initialization; the mutex
// guards the map, and only the turn loop's request-build path reaches it in
// production.
type observationPack struct {
	mu      sync.Mutex
	entries map[string]*packedObservation
}

// decide returns the request-build view for one oversized result content: the
// content itself while the full-look budget remains (consuming one look), else
// the packed view. The first encounter archives the content through store and
// computes the excerpts. When archiving fails the result is marked unpackable
// and always replays in full, and warning carries the reason for the caller
// to surface once.
func (p *observationPack) decide(store artifactStore, callID, content string) (view string, packed bool, warning string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	po := p.entries[callID]
	if po == nil {
		po = &packedObservation{bytes: len(content)}
		po.head, po.tail, po.lines = observationExcerpts(content)
		if store == nil {
			po.unpackable = true
			warning = "observation packing: no artifact store; a large tool result will replay in full"
		} else {
			ref, err := store.Put([]byte(content))
			if err != nil {
				po.unpackable = true
				warning = "observation packing: archiving a large tool result failed: " + err.Error()
			} else {
				po.ref = ref
			}
		}
		if p.entries == nil {
			p.entries = make(map[string]*packedObservation)
		}
		p.entries[callID] = po
	}
	if po.unpackable || po.fullSends < observationPackFullLooks {
		po.fullSends++
		return content, false, warning
	}
	return po.render(), true, ""
}

// render builds the packed view: the original size and line count, the head
// and tail excerpt lines, and the handle plus the read_transcript instruction
// that retrieves the full content on demand.
func (po *packedObservation) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[packed observation: original %d bytes in %d lines]\n", po.bytes, po.lines)
	for _, line := range po.head {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if omitted := po.lines - len(po.head) - len(po.tail); omitted > 0 {
		fmt.Fprintf(&b, "[... %d lines omitted ...]\n", omitted)
	}
	for _, line := range po.tail {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Full observation: %s\nRead with: read_transcript(transcript_ref=%q)\n", po.ref, po.ref)
	return b.String()
}

// observationExcerpts splits content into its head and tail excerpt lines.
// head holds up to observationPackExcerptLines leading lines, tail up to as
// many trailing lines without overlapping head. lines is the total line count
// (a trailing newline ends the last line rather than starting an empty one).
// Each returned line is capped at observationPackExcerptLineMax bytes on a
// rune boundary.
func observationExcerpts(content string) (head, tail []string, lines int) {
	all := strings.Split(content, "\n")
	if n := len(all); n > 0 && all[n-1] == "" {
		all = all[:n-1]
	}
	lines = len(all)
	if lines == 0 {
		return nil, nil, 0
	}
	headEnd := min(observationPackExcerptLines, lines)
	tailStart := max(lines-observationPackExcerptLines, headEnd)
	for _, line := range all[:headEnd] {
		head = append(head, observationExcerptLine(line))
	}
	for _, line := range all[tailStart:] {
		tail = append(tail, observationExcerptLine(line))
	}
	return head, tail, lines
}

// observationExcerptLine caps one excerpt line at observationPackExcerptLineMax
// bytes, cutting on a rune boundary and stating the full line length so the
// model knows how much of the line is gone.
func observationExcerptLine(line string) string {
	if len(line) <= observationPackExcerptLineMax {
		return line
	}
	cut := observationPackExcerptLineMax
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + fmt.Sprintf("[... line truncated: %d bytes ...]", len(line))
}

// packRequestObservations packs the oversized tool results in one request's
// message list. It is the single replay-side seam of observation packing:
// prepareModelRequestWithError calls it on the expanded history (and on the
// delegate controller's projection, which arrives through the same variable),
// so every tool result that reaches a request goes through here. The stored
// turns and the transcript are never mutated: packing replaces content only
// in the returned message list, copy-on-write over the shared
// *ToolResultData payloads (the same discipline maskObservations documents).
//
// Results at or under observationPackThresholdBytes, non-string results, and
// Evidence-Preserving Reducer receipts pass through untouched: a receipt is
// already the small, verified view of a log (isLogReceiptContent recognizes
// the rendered prefix), so packing one would double-process a result the
// reducer already reduced. With ObservationPacking off the input returns
// unchanged, byte-identical to a session without the mechanism.
func (s *Session) packRequestObservations(history []llm.Message) []llm.Message {
	if !s.cfg.ObservationPacking {
		return history
	}
	// decided dedups within this build: one look consumed per unique result,
	// and a duplicated occurrence renders the same view as the first.
	decided := make(map[string]string)
	var warnings []string
	for i := range history {
		var rebuilt []llm.ContentPart
		for j, part := range history[i].Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.ToolCallID == "" {
				continue
			}
			callID := part.ToolResult.ToolCallID
			if view, done := decided[callID]; done {
				if view == "" {
					continue
				}
				if rebuilt == nil {
					rebuilt = make([]llm.ContentPart, len(history[i].Content))
					copy(rebuilt, history[i].Content)
				}
				packedResult := *part.ToolResult
				packedResult.Content = view
				rebuilt[j].ToolResult = &packedResult
				continue
			}
			content, ok := part.ToolResult.Content.(string)
			if !ok || isLogReceiptContent(content) || len(content) <= observationPackThresholdBytes {
				continue
			}
			view, packed, warning := s.obsPack.decide(s.artifactStore, callID, content)
			decided[callID] = ""
			if packed {
				decided[callID] = view
				if rebuilt == nil {
					rebuilt = make([]llm.ContentPart, len(history[i].Content))
					copy(rebuilt, history[i].Content)
				}
				packedResult := *part.ToolResult
				packedResult.Content = view
				rebuilt[j].ToolResult = &packedResult
			}
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}
		if rebuilt != nil {
			msg := history[i]
			msg.Content = rebuilt
			history[i] = msg
		}
	}
	for _, warning := range warnings {
		s.emit(events.EventWarning, events.WarningData{Message: warning})
	}
	return history
}
