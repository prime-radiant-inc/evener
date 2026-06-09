package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const (
	defaultJobOutputBytes       = 65536
	maxJobOutputBytes           = 1048576
	maxJobOutputRetentionBytes  = 8 * 1024 * 1024
	defaultJobListLimit         = 50
	maxJobListLimit             = 100
	defaultJobBlockTimeoutMS    = 5000
	minJobBlockTimeoutMS        = 1000
	maxJobBlockTimeoutMS        = 60000
	jobToolResultDefaultMaxChar = 20_000
	jobToolResultMinJSONChars   = 800
	jobManagerUnavailableReason = "job manager is not available"
	maxJobGrepMatches           = 100
	maxJobGrepLineBytes         = 4096
	maxJobGrepPatternBytes      = 4096
	maxJobWatchResultTextChars  = 128
	maxJobWatchResultEvents     = 8
)

var rootOnlyJobControlTools = []string{"delegate", "job_watch"}

func registerJobTools(reg *tool.Registry, s *Session, deps *toolDeps) error {
	_ = deps
	if err := reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobReadOutput(), ReadOnly: true},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobReadOutputTool(ctx, s, args, jobToolResultMaxChars(reg, "job_read_output"))
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobList(), ReadOnly: true},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobListTool(s, args, jobToolResultMaxChars(reg, "job_list"))
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobStop()},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobStopTool(ctx, s, args, jobToolResultMaxChars(reg, "job_stop"))
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobSendMessage()},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobSendMessageTool(ctx, s, args, jobToolResultMaxChars(reg, "job_send_message"))
		},
	}); err != nil {
		return err
	}
	if err := reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobWatch(availableEventKindNames())},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			return jobWatchTool(s, args, jobToolResultMaxChars(reg, "job_watch"))
		},
	}); err != nil {
		return err
	}
	return reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefDelegate(s.delegateAgentTypeNames())},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return delegateTool(ctx, s, args, jobToolResultMaxChars(reg, "delegate"))
		},
	})
}

func jobSendMessageTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (string, error) {
	a := sendMessageArgs{
		Target:     stringArg(args, "target"),
		Message:    stringArg(args, "message"),
		OnFinished: stringArg(args, "on_finished"),
		Background: true,
	}
	if background, ok := args["background"].(bool); ok {
		a.Background = background
		a.BackgroundSet = true
	}
	if n, ok := shellIntArg(args, "block_timeout_ms"); ok {
		a.BlockTimeoutMS = n
	}

	res := s.sendDelegateMessage(ctx, a)
	if res.Err != nil {
		return "", res.Err
	}
	return marshalSendMessageResult(res, maxChars)
}

func jobWatchTool(s *Session, args map[string]any, maxChars int) (string, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	a, err := watchArgsFromToolArgs(args)
	if err != nil {
		return "", err
	}
	res, err := jm.configureWatch(a)
	if err != nil {
		return "", err
	}
	return marshalWatchResult(res, maxChars)
}

func delegateTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (string, error) {
	a := delegateArgs{
		Task:            stringArg(args, "task"),
		AgentType:       stringArg(args, "agent_type"),
		Model:           stringArg(args, "model"),
		ReasoningEffort: stringArg(args, "reasoning_effort"),
		Background:      true,
	}
	if background, ok := args["background"].(bool); ok {
		a.Background = background
	}
	if n, ok := shellIntArg(args, "block_timeout_ms"); ok {
		a.BlockTimeoutMS = n
	}
	if resultSchema, ok := args["result_schema"].(map[string]any); ok {
		a.ResultSchema = resultSchema
	}

	res := s.createDelegate(ctx, a)
	if res.Err != nil {
		return "", res.Err
	}
	return marshalDelegateResult(res, maxChars)
}

