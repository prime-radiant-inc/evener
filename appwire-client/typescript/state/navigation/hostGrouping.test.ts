import { expect, test } from "vitest";
import type { Source } from "../../types.gen";
import { canonicalHostId, orderedHosts, projectHostIds } from "./hostGrouping";

const source = (id: string, label: string, online = true): Source => ({
  id,
  label,
  kind: id === "local" ? "local" : "appwire",
  online,
});
const row = (host_id: string) => ({ host_id });

test("orders this hub first, then online hosts by label, offline hosts last, and reads an unknown host as online", () => {
  const sources = [
    source("local", "this host"),
    source("zeta", "Zeta"),
    source("alpha", "Alpha"),
    source("down", "Aardvark", false),
  ];
  expect(orderedHosts(["down", "zeta", "ghost", "alpha", "local", "zeta"], sources)).toEqual([
    { id: "local", label: "this host", online: true },
    { id: "alpha", label: "Alpha", online: true },
    { id: "ghost", label: "ghost", online: true },
    { id: "zeta", label: "Zeta", online: true },
    { id: "down", label: "Aardvark", online: false },
  ]);
});

test("a project's hosts are its sources plus its rows' hosts, and this hub when it names neither", () => {
  expect(projectHostIds(undefined, [])).toEqual(["local"]);
  expect(projectHostIds([], [])).toEqual(["local"]);
  expect(projectHostIds(["local", "paradise-park"], [])).toEqual(["local", "paradise-park"]);
  expect(projectHostIds(undefined, [row("local"), row("devbox"), row("local")])).toEqual(["local", "devbox"]);
  expect(projectHostIds(["paradise-park"], [row("paradise-park")])).toEqual(["paradise-park"]);
});

test("the canonical host is the first in display order with a loaded row, else the first", () => {
  const sources = [source("local", "this host"), source("paradise-park", "paradise-park")];
  const both = ["local", "paradise-park"];
  expect(canonicalHostId(both, [], sources)).toBe("local");
  expect(canonicalHostId(both, [row("paradise-park")], sources)).toBe("paradise-park");
  expect(canonicalHostId(both, [row("paradise-park"), row("local")], sources)).toBe("local");
});
