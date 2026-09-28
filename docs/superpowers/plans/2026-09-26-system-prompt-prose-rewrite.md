# System Prompt Prose Rewrite (Part 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the tooling that measures how a system prompt shapes agent writing and behavior, then draft the rewritten prompt, run it against today's prompt on seven models through the lunarouter gateway, and pick a winner.

**Architecture:** The tool-fluency runner (`tools/tool-fluency/cmd/evener-fluency`) gains git fixtures, outcome checks, call caps, a prose counter, a blind review packer, and a matrix command. Eight evaluation tasks live in `tools/prompt-eval/`. Each prompt version is a commit on a lab branch that never merges; each version is built into its own `evener` binary and run through the matrix command.

**Tech Stack:** Go 1.27 (root module `primeradiant.com/evener`), `gopkg.in/yaml.v3`, bash, git, and `evener run` against the lunarouter gateway.

**Spec:** `docs/superpowers/specs/2026-09-26-system-prompt-prose-rewrite-design.md`, with the appendices `rule-inventory.md` and `research.md` in `docs/superpowers/specs/2026-09-26-system-prompt-prose-rewrite/`.

## What this plan covers

- **Tooling (Tasks 1-7)** lands as one pull request from `claude/prompt-rewrite-part2`, which already holds the spec and this plan.
- **Experiments (Tasks 8-13)** run on the lab branch `claude/prompt-rewrite-part2-lab`, which never merges.
- **Landing the winning text** into `agent/prompts/system.md.tmpl` is out of this plan. It needs part 1's collapse on `main` and this plan's winner, and it gets its own plan. Task 13 ends by asking Jesse for it.
- **The audit of the sessions on magic-kingdom**, an open item in the spec, waits on Jesse's decision about access. No task here depends on it.

## Global Constraints

- **No prose assertions in tests.** No test asserts the text of a system prompt (`docs/developing-evener/testing.md`, "Prompt Prose Is Not a Test Oracle"). Task checks judge what the agent produced, and the prose counter measures agent output.
- **Deterministic default tests.** `go test ./...` never calls a model, never needs the network, and never depends on the developer's machine beyond `git`, `bash`, and `go` on the path (`AGENTS.md`).
- **Formatting and vet.** Format Go with `$(go env GOROOT)/bin/gofmt -w`. Vet what you touch with all four of:
  - `go vet ./tools/tool-fluency/...`
  - `go vet -tags evenerfuzz ./tools/tool-fluency/...`
  - `GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/...`
  - `GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...`
- **Existing probes keep working.** New manifest fields default to off, so today's probes behave exactly as before.
- **Voice of new prose.** Everything new here follows the spec's voice: task prompts, the README, the rubric, persona text, and the rewritten prompt. That means no em dashes, no bolded labels, no arrows, no capitals for emphasis, and names in place of identifiers.
- **Live runs go through lunarouter.** Keys stay in evener's credential store and never appear on a command line.
- **The lab branch never merges.** On it, `main`'s older prose-pinning tests fail once the prose changes. That is expected there; leave them.
- **Every run uses the openai-chat surface.** All models reach evener through lunarouter, so these runs never exercise the anthropic surface's prompt text. The landing plan covers that surface.

## Review Focus

1. A check command that waits on input or never exits must fail its check within the timeout, and the run must go on. Task 2 pins it with `TestRunCheckStopsAHungCommand`.
2. A fixture repository must commit even when the machine's global git config signs commits or installs hooks, and the agent's own commits must work without a global identity. Task 1 pins it with `TestMaterializeFixtureGitCommitsFilesAndLeavesUntrackedOut`.
3. A result-tool call whose arguments are not valid JSON must be skipped by the prose counter without failing the run. Task 4 pins it with `TestExtractRunProseSplitsRootAndDelegateProse`.
4. A blind review packet must not reveal its prompt version through run paths, which contain the version label. Task 5 pins it with `TestWriteReviewPackMasksRunPaths`.
5. A misspelled field in a task manifest must fail loudly. The runner decodes manifests leniently, so without that check a typo would silently switch a check off. Task 6 pins it by decoding every task strictly in `TestPromptEvalTasks`.

## Working in this worktree

This session's worktree holds three branches:
- part 1's pull request (#2562) on `claude/evener-system-prompt-cleanup-69b0b3`;
- this plan's tooling on `claude/prompt-rewrite-part2`;
- the experiments on `claude/prompt-rewrite-part2-lab`.

A hook forbids writing into other worktrees from this session, so switch branches here instead. Commit before every switch. Before acting on an Auto-fix event for #2562, run `git branch --show-current` and switch to its branch.

Lab outputs live in `tools/prompt-eval/results/lab/`. The root `.gitignore` ignores every `results/` directory, so they survive branch switches untracked.

---

## Tooling

### Task 1: Git fixtures with untracked files

**Files:**
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go` (`fixtureSpec` near line 205; `materializeFixture` near line 1048)
- Create: `tools/tool-fluency/cmd/evener-fluency/fixture_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `fixtureSpec` gains `Git bool` (yaml `git`) and `Untracked map[string]string` (yaml `untracked`). New `writeFixtureFiles(workDir string, files map[string]string) error` and `commitFixture(workDir string) error`.

- [ ] **Step 1: Write the failing tests**

Create `tools/tool-fluency/cmd/evener-fluency/fixture_test.go`:

```go
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaterializeFixtureGitCommitsFilesAndLeavesUntrackedOut: a git fixture is
// a repository on main whose one commit holds the fixture files, with the
// untracked files written after it, and with a local identity so the agent's
// own commits work on a machine that has none.
func TestMaterializeFixtureGitCommitsFilesAndLeavesUntrackedOut(t *testing.T) {
	t.Parallel()
	work := filepath.Join(t.TempDir(), "work")
	err := materializeFixture(work, fixtureSpec{
		Files:     map[string]string{"main.go": "package main\n"},
		Git:       true,
		Untracked: map[string]string{"notes.txt": "mine\n"},
	})
	if err != nil {
		t.Fatalf("materializeFixture: %v", err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"branch", "--show-current"}, "main"},
		{[]string{"ls-files"}, "main.go"},
		{[]string{"status", "--porcelain"}, "?? notes.txt"},
		{[]string{"rev-list", "--count", "HEAD"}, "1"},
		{[]string{"config", "user.email"}, "fixture@evener.test"},
		{[]string{"config", "core.hooksPath"}, ".git/hooks"},
	} {
		if got := fixtureGit(t, work, c.args...); got != c.want {
			t.Errorf("git %s = %q, want %q", strings.Join(c.args, " "), got, c.want)
		}
	}
}

func TestMaterializeFixtureRejectsUntrackedPathEscape(t *testing.T) {
	t.Parallel()
	err := materializeFixture(filepath.Join(t.TempDir(), "work"), fixtureSpec{
		Untracked: map[string]string{"../escape": "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes workdir") {
		t.Fatalf("err = %v, want a path-escape error", err)
	}
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestMaterializeFixture' -count=1`
Expected: FAIL to compile, with `unknown field Git in struct literal of type fixtureSpec`.

- [ ] **Step 3: Implement**

In `main.go`, replace `fixtureSpec` with:

```go
type fixtureSpec struct {
	Files map[string]string `yaml:"files"`
	// Git makes the work directory a repository on branch main whose first
	// commit holds Files, so a task can check what the agent changed.
	Git bool `yaml:"git,omitempty"`
	// Untracked files are written after that commit, so a task can check
	// that the agent leaves unrelated work alone.
	Untracked map[string]string `yaml:"untracked,omitempty"`
}
```

Replace `materializeFixture` with these three functions:

```go
func materializeFixture(workDir string, fixture fixtureSpec) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	if err := writeFixtureFiles(workDir, fixture.Files); err != nil {
		return err
	}
	if fixture.Git {
		if err := commitFixture(workDir); err != nil {
			return err
		}
	}
	return writeFixtureFiles(workDir, fixture.Untracked)
}

// writeFixtureFiles writes each file under workDir and refuses a path that
// escapes it.
func writeFixtureFiles(workDir string, files map[string]string) error {
	for rel, content := range files {
		path := filepath.Join(workDir, filepath.Clean(rel))
		within, err := filepath.Rel(workDir, path)
		if err != nil || strings.HasPrefix(within, "..") || filepath.IsAbs(within) {
			return fmt.Errorf("fixture path escapes workdir: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// commitFixture makes workDir a repository on main with one commit holding
// everything written so far. The identity lives in the repository, so the
// agent's own commits work on a machine with no global identity. The
// repository never signs and uses its own hooks directory, so the user's
// global signing and hooks, which belong to their own work, stay out of it.
func commitFixture(workDir string) error {
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Evener Fixture"},
		{"config", "user.email", "fixture@evener.test"},
		{"config", "commit.gpgsign", "false"},
		{"config", "core.hooksPath", ".git/hooks"},
		{"add", "-A"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -count=1`
Expected: `ok`, including every existing test.

- [ ] **Step 5: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/fixture_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/fixture_test.go
git commit -m "feat(tool-fluency): git fixtures with untracked files"
```

### Task 2: Outcome checks, call caps, and tolerated tool errors

**Files:**
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go` (`expectSpec` near line 209, `evaluateExpectations` near line 1162, imports)
- Create: `tools/tool-fluency/cmd/evener-fluency/expect_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `expectSpec` gains `MaxCalls map[string]int` (yaml `max_calls`), `Checks []checkSpec` (yaml `checks`), and `AllowToolErrors bool` (yaml `allow_tool_errors`).
  - `type checkSpec struct{ Name string; Run string }`.
  - `const checkTimeout = 2 * time.Minute`.
  - `runCheck(workDir string, check checkSpec, timeout time.Duration) (bool, string)`.
  - `lastBytes(s string, n int) string`.

- [ ] **Step 1: Write the failing tests**

Create `tools/tool-fluency/cmd/evener-fluency/expect_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvaluateExpectationsReportsFailedChecks(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "ok.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := probeFile{Expect: expectSpec{Checks: []checkSpec{
		{Name: "ok exists", Run: "test -f ok.txt"},
		{Name: "missing exists", Run: "test -f missing.txt"},
	}}}
	got := evaluateExpectations(work, probe, probeResult{})
	if len(got) != 1 || got[0].Category != "outcome" || got[0].Title != "check failed: missing exists" {
		t.Fatalf("findings = %+v, want one failed check named missing exists", got)
	}
}

// TestRunCheckStopsAHungCommand: a check that never exits fails as a timeout
// soon after its deadline, so one bad check cannot stall a run.
func TestRunCheckStopsAHungCommand(t *testing.T) {
	t.Parallel()
	start := time.Now()
	ok, detail := runCheck(t.TempDir(), checkSpec{Name: "hang", Run: "sleep 30"}, 200*time.Millisecond)
	if ok || !strings.Contains(detail, "timed out") {
		t.Fatalf("runCheck = %v, %q; want a timeout failure", ok, detail)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("runCheck returned after %s for a 200ms timeout", elapsed)
	}
}

func TestEvaluateExpectationsCapsToolCalls(t *testing.T) {
	t.Parallel()
	probe := probeFile{Expect: expectSpec{MaxCalls: map[string]int{"job_status": 1}}}
	within := evaluateExpectations(t.TempDir(), probe, probeResult{CanonicalToolCounts: map[string]int{"job_status": 1}})
	over := evaluateExpectations(t.TempDir(), probe, probeResult{CanonicalToolCounts: map[string]int{"job_status": 3}})
	if len(within) != 0 {
		t.Fatalf("at the cap: findings = %+v, want none", within)
	}
	if len(over) != 1 || over[0].Category != "churn" {
		t.Fatalf("over the cap: findings = %+v, want one churn finding", over)
	}
}

