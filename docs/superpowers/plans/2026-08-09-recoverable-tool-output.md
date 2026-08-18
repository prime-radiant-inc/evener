# Recoverable Tool Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every generically truncated text result recoverable through `read_transcript`, with exact artifact paging and bounded regex search over artifact and retained job output, including running jobs.

**Architecture:** Each root session tree owns a temporary-file artifact store shared by descendants. The tool registry preserves the exact pre-limit model-facing bytes; the common session execution seam stores them and appends an `artifact:` handle. `read_transcript` dispatches session, job, and artifact refs into shared fixed-page and bounded-line-search helpers; job reads use stable point-in-time snapshots and lifetime byte offsets.

**Tech Stack:** Go 1.24, standard-library `crypto/rand`, `encoding/base64`, `encoding/hex`, `io`, `os`, `regexp`, existing `agent/internal/jobstore`, existing tool registry and transcript envelopes.

## Global Constraints

- Read `docs/testing.md` before changing tests; default tests use only synthetic local data and no provider credentials or network.
- Artifact refs are opaque, unguessable capabilities; never accept or expose filesystem paths.
- Artifact files and directories use owner-only permissions and have no size cap.
- Artifact lifetime is one root session tree; descendants share the store, independent roots do not, and root cleanup runs after descendant shutdown.
- Existing session transcript formats, ranges, expansion pages, and continuation behavior remain unchanged.
- Existing no-argument `job:` reads retain their rendered markdown behavior, including delegate `structured_result` rendering.
- Raw pages are fixed at 16 KiB. Search accepts RE2, `context_lines` 0–10, and stops before 100 matches or 64 KiB of serialized match data.
- Search never evaluates a running job's trailing incomplete line; terminal unterminated final lines are complete.
- Keep changes focused; do not add artifact listing, metadata databases, retention schedulers, configurable page sizes, or head/tail range syntax.

---

## File Structure

**Create**

- `agent/internal/artifactstore/store.go` — tree-scoped temp-file store, opaque refs, secure open/close semantics.
- `agent/internal/artifactstore/store_test.go` — exact bytes, permissions, concurrency, expiry, and cleanup.
- `agent/retained_output_read.go` — shared raw-page and bounded line-search envelopes/helpers for `artifact:` and `job:` evidence.
- `agent/retained_output_read_test.go` — page encoding, search context, bounds, continuation, partial-prefix, and trailing-fragment tests.
- `agent/session_tool_artifacts.go` — session capture hook, recovery footer, retention-failure warning, and artifact-store ownership helpers.
- `agent/session_tool_artifacts_test.go` — capture correctness, split `TextResult`, event ref, failure behavior, and tree lifecycle.

**Modify**

- `agent/internal/tool/registry.go` — add `RecoverableOutput` and `Truncated`; make truncation markers availability-neutral.
- `agent/internal/tool/registry_test.go` — pin every truncation strategy and split `TextResult` semantics.
- `agent/events/payloads.go` and `agent/events/payloads_test.go` — add optional `output_ref` to `TOOL_CALL_END`.
- `agent/session.go`, `agent/session_config.go`, `agent/session_init.go`, `agent/session_lifecycle.go` — own or inherit one artifact store and close only at root-tree shutdown.
- `agent/subagents.go` and `agent/job_delegate.go` — pass the parent store through new and restored descendants.
- `agent/session_tools.go` — invoke the capture hook before output deltas and `TOOL_CALL_END`.
- `agent/session_tool_registry.go` — expose artifact open through `toolDeps` without capturing `*Session`.
- `agent/internal/jobstore/output.go` — add bounded search options for an initial partial fragment and deferred running EOF fragment.
- `agent/internal/jobstore/output_snapshot.go` — add stable lifetime-offset range snapshots for active files.
- `agent/internal/jobstore/output_test.go` and `agent/internal/jobstore/output_snapshot_test.go` — running append/prune, partial-line, and deferred-EOF coverage.
- `agent/job_transcript_read.go` — resolve job metadata and produce stable raw windows/search inputs.
- `agent/session_tools_transcript.go` — validate ref-specific arguments and dispatch page/search reads.
- `agent/internal/tool/definitions.go`, `agent/internal/tool/definitions_test.go`, `agent/read_transcript_description_test.go` — advertise `artifact:`, `output_match`, `context_lines`, and broadened offsets.
- `agent/session_tools_transcript_job_read_test.go` — job page/search API, running status, pruning, and markdown compatibility.
- `agent/session_tools_transcript_artifact_read_test.go` — artifact page/search API and expiry.
- Modify: `agent/session_tools_misc_contract_fuzz_test.go` — ref-kind and argument-combination validation.
- `agent/subagents_test.go`, `agent/job_delegate_test.go`, and `agent/bundled_agent_tool_surface_test.go` — reader availability in explicit and restored agent policies.

