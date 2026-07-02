package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

const (
	// defaultListDirLimit caps how many entries a list_dir call returns when the
	// caller gives no limit. The char budget below usually binds first on a large
	// directory; this is the ceiling for a directory of very short names.
	defaultListDirLimit = 1000
	// listDirCharBudget bounds the rendered listing so it stays well under list_dir's
	// tool-output char cap (registry defaultToolLimit, 20k) and never trips the
	// generic middle-truncator. list_dir is ls: truncation drops whole trailing
	// records, in record units, with an accurate count — not characters from the
	// middle.
	listDirCharBudget = 16_000
)

// listDirResult is a bounded page of directory entries plus the totals an agent
// needs to decide whether to page further or narrow the path/depth. It is
// rendered to plain text by formatDirListing.
type listDirResult struct {
	Path      string
	Entries   []execenv.DirEntry
	Total     int
	Returned  int
	Offset    int
	Truncated bool
}

// dirEntrySize over-estimates an entry's rendered line length (name, an optional
// slash or tab-separated size, and a newline) so the running budget keeps the
// rendered listing under the cap.
func dirEntrySize(e execenv.DirEntry) int {
	return len(e.Name) + 16
}

// formatDirListing renders a page as plain text, ls-style: one entry per line,
// directories suffixed with "/", files shown as "name\tsize", followed by a count
// footer that says how to page when the listing is truncated.
func formatDirListing(r listDirResult) string {
	var b strings.Builder
	for i, e := range r.Entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Name)
		switch {
		case e.IsDir:
			b.WriteByte('/')
		case e.IsSymlink:
			b.WriteByte('@')
		case e.IsExec:
			b.WriteByte('*')
		}
		if !e.IsDir {
			b.WriteByte('\t')
			b.WriteString(strconv.FormatInt(e.Size, 10))
		}
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	switch {
	case r.Truncated:
		fmt.Fprintf(&b, "%d of %d entries (offset %d) — more with list_dir(offset=%d)", r.Returned, r.Total, r.Offset, r.Offset+r.Returned)
	case r.Offset > 0:
		fmt.Fprintf(&b, "%d of %d entries (offset %d)", r.Returned, r.Total, r.Offset)
	default:
		fmt.Fprintf(&b, "%d entries", r.Total)
	}
	return b.String()
}

// paginateDirEntries returns the entries starting at offset as a page bounded by
// both limit (entry count; <= 0 applies defaultListDirLimit, matching the
// strict-schema unset-as-zero convention) and listDirCharBudget (marshalled
// size), whichever binds first, and reports the totals. At least one entry is
// always returned when any remain past offset. The page is a non-nil slice so it
// serializes as [] rather than null when empty.
func paginateDirEntries(path string, entries []execenv.DirEntry, offset, limit int) listDirResult {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultListDirLimit
	}
	total := len(entries)
	start := offset
	if start > total {
		start = total
	}
	page := make([]execenv.DirEntry, 0, limit)
	used := 0
	end := start
	for end < total && len(page) < limit {
		sz := dirEntrySize(entries[end])
		if len(page) > 0 && used+sz > listDirCharBudget {
			break
		}
		page = append(page, entries[end])
		used += sz
		end++
	}
	return listDirResult{
		Path:      path,
		Entries:   page,
		Total:     total,
		Returned:  len(page),
		Offset:    offset,
		Truncated: end < total,
	}
}