func TestEvaluateExpectationsAllowToolErrors(t *testing.T) {
	t.Parallel()
	res := probeResult{ToolErrors: map[string]int{"read_file": 2}}
	strict := evaluateExpectations(t.TempDir(), probeFile{}, res)
	lenient := evaluateExpectations(t.TempDir(), probeFile{Expect: expectSpec{AllowToolErrors: true}}, res)
	if len(strict) != 1 || strict[0].Category != "arguments" {
		t.Fatalf("strict: findings = %+v, want one arguments finding", strict)
	}
	if len(lenient) != 0 {
		t.Fatalf("lenient: findings = %+v, want none", lenient)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestEvaluateExpectations|TestRunCheck' -count=1`
Expected: FAIL to compile, with `unknown field Checks in struct literal of type expectSpec`.

- [ ] **Step 3: Implement**

Add `"maps"` to `main.go`'s imports. Replace `expectSpec` with:

```go
type expectSpec struct {
	Calls          []expectedCall   `yaml:"calls"`
	ForbiddenCalls []string         `yaml:"forbidden_calls"`
	Artifacts      []artifactExpect `yaml:"artifacts"`
	FinalContains  []string         `yaml:"final_contains"`
	// MaxCalls caps how many times a tool may be called, by canonical name.
	MaxCalls map[string]int `yaml:"max_calls,omitempty"`
	// Checks are shell commands run in the work directory after the agent
	// finishes. Each must exit zero. They judge the outcome of the work.
	Checks []checkSpec `yaml:"checks,omitempty"`
	// AllowToolErrors keeps tool errors from failing the run. A realistic
	// task meets missing files and failing commands on its way to the outcome.
	AllowToolErrors bool `yaml:"allow_tool_errors,omitempty"`
}

// checkSpec is one outcome check: a named shell command.
type checkSpec struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// checkTimeout bounds one outcome check, so a command that waits on a
// terminal or on a child process cannot stall a run.
const checkTimeout = 2 * time.Minute

// runCheck runs one check in workDir with no stdin. It reports whether the
// check passed and, when it failed, why.
func runCheck(workDir string, check checkSpec, timeout time.Duration) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", check.Run)
	cmd.Dir = workDir
	// A killed check can leave a child holding the output pipe; stop waiting
	// for it shortly after the kill.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, ""
	}
	if ctx.Err() != nil {
		return false, fmt.Sprintf("timed out after %s", timeout)
	}
	return false, fmt.Sprintf("%v: %s", err, lastBytes(strings.TrimSpace(string(out)), 1500))
}

// lastBytes keeps the end of s, where a failing command says why it failed.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
```

In `evaluateExpectations`, insert after the `ForbiddenCalls` loop:

```go
	for _, name := range slices.Sorted(maps.Keys(probe.Expect.MaxCalls)) {
		limit := probe.Expect.MaxCalls[name]
		got := max(res.CanonicalToolCounts[name], res.ModelToolCounts[name])
		if got > limit {
			out = append(out, finding{
				Category: "churn",
				Title:    "tool called more often than the task allows",
				Detail:   fmt.Sprintf("%s calls=%d max=%d", name, got, limit),
			})
		}
	}
```

Insert after the `Artifacts` loop:

```go
	for _, check := range probe.Expect.Checks {
		if ok, detail := runCheck(workDir, check, checkTimeout); !ok {
			out = append(out, finding{
				Category: "outcome",
				Title:    "check failed: " + check.Name,
				Detail:   detail,
			})
		}
	}
```

Wrap the existing `for toolName, n := range res.ToolErrors` loop in `if !probe.Expect.AllowToolErrors { ... }`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -count=1`
Expected: `ok`.

- [ ] **Step 5: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/expect_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/expect_test.go
git commit -m "feat(tool-fluency): outcome checks, call caps, and tolerated tool errors"
```

### Task 3: Count writing tics and opaque identifiers

**Files:**
- Create: `tools/tool-fluency/cmd/evener-fluency/prose.go`
- Create: `tools/tool-fluency/cmd/evener-fluency/prose_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type proseCounts struct{ Words, EmDashes, Contrastive, BoldLabels, Headers, Arrows, Shouting, OpaqueIDs int }`, with JSON tags `words`, `em_dashes`, `contrastive`, `bold_labels`, `headers`, `arrows`, `shouting`, and `opaque_ids`.
  - `(*proseCounts).add(o proseCounts)`.
  - `countProse(text string) proseCounts`.

- [ ] **Step 1: Write the failing test**

Create `tools/tool-fluency/cmd/evener-fluency/prose_test.go`:

```go
package main

import "testing"

func TestCountProse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		text string
		want proseCounts
	}{
		{"plain", "The build passes now.", proseCounts{Words: 4}},
		{"em dash", "It works — mostly.", proseCounts{Words: 3, EmDashes: 1}},
		{"comma not", "It is a guide, not a rule.", proseCounts{Words: 7, Contrastive: 1}},
		{"isn't it's", "It isn't slow, it's blocked.", proseCounts{Words: 5, Contrastive: 1}},
		{"plain negation", "Do not merge yet.", proseCounts{Words: 4}},
		{"bold label, colon inside", "- **Status:** done", proseCounts{Words: 2, BoldLabels: 1}},
		{"bold label, colon outside", "1. **Status**: done", proseCounts{Words: 3, BoldLabels: 1}},
		{"bold without a label", "- done **now**", proseCounts{Words: 2}},
		{"header", "## Summary\nDone.", proseCounts{Words: 2, Headers: 1}},
		{"arrow", "A → B", proseCounts{Words: 2, Arrows: 1}},
		{"arrow in code", "Run `a -> b` now.", proseCounts{Words: 2}},
		{"shouting", "NEVER push. Never mind NASA.", proseCounts{Words: 5, Shouting: 1}},
		{"identifiers",
			"Fixed #123 in Task 4 and F2 at 3f9a2c1 via job_034OOL8H87Mq in session 034OOL8H87MqrpJOhRqsGI.",
			proseCounts{Words: 14, OpaqueIDs: 6}},
		{"not identifiers", "The decade deadbeef release 1.2.3 cost 1234567 dollars.", proseCounts{Words: 10}},
		{"fenced code", "Done.\n```\nx -> y — #12\n```", proseCounts{Words: 1}},
	} {
		if got := countProse(c.text); got != c.want {
			t.Errorf("%s: countProse(%q) = %+v, want %+v", c.name, c.text, got, c.want)
		}
	}
}

func TestProseCountsAdd(t *testing.T) {
	t.Parallel()
	a := proseCounts{Words: 3, EmDashes: 1, OpaqueIDs: 2}
	a.add(proseCounts{Words: 4, Contrastive: 1, OpaqueIDs: 1})
	if want := (proseCounts{Words: 7, EmDashes: 1, Contrastive: 1, OpaqueIDs: 3}); a != want {
		t.Fatalf("add = %+v, want %+v", a, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestCountProse|TestProseCountsAdd' -count=1`
Expected: FAIL to compile, with `undefined: proseCounts`.

- [ ] **Step 3: Implement**

Create `tools/tool-fluency/cmd/evener-fluency/prose.go`:

```go
package main

import (
	"regexp"
	"strings"
	"unicode"
)

// proseCounts tallies the writing tics and opaque identifiers in agent
// prose. The prose rewrite is judged by these counts per 1,000 words.
type proseCounts struct {
	Words       int `json:"words"`
	EmDashes    int `json:"em_dashes"`
	Contrastive int `json:"contrastive"`
	BoldLabels  int `json:"bold_labels"`
	Headers     int `json:"headers"`
	Arrows      int `json:"arrows"`
	Shouting    int `json:"shouting"`
	OpaqueIDs   int `json:"opaque_ids"`
}

func (c *proseCounts) add(o proseCounts) {
	c.Words += o.Words
	c.EmDashes += o.EmDashes
	c.Contrastive += o.Contrastive
	c.BoldLabels += o.BoldLabels
	c.Headers += o.Headers
	c.Arrows += o.Arrows
	c.Shouting += o.Shouting
	c.OpaqueIDs += o.OpaqueIDs
}

var (
	fencedCodeRe = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe = regexp.MustCompile("`[^`\n]*`")
	wordRe       = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'’_-]*`)
	emDashRe     = regexp.MustCompile(`—| – | -- `)
	// The three shapes of the "X, not Y" tic: "a guide, not a rule",
	// "not just X but Y", and "it isn't X, it's Y".
	contrastiveRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i),\s+not\s+(?:a |an |the |just |only |merely )?[\p{L}\p{N}]`),
		regexp.MustCompile(`(?i)\bnot\s+(?:just|only|merely)\b[^.;:!?\n]{0,60}?\bbut\b`),
		regexp.MustCompile(`(?i)\b(?:isn't|is not|aren't|are not|wasn't|was not)\b[^.;:!?\n]{0,60}?[,;—]\s*(?:it's|it is|they're|they are|that's|this is)\b`),
	}
	boldLabelRe = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+\*\*[^*\n]+(?::\*\*|\*\*\s*[:—–-])`)
	headerRe    = regexp.MustCompile(`(?m)^#{1,6}\s`)
	arrowRe     = regexp.MustCompile(`→|⇒|->|=>`)
	shoutingRe  = regexp.MustCompile(`\b(?:NEVER|ALWAYS|MUST|CRITICAL|IMPORTANT|NOT|ONLY)\b`)
	opaqueIDRes = []*regexp.Regexp{
		regexp.MustCompile(`#\d+\b`),                                     // issue and pull request numbers
		regexp.MustCompile(`\b(?:Task|Step|Phase|Item|Finding)\s+\d+\b`), // numbered work items
		regexp.MustCompile(`\b[A-Z]{1,2}\d{1,3}\b`),                      // short codes such as T3 or D23
		regexp.MustCompile(`\b(?:job|dlg|watch)_[A-Za-z0-9_]{6,}\b`),     // evener job, delegate, and watch ids
	}
	hexRe    = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	base62Re = regexp.MustCompile(`\b[0-9A-Za-z]{22}\b`)
)

// countProse counts one piece of agent prose. Tics and words are counted
// outside code, since code spans hold commands and output. Identifiers are
// counted in inline code too, because an id in backticks is just as opaque
// to the reader; only fenced blocks are skipped.
func countProse(text string) proseCounts {
	noFences := fencedCodeRe.ReplaceAllString(text, " ")
	prose := inlineCodeRe.ReplaceAllString(noFences, " ")
	c := proseCounts{
		Words:      len(wordRe.FindAllString(prose, -1)),
		EmDashes:   len(emDashRe.FindAllString(prose, -1)),
		BoldLabels: len(boldLabelRe.FindAllString(prose, -1)),
		Headers:    len(headerRe.FindAllString(prose, -1)),
		Arrows:     len(arrowRe.FindAllString(prose, -1)),
		Shouting:   len(shoutingRe.FindAllString(prose, -1)),
	}
	for _, re := range contrastiveRes {
		c.Contrastive += len(re.FindAllString(prose, -1))
	}
	for _, re := range opaqueIDRes {
		c.OpaqueIDs += len(re.FindAllString(noFences, -1))
	}
	// A commit hash mixes digits and letters; a word or a number alone does not.
	for _, m := range hexRe.FindAllString(noFences, -1) {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsAny(m, "abcdef") {
			c.OpaqueIDs++
		}
	}
	for _, m := range base62Re.FindAllString(noFences, -1) {
		if hasDigitUpperLower(m) {
			c.OpaqueIDs++
		}
	}
	return c
}

// hasDigitUpperLower reports whether s mixes digits with upper- and lowercase
// letters, the shape of a session id.
func hasDigitUpperLower(s string) bool {
	var digit, upper, lower bool
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		}
	}
	return digit && upper && lower
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestCountProse|TestProseCountsAdd' -count=1 -v`
Expected: PASS. A row that fails on word count alone means that row's arithmetic is wrong. Fix the row's `want` only after counting that row's words by hand, and ledger the correction.

- [ ] **Step 5: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/prose.go tools/tool-fluency/cmd/evener-fluency/prose_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/prose.go tools/tool-fluency/cmd/evener-fluency/prose_test.go
git commit -m "feat(tool-fluency): count writing tics and opaque identifiers"
```

### Task 4: Prose from a run, and the prose-stats and prose-count commands

**Files:**
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go` (`walkTranscripts` near line 1026; `run` and `usage` near line 45)
- Create: `tools/tool-fluency/cmd/evener-fluency/prose_stats.go`
- Create: `tools/tool-fluency/cmd/evener-fluency/prose_stats_test.go`

**Interfaces:**
- Consumes:
  - `countProse`, `proseCounts.add` (Task 3);
  - `rootSessionID(stateDir string) (string, error)`;
  - `probeResult`;
  - `doctor.TranscriptResult`, `doctor.TranscriptOpts`, `doctor.TextMaxFull`.
- Produces:
  - `walkTranscriptsWith(stateDir string, opts doctor.TranscriptOpts, fn func(doctor.TranscriptResult) error) error`.
  - `type runProse struct{ ToUser, All []string }` and `extractRunProse(stateDir string) (runProse, error)`.
  - `resultMessages(arguments string) []string`.
  - `type labeledDir struct{ Label, Dir string }`, `parseLabeled(s string) (string, string, error)`, and `parseLabeledDirs(values []string) ([]labeledDir, error)`.
  - `type loadedResult struct{ Path string; Result probeResult }` and `loadResults(dir string) ([]loadedResult, error)`.
  - `type proseStats struct{...}`, `summarizeProse(dirs []labeledDir) ([]proseStats, error)`, and `renderProseTable(w io.Writer, stats []proseStats, channel string) error`.
  - Subcommands `prose-stats` and `prose-count`.

- [ ] **Step 1: Write the failing tests**

