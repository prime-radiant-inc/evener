package agent

import (
	"bytes"
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
	// digestHeadLines/digestTailLines size the bare job_read_output default: a
	// head+tail line digest (first N + last N lines) with the middle elided. The
	// agent pages with explicit head_lines/tail_lines for more.
	digestHeadLines = 100
	digestTailLines = 100
	// jobLineReadBudget bounds the bytes read per side when slicing lines, so a
	// pathological run of very long lines can't blow context.
	jobLineReadBudget           = 256 * 1024
	maxJobOutputBytes           = 1048576
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
	maxJobGrepPatternBytes      = 4096
	maxJobWatchResultTextChars  = 128
	maxJobWatchResultEvents     = 8
	// grantedReadBlockUnsupportedErr is the rejection for max_wait_ms>0 on a
	// cross-session read: a watch-granted read (spec §5.1) or a depth >= 2
	// descendant read resolved through the recursive owner path (spec §2). Both
	// are snapshot-only by decision: the blocking wait helpers are coupled to a
	// resolvable jobManager (done channels, output polling) that these
	// cross-session views do not own at the caller, and half-supporting the wait
	// by silently degrading it to a snapshot would lie to the model about having
	// waited.
	grantedReadBlockUnsupportedErr = "invalid_request: max_wait_ms is not supported for cross-session reads"
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

func jobSendMessageTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	a := sendMessageArgs{
		Target:     stringArg(args, "target"),
		Message:    stringArg(args, "message"),
		OnFinished: stringArg(args, "on_finished"),
		Background: true, // default: no wait, return immediately
	}
	// max_wait_ms: 0/absent = no wait; positive = wait inline up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		clamped := n
		if clamped < minJobBlockTimeoutMS {
			clamped = minJobBlockTimeoutMS
		}
		if clamped > maxJobBlockTimeoutMS {
			clamped = maxJobBlockTimeoutMS
		}
		a.Background = false
		a.BackgroundSet = true
		a.BlockTimeoutMS = clamped
	}

	res := s.sendDelegateMessage(ctx, a)
	if res.Err != nil {
		return "", res.Err
	}
	res.WaitIgnoredReason = liveSteerWaitIgnoredReason(a.BlockTimeoutMS, res.Status, res.Action)
	return marshalSendMessageResult(res, maxChars)
}

// liveSteerWaitIgnoredReason returns a note when a caller passed a positive
// max_wait_ms but the send was a live steer of a running delegate, which returns on
// delivery and cannot honor the wait. It returns "" when the wait was honored (a
// resumed job) or not requested, so the field stays omitted in the common case.
func liveSteerWaitIgnoredReason(blockTimeoutMS int, status jobstore.Status, action string) string {
	if blockTimeoutMS > 0 && status == jobstore.StatusRunning && (action == "sent" || action == "busy") {
		return "live steer returns on delivery; max_wait_ms applies only to resumed jobs"
	}
	return ""
}

func jobWatchTool(s *Session, args map[string]any, maxChars int) (any, error) {
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
		// Watches resolve own jobs only (spec §3/§8): a target_not_found for a
		// target that is actually a known descendant in the live subtree is
		// enriched with the delegate-the-watching guidance — the model should have
		// the owning session attach the watch, since only it can watch its own job.
		if errors.Is(err, errWatchTargetNotFound) {
			if _, owner, _, _, ok := s.resolveDescendantJobOwner(a.Target); ok && owner != nil {
				return "", fmt.Errorf("target_not_watchable: job %q is owned by descendant session %q, which you cannot watch directly (watches resolve own jobs only); delegate the watching to session %q so it attaches the watch on its own job", a.Target, owner.id, owner.id)
			}
		}
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
		Background:      true, // default: no wait, return job_id immediately
	}
	// max_wait_ms: 0/absent = no wait (background); positive = wait inline up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		clamped := n
		if clamped < minJobBlockTimeoutMS {
			clamped = minJobBlockTimeoutMS
		}
		if clamped > maxJobBlockTimeoutMS {
			clamped = maxJobBlockTimeoutMS
		}
		a.Background = false
		a.BlockTimeoutMS = clamped
	}
	// delegation_allowance: 0/absent = leaf delegate (cannot delegate); positive
	// = grant; negative = invalid_request. Zero reads as unset (strict-zero rule).
	// createDelegate enforces the grant rule (strictly less than own allowance).
	if n, ok := shellIntArg(args, "delegation_allowance"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: delegation_allowance must be non-negative")
		}
		a.DelegationAllowance = n
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

