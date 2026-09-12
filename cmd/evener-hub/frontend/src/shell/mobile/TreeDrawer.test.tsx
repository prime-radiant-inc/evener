import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID } from "../../stores/navigation/types";
import { registerPaneForTests } from "../paneRegistry";
import { resetWorkspaceStoreForTests } from "../workspace";
import { TreeDrawer } from "./TreeDrawer";

function needsYouNode(n: number) {
  return {
    ref: `local:s${n}`,
    host_id: "local",
    session_id: `s${n}`,
    title: `Needs you ${n}`,
    project: "proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
  };
}
function setNeedsYou(count: number): void {
  const key = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const rows = Array.from({ length: count }, (_, i) => needsYouNode(i));
  navigationStore.setState({
    mode: "v2",
    clientGenerationID: "generation_test",
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: "etag",
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
}

function DocFixture() {
  return <div>doc</div>;
}

// paneRegistry.ts is a shared module singleton - registerPaneForTests's
// restorer (called in afterAll below) puts back whatever "doc" resolved to
// before this file ran, so a later file sharing the same registry never
// inherits this fixture.
let restoreDocPane: () => void;

beforeAll(async () => {
  restoreDocPane = registerPaneForTests<{ ref: string }>({
    id: "doc",
    title: () => "Doc",
    component: lazy(() => Promise.resolve({ default: DocFixture })),
  });
  await import("../../panes/welcome/Welcome");
  await import("../../panes/welcome");
});

afterAll(() => {
  restoreDocPane();
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetNavigationStoreForTests();
});

afterEach(() => {
  cleanup();
});

test("renders a trigger button labeled Sessions", () => {
  render(<TreeDrawer onToggle={vi.fn()} />);
  expect(screen.getByRole("button", { name: "Sessions" })).toBeTruthy();
});

// UX fix: parity with RailHost's own hidden-rail ☰ chip, which already shows
// Badge(needsYou) - the mobile trigger carries the same overlay so attention
// is visible without opening the drawer first.
test("the trigger carries the same needs-you Badge overlay the desktop chip has", () => {
  setNeedsYou(2);
  render(<TreeDrawer onToggle={vi.fn()} />);
  expect(screen.getByText("2")).toBeTruthy();
});

test("no Badge overlay when nothing needs attention", () => {
  setNeedsYou(0);
  render(<TreeDrawer onToggle={vi.fn()} />);
  expect(screen.queryByText("0")).toBeNull();
});

// FIX 4 (real-browser report): the trigger's badge read
// attentionSummary.needsYou, a SEPARATE, more narrowly-scoped aggregate
// (hubcore's DeriveAttention: top-level, non-subagent, non-archived
// sessions only - attention.go's own tierEligible) than tree.needs_you (the
// server's own ordered needs-you list every OTHER needs-you surface in this
// app reads from - see needsYouCycle.ts's own header comment on why it is
// the one source of truth). A session that needs you but falls outside
// attentionSummary's narrower population (e.g. reached only via the tree's
// own needs_you list) left the drawer's rows showing "your move" while its
// trigger stayed unbadged. Keying the badge off tree.needs_you.length
// instead matches every other needs-you surface and can never disagree
// with what the drawer's own rows show once opened.
test("the trigger badge follows the bounded needs-you rows", () => {
  setNeedsYou(1);
  render(<TreeDrawer onToggle={vi.fn()} />);
  expect(screen.getByText("1")).toBeTruthy();
});

test("trigger calls onToggle instead of opening a Sheet", async () => {
  const onToggle = vi.fn();
  const user = userEvent.setup();
  render(<TreeDrawer onToggle={onToggle} />);
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  expect(onToggle).toHaveBeenCalled();
  // No dialog (Sheet) should be rendered by TreeDrawer anymore
  expect(screen.queryByRole("dialog")).toBeNull();
});
