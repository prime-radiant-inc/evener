package agent

// A step in a transcript is a tool call and its result: a commandExecution
// item carrying the tool's name and arguments, and the result item that
// settles it. Both clients read a step's summary ("Read agent/tree.go ·
// lines 1-4", "Ran ls agent") from those arguments and that output, so a
// summary written against hand-built items pins argument names and output
// shapes the tools may not use.
//
// This test is the corpus for those summaries. It runs each core tool for
// real, through a session's own registry against a temp workspace, with the
// arguments each tool's definition names (agent/internal/tool/definitions.go),
// and records the calls and results the way a session does (one ASSISTANT
// entry, one TOOL_RESULTS entry), projected through apptranscript the way
// history reaches the wire.
//
// Four outputs are hand-written in their tool's shape, because the tool can't
// run here: web_fetch and web_search need the network (and a model), and the
// two MCP tools need an MCP server. The uncovered tool is hand-written too;
// only its name matters to a summary.
//
// To keep the corpus the same on every machine, the temp workspace's path is
// rewritten to a fixed one (toolWireCwd), and grep's and glob's result lines
// are sorted: glob orders by modification time and grep by its walk, which
// neither pins. grep's output also differs by whether ripgrep is installed
// (execenv's Grep uses it when it can): ripgrep prints absolute paths and a
// trailing newline, the native fallback paths relative to the searched
// directory and none. The corpus records the fallback's form.
//
// Regenerate after an intentional change with `make fuzz-goldens`
// (wire_fixture_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// toolWireFixturePath is the committed corpus both clients read.
const toolWireFixturePath = "testdata/toolwire/calls.json"

// toolWireCwd is the fixed path the temp workspace is recorded as.
const toolWireCwd = "/home/jesse/git/evener"

// toolWireAnswers is the user's reply to call_ask_user, choosing its
// recommended option. It is a hand-written constant: the [answers] composer
// lives only in the TypeScript clients (appwire-client's composeAskAnswers),
// so askUserCorpus.test.ts pins that this string is what it composes.
const toolWireAnswers = "[answers]\n1. [Deploy] → \"Ship tonight\""

// toolWireEarlierSession is the earlier session in the project's state bucket.
const toolWireEarlierSession = "02wMz5Txv5aIxgf9yVdd0N"

// toolWireCurrentSession is the id the recording session is recorded as.
const toolWireCurrentSession = "02wMz5TxvEMoJEDTDGOTil"

type toolWireCall struct {
	id   string
	tool string
	note string
	args map[string]any
	// output is the recorded result for a tool that can't run here; empty
	// means the tool runs for real.
	output string
	// normalize makes a real result the same on every machine and run; nil
	// records it as the tool returned it.
	normalize func(tool.ExecResult) tool.ExecResult
	// inRepo runs the call in a session rooted at a real git repository,
	// which manage_worktree needs, in place of the temp workspace's.
	inRepo bool
	// argsFrom builds the call's arguments from the results of the calls
	// before it, by call id and as each tool returned them, for an argument
	// only a run can know (an id a tool minted). Nil uses args.
	argsFrom func(earlier map[string]tool.ExecResult) map[string]any
	// foregroundWaitMS is how long a shell call waits in the foreground
	// before its command becomes a job; zero keeps the session's
	// toolWireForegroundWaitMS.
	foregroundWaitMS int
}

// toolWireForegroundWaitMS is every recorded shell call's foreground wait
// unless the call names its own: the session's ceiling (MaxCommandTimeoutMS),
// so a command that finishes in milliseconds on an idle machine still
// finishes in the foreground on a loaded one. A call that must become a job
// names a short wait of its own instead of sharing one with every command.
const toolWireForegroundWaitMS = 600_000

// execute runs the call on the session, with its own foreground wait when it
// names one.
func (c toolWireCall) execute(session *Session, env execenv.ExecutionEnvironment, args json.RawMessage) tool.ExecResult {
	if c.foregroundWaitMS > 0 {
		setToolWireForegroundWait(session, c.foregroundWaitMS)
		defer setToolWireForegroundWait(session, toolWireForegroundWaitMS)
	}
	return session.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: c.id, Name: c.tool, Arguments: args})
}

// setToolWireForegroundWait sets the wait the shell tool reads live from the
// session's config (toolDeps.cmdTimeouts).
func setToolWireForegroundWait(session *Session, ms int) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.cfg.DefaultCommandTimeoutMS = ms
}

// arguments is the call's arguments, given the results of the calls before
// it.
func (c toolWireCall) arguments(earlier map[string]tool.ExecResult) map[string]any {
	if c.argsFrom != nil {
		return c.argsFrom(earlier)
	}
	return c.args
}