func jobReadOutputTool(ctx context.Context, s *Session, args map[string]any, registryMaxChars int) (any, error) {
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("invalid_request: job_id is required")
	}
	jm, resolvedRec, err := s.nestedOrLocalJobManager(jobID)
	// readSession is the session whose store the snapshot is served from: the
	// caller for own/depth-1 reads, the resolved OWNER for a depth >= 2
	// descendant (the T11 advisory — projection, resumability, and the
	// closed-store fallback all key on the owner). deepDescendant marks a
	// resolved depth >= 2 read, which is snapshot-only like a granted read.
	readSession := s
	// fallbackTarget is the session the closed-store read fallback resolves its
	// replacement store from: the caller for own/depth-1 reads (its store holds
	// the depth-1 forwarded copy), and the OWNER'S DIRECT PARENT for a depth >= 2
	// descendant (forwarding is single-hop, so the forwarded terminal copy lands
	// in the owner's parent store, not the root). For a direct child of the
	// caller the owner's parent IS the caller, so depth-1 stays identical.
	fallbackTarget := s
	deepDescendant := false
	// The one-hop resolver reaches only the caller and its direct children. A
	// job that is missing locally, or surfaced only as a forwarded copy of a
	// non-self owner, may be owned by a descendant at depth >= 2 (spec §2):
	// resolve it through the recursive owner path. A live direct-child read
	// (jm != local) and a job the caller itself owns are left untouched. A miss
	// (e.g. the owning branch is closed, or the job truly does not exist) leaves
	// the original resolution — including its error — unchanged.
	oneHopLocalMiss := jm == s.jobManager && (err != nil || (resolvedRec != nil && resolvedRec.OwnerSessionID != s.id))
	if oneHopLocalMiss {
		if owner, ownerSess, ownerParent, _, ok := s.resolveDescendantJobOwner(jobID); ok {
			jm = owner
			readSession = ownerSess
			fallbackTarget = ownerParent
			deepDescendant = true
			err = nil
		}
	}
	var granted *grantedJobRead
	if err != nil {
		// Local + nested resolution failed: consult the parent-injected watch
		// read-grant seam (spec §5.1) before failing. A grant hit serves the
		// read from the parent's store; a miss preserves the original
		// target_not_found error.
		lookup := s.cfg.spawn.parentGrantedJobRead
		if lookup == nil {
			return "", err
		}
		g, ok := lookup(s.id, jobID)
		if !ok {
			return "", err
		}
		granted = g
	}
	headLines, hasHead, err := strictZeroJobBytesArg(args, "head_lines")
	if err != nil {
		return "", err
	}
	tailLines, hasTail, err := strictZeroJobBytesArg(args, "tail_lines")
	if err != nil {
		return "", err
	}
	fromLine, hasFromLine, err := strictZeroJobBytesArg(args, "from_line")
	if err != nil {
		return "", err
	}
	lineCount, _, err := strictZeroJobBytesArg(args, "line_count")
	if err != nil {
		return "", err
	}
	if hasFromLine && (hasHead || hasTail) {
		return "", errors.New("invalid_request: from_line cannot be combined with head_lines/tail_lines")
	}
	if hasFromLine && lineCount <= 0 {
		lineCount = digestHeadLines // default window when from_line has no line_count
	}
	maxChars := registryMaxChars
	grep := stringArg(args, "grep")
	var grepRE *regexp.Regexp
	if grep != "" {
		if err := validateJobGrepPattern(grep, maxChars); err != nil {
			return "", err
		}
		re, err := regexp.Compile(grep)
		if err != nil {
			return "", fmt.Errorf("invalid_request: invalid grep pattern: %w", err)
		}
		grepRE = re
	}

	// max_wait_ms: 0/absent = snapshot now; positive = bounded wait;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	maxWaitMS := 0
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		maxWaitMS = n
	}
	if maxWaitMS > 0 {
		if granted != nil || deepDescendant {
			return "", errors.New(grantedReadBlockUnsupportedErr)
		}
		clamped := maxWaitMS
		if clamped < minJobBlockTimeoutMS {
			clamped = minJobBlockTimeoutMS
		}
		if clamped > maxJobBlockTimeoutMS {
			clamped = maxJobBlockTimeoutMS
		}
		timeout := time.Duration(clamped) * time.Millisecond
		if grepRE != nil {
			waitForJobGrepMatch(ctx, jm, jobID, grepRE, timeout)
		} else {
			waitForJobDoneOrOutput(ctx, jm, jobID, timeout)
		}
	}

	// readWindow reads one byte window (head or tail) through whichever path
	// applies — granted cross-session read or the own/closed-store fallback.
	readWindow := func(budget int, fromHead bool) (jobReadOutputSnapshot, error) {
		if granted != nil {
			return granted.snapshot(budget, fromHead, nil)
		}
		return readSession.readJobOutputSnapshot(jm, fallbackTarget, jobID, budget, fromHead, nil)
	}

	var snap jobReadOutputSnapshot
	switch {
	case grepRE != nil:
		if granted != nil {
			snap, err = granted.snapshot(jobLineReadBudget, false, grepRE)
		} else {
			snap, err = readSession.readJobOutputSnapshot(jm, fallbackTarget, jobID, jobLineReadBudget, false, grepRE)
		}
	case hasFromLine:
		snap, err = readWindow(maxJobOutputBytes, true)
		if err == nil {
			sliced, _, before, after := midLineBytes([]byte(snap.Content), fromLine, lineCount)
			snap.Content = string(sliced)
			if before || after {
				snap.Truncated = true // lines outside the requested range exist
			}
		}
	case hasHead && hasTail:
		snap, err = readJobOutputDigest(readWindow, headLines, tailLines)
	case hasHead:
		snap, err = readWindow(jobLineReadBudget, true)
		if err == nil {
			sliced, _, more := firstLineBytes([]byte(snap.Content), headLines)
			snap.Content = string(sliced)
			if more {
				snap.Truncated = true // the line slice dropped later lines
			}
		}
	case hasTail:
		snap, err = readWindow(jobLineReadBudget, false)
		if err == nil {
			sliced, _, more := lastLineBytes([]byte(snap.Content), tailLines)
			snap.Content = string(sliced)
			if more {
				snap.Truncated = true // the line slice dropped earlier lines
			}
		}
	default:
		snap, err = readJobOutputDigest(readWindow, digestHeadLines, digestTailLines)
	}
	if err != nil {
		return "", err
	}
	rec := snap.Record

	result := jobReadOutputResult{
		JobID:        rec.JobID,
		Type:         string(rec.Type),
		Status:       string(rec.Status),
		Reason:       stringPtrOrNil(rec.Reason),
		Content:      snap.Content,
		TotalBytes:   snap.TotalBytes,
		DroppedBytes: snap.DroppedBytes,
		OutputStatus: outputWindowStatus(snap.TotalBytes, snap.DroppedBytes, snap.Truncated),
		Truncated:    snap.Truncated,
		ExitCode:     rec.ExitCode,
		LastActivity: lastActivityProjection(rec),
	}
	if rec.StructuredResult != nil {
		result.StructuredResult = rec.StructuredResult
	}
	if rec.StructuredResultValid != nil {
		result.StructuredResultValid = rec.StructuredResultValid
	}
	result.StructuredResultReason = rec.StructuredResultReason
	if grep != "" {
		result.Grep = &grep
		projected := projectJobOutputMatches(snap.Matches)
		result.Matches = &projected
	}
	header := ""
	if hasFromLine {
		header = fmt.Sprintf("--- from line %d ---", fromLine)
	}
	return tool.StateResult{Output: formatJobReadOutput(&result, header, maxChars), State: result}, nil
}

