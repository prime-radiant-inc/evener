package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

type delegateSendResult struct {
	DelegateID             string                  `json:"delegate_id,omitempty"`
	Type                   string                  `json:"type,omitempty"`
	Status                 string                  `json:"status,omitempty"`
	Reason                 *string                 `json:"reason,omitempty"`
	ExhaustionBudget       string                  `json:"exhaustion_budget,omitempty"`
	ExhaustionLimit        int                     `json:"exhaustion_limit,omitempty"`
	Resumable              *bool                   `json:"resumable,omitempty"`
	RunningInBackground    bool                    `json:"running_in_background"`
	TimedOut               bool                    `json:"timed_out,omitempty"`
	Action                 string                  `json:"action"`
	TranscriptRef          string                  `json:"transcript_ref,omitempty"`
	Output                 *string                 `json:"output,omitempty"`
	Truncated              *bool                   `json:"truncated,omitempty"`
	StructuredResult       any                     `json:"structured_result,omitempty"`
	StructuredResultValid  *bool                   `json:"structured_result_valid,omitempty"`
	StructuredResultReason string                  `json:"structured_result_reason,omitempty"`
	Warnings               []string                `json:"warnings,omitempty"`
	Task                   string                  `json:"task,omitempty"`
	Description            string                  `json:"description,omitempty"`
	AgentType              string                  `json:"agent_type,omitempty"`
	Tools                  []string                `json:"tools,omitempty"`
	RequestedModel         string                  `json:"requested_model,omitempty"`
	ResolvedProfileID      string                  `json:"resolved_profile_id,omitempty"`
	ResolvedModel          string                  `json:"resolved_model,omitempty"`
	ReasoningEffort        string                  `json:"reasoning_effort,omitempty"`
	Name                   string                  `json:"name,omitempty"`
	RunStartedAt           string                  `json:"run_started_at,omitempty"`
	RunEndedAt             string                  `json:"run_ended_at,omitempty"`
	LatestActivityAt       string                  `json:"latest_activity_at,omitempty"`
	CumulativeUsage        *schema.CumulativeUsage `json:"cumulative_usage,omitempty"`
	// Worktree carries the isolation lane's path/branch/ahead/dirty state for
	// an isolated delegate's terminal job (native worktree tools spec §9
	// lifecycle step 3); nil for a non-isolated delegate.
	Worktree          *delegateWorktreeToolResult `json:"worktree,omitempty"`
	WaitIgnoredReason string                      `json:"wait_ignored_reason,omitempty"`
	// EarlierResults are results of the same delegate the caller had not
	// yet received, oldest first, carried ahead of this one by a wait that
	// collected them (#3906).
	EarlierResults []delegateSendResult `json:"earlier_results,omitempty"`
}

// delegateWorktreeToolResult is the tool-facing shape of delegateWorktreeReport
// (native worktree tools spec §9 lifecycle step 3).
type delegateWorktreeToolResult struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
	Ahead   int    `json:"ahead_commits"`
	Dirty   bool   `json:"dirty"`
	// DisposalHint carries the spec §P2 completion nudge. It MUST be exported —
	// an unexported field is silently dropped by encoding/json. Empty (and omitted) unless the receiving session has the dispose
	// op and owns the delegate.
	DisposalHint string `json:"disposal_hint,omitempty"`
}

func delegateWorktreeToolResultFrom(wt *delegateWorktreeReport) *delegateWorktreeToolResult {
	if wt == nil {
		return nil
	}
	return &delegateWorktreeToolResult{Path: wt.Path, Branch: wt.Branch, HeadSHA: wt.HeadSHA, Ahead: wt.Ahead, Dirty: wt.Dirty, DisposalHint: wt.DisposalHint}
}

type delegateSandboxToolResult struct {
	Mode    string `json:"mode"`
	Network bool   `json:"network"`
}

func delegateSandboxToolResultFrom(sb *delegateSandboxReport) *delegateSandboxToolResult {
	if sb == nil {
		return nil
	}
	return &delegateSandboxToolResult{Mode: sb.Mode, Network: sb.Network}
}

func marshalDelegateSendResult(res sendMessageResult, maxChars int) (any, error) {
	_ = maxChars
	out := delegateSendResultFrom(res)
	return tool.StateResult{Output: formatDelegateSend(out), State: out}, nil
}