func registerShellTools(reg *tool.Registry, s *Session, deps *toolDeps) error {
	// shell
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefShell()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			shellArgs, err := parseShellToolArgs(args)
			if err != nil {
				return "", err
			}
			if se, ok := env.(execenv.StreamingExecutor); ok {
				if s == nil || s.jobManager == nil {
					return "", errors.New("shell jobs require an initialized JobManager")
				}
				shellArgs = applyShellTimeoutPolicy(deps, shellArgs)
				return marshalShellToolResult(runShell(ctx, s.jobManager, se, shellArgs), shellToolResultMaxChars(reg))
			}
			return runBufferedShell(ctx, env, deps, shellArgs)
		},
	}); err != nil {
		return err
	}

	// list_dir (Gemini-aligned)
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefListDir(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := stringArg(args, "path")
			depth := 1
			if v, ok := args["depth"].(float64); ok && int(v) > 0 {
				depth = int(v)
			}
			offset := 0
			if v, ok := args["offset"].(float64); ok && int(v) > 0 {
				offset = int(v)
			}
			limit := 0
			if v, ok := args["limit"].(float64); ok && int(v) > 0 {
				limit = int(v)
			}
			entries, err := env.ListDirectory(path, depth)
			if err != nil {
				return nil, err
			}
			return formatDirListing(paginateDirEntries(path, entries, offset, limit)), nil
		},
	})

	// grep
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefGrep(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := stringArg(args, "pattern")
			path := stringArg(args, "path")
			glob := stringArg(args, "glob_filter")
			ci := false
			if v, ok := args["case_insensitive"].(bool); ok {
				ci = v
			}
			maxRes := 100
			if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
				maxRes = int(v)
			}
			outputMode := ""
			if v, ok := args["output_mode"].(string); ok {
				outputMode = v
			}
			return env.Grep(pat, path, glob, ci, maxRes, outputMode)
		},
	}); err != nil {
		return err
	}

	// glob
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefGlob(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := stringArg(args, "pattern")
			path := stringArg(args, "path")
			matches, err := env.Glob(pat, path)
			if err != nil {
				return "", err
			}
			return strings.Join(matches, "\n"), nil
		},
	}); err != nil {
		return err
	}

	// apply_patch (OpenAI-specific; best-effort implementation lives in this repo)
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefApplyPatch()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := fmt.Sprint(args["patch"])
			return tool.ApplyPatch(env.WorkingDirectory(), patch)
		},
	})

	return nil
}

func parseShellToolArgs(args map[string]any) (shellArgs, error) {
	parsed := shellArgs{
		Command:     fmt.Sprint(args["command"]),
		Description: stringArg(args, "description"),
		// background false (the strict-provider-forced default) runs foreground and
		// returns when the command finishes; true starts it and returns the job_id now.
		Background: shellBoolArg(args, "background"),
	}
	var ok bool
	if parsed.MaxRuntimeMS, ok = shellIntArg(args, "max_runtime_ms"); !ok {
		parsed.MaxRuntimeMS = 0
	}
	if parsed.MaxRuntimeMS < 0 {
		return shellArgs{}, errors.New("max_runtime_ms must be non-negative")
	}
	if parsed.MaxRuntimeMS > 0 && parsed.MaxRuntimeMS < minShellMaxRuntimeMS {
		parsed.MaxRuntimeMS = minShellMaxRuntimeMS
	}
	return parsed, nil
}