---

### Task 1: Secure Tree-Scoped Artifact Store

**Files:**
- Create: `agent/internal/artifactstore/store.go`
- Create: `agent/internal/artifactstore/store_test.go`

**Interfaces:**
- Produces: `artifactstore.New(base string) (*Store, error)`
- Produces: `(*Store).Put(data []byte) (ref string, err error)`
- Produces: `(*Store).Open(ref string) (*os.File, error)`
- Produces: `(*Store).Close() error`
- Produces: sentinel errors `ErrInvalidRef` and `ErrExpired`

- [ ] **Step 1: Write failing construction, exact-byte, and ref-validation tests**

```go
func TestStorePutOpenRoundTrip(t *testing.T) {
    s, err := New(t.TempDir())
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = s.Close() })

    want := []byte("first\x00second\n")
    ref, err := s.Put(want)
    if err != nil { t.Fatal(err) }
    if !strings.HasPrefix(ref, "artifact:") { t.Fatalf("ref=%q", ref) }

    f, err := s.Open(ref)
    if err != nil { t.Fatal(err) }
    got, err := io.ReadAll(f)
    _ = f.Close()
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(got, want) { t.Fatalf("got %q want %q", got, want) }
}

func TestStoreRejectsPathsAndUnknownRefs(t *testing.T) {
    s, _ := New(t.TempDir())
    defer s.Close()
    if _, err := s.Open("artifact:../../etc/passwd"); !errors.Is(err, ErrInvalidRef) {
        t.Fatalf("err=%v", err)
    }
    if _, err := s.Open("artifact:00112233445566778899aabbccddeeff"); !errors.Is(err, ErrExpired) {
        t.Fatalf("err=%v", err)
    }
}
```

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./agent/internal/artifactstore -run 'TestStore' -count=1`

Expected: FAIL because the package and symbols do not exist.

- [ ] **Step 3: Implement the minimal store**

```go
type Store struct {
    mu     sync.RWMutex
    dir    string
    closed bool
    refs   map[string]string
}

func New(base string) (*Store, error) {
    dir, err := os.MkdirTemp(base, "evener-artifacts-*")
    if err != nil { return nil, err }
    if err := os.Chmod(dir, 0o700); err != nil {
        _ = os.RemoveAll(dir)
        return nil, err
    }
    return &Store{dir: dir, refs: make(map[string]string)}, nil
}

