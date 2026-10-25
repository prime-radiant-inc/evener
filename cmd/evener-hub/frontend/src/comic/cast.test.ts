import { describe, expect, test } from "vitest";
import { comicDelegateKey, comicDelegatePersona } from "./cast";

describe("delegate casting", () => {
  test("complete owner/delegate identity survives collisions and changing display data", () => {
    const owners = ["local:root", "remote.host:root"];
    const ids = ["dlg_a", "dlg_b", "dlg_c", "dlg_d", "dlg_e", "dlg_f"];
    const people = owners.flatMap((owner) => ids.map((id) => comicDelegatePersona(owner, id, "Same name")));
    expect(new Set(people.map((persona) => persona.key)).size).toBe(people.length);
    expect(comicDelegateKey("a:b", "c")).not.toBe(comicDelegateKey("a", "b:c"));
    expect(people.map((p) => p.badge)).toEqual([...ids, ...ids]);
    expect(people.map((p) => p.ownerRef)).toEqual(owners.flatMap((owner) => ids.map(() => owner)));
    expect(new Set(people.map((p) => p.character)).size).toBeLessThanOrEqual(4);
    for (const persona of people) {
      expect(["armando", "bolo", "denise", "tiki"]).toContain(persona.character);
      const id = persona.badge;
      const owner = persona.ownerRef!;
      expect(comicDelegatePersona(owner, id)).toMatchObject({
        key: persona.key,
        character: persona.character,
        badge: id,
        ownerRef: owner,
      });
      expect(comicDelegatePersona(owner, id, "Renamed")).toMatchObject({
        key: persona.key,
        character: persona.character,
      });
    }
    expect(people[0]?.name).toBe("Same name");
    expect(comicDelegatePersona("local:root", "dlg_a").name).toContain("dlg_a");
    expect([...people].reverse().map((p) => p.key)).toEqual(people.map((p) => p.key).reverse());
  });
});
