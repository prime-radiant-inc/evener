# Observer Job Read Grant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the §5.1 observer read grant reachable from the model-facing
surface: a `job.notification` watch frame mints a durable read grant for the
receiving observer on the concrete terminal job the frame names, and the
observer spends it through `read_transcript(transcript_ref="job:<job_id>")`.

**Architecture:** Frame-referenced grant, minted by the runtime at delivery
(`recordWatchSend` → `mintWatchSendReadGrant`, derived from the delivered
`events.JobFinishedData` payload), spent through one shared read-resolution
chain (`Session.resolveJobRead`) that both `job_read_output` and
`read_transcript`'s `job:` path run: local store → one-hop owner → live
descendant walk → durable grant table → the original error.

**Tech Stack:** Go, `agent/` module only (`go.work` lists it as its own
module; run its tests from `agent/`).

**Spec:** `docs/superpowers/specs/2026-07-31-observer-job-read-grant-design.md`

**Kata:** eqs0. Blocks: fd8n (docs + scenario-card sweep; NOT this plan's work).

## Jesse's rulings, 2026-07-31

These are binding; the spec carries them under "Open decisions for Jesse".

| # | Ruling | Where it lands |
| --- | --- | --- |
| 1 | Extend `read_transcript`'s `job:<id>` path. No `job_read_output` re-registration, no new tool, never `transcript_ref` in the frame. | Task 3 |
| 2 | `job_status` on a granted job stays denied; improve the error text to point at the sanctioned `read_transcript` call. | Task 5 |
| 3 | No revocation — grants stay append-only. | no code; asserted by Task 6's reopen test |
| 4 | Delete `mintWatchCreateReadGrant`; terminal-only becomes structural. | Task 1 |
| 5 | Skip self-grants **at the mint** when the finished job is the receiver's own delegate job. Two-case test. | Task 2 |
| 6 | Take both: close the depth ≥ 2 descendant-read gap in the same seam. Chain: local → one-hop → descendant walk → grant → original error. | Task 3 |

### Ruling 5, restated precisely

The stamped ruling reads "the finished job's `DelegateID` equals the receiver
session id". Those two fields can never be equal: `events.JobFinishedData.DelegateID`
(`agent/events/payloads.go:521`) is a `dlg_` handle and `watchConfig.receiverSessionID`
is a session id. The ruling's purpose — "the table should hold only rows that
confer real access" — has exactly one implementation: compare the finished
job's `DelegateID` against `watchConfig.receiverDelegateID`, which is the same
`dlg_` handle in the same watched session's namespace (`installParentSourceWatchForChild`,
`agent/subagents.go:811-812`). It is also the stable one: a delegate id survives
resume, which is why the grant keys on the child session id rather than a job id.
This plan implements the delegate-handle comparison and flags the phrasing as a
premise correction.

## Global constraints

- Tests are deterministic: no provider credentials, no network, no sleeps.
- `agent/` is its own module. Run tests from `agent/`, e.g.
  `cd agent && go test -count=1 -run TestX ./`.
- Targeted runs only. `make lint` at the end covers all seven modules.
- `//go:build serffuzz` files are not linted by `make lint` but must keep
  compiling: verify with `cd agent && go vet -tags serffuzz ./...`.
- `docs/job-control.md` and `test/scenarios/` are **out of scope** — kata fd8n
  sweeps them. The spec doc may gain a short "implemented" status line.
- Comments say WHAT and WHY, never what changed.

---

### Task 1: delete the unreachable create-time mint (ruling 4)

