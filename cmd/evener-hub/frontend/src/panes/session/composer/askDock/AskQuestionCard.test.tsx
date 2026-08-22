import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { AskQuestionCard } from "./AskQuestionCard";
import type { AskAnswerState } from "./askDockStore";
import type { AskQuestionRef } from "./deriveAskQuestions";

afterEach(cleanup);

// A stylesheet assertion must never match its own commentary (testing.md:
// "A stylesheet assertion that matches its own comment") - strip comments
// before any toContain/match against the CSS text.
function cardCss(): string {
  return readFileSync(join(dirname(fileURLToPath(import.meta.url)), "askquestioncard.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );
}

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

// Option labels are inline bold text, not pills (ask-dialog UX rework): the
// whole label is ordinary wrapping text inside the option row, so an
// arbitrarily long VALID label wraps onto as many lines as it needs instead
// of being crushed inside a fixed-height chip. jsdom does not lay out text;
// the stylesheet assertions below pin the bold presentation contract, and
// layoutguard's askdock-mobile-tall-crush case owns the real geometry.
test("option labels render as bold inline text, keeping the full label available", () => {
  const label = `${"A valid option label that remains available to the user ".repeat(19)}A valid option label that remains available to the user`;
  render(
    <Harness
      q={question({
        options: [{ label, detail: "The detail remains separate from the label." }],
      })}
    />,
  );

  const radio = screen.getByRole("radio", { name: label });
  const labelEl = radio.nextElementSibling;
  expect(labelEl?.textContent).toBe(label);

  const css = cardCss();
  const rule = css.match(/\.optionLabel\s*\{([^}]*)\}/);
  expect(rule, "askquestioncard.module.css must declare an .optionLabel rule").not.toBeNull();
  expect(rule![1]).toMatch(/font-weight:\s*var\(--font-weight-semibold\)/);
  // The pill treatment is gone entirely - no chip wrapper class remains.
  expect(css).not.toContain(".optionChip");
});

// Recommended gets its own visual treatment (ask-dialog UX rework): an
// accent-colored suffix directly after the bold label, replacing the old
// far-right uppercase tag.
test("the recommended option carries an accent suffix right after its bold label", () => {
  render(<Harness q={question()} />);

  const yesLabel = screen.getByText("Yes");
  const marker = screen.getByText("· recommended");
  // Same option row, immediately after the label.
  expect(marker.parentElement).toBe(yesLabel.parentElement);
  expect(yesLabel.nextElementSibling).toBe(marker);

  const css = cardCss();
  const rule = css.match(/\.optionRecommended\s*\{([^}]*)\}/);
  expect(rule, "askquestioncard.module.css must declare an .optionRecommended rule").not.toBeNull();
  expect(rule![1]).toMatch(/color:\s*var\(--accent\)/);
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
  // Regular options render before the "Something else…" alternative either
  // way; the recommended one specifically must be first among THEM.
  expect(labels[0]).toContain("Yes");
});

test("renders the why explanation verbatim when present", () => {
  render(<Harness q={question({ why: "affects the release" })} />);
  expect(screen.getByText("affects the release")).toBeTruthy();
});

// The skip and if_unanswered-fallback pills are gone (ask-dialog UX rework):
// a recommended default is pre-selected and "Something else…" covers every
// other escape, so neither control is offered even when the question carries
// if_unanswered.
test("no skip or fallback controls are offered, even when if_unanswered is present", () => {
  render(<Harness q={question({ ifUnanswered: "assume yes" })} />);
  expect(screen.queryByRole("button", { name: /skip/i })).toBeNull();
  expect(screen.queryByRole("button", { name: /do that/i })).toBeNull();
});

