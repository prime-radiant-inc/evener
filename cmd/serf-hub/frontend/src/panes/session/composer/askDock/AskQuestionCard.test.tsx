import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { AskQuestionCard } from "./AskQuestionCard";
import type { AskAnswerState } from "./askDockStore";
import type { AskQuestionRef } from "./deriveAskQuestions";

afterEach(cleanup);

function question(overrides: Partial<AskQuestionRef> = {}): AskQuestionRef {
  return {
    key: "call_1:0",
    callId: "call_1",
    header: "Deploy?",
    question: "Should I deploy this to production now?",
    options: [
      { label: "Yes", detail: "Ship it", recommended: true },
      { label: "No", detail: "Wait" },
    ],
    multiSelect: false,
    ...overrides,
  };
}

const UNTOUCHED: AskAnswerState = { resolution: null, note: "" };

// Controlled harness: AskQuestionCard is a pure controlled component (no
// state of its own besides focus-management refs), so tests drive it the
// way a real parent (AskDock) would - own the answer state, re-render on
// every callback.
function Harness({ q, initial = UNTOUCHED }: { q: AskQuestionRef; initial?: AskAnswerState }) {
  const [answer, setAnswer] = useState<AskAnswerState>(initial);
  return (
    <AskQuestionCard
      question={q}
      number={1}
      answer={answer}
      onResolutionChange={(resolution) => setAnswer((prev) => ({ ...prev, resolution }))}
      onNoteChange={(note) => setAnswer((prev) => ({ ...prev, note }))}
    />
  );
}

test("renders the question number, header, and question text", () => {
  render(<Harness q={question()} />);
  expect(screen.getByText("1.")).toBeTruthy();
  expect(screen.getByText("Deploy?")).toBeTruthy();
  expect(screen.getByText("Should I deploy this to production now?")).toBeTruthy();
});

test("renders each option's label and detail, tagging only the recommended one", () => {
  render(<Harness q={question()} />);
  expect(screen.getByText("Yes")).toBeTruthy();
  expect(screen.getByText("Ship it")).toBeTruthy();
  expect(screen.getByText("No")).toBeTruthy();
  expect(screen.getAllByText(/recommended/i)).toHaveLength(1);
});

test("the recommended option renders first regardless of input order", () => {
  const q = question({
    options: [
      { label: "No", detail: "Wait" },
      { label: "Yes", detail: "Ship it", recommended: true },
    ],
  });
  render(<Harness q={q} />);
  const labels = screen
    .getAllByRole("radio")
    .map((el) => el.getAttribute("aria-label") ?? el.closest("label")?.textContent);
  // Regular options render before the free/decide alternatives either way;
  // the recommended one specifically must be first among THEM.
  expect(labels[0]).toContain("Yes");
});

test("renders the why explanation verbatim when present", () => {
  render(<Harness q={question({ why: "affects the release" })} />);
  expect(screen.getByText("affects the release")).toBeTruthy();
});

test("renders no fallback button when if_unanswered is absent", () => {
  render(<Harness q={question()} />);
  expect(screen.queryByRole("button", { name: /do that/i })).toBeNull();
});

test("renders a fallback button naming if_unanswered verbatim when present", () => {
  render(<Harness q={question({ ifUnanswered: "assume yes" })} />);
  expect(screen.getByRole("button", { name: /assume yes/i })).toBeTruthy();
});

test("skip is always offered", () => {
  render(<Harness q={question()} />);
  expect(screen.getByRole("button", { name: /skip/i })).toBeTruthy();
});

test("clicking a regular option (single-select) resolves to that option alone", async () => {
  const user = userEvent.setup();
  const onResolutionChange = vi.fn();
  render(
    <AskQuestionCard
      question={question()}
      number={1}
      answer={UNTOUCHED}
      onResolutionChange={onResolutionChange}
      onNoteChange={() => {}}
    />,
  );
  await user.click(screen.getByRole("radio", { name: /yes/i }));
  expect(onResolutionChange).toHaveBeenCalledWith({ kind: "option", labels: ["Yes"] });
});

test("a multi_select question renders checkboxes; checking two accumulates both labels", async () => {
  const user = userEvent.setup();
  const q = question({ multiSelect: true });
  render(<Harness q={q} />);
  await user.click(screen.getByRole("checkbox", { name: /yes/i }));
  await user.click(screen.getByRole("checkbox", { name: /no/i }));
  expect(screen.getByRole("checkbox", { name: /yes/i })).toHaveProperty("checked", true);
  expect(screen.getByRole("checkbox", { name: /no/i })).toHaveProperty("checked", true);
});

