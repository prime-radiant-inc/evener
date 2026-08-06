package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// SessionRow is one session's forensic summary, as ListSessions enumerates for
// a batch study: enough to triage many sessions without opening each one.
type SessionRow struct {
	SessionID     string `json:"session_id"`
	TranscriptRef string `json:"transcript_ref"`
	// Bucket is the project id; empty for an override/scratch state root,
	// which is itself a single bucket (see ResolveStateBase).
	Bucket          string    `json:"bucket"`
	StartedAt       time.Time `json:"started_at"`
	LastActivity    time.Time `json:"last_activity"`
	Models          []string  `json:"models"`
	TurnCount       int       `json:"turn_count"`
	TranscriptBytes int64     `json:"transcript_bytes"`
	IsSubagent      bool      `json:"is_subagent"`
	// ParentSessionID is the spawning parent from the transcript header (set
	// only for spawned subagent transcripts). This is distinct from
	// schema.SessionMeta.ParentSessionID, which records fork lineage, an
	// unrelated concept — a forked session is not a subagent.
	ParentSessionID string `json:"parent_session_id,omitempty"`
	DelegateCount   int    `json:"delegate_count"`
	ObserverCount   int    `json:"observer_count"`
	// Outcome is the final communicate-family (result-tool) call's end_turn —
	// plus a status field, when the result tool's schema carries one — read
	// from the session's last ASSISTANT turn. "none" when that turn carries no
	// result-tool call, or the session has no assistant turn at all.
	Outcome string `json:"outcome"`
}

// SessionsOpts narrows a ListSessions enumeration.
type SessionsOpts struct {
	// Since, when > 0, drops rows whose LastActivity is older than
	// time.Now().Add(-Since). Zero means no recency filter.
	Since time.Duration
	// Bucket scopes the enumeration to one project id. Empty enumerates every
	// bucket under the state root (the default — the shape a fleet-wide batch
	// study needs).
	Bucket string
}

// ListSessions enumerates every session under stateBase (or just one
// --bucket), sorted by last activity descending (most recently active
// first) — the shape a batch forensic study reproduces in one command.
// An unreadable transcript or jobs.jsonl is a loud error naming the file,
// never a silently shortened list: a batch study over an undercounted
// enumeration is worse than one that stops and says why.
func ListSessions(stateBase string, opts SessionsOpts) ([]SessionRow, error) {
	buckets, err := resolveBuckets(stateBase)
	if err != nil {
		return nil, err
	}

	var cutoff time.Time
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since)
	}

	rows := []SessionRow{}
	for _, b := range buckets {
		if opts.Bucket != "" && b.projectID != opts.Bucket {
			continue
		}
		metas, err := schema.ListSessionMetas(b.dir)
		if err != nil {
			return nil, fmt.Errorf("list session metas in bucket %s: %w", b.dir, err)
		}
		for _, meta := range metas {
			row, err := sessionRow(b, meta)
			if err != nil {
				return nil, err
			}
			if !cutoff.IsZero() && row.LastActivity.Before(cutoff) {
				continue
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LastActivity.After(rows[j].LastActivity) })
	return rows, nil
}

// sessionRow builds one SessionRow from a session's durable state: the
// transcript (header + entries, for started/models/outcome), the transcript
// file's own size and mtime (bytes/last-activity), the meta (turn count,
// subagent/observer facts), and the jobs fold (delegate count).
func sessionRow(b bucket, meta schema.SessionMeta) (SessionRow, error) {
	paths := pathsFor(b, meta.ID)

	doc, err := loadTranscript(paths.TranscriptPath)
	if err != nil {
		return SessionRow{}, fmt.Errorf("session %s: %w", meta.ID, err)
	}
	info, err := os.Stat(paths.TranscriptPath)
	if err != nil {
		return SessionRow{}, fmt.Errorf("stat transcript %s: %w", paths.TranscriptPath, err)
	}
	events, err := jobstore.ReadEvents(paths.JobsPath)
	if err != nil {
		return SessionRow{}, fmt.Errorf("session %s: %w", meta.ID, err)
	}

	delegateCount := 0
	for _, d := range jobstore.FoldDelegates(events) {
		if d.ChildSessionID != "" {
			delegateCount++
		}
	}

	resultTool := "communicate"
	if meta.Config.ResultToolName != "" {
		resultTool = meta.Config.ResultToolName
	}

	return SessionRow{
		SessionID:       paths.SessionID,
		TranscriptRef:   paths.TranscriptRef,
		Bucket:          b.projectID,
		StartedAt:       doc.Header.CreatedAt,
		LastActivity:    info.ModTime(),
		Models:          sessionModels(doc.Header.Model, meta.Model),
		TurnCount:       meta.TurnCount,
		TranscriptBytes: info.Size(),
		IsSubagent:      meta.IsSubagent,
		ParentSessionID: doc.Header.ParentSessionID,
		DelegateCount:   delegateCount,
		ObserverCount:   len(meta.ObservedBy),
		Outcome:         outcomeHint(doc, resultTool),
	}, nil
}

