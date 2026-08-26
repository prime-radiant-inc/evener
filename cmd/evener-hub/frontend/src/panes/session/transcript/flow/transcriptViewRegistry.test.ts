import { afterEach, expect, test, vi } from "vitest";
import {
  announceTranscriptViews,
  type CapturedTranscriptView,
  captureTranscriptViews,
  type RegisteredTranscriptView,
  registerTranscriptView,
  resetTranscriptViewRegistryForTests,
  restoreTranscriptViews,
  transitionTranscriptViews,
} from "./transcriptViewRegistry";

function captured(anchorId: string): CapturedTranscriptView {
  return {
    anchorId,
    anchorOffset: 10,
    normalizedOffset: 0.25,
    followingBottom: false,
    focusedEntryId: `${anchorId}-entry`,
  };
}

function view(id: string, snapshot: CapturedTranscriptView): RegisteredTranscriptView {
  return {
    id,
    capture: vi.fn(() => snapshot),
    restore: vi.fn(),
    focusDetailTrigger: vi.fn(),
    announce: vi.fn(),
  };
}

test("captures every pane before publishing, then restores and announces every pane", () => {
  const events: string[] = [];
  const left = view("left", captured("left-anchor"));
  const right = view("right", captured("right-anchor"));
  left.capture = vi.fn(() => {
    events.push("capture:left");
    return captured("left-anchor");
  });
  right.capture = vi.fn(() => {
    events.push("capture:right");
    return captured("right-anchor");
  });
  left.restore = vi.fn(() => events.push("restore:left"));
  right.restore = vi.fn(() => events.push("restore:right"));
  left.announce = vi.fn(() => events.push("announce:left"));
  right.announce = vi.fn(() => events.push("announce:right"));
  registerTranscriptView(left);
  registerTranscriptView(right);

  transitionTranscriptViews(() => events.push("publish"), "Transcript display changed");

  expect(events).toEqual([
    "capture:left",
    "capture:right",
    "publish",
    "restore:left",
    "restore:right",
    "announce:left",
    "announce:right",
  ]);
});

test("consumes one pane remount capture only for the matching target layout", () => {
  const snapshot = captured("remount-anchor");
  const first = view("pane", snapshot);
  first.layout = "desktop";
  const unregister = registerTranscriptView(first);

  transitionTranscriptViews(() => {}, "Transcript display changed", {
    fingerprint: "mobile-config",
    targetLayout: "mobile",
  });
  unregister();

  const replacement = view("pane", captured("replacement-anchor"));
  replacement.layout = "mobile";
  registerTranscriptView(replacement);

  expect(replacement.restore).toHaveBeenCalledOnce();
  expect(replacement.restore).toHaveBeenCalledWith(snapshot);
});

afterEach(() => {
  resetTranscriptViewRegistryForTests();
});

test("captures the currently registered views from two panes", () => {
  const leftSnapshot = captured("left-anchor");
  const rightSnapshot = captured("right-anchor");
  const left = view("left", leftSnapshot);
  const right = view("right", rightSnapshot);
  const unregisterLeft = registerTranscriptView(left);
  registerTranscriptView(right);

  const snapshot = captureTranscriptViews();

  expect(snapshot).toEqual(
    new Map([
      ["left", leftSnapshot],
      ["right", rightSnapshot],
    ]),
  );
  expect(left.capture).toHaveBeenCalledOnce();
  expect(right.capture).toHaveBeenCalledOnce();

  unregisterLeft();
  expect(snapshot.size).toBe(2);
  expect(captureTranscriptViews()).toEqual(new Map([["right", rightSnapshot]]));
});

