# SoL-Pi Slice 1 (Oracle Analyzer + Offline Runner) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build delivery slice 1 of the SoL-Pi auto-research loop: an oracle analyzer that measures real session transcripts for harness overhead, two committed smoke environments, and the offline-testable rollout runner mechanics, ending in the Phase 0 stop-or-go report on real data.

**Architecture:** A new `agent/research` package holds the library code (oracle walker + signals + projections, runner + executor interface, ATIF metrics extraction) so it can import `agent/internal/*` and `agent/transcript` types. The dev CLI (`cmd/evener-dev`, package `dev`) gets a thin `research` subcommand that wires flags to the library. Environments are committed data at `research/environments/`, each a task prompt, a fixture repo, and a verifier script.

**Tech Stack:** Go stdlib only, plus existing repo packages: `agent/transcript`, `agent/schema`, `agent/doctor`, `agent/internal/atif`, `agent/internal/liveeval`, `llm`. No new third-party dependencies.

**Spec:** `docs/superpowers/specs/2026-09-19-sol-pi-auto-research-loop-design.md`

## Global Constraints

- Default tests are deterministic and offline. No default test issues a live request. Live rollouts require `--live`, which requires `EVENER_LIVE_TESTS=1` (`agent/internal/liveeval.Enabled`).
- All work stays on the `sol-pi` branch in the `sol-pi` worktree; nothing lands on `main` except by pull request.
- Never commit run artifacts: run directories, ledgers, ATIF exports, and transcripts are runtime output, not repo content.
- Repo gates stay green on every task: `make lint`, `make vet`, and `make test` (or the shorter `go test ./agent/research/... ./cmd/evener-dev/...` while iterating, full gates before each commit when feasible).
- Disk is at 95 percent: tests use `t.TempDir()`; the runner writes to caller-provided scratch run dirs.
- Match surrounding code style. TDD: write the failing test first, then implement, then commit.
- Naming: Go code lives in `agent/research/` (package `research`); the data directory is `research/environments/`.

## Key repo facts (verified during design; read this before coding)

- Transcripts live at `<stateBase>/projects/<project-id>/sessions/<SID>.transcript.jsonl`. Line 1 is a header (`kind:"header"`, `format_version`); following lines are entries (`transcript.DecodeHeader`, `transcript.DecodeEntry`). `transcript.Entry{Kind, Seq, Turn schema.Turn}`.
- `schema.Turn` has `Kind schema.TurnKind`, `Message llm.Message`, `Usage llm.Usage`. Kinds seen: `TurnUserInput`, `TurnAssistant`, `TurnToolResults`, `TurnCheckpoint`, `TurnSummary`, `TurnSteering`, `TurnFailure`, `TurnModelSwitch`, `TurnSystem`.
- Tool calls sit on assistant messages as content parts: `Message.Content []ContentPart` with `Kind == llm.ContentToolCall`, `ToolCall *llm.ToolCallData{ID, Name, Arguments json.RawMessage, ParsedArguments map[string]any}`. Tool results sit on `TurnToolResults` turns as parts with `Kind == llm.ContentToolResult`, `ToolResult *llm.ToolResultData{ToolCallID, Name, Content any, IsError}`. `llm.Assistant("text")` builds a plain text assistant message.
- `llm.Usage` fields: `InputTokens`, `OutputTokens`, `TotalTokens`, `CacheReadTokens *int`, `CacheWriteTokens *int`. Set on assistant turns.
- The shell tool is registered as `"shell"`. File-mutation tools: `edit_file`, `write_file`, `apply_patch`.
- `agent/doctor.ResolveStateBase(flagStateDir string) string` resolves the default state base (use it in the CLI; the library takes a plain path).
- `agent/internal/atif` has `type Trajectory struct` with `Steps []Step` and `FinalMetrics *FinalMetrics{TotalPromptTokens, TotalCompletionTokens, TotalCachedTokens, TotalSteps}` (all `int`), JSON-tagged. `agent/research` may import it (same subtree).
- `agent/internal/liveeval` exports `OptInEnv = "EVENER_LIVE_TESTS"` and `Enabled(value string) bool`.
- `evener run` CLI (from `cmd/evener/run.go` and `cmd/evener/main.go`): positional prompt; flags `--model <provider/model>`, `--state-dir <dir>`, `--max-rounds <n>`, `-n <atif-export-path>`. The runner sets the subprocess working directory to the environment workdir rather than passing a cwd flag.
- `cmd/evener-dev` is package `dev` with a `subcommands map[string]func(args []string) int` in `main.go`; existing entries: `agent-shards`, `covstmt`, `module-lint`. Usage prints sorted names.
- Compaction boundaries in transcripts: `TurnCheckpoint` (deterministic layer) and `TurnSummary` (LLM layer).
- A session is driven in-process by `sess.ProcessInput(ctx, prompt, nil)`; package `agent` tests construct sessions via the testkit `newSession(t, ...)` with `fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{...}}` (see `agent/testkit_test.go`, `agent/task_workflow_test.go`).

---

### Task 1: `research` subcommand skeleton in the dev CLI

**Files:**
- Create: `cmd/evener-dev/research.go`
- Modify: `cmd/evener-dev/main.go` (register the subcommand)
- Test: `cmd/evener-dev/research_test.go`

**Interfaces:**
- Produces: `runResearch(args []string) int` dispatching to `researchOracleCmd` and `researchRolloutCmd` (added by Tasks 5 and 9). Until those exist, the dispatcher returns exit 2 with "not yet implemented" so the skeleton compiles alone.

- [ ] **Step 1: Write the failing test**

```go
package dev

import (
	"bytes"
	"strings"
	"testing"
)

func TestResearchDispatch_RequiresSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runResearch([]string{})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "oracle") || !strings.Contains(errOut.String(), "rollout") {
		// errOut is unused by runResearch itself; the usage text goes to
		// stderr via the package-level writers. Adjust if the package
		// pattern differs: agent-shards writes usage to stderr directly.
		_ = out.String()
	}
}
```

Note: before finalizing the test, read how `agentshards` returns usage (run `rg -n "usage\(" cmd/evener-dev/agentshards.go | head`). The dev package's subcommands write to package stderr writers; mirror that exactly and assert on the actual stream used.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/evener-dev/ -run TestResearchDispatch -v`
Expected: FAIL, `runResearch` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package dev

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// researchUsageDoc is the usage text for the research subcommand family.
const researchUsageDoc = `usage: evener-dev research <subcommand> [flags]

subcommands:
  oracle    measure harness overhead signals in real session transcripts
  rollout   run paired harness rollouts against research environments
`

// runResearch dispatches the research subcommand family. Subcommands are
// wired as they land: oracle (slice 1), rollout (slice 1), gate/freeze/
// validate (slice 2).
func runResearch(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, researchUsageDoc)
		return 2
	}
	switch args[0] {
	case "oracle":
		return researchOracleCmd(args[1:])
	case "rollout":
		return researchRolloutCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "evener-dev research: unknown subcommand %q\n%s", args[0], researchUsageDoc)
		return 2
	}
}

// researchOracleCmd and researchRolloutCmd are wired in by their tasks.
var (
	researchOracleCmd   = func(args []string) int { fmt.Fprint(os.Stderr, researchUsageDoc); return 2 }
	researchRolloutCmd  = func(args []string) int { fmt.Fprint(os.Stderr, researchUsageDoc); return 2 }
)
```

Then sort-usage note: the package `usage()` in main.go does not need changes beyond registering `"research": runResearch` in the `subcommands` map. Do that registration in this task.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/evener-dev/ -run TestResearchDispatch -v`
Expected: PASS. Also run `go test ./cmd/evener-dev/` for the package.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-dev/research.go cmd/evener-dev/research_test.go cmd/evener-dev/main.go
git commit -m "dev: research subcommand skeleton on the dev CLI"
```

---

### Task 2: Oracle transcript walker and entry loader

**Files:**
- Create: `agent/research/oracle.go`
- Test: `agent/research/oracle_test.go`

**Interfaces:**
- Produces:

```go
// sessionTranscript names one discovered transcript file.
type sessionTranscript struct {
	Path      string    // absolute path to <SID>.transcript.jsonl
	SessionID string    // <SID> from the file name
	ModTime   time.Time
}

// walkSessionTranscripts returns up to limit transcript paths under
// stateBase, newest first. limit <= 0 means no limit.
func walkSessionTranscripts(stateBase string, limit int) ([]sessionTranscript, error)

// loadEntries decodes one transcript file: header line first (validated but
// not returned), then one Entry per line. Undecodable lines are skipped and
// counted; a missing file is an error, a corrupt header is an error.
func loadEntries(path string) (entries []transcript.Entry, skipped int, err error)
```

- Consumes: `agent/transcript` (`DecodeHeader`, `DecodeEntry`, `Entry`), stdlib `path/filepath`, `os`, `sort`.

- [ ] **Step 1: Write the failing test**

```go
package research

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// writeTranscript writes a minimal valid v2 transcript: header + entries.
func writeTranscript(t *testing.T, dir, sid string, entries []transcript.Entry) string {
	t.Helper()
	sessions := filepath.Join(dir, "projects", "proj", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, sid+".transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(entries) == 0 {
		// Header-only file: still valid for walking.
		return path
	}
	return path
}

