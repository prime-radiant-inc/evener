package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
)

const (
	maxJobOutputRetentionBytes  = 8 * 1024 * 1024
	defaultJobListLimit         = 50
	maxJobListLimit             = 100
	minJobBlockTimeoutMS        = 1000
	maxJobBlockTimeoutMS        = 60000
	jobToolResultDefaultMaxChar = 20_000
	jobToolResultMinJSONChars   = 800
	jobManagerUnavailableReason = "job manager is not available"
	maxJobGrepMatches           = 100
	maxJobGrepLineBytes         = 4096
	jobKindAgent                = "agent"
	jobKindShell                = "shell"
	jobPhaseStarting            = "starting"
	jobPhaseAwaitingModel       = "awaiting_model"
	jobPhaseModelStreaming      = "model_streaming"
	jobPhaseModelRetrying       = "model_retrying"
	jobPhaseToolRunning         = "tool_running"
	jobPhaseProcessRunning      = "process_running"
)

// clampJobBlockTimeout clamps a positive max_wait_ms request into the supported
// inline-block window [minJobBlockTimeoutMS, maxJobBlockTimeoutMS] and returns
// it as a wait duration. Callers gate on ms > 0 (and reject negatives) upstream.
func clampJobBlockTimeout(ms int) time.Duration {
	clamped := max(ms, minJobBlockTimeoutMS)
	clamped = min(clamped, maxJobBlockTimeoutMS)
	return time.Duration(clamped) * time.Millisecond
}

// rootOnlyJobControlTools gates DELEGATION, not job control: a session that
// can run jobs can always watch its own jobs, at any depth, so job_watch is
// deliberately not here. The accesses the old job_watch strip was protecting
// are each enforced at their own source: `parent` requires
// delegate(watch_parent=true) (delegate_tree_watch.go), and a concrete job id
// must be owned by the watching session (validateWatchTarget's target_not_watchable).
var rootOnlyJobControlTools = []string{"delegate"}

func registerJobTools(reg *tool.Registry, s *Session, deps *toolDeps) error {
	if deps != nil && deps.registerTool != nil {
		return registerJobToolsWithRegistrar(reg, jobToolRegisterFunc(func(registered tool.RegisteredTool) error {
			return deps.registerTool(reg, registered)
		}), s, deps)
	}
	return registerJobToolsWithRegistrar(reg, reg, s, deps)
}

type jobToolRegisterFunc func(tool.RegisteredTool) error

func (f jobToolRegisterFunc) Register(registered tool.RegisteredTool) error { return f(registered) }

type jobToolRegistrar interface {
	Register(tool.RegisteredTool) error
}

// registerJobToolsWithRegistrar keeps production registration on Registry while
// allowing deterministic failure injection at each registration boundary.
func registerJobToolsWithRegistrar(reg *tool.Registry, registrar jobToolRegistrar, s *Session, deps *toolDeps) error {
	_ = deps
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobStatus(), ReadOnly: true,
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobStatusTool(s, args, jobToolResultMaxChars(reg, "job_status"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobListNamed(s.resultToolName()), ReadOnly: true,
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobListTool(s, args, jobListOutputMaxChars(reg))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobStop(),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobStopTool(ctx, s, args, jobToolResultMaxChars(reg, "job_stop"))
		},
	}); err != nil {
		return err
	}
	if err := registrar.Register(tool.RegisteredTool{
		Definition: tool.DefJobWatchNamed(availableEventKindNames(), s.resultToolName()),
		Limit:      schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobWatchToolWithContext(ctx, s, args, jobToolResultMaxChars(reg, "job_watch"))
		},
	}); err != nil {
		return err
	}
	return nil
}

// validateDelegateLabel validates a delegate name with the same alphabet
// manage_worktree names use (worktree.ValidateName), wrapped in the
// invalid_request convention both the create path and describe share.
// A blank name is valid (absent label).
func validateDelegateLabel(name string) error {
	if name == "" {
		return nil
	}
	if verr := worktree.ValidateName(name); verr != nil {
		return fmt.Errorf("invalid_request: name: %w", verr)
	}
	return nil
}

