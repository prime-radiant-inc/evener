# Activity View Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the card-based master-detail Activity view with a dense hierarchical tree (one line per job/agent, tokens ↑↓ + quiet time, terminal items folded, click opens transcript tab, chevron opens inline detail).

**Architecture:** Small additive Go wire change (`lastOutputAt` on activity jobs, `usage` on activity delegates) feeding a rewritten React tree: a pure `buildActivityRows` row-model builder, format helpers, dense rows with fold rows, and an inline detail strip. The right-hand inspector pane is deleted.

**Tech Stack:** Go (agent, appwire, internal/apptranscript), React + TypeScript + CSS modules (cmd/serf-hub/frontend), Vitest, Biome.

Spec: `docs/superpowers/specs/2026-08-05-activity-view-redesign-design.md`

## Global Constraints

- Default tests deterministic: no provider credentials, network, or live model behavior (`docs/testing.md`).
- Frontend gates: `npx biome check --write` on touched frontend files, then `make test-web`; on this Chrome-capable host also `make test-web-browser`. Run from repo root unless noted; frontend commands run in `cmd/serf-hub/frontend`.
- Every color is a token from `cmd/serf-hub/frontend/src/styles/tokens.css` (or a `color-mix()` of one). No hex/rgb/hsl literals elsewhere — `token-contract.test.ts` fails CI. Hue semantics: `--alive` = working, `--danger` = failure, `--attention` = needs-human only, `--accent` = selection/focus.
- IBM Plex Mono (`var(--font-mono)`) for numbers/timings/paths; IBM Plex Sans (`var(--font-sans)`) for labels. 4px space grid (`var(--space-N)`).
- Avoid `noNonNullAssertion` and array-index-key violations (Biome).
- After editing `appwire/types.go`, regenerate with `make generate`; `make lint-generated` must pass.
- Go tests: `go test ./agent/ ./appwire/` from repo root.

## File Structure

**Go (backend):**
- `appwire/types.go` — add `LastOutputAt` to `JobActivityJob` (line ~1226), add `Usage` to `JobActivityDelegate` (line ~1236).
- `agent/jobs_activity.go` — `activitySessionSnapshot` gains `Usage`; `projectActivityJob` stamps `LastOutputAt` from `JobRecord.LastActivity`; `projectActivityDelegate` copies child usage; `loadLiveActivityBase` fills snapshot usage from `CumulativeUsageSnapshot`.
- `agent/jobs_activity_past.go` — `loadHistoricalActivityBase` fills snapshot usage from the retained transcript via a memoizing `apptranscript.TurnCache`.
- `agent/jobs_activity_test.go`, `agent/jobs_activity_usage_test.go` (new) — tests.

**Frontend (`cmd/serf-hub/frontend/src`):**
- `protocol/types.gen.ts` — regenerated (Task 2), never hand-edited.
- `panes/session/chrome/activityData.ts` — parse `lastOutputAt` + `usage`; export `ActivityUsage`.
- `panes/session/chrome/activityFormat.ts` (new) — pure meta/quiet/tokens formatters.
- `panes/session/chrome/activityRows.ts` (new) — pure `buildActivityRows` + fold synthesis.
- `stores/activityPanel.ts` — `collapsedFoldIDs` + `toggleFold`.
- `panes/session/chrome/ActivityTree.tsx` — rewritten dense tree.
- `panes/session/chrome/ActivityRowDetail.tsx` (new) — inline detail strip.
- `panes/session/chrome/ActivityPanel.tsx` — single-column tree; inspector removed.
- `panes/session/chrome/activitypanel.module.css` — new row/fold/detail classes; inspector classes removed.
- Deleted: `panes/session/chrome/ActivityInspector.tsx`.
- Tests: `activityData.test.ts`, `activityFormat.test.ts` (new), `activityRows.test.ts` (new), `activityPanel.test.ts`, `ActivityTree.test.tsx`, `ActivityPanel.test.tsx`.

---

### Task 1: Go — wire fields and projection

**Files:**
- Modify: `appwire/types.go:1208-1237`
- Modify: `agent/jobs_activity.go` (struct at :35-46, `loadLiveActivityBase` at :301-339, `projectActivityDelegate` at :727-777, `projectActivityJob` at :975-1013)
- Test: `agent/jobs_activity_test.go`

**Interfaces:**
- Produces: `appwire.JobActivityJob.LastOutputAt string` (json `lastOutputAt,omitempty`, RFC3339, live-running jobs only); `appwire.JobActivityDelegate.Usage *appwire.SerfUsage` (json `usage,omitempty`); `activitySessionSnapshot.Usage *appwire.SerfUsage`.

- [ ] **Step 1: Write the failing tests**

Append to `agent/jobs_activity_test.go` (reuse its existing helpers/fixtures for building `jobstore.JobRecord` values):

```go
func TestProjectActivityJobStampsLastOutputAt(t *testing.T) {
	last := time.Date(2026, 8, 5, 15, 2, 11, 0, time.UTC)
	rec := &jobstore.JobRecord{
		JobID:        "job_live",
		Type:         jobstore.JobShell,
		Status:       jobstore.StatusRunning,
		Description:  "make test-web",
		StartedAt:    last.Add(-4 * time.Minute),
		LastActivity: &last,
	}
	job := projectActivityJob(rec, "ref_root")
	if job.LastOutputAt != "2026-08-05T15:02:11Z" {
		t.Errorf("LastOutputAt = %q, want %q", job.LastOutputAt, "2026-08-05T15:02:11Z")
	}
}

func TestProjectActivityJobOmitsLastOutputAtWithoutActivity(t *testing.T) {
	rec := &jobstore.JobRecord{
		JobID:       "job_done",
		Type:        jobstore.JobShell,
		Status:      jobstore.StatusCompleted,
		StartedAt:   time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC),
		Description: "done work",
	}
	job := projectActivityJob(rec, "ref_root")
	if job.LastOutputAt != "" {
		t.Errorf("LastOutputAt = %q, want empty", job.LastOutputAt)
	}
}
```

(Constant names match the existing fixtures in `agent/jobs_activity_test.go:22-24`: `jobstore.JobShell`, `jobstore.StatusRunning`, `jobstore.StatusCompleted`.)

For the delegate usage copy, find the existing test that drives `projectActivityDelegate` with a fake `activitySessionSnapshot` (search the test file for `projectActivityDelegate` or `Children:`) and add alongside it:

```go
func TestProjectActivityDelegateCopiesChildUsage(t *testing.T) {
	// Build the same minimal snapshot shape the neighboring delegate tests use,
	// then set the child snapshot's Usage:
	want := &appwire.SerfUsage{InputTokens: 41200, OutputTokens: 6100}
	// snapshot.Children[childID].Usage = want
	// delegate := projectActivityDelegate(snapshot, group, nil, 0, nil)
	// if delegate.Usage == nil || *delegate.Usage != *want { t.Fatalf(...) }
	// Also assert delegate.Usage != want (value copy, not shared pointer).
	_ = want
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run 'TestProjectActivityJobStampsLastOutputAt|TestProjectActivityJobOmitsLastOutputAtWithoutActivity|TestProjectActivityDelegateCopiesChildUsage' -count=1`
Expected: FAIL — `job.LastOutputAt` / `delegate.Usage` unknown fields (compile error) or empty values.

- [ ] **Step 3: Implement**

`appwire/types.go` — in `JobActivityJob` after `OutputBytes` (line 1226):

```go
	OutputBytes    int64  `json:"outputBytes"`
	// LastOutputAt is the RFC3339 timestamp of the job's most recent
	// parent-observable output/activity. Live-only: retained jobs omit it, and
	// clients fall back to startedAt (or hide quiet time for terminal rows).
	LastOutputAt string `json:"lastOutputAt,omitempty"`
```

In `JobActivityDelegate` after `Branch` (line 1236):

```go
	Branch         JobActivityBranchState `json:"branch"`
	// Usage is the child session's cumulative self-only token totals. Nil when
	// the child has no token data (fresh session, old daemon, shell-only work).
	Usage *SerfUsage `json:"usage,omitempty"`
```

`agent/jobs_activity.go`:

1. `activitySessionSnapshot` (line 35-46) — add field:

```go
	Delegates map[string]*jobstore.DelegateRecord
	Usage     *appwire.SerfUsage // cumulative self-only tokens; nil = unknown
	Children  map[string]*activitySessionSnapshot // child session ID
```

2. `loadLiveActivityBase` — after the snapshot literal (after line 314), add:

```go
	snapshot.Usage = appwire.SerfUsageFromLLM(s.CumulativeUsageSnapshot())
```

3. `projectActivityJob` — after the `EndedAt` block (line 1005-1007), add:

```go
	if rec.LastActivity != nil {
		job.LastOutputAt = rec.LastActivity.UTC().Format(time.RFC3339)
	}
```

4. `projectActivityDelegate` — after the `child.SessionID != record.ChildSessionID` guard (line 765-768), add:

```go
	if child.Usage != nil {
		usage := *child.Usage
		delegate.Usage = &usage
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run 'TestProjectActivity' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go agent/jobs_activity.go agent/jobs_activity_test.go
git commit -m "feat(agent): stamp lastOutputAt and delegate usage on activity wire"
```

---

### Task 2: Go — historical usage + codegen