func TestWalkSessionTranscripts_NewestFirstAndLimit(t *testing.T) {
	dir := t.TempDir()
	a := writeTranscript(t, dir, "aaa", nil)
	b := writeTranscript(t, dir, "bbb", nil)
	if err := os.Chtimes(b, timeNowPlus(0), timeNowPlus(0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(a, timeNowPlus(-3600), timeNowPlus(-3600)); err != nil {
		t.Fatal(err)
	}
	got, err := walkSessionTranscripts(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SessionID != "bbb" || got[1].SessionID != "aaa" {
		t.Fatalf("got %+v, want bbb then aaa", got)
	}
	limited, err := walkSessionTranscripts(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].SessionID != "bbb" {
		t.Fatalf("limit=1 got %+v", limited)
	}
}

func TestLoadEntries_SkipsBadLines(t *testing.T) {
	dir := t.TempDir()
	headerLine := `{"kind":"header","format_version":2}`
	userLine := `{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hi"}]}}}`
	badLine := `{not json`
	path := filepath.Join(dir, "s.transcript.jsonl")
	content := headerLine + "\n" + userLine + "\n" + badLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, skipped, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || skipped != 1 {
		t.Fatalf("entries=%d skipped=%d, want 1/1", len(entries), skipped)
	}
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Fatalf("kind = %s", entries[0].Turn.Kind)
	}
}
```

Add a tiny helper in the test file: `func timeNowPlus(seconds int64) time.Time { return time.Now().Add(time.Duration(seconds) * time.Second) }`.

Note: verify the header line's `format_version` value against `transcript.FormatVersion` (`rg -n "FormatVersion =" agent/transcript/transcript.go`) and use the constant in the test rather than a literal if it is exported.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/research/ -run 'TestWalk|TestLoadEntries' -v`
Expected: FAIL, functions undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// Package research implements the SoL-Pi auto-research loop's offline
// machinery: the oracle analyzer over real session transcripts, and the
// rollout runner that drives headless harness runs against committed
// research environments. See docs/superpowers/specs/2026-09-19-sol-pi-auto-research-loop-design.md.
package research

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/transcript"
)

type sessionTranscript struct {
	Path      string
	SessionID string
	ModTime   time.Time
}

// walkSessionTranscripts finds transcript files under
// <stateBase>/projects/*/sessions/*.transcript.jsonl, newest first.
func walkSessionTranscripts(stateBase string, limit int) ([]sessionTranscript, error) {
	pattern := filepath.Join(stateBase, "projects", "*", "sessions", "*.transcript.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("walk transcripts: %w", err)
	}
	out := make([]sessionTranscript, 0, len(matches))
	for _, p := range matches {
		st, err := os.Stat(p)
		if err != nil {
			continue // raced with deletion; skip
		}
		out = append(out, sessionTranscript{
			Path:      p,
			SessionID: strings.TrimSuffix(filepath.Base(p), ".transcript.jsonl"),
			ModTime:   st.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// loadEntries decodes one transcript file. The first line must be the v2
// header. Undecodable entry lines are skipped and counted: real corpora
// contain torn tail lines, and the oracle measures, it does not reject.
func loadEntries(path string) (entries []transcript.Entry, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			if _, err := transcript.DecodeHeader(line); err != nil {
				return nil, 0, fmt.Errorf("%s: %w", path, err)
			}
			first = false
			continue
		}
		e, err := transcript.DecodeEntry(line)
		if err != nil {
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan %s: %w", path, err)
	}
	return entries, skipped, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/research/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research/oracle.go agent/research/oracle_test.go
git commit -m "research: transcript walker and lenient entry loader"
```

---

### Task 3: Adjacency signal (Action Fusion projection)

**Files:**
- Modify: `agent/research/oracle.go`
- Test: `agent/research/oracle_test.go` (append)

**Interfaces:**
- Produces:

```go
// AdjacencyStats counts edit-then-command cycles an Action Fusion style
// tool change would collapse, and the tokens of the intervening request
// that disappears: the request whose input re-sent the edit result and
// whose output chose the test command.
type AdjacencyStats struct {
	Cycles                int
	SavedPromptTokens     int
	SavedCompletionTokens int
}

func measureAdjacency(entries []transcript.Entry) AdjacencyStats

// helper exports reused by Tasks 4 and 5:
func toolCallsOf(msg llm.Message) []llm.ToolCallData
func shellCommandOf(call llm.ToolCallData) string
```

- Consumes: `llm.Message`, `llm.ContentPart`, `llm.ToolCallData`, `schema.TurnKind`.

- [ ] **Step 1: Write the failing test**

```go
func assistantToolCallTurn(id string, calls []llm.ToolCallData, in, out int) transcript.Entry {
	content := make([]llm.ContentPart, 0, len(calls))
	for _, c := range calls {
		c := c
		content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &c})
	}
	return transcript.Entry{Kind: "entry", Turn: schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: "assistant", Content: content},
		Usage:  llm.Usage{InputTokens: in, OutputTokens: out},
	}}
}

func mutationCall(name string) llm.ToolCallData {
	return llm.ToolCallData{ID: "c_" + name, Name: name, Arguments: json.RawMessage(`{}`)}
}

func shellCall(command string) llm.ToolCallData {
	return llm.ToolCallData{
		ID:   "c_shell",
		Name: "shell",
		Arguments: json.RawMessage(`{"command":"` + command + `","description":"d"}`),
	}
}

func TestMeasureAdjacency_CountsEditThenTestCycles(t *testing.T) {
	entries := []transcript.Entry{
		assistantToolCallTurn("u1", []llm.ToolCallData{mutationCall("edit_file")}, 1000, 50),
		// result turn; adjacency scanner looks past it
		assistantToolCallTurn("u2", []llm.ToolCallData{shellCall("go test ./...")}, 2200, 80),
		assistantToolCallTurn("u3", []llm.ToolCallData{shellCall("go build ./...")}, 2400, 60),
		assistantToolCallTurn("u4", []llm.ToolCallData{mutationCall("apply_patch")}, 2500, 70),
		assistantToolCallTurn("u5", []llm.ToolCallData{shellCall("ls -la")}, 2600, 40), // not a test command
	}
	got := measureAdjacency(entries)
	if got.Cycles != 1 {
		t.Fatalf("Cycles = %d, want 1 (edit_file then go test)", got.Cycles)
	}
	if got.SavedPromptTokens != 2200 || got.SavedCompletionTokens != 80 {
		t.Fatalf("saved tokens = %d/%d, want 2200/80", got.SavedPromptTokens, got.SavedCompletionTokens)
	}
}
```

The `encoding/json` import joins the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/research/ -run TestMeasureAdjacency -v`
Expected: FAIL, `measureAdjacency` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
var researchMutationTools = map[string]bool{
	"edit_file":   true,
	"write_file":  true,
	"apply_patch": true,
}

// researchTestBuildRe matches shell commands whose output an agent typically
// consumes right after a file mutation: test, build, vet, lint. Fusing these
// into the mutation call removes the intervening model round trip.
var researchTestBuildRe = regexp.MustCompile(`\b(go test|go build|go vet|make(?:\s+\S+)?|npm test|npm run (?:test|build)|pytest|cargo (?:test|build))\b`)

// toolCallsOf returns the tool calls present in a message, in order.
func toolCallsOf(msg llm.Message) []llm.ToolCallData {
	var out []llm.ToolCallData
	for i := range msg.Content {
		if part := msg.Content[i]; part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			out = append(out, *part.ToolCall)
		}
	}
	return out
}

// shellCommandOf extracts the command string from a shell tool call's
// arguments. ParsedArguments is preferred; the raw JSON is the fallback.
func shellCommandOf(call llm.ToolCallData) string {
	if v, ok := call.ParsedArguments["command"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	var raw struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Arguments, &raw); err == nil {
		return raw.Command
	}
	return ""
}

func measureAdjacency(entries []transcript.Entry) AdjacencyStats {
	var st AdjacencyStats
	for i := range entries {
		turn := entries[i].Turn
		if turn.Kind != schema.TurnAssistant {
			continue
		}
		calls := toolCallsOf(turn.Message)
		mutated := false
		for _, c := range calls {
			if researchMutationTools[c.Name] {
				mutated = true
				break
			}
		}
		if !mutated {
			continue
		}
		// The next assistant turn is the request fusion would remove.
		for j := i + 1; j < len(entries); j++ {
			next := entries[j].Turn
			if next.Kind != schema.TurnAssistant {
				continue
			}
			nextCalls := toolCallsOf(next.Message)
			if len(nextCalls) > 0 && nextCalls[0].Name == "shell" &&
				researchTestBuildRe.MatchString(shellCommandOf(nextCalls[0])) {
				st.Cycles++
				st.SavedPromptTokens += next.Usage.InputTokens
				st.SavedCompletionTokens += next.Usage.OutputTokens
			}
			break
		}
	}
	return st
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/research/ -run TestMeasureAdjacency -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research/oracle.go agent/research/oracle_test.go
git commit -m "research: adjacency signal projecting Action Fusion savings"
```

---

### Task 4: Observation signals (ObservationPack residual and log volume)

**Files:**
- Modify: `agent/research/oracle.go`
- Test: `agent/research/oracle_test.go` (append)

**Interfaces:**
- Produces:

```go
// ObsResendStats measures bytes of oversized tool results that keep being
// re-sent on requests after their first two. First two requests free; the
// residual after that is what an ObservationPack style handle-plus-excerpt
// mechanism could save. Re-send accounting stops at a compaction boundary
// (TurnCheckpoint or TurnSummary) because masking/compaction may already
// have removed the observation from context there. This is a proxy: the
// transcript cannot show per-request masking, so treat results as resident
// until a boundary appears.
type ObsResendStats struct {
	Results       int // number of oversized results
	TotalBytes    int // sum of their sizes
	ResendBytes   int // bytes re-sent on requests after the first two
	ResendRequests int // requests paying that residual
}

func measureLargeObservations(entries []transcript.Entry, threshold int) ObsResendStats

// LogVolumeStats is the same measurement restricted to build/test-style
// shell output (the Evidence-Preserving Reducer's input class).
type LogVolumeStats struct {
	Results        int
	TotalBytes     int
	ResendBytes    int
	ResendRequests int
}

func measureLogVolume(entries []transcript.Entry, minBytes int) LogVolumeStats

// resultTextOf extracts the text of one tool result content value.
func resultTextOf(content any) string
```

- Consumes: `llm.ContentToolResult`, `llm.ToolResultData`, `schema.TurnToolResults`.

- [ ] **Step 1: Write the failing test**

```go
func toolResultsTurn(results ...llm.ToolResultData) transcript.Entry {
	content := make([]llm.ContentPart, 0, len(results))
	for _, r := range results {
		r := r
		content = append(content, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &r})
	}
	return transcript.Entry{Kind: "entry", Turn: schema.Turn{
		Kind:    schema.TurnToolResults,
		Message: llm.Message{Role: "tool", Content: content},
	}}
}