// "let evener decide" is gone (ask-dialog UX rework): the recommended option
// is selected by default instead of asking the reader to defer explicitly.
test("no let-evener-decide alternative is offered", () => {
  render(<Harness q={question()} />);
  expect(screen.queryByRole("radio", { name: /let evener decide/i })).toBeNull();
  expect(screen.queryByPlaceholderText(/leaning/i)).toBeNull();
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

// "Something else…" reuses the per-question note field as its answer input
// (ask-dialog UX rework): one text field per question, always - selecting
// the alternative turns THAT field into the free-text answer instead of
// adding a second input.
test("activating Something else turns the note field into the answer input and focuses it", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));

  const input = screen.getByPlaceholderText(/type your answer/i);
  expect(document.activeElement).toBe(input);
  expect(screen.queryByPlaceholderText(/note \(optional\)/i)).toBeNull();
  // AskDock's batch-level keydown handler reads this attribute to give a
  // bare Enter the primary action - it must travel with the field's
  // answer-input mode, not stay on a note field.
  expect(input.getAttribute("data-ask-free-input")).toBe("true");
});

test("while Something else is active, the shared text field edits the free resolution, not the note", async () => {
  const user = userEvent.setup();
  const onResolutionChange = vi.fn();
  const onNoteChange = vi.fn();
  render(
    <AskQuestionCard
      question={question()}
      number={1}
      answer={{ resolution: { kind: "free", text: "" }, note: "" }}
      onResolutionChange={onResolutionChange}
      onNoteChange={onNoteChange}
    />,
  );
  await user.type(screen.getByPlaceholderText(/type your answer/i), "x");
  expect(onResolutionChange).toHaveBeenCalledWith({ kind: "free", text: "x" });
  expect(onNoteChange).not.toHaveBeenCalled();
});

test("typing in the free-text field composes a free resolution", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} initial={{ resolution: { kind: "free", text: "" }, note: "" }} />);
  await user.type(screen.getByPlaceholderText(/type your answer/i), "hi");
  expect(screen.getByPlaceholderText(/type your answer/i)).toHaveProperty("value", "hi");
});

test("clicking the already-active Something else alternative again reverts the field to a note", async () => {
  const user = userEvent.setup();
  render(<Harness q={question()} initial={{ resolution: { kind: "free", text: "hello" }, note: "" }} />);
  await user.click(screen.getByRole("radio", { name: /something else/i }));
  expect(screen.queryByPlaceholderText(/type your answer/i)).toBeNull();
  const note = screen.getByPlaceholderText(/note \(optional\)/i);
  expect(note.getAttribute("data-ask-free-input")).toBeNull();
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
  expect(note.getAttribute("data-ask-free-input")).toBeNull();
  await user.type(note, "x");
  expect(onNoteChange).toHaveBeenCalled();
});

test("the free-mode field's aria-labelledby resolves to its own alternative label, not a colliding regular option", async () => {
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

// Touch target (UX fix, real-phone measurement): an option WITH a detail
// line stands ~46-68px tall (label + wrapped detail), but an option with no
// detail collapsed to ~21px - just the label's own line height, well under
// the platform's 44px tap floor. padding gives every option row a
// consistent comfortable size at desktop regardless of whether it has a
// detail; the (pointer: coarse) block (same pattern as askdock.module.css's
// own .tab rule) grows short rows the rest of the way to --tap-min on touch.
test("every option row gets comfortable padding, and reaches the tap floor on a coarse pointer", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "askquestioncard.module.css"), "utf8");
  const desktopRule = css.match(/\.option\s*\{([^}]*)\}/);
  expect(desktopRule, "askquestioncard.module.css must declare a base .option rule").not.toBeNull();
  expect(desktopRule![1]).toMatch(/padding:/);

  const coarse = css.match(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/);
  expect(coarse, "askquestioncard.module.css must have a (pointer: coarse) media block").not.toBeNull();
  const rule = coarse![1]!.match(/\.option\s*\{([^}]*)\}/);
  expect(rule, "the coarse-pointer block must override .option").not.toBeNull();
  expect(rule![1]).toContain("min-height: var(--tap-min)");
});
