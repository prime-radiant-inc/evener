# Job Cancellation Presentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render confirmed parent cancellation as neutral while preserving stopped attention, genuine failures, and complete diagnostics.

**Architecture:** Keep the backend contract unchanged. Add one private job-notification analysis in `steeringClassify.ts`; parse authority and signed exit once, then reuse that result for job tone, summary, and typed metadata. Leave shared delegate/observer tone logic and live activity formatting unchanged.

**Tech Stack:** TypeScript 6, React 19, Vitest 4, Testing Library, Biome.

**Spec:** `docs/superpowers/specs/2026-09-01-job-cancellation-presentation-design.md`

## Global constraints

- Change only the two documentation files and these frontend files:
  - `cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`
  - `cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts`
  - `cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx`
- Do not change backend, protocol, generated, activity, CSS, or shared component files.
- Explicit `cancelled` is neutral; every explicit `stopped` is warning; failure remains error.
- Delegate, watch, concern, and historical observer-callback behavior must remain unchanged.
- Tests are deterministic and use existing Chai-compatible assertions.
- Never run `npm ci` in a shared worktree setup.

---

### Task 1: Bootstrap the isolated frontend worktree

**Files:**
- No tracked files.

**Interfaces:**
- Produces a valid shared `node_modules` symlink and verified frontend toolchain before any direct `npx` command.

- [ ] **Step 1: Confirm worktree identity and cleanliness**

```bash
git branch --show-current
git status --short
git rev-parse --show-toplevel
git rev-parse --path-format=absolute --git-common-dir
```

Expected: the implementation branch is active and the worktree contains only the two intended untracked documentation files copied from the reviewed draft.

- [ ] **Step 2: Link the repository's one real frontend install**

```bash
main_checkout="$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")"
shared_modules="$main_checkout/cmd/evener-hub/frontend/node_modules"
test -d "$shared_modules"
ln -sfn "$shared_modules" cmd/evener-hub/frontend/node_modules
```

Expected: `test -d` and `ln -sfn` exit zero. Do not stage the ignored symlink.

- [ ] **Step 3: Read target help and run preflight**

```bash
make help
make web-preflight
```

Expected: both commands exit zero. Stop if dependency versions or generated frontend prerequisites do not match.

---

### Task 2: Specify job-notification analysis behavior

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx`

**Interfaces:**
- Pins the private analyzer's externally visible result through `parseSteeringNotifications`.
- Preserves existing `ParsedNotification` and `NotificationCard` public interfaces.

- [ ] **Step 1: Add the exact cancellation and terminal matrix parser tests**

Use the existing `notificationsOf` and `notif` helpers in `steeringClassify.test.ts`. Add:

```ts
test("confirmed parent cancellation is neutral and keeps signed diagnostics", () => {
  const block = `<job-notification job_id="job_1" job_type="shell" status="cancelled" reason="stopped_by_parent" exit_code="-1" description="Run repository lint, vet, and test gates">
Job job_1 cancelled.
</job-notification>`;
  expect(notif(notificationsOf(parseSteeringNotifications(block)), 0)).toMatchObject({
    title: "Job cancelled",
    tone: "neutral",
    secondary: "Run repository lint, vet, and test gates",
    status: "cancelled",
    reason: "stopped_by_parent",
    exitCode: -1,
  });
});

