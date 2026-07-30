import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../../protocol/model";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities } from "../../../../protocol/types.gen";
import { registerPane } from "../../../../shell/paneRegistry";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { connectionStore } from "../../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../../stores/threads";
import { Toast } from "../../../../widgets";
import { readDraft } from "../../composer/draft";
import { ignoringTurn, itemRendererFor } from "../types";
import { UserMessageItem, UserMessageView } from "./UserMessageItem";
import styles from "./usermessageitem.module.css";

afterEach(cleanup);

// See shell/rail/Rail.test.tsx's identical comment: Node 26 shadows jsdom's
// real window.localStorage with its own (non-functional under vitest) global,
// so the fork-affordance tests below - which read the seeded composer draft
// through draft.ts - need this same small in-memory stand-in. Scoped to this
// file.
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "userMessage", text: "", ...overrides };
}

test('self-registers under the wire\'s user-message item type ("userMessage")', () => {
  expect(itemRendererFor("userMessage")).toBe(UserMessageItem);
});

test("is memoized ignoring turn identity - a fresh turn object on every streaming delta must not re-render an unrelated settled user message", () => {
  expect(UserMessageItem.$$typeof).toBe(Symbol.for("react.memo"));
  expect((UserMessageItem as unknown as { compare: unknown }).compare).toBe(ignoringTurn);
});

test("renders the slack-lean speaker header: avatar tile + 'You', no stacked eyebrow or gutter tag", () => {
  render(<UserMessageView item={item({ text: "hello world" })} />);
  const root = screen.getByTestId("user-message-item");
  // One flex row: avatar wrapper first, content column second.
  const avatar = root.children[0];
  expect(avatar?.className).toBe(styles.avatar);
  const content = root.children[1];
  expect(content?.className).toBe(styles.content);
  // The header is the content column's first line and names the speaker.
  const header = content?.firstElementChild;
  expect(header).not.toBeNull();
  expect(header!.textContent).toBe("You");
  expect(root.querySelector("[class*=tag]")).toBeNull();
  expect(root.querySelector("[class*=eyebrow]")).toBeNull();
});

test("the avatar tile is decorative (aria-hidden) - the header already names the speaker in words", () => {
  render(<UserMessageView item={item({ text: "hello" })} />);
  const avatar = screen.getByTestId("speaker-avatar");
  expect(avatar.getAttribute("aria-hidden")).toBe("true");
});

test("the clock time renders in the header when startedAt is present", () => {
  const startedAt = "2026-07-29T14:05:00.000Z";
  render(<UserMessageView item={item({ text: "hello", startedAt })} />);
  // Local HH:MM projection of the instant, matching formatClockTime.
  const d = new Date(startedAt);
  const expected = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  const root = screen.getByTestId("user-message-item");
  const header = root.querySelector(`.${styles.header}`) as HTMLElement;
  const time = header.querySelector(`.${styles.time}`);
  expect(time).not.toBeNull();
  expect(time!.textContent).toBe(expected);
});

test("no time node at all (no placeholder) when startedAt is absent", () => {
  render(<UserMessageView item={item({ text: "hello" })} />);
  const root = screen.getByTestId("user-message-item");
  const header = root.querySelector(`.${styles.header}`) as HTMLElement;
  expect(header.querySelector(`.${styles.time}`)).toBeNull();
  expect(header.textContent).toBe("You");
});

test("actions live in the speaker header row", () => {
  render(<UserMessageView item={item({ text: "hello" })} actions={<button type="button">act</button>} />);
  const root = screen.getByTestId("user-message-item");
  const header = root.querySelector(`.${styles.header}`) as HTMLElement;
  expect(header.contains(screen.getByRole("button", { name: "act" }))).toBe(true);
});

test("renders the prompt text", () => {
  render(<UserMessageItem item={item({ text: "hello there" })} turn={turn} live={false} />);
  expect(screen.getByText("hello there")).toBeTruthy();
});