// formatJobReadOutput renders a job_read_output result as plain text: an optional
// line-range header, the output (or grep matches), then a bracketed footer with
// job_id, status, exit code, output_status, and the retained/dropped byte counts.
// A delegate's structured_result is appended as JSON since it is genuinely
// structured data the caller requested. Graceful degradation (spec): if including
// the structured_result would overflow maxChars it is dropped from the model wire
// and flagged (projection_too_large) — mutating out so the State reflects the drop
// — while the durable record keeps the full value. The footer always survives so
// the model can re-read with a narrower window.
func formatJobReadOutput(out *jobReadOutputResult, header string, maxChars int) string {
	if out.Matches != nil {
		var b strings.Builder
		for _, m := range *out.Matches {
			fmt.Fprintf(&b, "%d: %s\n", m.ByteOffset, m.Line)
		}
		fmt.Fprintf(&b, "[%d match(es) for %q in job %s]", len(*out.Matches), derefString(out.Grep), out.JobID)
		return b.String()
	}

	var body strings.Builder
	if header != "" {
		body.WriteString(header)
		body.WriteByte('\n')
	}
	if out.Content != "" {
		body.WriteString(out.Content)
		if !strings.HasSuffix(out.Content, "\n") {
			body.WriteByte('\n')
		}
	}
	foot := []string{"job " + out.JobID, out.Status}
	if out.ExitCode != nil {
		foot = append(foot, fmt.Sprintf("exit %d", *out.ExitCode))
	}
	if out.OutputStatus != "" {
		foot = append(foot, out.OutputStatus)
	}
	if out.TotalBytes > 0 {
		foot = append(foot, humanBytes(out.TotalBytes)+" total")
	}
	if out.DroppedBytes > 0 {
		foot = append(foot, humanBytes(out.DroppedBytes)+" dropped")
	}
	base := body.String() + "[" + strings.Join(foot, " · ") + "]"

	if out.StructuredResult == nil {
		return base
	}
	srText := ""
	if sr, err := json.Marshal(out.StructuredResult); err == nil {
		valid := out.StructuredResultValid != nil && *out.StructuredResultValid
		srText = fmt.Sprintf("\nstructured_result (valid=%v): %s", valid, sr)
	}
	if maxChars <= 0 || len([]rune(base+srText)) <= maxChars {
		return base + srText
	}
	// Too large to fit: drop the structured_result from the wire and flag it, so the
	// State the hub renders reflects the drop while the durable record is untouched.
	out.StructuredResult = nil
	invalid := false
	out.StructuredResultValid = &invalid
	out.StructuredResultReason = structuredResultReasonProjectionTooLarge
	return base + "\n[structured_result omitted: projection_too_large — re-read this job to retrieve it]"
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

type jobReadOutputSnapshot struct {
	Manager      *jobManager
	Record       *jobstore.JobRecord
	Content      string
	TotalBytes   int64
	DroppedBytes int64
	Truncated    bool
	Matches      []jobstore.Match
}

// readJobOutputSnapshot reads jobID's snapshot from jm, retrying through the
// closed-store fallback when jm's store has closed. fallbackTarget is the
// session whose store the fallback redirects to: the caller for own/depth-1
// reads, the OWNER'S DIRECT PARENT for a resolved depth >= 2 descendant (where
// the single-hop forwarded copy lands). The receiver s remains the read/owner
// session (the T11 projection key); only the fallback's replacement-store
// resolution keys on fallbackTarget.
func (s *Session) readJobOutputSnapshot(jm *jobManager, fallbackTarget *Session, jobID string, readBytes int, fromHead bool, grepRE *regexp.Regexp) (jobReadOutputSnapshot, error) {
	for {
		_, err := findJobRecord(jm, jobID)
		if err != nil {
			next, ok, fallbackErr := fallbackTarget.jobReadClosedStoreFallback(jm, err)
			if ok {
				jm = next
				continue
			}
			return jobReadOutputSnapshot{}, fallbackErr
		}

		content, totalBytes, dropped, truncated, err := jm.readJobWindow(jobID, readBytes, fromHead)
		if err != nil {
			next, ok, fallbackErr := fallbackTarget.jobReadClosedStoreFallback(jm, err)
			if ok {
				jm = next
				continue
			}
			return jobReadOutputSnapshot{}, fallbackErr
		}

		rec, err := findJobRecord(jm, jobID)
		if err != nil {
			next, ok, fallbackErr := fallbackTarget.jobReadClosedStoreFallback(jm, err)
			if ok {
				jm = next
				continue
			}
			return jobReadOutputSnapshot{}, fallbackErr
		}

		var matches []jobstore.Match
		if grepRE != nil {
			matches, err = jm.grepOutput(jobID, grepRE)
			if err != nil {
				next, ok, fallbackErr := fallbackTarget.jobReadClosedStoreFallback(jm, err)
				if ok {
					jm = next
					continue
				}
				return jobReadOutputSnapshot{}, fallbackErr
			}
			rec, err = findJobRecord(jm, jobID)
			if err != nil {
				next, ok, fallbackErr := fallbackTarget.jobReadClosedStoreFallback(jm, err)
				if ok {
					jm = next
					continue
				}
				return jobReadOutputSnapshot{}, fallbackErr
			}
		}

		return jobReadOutputSnapshot{
			Manager:      jm,
			Record:       rec,
			Content:      content,
			TotalBytes:   totalBytes,
			DroppedBytes: dropped,
			Truncated:    truncated,
			Matches:      matches,
		}, nil
	}
}

// snapshot serves a watch-granted cross-session read from the parent store's
// read-only view: the same content/grep shape as readJobOutputSnapshot, but
// the record is the lookup-time clone, Manager stays nil (the observer has no
// handle on the parent's jobManager), and there is no closed-store fallback
// chain — a failed parent-side read is a real error.
func (g *grantedJobRead) snapshot(readBytes int, fromHead bool, grepRE *regexp.Regexp) (jobReadOutputSnapshot, error) {
	content, totalBytes, dropped, truncated, err := g.readWindow(readBytes, fromHead)
	if err != nil {
		return jobReadOutputSnapshot{}, err
	}
	var matches []jobstore.Match
	if grepRE != nil {
		matches, err = g.grepOutput(grepRE)
		if err != nil {
			return jobReadOutputSnapshot{}, err
		}
	}
	return jobReadOutputSnapshot{
		Record:       g.record,
		Content:      content,
		TotalBytes:   totalBytes,
		DroppedBytes: dropped,
		Truncated:    truncated,
		Matches:      matches,
	}, nil
}

// jobReadClosedStoreFallback redirects a closed-store read to the receiver's own
// store, which holds the single-hop forwarded copy of the closed owner's job.
// The receiver is the fallback target: the caller for own/depth-1 reads (whose
// store holds the depth-1 forwarded copy), the OWNER'S DIRECT PARENT for a
// depth >= 2 descendant. The current == local guard keeps the redirect a no-op
// when the closed store IS the receiver's store (nothing further to fall back
// to); for depth >= 2 the owner's-parent store differs from the closed owner
// store, so the forwarded copy is recovered.
func (s *Session) jobReadClosedStoreFallback(current *jobManager, err error) (*jobManager, bool, error) {
	if !errors.Is(err, jobstore.ErrStoreClosed) {
		return nil, false, err
	}
	local, localErr := sessionJobManager(s)
	if localErr != nil {
		return nil, false, localErr
	}
	if local == current {
		return nil, false, err
	}
	return local, true, nil
}

func jobListTool(s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	filter, err := jobListFilterFromArgs(args)
	if err != nil {
		return "", err
	}
	var jobs []jobListEntry
	if filter.IncludeDescendants {
		descJobs, listErr := s.walkDescendantJobs(filter)
		if listErr != nil {
			return "", listErr
		}
		jobs = descJobs
	} else {
		recs, listErr := jm.listWithError(filter)
		if listErr != nil {
			return "", listErr
		}
		jobs = make([]jobListEntry, 0, len(recs))
		for _, rec := range recs {
			jobs = append(jobs, projectJobRecord(s, rec))
		}
	}
	s.mu.Lock()
	allowance := s.delegationAllowance
	s.mu.Unlock()
	result := jobListResult{
		Jobs:          jobs,
		Count:         len(jobs),
		Watches:       jm.liveWatchSummaries(),
		RecentWatches: jm.recentWatchSummaries(),
	}
	// Allowance ≤ 1 can only grant 0 — a no-op knob; surface it only when it
	// actually enables fan-out (mirrors eliding the delegate schema param).
	if allowance > 1 {
		result.DelegationAllowance = allowance
	}
	_ = maxChars
	return tool.StateResult{Output: formatJobList(result), State: result}, nil
}

// formatJobList renders job_list as plain text: a schema header, then one job per
// line — job_id, type, status, a label (description or shell command), and a
// bracketed detail tail (started time, reason, exit code, size, resumability) —
// then a count footer with the delegation allowance and any active/recent watches.
func formatJobList(out jobListResult) string {
	var b strings.Builder
	if len(out.Jobs) > 0 {
		b.WriteString("# job_id  type  status  label  [started · reason · exit · bytes]\n")
	}
	for _, j := range out.Jobs {
		fmt.Fprintf(&b, "%s  %s  %s", j.JobID, j.Type, j.Status)
		if j.Depth > 0 {
			fmt.Fprintf(&b, "  depth=%d", j.Depth)
		}
		label := j.Description
		if label == "" && j.Command != nil {
			label = *j.Command
		}
		if label != "" {
			fmt.Fprintf(&b, "  %s", label)
		}
		var detail []string
		if started := shortTimestamp(j.StartedAt); started != "" {
			detail = append(detail, "started "+started)
		}
		if j.Reason != nil && *j.Reason != "" {
			detail = append(detail, *j.Reason)
		}
		if j.ExitCode != nil {
			detail = append(detail, fmt.Sprintf("exit %d", *j.ExitCode))
		}
		detail = append(detail, fmt.Sprintf("%d bytes", j.TotalBytes))
		// Surface resumability only when a job actually is resumable; resumable=false
		// for an ordinary shell job is noise (keeps rows lean).
		if j.Resumable != nil && *j.Resumable {
			detail = append(detail, "resumable")
		}
		fmt.Fprintf(&b, "  [%s]\n", strings.Join(detail, " · "))
	}
	if len(out.Jobs) == 0 {
		b.WriteString("No jobs.\n")
	}
	fmt.Fprintf(&b, "\n%d job(s).", out.Count)
	if out.DelegationAllowance > 0 {
		fmt.Fprintf(&b, " delegation_allowance: %d.", out.DelegationAllowance)
	}
	for _, w := range out.Watches {
		fmt.Fprintf(&b, "\nwatch %s → %s (%s)", w.ID, w.Target, w.Condition)
	}
	for _, w := range out.RecentWatches {
		fmt.Fprintf(&b, "\nrecent watch %s → %s (%s, %d delivered)", w.ID, w.Target, w.EndReason, w.Deliveries)
	}
	return b.String()
}

// shortTimestamp renders an RFC3339Nano timestamp as "YYYY-MM-DD HH:MM" for
// compact display, returning "" if it cannot be parsed.
func shortTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02 15:04")
}

