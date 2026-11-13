package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// sessionRowJobsMaxBytes bounds how deep into a jobs journal a sessions row
// scans. The failures the row folds for are retired delegate activation
// records, written at activation time and so usually among a journal's oldest
// events — the head window is where that legacy state lives — and a journal
// at or under the bound is read whole. On a larger journal, records beyond
// the window are not reported: the row's failures stay a bounded summary of
// retired state, and the per-session jobs report remains the unbounded,
// authoritative view. Production never assigns it; a test shrinks it to
// prove the bound.
var sessionRowJobsMaxBytes int64 = 1 << 20

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
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	DelegateCount   int            `json:"delegate_count"`
	ObserverCount   int            `json:"observer_count"`
	Failures        []StateFailure `json:"failures,omitempty"`
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
	// time.Now().Add(-Since). Zero means no recency filter. A session whose
	// last activity cannot be determined (transcript missing) is never
	// dropped by this filter; it is reported as unreadable.
	Since time.Duration
	// Bucket scopes the enumeration to one project id. Empty enumerates every
	// bucket under the state root (the default — the shape a fleet-wide batch
	// study needs). A Bucket naming no enumerated bucket is an explicit
	// not-found error, never a silently empty list.
	Bucket string
}

// ListSessions enumerates every session under stateBase (or just one
// --bucket): the readable ones as SessionRows sorted by last activity
// descending (most recently active first), plus every session it could not
// read, in SessionsResult.Unreadable. A session is discovered from its primary
// metadata identity, so even a .meta.json that is present but unparseable is
// named in Unreadable rather than silently skipped the way a tolerant UI
// listing would drop it. One corrupt session never aborts the sweep or
// silently vanishes from the count — mirroring ScanTurnIDs, the established
// convention for a whole-state-root sweep. A cancelled ctx stops the walk and
// returns its error with no partial result. With a Since cutoff, a session
// whose transcript is older than the cutoff is skipped unread, so a corrupt
// transcript outside the window is not reported as unreadable.
func ListSessions(ctx context.Context, stateBase string, opts SessionsOpts) (SessionsResult, error) {
	buckets, stateRoot, err := resolveBuckets(stateBase)
	if err != nil {
		return SessionsResult{}, err
	}
	if opts.Bucket != "" {
		b, ok := bucketByProjectID(buckets, opts.Bucket)
		if !ok {
			return SessionsResult{}, fmt.Errorf("bucket %s not found %s", opts.Bucket, scannedUnder(stateRoot, len(buckets)))
		}
		buckets = []bucket{b}
	}

	var cutoff time.Time
	if opts.Since > 0 {
		cutoff = time.Now().Add(-opts.Since)
	}

	res := SessionsResult{Sessions: []SessionRow{}, Unreadable: []UnreadableSession{}}
	delegates := delegateCache{}
	for _, b := range buckets {
		if err := ctx.Err(); err != nil {
			return SessionsResult{}, err
		}
		metas, loadErrs, err := schema.ListSessionMetasWithErrors(ctx, b.dir)
		if err != nil {
			return SessionsResult{}, fmt.Errorf("list session metas in bucket %s: %w", b.dir, err)
		}
		// A metadata file that exists but could not be loaded is a session
		// ListSessions cannot build a row for; report it by its
		// filename-derived identity rather than drop it.
		for _, le := range loadErrs {
			res.Unreadable = append(res.Unreadable, UnreadableSession{
				SessionID:     le.ID,
				TranscriptRef: refFor(b.projectID, le.ID),
				Error:         le.Error.Error(),
			})
		}
		for _, meta := range metas {
			if err := ctx.Err(); err != nil {
				return SessionsResult{}, err
			}
			paths := pathsFor(b, meta.ID)
			if !cutoff.IsZero() && transcriptOlderThan(paths.TranscriptPath, cutoff) {
				continue
			}
			row, err := sessionRow(ctx, b, paths, meta, delegates)
			if err != nil {
				// A cancelled read is the operation stopping, not a
				// session that could not be read.
				if ctxErr := ctx.Err(); ctxErr != nil {
					return SessionsResult{}, ctxErr
				}
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
	if err := ctx.Err(); err != nil {
		return SessionsResult{}, err
	}
	sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].LastActivity.After(res.Sessions[j].LastActivity) })
	sort.Slice(res.Unreadable, func(i, j int) bool { return res.Unreadable[i].SessionID < res.Unreadable[j].SessionID })
	return res, nil
}

