package apptranscript

import (
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// ShellToolNames are the tool names whose result carries a process exit code
// that the transcript reads as failure. It is the transcript package's list,
// re-exported here for this package's own consumers — one list, so the reader
// that counts a finished transcript and the daemon that counts a running one
// can never disagree about what a shell call is.
var ShellToolNames = transcript.ShellToolNames

// FailedToolCallsFromFile counts how many of a session's tool calls failed,
// over the WHOLE transcript.
//
// It exists because a client cannot answer the question. thread/read windows
// turns (turnLimit), so a count over the items a client holds covers whatever
// fraction of the session happens to be loaded — measured at ~47% on a long
// real session. For failures specifically an undercount is worse than no
// answer: "0 failed", computed from a partial window, states in the session's
// own chrome that nothing went wrong, which is exactly the misreading this
// count exists to prevent.
//
// WHAT COUNTS is the same thing the transcript marks with a --danger glyph, so
// the figure and the glyphs can never disagree:
//
//   - a tool result carrying an error (ToolResultData.IsError — the wire's
//     ThreadItem.Error, and the "failed" status SettledToolStatus stamps);
//   - a SHELL call that ran and exited nonzero. That is a clean tool result
//     (IsError false) whose command failed, and the renderer marks it anyway.
//     On the session measured in kata hw2n, counting only IsError would report
//     1 against 6 visible glyphs.
//
// communicate results are excluded: ProjectTurn drops them, so they render no
// row and wear no glyph. A result whose own record omits its name is resolved
// from the call that announced it, the same way ProjectTurn resolves it.
//
// fromEntryOrdinal is the 1-based entry ordinal at which the session's OWN
// history begins — a fork child's SessionMeta.DivergenceTurn. A child
// transcript opens with a verbatim copy of the parent's prefix, and those
// failures were the parent's; charging them to the child is the same
// attribution bug the token sum's identical bound exists to prevent. Pass 0
// (or 1) for a session that inherited nothing.
//
// A count of zero is a real measurement, not an absence: the transcript records
// every tool result and none of them failed. Only an unreadable transcript (a
// legacy format_version 1 file, a missing one) is unknown, and that arrives as
// an error rather than a fabricated zero.
func (c *TurnCache) FailedToolCallsFromFile(path string, maxLineBytes int, fromEntryOrdinal int) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat transcript: %w", err)
	}
	identity := failedToolCallsKey{
		size:           info.Size(),
		modUnixNano:    info.ModTime().UnixNano(),
		fileIdentity:   fileIdentity(info),
		changeIdentity: fileChangeIdentity(info),
		fromOrdinal:    fromEntryOrdinal,
	}

	c.mu.Lock()
	if entry, ok := c.entries[path]; ok && entry.failedToolCalls != nil && entry.failedToolCalls.key == identity {
		count := entry.failedToolCalls.count
		c.touch(path)
		c.mu.Unlock()
		return count, nil
	}
	c.mu.Unlock()

	count, err := scanFailedToolCalls(path, maxLineBytes, fromEntryOrdinal)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	entry := c.entries[path]
	entry.failedToolCalls = &failedToolCallsMemo{key: identity, count: count}
	c.entries[path] = entry
	c.touch(path)
	c.evictLocked()
	c.mu.Unlock()
	return count, nil
}

// scanFailedToolCalls reads the transcript once, decoding only the tool calls
// and tool results. It reuses scanSemanticTranscript so the format gate (v1
// rejection, unknown-field strictness, header validation) is exactly the one
// every other reader in this package applies.
func scanFailedToolCalls(path string, maxLineBytes int, fromEntryOrdinal int) (int, error) {
	count := 0
	ordinal := 0
	// toolNames resolves a result whose own record omits its name, mirroring
	// ProjectTurn's map of the same name. It is filled from EVERY assistant
	// entry, including ones before the divergence cut: a fork child's own
	// result can answer a call the inherited prefix announced.
	toolNames := map[string]string{}
	if _, err := scanSemanticTranscript(path, maxLineBytes, func(raw json.RawMessage) error {
		ordinal++
		var record failedToolCallEntry
		if err := json.Unmarshal(raw, &record); err != nil {
			// Unreachable for any line scanSemanticTranscript admits: it has
			// already strictly decoded the whole entry into transcript.Entry,
			// of which this is a field-for-field subset. A failure here means
			// this struct has drifted from schema.Turn, and skipping the record
			// would silently undercount — reporting a wrong count is worse than
			// reporting none, so surface it.
			return fmt.Errorf("decode transcript entry tool calls: %w", err)
		}
		counting := ordinal >= fromEntryOrdinal
		for _, part := range record.Turn.Message.Content {
			switch {
			case part.ToolCall != nil && part.Kind == llm.ContentToolCall:
				toolNames[part.ToolCall.ID] = part.ToolCall.Name
			case part.ToolResult != nil && part.Kind == llm.ContentToolResult:
				if counting && failedToolResult(part.ToolResult, toolNames) {
					count++
				}
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	observeIndexRead(ReadStats{failureScans: 1})
	return count, nil
}

// failedToolResult applies the shared failure rule to this package's narrow
// decode of a tool result, resolving a nameless result from the call that
// announced it first. The rule itself lives in the transcript package — see
// transcript.FailedToolResult — because the daemon counts the same failures
// live as it writes them, and two copies of the rule are two rules.
func failedToolResult(result *failedToolCallResult, toolNames map[string]string) bool {
	name := result.Name
	if name == "" {
		name = toolNames[result.ToolCallID]
	}
	return transcript.FailedToolResult(name, result.IsError, result.ToolState)
}

// failedToolCallEntry decodes the few fields the count needs.
// scanSemanticTranscript has already validated the full record, so this narrow
// view can ignore the rest rather than paying to decode whole message bodies
// (including inline image bytes) per line.
type failedToolCallEntry struct {
	Turn struct {
		// No turn kind: the content parts are the discriminator this needs.
		// Tool calls and tool results only ever appear on the assistant and
		// tool-result kinds, so matching on the part itself is both narrower
		// and immune to a new turn kind that carries them.
		Message struct {
			Content []struct {
				Kind     llm.ContentKind `json:"kind"`
				ToolCall *struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"tool_call"`
				ToolResult *failedToolCallResult `json:"tool_result"`
			} `json:"content"`
		} `json:"message"`
	} `json:"turn"`
}

type failedToolCallResult struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	IsError    bool            `json:"is_error"`
	ToolState  json.RawMessage `json:"tool_state"`
}

// failedToolCallsKey is the file identity a memoized count is valid for. It
// mirrors usageTotalKey exactly, for the same reasons: object identity, size,
// mtime (as nanos, so the key stays comparable with ==) and platform change
// time, plus the divergence ordinal, since two ordinals over one file are two
// different answers.
type failedToolCallsKey struct {
	size           int64
	modUnixNano    int64
	fileIdentity   string
	changeIdentity string
	fromOrdinal    int
}

type failedToolCallsMemo struct {
	key   failedToolCallsKey
	count int
}
