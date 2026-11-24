import type { ThreadModel } from "@evener/appwire-client";
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { workspaceStore } from "../../../../shell/workspace";
import { readModuleCss } from "../../../../styles/cssBlock";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import "../../../doc";
import { toolRendererFor } from "../toolRenderers";
import "./askUser";
import type { ItemModel } from "@evener/appwire-client";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// Ground truth: agent/internal/tool/definitions.go's DefAskUser - exact
// argumentsJson shape: {questions:[{header, question,
// options:[{label,detail,recommended?}], multi_select?, why?,
// if_unanswered?}]}, 1-4 questions, or the single-question shorthand
// {question, options} at the top level (see the shorthand tests below).
// ask_user's own Output is always the
// SAME fixed string on success (agent/session_tools_ask.go's
// askUserAckText) - not useful for rendering, so this descriptor reads
// entirely from argumentsJSON directly (the model preserves it through
// settle like every other tool call).

// The rendered question cards, as the body draws them.
function questionCards(): HTMLElement[] {
  return screen.queryAllByTestId("ask-user-question");
}

function askUserArgs(questions: unknown[]): string {
  return JSON.stringify({ questions });
}

const ONE_QUESTION = [
  {
    header: "Deploy?",
    question: "Should I deploy this to production now?",
    options: [
      { label: "Yes", detail: "Ship it", recommended: true },
      { label: "No", detail: "Wait" },
    ],
  },
];

// --- summary ----------------------------------------------------------

test("summary lists each question's header, bracketed and comma-joined", () => {
  const d = toolRendererFor("ask_user");
  expect(d.summary(item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) }))).toContain("[Deploy?]");
});

test("summary joins multiple question headers", () => {
  const d = toolRendererFor("ask_user");
  const questions = [
    {
      header: "Alpha",
      question: "q1",
      options: [
        { label: "x", detail: "y" },
        { label: "z", detail: "w" },
      ],
    },
    {
      header: "Beta",
      question: "q2",
      options: [
        { label: "x", detail: "y" },
        { label: "z", detail: "w" },
      ],
    },
  ];
  const summary = d.summary(item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) }));
  expect(summary.indexOf("[Alpha]")).toBeGreaterThan(-1);
  expect(summary.indexOf("[Beta]")).toBeGreaterThan(summary.indexOf("[Alpha]"));
});

test("summary degrades to a bare label for malformed argumentsJSON, never throws", () => {
  const d = toolRendererFor("ask_user");
  expect(() => d.summary(item({ toolName: "ask_user", argumentsJSON: "{not json" }))).not.toThrow();
  expect(d.summary(item({ toolName: "ask_user", argumentsJSON: "{not json" }))).not.toBe("");
});

// --- summarySuffix (kata h70z): the collapsed row's "— answered: ..."
// recap, read back from a later [answers] reply. answeredAskUserSuffix
// itself is unit-tested exhaustively in protocol/askShared.test.ts; these
// tests only confirm the descriptor wires it up correctly. -------------

function threadModel(items: ItemModel[]) {
  return { turns: [{ id: "turn_1", status: "completed", items }] } as unknown as Parameters<
    NonNullable<ReturnType<typeof toolRendererFor>["summarySuffix"]>
  >[1];
}

test("summarySuffix returns undefined while the question is still unanswered (no model)", () => {
  const d = toolRendererFor("ask_user");
  const ask = item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) });
  expect(d.summarySuffix?.(ask, undefined)).toBeUndefined();
});

test("summarySuffix returns the answered recap once a later [answers] reply resolves the call", () => {
  const d = toolRendererFor("ask_user");
  const ask = item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) });
  const reply: ItemModel = {
    id: "item_2",
    turnId: "turn_1",
    type: "userMessage",
    text: '[answers]\n1. [Deploy?] → "Yes"',
  };
  expect(d.summarySuffix?.(ask, threadModel([ask, reply]))).toContain('"Yes"');
});

// --- body: renders read-only question cards --------------------------

