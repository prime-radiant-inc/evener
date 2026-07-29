# Bounded API-Log Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make canonical API-log open-time recovery independent of historical
file size so a large forensic log cannot prevent a resumed daemon from
rendezvousing with Hub.

**Architecture:** Keep the existing file, JSONL format, exclusive target lock,
and synchronous append path unchanged. Replace the offset-zero recovery scan
with a backward bounded scan that locates the final append boundary using the
canonical record-size limit, validates the complete record immediately before
that boundary, and reports a bounded partial tail for the existing owner to
truncate and sync.

**Tech Stack:** Go, `io.ReadSeeker`, strict `llm/apilog` record decoding,
deterministic sparse test readers, existing Unix target-file locking.

## Global Constraints

- The API log is forensic evidence, not session replay state.
- Open-time recovery validates the append boundary and possible torn tail, not
  every historical record; explicit readers validate every record they consume.
- Recovery work is bounded by the 128 MiB canonical record limit and is
  independent of total historical file size.
- A corrupt, unsupported, oversized, or missing boundary fails closed without
  modifying the file.
- Only a bounded incomplete final fragment may be truncated, and that
  truncation is synced before append admission.
- Existing exclusive file ownership and one-write-plus-sync append durability
  must not change.
- Tests use a sparse controlled `io.ReadSeeker`; no wall-clock assertion, giant
  fixture, provider credential, or network access is allowed.

---

### Task 1: Bound Recovery to the Append Boundary

**Files:**

- Modify: `llm/apilog/codec.go`
- Modify: `llm/apilog/codec_test.go`
- Verify unchanged behavior: `llm/apilog.go`
- Verify unchanged behavior: `llm/apilog_append_test.go`

**Interfaces:**

- Preserve:
  `func ScanRecovery(r io.ReadSeeker, maxLineBytes int) (lastCompleteOffset int64, partialTail bool, err error)`
- Add only private codec helpers for bounded backward newline search and exact
  boundary-record reads.
- Continue returning the complete offset consumed by
  `recoverCanonicalAPILogTail`; that owner remains responsible for
  `Truncate`, `Sync`, and error wrapping.

- [ ] **Step 1: Add a deterministic sparse-file regression.**

  Add a test-only `io.ReadSeeker` whose logical prefix is much larger than the
  in-memory tail. Put a newline, one valid canonical record, a newline, and a
  partial record in the materialized suffix. Record total bytes read and the
  lowest read offset.

  Call `ScanRecovery` with a small bound large enough for the valid record and
  assert:

  - it returns the newline after that valid record;
  - it reports `partialTail=true`;
  - it never reads the ancient logical prefix;
  - total bytes read are a small multiple of `maxLineBytes`, not logical size.

  This catches a production mutation back to `Seek(0, io.SeekStart)` or any
  other whole-history recovery scan.

- [ ] **Step 2: Run the regression and confirm a behavioral RED.**

  Run:

  ```bash
  go test ./llm/apilog -run TestScanRecoveryBoundsWorkToCanonicalTail -count=1
  ```

  Expected: FAIL because current `ScanRecovery` seeks to offset zero and tries
  to decode the sparse historical prefix instead of recovering the valid tail.
  A compile failure does not count as RED.

- [ ] **Step 3: Implement bounded backward recovery.**

  In `ScanRecovery`:

  1. reject non-positive `maxLineBytes`;
  2. seek to EOF and return cleanly for an empty file;
  3. inspect the final byte to distinguish clean newline EOF from a possible
     partial fragment;
  4. locate line boundaries backward in fixed-size chunks, refusing to scan
     farther than `maxLineBytes+1` for either a fragment or a complete record;
  5. for clean EOF, strictly decode the final complete record and return EOF;
  6. for partial EOF, locate and strictly decode the immediately preceding
     complete record, then return its newline offset with `partialTail=true`;
  7. for a file containing only one bounded partial fragment, return offset zero
     with `partialTail=true`;
  8. return an error without a recoverable offset when either boundary is
     missing within the bound or the complete boundary record fails strict
     decoding.

  Keep `recoverCanonicalAPILogTail` unchanged so truncation and sync remain in
  the flock-owning storage layer.

- [ ] **Step 4: Prove boundary failures remain fail closed.**

  Extend focused codec tests as needed to cover:

  - corrupt final complete record;
  - corrupt complete record immediately before a partial tail;
  - oversized clean boundary record;
  - oversized partial tail with no reachable newline.

  Use small byte fixtures and small limits. Assert structured offsets and file
  mutation through the existing logger tests rather than matching rendered
  error text.

- [ ] **Step 5: Run focused GREEN and mutation proof.**

  Run:

  ```bash
  go test ./llm/apilog -run 'TestScanRecovery' -count=100
  go test -race ./llm/apilog ./llm -run 'Test(ScanRecovery|RecoverCanonicalAPILogTail|APILoggerReopen)' -count=20
  ```

  Temporarily restore offset-zero scanning and confirm
  `TestScanRecoveryBoundsWorkToCanonicalTail` fails for the intended reason,
  then restore the bounded implementation and rerun GREEN.

- [ ] **Step 6: Run related verification.**

  Run:

  ```bash
  go test ./llm/apilog ./llm ./cmdutil ./cmd/serf -count=1
  go test -race ./llm/apilog ./llm -count=1
  go vet ./llm/apilog ./llm ./cmdutil ./cmd/serf
  golangci-lint run ./llm/apilog ./llm ./cmdutil ./cmd/serf
  make build
  ```

  Confirm the worktree is clean except for the intended committed files.

- [ ] **Step 7: Commit the implementation.**

  Stage only:

  ```bash
  git add llm/apilog/codec.go llm/apilog/codec_test.go
  git commit
  ```

  The detailed commit message must record the live failure chain, the
  append-boundary contract, the deterministic RED/GREEN evidence, and that
  flock, truncate-plus-sync, and append durability remain unchanged.
