import { describe, expect, test } from "vitest";
import { comicAuthorPersona, comicDelegateKey, comicDelegatePersona } from "./cast";

describe("delegate casting", () => {
  test("recorded agents use full qualified identity, never just a session suffix or display name", () => {
    expect(comicAuthorPersona(undefined)).toBeUndefined();
    expect(comicAuthorPersona({ kind: "unknown" })).toBeUndefined();
    const a = comicAuthorPersona({ kind: "agent", sessionRef: "remoteA:root" });
    const b = comicAuthorPersona({ kind: "agent", sessionRef: "remoteB:root" });
    expect(a).toEqual({ key: "remoteA:root", character: "xeno", name: "Xeno", badge: "remoteA:root" });
    expect(b?.key).toBe("remoteB:root");
    const original = {
      kind: "agent",
      sessionRef: "remoteA:child",
      ownerRef: "remoteA:root",
      delegateId: "dlg_original",
      delegateName: "Original",
    } as const;
    expect(comicAuthorPersona(original)).toMatchObject({
      key: '["remoteA:root","dlg_original"]',
      badge: "dlg_original",
      name: "Original",
    });
    expect(comicAuthorPersona({ ...original, sessionRef: "remoteA:resumed", delegateName: "Renamed" })?.key).toBe(
      '["remoteA:root","dlg_original"]',
    );
    expect(comicAuthorPersona({ ...original, ownerRef: "remoteB:root" })?.key).toBe('["remoteB:root","dlg_original"]');
  });

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
