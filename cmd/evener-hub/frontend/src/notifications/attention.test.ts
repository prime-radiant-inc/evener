// @vitest-environment node

import type { NavigationSessionSummary } from "@evener/appwire-client";
import { describe, expect, test } from "vitest";
import { type AttentionEntry, detectFires, levelFromState, snapshotFromNavigation } from "./attention";

function row(
  overrides: Partial<NavigationSessionSummary> & Pick<NavigationSessionSummary, "ref" | "state">,
): NavigationSessionSummary {
  const base: NavigationSessionSummary = {
    ref: overrides.ref,
    host_id: "local",
    session_id: overrides.ref.replace(/^local:/, ""),
    title: overrides.ref,
    project: "proj",
    state: overrides.state,
    kind: "session",
    live: true,
    children: [],
  };
  return { ...base, ...overrides };
}

describe("levelFromState", () => {
  test("active is working", () => expect(levelFromState("active")).toBe("working"));
  test("awaiting is needs_you", () => expect(levelFromState("awaiting")).toBe("needs_you"));
  test("warning is needs_you", () => expect(levelFromState("warning")).toBe("needs_you"));
  test("errored is error", () => expect(levelFromState("errored")).toBe("error"));
  test("idle is idle", () => expect(levelFromState("idle")).toBe("idle"));
  test("ended is idle", () => expect(levelFromState("ended")).toBe("idle"));
  test("unknown is idle", () => expect(levelFromState("notLoaded")).toBe("idle"));
});

describe("snapshotFromNavigation", () => {
  test("null rows are an empty snapshot", () => expect(snapshotFromNavigation(null).size).toBe(0));
  test("keys by ref, carrying level + askPending + title", () => {
    const snap = snapshotFromNavigation([
      row({ ref: "local:a", state: "awaiting", title: "Ask A", ask_pending: true }),
      row({ ref: "local:b", state: "errored", title: "Err B" }),
    ]);
    expect(snap.get("local:a")).toEqual<AttentionEntry>({
      ref: "local:a",
      title: "Ask A",
      level: "needs_you",
      askPending: true,
      approvalPending: false,
      needsResponse: false,
    });
    expect(snap.get("local:b")).toEqual<AttentionEntry>({
      ref: "local:b",
      title: "Err B",
      level: "error",
      askPending: false,
      approvalPending: false,
      needsResponse: false,
    });
  });
  // An approval blocks its turn mid-tool, so the row reports "active"; like
  // the hub's promotedAttentionLevel, the approval promotes it to needs_you.
  test("an active row waiting on an approval enters as needs_you, carrying the approval", () => {
    const snap = snapshotFromNavigation([row({ ref: "local:p", state: "active", approval_pending: true })]);
    expect(snap.get("local:p")).toEqual<AttentionEntry>({
      ref: "local:p",
      title: "local:p",
      level: "needs_you",
      askPending: false,
      approvalPending: true,
      needsResponse: false,
    });
  });
  // Awaiting with no question is the needs_response rest, the same rule the
  // hub's attention entry uses; a plain reply rests idle and is absent.
  test("an awaiting row with no question carries needsResponse", () => {
    const snap = snapshotFromNavigation([
      row({ ref: "local:r", state: "awaiting" }),
      row({ ref: "local:i", state: "idle" }),
    ]);
    expect(snap.get("local:r")?.needsResponse).toBe(true);
    expect(snap.has("local:i")).toBe(false);
  });
  test("a failed row stays an error with an approval pending", () => {
    const snap = snapshotFromNavigation([row({ ref: "local:e", state: "errored", approval_pending: true })]);
    expect(snap.get("local:e")?.level).toBe("error");
  });
  test("warning maps into the needs_you level", () => {
    const snap = snapshotFromNavigation([row({ ref: "local:w", state: "warning" })]);
    expect(snap.get("local:w")?.level).toBe("needs_you");
  });
});

describe("detectFires", () => {
  const asks = "asks" as const;
  const all = "all" as const;
  function snap(...rows: NavigationSessionSummary[]): Map<string, AttentionEntry> {
    return snapshotFromNavigation(rows);
  }
  test("a ref newly in the tier is a transition into the alarming set", () => {
    expect(
      detectFires(snap(), snap(row({ ref: "local:a", state: "awaiting", ask_pending: true })), asks).map((e) => e.ref),
    ).toEqual(["local:a"]);
  });
  test("a ref already in the tier does not re-fire", () => {
    const a = row({ ref: "local:a", state: "awaiting", ask_pending: true });
    expect(detectFires(snap(a), snap(a), all)).toEqual([]);
  });
  test("error->needs_you within the tier does not fire", () => {
    expect(
      detectFires(
        snap(row({ ref: "local:a", state: "errored" })),
        snap(row({ ref: "local:a", state: "awaiting" })),
        all,
      ),
    ).toEqual([]);
  });
  test("dropping out of the tier does not fire", () => {
    expect(detectFires(snap(row({ ref: "local:a", state: "awaiting" })), snap(), all)).toEqual([]);
  });
  test("asks: a needs_response rest fires, like a question", () => {
    expect(
      detectFires(snap(), snap(row({ ref: "local:a", state: "awaiting", ask_pending: false })), asks).map((e) => e.ref),
    ).toEqual(["local:a"]);
  });
  test("asks: a warning is silent", () => {
    expect(detectFires(snap(), snap(row({ ref: "local:w", state: "warning" })), asks)).toEqual([]);
  });
  test("asks: an ask_pending transition fires", () => {
    expect(
      detectFires(snap(), snap(row({ ref: "local:a", state: "awaiting", ask_pending: true })), asks).map((e) => e.ref),
    ).toEqual(["local:a"]);
  });
  test("asks: an approval transition fires, like a question", () => {
    expect(
      detectFires(snap(), snap(row({ ref: "local:p", state: "active", approval_pending: true })), asks).map(
        (e) => e.ref,
      ),
    ).toEqual(["local:p"]);
  });
  test("asks: an error transition fires", () => {
    expect(detectFires(snap(), snap(row({ ref: "local:e", state: "errored" })), asks).map((e) => e.ref)).toEqual([
      "local:e",
    ]);
  });
  test("all: a warning fires", () => {
    expect(detectFires(snap(), snap(row({ ref: "local:w", state: "warning" })), all).map((e) => e.ref)).toEqual([
      "local:w",
    ]);
  });
  test("multiple simultaneous transitions each fire under all", () => {
    const a = row({ ref: "local:a", state: "awaiting" });
    expect(
      detectFires(
        snap(a),
        snap(a, row({ ref: "local:b", state: "awaiting" }), row({ ref: "local:c", state: "errored" })),
        all,
      )
        .map((e) => e.ref)
        .sort(),
    ).toEqual(["local:b", "local:c"]);
  });
});

test("restart-required sessions enter attention and preserve notification baseline", () => {
  const next = snapshotFromNavigation([row({ ref: "local:upgrade", state: "restartRequired" })]);
  expect(next.get("local:upgrade")?.level).toBe("needs_you");
  expect(detectFires(new Map(), next, "all").map((entry) => entry.ref)).toEqual(["local:upgrade"]);
  expect(detectFires(next, next, "all")).toEqual([]);
});
