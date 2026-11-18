// Package envctx renders per-turn environment context as an append-only
// diff: only facts that changed since the last emission are rendered, so
// the injected message stays cache-safe (appended, never edited) and
// near-zero tokens on a quiet environment. See
// docs/superpowers/specs/2026-08-06-environment-context-design.md.
package envctx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Pressure holds human-readable resource-pressure warnings; "" means the
// resource is nominal. A non-empty→empty transition renders a one-time
// "back to normal" line so the model never believes stale pressure.
type Pressure struct {
	Load   string `json:"load,omitempty"`
	Memory string `json:"memory,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

// Snapshot is one observation of the session environment. Its fields are
// comparable for the nothing-changed fast path and marshal into session meta.
type Snapshot struct {
	Cwd               string   `json:"cwd,omitempty"`             // absolute working directory
	LocalDateHour     string   `json:"local_date_hour,omitempty"` // "2026-08-06 14:00 PDT"
	Sandbox           string   `json:"sandbox,omitempty"`         // always populated; "off" included
	ScratchDir        string   `json:"scratch_dir,omitempty"`
	WritableRoots     string   `json:"writable_roots,omitempty"`
	FileToolWrites    string   `json:"file_tool_writes,omitempty"`
	ShellNetwork      string   `json:"shell_network,omitempty"`
	DetachedShell     string   `json:"detached_shell,omitempty"`
	GitBranch         string   `json:"git_branch,omitempty"` // "" outside a git repo
	gitBranchUnknown  bool     // Applies to an observation, never to the retained state.
	recordGitBranch   bool     // The last durable value can differ from the model's retained baseline after a fold.
	Pressure          Pressure `json:"pressure,omitzero"`
	SessionID         string   `json:"session_id,omitempty"`
	ParentSessionID   string   `json:"parent_session_id,omitempty"`
	OrdinarySession   bool     `json:"ordinary_session,omitempty"`
	ReportingAudience string   `json:"reporting_audience,omitempty"`
	ResultToolName    string   `json:"result_tool_name,omitempty"`
	Model             string   `json:"model,omitempty"`
	KnowledgeCutoff   string   `json:"knowledge_cutoff,omitempty"`
	ExecutionMode     string   `json:"execution_mode,omitempty"` // set when a later observation supplies the job lifetime
	AskUserState      string   `json:"ask_user_state,omitempty"`
	ResultEndReason   string   `json:"result_end_reason,omitempty"`
	CommitTrailer     string   `json:"commit_trailer,omitempty"`
	OmitCommitTrailer bool     `json:"omit_commit_trailer,omitempty"` // explicit cancellation of a prior directive
}

// State is the Tracker's persistable form, stored in session meta so resume
// stays silent when nothing changed across a restart.
type State struct {
	Last    Snapshot `json:"last"`
	HasSent bool     `json:"has_sent"`
	// Unrecorded launch facts may still be named in the saved prompt.
	// The next observation, including an empty one, must supersede them.
	GitBranchUnknown     bool `json:"git_branch_unknown,omitempty"`
	ScratchDirUnknown    bool `json:"scratch_dir_unknown,omitempty"`
	WritableRootsUnknown bool `json:"writable_roots_unknown,omitempty"`
}

// Tracker diffs successive Snapshots into rendered context blocks.
type Tracker struct {
	st State
}

const ordinaryRole = "session role: ordinary"
const parentSessionPrefix = "parent session id: "
const reportingAudiencePrefix = "reporting audience: "
const resultToolPrefix = "result tool: "
const ReportToCaller = "caller"
const ReportToParent = "parent agent"
const scratchDirPrefix = "writable scratch directory: "
const writableRootsPrefix = "writable roots: "
const hostWritableRoots = writableRootsPrefix + "host filesystem permissions govern spawned commands"
const fileToolWritesPrefix = "file-tool writes: "
const FileWritesHost = "host filesystem permissions"
const FileWritesScratch = "session scratch only"
const FileWritesSandbox = "sandbox-granted paths and session scratch"
const shellNetworkPrefix = "shell network policy: "
const NetworkAllowed = "allowed"
const NetworkBlocked = "blocked"
const detachedShellPrefix = "detached shell commands: "
const DetachAvailable = "available"
const DetachUnavailable = "unavailable"
const executionModePrefix = "execution mode: "
const OneShotMode = "one-shot"
const DaemonMode = "daemon"
const askUserPrefix = "ask_user: "
const QuestionsAvailable = "available"
const QuestionsUnavailable = "unavailable"
const resultEndReasonPrefix = "result tool end_reason: "
const OmitEndReason = "omit"
const EndReasonAvailable = "available"
const commitTrailerPrefix = "For new commits in this session, use this trailer: "
const omitCommitTrailer = "Omit the Co-authored-by trailer on new commits in this session."
const unconfirmedBranch = "(unconfirmed for this checkout)"
const outsideWorktree = "(not in a git repository)"

func NewTracker(st State) *Tracker { return &Tracker{st: st} }

// State returns the persistable tracker state.
func (t *Tracker) State() State { return t.st }

// ReplayBlock folds one durable environment block into the tracker state.
// Blocks are diffs, so callers replay them in transcript order starting from
// the effective history boundary. This closes the write-before-metadata crash window
// without treating the rendered model text as a new observation.
func (t *Tracker) ReplayBlock(block string) bool {
	if t == nil {
		return false
	}
	body, ok := strings.CutPrefix(strings.TrimSpace(block), "<environment_context>")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, "</environment_context>")
	if !ok {
		return false
	}
	previousCwd := t.st.Last.Cwd
	var cwd, date, sandbox, branch bool
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "cwd: "):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, "cwd: ")); err == nil {
				t.st.Last.Cwd = value
				cwd = true
			}
		case strings.HasPrefix(line, "date: "):
			t.st.Last.LocalDateHour = strings.TrimPrefix(line, "date: ")
			date = true
		case strings.HasPrefix(line, "sandbox: "):
			t.st.Last.Sandbox = strings.TrimPrefix(line, "sandbox: ")
			sandbox = true
		case strings.HasPrefix(line, scratchDirPrefix):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, scratchDirPrefix)); err == nil {
				t.st.Last.ScratchDir = value
				t.st.ScratchDirUnknown = false
			}
		case line == hostWritableRoots:
			t.st.Last.WritableRoots = ""
			t.st.WritableRootsUnknown = false
		case strings.HasPrefix(line, writableRootsPrefix):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, writableRootsPrefix)); err == nil {
				t.st.Last.WritableRoots = value
				t.st.WritableRootsUnknown = false
			}
		case line == fileToolWritesPrefix+FileWritesHost, line == fileToolWritesPrefix+FileWritesScratch, line == fileToolWritesPrefix+FileWritesSandbox:
			t.st.Last.FileToolWrites = strings.TrimPrefix(line, fileToolWritesPrefix)
		case line == shellNetworkPrefix+NetworkAllowed, line == shellNetworkPrefix+NetworkBlocked:
			t.st.Last.ShellNetwork = strings.TrimPrefix(line, shellNetworkPrefix)
		case line == detachedShellPrefix+DetachAvailable, line == detachedShellPrefix+DetachUnavailable:
			t.st.Last.DetachedShell = strings.TrimPrefix(line, detachedShellPrefix)
		case strings.HasPrefix(line, "git branch: "):
			branch = true
			value := strings.TrimPrefix(line, "git branch: ")
			t.st.GitBranchUnknown = value == unconfirmedBranch
			if t.st.GitBranchUnknown || value == outsideWorktree {
				value = ""
			}
			t.st.Last.GitBranch = value
		case strings.HasPrefix(line, "session id: "):
			t.st.Last.SessionID = strings.TrimPrefix(line, "session id: ")
		case strings.HasPrefix(line, parentSessionPrefix):
			t.st.Last.ParentSessionID = strings.TrimPrefix(line, parentSessionPrefix)
		case line == ordinaryRole:
			t.st.Last.OrdinarySession = true
		case line == reportingAudiencePrefix+ReportToCaller, line == reportingAudiencePrefix+ReportToParent:
			t.st.Last.ReportingAudience = strings.TrimPrefix(line, reportingAudiencePrefix)
		case strings.HasPrefix(line, resultToolPrefix):
			if name, err := strconv.Unquote(strings.TrimPrefix(line, resultToolPrefix)); err == nil {
				t.st.Last.ResultToolName = name
			}
		case strings.HasPrefix(line, "model: "):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, "model: ")); err == nil {
				t.st.Last.Model = value
			}
		case strings.HasPrefix(line, "knowledge cutoff: "):
			if value, err := strconv.Unquote(strings.TrimPrefix(line, "knowledge cutoff: ")); err == nil {
				t.st.Last.KnowledgeCutoff = value
			}
		case line == executionModePrefix+OneShotMode, line == executionModePrefix+DaemonMode:
			t.st.Last.ExecutionMode = strings.TrimPrefix(line, executionModePrefix)
		case line == askUserPrefix+QuestionsAvailable, line == askUserPrefix+QuestionsUnavailable:
			t.st.Last.AskUserState = strings.TrimPrefix(line, askUserPrefix)
		case line == resultEndReasonPrefix+OmitEndReason, line == resultEndReasonPrefix+EndReasonAvailable:
			t.st.Last.ResultEndReason = strings.TrimPrefix(line, resultEndReasonPrefix)
		case strings.HasPrefix(line, commitTrailerPrefix):
			t.st.Last.CommitTrailer = strings.TrimPrefix(line, commitTrailerPrefix)
			t.st.Last.OmitCommitTrailer = false
		case line == omitCommitTrailer:
			t.st.Last.CommitTrailer = ""
			t.st.Last.OmitCommitTrailer = true
		case strings.HasPrefix(line, "load pressure: "):
			t.st.Last.Pressure.Load = pressureValue(line, "load")
		case strings.HasPrefix(line, "memory pressure: "):
			t.st.Last.Pressure.Memory = pressureValue(line, "memory")
		case strings.HasPrefix(line, "disk pressure: "):
			t.st.Last.Pressure.Disk = pressureValue(line, "disk")
		}
	}
	if !branch && previousCwd != "" && previousCwd != t.st.Last.Cwd {
		t.st.GitBranchUnknown = true
		t.st.Last.GitBranch = ""
	}
	// Compaction may retain a changed-field block while removing its full
	// baseline. Such a tail still needs a full emission before the next input.
	t.st.HasSent = t.st.HasSent || (cwd && date && sandbox)
	return true
}

func pressureValue(line, name string) string {
	prefix := name + " pressure: "
	value := strings.TrimPrefix(line, prefix)
	if value == "back to normal" {
		return ""
	}
	return line
}

// RenderDiff renders the changed fields of cur against the last emission,
// or every non-empty field on the first emission. It returns "" when there
// is nothing to say. A non-empty return updates the tracker state, so the
// caller must deliver the rendered block to the model.
func (t *Tracker) RenderDiff(cur Snapshot) string {
	first := !t.st.HasSent
	prev := t.st.Last
	moved := prev.Cwd != "" && cur.Cwd != prev.Cwd
	// The collector already consulted this checkout's observations. An
	// unqualified branch in retained history may belong to another checkout.
	unknown := cur.gitBranchUnknown
	if unknown {
		cur.GitBranch = ""
		cur.gitBranchUnknown = false
	}
	// An inconclusive probe establishes nothing. When it is for the checkout the
	// retained state already sits in, and that state carries no branch of its
	// own, there is nothing here to correct: the saved prompt's branch still
	// stands, and saying "unconfirmed" would supersede a real branch with a
	// weaker claim about the very same directory. Stay silent and leave the next
	// observation owed.
	//
	// Every other shape still speaks, because each one does have something to
	// correct or to warn about: a moved checkout (the retained branch describes
	// somewhere else), a retained branch that may belong to another checkout,
	// and a probe that answered -- including "no branch", which must clear.
	nothingToCorrect := unknown && !moved && prev.Cwd != "" && prev.GitBranch == ""
	reportBranch := cur.recordGitBranch || moved || cur.GitBranch != prev.GitBranch ||
		(!nothingToCorrect && unknown != t.st.GitBranchUnknown) ||
		(first && unknown && prev.Cwd != "" && !nothingToCorrect)
	cur.recordGitBranch = false
	if !first && cur == prev && !reportBranch && !t.st.ScratchDirUnknown && !t.st.WritableRootsUnknown {
		return ""
	}

	var lines []string
	add := func(changed bool, line string) {
		if line != "" && (first || changed) {
			lines = append(lines, line)
		}
	}
	// Compaction may retain this branch update by itself.
	add(cur.Cwd != prev.Cwd || reportBranch, fmt.Sprintf("cwd: %q", cur.Cwd))
	add(cur.LocalDateHour != prev.LocalDateHour, "date: "+cur.LocalDateHour)
	add(cur.Sandbox != prev.Sandbox, "sandbox: "+cur.Sandbox)
	if cur.ScratchDir != prev.ScratchDir || t.st.ScratchDirUnknown {
		lines = append(lines, scratchDirPrefix+strconv.Quote(cur.ScratchDir))
	}
	if cur.WritableRoots != prev.WritableRoots || t.st.WritableRootsUnknown {
		if cur.WritableRoots == "" {
			lines = append(lines, hostWritableRoots)
		} else {
			lines = append(lines, writableRootsPrefix+strconv.Quote(cur.WritableRoots))
		}
	}
	if cur.FileToolWrites != "" && cur.FileToolWrites != prev.FileToolWrites {
		lines = append(lines, fileToolWritesPrefix+cur.FileToolWrites)
	}
	if cur.ShellNetwork != "" && cur.ShellNetwork != prev.ShellNetwork {
		lines = append(lines, shellNetworkPrefix+cur.ShellNetwork)
	}
	if cur.DetachedShell != prev.DetachedShell {
		switch cur.DetachedShell {
		case DetachAvailable:
			lines = append(lines, detachedShellPrefix+DetachAvailable,
				"Use detached mode for a process that should outlive this session. Give it absolute paths outside session scratch.")
		case DetachUnavailable:
			lines = append(lines, detachedShellPrefix+DetachUnavailable,
				"Shell jobs started in background mode belong to this session and end when it exits.")
		}
	}
	if reportBranch {
		branch := cur.GitBranch
		if unknown {
			branch = unconfirmedBranch
		} else if branch == "" {
			branch = outsideWorktree
		}
		lines = append(lines, "git branch: "+branch)
	}
	// A fold can retain an identity update while dropping the first environment
	// block. Refill the missing environment without repeating retained identity.
	if cur.SessionID != "" && cur.SessionID != prev.SessionID {
		lines = append(lines, "session id: "+cur.SessionID)
	}
	if cur.OrdinarySession && (!prev.OrdinarySession || cur.SessionID != prev.SessionID || cur.ParentSessionID != prev.ParentSessionID) {
		lines = append(lines, ordinaryRole, parentSessionPrefix+cur.ParentSessionID,
			"This child was created without a Manager launch. Apply the startup rule for a newly created session and use the tools and arguments offered in this request.")
	}
	if cur.ReportingAudience != prev.ReportingAudience {
		switch cur.ReportingAudience {
		case ReportToCaller:
			lines = append(lines, reportingAudiencePrefix+ReportToCaller,
				"This is a root session. Direct progress and final reports to your human partner or caller through the result tool. Act on follow-up messages from that caller.")
		case ReportToParent:
			lines = append(lines, reportingAudiencePrefix+ReportToParent,
				"This is a delegate session. Direct progress and final reports to your parent agent through the result tool. Act on follow-up messages from that agent.")
		}
	}
	if cur.ResultToolName != "" && cur.ResultToolName != prev.ResultToolName {
		lines = append(lines, resultToolPrefix+strconv.Quote(cur.ResultToolName),
			"Use this tool for progress and final reports. Its name supersedes the result-tool name in the startup instructions.")
	}
	if cur.Model != "" {
		if cur.Model != prev.Model {
			lines = append(lines, "model: "+strconv.Quote(cur.Model))
		}
		if cur.Model != prev.Model || cur.KnowledgeCutoff != prev.KnowledgeCutoff {
			lines = append(lines, "knowledge cutoff: "+strconv.Quote(cur.KnowledgeCutoff))
		}
	}
	if cur.ExecutionMode != prev.ExecutionMode {
		switch cur.ExecutionMode {
		case OneShotMode:
			lines = append(lines, executionModePrefix+OneShotMode,
				"This process exits after the turn's work drains. Before ending the turn, keep any shell job whose result you need under a job_watch with a progress_interval_ms of a few minutes. If the watch tool is unavailable, collect the result before ending the turn. Jobs still running at exit are killed.")
		case DaemonMode:
			lines = append(lines, executionModePrefix+DaemonMode,
				"Shell jobs and delegates keep running after the turn ends. Their completion can wake this session without a progress watch.")
		}
	}
	if cur.AskUserState != prev.AskUserState {
		switch cur.AskUserState {
		case QuestionsAvailable:
			lines = append(lines, askUserPrefix+QuestionsAvailable,
				"Use ask_user for decisions only your human partner can settle. First check the task and available evidence, then ask the remaining questions together.")
		case QuestionsUnavailable:
			lines = append(lines, askUserPrefix+QuestionsUnavailable,
				"No one can answer through ask_user in this session. Use the task and available evidence to make decisions and continue. Report any assumptions that affect the result.")
		}
	}
	if cur.ResultEndReason != prev.ResultEndReason {
		switch cur.ResultEndReason {
		case OmitEndReason:
			lines = append(lines, resultEndReasonPrefix+OmitEndReason,
				"Omit end_reason when calling the result tool. Use end_turn=true to deliver your final report.")
		case EndReasonAvailable:
			lines = append(lines, resultEndReasonPrefix+EndReasonAvailable,
				"When ending your turn through the result tool, set end_reason to needs_response if your caller must act, waiting_on_work if pending work will wake you, or done when your work is complete.")
		}
	}
	if cur.CommitTrailer != "" {
		if cur.CommitTrailer != prev.CommitTrailer {
			lines = append(lines, commitTrailerPrefix+cur.CommitTrailer)
		}
	} else if prev.CommitTrailer != "" || cur.SessionID != prev.SessionID || (cur.OmitCommitTrailer && !prev.OmitCommitTrailer) {
		lines = append(lines, omitCommitTrailer)
		cur.OmitCommitTrailer = true
	}
	for _, p := range []struct{ label, cur, prev string }{
		{"load", cur.Pressure.Load, prev.Pressure.Load},
		{"memory", cur.Pressure.Memory, prev.Pressure.Memory},
		{"disk", cur.Pressure.Disk, prev.Pressure.Disk},
	} {
		switch {
		case p.cur != "":
			add(p.cur != p.prev, p.cur)
		case p.prev != "":
			lines = append(lines, p.label+" pressure: back to normal")
		}
	}

	if len(lines) == 0 {
		return ""
	}
	t.st = State{Last: cur, HasSent: true, GitBranchUnknown: unknown}
	return "<environment_context>\n" + strings.Join(lines, "\n") + "\n</environment_context>"
}

// parseLoad1 extracts the 1-minute load average from either darwin
// sysctl output ("{ 2.16 3.57 4.34 }") or /proc/loadavg ("2.16 3.57 ...").
func parseLoad1(s string) (float64, bool) {
	fields := strings.Fields(strings.Trim(strings.TrimSpace(s), "{}"))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	// ParseFloat accepts "NaN" and "Inf", which are not load averages. Reporting
	// ok for them cannot be recovered downstream: every comparison against NaN
	// is false, so loadWarning's `load1 <= 2*cores` guard falls through and
	// renders "load pressure: NaN (N cores)" into the model's context.
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func loadWarning(load1 float64, cores int) string {
	if cores <= 0 || load1 <= float64(2*cores) {
		return ""
	}
	return fmt.Sprintf("load pressure: %.1f (%d cores)", load1, cores)
}

func diskWarning(frac float64) string {
	if frac <= 0.90 {
		return ""
	}
	return fmt.Sprintf("disk pressure: volume %d%% full", int(frac*100+0.5))
}
