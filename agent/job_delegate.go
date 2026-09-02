package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
)

const (
	delegateResourceType       = "delegate"
	runtimeMessageAliasCaller  = "caller"
	runtimeMessageAliasWatched = "watched"
)

var delegateWorktreeControlPolicy func(*execenv.LocalExecutionEnvironment, string) error

type delegateArgs struct {
	Task                string
	AgentType           string
	Model               string
	ReasoningEffort     string
	DelegationAllowance int
	WatchParent         bool
	Isolation           string
	Sandbox             string
	SandboxNet          *bool
	ResultSchema        map[string]any
	TaskList            []taskpkg.TaskTemplate
}

type delegateWorktreeReport struct {
	Path         string
	Branch       string
	HeadSHA      string
	Ahead        int
	Dirty        bool
	DisposalHint string
}

type delegateSandboxReport struct {
	Mode    string
	Network bool
}

type delegateResult struct {
	DelegateID               string
	ChildSessionID           string
	Type                     string
	Status                   jobstore.Status
	Reason                   string
	ExhaustionBudget         string
	ExhaustionLimit          int
	Resumable                *bool
	RunningInBackground      bool
	TimedOut                 bool
	TranscriptRef            string
	Output                   string
	Truncated                bool
	StructuredResult         any
	StructuredResultValid    bool
	StructuredResultValidSet bool
	StructuredResultReason   string
	Worktree                 *delegateWorktreeReport
	Sandbox                  *delegateSandboxReport
	AgentType                string
	Tools                    []string
	Model                    string
	Warnings                 []string
	Err                      error
}

type sendMessageResult struct {
	Target                   string
	DelegateID               string
	Type                     string
	Status                   jobstore.Status
	Reason                   string
	ExhaustionBudget         string
	ExhaustionLimit          int
	Resumable                *bool
	RunningInBackground      bool
	TimedOut                 bool
	Action                   string
	TranscriptRef            string
	Output                   string
	Truncated                bool
	StructuredResult         any
	StructuredResultValid    bool
	StructuredResultValidSet bool
	StructuredResultReason   string
	Worktree                 *delegateWorktreeReport
	Warnings                 []string
	Task                     string
	Description              string
	AgentType                string
	Tools                    []string
	RequestedModel           string
	ResolvedProfileID        string
	ResolvedModel            string
	ReasoningEffort          string
	RunStartedAt             string
	RunEndedAt               string
	LatestActivityAt         string
	CumulativeUsage          *schema.CumulativeUsage
	WaitIgnoredReason        string
	Err                      error
}

func (s *Session) resolveDelegateRestoreProfileRef(base *provider.Profile, profileID, model string) (*provider.Profile, error) {
	ref := profileID + "/" + model
	if s.resolveProfile != nil {
		resolved, err := s.resolveProfile(ref)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			return nil, fmt.Errorf("profile %q unavailable", ref)
		}
		if resolved.ID() != base.ID() {
			resolved = resolved.WithCommunicateOverridesFrom(base)
		}
		return resolved, nil
	}
	if profileID != base.ID() {
		return nil, fmt.Errorf("profile %q unavailable", ref)
	}
	return base.WithModel(model), nil
}

func (s *Session) sandboxHostFacts() sandbox.HostFacts {
	if s == nil {
		return sandbox.RealProber{}.Probe()
	}
	s.sandboxHostFactsOnce.Do(func() {
		if s.cfg.testOnly.sandboxProber != nil {
			s.sandboxHostFactsValue = s.cfg.testOnly.sandboxProber.Probe()
			return
		}
		s.sandboxHostFactsValue = sandbox.RealProber{}.Probe()
	})
	return s.sandboxHostFactsValue
}

func validGrantRange(own int) string {
	if own <= 1 {
		return "0"
	}
	return fmt.Sprintf("0..%d", own-1)
}

func validateDelegateGrant(requested, own int) (bool, string) {
	return requested < own, validGrantRange(own)
}

func delegateStartFailed(err error) delegateResult {
	return delegateResult{
		Type:   delegateResourceType,
		Status: jobstore.StatusFailed,
		Reason: "start_failed",
		Err:    err,
	}
}

func sendMessageFailed(target string, err error) sendMessageResult {
	return sendMessageResult{
		Target: target,
		Err:    err,
	}
}

func (s *Session) stableDelegateWorktreeReport(desc delegatestore.Descriptor) *delegateWorktreeReport {
	report := s.delegateWorktreeReport(desc.Isolation, desc.WorkingDir)
	if report != nil {
		report.DisposalHint = s.stableDelegateDisposalHint(desc, filepath.Base(report.Path))
	}
	return report
}

func (s *Session) delegateWorktreeReport(isolation, workingDir string) *delegateWorktreeReport {
	if s == nil || strings.TrimSpace(isolation) != "worktree" {
		return nil
	}
	lanePath := strings.TrimSpace(workingDir)
	if lanePath == "" {
		return nil
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return nil
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	if controlEnv.SandboxReRootError() != nil {
		return nil
	}
	if err := s.useDelegateWorktreeControlPolicy(controlEnv, mainRoot); err != nil {
		return nil
	}
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)
	metaDir := metaDirForLane(lanePath)
	sidecar, err := worktree.ReadSidecar(metaDir, filepath.Base(lanePath))
	if err != nil {
		return nil
	}
	headOut, err := run("-C", lanePath, "rev-parse", "HEAD")
	if err != nil {
		return nil
	}
	headSHA := strings.TrimSpace(headOut)
	if headSHA == "" {
		return nil
	}
	clean, _, err := worktree.CleanTree(run, lanePath)
	if err != nil {
		return nil
	}
	aheadOut, err := run("-C", lanePath, "rev-list", "--count", sidecar.BaseSHA+".."+headSHA)
	if err != nil {
		return nil
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(aheadOut))
	if err != nil {
		return nil
	}
	return &delegateWorktreeReport{
		Path: lanePath, Branch: sidecar.Branch, HeadSHA: headSHA, Ahead: ahead, Dirty: !clean,
	}
}

func (s *Session) stableDelegateDisposalHint(desc delegatestore.Descriptor, delegateID string) string {
	if s == nil || desc.OwnerSessionID != s.id || !s.canInstructTool("manage_worktree") || !isDelegateID(delegateID) {
		return ""
	}
	return fmt.Sprintf("When you're done with this delegate's work (e.g., after merging it), dispose its worktree and branch: manage_worktree op=dispose id=%s.", delegateID)
}

func (s *Session) useDelegateWorktreeControlPolicy(env *execenv.LocalExecutionEnvironment, mainRoot string) error {
	if delegateWorktreeControlPolicy != nil {
		return delegateWorktreeControlPolicy(env, mainRoot)
	}
	return env.UseControlPolicy(mainRoot)
}