func delegateSendResultFrom(res sendMessageResult) delegateSendResult {
	out := delegateSendResult{
		DelegateID:          res.DelegateID,
		Type:                res.Type,
		Status:              string(res.Status),
		Reason:              stringPtrOrNil(res.Reason),
		ExhaustionBudget:    res.ExhaustionBudget,
		ExhaustionLimit:     res.ExhaustionLimit,
		Resumable:           res.Resumable,
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		Action:              res.Action,
		TranscriptRef:       res.TranscriptRef,
		Warnings:            append([]string(nil), res.Warnings...),
		Task:                res.Task,
		Description:         res.Description,
		AgentType:           res.AgentType,
		Tools:               append([]string(nil), res.Tools...),
		RequestedModel:      res.RequestedModel,
		ResolvedProfileID:   res.ResolvedProfileID,
		ResolvedModel:       res.ResolvedModel,
		Name:                res.Name,
		ReasoningEffort:     res.ReasoningEffort,
		RunStartedAt:        res.RunStartedAt,
		RunEndedAt:          res.RunEndedAt,
		LatestActivityAt:    res.LatestActivityAt,
		CumulativeUsage:     res.CumulativeUsage,
		Worktree:            delegateWorktreeToolResultFrom(res.Worktree),
		WaitIgnoredReason:   res.WaitIgnoredReason,
	}
	if !res.RunningInBackground || res.TimedOut {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
	}
	if res.StructuredResult != nil || res.StructuredResultValidSet {
		valid := res.StructuredResultValid
		out.StructuredResult = res.StructuredResult
		out.StructuredResultValid = &valid
		out.StructuredResultReason = res.StructuredResultReason
	}
	for _, packet := range res.Earlier {
		earlier := sendMessageResult{DelegateID: res.DelegateID, Type: res.Type, TranscriptRef: res.TranscriptRef}
		if packet.Kind == delegatestore.PacketUpdate {
			// A mid-work update the reply carries ahead of the newest result
			// settles nothing: it is the delegate's message and no more.
			earlier.Action = "update"
			message, err := delegateUpdateMessage(packet)
			if err != nil {
				// Unreachable by construction: the journal fold writes every
				// update message with json.Marshal of a string. The reply has
				// no error channel, so the decode error stands in as output.
				message = err.Error()
			}
			earlier.Output = message
		} else {
			completeStableDelegateSendResult(&earlier, packet)
		}
		out.EarlierResults = append(out.EarlierResults, delegateSendResultFrom(earlier))
	}
	return out
}

// latestResultHeader introduces a delegate_send reply's own result after the
// earlier results it carries, in the tool reply and in transcripts alike.
const latestResultHeader = "latest result:\n"

// earlierResultHeader introduces earlier result i (zero-based) of n that a
// delegate_send reply carries, in the tool reply and in transcripts alike.
func earlierResultHeader(i, n int) string {
	return fmt.Sprintf("earlier result %d of %d, not delivered before:\n", i+1, n)
}

// formatDelegateSend renders a delegate send/steer/start result: any earlier
// results it carries, numbered and each rendered the same way, then any reply
// output, a bracketed footer, and the structured_result (JSON, genuinely
// structured) when present.
func formatDelegateSend(out delegateSendResult) string {
	var b strings.Builder
	for i, earlier := range out.EarlierResults {
		b.WriteString(earlierResultHeader(i, len(out.EarlierResults)))
		b.WriteString(formatDelegateSend(earlier))
		b.WriteString("\n\n")
	}
	if len(out.EarlierResults) != 0 {
		b.WriteString(latestResultHeader)
	}
	if out.Output != nil && *out.Output != "" {
		b.WriteString(*out.Output)
		if !strings.HasSuffix(*out.Output, "\n") {
			b.WriteByte('\n')
		}
	}
	foot := []string{out.Action}
	if out.DelegateID != "" {
		foot = append([]string{"delegate_id " + out.DelegateID}, foot...)
	}
	if out.Status != "" {
		foot = append(foot, out.Status)
	}
	if out.RunningInBackground {
		foot = append(foot, "running in background")
	}
	if out.WaitIgnoredReason != "" {
		foot = append(foot, "wait ignored: "+out.WaitIgnoredReason)
	}
	b.WriteString("[")
	b.WriteString(strings.Join(foot, " · "))
	b.WriteString("]")
	if out.Worktree != nil {
		fmt.Fprintf(&b, "\nworktree: path=%s, branch=%s, head=%s, %d commits ahead, dirty=%t",
			out.Worktree.Path, out.Worktree.Branch, out.Worktree.HeadSHA,
			out.Worktree.Ahead, out.Worktree.Dirty)
	}
	for _, warning := range out.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s", warning)
	}
	if out.StructuredResult != nil {
		if sr, err := json.Marshal(out.StructuredResult); err == nil {
			valid := out.StructuredResultValid != nil && *out.StructuredResultValid
			fmt.Fprintf(&b, "\nstructured_result (valid=%v): %s", valid, sr)
		}
	}
	if out.Worktree != nil && out.Worktree.DisposalHint != "" {
		fmt.Fprintf(&b, "\ndisposal_hint: %s", out.Worktree.DisposalHint)
	}
	return b.String()
}