func bigResult(name, text string) llm.ToolResultData {
	return llm.ToolResultData{ToolCallID: "c_" + name, Name: name, Content: text}
}

func TestMeasureLargeObservations_ResendAfterFirstTwo(t *testing.T) {
	big := strings.Repeat("x", 11*1024)
	small := "ok"
	entries := []transcript.Entry{
		toolResultsTurn(bigResult("shell", big), bigResult("read_file", small)),
		assistantToolCallTurn("r1", nil, 5000, 10), // request 1 sees it (free)
		assistantToolCallTurn("r2", nil, 5200, 10), // request 2 sees it (free)
		assistantToolCallTurn("r3", nil, 5400, 10), // request 3 pays: +len(big)
		// compaction boundary ends residency accounting
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnCheckpoint}},
		assistantToolCallTurn("r4", nil, 2000, 10), // after boundary: not counted
	}
	got := measureLargeObservations(entries, 10*1024)
	if got.Results != 1 || got.TotalBytes != len(big) {
		t.Fatalf("results=%d bytes=%d, want 1/%d", got.Results, got.TotalBytes, len(big))
	}
	if got.ResendBytes != len(big) || got.ResendRequests != 1 {
		t.Fatalf("resend=%d over %d requests, want %d over 1", got.ResendBytes, got.ResendRequests, len(big))
	}
}

func TestMeasureLogVolume_OnlyDeclaredCommands(t *testing.T) {
	log := strings.Repeat("FAIL line\n", 600) // ~5.4 KiB, from go test
	entries := []transcript.Entry{
		toolResultsTurn(bigResult("shell", log)),
		assistantToolCallTurn("r1", nil, 5000, 10),
		assistantToolCallTurn("r2", nil, 5200, 10),
		assistantToolCallTurn("r3", nil, 5400, 10),
	}
	vol := measureLogVolume(entries, 4*1024)
	if vol.Results != 1 || vol.TotalBytes != len(log) {
		t.Fatalf("vol results=%d bytes=%d", vol.Results, vol.TotalBytes)
	}
	if vol.ResendBytes != len(log) {
		t.Fatalf("resend = %d, want %d", vol.ResendBytes, len(log))
	}
}
```

The log volume test needs the shell command to be recoverable. The result carries `Name: "shell"` but not the command; to attribute results to commands, pair each shell result with the tool call that produced it via `ToolCallID`. Implementation detail below resolves the command by looking up the tool call ID in earlier assistant turns; the test's `bigResult` uses ID `c_shell` and the preceding assistant turn must carry a matching call. Extend the second test to insert `assistantToolCallTurn("p", []llm.ToolCallData{shellCall("go test ./...")}, 10, 5)` before the results turn and give the result `ToolCallID: "c_shell"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/research/ -run 'TestMeasureLargeObservations|TestMeasureLogVolume' -v`
Expected: FAIL, functions undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// resultTextOf flattens a tool result content into text. Content is
// provider-shaped: usually a string, sometimes a list of parts.
func resultTextOf(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
			if s, ok := p.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	case map[string]any:
		if s, ok := v["text"].(string); ok {
			return s
		}
	}
	return ""
}

// pendingObs tracks one oversized result's residency in later requests.
type pendingObs struct {
	bytes     int
	fromShell bool // shell result attributable to a declared command
	seen     int   // assistant requests that carried it
}

// walkObservations is the shared engine of both observation signals. The
// filter decides which results count: every oversized result for the
// ObservationPack residual, shell results from declared commands for log
// volume.
func walkObservations(entries []transcript.Entry, minBytes int, filter func(callID string, name string, text string, entries []transcript.Entry, idx int) bool) (st ObsResendStats) {
	// First pass: map tool call IDs to their issuing assistant tool call.
	callCommand := map[string]string{}
	for i := range entries {
		if entries[i].Turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, c := range toolCallsOf(entries[i].Turn.Message) {
			if c.Name == "shell" {
				callCommand[c.ID] = shellCommandOf(c)
			}
		}
	}
	var pending []pendingObs
	for i := range entries {
		turn := entries[i].Turn
		switch turn.Kind {
		case schema.TurnToolResults:
			for _, part := range turn.Message.Content {
				if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
					continue
				}
				text := resultTextOf(part.ToolResult.Content)
				if len(text) < minBytes {
					continue
				}
				isDeclared := false
				if cmd, ok := callCommand[part.ToolResult.ToolCallID]; ok && researchTestBuildRe.MatchString(cmd) {
					isDeclared = true
				}
				if !filter(part.ToolResult.ToolCallID, part.ToolResult.Name, text, entries, i) {
					continue
				}
				_ = isDeclared
				st.Results++
				st.TotalBytes += len(text)
				pending = append(pending, pendingObs{bytes: len(text), fromShell: part.ToolResult.Name == "shell", seen: 0})
			}
		case schema.TurnAssistant:
			for k := range pending {
				pending[k].seen++
				if pending[k].seen > 2 {
					st.ResendBytes += pending[k].bytes
					st.ResendRequests++
				}
			}
		case schema.TurnCheckpoint, schema.TurnSummary:
			// Compaction boundary: residency accounting stops here.
			pending = pending[:0]
		}
	}
	return st
}

func measureLargeObservations(entries []transcript.Entry, threshold int) ObsResendStats {
	return walkObservations(entries, threshold, func(_, _, _ string, _ []transcript.Entry, _ int) bool { return true })
}

func measureLogVolume(entries []transcript.Entry, minBytes int) LogVolumeStats {
	// Only shell results whose issuing command matches the declared set.
	st := ObsResendStats{}
	callCommand := map[string]string{}
	for i := range entries {
		if entries[i].Turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, c := range toolCallsOf(entries[i].Turn.Message) {
			if c.Name == "shell" {
				callCommand[c.ID] = shellCommandOf(c)
			}
		}
	}
	_ = st
	_ = callCommand
	base := walkObservations(entries, minBytes, func(callID, _, _ string, all []transcript.Entry, _ int) bool {
		cmd, ok := callCommandFrom(callID, all)
		return ok && researchTestBuildRe.MatchString(cmd)
	})
	return LogVolumeStats{Results: base.Results, TotalBytes: base.TotalBytes, ResendBytes: base.ResendBytes, ResendRequests: base.ResendRequests}
}
```

Implementation note for the implementer: `walkObservations` already builds the call-ID-to-command map internally; extract that into a small named function `shellCommandByCallID(entries []transcript.Entry) map[string]string` and use it in both `walkObservations`'s filter plumbing and `measureLogVolume`, deleting the duplicated map-building code and the placeholder lines (`_ = st` etc. above show the seam; clean code is expected). The final shape: one helper builds the map; `walkObservations` takes a filter receiving `(name string, text string, cmd string, cmdOK bool)`; `measureLargeObservations` passes `func(...) bool { return true }`; `measureLogVolume` passes `func(..., cmd string, cmdOK bool) bool { return cmdOK }`. Write the tests first, then refactor to that shape; do not ship the duplicated-map version.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/research/ -run 'TestMeasureLarge|TestMeasureLogVolume' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research/oracle.go agent/research/oracle_test.go
git commit -m "research: observation resend and log volume signals"
```

---

### Task 5: Compaction timing, report assembly, projections, and the oracle CLI

**Files:**
- Modify: `agent/research/oracle.go`
- Modify: `cmd/evener-dev/research.go` (implement `researchOracleCmd`)
- Test: `agent/research/oracle_test.go` (append), `cmd/evener-dev/research_test.go` (append)

**Interfaces:**
- Produces:

```go
type CompactionStats struct {
	Compactions           []compactionPoint
	MaxPromptTokensSeen    int
	RequestsBetween        []int // assistant requests between consecutive boundaries
}
type compactionPoint struct {
	RequestIndex   int // ordinal of the assistant request most recently seen
	PromptTokens   int // that request's input tokens
	Kind           schema.TurnKind // TurnCheckpoint or TurnSummary
}

// MechanismProjection is one ranked candidate.
type MechanismProjection struct {
	Mechanism    string  // "action-fusion", "observation-pack", "log-reducer", "compaction-reminder"
	SavedTokens  int     // projected absolute token savings (input + output where applicable)
	TrafficPct   float64 // percent of corpus recorded traffic
	Basis        string  // human explanation of the measurement
	Informational bool   // true when no pct projection is computed (compaction timing)
}

type OracleReport struct {
	GeneratedAt   time.Time
	StateBase     string
	Sessions      int
	Entries       int
	SkippedLines  int
	TotalInputTokens  int
	TotalOutputTokens int
	Adjacency     AdjacencyStats
	LargeObs      ObsResendStats
	LogVolume     LogVolumeStats
	Compaction    CompactionStats
	Projections   []MechanismProjection
}

