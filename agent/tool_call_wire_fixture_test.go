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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

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
	// A short command timeout, so a long command's foreground wait ends inside
	// the test (call_shell_timeout); every other command finishes well within it.
	s := newSession(t, withDir(dir), withConfig(SessionConfig{
		MaxSubagentDepth:        1,
		DefaultCommandTimeoutMS: 2000,
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

// A job's id is random, and a foreground wait's elapsed seconds vary.
var (
	// identifier.NewJobID: job_, the owner session's id, _, a 12-character suffix.
	toolWireJobID       = regexp.MustCompile(`job_[0-9A-Za-z]{22}_[0-9A-Za-z]{12}`)
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

// jobTarget targets the job an earlier call's result names.
func jobTarget(callID string) func(map[string]tool.ExecResult) map[string]any {
	return func(earlier map[string]tool.ExecResult) map[string]any {
		return map[string]any{"target": toolWireJobID.FindString(earlier[callID].Output)}
	}
}

// withFixedJob records a shell result whose command became a job: its job id
// fixed, the wait's elapsed seconds fixed, and its state (which carries the
// elapsed milliseconds) left off.
func withFixedJob(res tool.ExecResult) tool.ExecResult {
	res.Output = toolWireJobID.ReplaceAllString(res.Output, "job_fixture")
	res.Output = toolWireWaitElapsed.ReplaceAllString(res.Output, "the foreground wait ended after 2s")
	return withoutState(res)
}

func TestToolCallWireFixtures(t *testing.T) {
	t.Parallel()
	dir, s := toolWireWorkspace(t)
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
			note:      "A command still running when its foreground wait timed out (the session's command timeout is 2s): it keeps running as a job, and the footer says so in several parts (its job id and the wait's seconds fixed, its state left off).",
			args:      map[string]any{"command": "printf 'started\\n'; sleep 10"},
			normalize: withFixedJob,
		},
		{
			id: "call_job_status", tool: "job_status",
			note:      "The job the timed-out command left running, checked straight after (it runs 8s more): its id read from that call's result, then fixed.",
			argsFrom:  jobTarget("call_shell_timeout"),
			normalize: withFixedJobClock,
		},
		{
			id: "call_job_list", tool: "job_list",
			note:      "The session's jobs, that one running.",
			args:      map[string]any{},
			normalize: withFixedJobClock,
		},
		{
			id: "call_job_stop", tool: "job_stop",
			note:      "The same job stopped.",
			argsFrom:  jobTarget("call_shell_timeout"),
			normalize: withFixedJobClock,
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
			id: "call_unknown", tool: "compact_context",
			note:   "Hand-written: a tool no step summary covers; only its name matters.",
			args:   map[string]any{"note_to_self": "Next: run the race detector."},
			output: "compacted",
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
			res := session.reg.ExecuteCall(context.Background(), env, llm.ToolCallData{ID: call.id, Name: call.tool, Arguments: args})
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
			// A job's id is random, in a call's arguments as in its result.
			text = toolWireJobID.ReplaceAllString(strings.ReplaceAll(text, dir, toolWireCwd), "job_fixture")
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
