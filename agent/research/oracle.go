// Package research implements the SoL-Pi auto-research loop's offline
// machinery: the oracle analyzer over real session transcripts, and the
// rollout runner that drives headless harness runs against committed
// research environments. See docs/superpowers/specs/2026-09-19-sol-pi-auto-research-loop-design.md.
package research

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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
	defer func() { _ = f.Close() }()
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
	return LogVolumeStats(st)
}

// CompactionStats records where compaction boundaries (checkpoints and
// summaries) fell relative to assistant requests, and the prompt-token
// high-water mark when each boundary landed.
type CompactionStats struct {
	Compactions         []compactionPoint
	MaxPromptTokensSeen int
	RequestsBetween     []int // assistant requests between consecutive boundaries
}

type compactionPoint struct {
	RequestIndex int             // ordinal of the assistant request most recently seen
	PromptTokens int             // that request's input tokens
	Kind         schema.TurnKind // TurnCheckpoint or TurnSummary
}

// MechanismProjection is one ranked candidate.
type MechanismProjection struct {
	Mechanism     string  // "action-fusion", "observation-pack", "log-reducer", "compaction-reminder"
	SavedTokens   int     // projected absolute token savings (input + output where applicable)
	TrafficPct    float64 // percent of corpus recorded traffic
	Basis         string  // human explanation of the measurement
	Informational bool    // true when no pct projection is computed (compaction timing)
}

type OracleReport struct {
	GeneratedAt       time.Time
	StateBase         string
	Sessions          int
	Entries           int
	SkippedLines      int
	TotalInputTokens  int
	TotalOutputTokens int
	Adjacency         AdjacencyStats
	LargeObs          ObsResendStats
	LogVolume         LogVolumeStats
	Compaction        CompactionStats
	Projections       []MechanismProjection
}

func measureCompaction(entries []transcript.Entry) CompactionStats {
	var st CompactionStats
	requests := 0
	for i := range entries {
		turn := entries[i].Turn
		switch turn.Kind {
		case schema.TurnAssistant:
			requests++
			if turn.Usage.InputTokens > st.MaxPromptTokensSeen {
				st.MaxPromptTokensSeen = turn.Usage.InputTokens
			}
		case schema.TurnCheckpoint, schema.TurnSummary:
			st.Compactions = append(st.Compactions, compactionPoint{
				RequestIndex: requests,
				PromptTokens: st.MaxPromptTokensSeen,
				Kind:         turn.Kind,
			})
		}
	}
	prev := 0
	for _, p := range st.Compactions {
		st.RequestsBetween = append(st.RequestsBetween, p.RequestIndex-prev)
		prev = p.RequestIndex
	}
	return st
}

const researchBytesPerToken = 4 // matches the contextmgr char/4 estimator

func buildReport(stateBase string, sessions, entries, skipped int, adj AdjacencyStats, obs ObsResendStats, logs LogVolumeStats, comp CompactionStats, totalIn, totalOut int) *OracleReport {
	r := &OracleReport{
		GeneratedAt: time.Now().UTC(),
		StateBase:   stateBase, Sessions: sessions, Entries: entries, SkippedLines: skipped,
		TotalInputTokens: totalIn, TotalOutputTokens: totalOut,
		Adjacency: adj, LargeObs: obs, LogVolume: logs, Compaction: comp,
	}
	traffic := totalIn + totalOut
	pct := func(saved int) float64 {
		if traffic == 0 {
			return 0
		}
		return float64(saved) / float64(traffic) * 100
	}
	r.Projections = []MechanismProjection{
		{
			Mechanism:   "action-fusion",
			SavedTokens: adj.SavedPromptTokens + adj.SavedCompletionTokens,
			TrafficPct:  pct(adj.SavedPromptTokens + adj.SavedCompletionTokens),
			Basis: fmt.Sprintf("%d edit-then-command cycles; savings are the intervening request's tokens",
				adj.Cycles),
		},
		{
			Mechanism:   "observation-pack",
			SavedTokens: obs.ResendBytes / researchBytesPerToken,
			TrafficPct:  float64(obs.ResendBytes/researchBytesPerToken) / float64(max(totalIn, 1)) * 100,
			Basis: fmt.Sprintf("%d oversized results, %d re-sent bytes after their first two requests (bytes/4 token estimate)",
				obs.Results, obs.ResendBytes),
		},
		{
			Mechanism:   "log-reducer",
			SavedTokens: logs.ResendBytes / researchBytesPerToken,
			TrafficPct:  float64(logs.ResendBytes/researchBytesPerToken) / float64(max(totalIn, 1)) * 100,
			Basis: fmt.Sprintf("%d build/test logs, %d re-sent bytes; overlaps observation-pack headroom (not additive)",
				logs.Results, logs.ResendBytes),
		},
		{
			Mechanism:     "compaction-reminder",
			Informational: true,
			Basis: fmt.Sprintf("%d compaction boundaries; max prompt tokens before one: %d; informational in slice 1",
				len(comp.Compactions), comp.MaxPromptTokensSeen),
		},
	}
	return r
}

