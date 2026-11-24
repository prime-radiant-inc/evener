import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { mediaBlock, readModuleCss } from "../../styles/cssBlock";
import { Cadence, type CadenceState, stateFamily } from "../cadence";
import rawCadenceStyles from "../cadence/cadence.module.css";
import { requireClass } from "../internal/requireClass";
import { StatusDot } from "./index";
import rawStyles from "./statusdot.module.css";

const styles = {
  alive: requireClass(rawStyles.alive, "statusdot.module.css", "alive"),
  attention: requireClass(rawStyles.attention, "statusdot.module.css", "attention"),
  danger: requireClass(rawStyles.danger, "statusdot.module.css", "danger"),
  neutral: requireClass(rawStyles.neutral, "statusdot.module.css", "neutral"),
};

const cadenceStyles = {
  alive: requireClass(rawCadenceStyles.alive, "cadence.module.css", "alive"),
  attention: requireClass(rawCadenceStyles.attention, "cadence.module.css", "attention"),
  danger: requireClass(rawCadenceStyles.danger, "cadence.module.css", "danger"),
  neutral: requireClass(rawCadenceStyles.neutral, "cadence.module.css", "neutral"),
};

// state -> token family, mirroring Cadence's own STATE_FAMILY mapping (see
// src/widgets/cadence/index.tsx): working=alive, needs-you=attention,
// failed=danger, idle/ended=neutral.
const STATE_FAMILIES: [CadenceState, string, string][] = [
  ["idle", styles.neutral, "Idle"],
  ["working", styles.alive, "Working"],
  ["needs-you", styles.attention, "Needs you"],
  ["failed", styles.danger, "Failed"],
  ["ended", styles.neutral, "Ended"],
];

for (const [state, familyClass, label] of STATE_FAMILIES) {
  test(`state ${state} maps to its token family class`, () => {
    render(<StatusDot state={state} />);
    expect(screen.getByRole("img").classList.contains(familyClass)).toBe(true);
  });

  test(`state ${state} carries an accessible name naming the state`, () => {
    render(<StatusDot state={state} />);
    expect(screen.getByRole("img", { name: label })).toBeTruthy();
  });
}

test("is not in the tab order - it's a status indicator, not a control", () => {
  render(<StatusDot state="working" />);
  expect(screen.getByRole("img").getAttribute("tabindex")).toBeNull();
});

// The opt-in a caller uses when its own adjacent text already names the status
// (the compact ancestor spine's exact runtime label): the glyph stays for its
// shape, geometry and motion but leaves the accessibility tree, so the status
// is never announced twice.
test("decorative drops the glyph's own role and accessible name", () => {
  render(<StatusDot state="working" decorative />);
  expect(screen.queryByRole("img")).toBeNull();
  const glyph = document.querySelector("[data-state]");
  expect(glyph?.getAttribute("aria-hidden")).toBe("true");
  expect(glyph?.getAttribute("aria-label")).toBeNull();
  expect(glyph?.getAttribute("data-state")).toBe("working");
  expect(glyph?.classList.contains(requireClass(rawStyles.working, "statusdot.module.css", "working"))).toBe(true);
});

test("decorative keeps the shared geometry: the failed diamond and the round dots", () => {
  for (const [state, shape] of [
    ["failed", "diamond"],
    ["idle", "dot"],
    ["needs-you", "dot"],
  ] as const) {
    const { unmount } = render(<StatusDot state={state} decorative />);
    expect(document.querySelector("[data-state]")?.getAttribute("data-shape")).toBe(shape);
    unmount();
  }
});

// vitest leaves CSS Modules unprocessed, so the motion contract is read off
// disk: the working spinner turns only inside the no-preference gate, and
// under reduced motion the working state is the plain static alive dot.
const CSS = readModuleCss(import.meta.url, "statusdot.module.css");

test("working plays its activity spinner only when motion is allowed", () => {
  render(<StatusDot state="working" />);
  const working = requireClass(rawStyles.working, "statusdot.module.css", "working");
  expect(screen.getByRole("img", { name: "Working" }).classList.contains(working)).toBe(true);
  const motionRules = mediaBlock(CSS, "prefers-reduced-motion: no-preference");
  expect(motionRules).toMatch(/\.working\s*\{[^}]*animation:[^}]*infinite/);
  expect(CSS.replace(motionRules, "")).not.toMatch(/animation:/);
});

// §1 bans color-only distinctions: failed and needs-you must differ in shape,
// not just hue, and the diamond's geometry must actually exist in the CSS.
test("failed is a diamond while every other state is a round dot", () => {
  const shapes = Object.fromEntries(
    (["idle", "working", "needs-you", "failed", "ended"] as const).map((state) => {
      const { unmount } = render(<StatusDot state={state} />);
      const shape = screen.getByRole("img").getAttribute("data-shape");
      unmount();
      return [state, shape];
    }),
  );
  expect(shapes).toEqual({ idle: "dot", working: "dot", "needs-you": "dot", failed: "diamond", ended: "dot" });
  expect(CSS).toMatch(/\.dot\[data-shape="diamond"\]\s*\{[^}]*border-radius:\s*1px;[^}]*rotate\(45deg\)/);
});

test("the spinner's period is the activity motion token", () => {
  expect(mediaBlock(CSS, "prefers-reduced-motion: no-preference")).toMatch(
    /animation:[^;]*var\(--motion-duration-activity\)/,
  );
});

test("only the working state carries the activity spinner", () => {
  const working = requireClass(rawStyles.working, "statusdot.module.css", "working");
  for (const state of ["idle", "needs-you", "failed", "ended"] as const) {
    const { unmount } = render(<StatusDot state={state} />);
    expect(screen.getByRole("img").classList.contains(working)).toBe(false);
    unmount();
  }
});

test("marks its state as data-state, so a state can be read without its accessible name", () => {
  for (const [state] of STATE_FAMILIES) {
    const { unmount } = render(<StatusDot state={state} />);
    expect(screen.getByRole("img").getAttribute("data-state")).toBe(state);
    unmount();
  }
});

// One table now: Cadence owns the state -> family map and StatusDot reads it.
// Rendering both glyphs for every state proves they land in the same family,
// so a private copy that drifts cannot come back unnoticed.
const FAMILY_NAMES = ["alive", "attention", "danger", "neutral"] as const;

function familyOf(element: Element, families: Record<(typeof FAMILY_NAMES)[number], string>): string {
  const matches = FAMILY_NAMES.filter((name) => element.classList.contains(families[name]));
  expect(matches).toHaveLength(1);
  return matches[0]!;
}

test("Cadence and StatusDot agree on the state -> family map", () => {
  for (const state of ["idle", "working", "needs-you", "failed", "ended"] as const) {
    const dot = render(<StatusDot state={state} />);
    const dotFamily = familyOf(screen.getByRole("img"), styles);
    dot.unmount();
    const cadence = render(<Cadence state={state} frameTimes={[]} now={0} />);
    const cadenceFamily = familyOf(screen.getByTestId("cadence-dot"), cadenceStyles);
    cadence.unmount();
    expect(dotFamily).toBe(stateFamily(state));
    expect(cadenceFamily).toBe(stateFamily(state));
  }
});