func jobStopTool(ctx context.Context, s *Session, args map[string]any, maxChars int) (any, error) {
	jm, err := sessionJobManager(s)
	if err != nil {
		return "", err
	}
	jobID := strings.TrimSpace(stringArg(args, "job_id"))
	if jobID == "" {
		return "", errors.New("invalid_request: job_id is required")
	}
	// max_wait_ms: 0/absent = request stop and return; positive = wait up to N;
	// negative = invalid_request. Zero reads as unset (strict-provider safe).
	maxWaitMS := 0
	if n, ok := shellIntArg(args, "max_wait_ms"); ok && n != 0 {
		if n < 0 {
			return "", errors.New("invalid_request: max_wait_ms must be non-negative")
		}
		maxWaitMS = n
	}

	targetJM := jm
	if routed, _, err := s.nestedOrLocalJobManager(jobID); err == nil {
		targetJM = routed
	}
	var childStopErr error
	if shellBoolArg(args, "include_children") {
		_, childStopErr = s.stopChildren(jobID)
	}
	// Stopping a delegate job cascades into its subtree (spec §2): resolve the
	// delegate's live child session BEFORE the stop signals (and cancels) the
	// coordinator's turn, then stop the coordinator's own running jobs (its
	// workers' delegate + shell jobs) recursively, so they do not survive
	// orphaned.
	cascadeChild := s.delegateChildSessionToCascade(jobID)
	var previousStatus jobstore.Status
	if _, pre, lookupErr := s.nestedOrLocalJobManager(jobID); lookupErr == nil && pre != nil {
		previousStatus = pre.Status
	}
	rec, err := s.stopNestedOrLocal(jobID)
	if err != nil {
		return "", errors.Join(childStopErr, err)
	}
	if cascadeChild != nil {
		if _, cascadeErr := s.stopDelegateSubtree(cascadeChild); cascadeErr != nil {
			childStopErr = errors.Join(childStopErr, cascadeErr)
		}
	}
	if maxWaitMS > 0 {
		clamped := maxWaitMS
		if clamped < minJobBlockTimeoutMS {
			clamped = minJobBlockTimeoutMS
		}
		if clamped > maxJobBlockTimeoutMS {
			clamped = maxJobBlockTimeoutMS
		}
		done := waitForJobDone(ctx, targetJM, jobID, time.Duration(clamped)*time.Millisecond)
		if _, latest, err := s.nestedOrLocalJobManager(jobID); err == nil {
			rec = latest
		}
		if !done && rec != nil && !rec.Status.IsTerminal() {
			pending := cloneJobRecord(rec)
			pending.Reason = "stop_pending"
			rec = pending
		}
	}
	if childStopErr != nil {
		return "", childStopErr
	}
	_ = maxChars
	stop := jobStopResult{
		JobID:          rec.JobID,
		Status:         string(rec.Status),
		Reason:         stringPtrOrNil(rec.Reason),
		PreviousStatus: string(previousStatus),
		Outcome:        classifyStopOutcome(previousStatus, rec),
	}
	return tool.StateResult{Output: formatJobStop(stop), State: stop}, nil
}

