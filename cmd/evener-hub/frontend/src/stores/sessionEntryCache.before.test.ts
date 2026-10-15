import { IDBIndex } from "fake-indexeddb";
import { expect, test, vi } from "vitest";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";

test("getBefore reads only a bounded ascending page below the exact position in one history", async () => {
  const fixture = createEntryCacheFixture();
  const rows = Array.from({ length: 225 }, (_, index) => ({
    historyKey: "history-a",
    transcriptKey: `row-${index + 1}`,
    position: { entry: index + 1, item: 0 },
    version: index + 1,
    item: { id: `row-${index + 1}`, turnId: "turn-a", type: "assistant", text: `row-${index + 1}` },
  }));
  try {
    await fixture.cache.putItems(rows);
    await fixture.cache.putItems([{ ...rows[0]!, historyKey: "history-b" }]);
    const openCursor = IDBIndex.prototype.openCursor;
    let visited = 0;
    vi.spyOn(IDBIndex.prototype, "openCursor").mockImplementation(function (this: IDBIndex, ...args) {
      const request = openCursor.apply(this, args);
      request.addEventListener("success", () => {
        if (request.result) visited++;
      });
      return request;
    });
    expect(await fixture.cache.getBefore("history-a", { entry: 26, item: 0 }, 10)).toEqual(rows.slice(15, 25));
    expect(visited).toBe(10);
    vi.restoreAllMocks();
    expect(await fixture.cache.getBefore("history-a", { entry: 16, item: 0 }, 200)).toEqual(rows.slice(0, 15));
    expect(await fixture.cache.getBefore("history-b", { entry: 2, item: 0 }, 200)).toEqual([
      { ...rows[0]!, historyKey: "history-b" },
    ]);
    expect(await fixture.cache.getBefore("history-a", { entry: 0, item: 0 }, 10)).toEqual([]);
    expect(await fixture.cache.getBefore("history-a", { entry: 26, item: 0 }, 0)).toEqual([]);
    await expect(fixture.cache.getBefore("history-a", { entry: -1, item: 0 }, 10)).rejects.toThrow(RangeError);
    await expect(fixture.cache.getBefore("history-a", { entry: 26, item: 0 }, -1)).rejects.toThrow(RangeError);
  } finally {
    vi.restoreAllMocks();
    await fixture.dispose();
  }
});

test("getBefore distinguishes subpositions and never certifies raw stored bodies", async () => {
  const fixture = createEntryCacheFixture();
  const rows = [0, 1, 2].map((sub) => ({
    historyKey: "history-a",
    transcriptKey: `sub-${sub}`,
    position: { entry: 5, item: 1, sub },
    version: 5,
    item: { id: `sub-${sub}`, turnId: "turn-a", type: "assistant", text: `sub-${sub}` },
  }));
  try {
    await fixture.cache.putItems(rows);
    expect(await fixture.cache.getBefore("history-a", { entry: 5, item: 1, sub: 2 }, 20)).toEqual(rows.slice(0, 2));
    expect(await fixture.cache.getBefore("history-a", { entry: 5, item: 1 }, 20)).toEqual([]);
    expect(await fixture.cache.getLatestWindow("history-a", 20)).toMatchObject({ coverage: [], complete: false });
  } finally {
    await fixture.dispose();
  }
});