func jobReadOutputTool(ctx context.Context, s *Session, args map[string]any, registryMaxChars int) (string, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("job_id is required")
	}
	tailBytes, err := boundedJobBytesArg(args, "tail_bytes", defaultJobOutputBytes)
	if err != nil {
		return "", err
	}
	limitBytes, err := boundedJobBytesArg(args, "limit_bytes", defaultJobOutputBytes)
	if err != nil {
		return "", err
	}
	maxChars, err := jobReadMaxCharsArg(args, registryMaxChars)
	if err != nil {
		return "", err
	}

	if shellBoolArg(args, "block") {
		timeoutMS, err := jobBlockTimeoutMS(args)
		if err != nil {
			return "", err
		}
		waitForJobDoneOrOutput(ctx, jm, jobID, time.Duration(timeoutMS)*time.Millisecond)
	}

	rec, err := findJobRecord(jm, jobID)
	if err != nil {
		return "", err
	}
	content, totalBytes, truncated, err := jm.readOutput(jobID, tailBytes)
	if err != nil {
		return "", err
	}

	result := jobReadOutputResult{
		JobID:      rec.JobID,
		Type:       string(rec.Type),
		Status:     string(rec.Status),
		Reason:     stringPtrOrNil(rec.Reason),
		Content:    content,
		TotalBytes: totalBytes,
		Truncated:  truncated,
		ExitCode:   rec.ExitCode,
	}
	if rec.StructuredResult != nil {
		result.StructuredResult = rec.StructuredResult
	}
	if rec.StructuredResultValid != nil {
		result.StructuredResultValid = rec.StructuredResultValid
	}
	if grep := stringArg(args, "grep"); grep != "" {
		if err := validateJobGrepPattern(grep, maxChars); err != nil {
			return "", err
		}
		re, err := regexp.Compile(grep)
		if err != nil {
			return "", err
		}
		matches, err := jm.grepOutput(jobID, re, limitBytes)
		if err != nil {
			return "", err
		}
		result.Grep = &grep
		projected := projectJobOutputMatches(matches)
		result.Matches = &projected
	}
	return marshalBoundedJobReadOutputResult(result, maxChars)
}

func jobListTool(s *Session, args map[string]any, maxChars int) (string, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	filter, err := jobListFilterFromArgs(args)
	if err != nil {
		return "", err
	}
	recs, err := jm.listWithError(filter)
	if err != nil {
		return "", err
	}
	jobs := make([]jobListEntry, 0, len(recs))
	for _, rec := range recs {
		jobs = append(jobs, projectJobRecord(rec))
	}
	return marshalBoundedJobListResult(jobListResult{
		Jobs:       jobs,
		Count:      len(jobs),
		NextCursor: nil,
	}, maxChars)
}

func jobStopTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (string, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("job_id is required")
	}
	if signal := strings.TrimSpace(stringArg(args, "signal")); signal != "" {
		return "", errors.New("signal is not supported for job_stop in this phase")
	}
	timeoutMS, err := jobBlockTimeoutMS(args)
	if err != nil {
		return "", err
	}

	rec, err := jm.stop(jobID)
	if err != nil {
		return "", err
	}
	if shellBoolArg(args, "block") {
		waitForJobDone(ctx, jm, jobID, time.Duration(timeoutMS)*time.Millisecond)
		if latest, err := findJobRecord(jm, jobID); err == nil {
			rec = latest
		}
	}
	return marshalBoundedJSON(jobStopResult{
		JobID:  rec.JobID,
		Status: string(rec.Status),
		Reason: stringPtrOrNil(rec.Reason),
	}, maxChars)
}

type jobReadOutputResult struct {
	JobID                 string            `json:"job_id"`
	Type                  string            `json:"type"`
	Status                string            `json:"status"`
	Reason                *string           `json:"reason"`
	Content               string            `json:"content"`
	Grep                  *string           `json:"grep,omitempty"`
	Matches               *[]jobOutputMatch `json:"matches,omitempty"`
	TotalBytes            int64             `json:"total_bytes"`
	Truncated             bool              `json:"truncated"`
	ExitCode              *int              `json:"exit_code"`
	StructuredResult      any               `json:"structured_result,omitempty"`
	StructuredResultValid *bool             `json:"structured_result_valid,omitempty"`
}

type jobOutputMatch struct {
	ByteOffset int64  `json:"byte_offset"`
	Line       string `json:"line"`
}

type jobListResult struct {
	Jobs       []jobListEntry `json:"jobs"`
	Count      int            `json:"count"`
	NextCursor *string        `json:"next_cursor"`
}