func renderReport(r *OracleReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "oracle report (%s)\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "sessions: %d, entries: %d, skipped lines: %d\n", r.Sessions, r.Entries, r.SkippedLines)
	fmt.Fprintf(&b, "recorded input tokens: %d, output tokens: %d\n", r.TotalInputTokens, r.TotalOutputTokens)
	fmt.Fprintf(&b, "compaction boundaries: %d\n\n", len(r.Compaction.Compactions))
	fmt.Fprintf(&b, "projected savings (per mechanism, upper bounds; observation-pack and log-reducer overlap):\n")
	best := 0.0
	for _, p := range r.Projections {
		if p.Informational {
			fmt.Fprintf(&b, "  - %s: informational. %s\n", p.Mechanism, p.Basis)
			continue
		}
		fmt.Fprintf(&b, "  - %s: %d tokens (%.2f%% of recorded traffic). %s\n", p.Mechanism, p.SavedTokens, p.TrafficPct, p.Basis)
		if p.TrafficPct > best {
			best = p.TrafficPct
		}
	}
	fmt.Fprintf(&b, "\nstop-or-go: best non-informational projection is %.2f%%; the go threshold is 5%%.\n", best)
	if best < 5 {
		fmt.Fprintf(&b, "VERDICT: best mechanism projects under 5%% of recorded traffic. Stop and report before building the spine.\n")
	} else {
		fmt.Fprintf(&b, "VERDICT: headroom above the 5%% threshold. Proceed to the spine.\n")
	}
	return b.String()
}

// OracleOptions configures one oracle corpus run.
type OracleOptions struct {
	StateBase string // resolved state base
	Limit     int    // max sessions, newest first; 0 = all
	OutPath   string // JSONL file; one report record appended per run
	Stdout    io.Writer
}

// RunOracle measures a corpus and appends the report record to opts.OutPath.
func RunOracle(opts OracleOptions) (*OracleReport, error) {
	if _, err := os.Stat(opts.StateBase); err != nil {
		return nil, fmt.Errorf("stat state base: %w", err)
	}
	transcripts, err := walkSessionTranscripts(opts.StateBase, opts.Limit)
	if err != nil {
		return nil, err
	}
	var (
		totalEntries, totalSkipped int
		adj                        AdjacencyStats
		obs                        ObsResendStats
		logs                       LogVolumeStats
		comp                       CompactionStats
		totalIn, totalOut          int
	)
	for _, st := range transcripts {
		entries, skipped, err := loadEntries(st.Path)
		if err != nil {
			// Unreadable single sessions do not sink the corpus run.
			_, _ = fmt.Fprintf(opts.Stdout, "skipping unreadable transcript %s: %v\n", st.Path, err)
			continue
		}
		totalSkipped += skipped
		totalEntries += len(entries)
		a := measureAdjacency(entries)
		adj.Cycles += a.Cycles
		adj.SavedPromptTokens += a.SavedPromptTokens
		adj.SavedCompletionTokens += a.SavedCompletionTokens
		o := measureLargeObservations(entries, 10*1024)
		obs.Results += o.Results
		obs.TotalBytes += o.TotalBytes
		obs.ResendBytes += o.ResendBytes
		obs.ResendRequests += o.ResendRequests
		l := measureLogVolume(entries, 4*1024)
		logs.Results += l.Results
		logs.TotalBytes += l.TotalBytes
		logs.ResendBytes += l.ResendBytes
		logs.ResendRequests += l.ResendRequests
		s := measureCompaction(entries)
		comp.Compactions = append(comp.Compactions, s.Compactions...)
		// The corpus max is the largest session high-water mark, so the
		// compaction basis reports a real number. RequestsBetween stays
		// per-session: corpus-level gap attribution is a slice-2 decision.
		comp.MaxPromptTokensSeen = max(comp.MaxPromptTokensSeen, s.MaxPromptTokensSeen)
		for i := range entries {
			if entries[i].Turn.Kind == schema.TurnAssistant {
				totalIn += entries[i].Turn.Usage.InputTokens
				totalOut += entries[i].Turn.Usage.OutputTokens
			}
		}
	}
	report := buildReport(opts.StateBase, len(transcripts), totalEntries, totalSkipped,
		adj, obs, logs, comp, totalIn, totalOut)
	if opts.OutPath != "" {
		line, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(opts.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open oracle ledger: %w", err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(append(line, '\n')); err != nil {
			return nil, err
		}
	}
	_, _ = fmt.Fprint(opts.Stdout, renderReport(report))
	return report, nil
}
