import { describe, expect, it } from "vitest";
import {
  LocationRepository,
  locationForRoute,
  restoredStack,
} from "./location";

function storage() {
  const values = new Map<string, string>();
  return {
    getItemSync: (key: string) => values.get(key) ?? null,
    setItemSync: (key: string, value: string) => {
      values.set(key, value);
    },
    removeItemSync: (key: string) => {
      values.delete(key);
    },
  };
}
describe("last mobile location", () => {
  it("restores the exact saved hub and conversation through a new repository", () => {
    const disk = storage();
    const location = {
      hubId: "studio",
      conversation: { ref: "same/ref", title: "Build 🛠" },
    };
    new LocationRepository(disk).save(location);
    expect(new LocationRepository(disk).read(["studio", "other"])).toEqual(
      location,
    );
    expect(new LocationRepository(disk).read(["other"])).toBeNull();
    const stack = restoredStack(location);
    expect(stack.index).toBe(2);
    expect(stack.routes.map((route) => route.name)).toEqual([
      "Hubs",
      "Sessions",
      "Conversation",
    ]);
    expect(stack.routes[2]?.params).toEqual({
      hubId: "studio",
      ref: "same/ref",
      title: "Build 🛠",
    });
  });
  it("returning to Hubs clears the saved destination", () => {
    const repo = new LocationRepository(storage());
    repo.save({ hubId: "studio" });
    repo.save(locationForRoute({ name: "Hubs" }, "studio"));
    expect(repo.read(["studio"])).toBeNull();
    expect(restoredStack(null)).toEqual({
      index: 0,
      routes: [{ name: "Hubs" }],
    });
  });
  it("never restores an action form or a conversation from another selected hub", () => {
    expect(
      locationForRoute(
        { name: "NewSession", params: { hubId: "studio" } },
        "studio",
      ),
    ).toEqual({ hubId: "studio" });
    expect(
      locationForRoute(
        {
          name: "Conversation",
          params: { hubId: "other", ref: "same/ref", title: "Other" },
        },
        "studio",
      ),
    ).toBeNull();
    expect(locationForRoute({ name: "Sessions" }, null)).toBeNull();
  });
  it("rejects malformed or unsupported persisted values", () => {
    for (const raw of [
      "{",
      "null",
      "[]",
      '{"hubId":4}',
      '{"hubId":"studio","conversation":{"ref":42}}',
    ]) {
      const repo = new LocationRepository({
        ...storage(),
        getItemSync: () => raw,
      });
      expect(repo.read(["studio"])).toBeNull();
    }
  });
  it("propagates actual storage failures so the UI can explain them", () => {
    const failure = new Error("disk unavailable");
    const repo = new LocationRepository({
      ...storage(),
      getItemSync: () => {
        throw failure;
      },
    });
    expect(() => repo.read(["studio"])).toThrow(failure);
  });
});