test("body renders the question text, options with detail, and the recommended marker", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) })} live={false} />);

  expect(screen.getByText("Should I deploy this to production now?")).toBeTruthy();
  expect(screen.getByText("No")).toBeTruthy();
  const label = screen.getByText("Yes");
  const detail = screen.getByText("Ship it");
  const labelLine = label.parentElement;
  const copy = labelLine?.parentElement;
  expect(copy?.contains(labelLine)).toBe(true);
  expect(detail.parentElement).toBe(copy);
  const options = screen.getAllByRole("listitem");
  expect(options.map((option) => option.dataset.recommended)).toEqual(["true", undefined]);
  // P6-R08: the read-only card uses the live dock's inline suffix - the
  // marker belongs to the recommended option's label line, and only that
  // option carries it.
  const markers = [...document.querySelectorAll('[data-slot="recommended"]')];
  expect(markers).toHaveLength(1);
  expect(options[0]?.querySelector('[data-slot="recommended"]')).toBe(markers[0]);
  expect(options[1]?.querySelector('[data-slot="recommended"]')).toBeNull();
  expect(label.nextElementSibling).toBe(markers[0]);
  // Still a read-only history card, not a second answer surface.
  expect(screen.queryAllByRole("radio")).toHaveLength(0);
  expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
});

// The question body is model-authored markdown and renders through the
// shared Markdown widget, the same as the composer's interactive card.
test("body renders the question text as markdown", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ ...ONE_QUESTION[0], question: "Deploy `agent/deploy.go:81` now?" }];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(screen.getByText("agent/deploy.go:81").tagName).toBe("CODE");
});

// The why and if_unanswered notes are model-authored prose too, so they
// take the same markdown pipeline as the question body.
test("body renders the why and if_unanswered notes as markdown", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [
    {
      ...ONE_QUESTION[0],
      why: "Changes `agent/deploy.go:81` behavior",
      if_unanswered: "assume the `make deploy` default",
    },
  ];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  const card = screen.getByTestId("ask-user-question");
  expect(card.querySelector('[data-note="why"] code')?.textContent).toBe("agent/deploy.go:81");
  expect(card.querySelector('[data-note="if-unanswered"] code')?.textContent).toBe("make deploy");
});

// The transcript question base is semibold like the dock's, so its strong
// spans need the same opt-in into the widget's bold seam; jsdom computes
// no cascade, so the stylesheet pin carries the contract.
test("bold emphasis in the question out-weighs the semibold base", () => {
  const css = readModuleCss(import.meta.url, "askuser.module.css");
  const rule = css.match(/\.question\s*\{([^}]*)\}/);
  expect(rule, "askuser.module.css must declare a .question rule").not.toBeNull();
  expect(rule![1]).toContain("--markdown-strong-weight: var(--font-weight-bold)");
});

// The transcript tool card renders inside ToolCallItem's transcript render
// provider, so its question prose can resolve file:// report links exactly
// like assistant prose. The fixture mirrors agentFileLinks.test.tsx's own
// thread fixture: the minimum ThreadModel AgentMarkdown needs to resolve a
// link against the session's cwd.
const fileLinkThread: ThreadModel = {
  ref: "local:sess_1",
  threadId: "sess_1",
  name: "Ask",
  status: { type: "idle" },
  modelProvider: "",
  model: "",
  visionModel: "",
  askPending: false,
  pendingEscalations: [],
  turns: [],
  queue: null,
  tasks: null,
  jobsUpdatedAt: null,
  jobsTreeRevision: null,
  lastFrameAt: 0,
  capabilities: {
    send: false,
    steer: false,
    interrupt: false,
    compact: false,
    clear: false,
    forkFromTurn: false,
    shutdown: false,
    changeModel: false,
    changeVisionModel: false,
    queue: false,
    goal: false,
    sharedNotes: false,
    rename: false,
  },
  goal: null,
  humanNote: "",
  agentNote: "",
  sessionUrls: [],
  contextUsed: 0,
  contextWindow: 0,
  contextPressure: 0,
  contextAssumed: false,
  compactionThreshold: 0,
  usage: null,
  workMillis: 0,
  reasoningEffortLevels: [],
  supportsReasoning: false,
  cwd: "/tmp/project",
};

test("body resolves a file:// report link in the question into a document request", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ ...ONE_QUESTION[0], question: "See [the report](file:///tmp/project/docs/report.md) first?" }];
  render(
    <TranscriptRenderProvider thread={fileLinkThread} sourcePaneId="pane_fixture">
      <Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />
    </TranscriptRenderProvider>,
  );

  const open = screen.getByRole("button", { name: "Open beside: docs/report.md" });
  fireEvent.click(open);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: "local:sess_1", path: "docs/report.md", kind: "file" } },
  ]);
});

test("body notes the answer happens in the composer (this wave is read-only)", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(ONE_QUESTION) })} live={false} />);
  expect(screen.getByTestId("ask-user-answer-hint")).toBeTruthy();
});