// decodeDelegateArgs decodes the delegate tool's raw params into delegateArgs,
// returning an invalid_request error for a malformed wait/allowance value. It is
// pure over the args map (no session state), so the decode — including the
// tri-state sandbox_net (nil = inherit, never a silent false) — is unit-testable
// without minting a delegate.
func decodeDelegateArgs(args map[string]any) (delegateArgs, error) {
	if strings.TrimSpace(stringArg(args, "prompt")) == "" {
		return delegateArgs{}, errors.New("invalid_request: prompt is required")
	}
	a := delegateArgs{
		Task:            stringArg(args, "prompt"),
		AgentType:       stringArg(args, "agent_type"),
		Model:           stringArg(args, "model"),
		ReasoningEffort: stringArg(args, "reasoning_effort"),
		WatchParent:     shellBoolArg(args, "watch_parent"),
		// Isolation is normalized here so every downstream consumer —
		// create, describe — sees one shape; a padded value must not
		// behave differently at exactly one of them.
		Isolation: strings.TrimSpace(stringArg(args, "isolation")),
		Sandbox:   stringArg(args, "sandbox"), // may carry "+nonet" suffix or be "nonet" alone
	}
	if raw, exists := args["fork_context"]; exists {
		var ok bool
		a.ForkContext, ok = raw.(bool)
		if !ok {
			return delegateArgs{}, errors.New("invalid_request: fork_context must be a JSON boolean")
		}
	}
	// The sandbox field now encodes both mode and an optional network override
	// in a single enum value. Values like "read-only+nonet" split into mode=
	// "read-only" and sandbox_net=false. "nonet" alone means inherit the
	// parent's mode and disable network. The legacy separate sandbox_net
	// boolean field is no longer part of the schema, but we still accept it
	// from in-process callers that build args directly (tests, scripted
	// providers) so existing test fixtures don't break.
	sandboxVal := strings.TrimSpace(a.Sandbox)
	if sandboxVal == "nonet" || strings.HasSuffix(sandboxVal, "+nonet") {
		// "nonet" alone (inherit parent mode, disable network) and any
		// "<mode>+nonet" suffix both decode to a net-off request. The bare
		// "nonet" value is advertised by the combined enum for non-off
		// parents, so it must split here exactly like "+nonet".
		mode := strings.TrimSuffix(sandboxVal, "+nonet")
		if mode == "nonet" {
			mode = ""
		}
		a.Sandbox = mode
		netFalse := false
		a.SandboxNet = &netFalse
	}
	if rawNet, exists := args["sandbox_net"]; exists {
		switch v := rawNet.(type) {
		case bool:
			a.SandboxNet = &v
		default:
			return delegateArgs{}, errors.New("invalid_request: sandbox_net must be a JSON boolean (true or false, not a quoted string)")
		}
	}
	// name: a short mnemonic the caller gives the delegate. With isolation
	// "worktree" it names the lane's git branch, so `git branch` and merges
	// read clearly; without isolation it is a display-only label. Either way
	// the lane directory and every addressing surface stay keyed to the
	// delegate id. Validated here with the same alphabet manage_worktree names
	// use — a branch-safe alphabet is also a good label alphabet — before any
	// capacity is reserved; the worktree create core re-checks with git's own
	// ref rules and refuses a branch that already exists.
	if name := stringArg(args, "name"); name != "" {
		if err := validateDelegateLabel(name); err != nil {
			return delegateArgs{}, err
		}
		a.Name = name
	}
	// delegation_allowance: absent = the default grant (one level below the
	// creator, resolved by createDelegate); 0 = leaf delegate; positive =
	// grant; negative = invalid_request. createDelegate enforces the grant
	// rule (strictly less than own allowance).
	if n, ok := shellIntArg(args, "delegation_allowance"); ok {
		if n < 0 {
			return delegateArgs{}, errors.New("invalid_request: delegation_allowance must be non-negative")
		}
		a.DelegationAllowance = &n
	}
	if resultSchema, ok := args["result_schema"].(map[string]any); ok {
		a.ResultSchema = resultSchema
	}
	taskList, err := delegateTaskListArg(args)
	if err != nil {
		return delegateArgs{}, err
	}
	a.TaskList = taskList
	return a, nil
}