func (s *Store) Put(data []byte) (string, error) {
    idBytes := make([]byte, 16)
    if _, err := rand.Read(idBytes); err != nil { return "", err }
    id := hex.EncodeToString(idBytes)
    ref := "artifact:" + id
    path := filepath.Join(s.dir, id)
    if err := os.WriteFile(path, data, 0o600); err != nil { return "", err }
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.closed { _ = os.Remove(path); return "", ErrExpired }
    s.refs[ref] = path
    return ref, nil
}
```

Implement `Open` by exact map lookup, never by joining caller text to a path. Implement idempotent `Close` by marking closed, clearing the map, and `os.RemoveAll(dir)`.

- [ ] **Step 4: Add concurrency, permission, and cleanup tests**

Test 32 concurrent `Put`/`Open` operations under `go test -race`; assert directory mode `0700`, file mode `0600`, `Close` removes the directory, and a previously valid ref returns `ErrExpired` after close.

- [ ] **Step 5: Run focused and race tests**

Run: `go test ./agent/internal/artifactstore -count=1`

Run: `go test -race ./agent/internal/artifactstore -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/internal/artifactstore/store.go agent/internal/artifactstore/store_test.go
git commit -m "feat(agent): add tree-scoped tool artifact store"
```

---

### Task 2: Preserve Recoverable Bytes in the Tool Registry

**Files:**
- Modify: `agent/internal/tool/registry.go:211-270,681-749,733-814`
- Modify: `agent/internal/tool/registry_test.go`

**Interfaces:**
- Produces: `ExecResult.RecoverableOutput string`
- Produces: `ExecResult.Truncated bool`
- Consumes later: Task 4's session capture hook

- [ ] **Step 1: Write failing result-contract tests**

```go
func TestToolRegistry_TruncatedTextResultKeepsModelSource(t *testing.T) {
    reg := NewRegistry()
    large := strings.Repeat("model-facing\n", 3000)
    event := "different event-facing body"
    registerTextResultTool(t, reg, TextResult{Output: large, FullOutput: event},
        schema.ToolOutputLimit{MaxChars: 100, Strategy: schema.TruncHeadTail})

    got := executeTestTool(t, reg)
    if !got.Truncated { t.Fatal("expected Truncated") }
    if got.RecoverableOutput != large { t.Fatal("recoverable bytes changed") }
    if got.FullOutput != event { t.Fatal("event-facing output changed") }
}
```

Add table cases for `TruncHeadTail`, `TruncTail`, `TruncHeadCount`, and line truncation. Add an untruncated split `TextResult` case asserting `Truncated=false`.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./agent/internal/tool -run 'TestToolRegistry_(Truncated|Untruncated)' -count=1`

Expected: FAIL because fields are absent and current markers claim event-stream availability.

- [ ] **Step 3: Implement exact truncation accounting**

```go
type ExecResult struct {
    ToolName         string
    CallID           string
    Output           string
    FullOutput       string
    RecoverableOutput string
    Truncated        bool
    // existing fields unchanged
}

func truncateResult(toolName, callID, full string, isErr bool, lim schema.ToolOutputLimit) ExecResult {
    out := applyToolLimit(full, lim)
    return ExecResult{
        ToolName: toolName, CallID: callID,
        Output: out, FullOutput: full,
        RecoverableOutput: full,
        Truncated: out != full,
        IsError: isErr,
    }
}
```

Keep `RecoverableOutput` unchanged when `dispatchedResult` applies a `TextResult.FullOutput` override. Replace every generic warning with availability-neutral counts such as `Output truncated: N characters removed from the middle.`

- [ ] **Step 4: Run registry tests**

Run: `go test ./agent/internal/tool -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/registry.go agent/internal/tool/registry_test.go
git commit -m "feat(agent): preserve recoverable tool output"
```

---

### Task 3: Own the Store Across a Root Session Tree

**Files:**
- Create: `agent/session_tool_artifacts.go`
- Create: `agent/session_tool_artifacts_test.go`
- Modify: `agent/session.go`
- Modify: `agent/session_config.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session_lifecycle.go:111-260`
- Modify: `agent/subagents.go`
- Modify: `agent/job_delegate.go`

**Interfaces:**
- Consumes: `artifactstore.Store` from Task 1
- Produces: private `artifactStore` interface and `Session.artifactStore`
- Produces: `Session.ownsArtifactStore bool`
- Produces later: Task 4 capture and Task 6 reader access

- [ ] **Step 1: Write failing root/child ownership tests**

```go
func TestSessionArtifactStoreSharedByDescendantsOnly(t *testing.T) {
    rootA := newTestSession(t)
    rootB := newTestSession(t)
    child := newTestSubagentSession(t, rootA)
    if rootA.artifactStore != child.artifactStore { t.Fatal("child did not inherit store") }
    if rootA.artifactStore == rootB.artifactStore { t.Fatal("independent roots shared store") }
    if !rootA.ownsArtifactStore || child.ownsArtifactStore { t.Fatal("wrong ownership") }
}
```

