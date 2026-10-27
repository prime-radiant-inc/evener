package agent

import (
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/identifier"
)

const (
	notResumableMissingDelegateResumeMetadata = "missing_delegate_resume_metadata"
	notResumableParentLinkageUnavailable      = "parent_linkage_unavailable"
	notResumableMissingChildSessionMeta       = "missing_child_session_meta"
	notResumableCorruptChildSessionMeta       = "corrupt_child_session_meta"
	notResumableMissingChildTranscript        = "missing_child_transcript"
	notResumableCorruptChildTranscript        = "corrupt_child_transcript"
	notResumableTranscriptSessionMismatch     = "transcript_session_mismatch"
	notResumableWorkingDirMissing             = "working_dir_missing"
)

type delegateRuntime struct {
	owner *Session
}

type stableDelegateSendOutcome struct {
	result sendMessageResult
	commit *delegateToolResultCommit
	// earlierCommits acknowledge, in order and before commit, the earlier
	// results the reply carries (result.Earlier).
	earlierCommits []*delegateToolResultCommit
}

// inlineOutcome is the send's reply for a resolved inline wait: result, which
// already carries the newest result, plus the earlier results the wait
// collected, oldest first, with their commits (#3906, #3951).
func inlineOutcome(result sendMessageResult, resolution delegateInlineResolution) stableDelegateSendOutcome {
	outcome := stableDelegateSendOutcome{result: result, commit: resolution.commit}
	for _, earlier := range resolution.earlier {
		outcome.result.Earlier = append(outcome.result.Earlier, earlier.packet)
		outcome.earlierCommits = append(outcome.earlierCommits, earlier.commit)
	}
	return outcome
}

type delegateRunLeaseContextKey struct{}

type delegatePreseededInput struct {
	sessionID string
	input     string
}

type delegatePreseededInputContextKey struct{}

type delegateChildSessionIDContextKey struct{}

type delegateAttentionRunContextKey struct{}

type delegateIsolation struct {
	env             execenv.ExecutionEnvironment
	ownsFreshEnv    bool
	worktreePath    string
	worktreeProject identifier.Project
	// laneBranch is the git branch the lane was cut on. Rollback deletes this
	// branch, never the id by assumption.
	laneBranch string
	// laneAdmission is the spawn's close-fence admission, carried here so a
	// rollback can rename it as it begins.
	laneAdmission envWorkID
	laneFenced    bool
}
