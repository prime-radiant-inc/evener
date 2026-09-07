// Contract test for the tiered-density spec (docs/web-ui/specs/
// 2026-07-27-transcript-tiered-density-design.md, ratification item 1):
// agent prose wins on CONTRAST (ink-hi vs the user's ink-mid), not on size.
// The 16px pane-title override fired on every narrative fragment, dozens
// per session, cancelling the signal it was meant to be.
//
// The slack-lean speaker header (2026-07-29-transcript-slack-lean-messages.
// md, decision 1) adds its own typography for the header's OWN words - the
// name at body size, the meta at caption - but prose typography stays
// untouched: the header does hierarchy with STRUCTURE (avatar row at
// exchange boundaries), not by resizing the message body.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8");
const uncommented = css.replace(/\/\*[\s\S]*?\*\//g, "");

test("agent prose is not size-promoted above body text", () => {
  expect(css).not.toContain("--prose-font-size");
});

test("prose typography stays with Markdown/StreamingText: the .message wrapper declares no font-size of its own", () => {
  expect(uncommented).not.toMatch(/\.message\s*\{[^}]*font-size\s*:/);
});

test("the speaker header's name is body-size like the prose it introduces; only the meta drops to the ui size", () => {
  expect(uncommented).toMatch(/\.name\s*\{[^}]*font-size:\s*var\(--font-size-body\);/);
  // ui, not caption: the model label and clock are read, not glanced past
  // (typography-spacing-critique-2026-09-06 finding 7).
  expect(uncommented).toMatch(/\.meta\s*\{[^}]*font-size:\s*var\(--font-size-ui\);/);
});
