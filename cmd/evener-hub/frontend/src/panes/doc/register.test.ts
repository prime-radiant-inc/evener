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

// A doc parseParams is what lets a persisted layout carry an explicit opening
// origin while rejecting a malformed one outright (never silently downgraded to
// the older origin-less shape). The registry reads parseParams only during
// restoreLayout (workspace.ts:readPanelParams); these tests drive it directly.
test("the doc pane exposes a parseParams that accepts the current shape, the approved older shape, and valid origin", () => {
  const parse = paneFor("doc").parseParams;
  if (parse === undefined) throw new Error("doc pane has no parseParams (pending source seam)");
  // The approved older shape (no origin) stays readable.
  expect(parse({ session: "s1", path: "notes.md", kind: "file" })).toEqual({
    session: "s1",
    path: "notes.md",
    kind: "file",
  });
  // Both new serialized origin shapes are validated, not just accepted blindly.
  expect(
    parse({
      session: "s1",
      path: "notes.md",
      kind: "file",
      origin: { kind: "browser-origin", paneId: "owner-pane", compositionOwnerId: "owner-A" },
    }),
  ).toEqual({
    session: "s1",
    path: "notes.md",
    kind: "file",
    origin: { kind: "browser-origin", paneId: "owner-pane", compositionOwnerId: "owner-A" },
  });
  expect(
    parse({
      session: "s1",
      path: "notes.md",
      kind: "image",
      origin: { kind: "browser-readonly-session", sourcePaneId: "source-pane" },
    }),
  ).toEqual({
    session: "s1",
    path: "notes.md",
    kind: "image",
    origin: { kind: "browser-readonly-session", sourcePaneId: "source-pane" },
  });
});

test("the doc parseParams rejects a malformed supplied origin instead of treating it as absent legacy data", () => {
  const parse = paneFor("doc").parseParams;
  if (parse === undefined) throw new Error("doc pane has no parseParams (pending source seam)");
  expect(parse({ session: "s1", path: "notes.md", kind: "file", origin: { kind: "bogus" } })).toBeNull();
  expect(
    parse({ session: "s1", path: "notes.md", kind: "file", origin: { kind: "browser-origin", paneId: "p" } }),
  ).toBeNull();
  expect(
    parse({
      session: "s1",
      path: "notes.md",
      kind: "file",
      origin: { kind: "browser-readonly-session", sourcePaneId: "" },
    }),
  ).toBeNull();
  expect(parse({ session: "s1", path: "notes.md", kind: "file", origin: "not-an-object" })).toBeNull();
});

test("the doc parseParams rejects a params bag missing the required session/path/kind shape", () => {
  const parse = paneFor("doc").parseParams;
  if (parse === undefined) throw new Error("doc pane has no parseParams (pending source seam)");
  expect(parse(null)).toBeNull();
  expect(parse({})).toBeNull();
  expect(parse({ session: "s1", path: "notes.md" })).toBeNull();
});