Add a close-order test with a fake store recording `Close`: child close must not close it; root close must close it once after descendants become closed. Add restored-delegate inheritance coverage.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./agent -run 'TestSessionArtifactStore|TestRestoredDelegate.*Artifact' -count=1`

Expected: FAIL because session fields and inheritance do not exist.

- [ ] **Step 3: Add private dependency injection and ownership**

```go
type artifactStore interface {
    Put([]byte) (string, error)
    Open(string) (*os.File, error)
    Close() error
}

type SessionConfig struct {
    // existing exported fields
    artifactStore artifactStore
}
```

In `NewSession`, create `artifactstore.New("")` only when `cfg.artifactStore == nil`; mark that session as owner. Before each child `NewSession` or restore call, set `childCfg.artifactStore = s.artifactStore`. In the root close cascade, close the owned store after descendant shutdown and before the root returns.

- [ ] **Step 4: Run lifecycle tests**

Run: `go test ./agent -run 'TestSessionArtifactStore|TestSession_Close|TestRestoredDelegate' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_tool_artifacts.go agent/session_tool_artifacts_test.go agent/session.go agent/session_config.go agent/session_init.go agent/session_lifecycle.go agent/subagents.go agent/job_delegate.go
git commit -m "feat(agent): scope tool artifacts to session trees"
```

---

### Task 4: Capture Truncated Results and Publish Handles

**Files:**
- Modify: `agent/session_tool_artifacts.go`
- Modify: `agent/session_tool_artifacts_test.go`
- Modify: `agent/session_tools.go:429-505`
- Modify: `agent/events/payloads.go:206-229`
- Modify: `agent/events/payloads_test.go`

**Interfaces:**
- Consumes: `ExecResult.RecoverableOutput`, `ExecResult.Truncated`, and tree store
- Produces: `retainToolArtifact(*tool.ExecResult) string`
- Produces: `events.ToolCallEndData.OutputRef string` with JSON `output_ref,omitempty`

- [ ] **Step 1: Write failing capture and event tests**

```go
func TestRetainToolArtifactUsesRecoverableOutput(t *testing.T) {
    store := newFakeArtifactStore()
    s := &Session{artifactStore: store}
    res := tool.ExecResult{Output: "preview", FullOutput: "event", RecoverableOutput: "model full", Truncated: true}
    ref := s.retainToolArtifact(&res)
    if string(store.puts[0]) != "model full" { t.Fatalf("stored %q", store.puts[0]) }
    if !strings.Contains(res.Output, "Full output: "+ref) { t.Fatalf("output=%q", res.Output) }
}
```

Add retention failure: output contains `retention_failed`, contains no `artifact:`, `event stream`, or availability claim, and preserves `IsError`. Marshal `ToolCallEndData{OutputRef:"artifact:abc"}` and assert the field.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./agent ./agent/events -run 'TestRetainToolArtifact|TestToolCallEnd.*OutputRef' -count=1`

Expected: FAIL.

- [ ] **Step 3: Implement the common capture hook**

```go
func (s *Session) retainToolArtifact(res *tool.ExecResult) string {
    if !res.Truncated { return "" }
    ref, err := s.artifactStore.Put([]byte(res.RecoverableOutput))
    if err != nil {
        res.Output += "\n[retention_failed: full output could not be retained: " + conciseError(err) + "]"
        return ""
    }
    res.Output += "\nFull output: " + ref +
        "\nRead with: read_transcript(transcript_ref=\"" + ref + "\")"
    return ref
}
```

Call it after final tool execution/escalation and before output deltas and `TOOL_CALL_END`. Set `endData.OutputRef = outputRef`; keep `endData.Output/Error = res.FullOutput` unchanged.

- [ ] **Step 4: Run focused and existing session tool tests**

Run: `go test ./agent ./agent/events -run 'TestRetainToolArtifact|TestExecTool|TestToolCallEnd' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_tool_artifacts.go agent/session_tool_artifacts_test.go agent/session_tools.go agent/events/payloads.go agent/events/payloads_test.go
git commit -m "feat(agent): return handles for truncated tool output"
```

---

### Task 5: Add Stable Raw Windows and Bounded Search Primitives

