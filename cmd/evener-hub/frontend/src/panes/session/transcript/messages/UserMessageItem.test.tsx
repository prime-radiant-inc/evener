import type { ItemModel, Thread, TurnModel } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { lazy } from "react";
import { afterAll, beforeAll, beforeEach, describe, expect, test } from "vitest";
import { registerPaneForTests } from "../../../../shell/paneRegistry";
import { documentPaneState, resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { resetThreadsStoreForTests } from "../../../../stores/threads";
import { readModuleCss, topRuleBlock } from "../../../../styles/cssBlock";
import { ALL_CAPABILITIES as FORK_CAPABILITIES } from "../../../../threadTestUtils";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import "../../../doc";
import { Toast } from "../../../../widgets";
import { hoverForTooltip } from "../../../../widgets/tooltip/tooltipTestUtils";
import { readDraft } from "../../composer/draft";
import { SessionNowContext } from "../../liveness";
import { ignoringTurn, itemRendererFor } from "../types";
import speakerStyles from "./speakerheader.module.css";
import { UserMessageItem, UserMessageView } from "./UserMessageItem";
import styles from "./usermessageitem.module.css";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
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
  expect(root.dataset.speaker).toBe("user");
  // The header holds the speaker's name and nothing else here.
  expect(header!.children).toHaveLength(1);
  expect(root.querySelector("[class*=tag]")).toBeNull();
  expect(root.querySelector("[class*=eyebrow]")).toBeNull();
});

test("the avatar tile is decorative (aria-hidden) - the header already names the speaker in words", () => {
  render(<UserMessageView item={item({ text: "hello" })} />);
  const avatar = screen.getByTestId("speaker-avatar");
  expect(avatar.getAttribute("aria-hidden")).toBe("true");
});

test("message timestamp advances with the shared clock and preserves the exact instant", () => {
  const message = <UserMessageView item={item({ text: "hello", startedAt: "2026-07-29T14:05:00.000Z" })} />;
  const { container, rerender } = render(
    <SessionNowContext value={Date.parse("2026-07-29T14:10:00Z")}>{message}</SessionNowContext>,
  );
  const before = container.querySelector("time")?.textContent;
  expect(before).toBeTruthy();
  expect(container.querySelector("time")?.dateTime).toBe("2026-07-29T14:05:00.000Z");
  expect(container.querySelector("time")?.title).toBeTruthy();
  rerender(<SessionNowContext value={Date.parse("2026-07-29T14:11:00Z")}>{message}</SessionNowContext>);
  expect(container.querySelector("time")?.textContent).not.toBe(before);
  expect(container.querySelector("time")?.dateTime).toBe("2026-07-29T14:05:00.000Z");
});

test("no time node at all (no placeholder) when startedAt is absent", () => {
  render(<UserMessageView item={item({ text: "hello" })} />);
  const root = screen.getByTestId("user-message-item");
  const header = root.querySelector(`.${speakerStyles.header}`) as HTMLElement;
  expect(header.querySelector(`.${speakerStyles.meta}`)).toBeNull();
  expect(header.children).toHaveLength(1);
});

test("actions live in the speaker header row", () => {
  render(<UserMessageView item={item({ text: "hello" })} actions={<button type="button">act</button>} />);
  const root = screen.getByTestId("user-message-item");
  const header = root.querySelector(`.${speakerStyles.header}`) as HTMLElement;
  expect(header.contains(screen.getByRole("button", { name: "act" }))).toBe(true);
});

test("renders the prompt text", () => {
  render(<UserMessageItem item={item({ text: "hello there" })} turn={turn} live={false} />);
  expect(screen.getByText("hello there")).toBeTruthy();
});

