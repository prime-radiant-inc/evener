import { expect, test } from "vitest";
import { EXPANSION_STORAGE_KEY } from "../../shell/rail/railExpansion";
import { railExpansionPersistence } from "./persistence";

test("the web port is the rail's one localStorage blob", () => {
  railExpansionPersistence.writeExpansion(new Map([["projectnode:p", true]]));
  expect(JSON.parse(localStorage.getItem(EXPANSION_STORAGE_KEY) ?? "null")).toEqual({ "projectnode:p": true });
  expect([...railExpansionPersistence.readExpansion()]).toEqual([["projectnode:p", true]]);
  localStorage.removeItem(EXPANSION_STORAGE_KEY);
});