Create `tools/tool-fluency/cmd/evener-fluency/prose_stats_test.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

const (
	proseRootID  = "02wMz5Txv1C3Hut0M8GCeB"
	proseChildID = "02wMz5Txv2enqVTitaig6F"
)

func writeFluencyMeta(t *testing.T, stateDir, id, parent string, created time.Time) {
	t.Helper()
	meta := schema.SessionMeta{ID: id, IsSubagent: parent != "", ParentSessionID: parent, CreatedAt: created}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "sessions", id+".meta.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assistantTurn(parts ...llm.ContentPart) schema.Turn {
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: parts})
}

func textPart(s string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: s}
}

// writeProseRun writes a root session that says rootMessage through its
// result tool and a delegate that reports "Child report.".
func writeProseRun(t *testing.T, stateDir, rootMessage string) {
	t.Helper()
	now := time.Now()
	writeFluencyMeta(t, stateDir, proseRootID, "", now)
	writeFluencyMeta(t, stateDir, proseChildID, proseRootID, now.Add(time.Second))
	message, _ := json.Marshal(map[string]any{"message": rootMessage, "end_turn": true})
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(textPart("Looking at the tests."), fluencyToolCall("communicate", string(message))),
		// Arguments that were not valid JSON: the transcript keeps the raw text.
		assistantTurn(llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "call_bad", Name: "communicate", Arguments: json.RawMessage(`{}`), RawArguments: "{not json",
		}}),
	})
	writeFluencyTranscript(t, stateDir, proseChildID, []schema.Turn{
		assistantTurn(fluencyToolCall("communicate", `{"message":"Child report.","end_turn":true}`)),
	})
}

// TestExtractRunProseSplitsRootAndDelegateProse: the user sees only the
// root's result-tool messages; the whole run's prose adds visible assistant
// text and delegate reports; a call with unparseable arguments is skipped.
func TestExtractRunProseSplitsRootAndDelegateProse(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	writeProseRun(t, stateDir, "Fixed — see #12.")
	p, err := extractRunProse(stateDir)
	if err != nil {
		t.Fatalf("extractRunProse: %v", err)
	}
	if want := []string{"Fixed — see #12."}; !slices.Equal(p.ToUser, want) {
		t.Errorf("ToUser = %q, want %q", p.ToUser, want)
	}
	for _, want := range []string{"Looking at the tests.", "Fixed — see #12.", "Child report."} {
		if !slices.Contains(p.All, want) {
			t.Errorf("All = %q, missing %q", p.All, want)
		}
	}
	if len(p.All) != 3 {
		t.Errorf("All = %q, want exactly 3 pieces", p.All)
	}
}

func TestResultMessagesReadsBothFieldsOnce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args string
		want []string
	}{
		{`{"message":"a"}`, []string{"a"}},
		{`{"message":"a","output":{"message":"b"}}`, []string{"a", "b"}},
		{`{"message":"a","output":{"message":"a"}}`, []string{"a"}},
		{`{"message":"a","output":"plain"}`, []string{"a"}},
		{`{not json`, nil},
	} {
		if got := resultMessages(c.args); !slices.Equal(got, c.want) {
			t.Errorf("resultMessages(%s) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestSummarizeProseGroupsByLabelAndModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	writeProseRun(t, stateDir, "Done — see #12.")
	for rep, status := range map[int]string{1: "passed", 2: "failed"} {
		res := probeResult{Probe: "prose.bugfix-tally", Model: "m", Repetition: rep, Status: status, StateDir: stateDir}
		data, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "m", "prose.bugfix-tally", fmt.Sprintf("rep-%02d", rep), "result.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := summarizeProse([]labeledDir{{Label: "baseline", Dir: dir}})
	if err != nil {
		t.Fatalf("summarizeProse: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want one row", stats)
	}
	s := stats[0]
	if s.Label != "baseline" || s.Model != "m" || s.Runs != 2 || s.Passed != 1 || s.Tasks != 1 || s.TasksAllPassed != 0 {
		t.Errorf("row = %+v, want baseline/m with 2 runs, 1 passed, 1 task, 0 all-passed", s)
	}
	if s.Messages != 2 || s.MedianMessageWords != 3 || s.ToUser.EmDashes != 2 || s.ToUser.OpaqueIDs != 2 {
		t.Errorf("row = %+v, want 2 messages of 3 words with 2 em dashes and 2 ids", s)
	}
	if s.All.Words <= s.ToUser.Words {
		t.Errorf("All.Words = %d, want more than ToUser.Words = %d", s.All.Words, s.ToUser.Words)
	}
}

func TestParseLabeledNeedsBothParts(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "label", "=dir", "label="} {
		if _, _, err := parseLabeled(bad); err == nil {
			t.Errorf("parseLabeled(%q) succeeded, want an error", bad)
		}
	}
	if label, value, err := parseLabeled("v1-A=/tmp/x"); err != nil || label != "v1-A" || value != "/tmp/x" {
		t.Errorf("parseLabeled = %q, %q, %v", label, value, err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestExtractRunProse|TestResultMessages|TestSummarizeProse|TestParseLabeled' -count=1`
Expected: FAIL to compile, with `undefined: extractRunProse`.

- [ ] **Step 3: Implement**

In `main.go`, turn `walkTranscripts` into a wrapper and move its body into `walkTranscriptsWith`:

```go
func walkTranscripts(stateDir string, fn func(doctor.TranscriptResult) error) error {
	return walkTranscriptsWith(stateDir, doctor.TranscriptOpts{}, fn)
}

// walkTranscriptsWith is walkTranscripts with render options, for callers
// that need each turn's whole text.
func walkTranscriptsWith(stateDir string, opts doctor.TranscriptOpts, fn func(doctor.TranscriptResult) error) error {
	matches, err := filepath.Glob(filepath.Join(stateDir, "sessions", "*.transcript.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	for _, path := range matches {
		id, ok := strings.CutSuffix(filepath.Base(path), ".transcript.jsonl")
		if !ok || id == "" {
			continue
		}
		tr, err := runnerReadTranscript(stateDir, id, opts)
		if err != nil {
			return err
		}
		if err := fn(tr); err != nil {
			return err
		}
	}
	return nil
}
```

Keep `walkTranscripts`'s existing doc comment on the wrapper.

Create `tools/tool-fluency/cmd/evener-fluency/prose_stats.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmdutil"
)

// runProse is the agent prose one run produced. ToUser holds what the root
// session sent the user through its result tool. All adds every assistant
// text turn and every result-tool message from every session, delegates
// included.
type runProse struct {
	ToUser []string
	All    []string
}

// extractRunProse reads the prose out of every transcript in a run's state
// directory.
func extractRunProse(stateDir string) (runProse, error) {
	rootID, err := rootSessionID(stateDir)
	if err != nil {
		return runProse{}, err
	}
	var p runProse
	err = walkTranscriptsWith(stateDir, doctor.TranscriptOpts{TextMax: doctor.TextMaxFull}, func(tr doctor.TranscriptResult) error {
		for _, turn := range tr.Turns {
			if turn.Kind != string(schema.TurnAssistant) {
				continue
			}
			if text := strings.TrimSpace(turn.Text); text != "" {
				p.All = append(p.All, text)
			}
			for _, call := range turn.ToolCalls {
				if !call.IsResult && call.Name != "communicate" {
					continue
				}
				for _, msg := range resultMessages(call.Arguments) {
					p.All = append(p.All, msg)
					if tr.SessionID == rootID {
						p.ToUser = append(p.ToUser, msg)
					}
				}
			}
		}
		return nil
	})
	return p, err
}

// resultMessages returns the visible text of one result-tool call: its
// message, and its output.message when that says something else. Arguments
// that are not valid JSON carry no message anyone saw.
func resultMessages(arguments string) []string {
	var args struct {
		Message string          `json:"message"`
		Output  json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil
	}
	var output struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(args.Output, &output) // output may be absent or not an object
	var out []string
	message := strings.TrimSpace(args.Message)
	if message != "" {
		out = append(out, message)
	}
	if m := strings.TrimSpace(output.Message); m != "" && m != message {
		out = append(out, m)
	}
	return out
}

// labeledDir is one results directory and the prompt version it measures.
type labeledDir struct {
	Label string
	Dir   string
}

// parseLabeled splits a LABEL=VALUE flag value.
func parseLabeled(s string) (string, string, error) {
	label, value, ok := strings.Cut(s, "=")
	label, value = strings.TrimSpace(label), strings.TrimSpace(value)
	if !ok || label == "" || value == "" {
		return "", "", fmt.Errorf("want LABEL=VALUE, got %q", s)
	}
	return label, value, nil
}

func parseLabeledDirs(values []string) ([]labeledDir, error) {
	dirs := make([]labeledDir, 0, len(values))
	for _, v := range values {
		label, dir, err := parseLabeled(v)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, labeledDir{Label: label, Dir: dir})
	}
	if len(dirs) == 0 {
		return nil, errors.New("at least one --results LABEL=DIR is required")
	}
	return dirs, nil
}

// loadedResult is one run's result and the file it came from.
type loadedResult struct {
	Path   string
	Result probeResult
}

// loadResults reads every result.json under dir: one run's output or a
// whole matrix.
func loadResults(dir string) ([]loadedResult, error) {
	var out []loadedResult
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "result.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var res probeResult
		if err := json.Unmarshal(data, &res); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, loadedResult{Path: path, Result: res})
		return nil
	})
	return out, err
}

// proseStats summarizes one prompt version on one model.
type proseStats struct {
	Label              string      `json:"label"`
	Model              string      `json:"model"`
	Runs               int         `json:"runs"`
	Passed             int         `json:"passed"`
	Tasks              int         `json:"tasks"`
	TasksAllPassed     int         `json:"tasks_all_passed"` // tasks that passed on every run
	Messages           int         `json:"messages"`         // root result-tool messages
	MedianMessageWords int         `json:"median_message_words"`
	ToUser             proseCounts `json:"to_user"`
	All                proseCounts `json:"all"`
	ProseErrors        int         `json:"prose_errors"` // runs whose transcripts could not be read
}

// summarizeProse groups runs by label and model and counts their prose.
func summarizeProse(dirs []labeledDir) ([]proseStats, error) {
	type key struct{ label, model string }
	rows := map[key]*proseStats{}
	messageWords := map[key][]int{}
	taskPasses := map[key]map[string]bool{} // probe -> passed on every run so far
	for _, d := range dirs {
		results, err := loadResults(d.Dir)
		if err != nil {
			return nil, err
		}
		for _, lr := range results {
			res := lr.Result
			k := key{d.Label, res.Model}
			row := rows[k]
			if row == nil {
				row = &proseStats{Label: d.Label, Model: res.Model}
				rows[k] = row
				taskPasses[k] = map[string]bool{}
			}
			row.Runs++
			passed := res.Status == "passed"
			if passed {
				row.Passed++
			}
			if prev, seen := taskPasses[k][res.Probe]; !seen {
				taskPasses[k][res.Probe] = passed
			} else {
				taskPasses[k][res.Probe] = prev && passed
			}
			p, err := extractRunProse(res.StateDir)
			if err != nil {
				row.ProseErrors++
				continue
			}
			for _, msg := range p.ToUser {
				c := countProse(msg)
				row.ToUser.add(c)
				row.Messages++
				messageWords[k] = append(messageWords[k], c.Words)
			}
			for _, piece := range p.All {
				row.All.add(countProse(piece))
			}
		}
	}
	out := make([]proseStats, 0, len(rows))
	for k, row := range rows {
		row.Tasks = len(taskPasses[k])
		for _, allPassed := range taskPasses[k] {
			if allPassed {
				row.TasksAllPassed++
			}
		}
		row.MedianMessageWords = median(messageWords[k])
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b proseStats) int {
		if c := strings.Compare(a.Label, b.Label); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out, nil
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	return s[len(s)/2]
}

func per1k(n, words int) float64 {
	if words == 0 {
		return 0
	}
	return float64(n) * 1000 / float64(words)
}

// renderProseTable prints one row per label and model for the chosen channel:
// to_user (what the root sent the user) or all (every session's prose).
func renderProseTable(w io.Writer, stats []proseStats, channel string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LABEL\tMODEL\tRUNS\tPASSED\tTASKS ALL PASSED\tMSGS/RUN\tMEDIAN MSG WORDS\tWORDS\tEM DASH/1K\tX-NOT-Y/1K\tBOLD LABEL/1K\tHEADER/1K\tARROW/1K\tSHOUT/1K\tIDS/1K\tPROSE ERRORS")
	for _, s := range stats {
		c := s.ToUser
		if channel == "all" {
			c = s.All
		}
		msgsPerRun := 0.0
		if s.Runs > 0 {
			msgsPerRun = float64(s.Messages) / float64(s.Runs)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d/%d\t%.1f\t%d\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%d\n",
			s.Label, s.Model, s.Runs, s.Passed, s.TasksAllPassed, s.Tasks, msgsPerRun, s.MedianMessageWords, c.Words,
			per1k(c.EmDashes, c.Words), per1k(c.Contrastive, c.Words), per1k(c.BoldLabels, c.Words),
			per1k(c.Headers, c.Words), per1k(c.Arrows, c.Words), per1k(c.Shouting, c.Words),
			per1k(c.OpaqueIDs, c.Words), s.ProseErrors)
	}
	return tw.Flush()
}

func runProseStats(args []string) error {
	fs := flag.NewFlagSet("prose-stats", flag.ContinueOnError)
	var results cmdutil.StringSliceFlag
	fs.Var(&results, "results", "LABEL=DIR of a run or matrix output (repeatable)")
	asJSON := fs.Bool("json", false, "emit JSON with both channels")
	channel := fs.String("channel", "to_user", "prose to tabulate: to_user or all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *channel != "to_user" && *channel != "all" {
		return errors.New("--channel must be to_user or all")
	}
	dirs, err := parseLabeledDirs(results)
	if err != nil {
		return err
	}
	stats, err := summarizeProse(dirs)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}
	return renderProseTable(os.Stdout, stats, *channel)
}

// runProseCount counts the prose in files, such as prompt sections.
func runProseCount(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: evener-fluency prose-count FILE...")
	}
	for _, path := range args {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c := countProse(string(data))
		fmt.Printf("%s\twords=%d em_dashes=%d contrastive=%d bold_labels=%d headers=%d arrows=%d shouting=%d opaque_ids=%d\n",
			path, c.Words, c.EmDashes, c.Contrastive, c.BoldLabels, c.Headers, c.Arrows, c.Shouting, c.OpaqueIDs)
	}
	return nil
}
```

In `main.go`'s `run`, add the two cases before `case "help"`:

```go
	case "prose-stats":
		return runProseStats(args[1:])
	case "prose-count":
		return runProseCount(args[1:])
```

Add these lines to the USAGE block in `usage()`:

```
  evener-fluency prose-stats --results LABEL=DIR [--results LABEL=DIR ...] [--channel to_user|all] [--json]
  evener-fluency prose-count FILE...
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -count=1`
Expected: `ok`.

- [ ] **Step 5: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/prose_stats.go tools/tool-fluency/cmd/evener-fluency/prose_stats_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/prose_stats.go tools/tool-fluency/cmd/evener-fluency/prose_stats_test.go
git commit -m "feat(tool-fluency): prose-stats and prose-count"
```

### Task 5: Blind review packets

**Files:**
- Create: `tools/tool-fluency/cmd/evener-fluency/review.go`
- Create: `tools/tool-fluency/cmd/evener-fluency/review_test.go`
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go` (`run`, `usage`)

**Interfaces:**
- Consumes:
  - `labeledDir`, `parseLabeledDirs`, `loadResults`, `loadedResult` (Task 4);
  - `rootSessionID`, `runnerReadTranscript`;
  - `doctor.RenderTranscript(r doctor.TranscriptResult, format string) string`.
- Produces:
  - `type reviewEntry struct{ Packet, Label, Model, Probe string; Repetition int; Result string }`.
  - `writeReviewPack(dirs []labeledDir, packetsDir, maskRoot string, rng *rand.Rand) ([]reviewEntry, error)`.
  - `maskRunDetails(text, maskRoot, label string) string`.
  - `pathWithin(dir, path string) (bool, error)`.
  - The subcommand `review-pack`.

- [ ] **Step 1: Write the failing tests**

Create `tools/tool-fluency/cmd/evener-fluency/review_test.go`:

```go
package main

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// TestWriteReviewPackMasksRunPaths: run paths contain the version label, and
// the transcript render truncates argument previews, which can cut a path in
// the middle of the label. So a packet carries no path under the mask root
// and no label, and only the key maps the packet back to its run.
func TestWriteReviewPackMasksRunPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "v1-A", "lunarouter-m", "prose.bugfix-tally", "rep-01")
	workDir, stateDir := filepath.Join(base, "work"), filepath.Join(base, "state")
	writeFluencyMeta(t, stateDir, proseRootID, "", time.Now())
	writeFluencyTranscript(t, stateDir, proseRootID, []schema.Turn{
		assistantTurn(
			textPart("I read "+workDir+"/tally/sum.go and found the loop."),
			fluencyToolCall("read_file", `{"file_path":"`+workDir+`/tally/sum.go"}`),
		),
	})
	res := probeResult{Probe: "prose.bugfix-tally", Model: "lunarouter/m", Repetition: 1, WorkDir: workDir, StateDir: stateDir}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "result.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	packets := filepath.Join(t.TempDir(), "packets")
	key, err := writeReviewPack([]labeledDir{{Label: "v1-A", Dir: filepath.Join(root, "v1-A")}}, packets, root, rand.New(rand.NewPCG(1, 1)))
	if err != nil {
		t.Fatalf("writeReviewPack: %v", err)
	}
	if len(key) != 1 || key[0].Label != "v1-A" || key[0].Probe != "prose.bugfix-tally" || key[0].Model != "lunarouter/m" {
		t.Fatalf("key = %+v, want one entry for v1-A", key)
	}
	packet, err := os.ReadFile(filepath.Join(packets, key[0].Packet))
	if err != nil {
		t.Fatal(err)
	}
	text := string(packet)
	for _, leak := range []string{"v1-A", workDir, stateDir, root} {
		if strings.Contains(text, leak) {
			t.Errorf("packet contains %q:\n%s", leak, text)
		}
	}
	if !strings.Contains(text, "found the loop") {
		t.Errorf("packet lost the transcript text:\n%s", text)
	}
}

func TestPathWithin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, c := range []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, "key.json"), true},
		{filepath.Join(dir, "sub", "key.json"), true},
		{filepath.Join(filepath.Dir(dir), "key.json"), false},
	} {
		got, err := pathWithin(dir, c.path)
		if err != nil || got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, %v; want %v", dir, c.path, got, err, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestWriteReviewPack|TestPathWithin' -count=1`
Expected: FAIL to compile, with `undefined: writeReviewPack`.

- [ ] **Step 3: Implement**

Create `tools/tool-fluency/cmd/evener-fluency/review.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/cmdutil"
)

// reviewEntry maps one blind packet back to its run. The key lives outside
// the packets directory, so a reviewer never sees it.
type reviewEntry struct {
	Packet     string `json:"packet"`
	Label      string `json:"label"`
	Model      string `json:"model"`
	Probe      string `json:"probe"`
	Repetition int    `json:"repetition"`
	Result     string `json:"result"`
}

// writeReviewPack renders each run's root transcript into packetsDir under a
// random name, masks everything that would reveal its prompt version, and
// returns the key. maskRoot is the directory the labeled results live under.
func writeReviewPack(dirs []labeledDir, packetsDir, maskRoot string, rng *rand.Rand) ([]reviewEntry, error) {
	if err := os.MkdirAll(packetsDir, 0o755); err != nil {
		return nil, err
	}
	used := map[string]bool{}
	var key []reviewEntry
	for _, d := range dirs {
		results, err := loadResults(d.Dir)
		if err != nil {
			return nil, err
		}
		for _, lr := range results {
			res := lr.Result
			rootID, err := rootSessionID(res.StateDir)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", lr.Path, err)
			}
			tr, err := runnerReadTranscript(res.StateDir, rootID, doctor.TranscriptOpts{Format: "markdown", TextMax: doctor.TextMaxFull})
			if err != nil {
				return nil, fmt.Errorf("%s: %w", lr.Path, err)
			}
			body := maskRunDetails(doctor.RenderTranscript(tr, "markdown"), maskRoot, d.Label)
			name := uniquePacketName(rng, used)
			content := fmt.Sprintf("# Task %s\n\n%s\n", res.Probe, body)
			if err := os.WriteFile(filepath.Join(packetsDir, name), []byte(content), 0o644); err != nil {
				return nil, err
			}
			key = append(key, reviewEntry{Packet: name, Label: d.Label, Model: res.Model, Probe: res.Probe, Repetition: res.Repetition, Result: lr.Path})
		}
	}
	slices.SortFunc(key, func(a, b reviewEntry) int { return strings.Compare(a.Packet, b.Packet) })
	return key, nil
}

// maskRunDetails removes what would reveal a packet's prompt version. Every
// path under maskRoot becomes <run>: run paths carry the version label, and a
// truncated argument preview can cut a path anywhere, so the whole path goes.
// The root's resolved form is masked too, for tools that print resolved
// paths. The label itself is masked last; pick labels that do not occur in
// ordinary prose, such as v1-A, so masking cannot change what the agent wrote.
func maskRunDetails(text, maskRoot, label string) string {
	roots := []string{filepath.Clean(maskRoot)}
	if resolved, err := filepath.EvalSymlinks(maskRoot); err == nil && resolved != roots[0] {
		roots = append(roots, resolved)
	}
	for _, root := range roots {
		re := regexp.MustCompile(regexp.QuoteMeta(root) + "[^\\s\"'`)\\]]*")
		text = re.ReplaceAllString(text, "<run>")
	}
	return strings.ReplaceAll(text, label, "<version>")
}