test('carries a quiet "You" tag as a sibling of the text, not mixed into it', () => {
  const { container } = render(<UserMessageItem item={item({ text: "hi" })} turn={turn} live={false} />);
  // The "You" tag and the prompt text are two separate nodes: the bubble
  // holds the text alone, so a mixed-in tag would show up in it.
  expect(screen.getByTestId("user-bubble").textContent?.trim()).toBe("hi");
  expect(screen.getByTestId("user-message-item").dataset.speaker).toBe("user");
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
  expect(content?.children[0]?.className).toBe(speakerStyles.header);
  expect((message as HTMLElement | null)?.dataset.speaker).toBe("user");
  const body = content?.children[1];
  expect(body?.className).toBe(styles.body);
  expect(body?.textContent).toContain("hi");
  expect(body?.querySelector('[data-testid="image-gallery-thumb"]')).toBeTruthy();
});

test("the speaker header and message column keep their alignment across widths", () => {
  const css = readModuleCss(import.meta.url, "usermessageitem.module.css");
  const speaker = readModuleCss(import.meta.url, "speakerheader.module.css");
  // One flex row: avatar (flex:none) + the transcript's single-sourced
  // --speaker-gap (10px, declared on .turn in turnblock.module.css), so the
  // content column lands on the same 34px line the TurnBlock gutter uses for
  // agent-side items. The 24+10=34 arithmetic itself is pinned by
  // speakeravatar.test.tsx's drift-pin test. The fallback (, 10px) covers the
  // "Intent" focused view (Session.tsx's focusedTranscript branch), which
  // renders this item directly - never under .turn - so there is no
  // ancestor to inherit the custom property from there (kata T9,
  // Session.test.tsx's own "declared exactly once" test covers the source
  // side of this contract).
  expect(css).toMatch(/\.message\s*\{[\s\S]*display:\s*flex;[\s\S]*gap:\s*var\(--speaker-gap\);/);
  expect(css).toMatch(/\.avatar\s*\{[\s\S]*flex:\s*none;/);
  // The content column must SPAN the row (flex: 1 1 auto), matching the agent
  // side's .column: left shrink-to-fit, the bubble's max-width becomes a
  // cyclic percentage against a containing block sized by the bubble itself,
  // which resolves against the text's own width and wraps the tail words of
  // every message wider than the header (2026-07-30 live-DOM measurement at
  // the old 92% cap: "commit and merge" clamped to 133px and wrapped in a
  // 651px row).
  expect(css).toMatch(/\.content\s*\{[\s\S]*flex:\s*1 1 auto;[\s\S]*min-width:\s*0;/);
  // The speaker header itself is shared with the agent message
  // (speakerheader.module.css): one flex row naming the speaker, with the
  // name at body size / semibold / --ink-hi (spec decision 1, weight raised
  // by typography-spacing-critique-2026-09-06 R3 so the header is a
  // landmark), and the meta slot composing the shared ui-size / --ink-mid
  // recipe (pinned once in text.contract.test.ts).
  expect(speaker).toMatch(/\.header\s*\{[\s\S]*display:\s*flex;[\s\S]*align-items:\s*baseline;/);
  expect(speaker).toMatch(
    /\.name\s*\{[\s\S]*font-size:\s*var\(--font-size-body\);[\s\S]*font-weight:\s*var\(--font-weight-semibold\);[\s\S]*color:\s*var\(--ink-hi\);/,
  );
  expect(speaker).toMatch(
    /\.meta\s*\{[\s\S]*composes:\s*textSecondary from "\.\.\/\.\.\/\.\.\/\.\.\/styles\/text\.module\.css";/,
  );
  // Text at --ink-hi (spec decision 5 - the header now carries the
  // boundary-scannability the old --ink-mid demotion was buying).
  expect(css).toMatch(/\.text\s*\{[\s\S]*color:\s*var\(--ink-hi\);/);
  // Actions stay right-anchored with hover/focus-within reveal.
  expect(css).toMatch(/\.actions\s*\{[\s\S]*margin-left:\s*auto;/);
  expect(css).toMatch(/\.message:hover\s+\.actions/);
  expect(css).toMatch(/\.message:focus-within\s+\.actions/);
  // One breakpoint, and it is not the gutter's (TurnBlock owns that): below
  // 700px the row becomes a grid so the bubble spans the pane under the
  // avatar + header row (typography-spacing-critique-2026-09-06 finding 2:
  // prose got 260px of a 375px screen). The avatar never leaves the header
  // line; the content column dissolves (display: contents) so its header and
  // body are the grid's own items.
  const phone = /@media \(max-width: 699px\)\s*\{([\s\S]*)\}\s*$/.exec(css);
  expect(phone).not.toBeNull();
  expect(phone![1]).toMatch(/\.message\s*\{[^}]*display:\s*grid;[^}]*grid-template-columns:\s*auto minmax\(0, 1fr\);/);
  expect(phone![1]).toMatch(/\.message > \.content\s*\{[^}]*display:\s*contents;/);
  expect(phone![1]).toMatch(/\.message \.body\s*\{[^}]*grid-column:\s*1 \/ -1;/);
  expect(css.split("@media").length).toBe(2);
});

test("the speaker avatar starts at the top of the user message row", () => {
  const css = readModuleCss(import.meta.url, "usermessageitem.module.css");
  expect(css).toMatch(/\.message\s*\{[\s\S]*align-items:\s*flex-start;/);
});

// --- the chat bubble (2026-07-30-transcript-chat-bubbles-design.md) --------

test("the body renders as a bubble wrapping the text and attachments", () => {
  render(<UserMessageView item={item({ text: "hi", images: [{ src: "data:image/png;base64,x" }] })} />);
  const bubble = screen.getByTestId("user-bubble");
  expect(bubble.textContent).toContain("hi");
  expect(bubble.querySelector('[data-testid="image-gallery-thumb"]')).toBeTruthy();
});

test("user prose spans its content column, pins the prose size, and avoids pre-wrap on markdown", () => {
  const css = readModuleCss(import.meta.url, "usermessageitem.module.css");
  const body = topRuleBlock(css, ".body");
  expect(body).toMatch(/(?:^|\n)\s*width:\s*100%/);
  expect(body).toMatch(/max-width:\s*100%/);
  expect(body).toMatch(/--prose-font-size:\s*var\(--font-size-prose\)/);
  expect(body).not.toMatch(/white-space:\s*pre-wrap/);
  expect(body).not.toMatch(/background:\s*var\(--accent-fill\)/);
  expect(body).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
  const text = topRuleBlock(css, ".text");
  expect(text).toMatch(/font-family:\s*var\(--font-prose\)/);
  expect(text).toMatch(/font-size:\s*var\(--font-size-prose\)/);
  const provisionalBody = topRuleBlock(css, ".provisional .body");
  expect(provisionalBody).toMatch(/width:\s*fit-content/);
  expect(provisionalBody).toMatch(/background:\s*var\(--surface-1\)/);
  expect(provisionalBody).toMatch(/border:\s*1px dashed var\(--accent\)/);
});

test("delivered user bubbles paint white on accent-fill while leaving agent and provisional rows neutral", () => {
  const css = readModuleCss(import.meta.url, "usermessageitem.module.css");
  const deliveredBody = topRuleBlock(css, '.message[data-speaker="user"]:not(.provisional) .body');
  expect(deliveredBody).toMatch(/width:\s*fit-content/);
  expect(deliveredBody).toMatch(/align-self:\s*flex-start/);
  expect(deliveredBody).toMatch(/justify-self:\s*start/);
  expect(deliveredBody).toMatch(/padding:\s*var\(--space-2\)\s+var\(--space-3\)/);
  expect(deliveredBody).toMatch(/background:\s*var\(--accent-fill\)/);
  expect(deliveredBody).toMatch(/color:\s*var\(--on-fill\)/);
  expect(deliveredBody).toMatch(
    /border-radius:\s*var\(--radius-chip\)\s+var\(--radius-pane\)\s+var\(--radius-pane\)\s+var\(--radius-pane\)/,
  );
  expect(deliveredBody).toMatch(/--markdown-ink:\s*var\(--on-fill\)/);
  expect(deliveredBody).toMatch(/--markdown-link:\s*var\(--on-fill\)/);
  expect(deliveredBody).toMatch(/--quiet-ink:\s*var\(--on-fill\)/);
  expect(deliveredBody).toMatch(/--selection-surface:\s*var\(--on-fill\)/);
  expect(deliveredBody).toMatch(/--selection-ink:\s*var\(--accent-fill\)/);
  expect(deliveredBody).toMatch(/--mermaid-surface:\s*var\(--surface-1\)/);
  expect(deliveredBody).toMatch(/--mermaid-radius:\s*var\(--radius-pane\)/);
  expect(deliveredBody).toMatch(/--mermaid-pad:\s*var\(--space-2\)/);
  expect(css).toMatch(
    /\.provisional\s+\.body:has\(pre,\s*hr,\s*table\),\s*\.message\[data-speaker="user"\]:not\(\.provisional\)\s+\.body:has\(pre,\s*hr,\s*table\)\s*\{[^}]*width:\s*100%/,
  );
  expect(css).not.toMatch(/\.message\[data-speaker="user"\]:not\(\.provisional\)\s+\.text\s*\{/);
});

test("inline markdown, entity triggers, quiet controls, and selection honor host overrides while self-surfaced blocks reset them", () => {
  const globalCss = readModuleCss(import.meta.url, "../../../../styles/global.css");
  const selection = topRuleBlock(globalCss, "::selection");
  expect(selection).toMatch(/background:\s*var\(--selection-surface,\s*var\(--selection-bg\)\)/);
  expect(selection).toMatch(/color:\s*var\(--selection-ink,\s*var\(--ink-hi\)\)/);

  const markdownCss = readModuleCss(import.meta.url, "../../../../widgets/markdown/markdown.module.css");
  expect(topRuleBlock(markdownCss, ".root a")).toMatch(/color:\s*var\(--markdown-link,\s*var\(--accent-ink\)\)/);
  expect(topRuleBlock(markdownCss, ".inlineCode")).toMatch(/color:\s*var\(--markdown-ink,\s*var\(--ink-hi\)\)/);
  expect(topRuleBlock(markdownCss, ".tableReflow")).toMatch(
    /contain-intrinsic-inline-size:\s*var\(--session-measure\)/,
  );
  expect(markdownCss).toMatch(
    /@container\s*\(max-width:\s*559px\)\s*\{[\s\S]*\.root \.tableReflow :where\(tbody tr\)\s*\{[^}]*--markdown-ink:\s*var\(--ink-hi\)/,
  );
  expect(markdownCss).toMatch(
    /@container\s*\(max-width:\s*559px\)\s*\{[\s\S]*\.root \.tableReflow :where\(tbody tr\)\s*\{[^}]*--selection-surface:\s*var\(--selection-bg\)/,
  );
  expect(markdownCss).not.toMatch(/--markdown-hover:\s*var\(--hover-1\)/);

  const codeblockCss = readModuleCss(import.meta.url, "../../../../widgets/codeblock/codeblock.module.css");
  const codeRoot = topRuleBlock(codeblockCss, ".root");
  expect(codeRoot).not.toMatch(/--quiet-/);
  expect(codeRoot).toMatch(/--selection-surface:\s*var\(--selection-bg\)/);
  expect(codeRoot).toMatch(/--selection-ink:\s*var\(--ink-hi\)/);

  const galleryCss = readModuleCss(import.meta.url, "../flow/imagegallery.module.css");
  const thumb = topRuleBlock(galleryCss, ".thumb");
  expect(thumb).toMatch(/--selection-surface:\s*var\(--selection-bg\)/);
  expect(thumb).toMatch(/--selection-ink:\s*var\(--ink-hi\)/);

  const entityCss = readModuleCss(import.meta.url, "../entityref.module.css");
  const trigger = topRuleBlock(entityCss, ".trigger");
  expect(trigger).toMatch(/color:\s*var\(--markdown-link,\s*var\(--accent-ink\)\)/);

  const buttonCss = readModuleCss(import.meta.url, "../../../../widgets/button/button.module.css");
  expect(topRuleBlock(buttonCss, ".quiet")).toMatch(/color:\s*var\(--quiet-ink,\s*var\(--ink-hi\)\)/);

  const mermaidCss = readModuleCss(import.meta.url, "../../../../widgets/mermaid/mermaid.module.css");
  const mermaidSvg = topRuleBlock(mermaidCss, ".svg");
  expect(mermaidSvg).toMatch(/background:\s*var\(--mermaid-surface,\s*transparent\)/);
  expect(mermaidSvg).toMatch(/border-radius:\s*var\(--mermaid-radius,\s*0\)/);
  expect(mermaidSvg).toMatch(/padding:\s*var\(--mermaid-pad,\s*0\)/);
});

test("renders GFM markdown in user messages and escapes raw HTML", () => {
  const source = [
    "## Plan",
    "",
    "Check **bold**, _italic_, `inline()`, and [docs](https://example.test/docs).",
    "",
    "- first",
    "- second",
    "",
    "> quoted note",
    "",
    "```ts",
    "const x = 1;",
    "```",
    "",
    "| Col | Val |",
    "| --- | --- |",
    "| a | 1 |",
    "",
    '<script>window.__xss = 1</script><div class="raw-box">literal</div><img src="x" onerror="alert(1)">',
  ].join("\n");

  const { container } = render(<UserMessageItem item={item({ text: source })} turn={turn} live={false} />);
  const bubble = screen.getByTestId("user-bubble");
  expect(screen.getByRole("heading", { level: 2, name: "Plan" })).toBeTruthy();
  expect(bubble.querySelector("strong")?.textContent).toBe("bold");
  expect(bubble.querySelector("em")?.textContent).toBe("italic");
  expect(bubble.querySelector("p code")?.textContent).toBe("inline()");
  const link = screen.getByRole("link", { name: "docs" });
  expect(link.getAttribute("href")).toBe("https://example.test/docs");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  expect(bubble.querySelectorAll("ul li")).toHaveLength(2);
  expect(bubble.querySelector("blockquote")?.textContent).toContain("quoted note");
  expect(bubble.querySelector("pre code")?.textContent).toBe("const x = 1;");
  expect(bubble.querySelectorAll("table tbody tr")).toHaveLength(1);
  // Raw HTML is escaped as text, never parsed into DOM elements.
  expect(container.querySelector("script")).toBeNull();
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector(".raw-box")).toBeNull();
  expect(bubble.textContent).toContain('<div class="raw-box">literal</div>');
});

test("resolves project file and file:// links in user messages beside the session pane", async () => {
  resetWorkspaceStoreForTests();
  const user = userEvent.setup();
  const sourcePaneId = workspaceStore.getState().openPane("session", { ref: "local:sess_1" });
  const thread = {
    ref: "local:sess_1",
    threadId: "sess_1",
    cwd: "/workspace/project",
  } as Parameters<typeof TranscriptRenderProvider>[0]["thread"];

  render(
    <TranscriptRenderProvider sessionRef="local:sess_1" thread={thread} sourcePaneId={sourcePaneId}>
      <UserMessageItem
        item={item({
          text: "Inspect [the report](file:///workspace/project/report.md) and src/main.ts.\n\n```\nsrc/ignored.ts\n```",
        })}
        turn={turn}
        live={false}
        sessionRef="local:sess_1"
      />
    </TranscriptRenderProvider>,
  );

  const reportLink = screen.getByRole("link", { name: "the report" });
  await user.click(reportLink);
  expect(
    workspaceStore
      .getState()
      .panes.filter((pane) => pane.type === "doc")
      .map((pane) => documentPaneState(pane)),
  ).toMatchObject([{ reference: { path: "report.md", cwd: "/workspace/project" }, origin: { id: sourcePaneId } }]);

  const bareFileLink = screen.getByRole("link", { name: "src/main.ts" });
  expect(bareFileLink).toBeTruthy();
  expect(screen.queryByRole("link", { name: "src/ignored.ts" })).toBeNull();
  resetWorkspaceStoreForTests();
});

test("markdown={false} keeps literal text for tool-output callers", () => {
  render(
    <UserMessageView
      item={item({ text: "Keep **stars** literal\nsecond line" })}
      speaker="agent"
      name="Agent → dlg_abc123"
      markdown={false}
    />,
  );
  const bubble = screen.getByTestId("user-bubble");
  expect(bubble.querySelector("strong")).toBeNull();
  expect(bubble.querySelector("p")).toBeNull();
  expect(bubble.textContent).toBe("Keep **stars** literal\nsecond line");
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
  expect(screen.getByTestId("user-message-item").dataset.speaker).toBe("user");
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

// A minimal, test-only "session" pane registration - real registerPane/
// openPane machinery without pulling in the actual panes/session module
// (mirrors SessionActionsMenu.test.tsx's identical setup for the same reason:
// these tests only assert openPane was called correctly, never that a real
// SessionPane renders).
afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);

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
    evener: { ref: "local/child_1", capabilities: FORK_CAPABILITIES, queue: { revision: 0 } },
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
    resetConnectionStoreForTests();
    resetThreadsStoreForTests();
    resetWorkspaceStoreForTests();
    localStorage.clear();
  });

  test("a user message with a sessionRef renders a Fork-from-here button; a read-only one (no ref) does not", () => {
    const { rerender } = render(
      <UserMessageItem
        item={item({ text: "fix the bug", transcriptKey: "apptranscript-item-v2:turn_1:0:0" })}
        turn={turn}
        live={false}
        sessionRef="ref_a"
      />,
    );
    expect(screen.getByRole("button", { name: /fork from here/i })).toBeTruthy();

    // No sessionRef (the read-only "open beside" transcript pane): forking
    // needs a ref to call thread/fork with, so the action is withheld.
    rerender(
      <UserMessageItem
        item={item({ text: "fix the bug", transcriptKey: "apptranscript-item-v2:turn_1:0:0" })}
        turn={turn}
        live={false}
      />,
    );
    expect(screen.queryByRole("button", { name: /fork from here/i })).toBeNull();
  });

  test("the fork button's hover label is the shared Tooltip, not a native title", () => {
    render(
      <UserMessageItem
        item={item({ text: "fix the bug", transcriptKey: "apptranscript-item-v2:turn_1:0:0" })}
        turn={turn}
        live={false}
        sessionRef="ref_a"
      />,
    );
    const button = screen.getByRole("button", { name: /fork from here/i });
    expect(button.hasAttribute("title")).toBe(false);
    expect(hoverForTooltip(button).textContent).toBe(button.getAttribute("aria-label"));
  });

  // thread/fork's sourceItemKey names an item key (transcriptindex.ItemKey),
  // not a turn id: cmd/evener-hub/app_threadlifecycle.go's parseSourceItemKey
  // resolves it to a 1-based index into the parent's entry list and hands
  // that straight to agent.ForkSessionAtUserTurn. The turn id only coincides
  // with that index on a transcript replayed from disk (internal/apptranscript
  // numbers turn_N off the entry index itself); every LIVE minter numbers
  // turns off a different counter, so sending turn.id cuts the child at an
  // unrelated entry. The item's own transcriptKey is the only field that
  // names the entry.
  test("forking calls thread/fork with this message's transcriptKey (not the turn id), seeds the child's composer draft, and opens it as a pane", async () => {
    const user = userEvent.setup();
    const fake = connectForkClient();
    let called: unknown;
    fake.on("thread/fork", (params) => {
      called = params;
      return { thread: forkWireThread(), originalInput: "fix the bug" };
    });

    // A live turn whose id (turn_1) and entry index (5) have diverged - the
    // everyday case past the first turn or two.
    render(
      <UserMessageItem
        item={item({ text: "fix the bug", transcriptKey: "apptranscript-item-v2:turn_1:4:0" })}
        turn={turn}
        live={false}
        sessionRef="ref_a"
      />,
    );
    await user.click(screen.getByRole("button", { name: /fork from here/i }));

    await waitFor(() =>
      expect(called).toEqual({
        ref: "ref_a",
        sourceItemKey: "apptranscript-item-v2:turn_1:4:0",
        deferInput: true,
      }),
    );
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
        <UserMessageItem
          item={item({ text: "fix the bug", transcriptKey: "apptranscript-item-v2:turn_1:0:0" })}
          turn={turn}
          live={false}
          sessionRef="ref_a"
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: /fork from here/i }));

    await screen.findByText(/fork boom/i);
    expect(workspaceStore.getState().panes).toHaveLength(0);
  });

  // The no-key fork: an item the transcript has not numbered names no
  // divergence position at all, so no child should be cut somewhere the user
  // never pointed at. A per-message
  // affordance refuses in the web-native way this component already uses for
  // the other unforkable case (no sessionRef, above): it is not offered.
  test("no Fork-from-here button at all when the item carries no transcriptKey", () => {
    const { rerender } = render(
      <UserMessageItem item={item({ text: "fix the bug" })} turn={turn} live={false} sessionRef="ref_a" />,
    );
    expect(screen.queryByRole("button", { name: /fork from here/i })).toBeNull();

    // An empty transcriptKey names no entry either.
    rerender(
      <UserMessageItem
        item={item({ text: "fix the bug", transcriptKey: "" })}
        turn={turn}
        live={false}
        sessionRef="ref_a"
      />,
    );
    expect(screen.queryByRole("button", { name: /fork from here/i })).toBeNull();
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

// --- speaker/name/timeIso overrides (delegate_send chat bubbles) -------------

test("UserMessageView accepts speaker/name/timeIso overrides for non-user speakers (delegate_send bubbles)", () => {
  render(
    <UserMessageView
      item={item({ text: "status?", startedAt: "2026-08-06T10:00:00Z" })}
      speaker="agent"
      name="Agent → dlg_abc123"
      timeIso="2026-08-06T10:05:00Z"
      markdown={false}
    />,
  );
  expect(screen.getByText("Agent → dlg_abc123")).toBeTruthy();
  expect(screen.getByTestId("user-bubble").textContent).toBe("status?");
  expect(screen.getByTestId("user-message-item").querySelector("time")?.dateTime).toBe("2026-08-06T10:05:00.000Z");
});

test("UserMessageView defaults are unchanged: user speaker, 'You' name, item.startedAt time", () => {
  render(<UserMessageView item={item({ text: "hi" })} />);
  const root = screen.getByTestId("user-message-item");
  expect(root.dataset.speaker).toBe("user");
  expect(root.querySelector(`.${speakerStyles.name}`)?.textContent ?? "").not.toBe("");
});

// --- the provisional register (steering-ghost live edge) ---------------------
// A held steering ghost renders through this same view in a provisional
// register (docs/web-ui/specs/2026-09-20-steering-ghost-live-edge.md §2): its
// caption takes the meta slot instead of the item time, and the row is marked
// for styling and tests. Absence of the prop renders exactly today's
// delivered form - the regression pin below.

test("a provisional caption renders in the meta slot instead of the item time", () => {
  render(
    <UserMessageView
      item={item({ text: "held body", startedAt: "2026-09-21T16:00:00.000Z" })}
      opensExchange={false}
      provisional="Delivers when this step finishes · held 42s"
    />,
  );
  expect(screen.getByText("Delivers when this step finishes · held 42s")).toBeTruthy();
  // The item time does not render: the meta slot is EXACTLY the caption, so
  // the header's whole text is the name plus the caption and nothing else.
  // (The old queryByText(/16:00/) pin was vacuous - MessageTimestamp renders
  // a relative label, never a clock string - while a timestamp rendered
  // alongside the caption would append its label here and fail.)
  const root = screen.getByTestId("user-message-item");
  const header = root.querySelector(`.${speakerStyles.header}`) as HTMLElement;
  expect(header.textContent).toBe("YouDelivers when this step finishes · held 42s");
});

test("the provisional register marks the row for styling and tests", () => {
  render(<UserMessageView item={item({ text: "held body" })} provisional="Joining this turn · 0s" />);
  expect(document.querySelector('[data-testid="user-message-item"][data-provisional="true"]')).not.toBeNull();
});

test("absent provisional renders exactly today's output (regression pin)", () => {
  render(<UserMessageView item={item({ text: "delivered body", startedAt: "2026-09-21T16:00:00.000Z" })} />);
  expect(document.querySelector('[data-testid="user-message-item"][data-provisional]')).toBeNull();
  expect(screen.getByTestId("user-bubble").textContent).toContain("delivered body");
});