**Files:**
- Create: `agent/retained_output_read.go`
- Create: `agent/retained_output_read_test.go`
- Modify: `agent/internal/jobstore/output.go`
- Modify: `agent/internal/jobstore/output_snapshot.go`
- Modify: `agent/internal/jobstore/output_test.go`
- Modify: `agent/internal/jobstore/output_snapshot_test.go`

**Interfaces:**
- Produces: `readRetainedPage(io.ReaderAt, retainedStart, total, offset int64) (retainedPage, error)`
- Produces: `searchRetainedOutput(searchSource, retainedSearchOptions) (retainedSearchEnvelope, error)`
- Produces: `jobstore.ReadOutputWindowSnapshot(path string, offset int64, maxBytes int) (OutputWindowSnapshot, error)`
- Produces: `jobstore.SearchOptions{StartOffset, MaxMatches, MaxSerializedBytes, ContextLines, SkipPartialPrefix, DeferEOFFragment}`

- [ ] **Step 1: Write failing page tests**

Cover a 40 KiB UTF-8 artifact across three 16 KiB pages, an invalid UTF-8 boundary that returns base64, offset beyond EOF, and a job lifetime offset with `retained_start_bytes > 0`.

```go
func TestReadRetainedPageReconstructsExactBytes(t *testing.T) {
    want := bytes.Repeat([]byte("0123456789abcdef"), 3000)
    // read continuations until nil; decode utf8/base64 and append
    if !bytes.Equal(got, want) { t.Fatal("page gap or overlap") }
}
```

- [ ] **Step 2: Write failing bounded-search tests**

Cover 0/1/10 context lines, 101 matches, a 64 KiB context boundary, continuation at the first unevaluated line, repeated lookahead context without skipped matches, oversized-line reporting, a pruned partial prefix, running deferred EOF, and terminal unterminated EOF.

- [ ] **Step 3: Run tests and verify failure**

Run: `go test ./agent ./agent/internal/jobstore -run 'Test(ReadRetainedPage|SearchRetained|ReadOutputWindowSnapshot)' -count=1`

Expected: FAIL because helpers and options do not exist.

- [ ] **Step 4: Implement stable range snapshots**

Extend the existing before/after metadata verification in `ReadOutputSnapshot` rather than performing an unlocked single read. Return:

```go
type OutputWindowSnapshot struct {
    Content       []byte
    Start         int64
    End           int64
    TotalBytes    int64
    RetainedStart int64
    Truncated     bool
}
```

For live `OutputStore` reads, use its mutex-protected `Window`; for file snapshots, retry once on `ErrOutputChangedDuringRead`. All offsets are lifetime offsets.

- [ ] **Step 5: Implement streaming bounded line search**

Use `bufio.Reader.ReadSlice('\n')`/bounded accumulation; do not build the full result before enforcing limits. Track line-start lifetime offsets. Stop before adding a match that would cross 100 matches or 64 KiB of serialized match/context data. Set continuation to the first line not evaluated as a match. Skip the initial retained fragment through newline when requested. Defer an unterminated EOF fragment when `DeferEOFFragment` is true.

- [ ] **Step 6: Run focused and race tests**

Run: `go test ./agent/internal/jobstore ./agent -run 'Test(ReadRetainedPage|SearchRetained|ReadOutputWindowSnapshot)' -count=1`

Run: `go test -race ./agent/internal/jobstore ./agent -run 'Test.*(Running|Concurrent|Snapshot)' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/retained_output_read.go agent/retained_output_read_test.go agent/internal/jobstore/output.go agent/internal/jobstore/output_snapshot.go agent/internal/jobstore/output_test.go agent/internal/jobstore/output_snapshot_test.go
git commit -m "feat(agent): add bounded retained output readers"
```

---

### Task 6: Expose Artifact and Job Paging/Search Through `read_transcript`

