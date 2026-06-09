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
)

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
	return reg.Register(tool.RegisteredTool{
		Tool:  llm.Tool{Definition: tool.DefJobStop()},
		Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return jobStopTool(ctx, s, args, jobToolResultMaxChars(reg, "job_stop"))
		},
	})
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
	maxChars, err := jobReadMaxCharsArg(args, registryMaxChars)
	if err != nil {
		return "", err
	}

	if shellBoolArg(args, "block") {
		timeoutMS, err := jobBlockTimeoutMS(args)
		if err != nil {
			return "", err
		}
		waitForJobDone(ctx, jm, jobID, time.Duration(timeoutMS)*time.Millisecond)
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
	if grep := stringArg(args, "grep"); grep != "" {
		if len([]byte(grep)) > maxJobGrepPatternBytes {
			return "", fmt.Errorf("grep must be at most %d bytes", maxJobGrepPatternBytes)
		}
		matches, err := grepJobOutput(content, totalBytes, truncated, grep)
		if err != nil {
			return "", err
		}
		result.Grep = &grep
		result.Matches = &matches
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
	_ = strings.TrimSpace(stringArg(args, "signal"))
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
	JobID      string            `json:"job_id"`
	Type       string            `json:"type"`
	Status     string            `json:"status"`
	Reason     *string           `json:"reason"`
	Content    string            `json:"content"`
	Grep       *string           `json:"grep,omitempty"`
	Matches    *[]jobOutputMatch `json:"matches,omitempty"`
	TotalBytes int64             `json:"total_bytes"`
	Truncated  bool              `json:"truncated"`
	ExitCode   *int              `json:"exit_code"`
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

func jobDone(jm *jobManager, jobID string) (<-chan struct{}, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		return nil, false
	}
	return run.done, true
}

func grepJobOutput(content string, totalBytes int64, truncated bool, pattern string) ([]jobOutputMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	baseOffset := int64(0)
	if truncated {
		baseOffset = totalBytes - int64(len([]byte(content)))
		if baseOffset < 0 {
			baseOffset = 0
		}
	}

	var matches []jobOutputMatch
	offset := int64(0)
	for len(content) > 0 && len(matches) < maxJobGrepMatches {
		line := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			line = content[:idx+1]
			content = content[idx+1:]
		} else {
			content = ""
		}
		trimmed := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		loc := re.FindStringIndex(trimmed)
		if loc != nil {
			matches = append(matches, jobOutputMatch{
				ByteOffset: baseOffset + offset + int64(loc[0]),
				Line:       boundedMatchLine(trimmed),
			})
		}
		offset += int64(len([]byte(line)))
	}
	return matches, nil
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