func buildReport(stateBase string, sessions int, entries int, skipped int, adj AdjacencyStats, obs ObsResendStats, logs LogVolumeStats, comp CompactionStats, totalIn, totalOut int) *OracleReport
func renderReport(r *OracleReport) string
func RunOracle(opts OracleOptions) (*OracleReport, error)

type OracleOptions struct {
	StateBase string // resolved state base
	Limit     int    // max sessions, newest first; 0 = all
	OutPath   string // JSONL file; one report record appended per run
	Stdout    io.Writer
}
```

- Consumes: Task 2-4 functions.

- [ ] **Step 1: Write the failing test**

```go
func TestBuildReport_RanksProjections(t *testing.T) {
	adj := AdjacencyStats{Cycles: 3, SavedPromptTokens: 9000, SavedCompletionTokens: 300}
	obs := ObsResendStats{Results: 2, TotalBytes: 40 * 1024, ResendBytes: 20 * 1024, ResendRequests: 5}
	logs := LogVolumeStats{Results: 1, TotalBytes: 12 * 1024, ResendBytes: 8 * 1024, ResendRequests: 2}
	r := buildReport("/x", 5, 100, 2, adj, obs, logs, CompactionStats{}, 200000, 40000)
	if r.Sessions != 5 || r.Entries != 100 || r.SkippedLines != 2 {
		t.Fatalf("report header wrong: %+v", r)
	}
	if r.TotalInputTokens != 200000 || r.TotalOutputTokens != 40000 {
		t.Fatalf("token totals wrong")
	}
	// Traffic pct is saved tokens over recorded traffic (input+output).
	wantAdj := float64(9300) / float64(240000) * 100
	var gotAdj *MechanismProjection
	for i := range r.Projections {
		if r.Projections[i].Mechanism == "action-fusion" {
			gotAdj = &r.Projections[i]
		}
	}
	if gotAdj == nil {
		t.Fatal("no action-fusion projection")
	}
	if gotAdj.SavedTokens != 9300 {
		t.Fatalf("action-fusion saved = %d, want 9300", gotAdj.SavedTokens)
	}
	if math.Abs(gotAdj.TrafficPct-wantAdj) > 0.001 {
		t.Fatalf("action-fusion pct = %f, want %f", gotAdj.TrafficPct, wantAdj)
	}
	// Byte-based projections estimate tokens at bytes/4 (the contextmgr
	// char/4 estimator) over input tokens only.
	wantObs := float64(20*1024/4) / float64(200000) * 100
	var gotObs *MechanismProjection
	for i := range r.Projections {
		if r.Projections[i].Mechanism == "observation-pack" {
			gotObs = &r.Projections[i]
		}
	}
	if gotObs == nil {
		t.Fatal("no observation-pack projection")
	}
	if math.Abs(gotObs.TrafficPct-wantObs) > 0.001 {
		t.Fatalf("observation-pack pct = %f, want %f", gotObs.TrafficPct, wantObs)
	}
	// Compaction reminder is informational in slice 1.
	for _, p := range r.Projections {
		if p.Mechanism == "compaction-reminder" && !p.Informational {
			t.Fatal("compaction-reminder must be informational in slice 1")
		}
	}
}

func TestRenderReport_IncludesStopOrGoVerdict(t *testing.T) {
	// A report whose best projection is under 5% must say so plainly.
	r := buildReport("/x", 1, 1, 0, AdjacencyStats{}, ObsResendStats{}, LogVolumeStats{}, CompactionStats{}, 1000, 100)
	text := renderReport(r)
	if !strings.Contains(text, "under 5%") && !strings.Contains(text, "5%") {
		t.Fatalf("rendered report lacks stop-or-go statement:\n%s", text)
	}
}
```

Also a CLI test in `cmd/evener-dev/research_test.go`:

```go
func TestResearchOracleCmd_MissingStateDirFails(t *testing.T) {
	// No --state-dir and no default resolution in this environment:
	// the cmd must fail with exit 1 and a message, not panic.
	code := researchOracleCmd([]string{"--state-dir", "/nonexistent-definitely-missing"})
	if code == 0 {
		t.Fatal("oracle on missing state dir returned 0")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/research/ ./cmd/evener-dev/ -run 'TestBuildReport|TestRenderReport|TestResearchOracleCmd' -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `agent/research/oracle.go` append:

```go
func measureCompaction(entries []transcript.Entry) CompactionStats {
	var st CompactionStats
	requests := 0
	for i := range entries {
		turn := entries[i].Turn
		switch turn.Kind {
		case schema.TurnAssistant:
			requests++
			if turn.Usage.InputTokens > st.MaxPromptTokensSeen {
				st.MaxPromptTokensSeen = turn.Usage.InputTokens
			}
		case schema.TurnCheckpoint, schema.TurnSummary:
			st.Compactions = append(st.Compactions, compactionPoint{
				RequestIndex: requests,
				PromptTokens: st.MaxPromptTokensSeen,
				Kind:         turn.Kind,
			})
		}
	}
	prev := 0
	for _, p := range st.Compactions {
		st.RequestsBetween = append(st.RequestsBetween, p.RequestIndex-prev)
		prev = p.RequestIndex
	}
	return st
}

const researchBytesPerToken = 4 // matches the contextmgr char/4 estimator

func buildReport(stateBase string, sessions, entries, skipped int, adj AdjacencyStats, obs ObsResendStats, logs LogVolumeStats, comp CompactionStats, totalIn, totalOut int) *OracleReport {
	r := &OracleReport{
		GeneratedAt: time.Now().UTC(),
		StateBase:   stateBase, Sessions: sessions, Entries: entries, SkippedLines: skipped,
		TotalInputTokens: totalIn, TotalOutputTokens: totalOut,
		Adjacency: adj, LargeObs: obs, LogVolume: logs, Compaction: comp,
	}
	traffic := totalIn + totalOut
	pct := func(saved int) float64 {
		if traffic == 0 {
			return 0
		}
		return float64(saved) / float64(traffic) * 100
	}
	r.Projections = []MechanismProjection{
		{
			Mechanism:  "action-fusion",
			SavedTokens: adj.SavedPromptTokens + adj.SavedCompletionTokens,
			TrafficPct: pct(adj.SavedPromptTokens + adj.SavedCompletionTokens),
			Basis: fmt.Sprintf("%d edit-then-command cycles; savings are the intervening request's tokens",
				adj.Cycles),
		},
		{
			Mechanism:  "observation-pack",
			SavedTokens: obs.ResendBytes / researchBytesPerToken,
			TrafficPct: float64(obs.ResendBytes/researchBytesPerToken) / float64(max(totalIn, 1)) * 100,
			Basis: fmt.Sprintf("%d oversized results, %d re-sent bytes after their first two requests (bytes/4 token estimate)",
				obs.Results, obs.ResendBytes),
		},
		{
			Mechanism:  "log-reducer",
			SavedTokens: logs.ResendBytes / researchBytesPerToken,
			TrafficPct: float64(logs.ResendBytes/researchBytesPerToken) / float64(max(totalIn, 1)) * 100,
			Basis: fmt.Sprintf("%d build/test logs, %d re-sent bytes; overlaps observation-pack headroom (not additive)",
				logs.Results, logs.ResendBytes),
		},
		{
			Mechanism:     "compaction-reminder",
			Informational: true,
			Basis: fmt.Sprintf("%d compaction boundaries; max prompt tokens before one: %d; informational in slice 1",
				len(comp.Compactions), comp.MaxPromptTokensSeen),
		},
	}
	return r
}

func renderReport(r *OracleReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "oracle report (%s)\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "sessions: %d, entries: %d, skipped lines: %d\n", r.Sessions, r.Entries, r.SkippedLines)
	fmt.Fprintf(&b, "recorded input tokens: %d, output tokens: %d\n", r.TotalInputTokens, r.TotalOutputTokens)
	fmt.Fprintf(&b, "compaction boundaries: %d\n\n", len(r.Compaction.Compactions))
	fmt.Fprintf(&b, "projected savings (per mechanism, upper bounds; observation-pack and log-reducer overlap):\n")
	best := 0.0
	for _, p := range r.Projections {
		if p.Informational {
			fmt.Fprintf(&b, "  - %s: informational. %s\n", p.Mechanism, p.Basis)
			continue
		}
		fmt.Fprintf(&b, "  - %s: %d tokens (%.2f%% of recorded traffic). %s\n", p.Mechanism, p.SavedTokens, p.TrafficPct, p.Basis)
		if p.TrafficPct > best {
			best = p.TrafficPct
		}
	}
	fmt.Fprintf(&b, "\nstop-or-go: best non-informational projection is %.2f%%; the go threshold is 5%%.\n", best)
	if best < 5 {
		fmt.Fprintf(&b, "VERDICT: best mechanism projects under 5%% of recorded traffic. Stop and report before building the spine.\n")
	} else {
		fmt.Fprintf(&b, "VERDICT: headroom above the 5%% threshold. Proceed to the spine.\n")
	}
	return b.String()
}

// RunOracle measures a corpus and appends the report record to opts.OutPath.
func RunOracle(opts OracleOptions) (*OracleReport, error) {
	transcripts, err := walkSessionTranscripts(opts.StateBase, opts.Limit)
	if err != nil {
		return nil, err
	}
	report := &OracleReport{GeneratedAt: time.Now().UTC(), StateBase: opts.StateBase, Sessions: len(transcripts)}
	for _, st := range transcripts {
		entries, skipped, err := loadEntries(st.Path)
		if err != nil {
			// Unreadable single sessions do not sink the corpus run.
			fmt.Fprintf(opts.Stdout, "skipping unreadable transcript %s: %v\n", st.Path, err)
			continue
		}
		report.SkippedLines += skipped
		report.Entries += len(entries)
		adj := measureAdjacency(entries)
		report.Adjacency.Cycles += adj.Cycles
		report.Adjacency.SavedPromptTokens += adj.SavedPromptTokens
		report.Adjacency.SavedCompletionTokens += adj.SavedCompletionTokens
		obs := measureLargeObservations(entries, 10*1024)
		report.LargeObs.Results += obs.Results
		report.LargeObs.TotalBytes += obs.TotalBytes
		report.LargeObs.ResendBytes += obs.ResendBytes
		report.LargeObs.ResendRequests += obs.ResendRequests
		logs := measureLogVolume(entries, 4*1024)
		report.LogVolume.Results += logs.Results
		report.LogVolume.TotalBytes += logs.TotalBytes
		report.LogVolume.ResendBytes += logs.ResendBytes
		report.LogVolume.ResendRequests += logs.ResendRequests
		for _, p := range measureCompaction(entries).Compactions {
			report.Compaction.Compactions = append(report.Compaction.Compactions, p)
		}
		for i := range entries {
			if entries[i].Turn.Kind == schema.TurnAssistant {
				report.TotalInputTokens += entries[i].Turn.Usage.InputTokens
				report.TotalOutputTokens += entries[i].Turn.Usage.OutputTokens
			}
		}
	}
	final := buildReport(opts.StateBase, report.Sessions, report.Entries, report.SkippedLines,
		report.Adjacency, report.LargeObs, report.LogVolume, report.Compaction,
		report.TotalInputTokens, report.TotalOutputTokens)
	if opts.OutPath != "" {
		line, err := json.Marshal(final)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(opts.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open oracle ledger: %w", err)
		}
		defer f.Close()
		if _, err := f.Write(append(line, '\n')); err != nil {
			return nil, err
		}
	}
	fmt.Fprint(opts.Stdout, renderReport(final))
	return final, nil
}
```

Note: `buildReport` re-derives GeneratedAt internally; remove the redundant early `report :=` construction in `RunOracle` and use the counts via a small struct or by calling `buildReport` directly (the implementer should end with ONE report object, not two; keep `RunOracle` accumulating raw counts in locals and calling `buildReport` once). Use `max` (Go 1.21+ builtin; the module is on go 1.27).

In `cmd/evener-dev/research.go`, replace the `researchOracleCmd` var with a function:

```go
func researchOracleCmd(args []string) int {
	fs := flag.NewFlagSet("evener-dev research oracle", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "state base to walk (default: resolved like evener doctor)")
	limit := fs.Int("limit", 30, "newest N sessions (0 = all)")
	out := fs.String("out", "", "append the report record to this JSONL path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := *stateDir
	if base == "" {
		base = doctor.ResolveStateBase("")
	}
	rep, err := research.RunOracle(research.OracleOptions{
		StateBase: base, Limit: *limit, OutPath: *out, Stdout: os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = rep
	return 0
}
```

Imports: `flag`, `os`, `primeradiant.com/evener/agent/doctor`, `primeradiant.com/evener/agent/research`. Check `doctor.ResolveStateBase("")` behavior on this machine (`rg -n "func ResolveStateBase" -A 15 agent/doctor/statedir.go`): if it requires a non-empty override or an env var, pass the right default; the CLI must resolve the real default state home when no flag is given.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/research/ ./cmd/evener-dev/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research/oracle.go agent/research/oracle_test.go cmd/evener-dev/research.go cmd/evener-dev/research_test.go
git commit -m "research: compaction signal, report assembly, projections, oracle CLI"
```

---

### Task 6: Smoke environments (committed fixtures)

**Files:**
- Create: `research/environments/smoke-fix/task.md`
- Create: `research/environments/smoke-fix/verify.sh`
- Create: `research/environments/smoke-fix/repo/go.mod`
- Create: `research/environments/smoke-fix/repo/calc/calc.go`
- Create: `research/environments/smoke-fix/repo/calc/calc_test.go`
- Create: `research/environments/smoke-feature/task.md`
- Create: `research/environments/smoke-feature/verify.sh`
- Create: `research/environments/smoke-feature/repo/go.mod`
- Create: `research/environments/smoke-feature/repo/stats/stats.go`
- Test: `agent/research/envs_test.go`

**Interfaces:**
- Produces: two valid environments with the directory contract `task.md` + `verify.sh` (executable) + `repo/` (nested Go module). Consumed by Task 7's `loadEnvironment`.

- [ ] **Step 1: Create smoke-fix contents**

`research/environments/smoke-fix/task.md`:

```markdown
The test in calc/calc_test.go fails because Average in calc/calc.go has a
bug. Find the bug and fix calc/calc.go so `go test ./...` passes. Do not
modify the test file.
```

`research/environments/smoke-fix/repo/go.mod`:

```
module research.local/smoke-fix

go 1.27
```

`research/environments/smoke-fix/repo/calc/calc.go`:

```go
package calc

// Average returns the arithmetic mean of xs. It returns 0 for an empty slice.
func Average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	// BUG: divides by len(xs)+1, skewing every result.
	return sum / float64(len(xs)+1)
}
```

(The bug must be visible: `sum` is computed correctly below. Final file:)

```go
package calc

// Average returns the arithmetic mean of xs. It returns 0 for an empty slice.
func Average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	// BUG: divides by len(xs)+1, skewing every result.
	return sum / float64(len(xs)+1)
}
```

Ship the second version (with the loop). The first snippet above is wrong on purpose: the test asserts a correct average, and the fix is changing the divisor to `float64(len(xs))`.

`research/environments/smoke-fix/repo/calc/calc_test.go`:

```go
package calc