// sessionModels reports the model(s) a session used: the model recorded at
// creation (the transcript header, immutable) plus the session's current
// model from meta. A header/meta mismatch is the durable trace of a
// mid-session model switch — the switch marker text itself
// (schema.TurnModelSwitch) is presentational prose, not a structured field, so
// this compares the two canonical structured sources instead of parsing it.
func sessionModels(headerModel, metaModel string) []string {
	switch {
	case headerModel == "" && metaModel == "":
		return nil
	case headerModel == "":
		return []string{metaModel}
	case metaModel == "" || metaModel == headerModel:
		return []string{headerModel}
	default:
		return []string{headerModel, metaModel}
	}
}

// outcomeHint reads the final communicate-family call's end_turn (and status,
// if the result tool's schema carries one) from the session's last ASSISTANT
// turn. It reports "none" when that turn carries no result-tool call, when its
// arguments don't parse, or when the session has no assistant turn at all —
// an absent or malformed outcome is reported as "none", not guessed at.
func outcomeHint(doc transcriptDoc, resultTool string) string {
	for i := len(doc.Entries) - 1; i >= 0; i-- {
		e := doc.Entries[i]
		if e.Turn.Kind != schema.TurnAssistant {
			continue
		}
		var resultCall *llm.ToolCallData
		for _, part := range e.Turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.Name == resultTool {
				resultCall = part.ToolCall
			}
		}
		if resultCall == nil {
			return "none"
		}
		return formatOutcome(resultCall.Arguments)
	}
	return "none"
}

// formatOutcome renders a result-tool call's arguments as the outcome hint.
// status is an optional, non-standard field: DefCommunicateNamed's base
// schema carries only end_turn, but a profile can extend the result tool's
// output schema (e.g. WithAllowedDecisions), so a present "status" string is
// surfaced when the caller's schema happens to carry one.
func formatOutcome(rawArgs json.RawMessage) string {
	var args struct {
		EndTurn bool   `json:"end_turn"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "none"
	}
	hint := fmt.Sprintf("end_turn=%t", args.EndTurn)
	if args.Status != "" {
		hint += " status=" + args.Status
	}
	return hint
}

// RenderSessions renders a session enumeration as a padded human table, sorted
// (already, by ListSessions) with the most recently active session first.
func RenderSessions(rows []SessionRow) string {
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("(no sessions match)\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%-24s %-24s %-20s %-20s %-30s %6s %10s %-6s %-24s %5s %5s %-24s\n",
		"session_id", "bucket", "started", "last_activity", "models", "turns", "bytes", "subag", "parent", "dele", "obs", "outcome")
	for _, r := range rows {
		bucket := r.Bucket
		if bucket == "" {
			bucket = "(override root)"
		}
		fmt.Fprintf(&b, "%-24s %-24s %-20s %-20s %-30s %6d %10d %-6t %-24s %5d %5d %-24s\n",
			truncate(r.SessionID, 24), truncate(bucket, 24),
			r.StartedAt.Format(time.RFC3339), r.LastActivity.Format(time.RFC3339),
			truncate(strings.Join(r.Models, ","), 30), r.TurnCount, r.TranscriptBytes, r.IsSubagent,
			dash(truncate(r.ParentSessionID, 24)), r.DelegateCount, r.ObserverCount, r.Outcome)
	}
	fmt.Fprintf(&b, "sessions=%d\n", len(rows))
	return b.String()
}