// delegateTaskListArg decodes the delegate tool's task_list items into task
// templates. Every item needs a title and a prompt that defines its own step,
// so an empty prompt is an error rather than a blank task.
func delegateTaskListArg(args map[string]any) ([]taskpkg.TaskTemplate, error) {
	raw, ok := args["task_list"]
	if !ok || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("invalid_request: task_list must be an array")
	}
	out := make([]taskpkg.TaskTemplate, 0, len(items))
	for i, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid_request: task_list[%d] must be an object", i)
		}
		title := strings.TrimSpace(stringArg(fields, "title"))
		if title == "" {
			return nil, fmt.Errorf("invalid_request: task_list[%d].title is required", i)
		}
		prompt := strings.TrimSpace(stringArg(fields, "prompt"))
		if prompt == "" {
			return nil, fmt.Errorf("invalid_request: task_list[%d].prompt is required", i)
		}
		effort := strings.TrimSpace(stringArg(fields, "reasoning_effort"))
		if effort != "" && effort != "low" && effort != "medium" && effort != "high" {
			return nil, fmt.Errorf("invalid_request: task_list[%d].reasoning_effort must be one of low, medium, high", i)
		}
		kind := strings.TrimSpace(stringArg(fields, "type"))
		switch taskpkg.TaskType(kind) {
		case "", taskpkg.TaskTypeResearch, taskpkg.TaskTypeImplement, taskpkg.TaskTypeVerify, taskpkg.TaskTypeFix:
		default:
			return nil, fmt.Errorf("invalid_request: task_list[%d].type must be one of research, implement, verify, fix", i)
		}
		out = append(out, taskpkg.TaskTemplate{
			Title:           title,
			Prompt:          prompt,
			ReasoningEffort: effort,
			Type:            kind,
		})
	}
	return out, nil
}

func sessionJobManager(s *Session) (*jobManager, error) {
	if s == nil || s.jobManager == nil {
		return nil, errors.New(jobManagerUnavailableReason)
	}
	return s.jobManager, nil
}

// sessionRunningJobIDs lists this session's own running (session-launched,
// non-nested) job ids, in job_list's default order. Used by the communicate
// end_turn running-job warning; returns nil when the session has no job
// manager or no running jobs.
func sessionRunningJobIDs(s *Session) []string {
	jm, err := sessionJobManager(s)
	if err != nil {
		return nil
	}
	recs := jm.list(listFilter{Status: jobstore.StatusRunning})
	if len(recs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.JobID)
	}
	return ids
}

// sessionRunningWorkIDs combines managed jobs with detached processes owned by
// this session. Detached processes deliberately stay out of the job manager and
// drain accounting, but an end_turn warning must still name one while its own
// completion receipt is open.
func sessionRunningWorkIDs(s *Session) []string {
	ids := sessionRunningJobIDs(s)
	return append(ids, s.runningDetachedProcessIDs()...)
}

func (s *Session) recordDetachedProcess(process execenv.DetachedProcess) {
	if s == nil || process.PID <= 0 || process.Done == nil {
		// Without a completion receipt we cannot distinguish a live process from a
		// stale PID safely, so do not manufacture an end-turn warning.
		return
	}
	s.mu.Lock()
	s.detachedProcesses = append(s.detachedProcesses, sessionDetachedProcess{
		pid:  process.PID,
		done: process.Done,
	})
	s.mu.Unlock()
}

// runningDetachedProcessIDs returns only processes whose launcher's completion
// receipt is still open. The receipt belongs to this exact launch, so a later
// process reusing the same PID can never be mistaken for a live owned process.
func (s *Session) runningDetachedProcessIDs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	alive := s.detachedProcesses[:0]
	ids := make([]string, 0, len(s.detachedProcesses))
	for _, process := range s.detachedProcesses {
		select {
		case <-process.done:
			continue
		default:
			alive = append(alive, process)
			ids = append(ids, fmt.Sprintf("detached process (pid %d)", process.pid))
		}
	}
	s.detachedProcesses = alive
	return ids
}

func stringArrayArg(args map[string]any, key string) ([]string, error) {
	if args[key] == nil {
		return nil, nil
	}
	return stringListArg(args, key, func(s string) (string, error) { return s, nil })
}

func jobTypeArrayArg(args map[string]any, key string) ([]jobstore.JobType, error) {
	return stringListArg(args, key, func(s string) (jobstore.JobType, error) {
		jobType := jobstore.JobType(s)
		switch jobType {
		case jobstore.JobShell, jobstore.JobType(delegateResourceType):
			return jobType, nil
		}
		return "", fmt.Errorf("invalid job type %q", jobType)
	})
}