import "testing"

func TestAverage(t *testing.T) {
	got := Average([]float64{1, 2, 3})
	if got != 2 {
		t.Fatalf("Average([1,2,3]) = %v, want 2", got)
	}
	if Average(nil) != 0 {
		t.Fatal("Average(nil) must be 0")
	}
}
```

`research/environments/smoke-fix/verify.sh` (chmod 0755):

```sh
#!/bin/sh
# Verifier for smoke-fix: the task succeeded when the repo's tests pass.
# Argument 1 is the run's working directory (a copy of repo/).
set -e
cd "$1"
exec go test ./...
```

- [ ] **Step 2: Create smoke-feature contents**

`research/environments/smoke-feature/task.md`:

```markdown
The stats package in repo/stats/stats.go exists but is empty. Add an
exported function Median(xs []float64) float64 to package stats that
returns the median of xs (the middle element of the sorted slice for odd
length, the mean of the two middle elements for even length, and 0 for an
empty slice). The repository's own tests must keep passing.
```

`research/environments/smoke-feature/repo/go.mod`:

```
module research.local/smoke-feature

go 1.27
```

`research/environments/smoke-feature/repo/stats/stats.go`:

```go
package stats
```

`research/environments/smoke-feature/verify.sh` (chmod 0755):

```sh
#!/bin/sh
# Verifier for smoke-feature: success is defined by this probe test, not by
# a test the agent can read (the paper's verifier-driven pattern).
set -e
cd "$1"
cat > stats/probe_verify_test.go <<'EOF'
package stats

import "testing"

func TestProbeMedian(t *testing.T) {
	if Median([]float64{3, 1, 2}) != 2 {
		t.Fatalf("Median([3,1,2]) = %v, want 2", Median([]float64{3, 1, 2}))
	}
	if Median([]float64{4, 1, 3, 2}) != 2.5 {
		t.Fatalf("Median([4,1,3,2]) = %v, want 2.5", Median([]float64{4, 1, 3, 2}))
	}
	if Median(nil) != 0 {
		t.Fatal("Median(nil) must be 0")
	}
}
EOF
exec go test ./...
```

- [ ] **Step 3: Write the failing validation test**

`agent/research/envs_test.go`:

```go
package research

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommittedEnvironmentsValid walks every committed environment under
// research/environments/ and enforces the directory contract.
func TestCommittedEnvironmentsValid(t *testing.T) {
	root := repoRoot(t)
	envDir := filepath.Join(root, "research", "environments")
	entries, err := os.ReadDir(envDir)
	if err != nil {
		t.Fatalf("read env dir: %v", err)
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		found++
		dir := filepath.Join(envDir, e.Name())
		task, err := os.ReadFile(filepath.Join(dir, "task.md"))
		if err != nil || len(strings.TrimSpace(string(task))) == 0 {
			t.Errorf("%s: task.md missing or empty", e.Name())
		}
		st, err := os.Stat(filepath.Join(dir, "verify.sh"))
		if err != nil || st.Mode()&0o111 == 0 {
			t.Errorf("%s: verify.sh missing or not executable", e.Name())
		}
		if _, err := os.Stat(filepath.Join(dir, "repo", "go.mod")); err != nil {
			t.Errorf("%s: repo/go.mod missing", e.Name())
		}
		// The fixture repo must build offline.
		cmd := exec.Command("go", "build", "./...")
		cmd.Dir = filepath.Join(dir, "repo")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: fixture repo does not build: %v\n%s", e.Name(), err, out)
		}
	}
	if found < 2 {
		t.Fatalf("expected at least 2 committed environments, found %d", found)
	}
}