type jobListEntry struct {
	JobID              string  `json:"job_id"`
	Type               string  `json:"type"`
	Status             string  `json:"status"`
	Reason             *string `json:"reason"`
	Description        string  `json:"description"`
	ParentJobID        *string `json:"parent_job_id"`
	OwnerSessionID     string  `json:"owner_session_id"`
	VisibleToSessionID string  `json:"visible_to_session_id"`
	TranscriptRef      *string `json:"transcript_ref"`
	Resumable          *bool   `json:"resumable"`
	NotResumableReason *string `json:"not_resumable_reason"`
	StartedAt          string  `json:"started_at"`
	EndedAt            *string `json:"ended_at"`
	ExitCode           *int    `json:"exit_code"`
	OutputBytes        int64   `json:"output_bytes"`
}

type jobStopResult struct {
	JobID  string  `json:"job_id"`
	Status string  `json:"status"`
	Reason *string `json:"reason"`
}

type jobSendMessageDelegateResult struct {
	Target                string  `json:"target"`
	JobID                 string  `json:"job_id"`
	Type                  string  `json:"type"`
	Status                string  `json:"status"`
	Reason                *string `json:"reason,omitempty"`
	RunningInBackground   bool    `json:"running_in_background"`
	TimedOut              bool    `json:"timed_out,omitempty"`
	Action                string  `json:"action"`
	ResumedFromJobID      string  `json:"resumed_from_job_id,omitempty"`
	TranscriptRef         string  `json:"transcript_ref"`
	Output                *string `json:"output,omitempty"`
	Truncated             *bool   `json:"truncated,omitempty"`
	StructuredResult      any     `json:"structured_result,omitempty"`
	StructuredResultValid *bool   `json:"structured_result_valid,omitempty"`
}

type jobSendMessageAliasResult struct {
	Target      string `json:"target"`
	Delivered   bool   `json:"delivered"`
	Action      string `json:"action"`
	MessageType string `json:"message_type"`
}

type jobWatchToolResult struct {
	Target             string                `json:"target"`
	Watching           bool                  `json:"watching"`
	OutputMatch        string                `json:"output_match,omitempty"`
	Events             []string              `json:"events,omitempty"`
	ProgressIntervalMS int                   `json:"progress_interval_ms,omitempty"`
	Send               *jobWatchToolSendArgs `json:"send,omitempty"`
	ReplacedExisting   bool                  `json:"replaced_existing,omitempty"`
}

type jobWatchToolSendArgs struct {
	To             string `json:"to"`
	Message        string `json:"message,omitempty"`
	IncludeFrame   bool   `json:"include_frame,omitempty"`
	IncludeExcerpt bool   `json:"include_excerpt,omitempty"`
}

type delegateToolResult struct {
	JobID                 string  `json:"job_id"`
	Type                  string  `json:"type"`
	Status                string  `json:"status"`
	Reason                *string `json:"reason,omitempty"`
	RunningInBackground   bool    `json:"running_in_background"`
	TimedOut              bool    `json:"timed_out"`
	TranscriptRef         string  `json:"transcript_ref"`
	Output                *string `json:"output,omitempty"`
	Truncated             *bool   `json:"truncated,omitempty"`
	StructuredResult      any     `json:"structured_result,omitempty"`
	StructuredResultValid *bool   `json:"structured_result_valid,omitempty"`
}

func marshalSendMessageResult(res sendMessageResult, maxChars int) (string, error) {
	if res.MessageType == "runtime" {
		return marshalBoundedJSON(jobSendMessageAliasResult{
			Target:      res.Target,
			Delivered:   res.Delivered,
			Action:      res.Action,
			MessageType: res.MessageType,
		}, maxChars)
	}
	out := jobSendMessageDelegateResult{
		Target:              res.Target,
		JobID:               res.JobID,
		Type:                res.Type,
		Status:              string(res.Status),
		Reason:              stringPtrOrNil(res.Reason),
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		Action:              res.Action,
		ResumedFromJobID:    res.ResumedFromJobID,
		TranscriptRef:       res.TranscriptRef,
	}
	if !res.RunningInBackground || res.TimedOut {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
	}
	if res.StructuredResult != nil || res.StructuredResultValidSet {
		valid := res.StructuredResultValid
		out.StructuredResult = res.StructuredResult
		out.StructuredResultValid = &valid
	}
	return marshalBoundedSendMessageDelegateResult(out, maxChars)
}