test("replacing a duplicate id makes the stale unregister callback harmless", () => {
  const first = view("pane", captured("first-anchor"));
  const replacementSnapshot = captured("replacement-anchor");
  const replacement = view("pane", replacementSnapshot);
  const unregisterFirst = registerTranscriptView(first);
  const unregisterReplacement = registerTranscriptView(replacement);

  unregisterFirst();

  expect(captureTranscriptViews()).toEqual(new Map([["pane", replacementSnapshot]]));
  expect(replacement.capture).toHaveBeenCalledOnce();

  unregisterReplacement();
  expect(captureTranscriptViews()).toEqual(new Map());
});

test("captures before a test-local publish callback runs", () => {
  const events: string[] = [];
  const pane = view("pane", captured("before-publish"));
  pane.capture = vi.fn(() => {
    events.push("capture");
    return captured("before-publish");
  });
  registerTranscriptView(pane);

  const publish = () => {
    events.push("publish");
  };
  const captureBeforePublish = (publishCallback: () => void) => {
    const snapshot = captureTranscriptViews();
    publishCallback();
    return snapshot;
  };

  const snapshot = captureBeforePublish(publish);

  expect(events).toEqual(["capture", "publish"]);
  expect(snapshot.get("pane")?.anchorId).toBe("before-publish");
});

test("restores only views that remain registered under each captured id", () => {
  const left = view("left", captured("left-anchor"));
  const right = view("right", captured("right-anchor"));
  const unregisterRight = registerTranscriptView(right);
  registerTranscriptView(left);
  const snapshot = new Map([
    ["left", captured("saved-left")],
    ["right", captured("saved-right")],
    ["missing", captured("saved-missing")],
  ]);

  unregisterRight();
  restoreTranscriptViews(snapshot);

  expect(left.restore).toHaveBeenCalledOnce();
  expect(left.restore).toHaveBeenCalledWith(snapshot.get("left"));
  expect(right.restore).not.toHaveBeenCalled();
});

test("announces once to every currently registered view", () => {
  const left = view("left", captured("left-anchor"));
  const right = view("right", captured("right-anchor"));
  const unregisterLeft = registerTranscriptView(left);
  registerTranscriptView(right);

  announceTranscriptViews("Transcript display changed");
  unregisterLeft();
  announceTranscriptViews("Transcript display changed again");

  expect(left.announce).toHaveBeenCalledOnce();
  expect(left.announce).toHaveBeenCalledWith("Transcript display changed");
  expect(right.announce).toHaveBeenCalledTimes(2);
  expect(right.announce).toHaveBeenNthCalledWith(1, "Transcript display changed");
  expect(right.announce).toHaveBeenNthCalledWith(2, "Transcript display changed again");
});

test("isolates a thrown capture and restore from the other views", () => {
  const safeSnapshot = captured("safe-anchor");
  const safe = view("safe", safeSnapshot);
  const throwing: RegisteredTranscriptView = {
    ...view("throwing", captured("throwing-anchor")),
    capture: vi.fn(() => {
      throw new Error("capture failed");
    }),
    restore: vi.fn(() => {
      throw new Error("restore failed");
    }),
  };
  registerTranscriptView(throwing);
  registerTranscriptView(safe);

  expect(() => captureTranscriptViews()).not.toThrow();
  expect(captureTranscriptViews()).toEqual(new Map([["safe", safeSnapshot]]));

  const savedSafeSnapshot = captured("saved-safe");
  expect(() =>
    restoreTranscriptViews(
      new Map([
        ["throwing", captured("saved-throwing")],
        ["safe", savedSafeSnapshot],
      ]),
    ),
  ).not.toThrow();
  expect(safe.restore).toHaveBeenCalledWith(savedSafeSnapshot);
});

test("reset removes every registered view", () => {
  const pane = view("pane", captured("anchor"));
  const unregister = registerTranscriptView(pane);

  resetTranscriptViewRegistryForTests();

  expect(captureTranscriptViews()).toEqual(new Map());
  announceTranscriptViews("ignored");
  expect(pane.announce).not.toHaveBeenCalled();
  unregister();
  expect(captureTranscriptViews()).toEqual(new Map());
});
