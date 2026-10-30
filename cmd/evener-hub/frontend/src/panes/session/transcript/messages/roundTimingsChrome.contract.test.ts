import { expect, test } from "vitest";
import { readModuleCss } from "../../../../styles/cssBlock";

const css = readModuleCss(import.meta.url, "systemnoticeitem.module.css");

test("round timings have no rule of their own - the plain shared line renders them", () => {
  // Any .timings* selector means the disclosure (or some new round-timings
  // chrome) is back. The flat line needs no rule beyond the shared .line.
  expect(css).not.toMatch(/\.timings\w*\s*\{/);
});