// transcriptOlderThan reports, from a stat alone, that the transcript's last
// write predates cutoff. sessionRow reports exactly that mtime as
// LastActivity, so such a session can never pass the cutoff and its
// transcript, jobs and delegate files need not be read. A transcript that
// cannot be stat'd is not skipped: sessionRow reports it as unreadable.
func transcriptOlderThan(path string, cutoff time.Time) bool {
	info, err := os.Stat(path)
	return err == nil && info.ModTime().Before(cutoff)
}

// scanSessionRowJobs reads a session's jobs journal bounded by
// sessionRowJobsMaxBytes. jobstore reports ErrScanLimitExceeded alongside the
// events decoded so far; the row keeps them, because the failures fold is a
// summary of retired state, not an inventory that must be complete.
func scanSessionRowJobs(ctx context.Context, path string) ([]jobstore.Event, error) {
	events, err := jobstore.ScanEvents(ctx, path, jobstore.ScanLimits{MaxBytes: sessionRowJobsMaxBytes})
	if err != nil && !errors.Is(err, jobstore.ErrScanLimitExceeded) {
		return nil, err
	}
	return events, nil
}

// sessionRow builds one SessionRow from a session's durable state: the
// transcript's header plus a bounded entry tail (started/models/parent and
// the outcome hint), the transcript file's own size and mtime
// (bytes/last-activity), the meta (turn count, subagent/observer facts), the
// delegates journal (delegate count), and the jobs scan (legacy delegate
// failures), bounded the same way.
func sessionRow(ctx context.Context, b bucket, paths Paths, meta schema.SessionMeta, delegates delegateCache) (SessionRow, error) {
	doc, err := loadSessionRowTranscript(ctx, paths.TranscriptPath)
	if err != nil {
		return SessionRow{}, fmt.Errorf("session %s: %w", meta.ID, err)
	}
	info, err := os.Stat(paths.TranscriptPath)
	if err != nil {
		return SessionRow{}, fmt.Errorf("stat transcript %s: %w", paths.TranscriptPath, err)
	}
	events, err := scanSessionRowJobs(ctx, paths.JobsPath)
	if err != nil {
		return SessionRow{}, fmt.Errorf("session %s: %w", meta.ID, err)
	}

	// The meta is already in hand, so resolve the delegates root from it
	// directly instead of going through delegates.get, which would re-read
	// the same meta from disk.
	rootSessionID := delegateRootFromMeta(meta, paths.SessionID)
	_, stable, _, err := delegates.getForRoot(ctx, b.dir, rootSessionID)
	if err != nil {
		return SessionRow{}, fmt.Errorf("session %s: %w", meta.ID, err)
	}
	delegateCount := len(projectDoctorDelegates("", meta.ID, stable))

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
		Failures:        legacyDelegateFailures(events),
		Outcome:         outcomeHint(doc, resultTool),
	}, nil
}

// sessionModels reports the model(s) a session used: the model recorded at
// creation (the transcript header, immutable) plus the session's current
// model from meta. This summary compares the two endpoint identities; it does
// not enumerate intermediate transitions from MODEL_SWITCH records.
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
				Truncate(r.SessionID, 24), Truncate(bucket, 24),
				r.StartedAt.UTC().Format(time.RFC3339), r.LastActivity.UTC().Format(time.RFC3339),
				Truncate(strings.Join(r.Models, ","), 30), r.TurnCount, r.TranscriptBytes, r.IsSubagent,
				dash(Truncate(r.ParentSessionID, 24)), r.DelegateCount, r.ObserverCount, r.Outcome)
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
