package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
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
	// plus output.decision, when the result tool's schema carries one (e.g.
	// provider.WithAllowedDecisions) — read from the session's last ASSISTANT
	// turn. "none" when that turn carries no result-tool call, or the session
	// has no assistant turn at all.
	Outcome string `json:"outcome"`
}

// UnreadableSession is a session ListSessions could not build a row for, and
// why. Reporting it as simply absent would be the dangerous answer: a batch
// study over an undercounted enumeration is worse than one that says exactly
// which sessions it could not read, so every unreadable session is listed,
// never silently dropped.
type UnreadableSession struct {
	SessionID     string `json:"session_id"`
	TranscriptRef string `json:"transcript_ref"`
	Error         string `json:"error"`
}

// SessionsResult is a session enumeration: every session ListSessions could
// read, plus every one it couldn't.
type SessionsResult struct {
	Sessions   []SessionRow        `json:"sessions"`
	Unreadable []UnreadableSession `json:"unreadable"`
}

// SessionsOpts narrows a ListSessions enumeration.
type SessionsOpts struct {
	// Since, when > 0, drops rows whose LastActivity is older than
	// time.Now().Add(-Since). Zero means no recency filter. Unreadable
	// sessions are never dropped by this filter — their last activity is
	// exactly the fact ListSessions couldn't determine.
	Since time.Duration
	// Bucket scopes the enumeration to one project id. Empty enumerates every
	// bucket under the state root (the default — the shape a fleet-wide batch
	// study needs).
	Bucket string
}

// ListSessions enumerates every session under stateBase (or just one
// --bucket): the readable ones as SessionRows sorted by last activity
// descending (most recently active first), plus every session it could not
// read, in SessionsResult.Unreadable. One corrupt session never aborts the
// sweep or silently vanishes from the count — mirroring ScanTurnIDs, the
// established convention for a whole-state-root sweep.
func ListSessions(stateBase string, opts SessionsOpts) (SessionsResult, error) {
	buckets, err := resolveBuckets(stateBase)
	if err != nil {
		return SessionsResult{}, err
	}

	var cutoff time.Time
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since)
	}

	res := SessionsResult{Sessions: []SessionRow{}, Unreadable: []UnreadableSession{}}
	for _, b := range buckets {
		if opts.Bucket != "" && b.projectID != opts.Bucket {
			continue
		}
		metas, err := schema.ListSessionMetas(b.dir)
		if err != nil {
			return SessionsResult{}, fmt.Errorf("list session metas in bucket %s: %w", b.dir, err)
		}
		for _, meta := range metas {
			paths := pathsFor(b, meta.ID)
			row, err := sessionRow(b, paths, meta)
			if err != nil {
				res.Unreadable = append(res.Unreadable, UnreadableSession{
					SessionID:     meta.ID,
					TranscriptRef: paths.TranscriptRef,
					Error:         err.Error(),
				})
				continue
			}
			if !cutoff.IsZero() && row.LastActivity.Before(cutoff) {
				continue
			}
			res.Sessions = append(res.Sessions, row)
		}
	}
	sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].LastActivity.After(res.Sessions[j].LastActivity) })
	sort.Slice(res.Unreadable, func(i, j int) bool { return res.Unreadable[i].SessionID < res.Unreadable[j].SessionID })
	return res, nil
}

// sessionRow builds one SessionRow from a session's durable state: the
// transcript (header + entries, for started/models/outcome), the transcript
// file's own size and mtime (bytes/last-activity), the meta (turn count,
// subagent/observer facts), and the jobs fold (delegate count).
func sessionRow(b bucket, paths Paths, meta schema.SessionMeta) (SessionRow, error) {
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

// outcomeHint reads the final communicate-family call's end_turn (and
// output.decision, if the result tool's schema carries one) from the
// session's last ASSISTANT turn. It reports "none" when that turn carries no
// result-tool call, when its arguments don't parse, or when the session has
// no assistant turn at all — an absent or malformed outcome is reported as
// "none", not guessed at.
func outcomeHint(doc transcriptDoc, resultTool string) string {
	for _, e := range slices.Backward(doc.Entries) {
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
// decision lives at output.decision, not at the call's top level:
// DefCommunicateNamed's base schema sets additionalProperties:false at the
// top level, so no schema-valid call can ever carry a top-level field beyond
// message/end_turn/output — a profile that wants a routing signal (e.g.
// provider.WithAllowedDecisions, for orchestration systems like Toil) nests
// it inside output instead (agent/provider/profile_overrides.go,
// addDecisionToSchema). A present decision is surfaced when the caller's
// schema happens to carry one; its absence is not an error.
func formatOutcome(rawArgs json.RawMessage) string {
	var args struct {
		EndTurn bool `json:"end_turn"`
		Output  struct {
			Decision string `json:"decision"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "none"
	}
	hint := fmt.Sprintf("end_turn=%t", args.EndTurn)
	if args.Output.Decision != "" {
		hint += " decision=" + args.Output.Decision
	}
	return hint
}

// RenderSessions renders a session enumeration as a padded human table
// (Sessions is already sorted by ListSessions, most recently active first),
// followed by the unreadable list — a session ListSessions could not read is
// always shown, never silently omitted from the count.
func RenderSessions(res SessionsResult) string {
	var b strings.Builder
	if len(res.Sessions) == 0 {
		b.WriteString("(no sessions match)\n")
	} else {
		fmt.Fprintf(&b, "%-24s %-24s %-20s %-20s %-30s %6s %10s %-6s %-24s %5s %5s %-24s\n",
			"session_id", "bucket", "started", "last_activity", "models", "turns", "bytes", "subag", "parent", "dele", "obs", "outcome")
		for _, r := range res.Sessions {
			bucket := r.Bucket
			if bucket == "" {
				bucket = "(override root)"
			}
			// StartedAt (doc.Header.CreatedAt) and LastActivity (the
			// transcript file's mtime, local-zoned on most platforms) come
			// from different clocks with different zones -- render both in
			// UTC so the table's two time columns are directly comparable
			// rather than silently mixing zones.
			fmt.Fprintf(&b, "%-24s %-24s %-20s %-20s %-30s %6d %10d %-6t %-24s %5d %5d %-24s\n",
				truncate(r.SessionID, 24), truncate(bucket, 24),
				r.StartedAt.UTC().Format(time.RFC3339), r.LastActivity.UTC().Format(time.RFC3339),
				truncate(strings.Join(r.Models, ","), 30), r.TurnCount, r.TranscriptBytes, r.IsSubagent,
				dash(truncate(r.ParentSessionID, 24)), r.DelegateCount, r.ObserverCount, r.Outcome)
		}
		fmt.Fprintf(&b, "sessions=%d\n", len(res.Sessions))
	}
	if len(res.Unreadable) > 0 {
		fmt.Fprintf(&b, "%d session(s) could not be read:\n", len(res.Unreadable))
		for _, u := range res.Unreadable {
			fmt.Fprintf(&b, "  · %s  (%s) — %s\n", u.SessionID, u.TranscriptRef, u.Error)
		}
	}
	return b.String()
}
