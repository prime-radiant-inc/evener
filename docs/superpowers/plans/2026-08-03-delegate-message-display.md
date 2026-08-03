# Delegate Message Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the full message sent through `delegate_send` and any returned response in the Web UI's expanded tool row without changing its compact summary or delegate-row correlation.

**Architecture:** Keep the feature in the existing job-tool renderer. Extract the existing subagent-row update effect into a reusable hook, then add a dedicated `DelegateSendBody` that renders labeled plain-text message and response blocks. Read the canonical response from `item.raw.output` when available; fall back to removing a recognized final delegate-status footer from `item.output` for historical transcript items.

**Tech Stack:** React 19, TypeScript 6, Vitest 4, Testing Library, existing `ItemModel`, tool-renderer registry, and `CodeBlock` widget.

## Global Constraints

- Change only the Web UI renderer and its deterministic frontend tests; do not change agent behavior, tool schemas, transcript storage, or wire formats.
- Keep the collapsed summary `Messaged <delegate> · <delivery/status>` unchanged.
- Render message and response content as plain text, not Markdown.
- Preserve message and response line breaks through the existing `CodeBlock` widget.
- Omit empty Message and Response sections.
- Preserve the legacy `job_send_message` alias.
- Preserve existing subagent-row correlation and never create a row from `delegate_send`.
- Reuse existing transcript body styles; add no layout-specific CSS.
- Follow `docs/testing.md`: tests must be deterministic and must not depend on providers, credentials, network access, timing, or ambient machine state.

## File Structure

- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`: own delegate-send response extraction, the shared correlation hook, and the dedicated expanded body.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`: prove the message/response display, fallbacks, alias behavior, unchanged summary, and unchanged correlation.

No new source or stylesheet files are needed. The renderer and its tests already live together, and the feature introduces no reusable cross-tool presentation primitive.

---

### Task 1: Render Delegate Messages and Responses

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx:19-57,197-228`
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx:149-168,255-294`

**Interfaces:**
- Consumes: `ItemModel.argumentsJSON?: string`, `ItemModel.output?: string`, `ItemModel.raw?: unknown`, `ToolRenderProps`, `parseArgs()`, `str()`, `trailingBracketFooter()`, `resolveRowKey()`, `classifyJobStatus()`, `statusWordFromText()`, and `updateSubagentRowIfExists()`.
- Produces: internal `useCorrelateSubagentRow(props, resolveKey, resolveKind, resolvePreview): void`, internal `delegateSendResponse(item): string | undefined`, and internal React component `DelegateSendBody(props: ToolRenderProps)`.
- Preserves: the registered descriptor for both `delegate_send` and `job_send_message`, its `send` icon, its current summary text, and its correlation key/status semantics.

- [ ] **Step 1: Add failing expanded-body tests for the sent message and response**

Add these tests immediately after the existing `delegate_send` summary test in `jobTools.test.tsx`. Use the descriptor's real body, not a test-only component:

```tsx
function renderDelegateSendBody({
  toolName = "delegate_send",
  argumentsJSON = JSON.stringify({ to: "dlg_abc123", message: "Inspect the parser.\nReport exact findings." }),
  output = "Found two call sites.\nBoth need coverage.\n[delegate_id dlg_abc123 · delivered · completed]",
  raw,
}: {
  toolName?: "delegate_send" | "job_send_message";
  argumentsJSON?: string;
  output?: string;
  raw?: unknown;
} = {}) {
  const Body = toolRendererFor(toolName).body!;
  render(
    <Body
      item={item({
        toolName,
        argumentsJSON,
        output,
        raw,
      })}
      live={false}
    />,
  );
}

test("delegate_send: expanded body shows the complete sent message and response without repeated status metadata", () => {
  renderDelegateSendBody();

  expect(screen.getByText("Message")).toBeTruthy();
  expect(screen.getByTestId("delegate-send-message").textContent).toBe(
    "Inspect the parser.\nReport exact findings.",
  );
  expect(screen.getByText("Response")).toBeTruthy();
  expect(screen.getByTestId("delegate-send-response").textContent).toBe(
    "Found two call sites.\nBoth need coverage.",
  );
  expect(screen.queryByText(/delegate_id dlg_abc123 · delivered · completed/)).toBeNull();
});

test("delegate_send: canonical raw output preserves the delegate response when formatted output has trailing metadata", () => {
  renderDelegateSendBody({
    output:
      "Exact response\n[delegate_id dlg_abc123 · delivered · completed]\nstructured_result (valid=true): {\"ok\":true}",
    raw: { output: "Exact response", status: "completed", action: "delivered" },
  });

  expect(screen.getByTestId("delegate-send-response").textContent).toBe("Exact response");
  expect(screen.queryByText(/structured_result/)).toBeNull();
});
```

