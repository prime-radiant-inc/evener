# Job notification description labels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a completed job's one-line description in collapsed notifications instead of the generic job type.

**Architecture:** Thread the existing job record description through the backend notification block, parse it in the frontend notification classifier, and prefer it for the card's secondary label. Retain the current job-type fallback and all expanded-card behavior.

**Tech Stack:** Go notification rendering, TypeScript/React frontend, Vitest, Go tests.

## Global Constraints

- Preserve unrelated existing worktree edits.
- Use the existing structured `JobRecord.Description`; do not infer labels from output.
- Keep status titles, expanded metadata, raw disclosure, and transcript actions unchanged.

---

### Task 1: Carry description in backend notifications

**Files:**
- Modify: `agent/jobs.go` (`jobNotification`)
- Modify: `agent/job_notify.go` (`jobNotificationFromRecord`, `formatJobNotificationBlock`)
- Test: `agent/job_notify_test.go`

- [ ] Add a failing Go assertion that a delegate notification contains an escaped `description` attribute.
- [ ] Run the focused notification test and confirm it fails because the attribute is absent.
- [ ] Add `Description string` to `jobNotification`, copy `rec.Description` in `jobNotificationFromRecord`, and append an escaped `description` attribute when formatting the block.
- [ ] Run `go test ./agent -run 'Test.*Notification|Test.*notification'` and confirm it passes.
- [ ] Commit the backend change.

### Task 2: Prefer description in the frontend card

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts`

- [ ] Add failing parser/card tests for a delegate notification with `description="Inspect the workspace"`; assert the parsed notification and collapsed card show that text.
- [ ] Add a fallback test with no description; assert the secondary label remains `delegate`.
- [ ] Run the focused Vitest tests and confirm the new tests fail.
- [ ] Add optional `description` to `ParsedNotification`, parse and entity-decode the attribute, and choose `description || notificationSecondary(...)` when constructing the notification.
- [ ] Run the focused Vitest tests and confirm they pass.
- [ ] Commit the frontend change.

### Task 3: Full verification and integration

- [ ] Run the focused Go and frontend tests again from a clean implementation worktree.
- [ ] Run the repository-required frontend typecheck/build check if available and inspect all failures.
- [ ] Verify only the intended implementation, tests, and plan/spec files are committed on the worktree branch.
- [ ] Return to `webui-workspace-shell` and merge the implementation branch with a non-fast-forward merge.
- [ ] Verify the merged branch status and rerun the focused tests from the merged checkout.
