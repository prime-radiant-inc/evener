import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

// A stylesheet-grep assertion must not be satisfiable by a comment - this repo
// has a precedent of exactly that passing while asserting nothing (see
// welcome.overflow.test.tsx's own stripCssComments).
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
}

const here = dirname(fileURLToPath(import.meta.url));
const pickerCss = () => stripCssComments(readFileSync(join(here, "directorypicker.module.css"), "utf8"));

// The recent sidebar's containment contract: the 180px column must never let a
// long path or an unbreakable basename escape into the browse column, and a
// long recents list must scroll inside the dialog instead of stretching it.
// jsdom cannot evaluate CSS geometry, so this pins the rules themselves the
// way welcome.overflow.test.tsx pins issue #197's wrap fix.
test("the recents sidebar contains and truncates its rows instead of overflowing the panel", () => {
  const css = pickerCss();

  // The sidebar scrolls internally rather than stretching the dialog past its
  // height cap.
  const recentsBlock = css.match(/\.recents\s*\{([^}]*)\}/);
  expect(recentsBlock, "directorypicker.module.css must define a .recents rule").toBeTruthy();
  expect(recentsBlock?.[1] ?? "").toMatch(/overflow-y:\s*auto/);

  // Rows stretch (never flex-start, which let fit-content lines escape the
  // sidebar) and clip anything a line-level rule misses.
  const rowBlock = css.match(/:where\(\.recents\)\s*\.row\s*\{([^}]*)\}/);
  expect(rowBlock, "the recents row rule must exist").toBeTruthy();
  expect(rowBlock?.[1] ?? "").toMatch(/align-items:\s*stretch/);
  expect(rowBlock?.[1] ?? "").not.toMatch(/flex-start/);
  expect(rowBlock?.[1] ?? "").toMatch(/overflow:\s*hidden/);

  // Each of the row's two lines ellipsizes inside the row's width.
  const truncationBlock = css.match(/:where\(\.recents\)\s*\.row\s*strong[^{]*\{([^}]*)\}/);
  expect(truncationBlock, "the recents line-truncation rule must exist").toBeTruthy();
  const truncation = truncationBlock?.[1] ?? "";
  expect(truncation).toMatch(/max-width:\s*100%/);
  expect(truncation).toMatch(/overflow:\s*hidden/);
  expect(truncation).toMatch(/text-overflow:\s*ellipsis/);
  expect(truncation).toMatch(/white-space:\s*nowrap/);

  // The shared .path rule itself must stay a wrapping rule: the footer
  // destination renders full paths and relies on overflow-wrap, not nowrap.
  const sharedPathBlock = css.match(/(?:^|\})\s*\.path\s*\{([^}]*)\}/);
  expect(sharedPathBlock, "the shared .path rule must exist").toBeTruthy();
  expect(sharedPathBlock?.[1] ?? "").not.toMatch(/white-space:\s*nowrap/);
  expect(sharedPathBlock?.[1] ?? "").toMatch(/overflow-wrap:\s*anywhere/);
});

// The scroll model: at the panel's height cap the body must stop growing and
// each column must scroll inside the browser row. Without this, a full
// 15-item recents list (~930px of single-line rows) is taller than the
// dialog's body budget on ordinary viewports, and the whole body scrolls
// while the sidebar's own scrollbar never engages - the dialog grows tall
// instead of the sidebar scrolling independently.
test("the picker's body caps the browser row so the columns scroll inside the dialog", () => {
  const css = pickerCss();

  const bodyBlock = css.match(/\.body\s*\{([^}]*)\}/);
  expect(bodyBlock, "the picker's .body rule must exist").toBeTruthy();
  expect(bodyBlock?.[1] ?? "").toMatch(/display:\s*flex/);
  expect(bodyBlock?.[1] ?? "").toMatch(/flex-direction:\s*column/);
  // auto, not hidden: a viewport too short for the 340px row floor must
  // scroll the body (the row's min-height wins over the shrink), not clip it.
  expect(bodyBlock?.[1] ?? "").toMatch(/overflow:\s*auto/);

  const browserBlock = css.match(/\.browser\s*\{([^}]*)\}/);
  expect(browserBlock, "the .browser rule must exist").toBeTruthy();
  expect(browserBlock?.[1] ?? "").toMatch(/flex:\s*1/);
  expect(browserBlock?.[1] ?? "").toMatch(/min-height:\s*340px/);

  const browseBlock = css.match(/\.browse\s*\{([^}]*)\}/);
  expect(browseBlock, "the .browse rule must exist").toBeTruthy();
  expect(browseBlock?.[1] ?? "").toMatch(/overflow-y:\s*auto/);

  // On phones the browser column stacks, so the recents block must be able to
  // shrink below its content (min-height 0) and scroll inside the dialog
  // instead of forcing its full content height and collapsing the browse pane.
  const mobileRecentsBlock = css.match(/@media[^{]*\{[\s\S]*?\.recents\s*\{([^}]*)\}/);
  expect(mobileRecentsBlock, "the phone-layout .recents rule must exist").toBeTruthy();
  expect(mobileRecentsBlock?.[1] ?? "").toMatch(/flex:\s*0 1 auto/);
  expect(mobileRecentsBlock?.[1] ?? "").toMatch(/min-height:\s*0/);
});