test('carries a quiet "You" tag as a sibling of the text, not mixed into it', () => {
  const { container } = render(<UserMessageItem item={item({ text: "hi" })} turn={turn} live={false} />);
  expect(screen.getByText("You")).toBeTruthy();
  // The "You" tag and the prompt text are two separate nodes - proven by
  // being independently queryable by their own exact text (a mixed-in tag
  // would make "hi" only findable as part of a larger "You hi" string).
  expect(screen.getByText("hi")).toBeTruthy();
  expect(container.querySelector('[data-testid="user-message-item"]')).toBeTruthy();
});

test("the approved You row keeps identity, text, and attachments in one avatar + content-column hierarchy", () => {
  const { container } = render(
    <UserMessageView item={item({ text: "hi", images: [{ src: "data:image/png;base64,x" }] })} />,
  );
  const message = container.querySelector('[data-testid="user-message-item"]');
  expect(message?.children[0]?.className).toBe(styles.avatar);
  const content = message?.children[1];
  expect(content?.className).toBe(styles.content);
  expect(content?.children[0]?.textContent).toBe("You");
  const body = content?.children[1];
  expect(body?.className).toBe(styles.body);
  expect(body?.textContent).toContain("hi");
  expect(body?.querySelector('[data-testid="image-gallery-thumb"]')).toBeTruthy();
});