func uniquePacketName(rng *rand.Rand, used map[string]bool) string {
	for {
		name := fmt.Sprintf("%08x.md", rng.Uint32())
		if !used[name] {
			used[name] = true
			return name
		}
	}
}

// pathWithin reports whether path lies inside dir.
func pathWithin(dir, path string) (bool, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false, err
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func runReviewPack(args []string) error {
	fs := flag.NewFlagSet("review-pack", flag.ContinueOnError)
	var results cmdutil.StringSliceFlag
	fs.Var(&results, "results", "LABEL=DIR of a run or matrix output (repeatable)")
	packets := fs.String("packets", "", "directory for the blind packets")
	keyPath := fs.String("key", "", "file for the packet key; must be outside --packets")
	maskRoot := fs.String("mask-root", "", "directory every --results DIR lives under; paths below it are masked")
	seed := fs.Uint64("seed", uint64(time.Now().UnixNano()), "shuffle seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *packets == "" || *keyPath == "" || *maskRoot == "" {
		return errors.New("--packets, --key, and --mask-root are required")
	}
	if inside, err := pathWithin(*packets, *keyPath); err != nil {
		return err
	} else if inside {
		return errors.New("--key must be outside --packets, or reviewers would see it")
	}
	dirs, err := parseLabeledDirs(results)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if inside, err := pathWithin(*maskRoot, d.Dir); err != nil {
			return err
		} else if !inside {
			return fmt.Errorf("%s is outside --mask-root %s, so its paths would not be masked", d.Dir, *maskRoot)
		}
	}
	key, err := writeReviewPack(dirs, *packets, *maskRoot, rand.New(rand.NewPCG(*seed, *seed)))
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*keyPath, append(data, '\n'), 0o644)
}
```

In `main.go`'s `run`, add `case "review-pack": return runReviewPack(args[1:])`. Add this usage line:

```
  evener-fluency review-pack --results LABEL=DIR [...] --mask-root DIR --packets DIR --key FILE [--seed N]
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -count=1`
Expected: `ok`. If `TestWriteReviewPackMasksRunPaths` finds a leak, read the packet it prints, extend the mask to the form it shows, and rerun. Never loosen the test.

- [ ] **Step 5: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/review.go tools/tool-fluency/cmd/evener-fluency/review_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/review.go tools/tool-fluency/cmd/evener-fluency/review_test.go
git commit -m "feat(tool-fluency): blind review packets"
```

### Task 6: The evaluation tasks

**Files:**
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go` (`probeFile` near line 150)
- Create: `tools/prompt-eval/README.md`
- Create: `tools/prompt-eval/rubric.md`
- Create: nine manifests in `tools/prompt-eval/tasks/`:
  - `smoke.yaml`
  - `bugfix-tally.yaml`
  - `investigate-cache.yaml`
  - `delegate-textutil.yaml`
  - `background-suite.yaml`
  - `git-greeting.yaml`
  - `research-proposals.yaml`
  - `ambiguous-export.yaml`
  - `handback-wordfreq.yaml`
- Create: `tools/tool-fluency/cmd/evener-fluency/prompt_eval_tasks_test.go`

**Interfaces:**
- Consumes: `fixtureSpec.Git`, `fixtureSpec.Untracked` (Task 1); `checkSpec`, `runCheck`, `checkTimeout` (Task 2).
- Produces: `probeFile.Reference string` (yaml `reference`). This is a shell script that solves the task. Only the offline test runs it; the runner ignores it.

- [ ] **Step 1: Write the failing test**

Create `tools/tool-fluency/cmd/evener-fluency/prompt_eval_tasks_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// promptEvalTasksDir holds the prompt rewrite's evaluation tasks.
const promptEvalTasksDir = "../../../prompt-eval/tasks"

// TestPromptEvalTasks checks every evaluation task offline. Each manifest
// decodes with no unknown fields, so a misspelled field cannot silently turn
// a check off. When a task has a reference solution, its checks fail on the
// untouched fixture and pass after the solution. A task with no reference
// asks for no change, so its checks pass untouched. A task whose checks pass
// before the agent acts, or that no solution can pass, would measure nothing.
func TestPromptEvalTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("task checks run under bash")
	}
	paths, err := filepath.Glob(filepath.Join(promptEvalTasksDir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no tasks under %s", promptEvalTasksDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			probe := decodeTaskStrict(t, path)
			if !strings.HasPrefix(probe.ID, "prose.") {
				t.Errorf("id %q, want the prose. prefix", probe.ID)
			}
			if strings.TrimSpace(probe.Prompt) == "" {
				t.Fatal("empty prompt")
			}
			for _, c := range probe.Expect.Checks {
				if c.Name == "" || c.Run == "" {
					t.Fatalf("check %+v needs a name and a command", c)
				}
			}
			if len(probe.Expect.Checks) == 0 {
				return
			}
			untouched := filepath.Join(t.TempDir(), "work")
			if err := materializeFixture(untouched, probe.Fixture); err != nil {
				t.Fatal(err)
			}
			failing := failingChecks(untouched, probe.Expect.Checks)
			if probe.Reference == "" {
				if len(failing) > 0 {
					t.Fatalf("checks %v fail on the untouched fixture, and the task has no reference solution", failing)
				}
				return
			}
			if len(failing) == 0 {
				t.Fatal("every check passes before the agent acts, so the task measures nothing")
			}
			solved := filepath.Join(t.TempDir(), "work")
			if err := materializeFixture(solved, probe.Fixture); err != nil {
				t.Fatal(err)
			}
			if ok, detail := runCheck(solved, checkSpec{Name: "reference", Run: probe.Reference}, 3*time.Minute); !ok {
				t.Fatalf("reference solution failed: %s", detail)
			}
			if failing := failingChecks(solved, probe.Expect.Checks); len(failing) > 0 {
				t.Fatalf("checks %v fail after the reference solution", failing)
			}
		})
	}
}

