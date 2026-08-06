# delegate_send Renderer Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `delegate_send` tool row read "Sent a message to delegate \<id\>" with an open-in-pane link, and render its expanded body as an outgoing chat message plus an incoming reply bubble.

**Architecture:** Pure frontend change in `cmd/serf-hub/frontend`. A new optional `openTranscriptRef` descriptor field (mirroring `openBesidePath`) lets `ToolCallItem` slot the existing `OpenTranscriptButton` into the row's `trailing`. The expanded body reuses `UserMessageView`, lightly generalized with `speaker`/`name`/`timeIso` props. The backend already ships `transcript_ref` in `delegate_send`'s raw state (`agent/session_tools_jobs.go`'s `delegateSendResult`) — no Go changes.

**Tech Stack:** React 19, TypeScript, Vitest + @testing-library/react, Biome.

**Spec:** `docs/superpowers/specs/2026-08-06-delegate-send-renderer-design.md`

## Global Constraints

- Work happens in the `delegate-send-renderer` worktree, branch `delegate-send-renderer`.
- Frontend test runner: `cd cmd/serf-hub/frontend && npx vitest run <file>` for a single file.
- Before each commit touching frontend files: `cd cmd/serf-hub/frontend && npx biome check --write <touched files>`.
- Avoid Biome `noNonNullAssertion` violations — no `!` postfix; use a local variable or type guard instead.
- Defaults on generalized components must preserve today's exact rendering for existing callers.
- Never render a dead affordance: no `transcript_ref` → no open button.

## File Structure

- `src/panes/session/transcript/messages/UserMessageItem.tsx` — `UserMessageView` gains optional `speaker`, `name`, `timeIso` props (defaults = current behavior).
- `src/panes/session/transcript/toolRenderers.ts` — `ToolRendererDescriptor` gains optional `openTranscriptRef?(item): string | undefined`.
- `src/panes/session/transcript/ToolCallItem.tsx` — reads `openTranscriptRef`, composes `OpenTranscriptButton` into ToolRow's `trailing`.
- `src/panes/session/transcript/tools/jobTools.tsx` — `delegate_send` descriptor: new summary wording, `openTranscriptRef` implementation, chat-bubble body, local `CopyTextButton`.
- Tests: `messages/UserMessageItem.test.tsx`, `toolRowGrammar.test.tsx`, `tools/jobTools.test.tsx`.

All paths below are relative to `cmd/serf-hub/frontend/`.

---

### Task 1: Generalize UserMessageView with speaker/name/timeIso props

**Files:**
- Modify: `src/panes/session/transcript/messages/UserMessageItem.tsx` (the `UserMessageView` function, lines 99-134)
- Test: `src/panes/session/transcript/messages/UserMessageItem.test.tsx`

**Interfaces:**
- Consumes: existing `SpeakerAvatar` (`src/widgets/speakeravatar/index.tsx`, `SpeakerAvatarSpeaker = "user" | "agent"`), `formatClockTime(iso: string | undefined): string | undefined` from `./format`.
- Produces: `UserMessageView({ item, actions, opensExchange, speaker, name, timeIso })` — `speaker?: SpeakerAvatarSpeaker` (default `"user"`), `name?: string` (default `"You"`), `timeIso?: string` (default `item.startedAt`). Task 4 renders it with `speaker="agent"`, a custom `name`, and an explicit `timeIso`.

- [ ] **Step 1: Write the failing test**

Append to `UserMessageItem.test.tsx` (its `item()` factory already exists, line 51):

```tsx
test("UserMessageView accepts speaker/name/timeIso overrides for non-user speakers (delegate_send bubbles)", () => {
  render(
    <UserMessageView
      item={item({ text: "status?", startedAt: "2026-08-06T10:05:00Z" })}
      speaker="agent"
      name="Agent → dlg_abc123"
      timeIso="2026-08-06T10:05:00Z"
    />,
  );
  expect(screen.getByText("Agent → dlg_abc123")).toBeTruthy();
  expect(screen.getByTestId("user-bubble").textContent).toBe("status?");
  // The header time comes from timeIso, formatted by the same formatClockTime
  // the default path uses - compute the expectation rather than hardcoding a
  // timezone-dependent literal.
  const expected = formatClockTime("2026-08-06T10:05:00Z");
  if (expected !== undefined) expect(screen.getByText(expected)).toBeTruthy();
});

test("UserMessageView defaults are unchanged: user speaker, 'You' name, item.startedAt time", () => {
  render(<UserMessageView item={item({ text: "hi" })} />);
  expect(screen.getByText("You")).toBeTruthy();
});
```

Add `formatClockTime` to the imports: `import { formatClockTime } from "./format";`

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/messages/UserMessageItem.test.tsx`
Expected: FAIL — TS error or runtime error, `speaker`/`name`/`timeIso` are not props of `UserMessageView`.

- [ ] **Step 3: Implement the props**

In `UserMessageItem.tsx`, change the import of SpeakerAvatar to also import the type:

```tsx
import { SpeakerAvatar, type SpeakerAvatarSpeaker } from "../../../../widgets/speakeravatar";
```

Replace the whole `UserMessageView` function (lines 99-134) with:

```tsx
export function UserMessageView({
  item,
  actions,
  opensExchange = true,
  speaker = "user",
  name = "You",
  timeIso,
}: {
  item: ItemModel;
  actions?: ReactNode;
  opensExchange?: boolean;
  // speaker/name/timeIso generalize the view beyond the human user's own
  // messages (delegate_send renders the agent's outgoing message and the
  // delegate's reply through this same slack-lean structure). Defaults
  // preserve the exact rendering a real userMessage gets.
  speaker?: SpeakerAvatarSpeaker;
  name?: string;
  timeIso?: string;
}) {
  // No placeholder when the wire carries no startedAt: a header with no time
  // shows no time rather than a guess (formatClockTime returns undefined for
  // a missing or unparseable timestamp).
  const time = formatClockTime(timeIso ?? item.startedAt);
  return (
    <div
      className={CLASS.message}
      data-testid="user-message-item"
      data-opens-exchange={opensExchange ? "true" : undefined}
    >
      <span className={CLASS.avatar}>
        <SpeakerAvatar speaker={speaker} />
      </span>
      <div className={CLASS.content}>
        <div className={CLASS.header}>
          <span className={CLASS.name}>{name}</span>
          {time !== undefined && <span className={CLASS.time}>{time}</span>}
          {actions !== undefined && <div className={CLASS.actions}>{actions}</div>}
        </div>
        <div className={CLASS.body} data-testid="user-bubble">
          <div className={CLASS.text}>{item.text}</div>
          <ImageGallery images={item.images} />
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/messages/UserMessageItem.test.tsx`
Expected: PASS (whole file — the new tests plus every pre-existing one, proving defaults are unchanged).

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/UserMessageItem.test.tsx
git add src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/UserMessageItem.test.tsx
git commit -m "feat(web): generalize UserMessageView with speaker/name/timeIso props"
```

---

### Task 2: `openTranscriptRef` descriptor field + ToolCallItem threading

**Files:**
- Modify: `src/panes/session/transcript/toolRenderers.ts` (add field after `openBesideInline`, i.e. after line 83)
- Modify: `src/panes/session/transcript/ToolCallItem.tsx` (compose trailing control; two `trailing={openBesideButton}` sites, lines 236 and 275)
- Test: `src/panes/session/transcript/toolRowGrammar.test.tsx`

**Interfaces:**
- Consumes: `OpenTranscriptButton` from `./openTranscript` (existing; props `{ transcriptRef: string; parentRef?: string; label?: string }`), `resetWorkspaceStoreForTests`/`workspaceStore` from `../../../shell/workspace`.
- Produces: `ToolRendererDescriptor.openTranscriptRef?(item: ItemModel): string | undefined`. Task 3 implements it on the `delegate_send` descriptor.

- [ ] **Step 1: Write the failing test**

Append to `toolRowGrammar.test.tsx` (its `item()` factory and `turn` constant already exist, lines 26-39; `fireEvent` is already imported):

```tsx
// Mirrors the summaryLink threading test above: the descriptor declares DATA,
// ToolCallItem owns the control - a wiring bug (ref read but never rendered,
// or rendered without the parent ref) must fail here, not in production.
test("ToolCallItem threads a descriptor's openTranscriptRef to a working OpenTranscriptButton", async () => {
  await import("../"); // pane registrations, same harness as openTranscript.test.tsx's beforeAll
  resetWorkspaceStoreForTests();
  registerToolRenderer({
    match: "trg_opentranscript",
    summary: () => "Sent a message to delegate dlg_x",
    openTranscriptRef: () => "local:child",
    body: () => <div>b</div>,
  });
  render(<ToolCallItem item={item({ toolName: "trg_opentranscript" })} turn={turn} live={false} sessionRef="local:owner" />);
  fireEvent.click(screen.getByRole("button", { name: "Open transcript" }));
  const panes = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === "local:child");
  expect(panes).toHaveLength(1);
  expect(panes[0]?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
});

test("ToolCallItem renders no transcript button when the descriptor has no openTranscriptRef", () => {
  registerToolRenderer({
    match: "trg_no_opentranscript",
    summary: () => "plain",
    body: () => <div>b</div>,
  });
  render(<ToolCallItem item={item({ toolName: "trg_no_opentranscript" })} turn={turn} live={false} />);
  expect(screen.queryByRole("button", { name: "Open transcript" })).toBeNull();
});
```

Add to the imports at the top of the file:

```tsx
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx`
Expected: FAIL — `openTranscriptRef` is not a known descriptor property (TS error), and no "Open transcript" button renders.

- [ ] **Step 3: Implement the field and threading**

In `toolRenderers.ts`, insert after the `openBesideInline` block (after line 83):

```ts
  // openTranscriptRef returns the transcript ref of the child session this
  // tool call targets, or undefined when it targets none - the one case today
  // is delegate_send, whose raw state carries the messaged delegate's
  // transcript_ref. ToolCallItem turns a non-undefined ref into an
  // OpenTranscriptButton in the row's trailing slot (the same control the
  // subagent module rows use). A data field, not a ReactNode, for the same
  // reason as openBesidePath: the descriptor declares WHAT it targets,
  // ToolCallItem owns the control that opens it.
  openTranscriptRef?(item: ItemModel): string | undefined;
```

In `ToolCallItem.tsx`, add the import:

```tsx
import { OpenTranscriptButton } from "./openTranscript";
```

After the existing `openBesideButton`/`trailingAfter` block (after line 120), add:

```tsx
  // A child-targeting tool (delegate_send today) exposes its target's
  // transcript ref via descriptor.openTranscriptRef; ToolCallItem turns that
  // into the same "open ⤢" control the subagent module rows use, riding the
  // row's trailing slot beside any file open-beside button. parentRef is the
  // enclosing session so the opened pane keeps its way back (kata 0pzz).
  const openTranscriptRef = descriptor.openTranscriptRef?.(item);
  const openTranscriptButton =
    openTranscriptRef !== undefined ? (
      <OpenTranscriptButton transcriptRef={openTranscriptRef} parentRef={sessionRef} />
    ) : null;
  const trailingControls =
    openBesideButton !== null || openTranscriptButton !== null ? (
      <>
        {openBesideButton}
        {openTranscriptButton}
      </>
    ) : null;
```

Replace both `trailing={openBesideButton}` occurrences (lines 236 and 275) with `trailing={trailingControls}`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/toolRowGrammar.test.tsx`
Expected: PASS (whole file).

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/transcript/toolRenderers.ts src/panes/session/transcript/ToolCallItem.tsx src/panes/session/transcript/toolRowGrammar.test.tsx
git add src/panes/session/transcript/toolRenderers.ts src/panes/session/transcript/ToolCallItem.tsx src/panes/session/transcript/toolRowGrammar.test.tsx
git commit -m "feat(web): openTranscriptRef descriptor field renders an OpenTranscriptButton in the tool row"
```

---

### Task 3: delegate_send summary wording + openTranscriptRef implementation

**Files:**
- Modify: `src/panes/session/transcript/tools/jobTools.tsx` (the `delegate_send` descriptor, lines 362-372; `DelegateSendRawState`/`delegateSendResult`, lines 217-232)
- Test: `src/panes/session/transcript/tools/jobTools.test.tsx`

**Interfaces:**
- Consumes: Task 2's `openTranscriptRef` descriptor field; `statusWordFromText` (already imported in jobTools.tsx from `./subagentModule`); existing `clip`, `delegateSendFooter`, `delegateSendTarget`, `ID_CLIP`.
- Produces: summary format `Sent a message to delegate <clipped-id>[ · <status>]`; `openTranscriptRef` reading `item.raw.transcript_ref`.

- [ ] **Step 1: Update the failing tests**

In `jobTools.test.tsx`, replace the test at lines 151-158 with:

```tsx
test("delegate_send: summary names the target delegate and a one-word status, not the raw footer", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  const output = "on it\n[delegate_id dlg_abc123 · delivered · running]";
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: args, output }))).toBe(
    "Sent a message to delegate dlg_abc123 · running",
  );
});

test("delegate_send: summary omits the status segment when there is no footer yet (in flight)", () => {
  const d = toolRendererFor("delegate_send");
  const args = JSON.stringify({ to: "dlg_abc123", message: "status?" });
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: args, output: "" }))).toBe(
    "Sent a message to delegate dlg_abc123",
  );
});

test("delegate_send: summary degrades gracefully with no target arg", () => {
  const d = toolRendererFor("delegate_send");
  expect(d.summary(item({ toolName: "delegate_send", argumentsJSON: "{}", output: "" }))).toBe(
    "Sent a message to a delegate",
  );
});

test("delegate_send: openTranscriptRef reads transcript_ref from valid raw state", () => {
  const d = toolRendererFor("delegate_send");
  const it = item({
    toolName: "delegate_send",
    raw: { action: "steered", running_in_background: true, transcript_ref: "local:child1" },
  });
  expect(d.openTranscriptRef?.(it)).toBe("local:child1");
});

test("delegate_send: openTranscriptRef is undefined for absent, malformed, or blank-ref raw state", () => {
  const d = toolRendererFor("delegate_send");
  expect(d.openTranscriptRef?.(item({ toolName: "delegate_send" }))).toBeUndefined();
  // Missing running_in_background: not a valid delegateSendResult at all.
  expect(
    d.openTranscriptRef?.(item({ toolName: "delegate_send", raw: { action: "steered", transcript_ref: "local:c" } })),
  ).toBeUndefined();
  expect(
    d.openTranscriptRef?.(
      item({ toolName: "delegate_send", raw: { action: "steered", running_in_background: true, transcript_ref: "  " } }),
    ),
  ).toBeUndefined();
});
```

Update the alias test at lines 296-304: change the expected summary from `"Messaged dlg_legacy"` to `"Sent a message to delegate dlg_legacy"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx`
Expected: FAIL — summary still reads "Messaged …", `openTranscriptRef` unknown.

- [ ] **Step 3: Implement**

In `jobTools.tsx`, extend `DelegateSendRawState` and its guard (lines 217-232):

```ts
type DelegateSendRawState = {
  action: string;
  running_in_background: boolean;
  output?: string;
  transcript_ref?: string;
};

function delegateSendResult(raw: unknown): raw is DelegateSendRawState {
  const state = asJsonObject(raw);
  return (
    state !== undefined &&
    typeof state.action === "string" &&
    state.action.trim() !== "" &&
    typeof state.running_in_background === "boolean" &&
    (state.output === undefined || typeof state.output === "string") &&
    (state.transcript_ref === undefined || typeof state.transcript_ref === "string")
  );
}
```

Add two functions after `delegateSendResponse` (after line 322):

```ts
// The target's transcript ref rides the tool call's raw state
// (agent/session_tools_jobs.go's delegateSendResult.TranscriptRef), so the
// collapsed row can offer the same open-in-pane link the subagent module rows
// have. Runtime-message results and pre-field transcripts carry no ref - no
// button, never a dead link.
function delegateSendTranscriptRef(item: ItemModel): string | undefined {
  if (!delegateSendResult(item.raw)) return undefined;
  const ref = item.raw.transcript_ref;
  return ref !== undefined && ref.trim() !== "" ? ref : undefined;
}

// The collapsed summary names the target and, once the call settles, one
// status word recovered from the footer's own text (statusWordFromText -
// field order/presence in the footer is not fixed). The footer's remaining
// metadata (delegate_id echo, started_job_id, "running in background") is
// noise on a one-line summary and stays out of it.
function delegateSendSummary(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  const target = clip(delegateSendTarget(args), ID_CLIP);
  const base = target === "" ? "Sent a message to a delegate" : `Sent a message to delegate ${target}`;
  const footer = delegateSendFooter(item.output ?? "");
  const status = footer ? statusWordFromText(footer.text) : undefined;
  return status ? `${base} · ${status}` : base;
}
```

Replace the descriptor registration (lines 362-372):

```tsx
registerToolRenderer({
  match: (name) => name === "delegate_send" || name === "job_send_message",
  icon: "send",
  summary: delegateSendSummary,
  openTranscriptRef: delegateSendTranscriptRef,
  body: DelegateSendBody,
});
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx`
Expected: PASS. NOTE: the pre-existing body tests (lines 185-304) still pass at this point because the body is not yet rewritten — if any fail, it is because they asserted the old summary; fix only summary-related expectations here, leave body assertions for Task 4.

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx
git add src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx
git commit -m "feat(web): delegate_send summary names the target delegate and links its transcript"
```

---

### Task 4: delegate_send body as chat bubbles

**Files:**
- Modify: `src/panes/session/transcript/tools/jobTools.tsx` (rewrite `DelegateSendBody`, lines 324-360; add `CopyTextButton`; update imports)
- Test: `src/panes/session/transcript/tools/jobTools.test.tsx` (rewrite the body tests, lines 185-294)

**Interfaces:**
- Consumes: Task 1's generalized `UserMessageView` (`../messages/UserMessageItem`); `IconButton` from `../../../../widgets`; existing `delegateSendResponse`, `delegateSendTarget`, `clip`, `ID_CLIP`, `useCorrelateSubagentRow` (unchanged).
- Produces: body testids `delegate-send-body`, `delegate-send-message`, `delegate-send-response` (unchanged contract) — each message/response section wraps a `UserMessageView` whose bubble carries `data-testid="user-bubble"`.

- [ ] **Step 1: Rewrite the failing tests**

In `jobTools.test.tsx`, add `within` to the `@testing-library/react` import (line 1):

```tsx
import { cleanup, render, screen, within } from "@testing-library/react";
```

Replace the test at lines 185-193 with:

```tsx
test("delegate_send: expanded body renders the sent message as an outgoing chat bubble and the reply as an incoming one", () => {
  renderDelegateSendBody();

  const outgoing = screen.getByTestId("delegate-send-message");
  expect(within(outgoing).getByText("Agent → dlg_abc123")).toBeTruthy();
  expect(within(outgoing).getByTestId("speaker-avatar")).toBeTruthy();
  expect(within(outgoing).getByTestId("user-bubble").textContent).toBe("Inspect the parser.\nReport exact findings.");
  expect(within(outgoing).getByRole("button", { name: "Copy message" })).toBeTruthy();

  const reply = screen.getByTestId("delegate-send-response");
  expect(within(reply).getByText("dlg_abc123 (delegate)")).toBeTruthy();
  expect(within(reply).getByTestId("user-bubble").textContent).toBe("Found two call sites.\nBoth need coverage.");
  expect(within(reply).getByRole("button", { name: "Copy response" })).toBeTruthy();

  expect(screen.queryByText(/delegate_id dlg_abc123 · delivered · completed/)).toBeNull();
});
```

In the remaining body tests (lines 195-294), change every response/message text assertion from the section wrapper to the bubble inside it. Concretely:

- Every `expect(screen.getByTestId("delegate-send-response").textContent).toBe(X)` becomes
  `expect(within(screen.getByTestId("delegate-send-response")).getByTestId("user-bubble").textContent).toBe(X)`.
- The same for `"delegate-send-message"` (the job_send_message test at lines 285-294 has one of each).
- The test at lines 234-260: replace both `expect(screen.queryByText("Response")).toBeNull()` with `expect(screen.queryByTestId("delegate-send-response")).toBeNull()`.
- The test at lines 279-283: replace `expect(screen.queryByText("Message")).toBeNull()` with `expect(screen.queryByTestId("delegate-send-message")).toBeNull()` and fix its response assertion per the rule above.
- The test at lines 224-232 already uses `queryByTestId("delegate-send-response")` — keep it, and drop its `queryByText("Response")` line if present (it asserts the removed label).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx`
Expected: FAIL — no `user-bubble` inside the sections, no "Agent → …" header, no copy buttons.

- [ ] **Step 3: Rewrite DelegateSendBody**

In `jobTools.tsx`:

Update the React import (line 18) to `import { useEffect, useLayoutEffect, useState } from "react";`.

Update the widgets import (line 20): remove `CodeBlock`, add `IconButton` — `import { IconButton } from "../../../../widgets";`. (Verify `CodeBlock` has no other use in the file first; `HeadClippedOutputBody` comes from `./bodies` and stays.)

Add the import:

```tsx
import { UserMessageView } from "../messages/UserMessageItem";
```

Add the copy control after the `asJsonObject` helper (it mirrors CodeBlock's own copy idiom, extracted because the bubbles are not code blocks):

```tsx
const COPIED_RESET_MS = 2_000;

function CopyGlyph() {
  return (
    <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden="true">
      <rect x="4.5" y="1.5" width="8" height="8" rx="1.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
      <path d="M9.5 12.5H3A1.5 1.5 0 0 1 1.5 11V4.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  );
}

function CopiedGlyph() {
  return (
    <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden="true">
      <path d="M2 7.5 L5.5 11 L12 3.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}

// CopyTextButton is the CodeBlock copy control's idiom (clipboard guard,
// "Copied" feedback with a timed reset) as a standalone header action: the
// chat bubbles carry prose, not a code block, so the affordance moves into
// the bubble header's actions slot.
function CopyTextButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), COPIED_RESET_MS);
    return () => clearTimeout(timer);
  }, [copied]);
  return (
    <IconButton
      label={copied ? "Copied" : label}
      icon={copied ? <CopiedGlyph /> : <CopyGlyph />}
      variant="quiet"
      size="xs"
      onClick={() => {
        // Clipboard access requires a secure context and isn't implemented by
        // every test/embed environment - degrade to a no-op rather than throw.
        if (!navigator.clipboard?.writeText) return;
        void navigator.clipboard.writeText(text).then(() => setCopied(true));
      }}
    />
  );
}
```

Replace `DelegateSendBody` (lines 324-360) with:

```tsx
// DelegateSendBody renders the exchange as a two-party conversation through
// the transcript's own slack-lean message view: the sent message as an
// outgoing bubble from the agent to the delegate, and - when the call waited
// for one - the delegate's reply as an incoming bubble below it. The
// section testids (delegate-send-message/-response) are the longstanding
// contract of this body and are unchanged.
function DelegateSendBody(props: ToolRenderProps) {
  const { item } = props;
  const args = parseArgs(item.argumentsJSON);
  const message = str(args, "message");
  const response = delegateSendResponse(item);
  const target = clip(delegateSendTarget(args), ID_CLIP);

  useCorrelateSubagentRow(props, {
    resolveKey: (item) =>
      resolveRowKey(delegateSendTarget(parseArgs(item.argumentsJSON)), undefined, item.callId ?? item.id),
    resolveKind: (item) => {
      const footer = delegateSendFooter(item.output ?? "");
      return footer ? classifyJobStatus(statusWordFromText(footer.text)) : undefined;
    },
    resolvePreview: (item) => delegateSendFooter(item.output ?? "")?.text ?? "",
  });

  if (!message && !response) return null;
  return (
    <div data-testid="delegate-send-body">
      {message ? (
        <section data-testid="delegate-send-message">
          <UserMessageView
            item={{ ...item, text: message }}
            speaker="agent"
            name={target === "" ? "Agent → delegate" : `Agent → ${target}`}
            timeIso={item.startedAt}
            opensExchange={false}
            actions={<CopyTextButton text={message} label="Copy message" />}
          />
        </section>
      ) : null}
      {response ? (
        <section data-testid="delegate-send-response">
          <UserMessageView
            item={{ ...item, text: response }}
            speaker="agent"
            name={target === "" ? "Delegate" : `${target} (delegate)`}
            timeIso={item.completedAt ?? item.startedAt}
            opensExchange={false}
            actions={<CopyTextButton text={response} label="Copy response" />}
          />
        </section>
      ) : null}
    </div>
  );
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/jobTools.test.tsx`
Expected: PASS (whole file, including the row-correlation tests at the end).

- [ ] **Step 5: Biome + commit**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx
git add src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx
git commit -m "feat(web): render delegate_send body as an outgoing chat message and reply bubble"
```

---

### Task 5: Full gates

**Files:** none (verification only).

- [ ] **Step 1: Biome over every touched file**

```bash
cd cmd/serf-hub/frontend
npx biome check src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/UserMessageItem.test.tsx src/panes/session/transcript/toolRenderers.ts src/panes/session/transcript/ToolCallItem.tsx src/panes/session/transcript/toolRowGrammar.test.tsx src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx
```
Expected: no errors, no diffs needed.

- [ ] **Step 2: Canonical frontend gate**

Run from the worktree repo root: `make test-web`
Expected: PASS (unit, typecheck, Biome).

- [ ] **Step 3: Browser gate**

Run from the worktree repo root: `make test-web-browser`
Expected: PASS. While it runs, this is also the visual check: if a dev-server harness renders the transcript, confirm the delegate_send card shows the new summary, the open ⤢ button, and the two bubbles. (If the harness cannot mount a delegate_send fixture, rely on the unit tests and say so in the final report.)

- [ ] **Step 4: Final commit (only if a gate forced a fix)**

```bash
git add -p   # stage only files this branch changed
git commit -m "fix(web): gate fixes for delegate_send renderer"
```