// toolWireWorkspace writes the files the calls read, search and edit, and
// installs the skill use_skill activates. The workspace's path is resolved
// once, so the tools and the corpus's relocation read the same spelling (a
// macOS temp dir lives behind /private).
func toolWireWorkspace(t *testing.T) (string, *Session) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"agent/tree.go":       "package agent\n\nfunc settle() {}\n",
		"agent/tree_test.go":  "package agent\n\nfunc settleForTest() {}\n",
		"agent/drain_test.go": "package agent\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, dir, "systematic-debugging",
		"---\nname: systematic-debugging\ndescription: Find the root cause first\n---\n# Systematic debugging\n\nFind the root cause first.\n")
	// The project's state bucket holds one earlier session, which the
	// transcript tools read and search.
	stateDir := newBucket(t)
	writeFindSession(t, stateDir, findMetaSpec{
		id:         toolWireEarlierSession,
		name:       "Settle race in the tree",
		model:      "gpt-5.2",
		workingDir: toolWireCwd,
		turnCount:  1,
		updated:    wireFixtureStart.Add(-24 * time.Hour),
	}, "The settle race comes from the drain running after settle reads the tree.")
	// Every shell call's foreground wait is the ceiling; only a call that
	// names its own wait (call_shell_timeout) waits less.
	s := newSession(t, withDir(dir), withConfig(SessionConfig{
		MaxSubagentDepth:        1,
		DefaultCommandTimeoutMS: toolWireForegroundWaitMS,
		AgentsDocPath:           filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		StateDir:                stateDir,
	}))
	skillDir := filepath.Join(dir, "skills", "systematic-debugging")
	s.skills.Entries["systematic-debugging"] = skill.Descriptor{
		CatalogName: "systematic-debugging",
		Controls:    skill.InvocationControls{UserInvocable: true},
		Meta: skill.SkillMeta{
			Name:        "systematic-debugging",
			Description: "Find the root cause first",
			Dir:         skillDir,
			SkillFile:   filepath.Join(skillDir, "SKILL.md"),
		},
	}
	return dir, s
}

// sortedOutput sorts a result's lines, for a tool whose order depends on the
// filesystem: glob orders by modification time and grep by its walk.
func sortedOutput(res tool.ExecResult) tool.ExecResult {
	lines := strings.Split(res.Output, "\n")
	sort.Strings(lines)
	res.Output = strings.Join(lines, "\n")
	return res
}

// nativeGrepForm writes a grep result the way execenv's native Grep fallback
// does, sorted: each path relative to the searched directory, no trailing
// newline. ripgrep, which Grep uses when it is installed, prints absolute
// paths and a trailing newline; the fallback's own output passes through
// unchanged.
func nativeGrepForm(dir, searched string) func(tool.ExecResult) tool.ExecResult {
	base := filepath.Join(dir, searched) + string(filepath.Separator)
	return func(res tool.ExecResult) tool.ExecResult {
		lines := strings.Split(strings.TrimSuffix(res.Output, "\n"), "\n")
		for i, line := range lines {
			lines[i] = strings.TrimPrefix(line, base)
		}
		res.Output = strings.Join(lines, "\n")
		return sortedOutput(res)
	}
}

// withoutState leaves a result's tool state off, for a tool whose state
// changes every run.
func withoutState(res tool.ExecResult) tool.ExecResult {
	res.ToolState = nil
	return res
}

// A URL entry's id is minted per run and its added_at is the session clock;
// neither can be pinned. urls_add's output and state carry the id, and
// urls_remove's arguments and state repeat it, so the id is replaced once it
// is minted (toolWireURLID reads it from urls_add's result); added_at is
// fixed here.
var (
	toolWireURLID   = regexp.MustCompile(`\[id: ([0-9A-Za-z]{22})\]`)
	toolWireAddedAt = regexp.MustCompile(`"added_at":\d+`)
)

// withFixedURLClock records a urls_add result with the entry's added_at fixed.
func withFixedURLClock(res tool.ExecResult) tool.ExecResult {
	res.ToolState = toolWireAddedAt.ReplaceAll(res.ToolState, []byte(`"added_at":1000000000`))
	return res
}

// toolWireModelSnapshot gives the recording session a pageable model list: two
// verified choices and one provider whose enumeration failed, so the snapshot
// is not inline (model_list is registered) and its first page carries a
// continuation cursor.
func toolWireModelSnapshot(t *testing.T) modelavailability.Snapshot {
	t.Helper()
	return modelavailability.Capture(context.Background(),
		[]string{"openai", "router"}, "openai",
		func(_ context.Context, name string) ([]string, error) {
			if name == "openai" {
				return []string{"gpt-5.2", "gpt-5.3"}, nil
			}
			return nil, errors.New("router is unavailable")
		}, time.Second)
}

// A job's id is random, and a foreground wait's elapsed seconds vary.
var (
	// identifier.NewJobID: job_, the owner session's id, _, a 12-character suffix.
	toolWireJobID       = regexp.MustCompile(`job_[0-9A-Za-z]{22}_[0-9A-Za-z]{12}`)
	toolWireWatchID     = regexp.MustCompile(`watch_[0-9A-Za-z]{22}`)
	toolWireWaitElapsed = regexp.MustCompile(`the foreground wait ended after [\d.]+s`)
)