test("body shows a multi_select note when the question allows multiple answers", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ ...ONE_QUESTION[0], multi_select: true }];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(screen.getByTestId("ask-user-question").querySelector('[data-note="multi-select"]')).not.toBeNull();
});

test("body shows why/if_unanswered notes when present", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [{ ...ONE_QUESTION[0], why: "affects the release", if_unanswered: "assume yes" }];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  const card = screen.getByTestId("ask-user-question");
  // Markdown serialization ends with a newline after the closing block tag
  // - the same trim convention markdown.test.tsx and ThinkBlock.test.tsx
  // use.
  expect(card.querySelector('[data-note="why"]')?.textContent?.trim()).toBe("affects the release");
  expect(card.querySelector('[data-note="if-unanswered"]')?.textContent?.trim()).toBe("If unanswered: assume yes");
  expect(card.querySelector('[data-note="multi-select"]')).toBeNull();
});

test("body renders multiple question cards in order", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [
    {
      header: "First",
      question: "question one",
      options: [
        { label: "a", detail: "b" },
        { label: "c", detail: "d" },
      ],
    },
    {
      header: "Second",
      question: "question two",
      options: [
        { label: "e", detail: "f" },
        { label: "g", detail: "h" },
      ],
    },
  ];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  // The question renders through the Markdown widget, whose serialized
  // output ends with a newline after the closing block tag - the same
  // trim convention markdown.test.tsx and ThinkBlock.test.tsx use.
  expect(questionCards().map((card) => card.querySelector('[data-slot="question"]')?.textContent?.trim())).toEqual([
    "question one",
    "question two",
  ]);
});

// --- defensive parsing: malformed input never crashes ---------------------
//
// Two distinct fallback messages, not one: a genuine JSON syntax error in
// THIS item's own argumentsJSON is "malformed" (something arrived and it's
// broken); everything else that still ends up with no usable questions -
// argumentsJSON absent entirely (now only a genuinely argless item, since
// the model preserves real argumentsJSON through both settle and
// hydration), or syntactically valid JSON that simply carries no
// questions - is honest absence, not corruption, and must not be
// described as "malformed".

test("body renders the malformed-data fallback for a genuine JSON syntax error", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  expect(() =>
    render(<Body item={item({ toolName: "ask_user", argumentsJSON: "{not json" })} live={false} />),
  ).not.toThrow();
  expect(screen.getByTestId("ask-user-fallback").dataset.reason).toBe("malformed");
});

test("body renders the absent-data fallback (not malformed) when questions is missing from otherwise-valid JSON", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: "{}" })} live={false} />);
  expect(screen.getByTestId("ask-user-fallback").dataset.reason).toBe("unavailable");
});

test("body renders the absent-data fallback (not malformed) when argumentsJSON is missing entirely - the cold-open case", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  render(<Body item={item({ toolName: "ask_user" })} live={false} />);
  expect(screen.getByTestId("ask-user-fallback").dataset.reason).toBe("unavailable");
});

// --- the single-question shorthand form (see askShared.ts for the form) ---

test("body renders a shorthand-form call's question card, not the unavailable fallback", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const shorthand = JSON.stringify({
    question: "Should I deploy this to production now?",
    options: [
      { label: "Yes", detail: "Ship it", recommended: true },
      { label: "No", detail: "Wait" },
    ],
  });
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: shorthand })} live={false} />);
  expect(screen.queryByTestId("ask-user-fallback")).toBeNull();
  expect(screen.getByText("Should I deploy this to production now?")).toBeTruthy();
  expect(screen.getByText("Ship it")).toBeTruthy();
  const options = screen.getAllByRole("listitem");
  expect(options.map((option) => option.dataset.recommended)).toEqual(["true", undefined]);
});

test("body skips a malformed individual question (missing required fields) rather than crashing the whole card", () => {
  const d = toolRendererFor("ask_user");
  const Body = d.body!;
  const questions = [
    {
      header: "Good",
      question: "a real question",
      options: [
        { label: "a", detail: "b" },
        { label: "c", detail: "d" },
      ],
    },
    { header: "Bad" /* missing question/options */ },
  ];
  render(<Body item={item({ toolName: "ask_user", argumentsJSON: askUserArgs(questions) })} live={false} />);
  expect(questionCards().map((card) => card.querySelector('[data-slot="question"]')?.textContent?.trim())).toEqual([
    "a real question",
  ]);
});