type jobReadOutputResult struct {
	JobID                  string            `json:"job_id"`
	Type                   string            `json:"type"`
	Status                 string            `json:"status"`
	Reason                 *string           `json:"reason"`
	Content                string            `json:"output"`
	Grep                   *string           `json:"grep,omitempty"`
	Matches                *[]jobOutputMatch `json:"matches,omitempty"`
	TotalBytes             int64             `json:"total_bytes"`
	DroppedBytes           int64             `json:"dropped_bytes,omitempty"`
	OutputStatus           string            `json:"output_status,omitempty"`
	Truncated              bool              `json:"truncated"`
	ExitCode               *int              `json:"exit_code"`
	StructuredResult       any               `json:"structured_result,omitempty"`
	StructuredResultValid  *bool             `json:"structured_result_valid,omitempty"`
	StructuredResultReason string            `json:"structured_result_reason,omitempty"`
	// LastActivity mirrors job_list's supervision signal: the most recent
	// parent-observable activity timestamp, with the same EndedAt/StartedAt
	// fallback for terminal records.
	LastActivity *string `json:"last_activity"`
}

type jobOutputMatch struct {
	ByteOffset int64  `json:"byte_offset"`
	Line       string `json:"line"`
}

type jobListResult struct {
	Jobs  []jobListEntry `json:"jobs"`
	Count int            `json:"count"`
	// Watches/RecentWatches/DelegationAllowance are supervision signal kept only
	// when they carry information: no active watches, no recent watch history, and
	// a no-op delegation allowance (≤ 1, which can only grant 0) are all omitted.
	Watches             []watchListEntry   `json:"watches,omitempty"`
	RecentWatches       []recentWatchEntry `json:"recent_watches,omitempty"`
	DelegationAllowance int                `json:"delegation_allowance,omitempty"`
}

// watchListEntry is one active watch in job_list's result (spec §4 F2),
// projected from the session's live watch configs. condition is a compact
// one-line summary of the watch's trigger; send_to is empty for a notify-caller
// watch; created_at is the watch's install time as an RFC3339Nano timestamp.
type watchListEntry struct {
	ID         string `json:"id"`
	Target     string `json:"target"`
	Condition  string `json:"condition"`
	SendTo     string `json:"send_to"`
	Deliveries int    `json:"deliveries"`
	CreatedAt  string `json:"created_at"`
}

// recentWatchEntry is one watch that has left the active set, surfaced by job_list's
// bounded recent_watches ring so a fired-then-removed watch stays legible. end_reason
// is one of auto_removed_terminal, cleared, replaced, budget_exhausted.
type recentWatchEntry struct {
	ID         string `json:"id"`
	Target     string `json:"target"`
	Condition  string `json:"condition"`
	SendTo     string `json:"send_to"`
	Deliveries int    `json:"deliveries"`
	EndReason  string `json:"end_reason"`
	EndedAt    string `json:"ended_at"`
}

type jobListEntry struct {
	JobID          string  `json:"job_id"`
	Type           string  `json:"type"`
	Status         string  `json:"status"`
	Reason         *string `json:"reason,omitempty"`
	Description    string  `json:"description"`
	ParentJobID    *string `json:"parent_job_id,omitempty"`
	OwnerSessionID string  `json:"owner_session_id"`
	// VisibleToSessionID is internal visibility routing — in a plain list it always
	// equals the owner, so it is kept for tooling but omitted from the model wire.
	VisibleToSessionID string `json:"-"`
	// TranscriptRef/Resumable/NotResumableReason are omitempty: nil for the common
	// running-shell scan (so they vanish), present where they carry signal (a
	// delegate's transcript handle, a runtime-lost delegate's resumability).
	TranscriptRef      *string `json:"transcript_ref,omitempty"`
	Resumable          *bool   `json:"resumable,omitempty"`
	NotResumableReason *string `json:"not_resumable_reason,omitempty"`
	StartedAt          string  `json:"started_at"`
	EndedAt            *string `json:"ended_at,omitempty"`
	// LastActivity is the most recent parent-observable activity timestamp
	// (output append or start) for a running job; for a terminal record with no
	// live stamp it falls back to ended_at, then started_at. A quiet running
	// delegate stays at its started_at, which is exactly what the quiet-job
	// watchdog surfaces. current_action is intentionally omitted: a running
	// delegate's mid-run "current action" is not cheaply readable from
	// parent-side state without cross-session probing.
	LastActivity *string `json:"last_activity"`
	ExitCode     *int    `json:"exit_code,omitempty"`
	// TotalBytes is the lifetime output byte count — the same name and concept the
	// shell result and job_read_output report, so the field is consistent across
	// every tool the agent reads.
	TotalBytes int64 `json:"total_bytes"`
	// Command is the shell command line for a shell job, omitted for delegates (which
	// have none) so the row stays lean. It lets the agent identify a job without
	// reading the transcript when the description is sparse.
	Command *string `json:"command,omitempty"`
	// Depth is the live-subtree distance from the calling session, populated only
	// by job_list(include_descendants=true): 0 for the caller's own store, 1 for
	// a direct child's store, and so on. It is the depth of the store the row was
	// surfaced from, so a dead descendant's terminal forwarded copy that survives
	// in an ancestor store carries that ancestor's depth. Omitted when 0 for the
	// default and include_nested listings, which do not walk the tree.
	Depth int `json:"depth,omitempty"`
}

