// The repository's half of boot generation adoption: a pre-cut `overlay/upserted`
// buffered across a source replacement belongs to the daemon that published it.
// Once a read has adopted a newer boot generation, a frame stamped with a
// generation that read retired must not enrich the replacement's cut, while the
// replacement's own pre-history frames and an older peer's unstamped frames
// still do. A successful `daemonless` read is saved-history provenance, so it
// must not erase the live generation this cut is comparing against.
import "fake-indexeddb/auto";
import type { AnyNotification, ThreadItem, ThreadReadResponse } from "@evener/appwire-client";
import { DAEMONLESS_BOOT_GENERATION } from "@evener/appwire-client";
import { afterEach, expect, test } from "vitest";
import { ALL_CAPABILITIES } from "../threadTestUtils";
import { entryHistoryKey, type TranscriptResolvedIdentity } from "./sessionCacheSchema";
import { createEntryCacheFixture } from "./testing/entryCacheFixture";
import { TranscriptRepository } from "./transcriptRepository";

type EntryCacheFixture = ReturnType<typeof createEntryCacheFixture>;
const fixtures: EntryCacheFixture[] = [];
const identity: TranscriptResolvedIdentity = {
  clientId: "client-a",
  hostId: "host-a",
  sessionId: "session-a",
  incarnation: "inc-a",
};
const ref = "ref-a";
const threadId = "thread-a";
const historyKey = entryHistoryKey(identity);

afterEach(() => {
  for (const fixture of fixtures.splice(0)) fixture.dispose();
});

function itemOf(entry: number, text: string): ThreadItem {
  return {
    type: "agentMessage",
    id: `item-${entry}`,
    transcriptKey: `item-${entry}`,
    turnId: "turn_1",
    position: { entry, item: 0 },
    version: entry,
    text,
  };
}

function read(bootGeneration: string): ThreadReadResponse {
  return {
    thread: {
      id: threadId,
      sessionId: identity.sessionId,
      preview: "",
      ephemeral: false,
      modelProvider: "test/provider",
      createdAt: 1,
      updatedAt: 1,
      status: { type: "idle" },
      cwd: "/tmp",
      cliVersion: "test",
      source: "evener",
      turns: [
        {
          id: "turn_1",
          itemsView: "full",
          status: "completed",
          version: 1,
          items: [itemOf(1, "read-row")],
        },
      ],
      evener: { ref, capabilities: ALL_CAPABILITIES, queue: { revision: 0 } },
    },
    bootGeneration,
    epoch: 0,
    snapshot: { incarnation: identity.incarnation, length: 1 },
    overlay: [],
  };
}

function upserted(key: string, bootGeneration?: string): AnyNotification {
  return {
    method: "overlay/upserted",
    params: {
      ref,
      threadId,
      ...(bootGeneration === undefined ? {} : { bootGeneration }),
      item: { key, kind: "stream", turnId: "turn_1", item: itemOf(2, key) },
    },
  } as AnyNotification;
}

function overlayKeys(owner: TranscriptRepository): string[] {
  return Object.keys(owner.getSnapshot()?.overlay ?? {}).sort();
}

function repository(fixture: EntryCacheFixture): TranscriptRepository {
  return new TranscriptRepository(fixture.cache, {
    ref,
    threadId,
    historyKey,
    identity,
    clearEpoch: 0,
    lifetime: 1,
    now: () => Date.now(),
  });
}

test("a pre-cut overlay from the retired generation is dropped and the replacement's is kept", () => {
  const fixture = createEntryCacheFixture("boot-generation-precut");
  fixtures.push(fixture);
  const owner = repository(fixture);
  // The replacement daemon boots as generation 2 and answers the read.
  expect(owner.acceptRead(read("2"), { lifetime: 1, readId: "read-1", boundary: "boundary-1" })).toBe(true);

  owner.acceptBeforeReadCut([upserted("departed", "1"), upserted("replacement", "2")], {
    lifetime: 1,
    adoptedRead: true,
  });

  expect(overlayKeys(owner)).toEqual(["replacement"]);
});

test("a pre-cut overlay from an older peer that names no generation is kept", () => {
  const fixture = createEntryCacheFixture("boot-generation-unstamped");
  fixtures.push(fixture);
  const owner = repository(fixture);
  expect(owner.acceptRead(read("2"), { lifetime: 1, readId: "read-1", boundary: "boundary-1" })).toBe(true);

  owner.acceptBeforeReadCut([upserted("unstamped")], { lifetime: 1, adoptedRead: true });

  expect(overlayKeys(owner)).toEqual(["unstamped"]);
});

test("a pre-cut overlay from a generation ahead of the adopted one is kept", () => {
  const fixture = createEntryCacheFixture("boot-generation-ahead");
  fixtures.push(fixture);
  const owner = repository(fixture);
  expect(owner.acceptRead(read("1"), { lifetime: 1, readId: "read-1", boundary: "boundary-1" })).toBe(true);

  // Only a generation the adopted read IGNORES is retired: a frame from a
  // generation ahead of it is not the departed source's.
  owner.acceptBeforeReadCut([upserted("ahead", "2")], { lifetime: 1, adoptedRead: true });

  expect(overlayKeys(owner)).toEqual(["ahead"]);
});

test("a post-cut overlay is admitted whatever generation it names", () => {
  const fixture = createEntryCacheFixture("boot-generation-live");
  fixtures.push(fixture);
  const owner = repository(fixture);
  expect(owner.acceptRead(read("2"), { lifetime: 1, readId: "read-1", boundary: "boundary-1" })).toBe(true);

  // The live path already refuses a retired frame in the reducer's own
  // ordering; this filter exists only for the buffered pre-cut window, so the
  // repository's ordinary `accept` keeps its existing contract.
  expect(owner.accept(upserted("live", "1"), { lifetime: 1 })).toBe(true);
  expect(overlayKeys(owner)).toEqual(["live"]);
});

test("a successful daemonless read keeps the live generation the cut compares against", () => {
  const fixture = createEntryCacheFixture("boot-generation-daemonless");
  fixtures.push(fixture);
  const owner = repository(fixture);
  expect(owner.acceptRead(read("1"), { lifetime: 1, readId: "read-1", boundary: "boundary-1" })).toBe(true);
  // The relay drops, and the transcript refresh answers from saved history. That
  // is provenance for the recorded history, not proof the daemon exited, so the
  // cut keeps generation 1 as the source its buffered frames are judged against.
  expect(
    owner.acceptRead(read(DAEMONLESS_BOOT_GENERATION), { lifetime: 1, readId: "read-2", boundary: "boundary-2" }),
  ).toBe(true);

  // A buffered frame the live generation provably predates is still retired: it
  // belongs to a daemon boot the source moved past. Had the daemonless token
  // replaced the baseline, nothing would retire it and the stale overlay would
  // enrich the cut.
  owner.acceptBeforeReadCut([upserted("departed", "0"), upserted("live", "1")], {
    lifetime: 1,
    adoptedRead: true,
  });

  expect(overlayKeys(owner)).toEqual(["live"]);
});