test.each([
  ["stopped", "stopped_by_parent", "-1", "warning", "shell · stopped_by_parent"],
  ["stopped", "cancelled", "-1", "warning", "shell · cancelled"],
  ["stopped", "run_timeout", "-1", "warning", "shell · run_timeout"],
  ["failed", "killed_by_signal: terminated", "-1", "error", "shell · exit -1 · killed_by_signal: terminated"],
  ["completed", "exit_zero", "7", "error", "shell · exit 7 · exit_zero"],
  ["mystery", "", "7", "error", "shell · exit 7"],
] as const)("maps %s/%s/exit %s to %s", (status, reason, exit, tone, secondary) => {
  const block = `<job-notification job_id="job_matrix" job_type="shell" status="${status}" reason="${reason}" exit_code="${exit}">
Job job_matrix ${status}.
</job-notification>`;
  expect(notif(notificationsOf(parseSteeringNotifications(block)), 0)).toMatchObject({ tone, secondary });
});
```

Add focused cases for:

```ts
// Explicit failure remains error, but malformed exit is absent everywhere.
status="failed" reason="wait_failed" exit_code="7x"
// Unknown malformed exit is neutral.
status="mystery" exit_code="7x"
// Blank status must not mask a failed event.
status="   " event="failed" exit_code="0"
```

For the first case assert `{ tone: "error", secondary: "shell · wait_failed", exitCode: undefined }`. For the second assert `{ tone: "neutral", secondary: "shell", exitCode: undefined }`. For the third assert `tone: "error"`.

- [ ] **Step 2: Add authority and historical-reader regressions**

Add job-notification cases proving:

- absent outer status/event plus communicate `cancelled` and exit `-1` → neutral with no compact exit;
- absent outer status/event plus communicate `stopped` and exit `-1` → warning with no compact exit;
- explicit outer `cancelled` plus communicate `done` → neutral.

Use `job_type="delegate"` so the existing parser reads the communicate envelope. Assert the complete tone and secondary values.

Extend the existing observer-callback test with `tone: "warning"` for communicate `done`. Keep existing watch and concern warning assertions; add tone assertions only where absent.

- [ ] **Step 3: Add the component presentation regression with supported matchers**

In `NotificationCard.test.tsx`, use the existing `notif` factory:

```ts
test("confirmed cancellation recedes while expanded diagnostics retain physical exit", () => {
  render(
    <NotificationCard
      notification={notif({
        title: "Job cancelled",
        tone: "neutral",
        secondary: "Run repository lint, vet, and test gates",
        status: "cancelled",
        reason: "stopped_by_parent",
        exitCode: -1,
        rawText:
          '<job-notification status="cancelled" reason="stopped_by_parent" exit_code="-1">cancelled</job-notification>',
      })}
    />,
  );

  const row = screen.getByTestId("notification-card");
  expect(row.getAttribute("data-tone")).toBe("neutral");
  expect(row.textContent).toContain("Job cancelled");
  expect(row.textContent).toContain("Run repository lint, vet, and test gates");
  expect(row.textContent).not.toContain("exit -1");
  expect(row.textContent).not.toContain("stopped_by_parent");
  expect(screen.queryByText("error")).toBeNull();
  expect(screen.queryByText("warning")).toBeNull();

  fireEvent.click(row);
  expect(screen.getByTestId("notification-field-status").textContent).toContain("cancelled");
  expect(screen.getByTestId("notification-field-reason").textContent).toContain("stopped_by_parent");
  expect(screen.getByTestId("notification-field-exit").textContent).toContain("-1");
  expect(screen.getByTestId("notification-raw").textContent).toContain('exit_code="-1"');
});
```

- [ ] **Step 4: Run the focused tests and verify the behavioral red state**

```bash
cd cmd/evener-hub/frontend
npx vitest run \
  src/panes/session/transcript/messages/steeringClassify.test.ts \
  src/panes/session/transcript/messages/NotificationCard.test.tsx \
  --maxWorkers=1
```

Expected: parser cancellation/signed-exit assertions fail against current production behavior. The component regression may already pass because `NotificationCard` correctly renders the parsed model it receives; that is not the production failing boundary.

- [ ] **Step 5: Commit tests and reviewed documentation**

```bash
git add \
  docs/superpowers/specs/2026-09-01-job-cancellation-presentation-design.md \
  docs/superpowers/plans/2026-09-01-job-cancellation-presentation-plan.md \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx
git commit -m "test(web): specify job cancellation presentation"
```

---

### Task 3: Analyze each job notification once

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`

**Interfaces:**
- Produces private `JobDisposition`, `JobNotificationAnalysis`, and `analyzeJobNotification` symbols.
- Preserves exported types and functions.
- Leaves the existing `notificationTone` implementation and all non-job callers unchanged.

- [ ] **Step 1: Add exact signed-exit parsing and private analysis**

Add near the existing integer parser:

```ts
type JobDisposition = "success" | "failure" | "cancelled" | "stopped" | "unknown";

interface JobNotificationAnalysis {
  disposition: JobDisposition;
  exitCode?: number;
}

function optionalSignedInteger(raw: string | undefined): number | undefined {
  const text = (raw ?? "").trim();
  if (!/^-?\d+$/.test(text)) return undefined;
  const value = Number(text);
  return Number.isSafeInteger(value) ? value : undefined;
}

function analyzeJobNotification(
  attrs: Record<string, string>,
  communicate: CommunicateEnvelope | null,
): JobNotificationAnalysis {
  const outerStatus = (attrs.status ?? "").trim().toLowerCase();
  const outerEvent = (attrs.event ?? "").trim().toLowerCase();
  const communicateStatus = (communicate?.status ?? "").trim().toLowerCase();
  const status = outerStatus || outerEvent || communicateStatus;
  const exitCode = optionalSignedInteger(attrs.exit_code);

  let disposition: JobDisposition = "unknown";
  if (status === "failed" || status === "error" || status === "exhausted" || status.includes("fail")) {
    disposition = "failure";
  } else if (status === "cancelled") {
    disposition = "cancelled";
  } else if (status === "stopped") {
    disposition = "stopped";
  } else if (status === "completed" || status === "done") {
    disposition = exitCode !== undefined && exitCode !== 0 ? "failure" : "success";
  } else if (exitCode !== undefined && exitCode !== 0) {
    disposition = "failure";
  }

  return exitCode === undefined ? { disposition } : { disposition, exitCode };
}
```

