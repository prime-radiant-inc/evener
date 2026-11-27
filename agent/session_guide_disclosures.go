package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const (
	guideTopicRunningJobs        = "running-jobs"
	guideTopicReadingTranscripts = "reading-transcripts"
	guideTopicUsingWorktrees     = "using-worktrees"
)

func guideNotificationOpenTag(topic string) string {
	return fmt.Sprintf("<system-notification topic=%q>", topic)
}

func wrapGuideNotification(topic, body string) string {
	return guideNotificationOpenTag(topic) + "\n" + body + "\n" + llm.SystemNotificationCloseTag
}

func (s *Session) buildRunningJobsDisclosure() string {
	parts := []string{fmt.Sprintf("Background jobs and delegates notify you automatically when they finish; yield quietly with `%s` (`next=\"log_quietly_and_end_turn\"`) while waiting rather than polling.", s.resultToolName())}
	var controls []string
	for _, c := range []struct{ tool, text string }{
		{"job_list", "check `job_list` before starting duplicate work"},
		{"job_status", "inspect a quiet target with `job_status` before stopping it"},
		{"job_stop", "cancel with `job_stop` (retains output)"},
	} {
		if s.canInstructTool(c.tool) {
			controls = append(controls, c.text)
		}
	}
	if len(controls) > 0 {
		parts = append(parts, strings.ToUpper(controls[0][:1])+strings.Join(controls, "; ")[1:]+".")
	}
	if s.canInstructTool("job_watch") {
		if s.cfg.TurnEndsProcess {
			parts = append(parts, "In one-shot runs, attach `job_watch(source=<job_id>, progress_interval_ms=180000)` (or `output_match`) so a needed background shell survives turn end.")
		} else {
			parts = append(parts, "Use `job_watch` for output patterns (`output_match`) or external timers (`after_seconds`/`repeat_seconds`).")
		}
	}
	if s.canInstructTool("delegate") {
		line := "Keep external waits in your own session since delegate shell jobs stop when their activation ends"
		if s.canInstructTool("delegate_send") {
			line += "; follow up via `delegate_send(to=<delegate_id>)`"
		}
		parts = append(parts, line+".")
	}
	if s.canInstructTool("read_transcript") {
		parts = append(parts, "Read job or delegate output with `read_transcript`.")
	}
	return wrapGuideNotification(guideTopicRunningJobs, strings.Join(parts, " "))
}

func (s *Session) buildReadingTranscriptsDisclosure() string {
	parts := []string{
		"Use `read_transcript(transcript_ref=..., offset_bytes=..., output_match=...)` to page retained `job:<job_id>` or `artifact:<id>` output or search lines with `output_match` and `context_lines`.",
		"Read session transcripts with `read_transcript` (`format=\"outline\"` first, then `range` or default `format=\"markdown\"` with `expand_turn` and `offset_bytes`).",
	}
	if s.canInstructTool("find_session_transcripts") {
		parts = append(parts, "Discover archived sessions with `find_session_transcripts`.")
	}
	parts = append(parts, "Read retained outputs via transcript tools rather than disk files, and wait for completion notices instead of polling `read_transcript`.")
	return wrapGuideNotification(guideTopicReadingTranscripts, strings.Join(parts, " "))
}

func (s *Session) buildUsingWorktreesDisclosure() string {
	return wrapGuideNotification(guideTopicUsingWorktrees,
		"Use `manage_worktree` for isolated or parallel branches in a local Git checkout; use plain `git` for ordinary branches. "+
			"`manage_worktree` has no merge operation: `exit` to the main checkout, fetch only the target ref (`--no-tags`), verify neither branch moved and no uncommitted edits collide, and merge with `git merge --no-ff --no-edit`. "+
			"Once tip commits land upstream, `exit` and clean up with `remove` (`delete_branch=true`) or `dispose` (`force=true` only after verifying upstream commits or explicit discard consent; keep `force_dirty=false`), verifying both worktree and branch are gone.")
}

