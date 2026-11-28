package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

var autoDisclosedGuideSkills = [3]string{
	skill.RunningJobsSkillName,
	skill.ReadingTranscriptsSkillName,
	skill.UsingWorktreesSkillName,
}

func (s *Session) detectToolRoundGuideTriggers(calls []llm.ToolCallData, results []tool.ExecResult) [3]bool {
	var wantJobs, wantTranscripts, wantWorktrees bool
	for i := range min(len(calls), len(results)) {
		res := results[i]
		if res.Truncated && strings.Contains(res.Output, `read_transcript(transcript_ref="artifact:`) {
			wantTranscripts = true
		}
		switch calls[i].Name {
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
	return [3]bool{
		wantJobs,
		wantTranscripts && s.canInstructTool("read_transcript"),
		wantWorktrees,
	}
}

// skillAlreadyDisclosedInLiveHistoryLocked reports whether name is already
// active in live context via a frozen preload, a live history carrier matching
// its recorded ordinary activation, or an in-flight delivery obligation.
// Caller holds s.mu.
func (s *Session) skillAlreadyDisclosedInLiveHistoryLocked(name string) bool {
	entry := s.skillLifecycle.Inventory[name]
	if entry.Preload != nil {
		return true
	}
	if entry.Ordinary != nil && completeSkillContentInTurns(s.history, entry.Ordinary.Identity) {
		return true
	}
	for _, obligation := range s.skillLifecycle.Obligations {
		if obligation.Identity.Name == name {
			return true
		}
	}
	return false
}

func (s *Session) pendingAutoDisclosedGuides(wants [3]bool) []string {
	if !wants[0] && !wants[1] && !wants[2] {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for i, name := range autoDisclosedGuideSkills {
		if wants[i] && s.isAutoDisclosedGuideLocked(name) && !s.skillAlreadyDisclosedInLiveHistoryLocked(name) {
			names = append(names, name)
		}
	}
	return names
}

func (s *Session) maybeDiscloseGuidesForToolRound(ctx context.Context, calls []llm.ToolCallData, results []tool.ExecResult) error {
	wants := s.detectToolRoundGuideTriggers(calls, results)
	if len(s.pendingAutoDisclosedGuides(wants)) == 0 {
		return nil
	}
	var cancelErr error
	if abortErr := s.withResponseSideEffects(ctx, func() {
		names := s.pendingAutoDisclosedGuides(wants)
		if len(names) == 0 {
			return
		}
		opID, err := s.mintSkillOperationID()
		if err != nil {
			return
		}
		invocations := make([]skillInvocation, 0, len(names))
		for _, name := range names {
			invocations = append(invocations, skillInvocation{
				Name:          name,
				Route:         "auto_disclosure",
				InvocationID:  opID + ":" + name,
				AtomicGroupID: opID,
			})
		}
		batch, err := s.prepareSkillActivations(ctx, invocations)
		if err != nil {
			if ctx.Err() != nil {
				cancelErr = ctx.Err()
				return
			}
			s.emit(warningDataFromError("auto-disclosing bundled guide skill failed", err))
			return
		}
		_ = s.admitSkillActivationBatch(batch)
	}); abortErr != nil {
		return abortErr
	}
	return cancelErr
}

func (s *Session) isAutoDisclosedGuideLocked(name string) bool {
	d, ok := s.skills.Entries[name]
	if !ok || d.Unavailable || d.Controls.DisableModelInvocation {
		return false
	}
	return skill.IsAutoDisclosedGuideSkill(d) && s.skillAvailableToSession(name)
}

func (s *Session) filterEnvTrackerBundledGuidesLocked() {
	if s.envTracker == nil {
		return
	}
	s.envTracker.FilterRuntimeSkills(func(name string) bool {
		return !s.isAutoDisclosedGuideLocked(name)
	})
}