Do not add `reason` to the analysis input or disposition rules.

- [ ] **Step 2: Add job-only tone mapping**

Add a private helper used only by `parseJobNotification`:

```ts
function jobNotificationTone(
  attrs: Record<string, string>,
  communicate: CommunicateEnvelope | null,
  analysis: JobNotificationAnalysis,
): NotificationTone {
  if (analysis.disposition === "failure") return "error";
  const event = (attrs.event ?? "").trim().toLowerCase();
  if (
    (communicate?.concerns.length ?? 0) > 0 ||
    analysis.disposition === "stopped" ||
    event === "watch_send" ||
    event === "watch"
  ) {
    return "warning";
  }
  if (analysis.disposition === "success") return "success";
  return "neutral";
}
```

Do not change shared `notificationTone`; delegate notifications and observer callbacks continue to use it.

- [ ] **Step 3: Make the compact summary consume the same analysis**

Change `notificationSecondary` to receive `analysis: JobNotificationAnalysis`. Append exit only from the parsed result:

```ts
if (analysis.disposition === "failure" && analysis.exitCode !== undefined && analysis.exitCode !== 0) {
  bits.push(`exit ${analysis.exitCode}`);
}
```

Keep the existing reason rule: reason appears only for error or warning tones.

- [ ] **Step 4: Wire one analysis through `parseJobNotification`**

After parsing communicate/description, compute once:

```ts
const analysis = analyzeJobNotification(attrs, communicate);
const tone = jobNotificationTone(attrs, communicate, analysis);
```

Use that same object for:

```ts
secondary: notificationSecondary(attrs, tone, description, analysis),
exitCode: analysis.exitCode,
```

Remove the old call to shared `notificationTone` from this parser only. Keep `optionalNonNegativeInteger(attrs, "output_bytes")` unchanged.

- [ ] **Step 5: Format all touched frontend files before final focused verification**

```bash
cd cmd/evener-hub/frontend
npx biome check --write \
  src/panes/session/transcript/messages/steeringClassify.ts \
  src/panes/session/transcript/messages/steeringClassify.test.ts \
  src/panes/session/transcript/messages/NotificationCard.test.tsx
```

Expected: exit zero. Inspect every rewrite.

- [ ] **Step 6: Run the focused green tests**

```bash
cd cmd/evener-hub/frontend
npx vitest run \
  src/panes/session/transcript/messages/steeringClassify.test.ts \
  src/panes/session/transcript/messages/NotificationCard.test.tsx \
  --maxWorkers=1
```

Expected: both files pass with zero failures.

- [ ] **Step 7: Commit the implementation**

```bash
git add \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx
git commit -m "fix(web): present confirmed cancellation as neutral"
```

---

### Task 4: Run canonical gates and audit scope

**Files:**
- Verify only the five tracked paths named in Global constraints.

**Interfaces:**
- Produces final evidence that focused behavior and repository gates pass without scope expansion.

- [ ] **Step 1: Run the canonical frontend gate**

```bash
make test-web
```

Expected: frontend unit tests, TypeScript, and Biome exit zero.

- [ ] **Step 2: Run repository gates**

```bash
make lint
make vet
make test
```

Expected: every command exits zero. Diagnose any failure; do not suppress or weaken a check.

- [ ] **Step 3: Audit the exact branch diff**

Capture the worktree's base commit before implementation as `BASE`. Then run:

```bash
git diff --check "$BASE"..HEAD
git diff --name-only "$BASE"..HEAD
git status --short
git diff "$BASE"..HEAD -- \
  docs/superpowers/specs/2026-09-01-job-cancellation-presentation-design.md \
  docs/superpowers/plans/2026-09-01-job-cancellation-presentation-plan.md \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/steeringClassify.test.ts \
  cmd/evener-hub/frontend/src/panes/session/transcript/messages/NotificationCard.test.tsx
```

Expected: no whitespace errors; only the five named paths changed; worktree clean; exact cancellation neutral; every stopped warning; failures error; signed exit preserved in expanded/raw diagnostics; observer/delegate/watch behavior unchanged.