**Files:**
- Modify: `agent/session_tool_registry.go:107-118,166-258`
- Modify: `agent/session_tools_transcript.go:44-94,200-415`
- Modify: `agent/job_transcript_read.go:137-174`
- Modify: `agent/internal/tool/definitions.go:716-739`
- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/read_transcript_description_test.go`
- Create: `agent/session_tools_transcript_artifact_read_test.go`
- Modify: `agent/session_tools_transcript_job_read_test.go`
- Modify/Create: focused transcript contract fuzz test

**Interfaces:**
- Consumes: store `Open`, Task 5 page/search helpers, and job snapshot metadata
- Adds tool args: `output_match string`, `context_lines integer 0..10`
- Broadens `offset_bytes` according to ref kind
- Produces page/search envelopes exactly named by the spec

- [ ] **Step 1: Write failing schema and validation tests**

Assert `DefReadTranscript` advertises the two new arguments and describes all three ref kinds. Table-test session/job/artifact combinations, including `context_lines` without match, invalid RE2, explicit artifact format, session search, job format plus page/search, offsets before retention, beyond EOF, malformed ref, and expired ref.

- [ ] **Step 2: Write failing artifact API tests**

```go
func TestReadTranscriptArtifactPageAndSearch(t *testing.T) {
    deps, ref := artifactTranscriptFixture(t, "zero\nneedle\nend\n")
    page := execRead(t, deps, map[string]any{"transcript_ref": ref})
    requirePage(t, page, 0, "zero\nneedle\nend\n")
    search := execRead(t, deps, map[string]any{
        "transcript_ref": ref, "output_match": "needle", "context_lines": float64(1),
    })
    requireMatch(t, search, 5, []string{"zero"}, "needle", []string{"end"})
}
```

- [ ] **Step 3: Write failing job API tests**

Cover unchanged no-argument markdown, raw page with `job_status`, running append then continuation, pruning outrunning a continuation (`output_unavailable` with first offset), running partial EOF search deferred then evaluated after append, terminal unterminated EOF match, and delegate structured-result compatibility.

- [ ] **Step 4: Run tests and verify failure**

Run: `go test ./agent -run 'Test(ReadTranscript|DefReadTranscript).*?(Artifact|Job|OutputMatch|Context)' -count=1`

Expected: FAIL.

- [ ] **Step 5: Add ref-kind parsing and dispatch**

Replace the static `jobRefRejectedParams` gate with explicit operation parsing:

```go
type retainedReadArgs struct {
    Ref          string
    OffsetSet    bool
    OffsetBytes  int64
    OutputMatch  string
    ContextLines int
}
```

Dispatch order: `artifact:` → artifact page/search; `job:` → existing markdown when no new operation, otherwise job page/search; all other refs → unchanged session path. Add `artifactStore` access to `toolDeps` through a narrow `openArtifact` closure.

- [ ] **Step 6: Render exact page/search envelopes**

Use the existing transcript expansion UTF-8/base64 encoding helper where possible. Artifact envelopes omit `job_status`; job envelopes use the folded record's current status and snapshot `total_bytes`. Ensure the 64 KiB search payload remains below the 600,000-character read-transcript backstop.

- [ ] **Step 7: Run focused tests and fuzz seeds**

Run: `go test ./agent -run 'Test(ReadTranscript|DefReadTranscript)' -count=1`

Run: `go test ./agent -run 'Fuzz.*ReadTranscript' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add agent/session_tool_registry.go agent/session_tools_transcript.go agent/job_transcript_read.go agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/read_transcript_description_test.go agent/session_tools_transcript_artifact_read_test.go agent/session_tools_transcript_job_read_test.go agent/session_tools_misc_contract_fuzz_test.go
git commit -m "feat(agent): page and search retained tool output"
```

---

### Task 7: Guarantee Reader Access and Replay the Original Failure

**Files:**
- Modify: `agent/internal/tool/registry.go`
- Modify: `agent/internal/tool/registry_test.go`
- Modify: `agent/session_init.go:1020-1080,1730-1785`
- Modify: `agent/subagents.go:228-255`
- Modify: `agent/job_delegate.go:1438-1516`
- Modify: `agent/subagents_test.go`
- Modify: `agent/job_delegate_test.go`
- Modify: `agent/bundled_agent_tool_surface_test.go`
- Create: `agent/recoverable_tool_output_e2e_test.go`

**Interfaces:**
- Consumes: `read_transcript` implementation and artifact capture
- Produces: `(*tool.Registry).RequiresOutputRecovery(names []string) bool`
- Produces: `ensureRecoveryReader(toolNames []string, reg *tool.Registry) []string`

- [ ] **Step 1: Write failing policy tests**

For explicit explorer/reviewer/worker allowlists that include `grep`, `read_file`, or another generically limited text tool, assert `read_transcript` is advertised. Assert a list containing only nontruncating control tools is unchanged. Assert frozen/restored delegate tool names preserve the injected reader.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./agent -run 'Test.*(AllowedTools|ToolSurface|RecoveryReader|RestoredDelegate)' -count=1`