// repoRoot walks up from the working directory to the repo root (the dir
// containing go.work).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.work)")
		}
		dir = parent
	}
}
```

- [ ] **Step 4: Run the test and the full gate check**

Run: `go test ./agent/research/ -run TestCommittedEnvironmentsValid -v`
Expected: PASS.

Then verify the fixtures do not leak into the workspace build:

```bash
go build ./... && go vet ./... && go test ./cmd/evener-dev/ ./agent/research/
```

Expected: all pass; the nested `research.local/*` modules are outside `go.work` so root `./...` must not descend into them. If any root-scoped command tries to compile fixture files, the fix is at the module boundary (nested go.mod already does this); investigate before proceeding, do not paper over with ignore files.

- [ ] **Step 5: Commit**

```bash
git add research/environments agent/research/envs_test.go
git commit -m "research: two verifier-driven smoke environments"
```

---

### Task 7: ATIF metrics extraction

**Files:**
- Create: `agent/research/metrics.go`
- Test: `agent/research/metrics_test.go`

**Interfaces:**
- Produces:

```go
// AtifMetrics summarizes one rollout's recorded traffic.
type AtifMetrics struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	Steps            int // ATIF TotalSteps
	ModelRequests    int // steps carrying per-step metrics
}

// ExtractAtifMetrics decodes an ATIF v1.7 export written by `evener run -n`.
func ExtractAtifMetrics(path string) (AtifMetrics, error)
```

- Consumes: `agent/internal/atif` (`Trajectory`, `FinalMetrics`, `Step`).

- [ ] **Step 1: Write the failing test**

```go
package research

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAtifMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.atif.json")
	content := `{
  "schema_version": "1.7",
  "session_id": "s1",
  "steps": [
    {"step_id": 1, "source": "model", "metrics": {"prompt_tokens": 100, "completion_tokens": 20, "cached_tokens": 10}},
    {"step_id": 2, "source": "tool", "metrics": null},
    {"step_id": 3, "source": "model", "metrics": {"prompt_tokens": 200, "completion_tokens": 30, "cached_tokens": 0}}
  ],
  "final_metrics": {"total_prompt_tokens": 300, "total_completion_tokens": 50, "total_cached_tokens": 10, "total_steps": 3}
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ExtractAtifMetrics(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.PromptTokens != 300 || m.CompletionTokens != 50 || m.CachedTokens != 10 {
		t.Fatalf("totals wrong: %+v", m)
	}
	if m.Steps != 3 {
		t.Fatalf("steps = %d, want 3", m.Steps)
	}
	if m.ModelRequests != 2 {
		t.Fatalf("model requests = %d, want 2 (steps with metrics)", m.ModelRequests)
	}
}

func TestExtractAtifMetrics_MissingFileIsError(t *testing.T) {
	if _, err := ExtractAtifMetrics(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing file must error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/research/ -run TestExtractAtif -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

```go
package research

import (
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/evener/agent/internal/atif"
)

type AtifMetrics struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	Steps            int
	ModelRequests    int
}

func ExtractAtifMetrics(path string) (AtifMetrics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AtifMetrics{}, fmt.Errorf("read atif export: %w", err)
	}
	var traj atif.Trajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		return AtifMetrics{}, fmt.Errorf("decode atif export: %w", err)
	}
	m := AtifMetrics{Steps: len(traj.Steps)}
	if traj.FinalMetrics != nil {
		m.PromptTokens = traj.FinalMetrics.TotalPromptTokens
		m.CompletionTokens = traj.FinalMetrics.TotalCompletionTokens
		m.CachedTokens = traj.FinalMetrics.TotalCachedTokens
		m.Steps = traj.FinalMetrics.TotalSteps
		if m.Steps == 0 {
			m.Steps = len(traj.Steps)
		}
	}
	for _, s := range traj.Steps {
		if s.Metrics != nil {
			m.ModelRequests++
		}
	}
	return m, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/research/ -run TestExtractAtif -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research/metrics.go agent/research/metrics_test.go
git commit -m "research: ATIF metrics extraction for rollout rows"
```

---

### Task 8: Runner core (offline mechanics)

**Files:**
- Create: `agent/research/runner.go`
- Test: `agent/research/runner_test.go`

**Interfaces:**
- Produces:

```go
// environment is one committed research environment.
type environment struct {
	Name        string
	Dir         string // environment directory
	TaskPrompt  string // task.md contents
	RepoDir     string // <dir>/repo, copied per run
	VerifyPath  string // <dir>/verify.sh
}

// loadEnvironment validates and loads one environment directory.
func loadEnvironment(dir string) (environment, error)

// SessionRun describes one rollout execution.
type SessionRun struct {
	Env       string
	WorkDir   string // prepared copy of repo/
	StateDir  string // isolated per-run state dir
	Model     string // provider/model, or "" when the executor stubs
	MaxRounds int
	AtifPath  string // where the executor must write the ATIF export
	Prompt    string
}

// sessionExecutor runs one session. An error return is an infra failure:
// the run is excluded from gates, never counted as a task failure.
type sessionExecutor interface {
	Run(ctx context.Context, run SessionRun) error
}

// ExecExecutor runs the real harness binary headless.
type ExecExecutor struct {
	Binary string
	Live   bool
}

func (e ExecExecutor) Run(ctx context.Context, run SessionRun) error

// RunRow is one ledger row (plain JSONL in slice 1; slice 2 adds chaining).
type RunRow struct {
	Env         string    `json:"env"`
	Arm         string    `json:"arm"`
	Rep         int       `json:"rep"`
	Model       string    `json:"model,omitempty"`
	Binary      string    `json:"binary,omitempty"`
	VerifyPass  bool      `json:"verify_pass"`
	InfraFail   bool      `json:"infra_fail"`
	Metrics     AtifMetrics `json:"metrics"`
	AtifPath    string    `json:"atif_path"`
	Timestamp   time.Time `json:"timestamp"`
}

type RolloutOptions struct {
	EnvDir      string // research/environments
	Envs        []string
	Arm         string // recorded in rows (base|candidate)
	Model       string
	Binary      string
	Runs        int // repetitions per env
	MaxRounds   int
	RunDir      string // scratch run dir: runs.jsonl + per-run subdirs
	Live        bool
	MaxLiveRuns int // total planned runs cap (applies offline too)
	Stdout      io.Writer
}

// RunRollouts prepares workdirs, executes sessions, verifies, and appends rows.
func RunRollouts(ctx context.Context, opts RolloutOptions, exec sessionExecutor) error
```

- Consumes: Tasks 6, 7.

- [ ] **Step 1: Write the failing test**

```go
package research

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubExecutor stands in for the real binary. It claims the task done by
// applying the known fix to the workdir and writing a canned ATIF export.
type stubExecutor struct {
	calls []SessionRun
}

func (s *stubExecutor) Run(_ context.Context, run SessionRun) error {
	s.calls = append(s.calls, run)
	fix := filepath.Join(run.WorkDir, "calc", "calc.go")
	if _, err := os.Stat(fix); err == nil {
		_ = os.WriteFile(fix, []byte("package calc\n\nfunc Average(xs []float64) float64 {\n\tif len(xs) == 0 {\n\t\treturn 0\n\t}\n\tsum := 0.0\n\tfor _, x := range xs {\n\t\tsum += x\n\t}\n\treturn sum / float64(len(xs))\n}\n"), 0o644)
	}
	return os.WriteFile(run.AtifPath, []byte(`{"schema_version":"1.7","session_id":"x","steps":[],"final_metrics":{"total_prompt_tokens":1,"total_completion_tokens":1,"total_cached_tokens":0,"total_steps":0}}`), 0o644)
}

func repoRootForEnvs(t *testing.T) string { return repoRoot(t) }

func TestRunRollouts_OfflineMechanics(t *testing.T) {
	root := repoRootForEnvs(t)
	runDir := t.TempDir()
	stub := &stubExecutor{}
	opts := RolloutOptions{
		EnvDir:      filepath.Join(root, "research", "environments"),
		Envs:        []string{"smoke-fix"},
		Arm:         "base",
		Runs:        2,
		MaxRounds:   20,
		RunDir:      runDir,
		MaxLiveRuns: 48,
		Stdout:      os.Stderr,
	}
	if err := RunRollouts(context.Background(), opts, stub); err != nil {
		t.Fatal(err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("executor calls = %d, want 2", len(stub.calls))
	}
	// The fixture repo must not be mutated: the workdir is a copy.
	orig, err := os.ReadFile(filepath.Join(root, "research", "environments", "smoke-fix", "repo", "calc", "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(orig), "len(xs)+1") {
		t.Fatal("fixture repo was mutated by the runner")
	}
	// Rows land in runs.jsonl with verify results and metrics.
	data, err := os.ReadFile(filepath.Join(runDir, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("rows = %d, want 2", len(lines))
	}
	var row RunRow
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row.Env != "smoke-fix" || row.Arm != "base" || !row.VerifyPass {
		t.Fatalf("row = %+v", row)
	}
	if row.Metrics.PromptTokens != 1 {
		t.Fatalf("metrics not extracted: %+v", row.Metrics)
	}
	// Workdir prompt came from task.md.
	if !strings.Contains(stub.calls[0].Prompt, "Average") {
		t.Fatal("prompt did not carry task.md contents")
	}
}

func TestRunRollouts_EnforcesCap(t *testing.T) {
	root := repoRootForEnvs(t)
	opts := RolloutOptions{
		EnvDir:      filepath.Join(root, "research", "environments"),
		Envs:        []string{"smoke-fix", "smoke-feature"},
		Runs:        2,
		RunDir:      t.TempDir(),
		MaxLiveRuns: 3, // 2 envs x 2 runs = 4 planned > 3
		Stdout:      os.Stderr,
	}
	err := RunRollouts(context.Background(), opts, &stubExecutor{})
	if err == nil || !strings.Contains(err.Error(), "max-live-runs") {
		t.Fatalf("want max-live-runs cap error, got %v", err)
	}
}

func TestRunRollouts_InfraFailExcludedFromVerifyVerdict(t *testing.T) {
	root := repoRootForEnvs(t)
	stub := &failingExecutor{}
	opts := RolloutOptions{
		EnvDir:    filepath.Join(root, "research", "environments"),
		Envs:      []string{"smoke-fix"},
		Runs:      1,
		RunDir:    t.TempDir(),
		MaxRounds: 20,
		Stdout:    os.Stderr,
	}
	if err := RunRollouts(context.Background(), opts, stub); err != nil {
		t.Fatalf("infra failures must not abort the pass: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(opts.RunDir, "runs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row RunRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err)
	}
	if !row.InfraFail {
		t.Fatalf("row must record infra failure: %+v", row)
	}
}

type failingExecutor struct{}

func (failingExecutor) Run(context.Context, SessionRun) error {
	return errors.New("provider stream cut")
}
```

Add `errors` to imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/research/ -run TestRunRollouts -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

```go
package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"primeradiant.com/evener/agent/internal/liveeval"
)

type environment struct {
	Name       string
	Dir        string
	TaskPrompt string
	RepoDir    string
	VerifyPath string
}

func loadEnvironment(dir string) (environment, error) {
	name := filepath.Base(dir)
	task, err := os.ReadFile(filepath.Join(dir, "task.md"))
	if err != nil {
		return environment{}, fmt.Errorf("%s: task.md: %w", name, err)
	}
	verify := filepath.Join(dir, "verify.sh")
	st, err := os.Stat(verify)
	if err != nil {
		return environment{}, fmt.Errorf("%s: verify.sh: %w", name, err)
	}
	if st.Mode()&0o111 == 0 {
		return environment{}, fmt.Errorf("%s: verify.sh not executable", name)
	}
	repo := filepath.Join(dir, "repo")
	if _, err := os.Stat(filepath.Join(repo, "go.mod")); err != nil {
		return environment{}, fmt.Errorf("%s: repo/go.mod: %w", name, err)
	}
	return environment{Name: name, Dir: dir, TaskPrompt: string(task), RepoDir: repo, VerifyPath: verify}, nil
}

type SessionRun struct {
	Env       string
	WorkDir   string
	StateDir  string
	Model     string
	MaxRounds int
	AtifPath  string
	Prompt    string
}

type sessionExecutor interface {
	Run(ctx context.Context, run SessionRun) error
}

// ExecExecutor runs the harness binary headless:
//
//	evener --model <model> --state-dir <dir> --max-rounds <n> -n <atif> <prompt>
//
// with the process working directory set to the run workdir.
type ExecExecutor struct {
	Binary string
	Live   bool
}

func (e ExecExecutor) Run(ctx context.Context, run SessionRun) error {
	args := []string{
		"--state-dir", run.StateDir,
		"--max-rounds", strconv.Itoa(run.MaxRounds),
		"-n", run.AtifPath,
	}
	if run.Model != "" {
		args = append(args, "--model", run.Model)
	}
	args = append(args, run.Prompt)
	cmd := exec.CommandContext(ctx, e.Binary, args...)
	cmd.Dir = run.WorkDir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

type RunRow struct {
	Env        string      `json:"env"`
	Arm        string      `json:"arm"`
	Rep        int         `json:"rep"`
	Model      string      `json:"model,omitempty"`
	Binary     string      `json:"binary,omitempty"`
	VerifyPass bool        `json:"verify_pass"`
	InfraFail  bool        `json:"infra_fail"`
	Metrics    AtifMetrics `json:"metrics"`
	AtifPath   string      `json:"atif_path"`
	Timestamp  time.Time   `json:"timestamp"`
}

type RolloutOptions struct {
	EnvDir      string
	Envs        []string
	Arm         string
	Model       string
	Binary      string
	Runs        int
	MaxRounds   int
	RunDir      string
	Live        bool
	MaxLiveRuns int
	Stdout      io.Writer
}

// liveGuard refuses a live rollout pass without the explicit opt-in.
func liveGuard(live bool) error {
	if !live {
		return nil
	}
	if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) {
		return fmt.Errorf("live rollouts require %s=1", liveeval.OptInEnv)
	}
	return nil
}

// copyTree copies a directory tree (regular files and dirs only).
func copyTree(dst, src string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, st.Mode().Perm())
	})
}

func RunRollouts(ctx context.Context, opts RolloutOptions, ex sessionExecutor) error {
	if err := liveGuard(opts.Live); err != nil {
		return err
	}
	if len(opts.Envs) == 0 {
		return errors.New("no environments selected")
	}
	runs := opts.Runs
	if runs <= 0 {
		runs = 1
	}
	maxRuns := opts.MaxLiveRuns
	if maxRuns <= 0 {
		maxRuns = 48
	}
	planned := len(opts.Envs) * runs
	if planned > maxRuns {
		return fmt.Errorf("planned %d runs exceed --max-live-runs %d", planned, maxRuns)
	}
	if err := os.MkdirAll(opts.RunDir, 0o755); err != nil {
		return err
	}
	ledger, err := os.OpenFile(filepath.Join(opts.RunDir, "runs.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer ledger.Close()
	for _, envName := range opts.Envs {
		env, err := loadEnvironment(filepath.Join(opts.EnvDir, envName))
		if err != nil {
			return err
		}
		for rep := 1; rep <= runs; rep++ {
			row, err := runOnce(ctx, opts, env, rep, ex)
			if err != nil {
				return err
			}
			line, err := json.Marshal(row)
			if err != nil {
				return err
			}
			if _, err := ledger.Write(append(line, '\n')); err != nil {
				return err
			}
		}
	}
	return nil
}

func runOnce(ctx context.Context, opts RolloutOptions, env environment, rep int, ex sessionExecutor) (RunRow, error) {
	row := RunRow{
		Env: env.Name, Arm: opts.Arm, Rep: rep,
		Model: opts.Model, Binary: opts.Binary,
		Timestamp: time.Now().UTC(),
	}
	runDir := filepath.Join(opts.RunDir, env.Name, fmt.Sprintf("%s-%02d", opts.Arm, rep))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return row, err
	}
	workDir := filepath.Join(runDir, "work")
	if err := copyTree(workDir, env.RepoDir); err != nil {
		return row, err
	}
	stateDir := filepath.Join(runDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return row, err
	}
	atifPath := filepath.Join(runDir, "run.atif.json")
	run := SessionRun{
		Env: env.Name, WorkDir: workDir, StateDir: stateDir,
		Model: opts.Model, MaxRounds: opts.MaxRounds,
		AtifPath: atifPath, Prompt: env.TaskPrompt,
	}
	if err := ex.Run(ctx, run); err != nil {
		row.InfraFail = true
		row.AtifPath = atifPath
		return row, nil // infra failure: recorded, not fatal to the pass
	}
	row.AtifPath = atifPath
	row.Metrics, _ = ExtractAtifMetrics(atifPath)
	verify := exec.CommandContext(ctx, env.VerifyPath, workDir)
	if err := verify.Run(); err != nil {
		row.VerifyPass = false
	} else {
		row.VerifyPass = true
	}
	return row, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/research/ -run TestRunRollouts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research/runner.go agent/research/runner_test.go
git commit -m "research: offline rollout runner mechanics with verify and ledger rows"
```

---

### Task 9: Rollout CLI wiring

**Files:**
- Modify: `cmd/evener-dev/research.go`
- Test: `cmd/evener-dev/research_test.go` (append)

**Interfaces:**
- Produces: `researchRolloutCmd(args []string) int` parsing flags and calling `research.RunRollouts` with `research.ExecExecutor`.
- Consumes: Task 8.

- [ ] **Step 1: Write the failing test**

```go
func TestResearchRolloutCmd_FlagErrorsFailFast(t *testing.T) {
	// A live pass without the opt-in env var must exit nonzero without
	// executing anything.
	code := researchRolloutCmd([]string{
		"--env-dir", "research/environments",
		"--env", "smoke-fix",
		"--model", "lunaroute/deepseek-4.1-flash",
		"--live",
		"--run-dir", t.TempDir(),
	})
	if code == 0 {
		t.Fatal("live rollout without EVENER_LIVE_TESTS must not exit 0")
	}
}
```

(The test process does not set `EVENER_LIVE_TESTS`, so the guard must trip. Check `agent/internal/liveeval.Enabled` semantics: it compares `== "1"`. If the ambient environment could set it in CI, set `t.Setenv(liveeval.OptInEnv, "")` first — the dev package may import `agent/internal/liveeval` for the constant; if the import is not allowed by package-import lint rules in `cmd/`, inline the literal `"EVENER_LIVE_TESTS"` with a comment naming the constant instead.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/evener-dev/ -run TestResearchRolloutCmd -v`
Expected: FAIL (the placeholder var exits 2 without touching flags; the test fails only if the guard is missing — assert instead on a distinctive stderr message. Adjust: the placeholder returns 2 and prints usage; that satisfies `code != 0`, so this test only becomes meaningful once wiring exists. Keep it, and extend the assertion after wiring: `strings.Contains(stderrText, "EVENER_LIVE_TESTS")` using the dev package's error writer.)

- [ ] **Step 3: Write minimal implementation**

Replace the `researchRolloutCmd` var with:

```go
func researchRolloutCmd(args []string) int {
	fs := flag.NewFlagSet("evener-dev research rollout", flag.ContinueOnError)
	envDir := fs.String("env-dir", "research/environments", "environments directory")
	var envs multiFlag
	fs.Var(&envs, "env", "environment name (repeatable)")
	model := fs.String("model", "", "provider/model for the session")
	binary := fs.String("binary", "evener", "harness binary to run")
	arm := fs.String("arm", "base", "arm label recorded in run rows")
	runs := fs.Int("runs", 1, "repetitions per environment")
	maxRounds := fs.Int("max-rounds", 40, "session round cap")
	runDir := fs.String("run-dir", "", "scratch run directory (required)")
	live := fs.Bool("live", false, "enable a live provider-backed pass")
	maxLiveRuns := fs.Int("max-live-runs", 48, "cap on total planned runs")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runDir == "" {
		fmt.Fprintln(os.Stderr, "--run-dir is required")
		return 2
	}
	if len(envs) == 0 {
		fmt.Fprintln(os.Stderr, "--env is required (at least one)")
		return 2
	}
	ctx := context.Background()
	err := research.RunRollouts(ctx, research.RolloutOptions{
		EnvDir: *envDir, Envs: envs, Arm: *arm, Model: *model, Binary: *binary,
		Runs: *runs, MaxRounds: *maxRounds, RunDir: *runDir,
		Live: *live, MaxLiveRuns: *maxLiveRuns, Stdout: os.Stdout,
	}, research.ExecExecutor{Binary: *binary, Live: *live})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// multiFlag collects repeated string flag values.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
```

Imports: add `context`, `strings`.

Note: `ExecExecutor.Live` exists to document intent; the guard lives in `RunRollouts` via `opts.Live`. The stub-executor tests bypass the executor entirely, so no live flag plumbing reaches them.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/evener-dev/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-dev/research.go cmd/evener-dev/research_test.go
git commit -m "dev: research rollout CLI wiring"
```

---

### Task 10: Smoke end-to-end test with a real session (scripted model, offline)

**Files:**
- Create: `agent/research_smoke_env_e2e_test.go` (package `agent`)

This test proves the smoke env is solvable by the real harness offline: a scripted model drives real tool execution (real file edits, real `go test`) in a copy of the fixture repo, and the committed verifier passes afterward. It lives in package `agent` because the testkit (`newSession`, `fakeAdapter`) is package-local.

**Interfaces:**
- Consumes: `agent/testkit_test.go` helpers (`newSession`, `fakeAdapter`), Task 6 fixtures, `research.loadEnvironment` / `research.RunRollouts` verify path (invoke the verifier script directly with `exec`).

- [ ] **Step 1: Write the failing test**

```go
package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/research"
	"primeradiant.com/evener/llm"
)

// TestResearchSmokeFix_SolvableByScriptedSession runs a real session with
// real tools against a copy of the smoke-fix fixture and then runs the
// committed verifier. It is the offline proof that the environment is
// solvable and the verifier is correct.
func TestResearchSmokeFix_SolvableByScriptedSession(t *testing.T) {
	root := repoRootForResearch(t)
	envDir := filepath.Join(root, "research", "environments", "smoke-fix")
	work := t.TempDir()
	if err := copyTreeForResearch(filepath.Join(work, "repo"), filepath.Join(envDir, "repo")); err != nil {
		t.Fatal(err)
	}
	task, err := os.ReadFile(filepath.Join(envDir, "task.md"))
	if err != nil {
		t.Fatal(err)
	}

	fixed := "package calc\n\nfunc Average(xs []float64) float64 {\n\tif len(xs) == 0 {\n\t\treturn 0\n\t}\n\tsum := 0.0\n\tfor _, x := range xs {\n\t\tsum += x\n\t}\n\treturn sum / float64(len(xs))\n}\n"
	calcPath := filepath.Join(work, "repo", "calc", "calc.go")

	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		// Round 1: run the failing test.
		func(r llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call1", Name: "shell",
					Arguments: json.RawMessage(`{"command":"go test ./...","description":"run tests"}`)}},
			}}}
		},
		// Round 2: fix the file.
		func(r llm.Request) llm.Response {
			args := map[string]any{
				"file_path": calcPath,
				"old_string": "\treturn sum / float64(len(xs)+1)",
				"new_string": "\treturn sum / float64(len(xs))",
				"intent":     "fix the divisor so Average is the arithmetic mean",
			}
			raw, _ := json.Marshal(args)
			return llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call2", Name: "edit_file", Arguments: raw}},
			}}}
		},
		// Round 3: run the test again, then stop with a text answer.
		func(r llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{Role: "assistant", Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call3", Name: "shell",
					Arguments: json.RawMessage(`{"command":"go test ./...","description":"confirm fix"}`)}},
			}}}
		},
		func(r llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("Fixed: Average divided by len(xs)+1; changed to len(xs). Tests pass.")}
		},
	}})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(filepath.Join(work, "repo")), SessionConfig{
		NonInteractive: true,
		StateDir:       dir,
		AgentsDocPath:  filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, string(task), nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The file must now be fixed in the workdir.
	got, err := os.ReadFile(calcPath)
	if err != nil || string(got) != fixed {
		t.Fatalf("workdir not fixed:\n%s\nerr=%v", got, err)
	}
	// The committed verifier must pass against the workdir.
	verify := exec.Command(filepath.Join(envDir, "verify.sh"), filepath.Join(work, "repo"))
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("verify.sh failed: %v\n%s", err, out)
	}
}

// repoRootForResearch and copyTreeForResearch are small local helpers; if the
// package already has equivalents (search first with rg), reuse them instead.
func repoRootForResearch(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func copyTreeForResearch(dst, src string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
```

Before writing, check two things: (1) whether the `execenv` import in the test needs the full path `primeradiant.com/evener/agent/execenv` (it does; the package agent test files import it as `execenv`), and (2) whether `fakeAdapter` step functions can return tool-call responses the session dispatches (yes; several workflow tests do exactly this — run `rg -n "ContentToolCall" agent/*_test.go | head` and mirror the closest existing example if the construction above hits a validation difference). Also verify the session config field names (`NonInteractive`, `StateDir`, `AgentsDocPath`) against an existing test that sets them (`rg -n "AgentsDocPath:" agent/*_test.go | head -3`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestResearchSmokeFix -v -count=1`
Expected: FAIL initially (helper name collisions or fixture mismatches surface here; fix them; the test itself should then pass only when the session loop executes the scripted tool calls correctly).

- [ ] **Step 3: Make the test pass (implementation changes if needed)**

No production code should be needed: this test validates fixtures and the scripted loop. If it fails on session behavior (tool schema validation of scripted arguments, sandbox refusal), fix the scripted arguments to match the real tool schemas (`agent/internal/tool/definitions.go`), not the harness. If ProcessInput's loop needs a `MaxRounds` config default raised, set it in SessionConfig.

- [ ] **Step 4: Run the full agent package short tests**

Run: `go test ./agent/ -run TestResearchSmokeFix -v -count=1` then `go test ./agent/ -count=1 -short`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/research_smoke_env_e2e_test.go
git commit -m "research: offline end-to-end proof that smoke-fix is solvable by a scripted session"
```

---

### Task 11: Full gates and Phase 0 stop-or-go report

**Files:**
- No new files. This task runs the gates and the real oracle, then reports.

- [ ] **Step 1: Run the full gates**

Run:

```bash
make lint
make vet
make test
```

Expected: all green. Any failure is fixed at its root cause before proceeding (including pre-existing failures: broken windows rule). Note `make test` runs the full multi-module gate plus the frontend gate; budget time accordingly. If `make test` requires the frontend install (`make test-web` preflight), let it run; never `npm ci` through a symlinked `node_modules` (see AGENTS.md).

- [ ] **Step 2: Run the oracle on real data (bounded sample)**

```bash
go run ./cmd/evener-dev research oracle --limit 30 \
  --out "$EVENER_SCRATCH_DIR/oracle-ledger.jsonl" 2>&1 | tee "$EVENER_SCRATCH_DIR/oracle-report.txt"
```

Read the rendered verdict. If the default state base does not resolve, pass `--state-dir "$HOME/.local/state/evener"` explicitly. The report is aggregates only; keep both files in scratch, out of the repo.

- [ ] **Step 3: Present the stop-or-go to Jesse**

Summarize, in a message to Jesse: per-mechanism projected savings, the corpus facts (sessions, entries, recorded tokens), and the verdict against the 5 percent threshold from the spec. If the best non-informational projection is under 5 percent, STOP and report per the spec; the spine (slices 2+) waits on his decision. Otherwise state that the go threshold is met and the next plan is slice 2 (gates, ledger, freeze, validate).

- [ ] **Step 4: Commit any gate fixes**

If gate runs surfaced fixes (they belong to this slice's code), commit them:

```bash
git add <fixed paths>
git commit -m "research: gate fixes from slice 1 full-gate run"
```

---

## Self-review (done during planning)

1. **Spec coverage for slice 1:** oracle analyzer (Tasks 2-5), smoke environments and the committed pool contract (Task 6), ATIF metrics extraction (Task 7), offline runner mechanics with infra-fail classification, run caps, live opt-in guard, and ledger rows (Tasks 8-9), offline end-to-end solvability proof (Task 10), Phase 0 stop-or-go on real data (Task 11). Slice 2 items (gates, freeze/validate, criteria file, hash-chained ledger) and slice 3 (the skill) are deliberately out of this plan and get their own plans per the spec's delivery list.
2. **Placeholder scan:** no TBDs; every code step carries compilable code or an exact command with expected output. Two steps contain investigation notes (flag-shape verification in Task 1, adapter construction verification in Task 10); each names the exact search to run and the fallback shape, so they are bounded lookups, not open work.
3. **Type consistency:** `AdjacencyStats`, `ObsResendStats`, `LogVolumeStats`, `CompactionStats`, `MechanismProjection`, `OracleReport`, `OracleOptions`, `environment`, `SessionRun`, `sessionExecutor`, `ExecExecutor`, `RunRow`, `RolloutOptions`, `AtifMetrics` are each defined once and used with matching names across tasks. `measureLogVolume`'s filter seam is called out for cleanup in Task 4's implementation note so the shipped shape is single-sourced.
```
