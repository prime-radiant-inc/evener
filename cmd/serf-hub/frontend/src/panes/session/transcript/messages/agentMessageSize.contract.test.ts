// Contract test for the tiered-density spec (docs/web-ui/specs/
// 2026-07-27-transcript-tiered-density-design.md, ratification item 1):
// agent prose wins on CONTRAST (ink-hi vs the user's ink-mid), not on size.
// The 16px pane-title override fired on every narrative fragment, dozens
// per session, cancelling the signal it was meant to be.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "agentmessageitem.module.css"), "utf8");

test("agent prose is not size-promoted above body text", () => {
  expect(css).not.toContain("--prose-font-size");
});