> **Landed together with Task 2, in one commit.** There is no green commit
> between them. `TestGrantSurvivesWatchClearAndStoreReopen` (ruling 3's test)
> clears its watch before any fire, so the create mint is its only grant
> source; and `TestTerminalCatchupSendMintsObserverReadGrant`'s catch-up fire
> is the per-fire mint's only source. Deleting the create mint first breaks the
> former; making the per-fire mint payload-derived first breaks the latter. The
> two halves are one seam: "terminal-only becomes structural".

**Files:**
- Modify: `agent/job_watch.go` (`mintWatchCreateReadGrant` at :3259-3303; its
  call site at :587-597)
- Modify: `agent/job_watch_loopguard_test.go` (`TestWatchCreateGrantAppendFailureFailsCreationLoudly`, :627)
- Modify: `agent/job_watch_timers_observe_fuzz_test.go` (:103)
- Modify: `agent/watch_grant_lifecycle_fuzz_test.go` (:134)

**Why it is a pure deletion:** `send` is not a public `job_watch` argument
(`watchArgsFromToolArgs`, `agent/session_tools_jobs.go:1986`), so the only
reachable `cfg.send != nil` shape is the receiver-internal one
`applyReceiverWatchSend` synthesizes from `ReceiverDelegateID`
(`agent/job_watch.go:1018`), whose target is always the `caller` alias. The
create mint returns early on `isWatchSessionTarget(cfg.target)`, so it has no
production caller. Deleting it also removes the one path that could grant on a
*running* job, which is what makes terminal-only hold by construction.

- [ ] **Step 1: Prove the deletion is compiler-checked, not grep-checked**

Delete `mintWatchCreateReadGrant` and its call site, then:

Run: `cd agent && go build ./... && go vet ./... && go vet -tags serffuzz ./...`
Expected: FAIL, naming exactly the three test call sites above. (This is the
completeness net — `docs/conventions/go-workspace.md` records why grep is not.)

- [ ] **Step 2: Rework the named callers**

- `TestWatchCreateGrantAppendFailureFailsCreationLoudly` — delete. It asserts
  "a create-time grant append failure fails watch creation"; there is no
  create-time grant. Its sibling `TestWatchPerFireGrantAppendFailureProceedsWithSend`
  (:663) keeps the surviving failure policy covered and needs no change (it
  already fires an `EventJobFinished` payload).
- `job_watch_timers_observe_fuzz_test.go:103` — delete the
  `mintWatchCreateReadGrant` assertion block ("not a grantable delegate" is a
  create-mint-only error).
- `watch_grant_lifecycle_fuzz_test.go:127-150` — drop the `mintWatchCreateReadGrant`
  call; the per-fire assertions move to Task 2 (they need the new signature).

- [ ] **Step 3: Verify**

Run: `cd agent && go build ./... && go vet ./... && go vet -tags serffuzz ./...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add agent/job_watch.go agent/job_watch_loopguard_test.go \
        agent/job_watch_timers_observe_fuzz_test.go agent/watch_grant_lifecycle_fuzz_test.go
git commit -m "agent: delete the unreachable create-time watch read grant mint"
```

---

### Task 2: mint the grant from the delivered job.notification payload (rulings 4, 5)

**Files:**
- Modify: `agent/job_watch.go` (`mintWatchSendReadGrant` :3305-3353;
  `recordWatchSend` :3034-3070; new `watchGrantableJob` helper;
  new `appendWatchFrameGrantedRead` helper near `buildWatchFrame` :4723)
- Test: `agent/job_watch_grant_mint_test.go` (new)
- Modify: `agent/job_watch_test.go` (`newGrantReadFixture` :604 — retarget it at
  the reachable parent-source shape)
- Modify: `agent/job_watch_loopguard_test.go` (`TestTerminalCatchupSendMintsObserverReadGrant` :600;
  `TestGrantSurvivesWatchClearAndStoreReopen` :839)
- Modify: `agent/watch_grant_lifecycle_fuzz_test.go`

**Interfaces produced:**

```go
// watchGrantableJob returns the concrete job a delivered watch event
// structurally names, and the delegate that ran it. Only
// events.EventJobFinished payloads name a job, which is what makes every
// read grant terminal-only by construction (spec §5.1): the job is already
// finished when the payload naming it is built. Every other payload —
// communicate, assistant.tool, output_match and progress ticks (whose root
// event carries no data at all) — grants nothing.
func watchGrantableJob(data events.EventData) (jobID, delegateID string, ok bool)

// mintWatchSendReadGrant ... returns the granted job id, or "" when this
// delivery granted nothing.
func (jm *jobManager) mintWatchSendReadGrant(cfg *watchConfig, resolvedSendTo string, data events.EventData) string
```

**Behaviour:**

1. Early return `""` when `cfg == nil` or `resolvedSendTo == runtimeMessageAliasCaller`
   (caller delivery grants nothing — the caller owns its own jobs). The
   `isWatchSessionTarget(watchedIdentity)` early return is **removed**: the
   grantable id no longer comes from the watched identity.
2. `watchGrantableJob(data)`; `!ok` → `""`.
3. **Ruling 5:** `delegateID != "" && delegateID == strings.TrimSpace(cfg.receiverDelegateID)`
   → `""`. The observer already owns that output; with grants never revoked, a
   self-grant row is permanent junk and surfaces the observer as its own
   observer in `LoadSessionObserverGrants`.
4. Dedup on `watchGrantKey{sendTo: resolvedSendTo, watchedJobID: jobID}`
   unchanged; a dedup hit returns the job id (the observer still holds the
   grant, so the frame line stays honest).
5. Observer resolution, append, `recordObserverLink`, failure policy: unchanged.
   A failed append returns `""`.

**`recordWatchSend` change** (`agent/job_watch.go:3039-3044`):

```go
	if terr == nil {
		// Mint the observer read grant BEFORE the pending persist so a durable
		// pending send always implies its grant was at least attempted
		// (restore re-delivers pendings without re-running this path).
		if grantedJobID := jm.mintWatchSendReadGrant(d.cfg, target, d.eventData); grantedJobID != "" {
			d.frame = appendWatchFrameGrantedRead(d.frame, grantedJobID)
		}
	}
```

**Frame line** (spec "What has to change" item 3). The frame is the observer's
only teaching surface for a capability it acquires mid-run:

```go
// appendWatchFrameGrantedRead names the one call this delivery's read grant
// answers. It is appended after buildWatchFrame applied its cap, for the same
// reason selfInfluenceNotice is prepended after it: the annotation must not be
// what the cap eats.
func appendWatchFrameGrantedRead(frame, jobID string) string {
	if frame == "" {
		return frame
	}
	if !strings.HasSuffix(frame, "\n") {
		frame += "\n"
	}
	return frame + `read with: read_transcript(transcript_ref="job:` + jobID + `")` + "\n"
}
```

- [ ] **Step 1: Write the failing tests** (`agent/job_watch_grant_mint_test.go`)

All four use the reachable parent-source shape: `Target: "caller"`,
`Events: ["job.notification"]`, `ReceiverSessionID`, `ReceiverDelegateID`
(which `applyReceiverWatchSend` turns into the internal send).

```go
// TestJobNotificationDeliveryMintsObserverReadGrant is the reachable mint:
// the shape installParentSourceWatchForChild builds, fired by a terminal job,
// grants the receiver SESSION a read on the concrete job the payload names.
func TestJobNotificationDeliveryMintsObserverReadGrant(t *testing.T)

// TestNonJobEventDeliveryMintsNoReadGrant: the same watch delivering a
// communicate frame and an assistant.tool frame mints nothing — those payloads
// carry no structured job reference (spec non-goal 3).
func TestNonJobEventDeliveryMintsNoReadGrant(t *testing.T)

// TestWatchSendGrantSkipsReceiverOwnDelegateJob is ruling 5's two cases: the
// observer's own resumed callback job mints nothing; a different delegate's
// completion still mints.
func TestWatchSendGrantSkipsReceiverOwnDelegateJob(t *testing.T)

// TestJobNotificationFrameNamesTheGrantedRead: a delivery that minted carries
// the read_transcript line; a delivery that minted nothing does not.
func TestJobNotificationFrameNamesTheGrantedRead(t *testing.T)
```

Assertions read `loadGrantTable(t, jm)` / `countWatchReadGrantEvents(t, jm)`
(`agent/job_watch_test.go:564,573`) and `loadWatchSendRecord(t, jm).Pending`
for the frame text.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test -count=1 -run 'TestJobNotificationDeliveryMints|TestNonJobEventDeliveryMints|TestWatchSendGrantSkipsReceiverOwn|TestJobNotificationFrameNames' ./`
Expected: FAIL — no grant rows (the mint bails on `isWatchSessionTarget("caller")`).

- [ ] **Step 3: Implement**

As specified above.

- [ ] **Step 4: Rework the fixtures the deleted create-mint fed**

- `newGrantReadFixture` (`agent/job_watch_test.go:604`): replace the
  concrete-target `output_match` watch with the parent-source shape, and drive
  the mint with `onSessionEventKD(parentJM, events.EventJobFinished, events.JobFinishedData{JobID: watched.JobID, ...})`
  after the watched job finalizes. The fixture then exercises the shape a model
  can actually reach; every granted-read test built on it keeps its meaning.
- `TestTerminalCatchupSendMintsObserverReadGrant` (`agent/job_watch_loopguard_test.go:600`):
  its premise dies with the payload-derived rule — a terminal `output_match`
  catch-up carries no `job.notification` payload. Rewrite it as the boundary
  pin for the new structural property (rename to
  `TestTerminalCatchupSendMintsNoReadGrant`), asserting the catch-up fires and
  mints zero grants.
- `TestGrantSurvivesWatchClearAndStoreReopen` (:839): same retarget as the
  fixture — mint through a `job.notification` delivery, then clear, close,
  reopen, and assert the grant still folds and still reads. This is ruling 3's
  test.
- `watch_grant_lifecycle_fuzz_test.go`: update the `mintWatchSendReadGrant`
  calls to the new signature, passing `events.JobFinishedData{JobID: ...}` for
  the minting cases and a `communicate` payload for a non-minting case.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd agent && go test -count=1 -run 'TestJobNotificationDeliveryMints|TestNonJobEventDeliveryMints|TestWatchSendGrantSkipsReceiverOwn|TestJobNotificationFrameNames|TestTerminalCatchupSendMints|TestGrantSurvives|TestGrantedRead|TestNonGrantedRead|TestWatchPerFireGrantAppendFailure' ./`
Expected: PASS

Run: `cd agent && go vet -tags serffuzz ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/job_watch.go agent/job_watch_grant_mint_test.go agent/job_watch_test.go \
        agent/job_watch_loopguard_test.go agent/watch_grant_lifecycle_fuzz_test.go
git commit -m "agent: mint the observer read grant from the delivered job.notification payload"
```

---

### Task 3: one shared job read chain, reached from read_transcript (rulings 1, 6)

**Files:**
- Modify: `agent/session_tools_jobs.go` (extract the chain out of
  `jobReadOutputTool` :449-504)
- Modify: `agent/session_tool_registry.go` (`toolDeps` :109-110; `newToolDeps` :252)
- Modify: `agent/session_tools_transcript.go` (`readJobTranscript` :374-410)
- Modify: `agent/transcript_test.go:3518`,
  `agent/transcript_read_tools_exact_fuzz_test.go:109,123`,
  `agent/transcript_render_lookup_exact_fuzz_test.go:448` (deps construction)
- Test: `agent/session_tools_transcript_job_read_test.go` (new)

**Interfaces produced:**

```go
// jobReadResolution names where a model-facing job output read is served from
// once the chain has run: the caller's own store, a direct child's, a live
// descendant at depth >= 2, or the parent-minted watch read grant (spec §5.1).
type jobReadResolution struct {
	manager        *jobManager
	readSession    *Session
	fallbackTarget *Session
	granted        *grantedJobRead
	deepDescendant bool
}

// resolveJobRead walks the read chain in one place so every model-facing job
// output read resolves identically: local store, one-hop owner, live
// descendant walk, then the durable grant table. A miss at every step returns
// the error the earlier steps produced, so a caller holding no grant learns
// nothing about whether the job exists.
func (s *Session) resolveJobRead(jobID string) (jobReadResolution, error)

// snapshot serves one window through whichever path resolveJobRead chose.
func (r jobReadResolution) snapshot(jobID string, readBytes int, fromHead bool, grepRE *regexp.Regexp) (jobReadOutputSnapshot, error)
```

`toolDeps` gains one field, populated in `newToolDeps` from a closure over the
session so it resolves at call time rather than registration time:

```go
	// jobRead resolves a job:<job_id> transcript ref through the same chain
	// job_read_output uses — the caller's store, a direct child's, a live
	// descendant, then the parent-minted watch read grant (spec §5.1) — and
	// returns a bounded snapshot of the job's retained output.
	jobRead func(jobID string, readBytes int) (jobReadOutputSnapshot, error)
```

`readJobTranscript` then becomes: parse the ref, call `deps.jobRead(jobID, maxJobOutputRetentionBytes)`,
render, bound. It gains the closed-store fallback and the descendant walk for
free, because it is now the same chain.

**Why extract rather than duplicate:** the chain is ~25 lines with three
subtle invariants already documented at `jobReadOutputTool` (`readSession` is
the owner for depth ≥ 2; `fallbackTarget` is the owner's *direct parent*;
`deepDescendant` and `granted` are both snapshot-only). Copying it into
`readJobTranscript` is how a half-wired resolution chain gets shipped.

- [ ] **Step 1: Write the failing tests** (`agent/session_tools_transcript_job_read_test.go`)

```go
// TestReadTranscriptServesGrantedJobOutput: the grant holder reads the watched
// job's output through the job: ref, with honest total_bytes/dropped_bytes.
func TestReadTranscriptServesGrantedJobOutput(t *testing.T)

// TestReadTranscriptStrangerKeepsOriginalNotFound: a session with no grant
// gets byte-identical `job "job_..." not found` — no oracle that the job exists.
// Asserted against errJobNotFound(jobID).Error() rather than a substring.
func TestReadTranscriptStrangerKeepsOriginalNotFound(t *testing.T)

// TestReadTranscriptResolvesDescendantJobAtDepthTwo is ruling 6: a job owned
// by a grandchild resolves through the descendant walk and is served from the
// OWNER's store.
func TestReadTranscriptResolvesDescendantJobAtDepthTwo(t *testing.T)

// TestReadTranscriptChainFallsThroughWalkToGrantThenError pins the ORDER: with
// a live subtree that does not own the job, the walk falls through to the grant
// lookup (hit -> served) and, for an ungranted id, to the original error.
func TestReadTranscriptChainFallsThroughWalkToGrantThenError(t *testing.T)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test -count=1 -run 'TestReadTranscript(ServesGranted|Stranger|ResolvesDescendant|ChainFalls)' ./`
Expected: FAIL — `job "job_..." not found` for the granted and descendant reads
(today `readJobTranscript` resolves only `deps.jobManager`).

- [ ] **Step 3: Implement**

Extract `resolveJobRead` + `jobReadResolution.snapshot` from `jobReadOutputTool`
without changing its behaviour, add the `toolDeps.jobRead` field, and rewrite
`readJobTranscript` on top of it. Keep the unavailable-guard message
(`"job transcript unavailable: job manager is not available"`) and switch its
condition to `deps == nil || deps.jobRead == nil`.

Update the three literal-`toolDeps` test sites to build the field from the real
production factory over a bare `&Session{jobManager: jm}` (all of
`sessionJobManager`, `ownerJobManagerFor`, and `liveSubagentSessions` are
nil-safe on one), so those tests keep exercising the real chain.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test -count=1 -run 'TestReadTranscript(ServesGranted|Stranger|ResolvesDescendant|ChainFalls)' ./`
Expected: PASS

Run: `cd agent && go test -count=1 -run 'TestGrantedRead|TestNonGrantedRead|TestGrantSurvives|TestJobReadOutput|TestReadJobTranscript|TestJobTranscript' ./`
Expected: PASS (the extraction must not move `job_read_output`'s behaviour)

Run: `cd agent && go vet ./... && go vet -tags serffuzz ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/session_tools_jobs.go agent/session_tool_registry.go \
        agent/session_tools_transcript.go agent/session_tools_transcript_job_read_test.go \
        agent/transcript_test.go agent/transcript_read_tools_exact_fuzz_test.go \
        agent/transcript_render_lookup_exact_fuzz_test.go
git commit -m "agent: read_transcript job refs resolve through the full job read chain"
```

---

### Task 4: render a delegate job as a delegate

**Files:**
- Modify: `agent/session_tools_transcript.go` (`renderShellJobTranscript` :412)
- Test: `agent/session_tools_transcript_job_read_test.go`

**Why:** the jobs named in a `job.notification` frame are usually delegate
jobs. A delegate has no `command`, its retained output is its final prose
report (`delegateOutputBytes`, `agent/job_delegate.go:2450`), and it carries a
`structured_result`. Rendering that under `# Shell Job` with a missing
`command:` line would be a lie in the model's evidence stream. This is the
first caller that can pass a delegate `job:` ref — terminal notifications hand
out the child's *session* ref, not a `job:` ref
(`notificationTranscriptRef`, `agent/job_notify.go:119`).

- [ ] **Step 1: Write the failing test**

```go
// TestReadTranscriptRendersDelegateJobAsDelegate: a granted read of a delegate
// job renders its task, its report, and its structured_result — never a
// "# Shell Job" heading.
func TestReadTranscriptRendersDelegateJobAsDelegate(t *testing.T)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -count=1 -run TestReadTranscriptRendersDelegateJobAsDelegate ./`
Expected: FAIL — content starts `# Shell Job`.

- [ ] **Step 3: Implement**

Add `renderJobTranscript(rec, output, total, dropped)` that dispatches on
`rec.Type == jobstore.JobDelegate` to a new `renderDelegateJobTranscript`
(heading, status, task, byte accounting, the report block, and the
`structured_result` as JSON with its validity flag, mirroring
`formatJobReadOutput`'s `structured_result (valid=%v)` shape). Leave
`renderShellJobTranscript` alone — `transcript_render_lookup_exact_fuzz_test.go:451`
pins its nil-record behaviour.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test -count=1 -run 'TestReadTranscript' ./`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/session_tools_transcript.go agent/session_tools_transcript_job_read_test.go
git commit -m "agent: a delegate job:<id> read renders as a delegate, not a shell job"
```

---

### Task 5: job_status stays denied, and says where to read (ruling 2)

**Files:**
- Modify: `agent/session_tools_jobs.go` (`jobStatusTool` :648-669)
- Test: `agent/session_tools_transcript_job_read_test.go`

**Why denied:** `jobTranscriptRef` (`agent/session_tools_jobs.go:2633`)
projects a delegate job's **session** transcript ref, and `resolveTranscript`
(`agent/transcript_lookup.go:23`) gates nothing (spec non-goal 4). A
grant-aware `job_status` would silently convert a one-job output grant into
full read access to the child's conversation. The frame already carries
`status`, `reason`, `exit_code`, and `output_bytes`, so nothing is lost.

**Change:** on the resolution error, consult the grant lookup; a hit swaps the
message for one that names the sanctioned call. A miss keeps the original error
byte-for-byte, so the message is never an oracle.

```go
// jobStatusGrantHint keeps job_status denied for a watch-granted job — status
// projects the delegate's session transcript_ref, which is a far larger
// capability than the one-job output grant (spec §5.1) — while naming the read
// the observer is actually allowed. A job with no grant keeps its original
// not-found error, so the message never discloses that a job exists.
func (s *Session) jobStatusGrantHint(jobID string, err error) error
```

Message: `job %q belongs to another session; read its output with read_transcript(transcript_ref="job:%s")`.

- [ ] **Step 1: Write the failing test**

```go
// TestJobStatusOnGrantedJobPointsAtReadTranscript: denied, names
// read_transcript and the job: ref, and discloses no transcript_ref of the
// delegate's session. A stranger's job_status keeps the original text.
func TestJobStatusOnGrantedJobPointsAtReadTranscript(t *testing.T)
```

The no-leak half asserts the error string does not contain the delegate's
`rec.TranscriptRef` and does not contain `local:`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test -count=1 -run TestJobStatusOnGrantedJobPointsAtReadTranscript ./`
Expected: FAIL — error is the plain `job "job_..." not found`.

- [ ] **Step 3: Implement**

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd agent && go test -count=1 -run 'TestJobStatus' ./`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/session_tools_jobs.go agent/session_tools_transcript_job_read_test.go
git commit -m "agent: job_status on a watch-granted job points at the sanctioned read"
```

---

### Task 6: gates and spec status line

**Files:**
- Modify: `docs/superpowers/specs/2026-07-31-observer-job-read-grant-design.md`
  (one status line, matching how the jobs-panel spec was treated)

- [ ] **Step 1: Full-workspace gate**

Run: `make lint`
Expected: PASS (7 modules; the only thing that catches a break in a sibling
module — `agent/` alone does not).

Run: `cd agent && go vet -tags serffuzz ./...`
Expected: PASS

- [ ] **Step 2: Add the spec status line**

Under the title: implemented on `wip/kata-eqs0`; note the ruling-5 phrasing
correction; note that `docs/job-control.md` and `test/scenarios/` are fd8n's.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-07-31-observer-job-read-grant-design.md
git commit -m "docs(eqs0): mark the observer read-grant spec implemented"
```

---

## Deliberately not done here

- **No revocation** (ruling 3). Grants stay append-only; `FoldGrants` stays
  order-insensitive.
- **No `transcript_ref` in the frame** (ruling 1, spec non-goal 4).
- **No `job_read_output` re-registration and no new tool** (ruling 1).
- **No ancestor `output_match`** (spec non-goal 1; the 2026-07-30 ruling
  stands). `watchSourceConcreteJob` resolution is untouched.
- **No hub or web-UI change** (spec non-goal 7). `LoadSessionObserverGrants`
  gains rows without a code change; ruling 5 keeps the self-observation row out.
- **No compensation for orphan grants** from a dropped or coalesced frame (spec
  "Failure modes"). Documented, not compensated.
- **`docs/job-control.md`, `docs/agentic-testing.md`, `test/scenarios/`** — all
  fd8n's, which is why it is blocked on this landing.