test("the slack-lean layout is token-backed and has no prose card treatment", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "usermessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  // One flex row: avatar (flex:none) + the transcript's single-sourced
  // --speaker-gap (10px, declared on .turn in turnblock.module.css), so the
  // content column lands on the same 34px line the TurnBlock gutter uses for
  // agent-side items. The 24+10=34 arithmetic itself is pinned by
  // speakeravatar.test.tsx's drift-pin test.
  expect(css).toMatch(/\.message\s*\{[\s\S]*display:\s*flex;[\s\S]*gap:\s*var\(--speaker-gap\);/);
  expect(css).toMatch(/\.avatar\s*\{[\s\S]*flex:\s*none;/);
  // The content column must SPAN the row (flex: 1 1 auto), matching the agent
  // side's .column: left shrink-to-fit, the bubble's max-width: 92% becomes a
  // cyclic percentage against a containing block sized by the bubble itself,
  // which resolves to roughly 92% of the text's own width and wraps the tail
  // words of every message wider than the header (2026-07-30 live-DOM
  // measurement: "commit and merge" clamped to 133px and wrapped in a 651px
  // row).
  expect(css).toMatch(/\.content\s*\{[\s\S]*flex:\s*1 1 auto;[\s\S]*min-width:\s*0;/);
  expect(css).toMatch(/\.header\s*\{[\s\S]*display:\s*flex;[\s\S]*align-items:\s*baseline;/);
  // Speaker name at body size / medium weight / --ink-hi (spec decision 1);
  // clock time at caption / --ink-low.
  expect(css).toMatch(
    /\.name\s*\{[\s\S]*font-size:\s*var\(--font-size-body\);[\s\S]*font-weight:\s*var\(--font-weight-medium\);[\s\S]*color:\s*var\(--ink-hi\);/,
  );
  expect(css).toMatch(/\.time\s*\{[\s\S]*font-size:\s*var\(--font-size-caption\);[\s\S]*color:\s*var\(--ink-low\);/);
  // Text at --ink-hi (spec decision 5 - the header now carries the
  // boundary-scannability the old --ink-mid demotion was buying).
  expect(css).toMatch(/\.text\s*\{[\s\S]*color:\s*var\(--ink-hi\);/);
  // Actions stay right-anchored with hover/focus-within reveal.
  expect(css).toMatch(/\.actions\s*\{[\s\S]*margin-left:\s*auto;/);
  expect(css).toMatch(/\.message:hover\s+\.actions/);
  expect(css).toMatch(/\.message:focus-within\s+\.actions/);
  // No breakpoint in this component - TurnBlock owns the gutter media query.
  expect(css).not.toMatch(/@media/);
  expect(css).not.toMatch(/\.message\s*\{[^}]*background\s*:/);
  expect(css).not.toMatch(/\.message\s*\{[^}]*border\s*:/);
});

// --- the chat bubble (2026-07-30-transcript-chat-bubbles-design.md) --------

test("the body renders as a bubble wrapping the text and attachments", () => {
  render(<UserMessageView item={item({ text: "hi", images: [{ src: "data:image/png;base64,x" }] })} />);
  const bubble = screen.getByTestId("user-bubble");
  expect(bubble.textContent).toContain("hi");
  expect(bubble.querySelector('[data-testid="image-gallery-thumb"]')).toBeTruthy();
});

test("the user bubble is an accent-wash token fill, hugging its content, tailed toward the avatar", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "usermessageitem.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const body = /\.body\s*\{([^}]*)\}/.exec(css);
  expect(body).not.toBeNull();
  expect(body![1]).toMatch(/background:\s*var\(--accent-bg\)/);
  expect(body![1]).toMatch(/width:\s*fit-content/);
  expect(body![1]).toMatch(/max-width:\s*92%/);
  expect(body![1]).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  // The 4px (control-radius) corner is the top-left one - toward the avatar.
  expect(body![1]).toMatch(
    /border-radius:\s*var\(--radius-control\) var\(--radius-pane\) var\(--radius-pane\) var\(--radius-pane\)/,
  );
});

test("no gallery thumbnails when the item carries no images", () => {
  render(<UserMessageItem item={item({ text: "no pictures here" })} turn={turn} live={false} />);
  expect(screen.queryAllByTestId("image-gallery-thumb")).toHaveLength(0);
});

test("a single image renders one gallery thumbnail", () => {
  render(
    <UserMessageItem
      item={item({ text: "look", images: [{ src: "data:image/png;base64,x" }] })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(1);
});

test("multiple images each render their own gallery thumbnail", () => {
  render(
    <UserMessageItem
      item={item({ text: "look", images: [{ src: "a" }, { src: "b" }, { src: "c" }] })}
      turn={turn}
      live={false}
    />,
  );
  expect(screen.getAllByTestId("image-gallery-thumb")).toHaveLength(3);
});

test("renders identically regardless of live/settled - the user's own words never stream", () => {
  const { container: liveContainer } = render(
    <UserMessageItem item={item({ text: "same either way" })} turn={turn} live={true} />,
  );
  const liveHtml = liveContainer.innerHTML;
  cleanup();
  const { container: settledContainer } = render(
    <UserMessageItem item={item({ text: "same either way" })} turn={turn} live={false} />,
  );
  expect(settledContainer.innerHTML).toBe(liveHtml);
});

test("UserMessageView is exported standalone for reuse by user-sourced steering", () => {
  render(<UserMessageView item={item({ text: "reused" })} />);
  expect(screen.getByText("reused")).toBeTruthy();
  expect(screen.getByText("You")).toBeTruthy();
});

// --- per-message fork affordance (ForkFromHereButton) ------------------------
//
// Fork used to be a session-chrome ⋯-menu item; it moved here, to a
// per-user-message affordance, because the specific message being forked from
// IS its context (a chrome menu had none - it guessed at "the last user
// message"). This is the fork flow's only home now, so its behavior is pinned
// here: it calls the SAME thread/fork RPC, but with deferInput:true (fork the
// child at this turn WITHOUT replaying it) and seeds the new session's
// composer draft with the wire's originalInput rather than opening an edit
// dialog first. openChildPane's success path is mirrored: open the new ref as
// its own pane, no toast.

const FORK_CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

// A minimal, test-only "session" pane registration - real registerPane/
// openPane machinery without pulling in the actual panes/session module
// (mirrors SessionActionsMenu.test.tsx's identical setup for the same reason:
// these tests only assert openPane was called correctly, never that a real
// SessionPane renders).
registerPane({
  id: "session",
  title: () => "test session",
  component: lazy(() => Promise.resolve({ default: () => null })),
});

function forkWireThread(overrides: Partial<Thread> = {}): Thread {
  return {
    id: "child_1",
    sessionId: "child_1",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    serf: { ref: "local/child_1", capabilities: FORK_CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

function connectForkClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

describe("per-message fork affordance", () => {
  beforeEach(() => {
    connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
    resetThreadsStoreForTests();
    resetWorkspaceStoreForTests();
    localStorage.clear();
  });

  test("a user message with a sessionRef renders a Fork-from-here button; a read-only one (no ref) does not", () => {
    const { rerender } = render(
      <UserMessageItem item={item({ text: "fix the bug" })} turn={turn} live={false} sessionRef="ref_a" />,
    );
    expect(screen.getByRole("button", { name: /fork from here/i })).toBeTruthy();

    // No sessionRef (the read-only "open beside" transcript pane): forking
    // needs a ref to call thread/fork with, so the action is withheld.
    rerender(<UserMessageItem item={item({ text: "fix the bug" })} turn={turn} live={false} />);
    expect(screen.queryByRole("button", { name: /fork from here/i })).toBeNull();
  });

  test("forking calls thread/fork with this turn + deferInput, seeds the child's composer draft, and opens it as a pane", async () => {
    const user = userEvent.setup();
    const fake = connectForkClient();
    let called: unknown;
    fake.on("thread/fork", (params) => {
      called = params;
      return { thread: forkWireThread(), originalInput: "fix the bug" };
    });

    render(<UserMessageItem item={item({ text: "fix the bug" })} turn={turn} live={false} sessionRef="ref_a" />);
    await user.click(screen.getByRole("button", { name: /fork from here/i }));

    await waitFor(() => expect(called).toEqual({ ref: "ref_a", sourceTurnId: "turn_1", deferInput: true }));
    // The child opens as its own pane...
    await waitFor(() =>
      expect(workspaceStore.getState().panes.find((p) => p.type === "session")?.params).toEqual({
        ref: "local/child_1",
      }),
    );
    // ...with the original text seeded into its composer draft (never auto-sent).
    expect(readDraft("local/child_1")).toBe("fix the bug");
  });

  test("a failed fork surfaces an error toast and opens no pane", async () => {
    const user = userEvent.setup();
    const fake = connectForkClient();
    fake.on("thread/fork", () => {
      throw new Error("fork boom");
    });

    render(
      <>
        <UserMessageItem item={item({ text: "fix the bug" })} turn={turn} live={false} sessionRef="ref_a" />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: /fork from here/i }));

    await screen.findByText(/fork boom/i);
    expect(workspaceStore.getState().panes).toHaveLength(0);
  });
});

// --- the exchange boundary --------------------------------------------------
// A "turn" in this codebase is one LLM round-trip, and a real session has many
// of them per thing the user actually asked: measured on a live transcript, 72
// of 74 turns did not open with a user message at all. So marking every turn
// boundary would draw dozens of lines through one continuous piece of agent
// work. The boundary a reader looks for is the EXCHANGE - where they last
// spoke - and a user message is what opens one.
//
// SteeringItem reuses UserMessageView verbatim for a user-sourced steer, which
// lands MID-turn and is an interjection inside the work rather than the start
// of new work. It must not carry the marker.

test("a user message marks itself as the start of an exchange", () => {
  render(<UserMessageView item={item({ text: "do the thing" })} />);
  expect(screen.getByTestId("user-message-item").getAttribute("data-opens-exchange")).toBe("true");
});

test("a user-sourced steer reuses the same view WITHOUT the exchange marker", () => {
  render(<UserMessageView item={item({ text: "actually, stop" })} opensExchange={false} />);
  expect(screen.getByTestId("user-message-item").getAttribute("data-opens-exchange")).toBeNull();
});