func decodeTaskStrict(t *testing.T, path string) probeFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var probe probeFile
	if err := dec.Decode(&probe); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return probe
}

func failingChecks(workDir string, checks []checkSpec) []string {
	var names []string
	for _, c := range checks {
		if ok, _ := runCheck(workDir, c, checkTimeout); !ok {
			names = append(names, c.Name)
		}
	}
	return names
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestPromptEvalTasks' -count=1`
Expected: FAIL to compile, with `probe.Reference undefined`.

- [ ] **Step 3: Add the reference field**

In `main.go`'s `probeFile`, add after `Skip`:

```go
	// Reference is a shell script that solves the task. Only the offline
	// task test runs it, to prove the checks can pass; the runner ignores it.
	Reference string `yaml:"reference,omitempty"`
```

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestPromptEvalTasks' -count=1`
Expected: FAIL with `no tasks under ../../../prompt-eval/tasks`.

- [ ] **Step 4: Write the tasks**

Every task prompt is written the way a colleague would ask. Go files in the fixtures indent with tabs inside the YAML block scalars; the block itself is indented with spaces.

`tools/prompt-eval/tasks/smoke.yaml`:

```yaml
schema: 1
id: prose.smoke
tool: prompt-eval
prompt: |
  Reply with exactly SMOKE_OK and finish.
expect:
  final_contains: ["SMOKE_OK"]
```

`tools/prompt-eval/tasks/bugfix-tally.yaml`:

```yaml
schema: 1
id: prose.bugfix-tally
tool: prompt-eval
prompt: |
  The totals in our weekly report are wrong. The report adds up numbers with tally.Sum, and the tests in this repository catch it too. Find the bug, fix it, and tell me what was wrong.
fixture:
  git: true
  files:
    go.mod: |
      module example.com/tally

      go 1.22
    tally/sum.go: |
      package tally

      // Sum adds up every value in xs.
      func Sum(xs []int) int {
      	total := 0
      	for i := 1; i < len(xs); i++ {
      		total += xs[i]
      	}
      	return total
      }
    tally/sum_test.go: |
      package tally

      import "testing"

      func TestSum(t *testing.T) {
      	cases := []struct {
      		in   []int
      		want int
      	}{
      		{nil, 0},
      		{[]int{5}, 5},
      		{[]int{1, 2, 3}, 6},
      	}
      	for _, c := range cases {
      		if got := Sum(c.in); got != c.want {
      			t.Errorf("Sum(%v) = %d, want %d", c.in, got, c.want)
      		}
      	}
      }
expect:
  allow_tool_errors: true
  checks:
    - name: tests pass
      run: go test ./...
    - name: tests unchanged
      run: git diff --quiet HEAD -- tally/sum_test.go
reference: |
  cat > tally/sum.go <<'EOF'
  package tally

  // Sum adds up every value in xs.
  func Sum(xs []int) int {
  	total := 0
  	for _, x := range xs {
  		total += x
  	}
  	return total
  }
  EOF
```

`tools/prompt-eval/tasks/investigate-cache.yaml`:

```yaml
schema: 1
id: prose.investigate-cache
tool: prompt-eval
prompt: |
  Users say the page cache never seems to work in staging. Figure out why and write me a short explanation. Please don't change any files; I want to understand it first.
fixture:
  git: true
  files:
    go.mod: |
      module example.com/pages

      go 1.22
    main.go: |
      package main

      import (
      	"fmt"
      	"os"
      	"strconv"
      	"time"
      )

      // cacheTTL reads how long cached pages live. Zero turns the cache off.
      func cacheTTL() time.Duration {
      	secs, err := strconv.Atoi(os.Getenv("APP_CACHE_TTL"))
      	if err != nil || secs <= 0 {
      		return 0
      	}
      	return time.Duration(secs) * time.Second
      }

      func main() {
      	if ttl := cacheTTL(); ttl > 0 {
      		fmt.Printf("cache on, ttl %s\n", ttl)
      	} else {
      		fmt.Println("cache off")
      	}
      }
    deploy/staging.env: |
      PORT=8080
      CACHE_TTL=300
      LOG_LEVEL=info
    deploy/production.env: |
      PORT=8080
      APP_CACHE_TTL=300
      LOG_LEVEL=warn
    README.md: |
      # pages

      Serves the marketing pages. Each environment's settings live in deploy/<environment>.env.
expect:
  allow_tool_errors: true
  final_contains: ["APP_CACHE_TTL"]
  checks:
    - name: no files changed
      run: test -z "$(git status --porcelain)"
```

`tools/prompt-eval/tasks/delegate-textutil.yaml`:

```yaml
schema: 1
id: prose.delegate-textutil
tool: prompt-eval
prompt: |
  Three functions in textutil are stubs: Reverse, CountWords, and Capitalize. Implement them so their tests pass. They don't depend on each other, so split the work however you like.
fixture:
  git: true
  files:
    go.mod: |
      module example.com/textutil

      go 1.22
    textutil/reverse.go: |
      package textutil

      // Reverse returns s with its characters in reverse order.
      func Reverse(s string) string {
      	return ""
      }
    textutil/words.go: |
      package textutil

      // CountWords returns how many whitespace-separated words s holds.
      func CountWords(s string) int {
      	return 0
      }
    textutil/capitalize.go: |
      package textutil

      // Capitalize upper-cases the first letter of every word in s.
      func Capitalize(s string) string {
      	return ""
      }
    textutil/textutil_test.go: |
      package textutil

      import "testing"

      func TestReverse(t *testing.T) {
      	for in, want := range map[string]string{"": "", "abc": "cba", "héllo": "olléh"} {
      		if got := Reverse(in); got != want {
      			t.Errorf("Reverse(%q) = %q, want %q", in, got, want)
      		}
      	}
      }

      func TestCountWords(t *testing.T) {
      	for in, want := range map[string]int{"": 0, "one": 1, "  two  words ": 2} {
      		if got := CountWords(in); got != want {
      			t.Errorf("CountWords(%q) = %d, want %d", in, got, want)
      		}
      	}
      }

      func TestCapitalize(t *testing.T) {
      	for in, want := range map[string]string{"": "", "hello world": "Hello World", "élan vital": "Élan Vital"} {
      		if got := Capitalize(in); got != want {
      			t.Errorf("Capitalize(%q) = %q, want %q", in, got, want)
      		}
      	}
      }
expect:
  allow_tool_errors: true
  checks:
    - name: tests pass
      run: go test ./...
    - name: tests unchanged
      run: git diff --quiet HEAD -- textutil/textutil_test.go
reference: |
  cat > textutil/reverse.go <<'EOF'
  package textutil

  // Reverse returns s with its characters in reverse order.
  func Reverse(s string) string {
  	r := []rune(s)
  	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
  		r[i], r[j] = r[j], r[i]
  	}
  	return string(r)
  }
  EOF
  cat > textutil/words.go <<'EOF'
  package textutil

  import "strings"

  // CountWords returns how many whitespace-separated words s holds.
  func CountWords(s string) int {
  	return len(strings.Fields(s))
  }
  EOF
  cat > textutil/capitalize.go <<'EOF'
  package textutil

  import (
  	"strings"
  	"unicode"
  )

  // Capitalize upper-cases the first letter of every word in s.
  func Capitalize(s string) string {
  	words := strings.Fields(s)
  	for i, w := range words {
  		r := []rune(w)
  		r[0] = unicode.ToUpper(r[0])
  		words[i] = string(r)
  	}
  	return strings.Join(words, " ")
  }
  EOF
```

`tools/prompt-eval/tasks/background-suite.yaml`:

```yaml
schema: 1
id: prose.background-suite
tool: prompt-eval
prompt: |
  Run the full test suite with `bash run-tests.sh`. It takes a minute or two. Tell me whether it passes.
fixture:
  files:
    run-tests.sh: |
      #!/usr/bin/env bash
      # The full suite takes about a minute and a half.
      set -euo pipefail
      echo "running 42 tests"
      sleep 90
      echo "all 42 tests passed"
expect:
  allow_tool_errors: true
  final_contains: ["42"]
  max_calls:
    job_status: 2
```

`tools/prompt-eval/tasks/git-greeting.yaml`:

```yaml
schema: 1
id: prose.git-greeting
tool: prompt-eval
prompt: |
  Create a branch named add-greeting. On it, add a function Greet(name string) string to greet.go that returns "Hello, <name>!", and commit that change. Then merge add-greeting into main with a merge commit. Leave everything else in the working tree as it is.
fixture:
  git: true
  files:
    go.mod: |
      module example.com/greet

      go 1.22
    greet.go: |
      package greet
    README.md: |
      # greet

      Greetings for the front desk.
  untracked:
    notes-local.txt: |
      my scratch notes, not for the repository
expect:
  allow_tool_errors: true
  checks:
    - name: main has Greet
      run: git show main:greet.go | grep -q 'func Greet(name string) string'
    - name: merge commit on main
      run: test "$(git rev-list --merges --count main)" -ge 1
    - name: branch exists
      run: git rev-parse --verify -q add-greeting
    - name: notes left untracked
      run: test -f notes-local.txt && test -z "$(git ls-files notes-local.txt)"
    - name: nothing else left over
      run: test "$(git status --porcelain)" = "?? notes-local.txt"
reference: |
  git switch -q -c add-greeting
  cat >> greet.go <<'EOF'

  // Greet returns a greeting for name.
  func Greet(name string) string {
  	return "Hello, " + name + "!"
  }
  EOF
  git commit -q -am "Add Greet"
  git switch -q main
  git merge -q --no-ff --no-edit add-greeting
```

`tools/prompt-eval/tasks/research-proposals.yaml`:

```yaml
schema: 1
id: prose.research-proposals
tool: prompt-eval
prompt: |
  Read the two proposals in docs/ along with docs/constraints.md, and recommend one. Write your recommendation to RECOMMENDATION.md. Its first line must be exactly "Recommendation: Option A" or "Recommendation: Option B", and the rest should explain the trade-off that decides it. Then tell me briefly what you picked and why.
fixture:
  files:
    docs/constraints.md: |
      # Constraints

      The tool runs on laptops at field sites that go without internet for days at a time.
      Two people maintain it, part time.
      There is no budget for hosted services.
    docs/option-a.md: |
      # Option A: a local notification table

      Job notifications go into a table in the local SQLite database, and the UI checks the table once a second.
      It works with no network, and backing up the database backs up the notifications.
      Checking once a second costs a little CPU and delays a notification by up to a second.
    docs/option-b.md: |
      # Option B: a hosted message service

      Job notifications go to a hosted publish-and-subscribe service, and the UI subscribes to it.
      Notifications arrive at once, and the service keeps them for a week.
      It needs a network connection and a paid account, and the client library adds 40 MB to the install.
expect:
  allow_tool_errors: true
  checks:
    - name: recommends the offline option
      run: head -n 1 RECOMMENDATION.md | grep -qx 'Recommendation: Option A'
reference: |
  printf 'Recommendation: Option A\n\nThe field sites go days without a network, and Option B needs one.\n' > RECOMMENDATION.md
```

`tools/prompt-eval/tasks/ambiguous-export.yaml`:

```yaml
schema: 1
id: prose.ambiguous-export
tool: prompt-eval
prompt: |
  The export is too slow. Make it faster.
fixture:
  git: true
  files:
    go.mod: |
      module example.com/export

      go 1.24
    export/export.go: |
      package export

      import "fmt"

      // Row is one line of the export.
      type Row struct {
      	Name  string
      	Count int
      }

      // CSV renders rows as comma-separated lines under a header.
      func CSV(rows []Row) string {
      	out := "name,count\n"
      	for _, r := range rows {
      		out += fmt.Sprintf("%s,%d\n", r.Name, r.Count)
      	}
      	return out
      }
    export/export_test.go: |
      package export

      import (
      	"strconv"
      	"testing"
      )

      func TestCSV(t *testing.T) {
      	got := CSV([]Row{{"apples", 3}, {"pears", 0}})
      	if want := "name,count\napples,3\npears,0\n"; got != want {
      		t.Fatalf("CSV = %q, want %q", got, want)
      	}
      }

      func BenchmarkCSV(b *testing.B) {
      	rows := make([]Row, 20000)
      	for i := range rows {
      		rows[i] = Row{Name: "item" + strconv.Itoa(i), Count: i}
      	}
      	for b.Loop() {
      		CSV(rows)
      	}
      }
expect:
  allow_tool_errors: true
  checks:
    - name: tests pass
      run: go test ./...
    - name: tests unchanged
      run: git diff --quiet HEAD -- export/export_test.go
    - name: export changed
      run: "! git diff --quiet HEAD -- export/export.go"
reference: |
  cat > export/export.go <<'EOF'
  package export

  import (
  	"strconv"
  	"strings"
  )

  // Row is one line of the export.
  type Row struct {
  	Name  string
  	Count int
  }

  // CSV renders rows as comma-separated lines under a header.
  func CSV(rows []Row) string {
  	var b strings.Builder
  	b.WriteString("name,count\n")
  	for _, r := range rows {
  		b.WriteString(r.Name)
  		b.WriteByte(',')
  		b.WriteString(strconv.Itoa(r.Count))
  		b.WriteByte('\n')
  	}
  	return b.String()
  }
  EOF
```

This fixture declares `go 1.24` because its benchmark uses `b.Loop`.

`tools/prompt-eval/tasks/handback-wordfreq.yaml`:

```yaml
schema: 1
id: prose.handback-wordfreq
tool: prompt-eval
prompt: |
  Build the wordfreq command into bin/wordfreq and leave it there so I can run it later. Make sure it works on sample.txt before you hand it back.
fixture:
  git: true
  files:
    go.mod: |
      module example.com/wordfreq

      go 1.22
    cmd/wordfreq/main.go: |
      package main

      import (
      	"fmt"
      	"os"
      	"sort"
      	"strings"
      )

      func main() {
      	if len(os.Args) != 2 {
      		fmt.Fprintln(os.Stderr, "usage: wordfreq FILE")
      		os.Exit(2)
      	}
      	data, err := os.ReadFile(os.Args[1])
      	if err != nil {
      		fmt.Fprintln(os.Stderr, err)
      		os.Exit(1)
      	}
      	counts := map[string]int{}
      	for _, w := range strings.Fields(strings.ToLower(string(data))) {
      		counts[w]++
      	}
      	words := make([]string, 0, len(counts))
      	for w := range counts {
      		words = append(words, w)
      	}
      	sort.Slice(words, func(i, j int) bool {
      		if counts[words[i]] != counts[words[j]] {
      			return counts[words[i]] > counts[words[j]]
      		}
      		return words[i] < words[j]
      	})
      	for _, w := range words {
      		fmt.Printf("%s %d\n", w, counts[w])
      	}
      }
    sample.txt: |
      the cat and the dog and the bird
expect:
  allow_tool_errors: true
  checks:
    - name: binary left in place and working
      run: test -x bin/wordfreq && ./bin/wordfreq sample.txt | head -n 1 | grep -qx 'the 3'
reference: |
  mkdir -p bin && go build -o bin/wordfreq ./cmd/wordfreq
```

- [ ] **Step 5: Write the rubric**

Create `tools/prompt-eval/rubric.md`:

```markdown
# Blind read rubric

Score each transcript on each line: 3 good, 2 acceptable, 1 poor, or n/a when the task does not exercise it. You do not know which prompt produced the transcript. Judge what the agent did and wrote, never what it says about its own reasons.

## Writing to the user

1. Outcome first. The first sentence of the final message says what happened.
2. Names over identifiers. Tasks, issues, files, and commits are named or described. The reader never has to look up an identifier.
3. Plain and light. Plain words, one idea per sentence, no stacked jargon. Someone who was not watching the session can follow it.
4. Length fits. A status update is short. The final report says what changed, how it was checked, and what is left, with no padding.
5. Updates earn their place. The agent writes when there is news and stays quiet otherwise.

## Behavior

6. Did the task. The requested outcome, at the requested scope.
7. Checked soundly. The checking fits the task, with no skipped checks and no ritual ones.
8. No wasted motion. No polling loops, repeated reads, or detours.
9. Handled what was unclear. When the request was ambiguous, the agent made a reasonable call and said what it assumed.
10. Left things right. Deliverables in place, unrelated work untouched, and nothing claimed that did not happen.

For each transcript, also write one sentence: the most important thing about it.
```

- [ ] **Step 6: Write the README**

Create `tools/prompt-eval/README.md`:

````markdown
# Prompt evaluation

These tasks measure how a system prompt shapes the way agents write and behave. They exist for the system prompt rewrite (`docs/superpowers/specs/2026-09-26-system-prompt-prose-rewrite-design.md`) and for later prompt work.

The tasks run on the tool-fluency runner. Each task is a manifest with a fixture, a prompt written the way a colleague would ask, and checks that judge the outcome of the work. No check reads the system prompt; the prose tools count what the agents wrote.

## The tasks

| Task | What it exercises |
| --- | --- |
| `prose.smoke` | The model answers at all. Run it first for every model. |
| `prose.bugfix-tally` | A bug fix the tests already catch, reported back in plain words. |
| `prose.investigate-cache` | An investigation that ends in a written explanation, with no changes. |
| `prose.delegate-textutil` | Three independent pieces of work that can be split across delegates. |
| `prose.background-suite` | A slow test run, waited on without polling. |
| `prose.git-greeting` | A branch, a commit, and a merge that leave unrelated files alone. |
| `prose.research-proposals` | Reading two proposals and recommending one in a written note. |
| `prose.ambiguous-export` | A vague request with no one to ask. |
| `prose.handback-wordfreq` | A deliverable that must be left in place and working. |

## Running

Build the runner and one evener binary for each prompt version:

```bash
go build -o /tmp/lab/evener-fluency ./tools/tool-fluency/cmd/evener-fluency
go build -o /tmp/lab/evener-baseline ./cmd/evener
```

Run every task on several models, three times each:

```bash
/tmp/lab/evener-fluency matrix \
  --version baseline=/tmp/lab/evener-baseline \
  --models lunarouter/deepseek-4.1-flash,lunarouter/glm-5.3-vision \
  --probes-dir tools/prompt-eval/tasks \
  --repetitions 3 --timeout 25m --max-concurrent 4 \
  --fast-cheap-model lunarouter/deepseek-4.1-flash \
  --out tools/prompt-eval/results/example
```

Count the prose each version produced:

```bash
/tmp/lab/evener-fluency prose-stats --results baseline=tools/prompt-eval/results/example/baseline
```

Make blind packets for a read against `rubric.md`. Keep the key somewhere the readers never look:

```bash
/tmp/lab/evener-fluency review-pack \
  --results baseline=tools/prompt-eval/results/example/baseline \
  --mask-root tools/prompt-eval/results/example \
  --packets /tmp/lab/review/packets --key /tmp/lab/review-key.json
```

Results land under `tools/prompt-eval/results/`, which git ignores.

## Adding a task

Write the manifest in `tasks/`, then run `go test ./tools/tool-fluency/cmd/evener-fluency/ -run TestPromptEvalTasks`. The test decodes every manifest strictly. When a task has a `reference` solution, the test proves its checks fail before the solution and pass after it. When a task asks for no change, the test proves its checks pass untouched.
````

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestPromptEvalTasks' -count=1 -v`
Expected: PASS for all nine subtests. `prose.smoke` and `prose.background-suite` return early, since they have no checks.

- [ ] **Step 8: Watch the test catch a broken task**

This proves the test measures something. Temporarily change the bugfix reference's `for _, x := range xs` to `for _, x := range xs[1:]`, run the test, and see `prose.bugfix-tally` fail with "checks [tests pass] fail after the reference solution". Revert. Then temporarily rename `allow_tool_errors` to `allow_tool_error` in one task and see strict decoding fail. Revert.

- [ ] **Step 9: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/prompt_eval_tasks_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/prompt_eval_tasks_test.go tools/prompt-eval
git commit -m "feat(prompt-eval): evaluation tasks for the prompt rewrite"
```

### Task 7: The matrix command, and the tooling pull request

**Files:**
- Create: `tools/tool-fluency/cmd/evener-fluency/matrix.go`
- Create: `tools/tool-fluency/cmd/evener-fluency/matrix_test.go`
- Modify: `tools/tool-fluency/cmd/evener-fluency/main.go` (`run`, `usage`)
- Modify: `tools/tool-fluency/README.md`: add one paragraph after "Quick Start" pointing to `tools/prompt-eval/README.md` for the matrix, prose-stats, and review-pack commands.

**Interfaces:**
- Consumes:
  - `runConfig`, `defineRunFlags`, `runSuiteWithConfig(cfg runConfig) error`, `safeName(s string) string`;
  - `parseLabeled` (Task 4).
- Produces:
  - `type matrixVersion struct{ Label, Bin string }`.
  - `matrixConfigs(base runConfig, versions []matrixVersion, models []string, out string) []runConfig`.
  - `var runMatrixSuite = runSuiteWithConfig`.
  - `runMatrix(cfgs []runConfig, maxConcurrent int) error`.
  - `runMatrixCommand(args []string) error`, registered as the `matrix` subcommand.

- [ ] **Step 1: Write the failing tests**

Create `tools/tool-fluency/cmd/evener-fluency/matrix_test.go`:

```go
package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMatrixConfigsExpandsVersionsByModels(t *testing.T) {
	t.Parallel()
	base := runConfig{repetitions: 3, probesDir: "tasks"}
	cfgs := matrixConfigs(base,
		[]matrixVersion{{Label: "baseline", Bin: "/b/base"}, {Label: "v1-A", Bin: "/b/a"}},
		[]string{"lunarouter/m1", "lunarouter/m2"}, "/out")
	if len(cfgs) != 4 {
		t.Fatalf("got %d configs, want 4", len(cfgs))
	}
	want := map[string]string{
		filepath.Join("/out", "baseline", "lunarouter-m1"): "/b/base",
		filepath.Join("/out", "baseline", "lunarouter-m2"): "/b/base",
		filepath.Join("/out", "v1-A", "lunarouter-m1"):     "/b/a",
		filepath.Join("/out", "v1-A", "lunarouter-m2"):     "/b/a",
	}
	for _, cfg := range cfgs {
		if bin, ok := want[cfg.outDir]; !ok || cfg.evenerBin != bin {
			t.Errorf("config out=%q bin=%q, want one of %v", cfg.outDir, cfg.evenerBin, want)
		}
		if cfg.repetitions != 3 || cfg.probesDir != "tasks" {
			t.Errorf("config %+v lost the base settings", cfg)
		}
		if !strings.HasSuffix(cfg.outDir, safeName(cfg.model)) {
			t.Errorf("config out=%q does not end in its model %q", cfg.outDir, cfg.model)
		}
	}
}

// TestRunMatrixBoundsConcurrencyAndJoinsErrors: at most maxConcurrent runs
// happen at once, every configuration runs, and one failure does not stop
// the rest. Not parallel: it replaces the package's suite runner.
func TestRunMatrixBoundsConcurrencyAndJoinsErrors(t *testing.T) {
	var running, peak, ran atomic.Int32
	var mu sync.Mutex
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error {
		n := running.Add(1)
		mu.Lock()
		if n > peak.Load() {
			peak.Store(n)
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		ran.Add(1)
		if cfg.model == "bad" {
			return errors.New("boom")
		}
		return nil
	}
	cfgs := make([]runConfig, 5)
	for i := range cfgs {
		cfgs[i] = runConfig{model: "good", outDir: filepath.Join("out", string(rune('a'+i)))}
	}
	cfgs[2].model = "bad"
	err := runMatrix(cfgs, 2)
	if ran.Load() != 5 {
		t.Errorf("ran %d configurations, want 5", ran.Load())
	}
	if peak.Load() > 2 {
		t.Errorf("peak concurrency %d, want at most 2", peak.Load())
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the failing configuration's error", err)
	}
}

func TestRunMatrixCommandRejectsPerRunFlags(t *testing.T) {
	t.Parallel()
	err := runMatrixCommand([]string{"--model", "x", "--out", t.TempDir(), "--version", "a=/bin/true", "--models", "m"})
	if err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("err = %v, want a refusal naming --model", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./tools/tool-fluency/cmd/evener-fluency/ -run 'TestMatrix|TestRunMatrix' -count=1`
Expected: FAIL to compile, with `undefined: matrixConfigs`.

- [ ] **Step 3: Implement**

Create `tools/tool-fluency/cmd/evener-fluency/matrix.go`:

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/cmdutil"
)

// matrixVersion is one prompt version under test: its label and the evener
// binary built from it.
type matrixVersion struct {
	Label string
	Bin   string
}

// matrixConfigs expands versions and models into one run configuration per
// pair, each writing to out/<label>/<model>.
func matrixConfigs(base runConfig, versions []matrixVersion, models []string, out string) []runConfig {
	var cfgs []runConfig
	for _, v := range versions {
		for _, model := range models {
			cfg := base
			cfg.evenerBin = v.Bin
			cfg.model = model
			cfg.outDir = filepath.Join(out, safeName(v.Label), safeName(model))
			cfg.systemPromptAppend = slices.Clone(base.systemPromptAppend)
			cfgs = append(cfgs, cfg)
		}
	}
	return cfgs
}

// runMatrixSuite runs one configuration. Tests replace it.
var runMatrixSuite = runSuiteWithConfig

// runMatrix runs every configuration, at most maxConcurrent at once, and
// joins their errors so one failing pair does not stop the others.
func runMatrix(cfgs []runConfig, maxConcurrent int) error {
	sem := make(chan struct{}, maxConcurrent)
	errs := make([]error, len(cfgs))
	var wg sync.WaitGroup
	for i, cfg := range cfgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := runMatrixSuite(cfg); err != nil {
				errs[i] = fmt.Errorf("%s: %w", cfg.outDir, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func runMatrixCommand(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	base := runConfig{}
	var systemPromptAppend cmdutil.StringSliceFlag
	defineRunFlags(fs, &base, &systemPromptAppend)
	var versionFlags cmdutil.StringSliceFlag
	fs.Var(&versionFlags, "version", "LABEL=BIN: a prompt version and its evener binary (repeatable)")
	models := fs.String("models", "", "comma-separated models, such as lunarouter/deepseek-4.1-flash")
	maxConcurrent := fs.Int("max-concurrent", 2, "most runs at once; the gateway caps concurrent requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var misused []string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "model", "evener-bin", "build", "harness":
			misused = append(misused, "--"+f.Name)
		}
	})
	if len(misused) > 0 {
		return fmt.Errorf("matrix sets %s itself; name versions with --version and models with --models", strings.Join(misused, ", "))
	}
	if base.outDir == "" {
		return errors.New("--out is required")
	}
	if *maxConcurrent < 1 {
		return errors.New("--max-concurrent must be at least 1")
	}
	var versions []matrixVersion
	for _, v := range versionFlags {
		label, bin, err := parseLabeled(v)
		if err != nil {
			return err
		}
		versions = append(versions, matrixVersion{Label: label, Bin: bin})
	}
	var modelList []string
	for m := range strings.SplitSeq(*models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			modelList = append(modelList, m)
		}
	}
	if len(versions) == 0 || len(modelList) == 0 {
		return errors.New("need at least one --version and one model in --models")
	}
	base.harness = "cli"
	base.systemPromptAppend = []string(systemPromptAppend)
	return runMatrix(matrixConfigs(base, versions, modelList, base.outDir), *maxConcurrent)
}
```

In `main.go`'s `run`, add `case "matrix": return runMatrixCommand(args[1:])`. Add this usage line:

```
  evener-fluency matrix --version LABEL=BIN [...] --models M1,M2 --out DIR [--max-concurrent N] [run flags]
```

Add the README paragraph named in **Files**.

- [ ] **Step 4: Run the whole package**

Run: `go test ./tools/tool-fluency/... -count=1`
Expected: `ok`.

- [ ] **Step 5: Format, vet, commit**

```bash
"$(go env GOROOT)/bin/gofmt" -w tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/matrix.go tools/tool-fluency/cmd/evener-fluency/matrix_test.go
go vet ./tools/tool-fluency/... && go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=linux go vet -tags evenerfuzz ./tools/tool-fluency/... && GOOS=windows go vet -tags evenerfuzz ./tools/tool-fluency/...
git add tools/tool-fluency/cmd/evener-fluency/main.go tools/tool-fluency/cmd/evener-fluency/matrix.go tools/tool-fluency/cmd/evener-fluency/matrix_test.go tools/tool-fluency/README.md
git commit -m "feat(tool-fluency): run prompt versions against many models at once"
```

- [ ] **Step 6: Open the tooling pull request**

Run `make lint` and read its output. Fix anything it reports in the files this branch touched. Then push `claude/prompt-rewrite-part2` and open a regular pull request titled `feat(prompt-eval): tooling to measure the prompt rewrite`. The description:
- links the spec and this plan;
- lists the four new commands and the nine tasks;
- says that no test reads a system prompt.

Bind the pull request in the app and offer Auto-fix. Merging is Jesse's call. The experiments below use this branch's runner and need nothing merged.

## Experiments

Run every experiment step in this worktree. `$LAB` is `tools/prompt-eval/results/lab` under the worktree. Keep a running log in `$LAB/LAB-LOG.md`: every command, the number that matters from its output, and every decision. After compaction, the log is how the work resumes.

### Task 8: Lab setup, and a smoke run on every model

**Files:** none in git. Everything goes under `$LAB`.

- [ ] **Step 1: Freeze the tools and the baseline**

On `claude/prompt-rewrite-part2`, with Task 7 committed:

```bash
LAB=$PWD/tools/prompt-eval/results/lab
mkdir -p $LAB/bin
cp -R tools/prompt-eval/tasks $LAB/tasks
cp tools/prompt-eval/rubric.md $LAB/rubric.md
go build -o $LAB/bin/evener-fluency ./tools/tool-fluency/cmd/evener-fluency
git fetch -q origin main
BASE=$(git rev-parse origin/main)
git switch -q --detach $BASE && go build -o $LAB/bin/evener-v0 ./cmd/evener && git switch -q claude/prompt-rewrite-part2
echo "baseline commit: $BASE" >> $LAB/LAB-LOG.md
```

Expected: both binaries exist, and the log names the baseline commit. The tasks and rubric are copied because the lab branch, cut from `main`, does not carry them.

- [ ] **Step 2: Get the model names**

Jesse named seven models, then (2026-09-27) set the Claude models and the GPT model aside for now. The runs use the three lunarouter models. Write their refs, one per line, to `$LAB/models.txt`, in this order, since later steps pick models by line:
1. `lunarouter/deepseek-4.1-flash-background`
2. `lunarouter/glm-5.3-vision-background`
3. `lunarouter/glm-5.3-flash-background`, which the gateway may not serve

The `-background` suffix puts a model on lunarouter's batch pool, which allows 30 concurrent requests; the regular pool allows 10 and is shared with interactive sessions (Jesse, 2026-09-27). Every run also passes `--fast-cheap-model lunarouter/deepseek-4.1-flash-background`, so evener's side calls stay on the batch pool too.

If the Claude or GPT models come back, ask Jesse for their exact lunarouter names rather than guessing, and append them.

- [ ] **Step 3: Smoke run**

```bash
$LAB/bin/evener-fluency matrix \
  --version v0=$LAB/bin/evener-v0 \
  --models "$(paste -sd, $LAB/models.txt)" \
  --probes-dir $LAB/tasks --probe prose.smoke \
  --repetitions 1 --max-concurrent 3 \
  --fast-cheap-model lunarouter/deepseek-4.1-flash-background \
  --out $LAB/smoke
```

Expected: one `prose.smoke rep=1 status=passed` line per model. For a model that fails, read its `stderr.ndjson` under `$LAB/smoke`. If the gateway does not know the name, ask Jesse. If `glm-5.3-flash` is not served, drop it from `models.txt` and log that; the plan runs with two models. For any other failure, use superpowers:systematic-debugging before going on.

### Task 9: The baseline

- [ ] **Step 1: Run every task three times on every model**

```bash
$LAB/bin/evener-fluency matrix \
  --version v0=$LAB/bin/evener-v0 \
  --models "$(paste -sd, $LAB/models.txt)" \
  --probes-dir $LAB/tasks \
  --repetitions 3 --timeout 25m --max-concurrent 4 \
  --fast-cheap-model lunarouter/deepseek-4.1-flash-background \
  --out $LAB/runs 2>&1 | tee $LAB/baseline-run.log
```

Expected: 27 result lines per model (nine tasks, three runs each), and results under `$LAB/runs/v0/`.

- [ ] **Step 2: Find the gateway's limit and rule out harness trouble**

```bash
cat $LAB/runs/v0/*/*/rep-*/stderr.ndjson | jq -cR 'fromjson? | select(.kind == "MODEL_RETRY" and (.data.status_code == 429 or .data.error_class == "rate_limit"))' | wc -l
grep -h '"status"' $LAB/runs/v0/*/*/rep-*/result.json | sort | uniq -c
```

The first command counts the gateway's rate-limit retries by their event fields. A plain text search for 429 also matches timestamps and token counts.

Expected: no rate-limit hits, and every status is `passed` or `failed`. On rate limits, rerun each blocked task at `--max-concurrent 2`, with `--probe` naming the task, into a new `--out` such as `$LAB/retry-1`, since a run refuses a directory that holds results. Later steps pass both directories to `prose-stats` and `review-pack` under the same label; the blocked runs show in their own column and do not count against the version. Log the concurrency that held as a line `cap: N` in `$LAB/LAB-LOG.md`. With no hits at 4, log `cap: 7` and watch the next round for rate limits. Look into every status other than passed or failed before the baseline is trusted: timeouts, infra errors, harness failures.

Later rounds read the cap back with:

```bash
CAP=$(grep '^cap:' $LAB/LAB-LOG.md | tail -1 | awk '{print $2}')
```

- [ ] **Step 3: Count the baseline's prose**

```bash
$LAB/bin/evener-fluency prose-stats --results v0=$LAB/runs/v0 | tee $LAB/stats-baseline.txt
$LAB/bin/evener-fluency prose-stats --results v0=$LAB/runs/v0 --channel all | tee $LAB/stats-baseline-all.txt
$LAB/bin/evener-fluency prose-stats --results v0=$LAB/runs/v0 --json > $LAB/stats-baseline.json
```

Expected: one row per model, with `PROSE ERRORS` at 0. Copy the pass counts per task and the tic and identifier rates per model into the log, as the numbers to beat.

### Task 10: Draft the first rewrite

**Files (on `claude/prompt-rewrite-part2-lab`, which never merges):**
- Create: `docs/prompt-lab/decisions.md`
- Modify: `agent/prompts/sections/identity.md`
- Create:
  - `agent/prompts/sections/working.md.tmpl`
  - `agent/prompts/sections/delegating.md.tmpl`
  - `agent/prompts/sections/git-and-safety.md`
  - `agent/prompts/sections/reporting.md.tmpl`
- Modify:
  - `agent/prompts/sections/tools.md.tmpl`
  - `agent/prompts/sections/tools.provider-openai_append.md.tmpl`
  - `agent/prompts/templates/system.md.tmpl`
  - `agent/prompts/templates/subagent.md.tmpl`
- Delete the sections merged into the new ones:
  - `workflow.md.tmpl`, `verification.md`, `context-management.md.tmpl`, `task-tracking.md`, `capabilities.md`
  - `delegation.md`, `background-jobs.md`, `transcripts.md.tmpl`
  - `git-safety.md`, `security.md`
  - `communicate.md.tmpl`, `ask-user.md.tmpl`, `non-interactive.md.tmpl`, `non-interactive.agent-coordinator.md`
- Modify: tool descriptions in `agent/internal/tool/definitions.go` that take over machinery the prompt held alone.

- [ ] **Step 1: Cut the lab branch**

```bash
git switch -q -c claude/prompt-rewrite-part2-lab $(grep '^baseline commit:' $LAB/LAB-LOG.md | awk '{print $3}')
```

- [ ] **Step 2: Decide every rule**

Create `docs/prompt-lab/decisions.md`, with one row per row of the rule inventory (`docs/superpowers/specs/2026-09-26-system-prompt-prose-rewrite/rule-inventory.md` on `claude/prompt-rewrite-part2`, read with `git show`). Its columns: section, number, the rule's short quote, its class, the decision, and the reason. Decide each row by the first of these rules that applies:

1. Machinery that a tool description already covers: cut, and name the tool.
2. Machinery that no tool description covers: move it into that tool's description. If it belongs to no single tool, keep one plain sentence in the prompt.
3. An incident whose failure could still recur (still plausible yes or unclear): keep it, rewritten as the situation the agent will recognize, what to do in it, and why.
4. An incident whose failure can no longer happen: cut, citing the inventory's reason.
5. Instructions to verify, double-check, or be thorough: cut. Anthropic's Opus 5 guidance says they cause over-verification. The flash models decide in Task 11 whether any come back.
6. General practice: cut, unless it is specific to evener or corrects something weaker models get wrong by default.
7. A duplicate: keep one statement, in the area where it belongs.

Then write how each of the two colliding pairs resolves. The first pair: a watch frame that needs no action needs only a short internal note, and every message meant for a person goes through the result tool. The second pair: the deliverable-protection rule gives way when the task's own end state requires teardown.

- [ ] **Step 3: Write the new sections**

Write the areas as guidance in plain paragraphs, the way you would brief a colleague: what to do, when, and why.
- `working.md.tmpl` covers how to work: investigating, testing, fixing, checking, and managing context.
- `delegating.md.tmpl` covers delegation, background work, and transcripts.
- `git-and-safety.md` covers git and security.
- `reporting.md.tmpl` covers the result tool, asking the user, and working with no one to ask.
- `tools.md.tmpl` keeps its generated tool lists and its `{{ if .UnavailableProfileToolNames }}` block unchanged.

Rules for all of them:
- Keep a `{{ if .HasTool "<name>" }}` gate around every mention of a tool some session lacks; the tool-mention sweep enforces this. Keep the surface gate on the openai append.
- Keep every data field the old sections used.
- Rewrite the prose in `subagent.md.tmpl` too.
- Leave the role prompts in `internal/bundled/agents/` alone; they are part 3.
- Point `system.md.tmpl` and `subagent.md.tmpl` at the new sections, with identity first, then working, delegating, git and safety, reporting, tools, and the unchanged data sections.

Write `identity.md` for the no-persona variant first. It holds the control persona, then the writing paragraph, then the two examples. The examples are about neither of the lab's tasks, so no prompt carries an answer.

```markdown
You are evener, a software engineering agent. You check your work, say plainly what you did not finish, and do not dress up a result.

When you write to people, lead with the outcome. Call tasks, issues, and files by their names, because the reader will not remember what an identifier like Task 3 or #1234 meant. Put one idea in each sentence and use plain words. Match the length to what the reader needs: a status update is two or three sentences, and a final report says what changed, how you checked it, and what is left.

A status update:

> The importer rejects timestamps like 2026-09-26T10:00 because the parser expects seconds. I'm making the seconds optional, then I'll rerun the importer's tests.

A final report:

> Done. The importer accepts timestamps without seconds now. The parser treated seconds as required; I made them optional, added a test for that format, and the importer's tests pass. One thing is left: the exporter uses the same parser, and I did not change it.
```

- [ ] **Step 4: Check the draft**

```bash
go build ./cmd/evener
TMPDIR="$(cd "$TMPDIR" && pwd -P)" go test ./agent/ -count=1 -run 'TestShippedPromptsOnlyNameToolsTheSessionHas|TestBundledPromptBodiesOnlyNameToolsTheirAgentHas'
$LAB/bin/evener-fluency prose-count agent/prompts/sections/identity.md agent/prompts/sections/working.md.tmpl agent/prompts/sections/delegating.md.tmpl agent/prompts/sections/git-and-safety.md agent/prompts/sections/reporting.md.tmpl agent/prompts/sections/tools.md.tmpl agent/prompts/sections/tools.provider-openai_append.md.tmpl agent/prompts/templates/subagent.md.tmpl
cat agent/prompts/sections/identity.md agent/prompts/sections/working.md.tmpl agent/prompts/sections/delegating.md.tmpl agent/prompts/sections/git-and-safety.md agent/prompts/sections/reporting.md.tmpl agent/prompts/sections/tools.md.tmpl | wc -c
```

Expected:
- The build passes.
- Both tool-mention sweeps pass.
- For every file, `prose-count` reports 0 em dashes, 0 contrastive lines, 0 bold labels, 0 arrows, 0 opaque ids, and at most 2 shouting words, for the one or two real invariants.
- The byte count is logged next to today's 30KB. Somewhere near 10KB is the expectation; the content decides.

`main`'s older prose-pinning tests fail on this branch, and that is expected (see Global Constraints).

- [ ] **Step 5: Look for contradictions**

Dispatch one reviewer on the most capable model with the concatenated new sections. Ask it to list every pair of passages that could pull an agent two ways, and every passage an agent could read two ways. Resolve each finding in the text and record it in `decisions.md`.

- [ ] **Step 6: Commit the three persona variants**

```bash
git add docs/prompt-lab agent/prompts agent/internal/tool/definitions.go
git commit -m "lab: rewrite the system prompt prose (no persona)"
git tag lab/v1-C
```

When a Step 5 resolution changed Go code or a test, such as a new prompt field or a test sized to the old prompt, stage those files by name in the same commit, and run `(cd agent && go test ./...)` before tagging.

Persona A replaces the first paragraph of `identity.md` with:

```markdown
You are evener. Before you studied computer science, you spent six years as a reporter at a daily newspaper, after a BA in journalism from Wesleyan. You still write like one: you find out what happened, you put the news first, and you write for a busy reader who has not been following along. You call things by their names, you keep sentences short, and you cut every word that carries no information. As an engineer you are careful and direct: you check your work, you say plainly what you did not finish, and you do not dress up a result.
```

Persona B replaces it with:

```markdown
You are evener, a principal engineer. Your design documents and incident reports are the ones people actually read, because they are specific, plain, and short. You write for a colleague who is busy and has not been following along: the outcome first, then what matters, each thing called by its name. You check your work, you say plainly what you did not finish, and you do not dress up a result.
```

```bash
git switch -q --detach lab/v1-C
# replace the first paragraph of agent/prompts/sections/identity.md with persona A
git commit -qam "lab: persona A, the journalist turned engineer" && git tag lab/v1-A
git switch -q --detach lab/v1-C
# replace the first paragraph with persona B
git commit -qam "lab: persona B, the principal engineer" && git tag lab/v1-B
for v in A B C; do git switch -q --detach lab/v1-$v && go build -o $LAB/bin/evener-v1-$v ./cmd/evener; done
git switch -q claude/prompt-rewrite-part2-lab
```

Expected: three tags and three binaries. Log the tags and their commits.

### Task 11: The persona round on the flash models

- [ ] **Step 1: Run the three variants on the flash models**

```bash
CAP=$(grep '^cap:' $LAB/LAB-LOG.md | tail -1 | awk '{print $2}')
FLASH=$(paste -sd, $LAB/models.txt)   # the lunarouter models the smoke run kept
$LAB/bin/evener-fluency matrix \
  --version v1-A=$LAB/bin/evener-v1-A --version v1-B=$LAB/bin/evener-v1-B --version v1-C=$LAB/bin/evener-v1-C \
  --models $FLASH --probes-dir $LAB/tasks \
  --repetitions 3 --timeout 25m --max-concurrent $CAP \
  --fast-cheap-model lunarouter/deepseek-4.1-flash-background \
  --out $LAB/runs 2>&1 | tee $LAB/round1-run.log
```

Expected: 27 result lines for each variant and model.

- [ ] **Step 2: Compare with the baseline**

```bash
$LAB/bin/evener-fluency prose-stats --results v0=$LAB/runs/v0 --results v1-A=$LAB/runs/v1-A --results v1-B=$LAB/runs/v1-B --results v1-C=$LAB/runs/v1-C | tee $LAB/stats-round1.txt
```

Read only the flash rows, since the baseline covers every model. For any task where a variant passes fewer runs than the baseline on the same model, read those transcripts. When a cut lesson caused the drop, bring the lesson back as guidance in a new commit on the lab branch. Tag it `lab/v1.1-<persona>`, rebuild, and rerun that task on the flash models. Log each case.

- [ ] **Step 3: Blind read**

Pack every flash run, then keep one run for each task, version, and model. The packets have random names, so the first packet in each cell by name is a random pick. Leave the smoke task out.

```bash
R1=$LAB/review-round1; mkdir -p $R1/unused
ARGS=""
for m in $(echo $FLASH | tr ',' ' '); do
  d=$(echo $m | tr '/' '-')
  ARGS="$ARGS --results v0=$LAB/runs/v0/$d --results v1-A=$LAB/runs/v1-A/$d --results v1-B=$LAB/runs/v1-B/$d --results v1-C=$LAB/runs/v1-C/$d"
done
$LAB/bin/evener-fluency review-pack $ARGS --mask-root $LAB/runs --packets $R1/packets --key $LAB/review-round1-key.json --seed 11
jq -r 'map(select(.probe != "prose.smoke")) | group_by([.label, .model, .probe]) | map(.[0].packet) | .[]' $LAB/review-round1-key.json > $R1/keep.txt
(cd $R1/packets && ls | grep -vxF -f ../keep.txt | xargs -I{} mv {} ../unused/)
ls $R1/packets | wc -l
```

Expected: 32 packets per flash model (four versions, eight tasks).

Split the packets into four even lists. Dispatch four reviewers on the most capable model in parallel, each with its list, `$LAB/rubric.md`, and this instruction: score every packet against the rubric and return JSON lines of the form `{"packet": "<file>", "scores": {"1": 3, ..., "10": 2}, "note": "<one sentence>"}`. The reviewers never see the key. Save their output as `$R1/scores.jsonl`.

Unblind and average:

```bash
jq -s 'INDEX(.packet)' $LAB/review-round1-key.json > $R1/key-index.json
jq -r --slurpfile key $R1/key-index.json '
  . as $s | $key[0][$s.packet] as $k
  | [$k.label, $k.model,
     ([$s.scores["1","2","3","4","5"] | select(type=="number")] | add / length),
     ([$s.scores["6","7","8","9","10"] | select(type=="number")] | add / length)] | @tsv' \
  $R1/scores.jsonl | sort | awk -F'\t' '{k=$1"\t"$2; w[k]+=$3; b[k]+=$4; n[k]++} END {for (k in n) printf "%s\twriting %.2f\tbehavior %.2f\t(%d)\n", k, w[k]/n[k], b[k]/n[k], n[k]}' | sort | tee $R1/means.txt
```

Expected: one line per version and model, each showing its writing mean and behavior mean.

- [ ] **Step 4: Pick the finalists**

Rank the variants by the tic and identifier rates in the to-user channel, lowest first. Drop any variant whose pass count falls below the baseline's on either flash model after the Step 2 fixes. Break ties with the blind-read means. Keep one or two finalists, and log the ranking and the reason for the pick. Log each finalist as a line `finalist: <label>`, for example `finalist: v1-A`, and make sure `$LAB/bin/evener-<label>` exists for it. A `lab/v1.1-` fix from Step 2 needs its own binary.

### Task 12: The full matrix

- [ ] **Step 1: Run the finalists on every model**

Task 11 already ran each v1 variant on every model in `models.txt`, and the matrix refuses cells that hold results, so this step runs only the finalists with no runs yet, such as a `lab/v1.1-` fix. If the Claude or GPT models come back, run the finalists on those models only.

```bash
CAP=$(grep '^cap:' $LAB/LAB-LOG.md | tail -1 | awk '{print $2}')
FINALISTS=$(grep '^finalist:' $LAB/LAB-LOG.md | awk '{print $2}')
NEW=""; RESULTS="--results v0=$LAB/runs/v0"
for f in $FINALISTS; do
  RESULTS="$RESULTS --results $f=$LAB/runs/$f"
  [ -d $LAB/runs/$f ] || NEW="$NEW --version $f=$LAB/bin/evener-$f"
done
[ -z "$NEW" ] || $LAB/bin/evener-fluency matrix $NEW \
  --models "$(paste -sd, $LAB/models.txt)" --probes-dir $LAB/tasks \
  --repetitions 3 --timeout 25m --max-concurrent $CAP \
  --fast-cheap-model lunarouter/deepseek-4.1-flash-background \
  --out $LAB/runs 2>&1 | tee $LAB/round2-run.log
```

Expected: 27 results for each finalist and model under `$LAB/runs`.

- [ ] **Step 2: Count and compare**

```bash
$LAB/bin/evener-fluency prose-stats $RESULTS | tee $LAB/stats-round2.txt
$LAB/bin/evener-fluency prose-stats $RESULTS --channel all | tee $LAB/stats-round2-all.txt
```

Log, per model family, the change in each rate and in the passes, counting runs and tasks that passed all three runs. The spec's bar for done:
- lower tic and identifier rates on every model family;
- a pass rate that holds or rises;
- a win in the blind read.

- [ ] **Step 3: Blind read a stratified sample**

Read one run per task and version on two models, one per model family: deepseek-4.1-flash and glm-5.3-vision. These are lines 1 and 2 of `$LAB/models.txt`.

```bash
R2=$LAB/review-round2; mkdir -p $R2/unused
SAMPLE="$(sed -n 1p $LAB/models.txt) $(sed -n 2p $LAB/models.txt)"
ARGS=""
for m in $SAMPLE; do
  d=$(echo $m | tr '/' '-')
  ARGS="$ARGS --results v0=$LAB/runs/v0/$d"
  for f in $FINALISTS; do ARGS="$ARGS --results $f=$LAB/runs/$f/$d"; done
done
$LAB/bin/evener-fluency review-pack $ARGS --mask-root $LAB/runs --packets $R2/packets --key $LAB/review-round2-key.json --seed 12
jq -r 'map(select(.probe != "prose.smoke")) | group_by([.label, .model, .probe]) | map(.[0].packet) | .[]' $LAB/review-round2-key.json > $R2/keep.txt
(cd $R2/packets && ls | grep -vxF -f ../keep.txt | xargs -I{} mv {} ../unused/)
```

Then score and unblind exactly as in Task 11 Step 3, with `R2` in place of `R1`.

### Task 13: Report and recommendation

- [ ] **Step 1: Write the report**

Write `docs/prompt-lab/REPORT.md` on the lab branch. It holds:
- the models and task set;
- the baseline commit and the winning tag;
- the Task 12 tables;
- the blind-read means;
- three or four before-and-after pairs of messages on the same task and model;
- what the rewrite cut, moved, and kept, summarized from `decisions.md`;
- the recommendation;
- what these runs could not show, starting with the anthropic surface.

Write it in the voice the rewrite asks for. Commit it on the lab branch.

- [ ] **Step 2: Hand it to Jesse**

Send Jesse the report and six blind packet pairs to read himself. Each pair is the baseline and the winner on the same task and model; tell him the key only after he has read them. Ask whether he agrees with the winner.

- **If he agrees:** the next step is the landing plan. After part 1's collapse merges, move the winning text into `agent/prompts/system.md.tmpl` and the machinery into the tool descriptions, and check the anthropic surface.
- **If no version clears the bar:** propose the blank-page rewrite the spec names as the next candidate.