type jobStopResult struct {
	JobID          string  `json:"job_id"`
	Status         string  `json:"status"`
	Reason         *string `json:"reason"`
	PreviousStatus string  `json:"previous_status"`
	Outcome        string  `json:"outcome"`
}

// formatJobStop renders a job_stop result as a single plain-text line matching the
// job-family footer style: [job <id> · <status> · <outcome> · <reason>].
func formatJobStop(out jobStopResult) string {
	parts := []string{"job " + out.JobID, out.Status, out.Outcome}
	if out.Reason != nil && *out.Reason != "" {
		parts = append(parts, *out.Reason)
	}
	return "[" + strings.Join(parts, " · ") + "]"
}

// classifyStopOutcome distinguishes a stop that cancelled a live job from one that
// raced with, or arrived after, the job's own completion. previous is the job's
// status read before the stop signal; rec is the record after it.
func classifyStopOutcome(previous jobstore.Status, rec *jobstore.JobRecord) string {
	if previous.IsTerminal() {
		return "already_terminal"
	}
	if rec == nil || !rec.Status.IsTerminal() {
		return "stop_requested" // still finalizing (e.g. reason "stop_pending")
	}
	if rec.Status == jobstore.StatusCancelled {
		return "cancelled_by_request"
	}
	return "completed_during_stop"
}

type jobSendMessageDelegateResult struct {
	Target                 string  `json:"target"`
	JobID                  string  `json:"job_id"`
	Type                   string  `json:"type"`
	Status                 string  `json:"status"`
	Reason                 *string `json:"reason,omitempty"`
	RunningInBackground    bool    `json:"running_in_background"`
	TimedOut               bool    `json:"timed_out,omitempty"`
	Action                 string  `json:"action"`
	ResumedFromJobID       string  `json:"resumed_from_job_id,omitempty"`
	TranscriptRef          string  `json:"transcript_ref"`
	Output                 *string `json:"output,omitempty"`
	Truncated              *bool   `json:"truncated,omitempty"`
	StructuredResult       any     `json:"structured_result,omitempty"`
	StructuredResultValid  *bool   `json:"structured_result_valid,omitempty"`
	StructuredResultReason string  `json:"structured_result_reason,omitempty"`
	WaitIgnoredReason      string  `json:"wait_ignored_reason,omitempty"`
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
	// replaced_existing and fired serialize explicitly even when false: the
	// contract's install example shows replaced_existing:false, and §7.1
	// promises "fired=false on none" for terminal catch-up.
	ReplacedExisting bool   `json:"replaced_existing"`
	Fired            bool   `json:"fired"`
	TerminalCatchup  bool   `json:"terminal_catchup,omitempty"`
	Status           string `json:"status,omitempty"`
}

type jobWatchToolSendArgs struct {
	To             string `json:"to"`
	Message        string `json:"message,omitempty"`
	IncludeExcerpt bool   `json:"include_excerpt,omitempty"`
}

type delegateToolResult struct {
	JobID                  string  `json:"job_id"`
	Type                   string  `json:"type"`
	Status                 string  `json:"status"`
	Reason                 *string `json:"reason,omitempty"`
	RunningInBackground    bool    `json:"running_in_background"`
	TimedOut               bool    `json:"timed_out"`
	TranscriptRef          string  `json:"transcript_ref"`
	Output                 *string `json:"output,omitempty"`
	Truncated              *bool   `json:"truncated,omitempty"`
	StructuredResult       any     `json:"structured_result,omitempty"`
	StructuredResultValid  *bool   `json:"structured_result_valid,omitempty"`
	StructuredResultReason string  `json:"structured_result_reason,omitempty"`
}

