// Package research implements the SoL-Pi auto-research loop's offline
// machinery: the oracle analyzer over real session transcripts, and the
// rollout runner that drives headless harness runs against committed
// research environments. See docs/superpowers/specs/2026-09-19-sol-pi-auto-research-loop-design.md.
package research

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type sessionTranscript struct {
	Path      string
	SessionID string
	ModTime   time.Time
}

// walkSessionTranscripts finds transcript files under
// <stateBase>/projects/*/sessions/*.transcript.jsonl, newest first.
func walkSessionTranscripts(stateBase string, limit int) ([]sessionTranscript, error) {
	pattern := filepath.Join(stateBase, "projects", "*", "sessions", "*.transcript.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("walk transcripts: %w", err)
	}
	out := make([]sessionTranscript, 0, len(matches))
	for _, p := range matches {
		st, err := os.Stat(p)
		if err != nil {
			continue // raced with deletion; skip
		}
		out = append(out, sessionTranscript{
			Path:      p,
			SessionID: strings.TrimSuffix(filepath.Base(p), ".transcript.jsonl"),
			ModTime:   st.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// loadEntries decodes one transcript file. The first line must be the v2
// header: a corrupt header, and equally a file with no header at all
// (including a 0-byte file), is an error. Undecodable entry lines are skipped
// and counted: real corpora contain torn tail lines, and the oracle measures,
// it does not reject.
func loadEntries(path string) (entries []transcript.Entry, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			if _, err := transcript.DecodeHeader(line); err != nil {
				return nil, 0, fmt.Errorf("%s: %w", path, err)
			}
			first = false
			continue
		}
		e, err := transcript.DecodeEntry(line)
		if err != nil {
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan %s: %w", path, err)
	}
	if first {
		return nil, 0, fmt.Errorf("%s: no transcript header", path)
	}
	return entries, skipped, nil
}

// AdjacencyStats counts edit-then-command cycles an Action Fusion style
// tool change would collapse, and the tokens of the intervening request
// that disappears: the request whose input re-sent the edit result and
// whose output chose the test command.
type AdjacencyStats struct {
	Cycles                int
	SavedPromptTokens     int
	SavedCompletionTokens int
}

var researchMutationTools = map[string]bool{
	"edit_file":   true,
	"write_file":  true,
	"apply_patch": true,
}

// researchTestBuildRe matches shell commands whose output an agent typically
// consumes right after a file mutation: test, build, vet, lint. Fusing these
// into the mutation call removes the intervening model round trip.
var researchTestBuildRe = regexp.MustCompile(`\b(go test|go build|go vet|make(?:\s+\S+)?|npm test|npm run (?:test|build)|pytest|cargo (?:test|build))\b`)

// toolCallsOf returns the tool calls present in a message, in order.
func toolCallsOf(msg llm.Message) []llm.ToolCallData {
	var out []llm.ToolCallData
	for i := range msg.Content {
		if part := msg.Content[i]; part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			out = append(out, *part.ToolCall)
		}
	}
	return out
}

// shellCommandOf extracts the command string from a shell tool call's
// arguments. ParsedArguments is preferred; the raw JSON is the fallback.
func shellCommandOf(call llm.ToolCallData) string {
	if v, ok := call.ParsedArguments["command"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	var raw struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Arguments, &raw); err == nil {
		return raw.Command
	}
	return ""
}

func measureAdjacency(entries []transcript.Entry) AdjacencyStats {
	var st AdjacencyStats
	for i := range entries {
		turn := entries[i].Turn
		if turn.Kind != schema.TurnAssistant {
			continue
		}
		calls := toolCallsOf(turn.Message)
		mutated := false
		for _, c := range calls {
			if researchMutationTools[c.Name] {
				mutated = true
				break
			}
		}
		if !mutated {
			continue
		}
		// The next assistant turn is the request fusion would remove.
		for j := i + 1; j < len(entries); j++ {
			next := entries[j].Turn
			if next.Kind != schema.TurnAssistant {
				continue
			}
			nextCalls := toolCallsOf(next.Message)
			if len(nextCalls) > 0 && nextCalls[0].Name == "shell" &&
				researchTestBuildRe.MatchString(shellCommandOf(nextCalls[0])) {
				st.Cycles++
				st.SavedPromptTokens += next.Usage.InputTokens
				st.SavedCompletionTokens += next.Usage.OutputTokens
			}
			break
		}
	}
	return st
}

// ObsResendStats measures bytes of oversized tool results that keep being
// re-sent on requests after their first two. First two requests free; the
// residual after that is what an ObservationPack style handle-plus-excerpt
// mechanism could save. Re-send accounting stops at a compaction boundary
// (TurnCheckpoint or TurnSummary) because masking/compaction may already
// have removed the observation from context there. This is a proxy: the
// transcript cannot show per-request masking, so treat results as resident
// until a boundary appears.
type ObsResendStats struct {
	Results        int // number of oversized results
	TotalBytes     int // sum of their sizes
	ResendBytes    int // bytes re-sent on requests after the first two
	ResendRequests int // requests paying that residual
}

// LogVolumeStats is the same measurement restricted to build/test-style
// shell output (the Evidence-Preserving Reducer's input class).
type LogVolumeStats struct {
	Results        int
	TotalBytes     int
	ResendBytes    int
	ResendRequests int
}

// resultTextOf flattens a tool result content into text. Content is
// provider-shaped: usually a string, sometimes a list of parts.
func resultTextOf(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
			if s, ok := p.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	case map[string]any:
		if s, ok := v["text"].(string); ok {
			return s
		}
	}
	return ""
}

// shellCommandByCallID maps each shell tool call's ID to the command it ran,
// so a tool result can be attributed to the command that produced it.
func shellCommandByCallID(entries []transcript.Entry) map[string]string {
	cmds := make(map[string]string)
	for i := range entries {
		turn := entries[i].Turn
		if turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, c := range toolCallsOf(turn.Message) {
			if c.Name == "shell" {
				cmds[c.ID] = shellCommandOf(c)
			}
		}
	}
	return cmds
}

// pendingObs tracks one counted result's residency in later requests.
type pendingObs struct {
	bytes int
	seen  int // assistant requests that carried it so far
}

// walkObservations is the shared engine of both observation signals. A result
// counts when its text is at least minBytes and filter accepts it. The filter
// receives the result's name and text plus the command of the shell call that
// produced it; cmdOK reports whether that command is build/test-style. Re-send
// accounting stops at a compaction boundary.
func walkObservations(entries []transcript.Entry, minBytes int, filter func(name, text, cmd string, cmdOK bool) bool) ObsResendStats {
	callCommand := shellCommandByCallID(entries)
	var st ObsResendStats
	var pending []pendingObs
	for i := range entries {
		turn := entries[i].Turn
		switch turn.Kind {
		case schema.TurnToolResults:
			for _, part := range turn.Message.Content {
				if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
					continue
				}
				text := resultTextOf(part.ToolResult.Content)
				if len(text) < minBytes {
					continue
				}
				cmd, ok := callCommand[part.ToolResult.ToolCallID]
				cmdOK := ok && researchTestBuildRe.MatchString(cmd)
				if !filter(part.ToolResult.Name, text, cmd, cmdOK) {
					continue
				}
				st.Results++
				st.TotalBytes += len(text)
				pending = append(pending, pendingObs{bytes: len(text)})
			}
		case schema.TurnAssistant:
			for k := range pending {
				pending[k].seen++
				if pending[k].seen > 2 {
					st.ResendBytes += pending[k].bytes
					st.ResendRequests++
				}
			}
		case schema.TurnCheckpoint, schema.TurnSummary:
			// Compaction boundary: residency accounting stops here.
			pending = pending[:0]
		}
	}
	return st
}

func measureLargeObservations(entries []transcript.Entry, threshold int) ObsResendStats {
	return walkObservations(entries, threshold, func(_, _, _ string, _ bool) bool { return true })
}

func measureLogVolume(entries []transcript.Entry, minBytes int) LogVolumeStats {
	st := walkObservations(entries, minBytes, func(_, _, _ string, cmdOK bool) bool { return cmdOK })
	return LogVolumeStats{
		Results:        st.Results,
		TotalBytes:     st.TotalBytes,
		ResendBytes:    st.ResendBytes,
		ResendRequests: st.ResendRequests,
	}
}