func marshalWatchResult(res watchResult, maxChars int) (string, error) {
	out := jobWatchToolResult{
		Target:             res.Target,
		Watching:           res.Watching,
		OutputMatch:        res.OutputMatch,
		Events:             res.Events,
		ProgressIntervalMS: res.ProgressIntervalMS,
		ReplacedExisting:   res.ReplacedExisting,
	}
	if res.Send != nil {
		out.Send = &jobWatchToolSendArgs{
			To:             res.Send.To,
			Message:        res.Send.Message,
			IncludeFrame:   res.Send.IncludeFrame,
			IncludeExcerpt: res.Send.IncludeExcerpt,
		}
	}
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Target = limitWatchText(out.Target, maxJobWatchResultTextChars)
	out.OutputMatch = limitWatchText(out.OutputMatch, maxJobWatchResultTextChars)
	out.Events = boundedJobWatchResultEvents(out.Events)
	if out.Send != nil {
		out.Send.To = limitWatchText(out.Send.To, maxJobWatchResultTextChars)
		out.Send.Message = limitWatchText(out.Send.Message, maxJobWatchResultTextChars)
	}
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.OutputMatch = ""
	out.Events = nil
	if out.Send != nil {
		out.Send.Message = ""
	}
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.Send = nil
	return marshalBoundedJSON(out, maxChars)
}

func marshalDelegateResult(res delegateResult, maxChars int) (string, error) {
	out := delegateToolResult{
		JobID:               res.JobID,
		Type:                res.Type,
		Status:              string(res.Status),
		Reason:              stringPtrOrNil(res.Reason),
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		TranscriptRef:       res.TranscriptRef,
	}
	if !res.RunningInBackground || res.TimedOut {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
	}
	if res.StructuredResult != nil || res.StructuredResultValidSet {
		valid := res.StructuredResultValid
		out.StructuredResult = res.StructuredResult
		out.StructuredResultValid = &valid
	}
	return marshalBoundedDelegateResult(out, maxChars)
}

func marshalBoundedSendMessageDelegateResult(out jobSendMessageDelegateResult, maxChars int) (string, error) {
	if fit, ok, err := marshalSendMessageDelegateResultWithOutputLimit(out, maxChars); err != nil || ok {
		return fit, err
	}
	empty := ""
	out.Output = &empty
	truncated := true
	out.Truncated = &truncated
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.StructuredResult = nil
	invalid := false
	out.StructuredResultValid = &invalid
	return marshalBoundedJSON(out, maxChars)
}

