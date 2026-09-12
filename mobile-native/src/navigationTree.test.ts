import { expect, it } from "vitest";
import { navigationTree } from "./navigationTree";

interface Row {
  ref: string;
  children: Row[];
}
const leaf = (ref: string): Row => ({ ref, children: [] });
it("expands only requested branches in server order", () => {
  const grand = leaf("grand");
  const child = { ref: "child", children: [grand] };
  const parent = { ref: "parent", children: [child, leaf("sibling")] };
  const rows = [parent, leaf("other")];
  expect(
    navigationTree(
      rows,
      (r) => r.ref,
      (r) => r.children,
      new Set(),
    ).map((r) => r.item.ref),
  ).toEqual(["parent", "other"]);
  expect(
    navigationTree(
      rows,
      (r) => r.ref,
      (r) => r.children,
      new Set(["parent"]),
    ).map((r) => [r.item.ref, r.depth]),
  ).toEqual([
    ["parent", 0],
    ["child", 1],
    ["sibling", 1],
    ["other", 0],
  ]);
  expect(
    navigationTree(
      rows,
      (r) => r.ref,
      (r) => r.children,
      new Set(["parent", "child"]),
    ).map((r) => r.item.ref),
  ).toEqual(["parent", "child", "grand", "sibling", "other"]);
});
it("avoids duplicate destinations and repeated ancestor references", () => {
  const root: Row = leaf("root");
  root.children = [root, leaf("child")];
  expect(
    navigationTree(
      [root, leaf("child")],
      (r) => r.ref,
      (r) => r.children,
      new Set(["root"]),
    ).map((r) => r.item.ref),
  ).toEqual(["root", "child"]);
});