// The recording session lists itself among the project's sessions, with a
// random id, an update time of now, and a project named for its temp
// workspace.
var toolWireCurrentRow = regexp.MustCompile(`updated \d{4}-\d{2}-\d{2} \d{2}:\d{2} · project \S+ · current`)

// withFixedCurrentSession records a session listing with the recording
// session's row fixed: its id toolWireCurrentSession, updated at the
// fixture's start, in the project evener.
func withFixedCurrentSession(id string) func(tool.ExecResult) tool.ExecResult {
	return func(res tool.ExecResult) tool.ExecResult {
		res.Output = strings.ReplaceAll(res.Output, id, toolWireCurrentSession)
		res.Output = toolWireCurrentRow.ReplaceAllString(res.Output,
			"updated "+wireFixtureStart.UTC().Format("2006-01-02 15:04")+" · project evener · current")
		return res
	}
}

// The git repository the worktree calls run in is recorded as the project at
// toolWireCwd, with its state under toolWireStateDir and its one commit as
// toolWireHead.
const (
	toolWireStateDir    = "/home/jesse/.local/state/evener/projects/evener"
	toolWireHead        = "5e5f1c3a9b7d4e2f8a6c0b1d3e5f7a9c2b4d6e8f"
	toolWireRepoSession = "02wMz5TxvQfJ8vYb2Wc7Lh"
)

var (
	// A managed worktree's directory under the state dir is named for the
	// repository's temp path.
	toolWireWorktreeProject = regexp.MustCompile(`/worktrees/[^/"\s]+/`)
	toolWireAge             = regexp.MustCompile(`"age_seconds": [\d.]+`)
	toolWireCreatedAt       = regexp.MustCompile(`"created_at": "[^"]+"`)
)

// toolWireRepoRelocated rewrites the repository's paths, commit and session
// in text: a result, or a call's arguments (adopt names a path).
func toolWireRepoRelocated(repo *wtRepo, text string) string {
	out := toolWireWorktreeProject.ReplaceAllString(text, "/worktrees/evener/")
	out = strings.ReplaceAll(out, repo.stateDir, toolWireStateDir)
	out = strings.ReplaceAll(out, repo.mainRoot, toolWireCwd)
	out = strings.ReplaceAll(out, repo.head, toolWireHead)
	out = strings.ReplaceAll(out, repo.head[:12], toolWireHead[:12])
	return strings.ReplaceAll(out, repo.s.ID(), toolWireRepoSession)
}

// withFixedRepo records a manage_worktree result with the repository's
// paths, commit, session and clock fixed.
func withFixedRepo(repo *wtRepo) func(tool.ExecResult) tool.ExecResult {
	return func(res tool.ExecResult) tool.ExecResult {
		out := toolWireAge.ReplaceAllString(toolWireRepoRelocated(repo, res.Output), `"age_seconds": 1`)
		res.Output = toolWireCreatedAt.ReplaceAllString(out,
			`"created_at": "`+wireFixtureStart.UTC().Format(time.RFC3339)+`"`)
		return res
	}
}

// A task's created, updated and completed stamps are the store's clock.
var toolWireTimestamp = regexp.MustCompile(`"\d{4}-\d{2}-\d{2}T[\d:.]+(?:Z|[+-]\d{2}:\d{2})"`)

// withFixedTimes records a result whose state carries clock times with each
// one fixed at the fixture's start.
func withFixedTimes(res tool.ExecResult) tool.ExecResult {
	fixed, err := json.Marshal(wireFixtureStart.UTC())
	if err != nil {
		panic(err)
	}
	res.ToolState = toolWireTimestamp.ReplaceAll(res.ToolState, fixed)
	return res
}

// A job's clock: how long it ran and sat quiet, when it started and last
// moved, and job_list's local start time.
var (
	toolWireJobMs     = regexp.MustCompile(`"(running_for_ms|quiet_for_ms)":\d+`)
	toolWireJobAt     = regexp.MustCompile(`"(started_at|last_event_at)":"[^"]+"`)
	toolWireJobListAt = regexp.MustCompile(`started \d{4}-\d{2}-\d{2} \d{2}:\d{2}`)
)

// withFixedJobClock records a job tool's result with the job fixed as
// withFixedJob fixes it, and its clock fixed at the fixture's start.
func withFixedJobClock(res tool.ExecResult) tool.ExecResult {
	res = withFixedJob(res)
	out := toolWireJobMs.ReplaceAllString(res.Output, `"$1":2000`)
	out = toolWireJobAt.ReplaceAllString(out, `"$1":"`+wireFixtureStart.UTC().Format(time.RFC3339)+`"`)
	res.Output = toolWireJobListAt.ReplaceAllString(out, "started "+wireFixtureStart.UTC().Format("2006-01-02 15:04"))
	return res
}