The first test proves historical/fallback parsing from the formatted output. The second proves that current items use the direct producer state, whose `output` field contains only the delegate's response.

- [ ] **Step 2: Run the focused tests and verify the new behavior fails**

Run:

```sh
cd cmd/serf-hub/frontend
npm test -- --run src/panes/session/transcript/tools/jobTools.test.tsx
```

Expected: FAIL because the current `CorrelatingBody` renders one raw output block and does not provide `delegate-send-message` or `delegate-send-response` elements.

- [ ] **Step 3: Add failing omission, fallback, and legacy-alias tests**

Append these tests to the same `delegate_send` section:

```tsx
test("delegate_send: footer-only and in-flight calls omit the Response section", () => {
  const { rerender } = render(
    (() => {
      const Body = toolRendererFor("delegate_send").body!;
      return (
        <Body
          item={item({
            toolName: "delegate_send",
            argumentsJSON: JSON.stringify({ to: "dlg_abc123", message: "status?" }),
            output: "[delegate_id dlg_abc123 · delivered · running]",
          })}
          live={false}
        />
      );
    })(),
  );
  expect(screen.queryByText("Response")).toBeNull();

  const Body = toolRendererFor("delegate_send").body!;
  rerender(
    <Body
      item={item({
        toolName: "delegate_send",
        argumentsJSON: JSON.stringify({ to: "dlg_abc123", message: "status?" }),
        output: "",
      })}
      live={true}
    />,
  );
  expect(screen.queryByText("Response")).toBeNull();
});

test("delegate_send: unrecognized output remains visible as the response", () => {
  renderDelegateSendBody({ output: "historical result without a recognized footer" });
  expect(screen.getByTestId("delegate-send-response").textContent).toBe(
    "historical result without a recognized footer",
  );
});

test("delegate_send: malformed or missing message arguments omit Message without hiding a response", () => {
  renderDelegateSendBody({ argumentsJSON: "not json", output: "reply from historical data" });
  expect(screen.queryByText("Message")).toBeNull();
  expect(screen.getByTestId("delegate-send-response").textContent).toBe("reply from historical data");
});

test("job_send_message: expanded body reads the legacy target shape and shows message and response", () => {
  renderDelegateSendBody({
    toolName: "job_send_message",
    argumentsJSON: JSON.stringify({ target: "dlg_legacy", message: "continue" }),
    output: "continuing\n[delegate_id dlg_legacy · delivered · running]",
  });

  expect(screen.getByTestId("delegate-send-message").textContent).toBe("continue");
  expect(screen.getByTestId("delegate-send-response").textContent).toBe("continuing");
});
```

If Testing Library reports the inline footer-only render as hard to read or lint rejects the inline IIFE, replace that test's setup with `renderDelegateSendBody(...)`, call `cleanup()`, then invoke `renderDelegateSendBody(...)` again for the in-flight case. Do not weaken the two assertions.

- [ ] **Step 4: Run the focused tests and verify all new cases fail for the intended reason**

Run:

```sh
cd cmd/serf-hub/frontend
npm test -- --run src/panes/session/transcript/tools/jobTools.test.tsx
```

Expected: FAIL only in the new expanded-body tests. Existing 21 tests must remain green.

- [ ] **Step 5: Extract the existing correlation effect into a hook**

Replace the effect inside `CorrelatingBody` with an internal hook. Keep its update rules unchanged:

```tsx
type CorrelationResolvers = {
  resolveKey: (item: ItemModel) => string;
  resolveKind: (item: ItemModel) => ReturnType<typeof classifyJobStatus> | undefined;
  resolvePreview: (item: ItemModel) => string;
};

function useCorrelateSubagentRow(
  { item, sessionRef }: Pick<ToolRenderProps, "item" | "sessionRef">,
  { resolveKey, resolveKind, resolvePreview }: CorrelationResolvers,
): void {
  useLayoutEffect(() => {
    const kind = resolveKind(item);
    if (kind === undefined) return;
    updateSubagentRowIfExists(turnScopeKey(sessionRef, item.turnId), resolveKey(item), {
      kind,
      resultPreview: resolvePreview(item),
      completedAt: item.completedAt,
    });
  });
}
```

Update `CorrelatingBody` to accept the same resolver fields, call `useCorrelateSubagentRow({ item, sessionRef }, { resolveKey, resolveKind, resolvePreview })`, and continue returning:

```tsx
return <HeadClippedOutputBody item={item} live={false} />;
```

Do not alter the `job_status` or `job_stop` descriptor callbacks.

- [ ] **Step 6: Implement response extraction and the dedicated body**

Import the existing plain-text widget:

```tsx
import { CodeBlock } from "../../../../widgets";
```

Add a narrow validator for the direct `delegate_send` producer state and a fallback that removes only a recognized final delegate footer:

```tsx
function delegateSendResponse(item: ItemModel): string | undefined {
  const raw = asJsonObject(item.raw);
  if (raw && typeof raw.output === "string") return raw.output === "" ? undefined : raw.output;

  const output = item.output ?? "";
  if (output === "") return undefined;
  const footer = trailingBracketFooter(output);
  if (!footer || !footer.startsWith("delegate_id ")) return output;

  const trimmed = output.trimEnd();
  const footerStart = trimmed.lastIndexOf("[");
  const response = trimmed.slice(0, footerStart).replace(/\n$/, "");
  return response.trim() === "" ? undefined : response;
}
```

Then add the dedicated body beside `delegateSendTarget`:

```tsx
function DelegateSendBody(props: ToolRenderProps) {
  const { item } = props;
  const message = str(parseArgs(item.argumentsJSON), "message");
  const response = delegateSendResponse(item);

  useCorrelateSubagentRow(props, {
    resolveKey: (item) =>
      resolveRowKey(delegateSendTarget(parseArgs(item.argumentsJSON)), undefined, item.callId ?? item.id),
    resolveKind: (item) => {
      const footer = trailingBracketFooter(item.output ?? "");
      return footer ? classifyJobStatus(statusWordFromText(footer)) : undefined;
    },
    resolvePreview: (item) => trailingBracketFooter(item.output ?? "") ?? "",
  });

  if (!message && !response) return null;
  return (
    <div data-testid="delegate-send-body">
      {message ? (
        <section>
          <strong>Message</strong>
          <div data-testid="delegate-send-message">
            <CodeBlock text={message} copyLabel="Copy message" />
          </div>
        </section>
      ) : null}
      {response ? (
        <section>
          <strong>Response</strong>
          <div data-testid="delegate-send-response">
            <CodeBlock text={response} copyLabel="Copy response" />
          </div>
        </section>
      ) : null}
    </div>
  );
}
```

Finally replace the `delegate_send` descriptor's current `body(props) { ... }` with:

```tsx
body: DelegateSendBody,
```

Do not change the descriptor's matcher, icon, or summary.

