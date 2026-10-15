import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { Button } from "../../widgets";
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
  resetNavigationStoreForTests();
});

function node(overrides: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
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

function setLive(rows: NavigationSessionSummary[]): void {
  const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  navigationStore.setState({
    visibleResourceKeys: new Set([keyID(key)]),
    resources: new Map([
      [
        keyID(key),
        {
          key,
          data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
          loadedRevision: 1,
          targetRevision: null,
          forceToken: 0,
          etag: '"test"',
          loading: false,
          stale: false,
          error: null,
          generationID: "generation_test",
        },
      ],
    ]),
  });
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
  setLive([node({ ref: "local:long1", title: LONG_TITLE })]);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  const button = screen.getByRole("button", { name: /Jump back in/ });
  expect(button.textContent).toContain(LONG_TITLE);
});

// Issue #197 root cause: Button keeps its label on one line, which made the
// 200-rune title unbreakable and inflated the flex column past the viewport.
// The resume button opts into Button's own `wrap` (the layoutguard case
// welcome-resume-candidate-overflow measures the geometry); the welcome
// stylesheet must not reach into Button to do it locally.
test("the resume-candidate button is a wrapping Button, with no local override of Button's look", () => {
  setLive([node({ ref: "local:long1", title: LONG_TITLE })]);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  const resumeClass = screen.getByRole("button", { name: /Jump back in/ }).className;
  cleanup();
  render(
    <Button variant="primary" wrap>
      reference
    </Button>,
  );
  expect(resumeClass).toBe(screen.getByRole("button").className);
  expect(welcomeCss()).not.toMatch(/\bbutton\b[^{}]*\{/);
});

// Issue #197 symptom: the inflated resume button cascades into .hints, whose
// `width: min(100%, 36rem)` resolves its 100% against the now-inflated .welcomeActions.
// The fix must cap the .welcomeActions / title containers so wide content wraps
// instead of inflating the flex layout. This asserts the containers that hold
// the title and hints carry a max-width (or equivalent width cap) plus
// overflow-wrap so long unbroken text wraps.
test("welcome.module.css caps width and wraps long text in the title/hints containers", () => {
  const css = welcomeCss();
  // .welcomeActions is the flex column holding the resume button and .hints. It must
  // cap its own width so an unbreakable child cannot inflate it past the
  // viewport.
  const actionsBlock = css.match(/\.welcomeActions\s*\{([^}]*)\}/);
  expect(actionsBlock, "welcome.module.css must define a .welcomeActions rule").toBeTruthy();
  const actionsRules = actionsBlock?.[1] ?? "";
  expect(actionsRules, ".welcomeActions must cap its width so a wide child cannot inflate it").toMatch(
    /max-width|min-width:\s*0|width:\s*min\(/,
  );
  expect(actionsRules, ".welcomeActions must let long words wrap").toMatch(
    /overflow-wrap:\s*break-word|word-break:\s*break-word/,
  );
});
