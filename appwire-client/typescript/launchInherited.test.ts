// @vitest-environment node
import { describe, expect, test } from "vitest";
import { asEnvEntries, asEnvObjects, asMcpList, asStringList, inheritedItems } from "./launchInherited";

describe("inheritedItems", () => {
  test("subtracts the local entries from the effective value by key", () => {
    expect(inheritedItems(["/a", "/b", "/c"], ["/b"], (item) => item, asStringList)).toEqual(["/a", "/c"]);
  });
  test("is empty when the effective value is absent or the wrong shape", () => {
    expect(inheritedItems(undefined, [], (item: string) => item, asStringList)).toEqual([]);
    expect(inheritedItems({ not: "a list" }, [], (item: string) => item, asStringList)).toEqual([]);
  });
  test("keys env entries by name, so a locally overridden variable is not inherited", () => {
    const effective = { HOME: "/home/me", EDITOR: "vim" };
    const local: [string, string][] = [["EDITOR", "nano"]];
    expect(inheritedItems(effective, local, ([name]) => name, asEnvEntries)).toEqual([["HOME", "/home/me"]]);
  });
});

describe("shape adapters", () => {
  test("asStringList accepts only arrays", () => {
    expect(asStringList(["x"])).toEqual(["x"]);
    expect(asStringList("x")).toEqual([]);
  });
  test("asEnvEntries accepts only a plain object", () => {
    expect(asEnvEntries({ A: "1" })).toEqual([["A", "1"]]);
    expect(asEnvEntries(["A=1"])).toEqual([]);
    expect(asEnvEntries(null)).toEqual([]);
  });
  test("asEnvObjects reshapes the entries as {name, value}", () => {
    expect(asEnvObjects({ A: "1" })).toEqual([{ name: "A", value: "1" }]);
  });
  test("asMcpList requires every item to be a named object", () => {
    expect(asMcpList([{ name: "fs", command: "mcp-fs" }])).toEqual([{ name: "fs", command: "mcp-fs" }]);
    expect(asMcpList([{ command: "mcp-fs" }])).toEqual([]);
    expect(asMcpList("fs")).toEqual([]);
  });
});
