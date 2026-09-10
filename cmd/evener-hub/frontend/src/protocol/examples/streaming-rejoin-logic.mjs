import assert from "node:assert/strict";

// A small window makes fragment paging observable on modest fixtures.
const itemLimit = 20;

export function inspectPage(turns) {
  const items = turns.flatMap((turn) => turn.items ?? []);
  const keys = new Set();
  let positioned = 0;
  let boundaries = 0;
  let previous;
  for (const turn of turns) if (turn.hasEarlierItems === true || turn.hasLaterItems === true) boundaries += 1;
  for (const item of items) {
    if (item.transcriptKey) {
      assert(!keys.has(item.transcriptKey), `duplicate transcript key ${item.transcriptKey}`);
      keys.add(item.transcriptKey);
    }
    if (!item.position) continue;
    positioned += 1;
    assert.ok(
      Number.isSafeInteger(item.position.entry) && item.position.entry >= 0,
      "entry positions include the system prelude at zero",
    );
    assert.ok(
      Number.isSafeInteger(item.position.item) && item.position.item >= 0,
      "fragment item positions are non-negative",
    );
    const current = [item.position.entry, item.position.item];
    if (previous)
      assert.ok(
        current[0] > previous[0] || (current[0] === previous[0] && current[1] >= previous[1]),
        "items are ordered by transcript position",
      );
    previous = current;
  }
  return { items: items.length, stableKeys: keys.size, positioned, boundaries };
}

export function reconcilePage(current, older) {
  const byKey = new Map();
  for (const item of [...older, ...current]) {
    const key =
      item.transcriptKey ?? (item.position ? `position:${item.position.entry}:${item.position.item}` : Symbol());
    byKey.set(key, item);
  }
  return [...byKey.values()].sort((left, right) => {
    const a = left.position ?? { entry: Number.MAX_SAFE_INTEGER, item: 0 };
    const b = right.position ?? { entry: Number.MAX_SAFE_INTEGER, item: 0 };
    return a.entry - b.entry || a.item - b.item;
  });
}

export async function recoverAfterStaleCursor(hub, ref, cursor) {
  try {
    return { page: await hub.request("thread/turns/list", { ref, cursor, itemsView: "fragment", itemLimit }) };
  } catch (error) {
    if (error?.evenerErrorInfo !== "transcriptItemCursorStale") throw error;
    return {
      snapshot: await hub.request("thread/read", {
        ref,
        includeTurns: true,
        itemsView: "fragment",
        itemLimit,
        subscribe: true,
        replaceSubscription: true,
      }),
    };
  }
}

const transcriptNotifications = new Set([
  "item/started",
  "item/completed",
  "item/agentMessage/delta",
  "item/agentMessage/reset",
  "evener/thread/resync",
  "turn/completed",
]);
const items = (snapshot) => snapshot.thread.turns.flatMap((turn) => turn.items ?? []);

// This bounded recipe reconciles a snapshot after the observation window.
// It does not render token deltas or implement a continuously live UI reducer.
export async function runReadRecipe(hub, ref, observe = async () => {}) {
  assert.ok(typeof ref === "string" && ref.length > 0, "Use an owned session ref for this recipe");
  const notifications = new Set();
  const stop = hub.onNotification((event) => {
    if (event.params?.ref === ref && transcriptNotifications.has(event.method)) notifications.add(event.method);
  });
  let instanceId;
  let subscribed = false;
  let primaryError;
  let cleanupError;
  let result;
  const validate = (snapshot) => {
    assert.equal(snapshot.thread.evener.ref, ref, "readback changed session identity");
    assert.ok(snapshot.thread.evener.instanceId, "this recipe requires an active session instance");
    if (instanceId)
      assert.equal(snapshot.thread.evener.instanceId, instanceId, "session instance changed; reopen explicitly");
    instanceId = snapshot.thread.evener.instanceId;
    inspectPage(snapshot.thread.turns);
    return snapshot;
  };
  const read = async (replaceSubscription) => {
    subscribed = true;
    return validate(
      await hub.request("thread/read", {
        ref,
        includeTurns: true,
        itemsView: "fragment",
        itemLimit,
        subscribe: true,
        ...(replaceSubscription ? { replaceSubscription: true } : {}),
      }),
    );
  };
  try {
    let snapshot = await read(false);
    const first = inspectPage(snapshot.thread.turns);
    let reconciledItems = items(snapshot);
    let paged = false;
    let staleCursorRecovered = false;
    if (snapshot.olderCursor) {
      const result = await recoverAfterStaleCursor(hub, ref, snapshot.olderCursor);
      if (result.snapshot) {
        snapshot = validate(result.snapshot);
        reconciledItems = items(snapshot);
        staleCursorRecovered = true;
      } else {
        inspectPage(result.page.data);
        reconciledItems = reconcilePage(
          reconciledItems,
          result.page.data.flatMap((turn) => turn.items ?? []),
        );
        paged = true;
      }
    }
    await observe();
    if (notifications.size) {
      snapshot = await read(true);
      // A reset/resync invalidates the retained window, including older pages.
      reconciledItems = items(snapshot);
    }
    subscribed = false;
    await hub.request("thread/unsubscribe", { ref });
    const rejoined = await read(true);
    result = {
      first,
      paged,
      staleCursorRecovered,
      reconciledItems,
      rejoinedItems: items(rejoined),
      notifications: [...notifications].sort(),
    };
  } catch (error) {
    primaryError = error;
  } finally {
    stop();
    if (subscribed) {
      try {
        await hub.request("thread/unsubscribe", { ref });
      } catch (error) {
        cleanupError = error;
      }
    }
  }
  if (primaryError && cleanupError)
    throw new AggregateError([primaryError, cleanupError], "Read workflow and subscription cleanup both failed");
  if (primaryError) throw primaryError;
  if (cleanupError) throw cleanupError;
  return result;
}