Expected: FAIL because current explicit policies remove `read_transcript`.

- [ ] **Step 3: Implement one centralized policy rule**

```go
func (r *Registry) RequiresOutputRecovery(names []string) bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    for _, name := range names {
        registered, ok := r.tools[name]
        if !ok || name == "read_transcript" { continue }
        lim := registered.Limit
        if lim.MaxChars > 0 || lim.MaxLines > 0 { return true }
    }
    return false
}

func ensureRecoveryReader(names []string, reg *tool.Registry) []string {
    if reg.RequiresOutputRecovery(names) {
        return appendUniqueStrings(names, "read_transcript")
    }
    return names
}
```

Add registry tests for limited, unlimited, unknown, and already-present names. Apply the helper before explicit-tool filtering and before freezing delegate tool names, so restore validates the same effective set. Do not edit bundled agent markdown files one by one.

- [ ] **Step 4: Add the receipt replay test**

Build a deterministic temp workspace with enough matching lines and context to exceed grep's inline cap. Execute real `grep` through the registry/session seam, extract the returned `artifact:` ref, then call `read_transcript` once with the original broad regex and `context_lines=3`. Assert all expected matches, including preview-omitted matches, are returned without a second grep.

- [ ] **Step 5: Add running-job end-to-end coverage**

Start a scripted local job, page while running, append, continue without gaps, search while the final line is partial, complete it, and assert one later search evaluates it exactly once with `job_status` transitions.

- [ ] **Step 6: Run feature gates**

Run: `go test ./agent/internal/artifactstore ./agent/internal/tool ./agent/internal/jobstore ./agent/events ./agent -count=1`

Run: `go test -race ./agent/internal/artifactstore ./agent/internal/jobstore ./agent -run 'Test.*(Artifact|Retained|RunningJob|Recoverable)' -count=1`

Run: `go vet ./agent/...`

Expected: all commands exit 0 with no warnings.

- [ ] **Step 7: Commit**

```bash
git add agent/internal/tool/registry.go agent/internal/tool/registry_test.go agent/session_init.go agent/subagents.go agent/job_delegate.go agent/subagents_test.go agent/job_delegate_test.go agent/bundled_agent_tool_surface_test.go agent/recoverable_tool_output_e2e_test.go
git commit -m "test(agent): guarantee recoverable tool output"
```

---

### Task 8: Final Verification and Documentation Consistency

**Files:**
- Verify: all files named above

**Interfaces:**
- Consumes: completed feature
- Produces: verified repository state and final evidence

- [ ] **Step 1: Run formatting checks**

Run: `git diff --check`

Run: `make lint-gofmt`

Expected: exit 0.

- [ ] **Step 2: Run the canonical short gate**

Run: `make test-short`

Expected: exit 0.

- [ ] **Step 3: Run race coverage for changed concurrency boundaries**

Run: `go test -race ./agent/internal/artifactstore ./agent/internal/jobstore ./agent -run 'Test.*(Artifact|Retained|RunningJob|Snapshot|Close)' -count=1`

Expected: exit 0.

- [ ] **Step 4: Audit acceptance criteria against test names**

Map each numbered acceptance criterion in the spec to at least one passing test. Confirm the receipt replay uses the original broad pattern, running-job envelopes report status, partial lines defer correctly, and every truncatable explicit-agent policy contains `read_transcript`.

- [ ] **Step 5: Confirm no uncommitted implementation changes remain**

Run: `git status --short`

Expected: no modified or untracked implementation files. The pre-existing untracked `read-transcript-feedback-2026-08-08.md` may remain; do not add or remove it.
