# Crash Tombstone Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resume a saved local session after hub restart even when its dead daemon remains in the roster as an errored tombstone.

**Architecture:** Preserve crash tombstones as UI evidence, but exclude them from `hubThreadResume`'s under-lock live-daemon reuse decision. Prove the production state through the existing appwire turn-start integration test before changing production code.

**Tech Stack:** Go, appwire integration test server, `hubcore.Roster`, `hubcore.ResumeLocks`

## Global Constraints

- Keep errored roster entries available to tree and diagnostic readers.
- Preserve per-session resume serialization and reuse of genuinely live racing replacements.
- Do not add retry loops or frontend behavior.
- Use deterministic local test servers and process-independent roster fixtures.

---

### Task 1: Reject crash tombstones during locked resume

**Files:**
- Modify: `cmd/serf-hub/app_rpc_test.go:6621`
- Modify: `cmd/serf-hub/app_threadlifecycle.go:251`

**Interfaces:**
- Consumes: `hubcore.LiveEntry.Status`, `hubcore.NewResumeLocks()`, and `hubThreadResume`
- Produces: `hubThreadResume` behavior that calls `Spawner.Resume` when the matching roster entry has status `"errored"`

- [ ] **Step 1: Make the integration test model an errored roster entry**

Update `TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError` so its
roster contains this production-equivalent entry before the request:

```go
roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
	Entry: rendezvous.Entry{
		PID:       109,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  staleEndpoint,
		SourceID:  "local",
		ThreadID:  sessionID,
		SessionID: sessionID,
	},
	SessionID: sessionID,
	Status:    "errored",
})
```

Configure the test server with the shared lock registry:

```go
ResumeLocks: hubcore.NewResumeLocks(),
```

Keep the fake spawner's existing replacement rendezvous write and assertions so
the test exercises the real appwire handler, source resolution, resume, and
replacement turn start.

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./cmd/serf-hub -run '^TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError$' -count=1
```

Expected: FAIL from `TurnStart` with `local daemon unavailable` and a refused
connection to `staleEndpoint`; `resumeCalled` remains false.

- [ ] **Step 3: Implement the minimal live-entry predicate**

Change the under-lock double-check in `hubThreadResume` to reuse only a
non-errored entry:

```go
if le, ok := cfg.Roster.Find(sessionID); ok && le.Status != "errored" {
	return hubResumedThreadResponse(ctx, sources, le.SessionID, le.ThreadID)
}
```

Update the adjacent comment so it states that refresh can preserve errored
crash tombstones and those must fall through to spawning.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
go test ./cmd/serf-hub -run '^(TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError|TestHubRPCTurnStart)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run broader verification**

Run:

```bash
go test ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore
make build
git diff --check
```

Expected: all commands pass with no warnings or formatting changes outside the
two implementation files and these design/plan documents.

- [ ] **Step 6: Commit**

Stage only:

```bash
git add docs/superpowers/specs/2026-07-27-crash-tombstone-resume-design.md \
  docs/superpowers/plans/2026-07-27-crash-tombstone-resume.md \
  cmd/serf-hub/app_rpc_test.go \
  cmd/serf-hub/app_threadlifecycle.go
```

Commit with a detailed message describing the observed dead PID/endpoint, the
errored-tombstone/live-entry distinction, the RED test, and verification.