func marshalSendMessageDelegateResultWithOutputLimit(out jobSendMessageDelegateResult, maxChars int) (string, bool, error) {
	if out.Output == nil {
		return marshalBoundedJSONWithFit(out, maxChars)
	}
	original := []rune(*out.Output)
	originalTruncated := out.Truncated != nil && *out.Truncated
	return marshalWithOutputLimit(maxChars, len(original), func(keep int) (string, error) {
		tail := string(original[len(original)-keep:])
		out.Output = &tail
		truncated := originalTruncated || keep < len(original)
		out.Truncated = &truncated
		b, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}

func marshalBoundedDelegateResult(out delegateToolResult, maxChars int) (string, error) {
	if fit, ok, err := marshalDelegateResultWithOutputLimit(out, maxChars); err != nil || ok {
		return fit, err
	}
	empty := ""
	out.Output = &empty
	truncated := true
	out.Truncated = &truncated
	if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok {
		return fit, err
	}
	out.StructuredResult = nil
	invalid := false
	out.StructuredResultValid = &invalid
	return marshalBoundedJSON(out, maxChars)
}

func marshalDelegateResultWithOutputLimit(out delegateToolResult, maxChars int) (string, bool, error) {
	if out.Output == nil {
		return marshalBoundedJSONWithFit(out, maxChars)
	}
	original := []rune(*out.Output)
	originalTruncated := out.Truncated != nil && *out.Truncated
	return marshalWithOutputLimit(maxChars, len(original), func(keep int) (string, error) {
		tail := string(original[len(original)-keep:])
		out.Output = &tail
		truncated := originalTruncated || keep < len(original)
		out.Truncated = &truncated
		b, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(b), nil
	})
}

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

func marshalWithOutputLimit(maxChars, outputRunes int, marshal func(keep int) (string, error)) (string, bool, error) {
	best := ""
	bestOK := false
	lo, hi := 0, outputRunes
	for lo <= hi {
		mid := lo + (hi-lo)/2
		candidate, err := marshal(mid)
		if err != nil {
			return "", false, err
		}
		if maxChars <= 0 || jsonCharLen([]byte(candidate)) <= maxChars {
			best = candidate
			bestOK = true
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, bestOK, nil
}

func sessionJobManager(s *Session) (*jobManager, error) {
	if s == nil || s.jobManager == nil {
		return nil, errors.New(jobManagerUnavailableReason)
	}
	return s.jobManager, nil
}

func boundedJobBytesArg(args map[string]any, key string, defaultValue int) (int, error) {
	value := defaultValue
	if n, ok := shellIntArg(args, key); ok {
		value = n
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", key)
	}
	if value > maxJobOutputBytes {
		value = maxJobOutputBytes
	}
	return value, nil
}

func jobReadMaxCharsArg(args map[string]any, registryMaxChars int) (int, error) {
	maxChars := registryMaxChars
	if maxChars <= 0 {
		maxChars = jobToolResultDefaultMaxChar
	}
	if n, ok := shellIntArg(args, "max_chars"); ok {
		if n <= 0 {
			return 0, errors.New("max_chars must be greater than 0")
		}
		if n < maxChars {
			maxChars = n
		}
	}
	if maxChars < jobToolResultMinJSONChars {
		maxChars = jobToolResultMinJSONChars
	}
	return maxChars, nil
}

func jobListFilterFromArgs(args map[string]any) (listFilter, error) {
	limit := defaultJobListLimit
	if n, ok := shellIntArg(args, "limit"); ok {
		limit = n
	}
	if limit <= 0 {
		return listFilter{}, errors.New("limit must be greater than 0")
	}
	if limit > maxJobListLimit {
		limit = maxJobListLimit
	}

	statuses, err := jobStatusArrayArg(args, "status")
	if err != nil {
		return listFilter{}, err
	}
	types, err := jobTypeArrayArg(args, "type")
	if err != nil {
		return listFilter{}, err
	}
	return listFilter{
		Statuses: statuses,
		Types:    types,
		Limit:    limit,
	}, nil
}

func jobStatusArrayArg(args map[string]any, key string) ([]jobstore.Status, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	statuses := make([]jobstore.Status, 0, len(values))
	for _, value := range values {
		status := jobstore.Status(fmt.Sprint(value))
		switch status {
		case jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusFailed, jobstore.StatusCancelled, jobstore.StatusStopped:
			statuses = append(statuses, status)
		default:
			return nil, fmt.Errorf("invalid job status %q", status)
		}
	}
	return statuses, nil
}

func watchArgsFromToolArgs(args map[string]any) (watchArgs, error) {
	a := watchArgs{
		Target:      strings.TrimSpace(stringArg(args, "target")),
		OutputMatch: stringArg(args, "output_match"),
		Clear:       shellBoolArg(args, "clear"),
	}
	if n, ok := shellIntArg(args, "progress_interval_ms"); ok {
		a.ProgressIntervalMS = n
	}
	events, err := stringArrayArg(args, "events")
	if err != nil {
		return watchArgs{}, err
	}
	a.Events = events
	if trigger, err := watchTriggerArg(args); err != nil {
		return watchArgs{}, err
	} else if trigger != nil {
		a.TriggerEvent = trigger.event
		a.TriggerEvery = trigger.every
	}
	send, err := watchSendArg(args)
	if err != nil {
		return watchArgs{}, err
	}
	a.Send = send
	return a, nil
}

func stringArrayArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s values must be strings", key)
		}
		out = append(out, s)
	}
	return out, nil
}

type watchTriggerToolArgs struct {
	event string
	every int
}

func watchTriggerArg(args map[string]any) (*watchTriggerToolArgs, error) {
	raw, ok := args["trigger"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("trigger must be an object")
	}
	trigger := &watchTriggerToolArgs{event: stringArg(values, "event")}
	if n, ok := shellIntArg(values, "every"); ok {
		trigger.every = n
	}
	return trigger, nil
}

func watchSendArg(args map[string]any) (*watchSendArgs, error) {
	raw, ok := args["send"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("send must be an object")
	}
	to := strings.TrimSpace(stringArg(values, "to"))
	if to == "" {
		return nil, errors.New("invalid_request: send.to is required")
	}
	return &watchSendArgs{
		To:             to,
		Message:        stringArg(values, "message"),
		IncludeFrame:   shellBoolArg(values, "include_frame"),
		IncludeExcerpt: shellBoolArg(values, "include_excerpt"),
	}, nil
}

func boundedJobWatchResultEvents(events []string) []string {
	if len(events) == 0 {
		return nil
	}
	limit := len(events)
	if limit > maxJobWatchResultEvents {
		limit = maxJobWatchResultEvents
	}
	out := make([]string, 0, limit)
	for _, event := range events[:limit] {
		out = append(out, limitWatchText(event, maxJobWatchResultTextChars))
	}
	return out
}

func validateJobGrepPattern(pattern string, maxChars int) error {
	if len([]byte(pattern)) > maxJobGrepPatternBytes {
		return fmt.Errorf("grep must be at most %d bytes", maxJobGrepPatternBytes)
	}
	b, err := json.Marshal(pattern)
	if err != nil {
		return err
	}
	if jsonCharLen(b) > maxJobGrepPatternJSONChars(maxChars) {
		return errors.New("grep is too large after JSON escaping")
	}
	return nil
}

func maxJobGrepPatternJSONChars(maxChars int) int {
	limit := maxChars / 4
	if limit < 64 {
		return 64
	}
	return limit
}

func jobTypeArrayArg(args map[string]any, key string) ([]jobstore.JobType, error) {
	raw, ok := args[key]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	types := make([]jobstore.JobType, 0, len(values))
	for _, value := range values {
		jobType := jobstore.JobType(fmt.Sprint(value))
		switch jobType {
		case jobstore.JobShell, jobstore.JobDelegate:
			types = append(types, jobType)
		default:
			return nil, fmt.Errorf("invalid job type %q", jobType)
		}
	}
	return types, nil
}

func jobBlockTimeoutMS(args map[string]any) (int, error) {
	timeoutMS := defaultJobBlockTimeoutMS
	if n, ok := shellIntArg(args, "block_timeout_ms"); ok {
		timeoutMS = n
	}
	if timeoutMS < 0 {
		return 0, errors.New("block_timeout_ms must be non-negative")
	}
	if timeoutMS == 0 {
		return defaultJobBlockTimeoutMS, nil
	}
	if timeoutMS < minJobBlockTimeoutMS {
		return minJobBlockTimeoutMS, nil
	}
	if timeoutMS > maxJobBlockTimeoutMS {
		return maxJobBlockTimeoutMS, nil
	}
	return timeoutMS, nil
}

func findJobRecord(jm *jobManager, jobID string) (*jobstore.JobRecord, error) {
	recs, err := jm.listWithError(listFilter{})
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		if rec.JobID == jobID {
			return rec, nil
		}
	}
	return nil, fmt.Errorf("job %q not found", jobID)
}