- [ ] **Step 7: Run focused tests and fix only implementation defects**

Run:

```sh
cd cmd/serf-hub/frontend
npm test -- --run src/panes/session/transcript/tools/jobTools.test.tsx
```

Expected: PASS with all existing and new tests. If `CodeBlock` contributes button text to a wrapper's `textContent`, query the widget's code/pre element inside each `data-testid` wrapper rather than loosening equality to substring matching.

- [ ] **Step 8: Strengthen the existing correlation test against presentation regressions**

Extend `delegate_send checking on a delegate (by delegate_id) updates its existing row` after its current `kind` assertion:

```tsx
expect(screen.getByTestId("delegate-send-message").textContent).toBe("status?");
expect(screen.getByTestId("delegate-send-response").textContent).toBe("on it");
```

Keep the existing `expect(screen.getByTestId("subagent-row").dataset.kind).toBe("done")` assertion. This proves that adding the body does not sacrifice correlation.

Also retain the existing summary test unchanged. It remains the regression test for compact-row copy.

- [ ] **Step 9: Run focused tests, typecheck, lint, and the full frontend suite**

Run each command separately so failures remain attributable:

```sh
cd cmd/serf-hub/frontend
npm test -- --run src/panes/session/transcript/tools/jobTools.test.tsx
npm run typecheck
npm run lint
npm test
```

Expected:

- focused `jobTools.test.tsx`: PASS;
- TypeScript: exit 0 with no errors;
- Biome: exit 0 with no diagnostics;
- full Vitest suite: all test files and tests PASS.

No browser layout guard is required because this plan adds no CSS. If implementation introduces CSS despite the plan, stop and revise the design rather than silently expanding scope.

- [ ] **Step 10: Review the diff and commit the implementation**

Run:

```sh
git diff --check
git diff -- cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx
git status --short
```

Confirm that only the two planned frontend files changed after the already committed design and plan documents. Then commit:

```sh
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx
git commit -m "feat(webui): show delegate messages and responses"
```

---

### Task 2: Verify the Delivered Behavior

**Files:**
- Verify: `docs/superpowers/specs/2026-08-03-delegate-message-display-design.md`
- Verify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`
- Verify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`

**Interfaces:**
- Consumes: the committed `DelegateSendBody` behavior and focused regression tests from Task 1.
- Produces: fresh verification evidence and a clean worktree ready for review.

- [ ] **Step 1: Run the acceptance checks from a clean process**

Run:

```sh
cd cmd/serf-hub/frontend
npm test -- --run src/panes/session/transcript/tools/jobTools.test.tsx
npm run typecheck
npm run lint
npm test
```

Expected: every command exits 0. Record the exact test-file and test counts from Vitest output.

- [ ] **Step 2: Inspect repository state and commit contents**

Run from the repository root:

```sh
git status --short
git log -3 --oneline
git show --stat --oneline HEAD
git diff HEAD^ --check
```

Expected: clean status; implementation commit at `HEAD`; only `jobTools.tsx` and `jobTools.test.tsx` in the implementation commit; no whitespace errors.

- [ ] **Step 3: Review acceptance criteria against test evidence**

Confirm each approved criterion has a direct assertion:

- complete sent message: `delegate-send-message` exact `textContent` assertion;
- response when present: `delegate-send-response` exact assertion;
- footer excluded: query for the footer returns null;
- absent response omitted: `Response` label query returns null for footer-only and in-flight cases;
- compact summary unchanged: existing exact summary assertion;
- correlation unchanged: existing row reaches `data-kind="done"` while new body text is present;
- malformed/historical data: malformed-arguments and unrecognized-output tests;
- legacy alias: `job_send_message` body test.

If any criterion lacks a direct assertion, add that assertion to `jobTools.test.tsx`, rerun all four commands from Task 2 Step 1, and create a new follow-up commit. Do not amend the implementation commit.
