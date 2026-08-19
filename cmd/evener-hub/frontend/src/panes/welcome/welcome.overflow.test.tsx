import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetTreeStoreForTests, type TreeNode, type TreeResponse, treeStore } from "../../stores/tree";
import Welcome from "./Welcome";

// A stylesheet-grep assertion must not be satisfiable by a comment - this repo
// has a precedent of exactly that passing while asserting nothing (see
// codeblock.test.tsx's own stripCssComments).
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  resetTreeStoreForTests();
});

function node(overrides: Partial<TreeNode> = {}): TreeNode {
  return {
    row_id: "row1",
    ref: "local:row1",
    host_id: "local",
    session_id: "row1",
    title: "Session",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

const EMPTY_TREE: TreeResponse = {
  generated_at: "2026-01-01T00:00:00Z",
  sources: [],
  live: [],
  needs_you: [],
  pin_sections: [],
  projects: [],
  archived_projects: [],
  test_runs: [],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};

function setTree(overrides: Partial<TreeResponse>): void {
  treeStore.setState({ tree: { ...EMPTY_TREE, ...overrides } });
}

const here = dirname(fileURLToPath(import.meta.url));
const welcomeCss = () => stripCssComments(readFileSync(join(here, "welcome.module.css"), "utf8"));

// Issue #197: a server-truncated-but-still-wide (200-rune) resume-candidate
// title must render in full in the DOM - the fix wraps it, it must not be
// client-side truncated out of existence. (This one already passes today:
// jsdom renders the whole text node regardless of CSS; it's the regression
// pin that the wrapping fix must never drop the text from the DOM.)
const LONG_TITLE =
  "This means that we're going to cover all of the binary names, all of the command entry points, and every alias the installer ships, in one pass so nothing is missed";

test("the full long resume-candidate title is present in the DOM, not truncated away", () => {
  setTree({ live: [node({ ref: "local:long1", title: LONG_TITLE })] });
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  const button = screen.getByRole("button", { name: /Jump back in/ });
  expect(button.textContent).toContain(LONG_TITLE);
});

// Issue #197 root cause: button.module.css sets `white-space: nowrap` on every
// button (correct for most, catastrophic here - it makes the 200-rune title
// unbreakable and inflates the flex column past the viewport). The fix must
// override that nowrap LOCALLY in the welcome screen for the resume button,
// not globally in button.module.css. The resume button is the one PRIMARY
// button rendered inside .actions (the "Jump back in" candidate); the example
// buttons are variant="quiet" and already have their own .examples button
// override. This assertion specifically requires an override that applies to
// the resume button's context, not merely the pre-existing .examples one.
test("welcome.module.css overrides white-space: nowrap for the resume-candidate button", () => {
  const css = welcomeCss();
  // The resume button is a primary Button rendered as a direct child of
  // .actions. An override scoped to .actions (covering its button children)
  // is what reaches the resume button; .examples button does NOT, because
  // the resume button is a sibling of .examples, not a descendant of it.
  // Require a wrapping white-space value to appear in a rule that can reach
  // the resume button - i.e. scoped under .actions, not only under .examples.
  const actionsBlock = css.match(/\.actions\s*\{([^}]*)\}/);
  expect(actionsBlock, "welcome.module.css must define an .actions rule").toBeTruthy();
  // Either .actions itself carries the override, or a descendant rule like
  // ".actions button" / ".actions .primary" does. Collect every rule whose
  // selector can match a primary button that is a direct child of .actions.
  const actionsScoped = css
    .split(/(?<=\})/)
    .filter(
      (rule) =>
        /\.actions\s+button\b/.test(rule) ||
        /\.actions\s+\.primary\b/.test(rule) ||
        /\.actions\b[^{]*\{[^}]*white-space/.test(rule),
    )
    .join("\n");
  const candidate = actionsScoped || actionsBlock?.[1] || "";
  expect(candidate, "welcome.module.css must override white-space for the resume button under .actions").toMatch(
    /white-space:\s*(normal|break-spaces|pre-wrap)/,
  );
  // And this file must not carry a `white-space: nowrap` of its own that would
  // re-apply to the resume button.
  expect(css).not.toMatch(/white-space:\s*nowrap/);
});

// Issue #197 symptom: the inflated resume button cascades into .examples and
// .hints, whose `width: min(100%, 36rem)` resolves its 100% against the
// now-inflated .actions. The fix must cap the .actions / title containers so
// wide content wraps instead of inflating the flex layout. This asserts the
// containers that hold the title and examples/hints carry a max-width (or
// equivalent width cap) plus overflow-wrap so long unbroken text wraps.
test("welcome.module.css caps width and wraps long text in the title/examples/hints containers", () => {
  const css = welcomeCss();
  // .actions is the flex column holding the resume button, the orientation
  // paragraph, .examples and .hints. It must cap its own width so an
  // unbreakable child cannot inflate it past the viewport.
  const actionsBlock = css.match(/\.actions\s*\{([^}]*)\}/);
  expect(actionsBlock, "welcome.module.css must define an .actions rule").toBeTruthy();
  const actionsRules = actionsBlock?.[1] ?? "";
  expect(actionsRules, ".actions must cap its width so a wide child cannot inflate it").toMatch(
    /max-width|min-width:\s*0|width:\s*min\(/,
  );
  expect(actionsRules, ".actions must let long words wrap").toMatch(
    /overflow-wrap:\s*break-word|word-break:\s*break-word/,
  );
});