func waitForJobDone(ctx context.Context, jm *jobManager, jobID string, timeout time.Duration) {
	done, ok := jobDone(jm, jobID)
	if !ok {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
}

func waitForJobDoneOrOutput(ctx context.Context, jm *jobManager, jobID string, timeout time.Duration) {
	initial, _ := jobOutputBytes(jm, jobID)
	done, ok := jobDone(jm, jobID)
	if !ok {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			current, err := jobOutputBytes(jm, jobID)
			if err == nil && current > initial {
				return
			}
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func jobOutputBytes(jm *jobManager, jobID string) (int64, error) {
	_, total, _, err := jm.readOutput(jobID, 0)
	return total, err
}

func jobDone(jm *jobManager, jobID string) (<-chan struct{}, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		return nil, false
	}
	return run.done, true
}

func boundedMatchLine(line string) string {
	if len([]byte(line)) <= maxJobGrepLineBytes {
		return line
	}
	runes := []rune(line)
	for len(runes) > 0 && len([]byte(string(runes))) > maxJobGrepLineBytes {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func projectJobOutputMatches(matches []jobstore.Match) []jobOutputMatch {
	out := make([]jobOutputMatch, 0, len(matches))
	for i, match := range matches {
		if i >= maxJobGrepMatches {
			break
		}
		out = append(out, jobOutputMatch{
			ByteOffset: match.ByteOffset,
			Line:       boundedMatchLine(match.Line),
		})
	}
	return out
}

func projectJobRecord(rec *jobstore.JobRecord) jobListEntry {
	return jobListEntry{
		JobID:              rec.JobID,
		Type:               string(rec.Type),
		Status:             string(rec.Status),
		Reason:             stringPtrOrNil(rec.Reason),
		Description:        rec.Description,
		ParentJobID:        stringPtrOrNil(rec.ParentJobID),
		OwnerSessionID:     rec.OwnerSessionID,
		VisibleToSessionID: rec.VisibleToSession,
		TranscriptRef:      stringPtrOrNil(rec.TranscriptRef),
		Resumable:          rec.Resumable,
		NotResumableReason: stringPtrOrNil(rec.NotResumableWhy),
		StartedAt:          rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:            timePtrOrNil(rec.EndedAt),
		ExitCode:           rec.ExitCode,
		OutputBytes:        rec.OutputBytes,
	}
}

func marshalBoundedJobReadOutputResult(out jobReadOutputResult, maxChars int) (string, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	if jsonCharLen(b) <= maxChars {
		return string(b), nil
	}

	out.Truncated = true
	if fit, ok, err := marshalJobReadOutputWithContentLimit(out, maxChars); err != nil || ok {
		return fit, err
	}
	if out.Matches != nil {
		empty := []jobOutputMatch{}
		out.Matches = &empty
	}
	if fit, ok, err := marshalJobReadOutputWithContentLimit(out, maxChars); err != nil || ok {
		return fit, err
	}
	if out.StructuredResult != nil {
		out.StructuredResult = nil
		invalid := false
		out.StructuredResultValid = &invalid
		if fit, ok, err := marshalJobReadOutputWithContentLimit(out, maxChars); err != nil || ok {
			return fit, err
		}
	}
	out.Content = ""
	return marshalBoundedJSON(out, maxChars)
}

func marshalJobReadOutputWithContentLimit(out jobReadOutputResult, maxChars int) (string, bool, error) {
	original := []rune(out.Content)
	best := ""
	bestOK := false
	lo, hi := 0, len(original)
	for lo <= hi {
		mid := lo + (hi-lo)/2
		out.Content = string(original[len(original)-mid:])
		b, err := json.Marshal(out)
		if err != nil {
			return "", false, err
		}
		if jsonCharLen(b) <= maxChars {
			best = string(b)
			bestOK = true
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, bestOK, nil
}

func marshalBoundedJobListResult(out jobListResult, maxChars int) (string, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	if jsonCharLen(b) <= maxChars {
		return string(b), nil
	}

	jobs := out.Jobs
	best := ""
	lo, hi := 0, len(jobs)
	for lo <= hi {
		mid := lo + (hi-lo)/2
		out.Jobs = jobs[:mid]
		out.Count = len(out.Jobs)
		b, err = json.Marshal(out)
		if err != nil {
			return "", err
		}
		if jsonCharLen(b) <= maxChars {
			best = string(b)
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best != "" {
		return best, nil
	}
	out.Jobs = nil
	out.Count = 0
	return marshalBoundedJSON(out, maxChars)
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
	for _, name := range []string{"job_read_output", "job_list", "job_stop", "delegate", "job_watch", "job_send_message"} {
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
