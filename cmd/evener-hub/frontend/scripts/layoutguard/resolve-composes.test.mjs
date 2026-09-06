import { describe, expect, test } from "vitest";
import { resolveComposes, unwrapGlobal } from "./resolve-composes.mjs";

describe("unwrapGlobal", () => {
  test("unwraps simple element and class inners verbatim", () => {
    expect(unwrapGlobal(".a > :global(span) { x: y; }")).toBe(".a > span { x: y; }");
    expect(unwrapGlobal(".a :global(button) { x: y; }")).toBe(".a button { x: y; }");
    expect(unwrapGlobal(".a :global(.foo) { x: y; }")).toBe(".a .foo { x: y; }");
  });

  test("leaves CSS without :global untouched", () => {
    expect(unwrapGlobal(".a { x: y; }")).toBe(".a { x: y; }");
  });

  test("throws on nested parens instead of passing them through silently", () => {
    // The regex cannot cross the inner parens, so without the leftover
    // check the :global( text would survive into the harness stylesheet,
    // where the browser drops the whole selector and the guard passes
    // without testing the rule.
    expect(() => unwrapGlobal(".a :global(:not(.x)) { x: y; }")).toThrow(/:global/);
  });
});

describe("resolveComposes", () => {
  test("unwraps :global while still resolving composes", () => {
    const out = resolveComposes([
      ".base { color: red; }",
      '.a { composes: base from "./b.module.css"; }\n.b > :global(span) { x: y; }',
    ]);
    expect(out).toContain(".b > span");
    expect(out).not.toContain(":global(");
  });
});