func marshalSendMessageResult(res sendMessageResult, maxChars int) (any, error) {
	_ = maxChars
	if res.MessageType == "runtime" {
		alias := jobSendMessageAliasResult{
			Target:      res.Target,
			Delivered:   res.Delivered,
			Action:      res.Action,
			MessageType: res.MessageType,
		}
		return tool.StateResult{Output: formatSendMessageAlias(alias), State: alias}, nil
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
	return tool.StateResult{Output: formatSendMessageDelegate(out), State: out}, nil
}

// formatSendMessageAlias renders a runtime-alias send result as a one-line footer.
func formatSendMessageAlias(a jobSendMessageAliasResult) string {
	delivered := "delivered"
	if !a.Delivered {
		delivered = "not delivered"
	}
	return fmt.Sprintf("[message to %s · %s · %s]", a.Target, a.Action, delivered)
}

// formatSendMessageDelegate renders a delegate send/steer/resume result: any reply
// output, a bracketed footer, and the structured_result (JSON, genuinely
// structured) when present.
func formatSendMessageDelegate(out jobSendMessageDelegateResult) string {
	var b strings.Builder
	if out.Output != nil && *out.Output != "" {
		b.WriteString(*out.Output)
		if !strings.HasSuffix(*out.Output, "\n") {
			b.WriteByte('\n')
		}
	}
	foot := []string{"message to " + out.Target, out.Action}
	if out.JobID != "" {
		foot = append(foot, "job "+out.JobID)
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
	b.WriteString("[" + strings.Join(foot, " · ") + "]")
	if out.StructuredResult != nil {
		if sr, err := json.Marshal(out.StructuredResult); err == nil {
			valid := out.StructuredResultValid != nil && *out.StructuredResultValid
			fmt.Fprintf(&b, "\nstructured_result (valid=%v): %s", valid, sr)
		}
	}
	return b.String()
}

func marshalWatchResult(res watchResult, maxChars int) (any, error) {
	_ = maxChars
	out := jobWatchToolResult{
		Target:             res.Target,
		Watching:           res.Watching,
		OutputMatch:        res.OutputMatch,
		Events:             res.Events,
		ProgressIntervalMS: res.ProgressIntervalMS,
		ReplacedExisting:   res.ReplacedExisting,
		Fired:              res.Fired,
		TerminalCatchup:    res.TerminalCatchup,
		Status:             res.Status,
	}
	if res.Send != nil {
		out.Send = &jobWatchToolSendArgs{
			To:             res.Send.To,
			Message:        res.Send.Message,
			IncludeExcerpt: res.Send.IncludeExcerpt,
		}
	}
	return tool.StateResult{Output: formatJobWatch(out), State: out}, nil
}

// formatJobWatch renders a job_watch result as a one-line footer summarizing the
// watch's target, trigger condition, and disposition.
func formatJobWatch(out jobWatchToolResult) string {
	if !out.Watching {
		return fmt.Sprintf("[watch on %s cleared]", out.Target)
	}
	parts := []string{"watching " + out.Target}
	var cond []string
	if out.OutputMatch != "" {
		cond = append(cond, "output_match: "+out.OutputMatch)
	}
	if len(out.Events) > 0 {
		cond = append(cond, "events: "+strings.Join(out.Events, ","))
	}
	if out.ProgressIntervalMS > 0 {
		cond = append(cond, fmt.Sprintf("every %dms", out.ProgressIntervalMS))
	}
	if len(cond) > 0 {
		parts = append(parts, strings.Join(cond, " "))
	}
	if out.Send != nil && out.Send.To != "" {
		parts = append(parts, "→ "+out.Send.To)
	}
	if out.ReplacedExisting {
		parts = append(parts, "replaced existing")
	}
	if out.Fired {
		parts = append(parts, "fired")
	}
	if out.TerminalCatchup {
		parts = append(parts, "terminal catch-up")
	}
	if out.Status != "" {
		parts = append(parts, out.Status)
	}
	return "[" + strings.Join(parts, " · ") + "]"
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
		out.StructuredResultReason = res.StructuredResultReason
	}
	return marshalBoundedDelegateResult(out, maxChars)
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
	if out.StructuredResult != nil {
		out.StructuredResult = nil
		invalid := false
		out.StructuredResultValid = &invalid
		out.StructuredResultReason = structuredResultReasonProjectionTooLarge
	}
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

// strictZeroJobBytesArg reads a head_bytes/tail_bytes arg under the strict-zero
// rule: absent or 0 → (0, false, nil); positive → (capped, true, nil);
// negative → (0, false, invalid_request error). Mirrors max_wait_ms behavior.
func strictZeroJobBytesArg(args map[string]any, key string) (int, bool, error) {
	n, ok := shellIntArg(args, key)
	if !ok || n == 0 {
		return 0, false, nil
	}
	if n < 0 {
		return 0, false, fmt.Errorf("invalid_request: %s must be non-negative", key)
	}
	if n > maxJobOutputBytes {
		n = maxJobOutputBytes
	}
	return n, true, nil
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
		Statuses:           statuses,
		Types:              types,
		Limit:              limit,
		IncludeNested:      shellBoolArg(args, "include_nested"),
		IncludeDescendants: shellBoolArg(args, "include_descendants"),
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
	if n, ok := shellIntArg(args, "every"); ok {
		a.Every = n
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

func watchSendArg(args map[string]any) (*watchSendArgs, error) {
	raw, ok := args["send"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("send must be an object")
	}
	if isEmptyWatchSend(values) {
		return nil, nil
	}
	to := strings.TrimSpace(stringArg(values, "to"))
	if to == "" {
		return nil, errors.New("invalid_request: send.to is required")
	}
	return &watchSendArgs{
		To:             to,
		Message:        stringArg(values, "message"),
		IncludeExcerpt: shellBoolArg(values, "include_excerpt"),
	}, nil
}

func isEmptyWatchSend(values map[string]any) bool {
	return strings.TrimSpace(stringArg(values, "to")) == "" &&
		stringArg(values, "message") == "" &&
		!shellBoolArg(values, "include_excerpt")
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

func findJobRecord(jm *jobManager, jobID string) (*jobstore.JobRecord, error) {
	recs, err := jm.listWithError(listFilter{IncludeNested: true})
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		if rec.JobID == jobID {
			return rec, nil
		}
	}
	return nil, errJobNotFound(jobID)
}

func waitForJobDone(ctx context.Context, jm *jobManager, jobID string, timeout time.Duration) bool {
	done, ok := jobDone(jm, jobID)
	if !ok {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
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

// waitForJobGrepMatch blocks until the job's retained output contains a line
// matching re, the job goes terminal, or timeout elapses. The retained output
// is checked before the first wait so an existing match returns immediately;
// afterwards each output-size change is re-evaluated incrementally from the
// last scanned line boundary instead of re-grepping the full retained buffer.
// The final snapshot re-greps for the result's matches, so correctness never
// depends on this wait's incremental state.
func waitForJobGrepMatch(ctx context.Context, jm *jobManager, jobID string, re *regexp.Regexp, timeout time.Duration) {
	maxLineBytes := maxJobGrepLineBytes
	var scan jobGrepScan
	if scan.step(jm, jobID, re, maxLineBytes) {
		return
	}
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
			if scan.step(jm, jobID, re, maxLineBytes) {
				return
			}
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// jobGrepScan tracks incremental grep progress over a job's lifetime output.
// scanned always sits at a line boundary (or inside a dead line), so a token
// split across appends is re-evaluated once its line grows; lastTotal gates
// re-scans to output-size changes.
type jobGrepScan struct {
	scanned    int64 // lifetime offset where the next scan starts
	lastTotal  int64 // lifetime total at the end of the previous scan
	inDeadLine bool  // scanned sits inside a line already too long to ever match
}

// step reports whether the job's retained output gained a grep match since the
// previous step. It reads only bytes at or beyond the last scanned line
// boundary; transient read errors leave the scan state unchanged for the next
// poll.
func (g *jobGrepScan) step(jm *jobManager, jobID string, re *regexp.Regexp, maxLineBytes int) bool {
	total, err := jobOutputBytes(jm, jobID)
	if err != nil || total <= g.lastTotal {
		return false
	}
	content, start, ok := readJobOutputFrom(jm, jobID, g.scanned, total)
	if !ok {
		return false
	}
	seg := []byte(content)
	if start < g.scanned {
		seg = seg[g.scanned-start:]
		start = g.scanned
	}
	// start can exceed scanned when retention pruned bytes below it; scan the
	// retained remainder from where it begins.
	g.scanned = start
	g.lastTotal = start + int64(len(seg))
	return g.scanSegment(seg, re, maxLineBytes)
}

// scanSegment consumes seg, which begins at lifetime offset g.scanned, with
// the snapshot grep's line semantics: a complete line matches without its
// trailing newline (and a carriage return before it), lines whose content
// exceeds maxLineBytes never match, and the trailing unterminated line is
// matched as-is, like the snapshot grep does at end of output. g.scanned is
// left at the start of a trailing incomplete line so the next step re-reads it
// once it grows (partial-line carry); a fragment already too long to ever
// match is instead skipped as it streams.
func (g *jobGrepScan) scanSegment(seg []byte, re *regexp.Regexp, maxLineBytes int) bool {
	for len(seg) > 0 {
		nl := bytes.IndexByte(seg, '\n')
		if g.inDeadLine {
			if nl < 0 {
				g.scanned += int64(len(seg))
				return false
			}
			g.inDeadLine = false
			g.scanned += int64(nl + 1)
			seg = seg[nl+1:]
			continue
		}
		if nl < 0 {
			break
		}
		line := seg[:nl]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		matched := len(line) <= maxLineBytes && re.Match(line)
		g.scanned += int64(nl + 1)
		seg = seg[nl+1:]
		if matched {
			return true
		}
	}
	if len(seg) == 0 {
		return false
	}
	if len(seg) > maxLineBytes+1 {
		// Even completed by a bare "\r\n" this line's content exceeds
		// maxLineBytes, so it can never match; skip its remainder as it
		// streams instead of re-carrying it every poll.
		g.inDeadLine = true
		g.scanned += int64(len(seg))
		return false
	}
	return len(seg) <= maxLineBytes && re.Match(seg)
}

// readJobOutputFrom reads the job's retained output from lifetime offset from
// (or from the start of retention if those bytes were pruned) through the
// current end, returning the lifetime offset of the first returned byte.
// readOutput sizes the request and reads under separate lock acquisitions, so
// concurrent appends can move the tail past the requested window; widen and
// retry a bounded number of times; legitimate short windows (retention floor)
// return ok, while a window still racing after the retry budget reports not-ok
// so the caller retries.
//
// Three conditions legitimately allow returning a window with start > from
// (bytes below start are genuinely unavailable, not just a race):
//   - start <= from: we received all wanted bytes — no gap.
//   - req < want: request was clamped at maxJobOutputRetentionBytes, so bytes
//     below start are beyond the retention cap and truly gone.
//   - len(c) < req: the file is shorter than requested; start is the true
//     retention floor and everything available was returned.
//
// When tries are exhausted but none of those conditions hold, the output was
// still growing during our retry window and [from, start) bytes are still
// retained — returning the short window would make the caller treat those
// bytes as pruned and skip them permanently. Return not-ok instead so the
// caller leaves its scan state unchanged and retries on the next tick.
func readJobOutputFrom(jm *jobManager, jobID string, from, total int64) (content string, start int64, ok bool) {
	want := total - from
	for tries := 0; ; tries++ {
		req := want
		if req > maxJobOutputRetentionBytes {
			req = maxJobOutputRetentionBytes
		}
		c, totalNow, _, err := jm.readOutput(jobID, int(req))
		if err != nil {
			return "", 0, false
		}
		start = totalNow - int64(len(c))
		if start <= from || req < want || int64(len(c)) < req {
			return c, start, true
		}
		if tries >= 3 {
			// The output kept growing past our window for the full retry budget
			// and [from, start) is still retained (not pruned). Returning this
			// short window would make the caller treat those still-retained bytes
			// as gone and skip them forever; report not-ok so the caller retries
			// on the next tick.
			return "", 0, false
		}
		want = totalNow - from
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

func projectJobRecord(s *Session, rec *jobstore.JobRecord) jobListEntry {
	resumable := rec.Resumable
	notResumableReason := stringPtrOrNil(rec.NotResumableWhy)
	if isRuntimeLostDelegate(rec) {
		assessment := s.assessDelegateResumability(rec, delegateResumabilityProjection)
		resumableValue := assessment.Resumable
		resumable = &resumableValue
		if assessment.Resumable {
			notResumableReason = nil
		} else {
			notResumableReason = stringPtrOrNil(assessment.Reason)
		}
	}
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
		Resumable:          resumable,
		NotResumableReason: notResumableReason,
		StartedAt:          rec.StartedAt.Format(time.RFC3339Nano),
		EndedAt:            timePtrOrNil(rec.EndedAt),
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

// lastActivityProjection renders a record's last_activity timestamp. A running
// job carries a live LastActivity stamp. A terminal record reloaded from the
// store has no stamp (LastActivity is in-memory only, never folded), so it
// falls back to the most recent activity it can attest: EndedAt, then
// StartedAt.
func lastActivityProjection(rec *jobstore.JobRecord) *string {
	if rec.LastActivity != nil {
		return timePtrOrNil(rec.LastActivity)
	}
	if rec.EndedAt != nil {
		return timePtrOrNil(rec.EndedAt)
	}
	started := rec.StartedAt
	return timePtrOrNil(&started)
}