func shellBoolArg(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func shellIntArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

const (
	shellToolResultDefaultMaxChars = 30_000
	shellToolResultMinJSONChars    = 800
	minShellMaxRuntimeMS           = 1000
)

func enforceShellToolJSONLimit(reg *tool.Registry) {
	if reg == nil {
		return
	}
	registered := reg.Get("shell")
	if registered == nil || registered.Limit.MaxChars >= shellToolResultMinJSONChars {
		return
	}
	reg.OverrideLimits(map[string]schema.ToolOutputLimit{
		"shell": {MaxChars: shellToolResultMinJSONChars},
	})
}

func shellToolResultMaxChars(reg *tool.Registry) int {
	if reg == nil {
		return shellToolResultDefaultMaxChars
	}
	registered := reg.Get("shell")
	if registered == nil || registered.Limit.MaxChars <= 0 {
		return shellToolResultDefaultMaxChars
	}
	if registered.Limit.MaxChars < shellToolResultMinJSONChars {
		return shellToolResultMinJSONChars
	}
	return registered.Limit.MaxChars
}

func marshalShellToolResult(res shellResult, maxChars int) (tool.StateResult, error) {
	// complete-or-handle (spec §0.6): within-bound results carry a settle
	// closure. Apply both layers — ride-whole budget (shellRideWholeBytes)
	// and tool-result char bound (maxChars) — to decide keep vs discard.
	if res.settle != nil {
		return marshalCompleteOrHandleResult(res, maxChars)
	}

	out := shellToolResult{
		JobID:               res.JobID,
		Type:                res.Type,
		Status:              res.Status,
		Reason:              shellStringPtrOrNil(res.Reason),
		RunningInBackground: res.RunningInBackground,
		TimedOut:            res.TimedOut,
		ExitCode:            res.ExitCode,
	}
	if !res.RunningInBackground || res.Output != "" {
		out.Output = &res.Output
		out.Truncated = &res.Truncated
		out.TotalBytes = res.TotalBytes
		out.DroppedBytes = res.DroppedBytes
		out.OutputStatus = outputWindowStatus(res.TotalBytes, res.DroppedBytes, res.Truncated)
	}
	return tool.StateResult{Output: formatShellResult(out), State: out}, nil
}

// marshalCompleteOrHandleResult implements spec §0.6 for within-bound shell
// completions. It checks both layers:
//
//  1. Ride-whole budget (shellRideWholeBytes): output > 8KB cannot ride whole
//     in the inline result.
//  2. Tool-result char bound (maxChars): marshaled JSON > bound also exceeds.
//
// If either layer triggers → settle(true) keeps the delayed job (durable),
// returns a truncated tail + job_id. Otherwise → settle(false) discards the
// delayed job (ephemeral), returns complete output inline with no job_id.
func marshalCompleteOrHandleResult(res shellResult, maxChars int) (tool.StateResult, error) {
	falseVal := false
	out := shellToolResult{
		Type:         res.Type,
		Status:       res.Status,
		Reason:       shellStringPtrOrNil(res.Reason),
		TimedOut:     res.TimedOut,
		ExitCode:     res.ExitCode,
		Output:       &res.Output,
		Truncated:    &falseVal,
		TotalBytes:   res.TotalBytes,
		DroppedBytes: res.DroppedBytes,
	}

	out.OutputStatus = outputWindowStatus(res.TotalBytes, res.DroppedBytes, false)

	// Two layers decide keep vs ephemeral (spec §6.4c): the ride-whole budget
	// (output too big to inline) and the tool-result char bound (the rendered
	// result would overflow maxChars). Either → keep a durable job so the model
	// can page; neither → return the complete output inline and discard the job.
	disp := shellResultDisposition(len(res.Output), len([]rune(formatShellResult(out))), maxChars, shellRideWholeBytes)
	if !disp.Keep {
		_ = res.settle(false)
		return tool.StateResult{Output: formatShellResult(out), State: out}, nil
	}

	// Keep: output cannot ride whole inline. Commit + finalize the delayed job.
	jobID := res.settle(true)
	out.JobID = jobID
	out.OutputStatus = outputWindowStatus(res.TotalBytes, res.DroppedBytes, true)

	// The model sees only a small head+tail digest + the job_id; it reads the rest
	// through the job transcript_ref. The full output is retained in the durable job.
	digest := shellInlineDigest(res.Output, res.TotalBytes, res.DroppedBytes)
	peekTruncated := true
	out.Output = &digest
	out.Truncated = &peekTruncated
	return tool.StateResult{Output: formatShellResult(out), State: out}, nil
}

// shellSettleDisposition is the keep-vs-discard decision for a within-bound
// shell completion (spec §6.4c). Keep reports whether the delayed job must be
// retained; EmbedExceeded and CharBoundExceeded attribute why. The two reasons
// are mutually exclusive: EmbedExceeded short-circuits the char-bound check.
type shellSettleDisposition struct{ Keep, EmbedExceeded, CharBoundExceeded bool }

// shellResultDisposition decides whether a within-bound shell completion must
// keep its delayed job. rawOutputBytes is the byte length of the full output;
// renderedRuneLen is the rune length of the rendered tool result. Output larger
// than rideWholeBytes cannot ride whole inline (EmbedExceeded); otherwise a
// rendered result over a positive maxChars exceeds the char bound
// (CharBoundExceeded). Either reason keeps the job.
func shellResultDisposition(rawOutputBytes, renderedRuneLen, maxChars, rideWholeBytes int) shellSettleDisposition {
	embedExceeded := rawOutputBytes > rideWholeBytes
	charBoundExceeded := !embedExceeded && maxChars > 0 && renderedRuneLen > maxChars
	return shellSettleDisposition{
		Keep:              embedExceeded || charBoundExceeded,
		EmbedExceeded:     embedExceeded,
		CharBoundExceeded: charBoundExceeded,
	}
}

// outputWindowStatus classifies an output window for the model: "all_retained"
// when the returned window is the whole retained log, "windowed" when the full
// log is retained but only a slice was returned (read the rest via the job
// transcript_ref), and "evicted" when the oldest bytes were permanently dropped
// past the retention cap. It describes the WINDOW, not the job lifecycle — a
// running job whose window covers everything-so-far still reports "all_retained".
func outputWindowStatus(total, dropped int64, truncated bool) string {
	if dropped > 0 {
		return "evicted"
	}
	if truncated {
		return "windowed"
	}
	return "all_retained"
}

func jsonCharLen(b []byte) int {
	return len([]rune(string(b)))
}

// formatShellResult renders a shell result as plain text for the model: the
// command output (a digest when windowed), then a bracketed footer carrying the
// structured signals — exit code, timeout/background status, the job_id to page a
// windowed or backgrounded job, and any retention-cap loss. The full structured
// result rides alongside as StateResult.State for the hub.
func formatShellResult(out shellToolResult) string {
	var b strings.Builder
	if out.Output != nil && *out.Output != "" {
		b.WriteString(*out.Output)
		if !strings.HasSuffix(*out.Output, "\n") {
			b.WriteByte('\n')
		}
	}
	var foot []string
	if out.ExitCode != nil && !out.RunningInBackground {
		foot = append(foot, fmt.Sprintf("exit %d", *out.ExitCode))
	}
	if out.TimedOut {
		foot = append(foot, "timed out")
	}
	switch {
	case out.RunningInBackground && out.JobID != "":
		foot = append(foot, "running in background as "+out.JobID)
	case out.JobID != "":
		foot = append(foot, fmt.Sprintf("output windowed — read more with read_transcript(transcript_ref=%q)", "job:"+out.JobID))
	}
	if out.DroppedBytes > 0 {
		foot = append(foot, fmt.Sprintf("%d bytes dropped past the retention cap", out.DroppedBytes))
	}
	if len(foot) > 0 {
		b.WriteString("[" + strings.Join(foot, " · ") + "]")
	}
	return strings.TrimRight(b.String(), "\n")
}

type shellToolResult struct {
	JobID               string  `json:"job_id,omitempty"`
	Type                string  `json:"type"`
	Status              string  `json:"status"`
	Reason              *string `json:"reason"`
	RunningInBackground bool    `json:"running_in_background"`
	TimedOut            bool    `json:"timed_out"`
	ExitCode            *int    `json:"exit_code,omitempty"`
	Output              *string `json:"output,omitempty"`
	Truncated           *bool   `json:"truncated,omitempty"`
	TotalBytes          int64   `json:"total_bytes,omitempty"`
	DroppedBytes        int64   `json:"dropped_bytes,omitempty"`
	OutputStatus        string  `json:"output_status,omitempty"`
}

func shellStringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func applyShellTimeoutPolicy(deps *toolDeps, args shellArgs) shellArgs {
	if deps == nil || deps.cmdTimeouts == nil {
		return args
	}
	defTimeout, maxTimeout := deps.cmdTimeouts()
	timeout := defTimeout
	if args.BlockTimeoutMS > 0 {
		timeout = args.BlockTimeoutMS
	}
	if maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
	}
	args.BlockTimeoutMS = timeout
	return args
}