**Files:**
- Modify: `agent/jobs_activity_past.go` (imports :3-12, `loadHistoricalActivityBase` at :39-85)
- Create: `agent/jobs_activity_usage_test.go`
- Modify (generated): `cmd/serf-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**
- Consumes: `apptranscript.NewTurnCache()`, `(*TurnCache).UsageTotalFromFile(path string, maxLineBytes int, fromEntryOrdinal int) (*appwire.SerfUsage, error)`; `sessionsSubdir = "sessions"` (`agent/session.go:1298`); `transcriptJSONLMaxLineBytes` (`agent/transcript_read.go:15`); `schema.SessionMeta.DivergenceTurn`.
- Produces: historical `activitySessionSnapshot.Usage`; regenerated TS types with `lastOutputAt?: string` and `usage?: SerfUsage`.

- [ ] **Step 1: Write the failing test**

Create `agent/jobs_activity_usage_test.go`, modeled on `agent/aside_test.go`'s transcript-writing pattern (`transcript.NewWriter(filepath.Join(stateDir, sessionsSubdir, id+".transcript.jsonl"), transcript.Header{...})`):

```go
package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestLoadHistoricalActivityBaseReadsUsage(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "histusagechild"
	meta := schema.SessionMeta{ID: sessionID}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	// Write a jobs.jsonl so the loader takes the main path: reuse the helper
	// the neighboring historical tests use (rg 'historicalJobsOpen|jobs.jsonl'
	// in agent tests) — one completed shell job record is enough.
	writeOneHistoricalJob(t, stateDir, sessionID) // local helper, see below

	tw, err := transcript.NewWriter(
		filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl"),
		transcript.Header{SessionID: sessionID},
	)
	if err != nil {
		t.Fatalf("transcript writer: %v", err)
	}
	// Append one turn entry carrying usage; copy the exact entry-append call
	// from aside_test.go (it appends schema/turn entries with Usage set).
	appendTurnWithUsage(t, tw, llm.Usage{InputTokens: 1200, OutputTokens: 300})
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	loaded, err := loadHistoricalActivityBase(stateDir, sessionID, true)
	if err != nil {
		t.Fatalf("loadHistoricalActivityBase: %v", err)
	}
	if loaded.snapshot.Usage == nil {
		t.Fatal("snapshot.Usage is nil, want totals from the transcript")
	}
	if loaded.snapshot.Usage.InputTokens != 1200 || loaded.snapshot.Usage.OutputTokens != 300 {
		t.Errorf("Usage = %+v, want 1200/300", loaded.snapshot.Usage)
	}
}
```

The two helpers must be written by copying real neighboring idioms: `writeOneHistoricalJob` mirrors however `jobs_activity_test.go` / `jobs_activity_past`-adjacent tests persist a jobs.jsonl record (find with `rg -l 'jobs.jsonl' agent/*_test.go`); `appendTurnWithUsage` mirrors `aside_test.go`'s writer usage. If `schema.SaveSessionMeta`'s signature differs, match the call site in `loadHistoricalActivityBase` (`schema.LoadSessionMeta(stateDir, sessionID)` at jobs_activity_past.go:40).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestLoadHistoricalActivityBaseReadsUsage -count=1`
Expected: FAIL — `snapshot.Usage is nil`.

- [ ] **Step 3: Implement**

`agent/jobs_activity_past.go` — add import `"primeradiant.com/serf/internal/apptranscript"`, then:

```go
// activityUsageCache memoizes per-transcript cumulative usage totals (keyed by
// file identity) so repeat activity-tree fetches don't rescan every retained
// child transcript.
var activityUsageCache = apptranscript.NewTurnCache()

// historicalActivityUsage sums a retained session's own token usage from its
// transcript. nil (not zero) when the transcript carries no usage, so the wire
// omits the field and the UI hides the token cluster rather than rendering
// ↑0 ↓0.
func historicalActivityUsage(stateDir, sessionID string, meta schema.SessionMeta) *appwire.SerfUsage {
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	total, err := activityUsageCache.UsageTotalFromFile(path, transcriptJSONLMaxLineBytes, meta.DivergenceTurn)
	if err != nil {
		return nil
	}
	return total
}
```

In `loadHistoricalActivityBase`, set `Usage: historicalActivityUsage(stateDir, sessionID, meta)` in BOTH `activitySessionSnapshot` literals (the early no-jobs-file return and the main return). Guard: when `metaErr != nil`, `meta` may be zero-value — `historicalActivityUsage` still works (DivergenceTurn 0); leave as-is.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run 'TestLoadHistoricalActivityBase|TestProjectActivity' -count=1`
Expected: PASS

- [ ] **Step 5: Regenerate the TS protocol types**

Run: `make generate`
Then: `make lint-generated`
Expected: both exit 0; `git status` shows `cmd/serf-hub/frontend/src/protocol/types.gen.ts` modified, containing `lastOutputAt?: string` on `JobActivityJob` and `usage?: SerfUsage` on `JobActivityDelegate`. Verify with:

Run: `rg -n "lastOutputAt|usage" cmd/serf-hub/frontend/src/protocol/types.gen.ts | head`
Expected: hits on both fields.

- [ ] **Step 6: Commit**

```bash
git add agent/jobs_activity_past.go agent/jobs_activity_usage_test.go cmd/serf-hub/frontend/src/protocol/types.gen.ts
git commit -m "feat(agent): historical delegate usage; regen appwire ts types"
```

---

### Task 3: TS — parse `lastOutputAt` and `usage`

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts` (ActivityJob :18-37, parseJob :161-224, ActivityDelegate :55-63, parseDelegate :255-304)
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts`

**Interfaces:**
- Produces: `ActivityUsage { inputTokens: number; outputTokens: number; cacheReadTokens?: number; totalTokens?: number }`; `ActivityJob.lastOutputAt?: string`; `ActivityDelegate.usage?: ActivityUsage`. Parse failures on malformed-but-present fields make the containing node parse as incomplete (same contract as existing optional fields).

- [ ] **Step 1: Write the failing tests**

Append to `activityData.test.ts` (follow its existing fixture style):

```ts
import { parseActivityTree } from "./activityData";

function jobFixture(overrides: Record<string, unknown> = {}) {
  return {
    jobId: "job_1",
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "shell",
    status: "running",
    terminal: false,
    background: true,
    hasOutput: true,
    description: "make test-web",
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 18432,
    ...overrides,
  };
}

function treeFixture(entries: unknown[]) {
  return {
    revision: 1,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root",
      aggregate: "working",
      counts: { active: 1, failed: 0, completed: 0, complete: true },
      entries,
      branch: {},
    },
  };
}

test("parses lastOutputAt on jobs", () => {
  const tree = parseActivityTree(
    treeFixture([{ kind: "shell", job: jobFixture({ lastOutputAt: "2026-08-05T15:02:11Z" }) }]),
  );
  expect(tree?.root.entries[0]).toMatchObject({
    kind: "shell",
    job: { lastOutputAt: "2026-08-05T15:02:11Z" },
  });
});

test("parses usage on delegates", () => {
  const tree = parseActivityTree(
    treeFixture([
      {
        kind: "delegate",
        delegate: {
          delegateId: "dlg_1",
          childSessionId: "sess_child",
          childRef: "ref_child",
          turns: [],
          branch: {},
          usage: { inputTokens: 41200, outputTokens: 6100 },
        },
      },
    ]),
  );
  expect(tree?.root.entries[0]).toMatchObject({
    kind: "delegate",
    delegate: { usage: { inputTokens: 41200, outputTokens: 6100 } },
  });
});

test("omits both fields when absent (old daemon)", () => {
  const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture() }]));
  const entry = tree?.root.entries[0];
  expect(entry?.kind === "shell" && entry.job.lastOutputAt).toBeUndefined();
});

test("rejects malformed usage (non-numeric inputTokens) as incomplete", () => {
  const tree = parseActivityTree(
    treeFixture([
      {
        kind: "delegate",
        delegate: {
          delegateId: "dlg_1",
          childSessionId: "sess_child",
          childRef: "ref_child",
          turns: [],
          branch: {},
          usage: { inputTokens: "many", outputTokens: 1 },
        },
      },
    ]),
  );
  // Malformed delegate drops from entries and flags the branch incomplete,
  // matching how other malformed fields behave.
  expect(tree?.root.entries).toHaveLength(0);
  expect(tree?.root.branch.error).toBeDefined();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityData.test.ts`
Expected: FAIL — `lastOutputAt`/`usage` undefined in parse output.

- [ ] **Step 3: Implement**

In `activityData.ts`:

```ts
export interface ActivityUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  totalTokens?: number;
}
```

Add `lastOutputAt?: string;` to `ActivityJob` (after `endedAt?: string;`) and `usage?: ActivityUsage;` to `ActivityDelegate` (after `mandate?: string;`).

Add a parser next to `parseCounts`:

```ts
function parseUsage(raw: unknown): ActivityUsage | null | undefined {
  if (typeof raw === "undefined") return undefined;
  if (!isPlainObject(raw)) return null;
  const inputTokens = readNonNegativeInteger(raw, "inputTokens");
  const outputTokens = readNonNegativeInteger(raw, "outputTokens");
  if (inputTokens === null || outputTokens === null) return null;
  const usage: ActivityUsage = { inputTokens, outputTokens };
  const cacheReadTokens = readNonNegativeInteger(raw, "cacheReadTokens");
  const totalTokens = readNonNegativeInteger(raw, "totalTokens");
  if (typeof raw.cacheReadTokens !== "undefined" && cacheReadTokens === null) return null;
  if (typeof raw.totalTokens !== "undefined" && totalTokens === null) return null;
  if (cacheReadTokens !== null) usage.cacheReadTokens = cacheReadTokens;
  if (totalTokens !== null) usage.totalTokens = totalTokens;
  return usage;
}
```

In `parseJob`, with the other optional fields:

```ts
  const lastOutputAt = readOptionalString(raw, "lastOutputAt");
  if (typeof raw.lastOutputAt !== "undefined" && typeof raw.lastOutputAt !== "string") return null;
  if (lastOutputAt) job.lastOutputAt = lastOutputAt;
```

In `parseDelegate`, after the mandate block:

```ts
  const usage = parseUsage(raw.usage);
  if (usage === null) return { value: null, incomplete: true };
  if (usage) delegate.usage = usage;
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityData.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/activityData.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityData.test.ts
git commit -m "feat(web): parse lastOutputAt and delegate usage in activityData"
```

---

### Task 4: TS — format helpers (`activityFormat.ts`)

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/activityFormat.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/activityFormat.test.ts`

**Interfaces:**
- Consumes: `ActivityJob`, `ActivityUsage` from `activityData.ts`; `formatTokenCount` from `../transcript/messages/format` (whole-k rounding: 41200 → "41k").
- Produces (exact signatures):

```ts
export function formatUsagePair(usage: ActivityUsage | undefined): string | null;
export function formatQuietAge(ms: number): string;
export function quietAnchorMillis(job: Pick<ActivityJob, "lastOutputAt" | "startedAt">): number;
export function jobStatusDotState(status: string, terminal?: boolean): "idle" | "working" | "needs-you" | "failed" | "ended";
export function isFailedStatus(status: string): boolean;
```

- [ ] **Step 1: Write the failing tests**

Create `activityFormat.test.ts`:

```ts
import { expect, test } from "vitest";
import { formatQuietAge, formatUsagePair, isFailedStatus, jobStatusDotState, quietAnchorMillis } from "./activityFormat";

test("formatUsagePair renders arrows with compact counts", () => {
  expect(formatUsagePair({ inputTokens: 41200, outputTokens: 6100 })).toBe("↑41k ↓6k");
  expect(formatUsagePair({ inputTokens: 900, outputTokens: 12 })).toBe("↑900 ↓12");
  expect(formatUsagePair(undefined)).toBeNull();
});

test("formatQuietAge buckets seconds, minutes, hours, days", () => {
  expect(formatQuietAge(0)).toBe("0s");
  expect(formatQuietAge(3_000)).toBe("3s");
  expect(formatQuietAge(59_999)).toBe("59s");
  expect(formatQuietAge(60_000)).toBe("1m");
  expect(formatQuietAge(13 * 3_600_000)).toBe("13h");
  expect(formatQuietAge(26 * 3_600_000)).toBe("1d");
  expect(formatQuietAge(-5)).toBe("0s"); // clock skew clamps, never negative
});

test("quietAnchorMillis prefers lastOutputAt, falls back to startedAt", () => {
  expect(quietAnchorMillis({ lastOutputAt: "2026-08-05T15:02:11Z", startedAt: "2026-08-05T15:00:00Z" })).toBe(
    Date.parse("2026-08-05T15:02:11Z"),
  );
  expect(quietAnchorMillis({ startedAt: "2026-08-05T15:00:00Z" })).toBe(Date.parse("2026-08-05T15:00:00Z"));
  expect(quietAnchorMillis({ startedAt: "not a date" })).toBe(0);
});

test("jobStatusDotState maps statuses onto StatusDot states", () => {
  expect(jobStatusDotState("running")).toBe("working");
  expect(jobStatusDotState("queued")).toBe("working");
  expect(jobStatusDotState("failed")).toBe("failed");
  expect(jobStatusDotState("exhausted")).toBe("failed");
  expect(jobStatusDotState("blocked")).toBe("needs-you");
  expect(jobStatusDotState("completed", true)).toBe("ended");
  expect(jobStatusDotState("stopped")).toBe("ended");
  expect(jobStatusDotState("whatever")).toBe("idle");
});

test("isFailedStatus matches the danger set", () => {
  expect(isFailedStatus("failed")).toBe(true);
  expect(isFailedStatus("exhausted")).toBe(true);
  expect(isFailedStatus("completed")).toBe(false);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityFormat.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement**

Create `activityFormat.ts`:

```ts
// Pure formatting helpers for the dense activity tree rows. Kept React-free so
// each is trivially unit-testable (same contract as transcript/messages/format).
import { formatTokenCount } from "../transcript/messages/format";
import type { ActivityUsage } from "./activityData";

// formatUsagePair renders a delegate row's token cluster ("↑41k ↓6k"), or null
// when the daemon sent no usage (old daemon, shell-only work) so the row hides
// the cluster instead of rendering ↑0 ↓0.
export function formatUsagePair(usage: ActivityUsage | undefined): string | null {
  if (!usage) return null;
  return `↑${formatTokenCount(usage.inputTokens)} ↓${formatTokenCount(usage.outputTokens)}`;
}

// formatQuietAge buckets a millisecond age into the rail's compact stamps
// ("12s", "1m", "13h", "2d"). Negative input (clock skew) clamps to 0s.
export function formatQuietAge(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

// quietAnchorMillis is the instant a live row's quiet clock measures from:
// the last observed output when known, else the job's start. 0 when neither
// parses, which renders the dishonest-huge-age case as a visible bug rather
// than a hidden one.
export function quietAnchorMillis(job: { lastOutputAt?: string; startedAt: string }): number {
  const anchor = job.lastOutputAt ?? job.startedAt;
  const parsed = Date.parse(anchor);
  return Number.isNaN(parsed) ? 0 : parsed;
}

// isFailedStatus is the single source for the danger set, shared by row dots,
// fold-row failure counts, and terminal meta text.
export function isFailedStatus(status: string): boolean {
  const normalized = status.trim().toLowerCase();
  return normalized === "failed" || normalized === "exhausted" || normalized === "error";
}

export function jobStatusDotState(status: string, terminal?: boolean): "idle" | "working" | "needs-you" | "failed" | "ended" {
  const normalized = status.trim().toLowerCase();
  if (
    normalized === "running" ||
    normalized === "working" ||
    normalized === "queued" ||
    normalized === "starting" ||
    normalized === "resuming"
  ) {
    return "working";
  }
  if (isFailedStatus(normalized)) return "failed";
  if (normalized === "needs-you" || normalized === "blocked") return "needs-you";
  if (terminal === true || normalized === "completed" || normalized === "cancelled" || normalized === "stopped")
    return "ended";
  return "idle";
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityFormat.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/activityFormat.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityFormat.test.ts
git commit -m "feat(web): activity row format helpers"
```

---

### Task 5: TS — row model builder (`activityRows.ts`)

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/activityRows.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/activityRows.test.ts`

**Interfaces:**
- Consumes: `ActivityTree`, `ActivitySessionNode`, `ActivityEntry`, `ActivityJob`, `ActivityDelegate`, `activityNodeID` from `activityData.ts`; `isFailedStatus` from `activityFormat.ts`.
- Produces (exact types — Task 7 renders these):

```ts
export interface ActivityRowBase {
  id: string;
  parentID?: string;
  level: number; // 1 = root's entries; +1 per nesting level
}
export interface ActivityJobRow extends ActivityRowBase {
  kind: "job";
  job: ActivityJob;
  live: boolean;
  transcriptRef?: string;
  parentRef: string; // owning session's ref, for openTranscript
}
export interface ActivityDelegateRow extends ActivityRowBase {
  kind: "delegate";
  delegate: ActivityDelegate;
  live: boolean;
  transcriptRef: string; // delegate.childRef
  parentRef: string; // owning session's ref
}
export interface ActivityFoldRow extends ActivityRowBase {
  kind: "fold";
  foldParentID: string; // activityNodeID of the owning session
  inactiveCount: number;
  failedCount: number;
}
export type ActivityRow = ActivityJobRow | ActivityDelegateRow | ActivityFoldRow;
export function foldRowID(sessionNodeID: string): string;
export function buildActivityRows(tree: ActivityTree, collapsedFolds: ReadonlySet<string>): ActivityRow[];
```

Semantics: per session, live entries render first in original order; then one fold row if any terminal entries exist; when the fold row's id is NOT in `collapsedFolds`, terminal entries render after the fold row in original order. Delegate rows with a `child` session recurse: child entries at `level + 1`, parented to the delegate row id. Sessions themselves never become rows.

- [ ] **Step 1: Write the failing tests**

Create `activityRows.test.ts`:

```ts
import { expect, test } from "vitest";
import type { ActivityTree } from "./activityData";
import { buildActivityRows, foldRowID } from "./activityRows";

function shell(jobId: string, terminal: boolean, status = terminal ? "completed" : "running") {
  return {
    kind: "shell" as const,
    job: {
      jobId,
      ownerSessionId: "sess_root",
      ownerRef: "ref_root",
      type: "shell",
      status,
      terminal,
      background: true,
      hasOutput: true,
      description: `job ${jobId}`,
      startedAt: "2026-08-05T15:00:00Z",
      outputBytes: 10,
    },
  };
}

function delegate(delegateId: string, opts: { active?: boolean; failed?: boolean; child?: unknown } = {}) {
  const status = opts.failed ? "failed" : opts.active ? "running" : "completed";
  return {
    kind: "delegate" as const,
    delegate: {
      delegateId,
      childSessionId: `sess_${delegateId}`,
      childRef: `ref_${delegateId}`,
      turns: [
        {
          jobId: `job_${delegateId}_t1`,
          ownerSessionId: "sess_root",
          ownerRef: "ref_root",
          type: "delegate",
          status,
          terminal: !opts.active,
          background: true,
          hasOutput: false,
          description: `turn of ${delegateId}`,
          startedAt: "2026-08-05T15:00:00Z",
          outputBytes: 0,
        },
      ],
      branch: {},
      child: opts.child,
    },
  };
}

function session(entries: unknown[], counts = { active: 0, failed: 0, completed: 0, complete: true }) {
  return {
    kind: "session" as const,
    sessionId: "sess_root",
    ref: "ref_root",
    label: "Root",
    aggregate: "working",
    counts,
    entries,
    branch: {},
  };
}

function tree(entries: unknown[]): ActivityTree {
  return { revision: 1, root: session(entries) as ActivityTree["root"] };
}

test("live entries render in order; terminal entries fold behind one row", () => {
  const rows = buildActivityRows(tree([shell("a", false), shell("b", true), shell("c", true), shell("d", false)]), new Set([foldRowID("session:sess_root")]));
  expect(rows.map((r) => r.id)).toEqual(["job:a", "job:d", "session:sess_root:inactive-fold"]);
  const fold = rows[2];
  expect(fold.kind === "fold" && fold.inactiveCount).toBe(2);
});

test("fold row counts failures separately", () => {
  const rows = buildActivityRows(tree([shell("x", true, "failed"), shell("y", true)]), new Set());
  const fold = rows.find((r) => r.kind === "fold");
  expect(fold?.kind === "fold" && fold.failedCount).toBe(1);
});

test("expanded fold reveals terminal rows after the fold row", () => {
  const rows = buildActivityRows(tree([shell("a", false), shell("b", true)]), new Set());
  expect(rows.map((r) => r.id)).toEqual(["job:a", "session:sess_root:inactive-fold", "job:b"]);
});

test("delegate children nest one level deeper under the delegate row", () => {
  const child = session([shell("gc", false)], { active: 1, failed: 0, completed: 0, complete: false });
  const rows = buildActivityRows(tree([delegate("dlg_1", { active: true, child })]), new Set());
  const drow = rows[0];
  const crow = rows[1];
  expect(drow.kind).toBe("delegate");
  expect(crow).toMatchObject({ kind: "job", level: 2, parentID: "delegate:dlg_1" });
});

test("all-terminal delegate folds as one inactive entry", () => {
  const rows = buildActivityRows(tree([delegate("dlg_1", {})]), new Set([foldRowID("session:sess_root")]));
  expect(rows.map((r) => r.id)).toEqual(["session:sess_root:inactive-fold"]);
});

test("no terminal entries renders no fold row", () => {
  const rows = buildActivityRows(tree([shell("a", false)]), new Set());
  expect(rows.every((r) => r.kind !== "fold")).toBe(true);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityRows.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement**

Create `activityRows.ts`:

```ts
// Pure row-model builder for the dense activity tree: walks a parsed
// ActivityTree and returns the flat list of rows to render. Live entries keep
// their original order; terminal entries collapse behind one fold row per
// parent session (revealed in original order when the fold is expanded).
// Sessions never become rows — the panel header covers the root and a delegate
// row stands in for its child session.
import {
  type ActivityDelegate,
  type ActivityEntry,
  type ActivityJob,
  type ActivitySessionNode,
  type ActivityTree,
  activityNodeID,
} from "./activityData";
import { isFailedStatus } from "./activityFormat";

export interface ActivityRowBase {
  id: string;
  parentID?: string;
  level: number;
}

export interface ActivityJobRow extends ActivityRowBase {
  kind: "job";
  job: ActivityJob;
  live: boolean;
  transcriptRef?: string;
  parentRef: string;
}

export interface ActivityDelegateRow extends ActivityRowBase {
  kind: "delegate";
  delegate: ActivityDelegate;
  live: boolean;
  transcriptRef: string;
  parentRef: string;
}

export interface ActivityFoldRow extends ActivityRowBase {
  kind: "fold";
  foldParentID: string;
  inactiveCount: number;
  failedCount: number;
}

export type ActivityRow = ActivityJobRow | ActivityDelegateRow | ActivityFoldRow;

export function foldRowID(sessionNodeID: string): string {
  return `${sessionNodeID}:inactive-fold`;
}

function jobIsActive(job: ActivityJob): boolean {
  return !job.terminal;
}

function sessionIsActive(session: ActivitySessionNode): boolean {
  return session.counts.active > 0 || session.entries.some(entryIsActive);
}

function entryIsActive(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return jobIsActive(entry.job);
  return entry.delegate.turns.some(jobIsActive) || (entry.delegate.child ? sessionIsActive(entry.delegate.child) : false);
}

function entryIsFailed(entry: ActivityEntry): boolean {
  if (entry.kind === "shell") return isFailedStatus(entry.job.status);
  const delegate: ActivityDelegate = entry.delegate;
  return delegate.turns.some((turn) => isFailedStatus(turn.status)) || (delegate.child?.counts.failed ?? 0) > 0;
}

export function buildActivityRows(tree: ActivityTree, collapsedFolds: ReadonlySet<string>): ActivityRow[] {
  const rows: ActivityRow[] = [];

  function appendEntry(entry: ActivityEntry, session: ActivitySessionNode, level: number): void {
    if (entry.kind === "shell") {
      rows.push({
        kind: "job",
        id: activityNodeID(entry),
        parentID: activityNodeID(session),
        level,
        job: entry.job,
        live: jobIsActive(entry.job),
        transcriptRef: entry.job.transcriptRef,
        parentRef: session.ref,
      });
      return;
    }
    const delegate = entry.delegate;
    const id = activityNodeID(entry);
    rows.push({
      kind: "delegate",
      id,
      parentID: activityNodeID(session),
      level,
      delegate,
      live: entryIsActive(entry),
      transcriptRef: delegate.childRef,
      parentRef: session.ref,
    });
    if (delegate.child) visitSession(delegate.child, level + 1);
  }

  function visitSession(session: ActivitySessionNode, level: number): void {
    const live: ActivityEntry[] = [];
    const inactive: ActivityEntry[] = [];
    for (const entry of session.entries) {
      (entryIsActive(entry) ? live : inactive).push(entry);
    }
    for (const entry of live) appendEntry(entry, session, level);
    if (inactive.length === 0) return;
    const id = foldRowID(activityNodeID(session));
    rows.push({
      kind: "fold",
      id,
      parentID: activityNodeID(session),
      level,
      foldParentID: activityNodeID(session),
      inactiveCount: inactive.length,
      failedCount: inactive.filter(entryIsFailed).length,
    });
    if (collapsedFolds.has(id)) return;
    for (const entry of inactive) appendEntry(entry, session, level);
  }

  visitSession(tree.root, 1);
  return rows;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/activityRows.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/chrome/activityRows.ts cmd/serf-hub/frontend/src/panes/session/chrome/activityRows.test.ts
git commit -m "feat(web): buildActivityRows with inactive folding"
```

---

### Task 6: TS — fold state in the activity panel store

**Files:**
- Modify: `cmd/serf-hub/frontend/src/stores/activityPanel.ts` (ActivityPanelEntry :24-32, EMPTY_ACTIVITY_PANEL_ENTRY :56-62, newEntry :66-74, store interface :41-48)
- Test: `cmd/serf-hub/frontend/src/stores/activityPanel.test.ts`

**Interfaces:**
- Produces: `ActivityPanelEntry.collapsedFoldIDs: string[]`; store action `toggleFold(ref: string, foldID: string): void` (adds the id when absent, removes when present).

- [ ] **Step 1: Write the failing test**

Append to `activityPanel.test.ts` (follow its existing store-reset idiom, e.g. `resetActivityPanelStoreForTests()` in a `beforeEach`):

```ts
test("toggleFold flips fold membership per session ref", () => {
  activityPanelStore.getState().toggleFold("ref_a", "session:s1:inactive-fold");
  expect(activityPanelStore.getState().entries.get("ref_a")?.collapsedFoldIDs).toEqual(["session:s1:inactive-fold"]);
  activityPanelStore.getState().toggleFold("ref_a", "session:s1:inactive-fold");
  expect(activityPanelStore.getState().entries.get("ref_a")?.collapsedFoldIDs).toEqual([]);
  // Independent per ref:
  activityPanelStore.getState().toggleFold("ref_a", "fold:1");
  activityPanelStore.getState().toggleFold("ref_b", "fold:2");
  expect(activityPanelStore.getState().entries.get("ref_a")?.collapsedFoldIDs).toEqual(["fold:1"]);
  expect(activityPanelStore.getState().entries.get("ref_b")?.collapsedFoldIDs).toEqual(["fold:2"]);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/stores/activityPanel.test.ts`
Expected: FAIL — `toggleFold` is not a function.

- [ ] **Step 3: Implement**

In `activityPanel.ts`:

1. `ActivityPanelEntry` — add `collapsedFoldIDs: string[];`.
2. `ActivityPanelStoreState` — add `toggleFold(ref: string, foldID: string): void;`.
3. `EMPTY_ACTIVITY_PANEL_ENTRY` and `newEntry()` — add `collapsedFoldIDs: [],`.
4. Store body, next to `setExpanded`:

```ts
  toggleFold(ref, foldID) {
    updateEntry(set, ref, (entry) => ({
      ...entry,
      collapsedFoldIDs: entry.collapsedFoldIDs.includes(foldID)
        ? entry.collapsedFoldIDs.filter((id) => id !== foldID)
        : [...entry.collapsedFoldIDs, foldID],
    }));
  },
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/stores/activityPanel.test.ts`
Expected: PASS (whole file — the new field must not break existing entry-shape assertions; if an existing test asserts exact `EMPTY_ACTIVITY_PANEL_ENTRY` shape, add `collapsedFoldIDs: []` to its expectation).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/stores/activityPanel.ts cmd/serf-hub/frontend/src/stores/activityPanel.test.ts
git commit -m "feat(web): fold state in activity panel store"
```

---

### Task 7: TS — dense `ActivityTree` rewrite

**Files:**
- Rewrite: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css` (add classes below)
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx` (rewrite)

**Interfaces:**
- Consumes: `buildActivityRows`, `ActivityRow` (Task 5); `formatUsagePair`, `formatQuietAge`, `quietAnchorMillis`, `jobStatusDotState`, `isFailedStatus` (Task 4); `openTranscript` from `../transcript/openTranscript` (signature `openTranscript(ref: string, parentRef?: string): void`); `StatusDot`, `Chevron`, `Button` from `../../../widgets`; `requireClass`.
- Produces (Task 9 wires these props):

```ts
export interface ActivityTreeProps {
  tree: ActivityTreeData;
  collapsedFoldIDs: string[];
  onToggleFold: (foldID: string) => void;
  continuationFailures?: Record<string, string | undefined>;
  onContinue?: (targetID: string, continuation: string) => void;
  loadingContinuationID?: string;
}
export interface ActivityTreeHandle {
  focusRow: (id: string) => void;
}
export const ActivityTree: ForwardRefExoticComponent<ActivityTreeProps & RefAttributes<ActivityTreeHandle>>;
```

The old exports `ActivitySelectionNode` and `findActivitySelection` are deleted (Task 9 removes their only consumer).

**Behavior contract (from spec):**
- One line per row: chevron ▸/▾ (toggles inline detail), `StatusDot`, kind glyph (`⌘` delegate / `$` shell, mono, `--ink-low`), name (medium weight when live), right-aligned mono meta.
- Meta: live delegate `↑in ↓out · quiet` (usage absent → `— · quiet`); live shell `— · quiet`; terminal delegate `↑in ↓out · duration-or-outcome`; terminal shell `duration` or `failed` (danger color). Quiet = `now − quietAnchorMillis(latest turn job / job)`, ticking.
- Delegate quiet anchor: the LAST turn job's `quietAnchorMillis`; with no turns, the delegate's own startedAt is unknown — hide the quiet segment (render tokens only).
- Terminal duration: `endedAt − startedAt` via `formatQuietAge`; missing `endedAt` → status text instead.
- Failed terminal rows: meta suffix `failed` in `--danger` (CSS class, not inline color).
- Fold row: `▸/▾ N inactive` (ink-mid) + `· M failed` (danger) when M > 0; click or Enter toggles via `onToggleFold(row.id)`; it is a focusable `treeitem` with `aria-expanded`.
- Row body click → `openTranscript(row.transcriptRef ?? `job:${row.job.jobId}`, row.parentRef)` for jobs (mirror the existing `ActivityTranscriptAction` fallback: today `ActivityTree` passes `job.transcriptRef` which the backend populates — check `ActivityTranscriptAction.tsx`; if it has a `job:<id>` fallback, replicate it exactly) and `openTranscript(row.transcriptRef, row.parentRef)` for delegates. Rows without any transcript target are not clickable (no handler, `aria-disabled` absent — just don't attach).
- Chevron click → toggles that row's inline detail (local state, one open at a time), `stopPropagation`, does NOT call `openTranscript`.
- Detail strip: `ActivityRowDetail` (Task 8) rendered directly under its row.
- Keyboard (roving tabIndex, existing pattern): ↑/↓ move focus; Enter/Space activates (job/delegate → open transcript; fold → toggle); → on a job/delegate row opens its detail, ← closes it; on fold rows → expands, ← collapses.
- Live ticking: `const [now, setNow] = useState(() => Date.now())`; a `useEffect` starts a 1s `setInterval` only while `rows.some(r => r.kind !== "fold" && r.live)`, cleared otherwise and on unmount.
- Continuation: for any session whose `branch.continuation` is set, render the existing "Load more" strip after that session's rows (root: after the last root row; delegate child: after its last child row). Reuse the existing markup/logic from the current `ActivityTree.tsx` lines 417-442 verbatim, driven by a small map from sessionNodeID → continuation built during the same walk (extend `buildActivityRows` locally in the component file — do NOT change Task 5's module: compute `continuations: { sessionNodeID, token, targetID, afterRowID }[]` with a separate small walk in `ActivityTree.tsx`).

**CSS classes to add to `activitypanel.module.css`** (replace usage of `rowContent`/`rowMain`/`rowMeta` for the new rows; keep old classes until Task 10 cleanup):

```css
/* Dense activity tree rows (2026-08-05 redesign). */
.denseRow {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 2px var(--space-3);
  font-size: var(--font-size-ui);
  color: var(--ink-hi);
  cursor: pointer;
}
.denseRow:hover { background: var(--surface-2); }
.denseRow:focus-visible {
  outline: 1px solid var(--accent);
  outline-offset: -1px;
}
.denseName {
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.denseNameLive { font-weight: var(--font-weight-medium); }
.denseKind {
  width: 14px;
  flex: none;
  text-align: center;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--ink-low);
}
.denseMeta {
  flex: none;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: var(--font-size-caption);
  color: var(--ink-low);
}
.denseQuiet { color: var(--ink-mid); }
.denseFailed { color: var(--danger); }
.foldRow {
  composes: denseRow;
  color: var(--ink-mid);
}
.rowToggle { /* keep existing; ensure 12px hit slot, chevron centered */ }
.detailStrip {
  margin: 2px var(--space-3) 6px calc(var(--space-3) + 20px);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-0);
  border: 1px solid var(--edge);
  border-radius: var(--radius-control);
  font-size: var(--font-size-caption);
  color: var(--ink-mid);
}
.detailCommand {
  display: block;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--ink-hi);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.detailMeta {
  margin-top: var(--space-1);
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--ink-low);
}
.detailActions { margin-top: var(--space-2); display: flex; gap: var(--space-2); }
.indentGuide {
  border-left: 1px solid var(--edge);
  margin-left: calc(var(--space-3) + 6px);
}
```

(If `composes` is not used elsewhere in this codebase, write `.foldRow` out longhand instead — check neighboring modules first. Nested groups wrap children in `<div className={CLASS.indentGuide}>` for level ≥ 2.)

**Component skeleton** (fill in from the behavior contract; the existing file's keyboard/focus machinery — `focusRow`, `rowRefs`, `pendingRefocusIDRef`, roving tabIndex — carries over nearly verbatim, minus selection):

```tsx
export const ActivityTree = forwardRef<ActivityTreeHandle, ActivityTreeProps>(function ActivityTree(
  { tree, collapsedFoldIDs, onToggleFold, continuationFailures = {}, onContinue, loadingContinuationID },
  ref,
) {
  const [detailID, setDetailID] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const rows = useMemo(() => buildActivityRows(tree, new Set(collapsedFoldIDs)), [tree, collapsedFoldIDs]);
  const hasLive = rows.some((row) => row.kind !== "fold" && row.live);
  useEffect(() => {
    if (!hasLive) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [hasLive]);
  // ... focus machinery from the old file; activate(row):
  //   fold → onToggleFold(row.id)
  //   job/delegate with transcriptRef → openTranscript(row.transcriptRef, row.parentRef)
  // render: rows.map(row => row.kind === "fold" ? foldRow(row) : denseRow(row)),
  //   denseRow renders chevron button (detail toggle), StatusDot, kind glyph,
  //   name, meta; detail strip under the row when row.id === detailID.
});
```

- [ ] **Step 1: Write the failing tests**

Rewrite `ActivityTree.test.tsx` around the new contract (keep the file's existing `openTranscript` mock pattern — `vi.mock("../transcript/openTranscript", () => ({ openTranscript: vi.fn() }))`). Required cases:

```ts
// 1. renders one dense row per live entry with kind glyph and meta
//    (live delegate shows "↑41k ↓6k · 12s"-shaped meta given usage +
//    lastOutputAt fixtures; vi.useFakeTimers() + vi.setSystemTime to pin now).
// 2. terminal entries hidden behind "2 inactive" fold row; clicking the fold
//    row calls onToggleFold with "session:sess_root:inactive-fold" and does
//    NOT call openTranscript.
// 3. clicking a job row calls openTranscript("job:…", "ref_root") — match the
//    transcriptRef fixtures; clicking a delegate row calls
//    openTranscript("ref_child", "ref_root").
// 4. clicking a row's chevron reveals the detail strip (command text visible)
//    and does NOT call openTranscript; clicking another row's chevron moves
//    the strip (only one open).
// 5. keyboard: ArrowDown/ArrowUp move focus (document.activeElement),
//    Enter on a job row calls openTranscript, Enter on the fold row calls
//    onToggleFold.
// 6. old-daemon shape (no usage, no lastOutputAt): delegate meta is
//    "— · <quiet>" measured from the last turn's startedAt; no token glyphs.
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityTree.test.tsx`
Expected: FAIL — prop/type errors against the old component.

- [ ] **Step 3: Implement the rewrite + CSS**

Per the skeleton and behavior contract above. Delete `ActivitySelectionNode`, `findActivitySelection`, and all selection/inspector plumbing from the file.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityTree.test.tsx`
Expected: PASS

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/ActivityTree.tsx src/panes/session/chrome/ActivityTree.test.tsx src/panes/session/chrome/activitypanel.module.css
git add cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityTree.test.tsx cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css
git commit -m "feat(web): dense activity tree rows with fold + inline detail"
```

---

### Task 8: TS — `ActivityRowDetail` inline strip

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityRowDetail.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityRowDetail.test.tsx`

**Interfaces:**
- Consumes: `ActivityJobRow`/`ActivityDelegateRow` (Task 5); `OpenTranscriptButton` from `../transcript/openTranscript` (props `{ transcriptRef: string; parentRef?: string; label?: string }`); `formatQuietAge` (Task 4).
- Produces:

```ts
export function ActivityRowDetail(props: { row: ActivityJobRow | ActivityDelegateRow; now: number }): JSX.Element;
```

Content: mono `code` line — `job.command ?? job.task ?? job.description` for jobs, `delegate.mandate ?? child label ?? childSessionId` for delegates. Meta line — live: `running <formatQuietAge(now − quietAnchorMillis(latest turn or job))> · <outputBytes> output bytes · started <formatClockTime(startedAt)>`; terminal: `duration <…> · exit <code> · <outputBytes> output bytes`. Actions row: `OpenTranscriptButton` with the row's refs. Reuse `formatClockTime` from `../transcript/messages/format`.

- [ ] **Step 1: Write the failing tests**

```tsx
// renders the command in mono for a shell job row
// renders the mandate for a delegate row, falling back to childSessionId
// live row meta says "running …", terminal row meta shows duration and exit code
// OpenTranscriptButton receives the row's transcriptRef and parentRef
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityRowDetail.test.tsx`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement** per the contract, using the `detailStrip`/`detailCommand`/`detailMeta`/`detailActions` classes from Task 7's CSS.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityRowDetail.test.tsx`
Expected: PASS

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/ActivityRowDetail.tsx src/panes/session/chrome/ActivityRowDetail.test.tsx
git add cmd/serf-hub/frontend/src/panes/session/chrome/ActivityRowDetail.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityRowDetail.test.tsx
git commit -m "feat(web): inline activity row detail strip"
```

---

### Task 9: TS — `ActivityPanel` single-column rewiring

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx` (body render :225-327)
- Delete: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityInspector.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx`

**Interfaces:**
- Consumes: new `ActivityTreeProps` (Task 7); `activityPanelStore.getState().toggleFold` (Task 6).
- Removes: `ActivityInspector`, `findActivitySelection`, `selection`, `showMobileTree` mobile swap, `setSelected`/`setExpanded` wiring to the tree.

- [ ] **Step 1: Update the failing tests**

In `ActivityPanel.test.tsx`: replace inspector assertions (`activity-inspector` testid, "Select activity") with: tree renders rows; no inspector element exists; clicking a fold row updates the store. Keep the load-state tests (unsupported/failed/loading/ended) intact — they must keep passing unchanged.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx`
Expected: FAIL — inspector gone/new props.

- [ ] **Step 3: Implement**

In `ActivityPanelBody`:
- Delete the `selection` memo, `showMobileTree` state and its three effects, `handleBackToActivity`, and both `ActivityInspector` renders.
- Replace the `masterDetail`/mobile split in `renderBody` with a single `<div className={CLASS.panelColumn}>` wrapping `<ActivityTree …>`:

```tsx
<ActivityTree
  ref={treeRef}
  tree={currentTree}
  collapsedFoldIDs={entry.collapsedFoldIDs}
  onToggleFold={(foldID) => activityPanelStore.getState().toggleFold(sessionRef, foldID)}
  continuationFailures={entry.continuationFailures}
  onContinue={handleContinue}
  loadingContinuationID={entry.continuationLoadingID}
/>
```

- Remove now-unused imports (`ActivityInspector`, `findActivitySelection`, `useIsMobile` if unused after) and unused `CLASS` entries (`masterDetail`, `mobilePane`, `mobileBack` — delete these CSS classes from `activitypanel.module.css` too).
- Delete `ActivityInspector.tsx`. Check for other importers first: `rg -n "ActivityInspector" src --glob '!*.test.*' -l` must return only `ActivityPanel.tsx` before deletion; then delete any `ActivityInspector`-specific test file if one exists (`rg -l "ActivityInspector" src`).
- `git grep -n "findActivitySelection\|ActivitySelectionNode"` after the edit must return nothing.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/SessionChrome.test.tsx`
Expected: PASS

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/ActivityPanel.tsx src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/activitypanel.module.css
git add -u cmd/serf-hub/frontend/src/panes/session/chrome/
git commit -m "feat(web): activity panel drops inspector for dense tree"
```

---

### Task 10: Cleanup + full gates

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css` (remove now-unreferenced inspector classes: `inspector`, `inspectorHeader`, `inspectorTitle`, `inspectorSection`, `inspectorMeta`, `detailList`, `detailRow`, `detailLabel`, `detailValue`, `prompt`, `turnsList`, `turnRow`, and any others `rg` shows unreferenced)

- [ ] **Step 1: Remove dead CSS**

For each candidate class: `rg -n "styles\.<name>|\"<name>\"" cmd/serf-hub/frontend/src --glob '*.tsx' --glob '*.ts'` — delete from the module only when unreferenced. `requireClass` throws on missing classes, so a wrong deletion fails loudly in tests (that's the safety net, not a reason to skip the grep).

- [ ] **Step 2: Frontend unit + typecheck + Biome gate**

Run: `make test-web`
Expected: PASS (vitest, tsc, biome across the frontend).

- [ ] **Step 3: Browser gate**

Run: `make test-web-browser`
Expected: PASS

- [ ] **Step 4: Go gate**

Run: `go test ./agent/ ./appwire/ -count=1`
Expected: PASS

- [ ] **Step 5: Generated-code gate**

Run: `make lint-generated`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -u cmd/serf-hub/frontend/src/panes/session/chrome/activitypanel.module.css
git commit -m "chore(web): drop dead activity inspector styles"
```

---

## Self-Review Notes (filled by the plan author)

- Spec coverage: rows/tokens/quiet (Tasks 1-4, 7), hierarchy+fold (5, 7), interaction click/chevron/keyboard (7), inline detail (8), inspector removal + panel (9), live updates ticker (7), backend degradation for old daemons (3 test 3, 7 test 6), testing gates (10). Continuation "Load more" preserved (7). Mobile simplification (9).
- Type consistency: `ActivityUsage`, `ActivityRow*`, `foldRowID`, `toggleFold`, `formatUsagePair`, `quietAnchorMillis`, `jobStatusDotState` names are used identically across tasks.
- Deliberate scope cuts vs. the old UI: per-row `expandedIDs` subtree expansion is gone (fold rows replace it); `disclosure.selectedID`/`setSelected` remain in the store but unwired (harmless; removing them would churn `reconcileActivityState` for no behavioral gain).
