import { expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";
// Import the OPENER, not ./index directly: every doc-pane producer imports
// openDoc, whose side-effect import of ./index registers the type. This guards
// the real wiring path (the pane resolving when openDocBeside routes it), not
// just index.tsx in isolation.
import "./openDoc";

test("importing the doc opener registers the doc pane type so openDocBeside resolves it", () => {
  expect(paneFor("doc").id).toBe("doc");
});

test("titles a doc tab with the file's basename, not the full path", () => {
  expect(paneFor("doc").title({ session: "s1", path: "src/panes/doc/DocPane.tsx", kind: "file" }, {})).toBe(
    "DocPane.tsx",
  );
});
