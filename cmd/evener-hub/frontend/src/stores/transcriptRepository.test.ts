import { afterEach, expect, it } from "vitest";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { TranscriptRepository } from "./transcriptRepository";

const fixtures: ReturnType<typeof createEntryCacheFixture>[] = [];
afterEach(() => {
  for (const fixture of fixtures.splice(0)) fixture.dispose();
});

it("publishes recorded rows before persistence and keeps streaming overlay memory-only", async () => {
  const fixture = createEntryCacheFixture();
  fixtures.push(fixture);
  const repository = new TranscriptRepository(fixture.cache, "host/session/incarnation");
  const updates: number[] = [];
  repository.subscribe(() => updates.push(updates.length + 1));
  const row = {
    historyKey: "host/session/incarnation",
    transcriptKey: "entry-1/item-0",
    position: { entry: 1, item: 0 },
    version: 1,
    item: { id: "item-0", turnId: "turn-1", type: "message", text: "recorded", overlayKey: "turn-1/text" },
  };

  repository.setOverlay({ key: "turn-1/text", value: "streaming" });
  repository.acceptRecorded(row);
  expect(updates).toHaveLength(2);
  const reader = repository.attachReader();
  await expect(reader.openLocal(40)).resolves.toEqual({
    items: [row],
    overlay: [],
  });
  await repository.settled();
  const reopened = new TranscriptRepository(fixture.cache, row.historyKey);
  const reopenedReader = reopened.attachReader();
  await expect(reopenedReader.openLocal(40)).resolves.toEqual({ items: [row], overlay: [] });
  await expect(fixture.cache.getLatest(row.historyKey, 40)).resolves.toEqual([row]);
  reader.dispose();
  reopenedReader.dispose();
  repository.dispose();
  reopened.dispose();
});
