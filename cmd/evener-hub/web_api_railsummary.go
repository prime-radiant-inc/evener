package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// hubThreadRailSummary answers evener/thread/railSummary: a compact
// full-history summary for the Session Rail — per-turn token/result tuples,
// job intervals, and totals — computed from the session's transcript JSONL
// and jobs.jsonl. One bounded response instead of paging N windows of full
// turn text. See docs/superpowers/specs/2026-08-22-session-rail-design.md.
//
// The rail shows only what a live observer at this instant could know, so
// for ended sessions the summary's EndedAt is the real session end (never a
// precomputed future), and every per-turn tuple carries its real timestamp.
// Live sessions don't call this endpoint — their data arrives via existing
// push (turn/started, turn/completed) and client-side per-turn accumulation.
func hubThreadRailSummary(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.RailSummaryParams) (appwire.RailSummaryResponse, error) {
	resp, ok, err := pastRailSummaryResponse(cfg, params)
	if err != nil {
		return appwire.RailSummaryResponse{}, err
	}
	if ok {
		return resp, nil
	}
	return appwire.RailSummaryResponse{}, fmt.Errorf("session not found: %s", params.Ref)
}

// pastRailSummaryResponse is the dead-session path: the ref must resolve to
// a LOCAL past thread id the index already knows. Only then does it read the
// session's persisted transcript and jobs.jsonl.
func pastRailSummaryResponse(cfg hubcore.WebConfig, params appwire.RailSummaryParams) (appwire.RailSummaryResponse, bool, error) {
	if cfg.Past == nil {
		return appwire.RailSummaryResponse{}, false, nil
	}
	threadID, ok := localPastThreadIDFromRef(params.Ref)
	if !ok {
		return appwire.RailSummaryResponse{}, false, nil
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.RailSummaryResponse{}, false, nil
	}
	resp, err := railSummaryFromTranscript(entry)
	if err != nil {
		return appwire.RailSummaryResponse{}, true, err
	}
	// Job intervals from the persisted jobs activity tree.
	tree, jobErr := agent.LoadSessionJobActivityTree(entry.StateDir, entry.Meta.ID, appwire.JobsListParams{Ref: params.Ref})
	if jobErr == nil {
		resp.Jobs = collectRailJobs(tree)
	}
	return resp, true, nil
}

// localPastThreadIDFromRef extracts the thread ID from a local ref string.
func localPastThreadIDFromRef(ref string) (string, bool) {
	if ref == "" {
		return "", false
	}
	r, err := appwire.ParseRef(ref)
	if err != nil {
		return "", false
	}
	if r.SourceID != "local" {
		return "", false
	}
	return r.ThreadID, true
}

// railSummaryFromTranscript reads the full transcript and extracts per-turn
// tuples (startedAt, inTok, outTok, resultBytes, flags) plus totals. One
// scan of the JSONL file; no turn text carried in the response.
func railSummaryFromTranscript(entry hubcore.PastEntry) (appwire.RailSummaryResponse, error) {
	path := pastTranscriptPath(entry)
	var resp appwire.RailSummaryResponse
	var firstTs, lastTs int64

	_, err := apptranscript.ScanPrelude(path, transcriptJSONLMaxLineBytes)
	if err != nil {
		return resp, fmt.Errorf("rail summary: %w", err)
	}

	// Use TurnsFromFile with a projector that captures the schema.Turn data.
	// The projector receives the decoded turn, so we extract what we need
	// without carrying turn text.
	turns, err := apptranscript.TurnsFromFile(path, transcriptJSONLMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		ts := turn.Timestamp.UnixMilli()
		if ts == 0 {
			return nil // skip turns without timestamps (e.g., system prelude)
		}
		if firstTs == 0 || ts < firstTs {
			firstTs = ts
		}
		if ts > lastTs {
			lastTs = ts
		}
		usage := turn.Usage
		inTok := usage.InputTokens
		outTok := usage.OutputTokens
		totalTok := usage.TotalTokens
		if totalTok == 0 && (inTok > 0 || outTok > 0) {
			totalTok = inTok + outTok
		}
		resp.TotalTokens += totalTok

		st := appwire.RailSummaryTurn{
			StartedAt: ts,
			InTokens:  inTok,
			OutTokens: outTok,
			UserInput: turn.Kind == schema.TurnUserInput,
			Steering:  turn.Kind == schema.TurnSteering,
		}

		// Result bytes: sum of JSON-encoded tool result content sizes on
		// TOOL_RESULTS turns.
		if turn.Kind == schema.TurnToolResults {
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
					if b, mErr := json.Marshal(part.ToolResult.Content); mErr == nil {
						n := int64(len(b))
						st.ResultBytes += n
						resp.ResultBytes += n
					}
				}
			}
		}

		// Error flag: TurnFailure turns, or tool results that are errors.
		if turn.Kind == schema.TurnFailure {
			st.Error = true
		}
		if turn.Error != nil {
			st.Error = true
		}
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.IsError {
				st.Error = true
			}
		}

		resp.Turns = append(resp.Turns, st)
		return nil // no items needed — the projector is only a scanner here
	})
	if err != nil {
		return resp, fmt.Errorf("rail summary: %w", err)
	}
	_ = turns // turns are not needed; the projector captured everything

	resp.StartedAt = firstTs
	resp.EndedAt = lastTs
	return resp, nil
}

// collectRailJobs flattens the job activity tree into job intervals for the
// rail's job micro-lanes.
func collectRailJobs(tree appwire.JobActivityTree) []appwire.RailSummaryJob {
	var jobs []appwire.RailSummaryJob
	collectRailJobsFromSession(&tree.Root, &jobs)
	return jobs
}

func collectRailJobsFromSession(session *appwire.JobActivitySession, jobs *[]appwire.RailSummaryJob) {
	for _, entry := range session.Entries {
		if entry.Kind == "shell" && entry.Job != nil {
			j := appwire.RailSummaryJob{
				JobID:  entry.Job.JobID,
				Status: entry.Job.Status,
			}
			if t, err := parseRFC3339Milli(entry.Job.StartedAt); err == nil {
				j.StartedAt = t
			}
			if entry.Job.EndedAt != "" {
				if t, err := parseRFC3339Milli(entry.Job.EndedAt); err == nil {
					j.FinishedAt = t
				}
			}
			j.ExitCode = entry.Job.ExitCode
			*jobs = append(*jobs, j)
		}
		if entry.Kind == "delegate" && entry.Delegate != nil && entry.Delegate.Child != nil {
			collectRailJobsFromSession(entry.Delegate.Child, jobs)
		}
	}
}

// parseRFC3339Milli parses an RFC3339 timestamp string (with optional
// fractional seconds) and returns epoch milliseconds.
func parseRFC3339Milli(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}