// jobTarget targets the job an earlier call's result names, failing the
// test when that result names none.
func jobTarget(t *testing.T, callID string) func(map[string]tool.ExecResult) map[string]any {
	return func(earlier map[string]tool.ExecResult) map[string]any {
		job := toolWireJobID.FindString(earlier[callID].Output)
		if job == "" {
			t.Fatalf("%s's result names no job: %q", callID, earlier[callID].Output)
		}
		return map[string]any{"target": job}
	}
}

// watchOperation runs a job_watch operation on the watch an earlier call's
// result names, failing the test when that result names none.
func watchOperation(t *testing.T, operation, callID string) func(map[string]tool.ExecResult) map[string]any {
	return func(earlier map[string]tool.ExecResult) map[string]any {
		watch := toolWireWatchID.FindString(earlier[callID].Output)
		if watch == "" {
			t.Fatalf("%s's result names no watch: %q", callID, earlier[callID].Output)
		}
		return map[string]any{"operation": operation, "watch_id": watch}
	}
}

// withFixedJob records a shell result whose command became a job: the
// wait's elapsed seconds fixed, and its state (which carries the elapsed
// milliseconds) left off. Its job id is fixed with every other in the items
// (toolWireIDsNumbered).
func withFixedJob(res tool.ExecResult) tool.ExecResult {
	res.Output = toolWireWaitElapsed.ReplaceAllString(res.Output, "the foreground wait ended after 2s")
	return withoutState(res)
}

// toolWireIDsNumbered fixes each distinct id the pattern matches in text as
// <kind>_fixture_1, <kind>_fixture_2, … in the order the ids first appear,
// so two jobs (or watches) stay two.
func toolWireIDsNumbered(text string, pattern *regexp.Regexp, kind string) string {
	numbers := map[string]string{}
	return pattern.ReplaceAllStringFunc(text, func(id string) string {
		if fixed, ok := numbers[id]; ok {
			return fixed
		}
		fixed := fmt.Sprintf("%s_fixture_%d", kind, len(numbers)+1)
		numbers[id] = fixed
		return fixed
	})
}

// withFixedJobList records job_list's listing with its clock fixed and its
// rows in a stable order: the tool orders them by activity time, which ties
// for jobs started together and breaks the tie at random. Rows sort by their
// text with the random ids left out.
func withFixedJobList(res tool.ExecResult) tool.ExecResult {
	res = withFixedJobClock(res)
	lines := strings.Split(res.Output, "\n")
	end := 1
	for end < len(lines) && lines[end] != "" {
		end++
	}
	rows := lines[1:end]
	sort.SliceStable(rows, func(i, j int) bool {
		return toolWireJobID.ReplaceAllString(rows[i], "") < toolWireJobID.ReplaceAllString(rows[j], "")
	})
	res.Output = strings.Join(lines, "\n")
	return res
}