func runBufferedShell(ctx context.Context, env execenv.ExecutionEnvironment, deps *toolDeps, args shellArgs) (string, error) {
	// A non-streaming environment has no job manager, so it cannot background a
	// command; making that explicit beats silently running it in the foreground.
	if args.Background {
		return "", errors.New("invalid_request: background requires a streaming execution environment")
	}
	args = applyShellTimeoutPolicy(deps, args)
	timeout := args.BlockTimeoutMS
	timeoutParam := "max_runtime_ms"
	if args.MaxRuntimeMS > 0 && (timeout == 0 || args.MaxRuntimeMS < timeout) {
		timeout = args.MaxRuntimeMS
	}
	res, err := env.ExecCommand(ctx, args.Command, timeout, "", nil)

	// Return a line-oriented tool output so line truncation works as intended for shell output.
	var b strings.Builder
	if strings.TrimSpace(res.Stdout) != "" {
		b.WriteString(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(res.Stderr) != "" {
		b.WriteString(res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteString("\n")
		}
	}
	if errors.Is(err, context.Canceled) && !res.TimedOut {
		b.WriteString("[ERROR: Command was canceled before completion. Partial output is shown above.]\n")
	} else if res.TimedOut {
		b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the %s parameter.]\n", timeout, timeoutParam))
	}
	b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
	return b.String(), err
}