func (s *Session) detectToolRoundGuideTriggers(calls []llm.ToolCallData, results []tool.ExecResult) [3]bool {
	var wantJobs, wantTranscripts, wantWorktrees bool
	for i := range min(len(calls), len(results)) {
		res := results[i]
		if res.Truncated && strings.Contains(res.Output, `read_transcript(transcript_ref="artifact:`) {
			wantTranscripts = true
		}
		effectiveName := s.canonicalToolName(calls[i].Name)
		switch effectiveName {
		case "shell":
			var st struct {
				Mode      string `json:"mode"`
				JobID     string `json:"job_id"`
				Truncated *bool  `json:"truncated"`
			}
			if len(res.ToolState) > 0 && json.Unmarshal(res.ToolState, &st) == nil && st.JobID != "" {
				wantJobs = wantJobs || (!res.IsError && st.Mode == string(shellModeBackground))
				wantTranscripts = wantTranscripts || (st.Truncated != nil && *st.Truncated)
			}
		case "delegate":
			if !res.IsError && !res.Rejected && !res.PrevalOnly {
				var created struct {
					DelegateID string `json:"delegate_id"`
				}
				if json.Unmarshal([]byte(cmp.Or(res.FullOutput, res.Output)), &created) == nil && created.DelegateID != "" {
					wantJobs = true
				}
			}
		case "manage_worktree":
			wantWorktrees = wantWorktrees || (!res.Rejected && !res.PrevalOnly)
		}
	}
	hasJobTools := slices.ContainsFunc([]string{"job_status", "job_stop", "job_list", "job_watch", "delegate"}, s.canInstructTool)
	return [3]bool{
		wantJobs && hasJobTools,
		wantTranscripts && s.canInstructTool("read_transcript"),
		wantWorktrees && s.canInstructTool("manage_worktree") && !s.worktreeDisposeOnlySurface(),
	}
}

type guideSlot struct {
	topic string
	flag  *bool
	build func() string
}

func (s *Session) guideSlots() [3]guideSlot {
	return [3]guideSlot{
		{guideTopicRunningJobs, &s.disclosedRunningJobs, s.buildRunningJobsDisclosure},
		{guideTopicReadingTranscripts, &s.disclosedReadingTranscripts, s.buildReadingTranscriptsDisclosure},
		{guideTopicUsingWorktrees, &s.disclosedUsingWorktrees, s.buildUsingWorktreesDisclosure},
	}
}

func (s *Session) maybeDiscloseGuidesForToolRound(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult) error {
	slots := s.guideSlots()
	s.mu.Lock()
	allDisclosed := !slices.ContainsFunc(slots[:], func(slot guideSlot) bool { return !*slot.flag })
	s.mu.Unlock()
	if allDisclosed {
		return nil
	}
	wants := s.detectToolRoundGuideTriggers(calls, results)
	s.mu.Lock()
	pending := slices.ContainsFunc([]int{0, 1, 2}, func(i int) bool { return wants[i] && !*slots[i].flag })
	s.mu.Unlock()
	if !pending {
		return nil
	}
	return s.withResponseSideEffects(ctx, func() {
		var blocks []string
		s.mu.Lock()
		for i, slot := range slots {
			if wants[i] && !*slot.flag {
				*slot.flag = true
				blocks = append(blocks, slot.build())
			}
		}
		s.mu.Unlock()
		if len(blocks) > 0 {
			msg := llm.UserMachinery(strings.Join(blocks, "\n\n"))
			s.appendTurnWithTranscriptMessage(schema.TurnSystem, msg, msg)
		}
	})
}

func (s *Session) syncDisclosedGuidesFromHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncDisclosedGuidesFromHistoryLocked(s.history)
}

func (s *Session) syncDisclosedGuidesFromHistoryLocked(history []schema.Turn) {
	slots := s.guideSlots()
	var openTags [3]string
	for i, slot := range slots {
		*slot.flag = false
		openTags[i] = guideNotificationOpenTag(slot.topic)
	}
	remaining := len(slots)
	for _, turn := range history {
		if turn.Kind != schema.TurnSystem {
			continue
		}
		text := turn.Message.Text()
		for i, slot := range slots {
			if *slot.flag {
				continue
			}
			if strings.Contains(text, openTags[i]) {
				*slot.flag = true
				remaining--
			}
		}
		if remaining == 0 {
			break
		}
	}
}