func TestToolCallWireFixtures(t *testing.T) {
	t.Parallel()
	dir, s := toolWireWorkspace(t)
	// The session's model list is pageable: two verified choices and a failed
	// provider, so its pages carry a cursor to record.
	snapshot := toolWireModelSnapshot(t)
	s.modelSnapshot = &snapshot
	// The ids the recording mints that a corpus cannot pin: the URL entry's id
	// and the model list's cursor token. Each is replaced in the items once its
	// call has run.
	var recordedURLID, recordedModelCursor string
	repo := newWorktreeRepo(t)
	// A worktree git made itself under the managed root, with no evener
	// record of it, for manage_worktree adopt.
	lane := repo.addUnmanagedWorktreeFixture(t, "lane", "lane")

	webFetch, err := json.Marshal(map[string]any{
		"answer":       "The release notes list three fixes to the tree settle pass.",
		"raw_file":     "/tmp/evener/web/release-notes.html",
		"url":          "https://example.com/release-notes",
		"content_type": "text/html",
		"size_bytes":   48213,
	})
	if err != nil {
		t.Fatalf("web_fetch output: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: agent/tree.go\n@@\n func settle() {\n+\tlog()\n*** Add File: agent/tree_drain.go\n+package agent\n*** End Patch"

	// In order: each call runs against the workspace the calls before it left.
	calls := []toolWireCall{
		{
			id: "call_read_file", tool: "read_file",
			note: "A whole-file read: the output numbers each line.",
			args: map[string]any{"file_path": "agent/tree.go"},
		},
		{
			id: "call_read_file_range", tool: "read_file",
			note: "A read with an offset and a limit.",
			args: map[string]any{"file_path": "agent/tree.go", "offset": 2, "limit": 2},
		},
		{
			id: "call_grep", tool: "grep",
			note:      "A search in a path, filtered by a glob: one path:line:text line per hit (sorted, in the native fallback's form).",
			args:      map[string]any{"pattern": "func settle", "path": "agent", "glob_filter": "*.go"},
			normalize: nativeGrepForm(dir, "agent"),
		},
		{
			id: "call_glob", tool: "glob",
			note:      "A glob match: one path per match (sorted here).",
			args:      map[string]any{"pattern": "agent/**/*_test.go"},
			normalize: sortedOutput,
		},
		{
			id: "call_list_dir", tool: "list_dir",
			note: "A directory listing: one name and size per entry, then a blank line and the entry count.",
			args: map[string]any{"path": "agent"},
		},
		{
			id: "call_list_dir_empty", tool: "list_dir",
			note: "An empty directory: the count alone.",
			args: map[string]any{"path": "empty"},
		},
		{
			id: "call_list_dir_page", tool: "list_dir",
			note: "A page of a longer listing, paged with the tool's own limit (its default page is 1000 entries): the footer says how many of how many, and where the next page starts.",
			args: map[string]any{"path": "agent", "limit": 2},
		},
		{
			id: "call_edit_file", tool: "edit_file",
			note: "One replacement in one file.",
			args: map[string]any{"file_path": "agent/tree.go", "old_string": "func settle() {}\n", "new_string": "func settle() {\n\tdrain()\n}\n"},
		},
		{
			id: "call_write_file", tool: "write_file",
			note: "A new file written whole.",
			args: map[string]any{"file_path": "agent/tree_order.go", "content": "package agent\n"},
		},
		{
			id: "call_apply_patch", tool: "apply_patch",
			note: "A v4a patch updating one file and adding another.",
			args: map[string]any{"patch": patch},
		},
		{
			id: "call_shell", tool: "shell",
			note: "A command run from the session's own directory, prefixed with cd to it: the output ends with its exit code.",
			args: map[string]any{"command": "cd " + dir + " && cat agent/tree_order.go"},
		},
		{
			id: "call_shell_failed", tool: "shell",
			note: "A command that exited 1: still a result, not an error, with [exit 1] at its end.",
			args: map[string]any{"command": "test -f agent/missing.go"},
		},
		{
			id: "call_shell_windowed", tool: "shell",
			note:      "A long output, windowed: a digest of its head and tail, then [exit 0 · output windowed — read more with read_transcript(...)] (its job id fixed, its state left off).",
			args:      map[string]any{"command": "seq 1 3000"},
			normalize: withFixedJob,
		},
		{
			id: "call_shell_timeout", tool: "shell",
			note:             "A command still running when its foreground wait timed out (this call's wait is 2s): it keeps running as a job, and the footer says so in several parts (its job id and the wait's seconds fixed, its state left off). It sleeps far longer than the recording takes, so the job calls after it always find it running until call_job_stop stops it.",
			args:             map[string]any{"command": "printf 'started\\n'; sleep 600"},
			foregroundWaitMS: 2000,
			normalize:        withFixedJob,
		},
		{
			id: "call_job_status", tool: "job_status",
			note:      "The job the timed-out command left running, checked: its id read from that call's result, then fixed.",
			argsFrom:  jobTarget(t, "call_shell_timeout"),
			normalize: withFixedJobClock,
		},
		{
			id: "call_job_list", tool: "job_list",
			note:      "The session's jobs, that one running (rows in a stable order).",
			args:      map[string]any{},
			normalize: withFixedJobList,
		},
		{
			id: "call_job_stop", tool: "job_stop",
			note:      "The same job stopped.",
			argsFrom:  jobTarget(t, "call_shell_timeout"),
			normalize: withFixedJobClock,
		},
		{
			id: "call_watch_timer", tool: "job_watch",
			note: "A one-shot timer the session set for itself, with a note.",
			args: map[string]any{"operation": "create", "source": "self", "after_seconds": 300, "note": "Check the deploy finished."},
		},
		{
			id: "call_watch_repeat", tool: "job_watch",
			note: "A repeating timer, with a note.",
			args: map[string]any{"operation": "create", "source": "self", "repeat_seconds": 600, "note": "Look over the open PRs."},
		},
		{
			id: "call_watch_list", tool: "job_watch",
			note:      "The session's watches, both timers (each created at the fixture's start).",
			args:      map[string]any{"operation": "list"},
			normalize: withFixedTimes,
		},
		{
			id: "call_watch_inspect", tool: "job_watch",
			note:      "The one-shot timer, inspected (created at the fixture's start).",
			argsFrom:  watchOperation(t, "inspect", "call_watch_timer"),
			normalize: withFixedTimes,
		},
		{
			id: "call_watch_clear", tool: "job_watch",
			note:     "The same timer, cleared.",
			argsFrom: watchOperation(t, "clear", "call_watch_timer"),
		},
		{
			id: "call_read_transcript", tool: "read_transcript",
			note: "An earlier session's transcript read as markdown (the default): its JSON envelope.",
			args: map[string]any{"transcript_ref": "local:" + toolWireEarlierSession},
		},
		{
			id: "call_read_transcript_outline", tool: "read_transcript",
			note: "The same transcript read as an outline, whose envelope is flat.",
			args: map[string]any{"transcript_ref": "local:" + toolWireEarlierSession, "format": "outline"},
		},
		{
			id: "call_find_sessions", tool: "find_session_transcripts",
			note: "A search of the project's sessions for a phrase the earlier one said.",
			args: map[string]any{"query": "settle race"},
		},
		{
			id: "call_find_sessions_catalog", tool: "find_session_transcripts",
			note:      "The project's recent sessions, with no query: the earlier session and the recording one (its id, update time and project fixed).",
			args:      map[string]any{},
			normalize: withFixedCurrentSession(s.ID()),
		},
		{
			id: "call_task_list_add", tool: "task_list",
			note:      "Three tasks added to an empty list. Its state is the whole list, each task's store-minted times fixed.",
			args:      map[string]any{"add": []map[string]any{{"type": "research", "description": "Reproduce the settle race", "prompt": "Run go test -race ./agent until the settle race shows."}, {"type": "fix", "description": "Order the drain before settle", "prompt": "Make the drain finish before settle reads the tree."}, {"type": "verify", "description": "Run the race detector again", "prompt": "Run go test -race ./agent -count=20."}}},
			normalize: withFixedTimes,
		},
		{
			id: "call_task_list_start", tool: "task_list",
			note:      "The first task started.",
			args:      map[string]any{"update": []map[string]any{{"id": 1, "status": "in_progress"}}},
			normalize: withFixedTimes,
		},
		{
			id: "call_task_list_done", tool: "task_list",
			note:      "The first task done with a note; the daemon starts the next one itself, so the state shows task 2 in progress though the call never named it.",
			args:      map[string]any{"update": []map[string]any{{"id": 1, "status": "done", "notes": "Reproduced in 3 of 20 runs."}}},
			normalize: withFixedTimes,
		},
		{
			id: "call_task_list_view", tool: "task_list",
			note:      "A bare call: the list, changing nothing.",
			args:      map[string]any{},
			normalize: withFixedTimes,
		},
		{
			id: "call_worktree_create", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "A worktree created from the repository's main branch; the session moves into it.",
			args: map[string]any{"operation": "create", "name": "settle-fix"},
		},
		{
			id: "call_worktree_list", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "The repository's managed worktrees, and the lane git made without evener under unmanaged.",
			args: map[string]any{"operation": "list"},
		},
		{
			id: "call_worktree_exit", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "The session leaves the worktree for the main checkout.",
			args: map[string]any{"operation": "exit"},
		},
		{
			id: "call_worktree_switch", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "The session moves back into the worktree.",
			args: map[string]any{"operation": "switch", "name": "settle-fix"},
		},
		{
			id: "call_worktree_switch_again", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "A switch to the worktree the session is already in, which changes nothing.",
			args: map[string]any{"operation": "switch", "name": "settle-fix"},
		},
		{
			id: "call_worktree_exit_again", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "The session leaves the worktree again, so it can be removed. The call and its result match the first exit's, so the registry appends its repetition nudge after the JSON.",
			args: map[string]any{"operation": "exit"},
		},
		{
			id: "call_worktree_remove", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "The worktree removed.",
			args: map[string]any{"operation": "remove", "name": "settle-fix"},
		},
		{
			id: "call_worktree_prune", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "A prune with nothing it may remove: the removed worktree is still in its grace period, and the lane has no evener record.",
			args: map[string]any{"operation": "prune"},
		},
		{
			id: "call_worktree_adopt", tool: "manage_worktree", inRepo: true, normalize: withFixedRepo(repo),
			note: "A worktree git made under the managed root without evener, adopted as a managed one.",
			args: map[string]any{"operation": "adopt", "path": lane},
		},
		{
			id: "call_ask_user", tool: "ask_user",
			note: "A question put to the user, run for real: the tool posts it and answers with its fixed acknowledgement. toolWireAnswers, the user's reply, follows in the next turn.",
			args: map[string]any{"questions": []map[string]any{{
				"header":   "Deploy",
				"question": "Ship the settle fix tonight, or wait for the race run?",
				"options": []map[string]any{
					{"label": "Ship tonight", "detail": "The fix is small and the drain test passes.", "recommended": true},
					{"label": "Wait", "detail": "Let go test -race run overnight first."},
				},
			}}},
		},
		{
			id: "call_web_fetch", tool: "web_fetch",
			note:   "Hand-written (web_fetch needs the network and a model): a fetched page's JSON result, with size_bytes.",
			args:   map[string]any{"url": "https://example.com/release-notes", "question": "What changed in the settle pass?"},
			output: string(webFetch),
		},
		{
			id: "call_web_search", tool: "web_search",
			note:   "Hand-written (web_search needs the network and a model): one result per line.",
			args:   map[string]any{"query": "go race detector settle drain"},
			output: "Go race detector docs https://go.dev/doc/articles/race_detector\nData races in Go https://example.com/races\n",
		},
		{
			id: "call_use_skill", tool: "use_skill",
			note:      "A skill activation, the skill installed in the workspace. Its tool state (the session's random id, a digest of the rendered skill) changes every run, so it is left off.",
			args:      map[string]any{"skill_name": "systematic-debugging"},
			normalize: withoutState,
		},
		{
			id: "call_mcp", tool: "github__create_issue",
			note:   "Hand-written (needs an MCP server): an MCP tool, named <server>__<tool> the way agent/internal/mcp's manager namespaces it.",
			args:   map[string]any{"title": "Tree settle races the drain", "body": "Seen in go test -race."},
			output: `{"number":3210,"url":"https://github.com/prime-radiant-inc/evener/issues/3210"}`,
		},
		{
			id: "call_mcp_hyphenated", tool: "linear_app__list_issues",
			note:   "Hand-written (needs an MCP server): an MCP tool whose server name had a hyphen (linear-app), which the manager turns into an underscore.",
			args:   map[string]any{"team": "EVE"},
			output: `{"issues":[]}`,
		},
		{
			id: "call_unknown", tool: "reindex_workspace",
			note:   "Hand-written: a tool no step summary covers (no such tool exists); only its name matters.",
			args:   map[string]any{"scope": "agent"},
			output: "reindexed 42 files",
		},
		{
			id: "call_notes_agent_set", tool: "notes_agent_set",
			note: "The agent's own note cleared with an empty note. The output is the tool's plain acknowledgement; the step's words read the empty note as a clear.",
			args: map[string]any{"note": ""},
		},
		{
			id: "call_notes_read", tool: "notes_read",
			note: "The session notes read with nothing recorded: the tool says there are none.",
			args: map[string]any{},
		},
		{
			id: "call_urls_add", tool: "urls_add",
			note:      "A labelled link added. The tool names the entry's minted id in its output (where urls_remove reads it) and carries the entry's added_at in its state; the id is fixed in the items and added_at here.",
			args:      map[string]any{"url": "https://example.com/ci/42", "label": "CI run"},
			normalize: withFixedURLClock,
		},
		{
			id: "call_urls_remove", tool: "urls_remove",
			note: "The link the call before it added, removed by the id that call's result named (the minted id fixed in the items).",
			argsFrom: func(earlier map[string]tool.ExecResult) map[string]any {
				match := toolWireURLID.FindStringSubmatch(earlier["call_urls_add"].Output)
				if len(match) < 2 {
					t.Fatalf("urls_add's result names no entry id: %q", earlier["call_urls_add"].Output)
				}
				recordedURLID = match[1]
				return map[string]any{"id": match[1]}
			},
		},
		{
			id: "call_update_goal", tool: "update_goal",
			note: "A goal marked complete when the session registered none: the tool recorded nothing and says so, which the step's words report as no goal set.",
			args: map[string]any{"status": "complete"},
		},
		{
			id: "call_compact_context", tool: "compact_context",
			note: "An empty note with no instructions and no reload_skills: the tool clears the note without asking for a compaction, and the first sentence of its output is what the step's words read.",
			args: map[string]any{"note_to_self": ""},
		},
		{
			id: "call_model_list", tool: "model_list",
			note: "The first page of the model list, bounded to one choice so a continuation follows (its cursor fixed in the items).",
			args: map[string]any{"max_count": 1},
		},
		{
			id: "call_model_list_next", tool: "model_list",
			note: "The next page, by the cursor the first page's result named: the step's words read the cursor as a further listing (its cursor fixed in the items).",
			argsFrom: func(earlier map[string]tool.ExecResult) map[string]any {
				var page struct {
					Next string `json:"next"`
				}
				if err := json.Unmarshal([]byte(earlier["call_model_list"].Output), &page); err != nil {
					t.Fatalf("model_list first page: %v", err)
				}
				if page.Next == "" {
					t.Fatalf("model_list first page names no cursor: %q", earlier["call_model_list"].Output)
				}
				recordedModelCursor = page.Next
				return map[string]any{"cursor": page.Next, "max_count": 1}
			},
		},
		{
			id: "call_doctor_evener", tool: "doctor_evener",
			note: "An earlier session's transcript rendered from the session's own state root: the command and selector are what the step's words name.",
			args: map[string]any{"command": "transcript", "selector": toolWireEarlierSession},
		},
		{
			id: "call_communicate", tool: "communicate",
			note: "A final report to the parent: end_turn ends the turn, and the step's words read it as done. The output envelope is required by the tool's schema.",
			args: map[string]any{
				"message":  "The settle race is fixed.",
				"end_turn": true,
				"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []any{}},
			},
		},
	}

	announce := llm.Message{Role: llm.RoleAssistant}
	results := llm.Message{Role: llm.RoleTool}
	notes := make(map[string]string, len(calls))
	earlier := make(map[string]tool.ExecResult, len(calls))
	for _, call := range calls {
		args, err := json.Marshal(call.arguments(earlier))
		if err != nil {
			t.Fatalf("%s arguments: %v", call.id, err)
		}
		output, isErr := call.output, false
		var state json.RawMessage
		if output == "" {
			session, env := s, s.env
			if call.inRepo {
				// A worktree call moves the session's environment, so each reads
				// the one the call before it left.
				session, env = repo.s, repo.s.currentEnv()
			}
			res := call.execute(session, env, args)
			earlier[call.id] = res
			// The result as a session records it, less its duration, which
			// differs every run.
			if call.normalize != nil {
				res = call.normalize(res)
			}
			output, isErr, state = res.Output, res.IsError, res.ToolState
			if isErr {
				t.Fatalf("%s: the %s call failed: %s", call.id, call.tool, output)
			}
		}
		announce.Content = append(announce.Content, llm.ContentPart{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: args},
		})
		results.Content = append(results.Content, llm.ContentPart{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: call.id, Name: call.tool, Content: output, IsError: isErr, ToolState: state},
		})
		notes[call.id] = call.note
	}
	reg := apptranscript.NewToolCallRegistry()
	items := apptranscript.ProjectTurn("turn_1", 1, schema.Turn{Kind: schema.TurnAssistant, Message: announce, Timestamp: wireFixtureStart}, reg, nil, nil)
	items = append(items, apptranscript.ProjectTurn("turn_1", 2, schema.Turn{Kind: schema.TurnToolResults, Message: results, Timestamp: wireFixtureStart.Add(2 * time.Second)}, reg, nil, nil)...)
	// The user's reply to the ask_user question starts the next turn.
	items = append(items, apptranscript.ProjectTurn("turn_2", 3, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User(toolWireAnswers), Timestamp: wireFixtureStart.Add(60 * time.Second)}, reg, nil, nil)...)

	checkWireFixture(t, toolWireFixturePath, struct {
		Note  string               `json:"note"`
		Cwd   string               `json:"cwd"`
		Notes map[string]string    `json:"notes"`
		Items []appwire.ThreadItem `json:"items"`
	}{
		Note:  "One ASSISTANT entry announcing one call per tool, the TOOL_RESULTS entry answering them, and the user's reply to the ask_user question, projected through apptranscript. Each core tool ran for real against a temp workspace, recorded as cwd; notes marks the few outputs hand-written because their tool can't run in a test.",
		Cwd:   toolWireCwd,
		Notes: notes,
		Items: toolWireRelocated(t, items, func(text string) string {
			// A job's id and a watch's are random, in a call's arguments as in
			// its result. This runs once over every item, so each keeps its
			// number from call to call.
			text = toolWireIDsNumbered(strings.ReplaceAll(text, dir, toolWireCwd), toolWireJobID, "job")
			text = toolWireIDsNumbered(text, toolWireWatchID, "watch")
			// The URL entry's id and the model list's cursor differ every run;
			// each is fixed here, in the arguments that name it as in the
			// results that printed it.
			if recordedURLID != "" {
				text = strings.ReplaceAll(text, recordedURLID, "url_fixture_1")
			}
			if recordedModelCursor != "" {
				text = strings.ReplaceAll(text, recordedModelCursor, "cursor_fixture_1")
			}
			return toolWireRepoRelocated(repo, text)
		}),
	}, "the AppWire package and mobile-native tests that read it")
}