test("unchecking one multi_select box removes just that label", async () => {
  const user = userEvent.setup();
  const q = question({ multiSelect: true });
  render(<Harness q={q} initial={{ resolution: { kind: "option", labels: ["Yes", "No"] }, note: "" }} />);
  await user.click(screen.getByRole("checkbox", { name: /yes/i }));
  expect(screen.getByRole("checkbox", { name: /yes/i })).toHaveProperty("checked", false);
  expect(screen.getByRole("checkbox", { name: /no/i })).toHaveProperty("checked", true);
});

test("unchecking the last multi_select box clears the resolution entirely", async () => {
  const user = userEvent.setup();
  const q = question({ multiSelect: true });
  render(<Harness q={q} initial={{ resolution: { kind: "option", labels: ["Yes"] }, note: "" }} />);
  await user.click(screen.getByRole("checkbox", { name: /yes/i }));
  expect(screen.getByRole("checkbox", { name: /yes/i })).toHaveProperty("checked", false);
  expect(screen.getByRole("checkbox", { name: /no/i })).toHaveProperty("checked", false);
});

test("activating the free-text alternative moves focus into its own input", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  expect(document.activeElement).toBe(screen.getByPlaceholderText(/type your answer/i));
});

test("typing in the free-text input composes a free resolution", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} initial={{ resolution: { kind: "free", text: "" }, note: "" }} />);
  await user.type(screen.getByPlaceholderText(/type your answer/i), "hi");
  expect(screen.getByPlaceholderText(/type your answer/i)).toHaveProperty("value", "hi");
});

test("clicking the already-active free alternative again toggles it off", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} initial={{ resolution: { kind: "free", text: "hello" }, note: "" }} />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  expect(screen.queryByPlaceholderText(/type your answer/i)).toBeNull();
});

test("activating decide shows an optional leaning input", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} />);
  await user.click(screen.getByRole("radio", { name: /let serf decide/i }));
  expect(document.activeElement).toBe(screen.getByPlaceholderText(/leaning/i));
});

test("clicking the already-active decide alternative again toggles it off", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} initial={{ resolution: { kind: "decide", leaning: "" }, note: "" }} />);
  await user.click(screen.getByRole("radio", { name: /let serf decide/i }));
  expect(screen.queryByPlaceholderText(/leaning/i)).toBeNull();
});

test("clicking fallback resolves to fallback; clicking it again toggles off", async () => {
  const user = userEvent.setup();
  render(<Harness q={question({ ifUnanswered: "assume yes" })} />);
  const fallbackBtn = screen.getByRole("button", { name: /assume yes/i });
  await user.click(fallbackBtn);
  expect(fallbackBtn.getAttribute("aria-pressed")).toBe("true");
  await user.click(fallbackBtn);
  expect(fallbackBtn.getAttribute("aria-pressed")).toBe("false");
});

test("clicking skip resolves to skip; clicking it again toggles off", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} />);
  const skipBtn = screen.getByRole("button", { name: /skip/i });
  await user.click(skipBtn);
  expect(skipBtn.getAttribute("aria-pressed")).toBe("true");
  await user.click(skipBtn);
  expect(skipBtn.getAttribute("aria-pressed")).toBe("false");
});

test("picking a regular option after free-text was active clears the free resolution (mutual exclusion)", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} initial={{ resolution: { kind: "free", text: "x" }, note: "" }} />);
  await user.click(screen.getByRole("radio", { name: /yes/i }));
  expect(screen.queryByPlaceholderText(/type your answer/i)).toBeNull();
  expect(screen.getByRole("radio", { name: /yes/i })).toHaveProperty("checked", true);
});

test("the note field is visible with no disclosure toggle and calls onNoteChange", async () => {
  const user = userEvent.setup();
  const onNoteChange = vi.fn();
  render(
    <AskQuestionCard
      question={question()}
      number={1}
      answer={UNTOUCHED}
      onResolutionChange={() => {}}
      onNoteChange={onNoteChange}
    />,
  );
  const note = screen.getByPlaceholderText(/note/i);
  await user.type(note, "x");
  expect(onNoteChange).toHaveBeenCalled();
});

test("the free input's aria-labelledby resolves to its own alternative label, not a colliding regular option", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  const freeInput = screen.getByPlaceholderText(/type your answer/i);
  const labelledBy = freeInput.getAttribute("aria-labelledby") ?? "";
  const ids = labelledBy.split(" ").filter(Boolean);
  expect(ids.length).toBeGreaterThan(0);
  for (const id of ids) {
    expect(document.getElementById(id)).not.toBeNull();
  }
  // Resolves to the free alternative's OWN label text, not "Yes"/"No".
  const labelEl = document.getElementById(ids[0] ?? "");
  expect(labelEl?.textContent).toMatch(/something else/i);
});

test("options render inside a group whose role reflects single vs multi select", () => {
  const { unmount } = render(<Harness q={question()} />);
  expect(screen.getByRole("radiogroup")).toBeTruthy();
  unmount();
  render(<Harness q={question({ multiSelect: true })} />);
  expect(screen.getByRole("group")).toBeTruthy();
});