func projectJobRecordForViewer(viewer *Session, assessor *Session, rec *jobstore.JobRecord) jobListEntry {
	now := time.Now()
	if viewer != nil && viewer.clock != nil {
		now = viewer.clock.Now()
	}
	if assessor != nil && assessor.jobManager != nil {
		now = assessor.jobManager.now()
	}
	return projectJobRecordAt(viewer, assessor, rec, now)
}

func projectJobRecordAt(viewer *Session, assessor *Session, rec *jobstore.JobRecord, now time.Time) jobListEntry {
	_ = viewer
	_ = assessor
	statusView := projectJobStatus(now, rec)
	return jobListEntry{
		ID:                 rec.JobID,
		JobID:              rec.JobID,
		Kind:               statusView.Kind,
		Type:               string(rec.Type),
		Status:             string(rec.Status),
		Phase:              statusView.Phase,
		Reason:             stringPtrOrNil(rec.Reason),
		Description:        jobRecordDisplayLabel(rec),
		ParentJobID:        stringPtrOrNil(rec.ParentJobID),
		ParentDelegateID:   stringPtrOrNil(rec.ParentDelegateID),
		OwnerSessionID:     rec.OwnerSessionID,
		VisibleToSessionID: rec.VisibleToSession,
		TranscriptRef:      stringPtrOrNil(statusView.TranscriptRef),
		ExhaustionBudget:   rec.ExhaustionBudget,
		ExhaustionLimit:    rec.ExhaustionLimit,
		StartedAt:          rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:            timePtrOrNil(rec.EndedAt),
		RunningForMS:       statusView.RunningForMS,
		DurationMS:         statusView.DurationMS,
		QuietForMS:         statusView.QuietForMS,
		LastEventAt:        statusView.LastEventAt,
		LastActivity:       lastActivityProjection(rec),
		ExitCode:           rec.ExitCode,
		TotalBytes:         rec.OutputBytes,
		Command:            stringPtrOrNil(rec.Command),
	}
}

func marshalBoundedJSON(v any, maxChars int) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if maxChars > 0 && jsonCharLen(b) > maxChars {
		return "", fmt.Errorf("job tool JSON output exceeds %d characters after bounding", maxChars)
	}
	return string(b), nil
}

// marshalBoundedJSONWithFit marshals v and returns (json, true, nil) when it fits
// maxChars, or ("", false, nil) when it does not — letting the caller progressively
// drop fields to fit. Used by the delegate result path, which keeps JSON output.
func marshalBoundedJSONWithFit(v any, maxChars int) (string, bool, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", false, err
	}
	if maxChars <= 0 || jsonCharLen(b) <= maxChars {
		return string(b), true, nil
	}
	return "", false, nil
}

func jobListOutputMaxChars(reg *tool.Registry) int {
	if reg != nil {
		if registered := reg.Get("job_list"); registered != nil {
			return registered.Limit.MaxChars
		}
	}
	return jobToolResultDefaultMaxChar
}

func jobToolResultMaxChars(reg *tool.Registry, name string) int {
	if reg == nil {
		return jobToolResultDefaultMaxChar
	}
	registered := reg.Get(name)
	if registered == nil || registered.Limit.MaxChars <= 0 {
		return jobToolResultDefaultMaxChar
	}
	if registered.Limit.MaxChars < jobToolResultMinJSONChars {
		return jobToolResultMinJSONChars
	}
	return registered.Limit.MaxChars
}

func enforceJobToolJSONLimits(reg *tool.Registry) {
	if reg == nil {
		return
	}
	overrides := map[string]schema.ToolOutputLimit{}
	for _, name := range []string{"job_status", "job_list", "job_stop", "delegate", "job_watch", "delegate_send"} {
		registered := reg.Get(name)
		if registered == nil || registered.Limit.MaxChars >= jobToolResultMinJSONChars {
			continue
		}
		overrides[name] = schema.ToolOutputLimit{MaxChars: jobToolResultMinJSONChars, Strategy: registered.Limit.Strategy}
	}
	if len(overrides) > 0 {
		reg.OverrideLimits(overrides)
	}
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func timePtrOrNil(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.Format(time.RFC3339Nano)
	return &formatted
}