// toolWireRelocated rewrites every machine-specific spelling, wherever it
// appears in the items (a shell command's cd, glob's paths, a skill's source,
// an adopted worktree's path), through relocate.
func toolWireRelocated(t *testing.T, items []appwire.ThreadItem, relocate func(string) string) []appwire.ThreadItem {
	t.Helper()
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("encode items: %v", err)
	}
	var relocated []appwire.ThreadItem
	if err := json.Unmarshal([]byte(relocate(string(encoded))), &relocated); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return relocated
}

// A call's arguments are its own, or built from the results of the calls
// before it when it has argsFrom.
func TestToolWireCallArgumentsFromEarlierResults(t *testing.T) {
	t.Parallel()
	static := toolWireCall{args: map[string]any{"path": "agent"}}
	if got := static.arguments(nil); got["path"] != "agent" {
		t.Fatalf("static arguments = %v, want its own args", got)
	}
	built := toolWireCall{
		args: map[string]any{"ignored": true},
		argsFrom: func(earlier map[string]tool.ExecResult) map[string]any {
			return map[string]any{"target": earlier["call_a"].Output}
		},
	}
	got := built.arguments(map[string]tool.ExecResult{"call_a": {Output: "job_123"}})
	if len(got) != 1 || got["target"] != "job_123" {
		t.Fatalf("built arguments = %v, want the earlier result's output as target", got)
	}
}

// A recorded shell command slower than call_shell_timeout's foreground wait
// still finishes in the foreground: that wait belongs to that call alone. A
// command that ran past it under load became a job, and its result carried the
// job's raw 1 KiB tail in place of the digest (#3357).
func TestToolWireShellCallsFinishPastTheTimeoutCallsWait(t *testing.T) {
	t.Parallel()
	_, s := toolWireWorkspace(t)
	args, err := json.Marshal(map[string]any{"command": "sleep 3; echo settled"})
	if err != nil {
		t.Fatal(err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "call_slow", Name: "shell", Arguments: args})
	if !strings.HasPrefix(res.Output, "settled\n[exit 0]") {
		t.Fatalf("a 3s command became a job, or failed: %q", res.Output)
	}
}
